package updater

import (
	"context"
	"fmt"
	"strings"
)

const (
	jobStateQueued              = "queued"
	jobStateStarting            = "starting"
	jobStateRunning             = "running"
	jobStateAccepted            = "accepted"
	jobStateVerifying           = "verifying"
	jobStateRefreshing          = "refreshing"
	jobStateSucceeded           = "succeeded"
	jobStateAcceptedNotVerified = "accepted_not_verified"
	jobStateFailed              = "failed"
	jobStateCancelled           = "cancelled"

	jobTypeInstall          = "install"
	jobTypeUpdate           = "update"
	jobTypeUpdateAll        = "update-all"
	jobTypeScan             = "scan"
	jobTypeManagerInstall   = "manager-install"
	jobTypeInventoryRefresh = "inventory-refresh"
	jobTypeSelfUpdate       = "app-self-update"
)

const operationJobRecentHistoryLimit = 25

type OperationJobStatus struct {
	JobID               string         `json:"job_id,omitempty"`
	Revision            int64          `json:"revision,omitempty"`
	Type                string         `json:"type,omitempty"`
	Mode                string         `json:"mode,omitempty"`
	State               string         `json:"state"`
	Running             bool           `json:"running"`
	CancelRequested     bool           `json:"cancel_requested"`
	CurrentPackage      string         `json:"current_package,omitempty"`
	CurrentKey          string         `json:"current_key,omitempty"`
	PackageKeys         []string       `json:"package_keys,omitempty"`
	Packages            []Package      `json:"packages,omitempty"`
	CurrentIndex        int            `json:"current_index"`
	Total               int            `json:"total"`
	Results             []UpdateResult `json:"results,omitempty"`
	Result              *CommandResult `json:"result,omitempty"`
	Scan                *ScanResult    `json:"scan,omitempty"`
	RefreshStarted      bool           `json:"refresh_started"`
	AllowUnknownVersion bool           `json:"allow_unknown_version,omitempty"`
	AllowPinned         bool           `json:"allow_pinned,omitempty"`
	StartedAt           string         `json:"started_at,omitempty"`
	FinishedAt          string         `json:"finished_at,omitempty"`
	Notice              string         `json:"notice,omitempty"`
	Error               string         `json:"error,omitempty"`
}

type OperationJob struct {
	status  OperationJobStatus
	execute func(context.Context, *OperationJob)
	cancel  context.CancelFunc
}

func (app *App) startOperationJob(jobType, mode string, total int, packageKeys []string, execute func(context.Context, *OperationJob)) OperationJobStatus {
	return app.startOperationJobWithPackageSnapshot(jobType, mode, total, packageKeys, nil, execute)
}

func (app *App) startOperationJobWithPackageSnapshot(jobType, mode string, total int, packageKeys []string, packages []Package, execute func(context.Context, *OperationJob)) OperationJobStatus {
	if app.isShuttingDown() {
		return OperationJobStatus{
			Type:        jobType,
			Mode:        mode,
			State:       jobStateFailed,
			Running:     false,
			Total:       total,
			PackageKeys: append([]string(nil), packageKeys...),
			Notice:      "Job not started because shutdown is in progress.",
			Error:       "shutdown in progress",
			FinishedAt:  utcNow(),
		}
	}
	app.jobsMu.Lock()
	if app.jobs == nil {
		app.jobs = map[string]*OperationJob{}
	}
	allowsUnknownVersion, allowsPinnedVersion := packageSnapshotUpdateAllowances(packages)
	app.jobSeq++
	operationJob := &OperationJob{
		execute: execute,
		status: OperationJobStatus{
			JobID:               fmt.Sprintf("job-%d", app.jobSeq),
			Revision:            1,
			Type:                jobType,
			Mode:                mode,
			State:               jobStateQueued,
			Total:               total,
			PackageKeys:         append([]string(nil), packageKeys...),
			Packages:            append([]Package(nil), packages...),
			AllowUnknownVersion: allowsUnknownVersion,
			AllowPinned:         allowsPinnedVersion,
		},
	}
	app.jobs[operationJob.status.JobID] = operationJob
	app.jobQueue = append(app.jobQueue, operationJob.status.JobID)
	queuedStatus := cloneOperationJobStatus(operationJob.status)
	appLog("Job %s queued for %s.", operationJob.status.JobID, jobType)
	shouldStartQueueRunner := !app.jobActive
	if shouldStartQueueRunner {
		app.jobActive = true
	}
	app.jobsMu.Unlock()
	if shouldStartQueueRunner {
		if !app.startBackgroundWork("operation job queue", app.runOperationJobQueue) {
			app.jobsMu.Lock()
			app.jobActive = false
			operationJob.status.CancelRequested = true
			finishQueuedOperationJobCancellation(&operationJob.status, "Job cancelled by shutdown.")
			operationJob.status.Revision++
			app.jobsMu.Unlock()
		}
	}
	return queuedStatus
}

func packageSnapshotUpdateAllowances(packages []Package) (allowsUnknownVersion, allowsPinnedVersion bool) {
	for _, pkg := range packages {
		allowsUnknownVersion = allowsUnknownVersion || pkg.AllowUnknownVersionUpdate
		allowsPinnedVersion = allowsPinnedVersion || pkg.AllowPinnedUpdate
	}
	return allowsUnknownVersion, allowsPinnedVersion
}

func (app *App) runOperationJobQueue(queueCtx context.Context) {
	for {
		app.jobsMu.Lock()
		var nextJob *OperationJob
		for len(app.jobQueue) > 0 {
			queuedJobID := app.jobQueue[0]
			app.jobQueue = app.jobQueue[1:]
			queuedJob := app.jobs[queuedJobID]
			if queuedJob == nil {
				continue
			}
			if queuedJob.status.CancelRequested {
				finishQueuedOperationJobCancellation(&queuedJob.status, "")
				queuedJob.status.Revision++
				continue
			}
			if queueCtx.Err() != nil {
				queuedJob.status.CancelRequested = true
				finishQueuedOperationJobCancellation(&queuedJob.status, "Job cancelled by shutdown.")
				queuedJob.status.Revision++
				continue
			}
			nextJob = queuedJob
			break
		}
		if nextJob == nil {
			app.jobActive = false
			app.jobsMu.Unlock()
			return
		}
		jobCtx, cancelJob := context.WithCancel(queueCtx)
		nextJob.cancel = cancelJob
		nextJob.status.State = jobStateRunning
		nextJob.status.Running = true
		nextJob.status.StartedAt = utcNow()
		nextJob.status.Revision++
		startedStatus := cloneOperationJobStatus(nextJob.status)
		app.jobsMu.Unlock()

		jobCtx = withLogMetadata(jobCtx, logMetadata{JobID: startedStatus.JobID, JobType: startedStatus.Type})
		appLogContext(jobCtx, "Job %s started for %s.", startedStatus.JobID, startedStatus.Type)
		panicValue := runOperationJobSafely(jobCtx, nextJob)
		cancelJob()
		if panicValue != nil {
			diagnostic := sanitizedPanicDiagnostic(panicValue)
			app.mutateOperationJob(nextJob, func(status *OperationJobStatus) {
				status.State = jobStateFailed
				status.Error = diagnostic
				status.Notice = "Job failed because an internal error occurred."
				status.Result = &CommandResult{Command: status.Type, Code: 1, Stderr: diagnostic}
			})
		}

		app.jobsMu.Lock()
		if nextJob.status.State == jobStateRunning || nextJob.status.State == jobStateRefreshing {
			if nextJob.status.CancelRequested {
				nextJob.status.State = jobStateCancelled
				nextJob.status.Notice = "Job cancelled."
			} else if nextJob.status.Error != "" || operationJobStatusHasFailures(nextJob.status) {
				nextJob.status.State = jobStateFailed
			} else {
				nextJob.status.State = jobStateSucceeded
			}
		}
		nextJob.status.Running = false
		if nextJob.status.FinishedAt == "" {
			nextJob.status.FinishedAt = utcNow()
		}
		if operationJobComplete(nextJob.status) {
			compactTerminalOperationJobStatus(&nextJob.status)
		}
		nextJob.status.Revision++
		finishedStatus := cloneOperationJobStatus(nextJob.status)
		app.pruneOperationJobsLocked()
		app.jobsMu.Unlock()
		appLogContext(jobCtx, "Job %s finished with state %s.", finishedStatus.JobID, finishedStatus.State)
	}
}

func finishQueuedOperationJobCancellation(status *OperationJobStatus, notice string) {
	status.State = jobStateCancelled
	status.Running = false
	if notice != "" {
		status.Notice = notice
	}
	status.FinishedAt = utcNow()
	compactTerminalOperationJobStatus(status)
}

func runOperationJobSafely(ctx context.Context, job *OperationJob) (panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	job.execute(ctx, job)
	return nil
}

func sanitizedPanicDiagnostic(panicValue any) string {
	message := strings.TrimSpace(fmt.Sprint(panicValue))
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "\n", " ")
	if message == "" {
		message = "unknown panic"
	}
	if len(message) > 240 {
		message = message[:240] + "..."
	}
	return "internal job panic: " + message
}

func operationJobStatusHasFailures(status OperationJobStatus) bool {
	if status.Result != nil && !status.Result.OK {
		return true
	}
	for _, result := range status.Results {
		if !result.Result.OK {
			return true
		}
	}
	return false
}

func compactTerminalOperationJobStatus(status *OperationJobStatus) {
	if status == nil || !operationJobComplete(*status) {
		return
	}
	status.Packages = nil
	if status.Result != nil {
		result := compactCommandResult(*status.Result, terminalCommandResultStreamBytes, maxCommandResultCommandBytes)
		status.Result = &result
	}
	for i := range status.Results {
		status.Results[i].Result = compactCommandResult(status.Results[i].Result, terminalCommandResultStreamBytes, maxCommandResultCommandBytes)
	}
	if status.Scan != nil {
		status.Scan.NewApps = nil
		status.Scan.RemovedApps = nil
		if status.Scan.WingetResult != nil {
			result := compactCommandResult(*status.Scan.WingetResult, terminalCommandResultStreamBytes, maxCommandResultCommandBytes)
			status.Scan.WingetResult = &result
		}
	}
}

func cloneOperationJobStatus(status OperationJobStatus) OperationJobStatus {
	status.PackageKeys = append([]string(nil), status.PackageKeys...)
	status.Packages = append([]Package(nil), status.Packages...)
	status.Results = append([]UpdateResult(nil), status.Results...)
	if status.Result != nil {
		result := *status.Result
		status.Result = &result
	}
	if status.Scan != nil {
		scan := *status.Scan
		status.Scan = &scan
	}
	return status
}

func (app *App) mutateOperationJob(job *OperationJob, mutate func(*OperationJobStatus)) OperationJobStatus {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	mutate(&job.status)
	job.status.Revision++
	return cloneOperationJobStatus(job.status)
}

func (app *App) operationJobStatus(id string) (OperationJobStatus, bool) {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	job := app.jobs[id]
	if job == nil {
		return OperationJobStatus{}, false
	}
	return cloneOperationJobStatus(job.status), true
}

func (app *App) latestOperationJobStatus(jobTypes ...string) OperationJobStatus {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	requestedTypes := map[string]bool{}
	for _, jobType := range jobTypes {
		requestedTypes[jobType] = true
	}
	for i := app.jobSeq; i >= 1; i-- {
		jobID := fmt.Sprintf("job-%d", i)
		job := app.jobs[jobID]
		if job == nil {
			continue
		}
		if len(requestedTypes) == 0 || requestedTypes[job.status.Type] {
			return cloneOperationJobStatus(job.status)
		}
	}
	return OperationJobStatus{}
}

func (app *App) operationJobsSnapshot() []OperationJobStatus {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	app.pruneOperationJobsLocked()
	statuses := make([]OperationJobStatus, 0, len(app.jobs))
	for i := int64(1); i <= app.jobSeq; i++ {
		jobID := fmt.Sprintf("job-%d", i)
		job := app.jobs[jobID]
		if job == nil {
			continue
		}
		statuses = append(statuses, cloneOperationJobStatus(job.status))
	}
	return statuses
}

func (app *App) activeOperationJobsSnapshot() []OperationJobStatus {
	allStatuses := app.operationJobsSnapshot()
	activeStatuses := make([]OperationJobStatus, 0, len(allStatuses))
	for _, status := range allStatuses {
		if operationJobComplete(status) {
			continue
		}
		activeStatuses = append(activeStatuses, status)
	}
	return activeStatuses
}

func operationJobComplete(status OperationJobStatus) bool {
	state := strings.ToLower(strings.TrimSpace(status.State))
	return !status.Running && state != jobStateQueued && state != jobStateRunning && state != jobStateRefreshing
}

func (app *App) cancelOperationJob(id string) (OperationJobStatus, bool) {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	job := app.jobs[id]
	if job == nil {
		return OperationJobStatus{}, false
	}
	if job.status.State == jobStateQueued || job.status.Running {
		job.status.CancelRequested = true
		job.status.Notice = "Cancelling job..."
		if job.cancel != nil {
			job.cancel()
		}
		if job.status.State == jobStateQueued {
			finishQueuedOperationJobCancellation(&job.status, "Job cancelled.")
			job.status.Revision++
		}
		appLog("Job %s cancellation requested.", job.status.JobID)
	}
	app.pruneOperationJobsLocked()
	return cloneOperationJobStatus(job.status), true
}

func (app *App) cancelOperationJobsForShutdown() {
	app.jobsMu.Lock()
	defer app.jobsMu.Unlock()
	for _, job := range app.jobs {
		if job == nil || operationJobComplete(job.status) {
			continue
		}
		job.status.CancelRequested = true
		job.status.Notice = "Job cancelled by shutdown."
		if job.cancel != nil {
			job.cancel()
		}
		if job.status.State == jobStateQueued {
			finishQueuedOperationJobCancellation(&job.status, "Job cancelled by shutdown.")
			job.status.Revision++
		}
	}
	app.pruneOperationJobsLocked()
}

func (app *App) pruneOperationJobsLocked() {
	if len(app.jobs) <= operationJobRecentHistoryLimit {
		return
	}
	retainedJobIDs := map[string]bool{}
	latestCompletedTypeRetained := map[string]bool{}
	for i := app.jobSeq; i >= 1; i-- {
		jobID := fmt.Sprintf("job-%d", i)
		job := app.jobs[jobID]
		if job == nil {
			continue
		}
		if !operationJobComplete(job.status) {
			retainedJobIDs[jobID] = true
			continue
		}
		if !latestCompletedTypeRetained[job.status.Type] {
			latestCompletedTypeRetained[job.status.Type] = true
			retainedJobIDs[jobID] = true
		}
	}
	recentCompletedRetained := 0
	for i := app.jobSeq; i >= 1 && recentCompletedRetained < operationJobRecentHistoryLimit; i-- {
		jobID := fmt.Sprintf("job-%d", i)
		job := app.jobs[jobID]
		if job == nil || !operationJobComplete(job.status) {
			continue
		}
		retainedJobIDs[jobID] = true
		recentCompletedRetained++
	}
	for jobID, job := range app.jobs {
		if retainedJobIDs[jobID] {
			continue
		}
		if job != nil && operationJobComplete(job.status) {
			delete(app.jobs, jobID)
		}
	}
	app.jobQueue = filterExistingOperationJobQueueIDs(app.jobQueue, app.jobs)
}

func filterExistingOperationJobQueueIDs(queuedJobIDs []string, jobsByID map[string]*OperationJob) []string {
	filteredJobIDs := queuedJobIDs[:0]
	for _, jobID := range queuedJobIDs {
		if jobsByID[jobID] != nil {
			filteredJobIDs = append(filteredJobIDs, jobID)
		}
	}
	return filteredJobIDs
}
