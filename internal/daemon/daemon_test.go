package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coco-papiyon/korocon/internal/runner"
)

type fakeDaemonSession struct {
	model string
}

type concurrentApprovalSession struct {
	handler   runner.ServerRequestHandler
	decisions []string
	errs      []error
}

type transientApprovalSession struct {
	handler             runner.ServerRequestHandler
	calls               int
	secondRequestAuto   bool
	secondJobRequestErr error
}

func (s *transientApprovalSession) RunTurn(ctx context.Context, _ string, _ string, _ func()) (runner.TurnResult, error) {
	s.calls++
	params, _ := json.Marshal(map[string]string{"command": "temporary-command"})
	if s.calls == 1 {
		if _, err := s.handler(ctx, "item/commandExecution/requestApproval", params); err != nil {
			return runner.TurnResult{}, err
		}
		_, err := s.handler(ctx, "item/commandExecution/requestApproval", params)
		s.secondRequestAuto = err == nil
		return runner.TurnResult{Text: "first"}, err
	}
	_, err := s.handler(ctx, "item/commandExecution/requestApproval", params)
	s.secondJobRequestErr = err
	return runner.TurnResult{Text: "second"}, err
}

func (s *transientApprovalSession) Close() error { return nil }

type approvalStepReader struct {
	prompts <-chan struct{}
	steps   []struct {
		line string
		wait bool
	}
	index int
}

func (r *approvalStepReader) Read(p []byte) (int, error) {
	if r.index >= len(r.steps) {
		return 0, io.EOF
	}
	step := r.steps[r.index]
	r.index++
	if step.wait {
		<-r.prompts
	}
	return copy(p, step.line+"\n"), nil
}

func (s *concurrentApprovalSession) RunTurn(ctx context.Context, _ string, _ string, _ func()) (runner.TurnResult, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			params, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("manual-command-%d", index)})
			result, err := s.handler(ctx, "item/commandExecution/requestApproval", params)
			mu.Lock()
			defer mu.Unlock()
			s.errs = append(s.errs, err)
			if decision, ok := result.(map[string]string); ok {
				s.decisions = append(s.decisions, decision["decision"])
			}
		}(i)
	}
	wg.Wait()
	return runner.TurnResult{Text: "final review"}, nil
}

func (s *concurrentApprovalSession) Close() error { return nil }

type approvalSignalWriter struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	prompts chan<- struct{}
}

func (w *approvalSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if bytes.Contains(p, []byte("[承認待ち]")) {
		w.prompts <- struct{}{}
	}
	return n, err
}

func (w *approvalSignalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

type approvalSignalReader struct {
	prompts <-chan struct{}
	left    int
}

func (r *approvalSignalReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	<-r.prompts
	p[0] = '\n'
	r.left--
	return 1, nil
}

func (s *fakeDaemonSession) RunTurn(_ context.Context, prompt, model string, onEvent func()) (runner.TurnResult, error) {
	if model != "" {
		s.model = model
	}
	if onEvent != nil {
		onEvent()
	}
	return runner.TurnResult{Text: strings.TrimSpace("-p " + prompt + " --model " + s.model)}, nil
}

func (s *fakeDaemonSession) SetModel(_ context.Context, model string) error {
	s.model = model
	return nil
}

func (s *fakeDaemonSession) Close() error { return nil }

func TestMain(m *testing.M) {
	oldStart := startDaemonSession
	startDaemonSession = func(_ context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
		return &fakeDaemonSession{model: cfg.Model}, nil
	}
	code := m.Run()
	startDaemonSession = oldStart
	os.Exit(code)
}

func TestJSONResultWriterDisplaysFinalAgentMessage(t *testing.T) {
	var log, result bytes.Buffer
	w := jsonResultWriter{log: &log, result: &result}
	events := "{\"type\":\"item.started\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final answer\"}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":120,\"output_tokens\":30}}\n"
	if _, err := w.Write([]byte(events)); err != nil {
		t.Fatal(err)
	}
	if log.String() != events || result.String() != "final answer\n" {
		t.Fatalf("log=%q result=%q", log.String(), result.String())
	}
	if got := w.TokenCount(); got != 150 {
		t.Fatalf("token count = %d, want 150", got)
	}
}

func TestJSONResultWriterReportsToolExecution(t *testing.T) {
	var log, result bytes.Buffer
	var status []string
	w := jsonResultWriter{
		log: &log, result: &result,
		toolStatus: func(message string) { status = append(status, message) },
	}
	events := "{\"type\":\"item.started\",\"item\":{\"type\":\"command_execution\",\"command\":\"rg -n TODO\"}}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"rg -n TODO\"}}\n"
	if _, err := w.Write([]byte(events)); err != nil {
		t.Fatal(err)
	}
	if len(status) != 2 || status[0] != "ツール実行中: rg -n TODO" || status[1] != "" {
		t.Fatalf("tool status = %#v", status)
	}
}

func TestRunRejectsMissingStreams(t *testing.T) {
	if err := Run(context.Background(), nil, &strings.Builder{}, Config{}); err == nil {
		t.Fatal("expected missing input error")
	}
}

func TestRunStartsJobsInBackground(t *testing.T) {
	var out strings.Builder
	err := Run(context.Background(), strings.NewReader("first\nsecond\n"), &out, Config{Provider: "copilot", Binary: "/bin/echo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "-p first") || !strings.Contains(out.String(), "-p second") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunQueuesConcurrentManualApprovals(t *testing.T) {
	oldStart := startDaemonSession
	var session *concurrentApprovalSession
	startDaemonSession = func(_ context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
		session = &concurrentApprovalSession{handler: cfg.HandleRequest}
		return session, nil
	}
	defer func() { startDaemonSession = oldStart }()

	prompts := make(chan struct{}, 3)
	status := &approvalSignalWriter{prompts: prompts}
	in := &approvalSignalReader{prompts: prompts, left: 3}
	if err := Run(context.Background(), in, io.Discard, Config{
		Provider: "copilot", InitialPrompt: "review", StatusOut: status,
	}); err != nil {
		t.Fatal(err)
	}
	if len(session.errs) != 3 || len(session.decisions) != 3 {
		t.Fatalf("errors = %v, decisions = %v", session.errs, session.decisions)
	}
	for _, err := range session.errs {
		if err != nil {
			t.Fatalf("approval error = %v", err)
		}
	}
	for _, decision := range session.decisions {
		if decision != "accept" {
			t.Fatalf("decision = %q, want accept", decision)
		}
	}
	if got := strings.Count(status.String(), "[承認待ち]"); got != 3 {
		t.Fatalf("approval prompts = %d, want 3: %q", got, status.String())
	}
}

func TestRunAllowsCommandForCurrentJobOnly(t *testing.T) {
	oldStart := startDaemonSession
	var session *transientApprovalSession
	startDaemonSession = func(_ context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
		session = &transientApprovalSession{handler: cfg.HandleRequest}
		return session, nil
	}
	defer func() { startDaemonSession = oldStart }()

	prompts := make(chan struct{}, 2)
	status := &approvalSignalWriter{prompts: prompts}
	in := &approvalStepReader{prompts: prompts, steps: []struct {
		line string
		wait bool
	}{
		{line: "/allow-job", wait: true},
		{line: "next", wait: false},
		{line: "/approve", wait: true},
	}}
	if err := Run(context.Background(), in, io.Discard, Config{
		Provider: "copilot", InitialPrompt: "first", StatusOut: status,
	}); err != nil {
		t.Fatal(err)
	}
	if session == nil || !session.secondRequestAuto || session.secondJobRequestErr != nil {
		t.Fatalf("session = %+v", session)
	}
	if got := strings.Count(status.String(), "[承認待ち]"); got != 2 {
		t.Fatalf("approval prompts = %d, want 2: %q", got, status.String())
	}
}

func TestRunPreparesEveryJobBeforeStarting(t *testing.T) {
	var out strings.Builder
	var prepared []string
	err := Run(context.Background(), strings.NewReader("first\nsecond\n"), &out, Config{
		Provider: "copilot", Binary: "/bin/echo",
		BeforeJob: func(_ context.Context, _ uint64, prompt string) error {
			prepared = append(prepared, prompt)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(prepared, ",") != "first,second" {
		t.Fatalf("prepared jobs = %v", prepared)
	}
	if !strings.Contains(out.String(), "-p first") || !strings.Contains(out.String(), "-p second") {
		t.Fatalf("jobs were not started: %q", out.String())
	}
}

func TestRunDoesNotStartJobWhenPreparationFails(t *testing.T) {
	var out, status strings.Builder
	err := Run(context.Background(), strings.NewReader("first\n"), &out, Config{
		Provider: "copilot", Binary: "/bin/echo", StatusOut: &status,
		BeforeJob: func(context.Context, uint64, string) error {
			return errors.New("pull failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "-p first") {
		t.Fatalf("job started after preparation failure: %q", out.String())
	}
	if !strings.Contains(out.String(), "prepare job: pull failed") || !strings.Contains(status.String(), "[job 1] 失敗") {
		t.Fatalf("out=%q status=%q", out.String(), status.String())
	}
}

func TestRunProcessesInitialPromptWithLifecycleCallbacks(t *testing.T) {
	var out strings.Builder
	var started, finished bool
	err := Run(context.Background(), strings.NewReader(""), &out, Config{
		Provider: "copilot", Binary: "/bin/echo", InitialPrompt: "design issue 42",
		OnJobStart: func(_ context.Context, id uint64, prompt string) error {
			started = id == 1 && prompt == "design issue 42"
			return nil
		},
		OnJobFinish: func(_ context.Context, id uint64, prompt, result string, runErr error) error {
			finished = id == 1 && prompt == "design issue 42" && strings.Contains(result, "design issue 42") && runErr == nil
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !started || !finished {
		t.Fatalf("callbacks: started=%v finished=%v", started, finished)
	}
	if !strings.Contains(out.String(), "design issue 42") {
		t.Fatalf("initial prompt was not executed: %q", out.String())
	}
}

func TestRunAllowsInputHandlerToConsumeEmptyLineAndStartPrompt(t *testing.T) {
	var out strings.Builder
	err := Run(context.Background(), strings.NewReader("\n"), &out, Config{
		Provider: "copilot", Binary: "/bin/echo",
		HandleInput: func(_ context.Context, line string) (InputAction, error) {
			if line != "" {
				t.Fatalf("line = %q", line)
			}
			return InputAction{Handled: true, Prompt: "revised design"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "revised design") {
		t.Fatalf("handler prompt was not executed: %q", out.String())
	}
}

func TestRunReturnsRestartWhenInputHandlerRequestsSelectionRestart(t *testing.T) {
	err := Run(context.Background(), strings.NewReader("restart\n"), &strings.Builder{}, Config{
		Provider: "copilot", Binary: "/bin/echo",
		HandleInput: func(context.Context, string) (InputAction, error) {
			return InputAction{Handled: true, Restart: true}, nil
		},
	})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("Run() error = %v, want ErrRestart", err)
	}
}

func TestRunReturnsRestartWhenFinishHookRequestsSelectionRestart(t *testing.T) {
	err := Run(context.Background(), strings.NewReader(""), &strings.Builder{}, Config{
		Provider: "copilot", Binary: "/bin/echo", InitialPrompt: "review PR",
		OnJobFinish: func(context.Context, uint64, string, string, error) error {
			return ErrRestart
		},
	})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("Run() error = %v, want ErrRestart", err)
	}
}

func TestRunRestartLeavesFollowingBufferedInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("restart\nissue\n"))
	err := Run(context.Background(), reader, &strings.Builder{}, Config{
		Provider: "copilot", Binary: "/bin/echo",
		HandleInput: func(context.Context, string) (InputAction, error) {
			return InputAction{Handled: true, Restart: true}, nil
		},
	})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("Run() error = %v, want ErrRestart", err)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "issue\n" {
		t.Fatalf("remaining input = %q, want issue\\n", line)
	}
}

func TestRunCallsFinishHookAfterDisplayingResult(t *testing.T) {
	var display strings.Builder
	err := Run(context.Background(), strings.NewReader("prompt\n"), &display, Config{
		Provider: "copilot", Binary: "/bin/echo", ResultOut: &display,
		OnJobFinish: func(_ context.Context, _ uint64, _, _ string, _ error) error {
			_, _ = display.WriteString("review prompt\n")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultAt := strings.Index(display.String(), "-p prompt")
	reviewAt := strings.Index(display.String(), "review prompt")
	if resultAt < 0 || reviewAt < 0 || resultAt > reviewAt {
		t.Fatalf("result was not displayed before review: %q", display.String())
	}
}

func TestRunDisplaysRunningStatusWhenJobStarts(t *testing.T) {
	var out, status strings.Builder
	err := Run(context.Background(), strings.NewReader("first\n"), &out, Config{
		Provider:  "copilot",
		Model:     "gpt-test",
		Binary:    "/bin/echo",
		StatusOut: &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "[job 1] 実行中...") || strings.Contains(status.String(), "provider: copilot") || strings.Contains(status.String(), "model: gpt-test") {
		t.Fatalf("unexpected job status: %q", status.String())
	}
}

func TestRunDisplaysJobStartAfterStartHook(t *testing.T) {
	var status strings.Builder
	err := Run(context.Background(), strings.NewReader(""), &strings.Builder{}, Config{
		Provider: "copilot", Model: "gpt-test", Binary: "/bin/echo", StatusOut: &status,
		InitialJob: &JobSpec{Prompt: "prompt"},
		OnJobStart: func(context.Context, uint64, string) error {
			_, _ = status.WriteString("Issue #1の実装を開始します。\n---\n")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := status.String()
	messageAt := strings.Index(output, "Issue #1の実装を開始します。")
	jobAt := strings.Index(output, "[job 1] 実行中")
	if messageAt < 0 || jobAt < 0 || messageAt > jobAt {
		t.Fatalf("job start order = %q", output)
	}
}

func TestRunExecutesCustomJobWhenPrimaryProviderIsCopilot(t *testing.T) {
	var result strings.Builder
	called := false
	usedModel := ""
	err := Run(context.Background(), strings.NewReader(""), &strings.Builder{}, Config{
		Provider: "copilot", Binary: "/bin/echo", ResultOut: &result,
		InitialJob: &JobSpec{Prompt: "fix review", Execute: func(_ context.Context, model string, _ runner.ServerRequestHandler, setPhase func(string), _ func()) (runner.TurnResult, error) {
			called = true
			usedModel = model
			setPhase("レビュー指摘修正")
			return runner.TurnResult{Text: "fixed"}, nil
		}},
		Model: "reviewer-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || usedModel != "reviewer-model" || !strings.Contains(result.String(), "fixed") {
		t.Fatalf("called=%t model=%q result=%q", called, usedModel, result.String())
	}
}

func TestRunKeepsPhaseHistoryWhenPhaseChanges(t *testing.T) {
	var status strings.Builder
	err := Run(context.Background(), strings.NewReader(""), &strings.Builder{}, Config{
		Provider:  "copilot",
		Binary:    "/bin/echo",
		StatusOut: &status,
		InitialJob: &JobSpec{Prompt: "implementation", Execute: func(_ context.Context, _ string, _ runner.ServerRequestHandler, setPhase func(string), showProgress func()) (runner.TurnResult, error) {
			setPhase("実装1回目")
			showProgress()
			setPhase("実装1回目")
			setPhase("検証1回目")
			setPhase("実装2回目")
			return runner.TurnResult{}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := status.String()
	for _, want := range []string{
		"[job 1] 完了(実装1回目)",
		"[job 2] 実行中(検証1回目)",
		"[job 2] 完了(検証1回目)",
		"[job 3] 実行中(実装2回目)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status does not contain %q: %q", want, got)
		}
	}
	if strings.Count(got, "完了(実装1回目)") != 1 {
		t.Fatalf("same-phase progress created duplicate history: %q", got)
	}
	if strings.Index(got, "[job 1] 完了(実装1回目)") > strings.Index(got, "[job 2] 完了(検証1回目)") {
		t.Fatalf("phase history is out of order: %q", got)
	}
}

func TestRunKeepsPhaseHistoryWhenPhaseFails(t *testing.T) {
	var status strings.Builder
	err := Run(context.Background(), strings.NewReader(""), &strings.Builder{}, Config{
		Provider:  "copilot",
		Binary:    "/bin/echo",
		StatusOut: &status,
		InitialJob: &JobSpec{Prompt: "implementation", Execute: func(_ context.Context, _ string, _ runner.ServerRequestHandler, setPhase func(string), _ func()) (runner.TurnResult, error) {
			setPhase("実装1回目")
			setPhase("検証1回目")
			return runner.TurnResult{}, errors.New("verification failed")
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := status.String()
	if !strings.Contains(got, "[job 1] 完了(実装1回目)") || !strings.Contains(got, "[job 2] 失敗") {
		t.Fatalf("phase history was not preserved on failure: %q", got)
	}
}

func TestRunModelCommandListsAndSwitchesModelByName(t *testing.T) {
	var out, status strings.Builder
	display := NewSystemOutput(&status)
	err := Run(context.Background(), strings.NewReader("/model\n/model gpt-5.6-terra\nfirst\n"), &out, Config{
		Provider:  "copilot",
		Binary:    "/bin/echo",
		Model:     "gpt-5.6-luna",
		StatusOut: display,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "1  auto") ||
		!strings.Contains(status.String(), "2  gpt-5.6-sol") ||
		!strings.Contains(status.String(), "3  gpt-5.6-terra") ||
		!strings.Contains(status.String(), "4* gpt-5.6-luna") ||
		!strings.Contains(status.String(), "モデルを gpt-5.6-terra に切り替えました") {
		t.Fatalf("unexpected model command output: %q", status.String())
	}
	if !strings.Contains(status.String(), "---\n[システム] モデルを gpt-5.6-terra に切り替えました。") {
		t.Fatalf("model switch was not formatted as a system message: %q", status.String())
	}
	if strings.Contains(out.String(), "/model") || !strings.Contains(out.String(), "--model gpt-5.6-terra") {
		t.Fatalf("selected model was not used for the next prompt: %q", out.String())
	}
}

func TestRunModelCommandRejectsUnavailableModelAsSystemMessage(t *testing.T) {
	var out, status strings.Builder
	display := NewSystemOutput(&status)
	err := Run(context.Background(), strings.NewReader("/model unavailable\n"), &out, Config{
		Provider: "copilot", Binary: "/bin/echo", StatusOut: display,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := status.String(); !strings.Contains(got, "---\n[システム] 利用できないモデルです: unavailable") {
		t.Fatalf("unavailable model was not formatted as a system message: %q", got)
	}
}

func TestSelectModelByName(t *testing.T) {
	selected, ok := selectModel("gpt-5.6-terra")
	if !ok || selected != "gpt-5.6-terra" {
		t.Fatalf("selectModel by name = (%q, %v)", selected, ok)
	}
	selected, ok = selectModel("2")
	if !ok || selected != "gpt-5.6-terra" {
		t.Fatalf("selectModel by number = (%q, %v)", selected, ok)
	}
}

func TestRunOnlyTreatsSlashAtStartAsCommand(t *testing.T) {
	var out strings.Builder
	err := Run(context.Background(), strings.NewReader(" /model 2\n"), &out, Config{Provider: "copilot", Binary: "/bin/echo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), " /model 2") {
		t.Fatalf("leading-space slash input was treated as a command: %q", out.String())
	}
}

func TestRunDiffCommandWithoutCompletedJob(t *testing.T) {
	var status strings.Builder
	err := Run(context.Background(), strings.NewReader("/diff\n"), &strings.Builder{}, Config{Provider: "copilot", StatusOut: &status})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "直前の修正のdiffはありません") {
		t.Fatalf("unexpected diff command output: %q", status.String())
	}
}

func TestCaptureGitDiff(t *testing.T) {
	dir := t.TempDir()
	if err := runGit(dir, "init"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(dir, "config", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(dir, "config", "user.name", "test"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGit(dir, "add", "file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(dir, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	diff, err := captureGitDiff(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-before") || !strings.Contains(diff, "+after") {
		t.Fatalf("unexpected diff: %q", diff)
	}
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}
