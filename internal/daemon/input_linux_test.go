//go:build linux

package daemon

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestDevNullIsNotTerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("character device without terminal controls was treated as a terminal")
	}
}

func TestLineEditorRenderDoesNotClearTerminal(t *testing.T) {
	var out strings.Builder
	e := lineEditor{out: &out, rows: [][]rune{{}}}
	e.render()
	out.Reset()

	e.rows[0] = []rune("aa")
	e.col = 2
	e.render()

	if strings.Contains(out.String(), "\033[2J") || strings.Contains(out.String(), "\033[H") {
		t.Fatalf("render cleared the terminal: %q", out.String())
	}
	if strings.Count(out.String(), "> ") != 1 {
		t.Fatalf("rendered prompt more than once: %q", out.String())
	}
}

func TestLineEditorUsesTerminalWidthForJapanese(t *testing.T) {
	var out strings.Builder
	e := lineEditor{out: &out, rows: [][]rune{[]rune("あ")}, col: 1}
	e.render()
	if !strings.Contains(out.String(), "\033[4C") {
		t.Fatalf("cursor did not account for full-width character: %q", out.String())
	}
}

func TestLineEditorCtrlCStopsInput(t *testing.T) {
	e := lineEditor{rows: [][]rune{{}}}
	if !e.handle(3, nil, nil) {
		t.Fatal("Ctrl+C did not stop input")
	}
}

func TestLineEditorShowsToolStatusOnPromptLine(t *testing.T) {
	var out strings.Builder
	e := lineEditor{out: &out, rows: [][]rune{{'h', 'i'}}, col: 2}
	e.render()
	out.Reset()
	e.setStatus("ツール実行中: rg -n TODO")
	if !strings.Contains(out.String(), "> hi  [ツール実行中: rg -n TODO]") {
		t.Fatalf("prompt does not contain tool status: %q", out.String())
	}
}

func TestDecodeModifiedShiftEnter(t *testing.T) {
	modifier, code, ok := decodeModifiedKey("[27;2;13~")
	if !ok || modifier != 2 || code != 13 {
		t.Fatalf("modifier=%d code=%d ok=%v", modifier, code, ok)
	}
}

func TestDecodeKittyCtrlC(t *testing.T) {
	modifier, code, ok := decodeKittyKey("[99;5u")
	if !ok || !isCtrlC(modifier, code) {
		t.Fatalf("modifier=%d code=%d ok=%v", modifier, code, ok)
	}
}

func TestLineEditorExitCommandStopsInput(t *testing.T) {
	e := lineEditor{out: io.Discard, rows: [][]rune{[]rune("exit")}, col: 4}
	if !e.handle(13, nil, func(string) { t.Fatal("exit was submitted to AI") }) {
		t.Fatal("exit did not stop input")
	}
}

func TestLineEditorSubmitsEmptyInput(t *testing.T) {
	called := false
	e := lineEditor{out: io.Discard, rows: [][]rune{{}}}
	if e.handle(13, nil, func(text string) {
		called = true
		if text != "" {
			t.Fatalf("text = %q", text)
		}
	}) {
		t.Fatal("empty input stopped input")
	}
	if !called {
		t.Fatal("empty input was not submitted")
	}
}
