package updater

import (
	"context"
	"errors"
	"fmt"
)

const updateJobModeAll = "all"
const updateJobModeSelected = "selected"

var errUpdateJobRunning = errors.New("an update job is already running")
var errNoUpdateCandidates = errors.New("no updateable packages found")

var refreshInventoryAfterUpdateJob = func(app *App) {
	app.refreshInventory(true)
}

type UpdateJobStatus struct {
	JobID           string         `json:"job_id,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	Running         bool           `json:"running"`
	CancelRequested bool           `json:"cancel_requested"`
	CurrentPackage  string         `json:"current_package,omitempty"`
	CurrentKey      string         `json:"current_key,omitempty"`
	PackageKeys     []string       `json:"package_keys,omitempty"`
	CurrentIndex    int            `json:"current_index"`
	Total           int            `json:"total"`
	Results         []UpdateResult `json:"results"`
	RefreshStarted  bool           `json:"refresh_started"`
	StartedAt       string         `json:"started_at,omitempty"`
	FinishedAt      string         `json:"finished_at,omitempty"`
	Notice          string         `json:"notice,omitempty"`
	Error           string         `json:"error,omitempty"`
}

type UpdateOptions struct {
	AllowUnknownVersion bool
	AllowPinned         bool
}

type UpdateJob struct {
	status   UpdateJobStatus
	packages []Package
	cancel   context.CancelFunc
}

func (app *App) startUpdateJob(packageKeys []string) (UpdateJobStatus, error) {
	return app.startUpdateJobWithOptions(packageKeys, UpdateOptions{})
}

func (app *App) startUpdateJobWithOptions(packageKeys []string, options UpdateOptions) (UpdateJobStatus, error) {
	app.updateJobMu.Lock()
	if app.updateJob != nil && app.updateJob.status.Running {
		status := cloneUpdateJobStatus(app.updateJob.status)
		app.updateJobMu.Unlock()
		return status, errUpdateJobRunning
	}
	app.updateJobMu.Unlock()

	packages, mode, err := app.updateJobPackages(packageKeys, options)
	if err != nil {
		return UpdateJobStatus{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	app.updateJobMu.Lock()
	defer app.updateJobMu.Unlock()
	if app.updateJob != nil && app.updateJob.status.Running {
		cancel()
		return cloneUpdateJobStatus(app.updateJob.status), errUpdateJobRunning
	}
	app.updateJobSeq++
	job := &UpdateJob{
		packages: packages,
		cancel:   cancel,
		status: UpdateJobStatus{
			JobID:   fmt.Sprintf("update-%d", app.updateJobSeq),
			Mode:    mode,
			Running: true,
			Total:   len(packages),
			PackageKeys: updateJobPackageKeys(
				packages,
			),
			StartedAt: utcNow(),
		},
	}
	app.updateJob = job
	appLog("Update job %s started in %s mode with %d package(s).", job.status.JobID, mode, len(packages))
	go app.runUpdateJob(ctx, job)
	return cloneUpdateJobStatus(job.status), nil
}

func cloneUpdateJobStatus(status UpdateJobStatus) UpdateJobStatus {
	if status.PackageKeys != nil {
		status.PackageKeys = append([]string(nil), status.PackageKeys...)
	}
	if status.Results != nil {
		status.Results = append([]UpdateResult(nil), status.Results...)
	}
	return status
}

func (app *App) runUpdateJob(ctx context.Context, job *UpdateJob) {
	for index, pkg := range job.packages {
		app.updateJobMu.Lock()
		if job.status.CancelRequested || ctx.Err() != nil {
			app.updateJobMu.Unlock()
			break
		}
		job.status.CurrentIndex = index + 1
		job.status.CurrentKey = pkg.Key
		job.status.CurrentPackage = updateJobPackageName(pkg)
		app.updateJobMu.Unlock()

		result := app.updatePackageWithInventoryRetry(ctx, pkg)

		app.updateJobMu.Lock()
		job.status.Results = append(job.status.Results, UpdateResult{Key: pkg.Key, Result: result})
		cancelled := ctx.Err() == context.Canceled || result.Code == commandCancelledCode
		if cancelled {
			job.status.CancelRequested = true
		}
		app.updateJobMu.Unlock()
		if cancelled {
			break
		}
	}

	refreshInventoryAfterUpdateJob(app)

	app.updateJobMu.Lock()
	job.status.Running = false
	job.status.FinishedAt = utcNow()
	job.status.RefreshStarted = true
	job.status.Notice = updateJobNotice(job.status)
	status := cloneUpdateJobStatus(job.status)
	app.updateJobMu.Unlock()

	appLog("Update job %s finished with %d/%d result(s).", status.JobID, len(status.Results), status.Total)
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

func (app *App) cancelUpdateJob() UpdateJobStatus {
	app.updateJobMu.Lock()
	defer app.updateJobMu.Unlock()
	if app.updateJob == nil {
		return UpdateJobStatus{}
	}
	if app.updateJob.status.Running {
		app.updateJob.status.CancelRequested = true
		app.updateJob.status.Notice = "Cancelling after current command stops..."
		if app.updateJob.cancel != nil {
			app.updateJob.cancel()
		}
		appLog("Update job %s cancellation requested.", app.updateJob.status.JobID)
	}
	return cloneUpdateJobStatus(app.updateJob.status)
}

func (app *App) updateJobStatus() UpdateJobStatus {
	app.updateJobMu.Lock()
	defer app.updateJobMu.Unlock()
	if app.updateJob == nil {
		return UpdateJobStatus{}
	}
	return cloneUpdateJobStatus(app.updateJob.status)
}
