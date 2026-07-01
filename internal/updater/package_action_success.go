package updater

import "strings"

const (
	packageActionRestartInitiatedCode = 1641
	packageActionRestartRequiredCode  = 3010
)

func normalizePackageActionResult(packageManager string, commandResult CommandResult) CommandResult {
	if commandResult.OK {
		return commandResult
	}

	if isPackageActionRestartSuccessCode(packageManager, commandResult.Code) || hasPackageActionSuccessOutput(commandResult) {
		commandResult.OK = true
		restartNote := packageActionRestartNote(commandResult.Code)
		if restartNote != "" && !strings.Contains(commandResult.Stdout+commandResult.Stderr, restartNote) {
			if strings.TrimSpace(commandResult.Stdout) != "" {
				commandResult.Stdout = strings.TrimRight(commandResult.Stdout, "\r\n") + "\n" + restartNote
			} else {
				commandResult.Stdout = restartNote
			}
		}
	}
	return commandResult
}

func hasPackageActionSuccessOutput(commandResult CommandResult) bool {
	normalizedOutput := normalizedCommandOutput(commandResult)
	if outputContainsAny(normalizedOutput, []string{
		"failed",
		"failure",
		"error",
		"exception",
		"denied",
		"not found",
		"no package found",
		"no product found",
		"could not find",
	}) {
		return false
	}
	return outputContainsAny(normalizedOutput, []string{
		"already up to date",
		"already installed",
		"no available update",
		"no update available",
		"no updates available",
		"no updates found",
		"no newer package versions are available",
		"the package is already installed",
		"successfully installed",
		"successfully upgraded",
		"successfully updated",
		"installation completed",
		"upgrade completed",
		"update completed",
	})
}

func isPackageActionRestartSuccessCode(packageManager string, exitCode int) bool {
	switch exitCode {
	case packageActionRestartInitiatedCode, packageActionRestartRequiredCode:
		return packageManager == managerWinget || packageManager == managerChoco || packageManager == managerStore
	default:
		return false
	}
}

func packageActionRestartNote(exitCode int) string {
	switch exitCode {
	case packageActionRestartInitiatedCode:
		return "Command completed successfully and initiated a restart."
	case packageActionRestartRequiredCode:
		return "Command completed successfully; restart required."
	default:
		return ""
	}
}
