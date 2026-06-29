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
	state := loadStateContext(ctx)
	managers := detectManagersContext(ctx)
	commandResults := map[string]CommandResult{}
	var packages []Package
	storeUpdateVersions := map[string]string{}

	inputs := collectInventoryInputs(ctx, managers)
	commandResults["appx_inventory"] = inputs.appxResult
	if inputs.storePackagedResult.Command != "" {
		commandResults["native_store_inventory"] = inputs.storePackagedResult
	}
	for _, inventory := range inputs.managerInventories {
		commandResults[inventory.listKey] = inventory.listResult
		commandResults[inventory.updateKey] = inventory.updateResult
		packages = append(packages, packagesFromManagerInventory(state, inventory)...)
	}

	if inputs.appxResult.OK || len(inputs.appxPackages) > 0 {
		packages = mergeAppxInventoryPackages(&state, managers, commandResults, packages, inputs.appxPackages, storeUpdateVersions)
	}

	sourceCounts := managedScanSourceCounts(state)
	inventory := Inventory{
		PackageLookup: PackageLookup{
			Packages:       packages,
			Managers:       managers,
			CommandResults: commandResults,
		},
		Scan: inventoryScanSummary(state, sourceCounts),
	}
	sortInventoryPackages(inventory.Packages)
	return inventory
}
