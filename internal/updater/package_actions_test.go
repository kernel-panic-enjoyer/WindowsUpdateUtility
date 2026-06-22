package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStoreUpdateTargetCandidatesIncludeStableAndMetadataTargets(t *testing.T) {
	pkg := Package{
		Manager: managerStore,
		ID:      "Vendor.App_1.2.3.4_x64__abc123",
		Name:    "Vendor App",
		Match:   "Vendor.App_abc123",
	}

	got := storeUpdateTargetCandidates(pkg)
	want := []string{
		"Vendor.App_1.2.3.4_x64__abc123",
		"Vendor.App",
		"Vendor.App_abc123",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected Store targets:\n got %#v\nwant %#v", got, want)
	}
}

func TestStoreUpdateTriesAlternateMetadataTarget(t *testing.T) {
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "Vendor.App_abc123" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "already up to date"}
			}
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Could not find installed product metadata"}
		},
		func(manager string) bool { return manager == managerStore },
	)
	defer restore()

	pkg := Package{Manager: managerStore, ID: "9BADTARGET", Name: "Vendor App", Match: "Vendor.App_abc123"}
	result := runStoreUpdatePackageWithFallbackContext(context.Background(), pkg)

	if !result.OK {
		t.Fatalf("expected alternate Store target to succeed, got %#v", result)
	}
	if strings.Join(targets, "|") != "9BADTARGET|Vendor.App_abc123" {
		t.Fatalf("unexpected Store target sequence: %#v", targets)
	}
}

func TestStoreUpdateDoesNotSearchFreshTargetAfterDirectTargetsMiss(t *testing.T) {
	var targets []string
	restoreActions := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "Fresh.Store.ID" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "already up to date"}
			}
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Could not find installed product metadata"}
		},
		func(manager string) bool { return manager == managerStore },
	)
	defer restoreActions()
	var queries []string
	restoreSearch := replaceStoreSearchHook(func(query string) ([]Package, CommandResult) {
		queries = append(queries, query)
		return []Package{{Name: "Vendor App", ID: "Fresh.Store.ID", Manager: managerStore}}, CommandResult{OK: true, Command: "store search " + query}
	})
	defer restoreSearch()

	pkg := Package{Manager: managerStore, ID: "Stale.Store.ID", Name: "Vendor App"}
	result := runStoreUpdatePackageWithFallbackContext(context.Background(), pkg)

	if result.OK {
		t.Fatalf("Store target miss should not be rescued by display-name search, got %#v", result)
	}
	if len(queries) != 0 {
		t.Fatalf("Store update must not run hidden display-name searches, got %#v", queries)
	}
	if !containsString(targets, "Stale.Store.ID") || containsString(targets, "Fresh.Store.ID") || strings.Contains(result.Command, "store search fallback") {
		t.Fatalf("unexpected Store target sequence after target miss, targets=%#v result=%#v", targets, result)
	}
}

func TestAssessedStoreUpdateUsesOnlyExactVerifiedTarget(t *testing.T) {
	pkg := Package{
		Manager:                    managerStore,
		ID:                         "9NVERIFIED",
		Name:                       "Display Name Must Not Be Used",
		Match:                      "Package.Family_abc123",
		UpdateState:                string(StoreUpdateAvailable),
		StoreProductID:             "9NVERIFIED",
		StoreUpdateID:              "Package.Family_abc123",
		ExactActionTargetAvailable: true,
	}
	got := storeUpdateTargetCandidates(pkg)
	if strings.Join(got, "|") != "9NVERIFIED|Package.Family_abc123" {
		t.Fatalf("assessed Store package must use only exact verified target, got %#v", got)
	}
}

func TestAssessedStoreUpdateDoesNotFallBackToWingetMSStore(t *testing.T) {
	var commands []string
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			commands = append(commands, strings.Join(args, " "))
			targets = append(targets, packageActionTargetFromArgs(args))
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Could not find installed product metadata"}
		},
		func(manager string) bool { return manager == managerStore || manager == managerWinget },
	)
	defer restore()

	pkg := Package{
		Manager:                    managerStore,
		ID:                         "9NVERIFIED",
		Name:                       "Display Name Must Not Be Used",
		UpdateState:                string(StoreUpdateAvailable),
		StoreProductID:             "9NVERIFIED",
		StoreUpdateID:              "Package.Family_abc123",
		ExactActionTargetAvailable: true,
	}
	result := runStoreUpdatePackageWithFallbackContext(context.Background(), pkg)
	if result.OK {
		t.Fatalf("expected exact Store update target failure, got %#v", result)
	}
	for _, command := range commands {
		if strings.Contains(command, "winget") {
			t.Fatalf("assessed Store update must not fall back to winget msstore, commands=%#v result=%#v", commands, result)
		}
	}
	if strings.Join(targets, "|") != "9NVERIFIED|Package.Family_abc123" {
		t.Fatalf("expected only exact Store targets, targets=%#v commands=%#v result=%#v", targets, commands, result)
	}
}

func TestStoreUpdateRetriesWithoutApplyWhenApplyFlagUnsupported(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--apply") {
				return CommandResult{Code: 1, Command: command, Stderr: "Unrecognized command or argument '--apply'."}
			}
			return CommandResult{OK: true, Command: command, Stdout: "Update completed"}
		},
		func(manager string) bool { return manager == managerStore },
	)
	defer restore()

	pkg := Package{Manager: managerStore, ID: "OpenAI.Codex", Name: "Codex", UpdateAvailable: true, UpdateSupported: true}
	result := runStoreUpdatePackageWithFallbackContext(context.Background(), pkg)

	if !result.OK {
		t.Fatalf("expected Store update retry without apply to succeed, got %#v", result)
	}
	if len(commands) != 2 || !strings.Contains(commands[0], "--apply true") || strings.Contains(commands[1], "--apply") || !strings.Contains(result.Command, "store update without apply flag") {
		t.Fatalf("expected Store update with apply then retry without apply; commands=%#v result=%#v", commands, result)
	}
}

func TestStoreUpdateDoesNotDropApplyForPackageTargetMiss(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			return CommandResult{Code: 1, Command: command, Stderr: "Could not find installed product metadata"}
		},
		func(manager string) bool { return manager == managerStore },
	)
	defer restore()
	restoreSearch := replaceStoreSearchHook(func(query string) ([]Package, CommandResult) {
		return nil, CommandResult{Code: 1, Command: "store search " + query, Stderr: "no match"}
	})
	defer restoreSearch()

	pkg := Package{Manager: managerStore, ID: "Missing.Store.ID", Name: "Missing Store App", UpdateAvailable: true, UpdateSupported: true}
	result := runStoreUpdatePackageWithFallbackContext(context.Background(), pkg)

	if result.OK {
		t.Fatalf("target miss should not become success, got %#v", result)
	}
	if len(commands) == 0 {
		t.Fatalf("expected at least one Store update command, got result=%#v", result)
	}
	for _, command := range commands {
		if !strings.Contains(command, "--apply true") {
			t.Fatalf("Store target miss should not retry just to drop apply flag; commands=%#v result=%#v", commands, result)
		}
	}
}

func TestWingetUpdateTriesAlternatePackageIDTarget(t *testing.T) {
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "Good.Package" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stdout: "No installed package found matching input criteria."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Bad.Package", Name: "Example App", Match: "Good.Package"}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if !result.OK {
		t.Fatalf("expected alternate winget target to succeed, got %#v", result)
	}
	if !containsString(targets, "Bad.Package") || !containsString(targets, "Good.Package") {
		t.Fatalf("expected bad and good targets to be attempted, got %#v", targets)
	}
}

func TestWingetUpdateTriesExactNameFallbackAfterIDMisses(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--name") && containsString(args, "Example App") {
				return CommandResult{OK: true, Command: command, Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: command, Stdout: "No installed package found matching input criteria."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Bad.Package", Name: "Example App", Match: "Also.Bad"}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if !result.OK {
		t.Fatalf("expected winget exact-name fallback to succeed, got %#v", result)
	}
	if len(commands) != 3 || !strings.Contains(commands[2], "--name Example App --exact") || !strings.Contains(result.Command, "winget name fallback") {
		t.Fatalf("expected ID, match, then exact-name fallback; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetUpdateRequiresExplicitChoiceForUnknownVersion(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			return CommandResult{Code: 1, Command: command, Stdout: "No applicable upgrade found for packages with unknown installed version. Use --include-unknown to include packages with unknown versions."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Vendor.Unknown", Name: "Vendor Unknown"}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if result.OK {
		t.Fatalf("unknown-version update should require explicit confirmation, got %#v", result)
	}
	if len(commands) != 1 || strings.Contains(commands[0], "--include-unknown") || !strings.Contains(result.Stderr, "requires explicit user confirmation") {
		t.Fatalf("expected no include-unknown retry without confirmation; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetUpdateRetriesIncludeUnknownWhenExplicitlyAllowed(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--include-unknown") {
				return CommandResult{OK: true, Command: command, Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: command, Stdout: "No applicable upgrade found for packages with unknown installed version. Use --include-unknown to include packages with unknown versions."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Vendor.Unknown", Name: "Vendor Unknown", AllowUnknownVersionUpdate: true}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if !result.OK {
		t.Fatalf("expected explicit include-unknown retry to succeed, got %#v", result)
	}
	if len(commands) != 2 || !strings.Contains(commands[1], "--include-unknown") || !strings.Contains(result.Command, "winget include-unknown retry") {
		t.Fatalf("expected normal upgrade then include-unknown retry; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetNameUpdateRetriesIncludeUnknownWhenExplicitlyAllowed(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--include-unknown") {
				return CommandResult{OK: true, Command: command, Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: command, Stderr: "No installed package found matching input criteria with a known version. Use --include-unknown to include unknown versions."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	result := runWingetUpgradeNameWithInstallFallbackContext(context.Background(), managerWinget, "Unknown Desktop App", true)

	if !result.OK {
		t.Fatalf("expected explicit include-unknown name retry to succeed, got %#v", result)
	}
	if len(commands) != 2 || !strings.Contains(commands[0], "--name Unknown Desktop App --exact") || !strings.Contains(commands[1], "--include-unknown") || !strings.Contains(result.Command, "winget include-unknown name retry") {
		t.Fatalf("expected exact-name upgrade then include-unknown retry; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetUpdateRequiresExplicitChoiceForPinnedPackage(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			return CommandResult{Code: 1, Command: command, Stderr: "Package is pinned. Use --include-pinned to include pinned packages."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Vendor.Pinned", Name: "Pinned App"}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if result.OK {
		t.Fatalf("pinned update should require explicit confirmation, got %#v", result)
	}
	if len(commands) != 1 || strings.Contains(commands[0], "--include-pinned") || !strings.Contains(result.Stderr, "requires explicit user confirmation") {
		t.Fatalf("expected no include-pinned retry without confirmation; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetUpdateRetriesIncludePinnedWhenExplicitlyAllowed(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--include-pinned") {
				return CommandResult{OK: true, Command: command, Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: command, Stderr: "Package is pinned. Use --include-pinned to include pinned packages."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	pkg := Package{Manager: managerWinget, ID: "Vendor.Pinned", Name: "Pinned App", AllowPinnedUpdate: true}
	result := runWingetUpgradePackageWithInstallFallbackContext(context.Background(), managerWinget, pkg)

	if !result.OK {
		t.Fatalf("expected explicit include-pinned retry to succeed, got %#v", result)
	}
	if len(commands) != 2 || !strings.Contains(commands[1], "--include-pinned") || !strings.Contains(result.Command, "winget include-pinned retry") {
		t.Fatalf("expected normal upgrade then include-pinned retry; commands=%#v result=%#v", commands, result)
	}
}

func TestWingetNameUpdateRetriesIncludePinnedWhenExplicitlyAllowed(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if containsString(args, "--include-pinned") {
				return CommandResult{OK: true, Command: command, Stdout: "Successfully upgraded"}
			}
			return CommandResult{Code: 1, Command: command, Stderr: "Package is pinned. Use --include-pinned to include pinned packages."}
		},
		func(manager string) bool { return manager == managerWinget },
	)
	defer restore()

	result := runWingetUpgradeNameWithInstallFallbackContext(context.Background(), managerWinget, "Pinned Desktop App", false, true)

	if !result.OK {
		t.Fatalf("expected explicit include-pinned name retry to succeed, got %#v", result)
	}
	if len(commands) != 2 || !strings.Contains(commands[0], "--name Pinned Desktop App --exact") || !strings.Contains(commands[1], "--include-pinned") || !strings.Contains(result.Command, "winget include-pinned name retry") {
		t.Fatalf("expected exact-name upgrade then include-pinned retry; commands=%#v result=%#v", commands, result)
	}
}

func TestPackageActionCommandRetriesTransientFailure(t *testing.T) {
	calls := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			calls++
			if calls == 1 {
				return CommandResult{Code: 2316632065, Command: strings.Join(args, " "), Stderr: "Another transaction is currently running"}
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "ok"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "Example.App")

	if !result.OK || calls != 2 {
		t.Fatalf("expected transient command to retry once and succeed, calls=%d result=%#v", calls, result)
	}
}

func TestPackageActionCommandRetriesRepeatedTransientFailures(t *testing.T) {
	calls := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			calls++
			if calls < packageActionMaxAttempts {
				return CommandResult{Code: 1618, Command: strings.Join(args, " "), Stderr: "Another installation is already in progress."}
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "ok"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerChoco, time.Second, "choco", "upgrade", "example")

	if !result.OK || calls != packageActionMaxAttempts || !strings.Contains(result.Command, "choco retry 2") {
		t.Fatalf("expected repeated transient retries to succeed on final attempt, calls=%d result=%#v", calls, result)
	}
}

func TestPackageActionCommandStopsAfterTransientRetryLimit(t *testing.T) {
	calls := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			calls++
			return CommandResult{Code: 1618, Command: strings.Join(args, " "), Stderr: "Another installation is already in progress."}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerChoco, time.Second, "choco", "upgrade", "example")

	if result.OK || calls != packageActionMaxAttempts {
		t.Fatalf("expected transient retries to stop after the limit, calls=%d result=%#v", calls, result)
	}
}

func TestPackageActionCommandRetriesNetworkTransientFailure(t *testing.T) {
	calls := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			calls++
			if calls == 1 {
				return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "The underlying connection was closed: A connection that was expected to be kept alive was closed by the server."}
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "download completed"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerChoco, time.Second, "choco", "upgrade", "example")

	if !result.OK || calls != 2 {
		t.Fatalf("expected network transient command to retry once and succeed, calls=%d result=%#v", calls, result)
	}
}

func TestPackageActionCommandDoesNotRetryPackageNotFoundAsNetworkTransient(t *testing.T) {
	calls := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			calls++
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stderr: "Unable to find package 'missing'."}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerChoco, time.Second, "choco", "upgrade", "missing")

	if result.OK || calls != 1 {
		t.Fatalf("package-not-found should not be retried by network transient logic, calls=%d result=%#v", calls, result)
	}
}

func TestPackageActionCommandTreatsRebootRequiredAsSuccess(t *testing.T) {
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{Code: 3010, Command: strings.Join(args, " "), Stdout: "installer completed"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerChoco, time.Second, "choco", "upgrade", "example")

	if !result.OK || result.Code != 3010 || !strings.Contains(result.Stdout, "restart required") {
		t.Fatalf("expected reboot-required code to be normalized as success, got %#v", result)
	}
}

func TestPackageActionCommandTreatsAlreadyCurrentOutputAsSuccess(t *testing.T) {
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stdout: "Package is already up to date"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerStore, time.Second, "store", "update", "example")

	if !result.OK || result.Code != 1 {
		t.Fatalf("expected already-current output to be normalized as success, got %#v", result)
	}
}

func TestPackageActionCommandDoesNotTreatWingetNoApplicableUpgradeAsSuccess(t *testing.T) {
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{Code: 1, Command: strings.Join(args, " "), Stdout: "No applicable upgrade found."}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "example")

	if result.OK {
		t.Fatalf("winget no-applicable-upgrade should remain available for forced-install fallback, got %#v", result)
	}
}

func TestPackageActionCommandRepairsWingetSourceBeforeRetry(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if strings.Contains(command, "source update") {
				return CommandResult{OK: true, Command: command, Stdout: "source updated"}
			}
			if len(commands) == 1 {
				return CommandResult{Code: 1, Command: command, Stderr: "Failed when searching source; source data is corrupted"}
			}
			return CommandResult{OK: true, Command: command, Stdout: "upgrade succeeded"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "Example.App")

	if !result.OK {
		t.Fatalf("expected winget source repair retry to succeed, got %#v", result)
	}
	if len(commands) != 3 || !strings.Contains(commands[1], "source update") || !strings.Contains(result.Command, "winget retry after source update") {
		t.Fatalf("expected original, source update, retry command sequence; commands=%#v result=%#v", commands, result)
	}
}

func TestPackageActionCommandRetriesTransientAfterWingetSourceRepair(t *testing.T) {
	var commands []string
	upgradeAttempts := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if strings.Contains(command, "source update") {
				return CommandResult{OK: true, Command: command, Stdout: "source updated"}
			}
			upgradeAttempts++
			switch upgradeAttempts {
			case 1:
				return CommandResult{Code: 1, Command: command, Stderr: "Failed when searching source; source data is corrupted"}
			case 2:
				return CommandResult{Code: 1618, Command: command, Stderr: "Another installation is already in progress."}
			default:
				return CommandResult{OK: true, Command: command, Stdout: "upgrade succeeded"}
			}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "Example.App")

	if !result.OK {
		t.Fatalf("expected transient retry after winget source repair to succeed, got %#v", result)
	}
	if len(commands) != 4 || !strings.Contains(commands[1], "source update") || !strings.Contains(result.Command, "winget retry after source update") || !strings.Contains(result.Command, "winget retry 1") {
		t.Fatalf("expected original, source update, transient retry, success sequence; commands=%#v result=%#v", commands, result)
	}
}

func TestPackageActionCommandResetsWingetSourceWhenSourceUpdateFails(t *testing.T) {
	var commands []string
	upgradeAttempts := 0
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			switch {
			case strings.Contains(command, "source update"):
				return CommandResult{Code: 1, Command: command, Stderr: "Source data is corrupted"}
			case strings.Contains(command, "source reset"):
				return CommandResult{OK: true, Command: command, Stdout: "sources reset"}
			default:
				upgradeAttempts++
				if upgradeAttempts == 1 {
					return CommandResult{Code: 1, Command: command, Stderr: "Failed when searching source; source data is corrupted"}
				}
				return CommandResult{OK: true, Command: command, Stdout: "upgrade succeeded"}
			}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "Example.App")

	if !result.OK {
		t.Fatalf("expected winget source reset repair retry to succeed, got %#v", result)
	}
	if len(commands) != 4 || !strings.Contains(commands[1], "source update") || !strings.Contains(commands[2], "source reset") || !strings.Contains(result.Command, "winget retry after source reset") {
		t.Fatalf("expected original, source update, source reset, retry sequence; commands=%#v result=%#v", commands, result)
	}
}

func TestPackageActionCommandDoesNotResetWingetSourceForNonSourceRepairFailure(t *testing.T) {
	var commands []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			commands = append(commands, command)
			if strings.Contains(command, "source update") {
				return CommandResult{Code: 5, Command: command, Stderr: "Access is denied."}
			}
			return CommandResult{Code: 1, Command: command, Stderr: "Failed when searching source; source data is corrupted"}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	result := runPackageActionCommand(context.Background(), managerWinget, time.Second, "winget", "upgrade", "--id", "Example.App")

	if result.OK {
		t.Fatalf("non-source repair failure should not be masked, got %#v", result)
	}
	if len(commands) != 2 || strings.Contains(strings.Join(commands, "\n"), "source reset") {
		t.Fatalf("source reset should not run after non-source repair failure; commands=%#v result=%#v", commands, result)
	}
}

func TestPackageForUpdateUsesExactStoreInventoryMetadata(t *testing.T) {
	app := &App{inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, "Vendor.App_abc123"),
		Manager:         managerStore,
		ID:              "Vendor.App_abc123",
		Name:            "Vendor App",
		Match:           "Vendor.App",
		UpdateSupported: true,
	}}}}}

	got := app.packageForUpdate(managerStore, "Vendor.App_abc123")

	if got.Name != "Vendor App" || got.Match != "Vendor.App" || got.ID != "Vendor.App_abc123" {
		t.Fatalf("expected inventory metadata for exact Store key, got %#v", got)
	}
	miss := app.packageForUpdate(managerStore, "Vendor.App_1.2.3.4_x64__abc123")
	if miss.Name == "Vendor App" {
		t.Fatalf("versioned Store full name must not match PFN metadata: %#v", miss)
	}
}

func TestPackageCommandBuilders(t *testing.T) {
	wingetInstallArgs := wingetInstallCommand("winget", "Git.Git", false)
	manager, verb := packageManagerCommandVerb(wingetInstallArgs)
	if manager != managerWinget || verb != "install" {
		t.Fatalf("winget install command should start with winget install: %#v", wingetInstallArgs)
	}
	wingetInstall := strings.Join(wingetInstallArgs, " ")
	for _, expected := range []string{"--id Git.Git --exact", "--source winget", "--accept-package-agreements", "--disable-interactivity", "--silent"} {
		if !strings.Contains(wingetInstall, expected) {
			t.Fatalf("winget install command missing %q: %s", expected, wingetInstall)
		}
	}
	if strings.Contains(wingetInstall, "--force") {
		t.Fatalf("normal install should not include force: %s", wingetInstall)
	}

	forcedStoreInstallArgs := wingetInstallCommand("store", "Microsoft To Do", true)
	manager, verb = packageManagerCommandVerb(forcedStoreInstallArgs)
	if manager != managerWinget || verb != "install" {
		t.Fatalf("forced store install command should use winget install fallback: %#v", forcedStoreInstallArgs)
	}
	forcedStoreInstall := strings.Join(forcedStoreInstallArgs, " ")
	for _, expected := range []string{"Microsoft To Do", "--source msstore", "--force"} {
		if !strings.Contains(forcedStoreInstall, expected) {
			t.Fatalf("forced store install command missing %q: %s", expected, forcedStoreInstall)
		}
	}

	wingetNameUpgrade := strings.Join(wingetUpgradeCommand("winget", "Long Desktop App"), " ")
	if !strings.Contains(wingetNameUpgrade, "upgrade Long Desktop App") || strings.Contains(wingetNameUpgrade, "--id Long Desktop App") {
		t.Fatalf("winget name target should use positional target, got %s", wingetNameUpgrade)
	}
	wingetExactNameUpgrade := strings.Join(wingetUpgradeNameCommand("winget", "Long Desktop App"), " ")
	for _, expected := range []string{"upgrade --name Long Desktop App --exact", "--source winget"} {
		if !strings.Contains(wingetExactNameUpgrade, expected) {
			t.Fatalf("winget exact-name command missing %q: %s", expected, wingetExactNameUpgrade)
		}
	}
	wingetStoreCatalogQuery := strings.Join(wingetMSStoreProductIDUpgradeAvailableCommand("9N4D0MSMP0PT"), " ")
	if !strings.Contains(wingetStoreCatalogQuery, "list --upgrade-available --id 9N4D0MSMP0PT --exact --source msstore") {
		t.Fatalf("expected read-only exact Product ID list command, got %s", wingetStoreCatalogQuery)
	}
	for _, forbidden := range []string{" upgrade ", " install ", "--silent", "--accept-package-agreements"} {
		if strings.Contains(wingetStoreCatalogQuery, forbidden) {
			t.Fatalf("Store catalog query command must not contain %q: %s", forbidden, wingetStoreCatalogQuery)
		}
	}

	chocoUpgrade := strings.Join(chocoPackageCommand("upgrade", "git"), " ")
	for _, expected := range []string{"upgrade git", "-y", "--no-progress", "--no-color"} {
		if !strings.Contains(chocoUpgrade, expected) {
			t.Fatalf("choco command missing %q: %s", expected, chocoUpgrade)
		}
	}

	storeUpdate := strings.Join(storeUpdateCommand("Codex", true), " ")
	for _, expected := range []string{"store", "update Codex", "--apply true"} {
		if !strings.Contains(storeUpdate, expected) {
			t.Fatalf("store update command missing %q: %s", expected, storeUpdate)
		}
	}
	storeUpdateNoApply := strings.Join(storeUpdateCommand("Codex", false), " ")
	for _, expected := range []string{"store", "update Codex", "--apply false"} {
		if !strings.Contains(storeUpdateNoApply, expected) {
			t.Fatalf("store update command without apply missing %q: %s", expected, storeUpdateNoApply)
		}
	}
}

func TestWingetNoApplicableUpgradeUsesFallbackDetection(t *testing.T) {
	english := CommandResult{Code: 1, Stdout: "No applicable upgrade found."}
	if !shouldForceInstallAfterWingetUpgrade(english) {
		t.Fatal("expected English no-applicable-upgrade output to trigger fallback")
	}
	if shouldRetryWingetIncludeUnknown(english) {
		t.Fatal("plain no-applicable-upgrade output should not trigger include-unknown retry")
	}

	german := CommandResult{Code: 1, Stdout: "Es wurde kein anwendbares Upgrade gefunden."}
	if !shouldForceInstallAfterWingetUpgrade(german) {
		t.Fatal("expected German no-applicable-upgrade output to trigger fallback")
	}

	success := CommandResult{OK: true, Stdout: "No applicable upgrade found."}
	if shouldForceInstallAfterWingetUpgrade(success) {
		t.Fatal("successful winget command should not trigger fallback")
	}

	unknownVersion := CommandResult{Code: 1, Stdout: "No applicable upgrade found for packages with unknown installed version. Use --include-unknown to include packages with unknown versions."}
	if !shouldRetryWingetIncludeUnknown(unknownVersion) {
		t.Fatal("unknown installed version output should trigger include-unknown retry")
	}

	pinnedPackage := CommandResult{Code: 1, Stderr: "Package is pinned. Use --include-pinned to include pinned packages."}
	if !shouldRetryWingetIncludePinned(pinnedPackage) {
		t.Fatal("pinned package output should trigger include-pinned retry path")
	}
}

func TestMergeCommandResultsKeepsPrimaryFailureContext(t *testing.T) {
	primary := CommandResult{Code: 1, Command: "winget upgrade", Stdout: "No applicable upgrade found.", Stderr: "primary stderr"}
	fallback := CommandResult{OK: true, Code: 0, Command: "winget install --force", Stdout: "Successfully installed", Stderr: ""}

	merged := mergeCommandResults(primary, fallback, "fallback")

	if !merged.OK || merged.Code != 0 {
		t.Fatalf("expected fallback success to win, got %#v", merged)
	}
	if !strings.Contains(merged.Command, "winget upgrade") || !strings.Contains(merged.Command, "winget install --force") {
		t.Fatalf("merged command did not include both commands: %q", merged.Command)
	}
	if !strings.Contains(merged.Stdout, "No applicable upgrade found.") || !strings.Contains(merged.Stdout, "Successfully installed") {
		t.Fatalf("merged stdout lost context: %q", merged.Stdout)
	}
	if !strings.Contains(merged.Stderr, "primary stderr") {
		t.Fatalf("merged stderr lost primary context: %q", merged.Stderr)
	}
}
