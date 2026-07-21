package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

// TemplateData contains the values available to configuration templates.
type TemplateData struct {
	IssueNumber    int
	RepositoryName string
}

// ExpandTemplate expands a configuration value using the supported variables.
// Variables can be written as {{ issue_number }} or {{ .issue_number }}.
func ExpandTemplate(value string, data TemplateData) (string, error) {
	if strings.ContainsAny(value, "<>") {
		return "", fmt.Errorf("legacy configuration placeholders are not supported; use {{ issue_number }} or {{ repository_name }}")
	}
	funcs := template.FuncMap{
		"issue_number":    func() int { return data.IssueNumber },
		"repository_name": func() string { return data.RepositoryName },
	}
	tmpl, err := template.New("config").Funcs(funcs).Option("missingkey=error").Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse configuration template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, map[string]any{
		"issue_number":    data.IssueNumber,
		"repository_name": data.RepositoryName,
	}); err != nil {
		return "", fmt.Errorf("expand configuration template: %w", err)
	}
	return output.String(), nil
}

// RepositoryName returns the repository directory name without a .git suffix.
func RepositoryName(repositoryDir string) string {
	name := filepath.Base(filepath.Clean(repositoryDir))
	return strings.TrimSuffix(name, ".git")
}
