package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coco-papiyon/korocon/internal/appdir"
)

const FileName = "config.json"

const defaultImplementationDirectory = "../branches-{{ repository_name }}/"

type Config struct {
	WorkspaceName              string   `json:"workspaceName"`
	BranchNamePattern          string   `json:"branchNamePattern"`
	ImplementationDirectory    string   `json:"implementationDirectory"`
	ImplementationLoopCount    int      `json:"implementationLoopCount"`
	AutoPollingInterval        string   `json:"autoPollingInterval"`
	SyncDirtyWorktree          string   `json:"syncDirtyWorktree"`
	BaseBranch                 string   `json:"baseBranch"`
	RuntimeVerificationEnabled bool     `json:"runtimeVerificationEnabled"`
	VSCodeNotificationEnabled  bool     `json:"vscodeNotificationEnabled"`
	StartupCommand             string   `json:"startupCommand,omitempty"`
	BuiltinAllowedCommands     []string `json:"builtinAllowedCommands"`
	BuiltinAllowedPaths        []string `json:"builtinAllowedPaths"`
	ImplementerProvider        string   `json:"implementerProvider"`
	ImplementerModel           string   `json:"implementerModel"`
	VerifierProvider           string   `json:"verifierProvider,omitempty"`
	VerifierModel              string   `json:"verifierModel,omitempty"`
	ReviewerProvider           string   `json:"reviewerProvider,omitempty"`
	ReviewerModel              string   `json:"reviewerModel,omitempty"`
	Reviewer                   string   `json:"reviewer,omitempty"`
}

var defaultAllowedCommands = []string{
	"npm install", "npm ci", "npm test",
	"go build", "go test", "go mod tidy", "go mod download",
	"git log", "git show", "git grep", "git add", "git commit", "git diff", "git status", "git stash",
	"git fetch", "git remote", "git ls-remote", "git worktree add",
	"git --no-pager blame", "git --no-pager diff", "git --no-pager grep", "git --no-pager log", "git --no-pager show", "git --no-pager status",
	"gh pr view", "gh pr diff", "gh pr checks", "gh issue view",
	"command -v", "cd", "true", "test",
	"ls", "dir", "cat", "type", "more", "head", "echo", "printf", "sed", "set", "pwd", "grep", "rg", "find", "sort", "tee", "wc",
	"git branch --show-current", "git rev-parse --verify",
	"get-childitem", "get-content", "select-object", "select-string",
}

var defaultAllowedPaths = []string{
	"~/.copilot/session-state/*/plan.md",
}

func Default() Config {
	return Config{
		WorkspaceName: ".workspace", BranchNamePattern: "issue_#{{ issue_number }}",
		ImplementationDirectory: defaultImplementationDirectory, ImplementationLoopCount: 3,
		AutoPollingInterval: "5m", SyncDirtyWorktree: "fail", BaseBranch: "main", RuntimeVerificationEnabled: true, VSCodeNotificationEnabled: true,
		BuiltinAllowedCommands: DefaultAllowedCommands(),
		BuiltinAllowedPaths:    DefaultAllowedPaths(),
		ImplementerProvider:    "codex", ImplementerModel: "gpt-5.6-luna",
	}
}

func DefaultAllowedCommands() []string {
	return append([]string(nil), defaultAllowedCommands...)
}

func DefaultAllowedPaths() []string {
	return append([]string(nil), defaultAllowedPaths...)
}

// Load reads config.json from the tool directory. A missing file uses defaults
// so installed binaries remain self-contained.
func Load() (Config, string, error) {
	path, err := Path()
	if err != nil {
		return Config{}, "", err
	}
	configured, err := loadFile(path)
	return configured, path, err
}

// Path returns the config file in the tool directory.
func Path() (string, error) {
	directory, err := appdir.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, FileName), nil
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

func AddBuiltinAllowedPath(configured Config, pattern string) (Config, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return configured, false
	}
	before := len(normalizePathList(configured.BuiltinAllowedPaths))
	configured.BuiltinAllowedPaths = normalizePathList(append(configured.BuiltinAllowedPaths, pattern))
	return configured, len(configured.BuiltinAllowedPaths) > before
}

// SetValue updates one scalar configuration value and validates its value.
// List-valued settings are intentionally managed by their dedicated commands.
func SetValue(configured Config, key, value string) (Config, error) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "workspaceName":
		if err := validateWorkspaceName(value); err != nil {
			return configured, fmt.Errorf("config %s: %w", key, err)
		}
		configured.WorkspaceName = value
	case "branchNamePattern":
		if value == "" {
			return configured, fmt.Errorf("config %s: must not be empty", key)
		}
		configured.BranchNamePattern = value
	case "implementationDirectory":
		if value == "" {
			return configured, fmt.Errorf("config %s: must not be empty", key)
		}
		configured.ImplementationDirectory = value
	case "implementationLoopCount":
		count, err := strconv.Atoi(value)
		if err != nil || count < 1 || count > 10 {
			return configured, fmt.Errorf("config %s: must be an integer from 1 to 10: %q", key, value)
		}
		configured.ImplementationLoopCount = count
	case "autoPollingInterval":
		interval, err := time.ParseDuration(value)
		if err != nil || interval <= 0 {
			return configured, fmt.Errorf("config %s: must be a positive duration: %q", key, value)
		}
		configured.AutoPollingInterval = value
	case "syncDirtyWorktree":
		policy, err := normalizeSyncDirtyWorktree(value)
		if err != nil {
			return configured, fmt.Errorf("config %s: %w", key, err)
		}
		configured.SyncDirtyWorktree = policy
	case "baseBranch":
		if value == "" {
			return configured, fmt.Errorf("config %s: must not be empty", key)
		}
		configured.BaseBranch = value
	case "runtimeVerificationEnabled":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return configured, fmt.Errorf("config %s: must be true or false: %q", key, value)
		}
		configured.RuntimeVerificationEnabled = enabled
	case "vscodeNotificationEnabled":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return configured, fmt.Errorf("config %s: must be true or false: %q", key, value)
		}
		configured.VSCodeNotificationEnabled = enabled
	case "startupCommand":
		configured.StartupCommand = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	case "implementerProvider", "verifierProvider", "reviewerProvider":
		if strings.EqualFold(value, "inherit") {
			value = ""
		}
		provider, err := normalizeProvider(value, "")
		if err != nil {
			return configured, fmt.Errorf("config %s: %w", key, err)
		}
		switch key {
		case "implementerProvider":
			if provider == "" {
				return configured, fmt.Errorf("config %s: must be codex or copilot", key)
			}
			configured.ImplementerProvider = provider
		case "verifierProvider":
			configured.VerifierProvider = provider
		case "reviewerProvider":
			configured.ReviewerProvider = provider
		}
	case "implementerModel":
		configured.ImplementerModel = value
	case "verifierModel":
		configured.VerifierModel = value
	case "reviewerModel":
		configured.ReviewerModel = value
	case "reviewer":
		configured.Reviewer = value
	default:
		return configured, fmt.Errorf("unknown or non-scalar config key %q", key)
	}
	return configured, nil
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

	data, err := io.ReadAll(file)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configured); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	var specified struct {
		ImplementerModel *string `json:"implementerModel"`
		VerifierModel    *string `json:"verifierModel"`
		ReviewerModel    *string `json:"reviewerModel"`
	}
	if err := json.Unmarshal(data, &specified); err != nil {
		return Config{}, fmt.Errorf("decode config model fields %q: %w", path, err)
	}
	configured.WorkspaceName = strings.TrimSpace(configured.WorkspaceName)
	if err := validateWorkspaceName(configured.WorkspaceName); err != nil {
		return Config{}, fmt.Errorf("config workspaceName: %w", err)
	}
	configured.BranchNamePattern = strings.TrimSpace(configured.BranchNamePattern)
	if configured.BranchNamePattern == "" {
		configured.BranchNamePattern = "issue_#{{ issue_number }}"
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
	configured.AutoPollingInterval = strings.TrimSpace(configured.AutoPollingInterval)
	if configured.AutoPollingInterval == "" {
		configured.AutoPollingInterval = "5m"
	}
	interval, err := time.ParseDuration(configured.AutoPollingInterval)
	if err != nil || interval <= 0 {
		return Config{}, fmt.Errorf("config autoPollingInterval: must be a positive duration: %q", configured.AutoPollingInterval)
	}
	configured.SyncDirtyWorktree, err = normalizeSyncDirtyWorktree(configured.SyncDirtyWorktree)
	if err != nil {
		return Config{}, fmt.Errorf("config syncDirtyWorktree: %w", err)
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
	configured.BuiltinAllowedPaths = normalizePathList(configured.BuiltinAllowedPaths)
	if len(configured.BuiltinAllowedPaths) == 0 {
		configured.BuiltinAllowedPaths = DefaultAllowedPaths()
	}
	configured.ImplementerProvider, err = normalizeProvider(configured.ImplementerProvider, "codex")
	if err != nil {
		return Config{}, fmt.Errorf("config implementerProvider: %w", err)
	}
	configured.ImplementerModel = strings.TrimSpace(configured.ImplementerModel)
	if configured.ImplementerModel == "" || (configured.ImplementerProvider == "copilot" && (specified.ImplementerModel == nil || strings.TrimSpace(*specified.ImplementerModel) == "" || strings.EqualFold(configured.ImplementerModel, "gpt-5.6-luna"))) {
		configured.ImplementerModel = modelDefaultForProvider(configured.ImplementerProvider)
	}
	configured.VerifierProvider, err = normalizeProvider(configured.VerifierProvider, "")
	if err != nil {
		return Config{}, fmt.Errorf("config verifierProvider: %w", err)
	}
	configured.VerifierModel = strings.TrimSpace(configured.VerifierModel)
	if configured.VerifierProvider == "copilot" && (configured.VerifierModel == "" || specified.VerifierModel == nil || strings.TrimSpace(*specified.VerifierModel) == "" || strings.EqualFold(configured.VerifierModel, "gpt-5.6-luna")) {
		configured.VerifierModel = "auto"
	}
	configured.ReviewerProvider, err = normalizeProvider(configured.ReviewerProvider, "")
	if err != nil {
		return Config{}, fmt.Errorf("config reviewerProvider: %w", err)
	}
	configured.ReviewerModel = strings.TrimSpace(configured.ReviewerModel)
	if configured.ReviewerProvider == "copilot" && (configured.ReviewerModel == "" || specified.ReviewerModel == nil || strings.TrimSpace(*specified.ReviewerModel) == "" || strings.EqualFold(configured.ReviewerModel, "gpt-5.6-luna")) {
		configured.ReviewerModel = "auto"
	}
	configured.Reviewer = strings.TrimSpace(configured.Reviewer)
	return configured, nil
}

func normalizeSyncDirtyWorktree(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "fail":
		return "fail", nil
	case "stash":
		return "stash", nil
	default:
		return "", fmt.Errorf("must be fail or stash: %q", value)
	}
}

func modelDefaultForProvider(provider string) string {
	if provider == "copilot" {
		return "auto"
	}
	return "gpt-5.6-luna"
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

func normalizePathList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		value = filepath.ToSlash(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
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
