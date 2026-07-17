package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coco-papiyon/korocon/internal/runner"
)

// Config controls the resident prompt worker.
type Config struct {
	Provider          string
	Binary            string
	Model             string
	WorkingDir        string
	AllowAllTools     bool
	StreamLogs        bool
	LogOut            io.Writer
	LogErr            io.Writer
	StatusOut         io.Writer
	ResultOut         io.Writer
	InitialPrompt     string
	InitialJob        *JobSpec
	AllowedCommands   []string
	AddAllowedCommand func(string) error
	BeforeJob         func(context.Context, uint64, string) error
	OnJobStart        func(context.Context, uint64, string) error
	OnJobFinish       func(context.Context, uint64, string, string, error) error
	HandleInput       func(context.Context, string) (InputAction, error)
}

// InputAction tells Run whether an external workflow consumed an input and
// whether it wants to enqueue another AI prompt.
type InputAction struct {
	Handled bool
	Prompt  string
	Job     *JobSpec
}

type JobExecutor func(context.Context, string, runner.ServerRequestHandler, func(string), func()) (runner.TurnResult, error)

type JobSpec struct {
	Prompt  string
	Execute JobExecutor
}

type residentJob struct {
	id      uint64
	prompt  string
	model   string
	execute JobExecutor
}

type approvalPrompt struct {
	decision chan string
	command  string
}

const maxProgressDots = 40

// Run reads prompts while a Codex app-server remains active. Codex turns are
// queued and executed in input order; other providers retain one process per
// prompt. It returns on input EOF or when ctx is cancelled.
func Run(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	if in == nil || out == nil {
		return errors.New("daemon input and output are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var nextID atomic.Uint64
	var wg sync.WaitGroup
	var writeMu sync.Mutex
	var logMu sync.Mutex
	var modelMu sync.RWMutex
	var diffMu sync.RWMutex
	var latestDiff string
	var hasLatestDiff bool
	var approvalMu sync.Mutex
	var allowedMu sync.RWMutex
	var sessionMu sync.Mutex
	var pendingApproval *approvalPrompt
	allowedCommands := normalizeAllowedCommands(cfg.AllowedCommands)
	logOut := synchronizedWriter{mu: &logMu, dst: cfg.LogOut}
	logErr := synchronizedWriter{mu: &logMu, dst: cfg.LogErr}
	// Tool command details are already present in the raw JSON log. Do not
	// route them through the interactive prompt.
	toolStatus := &toolStatusBridge{}
	resultOut := cfg.ResultOut
	if resultOut == nil {
		resultOut = out
	}
	displayOut := synchronizedWriter{mu: &logMu, dst: resultOut}
	provider := cfg.Provider
	if provider == "" {
		provider = "codex"
	}
	currentModel := cfg.Model
	status := func(format string, args ...any) {
		if cfg.StatusOut == nil {
			return
		}
		statusWriter := synchronizedWriter{mu: &logMu, dst: cfg.StatusOut}
		_, _ = fmt.Fprintf(statusWriter, format, args...)
	}
	promptMark := func() {
		if cfg.StatusOut == nil {
			return
		}
		statusWriter := synchronizedWriter{mu: &logMu, dst: cfg.StatusOut}
		_, _ = io.WriteString(statusWriter, "> ")
	}
	handleServerRequest := func(requestCtx context.Context, method string, params json.RawMessage) (any, error) {
		if !strings.HasSuffix(method, "requestApproval") {
			status("\r\033[2K[Codex応答待ち] %s\n", method)
			return nil, fmt.Errorf("unsupported Codex request: %s", method)
		}
		allowedMu.RLock()
		allowed := append([]string(nil), allowedCommands...)
		allowedMu.RUnlock()
		if method == "item/commandExecution/requestApproval" && commandRequestAllowed(params, allowed) {
			status("\r\033[2K[自動承認] %s\n", approvalDescription(method, params))
			return map[string]string{"decision": "accept"}, nil
		}
		var detail struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(params, &detail)
		prompt := &approvalPrompt{decision: make(chan string, 1), command: approvalCommand(params)}
		approvalMu.Lock()
		if pendingApproval != nil {
			approvalMu.Unlock()
			return nil, errors.New("another Codex approval is already pending")
		}
		pendingApproval = prompt
		approvalMu.Unlock()
		defer func() {
			approvalMu.Lock()
			if pendingApproval == prompt {
				pendingApproval = nil
			}
			approvalMu.Unlock()
		}()
		description := detail.Command
		if description == "" {
			description = detail.Reason
		}
		if description == "" {
			description = method
		}
		status("\r\033[2K[承認待ち] %s\n未入力状態でEnterまたは/approveで承認、/allowで承認して自動承認コマンドへ追加、/declineで拒否します。\n", description)
		promptMark()
		select {
		case <-requestCtx.Done():
			return nil, requestCtx.Err()
		case decision := <-prompt.decision:
			return map[string]string{"decision": decision}, nil
		}
	}

	startPrimarySession := func(model string) (*runner.Session, error) {
		sessionLogOut := io.Writer(io.Discard)
		sessionLogErr := io.Writer(io.Discard)
		if cfg.StreamLogs {
			sessionLogOut = logOut
			sessionLogErr = logErr
		}
		return runner.StartSession(ctx, runner.SessionConfig{
			Binary: cfg.Binary, Model: model, WorkingDir: cfg.WorkingDir,
			LogOut: sessionLogOut, LogErr: sessionLogErr, HandleRequest: handleServerRequest,
		})
	}
	var codexSession *runner.Session
	if provider == "codex" {
		var err error
		codexSession, err = startPrimarySession(currentModel)
		if err != nil {
			return err
		}
		defer func() {
			sessionMu.Lock()
			defer sessionMu.Unlock()
			if codexSession != nil {
				_ = codexSession.Close()
			}
		}()
	}
	commandOut := resultOut
	if cfg.StatusOut != nil {
		commandOut = cfg.StatusOut
	}
	var start func(string)
	respondApproval := func(decision string, addAllowed bool) {
		approvalMu.Lock()
		approval := pendingApproval
		approvalMu.Unlock()
		if approval == nil {
			_, _ = fmt.Fprintln(commandOut, "承認待ちの操作はありません。")
			return
		}
		if addAllowed {
			if approval.command == "" {
				_, _ = fmt.Fprintln(commandOut, "自動承認へ追加できるコマンドを取得できませんでした。")
				return
			}
			if cfg.AddAllowedCommand == nil {
				_, _ = fmt.Fprintln(commandOut, "自動承認コマンドの保存機能が設定されていません。")
				return
			}
			if err := cfg.AddAllowedCommand(approval.command); err != nil {
				_, _ = fmt.Fprintf(commandOut, "自動承認コマンドの保存に失敗しました: %v\n", err)
				return
			}
			allowedMu.Lock()
			allowedCommands = normalizeAllowedCommands(append(allowedCommands, approval.command))
			allowedMu.Unlock()
			_, _ = fmt.Fprintf(commandOut, "自動承認コマンドへ追加しました: %s\n", approval.command)
		}
		select {
		case approval.decision <- decision:
			_, _ = fmt.Fprintf(commandOut, "操作を%sしました。\n", map[bool]string{true: "承認", false: "拒否"}[decision == "accept"])
		default:
			_, _ = fmt.Fprintln(commandOut, "承認応答はすでに送信されています。")
		}
	}
	command := func(line string) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return
		}
		switch fields[0] {
		case "/model":
			if len(fields) == 1 {
				modelMu.RLock()
				selectedModel := currentModel
				modelMu.RUnlock()
				_, _ = fmt.Fprintln(commandOut, "選択可能なモデル:")
				for i, name := range runner.AvailableModels {
					marker := " "
					if name == selectedModel {
						marker = "*"
					}
					_, _ = fmt.Fprintf(commandOut, "  %d%s %s\n", i+1, marker, name)
				}
				_, _ = fmt.Fprintln(commandOut, "切り替えるには /model <番号> または /model <モデル名> を入力してください。")
				return
			}
			selection := fields[1]
			selected, ok := selectModel(selection)
			if !ok {
				_, _ = fmt.Fprintf(commandOut, "利用できないモデルです: %s\n", selection)
				return
			}
			sessionMu.Lock()
			session := codexSession
			if session != nil {
				if err := session.SetModel(ctx, selected); err != nil {
					sessionMu.Unlock()
					_, _ = fmt.Fprintf(commandOut, "Codexのモデル切替に失敗しました: %v\n", err)
					return
				}
			}
			sessionMu.Unlock()
			modelMu.Lock()
			currentModel = selected
			modelMu.Unlock()
			_, _ = fmt.Fprintf(commandOut, "モデルを %s に切り替えました。\n", selected)
		case "/approve", "/allow", "/decline":
			decision := "accept"
			if fields[0] == "/decline" {
				decision = "decline"
			}
			respondApproval(decision, fields[0] == "/allow")
		case "/diff":
			diffMu.RLock()
			diff, available := latestDiff, hasLatestDiff
			diffMu.RUnlock()
			if !available || diff == "" {
				_, _ = fmt.Fprintln(commandOut, "直前の修正のdiffはありません。")
				return
			}
			if len(fields) == 1 {
				_, _ = io.WriteString(commandOut, diff)
				if !strings.HasSuffix(diff, "\n") {
					_, _ = io.WriteString(commandOut, "\n")
				}
				return
			}
			filename := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
			diffPath := filename
			if !filepath.IsAbs(diffPath) {
				diffPath = filepath.Join(cfg.WorkingDir, diffPath)
			}
			if err := os.WriteFile(diffPath, []byte(diff), 0o600); err != nil {
				_, _ = fmt.Fprintf(commandOut, "diffの保存に失敗しました: %v\n", err)
				return
			}
			_, _ = fmt.Fprintf(commandOut, "diffを保存しました: %s\n", filename)
		default:
			_, _ = fmt.Fprintf(commandOut, "不明なコマンドです: %s\n", fields[0])
		}
	}
	writeResult := func(id uint64, output string, err error) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err != nil {
			_, writeErr := fmt.Fprintf(displayOut, "[job %d] error: %v\n", id, err)
			return writeErr
		}
		_, writeErr := io.WriteString(displayOut, output)
		return writeErr
	}

	var residentJobs chan residentJob
	var closeResidentJobs sync.Once
	if codexSession != nil {
		residentJobs = make(chan residentJob, 128)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range residentJobs {
				var progressMu sync.Mutex
				dots := 1
				displayID := job.id
				phase := ""
				phaseStarted := false
				setPhase := func(next string) {
					progressMu.Lock()
					defer progressMu.Unlock()
					if phaseStarted && strings.TrimSpace(next) != phase {
						displayID = nextID.Add(1)
					}
					phaseStarted = true
					phase = strings.TrimSpace(next)
					dots = 1
					if phase == "" {
						status("\r\033[2K[job %d] 実行中%s", displayID, strings.Repeat(".", dots))
					} else {
						status("\r\033[2K[job %d] 実行中(%s)%s", displayID, phase, strings.Repeat(".", dots))
					}
				}
				showProgress := func() {
					progressMu.Lock()
					defer progressMu.Unlock()
					dots++
					if dots > maxProgressDots {
						dots = 1
					}
					if phase == "" {
						status("\r\033[2K[job %d] 実行中%s", displayID, strings.Repeat(".", dots))
					} else {
						status("\r\033[2K[job %d] 実行中(%s)%s", displayID, phase, strings.Repeat(".", dots))
					}
				}
				var result runner.TurnResult
				var err error
				if job.execute != nil {
					sessionMu.Lock()
					if codexSession != nil {
						_ = codexSession.Close()
						codexSession = nil
					}
					sessionMu.Unlock()
					result, err = job.execute(ctx, job.model, handleServerRequest, setPhase, showProgress)
				} else {
					sessionMu.Lock()
					if codexSession == nil {
						codexSession, err = startPrimarySession(job.model)
					}
					session := codexSession
					sessionMu.Unlock()
					if err == nil {
						result, err = session.RunTurn(ctx, job.prompt, "", showProgress)
					}
				}
				if diff, diffErr := captureGitDiff(cfg.WorkingDir); diffErr == nil {
					diffMu.Lock()
					latestDiff = diff
					hasLatestDiff = true
					diffMu.Unlock()
				}
				if err != nil {
					_ = writeResult(job.id, "", err)
					status("\r\033[2K[job %d] 失敗\n", displayID)
					if cfg.OnJobFinish != nil {
						if finishErr := cfg.OnJobFinish(ctx, job.id, job.prompt, "", err); finishErr != nil {
							_ = writeResult(job.id, "", finishErr)
						}
					}
					promptMark()
					continue
				}
				if result.Tokens > 0 {
					status("\r\033[2K[job %d] 完了（トークン数: %d）\n", displayID, result.Tokens)
				} else {
					status("\r\033[2K[job %d] 完了\n", displayID)
				}
				if result.Text != "" {
					_, _ = io.WriteString(displayOut, result.Text)
					if !strings.HasSuffix(result.Text, "\n") {
						_, _ = io.WriteString(displayOut, "\n")
					}
				}
				if cfg.OnJobFinish != nil {
					if finishErr := cfg.OnJobFinish(ctx, job.id, job.prompt, result.Text, nil); finishErr != nil {
						_ = writeResult(job.id, "", finishErr)
					}
				}
				promptMark()
			}
		}()
	}
	stopResidentJobs := func() {
		if residentJobs != nil {
			closeResidentJobs.Do(func() { close(residentJobs) })
		}
	}

	var startJob func(JobSpec)
	start = func(prompt string) {
		startJob(JobSpec{Prompt: prompt})
	}
	startJob = func(spec JobSpec) {
		prompt := strings.TrimSpace(spec.Prompt)
		if prompt == "" {
			promptMark()
			return
		}
		id := nextID.Add(1)
		modelMu.RLock()
		jobModel := currentModel
		modelMu.RUnlock()
		status("[job %d] 実行中（provider: %s, model: %s）.", id, provider, jobModel)
		if cfg.BeforeJob != nil {
			if err := cfg.BeforeJob(ctx, id, prompt); err != nil {
				_ = writeResult(id, "", fmt.Errorf("prepare job: %w", err))
				status("\r\033[2K[job %d] 失敗\n", id)
				promptMark()
				return
			}
		}
		if cfg.OnJobStart != nil {
			if err := cfg.OnJobStart(ctx, id, prompt); err != nil {
				_ = writeResult(id, "", fmt.Errorf("start job: %w", err))
				status("\r\033[2K[job %d] 失敗\n", id)
				promptMark()
				return
			}
		}
		if residentJobs != nil {
			residentJobs <- residentJob{id: id, prompt: prompt, model: jobModel, execute: spec.Execute}
			return
		}
		if spec.Execute != nil {
			err := errors.New("implementation jobs require the codex provider")
			_ = writeResult(id, "", err)
			status("\r\033[2K[job %d] 失敗\n", id)
			if cfg.OnJobFinish != nil {
				_ = cfg.OnJobFinish(ctx, id, prompt, "", err)
			}
			promptMark()
			return
		}
		var progressMu sync.Mutex
		dots := 1
		showProgress := func() {
			progressMu.Lock()
			defer progressMu.Unlock()
			dots++
			if dots > maxProgressDots {
				dots = 1
			}
			status("\r\033[2K[job %d] 実行中%s", id, strings.Repeat(".", dots))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stdout, stderr strings.Builder
			var streamedResult strings.Builder
			var resultWriter *jsonResultWriter
			req := runner.Request{
				Provider: cfg.Provider, Binary: cfg.Binary, Prompt: prompt,
				Model: jobModel, WorkingDir: cfg.WorkingDir,
				AllowAllTools: cfg.AllowAllTools, Stdout: &stdout, Stderr: &stderr,
			}
			if cfg.StreamLogs {
				if cfg.Provider == "codex" || cfg.Provider == "" {
					resultWriter = &jsonResultWriter{log: progressWriter{dst: logOut, onWrite: showProgress}, result: &streamedResult, progress: showProgress, toolStatus: toolStatus.update}
					req.Stdout = resultWriter
				} else {
					req.Stdout = progressWriter{dst: io.MultiWriter(logOut, &streamedResult), onWrite: showProgress}
				}
				req.Stderr = progressWriter{dst: logErr, onWrite: showProgress}
			}
			err := runner.Run(ctx, req)
			if diff, diffErr := captureGitDiff(cfg.WorkingDir); diffErr == nil {
				diffMu.Lock()
				latestDiff = diff
				hasLatestDiff = true
				diffMu.Unlock()
			}
			if cfg.StreamLogs {
				if err != nil {
					_ = writeResult(id, "", err)
				}
				if err == nil {
					tokens := 0
					if resultWriter != nil {
						tokens = resultWriter.TokenCount()
					}
					if tokens > 0 {
						status("\r\033[2K[job %d] 完了（トークン数: %d）\n", id, tokens)
					} else {
						status("\r\033[2K[job %d] 完了\n", id)
					}
					if streamedResult.Len() > 0 {
						_, _ = io.WriteString(displayOut, streamedResult.String())
					}
					if cfg.OnJobFinish != nil {
						if finishErr := cfg.OnJobFinish(ctx, id, prompt, streamedResult.String(), nil); finishErr != nil {
							_ = writeResult(id, "", finishErr)
						}
					}
					promptMark()
				} else {
					status("\r\033[2K[job %d] 失敗\n", id)
					if cfg.OnJobFinish != nil {
						if finishErr := cfg.OnJobFinish(ctx, id, prompt, "", err); finishErr != nil {
							_ = writeResult(id, "", finishErr)
						}
					}
					promptMark()
				}
				return
			}
			if err != nil && stdout.Len() == 0 {
				stdout.WriteString(stderr.String())
			}
			_ = writeResult(id, stdout.String(), err)
			if err == nil {
				status("\r\033[2K[job %d] 完了\n", id)
			} else {
				status("\r\033[2K[job %d] 失敗\n", id)
			}
			if cfg.OnJobFinish != nil {
				result := stdout.String()
				if err != nil {
					result = ""
				}
				if finishErr := cfg.OnJobFinish(ctx, id, prompt, result, err); finishErr != nil {
					_ = writeResult(id, "", finishErr)
				}
			}
			promptMark()
		}()
	}

	processInput := func(line string) {
		if strings.TrimSpace(line) == "" {
			approvalMu.Lock()
			approval := pendingApproval
			approvalMu.Unlock()
			if approval != nil {
				respondApproval("accept", false)
				promptMark()
				return
			}
		}
		if cfg.HandleInput != nil {
			action, err := cfg.HandleInput(ctx, line)
			if err != nil {
				_, _ = fmt.Fprintf(displayOut, "入力処理に失敗しました: %v\n", err)
				promptMark()
				return
			}
			if action.Handled {
				if action.Job != nil {
					startJob(*action.Job)
				} else if prompt := strings.TrimSpace(action.Prompt); prompt != "" {
					start(prompt)
				} else {
					promptMark()
				}
				return
			}
		}
		if strings.TrimSpace(line) == "" {
			promptMark()
			return
		}
		if strings.HasPrefix(line, "/") {
			command(line)
			promptMark()
			return
		}
		start(strings.TrimSpace(line))
	}

	if cfg.InitialJob != nil {
		startJob(*cfg.InitialJob)
	} else if prompt := strings.TrimSpace(cfg.InitialPrompt); prompt != "" {
		start(prompt)
	}

	if handled, err := readInteractiveInput(ctx, in, cfg.StatusOut, processInput, toolStatus); handled {
		if errors.Is(err, context.Canceled) {
			cancel()
		}
		stopResidentJobs()
		wg.Wait()
		return err
	}

	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		// Prompts are commonly pasted as documents; allow lines larger than the
		// Scanner default while still bounding memory use for a resident process.
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readErr <- scanner.Err()
	}()
	promptMark()
	for {
		var line string
		select {
		case <-ctx.Done():
			stopResidentJobs()
			wg.Wait()
			return ctx.Err()
		case err := <-readErr:
			stopResidentJobs()
			if err != nil {
				wg.Wait()
				return fmt.Errorf("read daemon input: %w", err)
			}
			wg.Wait()
			return nil
		case line = <-lines:
		}
		processInput(line)
	}
}

// selectModel resolves both forms accepted by the interactive command. Keep
// the model name returned here canonical so it is passed unchanged to Codex's
// turn/start request.
func selectModel(selection string) (string, bool) {
	for i, name := range runner.AvailableModels {
		if selection == name || selection == fmt.Sprint(i+1) {
			return name, true
		}
	}
	return "", false
}

func captureGitDiff(workingDir string) (string, error) {
	if workingDir == "" {
		workingDir = "."
	}
	dir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	cmd := exec.Command("git", "diff", "--no-ext-diff", "--no-color")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(output), nil
}

type synchronizedWriter struct {
	mu  *sync.Mutex
	dst io.Writer
}

// toolStatusBridge is retained for parsing and test hooks. Interactive runs
// intentionally do not attach it to the line editor.
type toolStatusBridge struct {
	mu          sync.Mutex
	dst         io.Writer
	interactive func(string)
}

func (b *toolStatusBridge) update(message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.interactive != nil {
		b.interactive(message)
		return
	}
	if b.dst != nil {
		_, _ = fmt.Fprintf(b.dst, "\r\033[2K%s", message)
	}
}

func (b *toolStatusBridge) attach(fn func(string)) {
	b.mu.Lock()
	b.interactive = fn
	b.mu.Unlock()
}

func (b *toolStatusBridge) detach() {
	b.mu.Lock()
	b.interactive = nil
	b.mu.Unlock()
}

// jsonResultWriter stores every Codex event but exposes only final agent
// messages to the interactive CLI.
type jsonResultWriter struct {
	log        io.Writer
	result     io.Writer
	progress   func()
	toolStatus func(string)
	mu         sync.Mutex
	buf        []byte
	tokens     int
}

func (w *jsonResultWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.log.Write(p); err != nil {
		return 0, err
	}
	if w.progress != nil {
		w.progress()
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
			} `json:"item"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &event); err == nil && event.Type == "turn.completed" {
			w.tokens = event.Usage.InputTokens + event.Usage.OutputTokens
		}
		if err := json.Unmarshal(line, &event); err == nil && event.Item.Type == "command_execution" && w.toolStatus != nil {
			if event.Type == "item.started" && event.Item.Command != "" {
				w.toolStatus("ツール実行中: " + event.Item.Command)
			} else if event.Type == "item.completed" {
				w.toolStatus("")
			}
		}
		if err := json.Unmarshal(line, &event); err == nil && event.Type == "item.completed" && event.Item.Type == "agent_message" && event.Item.Text != "" {
			if _, err := io.WriteString(w.result, event.Item.Text+"\n"); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

func (w *jsonResultWriter) TokenCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tokens
}

type progressWriter struct {
	dst     io.Writer
	onWrite func()
}

func (w progressWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 && w.onWrite != nil {
		w.onWrite()
	}
	return n, err
}

func (w synchronizedWriter) Write(p []byte) (int, error) {
	if w.dst == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}
