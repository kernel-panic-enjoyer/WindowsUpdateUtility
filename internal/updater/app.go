package updater

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const gracefulShutdownTimeout = 5 * time.Second

type InventoryResponse struct {
	Inventory
	AsyncSnapshot
	// StoreLoading is true while the (slow) Microsoft Store update scan is
	// still running in the background after the fast managers (winget, choco)
	// have already been returned. The frontend keeps polling and shows a
	// per-Store loading indicator while this is true.
	StoreLoading bool `json:"store_loading,omitempty"`
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
	token         string
	sessionToken  string
	listenHost    string
	listenPort    int
	bootstrapUsed bool
	server        *http.Server
	mu            sync.RWMutex
	// inventory is the immutable manager/native cache. Published Store
	// assessments are overlaid only onto deep-copied effective snapshots.
	inventory          Inventory
	inventoryLoading   bool
	inventoryQueued    bool
	inventoryRefreshID int64
	inventoryFetchedAt time.Time
	inventoryErr       string
	// Microsoft Store update scan runs in the background so it never blocks the
	// fast managers. storeScanLoading reports an in-flight background scan;
	// scan timestamps are split so successful publications use the normal
	// cooldown while failed/unpublished scans use a shorter retry backoff.
	// storeBackgroundScanEnabled is set only on the production App so unit tests
	// (which stub inventoryGetter) never spawn real Store scans.
	storeScanLoading           bool
	storeScanQueued            bool
	storeScanLastAttemptAt     time.Time
	storeScanLastPublishedAt   time.Time
	storeScanLastFailureAt     time.Time
	storeBackgroundScanEnabled bool
	status                     StatusResponse
	statusLoading              bool
	statusQueued               bool
	statusFetchedAt            time.Time
	statusErr                  string
	jobsMu                     sync.Mutex
	jobs                       map[string]*OperationJob
	jobSeq                     int64
	jobQueue                   []string
	jobActive                  bool
	lifecycleMu                sync.Mutex
	rootCtx                    context.Context
	rootCancel                 context.CancelFunc
	shuttingDown               bool
	backgroundWg               sync.WaitGroup
	shutdownOnce               sync.Once
	shutdownCleanupMu          sync.Mutex
	shutdownCleanups           []func()
}

func (app *App) ensureRootContextLocked() context.Context {
	if app.rootCtx == nil {
		app.rootCtx, app.rootCancel = context.WithCancel(context.Background())
	}
	return app.rootCtx
}

func (app *App) isShuttingDown() bool {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	return app.shuttingDown
}

func (app *App) startBackgroundWork(name string, run func(context.Context)) bool {
	app.lifecycleMu.Lock()
	if app.shuttingDown {
		app.lifecycleMu.Unlock()
		appLog("Skipping %s because shutdown is in progress.", name)
		return false
	}
	ctx := app.ensureRootContextLocked()
	app.backgroundWg.Add(1)
	app.lifecycleMu.Unlock()

	go func() {
		defer app.backgroundWg.Done()
		run(ctx)
	}()
	return true
}

func (app *App) rootContext() context.Context {
	app.lifecycleMu.Lock()
	defer app.lifecycleMu.Unlock()
	return app.ensureRootContextLocked()
}

func (app *App) beginShutdown() {
	app.lifecycleMu.Lock()
	app.shuttingDown = true
	if app.rootCancel != nil {
		app.rootCancel()
	}
	app.lifecycleMu.Unlock()
	app.cancelOperationJobsForShutdown()
}

func (app *App) waitForBackgroundWork(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		app.backgroundWg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (app *App) addShutdownCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	app.shutdownCleanupMu.Lock()
	defer app.shutdownCleanupMu.Unlock()
	app.shutdownCleanups = append(app.shutdownCleanups, cleanup)
}

func (app *App) runShutdownCleanups() {
	app.shutdownCleanupMu.Lock()
	cleanups := append([]func(){}, app.shutdownCleanups...)
	app.shutdownCleanups = nil
	app.shutdownCleanupMu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		func(cleanup func()) {
			defer func() {
				if recovered := recover(); recovered != nil {
					appLog("Shutdown cleanup failed: %v", recovered)
				}
			}()
			cleanup()
		}(cleanups[i])
	}
}

func (app *App) requestShutdown(source string) {
	app.shutdownOnce.Do(func() {
		appLog("%s quit requested.", source)
		app.beginShutdown()
		if !app.waitForBackgroundWork(gracefulShutdownTimeout) {
			appLog("Shutdown timed out waiting for background work.")
		}
		app.runShutdownCleanups()
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
