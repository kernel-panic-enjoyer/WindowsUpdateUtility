package updater

import "strings"

func migrateStoreScanApps(state *State) {
	for key, app := range state.WingetApps {
		if !isStoreScannedApp(app) {
			continue
		}
		if app.Source == "" || app.Source == "msstore" || app.Source == "appx" {
			app.Source = "store"
		}
		if app.Manager == "" {
			app.Manager = "store"
		}
		state.StoreApps[key] = app
		delete(state.WingetApps, key)
	}
	normalizeStoreScanAppKeys(state)
}

func normalizeStoreScanAppKeys(state *State) {
	normalized := map[string]ScannedApp{}
	for key, app := range state.StoreApps {
		app.Source = "store"
		if app.Manager == "" {
			app.Manager = "store"
		}
		stableID := stableScannedStoreAppID(key, app)
		if stableID != "" {
			app.Key = "store:" + strings.ToLower(stableID)
			app.PackageID = stableID
		} else if app.Key == "" {
			app.Key = key
		}
		if existing, ok := normalized[app.Key]; ok && existing.FirstSeen != "" && (app.FirstSeen == "" || existing.FirstSeen < app.FirstSeen) {
			app.FirstSeen = existing.FirstSeen
		}
		normalized[app.Key] = app
	}
	state.StoreApps = normalized
}

func normalizeAutoUpdatePackageKeys(state *State) {
	normalized := map[string]bool{}
	for key, enabled := range state.AutoUpdatePackages {
		normalizedKey := normalizeAutoUpdatePackageKey(key)
		if normalizedKey == "" {
			normalizedKey = key
		}
		normalized[normalizedKey] = normalized[normalizedKey] || enabled
	}
	state.AutoUpdatePackages = normalized
}

func normalizeAutoUpdatePackageKey(key string) string {
	manager, id, err := splitPackageKey(key)
	if err != nil {
		return key
	}
	if manager == managerStore {
		id = stableStoreActionID(id)
	}
	return packageKey(manager, id)
}
