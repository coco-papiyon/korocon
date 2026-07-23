// Package appdir resolves the directory used by korocon for tool-owned files.
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path returns the tool directory. A `go run` executable is temporary, so the
// current working directory is used to keep config and state across runs.
func Path() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve korocon executable path: %w", err)
	}
	if IsGoRunExecutable(executable) {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working directory: %w", err)
		}
		return workingDir, nil
	}
	return filepath.Dir(executable), nil
}

// IsGoRunExecutable reports whether executable has the layout used by `go run`.
func IsGoRunExecutable(executable string) bool {
	path := filepath.ToSlash(filepath.Clean(executable))
	return !strings.HasSuffix(path, ".test") && strings.Contains(path, "/go-build") && strings.HasSuffix(filepath.ToSlash(filepath.Dir(path)), "/exe")
}
