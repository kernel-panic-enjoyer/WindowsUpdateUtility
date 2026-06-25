package updater

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationJobPanicDoesNotBlockQueue(t *testing.T) {
	app := testSessionApp()
	first := app.startOperationJob(jobTypeScan, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		panic("fixture panic")
	})
	second := app.startOperationJob(jobTypeInventoryRefresh, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.State = jobStateSucceeded
			status.Notice = "second job ran"
		})
	})

	secondStatus, ok := waitForOperationJobState(app, second.JobID, 2*time.Second)
	if !ok {
		t.Fatal("second job did not finish after first job panic")
	}
	if secondStatus.State != jobStateSucceeded {
		t.Fatalf("expected second job to succeed, got %#v", secondStatus)
	}
	firstStatus, ok := app.operationJobStatus(first.JobID)
	if !ok {
		t.Fatal("first job missing")
	}
	if firstStatus.State != jobStateFailed || firstStatus.Running || firstStatus.FinishedAt == "" {
		t.Fatalf("panic job was not finalized as failed: %#v", firstStatus)
	}
	if !strings.Contains(firstStatus.Error, "internal job panic") || !strings.Contains(firstStatus.Error, "fixture panic") {
		t.Fatalf("panic diagnostic not recorded: %#v", firstStatus)
	}
}

func TestOperationJobRetentionBoundsCompletedHistory(t *testing.T) {
	app := testSessionApp()
	var latest OperationJobStatus
	for i := 0; i < 80; i++ {
		index := i
		latest = app.startOperationJobWithPackageSnapshot(jobTypeUpdate, updateJobModeSelected, 1, []string{fmt.Sprintf("winget:App.%d", index)}, []Package{{
			Key:     fmt.Sprintf("winget:App.%d", index),
			Manager: managerWinget,
			ID:      fmt.Sprintf("App.%d", index),
			Name:    strings.Repeat("large package snapshot ", 100),
		}}, func(ctx context.Context, job *OperationJob) {
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.State = jobStateSucceeded
			})
		})
	}
	if _, ok := waitForOperationJobState(app, latest.JobID, 5*time.Second); !ok {
		t.Fatal("latest high-volume job did not finish")
	}
	statuses := app.operationJobsSnapshot()
	if len(statuses) > operationJobRecentHistoryLimit+1 {
		t.Fatalf("completed job history is not bounded: got %d jobs", len(statuses))
	}
	if statuses[len(statuses)-1].JobID != latest.JobID {
		t.Fatalf("latest job must be retained, got last=%s latest=%s", statuses[len(statuses)-1].JobID, latest.JobID)
	}
}

func TestShutdownCancelsRunningAndQueuedJobs(t *testing.T) {
	app := testSessionApp()
	started := make(chan struct{})
	first := app.startOperationJob(jobTypeScan, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		close(started)
		<-ctx.Done()
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CancelRequested = true
			status.State = jobStateCancelled
			status.Notice = "cancelled in test"
		})
	})
	second := app.startOperationJob(jobTypeInventoryRefresh, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.State = jobStateSucceeded
		})
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first job did not start")
	}
	app.requestShutdown("test")

	firstStatus, ok := app.operationJobStatus(first.JobID)
	if !ok || firstStatus.State != jobStateCancelled || firstStatus.Running {
		t.Fatalf("running job not cancelled by shutdown: ok=%t status=%#v", ok, firstStatus)
	}
	secondStatus, ok := app.operationJobStatus(second.JobID)
	if !ok || secondStatus.State != jobStateCancelled || secondStatus.Running {
		t.Fatalf("queued job not cancelled by shutdown: ok=%t status=%#v", ok, secondStatus)
	}
}

func TestShutdownCancelsStoreScanInProgress(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	oldGetter := inventoryGetter
	inventoryGetter = func(ctx context.Context) Inventory { return Inventory{} }
	defer func() { inventoryGetter = oldGetter }()

	oldScan := runStoreTransactionalScanForInventory
	started := make(chan struct{})
	var cancelled int32
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		close(started)
		<-ctx.Done()
		atomic.StoreInt32(&cancelled, 1)
		return StoreScanResult{}, ctx.Err()
	}
	defer func() { runStoreTransactionalScanForInventory = oldScan }()

	app := &App{storeBackgroundScanEnabled: true}
	app.refreshInventorySync("test")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background Store scan did not start")
	}
	app.requestShutdown("test")
	if atomic.LoadInt32(&cancelled) != 1 {
		t.Fatal("Store scan did not observe root cancellation")
	}
	if app.inventorySnapshot().StoreLoading {
		t.Fatal("store loading flag remained set after shutdown")
	}
}

func TestShutdownCancelsUpdateJobRefreshWait(t *testing.T) {
	app := testUpdateJobApp(t)
	oldRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		return CommandResult{OK: true, Command: "update " + pkg.ID}
	}
	refreshStarted := make(chan struct{})
	refreshInventoryAfterUpdateJob = func(ctx context.Context, app *App, packages []Package) error {
		close(refreshStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() {
		updatePackageRunner = oldRunner
		refreshInventoryAfterUpdateJob = oldRefresh
	}()

	status, err := app.startUpdateJob([]string{"winget:Git.Git"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh wait did not start")
	}
	app.requestShutdown("test")
	final, ok := app.operationJobStatus(status.JobID)
	if !ok || final.State != jobStateCancelled || final.Running {
		t.Fatalf("update job refresh wait was not cancelled: ok=%t status=%#v", ok, final)
	}
	app.requestShutdown("test again")
}
