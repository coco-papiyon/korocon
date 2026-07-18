package main

import (
	"bufio"
	"context"
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
	"syscall"
	"text/tabwriter"

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
var loadPullRequest = prworkflow.Load
var loadIssue = issueworkflow.Load

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
	fmt.Fprintf(stderr, "implementer: %s / %s / %s\nverifier: %s / %s / %s\nreviewer: %s / %s / %s\nconfig: %s\nworkspace: %s\nbranch: %s\nbase branch: %s\nimplementation directory: %s\nimplementation loops: %d\nstartup command: %s\nauto-approved commands: %d\nlog: %s\n", implementer.Provider, implementer.Model, aiBinaryName(implementer), verifier.Provider, verifier.Model, aiBinaryName(verifier), reviewer.Provider, reviewer.Model, aiBinaryName(reviewer), configPath, configured.WorkspaceName, configured.BranchNamePattern, configured.BaseBranch, configured.ImplementationDirectory, configured.ImplementationLoopCount, startupCommand, len(configured.BuiltinAllowedCommands), *logPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	selectionInput := in
	if _, isFile := in.(*os.File); !isFile {
		selectionInput = bufio.NewReader(in)
	}
	requested := requestedGitHubInformation{issueSpecified: issueSpecified, issueNumber: *issueNumber, prSpecified: prSpecified, prNumber: *prNumber}
	for {
		startupInput := selectionInput
		var selectedIssue *issueworkflow.Workflow
		var selectedPR *prworkflow.Workflow
		var err error
		if requested.issueSpecified || requested.prSpecified {
			selectedIssue, selectedPR, err = selectRequestedGitHubInformation(ctx, stdout, *dir, configured.WorkspaceName, requested)
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
			startupInput, selectedIssue, selectedPR, err = selectGitHubInformation(ctx, selectionInput, stdout, *dir, configured.WorkspaceName)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		err = func() error {
			activeAI := implementer
			if selectedPR != nil && selectedPR.Phase != prworkflow.PhaseConflict {
				activeAI = reviewer
			}
			initialPrompt := ""
			var initialJob *daemon.JobSpec
			var review *issueReviewController
			var implementationEngine *implementation.Engine
			var prController *prReviewController
			var fixEngine *prworkflow.FixEngine
			var runtimeCommand *prworkflow.RuntimeCommand
			if selectedIssue != nil {
				implementationEngine = implementation.New(implementation.Config{
					Provider: implementer.Provider, Binary: implementer.Binary,
					VerifierProvider: verifier.Provider, VerifierBinary: verifier.Binary, VerifierModel: verifier.Model,
					RepositoryDir: *dir, WorkspaceName: configured.WorkspaceName,
					ImplementationDirectory: configured.ImplementationDirectory,
					BranchNamePattern:       configured.BranchNamePattern, LoopCount: configured.ImplementationLoopCount,
					BaseBranch:  configured.BaseBranch,
					IssueNumber: selectedIssue.Issue.Number, IssueTitle: selectedIssue.Issue.Title,
					IssueContext: selectedIssue.Context(), LogOut: logFile, LogErr: logFile,
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
				review = newIssueReviewController(selectedIssue, selectedIssue.Phase, stderr, implementationJob, implementationEngine.Close)
				if selectedIssue.Phase == issueworkflow.PhaseImplementation {
					initialJob = review.InitialJob()
				} else {
					initialPrompt = review.InitialPrompt()
				}
			}
			if selectedPR != nil {
				fixEngine = prworkflow.NewFixEngine(prworkflow.FixConfig{
					Provider: implementer.Provider, Binary: implementer.Binary, Model: implementer.Model,
					RepositoryDir: *dir, ImplementationDirectory: configured.ImplementationDirectory,
					Number: selectedPR.PR.Number, Title: selectedPR.PR.Title,
					HeadRefName: selectedPR.PR.HeadRefName, BaseRefName: selectedPR.PR.BaseRefName,
					LogOut: logFile, LogErr: logFile,
				})
				selectedPR.SetFixPublisher(fixEngine.Publish)
				selectedPR.SetConflictPublisher(fixEngine.PublishConflict)
				defer fixEngine.Close()
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
				if configured.StartupCommand != "" {
					startVerification = func(ctx context.Context) (string, error) {
						worktree, err := fixEngine.Worktree(ctx)
						if err != nil {
							return "", fmt.Errorf("prepare PR worktree: %w", err)
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
				prController = newPRReviewController(selectedPR, stderr, fixJob, conflictJob, fixEngine.Close, startVerification, closeVerification)
				if selectedPR.Phase == prworkflow.PhaseConflict {
					initialJob = prController.InitialJob()
				} else {
					initialPrompt = prController.InitialPrompt()
				}
			}
			cfg := daemon.Config{
				Provider: activeAI.Provider, Binary: activeAI.Binary, Model: activeAI.Model,
				WorkingDir: *dir, AllowAllTools: *allowAllTools, StreamLogs: *streamLogs,
				LogOut: logFile, LogErr: logFile, StatusOut: stderr, ResultOut: stderr,
				InitialPrompt:   initialPrompt,
				InitialJob:      initialJob,
				AllowedCommands: configured.BuiltinAllowedCommands,
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
				return issueworkflow.SyncRepository(ctx, *dir)
			}
			if review != nil {
				cfg.OnJobStart = review.OnJobStart
				cfg.OnJobFinish = review.OnJobFinish
				cfg.HandleInput = review.HandleInput
			}
			if prController != nil {
				cfg.OnJobStart = prController.OnJobStart
				cfg.OnJobFinish = prController.OnJobFinish
				cfg.HandleInput = prController.HandleInput
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
	if model == "" {
		model = fallback.Model
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

func selectRequestedGitHubInformation(ctx context.Context, out io.Writer, workingDir, workspaceName string, requested requestedGitHubInformation) (*issueworkflow.Workflow, *prworkflow.Workflow, error) {
	if requested.issueSpecified {
		selected, err := loadIssue(ctx, workingDir, requested.issueNumber, workspaceName)
		if err != nil {
			return nil, nil, fmt.Errorf("Issue #%dの取得に失敗しました: %w", requested.issueNumber, err)
		}
		phaseName := "設計"
		if selected.Phase == issueworkflow.PhaseImplementation {
			phaseName = "実装"
		}
		if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), phaseName); err != nil {
			return nil, nil, err
		}
		return selected, nil, nil
	}
	if requested.prSpecified {
		selected, err := loadPullRequest(ctx, workingDir, requested.prNumber, workspaceName)
		if err != nil {
			return nil, nil, fmt.Errorf("PR #%dの取得に失敗しました: %w", requested.prNumber, err)
		}
		if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), pullRequestPhaseName(selected.Phase)); err != nil {
			return nil, nil, err
		}
		return nil, selected, nil
	}
	return nil, nil, errors.New("IssueまたはPRが指定されていません")
}

// selectGitHubInformation performs the small piece of setup that is needed
// before the resident AI process starts. Keeping this outside daemon.Run is
// important: the choice and issue number must not be sent as AI prompts.
func selectGitHubInformation(ctx context.Context, in io.Reader, out io.Writer, workingDir, workspaceName string) (io.Reader, *issueworkflow.Workflow, *prworkflow.Workflow, error) {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	for {
		if _, err := fmt.Fprint(out, "取得する情報を選択してください (issue/pr): "); err != nil {
			return nil, nil, nil, err
		}
		choice, err := readStringContext(ctx, reader)
		if err != nil && len(choice) == 0 {
			return nil, nil, nil, fmt.Errorf("GitHub情報の選択を読み取れません: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "1", "issue", "i":
			selected, err := selectIssue(ctx, reader, out, workingDir, workspaceName)
			if err != nil {
				return nil, nil, nil, err
			}
			return remainingInput(in, reader), selected, nil, nil
		case "2", "pr", "p":
			selected, err := selectPullRequest(ctx, reader, out, workingDir, workspaceName)
			if err != nil {
				return nil, nil, nil, err
			}
			return remainingInput(in, reader), nil, selected, nil
		default:
			if _, writeErr := fmt.Fprintln(out, "issue または pr を入力してください。"); writeErr != nil {
				return nil, nil, nil, writeErr
			}
		}
	}
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
	phaseName := "設計"
	if selected.Phase == issueworkflow.PhaseImplementation {
		phaseName = "実装"
	}
	if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), phaseName); err != nil {
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
	prs, err := listPullRequests(ctx, workingDir)
	if err != nil {
		return nil, fmt.Errorf("PR一覧の取得に失敗しました: %w", err)
	}
	prs = slices.DeleteFunc(prs, func(pr prworkflow.PullRequest) bool {
		return strings.EqualFold(strings.TrimSpace(pr.State), "MERGED") || pr.IsDraft
	})
	if len(prs) == 0 {
		return nil, errors.New("表示対象のPRがありません（MERGEDまたはDraftを除く）")
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	if _, err := fmt.Fprintln(out, "\nPR一覧:"); err != nil {
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
	number, err := strconv.Atoi(strings.TrimSpace(numberText))
	if err != nil || number < 1 {
		return nil, fmt.Errorf("PR番号が不正です: %q", strings.TrimSpace(numberText))
	}
	selected, err := loadPullRequest(ctx, workingDir, number, workspaceName)
	if err != nil {
		return nil, fmt.Errorf("PR #%dの取得に失敗しました: %w", number, err)
	}
	if _, err := fmt.Fprintf(out, "\n%s\n\n実行工程: %s\n", selected.Context(), pullRequestPhaseName(selected.Phase)); err != nil {
		return nil, err
	}
	return selected, nil
}

func pullRequestPhaseName(phase prworkflow.Phase) string {
	if phase == prworkflow.PhaseConflict {
		return "コンフリクト解消"
	}
	return "レビュー"
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
		return "オープン"
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
