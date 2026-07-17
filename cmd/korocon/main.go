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
	"strconv"
	"strings"
	"syscall"

	appconfig "github.com/coco-papiyon/korocon/internal/config"
	"github.com/coco-papiyon/korocon/internal/daemon"
	"github.com/coco-papiyon/korocon/internal/implementation"
	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	"github.com/coco-papiyon/korocon/internal/runner"
)

const version = "0.1.0"

const defaultModel = "gpt-5.6-luna"

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
	provider := fs.String("provider", "codex", "AI CLI provider (default: codex)")
	binary := fs.String("binary", "", "provider executable (default: codex)")
	model := fs.String("model", defaultModel, "model: gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.4, or gpt-5.4-mini")
	dir := fs.String("dir", ".", "working directory")
	allowAllTools := fs.Bool("allow-all-tools", false, "allow all provider tools")
	streamLogs := fs.Bool("stream-logs", true, "stream AI stdout/stderr in real time (default: true for testing)")
	logPath := fs.String("log-file", "korocon.log", "AI stdout/stderr log file (default: korocon.log)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file %q: %w", *logPath, err)
	}
	defer logFile.Close()
	binaryName := *binary
	if binaryName == "" {
		binaryName = *provider
	}
	fmt.Fprintf(stderr, "provider: %s\nmodel: %s\nbinary: %s\nconfig: %s\nworkspace: %s\nbranch: %s\nimplementation directory: %s\nimplementation loops: %d\nauto-approved commands: %d\nlog: %s\n", *provider, *model, binaryName, configPath, configured.WorkspaceName, configured.BranchNamePattern, configured.ImplementationDirectory, configured.ImplementationLoopCount, len(configured.BuiltinAllowedCommands), *logPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupInput, selectedIssue, err := selectGitHubInformation(ctx, in, stdout, *dir, configured.WorkspaceName)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	initialPrompt := ""
	var initialJob *daemon.JobSpec
	var review *issueReviewController
	var implementationEngine *implementation.Engine
	if selectedIssue != nil {
		implementationEngine = implementation.New(implementation.Config{
			Binary: *binary, RepositoryDir: *dir, WorkspaceName: configured.WorkspaceName,
			ImplementationDirectory: configured.ImplementationDirectory,
			BranchNamePattern:       configured.BranchNamePattern, LoopCount: configured.ImplementationLoopCount,
			IssueNumber: selectedIssue.Issue.Number, IssueTitle: selectedIssue.Issue.Title,
			IssueContext: selectedIssue.Context(), LogOut: logFile, LogErr: logFile,
		})
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
	cfg := daemon.Config{
		Provider: *provider, Binary: *binary, Model: *model,
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
	if review != nil {
		cfg.OnJobStart = review.OnJobStart
		cfg.OnJobFinish = review.OnJobFinish
		cfg.HandleInput = review.HandleInput
	}
	err = daemon.Run(ctx, startupInput, stdout, cfg)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// selectGitHubInformation performs the small piece of setup that is needed
// before the resident AI process starts. Keeping this outside daemon.Run is
// important: the choice and issue number must not be sent as AI prompts.
func selectGitHubInformation(ctx context.Context, in io.Reader, out io.Writer, workingDir, workspaceName string) (io.Reader, *issueworkflow.Workflow, error) {
	reader := bufio.NewReader(in)
	for {
		if _, err := fmt.Fprint(out, "取得する情報を選択してください (issue/pr): "); err != nil {
			return nil, nil, err
		}
		choice, err := readStringContext(ctx, reader)
		if err != nil && len(choice) == 0 {
			return nil, nil, fmt.Errorf("GitHub情報の選択を読み取れません: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "1", "issue", "i":
			selected, err := selectIssue(ctx, reader, out, workingDir, workspaceName)
			if err != nil {
				return nil, nil, err
			}
			return remainingInput(in, reader), selected, nil
		case "2", "pr", "p":
			if err := showPullRequests(ctx, out, workingDir); err != nil {
				return nil, nil, err
			}
			return remainingInput(in, reader), nil, nil
		default:
			if _, writeErr := fmt.Fprintln(out, "issue または pr を入力してください。"); writeErr != nil {
				return nil, nil, writeErr
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
	selected, err := issueworkflow.Load(ctx, workingDir, number, workspaceName)
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

func showPullRequests(ctx context.Context, out io.Writer, workingDir string) error {
	return showGitHubCommand(ctx, out, workingDir, "pr", "list")
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
  --provider NAME       provider name (default: codex)
  --binary PATH         executable (default: codex)
  --model NAME          gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.4, or gpt-5.4-mini
  --dir PATH            provider working directory (default: .)
  --allow-all-tools     grant all provider tools
  --stream-logs         stream AI stdout/stderr in real time (currently on)
  --log-file PATH       AI stdout/stderr log file (default: korocon.log)

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
