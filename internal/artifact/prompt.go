// Package artifact defines the output contract for AI-generated artifacts.
package artifact

import "strings"

// FullMarkdownInstruction requires the final response to contain the complete
// artifact even when a repository skill also writes it to a file.
const FullMarkdownInstruction = `成果物の出力要件:
スキルの指示に従って成果物をファイルへ保存した場合も、最終回答には成果物のMarkdown全文を省略せず再掲してください。
「保存しました」「保存先: ...」だけで終了しないでください。
最終回答そのものを正式な成果物として保存するため、説明や前置きではなく、スキルで指定された見出しから本文を出力してください。`

// RequireFullMarkdown appends the artifact contract exactly once at the end of
// a prompt. Moving an existing contract to the end keeps it authoritative when
// another prompt builder adds instructions.
func RequireFullMarkdown(prompt string) string {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, FullMarkdownInstruction, ""))
	if prompt == "" {
		return FullMarkdownInstruction
	}
	return prompt + "\n\n" + FullMarkdownInstruction
}
