package updater

import "strings"

func requireExplicitPinnedUpdate(result CommandResult) CommandResult {
	return appendCommandStderr(result, "Winget reported that this package is pinned. Updating it requires explicit user confirmation.")
}

func requireExplicitUnknownVersionUpdate(result CommandResult) CommandResult {
	return appendCommandStderr(result, "Winget reported an unknown installed version. Updating this package requires explicit user confirmation.")
}

func appendCommandStderr(result CommandResult, message string) CommandResult {
	if strings.TrimSpace(result.Stderr) != "" {
		result.Stderr = strings.TrimRight(result.Stderr, "\r\n") + "\n" + message
	} else {
		result.Stderr = message
	}
	return result
}

func normalizedCommandOutput(result CommandResult) string {
	return strings.ToLower(result.Stdout + "\n" + result.Stderr)
}

func outputContainsAny(output string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}
