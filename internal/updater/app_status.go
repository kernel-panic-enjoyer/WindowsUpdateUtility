package updater

import (
	"context"
	"sync"
	"time"
)

const statusCacheTTL = 30 * time.Second

func (app *App) refreshStatus(force bool) {
	app.mu.Lock()
	stale := app.statusFetchedAt.IsZero() || time.Since(app.statusFetchedAt) > statusCacheTTL
	if app.statusLoading {
		if force {
			app.statusQueued = true
			appLog("Status refresh queued.")
		}
		app.mu.Unlock()
		return
	}
	if !force && !stale {
		app.mu.Unlock()
		return
	}
	app.statusLoading = true
	app.statusErr = ""
	app.mu.Unlock()
	appLog("Status refresh started.")

	if !app.startBackgroundWork("status refresh", func(ctx context.Context) {
		app.runStatusRefresh(ctx, force)
	}) {
		app.mu.Lock()
		app.statusLoading = false
		app.statusErr = "shutdown in progress"
		app.mu.Unlock()
	}
}

func (app *App) runStatusRefresh(ctx context.Context, force bool) {
	status := buildStatusResponseContext(ctx, force)
	if ctx.Err() != nil {
		app.mu.Lock()
		app.statusLoading = false
		app.statusQueued = false
		app.statusErr = "status refresh cancelled: " + ctx.Err().Error()
		app.mu.Unlock()
		appLog("Status refresh cancelled.")
		return
	}
	app.mu.Lock()
	app.status = status
	app.statusFetchedAt = time.Now()
	app.statusErr = ""
	if app.statusQueued {
		app.statusQueued = false
		app.statusLoading = true
		app.mu.Unlock()
		appLog("Status refresh completed; running queued refresh.")
		if !app.startBackgroundWork("queued status refresh", func(ctx context.Context) {
			app.runStatusRefresh(ctx, true)
		}) {
			app.mu.Lock()
			app.statusLoading = false
			app.statusErr = "shutdown in progress"
			app.mu.Unlock()
		}
		return
	}
	app.statusLoading = false
	app.mu.Unlock()
	appLog("Status refresh completed.")
}

func buildStatusResponseContext(ctx context.Context, force bool) StatusResponse {
	state := loadStateContext(ctx)
	dir, _ := stateDir()
	var startupEnabled bool
	var autoTaskEnabled bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		startupEnabled = taskExistsContext(ctx, taskStartup)
	}()
	go func() {
		defer wg.Done()
		autoTaskEnabled = taskExistsContext(ctx, taskAutoUpdate)
	}()
	var managers map[string]ManagerStatus
	if force {
		managers = detectManagersFreshContext(ctx)
	} else {
		managers = detectManagersContext(ctx)
	}
	wg.Wait()

	return StatusResponse{
		Admin:           isAdmin(),
		StateDir:        dir,
		Managers:        managers,
		StartupEnabled:  startupEnabled,
		AutoTaskEnabled: autoTaskEnabled,
		Settings:        state,
	}
}

func (app *App) refreshStatusSyncContext(ctx context.Context, reason string) StatusResponse {
	appLog("Status refresh started for %s.", reason)
	app.mu.Lock()
	app.statusLoading = true
	app.statusQueued = false
	app.statusErr = ""
	app.mu.Unlock()

	status := buildStatusResponseContext(ctx, true)
	if ctx.Err() != nil {
		app.mu.Lock()
		cached := app.status
		app.statusLoading = false
		app.statusQueued = false
		app.statusErr = "status refresh cancelled: " + ctx.Err().Error()
		app.mu.Unlock()
		appLog("Status refresh cancelled for %s.", reason)
		return cached
	}

	app.mu.Lock()
	app.status = status
	app.statusFetchedAt = time.Now()
	app.statusErr = ""
	if app.statusQueued {
		app.statusQueued = false
		app.statusLoading = true
		app.mu.Unlock()
		appLog("Status refresh completed; running queued refresh.")
		if !app.startBackgroundWork("queued status refresh", func(ctx context.Context) {
			app.runStatusRefresh(ctx, true)
		}) {
			app.mu.Lock()
			app.statusLoading = false
			app.statusErr = "shutdown in progress"
			app.mu.Unlock()
		}
		return status
	}
	app.statusLoading = false
	app.mu.Unlock()
	appLog("Status refresh completed for %s.", reason)
	return status
}

func (app *App) statusSnapshot() StatusResponse {
	return app.statusSnapshotContext(context.Background())
}

func (app *App) statusSnapshotContext(ctx context.Context) StatusResponse {
	app.mu.RLock()
	status := app.status
	loading := app.statusLoading
	fetchedAt := app.statusFetchedAt
	errText := app.statusErr
	inventoryManagers := cloneManagerStatuses(app.inventory.Managers)
	app.mu.RUnlock()

	if status.StateDir == "" {
		status.Settings = loadStateContext(ctx)
		status.StateDir, _ = stateDir()
		status.Admin = isAdmin()
	}
	if status.Managers == nil {
		status.Managers = map[string]ManagerStatus{}
	} else {
		status.Managers = cloneManagerStatuses(status.Managers)
	}
	mergeStatusInventoryManagerDetails(&status, inventoryManagers)
	status.AsyncSnapshot = asyncSnapshot(loading, fetchedAt, errText)
	return status
}

func mergeStatusInventoryManagerDetails(status *StatusResponse, inventoryManagers map[string]ManagerStatus) {
	inventoryStore, ok := inventoryManagers[managerStore]
	if !ok || !inventoryStore.InventoryAvailable {
		return
	}
	store := status.Managers[managerStore]
	if store == (ManagerStatus{}) {
		store = inventoryStore
	} else {
		store.InventoryAvailable = true
		store.InventoryBackend = inventoryStore.InventoryBackend
		if store.ActionBackend == "" {
			store.ActionBackend = inventoryStore.ActionBackend
		}
	}
	status.Managers[managerStore] = store
}
