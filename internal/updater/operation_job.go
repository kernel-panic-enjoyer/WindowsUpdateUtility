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
	status OperationJobStatus
	run    func(context.Context, *OperationJob)
	cancel context.CancelFunc
}

func (app *App) startOperationJob(jobType, mode string, total int, packageKeys []string, run func(context.Context, *OperationJob)) OperationJobStatus {
	return app.startOperationJobWithPackageSnapshot(jobType, mode, total, packageKeys, nil, run)
}

func (app *App) startOperationJobWithPackageSnapshot(jobType, mode string, total int, packageKeys []string, packages []Package, run func(context.Context, *OperationJob)) OperationJobStatus {
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
	allowUnknown, allowPinned := updateOptionsFromPackageSnapshot(packages)
	app.jobSeq++
	job := &OperationJob{
		run: run,
		status: OperationJobStatus{
			JobID:               fmt.Sprintf("job-%d", app.jobSeq),
			Revision:            1,
			Type:                jobType,
			Mode:                mode,
			State:               jobStateQueued,
			Total:               total,
			PackageKeys:         append([]string(nil), packageKeys...),
			Packages:            append([]Package(nil), packages...),
			AllowUnknownVersion: allowUnknown,
			AllowPinned:         allowPinned,
		},
	}
	app.jobs[job.status.JobID] = job
	app.jobQueue = append(app.jobQueue, job.status.JobID)
	status := cloneOperationJobStatus(job.status)
	appLog("Job %s queued for %s.", job.status.JobID, jobType)
	shouldPump := !app.jobActive
	if shouldPump {
		app.jobActive = true
	}
	app.jobsMu.Unlock()
	if shouldPump {
		if !app.startBackgroundWork("operation job queue", app.runOperationJobQueue) {
			app.jobsMu.Lock()
			app.jobActive = false
			job.status.CancelRequested = true
			job.status.State = jobStateCancelled
			job.status.FinishedAt = utcNow()
			job.status.Notice = "Job cancelled by shutdown."
			compactTerminalOperationJobStatus(&job.status)
			job.status.Revision++
			app.jobsMu.Unlock()
		}
	}
	return status
}

func updateOptionsFromPackageSnapshot(packages []Package) (bool, bool) {
	var allowUnknown, allowPinned bool
	for _, pkg := range packages {
		allowUnknown = allowUnknown || pkg.AllowUnknownVersionUpdate
		allowPinned = allowPinned || pkg.AllowPinnedUpdate
	}
	return allowUnknown, allowPinned
}

func (app *App) runOperationJobQueue(queueCtx context.Context) {
	for {
		app.jobsMu.Lock()
		var job *OperationJob
		for len(app.jobQueue) > 0 {
			id := app.jobQueue[0]
			app.jobQueue = app.jobQueue[1:]
			candidate := app.jobs[id]
			if candidate == nil {
				continue
			}
			if candidate.status.CancelRequested {
				candidate.status.State = jobStateCancelled
				candidate.status.Running = false
				candidate.status.FinishedAt = utcNow()
				compactTerminalOperationJobStatus(&candidate.status)
				candidate.status.Revision++
				continue
			}
			if queueCtx.Err() != nil {
				candidate.status.CancelRequested = true
				candidate.status.State = jobStateCancelled
				candidate.status.Running = false
				candidate.status.Notice = "Job cancelled by shutdown."
				candidate.status.FinishedAt = utcNow()
				compactTerminalOperationJobStatus(&candidate.status)
				candidate.status.Revision++
				continue
			}
			job = candidate
			break
		}
		if job == nil {
			app.jobActive = false
			app.jobsMu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(queueCtx)
		job.cancel = cancel
		job.status.State = jobStateRunning
		job.status.Running = true
		job.status.StartedAt = utcNow()
		job.status.Revision++
		status := cloneOperationJobStatus(job.status)
		app.jobsMu.Unlock()

		ctx = withLogMetadata(ctx, logMetadata{JobID: status.JobID, JobType: status.Type})
		appLogContext(ctx, "Job %s started for %s.", status.JobID, status.Type)
		recovered := runOperationJobSafely(ctx, job)
		cancel()
		if recovered != nil {
			diagnostic := sanitizedPanicDiagnostic(recovered)
			app.mutateOperationJob(job, func(status *OperationJobStatus) {
				status.State = jobStateFailed
				status.Error = diagnostic
				status.Notice = "Job failed because an internal error occurred."
				status.Result = &CommandResult{Command: status.Type, Code: 1, Stderr: diagnostic}
			})
		}

		app.jobsMu.Lock()
		if job.status.State == jobStateRunning || job.status.State == jobStateRefreshing {
			if job.status.CancelRequested {
				job.status.State = jobStateCancelled
				job.status.Notice = "Job cancelled."
			} else if job.status.Error != "" || operationJobHasFailures(job.status) {
				job.status.State = jobStateFailed
			} else {
				job.status.State = jobStateSucceeded
			}
		}
		job.status.Running = false
		if job.status.FinishedAt == "" {
			job.status.FinishedAt = utcNow()
		}
		if operationJobComplete(job.status) {
			compactTerminalOperationJobStatus(&job.status)
		}
		job.status.Revision++
		finished := cloneOperationJobStatus(job.status)
		app.pruneOperationJobsLocked()
		app.jobsMu.Unlock()
		appLogContext(ctx, "Job %s finished with state %s.", finished.JobID, finished.State)
	}
}

func runOperationJobSafely(ctx context.Context, job *OperationJob) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	job.run(ctx, job)
	return nil
}

func sanitizedPanicDiagnostic(recovered any) string {
	message := strings.TrimSpace(fmt.Sprint(recovered))
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

func operationJobHasFailures(status OperationJobStatus) bool {
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
	wanted := map[string]bool{}
	for _, jobType := range jobTypes {
		wanted[jobType] = true
	}
	for i := app.jobSeq; i >= 1; i-- {
		id := fmt.Sprintf("job-%d", i)
		job := app.jobs[id]
		if job == nil {
			continue
		}
		if len(wanted) == 0 || wanted[job.status.Type] {
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
		job := app.jobs[fmt.Sprintf("job-%d", i)]
		if job == nil {
			continue
		}
		statuses = append(statuses, cloneOperationJobStatus(job.status))
	}
	return statuses
}

func (app *App) activeOperationJobsSnapshot() []OperationJobStatus {
	all := app.operationJobsSnapshot()
	active := make([]OperationJobStatus, 0, len(all))
	for _, status := range all {
		if operationJobComplete(status) {
			continue
		}
		active = append(active, status)
	}
	return active
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
			job.status.State = jobStateCancelled
			job.status.Running = false
			job.status.FinishedAt = utcNow()
			compactTerminalOperationJobStatus(&job.status)
			job.status.Revision++
			job.status.Notice = "Job cancelled."
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
			job.status.State = jobStateCancelled
			job.status.Running = false
			job.status.FinishedAt = utcNow()
			compactTerminalOperationJobStatus(&job.status)
			job.status.Revision++
		}
	}
	app.pruneOperationJobsLocked()
}

func (app *App) pruneOperationJobsLocked() {
	if len(app.jobs) <= operationJobRecentHistoryLimit {
		return
	}
	retain := map[string]bool{}
	latestByType := map[string]bool{}
	for i := app.jobSeq; i >= 1; i-- {
		id := fmt.Sprintf("job-%d", i)
		job := app.jobs[id]
		if job == nil {
			continue
		}
		if !operationJobComplete(job.status) {
			retain[id] = true
			continue
		}
		if !latestByType[job.status.Type] {
			latestByType[job.status.Type] = true
			retain[id] = true
		}
	}
	recent := 0
	for i := app.jobSeq; i >= 1 && recent < operationJobRecentHistoryLimit; i-- {
		id := fmt.Sprintf("job-%d", i)
		job := app.jobs[id]
		if job == nil || !operationJobComplete(job.status) {
			continue
		}
		retain[id] = true
		recent++
	}
	for id, job := range app.jobs {
		if retain[id] {
			continue
		}
		if job != nil && operationJobComplete(job.status) {
			delete(app.jobs, id)
		}
	}
	app.jobQueue = filterRetainedJobQueue(app.jobQueue, app.jobs)
}

func filterRetainedJobQueue(queue []string, jobs map[string]*OperationJob) []string {
	filtered := queue[:0]
	for _, id := range queue {
		if jobs[id] != nil {
			filtered = append(filtered, id)
		}
	}
	return filtered
}
