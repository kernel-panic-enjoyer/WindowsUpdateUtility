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
		ctx = withLogMetadata(ctx, logMetadata{PackageKey: key, Manager: manager})
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
			app.refreshInventorySyncContext(ctx, "install job "+jobID)
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
		ctx = withLogMetadata(ctx, logMetadata{PackageKey: manager, Manager: manager})
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
			app.refreshStatusSyncContext(ctx, "manager install job "+jobID)
			app.refreshInventorySyncContext(ctx, "manager install job "+jobID)
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
	pkg := app.packageForUpdateContext(app.rootContext(), manager, id)
	pkg.AllowUnknownVersionUpdate = options.AllowUnknownVersion
	pkg.AllowPinnedUpdate = options.AllowPinned
	if pkg.Key == "" {
		pkg.Key = packageKey(manager, id)
	}
	if !packageHasExactStoreUpdateTarget(pkg) {
		result := validationCommandResult("update", fmt.Errorf("%s has no exact verified Store update target", pkg.Key))
		return app.startRejectedUpdateJob(pkg, result)
	}
	if !packageHasFreshStoreAvailableAssessment(pkg) {
		result := validationCommandResult("update", fmt.Errorf("%s requires a fresh available Store assessment before updating", pkg.Key))
		return app.startRejectedUpdateJob(pkg, result)
	}
	return app.startUpdatePackagesOperation(jobTypeUpdate, updateJobModeSelected, []Package{pkg})
}

func (app *App) startRejectedUpdateJob(pkg Package, result CommandResult) OperationJobStatus {
	return app.startOperationJobWithPackageSnapshot(jobTypeUpdate, updateJobModeSelected, 1, []string{pkg.Key}, []Package{pkg}, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = pkg.Key
			status.CurrentPackage = updateJobPackageName(pkg)
			status.Results = []UpdateResult{{Key: pkg.Key, Result: result}}
			status.Result = &result
			status.State = jobStateFailed
			status.Notice = "Update not started. " + result.Stderr
		})
	})
}

func (app *App) startBulkUpdateJob(packageKeys []string, options UpdateOptions) (OperationJobStatus, error) {
	packages, mode, err := app.updateJobPackagesContext(app.rootContext(), packageKeys, options)
	if err != nil {
		return OperationJobStatus{}, err
	}
	return app.startUpdatePackagesOperation(jobTypeUpdateAll, mode, packages), nil
}

func (app *App) startUpdatePackagesOperation(jobType, mode string, packages []Package) OperationJobStatus {
	keys := updateJobPackageKeys(packages)
	return app.startOperationJobWithPackageSnapshot(jobType, mode, len(packages), keys, packages, func(ctx context.Context, job *OperationJob) {
		for index, pkg := range packages {
			packageCtx := withLogMetadata(ctx, logMetadata{PackageKey: pkg.Key, Manager: pkg.Manager})
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.CurrentIndex = index + 1
				status.CurrentKey = pkg.Key
				status.CurrentPackage = updateJobPackageName(pkg)
			})
			if packageCtx.Err() != nil {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.CancelRequested = true
					status.State = jobStateCancelled
					status.Notice = "Update cancelled."
				})
				break
			}
			result := app.updatePackageForJob(packageCtx, job, pkg)
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.Results = append(status.Results, UpdateResult{Key: pkg.Key, Result: result})
				status.Result = &result
				if packageCtx.Err() != nil || result.Code == commandCancelledCode {
					status.CancelRequested = true
					status.State = jobStateCancelled
					status.Notice = "Update cancelled."
				}
			})
			if packageCtx.Err() != nil || result.Code == commandCancelledCode {
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
		var refreshErr error
		if ctx.Err() == nil {
			refreshErr = refreshInventoryAfterUpdateJob(ctx, app, packages)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if ctx.Err() != nil {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Update cancelled."
				return
			}
			if updateResultsAcceptedNotVerified(status.Results) {
				status.State = jobStateAcceptedNotVerified
				status.Notice = "Update accepted but final package state could not be verified. See Session Log for diagnostics."
				return
			}
			if notice := updateResultsFailureNotice(status.Results); notice != "" {
				status.State = jobStateFailed
				status.Notice = notice
				return
			}
			if notice := updateRefreshNotice(refreshErr); notice != "" {
				status.State = jobStateAcceptedNotVerified
				status.Notice = notice
				return
			}
			status.State = jobStateSucceeded
			status.Notice = "Update completed. Refreshing package status..."
		})
	})
}

func (app *App) updatePackageForJob(ctx context.Context, job *OperationJob, pkg Package) CommandResult {
	if pkg.Manager == managerStore && pkg.UpdateState != "" {
		return storeExactUpdateExecutor.ExecuteWithCallbacks(ctx, pkg, StoreExactUpdateCallbacks{
			Starting: func(StoreExactUpdateRequest) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateStarting
					status.Notice = "Starting exact Store update for " + updateJobPackageName(pkg) + "..."
				})
			},
			Accepted: func(StoreExactUpdateRequest, CommandResult) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateAccepted
					status.Notice = "Store update accepted for " + updateJobPackageName(pkg) + "."
				})
			},
			Verifying: func(StoreExactUpdateRequest) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateVerifying
					status.Notice = "Verifying exact Store update for " + updateJobPackageName(pkg) + "..."
				})
			},
		})
	}
	return app.updatePackageWithInventoryRetry(ctx, pkg)
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
		store, err := defaultStateStore()
		var scan ScanResult
		if err != nil {
			scan = ScanResult{Errors: []map[string]string{{"source": "state", "error": err.Error()}}}
		} else {
			scan = scanInstalledApplicationsWithStore(ctx, store)
		}
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
			app.refreshInventorySyncContext(ctx, "scan job "+jobID)
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
		app.refreshInventorySyncContext(ctx, "inventory refresh job "+jobID)
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
