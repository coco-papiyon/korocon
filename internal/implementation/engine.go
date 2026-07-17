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
	Binary                  string
	RepositoryDir           string
	WorkspaceName           string
	ImplementationDirectory string
	BranchNamePattern       string
	LoopCount               int
	IssueNumber             int
	IssueTitle              string
	IssueContext            string
	LogOut                  io.Writer
	LogErr                  io.Writer
}

type codexSession interface {
	RunTurn(context.Context, string, string, func()) (runner.TurnResult, error)
	Close() error
}

var startCodexSession = func(ctx context.Context, cfg runner.SessionConfig) (codexSession, error) {
	return runner.StartSession(ctx, cfg)
}

type Engine struct {
	cfg         Config
	mu          sync.Mutex
	implementer codexSession
	verifier    codexSession
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
		cfg.ImplementationDirectory = "../"
	}
	if strings.TrimSpace(cfg.BranchNamePattern) == "" {
		cfg.BranchNamePattern = "issue_#<issue番号>"
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
		tokens += implemented.Tokens

		if setPhase != nil {
			setPhase(fmt.Sprintf("検証%d回目", attempt))
		}
		checked, err := e.verifier.RunTurn(ctx, e.verificationPrompt(string(design), implemented.Text, attempt), model, onEvent)
		if err != nil {
			return runner.TurnResult{}, fmt.Errorf("verification attempt %d: %w", attempt, err)
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

func (e *Engine) ensureStarted(ctx context.Context, model string, handleRequest runner.ServerRequestHandler) error {
	if e.implementer != nil && e.verifier != nil {
		return nil
	}
	worktree, branch, err := e.ensureWorktree(ctx)
	if err != nil {
		return err
	}
	common := runner.SessionConfig{
		Binary: e.cfg.Binary, Model: model, WorkingDir: worktree,
		LogOut: e.cfg.LogOut, LogErr: e.cfg.LogErr, HandleRequest: handleRequest,
		ApprovalPolicy: "on-request",
	}
	common.Sandbox = "workspace-write"
	implementer, err := startCodexSession(ctx, common)
	if err != nil {
		return fmt.Errorf("start implementation Codex: %w", err)
	}
	common.Sandbox = "read-only"
	verifier, err := startCodexSession(ctx, common)
	if err != nil {
		_ = implementer.Close()
		return fmt.Errorf("start verification Codex: %w", err)
	}
	e.implementer, e.verifier = implementer, verifier
	e.worktree, e.branch = worktree, branch
	return nil
}

func (e *Engine) ensureWorktree(ctx context.Context) (string, string, error) {
	repositoryDir, err := filepath.Abs(e.cfg.RepositoryDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository directory: %w", err)
	}
	repositoryName := strings.TrimSuffix(filepath.Base(filepath.Clean(repositoryDir)), ".git")
	root := e.cfg.ImplementationDirectory
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
