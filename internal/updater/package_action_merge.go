package updater

import "strings"

func mergeCommandResults(primary, fallback CommandResult, label string) CommandResult {
	merged := fallback
	merged.Command = primary.Command + "\n" + label + ": " + fallback.Command
	merged.Stdout = strings.TrimRight(primary.Stdout, "\r\n")
	if merged.Stdout != "" && fallback.Stdout != "" {
		merged.Stdout += "\n"
	}
	merged.Stdout += fallback.Stdout
	merged.Stderr = strings.TrimRight(primary.Stderr, "\r\n")
	if merged.Stderr != "" && fallback.Stderr != "" {
		merged.Stderr += "\n"
	}
	merged.Stderr += fallback.Stderr
	return merged
}
