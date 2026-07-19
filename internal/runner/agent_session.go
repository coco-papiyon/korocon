package runner

import (
	"context"
	"fmt"
	"strings"
)

type AgentSession interface {
	RunTurn(context.Context, string, string, func()) (TurnResult, error)
	Close() error
}

// ModelSession is implemented by resident providers that can change the model
// without replacing their process or conversation.
type ModelSession interface {
	SetModel(context.Context, string) error
}

func StartAgentSession(ctx context.Context, cfg SessionConfig) (AgentSession, error) {
	provider := normalizeProvider(cfg.Provider)
	if provider == "codex" {
		return StartSession(ctx, cfg)
	}
	if provider == "copilot" {
		return StartCopilotSession(ctx, cfg)
	}
	return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
}

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
var _ AgentSession = (*CopilotSession)(nil)
