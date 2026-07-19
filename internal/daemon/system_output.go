package daemon

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// SystemOutput keeps AI output untouched while providing a common format for
// messages emitted by the CLI itself.
type SystemOutput struct {
	mu          sync.Mutex
	out         io.Writer
	systemBlock bool
}

func NewSystemOutput(out io.Writer) *SystemOutput { return &SystemOutput{out: out} }

func NewSystemMessageWriter(out io.Writer) io.Writer {
	if _, ok := out.(*SystemOutput); ok {
		return systemMessageWriter{out: out}
	}
	return systemMessageWriter{out: NewSystemOutput(out)}
}

type systemMessageWriter struct{ out io.Writer }

func (w systemMessageWriter) Write(p []byte) (int, error) {
	if err := SystemMessage(w.out, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *SystemOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.systemBlock = false
	return o.out.Write(p)
}

// SystemMessage writes one logical system block. Existing status labels are
// already unambiguous and are intentionally not given a second prefix.
func (o *SystemOutput) SystemMessage(message string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	message = normalizeSystemMessage(message)
	if message == "" {
		return nil
	}
	if !o.systemBlock {
		if _, err := io.WriteString(o.out, "---\n"); err != nil {
			return err
		}
	}
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if i > 0 {
			if _, err := io.WriteString(o.out, "\n"); err != nil {
				return err
			}
		}
		if !knownSystemPrefix(line) {
			line = "[システム] " + line
		}
		if _, err := io.WriteString(o.out, line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(o.out, "\n")
	o.systemBlock = err == nil
	return err
}

func knownSystemPrefix(line string) bool {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"[システム]", "[job ", "[承認待ち]", "[自動承認]", "[Codex応答待ち]"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func SystemMessage(out io.Writer, message string) error {
	if writer, ok := out.(interface{ SystemMessage(string) error }); ok {
		return writer.SystemMessage(message)
	}
	message = normalizeSystemMessage(message)
	if message == "" {
		return nil
	}
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if i > 0 {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
		if !knownSystemPrefix(line) {
			line = "[システム] " + line
		}
		if _, err := fmt.Fprint(out, line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(out, "\n")
	return err
}

func normalizeSystemMessage(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if strings.HasPrefix(message, "---") {
		message = strings.TrimSpace(strings.TrimPrefix(message, "---"))
	}
	return strings.TrimRight(message, "\n")
}
