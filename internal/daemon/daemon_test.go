package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestRunDisplaysProviderAndModelWhenJobStarts(t *testing.T) {
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
	if !strings.Contains(status.String(), "provider: copilot") || !strings.Contains(status.String(), "model: gpt-test") {
		t.Fatalf("status does not include provider and model: %q", status.String())
	}
}

func TestRunModelCommandListsAndSwitchesModelByName(t *testing.T) {
	var out, status strings.Builder
	err := Run(context.Background(), strings.NewReader("/model\n/model gpt-5.6-terra\nfirst\n"), &out, Config{
		Provider:  "copilot",
		Binary:    "/bin/echo",
		Model:     "gpt-5.6-luna",
		StatusOut: &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), "1  gpt-5.6-sol") ||
		!strings.Contains(status.String(), "2  gpt-5.6-terra") ||
		!strings.Contains(status.String(), "3* gpt-5.6-luna") ||
		!strings.Contains(status.String(), "モデルを gpt-5.6-terra に切り替えました") {
		t.Fatalf("unexpected model command output: %q", status.String())
	}
	if strings.Contains(out.String(), "/model") || !strings.Contains(out.String(), "--model gpt-5.6-terra") {
		t.Fatalf("selected model was not used for the next prompt: %q", out.String())
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
