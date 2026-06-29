package updater

import (
	"sort"
	"strings"
)

var wingetTruncatedIDMarkers = []string{"\u2026", "\u00e2\u20ac\u00a6", "..."}

func wingetTruncationMarker(value string) string {
	for _, marker := range wingetTruncatedIDMarkers {
		if strings.Contains(value, marker) {
			return marker
		}
	}
	return ""
}

func isTruncatedID(id string) bool {
	return wingetTruncationMarker(id) != ""
}

func wingetTruncatedMSIXPackage(pkg Package) (Package, bool) {
	if !isTruncatedID(pkg.ID) || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(pkg.ID)), "msix\\") || strings.TrimSpace(pkg.Name) == "" {
		return Package{}, false
	}
	pkg.ID = strings.TrimSpace(pkg.Name)
	pkg.Manager = managerStore
	pkg.Source = sourceMSStore
	pkg.ActionBackend = backendWingetMSStoreFallback
	return pkg, true
}

func wingetIDMatches(fullID, tableID string) bool {
	full := strings.ToLower(fullID)
	table := strings.ToLower(tableID)
	if full == table {
		return true
	}
	if marker := wingetTruncationMarker(table); marker != "" {
		return strings.HasPrefix(full, strings.Split(table, marker)[0])
	}
	return false
}

func mergeWingetExportWithTable(exported, table []Package) []Package {
	used := map[int]bool{}
	var merged []Package
	for _, pkg := range exported {
		match := -1
		for i, tablePkg := range table {
			if used[i] || !wingetIDMatches(pkg.ID, tablePkg.ID) {
				continue
			}
			if pkg.Version != "" && tablePkg.Version != "" && pkg.Version != tablePkg.Version {
				continue
			}
			match = i
			break
		}
		if match >= 0 {
			used[match] = true
			tablePkg := table[match]
			pkg.Name = tablePkg.Name
			pkg.AvailableVersion = tablePkg.AvailableVersion
			if tablePkg.Source != "" {
				pkg.Source = tablePkg.Source
				pkg.Manager = wingetSourceManager(tablePkg.Source)
			}
		}
		merged = append(merged, pkg)
	}
	exportedIDs := map[string]bool{}
	for _, pkg := range exported {
		exportedIDs[strings.ToLower(pkg.ID)] = true
	}
	for i, pkg := range table {
		if used[i] || exportedIDs[strings.ToLower(pkg.ID)] {
			continue
		}
		if fallback, ok := wingetTruncatedMSIXPackage(pkg); ok {
			merged = append(merged, fallback)
			continue
		}
		if isTruncatedID(pkg.ID) {
			continue
		}
		if pkg.Source == sourceWinget || pkg.Source == sourceMSStore {
			merged = append(merged, pkg)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged
}
