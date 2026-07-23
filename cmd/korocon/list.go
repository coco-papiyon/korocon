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

func runIssue(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printIssueUsage(stdout)
		return errors.New("issue verb is required: list")
	}
	if args[0] == "--help" || args[0] == "help" {
		printIssueUsage(stdout)
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[0]))
	switch verb {
	case "list":
		return runIssueList(args[1:], "korocon issue list", stdout, stderr)
	default:
		printIssueUsage(stderr)
		return fmt.Errorf("unknown issue verb %q (use 'list')", args[0])
	}
}

func runPR(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPRUsage(stdout)
		return errors.New("pr verb is required: list")
	}
	if args[0] == "--help" || args[0] == "help" {
		printPRUsage(stdout)
		return nil
	}
	verb := strings.ToLower(strings.TrimSpace(args[0]))
	switch verb {
	case "list":
		return runPRList(args[1:], "korocon pr list", stdout, stderr)
	default:
		printPRUsage(stderr)
		return fmt.Errorf("unknown pr verb %q (use 'list')", args[0])
	}
}

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
	switch kind {
	case "issue":
		return runIssueList(args[1:], "korocon list issue", stdout, stderr)
	case "pr":
		return runPRList(args[1:], "korocon list pr", stdout, stderr)
	default:
		return fmt.Errorf("unknown list target %q (use 'issue' or 'pr')", args[0])
	}
}

// issueWorkflowState resolves the persisted workflow state for an issue.
// Overridable for testing.
var issueWorkflowState = issueworkflow.StateForIssue

// prWorkflowState resolves the persisted workflow state for a pull request.
// Overridable for testing.
var prWorkflowState = prworkflow.StateForPullRequest

func runIssueList(args []string, cmdName string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
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
	if err := fs.Parse(args); err != nil {
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
	return writeIssueList(stdout, issues, *dir)
}

func runPRList(args []string, cmdName string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
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
	if err := fs.Parse(args); err != nil {
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
	return writePullRequestList(stdout, prs, *dir)
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

func writeIssueList(out io.Writer, issues []issueworkflow.Issue, dir string) error {
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
		state, err := issueWorkflowState(issue, dir)
		if err != nil {
			return fmt.Errorf("Issue #%dの工程状態の取得に失敗しました: %w", issue.Number, err)
		}
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\n", issue.Number, issueWorkflowDisplayName(state), tableCell(issue.Title), displayValue(issue.Author.Login), issueAssignees(issue.Assignees), displayValue(issue.URL)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writePullRequestList(out io.Writer, prs []prworkflow.PullRequest, dir string) error {
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
		var status string
		if prworkflow.HasConflict(pr) {
			status = "コンフリクト"
		} else {
			state, err := prWorkflowState(pr, dir)
			if err != nil {
				return fmt.Errorf("PR #%dの工程状態の取得に失敗しました: %w", pr.Number, err)
			}
			status = prWorkflowDisplayName(state)
		}
		branch := strings.TrimSpace(pr.HeadRefName)
		if base := strings.TrimSpace(pr.BaseRefName); base != "" {
			branch += " -> " + base
		}
		if _, err := fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", pr.Number, status, yesNo(pr.IsDraft), displayValue(pr.ReviewDecision), tableCell(pr.Title), displayValue(pr.Author.Login), tableCell(branch), displayValue(pr.URL)); err != nil {
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

func printIssueUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  korocon issue list [options]

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

func printPRUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  korocon pr list [options]

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

func printListUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage (互換別名):
  korocon list issue [options]
  korocon list pr [options]

正式形は 'korocon issue list' および 'korocon pr list' を使用してください。

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

var issueStateDisplayNames = map[string]string{
	"":                               "設計待ち",
	"state:detected":                 "設計待ち",
	"state:design_running":           "設計中",
	"state:design_ready":             "設計完了・承認待ち",
	"state:design_approved":          "実装待ち",
	"state:implementation_running":   "実装中",
	"state:implementation_ready":     "実装完了・承認待ち",
	"state:implementation_approved":  "実装承認済み",
	"state:pr_created":               "PR作成済み",
	"state:design_failed":            "設計失敗・再実行待ち",
	"state:implementation_failed":    "実装失敗・再実行待ち",
	"state:failed":                   "失敗・再実行待ち",
}

func issueWorkflowDisplayName(state string) string {
	if name, ok := issueStateDisplayNames[strings.ToLower(strings.TrimSpace(state))]; ok {
		return name
	}
	return "不明な状態"
}

var prStateDisplayNames = map[string]string{
	"":                                         "未レビュー",
	"state:detected":                           "未レビュー",
	"state:pr_created":                         "PR作成済み",
	"state:pr_review_comment":                  "レビュー修正指示あり",
	"state:pr_conflict":                        "コンフリクト",
	"state:pr_conflict_running":                "コンフリクト解消中",
	"state:pr_conflict_ready":                  "コンフリクト解消完了・承認待ち",
	"state:pr_conflict_resolved":               "コンフリクト解消済み",
	"state:pr_conflict_failed":                 "コンフリクト解消失敗・再実行待ち",
	"state:review_fix_design_running":          "レビュー修正設計中",
	"state:review_fix_design_ready":            "レビュー修正設計完了・承認待ち",
	"state:review_fix_design_approved":         "レビュー修正設計承認済み",
	"state:review_fix_implementation_running":  "レビュー修正実装中",
	"state:review_fix_implementation_ready":    "レビュー修正実装完了・承認待ち",
	"state:review_fix_implementation_approved": "レビュー修正実装承認済み",
	"state:review_fixed":                       "レビュー修正済み",
	"state:review_running":                     "レビュー中",
	"state:review_ready":                       "レビュー完了・承認待ち",
	"state:review_approved":                    "レビュー承認済み",
	"state:review_failed":                      "レビュー失敗・再実行待ち",
	"state:review_fix_failed":                  "レビュー修正失敗・再実行待ち",
	"state:completed":                          "完了",
	"state:failed":                             "失敗",
}

func prWorkflowDisplayName(state string) string {
	if name, ok := prStateDisplayNames[strings.ToLower(strings.TrimSpace(state))]; ok {
		return name
	}
	return "不明な状態"
}
