package updater

import (
	"context"
	"sync"
	"time"
)

const statusCacheTTL = 30 * time.Second

func (app *App) refreshStatus(forceRefresh bool) {
	app.mu.Lock()
	cacheExpired := app.statusFetchedAt.IsZero() || time.Since(app.statusFetchedAt) > statusCacheTTL
	if app.statusLoading {
		if forceRefresh {
			app.statusQueued = true
			appLog("Status refresh queued.")
		}
		app.mu.Unlock()
		return
	}
	if !forceRefresh && !cacheExpired {
		app.mu.Unlock()
		return
	}
	app.statusLoading = true
	app.statusErr = ""
	app.mu.Unlock()
	appLog("Status refresh started.")

	if !app.startBackgroundWork("status refresh", func(ctx context.Context) {
		app.runStatusRefresh(ctx, forceRefresh)
	}) {
		app.mu.Lock()
		app.statusLoading = false
		app.statusErr = "shutdown in progress"
		app.mu.Unlock()
	}
}

func (app *App) runStatusRefresh(ctx context.Context, forceRefresh bool) {
	refreshedStatus := app.buildStatusResponseContext(ctx, forceRefresh)
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
	app.status = refreshedStatus
	app.statusFetchedAt = time.Now()
	app.statusErr = ""
	if app.statusQueued {
		app.statusQueued = false
		app.statusLoading = true
		app.mu.Unlock()
		app.startQueuedStatusRefresh()
		return
	}
	app.statusLoading = false
	app.mu.Unlock()
	appLog("Status refresh completed.")
}

func (app *App) startQueuedStatusRefresh() {
	appLog("Status refresh completed; running queued refresh.")
	if !app.startBackgroundWork("queued status refresh", func(ctx context.Context) {
		app.runStatusRefresh(ctx, true)
	}) {
		app.mu.Lock()
		app.statusLoading = false
		app.statusErr = "shutdown in progress"
		app.mu.Unlock()
	}
}

func buildStatusResponseContext(ctx context.Context, forceRefresh bool) StatusResponse {
	return buildStatusResponseContextWithUpdate(ctx, forceRefresh, AppUpdateStatus{CurrentVersion: currentAppVersion()})
}

func (app *App) buildStatusResponseContext(ctx context.Context, forceRefresh bool) StatusResponse {
	return buildStatusResponseContextWithUpdate(ctx, forceRefresh, app.appUpdateStatusContext(ctx, forceRefresh))
}

func buildStatusResponseContextWithUpdate(ctx context.Context, forceRefresh bool, appUpdateStatus AppUpdateStatus) StatusResponse {
	persistedState := loadStateContext(ctx)
	stateDirectory, _ := stateDir()
	var startupTaskEnabled bool
	var autoTaskEnabled bool
	var taskChecks sync.WaitGroup
	taskChecks.Add(2)
	go func() {
		defer taskChecks.Done()
		startupTaskEnabled = taskExistsContext(ctx, taskStartup)
	}()
	go func() {
		defer taskChecks.Done()
		autoTaskEnabled = taskExistsContext(ctx, taskAutoUpdate)
	}()
	var managerStatuses map[string]ManagerStatus
	if forceRefresh {
		managerStatuses = detectManagersFreshContext(ctx)
	} else {
		managerStatuses = detectManagersContext(ctx)
	}
	taskChecks.Wait()

	return StatusResponse{
		Admin:           isAdmin(),
		StateDir:        stateDirectory,
		Managers:        managerStatuses,
		StartupEnabled:  startupTaskEnabled,
		AutoTaskEnabled: autoTaskEnabled,
		Settings:        statusSettingsFromState(persistedState),
		AppUpdate:       appUpdateStatus,
		Application:     currentApplicationInfo(),
	}
}

func (app *App) refreshStatusSyncContext(ctx context.Context, reason string) StatusResponse {
	appLog("Status refresh started for %s.", reason)
	app.mu.Lock()
	app.statusLoading = true
	app.statusQueued = false
	app.statusErr = ""
	app.mu.Unlock()

	refreshedStatus := app.buildStatusResponseContext(ctx, true)
	if ctx.Err() != nil {
		app.mu.Lock()
		cachedStatus := app.status
		app.statusLoading = false
		app.statusQueued = false
		app.statusErr = "status refresh cancelled: " + ctx.Err().Error()
		app.mu.Unlock()
		appLog("Status refresh cancelled for %s.", reason)
		return cachedStatus
	}

	app.mu.Lock()
	app.status = refreshedStatus
	app.statusFetchedAt = time.Now()
	app.statusErr = ""
	if app.statusQueued {
		app.statusQueued = false
		app.statusLoading = true
		app.mu.Unlock()
		app.startQueuedStatusRefresh()
		return refreshedStatus
	}
	app.statusLoading = false
	app.mu.Unlock()
	appLog("Status refresh completed for %s.", reason)
	return refreshedStatus
}

func (app *App) statusSnapshot() StatusResponse {
	return app.statusSnapshotContext(context.Background())
}

func (app *App) statusSnapshotContext(ctx context.Context) StatusResponse {
	app.mu.RLock()
	snapshot := app.status
	statusLoading := app.statusLoading
	fetchedAt := app.statusFetchedAt
	refreshErr := app.statusErr
	inventoryManagerStatuses := cloneManagerStatuses(app.inventory.Managers)
	app.mu.RUnlock()

	snapshot.Settings = statusSettingsFromState(loadStateContext(ctx))
	if snapshot.StateDir == "" {
		snapshot.StateDir, _ = stateDir()
		snapshot.Admin = isAdmin()
	}
	if snapshot.Managers == nil {
		snapshot.Managers = map[string]ManagerStatus{}
	} else {
		snapshot.Managers = cloneManagerStatuses(snapshot.Managers)
	}
	if snapshot.AppUpdate.CurrentVersion == "" {
		snapshot.AppUpdate = app.appUpdateStatusContext(ctx, false)
	}
	if snapshot.Application.License == "" || snapshot.Application.Repository == "" {
		snapshot.Application = currentApplicationInfo()
	}
	mergeStatusInventoryManagerDetails(&snapshot, inventoryManagerStatuses)
	snapshot.AsyncSnapshot = asyncSnapshot(statusLoading, fetchedAt, refreshErr)
	return snapshot
}

func (app *App) appUpdateStatusContext(ctx context.Context, forceRefresh bool) AppUpdateStatus {
	currentVersion := currentAppVersion()
	if app == nil || app.appUpdateChecker == nil {
		return AppUpdateStatus{CurrentVersion: currentVersion}
	}
	app.mu.RLock()
	cachedStatus := app.appUpdateStatus
	cachedAt := app.appUpdateFetchedAt
	app.mu.RUnlock()
	if !forceRefresh && !cachedAt.IsZero() && time.Since(cachedAt) < appUpdateCacheTTL && cachedStatus.CurrentVersion != "" {
		return cachedStatus
	}
	checkCtx, cancel := context.WithTimeout(ctx, appUpdateCheckTimeout)
	defer cancel()
	updateStatus, err := app.appUpdateChecker.Check(checkCtx, currentVersion)
	checkedAt := time.Now()
	updateStatus.CurrentVersion = currentVersion
	updateStatus.CheckedAt = checkedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	if err != nil {
		updateStatus.Error = sanitizeProviderDiagnostic(err.Error())
	}
	app.mu.Lock()
	app.appUpdateStatus = updateStatus
	app.appUpdateFetchedAt = time.Now()
	app.mu.Unlock()
	return updateStatus
}

func statusSettingsFromState(persistedState State) StatusSettings {
	return StatusSettings{
		AutoUpdateGlobal:                persistedState.AutoUpdateGlobal,
		AutoUpdatePackages:              trimBoolMap(persistedState.AutoUpdatePackages, maxStateAutoUpdatePackages),
		Theme:                           persistedState.Theme,
		LastScanAt:                      persistedState.LastScanAt,
		LastAutoUpdateAt:                persistedState.LastAutoUpdateAt,
		LastAutoUpdateResults:           trimUpdateResultSummaries(persistedState.LastAutoUpdateResults),
		LastAutoUpdateSummary:           persistedState.LastAutoUpdateSummary,
		AppUpdatePromptDismissedVersion: persistedState.AppUpdatePromptDismissedVersion,
	}
}

func mergeStatusInventoryManagerDetails(statusResponse *StatusResponse, inventoryManagerStatuses map[string]ManagerStatus) {
	inventoryStoreStatus, ok := inventoryManagerStatuses[managerStore]
	if !ok || !inventoryStoreStatus.InventoryAvailable {
		return
	}
	storeStatus := statusResponse.Managers[managerStore]
	if storeStatus == (ManagerStatus{}) {
		storeStatus = inventoryStoreStatus
	} else {
		storeStatus.InventoryAvailable = true
		storeStatus.InventoryBackend = inventoryStoreStatus.InventoryBackend
		if storeStatus.ActionBackend == "" {
			storeStatus.ActionBackend = inventoryStoreStatus.ActionBackend
		}
	}
	statusResponse.Managers[managerStore] = storeStatus
}
