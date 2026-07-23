package pullrequest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/artifact"
	"github.com/coco-papiyon/korocon/internal/runner"
)

type fakeFixSession struct {
	results []runner.TurnResult
	prompts []string
	closed  bool
}

func (s *fakeFixSession) RunTurn(_ context.Context, prompt, _ string, _ func()) (runner.TurnResult, error) {
	s.prompts = append(s.prompts, prompt)
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func (s *fakeFixSession) Close() error {
	s.closed = true
	return nil
}

func TestFixEngineRepeatsImplementationAndVerification(t *testing.T) {
	repository := t.TempDir()
	implementer := &fakeFixSession{results: []runner.TurnResult{{Text: "first", Tokens: 2}, {Text: "second", Tokens: 3}}}
	verifier := &fakeFixSession{results: []runner.TurnResult{
		{Text: `{"status":"changes_requested","feedback":"add test","summary":"failed"}`, Tokens: 5},
		{Text: `{"status":"passed","feedback":"","summary":"verified"}`, Tokens: 7},
	}}
	engine := NewFixEngine(FixConfig{
		RepositoryDir: repository, WorkspaceName: ".workspace", LoopCount: 3,
		Number: 8, HeadRefName: "feature/8", Model: "implementer", VerifierModel: "verifier",
	})
	engine.worktree = t.TempDir()
	engine.implementer = implementer
	engine.verifier = verifier
	var phases []string
	result, err := engine.Run(context.Background(), "指摘Aを修正、指摘Bは修正不要", "", nil, func(phase string) {
		phases = append(phases, phase)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tokens != 17 || !strings.Contains(result.Text, "second") || !strings.Contains(result.Text, "verified") {
		t.Fatalf("result = %+v", result)
	}
	if len(implementer.prompts) != 2 || !strings.Contains(implementer.prompts[1], "add test") {
		t.Fatalf("prompts = %#v", implementer.prompts)
	}
	if strings.Join(phases, ",") != "レビュー修正 実装1回目,レビュー修正 検証1回目,レビュー修正 実装2回目,レビュー修正 検証2回目" {
		t.Fatalf("phases = %v", phases)
	}
	for _, name := range []string{"1回目_実装.md", "1回目_検証.md", "2回目_実装.md", "2回目_検証.md"} {
		if _, err := os.Stat(filepath.Join(repository, ".workspace", "review_fix", "8", name)); err != nil {
			t.Fatalf("artifact %s: %v", name, err)
		}
	}
	if err := engine.Close(); err != nil || !implementer.closed || !verifier.closed {
		t.Fatalf("close err=%v implementer=%t verifier=%t", err, implementer.closed, verifier.closed)
	}
}

func TestFixImplementationPromptRequiresCompleteMarkdownButVerifierStaysJSON(t *testing.T) {
	engine := NewFixEngine(FixConfig{LoopCount: 2, HeadRefName: "feature/8"})
	engine.worktree = "/tmp/worktree"

	prompt := engine.fixImplementationPrompt("workflow", "", 1)
	if strings.Count(prompt, artifact.FullMarkdownInstruction) != 1 || !strings.HasSuffix(prompt, artifact.FullMarkdownInstruction) {
		t.Fatalf("fix prompt does not end with the full Markdown contract:\n%s", prompt)
	}

	verificationPrompt := engine.fixVerificationPrompt("workflow", "implementation", 1)
	if strings.Contains(verificationPrompt, artifact.FullMarkdownInstruction) {
		t.Fatalf("JSON verifier prompt contains the Markdown contract:\n%s", verificationPrompt)
	}
	if !strings.Contains(verificationPrompt, "最終回答はJSONオブジェクトのみ") {
		t.Fatalf("JSON verifier contract is missing:\n%s", verificationPrompt)
	}
}
