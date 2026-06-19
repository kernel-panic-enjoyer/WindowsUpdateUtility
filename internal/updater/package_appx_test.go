package updater

import (
	"strings"
	"testing"
)

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

func TestMergeStoreNativeUpdatePackagesKeepsUnmatchedUpdate(t *testing.T) {
	got := mergeStoreNativeUpdatePackages(nil, []Package{
		{
			Key:              "store:Microsoft Store",
			ID:               "Microsoft Store",
			Name:             "Microsoft Store",
			AvailableVersion: "22605.1401.10.0",
			Manager:          managerStore,
			Source:           sourceStoreCLI,
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
			ActionBackend:    backendStoreCLI,
		},
	})
	if len(got) != 1 {
		t.Fatalf("expected unmatched native Store update row to remain, got %#v", got)
	}
	if got[0].ID != "Microsoft Store" || !got[0].UpdateAvailable || got[0].AvailableVersion != "22605.1401.10.0" {
		t.Fatalf("unexpected native Store update row: %#v", got[0])
	}
}

func TestMergeStoreNativeUpdatePackagesMarksInstalledStoreRow(t *testing.T) {
	installed := []Package{
		{
			Key:             "store:Codex",
			ID:              "Codex",
			Name:            "Codex",
			Version:         "26.611.8604.0",
			Manager:         managerStore,
			Source:          sourceStoreCLI,
			UpdateSupported: true,
			Installed:       true,
			ActionBackend:   backendStoreCLI,
		},
	}
	updates := []Package{
		{
			Key:              "store:Codex",
			ID:               "Codex",
			Name:             "Codex",
			AvailableVersion: "26.611.8604.0",
			Manager:          managerStore,
			Source:           sourceStoreCLI,
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
			ActionBackend:    backendStoreCLI,
		},
	}
	got := mergeStoreNativeUpdatePackages(installed, updates)
	if len(got) != 1 {
		t.Fatalf("expected native Store update to merge into installed row, got %#v", got)
	}
	if !got[0].UpdateAvailable || got[0].AvailableVersion != "26.611.8604.0" {
		t.Fatalf("expected installed Store row to be marked updateable, got %#v", got[0])
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

func TestApplyStoreUpdateVersionKeepsExplicitStoreUpdateWhenVersionsMatch(t *testing.T) {
	pkg := Package{
		ID:               "OpenAI.Codex_26.611.8273.0_x64__abc123",
		Name:             "Codex",
		Version:          "26.611.8273.0",
		AvailableVersion: "26.611.8273.0",
		Manager:          managerStore,
		Source:           sourceAppX,
		Match:            "OpenAI.Codex_abc123",
		ActionBackend:    backendStoreCLIResolved,
		UpdateAvailable:  true,
		UpdateSupported:  true,
	}
	updates := map[string]string{
		packageKey(managerStore, "codex"): "26.611.8273.0",
	}

	got := applyStoreUpdateVersion(pkg, updates, true)

	if !got.UpdateAvailable || got.AvailableVersion != "26.611.8273.0" || got.ID != "Codex" {
		t.Fatalf("explicit Store update row should remain updateable even when AppX version matches, got %#v", got)
	}
}

func TestApplyStoreUpdateVersionIgnoresOlderAvailableVersion(t *testing.T) {
	pkg := Package{
		ID:            "OpenAI.Codex_26.611.8273.0_x64__abc123",
		Name:          "Codex",
		Version:       "26.611.8273.0",
		Manager:       managerStore,
		Source:        sourceAppX,
		Match:         "OpenAI.Codex_abc123",
		ActionBackend: backendStoreCLIResolved,
	}
	updates := map[string]string{
		packageKey(managerStore, "codex"): "26.609.4994.0",
	}

	got := applyStoreUpdateVersion(pkg, updates, true)

	if got.UpdateAvailable || got.AvailableVersion != "" {
		t.Fatalf("older Store available version should not mark an update, got %#v", got)
	}
}
