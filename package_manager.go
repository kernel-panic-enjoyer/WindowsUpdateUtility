package main

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ManagerStatus struct {
	Available          bool   `json:"available"`
	Version            string `json:"version,omitempty"`
	Path               string `json:"path,omitempty"`
	Error              string `json:"error,omitempty"`
	InventoryAvailable bool   `json:"inventory_available,omitempty"`
	InventoryBackend   string `json:"inventory_backend,omitempty"`
	ActionBackend      string `json:"action_backend,omitempty"`
}

type Package struct {
	Key              string `json:"key"`
	Manager          string `json:"manager"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Version          string `json:"version"`
	AvailableVersion string `json:"available_version"`
	UpdateAvailable  bool   `json:"update_available"`
	UpdateSupported  bool   `json:"update_supported"`
	Installed        bool   `json:"installed"`
	AutoUpdate       bool   `json:"auto_update"`
	Source           string `json:"source,omitempty"`
	Match            string `json:"match,omitempty"`
	ActionBackend    string `json:"action_backend,omitempty"`
}

type Inventory struct {
	Packages       []Package                `json:"packages"`
	Managers       map[string]ManagerStatus `json:"managers"`
	CommandResults map[string]CommandResult `json:"command_results"`
	Scan           map[string]any           `json:"scan"`
}

type UpdateResult struct {
	Key    string        `json:"key"`
	Result CommandResult `json:"result"`
}

func isManagedPackageManager(manager string) bool {
	return manager == "winget" || manager == "store" || manager == "choco"
}

func wingetSourceManager(source string) string {
	if strings.EqualFold(strings.TrimSpace(source), "msstore") {
		return "store"
	}
	return "winget"
}

func managerSortRank(manager string) int {
	switch manager {
	case "winget":
		return 0
	case "store":
		return 1
	case "choco":
		return 2
	default:
		return 3
	}
}

func wingetSourceListCommand() []string {
	return managerCommand("winget", "source", "list", "--disable-interactivity")
}

func wingetSourceResetCommand() []string {
	return managerCommand("winget", "source", "reset", "--force", "--disable-interactivity")
}

func detectManager(manager string) ManagerStatus {
	if manager == "store" {
		return detectStoreCLIManager()
	}
	result := runCommand(20*time.Second, managerCommand(manager, "--version")...)
	if manager == "winget" && isWingetTransientFailure(result) {
		appLog("Winget version detection failed with transient code %d; retrying once.", result.Code)
		time.Sleep(350 * time.Millisecond)
		result = runCommand(20*time.Second, managerCommand(manager, "--version")...)
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		output = strings.TrimSpace(result.Stderr)
	}
	status := ManagerStatus{Available: result.OK}
	if result.OK {
		lines := strings.Split(output, "\n")
		status.Version = strings.TrimSpace(lines[0])
		status.Path = resolveExecutable(manager)
	} else {
		status.Error = strings.TrimSpace(result.Stderr + result.Stdout)
	}
	return status
}

func detectStoreCLIManager() ManagerStatus {
	result := runCommand(20*time.Second, managerCommand("store", "--help")...)
	status := ManagerStatus{
		Available:          result.OK,
		Path:               resolveExecutable("store"),
		InventoryAvailable: appxInventoryAvailable(),
		InventoryBackend:   "AppX",
	}
	if status.Available {
		status.Version = parseStoreHelpVersion(result.Stdout + "\n" + result.Stderr)
		status.ActionBackend = "store-cli"
		return status
	}
	if strings.TrimSpace(result.Stderr+result.Stdout) != "" {
		status.Error = strings.TrimSpace(result.Stderr + result.Stdout)
	} else {
		status.Error = "native Store CLI was not found"
	}
	if status.InventoryAvailable {
		status.Error = strings.TrimSpace(status.Error + "\nStore app inventory is available through Windows AppX.")
	}
	return status
}

func parseStoreHelpVersion(output string) string {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "usage:") || strings.Contains(lower, "<command>") {
			continue
		}
		if strings.Contains(lower, "version") {
			return line
		}
	}
	return ""
}

func isWingetTransientFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	output := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	if strings.Contains(output, "another transaction") || strings.Contains(output, "currently running") {
		return true
	}
	return result.Code == 2316632065
}

func detectManagers() map[string]ManagerStatus {
	managers := map[string]ManagerStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, manager := range []string{"winget", "store", "choco"} {
		manager := manager
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := detectManager(manager)
			mu.Lock()
			managers[manager] = status
			mu.Unlock()
		}()
	}
	wg.Wait()
	store := managers["store"]
	if !store.Available && managers["winget"].Available {
		store.ActionBackend = "winget-msstore-fallback"
		if store.Error == "" {
			store.Error = "native Store CLI was not found"
		}
		store.Error += "\nStore installs and updates can fall back to winget msstore when a compatible package ID is known."
		managers["store"] = store
	}
	return managers
}

func parseChocoList(output string) []Package {
	var packages []Package
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		version := strings.TrimSpace(parts[1])
		if id == "" || version == "" || strings.Contains(id, " ") || strings.HasPrefix(strings.ToLower(id), "this is try") {
			continue
		}
		packages = append(packages, Package{ID: id, Name: id, Version: version, Manager: "choco"})
	}
	return packages
}

func parseChocoOutdated(output string) map[string]string {
	updates := map[string]string{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 3 {
			id := strings.ToLower(strings.TrimSpace(parts[0]))
			available := strings.TrimSpace(parts[2])
			if id != "" && available != "" {
				updates[id] = available
			}
		}
	}
	return updates
}

func splitColumns(line string) []string {
	return regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(line), -1)
}

func isSourceToken(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "winget" || value == "msstore"
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
		cols := splitColumns(line)
		if len(cols) < 3 {
			continue
		}
		pkg := Package{Name: cols[0], ID: cols[1], Version: cols[2], Manager: "winget"}
		rest := cols[3:]
		if len(rest) > 0 {
			if isSourceToken(rest[len(rest)-1]) {
				pkg.Source = strings.ToLower(rest[len(rest)-1])
				pkg.Manager = wingetSourceManager(pkg.Source)
				rest = rest[:len(rest)-1]
			}
		}
		if len(rest) > 0 {
			pkg.AvailableVersion = rest[0]
			if strings.HasPrefix(strings.ToLower(rest[0]), "tag:") {
				pkg.AvailableVersion = ""
				pkg.Match = rest[0]
			}
		}
		if len(rest) > 1 {
			pkg.Match = rest[1]
		}
		packages = append(packages, pkg)
	}
	return packages
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
			sourceName = "winget"
		}
		for _, item := range source.Packages {
			id := strings.TrimSpace(item.PackageIdentifier)
			if id == "" {
				continue
			}
			packages = append(packages, Package{ID: id, Name: id, Version: item.Version, Manager: wingetSourceManager(sourceName), Source: sourceName})
		}
	}
	return packages
}

func isTruncatedID(id string) bool {
	return strings.Contains(id, "…") || strings.Contains(id, "...")
}

func wingetIDMatches(fullID, tableID string) bool {
	full := strings.ToLower(fullID)
	table := strings.ToLower(tableID)
	if full == table {
		return true
	}
	if strings.Contains(table, "…") {
		return strings.HasPrefix(full, strings.Split(table, "…")[0])
	}
	if strings.Contains(table, "...") {
		return strings.HasPrefix(full, strings.Split(table, "...")[0])
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
		if used[i] || isTruncatedID(pkg.ID) || exportedIDs[strings.ToLower(pkg.ID)] {
			continue
		}
		if pkg.Source == "winget" || pkg.Source == "msstore" {
			merged = append(merged, pkg)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged
}

func chocoInstalled() ([]Package, CommandResult) {
	result := runCommand(90*time.Second, managerCommand("choco", "list", "--local-only", "--limit-output", "--no-color")...)
	return parseChocoList(result.Stdout + "\n" + result.Stderr), result
}

func chocoUpdates() (map[string]string, CommandResult) {
	result := runCommand(120*time.Second, managerCommand("choco", "outdated", "--limit-output", "--no-color")...)
	return parseChocoOutdated(result.Stdout + "\n" + result.Stderr), result
}

func wingetInstalled() ([]Package, CommandResult) {
	var listResult CommandResult
	var tablePackages []Package
	var exportResult CommandResult
	var exported []Package
	exportPath := ""
	if tmp, err := os.CreateTemp("", "windows-updater-winget-*.json"); err == nil {
		exportPath = tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(exportPath)
		defer os.Remove(exportPath)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		listResult = runCommand(120*time.Second, managerCommand("winget", "list", "--accept-source-agreements", "--disable-interactivity")...)
		tablePackages = parseWingetTable(listResult.Stdout + "\n" + listResult.Stderr)
	}()
	if exportPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exportResult = runCommand(180*time.Second, managerCommand("winget", "export", "-o", exportPath, "--include-versions", "--accept-source-agreements", "--disable-interactivity")...)
			exportOutput, _ := os.ReadFile(exportPath)
			exported = parseWingetExport(string(exportOutput))
		}()
	}
	wg.Wait()

	listResult.Stderr += exportResult.Stderr
	if len(exported) > 0 {
		return mergeWingetExportWithTable(exported, tablePackages), listResult
	}
	return tablePackages, listResult
}

func wingetUpdates() (map[string]string, CommandResult) {
	result := runCommand(120*time.Second, managerCommand("winget", "upgrade", "--accept-source-agreements", "--disable-interactivity")...)
	updates := map[string]string{}
	for _, pkg := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
		if pkg.AvailableVersion != "" && !isTruncatedID(pkg.ID) {
			updates[packageKey(pkg.Manager, strings.ToLower(pkg.ID))] = pkg.AvailableVersion
		}
	}
	return updates, result
}

func parseStoreSearch(output string) []Package {
	return parseStorePackageTable(output)
}

func parseStoreUpdates(output string) map[string]string {
	updates := map[string]string{}
	for _, pkg := range parseStorePackageTable(output) {
		if pkg.ID == "" {
			continue
		}
		available := pkg.AvailableVersion
		if available == "" {
			available = pkg.Version
		}
		if available != "" {
			updates[packageKey("store", strings.ToLower(pkg.ID))] = available
		}
	}
	return updates
}

func parseStorePackageTable(output string) []Package {
	lines := strings.Split(output, "\n")
	headerSeen := false
	var packages []Package
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || isStoreSearchNoiseLine(line) {
			continue
		}
		if !headerSeen {
			if isStoreTableHeader(line) {
				headerSeen = true
			}
			continue
		}
		if isStoreDividerLine(line) {
			continue
		}
		cols := splitColumns(line)
		if len(cols) < 2 {
			continue
		}
		name := strings.TrimSpace(cols[0])
		if name == "" || strings.HasPrefix(name, "[") || isStoreSearchNoiseLine(name) {
			continue
		}
		id := name
		version := ""
		available := ""
		if len(cols) > 1 {
			if looksLikePackageID(cols[1]) {
				id = cols[1]
			}
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
		packages = append(packages, Package{
			ID:               id,
			Name:             name,
			Version:          version,
			AvailableVersion: available,
			Manager:          "store",
			Source:           "store-cli",
			UpdateSupported:  true,
			ActionBackend:    "store-cli",
		})
	}
	return packages
}

func isStoreTableHeader(line string) bool {
	cols := splitColumns(line)
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
		case "id", "publisher", "version", "current", "available", "status", "price":
			hasKnownColumn = true
		}
	}
	return hasName && hasKnownColumn
}

func isStoreSearchNoiseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	normalized := normalizePackageIdentity(trimmed)
	if strings.Contains(normalized, "searchresultsfor") ||
		strings.HasPrefix(normalized, "resultsfor") ||
		strings.Contains(lower, "no results") {
		return true
	}
	return isStoreDividerLine(trimmed)
}

func isStoreDividerLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	nonDivider := 0
	for _, r := range line {
		if r == '-' || r == '_' || r == '=' || r == '─' || r == '—' || r == '―' || r == ' ' || r == '\t' {
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
	return packageIDPattern.MatchString(value) && strings.ToUpper(value) == value && len(value) >= 8
}

func storeSearch(query string) ([]Package, CommandResult) {
	result := runCommand(90*time.Second, managerCommand("store", "search", query)...)
	return parseStoreSearch(result.Stdout + "\n" + result.Stderr), result
}

func appxInventoryAvailable() bool {
	result := runCommand(20*time.Second, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "$ErrorActionPreference='Stop'; Get-AppxPackage | Select-Object -First 1 -ExpandProperty PackageFullName")
	return result.OK || strings.TrimSpace(result.Stdout) != ""
}

func appxInstalled() ([]Package, CommandResult) {
	script := "$ErrorActionPreference='Stop'; Get-AppxPackage | Where-Object { -not $_.IsFramework -and -not $_.NonRemovable } | ForEach-Object { $displayName=''; $publisherDisplayName=''; try { $manifest=Get-AppxPackageManifest -Package $_.PackageFullName; $raw=[string]$manifest.Package.Properties.DisplayName; if ($raw -and -not $raw.StartsWith('ms-resource:')) { $displayName=$raw }; $publisherDisplayName=[string]$manifest.Package.Properties.PublisherDisplayName } catch {}; [pscustomobject]@{Name=$_.Name;DisplayName=$displayName;PublisherDisplayName=$publisherDisplayName;PackageFullName=$_.PackageFullName;PackageFamilyName=$_.PackageFamilyName;Version=$_.Version.ToString();Publisher=$_.Publisher;InstallLocation=$_.InstallLocation} } | ConvertTo-Json -Compress -Depth 3"
	result := runCommand(90*time.Second, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	return parseAppxPackageJSON(result.Stdout), result
}

func parseAppxPackageJSON(output string) []Package {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var items []struct {
		Name                 string
		DisplayName          string
		PublisherDisplayName string
		PackageFullName      string
		PackageFamilyName    string
		Version              string
		Publisher            string
		InstallLocation      string
	}
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			return nil
		}
	} else {
		var item struct {
			Name                 string
			DisplayName          string
			PublisherDisplayName string
			PackageFullName      string
			PackageFamilyName    string
			Version              string
			Publisher            string
			InstallLocation      string
		}
		if err := json.Unmarshal([]byte(output), &item); err != nil {
			return nil
		}
		items = append(items, item)
	}
	var packages []Package
	for _, item := range items {
		id := strings.TrimSpace(item.PackageFullName)
		rawName := strings.TrimSpace(item.Name)
		if id == "" || rawName == "" {
			continue
		}
		packages = append(packages, Package{
			ID:              id,
			Name:            friendlyAppxName(rawName, item.DisplayName),
			Version:         strings.TrimSpace(item.Version),
			Manager:         "store",
			Source:          "appx",
			Match:           strings.TrimSpace(item.PackageFamilyName),
			UpdateSupported: false,
			ActionBackend:   "appx-inventory",
		})
	}
	sort.Slice(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	return packages
}

func friendlyAppxName(name, displayName string) string {
	if clean := cleanManifestDisplayName(displayName); clean != "" {
		return clean
	}
	candidate := strings.TrimSpace(name)
	if candidate == "" {
		return candidate
	}
	if strings.Contains(candidate, ".") {
		parts := strings.Split(candidate, ".")
		candidate = parts[len(parts)-1]
	}
	candidate = regexp.MustCompile(`^\d+`).ReplaceAllString(candidate, "")
	candidate = strings.Trim(candidate, " ._-")
	candidate = splitJoinedWords(candidate)
	if candidate == "" {
		return strings.TrimSpace(name)
	}
	return candidate
}

func cleanManifestDisplayName(displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return ""
	}
	lower := strings.ToLower(displayName)
	if strings.HasPrefix(lower, "ms-resource:") || strings.HasPrefix(lower, "@{") || strings.Contains(displayName, "\\") {
		return ""
	}
	return displayName
}

func splitJoinedWords(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	runes := []rune(value)
	var out []rune
	for i, r := range runes {
		if i > 0 && shouldInsertWordSpace(runes, i) {
			out = append(out, ' ')
		}
		out = append(out, r)
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func shouldInsertWordSpace(runes []rune, index int) bool {
	prev := runes[index-1]
	current := runes[index]
	var next rune
	if index+1 < len(runes) {
		next = runes[index+1]
	}
	if isLowerASCII(prev) && isUpperASCII(current) {
		return index+1 < len(runes)
	}
	if isUpperASCII(prev) && isUpperASCII(current) && isLowerASCII(next) {
		return true
	}
	if isDigitASCII(prev) && (isUpperASCII(current) || isLowerASCII(current)) {
		return true
	}
	return false
}

func isLowerASCII(r rune) bool { return r >= 'a' && r <= 'z' }

func isUpperASCII(r rune) bool { return r >= 'A' && r <= 'Z' }

func isDigitASCII(r rune) bool { return r >= '0' && r <= '9' }

func mergeStoreAppxPackages(packages, appxPackages []Package) []Package {
	seen := map[string]bool{}
	markSeen := func(value string) {
		normalized := normalizePackageIdentity(value)
		if normalized != "" {
			seen[normalized] = true
		}
	}
	isSeen := func(value string) bool {
		normalized := normalizePackageIdentity(value)
		return normalized != "" && seen[normalized]
	}
	for _, pkg := range packages {
		if pkg.Manager != "store" {
			continue
		}
		markSeen(pkg.ID)
		markSeen(pkg.Name)
	}
	for _, pkg := range appxPackages {
		if isSeen(pkg.ID) || isSeen(pkg.Name) || isSeen(pkg.Match) {
			continue
		}
		packages = append(packages, pkg)
		markSeen(pkg.ID)
		markSeen(pkg.Name)
		markSeen(pkg.Match)
	}
	return packages
}

type storeSearchFunc func(query string) ([]Package, CommandResult)

func resolveStoreAppxPackages(state *State, packages []Package, storeAvailable bool, search storeSearchFunc) ([]Package, map[string]CommandResult, bool) {
	commandResults := map[string]CommandResult{}
	if !storeAvailable || search == nil {
		return packages, commandResults, false
	}
	if state.StoreResolveCache == nil {
		state.StoreResolveCache = map[string]StoreResolveCacheEntry{}
	}

	type job struct {
		index int
		pkg   Package
		key   string
	}
	var jobs []job
	cacheChanged := false
	for i := range packages {
		if packages[i].Source != "appx" || packages[i].UpdateSupported {
			continue
		}
		cacheKey := strings.ToLower(packages[i].ID)
		if entry, ok := state.StoreResolveCache[cacheKey]; ok && entry.AppXVersion == packages[i].Version {
			if entry.Resolved && validStoreResolvedTarget(entry) {
				packages[i] = applyStoreResolution(packages[i], entry)
				continue
			}
			if entry.Resolved {
				delete(state.StoreResolveCache, cacheKey)
				cacheChanged = true
				appLog("Store resolver discarded stale invalid mapping for %q.", packages[i].Name)
			} else {
				continue
			}
		}
		jobs = append(jobs, job{index: i, pkg: packages[i], key: cacheKey})
	}
	if len(jobs) == 0 {
		return packages, commandResults, false
	}

	appLog("Store resolver started for %d inventory-only app(s).", len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	changed := cacheChanged

	for _, item := range jobs {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			query := item.pkg.Name
			results, result := search(query)

			mu.Lock()
			commandResults["store_resolve_"+normalizePackageIdentity(item.pkg.Name)] = result
			if result.OK {
				entry := StoreResolveCacheEntry{
					AppXVersion: item.pkg.Version,
					ResolvedAt:  utcNow(),
				}
				if match, ok := chooseStoreResolution(item.pkg, results); ok {
					entry.Resolved = true
					entry.StoreID = strings.TrimSpace(match.ID)
					entry.StoreName = strings.TrimSpace(match.Name)
				}
				state.StoreResolveCache[item.key] = entry
				changed = true
				if entry.Resolved {
					packages[item.index] = applyStoreResolution(item.pkg, entry)
					appLog("Store resolver mapped %q to %q.", item.pkg.Name, resolvedStoreTarget(entry))
				} else {
					appLog("Store resolver kept %q as inventory-only.", item.pkg.Name)
				}
			} else {
				appLog("Store resolver search failed for %q with code %d.", item.pkg.Name, result.Code)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	appLog("Store resolver completed for %d app(s).", len(jobs))
	return packages, commandResults, changed
}

func applyStoreResolution(pkg Package, entry StoreResolveCacheEntry) Package {
	target := resolvedStoreTarget(entry)
	if target == "" || !validStoreResolvedTarget(entry) {
		return pkg
	}
	pkg.ID = target
	pkg.UpdateSupported = true
	pkg.ActionBackend = "store-cli-resolved"
	pkg.Source = "appx"
	pkg.Match = strings.TrimSpace(entry.StoreName)
	return pkg
}

func resolvedStoreTarget(entry StoreResolveCacheEntry) string {
	if strings.TrimSpace(entry.StoreID) != "" {
		return strings.TrimSpace(entry.StoreID)
	}
	return strings.TrimSpace(entry.StoreName)
}

func validStoreResolvedTarget(entry StoreResolveCacheEntry) bool {
	target := resolvedStoreTarget(entry)
	if target == "" || len(target) > 160 || isStoreSearchNoiseLine(target) {
		return false
	}
	if entry.StoreName != "" && isStoreSearchNoiseLine(entry.StoreName) {
		return false
	}
	return true
}

func chooseStoreResolution(appx Package, results []Package) (Package, bool) {
	candidates := storeResolutionCandidates(appx)
	bestScore := 0
	bestIndex := -1
	for i, result := range results {
		if !validStoreResolvedTarget(StoreResolveCacheEntry{StoreID: result.ID, StoreName: result.Name, Resolved: true}) {
			continue
		}
		score := storeResolutionScore(candidates, result, i)
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestIndex >= 0 && bestScore >= 70 {
		return results[bestIndex], true
	}
	return Package{}, false
}

func storeResolutionCandidates(pkg Package) []string {
	values := []string{pkg.Name, pkg.ID, pkg.Match}
	for _, value := range []string{pkg.ID, pkg.Match} {
		base := strings.Split(strings.TrimSpace(value), "_")[0]
		values = append(values, base)
		if strings.Contains(base, ".") {
			parts := strings.Split(base, ".")
			values = append(values, parts[len(parts)-1])
		}
	}

	seen := map[string]bool{}
	var candidates []string
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		candidates = append(candidates, normalized)
	}
	return candidates
}

func storeResolutionScore(candidates []string, result Package, rank int) int {
	resultValues := []string{
		normalizePackageIdentity(result.Name),
		normalizePackageIdentity(result.ID),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, value := range resultValues {
			if value == "" {
				continue
			}
			if value == candidate {
				return 100
			}
			if len(candidate) >= 5 && rank == 0 && (strings.Contains(value, candidate) || strings.Contains(candidate, value)) {
				return 70
			}
		}
	}
	return 0
}

func normalizePackageIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, "_8wekyb3d8bbwe")
	return regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(value, "")
}

func packageKey(manager, id string) string {
	return manager + ":" + id
}

func splitPackageKey(key string) (string, string, error) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 || parts[1] == "" || !isManagedPackageManager(parts[0]) {
		return "", "", errors.New("package key must be manager:id")
	}
	return parts[0], parts[1], nil
}

var packageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.+\-:]+$`)
var storeIDBlockedPattern = regexp.MustCompile(`[\x00\r\n&|<>^"]`)

type managerInventory struct {
	manager      string
	installed    []Package
	listResult   CommandResult
	updates      map[string]string
	updateResult CommandResult
	listKey      string
	updateKey    string
}

func collectManagerInventory(
	manager string,
	installedFn func() ([]Package, CommandResult),
	updatesFn func() (map[string]string, CommandResult),
	listKey string,
	updateKey string,
) managerInventory {
	var installed []Package
	var listResult CommandResult
	var updates map[string]string
	var updateResult CommandResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		installed, listResult = installedFn()
	}()
	go func() {
		defer wg.Done()
		updates, updateResult = updatesFn()
	}()
	wg.Wait()
	return managerInventory{
		manager:      manager,
		installed:    installed,
		listResult:   listResult,
		updates:      updates,
		updateResult: updateResult,
		listKey:      listKey,
		updateKey:    updateKey,
	}
}

func getInventory() Inventory {
	state := loadState()
	managers := detectManagers()
	commandResults := map[string]CommandResult{}
	var packages []Package

	inventoryCh := make(chan managerInventory, 2)
	var wg sync.WaitGroup

	if managers["winget"].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory("winget", wingetInstalled, wingetUpdates, "winget_list", "winget_upgrade")
		}()
	}

	if managers["choco"].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory("choco", chocoInstalled, chocoUpdates, "choco_list", "choco_outdated")
		}()
	}
	wg.Wait()
	close(inventoryCh)

	for inventory := range inventoryCh {
		commandResults[inventory.listKey] = inventory.listResult
		commandResults[inventory.updateKey] = inventory.updateResult
		for _, pkg := range inventory.installed {
			displayManager := inventory.manager
			if inventory.manager == "winget" {
				displayManager = wingetSourceManager(pkg.Source)
			}
			key := packageKey(displayManager, pkg.ID)
			available := inventory.updates[packageKey(displayManager, strings.ToLower(pkg.ID))]
			if available == "" && inventory.manager == "winget" {
				available = pkg.AvailableVersion
			}
			pkg.Key = key
			pkg.Manager = displayManager
			pkg.AvailableVersion = available
			pkg.UpdateAvailable = available != ""
			pkg.UpdateSupported = true
			pkg.Installed = true
			pkg.AutoUpdate = state.AutoUpdatePackages[key]
			if pkg.ActionBackend == "" {
				pkg.ActionBackend = displayManager
			}
			if displayManager == "store" && managers["store"].Available {
				pkg.ActionBackend = "store-cli"
			} else if displayManager == "store" {
				pkg.ActionBackend = "winget-msstore-fallback"
			}
			packages = append(packages, pkg)
		}
	}

	if managers["store"].InventoryAvailable {
		appxPackages, result := appxInstalled()
		commandResults["appx_inventory"] = result
		if managers["store"].Available {
			var resolveResults map[string]CommandResult
			var changed bool
			appxPackages, resolveResults, changed = resolveStoreAppxPackages(&state, appxPackages, true, storeSearch)
			for key, result := range resolveResults {
				commandResults[key] = result
			}
			if changed {
				_ = saveState(state)
			}
		}
		for i := range appxPackages {
			appxPackages[i].Key = packageKey("store", appxPackages[i].ID)
			appxPackages[i].Installed = true
			if appxPackages[i].UpdateSupported {
				appxPackages[i].AutoUpdate = state.AutoUpdatePackages[appxPackages[i].Key]
			}
		}
		packages = mergeStoreAppxPackages(packages, appxPackages)
	}

	sort.Slice(packages, func(i, j int) bool {
		if strings.EqualFold(packages[i].Name, packages[j].Name) {
			return packages[i].Manager < packages[j].Manager
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})

	sourceCounts := scanSourceCounts(state.WingetApps)
	return Inventory{
		Packages:       packages,
		Managers:       managers,
		CommandResults: commandResults,
		Scan: map[string]any{
			"last_scan_at":   state.LastScanAt,
			"tracked_count":  len(state.RegistryApps) + len(state.WingetApps),
			"registry_count": len(state.RegistryApps),
			"winget_count":   sourceCounts["winget"],
			"store_count":    sourceCounts["store"],
		},
	}
}

func searchPackages(query string) ([]Package, map[string]ManagerStatus, map[string]CommandResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil, nil, errors.New("search query cannot be empty")
	}
	appLog("Package search started for %q.", query)
	managers := detectManagers()
	results := []Package{}
	commandResults := map[string]CommandResult{}

	if managers["store"].Available {
		packages, result := storeSearch(query)
		commandResults["store_search"] = result
		for _, pkg := range packages {
			pkg.Key = packageKey("store", pkg.ID)
			pkg.UpdateSupported = true
			pkg.ActionBackend = "store-cli"
			results = append(results, pkg)
		}
	}

	if managers["winget"].Available {
		result := runCommand(90*time.Second, managerCommand("winget", "search", query, "--accept-source-agreements", "--disable-interactivity")...)
		commandResults["winget"] = result
		for _, pkg := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
			if !isTruncatedID(pkg.ID) {
				pkg.Manager = wingetSourceManager(pkg.Source)
				pkg.Key = packageKey(pkg.Manager, pkg.ID)
				pkg.UpdateSupported = true
				if pkg.Manager == "store" {
					pkg.ActionBackend = "winget-msstore-fallback"
				}
				results = append(results, pkg)
			}
		}
	}

	if managers["choco"].Available {
		result := runCommand(90*time.Second, managerCommand("choco", "search", query, "--limit-output", "--no-color")...)
		commandResults["choco"] = result
		for _, pkg := range parseChocoList(result.Stdout + "\n" + result.Stderr) {
			pkg.Key = packageKey("choco", pkg.ID)
			results = append(results, pkg)
		}
	}

	seen := map[string]bool{}
	deduped := []Package{}
	for _, pkg := range results {
		key := strings.ToLower(packageKey(pkg.Manager, pkg.ID))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, pkg)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Manager == deduped[j].Manager {
			return strings.ToLower(deduped[i].Name) < strings.ToLower(deduped[j].Name)
		}
		return managerSortRank(deduped[i].Manager) < managerSortRank(deduped[j].Manager)
	})
	appLog("Package search completed for %q with %d result(s).", query, len(deduped))
	return deduped, managers, commandResults, nil
}

func validateManagerAndID(manager, id string) error {
	if !isManagedPackageManager(manager) {
		return errors.New("manager must be winget, store, or choco")
	}
	id = strings.TrimSpace(id)
	if manager == "store" {
		if id == "" || len(id) > 240 || storeIDBlockedPattern.MatchString(id) {
			return errors.New("store package id or query contains unsupported characters")
		}
		return nil
	}
	if id == "" || !packageIDPattern.MatchString(id) {
		return errors.New("package id contains unsupported characters")
	}
	return nil
}

func wingetSourceArg(manager string) string {
	if manager == "store" {
		return "msstore"
	}
	return "winget"
}

func wingetStoreCommand(action, id string) []string {
	base := []string{"winget", action}
	if packageIDPattern.MatchString(id) {
		base = append(base, "--id", id, "--exact")
	} else {
		base = append(base, id)
	}
	base = append(base, "--source", "msstore", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")
	return managerCommand(base[0], base[1:]...)
}

func storeCLIAvailable() bool {
	return detectManager("store").Available
}

func installPackage(manager, id string) CommandResult {
	if err := validateManagerAndID(manager, id); err != nil {
		return CommandResult{Code: 2, Stderr: err.Error(), Command: "install"}
	}
	appLog("Install started for %s:%s.", manager, id)
	var result CommandResult
	if manager == "store" {
		if storeCLIAvailable() {
			result = runCommand(3600*time.Second, managerCommand("store", "install", id)...)
		} else if detectManager("winget").Available {
			result = runCommand(3600*time.Second, wingetStoreCommand("install", id)...)
		} else {
			result = CommandResult{Code: 1, Command: "store install", Stderr: "native Store CLI is unavailable and winget msstore fallback is unavailable"}
		}
	} else if manager == "winget" {
		result = runCommand(3600*time.Second, managerCommand("winget", "install", "--id", id, "--exact", "--source", wingetSourceArg(manager), "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")...)
	} else {
		result = runCommand(3600*time.Second, managerCommand("choco", "install", id, "-y", "--no-progress", "--no-color")...)
	}
	appLog("Install finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func updatePackage(manager, id string) CommandResult {
	if err := validateManagerAndID(manager, id); err != nil {
		return CommandResult{Code: 2, Stderr: err.Error(), Command: "update"}
	}
	appLog("Update started for %s:%s.", manager, id)
	var result CommandResult
	if manager == "store" {
		if storeCLIAvailable() {
			result = runCommand(3600*time.Second, managerCommand("store", "update", id)...)
		} else if detectManager("winget").Available {
			result = runCommand(3600*time.Second, wingetStoreCommand("upgrade", id)...)
			if shouldForceInstallAfterWingetUpgrade(result) {
				appLog("Winget msstore upgrade reported no applicable upgrade for %s; trying forced install fallback.", id)
				fallbackArgs := wingetStoreCommand("install", id)
				fallbackArgs = append(fallbackArgs, "--force")
				fallback := runCommand(3600*time.Second, fallbackArgs...)
				result = mergeCommandResults(result, fallback, "winget forced install fallback")
			}
		} else {
			result = CommandResult{Code: 1, Command: "store update", Stderr: "native Store CLI is unavailable and winget msstore fallback is unavailable"}
		}
	} else if manager == "winget" {
		result = runCommand(3600*time.Second, managerCommand("winget", "upgrade", "--id", id, "--exact", "--source", wingetSourceArg(manager), "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")...)
		if shouldForceInstallAfterWingetUpgrade(result) {
			appLog("Winget upgrade reported no applicable upgrade for %s; trying forced install fallback.", id)
			fallback := runCommand(3600*time.Second, managerCommand("winget", "install", "--id", id, "--exact", "--source", wingetSourceArg(manager), "--force", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")...)
			result = mergeCommandResults(result, fallback, "winget forced install fallback")
		}
	} else {
		result = runCommand(3600*time.Second, managerCommand("choco", "upgrade", id, "-y", "--no-progress", "--no-color")...)
	}
	appLog("Update finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func shouldForceInstallAfterWingetUpgrade(result CommandResult) bool {
	if result.OK {
		return false
	}
	output := strings.ToLower(result.Stdout + "\n" + result.Stderr)
	return strings.Contains(output, "no applicable upgrade") ||
		strings.Contains(output, "kein anwendbares upgrade")
}

func mergeCommandResults(primary, fallback CommandResult, label string) CommandResult {
	merged := fallback
	merged.Command = primary.Command + "\n" + label + ": " + fallback.Command
	merged.Stdout = strings.TrimRight(primary.Stdout, "\r\n")
	if merged.Stdout != "" && fallback.Stdout != "" {
		merged.Stdout += "\n"
	}
	merged.Stdout += fallback.Stdout
	merged.Stderr = strings.TrimRight(primary.Stderr, "\r\n")
	if merged.Stderr != "" && fallback.Stderr != "" {
		merged.Stderr += "\n"
	}
	merged.Stderr += fallback.Stderr
	return merged
}

func updateAll(packageKeys []string) []UpdateResult {
	if len(packageKeys) > 0 {
		appLog("Selected package update started for %d package(s).", len(packageKeys))
	} else {
		appLog("Update all started.")
	}
	results := []UpdateResult{}
	if len(packageKeys) > 0 {
		for _, key := range packageKeys {
			manager, id, err := splitPackageKey(key)
			if err != nil {
				results = append(results, UpdateResult{Key: key, Result: CommandResult{Code: 2, Stderr: err.Error()}})
				continue
			}
			results = append(results, UpdateResult{Key: key, Result: updatePackage(manager, id)})
		}
		appLog("Selected package update finished with %d result(s).", len(results))
		return results
	}

	managers := detectManagers()
	if managers["winget"].Available {
		results = append(results, UpdateResult{Key: "winget:*", Result: runCommand(7200*time.Second, managerCommand("winget", "upgrade", "--all", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")...)})
	}
	if managers["store"].Available {
		results = append(results, UpdateResult{Key: "store:*", Result: runCommand(7200*time.Second, managerCommand("store", "updates")...)})
	} else if managers["winget"].Available {
		results = append(results, UpdateResult{Key: "store:*", Result: runCommand(7200*time.Second, managerCommand("winget", "upgrade", "--all", "--source", "msstore", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity", "--silent")...)})
	}
	if managers["choco"].Available {
		results = append(results, UpdateResult{Key: "choco:*", Result: runCommand(7200*time.Second, managerCommand("choco", "upgrade", "all", "-y", "--no-progress", "--no-color")...)})
	}
	appLog("Update all finished with %d manager result(s).", len(results))
	return results
}

func installManager(manager string) CommandResult {
	appLog("Package manager install action started for %s.", manager)
	var result CommandResult
	switch manager {
	case "winget":
		err := openURL("ms-appinstaller:?source=https://aka.ms/getwinget")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open winget installer"}
			break
		}
		result = CommandResult{OK: true, Command: "open winget installer", Stdout: "Opened Microsoft App Installer for winget."}
	case "store":
		var messages []string
		opened := false
		if err := openURL("ms-windows-store://downloadsandupdates"); err != nil {
			messages = append(messages, "Could not open Microsoft Store updates: "+err.Error())
		} else {
			opened = true
			messages = append(messages, "Opened Microsoft Store Downloads and updates.")
		}
		if err := openURL("ms-settings:windowsupdate"); err != nil {
			messages = append(messages, "Could not open Windows Update settings: "+err.Error())
		} else {
			opened = true
			messages = append(messages, "Opened Windows Update settings.")
		}
		result = CommandResult{OK: opened, Command: "open Store CLI update surfaces", Stdout: strings.Join(messages, "\n")}
		if !opened {
			result.Code = 1
			result.Stderr = result.Stdout
			result.Stdout = ""
		}
	case "choco":
		if detectManager("winget").Available {
			result = installPackage("winget", "Chocolatey.Chocolatey")
			break
		}
		err := openURL("https://chocolatey.org/install")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open chocolatey install page"}
			break
		}
		result = CommandResult{OK: true, Command: "open chocolatey install page", Stdout: "Opened Chocolatey install page because winget is unavailable."}
	default:
		result = CommandResult{Code: 2, Stderr: "unknown manager", Command: "manager install"}
	}
	appLog("Package manager install action finished for %s with code %d.", manager, result.Code)
	return result
}
