package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCopilotACPArgs(t *testing.T) {
	want := []string{"--acp", "--stdio", "--model", "auto"}
	if got := copilotACPArgs("auto"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestCopilotRequestWithinWorkingDir(t *testing.T) {
	root := "/home/coco/dev/korocon-branches/korocon-21"
	tests := []struct {
		name string
		raw  map[string]any
		want bool
	}{
		{name: "absolute path", raw: map[string]any{"path": root + "/cmd/korocon/config_test.go"}, want: true},
		{name: "relative path", raw: map[string]any{"fileName": "internal/runner/runner.go"}, want: true},
		{name: "outside path", raw: map[string]any{"path": "/home/coco/.ssh/config"}, want: false},
		{name: "parent traversal", raw: map[string]any{"path": "../korocon/config.json"}, want: false},
		{name: "worktree diff", raw: map[string]any{"diff": "diff --git a/internal/runner/runner.go b/internal/runner/runner.go\n"}, want: true},
		{name: "absolute worktree diff", raw: map[string]any{"diff": "diff --git a/home/coco/dev/korocon-branches/korocon-21/README.md b/home/coco/dev/korocon-branches/korocon-21/README.md\n"}, want: true},
		{name: "mixed diff", raw: map[string]any{"diff": "diff --git a/README.md b/README.md\ndiff --git a/home/coco/.ssh/config b/home/coco/.ssh/config\n"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := copilotRequestWithinWorkingDir(test.raw, root); got != test.want {
				t.Fatalf("copilotRequestWithinWorkingDir() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStartCopilotSessionDoesNotPromptBeforeInputLoopStarts(t *testing.T) {
	oldCommand := copilotACPCommand
	copilotACPCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1", "KOROCON_COPILOT_REJECT_PROMPT=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	session, err := StartCopilotSession(context.Background(), SessionConfig{
		WorkingDir: t.TempDir(), LogErr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartAgentSessionKeepsOneCopilotACPProcess(t *testing.T) {
	oldCommand := copilotACPCommand
	starts := 0
	startedModel := ""
	copilotACPCommand = func(ctx context.Context, _ string, model string) *exec.Cmd {
		starts++
		startedModel = model
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	var log strings.Builder
	session, err := StartAgentSession(context.Background(), SessionConfig{
		Provider: "github_copilot", Model: "auto", WorkingDir: t.TempDir(), LogOut: &log, LogErr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.RunTurn(context.Background(), "first", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	modelSession, ok := session.(ModelSession)
	if !ok {
		t.Fatal("Copilot session does not support model changes")
	}
	if err := modelSession.SetModel(context.Background(), "gpt-5-mini"); err != nil {
		t.Fatal(err)
	}
	second, err := session.RunTurn(context.Background(), "second", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || startedModel != "auto" {
		t.Fatalf("starts = %d, model = %q", starts, startedModel)
	}
	if first.Text != "answer:first model:auto" || second.Text != "answer:second model:gpt-5-mini" {
		t.Fatalf("unexpected results: %#v %#v", first, second)
	}
	if !strings.Contains(log.String(), `"sessionUpdate":"agent_message_chunk"`) {
		t.Fatalf("ACP output was not logged: %q", log.String())
	}
}

func TestCopilotACPMapsPermissionRequestToExistingApprovalHandler(t *testing.T) {
	oldCommand := copilotACPCommand
	copilotACPCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	var method, command string
	session, err := StartCopilotSession(context.Background(), SessionConfig{
		WorkingDir: t.TempDir(), LogErr: io.Discard,
		HandleRequest: func(_ context.Context, gotMethod string, params json.RawMessage) (any, error) {
			method = gotMethod
			var detail struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(params, &detail)
			command = detail.Command
			return map[string]string{"decision": "accept"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(context.Background(), "permission", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if method != "item/commandExecution/requestApproval" || command != "go test ./..." {
		t.Fatalf("approval method=%q command=%q", method, command)
	}
	if result.Text != "permission:allow-once" {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestCopilotACPForwardsPathPermissionSeparately(t *testing.T) {
	oldCommand := copilotACPCommand
	copilotACPCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	var method, path string
	session, err := StartCopilotSession(context.Background(), SessionConfig{
		WorkingDir: t.TempDir(), LogErr: io.Discard,
		HandleRequest: func(_ context.Context, gotMethod string, params json.RawMessage) (any, error) {
			method = gotMethod
			var detail struct {
				RawInput struct {
					Path string `json:"path"`
				} `json:"rawInput"`
			}
			_ = json.Unmarshal(params, &detail)
			path = detail.RawInput.Path
			return map[string]string{"decision": "accept"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(context.Background(), "path-permission", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if method != "copilot/session/requestPermission" || path != "/tmp/copilot/session/plan.md" {
		t.Fatalf("approval method=%q path=%q", method, path)
	}
	if result.Text != "permission:allow-once" {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestCopilotACPAutomaticallyApprovesImplementationWorktreePath(t *testing.T) {
	oldCommand := copilotACPCommand
	copilotACPCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	handlerCalled := false
	session, err := StartCopilotSession(context.Background(), SessionConfig{
		WorkingDir: t.TempDir(), LogErr: io.Discard, ApproveWorkingDirPaths: true,
		HandleRequest: func(context.Context, string, json.RawMessage) (any, error) {
			handlerCalled = true
			return map[string]string{"decision": "decline"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(context.Background(), "worktree-path-permission", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if handlerCalled {
		t.Fatal("worktree path was sent to the manual approval handler")
	}
	if result.Text != "permission:allow-once" {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestCopilotACPCollectsAllChunksBeforeTurnResponse(t *testing.T) {
	oldCommand := copilotACPCommand
	copilotACPCommand = func(ctx context.Context, _ string, _ string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCopilotACPHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_COPILOT_ACP_HELPER=1")
		return cmd
	}
	defer func() { copilotACPCommand = oldCommand }()

	session, err := StartCopilotSession(context.Background(), SessionConfig{
		WorkingDir: t.TempDir(), LogErr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(context.Background(), "chunked", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&want, "chunk-%02d", i)
	}
	if result.Text != want.String() {
		t.Fatalf("result length = %d, want %d: %q", len(result.Text), len(want.String()), result.Text)
	}
}

func TestCopilotACPHelperProcess(t *testing.T) {
	if os.Getenv("KOROCON_COPILOT_ACP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	model := "auto"
	promptCount := 0
	for scanner.Scan() {
		var request struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}})
		case "session/new":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"sessionId": "session-1"}})
		case "session/prompt":
			if os.Getenv("KOROCON_COPILOT_REJECT_PROMPT") == "1" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "prompt sent during session startup"}})
				continue
			}
			promptCount++
			var params struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(request.Params, &params)
			prompt := ""
			if len(params.Prompt) > 0 {
				prompt = params.Prompt[0].Text
			}
			if promptCount == 1 && prompt != "/ide" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "first prompt must be /ide"}})
				continue
			}
			if strings.HasPrefix(prompt, "/model ") {
				model = strings.TrimPrefix(prompt, "/model ")
			}
			answer := "command:" + prompt
			if prompt == "permission" || prompt == "path-permission" || prompt == "worktree-path-permission" {
				rawInput := map[string]string{"command": "go test ./..."}
				if prompt == "path-permission" {
					rawInput = map[string]string{"path": "/tmp/copilot/session/plan.md"}
				} else if prompt == "worktree-path-permission" {
					rawInput = map[string]string{"path": "internal/runner/runner.go"}
				}
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission", "params": map[string]any{
					"sessionId": params.SessionID,
					"toolCall":  map[string]any{"toolCallId": "tool-1", "title": "Request permission", "rawInput": rawInput},
					"options":   []map[string]string{{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"}, {"optionId": "reject-once", "name": "Reject", "kind": "reject_once"}},
				}})
				if scanner.Scan() {
					var permissionResponse struct {
						Result struct {
							Outcome struct {
								OptionID string `json:"optionId"`
							} `json:"outcome"`
						} `json:"result"`
					}
					_ = json.Unmarshal(scanner.Bytes(), &permissionResponse)
					answer = "permission:" + permissionResponse.Result.Outcome.OptionID
				}
			}
			if !strings.HasPrefix(prompt, "/") {
				if prompt != "permission" && prompt != "path-permission" && prompt != "worktree-path-permission" {
					answer = fmt.Sprintf("answer:%s model:%s", prompt, model)
				}
			}
			chunks := []string{answer}
			if prompt == "chunked" {
				chunks = chunks[:0]
				for i := 0; i < 64; i++ {
					chunks = append(chunks, fmt.Sprintf("chunk-%02d", i))
				}
			}
			for _, chunk := range chunks {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
					"sessionId": params.SessionID,
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": chunk}},
				}})
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"stopReason": "end_turn"}})
		}
	}
}

func TestStartAgentSessionRejectsUnsupportedProvider(t *testing.T) {
	if _, err := StartAgentSession(context.Background(), SessionConfig{Provider: "unknown", WorkingDir: t.TempDir()}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}
