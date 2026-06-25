package updater

import (
	"context"
	"sync"
)

type managerInventoryCollector struct {
	manager   string
	installed func(context.Context) ([]Package, CommandResult)
	updates   func(context.Context) (map[string]string, map[string]Package, CommandResult)
	listKey   string
	updateKey string
}

var managerInventoryCollectors = []managerInventoryCollector{
	{managerWinget, wingetInstalledContext, wingetUpdatesContext, "winget_list", "winget_upgrade"},
	{managerChoco, chocoInstalledContext, chocoUpdatesContext, "choco_list", "choco_outdated"},
}

func collectManagerInventory(
	ctx context.Context,
	manager string,
	installedFn func(context.Context) ([]Package, CommandResult),
	updatesFn func(context.Context) (map[string]string, map[string]Package, CommandResult),
	listKey string,
	updateKey string,
) managerInventory {
	var installed []Package
	var listResult CommandResult
	var updates map[string]string
	var updateDetails map[string]Package
	var updateResult CommandResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		installed, listResult = installedFn(ctx)
	}()
	go func() {
		defer wg.Done()
		updates, updateDetails, updateResult = updatesFn(ctx)
	}()
	wg.Wait()
	return managerInventory{
		manager:       manager,
		installed:     installed,
		listResult:    listResult,
		updates:       updates,
		updateDetails: updateDetails,
		updateResult:  updateResult,
		listKey:       listKey,
		updateKey:     updateKey,
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
	for _, collector := range managerInventoryCollectors {
		if !managers[collector.manager].Available {
			continue
		}
		collector := collector
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory(ctx, collector.manager, collector.installed, collector.updates, collector.listKey, collector.updateKey)
		}()
	}

	wg.Wait()
	if inputs.storePackagedResult.OK {
		state := loadStateContext(ctx)
		inputs.appxPackages = packagesFromNativeStorePackagedInventory(state, inputs.storePackagedInventory)
		inputs.appxResult = inputs.storePackagedResult
	} else {
		inputs.appxPackages = nil
		inputs.appxResult = inputs.storePackagedResult
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
