package updater

import "net/http"

func setThemePreference(theme string) (State, error) {
	state := loadState()
	if theme == "light" {
		state.Theme = "light"
	} else {
		state.Theme = "dark"
	}
	return state, saveAppState(state)
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
			Global         *bool            `json:"global"`
			PackageKey     oneOrManyStrings `json:"package_key"`
			PackageKeys    oneOrManyStrings `json:"package_keys"`
			PackageEnabled *bool            `json:"package_enabled"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			result := validationCommandResult("auto-update settings", err)
			return nil, nil, nil, &result
		}
		return payload.Global, combineStringLists(payload.PackageKey, payload.PackageKeys), payload.PackageEnabled, nil
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

func (app *App) handleStartupSettingsAPI(w http.ResponseWriter, r *http.Request) {
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
}

func (app *App) handleAutoUpdateSettingsAPI(w http.ResponseWriter, r *http.Request) {
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
}

func (app *App) handleThemeSettingsAPI(w http.ResponseWriter, r *http.Request) {
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
}
