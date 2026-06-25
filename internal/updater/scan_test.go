package updater

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

func TestScanInstalledApplicationsReportsStateSaveFailure(t *testing.T) {
	oldRegistryReader := registryAppsReader
	oldWingetReader := wingetAppsReader
	oldAppxReader := appxAppsReader
	registryAppsReader = func() ([]ScannedApp, error) {
		return []ScannedApp{{Key: "registry:example", Name: "Example", Source: "registry"}}, nil
	}
	wingetAppsReader = func(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
		return nil, &CommandResult{OK: true, Command: "winget export"}, nil
	}
	appxAppsReader = func(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
		return nil, &CommandResult{OK: true, Command: "Get-AppxPackage"}, nil
	}
	defer func() {
		registryAppsReader = oldRegistryReader
		wingetAppsReader = oldWingetReader
		appxAppsReader = oldAppxReader
	}()

	store := newMemoryStateStore(defaultState())
	store.updateHook = func(*State) error {
		return errors.New("state write failed")
	}
	scan := scanInstalledApplicationsWithStore(context.Background(), store)
	found := false
	for _, item := range scan.Errors {
		if item["source"] == "state" && strings.Contains(item["error"], "state write failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected state save failure to be reported, got %#v", scan.Errors)
	}
	if scan.TrackedCount != 1 || scan.SourceCounts["registry"] != 1 {
		t.Fatalf("scan should still report collected apps, got tracked=%d counts=%#v", scan.TrackedCount, scan.SourceCounts)
	}
}
