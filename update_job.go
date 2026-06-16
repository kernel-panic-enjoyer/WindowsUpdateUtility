package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const updateJobModeAll = "all"
const updateJobModeSelected = "selected"

var errUpdateJobRunning = errors.New("an update job is already running")
var errNoUpdateCandidates = errors.New("no updateable packages found")

var updatePackageRunner = updatePackageContext
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

type UpdateJob struct {
	status   UpdateJobStatus
	packages []Package
	cancel   context.CancelFunc
}

func (app *App) startUpdateJob(packageKeys []string) (UpdateJobStatus, error) {
	app.updateJobMu.Lock()
	if app.updateJob != nil && app.updateJob.status.Running {
		status := app.updateJob.status
		app.updateJobMu.Unlock()
		return status, errUpdateJobRunning
	}
	app.updateJobMu.Unlock()

	packages, mode, err := app.updateJobPackages(packageKeys)
	if err != nil {
		return UpdateJobStatus{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	app.updateJobMu.Lock()
	defer app.updateJobMu.Unlock()
	if app.updateJob != nil && app.updateJob.status.Running {
		cancel()
		return app.updateJob.status, errUpdateJobRunning
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
	return job.status, nil
}

func (app *App) updateJobPackages(packageKeys []string) ([]Package, string, error) {
	app.mu.RLock()
	inventoryPackages := append([]Package(nil), app.inventory.Packages...)
	app.mu.RUnlock()

	byKey := map[string]Package{}
	for _, pkg := range inventoryPackages {
		key := normalizedJobPackageKey(pkg)
		if key != "" {
			pkg.Key = key
			byKey[key] = pkg
		}
	}

	if len(packageKeys) == 0 {
		var packages []Package
		seen := map[string]bool{}
		for _, pkg := range inventoryPackages {
			key := normalizedJobPackageKey(pkg)
			if key == "" || seen[key] || !pkg.UpdateAvailable || pkg.UpdateSupported == false {
				continue
			}
			pkg.Key = key
			packages = append(packages, pkg)
			seen[key] = true
		}
		if len(packages) == 0 {
			return nil, updateJobModeAll, errNoUpdateCandidates
		}
		return packages, updateJobModeAll, nil
	}

	var packages []Package
	seen := map[string]bool{}
	for _, key := range packageKeys {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized == "" {
			normalized = key
		}
		if seen[normalized] {
			continue
		}
		manager, id, err := splitPackageKey(normalized)
		if err != nil {
			return nil, updateJobModeSelected, err
		}
		pkg, ok := byKey[normalized]
		if !ok {
			pkg = Package{Key: normalized, Manager: manager, ID: id, Name: id, UpdateSupported: true}
		}
		if pkg.UpdateSupported == false {
			return nil, updateJobModeSelected, fmt.Errorf("%s does not support updates", normalized)
		}
		pkg.Key = normalized
		packages = append(packages, pkg)
		seen[normalized] = true
	}
	if len(packages) == 0 {
		return nil, updateJobModeSelected, errNoUpdateCandidates
	}
	return packages, updateJobModeSelected, nil
}

func updateJobPackageKeys(packages []Package) []string {
	keys := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Key != "" {
			keys = append(keys, pkg.Key)
		}
	}
	return keys
}

func normalizedJobPackageKey(pkg Package) string {
	if pkg.Key != "" {
		if normalized := normalizeAutoUpdatePackageKey(pkg.Key); normalized != "" {
			return normalized
		}
		return pkg.Key
	}
	if pkg.Manager == "" || pkg.ID == "" {
		return ""
	}
	return packageKey(pkg.Manager, pkg.ID)
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

		result := updatePackageRunner(ctx, pkg.Manager, pkg.ID)

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
	status := job.status
	app.updateJobMu.Unlock()

	appLog("Update job %s finished with %d/%d result(s).", status.JobID, len(status.Results), status.Total)
}

func updateJobPackageName(pkg Package) string {
	for _, value := range []string{pkg.Name, pkg.ID, pkg.Key} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "package"
}

func updateJobNotice(status UpdateJobStatus) string {
	if status.CancelRequested {
		return "Update cancelled. Refreshing package status..."
	}
	if notice := updateAllFailureNotice(status.Results); notice != "" {
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
	return app.updateJob.status
}

func (app *App) updateJobStatus() UpdateJobStatus {
	app.updateJobMu.Lock()
	defer app.updateJobMu.Unlock()
	if app.updateJob == nil {
		return UpdateJobStatus{}
	}
	return app.updateJob.status
}
