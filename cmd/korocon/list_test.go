package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

func TestRunListIssueIsRejected(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"list", "issue"}, &out, &out)
	if err == nil {
		t.Fatal("korocon list issue was accepted but should be rejected")
	}
	if strings.Contains(out.String(), "Backend feature") {
		t.Fatal("list retrieval was executed despite rejection")
	}
}

func TestRunListPRIsRejected(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"list", "pr"}, &out, &out)
	if err == nil {
		t.Fatal("korocon list pr was accepted but should be rejected")
	}
}

func TestRunListSubcommandAloneIsRejected(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"list"}, &out, &out)
	if err == nil {
		t.Fatal("korocon list was accepted but should be rejected")
	}
}

func TestRunIssueListAppliesOptionsAndFilters(t *testing.T) {
	oldList := listIssuesWithOptions
	oldState := issueWorkflowState
	t.Cleanup(func() {
		listIssuesWithOptions = oldList
		issueWorkflowState = oldState
	})
	issueWorkflowState = func(issue issueworkflow.Issue, _ string) (string, error) {
		if issue.Number == 3 {
			return "state:design_running", nil
		}
		return "state:detected", nil
	}
	var gotDir string
	var gotOptions issueworkflow.IssueListOptions
	listIssuesWithOptions = func(_ context.Context, dir string, options issueworkflow.IssueListOptions) ([]issueworkflow.Issue, error) {
		gotDir, gotOptions = dir, options
		return []issueworkflow.Issue{
			{Number: 3, Title: "API issue", State: "OPEN", Author: issueworkflow.User{Login: "carol"}, Labels: []issueworkflow.Label{{Name: "api"}}},
			{Number: 1, Title: "UI issue", State: "OPEN", Author: issueworkflow.User{Login: "dave"}, Labels: []issueworkflow.Label{{Name: "ui"}}},
		}, nil
	}

	var out bytes.Buffer
	err := runIssue([]string{"list", "--state", "all", "--dir", "/myrepo", "--label", "api"}, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != "/myrepo" || gotOptions.State != "all" {
		t.Fatalf("options = dir %q, %+v", gotDir, gotOptions)
	}
	if !strings.Contains(out.String(), "API issue") || !strings.Contains(out.String(), "設計中") || strings.Contains(out.String(), "UI issue") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunIssueSetStatus(t *testing.T) {
	oldSet := setIssueStatus
	t.Cleanup(func() { setIssueStatus = oldSet })
	var gotDir, gotStatus string
	var gotNumber int
	setIssueStatus = func(_ context.Context, dir string, number int, status string) (string, error) {
		gotDir, gotNumber, gotStatus = dir, number, status
		return "state:detected", nil
	}

	var out bytes.Buffer
	if err := runIssue([]string{"set-status", "25", "design", "--dir", "/repo"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/repo" || gotNumber != 25 || gotStatus != "design" {
		t.Fatalf("set status = dir %q, number %d, status %q", gotDir, gotNumber, gotStatus)
	}
	if !strings.Contains(out.String(), "Issue #25 の工程状態を変更しました: 設計待ち") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunIssueSetStatusRejectsInvalidArguments(t *testing.T) {
	var out bytes.Buffer
	if err := runIssue([]string{"set-status", "0", "design"}, &out, &out); err == nil {
		t.Fatal("invalid Issue number was accepted")
	}
	if err := runIssue([]string{"set-status", "25"}, &out, &out); err == nil {
		t.Fatal("missing status was accepted")
	}
}

func TestRunPRSetStatus(t *testing.T) {
	oldSet := setPRStatus
	t.Cleanup(func() { setPRStatus = oldSet })
	var gotDir, gotStatus string
	var gotNumber int
	setPRStatus = func(_ context.Context, dir string, number int, status string) (string, error) {
		gotDir, gotNumber, gotStatus = dir, number, status
		return "state:pr_review_comment", nil
	}

	var out bytes.Buffer
	if err := runPR([]string{"set-status", "25", "IMPLEMENTATION", "--dir", "/repo"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/repo" || gotNumber != 25 || gotStatus != "IMPLEMENTATION" {
		t.Fatalf("set status = dir %q, number %d, status %q", gotDir, gotNumber, gotStatus)
	}
	if !strings.Contains(out.String(), "PR #25 の工程状態を変更しました: レビュー指摘修正待ち") || !strings.Contains(out.String(), "--implementer --auto") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunPRSetStatusRejectsInvalidArguments(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{"set-status", "0", "review"}, {"set-status", "25"}, {"set-status", "25", "review", "extra"}} {
		if err := runPR(args, &out, &out); err == nil {
			t.Fatalf("invalid PR arguments were accepted: %v", args)
		}
	}
}

func TestRunIssueHelpPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if err := runIssue([]string{"--help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"korocon issue list", "korocon issue set-status"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q: %q", want, out.String())
		}
	}
}

func TestRunPRHelpPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if err := runPR([]string{"help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "korocon pr list") {
		t.Fatalf("help missing 'korocon pr list': %q", out.String())
	}
}

func TestRunIssueUnknownVerbReturnsError(t *testing.T) {
	var out bytes.Buffer
	if err := runIssue([]string{"delete"}, &out, &out); err == nil {
		t.Fatal("unknown verb was accepted")
	}
}

func TestRunPRUnknownVerbReturnsError(t *testing.T) {
	var out bytes.Buffer
	if err := runPR([]string{"merge"}, &out, &out); err == nil {
		t.Fatal("unknown verb was accepted")
	}
}

func TestRunIssueNoArgsReturnsError(t *testing.T) {
	var out bytes.Buffer
	if err := runIssue([]string{}, &out, &out); err == nil {
		t.Fatal("no args was accepted")
	}
}

func TestRunPRNoArgsReturnsError(t *testing.T) {
	var out bytes.Buffer
	if err := runPR([]string{}, &out, &out); err == nil {
		t.Fatal("no args was accepted")
	}
}

func TestWriteIssueListShowsWorkflowState(t *testing.T) {
	old := issueWorkflowState
	t.Cleanup(func() { issueWorkflowState = old })
	issueWorkflowState = func(issue issueworkflow.Issue, _ string) (string, error) {
		states := map[int]string{1: "state:design_running", 2: "state:detected"}
		return states[issue.Number], nil
	}
	var out bytes.Buffer
	issues := []issueworkflow.Issue{
		{Number: 1, Title: "Design issue"},
		{Number: 2, Title: "New issue"},
	}
	if err := writeIssueList(&out, issues, "."); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "設計中") || !strings.Contains(out.String(), "設計待ち") {
		t.Fatalf("output = %q", out.String())
	}
	if strings.Contains(out.String(), "OPEN") || strings.Contains(out.String(), "state:") {
		t.Fatalf("raw state leaked into output: %q", out.String())
	}
}

func TestWritePullRequestListShowsWorkflowState(t *testing.T) {
	old := prWorkflowState
	t.Cleanup(func() { prWorkflowState = old })
	prWorkflowState = func(pr prworkflow.PullRequest, _ string) (string, error) {
		states := map[int]string{1: "state:review_running", 2: ""}
		return states[pr.Number], nil
	}
	var out bytes.Buffer
	prs := []prworkflow.PullRequest{
		{Number: 1, Title: "Reviewing PR", State: "OPEN"},
		{Number: 2, Title: "New PR", State: "OPEN"},
	}
	if err := writePullRequestList(&out, prs, "."); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "レビュー中") || !strings.Contains(out.String(), "レビュー待ち") {
		t.Fatalf("output = %q", out.String())
	}
	if strings.Contains(out.String(), "OPEN") || strings.Contains(out.String(), "state:") {
		t.Fatalf("raw state leaked into output: %q", out.String())
	}
}

func TestWritePullRequestListConflictOverridesState(t *testing.T) {
	old := prWorkflowState
	t.Cleanup(func() { prWorkflowState = old })
	prWorkflowState = func(pr prworkflow.PullRequest, _ string) (string, error) {
		return "state:review_running", nil
	}
	var out bytes.Buffer
	prs := []prworkflow.PullRequest{
		{Number: 1, Title: "Conflict PR", State: "OPEN", Mergeable: "CONFLICTING"},
	}
	if err := writePullRequestList(&out, prs, "."); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "コンフリクト") {
		t.Fatalf("conflict was not shown: %q", out.String())
	}
	if strings.Contains(out.String(), "レビュー中") {
		t.Fatalf("DB state overrode conflict: %q", out.String())
	}
}

func TestWriteIssueListPropagatesStateError(t *testing.T) {
	old := issueWorkflowState
	t.Cleanup(func() { issueWorkflowState = old })
	issueWorkflowState = func(_ issueworkflow.Issue, _ string) (string, error) {
		return "", bytes.ErrTooLarge
	}
	var out bytes.Buffer
	issues := []issueworkflow.Issue{{Number: 1, Title: "Broken"}}
	if err := writeIssueList(&out, issues, "."); err == nil {
		t.Fatal("expected error from state lookup failure")
	}
}

func TestWritePullRequestListPropagatesStateError(t *testing.T) {
	old := prWorkflowState
	t.Cleanup(func() { prWorkflowState = old })
	prWorkflowState = func(_ prworkflow.PullRequest, _ string) (string, error) {
		return "", bytes.ErrTooLarge
	}
	var out bytes.Buffer
	prs := []prworkflow.PullRequest{{Number: 1, Title: "Broken", State: "OPEN"}}
	if err := writePullRequestList(&out, prs, "."); err == nil {
		t.Fatal("expected error from state lookup failure")
	}
}

func TestIssueWorkflowDisplayNameMapsAllExpectedStates(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"state:detected", "設計待ち"},
		{"", "設計待ち"},
		{"state:design_running", "設計中"},
		{"state:design_ready", "設計完了・承認待ち"},
		{"state:design_approved", "実装待ち"},
		{"state:implementation_running", "実装中"},
		{"state:implementation_ready", "実装完了・承認待ち"},
		{"state:pr_created", "PR作成済み"},
		{"state:design_failed", "設計失敗・再実行待ち"},
		{"state:implementation_failed", "実装失敗・再実行待ち"},
		{"state:failed", "失敗・再実行待ち"},
		{"state:unknown_future_state", "不明な状態"},
	}
	for _, c := range cases {
		if got := issueWorkflowDisplayName(c.state); got != c.want {
			t.Errorf("issueWorkflowDisplayName(%q) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestPRWorkflowDisplayNameMapsAllExpectedStates(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"state:detected", "レビュー待ち"},
		{"", "レビュー待ち"},
		{"state:review_running", "レビュー中"},
		{"state:review_ready", "レビュー完了・承認待ち"},
		{"state:review_approved", "レビュー承認済み"},
		{"state:pr_review_comment", "レビュー指摘修正待ち"},
		{"state:review_fix_implementation_running", "レビュー修正実装中"},
		{"state:pr_conflict", "コンフリクト"},
		{"state:pr_conflict_running", "コンフリクト解消中"},
		{"state:pr_conflict_ready", "コンフリクト解消完了・承認待ち"},
		{"state:review_failed", "レビュー失敗・再実行待ち"},
		{"state:completed", "完了"},
		{"state:failed", "失敗"},
		{"state:unknown_future_state", "不明な状態"},
	}
	for _, c := range cases {
		if got := prWorkflowDisplayName(c.state); got != c.want {
			t.Errorf("prWorkflowDisplayName(%q) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestRunIssueListShowsWorkflowStateNotGitHubState(t *testing.T) {
	oldList := listIssuesWithOptions
	oldState := issueWorkflowState
	t.Cleanup(func() {
		listIssuesWithOptions = oldList
		issueWorkflowState = oldState
	})
	listIssuesWithOptions = func(_ context.Context, _ string, _ issueworkflow.IssueListOptions) ([]issueworkflow.Issue, error) {
		return []issueworkflow.Issue{
			{Number: 26, Title: "fix status", State: "OPEN"},
		}, nil
	}
	issueWorkflowState = func(_ issueworkflow.Issue, _ string) (string, error) {
		return "state:design_running", nil
	}

	var out bytes.Buffer
	if err := runIssue([]string{"list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "設計中") {
		t.Fatalf("workflow state not shown, output = %q", out.String())
	}
	if strings.Contains(out.String(), "OPEN") {
		t.Fatalf("GitHub state leaked into output: %q", out.String())
	}
}

func TestRunPRListShowsWorkflowStateNotGitHubState(t *testing.T) {
	oldList := listPullRequestsWithOptions
	oldState := prWorkflowState
	t.Cleanup(func() {
		listPullRequestsWithOptions = oldList
		prWorkflowState = oldState
	})
	listPullRequestsWithOptions = func(_ context.Context, _ string, _ prworkflow.PullRequestListOptions) ([]prworkflow.PullRequest, error) {
		return []prworkflow.PullRequest{
			{Number: 42, Title: "review PR", State: "OPEN"},
		}, nil
	}
	prWorkflowState = func(_ prworkflow.PullRequest, _ string) (string, error) {
		return "state:review_running", nil
	}

	var out bytes.Buffer
	if err := runPR([]string{"list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "レビュー中") {
		t.Fatalf("workflow state not shown, output = %q", out.String())
	}
	if strings.Contains(out.String(), "OPEN") {
		t.Fatalf("GitHub state leaked into output: %q", out.String())
	}
}

func TestRunIssueListJSONPreservesGitHubState(t *testing.T) {
	oldList := listIssuesWithOptions
	t.Cleanup(func() { listIssuesWithOptions = oldList })
	listIssuesWithOptions = func(_ context.Context, _ string, _ issueworkflow.IssueListOptions) ([]issueworkflow.Issue, error) {
		return []issueworkflow.Issue{
			{Number: 1, Title: "issue", State: "OPEN"},
		}, nil
	}

	var out bytes.Buffer
	if err := runIssue([]string{"list", "--json"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "OPEN"`) {
		t.Fatalf("JSON state not preserved, output = %q", out.String())
	}
}
