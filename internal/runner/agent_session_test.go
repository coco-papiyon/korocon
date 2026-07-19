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
			if prompt == "permission" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission", "params": map[string]any{
					"sessionId": params.SessionID,
					"toolCall":  map[string]any{"toolCallId": "tool-1", "title": "Run tests", "rawInput": map[string]string{"command": "go test ./..."}},
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
				if prompt != "permission" {
					answer = fmt.Sprintf("answer:%s model:%s", prompt, model)
				}
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": params.SessionID,
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": answer}},
			}})
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]string{"stopReason": "end_turn"}})
		}
	}
}

func TestStartAgentSessionRejectsUnsupportedProvider(t *testing.T) {
	if _, err := StartAgentSession(context.Background(), SessionConfig{Provider: "unknown", WorkingDir: t.TempDir()}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}
