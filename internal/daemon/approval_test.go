package daemon

import (
	"encoding/json"
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
	if !commandRequestAllowed(json.RawMessage(`{"command":"git diff --stat"}`), []string{"git diff"}) {
		t.Fatal("expected git diff arguments to be allowed")
	}
	if commandRequestAllowed(json.RawMessage(`{"command":"git diff --stat && rm -rf ."}`), []string{"git diff"}) {
		t.Fatal("expected chained command to be rejected")
	}
	if commandRequestAllowed(json.RawMessage(`{"command":"git different"}`), []string{"git diff"}) {
		t.Fatal("expected command-name prefix collision to be rejected")
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
