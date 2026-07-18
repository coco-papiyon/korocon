package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const FileName = "config.json"

const defaultImplementationDirectory = "../<リポジトリ名>-branches/"

type Config struct {
	WorkspaceName           string   `json:"workspaceName"`
	BranchNamePattern       string   `json:"branchNamePattern"`
	ImplementationDirectory string   `json:"implementationDirectory"`
	ImplementationLoopCount int      `json:"implementationLoopCount"`
	BaseBranch              string   `json:"baseBranch"`
	StartupCommand          string   `json:"startupCommand,omitempty"`
	BuiltinAllowedCommands  []string `json:"builtinAllowedCommands"`
	ImplementerProvider     string   `json:"implementerProvider"`
	ImplementerModel        string   `json:"implementerModel"`
	VerifierProvider        string   `json:"verifierProvider,omitempty"`
	VerifierModel           string   `json:"verifierModel,omitempty"`
	ReviewerProvider        string   `json:"reviewerProvider,omitempty"`
	ReviewerModel           string   `json:"reviewerModel,omitempty"`
}

var defaultAllowedCommands = []string{
	"npm install", "npm ci", "npm test",
	"go build", "go test", "go mod tidy", "go mod download",
	"git log", "git diff", "git status", "git stash",
	"ls", "dir", "cat", "type", "more", "head", "echo", "sed", "set", "pwd", "grep", "find", "tee", "wc",
	"get-childitem", "get-content", "select-object", "select-string",
}

func Default() Config {
	return Config{
		WorkspaceName: ".workspace", BranchNamePattern: "issue_#<issue番号>",
		ImplementationDirectory: defaultImplementationDirectory, ImplementationLoopCount: 3, BaseBranch: "main",
		BuiltinAllowedCommands: DefaultAllowedCommands(),
		ImplementerProvider:    "codex", ImplementerModel: "gpt-5.6-luna",
	}
}

func DefaultAllowedCommands() []string {
	return append([]string(nil), defaultAllowedCommands...)
}

// Load reads config.json from the directory containing the korocon binary.
// A missing file uses defaults so installed binaries remain self-contained.
func Load() (Config, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return Config{}, "", fmt.Errorf("resolve executable path: %w", err)
	}
	path := filepath.Join(filepath.Dir(executable), FileName)
	configured, err := loadFile(path)
	return configured, path, err
}

func Save(path string, configured Config) error {
	data, err := json.MarshalIndent(configured, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config %q: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

func AddBuiltinAllowedCommand(configured Config, command string) (Config, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return configured, false
	}
	before := len(normalizeStringList(configured.BuiltinAllowedCommands))
	configured.BuiltinAllowedCommands = normalizeStringList(append(configured.BuiltinAllowedCommands, command))
	return configured, len(configured.BuiltinAllowedCommands) > before
}

func loadFile(path string) (Config, error) {
	configured := Default()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return configured, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configured); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	configured.WorkspaceName = strings.TrimSpace(configured.WorkspaceName)
	if err := validateWorkspaceName(configured.WorkspaceName); err != nil {
		return Config{}, fmt.Errorf("config workspaceName: %w", err)
	}
	configured.BranchNamePattern = strings.TrimSpace(configured.BranchNamePattern)
	if configured.BranchNamePattern == "" {
		configured.BranchNamePattern = "issue_#<issue番号>"
	}
	configured.ImplementationDirectory = strings.TrimSpace(configured.ImplementationDirectory)
	if configured.ImplementationDirectory == "" {
		configured.ImplementationDirectory = defaultImplementationDirectory
	}
	if configured.ImplementationLoopCount <= 0 {
		configured.ImplementationLoopCount = 3
	}
	if configured.ImplementationLoopCount > 10 {
		configured.ImplementationLoopCount = 10
	}
	configured.BaseBranch = strings.TrimSpace(configured.BaseBranch)
	if configured.BaseBranch == "" {
		configured.BaseBranch = "main"
	}
	configured.StartupCommand = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(configured.StartupCommand, "\r\n", "\n"), "\r", "\n"))
	configured.BuiltinAllowedCommands = normalizeStringList(configured.BuiltinAllowedCommands)
	if len(configured.BuiltinAllowedCommands) == 0 {
		configured.BuiltinAllowedCommands = DefaultAllowedCommands()
	}
	configured.ImplementerProvider, err = normalizeProvider(configured.ImplementerProvider, "codex")
	if err != nil {
		return Config{}, fmt.Errorf("config implementerProvider: %w", err)
	}
	configured.ImplementerModel = strings.TrimSpace(configured.ImplementerModel)
	if configured.ImplementerModel == "" {
		configured.ImplementerModel = "gpt-5.6-luna"
	}
	configured.VerifierProvider, err = normalizeProvider(configured.VerifierProvider, "")
	if err != nil {
		return Config{}, fmt.Errorf("config verifierProvider: %w", err)
	}
	configured.VerifierModel = strings.TrimSpace(configured.VerifierModel)
	configured.ReviewerProvider, err = normalizeProvider(configured.ReviewerProvider, "")
	if err != nil {
		return Config{}, fmt.Errorf("config reviewerProvider: %w", err)
	}
	configured.ReviewerModel = strings.TrimSpace(configured.ReviewerModel)
	return configured, nil
}

func normalizeProvider(value, fallback string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return fallback, nil
	case "codex":
		return "codex", nil
	case "copilot", "github_copilot", "github-copilot":
		return "copilot", nil
	default:
		return "", fmt.Errorf("unsupported provider %q", value)
	}
}

func normalizeStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(value), " "))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values")
}

func validateWorkspaceName(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("must be a directory name")
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return errors.New("must not contain a path")
	}
	return nil
}
