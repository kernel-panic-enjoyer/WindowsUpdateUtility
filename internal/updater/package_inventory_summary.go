package updater

import (
	"sort"
	"strings"
)

func markStoreInventoryAvailable(managers map[string]ManagerStatus) {
	store := managers[managerStore]
	store.InventoryAvailable = true
	store.InventoryBackend = inventoryBackendAppX
	if !store.Available && store.Error != "" {
		store.Error = strings.TrimSpace(store.Error + "\nStore app inventory is available through Windows AppX.")
	}
	managers[managerStore] = store
}

func sortInventoryPackages(packages []Package) {
	sort.Slice(packages, func(i, j int) bool {
		leftGroup := inventoryPackageSortGroup(packages[i])
		rightGroup := inventoryPackageSortGroup(packages[j])
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		if strings.EqualFold(packages[i].Name, packages[j].Name) {
			return packages[i].Manager < packages[j].Manager
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
}

func inventoryPackageSortGroup(pkg Package) int {
	if pkg.Manager != managerStore {
		return 0
	}
	if pkg.UpdateAvailable {
		return 0
	}
	if pkg.ActionBackend == backendAppXInventory {
		return 2
	}
	return 1
}

func inventoryScanSummary(state State, sourceCounts map[string]int) InventoryScanSummary {
	return InventoryScanSummary{
		LastScanAt:    state.LastScanAt,
		TrackedCount:  len(state.RegistryApps) + managedScanTrackedCount(state),
		RegistryCount: len(state.RegistryApps),
		WingetCount:   sourceCounts[managerWinget],
		StoreCount:    sourceCounts[managerStore],
	}
}
