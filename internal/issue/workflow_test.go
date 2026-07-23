package issue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	responses []string
	calls     [][]string
}

func (r *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(r.responses) == 0 {
		return []byte(`{}`), nil
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return []byte(response), nil
}

func TestLoadBuildsDesignPromptFromIssueContext(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{
		"number":42,"title":"add feature","body":"details","url":"https://example/42",
		"author":{"login":"alice"},"labels":[{"name":"bug"}],
		"comments":[{"author":{"login":"bob"},"body":"please handle this"}]
	}`}}
	workflow, err := load(context.Background(), ".", 42, ".workspace", runner)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Phase != PhaseDesign {
		t.Fatalf("phase = %s", workflow.Phase)
	}
	prompt := workflow.Prompt()
	for _, expected := range []string{"設計を行ってください", "#42 add feature", "details", "bug", "bob: please handle this"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "state:design_ready") {
		t.Fatalf("tool workflow details leaked into AI prompt: %s", prompt)
	}
}

func TestLoadSelectsImplementationOnlyAfterDesignApproval(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{
		"number":7,"title":"implement","body":"body",
		"labels":[{"name":"state:design_approved"}]
	}`}}
	workflow, err := load(context.Background(), ".", 7, ".workspace", runner)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Phase != PhaseImplementation || !strings.Contains(workflow.Prompt(), "実装を行ってください") {
		t.Fatalf("unexpected implementation workflow: phase=%s prompt=%q", workflow.Phase, workflow.Prompt())
	}
}

func TestLoadStopsAtApprovalStates(t *testing.T) {
	for _, test := range []struct {
		label  string
		phase  Phase
		subdir string
	}{
		{label: "state:design_ready", phase: PhaseDesignReady, subdir: "design"},
		{label: "state:implementation_ready", phase: PhaseImplementationReady, subdir: "implementation"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".workspace", test.subdir, "8_waiting.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("saved result"), 0o644); err != nil {
			t.Fatal(err)
		}
		runner := &fakeRunner{responses: []string{`{"number":8,"title":"waiting","labels":[{"name":"` + test.label + `"}]}`}}
		workflow, err := load(context.Background(), dir, 8, ".workspace", runner)
		if err != nil {
			t.Fatalf("label %s: %v", test.label, err)
		}
		if workflow.Phase != test.phase || workflow.PendingApprovalResult() != "saved result" {
			t.Fatalf("label %s: phase=%s result=%q", test.label, workflow.Phase, workflow.PendingApprovalResult())
		}
	}
	runner := &fakeRunner{responses: []string{`{"number":8,"title":"waiting","labels":[{"name":"state:implementation_approved"}]}`}}
	if _, err := load(context.Background(), ".", 8, ".workspace", runner); err == nil {
		t.Fatal("implementation_approved should not start another workflow")
	}
}

func TestLoadRejectsClosedIssue(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{"number":8,"title":"closed","state":"CLOSED","labels":[]}`}}
	if _, err := load(context.Background(), ".", 8, ".workspace", runner); err == nil || !strings.Contains(err.Error(), "openではありません") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRestoresPhaseFromDatabase(t *testing.T) {
	dir := t.TempDir()
	response := `{"number":31,"title":"feature","state":"OPEN","labels":[]}`
	workflow, err := load(context.Background(), dir, 31, ".workspace", &fakeRunner{responses: []string{response}})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := load(context.Background(), dir, 31, ".workspace", &fakeRunner{responses: []string{response}})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Phase != PhaseDesignReady {
		t.Fatalf("phase = %q, want %q", reloaded.Phase, PhaseDesignReady)
	}
}

func TestWorkflowStoresStateWithoutUpdatingLabels(t *testing.T) {
	runner := &fakeRunner{}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, Issue: Issue{Number: 12}, Phase: PhaseDesign}
	if err := workflow.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
}

func TestWorkflowFinishesWithReadyOrFailedState(t *testing.T) {
	for _, test := range []struct {
		phase  Phase
		failed bool
		want   string
	}{
		{PhaseDesign, false, labelDesignReady},
		{PhaseImplementation, false, labelImplementationReady},
		{PhaseImplementation, true, labelImplementationFailed},
	} {
		runner := &fakeRunner{}
		workflow := &Workflow{dir: t.TempDir(), runner: runner, Issue: Issue{Number: 13}, Phase: test.phase}
		var runErr error
		if test.failed {
			runErr = context.Canceled
		}
		if err := workflow.Finish(context.Background(), runErr); err != nil {
			t.Fatal(err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("phase=%s failed=%v calls=%v", test.phase, test.failed, runner.calls)
		}
	}
}

func TestRevisionPromptIncludesFeedbackAndIssue(t *testing.T) {
	workflow := &Workflow{Issue: Issue{Number: 21, Title: "feature", Body: "details"}, Phase: PhaseDesign}
	prompt := workflow.RevisionPrompt("画面仕様を追加してください")
	for _, expected := range []string{"再設計", "画面仕様を追加してください", "#21 feature", "details"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q: %s", expected, prompt)
		}
	}
}

func TestSyncRepositoryFetchesAndPulls(t *testing.T) {
	oldRun := runGitSyncCommand
	var calls [][]string
	runGitSyncCommand = func(_ context.Context, dir string, args ...string) (string, error) {
		if dir != "/repo" {
			t.Fatalf("dir = %q", dir)
		}
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}
	defer func() { runGitSyncCommand = oldRun }()

	if err := SyncRepository(context.Background(), "/repo", "fail"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || strings.Join(calls[0], " ") != "status --porcelain" || strings.Join(calls[1], " ") != "fetch --prune origin" || strings.Join(calls[2], " ") != "pull --no-rebase" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSyncRepositoryStopsWhenFetchFails(t *testing.T) {
	oldRun := runGitSyncCommand
	calls := 0
	runGitSyncCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		calls++
		if strings.Join(args, " ") == "fetch --prune origin" {
			return "", errors.New("network unavailable")
		}
		return "", nil
	}
	defer func() { runGitSyncCommand = oldRun }()

	if err := SyncRepository(context.Background(), "/repo", "fail"); err == nil || !strings.Contains(err.Error(), "fetch repository") {
		t.Fatalf("SyncRepository() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestSyncRepositoryStashesDirtyWorktreeAndRestoresIt(t *testing.T) {
	oldRun := runGitSyncCommand
	var calls [][]string
	runGitSyncCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if strings.Join(args, " ") == "status --porcelain" {
			return " M implementer.sh\n?? generated.txt\n", nil
		}
		return "", nil
	}
	defer func() { runGitSyncCommand = oldRun }()

	if err := SyncRepository(context.Background(), "/repo", "stash"); err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(calls))
	for i, call := range calls {
		got[i] = strings.Join(call, " ")
	}
	want := []string{
		"status --porcelain",
		"stash push --include-untracked --message korocon: pre-job sync",
		"fetch --prune origin",
		"pull --no-rebase",
		"stash pop",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestSyncRepositoryRejectsDirtyWorktreeByDefault(t *testing.T) {
	oldRun := runGitSyncCommand
	var calls [][]string
	runGitSyncCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return " M implementer.sh\n", nil
	}
	defer func() { runGitSyncCommand = oldRun }()

	err := SyncRepository(context.Background(), "/repo", "")
	if err == nil || !strings.Contains(err.Error(), "syncDirtyWorktree") {
		t.Fatalf("SyncRepository() error = %v", err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "status --porcelain" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSyncRepositoryKeepsStashWhenRestoreFails(t *testing.T) {
	oldRun := runGitSyncCommand
	runGitSyncCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "status --porcelain":
			return " M implementer.sh\n", nil
		case "stash pop":
			return "", errors.New("conflict")
		default:
			return "", nil
		}
	}
	defer func() { runGitSyncCommand = oldRun }()

	err := SyncRepository(context.Background(), "/repo", "stash")
	if err == nil || !strings.Contains(err.Error(), "stash was kept for manual recovery") {
		t.Fatalf("SyncRepository() error = %v", err)
	}
}

func TestSaveResultUsesKorobokcleWorkspaceLayout(t *testing.T) {
	dir := t.TempDir()
	workflow := &Workflow{
		dir: dir, workspaceName: ".custom-workspace",
		Issue: Issue{Number: 21, Title: "Add API / UI"}, Phase: PhaseDesign,
	}
	path, err := workflow.SaveResult("# Old heading\n\nDesign body\n")
	if err != nil {
		t.Fatal(err)
	}
	if path != ".custom-workspace/design/21_add-api---ui.md" {
		t.Fatalf("path = %q", path)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# 設計結果\n\nDesign body" {
		t.Fatalf("artifact = %q", raw)
	}
}

func TestSaveImplementationResultUsesImplementationDirectory(t *testing.T) {
	dir := t.TempDir()
	workflow := &Workflow{
		dir: dir, workspaceName: ".workspace",
		Issue: Issue{Number: 31, Title: "Implement feature"}, Phase: PhaseImplementation,
	}
	path, err := workflow.SaveResult("implementation result")
	if err != nil {
		t.Fatal(err)
	}
	if path != ".workspace/implementation/31_implement-feature.md" {
		t.Fatalf("path = %q", path)
	}
}

func TestApproveDesignPostsResultAndStoresApprovedState(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{}`}}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, Issue: Issue{Number: 22, Title: "Design title"}, Phase: PhaseDesign}
	if _, err := workflow.SaveResult("saved design result"); err != nil {
		t.Fatal(err)
	}
	path, err := workflow.artifactPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# 手動編集\n\nmanual design result"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Approve(context.Background(), "design result"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	comment := strings.Join(runner.calls[0], " ")
	if !strings.Contains(comment, "issue comment 22 --body # 設計結果\n\nmanual design result") {
		t.Fatalf("unexpected comment command: %q", comment)
	}
}

func TestApproveImplementationCreatesPRAndStoresPRCreatedState(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	workflow := &Workflow{dir: dir, runner: runner, Issue: Issue{Number: 23, Title: "Implementation"}, Phase: PhaseImplementation}
	if _, err := workflow.SaveResult("saved implementation result"); err != nil {
		t.Fatal(err)
	}
	workflow.SetImplementationPublisher(func(_ context.Context, result string) (string, error) {
		if result != "# 実装結果\n\nsaved implementation result" {
			t.Fatalf("result = %q", result)
		}
		return "https://github.com/acme/repo/pull/23", nil
	})
	url, err := workflow.Approve(context.Background(), "implementation result")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/repo/pull/23" {
		t.Fatalf("url = %q", url)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
}

func TestApproveImplementationPublishFailureDoesNotChangeLabels(t *testing.T) {
	runner := &fakeRunner{}
	dir := t.TempDir()
	workflow := &Workflow{dir: dir, runner: runner, Issue: Issue{Number: 24, Title: "Implementation"}, Phase: PhaseImplementation}
	if _, err := workflow.SaveResult("implementation result"); err != nil {
		t.Fatal(err)
	}
	workflow.SetImplementationPublisher(func(context.Context, string) (string, error) {
		return "", errors.New("push failed")
	})
	if _, err := workflow.Approve(context.Background(), "implementation result"); err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("Approve() error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("labels changed after publish failure: %v", runner.calls)
	}
}
