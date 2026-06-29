package updater

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
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
	var columnStarts []int
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
				columnStarts = packageTableColumnStarts(raw)
			}
			continue
		}
		if strings.Trim(line, "-") == "" || strings.HasPrefix(line, "-") {
			continue
		}
		simpleColumns := splitPackageTableColumns(line)
		headerColumns := splitPackageTableColumnsAtStarts(raw, columnStarts)
		candidates := [][]string{simpleColumns, headerColumns}
		if len(simpleColumns) < 3 || wingetTableColumnsNeedHeaderFallback(simpleColumns) {
			candidates = [][]string{headerColumns, simpleColumns}
		}
		for _, cols := range candidates {
			if pkg, ok := wingetPackageFromTableColumns(cols); ok {
				packages = append(packages, pkg)
				break
			}
		}
	}
	return packages
}

func wingetPackageFromTableColumns(cols []string) (Package, bool) {
	if len(cols) < 3 {
		return Package{}, false
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
	if !wingetTableRowLooksUsable(pkg) {
		if recovered, ok := recoverWingetOverflowRow(cols); ok {
			return recovered, true
		}
		return Package{}, false
	}
	return pkg, true
}

func recoverWingetOverflowRow(cols []string) (Package, bool) {
	if len(cols) < 3 {
		return Package{}, false
	}
	if len(cols) == 3 && isSourceToken(cols[2]) {
		id, version, ok := splitLastTableField(cols[1])
		if !ok {
			return Package{}, false
		}
		pkg := Package{Name: cols[0], ID: id, Version: version, Source: strings.ToLower(cols[2]), Manager: wingetSourceManager(cols[2])}
		pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
		if wingetTableRowLooksUsable(pkg) {
			return pkg, true
		}
		return Package{}, false
	}
	if len(cols) >= 4 && isSourceToken(cols[len(cols)-1]) {
		name, id, ok := splitLastTableField(cols[0])
		if !ok {
			return Package{}, false
		}
		pkg := Package{
			Name:             name,
			ID:               id,
			Version:          cols[1],
			AvailableVersion: cols[2],
			Source:           strings.ToLower(cols[len(cols)-1]),
			Manager:          wingetSourceManager(cols[len(cols)-1]),
		}
		pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
		for _, value := range cols[3 : len(cols)-1] {
			if isWingetPinnedColumn(value) {
				pkg.Pinned = true
			} else if isWingetMatchColumn(value) {
				pkg.Match = value
			}
		}
		if wingetTableRowLooksUsable(pkg) {
			return pkg, true
		}
	}
	return Package{}, false
}

func splitLastTableField(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	for end := len(value); end > 0; {
		r, size := utf8.DecodeLastRuneInString(value[:end])
		index := end - size
		if !unicode.IsSpace(r) {
			end = index
			continue
		}
		left := strings.TrimSpace(value[:index])
		right := strings.TrimSpace(value[index+size:])
		return left, right, left != "" && right != ""
	}
	return "", "", false
}

func wingetTableRowLooksUsable(pkg Package) bool {
	return wingetTableIDLooksUsable(pkg.ID) &&
		wingetTableVersionLooksUsable(pkg.Version) &&
		(pkg.AvailableVersion == "" || wingetTableVersionLooksUsable(pkg.AvailableVersion))
}

func wingetTableIDLooksUsable(id string) bool {
	id = strings.TrimSpace(id)
	lower := strings.ToLower(id)
	if id == "" || isSourceToken(id) || isWingetMatchColumn(id) || isLikelyVersionToken(id) {
		return false
	}
	if strings.ContainsFunc(id, unicode.IsControl) {
		return false
	}
	if isTruncatedID(id) {
		return true
	}
	if strings.HasPrefix(lower, "msix\\") || strings.HasPrefix(lower, "arp\\") {
		return true
	}
	if strings.Contains(id, "\\") {
		return false
	}
	if strings.ContainsAny(id, " \t") {
		return false
	}
	if strings.Contains(id, ".") {
		return true
	}
	return isStoreProductIDLike(id)
}

func isStoreProductIDLike(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 10 || len(value) > 20 {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

func wingetTableVersionLooksUsable(version string) bool {
	version = strings.TrimSpace(version)
	if isUnknownPackageVersion(version) {
		return true
	}
	if version == "" || isSourceToken(version) || isWingetMatchColumn(version) || isWingetPinnedColumn(version) {
		return false
	}
	if strings.ContainsFunc(version, unicode.IsControl) {
		return false
	}
	hasDigit := false
	for _, token := range strings.Fields(version) {
		if token == ">" || token == "<" || token == ">=" || token == "<=" || token == "=" {
			continue
		}
		tokenHasDigit := false
		for _, r := range token {
			if r >= '0' && r <= '9' {
				hasDigit = true
				tokenHasDigit = true
				continue
			}
			if unicode.IsLetter(r) || r == '.' || r == '-' || r == '_' || r == '+' || r == ':' || r == '~' {
				continue
			}
			return false
		}
		if !tokenHasDigit && token != "" {
			return false
		}
	}
	return hasDigit
}

func wingetTableColumnsNeedHeaderFallback(cols []string) bool {
	if len(cols) < 3 {
		return true
	}
	if isSourceToken(cols[2]) || isWingetMatchColumn(cols[2]) || isWingetPinnedColumn(cols[2]) {
		return true
	}
	if isLikelyVersionToken(cols[1]) {
		return true
	}
	if wingetTableColumnsLookShiftedByDisplayName(cols) {
		return true
	}
	return false
}

func wingetTableColumnsLookShiftedByDisplayName(cols []string) bool {
	if len(cols) < 4 {
		return false
	}
	if !wingetTableValueLooksLikeID(cols[2]) || isUnknownPackageVersion(cols[2]) || isLikelyVersionToken(cols[2]) {
		return false
	}
	return isLikelyVersionToken(cols[3]) || isUnknownPackageVersion(cols[3]) || isSourceToken(cols[3]) || isWingetMatchColumn(cols[3])
}

func wingetTableValueLooksLikeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isSourceToken(value) || isWingetMatchColumn(value) || isWingetPinnedColumn(value) {
		return false
	}
	for _, r := range value {
		if r == ' ' || r == '\t' {
			return false
		}
	}
	return true
}

func isLikelyVersionToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isUnknownPackageVersion(value) {
		return false
	}
	hasDigit := false
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '.' || r == '-' || r == '_' || r == '+':
		default:
			return false
		}
	}
	return hasDigit
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
