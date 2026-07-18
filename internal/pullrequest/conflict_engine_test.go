package pullrequest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictEnginePreparesMergeAndPublishesResolution(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repository := filepath.Join(root, "repository")
	runGitCommand(t, root, "init", "--bare", remote)
	runGitCommand(t, root, "clone", remote, repository)
	runGitCommand(t, repository, "config", "user.email", "test@example.com")
	runGitCommand(t, repository, "config", "user.name", "Test User")
	runGitCommand(t, repository, "checkout", "-b", "main")
	writeTestFile(t, filepath.Join(repository, "conflict.txt"), "initial\n")
	runGitCommand(t, repository, "add", "conflict.txt")
	runGitCommand(t, repository, "commit", "-m", "initial")
	runGitCommand(t, repository, "push", "-u", "origin", "main")

	runGitCommand(t, repository, "checkout", "-b", "feature/12")
	writeTestFile(t, filepath.Join(repository, "conflict.txt"), "head\n")
	runGitCommand(t, repository, "commit", "-am", "head change")
	runGitCommand(t, repository, "push", "-u", "origin", "feature/12")
	runGitCommand(t, repository, "checkout", "main")
	writeTestFile(t, filepath.Join(repository, "conflict.txt"), "base\n")
	runGitCommand(t, repository, "commit", "-am", "base change")
	runGitCommand(t, repository, "push", "origin", "main")

	engine := NewFixEngine(FixConfig{
		RepositoryDir: repository, ImplementationDirectory: filepath.Join(root, "worktrees"),
		Number: 12, HeadRefName: "feature/12", BaseRefName: "main",
	})
	worktree, err := engine.Worktree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.prepareConflictMerge(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine.conflictPrepared = true
	files, err := engine.conflictFiles(context.Background())
	if err != nil || files != "conflict.txt" {
		t.Fatalf("conflict files = %q, err=%v", files, err)
	}
	if err := engine.PublishConflict(context.Background(), "result"); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("unresolved conflict publish error = %v", err)
	}
	writeTestFile(t, filepath.Join(worktree, "conflict.txt"), "resolved\n")
	if err := engine.PublishConflict(context.Background(), "result"); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repository, "fetch", "origin", "feature/12")
	content := gitCommandOutput(t, repository, "show", "origin/feature/12:conflict.txt")
	if strings.TrimSpace(content) != "resolved" {
		t.Fatalf("published content = %q", content)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitCommandOutput(t, dir, args...)
}

func gitCommandOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
