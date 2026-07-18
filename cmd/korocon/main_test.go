package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

type blockingReader struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.release
	return 0, io.EOF
}

func TestReadStringContextStopsWhenContextIsCanceled(t *testing.T) {
	blocking := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	reader := bufio.NewReader(blocking)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := readStringContext(ctx, reader)
		result <- err
	}()
	<-blocking.started
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("error = %v, want context canceled", err)
	}
	close(blocking.release)
}

func TestRemainingInputPreservesBufferedData(t *testing.T) {
	original := bytes.NewBufferString("issue\n42\nnext prompt\n")
	reader := bufio.NewReader(original)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	remaining := remainingInput(original, reader)
	data, err := io.ReadAll(remaining)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "next prompt\n" {
		t.Fatalf("remaining input = %q", data)
	}
}

func TestRemainingInputReturnsOriginalWithoutReadAhead(t *testing.T) {
	original := bytes.NewBuffer(nil)
	reader := bufio.NewReader(original)
	if got := remainingInput(original, reader); got != original {
		t.Fatal("original terminal reader was not restored")
	}
}

func TestSelectPullRequestDisplaysStatusAndLoadsSelectedNumber(t *testing.T) {
	originalList, originalLoad := listPullRequests, loadPullRequest
	t.Cleanup(func() { listPullRequests, loadPullRequest = originalList, originalLoad })
	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{
			{Number: 3, Title: "Merged", State: "MERGED", ReviewDecision: "APPROVED", HeadRefName: "feature/3", BaseRefName: "main"},
			{Number: 4, Title: "Draft", State: "OPEN", IsDraft: true, Labels: []prworkflow.Label{{Name: "state:review_approved"}}, HeadRefName: "feature/4", BaseRefName: "develop"},
			{Number: 5, Title: "Review", State: "OPEN", Labels: []prworkflow.Label{{Name: "state:review_approved"}}, HeadRefName: "feature/5", BaseRefName: "main"},
			{Number: 6, Title: "Conflict", State: "OPEN", Mergeable: "CONFLICTING", Labels: []prworkflow.Label{{Name: "state:pr_review_comment"}}, HeadRefName: "feature/6", BaseRefName: "main"},
		}, nil
	}
	loaded := 0
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		loaded = number
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "Conflict", State: "OPEN", Mergeable: "CONFLICTING", HeadRefName: "feature/6", BaseRefName: "main"}, Phase: prworkflow.PhaseConflict}, nil
	}
	var out strings.Builder
	selected, err := selectPullRequest(context.Background(), bufio.NewReader(strings.NewReader("6\n")), &out, ".", ".workspace")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 6 || selected.PR.Number != 6 || selected.Phase != prworkflow.PhaseConflict {
		t.Fatalf("loaded=%d selected=%+v", loaded, selected.PR)
	}
	tableOutput := strings.Split(out.String(), "\nPR番号")[0]
	if !strings.Contains(tableOutput, "番号") || !strings.Contains(tableOutput, "状態") || !strings.Contains(tableOutput, "タイトル") ||
		strings.Contains(tableOutput, "Merged") || strings.Contains(tableOutput, "Draft") ||
		strings.Contains(tableOutput, "APPROVED") || !strings.Contains(tableOutput, "5     レビュー承認済み") ||
		!strings.Contains(tableOutput, "6     コンフリクト") {
		t.Fatalf("output = %q", tableOutput)
	}
}

func TestPullRequestStatusUsesJapaneseStateLabel(t *testing.T) {
	status := pullRequestStatus(prworkflow.PullRequest{State: "OPEN", Labels: []prworkflow.Label{{Name: "state:review_approved"}}})
	if status != "レビュー承認済み" {
		t.Fatalf("status = %q", status)
	}
}

func TestSelectRequestedGitHubInformationLoadsIssue(t *testing.T) {
	original := loadIssue
	t.Cleanup(func() { loadIssue = original })
	loadIssue = func(_ context.Context, _ string, number int, _ string) (*issueworkflow.Workflow, error) {
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, Title: "Feature"}, Phase: issueworkflow.PhaseImplementation}, nil
	}
	var out strings.Builder
	issue, pr, err := selectRequestedGitHubInformation(context.Background(), &out, ".", ".workspace", requestedGitHubInformation{issueSpecified: true, issueNumber: 12})
	if err != nil || issue == nil || issue.Issue.Number != 12 || pr != nil {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
	if !strings.Contains(out.String(), "Issue: #12 Feature") || !strings.Contains(out.String(), "実行工程: 実装") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSelectRequestedGitHubInformationLoadsPullRequest(t *testing.T) {
	original := loadPullRequest
	t.Cleanup(func() { loadPullRequest = original })
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "Review", State: "OPEN"}, Phase: prworkflow.PhaseReview}, nil
	}
	var out strings.Builder
	issue, pr, err := selectRequestedGitHubInformation(context.Background(), &out, ".", ".workspace", requestedGitHubInformation{prSpecified: true, prNumber: 8})
	if err != nil || issue != nil || pr == nil || pr.PR.Number != 8 {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
	if !strings.Contains(out.String(), "PR: #8 Review") || !strings.Contains(out.String(), "実行工程: レビュー") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSelectRequestedGitHubInformationReturnsLoadError(t *testing.T) {
	original := loadIssue
	t.Cleanup(func() { loadIssue = original })
	loadIssue = func(context.Context, string, int, string) (*issueworkflow.Workflow, error) {
		return nil, errors.New("not found")
	}
	_, _, err := selectRequestedGitHubInformation(context.Background(), io.Discard, ".", ".workspace", requestedGitHubInformation{issueSpecified: true, issueNumber: 404})
	if err == nil || !strings.Contains(err.Error(), "Issue #404") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunInteractiveRejectsIssueAndPRTogether(t *testing.T) {
	err := runInteractive([]string{"--issue", "1", "--pr", "2"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be specified together") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunInteractiveFallsBackToInitialSelectionWhenRequestedIssueIsMissing(t *testing.T) {
	original := loadIssue
	t.Cleanup(func() { loadIssue = original })
	loadIssue = func(context.Context, string, int, string) (*issueworkflow.Workflow, error) {
		return nil, errors.New("not found")
	}
	var out strings.Builder
	err := runInteractive([]string{"--issue", "404", "--log-file", filepath.Join(t.TempDir(), "korocon.log")}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "GitHub情報の選択を読み取れません") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out.String(), "指定された対象を取得できませんでした") ||
		!strings.Contains(out.String(), "通常の選択へ戻ります") ||
		!strings.Contains(out.String(), "取得する情報を選択してください (issue/pr):") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestResolveAISelectionDefaultsRolesToImplementer(t *testing.T) {
	implementer, err := resolveAISelection("github_copilot", "claude-sonnet", aiSelection{Provider: "codex", Model: defaultModel})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := resolveAISelection("", "", implementer)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := resolveAISelection("codex", "", implementer)
	if err != nil {
		t.Fatal(err)
	}
	if implementer.Provider != "copilot" || verifier != implementer {
		t.Fatalf("implementer=%+v verifier=%+v", implementer, verifier)
	}
	if reviewer.Provider != "codex" || reviewer.Model != "claude-sonnet" {
		t.Fatalf("reviewer=%+v", reviewer)
	}
}

func TestResolveAISelectionRejectsUnsupportedProvider(t *testing.T) {
	if _, err := resolveAISelection("unknown", "model", aiSelection{Provider: "codex", Model: defaultModel}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestRunInteractiveDisplaysRoleAISelectionsFromFlags(t *testing.T) {
	original := loadIssue
	t.Cleanup(func() { loadIssue = original })
	loadIssue = func(context.Context, string, int, string) (*issueworkflow.Workflow, error) {
		return nil, errors.New("not found")
	}
	var stderr strings.Builder
	err := runInteractive([]string{
		"--issue", "404", "--log-file", filepath.Join(t.TempDir(), "korocon.log"),
		"--implementer-provider", "copilot", "--implementer-model", "implementer-model",
		"--verifier-provider", "codex", "--verifier-model", "verifier-model",
		"--reviewer-provider", "github_copilot", "--reviewer-model", "reviewer-model",
	}, strings.NewReader(""), io.Discard, &stderr)
	if err == nil {
		t.Fatal("expected EOF after fallback selection")
	}
	for _, want := range []string{
		"implementer: copilot / implementer-model",
		"verifier: codex / verifier-model",
		"reviewer: copilot / reviewer-model",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}
