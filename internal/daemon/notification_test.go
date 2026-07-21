package daemon

import (
	"strings"
	"testing"
)

func TestVSCodeNotifierUsesOSC9(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "vscode")
	var out strings.Builder
	notifier := NewVSCodeNotifier(&out)
	if err := notifier.Notify("ジョブ完了\n入力待ち"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "\033]9;ジョブ完了 入力待ち\a"; got != want {
		t.Fatalf("notification = %q, want %q", got, want)
	}
}

func TestVSCodeNotifierIsDisabledOutsideVSCode(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "xterm")
	t.Setenv("VSCODE_PID", "")
	var out strings.Builder
	if err := NewVSCodeNotifier(&out).Notify("job complete"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification = %q, want empty", out.String())
	}
}
