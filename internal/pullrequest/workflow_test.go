package pullrequest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		"label create ":           "",
		"pr view 4 --json labels": `{"labels":[{"name":"state:pr_created"},{"name":"bug"}]}`,
		"pr edit 4 ":              "",
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
	if err := workflow.ApproveReview(context.Background(), "review result"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "--add-label state:review_running --remove-label state:pr_created") ||
		!strings.Contains(calls, "pr comment 4 --body review result") ||
		!strings.Contains(calls, "--add-label state:review_approved") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestRequestChangesAndApproveFix(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"label create ":           "",
		"pr view 7 --json labels": `{"labels":[]}`,
		"pr edit 7 ":              "",
		"pr comment 7 ":           "",
	}}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, PR: PullRequest{Number: 7, Title: "Fix"}, Phase: PhaseReview}
	if err := workflow.RequestChanges(context.Background(), "review", "add test"); err != nil {
		t.Fatal(err)
	}
	published := false
	workflow.SetFixPublisher(func(context.Context, string) error { published = true; return nil })
	workflow.SetPhase(PhaseFix)
	if err := workflow.ApproveFix(context.Background(), "fixed"); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("fix was not published")
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "レビュー修正指示") || !strings.Contains(calls, "--add-label state:pr_review_comment") || !strings.Contains(calls, "--add-label state:review_fixed") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestCompleteIfClosed(t *testing.T) {
	runner := &fakeRunner{responses: map[string]string{
		"pr view 9 --json state":  "MERGED\n",
		"label create ":           "",
		"pr view 9 --json labels": `{"labels":[{"name":"state:review_approved"}]}`,
		"pr edit 9 ":              "",
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
