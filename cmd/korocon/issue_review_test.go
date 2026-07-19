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
	number       int
	prompt       string
	started      int
	finished     int
	approvedWith string
	approvedURL  string
	approveErr   error
	revisions    []string
	savedResults []string
	phase        issueworkflow.Phase
	startErr     error
	finishErrs   []error
	finishErr    error
}

type fakePendingReviewWorkflow struct {
	*fakeReviewWorkflow
	pendingResult string
}

func (w *fakePendingReviewWorkflow) PendingApprovalResult() string { return w.pendingResult }

func (w *fakeReviewWorkflow) IssueNumber() int { return w.number }
func (w *fakeReviewWorkflow) Prompt() string   { return w.prompt }
func (w *fakeReviewWorkflow) RevisionPrompt(feedback string) string {
	w.revisions = append(w.revisions, feedback)
	return "revision: " + feedback
}
func (w *fakeReviewWorkflow) Start(context.Context) error {
	w.started++
	return w.startErr
}

func (w *fakeReviewWorkflow) Finish(_ context.Context, err error) error {
	w.finishErrs = append(w.finishErrs, err)
	if err == nil {
		w.finished++
	}
	return w.finishErr
}
func (w *fakeReviewWorkflow) SaveResult(result string) (string, error) {
	w.savedResults = append(w.savedResults, result)
	return ".workspace/design/1_design.md", nil
}
func (w *fakeReviewWorkflow) Approve(_ context.Context, result string) (string, error) {
	w.approvedWith = result
	return w.approvedURL, w.approveErr
}
func (w *fakeReviewWorkflow) SetPhase(phase issueworkflow.Phase) { w.phase = phase }

func TestIssueReviewApprovesEmptyInput(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "design"}
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
	if strings.Count(out.String(), "Issue #2の設計を開始します。\n---\n") != 1 {
		t.Fatalf("unexpected design start message: %q", out.String())
	}
	if strings.Contains(out.String(), "Issue #2の実装を開始します。") {
		t.Fatalf("implementation start message was printed before implementation job started: %q", out.String())
	}
}

func TestIssueReviewRerunsReadyPhaseWhenResultIsMissing(t *testing.T) {
	for _, test := range []struct {
		name    string
		ready   issueworkflow.Phase
		phase   issueworkflow.Phase
		wantJob bool
	}{
		{name: "design", ready: issueworkflow.PhaseDesignReady, phase: issueworkflow.PhaseDesign},
		{name: "implementation", ready: issueworkflow.PhaseImplementationReady, phase: issueworkflow.PhaseImplementation, wantJob: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workflow := &fakePendingReviewWorkflow{fakeReviewWorkflow: &fakeReviewWorkflow{prompt: test.name}}
			controller := newIssueReviewController(workflow, test.ready, &bytes.Buffer{}, func(prompt string) *daemon.JobSpec {
				return &daemon.JobSpec{Prompt: prompt}
			}, nil)
			if workflow.phase != test.phase || controller.InitialPrompt() != test.name {
				t.Fatalf("phase=%s prompt=%q", workflow.phase, controller.InitialPrompt())
			}
			if (controller.InitialJob() != nil) != test.wantJob {
				t.Fatalf("initial job = %+v", controller.InitialJob())
			}
		})
	}
}

func TestIssueReviewImplementationApprovalClosesSessions(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "implement", approvedURL: "https://github.com/acme/repo/pull/1"}
	closed := 0
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementation, &out, func(prompt string) *daemon.JobSpec {
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
	if !action.Handled || !action.Restart || closed != 1 {
		t.Fatalf("action=%+v closed=%d", action, closed)
	}
	if !strings.Contains(out.String(), "PRを作成しました: https://github.com/acme/repo/pull/1") {
		t.Fatalf("unexpected output: %q", out.String())
	}
	if strings.Count(out.String(), "Issue #2の実装を開始します。\n---\n") != 1 {
		t.Fatalf("unexpected implementation start message: %q", out.String())
	}
}

func TestIssueReviewImplementationApprovalFailureKeepsSessionsAndPendingState(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "implement", approveErr: errors.New("push failed")}
	closed := 0
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementation, &bytes.Buffer{}, nil, func() error {
		closed++
		return nil
	})
	if err := controller.OnJobStart(context.Background(), 1, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := controller.OnJobFinish(context.Background(), 1, "implement", "result", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.HandleInput(context.Background(), "approve"); err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("approval error = %v", err)
	}
	if closed != 0 {
		t.Fatalf("implementation sessions closed after failed approval: %d", closed)
	}
	workflow.approveErr = nil
	workflow.approvedURL = "https://github.com/acme/repo/pull/2"
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Handled || !action.Restart || closed != 1 {
		t.Fatalf("retry action=%+v err=%v closed=%d", action, err, closed)
	}
}

func TestIssueReviewFeedbackStartsTrackedRevision(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "implement"}
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
	if strings.Count(out.String(), "Issue #2の実装を開始します。\n---\n") != 2 {
		t.Fatalf("unexpected implementation start messages: %q", out.String())
	}
}

func TestIssueReviewOffersRetryForPersistedFailure(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 16, prompt: "implement", phase: issueworkflow.PhaseImplementationFailed}
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementationFailed, &out, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, nil)
	if controller.InitialPrompt() != "" || controller.InitialJob() != nil {
		t.Fatalf("failed issue started automatically: prompt=%q job=%+v", controller.InitialPrompt(), controller.InitialJob())
	}
	if !strings.Contains(out.String(), "1. 続きから再実行") || !strings.Contains(out.String(), "2. 最初から再実行") || !strings.Contains(out.String(), "3. モデルを変更") {
		t.Fatalf("failure options were not displayed: %q", out.String())
	}
	action, err := controller.HandleInput(context.Background(), "1")
	if err != nil || !action.Handled || action.Job == nil || action.Job.Prompt != "implement" {
		t.Fatalf("unexpected retry action=%+v err=%v", action, err)
	}
}

func TestIssueReviewOffersRetryAfterFailedJob(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "design"}
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseDesign, &out, nil, nil)
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
	if !action.Handled || action.Prompt != "design" {
		t.Fatalf("unexpected retry action: %+v", action)
	}
	if !strings.Contains(out.String(), "1. 続きから再実行") || !strings.Contains(out.String(), "2. 最初から再実行") {
		t.Fatalf("retry options were not displayed: %q", out.String())
	}
}

func TestIssueReviewDoesNotPrintStartMessageWhenWorkflowStartFails(t *testing.T) {
	workflow := &fakeReviewWorkflow{number: 2, prompt: "design", startErr: errors.New("label update failed")}
	var out bytes.Buffer
	controller := newIssueReviewController(workflow, issueworkflow.PhaseDesign, &out, nil, nil)
	if err := controller.OnJobStart(context.Background(), 1, "design"); err == nil {
		t.Fatal("expected start error")
	}
	if strings.Contains(out.String(), "Issue #2の設計を開始します。") || strings.Contains(out.String(), "---") {
		t.Fatalf("start message was printed after failed workflow start: %q", out.String())
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestIssueReviewRollsBackWhenStartMessageOutputFails(t *testing.T) {
	outputErr := errors.New("output closed")
	finishErr := errors.New("failed label update")
	workflow := &fakeReviewWorkflow{number: 2, prompt: "design", finishErr: finishErr}
	closed := 0
	controller := newIssueReviewController(workflow, issueworkflow.PhaseDesign, failingWriter{err: outputErr}, nil, func() error {
		closed++
		return nil
	})

	err := controller.OnJobStart(context.Background(), 1, "design")
	if !errors.Is(err, outputErr) || !errors.Is(err, finishErr) {
		t.Fatalf("start error = %v, want output and finish errors", err)
	}
	if len(workflow.finishErrs) != 1 || !errors.Is(workflow.finishErrs[0], outputErr) {
		t.Fatalf("finish errors = %v, want output error", workflow.finishErrs)
	}
	if len(controller.jobs) != 0 || controller.prompts["design"] != 1 {
		t.Fatalf("job tracking was not rolled back: jobs=%v prompts=%v", controller.jobs, controller.prompts)
	}
	if closed != 0 {
		t.Fatalf("design sessions were closed: %d", closed)
	}

	if err := controller.OnJobStart(context.Background(), 2, "design"); !errors.Is(err, outputErr) {
		t.Fatalf("retry start error = %v, want %v", err, outputErr)
	}
	if workflow.started != 2 || len(workflow.finishErrs) != 2 {
		t.Fatalf("retry state: started=%d finishErrors=%v", workflow.started, workflow.finishErrs)
	}
}

func TestIssueReviewRollsBackImplementationStartMessageOutputFailure(t *testing.T) {
	outputErr := errors.New("output closed")
	workflow := &fakeReviewWorkflow{number: 2, prompt: "implement"}
	closed := 0
	controller := newIssueReviewController(workflow, issueworkflow.PhaseImplementation, failingWriter{err: outputErr}, nil, func() error {
		closed++
		return nil
	})

	err := controller.OnJobStart(context.Background(), 1, "implement")
	if !errors.Is(err, outputErr) || closed != 1 {
		t.Fatalf("start error=%v closed=%d", err, closed)
	}
	if len(workflow.finishErrs) != 1 || !errors.Is(workflow.finishErrs[0], outputErr) {
		t.Fatalf("finish errors = %v, want output error", workflow.finishErrs)
	}
	if len(controller.jobs) != 0 || controller.prompts["implement"] != 1 {
		t.Fatalf("job tracking was not rolled back: jobs=%v prompts=%v", controller.jobs, controller.prompts)
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
