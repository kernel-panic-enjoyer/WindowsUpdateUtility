package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	packageActionRetryGap    = 2 * time.Second
	packageActionMaxAttempts = 3
	packageActionTimeout     = time.Hour
)

var packageActionCommandRunner = runCommandContext
var packageActionManagerAvailable = func(manager string) bool {
	return detectManager(manager).Available
}
var packageActionRetryWait = func(ctx context.Context) bool {
	timer := time.NewTimer(packageActionRetryGap)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func validateManagerAndID(manager, id string) error {
	if !isManagedPackageManager(manager) {
		return managerValidationError()
	}
	id = strings.TrimSpace(id)
	if manager == managerStore || manager == managerWinget {
		if id == "" || len(id) > 240 || containsBlockedPackageActionChar(id) || isOptionLikePackageTarget(id) {
			if manager == managerStore {
				return errors.New("store package id or query contains unsupported characters")
			}
			return errors.New("winget package id or query contains unsupported characters")
		}
		return nil
	}
	if id == "" || isOptionLikePackageTarget(id) || !isSafePackageID(id) {
		return errors.New("package id contains unsupported characters")
	}
	return nil
}

func isSafePackageID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if isASCIIAlphaNumeric(r) || r == '_' || r == '.' || r == '+' || r == '-' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func containsBlockedPackageActionChar(value string) bool {
	for _, r := range value {
		switch r {
		case 0, '\r', '\n', '&', '|', '<', '>', '^', '"', '%':
			return true
		}
	}
	return false
}

func isOptionLikePackageTarget(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "-")
}

func isASCIIAlphaNumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func installPackageContext(ctx context.Context, manager, id string) CommandResult {
	if err := validateManagerAndID(manager, id); err != nil {
		return validationCommandResult("install", err)
	}
	appLog("Install started for %s:%s.", manager, id)
	if result := runPrivilegedPackageInstall(ctx, manager, id); result.Command != "" || result.Code != 0 || result.OK {
		appLog("Install finished for %s:%s with code %d.", manager, id, result.Code)
		return result
	}
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerStore:
		result = runStoreInstallWithFallbackContext(ctx, id)
	case managerWinget:
		result = runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(manager, id, false)...)
	case managerChoco:
		result = runPackageActionCommand(ctx, managerChoco, packageActionTimeout, chocoPackageCommand("install", id)...)
	}
	appLog("Install finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func updatePackageWithMetadataContext(ctx context.Context, pkg Package) CommandResult {
	manager := strings.TrimSpace(pkg.Manager)
	id := strings.TrimSpace(pkg.ID)
	if err := validateManagerAndID(manager, id); err != nil {
		return validationCommandResult("update", err)
	}
	appLog("Update started for %s:%s.", manager, id)
	if result := runPrivilegedPackageUpdate(ctx, pkg); result.Command != "" || result.Code != 0 || result.OK {
		appLog("Update finished for %s:%s with code %d.", manager, id, result.Code)
		return result
	}
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerStore:
		if pkg.UpdateState != "" {
			result = runExactStoreUpdateWithVerification(ctx, pkg)
		} else {
			result = validationCommandResult("update", errors.New("Store update requires the exact assessment path"))
		}
	case managerWinget:
		result = runWingetUpgradePackageWithInstallFallbackContext(ctx, manager, pkg)
	case managerChoco:
		result = runChocoUpgradePackageWithFallbackContext(ctx, pkg)
	}
	appLog("Update finished for %s:%s with code %d.", manager, id, result.Code)
	return result
}

func runPackageActionCommand(ctx context.Context, manager string, timeout time.Duration, args ...string) CommandResult {
	result := runNormalizedPackageAction(ctx, manager, timeout, args...)
	if result.OK || ctx.Err() != nil {
		return result
	}
	if manager == managerWinget && isWingetSourceFailure(result) {
		appLog("Winget command failed because source metadata looked stale or unavailable; updating sources before retry.")
		repair := runNormalizedPackageAction(ctx, managerWinget, managerDetectionTimeout, wingetSourceUpdateCommand()...)
		merged := mergeCommandAttemptsWithFinalResult(result, repair, "winget source update")
		if ctx.Err() != nil {
			return merged
		}
		if !repair.OK {
			if !isWingetSourceFailure(repair) {
				return merged
			}
			appLog("Winget source update failed with source metadata errors; resetting sources before retry.")
			reset := runNormalizedPackageAction(ctx, managerWinget, managerDetectionTimeout, wingetSourceResetCommand()...)
			merged = mergeCommandAttemptsWithFinalResult(merged, reset, "winget source reset")
			if ctx.Err() != nil || !reset.OK {
				return merged
			}
			retry := runNormalizedPackageAction(ctx, manager, timeout, args...)
			result = mergeCommandAttemptsWithFinalResult(merged, retry, "winget retry after source reset")
			return retryTransientPackageAction(ctx, manager, timeout, result, args...)
		}
		retry := runNormalizedPackageAction(ctx, manager, timeout, args...)
		result = mergeCommandAttemptsWithFinalResult(merged, retry, "winget retry after source update")
	}
	return retryTransientPackageAction(ctx, manager, timeout, result, args...)
}

func runNormalizedPackageAction(ctx context.Context, manager string, timeout time.Duration, args ...string) CommandResult {
	return normalizePackageActionResult(manager, packageActionCommandRunner(ctx, timeout, args...))
}

func retryTransientPackageAction(ctx context.Context, manager string, timeout time.Duration, result CommandResult, args ...string) CommandResult {
	for attempt := 2; attempt <= packageActionMaxAttempts; attempt++ {
		if result.OK || ctx.Err() != nil || !isTransientPackageManagerFailure(manager, result) {
			return result
		}
		appLog("%s command failed with transient code %d; retrying attempt %d/%d.", manager, result.Code, attempt, packageActionMaxAttempts)
		if !packageActionRetryWait(ctx) {
			return result
		}
		retry := runNormalizedPackageAction(ctx, manager, timeout, args...)
		result = mergeCommandAttemptsWithFinalResult(result, retry, fmt.Sprintf("%s retry %d", manager, attempt-1))
	}
	return result
}

func installManager(manager string) CommandResult {
	return installManagerContext(context.Background(), manager)
}

func installManagerContext(ctx context.Context, manager string) CommandResult {
	appLog("Package manager install action started for %s.", manager)
	invalidateManagerDetectionCache()
	defer invalidateManagerDetectionCache()
	if manager == managerChoco && !isAdmin() {
		result := runElevatedWorkerOperation(ctx, elevatedWorkerInvocation{
			Operation: workerOperationManagerInstall,
			Payload:   elevatedWorkerManagerInstallPayload{Manager: manager},
		})
		if result.OK {
			refreshProcessEnvironmentFromRegistry()
		}
		appLog("Package manager install action finished for %s with code %d.", manager, result.Code)
		return result
	}
	var result CommandResult
	switch manager {
	case managerWinget:
		// Manager bootstrap handoffs intentionally detach: these open trusted
		// Windows UI surfaces or a browser page and do not represent a mutable
		// package-manager command that this process can own with a Job Object.
		err := openURL("ms-appinstaller:?source=https://aka.ms/getwinget")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open winget installer"}
			break
		}
		result = CommandResult{OK: true, Command: "open winget installer", Stdout: "Opened Microsoft App Installer for winget."}
	case managerStore:
		var messages []string
		opened := false
		if err := openURL("ms-windows-store://downloadsandupdates"); err != nil {
			messages = append(messages, "Could not open Microsoft Store updates: "+err.Error())
		} else {
			opened = true
			messages = append(messages, "Opened Microsoft Store Downloads and updates.")
		}
		if err := openURL("ms-settings:windowsupdate"); err != nil {
			messages = append(messages, "Could not open Windows Update settings: "+err.Error())
		} else {
			opened = true
			messages = append(messages, "Opened Windows Update settings.")
		}
		result = CommandResult{OK: opened, Command: "open Store CLI update surfaces", Stdout: strings.Join(messages, "\n")}
		if !opened {
			result.Code = 1
			result.Stderr = result.Stdout
			result.Stdout = ""
		}
	case managerChoco:
		if detectManager(managerWinget).Available {
			result = installPackageContext(ctx, managerWinget, "Chocolatey.Chocolatey")
			break
		}
		err := openURL("https://chocolatey.org/install")
		if err != nil {
			result = CommandResult{Code: 1, Stderr: err.Error(), Command: "open chocolatey install page"}
			break
		}
		result = CommandResult{OK: true, Command: "open chocolatey install page", Stdout: "Opened Chocolatey install page because winget is unavailable."}
	default:
		result = validationCommandResult("manager install", errors.New("unknown manager"))
	}
	if result.OK {
		refreshProcessEnvironmentFromRegistry()
	}
	appLog("Package manager install action finished for %s with code %d.", manager, result.Code)
	return result
}
