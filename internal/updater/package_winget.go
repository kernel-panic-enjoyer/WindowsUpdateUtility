package updater

import (
	"encoding/json"
	"strings"
)

func isSourceToken(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == sourceWinget || value == sourceMSStore
}

func isWingetMatchColumn(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, prefix := range []string{"tag:", "moniker:", "command:", "packagefamilyname:", "productcode:"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func isWingetPinnedColumn(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.Contains(value, "not pinned") || strings.Contains(value, "nicht") {
		return false
	}
	for _, token := range []string{"pinned", "pinning", "angeheftet", "gepinnt", "fixiert"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func wingetMatchValue(value string) string {
	value = strings.TrimSpace(value)
	if before, after, ok := strings.Cut(value, ":"); ok && before != "" {
		return strings.TrimSpace(after)
	}
	return value
}

func parseWingetTable(output string) []Package {
	lines := strings.Split(output, "\n")
	headerSeen := false
	var packages []Package
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if !headerSeen {
			if strings.Contains(lower, "name") && strings.Contains(lower, "id") && strings.Contains(lower, "version") {
				headerSeen = true
			}
			continue
		}
		if strings.Trim(line, "-") == "" || strings.HasPrefix(line, "-") {
			continue
		}
		cols := splitPackageTableColumns(line)
		if len(cols) < 3 {
			continue
		}
		pkg := Package{Name: cols[0], ID: cols[1], Version: cols[2], Manager: managerWinget}
		pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
		rest := cols[3:]
		for i := len(rest) - 1; i >= 0; i-- {
			if isSourceToken(rest[i]) {
				pkg.Source = strings.ToLower(rest[i])
				pkg.Manager = wingetSourceManager(pkg.Source)
				rest = append(rest[:i], rest[i+1:]...)
				break
			}
		}
		for i := 0; i < len(rest); {
			if isWingetPinnedColumn(rest[i]) {
				pkg.Pinned = true
				rest = append(rest[:i], rest[i+1:]...)
				continue
			}
			i++
		}
		if len(rest) > 0 {
			if isWingetMatchColumn(rest[0]) {
				pkg.Match = rest[0]
			} else {
				pkg.AvailableVersion = rest[0]
			}
		}
		if len(rest) > 1 {
			pkg.Match = rest[1]
		}
		packages = append(packages, pkg)
	}
	return packages
}

func isUnknownPackageVersion(version string) bool {
	normalized := strings.ToLower(strings.TrimSpace(version))
	switch normalized {
	case "", "-", "unknown", "unbekannt", "unknown version", "unbekannte version":
		return true
	default:
		return false
	}
}

func parseWingetExport(output string) []Package {
	var decoded struct {
		Sources []struct {
			Packages []struct {
				PackageIdentifier string
				Version           string
			}
			SourceDetails struct {
				Name string
			}
		}
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return nil
	}
	var packages []Package
	for _, source := range decoded.Sources {
		sourceName := source.SourceDetails.Name
		if sourceName == "" {
			sourceName = sourceWinget
		}
		for _, item := range source.Packages {
			id := strings.TrimSpace(item.PackageIdentifier)
			if id == "" {
				continue
			}
			packages = append(packages, Package{
				ID:             id,
				Name:           id,
				Version:        item.Version,
				Manager:        wingetSourceManager(sourceName),
				Source:         sourceName,
				UnknownVersion: isUnknownPackageVersion(item.Version),
			})
		}
	}
	return packages
}
