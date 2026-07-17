package implementation

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/runner"
)

type fakeSession struct {
	results []runner.TurnResult
	prompts []string
	closed  bool
}

func TestEnsureWorktreeCreatesConfiguredBranch(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		runGit(t, repository, args...)
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")

	engine := New(Config{RepositoryDir: repository, ImplementationDirectory: "../", BranchNamePattern: "issue_#<issue番号>", IssueNumber: 9})
	path, branch, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(filepath.Dir(repository), "repo-9") || branch != "issue_#9" {
		t.Fatalf("path=%q branch=%q", path, branch)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree was not created: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", command...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func (s *fakeSession) RunTurn(_ context.Context, prompt, _ string, _ func()) (runner.TurnResult, error) {
	s.prompts = append(s.prompts, prompt)
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func (s *fakeSession) Close() error {
	s.closed = true
	return nil
}

func TestEngineRepeatsImplementationUntilVerificationPasses(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "sample-repo")
	worktree := filepath.Join(filepath.Dir(repository), "sample-repo-42")
	if err := os.MkdirAll(filepath.Join(repository, ".workspace", "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".workspace", "design", "42_add-feature.md"), []byte("# Add feature\n\ndesign"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementer := &fakeSession{results: []runner.TurnResult{{Text: "first", Tokens: 2}, {Text: "second", Tokens: 3}}}
	verifier := &fakeSession{results: []runner.TurnResult{
		{Text: `{"status":"changes_requested","feedback":"fix tests","summary":"failed"}`, Tokens: 5},
		{Text: `{"status":"passed","feedback":"","summary":"確認済み"}`, Tokens: 7},
	}}
	oldStart := startCodexSession
	var configs []runner.SessionConfig
	startCodexSession = func(_ context.Context, cfg runner.SessionConfig) (codexSession, error) {
		configs = append(configs, cfg)
		if len(configs) == 1 {
			return implementer, nil
		}
		return verifier, nil
	}
	defer func() { startCodexSession = oldStart }()

	engine := New(Config{
		RepositoryDir: repository, WorkspaceName: ".workspace", ImplementationDirectory: "../",
		BranchNamePattern: "issue_#<issue番号>", LoopCount: 3,
		IssueNumber: 42, IssueTitle: "Add feature", IssueContext: "issue context",
		LogOut: io.Discard, LogErr: io.Discard,
	})
	var phases []string
	result, err := engine.Run(context.Background(), "implement", "gpt-test", nil, func(phase string) {
		phases = append(phases, phase)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tokens != 17 || !strings.Contains(result.Text, "second") || !strings.Contains(result.Text, "確認済み") {
		t.Fatalf("result = %+v", result)
	}
	if len(implementer.prompts) != 2 || !strings.Contains(implementer.prompts[1], "fix tests") {
		t.Fatalf("implementation prompts = %#v", implementer.prompts)
	}
	if strings.Join(phases, ",") != "実装1回目,検証1回目,実装2回目,検証2回目" {
		t.Fatalf("phases = %v", phases)
	}
	if len(configs) != 2 || configs[0].Sandbox != "workspace-write" || configs[1].Sandbox != "read-only" {
		t.Fatalf("session configs = %+v", configs)
	}
	if err := engine.Close(); err != nil || !implementer.closed || !verifier.closed {
		t.Fatalf("sessions were not closed: err=%v implementation=%v verifier=%v", err, implementer.closed, verifier.closed)
	}
}

func TestEnsureWorktreeSkipsExistingDirectory(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repo")
	worktree := filepath.Join(filepath.Dir(repository), "repo-7")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	engine := New(Config{RepositoryDir: repository, ImplementationDirectory: "../", BranchNamePattern: "feature/<issueNumber>", IssueNumber: 7})
	path, branch, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != worktree || branch != "feature/7" {
		t.Fatalf("path=%q branch=%q", path, branch)
	}
}

func TestParseVerificationRejectsUnknownStatus(t *testing.T) {
	if _, err := parseVerification(`{"status":"unknown"}`); err == nil {
		t.Fatal("unknown verification status was accepted")
	}
}
