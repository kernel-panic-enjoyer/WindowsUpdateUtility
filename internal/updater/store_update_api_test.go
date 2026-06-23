package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStoreAssessmentFeatureFlagDisabledAndEnabled(t *testing.T) {
	now := fixedStoreAssessmentClock(t)
	restoreSID := replaceStoreAssessmentSID("S-1-5-21-test")
	defer restoreSID()

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{
		CommandResults: map[string]CommandResult{"store_updates": {OK: true, Command: "store updates --apply false"}},
		Packages: []Package{{
			Key:              "store:Codex",
			Manager:          managerStore,
			ID:               "Codex",
			Name:             "Codex",
			Version:          "1.0.0",
			AvailableVersion: "1.1.0",
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
			Source:           sourceAppX,
			Match:            "OpenAI.Codex_abc123",
			ActionBackend:    backendStoreCLIResolved,
		}},
	}}
	app.inventoryFetchedAt = now

	disabled := requestPackages(t, app)
	if got := disabled.Packages[0]; got.UpdateState != "" || !got.UpdateAvailable {
		t.Fatalf("feature disabled should preserve legacy package, got %#v", got)
	}

	enableStoreAssessmentForTest(t)
	enabled := requestPackages(t, app)
	got := enabled.Packages[0]
	if got.UpdateState != string(StoreUpdateUnknown) {
		t.Fatalf("feature enabled state = %q, want unknown", got.UpdateState)
	}
	if got.UpdateAvailable {
		t.Fatalf("legacy UpdateAvailable must derive from update_state only, got true")
	}
	if !got.ExactIdentityAvailable || got.ExactActionTargetAvailable {
		t.Fatalf("unexpected exact identity/action flags: %#v", got)
	}
	if !strings.Contains(got.UpdateReason, "no exact verified Store action target") {
		t.Fatalf("unexpected reason: %q", got.UpdateReason)
	}
}

func TestStoreAssessmentAPISerializesStates(t *testing.T) {
	now := fixedStoreAssessmentClock(t)
	restoreSID := replaceStoreAssessmentSID("S-1-5-21-test")
	defer restoreSID()
	enableStoreAssessmentForTest(t)

	packages := []Package{
		storeAssessmentPackage("current", StoreUpdateCurrent, true),
		storeAssessmentPackage("available", StoreUpdateAvailable, true),
		storeAssessmentPackage("one-provider-failed", StoreUpdateUnknown, true),
		storeAssessmentPackage("all-providers-failed", StoreUpdateUnknown, true),
		storeAssessmentPackage("conflict", StoreUpdateConflict, true),
		storeAssessmentPackage("inapplicable", StoreUpdateInapplicable, true),
		storeAssessmentPackage("pending", StoreUpdatePending, true),
		storeAssessmentPackage("stale", StoreUpdateAvailable, true),
		storeAssessmentPackage("no-target", StoreUpdateAvailable, false),
	}
	packages[2].UpdateReason = "Store CLI failed; WinGet msstore completed."
	packages[2].ProviderSummaries = []StorePackageProviderSummary{{Name: "Store CLI", Health: string(StoreProviderFailed), Kind: string(StoreObservationProviderFailure), ObservedAt: formatAssessmentTime(now), Error: "failed"}}
	packages[3].UpdateReason = "all Store providers failed"
	packages[3].ProviderSummaries = []StorePackageProviderSummary{
		{Name: "Store CLI", Health: string(StoreProviderFailed), Kind: string(StoreObservationProviderFailure), ObservedAt: formatAssessmentTime(now), Error: "failed"},
		{Name: "WinGet msstore", Health: string(StoreProviderFailed), Kind: string(StoreObservationProviderFailure), ObservedAt: formatAssessmentTime(now), Error: "failed"},
	}
	packages[7].Stale = true
	packages[7].UpdateReason = "retained last known positive update because the latest Store scan is incomplete"

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{Packages: packages}}
	app.inventoryFetchedAt = now
	response := requestPackages(t, app)

	byID := map[string]Package{}
	for _, pkg := range response.Packages {
		byID[pkg.ID] = pkg
		if pkg.Manager == managerStore && pkg.UpdateAvailable != (pkg.UpdateState == string(StoreUpdateAvailable)) {
			t.Fatalf("%s UpdateAvailable=%v state=%s", pkg.ID, pkg.UpdateAvailable, pkg.UpdateState)
		}
	}
	for id, state := range map[string]string{
		"current":              string(StoreUpdateCurrent),
		"available":            string(StoreUpdateAvailable),
		"one-provider-failed":  string(StoreUpdateUnknown),
		"all-providers-failed": string(StoreUpdateUnknown),
		"conflict":             string(StoreUpdateConflict),
		"inapplicable":         string(StoreUpdateInapplicable),
		"pending":              string(StoreUpdatePending),
		"stale":                string(StoreUpdateAvailable),
		"no-target":            string(StoreUpdateAvailable),
	} {
		if byID[id].UpdateState != state {
			t.Fatalf("%s state=%q, want %q", id, byID[id].UpdateState, state)
		}
	}
	if !byID["stale"].Stale {
		t.Fatal("expected stale positive update to remain marked stale")
	}
	if byID["no-target"].UpdateSupported || packageAllowedInBulkUpdate(byID["no-target"], UpdateOptions{}) {
		t.Fatal("Store package without exact target must not be updateable")
	}
	if len(byID["all-providers-failed"].ProviderSummaries) != 2 {
		t.Fatalf("expected provider diagnostics to serialize: %#v", byID["all-providers-failed"].ProviderSummaries)
	}
}

func TestNewStoreAPIScanGenerationRecordsSystemContext(t *testing.T) {
	restoreContext := replaceStoreScanSystemContext(storeScanSystemContext{
		WindowsVersion: "Windows 10 22H2",
		WindowsBuild:   "10.0.19045.4529",
		Architecture:   "x64",
	})
	defer restoreContext()

	scan := newStoreAPIScanGeneration("S-1-5-21-api-context", time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC))
	if scan.WindowsVersion != "Windows 10 22H2" || scan.WindowsBuild != "10.0.19045.4529" || scan.Architecture != "x64" {
		t.Fatalf("API scan context was not recorded: %#v", scan)
	}
}

func TestStoreAssessmentUnresolvedIdentityIsUnknown(t *testing.T) {
	fixedStoreAssessmentClock(t)
	restoreSID := replaceStoreAssessmentSID("S-1-5-21-test")
	defer restoreSID()
	enableStoreAssessmentForTest(t)

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             "store:Codex",
		Manager:         managerStore,
		ID:              "Codex",
		Name:            "Codex",
		UpdateSupported: true,
		Installed:       true,
		Source:          sourceStoreCLI,
		ActionBackend:   backendStoreCLI,
	}}}}
	app.inventoryFetchedAt = storeAssessmentNow()

	got := requestPackages(t, app).Packages[0]
	if got.UpdateState != string(StoreUpdateUnknown) || got.ExactIdentityAvailable {
		t.Fatalf("unresolved identity should be unknown without exact identity: %#v", got)
	}
	if !strings.Contains(got.UpdateReason, "unresolved") {
		t.Fatalf("unexpected unresolved reason: %q", got.UpdateReason)
	}
}

func TestStoreAssessmentRetainsStalePositiveDuringIncompleteRescan(t *testing.T) {
	now := fixedStoreAssessmentClock(t)
	restoreSID := replaceStoreAssessmentSID("S-1-5-21-test")
	defer restoreSID()
	enableStoreAssessmentForTest(t)
	stateDir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", stateDir)

	state := defaultState()
	state.StoreUpdateAssessmentCache[storeAssessmentCacheKey("S-1-5-21-test", "OpenAI.Codex_abc123")] = StoreUpdateAssessmentCacheEntry{
		UserSID:                    "S-1-5-21-test",
		PackageFamilyName:          "OpenAI.Codex_abc123",
		ScanID:                     "previous-scan",
		State:                      string(StoreUpdateAvailable),
		Reason:                     "previous exact positive",
		ObservedAt:                 "2026-06-21T12:00:00Z",
		InstalledVersion:           "1.0.0",
		OfferedVersion:             "1.1.0",
		StoreProductID:             "9NTEST",
		Applicability:              "applicable",
		ExactActionTargetAvailable: true,
	}
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{
		CommandResults: map[string]CommandResult{"store_updates": {Command: "store updates --apply false", Code: 1, Stderr: "provider failed"}},
		Packages: []Package{{
			Key:             "store:OpenAI.Codex_abc123",
			Manager:         managerStore,
			ID:              "OpenAI.Codex_abc123",
			Name:            "Codex",
			Version:         "1.0.0",
			UpdateSupported: true,
			Installed:       true,
			Source:          sourceNativeAppX,
			ActionBackend:   backendAppXInventory,
		}},
	}}
	app.inventoryFetchedAt = now

	got := requestPackages(t, app).Packages[0]
	if got.UpdateState != string(StoreUpdateAvailable) || !got.UpdateAvailable || !got.Stale {
		t.Fatalf("expected retained stale positive update, got %#v", got)
	}
	if got.AvailableVersion != "1.1.0" || got.ScanID != "previous-scan" {
		t.Fatalf("cached update details were not retained: %#v", got)
	}
}

func TestTransactionalStoreAssessmentAPISerializesPublishedStates(t *testing.T) {
	cases := []struct {
		name                string
		providers           []StoreCatalogProvider
		wantState           StoreUpdateState
		wantUpdateAvailable bool
		checkExactTarget    bool
		wantExactTarget     bool
		wantHealthy         bool
		wantReason          string
	}{
		{
			name:             "complete healthy scan with no updates",
			providers:        []StoreCatalogProvider{negativeProvider("OpenAI.Codex_abc123", "1.0.0")},
			wantState:        StoreUpdateCurrent,
			checkExactTarget: true,
			wantHealthy:      true,
		},
		{
			name:                "update available",
			providers:           []StoreCatalogProvider{positiveProvider("OpenAI.Codex_abc123", "1.0.0", "1.1.0")},
			wantState:           StoreUpdateAvailable,
			wantUpdateAvailable: true,
			checkExactTarget:    true,
			wantExactTarget:     true,
			wantHealthy:         true,
		},
		{
			name:       "one provider failed",
			providers:  []StoreCatalogProvider{negativeProvider("OpenAI.Codex_abc123", "1.0.0"), failingProvider("catalog timeout")},
			wantState:  StoreUpdateUnknown,
			wantReason: "incomplete",
		},
		{
			name:       "all providers failed",
			providers:  []StoreCatalogProvider{failingProvider("catalog unavailable")},
			wantState:  StoreUpdateUnknown,
			wantReason: "incomplete",
		},
		{
			name:       "provider disagreement",
			providers:  []StoreCatalogProvider{positiveAndNegativeProvider("OpenAI.Codex_abc123")},
			wantState:  StoreUpdateConflict,
			wantReason: "disagree",
		},
		{
			name:       "inapplicable offer",
			providers:  []StoreCatalogProvider{inapplicableProvider("OpenAI.Codex_abc123")},
			wantState:  StoreUpdateInapplicable,
			wantReason: "no applicable installer",
		},
		{
			name:             "exact target unavailable",
			providers:        []StoreCatalogProvider{positiveWithoutTargetProvider("OpenAI.Codex_abc123")},
			wantState:        StoreUpdateUnknown,
			checkExactTarget: true,
			wantReason:       "no exact verified target",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := transactionalPackagesResponse(t, tc.providers, false)
			got := findStorePackageByPFN(t, response.Packages, "OpenAI.Codex_abc123")
			if got.UpdateState != string(tc.wantState) {
				t.Fatalf("state=%q, want %q; package=%#v", got.UpdateState, tc.wantState, got)
			}
			if got.UpdateAvailable != tc.wantUpdateAvailable {
				t.Fatalf("UpdateAvailable=%v, want %v; package=%#v", got.UpdateAvailable, tc.wantUpdateAvailable, got)
			}
			if tc.checkExactTarget && got.ExactActionTargetAvailable != tc.wantExactTarget {
				t.Fatalf("ExactActionTargetAvailable=%v, want %v; package=%#v", got.ExactActionTargetAvailable, tc.wantExactTarget, got)
			}
			if tc.wantReason != "" && !strings.Contains(got.UpdateReason, tc.wantReason) {
				t.Fatalf("reason=%q, want substring %q", got.UpdateReason, tc.wantReason)
			}
			if response.StoreScanHealth.Healthy != tc.wantHealthy {
				t.Fatalf("health=%#v, want healthy=%v", response.StoreScanHealth, tc.wantHealthy)
			}
			if !tc.wantHealthy && response.StoreScanHealth.Authoritative {
				t.Fatalf("non-healthy Store scan must not be authoritative: %#v", response.StoreScanHealth)
			}
		})
	}
}

func TestTransactionalStoreAssessmentAPIStalePositiveAndBrowserReload(t *testing.T) {
	response := transactionalPackagesResponse(t, []StoreCatalogProvider{
		positiveProvider("OpenAI.Codex_abc123", "1.0.0", "1.1.0"),
		failingProvider("catalog timeout after previous positive"),
	}, true)
	got := findStorePackageByPFN(t, response.Packages, "OpenAI.Codex_abc123")
	if !response.Loading {
		t.Fatal("expected loading snapshot to survive browser reload during scanning")
	}
	if got.UpdateState != string(StoreUpdateAvailable) || !got.UpdateAvailable || !got.Stale {
		t.Fatalf("expected retained stale positive during incomplete rescan, got %#v", got)
	}
	if response.StoreScanHealth.Healthy || response.StoreScanHealth.Authoritative {
		t.Fatalf("stale positive must not make Store scan authoritative: %#v", response.StoreScanHealth)
	}
	if response.StoreScanHealth.Counts["stale"] == 0 {
		t.Fatalf("expected stale count in scan health: %#v", response.StoreScanHealth)
	}
}

func TestStoreAssessmentBrowserSmokeStrings(t *testing.T) {
	surface := uiJS + "\n" + uiCSS
	for _, expected := range []string{
		"storeUpdateState",
		"storeScanHealth",
		"store_scan_health",
		"latestStoreScanHealth",
		"renderStoreScanHealth",
		"Provider diagnostics",
		"Exact target unavailable",
		"Not authoritative",
		"authoritative",
		"Store update status is unknown. Review scan health.",
		"state-badge",
		"store-rescan-button",
	} {
		if !strings.Contains(surface, expected) {
			t.Fatalf("UI assets missing %q", expected)
		}
	}
}

func requestPackages(t *testing.T, app *App) InventoryResponse {
	t.Helper()
	request := authenticatedRequest(app, http.MethodGet, "/api/packages", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/packages status %d: %s", response.Code, response.Body.String())
	}
	var decoded InventoryResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func transactionalPackagesResponse(t *testing.T, providers []StoreCatalogProvider, loading bool) InventoryResponse {
	t.Helper()
	t.Setenv(storeTransactionalScanFeatureFlag, "1")
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	userSID := "S-1-5-21-transactional-api"
	pfn := "OpenAI.Codex_abc123"
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()
	store, err := openDefaultStoreScanStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) == 2 && providerIDForTest(providers[0]) == "catalog-positive" && providerIDForTest(providers[1]) == "catalog-failed" {
		runTestPipelineWithProviders(t, store, userSID, pfn, []StoreCatalogProvider{providers[0]}, "transactional-positive-scan")
		runTestPipelineWithProviders(t, store, userSID, pfn, []StoreCatalogProvider{providers[1]}, "transactional-incomplete-scan")
	} else {
		runTestPipelineWithProviders(t, store, userSID, pfn, providers)
	}
	_ = store.Close()

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{Packages: []Package{transactionalStoreAPIPackage(pfn)}}}
	app.inventoryFetchedAt = storeAssessmentNow()
	app.inventoryLoading = loading
	return requestPackages(t, app)
}

func runTestPipelineWithProviders(t *testing.T, store *StoreScanStore, userSID, pfn string, providers []StoreCatalogProvider, scanID ...string) StoreScanResult {
	t.Helper()
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	pipeline.CatalogProviders = providers
	if len(scanID) > 0 && scanID[0] != "" {
		pipeline.NewScanID = func(time.Time) string { return scanID[0] }
	}
	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func providerIDForTest(provider StoreCatalogProvider) string {
	if provider == nil {
		return ""
	}
	return provider.Identity().Key()
}

func transactionalStoreAPIPackage(pfn string) Package {
	return Package{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		UpdateSupported: false,
		Installed:       true,
		Source:          sourceNativeAppX,
		Match:           pfn,
		ActionBackend:   backendAppXInventory,
	}
}

func findStorePackageByPFN(t *testing.T, packages []Package, pfn string) Package {
	t.Helper()
	for _, pkg := range packages {
		if pkg.Manager == managerStore && strings.EqualFold(pkg.InstalledPackageFamilyName, pfn) {
			return pkg
		}
	}
	t.Fatalf("Store package with PFN %q not found in %#v", pfn, packages)
	return Package{}
}

func storeAssessmentPackage(id string, state StoreUpdateState, exactTarget bool) Package {
	return Package{
		Key:                        packageKey(managerStore, id),
		Manager:                    managerStore,
		ID:                         id,
		Name:                       id,
		Version:                    "1.0.0",
		AvailableVersion:           "1.1.0",
		UpdateState:                string(state),
		UpdateReason:               "test " + string(state),
		ObservedAt:                 "2026-06-21T12:00:00Z",
		ScanID:                     "scan-" + id,
		ExactIdentityAvailable:     true,
		ExactActionTargetAvailable: exactTarget,
		InstalledPackageFamilyName: id + "_abc123",
		StoreProductID:             "9N" + strings.ToUpper(strings.ReplaceAll(id, "-", "")),
		InstalledVersion:           "1.0.0",
		OfferedVersion:             "1.1.0",
		Applicability:              "applicable",
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		ActionBackend:              backendStoreCLI,
		ProviderSummaries:          []StorePackageProviderSummary{{Name: "Store provider", Health: string(StoreProviderHealthy), Kind: string(StoreObservationPositiveUpdateOffer), ObservedAt: "2026-06-21T12:00:00Z"}},
	}
}

func enableStoreAssessmentForTest(t *testing.T) {
	t.Helper()
	t.Setenv(storeUpdateAssessmentFeatureFlag, "1")
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
}

func fixedStoreAssessmentClock(t *testing.T) time.Time {
	t.Helper()
	now := time.Date(2026, 6, 21, 12, 34, 56, 0, time.UTC)
	oldNow := storeAssessmentNow
	storeAssessmentNow = func() time.Time { return now }
	t.Cleanup(func() { storeAssessmentNow = oldNow })
	return now
}

func replaceStoreAssessmentSID(sid string) func() {
	old := storeAssessmentCurrentUserSID
	storeAssessmentCurrentUserSID = func() (string, error) { return sid, nil }
	return func() { storeAssessmentCurrentUserSID = old }
}
