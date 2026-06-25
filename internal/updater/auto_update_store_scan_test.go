package updater

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScheduledAutoUpdateSkipsStoreEvidenceThatPredatesRunAfterScanFailure(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	repository := newTestStoreScanRepository(t)
	completedBeforeRun := time.Now().UTC().Add(-time.Minute)
	persistStoreAutoUpdatePositiveSnapshot(t, repository, identity, "scan-before-scheduled-run", completedBeforeRun)

	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{canonicalStoreAutoUpdateKey(userSID, pfn): true}
	store := newMemoryStateStore(state)

	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()
	oldOpen := openStoreTransactionalStoreForInventory
	openStoreTransactionalStoreForInventory = func() (StoreScanRepository, error) {
		return repository, nil
	}
	defer func() { openStoreTransactionalStoreForInventory = oldOpen }()

	oldScan := runStoreTransactionalScanForInventory
	runStoreTransactionalScanForInventory = func(context.Context) (StoreScanResult, error) {
		return StoreScanResult{}, errors.New("fresh Store scan failed")
	}
	defer func() { runStoreTransactionalScanForInventory = oldScan }()

	oldGetter := inventoryGetter
	inventoryGetter = func(context.Context) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:                        packageKey(managerStore, pfn),
			Manager:                    managerStore,
			ID:                         pfn,
			Name:                       "Codex",
			Version:                    "1.0.0",
			Installed:                  true,
			Source:                     sourceNativeAppX,
			Match:                      pfn + "_1.0.0.0_x64__abc123",
			ActionBackend:              backendAppXInventory,
			UpdateSupported:            false,
			InstalledPackageFamilyName: pfn,
			ExactIdentityAvailable:     true,
		}}}}
	}
	defer func() { inventoryGetter = oldGetter }()

	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(string) bool { return true }
	defer func() { packageActionManagerAvailable = oldAvailable }()

	oldExecutor := storeExactUpdateExecutor
	var executed bool
	storeExactUpdateExecutor = testStoreExactExecutor(
		fakeStoreExactRunner{
			result: CommandResult{Command: "fake store update", Code: 1, Stderr: "Store update should not execute from pre-run evidence"},
			after:  func() { executed = true },
		},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{{
			Identity:        identity,
			PackageFullName: pfn + "_1.0.0.0_x64__abc123",
			Version:         "1.0.0",
			Healthy:         true,
			Exists:          true,
			ObservedAt:      completedBeforeRun,
		}}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	defer func() { storeExactUpdateExecutor = oldExecutor }()

	results := runAutoUpdateWithStore(context.Background(), store)
	if executed || len(results) != 0 {
		t.Fatalf("scheduled Store auto-update used pre-run evidence after a failed fresh scan: executed=%t results=%#v", executed, results)
	}
	updated, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastAutoUpdateSummary == nil || len(updated.LastAutoUpdateSummary.SkippedPackages) != 1 {
		t.Fatalf("expected skipped Store package summary, got %#v", updated.LastAutoUpdateSummary)
	}
}

func TestScheduledAutoUpdateRunsStorePackageAfterOwnedFreshScanPublishes(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	repository := newTestStoreScanRepository(t)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{canonicalStoreAutoUpdateKey(userSID, pfn): true}
	store := newMemoryStateStore(state)

	executed := installScheduledStoreAutoUpdateHooks(t, userSID, pfn, repository, func(context.Context) (StoreScanResult, error) {
		completedAt := time.Now().UTC().Add(time.Second)
		scanID := "fresh-owned-scan"
		persistStoreAutoUpdatePositiveSnapshot(t, repository, identity, scanID, completedAt)
		return StoreScanResult{
			Published: true,
			Scan: StoreScanGeneration{
				ScanID:           scanID,
				UserSID:          userSID,
				StartedAt:        completedAt.Add(-time.Second),
				CompletedAt:      completedAt,
				CompletionStatus: StoreScanCompleted,
			},
		}, nil
	})

	results := runAutoUpdateWithStore(context.Background(), store)
	if !*executed || len(results) != 1 || results[0].Key != packageKey(managerStore, pfn) {
		t.Fatalf("scheduled Store auto-update should use its own fresh published scan: executed=%t results=%#v", *executed, results)
	}
	updated, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastAutoUpdateSummary == nil || !updated.LastAutoUpdateSummary.StoreScan.FreshGeneration || updated.LastAutoUpdateSummary.StoreScan.UsedGenerationID != "fresh-owned-scan" {
		t.Fatalf("fresh owned scan was not summarized correctly: %#v", updated.LastAutoUpdateSummary)
	}
}

func TestScheduledAutoUpdateWaitsForConcurrentFreshStoreScan(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	repository := newTestStoreScanRepository(t)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{canonicalStoreAutoUpdateKey(userSID, pfn): true}
	store := newMemoryStateStore(state)

	oldTimeout := scheduledStoreScanWaitTimeout
	oldPoll := scheduledStoreScanPollInterval
	scheduledStoreScanWaitTimeout = 750 * time.Millisecond
	scheduledStoreScanPollInterval = 10 * time.Millisecond
	defer func() {
		scheduledStoreScanWaitTimeout = oldTimeout
		scheduledStoreScanPollInterval = oldPoll
	}()

	scanBlocked := make(chan struct{})
	executed := installScheduledStoreAutoUpdateHooks(t, userSID, pfn, repository, func(context.Context) (StoreScanResult, error) {
		close(scanBlocked)
		return StoreScanResult{}, errStoreScanAlreadyRunning
	})
	go func() {
		<-scanBlocked
		time.Sleep(25 * time.Millisecond)
		persistStoreAutoUpdatePositiveSnapshot(t, repository, identity, "fresh-concurrent-scan", time.Now().UTC().Add(time.Second))
	}()

	results := runAutoUpdateWithStore(context.Background(), store)
	if !*executed || len(results) != 1 || results[0].Key != packageKey(managerStore, pfn) {
		t.Fatalf("scheduled Store auto-update should wait for concurrent fresh scan: executed=%t results=%#v", *executed, results)
	}
	updated, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastAutoUpdateSummary == nil || updated.LastAutoUpdateSummary.StoreScan.UsedGenerationID != "fresh-concurrent-scan" {
		t.Fatalf("concurrent scan generation was not summarized correctly: %#v", updated.LastAutoUpdateSummary)
	}
}

func TestScheduledAutoUpdateSkipsStorePackageWhenConcurrentScanTimesOut(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	repository := newTestStoreScanRepository(t)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{canonicalStoreAutoUpdateKey(userSID, pfn): true}
	store := newMemoryStateStore(state)

	oldTimeout := scheduledStoreScanWaitTimeout
	oldPoll := scheduledStoreScanPollInterval
	scheduledStoreScanWaitTimeout = 35 * time.Millisecond
	scheduledStoreScanPollInterval = 5 * time.Millisecond
	defer func() {
		scheduledStoreScanWaitTimeout = oldTimeout
		scheduledStoreScanPollInterval = oldPoll
	}()

	executed := installScheduledStoreAutoUpdateHooks(t, userSID, pfn, repository, func(context.Context) (StoreScanResult, error) {
		return StoreScanResult{}, errStoreScanAlreadyRunning
	})

	results := runAutoUpdateWithStore(context.Background(), store)
	if *executed || len(results) != 0 {
		t.Fatalf("scheduled Store auto-update should not run after scan wait timeout: executed=%t results=%#v", *executed, results)
	}
	updated, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastAutoUpdateSummary == nil || updated.LastAutoUpdateSummary.StoreScan.Error == "" || len(updated.LastAutoUpdateSummary.SkippedPackages) == 0 {
		t.Fatalf("expected timeout and skipped Store package summary, got %#v", updated.LastAutoUpdateSummary)
	}
}

func TestScheduledAutoUpdateSkipsStorePackageAfterUnpublishedIncompleteScan(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	repository := newTestStoreScanRepository(t)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{canonicalStoreAutoUpdateKey(userSID, pfn): true}
	store := newMemoryStateStore(state)

	executed := installScheduledStoreAutoUpdateHooks(t, userSID, pfn, repository, func(context.Context) (StoreScanResult, error) {
		completedAt := time.Now().UTC().Add(time.Second)
		return StoreScanResult{
			Published: false,
			Scan: StoreScanGeneration{
				ScanID:           "incomplete-unpublished",
				UserSID:          userSID,
				StartedAt:        completedAt.Add(-time.Second),
				CompletedAt:      completedAt,
				CompletionStatus: StoreScanIncomplete,
			},
		}, nil
	})

	results := runAutoUpdateWithStore(context.Background(), store)
	if *executed || len(results) != 0 {
		t.Fatalf("scheduled Store auto-update should not run after unpublished incomplete scan: executed=%t results=%#v", *executed, results)
	}
	updated, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastAutoUpdateSummary == nil || updated.LastAutoUpdateSummary.StoreScan.Error == "" || len(updated.LastAutoUpdateSummary.SkippedPackages) == 0 {
		t.Fatalf("expected unpublished scan and skipped Store package summary, got %#v", updated.LastAutoUpdateSummary)
	}
}

func TestScheduledAutoUpdateRunsNonStorePackageWhenStoreScanFails(t *testing.T) {
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{"winget:Vendor.App": true}
	store := newMemoryStateStore(state)

	oldScan := runStoreTransactionalScanForInventory
	runStoreTransactionalScanForInventory = func(context.Context) (StoreScanResult, error) {
		return StoreScanResult{}, errors.New("fresh Store scan failed")
	}
	defer func() { runStoreTransactionalScanForInventory = oldScan }()

	oldGetter := inventoryGetter
	inventoryGetter = func(context.Context) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:              "winget:Vendor.App",
			Manager:          managerWinget,
			ID:               "Vendor.App",
			Name:             "Vendor App",
			Version:          "1.0.0",
			AvailableVersion: "1.1.0",
			UpdateAvailable:  true,
			UpdateSupported:  true,
			Installed:        true,
		}}}}
	}
	defer func() { inventoryGetter = oldGetter }()

	restoreActions := replacePackageActionHooks(
		func(context.Context, time.Duration, ...string) CommandResult {
			return CommandResult{OK: true, Command: "winget upgrade Vendor.App"}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restoreActions()

	results := runAutoUpdateWithStore(context.Background(), store)
	if len(results) != 1 || results[0].Key != "winget:Vendor.App" || !results[0].Result.OK {
		t.Fatalf("non-Store auto-update should continue after Store scan failure, got %#v", results)
	}
}

func persistStoreAutoUpdatePositiveSnapshot(t *testing.T, repository StoreScanRepository, identity StoreInstalledIdentity, scanID string, completedAt time.Time) {
	t.Helper()
	scan := StoreScanGeneration{
		ScanID:           scanID,
		UserSID:          identity.UserSID,
		StartedAt:        completedAt.Add(-time.Second),
		CompletedAt:      completedAt,
		CompletionStatus: StoreScanCompleted,
	}
	target := &ExactStoreUpdateTarget{
		Identity:   identity,
		Provider:   StoreProviderIdentity{ID: managerStore, Name: "Store CLI", Backend: backendStoreCLI},
		ProductID:  "9NCODEX",
		UpdateID:   identity.PackageFamilyName,
		Verified:   true,
		VerifiedBy: "test",
		VerifiedAt: completedAt,
	}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         identity,
			ScanID:           scan.ScanID,
			Reason:           "fresh exact positive update evidence",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
			Target:           target,
			Evidence: []StoreEvidenceSummary{{
				Provider: managerStore,
				Health:   StoreProviderHealthy,
				Kind:     StoreObservationPositiveUpdateOffer,
			}},
		},
		ObservedAt:                 completedAt,
		StoreProductID:             target.ProductID,
		UpdateID:                   target.UpdateID,
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	if _, err := repository.PersistCompletedScanSnapshot(context.Background(), StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan:          scan,
		Inventory:     testStoreInventory(scan, identity.PackageFamilyName, "1.0.0"),
		Assessments:   []StorePublishedAssessment{assessment},
	}); err != nil {
		t.Fatal(err)
	}
}

func installScheduledStoreAutoUpdateHooks(
	t *testing.T,
	userSID string,
	pfn string,
	repository StoreScanRepository,
	scan func(context.Context) (StoreScanResult, error),
) *bool {
	t.Helper()
	restoreSID := replaceStoreScanSID(userSID)
	t.Cleanup(restoreSID)

	oldOpen := openStoreTransactionalStoreForInventory
	openStoreTransactionalStoreForInventory = func() (StoreScanRepository, error) {
		return repository, nil
	}
	t.Cleanup(func() { openStoreTransactionalStoreForInventory = oldOpen })

	oldScan := runStoreTransactionalScanForInventory
	runStoreTransactionalScanForInventory = scan
	t.Cleanup(func() { runStoreTransactionalScanForInventory = oldScan })

	oldGetter := inventoryGetter
	inventoryGetter = func(context.Context) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:                        packageKey(managerStore, pfn),
			Manager:                    managerStore,
			ID:                         pfn,
			Name:                       "Codex",
			Version:                    "1.0.0",
			Installed:                  true,
			Source:                     sourceNativeAppX,
			Match:                      pfn + "_1.0.0.0_x64__abc123",
			ActionBackend:              backendAppXInventory,
			UpdateSupported:            false,
			InstalledPackageFamilyName: pfn,
			ExactIdentityAvailable:     true,
		}}}}
	}
	t.Cleanup(func() { inventoryGetter = oldGetter })

	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(string) bool { return true }
	t.Cleanup(func() { packageActionManagerAvailable = oldAvailable })

	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	oldExecutor := storeExactUpdateExecutor
	executed := false
	storeExactUpdateExecutor = testStoreExactExecutor(
		fakeStoreExactRunner{
			result: CommandResult{Command: "fake store update", Code: 1, Stderr: "fake update stopped after selection"},
			after:  func() { executed = true },
		},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{{
			Identity:        identity,
			PackageFullName: pfn + "_1.0.0.0_x64__abc123",
			Version:         "1.0.0",
			Healthy:         true,
			Exists:          true,
			ObservedAt:      time.Now().UTC(),
		}}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	t.Cleanup(func() { storeExactUpdateExecutor = oldExecutor })
	return &executed
}
