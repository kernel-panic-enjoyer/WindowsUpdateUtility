package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type StoreResolveCacheEntry struct {
	AppXVersion string `json:"appx_version"`
	StoreID     string `json:"store_id,omitempty"`
	StoreName   string `json:"store_name,omitempty"`
	Resolved    bool   `json:"resolved"`
	ResolvedAt  string `json:"resolved_at"`
}

type State struct {
	CreatedAt             string                            `json:"created_at"`
	UpdatedAt             string                            `json:"updated_at"`
	AutoUpdateGlobal      bool                              `json:"auto_update_global"`
	AutoUpdatePackages    map[string]bool                   `json:"auto_update_packages"`
	RegistryApps          map[string]ScannedApp             `json:"registry_apps"`
	WingetApps            map[string]ScannedApp             `json:"winget_apps"`
	StoreResolveCache     map[string]StoreResolveCacheEntry `json:"store_resolve_cache"`
	LastScanAt            string                            `json:"last_scan_at"`
	LastAutoUpdateAt      string                            `json:"last_auto_update_at"`
	LastAutoUpdateResults []UpdateResult                    `json:"last_auto_update_results"`
	Theme                 string                            `json:"theme"`
}

func utcNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func appRoot() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func stateDir() (string, error) {
	if override := os.Getenv("UPDATER_STATE_DIR"); override != "" {
		if err := os.MkdirAll(override, 0o755); err != nil {
			return "", err
		}
		if !canWriteDir(override) {
			return "", fmt.Errorf("state directory is not writable: %s", override)
		}
		return override, nil
	}

	var candidates []string
	for _, env := range []string{"LOCALAPPDATA", "APPDATA", "USERPROFILE", "ProgramData"} {
		if value := os.Getenv(env); value != "" {
			candidates = append(candidates, filepath.Join(value, appDirName))
		}
	}
	candidates = append(candidates, filepath.Join(appRoot(), ".state"))

	for _, candidate := range candidates {
		if err := os.MkdirAll(candidate, 0o755); err == nil && canWriteDir(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("could not create a state directory")
}

func canWriteDir(dir string) bool {
	path := filepath.Join(dir, fmt.Sprintf(".write-test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}

func defaultState() State {
	now := utcNow()
	return State{
		CreatedAt:          now,
		UpdatedAt:          now,
		AutoUpdatePackages: map[string]bool{},
		RegistryApps:       map[string]ScannedApp{},
		WingetApps:         map[string]ScannedApp{},
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
	if state.RegistryApps == nil {
		state.RegistryApps = map[string]ScannedApp{}
	}
	if state.WingetApps == nil {
		state.WingetApps = map[string]ScannedApp{}
	}
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
