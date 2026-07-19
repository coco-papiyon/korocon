package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandRequestAllowed(t *testing.T) {
	params := json.RawMessage(`{
		"command": "/bin/bash -lc 'GOCACHE=/tmp/korocon-go-cache go test ./...'",
		"commandActions": [{"command": "GOCACHE=/tmp/korocon-go-cache go test ./..."}],
		"proposedExecpolicyAmendment": ["go", "test"]
	}`)
	if !commandRequestAllowed(params, []string{"go test"}) {
		t.Fatal("expected Codex go test approval to be allowed")
	}
}

func TestCommandRequestAllowedWithSafeArguments(t *testing.T) {
	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{name: "git add", command: "git add .", allowed: true},
		{name: "git diff", command: "git diff --stat", allowed: true},
		{name: "git status", command: "git status --short", allowed: true},
		{name: "go test", command: "go test ./...", allowed: true},
		{name: "safe and chain", command: "cd /tmp/worktree && go test ./...", allowed: true},
		{name: "safe or chain", command: "command -v code || command -v codium || true", allowed: true},
		{name: "stderr redirect", command: "go test -count=1 ./... 2>&1", allowed: true},
		{name: "quoted pipe", command: `grep -rn "claude-opus\\|gpt-5\\.6-sol" .`, allowed: true},
		{name: "pipeline", command: "git add . | rm -rf .", allowed: false},
		{name: "chain", command: "git diff --stat && rm -rf .", allowed: false},
		{name: "unclosed quote", command: `grep "value`, allowed: false},
		{name: "redirection", command: "git status > status.txt", allowed: false},
		{name: "command substitution", command: "go test $(malicious)", allowed: false},
		{name: "command-name prefix collision", command: "git different", allowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]string{"command": test.command})
			if err != nil {
				t.Fatal(err)
			}
			got := commandRequestAllowed(params, []string{"cd", "command -v", "true", "grep", "git add", "git diff", "git status", "go test"})
			if got != test.allowed {
				t.Fatalf("commandRequestAllowed(%q) = %v, want %v", test.command, got, test.allowed)
			}
		})
	}
}

func TestCommandRequestAllowedWithCommandAction(t *testing.T) {
	params := json.RawMessage(`{
		"command": "/bin/bash -lc 'go test ./...'",
		"commandActions": [{"command": "go test ./..."}]
	}`)
	if !commandRequestAllowed(params, []string{"go test"}) {
		t.Fatal("expected normalized command action to be allowed")
	}
}

func TestRequestedCopilotCommandsAreAllowed(t *testing.T) {
	allowed := []string{
		"command -v", "cd", "true", "grep", "sed", "go test",
		"git add", "git commit", "git --no-pager diff", "git --no-pager grep",
	}
	worktree := "/home/coco/dev/go/src/github.com/coco-papiyon/korocon-branches/korocon-21"
	commands := []string{
		"command -v code || command -v codium",
		`cd ` + worktree + ` && grep -rn "claude-opus\|gpt-5\.6-sol\|cloade" --include="*.go" .`,
		"cd " + worktree + " && git --no-pager diff --name-only",
		"cd " + worktree + " && go test ./cmd/korocon ./internal/runner",
		`cd ` + worktree + ` && git --no-pager grep -n "cloade-sonnet-4.6"`,
		`cd ` + worktree + ` && grep -n "cloade-sonnet-4\.6" README.md docs/usage.md docs/design.md`,
		`cd ` + worktree + ` && sed -i 's/cloade-sonnet-4\.6/claude-sonnet-4.6/g' README.md docs/usage.md docs/design.md`,
		"cd " + worktree + " && go test ./cmd/korocon ./internal/runner 2>&1",
		"cd " + worktree + " && go test -count=1 ./cmd/korocon ./internal/runner 2>&1",
		"cd " + worktree + " && git add README.md docs/usage.md docs/design.md cmd/korocon/config_test.go && git commit -m \"fix model spelling (#21)\"",
	}
	for _, command := range commands {
		params, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		if !commandRequestAllowed(params, allowed) {
			t.Errorf("expected command to be allowed: %s", command)
		}
	}
}

func TestRequestedCopilotWorktreePathIsAllowed(t *testing.T) {
	params, err := json.Marshal(map[string]any{"rawInput": map[string]string{
		"path": "/home/coco/dev/go/src/github.com/coco-papiyon/korocon-branches/korocon-21/cmd/korocon/config_test.go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"~/dev/go/src/github.com/coco-papiyon/korocon-branches/*/cmd/korocon/config_test.go"}
	if !copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected requested worktree path to be allowed")
	}
}

func TestCommandRequestAllowedWithPOSIXEnvironmentAssignment(t *testing.T) {
	params := json.RawMessage(`{"commandActions":[{"command":"GOCACHE=/tmp/korocon-go-cache go test ./..."}]}`)
	if !commandRequestAllowed(params, []string{"go test"}) {
		t.Fatal("expected go test with a safe environment assignment to be allowed")
	}

	params = json.RawMessage(`{"commandActions":[{"command":"GOCACHE=$(malicious) go test ./..."}]}`)
	if commandRequestAllowed(params, []string{"go test"}) {
		t.Fatal("expected command substitution in an environment assignment to be rejected")
	}
}

func TestApprovalCommandUsesConcreteCommandAction(t *testing.T) {
	params := json.RawMessage(`{
		"command":"/bin/bash -lc 'GOCACHE=/tmp/korocon-go-cache go test ./...'",
		"commandActions":[{"command":"GOCACHE=/tmp/korocon-go-cache go test ./..."}],
		"proposedExecpolicyAmendment":["/bin/bash","-lc","GOCACHE=/tmp/korocon-go-cache go test ./..."]
	}`)
	if got := approvalCommand(params); got != "go test ./..." {
		t.Fatalf("approvalCommand() = %q, want %q", got, "go test ./...")
	}
}

func TestApprovalCommandFallsBackToRequestedCommand(t *testing.T) {
	params := json.RawMessage(`{"command":"custom-tool --check"}`)
	if got := approvalCommand(params); got != "custom-tool --check" {
		t.Fatalf("approvalCommand() = %q", got)
	}
}

func TestCommandRequestAllowedRejectsInvalidRequest(t *testing.T) {
	if commandRequestAllowed(json.RawMessage(`{`), []string{"go test"}) {
		t.Fatal("expected invalid request to be rejected")
	}
	if commandRequestAllowed(json.RawMessage(`{"command":"go test ./..."}`), nil) {
		t.Fatal("expected empty allowlist to reject command")
	}
}

func TestCopilotPathRequestAllowed(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.ToSlash(filepath.Join(home, ".copilot", "session-state", "11ca99f2-0e72-4358-a7fb-609d71c64a95", "plan.md"))
	allowed := []string{"~/.copilot/session-state/*/plan.md"}
	params, _ := json.Marshal(map[string]any{"rawInput": map[string]string{"path": plan}})
	if !copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected Copilot plan path to be allowed")
	}
	params, _ = json.Marshal(map[string]any{"rawInput": map[string]string{"fileName": plan}})
	if !copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected Copilot plan fileName to be allowed")
	}
	params, _ = json.Marshal(map[string]any{"rawInput": map[string]string{"path": filepath.Join(home, ".ssh", "config")}})
	if copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected path outside Copilot session state to be rejected")
	}
}

func TestCopilotDiffRequestRequiresEveryTargetToBeAllowed(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.ToSlash(filepath.Join(home, ".copilot", "session-state", "session-1", "plan.md"))
	diff := "diff --git a" + plan + " b" + plan + "\n--- a/dev/null\n+++ b" + plan + "\n"
	params, _ := json.Marshal(map[string]any{"rawInput": map[string]string{"diff": diff, "fileName": plan}})
	allowed := []string{"~/.copilot/session-state/*/plan.md"}
	if !copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected Copilot plan diff to be allowed")
	}
	params, _ = json.Marshal(map[string]any{"rawInput": map[string]string{"diff": diff, "fileName": filepath.Join(home, ".ssh", "config")}})
	if copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected allowed diff with mismatched fileName to be rejected")
	}

	other := filepath.ToSlash(filepath.Join(home, ".ssh", "config"))
	mixed := diff + "diff --git a" + other + " b" + other + "\n"
	params, _ = json.Marshal(map[string]any{"rawInput": map[string]string{"diff": mixed}})
	if copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected mixed diff targets to be rejected")
	}
	params, _ = json.Marshal(map[string]any{"rawInput": map[string]string{"diff": "not a unified diff"}})
	if copilotPathRequestAllowed(params, allowed) {
		t.Fatal("expected diff without target headers to be rejected")
	}
}
