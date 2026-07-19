package daemon

import (
	"strings"
	"testing"
)

func TestSystemOutputSeparatesAndPrefixesLogicalBlocks(t *testing.T) {
	var out strings.Builder
	display := NewSystemOutput(&out)
	_, _ = display.Write([]byte("AI 1\nAI 2\n"))
	if err := display.SystemMessage("一行目\n二行目"); err != nil {
		t.Fatal(err)
	}
	if err := display.SystemMessage("続き"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	want := "AI 1\nAI 2\n---\n[システム] 一行目\n[システム] 二行目\n[システム] 続き\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSystemOutputPreservesKnownPrefixesAndEmptyAIResult(t *testing.T) {
	var out strings.Builder
	display := NewSystemOutput(&out)
	if err := display.SystemMessage("[job 2] 実行中...\n[承認待ち] 操作"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "---\n[job 2] 実行中...\n[承認待ち] 操作\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
