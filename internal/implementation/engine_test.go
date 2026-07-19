package implementation

import (
	"context"
	"errors"
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

	engine := New(Config{RepositoryDir: repository, BranchNamePattern: "issue_#<issue番号>", IssueNumber: 9})
	path, branch, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(filepath.Dir(repository), "repo-branches", "repo-9") || branch != "issue_#9" {
		t.Fatalf("path=%q branch=%q", path, branch)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree was not created: %v", err)
	}
}

func TestEnsureWorktreePrunesRegistrationWhenDirectoryWasRemoved(t *testing.T) {
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

	engine := New(Config{RepositoryDir: repository, ImplementationDirectory: "../", BranchNamePattern: "issue_#<issue番号>", IssueNumber: 10})
	path, _, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	path, branch, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if branch != "issue_#10" {
		t.Fatalf("branch = %q", branch)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatalf("worktree was not recreated: %v", err)
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
	if err := os.WriteFile(filepath.Join(repository, ".workspace", "design", "42_add-feature.md"), []byte("# 設計結果\n\nmanual design edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementer := &fakeSession{results: []runner.TurnResult{{Text: "first", Tokens: 2}, {Text: "second", Tokens: 3}}}
	verifier := &fakeSession{results: []runner.TurnResult{
		{Text: `{"status":"changes_requested","feedback":"fix tests","summary":"failed"}`, Tokens: 5},
		{Text: `{"status":"passed","feedback":"","summary":"確認済み"}`, Tokens: 7},
	}}
	oldStart := startImplementationSession
	var configs []runner.SessionConfig
	startImplementationSession = func(_ context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
		configs = append(configs, cfg)
		if len(configs) == 1 {
			return implementer, nil
		}
		return verifier, nil
	}
	defer func() { startImplementationSession = oldStart }()

	engine := New(Config{
		Provider: "copilot", VerifierProvider: "codex", VerifierModel: "gpt-verifier",
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
	if !strings.Contains(implementer.prompts[0], "manual design edit") {
		t.Fatalf("workspace design was not passed to implementation: %q", implementer.prompts[0])
	}
	if strings.Join(phases, ",") != "実装1回目,検証1回目,実装2回目,検証2回目" {
		t.Fatalf("phases = %v", phases)
	}
	if len(configs) != 2 || configs[0].Provider != "copilot" || configs[0].Model != "gpt-test" || configs[0].Sandbox != "workspace-write" || !configs[0].ApproveWorkingDirPaths ||
		configs[1].Provider != "codex" || configs[1].Model != "gpt-verifier" || configs[1].Sandbox != "read-only" || !configs[1].ApproveWorkingDirPaths {
		t.Fatalf("session configs = %+v", configs)
	}
	artifacts := map[string]string{
		"1回目_実装.md": "# Add feature 実装 1回目\n\nfirst",
		"1回目_検討.md": "# Add feature 検討 1回目\n\n" + `{"status":"changes_requested","feedback":"fix tests","summary":"failed"}`,
		"2回目_実装.md": "# Add feature 実装 2回目\n\nsecond",
		"2回目_検討.md": "# Add feature 検討 2回目\n\n" + `{"status":"passed","feedback":"","summary":"確認済み"}`,
	}
	for name, want := range artifacts {
		raw, err := os.ReadFile(filepath.Join(repository, ".workspace", "implementation", "42", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(raw) != want {
			t.Fatalf("artifact %s = %q, want %q", name, raw, want)
		}
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

func TestPublishCommitsPushesAndCreatesPullRequest(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "init", "--bare")
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")
	runGit(t, repository, "branch", "-M", "main")
	runGit(t, repository, "remote", "add", "origin", remote)

	engine := New(Config{
		RepositoryDir: repository, ImplementationDirectory: "../", BranchNamePattern: "issue_#<issue番号>",
		BaseBranch: "main", IssueNumber: 42, IssueTitle: "Add feature", Reviewer: "octocat",
	})
	worktree, branch, err := engine.ensureWorktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	engine.worktree, engine.branch = worktree, branch
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("implemented\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(repository, ".workspace", "implementation")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "42_add-feature.md"), []byte("# Add feature\n\nsaved implementation result"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldRunGH := runGHCommand
	var createArgs []string
	runGHCommand = func(_ context.Context, dir string, args ...string) ([]byte, error) {
		if dir != worktree {
			t.Fatalf("gh dir = %q, want %q", dir, worktree)
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
			return nil, errors.New("no pull request")
		}
		createArgs = append([]string(nil), args...)
		return []byte("https://github.com/acme/repo/pull/42\n"), nil
	}
	defer func() { runGHCommand = oldRunGH }()

	url, err := engine.Publish(context.Background(), "## 概要\nimplemented")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/repo/pull/42" {
		t.Fatalf("url = %q", url)
	}
	args := strings.Join(createArgs, " ")
	for _, expected := range []string{"pr create", "--base main", "--title Add feature", "--head issue_#42", "--assignee @me", "--reviewer octocat", "# 実装結果", "saved implementation result", "Closes #42"} {
		if !strings.Contains(args, expected) {
			t.Fatalf("gh args do not contain %q: %q", expected, args)
		}
	}
	if strings.Contains(args, "## 概要") {
		t.Fatalf("PR body used the transient result instead of the saved artifact: %q", args)
	}
	cmd := exec.Command("git", "--git-dir", remote, "rev-parse", "refs/heads/"+branch)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remote branch was not pushed: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(worktree, "feature.txt")); err != nil {
		t.Fatal(err)
	}
	status := exec.Command("git", "-C", worktree, "status", "--porcelain")
	if output, err := status.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("worktree is not clean: err=%v output=%s", err, output)
	}
}

func TestPublishReusesExistingPullRequest(t *testing.T) {
	engine := New(Config{IssueNumber: 1, IssueTitle: "title"})
	engine.worktree = t.TempDir()
	engine.branch = "issue_#1"
	oldRunGH := runGHCommand
	runGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "view" {
			return []byte("https://github.com/acme/repo/pull/1\n"), nil
		}
		t.Fatalf("unexpected gh command: %v", args)
		return nil, nil
	}
	defer func() { runGHCommand = oldRunGH }()
	// Exercise only the idempotent lookup directly; git publication is covered above.
	if url, ok := engine.existingPullRequest(context.Background()); !ok || url != "https://github.com/acme/repo/pull/1" {
		t.Fatalf("existingPullRequest() = (%q, %v)", url, ok)
	}
}

func TestBuildPullRequestCreateArgsOmitsUnsetReviewer(t *testing.T) {
	args := strings.Join(buildPullRequestCreateArgs("main", "title", "body", "branch", "  "), " ")
	if !strings.Contains(args, "--assignee @me") {
		t.Fatalf("PR assignee was not set: %q", args)
	}
	if strings.Contains(args, "--reviewer") {
		t.Fatalf("unset reviewer was added: %q", args)
	}
}

func TestPullRequestURLUsesLastURLLine(t *testing.T) {
	output := "warning: branch was already pushed\nhttps://github.com/acme/repo/pull/8\n"
	if got := pullRequestURL(output); got != "https://github.com/acme/repo/pull/8" {
		t.Fatalf("pullRequestURL() = %q", got)
	}
}

func TestParseVerificationRejectsUnknownStatus(t *testing.T) {
	if _, err := parseVerification(`{"status":"unknown"}`); err == nil {
		t.Fatal("unknown verification status was accepted")
	}
}
