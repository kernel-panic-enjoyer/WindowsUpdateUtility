package updater

import (
	"strings"
	"time"
)

func parseStoreSearch(output string) []Package {
	return parseStorePackageRows(output)
}

func parseStorePackageRows(output string) []Package {
	lines := strings.Split(output, "\n")
	tableHeaderFound := false
	pipeDelimitedTable := false
	var headerColumns []string
	var pendingPipeRow []string
	var parsedPackages []Package
	flushPendingPipeRow := func() {
		if len(pendingPipeRow) == 0 {
			return
		}
		if pkg, ok := storePackageFromTableRow(headerColumns, pendingPipeRow); ok {
			parsedPackages = append(parsedPackages, pkg)
		}
		pendingPipeRow = nil
	}
	for _, raw := range lines {
		line := strings.TrimSpace(normalizeStoreTableDelimiters(raw))
		if line == "" || isStoreOutputNoiseLine(line) {
			continue
		}
		if !tableHeaderFound {
			if isStorePackageTableHeader(line) {
				tableHeaderFound = true
				headerColumns = splitStoreHeaderColumns(line)
				pipeDelimitedTable = strings.Contains(line, "|")
			}
			continue
		}
		if isStoreDividerLine(line) {
			continue
		}
		if pipeDelimitedTable && !strings.Contains(line, "|") {
			flushPendingPipeRow()
			continue
		}
		if strings.Contains(line, "|") && len(headerColumns) > 0 {
			rowColumns := fitStoreColumnsToHeader(splitStorePipeColumns(line), len(headerColumns))
			if len(rowColumns) < 2 || storeColumnsAllEmpty(rowColumns) {
				continue
			}
			if isStorePipeContinuationRow(headerColumns, rowColumns, pendingPipeRow) {
				pendingPipeRow = mergeStorePipeContinuationRow(pendingPipeRow, rowColumns)
				continue
			}
			flushPendingPipeRow()
			pendingPipeRow = rowColumns
			continue
		}
		flushPendingPipeRow()
		rowColumns := splitStoreColumns(line)
		rowColumns = fitStoreColumnsToHeader(rowColumns, len(headerColumns))
		if pkg, ok := storePackageFromTableRow(headerColumns, rowColumns); ok {
			parsedPackages = append(parsedPackages, pkg)
		}
	}
	flushPendingPipeRow()
	return parsedPackages
}

func isStorePackageTableHeader(line string) bool {
	columns := splitStoreColumns(line)
	if len(columns) < 2 {
		return false
	}
	hasName := false
	hasKnownColumn := false
	for _, column := range columns {
		normalized := strings.ToLower(strings.TrimSpace(column))
		switch normalized {
		case "name", "app", "application":
			hasName = true
		case "id", "product id", "package id", "publisher", "version", "current", "available", "status", "price":
			hasKnownColumn = true
		}
	}
	return hasName && hasKnownColumn
}

func storeUpdatesCommand() []string {
	return managerCommand(managerStore, "updates", "--apply", "false")
}

func splitStoreColumns(line string) []string {
	line = normalizeStoreTableDelimiters(line)
	if strings.Contains(line, "|") {
		return trimNonEmptyStoreColumns(splitStorePipeColumns(line))
	}
	return splitPackageTableColumns(line)
}

func splitStoreHeaderColumns(line string) []string {
	line = normalizeStoreTableDelimiters(line)
	if strings.Contains(line, "|") {
		return splitStorePipeColumns(line)
	}
	return splitPackageTableColumns(line)
}

func splitStorePipeColumns(line string) []string {
	line = strings.TrimSpace(normalizeStoreTableDelimiters(line))
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		columns = append(columns, strings.TrimSpace(part))
	}
	return columns
}

func trimNonEmptyStoreColumns(columns []string) []string {
	filtered := make([]string, 0, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column != "" {
			filtered = append(filtered, column)
		}
	}
	return filtered
}

func fitStoreColumnsToHeader(columns []string, headerColumnCount int) []string {
	if headerColumnCount <= 0 {
		return columns
	}
	normalized := make([]string, headerColumnCount)
	for i := 0; i < headerColumnCount && i < len(columns); i++ {
		normalized[i] = strings.TrimSpace(columns[i])
	}
	if len(columns) > headerColumnCount {
		normalized[headerColumnCount-1] = strings.TrimSpace(strings.Join(append([]string{normalized[headerColumnCount-1]}, columns[headerColumnCount:]...), " "))
	}
	return normalized
}

func normalizeStoreTableDelimiters(line string) string {
	return strings.NewReplacer(
		"\u2502", "|",
		"\u2503", "|",
		"\u2500", "-",
		"\u250c", "-",
		"\u2510", "-",
		"\u2514", "-",
		"\u2518", "-",
		"\u251c", "-",
		"\u2524", "-",
		"\u252c", "-",
		"\u2534", "-",
		"\u253c", "-",
		"â”‚", "|",
		"â”ƒ", "|",
		"â”€", "-",
		"â”Œ", "-",
		"â”", "-",
		"â””", "-",
		"â”˜", "-",
		"â”œ", "-",
		"â”¤", "-",
		"â”¬", "-",
		"â”´", "-",
		"â”¼", "-",
	).Replace(line)
}

func isStoreOutputNoiseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	normalized := normalizePackageIdentity(trimmed)
	if strings.Contains(normalized, "searchresultsfor") ||
		strings.Contains(normalized, "updatesavailable") ||
		strings.HasPrefix(normalized, "resultsfor") ||
		strings.Contains(lower, "no results") ||
		strings.Contains(lower, "no updates found") ||
		strings.Contains(lower, "checking for updates") ||
		strings.Contains(lower, "checking updates") ||
		strings.Contains(lower, "looking up product") ||
		strings.Contains(lower, "already up to date") ||
		strings.Contains(lower, "would you like to install") ||
		strings.Contains(lower, "failed to read input") ||
		strings.Contains(lower, "non-interactive mode") ||
		strings.HasPrefix(lower, "pending:") ||
		strings.HasPrefix(lower, "downloading:") ||
		strings.HasPrefix(lower, "ready to download:") {
		return true
	}
	return isStoreDividerLine(trimmed)
}

func isStoreDividerLine(line string) bool {
	line = strings.TrimSpace(normalizeStoreTableDelimiters(line))
	if line == "" {
		return true
	}
	nonDivider := 0
	for _, r := range line {
		if r == '-' || r == '_' || r == '=' || r == '|' || r == '+' || r == ' ' || r == '\t' {
			continue
		}
		nonDivider++
	}
	return nonDivider == 0
}

func looksLikeVersion(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, ".") {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func looksLikeStoreTableID(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if strings.Contains(candidate, ".") || strings.Contains(candidate, "_") {
		return true
	}
	return isSafePackageID(candidate) && strings.ToUpper(candidate) == candidate && len(candidate) >= 8
}

func storePackageFromTableRow(headerColumns, rowColumns []string) (Package, bool) {
	if len(rowColumns) < 2 {
		return Package{}, false
	}
	packageName := storeTableColumnValue(headerColumns, rowColumns, "name", "app", "application")
	if packageName == "" && len(rowColumns) > 0 {
		packageName = strings.TrimSpace(rowColumns[0])
	}
	if packageName == "" || strings.HasPrefix(packageName, "[") || isStoreOutputNoiseLine(packageName) {
		return Package{}, false
	}
	packageID := storeTableColumnValue(headerColumns, rowColumns, "id", "product id", "package id")
	if packageID == "" && len(rowColumns) > 1 && looksLikeStoreTableID(rowColumns[1]) {
		packageID = strings.TrimSpace(rowColumns[1])
	}
	if packageID == "" {
		packageID = packageName
	}
	installedVersion := storeTableColumnValue(headerColumns, rowColumns, "current")
	availableVersion := storeTableColumnValue(headerColumns, rowColumns, "available")
	if installedVersion == "" {
		installedVersion = storeTableColumnValue(headerColumns, rowColumns, "version")
	}
	if installedVersion == "" {
		for i := 1; i < len(rowColumns); i++ {
			if looksLikeVersion(rowColumns[i]) {
				if installedVersion == "" {
					installedVersion = rowColumns[i]
				} else if availableVersion == "" {
					availableVersion = rowColumns[i]
				}
			}
		}
	}
	return Package{
		ID:               strings.TrimSpace(packageID),
		Name:             strings.TrimSpace(packageName),
		Version:          strings.TrimSpace(installedVersion),
		AvailableVersion: strings.TrimSpace(availableVersion),
		Manager:          managerStore,
		Source:           sourceStoreCLI,
		UpdateSupported:  true,
		ActionBackend:    backendStoreCLI,
	}, true
}

func storeTableColumnValue(headerColumns, rowColumns []string, names ...string) string {
	index := storeTableColumnIndex(headerColumns, names...)
	if index < 0 || index >= len(rowColumns) {
		return ""
	}
	return strings.TrimSpace(rowColumns[index])
}

func storeTableColumnIndex(headerColumns []string, names ...string) int {
	for i, column := range headerColumns {
		normalized := strings.ToLower(strings.TrimSpace(column))
		for _, name := range names {
			if normalized == name {
				return i
			}
		}
	}
	return -1
}

func storeColumnsAllEmpty(columns []string) bool {
	for _, column := range columns {
		if strings.TrimSpace(column) != "" {
			return false
		}
	}
	return true
}

func isStorePipeContinuationRow(headerColumns, rowColumns, pendingRow []string) bool {
	if len(pendingRow) == 0 {
		return false
	}
	if index := storeTableColumnIndex(headerColumns, "current", "available", "version"); index >= 0 && index < len(rowColumns) {
		if looksLikeVersion(rowColumns[index]) {
			return false
		}
		return true
	}
	if index := storeTableColumnIndex(headerColumns, "id", "product id", "package id"); index >= 0 && index < len(rowColumns) {
		return strings.TrimSpace(rowColumns[index]) == ""
	}
	return false
}

func mergeStorePipeContinuationRow(baseRow, continuationRow []string) []string {
	if len(baseRow) < len(continuationRow) {
		expanded := make([]string, len(continuationRow))
		copy(expanded, baseRow)
		baseRow = expanded
	}
	for i, value := range continuationRow {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if baseRow[i] == "" {
			baseRow[i] = value
		} else {
			baseRow[i] += " " + value
		}
	}
	return baseRow
}

func storeSearch(query string) ([]Package, CommandResult) {
	result := runCommand(90*time.Second, managerCommand(managerStore, "search", query)...)
	return parseStoreSearch(result.Stdout + "\n" + result.Stderr), result
}
