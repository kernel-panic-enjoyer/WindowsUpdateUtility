package updater

type managerInventory struct {
	manager       string
	installed     []Package
	listResult    CommandResult
	updates       map[string]string
	updateDetails map[string]Package
	updateResult  CommandResult
	listKey       string
	updateKey     string
}

type inventoryInputs struct {
	managerInventories         []managerInventory
	appxPackages               []Package
	appxResult                 CommandResult
	nativeStoreInstalled       []Package
	nativeStoreInstalledResult CommandResult
	nativeStoreUpdates         map[string]string
	nativeStoreUpdatePackages  []Package
	nativeStoreUpdatesResult   CommandResult
}

var inventoryGetter = getInventory

func getInventory() Inventory {
	state := loadState()
	managers := detectManagers()
	commandResults := map[string]CommandResult{}
	var packages []Package
	storeUpdateVersions := map[string]string{}

	inputs := collectInventoryInputs(managers)
	commandResults["appx_inventory"] = inputs.appxResult
	if inputs.nativeStoreInstalledResult.Command != "" {
		commandResults["store_installed"] = inputs.nativeStoreInstalledResult
	}
	if inputs.nativeStoreUpdatesResult.Command != "" {
		commandResults["store_updates"] = inputs.nativeStoreUpdatesResult
		mergeUpdateVersions(storeUpdateVersions, inputs.nativeStoreUpdates)
	}

	for _, inventory := range inputs.managerInventories {
		commandResults[inventory.listKey] = inventory.listResult
		commandResults[inventory.updateKey] = inventory.updateResult
		if inventory.manager == managerWinget {
			mergeWingetStoreUpdateVersions(storeUpdateVersions, inventory.updates)
		}
		packages = append(packages, packagesFromManagerInventory(state, managers, inventory)...)
	}
	if managers[managerStore].Available {
		packages = append(packages, packagesFromNativeStoreInstalled(state, inputs.nativeStoreInstalled)...)
		packages = mergeStoreNativeUpdatePackages(packages, packagesFromNativeStoreUpdates(state, inputs.nativeStoreUpdatePackages))
	}

	if inputs.appxResult.OK || len(inputs.appxPackages) > 0 {
		packages = mergeAppxInventoryPackages(&state, managers, commandResults, packages, inputs.appxPackages, storeUpdateVersions)
	}

	sortInventoryPackages(packages)

	sourceCounts := managedScanSourceCounts(state)
	return Inventory{
		PackageLookup: PackageLookup{
			Packages:       packages,
			Managers:       managers,
			CommandResults: commandResults,
		},
		Scan: inventoryScanSummary(state, sourceCounts),
	}
}
