package updater

import (
	"context"
	"net/http"
	"strings"
)

func setThemePreference(theme string) (State, error) {
	store, err := defaultStateStore()
	if err != nil {
		return State{}, err
	}
	return setThemePreferenceWithStore(context.Background(), store, theme)
}

func setThemePreferenceWithStore(ctx context.Context, store StateStore, theme string) (State, error) {
	return store.Update(ctx, func(state *State) error {
		if theme == "light" {
			state.Theme = "light"
		} else {
			state.Theme = "dark"
		}
		return nil
	})
}

func setAppUpdatePromptDismissedVersion(version string) (State, error) {
	store, err := defaultStateStore()
	if err != nil {
		return State{}, err
	}
	return setAppUpdatePromptDismissedVersionWithStore(context.Background(), store, version)
}

func setAppUpdatePromptDismissedVersionWithStore(ctx context.Context, store StateStore, version string) (State, error) {
	version = strings.TrimSpace(version)
	return store.Update(ctx, func(state *State) error {
		state.AppUpdatePromptDismissedVersion = version
		return nil
	})
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

func parseAppUpdatePromptRequest(r *http.Request) (string, error) {
	if requestIsJSON(r) {
		var payload struct {
			Version string `json:"version"`
		}
		if err := decodeJSONRequest(r, &payload); err != nil {
			return "", err
		}
		return strings.TrimSpace(payload.Version), nil
	}
	_ = r.ParseForm()
	return strings.TrimSpace(r.Form.Get("version")), nil
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

func (app *App) handleAppUpdatePromptSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	version, err := parseAppUpdatePromptRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := setAppUpdatePromptDismissedVersion(version)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(state))
}
