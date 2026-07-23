package artifact

import (
	"strings"
	"testing"
)

func TestRequireFullMarkdownAppendsContractExactlyOnceAtEnd(t *testing.T) {
	prompt := RequireFullMarkdown("作業してください。\n\n" + FullMarkdownInstruction + "\n\n追加指示")
	if strings.Count(prompt, FullMarkdownInstruction) != 1 {
		t.Fatalf("contract count = %d: %q", strings.Count(prompt, FullMarkdownInstruction), prompt)
	}
	if !strings.HasSuffix(prompt, FullMarkdownInstruction) {
		t.Fatalf("contract is not at the end: %q", prompt)
	}
}
