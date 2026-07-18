package implementation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/coco-papiyon/korocon/internal/runner"
)

type Config struct {
	Provider                string
	Binary                  string
	VerifierProvider        string
	VerifierBinary          string
	VerifierModel           string
	RepositoryDir           string
	WorkspaceName           string
	ImplementationDirectory string
	BranchNamePattern       string
	BaseBranch              string
	LoopCount               int
	IssueNumber             int
	IssueTitle              string
	IssueContext            string
	LogOut                  io.Writer
	LogErr                  io.Writer
}

var startImplementationSession = func(ctx context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
	return runner.StartAgentSession(ctx, cfg)
}

var runGHCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Engine struct {
	cfg         Config
	mu          sync.Mutex
	implementer runner.AgentSession
	verifier    runner.AgentSession
	worktree    string
	branch      string
}

type verification struct {
	Status   string `json:"status"`
	Feedback string `json:"feedback"`
	Summary  string `json:"summary"`
}

func New(cfg Config) *Engine {
	if strings.TrimSpace(cfg.WorkspaceName) == "" {
		cfg.WorkspaceName = ".workspace"
	}
	if strings.TrimSpace(cfg.ImplementationDirectory) == "" {
		cfg.ImplementationDirectory = "../<リポジトリ名>-branches/"
	}
	if strings.TrimSpace(cfg.BranchNamePattern) == "" {
		cfg.BranchNamePattern = "issue_#<issue番号>"
	}
	if strings.TrimSpace(cfg.BaseBranch) == "" {
		cfg.BaseBranch = "main"
	}
	if cfg.LoopCount <= 0 {
		cfg.LoopCount = 3
	}
	if cfg.LoopCount > 10 {
		cfg.LoopCount = 10
	}
	return &Engine{cfg: cfg}
}

func (e *Engine) Run(ctx context.Context, workflowPrompt, model string, handleRequest runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureStarted(ctx, model, handleRequest); err != nil {
		return runner.TurnResult{}, err
	}
	design, err := os.ReadFile(e.designArtifactPath())
	if err != nil {
		return runner.TurnResult{}, fmt.Errorf("read design artifact: %w", err)
	}

	feedback := ""
	tokens := 0
	for attempt := 1; attempt <= e.cfg.LoopCount; attempt++ {
		if setPhase != nil {
			setPhase(fmt.Sprintf("実装%d回目", attempt))
		}
		implementationPrompt := e.implementationPrompt(workflowPrompt, string(design), feedback, attempt)
		implemented, err := e.implementer.RunTurn(ctx, implementationPrompt, model, onEvent)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("implementation attempt %d: %w", attempt, err)
		}
		if err := e.saveAttemptResult(attempt, false, implemented.Text); err != nil {
			return runner.TurnResult{}, fmt.Errorf("save implementation attempt %d: %w", attempt, err)
		}
		tokens += implemented.Tokens

		if setPhase != nil {
			setPhase(fmt.Sprintf("検証%d回目", attempt))
		}
		checked, err := e.verifier.RunTurn(ctx, e.verificationPrompt(string(design), implemented.Text, attempt), e.verifierModel(model), onEvent)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("verification attempt %d: %w", attempt, err)
		}
		if err := e.saveAttemptResult(attempt, true, checked.Text); err != nil {
			return runner.TurnResult{}, fmt.Errorf("save verification attempt %d: %w", attempt, err)
		}
		tokens += checked.Tokens
		verdict, err := parseVerification(checked.Text)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("verification attempt %d: %w", attempt, err)
		}
		if verdict.Status == "passed" {
			text := strings.TrimSpace(implemented.Text)
			if strings.TrimSpace(verdict.Summary) != "" {
				text += "\n\n## 検証者エージェントの判定\n" + strings.TrimSpace(verdict.Summary)
			}
			return runner.TurnResult{Text: text, Tokens: tokens}, nil
		}
		feedback = strings.TrimSpace(verdict.Feedback)
		if feedback == "" {
			feedback = strings.TrimSpace(verdict.Summary)
		}
	}
	return runner.TurnResult{}, fmt.Errorf("implementation verification did not pass after %d attempts: %s", e.cfg.LoopCount, feedback)
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var err error
	if e.implementer != nil {
		err = errors.Join(err, e.implementer.Close())
		e.implementer = nil
	}
	if e.verifier != nil {
		err = errors.Join(err, e.verifier.Close())
		e.verifier = nil
	}
	return err
}

func (e *Engine) Publish(ctx context.Context, result string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.TrimSpace(e.worktree) == "" || strings.TrimSpace(e.branch) == "" {
		return "", errors.New("implementation worktree is not initialized")
	}
	message := fmt.Sprintf("feat: implement #%d %s", e.cfg.IssueNumber, strings.TrimSpace(e.cfg.IssueTitle))
	if err := stageAndCommitIfNeeded(ctx, e.worktree, message); err != nil {
		return "", fmt.Errorf("commit implementation: %w", err)
	}
	if err := ensureBranchHasCommit(ctx, e.worktree, e.cfg.BaseBranch, e.branch); err != nil {
		return "", fmt.Errorf("prepare implementation branch: %w", err)
	}
	if err := publishBranch(ctx, e.worktree, e.branch); err != nil {
		return "", fmt.Errorf("publish implementation branch: %w", err)
	}
	if url, ok := e.existingPullRequest(ctx); ok {
		return url, nil
	}
	artifact := result
	if saved, err := os.ReadFile(e.implementationArtifactPath()); err == nil {
		artifact = string(saved)
	}
	body := buildPullRequestBody(e.cfg.IssueNumber, artifact)
	out, err := runGHCommand(ctx, e.worktree,
		"pr", "create", "--base", e.cfg.BaseBranch, "--title", e.cfg.IssueTitle,
		"--body", body, "--head", e.branch,
	)
	if err != nil {
		return "", fmt.Errorf("create pull request: %w", err)
	}
	url := pullRequestURL(string(out))
	if url == "" {
		return "", errors.New("create pull request returned an empty URL")
	}
	return url, nil
}

func (e *Engine) existingPullRequest(ctx context.Context) (string, bool) {
	out, err := runGHCommand(ctx, e.worktree, "pr", "view", e.branch, "--json", "url", "--jq", ".url")
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(string(out))
	return url, url != ""
}

func stageAndCommitIfNeeded(ctx context.Context, dir, message string) error {
	out, err := runGitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if _, err := runGitOutput(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	_, err = runGitOutput(ctx, dir, "commit", "-m", message)
	return err
}

func ensureBranchHasCommit(ctx context.Context, dir, baseBranch, branch string) error {
	out, err := runGitOutput(ctx, dir, "rev-list", "--count", baseBranch+".."+branch)
	if err != nil {
		return err
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return fmt.Errorf("parse branch commit count: %w", err)
	}
	if count > 0 {
		return nil
	}
	_, err = runGitOutput(ctx, dir, "commit", "--allow-empty", "-m", "chore: prepare PR for "+branch)
	return err
}

func publishBranch(ctx context.Context, dir, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-remote", "--exit-code", "--heads", "origin", branch)
	out, err := cmd.CombinedOutput()
	if err == nil {
		if _, err := runGitOutput(ctx, dir, "pull", "--rebase", "origin", branch); err != nil {
			return fmt.Errorf("rebase remote branch before push: %w", err)
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			return fmt.Errorf("git ls-remote --heads origin %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
		}
	}
	_, err = runGitOutput(ctx, dir, "push", "-u", "origin", branch+":"+branch)
	return err
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	command := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", command...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func buildPullRequestBody(issueNumber int, result string) string {
	artifact := strings.TrimSpace(result)
	if lines := strings.Split(artifact, "\n"); len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		artifact = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return strings.Join([]string{"# 実装結果", "", artifact, "", "Closes #" + strconv.Itoa(issueNumber)}, "\n")
}

func pullRequestURL(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "http://") {
			return line
		}
	}
	return ""
}

func (e *Engine) ensureStarted(ctx context.Context, model string, handleRequest runner.ServerRequestHandler) error {
	if e.implementer != nil && e.verifier != nil {
		return nil
	}
	worktree, branch, err := e.ensureWorktree(ctx)
	if err != nil {
		return err
	}
	common := runner.SessionConfig{
		Provider: e.cfg.Provider, Binary: e.cfg.Binary, Model: model, WorkingDir: worktree,
		LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handleRequest,
		ApprovalPolicy: "on-request",
	}
	common.Sandbox = "workspace-write"
	implementer, err := startImplementationSession(ctx, common)
	if err != nil {
		return fmt.Errorf("start implementation AI: %w", err)
	}
	common.Provider = e.verifierProvider()
	common.Binary = e.cfg.VerifierBinary
	if common.Binary == "" && common.Provider == e.implementerProvider() {
		common.Binary = e.cfg.Binary
	}
	common.Model = e.verifierModel(model)
	common.Sandbox = "read-only"
	verifier, err := startImplementationSession(ctx, common)
	if err != nil {
		_ = implementer.Close()
		return fmt.Errorf("start verification AI: %w", err)
	}
	e.implementer, e.verifier = implementer, verifier
	e.worktree, e.branch = worktree, branch
	return nil
}

func (e *Engine) implementerProvider() string {
	provider := strings.TrimSpace(e.cfg.Provider)
	if provider == "" {
		return "codex"
	}
	return provider
}

func (e *Engine) verifierProvider() string {
	provider := strings.TrimSpace(e.cfg.VerifierProvider)
	if provider == "" {
		return e.implementerProvider()
	}
	return provider
}

func (e *Engine) verifierModel(implementerModel string) string {
	model := strings.TrimSpace(e.cfg.VerifierModel)
	if model == "" {
		return implementerModel
	}
	return model
}

func (e *Engine) ensureWorktree(ctx context.Context) (string, string, error) {
	repositoryDir, err := filepath.Abs(e.cfg.RepositoryDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository directory: %w", err)
	}
	repositoryName := strings.TrimSuffix(filepath.Base(filepath.Clean(repositoryDir)), ".git")
	root := strings.NewReplacer(
		"<リポジトリ名>", repositoryName,
		"<repositoryName>", repositoryName,
	).Replace(e.cfg.ImplementationDirectory)
	if !filepath.IsAbs(root) {
		root = filepath.Join(repositoryDir, root)
	}
	worktree := filepath.Clean(filepath.Join(root, repositoryName+"-"+strconv.Itoa(e.cfg.IssueNumber)))
	branch := renderBranchName(e.cfg.BranchNamePattern, e.cfg.IssueNumber)
	if strings.TrimSpace(branch) == "" {
		return "", "", errors.New("branch name is empty")
	}
	if _, err := os.Stat(worktree); err == nil {
		return worktree, branch, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect implementation worktree: %w", err)
	}
	prune := exec.CommandContext(ctx, "git", "-C", repositoryDir, "worktree", "prune")
	if output, err := prune.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("prune stale implementation worktrees: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return "", "", fmt.Errorf("create implementation directory: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repositoryDir, "worktree", "add", "-B", branch, worktree, "HEAD")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("create implementation worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return worktree, branch, nil
}

func (e *Engine) designArtifactPath() string {
	name := fmt.Sprintf("%d_%s.md", e.cfg.IssueNumber, sanitizePart(e.cfg.IssueTitle))
	return filepath.Join(e.cfg.RepositoryDir, e.cfg.WorkspaceName, "design", name)
}

func (e *Engine) implementationArtifactPath() string {
	name := fmt.Sprintf("%d_%s.md", e.cfg.IssueNumber, sanitizePart(e.cfg.IssueTitle))
	return filepath.Join(e.cfg.RepositoryDir, e.cfg.WorkspaceName, "implementation", name)
}

func (e *Engine) attemptArtifactPath(attempt int, verification bool) string {
	name := fmt.Sprintf("%d回目_実装.md", attempt)
	if verification {
		name = fmt.Sprintf("%d回目_検討.md", attempt)
	}
	return filepath.Join(e.cfg.RepositoryDir, e.cfg.WorkspaceName, "implementation", strconv.Itoa(e.cfg.IssueNumber), name)
}

func (e *Engine) saveAttemptResult(attempt int, verification bool, result string) error {
	path := e.attemptArtifactPath(attempt, verification)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create attempt artifact directory: %w", err)
	}
	title := fmt.Sprintf("%s 実装 %d回目", e.cfg.IssueTitle, attempt)
	if verification {
		title = fmt.Sprintf("%s 検討 %d回目", e.cfg.IssueTitle, attempt)
	}
	content := fmt.Sprintf("# %s\n\n%s", strings.TrimSpace(title), strings.TrimSpace(result))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write attempt artifact: %w", err)
	}
	return nil
}

func (e *Engine) implementationPrompt(workflowPrompt, design, feedback string, attempt int) string {
	parts := []string{
		workflowPrompt,
		"", fmt.Sprintf("実装・検証ループ: %d/%d", attempt, e.cfg.LoopCount),
		"作業ディレクトリ: " + e.worktree,
		"ブランチ: " + e.branch,
		"", "承認済み設計:", strings.TrimSpace(design),
	}
	if feedback != "" {
		parts = append(parts, "", "検証者からの修正指示:", feedback)
	}
	parts = append(parts, "", "設計に従って実装と必要なテストを行い、リポジトリのimplement-from-designスキルに従って結果をまとめてください。")
	return strings.Join(parts, "\n")
}

func (e *Engine) verificationPrompt(design, implementation string, attempt int) string {
	return strings.Join([]string{
		"実装結果を独立して検証してください。リポジトリのverifier-from-designスキルと設計の受入基準に従ってください。",
		fmt.Sprintf("検証回数: %d/%d", attempt, e.cfg.LoopCount),
		"作業ディレクトリ: " + e.worktree,
		"ブランチ: " + e.branch,
		"", "Issue情報:", strings.TrimSpace(e.cfg.IssueContext),
		"", "承認済み設計:", strings.TrimSpace(design),
		"", "実装者の結果:", strings.TrimSpace(implementation),
		"", "ファイルを変更せず、必要なテストと差分確認だけを行ってください。",
		`最終回答はJSONオブジェクトのみとし、{"status":"passed|changes_requested","feedback":"実装者への具体的な修正指示","summary":"日本語の検証概要"}の形式にしてください。`,
		"問題がなければpassed、問題があればchanges_requestedを指定してください。",
	}, "\n")
}

func parseVerification(raw string) (verification, error) {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```") {
		lines := strings.Split(cleaned, "\n")
		if len(lines) >= 3 {
			cleaned = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	if start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}"); start >= 0 && end >= start {
		cleaned = cleaned[start : end+1]
	}
	var result verification
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return verification{}, fmt.Errorf("decode verifier response: %w", err)
	}
	if result.Status != "passed" && result.Status != "changes_requested" {
		return verification{}, fmt.Errorf("unsupported verifier status %q", result.Status)
	}
	return result, nil
}

func renderBranchName(pattern string, issueNumber int) string {
	number := strconv.Itoa(issueNumber)
	result := strings.ReplaceAll(pattern, "<issue番号>", number)
	result = strings.ReplaceAll(result, "<issueNumber>", number)
	return strings.TrimSpace(result)
}

func sanitizePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-", "#", "-", ".", "-", ",", "-", "(", "-", ")", "-")
	return strings.Trim(replacer.Replace(value), "-")
}
