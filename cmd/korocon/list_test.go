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
