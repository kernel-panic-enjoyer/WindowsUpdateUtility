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
		if strings.EqualFold(packages[i].Name, packages[j].Name) {
			return packages[i].Manager < packages[j].Manager
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
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
