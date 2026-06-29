package updater

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func replaceUpdateJobHooks(runner func(context.Context, string, string) CommandResult) func() {
	return replaceUpdateJobHooksWithRefresh(runner, func(ctx context.Context, app *App, packages []Package) error { return nil })
}

func replaceUpdateJobHooksWithRefresh(runner func(context.Context, string, string) CommandResult, refresh func(context.Context, *App, []Package) error) func() {
	oldRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	oldEligible := elevatedPackageUpdateBatchEligible
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		return runner(ctx, pkg.Manager, pkg.ID)
	}
	refreshInventoryAfterUpdateJob = refresh
	elevatedPackageUpdateBatchEligible = func(Package) bool { return false }
	return func() {
		updatePackageRunner = oldRunner
		refreshInventoryAfterUpdateJob = oldRefresh
		elevatedPackageUpdateBatchEligible = oldEligible
	}
}

func replaceBulkUpdateBatchHooks(
	eligible func(Package) bool,
	batchRunner func(context.Context, []Package, func(int, Package)) ([]UpdateResult, CommandResult),
	singleRunner func(context.Context, string, string) CommandResult,
) func() {
	oldEligible := elevatedPackageUpdateBatchEligible
	oldBatchRunner := elevatedPackageUpdateBatchRunner
	oldSingleRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	elevatedPackageUpdateBatchEligible = eligible
	elevatedPackageUpdateBatchRunner = batchRunner
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		return singleRunner(ctx, pkg.Manager, pkg.ID)
	}
	refreshInventoryAfterUpdateJob = func(ctx context.Context, app *App, packages []Package) error { return nil }
	return func() {
		elevatedPackageUpdateBatchEligible = oldEligible
		elevatedPackageUpdateBatchRunner = oldBatchRunner
		updatePackageRunner = oldSingleRunner
		refreshInventoryAfterUpdateJob = oldRefresh
	}
}

func updatableTestPackage(manager, id, name string) Package {
	return Package{
		Key:              packageKey(manager, id),
		Manager:          manager,
		ID:               id,
		Name:             name,
		Version:          "1.0.0",
		AvailableVersion: "2.0.0",
		UpdateAvailable:  true,
		UpdateSupported:  true,
		CanUpdateNow:     true,
	}
}

func replacePackageActionHooks(
	runner func(context.Context, time.Duration, ...string) CommandResult,
	available func(string) bool,
) func() {
	oldRunner := packageActionCommandRunner
	oldAvailable := packageActionManagerAvailable
	oldWait := packageActionRetryWait
	packageActionCommandRunner = runner
	packageActionManagerAvailable = available
	packageActionRetryWait = func(ctx context.Context) bool { return ctx.Err() == nil }
	return func() {
		packageActionCommandRunner = oldRunner
		packageActionManagerAvailable = oldAvailable
		packageActionRetryWait = oldWait
	}
}

func packageActionTargetFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--id" && i+1 < len(args) {
			return args[i+1]
		}
	}
	for i, arg := range args {
		if (arg == "install" || arg == "update" || arg == "upgrade") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testSessionApp() *App {
	return &App{token: "test-token", sessionToken: "test-session"}
}

func addTestSessionCookie(app *App, request *http.Request) {
	if app.sessionToken == "" {
		app.sessionToken = "test-session"
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: app.sessionToken})
}

func authenticatedRequest(app *App, method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	addTestSessionCookie(app, request)
	return request
}

func testUpdateJobApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	return &App{inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{
		{Key: "winget:Git.Git", Manager: managerWinget, ID: "Git.Git", Name: "Git", UpdateAvailable: true, UpdateSupported: true},
		{Key: "choco:gh", Manager: managerChoco, ID: "gh", Name: "GitHub CLI", UpdateAvailable: true, UpdateSupported: true},
		{Key: "winget:Vendor.Unknown", Manager: managerWinget, ID: "Vendor.Unknown", Name: "Unknown App", Version: "Unknown", AvailableVersion: "1.2.0", UpdateAvailable: true, UpdateSupported: true, UnknownVersion: true},
	}}}}
}

func waitForUpdateJobStopped(t *testing.T, app *App) UpdateJobStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := app.updateJobStatus()
		if status.JobID != "" && operationJobComplete(status) {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("update job did not stop")
	return UpdateJobStatus{}
}

func waitForOperationJobState(app *App, id string, timeout time.Duration) (OperationJobStatus, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if status, ok := app.operationJobStatus(id); ok && operationJobComplete(status) {
			return status, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return OperationJobStatus{}, false
}

type memoryStateStore struct {
	mu         sync.Mutex
	state      State
	loadErr    error
	updateErr  error
	updateHook func(*State) error
}

func newMemoryStateStore(state State) *memoryStateStore {
	normalizeState(&state, nil)
	return &memoryStateStore{state: state}
}

func (store *memoryStateStore) Load(context.Context) (State, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadErr != nil {
		return State{}, store.loadErr
	}
	return store.state, nil
}

func (store *memoryStateStore) Update(ctx context.Context, mutate func(*State) error) (State, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if store.updateErr != nil {
		return State{}, store.updateErr
	}
	next := store.state
	if err := mutate(&next); err != nil {
		return State{}, err
	}
	if store.updateHook != nil {
		if err := store.updateHook(&next); err != nil {
			return State{}, err
		}
	}
	normalizeState(&next, nil)
	next.UpdatedAt = utcNow()
	store.state = next
	return next, nil
}
