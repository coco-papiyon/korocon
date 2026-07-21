package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	appconfig "github.com/coco-papiyon/korocon/internal/config"
	"github.com/coco-papiyon/korocon/internal/daemon"
	"github.com/coco-papiyon/korocon/internal/implementation"
	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
	"github.com/coco-papiyon/korocon/internal/runner"
)

const version = "0.1.0"

const defaultModel = "gpt-5.6-luna"

var listPullRequests = prworkflow.List
var listPullRequestsWithSearch = prworkflow.ListWithSearch
var listPullRequestsWithOptions = prworkflow.ListWithOptions
var loadPullRequest = prworkflow.Load
var listIssues = issueworkflow.List
var listIssuesWithSearch = issueworkflow.ListWithSearch
var listIssuesWithOptions = issueworkflow.ListWithOptions
var loadIssue = issueworkflow.Load
var currentGitHubUser = lookupCurrentGitHubUser
var loadProjectMembership = fetchProjectMembership

var errNoAutoTargets = errors.New("no automatic processing targets")

type selectionMode string

const (
	selectionModeDefault     selectionMode = ""
	selectionModeImplementer selectionMode = "implementer"
	selectionModeReviewer    selectionMode = "reviewer"
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value != "" {
		*f = append(*f, value)
	}
	return nil
}

type githubSelectionFilters struct {
	LabelIncludes []string
	LabelExcludes []string
	TitleContains []string
	Authors       []string
	Search        string
	ProjectNumber int
	ProjectOwner  string
	ProjectQuery  string
	ProjectItems  *projectMembership
}

type projectMembership struct {
	issueNumbers map[int]struct{}
	prNumbers    map[int]struct{}
	urls         map[string]struct{}
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "korocon:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return runInteractive(nil, os.Stdin, stdout, stderr)
	}
	if args[0] == "help" || args[0] == "--help" {
		printUsage(stdout)
		return nil
	}
	if strings.HasPrefix(args[0], "-") && args[0] != "--version" {
		return runInteractive(args, os.Stdin, stdout, stderr)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return nil
	case "doctor":
		return doctor(args[1:], stdout)
	case "config":
		return runConfig(args[1:], os.Stdin, stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "run":
		return runPrompt(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (try 'korocon help')", args[0])
	}
}

func runInteractive(args []string, in io.Reader, stdout, stderr io.Writer) error {
	configured, configPath, err := appconfig.Load()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("korocon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", configured.ImplementerProvider, "implementer provider (legacy alias)")
	binary := fs.String("binary", "", "provider executable (default: codex)")
	model := fs.String("model", configured.ImplementerModel, "implementer model (legacy alias)")
	implementerProvider := fs.String("implementer-provider", "", "implementer provider: codex or copilot")
	implementerModel := fs.String("implementer-model", "", "implementer model")
	verifierProvider := fs.String("verifier-provider", configured.VerifierProvider, "verifier provider: codex or copilot")
	verifierModel := fs.String("verifier-model", configured.VerifierModel, "verifier model")
	reviewerProvider := fs.String("reviewer-provider", configured.ReviewerProvider, "reviewer provider: codex or copilot")
	reviewerModel := fs.String("reviewer-model", configured.ReviewerModel, "reviewer model")
	dir := fs.String("dir", ".", "working directory")
	allowAllTools := fs.Bool("allow-all-tools", false, "allow all provider tools")
	streamLogs := fs.Bool("stream-logs", true, "stream AI stdout/stderr in real time (default: true for testing)")
	logPath := fs.String("log-file", "korocon.log", "AI stdout/stderr log file (default: korocon.log)")
	issueNumber := fs.Int("issue", 0, "start the specified issue workflow")
	prNumber := fs.Int("pr", 0, "start the specified pull request workflow")
	var implementerMode bool
	fs.BoolVar(&implementerMode, "implementer", false, "select issues and pull requests assigned to the implementer")
	fs.BoolVar(&implementerMode, "i", false, "shorthand for --implementer")
	var reviewerMode bool
	fs.BoolVar(&reviewerMode, "reviewer", false, "select unreviewed pull requests assigned to the reviewer")
	fs.BoolVar(&reviewerMode, "r", false, "shorthand for --reviewer")
	assignee := fs.String("assignee", "", "filter issues and pull requests by assignee (blank disables the filter)")
	var labelIncludes, labelExcludes, titleContains, authors stringListFlag
	fs.Var(&labelIncludes, "label", "require label (repeatable)")
	fs.Var(&labelExcludes, "exclude-label", "exclude label (repeatable)")
	fs.Var(&titleContains, "title", "require title substring (repeatable, OR)")
	fs.Var(&authors, "author", "filter by author (repeatable, OR)")
	search := fs.String("search", "", "GitHub issue/PR advanced search query")
	projectNumber := fs.Int("project", 0, "GitHub Projects v2 number")
	projectOwner := fs.String("project-owner", "@me", "GitHub project owner login or organization")
	projectStatus := fs.String("project-status", "", "GitHub Projects v2 Status value")
	projectQuery := fs.String("project-query", "", "GitHub Projects v2 filter query")
	autoMode := fs.Bool("auto", false, "process matching targets sequentially")
	if err := fs.Parse(args); err != nil {
		return err
	}
	issueSpecified, prSpecified := false, false
	fs.Visit(func(selected *flag.Flag) {
		switch selected.Name {
		case "issue":
			issueSpecified = true
		case "pr":
			prSpecified = true
		}
	})
	if issueSpecified && prSpecified {
		return errors.New("--issue and --pr cannot be specified together")
	}
	if implementerMode && reviewerMode {
		return errors.New("--implementer (-i) and --reviewer (-r) cannot be specified together")
	}
	mode := selectionModeDefault
	if implementerMode {
		mode = selectionModeImplementer
	} else if reviewerMode {
		mode = selectionModeReviewer
	}
	if mode == selectionModeReviewer && issueSpecified {
		return errors.New("--reviewer cannot be used with --issue")
	}
	if *autoMode && mode == selectionModeDefault {
		return errors.New("--auto requires --implementer (-i) or --reviewer (-r)")
	}
	if *autoMode && (issueSpecified || prSpecified) {
		return errors.New("--auto cannot be used with --issue or --pr")
	}
	assigneeFilter := strings.TrimSpace(*assignee)
	assigneeSpecified := false
	fs.Visit(func(selected *flag.Flag) {
		if selected.Name == "assignee" {
			assigneeSpecified = true
		}
	})
	if !assigneeSpecified {
		assigneeFilter, err = currentGitHubUser(context.Background(), *dir)
		if err != nil {
			return fmt.Errorf("current GitHub user: %w", err)
		}
		assigneeFilter = strings.TrimSpace(assigneeFilter)
	}
	if *projectNumber < 0 {
		return errors.New("--project must be zero or greater")
	}
	if *projectNumber == 0 && (strings.TrimSpace(*projectStatus) != "" || strings.TrimSpace(*projectQuery) != "") {
		return errors.New("--project-status and --project-query require --project")
	}
	resolvedProjectQuery := buildProjectQuery(*projectStatus, *projectQuery)
	filters := githubSelectionFilters{
		LabelIncludes: labelIncludes, LabelExcludes: labelExcludes,
		TitleContains: titleContains, Authors: authors,
		Search: strings.TrimSpace(*search), ProjectNumber: *projectNumber,
		ProjectOwner: strings.TrimSpace(*projectOwner), ProjectQuery: resolvedProjectQuery,
	}
	implementer, err := resolveAISelection(*provider, *model, aiSelection{Provider: "codex", Model: defaultModel})
	if err != nil {
		return fmt.Errorf("implementer AI: %w", err)
	}
	if strings.TrimSpace(*implementerProvider) != "" {
		implementer, err = resolveAISelection(*implementerProvider, valueOrFallback(*implementerModel, implementer.Model), implementer)
	} else if strings.TrimSpace(*implementerModel) != "" {
		implementer.Model = strings.TrimSpace(*implementerModel)
	}
	if err != nil {
		return fmt.Errorf("implementer AI: %w", err)
	}
	verifier, err := resolveAISelection(*verifierProvider, *verifierModel, implementer)
	if err != nil {
		return fmt.Errorf("verifier AI: %w", err)
	}
	reviewer, err := resolveAISelection(*reviewerProvider, *reviewerModel, implementer)
	if err != nil {
		return fmt.Errorf("reviewer AI: %w", err)
	}
	implementer.Binary = strings.TrimSpace(*binary)
	if verifier.Provider == implementer.Provider {
		verifier.Binary = implementer.Binary
	}
	if reviewer.Provider == implementer.Provider {
		reviewer.Binary = implementer.Binary
	}
	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file %q: %w", *logPath, err)
	}
	defer logFile.Close()
	startupCommand := configured.StartupCommand
	if startupCommand == "" {
		startupCommand = "未設定"
	}
	githubReviewer := configured.Reviewer
	if githubReviewer == "" {
		githubReviewer = "未設定"
	}
	autoPollingInterval, err := time.ParseDuration(configured.AutoPollingInterval)
	if err != nil {
		return fmt.Errorf("auto polling interval: %w", err)
	}
	if err := writeStartupSummary(stderr, implementer, verifier, reviewer, githubReviewer, configured.BranchNamePattern, configured.BaseBranch, startupCommand); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	selectionInput := in
	if _, isFile := in.(*os.File); !isFile {
		selectionInput = bufio.NewReader(in)
	}
	var autoInput *autoPollingInput
	if *autoMode {
		autoInput = newAutoPollingInput(selectionInput)
		selectionInput = autoInput
	}
	selectionDisplay := daemon.NewSystemOutput(stdout)
	requested := requestedGitHubInformation{issueSpecified: issueSpecified, issueNumber: *issueNumber, prSpecified: prSpecified, prNumber: *prNumber}
	for {
		activeFilters := filters
		if activeFilters.ProjectNumber > 0 {
			activeFilters.ProjectItems, err = loadProjectMembership(ctx, *dir, activeFilters.ProjectNumber, activeFilters.ProjectOwner, activeFilters.ProjectQuery)
			if err != nil {
				return fmt.Errorf("GitHub Projectの取得に失敗しました: %w", err)
			}
		}
		startupInput := selectionInput
		var selectedIssue *issueworkflow.Workflow
		var selectedPR *prworkflow.Workflow
		var err error
		if *autoMode {
			selectedIssue, selectedPR, err = selectAutoGitHubInformation(ctx, selectionDisplay, *dir, configured.WorkspaceName, mode, assigneeFilter, activeFilters)
			if errors.Is(err, errNoAutoTargets) {
				if waitErr := waitForAutoPolling(ctx, stdout, configured.AutoPollingInterval, autoPollingInterval, autoInput); waitErr != nil {
					if errors.Is(waitErr, context.Canceled) {
						return nil
					}
					return waitErr
				}
				continue
			}
		} else if requested.issueSpecified || requested.prSpecified {
			selectedIssue, selectedPR, err = selectRequestedGitHubInformationWithFilters(ctx, selectionDisplay, *dir, configured.WorkspaceName, requested, assigneeFilter, activeFilters)
			requested = requestedGitHubInformation{}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				if _, writeErr := fmt.Fprintf(stdout, "指定された対象を取得できませんでした: %v\n通常の選択へ戻ります。\n", err); writeErr != nil {
					return writeErr
				}
				continue
			}
		} else {
			startupInput, selectedIssue, selectedPR, err = selectGitHubInformation(ctx, selectionInput, selectionDisplay, *dir, configured.WorkspaceName, mode, assigneeFilter, activeFilters)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		err = func() error {
			activeAI := implementer
			workingDir := *dir
			if selectedPR != nil {
				activeAI = pullRequestAI(selectedPR.Phase, implementer, reviewer)
			}
			initialPrompt := ""
			var initialJob *daemon.JobSpec
			var review *issueReviewController
			var implementationEngine *implementation.Engine
			var prController *prReviewController
			var fixEngine *prworkflow.FixEngine
			var runtimeCommand *prworkflow.RuntimeCommand
			displayOut := daemon.NewSystemOutput(stderr)
			if selectedIssue != nil {
				implementationEngine = implementation.New(implementation.Config{
					Provider: implementer.Provider, Binary: implementer.Binary,
					VerifierProvider: verifier.Provider, VerifierBinary: verifier.Binary, VerifierModel: verifier.Model,
					RepositoryDir: *dir, WorkspaceName: configured.WorkspaceName,
					ImplementationDirectory: configured.ImplementationDirectory,
					BranchNamePattern:       configured.BranchNamePattern, LoopCount: configured.ImplementationLoopCount,
					BaseBranch:  configured.BaseBranch,
					IssueNumber: selectedIssue.Issue.Number, IssueTitle: selectedIssue.Issue.Title,
					IssueContext: selectedIssue.Context(), Reviewer: configured.Reviewer,
					LogOut: logFile, LogErr: logFile,
				})
				selectedIssue.SetImplementationPublisher(implementationEngine.Publish)
				defer implementationEngine.Close()
				implementationJob := func(prompt string) *daemon.JobSpec {
					return &daemon.JobSpec{
						Prompt: prompt,
						Execute: func(ctx context.Context, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
							return implementationEngine.Run(ctx, prompt, model, handler, setPhase, onEvent)
						},
					}
				}
				review = newIssueReviewController(selectedIssue, selectedIssue.Phase, displayOut, implementationJob, implementationEngine.Close)
				review.SetResetImplementation(implementationEngine.Reset)
				if selectedIssue.Phase == issueworkflow.PhaseImplementation {
					initialJob = review.InitialJob()
				} else {
					initialPrompt = review.InitialPrompt()
				}
			}
			if selectedPR != nil {
				if selectedPR.Phase == prworkflow.PhaseFix {
					feedbackPath, feedback, err := selectedPR.SaveReviewFeedback(ctx)
					if err != nil {
						return fmt.Errorf("レビュー指摘内容の取得に失敗しました: %w", err)
					}
					if _, err := fmt.Fprintf(displayOut, "%s\n", strings.TrimSpace(feedback)); err != nil {
						return err
					}
					if err := daemon.SystemMessage(displayOut, fmt.Sprintf("保存先: %s\nレビュー指摘内容を確認してください。すべて修正する場合は未入力状態でEnter、修正対象を選ぶ場合は修正する指摘と修正不要な指摘を入力してください。", feedbackPath)); err != nil {
						return err
					}
				}
				fixEngine = prworkflow.NewFixEngine(prworkflow.FixConfig{
					Provider: implementer.Provider, Binary: implementer.Binary, Model: implementer.Model,
					VerifierProvider: verifier.Provider, VerifierBinary: verifier.Binary, VerifierModel: verifier.Model,
					RepositoryDir: *dir, ImplementationDirectory: configured.ImplementationDirectory,
					WorkspaceName: configured.WorkspaceName, LoopCount: configured.ImplementationLoopCount,
					Number: selectedPR.PR.Number, Title: selectedPR.PR.Title,
					HeadRefName: selectedPR.PR.HeadRefName, BaseRefName: selectedPR.PR.BaseRefName,
					LogOut: logFile, LogErr: logFile,
				})
				selectedPR.SetFixPublisher(fixEngine.Publish)
				selectedPR.SetConflictPublisher(fixEngine.PublishConflict)
				defer fixEngine.Close()
				if pullRequestUsesReviewerWorktree(selectedPR.Phase) {
					workingDir, err = fixEngine.PrepareWorktree(ctx)
					if err != nil {
						return fmt.Errorf("prepare PR review worktree: %w", err)
					}
				}
				fixJob := func(prompt string) *daemon.JobSpec {
					return &daemon.JobSpec{Prompt: prompt, Execute: func(ctx context.Context, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
						return fixEngine.Run(ctx, prompt, model, handler, setPhase, onEvent)
					}}
				}
				conflictJob := func(prompt string) *daemon.JobSpec {
					return &daemon.JobSpec{Prompt: prompt, Execute: func(ctx context.Context, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
						return fixEngine.RunConflict(ctx, prompt, model, handler, setPhase, onEvent)
					}}
				}
				var startVerification func(context.Context) (string, error)
				var closeVerification func() error
				if configured.RuntimeVerificationEnabled {
					startVerification = func(ctx context.Context) (string, error) {
						worktree, err := fixEngine.PrepareWorktree(ctx)
						if err != nil {
							return "", fmt.Errorf("prepare PR worktree: %w", err)
						}
						if strings.TrimSpace(configured.StartupCommand) == "" {
							return "", nil
						}
						runtimeCommand = prworkflow.NewRuntimeCommand(configured.StartupCommand, worktree, logFile)
						return runtimeCommand.Start(ctx)
					}
					closeVerification = func() error {
						if runtimeCommand == nil {
							return nil
						}
						return runtimeCommand.Close()
					}
					defer closeVerification()
				}
				prController = newPRReviewController(selectedPR, displayOut, fixJob, conflictJob, fixEngine.Close, startVerification, closeVerification)
				prController.SetResetJob(fixEngine.Reset)
				if job := prController.InitialJob(); job != nil {
					initialJob = job
				} else {
					initialPrompt = prController.InitialPrompt()
				}
			}
			cfg := daemon.Config{
				Provider: activeAI.Provider, Binary: activeAI.Binary, Model: activeAI.Model,
				WorkingDir: workingDir, AllowAllTools: *allowAllTools, StreamLogs: *streamLogs,
				LogOut: logFile, LogErr: logFile, StatusOut: displayOut, ResultOut: displayOut,
				InitialPrompt:   initialPrompt,
				InitialJob:      initialJob,
				AllowedCommands: configured.BuiltinAllowedCommands,
				AllowedPaths:    configured.BuiltinAllowedPaths,
			}
			cfg.AddAllowedCommand = func(command string) error {
				updated, _ := appconfig.AddBuiltinAllowedCommand(configured, command)
				if err := appconfig.Save(configPath, updated); err != nil {
					return err
				}
				configured = updated
				return nil
			}
			cfg.BeforeJob = func(ctx context.Context, _ uint64, _ string) error {
				if err := issueworkflow.SyncRepository(ctx, *dir); err != nil {
					return err
				}
				if selectedPR != nil && pullRequestUsesReviewerWorktree(selectedPR.Phase) {
					_, err := fixEngine.PrepareWorktree(ctx)
					return err
				}
				return nil
			}
			if review != nil {
				cfg.OnJobStart = review.OnJobStart
				cfg.OnJobFinish = review.OnJobFinish
				cfg.HandleInput = review.HandleInput
				cfg.OnModelChange = review.OnModelChange
			}
			if prController != nil {
				cfg.OnJobStart = prController.OnJobStart
				cfg.OnJobFinish = prController.OnJobFinish
				cfg.HandleInput = prController.HandleInput
				cfg.OnModelChange = prController.OnModelChange
			}
			return daemon.Run(ctx, startupInput, stdout, cfg)
		}()
		if errors.Is(err, daemon.ErrRestart) {
			selectionInput = startupInput
			continue
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func waitForAutoPolling(ctx context.Context, out io.Writer, displayInterval string, interval time.Duration, inputs ...io.Reader) error {
	if _, err := fmt.Fprintf(out, "フィルタに一致する自動処理対象がありません。Enterで再取得、%s後に再取得します。\n", displayInterval); err != nil {
		return err
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var input <-chan string
	var inputErr <-chan error
	if len(inputs) > 0 {
		if lifecycle, ok := inputs[0].(interface {
			beginWait()
			endWait()
		}); ok {
			lifecycle.beginWait()
			defer lifecycle.endWait()
		}
	}
	startInput := func() {
		if len(inputs) == 0 || inputs[0] == nil {
			return
		}
		if lineInput, ok := inputs[0].(interface {
			nextLine(context.Context) (string, error)
		}); ok {
			lines := make(chan string, 1)
			errs := make(chan error, 1)
			go func() {
				line, err := lineInput.nextLine(waitCtx)
				if err != nil {
					errs <- err
					return
				}
				lines <- line
			}()
			input, inputErr = lines, errs
		} else {
			lines := make(chan string, 1)
			go func() {
				line, err := bufio.NewReader(inputs[0]).ReadString('\n')
				if err != nil {
					return
				}
				lines <- line
			}()
			input = lines
		}
	}
	startInput()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case line := <-input:
			if strings.TrimSpace(line) == "" {
				return nil
			}
			startInput()
		case err := <-inputErr:
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			input, inputErr = nil, nil
		}
	}
}

type autoPollingInput struct {
	mu          sync.Mutex
	waiting     bool
	waitLines   []autoPollingLine
	normalLines []autoPollingLine
	notify      chan struct{}
	pending     []byte
	terminalErr error
}

type autoPollingLine struct {
	line string
	err  error
}

func newAutoPollingInput(in io.Reader) *autoPollingInput {
	input := &autoPollingInput{notify: make(chan struct{}, 1)}
	go func() {
		reader := bufio.NewReader(in)
		for {
			line, err := reader.ReadString('\n')
			input.mu.Lock()
			if input.waiting {
				input.waitLines = append(input.waitLines, autoPollingLine{line: line, err: err})
			} else {
				input.normalLines = append(input.normalLines, autoPollingLine{line: line, err: err})
			}
			input.mu.Unlock()
			select {
			case input.notify <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	return input
}

func (in *autoPollingInput) beginWait() {
	in.mu.Lock()
	in.waiting = true
	in.waitLines = nil
	in.normalLines = nil
	in.mu.Unlock()
}

func (in *autoPollingInput) endWait() {
	in.mu.Lock()
	in.waiting = false
	in.waitLines = nil
	in.mu.Unlock()
}

func (in *autoPollingInput) nextLine(ctx context.Context) (string, error) {
	for {
		in.mu.Lock()
		if len(in.waitLines) > 0 {
			result := in.waitLines[0]
			in.waitLines = in.waitLines[1:]
			in.mu.Unlock()
			return result.line, result.err
		}
		in.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-in.notify:
		}
	}
}

func (in *autoPollingInput) Read(p []byte) (int, error) {
	if len(in.pending) == 0 {
		if in.terminalErr != nil {
			return 0, in.terminalErr
		}
		for {
			in.mu.Lock()
			if len(in.normalLines) > 0 {
				result := in.normalLines[0]
				in.normalLines = in.normalLines[1:]
				in.mu.Unlock()
				if result.err != nil {
					in.terminalErr = result.err
					if len(result.line) == 0 {
						return 0, result.err
					}
				}
				in.pending = []byte(result.line)
				break
			}
			in.mu.Unlock()
			<-in.notify
		}
	}
	n := copy(p, in.pending)
	in.pending = in.pending[n:]
	return n, nil
}

type requestedGitHubInformation struct {
	issueSpecified bool
	issueNumber    int
	prSpecified    bool
	prNumber       int
}

type aiSelection struct {
	Provider string
	Model    string
	Binary   string
}

func resolveAISelection(provider, model string, fallback aiSelection) (aiSelection, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = fallback.Provider
	}
	switch provider {
	case "codex":
	case "copilot", "github_copilot", "github-copilot":
		provider = "copilot"
	default:
		return aiSelection{}, fmt.Errorf("unsupported provider %q", provider)
	}
	model = strings.TrimSpace(model)
	if model == "" || (provider == "copilot" && strings.EqualFold(model, defaultModel)) {
		if provider == "copilot" {
			model = "auto"
		} else {
			model = fallback.Model
		}
	}
	return aiSelection{Provider: provider, Model: model}, nil
}

func valueOrFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func aiBinaryName(selection aiSelection) string {
	if strings.TrimSpace(selection.Binary) != "" {
		return selection.Binary
	}
	return selection.Provider
}

const startupSummaryFieldWidth = 15

func sameAISelection(left, right aiSelection) bool {
	return left.Provider == right.Provider && left.Model == right.Model && aiBinaryName(left) == aiBinaryName(right)
}

func writeStartupSummary(out io.Writer, implementer, verifier, reviewer aiSelection, githubReviewer, branch, baseBranch, startupCommand string) error {
	if _, err := fmt.Fprintln(out, "AI:"); err != nil {
		return err
	}
	if err := writeStartupSummaryField(out, "implementer", formatAISelection(implementer)); err != nil {
		return err
	}
	if !sameAISelection(implementer, verifier) {
		if err := writeStartupSummaryField(out, "verifier", formatAISelection(verifier)); err != nil {
			return err
		}
	}
	if !sameAISelection(implementer, reviewer) {
		if err := writeStartupSummaryField(out, "reviewer", formatAISelection(reviewer)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nGitHub:"); err != nil {
		return err
	}
	if err := writeStartupSummaryField(out, "github reviewer", githubReviewer); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "\nWorkflow:"); err != nil {
		return err
	}
	for _, field := range [][2]string{{"branch", branch}, {"base branch", baseBranch}, {"startup command", startupCommand}} {
		if err := writeStartupSummaryField(out, field[0], field[1]); err != nil {
			return err
		}
	}
	return nil
}

func writeStartupSummaryField(out io.Writer, name, value string) error {
	_, err := fmt.Fprintf(out, "  %-*s : %s\n", startupSummaryFieldWidth, name, value)
	return err
}

func formatAISelection(selection aiSelection) string {
	return fmt.Sprintf("%s / %s / %s", selection.Provider, selection.Model, aiBinaryName(selection))
}

func selectRequestedGitHubInformation(ctx context.Context, out io.Writer, workingDir, workspaceName string, requested requestedGitHubInformation, assigneeFilters ...string) (*issueworkflow.Workflow, *prworkflow.Workflow, error) {
	assigneeFilter := ""
	if len(assigneeFilters) > 0 {
		assigneeFilter = assigneeFilters[0]
	}
	return selectRequestedGitHubInformationWithFilters(ctx, out, workingDir, workspaceName, requested, assigneeFilter, githubSelectionFilters{})
}

func selectRequestedGitHubInformationWithFilters(ctx context.Context, out io.Writer, workingDir, workspaceName string, requested requestedGitHubInformation, assigneeFilter string, filters githubSelectionFilters) (*issueworkflow.Workflow, *prworkflow.Workflow, error) {
	if requested.issueSpecified {
		selected, err := loadIssue(ctx, workingDir, requested.issueNumber, workspaceName)
		if err != nil {
			return nil, nil, fmt.Errorf("Issue #%dの取得に失敗しました: %w", requested.issueNumber, err)
		}
		if !assignedTo(selected.Issue.Assignees, assigneeFilter) {
			return nil, nil, fmt.Errorf("Issue #%dは担当者 %q が割り当てられていません", requested.issueNumber, assigneeFilter)
		}
		if !matchesIssueFilters(selected.Issue, filters) {
			return nil, nil, fmt.Errorf("Issue #%dは指定されたフィルタ条件に一致しません", requested.issueNumber)
		}
		if err := writeSelectedIssue(out, selected, issuePhaseName(selected.Phase)); err != nil {
			return nil, nil, err
		}
		return selected, nil, nil
	}
	if requested.prSpecified {
		selected, err := loadPullRequest(ctx, workingDir, requested.prNumber, workspaceName)
		if err != nil {
			return nil, nil, fmt.Errorf("PR #%dの取得に失敗しました: %w", requested.prNumber, err)
		}
		if !assignedToPR(selected.PR.Assignees, assigneeFilter) {
			return nil, nil, fmt.Errorf("PR #%dは担当者 %q が割り当てられていません", requested.prNumber, assigneeFilter)
		}
		if !matchesPullRequestFilters(selected.PR, filters) {
			return nil, nil, fmt.Errorf("PR #%dは指定されたフィルタ条件に一致しません", requested.prNumber)
		}
		if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), pullRequestPhaseName(selected.Phase)); err != nil {
			return nil, nil, err
		}
		if selected.Phase == prworkflow.PhaseReviewApproved {
			if err := daemon.SystemMessage(out, "レビュー指摘承認済みです。未入力状態でEnterを押すとIssue/PR選択へ戻ります。文字を入力すると再レビューを実行します。"); err != nil {
				return nil, nil, err
			}
		}
		return nil, selected, nil
	}
	return nil, nil, errors.New("IssueまたはPRが指定されていません")
}

func selectAutoGitHubInformation(ctx context.Context, out io.Writer, workingDir, workspaceName string, mode selectionMode, assigneeFilter string, filters githubSelectionFilters) (*issueworkflow.Workflow, *prworkflow.Workflow, error) {
	if mode == selectionModeImplementer {
		issues, err := listIssuesForSelection(ctx, workingDir, filters.Search)
		if err != nil {
			return nil, nil, fmt.Errorf("Issue一覧の取得に失敗しました: %w", err)
		}
		issues = slices.DeleteFunc(issues, func(issue issueworkflow.Issue) bool {
			return issueIsRunning(issue) || !issueIsImplementerTarget(issue) || !assignedTo(issue.Assignees, assigneeFilter) || !matchesIssueFilters(issue, filters)
		})
		if len(issues) > 0 {
			sort.Slice(issues, func(i, j int) bool { return issues[i].Number > issues[j].Number })
			number := issues[0].Number
			if _, err := fmt.Fprintf(out, "自動選択: Issue #%d %s\n", number, tableCell(issues[0].Title)); err != nil {
				return nil, nil, err
			}
			return selectRequestedGitHubInformationWithFilters(ctx, out, workingDir, workspaceName, requestedGitHubInformation{issueSpecified: true, issueNumber: number}, assigneeFilter, filters)
		}
	}

	prs, err := listPullRequestsForSelection(ctx, workingDir, filters.Search)
	if err != nil {
		return nil, nil, fmt.Errorf("PR一覧の取得に失敗しました: %w", err)
	}
	prs = slices.DeleteFunc(prs, func(pr prworkflow.PullRequest) bool {
		return pullRequestIsRunning(pr) || strings.EqualFold(strings.TrimSpace(pr.State), "MERGED") || pr.IsDraft || !pullRequestIsRoleTarget(pr, mode) || !assignedToPR(pr.Assignees, assigneeFilter) || !matchesPullRequestFilters(pr, filters)
	})
	if len(prs) == 0 {
		return nil, nil, errNoAutoTargets
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	number := prs[0].Number
	if _, err := fmt.Fprintf(out, "自動選択: PR #%d %s\n", number, tableCell(prs[0].Title)); err != nil {
		return nil, nil, err
	}
	return selectRequestedGitHubInformationWithFilters(ctx, out, workingDir, workspaceName, requestedGitHubInformation{prSpecified: true, prNumber: number}, assigneeFilter, filters)
}

// selectGitHubInformation performs the small piece of setup that is needed
// before the resident AI process starts. Keeping this outside daemon.Run is
// important: the choice and issue number must not be sent as AI prompts.
func selectGitHubInformation(ctx context.Context, in io.Reader, out io.Writer, workingDir, workspaceName string, mode selectionMode, assigneeFilter string, optionalFilters ...githubSelectionFilters) (io.Reader, *issueworkflow.Workflow, *prworkflow.Workflow, error) {
	filters := githubSelectionFilters{}
	if len(optionalFilters) > 0 {
		filters = optionalFilters[0]
	}
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	if mode == selectionModeReviewer {
		selected, err := selectPullRequestForRole(ctx, reader, out, workingDir, workspaceName, mode, assigneeFilter, filters)
		if err != nil {
			return nil, nil, nil, err
		}
		return remainingInput(in, reader), nil, selected, nil
	}
	for {
		prompt := "取得する情報を選択してください (ISSUE/PR): "
		if mode == selectionModeImplementer {
			prompt = "実装者が担当する対象を選択してください (ISSUE/PR): "
		}
		if _, err := fmt.Fprint(out, prompt); err != nil {
			return nil, nil, nil, err
		}
		choice, err := readStringContext(ctx, reader)
		if err != nil && len(choice) == 0 {
			return nil, nil, nil, fmt.Errorf("GitHub情報の選択を読み取れません: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "1", "issue", "i":
			selected, err := selectIssueForRole(ctx, reader, out, workingDir, workspaceName, mode, assigneeFilter, filters)
			if err != nil {
				if _, writeErr := fmt.Fprintf(out, "%v\n", err); writeErr != nil {
					return nil, nil, nil, writeErr
				}
				continue
			}
			return remainingInput(in, reader), selected, nil, nil
		case "2", "pr", "p":
			selected, err := selectPullRequestForRole(ctx, reader, out, workingDir, workspaceName, mode, assigneeFilter, filters)
			if err != nil {
				if _, writeErr := fmt.Fprintf(out, "%v\n", err); writeErr != nil {
					return nil, nil, nil, writeErr
				}
				continue
			}
			return remainingInput(in, reader), nil, selected, nil
		default:
			if _, writeErr := fmt.Fprintln(out, "ISSUE または PR を入力してください。"); writeErr != nil {
				return nil, nil, nil, writeErr
			}
		}
	}
}

func selectIssueForRole(ctx context.Context, reader *bufio.Reader, out io.Writer, workingDir, workspaceName string, mode selectionMode, assigneeFilter string, optionalFilters ...githubSelectionFilters) (*issueworkflow.Workflow, error) {
	filters := githubSelectionFilters{}
	if len(optionalFilters) > 0 {
		filters = optionalFilters[0]
	}
	if mode != selectionModeImplementer {
		selected, err := selectIssue(ctx, reader, out, workingDir, workspaceName)
		if err != nil {
			return nil, err
		}
		if !assignedTo(selected.Issue.Assignees, assigneeFilter) {
			return nil, fmt.Errorf("Issue #%dは担当者 %q が割り当てられていません", selected.Issue.Number, assigneeFilter)
		}
		if !matchesIssueFilters(selected.Issue, filters) {
			return nil, fmt.Errorf("Issue #%dは指定されたフィルタ条件に一致しません", selected.Issue.Number)
		}
		return selected, nil
	}
	issues, err := listIssuesForSelection(ctx, workingDir, filters.Search)
	if err != nil {
		return nil, fmt.Errorf("Issue一覧の取得に失敗しました: %w", err)
	}
	issues = slices.DeleteFunc(issues, func(issue issueworkflow.Issue) bool {
		return !issueIsImplementerTarget(issue) || !assignedTo(issue.Assignees, assigneeFilter) || !matchesIssueFilters(issue, filters)
	})
	if len(issues) == 0 {
		return nil, errors.New("実装者が担当するIssueがありません")
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number > issues[j].Number })
	if _, err := fmt.Fprintln(out, "\n実装者担当Issue一覧:"); err != nil {
		return nil, err
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "番号\t状態\tタイトル"); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(table, "----\t----\t--------"); err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\n", issue.Number, issueStatus(issue), tableCell(issue.Title)); err != nil {
			return nil, err
		}
	}
	if err := table.Flush(); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprint(out, "\nIssue番号を入力してください: "); err != nil {
		return nil, err
	}
	numberText, err := readStringContext(ctx, reader)
	if err != nil && len(numberText) == 0 {
		return nil, fmt.Errorf("Issue番号を読み取れません: %w", err)
	}
	numberText = strings.TrimSpace(numberText)
	number := issues[0].Number
	if numberText != "" {
		number, err = strconv.Atoi(numberText)
		if err != nil || number < 1 {
			return nil, fmt.Errorf("Issue番号が不正です: %q", numberText)
		}
	}
	selected, err := loadIssue(ctx, workingDir, number, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("Issue #%dの取得に失敗しました: %w", number, err)
	}
	err = writeSelectedIssue(out, selected, issuePhaseName(selected.Phase))
	return selected, err
}

func issueIsImplementerTarget(issue issueworkflow.Issue) bool {
	for _, label := range issue.Labels {
		switch strings.ToLower(strings.TrimSpace(label.Name)) {
		case "state:design_ready", "state:implementation_ready", "state:implementation_approved", "state:pr_created":
			return false
		}
	}
	return true
}

func issueIsRunning(issue issueworkflow.Issue) bool {
	for _, label := range issue.Labels {
		switch strings.ToLower(strings.TrimSpace(label.Name)) {
		case "state:design_running", "state:implementation_running":
			return true
		}
	}
	return false
}

func issueStatus(issue issueworkflow.Issue) string {
	for _, label := range issue.Labels {
		name := strings.ToLower(strings.TrimSpace(label.Name))
		if status, ok := map[string]string{
			"state:design_approved":        "実装待ち",
			"state:implementation_running": "実装中",
			"state:implementation_ready":   "実装完了・承認待ち",
			"state:design_failed":          "設計失敗・再実行待ち",
			"state:implementation_failed":  "実装失敗・再実行待ち",
			"state:failed":                 "失敗・再実行待ち",
			"state:review_fix":             "レビュー修正",
			"state:pr_review_comment":      "PRレビュー指摘あり",
		}[name]; ok {
			return status
		}
	}
	return "設計"
}

func issuePhaseName(phase issueworkflow.Phase) string {
	switch phase {
	case issueworkflow.PhaseImplementation, issueworkflow.PhaseImplementationReady:
		return "実装"
	case issueworkflow.PhaseImplementationFailed:
		return "実装"
	default:
		return "設計"
	}
}

func writeSelectedIssue(out io.Writer, selected *issueworkflow.Workflow, phaseName string) error {
	if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), phaseName); err != nil {
		return err
	}
	if selected.Phase != issueworkflow.PhaseDesignReady && selected.Phase != issueworkflow.PhaseImplementationReady {
		return nil
	}
	result := selected.PendingApprovalResult()
	if strings.TrimSpace(result) == "" {
		return daemon.SystemMessage(out, fmt.Sprintf("保存済みの%s結果がないため、Issue #%dの%sを再実行します。", phaseName, selected.Issue.Number, phaseName))
	}
	if _, err := fmt.Fprintf(out, "%s\n", strings.TrimRight(result, "\n")); err != nil {
		return err
	}
	return daemon.SystemMessage(out, fmt.Sprintf("%sが完了しました。承認する場合は未入力状態でEnter、もしくは承認、approve、aのいずれかを入力してください。\n修正する場合は内容を入力してください。AIへ送信して再%sします。", phaseName, phaseName))
}

func listIssuesForSelection(ctx context.Context, workingDir, search string) ([]issueworkflow.Issue, error) {
	if strings.TrimSpace(search) == "" {
		return listIssues(ctx, workingDir)
	}
	return listIssuesWithSearch(ctx, workingDir, search)
}

func listPullRequestsForSelection(ctx context.Context, workingDir, search string) ([]prworkflow.PullRequest, error) {
	if strings.TrimSpace(search) == "" {
		return listPullRequests(ctx, workingDir)
	}
	return listPullRequestsWithSearch(ctx, workingDir, search)
}

func matchesIssueFilters(issue issueworkflow.Issue, filters githubSelectionFilters) bool {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.Name)
	}
	return matchesCommonFilters(issue.Title, labels, issue.Author.Login, filters) && matchesProjectItem(issue.URL, issue.Number, true, filters.ProjectItems)
}

func matchesPullRequestFilters(pr prworkflow.PullRequest, filters githubSelectionFilters) bool {
	labels := make([]string, 0, len(pr.Labels))
	for _, label := range pr.Labels {
		labels = append(labels, label.Name)
	}
	return matchesCommonFilters(pr.Title, labels, pr.Author.Login, filters) && matchesProjectItem(pr.URL, pr.Number, false, filters.ProjectItems)
}

func matchesCommonFilters(title string, labels []string, author string, filters githubSelectionFilters) bool {
	labelSet := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		labelSet[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}
	for _, required := range filters.LabelIncludes {
		if _, ok := labelSet[strings.ToLower(strings.TrimSpace(required))]; !ok {
			return false
		}
	}
	for _, excluded := range filters.LabelExcludes {
		if _, ok := labelSet[strings.ToLower(strings.TrimSpace(excluded))]; ok {
			return false
		}
	}
	if len(filters.TitleContains) > 0 {
		matched := false
		for _, value := range filters.TitleContains {
			if strings.Contains(strings.ToLower(title), strings.ToLower(strings.TrimSpace(value))) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(filters.Authors) > 0 {
		matched := false
		for _, value := range filters.Authors {
			if strings.EqualFold(strings.TrimSpace(author), strings.TrimSpace(value)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func matchesProjectItem(url string, number int, issue bool, membership *projectMembership) bool {
	if membership == nil {
		return true
	}
	if url = strings.TrimSpace(url); url != "" {
		_, ok := membership.urls[url]
		return ok
	}
	if issue {
		_, ok := membership.issueNumbers[number]
		return ok
	}
	_, ok := membership.prNumbers[number]
	return ok
}

func assignedTo(assignees []issueworkflow.User, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	for _, assignee := range assignees {
		if strings.EqualFold(strings.TrimSpace(assignee.Login), filter) {
			return true
		}
	}
	return false
}

func assignedToPR(assignees []prworkflow.User, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	for _, assignee := range assignees {
		if strings.EqualFold(strings.TrimSpace(assignee.Login), filter) {
			return true
		}
	}
	return false
}

func remainingInput(original io.Reader, buffered *bufio.Reader) io.Reader {
	if buffered.Buffered() == 0 {
		return original
	}
	return buffered
}

func selectIssue(ctx context.Context, reader *bufio.Reader, out io.Writer, workingDir, workspaceName string) (*issueworkflow.Workflow, error) {
	if _, err := fmt.Fprint(out, "issue番号を入力してください: "); err != nil {
		return nil, err
	}
	numberText, err := readStringContext(ctx, reader)
	if err != nil && len(numberText) == 0 {
		return nil, fmt.Errorf("issue番号を読み取れません: %w", err)
	}
	numberText = strings.TrimSpace(numberText)
	number, err := strconv.Atoi(numberText)
	if err != nil || number < 1 {
		return nil, fmt.Errorf("issue番号が不正です: %q", numberText)
	}
	selected, err := loadIssue(ctx, workingDir, number, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("issue #%dの取得に失敗しました: %w", number, err)
	}
	if err := writeSelectedIssue(out, selected, issuePhaseName(selected.Phase)); err != nil {
		return nil, err
	}
	return selected, nil
}

type readStringResult struct {
	text string
	err  error
}

// bufio.Reader has no context-aware read. Run the blocking read separately so
// SIGINT can return from the startup selection before the daemon starts.
func readStringContext(ctx context.Context, reader *bufio.Reader) (string, error) {
	result := make(chan readStringResult, 1)
	go func() {
		text, err := reader.ReadString('\n')
		result <- readStringResult{text: text, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-result:
		return value.text, value.err
	}
}

func selectPullRequest(ctx context.Context, reader *bufio.Reader, out io.Writer, workingDir, workspaceName string) (*prworkflow.Workflow, error) {
	return selectPullRequestForRole(ctx, reader, out, workingDir, workspaceName, selectionModeDefault, "")
}

func selectPullRequestForRole(ctx context.Context, reader *bufio.Reader, out io.Writer, workingDir, workspaceName string, mode selectionMode, assigneeFilter string, optionalFilters ...githubSelectionFilters) (*prworkflow.Workflow, error) {
	filters := githubSelectionFilters{}
	if len(optionalFilters) > 0 {
		filters = optionalFilters[0]
	}
	prs, err := listPullRequestsForSelection(ctx, workingDir, filters.Search)
	if err != nil {
		return nil, fmt.Errorf("PR一覧の取得に失敗しました: %w", err)
	}
	prs = slices.DeleteFunc(prs, func(pr prworkflow.PullRequest) bool {
		if strings.EqualFold(strings.TrimSpace(pr.State), "MERGED") || pr.IsDraft {
			return true
		}
		return !pullRequestIsRoleTarget(pr, mode) || !assignedToPR(pr.Assignees, assigneeFilter) || !matchesPullRequestFilters(pr, filters)
	})
	if len(prs) == 0 {
		if mode == selectionModeReviewer {
			return nil, errors.New("レビューアが担当する未レビューPRがありません")
		}
		if mode == selectionModeImplementer {
			return nil, errors.New("実装者が担当するPRがありません")
		}
		return nil, errors.New("表示対象のPRがありません（MERGEDまたはDraftを除く）")
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	title := "\nPR一覧:"
	if mode == selectionModeImplementer {
		title = "\n実装者担当PR一覧:"
	} else if mode == selectionModeReviewer {
		title = "\nレビューア担当PR一覧（未レビュー）:"
	}
	if _, err := fmt.Fprintln(out, title); err != nil {
		return nil, err
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "番号\t状態\tタイトル"); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(table, "----\t----\t--------"); err != nil {
		return nil, err
	}
	for _, pr := range prs {
		status := pullRequestStatus(pr)
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\n", pr.Number, status, tableCell(pr.Title)); err != nil {
			return nil, err
		}
	}
	if err := table.Flush(); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprint(out, "\nPR番号を入力してください: "); err != nil {
		return nil, err
	}
	numberText, err := readStringContext(ctx, reader)
	if err != nil && len(numberText) == 0 {
		return nil, fmt.Errorf("PR番号を読み取れません: %w", err)
	}
	numberText = strings.TrimSpace(numberText)
	number := prs[0].Number
	if numberText != "" {
		number, err = strconv.Atoi(numberText)
		if err != nil || number < 1 {
			return nil, fmt.Errorf("PR番号が不正です: %q", numberText)
		}
	}
	selected, err := loadPullRequest(ctx, workingDir, number, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("PR #%dの取得に失敗しました: %w", number, err)
	}
	if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), pullRequestPhaseName(selected.Phase)); err != nil {
		return nil, err
	}
	if selected.Phase == prworkflow.PhaseReviewApproved {
		if err := daemon.SystemMessage(out, "レビュー指摘承認済みです。未入力状態でEnterを押すとIssue/PR選択へ戻ります。文字を入力すると再レビューを実行します。"); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func pullRequestIsRoleTarget(pr prworkflow.PullRequest, mode selectionMode) bool {
	switch mode {
	case selectionModeImplementer:
		return prworkflow.HasConflict(pr) || prworkflow.PullRequestHasLabel(pr, "state:pr_conflict") || prworkflow.PullRequestHasLabel(pr, "state:pr_review_comment") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_design_running") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_design_ready") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_design_approved") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_implementation_running") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_implementation_ready") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_implementation_approved") || prworkflow.PullRequestHasLabel(pr, "state:review_failed") || prworkflow.PullRequestHasLabel(pr, "state:review_fix_failed") || prworkflow.PullRequestHasLabel(pr, "state:pr_conflict_failed") || prworkflow.PullRequestHasLabel(pr, "state:failed")
	case selectionModeReviewer:
		return (!pullRequestHasStateLabel(pr) || prworkflow.PullRequestHasLabel(pr, "state:review_fixed")) && !prworkflow.HasConflict(pr) && !prworkflow.PullRequestHasLabel(pr, "state:pr_conflict")
	default:
		return true
	}
}

func pullRequestIsRunning(pr prworkflow.PullRequest) bool {
	for _, label := range pr.Labels {
		switch strings.ToLower(strings.TrimSpace(label.Name)) {
		case "state:review_running", "state:review_fix_design_running", "state:review_fix_implementation_running", "state:pr_conflict_running":
			return true
		}
	}
	return false
}

func pullRequestHasStateLabel(pr prworkflow.PullRequest) bool {
	for _, label := range pr.Labels {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(label.Name)), "state:") {
			return true
		}
	}
	return false
}

func pullRequestPhaseName(phase prworkflow.Phase) string {
	switch phase {
	case prworkflow.PhaseConflict:
		return "コンフリクト解消"
	case prworkflow.PhaseFix:
		return "レビュー指摘修正"
	case prworkflow.PhaseReviewApproved:
		return "レビュー指摘承認済み"
	case prworkflow.PhaseReviewFailed:
		return "レビュー失敗・再実行待ち"
	case prworkflow.PhaseFixFailed:
		return "レビュー修正失敗・再実行待ち"
	case prworkflow.PhaseConflictFailed:
		return "コンフリクト解消失敗・再実行待ち"
	default:
		return "レビュー"
	}
}

func pullRequestAI(phase prworkflow.Phase, implementer, reviewer aiSelection) aiSelection {
	if phase == prworkflow.PhaseReview || phase == prworkflow.PhaseReviewApproved || phase == prworkflow.PhaseVerification || phase == prworkflow.PhaseReviewFailed {
		return reviewer
	}
	return implementer
}

func pullRequestUsesReviewerWorktree(phase prworkflow.Phase) bool {
	switch phase {
	case prworkflow.PhaseReview, prworkflow.PhaseReviewApproved, prworkflow.PhaseVerification, prworkflow.PhaseReviewFailed:
		return true
	default:
		return false
	}
}

var pullRequestStateNames = map[string]string{
	"state:detected":                           "検出済み",
	"state:design_running":                     "設計実行中",
	"state:design_ready":                       "設計完了・承認待ち",
	"state:design_approved":                    "設計承認済み",
	"state:implementation_running":             "実装中",
	"state:implementation_ready":               "実装完了・承認待ち",
	"state:implementation_approved":            "実装承認済み",
	"state:pr_created":                         "PR作成済み",
	"state:pr_review_comment":                  "レビュー修正指示あり",
	"state:pr_conflict":                        "コンフリクトあり",
	"state:pr_conflict_running":                "コンフリクト解消中",
	"state:pr_conflict_ready":                  "コンフリクト解消完了・承認待ち",
	"state:pr_conflict_resolved":               "コンフリクト解消済み",
	"state:review_fix_design_running":          "レビュー修正設計中",
	"state:review_fix_design_ready":            "レビュー修正設計完了・承認待ち",
	"state:review_fix_design_approved":         "レビュー修正設計承認済み",
	"state:review_fix_implementation_running":  "レビュー修正実装中",
	"state:review_fix_implementation_ready":    "レビュー修正実装完了・承認待ち",
	"state:review_fix_implementation_approved": "レビュー修正実装承認済み",
	"state:review_fixed":                       "レビュー修正済み",
	"state:review_running":                     "レビュー中",
	"state:review_ready":                       "レビュー完了・承認待ち",
	"state:review_approved":                    "レビュー承認済み",
	"state:review_failed":                      "レビュー失敗・再実行待ち",
	"state:review_fix_failed":                  "レビュー修正失敗・再実行待ち",
	"state:pr_conflict_failed":                 "コンフリクト解消失敗・再実行待ち",
	"state:completed":                          "完了",
	"state:failed":                             "失敗",
}

func pullRequestStatus(pr prworkflow.PullRequest) string {
	if prworkflow.HasConflict(pr) {
		return "コンフリクト"
	}
	for _, label := range pr.Labels {
		name := strings.ToLower(strings.TrimSpace(label.Name))
		if status, ok := pullRequestStateNames[name]; ok {
			return status
		}
		if strings.HasPrefix(name, "state:") {
			return strings.TrimPrefix(name, "state:")
		}
	}
	switch strings.ToUpper(strings.TrimSpace(pr.State)) {
	case "OPEN":
		return "未レビュー"
	case "CLOSED":
		return "クローズ"
	default:
		return strings.ToUpper(strings.TrimSpace(pr.State))
	}
}

func tableCell(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")), " ")
}

func showGitHubCommand(ctx context.Context, out io.Writer, workingDir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("GitHub情報の取得に失敗しました (gh %s): %w", strings.Join(args, " "), err)
	}
	return nil
}

func lookupCurrentGitHubUser(ctx context.Context, workingDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login")
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	login := strings.TrimSpace(string(output))
	if login == "" {
		return "", errors.New("gh api user returned an empty login")
	}
	return login, nil
}

func fetchProjectMembership(ctx context.Context, workingDir string, number int, owner, query string) (*projectMembership, error) {
	if number < 1 {
		return nil, errors.New("project number must be greater than zero")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "@me"
	}
	args := []string{"project", "item-list", strconv.Itoa(number), "--owner", owner, "--limit", "1000", "--format", "json"}
	if strings.TrimSpace(query) != "" {
		args = append(args, "--query", strings.TrimSpace(query))
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	var response struct {
		Items []struct {
			Content struct {
				Number int    `json:"number"`
				Type   string `json:"type"`
				URL    string `json:"url"`
			} `json:"content"`
		} `json:"items"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode GitHub Project items: %w", err)
	}
	membership := &projectMembership{issueNumbers: make(map[int]struct{}), prNumbers: make(map[int]struct{}), urls: make(map[string]struct{})}
	for _, item := range response.Items {
		content := item.Content
		if strings.TrimSpace(content.URL) != "" {
			membership.urls[strings.TrimSpace(content.URL)] = struct{}{}
		}
		switch strings.ToLower(strings.TrimSpace(content.Type)) {
		case "issue":
			membership.issueNumbers[content.Number] = struct{}{}
		case "pullrequest", "pull request":
			membership.prNumbers[content.Number] = struct{}{}
		}
	}
	return membership, nil
}

func buildProjectQuery(status, query string) string {
	status = strings.TrimSpace(status)
	query = strings.TrimSpace(query)
	if status == "" {
		return query
	}
	escapedStatus := strings.ReplaceAll(strings.ReplaceAll(status, `\`, `\\`), `"`, `\"`)
	statusQuery := `status:"` + escapedStatus + `"`
	if query == "" {
		return statusQuery
	}
	return statusQuery + " " + query
}

func runPrompt(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "codex", "AI CLI provider (default: codex)")
	binary := fs.String("binary", "", "provider executable (default: codex)")
	model := fs.String("model", defaultModel, "model: gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.4, or gpt-5.4-mini")
	dir := fs.String("dir", ".", "working directory")
	allowAllTools := fs.Bool("allow-all-tools", false, "allow all provider tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read prompt from stdin: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		return errors.New("prompt is required (argument or stdin)")
	}
	providerName := *provider
	if providerName == "" {
		providerName = "codex"
	}
	modelName := *model
	if modelName == "" {
		modelName = "(default)"
	}
	fmt.Fprintf(stderr, "実行（provider: %s, model: %s）\n", providerName, modelName)
	return runner.Run(context.Background(), runner.Request{
		Provider: *provider, Binary: *binary, Prompt: prompt, Model: *model,
		WorkingDir: *dir, AllowAllTools: *allowAllTools, Stdout: stdout, Stderr: stderr,
	})
}

func doctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binary := fs.String("binary", "codex", "provider executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := exec.LookPath(*binary)
	if err != nil {
		return fmt.Errorf("%s was not found on PATH; install it and run its login flow", *binary)
	}
	fmt.Fprintf(stdout, "%s: %s\n", *binary, path)
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `korocon - run AI CLIs from Go

Usage:
  korocon [options]
  korocon config init [--force]
  korocon config list
  korocon config model
  korocon config set <KEY> <VALUE>
  korocon config allow [COMMAND]
  korocon config allow-path [GLOB]
  korocon list issue [options]
  korocon list pr [options]
  korocon doctor [--binary codex]
  korocon version

Run options:
  --provider NAME       implementer provider (legacy alias)
  --binary PATH         executable (default: codex)
  --model NAME          implementer model (legacy alias)
  --implementer-provider NAME  implementer provider
  --implementer-model NAME     implementer model
  --verifier-provider NAME     verifier provider (default: implementer)
  --verifier-model NAME        verifier model (default: implementer)
  --reviewer-provider NAME     reviewer provider (default: implementer)
  --reviewer-model NAME        reviewer model (default: implementer)
  --dir PATH            provider working directory (default: .)
  --allow-all-tools     grant all provider tools
  --stream-logs         stream AI stdout/stderr in real time (currently on)
  --log-file PATH       AI stdout/stderr log file (default: korocon.log)
  --issue NUMBER        start the specified issue workflow
  --pr NUMBER           start the specified pull request workflow
  --implementer         select implementer-owned issues and pull requests
  -i                    shorthand for --implementer
  --reviewer            select only unreviewed pull requests for the reviewer
  -r                    shorthand for --reviewer
  --assignee USER       filter by assignee; omitted uses gh api user, blank disables filtering
  --label NAME          require a label; repeat to require all labels
  --exclude-label NAME  exclude a label; repeatable
  --title TEXT          require a title substring; repeated values use OR
  --author USER         filter by author; repeated values use OR
  --search QUERY        GitHub advanced issue/PR search query
  --project NUMBER      GitHub Projects v2 project number
  --project-owner OWNER project owner login or organization (default: @me)
  --project-status NAME GitHub Projects v2 Status value (requires --project)
  --project-query QUERY GitHub Projects v2 filter query (requires --project)
  --auto                process matching targets sequentially (requires -i or -r)

Interactive mode:
  Start one resident Codex process, send prompts through its stdin,
  and print each completed result. Ctrl+C stops the CLI and Codex.
  Issue selection starts design or implementation according to korobokcle
  state labels and leaves detailed instructions to repository skills.

Interactive commands:
  /model [NUMBER|NAME]  list or switch the model
  /approve              approve the pending Codex operation
  /allow                approve and add the command to automatic approvals
  /decline              decline the pending Codex operation
  /diff                 print the latest completed job's git diff
  /diff FILE            save that diff under the working directory
`)
}
