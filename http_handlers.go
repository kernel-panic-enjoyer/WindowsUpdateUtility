package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (app *App) tokenOK(r *http.Request) bool {
	token := r.URL.Query().Get("token")
	if token == "" {
		_ = r.ParseForm()
		token = r.Form.Get("token")
	}
	if token == "" {
		token = r.Header.Get("X-Updater-Token")
	}
	return token == app.token
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func formBool(r *http.Request, name string) (bool, bool) {
	if !r.Form.Has(name) {
		return false, false
	}
	value := strings.ToLower(strings.TrimSpace(r.Form.Get(name)))
	return value == "true" || value == "1" || value == "on" || value == "yes", true
}

func setThemePreference(theme string) (State, error) {
	state := loadState()
	if theme == "light" {
		state.Theme = "light"
	} else {
		state.Theme = "dark"
	}
	return state, saveState(state)
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
	case "/api/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		app.refreshStatus(r.URL.Query().Get("refresh") == "1")
		writeJSON(w, http.StatusOK, app.statusSnapshot())
	case "/api/packages":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		app.refreshInventory(r.URL.Query().Get("refresh") == "1")
		writeJSON(w, http.StatusOK, app.inventorySnapshot())
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
		writeJSON(w, http.StatusOK, map[string]any{"entries": sessionLogs.Since(since), "latest_id": sessionLogs.LatestID()})
	case "/api/search":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		results, managers, commandResults, err := searchPackages(r.URL.Query().Get("q"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"packages": results, "managers": managers, "command_results": commandResults})
	case "/api/scan":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		scan := scanInstalledApplications()
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, scan)
	case "/api/update":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		manager := r.Form.Get("manager")
		id := r.Form.Get("package_id")
		if err := validateManagerAndID(manager, id); err != nil {
			result := CommandResult{Code: 2, Stderr: err.Error(), Command: "update"}
			writeJSON(w, http.StatusBadRequest, map[string]any{"result": result, "refresh_started": false})
			return
		}
		result := updatePackage(manager, id)
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, map[string]any{"result": result, "refresh_started": true})
	case "/api/update-all":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		for _, key := range r.Form["package_key"] {
			if err := validatePackageKey(key); err != nil {
				result := UpdateResult{Key: key, Result: CommandResult{Code: 2, Stderr: err.Error(), Command: "update-all"}}
				writeJSON(w, http.StatusBadRequest, map[string]any{"results": []UpdateResult{result}, "refresh_started": false})
				return
			}
		}
		results := updateAll(r.Form["package_key"])
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "refresh_started": true})
	case "/api/settings/startup":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		enabled, _ := formBool(r, "enabled")
		result := setStartup(enabled)
		app.refreshStatus(true)
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
	case "/api/settings/auto-update":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		var global *bool
		if value, ok := formBool(r, "global"); ok {
			global = &value
		}
		var packageEnabled *bool
		if value, ok := formBool(r, "package_enabled"); ok {
			packageEnabled = &value
		}
		state, result := setAutoUpdate(global, r.Form["package_key"], packageEnabled)
		app.refreshStatus(true)
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, map[string]any{"settings": state, "result": result})
	case "/api/settings/theme":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		state, err := setThemePreference(r.Form.Get("theme"))
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": state})
	default:
		http.NotFound(w, r)
	}
}

func (app *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/shutdown" && app.tokenOK(r) {
		_, _ = io.WriteString(w, "Stopping")
		go func() {
			time.Sleep(200 * time.Millisecond)
			app.requestShutdown("WebUI Stop")
		}()
		return
	}
	if !app.tokenOK(r) {
		http.Error(w, "Unauthorized. Start the app and use the tokenized URL.", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		app.serveAPI(w, r)
		return
	}

	switch r.URL.Path {
	case "/":
		app.render(w, r, PageData{})
	case "/search":
		app.render(w, r, PageData{SearchQuery: r.URL.Query().Get("q")})
	case "/scan":
		scan := scanInstalledApplications()
		app.render(w, r, PageData{Scan: &scan, Message: "Application scan completed."})
	case "/install":
		_ = r.ParseForm()
		result := installPackage(r.Form.Get("manager"), r.Form.Get("package_id"))
		app.render(w, r, PageData{CommandResult: &result, Message: "Install command completed."})
	case "/manager/install":
		_ = r.ParseForm()
		result := installManager(r.Form.Get("manager"))
		app.render(w, r, PageData{CommandResult: &result, Message: "Package manager install action completed."})
	case "/update":
		_ = r.ParseForm()
		result := updatePackage(r.Form.Get("manager"), r.Form.Get("package_id"))
		app.render(w, r, PageData{CommandResult: &result, Message: "Update command completed."})
	case "/update-selected":
		_ = r.ParseForm()
		results := updateAll(r.Form["package_key"])
		app.render(w, r, PageData{ActionResults: results, Message: "Selected update command completed."})
	case "/update-all":
		results := updateAll(nil)
		app.render(w, r, PageData{ActionResults: results, Message: "Update all command completed."})
	case "/settings/startup":
		_ = r.ParseForm()
		result := setStartup(r.Form.Get("enabled") == "true")
		app.render(w, r, PageData{CommandResult: &result, Message: "Startup setting updated."})
	case "/settings/auto":
		_ = r.ParseForm()
		var global *bool
		if r.Form.Has("global") {
			value := r.Form.Get("global") == "true"
			global = &value
		}
		var packageEnabled *bool
		if r.Form.Has("package_enabled") {
			value := r.Form.Get("package_enabled") == "true"
			packageEnabled = &value
		}
		_, result := setAutoUpdate(global, r.Form["package_key"], packageEnabled)
		app.render(w, r, PageData{CommandResult: &result, Message: "Auto-update setting updated."})
	case "/settings/theme":
		_ = r.ParseForm()
		_, _ = setThemePreference(r.Form.Get("theme"))
		app.render(w, r, PageData{Message: "Theme updated."})
	default:
		http.NotFound(w, r)
	}
}

func (app *App) render(w http.ResponseWriter, r *http.Request, data PageData) {
	state := loadState()
	data.Token = app.token
	data.Admin = isAdmin()
	data.StateDir, _ = stateDir()
	data.Settings = state
	data.Theme = state.Theme

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
