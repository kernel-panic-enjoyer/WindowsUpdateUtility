package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Notice         string         `json:"notice,omitempty"`
}

type updateAllAPIResponse = UpdateJobStatus

type stringList []string

func (list *stringList) UnmarshalJSON(data []byte) error {
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*list = many
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*list = []string{one}
		return nil
	}
	if strings.TrimSpace(string(data)) == "null" {
		*list = nil
		return nil
	}
	return fmt.Errorf("expected string or string array")
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

func requestIsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json")
}

func decodeJSONRequest(r *http.Request, target any) error {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func parsePackageAction(r *http.Request, command string) (string, string, *CommandResult) {
	var manager string
	var id string
	if requestIsJSON(r) {
		var payload struct {
			Manager   string `json:"manager"`
			PackageID string `json:"package_id"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := validationCommandResult(command, err)
			return "", "", &result
		}
		manager = payload.Manager
		id = payload.PackageID
	} else {
		_ = r.ParseForm()
		manager = r.Form.Get("manager")
		id = r.Form.Get("package_id")
	}
	if err := validateManagerAndID(manager, id); err != nil {
		result := validationCommandResult(command, err)
		return "", "", &result
	}
	return manager, id, nil
}

func parseManagerRequest(r *http.Request) (string, *CommandResult) {
	if requestIsJSON(r) {
		var payload struct {
			Manager string `json:"manager"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := validationCommandResult("manager install", err)
			return "", &result
		}
		return payload.Manager, nil
	}
	_ = r.ParseForm()
	return r.Form.Get("manager"), nil
}

func parseUpdateAllPackageKeys(r *http.Request) ([]string, *UpdateResult) {
	if requestIsJSON(r) {
		var payload struct {
			PackageKey  stringList `json:"package_key"`
			PackageKeys stringList `json:"package_keys"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := UpdateResult{Result: validationCommandResult("update-all", err)}
			return nil, &result
		}
		keys := append([]string{}, payload.PackageKey...)
		keys = append(keys, payload.PackageKeys...)
		return keys, nil
	}
	_ = r.ParseForm()
	return r.Form["package_key"], nil
}

func parseStartupRequest(r *http.Request) (bool, *CommandResult) {
	if requestIsJSON(r) {
		var payload struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := validationCommandResult("startup settings", err)
			return false, &result
		}
		if payload.Enabled == nil {
			return false, nil
		}
		return *payload.Enabled, nil
	}
	_ = r.ParseForm()
	enabled, _ := formBool(r, "enabled")
	return enabled, nil
}

func parseAutoUpdateRequest(r *http.Request) (*bool, []string, *bool, *CommandResult) {
	if requestIsJSON(r) {
		var payload struct {
			Global         *bool      `json:"global"`
			PackageKey     stringList `json:"package_key"`
			PackageKeys    stringList `json:"package_keys"`
			PackageEnabled *bool      `json:"package_enabled"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := validationCommandResult("auto-update settings", err)
			return nil, nil, nil, &result
		}
		keys := append([]string{}, payload.PackageKey...)
		keys = append(keys, payload.PackageKeys...)
		return payload.Global, keys, payload.PackageEnabled, nil
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
	return global, r.Form["package_key"], packageEnabled, nil
}

func parseThemeRequest(r *http.Request) (string, error) {
	if requestIsJSON(r) {
		var payload struct {
			Theme string `json:"theme"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			return "", err
		}
		return payload.Theme, nil
	}
	_ = r.ParseForm()
	return r.Form.Get("theme"), nil
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
		manager, invalid := parseManagerRequest(r)
		if invalid != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse(*invalid))
			return
		}
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
		response := refreshedCommandResponse(result)
		response.Notice = updateFailureNotice(result)
		writeJSON(w, http.StatusOK, response)
	case "/api/update-all/status":
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, app.updateJobStatus())
	case "/api/update-all/cancel":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		writeJSON(w, http.StatusOK, app.cancelUpdateJob())
	case "/api/update-all":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		packageKeys, invalid := parseUpdateAllPackageKeys(r)
		if invalid != nil {
			results := []UpdateResult{*invalid}
			writeJSON(w, http.StatusBadRequest, updateAllAPIResponse{Results: results, RefreshStarted: false, Notice: updateAllFailureNotice(results)})
			return
		}
		for _, key := range packageKeys {
			if err := validatePackageKey(key); err != nil {
				result := UpdateResult{Key: key, Result: validationCommandResult("update-all", err)}
				results := []UpdateResult{result}
				writeJSON(w, http.StatusBadRequest, updateAllAPIResponse{Results: results, RefreshStarted: false, Notice: updateAllFailureNotice(results)})
				return
			}
		}
		status, err := app.startUpdateJob(packageKeys)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errUpdateJobRunning) {
				code = http.StatusConflict
			}
			status.Error = err.Error()
			writeJSON(w, code, status)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case "/api/settings/startup":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		enabled, invalid := parseStartupRequest(r)
		if invalid != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse(*invalid))
			return
		}
		result := setStartup(enabled)
		app.refreshStatus(true)
		writeJSON(w, http.StatusOK, commandResponse(result))
	case "/api/settings/auto-update":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		global, packageKeys, packageEnabled, invalid := parseAutoUpdateRequest(r)
		if invalid != nil {
			writeJSON(w, http.StatusBadRequest, commandResponse(*invalid))
			return
		}
		state, result := setAutoUpdate(global, packageKeys, packageEnabled)
		app.refreshStatus(true)
		app.refreshInventory(true)
		writeJSON(w, http.StatusOK, settingsCommandResponse(state, result))
	case "/api/settings/theme":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		theme, err := parseThemeRequest(r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		state, err := setThemePreference(theme)
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
