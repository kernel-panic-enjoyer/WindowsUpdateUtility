package updater

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBeginStoreScanLockedGating verifies the background Store scan gating:
// disabled by default, single-flight while running, debounced for non-forced
// refreshes, and always allowed for forced refreshes.
func TestBeginStoreScanLockedGating(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	defer app.mu.Unlock()

	if app.beginStoreScanLocked(true) {
		t.Fatal("a scan must not start when background scanning is disabled")
	}
	app.storeBackgroundScanEnabled = true
	if !app.beginStoreScanLocked(true) {
		t.Fatal("a forced scan should start when enabled and idle")
	}
	if app.beginStoreScanLocked(true) {
		t.Fatal("a second scan must not start while one is already in flight (single-flight)")
	}

	app.storeScanLoading = false
	app.storeScanLastPublishedAt = time.Now()
	if app.beginStoreScanLocked(false) {
		t.Fatal("a non-forced scan within the cooldown window must be skipped")
	}
	if !app.beginStoreScanLocked(true) {
		t.Fatal("a forced scan must bypass the cooldown window")
	}

	app.storeScanLoading = false
	app.storeScanLastPublishedAt = time.Now().Add(-2 * storeScanCooldown)
	if !app.beginStoreScanLocked(false) {
		t.Fatal("a non-forced scan after the cooldown window should start")
	}
}

func TestBeginStoreScanLockedFailureRetryBackoff(t *testing.T) {
	app := &App{storeBackgroundScanEnabled: true}
	app.mu.Lock()
	defer app.mu.Unlock()

	app.storeScanLastFailureAt = time.Now()
	if app.beginStoreScanLocked(false) {
		t.Fatal("a non-forced scan inside the failure retry backoff must be skipped")
	}

	app.storeScanLastFailureAt = time.Now().Add(-2 * storeScanFailureRetryBackoff)
	if !app.beginStoreScanLocked(false) {
		t.Fatal("a non-forced scan after the failure retry backoff should start")
	}
}

func TestBackgroundStoreScanTimestamps(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())

	oldScan := runStoreTransactionalScanForInventory
	defer func() { runStoreTransactionalScanForInventory = oldScan }()

	app := &App{storeBackgroundScanEnabled: true, storeScanLoading: true}
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		return StoreScanResult{Published: true, Scan: StoreScanGeneration{ScanID: "published"}}, nil
	}
	app.runStoreScan(context.Background())
	if app.storeScanLastAttemptAt.IsZero() {
		t.Fatal("successful scan should record an attempt timestamp")
	}
	if app.storeScanLastPublishedAt.IsZero() {
		t.Fatal("successful published scan should record a publication timestamp")
	}
	if !app.storeScanLastFailureAt.IsZero() {
		t.Fatalf("successful published scan should clear failure timestamp, got %s", app.storeScanLastFailureAt)
	}

	app = &App{storeBackgroundScanEnabled: true, storeScanLoading: true}
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		return StoreScanResult{Scan: StoreScanGeneration{ScanID: "unpublished"}}, nil
	}
	app.runStoreScan(context.Background())
	if app.storeScanLastFailureAt.IsZero() {
		t.Fatal("unpublished scan should record a failure retry timestamp")
	}
	if !app.storeScanLastPublishedAt.IsZero() {
		t.Fatal("unpublished scan must not record a publication timestamp")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app = &App{storeBackgroundScanEnabled: true, storeScanLoading: true}
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		t.Fatal("cancelled scan should not invoke the Store pipeline")
		return StoreScanResult{}, nil
	}
	app.runStoreScan(ctx)
	if !app.storeScanLastFailureAt.IsZero() || !app.storeScanLastPublishedAt.IsZero() {
		t.Fatalf("shutdown cancellation should not schedule retry or publication timestamps: failure=%s published=%s", app.storeScanLastFailureAt, app.storeScanLastPublishedAt)
	}
}

// TestBackgroundStoreScanRunsOffCriticalPath verifies that refreshInventorySync
// returns the fast-manager inventory without waiting for the Store scan, that
// store_loading reflects the in-flight background scan, and that the scan runs
// exactly once and clears the flag on completion.
func TestBackgroundStoreScanRunsOffCriticalPath(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())

	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()
	inventoryGetter = func(ctx context.Context) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{
			{Key: "winget:Fast.App", Manager: managerWinget, ID: "Fast.App", Name: "Fast App", Installed: true},
		}}}
	}

	oldScan := runStoreTransactionalScanForInventory
	defer func() { runStoreTransactionalScanForInventory = oldScan }()
	var scanCount int32
	var startedOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		atomic.AddInt32(&scanCount, 1)
		startedOnce.Do(func() { close(started) })
		<-release
		return StoreScanResult{Published: true, Scan: StoreScanGeneration{ScanID: "test-scan"}}, nil
	}

	app := &App{storeBackgroundScanEnabled: true}

	done := make(chan Inventory, 1)
	go func() { done <- app.refreshInventorySync("test") }()

	var inv Inventory
	select {
	case inv = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("refreshInventorySync blocked on the background Store scan")
	}
	if len(inv.Packages) != 1 || inv.Packages[0].ID != "Fast.App" {
		t.Fatalf("fast-manager inventory was not returned promptly: %#v", inv.Packages)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Store scan did not start")
	}
	if !app.inventorySnapshot().StoreLoading {
		t.Fatal("store_loading should be true while the background Store scan is in flight")
	}

	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !app.inventorySnapshot().StoreLoading {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.inventorySnapshot().StoreLoading {
		t.Fatal("store_loading should clear after the background Store scan completes")
	}
	if got := atomic.LoadInt32(&scanCount); got != 1 {
		t.Fatalf("expected exactly one background Store scan, got %d", got)
	}
}

// TestForcedRefreshDuringScanQueuesFollowupScan verifies that a forced refresh
// arriving while a background Store scan is in flight is not dropped: a single
// follow-up scan runs after the in-flight one, and store_loading stays true
// until that follow-up completes.
func TestForcedRefreshDuringScanQueuesFollowupScan(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())

	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()
	inventoryGetter = func(ctx context.Context) Inventory { return Inventory{} }

	oldScan := runStoreTransactionalScanForInventory
	defer func() { runStoreTransactionalScanForInventory = oldScan }()
	var scanCount int32
	var firstStarted sync.Once
	firstStartedCh := make(chan struct{})
	gate := make(chan struct{})
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		if atomic.AddInt32(&scanCount, 1) == 1 {
			firstStarted.Do(func() { close(firstStartedCh) })
		}
		<-gate
		return StoreScanResult{Published: true, Scan: StoreScanGeneration{ScanID: "s"}}, nil
	}

	app := &App{storeBackgroundScanEnabled: true}

	app.refreshInventorySync("first")
	select {
	case <-firstStartedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first background scan did not start")
	}

	// Forced refresh while the first scan is in flight must queue a follow-up.
	app.refreshInventorySync("second-forced")
	app.mu.Lock()
	queued := app.storeScanQueued
	app.mu.Unlock()
	if !queued {
		t.Fatal("forced refresh during an in-flight scan should queue a follow-up scan")
	}
	if !app.inventorySnapshot().StoreLoading {
		t.Fatal("store_loading should remain true while a follow-up scan is queued")
	}

	gate <- struct{}{} // release first scan; runStoreScan loops into the follow-up
	gate <- struct{}{} // release the queued follow-up scan

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !app.inventorySnapshot().StoreLoading {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if app.inventorySnapshot().StoreLoading {
		t.Fatal("store_loading should clear after the queued follow-up scan completes")
	}
	if got := atomic.LoadInt32(&scanCount); got != 2 {
		t.Fatalf("expected initial scan plus one queued follow-up (2), got %d", got)
	}
}

// TestRefreshInventorySyncSkipsStoreScanWhenDisabled confirms that App values
// without background scanning enabled (e.g. unit-test apps) never spawn a Store
// scan, preserving existing test behavior.
func TestRefreshInventorySyncSkipsStoreScanWhenDisabled(t *testing.T) {
	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()
	inventoryGetter = func(ctx context.Context) Inventory { return Inventory{} }

	oldScan := runStoreTransactionalScanForInventory
	defer func() { runStoreTransactionalScanForInventory = oldScan }()
	var scanCount int32
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		atomic.AddInt32(&scanCount, 1)
		return StoreScanResult{}, nil
	}

	app := &App{}
	app.refreshInventorySync("test")
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&scanCount); got != 0 {
		t.Fatalf("disabled app must not trigger a background Store scan, got %d", got)
	}
	if app.inventorySnapshot().StoreLoading {
		t.Fatal("store_loading must remain false when background scanning is disabled")
	}
}
