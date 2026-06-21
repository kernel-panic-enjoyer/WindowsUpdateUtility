package updater

import (
	"context"
	"fmt"
	"net/http"
)

func commandJobNotice(action string, result CommandResult) string {
	if result.OK {
		return action + " completed."
	}
	if notice := updateFailureNotice(result); notice != "" {
		return action + " finished with errors. " + notice
	}
	return action + " finished with errors. See Session Log for full output."
}

func (app *App) startInstallJob(manager, id string) OperationJobStatus {
	key := packageKey(manager, id)
	return app.startOperationJob(jobTypeInstall, "", 1, []string{key}, func(ctx context.Context, job *OperationJob) {
		started := app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = key
			status.CurrentPackage = id
		})
		jobID := started.JobID
		result := installPackageContext(ctx, manager, id)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Result = &result
			status.Results = []UpdateResult{{Key: key, Result: result}}
			if ctx.Err() != nil || result.Code == commandCancelledCode {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Install cancelled."
				return
			}
			status.State = jobStateRefreshing
			status.RefreshStarted = true
			status.Notice = "Install command completed. Refreshing package status..."
		})
		if ctx.Err() == nil {
			app.refreshInventorySync("install job " + jobID)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if result.OK {
				status.State = jobStateSucceeded
			} else {
				status.State = jobStateFailed
			}
			status.Notice = commandJobNotice("Install", result)
		})
	})
}

func (app *App) startManagerInstallJob(manager string) OperationJobStatus {
	return app.startOperationJob(jobTypeManagerInstall, "", 1, []string{manager}, func(ctx context.Context, job *OperationJob) {
		started := app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = manager
			status.CurrentPackage = manager
		})
		jobID := started.JobID
		result := installManagerContext(ctx, manager)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Result = &result
			status.Results = []UpdateResult{{Key: manager, Result: result}}
			if ctx.Err() != nil || result.Code == commandCancelledCode {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Package manager install cancelled."
				return
			}
			status.State = jobStateRefreshing
			status.RefreshStarted = true
			status.Notice = "Package manager install action completed. Refreshing manager status..."
		})
		if ctx.Err() == nil {
			app.refreshStatusSync("manager install job " + jobID)
			app.refreshInventorySync("manager install job " + jobID)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if result.OK {
				status.State = jobStateSucceeded
			} else {
				status.State = jobStateFailed
			}
			status.Notice = commandJobNotice("Package manager install", result)
		})
	})
}

func (app *App) startSingleUpdateJob(manager, id string, options UpdateOptions) OperationJobStatus {
	pkg := app.packageForUpdate(manager, id)
	pkg.AllowUnknownVersionUpdate = options.AllowUnknownVersion
	pkg.AllowPinnedUpdate = options.AllowPinned
	if pkg.Key == "" {
		pkg.Key = packageKey(manager, id)
	}
	return app.startUpdatePackagesOperation(jobTypeUpdate, updateJobModeSelected, []Package{pkg})
}

func (app *App) startBulkUpdateJob(packageKeys []string, options UpdateOptions) (OperationJobStatus, error) {
	packages, mode, err := app.updateJobPackages(packageKeys, options)
	if err != nil {
		return OperationJobStatus{}, err
	}
	return app.startUpdatePackagesOperation(jobTypeUpdateAll, mode, packages), nil
}

func (app *App) startUpdatePackagesOperation(jobType, mode string, packages []Package) OperationJobStatus {
	keys := updateJobPackageKeys(packages)
	return app.startOperationJobWithPackageSnapshot(jobType, mode, len(packages), keys, packages, func(ctx context.Context, job *OperationJob) {
		for index, pkg := range packages {
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.CurrentIndex = index + 1
				status.CurrentKey = pkg.Key
				status.CurrentPackage = updateJobPackageName(pkg)
			})
			if ctx.Err() != nil {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.CancelRequested = true
					status.State = jobStateCancelled
					status.Notice = "Update cancelled."
				})
				break
			}
			result := app.updatePackageWithInventoryRetry(ctx, pkg)
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.Results = append(status.Results, UpdateResult{Key: pkg.Key, Result: result})
				status.Result = &result
				if ctx.Err() != nil || result.Code == commandCancelledCode {
					status.CancelRequested = true
					status.State = jobStateCancelled
					status.Notice = "Update cancelled."
				}
			})
			if ctx.Err() != nil || result.Code == commandCancelledCode {
				break
			}
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State != jobStateCancelled {
				status.State = jobStateRefreshing
				status.RefreshStarted = true
				status.Notice = "Update completed. Refreshing package status..."
			}
		})
		if ctx.Err() == nil {
			refreshInventoryAfterUpdateJob(app)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if notice := updateResultsFailureNotice(status.Results); notice != "" {
				status.State = jobStateFailed
				status.Notice = notice
				return
			}
			status.State = jobStateSucceeded
			status.Notice = "Update completed. Refreshing package status..."
		})
	})
}

func (app *App) startScanJob() OperationJobStatus {
	return app.startOperationJob(jobTypeScan, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		started := app.mutateOperationJob(job, func(status *OperationJobStatus) {})
		jobID := started.JobID
		if ctx.Err() != nil {
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Scan cancelled."
			})
			return
		}
		scan := scanInstalledApplications()
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Scan = &scan
			if ctx.Err() != nil {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Scan cancelled."
				return
			}
			status.State = jobStateRefreshing
			status.RefreshStarted = true
			status.Notice = "Application scan completed. Refreshing package status..."
		})
		if ctx.Err() == nil {
			app.refreshInventorySync("scan job " + jobID)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if len(scan.Errors) > 0 {
				status.State = jobStateFailed
				status.Notice = "Application scan completed with errors."
				return
			}
			status.State = jobStateSucceeded
			status.Notice = "Application scan completed."
		})
	})
}

func (app *App) startInventoryRefreshJob() OperationJobStatus {
	return app.startOperationJob(jobTypeInventoryRefresh, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		started := app.mutateOperationJob(job, func(status *OperationJobStatus) {})
		jobID := started.JobID
		if ctx.Err() != nil {
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Inventory refresh cancelled."
			})
			return
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.State = jobStateRefreshing
			status.RefreshStarted = true
			status.Notice = "Refreshing package status..."
		})
		app.refreshInventorySync("inventory refresh job " + jobID)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if ctx.Err() != nil {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Inventory refresh cancelled."
				return
			}
			status.State = jobStateSucceeded
			status.Notice = "Package status refreshed."
		})
	})
}

func jobAcceptedResponse(w http.ResponseWriter, status OperationJobStatus) {
	writeJSON(w, http.StatusAccepted, status)
}

func jobNotFoundError(id string) string {
	if id == "" {
		return "job_id is required"
	}
	return fmt.Sprintf("job %s was not found", id)
}
