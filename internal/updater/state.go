package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type StoreResolveCacheEntry struct {
	AppXVersion  string `json:"appx_version"`
	StoreID      string `json:"store_id,omitempty"`
	StoreName    string `json:"store_name,omitempty"`
	StoreVersion string `json:"store_version,omitempty"`
	Resolved     bool   `json:"resolved"`
	ResolvedAt   string `json:"resolved_at"`
}

type State struct {
	CreatedAt             string                            `json:"created_at"`
	UpdatedAt             string                            `json:"updated_at"`
	AutoUpdateGlobal      bool                              `json:"auto_update_global"`
	AutoUpdatePackages    map[string]bool                   `json:"auto_update_packages"`
	RegistryApps          map[string]ScannedApp             `json:"registry_apps"`
	WingetApps            map[string]ScannedApp             `json:"winget_apps"`
	StoreApps             map[string]ScannedApp             `json:"store_apps"`
	StoreResolveCache     map[string]StoreResolveCacheEntry `json:"store_resolve_cache"`
	LastScanAt            string                            `json:"last_scan_at"`
	LastAutoUpdateAt      string                            `json:"last_auto_update_at"`
	LastAutoUpdateResults []UpdateResult                    `json:"last_auto_update_results"`
	Theme                 string                            `json:"theme"`
}

func utcNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func defaultState() State {
	now := utcNow()
	return State{
		CreatedAt:          now,
		UpdatedAt:          now,
		AutoUpdatePackages: map[string]bool{},
		RegistryApps:       map[string]ScannedApp{},
		WingetApps:         map[string]ScannedApp{},
		StoreApps:          map[string]ScannedApp{},
		StoreResolveCache:  map[string]StoreResolveCacheEntry{},
		Theme:              "dark",
	}
}

func loadState() State {
	state := defaultState()
	dir, err := stateDir()
	if err != nil {
		return state
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return defaultState()
	}
	if state.AutoUpdatePackages == nil {
		state.AutoUpdatePackages = map[string]bool{}
	}
	normalizeAutoUpdatePackageKeys(&state)
	if state.RegistryApps == nil {
		state.RegistryApps = map[string]ScannedApp{}
	}
	if state.WingetApps == nil {
		state.WingetApps = map[string]ScannedApp{}
	}
	if state.StoreApps == nil {
		state.StoreApps = map[string]ScannedApp{}
	}
	migrateStoreScanApps(&state)
	if state.StoreResolveCache == nil {
		state.StoreResolveCache = map[string]StoreResolveCacheEntry{}
	}
	if state.Theme == "" {
		state.Theme = "dark"
	}
	return state
}

func saveState(state State) error {
	state.UpdatedAt = utcNow()
	dir, err := stateDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("state-%d-%d.tmp", os.Getpid(), time.Now().UnixNano()))
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tmp, path); retryErr != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

var saveAppState = saveState
