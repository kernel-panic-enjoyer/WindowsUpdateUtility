package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
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

type PackageLookup struct {
	Packages       []Package                `json:"packages"`
	Managers       map[string]ManagerStatus `json:"managers"`
	CommandResults map[string]CommandResult `json:"command_results"`
}

type Inventory struct {
	PackageLookup
	Scan InventoryScanSummary `json:"scan"`
}

type UpdateResult struct {
	Key    string        `json:"key"`
	Result CommandResult `json:"result"`
}

type InventoryScanSummary struct {
	LastScanAt    string `json:"last_scan_at,omitempty"`
	TrackedCount  int    `json:"tracked_count"`
	RegistryCount int    `json:"registry_count"`
	WingetCount   int    `json:"winget_count"`
	StoreCount    int    `json:"store_count"`
}

const (
	managerWinget = "winget"
	managerStore  = "store"
	managerChoco  = "choco"

	sourceWinget   = "winget"
	sourceMSStore  = "msstore"
	sourceStoreCLI = "store-cli"
	sourceAppX     = "appx"

	backendStoreCLI              = "store-cli"
	backendAppXInventory         = "appx-inventory"
	backendStoreCLIResolved      = "store-cli-resolved"
	backendWingetMSStoreFallback = "winget-msstore-fallback"
	inventoryBackendAppX         = "AppX"
)

var managedPackageManagers = []string{managerWinget, managerStore, managerChoco}

const managerValidationMessage = "manager must be winget, store, or choco"
const storeActionUnavailableMessage = "native Store CLI is unavailable and winget msstore fallback is unavailable"

const (
	managerDetectionTimeout = 20 * time.Second
	managerStatusCacheTTL   = 10 * time.Second
	wingetDetectionRetryGap = 350 * time.Millisecond
	packageActionTimeout    = time.Hour
	updateAllCommandTimeout = 2 * time.Hour
	storeResolveCacheTTL    = 6 * time.Hour
	storeUnresolvedCacheTTL = 0
)

type managerDetectionState struct {
	mu        sync.Mutex
	cached    map[string]ManagerStatus
	fetchedAt time.Time
	inFlight  chan struct{}
}

var managerDetectionCache = &managerDetectionState{}

func isManagedPackageManager(manager string) bool {
	for _, supported := range managedPackageManagers {
		if manager == supported {
			return true
		}
	}
	return false
}

func managerValidationError() error {
	return errors.New(managerValidationMessage)
}

func wingetSourceManager(source string) string {
	if strings.EqualFold(strings.TrimSpace(source), sourceMSStore) {
		return managerStore
	}
	return managerWinget
}

func managerSortRank(manager string) int {
	for index, supported := range managedPackageManagers {
		if manager == supported {
			return index
		}
	}
	return len(managedPackageManagers)
}

func wingetSourceListCommand() []string {
	return managerCommand(managerWinget, "source", "list", "--disable-interactivity")
}

func wingetSourceResetCommand() []string {
	return managerCommand(managerWinget, "source", "reset", "--force", "--disable-interactivity")
}

func detectManager(manager string) ManagerStatus {
	if manager == managerStore {
		return detectStoreCLIManager()
	}
	result := runCommand(managerDetectionTimeout, managerCommand(manager, "--version")...)
	if manager == managerWinget && isWingetTransientFailure(result) {
		appLog("Winget version detection failed with transient code %d; retrying once.", result.Code)
		time.Sleep(wingetDetectionRetryGap)
		result = runCommand(managerDetectionTimeout, managerCommand(manager, "--version")...)
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
	result := runCommand(managerDetectionTimeout, managerCommand(managerStore, "--help")...)
	status := ManagerStatus{
		Available: result.OK,
		Path:      resolveExecutable(managerStore),
	}
	if status.Available {
		status.Version = parseStoreHelpVersion(result.Stdout + "\n" + result.Stderr)
		status.ActionBackend = backendStoreCLI
		return status
	}
	if strings.TrimSpace(result.Stderr+result.Stdout) != "" {
		status.Error = strings.TrimSpace(result.Stderr + result.Stdout)
	} else {
		status.Error = "native Store CLI was not found"
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
	return detectManagersCached(false)
}

func detectManagersFresh() map[string]ManagerStatus {
	return detectManagersCached(true)
}

func detectManagersCached(force bool) map[string]ManagerStatus {
	for {
		managerDetectionCache.mu.Lock()
		if !force && managerDetectionCache.cached != nil && time.Since(managerDetectionCache.fetchedAt) < managerStatusCacheTTL {
			managers := cloneManagerStatuses(managerDetectionCache.cached)
			managerDetectionCache.mu.Unlock()
			return managers
		}
		if managerDetectionCache.inFlight != nil {
			inFlight := managerDetectionCache.inFlight
			managerDetectionCache.mu.Unlock()
			<-inFlight
			force = false
			continue
		}
		inFlight := make(chan struct{})
		managerDetectionCache.inFlight = inFlight
		managerDetectionCache.mu.Unlock()

		managers := detectManagersUncached()

		managerDetectionCache.mu.Lock()
		managerDetectionCache.cached = cloneManagerStatuses(managers)
		managerDetectionCache.fetchedAt = time.Now()
		managerDetectionCache.inFlight = nil
		close(inFlight)
		managerDetectionCache.mu.Unlock()
		return cloneManagerStatuses(managers)
	}
}

func invalidateManagerDetectionCache() {
	managerDetectionCache.mu.Lock()
	defer managerDetectionCache.mu.Unlock()
	managerDetectionCache.cached = nil
	managerDetectionCache.fetchedAt = time.Time{}
}

func cloneManagerStatuses(input map[string]ManagerStatus) map[string]ManagerStatus {
	cloned := make(map[string]ManagerStatus, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func detectManagersUncached() map[string]ManagerStatus {
	managers := map[string]ManagerStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, manager := range managedPackageManagers {
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
	store := managers[managerStore]
	if !store.Available && managers[managerWinget].Available {
		store.ActionBackend = backendWingetMSStoreFallback
		if store.Error == "" {
			store.Error = "native Store CLI was not found"
		}
		store.Error += "\nStore installs and updates can fall back to winget msstore when a compatible package ID is known."
		managers[managerStore] = store
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
		packages = append(packages, Package{ID: id, Name: id, Version: version, Manager: managerChoco})
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
		cols := splitColumns(line)
		if len(cols) < 3 {
			continue
		}
		pkg := Package{Name: cols[0], ID: cols[1], Version: cols[2], Manager: managerWinget}
		rest := cols[3:]
		if len(rest) > 0 {
			if isSourceToken(rest[len(rest)-1]) {
				pkg.Source = strings.ToLower(rest[len(rest)-1])
				pkg.Manager = wingetSourceManager(pkg.Source)
				rest = rest[:len(rest)-1]
			}
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
			packages = append(packages, Package{ID: id, Name: id, Version: item.Version, Manager: wingetSourceManager(sourceName), Source: sourceName})
		}
	}
	return packages
}

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
		if used[i] || isTruncatedID(pkg.ID) || exportedIDs[strings.ToLower(pkg.ID)] {
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

func chocoInstalled() ([]Package, CommandResult) {
	result := runCommand(90*time.Second, managerCommand(managerChoco, "list", "--local-only", "--limit-output", "--no-color")...)
	return parseChocoList(result.Stdout + "\n" + result.Stderr), result
}

func chocoUpdates() (map[string]string, CommandResult) {
	result := runCommand(120*time.Second, managerCommand(managerChoco, "outdated", "--limit-output", "--no-color")...)
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
		listResult = runCommand(120*time.Second, managerCommand(managerWinget, "list", "--accept-source-agreements", "--disable-interactivity")...)
		tablePackages = parseWingetTable(listResult.Stdout + "\n" + listResult.Stderr)
	}()
	if exportPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exportResult = runCommand(180*time.Second, managerCommand(managerWinget, "export", "-o", exportPath, "--include-versions", "--accept-source-agreements", "--disable-interactivity")...)
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
	updates := map[string]string{}
	result := runCommand(120*time.Second, managerCommand(managerWinget, "upgrade", "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(updates, result.Stdout+"\n"+result.Stderr, "")
	storeResult := runCommand(120*time.Second, managerCommand(managerWinget, "upgrade", "--source", sourceMSStore, "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(updates, storeResult.Stdout+"\n"+storeResult.Stderr, managerStore)
	return updates, mergeReadOnlyCommandResults(result, storeResult, "winget msstore update check")
}

func mergeWingetUpdateOutput(updates map[string]string, output, forceManager string) {
	for _, pkg := range parseWingetTable(output) {
		if pkg.AvailableVersion == "" || isTruncatedID(pkg.ID) {
			continue
		}
		manager := pkg.Manager
		if forceManager != "" {
			manager = forceManager
		}
		updates[packageKey(manager, strings.ToLower(pkg.ID))] = pkg.AvailableVersion
	}
}

func mergeReadOnlyCommandResults(primary, secondary CommandResult, label string) CommandResult {
	merged := primary
	if merged.Command != "" && secondary.Command != "" {
		merged.Command += "\n" + label + ": " + secondary.Command
	} else if secondary.Command != "" {
		merged.Command = secondary.Command
	}
	merged.Stdout = strings.TrimRight(primary.Stdout, "\r\n")
	if merged.Stdout != "" && secondary.Stdout != "" {
		merged.Stdout += "\n"
	}
	merged.Stdout += secondary.Stdout
	merged.Stderr = strings.TrimRight(primary.Stderr, "\r\n")
	if merged.Stderr != "" && secondary.Stderr != "" {
		merged.Stderr += "\n"
	}
	merged.Stderr += secondary.Stderr
	if primary.OK || secondary.OK {
		merged.OK = true
		merged.Code = 0
		return merged
	}
	if secondary.Code != 0 {
		merged.Code = secondary.Code
	}
	return merged
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
			updates[packageKey(managerStore, strings.ToLower(pkg.ID))] = available
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
		cols := splitStoreColumns(line)
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
			Manager:          managerStore,
			Source:           sourceStoreCLI,
			UpdateSupported:  true,
			ActionBackend:    backendStoreCLI,
		})
	}
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

func splitStoreColumns(line string) []string {
	line = normalizeStoreTableDelimiters(line)
	if strings.ContainsAny(line, "│┃|") {
		parts := strings.FieldsFunc(line, func(r rune) bool {
			return r == '│' || r == '┃' || r == '|'
		})
		cols := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				cols = append(cols, part)
			}
		}
		return cols
	}
	return splitColumns(line)
}

func normalizeStoreTableDelimiters(line string) string {
	return strings.NewReplacer(
		"â”‚", "|",
		"â”ƒ", "|",
		"â”Œ", "─",
		"â”", "─",
		"â””", "─",
		"â”˜", "─",
		"â”œ", "─",
		"â”¤", "─",
		"â”¬", "─",
		"â”´", "─",
		"â”¼", "─",
	).Replace(line)
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
	result := runCommand(90*time.Second, managerCommand(managerStore, "search", query)...)
	return parseStoreSearch(result.Stdout + "\n" + result.Stderr), result
}

func storeUpdates() (map[string]string, CommandResult) {
	result := runCommand(120*time.Second, managerCommand(managerStore, "updates")...)
	return parseStoreUpdates(result.Stdout + "\n" + result.Stderr), result
}

func appxInstalled() ([]Package, CommandResult) {
	script := `$ErrorActionPreference='Stop'
$startNames=@{}
try {
  Get-StartApps | ForEach-Object {
    $appId=[string]$_.AppID
    if ($appId -match '^([^!]+)!' -and -not $startNames.ContainsKey($matches[1])) {
      $startNames[$matches[1]]=[string]$_.Name
    }
  }
} catch {}
$packages=$null
try {
  $packages=Get-AppxPackage -AllUsers -PackageTypeFilter Main,Framework,Bundle,Optional
} catch {
  $packages=Get-AppxPackage -PackageTypeFilter Main,Framework,Bundle,Optional
}
$packages | ForEach-Object {
  $displayName=''
  $publisherDisplayName=''
  $startName=''
  if ($startNames.ContainsKey($_.PackageFamilyName)) { $startName=$startNames[$_.PackageFamilyName] }
  try {
    $manifest=Get-AppxPackageManifest -Package $_.PackageFullName
    $raw=[string]$manifest.Package.Properties.DisplayName
    if ($raw -and -not $raw.StartsWith('ms-resource:')) { $displayName=$raw }
    $publisherDisplayName=[string]$manifest.Package.Properties.PublisherDisplayName
  } catch {}
  [pscustomobject]@{Name=$_.Name;StartName=$startName;DisplayName=$displayName;PublisherDisplayName=$publisherDisplayName;PackageFullName=$_.PackageFullName;PackageFamilyName=$_.PackageFamilyName;Version=$_.Version.ToString();Publisher=$_.Publisher;InstallLocation=$_.InstallLocation}
} | ConvertTo-Json -Compress -Depth 3`
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
		StartName            string
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
			StartName            string
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
			Name:            friendlyAppxName(rawName, item.DisplayName, item.StartName),
			Version:         strings.TrimSpace(item.Version),
			Manager:         managerStore,
			Source:          sourceAppX,
			Match:           strings.TrimSpace(item.PackageFamilyName),
			UpdateSupported: false,
			ActionBackend:   backendAppXInventory,
		})
	}
	sort.Slice(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	return packages
}

func friendlyAppxName(name, displayName string, preferred ...string) string {
	for _, value := range preferred {
		if clean := cleanManifestDisplayName(value); clean != "" {
			return clean
		}
	}
	if clean := cleanManifestDisplayName(displayName); clean != "" {
		return clean
	}
	candidate := strings.TrimSpace(name)
	if candidate == "" {
		return candidate
	}
	if strings.Contains(candidate, ".") {
		candidate = friendlyDottedPackageIdentity(candidate)
	}
	candidate = regexp.MustCompile(`^\d+`).ReplaceAllString(candidate, "")
	candidate = strings.Trim(candidate, " ._-")
	candidate = splitJoinedWords(candidate)
	if candidate == "" {
		return strings.TrimSpace(name)
	}
	return candidate
}

func friendlyDottedPackageIdentity(name string) string {
	parts := strings.Split(strings.Trim(name, " ._"), ".")
	if len(parts) > 1 {
		parts = parts[1:]
	}
	if len(parts) >= 2 && numericString(parts[len(parts)-1]) && numericString(parts[len(parts)-2]) {
		version := parts[len(parts)-2] + "." + parts[len(parts)-1]
		parts = append(parts[:len(parts)-2], version)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func numericString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
	if looksLikeManifestPackageIdentity(displayName) {
		return ""
	}
	return displayName
}

func looksLikeManifestPackageIdentity(displayName string) bool {
	displayName = strings.TrimSpace(displayName)
	return strings.Contains(displayName, ".") && !strings.Contains(displayName, " ")
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
	seen := map[string]int{}
	markSeen := func(index int, value string) {
		normalized := normalizePackageIdentity(value)
		if normalized != "" {
			seen[normalized] = index
		}
	}
	for i, pkg := range packages {
		if pkg.Manager != managerStore {
			continue
		}
		markSeen(i, pkg.ID)
		markSeen(i, pkg.Name)
		markSeen(i, pkg.Match)
	}
	findDuplicate := func(pkg Package) (int, bool) {
		for _, value := range []string{pkg.ID, pkg.Name, pkg.Match} {
			normalized := normalizePackageIdentity(value)
			if normalized == "" {
				continue
			}
			if index, ok := seen[normalized]; ok {
				return index, true
			}
		}
		return -1, false
	}
	for _, pkg := range appxPackages {
		if index, ok := findDuplicate(pkg); ok {
			packages[index] = mergeStoreDuplicatePackage(packages[index], pkg)
			markSeen(index, packages[index].ID)
			markSeen(index, packages[index].Name)
			markSeen(index, packages[index].Match)
			continue
		}
		index := len(packages)
		packages = append(packages, pkg)
		markSeen(index, pkg.ID)
		markSeen(index, pkg.Name)
		markSeen(index, pkg.Match)
	}
	return packages
}

func mergeStoreDuplicatePackage(existing, appx Package) Package {
	if appx.Name != "" && (existing.Name == "" || existing.Name == existing.ID || appx.ActionBackend == backendStoreCLIResolved) {
		existing.Name = appx.Name
	}
	if appx.Version != "" {
		existing.Version = appx.Version
	}
	if appx.Match != "" {
		existing.Match = appx.Match
	}
	if appx.ActionBackend == backendStoreCLIResolved {
		existing.ID = appx.ID
		existing.Key = appx.Key
		existing.Source = appx.Source
		existing.ActionBackend = appx.ActionBackend
		existing.UpdateSupported = true
	}
	if appx.UpdateAvailable {
		existing.AvailableVersion = appx.AvailableVersion
		existing.UpdateAvailable = true
	}
	if appx.AutoUpdate {
		existing.AutoUpdate = true
	}
	existing.Installed = existing.Installed || appx.Installed
	return existing
}

func storeUpdateVersionForPackage(pkg Package, updates map[string]string) string {
	available, _ := storeUpdateForPackage(pkg, updates)
	return available
}

func storeUpdateForPackage(pkg Package, updates map[string]string) (string, string) {
	if pkg.Manager != managerStore || len(updates) == 0 {
		return "", ""
	}
	candidates := []string{pkg.Name, pkg.ID, stableStoreActionID(pkg.ID), pkg.Match, stableStoreActionID(pkg.Match)}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if available := updates[packageKey(managerStore, strings.ToLower(candidate))]; available != "" {
			return available, candidate
		}
	}
	return "", ""
}

func applyStoreUpdateVersion(pkg Package, updates map[string]string, storeAvailable bool) Package {
	available, target := storeUpdateForPackage(pkg, updates)
	if available == "" {
		return pkg
	}
	target = strings.TrimSpace(target)
	if target == "" {
		target = stableStoreActionID(pkg.ID)
	}
	if target != "" && target != pkg.ID {
		pkg.ID = target
		pkg.Key = packageKey(managerStore, target)
	}
	pkg.AvailableVersion = available
	pkg.UpdateAvailable = true
	pkg.UpdateSupported = true
	if pkg.ActionBackend == "" || pkg.ActionBackend == backendAppXInventory {
		if storeAvailable {
			pkg.ActionBackend = backendStoreCLIResolved
		} else {
			pkg.ActionBackend = backendWingetMSStoreFallback
		}
	}
	return pkg
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
		index     int
		pkg       Package
		key       string
		cached    StoreResolveCacheEntry
		hasCached bool
	}
	var jobs []job
	cacheChanged := false
	for i := range packages {
		if packages[i].Source != sourceAppX || packages[i].UpdateSupported {
			continue
		}
		cacheKey := strings.ToLower(packages[i].ID)
		if entry, ok := state.StoreResolveCache[cacheKey]; ok && entry.AppXVersion == packages[i].Version {
			if entry.Resolved && validStoreResolvedTargetForPackage(packages[i], entry) {
				packages[i] = applyStoreResolution(packages[i], entry)
				jobs = append(jobs, job{index: i, pkg: packages[i], key: cacheKey, cached: entry, hasCached: true})
				continue
			}
			if entry.Resolved {
				delete(state.StoreResolveCache, cacheKey)
				cacheChanged = true
				appLog("Store resolver discarded stale invalid mapping for %q.", packages[i].Name)
			} else if storeResolveUnresolvedCacheFresh(entry) {
				continue
			}
		}
		jobs = append(jobs, job{index: i, pkg: packages[i], key: cacheKey})
	}
	if len(jobs) == 0 {
		return packages, commandResults, cacheChanged
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
					entry.StoreVersion = latestPackageVersion(match)
				} else if item.hasCached && item.cached.Resolved && validStoreResolvedTargetForPackage(item.pkg, item.cached) {
					entry = item.cached
					entry.AppXVersion = item.pkg.Version
					entry.ResolvedAt = utcNow()
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
	pkg.ActionBackend = backendStoreCLIResolved
	pkg.Source = sourceAppX
	pkg.Match = strings.TrimSpace(entry.StoreName)
	storeVersion := strings.TrimSpace(entry.StoreVersion)
	if versionGreater(storeVersion, pkg.Version) {
		pkg.AvailableVersion = storeVersion
		pkg.UpdateAvailable = true
	} else {
		pkg.AvailableVersion = ""
		pkg.UpdateAvailable = false
	}
	return pkg
}

func storeResolveCacheFresh(entry StoreResolveCacheEntry) bool {
	if entry.ResolvedAt == "" {
		return false
	}
	resolvedAt, err := time.Parse(time.RFC3339, entry.ResolvedAt)
	if err != nil {
		return false
	}
	return time.Since(resolvedAt) < storeResolveCacheTTL
}

func storeResolveUnresolvedCacheFresh(entry StoreResolveCacheEntry) bool {
	if entry.ResolvedAt == "" {
		return false
	}
	resolvedAt, err := time.Parse(time.RFC3339, entry.ResolvedAt)
	if err != nil {
		return false
	}
	return time.Since(resolvedAt) < storeUnresolvedCacheTTL
}

func resolvedStoreTarget(entry StoreResolveCacheEntry) string {
	if strings.TrimSpace(entry.StoreID) != "" {
		return strings.TrimSpace(entry.StoreID)
	}
	return strings.TrimSpace(entry.StoreName)
}

func validStoreResolvedTarget(entry StoreResolveCacheEntry) bool {
	target := resolvedStoreTarget(entry)
	if target == "" || len(target) > 160 || isStoreSearchNoiseLine(target) || storeIDBlockedPattern.MatchString(target) {
		return false
	}
	if entry.StoreName != "" && (isStoreSearchNoiseLine(entry.StoreName) || storeIDBlockedPattern.MatchString(entry.StoreName)) {
		return false
	}
	return true
}

func validStoreResolvedTargetForPackage(pkg Package, entry StoreResolveCacheEntry) bool {
	if !validStoreResolvedTarget(entry) {
		return false
	}
	score := storeResolutionScore(storeResolutionCandidates(pkg), Package{
		Name:    entry.StoreName,
		ID:      entry.StoreID,
		Manager: managerStore,
	}, 0)
	return score >= 70
}

func latestPackageVersion(pkg Package) string {
	if strings.TrimSpace(pkg.AvailableVersion) != "" {
		return strings.TrimSpace(pkg.AvailableVersion)
	}
	return strings.TrimSpace(pkg.Version)
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
			if len(candidate) >= 5 && rank == 0 && strings.Contains(value, candidate) {
				return 70
			}
			if len(candidate) >= 5 && rank == 0 && strings.Contains(candidate, value) && len(value)*100/len(candidate) >= 80 {
				return 70
			}
		}
	}
	return 0
}

func versionGreater(candidate, current string) bool {
	candidateParts := versionParts(candidate)
	currentParts := versionParts(current)
	if len(candidateParts) == 0 || len(currentParts) == 0 {
		return false
	}
	maxParts := len(candidateParts)
	if len(currentParts) > maxParts {
		maxParts = len(currentParts)
	}
	for i := 0; i < maxParts; i++ {
		candidatePart := 0
		currentPart := 0
		if i < len(candidateParts) {
			candidatePart = candidateParts[i]
		}
		if i < len(currentParts) {
			currentPart = currentParts[i]
		}
		if candidatePart > currentPart {
			return true
		}
		if candidatePart < currentPart {
			return false
		}
	}
	return false
}

func versionParts(value string) []int {
	fields := regexp.MustCompile(`\D+`).Split(strings.TrimSpace(value), -1)
	parts := []int{}
	for _, field := range fields {
		if field == "" {
			continue
		}
		part, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		parts = append(parts, part)
	}
	return parts
}

func normalizePackageIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, "_8wekyb3d8bbwe")
	return regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(value, "")
}

func packageKey(manager, id string) string {
	return manager + ":" + id
}

func packageAutoUpdateEnabled(state State, pkg Package) bool {
	if state.AutoUpdatePackages[pkg.Key] {
		return true
	}
	normalizedKey := normalizeAutoUpdatePackageKey(pkg.Key)
	if state.AutoUpdatePackages[normalizedKey] {
		return true
	}
	for key, enabled := range state.AutoUpdatePackages {
		if enabled && equivalentPackageKeys(pkg.Key, key) {
			return true
		}
	}
	return false
}

func equivalentPackageKeys(left, right string) bool {
	leftManager, leftID, leftErr := splitPackageKey(left)
	rightManager, rightID, rightErr := splitPackageKey(right)
	if leftErr != nil || rightErr != nil || leftManager != rightManager {
		return false
	}
	if leftManager == managerStore {
		return strings.EqualFold(stableStoreActionID(leftID), stableStoreActionID(rightID))
	}
	return leftID == rightID
}

func splitPackageKey(key string) (string, string, error) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 || parts[1] == "" || !isManagedPackageManager(parts[0]) {
		return "", "", errors.New("package key must be manager:id")
	}
	return parts[0], parts[1], nil
}

var packageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.+\-:]+$`)
var storeIDBlockedPattern = regexp.MustCompile(`[\x00\r\n&|<>^"%]`)

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
	storeUpdateVersions := map[string]string{}

	inventoryCh := make(chan managerInventory, 2)
	var wg sync.WaitGroup
	var appxPackages []Package
	var appxResult CommandResult
	var nativeStoreUpdates map[string]string
	var nativeStoreUpdatesResult CommandResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		appxPackages, appxResult = appxInstalled()
	}()

	if managers[managerStore].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nativeStoreUpdates, nativeStoreUpdatesResult = storeUpdates()
		}()
	}

	if managers[managerWinget].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory(managerWinget, wingetInstalled, wingetUpdates, "winget_list", "winget_upgrade")
		}()
	}

	if managers[managerChoco].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inventoryCh <- collectManagerInventory(managerChoco, chocoInstalled, chocoUpdates, "choco_list", "choco_outdated")
		}()
	}
	wg.Wait()
	close(inventoryCh)
	commandResults["appx_inventory"] = appxResult
	if nativeStoreUpdatesResult.Command != "" {
		commandResults["store_updates"] = nativeStoreUpdatesResult
		for key, available := range nativeStoreUpdates {
			storeUpdateVersions[key] = available
		}
	}

	for inventory := range inventoryCh {
		commandResults[inventory.listKey] = inventory.listResult
		commandResults[inventory.updateKey] = inventory.updateResult
		if inventory.manager == managerWinget {
			for key, available := range inventory.updates {
				manager, _, err := splitPackageKey(key)
				if err == nil && manager == managerStore {
					storeUpdateVersions[key] = available
				}
			}
		}
		for _, pkg := range inventory.installed {
			displayManager := inventory.manager
			if inventory.manager == managerWinget {
				displayManager = wingetSourceManager(pkg.Source)
			}
			key := packageKey(displayManager, pkg.ID)
			available := inventory.updates[packageKey(displayManager, strings.ToLower(pkg.ID))]
			if available == "" && inventory.manager == managerWinget {
				available = pkg.AvailableVersion
			}
			pkg.Key = key
			pkg.Manager = displayManager
			pkg.AvailableVersion = available
			pkg.UpdateAvailable = available != ""
			pkg.UpdateSupported = true
			pkg.Installed = true
			pkg.AutoUpdate = packageAutoUpdateEnabled(state, pkg)
			if pkg.ActionBackend == "" {
				pkg.ActionBackend = displayManager
			}
			if displayManager == managerStore && managers[managerStore].Available {
				pkg.ActionBackend = backendStoreCLI
			} else if displayManager == managerStore {
				pkg.ActionBackend = backendWingetMSStoreFallback
			}
			packages = append(packages, pkg)
		}
	}

	if appxResult.OK || len(appxPackages) > 0 {
		store := managers[managerStore]
		store.InventoryAvailable = true
		store.InventoryBackend = inventoryBackendAppX
		if !store.Available && store.Error != "" {
			store.Error = strings.TrimSpace(store.Error + "\nStore app inventory is available through Windows AppX.")
		}
		managers[managerStore] = store
		if managers[managerStore].Available {
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
			appxPackages[i] = applyStoreUpdateVersion(appxPackages[i], storeUpdateVersions, managers[managerStore].Available)
			appxPackages[i].Key = packageKey(managerStore, appxPackages[i].ID)
			appxPackages[i].Installed = true
			if appxPackages[i].UpdateSupported {
				appxPackages[i].AutoUpdate = packageAutoUpdateEnabled(state, appxPackages[i])
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

	sourceCounts := managedScanSourceCounts(state)
	return Inventory{
		PackageLookup: PackageLookup{
			Packages:       packages,
			Managers:       managers,
			CommandResults: commandResults,
		},
		Scan: inventoryScanSummary(state, sourceCounts),
	}
}

func inventoryScanSummary(state State, sourceCounts map[string]int) InventoryScanSummary {
	return InventoryScanSummary{
		LastScanAt:    state.LastScanAt,
		TrackedCount:  len(state.RegistryApps) + managedScanTrackedCount(state),
		RegistryCount: len(state.RegistryApps),
		WingetCount:   sourceCounts[managerWinget],
		StoreCount:    sourceCounts[managerStore],
	}
}

func searchPackages(query string) (PackageLookup, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return PackageLookup{}, errors.New("search query cannot be empty")
	}
	appLog("Package search started for %q.", query)
	managers := detectManagers()
	foundPackages := []Package{}
	commandResults := map[string]CommandResult{}
	type searchResult struct {
		key      string
		packages []Package
		result   CommandResult
	}
	searchCh := make(chan searchResult, len(managedPackageManagers))
	var wg sync.WaitGroup

	if managers[managerStore].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			packages, result := storeSearch(query)
			for i := range packages {
				packages[i].Key = packageKey(managerStore, packages[i].ID)
				packages[i].UpdateSupported = true
				packages[i].ActionBackend = backendStoreCLI
			}
			searchCh <- searchResult{key: "store_search", packages: packages, result: result}
		}()
	}

	if managers[managerWinget].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			packages, result := wingetSearch(query)
			searchCh <- searchResult{key: managerWinget, packages: packages, result: result}
		}()
	}

	if managers[managerChoco].Available {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := runCommand(90*time.Second, managerCommand(managerChoco, "search", query, "--limit-output", "--no-color")...)
			packages := parseChocoList(result.Stdout + "\n" + result.Stderr)
			for i := range packages {
				packages[i].Key = packageKey(managerChoco, packages[i].ID)
			}
			searchCh <- searchResult{key: managerChoco, packages: packages, result: result}
		}()
	}
	wg.Wait()
	close(searchCh)
	for search := range searchCh {
		commandResults[search.key] = search.result
		foundPackages = append(foundPackages, search.packages...)
	}

	seen := map[string]bool{}
	packages := []Package{}
	for _, pkg := range foundPackages {
		key := strings.ToLower(packageKey(pkg.Manager, pkg.ID))
		if seen[key] {
			continue
		}
		seen[key] = true
		packages = append(packages, pkg)
	}
	sortSearchPackages(query, packages)
	appLog("Package search completed for %q with %d result(s).", query, len(packages))
	return PackageLookup{Packages: packages, Managers: managers, CommandResults: commandResults}, nil
}

func sortSearchPackages(query string, packages []Package) {
	sort.SliceStable(packages, func(i, j int) bool {
		leftScore := packageSearchScore(query, packages[i])
		rightScore := packageSearchScore(query, packages[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if packages[i].Manager != packages[j].Manager {
			return managerSortRank(packages[i].Manager) < managerSortRank(packages[j].Manager)
		}
		if len(packages[i].Name) != len(packages[j].Name) {
			return len(packages[i].Name) < len(packages[j].Name)
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
}

func packageSearchScore(query string, pkg Package) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	queryNorm := normalizePackageIdentity(query)
	primaryValues := []string{pkg.Name, pkg.ID}
	matchValues := []string{pkg.Match, wingetMatchValue(pkg.Match)}
	if valuesContainExact(primaryValues, query) {
		return 1200
	}
	if valuesContainExact(matchValues, query) {
		return 1100
	}
	if normalizedValuesContainExact(primaryValues, queryNorm) {
		return 1000
	}
	if normalizedValuesContainExact(matchValues, queryNorm) {
		return 950
	}
	if normalizedValuesHavePrefix(primaryValues, queryNorm) {
		return 700
	}
	if normalizedValuesHavePrefix(matchValues, queryNorm) {
		return 650
	}
	if normalizedValuesContain(primaryValues, queryNorm) {
		return 500
	}
	if normalizedValuesContain(matchValues, queryNorm) {
		return 450
	}
	return 0
}

func valuesContainExact(values []string, query string) bool {
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == query {
			return true
		}
	}
	return false
}

func normalizedValuesContainExact(values []string, query string) bool {
	for _, value := range values {
		if normalizePackageIdentity(value) == query {
			return true
		}
	}
	return false
}

func normalizedValuesHavePrefix(values []string, query string) bool {
	if query == "" {
		return false
	}
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized != "" && strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}

func normalizedValuesContain(values []string, query string) bool {
	if query == "" {
		return false
	}
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized != "" && strings.Contains(normalized, query) {
			return true
		}
	}
	return false
}

func wingetSearch(query string) ([]Package, CommandResult) {
	variants := searchQueryVariants(query)
	var cleanEmptyResult *CommandResult
	for index, candidate := range variants {
		result := runCommand(90*time.Second, managerCommand(managerWinget, "search", candidate, "--accept-source-agreements", "--disable-interactivity")...)
		packages := parseWingetSearchPackages(result)
		if len(packages) > 0 {
			return packages, result
		}
		if result.OK && cleanEmptyResult == nil {
			cleanEmptyResult = &result
		}
		if index == len(variants)-1 {
			if cleanEmptyResult != nil {
				return nil, *cleanEmptyResult
			}
			return nil, result
		}
	}
	return nil, CommandResult{Code: 1, Command: "winget search", Stderr: "no winget search variants were available"}
}

func parseWingetSearchPackages(result CommandResult) []Package {
	packages := []Package{}
	for _, pkg := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
		if !isTruncatedID(pkg.ID) {
			pkg.Manager = wingetSourceManager(pkg.Source)
			pkg.Key = packageKey(pkg.Manager, pkg.ID)
			pkg.UpdateSupported = true
			if pkg.Manager == managerStore {
				pkg.ActionBackend = backendWingetMSStoreFallback
			}
			packages = append(packages, pkg)
		}
	}
	return packages
}

func searchQueryVariants(query string) []string {
	query = strings.TrimSpace(query)
	variants := []string{query}
	normalized := strings.Join(regexp.MustCompile(`[-_.]+`).Split(query, -1), " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized != "" && !strings.EqualFold(normalized, query) {
		variants = append(variants, normalized)
	}
	return variants
}

func validateManagerAndID(manager, id string) error {
	if !isManagedPackageManager(manager) {
		return managerValidationError()
	}
	id = strings.TrimSpace(id)
	if manager == managerStore {
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
	if manager == managerStore {
		return sourceMSStore
	}
	return sourceWinget
}

var wingetInteractiveFlags = []string{
	"--accept-package-agreements",
	"--accept-source-agreements",
	"--disable-interactivity",
	"--silent",
}

func wingetPackageCommand(action, source, id string, extra ...string) []string {
	args := []string{action}
	if id != "" && packageIDPattern.MatchString(id) {
		args = append(args, "--id", id, "--exact")
	} else {
		args = append(args, id)
	}
	args = append(args, "--source", source)
	args = append(args, extra...)
	args = append(args, wingetInteractiveFlags...)
	return managerCommand(managerWinget, args...)
}

func wingetInstallCommand(manager, id string, force bool) []string {
	var extra []string
	if force {
		extra = append(extra, "--force")
	}
	return wingetPackageCommand("install", wingetSourceArg(manager), id, extra...)
}

func wingetUpgradeCommand(manager, id string) []string {
	return wingetPackageCommand("upgrade", wingetSourceArg(manager), id)
}

func wingetUpgradeAllCommand(source string) []string {
	args := []string{"upgrade", "--all", "--source", source}
	args = append(args, wingetInteractiveFlags...)
	return managerCommand(managerWinget, args...)
}

func chocoPackageCommand(action, id string) []string {
	return managerCommand(managerChoco, action, id, "-y", "--no-progress", "--no-color")
}

func storeCLIAvailable() bool {
	return detectManager(managerStore).Available
}

func storeActionUnavailableResult(action string) CommandResult {
	return CommandResult{Code: 1, Command: "store " + action, Stderr: storeActionUnavailableMessage}
}

func runStoreInstallWithFallback(id string) CommandResult {
	if storeCLIAvailable() {
		result := runCommand(packageActionTimeout, managerCommand(managerStore, "install", id)...)
		if result.OK || !detectManager(managerWinget).Available {
			return result
		}
		appLog("Store install for %q failed; trying winget msstore fallback.", id)
		fallback := runCommand(packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
		return mergeCommandResults(result, fallback, "winget msstore fallback")
	}
	if detectManager(managerWinget).Available {
		return runCommand(packageActionTimeout, wingetInstallCommand(managerStore, id, false)...)
	}
	return storeActionUnavailableResult("install")
}

func runStoreUpdateWithFallback(id string) CommandResult {
	return runStoreUpdateWithFallbackContext(context.Background(), id)
}

func runStoreUpdateWithFallbackContext(ctx context.Context, id string) CommandResult {
	if storeCLIAvailable() {
		result := runCommandContext(ctx, packageActionTimeout, managerCommand(managerStore, "update", id, "--apply", "true")...)
		if result.OK || !detectManager(managerWinget).Available {
			return result
		}
		if ctx.Err() != nil {
			return result
		}
		appLog("Store update for %q failed; trying winget msstore fallback.", id)
		fallback := runWingetUpgradeWithInstallFallbackContext(ctx, managerStore, id)
		return mergeCommandResults(result, fallback, "winget msstore fallback")
	}
	if detectManager(managerWinget).Available {
		return runWingetUpgradeWithInstallFallbackContext(ctx, managerStore, id)
	}
	return storeActionUnavailableResult("update")
}

func installPackage(manager, id string) CommandResult {
	if err := validateManagerAndID(manager, id); err != nil {
		return validationCommandResult("install", err)
	}
	appLog("Install started for %s:%s.", manager, id)
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerStore:
		result = runStoreInstallWithFallback(id)
	case managerWinget:
		result = runCommand(packageActionTimeout, wingetInstallCommand(manager, id, false)...)
	case managerChoco:
		result = runCommand(packageActionTimeout, chocoPackageCommand("install", id)...)
	}
	appLog("Install finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func updatePackage(manager, id string) CommandResult {
	return updatePackageContext(context.Background(), manager, id)
}

func updatePackageContext(ctx context.Context, manager, id string) CommandResult {
	if err := validateManagerAndID(manager, id); err != nil {
		return validationCommandResult("update", err)
	}
	appLog("Update started for %s:%s.", manager, id)
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerStore:
		result = runStoreUpdateWithFallbackContext(ctx, id)
	case managerWinget:
		result = runWingetUpgradeWithInstallFallbackContext(ctx, manager, id)
	case managerChoco:
		result = runCommandContext(ctx, packageActionTimeout, chocoPackageCommand("upgrade", id)...)
	}
	appLog("Update finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func runWingetUpgradeWithInstallFallback(manager, id string) CommandResult {
	return runWingetUpgradeWithInstallFallbackContext(context.Background(), manager, id)
}

func runWingetUpgradeWithInstallFallbackContext(ctx context.Context, manager, id string) CommandResult {
	result := runCommandContext(ctx, packageActionTimeout, wingetUpgradeCommand(manager, id)...)
	if shouldForceInstallAfterWingetUpgrade(result) {
		if ctx.Err() != nil {
			return result
		}
		appLog("Winget upgrade for %s:%s reported no applicable upgrade; trying forced install fallback.", manager, id)
		fallback := runCommandContext(ctx, packageActionTimeout, wingetInstallCommand(manager, id, true)...)
		return mergeCommandResults(result, fallback, "winget forced install fallback")
	}
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
	defer invalidateManagerDetectionCache()
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
				results = append(results, UpdateResult{Key: key, Result: validationCommandResult("update-all", err)})
				continue
			}
			results = append(results, UpdateResult{Key: key, Result: updatePackage(manager, id)})
		}
		appLog("Selected package update finished with %d result(s).", len(results))
		return results
	}

	managers := detectManagers()
	if managers[managerWinget].Available {
		results = append(results, UpdateResult{Key: packageKey(managerWinget, "*"), Result: runCommand(updateAllCommandTimeout, wingetUpgradeAllCommand(sourceWinget)...)})
	}
	if managers[managerStore].Available {
		results = append(results, UpdateResult{Key: packageKey(managerStore, "*"), Result: runCommand(updateAllCommandTimeout, managerCommand(managerStore, "updates")...)})
	} else if managers[managerWinget].Available {
		results = append(results, UpdateResult{Key: packageKey(managerStore, "*"), Result: runCommand(updateAllCommandTimeout, wingetUpgradeAllCommand(sourceMSStore)...)})
	}
	if managers[managerChoco].Available {
		results = append(results, UpdateResult{Key: packageKey(managerChoco, "*"), Result: runCommand(updateAllCommandTimeout, chocoPackageCommand("upgrade", "all")...)})
	}
	appLog("Update all finished with %d manager result(s).", len(results))
	return results
}

func installManager(manager string) CommandResult {
	appLog("Package manager install action started for %s.", manager)
	invalidateManagerDetectionCache()
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerWinget:
		err := openURL("ms-appinstaller:?source=https://aka.ms/getwinget")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open winget installer"}
			break
		}
		result = CommandResult{OK: true, Command: "open winget installer", Stdout: "Opened Microsoft App Installer for winget."}
	case managerStore:
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
	case managerChoco:
		if detectManager(managerWinget).Available {
			result = installPackage(managerWinget, "Chocolatey.Chocolatey")
			break
		}
		err := openURL("https://chocolatey.org/install")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open chocolatey install page"}
			break
		}
		result = CommandResult{OK: true, Command: "open chocolatey install page", Stdout: "Opened Chocolatey install page because winget is unavailable."}
	default:
		result = validationCommandResult("manager install", errors.New("unknown manager"))
	}
	appLog("Package manager install action finished for %s with code %d.", manager, result.Code)
	return result
}
