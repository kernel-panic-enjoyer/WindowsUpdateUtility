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
	sessionToken       string
	listenHost         string
	listenPort         int
	bootstrapUsed      bool
	server             *http.Server
	mu                 sync.RWMutex
	inventory          Inventory
	inventoryLoading   bool
	inventoryQueued    bool
	inventoryRefreshID int64
	inventoryFetchedAt time.Time
	inventoryErr       string
	status             StatusResponse
	statusLoading      bool
	statusQueued       bool
	statusFetchedAt    time.Time
	statusErr          string
	jobsMu             sync.Mutex
	jobs               map[string]*OperationJob
	jobSeq             int64
	jobQueue           []string
	jobActive          bool
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
