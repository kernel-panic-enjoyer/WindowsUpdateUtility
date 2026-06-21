package updater

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type jobsAPIResponse struct {
	Jobs []OperationJobStatus `json:"jobs"`
}

func parseJobID(r *http.Request) string {
	if id := r.URL.Query().Get("job_id"); id != "" {
		return id
	}
	if requestIsJSON(r) {
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err := decodeJSONRequest(r, &payload); err == nil {
			return payload.JobID
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
	id := parseJobID(r)
	status, ok := app.operationJobStatus(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, jobNotFoundError(id))
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (app *App) handleJobsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, jobsAPIResponse{Jobs: app.operationJobsSnapshot()})
}

func (app *App) handleJobCancelAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	id := parseJobID(r)
	status, ok := app.cancelOperationJob(id)
	if !ok {
		writeAPIError(w, http.StatusNotFound, jobNotFoundError(id))
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
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "since must be an integer")
			return
		}
		since = parsed
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(event string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	send("jobs", jobsAPIResponse{Jobs: app.operationJobsSnapshot()})
	initialLogs := sessionLogs.Since(since)
	if !send("logs", logsAPIResponse{Entries: initialLogs, LatestID: sessionLogs.LatestID()}) {
		return
	}
	for _, entry := range initialLogs {
		if entry.ID > since {
			since = entry.ID
		}
	}

	heartbeatDue := time.Now().Add(10 * time.Second)
	for {
		jobs := app.operationJobsSnapshot()
		active := false
		for _, job := range jobs {
			if !operationJobComplete(job) {
				active = true
				break
			}
		}
		delay := 5 * time.Second
		if active {
			delay = 1 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
			jobs = app.operationJobsSnapshot()
			active = false
			for _, job := range jobs {
				if !operationJobComplete(job) {
					active = true
					break
				}
			}
			entries := sessionLogs.Since(since)
			latestID := sessionLogs.LatestID()
			if active || len(entries) > 0 {
				if !send("jobs", jobsAPIResponse{Jobs: jobs}) {
					return
				}
			}
			if len(entries) == 0 {
				if time.Now().After(heartbeatDue) {
					if !send("heartbeat", map[string]any{"latest_id": latestID}) {
						return
					}
					heartbeatDue = time.Now().Add(10 * time.Second)
				}
				continue
			}
			if !send("logs", logsAPIResponse{Entries: entries, LatestID: latestID}) {
				return
			}
			for _, entry := range entries {
				if entry.ID > since {
					since = entry.ID
				}
			}
		}
	}
}
