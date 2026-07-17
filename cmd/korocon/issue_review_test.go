package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/daemon"
	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
)

type fakeReviewWorkflow struct {
	prompt       string
	started      int
	finished     int
	approvedWith string
	revisions    []string
	savedResults []string
	phase        issueworkflow.Phase
}

func (w *fakeReviewWorkflow) Prompt() string { return w.prompt }
func (w *fakeReviewWorkflow) RevisionPrompt(feedback string) string {
	w.revisions = append(w.revisions, feedback)
	return "revision: " + feedback
}
func (w *fakeReviewWorkflow) Start(context.Context) error {
	w.started++
	return nil
}
func (w *fakeReviewWorkflow) Finish(_ context.Context, err error) error {
	if err == nil {
		w.finished++
	}
	return nil
}
func (w *fakeReviewWorkflow) SaveResult(result string) (string, error) {
	w.savedResults = append(w.savedResults, result)
	return ".workspace/design/1_design.md", nil
}
func (w *fakeReviewWorkflow) Approve(_ context.Context, result string) error {
	w.approvedWith = result
	return nil
}
func (w *fakeReviewWorkflow) SetPhase(phase issueworkflow.Phase) { w.phase = phase }

func TestIssueReviewApprovesEmptyInput(t *testing.T) {
	workflow := &fakeReviewWorkflow{prompt: "design"}
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseDesign, &out, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, nil)
	if err := controller.OnJobStart(context.Background(), 1, "design"); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "design", "design result", nil); err != nil {
		t.Fatal(err)
	}
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !action.Handled || action.Job == nil || workflow.phase != issueworkflow.PhaseImplementation || workflow.approvedWith != "design result" || len(workflow.savedResults) != 1 {
		t.Fatalf("action=%+v approvedWith=%q", action, workflow.approvedWith)
	}
	if !strings.Contains(out.String(), "\n\n---\n\n設計結果を保存しました: .workspace/design/1_design.md") ||
		!strings.Contains(out.String(), "設計が完了しました。承認する場合は未入力状態でEnter、もしくは承認、approve、aのいずれかを入力してください。") ||
		!strings.Contains(out.String(), "設計を承認しました") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestIssueReviewImplementationApprovalClosesSessions(t *testing.T) {
	workflow := &fakeReviewWorkflow{prompt: "implement"}
	closed := 0
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementation, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, func() error {
		closed++
		return nil
	})
	if err := controller.OnJobStart(context.Background(), 1, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "implement", "result", nil); err != nil {
		t.Fatal(err)
	}
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil {
		t.Fatal(err)
	}
	if !action.Handled || closed != 1 {
		t.Fatalf("action=%+v closed=%d", action, closed)
	}
}

func TestIssueReviewFeedbackStartsTrackedRevision(t *testing.T) {
	workflow := &fakeReviewWorkflow{prompt: "implement"}
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementation, &out, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, nil)
	if err := controller.OnJobStart(context.Background(), 1, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "implement", "result", nil); err != nil {
		t.Fatal(err)
	}
	action, err := controller.HandleInput(context.Background(), "テストを追加してください")
	if err != nil {
		t.Fatal(err)
	}
	if !action.Handled || action.Job == nil || action.Job.Prompt != "revision: テストを追加してください" {
		t.Fatalf("action=%+v", action)
	}
	if err := controller.OnJobStart(context.Background(), 2, action.Job.Prompt); err != nil {
		t.Fatal(err)
	}
	if workflow.started != 2 || len(workflow.revisions) != 1 {
		t.Fatalf("started=%d revisions=%v", workflow.started, workflow.revisions)
	}
	if !strings.Contains(out.String(), "再実装") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestIssueReviewDoesNotPromptAfterFailedJob(t *testing.T) {
	workflow := &fakeReviewWorkflow{prompt: "design"}
	controller := newIssueReviewController(workflow, issueworkflow.PhaseDesign, &bytes.Buffer{}, nil, nil)
	if err := controller.OnJobStart(context.Background(), 1, "design"); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "design", "", errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if action.Handled {
		t.Fatal("failed job entered approval state")
	}
}

func TestApprovalInputs(t *testing.T) {
	for _, input := range []string{"", "  ", "承認", "approve", "A", "yes", "ok"} {
		if !isApprovalInput(input) {
			t.Fatalf("%q was not accepted", input)
		}
	}
	if isApprovalInput("修正してください") {
		t.Fatal("feedback was treated as approval")
	}
}
