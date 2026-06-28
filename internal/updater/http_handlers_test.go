package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPILogsRequiresTokenAndReturnsEntries(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	sessionLogs.Append("app", "hello")
	app := testSessionApp()

	badRequest := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	badResponse := httptest.NewRecorder()
	app.serveHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized log request, got %d", badResponse.Code)
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/logs", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}

	var decoded logsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LatestID != 1 || len(decoded.Entries) != 1 || decoded.Entries[0].Message != "hello" {
		t.Fatalf("unexpected log response: %#v", decoded)
	}
	if !logEntryInCategory(decoded.Entries[0], logCategoryApplication) {
		t.Fatalf("expected application log category: %#v", decoded.Entries[0])
	}
}

func TestAPILogsReportsGapMetadata(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{maxEntries: 3, maxBytes: 64 * 1024}
	defer func() { sessionLogs = oldLogs }()
	for _, message := range []string{"one", "two", "three", "four", "five"} {
		sessionLogs.Append("app", message)
	}
	app := testSessionApp()
	request := authenticatedRequest(app, http.MethodGet, "/api/logs?since=1", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var decoded logsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.GapDetected || decoded.DroppedCount == 0 || decoded.OldestID != 3 || decoded.LatestID != 5 {
		t.Fatalf("unexpected gap response: %#v", decoded)
	}
}

func TestAPIJobsRequiresTokenAndReturnsJobs(t *testing.T) {
	app := testSessionApp()
	status := app.startOperationJob(jobTypeInventoryRefresh, "", 1, nil, func(ctx context.Context, job *OperationJob) {
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.State = jobStateSucceeded
		})
	})

	badRequest := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	badResponse := httptest.NewRecorder()
	app.serveHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized jobs request, got %d", badResponse.Code)
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/jobs", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var decoded jobsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Jobs) != 1 || decoded.Jobs[0].JobID != status.JobID {
		t.Fatalf("unexpected jobs response: %#v", decoded)
	}
}

func TestAPIJobLogRequiresTokenAndRejectsUnknownJob(t *testing.T) {
	app := testSessionApp()

	badRequest := httptest.NewRequest(http.MethodGet, "/api/jobs/log?job_id=missing", nil)
	badResponse := httptest.NewRecorder()
	app.serveHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized job log request, got %d", badResponse.Code)
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/jobs/log?job_id=missing", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected not found for unknown job, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAPIJobLogReturnsCorrelatedEntries(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	app := testSessionApp()
	status := app.startOperationJob(jobTypeUpdate, "", 1, []string{"winget|Git.Git"}, func(ctx context.Context, job *OperationJob) {
		ctx = withLogMetadata(ctx, logMetadata{JobID: job.status.JobID, JobType: job.status.Type, PackageKey: "winget|Git.Git", Manager: managerWinget})
		sessionLogs.AppendContext(ctx, "command", "winget upgrade --id Git.Git --exact", logCategoriesForCommand([]string{"winget", "upgrade", "--id", "Git.Git", "--exact"}))
		sessionLogs.AppendContext(ctx, "stderr", "upgrade failed", logCategoriesForCommand([]string{"winget", "upgrade", "--id", "Git.Git", "--exact"}))
		app.mutateOperationJob(job, func(status *OperationJobStatus) {
			status.State = jobStateFailed
		})
	})
	if _, ok := waitForOperationJobState(app, status.JobID, time.Second); !ok {
		t.Fatal("job did not finish")
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/jobs/log?job_id="+status.JobID, nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var decoded logsAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	joined := joinLogMessages(decoded.Entries)
	if !strings.Contains(joined, "Git.Git") || !strings.Contains(joined, "upgrade failed") {
		t.Fatalf("job log missing correlated command output: %q", joined)
	}
}

func TestAPILogsExportRequiresTokenAndReturnsZip(t *testing.T) {
	oldLogs := sessionLogs
	sessionLogs = &LogBuffer{}
	defer func() { sessionLogs = oldLogs }()

	sessionLogs.Append("app", "app started")
	sessionLogs.AppendCategorized("command", "winget search gh", logCategoriesForCommand([]string{"winget", "search", "gh"}))
	sessionLogs.AppendCategorized("stdout", "GitHub CLI", logCategoriesForCommand([]string{"winget", "search", "gh"}))
	app := testSessionApp()

	badRequest := httptest.NewRequest(http.MethodGet, "/api/logs/export", nil)
	badResponse := httptest.NewRecorder()
	app.serveHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized export request, got %d", badResponse.Code)
	}

	request := authenticatedRequest(app, http.MethodGet, "/api/logs/export", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected export ok, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("expected zip content type, got %q", got)
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "_windows-updater-webui-logs.zip") {
		t.Fatalf("missing zip attachment header: %q", response.Header().Get("Content-Disposition"))
	}

	files := readZipTextFiles(t, response.Body.Bytes())
	for _, file := range []string{"all.txt", "application.txt", "searches.txt", "updates.txt", "winget.txt", "store.txt", "chocolatey.txt"} {
		if _, ok := files[file]; !ok {
			t.Fatalf("missing exported log file %s in %#v", file, files)
		}
	}
	if !strings.Contains(files["application.txt"], "APP app started") {
		t.Fatalf("application export missing app entry: %q", files["application.txt"])
	}
	if !strings.Contains(files["winget.txt"], "COMMAND winget search gh") || !strings.Contains(files["searches.txt"], "STDOUT GitHub CLI") {
		t.Fatalf("manager/search exports missing command output: %#v", files)
	}
	if strings.Contains(files["updates.txt"], "winget search gh") {
		t.Fatalf("search command should not be exported as update: %q", files["updates.txt"])
	}
}

func TestLogExportFilenameUsesTimestampPrefix(t *testing.T) {
	got := logExportFilename(time.Date(2026, 6, 21, 17, 42, 9, 0, time.Local))
	want := "2026-06-21_17-42-09_windows-updater-webui-logs.zip"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFaviconServesEmbeddedAppIconWithoutToken(t *testing.T) {
	app := testSessionApp()
	request := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	response := httptest.NewRecorder()

	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected favicon ok, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Fatalf("expected image/x-icon content type, got %q", got)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Fatalf("expected no-cache favicon response, got %q", got)
	}
	if got := response.Header().Get("ETag"); !strings.Contains(got, appIconVersion()) {
		t.Fatalf("expected favicon etag with icon version, got %q", got)
	}
	if !bytes.Equal(response.Body.Bytes(), appIconICO) {
		t.Fatalf("favicon response did not match embedded app icon")
	}
}

func TestRequestShutdownRunsRegisteredCleanupsOnce(t *testing.T) {
	app := &App{}
	calls := 0
	app.addShutdownCleanup(func() {
		calls++
	})

	app.requestShutdown("test")
	app.requestShutdown("test again")

	if calls != 1 {
		t.Fatalf("expected shutdown cleanup once, got %d", calls)
	}
}

func TestBootstrapTokenCreatesHttpOnlySessionAndRedirectsClean(t *testing.T) {
	app := &App{token: "bootstrap-token", sessionToken: "session-token", listenHost: defaultHost, listenPort: 4183}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/?token=bootstrap-token", nil)
	response := httptest.NewRecorder()

	app.serveHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected bootstrap redirect, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "/" {
		t.Fatalf("expected clean redirect location, got %q", got)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie, got %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value != "session-token" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "" {
		t.Fatalf("unexpected session cookie: %#v", cookie)
	}

	tokenOnlyAPI := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/api/logs?token=bootstrap-token", nil)
	tokenOnlyResponse := httptest.NewRecorder()
	app.serveHTTP(tokenOnlyResponse, tokenOnlyAPI)
	if tokenOnlyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("query token should not authorize API requests, got %d", tokenOnlyResponse.Code)
	}

	reuse := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/?token=bootstrap-token", nil)
	reuseResponse := httptest.NewRecorder()
	app.serveHTTP(reuseResponse, reuse)
	if reuseResponse.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap token should be one-time without an existing session, got %d", reuseResponse.Code)
	}

	authenticated := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/api/logs", nil)
	addTestSessionCookie(app, authenticated)
	authenticatedResponse := httptest.NewRecorder()
	app.serveHTTP(authenticatedResponse, authenticated)
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("expected session cookie to authorize API, got %d: %s", authenticatedResponse.Code, authenticatedResponse.Body.String())
	}
}

func TestBrowserSecurityHeadersAndNoTokenInRenderedHTML(t *testing.T) {
	app := &App{token: "bootstrap-token", sessionToken: "session-token", listenHost: defaultHost, listenPort: 4183}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/", nil)
	addTestSessionCookie(app, request)
	response := httptest.NewRecorder()

	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected page ok, got %d: %s", response.Code, response.Body.String())
	}
	headers := response.Header()
	if got := headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store cache header, got %q", got)
	}
	if got := headers.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("expected no-referrer, got %q", got)
	}
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	csp := headers.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
	body := response.Body.String()
	for _, leaked := range []string{"bootstrap-token", "session-token", "data-token", `name="token"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("rendered page leaked %q", leaked)
		}
	}
}

func TestRequestBoundaryRejectsBadHostOriginAndFetchMetadata(t *testing.T) {
	app := &App{token: "bootstrap-token", sessionToken: "session-token", listenHost: defaultHost, listenPort: 4183}

	badHost := httptest.NewRequest(http.MethodGet, "http://evil.test:4183/", nil)
	addTestSessionCookie(app, badHost)
	badHostResponse := httptest.NewRecorder()
	app.serveHTTP(badHostResponse, badHost)
	if badHostResponse.Code != http.StatusMisdirectedRequest {
		t.Fatalf("expected bad host rejection, got %d", badHostResponse.Code)
	}

	badOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4183/api/scan", nil)
	addTestSessionCookie(app, badOrigin)
	badOrigin.Header.Set("Origin", "http://evil.test:4183")
	badOriginResponse := httptest.NewRecorder()
	app.serveHTTP(badOriginResponse, badOrigin)
	if badOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("expected bad origin rejection, got %d", badOriginResponse.Code)
	}

	nullOriginWithoutUIHeader := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4183/api/scan", nil)
	addTestSessionCookie(app, nullOriginWithoutUIHeader)
	nullOriginWithoutUIHeader.Header.Set("Origin", "null")
	nullOriginWithoutUIHeaderResponse := httptest.NewRecorder()
	app.serveHTTP(nullOriginWithoutUIHeaderResponse, nullOriginWithoutUIHeader)
	if nullOriginWithoutUIHeaderResponse.Code != http.StatusForbidden {
		t.Fatalf("expected null origin without UI header rejection, got %d", nullOriginWithoutUIHeaderResponse.Code)
	}

	nullOriginUIRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4183/shutdown", nil)
	addTestSessionCookie(app, nullOriginUIRequest)
	nullOriginUIRequest.Header.Set("Origin", "null")
	nullOriginUIRequest.Header.Set(trustedUIRequestHeader, "1")
	nullOriginUIRequestResponse := httptest.NewRecorder()
	app.serveHTTP(nullOriginUIRequestResponse, nullOriginUIRequest)
	if nullOriginUIRequestResponse.Code != http.StatusOK {
		t.Fatalf("expected trusted UI null origin request to pass boundary, got %d: %s", nullOriginUIRequestResponse.Code, nullOriginUIRequestResponse.Body.String())
	}

	badOriginWithUIHeader := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4183/api/scan", nil)
	addTestSessionCookie(app, badOriginWithUIHeader)
	badOriginWithUIHeader.Header.Set("Origin", "http://evil.test:4183")
	badOriginWithUIHeader.Header.Set(trustedUIRequestHeader, "1")
	badOriginWithUIHeaderResponse := httptest.NewRecorder()
	app.serveHTTP(badOriginWithUIHeaderResponse, badOriginWithUIHeader)
	if badOriginWithUIHeaderResponse.Code != http.StatusForbidden {
		t.Fatalf("expected bad origin with UI header rejection, got %d", badOriginWithUIHeaderResponse.Code)
	}

	badFetch := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4183/api/scan", nil)
	addTestSessionCookie(app, badFetch)
	badFetch.Header.Set("Sec-Fetch-Site", "cross-site")
	badFetchResponse := httptest.NewRecorder()
	app.serveHTTP(badFetchResponse, badFetch)
	if badFetchResponse.Code != http.StatusForbidden {
		t.Fatalf("expected bad fetch metadata rejection, got %d", badFetchResponse.Code)
	}

	shutdownGet := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4183/shutdown", nil)
	addTestSessionCookie(app, shutdownGet)
	shutdownGetResponse := httptest.NewRecorder()
	app.serveHTTP(shutdownGetResponse, shutdownGet)
	if shutdownGetResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected shutdown GET to be rejected, got %d", shutdownGetResponse.Code)
	}
}

func TestShutdownRouteStopsServer(t *testing.T) {
	app := testSessionApp()
	cleanupDone := make(chan struct{})
	app.addShutdownCleanup(func() {
		close(cleanupDone)
	})
	server := httptest.NewServer(http.HandlerFunc(app.serveHTTP))
	app.server = server.Config
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	addTestSessionCookie(app, request)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected shutdown response ok, got %d", response.StatusCode)
	}
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown route did not run registered cleanup")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		check, err := server.Client().Get(server.URL + "/")
		if err != nil {
			return
		}
		_ = check.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server still responded after shutdown")
}

func TestAPIUpdateIgnoresCanceledRequestContext(t *testing.T) {
	oldRunner := updatePackageRunner
	oldGetter := inventoryGetter
	var observedErr error
	updatePackageRunner = func(ctx context.Context, pkg Package) CommandResult {
		observedErr = ctx.Err()
		return CommandResult{OK: true, Command: "update " + pkg.ID}
	}
	inventoryGetter = func(ctx context.Context) Inventory {
		return Inventory{}
	}
	defer func() {
		updatePackageRunner = oldRunner
		inventoryGetter = oldGetter
	}()

	app := &App{
		sessionToken: "test-session",
		inventory: Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:             "winget:Git.Git",
			Manager:         managerWinget,
			ID:              "Git.Git",
			Name:            "Git",
			UpdateAvailable: true,
			UpdateSupported: true,
		}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/update", strings.NewReader("manager=winget&package_id=Git.Git")).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addTestSessionCookie(app, request)
	response := httptest.NewRecorder()

	app.serveHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected update request to start a background job despite canceled request context, got %d: %s", response.Code, response.Body.String())
	}
	var status OperationJobStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.JobID == "" {
		t.Fatalf("expected job id in response: %#v", status)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status, ok := app.operationJobStatus(status.JobID)
		if ok && !status.Running && status.State != jobStateQueued {
			if observedErr != nil {
				t.Fatalf("update command used canceled request context: %v", observedErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background update job did not finish")
}

func TestAPIRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		content    string
		wantResult bool
		wantText   string
	}{
		{"update form", "/api/update", "manager=invalid&package_id=Git.Git", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"install form", "/api/install", "manager=invalid&package_id=Git.Git", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"manager install form", "/api/managers/install", "manager=invalid", "application/x-www-form-urlencoded", true, managerValidationMessage},
		{"update all form", "/api/update-all", "package_key=not-a-valid-key", "application/x-www-form-urlencoded", false, "package key must be manager:id"},
		{"update json", "/api/update", `{"manager":"invalid","package_id":"Git.Git"}`, "application/json", true, managerValidationMessage},
		{"install json", "/api/install", `{"manager":"winget","package_id":"bad&id"}`, "application/json", true, "winget package id or query contains unsupported characters"},
		{"manager install json", "/api/managers/install", `{"manager":"invalid"}`, "application/json", true, managerValidationMessage},
		{"update all json", "/api/update-all", `{"package_keys":["not-a-valid-key"]}`, "application/json", false, "package key must be manager:id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testSessionApp()
			request := authenticatedRequest(app, http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.content)
			response := httptest.NewRecorder()

			app.serveHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected bad request, got %d: %s", response.Code, response.Body.String())
			}

			var decoded struct {
				Result         *CommandResult `json:"result"`
				Results        []UpdateResult `json:"results"`
				RefreshStarted bool           `json:"refresh_started"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.RefreshStarted {
				t.Fatal("invalid request should not start an inventory refresh")
			}
			if tc.wantResult {
				if decoded.Result == nil || decoded.Result.Code != 2 || !strings.Contains(decoded.Result.Stderr, tc.wantText) {
					t.Fatalf("unexpected validation result: %#v", decoded.Result)
				}
				return
			}
			if len(decoded.Results) != 1 || decoded.Results[0].Result.Code != 2 || !strings.Contains(decoded.Results[0].Result.Stderr, tc.wantText) {
				t.Fatalf("unexpected update-all validation result: %#v", decoded.Results)
			}
		})
	}
}

func TestSettingsJSONRequestParsers(t *testing.T) {
	updateRequest := httptest.NewRequest(http.MethodPost, "/api/update", strings.NewReader(`{"manager":"winget","package_id":"Vendor.Unknown","allow_unknown_version":true,"allow_pinned":true}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	manager, packageID, updateOptions, invalidUpdate := parsePackageUpdateAction(updateRequest)
	if invalidUpdate != nil || manager != managerWinget || packageID != "Vendor.Unknown" || !updateOptions.AllowUnknownVersion || !updateOptions.AllowPinned {
		t.Fatalf("unexpected update JSON parse: manager=%q packageID=%q options=%#v invalid=%#v", manager, packageID, updateOptions, invalidUpdate)
	}

	updateAllRequest := httptest.NewRequest(http.MethodPost, "/api/update-all", strings.NewReader(`{"package_keys":["winget:Vendor.Unknown"],"allow_unknown_version":true,"allow_pinned":true}`))
	updateAllRequest.Header.Set("Content-Type", "application/json")
	updateAllKeys, updateAllOptions, invalidUpdateAll := parseUpdateAllRequest(updateAllRequest)
	if invalidUpdateAll != nil || len(updateAllKeys) != 1 || updateAllKeys[0] != "winget:Vendor.Unknown" || !updateAllOptions.AllowUnknownVersion || !updateAllOptions.AllowPinned {
		t.Fatalf("unexpected update-all JSON parse: keys=%#v options=%#v invalid=%#v", updateAllKeys, updateAllOptions, invalidUpdateAll)
	}

	startupRequest := httptest.NewRequest(http.MethodPost, "/api/settings/startup", strings.NewReader(`{"enabled":true}`))
	startupRequest.Header.Set("Content-Type", "application/json")
	enabled, invalidStartup := parseStartupRequest(startupRequest)
	if invalidStartup != nil || !enabled {
		t.Fatalf("expected enabled startup JSON parse, enabled=%t invalid=%#v", enabled, invalidStartup)
	}

	autoRequest := httptest.NewRequest(http.MethodPost, "/api/settings/auto-update", strings.NewReader(`{"global":true,"package_keys":["winget:Git.Git"],"package_enabled":false}`))
	autoRequest.Header.Set("Content-Type", "application/json")
	global, keys, packageEnabled, invalidAuto := parseAutoUpdateRequest(autoRequest)
	if invalidAuto != nil || global == nil || !*global || packageEnabled == nil || *packageEnabled || len(keys) != 1 || keys[0] != "winget:Git.Git" {
		t.Fatalf("unexpected auto-update JSON parse: global=%v keys=%#v packageEnabled=%v invalid=%#v", global, keys, packageEnabled, invalidAuto)
	}

	themeRequest := httptest.NewRequest(http.MethodPost, "/api/settings/theme", strings.NewReader(`{"theme":"light"}`))
	themeRequest.Header.Set("Content-Type", "application/json")
	theme, err := parseThemeRequest(themeRequest)
	if err != nil || theme != "light" {
		t.Fatalf("unexpected theme JSON parse: theme=%q err=%v", theme, err)
	}

	appUpdatePromptRequest := httptest.NewRequest(http.MethodPost, "/api/settings/app-update-prompt", strings.NewReader(`{"version":"1.2.3"}`))
	appUpdatePromptRequest.Header.Set("Content-Type", "application/json")
	version, err := parseAppUpdatePromptRequest(appUpdatePromptRequest)
	if err != nil || version != "1.2.3" {
		t.Fatalf("unexpected app update prompt JSON parse: version=%q err=%v", version, err)
	}
}

func TestSettingsAPIsRejectMalformedJSONBeforeSideEffects(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       string
		wantResult bool
	}{
		{"startup", "/api/settings/startup", `{"enabled":`, true},
		{"auto update", "/api/settings/auto-update", `{"package_keys":{}}`, true},
		{"theme", "/api/settings/theme", `{"theme":`, false},
		{"app update prompt", "/api/settings/app-update-prompt", `{"version":`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testSessionApp()
			request := authenticatedRequest(app, http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			app.serveHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected bad request, got %d: %s", response.Code, response.Body.String())
			}
			if tc.wantResult {
				var decoded commandAPIResponse
				if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.Result == nil || decoded.Result.Code != 2 || !strings.Contains(decoded.Result.Stderr, "invalid JSON body") {
					t.Fatalf("expected validation command result, got %#v", decoded.Result)
				}
				return
			}
			var decoded apiErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(decoded.Error, "invalid JSON body") {
				t.Fatalf("expected invalid JSON error, got %#v", decoded)
			}
		})
	}
}

func TestAppUpdatePromptSettingsEndpointPersistsDismissedVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	original := defaultState()
	original.Theme = "light"
	original.AutoUpdatePackages["winget:Git.Git"] = true
	if err := saveState(original); err != nil {
		t.Fatal(err)
	}

	app := testSessionApp()
	request := authenticatedRequest(app, http.MethodPost, "/api/settings/app-update-prompt", strings.NewReader(`{"version":"1.2.3"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected ok, got %d: %s", response.Code, response.Body.String())
	}
	var decoded commandAPIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Settings == nil || decoded.Settings.AppUpdatePromptDismissedVersion != "1.2.3" {
		t.Fatalf("settings response did not include dismissed version: %#v", decoded.Settings)
	}
	loaded := loadState()
	if loaded.AppUpdatePromptDismissedVersion != "1.2.3" || loaded.Theme != "light" || !loaded.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("endpoint did not preserve unrelated state: %#v", loaded)
	}
}
