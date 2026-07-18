package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/coco-papiyon/korocon/internal/daemon"
	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
)

type reviewWorkflow interface {
	IssueNumber() int
	Prompt() string
	RevisionPrompt(string) string
	Start(context.Context) error
	Finish(context.Context, error) error
	SaveResult(string) (string, error)
	Approve(context.Context, string) (string, error)
	SetPhase(issueworkflow.Phase)
}

type issueReviewController struct {
	mu                  sync.Mutex
	workflow            reviewWorkflow
	phase               issueworkflow.Phase
	out                 io.Writer
	prompts             map[string]int
	jobs                map[uint64]struct{}
	pending             bool
	result              string
	implementationJob   func(string) *daemon.JobSpec
	closeImplementation func() error
}

func newIssueReviewController(workflow reviewWorkflow, phase issueworkflow.Phase, out io.Writer, implementationJob func(string) *daemon.JobSpec, closeImplementation func() error) *issueReviewController {
	c := &issueReviewController{
		workflow:            workflow,
		phase:               phase,
		out:                 out,
		prompts:             make(map[string]int),
		jobs:                make(map[uint64]struct{}),
		implementationJob:   implementationJob,
		closeImplementation: closeImplementation,
	}
	c.registerPrompt(workflow.Prompt())
	return c
}

func (c *issueReviewController) InitialJob() *daemon.JobSpec {
	if c.phase != issueworkflow.PhaseImplementation || c.implementationJob == nil {
		return nil
	}
	return c.implementationJob(c.workflow.Prompt())
}

func (c *issueReviewController) InitialPrompt() string {
	return c.workflow.Prompt()
}

func (c *issueReviewController) OnJobStart(ctx context.Context, id uint64, prompt string) error {
	c.mu.Lock()
	if c.prompts[prompt] == 0 {
		c.mu.Unlock()
		return nil
	}
	c.prompts[prompt]--
	c.jobs[id] = struct{}{}
	c.mu.Unlock()

	if err := c.workflow.Start(ctx); err != nil {
		c.mu.Lock()
		delete(c.jobs, id)
		c.prompts[prompt]++
		c.mu.Unlock()
		return err
	}
	phase, _ := c.phaseNames()
	if _, err := fmt.Fprintf(c.out, "Issue #%dの%sを開始します。\n---\n", c.workflow.IssueNumber(), phase); err != nil {
		c.mu.Lock()
		delete(c.jobs, id)
		c.prompts[prompt]++
		c.mu.Unlock()

		finishErr := c.workflow.Finish(ctx, err)
		if c.phase == issueworkflow.PhaseImplementation && c.closeImplementation != nil {
			finishErr = errors.Join(finishErr, c.closeImplementation())
		}
		return errors.Join(err, finishErr)
	}
	return nil
}

func (c *issueReviewController) OnJobFinish(ctx context.Context, id uint64, _ string, result string, runErr error) error {
	c.mu.Lock()
	_, tracked := c.jobs[id]
	delete(c.jobs, id)
	c.mu.Unlock()
	if !tracked {
		return nil
	}
	artifactPath := ""
	if runErr == nil {
		var err error
		artifactPath, err = c.workflow.SaveResult(result)
		if err != nil {
			finishErr := c.workflow.Finish(ctx, err)
			if c.phase == issueworkflow.PhaseImplementation && c.closeImplementation != nil {
				finishErr = errors.Join(finishErr, c.closeImplementation())
			}
			return errors.Join(err, finishErr)
		}
	}
	if err := c.workflow.Finish(ctx, runErr); err != nil {
		return err
	}
	if runErr != nil {
		if c.phase == issueworkflow.PhaseImplementation && c.closeImplementation != nil {
			return c.closeImplementation()
		}
		return nil
	}

	c.mu.Lock()
	c.pending = true
	c.result = result
	phase, revision := c.phaseNames()
	_, err := fmt.Fprintf(c.out,
		"\n\n---\n\n%s結果を保存しました: %s\n%sが完了しました。承認する場合は未入力状態でEnter、もしくは承認、approve、aのいずれかを入力してください。\n修正する場合は内容を入力してください。AIへ送信して%sします。\n",
		phase, artifactPath, phase, revision)
	c.mu.Unlock()
	return err
}

func (c *issueReviewController) HandleInput(ctx context.Context, input string) (daemon.InputAction, error) {
	c.mu.Lock()
	if !c.pending {
		c.mu.Unlock()
		return daemon.InputAction{}, nil
	}
	result := c.result
	c.mu.Unlock()

	if isApprovalInput(input) {
		prURL, approveErr := c.workflow.Approve(ctx, result)
		if approveErr != nil {
			return daemon.InputAction{Handled: true}, approveErr
		}
		c.mu.Lock()
		c.pending = false
		c.result = ""
		phase, _ := c.phaseNames()
		message := fmt.Sprintf("%sを承認しました。\n", phase)
		if c.phase == issueworkflow.PhaseImplementation && strings.TrimSpace(prURL) != "" {
			message = fmt.Sprintf("実装を承認し、PRを作成しました: %s\n", strings.TrimSpace(prURL))
		}
		_, err := fmt.Fprint(c.out, message)
		if c.phase == issueworkflow.PhaseDesign {
			c.phase = issueworkflow.PhaseImplementation
			c.workflow.SetPhase(issueworkflow.PhaseImplementation)
			prompt := c.workflow.Prompt()
			c.prompts[prompt]++
			job := c.implementationJob
			c.mu.Unlock()
			if err != nil {
				return daemon.InputAction{Handled: true}, err
			}
			if job == nil {
				return daemon.InputAction{Handled: true}, errors.New("implementation job is not configured")
			}
			return daemon.InputAction{Handled: true, Job: job(prompt)}, nil
		}
		c.mu.Unlock()
		if c.closeImplementation != nil {
			err = errors.Join(err, c.closeImplementation())
		}
		return daemon.InputAction{Handled: true, Restart: true}, err
	}

	prompt := c.workflow.RevisionPrompt(input)
	c.mu.Lock()
	c.pending = false
	c.result = ""
	c.prompts[prompt]++
	_, revision := c.phaseNames()
	_, err := fmt.Fprintf(c.out, "フィードバックをAIへ送信し、%sします。\n", revision)
	c.mu.Unlock()
	if c.phase == issueworkflow.PhaseImplementation {
		if c.implementationJob == nil {
			return daemon.InputAction{Handled: true}, errors.New("implementation job is not configured")
		}
		return daemon.InputAction{Handled: true, Job: c.implementationJob(prompt)}, err
	}
	return daemon.InputAction{Handled: true, Prompt: prompt}, err
}

func (c *issueReviewController) registerPrompt(prompt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts[prompt]++
}

func (c *issueReviewController) phaseNames() (phase, revision string) {
	if c.phase == issueworkflow.PhaseImplementation {
		return "実装", "再実装"
	}
	return "設計", "再設計"
}

func isApprovalInput(input string) bool {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "承認", "承認する", "承認します", "approve", "approved", "a", "yes", "y", "ok":
		return true
	default:
		return false
	}
}
