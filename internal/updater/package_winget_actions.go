package updater

import (
	"context"
	"fmt"
)

func wingetSourceArg(packageManager string) string {
	if packageManager == managerStore {
		return sourceMSStore
	}
	return sourceWinget
}

var wingetInstallUpgradeFlags = []string{
	"--accept-package-agreements",
	"--accept-source-agreements",
	"--disable-interactivity",
	"--silent",
}

func wingetPackageCommand(action, source, packageID string, extraArgs ...string) []string {
	args := []string{action}
	if packageID != "" && isSafePackageID(packageID) {
		args = append(args, "--id", packageID, "--exact")
	} else {
		args = append(args, packageID)
	}
	args = append(args, "--source", source)
	args = append(args, extraArgs...)
	args = append(args, wingetInstallUpgradeFlags...)
	return managerCommand(managerWinget, args...)
}

func wingetPackageNameCommand(action, source, packageName string, extraArgs ...string) []string {
	args := []string{action, "--name", packageName, "--exact", "--source", source}
	args = append(args, extraArgs...)
	args = append(args, wingetInstallUpgradeFlags...)
	return managerCommand(managerWinget, args...)
}

func wingetInstallForceArgs(force bool) []string {
	if force {
		return []string{"--force"}
	}
	return nil
}

func wingetInstallCommand(packageManager, packageID string, force bool) []string {
	return wingetPackageCommand("install", wingetSourceArg(packageManager), packageID, wingetInstallForceArgs(force)...)
}

func wingetUpgradeCommand(packageManager, packageID string, extraArgs ...string) []string {
	return wingetPackageCommand("upgrade", wingetSourceArg(packageManager), packageID, extraArgs...)
}

func wingetMSStoreProductIDUpgradeCommand(productID string) []string {
	return wingetPackageCommand("upgrade", sourceMSStore, productID)
}

func wingetMSStoreProductIDUpgradeAvailableCommand(productID string) []string {
	return managerCommand(managerWinget, "list", "--upgrade-available", "--id", productID, "--exact", "--source", sourceMSStore, "--accept-source-agreements", "--disable-interactivity")
}

func wingetUpgradeNameCommand(packageManager, packageName string, extraArgs ...string) []string {
	return wingetPackageNameCommand("upgrade", wingetSourceArg(packageManager), packageName, extraArgs...)
}

func wingetInstallNameCommand(packageManager, packageName string, force bool) []string {
	return wingetPackageNameCommand("install", wingetSourceArg(packageManager), packageName, wingetInstallForceArgs(force)...)
}

func runWingetUpgradePackageWithInstallFallbackContext(ctx context.Context, packageManager string, pkg Package) CommandResult {
	targetResult := runPackageUpdateCandidates(ctx, wingetUpdateTargetCandidates(pkg), "winget target", func(target string) CommandResult {
		return runWingetUpgradeTargetWithInstallFallbackContext(ctx, packageManager, target, pkg.AllowUnknownVersionUpdate, pkg.AllowPinnedUpdate)
	})
	if targetResult.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(targetResult) {
		return targetResult
	}
	fallbackName := wingetNameFallbackTarget(pkg)
	if fallbackName == "" {
		return targetResult
	}
	appLog("Winget update targets for %s missed; trying exact package name %q.", updateJobPackageName(pkg), fallbackName)
	nameResult := runWingetUpgradeNameWithInstallFallbackContext(ctx, packageManager, fallbackName, pkg.AllowUnknownVersionUpdate, pkg.AllowPinnedUpdate)
	return mergeCommandAttemptsWithFinalResult(targetResult, nameResult, "winget name fallback")
}

func runWingetUpgradeTargetWithInstallFallbackContext(ctx context.Context, packageManager, packageID string, allowUnknownVersionRetry bool, allowPinnedRetry bool) CommandResult {
	return runWingetUpgradePlanWithFallbacks(ctx, wingetUpgradePlan{
		TargetDescription:        fmt.Sprintf("%s:%s", packageManager, packageID),
		AllowUnknownVersionRetry: allowUnknownVersionRetry,
		AllowPinnedRetry:         allowPinnedRetry,
		BuildUpgradeCommand: func(extraArgs ...string) []string {
			return wingetUpgradeCommand(packageManager, packageID, extraArgs...)
		},
		BuildForcedInstallCommand:     func() []string { return wingetInstallCommand(packageManager, packageID, true) },
		UnknownVersionRetryMergeLabel: "winget include-unknown retry",
		PinnedRetryMergeLabel:         "winget include-pinned retry",
	})
}

func runWingetUpgradeNameWithInstallFallbackContext(ctx context.Context, packageManager, packageName string, retryAllowances ...bool) CommandResult {
	allowUnknownVersionRetry := len(retryAllowances) > 0 && retryAllowances[0]
	allowPinnedRetry := len(retryAllowances) > 1 && retryAllowances[1]
	return runWingetUpgradePlanWithFallbacks(ctx, wingetUpgradePlan{
		TargetDescription:        fmt.Sprintf("%s name %q", packageManager, packageName),
		AllowUnknownVersionRetry: allowUnknownVersionRetry,
		AllowPinnedRetry:         allowPinnedRetry,
		BuildUpgradeCommand: func(extraArgs ...string) []string {
			return wingetUpgradeNameCommand(packageManager, packageName, extraArgs...)
		},
		BuildForcedInstallCommand:     func() []string { return wingetInstallNameCommand(packageManager, packageName, true) },
		UnknownVersionRetryMergeLabel: "winget include-unknown name retry",
		PinnedRetryMergeLabel:         "winget include-pinned name retry",
	})
}

type wingetUpgradePlan struct {
	TargetDescription             string
	AllowUnknownVersionRetry      bool
	AllowPinnedRetry              bool
	BuildUpgradeCommand           func(extraArgs ...string) []string
	BuildForcedInstallCommand     func() []string
	UnknownVersionRetryMergeLabel string
	PinnedRetryMergeLabel         string
}

type wingetUpgradeRetryPlan struct {
	CommandFlag                string
	RetryLogMessage            string
	MergeLabel                 string
	AllowedByPolicy            bool
	ShouldRetry                func(CommandResult) bool
	BuildConsentRequiredResult func(CommandResult) CommandResult
}

func runWingetUpgradePlanWithFallbacks(ctx context.Context, plan wingetUpgradePlan) CommandResult {
	result := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, plan.BuildUpgradeCommand()...)

	for _, retryPlan := range []wingetUpgradeRetryPlan{
		{
			CommandFlag:                "--include-unknown",
			RetryLogMessage:            "Winget upgrade for %s reported an unknown installed version; retrying with --include-unknown.",
			MergeLabel:                 plan.UnknownVersionRetryMergeLabel,
			AllowedByPolicy:            plan.AllowUnknownVersionRetry,
			ShouldRetry:                shouldRetryWingetIncludeUnknown,
			BuildConsentRequiredResult: requireExplicitUnknownVersionUpdate,
		},
		{
			CommandFlag:                "--include-pinned",
			RetryLogMessage:            "Winget upgrade for %s reported a pinned package; retrying with --include-pinned.",
			MergeLabel:                 plan.PinnedRetryMergeLabel,
			AllowedByPolicy:            plan.AllowPinnedRetry,
			ShouldRetry:                shouldRetryWingetIncludePinned,
			BuildConsentRequiredResult: requireExplicitPinnedUpdate,
		},
	} {
		var stopAfterRetry bool
		result, stopAfterRetry = runWingetUpgradeRetryIfNeeded(ctx, plan, result, retryPlan)
		if stopAfterRetry {
			return result
		}
	}

	if shouldRetryWingetForceUpgrade(result) {
		if ctx.Err() != nil {
			return result
		}
		appLog("Winget upgrade for %s reported no applicable upgrade; retrying with --force.", plan.TargetDescription)
		forceUpgradeResult := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, plan.BuildUpgradeCommand("--force")...)
		result = mergeCommandAttemptsWithFinalResult(result, forceUpgradeResult, "winget force upgrade retry")
		if forceUpgradeResult.OK || ctx.Err() != nil {
			return result
		}
		if shouldRetryWingetForceUpgrade(forceUpgradeResult) && plan.BuildForcedInstallCommand != nil {
			appLog("Winget forced upgrade for %s still reported no applicable upgrade; retrying with exact forced install.", plan.TargetDescription)
			forcedInstallResult := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, plan.BuildForcedInstallCommand()...)
			result = mergeCommandAttemptsWithFinalResult(result, forcedInstallResult, "winget forced install fallback")
			if forcedInstallResult.OK || ctx.Err() != nil {
				return result
			}
		}
		return requireExplicitWingetRepair(result)
	}
	return result
}

func runWingetUpgradeRetryIfNeeded(ctx context.Context, plan wingetUpgradePlan, currentResult CommandResult, retryPlan wingetUpgradeRetryPlan) (CommandResult, bool) {
	if !retryPlan.ShouldRetry(currentResult) {
		return currentResult, false
	}
	if ctx.Err() != nil {
		return currentResult, true
	}
	if !retryPlan.AllowedByPolicy {
		return retryPlan.BuildConsentRequiredResult(currentResult), true
	}
	appLog(retryPlan.RetryLogMessage, plan.TargetDescription)
	retryResult := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, plan.BuildUpgradeCommand(retryPlan.CommandFlag)...)
	currentResult = mergeCommandAttemptsWithFinalResult(currentResult, retryResult, retryPlan.MergeLabel)
	return currentResult, retryResult.OK || ctx.Err() != nil
}
