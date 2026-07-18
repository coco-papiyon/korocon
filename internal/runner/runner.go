package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Request describes one non-interactive AI CLI invocation.
type Request struct {
	Provider      string
	Binary        string
	Prompt        string
	Model         string
	WorkingDir    string
	AllowAllTools bool
	Stdout        io.Writer
	Stderr        io.Writer
}

// AvailableModels is the set of models offered by the interactive selector.
// Keep this in the runner package so the selector and provider argument
// handling cannot drift apart.
var AvailableModels = []string{
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
}

// AvailableCopilotModels contains stable selector choices supported by the
// Copilot CLI. Concrete model names can still be entered directly.
var AvailableCopilotModels = []string{
	"auto",
}

// BuildArgs returns arguments without invoking a shell. Keeping this separate
// makes the permission boundary easy to test and prevents prompt injection from
// becoming shell syntax.
func BuildArgs(req Request) ([]string, error) {
	if req.Prompt == "" {
		return nil, errors.New("prompt is empty")
	}
	provider := normalizeProvider(req.Provider)
	switch provider {
	case "codex":
		args := []string{"exec", "--json", "--sandbox", "workspace-write"}
		if req.Model == "" {
			args = []string{"exec", "--json", "--sandbox", "workspace-write"}
		} else {
			args = append(args, "--model", req.Model)
		}
		return append(args, req.Prompt), nil
	case "copilot":
		args := []string{"-p", req.Prompt, "-s", "--no-ask-user"}
		if req.Model != "" {
			args = append(args, "--model", req.Model)
		}
		if req.AllowAllTools {
			args = append(args, "--allow-all-tools")
		}
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

// Run executes the configured provider directly. It intentionally does not use
// cmd.exe, PowerShell, or sh.
func Run(ctx context.Context, req Request) error {
	args, err := BuildArgs(req)
	if err != nil {
		return err
	}
	binary := req.Binary
	if binary == "" {
		binary = req.Provider
		if binary == "" {
			binary = "codex"
		}
	}
	dir, err := workingDir(req.WorkingDir)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Stdout = writerOrDiscard(req.Stdout)
	cmd.Stderr = writerOrDiscard(req.Stderr)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("run %s: %w", binary, err)
	}
	return nil
}

func workingDir(value string) (string, error) {
	if value == "" {
		value = "."
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("working directory %q: %w", value, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", value)
	}
	return abs, nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
