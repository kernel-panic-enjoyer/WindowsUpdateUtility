package main

import "html/template"

type PageData struct {
	Token    string
	Admin    bool
	StateDir string
	Theme    string
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en" data-theme="{{.Theme}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Windows Updater WebUI</title>
  <script>!function(){try{var t=localStorage.getItem("windows-updater-theme");if(t==="light"||t==="dark"){document.documentElement.dataset.theme=t}}catch(e){}}();</script>
  <style>` + pageCSS + `</style>
</head>
<body data-token="{{.Token}}">
  <header class="app-header">
    <div>
      <h1>Windows Updater WebUI</h1>
      <p>{{if .Admin}}Running elevated{{else}}Not elevated{{end}} - State: {{.StateDir}}</p>
    </div>
    <div class="header-actions">
      <button id="theme-toggle" type="button">Theme</button>
      <button id="scan-button" type="button">Scan Apps</button>
      <form method="post" action="/shutdown"><input type="hidden" name="token" value="{{.Token}}"><button class="secondary" type="submit">Stop</button></form>
    </div>
  </header>
  <main>
    <section id="notice" class="notice hidden"></section>

    <section class="status-grid">
      <div class="panel"><h2>Package Managers</h2><div id="manager-list"><p class="muted">Checking package managers...</p></div></div>
      <div class="panel"><h2>Automation</h2><div class="stack">
        <button id="startup-toggle" type="button" disabled>Checking startup...</button>
        <button id="auto-global-toggle" type="button" disabled>Checking auto-update...</button>
        <div class="button-row"><button id="auto-all" type="button" disabled>Auto All</button><button id="auto-none" type="button" disabled>Auto None</button></div>
        <p id="automation-status" class="muted">Loading task status...</p>
      </div></div>
      <div class="panel"><h2>Search</h2><form id="search-form" class="search" method="get" action="/"><input type="hidden" name="token" value="{{.Token}}"><input id="search-input" name="q" placeholder="Search packages"><button type="submit">Search</button></form></div>
    </section>

    <section id="scan-panel" class="panel hidden">
      <h2>Scan Results</h2>
      <p id="scan-summary" class="muted"></p>
      <pre id="scan-errors" class="hidden"></pre>
      <table><thead><tr><th>Source</th><th>Name</th><th>Version</th><th>Publisher</th><th>Location</th></tr></thead><tbody id="scan-body"></tbody></table>
    </section>

    <section id="search-results-panel" class="panel hidden">
      <h2>Search Results</h2>
      <table><thead><tr><th>Name</th><th>Manager</th><th>ID</th><th>Version</th><th>Action</th></tr></thead><tbody id="search-results-body"></tbody></table>
    </section>

    <section id="update-progress" class="progress-panel hidden"><div class="progress-header"><div class="progress-title">Updating packages...</div><button id="cancel-updates-button" class="secondary hidden" type="button">Cancel Updates</button></div><div class="progress-bar"><span></span></div></section>

	<section class="panel">
	  <div class="section-heading"><h2>Updates Available</h2><div class="button-row"><button id="refresh-packages" type="button">Refresh</button><form class="update-all-form" method="post" action="/api/update-all"><input type="hidden" name="token" value="{{.Token}}"><button id="update-all-button" type="submit">Update All</button></form></div></div>
	  <form id="update-selected-form" method="post" action="/api/update-all"><input type="hidden" name="token" value="{{.Token}}"></form>
	  <table><thead><tr><th></th><th>Name</th><th>Manager</th><th>Installed</th><th>Available</th><th>Auto</th><th>Action</th></tr></thead><tbody id="updates-body"><tr><td colspan="7">Loading packages...</td></tr></tbody></table>
	  <button id="update-selected-button" form="update-selected-form" type="submit">Update Selected</button>
	</section>

	<section class="panel">
	  <div class="section-heading"><h2>Installed Packages</h2><div class="button-row"><input id="installed-search" class="table-search" type="search" placeholder="Filter installed packages"><button id="installed-prev" type="button">Previous</button><span id="installed-page-status" class="muted">Page 1</span><button id="installed-next" type="button">Next</button></div></div>
	  <table><thead><tr><th>Name</th><th>Manager</th><th>Installed</th><th>Available</th><th>Status</th><th>Auto</th><th>Action</th></tr></thead><tbody id="packages-body"><tr><td colspan="7">Loading packages...</td></tr></tbody></table>
	</section>

    <section id="session-log-panel" class="panel log-panel">
      <div class="section-heading"><h2>Session Log</h2><div class="button-row"><label class="check-control"><input id="log-autoscroll" type="checkbox" checked> Auto Scroll</label><button id="copy-log-view" type="button">Copy Log</button><button id="clear-log-view" type="button">Clear View</button></div></div>
      <pre id="session-log" class="session-log"></pre>
    </section>
  </main>
  <script>` + pageJS + `</script>
</body>
</html>`))

const pageJS = `
(function(){
  var token = document.body.dataset.token || "";
  var packages = [];
  var updateBusy = false;
  var installedPage = 1;
  var installedPageSize = 10;
  var installedSearchQuery = "";
  var lastLogID = 0;
  var logLines = [];
  var maxLogLines = 2000;
  var managersRendered = false;
  var updateJobPollTimer = null;
  var activeUpdateKeys = [];
  var activeUpdateJobID = "";
  function $(id){ return document.getElementById(id); }
  function api(path, params){
    var url = new URL(path, window.location.origin);
    url.searchParams.set("token", token);
    Object.keys(params || {}).forEach(function(key){ url.searchParams.set(key, params[key]); });
    return url.toString();
  }
  function html(value){
    return String(value == null ? "" : value).replace(/[&<>"']/g, function(ch){
      return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[ch];
    });
  }
  function attr(value){ return html(value); }
  function showNotice(message){
    var notice = $("notice");
    if(!notice){ return; }
    notice.textContent = message || "";
    notice.classList.toggle("hidden", !message);
  }
  function formatLogEntry(entry){
    var stamp = entry.timestamp || "";
    if(stamp){
      var date = new Date(stamp);
      stamp = isNaN(date.getTime()) ? stamp : date.toLocaleTimeString();
    }
    var stream = (entry.stream || "app").toUpperCase();
    return "[" + stamp + "] " + stream + " " + (entry.message || "");
  }
  function renderLogLines(shouldScroll){
    var target = $("session-log");
    if(!target){ return; }
    target.textContent = logLines.join("\n") + (logLines.length ? "\n" : "");
    if(shouldScroll){
      target.scrollTop = target.scrollHeight;
    }
  }
  function appendLogEntries(entries){
    if(!entries || entries.length === 0){ return; }
    entries.forEach(function(entry){
      lastLogID = Math.max(lastLogID, Number(entry.id || 0));
      logLines.push(formatLogEntry(entry));
    });
    if(logLines.length > maxLogLines){
      logLines = logLines.slice(logLines.length - maxLogLines);
    }
    var auto = $("log-autoscroll");
    renderLogLines(!auto || auto.checked);
  }
  async function loadLogs(){
    try{
      var data = await (await fetch(api("/api/logs", {since:String(lastLogID)}))).json();
      appendLogEntries(data.entries || []);
      if(typeof data.latest_id === "number" && data.latest_id > lastLogID && (!data.entries || data.entries.length === 0)){
        lastLogID = data.latest_id;
      }
    }catch(e){}
  }
  async function copyLogView(){
    var target = $("session-log");
    var text = target ? target.textContent || "" : "";
    try{
      if(navigator.clipboard && navigator.clipboard.writeText){
        await navigator.clipboard.writeText(text);
      }else{
        var textarea = document.createElement("textarea");
        textarea.value = text;
        textarea.setAttribute("readonly", "");
        textarea.style.position = "fixed";
        textarea.style.left = "-9999px";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      }
      showNotice("Session log copied.");
    }catch(e){
      showNotice("Could not copy session log: " + e.message);
    }
  }
  function setGlobalProgress(show, message, cancelVisible){
    var panel = $("update-progress");
    if(!panel){ return; }
    var title = panel.querySelector(".progress-title");
    if(title){ title.textContent = message || "Updating packages..."; }
    var cancel = $("cancel-updates-button");
    if(cancel){
      cancel.classList.toggle("hidden", !cancelVisible);
      cancel.disabled = !cancelVisible;
    }
    panel.classList.toggle("hidden", !show);
  }
  function setUpdateBusy(busy, keys, currentKey){
    updateBusy = busy;
    var keySet = {};
    (keys || []).forEach(function(key){ keySet[key] = true; });
    document.querySelectorAll("button,input").forEach(function(control){
      if(control.closest("#session-log-panel")){ return; }
      if(control.id === "theme-toggle" || control.closest(".header-actions form")){ return; }
      if(control.classList.contains("auto-package")){ control.disabled = busy; return; }
      if(control.name === "package_key" || control.closest(".update-form") || control.id === "update-all-button" || control.id === "update-selected-button" || control.id === "refresh-packages"){
        control.disabled = busy;
      }
    });
    document.querySelectorAll(".update-form").forEach(function(form){
      var active = busy && (keys == null || keySet[form.dataset.key]);
      var progress = form.querySelector(".row-progress");
      if(progress){ progress.classList.toggle("hidden", !active); }
    });
    document.querySelectorAll("tr[data-key]").forEach(function(row){
      row.classList.toggle("updating-current", !!currentKey && row.dataset.key === currentKey);
    });
  }
  function compactNoticeText(value){
    return String(value || "").replace(/\s+/g, " ").trim();
  }
  function truncateNoticeText(value, maxLength){
    value = compactNoticeText(value);
    if(value.length <= maxLength){ return value; }
    return value.slice(0, Math.max(0, maxLength - 3)).trimEnd() + "...";
  }
  function firstMeaningfulOutputLine(value){
    var lines = String(value || "").split(/\r?\n/);
    for(var i = 0; i < lines.length; i++){
      var line = compactNoticeText(lines[i]);
      if(!line || /^[\\|\/-]+$/.test(line)){ continue; }
      if(/^progress:/i.test(line)){ continue; }
      return line;
    }
    return "";
  }
  function commandLabel(result){
    if(!result || !result.command){ return "command"; }
    var line = compactNoticeText(String(result.command).split(/\r?\n/)[0]);
    if(!line){ return "command"; }
    var parts = line.split(/\s+/);
    var exe = (parts.shift() || "command").split(/[\\\/]/).pop().replace(/\.exe$/i, "");
    var detail = [];
    for(var i = 0; i < parts.length && detail.length < 2; i++){
      if(parts[i].charAt(0) === "-" || parts[i].charAt(0) === "/"){ continue; }
      detail.push(parts[i]);
    }
    return compactNoticeText([exe].concat(detail).join(" "));
  }
  function commandText(result){
    if(!result){ return "Code 0. See Session Log for full output."; }
    var reason = firstMeaningfulOutputLine(result.stderr) || firstMeaningfulOutputLine(result.stdout);
    var text = commandLabel(result) + " failed with code " + (result.code || 0);
    if(reason){ text += ": " + truncateNoticeText(reason, 140); }
    return truncateNoticeText(text, 210) + ". See Session Log for full output.";
  }
  function resultNotice(successMessage, failurePrefix, result){
    return result && result.ok ? successMessage : failurePrefix + ". " + commandText(result);
  }
  function summarizeUpdatePayload(payload){
    if(payload.notice){
      return payload.notice;
    }
    if(payload.result){
      return payload.result.ok ? "Update completed. Refreshing package status..." : "Update finished with errors. " + commandText(payload.result);
    }
    var results = payload.results || [];
    var failed = results.filter(function(item){ return !(item.result && item.result.ok); });
    if(failed.length === 0){
      return "Update completed. Refreshing package status...";
    }
    return failed.length + " update command(s) finished with errors. " + commandText(failed[0].result);
  }
  function postForm(path, params){
    var body = params instanceof URLSearchParams ? params : new URLSearchParams(params || {});
    return fetch(api(path), {method:"POST", headers:{"Content-Type":"application/x-www-form-urlencoded"}, body:body});
  }
  function setTheme(theme){
    document.documentElement.dataset.theme = theme;
    try{ localStorage.setItem("windows-updater-theme", theme); }catch(e){}
    var button = $("theme-toggle");
    if(button){ button.textContent = theme === "dark" ? "Light Mode" : "Dark Mode"; }
  }
  function currentTheme(){
    return document.documentElement.dataset.theme === "light" ? "light" : "dark";
  }
  function renderManagers(data){
    var target = $("manager-list");
    if(!target){ return; }
    var managers = data.managers || {};
    var names = Object.keys(managers).sort();
    if(names.length === 0){
      if(managersRendered){ return; }
      var placeholder = '<p class="muted">' + (data.loading ? 'Checking package managers...' : 'No package manager status yet.') + '</p>';
      if(target.innerHTML !== placeholder){ target.innerHTML = placeholder; }
      return;
    }
    managersRendered = true;
    function managerAvailabilityText(name, manager){
      var version = String(manager.version || "").trim();
      if(name === "store" && (!version || /^usage:/i.test(version) || /<command>/i.test(version))){
        return "Available";
      }
      return "Available" + (version ? " " + version : "");
    }
    function managerDisplayDetails(name, manager){
      var details = [];
      if(name === "store"){
        if(manager.inventory_available){
          details.push('<span class="muted">Store apps detected via ' + html(manager.inventory_backend || 'AppX') + ' inventory</span>');
        }
        if(!manager.available && manager.action_backend === "winget-msstore-fallback"){
          details.push('<span class="muted">Store installs and updates can fall back to winget for compatible Store IDs.</span>');
        }
        return details.join("");
      }
      if(manager.inventory_available){ details.push('<span class="badge ok">Inventory available</span>'); }
      return details.join("");
    }
    var markup = names.map(function(name){
      var manager = managers[name] || {};
      var details = managerDisplayDetails(name, manager);
      if(manager.available){
        return '<div class="manager"><strong>' + html(name) + '</strong><span class="badge ok">' + html(managerAvailabilityText(name, manager)) + '</span>' + details + '<span class="muted">' + html(manager.path || '') + '</span></div>';
      }
      return '<div class="manager"><strong>' + html(name) + '</strong><span class="badge error">Missing</span>' + details + '<span class="muted">' + html(manager.error || '') + '</span><form class="manager-install-form" method="post" action="/api/managers/install"><input type="hidden" name="token" value="' + attr(token) + '"><input type="hidden" name="manager" value="' + attr(name) + '"><button type="submit">Install ' + html(name) + '</button></form></div>';
    }).join("");
    if(target.innerHTML !== markup){ target.innerHTML = markup; }
  }
  function renderStatus(data){
    renderManagers(data);
    var startup = $("startup-toggle");
    if(startup){
      startup.disabled = !!data.loading;
      startup.dataset.enabled = data.startup_enabled ? "true" : "false";
      startup.textContent = data.startup_enabled ? "Disable Start With Windows" : "Enable Start With Windows";
    }
    var auto = $("auto-global-toggle");
    var globalEnabled = !!(data.settings && data.settings.auto_update_global);
    if(auto){
      auto.disabled = !!data.loading;
      auto.dataset.enabled = globalEnabled ? "true" : "false";
      auto.textContent = globalEnabled ? "Disable Daily Auto-Update" : "Enable Daily Auto-Update";
    }
    var status = $("automation-status");
    if(status){
      status.textContent = "Startup task: " + (!!data.startup_enabled) + " - Auto task: " + (!!data.auto_task_enabled);
    }
  }
  function packageNameCell(pkg){
    var secondary = pkg.action_backend === "appx-inventory" ? "Store app" : pkg.id;
    return '<strong>' + html(pkg.name) + '</strong><br><span class="muted">' + html(secondary) + '</span>';
  }
	function managerCell(pkg){
		var backend = pkg.action_backend ? '<br><span class="muted">' + html(pkg.action_backend === "store-cli-resolved" ? "store-cli-resolved" : pkg.action_backend) + '</span>' : '';
		return '<span class="badge">' + html(pkg.manager) + '</span>' + backend;
	}
  function autoButton(pkg){
    if(pkg.update_supported === false){
      return '<span class="muted">N/A</span>';
    }
    return '<button class="auto-package" type="button" data-key="' + attr(pkg.key) + '" data-enabled="' + (pkg.auto_update ? 'true' : 'false') + '"' + (updateBusy ? ' disabled' : '') + '>' + (pkg.auto_update ? 'On' : 'Off') + '</button>';
  }
	function updateForm(pkg){
		if(pkg.update_supported === false){
			return '<span class="muted">Inventory only</span>';
		}
		return '<form class="update-form" data-key="' + attr(pkg.key) + '" method="post" action="/api/update"><input type="hidden" name="token" value="' + attr(token) + '"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit"' + (updateBusy ? ' disabled' : '') + '>Update</button><div class="row-progress hidden"><div class="progress-bar"><span></span></div></div></form>';
	}
	function installedAction(pkg){
		if(pkg.action_backend === "store-cli-resolved" && pkg.update_available){
			return updateForm(pkg);
		}
		return '<span class="muted">-</span>';
	}
  function packageMatchesInstalledSearch(pkg){
    var query = installedSearchQuery.trim().toLowerCase();
    if(!query){ return true; }
    return [pkg.name, pkg.id, pkg.manager, pkg.version, pkg.available_version].some(function(value){
      return String(value || "").toLowerCase().indexOf(query) !== -1;
    });
  }
  function renderUpdatesTable(updates, loading){
    var target = $("updates-body");
    if(!target){ return; }
    if(updates.length === 0){
      target.innerHTML = '<tr><td colspan="7">' + (loading ? 'Checking for updates...' : 'No updates available.') + '</td></tr>';
      return;
    }
    target.innerHTML = updates.map(function(pkg){
      var selectable = pkg.update_supported !== false;
      return '<tr data-key="' + attr(pkg.key) + '"><td><input form="update-selected-form" type="checkbox" name="package_key" value="' + attr(pkg.key) + '"' + ((updateBusy || !selectable) ? ' disabled' : '') + '></td><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + html(pkg.available_version) + '</td><td>' + autoButton(pkg) + '</td><td>' + updateForm(pkg) + '</td></tr>';
    }).join("");
  }
  function renderInstalledTable(loading){
    var target = $("packages-body");
    var status = $("installed-page-status");
    var prev = $("installed-prev");
    var next = $("installed-next");
    if(!target){ return; }
    var visiblePackages = packages.filter(packageMatchesInstalledSearch);
    var total = visiblePackages.length;
    var totalPages = Math.max(1, Math.ceil(total / installedPageSize));
    if(installedPage > totalPages){ installedPage = totalPages; }
    if(installedPage < 1){ installedPage = 1; }
	if(total === 0){
		target.innerHTML = '<tr><td colspan="7">' + (loading ? 'Loading packages...' : (installedSearchQuery ? 'No packages match your filter.' : 'No managed packages found.')) + '</td></tr>';
      if(status){ status.textContent = loading ? 'Loading...' : (installedSearchQuery ? 'No matches' : 'No packages'); }
      if(prev){ prev.disabled = true; }
      if(next){ next.disabled = true; }
      return;
    }
    var start = (installedPage - 1) * installedPageSize;
    var visible = visiblePackages.slice(start, start + installedPageSize);
	target.innerHTML = visible.map(function(pkg){
		var rowStatus = pkg.update_supported === false ? '<span class="badge">Inventory only</span>' : (pkg.update_available ? '<span class="badge warn">Update</span>' : '<span class="badge ok">Current</span>');
		return '<tr data-key="' + attr(pkg.key) + '"><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + html(pkg.available_version) + '</td><td>' + rowStatus + '</td><td>' + autoButton(pkg) + '</td><td>' + installedAction(pkg) + '</td></tr>';
	}).join("");
    if(status){
      status.textContent = "Showing " + (start + 1) + "-" + Math.min(start + installedPageSize, total) + " of " + total + (installedSearchQuery ? " matches" : "");
    }
    if(prev){ prev.disabled = installedPage <= 1; }
    if(next){ next.disabled = installedPage >= totalPages; }
  }
  function renderPackages(data){
    renderManagers(data);
    packages = data.packages || [];
    var updates = packages.filter(function(pkg){ return !!pkg.update_available; });
    var updateablePackages = packages.filter(function(pkg){ return pkg.update_supported !== false; });
    $("auto-all").disabled = updateBusy || updateablePackages.length === 0;
    $("auto-none").disabled = updateBusy || updateablePackages.length === 0;
    renderUpdatesTable(updates, !!data.loading);
    renderInstalledTable(!!data.loading);
    var supportedUpdates = updates.filter(function(pkg){ return pkg.update_supported !== false; });
    $("update-all-button").disabled = updateBusy || supportedUpdates.length === 0;
    $("update-selected-button").disabled = updateBusy || supportedUpdates.length === 0;
  }
  function renderScan(scan){
    var panel = $("scan-panel");
    if(!panel){ return; }
    panel.classList.remove("hidden");
    var counts = scan.source_counts || {};
    var registryCount = counts.registry || scan.registry_count || 0;
    var wingetCount = counts.winget || scan.winget_count || 0;
    var storeCount = counts.store || scan.store_count || 0;
    $("scan-summary").textContent = "Tracked " + (scan.tracked_count || 0) + " apps - Registry " + registryCount + " - Winget " + wingetCount + " - Store " + storeCount;
    var errors = $("scan-errors");
    var errorText = (scan.errors || []).map(function(item){ return (item.source || "source") + ": " + (item.error || ""); }).join("\n");
    errors.textContent = errorText;
    errors.classList.toggle("hidden", !errorText);
    var apps = scan.new_apps || [];
    $("scan-body").innerHTML = apps.length ? apps.map(function(app){
      return '<tr><td>' + html(app.source) + '</td><td>' + html(app.name) + '</td><td>' + html(app.version) + '</td><td>' + html(app.publisher) + '</td><td>' + html(app.install_location) + '</td></tr>';
    }).join("") : '<tr><td colspan="5">No newly detected applications.</td></tr>';
  }
  function renderSearch(data){
    var panel = $("search-results-panel");
    var body = $("search-results-body");
    if(!panel || !body){ return; }
    panel.classList.remove("hidden");
    var results = data.packages || [];
    body.innerHTML = results.length ? results.map(function(pkg){
      return '<tr><td>' + html(pkg.name) + '</td><td>' + html(pkg.manager) + (pkg.action_backend ? '<br><span class="muted">' + html(pkg.action_backend) + '</span>' : '') + '</td><td>' + html(pkg.id) + '</td><td>' + html(pkg.version) + '</td><td><form class="install-form" method="post" action="/api/install"><input type="hidden" name="token" value="' + attr(token) + '"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit">Install</button></form></td></tr>';
    }).join("") : '<tr><td colspan="5">No installable results.</td></tr>';
  }
  async function loadStatus(force){
    try{
      var data = await (await fetch(api("/api/status", force ? {refresh:"1"} : {}))).json();
      renderStatus(data);
      if(data.loading){ setTimeout(function(){ loadStatus(false); }, 800); }
    }catch(e){ showNotice("Could not load status: " + e.message); }
  }
  async function loadPackages(force){
    try{
      var data = await (await fetch(api("/api/packages", force ? {refresh:"1"} : {}))).json();
      renderPackages(data);
      if(data.loading){ setTimeout(function(){ loadPackages(false); }, 900); }
      return data;
    }catch(e){ showNotice("Could not load packages: " + e.message); }
  }
  async function refreshPackagesAfterUpdate(refreshAlreadyStarted){
    var data = await loadPackages(!refreshAlreadyStarted);
    while(data && data.loading){
      await new Promise(function(resolve){ setTimeout(resolve, 900); });
      data = await loadPackages(false);
    }
    return data;
  }
  async function runUpdateRequest(path, params, keys, message){
    setGlobalProgress(true, message || "Updating packages...");
    setUpdateBusy(true, keys);
    showNotice(message || "Updating packages...");
    try{
      var response = await postForm(path, params);
      var payload = await response.json();
      if(!response.ok && !payload.result && !payload.results){
        throw new Error(payload.error || "Update failed");
      }
      showNotice(summarizeUpdatePayload(payload));
      if(payload.refresh_started){
        setGlobalProgress(true, "Refreshing package status...");
        await refreshPackagesAfterUpdate(true);
      }
    }catch(e){
      showNotice("Update failed: " + e.message);
    }finally{
      setUpdateBusy(false, []);
      setGlobalProgress(false);
    }
  }
  function updateJobMessage(status){
    status = status || {};
    var mode = status.mode === "selected" ? "selected" : "all";
    if(status.cancel_requested && status.running){
      return "Cancelling after current command stops...";
    }
    if(status.running){
      var name = status.current_package || "package";
      var counter = status.total ? " (" + (status.current_index || 0) + "/" + status.total + ")" : "";
      return (mode === "selected" ? "Updating selected packages: " : "Updating all packages: ") + name + counter;
    }
    if(status.cancel_requested){
      return status.notice || "Update cancelled. Refreshing package status...";
    }
    return status.notice || "Update completed. Refreshing package status...";
  }
  function updateableUpdateKeys(){
    return packages.filter(function(pkg){
      return !!pkg.update_available && pkg.update_supported !== false;
    }).map(function(pkg){ return pkg.key; });
  }
  function updateJobPackageKeys(status){
    return status && Array.isArray(status.package_keys) ? status.package_keys : [];
  }
  function applyUpdateJobPackageKeys(status){
    var keys = updateJobPackageKeys(status);
    if(keys.length > 0){
      activeUpdateKeys = keys;
    }
  }
  function clearUpdateJobPoll(){
    if(updateJobPollTimer){
      clearTimeout(updateJobPollTimer);
      updateJobPollTimer = null;
    }
  }
  function renderUpdateJobStatus(status){
    applyUpdateJobPackageKeys(status);
    var message = updateJobMessage(status);
    var active = !!(status && status.running);
    setUpdateBusy(active || !!(status && status.refresh_started), activeUpdateKeys, status ? status.current_key : "");
    setGlobalProgress(active || !!(status && status.refresh_started), message, active && !status.cancel_requested);
    showNotice(message);
  }
  async function finishUpdateJob(status){
    clearUpdateJobPoll();
    renderUpdateJobStatus(status);
    try{
      if(status && status.refresh_started){
        setGlobalProgress(true, updateJobMessage(status), false);
        await refreshPackagesAfterUpdate(true);
      }
    }finally{
      activeUpdateKeys = [];
      activeUpdateJobID = "";
      setUpdateBusy(false, [], "");
      setGlobalProgress(false, "", false);
    }
  }
  async function pollUpdateJobStatus(){
    try{
      var response = await fetch(api("/api/update-all/status"));
      var status = await response.json();
      if(!response.ok){ throw new Error(status.error || "Could not load update status"); }
      if(activeUpdateJobID && status.job_id && status.job_id !== activeUpdateJobID){ return; }
      renderUpdateJobStatus(status);
      if(status.running){
        updateJobPollTimer = setTimeout(pollUpdateJobStatus, 800);
        return;
      }
      await finishUpdateJob(status);
    }catch(e){
      showNotice("Could not load update status: " + e.message);
      updateJobPollTimer = setTimeout(pollUpdateJobStatus, 1200);
    }
  }
  async function startUpdateJob(params, keys, message){
    clearUpdateJobPoll();
    activeUpdateKeys = keys || [];
    activeUpdateJobID = "";
    setUpdateBusy(true, activeUpdateKeys);
    setGlobalProgress(true, message || "Starting updates...", true);
    showNotice(message || "Starting updates...");
    try{
      var response = await postForm("/api/update-all", params);
      var status = await response.json();
      if(!response.ok){
        if(response.status === 409 && status.running){
          activeUpdateJobID = status.job_id || "";
          renderUpdateJobStatus(status);
          updateJobPollTimer = setTimeout(pollUpdateJobStatus, 800);
          return;
        }
        throw new Error(status.error || status.notice || "Could not start update job");
      }
      activeUpdateJobID = status.job_id || "";
      renderUpdateJobStatus(status);
      if(status.running){
        updateJobPollTimer = setTimeout(pollUpdateJobStatus, 800);
        return;
      }
      await finishUpdateJob(status);
    }catch(e){
      activeUpdateKeys = [];
      activeUpdateJobID = "";
      setUpdateBusy(false, [], "");
      setGlobalProgress(false, "", false);
      showNotice("Update failed: " + e.message);
    }
  }
  async function checkActiveUpdateJob(){
    try{
      var response = await fetch(api("/api/update-all/status"));
      var status = await response.json();
      if(!response.ok || !status.running){ return; }
      activeUpdateJobID = status.job_id || "";
      renderUpdateJobStatus(status);
      clearUpdateJobPoll();
      updateJobPollTimer = setTimeout(pollUpdateJobStatus, 800);
    }catch(e){}
  }
  async function cancelUpdateJob(){
    var button = $("cancel-updates-button");
    if(button){ button.disabled = true; }
    showNotice("Cancelling after current command stops...");
    setGlobalProgress(true, "Cancelling after current command stops...", false);
    try{
      var response = await postForm("/api/update-all/cancel", {});
      var status = await response.json();
      if(!response.ok){ throw new Error(status.error || "Could not cancel update job"); }
      renderUpdateJobStatus(status);
      clearUpdateJobPoll();
      updateJobPollTimer = setTimeout(pollUpdateJobStatus, 500);
    }catch(e){
      showNotice("Could not cancel updates: " + e.message);
      if(button){ button.disabled = false; }
    }
  }
  async function loadSearch(query){
    var body = $("search-results-body");
    $("search-results-panel").classList.remove("hidden");
    body.innerHTML = '<tr><td colspan="5">Searching...</td></tr>';
    try{
      var response = await fetch(api("/api/search", {q:query}));
      var data = await response.json();
      if(!response.ok){ throw new Error(data.error || "Search failed"); }
      renderSearch(data);
    }catch(e){
      body.innerHTML = '<tr><td colspan="5">' + html(e.message) + '</td></tr>';
    }
  }
  async function installFromForm(form){
    var button = form.querySelector("button");
    if(button){ button.disabled = true; }
    showNotice("Installing package...");
    try{
      var response = await postForm("/api/install", new URLSearchParams(new FormData(form)));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Install failed"); }
      showNotice(resultNotice("Install command completed. Refreshing package status...", "Install finished with errors", payload.result));
      await refreshPackagesAfterUpdate(!!payload.refresh_started);
    }catch(e){
      showNotice("Install failed: " + e.message);
    }finally{
      if(button){ button.disabled = false; }
    }
  }
  async function installManagerFromForm(form){
    var button = form.querySelector("button");
    if(button){ button.disabled = true; }
    showNotice("Opening package manager install action...");
    try{
      var response = await postForm("/api/managers/install", new URLSearchParams(new FormData(form)));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Package manager install failed"); }
      showNotice(resultNotice("Package manager install action completed.", "Package manager install finished with errors", payload.result));
      loadStatus(true);
    }catch(e){
      showNotice("Package manager install failed: " + e.message);
    }finally{
      if(button){ button.disabled = false; }
    }
  }
  async function setPackageAuto(key, enabled, button){
    button.disabled = true;
    var params = new URLSearchParams();
    params.append("package_key", key);
    params.set("package_enabled", enabled ? "true" : "false");
    try{
      await postForm("/api/settings/auto-update", params);
      button.dataset.enabled = enabled ? "true" : "false";
      button.textContent = enabled ? "On" : "Off";
    }catch(e){ showNotice("Could not update auto setting: " + e.message); }
    button.disabled = false;
  }
  async function setAllAuto(enabled){
    var params = new URLSearchParams();
    params.set("global", enabled ? "true" : "false");
    params.set("package_enabled", enabled ? "true" : "false");
    packages.forEach(function(pkg){ if(pkg.update_supported !== false){ params.append("package_key", pkg.key); } });
    showNotice("Updating auto-update settings...");
    await postForm("/api/settings/auto-update", params);
    showNotice("Auto-update settings updated.");
    loadStatus(true);
    loadPackages(true);
  }
  document.addEventListener("click", function(event){
    var autoButton = event.target.closest(".auto-package");
    if(autoButton){
      setPackageAuto(autoButton.dataset.key, autoButton.dataset.enabled !== "true", autoButton);
    }
  });
  document.addEventListener("submit", function(event){
    var form = event.target;
    if(form.id === "search-form"){
      event.preventDefault();
      var query = String($("search-input").value || "").trim();
      if(!query){
        showNotice("Enter a package name to search.");
        return;
      }
      var url = new URL(window.location.href);
      url.searchParams.set("q", query);
      window.history.replaceState(null, "", url.toString());
      loadSearch(query);
      return;
    }
    if(form.matches(".install-form")){
      event.preventDefault();
      installFromForm(form);
      return;
    }
    if(form.matches(".manager-install-form")){
      event.preventDefault();
      installManagerFromForm(form);
      return;
    }
    if(form.matches(".update-form")){
      event.preventDefault();
      var key = form.dataset.key;
      runUpdateRequest("/api/update", new URLSearchParams(new FormData(form)), [key], "Updating package...");
      return;
    }
    if(form.id === "update-selected-form"){
      event.preventDefault();
      var params = new URLSearchParams(new FormData(form));
      var keys = params.getAll("package_key");
      if(keys.length === 0){
        showNotice("Select at least one package to update.");
        return;
      }
      startUpdateJob(params, keys, "Updating selected packages...");
      return;
    }
    if(form.matches(".update-all-form")){
      event.preventDefault();
      var allKeys = updateableUpdateKeys();
      startUpdateJob(new URLSearchParams(new FormData(form)), allKeys, "Updating all packages...");
    }
  });
  $("theme-toggle").addEventListener("click", function(){
    var next = currentTheme() === "dark" ? "light" : "dark";
    setTheme(next);
    postForm("/api/settings/theme", {theme:next}).catch(function(){});
  });
  $("scan-button").addEventListener("click", async function(){
    var button = this;
    button.disabled = true;
    showNotice("Scanning applications...");
    try{
      var data = await (await postForm("/api/scan", {})).json();
      renderScan(data);
      showNotice("Application scan completed.");
    }catch(e){ showNotice("Scan failed: " + e.message); }
    button.disabled = false;
  });
  $("refresh-packages").addEventListener("click", function(){ loadPackages(true); });
  $("installed-search").addEventListener("input", function(){
    installedSearchQuery = this.value || "";
    installedPage = 1;
    renderInstalledTable(false);
  });
  $("installed-prev").addEventListener("click", function(){
    installedPage--;
    renderInstalledTable(false);
  });
  $("installed-next").addEventListener("click", function(){
    installedPage++;
    renderInstalledTable(false);
  });
  $("startup-toggle").addEventListener("click", async function(){
    var enabled = this.dataset.enabled !== "true";
    this.disabled = true;
    await postForm("/api/settings/startup", {enabled:enabled ? "true" : "false"});
    loadStatus(true);
  });
  $("auto-global-toggle").addEventListener("click", async function(){
    var enabled = this.dataset.enabled !== "true";
    this.disabled = true;
    await postForm("/api/settings/auto-update", {global:enabled ? "true" : "false"});
    loadStatus(true);
  });
  $("auto-all").addEventListener("click", function(){ setAllAuto(true); });
  $("auto-none").addEventListener("click", function(){ setAllAuto(false); });
  $("clear-log-view").addEventListener("click", function(){
    logLines = [];
    renderLogLines(false);
  });
  $("copy-log-view").addEventListener("click", function(){ copyLogView(); });
  $("cancel-updates-button").addEventListener("click", function(){ cancelUpdateJob(); });
  setTheme(currentTheme());
  loadStatus(false);
  loadPackages(false).then(function(){ checkActiveUpdateJob(); });
  loadLogs();
  setInterval(loadLogs, 750);
  var query = new URLSearchParams(window.location.search).get("q");
  if(query){
    $("search-input").value = query;
    loadSearch(query);
  }
})();
`

const pageCSS = `
:root{color-scheme:light dark;--bg:#f6f7f9;--surface:#fff;--line:#d8dee8;--text:#18202b;--muted:#5d6979;--header:#172033;--header-text:#fff;--blue:#1d5fd1;--green:#18784f;--amber:#946200;--red:#b42318;--input:#fff;--input-text:#18202b;--log-bg:#111827;--log-text:#e5e7eb;--log-border:#243244}
[data-theme=dark]{--bg:#101419;--surface:#171d24;--line:#2c3542;--text:#ecf1f7;--muted:#a8b3c2;--header:#0c1117;--header-text:#f5f8fb;--blue:#5d9bff;--green:#71d29d;--amber:#f1c65b;--red:#ff8b7f;--input:#111820;--input-text:#ecf1f7;--log-bg:#05080d;--log-text:#d7f7df;--log-border:#1c2735}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 "Segoe UI",system-ui,sans-serif}.app-header{display:flex;justify-content:space-between;gap:16px;align-items:center;background:var(--header);color:var(--header-text);padding:18px 24px;border-bottom:4px solid #2c9a78}.app-header h1{margin:0;font-size:24px}.app-header p{margin:4px 0 0;color:#dce6f4}.header-actions,.section-heading,.search,.button-row,.progress-header{display:flex;gap:10px;align-items:center;flex-wrap:wrap}main{width:min(1480px,100%);margin:auto;padding:20px 24px}.status-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px}.panel,.notice,.log,.progress-panel{background:var(--surface);border:1px solid var(--line);margin-bottom:16px;padding:14px;box-shadow:0 8px 24px rgba(18,32,51,.08)}.hidden{display:none!important}.notice{border-left:4px solid var(--blue);max-height:96px;overflow:auto;overflow-wrap:anywhere;white-space:normal}.progress-panel{border-left:4px solid var(--amber)}.progress-header{justify-content:space-between;margin-bottom:8px}.progress-title{font-weight:700}.progress-bar{position:relative;height:6px;overflow:hidden;background:rgba(127,127,127,.18);border-radius:999px}.progress-bar span{position:absolute;inset:0 auto 0 0;width:38%;background:var(--blue);border-radius:999px;animation:progress-slide 1s linear infinite}.row-progress{margin-top:8px;min-width:90px}.row-progress .progress-bar{height:4px}@keyframes progress-slide{0%{transform:translateX(-110%)}100%{transform:translateX(270%)}}.manager{display:grid;gap:6px;margin-top:10px}.stack{display:grid;gap:10px}.muted{color:var(--muted)}.check-control{display:inline-flex;align-items:center;gap:6px;color:var(--muted);font-weight:650}button{min-height:34px;border:1px solid var(--blue);background:var(--blue);color:#fff;padding:6px 10px;font:inherit;font-weight:600;cursor:pointer}button:disabled{cursor:wait;opacity:.6}button.secondary{background:transparent;border-color:rgba(255,255,255,.35)}input{min-height:34px;border:1px solid var(--line);background:var(--input);color:var(--input-text);padding:6px 9px;font:inherit}input[type=checkbox]{min-height:auto;padding:0}table{width:100%;border-collapse:collapse;table-layout:fixed}tr.updating-current{background:rgba(93,155,255,.12)}th,td{border-bottom:1px solid var(--line);padding:9px 10px;text-align:left;vertical-align:middle;overflow-wrap:anywhere}th{color:var(--muted);text-transform:uppercase;font-size:12px}.badge{display:inline-flex;min-height:22px;align-items:center;border:1px solid var(--line);padding:1px 7px;background:rgba(127,127,127,.1);font-size:12px;font-weight:650}.badge.ok{color:var(--green)}.badge.warn{color:var(--amber)}.badge.error{color:var(--red)}pre{white-space:pre-wrap;overflow:auto;background:var(--log-bg);color:var(--log-text);padding:10px}.session-log{height:280px;margin:0;border:1px solid var(--log-border);font:12px/1.45 Consolas,"Cascadia Mono","Courier New",monospace}.log-panel .section-heading{justify-content:space-between;margin-bottom:10px}@media(max-width:900px){.app-header,.status-grid{display:block}.header-actions{margin-top:12px}main{padding:12px}table{min-width:900px}.panel{overflow-x:auto}.session-log{min-width:640px}}`
