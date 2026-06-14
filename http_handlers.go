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
	writeJSON(w, status, apiErrorResponse{Error: message})
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

type apiErrorResponse struct {
	Error string `json:"error"`
}

type logsAPIResponse struct {
	Entries  []LogEntry `json:"entries"`
	LatestID int64      `json:"latest_id"`
}

type commandAPIResponse struct {
	Result         *CommandResult `json:"result,omitempty"`
	RefreshStarted bool           `json:"refresh_started,omitempty"`
	Settings       *State         `json:"settings,omitempty"`
}

type updateAllAPIResponse struct {
	Results        []UpdateResult `json:"results"`
	RefreshStarted bool           `json:"refresh_started"`
}

func commandResponse(result CommandResult) commandAPIResponse {
	return commandAPIResponse{Result: &result}
}

func refreshedCommandResponse(result CommandResult) commandAPIResponse {
	return commandAPIResponse{Result: &result, RefreshStarted: true}
}

func settingsResponse(state State) commandAPIResponse {
	return commandAPIResponse{Settings: &state}
}

func settingsCommandResponse(state State, result CommandResult) commandAPIResponse {
	return commandAPIResponse{Result: &result, Settings: &state}
}

func parsePackageAction(r *http.Request, command string) (string, string, *CommandResult) {
	_ = r.ParseForm()
	manager := r.Form.Get("manager")
	id := r.Form.Get("package_id")
	if err := validateManagerAndID(manager, id); err != nil {
		result := validationCommandResult(command, err)
		return "", "", &result
	}
	return manager, id, nil
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
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		manager, id, invalid := parsePackageAction(r, "install")
		if invalid != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse(*invalid))
			return
		}
		result := installPackage(manager, id)
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, refreshedCommandResponse(result))
	case "/api/managers/install":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		manager := r.Form.Get("manager")
		if !isManagedPackageManager(manager) {
			result := validationCommandResult("manager install", managerValidationError())
			writeJSON(w, http.StatusBadRequest, commandResponse(result))
			return
		}
		result := installManager(manager)
		app.refreshStatus(true)
		writeJSON(w, http.StatusOK, commandResponse(result))
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
		manager, id, invalid := parsePackageAction(r, "update")
		if invalid != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse(*invalid))
			return
		}
		result := updatePackage(manager, id)
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, refreshedCommandResponse(result))
	case "/api/update-all":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		for _, key := range r.Form["package_key"] {
			if err := validatePackageKey(key); err != nil {
				result := UpdateResult{Key: key, Result: validationCommandResult("update-all", err)}
				writeJSON(w, http.StatusBadRequest, updateAllAPIResponse{Results: []UpdateResult{result}, RefreshStarted: false})
				return
			}
		}
		results := updateAll(r.Form["package_key"])
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, updateAllAPIResponse{Results: results, RefreshStarted: true})
	case "/api/settings/startup":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		_ = r.ParseForm()
		enabled, _ := formBool(r, "enabled")
		result := setStartup(enabled)
		app.refreshStatus(true)
		writeJSON(w, http.StatusOK, commandResponse(result))
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
		writeJSON(w, http.StatusOK, settingsCommandResponse(state, result))
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
		writeJSON(w, http.StatusOK, settingsResponse(state))
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
	default:
		http.NotFound(w, r)
	}
}

func (app *App) render(w http.ResponseWriter, r *http.Request, data PageData) {
	state := loadState()
	data.Token = app.token
	data.Admin = isAdmin()
	data.StateDir, _ = stateDir()
	data.Theme = state.Theme

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
