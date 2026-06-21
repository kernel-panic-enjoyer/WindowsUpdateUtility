package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateJobQueuesConcurrentStarts(t *testing.T) {
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	status, err := app.startUpdateJob(nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != jobStateQueued || status.Total != 2 {
		t.Fatalf("unexpected initial job status: %#v", status)
	}
	if len(status.PackageKeys) != 2 || status.PackageKeys[0] != "winget:Git.Git" || status.PackageKeys[1] != "choco:gh" {
		t.Fatalf("unexpected job package keys: %#v", status.PackageKeys)
	}

	next, err := app.startUpdateJob(nil)
	if err != nil {
		t.Fatalf("expected concurrent update job to queue, got %v", err)
	}
	if next.JobID == status.JobID || next.State != jobStateQueued {
		t.Fatalf("expected second queued update job, first=%#v second=%#v", status, next)
	}
	app.cancelOperationJob(status.JobID)
	app.cancelOperationJob(next.JobID)
	waitForUpdateJobStopped(t, app)
}

func TestUpdateJobStatusReturnsIndependentSlices(t *testing.T) {
	original := UpdateJobStatus{
		JobID:       "update-1",
		Running:     true,
		PackageKeys: []string{"winget:Git.Git"},
		Results: []UpdateResult{{
			Key:    "winget:Git.Git",
			Result: CommandResult{OK: true, Command: "winget upgrade Git.Git"},
		}},
	}

	status := cloneUpdateJobStatus(original)
	status.PackageKeys[0] = "winget:Mutated.App"
	status.Results[0].Key = "winget:Mutated.App"
	status.Results[0].Result.Command = "mutated"

	next := cloneUpdateJobStatus(original)
	if next.PackageKeys[0] != "winget:Git.Git" {
		t.Fatalf("package key slice aliases internal job state: %#v", next.PackageKeys)
	}
	if next.Results[0].Key != "winget:Git.Git" || next.Results[0].Result.Command != "winget upgrade Git.Git" {
		t.Fatalf("results slice aliases internal job state: %#v", next.Results)
	}
}

func TestUpdateJobRejectsSelectedUnknownVersionPackage(t *testing.T) {
	app := testUpdateJobApp()
	_, err := app.startUpdateJob([]string{"winget:Vendor.Unknown"})
	if err == nil || !strings.Contains(err.Error(), "requires an explicit global unknown-version update choice") {
		t.Fatalf("expected selected unknown-version package to be rejected, got %v", err)
	}
}

func TestUpdateJobAllowsSelectedUnknownVersionPackageWithGlobalOption(t *testing.T) {
	app := testUpdateJobApp()
	packages, mode, err := app.updateJobPackages([]string{"winget:Vendor.Unknown"}, UpdateOptions{AllowUnknownVersion: true, AllowPinned: true})
	if err != nil {
		t.Fatal(err)
	}
	if mode != updateJobModeSelected || len(packages) != 1 || packages[0].Key != "winget:Vendor.Unknown" {
		t.Fatalf("unexpected selected packages: mode=%q packages=%#v", mode, packages)
	}
	if !packages[0].AllowUnknownVersionUpdate || !packages[0].AllowPinnedUpdate {
		t.Fatalf("expected global update options on selected package, got %#v", packages[0])
	}
}

func TestUpdateJobRejectsSelectedPinnedPackage(t *testing.T) {
	app := testUpdateJobApp()
	app.mu.Lock()
	app.inventory.Packages = append(app.inventory.Packages, Package{
		Key:              "winget:Vendor.Pinned",
		Manager:          managerWinget,
		ID:               "Vendor.Pinned",
		Name:             "Pinned App",
		Version:          "1.0",
		AvailableVersion: "2.0",
		UpdateAvailable:  true,
		UpdateSupported:  true,
		Pinned:           true,
	})
	app.mu.Unlock()

	_, _, err := app.updateJobPackages([]string{"winget:Vendor.Pinned"}, UpdateOptions{})
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("expected pinned package rejection, got %v", err)
	}
}

func TestUpdateJobAllowsSelectedPinnedPackageWithGlobalOption(t *testing.T) {
	app := testUpdateJobApp()
	app.mu.Lock()
	app.inventory.Packages = append(app.inventory.Packages, Package{
		Key:              "winget:Vendor.Pinned",
		Manager:          managerWinget,
		ID:               "Vendor.Pinned",
		Name:             "Pinned App",
		Version:          "1.0",
		AvailableVersion: "2.0",
		UpdateAvailable:  true,
		UpdateSupported:  true,
		Pinned:           true,
	})
	app.mu.Unlock()

	packages, _, err := app.updateJobPackages([]string{"winget:Vendor.Pinned"}, UpdateOptions{AllowPinned: true})
	if err != nil {
		t.Fatalf("expected pinned package with override to be allowed, got %v", err)
	}
	if len(packages) != 1 || !packages[0].Pinned || !packages[0].AllowPinnedUpdate {
		t.Fatalf("expected pinned package metadata and override, got %#v", packages)
	}
}

func TestUpdateJobStatusKeepsPackageSnapshotAndOverrides(t *testing.T) {
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	app.mu.Lock()
	app.inventory.Packages = append(app.inventory.Packages, Package{
		Key:              "winget:Vendor.Pinned",
		Manager:          managerWinget,
		ID:               "Vendor.Pinned",
		Name:             "Pinned App",
		Version:          "1.0",
		AvailableVersion: "2.0",
		UpdateAvailable:  true,
		UpdateSupported:  true,
		Pinned:           true,
	})
	app.mu.Unlock()

	status, err := app.startUpdateJobWithOptions([]string{"winget:Vendor.Pinned"}, UpdateOptions{AllowPinned: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.cancelOperationJob(status.JobID)
	if !status.AllowPinned || len(status.Packages) != 1 || !status.Packages[0].Pinned || status.Packages[0].Name != "Pinned App" {
		t.Fatalf("expected durable package snapshot and override in job status, got %#v", status)
	}
}

func TestUpdateJobBulkIncludesUnknownVersionWithGlobalOption(t *testing.T) {
	app := testUpdateJobApp()
	packages, mode, err := app.updateJobPackages(nil, UpdateOptions{AllowUnknownVersion: true})
	if err != nil {
		t.Fatal(err)
	}
	if mode != updateJobModeAll || len(packages) != 3 {
		t.Fatalf("expected all updateable packages including unknown version, mode=%q packages=%#v", mode, packages)
	}
	if packages[2].Key != "winget:Vendor.Unknown" || !packages[2].AllowUnknownVersionUpdate {
		t.Fatalf("expected unknown package with global option applied, got %#v", packages)
	}
}

func TestUpdateJobCancelStopsQueuedPackages(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	var calls int
	var mu sync.Mutex
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		mu.Lock()
		calls++
		mu.Unlock()
		once.Do(func() { close(started) })
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not start first package")
	}
	cancelStatus := app.cancelUpdateJob()
	if !cancelStatus.CancelRequested {
		t.Fatalf("expected cancel requested status, got %#v", cancelStatus)
	}
	status := waitForUpdateJobStopped(t, app)
	if !status.CancelRequested || status.Running || status.RefreshStarted {
		t.Fatalf("unexpected cancelled status: %#v", status)
	}
	if len(status.Results) != 1 || status.Results[0].Result.Code != commandCancelledCode {
		t.Fatalf("expected one cancelled result, got %#v", status.Results)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected queued package to be skipped after cancel, calls=%d", calls)
	}
}

func TestUpdateJobStatusEndpointReportsProgress(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return CommandResult{OK: true, Command: id}
		case <-ctx.Done():
			return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
		}
	})
	defer restore()

	app := testUpdateJobApp()
	app.sessionToken = "test-session"
	if _, err := app.startUpdateJob([]string{"winget:Git.Git"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not report progress")
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/update-all/status", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var status UpdateJobStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.CurrentPackage != "Git" || status.CurrentIndex != 1 || status.Total != 1 {
		t.Fatalf("unexpected progress status: %#v", status)
	}
	if len(status.PackageKeys) != 1 || status.PackageKeys[0] != "winget:Git.Git" {
		t.Fatalf("expected job package keys in status, got %#v", status.PackageKeys)
	}
	close(release)
	waitForUpdateJobStopped(t, app)
}

func TestUpdateJobPassesPackageMetadataToRunner(t *testing.T) {
	var got Package
	oldRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		got = pkg
		return CommandResult{OK: true, Command: pkg.ID}
	}
	refreshInventoryAfterUpdateJob = func(app *App) {}
	defer func() {
		updatePackageRunner = oldRunner
		refreshInventoryAfterUpdateJob = oldRefresh
	}()

	app := &App{inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             "store:Vendor.App_abc123",
		Manager:         managerStore,
		ID:              "Vendor.App_abc123",
		Name:            "Vendor App",
		Match:           "Vendor.App",
		UpdateAvailable: true,
		UpdateSupported: true,
		ActionBackend:   backendStoreCLIResolved,
	}}}}}
	if _, err := app.startUpdateJob([]string{"store:Vendor.App_abc123"}); err != nil {
		t.Fatal(err)
	}
	waitForUpdateJobStopped(t, app)

	if got.Manager != managerStore || got.ID != "Vendor.App_abc123" || got.Match != "Vendor.App" || got.ActionBackend != backendStoreCLIResolved {
		t.Fatalf("expected full package metadata in update runner, got %#v", got)
	}
}

func TestUpdatePackageWithInventoryRetryUsesFreshMetadata(t *testing.T) {
	var calls []Package
	oldRunner := updatePackageRunner
	oldRefresher := updateRetryInventoryRefresher
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		calls = append(calls, pkg)
		if pkg.ID == "Fresh.App" {
			return CommandResult{OK: true, Command: "update " + pkg.ID, Stdout: "updated"}
		}
		return CommandResult{Code: 1, Command: "update " + pkg.ID, Stderr: "No installed package found matching input criteria."}
	}
	updateRetryInventoryRefresher = func(app *App, reason string) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:             "winget:Fresh.App",
			Manager:         managerWinget,
			ID:              "Fresh.App",
			Name:            "Example App",
			UpdateAvailable: true,
			UpdateSupported: true,
		}}}}
	}
	defer func() {
		updatePackageRunner = oldRunner
		updateRetryInventoryRefresher = oldRefresher
	}()

	app := &App{}
	result := app.updatePackageWithInventoryRetry(context.Background(), Package{Manager: managerWinget, ID: "Old.App", Name: "Example App", UpdateAvailable: true, UpdateSupported: true})

	if !result.OK || len(calls) != 2 || calls[1].ID != "Fresh.App" || !strings.Contains(result.Command, "fresh inventory retry") {
		t.Fatalf("expected retry with fresh package metadata, calls=%#v result=%#v", calls, result)
	}
}

func TestUpdatePackageWithInventoryRetryTreatsFreshNoUpdateAsCurrent(t *testing.T) {
	calls := 0
	oldRunner := updatePackageRunner
	oldRefresher := updateRetryInventoryRefresher
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		calls++
		return CommandResult{Code: 1, Command: "update " + pkg.ID, Stderr: "No installed package found matching input criteria."}
	}
	updateRetryInventoryRefresher = func(app *App, reason string) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:             "winget:Old.App",
			Manager:         managerWinget,
			ID:              "Old.App",
			Name:            "Example App",
			UpdateAvailable: false,
			UpdateSupported: true,
		}}}}
	}
	defer func() {
		updatePackageRunner = oldRunner
		updateRetryInventoryRefresher = oldRefresher
	}()

	app := &App{}
	result := app.updatePackageWithInventoryRetry(context.Background(), Package{Manager: managerWinget, ID: "Old.App", Name: "Example App", UpdateAvailable: true, UpdateSupported: true})

	if !result.OK || calls != 1 || !strings.Contains(result.Stdout, "no longer reports an available update") {
		t.Fatalf("expected fresh no-update inventory to be treated as current, calls=%d result=%#v", calls, result)
	}
}

func TestUpdatePackageWithInventoryRetrySkipsNonTargetFailures(t *testing.T) {
	refreshes := 0
	oldRunner := updatePackageRunner
	oldRefresher := updateRetryInventoryRefresher
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		return CommandResult{Code: 5, Command: "update " + pkg.ID, Stderr: "Access is denied."}
	}
	updateRetryInventoryRefresher = func(app *App, reason string) Inventory {
		refreshes++
		return Inventory{}
	}
	defer func() {
		updatePackageRunner = oldRunner
		updateRetryInventoryRefresher = oldRefresher
	}()

	app := &App{}
	result := app.updatePackageWithInventoryRetry(context.Background(), Package{Manager: managerWinget, ID: "Old.App", Name: "Example App", UpdateAvailable: true, UpdateSupported: true})

	if result.OK || refreshes != 0 {
		t.Fatalf("non-target failures should not trigger inventory retry, refreshes=%d result=%#v", refreshes, result)
	}
}

func TestRefreshInventorySyncPreventsStaleAsyncOverwrite(t *testing.T) {
	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var callMu sync.Mutex
	calls := 0
	inventoryGetter = func() Inventory {
		callMu.Lock()
		calls++
		call := calls
		callMu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
			return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Name: "stale"}}}}
		}
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Name: "fresh"}}}}
	}

	app := &App{}
	app.refreshInventory(true)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("async inventory refresh did not start")
	}

	syncInventory := app.refreshInventorySync("test")
	if len(syncInventory.Packages) != 1 || syncInventory.Packages[0].Name != "fresh" {
		t.Fatalf("expected synchronous refresh to return fresh inventory, got %#v", syncInventory.Packages)
	}
	close(releaseFirst)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.mu.RLock()
		packages := append([]Package(nil), app.inventory.Packages...)
		loading := app.inventoryLoading
		app.mu.RUnlock()
		if !loading && len(packages) == 1 && packages[0].Name == "fresh" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	t.Fatalf("stale async refresh overwrote cache or left loading active: loading=%v packages=%#v", app.inventoryLoading, app.inventory.Packages)
}

func TestRefreshInventorySyncRunsQueuedForcedRefresh(t *testing.T) {
	oldGetter := inventoryGetter
	defer func() { inventoryGetter = oldGetter }()

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	var callMu sync.Mutex
	calls := 0
	inventoryGetter = func() Inventory {
		callMu.Lock()
		calls++
		call := calls
		callMu.Unlock()
		if call == 1 {
			close(syncStarted)
			<-releaseSync
			return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Name: "sync"}}}}
		}
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{Name: "queued"}}}}
	}

	app := &App{}
	syncDone := make(chan Inventory, 1)
	go func() {
		syncDone <- app.refreshInventorySync("test")
	}()
	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("synchronous inventory refresh did not start")
	}

	app.refreshInventory(true)
	close(releaseSync)

	var syncInventory Inventory
	select {
	case syncInventory = <-syncDone:
	case <-time.After(time.Second):
		t.Fatal("synchronous inventory refresh did not finish")
	}
	if len(syncInventory.Packages) != 1 || syncInventory.Packages[0].Name != "sync" {
		t.Fatalf("expected synchronous refresh to return sync inventory, got %#v", syncInventory.Packages)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.mu.RLock()
		packages := append([]Package(nil), app.inventory.Packages...)
		loading := app.inventoryLoading
		app.mu.RUnlock()
		if !loading && len(packages) == 1 && packages[0].Name == "queued" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	t.Fatalf("queued forced refresh did not update cache: loading=%v packages=%#v", app.inventoryLoading, app.inventory.Packages)
}

func TestUpdateJobKeepsRunningUntilRefreshStarts(t *testing.T) {
	refreshEntered := make(chan struct{})
	releaseRefresh := make(chan struct{})
	restore := replaceUpdateJobHooksWithRefresh(
		func(ctx context.Context, manager, id string) CommandResult {
			return CommandResult{OK: true, Command: id}
		},
		func(app *App) {
			close(refreshEntered)
			<-releaseRefresh
		},
	)
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob([]string{"winget:Git.Git"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("update job did not start inventory refresh")
	}
	if status := app.updateJobStatus(); !status.Running || !status.RefreshStarted || status.State != jobStateRefreshing {
		t.Fatalf("job should stay running while refresh is active, got %#v", status)
	}
	close(releaseRefresh)
	status := waitForUpdateJobStopped(t, app)
	if status.Running || !status.RefreshStarted {
		t.Fatalf("expected stopped job with refresh started, got %#v", status)
	}
}

func TestUpdateJobValidatesConcurrentQueuedStart(t *testing.T) {
	restore := replaceUpdateJobHooks(func(ctx context.Context, manager, id string) CommandResult {
		<-ctx.Done()
		return CommandResult{Code: commandCancelledCode, Command: id, Stderr: "Cancelled."}
	})
	defer restore()

	app := testUpdateJobApp()
	if _, err := app.startUpdateJob(nil); err != nil {
		t.Fatal(err)
	}
	_, err := app.startUpdateJob([]string{"not-a-valid-key"})
	if err == nil || !strings.Contains(err.Error(), "package key must be manager:id") {
		t.Fatalf("expected invalid package key validation, got %v", err)
	}
	app.cancelUpdateJob()
	waitForUpdateJobStopped(t, app)
}
