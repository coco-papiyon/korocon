package pullrequest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/artifact"
)

func TestSetStatusValidatesPRAndPersistsLocalState(t *testing.T) {
	dir := t.TempDir()

	runner := &fakeRunner{responses: map[string]string{
		"pr view 25 --json number,state,isDraft,url": `{"number":25,"state":"OPEN","isDraft":false,"url":"https://github.com/acme/repo/pull/25"}`,
	}}
	state, err := setStatus(context.Background(), dir, 25, "  IMPLEMENTATION ", runner)
	if err != nil || state != "state:pr_review_comment" {
		t.Fatalf("setStatus() = %q, %v", state, err)
	}
	state, err = setStatus(context.Background(), dir, 25, "review", runner)
	if err != nil || state != "" {
		t.Fatalf("review setStatus() = %q, %v", state, err)
	}
}

func TestSetStatusRejectsClosedDraftAndInvalidPR(t *testing.T) {
	for name, response := range map[string]string{
		"closed":          `{"number":25,"state":"CLOSED","isDraft":false}`,
		"merged":          `{"number":25,"state":"MERGED","isDraft":false}`,
		"draft":           `{"number":25,"state":"OPEN","isDraft":true}`,
		"number missing":  `{"state":"OPEN","isDraft":false}`,
		"number mismatch": `{"number":24,"state":"OPEN","isDraft":false}`,
	} {
		runner := &fakeRunner{responses: map[string]string{"pr view 25 --json number,state,isDraft,url": response}}
		if _, err := setStatus(context.Background(), t.TempDir(), 25, "review", runner); err == nil {
			t.Fatalf("%s PR was accepted", name)
		}
	}
	if _, err := setStatus(context.Background(), t.TempDir(), 25, "other", &fakeRunner{}); err == nil {
		t.Fatal("invalid status was accepted")
	}
}

type fakeRunner struct {
	responses map[string]string
	calls     []string
}

func (r *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	for prefix, response := range r.responses {
		if strings.HasPrefix(call, prefix) {
			return []byte(response), nil
		}
	}
	return nil, fmt.Errorf("unexpected command: %s", call)
}

func TestLoadAndReviewLifecycle(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr view 4 --json number": `{"number":4,"title":"Feature","state":"OPEN","headRefName":"issue_#2","baseRefName":"main","url":"https://example/pr/4"}`,
		"pr comment 4 ":           "",
	}}
	workflow, err := load(context.Background(), t.TempDir(), 4, ".workspace", runner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(workflow.Prompt(), "review-pull-request") || !strings.Contains(workflow.Context(), "issue_#2 -> main") {
		t.Fatalf("unexpected prompt/context: %s", workflow.Prompt())
	}
	if err := workflow.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := workflow.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	path, err := workflow.SaveResult("# Feature\n\nreview result")
	if err != nil {
		t.Fatal(err)
	}
	if path != ".workspace/review/4_feature.md" {
		t.Fatalf("artifact path = %q", path)
	}
	if err := os.WriteFile(filepath.Join(workflow.dir, filepath.FromSlash(path)), []byte("# 手動編集\n\nmanual review result"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ApproveReview(context.Background(), "review result"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "pr comment 4 --body # レビュー結果\n\nmanual review result") ||
		strings.Contains(calls, "label ") || strings.Contains(calls, "--add-label") || strings.Contains(calls, "--remove-label") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestRequestChangesAndApproveFix(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr comment 7 ": "",
	}}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, PR: PullRequest{Number: 7, Title: "Fix"}, Phase: PhaseReview}
	if _, err := workflow.SaveResult("review"); err != nil {
		t.Fatal(err)
	}
	if err := workflow.RequestChanges(context.Background(), "review", "add test"); err != nil {
		t.Fatal(err)
	}
	published := false
	workflow.SetFixPublisher(func(context.Context, string) error { published = true; return nil })
	workflow.SetPhase(PhaseFix)
	if _, err := workflow.SaveResult("fixed"); err != nil {
		t.Fatal(err)
	}
	if err := workflow.ApproveFix(context.Background(), "fixed"); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("fix was not published")
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "レビュー修正指示") || strings.Contains(calls, "label ") || strings.Contains(calls, "--add-label") || strings.Contains(calls, "--remove-label") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestCompleteIfClosed(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr view 9 --json state": "MERGED\n",
	}}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, PR: PullRequest{Number: 9}}
	completed, state, err := workflow.CompleteIfClosed(context.Background())
	if err != nil || !completed || state != "MERGED" {
		t.Fatalf("CompleteIfClosed() = %v, %q, %v", completed, state, err)
	}
}

func TestSaveFixResult(t *testing.T) {
	dir := t.TempDir()
	workflow := &Workflow{dir: dir, workspaceName: ".workspace", PR: PullRequest{Number: 3, Title: "Review Fix"}, Phase: PhaseFix}
	path, err := workflow.SaveResult("result")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".workspace", "review_fix_implementation", "3_review-fix.md")
	if path != ".workspace/review_fix_implementation/3_review-fix.md" {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReviewFeedbackIncludesAllCommentTypes(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{responses: map[string]string{
		"api --paginate repos/acme/repo/issues/8/comments?per_page=100": `[{"user":{"login":"alice"},"body":"general comment"}]`,
		"api --paginate repos/acme/repo/pulls/8/reviews?per_page=100":   `[{"user":{"login":"bob"},"state":"CHANGES_REQUESTED","body":"review body"}]`,
		"api --paginate repos/acme/repo/pulls/8/comments?per_page=100":  `[{"user":{"login":"carol"},"body":"inline one","path":"main.go","line":12}][{"user":{"login":"dave"},"body":"inline two","path":"main_test.go","start_line":30}]`,
	}}
	workflow := &Workflow{
		dir: dir, workspaceName: ".workspace", runner: runner, Phase: PhaseFix,
		PR: PullRequest{
			Number: 8, Title: "Review Fix", URL: "https://github.com/acme/repo/pull/8",
		},
	}
	path, content, err := workflow.SaveReviewFeedback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != ".workspace/review_fix/8_review-fix_レビュー指摘.md" {
		t.Fatalf("path = %q", path)
	}
	for _, want := range []string{"alice: general comment", "bob [CHANGES_REQUESTED]: review body", "carol [main.go:12]: inline one", "dave [main_test.go:30]: inline two"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
		if !strings.Contains(workflow.FixPrompt("指摘を修正"), want) {
			t.Fatalf("fix prompt missing %q", want)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(raw) != content {
		t.Fatalf("saved content = %q, err=%v", raw, err)
	}
}

func TestLoadConflictTakesPriorityAndUsesConflictLifecycle(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr view 12 --json number": `{"number":12,"title":"Conflict","state":"OPEN","mergeable":"CONFLICTING","headRefName":"feature/12","baseRefName":"main","labels":[{"name":"state:pr_review_comment"}]}`,
		"pr comment 12 ":           "",
	}}
	workflow, err := load(context.Background(), t.TempDir(), 12, ".workspace", runner)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Phase != PhaseConflict || !strings.Contains(workflow.Prompt(), "resolve-pr-conflicts") {
		t.Fatalf("phase=%q prompt=%q", workflow.Phase, workflow.Prompt())
	}
	if err := workflow.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := workflow.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	path, err := workflow.SaveResult("conflict result")
	if err != nil {
		t.Fatal(err)
	}
	if path != ".workspace/pr_conflict/12_conflict.md" {
		t.Fatalf("artifact path = %q", path)
	}
	published := false
	workflow.SetConflictPublisher(func(context.Context, string) error {
		published = true
		return nil
	})
	if err := workflow.ApproveConflict(context.Background(), "conflict result"); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("conflict resolution was not published")
	}
	calls := strings.Join(runner.calls, "\n")
	if strings.Contains(calls, "label ") || strings.Contains(calls, "--add-label") || strings.Contains(calls, "--remove-label") {
		t.Fatalf("workflow state unexpectedly updated GitHub labels:\n%s", calls)
	}
}

func TestLoadReviewCommentStartsSeparateFixPhase(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr view 13 --json number": `{"number":13,"title":"Fix","state":"OPEN","mergeable":"MERGEABLE","headRefName":"feature/13","baseRefName":"main","labels":[{"name":"state:pr_review_comment"}],"comments":[{"author":{"login":"reviewer"},"body":"テストを追加してください"}]}`,
	}}
	workflow, err := load(context.Background(), t.TempDir(), 13, ".workspace", runner)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Phase != PhaseFix {
		t.Fatalf("phase = %q", workflow.Phase)
	}
	prompt := workflow.Prompt()
	if !strings.Contains(prompt, "review-comment-fix") || !strings.Contains(prompt, "テストを追加してください") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestPullRequestPhasePrioritizesConflictOverReviewFix(t *testing.T) {
	pr := PullRequest{
		Mergeable: "CONFLICTING",
		Labels:    []Label{{Name: "state:pr_review_comment"}},
	}
	if phase := pullRequestPhase(pr); phase != PhaseConflict {
		t.Fatalf("phase = %q", phase)
	}
}

func TestPullRequestPhaseDetectsReviewApproved(t *testing.T) {
	pr := PullRequest{Labels: []Label{{Name: "state:review_approved"}}}
	if phase := pullRequestPhase(pr); phase != PhaseReviewApproved {
		t.Fatalf("phase = %q", phase)
	}
}

func TestArtifactProducingPromptsRequireCompleteMarkdown(t *testing.T) {
	workflow := &Workflow{
		PR: PullRequest{Number: 4, Title: "Feature"},
	}
	prompts := map[string]string{
		"review":   workflow.Prompt(),
		"revision": workflow.RevisionPrompt("補足"),
		"fix":      workflow.FixPrompt("修正"),
		"conflict": workflow.ConflictPrompt("追加指示"),
	}
	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			if strings.Count(prompt, artifact.FullMarkdownInstruction) != 1 {
				t.Fatalf("full Markdown contract count = %d:\n%s", strings.Count(prompt, artifact.FullMarkdownInstruction), prompt)
			}
			if !strings.HasSuffix(prompt, artifact.FullMarkdownInstruction) {
				t.Fatalf("full Markdown contract is not at the end:\n%s", prompt)
			}
		})
	}
}
