//go:build uitestsupport

// Package updater's UI test-support surface.
//
// This file is compiled ONLY under the "uitestsupport" build tag. It exists so
// the out-of-tree browser test module (windows-updater-webui/tests/browser) can
// drive the App as a black-box consumer without importing chromedp into this
// (production) module. Because the tag is never set by `go build` or the normal
// `go test ./...` run, none of these exported symbols enter production builds or
// this package's public API in regular use.
//
// It imports only the standard library, so it cannot pull browser-only
// dependencies back into the production module graph.

package updater

import (
	"context"
	"net/http"
	"time"
)

// Bootstrap token used by NewBrowserTestApp; exported so navigation/DOM
// assertions in the browser module can reference the same value.
const BrowserTestToken = "browser-bootstrap-token"

// SessionCookieName mirrors the production session cookie name for browser
// tests that construct their own HTTP server.
const SessionCookieName = sessionCookieName

// Exported mirrors of internal package constants the browser tests rely on.
const (
	ManagerWinget = managerWinget
	ManagerStore  = managerStore
	ManagerChoco  = managerChoco

	SourceWinget   = sourceWinget
	SourceStoreCLI = sourceStoreCLI

	InventoryBackendAppX  = inventoryBackendAppX
	ActionBackendStoreCLI = backendStoreCLI

	CommandCancelledCode = commandCancelledCode

	JobTypeInventoryRefresh = jobTypeInventoryRefresh
	JobStateSucceeded       = jobStateSucceeded
	JobStateCancelled       = jobStateCancelled
)

// NewBrowserTestApp builds an App preloaded with deterministic status and
// inventory fixtures for browser-level UI tests.
func NewBrowserTestApp() *App {
	now := time.Now()
	managers := map[string]ManagerStatus{
		managerWinget: {Available: true, Version: "v1.test", Path: "winget.exe"},
		managerStore:  {Available: true, Path: "store.exe", InventoryAvailable: true, InventoryBackend: inventoryBackendAppX, ActionBackend: backendStoreCLI},
		managerChoco:  {Available: true, Version: "2.test", Path: "choco.exe"},
	}
	return &App{
		token:           BrowserTestToken,
		sessionToken:    "browser-session-token",
		status:          StatusResponse{Managers: cloneManagerStatuses(managers)},
		statusFetchedAt: now,
		inventory: Inventory{PackageLookup: PackageLookup{
			Managers: cloneManagerStatuses(managers),
			Packages: []Package{
				{Key: "winget:Test.App", Manager: managerWinget, ID: "Test.App", Name: "Browser Test App", Version: "1.0.0", AvailableVersion: "1.1.0", UpdateAvailable: true, UpdateSupported: true, Installed: true, Source: sourceWinget},
				{Key: "choco:current", Manager: managerChoco, ID: "current", Name: "Current Tool", Version: "2.0.0", AvailableVersion: "2.0.0", UpdateSupported: true, Installed: true, Source: managerChoco},
			},
		}},
		inventoryFetchedAt: now,
	}
}

// TestHandler exposes the App HTTP handler for black-box browser tests.
func (app *App) TestHandler() http.HandlerFunc { return app.serveHTTP }

// TestSessionOK reports whether the request carries a valid session.
func (app *App) TestSessionOK(r *http.Request) bool { return app.sessionOK(r) }

// LatestUpdateJobStatus returns the most recent update-all/update job status.
func (app *App) LatestUpdateJobStatus() OperationJobStatus {
	return app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
}

// SetTestSecurityHeaders applies the production security headers.
func SetTestSecurityHeaders(w http.ResponseWriter) { setSecurityHeaders(w) }

// WriteTestJSON writes a JSON payload using the production encoder.
func WriteTestJSON(w http.ResponseWriter, status int, payload any) { writeJSON(w, status, payload) }

// ReplaceManagerDetectionCacheForTest swaps the cached manager-detection map and
// returns a restore function.
func ReplaceManagerDetectionCacheForTest(managers map[string]ManagerStatus) func() {
	managerDetectionCache.mu.Lock()
	oldCached := managerDetectionCache.cached
	oldFetchedAt := managerDetectionCache.fetchedAt
	oldInFlight := managerDetectionCache.inFlight
	managerDetectionCache.cached = cloneManagerStatuses(managers)
	managerDetectionCache.fetchedAt = time.Now()
	managerDetectionCache.inFlight = nil
	managerDetectionCache.mu.Unlock()
	return func() {
		managerDetectionCache.mu.Lock()
		managerDetectionCache.cached = oldCached
		managerDetectionCache.fetchedAt = oldFetchedAt
		managerDetectionCache.inFlight = oldInFlight
		managerDetectionCache.mu.Unlock()
	}
}

// StubSearchResult describes a fake package-manager search runner: given a query
// it returns the command outcome and any packages that manager surfaced.
type StubSearchResult struct {
	Manager string
	Run     func(query string) (CommandResult, []Package)
}

// ReplacePackageSearchRunnersForTest installs fake search runners and returns a
// restore function. It adapts the exported stub shape to the internal runner type.
func ReplacePackageSearchRunnersForTest(stubs []StubSearchResult) func() {
	old := packageSearchRunners
	runners := make([]packageSearchRunner, 0, len(stubs))
	for _, stub := range stubs {
		stub := stub
		runners = append(runners, packageSearchRunner{
			Manager: stub.Manager,
			Run: func(query string) packageSearchResult {
				result, packages := stub.Run(query)
				return packageSearchResult{ResultKey: stub.Manager, CommandResult: result, Packages: packages}
			},
		})
	}
	packageSearchRunners = runners
	return func() { packageSearchRunners = old }
}

// ReplaceUpdateJobHooksWithRefresh swaps the update-job runner and post-job
// refresh hook, returning a restore function.
func ReplaceUpdateJobHooksWithRefresh(runner func(context.Context, string, string) CommandResult, refresh func(context.Context, *App, []Package) error) func() {
	return replaceUpdateJobHooksWithRefresh(runner, refresh)
}
