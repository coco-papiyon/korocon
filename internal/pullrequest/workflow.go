package pullrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coco-papiyon/korocon/internal/workflowstate"
)

type Phase string

const (
	PhaseReview         Phase = "review"
	PhaseReviewApproved Phase = "review_approved"
	PhaseFix            Phase = "review_fix_implementation"
	PhaseConflict       Phase = "pr_conflict"
	PhaseVerification   Phase = "verification"
	PhaseReviewFailed   Phase = "review_failed"
	PhaseFixFailed      Phase = "review_fix_failed"
	PhaseConflictFailed Phase = "pr_conflict_failed"
)

var stateLabels = map[string]struct{}{
	"state:detected": {}, "state:design_running": {}, "state:design_ready": {},
	"state:design_approved": {}, "state:implementation_running": {},
	"state:implementation_ready": {}, "state:implementation_approved": {},
	"state:pr_created": {}, "state:pr_review_comment": {}, "state:pr_conflict": {},
	"state:pr_conflict_running": {}, "state:pr_conflict_ready": {},
	"state:pr_conflict_resolved": {}, "state:review_fix_design_running": {},
	"state:review_fix_design_ready": {}, "state:review_fix_design_approved": {},
	"state:review_fix_implementation_running": {}, "state:review_fix_implementation_ready": {},
	"state:review_fix_implementation_approved": {}, "state:review_fixed": {},
	"state:review_running": {}, "state:review_ready": {},
	"state:review_approved": {}, "state:completed": {}, "state:failed": {},
	"state:review_failed": {}, "state:review_fix_failed": {}, "state:pr_conflict_failed": {},
}

type Label struct {
	Name string `json:"name"`
}

type User struct {
	Login string `json:"login"`
}

type Comment struct {
	Author User   `json:"author"`
	Body   string `json:"body"`
}

type Review struct {
	Author User   `json:"author"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

type File struct {
	Path string `json:"path"`
}

type InlineComment struct {
	Author    User   `json:"user"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line"`
}

type apiComment struct {
	Author User   `json:"user"`
	Body   string `json:"body"`
}

type apiReview struct {
	Author User   `json:"user"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

type PullRequest struct {
	Number           int       `json:"number"`
	Title            string    `json:"title"`
	Body             string    `json:"body"`
	State            string    `json:"state"`
	IsDraft          bool      `json:"isDraft"`
	ReviewDecision   string    `json:"reviewDecision"`
	Mergeable        string    `json:"mergeable"`
	MergeStateStatus string    `json:"mergeStateStatus"`
	HeadRefName      string    `json:"headRefName"`
	BaseRefName      string    `json:"baseRefName"`
	URL              string    `json:"url"`
	Author           User      `json:"author"`
	Assignees        []User    `json:"assignees"`
	Labels           []Label   `json:"labels"`
	Comments         []Comment `json:"comments"`
	Reviews          []Review  `json:"reviews"`
	Files            []File    `json:"files"`
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ghCommandRunner struct{}

func (ghCommandRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Workflow struct {
	dir             string
	workspaceName   string
	runner          commandRunner
	PR              PullRequest
	Phase           Phase
	publishFix      func(context.Context, string) error
	publishConflict func(context.Context, string) error
	reviewFeedback  string
}

func List(ctx context.Context, workingDir string) ([]PullRequest, error) {
	return ListWithSearch(ctx, workingDir, "")
}

func ListWithSearch(ctx context.Context, workingDir, search string) ([]PullRequest, error) {
	return ListWithOptions(ctx, workingDir, PullRequestListOptions{State: "all", Search: search})
}

type PullRequestListOptions struct {
	State  string
	Search string
}

func ListWithOptions(ctx context.Context, workingDir string, options PullRequestListOptions) ([]PullRequest, error) {
	state := strings.ToLower(strings.TrimSpace(options.State))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, fmt.Errorf("invalid pull request state %q", options.State)
	}
	args := []string{"pr", "list", "--state", state, "--limit", "100", "--json", "number,title,state,isDraft,reviewDecision,mergeable,mergeStateStatus,headRefName,baseRefName,url,labels,assignees,author"}
	if strings.TrimSpace(options.Search) != "" {
		args = append(args, "--search", strings.TrimSpace(options.Search))
	}
	raw, err := ghCommandRunner{}.Run(ctx, workingDir, args...)
	if err != nil {
		return nil, err
	}
	var prs []PullRequest
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("decode pull request list: %w", err)
	}
	return prs, nil
}

func Load(ctx context.Context, workingDir string, number int, workspaceName string) (*Workflow, error) {
	return load(ctx, workingDir, number, workspaceName, ghCommandRunner{})
}

func load(ctx context.Context, workingDir string, number int, workspaceName string, runner commandRunner) (*Workflow, error) {
	if number < 1 {
		return nil, errors.New("pull request number must be greater than zero")
	}
	raw, err := runner.Run(ctx, workingDir, "pr", "view", strconv.Itoa(number), "--json", "number,title,body,state,isDraft,reviewDecision,mergeable,mergeStateStatus,headRefName,baseRefName,url,author,labels,assignees,comments,reviews,files")
	if err != nil {
		return nil, err
	}
	var pr PullRequest
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("decode pull request #%d: %w", number, err)
	}
	if isClosed(pr.State) {
		return nil, fmt.Errorf("PR #%dは%sです", number, strings.ToUpper(pr.State))
	}
	phase, err := loadPhase(pr, workingDir)
	if err != nil {
		return nil, err
	}
	return &Workflow{dir: workingDir, workspaceName: workspaceName, runner: runner, PR: pr, Phase: phase}, nil
}

func (w *Workflow) SetFixPublisher(publisher func(context.Context, string) error) {
	w.publishFix = publisher
}

func (w *Workflow) SetConflictPublisher(publisher func(context.Context, string) error) {
	w.publishConflict = publisher
}

func (w *Workflow) Prompt() string {
	if w.Phase == PhaseReviewApproved {
		return ""
	}
	if w.Phase == PhaseConflict {
		return w.ConflictPrompt("")
	}
	if w.Phase == PhaseFix {
		return w.FixPrompt("")
	}
	return strings.Join([]string{
		"以下のGitHub Pull Requestをレビューしてください。",
		"リポジトリのreview-pull-requestスキルに従い、差分、関連Issue、テスト結果を確認してください。",
		"", "Pull Request情報:", w.Context(),
	}, "\n")
}

func (w *Workflow) ConflictPrompt(feedback string) string {
	lines := []string{
		"以下のGitHub Pull Requestのコンフリクトを解消してください。",
		"リポジトリのresolve-pr-conflictsスキルに従い、競合ファイル、関連PR、head/baseブランチと双方に対応するIssueの要件を確認してください。",
		"両方の変更意図を維持して競合を解消し、必要なテストを実行してください。",
	}
	if strings.TrimSpace(feedback) != "" {
		lines = append(lines, "", "追加指示:", strings.TrimSpace(feedback))
	}
	return strings.Join(append(lines, "", "Pull Request情報:", w.Context()), "\n")
}

func (w *Workflow) RevisionPrompt(feedback string) string {
	return strings.Join([]string{
		"以下の補足を反映し、GitHub Pull Requestを再レビューしてください。",
		"リポジトリのreview-pull-requestスキルに従ってください。",
		"", "補足:", strings.TrimSpace(feedback), "", "Pull Request情報:", w.Context(),
	}, "\n")
}

func (w *Workflow) FixPrompt(instruction string) string {
	lines := []string{
		"以下のレビュー修正指示を検討し、GitHub Pull Requestの実装を修正してください。",
		"リポジトリのreview-comment-fixスキルに従い、設計検討、実装、テストまで行ってください。",
	}
	if strings.TrimSpace(instruction) != "" {
		lines = append(lines, "", "追加のレビュー修正指示:", strings.TrimSpace(instruction))
	} else {
		lines = append(lines, "", "PRコメントに登録されているレビュー結果とレビュー修正指示を確認してください。")
	}
	if strings.TrimSpace(w.reviewFeedback) != "" {
		lines = append(lines, "", "取得済みのレビュー指摘・全コメント:", strings.TrimSpace(w.reviewFeedback))
	}
	return strings.Join(append(lines, "", "Pull Request情報:", w.Context()), "\n")
}

func (w *Workflow) Context() string {
	lines := []string{
		fmt.Sprintf("PR: #%d %s", w.PR.Number, strings.TrimSpace(w.PR.Title)),
		"URL: " + strings.TrimSpace(w.PR.URL),
		fmt.Sprintf("State: %s / Review: %s / Draft: %t", w.PR.State, w.PR.ReviewDecision, w.PR.IsDraft),
		fmt.Sprintf("Mergeable: %s / Merge state: %s", w.PR.Mergeable, w.PR.MergeStateStatus),
		fmt.Sprintf("Branch: %s -> %s", w.PR.HeadRefName, w.PR.BaseRefName),
		"Author: " + w.PR.Author.Login, "", "Body:", strings.TrimSpace(w.PR.Body),
	}
	if len(w.PR.Comments) > 0 {
		lines = append(lines, "", "Comments:")
		for _, comment := range w.PR.Comments {
			lines = append(lines, fmt.Sprintf("- %s: %s", comment.Author.Login, strings.TrimSpace(comment.Body)))
		}
	}
	if len(w.PR.Reviews) > 0 {
		lines = append(lines, "", "Reviews:")
		for _, review := range w.PR.Reviews {
			lines = append(lines, fmt.Sprintf("- %s [%s]: %s", review.Author.Login, review.State, strings.TrimSpace(review.Body)))
		}
	}
	if len(w.PR.Files) > 0 {
		lines = append(lines, "", "Files:")
		for _, file := range w.PR.Files {
			lines = append(lines, "- "+file.Path)
		}
	}
	return strings.Join(lines, "\n")
}

func (w *Workflow) Start(ctx context.Context) error {
	label := "state:review_running"
	if w.Phase == PhaseFix {
		label = "state:review_fix_implementation_running"
	} else if w.Phase == PhaseConflict {
		label = "state:pr_conflict_running"
	}
	return w.setState(ctx, label)
}

func (w *Workflow) Finish(ctx context.Context, runErr error) error {
	label := "state:review_failed"
	if runErr == nil {
		label = "state:review_ready"
		if w.Phase == PhaseFix {
			label = "state:review_fix_implementation_ready"
		} else if w.Phase == PhaseConflict {
			label = "state:pr_conflict_ready"
		}
	} else if w.Phase == PhaseFix {
		label = "state:review_fix_failed"
	} else if w.Phase == PhaseConflict {
		label = "state:pr_conflict_failed"
	}
	return w.setState(ctx, label)
}

func (w *Workflow) SetPhase(phase Phase) { w.Phase = phase }
func (w *Workflow) CurrentPhase() Phase  { return w.Phase }
func (w *Workflow) Number() int          { return w.PR.Number }
func (w *Workflow) URL() string          { return w.PR.URL }

func (w *Workflow) SaveReviewFeedback(ctx context.Context) (string, string, error) {
	repository, err := w.repository(ctx)
	if err != nil {
		return "", "", err
	}
	apiComments, err := fetchPaginated[apiComment](ctx, w.runner, w.dir, fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repository, w.PR.Number))
	if err != nil {
		return "", "", fmt.Errorf("fetch PR comments: %w", err)
	}
	apiReviews, err := fetchPaginated[apiReview](ctx, w.runner, w.dir, fmt.Sprintf("repos/%s/pulls/%d/reviews?per_page=100", repository, w.PR.Number))
	if err != nil {
		return "", "", fmt.Errorf("fetch PR reviews: %w", err)
	}
	inlineComments, err := fetchPaginated[InlineComment](ctx, w.runner, w.dir, fmt.Sprintf("repos/%s/pulls/%d/comments?per_page=100", repository, w.PR.Number))
	if err != nil {
		return "", "", fmt.Errorf("fetch PR inline comments: %w", err)
	}
	w.PR.Comments = make([]Comment, 0, len(apiComments))
	for _, comment := range apiComments {
		w.PR.Comments = append(w.PR.Comments, Comment{Author: comment.Author, Body: comment.Body})
	}
	w.PR.Reviews = make([]Review, 0, len(apiReviews))
	for _, review := range apiReviews {
		w.PR.Reviews = append(w.PR.Reviews, Review{Author: review.Author, Body: review.Body, State: review.State})
	}
	content := w.reviewFeedbackContent(inlineComments)
	w.reviewFeedback = content
	workspace := strings.TrimSpace(w.workspaceName)
	if workspace == "" {
		workspace = ".workspace"
	}
	path := filepath.Join(w.dir, workspace, "review_fix", fmt.Sprintf("%d_%s_レビュー指摘.md", w.PR.Number, sanitizePart(w.PR.Title)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("create review feedback directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", "", fmt.Errorf("write review feedback: %w", err)
	}
	relative, relErr := filepath.Rel(w.dir, path)
	if relErr != nil {
		return path, content, nil
	}
	return filepath.ToSlash(relative), content, nil
}

func (w *Workflow) repository(ctx context.Context) (string, error) {
	repository := repositoryFromPullRequestURL(w.PR.URL)
	if repository == "" {
		raw, err := w.runner.Run(ctx, w.dir, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
		if err != nil {
			return "", fmt.Errorf("resolve PR repository: %w", err)
		}
		repository = strings.TrimSpace(string(raw))
	}
	if repository == "" {
		return "", errors.New("PR repository is empty")
	}
	return repository, nil
}

func fetchPaginated[T any](ctx context.Context, runner commandRunner, dir, endpoint string) ([]T, error) {
	raw, err := runner.Run(ctx, dir, "api", "--paginate", endpoint)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values []T
	for {
		var page []T
		if err := decoder.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		values = append(values, page...)
	}
	return values, nil
}

func repositoryFromPullRequestURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || !strings.EqualFold(parts[2], "pull") {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func (w *Workflow) reviewFeedbackContent(inlineComments []InlineComment) string {
	lines := []string{fmt.Sprintf("# PR #%d %s レビュー指摘", w.PR.Number, strings.TrimSpace(w.PR.Title)), "", "## レビュー"}
	if len(w.PR.Reviews) == 0 {
		lines = append(lines, "なし")
	} else {
		for _, review := range w.PR.Reviews {
			lines = append(lines, fmt.Sprintf("- %s [%s]: %s", review.Author.Login, review.State, strings.TrimSpace(review.Body)))
		}
	}
	lines = append(lines, "", "## PRコメント")
	if len(w.PR.Comments) == 0 {
		lines = append(lines, "なし")
	} else {
		for _, comment := range w.PR.Comments {
			lines = append(lines, fmt.Sprintf("- %s: %s", comment.Author.Login, strings.TrimSpace(comment.Body)))
		}
	}
	lines = append(lines, "", "## 行単位レビューコメント")
	if len(inlineComments) == 0 {
		lines = append(lines, "なし")
	} else {
		for _, comment := range inlineComments {
			line := comment.Line
			if line == 0 {
				line = comment.StartLine
			}
			location := strings.TrimSpace(comment.Path)
			if line > 0 {
				location += ":" + strconv.Itoa(line)
			}
			lines = append(lines, fmt.Sprintf("- %s [%s]: %s", comment.Author.Login, location, strings.TrimSpace(comment.Body)))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func (w *Workflow) SaveResult(result string) (string, error) {
	path := w.resultArtifactPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create PR artifact directory: %w", err)
	}
	content := withTopLevelHeading(w.resultArtifactHeading(), result)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write PR artifact: %w", err)
	}
	relative, err := filepath.Rel(w.dir, path)
	if err != nil {
		return path, nil
	}
	return filepath.ToSlash(relative), nil
}

func (w *Workflow) resultArtifactPath() string {
	workspace := strings.TrimSpace(w.workspaceName)
	if workspace == "" {
		workspace = ".workspace"
	}
	subdir := "review"
	if w.Phase == PhaseFix {
		subdir = "review_fix_implementation"
	} else if w.Phase == PhaseConflict {
		subdir = "pr_conflict"
	}
	return filepath.Join(w.dir, workspace, subdir, fmt.Sprintf("%d_%s.md", w.PR.Number, sanitizePart(w.PR.Title)))
}

func (w *Workflow) resultArtifactHeading() string {
	switch w.Phase {
	case PhaseFix:
		return "レビュー指摘修正結果"
	case PhaseConflict:
		return "コンフリクト解消結果"
	default:
		return "レビュー結果"
	}
}

func (w *Workflow) readResultArtifact() (string, error) {
	path := w.resultArtifactPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read workspace artifact %s: %w", path, err)
	}
	return string(raw), nil
}

func (w *Workflow) ApproveReview(ctx context.Context, _ string) error {
	artifact, err := w.readResultArtifact()
	if err != nil {
		return err
	}
	if err := w.comment(ctx, withTopLevelHeading("レビュー結果", artifact)); err != nil {
		return err
	}
	return w.setState(ctx, "state:review_approved")
}

func (w *Workflow) RequestChanges(ctx context.Context, _ string, instruction string) error {
	artifact, err := w.readResultArtifact()
	if err != nil {
		return err
	}
	body := withTopLevelHeading("レビュー結果", artifact) + "\n\n## レビュー修正指示\n" + strings.TrimSpace(instruction)
	if err := w.comment(ctx, body); err != nil {
		return err
	}
	return w.setState(ctx, "state:pr_review_comment")
}

func (w *Workflow) ApproveFix(ctx context.Context, _ string) error {
	artifact, err := w.readResultArtifact()
	if err != nil {
		return err
	}
	if w.publishFix == nil {
		return errors.New("review fix publisher is not configured")
	}
	if err := w.publishFix(ctx, artifact); err != nil {
		return err
	}
	if err := w.comment(ctx, withTopLevelHeading("レビュー指摘修正結果", artifact)); err != nil {
		return err
	}
	return w.setState(ctx, "state:review_fixed")
}

func (w *Workflow) ApproveConflict(ctx context.Context, _ string) error {
	artifact, err := w.readResultArtifact()
	if err != nil {
		return err
	}
	if w.publishConflict == nil {
		return errors.New("conflict publisher is not configured")
	}
	if err := w.publishConflict(ctx, artifact); err != nil {
		return err
	}
	if err := w.comment(ctx, withTopLevelHeading("コンフリクト解消結果", artifact)); err != nil {
		return err
	}
	return w.setState(ctx, "state:pr_conflict_resolved")
}

func HasConflict(pr PullRequest) bool {
	return strings.EqualFold(strings.TrimSpace(pr.Mergeable), "CONFLICTING") ||
		strings.EqualFold(strings.TrimSpace(pr.MergeStateStatus), "DIRTY")
}

func PullRequestHasLabel(pr PullRequest, target string) bool {
	for _, label := range pr.Labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), target) {
			return true
		}
	}
	return false
}

func pullRequestPhase(pr PullRequest) Phase {
	for _, label := range pr.Labels {
		switch strings.ToLower(strings.TrimSpace(label.Name)) {
		case "state:review_failed", "state:failed":
			return PhaseReviewFailed
		case "state:review_fix_failed":
			return PhaseFixFailed
		case "state:pr_conflict_failed":
			return PhaseConflictFailed
		}
	}
	if HasConflict(pr) || PullRequestHasLabel(pr, "state:pr_conflict") {
		return PhaseConflict
	}
	fixLabels := map[string]struct{}{
		"state:pr_review_comment":                  {},
		"state:review_fix_design_running":          {},
		"state:review_fix_design_ready":            {},
		"state:review_fix_design_approved":         {},
		"state:review_fix_implementation_running":  {},
		"state:review_fix_implementation_ready":    {},
		"state:review_fix_implementation_approved": {},
	}
	for _, label := range pr.Labels {
		if _, ok := fixLabels[strings.ToLower(strings.TrimSpace(label.Name))]; ok {
			return PhaseFix
		}
	}
	for _, label := range pr.Labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), "state:review_approved") {
			return PhaseReviewApproved
		}
	}
	return PhaseReview
}

func (w *Workflow) CompleteIfClosed(ctx context.Context) (bool, string, error) {
	raw, err := w.runner.Run(ctx, w.dir, "pr", "view", strconv.Itoa(w.PR.Number), "--json", "state", "--jq", ".state")
	if err != nil {
		return false, "", err
	}
	state := strings.ToUpper(strings.TrimSpace(string(raw)))
	if state != "CLOSED" && state != "MERGED" {
		return false, state, nil
	}
	if err := w.setState(ctx, "state:completed"); err != nil {
		return false, state, err
	}
	return true, state, nil
}

func (w *Workflow) comment(ctx context.Context, body string) error {
	_, err := w.runner.Run(ctx, w.dir, "pr", "comment", strconv.Itoa(w.PR.Number), "--body", strings.TrimSpace(body))
	if err != nil {
		return fmt.Errorf("post PR comment: %w", err)
	}
	return nil
}

func loadPhase(pr PullRequest, workingDir string) (Phase, error) {
	key := pullRequestStateKey(pr, workingDir)
	state, found, err := workflowstate.Get(key)
	if err != nil {
		return "", err
	}
	if found {
		return pullRequestPhase(PullRequest{Labels: []Label{{Name: state}}, Mergeable: pr.Mergeable, MergeStateStatus: pr.MergeStateStatus}), nil
	}
	phase := pullRequestPhase(pr)
	if err := workflowstate.Set(key, pullRequestStateForPhase(phase)); err != nil {
		return "", err
	}
	return phase, nil
}

func pullRequestStateForPhase(phase Phase) string {
	switch phase {
	case PhaseReviewApproved:
		return "state:review_approved"
	case PhaseFix:
		return "state:pr_review_comment"
	case PhaseConflict:
		return "state:pr_conflict"
	case PhaseReviewFailed:
		return "state:review_failed"
	case PhaseFixFailed:
		return "state:review_fix_failed"
	case PhaseConflictFailed:
		return "state:pr_conflict_failed"
	default:
		return "state:review_running"
	}
}

func (w *Workflow) setState(_ context.Context, target string) error {
	return workflowstate.Set(pullRequestStateKey(w.PR, w.dir), target)
}

func pullRequestStateKey(pr PullRequest, workingDir string) workflowstate.Key {
	return workflowstate.Key{Repository: repositoryID(pr.URL, workingDir), Kind: "pull_request", Number: pr.Number}
}

func repositoryID(resourceURL, workingDir string) string {
	parsed, err := url.Parse(strings.TrimSpace(resourceURL))
	if err == nil && strings.EqualFold(parsed.Host, "github.com") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return "github.com/" + parts[0] + "/" + parts[1]
		}
	}
	abs, err := filepath.Abs(workingDir)
	if err == nil {
		return "file://" + filepath.ToSlash(abs)
	}
	return "file://" + filepath.ToSlash(workingDir)
}

func isClosed(state string) bool {
	state = strings.ToUpper(strings.TrimSpace(state))
	return state == "CLOSED" || state == "MERGED"
}

func sanitizePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "#", "-", ".", "-", ",", "-", "(", "-", ")", "-")
	return strings.Trim(replacer.Replace(value), "-")
}

func stripLeadingH1(content string) string {
	trimmed := strings.TrimSpace(content)
	lines := strings.Split(trimmed, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		return strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return trimmed
}

func withTopLevelHeading(heading, content string) string {
	return strings.Join([]string{"# " + strings.TrimSpace(heading), "", stripLeadingH1(content)}, "\n")
}
