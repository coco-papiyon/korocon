package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/daemon"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

type fakePRWorkflow struct {
	phase       prworkflow.Phase
	completed   bool
	state       string
	approved    string
	changes     string
	fixApproved string
}

func (w *fakePRWorkflow) Prompt() string                      { return "review prompt" }
func (w *fakePRWorkflow) RevisionPrompt(s string) string      { return "rerun: " + s }
func (w *fakePRWorkflow) FixPrompt(s string) string           { return "fix: " + s }
func (w *fakePRWorkflow) Start(context.Context) error         { return nil }
func (w *fakePRWorkflow) Finish(context.Context, error) error { return nil }
func (w *fakePRWorkflow) SaveResult(string) (string, error)   { return ".workspace/review/4_pr.md", nil }
func (w *fakePRWorkflow) SetPhase(p prworkflow.Phase)         { w.phase = p }
func (w *fakePRWorkflow) CurrentPhase() prworkflow.Phase      { return w.phase }
func (w *fakePRWorkflow) Number() int                         { return 4 }
func (w *fakePRWorkflow) CompleteIfClosed(context.Context) (bool, string, error) {
	return w.completed, w.state, nil
}
func (w *fakePRWorkflow) ApproveReview(_ context.Context, result string) error {
	w.approved = result
	return nil
}
func (w *fakePRWorkflow) RequestChanges(_ context.Context, result, instruction string) error {
	w.changes = result + ":" + instruction
	return nil
}
func (w *fakePRWorkflow) ApproveFix(_ context.Context, result string) error {
	w.fixApproved = result
	return nil
}

func completePRJob(t *testing.T, controller *prReviewController, prompt, result string) {
	t.Helper()
	if err := controller.OnJobStart(context.Background(), 1, prompt); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "", result, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPRReviewApprovalMovesToVerificationAndCompletesWhenClosed(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	var out bytes.Buffer
	controller := newPRReviewController(workflow, &out, nil, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil || !action.Handled || workflow.phase != prworkflow.PhaseVerification || workflow.approved != "review result" {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
	workflow.completed, workflow.state = false, "OPEN"
	action, err = controller.HandleInput(context.Background(), "")
	if err != nil || action.Restart || !strings.Contains(out.String(), "PR #4はOPENです") {
		t.Fatalf("open action=%+v output=%q err=%v", action, out.String(), err)
	}
	workflow.completed, workflow.state = true, "MERGED"
	action, err = controller.HandleInput(context.Background(), "/check")
	if err != nil || !action.Restart || !strings.Contains(out.String(), "処理を完了しました") {
		t.Fatalf("action=%+v output=%q err=%v", action, out.String(), err)
	}
}

func TestPRFixApprovalClosesFixSessionAndRerunsReview(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	closed := 0
	controller := newPRReviewController(workflow, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec { return &daemon.JobSpec{Prompt: prompt} }, func() error {
		closed++
		return nil
	})
	completePRJob(t, controller, workflow.Prompt(), "review result")
	fixAction, err := controller.HandleInput(context.Background(), "修正してください")
	if err != nil || fixAction.Job == nil {
		t.Fatalf("fix action=%+v err=%v", fixAction, err)
	}
	if err := controller.OnJobStart(context.Background(), 2, fixAction.Job.Prompt); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 2, "", "fixed result", nil); err != nil {
		t.Fatal(err)
	}
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || action.Prompt != workflow.Prompt() || workflow.fixApproved != "fixed result" || workflow.phase != prworkflow.PhaseReview || closed != 1 {
		t.Fatalf("action=%+v workflow=%+v closed=%d err=%v", action, workflow, closed, err)
	}
}

func TestPRReviewRerunAndFixInstruction(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	controller := newPRReviewController(workflow, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec { return &daemon.JobSpec{Prompt: prompt} }, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "/rerun focus tests")
	if err != nil || action.Prompt != "rerun: focus tests" {
		t.Fatalf("rerun action=%+v err=%v", action, err)
	}
	completePRJob(t, controller, action.Prompt, "review result 2")
	action, err = controller.HandleInput(context.Background(), "テストを追加してください")
	if err != nil || action.Job == nil || action.Job.Prompt != "fix: テストを追加してください" || workflow.phase != prworkflow.PhaseFix {
		t.Fatalf("fix action=%+v workflow=%+v err=%v", action, workflow, err)
	}
	if workflow.changes != "review result 2:テストを追加してください" {
		t.Fatalf("changes = %q", workflow.changes)
	}
}
