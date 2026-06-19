package updater

import (
	"context"
	"testing"
	"time"
)

func replaceUpdateJobHooks(runner func(context.Context, string, string) CommandResult) func() {
	return replaceUpdateJobHooksWithRefresh(runner, func(app *App) {})
}

func replaceUpdateJobHooksWithRefresh(runner func(context.Context, string, string) CommandResult, refresh func(*App)) func() {
	oldRunner := updatePackageRunner
	oldRefresh := refreshInventoryAfterUpdateJob
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		return runner(ctx, pkg.Manager, pkg.ID)
	}
	refreshInventoryAfterUpdateJob = refresh
	return func() {
		updatePackageRunner = oldRunner
		refreshInventoryAfterUpdateJob = oldRefresh
	}
}

func replacePackageActionHooks(
	runner func(context.Context, time.Duration, ...string) CommandResult,
	available func(string) bool,
) func() {
	oldRunner := packageActionCommandRunner
	oldAvailable := packageActionManagerAvailable
	oldWait := packageActionRetryWait
	packageActionCommandRunner = runner
	packageActionManagerAvailable = available
	packageActionRetryWait = func(ctx context.Context) bool { return ctx.Err() == nil }
	return func() {
		packageActionCommandRunner = oldRunner
		packageActionManagerAvailable = oldAvailable
		packageActionRetryWait = oldWait
	}
}

func replaceStoreSearchHook(search storeSearchFunc) func() {
	oldSearch := packageActionStoreSearch
	packageActionStoreSearch = search
	return func() {
		packageActionStoreSearch = oldSearch
	}
}

func packageActionTargetFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--id" && i+1 < len(args) {
			return args[i+1]
		}
	}
	for i, arg := range args {
		if (arg == "install" || arg == "update" || arg == "upgrade") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testUpdateJobApp() *App {
	return &App{inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{
		{Key: "winget:Git.Git", Manager: managerWinget, ID: "Git.Git", Name: "Git", UpdateAvailable: true, UpdateSupported: true},
		{Key: "choco:gh", Manager: managerChoco, ID: "gh", Name: "GitHub CLI", UpdateAvailable: true, UpdateSupported: true},
		{Key: "winget:Vendor.Unknown", Manager: managerWinget, ID: "Vendor.Unknown", Name: "Unknown App", Version: "Unknown", AvailableVersion: "1.2.0", UpdateAvailable: true, UpdateSupported: true, UnknownVersion: true},
	}}}}
}

func waitForUpdateJobStopped(t *testing.T, app *App) UpdateJobStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := app.updateJobStatus()
		if !status.Running {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("update job did not stop")
	return UpdateJobStatus{}
}
