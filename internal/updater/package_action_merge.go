package updater

import "strings"

func mergeCommandAttemptsWithFinalResult(previousAttempt, finalAttempt CommandResult, finalAttemptLabel string) CommandResult {
	mergedResult := finalAttempt
	mergedResult.Command = previousAttempt.Command + "\n" + finalAttemptLabel + ": " + finalAttempt.Command
	mergedResult.Stdout = mergeCommandAttemptOutput(previousAttempt.Stdout, finalAttempt.Stdout)
	mergedResult.Stderr = mergeCommandAttemptOutput(previousAttempt.Stderr, finalAttempt.Stderr)
	return compactCommandResult(mergedResult, commandResultStreamLimitBytes, maxCommandResultCommandBytes)
}

func mergeCommandAttemptOutput(previousOutput, finalOutput string) string {
	previousOutput = strings.TrimRight(previousOutput, "\r\n")
	if previousOutput != "" && finalOutput != "" {
		return previousOutput + "\n" + finalOutput
	}
	return previousOutput + finalOutput
}
