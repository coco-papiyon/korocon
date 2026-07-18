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
		"6",
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
	if configured.ReviewerProvider != "copilot" || configured.ReviewerModel != "gpt-5.4-mini" {
		t.Fatalf("reviewer settings = %+v", configured)
	}
	for _, prompt := range []string{"baseBranch [main]:", "branchNamePattern [issue_#<issue番号>]:", "startupCommand [未設定]:", "実装者Provider", "検証者Model", "レビューアModel"} {
		if !strings.Contains(out.String(), prompt) {
			t.Fatalf("output does not contain %q: %q", prompt, out.String())
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
