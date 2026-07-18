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

// ServerRequestHandler handles requests that Codex sends back to its client,
// such as command execution approvals.
type ServerRequestHandler func(context.Context, string, json.RawMessage) (any, error)

// SessionConfig controls a resident Codex app-server process.
type SessionConfig struct {
	Provider       string
	Binary         string
	Model          string
	WorkingDir     string
	LogOut         io.Writer
	LogErr         io.Writer
	HandleRequest  ServerRequestHandler
	Sandbox        string
	ApprovalPolicy string
}

// TurnResult is the final output of one turn in a resident Codex thread.
type TurnResult struct {
	Text   string
	Tokens int
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Session owns one Codex process and one conversation thread.
type Session struct {
	ctx           context.Context
	cancel        context.CancelFunc
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	logOut        io.Writer
	handleRequest ServerRequestHandler
	threadID      string
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

var appServerCommand = func(ctx context.Context, binary string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, "app-server", "--stdio")
}

// StartSession starts Codex once and initializes a resident conversation.
func StartSession(ctx context.Context, cfg SessionConfig) (*Session, error) {
	binary := cfg.Binary
	if binary == "" {
		binary = "codex"
	}
	dir, err := workingDir(cfg.WorkingDir)
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := appServerCommand(processCtx, binary)
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
		return nil, fmt.Errorf("start %s app-server: %w", binary, err)
	}

	s := &Session{
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

	var initialized json.RawMessage
	if err := s.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "korocon", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, &initialized); err != nil {
		s.Close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := s.notify("initialized", map[string]any{}); err != nil {
		s.Close()
		return nil, fmt.Errorf("notify codex initialized: %w", err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	sandbox := strings.TrimSpace(cfg.Sandbox)
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	approvalPolicy := strings.TrimSpace(cfg.ApprovalPolicy)
	if approvalPolicy == "" {
		approvalPolicy = "on-request"
	}
	if err := s.call(ctx, "thread/start", map[string]any{
		"cwd":            dir,
		"model":          emptyAsNil(cfg.Model),
		"sandbox":        sandbox,
		"approvalPolicy": approvalPolicy,
		"ephemeral":      true,
	}, &started); err != nil {
		s.Close()
		return nil, fmt.Errorf("start codex thread: %w", err)
	}
	if started.Thread.ID == "" {
		s.Close()
		return nil, errors.New("start codex thread: response did not include a thread id")
	}
	s.threadID = started.Thread.ID
	return s, nil
}

// RunTurn sends one prompt to the already-running Codex process. Calls are
// serialized because one thread can have only one active turn.
func (s *Session) RunTurn(ctx context.Context, prompt, model string, onEvent func()) (TurnResult, error) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if strings.TrimSpace(prompt) == "" {
		return TurnResult{}, errors.New("prompt is empty")
	}
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := s.call(ctx, "turn/start", map[string]any{
		"threadId": s.threadID,
		"input":    []map[string]string{{"type": "text", "text": prompt}},
		"model":    emptyAsNil(model),
	}, &started); err != nil {
		return TurnResult{}, fmt.Errorf("start codex turn: %w", err)
	}
	if started.Turn.ID == "" {
		return TurnResult{}, errors.New("start codex turn: response did not include a turn id")
	}

	turnID := started.Turn.ID
	var finalText string
	var tokens int
	for {
		select {
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		case <-s.done:
			return TurnResult{}, s.processError()
		case event := <-s.events:
			if onEvent != nil {
				onEvent()
			}
			switch event.Method {
			case "item/completed":
				var params struct {
					TurnID string `json:"turnId"`
					Item   struct {
						Type  string  `json:"type"`
						Text  string  `json:"text"`
						Phase *string `json:"phase"`
					} `json:"item"`
				}
				if json.Unmarshal(event.Params, &params) == nil && params.TurnID == turnID && params.Item.Type == "agentMessage" {
					if params.Item.Phase == nil || *params.Item.Phase == "final_answer" {
						finalText = params.Item.Text
					}
				}
			case "thread/tokenUsage/updated":
				var params struct {
					TurnID     string `json:"turnId"`
					TokenUsage struct {
						Last struct {
							Total int `json:"totalTokens"`
						} `json:"last"`
					} `json:"tokenUsage"`
				}
				if json.Unmarshal(event.Params, &params) == nil && params.TurnID == turnID {
					tokens = params.TokenUsage.Last.Total
				}
			case "turn/completed":
				var params struct {
					Turn struct {
						ID     string `json:"id"`
						Status string `json:"status"`
						Error  *struct {
							Message string `json:"message"`
						} `json:"error"`
						Items []struct {
							Type  string  `json:"type"`
							Text  string  `json:"text"`
							Phase *string `json:"phase"`
						} `json:"items"`
					} `json:"turn"`
				}
				if json.Unmarshal(event.Params, &params) != nil || params.Turn.ID != turnID {
					continue
				}
				for _, item := range params.Turn.Items {
					if item.Type == "agentMessage" && (item.Phase == nil || *item.Phase == "final_answer") {
						finalText = item.Text
					}
				}
				if params.Turn.Status != "completed" {
					message := params.Turn.Status
					if params.Turn.Error != nil && params.Turn.Error.Message != "" {
						message = params.Turn.Error.Message
					}
					return TurnResult{}, fmt.Errorf("codex turn %s: %s", params.Turn.Status, message)
				}
				return TurnResult{Text: finalText, Tokens: tokens}, nil
			}
		}
	}
}

// SetModel updates the model of the running Codex thread. This is the
// app-server protocol equivalent of the interactive TUI's /model command.
func (s *Session) SetModel(ctx context.Context, model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model is empty")
	}
	var updated json.RawMessage
	if err := s.call(ctx, "thread/settings/update", map[string]any{
		"threadId": s.threadID,
		"model":    model,
	}, &updated); err != nil {
		return fmt.Errorf("update Codex model: %w", err)
	}
	return nil
}

// Close stops the resident Codex process.
func (s *Session) Close() error {
	s.cancel()
	_ = s.stdin.Close()
	<-s.done
	err := s.processError()
	if errors.Is(err, context.Canceled) || errors.Is(s.ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

func (s *Session) call(ctx context.Context, method string, params any, result any) error {
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
	if err := s.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
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

func (s *Session) notify(method string, params any) error {
	return s.write(map[string]any{"method": method, "params": params})
}

func (s *Session) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write codex stdin: %w", err)
	}
	return nil
}

func (s *Session) read(src io.Reader) {
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

func (s *Session) handleServerRequest(message rpcMessage) {
	var result any
	var err error
	if s.handleRequest == nil {
		err = fmt.Errorf("client cannot handle %s", message.Method)
	} else {
		result, err = s.handleRequest(s.ctx, message.Method, message.Params)
	}
	response := map[string]any{"id": json.RawMessage(message.ID)}
	if err != nil {
		response["error"] = map[string]any{"code": -32603, "message": err.Error()}
	} else {
		response["result"] = result
	}
	_ = s.write(response)
}

func (s *Session) processError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.processErr == nil {
		return errors.New("codex app-server stopped")
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	return fmt.Errorf("codex app-server stopped: %w", s.processErr)
}

func emptyAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
