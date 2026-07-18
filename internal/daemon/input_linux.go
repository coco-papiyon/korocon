//go:build linux

package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func readInteractiveInput(ctx context.Context, in io.Reader, out io.Writer, submit func(string), toolStatus *toolStatusBridge) (bool, error) {
	f, ok := in.(*os.File)
	if !ok || !isTerminal(f) {
		return false, nil
	}

	term, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return true, fmt.Errorf("get terminal mode: %w", err)
	}
	raw := *term
	raw.Lflag &^= unix.ICANON | unix.ECHO
	raw.Iflag &^= unix.IXON | unix.ICRNL
	// Wake periodically so a Ctrl+C signal can cancel the context even while
	// no input bytes are arriving.
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &raw); err != nil {
		return true, fmt.Errorf("set terminal mode: %w", err)
	}
	defer unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, term)

	// Ask xterm-compatible and Kitty-protocol terminals (including VS Code's
	// integrated terminal) to distinguish Shift+Enter from Enter.
	_, _ = io.WriteString(out, "\033[>4;2m\033[>1u")
	defer io.WriteString(out, "\033[<u\033[>4m")

	e := &lineEditor{out: out, rows: [][]rune{{}}}
	e.render()
	buf := make([]byte, 1)
	for {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		n, err := f.Read(buf)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			// With VMIN=0/VTIME>0, os.File may report a terminal read
			// timeout as io.EOF. The terminal itself is still open.
			if err == io.EOF {
				continue
			}
			return true, err
		}
		if n == 0 {
			continue
		}
		if e.handle(buf[0], f, submit) {
			return true, context.Canceled
		}
	}
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

type lineEditor struct {
	mu             sync.Mutex
	out            io.Writer
	rows           [][]rune
	row            int
	col            int
	renderedRows   int
	renderedCursor int
	status         string
}

func (e *lineEditor) setStatus(status string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = strings.ReplaceAll(strings.ReplaceAll(status, "\r", ""), "\n", " ")
	e.renderLocked()
}

func (e *lineEditor) handle(b byte, f *os.File, submit func(string)) bool {
	if b == 3 { // Ctrl+C is handled by signal.NotifyContext as well.
		return true
	}
	if b == 13 || b == 10 {
		text := e.text()
		e.finishInput()
		e.rows = [][]rune{{}}
		e.row, e.col = 0, 0
		trimmed := strings.TrimSpace(text)
		if trimmed == "exit" {
			return true
		}
		submit(text)
		return false
	}
	if b == 127 || b == 8 {
		if e.col > 0 {
			e.rows[e.row] = append(e.rows[e.row][:e.col-1], e.rows[e.row][e.col:]...)
			e.col--
		} else if e.row > 0 {
			e.col = len(e.rows[e.row-1])
			e.rows = append(e.rows[:e.row], e.rows[e.row+1:]...)
			e.row--
		}
		e.render()
		return false
	}
	if b == 27 {
		seq := make([]byte, 0, 32)
		one := []byte{0}
		for len(seq) < cap(seq) {
			n, err := f.Read(one)
			if err != nil || n == 0 {
				break
			}
			seq = append(seq, one[0])
			if one[0] == 'A' || one[0] == 'B' || one[0] == 'C' || one[0] == 'D' || one[0] == '~' || one[0] == 'u' {
				break
			}
		}
		s := string(seq)
		modifier, code, modified := decodeModifiedKey(s)
		if !modified {
			modifier, code, modified = decodeKittyKey(s)
		}
		switch {
		case modified && isCtrlC(modifier, code):
			return true
		case strings.HasSuffix(s, "\r"), strings.HasSuffix(s, "\n"):
			e.insertLineBreak()
		case modified && code == 13 && (modifier-1)&1 != 0:
			e.insertLineBreak()
		case modified && code >= 32 && code <= utf8.MaxRune:
			e.insertRune(rune(code))
		case strings.HasSuffix(s, "[D"):
			if e.col > 0 {
				e.col--
			}
		case strings.HasSuffix(s, "[C"):
			if e.col < len(e.rows[e.row]) {
				e.col++
			}
		case strings.HasSuffix(s, "[A"):
			if e.row > 0 {
				e.row--
				if e.col > len(e.rows[e.row]) {
					e.col = len(e.rows[e.row])
				}
			}
		case strings.HasSuffix(s, "[B"):
			if e.row+1 < len(e.rows) {
				e.row++
				if e.col > len(e.rows[e.row]) {
					e.col = len(e.rows[e.row])
				}
			}
		case strings.Contains(s, "13;2u"), strings.Contains(s, "27;2;13~"):
			e.insertLineBreak()
		}
		e.render()
		return false
	}
	if b < 32 {
		return false
	}
	// Decode a UTF-8 character from the terminal without treating its bytes
	// as separate cursor positions.
	read := []byte{b}
	for !utf8.FullRune(read) {
		one := []byte{0}
		n, err := f.Read(one)
		if err != nil || n == 0 {
			return false
		}
		read = append(read, one[0])
	}
	r, _ := utf8.DecodeRune(read)
	e.insertRune(r)
	e.render()
	return false
}

func decodeModifiedKey(sequence string) (modifier, code int, ok bool) {
	n, err := fmt.Sscanf(sequence, "[27;%d;%d~", &modifier, &code)
	return modifier, code, err == nil && n == 2
}

func decodeKittyKey(sequence string) (modifier, code int, ok bool) {
	n, err := fmt.Sscanf(sequence, "[%d;%du", &code, &modifier)
	return modifier, code, err == nil && n == 2
}

func isCtrlC(modifier, code int) bool {
	ctrl := (modifier-1)&4 != 0
	return ctrl && (code == 'c' || code == 'C')
}

func (e *lineEditor) insertRune(r rune) {
	line := e.rows[e.row]
	line = append(line, 0)
	copy(line[e.col+1:], line[e.col:])
	line[e.col] = r
	e.rows[e.row] = line
	e.col++
}

func (e *lineEditor) insertLineBreak() {
	e.rows = append(e.rows, append([]rune(nil), e.rows[e.row][e.col:]...))
	e.rows[e.row] = e.rows[e.row][:e.col]
	e.row++
	e.col = 0
}

func (e *lineEditor) text() string {
	lines := make([]string, len(e.rows))
	for i, row := range e.rows {
		lines[i] = string(row)
	}
	return strings.Join(lines, "\n")
}

func (e *lineEditor) render() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.renderLocked()
}

func (e *lineEditor) renderLocked() {
	// Move to the first editor row, then redraw only the editor area. Clearing
	// the whole terminal breaks VS Code's scrollback and duplicates prompts.
	if e.renderedRows > 0 {
		_, _ = io.WriteString(e.out, "\r")
		if e.renderedCursor > 0 {
			_, _ = fmt.Fprintf(e.out, "\033[%dA", e.renderedCursor)
		}
	}

	total := len(e.rows)
	if e.renderedRows > total {
		total = e.renderedRows
	}
	for i := 0; i < total; i++ {
		_, _ = io.WriteString(e.out, "\r\033[2K")
		if i < len(e.rows) {
			prefix := "  "
			if i == 0 {
				prefix = "> "
			}
			line := prefix + string(e.rows[i])
			if i == 0 && e.status != "" {
				line += "  [" + e.status + "]"
			}
			_, _ = io.WriteString(e.out, line)
		}
		if i+1 < total {
			_, _ = io.WriteString(e.out, "\r\n")
		}
	}

	currentBottom := total - 1
	if currentBottom > e.row {
		_, _ = fmt.Fprintf(e.out, "\033[%dA", currentBottom-e.row)
	}
	_, _ = io.WriteString(e.out, "\r")
	cursorColumns := 2 + displayWidth(e.rows[e.row][:e.col])
	if cursorColumns > 0 {
		_, _ = fmt.Fprintf(e.out, "\033[%dC", cursorColumns)
	}
	e.renderedRows = len(e.rows)
	e.renderedCursor = e.row
}

func displayWidth(runes []rune) int {
	width := 0
	for _, r := range runes {
		width += runeWidth(r)
	}
	return width
}

func runeWidth(r rune) int {
	if r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe19) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1faff)) {
		return 2
	}
	return 1
}

func (e *lineEditor) finishInput() {
	if e.renderedRows == 0 {
		// After a submitted job, daemon.Run prints the next prompt directly.
		// The editor therefore has no rendered rows recorded even though "> " is
		// visible. Always finish that prompt line before handling empty input.
		_, _ = io.WriteString(e.out, "\r\n")
		return
	}
	if below := e.renderedRows - 1 - e.renderedCursor; below > 0 {
		_, _ = fmt.Fprintf(e.out, "\033[%dB", below)
	}
	_, _ = io.WriteString(e.out, "\r\n")
	e.renderedRows = 0
	e.renderedCursor = 0
}
