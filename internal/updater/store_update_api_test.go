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
	if got.UpdateState != string(StoreUpdateAvailable) || got.UpdateAvailable || !got.Stale || got.UpdateSupported {
		t.Fatalf("expected retained stale positive during incomplete rescan, got %#v", got)
	}
	if response.StoreScanHealth.Healthy || response.StoreScanHealth.Authoritative {
		t.Fatalf("stale positive must not make Store scan authoritative: %#v", response.StoreScanHealth)
	}
	if response.StoreScanHealth.Counts["stale"] == 0 {
		t.Fatalf("expected stale count in scan health: %#v", response.StoreScanHealth)
	}
}

func TestPackageCapabilitiesSeparateStorePreferenceFromActionability(t *testing.T) {
	current := applyPackageCapabilities(Package{
		Key:                        packageKey(managerStore, "OpenAI.Codex_abc123"),
		Manager:                    managerStore,
		ID:                         "OpenAI.Codex_abc123",
		Name:                       "Codex",
		Version:                    "1.0.0",
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		InstalledPackageFamilyName: "OpenAI.Codex_abc123",
		UpdateState:                string(StoreUpdateCurrent),
	})
	if !current.PreferenceEligible || current.CanUpdateNow || current.ExactTargetKind != exactTargetKindNone {
		t.Fatalf("current exact Store package should be preference-eligible but not actionable: %#v", current)
	}

	unknown := current
	unknown.UpdateState = string(StoreUpdateUnknown)
	unknown = applyPackageCapabilities(unknown)
	if !unknown.PreferenceEligible || unknown.CanUpdateNow {
		t.Fatalf("unknown exact Store package should be preference-eligible but not actionable: %#v", unknown)
	}

	conflict := current
	conflict.UpdateState = string(StoreUpdateConflict)
	conflict.StoreProductID = "9NCODEX"
	conflict.ExactActionTargetAvailable = false
	conflict = applyPackageCapabilities(conflict)
	if !conflict.PreferenceEligible || conflict.CanUpdateNow || !strings.Contains(conflict.CannotUpdateReason, "conflict") {
		t.Fatalf("conflicting Store package must stay preference-only and non-actionable: %#v", conflict)
	}
}

func TestPackageCapabilitiesAllowUpdateIDOnlyStoreTarget(t *testing.T) {
	pkg := applyPackageCapabilities(Package{
		Key:                        packageKey(managerStore, "OpenAI.Codex_abc123"),
		Manager:                    managerStore,
		ID:                         "OpenAI.Codex_abc123",
		Name:                       "Codex",
		Version:                    "1.0.0",
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		InstalledPackageFamilyName: "OpenAI.Codex_abc123",
		UpdateState:                string(StoreUpdateAvailable),
		ExactActionTargetAvailable: true,
		StoreUpdateID:              "OpenAI.Codex_abc123",
	})
	if !pkg.PreferenceEligible || !pkg.CanUpdateNow || pkg.ExactTargetKind != exactTargetKindUpdateID || pkg.CannotUpdateReason != "" {
		t.Fatalf("update-ID-only Store target should be actionable by backend capability fields: %#v", pkg)
	}
}

func TestUpdateSelectionUsesSharedPackagePolicy(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	app := testSessionApp()
	actionable := applyPackageCapabilities(Package{
		Key:                        packageKey(managerStore, "OpenAI.Codex_abc123"),
		Manager:                    managerStore,
		ID:                         "OpenAI.Codex_abc123",
		Name:                       "Codex",
		Version:                    "1.0.0",
		UpdateAvailable:            true,
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		InstalledPackageFamilyName: "OpenAI.Codex_abc123",
		UpdateState:                string(StoreUpdateAvailable),
		ExactActionTargetAvailable: true,
		StoreUpdateID:              "OpenAI.Codex_abc123",
	})
	blocked := applyPackageCapabilities(Package{
		Key:                        packageKey(managerStore, "Blocked.App_abc123"),
		Manager:                    managerStore,
		ID:                         "Blocked.App_abc123",
		Name:                       "Blocked",
		Version:                    "1.0.0",
		UpdateAvailable:            false,
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		InstalledPackageFamilyName: "Blocked.App_abc123",
		UpdateState:                string(StoreUpdateConflict),
	})
	app.inventory = Inventory{PackageLookup: PackageLookup{Packages: []Package{actionable, blocked}}}
	app.inventoryFetchedAt = time.Now()

	if !packageAllowedInBulkUpdate(actionable, UpdateOptions{}) {
		t.Fatal("bulk policy rejected actionable update-ID-only Store package")
	}
	packages, mode, err := app.updateJobPackagesContext(context.Background(), []string{actionable.Key}, UpdateOptions{})
	if err != nil || mode != updateJobModeSelected || len(packages) != 1 || packages[0].ExactTargetKind != exactTargetKindUpdateID {
		t.Fatalf("selected update did not use shared actionable Store policy: packages=%#v mode=%s err=%v", packages, mode, err)
	}
	if packageAllowedInBulkUpdate(blocked, UpdateOptions{}) {
		t.Fatal("bulk policy accepted conflicting Store package")
	}
	if _, _, err := app.updateJobPackagesContext(context.Background(), []string{blocked.Key}, UpdateOptions{}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("selected update should reject conflict with backend reason, err=%v", err)
	}
}

func TestStoreAssessmentBrowserSmokeStrings(t *testing.T) {
	surface := uiJS + "\n" + uiCSS
	for _, expected := range []string{
		"storeUpdateState",
		`if(pkg.stale){ return false; }`,
		"return !!pkg.can_update_now;",
		"Stale evidence",
		`? "Stale"`,
		"storeScanHealth",
		"store_scan_health",
		"latestStoreScanHealth",
		"renderStoreScanHealth",
		"Provider diagnostics",
		"Exact target unavailable",
		"Not authoritative",
		"authoritative",
		"updatesEmptyState",
		"No actionable updates available. Review Store scan health for diagnostics.",
		"state-badge",
		"store-rescan-button",
	} {
		if !strings.Contains(surface, expected) {
			t.Fatalf("UI assets missing %q", expected)
		}
	}
	for _, unexpected := range []string{
		`Available (stale)`,
		`Available (Stale)`,
		`stateLabel(state) + " (stale)"`,
	} {
		if strings.Contains(surface, unexpected) {
			t.Fatalf("UI assets should not render stale Store evidence as %q", unexpected)
		}
	}
}

func TestStoreStaleEvidenceHiddenFromPrimaryUpdateQueueAssets(t *testing.T) {
	start := strings.Index(uiJS, "function packageShouldAppearInUpdateQueueBeforeSessionSuppression(pkg){")
	if start < 0 {
		t.Fatal("packageShouldAppearInUpdateQueueBeforeSessionSuppression function not found")
	}
	end := strings.Index(uiJS[start:], "\n  function packageHiddenAfterSuccessfulUpdate")
	if end < 0 {
		t.Fatal("packageShouldAppearInUpdateQueueBeforeSessionSuppression function end not found")
	}
	body := uiJS[start : start+end]
	for _, expected := range []string{
		`if(pkg && pkg.manager === "store" && !storeAssessmentActive(pkg)){ return false; }`,
		`if(pkg.stale){ return false; }`,
		`return !!pkg.can_update_now || state === "conflict";`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("packageShouldAppearInUpdateQueueBeforeSessionSuppression should contain %q; body:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "|| !!pkg.stale") {
		t.Fatalf("stale Store evidence should not enter the primary update queue; body:\n%s", body)
	}
	if strings.Contains(body, `state === "unknown"`) {
		t.Fatalf("unknown Store evidence should not enter the primary update queue; body:\n%s", body)
	}
	if strings.Contains(body, `state === "pending"`) {
		t.Fatalf("queue-only pending Store evidence should not enter the primary update queue; body:\n%s", body)
	}
}

func TestWinRTPendingQueueDiagnosticsStayOutOfPrimaryQueue(t *testing.T) {
	response := transactionalPackagesResponse(t, []StoreCatalogProvider{
		pendingWinRTDiscoveryProvider("OpenAI.Codex_abc123"),
	}, false)
	got := findStorePackageByPFN(t, response.Packages, "OpenAI.Codex_abc123")
	if got.UpdateState != string(StoreUpdatePending) || got.UpdateAvailable || got.CanUpdateNow || got.UpdateSupported {
		t.Fatalf("pending Store queue evidence must stay non-actionable: %#v", got)
	}
	if response.StoreScanHealth.Counts["pending"] == 0 {
		t.Fatalf("expected pending Store diagnostic count: %#v", response.StoreScanHealth)
	}
	if response.StoreScanHealth.Healthy || response.StoreScanHealth.Authoritative {
		t.Fatalf("pending queue evidence should keep Store health diagnostic-only: %#v", response.StoreScanHealth)
	}
	if len(got.ProviderSummaries) == 0 {
		t.Fatalf("expected per-package provider diagnostics for pending queue evidence: %#v", got)
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
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	userSID := "S-1-5-21-transactional-api"
	pfn := "OpenAI.Codex_abc123"
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()
	restoreNow := replaceStoreScanNow(time.Date(2026, 6, 21, 12, 0, 3, 0, time.UTC))
	defer restoreNow()
	store, err := openDefaultStoreScanRepository()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) == 2 && providerIDForTest(providers[0]) == "catalog-positive" && providerIDForTest(providers[1]) == "catalog-failed" {
		runTestPipelineWithProviders(t, store, userSID, pfn, []StoreCatalogProvider{providers[0]}, "transactional-positive-scan")
		runTestPipelineWithProviders(t, store, userSID, pfn, []StoreCatalogProvider{providers[1]}, "transactional-z-incomplete-scan")
	} else {
		runTestPipelineWithProviders(t, store, userSID, pfn, providers)
	}
	_ = store.Close()

	app := testSessionApp()
	app.inventory = Inventory{PackageLookup: PackageLookup{Packages: []Package{transactionalStoreAPIPackage(pfn)}}}
	app.inventoryFetchedAt = time.Now()
	app.inventoryLoading = loading
	return requestPackages(t, app)
}

func runTestPipelineWithProviders(t *testing.T, store StoreScanRepository, userSID, pfn string, providers []StoreCatalogProvider, scanID ...string) StoreScanResult {
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

func pendingWinRTDiscoveryProvider(pfn string) StoreCatalogProvider {
	return fakeCatalogProvider{id: storeWinRTDiscoveryProviderID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID, Name: "WinRT Store update discovery", Backend: backendWinRT},
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.CompletedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{{
				Provider:         StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID, Name: "WinRT Store update discovery", Backend: backendWinRT},
				Health:           StoreProviderHealthy,
				Kind:             StoreObservationPendingUpdate,
				Identity:         identity,
				ScanID:           scan.ScanID,
				ObservedAt:       scan.CompletedAt,
				InstalledVersion: "1.0.0",
				Diagnostics:      "WinRT Store queue reported a pending package state. state=paused",
			}},
		}
	}}
}
