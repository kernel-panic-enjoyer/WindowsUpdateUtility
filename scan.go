package main

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeRegistryKey(name, publisher, location string) string {
	base := strings.ToLower(name + "|" + publisher + "|" + location)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	key := strings.Trim(re.ReplaceAllString(base, "-"), "-")
	if key == "" {
		key = strings.Trim(re.ReplaceAllString(strings.ToLower(name), "-"), "-")
	}
	return key
}

func parseRegQuery(output, hive string) []ScannedApp {
	valuePattern := regexp.MustCompile(`^\s+([^\s]+)\s+REG_\S+\s*(.*)$`)
	apps := map[string]map[string]string{}
	current := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), "HKEY_") {
			current = strings.TrimSpace(line)
			if apps[current] == nil {
				apps[current] = map[string]string{}
			}
			continue
		}
		if current == "" {
			continue
		}
		match := valuePattern.FindStringSubmatch(line)
		if len(match) == 3 {
			apps[current][match[1]] = normalizeText(match[2])
		}
	}

	var scanned []ScannedApp
	for _, values := range apps {
		name := values["DisplayName"]
		if name == "" {
			continue
		}
		if values["SystemComponent"] == "0x1" {
			continue
		}
		releaseType := strings.ToLower(values["ReleaseType"])
		if releaseType == "hotfix" || releaseType == "security update" || releaseType == "update rollup" {
			continue
		}
		publisher := values["Publisher"]
		location := values["InstallLocation"]
		key := normalizeRegistryKey(name, publisher, location)
		scanned = append(scanned, ScannedApp{
			Key:             key,
			Name:            name,
			Version:         values["DisplayVersion"],
			Publisher:       publisher,
			InstallLocation: location,
			Source:          "registry",
			RegistryHive:    hive,
		})
	}
	sort.Slice(scanned, func(i, j int) bool { return strings.ToLower(scanned[i].Name) < strings.ToLower(scanned[j].Name) })
	return scanned
}

func readRegistryApps() ([]ScannedApp, error) {
	queries := []struct {
		key  string
		hive string
	}{
		{`HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall`, "HKLM"},
		{`HKLM\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "HKLM32"},
		{`HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall`, "HKCU"},
	}
	appMap := map[string]ScannedApp{}
	var errs []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, query := range queries {
		query := query
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := runCommand(60*time.Second, "reg.exe", "query", query.key, "/s")
			mu.Lock()
			defer mu.Unlock()
			if !result.OK && result.Stdout == "" {
				errs = append(errs, result.Stderr)
				return
			}
			for _, app := range parseRegQuery(result.Stdout, query.hive) {
				appMap[app.Key] = app
			}
		}()
	}
	wg.Wait()
	var apps []ScannedApp
	for _, app := range appMap {
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	if len(apps) == 0 && len(errs) > 0 {
		return apps, errors.New(strings.Join(errs, "\n"))
	}
	return apps, nil
}

func readWingetApps() ([]ScannedApp, *CommandResult, error) {
	if !detectManager("winget").Available {
		return nil, nil, nil
	}
	packages, result := wingetInstalled()
	apps := []ScannedApp{}
	for _, pkg := range packages {
		if pkg.ID == "" {
			continue
		}
		manager := wingetSourceManager(pkg.Source)
		apps = append(apps, ScannedApp{
			Key:             manager + ":" + strings.ToLower(pkg.ID),
			Name:            pkg.Name,
			Version:         pkg.Version,
			InstallLocation: pkg.ID,
			Source:          manager,
			Manager:         manager,
			PackageID:       pkg.ID,
		})
	}
	return apps, &result, nil
}

func readAppxApps() ([]ScannedApp, *CommandResult, error) {
	packages, result := appxInstalled()
	if !result.OK && len(packages) == 0 {
		errText := strings.TrimSpace(result.Stderr + result.Stdout)
		if errText == "" {
			errText = "Get-AppxPackage returned no inventory"
		}
		return nil, &result, errors.New(errText)
	}
	apps := []ScannedApp{}
	for _, pkg := range packages {
		stableID := stableAppxScanID(pkg)
		apps = append(apps, ScannedApp{
			Key:             "store:" + strings.ToLower(stableID),
			Name:            pkg.Name,
			Version:         pkg.Version,
			Publisher:       "",
			InstallLocation: pkg.ID,
			Source:          "store",
			Manager:         "store",
			PackageID:       stableID,
		})
	}
	return apps, &result, nil
}

func stableAppxScanID(pkg Package) string {
	if stableID := stableStoreScanIdentity(pkg.Match); stableID != "" {
		return stableID
	}
	if stableID := stableStoreScanIdentity(pkg.ID); stableID != "" {
		return stableID
	}
	return ""
}

func isStoreScannedApp(app ScannedApp) bool {
	source := strings.ToLower(strings.TrimSpace(app.Source))
	manager := strings.ToLower(strings.TrimSpace(app.Manager))
	return source == "store" || source == "msstore" || source == "appx" || manager == "store"
}

func splitScannedManagedApps(apps []ScannedApp) ([]ScannedApp, []ScannedApp) {
	var wingetApps []ScannedApp
	var storeApps []ScannedApp
	for _, app := range apps {
		if isStoreScannedApp(app) {
			app.Source = "store"
			app.Manager = "store"
			storeApps = append(storeApps, app)
			continue
		}
		if app.Source == "" {
			app.Source = "winget"
		}
		if app.Manager == "" {
			app.Manager = "winget"
		}
		wingetApps = append(wingetApps, app)
	}
	return wingetApps, storeApps
}

func mergeScannedManagedApps(wingetApps, appxApps []ScannedApp) []ScannedApp {
	managed := make([]ScannedApp, 0, len(wingetApps)+len(appxApps))
	seen := map[string]bool{}
	markSeen := func(app ScannedApp) {
		for _, value := range []string{app.Key, app.Name, app.PackageID} {
			normalized := normalizePackageIdentity(value)
			if normalized != "" {
				seen[normalized] = true
			}
		}
	}
	for _, app := range wingetApps {
		managed = append(managed, app)
		markSeen(app)
	}
	for _, app := range appxApps {
		if seen[normalizePackageIdentity(app.Key)] || seen[normalizePackageIdentity(app.Name)] || seen[normalizePackageIdentity(app.PackageID)] {
			continue
		}
		managed = append(managed, app)
		markSeen(app)
	}
	return managed
}

func scanSourceCounts(apps map[string]ScannedApp) map[string]int {
	counts := map[string]int{"winget": 0, "store": 0}
	for _, app := range apps {
		source := app.Source
		if source == "" || source == "msstore" {
			source = wingetSourceManager(source)
		} else if source == "appx" {
			source = "store"
		}
		counts[source]++
	}
	return counts
}

func managedScanSourceCounts(state State) map[string]int {
	counts := scanSourceCounts(state.WingetApps)
	for source, count := range scanSourceCounts(state.StoreApps) {
		counts[source] += count
	}
	return counts
}

func managedScanTrackedCount(state State) int {
	return len(state.WingetApps) + len(state.StoreApps)
}

func diffSnapshot(current []ScannedApp, previous map[string]ScannedApp) (map[string]ScannedApp, []ScannedApp, []ScannedApp, bool) {
	now := utcNow()
	currentMap := map[string]ScannedApp{}
	var newApps []ScannedApp
	for _, app := range current {
		prev, ok := previous[app.Key]
		if ok {
			app.FirstSeen = prev.FirstSeen
		} else {
			app.FirstSeen = now
			if len(previous) > 0 {
				newApps = append(newApps, app)
			}
		}
		currentMap[app.Key] = app
	}
	var removed []ScannedApp
	for key, app := range previous {
		if _, ok := currentMap[key]; !ok {
			removed = append(removed, app)
		}
	}
	sort.Slice(newApps, func(i, j int) bool { return strings.ToLower(newApps[i].Name) < strings.ToLower(newApps[j].Name) })
	sort.Slice(removed, func(i, j int) bool { return strings.ToLower(removed[i].Name) < strings.ToLower(removed[j].Name) })
	return currentMap, newApps, removed, len(previous) == 0
}

func scanInstalledApplications() ScanResult {
	appLog("Application scan started.")
	state := loadState()
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
		registryApps, registryErr = readRegistryApps()
	}()
	go func() {
		defer wg.Done()
		wingetApps, wingetResult, wingetErr = readWingetApps()
	}()
	go func() {
		defer wg.Done()
		appxApps, appxResult, appxErr = readAppxApps()
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

	registryMap, registryNew, registryRemoved, registryBaseline := diffSnapshot(registryApps, state.RegistryApps)
	wingetOnlyApps, wingetStoreApps := splitScannedManagedApps(wingetApps)
	storeApps := mergeScannedManagedApps(wingetStoreApps, appxApps)
	wingetMap, wingetNew, wingetRemoved, wingetBaseline := diffSnapshot(wingetOnlyApps, state.WingetApps)
	storeMap, storeNew, storeRemoved, storeBaseline := diffSnapshot(storeApps, state.StoreApps)
	state.RegistryApps = registryMap
	state.WingetApps = wingetMap
	state.StoreApps = storeMap
	state.LastScanAt = utcNow()
	_ = saveState(state)

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
		LastScanAt:      state.LastScanAt,
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
