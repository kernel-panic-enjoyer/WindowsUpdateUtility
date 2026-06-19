package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderedHTMLContainsAsyncUpdateHooks(t *testing.T) {
	var output bytes.Buffer
	data := PageData{
		Token: "test-token",
		Theme: "dark",
	}
	if err := pageTemplate.Execute(&output, data); err != nil {
		t.Fatal(err)
	}
	rendered := output.String()
	for _, expected := range []string{
		`class="dashboard-hero"`,
		`rel="icon" href="/favicon.ico?v=`,
		`rel="shortcut icon" href="/favicon.ico?v=`,
		`id="dashboard-summary"`,
		`id="summary-updates"`,
		`id="summary-packages"`,
		`id="summary-managers"`,
		`id="summary-automation"`,
		`id="toast-region"`,
		`id="updates-section"`,
		`id="install-progress"`,
		`id="update-progress"`,
		`id="updates-prev"`,
		`id="updates-page-status"`,
		`id="updates-next"`,
		`class="update-all-form"`,
		`id="search-form"`,
		`id="search-prev"`,
		`id="search-page-status"`,
		`id="search-next"`,
		`action="/api/install"`,
		`action="/api/managers/install"`,
		`rowUpdateQueue`,
		`rowUpdateActive`,
		`enqueueUpdateRequest`,
		`processRowUpdateQueue`,
		`rowUpdateState`,
		`rowUpdateQueueActive`,
		`queuedRowUpdateKeys`,
		`Queued`,
		`postCommandPayload`,
		`payload.result && !payload.result.ok`,
		`Application scan completed with errors`,
		`Could not update startup setting`,
		`Could not update auto-update settings`,
		`allow_unknown_version`,
		`allow_pinned`,
		`id="update-allow-unknown"`,
		`id="update-allow-pinned"`,
		`Global update options`,
		`appendGlobalUpdateOptions`,
		`allowUnknownVersionUpdates`,
		`allowPinnedUpdates`,
		`packageBulkUpdateable`,
		`updatePageSize = 10`,
		`showToast`,
		`Math.max(duration || 10000, 10000)`,
		`pauseToastTimers`,
		`resumeToastTimers`,
		`document.hidden`,
		`visibilitychange`,
		`toast-close`,
		`toast-region`,
		`bottom:18px`,
		`startUpdateJob`,
		`pollUpdateJobStatus`,
		`checkActiveUpdateJob`,
		`api("/api/update-all/status"`,
		`postForm("/api/update-all/cancel"`,
		`id="cancel-updates-button"`,
		`status.package_keys`,
		`applyUpdateJobPackageKeys`,
		`response.status === 409 && status.running`,
		`active && !status.cancel_requested`,
		`installFromForm`,
		`renderSearchTable`,
		`searchPageSize = 10`,
		`installManagerFromForm`,
		`refreshStatusAfterManagerInstall`,
		`setInstallProgress`,
		`install-progress-panel`,
		`refreshPackagesAfterUpdate`,
		`id="session-log-panel"`,
		`class="log-tab active"`,
		`data-log-category="all"`,
		`data-log-category="application"`,
		`data-log-category="searches"`,
		`data-log-category="updates"`,
		`data-log-category="winget"`,
		`data-log-category="store"`,
		`data-log-category="chocolatey"`,
		`id="log-search"`,
		`id="copy-log-view"`,
		`id="export-log-view"`,
		`id="clear-log-view"`,
		`id="log-autoscroll"`,
		`activeLogCategory`,
		`logSearchQuery`,
		`filteredLogLines`,
		`setActiveLogCategory`,
		`clearCurrentLogView`,
		`exportLogs`,
		`copyLogView`,
		`navigator.clipboard.writeText`,
		`document.execCommand("copy")`,
		`api("/api/logs"`,
		`api("/api/logs/export"`,
		`id="updates-body"`,
		`id="installed-search"`,
		`id="installed-page-status"`,
		`packageMatchesInstalledSearch`,
		`packageAvailableCell`,
		`Pending in Microsoft Store`,
		`managersRendered`,
		`renderUpdatesTable`,
		`renderInstalledTable`,
		`installedAction`,
		`updating-current`,
		`managerAvailabilityText`,
		`managerDisplayDetails`,
		`renderDashboardSummary`,
		`managerLabel`,
		`backendLabel`,
		`function icon(name)`,
		`function spinner()`,
		`function loadingText(message)`,
		`function loadingTableRow(colspan, message)`,
		`class="spinner"`,
		`class="loading-text"`,
		`conic-gradient`,
		`will-change:transform`,
		`@keyframes spin`,
		`prefers-reduced-motion:reduce`,
		`class="button-icon"`,
		`class="summary-card`,
		`compactNoticeText`,
		`truncateNoticeText`,
		`firstMeaningfulOutputLine`,
		`See Session Log for full output.`,
		`max-height:96px`,
		`manager.inventory_available`,
		`pkg.action_backend`,
		`Inventory only`,
		`Store apps detected via`,
		`store-cli-resolved`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered page did not contain %q", expected)
		}
	}
	for _, unexpected := range []string{
		`Inventory: `,
		`Actions: `,
		`unknown-confirm`,
		`pinned-confirm`,
		`Update Anyway`,
		`Available Usage: store`,
		`Usage: store <command>`,
		`? "Current" : "-"`,
		`action="/install"`,
		`action="/manager/install"`,
		`action="/update"`,
		`action="/update-all"`,
		`class="status-grid"`,
		`{{if .CommandResult}}`,
		`{{if .ActionResults}}`,
		`{{if .Scan}}`,
		`showNotice("Refreshing package status...", true)`,
		`showNotice(rowUpdateProgressMessage(), true)`,
		`showNotice(message, active || !!(status && status.refresh_started))`,
		`showNotice(message || "Starting updates...", true)`,
	} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("rendered page should not contain %q", unexpected)
		}
	}
	progressIndex := strings.Index(rendered, `id="update-progress"`)
	updatesIndex := strings.Index(rendered, `Updates Available`)
	if progressIndex < 0 || updatesIndex < 0 || progressIndex > updatesIndex {
		t.Fatalf("expected update progress banner before updates table, progress=%d updates=%d", progressIndex, updatesIndex)
	}
	installProgressIndex := strings.Index(rendered, `id="install-progress"`)
	searchResultsIndex := strings.Index(rendered, `id="search-results-panel"`)
	if installProgressIndex < 0 || searchResultsIndex < 0 || installProgressIndex > searchResultsIndex {
		t.Fatalf("expected install progress banner before search results, progress=%d search=%d", installProgressIndex, searchResultsIndex)
	}
}
