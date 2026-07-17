package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionKeepsOneProcessAndThreadForMultipleTurns(t *testing.T) {
	oldCommand := appServerCommand
	starts := 0
	appServerCommand = func(ctx context.Context, _ string) *exec.Cmd {
		starts++
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSessionHelperProcess")
		cmd.Env = append(os.Environ(), "KOROCON_APP_SERVER_HELPER=1")
		return cmd
	}
	defer func() { appServerCommand = oldCommand }()

	var log strings.Builder
	session, err := StartSession(context.Background(), SessionConfig{
		Model: "gpt-5.6-luna", WorkingDir: ".", LogOut: &log, LogErr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.RunTurn(context.Background(), "first", "gpt-5.6-luna", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetModel(context.Background(), "gpt-5.6-terra"); err != nil {
		t.Fatal(err)
	}
	second, err := session.RunTurn(context.Background(), "second", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("app-server starts = %d, want 1", starts)
	}
	if first.Text != "answer:first model:gpt-5.6-luna" || second.Text != "answer:second model:gpt-5.6-terra" {
		t.Fatalf("unexpected results: %#v %#v", first, second)
	}
	if first.Tokens != 12 || second.Tokens != 12 {
		t.Fatalf("unexpected token counts: %#v %#v", first, second)
	}
	if !strings.Contains(log.String(), `"method":"thread/started"`) {
		t.Fatalf("protocol output was not logged: %q", log.String())
	}
}

func TestSessionHelperProcess(t *testing.T) {
	if os.Getenv("KOROCON_APP_SERVER_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	turn := 0
	currentModel := ""
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
			var params struct {
				Capabilities struct {
					ExperimentalAPI bool `json:"experimentalApi"`
				} `json:"capabilities"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if !params.Capabilities.ExperimentalAPI {
				_ = encoder.Encode(map[string]any{"id": request.ID, "error": map[string]any{"code": -32600, "message": "experimentalApi capability is required"}})
				continue
			}
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{}})
		case "initialized":
			continue
		case "thread/start":
			var params struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(request.Params, &params)
			currentModel = params.Model
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-1"}}})
			_ = encoder.Encode(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]string{"id": "thread-1"}}})
		case "thread/settings/update":
			var params struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(request.Params, &params)
			currentModel = params.Model
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{"model": currentModel}})
		case "turn/start":
			turn++
			var params struct {
				ThreadID string `json:"threadId"`
				Model    string `json:"model"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Model != "" {
				currentModel = params.Model
			}
			turnID := fmt.Sprintf("turn-%d", turn)
			text := ""
			if len(params.Input) > 0 {
				text = params.Input[0].Text
			}
			_ = encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": turnID, "items": []any{}, "status": "inProgress"}}})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
				"threadId": params.ThreadID, "turnId": turnID,
				"item": map[string]string{"id": "message-1", "type": "agentMessage", "phase": "final_answer", "text": "answer:" + text + " model:" + currentModel},
			}})
			_ = encoder.Encode(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{
				"threadId": params.ThreadID, "turnId": turnID,
				"tokenUsage": map[string]any{"last": map[string]int{"totalTokens": 12}},
			}})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{
				"threadId": params.ThreadID,
				"turn":     map[string]any{"id": turnID, "items": []any{}, "status": "completed"},
			}})
		}
	}
}
