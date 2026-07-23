package workflowstate

import (
	"path/filepath"
	"testing"
)

func TestIsGoRunExecutable(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/tmp/go-build123/exe/korocon", want: true},
		{path: "/tmp/go-build123/exe/workflowstate.test", want: false},
		{path: "/tmp/go-build123/korocon", want: false},
		{path: "/usr/local/bin/korocon", want: false},
	} {
		if got := isGoRunExecutable(test.path); got != test.want {
			t.Fatalf("isGoRunExecutable(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}

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
