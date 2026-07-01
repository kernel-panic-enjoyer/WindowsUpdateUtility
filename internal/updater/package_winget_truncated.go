package updater

import (
	"sort"
	"strings"
)

// Includes the normal ellipsis and the mojibake form winget can emit through lossy console decoding.
var wingetIDTruncationMarkers = []string{"\u2026", "\u00e2\u20ac\u00a6", "..."}

func wingetIDTruncationMarker(value string) string {
	for _, marker := range wingetIDTruncationMarkers {
		if strings.Contains(value, marker) {
			return marker
		}
	}
	return ""
}

func isTruncatedID(id string) bool {
	return wingetIDTruncationMarker(id) != ""
}

func wingetTruncatedMSIXPackage(pkg Package) (Package, bool) {
	trimmedID := strings.TrimSpace(pkg.ID)
	trimmedName := strings.TrimSpace(pkg.Name)
	if !isTruncatedID(trimmedID) || !strings.HasPrefix(strings.ToLower(trimmedID), "msix\\") || trimmedName == "" {
		return Package{}, false
	}
	pkg.ID = trimmedName
	pkg.Manager = managerStore
	pkg.Source = sourceMSStore
	pkg.ActionBackend = backendWingetMSStoreFallback
	return pkg, true
}

func wingetIDMatches(fullID, tableID string) bool {
	fullIDLower := strings.ToLower(fullID)
	tableIDLower := strings.ToLower(tableID)
	if fullIDLower == tableIDLower {
		return true
	}
	if marker := wingetIDTruncationMarker(tableIDLower); marker != "" {
		tableIDPrefix := strings.Split(tableIDLower, marker)[0]
		return strings.HasPrefix(fullIDLower, tableIDPrefix)
	}
	return false
}

func mergeWingetExportWithTable(exportedPackages, tablePackages []Package) []Package {
	matchedTableRows := make(map[int]bool, len(tablePackages))
	exportedIDsByLowercase := make(map[string]bool, len(exportedPackages))
	mergedPackages := make([]Package, 0, len(exportedPackages)+len(tablePackages))
	for _, exportedPkg := range exportedPackages {
		exportedIDsByLowercase[strings.ToLower(exportedPkg.ID)] = true
		matchedTableIndex := -1
		for i, tablePkg := range tablePackages {
			if matchedTableRows[i] || !wingetIDMatches(exportedPkg.ID, tablePkg.ID) {
				continue
			}
			if exportedPkg.Version != "" && tablePkg.Version != "" && exportedPkg.Version != tablePkg.Version {
				continue
			}
			matchedTableIndex = i
			break
		}
		if matchedTableIndex >= 0 {
			matchedTableRows[matchedTableIndex] = true
			tablePkg := tablePackages[matchedTableIndex]
			exportedPkg.Name = tablePkg.Name
			exportedPkg.AvailableVersion = tablePkg.AvailableVersion
			if tablePkg.Source != "" {
				exportedPkg.Source = tablePkg.Source
				exportedPkg.Manager = wingetSourceManager(tablePkg.Source)
			}
		}
		mergedPackages = append(mergedPackages, exportedPkg)
	}
	for i, tablePkg := range tablePackages {
		if matchedTableRows[i] || exportedIDsByLowercase[strings.ToLower(tablePkg.ID)] {
			continue
		}
		if fallbackPkg, ok := wingetTruncatedMSIXPackage(tablePkg); ok {
			mergedPackages = append(mergedPackages, fallbackPkg)
			continue
		}
		if isTruncatedID(tablePkg.ID) {
			continue
		}
		if tablePkg.Source == sourceWinget || tablePkg.Source == sourceMSStore {
			mergedPackages = append(mergedPackages, tablePkg)
		}
	}
	sort.Slice(mergedPackages, func(i, j int) bool {
		return strings.ToLower(mergedPackages[i].Name) < strings.ToLower(mergedPackages[j].Name)
	})
	return mergedPackages
}
