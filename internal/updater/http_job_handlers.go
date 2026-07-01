package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type jobsAPIResponse struct {
	Jobs     []OperationJobStatus `json:"jobs"`
	Revision int64                `json:"revision,omitempty"`
}

func jobIDFromRequest(r *http.Request) string {
	if jobID := r.URL.Query().Get("job_id"); jobID != "" {
		return jobID
	}
	if requestIsJSON(r) {
		var requestPayload struct {
			JobID string `json:"job_id"`
		}
		if err := decodeJSONRequest(r, &requestPayload); err == nil {
			return requestPayload.JobID
		}
		return ""
	}
	_ = r.ParseForm()
	return r.Form.Get("job_id")
}

func (app *App) handleJobStatusAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	jobID := jobIDFromRequest(r)
	status, ok := app.operationJobStatus(jobID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, jobNotFoundError(jobID))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (app *App) handleJobsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	jobStatuses := app.operationJobsSnapshot()
	writeJSON(w, http.StatusOK, jobsAPIResponse{Jobs: jobStatuses, Revision: latestJobRevision(jobStatuses)})
}

func (app *App) handleJobLogAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	jobID := jobIDFromRequest(r)
	if _, ok := app.operationJobStatus(jobID); !ok {
		writeAPIError(w, http.StatusNotFound, jobNotFoundError(jobID))
		return
	}
	since, ok := parseLogSince(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, logsAPIResponseFromQuery(sessionLogs.JobQuery(jobID, since)))
}

func (app *App) handleJobCancelAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	jobID := jobIDFromRequest(r)
	status, ok := app.cancelOperationJob(jobID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, jobNotFoundError(jobID))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (app *App) handleInventoryRefreshAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	jobAcceptedResponse(w, app.startInventoryRefreshJob())
}

func (app *App) handleScanAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	jobAcceptedResponse(w, app.startScanJob())
}

func (app *App) handleEventsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	lastSentLogID := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "since must be an integer")
			return
		}
		lastSentLogID = parsed
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendEvent := func(event string, payload any) bool {
		encodedPayload, err := json.Marshal(payload)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encodedPayload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	initialJobStatuses := app.operationJobsSnapshot()
	lastJobRevision := latestJobRevision(initialJobStatuses)
	sendEvent("jobs", jobsAPIResponse{Jobs: initialJobStatuses, Revision: lastJobRevision})
	initialLogQuery := sessionLogs.Query(lastSentLogID)
	initialLogEntries := initialLogQuery.Entries
	if !sendEvent("logs", logsAPIResponseFromQuery(initialLogQuery)) {
		return
	}
	for _, entry := range initialLogEntries {
		if entry.ID > lastSentLogID {
			lastSentLogID = entry.ID
		}
	}

	heartbeatInterval := 10 * time.Second
	nextHeartbeatAt := time.Now().Add(heartbeatInterval)
	for {
		jobStatuses := app.operationJobsSnapshot()
		hasActiveJobs := false
		for _, job := range jobStatuses {
			if !operationJobComplete(job) {
				hasActiveJobs = true
				break
			}
		}
		delay := 5 * time.Second
		if hasActiveJobs {
			delay = 1 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
			jobStatuses = app.operationJobsSnapshot()
			logQuery := sessionLogs.Query(lastSentLogID)
			logEntries := logQuery.Entries
			latestLogID := logQuery.LatestID
			jobRevision := latestJobRevision(jobStatuses)
			if jobRevision != lastJobRevision {
				if !sendEvent("jobs", jobsAPIResponse{Jobs: jobStatuses, Revision: jobRevision}) {
					return
				}
				lastJobRevision = jobRevision
			}
			if len(logEntries) == 0 {
				if time.Now().After(nextHeartbeatAt) {
					if !sendEvent("heartbeat", map[string]any{"latest_id": latestLogID}) {
						return
					}
					nextHeartbeatAt = time.Now().Add(heartbeatInterval)
				}
				continue
			}
			if !sendEvent("logs", logsAPIResponseFromQuery(logQuery)) {
				return
			}
			for _, entry := range logEntries {
				if entry.ID > lastSentLogID {
					lastSentLogID = entry.ID
				}
			}
		}
	}
}

func latestJobRevision(jobs []OperationJobStatus) int64 {
	var revision int64
	for _, job := range jobs {
		if job.Revision > revision {
			revision = job.Revision
		}
	}
	return revision
}
