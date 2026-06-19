package updater

func mergeAppxInventoryPackages(
	state *State,
	managers map[string]ManagerStatus,
	commandResults map[string]CommandResult,
	packages []Package,
	appxPackages []Package,
	storeUpdateVersions map[string]string,
) []Package {
	markStoreInventoryAvailable(managers)
	if managers[managerStore].Available {
		var resolveResults map[string]CommandResult
		var changed bool
		appxPackages, resolveResults, changed = resolveStoreAppxPackages(state, appxPackages, true, storeSearch)
		for key, result := range resolveResults {
			commandResults[key] = result
		}
		if changed {
			if err := saveAppState(*state); err != nil {
				commandResults["store_resolve_cache_save"] = validationCommandResult("store resolve cache save", err)
				appLog("Store resolver could not save cache: %s.", err)
			}
		}
	}
	for i := range appxPackages {
		appxPackages[i] = applyStoreUpdateVersion(appxPackages[i], storeUpdateVersions, managers[managerStore].Available)
		appxPackages[i].Key = packageKey(managerStore, appxPackages[i].ID)
		appxPackages[i].Installed = true
		if appxPackages[i].UpdateSupported {
			appxPackages[i].AutoUpdate = packageAutoUpdateEnabled(*state, appxPackages[i])
		}
	}
	return mergeStoreAppxPackages(packages, appxPackages)
}
