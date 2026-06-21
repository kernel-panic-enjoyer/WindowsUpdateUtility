package updater

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func formBool(r *http.Request, name string) (bool, bool) {
	if !r.Form.Has(name) {
		return false, false
	}
	value := strings.ToLower(strings.TrimSpace(r.Form.Get(name)))
	return value == "true" || value == "1" || value == "on" || value == "yes", true
}

func validatePackageKey(key string) error {
	manager, id, err := splitPackageKey(key)
	if err != nil {
		return err
	}
	return validateManagerAndID(manager, id)
}

func (app *App) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/logs/export":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		data, err := buildLogArchive(sessionLogs.Snapshot())
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+logExportFilename(time.Now())+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case "/api/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, app.statusSnapshot())
	case "/api/status/refresh":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		app.refreshStatus(true)
		writeJSON(w, http.StatusAccepted, app.statusSnapshot())
	case "/api/packages":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, app.inventorySnapshot())
	case "/api/inventory/refresh":
		app.handleInventoryRefreshAPI(w, r)
	case "/api/jobs/status":
		app.handleJobStatusAPI(w, r)
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
		var since int64
		if raw := r.URL.Query().Get("since"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "since must be an integer")
				return
			}
			since = parsed
		}
		writeJSON(w, http.StatusOK, logsAPIResponse{Entries: sessionLogs.Since(since), LatestID: sessionLogs.LatestID()})
	case "/api/search":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		lookup, err := searchPackages(r.URL.Query().Get("q"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, lookup)
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
	default:
		http.NotFound(w, r)
	}
}

func logExportFilename(now time.Time) string {
	return now.Format("2006-01-02_15-04-05") + "_windows-updater-webui-logs.zip"
}

func (app *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if !app.trustedHost(r) {
		writeAPIError(w, http.StatusMisdirectedRequest, "untrusted host")
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

func (app *App) render(w http.ResponseWriter, r *http.Request, data PageData) {
	state := loadState()
	data.Admin = isAdmin()
	data.StateDir, _ = stateDir()
	data.Theme = state.Theme
	data.IconVersion = appIconVersion()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
