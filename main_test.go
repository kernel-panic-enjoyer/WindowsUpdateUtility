package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestArgValueParsesEqualsAndSeparatedForms(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"updater", "--token=abc", "--port", "4299"}
	if got, ok := argValue("--token"); !ok || got != "abc" {
		t.Fatalf("unexpected token arg: %q %t", got, ok)
	}
	if got, ok := argValue("--port"); !ok || got != "4299" {
		t.Fatalf("unexpected port arg: %q %t", got, ok)
	}
	if got, ok := argValue("--missing"); ok || got != "" {
		t.Fatalf("unexpected missing arg: %q %t", got, ok)
	}
}

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
GitHub CLI                   GitHub.cli                         2.74.0    Moniker: gh    winget
`
	got := parseWingetTable(output)
	if len(got) != 3 {
		t.Fatalf("expected 3 packages, got %d: %#v", len(got), got)
	}
	if !isTruncatedID(got[0].ID) {
		t.Fatalf("expected truncated id: %#v", got[0])
	}
	if got[1].Source != "winget" || got[1].Match != "Tag: zed" || got[1].AvailableVersion != "" {
		t.Fatalf("expected resilient source parsing, got %#v", got[1])
	}
	if got[2].Match != "Moniker: gh" || got[2].AvailableVersion != "" {
		t.Fatalf("expected winget moniker to be parsed as match, got %#v", got[2])
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

func TestSearchQueryVariantsNormalizePunctuation(t *testing.T) {
	cases := map[string][]string{
		"github-cli": {"github-cli", "github cli"},
		"GitHub.cli": {"GitHub.cli", "GitHub cli"},
		"gh":         {"gh"},
	}
	for query, want := range cases {
		got := searchQueryVariants(query)
		if len(got) != len(want) {
			t.Fatalf("searchQueryVariants(%q) = %#v, want %#v", query, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("searchQueryVariants(%q) = %#v, want %#v", query, got, want)
			}
		}
	}
}

func TestSortSearchPackagesPrioritizesExactIdentityBeforePrefixAndMoniker(t *testing.T) {
	packages := []Package{
		{Name: "Ghidra", ID: "NationalSecurityAgency.Ghidra", Manager: managerWinget},
		{Name: "Ghostscript", ID: "ArtifexSoftware.GhostScript", Manager: managerWinget},
		{Name: "ghx", ID: "ghx", Manager: managerWinget},
		{Name: "GitHub CLI", ID: "GitHub.cli", Match: "Moniker: gh", Manager: managerWinget},
		{Name: "gh", ID: "gh", Manager: managerChoco},
	}

	sortSearchPackages("gh", packages)

	if packages[0].ID != "gh" {
		t.Fatalf("expected exact package id before alias/prefix matches, got %#v", packages)
	}
	if packages[1].Name != "GitHub CLI" || packages[1].Match != "Moniker: gh" {
		t.Fatalf("expected exact moniker match before prefix matches, got %#v", packages)
	}
	if packages[2].ID != "ghx" {
		t.Fatalf("expected ghx prefix match after exact identity and exact moniker, got %#v", packages)
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

func TestWingetTruncatedIDRecognizesUnicodeEllipsis(t *testing.T) {
	cases := []struct {
		name      string
		truncated string
	}{
		{"unicode", "DragonframeLicenseMa\u2026"},
		{"mojibake", "DragonframeLicenseMa\u00e2\u20ac\u00a6"},
		{"ascii", "DragonframeLicenseMa..."},
	}
	for _, tc := range cases {
		if !isTruncatedID(tc.truncated) {
			t.Fatalf("expected %s ellipsis ID to be treated as truncated", tc.name)
		}
		if !wingetIDMatches("DragonframeLicenseManager.Full.ID", tc.truncated) {
			t.Fatalf("expected %s ellipsis ID to match full exported ID", tc.name)
		}
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

func TestLoadStateMigratesStoreAppsOutOfWingetBucket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {},
  "registry_apps": {},
  "winget_apps": {
    "winget:git.git": {"key":"winget:git.git","name":"Git","source":"winget","manager":"winget"},
    "store:openai.codex": {"key":"store:openai.codex","name":"Codex","source":"store","manager":"store"}
  },
  "store_resolve_cache": {},
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	if _, ok := state.WingetApps["store:openai.codex"]; ok {
		t.Fatalf("store app was not migrated out of winget bucket: %#v", state.WingetApps)
	}
	if state.StoreApps["store:openai.codex"].Name != "Codex" {
		t.Fatalf("store app missing after migration: %#v", state.StoreApps)
	}
}

func TestLoadStateNormalizesVersionedStoreAppKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {},
  "registry_apps": {},
  "winget_apps": {},
  "store_apps": {
    "store:openai.codex_1.0.0.0_x64__abc123": {
      "key": "store:openai.codex_1.0.0.0_x64__abc123",
      "name": "Codex",
      "source": "store",
      "manager": "store",
      "package_id": "OpenAI.Codex_1.0.0.0_x64__abc123",
      "first_seen": "2026-06-14T12:00:00Z"
    }
  },
  "store_resolve_cache": {},
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	if _, ok := state.StoreApps["store:openai.codex_1.0.0.0_x64__abc123"]; ok {
		t.Fatalf("versioned store key was not normalized: %#v", state.StoreApps)
	}
	app := state.StoreApps["store:openai.codex"]
	if app.PackageID != "OpenAI.Codex" || app.FirstSeen != "2026-06-14T12:00:00Z" {
		t.Fatalf("unexpected normalized store app: %#v", app)
	}
}

func TestLoadStateNormalizesStoreAutoUpdateKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {
    "store:OpenAI.Codex_1.0.0.0_x64__abc123": true,
    "winget:Git.Git": true
  },
  "registry_apps": {},
  "winget_apps": {},
  "store_apps": {},
  "store_resolve_cache": {},
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	if !state.AutoUpdatePackages["store:OpenAI.Codex"] || !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("auto-update keys were not normalized correctly: %#v", state.AutoUpdatePackages)
	}
	if state.AutoUpdatePackages["store:OpenAI.Codex_1.0.0.0_x64__abc123"] {
		t.Fatalf("versioned Store auto-update key should not remain: %#v", state.AutoUpdatePackages)
	}
}

func TestRunAutoUpdateSkipsWhenNoPackagesOptedIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	state := defaultState()
	state.AutoUpdateGlobal = true
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}

	results := runAutoUpdate()
	if len(results) != 0 {
		t.Fatalf("expected no auto-update results without opted-in packages, got %#v", results)
	}

	updated := loadState()
	if updated.LastAutoUpdateAt == "" {
		t.Fatal("expected skipped auto-update to record a run timestamp")
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

func TestDetectManagersReturnsCachedCopy(t *testing.T) {
	invalidateManagerDetectionCache()
	t.Cleanup(invalidateManagerDetectionCache)
	managerDetectionCache.mu.Lock()
	managerDetectionCache.cached = map[string]ManagerStatus{
		managerStore: {Available: true, Version: "cached"},
	}
	managerDetectionCache.fetchedAt = time.Now()
	managerDetectionCache.inFlight = nil
	managerDetectionCache.mu.Unlock()

	got := detectManagers()
	got[managerStore] = ManagerStatus{Available: false}
	again := detectManagers()
	if !again[managerStore].Available || again[managerStore].Version != "cached" {
		t.Fatalf("cached manager status should be copied defensively, got %#v", again)
	}
}

func TestInvalidateManagerDetectionCache(t *testing.T) {
	managerDetectionCache.mu.Lock()
	managerDetectionCache.cached = map[string]ManagerStatus{managerStore: {Available: true}}
	managerDetectionCache.fetchedAt = time.Now()
	managerDetectionCache.mu.Unlock()

	invalidateManagerDetectionCache()

	managerDetectionCache.mu.Lock()
	defer managerDetectionCache.mu.Unlock()
	if managerDetectionCache.cached != nil || !managerDetectionCache.fetchedAt.IsZero() {
		t.Fatalf("manager detection cache was not cleared: %#v at %s", managerDetectionCache.cached, managerDetectionCache.fetchedAt)
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

func TestPackageManagerMutationCommandDetection(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"store", "search", "Codex"}, false},
		{[]string{"store", "install", "OpenAI.Codex"}, true},
		{[]string{"cmd.exe", "/d", "/c", "store", "updates"}, true},
		{[]string{"winget", "list"}, false},
		{[]string{"winget", "upgrade", "--all"}, true},
		{[]string{"cmd.exe", "/d", "/c", "winget", "search", "git"}, false},
		{[]string{"choco", "outdated"}, false},
		{[]string{"choco", "upgrade", "all"}, true},
	}
	for _, tc := range cases {
		if got := isPackageManagerMutationCommand(tc.args); got != tc.want {
			t.Fatalf("isPackageManagerMutationCommand(%#v) = %t, want %t", tc.args, got, tc.want)
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
	if err := validateManagerAndID("store", "bad%query"); err == nil {
		t.Fatal("store queries with cmd expansion characters should be rejected")
	}
}

func TestPackageAutoUpdateEnabledUsesEquivalentStoreKey(t *testing.T) {
	state := State{
		AutoUpdatePackages: map[string]bool{
			"store:OpenAI.Codex_1.0.0.0_x64__abc123": true,
		},
	}
	pkg := Package{
		Key:     "store:OpenAI.Codex",
		Manager: "store",
		ID:      "OpenAI.Codex",
	}
	if !packageAutoUpdateEnabled(state, pkg) {
		t.Fatalf("expected equivalent Store auto-update key to be honored")
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

	storeUpdate := strings.Join(managerCommand(managerStore, "update", "Codex", "--apply", "true"), " ")
	for _, expected := range []string{"store", "update Codex", "--apply true"} {
		if !strings.Contains(storeUpdate, expected) {
			t.Fatalf("store update command missing %q: %s", expected, storeUpdate)
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

func TestParseStoreSearchBoxTable(t *testing.T) {
	output := `
Searching for "codex"…

── Search Results for "codex" ──────────────────────────────────────────────────

┌────────────────┬────────────────┬────────────────┬───────────────┬───────────┐
│ Name           │ Product ID     │ Publisher      │ Categories    │ Price     │
├────────────────┼────────────────┼────────────────┼───────────────┼───────────┤
│ Codex          │ 9PLM9XGG6VKS   │ OpenAI         │ Entwicklungst │ Kostenlos │
│                │                │                │ ools          │           │
│ Codex (Beta)   │ 9N8CJ4W95TBZ   │ OpenAI         │ Entwicklungst │ Kostenlos │
└────────────────┴────────────────┴────────────────┴───────────────┴───────────┘
`
	got := parseStoreSearch(output)
	if len(got) != 2 {
		t.Fatalf("expected two parsed Store rows, got %#v", got)
	}
	if got[0].Name != "Codex" || got[0].ID != "9PLM9XGG6VKS" || got[0].ActionBackend != backendStoreCLI {
		t.Fatalf("unexpected first Store row: %#v", got[0])
	}
	if got[1].Name != "Codex (Beta)" || got[1].ID != "9N8CJ4W95TBZ" {
		t.Fatalf("unexpected second Store row: %#v", got[1])
	}
}

func TestParseStoreUpdatesBoxTable(t *testing.T) {
	output := `
Checking for updates…

── Updates available (1 found) ─────────────────────────────────────────────────

Store-managed update available
This Store app update can be installed immediately.
┌───────┬───────────┬───────────────┬────────────┐
│ Name  │ Publisher │ Version       │ Date       │
├───────┼───────────┼───────────────┼────────────┤
│ Codex │ OpenAI    │ 26.609.4994.0 │ 2026-06-13 │
└───────┴───────────┴───────────────┴────────────┘
`
	got := parseStoreUpdates(output)
	if got["store:codex"] != "26.609.4994.0" {
		t.Fatalf("expected Codex Store update from box table, got %#v", got)
	}
}

func TestMergeWingetUpdateOutputForcesMSStoreSource(t *testing.T) {
	output := `
Name                 ID                                    Version          Available
-----------------------------------------------------------------------------------
Windows App Runtime  Microsoft.WindowsAppRuntime.Singleton  8000.318.101.0  8000.328.111.0
Codex                OpenAI.Codex                          0.1.0            0.2.0
`
	updates := map[string]string{}
	mergeWingetUpdateOutput(updates, output, managerStore)

	if updates["store:microsoft.windowsappruntime.singleton"] != "8000.328.111.0" {
		t.Fatalf("missing Windows App Runtime Store update: %#v", updates)
	}
	if updates["store:openai.codex"] != "0.2.0" {
		t.Fatalf("missing Codex Store update: %#v", updates)
	}
	if _, ok := updates["winget:openai.codex"]; ok {
		t.Fatalf("msstore-specific update should not be keyed as winget: %#v", updates)
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
{"Name":"Microsoft.Todos","StartName":"Microsoft To Do","DisplayName":"ms-resource:AppName","PackageFullName":"Microsoft.Todos_2.130.0.0_x64__8wekyb3d8bbwe","PackageFamilyName":"Microsoft.Todos_8wekyb3d8bbwe","Version":"2.130.0.0","Publisher":"CN=Microsoft","InstallLocation":"C:\\Program Files\\WindowsApps\\Microsoft.Todos"},
{"Name":"Microsoft.WindowsAppRuntime.Singleton","DisplayName":"","PackageFullName":"Microsoft.WindowsAppRuntime.Singleton_8000.318.101.0_x64__8wekyb3d8bbwe","PackageFamilyName":"Microsoft.WindowsAppRuntime.Singleton_8wekyb3d8bbwe","Version":"8000.318.101.0","Publisher":"CN=Microsoft","InstallLocation":"C:\\Program Files\\WindowsApps\\Microsoft.WindowsAppRuntime.Singleton"}
]`
	got := parseAppxPackageJSON(output)
	if len(got) != 3 {
		t.Fatalf("expected three AppX packages, got %#v", got)
	}
	if got[0].Name != "Codex" || got[1].Name != "Microsoft To Do" {
		t.Fatalf("expected friendly AppX display names, got %#v", got)
	}
	if got[2].Name != "Windows App Runtime Singleton" {
		t.Fatalf("expected friendly Windows App Runtime name, got %#v", got[2])
	}
	if got[0].Manager != "store" || got[0].Source != "appx" || got[0].UpdateSupported {
		t.Fatalf("unexpected AppX package metadata: %#v", got[0])
	}
}

func TestFriendlyAppxNameCleansPackageIdentity(t *testing.T) {
	cases := map[string]string{
		"19568ShareX.ShareX":                             "ShareX",
		"28017CharlesMilette.TranslucentTB":              "Translucent TB",
		"38002AlexanderFrangos.TwinkleTray":              "Twinkle Tray",
		"9662DuongDieuPhap.ImageGlass":                   "Image Glass",
		"Microsoft.WindowsNotepad":                       "Windows Notepad",
		"Microsoft.WindowsAppRuntime.Singleton":          "Windows App Runtime Singleton",
		"Microsoft.WindowsAppRuntime.CBS.1.8":            "Windows App Runtime CBS 1.8",
		"MicrosoftCorporationII.WinAppRuntime.Singleton": "Win App Runtime Singleton",
		"Contoso.FooBar.Baz2":                            "Foo Bar Baz2",
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
	if got := friendlyAppxName("Microsoft.Todos", "ms-resource:AppName", "Microsoft To Do"); got != "Microsoft To Do" {
		t.Fatalf("start menu display name should win over resource fallback, got %q", got)
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

func TestMergeStoreAppxPackagesPrefersResolvedStoreRow(t *testing.T) {
	managed := []Package{
		{
			Key:             "store:openai.codex",
			ID:              "OpenAI.Codex",
			Name:            "OpenAI Codex",
			Version:         "1.0.0.0",
			Manager:         "store",
			Source:          "msstore",
			ActionBackend:   "winget-msstore-fallback",
			UpdateSupported: true,
		},
	}
	appx := []Package{
		{
			Key:              "store:OpenAI.Codex",
			ID:               "OpenAI.Codex",
			Name:             "Codex",
			Version:          "1.0.0.0",
			AvailableVersion: "1.1.0",
			Manager:          "store",
			Source:           "appx",
			Match:            "Codex",
			ActionBackend:    "store-cli-resolved",
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
		},
	}

	got := mergeStoreAppxPackages(managed, appx)
	if len(got) != 1 {
		t.Fatalf("expected duplicate store rows to merge, got %#v", got)
	}
	if got[0].Name != "Codex" || got[0].ActionBackend != "store-cli-resolved" || got[0].AvailableVersion != "1.1.0" || !got[0].UpdateAvailable {
		t.Fatalf("resolved AppX row details were not preserved: %#v", got[0])
	}
}

func TestApplyStoreUpdateVersionMatchesAppxFullName(t *testing.T) {
	pkg := Package{
		ID:            "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:          "Codex",
		Version:       "1.0.0.0",
		Manager:       managerStore,
		Source:        sourceAppX,
		Match:         "OpenAI.Codex_abc123",
		ActionBackend: backendAppXInventory,
	}
	updates := map[string]string{
		packageKey(managerStore, strings.ToLower("OpenAI.Codex")): "1.1.0.0",
	}

	got := applyStoreUpdateVersion(pkg, updates, false)

	if got.ID != "OpenAI.Codex" || got.Key != "store:OpenAI.Codex" {
		t.Fatalf("expected AppX full name to collapse to Store action ID, got %#v", got)
	}
	if !got.UpdateAvailable || got.AvailableVersion != "1.1.0.0" || !got.UpdateSupported {
		t.Fatalf("expected winget msstore update to mark Store package updateable, got %#v", got)
	}
	if got.ActionBackend != backendWingetMSStoreFallback {
		t.Fatalf("expected winget msstore fallback backend, got %#v", got)
	}
}

func TestApplyStoreUpdateVersionUsesStoreUpdateNameTarget(t *testing.T) {
	pkg := Package{
		ID:            "OpenAI.Codex_1.0.0.0_x64__abc123",
		Name:          "Codex",
		Version:       "1.0.0.0",
		Manager:       managerStore,
		Source:        sourceAppX,
		Match:         "OpenAI.Codex_abc123",
		ActionBackend: backendStoreCLIResolved,
	}
	updates := map[string]string{
		packageKey(managerStore, "codex"): "26.609.4994.0",
	}

	got := applyStoreUpdateVersion(pkg, updates, true)

	if got.ID != "Codex" || got.Key != "store:Codex" {
		t.Fatalf("expected Store CLI update target to use app name, got %#v", got)
	}
	if !got.UpdateAvailable || !got.UpdateSupported || got.ActionBackend != backendStoreCLIResolved {
		t.Fatalf("expected Store update to mark row updateable, got %#v", got)
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

func TestResolveStoreAppxPackagesMarksUpdateFromStoreVersion(t *testing.T) {
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
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if !changed {
		t.Fatal("expected resolver cache change")
	}
	if !got[0].UpdateAvailable || got[0].AvailableVersion != "1.1.0" {
		t.Fatalf("expected Store search version to mark update available, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower("OpenAI.Codex_1.0.0.0_x64__abc123")]
	if entry.StoreVersion != "1.1.0" {
		t.Fatalf("expected Store version in cache, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesKeepsCurrentWhenStoreVersionMatches(t *testing.T) {
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

	got, _, _ := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.0.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if got[0].UpdateAvailable || got[0].AvailableVersion != "" {
		t.Fatalf("matching Store version should stay current, got %#v", got[0])
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

func TestResolveStoreAppxPackagesRejectsGenericContainedStoreResult(t *testing.T) {
	appx := Package{
		ID:              "Microsoft.Example.WindowsAppHelper_1.0.0.0_x64__abc123",
		Name:            "Windows App Runtime Singleton",
		Version:         "1.0.0.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}
	if match, ok := chooseStoreResolution(appx, []Package{{Name: "Windows App", ID: "9MVJJ5Q28CJ2", Manager: "store"}}); ok {
		t.Fatalf("generic contained Store result should not resolve, got %#v", match)
	}
}

func TestResolveStoreAppxPackagesRejectsGenericCachedContainedStoreResult(t *testing.T) {
	state := defaultState()
	appxID := "MicrosoftCorporationII.WinAppRuntime.Singleton_8002.1.3.0_x64__8wekyb3d8bbwe"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "8002.1.3.0",
		StoreID:     "9MVJJ5Q28CJ2",
		StoreName:   "Windows App",
		Resolved:    true,
		ResolvedAt:  utcNow(),
	}
	appx := []Package{{
		ID:              appxID,
		Name:            "Windows App Runtime Singleton",
		Version:         "8002.1.3.0",
		Manager:         "store",
		Source:          "appx",
		UpdateSupported: false,
		ActionBackend:   "appx-inventory",
	}}

	calls := 0
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Windows App", ID: "9MVJJ5Q28CJ2", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale generic cache to be discarded and refreshed, calls=%d changed=%t", calls, changed)
	}
	if got[0].UpdateSupported || got[0].ActionBackend != backendAppXInventory {
		t.Fatalf("generic cached Store result should not resolve, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower(appxID)]
	if entry.Resolved {
		t.Fatalf("generic cached Store result should be replaced with unresolved cache, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesRetriesStaleUnresolvedCache(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		Resolved:    false,
		ResolvedAt:  time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
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
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected stale unresolved cache to be retried, calls=%d changed=%t", calls, changed)
	}
	if !got[0].UpdateAvailable || got[0].ID != "OpenAI.Codex" {
		t.Fatalf("expected retry to resolve Codex update, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesRetriesFreshUnresolvedCache(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		Resolved:    false,
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
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "9PLM9XGG6VKS", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected fresh unresolved cache to be retried, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "9PLM9XGG6VKS" || got[0].ActionBackend != backendStoreCLIResolved || !got[0].UpdateSupported {
		t.Fatalf("expected retry to resolve Codex Store product ID, got %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesRefreshesResolvedCache(t *testing.T) {
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
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed || len(results) != 1 {
		t.Fatalf("resolved cache should refresh version data, calls=%d changed=%t results=%#v", calls, changed, results)
	}
	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("cache hit did not resolve package: %#v", got[0])
	}
}

func TestResolveStoreAppxPackagesRefreshesFreshCacheVersion(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion:  "1.0.0.0",
		StoreID:      "OpenAI.Codex",
		StoreName:    "Codex",
		StoreVersion: "1.0.0.0",
		Resolved:     true,
		ResolvedAt:   utcNow(),
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
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Version: "1.1.0.0", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected fresh cache to refresh Store version, calls=%d changed=%t", calls, changed)
	}
	if !got[0].UpdateAvailable || got[0].AvailableVersion != "1.1.0.0" {
		t.Fatalf("expected refreshed Store version to mark Codex update, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower(appxID)]
	if entry.StoreVersion != "1.1.0.0" {
		t.Fatalf("expected refreshed Store version in cache, got %#v", entry)
	}
}

func TestResolveStoreAppxPackagesKeepsCachedMappingOnBadRefreshMatch(t *testing.T) {
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

	got, _, _ := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		return []Package{{Name: "Notepad", ID: "Microsoft.WindowsNotepad", Manager: "store"}}, CommandResult{OK: true}
	})

	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("bad refresh match should keep cached Store mapping, got %#v", got[0])
	}
	entry := state.StoreResolveCache[strings.ToLower(appxID)]
	if !entry.Resolved || entry.StoreID != "OpenAI.Codex" {
		t.Fatalf("bad refresh match should not overwrite safe cache entry: %#v", entry)
	}
}

func TestResolveStoreAppxPackagesRefreshesStaleCacheWithoutDroppingMapping(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	state.StoreResolveCache[strings.ToLower(appxID)] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.Codex",
		StoreName:   "Codex",
		Resolved:    true,
		ResolvedAt:  time.Now().UTC().Add(-storeResolveCacheTTL * 2).Format(time.RFC3339),
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
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return nil, CommandResult{Code: 1}
	})

	if calls != 1 || changed {
		t.Fatalf("expected stale cache refresh attempt without cache mutation, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "OpenAI.Codex" || got[0].ActionBackend != "store-cli-resolved" || !got[0].UpdateSupported {
		t.Fatalf("stale cache refresh failure should keep safe mapping, got %#v", got[0])
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

func TestResolveStoreAppxPackagesRejectsUnsafeCachedTarget(t *testing.T) {
	state := defaultState()
	appxID := "OpenAI.Codex_1.0.0.0_x64__abc123"
	cacheKey := strings.ToLower(appxID)
	state.StoreResolveCache[cacheKey] = StoreResolveCacheEntry{
		AppXVersion: "1.0.0.0",
		StoreID:     "OpenAI.%USERNAME%.Codex",
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
	got, _, changed := resolveStoreAppxPackages(&state, appx, true, func(query string) ([]Package, CommandResult) {
		calls++
		return []Package{{Name: "Codex", ID: "OpenAI.Codex", Manager: "store"}}, CommandResult{OK: true}
	})

	if calls != 1 || !changed {
		t.Fatalf("expected unsafe cache target to be invalidated and searched, calls=%d changed=%t", calls, changed)
	}
	if got[0].ID != "OpenAI.Codex" || !got[0].UpdateSupported {
		t.Fatalf("expected safe target replacement, got %#v", got[0])
	}
}

func TestVersionGreater(t *testing.T) {
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{"1.1.0", "1.0.9", true},
		{"2026.11050.1001.0", "2026.11050.1001.0", false},
		{"1.0", "1.0.1", false},
		{"v2.0.0", "1.9.9", true},
		{"latest", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := versionGreater(tc.candidate, tc.current); got != tc.want {
			t.Fatalf("versionGreater(%q, %q) = %t, want %t", tc.candidate, tc.current, got, tc.want)
		}
	}
}

func TestScanSourceCountsSeparatesStoreApps(t *testing.T) {
	state := State{
		LastScanAt: "2026-06-14T12:00:00Z",
		RegistryApps: map[string]ScannedApp{
			"registry:app": {Source: "registry"},
		},
		WingetApps: map[string]ScannedApp{
			"winget:git.git": {Source: "winget"},
		},
		StoreApps: map[string]ScannedApp{
			"store:9nblggh5r558": {Source: "store"},
			"store:legacy":       {Source: "msstore"},
		},
	}
	counts := managedScanSourceCounts(state)
	if counts["winget"] != 1 || counts["store"] != 2 {
		t.Fatalf("unexpected scan source counts: %#v", counts)
	}

	summary := inventoryScanSummary(state, counts)
	if summary.LastScanAt != state.LastScanAt || summary.TrackedCount != 4 || summary.RegistryCount != 1 || summary.WingetCount != 1 || summary.StoreCount != 2 {
		t.Fatalf("unexpected inventory scan summary: %#v", summary)
	}
}

func TestSplitScannedManagedAppsSeparatesStoreRows(t *testing.T) {
	winget, store := splitScannedManagedApps([]ScannedApp{
		{Key: "winget:git.git", Name: "Git", Source: "winget", Manager: "winget"},
		{Key: "store:codex", Name: "Codex", Source: "msstore", Manager: "store"},
		{Key: "store:paint", Name: "Paint", Source: "appx"},
	})
	if len(winget) != 1 || winget[0].Name != "Git" {
		t.Fatalf("unexpected winget split: %#v", winget)
	}
	if len(store) != 2 || store[0].Source != "store" || store[1].Manager != "store" {
		t.Fatalf("unexpected store split: %#v", store)
	}
}

func TestMergeScannedManagedAppsDedupesStoreAppxAgainstWingetStoreID(t *testing.T) {
	wingetStoreApps := []ScannedApp{
		{Key: "store:openai.codex", Name: "OpenAI Codex", Source: "store", Manager: "store", PackageID: "OpenAI.Codex"},
	}
	appxApps := []ScannedApp{
		{Key: "store:openai.codex", Name: "Codex", Source: "store", Manager: "store", PackageID: "OpenAI.Codex"},
	}

	got := mergeScannedManagedApps(wingetStoreApps, appxApps)
	if len(got) != 1 {
		t.Fatalf("expected Store AppX and winget Store scan rows to dedupe, got %#v", got)
	}
}

func TestStableAppxScanIDIgnoresPackageVersion(t *testing.T) {
	first := Package{
		ID:    "OpenAI.Codex_1.0.0.0_x64__abc123",
		Match: "OpenAI.Codex_abc123",
	}
	updated := Package{
		ID:    "OpenAI.Codex_1.1.0.0_x64__abc123",
		Match: "OpenAI.Codex_abc123",
	}
	if stableAppxScanID(first) != stableAppxScanID(updated) {
		t.Fatalf("AppX scan ID should be version-stable, got %q and %q", stableAppxScanID(first), stableAppxScanID(updated))
	}
	if got := stableAppxScanID(Package{ID: "OpenAI.Codex_1.1.0.0_x64__abc123"}); got != "OpenAI.Codex" {
		t.Fatalf("expected package-name fallback without version, got %q", got)
	}
	if got := stableAppxScanID(Package{ID: "Microsoft.Example_2.0.0.0_neutral_~_8wekyb3d8bbwe"}); got != "Microsoft.Example" {
		t.Fatalf("expected neutral AppX full name to normalize to package family, got %q", got)
	}
	if got := stableAppxScanID(Package{ID: "OpenAI.Codex_abc123"}); got != "OpenAI.Codex" {
		t.Fatalf("package family name should remain stable, got %q", got)
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

func TestStatusSnapshotPreservesStoreInventoryManagerDetails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	app := &App{
		status: StatusResponse{
			Managers: map[string]ManagerStatus{
				managerStore: {Available: true, ActionBackend: backendStoreCLI},
			},
		},
		statusFetchedAt: time.Now(),
		inventory: Inventory{
			PackageLookup: PackageLookup{
				Managers: map[string]ManagerStatus{
					managerStore: {
						Available:          true,
						ActionBackend:      backendStoreCLI,
						InventoryAvailable: true,
						InventoryBackend:   inventoryBackendAppX,
					},
				},
			},
		},
	}

	snapshot := app.statusSnapshot()
	store := snapshot.Managers[managerStore]
	if !store.InventoryAvailable || store.InventoryBackend != inventoryBackendAppX {
		t.Fatalf("expected status snapshot to keep Store inventory details, got %#v", store)
	}
	if app.status.Managers[managerStore].InventoryAvailable {
		t.Fatal("status snapshot should not mutate cached status managers in place")
	}
}

func TestRefreshStatusQueuesForcedRefreshWhileLoading(t *testing.T) {
	app := &App{statusLoading: true}

	app.refreshStatus(false)
	if app.statusQueued {
		t.Fatal("non-forced status refresh should not queue while loading")
	}

	app.refreshStatus(true)
	if !app.statusQueued {
		t.Fatal("forced status refresh should queue while loading")
	}
	if !app.statusLoading {
		t.Fatal("status should remain loading after queueing forced refresh")
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

func TestAppendLogChunkDropsCarriageReturnSpinnerFrames(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = newLogBuffer(10)
	defer func() { sessionLogs = oldLogs }()

	pending := appendLogChunk("stdout", "", "Downloading\r|\r/\r-\r")
	pending = appendLogChunk("stdout", pending, `\`+"\rDone\n")
	if pending != "" {
		t.Fatalf("expected no pending log text, got %q", pending)
	}

	entries := sessionLogs.Since(0)
	if len(entries) != 1 || entries[0].Message != "Done" {
		t.Fatalf("expected only final line, got %#v", entries)
	}
}

func TestStreamCommandOutputKeepsRawOutputWhileDroppingSpinnerLog(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = newLogBuffer(10)
	defer func() { sessionLogs = oldLogs }()

	raw := "Downloading\r|\r/\r-\rDone\n"
	var output bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	streamCommandOutput(strings.NewReader(raw), "stdout", &output, &wg)
	wg.Wait()

	if output.String() != raw {
		t.Fatalf("raw output changed: got %q want %q", output.String(), raw)
	}
	entries := sessionLogs.Since(0)
	if len(entries) != 1 || entries[0].Message != "Done" {
		t.Fatalf("expected only final log line, got %#v", entries)
	}
}

func TestAppendLogChunkPreservesNormalLines(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = newLogBuffer(10)
	defer func() { sessionLogs = oldLogs }()

	pending := appendLogChunk("stdout", "", "first\r")
	pending = appendLogChunk("stdout", pending, "\nsecond\nthird")
	pending = appendLogChunk("stdout", pending, "\n")
	if pending != "" {
		t.Fatalf("expected no pending log text, got %q", pending)
	}

	entries := sessionLogs.Since(0)
	if len(entries) != 3 || entries[0].Message != "first" || entries[1].Message != "second" || entries[2].Message != "third" {
		t.Fatalf("unexpected normal log lines: %#v", entries)
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

func TestRunCommandContextCancellation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows command cancellation test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result := runCommandContext(ctx, 10*time.Second, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "Start-Sleep -Seconds 5")

	if result.OK || result.Code != commandCancelledCode || !strings.Contains(result.Stderr, "Cancelled.") {
		t.Fatalf("expected cancelled command result, got %#v", result)
	}
}

func TestRunCommandContextCancellationWhileWaitingForMutationLock(t *testing.T) {
	packageManagerMutationMu.Lock()
	defer packageManagerMutationMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	result := runCommandContext(ctx, 10*time.Second, "choco.exe", "upgrade", "example-package")
	elapsed := time.Since(started)

	if result.OK || result.Code != commandCancelledCode || !strings.Contains(result.Stderr, "Cancelled.") {
		t.Fatalf("expected cancelled command result, got %#v", result)
	}
	if elapsed > time.Second {
		t.Fatalf("cancel while waiting for package-manager lock took too long: %s", elapsed)
	}
	if !strings.Contains(result.Command, "choco.exe upgrade example-package") {
		t.Fatalf("unexpected command text: %q", result.Command)
	}
}

func TestRunCommandContextTimeoutWhileWaitingForMutationLock(t *testing.T) {
	packageManagerMutationMu.Lock()
	defer packageManagerMutationMu.Unlock()

	started := time.Now()
	result := runCommandContext(context.Background(), 50*time.Millisecond, "choco.exe", "upgrade", "example-package")
	elapsed := time.Since(started)

	if result.OK || result.Code != 124 || !strings.Contains(result.Stderr, "Timed out.") {
		t.Fatalf("expected timeout command result, got %#v", result)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout while waiting for package-manager lock took too long: %s", elapsed)
	}
	if !strings.Contains(result.Command, "choco.exe upgrade example-package") {
		t.Fatalf("unexpected command text: %q", result.Command)
	}
}

func TestUpdateJobRejectsConcurrentStarts(t *testing.T) {
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	status, err := app.startUpdateJob(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.Total != 2 {
		t.Fatalf("unexpected initial job status: %#v", status)
	}
	if len(status.PackageKeys) != 2 || status.PackageKeys[0] != "winget:Git.Git" || status.PackageKeys[1] != "choco:gh" {
		t.Fatalf("unexpected job package keys: %#v", status.PackageKeys)
	}

	_, err = app.startUpdateJob(nil)
	if !errors.Is(err, errUpdateJobRunning) {
		t.Fatalf("expected concurrent start rejection, got %v", err)
	}
	app.cancelUpdateJob()
	waitForUpdateJobStopped(t, app)
}

func TestUpdateJobCancelStopsQueuedPackages(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	var calls int
	var mu sync.Mutex
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		mu.Lock()
		calls++
		mu.Unlock()
		once.Do(func() { close(started) })
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not start first package")
	}
	cancelStatus := app.cancelUpdateJob()
	if !cancelStatus.CancelRequested {
		t.Fatalf("expected cancel requested status, got %#v", cancelStatus)
	}
	status := waitForUpdateJobStopped(t, app)
	if !status.CancelRequested || status.Running || !status.RefreshStarted {
		t.Fatalf("unexpected cancelled status: %#v", status)
	}
	if len(status.Results) != 1 || status.Results[0].Result.Code != commandCancelledCode {
		t.Fatalf("expected one cancelled result, got %#v", status.Results)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected queued package to be skipped after cancel, calls=%d", calls)
	}
}

func TestUpdateJobStatusEndpointReportsProgress(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return CommandResult{OK: true, Command: id}
		case <-ctx.Done():
			return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
		}
	})
	defer restore()

	app := testUpdateJobApp()
	app.token = "test-token"
	if _, err := app.startUpdateJob([]string{"winget:Git.Git"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not report progress")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/update-all/status?token=test-token", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var status UpdateJobStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.CurrentPackage != "Git" || status.CurrentIndex != 1 || status.Total != 1 {
		t.Fatalf("unexpected progress status: %#v", status)
	}
	if len(status.PackageKeys) != 1 || status.PackageKeys[0] != "winget:Git.Git" {
		t.Fatalf("expected job package keys in status, got %#v", status.PackageKeys)
	}
	close(release)
	waitForUpdateJobStopped(t, app)
}

func TestUpdateJobKeepsRunningUntilRefreshStarts(t *testing.T) {
	refreshEntered := make(chan struct{})
	releaseRefresh := make(chan struct{})
	restore := replaceUpdateJobHooksWithRefresh(
		func(ctx context.Context, manager, id string) CommandResult {
			return CommandResult{OK: true, Command: id}
		},
		func(app *App) {
			close(refreshEntered)
			<-releaseRefresh
		},
	)
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob([]string{"winget:Git.Git"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not start inventory refresh")
	}
	if status := app.updateJobStatus(); !status.Running || status.RefreshStarted {
		t.Fatalf("job should not publish final status before refresh starts, got %#v", status)
	}
	close(releaseRefresh)
	status := waitForUpdateJobStopped(t, app)
	if status.Running || !status.RefreshStarted {
		t.Fatalf("expected stopped job with refresh started, got %#v", status)
	}
}

func TestUpdateJobRejectsConcurrentStartBeforeValidation(t *testing.T) {
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob(nil); err != nil {
		t.Fatal(err)
	}
	_, err := app.startUpdateJob([]string{"not-a-valid-key"})
	if !errors.Is(err, errUpdateJobRunning) {
		t.Fatalf("expected running-job rejection before validation, got %v", err)
	}
	app.cancelUpdateJob()
	waitForUpdateJobStopped(t, app)
}

func replaceUpdateJobHooks(runner func(context.Context, string, string) CommandResult) func() {
	return replaceUpdateJobHooksWithRefresh(runner, func(app *App) {})
}

func replaceUpdateJobHooksWithRefresh(runner func(context.Context, string, string) CommandResult, refresh func(*App)) func() {
	oldRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	updatePackageRunner = runner
	refreshInventoryAfterUpdateJob = refresh
	return func() {
		updatePackageRunner = oldRunner
		refreshInventoryAfterUpdateJob = oldRefresh
	}
}

func testUpdateJobApp() *App {
	return &App{inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{
		{Key: "winget:Git.Git", Manager: managerWinget, ID: "Git.Git", Name: "Git", UpdateAvailable: true, UpdateSupported: true},
		{Key: "choco:gh", Manager: managerChoco, ID: "gh", Name: "GitHub CLI", UpdateAvailable: true, UpdateSupported: true},
	}}}}
}

func waitForUpdateJobStopped(t *testing.T, app *App) UpdateJobStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := app.updateJobStatus()
		if !status.Running {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("update job did not stop")
	return UpdateJobStatus{}
}

func TestAPIRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		content    string
		wantResult bool
		wantText   string
	}{
		{"update form", "/api/update?token=test-token", "manager=invalid&package_id=Git.Git", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"install form", "/api/install?token=test-token", "manager=invalid&package_id=Git.Git", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"manager install form", "/api/managers/install?token=test-token", "manager=invalid", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"update all form", "/api/update-all?token=test-token", "package_key=not-a-valid-key", "application/x-www-form-urlencoded", false, "package key must be manager:id"},
		{"update json", "/api/update?token=test-token", `{"manager":"invalid","package_id":"Git.Git"}`, "application/json", true, managerValidationMessage},
		{"install json", "/api/install?token=test-token", `{"manager":"winget","package_id":"bad id"}`, "application/json", true, "package id contains unsupported characters"},
		{"manager install json", "/api/managers/install?token=test-token", `{"manager":"invalid"}`, "application/json", true, managerValidationMessage},
		{"update all json", "/api/update-all?token=test-token", `{"package_keys":["not-a-valid-key"]}`, "application/json", false, "package key must be manager:id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{token: "test-token"}
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.content)
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

func TestSettingsJSONRequestParsers(t *testing.T) {
	startupRequest := httptest.NewRequest(http.MethodPost, "/api/settings/startup", strings.NewReader(`{"enabled":true}`))
	startupRequest.Header.Set("Content-Type", "application/json")
	enabled, invalidStartup := parseStartupRequest(startupRequest)
	if invalidStartup != nil || !enabled {
		t.Fatalf("expected enabled startup JSON parse, enabled=%t invalid=%#v", enabled, invalidStartup)
	}

	autoRequest := httptest.NewRequest(http.MethodPost, "/api/settings/auto-update", strings.NewReader(`{"global":true,"package_keys":["winget:Git.Git"],"package_enabled":false}`))
	autoRequest.Header.Set("Content-Type", "application/json")
	global, keys, packageEnabled, invalidAuto := parseAutoUpdateRequest(autoRequest)
	if invalidAuto != nil || global == nil || !*global || packageEnabled == nil || *packageEnabled || len(keys) != 1 || keys[0] != "winget:Git.Git" {
		t.Fatalf("unexpected auto-update JSON parse: global=%v keys=%#v packageEnabled=%v invalid=%#v", global, keys, packageEnabled, invalidAuto)
	}

	themeRequest := httptest.NewRequest(http.MethodPost, "/api/settings/theme", strings.NewReader(`{"theme":"light"}`))
	themeRequest.Header.Set("Content-Type", "application/json")
	theme, err := parseThemeRequest(themeRequest)
	if err != nil || theme != "light" {
		t.Fatalf("unexpected theme JSON parse: theme=%q err=%v", theme, err)
	}
}

func TestSettingsAPIsRejectMalformedJSONBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		wantResult bool
	}{
		{"startup", "/api/settings/startup?token=test-token", `{"enabled":`, true},
		{"auto update", "/api/settings/auto-update?token=test-token", `{"package_keys":{}}`, true},
		{"theme", "/api/settings/theme?token=test-token", `{"theme":`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{token: "test-token"}
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			app.serveHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected bad request, got %d: %s", response.Code, response.Body.String())
			}
			if tc.wantResult {
				var decoded commandAPIResponse
				if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.Result == nil || decoded.Result.Code != 2 || !strings.Contains(decoded.Result.Stderr, "invalid JSON body") {
					t.Fatalf("expected validation command result, got %#v", decoded.Result)
				}
				return
			}
			var decoded apiErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(decoded.Error, "invalid JSON body") {
				t.Fatalf("expected invalid JSON error, got %#v", decoded)
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
		`startUpdateJob`,
		`pollUpdateJobStatus`,
		`checkActiveUpdateJob`,
		`api("/api/update-all/status"`,
		`postForm("/api/update-all/cancel"`,
		`id="cancel-updates-button"`,
		`status.package_keys`,
		`applyUpdateJobPackageKeys`,
		`response.status === 409 && status.running`,
		`active && !status.cancel_requested`,
		`installFromForm`,
		`installManagerFromForm`,
		`refreshPackagesAfterUpdate`,
		`id="session-log-panel"`,
		`id="copy-log-view"`,
		`id="clear-log-view"`,
		`id="log-autoscroll"`,
		`copyLogView`,
		`navigator.clipboard.writeText`,
		`document.execCommand("copy")`,
		`api("/api/logs"`,
		`id="updates-body"`,
		`id="installed-search"`,
		`id="installed-page-status"`,
		`packageMatchesInstalledSearch`,
		`managersRendered`,
		`renderUpdatesTable`,
		`renderInstalledTable`,
		`installedAction`,
		`updating-current`,
		`managerAvailabilityText`,
		`managerDisplayDetails`,
		`compactNoticeText`,
		`truncateNoticeText`,
		`firstMeaningfulOutputLine`,
		`See Session Log for full output.`,
		`max-height:96px`,
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
	progressIndex := strings.Index(rendered, `id="update-progress"`)
	updatesIndex := strings.Index(rendered, `Updates Available`)
	if progressIndex < 0 || updatesIndex < 0 || progressIndex > updatesIndex {
		t.Fatalf("expected update progress banner before updates table, progress=%d updates=%d", progressIndex, updatesIndex)
	}
}

func TestUpdateAllFailureNoticeCompactsNoisyChocolateyOutput(t *testing.T) {
	noisyOutput := `Chocolatey v2.7.2
Upgrading the following packages:
all
By upgrading, you accept licenses for the packages.
anaconda3 v2025.12.0 is the latest version available based on your source(s).
arduino v2.3.6 is the latest version available based on your source(s).
You have chocolatey v2.7.2 installed. Version 2.7.3 is available based on your source(s).
Downloading package from source 'https://community.chocolatey.org/api/v2/'
[Approved] chocolatey package files upgrade completed. Performing other installation steps.
WARNING: It's very likely you will need to close and reopen shells before you can use choco.
`
	notice := updateAllFailureNotice([]UpdateResult{{
		Key: packageKey(managerChoco, "*"),
		Result: CommandResult{
			Code:    1603,
			Command: `C:\ProgramData\chocolatey\bin\choco.exe upgrade all -y --no-progress --no-color`,
			Stdout:  noisyOutput,
		},
	}})

	for _, expected := range []string{
		"1 update command(s) finished with errors.",
		"choco upgrade all failed with code 1603",
		"WARNING:",
		"See Session Log for full output.",
	} {
		if !strings.Contains(notice, expected) {
			t.Fatalf("notice missing %q: %s", expected, notice)
		}
	}
	for _, unexpected := range []string{
		"anaconda3 v2025.12.0",
		"arduino v2.3.6",
		"[Approved] chocolatey package files",
	} {
		if strings.Contains(notice, unexpected) {
			t.Fatalf("notice included noisy output %q: %s", unexpected, notice)
		}
	}
	if len(notice) > 300 {
		t.Fatalf("notice too long: %d %q", len(notice), notice)
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
