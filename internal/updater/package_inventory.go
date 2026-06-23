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
	managerInventories      []managerInventory
	appxPackages            []Package
	appxResult              CommandResult
	storePackagedInventory  StorePackagedAppInventory
	storePackagedResult     CommandResult
	storePackagedComparison StorePackagedInventoryComparison
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
	if inputs.storePackagedResult.Command != "" {
		commandResults["native_store_inventory"] = inputs.storePackagedResult
	}
	if len(inputs.storePackagedComparison.MissingNativePFNs) > 0 ||
		len(inputs.storePackagedComparison.MissingLegacyPFNs) > 0 ||
		len(inputs.storePackagedComparison.VersionDifferences) > 0 ||
		len(inputs.storePackagedComparison.ScopeDifferences) > 0 ||
		len(inputs.storePackagedComparison.ClassificationNotes) > 0 ||
		len(inputs.storePackagedComparison.NativeErrors) > 0 {
		commandResults["native_store_inventory_compare"] = storePackagedInventoryComparisonResult(inputs.storePackagedComparison)
	}
	for _, inventory := range inputs.managerInventories {
		commandResults[inventory.listKey] = inventory.listResult
		commandResults[inventory.updateKey] = inventory.updateResult
		packages = append(packages, packagesFromManagerInventory(state, managers, inventory)...)
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
	// Overlay only the LAST PUBLISHED Store scan here (a fast, read-only disk
	// read). The expensive fresh Store scan is run separately in the background
	// by the App layer so the fast managers (winget, choco) are returned without
	// waiting on the slow Microsoft Store providers. inventorySnapshot re-applies
	// the published overlay on every read, so the latest Store generation always
	// surfaces once a background scan completes.
	inventory = applyPublishedStoreScanAssessments(context.Background(), state, inventory)
	sortInventoryPackages(inventory.Packages)
	return inventory
}
