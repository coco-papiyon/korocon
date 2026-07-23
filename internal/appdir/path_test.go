package appdir

import "testing"

func TestIsGoRunExecutable(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "/tmp/go-build123/exe/korocon", want: true},
		{path: "/tmp/go-build123/exe/app.test", want: false},
		{path: "/tmp/go-build123/korocon", want: false},
		{path: "/usr/local/bin/korocon", want: false},
	} {
		if got := IsGoRunExecutable(test.path); got != test.want {
			t.Errorf("IsGoRunExecutable(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}
