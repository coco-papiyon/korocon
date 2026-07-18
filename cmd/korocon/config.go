package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	default:
		return fmt.Errorf("unknown config command %q (try 'korocon config help')", args[0])
	}
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
	if _, err := fmt.Fprintln(out, "利用可能なモデル:"); err != nil {
		return configured, err
	}
	for i, model := range runner.AvailableModels {
		if _, err := fmt.Fprintf(out, "%d. %s\n", i+1, model); err != nil {
			return configured, err
		}
	}

	provider, err := readProvider(reader, out, "実装者", configured.ImplementerProvider, false)
	if err != nil {
		return configured, err
	}
	configured.ImplementerProvider = provider
	configured.ImplementerModel, err = readModel(reader, out, "実装者", configured.ImplementerModel, false)
	if err != nil {
		return configured, err
	}

	configured.VerifierProvider, err = readProvider(reader, out, "検証者", configured.VerifierProvider, true)
	if err != nil {
		return configured, err
	}
	configured.VerifierModel, err = readModel(reader, out, "検証者", configured.VerifierModel, true)
	if err != nil {
		return configured, err
	}

	configured.ReviewerProvider, err = readProvider(reader, out, "レビューア", configured.ReviewerProvider, true)
	if err != nil {
		return configured, err
	}
	configured.ReviewerModel, err = readModel(reader, out, "レビューア", configured.ReviewerModel, true)
	return configured, err
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

func readModel(reader *bufio.Reader, out io.Writer, role, current string, allowInherit bool) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" && !allowInherit {
		current = defaultModel
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
	if model, ok := selectConfiguredModel(value); ok {
		return model, nil
	}
	return value, nil
}

func selectConfiguredModel(selection string) (string, bool) {
	for i, model := range runner.AvailableModels {
		if selection == model || selection == fmt.Sprint(i+1) {
			return model, true
		}
	}
	return "", false
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
  korocon config model
  korocon config allow [COMMAND]

Commands:
  init   interactively create config.json
  model  interactively update provider and model settings
  allow  add a command to builtinAllowedCommands
`)
}
