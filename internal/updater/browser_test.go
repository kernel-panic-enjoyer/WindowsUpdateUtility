package updater

import (
	"context"
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
)

const browserTestToken = "browser-bootstrap-token"

func newBrowserContext(t *testing.T) (context.Context, context.CancelFunc) {
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
	timeoutContext, timeoutCancel := context.WithTimeout(browserContext, 25*time.Second)
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

func newBrowserTestApp() *App {
	now := time.Now()
	return &App{
		token:        browserTestToken,
		sessionToken: "browser-session-token",
		status: StatusResponse{Managers: map[string]ManagerStatus{
			managerWinget: {Available: true, Version: "v1.test", Path: "winget.exe"},
			managerStore:  {Available: true, Path: "store.exe", InventoryAvailable: true, InventoryBackend: inventoryBackendAppX, ActionBackend: backendStoreCLI},
			managerChoco:  {Available: true, Version: "2.test", Path: "choco.exe"},
		}},
		statusFetchedAt: now,
		inventory: Inventory{PackageLookup: PackageLookup{
			Managers: map[string]ManagerStatus{
				managerWinget: {Available: true, Version: "v1.test", Path: "winget.exe"},
				managerStore:  {Available: true, Path: "store.exe", InventoryAvailable: true, InventoryBackend: inventoryBackendAppX, ActionBackend: backendStoreCLI},
				managerChoco:  {Available: true, Version: "2.test", Path: "choco.exe"},
			},
			Packages: []Package{
				{Key: "winget:Test.App", Manager: managerWinget, ID: "Test.App", Name: "Browser Test App", Version: "1.0.0", AvailableVersion: "1.1.0", UpdateAvailable: true, UpdateSupported: true, Installed: true, Source: sourceWinget},
				{Key: "choco:current", Manager: managerChoco, ID: "current", Name: "Current Tool", Version: "2.0.0", AvailableVersion: "2.0.0", UpdateSupported: true, Installed: true, Source: managerChoco},
			},
		}},
		inventoryFetchedAt: now,
	}
}

func startBrowserTestServer(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(app.serveHTTP))
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
	app := newBrowserTestApp()
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
	app := newBrowserTestApp()
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

func TestBrowserSearchShowsPartialFailuresAndProvenance(t *testing.T) {
	restoreManagers := replaceManagerDetectionCache(map[string]ManagerStatus{
		managerWinget: {Available: true},
		managerStore:  {Available: true},
		managerChoco:  {Available: true},
	})
	defer restoreManagers()
	restoreSearch := replacePackageSearchRunnersForTest([]packageSearchRunner{
		{managerWinget, func(string) packageSearchResult {
			return packageSearchResult{ResultKey: managerWinget, CommandResult: CommandResult{Command: "winget search gh", Code: 1, Stderr: "winget source unavailable"}}
		}},
		{managerStore, func(string) packageSearchResult {
			return packageSearchResult{
				ResultKey:     managerStore,
				CommandResult: CommandResult{OK: true, Command: "store search gh"},
				Packages: []Package{{
					Key:           "store:GitHubCLI",
					Manager:       managerStore,
					ID:            "GitHubCLI",
					Name:          "GitHub CLI Store",
					Version:       "1.0.0",
					Source:        sourceStoreCLI,
					ActionBackend: backendStoreCLI,
					MatchReason:   "Package name contains the search text.",
				}},
			}
		}},
		{managerChoco, func(string) packageSearchResult {
			return packageSearchResult{
				ResultKey:     managerChoco,
				CommandResult: CommandResult{OK: true, Command: "choco search gh"},
				Packages: []Package{{
					Key:         "choco:gh",
					Manager:     managerChoco,
					ID:          "gh",
					Name:        "GitHub CLI",
					Version:     "2.0.0",
					Source:      managerChoco,
					MatchReason: "Exact package ID match.",
				}},
			}
		}},
	})
	defer restoreSearch()

	app := newBrowserTestApp()
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
	restore := replaceUpdateJobHooksWithRefresh(func(ctx context.Context, manager, id string) CommandResult {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return CommandResult{Command: "update " + id, Code: commandCancelledCode, Stderr: "Cancelled."}
	}, func(app *App) {})
	defer restore()

	app := newBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	navigateAuthenticated(t, ctx, server.URL)
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
		status := app.latestOperationJobStatus(jobTypeUpdateAll, jobTypeUpdate)
		if status.State == jobStateCancelled || status.CancelRequested {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cancel button did not request cancellation")
}

func TestBrowserIgnoresStalePackageResponses(t *testing.T) {
	app := newBrowserTestApp()
	var packageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages":
			setSecurityHeaders(w)
			if !app.sessionOK(r) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			count := packageRequests.Add(1)
			if count == 1 {
				time.Sleep(700 * time.Millisecond)
				writeJSON(w, http.StatusOK, InventoryResponse{Inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{{Key: "winget:old", Manager: managerWinget, ID: "old", Name: "Stale Package", Installed: true}}}}})
				return
			}
			writeJSON(w, http.StatusOK, InventoryResponse{Inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{{Key: "winget:new", Manager: managerWinget, ID: "new", Name: "Fresh Package", Installed: true}}}}})
		case "/api/inventory/refresh":
			setSecurityHeaders(w)
			writeJSON(w, http.StatusAccepted, OperationJobStatus{JobID: "refresh-job", Type: jobTypeInventoryRefresh, State: jobStateSucceeded, Total: 1, Notice: "Package status refreshed."})
		case "/api/jobs/status":
			setSecurityHeaders(w)
			writeJSON(w, http.StatusOK, OperationJobStatus{JobID: "refresh-job", Type: jobTypeInventoryRefresh, State: jobStateSucceeded, Total: 1, Notice: "Package status refreshed."})
		default:
			app.serveHTTP(w, r)
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

func TestBrowserKeyboardAccessibilityAndMobileLayout(t *testing.T) {
	restoreManagers := replaceManagerDetectionCache(map[string]ManagerStatus{
		managerWinget: {Available: false, Error: "winget unavailable"},
		managerStore:  {Available: false, Error: "store unavailable"},
		managerChoco:  {Available: true},
	})
	defer restoreManagers()
	restoreSearch := replacePackageSearchRunnersForTest([]packageSearchRunner{
		{managerChoco, func(string) packageSearchResult {
			return packageSearchResult{
				ResultKey:     managerChoco,
				CommandResult: CommandResult{OK: true, Command: "choco search keyboard"},
				Packages: []Package{{
					Key:         "choco:keyboard-tool",
					Manager:     managerChoco,
					ID:          "keyboard-tool",
					Name:        "Keyboard Tool",
					Version:     "1.0.0",
					Source:      managerChoco,
					MatchReason: "Package name contains the search text.",
				}},
			}
		}},
	})
	defer restoreSearch()

	app := newBrowserTestApp()
	server := startBrowserTestServer(t, app)
	ctx, cancel := newBrowserContext(t)
	defer cancel()

	if err := chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(390, 900, 1, false)); err != nil {
		t.Fatal(err)
	}
	navigateAuthenticated(t, ctx, server.URL)
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
	err := chromedp.Run(ctx,
		chromedp.AttributeValue(`#log-tab-application`, `aria-selected`, &selectedTab, nil, chromedp.ByQuery),
		chromedp.Evaluate(browserAccessibilityScanScript(), &accessibilityIssues),
		chromedp.Evaluate(`document.documentElement.scrollWidth > document.documentElement.clientWidth + 1`, &hasHorizontalOverflow),
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

func replaceManagerDetectionCache(managers map[string]ManagerStatus) func() {
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

func replacePackageSearchRunnersForTest(runners []packageSearchRunner) func() {
	old := packageSearchRunners
	packageSearchRunners = runners
	return func() {
		packageSearchRunners = old
	}
}
