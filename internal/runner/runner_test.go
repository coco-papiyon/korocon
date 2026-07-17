package runner

import (
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	args, err := BuildArgs(Request{Prompt: `review "this"; do not execute`, Model: "gpt-5.2-codex", AllowAllTools: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `-p review "this"; do not execute`) {
		t.Fatalf("prompt was not preserved: %v", args)
	}
	if !strings.Contains(joined, "--allow-all-tools") {
		t.Fatalf("permission flag missing: %v", args)
	}
}

func TestBuildArgsRejectsUnsupportedProvider(t *testing.T) {
	if _, err := BuildArgs(Request{Provider: "unknown", Prompt: "hello"}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}
