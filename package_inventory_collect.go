package main

import "sync"

type managerInventoryCollector struct {
	manager   string
	installed func() ([]Package, CommandResult)
	updates   func() (map[string]string, CommandResult)
	listKey   string
	updateKey string
}

var managerInventoryCollectors = []managerInventoryCollector{
	{managerWinget, wingetInstalled, wingetUpdates, "winget_list", "winget_upgrade"},
	{managerChoco, chocoInstalled, chocoUpdates, "choco_list", "choco_outdated"},
}

func collectManagerInventory(
	manager string,
	installedFn func() ([]Package, CommandResult),
	updatesFn func() (map[string]string, CommandResult),
	listKey string,
	updateKey string,
) managerInventory {
	var installed []Package
	var listResult CommandResult
	var updates map[string]string
	var updateResult CommandResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		installed, listResult = installedFn()
	}()
	go func() {
		defer wg.Done()
		updates, updateResult = updatesFn()
	}()
	wg.Wait()
	return managerInventory{
		manager:      manager,
		installed:    installed,
		listResult:   listResult,
		updates:      updates,
		updateResult: updateResult,
		listKey:      listKey,
		updateKey:    updateKey,
	}
}

func collectInventoryInputs(managers map[string]ManagerStatus) inventoryInputs {
	inputs := inventoryInputs{}
	inventoryCh := make(chan managerInventory, len(managedPackageManagers))
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		inputs.appxPackages, inputs.appxResult = appxInstalled()
	}()
	if managers[managerStore].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inputs.nativeStoreInstalled, inputs.nativeStoreInstalledResult = storeInstalled()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			inputs.nativeStoreUpdates, inputs.nativeStoreUpdatePackages, inputs.nativeStoreUpdatesResult = storeUpdates()
		}()
	}
	for _, collector := range managerInventoryCollectors {
		if !managers[collector.manager].Available {
			continue
		}
		collector := collector
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory(collector.manager, collector.installed, collector.updates, collector.listKey, collector.updateKey)
		}()
	}

	wg.Wait()
	close(inventoryCh)
	for inventory := range inventoryCh {
		inputs.managerInventories = append(inputs.managerInventories, inventory)
	}
	return inputs
}
