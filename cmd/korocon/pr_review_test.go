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
	url              string
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
func (w *fakePRWorkflow) URL() string                         { return w.url }
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
	var out bytes.Buffer
	closed := 0
	controller := newPRReviewController(workflow, &out, func(prompt string) *daemon.JobSpec { return &daemon.JobSpec{Prompt: prompt} }, nil, func() error {
		closed++
		return nil
	}, nil, nil)
	if controller.InitialJob() != nil || controller.InitialPrompt() != "" {
		t.Fatalf("fix started before user instruction")
	}
	action, err := controller.HandleInput(context.Background(), "指摘Aを修正し、指摘Bは対応不要")
	if err != nil || action.Job == nil || !strings.Contains(action.Job.Prompt, "指摘Aを修正") {
		t.Fatalf("instruction action=%+v err=%v", action, err)
	}
	completePRJob(t, controller, action.Job.Prompt, "fixed result")
	action, err = controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Restart || action.Prompt != "" || workflow.fixApproved != "fixed result" || workflow.phase != prworkflow.PhaseFix || closed != 1 {
		t.Fatalf("action=%+v workflow=%+v closed=%d err=%v", action, workflow, closed, err)
	}
	wantMessage := "レビュー指摘修正を承認してPR headへpushしました。修正処理を終了します。"
	if !strings.Contains(out.String(), wantMessage) || strings.Contains(out.String(), "修正処理を終了し、Issue/PR選択へ戻ります。") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPRFixEmptyInputStartsWithSavedFeedback(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseFix}
	var out bytes.Buffer
	controller := newPRReviewController(workflow, &out, func(prompt string) *daemon.JobSpec {
		return &daemon.JobSpec{Prompt: prompt}
	}, nil, nil, nil, nil)
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil || !action.Handled || action.Job == nil || action.Job.Prompt != "fix: " {
		t.Fatalf("action=%+v err=%v", action, err)
	}
	if !strings.Contains(out.String(), "保存済みのレビュー指摘内容を使用して") {
		t.Fatalf("output = %q", out.String())
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

func TestPRReviewFindingsWaitForApproval(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	var out bytes.Buffer
	controller := newPRReviewController(workflow, &out, nil, nil, nil, nil, nil)
	if err := controller.OnJobStart(context.Background(), 1, workflow.Prompt()); err != nil {
		t.Fatal(err)
	}
	result := "## 結果\n\n要修正\n\n## 指摘事項\n- 修正してください"
	err := controller.OnJobFinish(context.Background(), 1, workflow.Prompt(), result, nil)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if workflow.changes != "" || !strings.Contains(out.String(), "レビューで指摘が見つかりました") {
		t.Fatalf("workflow=%+v output=%q", workflow, out.String())
	}
	action, err := controller.HandleInput(context.Background(), "修正してください")
	if err != nil || !action.Handled || !action.Restart || !strings.Contains(workflow.changes, "修正してください") {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
}

func TestPRReviewFindingsCanBeApproved(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	controller := newPRReviewController(workflow, &bytes.Buffer{}, nil, nil, nil, nil, nil)
	result := "## 結果\n\nコメントあり\n\n## 指摘事項\n- 確認してください"
	completePRJob(t, controller, workflow.Prompt(), result)
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Handled || !action.Restart || workflow.approved != result {
		t.Fatalf("action=%+v workflow=%+v err=%v", action, workflow, err)
	}
}

func TestPRReviewApprovedPRWaitsForInput(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReviewApproved}
	controller := newPRReviewController(workflow, &bytes.Buffer{}, nil, nil, nil, nil, nil)
	if prompt := controller.InitialPrompt(); prompt != "" {
		t.Fatalf("initial prompt = %q, want empty", prompt)
	}
	action, err := controller.HandleInput(context.Background(), "")
	if err != nil || !action.Handled || !action.Restart {
		t.Fatalf("enter action=%+v err=%v", action, err)
	}

	workflow = &fakePRWorkflow{phase: prworkflow.PhaseReviewApproved}
	controller = newPRReviewController(workflow, &bytes.Buffer{}, nil, nil, nil, nil, nil)
	action, err = controller.HandleInput(context.Background(), "確認内容")
	if err != nil || !action.Handled || action.Prompt == "" || workflow.phase != prworkflow.PhaseReview || !strings.Contains(action.Prompt, "確認内容") {
		t.Fatalf("rerun action=%+v phase=%q err=%v", action, workflow.phase, err)
	}
}

func TestReviewRequiresChanges(t *testing.T) {
	for _, test := range []struct {
		result string
		want   bool
	}{
		{"## 結果\n問題なし", false},
		{"## 結果\n要修正", true},
		{"## 結果\n**コメントあり**", true},
		{"## 概要\n要修正", false},
	} {
		if got := reviewRequiresChanges(test.result); got != test.want {
			t.Fatalf("reviewRequiresChanges(%q) = %t, want %t", test.result, got, test.want)
		}
	}
}

func TestPRReviewApprovalWithoutStartupCommandReturnsToSelection(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview, url: "https://github.com/owner/repository/pull/4"}
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
	for _, want := range []string{"動作確認後にPRをマージしてください。", "PR URL: https://github.com/owner/repository/pull/4"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, missing %q", out.String(), want)
		}
	}
}

func TestPRReviewApprovalWithoutStartupCommandAllowsEmptyURL(t *testing.T) {
	workflow := &fakePRWorkflow{phase: prworkflow.PhaseReview}
	var out bytes.Buffer
	controller := newPRReviewController(workflow, &out, nil, nil, nil, nil, nil)
	completePRJob(t, controller, workflow.Prompt(), "review result")
	action, err := controller.HandleInput(context.Background(), "approve")
	if err != nil || !action.Handled || !action.Restart || !strings.Contains(out.String(), "PR URL: \n") {
		t.Fatalf("action=%+v output=%q err=%v", action, out.String(), err)
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
