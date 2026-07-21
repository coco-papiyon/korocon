package pullrequest

import (
	"context"
	"testing"
)

func TestLoadRestoresPhaseFromDatabase(t *testing.T) {
	dir := t.TempDir()
	response := `{"number":31,"title":"feature","state":"OPEN","mergeable":"MERGEABLE","labels":[]}`
	workflow, err := load(context.Background(), dir, 31, ".workspace", &fakeRunner{responses: map[string]string{"pr view 31 --json number": response}})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Finish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err := load(context.Background(), dir, 31, ".workspace", &fakeRunner{responses: map[string]string{"pr view 31 --json number": response}})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Phase != PhaseReview {
		t.Fatalf("phase = %q, want %q", reloaded.Phase, PhaseReview)
	}
}
