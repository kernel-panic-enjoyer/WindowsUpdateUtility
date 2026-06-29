package updater

import "strings"

func mergeCommandAttemptsWithFinalResult(priorAttempt, finalAttempt CommandResult, label string) CommandResult {
	merged := finalAttempt
	merged.Command = priorAttempt.Command + "\n" + label + ": " + finalAttempt.Command
	merged.Stdout = strings.TrimRight(priorAttempt.Stdout, "\r\n")
	if merged.Stdout != "" && finalAttempt.Stdout != "" {
		merged.Stdout += "\n"
	}
	merged.Stdout += finalAttempt.Stdout
	merged.Stderr = strings.TrimRight(priorAttempt.Stderr, "\r\n")
	if merged.Stderr != "" && finalAttempt.Stderr != "" {
		merged.Stderr += "\n"
	}
	merged.Stderr += finalAttempt.Stderr
	return compactCommandResult(merged, commandResultStreamLimitBytes, maxCommandResultCommandBytes)
}
