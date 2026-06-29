package updater

import (
	"strings"
	"testing"
)

func TestParseLocalizedWingetTable(t *testing.T) {
	output := `
Name      ID              Version  Verfuegbar Quelle
---------------------------------------------------
Git       Git.Git         2.53.0   2.54.0    winget
Zed       Zed.Zed         0.233.10           winget
Mystery   Vendor.Mystery  Unknown  1.2.0     winget
`
	got := parseWingetTable(output)
	if len(got) != 3 {
		t.Fatalf("expected 3 packages, got %d: %#v", len(got), got)
	}
	if got[0].ID != "Git.Git" || got[0].AvailableVersion != "2.54.0" {
		t.Fatalf("unexpected first package: %#v", got[0])
	}
	if got[1].Source != "winget" {
		t.Fatalf("expected source winget, got %#v", got[1])
	}
	if !got[2].UnknownVersion || got[2].AvailableVersion != "1.2.0" {
		t.Fatalf("expected unknown installed version update parse, got %#v", got[2])
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

func TestParseWingetSearchTableWhenNameFillsColumn(t *testing.T) {
	output := `
Name                                   ID                                        Version        Quelle
--------------------------------------------------------------------------------------------------------
Visual Studio Community                XPDCFJDKLZJLP8                            Unknown        msstore
Microsoft Visual Studio Community 2019 Microsoft.VisualStudio.2019.Community     16.11.37301.9  winget
Visual Studio Community 2022           Microsoft.VisualStudio.2022.Community     17.14.35       winget
Visual Studio Community 2026 Insiders  Microsoft.VisualStudio.Community.Insiders 18.8.11912.234 winget
`
	got := parseWingetTable(output)
	if len(got) != 4 {
		t.Fatalf("expected 4 packages, got %d: %#v", len(got), got)
	}
	if got[1].Name != "Microsoft Visual Studio Community 2019" || got[1].ID != "Microsoft.VisualStudio.2019.Community" || got[1].Source != sourceWinget {
		t.Fatalf("expected full-width Community 2019 row to parse, got %#v", got[1])
	}
	if got[2].Name != "Visual Studio Community 2022" || got[2].ID != "Microsoft.VisualStudio.2022.Community" || got[2].Version != "17.14.35" {
		t.Fatalf("expected Community 2022 row to parse, got %#v", got[2])
	}
	if got[3].ID != "Microsoft.VisualStudio.Community.Insiders" || got[3].Source != sourceWinget {
		t.Fatalf("expected Community Insiders row to parse, got %#v", got[3])
	}
}

func TestParseWingetTableKeepsDoubleSpaceDisplayNameInNameColumn(t *testing.T) {
	output := `
Name                                                         ID                                                                                         Version                       Verfügbar                     Quelle
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
Microsoft Visual C++ 2010  x64 Redistributable - 10.0.40219  Microsoft.VCRedist.2010.x64                                                                10.0.40219                                                  winget
`
	got := parseWingetTable(output)
	if len(got) != 1 {
		t.Fatalf("expected 1 package, got %d: %#v", len(got), got)
	}
	pkg := got[0]
	if pkg.Name != "Microsoft Visual C++ 2010  x64 Redistributable - 10.0.40219" {
		t.Fatalf("display name shifted or changed: %#v", pkg)
	}
	if pkg.ID != "Microsoft.VCRedist.2010.x64" || pkg.Version != "10.0.40219" || pkg.AvailableVersion != "" || pkg.Source != sourceWinget {
		t.Fatalf("expected exact ID/current version with no available update, got %#v", pkg)
	}
}

func TestWingetInventoryDoesNotTreatDoubleSpaceDisplayNameAsUpdate(t *testing.T) {
	output := `
Name                                                         ID                                                                                         Version                       Verfügbar                     Quelle
---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
Microsoft Visual C++ 2010  x64 Redistributable - 10.0.40219  Microsoft.VCRedist.2010.x64                                                                10.0.40219                                                  winget
`
	inventory := managerInventory{
		manager:       managerWinget,
		installed:     parseWingetTable(output),
		updates:       map[string]string{},
		updateDetails: map[string]Package{},
	}
	got := packagesFromManagerInventory(State{}, inventory)
	if len(got) != 1 {
		t.Fatalf("expected 1 package, got %d: %#v", len(got), got)
	}
	if got[0].ID != "Microsoft.VCRedist.2010.x64" || got[0].UpdateAvailable || got[0].AvailableVersion != "" {
		t.Fatalf("current Visual C++ row must not become an update candidate, got %#v", got[0])
	}
}

func TestParseWingetExactIDSearchTableWithoutSourceColumn(t *testing.T) {
	output := `
Name                          ID                                     Version
-----------------------------------------------------------------------------
Visual Studio BuildTools 2022 Microsoft.VisualStudio.2022.BuildTools 17.14.35
`
	got := parseWingetSearchPackages(CommandResult{OK: true, Stdout: output})
	if len(got) != 1 {
		t.Fatalf("expected 1 package, got %d: %#v", len(got), got)
	}
	if got[0].Name != "Visual Studio BuildTools 2022" || got[0].ID != "Microsoft.VisualStudio.2022.BuildTools" || got[0].Key != "winget:Microsoft.VisualStudio.2022.BuildTools" {
		t.Fatalf("expected exact BuildTools row to be search result, got %#v", got[0])
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

func TestParseWingetTableMarksPinnedRows(t *testing.T) {
	output := `
Name        ID             Version  Available Source  Pinned
-------------------------------------------------------------
Pinned App  Vendor.Pinned  1.0      2.0       winget  Pinned
Normal App  Vendor.Normal  1.0      2.0       winget
`
	got := parseWingetTable(output)
	if len(got) != 2 {
		t.Fatalf("expected two rows, got %#v", got)
	}
	if !got[0].Pinned || got[0].Source != sourceWinget || got[0].AvailableVersion != "2.0" {
		t.Fatalf("expected pinned update row with source and version, got %#v", got[0])
	}
	if got[1].Pinned {
		t.Fatalf("normal row should not be pinned: %#v", got[1])
	}
}

func TestMergeWingetUpdateOutputCarriesPinnedMetadata(t *testing.T) {
	output := `
Name        ID             Version  Available Source  Pinned
-------------------------------------------------------------
Pinned App  Vendor.Pinned  1.0      2.0       winget  Pinned
`
	updates := map[string]string{}
	details := map[string]Package{}
	mergeWingetUpdateOutput(updates, details, output, "")
	key := packageKey(managerWinget, strings.ToLower("Vendor.Pinned"))
	if updates[key] != "2.0" || !details[key].Pinned {
		t.Fatalf("expected pinned metadata in update details, updates=%#v details=%#v", updates, details)
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

func TestUnknownWingetVersionsIncludeMissingValues(t *testing.T) {
	for _, version := range []string{"", " ", "-", "Unknown", "Unbekannt"} {
		if !isUnknownPackageVersion(version) {
			t.Fatalf("expected %q to be treated as an unknown package version", version)
		}
	}
	if isUnknownPackageVersion("1.2.3") {
		t.Fatal("known semantic version was treated as unknown")
	}
}

func TestParseWingetExportMarksMissingVersionUnknown(t *testing.T) {
	output := `{
  "Sources": [{
    "Packages": [{"PackageIdentifier": "Vendor.Unknown", "Version": ""}],
    "SourceDetails": {"Name": "winget"}
  }]
}`
	got := parseWingetExport(output)
	if len(got) != 1 || !got[0].UnknownVersion {
		t.Fatalf("expected missing export version to be unknown, got %#v", got)
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

func TestMergeWingetUpdateOutputForcesMSStoreSource(t *testing.T) {
	output := `
Name                 ID                                    Version          Available
-----------------------------------------------------------------------------------
Windows App Runtime  Microsoft.WindowsAppRuntime.Singleton  8000.318.101.0  8000.328.111.0
Codex                OpenAI.Codex                          0.1.0            0.2.0
`
	updates := map[string]string{}
	mergeWingetUpdateOutput(updates, nil, output, managerStore)

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

func TestMergeWingetExportWithTableKeepsOnlyTruncatedMSIXRowsAsActionTargets(t *testing.T) {
	table := []Package{
		{Name: "WinAppRuntime.Main.1.8", ID: "MSIX\\MicrosoftCorporationII.WinAppRuntime.M…", Version: "8000.859.21.0", Manager: managerWinget},
		{Name: "Truncated Desktop", ID: "SomeVendor.SomeReallyLongDesktopPackageIdent…", Version: "1.0", Manager: managerWinget, Source: sourceWinget},
	}

	got := mergeWingetExportWithTable(nil, table)

	if len(got) != 1 {
		t.Fatalf("expected only truncated MSIX rows to be preserved, got %#v", got)
	}
	var sawStore bool
	for _, pkg := range got {
		if pkg.Manager == managerStore && pkg.Source == sourceMSStore && pkg.ID == "WinAppRuntime.Main.1.8" && pkg.ActionBackend == backendWingetMSStoreFallback {
			sawStore = true
		}
	}
	if !sawStore {
		t.Fatalf("expected truncated MSIX row to become a Store action target, got %#v", got)
	}
}

func TestMergeWingetUpdateOutputKeepsOnlyTruncatedMSIXUpdatesByName(t *testing.T) {
	storeOutput := `
Name                   ID                                                                                 Version       Available
-------------------------------------------------------------------------------------------------------------------------------
WinAppRuntime.Main.1.8  MSIX\MicrosoftCorporationII.WinAppRuntime.Main.1.8_8000.859.21.0_x64__8wekyb3d8bb…  8000.859.21.0  8000.900.1.0
`
	updates := map[string]string{}

	mergeWingetUpdateOutput(updates, nil, storeOutput, managerStore)

	if updates[packageKey(managerStore, strings.ToLower("WinAppRuntime.Main.1.8"))] != "8000.900.1.0" {
		t.Fatalf("expected truncated MSIX update to be keyed by package name, got %#v", updates)
	}
	desktopOutput := `
Name               ID                                                               Version Available Source
------------------------------------------------------------------------------------------------------------
Truncated Desktop  SomeVendor.SomeReallyLongDesktopPackageIdentifierThatCannotFit…  1.0     1.1       winget
`
	mergeWingetUpdateOutput(updates, nil, desktopOutput, "")
	if _, ok := updates[packageKey(managerWinget, strings.ToLower("Truncated Desktop"))]; ok {
		t.Fatalf("truncated desktop updates must not be keyed by display name, got %#v", updates)
	}
}
