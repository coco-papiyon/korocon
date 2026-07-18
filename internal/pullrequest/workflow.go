package pullrequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Phase string

const (
	PhaseReview       Phase = "review"
	PhaseFix          Phase = "review_fix_implementation"
	PhaseVerification Phase = "verification"
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

type PullRequest struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	State          string    `json:"state"`
	IsDraft        bool      `json:"isDraft"`
	ReviewDecision string    `json:"reviewDecision"`
	HeadRefName    string    `json:"headRefName"`
	BaseRefName    string    `json:"baseRefName"`
	URL            string    `json:"url"`
	Author         User      `json:"author"`
	Labels         []Label   `json:"labels"`
	Comments       []Comment `json:"comments"`
	Reviews        []Review  `json:"reviews"`
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
	dir           string
	workspaceName string
	runner        commandRunner
	PR            PullRequest
	Phase         Phase
	publishFix    func(context.Context, string) error
}

func List(ctx context.Context, workingDir string) ([]PullRequest, error) {
	raw, err := ghCommandRunner{}.Run(ctx, workingDir, "pr", "list", "--state", "all", "--limit", "100", "--json", "number,title,state,isDraft,reviewDecision,headRefName,baseRefName,url")
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
	raw, err := runner.Run(ctx, workingDir, "pr", "view", strconv.Itoa(number), "--json", "number,title,body,state,isDraft,reviewDecision,headRefName,baseRefName,url,author,labels,comments,reviews")
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
	return &Workflow{dir: workingDir, workspaceName: workspaceName, runner: runner, PR: pr, Phase: PhaseReview}, nil
}

func (w *Workflow) SetFixPublisher(publisher func(context.Context, string) error) {
	w.publishFix = publisher
}

func (w *Workflow) Prompt() string {
	return strings.Join([]string{
		"以下のGitHub Pull Requestをレビューしてください。",
		"リポジトリのreview-pull-requestスキルに従い、差分、関連Issue、テスト結果を確認してください。",
		"", "Pull Request情報:", w.Context(),
	}, "\n")
}

func (w *Workflow) RevisionPrompt(feedback string) string {
	return strings.Join([]string{
		"以下の補足を反映し、GitHub Pull Requestを再レビューしてください。",
		"リポジトリのreview-pull-requestスキルに従ってください。",
		"", "補足:", strings.TrimSpace(feedback), "", "Pull Request情報:", w.Context(),
	}, "\n")
}

func (w *Workflow) FixPrompt(instruction string) string {
	return strings.Join([]string{
		"以下のレビュー修正指示を検討し、GitHub Pull Requestの実装を修正してください。",
		"リポジトリのreview-comment-fixスキルに従い、設計検討、実装、テストまで行ってください。",
		"", "レビュー修正指示:", strings.TrimSpace(instruction), "", "Pull Request情報:", w.Context(),
	}, "\n")
}

func (w *Workflow) Context() string {
	lines := []string{
		fmt.Sprintf("PR: #%d %s", w.PR.Number, strings.TrimSpace(w.PR.Title)),
		"URL: " + strings.TrimSpace(w.PR.URL),
		fmt.Sprintf("State: %s / Review: %s / Draft: %t", w.PR.State, w.PR.ReviewDecision, w.PR.IsDraft),
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
	return strings.Join(lines, "\n")
}

func (w *Workflow) Start(ctx context.Context) error {
	label := "state:review_running"
	if w.Phase == PhaseFix {
		label = "state:review_fix_implementation_running"
	}
	return w.setStateLabel(ctx, label)
}

func (w *Workflow) Finish(ctx context.Context, runErr error) error {
	label := "state:failed"
	if runErr == nil {
		label = "state:review_ready"
		if w.Phase == PhaseFix {
			label = "state:review_fix_implementation_ready"
		}
	}
	return w.setStateLabel(ctx, label)
}

func (w *Workflow) SetPhase(phase Phase) { w.Phase = phase }
func (w *Workflow) CurrentPhase() Phase  { return w.Phase }
func (w *Workflow) Number() int          { return w.PR.Number }

func (w *Workflow) SaveResult(result string) (string, error) {
	workspace := strings.TrimSpace(w.workspaceName)
	if workspace == "" {
		workspace = ".workspace"
	}
	subdir := "review"
	if w.Phase == PhaseFix {
		subdir = "review_fix_implementation"
	}
	path := filepath.Join(w.dir, workspace, subdir, fmt.Sprintf("%d_%s.md", w.PR.Number, sanitizePart(w.PR.Title)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create PR artifact directory: %w", err)
	}
	content := fmt.Sprintf("# %s\n\n%s", strings.TrimSpace(w.PR.Title), stripLeadingH1(result))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write PR artifact: %w", err)
	}
	relative, err := filepath.Rel(w.dir, path)
	if err != nil {
		return path, nil
	}
	return filepath.ToSlash(relative), nil
}

func (w *Workflow) ApproveReview(ctx context.Context, result string) error {
	if err := w.comment(ctx, result); err != nil {
		return err
	}
	return w.setStateLabel(ctx, "state:review_approved")
}

func (w *Workflow) RequestChanges(ctx context.Context, result, instruction string) error {
	body := strings.TrimSpace(result) + "\n\n## レビュー修正指示\n" + strings.TrimSpace(instruction)
	if err := w.comment(ctx, body); err != nil {
		return err
	}
	return w.setStateLabel(ctx, "state:pr_review_comment")
}

func (w *Workflow) ApproveFix(ctx context.Context, result string) error {
	if w.publishFix == nil {
		return errors.New("review fix publisher is not configured")
	}
	if err := w.publishFix(ctx, result); err != nil {
		return err
	}
	if err := w.comment(ctx, result); err != nil {
		return err
	}
	return w.setStateLabel(ctx, "state:review_fixed")
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
	if err := w.setStateLabel(ctx, "state:completed"); err != nil {
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

func (w *Workflow) setStateLabel(ctx context.Context, target string) error {
	if _, err := w.runner.Run(ctx, w.dir, "label", "create", target, "--color", "0E8A16", "--description", "korobokcle state label", "--force"); err != nil {
		return fmt.Errorf("ensure label %s: %w", target, err)
	}
	raw, err := w.runner.Run(ctx, w.dir, "pr", "view", strconv.Itoa(w.PR.Number), "--json", "labels")
	if err != nil {
		return err
	}
	var current struct {
		Labels []Label `json:"labels"`
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return fmt.Errorf("decode PR labels: %w", err)
	}
	args := []string{"pr", "edit", strconv.Itoa(w.PR.Number), "--add-label", target}
	for _, label := range current.Labels {
		name := strings.TrimSpace(label.Name)
		if strings.EqualFold(name, target) {
			continue
		}
		if _, ok := stateLabels[strings.ToLower(name)]; ok {
			args = append(args, "--remove-label", name)
		}
	}
	if _, err := w.runner.Run(ctx, w.dir, args...); err != nil {
		return fmt.Errorf("update PR state label: %w", err)
	}
	return nil
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
