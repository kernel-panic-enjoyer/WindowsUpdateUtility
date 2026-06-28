// Package browser holds the chromedp-driven, browser-level UI tests for the
// Windows Updater Web UI.
//
// These tests live in a SEPARATE Go module (windows-updater-webui/tests/browser)
// so that chromedp and cdproto stay out of the production module's dependency
// graph. They drive the app as a black-box consumer of the exported test-support
// surface in internal/updater/uitestsupport.go, and therefore must be built with
// the matching build tag, e.g.:
//
//	go test -tags uitestsupport ./...
//
// Each test self-skips when no Chromium/Edge browser is available.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	updater "windows-updater-webui/internal/updater"
)

const browserTestToken = updater.BrowserTestToken

func newBrowserContext(t *testing.T) (context.Context, context.CancelFunc) {
	return newBrowserContextWithTimeout(t, 25*time.Second)
}

func newBrowserContextWithTimeout(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	exe, ok := chromiumExecutable()
	if !ok {
		t.Skip("Chromium or Edge browser not found; skipping browser-level UI test")
	}
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(exe),
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-extensions", true),
	)
	allocatorContext, allocatorCancel := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	browserContext, browserCancel := chromedp.NewContext(allocatorContext)
	timeoutContext, timeoutCancel := context.WithTimeout(browserContext, timeout)
	return timeoutContext, func() {
		timeoutCancel()
		browserCancel()
		allocatorCancel()
	}
}

func chromiumExecutable() (string, bool) {
	if fromEnv := strings.TrimSpace(os.Getenv("CHROME_PATH")); fromEnv != "" && fileExists(fromEnv) {
		return fromEnv, true
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, true
		}
	}
	for _, name := range []string{"msedge.exe", "chrome.exe", "chromium.exe", "msedge", "google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func startBrowserTestServer(t *testing.T, app *updater.App) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(app.TestHandler())
	t.Cleanup(server.Close)
	return server
}

func navigateAuthenticated(t *testing.T, ctx context.Context, serverURL string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(serverURL+"/?token="+browserTestToken),
		chromedp.WaitVisible(`#search-input`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
}

func waitForText(t *testing.T, ctx context.Context, selector, want string) string {
	t.Helper()
	var text string
	err := chromedp.Run(ctx,
		chromedp.Poll(fmt.Sprintf(`document.querySelector(%q) && document.querySelector(%q).innerText.includes(%q)`, selector, selector, want), nil, chromedp.WithPollingInterval(50*time.Millisecond), chromedp.WithPollingTimeout(8*time.Second)),
		chromedp.Text(selector, &text, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestBrowserAuthBootstrapURLCleanupAndSecurityHeaders(t *testing.T) {
	app := updater.NewBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)

	var currentURL, documentCookie string
	var tokenInDOM bool
	var statusCode int
	var csp string
	err := chromedp.Run(ctx,
		chromedp.Location(&currentURL),
		chromedp.Evaluate(`document.cookie`, &documentCookie),
		chromedp.Evaluate(`document.documentElement.outerHTML.indexOf("`+browserTestToken+`") !== -1`, &tokenInDOM),
		chromedp.Evaluate(`(() => { const xhr = new XMLHttpRequest(); xhr.open("GET", "/api/status", false); xhr.send(); return xhr.status; })()`, &statusCode),
		chromedp.Evaluate(`(() => { const xhr = new XMLHttpRequest(); xhr.open("GET", window.location.pathname, false); xhr.send(); return xhr.getResponseHeader("content-security-policy") || ""; })()`, &csp),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(currentURL, "token=") {
		t.Fatalf("bootstrap URL was not cleaned: %s", currentURL)
	}
	if documentCookie != "" {
		t.Fatalf("session cookie should be HttpOnly and hidden from document.cookie, got %q", documentCookie)
	}
	if tokenInDOM {
		t.Fatal("bootstrap token leaked into rendered DOM")
	}
	if statusCode != http.StatusOK {
		t.Fatalf("authenticated API fetch returned status %d", statusCode)
	}
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "style-src 'self'") {
		t.Fatalf("CSP does not load scripts/styles from self only: %q", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP still allows inline code: %q", csp)
	}
}

func TestBrowserStopButtonUsesAsyncShutdownRequest(t *testing.T) {
	app := updater.NewBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	if err := chromedp.Run(ctx,
		chromedp.Click(`#shutdown-button`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	notice := waitForText(t, ctx, `#notice`, "Application is stopping")
	var currentURL, body string
	if err := chromedp.Run(ctx,
		chromedp.Location(&currentURL),
		chromedp.Text(`body`, &body, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(currentURL, "/shutdown") {
		t.Fatalf("stop button navigated to shutdown response: %s", currentURL)
	}
	if strings.Contains(body, "forbidden origin") || strings.Contains(body, `"error"`) {
		t.Fatalf("stop button rendered JSON error instead of staying on dashboard; notice=%q body=%q", notice, body)
	}
}

func TestBrowserConnectionBadgeExpiresWhenBackendStops(t *testing.T) {
	app := updater.NewBrowserTestApp()
	server := httptest.NewServer(app.TestHandler())
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	ctx, cancel := newBrowserContextWithTimeout(t, 40*time.Second)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	waitForText(t, ctx, `#log-connection-status`, "Connected")
	server.CloseClientConnections()
	server.Close()

	var text string
	err := chromedp.Run(ctx,
		chromedp.Poll(`(() => {
		  const badge = document.querySelector("#log-connection-status");
		  return !!badge && !badge.innerText.includes("Connected");
		})()`, nil, chromedp.WithPollingInterval(100*time.Millisecond), chromedp.WithPollingTimeout(22*time.Second)),
		chromedp.Text(`#log-connection-status`, &text, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Connected") {
		t.Fatalf("connection badge stayed connected after backend stopped: %q", text)
	}
}

func TestBrowserWingetLogTabSurvivesStoreFlood(t *testing.T) {
	app := updater.NewBrowserTestApp()
	logPayload := browserLogFloodPayload(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/events" {
			updater.SetTestSecurityHeaders(w)
			if !app.TestSessionOK(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "event: logs\ndata: %s\n\n", logPayload)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		app.TestHandler()(w, r)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	waitForText(t, ctx, `#log-connection-status`, "Connected")
	if err := chromedp.Run(ctx,
		chromedp.Click(`[data-log-category="winget"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	waitForText(t, ctx, `#session-log`, "Older log entries were discarded before this point.")
	logText := waitForText(t, ctx, `#session-log`, "winget upgrade --id yt-dlp.FFmpeg --exact")
	if !strings.Contains(logText, "portable package was modified") {
		t.Fatalf("winget tab lost failure diagnostics after Store flood:\n%s", logText)
	}
}

func browserLogFloodPayload(t *testing.T) string {
	t.Helper()
	type entry struct {
		ID         int64    `json:"id"`
		Timestamp  string   `json:"timestamp"`
		Stream     string   `json:"stream"`
		Message    string   `json:"message"`
		Categories []string `json:"categories"`
		JobID      string   `json:"job_id,omitempty"`
		PackageKey string   `json:"package_key,omitempty"`
		Manager    string   `json:"manager,omitempty"`
		CommandID  string   `json:"command_id,omitempty"`
	}
	entries := []entry{{
		ID:         100,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Stream:     "stderr",
		Message:    "winget upgrade --id yt-dlp.FFmpeg --exact failed: portable package was modified",
		Categories: []string{"all", "winget", "updates", "mutations"},
		JobID:      "job-browser-winget",
		PackageKey: "winget:yt-dlp.FFmpeg",
		Manager:    updater.ManagerWinget,
		CommandID:  "cmd-browser-winget",
	}}
	for i := 0; i < 3200; i++ {
		entries = append(entries, entry{
			ID:         int64(101 + i),
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Stream:     "stdout",
			Message:    "store marketing description rating screenshot filler",
			Categories: []string{"all", "store", "store-scan"},
			Manager:    updater.ManagerStore,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"entries":       entries,
		"oldest_id":     int64(100),
		"latest_id":     int64(3300),
		"dropped_count": int64(99),
		"dropped_bytes": int64(4096),
		"gap_detected":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestBrowserSearchShowsPartialFailuresAndProvenance(t *testing.T) {
	restoreManagers := updater.ReplaceManagerDetectionCacheForTest(map[string]updater.ManagerStatus{
		updater.ManagerWinget: {Available: true},
		updater.ManagerStore:  {Available: true},
		updater.ManagerChoco:  {Available: true},
	})
	defer restoreManagers()
	restoreSearch := updater.ReplacePackageSearchRunnersForTest([]updater.StubSearchResult{
		{Manager: updater.ManagerWinget, Run: func(string) (updater.CommandResult, []updater.Package) {
			return updater.CommandResult{Command: "winget search gh", Code: 1, Stderr: "winget source unavailable"}, nil
		}},
		{Manager: updater.ManagerStore, Run: func(string) (updater.CommandResult, []updater.Package) {
			return updater.CommandResult{OK: true, Command: "store search gh"}, []updater.Package{{
				Key:           "store:GitHubCLI",
				Manager:       updater.ManagerStore,
				ID:            "GitHubCLI",
				Name:          "GitHub CLI Store",
				Version:       "1.0.0",
				Source:        updater.SourceStoreCLI,
				ActionBackend: updater.ActionBackendStoreCLI,
				MatchReason:   "Package name contains the search text.",
			}}
		}},
		{Manager: updater.ManagerChoco, Run: func(string) (updater.CommandResult, []updater.Package) {
			return updater.CommandResult{OK: true, Command: "choco search gh"}, []updater.Package{{
				Key:         "choco:gh",
				Manager:     updater.ManagerChoco,
				ID:          "gh",
				Name:        "GitHub CLI",
				Version:     "2.0.0",
				Source:      updater.ManagerChoco,
				MatchReason: "Exact package ID match.",
			}}
		}},
	})
	defer restoreSearch()

	app := updater.NewBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	if err := chromedp.Run(ctx,
		chromedp.SetValue(`#search-input`, `gh`, chromedp.ByQuery),
		chromedp.Click(`#search-form button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#search-provenance`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	provenance := waitForText(t, ctx, `#search-provenance`, "winget search failed")
	if !strings.Contains(provenance, "results from") {
		t.Fatalf("search provenance did not describe partial result sources: %q", provenance)
	}
	table := waitForText(t, ctx, `#search-results-panel`, "Exact package ID match.")
	for _, expected := range []string{"Chocolatey", "Store CLI", "Backend:", "gh", "GitHubCLI"} {
		if !strings.Contains(table, expected) {
			t.Fatalf("search table missing %q in:\n%s", expected, table)
		}
	}
}

func TestBrowserReloadDuringJobAndCancellation(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	restore := updater.ReplaceUpdateJobHooksWithRefresh(func(ctx context.Context, manager, id string) updater.CommandResult {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return updater.CommandResult{Command: "update " + id, Code: updater.CommandCancelledCode, Stderr: "Cancelled."}
	}, func(ctx context.Context, app *updater.App, packages []updater.Package) error { return nil })
	defer restore()

	app := updater.NewBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	waitForText(t, ctx, `#updates-body`, "Browser Test App")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`#update-all-button`, chromedp.ByQuery),
		chromedp.Click(`#update-all-button`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-update-job`, chromedp.ByQuery),
		chromedp.Click(`#confirm-update-job`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("update job did not start")
	}
	waitForText(t, ctx, `#update-progress-status`, "Browser Test App")
	var spinnerMarked bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
		  const spinner = document.querySelector("#update-progress-status .spinner");
		  if (!spinner) return false;
		  spinner.dataset.persistCheck = "yes";
		  return true;
		})()`, &spinnerMarked),
	); err != nil {
		t.Fatal(err)
	}
	if !spinnerMarked {
		t.Fatal("update progress spinner was not present")
	}
	time.Sleep(1300 * time.Millisecond)
	var spinnerPreserved bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
		  const spinner = document.querySelector("#update-progress-status .spinner");
		  return !!spinner && spinner.dataset.persistCheck === "yes";
		})()`, &spinnerPreserved),
	); err != nil {
		t.Fatal(err)
	}
	if !spinnerPreserved {
		t.Fatal("update progress spinner was recreated during status polling")
	}
	if err := chromedp.Run(ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(`#search-input`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	waitForText(t, ctx, `#update-progress-status`, "Browser Test App")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`#cancel-updates-button`, chromedp.ByQuery),
		chromedp.Click(`#cancel-updates-button`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := app.LatestUpdateJobStatus()
		if status.State == updater.JobStateCancelled || status.CancelRequested {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cancel button did not request cancellation")
}

func TestBrowserIgnoresStalePackageResponses(t *testing.T) {
	app := updater.NewBrowserTestApp()
	var packageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages":
			updater.SetTestSecurityHeaders(w)
			if !app.TestSessionOK(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			count := packageRequests.Add(1)
			if count == 1 {
				time.Sleep(700 * time.Millisecond)
				updater.WriteTestJSON(w, http.StatusOK, updater.InventoryResponse{Inventory: updater.Inventory{PackageLookup: updater.PackageLookup{Packages: []updater.Package{{Key: "winget:old", Manager: updater.ManagerWinget, ID: "old", Name: "Stale Package", Installed: true}}}}})
				return
			}
			updater.WriteTestJSON(w, http.StatusOK, updater.InventoryResponse{Inventory: updater.Inventory{PackageLookup: updater.PackageLookup{Packages: []updater.Package{{Key: "winget:new", Manager: updater.ManagerWinget, ID: "new", Name: "Fresh Package", Installed: true}}}}})
		case "/api/inventory/refresh":
			updater.SetTestSecurityHeaders(w)
			updater.WriteTestJSON(w, http.StatusAccepted, updater.OperationJobStatus{JobID: "refresh-job", Type: updater.JobTypeInventoryRefresh, State: updater.JobStateSucceeded, Total: 1, Notice: "Package status refreshed."})
		case "/api/jobs/status":
			updater.SetTestSecurityHeaders(w)
			updater.WriteTestJSON(w, http.StatusOK, updater.OperationJobStatus{JobID: "refresh-job", Type: updater.JobTypeInventoryRefresh, State: updater.JobStateSucceeded, Total: 1, Notice: "Package status refreshed."})
		default:
			app.TestHandler()(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	for packageRequests.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`#refresh-packages`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	waitForText(t, ctx, `body`, "Fresh Package")
	time.Sleep(900 * time.Millisecond)
	var body string
	if err := chromedp.Run(ctx, chromedp.Text(`body`, &body, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Stale Package") {
		t.Fatalf("stale package response overwrote fresh table:\n%s", body)
	}
}

func TestBrowserManagerFilterUsesVisiblePackages(t *testing.T) {
	app := updater.NewBrowserTestApp()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages":
			updater.SetTestSecurityHeaders(w)
			if !app.TestSessionOK(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			updater.WriteTestJSON(w, http.StatusOK, updater.InventoryResponse{Inventory: updater.Inventory{PackageLookup: updater.PackageLookup{
				Managers: map[string]updater.ManagerStatus{
					updater.ManagerWinget: {Available: true},
					updater.ManagerStore:  {Available: false, InventoryAvailable: true, InventoryBackend: updater.InventoryBackendAppX, Error: "store unavailable"},
					updater.ManagerChoco:  {Available: true},
				},
				Packages: []updater.Package{
					{Key: "winget:Visible.App", Manager: updater.ManagerWinget, ID: "Visible.App", Name: "Visible Winget App", Installed: true, UpdateSupported: true},
					{Key: "store:Visible.Store_abc123", Manager: updater.ManagerStore, ID: "Visible.Store_abc123", Name: "Visible Store App", Installed: true, UpdateSupported: false},
				},
			}}})
		default:
			app.TestHandler()(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	waitForText(t, ctx, `#installed-section`, "Visible Store App")
	var storeOptionEnabled bool
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
		  const option = [...document.querySelector("#installed-manager-filter").options].find((item) => item.value === "store");
		  return !!option && !option.hidden && !option.disabled;
		})()`, &storeOptionEnabled),
	); err != nil {
		t.Fatal(err)
	}
	if !storeOptionEnabled {
		t.Fatal("installed Microsoft Store filter option should stay enabled when Store inventory rows are present")
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
		  const select = document.querySelector("#installed-manager-filter");
		  select.value = "store";
		  select.dispatchEvent(new Event("change", {bubbles:true}));
		})()`, nil),
	); err != nil {
		t.Fatal(err)
	}
	section := waitForText(t, ctx, `#installed-section`, "Visible Store App")
	if strings.Contains(section, "Visible Winget App") {
		t.Fatalf("installed Store filter should hide non-Store rows:\n%s", section)
	}
}

func TestBrowserHidesStaleStoreEvidenceFromUpdatesQueue(t *testing.T) {
	app := updater.NewBrowserTestApp()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages":
			updater.SetTestSecurityHeaders(w)
			if !app.TestSessionOK(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			updater.WriteTestJSON(w, http.StatusOK, updater.InventoryResponse{Inventory: updater.Inventory{PackageLookup: updater.PackageLookup{
				Managers: map[string]updater.ManagerStatus{
					updater.ManagerWinget: {Available: true},
					updater.ManagerStore:  {Available: true, InventoryAvailable: true, InventoryBackend: updater.InventoryBackendAppX, ActionBackend: updater.ActionBackendStoreCLI},
				},
				Packages: []updater.Package{
					{Key: "winget:Visible.Update", Manager: updater.ManagerWinget, ID: "Visible.Update", Name: "Visible Winget Update", Version: "1.0.0", AvailableVersion: "1.1.0", UpdateAvailable: true, UpdateSupported: true, Installed: true, Source: updater.SourceWinget, PreferenceEligible: true, CanUpdateNow: true},
					{
						Key:                        "store:Hidden.Store_abc123",
						Manager:                    updater.ManagerStore,
						ID:                         "Hidden.Store_abc123",
						Name:                       "Hidden Stale Store",
						Version:                    "1.0.0",
						Installed:                  true,
						UpdateSupported:            false,
						UpdateState:                "available",
						UpdateReason:               "retained previous positive update because the latest scan was incomplete",
						Stale:                      true,
						InstalledPackageFamilyName: "Hidden.Store_abc123",
						CannotUpdateReason:         "Store update requires a fresh assessment; rescan required.",
						ProviderSummaries: []updater.StorePackageProviderSummary{{
							Name:   "previous-generation",
							Health: "stale",
							Kind:   "stale_result",
						}},
					},
				},
			}, StoreScanHealth: updater.StoreScanHealthSummary{
				Active:        true,
				Healthy:       false,
				Authoritative: false,
				Status:        "incomplete",
				Reason:        "retained previous positive update because the latest scan was incomplete",
				Counts:        map[string]int{"available": 1, "stale": 1},
			}}})
		default:
			app.TestHandler()(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
	waitForText(t, ctx, `#updates-body`, "Visible Winget Update")
	waitForText(t, ctx, `#store-scan-health-body`, "Hidden Stale Store")
	var updatesBody string
	if err := chromedp.Run(ctx, chromedp.Text(`#updates-body`, &updatesBody, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updatesBody, "Hidden Stale Store") || strings.Contains(updatesBody, "Stale evidence") {
		t.Fatalf("stale Store evidence should stay out of Updates Available:\n%s", updatesBody)
	}
}

func TestBrowserKeyboardAccessibilityAndMobileLayout(t *testing.T) {
	restoreManagers := updater.ReplaceManagerDetectionCacheForTest(map[string]updater.ManagerStatus{
		updater.ManagerWinget: {Available: false, Error: "winget unavailable"},
		updater.ManagerStore:  {Available: false, Error: "store unavailable"},
		updater.ManagerChoco:  {Available: true},
	})
	defer restoreManagers()
	restoreSearch := updater.ReplacePackageSearchRunnersForTest([]updater.StubSearchResult{
		{Manager: updater.ManagerChoco, Run: func(string) (updater.CommandResult, []updater.Package) {
			return updater.CommandResult{OK: true, Command: "choco search keyboard"}, []updater.Package{{
				Key:         "choco:keyboard-tool",
				Manager:     updater.ManagerChoco,
				ID:          "keyboard-tool",
				Name:        "Keyboard Tool",
				Version:     "1.0.0",
				Source:      updater.ManagerChoco,
				MatchReason: "Package name contains the search text.",
			}}
		}},
	})
	defer restoreSearch()

	app := updater.NewBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	if err := chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(390, 900, 1, false)); err != nil {
		t.Fatal(err)
	}
	navigateAuthenticated(t, ctx, server.URL)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.dataset.theme = "light"`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx,
		chromedp.Focus(`#search-input`, chromedp.ByQuery),
		chromedp.SendKeys(`#search-input`, `keyboard`+"\n", chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	waitForText(t, ctx, `#search-results-panel`, "Keyboard Tool")
	if err := chromedp.Run(ctx,
		chromedp.Click(`#log-tab-all`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
		  const tab = document.querySelector("#log-tab-all");
		  tab.focus();
		  tab.dispatchEvent(new KeyboardEvent("keydown", {key:"ArrowRight", bubbles:true}));
		})()`, nil),
	); err != nil {
		t.Fatal(err)
	}
	var selectedTab string
	var accessibilityIssues []string
	var hasHorizontalOverflow bool
	var nativeColorScheme string
	err := chromedp.Run(ctx,
		chromedp.AttributeValue(`#log-tab-application`, `aria-selected`, &selectedTab, nil, chromedp.ByQuery),
		chromedp.Evaluate(browserAccessibilityScanScript(), &accessibilityIssues),
		chromedp.Evaluate(`document.documentElement.scrollWidth > document.documentElement.clientWidth + 1`, &hasHorizontalOverflow),
		chromedp.Evaluate(`getComputedStyle(document.documentElement).colorScheme`, &nativeColorScheme),
	)
	if err != nil {
		t.Fatal(err)
	}
	if selectedTab != "true" {
		t.Fatalf("keyboard arrow navigation did not move log tab focus/selection, aria-selected=%q", selectedTab)
	}
	if len(accessibilityIssues) > 0 {
		t.Fatalf("browser accessibility scan found issues: %s", strings.Join(accessibilityIssues, "; "))
	}
	if hasHorizontalOverflow {
		t.Fatal("mobile-width layout overflows the viewport")
	}
	if nativeColorScheme != "light" {
		t.Fatalf("light theme should use light native form controls, color-scheme=%q", nativeColorScheme)
	}
}

func browserAccessibilityScanScript() string {
	return `(() => {
  const issues = [];
  const text = (node) => (node.innerText || node.textContent || "").trim();
  const nameFor = (node) => {
    if (node.getAttribute("aria-label")) return node.getAttribute("aria-label").trim();
    const labelledBy = node.getAttribute("aria-labelledby");
    if (labelledBy) {
      const label = document.getElementById(labelledBy);
      if (label && text(label)) return text(label);
    }
    if (node.id) {
      const explicit = document.querySelector('label[for="' + CSS.escape(node.id) + '"]');
      if (explicit && text(explicit)) return text(explicit);
    }
    const wrappingLabel = node.closest("label");
    if (wrappingLabel && text(wrappingLabel)) return text(wrappingLabel);
    return text(node);
  };
  document.querySelectorAll("button").forEach((button) => {
    if (!nameFor(button)) issues.push("button without accessible name: " + (button.id || button.className || button.outerHTML.slice(0, 40)));
  });
  document.querySelectorAll("input").forEach((input) => {
    if (input.type === "hidden") return;
    if (!nameFor(input) && !input.placeholder) issues.push("input without accessible name: " + (input.id || input.name || input.type));
  });
  document.querySelectorAll('[role="progressbar"]').forEach((bar) => {
    if (!nameFor(bar)) issues.push("progressbar without accessible name");
  });
  document.querySelectorAll('[role="tab"]').forEach((tab) => {
    if (!tab.hasAttribute("aria-selected")) issues.push("tab without aria-selected: " + (tab.id || text(tab)));
    const panelID = tab.getAttribute("aria-controls");
    if (!panelID || !document.getElementById(panelID)) issues.push("tab without valid aria-controls: " + (tab.id || text(tab)));
  });
  const ids = new Set();
  document.querySelectorAll("[id]").forEach((node) => {
    if (ids.has(node.id)) issues.push("duplicate id: " + node.id);
    ids.add(node.id);
  });
  return issues;
})()`
}
