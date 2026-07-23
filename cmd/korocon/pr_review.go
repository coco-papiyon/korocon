package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/coco-papiyon/korocon/internal/artifact"
	"github.com/coco-papiyon/korocon/internal/daemon"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

type prReviewController struct {
	mu                sync.Mutex
	workflow          prWorkflow
	out               io.Writer
	fixJob            func(string) *daemon.JobSpec
	conflictJob       func(string) *daemon.JobSpec
	closeFix          func() error
	startVerification func(context.Context) (string, error)
	closeVerification func() error
	prompts           map[string]int
	jobs              map[uint64]struct{}
	pending           bool
	result            string
	awaitingFixInput  bool
	resetJob          func(context.Context) error
	failed            bool
	failedPrompt      string
}

func (c *prReviewController) SetResetJob(reset func(context.Context) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetJob = reset
}

func (c *prReviewController) OnModelChange(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		_, _ = fmt.Fprintln(c.out, failureOptions())
	}
}

type prWorkflow interface {
	Prompt() string
	RevisionPrompt(string) string
	FixPrompt(string) string
	ConflictPrompt(string) string
	Start(context.Context) error
	Finish(context.Context, error) error
	SaveResult(string) (string, error)
	SaveVerificationResult(string) (string, error)
	ApproveReview(context.Context, string) error
	RequestChanges(context.Context, string, string) error
	ApproveFix(context.Context, string) error
	ApproveConflict(context.Context, string) error
	CompleteIfClosed(context.Context) (bool, string, error)
	SetPhase(prworkflow.Phase)
	CurrentPhase() prworkflow.Phase
	Number() int
	URL() string
}

func newPRReviewController(workflow prWorkflow, out io.Writer, fixJob, conflictJob func(string) *daemon.JobSpec, closeFix func() error, startVerification func(context.Context) (string, error), closeVerification func() error) *prReviewController {
	c := &prReviewController{workflow: workflow, out: daemon.NewSystemMessageWriter(out), fixJob: fixJob, conflictJob: conflictJob, closeFix: closeFix, startVerification: startVerification, closeVerification: closeVerification, prompts: make(map[string]int), jobs: make(map[uint64]struct{})}
	phase := workflow.CurrentPhase()
	if phase == prworkflow.PhaseReviewFailed || phase == prworkflow.PhaseFixFailed || phase == prworkflow.PhaseConflictFailed {
		c.failed = true
		switch phase {
		case prworkflow.PhaseFixFailed:
			c.failedPrompt = workflow.FixPrompt("")
			workflow.SetPhase(prworkflow.PhaseFix)
		case prworkflow.PhaseConflictFailed:
			c.failedPrompt = workflow.ConflictPrompt("")
			workflow.SetPhase(prworkflow.PhaseConflict)
		default:
			c.failedPrompt = workflow.Prompt()
			workflow.SetPhase(prworkflow.PhaseReview)
		}
		_, _ = fmt.Fprintln(c.out, failureOptions())
	} else if workflow.CurrentPhase() == prworkflow.PhaseFix {
		c.awaitingFixInput = true
	} else {
		c.prompts[c.initialPrompt()]++
	}
	return c
}

func (c *prReviewController) InitialPrompt() string { return c.initialPrompt() }

func (c *prReviewController) initialPrompt() string {
	if c.failed {
		return ""
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseReviewApproved {
		return ""
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseConflict {
		return c.workflow.ConflictPrompt("")
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseFix {
		return ""
	}
	return c.workflow.Prompt()
}

func (c *prReviewController) InitialJob() *daemon.JobSpec {
	if c.failed {
		return nil
	}
	switch c.workflow.CurrentPhase() {
	case prworkflow.PhaseConflict:
		if c.conflictJob != nil {
			return c.conflictJob(c.initialPrompt())
		}
	}
	return nil
}

func (c *prReviewController) OnJobStart(ctx context.Context, id uint64, prompt string) error {
	c.mu.Lock()
	if c.prompts[prompt] == 0 {
		c.mu.Unlock()
		return nil
	}
	c.prompts[prompt]--
	c.jobs[id] = struct{}{}
	verification := c.workflow.CurrentPhase() == prworkflow.PhaseVerification
	c.mu.Unlock()
	if verification {
		return nil
	}
	if err := c.workflow.Start(ctx); err != nil {
		c.mu.Lock()
		delete(c.jobs, id)
		c.prompts[prompt]++
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *prReviewController) OnJobFinish(ctx context.Context, id uint64, prompt string, result string, runErr error) error {
	c.mu.Lock()
	_, tracked := c.jobs[id]
	delete(c.jobs, id)
	c.mu.Unlock()
	if !tracked {
		return nil
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseVerification {
		if runErr != nil {
			c.mu.Lock()
			c.failed = true
			c.failedPrompt = prompt
			_, err := fmt.Fprintln(c.out, failureOptions())
			c.mu.Unlock()
			return err
		}
		artifact, err := c.workflow.SaveVerificationResult(result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.out, "動作確認結果を保存しました: %s\n動作確認が完了しました。\n動作確認後にPRをクローズし、未入力状態でEnterまたは/checkを入力してください。\n", artifact)
		return err
	}
	artifact := ""
	if runErr == nil {
		var err error
		artifact, err = c.workflow.SaveResult(result)
		if err != nil {
			return errors.Join(err, c.workflow.Finish(ctx, err))
		}
	}
	if err := c.workflow.Finish(ctx, runErr); err != nil {
		return err
	}
	if runErr != nil {
		if c.closeFix != nil {
			if err := c.closeFix(); err != nil {
				return err
			}
		}
		c.mu.Lock()
		c.failed = true
		c.failedPrompt = prompt
		_, err := fmt.Fprintln(c.out, failureOptions())
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.pending, c.result = true, result
	phase := "レビュー"
	if c.workflow.CurrentPhase() == prworkflow.PhaseFix {
		phase = "レビュー指摘修正"
	} else if c.workflow.CurrentPhase() == prworkflow.PhaseConflict {
		phase = "コンフリクト解消"
	}
	_, err := fmt.Fprintf(c.out, "%s結果を保存しました: %s\n%sが完了しました。承認する場合は未入力状態でEnter、もしくは承認、approve、aのいずれかを入力してください。\n", phase, artifact, phase)
	if c.workflow.CurrentPhase() == prworkflow.PhaseReview {
		var noticeErr error
		if reviewRequiresChanges(result) {
			_, noticeErr = fmt.Fprintln(c.out, "レビューで指摘が見つかりました。レビュー結果を確認してください。承認する場合は未入力状態でEnter、もしくは承認、approve、aを入力してください。指摘を修正する場合は内容を入力してEnter、/rerunでレビューを再実行できます。")
		} else {
			_, noticeErr = fmt.Fprintln(c.out, "再実行する場合は /rerun または /rerun 補足、修正する場合はレビュー修正指示を入力してください。")
		}
		err = errors.Join(err, noticeErr)
	} else {
		var noticeErr error
		_, noticeErr = fmt.Fprintln(c.out, "再実行または追加修正する場合は内容を入力してください。")
		err = errors.Join(err, noticeErr)
	}
	c.mu.Unlock()
	return err
}

func reviewRequiresChanges(result string) bool {
	lines := strings.Split(strings.ReplaceAll(result, "\r\n", "\n"), "\n")
	for i, line := range lines {
		if !strings.EqualFold(strings.TrimSpace(line), "## 結果") {
			continue
		}
		for _, value := range lines[i+1:] {
			value = strings.Trim(strings.TrimSpace(value), "*_` ")
			if value == "" {
				continue
			}
			if strings.HasPrefix(value, "#") {
				return false
			}
			return strings.Contains(value, "要修正") || strings.Contains(value, "コメントあり")
		}
	}
	return false
}

func (c *prReviewController) HandleInput(ctx context.Context, input string) (daemon.InputAction, error) {
	c.mu.Lock()
	if c.failed {
		failedPrompt := c.failedPrompt
		reset := c.resetJob
		phase := c.workflow.CurrentPhase()
		c.mu.Unlock()
		return c.handleFailureInput(ctx, input, failedPrompt, phase, reset)
	}
	pending, result := c.pending, c.result
	c.mu.Unlock()
	if c.workflow.CurrentPhase() == prworkflow.PhaseVerification {
		if !isApprovalInput(input) && strings.TrimSpace(input) != "/check" {
			_, err := fmt.Fprintln(c.out, "動作確認後にPRをクローズし、未入力状態でEnterまたは/checkを入力してください。")
			return daemon.InputAction{Handled: true}, err
		}
		completed, state, err := c.workflow.CompleteIfClosed(ctx)
		if err != nil {
			return daemon.InputAction{Handled: true}, err
		}
		if !completed {
			_, err = fmt.Fprintf(c.out, "PR #%dは%sです。動作確認完了後にPRをクローズして再確認してください。\n", c.workflow.Number(), state)
			return daemon.InputAction{Handled: true}, err
		}
		_, err = fmt.Fprintf(c.out, "PR #%dが%sになったため、処理を完了しました。\n", c.workflow.Number(), state)
		if c.closeVerification != nil {
			err = errors.Join(err, c.closeVerification())
		}
		if c.closeFix != nil {
			err = errors.Join(err, c.closeFix())
		}
		return daemon.InputAction{Handled: true, Restart: true}, err
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseReviewApproved {
		if isApprovalInput(input) {
			_, err := fmt.Fprintln(c.out, "レビュー承認済みのPR処理を終了し、Issue/PR選択へ戻ります。")
			return daemon.InputAction{Handled: true, Restart: true}, err
		}
		c.workflow.SetPhase(prworkflow.PhaseReview)
		return c.enqueue(c.workflow.RevisionPrompt(strings.TrimSpace(input)), false, "レビュー承認済みPRのレビューを再実行します。")
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseFix && c.awaitingFixInput {
		instruction := strings.TrimSpace(input)
		c.mu.Lock()
		c.awaitingFixInput = false
		c.mu.Unlock()
		message := "修正指示をAIへ送信し、実装・検証を開始します。"
		if instruction == "" {
			message = "保存済みのレビュー指摘内容を使用して、実装・検証を開始します。"
		}
		return c.enqueue(c.workflow.FixPrompt(instruction), true, message)
	}
	if !pending {
		return daemon.InputAction{}, nil
	}

	trimmed := strings.TrimSpace(input)
	if c.workflow.CurrentPhase() == prworkflow.PhaseConflict {
		if isApprovalInput(input) {
			if err := c.workflow.ApproveConflict(ctx, result); err != nil {
				return daemon.InputAction{Handled: true}, err
			}
			c.mu.Lock()
			c.pending, c.result = false, ""
			c.mu.Unlock()
			_, err := fmt.Fprintln(c.out, "コンフリクト解消を承認してPR headへpushしました。PR処理を終了します。")
			if c.closeFix != nil {
				err = errors.Join(err, c.closeFix())
			}
			return daemon.InputAction{Handled: true, Restart: true}, err
		}
		return c.enqueueConflict(c.workflow.ConflictPrompt(input), "追加指示をAIへ送信し、コンフリクト解消を再実行します。")
	}
	if c.workflow.CurrentPhase() == prworkflow.PhaseReview {
		fields := strings.Fields(trimmed)
		command := ""
		if len(fields) > 0 {
			command = strings.ToLower(fields[0])
		}
		if command == "/rerun" {
			feedback := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			prompt := c.workflow.Prompt()
			if feedback != "" {
				prompt = c.workflow.RevisionPrompt(feedback)
			}
			return c.enqueue(prompt, false, "レビューを再実行します。")
		}
		if isApprovalInput(input) {
			verification := ""
			if c.startVerification != nil {
				var err error
				verification, err = c.startVerification(ctx)
				if err != nil {
					return daemon.InputAction{Handled: true}, fmt.Errorf("動作確認コマンドの起動に失敗しました: %w", err)
				}
			}
			if err := c.workflow.ApproveReview(ctx, result); err != nil {
				if c.closeVerification != nil && c.startVerification != nil {
					_ = c.closeVerification()
				}
				return daemon.InputAction{Handled: true}, err
			}
			c.mu.Lock()
			c.pending, c.result = false, ""
			c.mu.Unlock()
			if c.startVerification == nil {
				_, err := fmt.Fprintf(c.out, "レビューを承認しました。動作確認が無効のため、PR処理を終了します。\nPR URL: %s\n", c.workflow.URL())
				if c.closeFix != nil {
					err = errors.Join(err, c.closeFix())
				}
				return daemon.InputAction{Handled: true, Restart: true}, err
			}
			c.mu.Lock()
			c.workflow.SetPhase(prworkflow.PhaseVerification)
			verificationPrompt := runtimeVerificationPrompt(verification)
			c.prompts[verificationPrompt]++
			c.mu.Unlock()
			message := "レビューを承認しました。AIにworktreeでの動作確認を指示します。"
			if verification != "" {
				message += "\n動作確認コマンドを起動しました: " + verification
			}
			_, err := fmt.Fprintln(c.out, message)
			return daemon.InputAction{Handled: true, Prompt: verificationPrompt}, err
		}
		instruction := trimmed
		if command == "/fix" {
			instruction = strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		}
		if instruction == "" {
			return daemon.InputAction{Handled: true}, errors.New("レビュー修正指示を入力してください")
		}
		if err := c.workflow.RequestChanges(ctx, result, instruction); err != nil {
			return daemon.InputAction{Handled: true}, err
		}
		c.mu.Lock()
		c.pending, c.result = false, ""
		c.mu.Unlock()
		_, err := fmt.Fprintln(c.out, "レビュー修正指示をPRへ登録しました。レビューを終了し、Issue/PR選択へ戻ります。")
		return daemon.InputAction{Handled: true, Restart: true}, err
	}

	if isApprovalInput(input) {
		if err := c.workflow.ApproveFix(ctx, result); err != nil {
			return daemon.InputAction{Handled: true}, err
		}
		if c.closeFix != nil {
			if err := c.closeFix(); err != nil {
				return daemon.InputAction{Handled: true}, err
			}
		}
		c.mu.Lock()
		c.pending, c.result = false, ""
		c.mu.Unlock()
		_, err := fmt.Fprintln(c.out, "レビュー指摘修正を承認してPR headへpushしました。修正処理を終了します。")
		return daemon.InputAction{Handled: true, Restart: true}, err
	}
	return c.enqueue(c.workflow.FixPrompt(input), true, "追加指示をAIへ送信し、レビュー指摘修正を再実行します。")
}

func runtimeVerificationPrompt(command string) string {
	lines := []string{
		"PRレビューが承認されました。現在のPR head worktreeで動作確認を行ってください。",
		"リポジトリで利用可能な動作確認用スキルがあれば、その手順に従ってください。",
		"ソースや設定を変更せず、実行したコマンド、結果、指摘事項、残課題を日本語でまとめてください。",
	}
	if command = strings.TrimSpace(command); command != "" {
		lines = append(lines, "動作確認コマンドは別プロセスで起動済みです: "+command)
	}
	return artifact.RequireFullMarkdown(strings.Join(lines, "\n"))
}

func (c *prReviewController) handleFailureInput(ctx context.Context, input, prompt string, phase prworkflow.Phase, reset func(context.Context) error) (daemon.InputAction, error) {
	trimmed := strings.TrimSpace(input)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "/model") {
		return daemon.InputAction{}, nil
	}
	if lower == "3" || lower == "model" || lower == "モデル変更" {
		_, err := fmt.Fprintln(c.out, "モデルを変更する場合は /model または /model <番号|モデル名> を入力してください。変更後、続きから再実行または最初から再実行を選択してください。")
		return daemon.InputAction{Handled: true}, err
	}
	resetWorktree := lower == "2" || lower == "restart" || lower == "reset" || lower == "最初から" || lower == "最初から再実行"
	continueWorktree := trimmed == "" || lower == "1" || lower == "retry" || lower == "continue" || lower == "続きから" || lower == "続きから再実行"
	if !resetWorktree && !continueWorktree {
		_, err := fmt.Fprintln(c.out, failureOptions())
		return daemon.InputAction{Handled: true}, err
	}
	if resetWorktree && reset != nil {
		if err := reset(ctx); err != nil {
			return daemon.InputAction{Handled: true}, fmt.Errorf("worktreeを初期状態へ戻せませんでした: %w", err)
		}
	}
	c.mu.Lock()
	c.failed = false
	c.failedPrompt = ""
	c.mu.Unlock()
	if phase == prworkflow.PhaseConflict {
		return c.enqueueConflict(prompt, "失敗したコンフリクト解消を再実行します。")
	}
	if phase == prworkflow.PhaseFix {
		return c.enqueue(prompt, true, "失敗したレビュー指摘修正を再実行します。")
	}
	return c.enqueue(prompt, false, "失敗したレビューを再実行します。")
}

func (c *prReviewController) enqueueConflict(prompt, message string) (daemon.InputAction, error) {
	c.mu.Lock()
	c.pending, c.result = false, ""
	c.prompts[prompt]++
	c.mu.Unlock()
	_, err := fmt.Fprintln(c.out, message)
	if c.conflictJob == nil {
		return daemon.InputAction{Handled: true}, errors.New("conflict resolution job is not configured")
	}
	return daemon.InputAction{Handled: true, Job: c.conflictJob(prompt)}, err
}

func (c *prReviewController) enqueue(prompt string, fix bool, message string) (daemon.InputAction, error) {
	c.mu.Lock()
	c.pending, c.result = false, ""
	c.prompts[prompt]++
	c.mu.Unlock()
	_, err := fmt.Fprintln(c.out, message)
	if fix {
		if c.fixJob == nil {
			return daemon.InputAction{Handled: true}, errors.New("review fix job is not configured")
		}
		return daemon.InputAction{Handled: true, Job: c.fixJob(prompt)}, err
	}
	return daemon.InputAction{Handled: true, Prompt: prompt}, err
}
