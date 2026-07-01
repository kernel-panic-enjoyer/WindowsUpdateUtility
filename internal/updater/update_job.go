package updater

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const updateJobModeAll = "all"
const updateJobModeSelected = "selected"

var errUpdateJobRunning = errors.New("an update job is already running")
var errNoUpdateCandidates = errors.New("no updatable packages found")

var errUpdateJobStoreScanRefreshTimeout = errors.New("timed out waiting for Microsoft Store scan to finish")

var refreshInventoryAfterUpdateJob = func(ctx context.Context, app *App, updatedPackages []Package) error {
	app.refreshInventorySyncContext(ctx, "update job")
	if !updateJobIncludesStorePackage(updatedPackages) {
		return nil
	}
	if !app.waitForStoreScanIdle(ctx, 12*time.Minute) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		appLog("Microsoft Store scan did not finish before update job refresh timeout.")
		return errUpdateJobStoreScanRefreshTimeout
	}
	return nil
}

type UpdateJobStatus = OperationJobStatus

type UpdateOptions struct {
	AllowUnknownVersion bool
	AllowPinned         bool
}

func (app *App) startUpdateJob(packageKeys []string) (UpdateJobStatus, error) {
	return app.startBulkUpdateJob(packageKeys, UpdateOptions{})
}

func (app *App) startUpdateJobWithOptions(packageKeys []string, options UpdateOptions) (UpdateJobStatus, error) {
	return app.startBulkUpdateJob(packageKeys, options)
}

func cloneUpdateJobStatus(status UpdateJobStatus) UpdateJobStatus {
	return cloneOperationJobStatus(status)
}

func updateJobNotice(status UpdateJobStatus) string {
	if status.CancelRequested {
		return "Update cancelled. Refreshing package status..."
	}
	if failureNotice := updateResultsFailureNotice(status.Results); failureNotice != "" {
		return failureNotice
	}
	return "Update completed. Refreshing package status..."
}

func updateJobIncludesStorePackage(updatedPackages []Package) bool {
	for _, updatedPackage := range updatedPackages {
		if updatedPackage.Manager == managerStore {
			return true
		}
	}
	return false
}

func updateRefreshNotice(refreshErr error) string {
	if refreshErr == nil {
		return ""
	}
	if errors.Is(refreshErr, errUpdateJobStoreScanRefreshTimeout) {
		return "Update accepted, but Microsoft Store status is still refreshing. See Session Log for diagnostics."
	}
	return fmt.Sprintf("Update refresh did not complete: %s", refreshErr)
}

func (app *App) cancelUpdateJob() UpdateJobStatus {
	latestStatus := app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
	if latestStatus.JobID == "" {
		return UpdateJobStatus{}
	}
	cancelledStatus, ok := app.cancelOperationJob(latestStatus.JobID)
	if !ok {
		return UpdateJobStatus{}
	}
	return cancelledStatus
}

func (app *App) updateJobStatus() UpdateJobStatus {
	return app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
}
