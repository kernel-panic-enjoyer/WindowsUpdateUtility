package updater

import "context"

func setAutoUpdate(global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	appLog("Auto-update settings update started.")
	state := loadState()
	if state.AutoUpdatePackages == nil {
		state.AutoUpdatePackages = map[string]bool{}
	}
	if global != nil {
		state.AutoUpdateGlobal = *global
	}
	if packageEnabled != nil {
		for _, key := range packageKeys {
			if _, _, err := splitPackageKey(key); err == nil {
				normalized := normalizeAutoUpdatePackageKey(key)
				if normalized == "" {
					appLog("Auto-update package key ignored because it is not an exact canonical target: %s.", key)
					continue
				}
				state.AutoUpdatePackages[normalized] = *packageEnabled
			}
		}
	}
	if err := saveAppState(state); err != nil {
		result := validationCommandResult("auto-update settings", err)
		appLog("Auto-update settings update failed before task change: %s.", err)
		return loadState(), result
	}
	var result CommandResult
	if state.AutoUpdateGlobal {
		result = createAutoUpdateTaskRunner()
	} else {
		result = deleteTaskRunner(taskAutoUpdate)
	}
	appLog("Auto-update settings update finished with code %d.", result.Code)
	return state, result
}

func runAutoUpdate() []UpdateResult {
	appLog("Scheduled auto-update task started.")
	state := loadState()
	if !state.AutoUpdateGlobal {
		appLog("Scheduled auto-update skipped because global auto-update is disabled.")
		return nil
	}
	var selected []string
	for key, enabled := range state.AutoUpdatePackages {
		if enabled {
			selected = append(selected, key)
		}
	}
	if len(selected) == 0 {
		appLog("Scheduled auto-update skipped because no packages are opted in.")
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = nil
		if err := saveAppState(state); err != nil {
			appLog("Could not save scheduled auto-update skip result: %s.", err)
		}
		return nil
	}
	inventory := inventoryGetter()
	// The scheduled auto-update task runs as a standalone process with no running
	// server, so the App's background Store scan never fires here. getInventory
	// only overlays the last published snapshot, so run a fresh transactional
	// Store scan inline to discover currently-available Store updates before
	// deciding what to update (matching the pre-progressive-loading behavior).
	inventory = applyStoreTransactionalScanPipeline(context.Background(), state, inventory)
	selectedSet := map[string]bool{}
	for _, key := range selected {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized != "" {
			selectedSet[normalized] = true
		}
	}
	var results []UpdateResult
	seen := map[string]bool{}
	for _, pkg := range inventory.Packages {
		key := normalizedJobPackageKey(pkg)
		if key == "" || seen[key] || !selectedSet[normalizeAutoUpdatePackageKey(key)] {
			continue
		}
		seen[key] = true
		if !packageAllowedInBulkUpdate(pkg, UpdateOptions{}) {
			appLog("Scheduled auto-update skipped %s because it requires explicit user confirmation or does not support updates.", key)
			continue
		}
		pkg.Key = key
		results = append(results, UpdateResult{Key: key, Result: updatePackageWithMetadataContext(context.Background(), pkg)})
	}
	state.LastAutoUpdateAt = utcNow()
	state.LastAutoUpdateResults = results
	if err := saveAppState(state); err != nil {
		appLog("Could not save scheduled auto-update results: %s.", err)
	}
	appLog("Scheduled auto-update task finished with %d result(s).", len(results))
	return results
}
