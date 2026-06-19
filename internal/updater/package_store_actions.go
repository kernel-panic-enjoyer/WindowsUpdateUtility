package updater

import (
	"context"
	"strings"
)

func storeUpdateCommand(target string, apply bool) []string {
	args := []string{"update", target}
	if apply {
		args = append(args, "--apply", "true")
	}
	return managerCommand(managerStore, args...)
}

func storeActionUnavailableResult(action string) CommandResult {
	return CommandResult{Code: 1, Command: "store " + action, Stderr: storeActionUnavailableMessage}
}

func runStoreInstallWithFallback(id string) CommandResult {
	if packageActionManagerAvailable(managerStore) {
		result := runPackageActionCommand(context.Background(), managerStore, packageActionTimeout, managerCommand(managerStore, "install", id)...)
		if result.OK || !packageActionManagerAvailable(managerWinget) {
			return result
		}
		appLog("Store install for %q failed; trying winget msstore fallback.", id)
		fallback := runPackageActionCommand(context.Background(), managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
		return mergeCommandResults(result, fallback, "winget msstore fallback")
	}
	if packageActionManagerAvailable(managerWinget) {
		return runPackageActionCommand(context.Background(), managerWinget, packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
	}
	return storeActionUnavailableResult("install")
}

func runStoreUpdatePackageWithFallbackContext(ctx context.Context, pkg Package) CommandResult {
	if packageActionManagerAvailable(managerStore) {
		result := runNativeStoreUpdate(ctx, pkg)
		if result.OK || ctx.Err() != nil {
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
	result := runPackageUpdateCandidates(ctx, candidates, "store target", func(target string) CommandResult {
		return runStoreUpdateCommandWithApplyFallback(ctx, target)
	})
	if result.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(result) {
		return result
	}
	searchFallback := runStoreSearchUpdateFallback(ctx, pkg, candidates)
	if searchFallback.Command == "" {
		return result
	}
	return mergeCommandResults(result, searchFallback, "store search fallback")
}

func runWingetStoreUpdateFallback(ctx context.Context, pkg Package) CommandResult {
	return runWingetUpgradePackageWithInstallFallbackContext(ctx, managerStore, pkg)
}

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
	if result.OK || ctx.Err() != nil || !shouldRetryStoreUpdateWithoutApply(result) {
		return result
	}
	appLog("Store update command rejected --apply; retrying without that option.")
	retry := runPackageActionCommand(ctx, managerStore, packageActionTimeout, storeUpdateCommand(target, false)...)
	return mergeCommandResults(result, retry, "store update without apply flag")
}

func updateTargetAlreadyAttempted(target string, attempted []string) bool {
	for _, existing := range attempted {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
