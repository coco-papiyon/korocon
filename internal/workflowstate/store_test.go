package workflowstate

import (
	"path/filepath"
	"testing"
)

func TestSetAndGet(t *testing.T) {
	previous := resolvePath
	databasePath := filepath.Join(t.TempDir(), "korocon.db")
	resolvePath = func() (string, error) { return databasePath, nil }
	t.Cleanup(func() { resolvePath = previous })

	key := Key{Repository: "github.com/acme/repository", Kind: "issue", Number: 42}
	if err := Set(key, "state:design_ready"); err != nil {
		t.Fatal(err)
	}
	state, found, err := Get(key)
	if err != nil || !found || state != "state:design_ready" {
		t.Fatalf("Get() = %q, %t, %v", state, found, err)
	}
	if err := Set(key, "state:implementation_running"); err != nil {
		t.Fatal(err)
	}
	state, found, err = Get(key)
	if err != nil || !found || state != "state:implementation_running" {
		t.Fatalf("Get() after update = %q, %t, %v", state, found, err)
	}
}
