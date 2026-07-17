package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/coco-papiyon/korocon/internal/runner"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "korocon:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version)
		return nil
	case "doctor":
		return doctor(args[1:], stdout)
	case "run":
		return runPrompt(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (try 'korocon help')", args[0])
	}
}

func runPrompt(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "copilot", "AI CLI provider")
	binary := fs.String("binary", "", "provider executable (default: copilot)")
	model := fs.String("model", "", "model name")
	dir := fs.String("dir", ".", "working directory")
	allowAllTools := fs.Bool("allow-all-tools", false, "allow all provider tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read prompt from stdin: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
		return errors.New("prompt is required (argument or stdin)")
	}
	return runner.Run(context.Background(), runner.Request{
		Provider: *provider, Binary: *binary, Prompt: prompt, Model: *model,
		WorkingDir: *dir, AllowAllTools: *allowAllTools, Stdout: stdout, Stderr: stderr,
	})
}

func doctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binary := fs.String("binary", "copilot", "provider executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := exec.LookPath(*binary)
	if err != nil {
		return fmt.Errorf("%s was not found on PATH; install it and run its login flow", *binary)
	}
	fmt.Fprintf(stdout, "%s: %s\n", *binary, path)
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `korocon - run AI CLIs from Go

Usage:
  korocon run [options] PROMPT
  korocon run [options] < prompt.txt
  korocon doctor [--binary copilot]
  korocon version

Run options:
  --provider NAME       provider name (default: copilot)
  --binary PATH         executable (default: copilot)
  --model NAME          model to request
  --dir PATH            provider working directory (default: .)
  --allow-all-tools     grant all provider tools
`)
}
