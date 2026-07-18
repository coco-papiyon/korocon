package pullrequest

import (
	"context"
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
	RepositoryDir           string
	ImplementationDirectory string
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
	session          runner.AgentSession
	worktree         string
	conflictPrepared bool
}

func NewFixEngine(cfg FixConfig) *FixEngine { return &FixEngine{cfg: cfg} }

func (e *FixEngine) Run(ctx context.Context, prompt, model string, handler runner.ServerRequestHandler, setPhase func(string), onEvent func()) (runner.TurnResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ensureStarted(ctx, model, handler); err != nil {
		return runner.TurnResult{}, err
	}
	if setPhase != nil {
		setPhase("レビュー指摘修正")
	}
	prompt = strings.Join([]string{prompt, "", "作業ディレクトリ: " + e.worktree, "PR headブランチ: " + e.cfg.HeadRefName, "作業ディレクトリ内を直接修正し、必要なテストを実行してください。"}, "\n")
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	return e.session.RunTurn(ctx, prompt, model, onEvent)
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
	return e.session.RunTurn(ctx, prompt, model, onEvent)
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
	if e.session == nil {
		return nil
	}
	err := e.session.Close()
	e.session = nil
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
	if e.session != nil {
		return nil
	}
	worktree, err := e.ensureWorktree(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	session, err := startFixSession(ctx, runner.SessionConfig{Provider: e.cfg.Provider, Binary: e.cfg.Binary, Model: model, WorkingDir: worktree, LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handler})
	if err != nil {
		return fmt.Errorf("start review fix AI: %w", err)
	}
	e.worktree, e.session = worktree, session
	return nil
}

func (e *FixEngine) ensureConflictStarted(ctx context.Context, model string, handler runner.ServerRequestHandler) error {
	if e.session != nil && e.conflictPrepared {
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
	if e.session != nil {
		return nil
	}
	if strings.TrimSpace(e.cfg.Model) != "" {
		model = e.cfg.Model
	}
	session, err := startFixSession(ctx, runner.SessionConfig{Provider: e.cfg.Provider, Binary: e.cfg.Binary, Model: model, WorkingDir: worktree, LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handler})
	if err != nil {
		return fmt.Errorf("start conflict resolution AI: %w", err)
	}
	e.session = session
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
