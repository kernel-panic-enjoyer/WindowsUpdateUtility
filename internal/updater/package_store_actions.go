package updater

import (
	"context"
)

func storeUpdateCommand(target string, apply bool) []string {
	return managerCommand(managerStore, "update", target, "--apply", boolStoreCLIValue(apply))
}

func storeUpdateWithoutApplyCommand(target string) []string {
	return managerCommand(managerStore, "update", target)
}

func boolStoreCLIValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func storeActionUnavailableResult(action string) CommandResult {
	return CommandResult{Code: 1, Command: "store " + action, Stderr: storeActionUnavailableMessage}
}

func runStoreInstallWithFallback(id string) CommandResult {
	return runStoreInstallWithFallbackContext(context.Background(), id)
}

func runStoreInstallWithFallbackContext(ctx context.Context, id string) CommandResult {
	if packageActionManagerAvailable(managerStore) {
		result := runPackageActionCommand(ctx, managerStore, packageActionTimeout, managerCommand(managerStore, "install", id)...)
		if result.OK || ctx.Err() != nil || !packageActionManagerAvailable(managerWinget) {
			return result
		}
		appLog("Store install for %q failed; trying winget msstore fallback.", id)
		fallback := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
		return mergeCommandAttemptsWithFinalResult(result, fallback, "winget msstore fallback")
	}
	if packageActionManagerAvailable(managerWinget) {
		return runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
	}
	return storeActionUnavailableResult("install")
}

func runStoreUpdateCommandWithApplyFallback(ctx context.Context, target string) CommandResult {
	result := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateCommand(target, true)...)
	result = normalizeStoreUpdateCommandResult(result)
	if result.OK || ctx.Err() != nil || !shouldRetryStoreUpdateWithoutApply(result) {
		return result
	}
	appLog("Store update command rejected --apply; retrying without that option.")
	retry := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateWithoutApplyCommand(target)...)
	retry = normalizeStoreUpdateCommandResult(retry)
	return mergeCommandAttemptsWithFinalResult(result, retry, "store update without apply flag")
}

func normalizeStoreUpdateCommandResult(result CommandResult) CommandResult {
	if result.Code == commandCancelledCode || result.Code == 124 {
		return result
	}
	output := normalizedCommandOutput(result)
	if !storeUpdateOutputIndicatesFailure(output) {
		return result
	}
	result.OK = false
	if result.Code == 0 {
		result.Code = 1
	}
	return appendCommandStderr(result, "Store CLI reported an update failure despite the process exit code.")
}

func storeUpdateOutputIndicatesFailure(output string) bool {
	return outputContainsAny(output, []string{
		"error:",
		"could not find installed product metadata",
		"failed to read input in non-interactive mode",
	})
}
