package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	if configured.BranchNamePattern != "issue_#<issue番号>" || configured.ImplementationDirectory != "../<リポジトリ名>-branches/" || configured.ImplementationLoopCount != 3 {
		t.Fatalf("defaults = %+v", configured)
	}
	if configured.BaseBranch != "main" {
		t.Fatalf("baseBranch = %q", configured.BaseBranch)
	}
	if !reflect.DeepEqual(configured.BuiltinAllowedCommands, DefaultAllowedCommands()) {
		t.Fatalf("builtinAllowedCommands = %+v", configured.BuiltinAllowedCommands)
	}
}

func TestDefaultAllowedCommandsMatchesKorobokcle(t *testing.T) {
	want := []string{
		"npm install", "npm ci", "npm test",
		"go build", "go test", "go mod tidy", "go mod download",
		"git log", "git diff", "git status", "git stash",
		"ls", "dir", "cat", "type", "more", "head", "echo", "sed", "set", "pwd", "grep", "find", "tee", "wc",
		"get-childitem", "get-content", "select-object", "select-string",
	}
	if got := DefaultAllowedCommands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultAllowedCommands() = %+v, want %+v", got, want)
	}
}

func TestLoadFileNormalizesBuiltinAllowedCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	content := []byte(`{"builtinAllowedCommands":[" go test ","GO   TEST","","git diff"]}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go test", "git diff"}
	if !reflect.DeepEqual(configured.BuiltinAllowedCommands, want) {
		t.Fatalf("builtinAllowedCommands = %+v, want %+v", configured.BuiltinAllowedCommands, want)
	}
}

func TestAddBuiltinAllowedCommandAndSave(t *testing.T) {
	configured := Default()
	updated, added := AddBuiltinAllowedCommand(configured, "go test ./...")
	if !added {
		t.Fatal("expected a new command to be added")
	}
	updated, added = AddBuiltinAllowedCommand(updated, " GO   TEST ./... ")
	if added {
		t.Fatal("expected normalized duplicate command not to be added")
	}

	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, updated); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.BuiltinAllowedCommands, updated.BuiltinAllowedCommands) {
		t.Fatalf("saved commands = %+v, want %+v", loaded.BuiltinAllowedCommands, updated.BuiltinAllowedCommands)
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

func TestLoadFileReadsBaseBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"baseBranch":"develop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configured, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if configured.BaseBranch != "develop" {
		t.Fatalf("baseBranch = %q", configured.BaseBranch)
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
