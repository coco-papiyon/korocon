package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appconfig "github.com/coco-papiyon/korocon/internal/config"
)

func TestInitializeConfigPromptsSettingsAndModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	input := strings.Join([]string{
		"develop",
		"",
		"go run ./cmd/app",
		"",
		"2",
		"inherit",
		"inherit",
		"copilot",
		"1",
	}, "\n") + "\n"
	var out strings.Builder
	if err := initializeConfig(strings.NewReader(input), &out, path, false); err != nil {
		t.Fatal(err)
	}

	configured := readSavedConfig(t, path)
	if configured.BaseBranch != "develop" || configured.BranchNamePattern != "issue_#<issue番号>" || configured.StartupCommand != "go run ./cmd/app" {
		t.Fatalf("general settings = %+v", configured)
	}
	if configured.ImplementerProvider != "codex" || configured.ImplementerModel != "gpt-5.6-terra" {
		t.Fatalf("implementer settings = %+v", configured)
	}
	if configured.VerifierProvider != "" || configured.VerifierModel != "" {
		t.Fatalf("verifier settings = %+v", configured)
	}
	if configured.ReviewerProvider != "copilot" || configured.ReviewerModel != "auto" {
		t.Fatalf("reviewer settings = %+v", configured)
	}
	for _, prompt := range []string{"baseBranch [main]:", "branchNamePattern [issue_#<issue番号>]:", "startupCommand [未設定]:", "実装者Provider", "検証者Model", "レビューアModel"} {
		if !strings.Contains(out.String(), prompt) {
			t.Fatalf("output does not contain %q: %q", prompt, out.String())
		}
	}
}

func TestPrintConfigList(t *testing.T) {
	configured := appconfig.Default()
	configured.ImplementerProvider = "copilot"
	configured.ImplementerModel = "auto"
	configured.BuiltinAllowedCommands = []string{"go test ./...", "git status"}
	configured.BuiltinAllowedPaths = []string{"~/.copilot/session-state/*/plan.md"}
	var out strings.Builder
	if err := printConfigList(&out, "/tmp/korocon/config.json", configured); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"設定一覧",
		"config: /tmp/korocon/config.json",
		"implementerProvider: copilot",
		"implementerModel: auto",
		"  - go test ./...",
		"  - git status",
		"builtinAllowedPaths:",
		"  - ~/.copilot/session-state/*/plan.md",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output does not contain %q: %q", want, out.String())
		}
	}
}

func TestConfigureModelsUsesAutoWhenImplementerChangesToCopilot(t *testing.T) {
	configured := appconfig.Default()
	input := strings.Join([]string{"copilot", "", "", "", "", ""}, "\n") + "\n"
	var out strings.Builder
	got, err := configureModels(bufio.NewReader(strings.NewReader(input)), &out, configured)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImplementerProvider != "copilot" || got.ImplementerModel != "auto" {
		t.Fatalf("implementer settings = %+v", got)
	}
	if !strings.Contains(out.String(), "実装者で選択可能なモデル (provider: copilot):\n1. auto") {
		t.Fatalf("output = %q", out.String())
	}
	for _, model := range []string{"1. auto", "2. gpt-5.6-sol", "3. gpt-5.6-terra", "4. gpt-5.6-luna", "5. gpt-5-mini", "6. cloade-sonnet-4.6", "7. claude-opus-4.6"} {
		if !strings.Contains(out.String(), model) {
			t.Fatalf("Copilot model %q was not displayed: %q", model, out.String())
		}
	}
}

func TestInitializeConfigUsesDefaultsForEmptyInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := initializeConfig(strings.NewReader(strings.Repeat("\n", 9)), &strings.Builder{}, path, false); err != nil {
		t.Fatal(err)
	}
	configured := readSavedConfig(t, path)
	defaults := appconfig.Default()
	if configured.BaseBranch != defaults.BaseBranch || configured.BranchNamePattern != defaults.BranchNamePattern || configured.StartupCommand != defaults.StartupCommand ||
		configured.ImplementerProvider != defaults.ImplementerProvider || configured.ImplementerModel != defaults.ImplementerModel ||
		configured.VerifierProvider != "" || configured.VerifierModel != "" || configured.ReviewerProvider != "" || configured.ReviewerModel != "" {
		t.Fatalf("config = %+v, want defaults %+v", configured, defaults)
	}
}

func TestInitializeConfigRequiresForceToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initializeConfig(strings.NewReader(""), &strings.Builder{}, path, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigureModelsKeepsCurrentValuesOnEmptyInput(t *testing.T) {
	configured := appconfig.Default()
	configured.VerifierProvider = "codex"
	configured.VerifierModel = "gpt-5.4"
	configured.ReviewerProvider = "copilot"
	configured.ReviewerModel = "custom-review-model"
	got, err := configureModels(bufio.NewReader(strings.NewReader(strings.Repeat("\n", 6))), &strings.Builder{}, configured)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, configured) {
		t.Fatalf("configured = %+v, want %+v", got, configured)
	}
}

func TestAddBuiltinAllowedCommandSavesNormalizedCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configured := appconfig.Default()
	var out strings.Builder
	if err := addBuiltinAllowedCommand(configured, path, "  go test ./...  ", &out); err != nil {
		t.Fatal(err)
	}
	saved := readSavedConfig(t, path)
	if got := saved.BuiltinAllowedCommands[len(saved.BuiltinAllowedCommands)-1]; got != "go test ./..." {
		t.Fatalf("last allowed command = %q", got)
	}
	if !strings.Contains(out.String(), "自動承認コマンドを追加しました: go test ./...") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAddBuiltinAllowedCommandDoesNotSaveDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configured := appconfig.Default()
	var out strings.Builder
	if err := addBuiltinAllowedCommand(configured, path, "GO   TEST", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("duplicate command unexpectedly saved config: %v", err)
	}
	if !strings.Contains(out.String(), "すでに登録されています") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAddBuiltinAllowedPathSavesPattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configured := appconfig.Default()
	var out strings.Builder
	if err := addBuiltinAllowedPath(configured, path, " /tmp/copilot/*/plan.md ", &out); err != nil {
		t.Fatal(err)
	}
	saved := readSavedConfig(t, path)
	if got := saved.BuiltinAllowedPaths[len(saved.BuiltinAllowedPaths)-1]; got != "/tmp/copilot/*/plan.md" {
		t.Fatalf("last allowed path = %q", got)
	}
	if !strings.Contains(out.String(), "自動承認パスを追加しました: /tmp/copilot/*/plan.md") {
		t.Fatalf("output = %q", out.String())
	}
}

func readSavedConfig(t *testing.T, path string) appconfig.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var configured appconfig.Config
	if err := json.Unmarshal(data, &configured); err != nil {
		t.Fatal(err)
	}
	return configured
}
