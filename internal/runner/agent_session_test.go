package runner

import (
	"context"
	"strings"
	"testing"
)

func TestStartAgentSessionRunsCopilotCompatibleProvider(t *testing.T) {
	session, err := StartAgentSession(context.Background(), SessionConfig{
		Provider: "github_copilot", Binary: "/bin/echo", Model: "model-a", WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(context.Background(), "hello", "model-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "-p hello") || !strings.Contains(result.Text, "--model model-b") {
		t.Fatalf("result = %q", result.Text)
	}
}

func TestStartAgentSessionRejectsUnsupportedProvider(t *testing.T) {
	if _, err := StartAgentSession(context.Background(), SessionConfig{Provider: "unknown", WorkingDir: t.TempDir()}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}
