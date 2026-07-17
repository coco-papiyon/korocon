package issue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestLoadRestoresApprovalStates(t *testing.T) {
	for _, test := range []struct {
		label string
		phase Phase
	}{
		{"state:design_ready", PhaseDesign},
		{"state:implementation_ready", PhaseImplementation},
	} {
		runner := &fakeRunner{responses: []string{`{"number":8,"title":"waiting","labels":[{"name":"` + test.label + `"}]}`}}
		workflow, err := load(context.Background(), ".", 8, ".workspace", runner)
		if err != nil || !workflow.IsPending() || workflow.Phase != test.phase {
			t.Fatalf("label %s: workflow=%+v err=%v", test.label, workflow, err)
		}
	}
	runner := &fakeRunner{responses: []string{`{"number":8,"title":"waiting","labels":[{"name":"state:implementation_approved"}]}`}}
	if _, err := load(context.Background(), ".", 8, ".workspace", runner); err == nil {
		t.Fatal("completed workflow should not start another workflow")
	}
}

func TestLoadRejectsClosedIssue(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{"number":9,"title":"closed","state":"CLOSED"}`}}
	workflow, err := load(context.Background(), ".", 9, ".workspace", runner)
	if err != nil || workflow.IsOpen() {
		t.Fatalf("workflow=%+v err=%v", workflow, err)
	}
}

func TestPendingResultReadsExpectedArtifact(t *testing.T) {
	dir := t.TempDir()
	workflow := &Workflow{dir: dir, workspaceName: ".workspace", pending: true, Phase: PhaseDesign, Issue: Issue{Number: 10, Title: "Waiting"}}
	if _, err := workflow.SaveResult("saved result"); err != nil {
		t.Fatal(err)
	}
	result, path, err := workflow.PendingResult()
	if err != nil || result != "# Waiting\n\nsaved result" || !strings.HasSuffix(path, "10_waiting.md") {
		t.Fatalf("result=%q path=%q err=%v", result, path, err)
	}
}

func TestWorkflowUpdatesOnlyKnownStateLabels(t *testing.T) {
	runner := &fakeRunner{responses: []string{
		`{}`,
		`{"labels":[{"name":"bug"},{"name":"state:detected"},{"name":"state:custom"}]}`,
		`{}`,
	}}
	workflow := &Workflow{dir: ".", runner: runner, Issue: Issue{Number: 12}, Phase: PhaseDesign}
	if err := workflow.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	edit := strings.Join(runner.calls[2], " ")
	if !strings.Contains(edit, "--add-label state:design_running") || !strings.Contains(edit, "--remove-label state:detected") {
		t.Fatalf("unexpected edit command: %s", edit)
	}
	if strings.Contains(edit, "bug") || strings.Contains(edit, "state:custom") {
		t.Fatalf("non-workflow labels were removed: %s", edit)
	}
}

func TestWorkflowFinishesWithReadyOrFailedLabel(t *testing.T) {
	for _, test := range []struct {
		phase  Phase
		failed bool
		want   string
	}{
		{PhaseDesign, false, labelDesignReady},
		{PhaseImplementation, false, labelImplementationReady},
		{PhaseImplementation, true, labelFailed},
	} {
		runner := &fakeRunner{responses: []string{`{}`, `{"labels":[]}`, `{}`}}
		workflow := &Workflow{dir: ".", runner: runner, Issue: Issue{Number: 13}, Phase: test.phase}
		var runErr error
		if test.failed {
			runErr = context.Canceled
		}
		if err := workflow.Finish(context.Background(), runErr); err != nil {
			t.Fatal(err)
		}
		if command := strings.Join(runner.calls[2], " "); !strings.Contains(command, "--add-label "+test.want) {
			t.Fatalf("phase=%s failed=%v command=%s", test.phase, test.failed, command)
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
	runGitSyncCommand = func(_ context.Context, dir string, args ...string) error {
		if dir != "/repo" {
			t.Fatalf("dir = %q", dir)
		}
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	defer func() { runGitSyncCommand = oldRun }()

	if err := SyncRepository(context.Background(), "/repo"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || strings.Join(calls[0], " ") != "fetch --prune origin" || strings.Join(calls[1], " ") != "pull --ff-only" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSyncRepositoryStopsWhenFetchFails(t *testing.T) {
	oldRun := runGitSyncCommand
	calls := 0
	runGitSyncCommand = func(_ context.Context, _ string, _ ...string) error {
		calls++
		return errors.New("network unavailable")
	}
	defer func() { runGitSyncCommand = oldRun }()

	if err := SyncRepository(context.Background(), "/repo"); err == nil || !strings.Contains(err.Error(), "fetch repository") {
		t.Fatalf("SyncRepository() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
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
	if string(raw) != "# Add API / UI\n\nDesign body" {
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

func TestApproveDesignPostsResultAndSetsApprovedLabel(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{}`, `{}`, `{"labels":[{"name":"state:design_ready"}]}`, `{}`}}
	workflow := &Workflow{dir: t.TempDir(), runner: runner, Issue: Issue{Number: 22, Title: "Design title"}, Phase: PhaseDesign}
	if _, err := workflow.SaveResult("saved design result"); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.Approve(context.Background(), "design result"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	comment := strings.Join(runner.calls[0], " ")
	if !strings.Contains(comment, "issue comment 22 --body # Design title\n\nsaved design result") {
		t.Fatalf("unexpected comment command: %q", comment)
	}
	edit := strings.Join(runner.calls[3], " ")
	if !strings.Contains(edit, "--add-label "+labelDesignApproved) || !strings.Contains(edit, "--remove-label "+labelDesignReady) {
		t.Fatalf("unexpected edit command: %q", edit)
	}
}

func TestApproveImplementationCreatesPRAndSetsPRCreatedLabel(t *testing.T) {
	runner := &fakeRunner{responses: []string{`{}`, `{"labels":[{"name":"state:implementation_ready"}]}`, `{}`}}
	workflow := &Workflow{dir: ".", runner: runner, Issue: Issue{Number: 23}, Phase: PhaseImplementation}
	workflow.SetImplementationPublisher(func(_ context.Context, result string) (string, error) {
		if result != "implementation result" {
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
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
	edit := strings.Join(runner.calls[2], " ")
	if !strings.Contains(edit, "--add-label "+labelPRCreated) || !strings.Contains(edit, "--remove-label "+labelImplementationReady) {
		t.Fatalf("unexpected edit command: %q", edit)
	}
}

func TestApproveImplementationPublishFailureDoesNotChangeLabels(t *testing.T) {
	runner := &fakeRunner{}
	workflow := &Workflow{dir: ".", runner: runner, Issue: Issue{Number: 24}, Phase: PhaseImplementation}
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
