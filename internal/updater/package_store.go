package updater

import (
	"strings"
	"time"
)

func parseStoreSearch(output string) []Package {
	return parseStorePackageTable(output)
}

func parseStoreUpdates(output string) map[string]string {
	updates := map[string]string{}
	for _, pkg := range parseStoreUpdatePackages(output) {
		if pkg.ID == "" {
			continue
		}
		available := pkg.AvailableVersion
		if available == "" {
			available = pkg.Version
		}
		if available != "" {
			updates[packageKey(managerStore, strings.ToLower(pkg.ID))] = available
		}
	}
	return updates
}

func parseStoreInstalled(output string) []Package {
	packages := parseStorePackageTable(output)
	for i := range packages {
		packages[i].Key = packageKey(managerStore, packages[i].ID)
		packages[i].Manager = managerStore
		packages[i].Source = sourceStoreCLI
		packages[i].Installed = true
		packages[i].UpdateSupported = true
		packages[i].ActionBackend = backendStoreCLI
	}
	return packages
}

func parseStoreUpdatePackages(output string) []Package {
	rows := parseStorePackageTable(output)
	packages := make([]Package, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		name := strings.TrimSpace(row.Name)
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		if id == "" {
			continue
		}
		available := latestPackageVersion(row)
		packages = append(packages, Package{
			Key:              packageKey(managerStore, id),
			ID:               id,
			Name:             name,
			AvailableVersion: available,
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
			Manager:          managerStore,
			Source:           sourceStoreCLI,
			ActionBackend:    backendStoreCLI,
		})
	}
	return packages
}

func parseStorePackageTable(output string) []Package {
	lines := strings.Split(output, "\n")
	headerSeen := false
	headerBoxed := false
	var header []string
	var pendingBoxRow []string
	var packages []Package
	flushPendingBoxRow := func() {
		if len(pendingBoxRow) == 0 {
			return
		}
		if pkg, ok := storePackageFromColumns(header, pendingBoxRow); ok {
			packages = append(packages, pkg)
		}
		pendingBoxRow = nil
	}
	for _, raw := range lines {
		line := strings.TrimSpace(normalizeStoreTableDelimiters(raw))
		if line == "" || isStoreOutputNoiseLine(line) {
			continue
		}
		if !headerSeen {
			if isStoreTableHeader(line) {
				headerSeen = true
				header = splitStoreHeaderColumns(line)
				headerBoxed = strings.Contains(line, "|")
			}
			continue
		}
		if isStoreDividerLine(line) {
			continue
		}
		if headerBoxed && !strings.Contains(line, "|") {
			flushPendingBoxRow()
			continue
		}
		if strings.Contains(line, "|") && len(header) > 0 {
			cols := normalizeStoreColumnCount(splitStoreBoxColumns(line), len(header))
			if len(cols) < 2 || storeColumnsEmpty(cols) {
				continue
			}
			if isStoreBoxContinuationRow(header, cols, pendingBoxRow) {
				pendingBoxRow = appendStoreBoxContinuation(pendingBoxRow, cols)
				continue
			}
			flushPendingBoxRow()
			pendingBoxRow = cols
			continue
		}
		flushPendingBoxRow()
		cols := splitStoreColumns(line)
		cols = normalizeStoreColumnCount(cols, len(header))
		if pkg, ok := storePackageFromColumns(header, cols); ok {
			packages = append(packages, pkg)
		}
	}
	flushPendingBoxRow()
	return packages
}

func isStoreTableHeader(line string) bool {
	cols := splitStoreColumns(line)
	if len(cols) < 2 {
		return false
	}
	hasName := false
	hasKnownColumn := false
	for _, col := range cols {
		normalized := strings.ToLower(strings.TrimSpace(col))
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
		return nonEmptyStoreColumns(splitStoreBoxColumns(line))
	}
	return splitPackageTableColumns(line)
}

func splitStoreHeaderColumns(line string) []string {
	line = normalizeStoreTableDelimiters(line)
	if strings.Contains(line, "|") {
		return splitStoreBoxColumns(line)
	}
	return splitPackageTableColumns(line)
}

func splitStoreBoxColumns(line string) []string {
	line = strings.TrimSpace(normalizeStoreTableDelimiters(line))
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cols := make([]string, 0, len(parts))
	for _, part := range parts {
		cols = append(cols, strings.TrimSpace(part))
	}
	return cols
}

func nonEmptyStoreColumns(cols []string) []string {
	filtered := make([]string, 0, len(cols))
	for _, col := range cols {
		col = strings.TrimSpace(col)
		if col != "" {
			filtered = append(filtered, col)
		}
	}
	return filtered
}

func normalizeStoreColumnCount(cols []string, count int) []string {
	if count <= 0 {
		return cols
	}
	normalized := make([]string, count)
	for i := 0; i < count && i < len(cols); i++ {
		normalized[i] = strings.TrimSpace(cols[i])
	}
	if len(cols) > count {
		normalized[count-1] = strings.TrimSpace(strings.Join(append([]string{normalized[count-1]}, cols[count:]...), " "))
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

func isStoreSearchNoiseLine(line string) bool {
	return isStoreOutputNoiseLine(line)
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

func looksLikePackageID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, ".") || strings.Contains(value, "_") {
		return true
	}
	return isSafePackageID(value) && strings.ToUpper(value) == value && len(value) >= 8
}

func storePackageFromColumns(header, cols []string) (Package, bool) {
	if len(cols) < 2 {
		return Package{}, false
	}
	name := storeColumnValue(header, cols, "name", "app", "application")
	if name == "" && len(cols) > 0 {
		name = strings.TrimSpace(cols[0])
	}
	if name == "" || strings.HasPrefix(name, "[") || isStoreOutputNoiseLine(name) {
		return Package{}, false
	}
	id := storeColumnValue(header, cols, "id", "product id", "package id")
	if id == "" && len(cols) > 1 && looksLikePackageID(cols[1]) {
		id = strings.TrimSpace(cols[1])
	}
	if id == "" {
		id = name
	}
	version := storeColumnValue(header, cols, "current")
	available := storeColumnValue(header, cols, "available")
	if version == "" {
		version = storeColumnValue(header, cols, "version")
	}
	if version == "" {
		for i := 1; i < len(cols); i++ {
			if looksLikeVersion(cols[i]) {
				if version == "" {
					version = cols[i]
				} else if available == "" {
					available = cols[i]
				}
			}
		}
	}
	return Package{
		ID:               strings.TrimSpace(id),
		Name:             strings.TrimSpace(name),
		Version:          strings.TrimSpace(version),
		AvailableVersion: strings.TrimSpace(available),
		Manager:          managerStore,
		Source:           sourceStoreCLI,
		UpdateSupported:  true,
		ActionBackend:    backendStoreCLI,
	}, true
}

func storeColumnValue(header, cols []string, names ...string) string {
	index := storeColumnIndex(header, names...)
	if index < 0 || index >= len(cols) {
		return ""
	}
	return strings.TrimSpace(cols[index])
}

func storeColumnIndex(header []string, names ...string) int {
	for i, col := range header {
		normalized := strings.ToLower(strings.TrimSpace(col))
		for _, name := range names {
			if normalized == name {
				return i
			}
		}
	}
	return -1
}

func storeColumnsEmpty(cols []string) bool {
	for _, col := range cols {
		if strings.TrimSpace(col) != "" {
			return false
		}
	}
	return true
}

func isStoreBoxContinuationRow(header, cols, pending []string) bool {
	if len(pending) == 0 {
		return false
	}
	if index := storeColumnIndex(header, "current", "available", "version"); index >= 0 && index < len(cols) {
		if looksLikeVersion(cols[index]) {
			return false
		}
		return true
	}
	if index := storeColumnIndex(header, "id", "product id", "package id"); index >= 0 && index < len(cols) {
		return strings.TrimSpace(cols[index]) == ""
	}
	return false
}

func appendStoreBoxContinuation(row, continuation []string) []string {
	if len(row) < len(continuation) {
		expanded := make([]string, len(continuation))
		copy(expanded, row)
		row = expanded
	}
	for i, value := range continuation {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if row[i] == "" {
			row[i] = value
		} else {
			row[i] += " " + value
		}
	}
	return row
}

func storeSearch(query string) ([]Package, CommandResult) {
	result := runCommand(90*time.Second, managerCommand(managerStore, "search", query)...)
	return parseStoreSearch(result.Stdout + "\n" + result.Stderr), result
}

func storeInstalled() ([]Package, CommandResult) {
	result := runCommand(120*time.Second, managerCommand(managerStore, "installed")...)
	return parseStoreInstalled(result.Stdout + "\n" + result.Stderr), result
}

func storeUpdates() (map[string]string, []Package, CommandResult) {
	result := runCommand(120*time.Second, storeUpdatesCommand()...)
	output := result.Stdout + "\n" + result.Stderr
	return parseStoreUpdates(output), parseStoreUpdatePackages(output), result
}
