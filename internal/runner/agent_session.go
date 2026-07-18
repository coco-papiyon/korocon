package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type AgentSession interface {
	RunTurn(context.Context, string, string, func()) (TurnResult, error)
	Close() error
}

func StartAgentSession(ctx context.Context, cfg SessionConfig) (AgentSession, error) {
	provider := normalizeProvider(cfg.Provider)
	if provider == "codex" {
		return StartSession(ctx, cfg)
	}
	if provider != "copilot" {
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
	dir, err := workingDir(cfg.WorkingDir)
	if err != nil {
		return nil, err
	}
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = "copilot"
	}
	return &commandSession{provider: provider, binary: binary, model: cfg.Model, workingDir: dir, logOut: writerOrDiscard(cfg.LogOut), logErr: writerOrDiscard(cfg.LogErr)}, nil
}

type commandSession struct {
	provider   string
	binary     string
	model      string
	workingDir string
	logOut     io.Writer
	logErr     io.Writer
}

func (s *commandSession) RunTurn(ctx context.Context, prompt, model string, onEvent func()) (TurnResult, error) {
	if strings.TrimSpace(model) == "" {
		model = s.model
	}
	args, err := BuildArgs(Request{Provider: s.provider, Prompt: prompt, Model: model})
	if err != nil {
		return TurnResult{}, err
	}
	cmd := exec.CommandContext(ctx, s.binary, args...)
	cmd.Dir = s.workingDir
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, s.logOut)
	cmd.Stderr = s.logErr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return TurnResult{}, ctx.Err()
		}
		return TurnResult{}, fmt.Errorf("run %s: %w", s.binary, err)
	}
	if onEvent != nil {
		onEvent()
	}
	return TurnResult{Text: strings.TrimSpace(output.String())}, nil
}

func (s *commandSession) Close() error { return nil }

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "codex":
		return "codex"
	case "copilot", "github_copilot", "github-copilot":
		return "copilot"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

var _ AgentSession = (*Session)(nil)
var _ AgentSession = (*commandSession)(nil)
