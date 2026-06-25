package updater

import (
	"context"
	"strings"
	"time"
)

const inventoryCacheTTL = 90 * time.Second

func (app *App) refreshInventory(force bool) {
	app.mu.Lock()
	stale := app.inventoryFetchedAt.IsZero() || time.Since(app.inventoryFetchedAt) > inventoryCacheTTL
	if app.inventoryLoading {
		if force {
			app.inventoryQueued = true
			appLog("Inventory refresh queued.")
		}
		app.mu.Unlock()
		return
	}
	if !force && !stale {
		app.mu.Unlock()
		return
	}
	app.inventoryLoading = true
	app.inventoryErr = ""
	app.inventoryRefreshID++
	refreshID := app.inventoryRefreshID
	app.mu.Unlock()
	appLog("Inventory refresh started.")

	if !app.startBackgroundWork("inventory refresh", func(ctx context.Context) {
		app.runInventoryRefresh(ctx, refreshID, force)
	}) {
		app.mu.Lock()
		if refreshID == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryErr = "shutdown in progress"
		}
		app.mu.Unlock()
	}
}

func (app *App) runInventoryRefresh(ctx context.Context, refreshID int64, force bool) {
	inventory := inventoryGetter(ctx)
	if ctx.Err() != nil {
		app.mu.Lock()
		if refreshID == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryQueued = false
			app.inventoryErr = "inventory refresh cancelled: " + ctx.Err().Error()
		}
		app.mu.Unlock()
		appLog("Inventory refresh cancelled.")
		return
	}
	app.mu.Lock()
	if refreshID != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale inventory refresh result.")
		return
	}
	app.inventory = inventory.DeepCopy()
	app.inventoryFetchedAt = time.Now()
	app.inventoryErr = ""
	if app.inventoryQueued {
		app.inventoryQueued = false
		app.inventoryLoading = true
		app.inventoryRefreshID++
		nextRefreshID := app.inventoryRefreshID
		startScan := app.beginStoreScanLocked(force)
		app.mu.Unlock()
		appLog("Inventory refresh completed with %d package(s); running queued refresh.", len(inventory.Packages))
		if startScan {
			app.startStoreScanBackground()
		}
		if !app.startBackgroundWork("queued inventory refresh", func(ctx context.Context) {
			app.runInventoryRefresh(ctx, nextRefreshID, true)
		}) {
			app.mu.Lock()
			if nextRefreshID == app.inventoryRefreshID {
				app.inventoryLoading = false
				app.inventoryErr = "shutdown in progress"
			}
			app.mu.Unlock()
		}
		return
	}
	app.inventoryLoading = false
	startScan := app.beginStoreScanLocked(force)
	app.mu.Unlock()
	appLog("Inventory refresh completed with %d package(s).", len(inventory.Packages))
	if startScan {
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
	refreshID := app.inventoryRefreshID
	app.inventoryLoading = true
	app.inventoryQueued = false
	app.inventoryErr = ""
	app.mu.Unlock()

	inventory := inventoryGetter(ctx)
	if ctx.Err() != nil {
		app.mu.Lock()
		cached := app.inventory.DeepCopy()
		if refreshID == app.inventoryRefreshID {
			app.inventoryLoading = false
			app.inventoryQueued = false
			app.inventoryErr = "inventory refresh cancelled: " + ctx.Err().Error()
		}
		app.mu.Unlock()
		appLog("Inventory refresh cancelled for %s.", reason)
		return cached
	}
	app.mu.Lock()
	if refreshID != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale synchronous inventory refresh result for %s.", reason)
		return inventory
	}
	app.inventory = inventory.DeepCopy()
	app.inventoryFetchedAt = time.Now()
	app.inventoryErr = ""
	if app.inventoryQueued {
		app.inventoryQueued = false
		app.inventoryLoading = true
		app.inventoryRefreshID++
		nextRefreshID := app.inventoryRefreshID
		startScan := app.beginStoreScanLocked(true)
		app.mu.Unlock()
		appLog("Inventory refresh completed for %s with %d package(s); running queued refresh.", reason, len(inventory.Packages))
		if startScan {
			app.startStoreScanBackground()
		}
		if !app.startBackgroundWork("queued inventory refresh", func(ctx context.Context) {
			app.runInventoryRefresh(ctx, nextRefreshID, true)
		}) {
			app.mu.Lock()
			if nextRefreshID == app.inventoryRefreshID {
				app.inventoryLoading = false
				app.inventoryErr = "shutdown in progress"
			}
			app.mu.Unlock()
		}
		return inventory
	}
	app.inventoryLoading = false
	startScan := app.beginStoreScanLocked(true)
	app.mu.Unlock()
	appLog("Inventory refresh completed for %s with %d package(s).", reason, len(inventory.Packages))
	if startScan {
		app.startStoreScanBackground()
	}
	return inventory
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
func (app *App) beginStoreScanLocked(force bool) bool {
	if !app.storeBackgroundScanEnabled {
		return false
	}
	now := time.Now()
	// A non-forced refresh within the cooldown window does not warrant a scan.
	if !force {
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
		if force {
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
		loading := app.storeScanLoading
		app.mu.RUnlock()
		if !loading {
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

type inventoryCacheSnapshot struct {
	inventory    Inventory
	loading      bool
	fetchedAt    time.Time
	errText      string
	storeLoading bool
}

func (app *App) cacheInventorySnapshot() inventoryCacheSnapshot {
	app.mu.RLock()
	snapshot := inventoryCacheSnapshot{
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
	snapshot, err := app.effectiveInventoryCacheSnapshot(ctx)
	return snapshot.inventory, err
}

func (app *App) effectiveInventoryCacheSnapshot(ctx context.Context) (inventoryCacheSnapshot, error) {
	snapshot := app.cacheInventorySnapshot()
	state := loadStateContext(ctx)
	if snapshot.fetchedAt.IsZero() {
		snapshot.inventory.Scan = inventoryScanSummary(state, managedScanSourceCounts(state))
	}
	snapshot.inventory = effectiveInventoryFromBase(ctx, state, snapshot.inventory)
	return snapshot, ctx.Err()
}

func effectiveInventoryFromBase(ctx context.Context, state State, inventory Inventory) Inventory {
	inventory = applyInventoryState(state, inventory.DeepCopy())
	return applyPublishedStoreScanAssessments(ctx, state, inventory)
}

func applyInventoryState(state State, inventory Inventory) Inventory {
	for index := range inventory.Packages {
		inventory.Packages[index].AutoUpdate = packageAutoUpdateEnabled(state, inventory.Packages[index])
		inventory.Packages[index] = applyPackageCapabilities(inventory.Packages[index])
	}
	return inventory
}

func (app *App) inventorySnapshot() InventoryResponse {
	return app.inventorySnapshotContext(context.Background())
}

func (app *App) inventorySnapshotContext(ctx context.Context) InventoryResponse {
	snapshot, _ := app.effectiveInventoryCacheSnapshot(ctx)

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
