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
	"time"

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
	workingDir := t.TempDir()
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
	selected, err := selectPullRequest(context.Background(), bufio.NewReader(strings.NewReader("6\n")), &out, workingDir, ".workspace")
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

func TestSelectIssueForImplementerDefaultsToHighestListedIssueWhenInputIsBlank(t *testing.T) {
	originalIssues, originalLoad := listIssues, loadIssue
	t.Cleanup(func() { listIssues, loadIssue = originalIssues, originalLoad })
	listIssues = func(context.Context, string) ([]issueworkflow.Issue, error) {
		return []issueworkflow.Issue{
			{Number: 9, Title: "Issue 9"},
			{Number: 12, Title: "Issue 12", Labels: []issueworkflow.Label{{Name: "state:design_ready"}}},
			{Number: 7, Title: "Issue 7"},
		}, nil
	}
	loaded := 0
	loadIssue = func(_ context.Context, _ string, number int, _ string) (*issueworkflow.Workflow, error) {
		loaded = number
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, Title: "Selected"}, Phase: issueworkflow.PhaseDesign}, nil
	}

	selected, err := selectIssueForRole(context.Background(), bufio.NewReader(strings.NewReader("  \n")), io.Discard, ".", ".workspace", selectionModeImplementer, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 9 || selected.Issue.Number != 9 {
		t.Fatalf("loaded=%d selected=%d, want 9", loaded, selected.Issue.Number)
	}
}

func TestSelectPullRequestDefaultsToHighestListedPRWhenInputIsBlank(t *testing.T) {
	originalList, originalLoad := listPullRequests, loadPullRequest
	t.Cleanup(func() { listPullRequests, loadPullRequest = originalList, originalLoad })
	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{
			{Number: 4, Title: "PR 4", State: "OPEN"},
			{Number: 9, Title: "PR 9", State: "OPEN"},
			{Number: 7, Title: "Merged", State: "MERGED"},
		}, nil
	}
	loaded := 0
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		loaded = number
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "Selected", State: "OPEN"}, Phase: prworkflow.PhaseReview}, nil
	}

	selected, err := selectPullRequest(context.Background(), bufio.NewReader(strings.NewReader(" \t\n")), io.Discard, ".", ".workspace")
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 9 || selected.PR.Number != 9 {
		t.Fatalf("loaded=%d selected=%d, want 9", loaded, selected.PR.Number)
	}
}

func TestGitHubInformationSelectionAcceptsCaseInsensitiveShortcutsAndDefaultsToIssue(t *testing.T) {
	originalLoadIssue := loadIssue
	originalListPRs, originalLoadPR := listPullRequests, loadPullRequest
	t.Cleanup(func() {
		loadIssue = originalLoadIssue
		listPullRequests, loadPullRequest = originalListPRs, originalLoadPR
	})
	loadIssue = func(_ context.Context, _ string, number int, _ string) (*issueworkflow.Workflow, error) {
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, Title: "selected"}, Phase: issueworkflow.PhaseDesign}, nil
	}
	var out strings.Builder
	_, issue, pr, err := selectGitHubInformation(context.Background(), strings.NewReader("\n42\n"), &out, ".", ".workspace", selectionModeDefault, "")
	if err != nil || issue == nil || pr != nil || issue.Issue.Number != 42 {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
	if !strings.Contains(out.String(), "取得する情報を選択してください (ISSUE/PR): ") {
		t.Fatalf("output = %q", out.String())
	}

	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{{Number: 9, Title: "selected", State: "OPEN"}}, nil
	}
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "selected", State: "OPEN"}, Phase: prworkflow.PhaseReview}, nil
	}
	_, issue, pr, err = selectGitHubInformation(context.Background(), strings.NewReader("P\n9\n"), &out, ".", ".workspace", selectionModeDefault, "")
	if err != nil || issue != nil || pr == nil || pr.PR.Number != 9 {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
}

func TestPullRequestStatusUsesJapaneseStateLabel(t *testing.T) {
	oldState := prWorkflowState
	t.Cleanup(func() { prWorkflowState = oldState })
	prWorkflowState = func(pr prworkflow.PullRequest, _ string) (string, error) {
		for _, label := range pr.Labels {
			return label.Name, nil
		}
		return "", nil
	}
	status, err := pullRequestStatus(prworkflow.PullRequest{State: "OPEN", Labels: []prworkflow.Label{{Name: "state:review_approved"}}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if status != "レビュー承認済み" {
		t.Fatalf("status = %q", status)
	}
}

func TestRoleSelectionTargets(t *testing.T) {
	implementerPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:pr_review_comment"}}}
	reviewerPR := prworkflow.PullRequest{}
	reviewFixedPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:review_fixed"}}}
	processedPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:review_ready"}}}
	approvedPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:review_approved"}}}
	if !pullRequestIsRoleTarget(implementerPR, selectionModeImplementer) || pullRequestIsRoleTarget(implementerPR, selectionModeReviewer) {
		t.Fatalf("implementer PR role selection is incorrect")
	}
	if !pullRequestIsRoleTarget(reviewerPR, selectionModeReviewer) || pullRequestIsRoleTarget(reviewerPR, selectionModeImplementer) {
		t.Fatalf("reviewer PR role selection is incorrect")
	}
	if !pullRequestIsRoleTarget(reviewFixedPR, selectionModeReviewer) {
		t.Fatalf("review_fixed PR was not selected for reviewer mode")
	}
	if pullRequestIsRoleTarget(processedPR, selectionModeReviewer) || pullRequestIsRoleTarget(approvedPR, selectionModeReviewer) {
		t.Fatalf("approved PR was selected for reviewer mode")
	}
	if issueIsImplementerTarget(issueworkflow.Issue{Labels: []issueworkflow.Label{{Name: "state:implementation_ready"}}}) {
		t.Fatalf("issue approval state was selected for implementer mode")
	}
	if !issueIsImplementerTarget(issueworkflow.Issue{Labels: []issueworkflow.Label{{Name: "state:design_approved"}}}) {
		t.Fatalf("design-approved issue was not selected for implementer mode")
	}
}

func TestAutoSelectionExcludesRunningTargets(t *testing.T) {
	runningIssue := issueworkflow.Issue{Labels: []issueworkflow.Label{{Name: "state:implementation_running"}}}
	if !issueIsImplementerTarget(runningIssue) || !issueIsRunning(runningIssue) {
		t.Fatal("running issue should be recognized as an implementer target but excluded from auto mode")
	}
	readyIssue := issueworkflow.Issue{Labels: []issueworkflow.Label{{Name: "state:design_approved"}}}
	if issueIsRunning(readyIssue) {
		t.Fatal("non-running issue was classified as running")
	}
	runningPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:review_fix_implementation_running"}}}
	if !pullRequestIsRunning(runningPR) {
		t.Fatal("running PR was not recognized")
	}
	readyPR := prworkflow.PullRequest{Labels: []prworkflow.Label{{Name: "state:review_fix_implementation_ready"}}}
	if pullRequestIsRunning(readyPR) {
		t.Fatal("non-running PR was classified as running")
	}
}

func TestGitHubSelectionFilters(t *testing.T) {
	filters := githubSelectionFilters{
		LabelIncludes: []string{"bug", "backend"},
		LabelExcludes: []string{"blocked"},
		TitleContains: []string{"API", "CLI"},
		Authors:       []string{"alice"},
		ProjectItems: &projectMembership{
			issueNumbers: map[int]struct{}{12: {}},
			prNumbers:    map[int]struct{}{34: {}},
			urls:         map[string]struct{}{},
		},
	}
	issue := issueworkflow.Issue{
		Number: 12, Title: "Improve API", Author: issueworkflow.User{Login: "Alice"},
		Labels: []issueworkflow.Label{{Name: "bug"}, {Name: "backend"}},
	}
	if !matchesIssueFilters(issue, filters) {
		t.Fatal("matching issue was excluded")
	}
	issue.Labels = append(issue.Labels, issueworkflow.Label{Name: "blocked"})
	if matchesIssueFilters(issue, filters) {
		t.Fatal("excluded label was ignored")
	}
	pr := prworkflow.PullRequest{
		Number: 34, Title: "CLI update", Author: prworkflow.User{Login: "alice"},
		Labels: []prworkflow.Label{{Name: "bug"}, {Name: "backend"}},
	}
	if !matchesPullRequestFilters(pr, filters) {
		t.Fatal("matching pull request was excluded")
	}
	pr.Number = 35
	if matchesPullRequestFilters(pr, filters) {
		t.Fatal("pull request outside the project was included")
	}
}

func TestAutoSelectionPrioritizesImplementerIssue(t *testing.T) {
	originalIssues, originalPRs, originalLoadIssue := listIssues, listPullRequests, loadIssue
	t.Cleanup(func() {
		listIssues, listPullRequests, loadIssue = originalIssues, originalPRs, originalLoadIssue
	})
	listIssues = func(context.Context, string) ([]issueworkflow.Issue, error) {
		return []issueworkflow.Issue{{Number: 3, Title: "Issue 3"}, {Number: 8, Title: "Issue 8"}}, nil
	}
	prListed := false
	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		prListed = true
		return nil, nil
	}
	loadIssue = func(_ context.Context, _ string, number int, _ string) (*issueworkflow.Workflow, error) {
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, Title: "Selected"}, Phase: issueworkflow.PhaseDesign}, nil
	}
	issue, pr, err := selectAutoGitHubInformation(context.Background(), io.Discard, ".", ".workspace", selectionModeImplementer, "", githubSelectionFilters{})
	if err != nil || issue == nil || issue.Issue.Number != 8 || pr != nil || prListed {
		t.Fatalf("issue=%+v pr=%+v prListed=%t err=%v", issue, pr, prListed, err)
	}
}

func TestAutoSelectionUsesHighestReviewerPR(t *testing.T) {
	originalPRs, originalLoadPR := listPullRequests, loadPullRequest
	t.Cleanup(func() { listPullRequests, loadPullRequest = originalPRs, originalLoadPR })
	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{
			{Number: 4, Title: "PR 4", State: "OPEN"},
			{Number: 9, Title: "PR 9", State: "OPEN"},
		}, nil
	}
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "Selected", State: "OPEN"}, Phase: prworkflow.PhaseReview}, nil
	}
	issue, pr, err := selectAutoGitHubInformation(context.Background(), io.Discard, ".", ".workspace", selectionModeReviewer, "", githubSelectionFilters{})
	if err != nil || issue != nil || pr == nil || pr.PR.Number != 9 {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
}

func TestAutoImplementerFallsBackToPullRequest(t *testing.T) {
	originalIssues, originalPRs, originalLoadPR := listIssues, listPullRequests, loadPullRequest
	t.Cleanup(func() {
		listIssues, listPullRequests, loadPullRequest = originalIssues, originalPRs, originalLoadPR
	})
	listIssues = func(context.Context, string) ([]issueworkflow.Issue, error) { return nil, nil }
	listPullRequests = func(context.Context, string) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{{
			Number: 7, Title: "Fix review", State: "OPEN",
			Labels: []prworkflow.Label{{Name: "state:pr_review_comment"}},
		}}, nil
	}
	loadPullRequest = func(_ context.Context, _ string, number int, _ string) (*prworkflow.Workflow, error) {
		return &prworkflow.Workflow{PR: prworkflow.PullRequest{Number: number, Title: "Selected", State: "OPEN"}, Phase: prworkflow.PhaseFix}, nil
	}
	issue, pr, err := selectAutoGitHubInformation(context.Background(), io.Discard, ".", ".workspace", selectionModeImplementer, "", githubSelectionFilters{})
	if err != nil || issue != nil || pr == nil || pr.PR.Number != 7 {
		t.Fatalf("issue=%+v pr=%+v err=%v", issue, pr, err)
	}
}

func TestBuildProjectQuery(t *testing.T) {
	tests := []struct {
		status string
		query  string
		want   string
	}{
		{status: "In Progress", want: `status:"In Progress"`},
		{status: "Ready", query: "priority:P1", want: `status:"Ready" priority:P1`},
		{query: "priority:P2", want: "priority:P2"},
		{status: `Needs "Review"`, want: `status:"Needs \"Review\""`},
	}
	for _, test := range tests {
		if got := buildProjectQuery(test.status, test.query); got != test.want {
			t.Fatalf("buildProjectQuery(%q, %q) = %q, want %q", test.status, test.query, got, test.want)
		}
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

func TestRunInteractiveRejectsShortRoleModesTogether(t *testing.T) {
	err := runInteractive([]string{"-i", "-r"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be specified together") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunInteractiveRequiresRoleForAutoMode(t *testing.T) {
	err := runInteractive([]string{"--auto"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requires --implementer") {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitForAutoPollingWaitsAndDisplaysNextFetch(t *testing.T) {
	var out strings.Builder
	if err := waitForAutoPolling(context.Background(), &out, "5m", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "フィルタに一致する自動処理対象がありません。Enterで再取得、5m後に再取得します。") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWaitForAutoPollingRestartsOnEnter(t *testing.T) {
	var out strings.Builder
	started := time.Now()
	if err := waitForAutoPolling(context.Background(), &out, "5m", time.Hour, strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("wait elapsed = %s", elapsed)
	}
}

func TestWaitForAutoPollingIgnoresNonEmptyInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := waitForAutoPolling(ctx, io.Discard, "5m", time.Hour, strings.NewReader("not enter\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitForAutoPollingDiscardsInputAfterWait(t *testing.T) {
	input := newAutoPollingInput(strings.NewReader("entered while waiting\n"))
	if err := waitForAutoPolling(context.Background(), io.Discard, "5m", 10*time.Millisecond, input); err != nil {
		t.Fatal(err)
	}

	input.mu.Lock()
	defer input.mu.Unlock()
	if len(input.waitLines) != 0 || len(input.normalLines) != 0 {
		t.Fatalf("input queues after wait = wait:%d normal:%d", len(input.waitLines), len(input.normalLines))
	}
}

func TestWaitForAutoPollingStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForAutoPolling(ctx, io.Discard, "5m", time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestUsageIncludesShortRoleModes(t *testing.T) {
	var out strings.Builder
	printUsage(&out)
	if !strings.Contains(out.String(), "-i") || !strings.Contains(out.String(), "-r") {
		t.Fatalf("usage = %q", out.String())
	}
}

func TestRunInteractiveFallsBackToInitialSelectionWhenRequestedIssueIsMissing(t *testing.T) {
	original := loadIssue
	originalUser := currentGitHubUser
	t.Cleanup(func() { loadIssue = original; currentGitHubUser = originalUser })
	currentGitHubUser = func(context.Context, string) (string, error) { return "test-user", nil }
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
		!strings.Contains(out.String(), "取得する情報を選択してください (ISSUE/PR):") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestExplicitBlankAssigneeDisablesCurrentUserLookup(t *testing.T) {
	originalLoadIssue := loadIssue
	originalUser := currentGitHubUser
	t.Cleanup(func() { loadIssue, currentGitHubUser = originalLoadIssue, originalUser })
	currentGitHubUser = func(context.Context, string) (string, error) {
		return "", errors.New("current user lookup should not be called")
	}
	loadIssue = func(context.Context, string, int, string) (*issueworkflow.Workflow, error) {
		return nil, errors.New("not found")
	}
	var out strings.Builder
	err := runInteractive([]string{"--issue", "404", "--assignee", "", "--log-file", filepath.Join(t.TempDir(), "korocon.log")}, strings.NewReader(""), &out, io.Discard)
	if err == nil || strings.Contains(err.Error(), "current user lookup") {
		t.Fatalf("error = %v", err)
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
	if implementer.Provider != "copilot" || verifier.Provider != "copilot" || verifier.Model != "auto" {
		t.Fatalf("implementer=%+v verifier=%+v", implementer, verifier)
	}
	if reviewer.Provider != "codex" || reviewer.Model != "claude-sonnet" {
		t.Fatalf("reviewer=%+v", reviewer)
	}
}

func TestResolveAISelectionDefaultsEmptyCopilotModelToAuto(t *testing.T) {
	selection, err := resolveAISelection("copilot", "", aiSelection{Provider: "codex", Model: defaultModel})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Provider != "copilot" || selection.Model != "auto" {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestResolveAISelectionConvertsCodexDefaultForCopilotToAuto(t *testing.T) {
	selection, err := resolveAISelection("copilot", defaultModel, aiSelection{Provider: "codex", Model: defaultModel})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Model != "auto" {
		t.Fatalf("selection = %+v", selection)
	}
}

func TestResolveAISelectionRejectsUnsupportedProvider(t *testing.T) {
	if _, err := resolveAISelection("unknown", "model", aiSelection{Provider: "codex", Model: defaultModel}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestPullRequestAIUsesSeparateRoleByPhase(t *testing.T) {
	implementer := aiSelection{Provider: "codex", Model: "implementer"}
	reviewer := aiSelection{Provider: "copilot", Model: "reviewer"}
	for _, phase := range []prworkflow.Phase{prworkflow.PhaseFix, prworkflow.PhaseConflict} {
		if got := pullRequestAI(phase, implementer, reviewer); got != implementer {
			t.Fatalf("phase %q AI = %+v, want implementer", phase, got)
		}
	}
	for _, phase := range []prworkflow.Phase{prworkflow.PhaseReview, prworkflow.PhaseVerification} {
		if got := pullRequestAI(phase, implementer, reviewer); got != reviewer {
			t.Fatalf("phase %q AI = %+v, want reviewer", phase, got)
		}
	}
}

func TestPullRequestUsesReviewerWorktree(t *testing.T) {
	for _, phase := range []prworkflow.Phase{
		prworkflow.PhaseReview,
		prworkflow.PhaseReviewApproved,
		prworkflow.PhaseVerification,
		prworkflow.PhaseReviewFailed,
	} {
		if !pullRequestUsesReviewerWorktree(phase) {
			t.Fatalf("phase %q should use reviewer worktree", phase)
		}
	}
	for _, phase := range []prworkflow.Phase{
		prworkflow.PhaseFix,
		prworkflow.PhaseConflict,
		prworkflow.PhaseFixFailed,
		prworkflow.PhaseConflictFailed,
	} {
		if pullRequestUsesReviewerWorktree(phase) {
			t.Fatalf("phase %q should use fix engine worktree handling", phase)
		}
	}
}

func TestRunInteractiveDisplaysRoleAISelectionsFromFlags(t *testing.T) {
	original := loadIssue
	originalUser := currentGitHubUser
	t.Cleanup(func() { loadIssue = original; currentGitHubUser = originalUser })
	currentGitHubUser = func(context.Context, string) (string, error) { return "test-user", nil }
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
		"  implementer     : copilot / implementer-model / copilot",
		"  verifier        : codex / verifier-model / codex",
		"  reviewer        : copilot / reviewer-model / copilot",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}

func TestWriteStartupSummaryGroupsAndAlignsFields(t *testing.T) {
	var out strings.Builder
	implementer := aiSelection{Provider: "codex", Model: "implementer", Binary: "codex"}
	verifier := aiSelection{Provider: "copilot", Model: "verifier", Binary: "copilot"}
	reviewer := aiSelection{Provider: "codex", Model: "reviewer", Binary: "codex"}
	if err := writeStartupSummary(&out, implementer, verifier, reviewer, "未設定", "issue_#13", "main", "未設定"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"AI:\n", "GitHub:\n", "Workflow:\n", "github reviewer", "branch", "base branch", "startup command", "未設定"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
	colonColumn := -1
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  ") {
			column := strings.Index(line, ":")
			if colonColumn == -1 {
				colonColumn = column
			} else if column != colonColumn {
				t.Fatalf("colon column = %d in %q, want %d", column, line, colonColumn)
			}
		}
	}
}

func TestWriteStartupSummaryOmitsAISelectionsMatchingImplementer(t *testing.T) {
	var out strings.Builder
	implementer := aiSelection{Provider: "codex", Model: "same", Binary: "codex"}
	if err := writeStartupSummary(&out, implementer, implementer, implementer, "reviewer", "branch", "main", "command"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "\n  verifier") || strings.Contains(got, "\n  reviewer") {
		t.Fatalf("summary = %q, duplicate AI roles should be omitted", got)
	}
	if !strings.Contains(got, "implementer") || !strings.Contains(got, "github reviewer") {
		t.Fatalf("summary = %q, required fields are missing", got)
	}
}
