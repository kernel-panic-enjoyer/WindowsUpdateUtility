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

	go app.runInventoryRefresh(refreshID, force)
}

func (app *App) runInventoryRefresh(refreshID int64, force bool) {
	inventory := inventoryGetter()
	app.mu.Lock()
	if refreshID != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale inventory refresh result.")
		return
	}
	app.inventory = inventory
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
			go app.runStoreScan()
		}
		go app.runInventoryRefresh(nextRefreshID, true)
		return
	}
	app.inventoryLoading = false
	startScan := app.beginStoreScanLocked(force)
	app.mu.Unlock()
	appLog("Inventory refresh completed with %d package(s).", len(inventory.Packages))
	if startScan {
		go app.runStoreScan()
	}
}

func (app *App) refreshInventorySync(reason string) Inventory {
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

	inventory := inventoryGetter()
	app.mu.Lock()
	if refreshID != app.inventoryRefreshID {
		app.mu.Unlock()
		appLog("Discarded stale synchronous inventory refresh result for %s.", reason)
		return inventory
	}
	app.inventory = inventory
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
			go app.runStoreScan()
		}
		go app.runInventoryRefresh(nextRefreshID, true)
		return inventory
	}
	app.inventoryLoading = false
	startScan := app.beginStoreScanLocked(true)
	app.mu.Unlock()
	appLog("Inventory refresh completed for %s with %d package(s).", reason, len(inventory.Packages))
	if startScan {
		go app.runStoreScan()
	}
	return inventory
}

// storeScanCooldown debounces automatic (non-forced) background Store scans so
// the heavy provider sweep does not re-run on every stale-cache refresh.
// Forced refreshes (startup, jobs, explicit user refresh) bypass the cooldown.
const storeScanCooldown = 5 * time.Minute

// beginStoreScanLocked decides whether to start a background Store scan and, if
// so, marks one in flight. It must be called with app.mu held and returns true
// when the caller should launch app.runStoreScan in a goroutine.
func (app *App) beginStoreScanLocked(force bool) bool {
	if !app.storeBackgroundScanEnabled || !storeTransactionalScanEnabled() {
		return false
	}
	// A non-forced refresh within the cooldown window does not warrant a scan.
	if !force && !app.storeScanFetchedAt.IsZero() && time.Since(app.storeScanFetchedAt) < storeScanCooldown {
		return false
	}
	if app.storeScanLoading {
		// A scan is already running. Queue a follow-up so this request is not
		// lost to an in-flight scan that may have started before the caller's
		// action (for example, re-scanning after applying a Store update).
		app.storeScanQueued = true
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
func (app *App) runStoreScan() {
	for {
		appLog("Background Microsoft Store update scan started.")
		result, err := runStoreTransactionalScanForInventory(context.Background())
		switch {
		case err != nil:
			appLog("Background Store update scan failed: %s", err)
		case !result.Published:
			appLog("Background Store update scan %s completed but was not published.", result.Scan.ScanID)
		default:
			appLog("Background Store update scan %s completed.", result.Scan.ScanID)
		}
		app.mu.Lock()
		app.storeScanFetchedAt = time.Now()
		if app.storeScanQueued {
			app.storeScanQueued = false
			app.mu.Unlock()
			continue
		}
		app.storeScanLoading = false
		app.mu.Unlock()
		return
	}
}

func (app *App) inventorySnapshot() InventoryResponse {
	app.mu.RLock()
	inventory := app.inventory
	loading := app.inventoryLoading
	fetchedAt := app.inventoryFetchedAt
	errText := app.inventoryErr
	storeLoading := app.storeScanLoading
	app.mu.RUnlock()

	response := InventoryResponse{
		Inventory:     inventory,
		AsyncSnapshot: asyncSnapshot(loading, fetchedAt, errText),
		StoreLoading:  storeLoading,
	}
	if response.Managers == nil {
		response.Managers = map[string]ManagerStatus{}
	}
	if response.CommandResults == nil {
		response.CommandResults = map[string]CommandResult{}
	}
	if fetchedAt.IsZero() {
		state := loadState()
		response.Scan = inventoryScanSummary(state, managedScanSourceCounts(state))
	}
	if storeTransactionalScanEnabled() {
		state := loadState()
		response.Inventory = applyPublishedStoreScanAssessments(context.Background(), state, response.Inventory)
	}
	return response
}
