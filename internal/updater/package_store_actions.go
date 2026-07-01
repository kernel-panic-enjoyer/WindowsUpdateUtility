package updater

import (
	"context"
)

func storeUpdateCommand(storeUpdateTarget string, applyUpdate bool) []string {
	return managerCommand(managerStore, "update", storeUpdateTarget, "--apply", storeCLIBoolArgument(applyUpdate))
}

func storeUpdateCommandWithoutApplyOption(storeUpdateTarget string) []string {
	return managerCommand(managerStore, "update", storeUpdateTarget)
}

func storeCLIBoolArgument(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func storeActionUnavailableResult(action string) CommandResult {
	return CommandResult{Code: 1, Command: "store " + action, Stderr: storeActionUnavailableMessage}
}

func runStoreInstallWithFallback(storeIDOrQuery string) CommandResult {
	return runStoreInstallWithFallbackContext(context.Background(), storeIDOrQuery)
}

func runStoreInstallWithFallbackContext(ctx context.Context, storeIDOrQuery string) CommandResult {
	if packageActionManagerAvailable(managerStore) {
		storeInstallResult := runPackageActionCommand(ctx, managerStore, packageActionTimeout, managerCommand(managerStore, "install", storeIDOrQuery)...)
		if storeInstallResult.OK || ctx.Err() != nil || !packageActionManagerAvailable(managerWinget) {
			return storeInstallResult
		}
		appLog("Store install for %q failed; trying winget msstore fallback.", storeIDOrQuery)
		wingetFallbackResult := runWingetMSStoreInstall(ctx, storeIDOrQuery)
		return mergeCommandAttemptsWithFinalResult(storeInstallResult, wingetFallbackResult, "winget msstore fallback")
	}
	if packageActionManagerAvailable(managerWinget) {
		return runWingetMSStoreInstall(ctx, storeIDOrQuery)
	}
	return storeActionUnavailableResult("install")
}

func runWingetMSStoreInstall(ctx context.Context, storeIDOrQuery string) CommandResult {
	return runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, storeIDOrQuery, false)...)
}

func runStoreUpdateCommandWithApplyFallback(ctx context.Context, exactStoreTarget string) CommandResult {
	updateWithApplyResult := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateCommand(exactStoreTarget, true)...)
	updateWithApplyResult = normalizeStoreUpdateCommandResult(updateWithApplyResult)
	if updateWithApplyResult.OK || ctx.Err() != nil || !shouldRetryStoreUpdateWithoutApply(updateWithApplyResult) {
		return updateWithApplyResult
	}
	appLog("Store update command rejected --apply; retrying without that option.")
	updateWithoutApplyResult := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateCommandWithoutApplyOption(exactStoreTarget)...)
	updateWithoutApplyResult = normalizeStoreUpdateCommandResult(updateWithoutApplyResult)
	return mergeCommandAttemptsWithFinalResult(updateWithApplyResult, updateWithoutApplyResult, "store update without apply flag")
}

func normalizeStoreUpdateCommandResult(commandResult CommandResult) CommandResult {
	if commandResult.Code == commandCancelledCode || commandResult.Code == 124 {
		return commandResult
	}
	normalizedOutput := normalizedCommandOutput(commandResult)
	if !storeUpdateOutputReportsFailure(normalizedOutput) {
		return commandResult
	}
	commandResult.OK = false
	if commandResult.Code == 0 {
		commandResult.Code = 1
	}
	return appendCommandStderr(commandResult, "Store CLI reported an update failure despite the process exit code.")
}

func storeUpdateOutputReportsFailure(normalizedOutput string) bool {
	return outputContainsAny(normalizedOutput, []string{
		"error:",
		"could not find installed product metadata",
		"failed to read input in non-interactive mode",
	})
}
