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
	LogOut                  io.Writer
	LogErr                  io.Writer
}

var startFixSession = func(ctx context.Context, cfg runner.SessionConfig) (runner.AgentSession, error) {
	return runner.StartAgentSession(ctx, cfg)
}

type FixEngine struct {
	cfg      FixConfig
	mu       sync.Mutex
	session  runner.AgentSession
	worktree string
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
