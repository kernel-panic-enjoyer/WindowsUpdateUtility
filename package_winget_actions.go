package main

import (
	"context"
	"fmt"
)

func wingetSourceArg(manager string) string {
	if manager == managerStore {
		return sourceMSStore
	}
	return sourceWinget
}

var wingetInteractiveFlags = []string{
	"--accept-package-agreements",
	"--accept-source-agreements",
	"--disable-interactivity",
	"--silent",
}

func wingetPackageCommand(action, source, id string, extra ...string) []string {
	args := []string{action}
	if id != "" && isSafePackageID(id) {
		args = append(args, "--id", id, "--exact")
	} else {
		args = append(args, id)
	}
	args = append(args, "--source", source)
	args = append(args, extra...)
	args = append(args, wingetInteractiveFlags...)
	return managerCommand(managerWinget, args...)
}

func wingetPackageNameCommand(action, source, name string, extra ...string) []string {
	args := []string{action, "--name", name, "--exact", "--source", source}
	args = append(args, extra...)
	args = append(args, wingetInteractiveFlags...)
	return managerCommand(managerWinget, args...)
}

func wingetInstallCommand(manager, id string, force bool) []string {
	var extra []string
	if force {
		extra = append(extra, "--force")
	}
	return wingetPackageCommand("install", wingetSourceArg(manager), id, extra...)
}

func wingetUpgradeCommand(manager, id string, extra ...string) []string {
	return wingetPackageCommand("upgrade", wingetSourceArg(manager), id, extra...)
}

func wingetUpgradeNameCommand(manager, name string, extra ...string) []string {
	return wingetPackageNameCommand("upgrade", wingetSourceArg(manager), name, extra...)
}

func wingetInstallNameCommand(manager, name string, force bool) []string {
	var extra []string
	if force {
		extra = append(extra, "--force")
	}
	return wingetPackageNameCommand("install", wingetSourceArg(manager), name, extra...)
}

func runWingetUpgradePackageWithInstallFallbackContext(ctx context.Context, manager string, pkg Package) CommandResult {
	result := runPackageUpdateCandidates(ctx, wingetUpdateTargetCandidates(pkg), "winget target", func(target string) CommandResult {
		return runWingetUpgradeTargetWithInstallFallbackContext(ctx, manager, target, pkg.AllowUnknownVersionUpdate, pkg.AllowPinnedUpdate)
	})
	if result.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(result) {
		return result
	}
	name := wingetNameFallbackTarget(pkg)
	if name == "" {
		return result
	}
	appLog("Winget update targets for %s missed; trying exact package name %q.", updateJobPackageName(pkg), name)
	nameResult := runWingetUpgradeNameWithInstallFallbackContext(ctx, manager, name, pkg.AllowUnknownVersionUpdate, pkg.AllowPinnedUpdate)
	return mergeCommandResults(result, nameResult, "winget name fallback")
}

func runWingetUpgradeTargetWithInstallFallbackContext(ctx context.Context, manager, id string, allowUnknownVersion bool, allowPinned bool) CommandResult {
	return runWingetUpgradeAttemptWithFallbacks(ctx, wingetUpgradeAttempt{
		Description:              fmt.Sprintf("%s:%s", manager, id),
		AllowUnknownVersion:      allowUnknownVersion,
		AllowPinned:              allowPinned,
		UpgradeCommand:           func(extra ...string) []string { return wingetUpgradeCommand(manager, id, extra...) },
		ForcedInstallCommand:     func() []string { return wingetInstallCommand(manager, id, true) },
		UnknownVersionRetryLabel: "winget include-unknown retry",
		PinnedRetryLabel:         "winget include-pinned retry",
		ForcedInstallLabel:       "winget forced install fallback",
	})
}

func runWingetUpgradeNameWithInstallFallbackContext(ctx context.Context, manager, name string, options ...bool) CommandResult {
	allowUnknownVersion := len(options) > 0 && options[0]
	allowPinned := len(options) > 1 && options[1]
	return runWingetUpgradeAttemptWithFallbacks(ctx, wingetUpgradeAttempt{
		Description:              fmt.Sprintf("%s name %q", manager, name),
		AllowUnknownVersion:      allowUnknownVersion,
		AllowPinned:              allowPinned,
		UpgradeCommand:           func(extra ...string) []string { return wingetUpgradeNameCommand(manager, name, extra...) },
		ForcedInstallCommand:     func() []string { return wingetInstallNameCommand(manager, name, true) },
		UnknownVersionRetryLabel: "winget include-unknown name retry",
		PinnedRetryLabel:         "winget include-pinned name retry",
		ForcedInstallLabel:       "winget forced install name fallback",
	})
}

type wingetUpgradeAttempt struct {
	Description              string
	AllowUnknownVersion      bool
	AllowPinned              bool
	UpgradeCommand           func(extra ...string) []string
	ForcedInstallCommand     func() []string
	UnknownVersionRetryLabel string
	PinnedRetryLabel         string
	ForcedInstallLabel       string
}

func runWingetUpgradeAttemptWithFallbacks(ctx context.Context, attempt wingetUpgradeAttempt) CommandResult {
	result := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, attempt.UpgradeCommand()...)
	if shouldRetryWingetIncludeUnknown(result) {
		if ctx.Err() != nil {
			return result
		}
		if !attempt.AllowUnknownVersion {
			return requireExplicitUnknownVersionUpdate(result)
		}
		appLog("Winget upgrade for %s reported an unknown installed version; retrying with --include-unknown.", attempt.Description)
		retry := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, attempt.UpgradeCommand("--include-unknown")...)
		result = mergeCommandResults(result, retry, attempt.UnknownVersionRetryLabel)
		if retry.OK || ctx.Err() != nil {
			return result
		}
	}
	if shouldRetryWingetIncludePinned(result) {
		if ctx.Err() != nil {
			return result
		}
		if !attempt.AllowPinned {
			return requireExplicitPinnedUpdate(result)
		}
		appLog("Winget upgrade for %s reported a pinned package; retrying with --include-pinned.", attempt.Description)
		retry := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, attempt.UpgradeCommand("--include-pinned")...)
		result = mergeCommandResults(result, retry, attempt.PinnedRetryLabel)
		if retry.OK || ctx.Err() != nil {
			return result
		}
	}
	if shouldForceInstallAfterWingetUpgrade(result) {
		if ctx.Err() != nil {
			return result
		}
		appLog("Winget upgrade for %s reported no applicable upgrade; trying forced install fallback.", attempt.Description)
		fallback := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, attempt.ForcedInstallCommand()...)
		return mergeCommandResults(result, fallback, attempt.ForcedInstallLabel)
	}
	return result
}
