package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// CopilotSession owns one Copilot ACP process and one conversation session.
type CopilotSession struct {
	ctx           context.Context
	cancel        context.CancelFunc
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	logOut        io.Writer
	handleRequest ServerRequestHandler
	sessionID     string
	ideEnabled    bool
	nextID        atomic.Int64
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[int64]chan rpcMessage
	turnMu        sync.Mutex
	events        chan rpcMessage
	done          chan struct{}
	errMu         sync.Mutex
	processErr    error
}

var copilotACPCommand = func(ctx context.Context, binary string, model string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, copilotACPArgs(model)...)
}

func copilotACPArgs(model string) []string {
	args := []string{"--acp", "--stdio"}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	return args
}

// StartCopilotSession starts Copilot once and creates an ACP session. IDE
// integration is enabled lazily after the daemon input loop has started.
func StartCopilotSession(ctx context.Context, cfg SessionConfig) (*CopilotSession, error) {
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "copilot"
	}
	dir, err := workingDir(cfg.WorkingDir)
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := copilotACPCommand(processCtx, binary, cfg.Model)
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open %s stdin: %w", binary, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open %s stdout: %w", binary, err)
	}
	cmd.Stderr = writerOrDiscard(cfg.LogErr)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s ACP server: %w", binary, err)
	}

	s := &CopilotSession{
		ctx: processCtx, cancel: cancel, cmd: cmd, stdin: stdin,
		logOut: writerOrDiscard(cfg.LogOut), handleRequest: cfg.HandleRequest,
		pending: make(map[int64]chan rpcMessage), events: make(chan rpcMessage, 256),
		done: make(chan struct{}),
	}
	go s.read(stdout)
	go func() {
		err := cmd.Wait()
		s.errMu.Lock()
		s.processErr = err
		s.errMu.Unlock()
		close(s.done)
	}()

	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]string{"name": "korocon", "title": "korocon", "version": "0.1.0"},
	}, &initialized); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("initialize Copilot ACP server: %w", err)
	}
	if initialized.ProtocolVersion != 1 {
		_ = s.Close()
		return nil, fmt.Errorf("initialize Copilot ACP server: unsupported protocol version %d", initialized.ProtocolVersion)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := s.call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &created); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("create Copilot ACP session: %w", err)
	}
	if created.SessionID == "" {
		_ = s.Close()
		return nil, errors.New("create Copilot ACP session: response did not include a session id")
	}
	s.sessionID = created.SessionID
	return s, nil
}

func (s *CopilotSession) RunTurn(ctx context.Context, prompt, model string, onEvent func()) (TurnResult, error) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if strings.TrimSpace(prompt) == "" {
		return TurnResult{}, errors.New("prompt is empty")
	}
	if err := s.enableIDE(ctx, onEvent); err != nil {
		return TurnResult{}, err
	}
	if strings.TrimSpace(model) != "" {
		if _, err := s.runTurn(ctx, "/model "+model, onEvent); err != nil {
			return TurnResult{}, fmt.Errorf("update Copilot model: %w", err)
		}
	}
	return s.runTurn(ctx, prompt, onEvent)
}

func (s *CopilotSession) SetModel(ctx context.Context, model string) error {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if strings.TrimSpace(model) == "" {
		return errors.New("model is empty")
	}
	if err := s.enableIDE(ctx, nil); err != nil {
		return err
	}
	if _, err := s.runTurn(ctx, "/model "+model, nil); err != nil {
		return fmt.Errorf("update Copilot model: %w", err)
	}
	return nil
}

func (s *CopilotSession) enableIDE(ctx context.Context, onEvent func()) error {
	if s.ideEnabled {
		return nil
	}
	if _, err := s.runTurn(ctx, "/ide", onEvent); err != nil {
		return fmt.Errorf("enable Copilot IDE mode: %w", err)
	}
	s.ideEnabled = true
	return nil
}

func (s *CopilotSession) runTurn(ctx context.Context, prompt string, onEvent func()) (TurnResult, error) {
	id := s.nextID.Add(1)
	response := make(chan rpcMessage, 1)
	s.pendingMu.Lock()
	s.pending[id] = response
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()
	if err := s.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "session/prompt",
		"params": map[string]any{"sessionId": s.sessionID, "prompt": []map[string]string{{"type": "text", "text": prompt}}},
	}); err != nil {
		return TurnResult{}, err
	}

	var text strings.Builder
	for {
		select {
		case <-ctx.Done():
			_ = s.notify("session/cancel", map[string]string{"sessionId": s.sessionID})
			return TurnResult{}, ctx.Err()
		case <-s.done:
			return TurnResult{}, s.processError()
		case event := <-s.events:
			s.appendTurnEvent(event, &text, onEvent)
		case message := <-response:
			// stdout notifications precede the response, but the dispatcher sends
			// them through a different channel. Drain those queued notifications so
			// select scheduling cannot truncate the final agent message.
			for {
				select {
				case event := <-s.events:
					s.appendTurnEvent(event, &text, onEvent)
				default:
					return s.finishTurn(message, text.String())
				}
			}
		}
	}
}

func (s *CopilotSession) appendTurnEvent(event rpcMessage, text *strings.Builder, onEvent func()) {
	if onEvent != nil {
		onEvent()
	}
	if event.Method != "session/update" {
		return
	}
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type    string `json:"sessionUpdate"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(event.Params, &params) == nil && params.SessionID == s.sessionID &&
		params.Update.Type == "agent_message_chunk" && params.Update.Content.Type == "text" {
		text.WriteString(params.Update.Content.Text)
	}
}

func (s *CopilotSession) finishTurn(message rpcMessage, text string) (TurnResult, error) {
	if message.Error != nil {
		return TurnResult{}, fmt.Errorf("%s (%d)", message.Error.Message, message.Error.Code)
	}
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(message.Result, &result); err != nil {
		return TurnResult{}, fmt.Errorf("decode session/prompt response: %w", err)
	}
	if result.StopReason != "end_turn" {
		return TurnResult{}, fmt.Errorf("Copilot turn stopped: %s", result.StopReason)
	}
	return TurnResult{Text: strings.TrimSpace(text)}, nil
}

func (s *CopilotSession) Close() error {
	s.cancel()
	_ = s.stdin.Close()
	<-s.done
	err := s.processError()
	if errors.Is(err, context.Canceled) || errors.Is(s.ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

func (s *CopilotSession) call(ctx context.Context, method string, params any, result any) error {
	id := s.nextID.Add(1)
	response := make(chan rpcMessage, 1)
	s.pendingMu.Lock()
	s.pending[id] = response
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.processError()
	case message := <-response:
		if message.Error != nil {
			return fmt.Errorf("%s (%d)", message.Error.Message, message.Error.Code)
		}
		if result == nil || len(message.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(message.Result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}
}

func (s *CopilotSession) notify(method string, params any) error {
	return s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *CopilotSession) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write Copilot stdin: %w", err)
	}
	return nil
}

func (s *CopilotSession) read(src io.Reader) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		_, _ = s.logOut.Write(append(line, '\n'))
		var message rpcMessage
		if json.Unmarshal(line, &message) != nil {
			continue
		}
		if message.Method != "" && len(message.ID) > 0 {
			go s.handleServerRequest(message)
			continue
		}
		if message.Method != "" {
			select {
			case s.events <- message:
			case <-s.ctx.Done():
				return
			}
			continue
		}
		var id int64
		if json.Unmarshal(message.ID, &id) != nil {
			continue
		}
		s.pendingMu.Lock()
		response := s.pending[id]
		s.pendingMu.Unlock()
		if response != nil {
			response <- message
		}
	}
}

func (s *CopilotSession) handleServerRequest(message rpcMessage) {
	response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID)}
	if message.Method != "session/request_permission" {
		response["error"] = map[string]any{"code": -32601, "message": "unsupported Copilot request: " + message.Method}
		_ = s.write(response)
		return
	}
	var params struct {
		ToolCall struct {
			Title    string         `json:"title"`
			RawInput map[string]any `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		response["error"] = map[string]any{"code": -32602, "message": err.Error()}
		_ = s.write(response)
		return
	}
	decision := "decline"
	if s.handleRequest != nil {
		command := copilotApprovalCommand(params.ToolCall.RawInput)
		method := "item/commandExecution/requestApproval"
		var translated []byte
		if command != "" {
			translated, _ = json.Marshal(map[string]string{"command": command, "reason": params.ToolCall.Title})
		} else {
			method = "copilot/session/requestPermission"
			translated, _ = json.Marshal(map[string]any{"title": params.ToolCall.Title, "rawInput": params.ToolCall.RawInput})
		}
		result, err := s.handleRequest(s.ctx, method, translated)
		if err != nil {
			response["error"] = map[string]any{"code": -32603, "message": err.Error()}
			_ = s.write(response)
			return
		}
		if value, ok := result.(map[string]string); ok {
			decision = value["decision"]
		}
	}
	wanted := "reject_once"
	if decision == "accept" {
		wanted = "allow_once"
	}
	for _, option := range params.Options {
		if option.Kind == wanted {
			response["result"] = map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option.OptionID}}
			_ = s.write(response)
			return
		}
	}
	response["result"] = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	_ = s.write(response)
}

func copilotApprovalCommand(raw map[string]any) string {
	for _, key := range []string{"command", "cmd", "script"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *CopilotSession) processError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.processErr == nil {
		return errors.New("Copilot ACP server stopped")
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	return fmt.Errorf("Copilot ACP server stopped: %w", s.processErr)
}
