package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// RuntimeCommand owns the process used for manual PR operation checks.
type RuntimeCommand struct {
	command string
	dir     string
	out     io.Writer

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan error
}

func NewRuntimeCommand(command, dir string, out io.Writer) *RuntimeCommand {
	return &RuntimeCommand{command: strings.TrimSpace(command), dir: dir, out: out}
}

func (r *RuntimeCommand) Start(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.command == "" {
		return "", errors.New("startup command is empty")
	}
	if r.done != nil {
		select {
		case <-r.done:
			r.done = nil
		default:
			return "", errors.New("startup command is already running")
		}
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := runtimeShellCommand(processCtx, r.command)
	cmd.Dir = r.dir
	cmd.Stdout = r.out
	cmd.Stderr = r.out
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("start %q: %w", r.command, err)
	}
	r.cancel = cancel
	r.done = make(chan error, 1)
	done := r.done
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	return r.command, nil
}

func (r *RuntimeCommand) Close() error {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil
	r.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	err := <-done
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return fmt.Errorf("stop startup command: %w", err)
}

func runtimeShellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-lc", command)
}
