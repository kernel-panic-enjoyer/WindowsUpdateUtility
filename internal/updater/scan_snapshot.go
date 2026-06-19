package updater

import (
	"sort"
	"strings"
)

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
