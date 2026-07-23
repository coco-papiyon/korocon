package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	appconfig "github.com/coco-papiyon/korocon/internal/config"
	"github.com/coco-papiyon/korocon/internal/runner"
)

func runConfig(args []string, in io.Reader, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		printConfigUsage(out)
		return nil
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("config init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		force := fs.Bool("force", false, "overwrite an existing config file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("config init does not accept arguments: %s", strings.Join(fs.Args(), " "))
		}
		path, err := appconfig.Path()
		if err != nil {
			return err
		}
		return initializeConfig(in, out, path, *force)
	case "model":
		if len(args) != 1 {
			return errors.New("config model does not accept arguments")
		}
		configured, path, err := appconfig.Load()
		if err != nil {
			return err
		}
		configured, err = configureModels(bufio.NewReader(in), out, configured)
		if err != nil {
			return err
		}
		if err := appconfig.Save(path, configured); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "モデル設定を保存しました。\nconfig: %s\n", path)
		return err
	case "set":
		return setConfigValue(args[1:], out)
	case "list":
		if len(args) != 1 {
			return errors.New("config list does not accept arguments")
		}
		configured, path, err := appconfig.Load()
		if err != nil {
			return err
		}
		return printConfigList(out, path, configured)
	case "allow":
		configured, path, err := appconfig.Load()
		if err != nil {
			return err
		}
		command := strings.TrimSpace(strings.Join(args[1:], " "))
		if command == "" {
			reader := bufio.NewReader(in)
			command, err = readConfigLine(reader, out, "自動承認コマンド: ")
			if err != nil {
				return err
			}
		}
		return addBuiltinAllowedCommand(configured, path, command, out)
	case "allow-path":
		configured, path, err := appconfig.Load()
		if err != nil {
			return err
		}
		pattern := strings.TrimSpace(strings.Join(args[1:], " "))
		if pattern == "" {
			reader := bufio.NewReader(in)
			pattern, err = readConfigLine(reader, out, "自動承認パス(glob): ")
			if err != nil {
				return err
			}
		}
		return addBuiltinAllowedPath(configured, path, pattern, out)
	default:
		return fmt.Errorf("unknown config command %q (try 'korocon config help')", args[0])
	}
}

func setConfigValue(args []string, out io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help")) {
		_, err := fmt.Fprintln(out, "Usage: korocon config set <key> <value>")
		return err
	}
	if len(args) < 2 {
		return errors.New("config set requires a key and value")
	}
	key := strings.TrimSpace(args[0])
	value := strings.TrimSpace(strings.Join(args[1:], " "))
	configured, path, err := appconfig.Load()
	if err != nil {
		return err
	}
	updated, err := appconfig.SetValue(configured, key, value)
	if err != nil {
		return err
	}
	if err := appconfig.Save(path, updated); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "設定を更新しました: %s = %s\nconfig: %s\n", key, value, path)
	return err
}

func printConfigList(out io.Writer, path string, configured appconfig.Config) error {
	if _, err := fmt.Fprintf(out, "設定一覧\nconfig: %s\n\n", path); err != nil {
		return err
	}
	values := []struct {
		name  string
		value string
	}{
		{"workspaceName", configured.WorkspaceName},
		{"baseBranch", configured.BaseBranch},
		{"branchNamePattern", configured.BranchNamePattern},
		{"implementationDirectory", configured.ImplementationDirectory},
		{"implementationLoopCount", fmt.Sprint(configured.ImplementationLoopCount)},
		{"autoPollingInterval", configured.AutoPollingInterval},
		{"syncDirtyWorktree", configured.SyncDirtyWorktree},
		{"runtimeVerificationEnabled", strconv.FormatBool(configured.RuntimeVerificationEnabled)},
		{"vscodeNotificationEnabled", strconv.FormatBool(configured.VSCodeNotificationEnabled)},
		{"startupCommand", configured.StartupCommand},
		{"implementerProvider", configured.ImplementerProvider},
		{"implementerModel", configured.ImplementerModel},
		{"verifierProvider", configured.VerifierProvider},
		{"verifierModel", configured.VerifierModel},
		{"reviewerProvider", configured.ReviewerProvider},
		{"reviewerModel", configured.ReviewerModel},
		{"reviewer", configured.Reviewer},
	}
	for _, item := range values {
		value := item.value
		if strings.TrimSpace(value) == "" {
			value = "(未設定)"
		}
		if _, err := fmt.Fprintf(out, "%s: %s\n", item.name, value); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "builtinAllowedCommands:"); err != nil {
		return err
	}
	for _, command := range configured.BuiltinAllowedCommands {
		if _, err := fmt.Fprintf(out, "  - %s\n", command); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "builtinAllowedPaths:"); err != nil {
		return err
	}
	for _, pattern := range configured.BuiltinAllowedPaths {
		if _, err := fmt.Fprintf(out, "  - %s\n", pattern); err != nil {
			return err
		}
	}
	return nil
}

func addBuiltinAllowedCommand(configured appconfig.Config, path, command string, out io.Writer) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("追加する自動承認コマンドを入力してください")
	}
	updated, added := appconfig.AddBuiltinAllowedCommand(configured, command)
	if !added {
		_, err := fmt.Fprintf(out, "自動承認コマンドはすでに登録されています: %s\n", command)
		return err
	}
	if err := appconfig.Save(path, updated); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "自動承認コマンドを追加しました: %s\nconfig: %s\n", command, path)
	return err
}

func addBuiltinAllowedPath(configured appconfig.Config, path, pattern string, out io.Writer) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return errors.New("追加する自動承認パスを入力してください")
	}
	updated, added := appconfig.AddBuiltinAllowedPath(configured, pattern)
	if !added {
		_, err := fmt.Fprintf(out, "自動承認パスはすでに登録されています: %s\n", pattern)
		return err
	}
	if err := appconfig.Save(path, updated); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "自動承認パスを追加しました: %s\nconfig: %s\n", pattern, path)
	return err
}

func initializeConfig(in io.Reader, out io.Writer, path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config file already exists: %s (use --force to overwrite)", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config %q: %w", path, err)
	}

	configured := appconfig.Default()
	reader := bufio.NewReader(in)
	var err error
	configured.BaseBranch, err = readSetting(reader, out, "baseBranch", configured.BaseBranch)
	if err != nil {
		return err
	}
	configured.BranchNamePattern, err = readSetting(reader, out, "branchNamePattern", configured.BranchNamePattern)
	if err != nil {
		return err
	}
	configured.StartupCommand, err = readSetting(reader, out, "startupCommand", configured.StartupCommand)
	if err != nil {
		return err
	}
	configured, err = configureModels(reader, out, configured)
	if err != nil {
		return err
	}
	if err := appconfig.Save(path, configured); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "設定ファイルを作成しました。\nconfig: %s\n", path)
	return err
}

func readSetting(reader *bufio.Reader, out io.Writer, name, defaultValue string) (string, error) {
	displayDefault := defaultValue
	if displayDefault == "" {
		displayDefault = "未設定"
	}
	value, err := readConfigLine(reader, out, fmt.Sprintf("%s [%s]: ", name, displayDefault))
	if err != nil {
		return "", err
	}
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func configureModels(reader *bufio.Reader, out io.Writer, configured appconfig.Config) (appconfig.Config, error) {
	if _, err := fmt.Fprintln(out, "\nモデル設定を行います。"); err != nil {
		return configured, err
	}

	previousProvider := configured.ImplementerProvider
	provider, err := readProvider(reader, out, "実装者", configured.ImplementerProvider, false)
	if err != nil {
		return configured, err
	}
	configured.ImplementerProvider = provider
	configured.ImplementerModel = modelDefaultAfterProviderChange(previousProvider, provider, configured.ImplementerModel)
	configured.ImplementerModel, err = readModel(reader, out, "実装者", provider, configured.ImplementerModel, false)
	if err != nil {
		return configured, err
	}

	previousProvider = configured.VerifierProvider
	configured.VerifierProvider, err = readProvider(reader, out, "検証者", configured.VerifierProvider, true)
	if err != nil {
		return configured, err
	}
	configured.VerifierModel = modelDefaultAfterProviderChange(previousProvider, configured.VerifierProvider, configured.VerifierModel)
	verifierProvider := configured.VerifierProvider
	if verifierProvider == "" {
		verifierProvider = configured.ImplementerProvider
	}
	configured.VerifierModel, err = readModel(reader, out, "検証者", verifierProvider, configured.VerifierModel, true)
	if err != nil {
		return configured, err
	}

	previousProvider = configured.ReviewerProvider
	configured.ReviewerProvider, err = readProvider(reader, out, "レビューア", configured.ReviewerProvider, true)
	if err != nil {
		return configured, err
	}
	configured.ReviewerModel = modelDefaultAfterProviderChange(previousProvider, configured.ReviewerProvider, configured.ReviewerModel)
	reviewerProvider := configured.ReviewerProvider
	if reviewerProvider == "" {
		reviewerProvider = configured.ImplementerProvider
	}
	configured.ReviewerModel, err = readModel(reader, out, "レビューア", reviewerProvider, configured.ReviewerModel, true)
	return configured, err
}

func modelDefaultAfterProviderChange(previous, selected, current string) string {
	previous = strings.TrimSpace(previous)
	selected = strings.TrimSpace(selected)
	if selected == "" || previous == selected {
		return current
	}
	if selected == "copilot" {
		return "auto"
	}
	return defaultModel
}

func readProvider(reader *bufio.Reader, out io.Writer, role, current string, allowInherit bool) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" && !allowInherit {
		current = "codex"
	}
	display := current
	if display == "" {
		display = "実装者と同じ"
	}
	for {
		choices := "codex/copilot"
		if allowInherit {
			choices = "inherit/codex/copilot"
		}
		value, err := readConfigLine(reader, out, fmt.Sprintf("%sProvider (%s) [%s]: ", role, choices, display))
		if err != nil {
			return "", err
		}
		if value == "" {
			return current, nil
		}
		switch strings.ToLower(value) {
		case "codex", "1":
			return "codex", nil
		case "copilot", "github_copilot", "github-copilot", "2":
			return "copilot", nil
		case "inherit", "same", "実装者と同じ", "3":
			if allowInherit {
				return "", nil
			}
		}
		if allowInherit {
			_, _ = fmt.Fprintln(out, "codex、copilot、inheritのいずれかを入力してください。")
		} else {
			_, _ = fmt.Fprintln(out, "codexまたはcopilotを入力してください。")
		}
	}
}

func readModel(reader *bufio.Reader, out io.Writer, role, provider, current string, allowInherit bool) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" && !allowInherit {
		current = modelDefaultForProvider(provider)
	}
	models := availableModelsForProvider(provider)
	if _, err := fmt.Fprintf(out, "%sで選択可能なモデル (provider: %s):\n", role, provider); err != nil {
		return "", err
	}
	for i, model := range models {
		if _, err := fmt.Fprintf(out, "%d. %s\n", i+1, model); err != nil {
			return "", err
		}
	}
	if provider == "copilot" {
		if _, err := fmt.Fprintln(out, "一覧にないCopilotモデル名も直接入力できます。"); err != nil {
			return "", err
		}
	}
	display := current
	if display == "" {
		display = "実装者と同じ"
	}
	value, err := readConfigLine(reader, out, fmt.Sprintf("%sModel [%s]: ", role, display))
	if err != nil {
		return "", err
	}
	if value == "" {
		return current, nil
	}
	if allowInherit && (strings.EqualFold(value, "inherit") || strings.EqualFold(value, "same") || value == "実装者と同じ") {
		return "", nil
	}
	if model, ok := selectConfiguredModel(value, models); ok {
		return model, nil
	}
	return value, nil
}

func selectConfiguredModel(selection string, models []string) (string, bool) {
	for i, model := range models {
		if selection == model || selection == fmt.Sprint(i+1) {
			return model, true
		}
	}
	return "", false
}

func availableModelsForProvider(provider string) []string {
	if strings.EqualFold(strings.TrimSpace(provider), "copilot") {
		return runner.AvailableCopilotModels
	}
	return runner.AvailableModels
}

func modelDefaultForProvider(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "copilot") {
		return "auto"
	}
	return defaultModel
}

func readConfigLine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := io.WriteString(out, prompt); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", fmt.Errorf("設定入力を読み取れません: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func printConfigUsage(out io.Writer) {
	fmt.Fprint(out, `Usage:
  korocon config init [--force]
  korocon config list
  korocon config model
  korocon config set <KEY> <VALUE>
  korocon config allow [COMMAND]
  korocon config allow-path [GLOB]

Commands:
  init   interactively create config.json
  list   display all settings
  model  interactively update provider and model settings
  set    update one scalar setting
  allow  add a command to builtinAllowedCommands
  allow-path  add a path glob to builtinAllowedPaths
`)
}
