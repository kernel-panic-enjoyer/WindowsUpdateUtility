package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	packageActionRetryDelay  = 2 * time.Second
	packageActionMaxAttempts = 3
	// Mutable package-manager commands can hand off to silent installers. Keep
	// the timeout tight enough that a hidden installer cannot leave an update
	// job looking stuck for an hour.
	packageActionTimeout = 20 * time.Minute
)

var packageActionCommandRunner = runCommandContext
var packageActionManagerAvailable = func(packageManager string) bool {
	return detectManager(packageManager).Available
}
var packageActionRetryWait = func(ctx context.Context) bool {
	timer := time.NewTimer(packageActionRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func validateManagerAndID(manager, packageIDOrQuery string) error {
	if !isManagedPackageManager(manager) {
		return managerValidationError()
	}
	target := strings.TrimSpace(packageIDOrQuery)
	if manager == managerStore || manager == managerWinget {
		if target == "" || len(target) > 240 || containsBlockedPackageActionChar(target) || isOptionLikePackageTarget(target) {
			if manager == managerStore {
				return errors.New("store package id or query contains unsupported characters")
			}
			return errors.New("winget package id or query contains unsupported characters")
		}
		return nil
	}
	if target == "" || isOptionLikePackageTarget(target) || !isSafePackageID(target) {
		return errors.New("package id contains unsupported characters")
	}
	return nil
}

func isSafePackageID(packageID string) bool {
	if packageID == "" {
		return false
	}
	for _, character := range packageID {
		if isASCIIAlphaNumeric(character) || character == '_' || character == '.' || character == '+' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func containsBlockedPackageActionChar(target string) bool {
	for _, character := range target {
		switch character {
		case 0, '\r', '\n', '&', '|', '<', '>', '^', '"', '%':
			return true
		}
	}
	return false
}

func isOptionLikePackageTarget(target string) bool {
	return strings.HasPrefix(strings.TrimSpace(target), "-")
}

func isASCIIAlphaNumeric(character rune) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')
}

func installPackageContext(ctx context.Context, manager, packageIDOrQuery string) CommandResult {
	if err := validateManagerAndID(manager, packageIDOrQuery); err != nil {
		return validationCommandResult("install", err)
	}
	appLog("Install started for %s:%s.", manager, packageIDOrQuery)
	if result := runPrivilegedPackageInstall(ctx, manager, packageIDOrQuery); result.Command != "" || result.Code != 0 || result.OK {
		appLog("Install finished for %s:%s with code %d.", manager, packageIDOrQuery, result.Code)
		return result
	}
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch manager {
	case managerStore:
		result = runStoreInstallWithFallbackContext(ctx, packageIDOrQuery)
	case managerWinget:
		result = runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetInstallCommand(manager, packageIDOrQuery, false)...)
	case managerChoco:
		result = runPackageActionCommand(ctx, managerChoco, packageActionTimeout, chocoPackageCommand("install", packageIDOrQuery)...)
	}
	appLog("Install finished for %s:%s with code %d.", manager, packageIDOrQuery, result.Code)
	return result
}

func updatePackageWithMetadataContext(ctx context.Context, pkg Package) CommandResult {
	packageManager := strings.TrimSpace(pkg.Manager)
	packageID := strings.TrimSpace(pkg.ID)
	if err := validateManagerAndID(packageManager, packageID); err != nil {
		return validationCommandResult("update", err)
	}
	appLog("Update started for %s:%s.", packageManager, packageID)
	if result := runPrivilegedPackageUpdate(ctx, pkg); result.Command != "" || result.Code != 0 || result.OK {
		appLog("Update finished for %s:%s with code %d.", packageManager, packageID, result.Code)
		return result
	}
	defer invalidateManagerDetectionCache()
	var result CommandResult
	switch packageManager {
	case managerStore:
		if pkg.UpdateState != "" {
			result = runExactStoreUpdateWithVerification(ctx, pkg)
		} else {
			result = validationCommandResult("update", errors.New("Store update requires the exact assessment path"))
		}
	case managerWinget:
		result = runWingetUpgradePackageWithInstallFallbackContext(ctx, packageManager, pkg)
	case managerChoco:
		result = runChocoUpgradePackageWithFallbackContext(ctx, pkg)
	}
	appLog("Update finished for %s:%s with code %d.", packageManager, packageID, result.Code)
	return result
}

func runPackageActionCommand(ctx context.Context, packageManager string, timeout time.Duration, args ...string) CommandResult {
	actionResult := runPackageActionAttempt(ctx, packageManager, timeout, args...)
	if actionResult.OK || ctx.Err() != nil {
		return actionResult
	}
	if packageManager == managerWinget && isWingetSourceFailure(actionResult) {
		appLog("Winget command failed because source metadata looked stale or unavailable; updating sources before retry.")
		sourceUpdateResult := runPackageActionAttempt(ctx, managerWinget, managerDetectionTimeout, wingetSourceUpdateCommand()...)
		actionResult = mergeCommandAttemptsWithFinalResult(actionResult, sourceUpdateResult, "winget source update")
		if ctx.Err() != nil {
			return actionResult
		}
		retryLabel := "winget retry after source update"
		if !sourceUpdateResult.OK {
			if !isWingetSourceFailure(sourceUpdateResult) {
				return actionResult
			}
			appLog("Winget source update failed with source metadata errors; resetting sources before retry.")
			sourceResetResult := runPackageActionAttempt(ctx, managerWinget, managerDetectionTimeout, wingetSourceResetCommand()...)
			actionResult = mergeCommandAttemptsWithFinalResult(actionResult, sourceResetResult, "winget source reset")
			if ctx.Err() != nil || !sourceResetResult.OK {
				return actionResult
			}
			retryLabel = "winget retry after source reset"
		}
		retryResult := runPackageActionAttempt(ctx, packageManager, timeout, args...)
		actionResult = mergeCommandAttemptsWithFinalResult(actionResult, retryResult, retryLabel)
	}
	return retryPackageActionOnTransientFailure(ctx, packageManager, timeout, actionResult, args...)
}

func runPackageActionAttempt(ctx context.Context, packageManager string, timeout time.Duration, args ...string) CommandResult {
	return normalizePackageActionResult(packageManager, packageActionCommandRunner(ctx, timeout, args...))
}

func retryPackageActionOnTransientFailure(ctx context.Context, packageManager string, timeout time.Duration, currentResult CommandResult, args ...string) CommandResult {
	for attempt := 2; attempt <= packageActionMaxAttempts; attempt++ {
		if currentResult.OK || ctx.Err() != nil || !isTransientPackageManagerFailure(packageManager, currentResult) {
			return currentResult
		}
		appLog("%s command failed with transient code %d; retrying attempt %d/%d.", packageManager, currentResult.Code, attempt, packageActionMaxAttempts)
		if !packageActionRetryWait(ctx) {
			return currentResult
		}
		retryResult := runPackageActionAttempt(ctx, packageManager, timeout, args...)
		currentResult = mergeCommandAttemptsWithFinalResult(currentResult, retryResult, fmt.Sprintf("%s retry %d", packageManager, attempt-1))
	}
	return currentResult
}

func installManager(packageManager string) CommandResult {
	return installManagerContext(context.Background(), packageManager)
}

func installManagerContext(ctx context.Context, packageManager string) CommandResult {
	appLog("Package manager install action started for %s.", packageManager)
	invalidateManagerDetectionCache()
	defer invalidateManagerDetectionCache()
	if packageManager == managerChoco && !isAdmin() {
		result := runElevatedWorkerOperation(ctx, elevatedWorkerInvocation{
			Operation: workerOperationManagerInstall,
			Payload:   elevatedWorkerManagerInstallPayload{Manager: packageManager},
		})
		if result.OK {
			refreshProcessEnvironmentFromRegistry()
		}
		appLog("Package manager install action finished for %s with code %d.", packageManager, result.Code)
		return result
	}
	var result CommandResult
	switch packageManager {
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
	appLog("Package manager install action finished for %s with code %d.", packageManager, result.Code)
	return result
}
