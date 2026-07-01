package updater

import (
	"context"
	"strings"
	"time"
)

const inventoryCacheTTL = 90 * time.Second

func (app *App) refreshInventory(force bool) {
	app.mu.Lock()
	cacheExpired := app.inventoryFetchedAt.IsZero() || time.Since(app.inventoryFetchedAt) > inventoryCacheTTL
	if app.inventoryLoading {
		if force {
			app.inventoryQueued = true
			appLog("Inventory refresh queued.")
		}
		app.mu.Unlock()
		return
	}
	if !force && !cacheExpired {
		app.mu.Unlock()
		return
	}
	app.inventoryLoading = true
	app.inventoryErr = ""
	app.inventoryRefreshID++
	refreshGeneration := app.inventoryRefreshID
	app.mu.Unlock()
	appLog("Inventory refresh started.")

	if !app.startBackgroundWork("inventory refresh", func(ctx context.Context) {
		app.runInventoryRefresh(ctx, refreshGeneration, force)
	}) {
		app.mu.Lock()
		if refreshGeneration == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryErr = "shutdown in progress"
		}
		app.mu.Unlock()
	}
}

func (app *App) runInventoryRefresh(ctx context.Context, refreshGeneration int64, force bool) {
	refreshedInventory := inventoryGetter(ctx)
	if ctx.Err() != nil {
		app.mu.Lock()
		if refreshGeneration == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryQueued = false
			app.inventoryErr = "inventory refresh cancelled: " + ctx.Err().Error()
		}
		app.mu.Unlock()
		appLog("Inventory refresh cancelled.")
		return
	}
	app.mu.Lock()
	if refreshGeneration != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale inventory refresh result.")
		return
	}
	app.inventory = refreshedInventory.DeepCopy()
	app.inventoryFetchedAt = time.Now()
	app.inventoryErr = ""
	if app.inventoryQueued {
		queuedRefreshGeneration, shouldStartStoreScan := app.prepareQueuedInventoryRefreshLocked(force)
		app.mu.Unlock()
		appLog("Inventory refresh completed with %d package(s); running queued refresh.", len(refreshedInventory.Packages))
		if shouldStartStoreScan {
			app.startStoreScanBackground()
		}
		app.startQueuedInventoryRefresh(queuedRefreshGeneration)
		return
	}
	app.inventoryLoading = false
	shouldStartStoreScan := app.beginStoreScanLocked(force)
	app.mu.Unlock()
	appLog("Inventory refresh completed with %d package(s).", len(refreshedInventory.Packages))
	if shouldStartStoreScan {
		app.startStoreScanBackground()
	}
}

func (app *App) refreshInventorySync(reason string) Inventory {
	return app.refreshInventorySyncContext(context.Background(), reason)
}

func (app *App) refreshInventorySyncContext(ctx context.Context, reason string) Inventory {
	if strings.TrimSpace(reason) == "" {
		reason = "synchronous request"
	}
	appLog("Inventory refresh started for %s.", reason)
	app.mu.Lock()
	app.inventoryRefreshID++
	refreshGeneration := app.inventoryRefreshID
	app.inventoryLoading = true
	app.inventoryQueued = false
	app.inventoryErr = ""
	app.mu.Unlock()

	refreshedInventory := inventoryGetter(ctx)
	if ctx.Err() != nil {
		app.mu.Lock()
		cachedInventory := app.inventory.DeepCopy()
		if refreshGeneration == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryQueued = false
			app.inventoryErr = "inventory refresh cancelled: " + ctx.Err().Error()
		}
		app.mu.Unlock()
		appLog("Inventory refresh cancelled for %s.", reason)
		return cachedInventory
	}
	app.mu.Lock()
	if refreshGeneration != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale synchronous inventory refresh result for %s.", reason)
		return refreshedInventory
	}
	app.inventory = refreshedInventory.DeepCopy()
	app.inventoryFetchedAt = time.Now()
	app.inventoryErr = ""
	if app.inventoryQueued {
		queuedRefreshGeneration, shouldStartStoreScan := app.prepareQueuedInventoryRefreshLocked(true)
		app.mu.Unlock()
		appLog("Inventory refresh completed for %s with %d package(s); running queued refresh.", reason, len(refreshedInventory.Packages))
		if shouldStartStoreScan {
			app.startStoreScanBackground()
		}
		app.startQueuedInventoryRefresh(queuedRefreshGeneration)
		return refreshedInventory
	}
	app.inventoryLoading = false
	shouldStartStoreScan := app.beginStoreScanLocked(true)
	app.mu.Unlock()
	appLog("Inventory refresh completed for %s with %d package(s).", reason, len(refreshedInventory.Packages))
	if shouldStartStoreScan {
		app.startStoreScanBackground()
	}
	return refreshedInventory
}

// prepareQueuedInventoryRefreshLocked starts the next refresh generation after
// the just-finished generation has published. It must be called with app.mu held.
func (app *App) prepareQueuedInventoryRefreshLocked(forceStoreScan bool) (int64, bool) {
	app.inventoryQueued = false
	app.inventoryLoading = true
	app.inventoryRefreshID++
	queuedRefreshGeneration := app.inventoryRefreshID
	shouldStartStoreScan := app.beginStoreScanLocked(forceStoreScan)
	return queuedRefreshGeneration, shouldStartStoreScan
}

func (app *App) startQueuedInventoryRefresh(refreshGeneration int64) {
	if app.startBackgroundWork("queued inventory refresh", func(ctx context.Context) {
		app.runInventoryRefresh(ctx, refreshGeneration, true)
	}) {
		return
	}
	app.mu.Lock()
	if refreshGeneration == app.inventoryRefreshID {
		app.inventoryLoading = false
		app.inventoryErr = "shutdown in progress"
	}
	app.mu.Unlock()
}

func (app *App) startStoreScanBackground() {
	if app.startBackgroundWork("Store update scan", app.runStoreScan) {
		return
	}
	app.mu.Lock()
	app.storeScanLoading = false
	app.storeScanQueued = false
	app.mu.Unlock()
}

// storeScanCooldown debounces automatic (non-forced) background Store scans so
// the heavy provider sweep does not re-run on every stale-cache refresh.
// Forced refreshes (startup, jobs, explicit user refresh) bypass the cooldown.
const storeScanCooldown = 5 * time.Minute
const storeScanFailureRetryBackoff = 45 * time.Second

// beginStoreScanLocked decides whether to start a background Store scan and, if
// so, marks one in flight. It must be called with app.mu held and returns true
// when the caller should launch app.runStoreScan in a goroutine.
func (app *App) beginStoreScanLocked(forceStoreScan bool) bool {
	if !app.storeBackgroundScanEnabled {
		return false
	}
	now := time.Now()
	// A non-forced refresh within the cooldown window does not warrant a scan.
	if !forceStoreScan {
		if !app.storeScanLastPublishedAt.IsZero() && now.Sub(app.storeScanLastPublishedAt) < storeScanCooldown {
			return false
		}
		if !app.storeScanLastFailureAt.IsZero() && now.Sub(app.storeScanLastFailureAt) < storeScanFailureRetryBackoff {
			return false
		}
	}
	if app.storeScanLoading {
		// A scan is already running. Queue a follow-up so this request is not
		// lost to an in-flight scan that may have started before the caller's
		// action (for example, re-scanning after applying a Store update).
		if forceStoreScan {
			app.storeScanQueued = true
		}
		return false
	}
	app.storeScanLoading = true
	return true
}

// runStoreScan runs the expensive Microsoft Store transactional scan and
// persists its published snapshot. It does not write back to app.inventory:
// inventorySnapshot re-overlays the latest published snapshot on every read, so
// the fresh Store generation surfaces automatically on the next poll.
//
// If a fresh scan is requested while one is in flight (storeScanQueued), this
// loops and runs exactly one follow-up so a forced refresh is never dropped;
// storeScanLoading stays true across the follow-up so the frontend keeps
// polling until the latest requested scan completes.
func (app *App) runStoreScan(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			app.mu.Lock()
			app.storeScanLoading = false
			app.storeScanQueued = false
			app.mu.Unlock()
			appLog("Background Microsoft Store update scan cancelled before start.")
			return
		}
		appLog("Background Microsoft Store update scan started.")
		app.mu.Lock()
		app.storeScanLastAttemptAt = time.Now()
		app.mu.Unlock()
		result, err := runStoreTransactionalScanForInventory(ctx)
		switch {
		case ctx.Err() != nil:
			appLog("Background Store update scan cancelled.")
		case err != nil:
			appLog("Background Store update scan failed: %s", err)
		case !result.Published:
			appLog("Background Store update scan %s completed but was not published.", result.Scan.ScanID)
		default:
			appLog("Background Store update scan %s completed.", result.Scan.ScanID)
		}
		app.mu.Lock()
		if ctx.Err() == nil {
			now := time.Now()
			if err == nil && result.Published {
				app.storeScanLastPublishedAt = now
				app.storeScanLastFailureAt = time.Time{}
			} else {
				app.storeScanLastFailureAt = now
			}
		}
		if ctx.Err() == nil && app.storeScanQueued {
			app.storeScanQueued = false
			app.mu.Unlock()
			continue
		}
		app.storeScanLoading = false
		app.mu.Unlock()
		return
	}
}

func (app *App) waitForStoreScanIdle(ctx context.Context, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 12 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		app.mu.RLock()
		storeScanLoading := app.storeScanLoading
		app.mu.RUnlock()
		if !storeScanLoading {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

type cachedInventorySnapshot struct {
	inventory    Inventory
	loading      bool
	fetchedAt    time.Time
	errText      string
	storeLoading bool
}

func (app *App) cacheInventorySnapshot() cachedInventorySnapshot {
	app.mu.RLock()
	snapshot := cachedInventorySnapshot{
		inventory:    app.inventory.DeepCopy(),
		loading:      app.inventoryLoading,
		fetchedAt:    app.inventoryFetchedAt,
		errText:      app.inventoryErr,
		storeLoading: app.storeScanLoading,
	}
	app.mu.RUnlock()
	return snapshot
}

func (app *App) effectiveInventorySnapshot(ctx context.Context) (Inventory, error) {
	snapshot, err := app.effectiveCachedInventorySnapshot(ctx)
	return snapshot.inventory, err
}

func (app *App) effectiveCachedInventorySnapshot(ctx context.Context) (cachedInventorySnapshot, error) {
	snapshot := app.cacheInventorySnapshot()
	persistedState := loadStateContext(ctx)
	if snapshot.fetchedAt.IsZero() {
		snapshot.inventory.Scan = inventoryScanSummary(persistedState, managedScanSourceCounts(persistedState))
	}
	snapshot.inventory = effectiveInventoryFromBase(ctx, persistedState, snapshot.inventory)
	return snapshot, ctx.Err()
}

func effectiveInventoryFromBase(ctx context.Context, state State, baseInventory Inventory) Inventory {
	effectiveInventory := applyStateAndCapabilitiesToInventory(state, baseInventory.DeepCopy())
	return applyPublishedStoreScanAssessments(ctx, state, effectiveInventory)
}

func applyStateAndCapabilitiesToInventory(state State, inventory Inventory) Inventory {
	for packageIndex := range inventory.Packages {
		inventory.Packages[packageIndex].AutoUpdate = packageAutoUpdateEnabled(state, inventory.Packages[packageIndex])
		inventory.Packages[packageIndex] = applyPackageCapabilities(inventory.Packages[packageIndex])
	}
	return inventory
}

func (app *App) inventorySnapshot() InventoryResponse {
	return app.inventorySnapshotContext(context.Background())
}

func (app *App) inventorySnapshotContext(ctx context.Context) InventoryResponse {
	snapshot, _ := app.effectiveCachedInventorySnapshot(ctx)

	response := InventoryResponse{
		Inventory:     snapshot.inventory,
		AsyncSnapshot: asyncSnapshot(snapshot.loading, snapshot.fetchedAt, snapshot.errText),
		StoreLoading:  snapshot.storeLoading,
	}
	if response.Managers == nil {
		response.Managers = map[string]ManagerStatus{}
	}
	if response.CommandResults == nil {
		response.CommandResults = map[string]CommandResult{}
	}
	return response
}
