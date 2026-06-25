package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunInventoryRefreshCancellationRetainsPreviousCache(t *testing.T) {
	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()

	getterCtx := make(chan context.Context, 1)
	inventoryGetter = func(ctx context.Context) Inventory {
		getterCtx <- ctx
		<-ctx.Done()
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Key: "winget:new", Manager: managerWinget, ID: "new"}}}}
	}

	previousFetchedAt := time.Now().Add(-time.Minute)
	app := &App{
		inventory:          Inventory{PackageLookup: PackageLookup{Packages: []Package{{Key: "winget:old", Manager: managerWinget, ID: "old"}}}},
		inventoryFetchedAt: previousFetchedAt,
		inventoryLoading:   true,
		inventoryRefreshID: 1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		app.runInventoryRefresh(ctx, 1, true)
		close(done)
	}()

	select {
	case got := <-getterCtx:
		if got != ctx {
			t.Fatalf("inventory getter received unexpected context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inventory getter was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("inventory refresh did not stop after cancellation")
	}

	app.mu.RLock()
	defer app.mu.RUnlock()
	if len(app.inventory.Packages) != 1 || app.inventory.Packages[0].Key != "winget:old" {
		t.Fatalf("cancelled refresh replaced cached inventory: %+v", app.inventory.Packages)
	}
	if app.inventoryLoading {
		t.Fatal("cancelled refresh left inventory loading")
	}
	if app.inventoryFetchedAt != previousFetchedAt {
		t.Fatalf("cancelled refresh changed successful fetched timestamp: got %v want %v", app.inventoryFetchedAt, previousFetchedAt)
	}
	if !strings.Contains(strings.ToLower(app.inventoryErr), "cancel") {
		t.Fatalf("cancelled refresh did not expose cancellation error: %q", app.inventoryErr)
	}
}

func TestQueuedInventoryRefreshCancelledDuringShutdown(t *testing.T) {
	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()

	firstStarted := make(chan context.Context, 1)
	inventoryGetter = func(ctx context.Context) Inventory {
		select {
		case firstStarted <- ctx:
		default:
		}
		<-ctx.Done()
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Key: "winget:cancelled", Manager: managerWinget, ID: "cancelled"}}}}
	}

	app := &App{}
	app.refreshInventory(true)
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initial inventory refresh did not start")
	}
	app.refreshInventory(true)
	app.requestShutdown("test")

	app.mu.RLock()
	defer app.mu.RUnlock()
	if app.inventoryLoading || app.inventoryQueued {
		t.Fatalf("shutdown left inventory refresh state active: loading=%t queued=%t", app.inventoryLoading, app.inventoryQueued)
	}
	if len(app.inventory.Packages) != 0 {
		t.Fatalf("shutdown cancellation published partial inventory: %+v", app.inventory.Packages)
	}
	if !strings.Contains(strings.ToLower(app.inventoryErr), "cancel") {
		t.Fatalf("shutdown cancellation did not expose cancellation error: %q", app.inventoryErr)
	}
}

func TestRunStatusRefreshCancellationDoesNotWaitOnManagerDetection(t *testing.T) {
	managerDetectionCache.mu.Lock()
	inFlight := make(chan struct{})
	managerDetectionCache.cached = nil
	managerDetectionCache.fetchedAt = time.Time{}
	managerDetectionCache.inFlight = inFlight
	managerDetectionCache.mu.Unlock()
	t.Cleanup(func() {
		managerDetectionCache.mu.Lock()
		if managerDetectionCache.inFlight == inFlight {
			managerDetectionCache.inFlight = nil
			close(inFlight)
		}
		managerDetectionCache.cached = nil
		managerDetectionCache.fetchedAt = time.Time{}
		managerDetectionCache.mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := &App{statusLoading: true}
	done := make(chan struct{})
	go func() {
		app.runStatusRefresh(ctx, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("status refresh blocked on manager detection after cancellation")
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	if app.statusLoading || app.statusQueued {
		t.Fatalf("cancelled status refresh left active state: loading=%t queued=%t", app.statusLoading, app.statusQueued)
	}
	if !strings.Contains(strings.ToLower(app.statusErr), "cancel") {
		t.Fatalf("cancelled status refresh did not expose cancellation error: %q", app.statusErr)
	}
}

func TestRunAutoUpdatePassesContextToInventoryGetter(t *testing.T) {
	oldGetter := inventoryGetter
	oldStoreScan := runStoreTransactionalScanForInventory
	defer func() {
		inventoryGetter = oldGetter
		runStoreTransactionalScanForInventory = oldStoreScan
	}()

	type contextKey string
	key := contextKey("marker")
	ctx := context.WithValue(context.Background(), key, "expected")
	called := false
	inventoryGetter = func(got context.Context) Inventory {
		called = true
		if got.Value(key) != "expected" {
			t.Fatalf("scheduled auto-update inventory getter did not receive caller context")
		}
		return Inventory{}
	}
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		return StoreScanResult{}, nil
	}

	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages = map[string]bool{"winget:Vendor.App": true}
	runAutoUpdateWithStore(ctx, newMemoryStateStore(state))
	if !called {
		t.Fatal("scheduled auto-update did not read inventory")
	}
}

func TestCollectManagerInventoryPassesContextToCollectors(t *testing.T) {
	type contextKey string
	key := contextKey("marker")
	ctx := context.WithValue(context.Background(), key, "expected")

	installedCalled := false
	updatesCalled := false
	collectManagerInventory(
		ctx,
		managerWinget,
		func(got context.Context) ([]Package, CommandResult) {
			installedCalled = true
			if got.Value(key) != "expected" {
				t.Fatalf("installed collector did not receive caller context")
			}
			return nil, CommandResult{OK: true}
		},
		func(got context.Context) (map[string]string, map[string]Package, CommandResult) {
			updatesCalled = true
			if got.Value(key) != "expected" {
				t.Fatalf("updates collector did not receive caller context")
			}
			return nil, nil, CommandResult{OK: true}
		},
		"list",
		"updates",
	)
	if !installedCalled || !updatesCalled {
		t.Fatalf("collectors were not called: installed=%t updates=%t", installedCalled, updatesCalled)
	}
}

func TestCollectNativeStorePackagedInventoryUsesCallerContext(t *testing.T) {
	oldProvider := storePackagedAppInventoryProvider
	defer func() { storePackagedAppInventoryProvider = oldProvider }()

	type contextKey string
	key := contextKey("marker")
	ctx := context.WithValue(context.Background(), key, "expected")
	providerCalled := false
	storePackagedAppInventoryProvider = func() StorePackagedAppInventoryProvider {
		return fakeStorePackagedInventoryProvider{
			inventory: func(got context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
				providerCalled = true
				if got.Value(key) != "expected" {
					t.Fatalf("native Store inventory provider did not receive caller context")
				}
				return StorePackagedAppInventory{Scan: scan}, CommandResult{OK: true, Command: "native Store inventory"}
			},
		}
	}

	_, result := collectNativeStorePackagedInventoryContext(ctx)
	if !providerCalled {
		t.Fatal("native Store inventory provider was not called")
	}
	if !result.OK {
		t.Fatalf("native Store inventory result was not OK: %+v", result)
	}
}

func TestDetectManagersWaitHonorsContext(t *testing.T) {
	managerDetectionCache.mu.Lock()
	inFlight := make(chan struct{})
	managerDetectionCache.cached = nil
	managerDetectionCache.fetchedAt = time.Time{}
	managerDetectionCache.inFlight = inFlight
	managerDetectionCache.mu.Unlock()
	t.Cleanup(func() {
		managerDetectionCache.mu.Lock()
		if managerDetectionCache.inFlight == inFlight {
			managerDetectionCache.inFlight = nil
			close(inFlight)
		}
		managerDetectionCache.cached = nil
		managerDetectionCache.fetchedAt = time.Time{}
		managerDetectionCache.mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan map[string]ManagerStatus, 1)
	go func() {
		done <- detectManagersCached(ctx, true)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manager detection waiter blocked after context cancellation")
	}

	managerDetectionCache.mu.Lock()
	defer managerDetectionCache.mu.Unlock()
	if managerDetectionCache.inFlight != inFlight {
		t.Fatal("cancelled waiter corrupted another caller's in-flight detection")
	}
}

type fakeStorePackagedInventoryProvider struct {
	inventory func(context.Context, StoreScanGeneration) (StorePackagedAppInventory, CommandResult)
}

func (provider fakeStorePackagedInventoryProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	return provider.inventory(ctx, scan)
}
