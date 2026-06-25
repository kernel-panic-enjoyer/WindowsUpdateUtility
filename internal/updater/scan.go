package updater

import (
	"context"
	"sort"
	"strings"
	"sync"
)

type ScannedApp struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Publisher       string `json:"publisher"`
	InstallLocation string `json:"install_location"`
	Source          string `json:"source"`
	RegistryHive    string `json:"registry_hive,omitempty"`
	Manager         string `json:"manager,omitempty"`
	PackageID       string `json:"package_id,omitempty"`
	FirstSeen       string `json:"first_seen"`
}

type ScanResult struct {
	LastScanAt      string              `json:"last_scan_at"`
	Baseline        bool                `json:"baseline"`
	Baselines       map[string]bool     `json:"baselines"`
	NewApps         []ScannedApp        `json:"new_apps"`
	RemovedApps     []ScannedApp        `json:"removed_apps"`
	TrackedCount    int                 `json:"tracked_count"`
	SourceCounts    map[string]int      `json:"source_counts"`
	WingetAvailable bool                `json:"winget_available"`
	StoreAvailable  bool                `json:"store_available"`
	WingetResult    *CommandResult      `json:"winget_result,omitempty"`
	Errors          []map[string]string `json:"errors"`
}

var registryAppsReader = readRegistryApps
var wingetAppsReader = readWingetAppsContext
var appxAppsReader = readAppxAppsContext

func scanInstalledApplications() ScanResult {
	appLog("Application scan started.")
	store, err := defaultStateStore()
	if err != nil {
		appLog("Application scan could not open state store: %s.", err)
		return ScanResult{Errors: []map[string]string{{"source": "state", "error": err.Error()}}}
	}
	return scanInstalledApplicationsWithStore(context.Background(), store)
}

func scanInstalledApplicationsWithStore(ctx context.Context, store StateStore) ScanResult {
	var errorsOut []map[string]string
	var registryApps []ScannedApp
	var wingetApps []ScannedApp
	var appxApps []ScannedApp
	var wingetResult *CommandResult
	var appxResult *CommandResult
	var registryErr error
	var wingetErr error
	var appxErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		registryApps, registryErr = registryAppsReader()
	}()
	go func() {
		defer wg.Done()
		wingetApps, wingetResult, wingetErr = wingetAppsReader(ctx)
	}()
	go func() {
		defer wg.Done()
		appxApps, appxResult, appxErr = appxAppsReader(ctx)
	}()
	wg.Wait()
	if registryErr != nil {
		errorsOut = append(errorsOut, map[string]string{"source": "registry", "error": registryErr.Error()})
	}
	if wingetErr != nil {
		errorsOut = append(errorsOut, map[string]string{"source": "winget", "error": wingetErr.Error()})
	}
	if appxErr != nil {
		errorsOut = append(errorsOut, map[string]string{"source": "store", "error": appxErr.Error()})
	}

	wingetOnlyApps, wingetStoreApps := splitScannedManagedApps(wingetApps)
	storeApps := mergeScannedManagedApps(wingetStoreApps, appxApps)

	var registryMap map[string]ScannedApp
	var wingetMap map[string]ScannedApp
	var storeMap map[string]ScannedApp
	var registryNew []ScannedApp
	var wingetNew []ScannedApp
	var storeNew []ScannedApp
	var registryRemoved []ScannedApp
	var wingetRemoved []ScannedApp
	var storeRemoved []ScannedApp
	var registryBaseline bool
	var wingetBaseline bool
	var storeBaseline bool
	var lastScanAt string

	_, updateErr := store.Update(ctx, func(state *State) error {
		registryMap, registryNew, registryRemoved, registryBaseline = diffSnapshot(registryApps, state.RegistryApps)
		wingetMap, wingetNew, wingetRemoved, wingetBaseline = diffSnapshot(wingetOnlyApps, state.WingetApps)
		storeMap, storeNew, storeRemoved, storeBaseline = diffSnapshot(storeApps, state.StoreApps)
		lastScanAt = utcNow()
		state.RegistryApps = registryMap
		state.WingetApps = wingetMap
		state.StoreApps = storeMap
		state.LastScanAt = lastScanAt
		return nil
	})
	if updateErr != nil {
		errorsOut = append(errorsOut, map[string]string{"source": "state", "error": updateErr.Error()})
		appLog("Application scan could not save state: %s.", updateErr)
	}

	newApps := append(registryNew, wingetNew...)
	newApps = append(newApps, storeNew...)
	removedApps := append(registryRemoved, wingetRemoved...)
	removedApps = append(removedApps, storeRemoved...)
	sort.Slice(newApps, func(i, j int) bool {
		return strings.ToLower(newApps[i].Source+newApps[i].Name) < strings.ToLower(newApps[j].Source+newApps[j].Name)
	})

	trackedCount := len(registryMap) + len(wingetMap) + len(storeMap)
	appLog("Application scan completed with %d tracked app(s) and %d new app(s).", trackedCount, len(newApps))
	return ScanResult{
		LastScanAt:      lastScanAt,
		Baseline:        registryBaseline && wingetBaseline && storeBaseline,
		Baselines:       map[string]bool{"registry": registryBaseline, "winget": wingetBaseline, "store": storeBaseline},
		NewApps:         newApps,
		RemovedApps:     removedApps,
		TrackedCount:    trackedCount,
		SourceCounts:    map[string]int{"registry": len(registryMap), "winget": len(wingetMap), "store": len(storeMap)},
		WingetAvailable: wingetResult != nil,
		StoreAvailable:  appxResult != nil,
		WingetResult:    wingetResult,
		Errors:          errorsOut,
	}
}
