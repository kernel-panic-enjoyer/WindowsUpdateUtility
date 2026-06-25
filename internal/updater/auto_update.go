package updater

import "context"

func setAutoUpdate(global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	appLog("Auto-update settings update started.")
	store, err := defaultStateStore()
	if err != nil {
		return defaultState(), validationCommandResult("auto-update settings", err)
	}
	return setAutoUpdateWithStore(context.Background(), store, global, packageKeys, packageEnabled)
}

func setAutoUpdateWithStore(ctx context.Context, store StateStore, global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	state, err := store.Update(ctx, func(state *State) error {
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
		return nil
	})
	if err != nil {
		result := validationCommandResult("auto-update settings", err)
		appLog("Auto-update settings update failed before task change: %s.", err)
		loaded, loadErr := store.Load(ctx)
		if loadErr != nil {
			return defaultState(), result
		}
		return loaded, result
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
	store, err := defaultStateStore()
	if err != nil {
		appLog("Scheduled auto-update could not open state store: %s.", err)
		return nil
	}
	return runAutoUpdateWithStore(context.Background(), store)
}

func runAutoUpdateWithStore(ctx context.Context, store StateStore) []UpdateResult {
	taskStartedAt := storeScanNow()
	state, err := store.Load(ctx)
	if err != nil {
		appLog("Scheduled auto-update could not load state: %s.", err)
		return nil
	}
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
		if _, err := store.Update(ctx, func(state *State) error {
			state.LastAutoUpdateAt = utcNow()
			state.LastAutoUpdateResults = nil
			state.LastAutoUpdateSummary = nil
			return nil
		}); err != nil {
			appLog("Could not save scheduled auto-update skip result: %s.", err)
		}
		return nil
	}
	inventory := inventoryGetter(ctx)
	// The scheduled auto-update task runs as a standalone process with no running
	// server, so the App's background Store scan never fires here. getInventory
	// only overlays the last published snapshot, so run a fresh transactional
	// Store scan inline to discover currently-available Store updates before
	// deciding what to update (matching the pre-progressive-loading behavior).
	storeProjection := applyStoreTransactionalScanPipelineResult(ctx, state, inventory, taskStartedAt)
	inventory = storeProjection.Inventory
	summary := scheduledAutoUpdateSummaryFromProjection(storeProjection)
	selectedSet := map[string]bool{}
	selectedStoreKeys := map[string]string{}
	for _, key := range selected {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized != "" {
			selectedSet[normalized] = true
			if manager, _, err := splitPackageKey(normalized); err == nil && manager == managerStore {
				selectedStoreKeys[normalized] = key
			}
		}
	}
	var results []UpdateResult
	seen := map[string]bool{}
	seenSelected := map[string]bool{}
	for _, pkg := range inventory.Packages {
		key := normalizedJobPackageKey(pkg)
		if key == "" || seen[key] || !selectedSet[normalizeAutoUpdatePackageKey(key)] {
			continue
		}
		seen[key] = true
		seenSelected[normalizeAutoUpdatePackageKey(key)] = true
		if pkg.Manager == managerStore && !storeProjection.FreshGeneration {
			reason := scheduledStoreAutoUpdateFreshnessSkipReason(storeProjection)
			appLog("Scheduled auto-update skipped %s because %s.", key, reason)
			summary.addSkippedPackage(pkg, key, reason)
			continue
		}
		if !packageAllowedInBulkUpdate(pkg, UpdateOptions{}) {
			appLog("Scheduled auto-update skipped %s because it requires explicit user confirmation or does not support updates.", key)
			if pkg.Manager == managerStore {
				summary.addSkippedPackage(pkg, key, "Store package is not currently actionable; it needs a fresh available assessment, exact target, and supported executor")
			}
			continue
		}
		pkg.Key = key
		results = append(results, UpdateResult{Key: key, Result: updatePackageWithMetadataContext(ctx, pkg)})
	}
	for normalized, original := range selectedStoreKeys {
		if seenSelected[normalized] {
			continue
		}
		summary.SkippedPackages = append(summary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
			Key:     original,
			Manager: managerStore,
			Reason:  "No installed Store package matched this auto-update preference in the effective inventory",
		})
	}
	if _, err := store.Update(ctx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = results
		state.LastAutoUpdateSummary = &summary
		return nil
	}); err != nil {
		appLog("Could not save scheduled auto-update results: %s.", err)
	}
	appLog("Scheduled auto-update task finished with %d result(s).", len(results))
	return results
}

func scheduledAutoUpdateSummaryFromProjection(projection StoreInventoryProjectionResult) ScheduledAutoUpdateSummary {
	summary := ScheduledAutoUpdateSummary{
		StoreScan: ScheduledAutoUpdateStoreScanSummary{
			ScanID:           projection.ScanID,
			UsedGenerationID: projection.UsedGenerationID,
			StartedAt:        formatStoreScanTime(projection.StartedAt),
			CompletedAt:      formatStoreScanTime(projection.CompletedAt),
			Published:        projection.Published,
			CompletionStatus: string(projection.CompletionStatus),
			FreshGeneration:  projection.FreshGeneration,
		},
	}
	if projection.Error != nil {
		summary.StoreScan.Error = sanitizeProviderDiagnostic(projection.Error.Error())
	}
	return summary
}

func scheduledStoreAutoUpdateFreshnessSkipReason(projection StoreInventoryProjectionResult) string {
	if projection.Error != nil {
		return "Store scan did not publish fresh evidence: " + sanitizeProviderDiagnostic(projection.Error.Error())
	}
	if projection.UsedGenerationID == "" {
		return "no published Store scan generation is available for this scheduled run"
	}
	if projection.CompletedAt.IsZero() {
		return "published Store scan generation has no completion time"
	}
	return "published Store scan generation did not complete after this scheduled run started"
}

func (summary *ScheduledAutoUpdateSummary) addSkippedPackage(pkg Package, key, reason string) {
	if summary == nil {
		return
	}
	summary.SkippedPackages = append(summary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
		Key:       key,
		Manager:   pkg.Manager,
		PackageID: pkg.ID,
		Reason:    sanitizeProviderDiagnostic(reason),
	})
}
