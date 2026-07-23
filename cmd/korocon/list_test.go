package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

func TestRunListIssueTableAppliesOptionsAndFilters(t *testing.T) {
	oldList := listIssuesWithOptions
	t.Cleanup(func() { listIssuesWithOptions = oldList })
	var gotDir string
	var gotOptions issueworkflow.IssueListOptions
	listIssuesWithOptions = func(_ context.Context, dir string, options issueworkflow.IssueListOptions) ([]issueworkflow.Issue, error) {
		gotDir, gotOptions = dir, options
		return []issueworkflow.Issue{
			{Number: 2, Title: "Backend feature", State: "OPEN", Author: issueworkflow.User{Login: "alice"}, Labels: []issueworkflow.Label{{Name: "backend"}}},
			{Number: 1, Title: "Frontend feature", State: "OPEN", Author: issueworkflow.User{Login: "bob"}, Labels: []issueworkflow.Label{{Name: "frontend"}}},
		}, nil
	}

	var out bytes.Buffer
	err := runList([]string{"issue", "--state", "closed", "--dir", "/repo", "--label", "backend", "--author", "alice"}, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != "/repo" || gotOptions.State != "closed" || gotOptions.Search != "" {
		t.Fatalf("options = dir %q, %+v", gotDir, gotOptions)
	}
	if !strings.Contains(out.String(), "Backend feature") || strings.Contains(out.String(), "Frontend feature") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunListPRJSONExcludesDraftByDefault(t *testing.T) {
	oldList := listPullRequestsWithOptions
	t.Cleanup(func() { listPullRequestsWithOptions = oldList })
	listPullRequestsWithOptions = func(_ context.Context, _ string, options prworkflow.PullRequestListOptions) ([]prworkflow.PullRequest, error) {
		if options.State != "open" || options.Search != "is:open" {
			t.Fatalf("options = %+v", options)
		}
		return []prworkflow.PullRequest{
			{Number: 2, Title: "Draft PR", State: "OPEN", IsDraft: true},
			{Number: 1, Title: "Ready PR", State: "OPEN", IsDraft: false},
		}, nil
	}

	var out bytes.Buffer
	err := runList([]string{"pr", "--search", "is:open", "--json"}, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Draft PR") || !strings.Contains(out.String(), "Ready PR") {
		t.Fatalf("unexpected JSON output: %q", out.String())
	}
}

func TestRunListAllowsAllPRsIncludingDrafts(t *testing.T) {
	oldList := listPullRequestsWithOptions
	t.Cleanup(func() { listPullRequestsWithOptions = oldList })
	listPullRequestsWithOptions = func(_ context.Context, _ string, options prworkflow.PullRequestListOptions) ([]prworkflow.PullRequest, error) {
		if options.State != "all" {
			t.Fatalf("options = %+v", options)
		}
		return []prworkflow.PullRequest{{Number: 2, Title: "Draft PR", IsDraft: true}}, nil
	}

	var out bytes.Buffer
	if err := runList([]string{"pr", "--state", "all"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Draft PR") {
		t.Fatalf("draft PR was omitted: %q", out.String())
	}
}

func TestRunListRejectsInvalidState(t *testing.T) {
	var out bytes.Buffer
	if err := runList([]string{"issue", "--state", "running"}, &out, &out); err == nil {
		t.Fatal("invalid state was accepted")
	}
}

func TestRunIssueListAppliesOptionsAndFilters(t *testing.T) {
	oldList := listIssuesWithOptions
	t.Cleanup(func() { listIssuesWithOptions = oldList })
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
	if !strings.Contains(out.String(), "API issue") || strings.Contains(out.String(), "UI issue") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunIssueListMatchesRunListIssue(t *testing.T) {
	oldList := listIssuesWithOptions
	t.Cleanup(func() { listIssuesWithOptions = oldList })
	fixture := []issueworkflow.Issue{
		{Number: 5, Title: "Shared issue", State: "OPEN", Author: issueworkflow.User{Login: "eve"}},
	}
	listIssuesWithOptions = func(_ context.Context, _ string, _ issueworkflow.IssueListOptions) ([]issueworkflow.Issue, error) {
		return fixture, nil
	}

	var outNew, outOld bytes.Buffer
	if err := runIssue([]string{"list", "--state", "open"}, &outNew, &outNew); err != nil {
		t.Fatal(err)
	}
	if err := runList([]string{"issue", "--state", "open"}, &outOld, &outOld); err != nil {
		t.Fatal(err)
	}
	if outNew.String() != outOld.String() {
		t.Fatalf("output mismatch:\nnew: %q\nold: %q", outNew.String(), outOld.String())
	}
}

func TestRunPRListMatchesRunListPR(t *testing.T) {
	oldList := listPullRequestsWithOptions
	t.Cleanup(func() { listPullRequestsWithOptions = oldList })
	fixture := []prworkflow.PullRequest{
		{Number: 7, Title: "Shared PR", State: "OPEN", IsDraft: false, Author: prworkflow.User{Login: "frank"}},
	}
	listPullRequestsWithOptions = func(_ context.Context, _ string, _ prworkflow.PullRequestListOptions) ([]prworkflow.PullRequest, error) {
		return fixture, nil
	}

	var outNew, outOld bytes.Buffer
	if err := runPR([]string{"list", "--state", "open"}, &outNew, &outNew); err != nil {
		t.Fatal(err)
	}
	if err := runList([]string{"pr", "--state", "open"}, &outOld, &outOld); err != nil {
		t.Fatal(err)
	}
	if outNew.String() != outOld.String() {
		t.Fatalf("output mismatch:\nnew: %q\nold: %q", outNew.String(), outOld.String())
	}
}

func TestRunIssueHelpPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if err := runIssue([]string{"--help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "korocon issue list") {
		t.Fatalf("help missing 'korocon issue list': %q", out.String())
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
