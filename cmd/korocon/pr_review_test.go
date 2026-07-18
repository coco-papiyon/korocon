package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coco-papiyon/korocon/internal/daemon"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

type fakePRWorkflow struct {
	phase            prworkflow.Phase
	completed        bool
	state            string
	approved         string
	changes          string
	fixApproved      string
	conflictApproved string
}

func (w *fakePRWorkflow) Prompt() string                      { return "review prompt" }
func (w *fakePRWorkflow) RevisionPrompt(s string) string      { return "rerun: " + s }
func (w *fakePRWorkflow) FixPrompt(s string) string           { return "fix: " + s }
func (w *fakePRWorkflow) ConflictPrompt(s string) string      { return "conflict: " + s }
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
func (w *fakePRWorkflow) ApproveConflict(_ context.Context, result string) error {
	w.conflictApproved = result
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
	started, closed := 0, 0
	controller := newPRReviewController(workflow, &out, nil, nil, nil, func(context.Context) (string, error) {
		started++
		return "go run ./cmd/app", nil
	}, func() error {
		closed++
		return nil
	})
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil || !action.Handled || action.Restart || workflow.phase != prworkflow.PhaseVerification || workflow.approved != "review result" || started != 1 {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
	workflow.completed, workflow.state = false, "OPEN"
	action, err = controller.HandleInput(context.Background(), "")
	if err != nil || action.Restart || !strings.Contains(out.String(), "PR #4はOPENです") {
		t.Fatalf("open action=%+v output=%q err=%v", action, out.String(), err)
	}
	workflow.completed, workflow.state = true, "MERGED"
	action, err = controller.HandleInput(context.Background(), "/check")
	if err != nil || !action.Restart || closed != 1 || !strings.Contains(out.String(), "処理を完了しました") {
		t.Fatalf("action=%+v output=%q err=%v", action, out.String(), err)
	}
}

func TestPRFixRunsAsSeparateJobAndReturnsToSelectionAfterApproval(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseFix}
	closed := 0
	controller := newPRReviewController(workflow, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec { return &daemon.JobSpec{Prompt: prompt} }, nil, func() error {
		closed++
		return nil
	}, nil, nil)
	fixJob := controller.InitialJob()
	if fixJob == nil || !strings.Contains(fixJob.Prompt, "fix:") {
		t.Fatalf("initial fix job=%+v", fixJob)
	}
	completePRJob(t, controller, fixJob.Prompt, "fixed result")
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Restart || action.Prompt != "" || workflow.fixApproved != "fixed result" || workflow.phase != prworkflow.PhaseFix || closed != 1 {
		t.Fatalf("action=%+v workflow=%+v closed=%d err=%v", action, workflow, closed, err)
	}
}

func TestPRReviewRerunAndFixInstruction(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	controller := newPRReviewController(workflow, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec { return &daemon.JobSpec{Prompt: prompt} }, nil, nil, nil, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "/rerun focus tests")
	if err != nil || action.Prompt != "rerun: focus tests" {
		t.Fatalf("rerun action=%+v err=%v", action, err)
	}
	completePRJob(t, controller, action.Prompt, "review result 2")
	action, err = controller.HandleInput(context.Background(), "テストを追加してください")
	if err != nil || !action.Restart || action.Job != nil || workflow.phase != prworkflow.PhaseReview {
		t.Fatalf("fix action=%+v workflow=%+v err=%v", action, workflow, err)
	}
	if workflow.changes != "review result 2:テストを追加してください" {
		t.Fatalf("changes = %q", workflow.changes)
	}
}

func TestPRReviewApprovalWithoutStartupCommandReturnsToSelection(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	var out bytes.Buffer
	controller := newPRReviewController(workflow, &out, nil, nil, nil, nil, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Handled || !action.Restart || workflow.approved != "review result" {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
	if !strings.Contains(out.String(), "動作確認コマンドが設定されていないため、PR処理を終了します") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPRReviewKeepsApprovalPendingWhenStartupCommandFails(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	controller := newPRReviewController(workflow, &bytes.Buffer{}, nil, nil, nil, func(context.Context) (string, error) {
		return "", errors.New("start failed")
	}, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "approve")
	if err == nil || !action.Handled || action.Restart || workflow.approved != "" || workflow.phase != prworkflow.PhaseReview {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
}

func TestPRConflictApprovalPublishesAndReturnsToSelection(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseConflict}
	closed := 0
	controller := newPRReviewController(workflow, &bytes.Buffer{}, nil, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, func() error {
		closed++
		return nil
	}, nil, nil)
	job := controller.InitialJob()
	if job == nil || !strings.Contains(job.Prompt, "conflict") {
		t.Fatalf("initial job = %+v", job)
	}
	completePRJob(t, controller, job.Prompt, "conflict result")
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Handled || !action.Restart || workflow.conflictApproved != "conflict result" || closed != 1 {
		t.Fatalf("action=%+v workflow=%+v closed=%d err=%v", action, workflow, closed, err)
	}
}
