package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileUsesDefaultWhenMissing(t *testing.T) {
	configured, err := loadFile(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if configured.WorkspaceName != ".workspace" {
		t.Fatalf("workspaceName = %q", configured.WorkspaceName)
	}
	if configured.BranchNamePattern != "issue_#<issue番号>" || configured.ImplementationDirectory != "../" || configured.ImplementationLoopCount != 3 {
		t.Fatalf("defaults = %+v", configured)
	}
}

func TestLoadFileReadsImplementationSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	content := []byte(`{"workspaceName":".workspace","branchNamePattern":"feature/<issueNumber>","implementationDirectory":"../worktrees","implementationLoopCount":5}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured.BranchNamePattern != "feature/<issueNumber>" || configured.ImplementationDirectory != "../worktrees" || configured.ImplementationLoopCount != 5 {
		t.Fatalf("config = %+v", configured)
	}
}

func TestLoadFileCapsImplementationLoopCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"implementationLoopCount":20}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured.ImplementationLoopCount != 10 {
		t.Fatalf("implementationLoopCount = %d", configured.ImplementationLoopCount)
	}
}

func TestLoadFileReadsWorkspaceName(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"workspaceName":".korocon-workspace"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured.WorkspaceName != ".korocon-workspace" {
		t.Fatalf("workspaceName = %q", configured.WorkspaceName)
	}
}

func TestLoadFileRejectsUnsafeWorkspaceName(t *testing.T) {
	for _, name := range []string{"", "..", "path/to/workspace", `path\to\workspace`} {
		path := filepath.Join(t.TempDir(), FileName)
		content, err := json.Marshal(Config{WorkspaceName: name})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadFile(path); err == nil {
			t.Fatalf("workspaceName %q was accepted", name)
		}
	}
}
