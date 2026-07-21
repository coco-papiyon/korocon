package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	PhaseDesign               Phase = "design"
	PhaseImplementation       Phase = "implementation"
	PhaseDesignReady          Phase = "design_ready"
	PhaseImplementationReady  Phase = "implementation_ready"
	PhaseDesignFailed         Phase = "design_failed"
	PhaseImplementationFailed Phase = "implementation_failed"
	PhaseFailed               Phase = "failed"
)

const (
	labelDesignRunning          = "state:design_running"
	labelDesignReady            = "state:design_ready"
	labelDesignApproved         = "state:design_approved"
	labelImplementationRunning  = "state:implementation_running"
	labelImplementationReady    = "state:implementation_ready"
	labelImplementationApproved = "state:implementation_approved"
	labelPRCreated              = "state:pr_created"
	labelDesignFailed           = "state:design_failed"
	labelImplementationFailed   = "state:implementation_failed"
)

var stateLabels = map[string]struct{}{
	"state:detected": {}, "state:design_running": {}, "state:design_ready": {},
	"state:design_approved": {}, "state:implementation_running": {},
	"state:implementation_ready": {}, "state:implementation_approved": {},
	"state:pr_created": {}, "state:pr_review_comment": {}, "state:pr_conflict": {},
	"state:pr_conflict_running": {}, "state:pr_conflict_ready": {},
	"state:pr_conflict_resolved": {}, "state:review_fix_design_running": {},
	"state:review_fix_design_ready": {}, "state:review_fix_design_approved": {},
	"state:review_fix_implementation_running":  {},
	"state:review_fix_implementation_ready":    {},
	"state:review_fix_implementation_approved": {}, "state:review_fixed": {},
	"state:review_running": {}, "state:review_ready": {},
	"state:review_approved": {}, "state:completed": {}, "state:failed": {},
	"state:design_failed": {}, "state:implementation_failed": {},
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

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []Label   `json:"labels"`
	Comments  []Comment `json:"comments"`
	URL       string    `json:"url"`
	Author    User      `json:"author"`
	Assignees []User    `json:"assignees"`
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ghCommandRunner struct{}

var runGitSyncCommand = func(ctx context.Context, dir string, args ...string) error {
	command := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", command...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ghCommandRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Workflow manages one selected issue through the same issue state labels as
// korobokcle. The AI-specific procedure remains owned by repository skills.
type Workflow struct {
	dir                   string
	runner                commandRunner
	Issue                 Issue
	Phase                 Phase
	workspaceName         string
	pendingResult         string
	publishImplementation func(context.Context, string) (string, error)
}

func Load(ctx context.Context, workingDir string, number int, workspaceName string) (*Workflow, error) {
	return load(ctx, workingDir, number, workspaceName, ghCommandRunner{})
}

func List(ctx context.Context, workingDir string) ([]Issue, error) {
	return ListWithSearch(ctx, workingDir, "")
}

func ListWithSearch(ctx context.Context, workingDir, search string) ([]Issue, error) {
	return ListWithOptions(ctx, workingDir, IssueListOptions{State: "open", Search: search})
}

type IssueListOptions struct {
	State  string
	Search string
}

func ListWithOptions(ctx context.Context, workingDir string, options IssueListOptions) ([]Issue, error) {
	state := strings.ToLower(strings.TrimSpace(options.State))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, fmt.Errorf("invalid issue state %q", options.State)
	}
	args := []string{"issue", "list", "--state", state, "--limit", "100", "--json", "number,title,body,state,labels,comments,url,author,assignees"}
	if strings.TrimSpace(options.Search) != "" {
		args = append(args, "--search", strings.TrimSpace(options.Search))
	}
	raw, err := ghCommandRunner{}.Run(ctx, workingDir, args...)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return nil, fmt.Errorf("decode issue list: %w", err)
	}
	return issues, nil
}

func load(ctx context.Context, workingDir string, number int, workspaceName string, runner commandRunner) (*Workflow, error) {
	if number < 1 {
		return nil, errors.New("issue number must be greater than zero")
	}
	raw, err := runner.Run(ctx, workingDir, "issue", "view", strconv.Itoa(number), "--json", "number,title,body,state,labels,comments,url,author,assignees")
	if err != nil {
		return nil, err
	}
	var selected Issue
	if err := json.Unmarshal(raw, &selected); err != nil {
		return nil, fmt.Errorf("decode issue #%d: %w", number, err)
	}
	if state := strings.TrimSpace(selected.State); state != "" && !strings.EqualFold(state, "open") {
		return nil, fmt.Errorf("Issue #%dはopenではありません (state: %s)", number, state)
	}
	phase, err := loadPhase(selected, workingDir)
	if err != nil {
		return nil, err
	}
	workflow := &Workflow{dir: workingDir, runner: runner, Issue: selected, Phase: phase, workspaceName: workspaceName}
	if phase == PhaseFailed {
		implementationDir := filepath.Join(workingDir, workspaceName, "implementation", strconv.Itoa(number))
		if _, statErr := os.Stat(implementationDir); statErr == nil {
			workflow.Phase = PhaseImplementationFailed
		} else {
			workflow.Phase = PhaseDesignFailed
		}
	}
	if phase == PhaseDesignReady || phase == PhaseImplementationReady {
		resultPath, pathErr := workflow.artifactPath()
		if pathErr != nil {
			return nil, pathErr
		}
		result, readErr := os.ReadFile(resultPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("read approval result: %w", readErr)
		}
		if readErr == nil {
			workflow.pendingResult = string(result)
		}
	}
	return workflow, nil
}

func classify(labels []Label) (Phase, error) {
	has := func(target string) bool {
		for _, label := range labels {
			if strings.EqualFold(strings.TrimSpace(label.Name), target) {
				return true
			}
		}
		return false
	}
	switch {
	case has("state:implementation_failed"):
		return PhaseImplementationFailed, nil
	case has("state:design_failed"):
		return PhaseDesignFailed, nil
	case has("state:failed"):
		return PhaseFailed, nil
	case has("state:implementation_ready"):
		return PhaseImplementationReady, nil
	case has("state:implementation_approved"), has("state:pr_created"):
		return "", errors.New("issueの実装工程は完了しています")
	case has(labelDesignReady):
		return PhaseDesignReady, nil
	case has(labelDesignApproved), has(labelImplementationRunning):
		return PhaseImplementation, nil
	default:
		return PhaseDesign, nil
	}
}

func (w *Workflow) Prompt() string {
	action := "設計"
	if w.Phase == PhaseImplementation {
		action = "実装"
	}
	return strings.Join([]string{
		fmt.Sprintf("以下のGitHub Issueの%sを行ってください。", action),
		"具体的な手順と成果物の形式は、リポジトリで利用可能なスキルの指示に従ってください。",
		"",
		"Issue情報:",
		w.Context(),
	}, "\n")
}

func (w *Workflow) IssueNumber() int {
	return w.Issue.Number
}

func (w *Workflow) SetPhase(phase Phase) {
	w.Phase = phase
}

func (w *Workflow) PendingApprovalResult() string { return w.pendingResult }

func (w *Workflow) SetImplementationPublisher(publisher func(context.Context, string) (string, error)) {
	w.publishImplementation = publisher
}

func SyncRepository(ctx context.Context, dir string) error {
	if err := runGitSyncCommand(ctx, dir, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("fetch repository: %w", err)
	}
	if err := runGitSyncCommand(ctx, dir, "pull", "--ff-only"); err != nil {
		return fmt.Errorf("pull repository: %w", err)
	}
	return nil
}

func (w *Workflow) RevisionPrompt(feedback string) string {
	action := "再設計"
	if w.Phase == PhaseImplementation || w.Phase == PhaseImplementationReady {
		action = "再実装"
	}
	return strings.Join([]string{
		fmt.Sprintf("以下のフィードバックを反映し、GitHub Issueの%sを行ってください。", action),
		"具体的な手順と成果物の形式は、リポジトリで利用可能なスキルの指示に従ってください。",
		"",
		"フィードバック:",
		strings.TrimSpace(feedback),
		"",
		"Issue情報:",
		w.Context(),
	}, "\n")
}

func (w *Workflow) Context() string {
	lines := []string{
		fmt.Sprintf("Issue: #%d %s", w.Issue.Number, strings.TrimSpace(w.Issue.Title)),
		"URL: " + strings.TrimSpace(w.Issue.URL),
		"Author: " + strings.TrimSpace(w.Issue.Author.Login),
		"",
		"Body:",
		strings.TrimSpace(w.Issue.Body),
	}
	if len(w.Issue.Labels) > 0 {
		labels := make([]string, 0, len(w.Issue.Labels))
		for _, label := range w.Issue.Labels {
			labels = append(labels, label.Name)
		}
		lines = append(lines, "", "Labels:", strings.Join(labels, ", "))
	}
	if len(w.Issue.Comments) > 0 {
		lines = append(lines, "", "Comments:")
		for _, comment := range w.Issue.Comments {
			lines = append(lines, fmt.Sprintf("- %s: %s", comment.Author.Login, strings.TrimSpace(comment.Body)))
		}
	}
	return strings.Join(lines, "\n")
}

func (w *Workflow) Start(ctx context.Context) error {
	label := labelDesignRunning
	if w.Phase == PhaseImplementation || w.Phase == PhaseImplementationReady {
		label = labelImplementationRunning
	}
	return w.setState(ctx, label)
}

func (w *Workflow) Finish(ctx context.Context, runErr error) error {
	label := labelDesignFailed
	if runErr == nil {
		label = labelDesignReady
		if w.Phase == PhaseImplementation {
			label = labelImplementationReady
		}
	} else if w.Phase == PhaseImplementation {
		label = labelImplementationFailed
	}
	return w.setState(ctx, label)
}

// SaveResult persists the reviewable artifact using korobokcle's workspace
// subdirectories, file-name normalization, and top-level heading format.
func (w *Workflow) SaveResult(result string) (string, error) {
	path, err := w.artifactPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create artifact directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(w.artifactContent(result)), 0o644); err != nil {
		return "", fmt.Errorf("write artifact: %w", err)
	}
	relative, err := filepath.Rel(w.dir, path)
	if err != nil {
		return path, nil
	}
	return filepath.ToSlash(relative), nil
}

func (w *Workflow) Approve(ctx context.Context, _ string) (string, error) {
	artifact, err := w.readArtifact()
	if err != nil {
		return "", err
	}
	if w.Phase == PhaseImplementation || w.Phase == PhaseImplementationReady || w.Phase == PhaseImplementationFailed {
		if w.publishImplementation == nil {
			return "", errors.New("implementation publisher is not configured")
		}
		url, err := w.publishImplementation(ctx, artifact)
		if err != nil {
			return "", err
		}
		if err := w.setState(ctx, labelPRCreated); err != nil {
			return "", fmt.Errorf("mark pull request created: %w", err)
		}
		return url, nil
	}
	if w.Phase == PhaseDesign {
		body := withTopLevelHeading("設計結果", artifact)
		if _, err := w.runner.Run(ctx, w.dir, "issue", "comment", strconv.Itoa(w.Issue.Number), "--body", body); err != nil {
			return "", fmt.Errorf("post design result: %w", err)
		}
	}
	return "", w.setState(ctx, labelDesignApproved)
}

func (w *Workflow) readArtifact() (string, error) {
	path, err := w.artifactPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read workspace artifact %s: %w", path, err)
	}
	return string(raw), nil
}

func (w *Workflow) artifactPath() (string, error) {
	workspaceName := strings.TrimSpace(w.workspaceName)
	if workspaceName == "" {
		workspaceName = ".workspace"
	}
	if filepath.IsAbs(workspaceName) || strings.ContainsAny(workspaceName, `/\`) || workspaceName == "." || workspaceName == ".." {
		return "", errors.New("workspace name must be a directory name")
	}
	subdir := "design"
	if w.Phase == PhaseImplementation || w.Phase == PhaseImplementationReady {
		subdir = "implementation"
	}
	name := fmt.Sprintf("%d_%s.md", w.Issue.Number, sanitizePart(w.Issue.Title))
	return filepath.Join(w.dir, workspaceName, subdir, name), nil
}

func (w *Workflow) artifactContent(result string) string {
	return withTopLevelHeading(w.artifactHeading(), result)
}

func (w *Workflow) artifactHeading() string {
	if w.Phase == PhaseImplementation || w.Phase == PhaseImplementationReady || w.Phase == PhaseImplementationFailed {
		return "実装結果"
	}
	return "設計結果"
}

func withTopLevelHeading(heading, content string) string {
	return strings.Join([]string{"# " + strings.TrimSpace(heading), "", stripLeadingH1(content)}, "\n")
}

func stripLeadingH1(content string) string {
	trimmed := strings.TrimSpace(content)
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}

func sanitizePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "#", "-", ".", "-", ",", "-", "(", "-", ")", "-")
	value = replacer.Replace(value)
	return strings.Trim(value, "-")
}

func loadPhase(issue Issue, workingDir string) (Phase, error) {
	key := issueStateKey(issue, workingDir)
	state, found, err := workflowstate.Get(key)
	if err != nil {
		return "", err
	}
	if found {
		return classify([]Label{{Name: state}})
	}
	phase, err := classify(issue.Labels)
	if err != nil {
		return "", err
	}
	if err := workflowstate.Set(key, issueStateForPhase(phase)); err != nil {
		return "", err
	}
	return phase, nil
}

func issueStateForPhase(phase Phase) string {
	switch phase {
	case PhaseDesignReady:
		return labelDesignReady
	case PhaseImplementation:
		return labelImplementationRunning
	case PhaseImplementationReady:
		return labelImplementationReady
	case PhaseDesignFailed:
		return labelDesignFailed
	case PhaseImplementationFailed:
		return labelImplementationFailed
	case PhaseFailed:
		return "state:failed"
	default:
		return labelDesignRunning
	}
}

func (w *Workflow) setState(_ context.Context, target string) error {
	return workflowstate.Set(issueStateKey(w.Issue, w.dir), target)
}

func issueStateKey(issue Issue, workingDir string) workflowstate.Key {
	return workflowstate.Key{Repository: repositoryID(issue.URL, workingDir), Kind: "issue", Number: issue.Number}
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
