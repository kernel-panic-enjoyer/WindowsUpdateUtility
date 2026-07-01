package updater

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func formBool(request *http.Request, fieldName string) (bool, bool) {
	if !request.Form.Has(fieldName) {
		return false, false
	}
	normalizedValue := strings.ToLower(strings.TrimSpace(request.Form.Get(fieldName)))
	return normalizedValue == "true" || normalizedValue == "1" || normalizedValue == "on" || normalizedValue == "yes", true
}

func validatePackageKey(packageKey string) error {
	manager, id, err := splitPackageKey(packageKey)
	if err != nil {
		return err
	}
	return validateManagerAndID(manager, id)
}

func (app *App) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/store/diagnostics/export":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		diagnosticsJSON, err := buildStoreDiagnosticsExport(r.Context(), loadState())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAttachmentResponse(w, "application/json", storeDiagnosticsExportFilename(time.Now()), diagnosticsJSON)
	case "/api/logs/export":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		logArchive, err := buildLogArchiveFromSnapshot(sessionLogs.ExportSnapshot())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeAttachmentResponse(w, "application/zip", logExportFilename(time.Now()), logArchive)
	case "/api/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, app.statusSnapshotContext(r.Context()))
	case "/api/status/refresh":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		app.refreshStatus(true)
		writeJSON(w, http.StatusAccepted, app.statusSnapshotContext(r.Context()))
	case "/api/app-update/check":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, http.StatusOK, app.appUpdateStatusContext(r.Context(), true))
	case "/api/app-update/apply":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		jobAcceptedResponse(w, app.startSelfUpdateJob())
	case "/api/packages":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, app.inventorySnapshotContext(r.Context()))
	case "/api/inventory/refresh":
		app.handleInventoryRefreshAPI(w, r)
	case "/api/jobs/status":
		app.handleJobStatusAPI(w, r)
	case "/api/jobs/log":
		app.handleJobLogAPI(w, r)
	case "/api/jobs":
		app.handleJobsAPI(w, r)
	case "/api/jobs/cancel":
		app.handleJobCancelAPI(w, r)
	case "/api/events":
		app.handleEventsAPI(w, r)
	case "/api/logs":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		since, ok := parseLogSince(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, logsAPIResponseFromQuery(sessionLogs.Query(since)))
	case "/api/search":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		searchResults, err := searchPackages(r.URL.Query().Get("q"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, searchResults)
	case "/api/install":
		app.handleInstallAPI(w, r)
	case "/api/managers/install":
		app.handleManagerInstallAPI(w, r)
	case "/api/scan":
		app.handleScanAPI(w, r)
	case "/api/update":
		app.handleUpdateAPI(w, r)
	case "/api/update-all/status":
		app.handleUpdateAllStatusAPI(w, r)
	case "/api/update-all/cancel":
		app.handleUpdateAllCancelAPI(w, r)
	case "/api/update-all":
		app.handleUpdateAllAPI(w, r)
	case "/api/settings/startup":
		app.handleStartupSettingsAPI(w, r)
	case "/api/settings/auto-update":
		app.handleAutoUpdateSettingsAPI(w, r)
	case "/api/settings/theme":
		app.handleThemeSettingsAPI(w, r)
	case "/api/settings/app-update-prompt":
		app.handleAppUpdatePromptSettingsAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeAttachmentResponse(w http.ResponseWriter, contentType, filename string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func parseLogSince(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var sinceID int64
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		parsedSinceID, err := strconv.ParseInt(sinceParam, 10, 64)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "since must be an integer")
			return 0, false
		}
		sinceID = parsedSinceID
	}
	return sinceID, true
}

func logsAPIResponseFromQuery(result LogQueryResult) logsAPIResponse {
	return logsAPIResponse{
		Entries:      result.Entries,
		OldestID:     result.OldestID,
		LatestID:     result.LatestID,
		DroppedCount: result.DroppedCount,
		DroppedBytes: result.DroppedBytes,
		GapDetected:  result.GapDetected,
	}
}

func logExportFilename(now time.Time) string {
	return now.Format("2006-01-02_15-04-05") + "_windows-updater-webui-logs.zip"
}

func storeDiagnosticsExportFilename(now time.Time) string {
	return now.Format("2006-01-02_15-04-05") + "_store-diagnostics.json"
}

func (app *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if !app.trustedHost(r) {
		writeAPIError(w, http.StatusMisdirectedRequest, "untrusted host")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		app.serveFrontendAsset(w, r)
		return
	}
	if r.URL.Path == "/favicon.ico" {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
		w.Header().Set("ETag", `"`+appIconVersion()+`"`)
		_, _ = w.Write(appIconICO)
		return
	}
	if app.handleBootstrap(w, r) {
		return
	}
	if !app.sessionOK(r) {
		http.Error(w, "Unauthorized. Start the app and use the tokenized bootstrap URL.", http.StatusUnauthorized)
		return
	}
	if !app.requestBoundaryOK(w, r) {
		return
	}
	if r.URL.Path == "/shutdown" {
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_, _ = io.WriteString(w, "Stopping")
		go func() {
			time.Sleep(200 * time.Millisecond)
			app.requestShutdown("WebUI Stop")
		}()
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		app.serveAPI(w, r)
		return
	}

	switch r.URL.Path {
	case "/":
		app.render(w, r, PageData{})
	default:
		http.NotFound(w, r)
	}
}

func (app *App) render(w http.ResponseWriter, r *http.Request, pageData PageData) {
	savedState := loadState()
	pageData.Admin = isAdmin()
	pageData.StateDir, _ = stateDir()
	pageData.Theme = savedState.Theme
	pageData.IconVersion = appIconVersion()
	pageData.AssetVersion = frontendAssetVersion()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, pageData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (app *App) serveFrontendAsset(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	assetVersion := frontendAssetVersion()
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("ETag", `"`+assetVersion+`"`)
	switch r.URL.Path {
	case "/assets/ui.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, uiCSS)
	case "/assets/ui.js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = io.WriteString(w, uiJS)
	default:
		http.NotFound(w, r)
	}
}
