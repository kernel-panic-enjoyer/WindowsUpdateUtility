package updater

import (
	"context"
	"strings"
)

var updatePackageRunner = updatePackageWithMetadataContext
var updateRetryInventoryRefresher = func(ctx context.Context, app *App, reason string) Inventory {
	return app.refreshInventorySyncContext(ctx, reason)
}

func (app *App) updatePackageWithInventoryRetry(ctx context.Context, pkg Package) CommandResult {
	result := updatePackageRunner(ctx, pkg)
	if result.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(result) {
		return result
	}

	inventory := updateRetryInventoryRefresher(ctx, app, "update retry")
	fresh, ok := findPackageForUpdateRetry(inventory.Packages, pkg)
	if !ok {
		return result
	}
	fresh.AllowUnknownVersionUpdate = pkg.AllowUnknownVersionUpdate
	fresh.AllowPinnedUpdate = pkg.AllowPinnedUpdate
	if !fresh.UpdateAvailable {
		noUpdate := CommandResult{
			OK:      true,
			Command: "inventory refresh",
			Stdout:  "Inventory refresh no longer reports an available update for " + updateJobPackageName(pkg) + ".",
		}
		return mergeCommandAttemptsWithFinalResult(result, noUpdate, "fresh inventory no-update")
	}
	if samePackageUpdateTarget(pkg, fresh) {
		return result
	}
	appLog("Update target for %q failed; refreshed inventory found %q. Retrying once with fresh metadata.", updateJobPackageName(pkg), updateJobPackageName(fresh))
	retry := updatePackageRunner(ctx, fresh)
	return mergeCommandAttemptsWithFinalResult(result, retry, "fresh inventory retry")
}

func findPackageForUpdateRetry(packages []Package, original Package) (Package, bool) {
	originalKey := normalizedJobPackageKey(original)
	originalKeyNormalized := normalizeAutoUpdatePackageKey(originalKey)
	originalID := strings.TrimSpace(original.ID)
	originalPFN := strings.TrimSpace(storeInstalledPackageFamilyName(original))
	if original.Manager == managerStore {
		for i := range packages {
			pkg := packages[i]
			if pkg.Manager != managerStore {
				continue
			}
			key := normalizedJobPackageKey(pkg)
			keyNormalized := normalizeAutoUpdatePackageKey(key)
			if originalKey != "" && (strings.EqualFold(key, originalKey) || strings.EqualFold(keyNormalized, originalKeyNormalized)) {
				return pkg, true
			}
			if originalPFN != "" && strings.EqualFold(storeInstalledPackageFamilyName(pkg), originalPFN) {
				return pkg, true
			}
		}
		return Package{}, false
	}
	originalName := normalizePackageIdentity(original.Name)
	originalMatch := normalizePackageIdentity(original.Match)
	originalStableID := normalizePackageIdentity(stableStoreActionID(original.ID))

	var nameMatch *Package
	for i := range packages {
		pkg := packages[i]
		if pkg.Manager != original.Manager {
			continue
		}
		key := normalizedJobPackageKey(pkg)
		keyNormalized := normalizeAutoUpdatePackageKey(key)
		if originalKey != "" && (strings.EqualFold(key, originalKey) || strings.EqualFold(keyNormalized, originalKeyNormalized) || equivalentPackageKeys(key, originalKey)) {
			return pkg, true
		}
		if originalID != "" && strings.EqualFold(pkg.ID, originalID) {
			return pkg, true
		}
		for _, value := range []string{pkg.ID, pkg.Match} {
			normalized := normalizePackageIdentity(value)
			if normalized == "" {
				continue
			}
			if normalized == originalMatch || normalized == originalStableID {
				return pkg, true
			}
		}
		if originalName != "" && len(originalName) >= 5 && normalizePackageIdentity(pkg.Name) == originalName {
			candidate := pkg
			nameMatch = &candidate
		}
	}
	if nameMatch != nil {
		return *nameMatch, true
	}
	return Package{}, false
}

func samePackageUpdateTarget(left, right Package) bool {
	return strings.EqualFold(strings.TrimSpace(left.Manager), strings.TrimSpace(right.Manager)) &&
		strings.EqualFold(strings.TrimSpace(left.ID), strings.TrimSpace(right.ID)) &&
		strings.EqualFold(strings.TrimSpace(left.Match), strings.TrimSpace(right.Match)) &&
		strings.EqualFold(strings.TrimSpace(left.ActionBackend), strings.TrimSpace(right.ActionBackend))
}
