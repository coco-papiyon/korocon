// Package workflowstate persists korocon's workflow state independently from
// GitHub labels.
package workflowstate

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const databaseName = "korocon.db"

var resolvePath = Path

// Key identifies one GitHub resource. Repository is part of the key because
// Issue and PR numbers are only unique within a repository.
type Key struct {
	Repository string
	Kind       string
	Number     int
}

// Path returns the path next to the running korocon executable.
func Path() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve korocon executable path: %w", err)
	}
	// `go run` creates a new executable below a temporary go-build directory
	// for every invocation. Use the current working directory in that case so
	// an approval and a subsequent list command share the same state database.
	if isGoRunExecutable(executable) {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working directory: %w", err)
		}
		return filepath.Join(workingDir, databaseName), nil
	}
	return filepath.Join(filepath.Dir(executable), databaseName), nil
}

func isGoRunExecutable(executable string) bool {
	path := filepath.ToSlash(filepath.Clean(executable))
	return !strings.HasSuffix(path, ".test") && strings.Contains(path, "/go-build") && strings.HasSuffix(filepath.ToSlash(filepath.Dir(path)), "/exe")
}

// Get returns the stored state for key. found is false when no state exists.
func Get(key Key) (state string, found bool, err error) {
	databasePath, err := resolvePath()
	if err != nil {
		return "", false, err
	}
	db, err := open(databasePath)
	if err != nil {
		return "", false, err
	}
	defer db.Close()

	err = db.QueryRow(`
		SELECT phase
		FROM workflow_states
		WHERE repository = ? AND resource_kind = ? AND resource_number = ?`,
		normalizeRepository(key.Repository), normalizeKind(key.Kind), key.Number,
	).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read workflow state: %w", err)
	}
	return state, true, nil
}

// Set inserts or replaces the state for key.
func Set(key Key, state string) error {
	databasePath, err := resolvePath()
	if err != nil {
		return err
	}
	db, err := open(databasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workflow_states (repository, resource_kind, resource_number, phase, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repository, resource_kind, resource_number)
		DO UPDATE SET phase = excluded.phase, updated_at = excluded.updated_at`,
		normalizeRepository(key.Repository), normalizeKind(key.Kind), key.Number,
		strings.TrimSpace(state), time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("write workflow state: %w", err)
	}
	return nil
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open workflow database: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workflow_states (
			repository TEXT NOT NULL,
			resource_kind TEXT NOT NULL,
			resource_number INTEGER NOT NULL,
			phase TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (repository, resource_kind, resource_number)
		)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize workflow database: %w", err)
	}
	return db, nil
}

func normalizeRepository(repository string) string {
	return strings.TrimSuffix(strings.TrimSpace(strings.ToLower(repository)), "/")
}

func normalizeKind(kind string) string {
	return strings.TrimSpace(strings.ToLower(kind))
}
