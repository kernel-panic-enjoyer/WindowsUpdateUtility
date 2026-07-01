package updater

import (
	"errors"
	"net/http"
)

type packageActionRequest struct {
	Manager   string `json:"manager"`
	PackageID string `json:"package_id"`
}

func packageActionRequestFromForm(r *http.Request) packageActionRequest {
	_ = r.ParseForm()
	return packageActionRequest{
		Manager:   r.Form.Get("manager"),
		PackageID: r.Form.Get("package_id"),
	}
}

func validatePackageActionRequest(commandName string, actionRequest packageActionRequest) (string, string, *CommandResult) {
	if err := validateManagerAndID(actionRequest.Manager, actionRequest.PackageID); err != nil {
		result := validationCommandResult(commandName, err)
		return "", "", &result
	}
	return actionRequest.Manager, actionRequest.PackageID, nil
}

func parsePackageAction(r *http.Request, commandName string) (string, string, *CommandResult) {
	var actionRequest packageActionRequest
	if requestIsJSON(r) {
		if err := decodeJSONRequest(r, &actionRequest); err != nil {
			result := validationCommandResult(commandName, err)
			return "", "", &result
		}
	} else {
		actionRequest = packageActionRequestFromForm(r)
	}
	return validatePackageActionRequest(commandName, actionRequest)
}

func parsePackageUpdateAction(r *http.Request) (string, string, UpdateOptions, *CommandResult) {
	var actionRequest packageActionRequest
	var options UpdateOptions
	if requestIsJSON(r) {
		var jsonPayload struct {
			Manager             string `json:"manager"`
			PackageID           string `json:"package_id"`
			AllowUnknownVersion bool   `json:"allow_unknown_version"`
			AllowPinned         bool   `json:"allow_pinned"`
		}
		if err := decodeJSONRequest(r, &jsonPayload); err != nil {
			result := validationCommandResult("update", err)
			return "", "", UpdateOptions{}, &result
		}
		actionRequest = packageActionRequest{
			Manager:   jsonPayload.Manager,
			PackageID: jsonPayload.PackageID,
		}
		options = UpdateOptions{
			AllowUnknownVersion: jsonPayload.AllowUnknownVersion,
			AllowPinned:         jsonPayload.AllowPinned,
		}
	} else {
		actionRequest = packageActionRequestFromForm(r)
		options = updateOptionsFromForm(r)
	}
	manager, packageID, validationFailure := validatePackageActionRequest("update", actionRequest)
	if validationFailure != nil {
		return "", "", UpdateOptions{}, validationFailure
	}
	return manager, packageID, options, nil
}

func parseManagerRequest(r *http.Request) (string, *CommandResult) {
	if requestIsJSON(r) {
		var jsonPayload struct {
			Manager string `json:"manager"`
		}
		if err := decodeJSONRequest(r, &jsonPayload); err != nil {
			result := validationCommandResult("manager install", err)
			return "", &result
		}
		return jsonPayload.Manager, nil
	}
	_ = r.ParseForm()
	return r.Form.Get("manager"), nil
}

func updateOptionsFromForm(r *http.Request) UpdateOptions {
	allowUnknownVersion, _ := formBool(r, "allow_unknown_version")
	allowPinned, _ := formBool(r, "allow_pinned")
	return UpdateOptions{
		AllowUnknownVersion: allowUnknownVersion,
		AllowPinned:         allowPinned,
	}
}

func parseUpdateAllRequest(r *http.Request) ([]string, UpdateOptions, *UpdateResult) {
	if requestIsJSON(r) {
		var jsonPayload struct {
			PackageKey          oneOrManyStrings `json:"package_key"`
			PackageKeys         oneOrManyStrings `json:"package_keys"`
			AllowUnknownVersion bool             `json:"allow_unknown_version"`
			AllowPinned         bool             `json:"allow_pinned"`
		}
		if err := decodeJSONRequest(r, &jsonPayload); err != nil {
			result := UpdateResult{Result: validationCommandResult("update-all", err)}
			return nil, UpdateOptions{}, &result
		}
		return combineStringLists(jsonPayload.PackageKey, jsonPayload.PackageKeys), UpdateOptions{
			AllowUnknownVersion: jsonPayload.AllowUnknownVersion,
			AllowPinned:         jsonPayload.AllowPinned,
		}, nil
	}
	_ = r.ParseForm()
	return r.Form["package_key"], updateOptionsFromForm(r), nil
}

func writeUpdateAllValidationFailure(w http.ResponseWriter, result UpdateResult) {
	results := []UpdateResult{result}
	writeJSON(w, http.StatusBadRequest, UpdateJobStatus{
		Results:        results,
		RefreshStarted: false,
		Notice:         updateResultsFailureNotice(results),
	})
}

func (app *App) handleInstallAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	manager, packageID, validationFailure := parsePackageAction(r, "install")
	if validationFailure != nil {
		writeJSON(w, http.StatusBadRequest, commandResponse(*validationFailure))
		return
	}
	jobAcceptedResponse(w, app.startInstallJob(manager, packageID))
}

func (app *App) handleManagerInstallAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	manager, validationFailure := parseManagerRequest(r)
	if validationFailure != nil {
		writeJSON(w, http.StatusBadRequest, commandResponse(*validationFailure))
		return
	}
	if !isManagedPackageManager(manager) {
		result := validationCommandResult("manager install", managerValidationError())
		writeJSON(w, http.StatusBadRequest, commandResponse(result))
		return
	}
	jobAcceptedResponse(w, app.startManagerInstallJob(manager))
}

func (app *App) handleUpdateAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	manager, packageID, options, validationFailure := parsePackageUpdateAction(r)
	if validationFailure != nil {
		writeJSON(w, http.StatusBadRequest, commandResponse(*validationFailure))
		return
	}
	jobAcceptedResponse(w, app.startSingleUpdateJob(manager, packageID, options))
}

func (app *App) handleUpdateAllAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	packageKeys, options, validationFailure := parseUpdateAllRequest(r)
	if validationFailure != nil {
		writeUpdateAllValidationFailure(w, *validationFailure)
		return
	}
	for _, key := range packageKeys {
		if err := validatePackageKey(key); err != nil {
			writeUpdateAllValidationFailure(w, UpdateResult{Key: key, Result: validationCommandResult("update-all", err)})
			return
		}
	}
	status, err := app.startBulkUpdateJob(packageKeys, options)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errUpdateJobRunning) {
			code = http.StatusConflict
		}
		status.Error = err.Error()
		writeJSON(w, code, status)
		return
	}
	jobAcceptedResponse(w, status)
}

func (app *App) handleUpdateAllStatusAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	status := app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
	if status.JobID != "" {
		writeJSON(w, http.StatusOK, status)
		return
	}
	writeJSON(w, http.StatusOK, app.updateJobStatus())
}

func (app *App) handleUpdateAllCancelAPI(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	status := app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
	if status.JobID == "" {
		writeJSON(w, http.StatusOK, app.cancelUpdateJob())
		return
	}
	cancelled, ok := app.cancelOperationJob(status.JobID)
	if !ok {
		writeJSON(w, http.StatusOK, OperationJobStatus{})
		return
	}
	writeJSON(w, http.StatusOK, cancelled)
}
