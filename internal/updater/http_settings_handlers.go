package updater

import (
	"context"
	"net/http"
	"strings"
)

func setThemePreference(requestedTheme string) (State, error) {
	stateStore, err := defaultStateStore()
	if err != nil {
		return State{}, err
	}
	return setThemePreferenceWithStore(context.Background(), stateStore, requestedTheme)
}

func setThemePreferenceWithStore(ctx context.Context, stateStore StateStore, requestedTheme string) (State, error) {
	themePreference := "dark"
	if requestedTheme == "light" {
		themePreference = "light"
	}
	return stateStore.Update(ctx, func(state *State) error {
		state.Theme = themePreference
		return nil
	})
}

func setAppUpdatePromptDismissedVersion(dismissedVersion string) (State, error) {
	stateStore, err := defaultStateStore()
	if err != nil {
		return State{}, err
	}
	return setAppUpdatePromptDismissedVersionWithStore(context.Background(), stateStore, dismissedVersion)
}

func setAppUpdatePromptDismissedVersionWithStore(ctx context.Context, stateStore StateStore, dismissedVersion string) (State, error) {
	dismissedVersion = strings.TrimSpace(dismissedVersion)
	return stateStore.Update(ctx, func(state *State) error {
		state.AppUpdatePromptDismissedVersion = dismissedVersion
		return nil
	})
}

func parseStartupRequest(r *http.Request) (bool, *CommandResult) {
	if requestIsJSON(r) {
		var startupSettings struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSONRequest(r, &startupSettings); err != nil {
			result := validationCommandResult("startup settings", err)
			return false, &result
		}
		if startupSettings.Enabled == nil {
			return false, nil
		}
		return *startupSettings.Enabled, nil
	}
	_ = r.ParseForm()
	startupEnabled, _ := formBool(r, "enabled")
	return startupEnabled, nil
}

func parseAutoUpdateRequest(r *http.Request) (*bool, []string, *bool, *CommandResult) {
	if requestIsJSON(r) {
		var autoUpdateSettings struct {
			Global         *bool            `json:"global"`
			PackageKey     oneOrManyStrings `json:"package_key"`
			PackageKeys    oneOrManyStrings `json:"package_keys"`
			PackageEnabled *bool            `json:"package_enabled"`
		}
		if err := decodeJSONRequest(r, &autoUpdateSettings); err != nil {
			result := validationCommandResult("auto-update settings", err)
			return nil, nil, nil, &result
		}
		packageKeys := combineStringLists(autoUpdateSettings.PackageKey, autoUpdateSettings.PackageKeys)
		return autoUpdateSettings.Global, packageKeys, autoUpdateSettings.PackageEnabled, nil
	}
	_ = r.ParseForm()
	var globalAutoUpdateEnabled *bool
	if value, ok := formBool(r, "global"); ok {
		globalAutoUpdateEnabled = &value
	}
	var packageAutoUpdateEnabled *bool
	if value, ok := formBool(r, "package_enabled"); ok {
		packageAutoUpdateEnabled = &value
	}
	return globalAutoUpdateEnabled, r.Form["package_key"], packageAutoUpdateEnabled, nil
}

func parseThemeRequest(r *http.Request) (string, error) {
	if requestIsJSON(r) {
		var themeSettings struct {
			Theme string `json:"theme"`
		}
		if err := decodeJSONRequest(r, &themeSettings); err != nil {
			return "", err
		}
		return themeSettings.Theme, nil
	}
	_ = r.ParseForm()
	return r.Form.Get("theme"), nil
}

func parseAppUpdatePromptRequest(r *http.Request) (string, error) {
	var dismissedVersion string
	if requestIsJSON(r) {
		var appUpdatePromptSettings struct {
			Version string `json:"version"`
		}
		if err := decodeJSONRequest(r, &appUpdatePromptSettings); err != nil {
			return "", err
		}
		dismissedVersion = appUpdatePromptSettings.Version
	} else {
		_ = r.ParseForm()
		dismissedVersion = r.Form.Get("version")
	}
	return strings.TrimSpace(dismissedVersion), nil
}

func (app *App) handleStartupSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	startupEnabled, validationFailure := parseStartupRequest(r)
	if validationFailure != nil {
		writeJSON(w, http.StatusBadRequest, commandResponse(*validationFailure))
		return
	}
	result := setStartup(startupEnabled)
	app.refreshStatus(true)
	writeJSON(w, http.StatusOK, commandResponse(result))
}

func (app *App) handleAutoUpdateSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	globalAutoUpdateEnabled, packageKeys, packageAutoUpdateEnabled, validationFailure := parseAutoUpdateRequest(r)
	if validationFailure != nil {
		writeJSON(w, http.StatusBadRequest, commandResponse(*validationFailure))
		return
	}
	state, result := setAutoUpdate(globalAutoUpdateEnabled, packageKeys, packageAutoUpdateEnabled)
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
	dismissedVersion, err := parseAppUpdatePromptRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, err := setAppUpdatePromptDismissedVersion(dismissedVersion)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(state))
}
