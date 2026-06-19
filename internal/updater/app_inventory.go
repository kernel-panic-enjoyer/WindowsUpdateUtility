package updater

import (
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

	go app.runInventoryRefresh(refreshID)
}

func (app *App) runInventoryRefresh(refreshID int64) {
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
		app.mu.Unlock()
		appLog("Inventory refresh completed with %d package(s); running queued refresh.", len(inventory.Packages))
		go app.runInventoryRefresh(nextRefreshID)
		return
	}
	app.inventoryLoading = false
	app.mu.Unlock()
	appLog("Inventory refresh completed with %d package(s).", len(inventory.Packages))
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
		app.mu.Unlock()
		appLog("Inventory refresh completed for %s with %d package(s); running queued refresh.", reason, len(inventory.Packages))
		go app.runInventoryRefresh(nextRefreshID)
		return inventory
	}
	app.inventoryLoading = false
	app.mu.Unlock()
	appLog("Inventory refresh completed for %s with %d package(s).", reason, len(inventory.Packages))
	return inventory
}

func (app *App) inventorySnapshot() InventoryResponse {
	app.mu.RLock()
	inventory := app.inventory
	loading := app.inventoryLoading
	fetchedAt := app.inventoryFetchedAt
	errText := app.inventoryErr
	app.mu.RUnlock()

	response := InventoryResponse{
		Inventory:     inventory,
		AsyncSnapshot: asyncSnapshot(loading, fetchedAt, errText),
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
	return response
}
