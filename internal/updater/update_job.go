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
var errNoUpdateCandidates = errors.New("no updateable packages found")

var errStoreScanRefreshTimeout = errors.New("timed out waiting for Microsoft Store scan to finish")

var refreshInventoryAfterUpdateJob = func(ctx context.Context, app *App, packages []Package) error {
	app.refreshInventorySyncContext(ctx, "update job")
	if updateJobTouchesStore(packages) {
		if !app.waitForStoreScanIdle(ctx, 12*time.Minute) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			appLog("Microsoft Store scan did not finish before update job refresh timeout.")
			return errStoreScanRefreshTimeout
		}
	}
	return nil
}

type UpdateJobStatus = OperationJobStatus

type UpdateOptions struct {
	AllowUnknownVersion bool
	AllowPinned         bool
}

func (app *App) startUpdateJob(packageKeys []string) (UpdateJobStatus, error) {
	return app.startUpdateJobWithOptions(packageKeys, UpdateOptions{})
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
	if notice := updateResultsFailureNotice(status.Results); notice != "" {
		return notice
	}
	return "Update completed. Refreshing package status..."
}

func updateJobTouchesStore(packages []Package) bool {
	for _, pkg := range packages {
		if pkg.Manager == managerStore {
			return true
		}
	}
	return false
}

func updateRefreshNotice(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errStoreScanRefreshTimeout) {
		return "Update accepted, but Microsoft Store status is still refreshing. See Session Log for diagnostics."
	}
	return fmt.Sprintf("Update refresh did not complete: %s", err)
}

func (app *App) cancelUpdateJob() UpdateJobStatus {
	status := app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
	if status.JobID == "" {
		return UpdateJobStatus{}
	}
	cancelled, ok := app.cancelOperationJob(status.JobID)
	if !ok {
		return UpdateJobStatus{}
	}
	return cancelled
}

func (app *App) updateJobStatus() UpdateJobStatus {
	return app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
}
