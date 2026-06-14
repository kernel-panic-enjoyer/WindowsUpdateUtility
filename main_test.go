package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseChocoListIgnoresWarnings(t *testing.T) {
	output := `
This is try 1/3. Retrying after 300 milliseconds.
Error converted to warning:
git|2.54.0
python314|3.14.5
`
	got := parseChocoList(output)
	if len(got) != 2 {
		t.Fatalf("expected 2 packages, got %d: %#v", len(got), got)
	}
	if got[0].ID != "git" || got[1].ID != "python314" {
		t.Fatalf("unexpected packages: %#v", got)
	}
}

func TestParseChocoOutdated(t *testing.T) {
	got := parseChocoOutdated("git|2.53.0|2.54.0|false\npython314|3.14.4|3.14.5|false\n")
	if got["git"] != "2.54.0" || got["python314"] != "3.14.5" {
		t.Fatalf("unexpected outdated map: %#v", got)
	}
}

func TestParseLocalizedWingetTable(t *testing.T) {
	output := `
Name      ID              Version  Verfuegbar Quelle
---------------------------------------------------
Git       Git.Git         2.53.0   2.54.0    winget
Zed       Zed.Zed         0.233.10           winget
`
	got := parseWingetTable(output)
	if len(got) != 2 {
		t.Fatalf("expected 2 packages, got %d: %#v", len(got), got)
	}
	if got[0].ID != "Git.Git" || got[0].AvailableVersion != "2.54.0" {
		t.Fatalf("unexpected first package: %#v", got[0])
	}
	if got[1].Source != "winget" {
		t.Fatalf("expected source winget, got %#v", got[1])
	}
}

func TestParseWingetSearchTableWithMatchColumn(t *testing.T) {
	output := `
Name                         ID                                 Version   Uebereinstimmung Quelle
-----------------------------------------------------------------------------------------------
DragonframeLicenseManager    DZEDSystems.DragonframeLicenseMa... 3.0.3                    winget
Zed                          ZedIndustries.Zed                  1.6.3     Tag: zed       winget
`
	got := parseWingetTable(output)
	if len(got) != 2 {
		t.Fatalf("expected 2 packages, got %d: %#v", len(got), got)
	}
	if !isTruncatedID(got[0].ID) {
		t.Fatalf("expected truncated id: %#v", got[0])
	}
	if got[1].Source != "winget" {
		t.Fatalf("expected resilient source parsing, got %#v", got[1])
	}
}

func TestParseWingetTableMapsMicrosoftStoreSource(t *testing.T) {
	output := `
Name              ID                       Version  Available Source
--------------------------------------------------------------------
Microsoft To Do   9NBLGGH5R558             2.130.0  2.131.0   msstore
PowerToys         Microsoft.PowerToys      0.95.0             winget
`
	got := parseWingetTable(output)
	if len(got) != 2 {
		t.Fatalf("expected 2 packages, got %d: %#v", len(got), got)
	}
	if got[0].Manager != "store" || got[0].Source != "msstore" || got[0].AvailableVersion != "2.131.0" {
		t.Fatalf("expected msstore row to map to store manager: %#v", got[0])
	}
	if got[1].Manager != "winget" || got[1].Source != "winget" {
		t.Fatalf("expected winget row to remain winget: %#v", got[1])
	}
}

func TestParseWingetExport(t *testing.T) {
	output := `{
  "Sources": [{
    "Packages": [{"PackageIdentifier": "ZedIndustries.Zed", "Version": "1.5.4"}],
    "SourceDetails": {"Name": "winget"}
  }]
}`
	got := parseWingetExport(output)
	if len(got) != 1 || got[0].ID != "ZedIndustries.Zed" || got[0].Version != "1.5.4" || got[0].Source != "winget" {
		t.Fatalf("unexpected export parse: %#v", got)
	}
}

func TestParseWingetExportMapsMicrosoftStoreSource(t *testing.T) {
	output := `{
  "Sources": [{
    "Packages": [{"PackageIdentifier": "9NBLGGH5R558", "Version": "2.130.0"}],
    "SourceDetails": {"Name": "msstore"}
  }]
}`
	got := parseWingetExport(output)
	if len(got) != 1 || got[0].Manager != "store" || got[0].Source != "msstore" {
		t.Fatalf("unexpected store export parse: %#v", got)
	}
}

func TestMergeWingetExportWithTruncatedTableIDs(t *testing.T) {
	exported := []Package{
		{ID: "Microsoft.VCRedist.2015+.x64", Name: "Microsoft.VCRedist.2015+.x64", Version: "14.51.36231.0", Manager: "winget", Source: "winget"},
		{ID: "ZedIndustries.Zed", Name: "ZedIndustries.Zed", Version: "1.5.4", Manager: "winget", Source: "winget"},
	}
	table := []Package{
		{ID: "Microsoft.VCRedist.2015+.x...", Name: "Microsoft Visual C++ 2015-2026 Redistributable", Version: "14.51.36231.0", AvailableVersion: "14.51.36247.0", Manager: "winget", Source: "winget"},
		{ID: "ZedIndustries.Zed", Name: "Zed", Version: "1.5.4", AvailableVersion: "1.6.3", Manager: "winget", Source: "winget"},
	}
	got := mergeWingetExportWithTable(exported, table)
	byID := map[string]Package{}
	for _, pkg := range got {
		byID[pkg.ID] = pkg
	}
	if byID["Microsoft.VCRedist.2015+.x64"].AvailableVersion != "14.51.36247.0" {
		t.Fatalf("truncated id did not merge: %#v", byID["Microsoft.VCRedist.2015+.x64"])
	}
	if byID["ZedIndustries.Zed"].Name != "Zed" {
		t.Fatalf("display name did not merge: %#v", byID["ZedIndustries.Zed"])
	}
}

func TestParseRegQuery(t *testing.T) {
	output := `
HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Uninstall\Git_is1
    DisplayName    REG_SZ    Git
    DisplayVersion    REG_SZ    2.54.0
    Publisher    REG_SZ    The Git Development Community
    InstallLocation    REG_SZ    C:\Program Files\Git
`
	got := parseRegQuery(output, "HKLM")
	if len(got) != 1 {
		t.Fatalf("expected one app, got %#v", got)
	}
	if got[0].Name != "Git" || got[0].RegistryHive != "HKLM" || got[0].Source != "registry" {
		t.Fatalf("unexpected registry app: %#v", got[0])
	}
}

func TestDiffSnapshot(t *testing.T) {
	previous := map[string]ScannedApp{
		"winget:git.git": {Key: "winget:git.git", Name: "Git", FirstSeen: "old"},
	}
	current := []ScannedApp{
		{Key: "winget:git.git", Name: "Git"},
		{Key: "winget:zed.zed", Name: "Zed"},
	}
	currentMap, newApps, removed, baseline := diffSnapshot(current, previous)
	if baseline {
		t.Fatal("expected non-baseline diff")
	}
	if len(newApps) != 1 || newApps[0].Key != "winget:zed.zed" {
		t.Fatalf("unexpected new apps: %#v", newApps)
	}
	if len(removed) != 0 {
		t.Fatalf("unexpected removed apps: %#v", removed)
	}
	if currentMap["winget:git.git"].FirstSeen != "old" {
		t.Fatalf("first_seen was not preserved: %#v", currentMap["winget:git.git"])
	}
}

func TestStateDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	got, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("expected override %s, got %s", dir, got)
	}
}

func TestManagerCommandOverride(t *testing.T) {
	t.Setenv("UPDATER_WINGET_PATH", filepath.Join("C:", "Tools", "winget.exe"))
	got := managerCommand("winget", "--version")
	if len(got) != 2 || got[0] != filepath.Join("C:", "Tools", "winget.exe") || got[1] != "--version" {
		t.Fatalf("unexpected manager command: %#v", got)
	}

	t.Setenv("UPDATER_STORE_PATH", filepath.Join("C:", "Tools", "store.exe"))
	got = managerCommand("store", "--help")
	if len(got) != 2 || got[0] != filepath.Join("C:", "Tools", "store.exe") || got[1] != "--help" {
		t.Fatalf("unexpected store manager command: %#v", got)
	}
}

func TestWingetSourceCommandsAvoidUnsupportedAgreementFlag(t *testing.T) {
	for _, command := range [][]string{wingetSourceListCommand(), wingetSourceResetCommand()} {
		joined := strings.Join(command, " ")
		if strings.Contains(joined, "--accept-source-agreements") {
			t.Fatalf("winget source command included unsupported agreement flag: %#v", command)
		}
		if !strings.Contains(joined, "--disable-interactivity") {
			t.Fatalf("winget source command should disable interactivity: %#v", command)
		}
	}
}

func TestIsWingetCommand(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{filepath.Join("C:", "Users", "User", "AppData", "Local", "Microsoft", "WindowsApps", "winget.exe"), "--version"}, true},
		{[]string{"winget", "--version"}, true},
		{[]string{"cmd.exe", "/d", "/c", "winget", "--version"}, true},
		{[]string{"choco", "--version"}, false},
		{[]string{"cmd.exe", "/c", "winget", "--version"}, false},
	}
	for _, tc := range cases {
		if got := isWingetCommand(tc.args); got != tc.want {
			t.Fatalf("isWingetCommand(%#v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestWingetTransientFailureDetection(t *testing.T) {
	if !isWingetTransientFailure(CommandResult{Code: 2316632065}) {
		t.Fatal("expected App Installer winget code to be transient")
	}
	if !isWingetTransientFailure(CommandResult{Code: 1, Stderr: "Another transaction is currently running"}) {
		t.Fatal("expected concurrent transaction message to be transient")
	}
	if isWingetTransientFailure(CommandResult{OK: true, Code: 0}) {
		t.Fatal("successful command must not be transient")
	}
	if isWingetTransientFailure(CommandResult{Code: 1, Stderr: "ordinary failure"}) {
		t.Fatal("ordinary failure should not be treated as transient")
	}
}

func TestIsStoreCommand(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{filepath.Join("C:", "Users", "User", "AppData", "Local", "Microsoft", "WindowsApps", "store.exe"), "--help"}, true},
		{[]string{"store", "--help"}, true},
		{[]string{"cmd.exe", "/d", "/c", "store", "--help"}, true},
		{[]string{"winget", "--version"}, false},
		{[]string{"cmd.exe", "/c", "store", "--help"}, false},
	}
	for _, tc := range cases {
		if got := isStoreCommand(tc.args); got != tc.want {
			t.Fatalf("isStoreCommand(%#v) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestStorePackageKeysAreValid(t *testing.T) {
	for rank, manager := range managedPackageManagers {
		if !isManagedPackageManager(manager) {
			t.Fatalf("%q should be accepted by manager validation", manager)
		}
		if managerSortRank(manager) != rank {
			t.Fatalf("%q sort rank should be %d, got %d", manager, rank, managerSortRank(manager))
		}
	}
	if isManagedPackageManager("npm") {
		t.Fatal("unexpected manager accepted")
	}
	if managerValidationError().Error() != managerValidationMessage {
		t.Fatalf("unexpected manager validation message: %q", managerValidationError().Error())
	}

	manager, id, err := splitPackageKey("store:9NBLGGH5R558")
	if err != nil {
		t.Fatal(err)
	}
	if manager != "store" || id != "9NBLGGH5R558" {
		t.Fatalf("unexpected store package key split: %q %q", manager, id)
	}
	if err := validateManagerAndID("store", "9NBLGGH5R558"); err != nil {
		t.Fatalf("store manager should validate: %v", err)
	}
	if wingetSourceArg("store") != "msstore" {
		t.Fatal("store manager should use the msstore winget source")
	}
	if err := validateManagerAndID("store", "Microsoft To Do"); err != nil {
		t.Fatalf("store queries with spaces should validate: %v", err)
	}
	if err := validateManagerAndID("store", "bad&query"); err == nil {
		t.Fatal("store queries with shell metacharacters should be rejected")
	}
}

func TestPackageCommandBuilders(t *testing.T) {
	wingetInstall := strings.Join(wingetInstallCommand("winget", "Git.Git", false), " ")
	for _, expected := range []string{"winget install", "--id Git.Git --exact", "--source winget", "--accept-package-agreements", "--disable-interactivity", "--silent"} {
		if !strings.Contains(wingetInstall, expected) {
			t.Fatalf("winget install command missing %q: %s", expected, wingetInstall)
		}
	}
	if strings.Contains(wingetInstall, "--force") {
		t.Fatalf("normal install should not include force: %s", wingetInstall)
	}

	forcedStoreInstall := strings.Join(wingetInstallCommand("store", "Microsoft To Do", true), " ")
	for _, expected := range []string{"winget install", "Microsoft To Do", "--source msstore", "--force"} {
		if !strings.Contains(forcedStoreInstall, expected) {
			t.Fatalf("forced store install command missing %q: %s", expected, forcedStoreInstall)
		}
	}

	chocoUpgrade := strings.Join(chocoPackageCommand("upgrade", "git"), " ")
	for _, expected := range []string{"upgrade git", "-y", "--no-progress", "--no-color"} {
		if !strings.Contains(chocoUpgrade, expected) {
			t.Fatalf("choco command missing %q: %s", expected, chocoUpgrade)
		}
	}
}

func TestParseStoreSearchAndUpdates(t *testing.T) {
	searchOutput := `
Name             ID              Publisher
------------------------------------------
Microsoft To Do  9NBLGGH5R558    Microsoft
Codex            OpenAI.Codex    OpenAI
`
	search := parseStoreSearch(searchOutput)
	if len(search) != 2 {
		t.Fatalf("expected two store search results, got %#v", search)
	}
	if search[0].Manager != "store" || search[0].ActionBackend != "store-cli" || search[0].ID != "9NBLGGH5R558" {
		t.Fatalf("unexpected store search parse: %#v", search[0])
	}

	updateOutput := `
Name   ID            Current  Available
---------------------------------------
Codex  OpenAI.Codex  1.0.0    1.1.0
`
	updates := parseStoreUpdates(updateOutput)
	if updates["store:openai.codex"] != "1.1.0" {
		t.Fatalf("unexpected store updates parse: %#v", updates)
	}
}

func TestParseStoreSearchSkipsBannerLines(t *testing.T) {
	output := `
Application Compatibility Enhancements
-- Search Results for
"Application Compatibility Enhancements"
--------------------------------------
Name                                    ID                                     Version
------------------------------------------------------------------------------------
Application Compatibility Enhancements  Microsoft.ApplicationCompatibility     1.2511.9.0
`
	got := parseStoreSearch(output)
	if len(got) != 1 {
		t.Fatalf("expected one parsed search result, got %#v", got)
	}
	if got[0].ID != "Microsoft.ApplicationCompatibility" || strings.Contains(got[0].ID, "Search Results") {
		t.Fatalf("store search banner was parsed as a result: %#v", got[0])
	}
}

func TestParseStoreHelpVersionIgnoresUsageBanner(t *testing.T) {
	output := `Usage: store <command> [options]

Commands:
  install
  search
`
	if got := parseStoreHelpVersion(output); got != "" {
		t.Fatalf("usage banner should not be treated as a version, got %q", got)
	}
	if got := parseStoreHelpVersion("Store CLI version 1.2.3"); got != "Store CLI version 1.2.3" {
		t.Fatalf("expected version-like line to be preserved, got %q", got)
	}
}

func TestParseAppxPackageJSON(t *testing.T) {
	output := `[
{"Name":"OpenAI.Codex","DisplayName":"Codex","PackageFullName":"OpenAI.Codex_1.0.0.0_x64__abc123","PackageFamilyName":"OpenAI.Codex_abc123","Version":"1.0.0.0","Publisher":"CN=OpenAI","InstallLocation":"C:\\Program Files\\WindowsApps\\OpenAI.Codex"},
{"Name":"Microsoft.Todos","DisplayName":"ms-resource:AppName","PackageFullName":"Microsoft.Todos_2.130.0.0_x64__8wekyb3d8bbwe","PackageFamilyName":"Microsoft.Todos_8wekyb3d8bbwe","Version":"2.130.0.0","Publisher":"CN=Microsoft","InstallLocation":"C:\\Program Files\\WindowsApps\\Microsoft.Todos"}
]`
	got := parseAppxPackageJSON(output)
	if len(got) != 2 {
		t.Fatalf("expected two AppX packages, got %#v", got)
	}
	if got[0].Name != "Codex" || got[1].Name != "Todos" {
		t.Fatalf("expected friendly AppX display names, got %#v", got)
	}
	if got[0].Manager != "store" || got[0].Source != "appx" || got[0].UpdateSupported {
		t.Fatalf("unexpected AppX package metadata: %#v", got[0])
	}
}

func TestFriendlyAppxNameCleansPackageIdentity(t *testing.T) {
	cases := map[string]string{
		"19568ShareX.ShareX":                "ShareX",
		"28017CharlesMilette.TranslucentTB": "Translucent TB",
		"38002AlexanderFrangos.TwinkleTray": "Twinkle Tray",
		"9662DuongDieuPhap.ImageGlass":      "Image Glass",
		"Microsoft.WindowsNotepad":          "Windows Notepad",
	}
	for input, want := range cases {
		if got := friendlyAppxName(input, ""); got != want {
			t.Fatalf("friendlyAppxName(%q) = %q, want %q", input, got, want)
		}
	}
	if got := friendlyAppxName("19568ShareX.ShareX", "ShareX"); got != "ShareX" {
		t.Fatalf("manifest display name should win, got %q", got)
	}
	if got := friendlyAppxName("19568ShareX.ShareX", "ms-resource:AppName"); got != "ShareX" {
		t.Fatalf("resource display name should fall back to package cleanup, got %q", got)
	}
}

func TestMergeStoreAppxPackagesAddsInventoryOnlyRows(t *testing.T) {
	managed := []Package{
		{ID: "9NBLGGH5R558", Name: "Microsoft To Do", Manager: "store", Source: "msstore", UpdateSupported: true},
	}
	appx := []Package{
		{ID: "Microsoft.Todos_2.130.0.0_x64__8wekyb3d8bbwe", Name: "Microsoft To Do", Manager: "store", Source: "appx"},
		{ID: "OpenAI.Codex_1.0.0.0_x64__abc123", Name: "OpenAI.Codex", Manager: "store", Source: "appx"},
	}
	got := mergeStoreAppxPackages(managed, appx)
	if len(got) != 2 {
		t.Fatalf("expected duplicate appx row to be skipped and Codex added, got %#v", got)
	}
	if got[1].Name != "OpenAI.Codex" || got[1].Source != "appx" {
		t.Fatalf("unexpected merged AppX row: %#v", got[1])
	}
}

func TestResolveStoreAppxPackagesMapsCodex(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, results, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		if query != "Codex" {
			t.Fatalf("unexpected query %q", query)
		}
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true, Command: "store search Codex"}
	})

	if !changed || calls != 1 || len(results) != 1 {
		t.Fatalf("expected one resolver search and cache change, calls=%d changed=%t results=%#v", calls, changed, results)
	}
	if got[0].ID != "OpenAI.Codex" || !got[0].UpdateSupported || got[0].ActionBackend != "store-cli-resolved" {
		t.Fatalf("expected resolved Store CLI target, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if !entry.Resolved || entry.StoreID != "OpenAI.Codex" || entry.AppXVersion != "1.0.0.0" {
		t.Fatalf("unexpected resolver cache entry: %#v", entry)
	}
}

func TestResolveStoreAppxPackagesKeepsMismatchInventoryOnly(t *testing.T) {
	state := defaultState()
	appx := []Package{{
		ID:              "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Notepad", ID: "Microsoft.WindowsNotepad", Manager: "store"}}, CommandResult{OK: true}
	})

	if !changed {
		t.Fatal("expected unresolved lookup to be cached")
	}
	if got[0].UpdateSupported || got[0].ActionBackend != "appx-inventory" {
		t.Fatalf("mismatch should stay inventory-only, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if entry.Resolved {
		t.Fatalf("mismatch should cache unresolved entry, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesUsesCacheWithoutSearch(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Codex",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, results, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return nil, CommandResult{}
	})

	if calls != 0 || changed || len(results) != 0 {
		t.Fatalf("cache hit should avoid search, calls=%d changed=%t results=%#v", calls, changed, results)
	}
	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("cache hit did not resolve package: %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesInvalidatesBadSearchBannerCache(t *testing.T) {
	state := defaultState()
	appxID := "Microsoft.ApplicationCompatibility_1.2511.9.0_x64__abc123"
	cacheKey := strings.ToLower(appxID)
	state.StoreResolveCache[cacheKey] = StoreResolveCacheEntry{
		AppXVersion: "1.2511.9.0",
		StoreID:     "Search Results for \"Application Compatibility Enhancements\"",
		StoreName:   "Application Compatibility Enhancements",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Application Compatibility Enhancements",
		Version:         "1.2511.9.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Application Compatibility Enhancements", ID: "Microsoft.ApplicationCompatibility", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale cache to be invalidated and searched, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "Microsoft.ApplicationCompatibility" || got[0].ID == "Search Results for \"Application Compatibility Enhancements\"" {
		t.Fatalf("bad cached banner target was not replaced: %#v", got[0])
	}
}

func TestScanSourceCountsSeparatesStoreApps(t *testing.T) {
	state := State{
		LastScanAt: "2026-06-14T12:00:00Z",
		RegistryApps: map[string]ScannedApp{
			"registry:app": {Source: "registry"},
		},
		WingetApps: map[string]ScannedApp{
			"winget:git.git":     {Source: "winget"},
			"store:9nblggh5r558": {Source: "store"},
			"store:legacy":       {Source: "msstore"},
		},
	}
	counts := scanSourceCounts(state.WingetApps)
	if counts["winget"] != 1 || counts["store"] != 2 {
		t.Fatalf("unexpected scan source counts: %#v", counts)
	}

	summary := inventoryScanSummary(state, counts)
	if summary.LastScanAt != state.LastScanAt || summary.TrackedCount != 4 || summary.RegistryCount != 1 || summary.WingetCount != 1 || summary.StoreCount != 2 {
		t.Fatalf("unexpected inventory scan summary: %#v", summary)
	}
}

func TestInventoryResponseFlattensInventoryJSON(t *testing.T) {
	response := InventoryResponse{
		Inventory: Inventory{
			PackageLookup: PackageLookup{
				Packages: []Package{{Name: "Git", ID: "Git.Git", Manager: managerWinget}},
				Managers: map[string]ManagerStatus{
					managerWinget: {Available: true},
				},
				CommandResults: map[string]CommandResult{
					"winget_list": {OK: true},
				},
			},
			Scan: InventoryScanSummary{TrackedCount: 1},
		},
		AsyncSnapshot: AsyncSnapshot{Loading: true},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"packages", "managers", "command_results", "scan", "loading"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing flattened inventory response key %q in %s", key, encoded)
		}
	}
	if _, ok := payload["Inventory"]; ok {
		t.Fatalf("embedded Inventory should not be encoded as a nested field: %s", encoded)
	}
	if _, ok := payload["PackageLookup"]; ok {
		t.Fatalf("embedded PackageLookup should not be encoded as a nested field: %s", encoded)
	}
	if _, ok := payload["AsyncSnapshot"]; ok {
		t.Fatalf("embedded AsyncSnapshot should not be encoded as a nested field: %s", encoded)
	}
}

func TestLogBufferAppendSinceAndRetention(t *testing.T) {
	buffer := newLogBuffer(3)
	first := buffer.Append("app", "one")
	second := buffer.Append("stdout", "two")
	third := buffer.Append("stderr", "three")
	fourth := buffer.Append("exit", "four")

	if first.ID != 1 || second.ID != 2 || third.ID != 3 || fourth.ID != 4 {
		t.Fatalf("unexpected log ids: %d %d %d %d", first.ID, second.ID, third.ID, fourth.ID)
	}
	if buffer.LatestID() != 4 {
		t.Fatalf("expected latest id 4, got %d", buffer.LatestID())
	}

	retained := buffer.Since(0)
	if len(retained) != 3 || retained[0].Message != "two" || retained[2].Message != "four" {
		t.Fatalf("unexpected retained entries: %#v", retained)
	}

	newer := buffer.Since(2)
	if len(newer) != 2 || newer[0].ID != 3 || newer[1].ID != 4 {
		t.Fatalf("unexpected since entries: %#v", newer)
	}
}

func TestAPILogsRequiresTokenAndReturnsEntries(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = newLogBuffer(10)
	defer func() { sessionLogs = oldLogs }()

	sessionLogs.Append("app", "hello")
	app := &App{token: "test-token"}

	badRequest := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	badResponse := httptest.NewRecorder()
	app.serveHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized log request, got %d", badResponse.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/logs?token=test-token", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}

	var decoded struct {
		Entries  []LogEntry `json:"entries"`
		LatestID int64      `json:"latest_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LatestID != 1 || len(decoded.Entries) != 1 || decoded.Entries[0].Message != "hello" {
		t.Fatalf("unexpected log response: %#v", decoded)
	}
}

func TestShutdownRouteStopsServer(t *testing.T) {
	app := &App{token: "test-token"}
	server := httptest.NewServer(http.HandlerFunc(app.serveHTTP))
	app.server = server.Config
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/shutdown?token=test-token", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected shutdown response ok, got %d", response.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		check, err := server.Client().Get(server.URL + "/?token=test-token")
		if err != nil {
			return
		}
		_ = check.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server still responded after shutdown")
}

func TestWingetNoApplicableUpgradeUsesFallbackDetection(t *testing.T) {
	english := CommandResult{Code: 1, Stdout: "No applicable upgrade found."}
	if !shouldForceInstallAfterWingetUpgrade(english) {
		t.Fatal("expected English no-applicable-upgrade output to trigger fallback")
	}

	german := CommandResult{Code: 1, Stdout: "Es wurde kein anwendbares Upgrade gefunden."}
	if !shouldForceInstallAfterWingetUpgrade(german) {
		t.Fatal("expected German no-applicable-upgrade output to trigger fallback")
	}

	success := CommandResult{OK: true, Stdout: "No applicable upgrade found."}
	if shouldForceInstallAfterWingetUpgrade(success) {
		t.Fatal("successful winget command should not trigger fallback")
	}
}

func TestMergeCommandResultsKeepsPrimaryFailureContext(t *testing.T) {
	primary := CommandResult{Code: 1, Command: "winget upgrade", Stdout: "No applicable upgrade found.", Stderr: "primary stderr"}
	fallback := CommandResult{OK: true, Code: 0, Command: "winget install --force", Stdout: "Successfully installed", Stderr: ""}

	merged := mergeCommandResults(primary, fallback, "fallback")

	if !merged.OK || merged.Code != 0 {
		t.Fatalf("expected fallback success to win, got %#v", merged)
	}
	if !strings.Contains(merged.Command, "winget upgrade") || !strings.Contains(merged.Command, "winget install --force") {
		t.Fatalf("merged command did not include both commands: %q", merged.Command)
	}
	if !strings.Contains(merged.Stdout, "No applicable upgrade found.") || !strings.Contains(merged.Stdout, "Successfully installed") {
		t.Fatalf("merged stdout lost context: %q", merged.Stdout)
	}
	if !strings.Contains(merged.Stderr, "primary stderr") {
		t.Fatalf("merged stderr lost primary context: %q", merged.Stderr)
	}
}

func TestAPIRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		wantResult bool
		wantText   string
	}{
		{"update", "/api/update?token=test-token", "manager=invalid&package_id=Git.Git", true, managerValidationMessage},
		{"install", "/api/install?token=test-token", "manager=invalid&package_id=Git.Git", true, managerValidationMessage},
		{"manager install", "/api/managers/install?token=test-token", "manager=invalid", true, managerValidationMessage},
		{"update all", "/api/update-all?token=test-token", "package_key=not-a-valid-key", false, "package key must be manager:id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{token: "test-token"}
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			app.serveHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected bad request, got %d: %s", response.Code, response.Body.String())
			}

			var decoded struct {
				Result         *CommandResult `json:"result"`
				Results        []UpdateResult `json:"results"`
				RefreshStarted bool           `json:"refresh_started"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.RefreshStarted {
				t.Fatal("invalid request should not start an inventory refresh")
			}
			if tc.wantResult {
				if decoded.Result == nil || decoded.Result.Code != 2 || !strings.Contains(decoded.Result.Stderr, tc.wantText) {
					t.Fatalf("unexpected validation result: %#v", decoded.Result)
				}
				return
			}
			if len(decoded.Results) != 1 || decoded.Results[0].Result.Code != 2 || !strings.Contains(decoded.Results[0].Result.Stderr, tc.wantText) {
				t.Fatalf("unexpected update-all validation result: %#v", decoded.Results)
			}
		})
	}
}

func TestRenderedHTMLContainsAsyncUpdateHooks(t *testing.T) {
	var output bytes.Buffer
	data := PageData{
		Token: "test-token",
		Theme: "dark",
	}
	if err := pageTemplate.Execute(&output, data); err != nil {
		t.Fatal(err)
	}
	rendered := output.String()
	for _, expected := range []string{
		`id="update-progress"`,
		`class="update-all-form"`,
		`id="search-form"`,
		`action="/api/install"`,
		`action="/api/managers/install"`,
		`runUpdateRequest("/api/update"`,
		`runUpdateRequest("/api/update-all"`,
		`installFromForm`,
		`installManagerFromForm`,
		`refreshPackagesAfterUpdate`,
		`id="session-log-panel"`,
		`id="clear-log-view"`,
		`id="log-autoscroll"`,
		`api("/api/logs"`,
		`id="updates-body"`,
		`id="installed-search"`,
		`id="installed-page-status"`,
		`packageMatchesInstalledSearch`,
		`managersRendered`,
		`renderUpdatesTable`,
		`renderInstalledTable`,
		`installedAction`,
		`managerAvailabilityText`,
		`managerDisplayDetails`,
		`manager.inventory_available`,
		`pkg.action_backend`,
		`Inventory only`,
		`Store apps detected via`,
		`store-cli-resolved`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered page did not contain %q", expected)
		}
	}
	for _, unexpected := range []string{
		`Inventory: `,
		`Actions: `,
		`Available Usage: store`,
		`Usage: store <command>`,
		`? "Current" : "-"`,
		`action="/install"`,
		`action="/manager/install"`,
		`action="/update"`,
		`action="/update-all"`,
		`{{if .CommandResult}}`,
		`{{if .ActionResults}}`,
		`{{if .Scan}}`,
	} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("rendered page should not contain %q", unexpected)
		}
	}
}

func TestIntegrationInventoryAndScan(t *testing.T) {
	if os.Getenv("UPDATER_INTEGRATION") != "1" {
		t.Skip("set UPDATER_INTEGRATION=1 to run real winget/choco integration test")
	}
	inventory := getInventory()
	if !inventory.Managers["winget"].Available {
		t.Fatalf("winget unavailable: %#v", inventory.Managers["winget"])
	}
	if !inventory.Managers["choco"].Available {
		t.Fatalf("choco unavailable: %#v", inventory.Managers["choco"])
	}
	var wingetCount, chocoCount, updateCount int
	for _, pkg := range inventory.Packages {
		switch pkg.Manager {
		case "winget":
			wingetCount++
			if isTruncatedID(pkg.ID) {
				t.Fatalf("inventory contained truncated winget id: %#v", pkg)
			}
		case "choco":
			chocoCount++
		}
		if pkg.UpdateAvailable {
			updateCount++
		}
	}
	if wingetCount == 0 || chocoCount == 0 {
		t.Fatalf("expected both managers to list packages, winget=%d choco=%d", wingetCount, chocoCount)
	}
	if updateCount == 0 {
		t.Fatalf("expected at least one available update in this environment")
	}
	scan := scanInstalledApplications()
	if len(scan.Errors) > 0 {
		t.Fatalf("scan errors: %#v", scan.Errors)
	}
	if scan.SourceCounts["registry"] == 0 || scan.SourceCounts["winget"] == 0 {
		t.Fatalf("expected registry and winget scan counts, got %#v", scan.SourceCounts)
	}
}
