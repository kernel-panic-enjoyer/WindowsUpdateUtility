package updater

import "context"

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
	managerInventories     []managerInventory
	appxPackages           []Package
	appxResult             CommandResult
	storePackagedInventory StorePackagedAppInventory
	storePackagedResult    CommandResult
}

var inventoryGetter = getInventoryContext

func getInventory() Inventory {
	return getInventoryContext(context.Background())
}

func getInventoryContext(ctx context.Context) Inventory {
	persistedState := loadStateContext(ctx)
	managerStatuses := detectManagersContext(ctx)
	commandResultsByKey := map[string]CommandResult{}
	var inventoryPackages []Package

	collectedInputs := collectInventoryInputs(ctx, managerStatuses)
	commandResultsByKey["appx_inventory"] = collectedInputs.appxResult
	if collectedInputs.storePackagedResult.Command != "" {
		commandResultsByKey["native_store_inventory"] = collectedInputs.storePackagedResult
	}
	for _, managerSnapshot := range collectedInputs.managerInventories {
		commandResultsByKey[managerSnapshot.listKey] = managerSnapshot.listResult
		commandResultsByKey[managerSnapshot.updateKey] = managerSnapshot.updateResult
		inventoryPackages = append(inventoryPackages, packagesFromManagerInventory(persistedState, managerSnapshot)...)
	}

	if collectedInputs.appxResult.OK || len(collectedInputs.appxPackages) > 0 {
		inventoryPackages = mergeAppxInventoryPackages(
			&persistedState,
			managerStatuses,
			commandResultsByKey,
			inventoryPackages,
			collectedInputs.appxPackages,
			map[string]string{},
		)
	}

	managedSourceCounts := managedScanSourceCounts(persistedState)
	result := Inventory{
		PackageLookup: PackageLookup{
			Packages:       inventoryPackages,
			Managers:       managerStatuses,
			CommandResults: commandResultsByKey,
		},
		Scan: inventoryScanSummary(persistedState, managedSourceCounts),
	}
	sortInventoryPackages(result.Packages)
	return result
}
