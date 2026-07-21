package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	prworkflow "github.com/coco-papiyon/korocon/internal/pullrequest"
)

func runList(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printListUsage(stdout)
		return errors.New("list subcommand is required: issue or pr")
	}
	if args[0] == "--help" || args[0] == "help" {
		printListUsage(stdout)
		return nil
	}

	kind := strings.ToLower(strings.TrimSpace(args[0]))
	if kind != "issue" && kind != "pr" {
		return fmt.Errorf("unknown list target %q (use 'issue' or 'pr')", args[0])
	}

	fs := flag.NewFlagSet("korocon list "+kind, flag.ContinueOnError)
	fs.SetOutput(stderr)
	state := fs.String("state", "open", "state: open, closed, or all")
	dir := fs.String("dir", ".", "working directory")
	search := fs.String("search", "", "GitHub advanced search query")
	jsonOutput := fs.Bool("json", false, "output JSON")
	var labelIncludes, labelExcludes, titleContains, authors stringListFlag
	fs.Var(&labelIncludes, "label", "require label (repeatable)")
	fs.Var(&labelExcludes, "exclude-label", "exclude label (repeatable)")
	fs.Var(&titleContains, "title", "require title substring (repeatable, OR)")
	fs.Var(&authors, "author", "filter by author (repeatable, OR)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	stateValue := strings.ToLower(strings.TrimSpace(*state))
	if stateValue != "open" && stateValue != "closed" && stateValue != "all" {
		return fmt.Errorf("--state must be open, closed, or all: %q", *state)
	}
	filters := githubSelectionFilters{
		LabelIncludes: labelIncludes,
		LabelExcludes: labelExcludes,
		TitleContains: titleContains,
		Authors:       authors,
	}

	ctx := context.Background()
	if kind == "issue" {
		issues, err := listIssuesWithOptions(ctx, *dir, issueworkflow.IssueListOptions{State: stateValue, Search: *search})
		if err != nil {
			return fmt.Errorf("Issue一覧の取得に失敗しました: %w", err)
		}
		issues = filterListedIssues(issues, filters)
		if issues == nil {
			issues = []issueworkflow.Issue{}
		}
		sort.Slice(issues, func(i, j int) bool { return issues[i].Number > issues[j].Number })
		if *jsonOutput {
			return writeListJSON(stdout, issues)
		}
		return writeIssueList(stdout, issues)
	}

	prs, err := listPullRequestsWithOptions(ctx, *dir, prworkflow.PullRequestListOptions{State: stateValue, Search: *search})
	if err != nil {
		return fmt.Errorf("PR一覧の取得に失敗しました: %w", err)
	}
	prs = filterListedPullRequests(prs, filters)
	if stateValue == "open" {
		prs = filterOpenPullRequests(prs)
	}
	if prs == nil {
		prs = []prworkflow.PullRequest{}
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	if *jsonOutput {
		return writeListJSON(stdout, prs)
	}
	return writePullRequestList(stdout, prs)
}

func filterListedIssues(issues []issueworkflow.Issue, filters githubSelectionFilters) []issueworkflow.Issue {
	return slices.DeleteFunc(issues, func(issue issueworkflow.Issue) bool {
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, label.Name)
		}
		return !matchesCommonFilters(issue.Title, labels, issue.Author.Login, filters)
	})
}

func filterListedPullRequests(prs []prworkflow.PullRequest, filters githubSelectionFilters) []prworkflow.PullRequest {
	return slices.DeleteFunc(prs, func(pr prworkflow.PullRequest) bool {
		labels := make([]string, 0, len(pr.Labels))
		for _, label := range pr.Labels {
			labels = append(labels, label.Name)
		}
		return !matchesCommonFilters(pr.Title, labels, pr.Author.Login, filters)
	})
}

func filterOpenPullRequests(prs []prworkflow.PullRequest) []prworkflow.PullRequest {
	return slices.DeleteFunc(prs, func(pr prworkflow.PullRequest) bool { return pr.IsDraft })
}

func writeListJSON(out io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", encoded)
	return err
}

func writeIssueList(out io.Writer, issues []issueworkflow.Issue) error {
	if len(issues) == 0 {
		_, err := fmt.Fprintln(out, "表示対象のIssueがありません")
		return err
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "番号\t状態\tタイトル\t作成者\t担当者\tURL"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "----\t----\t--------\t------\t------\t---"); err != nil {
		return err
	}
	for _, issue := range issues {
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\n", issue.Number, displayValue(issue.State), tableCell(issue.Title), displayValue(issue.Author.Login), issueAssignees(issue.Assignees), displayValue(issue.URL)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writePullRequestList(out io.Writer, prs []prworkflow.PullRequest) error {
	if len(prs) == 0 {
		_, err := fmt.Fprintln(out, "表示対象のPRがありません")
		return err
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "番号\t状態\tDraft\tレビュー判定\tタイトル\t作成者\tブランチ\tURL"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(table, "----\t----\t-----\t------------\t--------\t------\t--------\t---"); err != nil {
		return err
	}
	for _, pr := range prs {
		branch := strings.TrimSpace(pr.HeadRefName)
		if base := strings.TrimSpace(pr.BaseRefName); base != "" {
			branch += " -> " + base
		}
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", pr.Number, displayValue(pr.State), yesNo(pr.IsDraft), displayValue(pr.ReviewDecision), tableCell(pr.Title), displayValue(pr.Author.Login), tableCell(branch), displayValue(pr.URL)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func issueAssignees(users []issueworkflow.User) string {
	values := make([]string, 0, len(users))
	for _, user := range users {
		if value := strings.TrimSpace(user.Login); value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func displayValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return tableCell(value)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func printListUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  korocon list issue [options]
  korocon list pr [options]

Options:
  --state STATE         open, closed, or all (default: open)
  --dir PATH            working directory (default: .)
  --search QUERY        GitHub advanced search query
  --label NAME          require a label; repeatable
  --exclude-label NAME  exclude a label; repeatable
  --title TEXT          require a title substring; repeatable (OR)
  --author USER         filter by author; repeatable (OR)
  --json                output JSON instead of a table`)
}
