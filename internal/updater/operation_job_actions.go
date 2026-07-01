package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func commandResultJobNotice(actionName string, result CommandResult) string {
	if result.OK {
		return actionName + " completed."
	}
	if notice := updateFailureNotice(result); notice != "" {
		return actionName + " finished with errors. " + notice
	}
	return actionName + " finished with errors. See Session Log for full output."
}

func (app *App) startInstallJob(manager, packageID string) OperationJobStatus {
	jobPackageKey := packageKey(manager, packageID)
	return app.startOperationJob(jobTypeInstall, "", 1, []string{jobPackageKey}, func(ctx context.Context, job *OperationJob) {
		ctx = withLogMetadata(ctx, logMetadata{PackageKey: jobPackageKey, Manager: manager})
		startedStatus := app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = jobPackageKey
			status.CurrentPackage = packageID
		})
		result := installPackageContext(ctx, manager, packageID)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Result = &result
			status.Results = []UpdateResult{{Key: jobPackageKey, Result: result}}
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
			app.refreshInventorySyncContext(ctx, "install job "+startedStatus.JobID)
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
			status.Notice = commandResultJobNotice("Install", result)
		})
	})
}

func (app *App) startManagerInstallJob(managerName string) OperationJobStatus {
	return app.startOperationJob(jobTypeManagerInstall, "", 1, []string{managerName}, func(ctx context.Context, job *OperationJob) {
		ctx = withLogMetadata(ctx, logMetadata{PackageKey: managerName, Manager: managerName})
		startedStatus := app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = managerName
			status.CurrentPackage = managerName
		})
		result := installManagerContext(ctx, managerName)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Result = &result
			status.Results = []UpdateResult{{Key: managerName, Result: result}}
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
			app.refreshStatusSyncContext(ctx, "manager install job "+startedStatus.JobID)
			app.refreshInventorySyncContext(ctx, "manager install job "+startedStatus.JobID)
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
			status.Notice = commandResultJobNotice("Package manager install", result)
		})
	})
}

func (app *App) startSingleUpdateJob(manager, packageID string, options UpdateOptions) OperationJobStatus {
	targetPackage := app.packageForUpdateContext(app.rootContext(), manager, packageID)
	targetPackage.AllowUnknownVersionUpdate = options.AllowUnknownVersion
	targetPackage.AllowPinnedUpdate = options.AllowPinned
	if targetPackage.Key == "" {
		targetPackage.Key = packageKey(manager, packageID)
	}
	if !packageHasExactStoreUpdateTarget(targetPackage) {
		result := validationCommandResult("update", fmt.Errorf("%s has no exact verified Store update target", targetPackage.Key))
		return app.startRejectedUpdateJob(targetPackage, result)
	}
	if !packageHasFreshStoreAvailableAssessment(targetPackage) {
		result := validationCommandResult("update", fmt.Errorf("%s requires a fresh available Store assessment before updating", targetPackage.Key))
		return app.startRejectedUpdateJob(targetPackage, result)
	}
	return app.startUpdatePackagesOperation(jobTypeUpdate, updateJobModeSelected, []Package{targetPackage})
}

func (app *App) startRejectedUpdateJob(rejectedPackage Package, result CommandResult) OperationJobStatus {
	return app.startOperationJobWithPackageSnapshot(jobTypeUpdate, updateJobModeSelected, 1, []string{rejectedPackage.Key}, []Package{rejectedPackage}, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentKey = rejectedPackage.Key
			status.CurrentPackage = updateJobPackageName(rejectedPackage)
			status.Results = []UpdateResult{{Key: rejectedPackage.Key, Result: result}}
			status.Result = &result
			status.State = jobStateFailed
			status.Notice = "Update not started. " + result.Stderr
		})
	})
}

func (app *App) startBulkUpdateJob(packageKeys []string, options UpdateOptions) (OperationJobStatus, error) {
	updatePackages, updateMode, err := app.updateJobPackagesContext(app.rootContext(), packageKeys, options)
	if err != nil {
		return OperationJobStatus{}, err
	}
	return app.startUpdatePackagesOperation(jobTypeUpdateAll, updateMode, updatePackages), nil
}

func (app *App) startUpdatePackagesOperation(operationType, updateMode string, updatePackages []Package) OperationJobStatus {
	packageKeys := updateJobPackageKeys(updatePackages)
	return app.startOperationJobWithPackageSnapshot(operationType, updateMode, len(updatePackages), packageKeys, updatePackages, func(ctx context.Context, job *OperationJob) {
		batchedPackages, remainingPackages := planElevatedPackageUpdateBatch(updatePackages, elevatedPackageUpdateBatchEligible)
		if len(batchedPackages) > 0 {
			result := app.runElevatedPackageUpdateBatchForJob(ctx, job, batchedPackages)
			if ctx.Err() != nil || result.Code == commandCancelledCode {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.CancelRequested = true
					status.State = jobStateCancelled
					status.Notice = "Update cancelled."
				})
			}
		}
		if ctx.Err() == nil && app.operationJobCanContinue(job) {
			for _, updatePackage := range remainingPackages {
				packageCtx := withLogMetadata(ctx, logMetadata{PackageKey: updatePackage.Key, Manager: updatePackage.Manager})
				nextIndex := app.nextUpdateResultIndex(job)
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.CurrentIndex = nextIndex
					status.CurrentKey = updatePackage.Key
					status.CurrentPackage = updateJobPackageName(updatePackage)
				})
				if packageCtx.Err() != nil {
					app.mutateOperationJob(job, func(status *OperationJobStatus) {
						status.CancelRequested = true
						status.State = jobStateCancelled
						status.Notice = "Update cancelled."
					})
					break
				}
				result := app.updatePackageForJob(packageCtx, job, updatePackage)
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.Results = append(status.Results, UpdateResult{Key: updatePackage.Key, Result: result})
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
			refreshErr = refreshInventoryAfterUpdateJob(ctx, app, updatePackages)
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

func (app *App) operationJobCanContinue(job *OperationJob) bool {
	status := app.operationJobStatusSnapshot(job)
	return status.State != jobStateCancelled
}

func (app *App) nextUpdateResultIndex(job *OperationJob) int {
	status := app.operationJobStatusSnapshot(job)
	return len(status.Results) + 1
}

func (app *App) operationJobStatusSnapshot(job *OperationJob) OperationJobStatus {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	return cloneOperationJobStatus(job.status)
}

func (app *App) runElevatedPackageUpdateBatchForJob(ctx context.Context, job *OperationJob, updatePackages []Package) CommandResult {
	firstPackage := updatePackages[0]
	firstBatchIndex := app.nextUpdateResultIndex(job)
	batchStartNotice := fmt.Sprintf("Starting elevated package batch for %d package(s). Approve the Windows UAC prompt if shown.", len(updatePackages))
	appLogContext(ctx, "%s", batchStartNotice)
	app.mutateOperationJob(job, func(status *OperationJobStatus) {
		status.State = jobStateStarting
		status.CurrentIndex = firstBatchIndex
		status.CurrentKey = firstPackage.Key
		status.CurrentPackage = updateJobPackageName(firstPackage)
		status.Notice = batchStartNotice
	})
	batchResults, batchResult := elevatedPackageUpdateBatchRunner(ctx, updatePackages, func(packagePosition int, updatePackage Package) {
		progressNotice := fmt.Sprintf("Elevated package batch running %d/%d: %s.", packagePosition, len(updatePackages), updateJobPackageName(updatePackage))
		appLogContext(ctx, "%s", progressNotice)
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = firstBatchIndex + packagePosition - 1
			status.CurrentKey = updatePackage.Key
			status.CurrentPackage = updateJobPackageName(updatePackage)
			status.Notice = progressNotice
		})
	})
	appLogContext(ctx, "Elevated package batch finished with code %d.", batchResult.Code)
	app.mutateOperationJob(job, func(status *OperationJobStatus) {
		status.Results = append(status.Results, batchResults...)
		status.Result = &batchResult
		if len(batchResults) > 0 {
			lastResult := batchResults[len(batchResults)-1]
			status.CurrentIndex = len(status.Results)
			status.CurrentKey = lastResult.Key
			status.CurrentPackage = updateJobPackageName(packageForUpdateResultKey(updatePackages, lastResult.Key))
		}
	})
	return batchResult
}

func packageForUpdateResultKey(updatePackages []Package, resultKey string) Package {
	for _, updatePackage := range updatePackages {
		if updatePackage.Key == resultKey || normalizedJobPackageKey(updatePackage) == resultKey {
			return updatePackage
		}
	}
	if len(updatePackages) == 0 {
		return Package{}
	}
	return updatePackages[len(updatePackages)-1]
}

func (app *App) updatePackageForJob(ctx context.Context, job *OperationJob, updatePackage Package) CommandResult {
	if updatePackage.Manager == managerStore && updatePackage.UpdateState != "" {
		return storeExactUpdateExecutor.ExecuteWithCallbacks(ctx, updatePackage, StoreExactUpdateCallbacks{
			Starting: func(StoreExactUpdateRequest) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateStarting
					status.Notice = "Starting exact Store update for " + updateJobPackageName(updatePackage) + "..."
				})
			},
			Accepted: func(StoreExactUpdateRequest, CommandResult) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateAccepted
					status.Notice = "Store update accepted for " + updateJobPackageName(updatePackage) + "."
				})
			},
			Verifying: func(StoreExactUpdateRequest) {
				app.mutateOperationJob(job, func(status *OperationJobStatus) {
					status.State = jobStateVerifying
					status.Notice = "Verifying exact Store update for " + updateJobPackageName(updatePackage) + "..."
				})
			},
		})
	}
	return app.updatePackageWithInventoryRetry(ctx, updatePackage)
}

func (app *App) startScanJob() OperationJobStatus {
	return app.startOperationJob(jobTypeScan, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		startedStatus := app.mutateOperationJob(job, func(status *OperationJobStatus) {})
		if ctx.Err() != nil {
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.CancelRequested = true
				status.State = jobStateCancelled
				status.Notice = "Scan cancelled."
			})
			return
		}
		stateStore, err := defaultStateStore()
		var scanResult ScanResult
		if err != nil {
			scanResult = ScanResult{Errors: []map[string]string{{"source": "state", "error": err.Error()}}}
		} else {
			scanResult = scanInstalledApplicationsWithStore(ctx, stateStore)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Scan = &scanResult
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
			app.refreshInventorySyncContext(ctx, "scan job "+startedStatus.JobID)
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			if status.State == jobStateCancelled {
				return
			}
			if len(scanResult.Errors) > 0 {
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
		startedStatus := app.mutateOperationJob(job, func(status *OperationJobStatus) {})
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
		app.refreshInventorySyncContext(ctx, "inventory refresh job "+startedStatus.JobID)
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

func (app *App) startSelfUpdateJob() OperationJobStatus {
	return app.startOperationJob(jobTypeSelfUpdate, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.CurrentIndex = 1
			status.CurrentPackage = "WindowsUpdaterWebUI"
			status.Notice = "Checking for application update..."
		})
		if app.appUpdateChecker == nil {
			result := validationCommandResult("self-update", fmt.Errorf("application self-update is not configured"))
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application self-update is not configured.")
			return
		}
		updateStatus := app.appUpdateStatusContext(ctx, true)
		if ctx.Err() != nil {
			result := commandContextDoneResult(ctx, "self-update", "while checking for application update", []string{"all", "application"})
			app.finishSelfUpdateJob(job, result, jobStateCancelled, "Application self-update cancelled.")
			return
		}
		if updateStatus.Error != "" {
			result := validationCommandResult("self-update", errors.New(updateStatus.Error))
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application update check failed.")
			return
		}
		if !updateStatus.Available {
			result := CommandResult{OK: true, Command: "self-update", Stdout: "No newer application release is available."}
			app.finishSelfUpdateJob(job, result, jobStateSucceeded, "Application is already up to date.")
			return
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Notice = "Downloading application update " + updateStatus.LatestVersion + "..."
		})
		downloadDir, err := selfUpdateDownloadDir()
		if err != nil {
			result := workerCommandResultError("self-update", err)
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application update download failed.")
			return
		}
		downloadedArtifact, err := downloadSelfUpdateArtifact(ctx, http.DefaultClient, updateStatus, downloadDir)
		if err != nil {
			result := workerCommandResultError("self-update", err)
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application update download failed.")
			return
		}
		currentExecutable, err := osExecutable()
		if err != nil {
			result := workerCommandResultError("self-update", err)
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application update could not locate the running executable.")
			return
		}
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.Notice = "Preparing to restart and apply application update " + updateStatus.LatestVersion + "..."
		})
		if err := launchSelfUpdateApply(ctx, downloadedArtifact, currentExecutable); err != nil {
			result := workerCommandResultError("self-update", err)
			app.finishSelfUpdateJob(job, result, jobStateFailed, "Application update could not start the apply helper.")
			return
		}
		successResult := CommandResult{
			OK:      true,
			Command: "self-update",
			Stdout:  "Application update " + updateStatus.LatestVersion + " downloaded and verified. Restarting to apply it.",
		}
		app.finishSelfUpdateJob(job, successResult, jobStateSucceeded, "Application update downloaded. Restarting to apply it...")
		go func() {
			time.Sleep(200 * time.Millisecond)
			app.requestShutdown("Application self-update")
		}()
	})
}

func (app *App) finishSelfUpdateJob(job *OperationJob, result CommandResult, jobState, notice string) {
	app.mutateOperationJob(job, func(status *OperationJobStatus) {
		status.Result = &result
		status.Results = []UpdateResult{{Key: "app:self-update", Result: result}}
		status.State = jobState
		status.Notice = notice
	})
}

func jobAcceptedResponse(w http.ResponseWriter, status OperationJobStatus) {
	writeJSON(w, http.StatusAccepted, status)
}

func jobNotFoundError(jobID string) string {
	if jobID == "" {
		return "job_id is required"
	}
	return fmt.Sprintf("job %s was not found", jobID)
}
