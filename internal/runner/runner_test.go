package runner

import (
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	args, err := BuildArgs(Request{Provider: "copilot", Prompt: `review "this"; do not execute`, Model: "gpt-5.6-luna", AllowAllTools: true})
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

func TestBuildArgsCodex(t *testing.T) {
	args, err := BuildArgs(Request{Provider: "codex", Prompt: "hello", Model: "gpt-5.6-luna", AllowAllTools: true})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"exec", "--json", "--sandbox", "workspace-write", "--model", "gpt-5.6-luna", "hello"}
	if strings.Join(args, " ") != strings.Join(expected, " ") {
		t.Fatalf("unexpected Codex args: %v", args)
	}
}

func TestBuildArgsSupportsAvailableModels(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"} {
		args, err := BuildArgs(Request{Prompt: "hello", Model: model})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--model "+model) {
			t.Fatalf("model %s was not passed: %v", model, args)
		}
	}
}

func TestAvailableModelsOrder(t *testing.T) {
	expected := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}
	if strings.Join(AvailableModels, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected available model order: %v", AvailableModels)
	}
}

func TestAvailableCopilotModelsIncludesAuto(t *testing.T) {
	if strings.Join(AvailableCopilotModels, "\n") != "auto" {
		t.Fatalf("unexpected Copilot model choices: %v", AvailableCopilotModels)
	}
	args, err := BuildArgs(Request{Provider: "copilot", Prompt: "hello", Model: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--model auto") {
		t.Fatalf("Copilot auto model was not passed: %v", args)
	}
}
