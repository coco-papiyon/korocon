package daemon

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Notifier reports state changes to the host terminal or editor.
type Notifier interface {
	Notify(string) error
}

type vscodeNotifier struct {
	mu  sync.Mutex
	out io.Writer
}

// NewVSCodeNotifier creates a notifier for a VS Code integrated terminal.
// Non-VS Code terminals are ignored by Notify.
func NewVSCodeNotifier(out io.Writer) Notifier {
	return &vscodeNotifier{out: out}
}

func (n *vscodeNotifier) Notify(message string) error {
	if n == nil || n.out == nil || !isVSCodeTerminal() {
		return nil
	}
	message = strings.NewReplacer("\a", " ", "\033", " ", "\r", " ", "\n", " ").Replace(strings.TrimSpace(message))
	if message == "" {
		return nil
	}
	if len(message) > 240 {
		message = message[:240]
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err := fmt.Fprintf(n.out, "\033]9;%s\a", message)
	return err
}

func isVSCodeTerminal() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TERM_PROGRAM")), "vscode") || os.Getenv("VSCODE_PID") != ""
}
