package updater

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	wingetMatchColumnPrefixes = [...]string{"tag:", "moniker:", "command:", "packagefamilyname:", "productcode:"}
	wingetPinnedColumnMarkers = [...]string{"pinned", "pinning", "angeheftet", "gepinnt", "fixiert"}
)

func isWingetSourceColumnValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == sourceWinget || value == sourceMSStore
}

func isWingetMatchColumnValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, prefix := range wingetMatchColumnPrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func isWingetPinnedColumnValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.Contains(value, "not pinned") || strings.Contains(value, "nicht") {
		return false
	}
	for _, marker := range wingetPinnedColumnMarkers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isWingetMetadataColumnValue(value string) bool {
	return isWingetSourceColumnValue(value) || isWingetMatchColumnValue(value) || isWingetPinnedColumnValue(value)
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
	for _, rawLine := range lines {
		trimmedLine := strings.TrimSpace(rawLine)
		if trimmedLine == "" {
			continue
		}
		lowerLine := strings.ToLower(trimmedLine)
		if !headerSeen {
			if strings.Contains(lowerLine, "name") && strings.Contains(lowerLine, "id") && strings.Contains(lowerLine, "version") {
				headerSeen = true
				columnStarts = packageTableColumnStarts(rawLine)
			}
			continue
		}
		if strings.Trim(trimmedLine, "-") == "" || strings.HasPrefix(trimmedLine, "-") {
			continue
		}
		simpleColumns := splitPackageTableColumns(trimmedLine)
		headerColumns := splitPackageTableColumnsAtStarts(rawLine, columnStarts)
		candidates := [][]string{simpleColumns, headerColumns}
		if len(simpleColumns) < 3 || wingetSimpleColumnsNeedHeaderFallback(simpleColumns) {
			candidates = [][]string{headerColumns, simpleColumns}
		}
		for _, candidateColumns := range candidates {
			if pkg, ok := parseWingetPackageColumns(candidateColumns); ok {
				packages = append(packages, pkg)
				break
			}
		}
	}
	return packages
}

func parseWingetPackageColumns(columns []string) (Package, bool) {
	if len(columns) < 3 {
		return Package{}, false
	}
	pkg := Package{Name: columns[0], ID: columns[1], Version: columns[2], Manager: managerWinget}
	pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
	remainingFields := columns[3:]
	for i := len(remainingFields) - 1; i >= 0; i-- {
		if isWingetSourceColumnValue(remainingFields[i]) {
			pkg.Source = strings.ToLower(remainingFields[i])
			pkg.Manager = wingetSourceManager(pkg.Source)
			remainingFields = append(remainingFields[:i], remainingFields[i+1:]...)
			break
		}
	}
	for i := 0; i < len(remainingFields); {
		if isWingetPinnedColumnValue(remainingFields[i]) {
			pkg.Pinned = true
			remainingFields = append(remainingFields[:i], remainingFields[i+1:]...)
			continue
		}
		i++
	}
	if len(remainingFields) > 0 {
		if isWingetMatchColumnValue(remainingFields[0]) {
			pkg.Match = remainingFields[0]
		} else {
			pkg.AvailableVersion = remainingFields[0]
		}
	}
	if len(remainingFields) > 1 {
		pkg.Match = remainingFields[1]
	}
	if !parsedWingetTablePackageLooksUsable(pkg) {
		if recovered, ok := recoverWingetTableOverflowRow(columns); ok {
			return recovered, true
		}
		return Package{}, false
	}
	return pkg, true
}

func recoverWingetTableOverflowRow(columns []string) (Package, bool) {
	if len(columns) < 3 {
		return Package{}, false
	}
	if len(columns) == 3 && isWingetSourceColumnValue(columns[2]) {
		id, version, ok := splitTrailingTableField(columns[1])
		if !ok {
			return Package{}, false
		}
		pkg := Package{Name: columns[0], ID: id, Version: version, Source: strings.ToLower(columns[2]), Manager: wingetSourceManager(columns[2])}
		pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
		if parsedWingetTablePackageLooksUsable(pkg) {
			return pkg, true
		}
		return Package{}, false
	}
	if len(columns) >= 4 && isWingetSourceColumnValue(columns[len(columns)-1]) {
		name, id, ok := splitTrailingTableField(columns[0])
		if !ok {
			return Package{}, false
		}
		pkg := Package{
			Name:             name,
			ID:               id,
			Version:          columns[1],
			AvailableVersion: columns[2],
			Source:           strings.ToLower(columns[len(columns)-1]),
			Manager:          wingetSourceManager(columns[len(columns)-1]),
		}
		pkg.UnknownVersion = isUnknownPackageVersion(pkg.Version)
		for _, value := range columns[3 : len(columns)-1] {
			if isWingetPinnedColumnValue(value) {
				pkg.Pinned = true
			} else if isWingetMatchColumnValue(value) {
				pkg.Match = value
			}
		}
		if parsedWingetTablePackageLooksUsable(pkg) {
			return pkg, true
		}
	}
	return Package{}, false
}

func splitTrailingTableField(value string) (string, string, bool) {
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

func parsedWingetTablePackageLooksUsable(pkg Package) bool {
	return wingetTablePackageIDLooksUsable(pkg.ID) &&
		wingetTablePackageVersionLooksUsable(pkg.Version) &&
		(pkg.AvailableVersion == "" || wingetTablePackageVersionLooksUsable(pkg.AvailableVersion))
}

func wingetTablePackageIDLooksUsable(id string) bool {
	id = strings.TrimSpace(id)
	lower := strings.ToLower(id)
	if id == "" || isWingetSourceColumnValue(id) || isWingetMatchColumnValue(id) || isLikelyWingetVersionToken(id) {
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
	return looksLikeWingetTableStoreProductID(id)
}

func looksLikeWingetTableStoreProductID(value string) bool {
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

func wingetTablePackageVersionLooksUsable(version string) bool {
	version = strings.TrimSpace(version)
	if isUnknownPackageVersion(version) {
		return true
	}
	if version == "" || isWingetMetadataColumnValue(version) {
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

func wingetSimpleColumnsNeedHeaderFallback(columns []string) bool {
	if len(columns) < 3 {
		return true
	}
	if isWingetMetadataColumnValue(columns[2]) {
		return true
	}
	if isLikelyWingetVersionToken(columns[1]) {
		return true
	}
	if wingetSimpleColumnsShiftedByDisplayName(columns) {
		return true
	}
	return false
}

func wingetSimpleColumnsShiftedByDisplayName(columns []string) bool {
	if len(columns) < 4 {
		return false
	}
	if !wingetTableValueLooksLikePackageID(columns[2]) || isUnknownPackageVersion(columns[2]) || isLikelyWingetVersionToken(columns[2]) {
		return false
	}
	return isLikelyWingetVersionToken(columns[3]) || isUnknownPackageVersion(columns[3]) || isWingetSourceColumnValue(columns[3]) || isWingetMatchColumnValue(columns[3])
}

func wingetTableValueLooksLikePackageID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isWingetMetadataColumnValue(value) {
		return false
	}
	for _, r := range value {
		if r == ' ' || r == '\t' {
			return false
		}
	}
	return true
}

func isLikelyWingetVersionToken(value string) bool {
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
