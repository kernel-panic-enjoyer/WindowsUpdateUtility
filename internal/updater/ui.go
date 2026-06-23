package updater

import "html/template"

type PageData struct {
	Admin        bool
	StateDir     string
	Theme        string
	IconVersion  string
	AssetVersion string
}

var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	"logTabs": func() []LogCategorySpec { return logCategorySpecs },
}).Parse(`<!doctype html>
<html lang="en" data-theme="{{.Theme}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico?v={{.IconVersion}}" type="image/x-icon" sizes="any">
  <link rel="shortcut icon" href="/favicon.ico?v={{.IconVersion}}" type="image/x-icon">
  <link rel="stylesheet" href="/assets/ui.css?v={{.AssetVersion}}">
  <title>Windows Updater WebUI</title>
</head>
<body>
  <header class="app-header">
    <div class="brand-block">
      <span class="app-mark" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M4 5.8 12 2l8 3.8v6.1c0 4.9-3.3 8.4-8 10.1-4.7-1.7-8-5.2-8-10.1V5.8Z"/><path d="m8 12.4 2.4 2.4L16.5 8.8"/></svg></span>
      <div>
        <h1>Windows Updater WebUI</h1>
        <p>{{if .Admin}}Running elevated{{else}}Not elevated{{end}} - Local dashboard - State: {{.StateDir}}</p>
      </div>
    </div>
    <div class="header-actions">
      <button id="theme-toggle" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3a6 6 0 1 0 6 6c0 5-4 9-9 9a6 6 0 0 0 3-15Z"/></svg></span><span>Theme</span></button>
      <button id="scan-button" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M4 7V5a1 1 0 0 1 1-1h2"/><path d="M17 4h2a1 1 0 0 1 1 1v2"/><path d="M20 17v2a1 1 0 0 1-1 1h-2"/><path d="M7 20H5a1 1 0 0 1-1-1v-2"/><path d="M7 12h10"/></svg></span><span>Scan Apps</span></button>
      <form id="shutdown-form" method="post" action="/shutdown"><button id="shutdown-button" class="danger" type="submit"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3v8"/><path d="M7.1 6.9a8 8 0 1 0 9.8 0"/></svg></span><span>Stop</span></button></form>
    </div>
  </header>
  <main>
    <section id="notice" class="notice hidden"></section>
    <section id="toast-region" class="toast-region" aria-live="polite" aria-atomic="false"></section>

    <section id="store-status-modal" class="modal hidden" role="dialog" aria-modal="true" aria-labelledby="store-status-modal-title">
      <div class="modal-backdrop" data-store-status-close></div>
      <div class="modal-panel store-health-modal" role="document">
        <div class="modal-header">
          <div><span class="panel-kicker">Microsoft Store</span><h2 id="store-status-modal-title">Store status details</h2></div>
          <button id="store-status-close" class="ghost" type="button" data-store-status-close><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M6 6l12 12"/><path d="M18 6 6 18"/></svg></span><span>Close</span></button>
        </div>
        <div id="store-scan-health-summary" class="store-health-summary-text"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking Store status...</span></span></div>
        <div class="button-row store-health-actions"><button id="store-diagnostics-export-button" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg></span><span>Export Store Diagnostics</span></button><button id="store-rescan-button" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 0 1-15.5 6.2"/><path d="M3 12a9 9 0 0 1 15.5-6.2"/><path d="M3 18v-6h6"/><path d="M21 6v6h-6"/></svg></span><span>Rescan Store Status</span></button></div>
        <div id="store-scan-health-body" class="store-health-body" aria-live="polite"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking Store coverage...</span></span></div>
      </div>
    </section>

    <section id="package-diagnostics-modal" class="modal hidden" role="dialog" aria-modal="true" aria-labelledby="package-diagnostics-modal-title">
      <div class="modal-backdrop" data-package-diagnostics-close></div>
      <div class="modal-panel package-diagnostics-modal" role="document">
        <div class="modal-header">
          <div><span class="panel-kicker">Update diagnostics</span><h2 id="package-diagnostics-modal-title">Package diagnostics</h2></div>
          <button id="package-diagnostics-close" class="ghost" type="button" data-package-diagnostics-close><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M6 6l12 12"/><path d="M18 6 6 18"/></svg></span><span>Close</span></button>
        </div>
        <div id="package-diagnostics-body" class="package-diagnostics-body" aria-live="polite"></div>
      </div>
    </section>

    <section class="dashboard-hero">
      <div class="hero-copy">
        <div class="hero-topline"><span class="eyebrow">Updates first</span><span id="log-connection-status" class="badge connection-badge" role="status" aria-live="polite">Connecting</span></div>
        <h2>Keep Windows apps current without leaving the browser.</h2>
        <p class="muted">Winget, Chocolatey, and Store inventory are merged into one local control surface.</p>
      </div>
      <form id="search-form" class="search hero-search" method="get" action="/">
        <label class="search-field" for="search-input"><span class="sr-only">Search and install packages</span><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="m16.5 16.5 4 4"/></svg></span><input id="search-input" name="q" type="search" placeholder="Search and install packages" autocomplete="off"></label>
        <button type="submit"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12h14"/><path d="m13 6 6 6-6 6"/></svg></span><span>Search</span></button>
      </form>
    </section>

    <section id="dashboard-summary" class="summary-grid" aria-label="Dashboard summary">
      <article class="summary-card accent-update"><span class="summary-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-2.6-6.4"/><path d="M21 4v6h-6"/></svg></span><div><p>Updates available</p><strong id="summary-updates">-</strong><span id="summary-updates-detail" class="muted"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking packages</span></span></span></div></article>
      <article class="summary-card"><span class="summary-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M4 7h16"/><path d="M6 7v12h12V7"/><path d="M9 11h6"/></svg></span><div><p>Managed packages</p><strong id="summary-packages">-</strong><span id="summary-packages-detail" class="muted">Inventory loading</span></div></article>
      <article class="summary-card"><span class="summary-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M7 8h10"/><path d="M7 12h10"/><path d="M7 16h6"/><rect x="4" y="4" width="16" height="16" rx="3"/></svg></span><div><p>Package managers</p><strong id="summary-managers">-</strong><span id="summary-managers-detail" class="muted"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking tools</span></span></span></div></article>
      <article class="summary-card"><span class="summary-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 6v6l4 2"/><circle cx="12" cy="12" r="9"/></svg></span><div><p>Automation</p><strong id="summary-automation">-</strong><span id="summary-automation-detail" class="muted"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Loading tasks</span></span></span></div></article>
    </section>

    <section class="control-grid">
      <div class="panel manager-panel"><div class="section-heading"><h2>Package Managers</h2><span class="panel-kicker">Availability</span></div><div id="manager-list"><p class="muted"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking package managers...</span></span></p></div></div>
      <div class="panel automation-panel"><div class="section-heading"><h2>Automation</h2><span class="panel-kicker">Scheduled tasks</span></div><div class="stack">
        <button id="startup-toggle" type="button" disabled><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking startup...</span></span></button>
        <button id="auto-global-toggle" type="button" disabled><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Checking auto-update...</span></span></button>
        <div class="button-row"><button id="auto-all" type="button" disabled>Auto All</button><button id="auto-none" type="button" disabled>Auto None</button></div>
        <p id="automation-status" class="muted"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Loading task status...</span></span></p>
      </div></div>
    </section>

    <section id="scan-panel" class="panel table-panel hidden">
      <div class="section-heading"><h2>Scan Results</h2><span class="panel-kicker">New since last scan</span></div>
      <p id="scan-summary" class="muted"></p>
      <pre id="scan-errors" class="hidden"></pre>
      <div class="table-wrap"><table><thead><tr><th>Source</th><th>Name</th><th>Version</th><th>Publisher</th><th>Location</th></tr></thead><tbody id="scan-body"></tbody></table></div>
    </section>

    <section id="install-progress" class="progress-panel install-progress-panel hidden" aria-busy="false"><div class="progress-header"><div><span class="panel-kicker">Install job</span><div id="install-progress-status" class="progress-title" role="status" aria-live="polite" aria-atomic="true"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Installing package...</span></span></div></div></div><div class="progress-bar" role="progressbar" aria-labelledby="install-progress-status" aria-valuetext="In progress"><span></span></div></section>

    <section id="search-results-panel" class="panel table-panel hidden">
      <div class="section-heading"><div><span class="panel-kicker">Installable packages</span><h2>Search Results</h2></div><div class="button-row"><button id="search-prev" class="ghost" type="button" disabled>Previous</button><span id="search-page-status" class="muted">Page 1</span><button id="search-next" class="ghost" type="button" disabled>Next</button></div></div>
      <div id="search-provenance" class="search-provenance hidden"></div>
      <div class="table-wrap"><table><thead><tr><th>Name</th><th>Source</th><th>Manager / Backend</th><th>Exact ID</th><th>Match</th><th>Version</th><th>Action</th></tr></thead><tbody id="search-results-body"></tbody></table></div>
    </section>

    <section id="update-progress" class="progress-panel hidden" aria-busy="false"><div class="progress-header"><div><span class="panel-kicker">Update job</span><div id="update-progress-status" class="progress-title" role="status" aria-live="polite" aria-atomic="true"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Updating packages...</span></span></div></div><button id="cancel-updates-button" class="secondary hidden" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M6 6l12 12"/><path d="M18 6 6 18"/></svg></span><span>Cancel Updates</span></button></div><div class="progress-bar" role="progressbar" aria-labelledby="update-progress-status" aria-valuetext="In progress"><span></span></div></section>

    <section id="update-preflight-panel" class="panel table-panel hidden">
      <div class="section-heading"><div><span class="panel-kicker">Confirm bulk update</span><h2>Update Preflight</h2></div><div class="button-row"><button id="confirm-update-job" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12h14"/><path d="m13 6 6 6-6 6"/></svg></span><span>Confirm Update</span></button><button id="cancel-update-preflight" class="ghost" type="button">Cancel</button></div></div>
      <div id="update-preflight-summary" class="summary-line"></div>
      <div id="update-preflight-overrides" class="summary-line muted"></div>
      <div class="table-wrap"><table><thead><tr><th>Package</th><th>Source</th><th>Installed</th><th>Target</th><th>Manager / Backend / Notes</th></tr></thead><tbody id="update-preflight-body"></tbody></table></div>
      <div id="update-preflight-excluded" class="preflight-excluded"></div>
    </section>

    <section id="update-results-panel" class="panel table-panel hidden">
      <div class="section-heading"><div><span class="panel-kicker">Structured record</span><h2>Update Results</h2></div><div class="button-row"><button id="retry-failed-updates" type="button" disabled><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-2.6-6.4"/><path d="M21 4v6h-6"/></svg></span><span>Retry Failed</span></button></div></div>
      <div id="update-results-summary" class="summary-line"></div>
      <div class="table-wrap"><table><thead><tr><th>Status</th><th>Package</th><th>Source</th><th>Installed</th><th>Target</th><th>Result</th></tr></thead><tbody id="update-results-body"></tbody></table></div>
    </section>

	<section id="updates-section" class="panel table-panel priority-panel">
	  <div class="section-heading"><div><span class="panel-kicker">Primary queue</span><h2>Updates Available</h2></div><div class="button-row"><label class="sr-only" for="updates-manager-filter">Filter updates by package manager</label><select id="updates-manager-filter" class="table-filter" aria-label="Filter updates by package manager"><option value="all">All managers</option><option value="winget">winget</option><option value="store">Microsoft Store</option><option value="choco">Chocolatey</option></select><button id="updates-prev" class="ghost" type="button" disabled>Previous</button><span id="updates-page-status" class="muted">Page 1</span><button id="updates-next" class="ghost" type="button" disabled>Next</button><button id="refresh-packages" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M21 12a9 9 0 0 1-15.5 6.2"/><path d="M3 12a9 9 0 0 1 15.5-6.2"/><path d="M3 18v-6h6"/><path d="M21 6v6h-6"/></svg></span><span>Refresh</span></button><form class="update-all-form" method="post" action="/api/update-all"><button id="update-all-button" type="submit"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg></span><span>Update All</span></button></form></div></div>
	  <div class="update-options" aria-label="Global update options"><span class="muted">Global update options</span><label class="check-control"><input id="update-allow-unknown" type="checkbox" name="allow_unknown_version" value="true"> Allow unknown version updates</label><label class="check-control"><input id="update-allow-pinned" type="checkbox" name="allow_pinned" value="true"> Allow pinned updates</label></div>
	  <form id="update-selected-form" method="post" action="/api/update-all"></form>
	  <div class="table-wrap"><table><thead><tr><th>Select</th><th>Name</th><th>Manager</th><th>Installed</th><th>Available</th><th>Auto</th><th>Action</th></tr></thead><tbody id="updates-body"><tr><td colspan="7"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Loading packages...</span></span></td></tr></tbody></table></div>
	  <p id="updates-store-loading" class="store-loading-note hidden" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span><span>Microsoft Store is still checking for updates...</span></p>
	  <div class="table-footer"><button id="update-selected-button" form="update-selected-form" type="submit"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12h14"/><path d="m13 6 6 6-6 6"/></svg></span><span>Update Selected</span></button></div>
	</section>

	<section id="installed-section" class="panel table-panel">
	  <div class="section-heading"><div><span class="panel-kicker">Inventory</span><h2>Installed Packages</h2></div><div class="button-row"><label class="sr-only" for="installed-search">Filter installed packages</label><input id="installed-search" class="table-search" type="search" placeholder="Filter installed packages" autocomplete="off"><label class="sr-only" for="installed-manager-filter">Filter installed packages by package manager</label><select id="installed-manager-filter" class="table-filter" aria-label="Filter installed packages by package manager"><option value="all">All managers</option><option value="winget">winget</option><option value="store">Microsoft Store</option><option value="choco">Chocolatey</option></select><button id="installed-prev" class="ghost" type="button">Previous</button><span id="installed-page-status" class="muted">Page 1</span><button id="installed-next" class="ghost" type="button">Next</button></div></div>
	  <div class="table-wrap"><table><thead><tr><th>Name</th><th>Manager</th><th>Installed</th><th>Available</th><th>Status</th><th>Auto</th><th>Action</th></tr></thead><tbody id="packages-body"><tr><td colspan="7"><span class="loading-text"><span class="spinner" aria-hidden="true"></span><span>Loading packages...</span></span></td></tr></tbody></table></div>
	  <p id="installed-store-loading" class="store-loading-note hidden" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span><span>Microsoft Store is still checking for updates...</span></p>
	</section>

    <section id="session-log-panel" class="panel log-panel">
      <div class="section-heading"><div><span class="panel-kicker">Command output</span><h2>Session Log</h2></div><div class="button-row"><label class="sr-only" for="log-search">Search active log</label><input id="log-search" class="table-search" type="search" placeholder="Search active log" autocomplete="off"><label class="check-control"><input id="log-autoscroll" type="checkbox" checked> Auto Scroll</label><button id="copy-log-view" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></span><span>Copy Log</span></button><button id="export-log-view" class="ghost" type="button"><span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg></span><span>Export Logs</span></button><button id="clear-log-view" class="ghost" type="button">Clear View</button></div></div>
      <div class="log-tabs" role="tablist" aria-label="Session log categories" aria-orientation="horizontal">
        {{range $index, $tab := logTabs}}<button id="log-tab-{{$tab.Category}}" class="log-tab{{if eq $index 0}} active{{end}}" type="button" role="tab" aria-selected="{{if eq $index 0}}true{{else}}false{{end}}" aria-controls="session-log" tabindex="{{if eq $index 0}}0{{else}}-1{{end}}" data-log-category="{{$tab.Category}}">{{$tab.Label}}</button>{{end}}
      </div>
      <pre id="session-log" class="session-log" role="tabpanel" tabindex="0" aria-labelledby="log-tab-all"></pre>
    </section>
  </main>
  <script src="/assets/ui.js?v={{.AssetVersion}}" defer></script>
</body>
</html>`))
