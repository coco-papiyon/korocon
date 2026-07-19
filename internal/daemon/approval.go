package daemon

import (
	"encoding/json"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

func approvalDescription(method string, params json.RawMessage) string {
	var detail struct {
		Command string `json:"command"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(params, &detail)
	if detail.Command != "" {
		return detail.Command
	}
	if detail.Reason != "" {
		return detail.Reason
	}
	return method
}

func approvalCommand(params json.RawMessage) string {
	var request struct {
		Command        string `json:"command"`
		CommandActions []struct {
			Command string `json:"command"`
		} `json:"commandActions"`
		ProposedExecpolicyAmendment []string `json:"proposedExecpolicyAmendment"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return ""
	}
	for _, action := range request.CommandActions {
		if command := preferredCommandCandidate(action.Command); command != "" {
			return command
		}
	}
	if len(request.ProposedExecpolicyAmendment) > 0 {
		if command := preferredCommandCandidate(strings.Join(request.ProposedExecpolicyAmendment, " ")); command != "" {
			return command
		}
	}
	return preferredCommandCandidate(request.Command)
}

func preferredCommandCandidate(command string) string {
	candidates := commandCandidates(command)
	if len(candidates) == 0 {
		return ""
	}
	return strings.Join(strings.Fields(candidates[len(candidates)-1]), " ")
}

func commandRequestAllowed(params json.RawMessage, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, command := range normalizeAllowedCommands(allowed) {
		allowedSet[normalizeCommand(command)] = struct{}{}
	}
	if len(allowedSet) == 0 {
		return false
	}

	var request struct {
		Command                     string   `json:"command"`
		ProposedExecpolicyAmendment []string `json:"proposedExecpolicyAmendment"`
		CommandActions              []struct {
			Command string `json:"command"`
		} `json:"commandActions"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return false
	}
	if commandMatchesAllowed(request.Command, allowedSet) {
		return true
	}
	if len(request.ProposedExecpolicyAmendment) > 0 && commandMatchesAllowed(strings.Join(request.ProposedExecpolicyAmendment, " "), allowedSet) {
		return true
	}
	for _, action := range request.CommandActions {
		if commandMatchesAllowed(action.Command, allowedSet) {
			return true
		}
	}
	return false
}

func commandMatchesAllowed(command string, allowedSet map[string]struct{}) bool {
	for _, candidate := range commandCandidates(command) {
		normalized := normalizeCommand(candidate)
		if _, ok := allowedSet[normalized]; ok {
			return true
		}
		for allowed := range allowedSet {
			if strings.HasPrefix(normalized, allowed+" ") && safeCommandArguments(normalized[len(allowed):]) {
				return true
			}
		}
	}
	return false
}

func safeCommandArguments(arguments string) bool {
	arguments = stripAllowedStderrRedirection(arguments)
	return !containsUnsafeShellMetacharacter(arguments) && !strings.Contains(arguments, "$(")
}

func stripAllowedStderrRedirection(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	devNull := strings.ToLower(strings.TrimSpace(os.DevNull))
	for _, suffix := range []string{"2> " + devNull, "2>" + devNull, "2> " + strings.ToUpper(devNull), "2>" + strings.ToUpper(devNull)} {
		if strings.HasSuffix(strings.ToLower(trimmed), suffix) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
		}
	}
	return trimmed
}

func containsUnsafeShellMetacharacter(arguments string) bool {
	var quote byte
	escaped := false
	for i := 0; i < len(arguments); i++ {
		ch := arguments[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case ';', '|', '>', '<', '`', '&', '\r', '\n':
			return true
		}
	}
	return false
}

func commandCandidates(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	candidates := []string{command}
	if stripped, ok := stripPowerShellEnvAssignments(command); ok {
		candidates = append(candidates, stripped)
	}
	if stripped, ok := stripPOSIXEnvAssignments(command); ok {
		candidates = append(candidates, stripped)
	}
	return candidates
}

func stripPOSIXEnvAssignments(command string) (string, bool) {
	fields := strings.Fields(command)
	stripped := 0
	for stripped < len(fields) {
		assignment := fields[stripped]
		equals := strings.IndexByte(assignment, '=')
		if equals <= 0 || !validEnvironmentName(assignment[:equals]) || !safeEnvironmentValue(assignment[equals+1:]) {
			break
		}
		stripped++
	}
	if stripped == 0 || stripped == len(fields) {
		return "", false
	}
	return strings.Join(fields[stripped:], " "), true
}

func validEnvironmentName(name string) bool {
	for i, ch := range name {
		if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

func safeEnvironmentValue(value string) bool {
	return value != "" && !strings.ContainsAny(value, "$`;'\"|&<>\r\n")
}

func stripPowerShellEnvAssignments(command string) (string, bool) {
	segments := strings.Split(command, ";")
	remaining := make([]string, 0, len(segments))
	stripped := false
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if len(remaining) == 0 && strings.HasPrefix(strings.ToLower(segment), "$env:") && strings.Contains(segment, "=") {
			stripped = true
			continue
		}
		remaining = append(remaining, segment)
	}
	if !stripped || len(remaining) != 1 {
		return "", false
	}
	return remaining[0], true
}

func normalizeAllowedCommands(commands []string) []string {
	seen := make(map[string]struct{}, len(commands))
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		key := normalizeCommand(command)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, command)
	}
	return out
}

func normalizeCommand(command string) string {
	return strings.ToLower(strings.Join(strings.Fields(command), " "))
}

func copilotPathRequestAllowed(params json.RawMessage, allowed []string) bool {
	var request struct {
		RawInput struct {
			Path     string `json:"path"`
			FileName string `json:"fileName"`
			Diff     string `json:"diff"`
		} `json:"rawInput"`
	}
	if json.Unmarshal(params, &request) != nil {
		return false
	}
	patterns := normalizeAllowedPaths(allowed)
	if len(patterns) == 0 {
		return false
	}
	if request.RawInput.Diff != "" {
		paths := diffTargetPaths(request.RawInput.Diff)
		if len(paths) == 0 {
			return false
		}
		for _, target := range paths {
			if !pathMatchesAllowed(target, patterns) {
				return false
			}
		}
		for _, target := range []string{request.RawInput.Path, request.RawInput.FileName} {
			if strings.TrimSpace(target) != "" && !pathMatchesAllowed(target, patterns) {
				return false
			}
		}
		return true
	}
	paths := []string{request.RawInput.Path, request.RawInput.FileName}
	found := false
	for _, target := range paths {
		if strings.TrimSpace(target) == "" {
			continue
		}
		found = true
		if !pathMatchesAllowed(target, patterns) {
			return false
		}
	}
	return found
}

func copilotPathApprovalDescription(params json.RawMessage) string {
	var request struct {
		Title    string `json:"title"`
		RawInput struct {
			Path     string `json:"path"`
			FileName string `json:"fileName"`
			Diff     string `json:"diff"`
		} `json:"rawInput"`
	}
	if json.Unmarshal(params, &request) != nil {
		return "Copilotのパス操作"
	}
	if request.RawInput.Path != "" {
		return request.RawInput.Path
	}
	if request.RawInput.FileName != "" {
		return request.RawInput.FileName
	}
	if paths := diffTargetPaths(request.RawInput.Diff); len(paths) > 0 {
		return "diff: " + strings.Join(paths, ", ")
	}
	if request.Title != "" {
		return request.Title
	}
	return "Copilotのパス操作"
}

func normalizeAllowedPaths(patterns []string) []string {
	home, _ := os.UserHomeDir()
	seen := make(map[string]struct{}, len(patterns))
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if pattern == "~" && home != "" {
			pattern = filepath.ToSlash(home)
		} else if strings.HasPrefix(pattern, "~/") && home != "" {
			pattern = strings.TrimSuffix(filepath.ToSlash(home), "/") + pattern[1:]
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	return out
}

func pathMatchesAllowed(target string, patterns []string) bool {
	target = filepath.ToSlash(filepath.Clean(strings.TrimSpace(target)))
	if target == "." || target == "" {
		return false
	}
	candidates := []string{target}
	if !strings.HasPrefix(target, "/") {
		candidates = append(candidates, "/"+target)
	}
	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if matched, err := pathpkg.Match(pattern, candidate); err == nil && matched {
				return true
			}
		}
	}
	return false
}

func diffTargetPaths(diff string) []string {
	seen := make(map[string]struct{})
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "diff --git "))
		if len(fields) != 2 {
			return nil
		}
		for _, target := range fields {
			target = strings.TrimPrefix(strings.TrimPrefix(target, "a/"), "b/")
			target = filepath.ToSlash(filepath.Clean(target))
			if target == "." || target == "dev/null" {
				continue
			}
			if _, ok := seen[target]; ok {
				continue
			}
			seen[target] = struct{}{}
			paths = append(paths, target)
		}
	}
	return paths
}
