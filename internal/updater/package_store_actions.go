package updater

import (
	"context"
	"strings"
)

func storeUpdateCommand(target string, apply bool) []string {
	return managerCommand(managerStore, "update", target, "--apply", boolStoreCLIValue(apply))
}

func storeUpdateLegacyNoApplyCommand(target string) []string {
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
		return mergeCommandResults(result, fallback, "winget msstore fallback")
	}
	if packageActionManagerAvailable(managerWinget) {
		return runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
	}
	return storeActionUnavailableResult("install")
}

func runStoreUpdatePackageWithFallbackContext(ctx context.Context, pkg Package) CommandResult {
	if packageActionManagerAvailable(managerStore) {
		result := runNativeStoreUpdate(ctx, pkg)
		if result.OK || ctx.Err() != nil {
			return result
		}
		if pkg.UpdateState != "" {
			return result
		}
		if !packageActionManagerAvailable(managerWinget) {
			return result
		}
		appLog("Store update for %q failed; trying winget msstore fallback.", updateJobPackageName(pkg))
		return mergeCommandResults(result, runWingetStoreUpdateFallback(ctx, pkg), "winget msstore fallback")
	}
	if packageActionManagerAvailable(managerWinget) {
		return runWingetStoreUpdateFallback(ctx, pkg)
	}
	return storeActionUnavailableResult("update")
}

func runNativeStoreUpdate(ctx context.Context, pkg Package) CommandResult {
	candidates := storeUpdateTargetCandidates(pkg)
	return runPackageUpdateCandidates(ctx, candidates, "store target", func(target string) CommandResult {
		return runStoreUpdateCommandWithApplyFallback(ctx, target)
	})
}

func runWingetStoreUpdateFallback(ctx context.Context, pkg Package) CommandResult {
	return runWingetUpgradePackageWithInstallFallbackContext(ctx, managerStore, pkg)
}

// runStoreSearchUpdateFallback is retained only for the explicit legacy Store
// rollback path. The default Store detector and update executor must never call
// it because Store identity cannot be established from display-name search.
func runStoreSearchUpdateFallback(ctx context.Context, pkg Package, attempted []string) CommandResult {
	query := strings.TrimSpace(pkg.Name)
	if query == "" || len(query) > 160 || containsBlockedPackageActionChar(query) {
		return CommandResult{}
	}
	appLog("Store update targets for %q missed; searching Store for a fresh update target.", query)
	results, searchResult := packageActionStoreSearch(query)
	if !searchResult.OK || ctx.Err() != nil {
		return searchResult
	}
	match, ok := chooseStoreResolution(pkg, results)
	if !ok {
		return CommandResult{Command: searchResult.Command, Code: 1, Stderr: "Store search did not return a confident update target."}
	}
	target := strings.TrimSpace(match.ID)
	if target == "" {
		target = strings.TrimSpace(match.Name)
	}
	if target == "" || updateTargetAlreadyAttempted(target, attempted) {
		return CommandResult{Command: searchResult.Command, Code: 1, Stderr: "Store search returned no new update target."}
	}
	updateResult := runStoreUpdateCommandWithApplyFallback(ctx, target)
	return mergeCommandResults(searchResult, updateResult, "store search resolved update")
}

func runStoreUpdateCommandWithApplyFallback(ctx context.Context, target string) CommandResult {
	result := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateCommand(target, true)...)
	result = normalizeStoreUpdateCommandResult(result)
	if result.OK || ctx.Err() != nil || !shouldRetryStoreUpdateWithoutApply(result) {
		return result
	}
	appLog("Store update command rejected --apply; retrying without that option.")
	retry := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateLegacyNoApplyCommand(target)...)
	retry = normalizeStoreUpdateCommandResult(retry)
	return mergeCommandResults(result, retry, "store update without apply flag")
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

func updateTargetAlreadyAttempted(target string, attempted []string) bool {
	for _, existing := range attempted {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
