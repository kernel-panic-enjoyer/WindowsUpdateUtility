package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	inventoryCacheTTL       = 90 * time.Second
	statusCacheTTL          = 30 * time.Second
)

type InventoryResponse struct {
	Inventory
	AsyncSnapshot
}

type StatusResponse struct {
	Admin           bool                     `json:"admin"`
	StateDir        string                   `json:"state_dir"`
	Managers        map[string]ManagerStatus `json:"managers"`
	StartupEnabled  bool                     `json:"startup_enabled"`
	AutoTaskEnabled bool                     `json:"auto_task_enabled"`
	Settings        State                    `json:"settings"`
	AsyncSnapshot
}

type AsyncSnapshot struct {
	Loading   bool   `json:"loading"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func asyncSnapshot(loading bool, fetchedAt time.Time, errText string) AsyncSnapshot {
	snapshot := AsyncSnapshot{Loading: loading, Error: errText}
	if !fetchedAt.IsZero() {
		snapshot.UpdatedAt = fetchedAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	return snapshot
}

type App struct {
	token              string
	server             *http.Server
	mu                 sync.RWMutex
	inventory          Inventory
	inventoryLoading   bool
	inventoryQueued    bool
	inventoryFetchedAt time.Time
	inventoryErr       string
	status             StatusResponse
	statusLoading      bool
	statusFetchedAt    time.Time
	statusErr          string
	shutdownOnce       sync.Once
}

func (app *App) requestShutdown(source string) {
	app.shutdownOnce.Do(func() {
		appLog("%s quit requested.", source)
		if app.server == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()
		if err := app.server.Shutdown(ctx); err != nil {
			appLog("Graceful shutdown failed: %s; forcing server close.", err)
			_ = app.server.Close()
		}
	})
}

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
	app.mu.Unlock()
	appLog("Inventory refresh started.")

	go app.runInventoryRefresh()
}

func (app *App) runInventoryRefresh() {
	inventory := getInventory()
	app.mu.Lock()
	app.inventory = inventory
	app.inventoryFetchedAt = time.Now()
	app.inventoryErr = ""
	if app.inventoryQueued {
		app.inventoryQueued = false
		app.inventoryLoading = true
		app.mu.Unlock()
		appLog("Inventory refresh completed with %d package(s); running queued refresh.", len(inventory.Packages))
		go app.runInventoryRefresh()
		return
	}
	app.inventoryLoading = false
	app.mu.Unlock()
	appLog("Inventory refresh completed with %d package(s).", len(inventory.Packages))
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
		response.Scan = inventoryScanSummary(state, scanSourceCounts(state.WingetApps))
	}
	return response
}

func (app *App) refreshStatus(force bool) {
	app.mu.Lock()
	stale := app.statusFetchedAt.IsZero() || time.Since(app.statusFetchedAt) > statusCacheTTL
	if app.statusLoading || (!force && !stale) {
		app.mu.Unlock()
		return
	}
	app.statusLoading = true
	app.statusErr = ""
	app.mu.Unlock()
	appLog("Status refresh started.")

	go func() {
		state := loadState()
		dir, _ := stateDir()
		var startupEnabled bool
		var autoTaskEnabled bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			startupEnabled = taskExists(taskStartup)
		}()
		go func() {
			defer wg.Done()
			autoTaskEnabled = taskExists(taskAutoUpdate)
		}()
		managers := detectManagers()
		wg.Wait()

		app.mu.Lock()
		app.status = StatusResponse{
			Admin:           isAdmin(),
			StateDir:        dir,
			Managers:        managers,
			StartupEnabled:  startupEnabled,
			AutoTaskEnabled: autoTaskEnabled,
			Settings:        state,
		}
		app.statusFetchedAt = time.Now()
		app.statusLoading = false
		app.statusErr = ""
		app.mu.Unlock()
		appLog("Status refresh completed.")
	}()
}

func (app *App) statusSnapshot() StatusResponse {
	app.mu.RLock()
	status := app.status
	loading := app.statusLoading
	fetchedAt := app.statusFetchedAt
	errText := app.statusErr
	app.mu.RUnlock()

	if status.StateDir == "" {
		status.Settings = loadState()
		status.StateDir, _ = stateDir()
		status.Admin = isAdmin()
	}
	if status.Managers == nil {
		status.Managers = map[string]ManagerStatus{}
	}
	status.AsyncSnapshot = asyncSnapshot(loading, fetchedAt, errText)
	return status
}
