package updater

import (
	"context"
	"sync"
)

type packageManagerInventoryCollector struct {
	manager          string
	collectInstalled func(context.Context) ([]Package, CommandResult)
	collectUpdates   func(context.Context) (map[string]string, map[string]Package, CommandResult)
	listResultKey    string
	updateResultKey  string
}

var packageManagerInventoryCollectors = []packageManagerInventoryCollector{
	{
		manager:          managerWinget,
		collectInstalled: wingetInstalledContext,
		collectUpdates:   wingetUpdatesContext,
		listResultKey:    "winget_list",
		updateResultKey:  "winget_upgrade",
	},
	{
		manager:          managerChoco,
		collectInstalled: chocoInstalledContext,
		collectUpdates:   chocoUpdatesContext,
		listResultKey:    "choco_list",
		updateResultKey:  "choco_outdated",
	},
}

func collectManagerInventory(
	ctx context.Context,
	manager string,
	collectInstalled func(context.Context) ([]Package, CommandResult),
	collectUpdates func(context.Context) (map[string]string, map[string]Package, CommandResult),
	listResultKey string,
	updateResultKey string,
) managerInventory {
	var installedPackages []Package
	var listResult CommandResult
	var availableVersions map[string]string
	var updateDetailsByKey map[string]Package
	var updateResult CommandResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		installedPackages, listResult = collectInstalled(ctx)
	}()
	go func() {
		defer wg.Done()
		availableVersions, updateDetailsByKey, updateResult = collectUpdates(ctx)
	}()
	wg.Wait()
	return managerInventory{
		manager:       manager,
		installed:     installedPackages,
		listResult:    listResult,
		updates:       availableVersions,
		updateDetails: updateDetailsByKey,
		updateResult:  updateResult,
		listKey:       listResultKey,
		updateKey:     updateResultKey,
	}
}

func collectInventoryInputs(ctx context.Context, managers map[string]ManagerStatus) inventoryInputs {
	inputs := inventoryInputs{}
	inventoryCh := make(chan managerInventory, len(managedPackageManagers))
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		inputs.storePackagedInventory, inputs.storePackagedResult = collectNativeStorePackagedInventoryContext(ctx)
	}()
	for _, collector := range packageManagerInventoryCollectors {
		if !managers[collector.manager].Available {
			continue
		}
		activeCollector := collector
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory(
				ctx,
				activeCollector.manager,
				activeCollector.collectInstalled,
				activeCollector.collectUpdates,
				activeCollector.listResultKey,
				activeCollector.updateResultKey,
			)
		}()
	}

	wg.Wait()
	inputs.appxResult = inputs.storePackagedResult
	if inputs.storePackagedResult.OK {
		state := loadStateContext(ctx)
		inputs.appxPackages = packagesFromNativeStorePackagedInventory(state, inputs.storePackagedInventory)
	}
	close(inventoryCh)
	for inventory := range inventoryCh {
		inputs.managerInventories = append(inputs.managerInventories, inventory)
	}
	return inputs
}

func collectNativeStorePackagedInventory() (StorePackagedAppInventory, CommandResult) {
	return collectNativeStorePackagedInventoryContext(context.Background())
}

func collectNativeStorePackagedInventoryContext(ctx context.Context) (StorePackagedAppInventory, CommandResult) {
	userSID, err := currentUserSID()
	if err != nil {
		result := validationCommandResult("native Store inventory", err)
		return StorePackagedAppInventory{Partial: true, Errors: []string{err.Error()}}, result
	}
	scan := newStorePackagedAppScan(userSID)
	return storePackagedAppInventoryProvider().Inventory(ctx, scan)
}
