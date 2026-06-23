package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreScanPipelineInterruptedTransactionKeepsPreviousGeneration(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-pipeline"
	pfn := "OpenAI.Codex_abc123"
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	pipeline.NewScanID = func(time.Time) string { return "scan-interrupted" }
	pipeline.BeforeCommit = func(context.Context, storeScanPersistInput) error {
		return errors.New("boom before commit")
	}
	if _, err := pipeline.Run(context.Background()); err == nil {
		t.Fatal("expected interrupted scan error")
	}
	if assessments, err := store.PublishedAssessments(context.Background(), userSID); err != nil || len(assessments) != 0 {
		t.Fatalf("interrupted transaction published assessments: assessments=%#v err=%v", assessments, err)
	}
}

func TestStoreScanDatabaseMigrationAndJSONStateImport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	state := defaultState()
	state.Theme = "light"
	state.AutoUpdateGlobal = true
	state.StoreUpdateAssessmentCache[storeAssessmentCacheKey("S-1-5-21-json", "OpenAI.Codex_abc123")] = StoreUpdateAssessmentCacheEntry{
		UserSID:                    "S-1-5-21-json",
		PackageFamilyName:          "OpenAI.Codex_abc123",
		ScanID:                     "json-scan",
		State:                      string(StoreUpdateAvailable),
		Reason:                     "json positive",
		ObservedAt:                 "2026-06-21T10:00:00Z",
		InstalledVersion:           "1.0.0",
		OfferedVersion:             "1.1.0",
		StoreProductID:             "9NCODEX",
		Applicability:              "applicable",
		ExactActionTargetAvailable: true,
	}
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}
	store, err := openStoreScanStore(filepath.Join(dir, "store-scans.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeScanSchemaVersion {
		t.Fatalf("schema version=%d, want %d", version, storeScanSchemaVersion)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM update_assessments WHERE scan_id = 'json-scan' AND package_family_name = 'OpenAI.Codex_abc123'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected JSON assessment cache migration, count=%d", count)
	}
	var providerVersionColumns int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('provider_runs') WHERE name = 'provider_version'`).Scan(&providerVersionColumns); err != nil {
		t.Fatal(err)
	}
	if providerVersionColumns != 1 {
		t.Fatal("provider_runs migration did not add provider_version column")
	}
	loaded := loadState()
	if loaded.Theme != "light" || !loaded.AutoUpdateGlobal {
		t.Fatalf("JSON migration altered unrelated settings: %#v", loaded)
	}
}

func TestProviderVersionMapUsesActualRunVersions(t *testing.T) {
	runs := []StoreCatalogProviderRun{
		{Provider: StoreProviderIdentity{ID: "store-cli-exact"}, Version: "v22605.1401.12.0"},
		{Provider: StoreProviderIdentity{ID: "winget-msstore-exact"}, Version: "v1.28.240"},
		{Provider: StoreProviderIdentity{ID: "fake-no-version"}},
	}
	got := providerVersionMap(runs)
	if got["store-cli-exact"] != "v22605.1401.12.0" || got["winget-msstore-exact"] != "v1.28.240" {
		t.Fatalf("provider versions not preserved: %#v", got)
	}
	if _, ok := got["fake-no-version"]; ok {
		t.Fatalf("empty provider version should not be advertised: %#v", got)
	}
}

func TestStoreScanPipelineRecordsSystemContext(t *testing.T) {
	restoreContext := replaceStoreScanSystemContext(storeScanSystemContext{
		WindowsVersion: "Windows 11 24H2",
		WindowsBuild:   "10.0.26200.8655",
		Architecture:   "arm64",
	})
	defer restoreContext()

	store := newTestStoreScanStore(t)
	result := runTestPipeline(t, store, "S-1-5-21-context", "OpenAI.Codex_abc123", negativeProvider("OpenAI.Codex_abc123", "1.0.0"))

	if result.Scan.WindowsVersion != "Windows 11 24H2" || result.Scan.WindowsBuild != "10.0.26200.8655" || result.Scan.Architecture != "arm64" {
		t.Fatalf("scan context was not recorded: %#v", result.Scan)
	}
	persisted, ok, err := store.LatestPublishedScan(context.Background(), "S-1-5-21-context")
	if err != nil || !ok {
		t.Fatalf("latest scan not persisted: scan=%#v ok=%t err=%v", persisted, ok, err)
	}
	if persisted.WindowsVersion != "Windows 11 24H2" || persisted.WindowsBuild != "10.0.26200.8655" || persisted.Architecture != "arm64" {
		t.Fatalf("persisted scan context was not recorded: %#v", persisted)
	}
}

func TestStoreScanCorruptDatabaseRecoveryPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store-scans.sqlite")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := openStoreScanStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	matches, err := filepath.Glob(path + ".corrupt.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one corrupt backup, got %#v", matches)
	}
	var version int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeScanSchemaVersion {
		t.Fatalf("recovered schema version=%d", version)
	}
}

func TestStoreScanRetentionPrunesOldGenerationsForUser(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-retention"
	pfn := "OpenAI.Codex_abc123"
	base := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	for index := 0; index < storeScanRetentionRunsUser+5; index++ {
		scan := StoreScanGeneration{
			ScanID:           fmt.Sprintf("scan-retention-%02d", index),
			UserSID:          userSID,
			StartedAt:        base.Add(time.Duration(index) * time.Second),
			CompletedAt:      base.Add(time.Duration(index)*time.Second + time.Millisecond),
			CompletionStatus: StoreScanCompleted,
		}
		identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
		assessment := StorePublishedAssessment{
			StoreUpdateAssessment: StoreUpdateAssessment{
				State:    StoreUpdateCurrent,
				Identity: identity,
				ScanID:   scan.ScanID,
				Reason:   "authoritative negative",
			},
			ObservedAt: scan.CompletedAt,
		}
		if _, err := store.PersistScan(context.Background(), storeScanPersistInput{
			Scan:        scan,
			Inventory:   testStoreInventory(scan, pfn, "1.0.0"),
			Assessments: []StorePublishedAssessment{assessment},
			Publish:     true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM scan_runs WHERE user_sid = ?`, userSID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != storeScanRetentionRunsUser {
		t.Fatalf("scan retention kept %d rows, want %d", count, storeScanRetentionRunsUser)
	}
	var oldestCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM scan_runs WHERE scan_id = ?`, "scan-retention-00").Scan(&oldestCount); err != nil {
		t.Fatal(err)
	}
	if oldestCount != 0 {
		t.Fatal("oldest scan generation was not pruned")
	}
	scan, ok, err := store.LatestPublishedScan(context.Background(), userSID)
	if err != nil || !ok || scan.ScanID != fmt.Sprintf("scan-retention-%02d", storeScanRetentionRunsUser+4) {
		t.Fatalf("latest published scan not preserved: scan=%#v ok=%v err=%v", scan, ok, err)
	}
}

func TestStoreScanRejectsTwoSimultaneousRequests(t *testing.T) {
	store := newTestStoreScanStore(t)
	block := make(chan struct{})
	started := make(chan struct{})
	userSID := "S-1-5-21-concurrent"
	pipeline := newTestStoreScanPipeline(store, userSID, "OpenAI.Codex_abc123", positiveProvider("OpenAI.Codex_abc123", "1.0.0", "1.1.0"))
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		close(started)
		<-block
		return testStoreInventory(scan, "OpenAI.Codex_abc123", "1.0.0"), CommandResult{OK: true, Command: "inventory"}
	}}
	done := make(chan error)
	go func() {
		_, err := pipeline.Run(context.Background())
		done <- err
	}()
	<-started
	if _, err := pipeline.Run(context.Background()); !errors.Is(err, errStoreScanAlreadyRunning) {
		t.Fatalf("second scan error=%v, want errStoreScanAlreadyRunning", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("first scan failed: %v", err)
	}
}

func TestStoreScanOlderCompletionDoesNotPublishOverNewerScan(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-order"
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pfn := "OpenAI.Codex_abc123"
	base := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	older := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	older.Now = fixedPipelineTimes(base, base.Add(3*time.Second))
	older.NewScanID = func(time.Time) string { return "older-scan" }
	newer := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	newer.Now = fixedPipelineTimes(base.Add(time.Second), base.Add(2*time.Second))
	newer.NewScanID = func(time.Time) string { return "newer-scan" }
	if result, err := newer.Run(context.Background()); err != nil || !result.Published {
		t.Fatalf("newer scan publish failed: result=%#v err=%v", result, err)
	}
	if result, err := older.Run(context.Background()); err != nil || result.Published {
		t.Fatalf("older stale scan should persist but not publish: result=%#v err=%v", result, err)
	}
	scan, ok, err := store.LatestPublishedScan(context.Background(), userSID)
	if err != nil || !ok || scan.ScanID != "newer-scan" {
		t.Fatalf("latest published scan=%#v ok=%v err=%v", scan, ok, err)
	}
}

func TestStoreScanCrossUserEvidenceBecomesUnknown(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-user-a"
	pfn := "OpenAI.Codex_abc123"
	provider := fakeCatalogProvider{id: "wrong-user", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		other := StoreInstalledIdentity{UserSID: "S-1-5-21-user-b", PackageFamilyName: pfn}
		target := exactStoreTarget(other, StoreProviderIdentity{ID: "wrong-user"})
		return StoreCatalogProviderRun{
			Provider:     StoreProviderIdentity{ID: "wrong-user"},
			StartedAt:    scan.StartedAt,
			CompletedAt:  scan.StartedAt.Add(time.Second),
			Health:       StoreProviderHealthy,
			Observations: []StoreProviderObservation{storeObservation(other, scan, StoreProviderIdentity{ID: "wrong-user"}, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target)},
		}
	}}
	result := runTestPipeline(t, store, userSID, pfn, provider)
	if got := result.Assessments[0]; got.State != StoreUpdateUnknown {
		t.Fatalf("wrong-user evidence state=%s, want unknown; assessment=%#v", got.State, got)
	}
	if result.ProviderRuns[1].Health != StoreProviderFailed {
		t.Fatalf("wrong-user provider run health=%s", result.ProviderRuns[1].Health)
	}
}

func TestStoreScanProviderPartialFailureIsUnknown(t *testing.T) {
	store := newTestStoreScanStore(t)
	result := runTestPipeline(t, store, "S-1-5-21-partial", "OpenAI.Codex_abc123", failingProvider("catalog unavailable"))
	if got := result.Assessments[0]; got.State != StoreUpdateUnknown || got.Stale {
		t.Fatalf("partial provider assessment=%#v", got)
	}
}

func TestStoreScanOptionalProviderFailureDoesNotAllowCurrent(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-optional-failure"
	pfn := "OpenAI.Codex_abc123"
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: storeCLIUpdatesProviderID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{
			Provider:    StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI},
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "aggregate update output mentioned updates without exact PFN/Product ID associations",
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown {
		t.Fatalf("optional provider failure without package rows produced %s, want unknown; assessment=%#v", got.State, got)
	}
	if !strings.Contains(got.Reason, storeCLIUpdatesProviderID) {
		t.Fatalf("expected provider failure reason to cite %s, got %#v", storeCLIUpdatesProviderID, got)
	}
}

func TestStoreScanRequiredProviderPackageFailurePreventsOtherCurrent(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-required-package-failure"
	healthyPFN := "OpenAI.Codex_abc123"
	failedPFN := "Vendor.Broken_abc123"
	providerID := StoreProviderIdentity{ID: "required-exact", Name: "Required exact provider"}
	pipeline := newTestStoreScanPipeline(store, userSID, healthyPFN, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		healthyIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: healthyPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(healthyIdentity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				storeObservation(failedIdentity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, healthyPFN, "1.0.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	for _, assessment := range result.Assessments {
		if assessment.State == StoreUpdateCurrent {
			t.Fatalf("incomplete required provider scan emitted current assessment: %#v", assessment)
		}
	}
}

func TestStoreScanOptionalExactFailureDoesNotBlockUnrelatedCurrentWithAggregateCoverage(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-optional-exact-unrelated"
	currentPFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	failedPFN := "Vendor.Broken_abc123"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, currentPFN, fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		currentIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: currentPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    exactProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(currentIdentity, scan, exactProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				storeObservation(failedIdentity, scan, exactProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: currentIdentity,
				ProductID:         "9N4D0MSMP0PT",
				Provider:          exactProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		currentIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: currentPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(currentIdentity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				storeObservation(failedIdentity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, currentPFN, "1.2.20.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanCompleted {
		t.Fatalf("scan status=%s, want completed; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	byPFN := map[string]StorePublishedAssessment{}
	for _, assessment := range result.Assessments {
		byPFN[assessment.Identity.PackageFamilyName] = assessment
	}
	if got := byPFN[currentPFN]; got.State != StoreUpdateCurrent || got.StoreProductID != "9N4D0MSMP0PT" {
		t.Fatalf("healthy exact negative plus aggregate coverage should be current: %#v", got)
	}
	if got := byPFN[failedPFN]; got.State != StoreUpdateUnknown || !strings.Contains(got.Reason, "store-cli-exact") {
		t.Fatalf("package with incomplete exact evidence should remain unknown: %#v", got)
	}
}

func TestStoreScanFreshPositiveSurvivesUnrelatedIncompletePackage(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-positive-unrelated-failure"
	updatePFN := "OpenAI.Codex_abc123"
	failedPFN := "Vendor.Broken_abc123"
	providerID := StoreProviderIdentity{ID: "required-exact-positive", Name: "Required exact provider"}
	pipeline := newTestStoreScanPipeline(store, userSID, updatePFN, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		updateIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: updatePFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		target := &ExactStoreUpdateTarget{Identity: updateIdentity, Provider: providerID, ProductID: "9NCODEX", Verified: true, VerifiedBy: providerID.ID, VerifiedAt: scan.StartedAt.Add(time.Second)}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(updateIdentity, scan, providerID, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
				storeObservation(failedIdentity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, updatePFN, "1.0.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	byPFN := map[string]StorePublishedAssessment{}
	for _, assessment := range result.Assessments {
		byPFN[assessment.Identity.PackageFamilyName] = assessment
	}
	if got := byPFN[updatePFN]; got.State != StoreUpdateAvailable || got.Stale || got.StoreProductID != "9NCODEX" || !got.ExactActionTargetAvailable {
		t.Fatalf("fresh exact positive should stay available despite unrelated package failure: %#v", got)
	}
	if got := byPFN[failedPFN]; got.State != StoreUpdateUnknown || got.Stale {
		t.Fatalf("failed package should remain unknown: %#v", got)
	}
}

func TestStoreScanOptionalExactPositiveSurvivesRequiredProviderIncompleteForSamePackage(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-optional-positive-required-incomplete"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			target := &ExactStoreUpdateTarget{Identity: identity, Provider: exactProvider, ProductID: productID, UpdateID: pfn, Verified: true, VerifiedBy: exactProvider.ID, VerifiedAt: scan.StartedAt.Add(time.Second)}
			return StoreCatalogProviderRun{
				Provider:    exactProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderHealthy,
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, exactProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.2.13.0", "1.2.20.0", target),
				},
				Mappings: []VerifiedStoreIdentityMapping{{
					InstalledIdentity: identity,
					ProductID:         productID,
					Provider:          exactProvider,
					ScanID:            scan.ScanID,
					VerifiedAt:        target.VerifiedAt,
					Evidence:          "store show exact PFN/Product ID association",
				}},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "store updates timed out",
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, aggregateProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.13.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || got.UpdateID != pfn || !got.ExactActionTargetAvailable {
		t.Fatalf("optional exact positive did not survive required incomplete provider: %#v", got)
	}
	if got.Stale {
		t.Fatalf("fresh exact positive should not be marked stale: %#v", got)
	}
}

func TestStoreScanWingetExactPositiveSurvivesRequiredProviderIncompleteForSamePackage(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-winget-positive-required-incomplete"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			return StoreCatalogProviderRun{
				Provider:    aggregateProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderIncomplete,
				Error:       "store updates timed out",
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, aggregateProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.13.0", "", nil),
				},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: wingetProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: wingetProvider, ProductID: productID, UpdateID: pfn, Verified: true, VerifiedBy: wingetProvider.ID, VerifiedAt: scan.StartedAt.Add(2 * time.Second)}
		return StoreCatalogProviderRun{
			Provider:    wingetProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.2.13.0", "1.2.20.0", target),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          wingetProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        target.VerifiedAt,
				Evidence:          "winget msstore exact PFN/Product ID association",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || got.UpdateID != pfn || !got.ExactActionTargetAvailable {
		t.Fatalf("WinGet exact positive did not survive required incomplete provider: %#v", got)
	}
	if got.Stale {
		t.Fatalf("fresh exact positive should not be marked stale: %#v", got)
	}
}

func TestStoreScanWingetNonExactPositivePreventsCurrent(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-winget-unresolved-positive"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			return StoreCatalogProviderRun{
				Provider:    aggregateProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderHealthy,
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: wingetProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{
			Provider:    wingetProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "ignored 1 WinGet msstore update row(s) without exact installed PFN association",
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown || !strings.Contains(got.Reason, wingetMSStoreExactProviderID) {
		t.Fatalf("unresolved WinGet Store positive must prevent current: %#v", got)
	}
}

func TestStoreScanPublishesVerifiedProductIDForAuthoritativeNegative(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-current-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    exactProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, exactProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          exactProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateCurrent || got.StoreProductID != productID {
		t.Fatalf("verified Product ID should be published for current assessment: %#v", got)
	}
	if got.ExactActionTargetAvailable {
		t.Fatalf("current assessment must not become updateable just because Product ID is known: %#v", got)
	}
	persisted, err := store.PublishedAssessments(context.Background(), userSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].StoreProductID != productID || persisted[0].ExactActionTargetAvailable {
		t.Fatalf("persisted assessment lost verified Product ID or changed target availability: %#v", persisted)
	}
}

func TestStoreScanPublishesVerifiedProductIDForUnknownIncompleteAssessment(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-unknown-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	providerID := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          providerID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association before update-state failure",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown || got.StoreProductID != productID {
		t.Fatalf("verified Product ID should be published for unknown assessment: %#v", got)
	}
	if got.ExactActionTargetAvailable {
		t.Fatalf("unknown assessment must not become updateable just because Product ID is known: %#v", got)
	}
}

func TestStoreScanDoesNotPublishConflictingVerifiedProductIDs(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-conflicting-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	providerID := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	otherProviderID := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         "9N4D0MSMP0PT",
				Provider:          providerID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: otherProviderID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(2 * time.Second)
		return StoreCatalogProviderRun{
			Provider:    otherProviderID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         "9NCONFLICT",
				Provider:          otherProviderID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "conflicting exact PFN/Product ID association",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.StoreProductID != "" {
		t.Fatalf("conflicting verified Product IDs must not be published: %#v", got)
	}
}

func TestStoreScanPositiveHysteresisRetainsUpdateOnIncompleteScan(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-hysteresis"
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}
	second := runTestPipeline(t, store, userSID, pfn, failingProvider("catalog timeout"))
	if got := second.Assessments[0]; got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("hysteresis assessment=%#v", got)
	}
}

func TestStoreScanHealthyRetractionStopsPositiveHysteresis(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-retraction"
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-retraction", Name: "Catalog retraction"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateCurrent || got.Stale {
		t.Fatalf("complete healthy retraction should clear stale positive as current: %#v", got)
	}
}

func TestStoreScanIncompleteRetractionDoesNotClearPreviousPositive(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-incomplete-retraction"
	pfn := "OpenAI.Codex_abc123"
	otherPFN := "Vendor.Broken_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-retraction", Name: "Catalog retraction"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		other := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: otherPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				storeObservation(other, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	second.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, pfn, "1.0.0")
		broken := testStoreInventory(scan, otherPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("incomplete retraction should retain stale positive: %#v", got)
	}
}

func TestStoreScanIncompleteInapplicableDoesNotClearPreviousPositive(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-incomplete-inapplicable"
	pfn := "OpenAI.Codex_abc123"
	otherPFN := "Vendor.Broken_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-inapplicable", Name: "Catalog inapplicable"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		other := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: otherPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationNewerCatalogNoApplicableInstaller, "1.0.0", "1.2.0", nil),
				storeObservation(other, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	second.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, pfn, "1.0.0")
		broken := testStoreInventory(scan, otherPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("incomplete inapplicable evidence should retain stale positive: %#v", got)
	}
}

func TestStoreScanConflictResolution(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-conflict"
	pfn := "OpenAI.Codex_abc123"
	result := runTestPipeline(t, store, userSID, pfn, positiveAndNegativeProvider(pfn))
	if got := result.Assessments[0]; got.State != StoreUpdateConflict {
		t.Fatalf("conflict assessment=%#v", got)
	}
}

func TestStoreScanCancellationDoesNotPublish(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-cancel"
	pfn := "OpenAI.Codex_abc123"
	ctx, cancel := context.WithCancel(context.Background())
	provider := fakeCatalogProvider{id: "blocking", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		cancel()
		<-ctx.Done()
		return StoreCatalogProviderRun{Provider: StoreProviderIdentity{ID: "blocking"}, Health: StoreProviderFailed, Error: ctx.Err().Error()}
	}}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, provider)
	if _, err := pipeline.Run(ctx); err == nil {
		t.Fatal("expected cancelled scan to fail before publish")
	}
	if assessments, err := store.PublishedAssessments(context.Background(), userSID); err != nil || len(assessments) != 0 {
		t.Fatalf("cancelled scan published assessments=%#v err=%v", assessments, err)
	}
}

func TestConfiguredStoreProviderTimeoutFromEnvironment(t *testing.T) {
	t.Setenv(storeProviderTimeoutEnv, "17")
	if got := configuredStoreProviderTimeout(); got != 17*time.Second {
		t.Fatalf("timeout=%s, want 17s", got)
	}
	t.Setenv(storeProviderTimeoutEnv, "0")
	if got := configuredStoreProviderTimeout(); got != defaultStoreProviderTimeout {
		t.Fatalf("zero timeout=%s, want default %s", got, defaultStoreProviderTimeout)
	}
	t.Setenv(storeProviderTimeoutEnv, "not-a-number")
	if got := configuredStoreProviderTimeout(); got != defaultStoreProviderTimeout {
		t.Fatalf("invalid timeout=%s, want default %s", got, defaultStoreProviderTimeout)
	}
}

func TestStoreScanPreviousGenerationFallbackOnFatalInventoryFailure(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-fallback"
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if !first.Published {
		t.Fatal("initial scan did not publish")
	}
	failed := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	failed.NewScanID = func(time.Time) string { return "failed-inventory-scan" }
	failed.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := StorePackagedAppInventory{Scan: scan, Partial: true, Errors: []string{"inventory failed"}}
		return inventory, CommandResult{Command: "inventory", Code: 1, Stderr: "inventory failed"}
	}}
	result, err := failed.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Published {
		t.Fatalf("fatal inventory scan should not publish: %#v", result)
	}
	assessments, err := store.PublishedAssessments(context.Background(), userSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assessments) != 1 || assessments[0].State != StoreUpdateAvailable || assessments[0].ScanID != first.Scan.ScanID {
		t.Fatalf("previous generation fallback broken: %#v", assessments)
	}
}

func TestStoreScanInapplicableAndUnresolvedIdentity(t *testing.T) {
	store := newTestStoreScanStore(t)
	userSID := "S-1-5-21-states"
	pfn := "OpenAI.Codex_abc123"
	inapplicable := runTestPipeline(t, store, userSID, pfn, inapplicableProvider(pfn))
	if inapplicable.Assessments[0].State != StoreUpdateInapplicable {
		t.Fatalf("inapplicable assessment=%#v", inapplicable.Assessments[0])
	}
	unresolved := runTestPipeline(t, store, userSID, pfn, positiveWithoutTargetProvider(pfn))
	if unresolved.Assessments[0].State != StoreUpdateUnknown || !strings.Contains(unresolved.Assessments[0].Reason, "no exact verified target") {
		t.Fatalf("unresolved target assessment=%#v", unresolved.Assessments[0])
	}
}

func TestStoreTransactionalScanFeatureFlagAndInventoryAdapter(t *testing.T) {
	if !storeTransactionalScanEnabled() {
		t.Fatal("transactional Store scan pipeline must be enabled by default after cutover")
	}
	t.Setenv(storeLegacyDetectorRollbackFlag, "1")
	if storeTransactionalScanEnabled() {
		t.Fatal("legacy rollback flag should disable transactional Store scan")
	}
	t.Setenv(storeLegacyDetectorRollbackFlag, "")
	t.Setenv(storeCutoverDisableScanFlag, "1")
	if storeTransactionalScanEnabled() {
		t.Fatal("explicit disable flag should disable transactional Store scan")
	}
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: false,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "scan-adapter",
			Reason:           "fresh exact positive update evidence",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
		},
		ObservedAt:                 time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		StoreProductID:             "9NCODEX",
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, []StorePublishedAssessment{assessment}, nil, nil)
	got := adapted.Packages[0]
	if got.UpdateState != string(StoreUpdateAvailable) || !got.UpdateAvailable || !got.ExactActionTargetAvailable || got.ID != pfn || got.StoreProductID != "9NCODEX" {
		t.Fatalf("published assessment was not adapted into package response: %#v", got)
	}
}

func TestPublishedStoreAssessmentStalePositiveIsDiagnosticOnly(t *testing.T) {
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: true,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "previous-scan",
			Reason:           "retained previous positive update because the latest scan was incomplete",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
		},
		ObservedAt:                 time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		Stale:                      true,
		StoreProductID:             "9NCODEX",
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, []StorePublishedAssessment{assessment}, nil, nil)
	got := adapted.Packages[0]
	if got.UpdateState != string(StoreUpdateAvailable) || !got.Stale {
		t.Fatalf("stale assessment state was not preserved for diagnostics: %#v", got)
	}
	if got.UpdateAvailable || got.UpdateSupported || got.AvailableVersion != "" || got.OfferedVersion != "1.1.0" {
		t.Fatalf("stale assessment must not be exposed as an available update: %#v", got)
	}
}

func TestPublishedStoreAssessmentUsesFriendlyPFNPresentationFallback(t *testing.T) {
	state := defaultState()
	pkg := packageFromPublishedStoreAssessment(state, StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateCurrent,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: "19568ShareX.ShareX_egrzcvs15399j"},
			ScanID:           "scan-friendly-name",
			Reason:           "authoritative negative",
			InstalledVersion: "20.2.0.0",
		},
		ObservedAt: time.Date(2026, 6, 22, 17, 0, 0, 0, time.UTC),
	}, StorePackagedAppFamily{}, nil)

	if pkg.Name != "ShareX" {
		t.Fatalf("package assessment name = %q, want ShareX", pkg.Name)
	}
	if pkg.InstalledPackageFamilyName != "19568ShareX.ShareX_egrzcvs15399j" {
		t.Fatalf("canonical PFN was not preserved: %#v", pkg)
	}
}

func newTestStoreScanStore(t *testing.T) *StoreScanStore {
	t.Helper()
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	store, err := openStoreScanStore(filepath.Join(t.TempDir(), "store-scans.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestStoreScanPipeline(store *StoreScanStore, userSID, pfn string, provider StoreCatalogProvider) *StoreScanPipeline {
	return &StoreScanPipeline{
		Store:             store,
		InventoryProvider: fakeInventoryProvider{inventory: testStoreInventory(StoreScanGeneration{ScanID: "placeholder", UserSID: userSID, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanCompleted}, pfn, "1.0.0")},
		CatalogProviders:  []StoreCatalogProvider{provider},
		ProviderTimeout:   500 * time.Millisecond,
		Now:               fixedPipelineTimes(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 2, 0, time.UTC)),
		NewScanID:         func(now time.Time) string { return "scan-" + strings.ReplaceAll(pfn, ".", "-") + fmtTimeForID(now) },
	}
}

func runTestPipeline(t *testing.T, store *StoreScanStore, userSID, pfn string, provider StoreCatalogProvider) StoreScanResult {
	t.Helper()
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, provider)
	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type fakeInventoryProvider struct {
	inventory StorePackagedAppInventory
	result    CommandResult
	fn        func(context.Context, StoreScanGeneration) (StorePackagedAppInventory, CommandResult)
}

func (provider fakeInventoryProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	if provider.fn != nil {
		return provider.fn(ctx, scan)
	}
	inventory := provider.inventory
	inventory.Scan = scan
	inventory.Scan.CompletedAt = scan.StartedAt.Add(time.Second)
	inventory.Scan.CompletionStatus = StoreScanCompleted
	for index := range inventory.Records {
		inventory.Records[index].UserSID = scan.UserSID
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	result := provider.result
	if result.Command == "" {
		result = CommandResult{OK: true, Command: "fake inventory"}
	}
	return inventory, result
}

type fakeCatalogProvider struct {
	id string
	fn func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) StoreCatalogProviderRun
}

func (provider fakeCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: provider.id, Name: provider.id, Backend: "fake"}
}

func (provider fakeCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	return provider.fn(ctx, scan, families)
}

func testStoreInventory(scan StoreScanGeneration, pfn, version string) StorePackagedAppInventory {
	if scan.CompletedAt.IsZero() {
		scan.CompletedAt = scan.StartedAt.Add(time.Second)
	}
	scan.CompletionStatus = StoreScanCompleted
	record := StorePackagedAppRecord{
		UserSID:               scan.UserSID,
		PackageFamilyName:     pfn,
		PackageFullName:       pfn + "_" + version + "_x64__abc123",
		IdentityName:          strings.Split(pfn, "_")[0],
		Version:               parseTestStoreVersion(version),
		ProcessorArchitecture: "x64",
		PackageType:           "Main",
		Classification:        storePackageClassMain,
		DisplayName:           "Codex",
		Status:                StorePackageStatus{OK: true},
	}
	inventory := StorePackagedAppInventory{
		Scan:    scan,
		Records: []StorePackagedAppRecord{record},
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	return inventory
}

func parseTestStoreVersion(value string) StorePackageVersion {
	parts := versionParts(value)
	version := StorePackageVersion{}
	if len(parts) > 0 {
		version.Major = uint16(parts[0])
	}
	if len(parts) > 1 {
		version.Minor = uint16(parts[1])
	}
	if len(parts) > 2 {
		version.Build = uint16(parts[2])
	}
	if len(parts) > 3 {
		version.Revision = uint16(parts[3])
	}
	return version
}

func positiveProvider(pfn, installed, available string) StoreCatalogProvider {
	id := "catalog-positive"
	provider := StoreProviderIdentity{ID: id, Name: id, Backend: "fake"}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: provider, ProductID: "9NCODEX", Verified: true, VerifiedBy: id, VerifiedAt: scan.StartedAt.Add(time.Second)}
		mapping := VerifiedStoreIdentityMapping{InstalledIdentity: identity, ProductID: "9NCODEX", Provider: provider, ScanID: scan.ScanID, VerifiedAt: target.VerifiedAt, Evidence: "fake exact PFN/product mapping"}
		return StoreCatalogProviderRun{
			Provider:    provider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, installed, available, target),
			},
			Mappings: []VerifiedStoreIdentityMapping{mapping},
		}
	}}
}

func negativeProvider(pfn, installed string) StoreCatalogProvider {
	id := "catalog-negative"
	provider := StoreProviderIdentity{ID: id, Name: id, Backend: "fake"}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, installed, "", nil),
		}}
	}}
}

func failingProvider(message string) StoreCatalogProvider {
	id := "catalog-failed"
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{Provider: StoreProviderIdentity{ID: id}, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderFailed, Error: message}
	}}
}

func positiveAndNegativeProvider(pfn string) StoreCatalogProvider {
	id := "catalog-conflict"
	provider := StoreProviderIdentity{ID: id, Name: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: provider, ProductID: "9NCODEX", Verified: true, VerifiedBy: id, VerifiedAt: scan.StartedAt.Add(time.Second)}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
		}}
	}}
}

func inapplicableProvider(pfn string) StoreCatalogProvider {
	id := "catalog-inapplicable"
	provider := StoreProviderIdentity{ID: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationNewerCatalogNoApplicableInstaller, "1.0.0", "1.1.0", nil),
		}}
	}}
}

func positiveWithoutTargetProvider(pfn string) StoreCatalogProvider {
	id := "catalog-no-target"
	provider := StoreProviderIdentity{ID: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", nil),
		}}
	}}
}

func replaceStoreScanSID(sid string) func() {
	old := storeScanCurrentUserSID
	storeScanCurrentUserSID = func() (string, error) { return sid, nil }
	return func() { storeScanCurrentUserSID = old }
}

func replaceStoreScanSystemContext(context storeScanSystemContext) func() {
	old := storeScanSystemContextProvider
	storeScanSystemContextProvider = func() storeScanSystemContext { return context }
	return func() { storeScanSystemContextProvider = old }
}

func fixedPipelineTimes(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Now().UTC()
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func fmtTimeForID(value time.Time) string {
	return value.UTC().Format("150405")
}
