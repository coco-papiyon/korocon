package pullrequest

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

type FixConfig struct {
	Provider                string
	Binary                  string
	Model                   string
	VerifierProvider        string
	VerifierBinary          string
	VerifierModel           string
	RepositoryDir           string
	ImplementationDirectory string
	WorkspaceName           string
	LoopCount               int
	Number                  int
	Title                   string
	HeadRefName             string
	BaseRefName             string
	LogOut                  io.Writer
	LogErr                  io.Writer
}

var startFixSession = func(ctx context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
	return runner.StartAgentSession(ctx, cfg)
}

type FixEngine struct {
	cfg              FixConfig
	mu               sync.Mutex
	implementer      runner.AgentSession
	verifier         runner.AgentSession
	worktree         string
	conflictPrepared bool
}

type fixVerification struct {
	Status   string `json:"status"`
	Feedback string `json:"feedback"`
	Summary  string `json:"summary"`
}

func NewFixEngine(cfg FixConfig) *FixEngine {
	if strings.TrimSpace(cfg.WorkspaceName) == "" {
		cfg.WorkspaceName = ".workspace"
	}
	if cfg.LoopCount <= 0 {
		cfg.LoopCount = 3
	}
	if cfg.LoopCount > 10 {
		cfg.LoopCount = 10
	}
	if strings.TrimSpace(cfg.VerifierProvider) == "" {
		cfg.VerifierProvider = cfg.Provider
	}
	if strings.TrimSpace(cfg.VerifierModel) == "" {
		cfg.VerifierModel = cfg.Model
	}
	return &FixEngine{cfg: cfg}
}

func (e *FixEngine) Run(ctx context.Context, prompt, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureStarted(ctx, model, handler); err != nil {
		return runner.TurnResult{}, err
	}
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	feedback := ""
	tokens := 0
	for attempt := 1; attempt <= e.cfg.LoopCount; attempt++ {
		if setPhase != nil {
			setPhase(fmt.Sprintf("レビュー修正 実装%d回目", attempt))
		}
		implementationPrompt := e.fixImplementationPrompt(prompt, feedback, attempt)
		implemented, err := e.implementer.RunTurn(ctx, implementationPrompt, model, onEvent)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("review fix implementation attempt %d: %w", attempt, err)
		}
		if err := e.saveFixAttempt(attempt, false, implemented.Text); err != nil {
			return runner.TurnResult{}, err
		}
		tokens += implemented.Tokens

		if setPhase != nil {
			setPhase(fmt.Sprintf("レビュー修正 検証%d回目", attempt))
		}
		checked, err := e.verifier.RunTurn(ctx, e.fixVerificationPrompt(prompt, implemented.Text, attempt), e.cfg.VerifierModel, onEvent)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("review fix verification attempt %d: %w", attempt, err)
		}
		if err := e.saveFixAttempt(attempt, true, checked.Text); err != nil {
			return runner.TurnResult{}, err
		}
		tokens += checked.Tokens
		verdict, err := parseFixVerification(checked.Text)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("review fix verification attempt %d: %w", attempt, err)
		}
		if verdict.Status == "passed" {
			result := strings.TrimSpace(implemented.Text)
			if strings.TrimSpace(verdict.Summary) != "" {
				result += "\n\n## 検証者エージェントの判定\n" + strings.TrimSpace(verdict.Summary)
			}
			return runner.TurnResult{Text: result, Tokens: tokens}, nil
		}
		feedback = strings.TrimSpace(verdict.Feedback)
		if feedback == "" {
			feedback = strings.TrimSpace(verdict.Summary)
		}
	}
	return runner.TurnResult{}, fmt.Errorf("review fix verification did not pass after %d attempts: %s", e.cfg.LoopCount, feedback)
}

func (e *FixEngine) RunConflict(ctx context.Context, prompt, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureConflictStarted(ctx, model, handler); err != nil {
		return runner.TurnResult{}, err
	}
	if setPhase != nil {
		setPhase("コンフリクト解消")
	}
	files, err := e.conflictFiles(ctx)
	if err != nil {
		return runner.TurnResult{}, err
	}
	prompt = strings.Join([]string{
		prompt,
		"",
		"作業ディレクトリ: " + e.worktree,
		"PR headブランチ: " + e.cfg.HeadRefName,
		"PR baseブランチ: " + e.cfg.BaseRefName,
		"競合ファイル:", files,
		"作業ディレクトリではbaseブランチのmergeを開始済みです。競合マーカーを解消し、必要なテストを実行してください。",
	}, "\n")
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	return e.implementer.RunTurn(ctx, prompt, model, onEvent)
}

func (e *FixEngine) Publish(ctx context.Context, _ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worktree == "" {
		return errors.New("review fix worktree is not initialized")
	}
	status, err := gitOutput(ctx, e.worktree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := gitOutput(ctx, e.worktree, "add", "-A"); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, e.worktree, "commit", "-m", fmt.Sprintf("fix: address review feedback for PR #%d", e.cfg.Number)); err != nil {
			return err
		}
	}
	_, err = gitOutput(ctx, e.worktree, "push", "origin", "HEAD:"+e.cfg.HeadRefName)
	return err
}

func (e *FixEngine) PublishConflict(ctx context.Context, _ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worktree == "" || !e.conflictPrepared {
		return errors.New("conflict worktree is not initialized")
	}
	if _, err := gitOutput(ctx, e.worktree, "add", "-A"); err != nil {
		return err
	}
	unresolved, err := e.conflictFiles(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(unresolved) != "" {
		return fmt.Errorf("unresolved conflict files remain:\n%s", unresolved)
	}
	check := exec.CommandContext(ctx, "git", "-C", e.worktree, "diff", "--cached", "--check")
	if out, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("conflict resolution contains invalid markers or whitespace errors: %w: %s", err, strings.TrimSpace(string(out)))
	}
	status, err := gitOutput(ctx, e.worktree, "status", "--porcelain")
	if err != nil {
		return err
	}
	if mergeInProgress(ctx, e.worktree) {
		if _, err := gitOutput(ctx, e.worktree, "commit", "--no-edit"); err != nil {
			return fmt.Errorf("commit conflict resolution: %w", err)
		}
	} else if strings.TrimSpace(status) != "" {
		if _, err := gitOutput(ctx, e.worktree, "commit", "-m", fmt.Sprintf("fix: resolve conflicts for PR #%d", e.cfg.Number)); err != nil {
			return fmt.Errorf("commit conflict resolution: %w", err)
		}
	}
	baseRef := "origin/" + strings.TrimSpace(e.cfg.BaseRefName)
	ancestor := exec.CommandContext(ctx, "git", "-C", e.worktree, "merge-base", "--is-ancestor", baseRef, "HEAD")
	if out, err := ancestor.CombinedOutput(); err != nil {
		return fmt.Errorf("PR base branch is not integrated into the resolved HEAD: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, err = gitOutput(ctx, e.worktree, "push", "origin", "HEAD:"+e.cfg.HeadRefName)
	return err
}

func (e *FixEngine) Close() error {
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

// Worktree prepares and returns the PR head worktree without starting a fix AI.
func (e *FixEngine) Worktree(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worktree != "" {
		return e.worktree, nil
	}
	worktree, err := e.ensureWorktree(ctx)
	if err != nil {
		return "", err
	}
	e.worktree = worktree
	return worktree, nil
}

func (e *FixEngine) ensureStarted(ctx context.Context, model string, handler runner.ServerRequestHandler) error {
	if e.implementer != nil && e.verifier != nil {
		return nil
	}
	worktree, err := e.ensureWorktree(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	implementer, err := startFixSession(ctx, runner.SessionConfig{Provider: e.cfg.Provider, Binary: e.cfg.Binary, Model: model, WorkingDir: worktree, LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handler})
	if err != nil {
		return fmt.Errorf("start review fix AI: %w", err)
	}
	verifier, err := startFixSession(ctx, runner.SessionConfig{Provider: e.cfg.VerifierProvider, Binary: e.cfg.VerifierBinary, Model: e.cfg.VerifierModel, WorkingDir: worktree, LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handler, Sandbox: "read-only"})
	if err != nil {
		_ = implementer.Close()
		return fmt.Errorf("start review fix verifier AI: %w", err)
	}
	e.worktree, e.implementer, e.verifier = worktree, implementer, verifier
	return nil
}

func (e *FixEngine) ensureConflictStarted(ctx context.Context, model string, handler runner.ServerRequestHandler) error {
	if e.implementer != nil && e.conflictPrepared {
		return nil
	}
	worktree, err := e.ensureWorktree(ctx)
	if err != nil {
		return err
	}
	e.worktree = worktree
	if !e.conflictPrepared {
		if err := e.prepareConflictMerge(ctx); err != nil {
			return err
		}
		e.conflictPrepared = true
	}
	if e.implementer != nil {
		return nil
	}
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	implementer, err := startFixSession(ctx, runner.SessionConfig{Provider: e.cfg.Provider, Binary: e.cfg.Binary, Model: model, WorkingDir: worktree, LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handler})
	if err != nil {
		return fmt.Errorf("start conflict resolution AI: %w", err)
	}
	e.implementer = implementer
	return nil
}

func (e *FixEngine) prepareConflictMerge(ctx context.Context) error {
	base := strings.TrimSpace(e.cfg.BaseRefName)
	if base == "" {
		return errors.New("PR base branch is empty")
	}
	if mergeInProgress(ctx, e.worktree) {
		return nil
	}
	if _, err := gitOutput(ctx, e.worktree, "fetch", "origin", base); err != nil {
		return fmt.Errorf("fetch PR base branch: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", e.worktree, "merge", "--no-edit", "origin/"+base)
	out, err := cmd.CombinedOutput()
	if err == nil || mergeInProgress(ctx, e.worktree) {
		return nil
	}
	return fmt.Errorf("merge PR base branch: %w: %s", err, strings.TrimSpace(string(out)))
}

func (e *FixEngine) conflictFiles(ctx context.Context) (string, error) {
	files, err := gitOutput(ctx, e.worktree, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return "", fmt.Errorf("list conflict files: %w", err)
	}
	return strings.TrimSpace(files), nil
}

func (e *FixEngine) fixImplementationPrompt(workflowPrompt, feedback string, attempt int) string {
	parts := []string{
		workflowPrompt,
		"", fmt.Sprintf("実装・検証ループ: %d/%d", attempt, e.cfg.LoopCount),
		"作業ディレクトリ: " + e.worktree,
		"PR headブランチ: " + e.cfg.HeadRefName,
	}
	if feedback != "" {
		parts = append(parts, "", "検証者からの修正指示:", feedback)
	}
	parts = append(parts, "", "ユーザーの修正方針に従って実装と必要なテストを行い、review-comment-fixスキルの出力形式で結果をまとめてください。")
	return strings.Join(parts, "\n")
}

func (e *FixEngine) fixVerificationPrompt(workflowPrompt, implementation string, attempt int) string {
	return strings.Join([]string{
		"PRレビュー指摘に対する修正を独立して検証してください。",
		fmt.Sprintf("検証回数: %d/%d", attempt, e.cfg.LoopCount),
		"作業ディレクトリ: " + e.worktree,
		"PR headブランチ: " + e.cfg.HeadRefName,
		"", "レビュー指摘とユーザーの修正方針:", strings.TrimSpace(workflowPrompt),
		"", "実装者の結果:", strings.TrimSpace(implementation),
		"", "ファイルを変更せず、指示対象の修正、差分、必要なテストを確認してください。",
		`最終回答はJSONオブジェクトのみとし、{"status":"passed|changes_requested","feedback":"実装者への具体的な修正指示","summary":"日本語の検証概要"}の形式にしてください。`,
		"問題がなければpassed、問題があればchanges_requestedを指定してください。",
	}, "\n")
}

func (e *FixEngine) saveFixAttempt(attempt int, verification bool, result string) error {
	name := fmt.Sprintf("%d回目_実装.md", attempt)
	title := fmt.Sprintf("PR #%d レビュー指摘修正 実装%d回目", e.cfg.Number, attempt)
	if verification {
		name = fmt.Sprintf("%d回目_検証.md", attempt)
		title = fmt.Sprintf("PR #%d レビュー指摘修正 検証%d回目", e.cfg.Number, attempt)
	}
	path := filepath.Join(e.cfg.RepositoryDir, e.cfg.WorkspaceName, "review_fix", strconv.Itoa(e.cfg.Number), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create review fix attempt directory: %w", err)
	}
	content := fmt.Sprintf("# %s\n\n%s", title, strings.TrimSpace(result))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write review fix attempt: %w", err)
	}
	return nil
}

func parseFixVerification(raw string) (fixVerification, error) {
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
	var result fixVerification
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return fixVerification{}, fmt.Errorf("decode verifier response: %w", err)
	}
	if result.Status != "passed" && result.Status != "changes_requested" {
		return fixVerification{}, fmt.Errorf("unsupported verifier status %q", result.Status)
	}
	return result, nil
}

func mergeInProgress(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return cmd.Run() == nil
}

func (e *FixEngine) ensureWorktree(ctx context.Context) (string, error) {
	repositoryDir, err := filepath.Abs(e.cfg.RepositoryDir)
	if err != nil {
		return "", err
	}
	repositoryName := strings.TrimSuffix(filepath.Base(filepath.Clean(repositoryDir)), ".git")
	root := strings.NewReplacer("<リポジトリ名>", repositoryName, "<repositoryName>", repositoryName).Replace(e.cfg.ImplementationDirectory)
	if strings.TrimSpace(root) == "" {
		root = "../" + repositoryName + "-branches/"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(repositoryDir, root)
	}
	worktree := filepath.Clean(filepath.Join(root, repositoryName+"-pr-"+strconv.Itoa(e.cfg.Number)))
	if _, err := os.Stat(worktree); err == nil {
		status, statusErr := gitOutput(ctx, worktree, "status", "--porcelain")
		if statusErr != nil {
			return "", fmt.Errorf("inspect existing PR worktree: %w", statusErr)
		}
		if strings.TrimSpace(status) == "" {
			if _, mergeErr := gitOutput(ctx, worktree, "merge", "--ff-only", "origin/"+strings.TrimSpace(e.cfg.HeadRefName)); mergeErr != nil {
				return "", fmt.Errorf("update existing PR worktree: %w", mergeErr)
			}
		}
		return worktree, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repositoryDir, "worktree", "prune").CombinedOutput(); err != nil {
		return "", fmt.Errorf("prune stale PR worktrees: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return "", err
	}
	ref := "origin/" + strings.TrimSpace(e.cfg.HeadRefName)
	cmd := exec.CommandContext(ctx, "git", "-C", repositoryDir, "worktree", "add", "--detach", worktree, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create PR worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return worktree, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
