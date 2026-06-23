// Embedded Windows Updater WebUI frontend.

(function(){
  var packages = [];
  var updateBusy = false;
  var updatePage = 1;
  var updatePageSize = 10;
  var installedPage = 1;
  var installedPageSize = 10;
  var installedSearchQuery = "";
  var searchResults = [];
  var searchMetadata = {};
  var searchPage = 1;
  var searchPageSize = 10;
  var maxBrowserLogEntries = 2500;
  var maxBrowserLogBytes = 1024 * 1024;
  var lastLogID = 0;
  var logEntries = [];
  var logBytes = 0;
  var logSearchQuery = "";
  var activeLogCategory = "all";
  var clearedLogBeforeByCategory = {};
  var logRenderFrame = 0;
  var managersRendered = false;
  var logPollTimer = null;
  var logPollDelay = 2500;
  var eventStream = null;
  var jobsInitialized = false;
  var serverJobs = [];
  var completedJobIDs = {};
  var activeUpdateKeys = [];
  var activeUpdateJobID = "";
  var pendingBulkUpdate = null;
  var lastUpdateResultJob = null;
  var latestStatus = null;
  var latestStoreScanHealth = null;
  var latestPackagesLoading = true;
  var statusRequestSeq = 0;
  var packageRequestSeq = 0;
  var statusController = null;
  var packageController = null;
  var statusPollTimer = null;
  var packagePollTimer = null;
  var statusPollDelay = 800;
  var packagePollDelay = 900;
  var toastSeq = 0;
  var toasts = [];
  var toastAnimationFrame = 0;
  var spinnerAnimationFrame = 0;
  var spinnerPeriodMs = 900;
  var spinnerObserver = null;
  var reducedMotionQuery = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;
  function $(id){ return document.getElementById(id); }
  function api(path, params){
    var url = new URL(path, window.location.origin);
    Object.keys(params || {}).forEach(function(key){ url.searchParams.set(key, params[key]); });
    return url.toString();
  }
  function html(value){
    return String(value == null ? "" : value).replace(/[&<>"']/g, function(ch){
      return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[ch];
    });
  }
  function attr(value){ return html(value); }
  function spinner(){
    return '<span class="spinner" aria-hidden="true"></span>';
  }
  function spinnerMotionReduced(){
    return !!(reducedMotionQuery && reducedMotionQuery.matches);
  }
  function hasActiveSpinners(){
    return !!document.querySelector(".spinner");
  }
  function updateSpinnerPhase(){
    if(spinnerMotionReduced()){
      document.documentElement.style.setProperty("--spinner-angle", "0deg");
      return;
    }
    var angle = ((Date.now() % spinnerPeriodMs) / spinnerPeriodMs) * 360;
    document.documentElement.style.setProperty("--spinner-angle", angle.toFixed(2) + "deg");
  }
  function startSpinnerLoop(){
    if(spinnerAnimationFrame || document.hidden || spinnerMotionReduced() || !hasActiveSpinners()){ return; }
    function tick(){
      spinnerAnimationFrame = 0;
      updateSpinnerPhase();
      if(!document.hidden && hasActiveSpinners()){
        spinnerAnimationFrame = window.requestAnimationFrame(tick);
      }
    }
    spinnerAnimationFrame = window.requestAnimationFrame(tick);
  }
  function stopSpinnerLoop(){
    if(spinnerAnimationFrame){
      window.cancelAnimationFrame(spinnerAnimationFrame);
      spinnerAnimationFrame = 0;
    }
  }
  function observeSpinnerPresence(){
    if(spinnerObserver || !window.MutationObserver || !document.body){ return; }
    spinnerObserver = new MutationObserver(function(){ startSpinnerLoop(); });
    spinnerObserver.observe(document.body, {childList:true, subtree:true});
  }
  function loadingText(message){
    return '<span class="loading-text">' + spinner() + '<span class="loading-message">' + html(message) + '</span></span>';
  }
  function setLoadingContent(target, message, loading){
    if(!target){ return; }
    message = String(message || "");
    if(!loading){
      target.textContent = message;
      return;
    }
    var loadingNode = target.querySelector(".loading-text");
    var messageNode = loadingNode ? loadingNode.querySelector(".loading-message") : null;
    if(!messageNode && loadingNode){
      messageNode = loadingNode.lastElementChild;
    }
    if(loadingNode && loadingNode.querySelector(".spinner") && messageNode){
      if(messageNode.textContent !== message){
        messageNode.textContent = message;
      }
      return;
    }
    target.innerHTML = loadingText(message);
  }
  function loadingTableRow(colspan, message){
    return '<tr><td colspan="' + colspan + '">' + loadingText(message) + '</td></tr>';
  }
  function progressBar(label){
    return '<div class="progress-bar" role="progressbar" aria-label="' + attr(label || "Operation in progress") + '" aria-valuetext="In progress"><span></span></div>';
  }
  function icon(name){
    var paths = {
      moon:'<path d="M12 3a6 6 0 1 0 6 6c0 5-4 9-9 9a6 6 0 0 0 3-15Z"/>',
      sun:'<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.9 4.9 1.4 1.4"/><path d="m17.7 17.7 1.4 1.4"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m4.9 19.1 1.4-1.4"/><path d="m17.7 6.3 1.4-1.4"/>',
      refresh:'<path d="M21 12a9 9 0 0 1-15.5 6.2"/><path d="M3 12a9 9 0 0 1 15.5-6.2"/><path d="M3 18v-6h6"/><path d="M21 6v6h-6"/>',
      update:'<path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/>',
      install:'<path d="M12 5v14"/><path d="M5 12h14"/>',
      check:'<path d="m5 12 4 4L19 6"/>',
      alert:'<path d="M12 9v4"/><path d="M12 17h.01"/><path d="M10.3 4.3 2.5 18a2 2 0 0 0 1.7 3h15.6a2 2 0 0 0 1.7-3L13.7 4.3a2 2 0 0 0-3.4 0Z"/>',
      box:'<path d="M4 7h16"/><path d="M6 7v12h12V7"/><path d="M9 11h6"/>'
    };
    return '<span class="button-icon" aria-hidden="true"><svg viewBox="0 0 24 24">' + (paths[name] || paths.box) + '</svg></span>';
  }
  function showNotice(message, loading){
    var notice = $("notice");
    if(!notice){ return; }
    setLoadingContent(notice, message || "", !!(loading && message));
    notice.classList.toggle("hidden", !message);
  }
  function showToast(message, kind, duration){
    var toast = {
      id: ++toastSeq,
      message: String(message || ""),
      kind: kind || "info",
      totalDuration: Math.max(duration || 10000, 10000),
      remaining: Math.max(duration || 10000, 10000),
      timer: null,
      startedAt: 0
    };
    if(!toast.message){ return; }
    toasts.push(toast);
    renderToasts();
    startToastTimer(toast);
  }
  function renderToasts(){
    var region = $("toast-region");
    if(!region){ return; }
    region.innerHTML = toasts.map(function(toast){
      return '<article class="toast toast-' + attr(toast.kind) + '" data-toast-id="' + attr(toast.id) + '"><div><strong>' + html(toastTitle(toast.kind)) + '</strong><p>' + html(toast.message) + '</p></div><button class="toast-close ghost" type="button" aria-label="Dismiss notification">&times;</button><span class="toast-progress" aria-hidden="true"><span></span></span></article>';
    }).join("");
    updateToastProgress();
  }
  function toastTitle(kind){
    if(kind === "success"){ return "Success"; }
    if(kind === "error"){ return "Needs attention"; }
    return "Notice";
  }
  function toastProgress(toast){
    return Math.max(0, Math.min(1, toast.remaining / Math.max(1, toast.totalDuration)));
  }
  function startToastTimer(toast){
    clearToastTimer(toast);
    if(document.hidden){ return; }
    toast.startedAt = Date.now();
    toast.timer = setTimeout(function(){ removeToast(toast.id); }, Math.max(0, toast.remaining));
    startToastProgressLoop();
  }
  function clearToastTimer(toast){
    if(toast.timer){
      clearTimeout(toast.timer);
      toast.timer = null;
    }
  }
  function pauseToastTimers(){
    toasts.forEach(function(toast){
      if(toast.timer){
        toast.remaining = Math.max(0, toast.remaining - (Date.now() - toast.startedAt));
        clearToastTimer(toast);
      }
    });
    stopToastProgressLoop();
    updateToastProgress();
  }
  function resumeToastTimers(){
    toasts.slice().forEach(function(toast){
      if(toast.remaining <= 0){
        removeToast(toast.id);
      }else{
        startToastTimer(toast);
      }
    });
    startToastProgressLoop();
  }
  function removeToast(id){
    toasts = toasts.filter(function(toast){
      if(toast.id === id){
        clearToastTimer(toast);
        return false;
      }
      return true;
    });
    renderToasts();
    if(toasts.length === 0){ stopToastProgressLoop(); }
  }
  function updateToastProgress(){
    toasts.forEach(function(toast){
      if(toast.timer){
        var elapsed = Date.now() - toast.startedAt;
        var currentRemaining = Math.max(0, toast.remaining - elapsed);
        var element = document.querySelector('.toast[data-toast-id="' + toast.id + '"]');
        if(element){ element.style.setProperty("--toast-progress", String(Math.max(0, Math.min(1, currentRemaining / Math.max(1, toast.totalDuration))))); }
      }
    });
  }
  function startToastProgressLoop(){
    if(toastAnimationFrame || document.hidden || toasts.length === 0){ return; }
    function tick(){
      toastAnimationFrame = 0;
      if(document.hidden || toasts.length === 0){ return; }
      updateToastProgress();
      toastAnimationFrame = window.requestAnimationFrame(tick);
    }
    toastAnimationFrame = window.requestAnimationFrame(tick);
  }
  function stopToastProgressLoop(){
    if(toastAnimationFrame){
      window.cancelAnimationFrame(toastAnimationFrame);
      toastAnimationFrame = 0;
    }
  }
  function updatePayloadSucceeded(payload){
    if(payload && payload.result){ return !!payload.result.ok; }
    var results = payload && payload.results ? payload.results : [];
    if(results.length === 0){ return false; }
    return results.every(function(item){ return item.result && item.result.ok; });
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
  function logEntryClientSize(entry){
    var size = 48;
    size += String(entry.timestamp || "").length;
    size += String(entry.stream || "").length;
    size += String(entry.message || "").length;
    (entry.categories || []).forEach(function(category){ size += String(category || "").length + 1; });
    return size;
  }
  function prepareLogEntry(entry){
    entry._formatted = formatLogEntry(entry);
    entry._size = logEntryClientSize(entry);
    return entry;
  }
  function trimBrowserLogs(){
    while(logEntries.length > 0 && (logEntries.length > maxBrowserLogEntries || logBytes > maxBrowserLogBytes)){
      var removed = logEntries.shift();
      logBytes -= Number(removed && removed._size || 0);
    }
    if(logBytes < 0){ logBytes = 0; }
  }
  function logEntryInActiveCategory(entry){
    var category = activeLogCategory || "all";
    var categories = entry.categories || ["all", "application"];
    return categories.indexOf(category) !== -1;
  }
  function renderLogLines(shouldScroll){
    var target = $("session-log");
    if(!target){ return; }
    var lines = filteredLogLines();
    target.textContent = lines.join("\n") + (lines.length ? "\n" : "");
    if(shouldScroll){
      target.scrollTop = target.scrollHeight;
    }
  }
  function scheduleLogRender(shouldScroll){
    if(logRenderFrame){ return; }
    logRenderFrame = window.requestAnimationFrame(function(){
      logRenderFrame = 0;
      renderLogLines(shouldScroll);
    });
  }
  function filteredLogLines(){
    var query = logSearchQuery.trim().toLowerCase();
    var clearedBefore = clearedLogBeforeByCategory[activeLogCategory || "all"] || 0;
    return logEntries.filter(function(entry){
      return Number(entry.id || 0) > clearedBefore && logEntryInActiveCategory(entry);
    }).map(function(entry){ return entry._formatted || formatLogEntry(entry); }).filter(function(line){
      return !query || line.toLowerCase().indexOf(query) !== -1;
    });
  }
  function appendLogEntries(entries){
    if(!entries || entries.length === 0){ return; }
    entries.forEach(function(entry){
      lastLogID = Math.max(lastLogID, Number(entry.id || 0));
      logEntries.push(prepareLogEntry(entry));
      logBytes += Number(entry._size || 0);
    });
    trimBrowserLogs();
    var auto = $("log-autoscroll");
    scheduleLogRender(!auto || auto.checked);
  }
  function setLogConnectionState(state, message){
    var target = $("log-connection-status");
    if(!target){ return; }
    target.textContent = message || state;
    target.classList.toggle("ok", state === "connected");
    target.classList.toggle("warn", state === "reconnecting");
    target.classList.toggle("error", state === "disconnected");
  }
  async function loadLogs(){
    try{
      setLogConnectionState("reconnecting", "Reconnecting");
      var response = await fetch(api("/api/logs", {since:String(lastLogID)}));
      var data = await response.json();
      if(!response.ok){ throw new Error(data.error || "Log polling failed"); }
      appendLogEntries(data.entries || []);
      if(typeof data.latest_id === "number" && data.latest_id > lastLogID && (!data.entries || data.entries.length === 0)){
        lastLogID = data.latest_id;
      }
      setLogConnectionState("connected", "Connected");
      if(data.entries && data.entries.length){
        logPollDelay = 1000;
      }else if(activeServerJobs().length > 0){
        logPollDelay = 1500;
      }else{
        logPollDelay = Math.min(10000, Math.round(logPollDelay * 1.35));
      }
    }catch(e){
      setLogConnectionState("disconnected", "Log reconnecting");
      logPollDelay = Math.min(15000, Math.round(logPollDelay * 1.6));
    }
  }
  function scheduleLogPolling(){
    if(logPollTimer || eventStream){ return; }
    logPollTimer = setTimeout(async function(){
      logPollTimer = null;
      if(document.hidden){
        scheduleLogPolling();
        return;
      }
      await loadLogs();
      scheduleLogPolling();
    }, logPollDelay);
  }
  function stopLogPolling(){
    if(logPollTimer){
      clearTimeout(logPollTimer);
      logPollTimer = null;
    }
  }
  function startEventStream(){
    if(!window.EventSource){
      scheduleLogPolling();
      return;
    }
    stopLogPolling();
    if(eventStream){ eventStream.close(); }
    setLogConnectionState("reconnecting", "Connecting");
    eventStream = new EventSource(api("/api/events", {since:String(lastLogID)}));
    eventStream.onopen = function(){
      setLogConnectionState("connected", "Connected");
    };
    eventStream.addEventListener("logs", function(event){
      try{
        var data = JSON.parse(event.data || "{}");
        appendLogEntries(data.entries || []);
        if(typeof data.latest_id === "number" && data.latest_id > lastLogID && (!data.entries || data.entries.length === 0)){
          lastLogID = data.latest_id;
        }
      }catch(e){}
    });
    eventStream.addEventListener("jobs", function(event){
      try{
        var data = JSON.parse(event.data || "{}");
        reconcileJobs(data.jobs || []);
      }catch(e){}
    });
    eventStream.onerror = function(){
      setLogConnectionState("disconnected", "Log reconnecting");
      if(eventStream){
        eventStream.close();
        eventStream = null;
      }
      scheduleLogPolling();
    };
  }
  function setActiveLogCategory(category){
    activeLogCategory = category || "all";
    document.querySelectorAll(".log-tab").forEach(function(button){
      var active = button.dataset.logCategory === activeLogCategory;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", active ? "true" : "false");
      button.setAttribute("tabindex", active ? "0" : "-1");
      if(active){
        var panel = $("session-log");
        if(panel){ panel.setAttribute("aria-labelledby", button.id); }
      }
    });
    renderLogLines(false);
  }
  function focusAdjacentLogTab(current, direction){
    var tabs = Array.prototype.slice.call(document.querySelectorAll(".log-tab"));
    if(tabs.length === 0){ return; }
    var index = Math.max(0, tabs.indexOf(current));
    if(direction === "home"){ index = 0; }
    else if(direction === "end"){ index = tabs.length - 1; }
    else{ index = (index + direction + tabs.length) % tabs.length; }
    tabs[index].focus();
    setActiveLogCategory(tabs[index].dataset.logCategory || "all");
  }
  function clearCurrentLogView(){
    clearedLogBeforeByCategory[activeLogCategory || "all"] = lastLogID;
    renderLogLines(false);
  }
  function exportLogs(){
    window.location.href = api("/api/logs/export");
  }
  function exportStoreDiagnostics(){
    window.location.href = api("/api/store/diagnostics/export");
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
    panel.setAttribute("aria-busy", show ? "true" : "false");
    var title = panel.querySelector(".progress-title");
    if(title){ setLoadingContent(title, message || "Updating packages...", show); }
    var bar = panel.querySelector("[role=progressbar]");
    if(bar){
      bar.setAttribute("aria-label", message || "Updating packages");
      bar.setAttribute("aria-valuetext", show ? "In progress" : "Idle");
    }
    var cancel = $("cancel-updates-button");
    if(cancel){
      cancel.classList.toggle("hidden", !cancelVisible);
      cancel.disabled = !cancelVisible;
    }
    panel.classList.toggle("hidden", !show);
  }
  function setInstallProgress(show, message){
    var panel = $("install-progress");
    if(!panel){ return; }
    panel.setAttribute("aria-busy", show ? "true" : "false");
    var title = panel.querySelector(".progress-title");
    if(title){ setLoadingContent(title, message || "Installing package...", show); }
    var bar = panel.querySelector("[role=progressbar]");
    if(bar){
      bar.setAttribute("aria-label", message || "Installing package");
      bar.setAttribute("aria-valuetext", show ? "In progress" : "Idle");
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
      if(control.name === "package_key" || control.closest(".update-form") || control.id === "update-all-button" || control.id === "update-selected-button" || control.id === "refresh-packages" || control.id === "update-allow-unknown" || control.id === "update-allow-pinned"){
        control.disabled = busy;
      }
    });
    document.querySelectorAll(".update-form").forEach(function(form){
      var active = busy && (keys == null || keySet[form.dataset.key]);
      var progress = form.querySelector(".row-progress");
      if(progress){ progress.classList.toggle("hidden", !active); }
      var progressBar = form.querySelector("[role=progressbar]");
      if(progressBar){
        progressBar.setAttribute("aria-valuetext", active ? "In progress" : "Idle");
      }
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
    return fetch(api(path), {method:"POST", headers:{"Content-Type":"application/x-www-form-urlencoded","X-Windows-Updater-WebUI":"1"}, body:body});
  }
  async function postCommandPayload(path, params, fallbackMessage){
    var response = await postForm(path, params);
    var payload = {};
    try{
      payload = await response.json();
    }catch(e){
      if(!response.ok){ throw new Error(response.statusText || fallbackMessage || "Request failed"); }
      throw e;
    }
    if(!response.ok){
      throw new Error(payload.error || fallbackMessage || "Request failed");
    }
    if(payload.result && !payload.result.ok){
      throw new Error(commandText(payload.result));
    }
    return payload;
  }


  function setTheme(theme){
    document.documentElement.dataset.theme = theme;
    try{ localStorage.setItem("windows-updater-theme", theme); }catch(e){}
    var button = $("theme-toggle");
    if(button){ button.innerHTML = icon(theme === "dark" ? "sun" : "moon") + '<span>' + (theme === "dark" ? "Light Mode" : "Dark Mode") + '</span>'; }
  }
  function currentTheme(){
    return document.documentElement.dataset.theme === "light" ? "light" : "dark";
  }
  function setText(id, value){
    var node = $(id);
    if(node){ node.textContent = value; }
  }
  function managerLabel(value){
    var labels = {
      choco: "Chocolatey",
      winget: "winget",
      store: "Store"
    };
    return labels[value] || value;
  }
  function backendLabel(value){
    var labels = {
      "appx-inventory": "AppX inventory",
      "store-cli": "Store CLI",
      "store-cli-resolved": "Store resolved",
      "winget-msstore-fallback": "winget Store fallback"
    };
    return labels[value] || value;
  }
  function sourceLabel(value){
    var labels = {
      winget: "winget repository",
      msstore: "Microsoft Store",
      "store-cli": "Store CLI",
      appx: "AppX inventory",
      choco: "Chocolatey sources"
    };
    return labels[value] || value || "Unknown source";
  }
  function executionBackendLabel(pkg){
    pkg = pkg || {};
    if(pkg.action_backend){ return backendLabel(pkg.action_backend); }
    if(pkg.manager === "store" && pkg.source === "msstore"){ return backendLabel("winget-msstore-fallback"); }
    return managerLabel(pkg.manager);
  }
  function installRouteText(pkg){
    pkg = pkg || {};
    if(pkg.manager === "store"){
      if(pkg.action_backend === "winget-msstore-fallback" || pkg.source === "msstore"){
        return "Installs through winget Store fallback.";
      }
      if(pkg.action_backend === "store-cli" || pkg.source === "store-cli"){
        return "Installs through native Store CLI.";
      }
      return "Installs through Store backend.";
    }
    return "Installs through " + managerLabel(pkg.manager) + ".";
  }
  function allowUnknownVersionUpdates(){
    var control = $("update-allow-unknown");
    return !!control && !!control.checked;
  }
  function allowPinnedUpdates(){
    var control = $("update-allow-pinned");
    return !!control && !!control.checked;
  }
  function appendGlobalUpdateOptions(params){
    params.delete("allow_unknown_version");
    params.delete("allow_pinned");
    if(allowUnknownVersionUpdates()){ params.set("allow_unknown_version", "true"); }
    if(allowPinnedUpdates()){ params.set("allow_pinned", "true"); }
    return params;
  }
  function packageAutoUpdateable(pkg){
    return pkg.update_supported !== false && packageHasExactStoreTarget(pkg) && packageHasFreshStoreAssessment(pkg) && !pkg.unknown_version && !pkg.pinned;
  }
  function packageBulkUpdateable(pkg){
    var options = arguments.length > 1 && arguments[1] ? arguments[1] : {allowUnknown:allowUnknownVersionUpdates(), allowPinned:allowPinnedUpdates()};
    return packageHasUpdate(pkg) &&
      pkg.update_supported !== false &&
      packageHasExactStoreTarget(pkg) &&
      packageHasFreshStoreAssessment(pkg) &&
      (!pkg.unknown_version || options.allowUnknown) &&
      (!pkg.pinned || options.allowPinned);
  }
  function pagedItems(items, currentPage, pageSize){
    var total = items.length;
    var totalPages = Math.max(1, Math.ceil(total / pageSize));
    var page = Math.min(Math.max(currentPage, 1), totalPages);
    var start = (page - 1) * pageSize;
    return {
      page: page,
      total: total,
      totalPages: totalPages,
      start: start,
      end: Math.min(start + pageSize, total),
      items: items.slice(start, start + pageSize)
    };
  }
  function renderPager(page, status, prev, next, suffix){
    if(status){ status.textContent = "Showing " + (page.start + 1) + "-" + page.end + " of " + page.total + (suffix || ""); }
    if(prev){ prev.disabled = page.page <= 1; }
    if(next){ next.disabled = page.page >= page.totalPages; }
  }
  function renderEmptyPager(status, statusHTML, prev, next){
    if(status){ status.innerHTML = statusHTML; }
    if(prev){ prev.disabled = true; }
    if(next){ next.disabled = true; }
  }

  function storeAssessmentActive(pkg){
    return pkg && pkg.manager === "store" && !!pkg.update_state;
  }
  function storeUpdateState(pkg){
    if(storeAssessmentActive(pkg)){ return String(pkg.update_state || "unknown").toLowerCase(); }
    if(pkg && pkg.manager === "store"){
      if(pkg.update_available){ return "available"; }
      return "unknown";
    }
    if(pkg && pkg.update_available){ return "available"; }
    return "current";
  }
  function stateLabel(state){
    state = String(state || "unknown").toLowerCase();
    if(state === "inapplicable"){ return "Inapplicable"; }
    return state.charAt(0).toUpperCase() + state.slice(1);
  }
  function stateBadge(pkg){
    var state = storeUpdateState(pkg);
    var className = "state-" + (pkg && pkg.stale ? "stale" : state);
    var label = pkg && pkg.stale ? "Stale" : stateLabel(state);
    if(storeAssessmentActive(pkg) && state === "available" && !pkg.stale && !packageHasExactStoreTarget(pkg)){
      className = "state-unknown";
      label = "Target unavailable";
    }
    return '<span class="badge state-badge ' + attr(className) + '">' + html(label) + '</span>';
  }
  function packageHasExactStoreTarget(pkg){
    if(pkg && pkg.manager === "store" && !storeAssessmentActive(pkg)){ return false; }
    if(!storeAssessmentActive(pkg)){ return true; }
    return !!pkg.exact_action_target_available && !!pkg.installed_package_family_name && !!pkg.store_product_id;
  }
  function packageHasFreshStoreAssessment(pkg){
    if(pkg && pkg.manager === "store" && !storeAssessmentActive(pkg)){ return false; }
    if(!storeAssessmentActive(pkg)){ return true; }
    return storeUpdateState(pkg) === "available" && !pkg.stale;
  }
  function packageHasUpdate(pkg){
    if(storeAssessmentActive(pkg)){ return packageHasFreshStoreAssessment(pkg) && packageHasExactStoreTarget(pkg); }
    if(pkg && pkg.manager === "store"){ return false; }
    return !!pkg.update_available;
  }
  function packageNeedsUpdateAttention(pkg){
    if(pkg && pkg.manager === "store" && !storeAssessmentActive(pkg)){ return true; }
    if(!storeAssessmentActive(pkg)){ return !!pkg.update_available; }
    return packageHasUpdate(pkg);
  }
  function packageReasonText(pkg){
    return String((pkg && pkg.update_reason) || "").trim();
  }
  function providerDiagnosticsMarkup(pkg){
    var list = providerDiagnosticsListMarkup(pkg);
    if(!list){ return ""; }
    return '<details class="diagnostic-details"><summary>Update diagnostics</summary>' + list + '</details>';
  }
  function providerDiagnosticsListMarkup(pkg){
    var summaries = (pkg && pkg.provider_summaries) || [];
    if(!packageReasonText(pkg) && summaries.length === 0){ return ""; }
    var items = [];
    if(packageReasonText(pkg)){
      items.push('<li><strong>Reason:</strong> ' + html(packageReasonText(pkg)) + '</li>');
    }
    summaries.forEach(function(summary){
      var text = (summary.name || "Provider") + " - " + (summary.health || "unknown") + " / " + (summary.kind || "evidence");
      if(summary.observed_at){ text += " at " + summary.observed_at; }
      if(summary.error){ text += " - " + summary.error; }
      items.push('<li>' + html(text) + '</li>');
    });
    return '<ul class="diagnostic-list">' + items.join("") + '</ul>';
  }
  function defaultStoreHealthCounts(){
    return {available:0,current:0,unknown:0,conflict:0,inapplicable:0,pending:0,stale:0};
  }
  function normalizedStoreHealthFromAPI(){
    var health = latestStoreScanHealth || {};
    if(!health.active){ return null; }
    var counts = defaultStoreHealthCounts();
    Object.keys(health.counts || {}).forEach(function(key){
      counts[key] = Number(health.counts[key]) || 0;
    });
    var providerIssues = packages.filter(function(pkg){
      if(!storeAssessmentActive(pkg)){ return false; }
      var state = storeUpdateState(pkg);
      return ["unknown","conflict","inapplicable","pending"].indexOf(state) !== -1 || !!pkg.stale;
    });
    return {
      active:true,
      healthy:!!(health.healthy && health.authoritative),
      storePackages:packages.filter(function(pkg){ return pkg.manager === "store"; }),
      assessed:packages.filter(storeAssessmentActive),
      counts:counts,
      providerIssues:providerIssues,
      scanID:health.scan_id || "",
      observedAt:health.observed_at || "",
      status:health.status || "",
      reason:health.reason || "",
      providers:health.providers || []
    };
  }
  function storeScanHealth(){
    var apiHealth = normalizedStoreHealthFromAPI();
    if(apiHealth){ return apiHealth; }
    var storePackages = packages.filter(function(pkg){ return pkg.manager === "store"; });
    var assessed = storePackages.filter(storeAssessmentActive);
    var counts = defaultStoreHealthCounts();
    var providerIssues = [];
    assessed.forEach(function(pkg){
      var state = storeUpdateState(pkg);
      if(counts[state] == null){ counts.unknown++; } else { counts[state]++; }
      if(pkg.stale){ counts.stale++; }
      if(["unknown","conflict","inapplicable","pending"].indexOf(state) !== -1 || pkg.stale){
        providerIssues.push(pkg);
      }
    });
    if(storePackages.length > 0 && assessed.length === 0){
      counts.unknown = storePackages.length;
      providerIssues = storePackages.slice();
    }
    var active = storePackages.length > 0 || assessed.length > 0;
    var healthy = active && counts.unknown === 0 && counts.conflict === 0 && counts.inapplicable === 0 && counts.stale === 0;
    return {active:active, healthy:healthy, storePackages:storePackages, assessed:assessed, counts:counts, providerIssues:providerIssues, providers:[]};
  }
  function storeCoverageHealthy(){
    var health = storeScanHealth();
    return !health.active || health.healthy;
  }
  function updateModalOpenState(){
    var openModal = document.querySelector(".modal:not(.hidden)");
    document.body.classList.toggle("modal-open", !!openModal);
  }
  function openStoreStatusModal(){
    var modal = $("store-status-modal");
    if(!modal){ return; }
    renderStoreScanHealth();
    modal.classList.remove("hidden");
    updateModalOpenState();
    var closeButton = $("store-status-close");
    if(closeButton){ closeButton.focus(); }
  }
  function closeStoreStatusModal(){
    var modal = $("store-status-modal");
    if(!modal){ return; }
    modal.classList.add("hidden");
    updateModalOpenState();
  }
  function packageByKey(key){
    key = String(key || "");
    for(var i = 0; i < packages.length; i++){
      if(packages[i] && String(packages[i].key || "") === key){ return packages[i]; }
    }
    return null;
  }
  function packageDiagnosticsButton(pkg){
    if(!storeAssessmentActive(pkg) || (!packageReasonText(pkg) && !(pkg.provider_summaries || []).length)){ return ""; }
    return '<button class="ghost diagnostics-button" type="button" data-package-diagnostics-open data-key="' + attr(pkg.key) + '" aria-label="Show update diagnostics for ' + attr(pkg.name || pkg.id || "package") + '">' + icon("alert") + '<span>Diagnostics</span></button>';
  }
  function packageDiagnosticField(label, value){
    value = String(value || "").trim();
    if(!value){ return ""; }
    return '<div class="diagnostic-field"><span>' + html(label) + '</span>' + html(value) + '</div>';
  }
  function openPackageDiagnosticsModal(key){
    var pkg = packageByKey(key);
    var modal = $("package-diagnostics-modal");
    var title = $("package-diagnostics-modal-title");
    var body = $("package-diagnostics-body");
    if(!pkg || !modal || !body){ return; }
    if(title){ title.textContent = pkg.name || pkg.id || "Package diagnostics"; }
    var fields = [
      packageDiagnosticField("Manager", managerLabel(pkg.manager)),
      packageDiagnosticField("Backend", backendLabel(pkg.action_backend || pkg.source || pkg.manager)),
      packageDiagnosticField("Package family", pkg.installed_package_family_name),
      packageDiagnosticField("Product ID", pkg.store_product_id),
      packageDiagnosticField("Scan ID", pkg.scan_id),
      packageDiagnosticField("Observed", pkg.observed_at)
    ].join("");
    body.innerHTML =
      '<div class="health-summary">' + stateBadge(pkg) + '<strong>' + html(packageReasonText(pkg) || stateLabel(storeUpdateState(pkg))) + '</strong></div>' +
      (fields ? '<div class="diagnostic-grid">' + fields + '</div>' : '') +
      (providerDiagnosticsListMarkup(pkg) || '<p class="muted">No provider diagnostics are attached to this package.</p>');
    modal.classList.remove("hidden");
    updateModalOpenState();
    var closeButton = $("package-diagnostics-close");
    if(closeButton){ closeButton.focus(); }
  }
  function closePackageDiagnosticsModal(){
    var modal = $("package-diagnostics-modal");
    if(!modal){ return; }
    modal.classList.add("hidden");
    updateModalOpenState();
  }
  function renderStoreScanHealth(){
    var target = $("store-scan-health-body");
    var summary = $("store-scan-health-summary");
    if(!target){ return; }
    function setStoreHealthSummary(markup){
      if(summary){ summary.innerHTML = markup; }
    }
    function storeManagerDetailsMarkup(){
      var manager = latestStatus && latestStatus.managers ? latestStatus.managers.store : null;
      if(!manager){ return ""; }
      var details = [];
      if(manager.inventory_available){
        details.push("Store apps detected via " + (manager.inventory_backend || "AppX") + " inventory.");
      }
      if(!manager.available && manager.action_backend === "winget-msstore-fallback"){
        details.push("Store installs and updates can fall back to winget for compatible Store IDs.");
      }
      if(manager.path){
        details.push("Store CLI path: " + manager.path);
      }
      if(details.length === 0){ return ""; }
      return '<div class="health-summary manager-status-details"><span class="badge">Inventory</span><span>' + html(details.join(" ")) + '</span></div>';
    }
    if(latestPackagesLoading){
      setStoreHealthSummary(loadingText("Checking Store status..."));
      target.innerHTML = loadingText("Checking Store coverage...");
      return;
    }
    var health = storeScanHealth();
    if(!health.active){
      setStoreHealthSummary('<span class="badge state-unknown">Legacy</span><span>Detailed Store status unavailable</span>');
      target.innerHTML = storeManagerDetailsMarkup() + '<div class="health-summary"><span class="badge state-unknown">Legacy Store detector</span><span class="muted">New Store assessment fields are disabled.</span></div>';
      return;
    }
    var title = health.healthy ? "Store scan is complete enough to report Current." : "Store update status needs attention.";
    var badge = health.healthy ? '<span class="badge state-current">Healthy</span>' : '<span class="badge state-unknown">Not authoritative</span>';
    var summaryText = health.healthy ? "Coverage OK" : "Needs review";
    setStoreHealthSummary(badge + '<span>' + html(summaryText) + '</span>');
    var metrics = ["available","current","unknown","conflict","inapplicable","pending","stale"].map(function(key){
      return '<span class="badge state-' + attr(key) + '">' + html(stateLabel(key)) + ': ' + html(health.counts[key] || 0) + '</span>';
    }).join("");
    var details = [];
    if(health.reason){
      details.push('<li><strong>Summary:</strong> ' + html(health.reason) + '</li>');
    }
    if(health.scanID || health.observedAt || health.status){
      details.push('<li><strong>Scan:</strong> ' + html([health.scanID, health.status, health.observedAt].filter(Boolean).join(" - ")) + '</li>');
    }
    (health.providers || []).forEach(function(provider){
      var text = (provider.name || "Provider") + " - " + (provider.health || "unknown") + " / " + (provider.kind || "provider_run");
      if(provider.observed_at){ text += " at " + provider.observed_at; }
      if(provider.error){ text += " - " + provider.error; }
      details.push('<li>' + html(text) + '</li>');
    });
    health.providerIssues.slice(0, 20).forEach(function(pkg){
      details.push('<li><strong>' + html(pkg.name || pkg.id || "Store package") + ':</strong> ' + html(packageReasonText(pkg) || storeUpdateState(pkg)) + providerDiagnosticsMarkup(pkg) + '</li>');
    });
    target.innerHTML = storeManagerDetailsMarkup() + '<div class="health-summary">' + badge + '<strong>' + html(title) + '</strong></div><div class="health-metrics">' + metrics + '</div>' + (details.length ? '<details class="diagnostic-details"><summary>Provider diagnostics</summary><ul class="diagnostic-list">' + details.join("") + '</ul></details>' : '');
  }

  function renderDashboardSummary(){
    var managerMap = latestStatus && latestStatus.managers ? latestStatus.managers : {};
    var managerNames = Object.keys(managerMap);
    var availableManagers = managerNames.filter(function(name){ return managerMap[name] && managerMap[name].available; }).length;
    var updates = packages.filter(packageHasUpdate);
    var supportedUpdates = updates.filter(packageBulkUpdateable);
    var updateablePackages = packages.filter(packageAutoUpdateable);
    var inventoryOnly = packages.filter(function(pkg){ return pkg.update_supported === false; }).length;
    var statusLoading = !!(latestStatus && latestStatus.loading);
    var loading = latestPackagesLoading || statusLoading;

    setText("summary-updates", loading ? "-" : String(updates.length));
    var updatesDetail = $("summary-updates-detail");
    if(updatesDetail){
      if(loading){ updatesDetail.innerHTML = loadingText("Checking package status"); }
      else if(!storeCoverageHealthy()){ updatesDetail.innerHTML = html(supportedUpdates.length + " updateable; Store status not authoritative"); }
      else{ updatesDetail.innerHTML = html(supportedUpdates.length + " updateable"); }
    }
    setText("summary-packages", loading ? "-" : String(packages.length));
    var packagesDetail = $("summary-packages-detail");
    if(packagesDetail){ packagesDetail.innerHTML = loading ? loadingText("Inventory loading") : html(updateablePackages.length + " managed, " + inventoryOnly + " inventory-only"); }
    setText("summary-managers", statusLoading ? "-" : availableManagers + "/" + managerNames.length);
    var managersDetail = $("summary-managers-detail");
    if(managersDetail){ managersDetail.innerHTML = statusLoading ? loadingText("Checking tools") : "Available package managers"; }
    var startupEnabled = !!(latestStatus && latestStatus.startup_enabled);
    var autoTaskEnabled = !!(latestStatus && latestStatus.auto_task_enabled);
    setText("summary-automation", statusLoading ? "-" : ((startupEnabled || autoTaskEnabled) ? "On" : "Off"));
    var automationDetail = $("summary-automation-detail");
    if(automationDetail){ automationDetail.innerHTML = statusLoading ? loadingText("Loading tasks") : html("Startup " + (startupEnabled ? "on" : "off") + ", daily updates " + (autoTaskEnabled ? "on" : "off")); }
  }
  function renderManagers(data){
    var target = $("manager-list");
    if(!target){ return; }
    var managers = data.managers || {};
    var names = Object.keys(managers).sort();
    if(names.length === 0){
      if(managersRendered){ return; }
      var placeholder = '<p class="muted">' + (data.loading ? loadingText('Checking package managers...') : 'No package manager status yet.') + '</p>';
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
        return "";
      }
      if(manager.inventory_available){ details.push('<span class="badge ok">Inventory available</span>'); }
      return details.length ? '<div class="manager-extra">' + details.join("") + '</div>' : "";
    }
    function managerAvailabilityMarkup(name, manager){
      var badge = '<span class="badge ' + (manager.available ? 'ok' : 'error') + '">' + html(manager.available ? managerAvailabilityText(name, manager) : 'Missing') + '</span>';
      if(name !== "store"){ return badge; }
      return '<div class="manager-availability"><button class="ghost manager-details-button" type="button" data-store-status-open>Details</button>' + badge + '</div>';
    }
    var markup = names.map(function(name){
      var manager = managers[name] || {};
      var details = managerDisplayDetails(name, manager);
      var availability = managerAvailabilityMarkup(name, manager);
      if(manager.available){
        return '<div class="manager manager-ok"><div class="manager-main"><span class="manager-dot">' + icon("check") + '</span><div><strong>' + html(managerLabel(name)) + '</strong><span class="muted">' + html(manager.path || '') + '</span></div></div>' + availability + details + '</div>';
      }
      return '<div class="manager manager-missing"><div class="manager-main"><span class="manager-dot">' + icon("alert") + '</span><div><strong>' + html(managerLabel(name)) + '</strong><span class="muted">' + html(manager.error || '') + '</span></div></div>' + availability + details + '<form class="manager-install-form" method="post" action="/api/managers/install"><input type="hidden" name="manager" value="' + attr(name) + '"><button type="submit">' + icon("install") + '<span>Install ' + html(managerLabel(name)) + '</span></button></form></div>';
    }).join("");
    if(target.innerHTML !== markup){ target.innerHTML = markup; }
  }
  function renderStatus(data){
    latestStatus = data || {};
    renderManagers(data);
    var startup = $("startup-toggle");
    if(startup){
      startup.disabled = !!data.loading;
      startup.dataset.enabled = data.startup_enabled ? "true" : "false";
      startup.setAttribute("aria-pressed", data.startup_enabled ? "true" : "false");
      startup.innerHTML = data.loading ? loadingText("Checking startup...") : icon("refresh") + '<span>' + (data.startup_enabled ? "Disable Start With Windows" : "Enable Start With Windows") + '</span>';
    }
    var auto = $("auto-global-toggle");
    var globalEnabled = !!(data.settings && data.settings.auto_update_global);
    if(auto){
      auto.disabled = !!data.loading;
      auto.dataset.enabled = globalEnabled ? "true" : "false";
      auto.setAttribute("aria-pressed", globalEnabled ? "true" : "false");
      auto.innerHTML = data.loading ? loadingText("Checking auto-update...") : icon("update") + '<span>' + (globalEnabled ? "Disable Daily Auto-Update" : "Enable Daily Auto-Update") + '</span>';
    }
    var status = $("automation-status");
    if(status){
      status.innerHTML = data.loading ? loadingText("Loading task status...") : html("Startup task: " + (data.startup_enabled ? "enabled" : "disabled") + " - Daily update task: " + (data.auto_task_enabled ? "enabled" : "disabled"));
    }
    renderDashboardSummary();
  }


  function packageNameCell(pkg){
    var secondary = pkg.action_backend === "appx-inventory" ? "Store app" : pkg.id;
    if(pkg.unknown_version){
      secondary += " - unknown installed version";
    }
    if(pkg.pinned){
      secondary += " - pinned";
    }
    return '<strong>' + html(pkg.name || "Store app") + '</strong><br><span class="muted">' + html(secondary) + '</span>';
  }
	function managerCell(pkg){
		var backend = pkg.action_backend ? '<br><span class="muted">' + html(backendLabel(pkg.action_backend)) + '</span>' : '';
		return '<span class="badge manager-badge">' + html(managerLabel(pkg.manager)) + '</span>' + backend;
	}
  function autoButton(pkg){
    if(pkg.update_supported === false || !packageHasExactStoreTarget(pkg)){
      return '<span class="muted">N/A</span>';
    }
    if(pkg.unknown_version || pkg.pinned){
      return '<span class="muted">Explicit only</span>';
    }
    return '<button class="auto-package toggle-button" type="button" data-key="' + attr(pkg.key) + '" data-package-name="' + attr(pkg.name) + '" data-enabled="' + (pkg.auto_update ? 'true' : 'false') + '" aria-pressed="' + (pkg.auto_update ? 'true' : 'false') + '" aria-label="Auto-update for ' + attr(pkg.name) + '"' + (updateBusy ? ' disabled' : '') + '><span>' + (pkg.auto_update ? 'On' : 'Off') + '</span></button>';
  }
  function packageAvailableCell(pkg, options){
    options = options || {};
    var showStatusBadge = options.statusBadge !== false;
    var compact = options.compact === true;
    function withDiagnostics(content){
      var diagnostics = options.diagnostics ? packageDiagnosticsButton(pkg) : "";
      if(!diagnostics){ return content; }
      return '<div class="available-cell">' + content + diagnostics + '</div>';
    }
    function withOptionalBadge(text, muted){
      var content = muted ? '<span class="muted">' + html(text) + '</span>' : html(text);
      if(!showStatusBadge){
        return withDiagnostics(content);
      }
      return withDiagnostics(stateBadge(pkg) + '<br>' + content);
    }
    if(storeAssessmentActive(pkg)){
      var state = storeUpdateState(pkg);
      var offered = pkg.offered_version || pkg.available_version || "";
      var text = "";
      if(pkg.stale){
        text = "Stale evidence";
      }else if(state === "available" && !packageHasExactStoreTarget(pkg)){
        text = "Exact target unavailable";
      }else if(state === "available"){
        text = offered ? offered : "Update available";
      }else if(state === "pending"){
        text = offered ? offered + " pending" : "Pending verification";
      }else if(state === "inapplicable"){
        text = "Not applicable";
      }else if(state === "conflict"){
        text = "Provider conflict";
      }else if(state === "unknown"){
        text = "Unknown";
      }else if(state === "current"){
        text = "Current";
      }else{
        text = stateLabel(state);
      }
      if(compact && (state === "current" || state === "unknown")){
        return withDiagnostics('<span class="muted">-</span>');
      }
      return withOptionalBadge(text, state !== "available" || pkg.stale || !packageHasExactStoreTarget(pkg));
    }
    if(pkg.manager === "store"){
      if(compact){
        return withDiagnostics('<span class="muted">-</span>');
      }
      return withOptionalBadge("Unknown", true);
    }
    var available = html(pkg.available_version);
    return withDiagnostics(available);
  }
	function updateForm(pkg){
    if(storeAssessmentActive(pkg) && !packageHasExactStoreTarget(pkg)){
      return '<span class="muted" title="Store updates require an exact verified action target">Exact target unavailable</span>';
    }
    if(storeAssessmentActive(pkg) && !packageHasFreshStoreAssessment(pkg)){
      return '<span class="muted" title="Store updates require a fresh available assessment">Rescan required</span>';
    }
		if(pkg.update_supported === false){
			return '<span class="muted">Inventory only</span>';
		}
    if(!packageHasExactStoreTarget(pkg)){
      return '<span class="muted" title="Store updates require an exact verified action target">Exact target unavailable</span>';
    }
    if(!packageHasFreshStoreAssessment(pkg)){
      return '<span class="muted" title="Store updates require a fresh available assessment">Rescan required</span>';
    }
    var blockedUnknown = pkg.unknown_version && !allowUnknownVersionUpdates();
    var blockedPinned = pkg.pinned && !allowPinnedUpdates();
    var updateState = rowUpdateState(pkg.key);
    var disabled = updateBusy || !!updateState || blockedUnknown || blockedPinned;
    var label = updateState === "active" ? "Updating" : (updateState === "queued" ? "Queued" : "Update");
    var title = blockedUnknown ? ' title="Enable the global unknown-version option first"' : (blockedPinned ? ' title="Enable the global pinned update option first"' : '');
		return '<form class="update-form" data-key="' + attr(pkg.key) + '" data-unknown-version="' + (pkg.unknown_version ? 'true' : 'false') + '" data-pinned="' + (pkg.pinned ? 'true' : 'false') + '" data-blocked-unknown="' + (blockedUnknown ? 'true' : 'false') + '" data-blocked-pinned="' + (blockedPinned ? 'true' : 'false') + '" method="post" action="/api/update"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit" aria-label="' + attr(label + " " + pkg.name) + '"' + (disabled ? ' disabled' : '') + title + '>' + icon("update") + '<span>' + html(label) + '</span></button><div class="row-progress' + (updateState ? '' : ' hidden') + '">' + progressBar((updateState ? label : "Update progress") + " for " + pkg.name) + '</div></form>';
  }
	function installedAction(pkg){
		if(packageHasUpdate(pkg) && packageHasExactStoreTarget(pkg) && packageHasFreshStoreAssessment(pkg)){
			return updateForm(pkg);
		}
		return '<span class="muted">-</span>';
	}
  function packageMatchesInstalledSearch(pkg){
    var query = installedSearchQuery.trim().toLowerCase();
    if(!query){ return true; }
    return [pkg.name, pkg.id, pkg.manager, pkg.version, pkg.available_version, pkg.update_state, pkg.update_reason, pkg.installed_package_family_name, pkg.store_product_id].some(function(value){
      return String(value || "").toLowerCase().indexOf(query) !== -1;
    });
  }
  function renderUpdatesTable(updates, loading){
    var target = $("updates-body");
    var status = $("updates-page-status");
    var prev = $("updates-prev");
    var next = $("updates-next");
    if(!target){ return; }
    if(updates.length === 0){
      var emptyText = storeCoverageHealthy() ? 'No updates available.' : 'Store update status is unknown. Review scan health.';
      target.innerHTML = loading ? loadingTableRow(7, "Checking for updates...") : '<tr><td colspan="7">' + html(emptyText) + '</td></tr>';
      renderEmptyPager(status, loading ? loadingText('Checking...') : html(storeCoverageHealthy() ? 'No updates' : 'Store status unknown'), prev, next);
      return;
    }
    var page = pagedItems(updates, updatePage, updatePageSize);
    updatePage = page.page;
    target.innerHTML = page.items.map(function(pkg){
      var selectable = packageBulkUpdateable(pkg);
      var rowClass = rowUpdateState(pkg.key) === "active" ? ' class="updating-current"' : '';
      return '<tr data-key="' + attr(pkg.key) + '"' + rowClass + '><td><input form="update-selected-form" type="checkbox" name="package_key" value="' + attr(pkg.key) + '" aria-label="Select ' + attr(pkg.name) + ' for update"' + ((updateBusy || !selectable) ? ' disabled' : '') + '></td><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + packageAvailableCell(pkg, {diagnostics:true}) + '</td><td>' + autoButton(pkg) + '</td><td>' + updateForm(pkg) + '</td></tr>';
    }).join("");
    renderPager(page, status, prev, next);
  }
  function renderInstalledTable(loading){
    var target = $("packages-body");
    var status = $("installed-page-status");
    var prev = $("installed-prev");
    var next = $("installed-next");
    if(!target){ return; }
    var visiblePackages = packages.filter(packageMatchesInstalledSearch);
    if(visiblePackages.length === 0){
		target.innerHTML = loading ? loadingTableRow(7, "Loading packages...") : '<tr><td colspan="7">' + (installedSearchQuery ? 'No packages match your filter.' : 'No managed packages found.') + '</td></tr>';
      renderEmptyPager(status, loading ? loadingText('Loading...') : html(installedSearchQuery ? 'No matches' : 'No packages'), prev, next);
      return;
    }
    var page = pagedItems(visiblePackages, installedPage, installedPageSize);
    installedPage = page.page;
	target.innerHTML = page.items.map(function(pkg){
		var rowStatus = pkg.manager === "store" ? stateBadge(pkg) : (pkg.update_supported === false ? '<span class="badge">Inventory only</span>' : ((pkg.unknown_version || pkg.pinned) && pkg.update_available ? '<span class="badge warn">Explicit update</span>' : (pkg.update_available ? '<span class="badge warn">Update</span>' : '<span class="badge ok">Current</span>')));
    var rowClass = rowUpdateState(pkg.key) === "active" ? ' class="updating-current"' : '';
		return '<tr data-key="' + attr(pkg.key) + '"' + rowClass + '><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + packageAvailableCell(pkg, {statusBadge:false, compact:true}) + '</td><td>' + rowStatus + '</td><td>' + autoButton(pkg) + '</td><td>' + installedAction(pkg) + '</td></tr>';
	}).join("");
    renderPager(page, status, prev, next, installedSearchQuery ? " matches" : "");
  }
  function renderPackageTables(){
    var updates = packages.filter(packageNeedsUpdateAttention);
    var updateablePackages = packages.filter(packageAutoUpdateable);
    var updateJobRunning = activeUpdateJobRunning();
    $("auto-all").disabled = updateBusy || updateablePackages.length === 0;
    $("auto-none").disabled = updateBusy || updateablePackages.length === 0;
    renderUpdatesTable(updates, latestPackagesLoading);
    renderInstalledTable(latestPackagesLoading);
    var supportedUpdates = updates.filter(packageBulkUpdateable);
    $("update-all-button").disabled = updateBusy || updateJobRunning || supportedUpdates.length === 0;
    $("update-selected-button").disabled = updateBusy || updateJobRunning || supportedUpdates.length === 0;
    renderDashboardSummary();
  }
  function renderPackages(data){
    renderManagers(data);
    packages = data.packages || [];
    latestStoreScanHealth = data.store_scan_health || null;
    latestPackagesLoading = !!data.loading;
    renderStoreScanHealth();
    renderPackageTables();
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
    searchResults = data.packages || [];
    searchMetadata = data || {};
    searchPage = 1;
    renderSearchProvenance(data || {});
    renderSearchTable();
  }
  function searchCommandResult(data, manager){
    var results = data.command_results || {};
    if(manager === "store"){
      return results.store || results.store_search || null;
    }
    return results[manager] || null;
  }
  function uniqueSearchManagers(packages){
    var seen = {};
    var names = [];
    (packages || []).forEach(function(pkg){
      var manager = pkg.manager || "";
      if(!manager || seen[manager]){ return; }
      seen[manager] = true;
      names.push(manager);
    });
    return names;
  }
  function searchOriginLabel(pkg){
    var source = sourceLabel(pkg.source || pkg.manager);
    var backend = executionBackendLabel(pkg);
    return source === backend ? source : source + " via " + backend;
  }
  function uniqueSearchOrigins(packages){
    var seen = {};
    var names = [];
    (packages || []).forEach(function(pkg){
      var label = searchOriginLabel(pkg || {});
      if(!label || seen[label]){ return; }
      seen[label] = true;
      names.push(label);
    });
    return names;
  }
  function searchShownPhrase(packages){
    var origins = uniqueSearchOrigins(packages);
    if(origins.length === 0){ return "no package results are shown"; }
    return "results from " + origins.join(", ") + " are shown";
  }
  function searchManagerLabel(manager){
    if(manager === "store"){ return "Store CLI"; }
    return managerLabel(manager);
  }
  function searchFailureReason(result, managerStatus){
    if(result){
      var reason = firstMeaningfulOutputLine(result.stderr) || firstMeaningfulOutputLine(result.stdout);
      if(reason){ return " Code " + (result.code || 0) + ": " + truncateNoticeText(reason, 100); }
      return " Code " + (result.code || 0) + ".";
    }
    if(managerStatus && managerStatus.error){
      return " " + truncateNoticeText(managerStatus.error, 120);
    }
    return "";
  }
  function renderSearchProvenance(data){
    var target = $("search-provenance");
    if(!target){ return; }
    var packages = data.packages || [];
    var managers = data.managers || {};
    var shown = searchShownPhrase(packages);
    var messages = [];
    ["winget","store","choco"].forEach(function(manager){
      var status = managers[manager] || {};
      var result = searchCommandResult(data, manager);
      if(status.available === false){
        messages.push(searchManagerLabel(manager) + " unavailable;" + searchFailureReason(null, status) + " " + shown + ".");
        return;
      }
      if(result && !result.ok){
        messages.push(searchManagerLabel(manager) + " search failed;" + searchFailureReason(result, status) + " " + shown + ".");
      }
    });
    var sourceOrigins = uniqueSearchOrigins(packages).join(", ");
    if(sourceOrigins){
      messages.unshift("Showing " + packages.length + " result(s) from " + sourceOrigins + ".");
    }
    if(messages.length === 0){
      target.classList.add("hidden");
      target.innerHTML = "";
      return;
    }
    target.classList.remove("hidden");
    target.innerHTML = messages.map(function(message){
      return '<div class="provenance-item">' + html(message) + '</div>';
    }).join("");
  }
  function searchMatchCell(pkg){
    var reason = pkg.match_reason || "";
    if(!reason && pkg.match){ reason = "Matched " + pkg.match + "."; }
    if(!reason){ reason = "Returned by " + managerLabel(pkg.manager) + " search."; }
    var raw = pkg.match ? '<br><span class="muted">Raw match: ' + html(pkg.match) + '</span>' : '';
    return html(reason) + raw;
  }
  function searchManagerBackendCell(pkg){
    return '<span class="badge manager-badge">' + html(managerLabel(pkg.manager)) + '</span><br><span class="muted">Backend: ' + html(executionBackendLabel(pkg)) + '</span>';
  }
  function searchActionCell(pkg){
    return '<form class="install-form" method="post" action="/api/install" data-backend-label="' + attr(executionBackendLabel(pkg)) + '"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit" aria-label="Install ' + attr(pkg.name || pkg.id) + '">' + icon("install") + '<span>Install</span></button><span class="muted install-route">' + html(installRouteText(pkg)) + '</span></form>';
  }
  function renderSearchTable(){
    var body = $("search-results-body");
    var status = $("search-page-status");
    var prev = $("search-prev");
    var next = $("search-next");
    if(!body){ return; }
    if(searchResults.length === 0){
      body.innerHTML = '<tr><td colspan="7">No installable results.</td></tr>';
      renderEmptyPager(status, html("No results"), prev, next);
      return;
    }
    var page = pagedItems(searchResults, searchPage, searchPageSize);
    searchPage = page.page;
    body.innerHTML = page.items.map(function(pkg){
      return '<tr><td><strong>' + html(pkg.name) + '</strong></td><td>' + html(sourceLabel(pkg.source || pkg.manager)) + '</td><td>' + searchManagerBackendCell(pkg) + '</td><td><code class="package-id">' + html(pkg.id) + '</code></td><td>' + searchMatchCell(pkg) + '</td><td>' + html(pkg.version || "") + '</td><td>' + searchActionCell(pkg) + '</td></tr>';
    }).join("");
    renderPager(page, status, prev, next);
  }


  function abortControllerOrNull(){
    return window.AbortController ? new AbortController() : null;
  }
  function scheduleStatusLoad(force, delay){
    if(statusPollTimer){ clearTimeout(statusPollTimer); }
    statusPollTimer = setTimeout(function(){
      statusPollTimer = null;
      if(document.hidden){
        scheduleStatusLoad(force, Math.min(15000, delay * 2));
        return;
      }
      loadStatus(force);
    }, delay);
  }
  function schedulePackageLoad(force, delay){
    if(packagePollTimer){ clearTimeout(packagePollTimer); }
    packagePollTimer = setTimeout(function(){
      packagePollTimer = null;
      if(document.hidden){
        schedulePackageLoad(force, Math.min(15000, delay * 2));
        return;
      }
      loadPackages(force);
    }, delay);
  }
  async function loadStatus(force){
    var seq = ++statusRequestSeq;
    if(statusController){ statusController.abort(); }
    statusController = abortControllerOrNull();
    try{
      var options = statusController ? {signal:statusController.signal} : {};
      var response = force ? await postForm("/api/status/refresh", {}) : await fetch(api("/api/status"), options);
      var data = await response.json();
      if(seq !== statusRequestSeq){ return null; }
      renderStatus(data);
      statusPollDelay = data.loading ? Math.min(15000, Math.round(statusPollDelay * 1.35)) : 800;
      if(data.loading){ scheduleStatusLoad(false, statusPollDelay); }
      return data;
    }catch(e){
      if(e && e.name === "AbortError"){ return null; }
      if(seq !== statusRequestSeq){ return null; }
      statusPollDelay = Math.min(15000, Math.round(statusPollDelay * 1.6));
      showNotice("Could not load status: " + e.message);
      scheduleStatusLoad(false, statusPollDelay);
    }
  }
  async function loadPackages(force){
    var seq = ++packageRequestSeq;
    if(packageController){ packageController.abort(); }
    packageController = abortControllerOrNull();
    try{
      var options = packageController ? {signal:packageController.signal} : {};
      var data = await (await fetch(api("/api/packages", {}), options)).json();
      if(seq !== packageRequestSeq){ return null; }
      renderPackages(data);
      packagePollDelay = data.loading ? Math.min(15000, Math.round(packagePollDelay * 1.35)) : 900;
      if(data.loading){ schedulePackageLoad(false, packagePollDelay); }
      return data;
    }catch(e){
      if(e && e.name === "AbortError"){ return null; }
      if(seq !== packageRequestSeq){ return null; }
      packagePollDelay = Math.min(15000, Math.round(packagePollDelay * 1.6));
      showNotice("Could not load packages: " + e.message);
      schedulePackageLoad(false, packagePollDelay);
    }
  }
  async function refreshPackagesAfterUpdate(refreshAlreadyStarted){
    var data = await loadPackages(!refreshAlreadyStarted);
    while(data && data.loading){
      await new Promise(function(resolve){ setTimeout(resolve, 900); });
      data = await loadPackages(false);
    }
    return data;
  }
  async function startInventoryRefresh(){
    showNotice("Starting package status refresh...", true);
    var response = await postForm("/api/inventory/refresh", {});
    var payload = await response.json();
    if(!response.ok){ throw new Error(payload.error || "Could not start inventory refresh"); }
    await waitForJob(payload.job_id, function(status){
      showNotice(status.notice || "Refreshing package status...", true);
    });
    showNotice("Package status refreshed.");
    return loadPackages(false);
  }
  async function refreshStatusAfterManagerInstall(){
    var data = await loadStatus(true);
    while(data && data.loading){
      await new Promise(function(resolve){ setTimeout(resolve, 800); });
      data = await loadStatus(false);
    }
    return data;
  }
  function jobComplete(status){
    return status && !status.running && ["queued","starting","running","accepted","verifying","refreshing"].indexOf(status.state) === -1;
  }
  function waitUntilVisible(){
    if(!document.hidden){ return Promise.resolve(); }
    return new Promise(function(resolve){
      function onVisible(){
        if(document.hidden){ return; }
        document.removeEventListener("visibilitychange", onVisible);
        resolve();
      }
      document.addEventListener("visibilitychange", onVisible);
    });
  }
  async function waitForJob(jobID, onStatus){
    var status = null;
    var delay = 500;
    while(jobID){
      await waitUntilVisible();
      var controller = abortControllerOrNull();
      var options = controller ? {signal:controller.signal} : {};
      var abortIfHidden = null;
      if(controller){
        abortIfHidden = function(){ if(document.hidden){ controller.abort(); } };
        document.addEventListener("visibilitychange", abortIfHidden);
      }
      var response;
      try{
        response = await fetch(api("/api/jobs/status", {job_id:jobID}), options);
      }catch(e){
        if(abortIfHidden){ document.removeEventListener("visibilitychange", abortIfHidden); }
        if(e && e.name === "AbortError"){ continue; }
        throw e;
      }
      if(abortIfHidden){ document.removeEventListener("visibilitychange", abortIfHidden); }
      status = await response.json();
      if(!response.ok){ throw new Error(status.error || "Could not read job status"); }
      if(onStatus){ onStatus(status); }
      if(jobComplete(status)){ return status; }
      await new Promise(function(resolve){ setTimeout(resolve, delay); });
      delay = Math.min(2000, Math.round(delay * 1.35));
    }
    return status;
  }
  function jobSucceeded(status){
    if(!status){ return false; }
    if(status.state === "succeeded"){ return true; }
    if(status.result){ return !!status.result.ok; }
    if(status.results && status.results.length){ return status.results.every(function(item){ return item.result && item.result.ok; }); }
    return false;
  }
  function jobIsUpdate(status){
    return status && (status.type === "update" || status.type === "update-all");
  }
  function activeServerJobs(){
    return serverJobs.filter(function(job){ return !jobComplete(job); });
  }
  function activeUpdateJobs(){
    return activeServerJobs().filter(jobIsUpdate);
  }
  function upsertServerJob(job){
    if(!job || !job.job_id){ return; }
    var replaced = false;
    serverJobs = serverJobs.map(function(existing){
      if(existing.job_id === job.job_id){
        replaced = true;
        return job;
      }
      return existing;
    });
    if(!replaced){ serverJobs.push(job); }
    reconcileJobs(serverJobs);
  }
  function reconcileJobs(jobs){
    serverJobs = (jobs || []).slice();
    var activeUpdates = activeUpdateJobs();
    activeUpdateKeys = [];
    activeUpdates.forEach(function(job){
      updateJobPackageKeys(job).forEach(function(key){
        if(activeUpdateKeys.indexOf(key) === -1){ activeUpdateKeys.push(key); }
      });
    });
    var bulkJob = null;
    for(var i = activeUpdates.length - 1; i >= 0; i--){
      if(activeUpdates[i].type === "update-all"){
        bulkJob = activeUpdates[i];
        break;
      }
    }
    if(bulkJob){
      activeUpdateJobID = bulkJob.job_id || "";
      renderUpdateJobStatus(bulkJob);
    }else if(activeUpdates.length > 0){
      var current = activeUpdates[0];
      var message = current.state === "queued" ? "Queued update: " + (current.current_package || packageNameForKey((current.package_keys || [])[0]) || "package") : updateJobMessage(current);
      setUpdateBusy(false, activeUpdateKeys, current.current_key || "");
      setGlobalProgress(true, message, false);
      showNotice("");
    }else if(!updateBusy){
      activeUpdateKeys = [];
      activeUpdateJobID = "";
      setGlobalProgress(false, "", false);
    }
    reconcileAuxiliaryJobProgress(activeServerJobs().filter(function(job){ return !jobIsUpdate(job); }));
    renderPackageTables();
    renderLatestUpdateResult(serverJobs);
    reconcileCompletedJobs(serverJobs);
    jobsInitialized = true;
  }
  function reconcileAuxiliaryJobProgress(activeJobs){
    var installLike = null;
    var noticeLike = null;
    for(var i = 0; i < activeJobs.length; i++){
      var job = activeJobs[i];
      if(job.type === "install" || job.type === "manager-install"){
        installLike = job;
        break;
      }
      if(job.type === "scan" || job.type === "inventory-refresh"){
        noticeLike = job;
      }
    }
    if(installLike){
      setInstallProgress(true, installLike.notice || (installLike.type === "manager-install" ? "Installing package manager..." : "Installing package..."));
    }else{
      setInstallProgress(false);
    }
    if(noticeLike && activeUpdateJobs().length === 0){
      showNotice(noticeLike.notice || (noticeLike.type === "scan" ? "Scanning applications..." : "Refreshing package status..."), true);
    }
  }
  function reconcileCompletedJobs(jobs){
    (jobs || []).forEach(function(job){
      if(!job || !job.job_id || !jobComplete(job)){ return; }
      if(!jobsInitialized){
        completedJobIDs[job.job_id] = true;
        return;
      }
      if(completedJobIDs[job.job_id]){ return; }
      completedJobIDs[job.job_id] = true;
      if(job.type === "update" || job.type === "update-all"){
        showUpdateJobToast(job);
        loadPackages(false);
      }else if(job.type === "install"){
        showToast(jobSucceeded(job) ? "Install completed successfully." : "Install finished with errors. See Session Log for full output.", jobSucceeded(job) ? "success" : "error");
        loadPackages(false);
      }else if(job.type === "manager-install"){
        showToast(jobSucceeded(job) ? "Package manager installed and status refreshed." : "Package manager install finished with errors. See Session Log for full output.", jobSucceeded(job) ? "success" : "error");
        loadStatus(false);
        loadPackages(false);
      }else if(job.type === "scan"){
        showToast(jobSucceeded(job) ? "Application scan completed." : "Application scan completed with errors.", jobSucceeded(job) ? "success" : "error");
        if(job.scan){ renderScan(job.scan); }
        loadPackages(false);
      }else if(job.type === "inventory-refresh"){
        loadPackages(false);
      }
    });
  }
  function packageByKey(key){
    for(var i = 0; i < packages.length; i++){
      if(packages[i].key === key){ return packages[i]; }
    }
    return null;
  }
  function packageNameForKey(key){
    var pkg = packageByKey(key);
    return pkg && pkg.name ? pkg.name : key;
  }
  function rowUpdateState(key){
    for(var i = 0; i < serverJobs.length; i++){
      var job = serverJobs[i];
      if(!jobIsUpdate(job) || jobComplete(job)){ continue; }
      if(job.current_key === key){ return "active"; }
      var keys = updateJobPackageKeys(job);
      if(keys.indexOf(key) !== -1){ return "queued"; }
    }
    return "";
  }
  function activeUpdateJobRunning(){
    return activeUpdateJobs().length > 0;
  }
  async function enqueueUpdateRequest(form){
    if(updateBusy){
      showNotice("A bulk update job is already running.");
      return;
    }
    var key = form.dataset.key || "";
    if(!key){
      showNotice("Could not queue update: missing package key.");
      return;
    }
    if(rowUpdateState(key)){
      showNotice(packageNameForKey(key) + " is already queued for update.");
      showToast("Package update is already queued.", "info");
      return;
    }
    try{
      var response = await postForm("/api/update", appendGlobalUpdateOptions(new URLSearchParams(new FormData(form))));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Update failed"); }
      upsertServerJob(payload);
      showNotice("Queued update for " + packageNameForKey(key) + ".");
      showToast("Queued update for " + packageNameForKey(key) + ".", "info");
    }catch(e){
      showNotice("Update failed: " + e.message);
      showToast("Update failed: " + e.message, "error");
    }
  }


  function updateJobMessage(status){
    status = status || {};
    var mode = status.mode === "selected" ? "selected" : "all";
    if(status.state === "queued"){
      return mode === "selected" ? "Queued selected updates..." : "Queued update job...";
    }
    if(status.cancel_requested && status.running){
      return "Cancelling after current command stops...";
    }
    if(status.running){
      var name = status.current_package || "package";
      var counter = status.total ? " (" + (status.current_index || 0) + "/" + status.total + ")" : "";
      if(status.state === "starting"){ return "Starting update: " + name + counter; }
      if(status.state === "accepted"){ return "Update accepted: " + name + counter; }
      if(status.state === "verifying"){ return "Verifying update: " + name + counter; }
      return (mode === "selected" ? "Updating selected packages: " : "Updating all packages: ") + name + counter;
    }
    if(status.cancel_requested){
      return status.notice || "Update cancelled. Refreshing package status...";
    }
    return status.notice || "Update completed. Refreshing package status...";
  }
  function updateableUpdateKeys(){
    return packages.filter(packageBulkUpdateable).map(function(pkg){ return pkg.key; });
  }
  function updateOptionsFromParams(params){
    params = params || new URLSearchParams();
    return {
      allowUnknown: params.get("allow_unknown_version") === "true",
      allowPinned: params.get("allow_pinned") === "true"
    };
  }
  function updateOptionsSummary(options){
    options = options || {};
    var enabled = [];
    if(options.allowUnknown){ enabled.push("unknown-version override enabled"); }
    if(options.allowPinned){ enabled.push("pinned-package override enabled"); }
    return enabled.length ? enabled.join("; ") : "No unknown-version or pinned-package overrides enabled.";
  }
  function packageUpdateTarget(pkg){
    return pkg.offered_version || pkg.available_version || "Unknown target";
  }
  function packageUpdateSource(pkg){
    return sourceLabel(pkg.source || pkg.manager);
  }
  function packageUpdateBackend(pkg){
    return executionBackendLabel(pkg);
  }
  function packageFallbackNote(pkg){
    if(pkg.manager === "store" && (pkg.action_backend === "winget-msstore-fallback" || pkg.source === "msstore")){
      return "Store action uses winget Microsoft Store fallback.";
    }
    if(pkg.manager === "store" && (pkg.action_backend === "store-cli" || pkg.action_backend === "store-cli-resolved")){
      return "Store action uses native Store CLI.";
    }
    return "";
  }
  function packageRiskNotes(pkg, options){
    var notes = [];
    if(pkg.unknown_version){ notes.push(options.allowUnknown ? "unknown-version override" : "unknown version blocked"); }
    if(pkg.pinned){ notes.push(options.allowPinned ? "pinned override" : "pinned blocked"); }
    var fallback = packageFallbackNote(pkg);
    if(fallback){ notes.push(fallback); }
    return notes.join("; ");
  }
  function selectedKeyMap(keys){
    var map = {};
    (keys || []).forEach(function(key){ map[key] = true; });
    return map;
  }
  function exclusionReason(pkg, mode, selectedMap, options){
    if(mode === "selected" && !selectedMap[pkg.key]){ return "Not selected."; }
    if(!packageHasUpdate(pkg)){ return pkg.manager === "store" ? "Store state is " + stateLabel(storeUpdateState(pkg)) + "." : "No update available."; }
    if(pkg.update_supported === false){ return "Updates are not supported for this package."; }
    if(!packageHasExactStoreTarget(pkg)){ return "Store update requires an exact verified action target."; }
    if(pkg.unknown_version && !options.allowUnknown){ return "Unknown installed version requires the global unknown-version override."; }
    if(pkg.pinned && !options.allowPinned){ return "Pinned package requires the global pinned update override."; }
    return "";
  }
  function buildUpdatePreflight(mode, keys, params, message){
    var options = updateOptionsFromParams(params);
    var selected = selectedKeyMap(keys || []);
    var updates = packages.filter(packageNeedsUpdateAttention);
    var affected = [];
    var excluded = [];
    updates.forEach(function(pkg){
      var reason = exclusionReason(pkg, mode, selected, options);
      if(reason){
        excluded.push({pkg:pkg, reason:reason});
        return;
      }
      if(mode === "selected" && !selected[pkg.key]){ return; }
      if(packageBulkUpdateable(pkg, options)){
        affected.push(pkg);
      }
    });
    var submitParams = new URLSearchParams(params);
    if(mode === "selected"){
      submitParams.delete("package_key");
      affected.forEach(function(pkg){ submitParams.append("package_key", pkg.key); });
    }
    return {
      mode: mode,
      keys: affected.map(function(pkg){ return pkg.key; }),
      params: submitParams,
      message: message,
      options: options,
      affected: affected,
      excluded: excluded
    };
  }
  function preflightPackageRow(pkg, options){
    var notes = packageRiskNotes(pkg, options);
    return '<tr><td><strong>' + html(pkg.name || pkg.id || pkg.key) + '</strong><br><span class="muted">' + html(pkg.id || pkg.key) + '</span></td><td>' + html(packageUpdateSource(pkg)) + '</td><td>' + html(pkg.version || "Unknown") + '</td><td>' + html(packageUpdateTarget(pkg)) + '</td><td>' + html(managerLabel(pkg.manager)) + ' / ' + html(packageUpdateBackend(pkg)) + (notes ? '<br><span class="muted">' + html(notes) + '</span>' : '') + '</td></tr>';
  }
  function renderUpdatePreflight(preflight){
    var panel = $("update-preflight-panel");
    if(!panel){ return; }
    pendingBulkUpdate = preflight;
    if(!preflight || preflight.affected.length === 0){
      panel.classList.add("hidden");
      var reason = preflight && preflight.excluded.length ? "All candidate updates are excluded. Review the global overrides." : "No packages are eligible for this update.";
      showNotice(reason);
      return;
    }
    panel.classList.remove("hidden");
    setText("update-preflight-summary", (preflight.mode === "selected" ? "Update Selected" : "Update All") + " will affect " + preflight.affected.length + " package(s): " + preflight.affected.map(function(pkg){ return pkg.name || pkg.id || pkg.key; }).join(", ") + ".");
    setText("update-preflight-overrides", updateOptionsSummary(preflight.options));
    $("update-preflight-body").innerHTML = preflight.affected.map(function(pkg){ return preflightPackageRow(pkg, preflight.options); }).join("");
    var excluded = $("update-preflight-excluded");
    if(excluded){
      if(preflight.excluded.length === 0){
        excluded.innerHTML = '<p class="muted">No update candidates are excluded.</p>';
      }else{
        excluded.innerHTML = '<h3>Excluded from this operation (' + preflight.excluded.length + ')</h3><ul class="mini-list">' + preflight.excluded.map(function(item){
          var pkg = item.pkg || {};
          return '<li><strong>' + html(pkg.name || pkg.id || pkg.key) + '</strong> - ' + html(item.reason) + ' <span class="muted">' + html(packageUpdateSource(pkg)) + ' ' + html(pkg.version || "Unknown") + ' -> ' + html(packageUpdateTarget(pkg)) + '</span></li>';
        }).join("") + '</ul>';
      }
    }
  }
  function hideUpdatePreflight(){
    pendingBulkUpdate = null;
    var panel = $("update-preflight-panel");
    if(panel){ panel.classList.add("hidden"); }
  }
  function confirmPendingUpdateJob(){
    if(!pendingBulkUpdate){ return; }
    var preflight = pendingBulkUpdate;
    hideUpdatePreflight();
    startUpdateJob(preflight.params, preflight.keys, preflight.message);
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
  function renderUpdateJobStatus(status){
    applyUpdateJobPackageKeys(status);
    var message = updateJobMessage(status);
    var active = !!(status && !jobComplete(status));
    setUpdateBusy(active, activeUpdateKeys, status ? status.current_key : "");
    setGlobalProgress(active, message, active && !status.cancel_requested);
    if(active){
      showNotice("");
    }else{
      showNotice(message);
    }
  }
  function showUpdateJobToast(status){
    status = status || {};
    if(status.cancel_requested){
      showToast("Update job cancelled.", "info");
      return;
    }
    var results = status.results || [];
    var failed = results.filter(function(item){ return !(item.result && item.result.ok) && !(item.result && item.result.code === 202); });
    var unverified = results.filter(function(item){ return item.result && item.result.code === 202; });
    if(unverified.length > 0){
      showToast(unverified.length + " Store update request(s) were accepted but not verified. See Session Log for diagnostics.", "error");
      return;
    }
    if(failed.length > 0){
      showToast(failed.length + " update command(s) finished with errors. See Session Log for full output.", "error");
      return;
    }
    if(results.length > 0){
      showToast("Update job completed successfully.", "success");
    }
  }
  function updateJobPackageSnapshot(status){
    var map = {};
    (status && status.packages || []).forEach(function(pkg){
      if(pkg.key){ map[pkg.key] = pkg; }
    });
    return map;
  }
  function updateResultMap(status){
    var map = {};
    (status && status.results || []).forEach(function(item){
      if(item.key){ map[item.key] = item.result || {}; }
    });
    return map;
  }
  function updateResultStatus(status, key, result){
    if(!result){ return status && status.cancel_requested ? "skipped" : "skipped"; }
    if(result.code === 130){ return "cancelled"; }
    if(result.code === 202){ return "accepted_not_verified"; }
    return result.ok ? "succeeded" : "failed";
  }
  function updateResultText(result, statusText){
    if(!result){
      return statusText === "skipped" ? "No command was run for this package." : "No command result recorded.";
    }
    if(result.ok){ return "Command succeeded."; }
    if(result.code === 130){ return "Command cancelled."; }
    if(result.code === 202){ return "Accepted but final package state could not be verified."; }
    return commandText(result);
  }
  function updateResultRows(status){
    var packageMap = updateJobPackageSnapshot(status);
    var resultMap = updateResultMap(status);
    var keys = updateJobPackageKeys(status);
    if(keys.length === 0){
      keys = Object.keys(resultMap);
    }
    return keys.map(function(key){
      var pkg = packageMap[key] || packageByKey(key) || {key:key, id:key, name:key, manager:""};
      var result = resultMap[key] || null;
      var state = updateResultStatus(status, key, result);
      return {key:key, pkg:pkg, result:result, state:state};
    });
  }
  function renderUpdateResultPanel(status){
    var panel = $("update-results-panel");
    if(!panel || !status || !jobIsUpdate(status) || !jobComplete(status)){ return; }
    var rows = updateResultRows(status);
    if(rows.length === 0){ return; }
    lastUpdateResultJob = status;
    var counts = {succeeded:0, failed:0, skipped:0, cancelled:0, accepted_not_verified:0};
    rows.forEach(function(row){ counts[row.state] = (counts[row.state] || 0) + 1; });
    panel.classList.remove("hidden");
    setText("update-results-summary", "Succeeded " + counts.succeeded + " - Accepted not verified " + counts.accepted_not_verified + " - Failed " + counts.failed + " - Skipped " + counts.skipped + " - Cancelled " + counts.cancelled + ".");
    $("update-results-body").innerHTML = rows.map(function(row){
      var pkg = row.pkg || {};
      var notes = packageRiskNotes(pkg, {allowUnknown:!!status.allow_unknown_version, allowPinned:!!status.allow_pinned});
      return '<tr><td><span class="result-status ' + attr(row.state) + '">' + html(row.state.charAt(0).toUpperCase() + row.state.slice(1)) + '</span></td><td><strong>' + html(pkg.name || pkg.id || row.key) + '</strong><br><span class="muted">' + html(pkg.id || row.key) + '</span></td><td>' + html(packageUpdateSource(pkg)) + '</td><td>' + html(pkg.version || "Unknown") + '</td><td>' + html(packageUpdateTarget(pkg)) + '</td><td>' + html(updateResultText(row.result, row.state)) + (notes ? '<br><span class="muted">' + html(notes) + '</span>' : '') + '</td></tr>';
    }).join("");
    var retry = $("retry-failed-updates");
    if(retry){
      retry.disabled = counts.failed === 0;
      retry.dataset.jobId = status.job_id || "";
    }
  }
  function renderLatestUpdateResult(jobs){
    var latest = null;
    (jobs || []).forEach(function(job){
      if(jobIsUpdate(job) && jobComplete(job)){
        latest = job;
      }
    });
    if(latest){ renderUpdateResultPanel(latest); }
  }
  function retryFailedUpdateResults(){
    var status = lastUpdateResultJob;
    if(!status){ return; }
    var rows = updateResultRows(status).filter(function(row){ return row.state === "failed"; });
    if(rows.length === 0){
      showNotice("There are no failed update commands to retry.");
      return;
    }
    var params = new URLSearchParams();
    rows.forEach(function(row){ params.append("package_key", row.key); });
    if(status.allow_unknown_version){ params.set("allow_unknown_version", "true"); }
    if(status.allow_pinned){ params.set("allow_pinned", "true"); }
    renderUpdatePreflight(buildUpdatePreflight("selected", rows.map(function(row){ return row.key; }), params, "Retrying failed packages..."));
  }
  async function startUpdateJob(params, keys, message){
    activeUpdateKeys = keys || [];
    activeUpdateJobID = "";
    setUpdateBusy(true, activeUpdateKeys);
    setGlobalProgress(true, message || "Starting updates...", true);
    showNotice("");
    try{
      var response = await postForm("/api/update-all", params);
      var status = await response.json();
      if(!response.ok){
        if(response.status === 409 && status.running){
          activeUpdateJobID = status.job_id || "";
          upsertServerJob(status);
          return;
        }
        throw new Error(status.error || status.notice || "Could not start update job");
      }
      activeUpdateJobID = status.job_id || "";
      renderUpdateJobStatus(status);
      upsertServerJob(status);
    }catch(e){
      activeUpdateKeys = [];
      activeUpdateJobID = "";
      setUpdateBusy(false, [], "");
      setGlobalProgress(false, "", false);
      showNotice("Update failed: " + e.message);
      showToast("Update failed: " + e.message, "error");
    }
  }
  async function checkActiveUpdateJob(){
    try{
      var response = await fetch(api("/api/jobs"));
      var payload = await response.json();
      if(!response.ok){ return; }
      reconcileJobs(payload.jobs || []);
    }catch(e){}
  }
  async function cancelUpdateJob(){
    var button = $("cancel-updates-button");
    if(button){ button.disabled = true; }
    setGlobalProgress(true, "Cancelling after current command stops...", false);
    showNotice("");
    try{
      var cancelID = activeUpdateJobID;
      if(!cancelID){
        var active = activeUpdateJobs();
        cancelID = active.length ? active[0].job_id : "";
      }
      var params = new URLSearchParams();
      if(cancelID){ params.set("job_id", cancelID); }
      var response = await postForm(cancelID ? "/api/jobs/cancel" : "/api/update-all/cancel", params);
      var status = await response.json();
      if(!response.ok){ throw new Error(status.error || "Could not cancel update job"); }
      upsertServerJob(status);
    }catch(e){
      showNotice("Could not cancel updates: " + e.message);
      if(button){ button.disabled = false; }
    }
  }


  async function loadSearch(query){
    var body = $("search-results-body");
    $("search-results-panel").classList.remove("hidden");
    searchResults = [];
    searchPage = 1;
    setText("search-page-status", "Searching...");
    var prev = $("search-prev");
    var next = $("search-next");
    if(prev){ prev.disabled = true; }
    if(next){ next.disabled = true; }
    body.innerHTML = loadingTableRow(7, "Searching...");
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
    var backend = form.dataset.backendLabel || "";
    var baseMessage = backend ? "Installing package through " + backend + "..." : "Installing package...";
    setInstallProgress(true, baseMessage);
    try{
      var response = await postForm("/api/install", new URLSearchParams(new FormData(form)));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Install failed"); }
      var finalStatus = await waitForJob(payload.job_id, function(status){
        setInstallProgress(true, status.notice || baseMessage);
      });
      var result = finalStatus && finalStatus.result;
      var notice = finalStatus.notice || resultNotice("Install command completed. Refreshing package status...", "Install finished with errors", result);
      setInstallProgress(true, notice);
      await loadPackages(false);
      showNotice(notice);
      showToast(jobSucceeded(finalStatus) ? "Install completed successfully." : "Install finished with errors. See Session Log for full output.", jobSucceeded(finalStatus) ? "success" : "error");
    }catch(e){
      showNotice("Install failed: " + e.message);
      showToast("Install failed: " + e.message, "error");
    }finally{
      setInstallProgress(false);
      if(button){ button.disabled = false; }
    }
  }
  async function installManagerFromForm(form){
    var button = form.querySelector("button");
    if(button){ button.disabled = true; }
    showNotice("Opening package manager install action...", true);
    try{
      var response = await postForm("/api/managers/install", new URLSearchParams(new FormData(form)));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Package manager install failed"); }
      var finalStatus = await waitForJob(payload.job_id, function(status){
        showNotice(status.notice || "Installing package manager...", true);
      });
      var result = finalStatus && finalStatus.result;
      var notice = finalStatus.notice || resultNotice("Package manager install action completed. Refreshing manager status...", "Package manager install finished with errors", result);
      showNotice(notice, jobSucceeded(finalStatus));
      if(jobSucceeded(finalStatus)){
        await loadStatus(false);
        await loadPackages(false);
        showNotice("Package manager status refreshed.");
        showToast("Package manager installed and status refreshed.", "success");
      }else{
        loadStatus(true);
        showToast("Package manager install finished with errors. See Session Log for full output.", "error");
      }
    }catch(e){
      showNotice("Package manager install failed: " + e.message);
      showToast("Package manager install failed: " + e.message, "error");
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
      await postCommandPayload("/api/settings/auto-update", params, "Could not update auto setting");
      button.dataset.enabled = enabled ? "true" : "false";
      button.setAttribute("aria-pressed", enabled ? "true" : "false");
      button.setAttribute("aria-label", "Auto-update for " + (button.dataset.packageName || key));
      button.innerHTML = '<span>' + (enabled ? 'On' : 'Off') + '</span>';
      showNotice("Auto-update setting updated.");
      showToast("Auto-update setting updated.", "success");
    }catch(e){
      showNotice("Could not update auto setting: " + e.message);
      showToast("Could not update auto setting: " + e.message, "error");
      loadStatus(true);
      startInventoryRefresh().catch(function(){ loadPackages(false); });
    }
    button.disabled = false;
  }
  async function setAllAuto(enabled){
    var params = new URLSearchParams();
    params.set("global", enabled ? "true" : "false");
    params.set("package_enabled", enabled ? "true" : "false");
    packages.forEach(function(pkg){ if(packageAutoUpdateable(pkg)){ params.append("package_key", pkg.key); } });
    showNotice("Updating auto-update settings...", true);
    try{
      await postCommandPayload("/api/settings/auto-update", params, "Could not update auto-update settings");
      showNotice("Auto-update settings updated.");
      showToast("Auto-update settings updated.", "success");
    }catch(e){
      showNotice("Could not update auto-update settings: " + e.message);
      showToast("Could not update auto-update settings: " + e.message, "error");
    }finally{
      loadStatus(true);
      startInventoryRefresh().catch(function(){ loadPackages(false); });
    }
  }


  document.addEventListener("click", function(event){
    var openStoreStatus = event.target.closest("[data-store-status-open]");
    if(openStoreStatus){
      openStoreStatusModal();
      return;
    }
    var closeStoreStatus = event.target.closest("[data-store-status-close]");
    if(closeStoreStatus){
      closeStoreStatusModal();
      return;
    }
    var openPackageDiagnostics = event.target.closest("[data-package-diagnostics-open]");
    if(openPackageDiagnostics){
      openPackageDiagnosticsModal(openPackageDiagnostics.dataset.key);
      return;
    }
    var closePackageDiagnostics = event.target.closest("[data-package-diagnostics-close]");
    if(closePackageDiagnostics){
      closePackageDiagnosticsModal();
      return;
    }
    var toastClose = event.target.closest(".toast-close");
    if(toastClose){
      var toast = toastClose.closest(".toast");
      if(toast){ removeToast(Number(toast.dataset.toastId || 0)); }
      return;
    }
    var autoButton = event.target.closest(".auto-package");
    if(autoButton){
      setPackageAuto(autoButton.dataset.key, autoButton.dataset.enabled !== "true", autoButton);
    }
    var logTab = event.target.closest(".log-tab");
    if(logTab){
      setActiveLogCategory(logTab.dataset.logCategory || "all");
      return;
    }
  });
  document.addEventListener("keydown", function(event){
    var storeModal = $("store-status-modal");
    var packageModal = $("package-diagnostics-modal");
    if(event.key === "Escape" && packageModal && !packageModal.classList.contains("hidden")){
      closePackageDiagnosticsModal();
      return;
    }
    if(event.key === "Escape" && storeModal && !storeModal.classList.contains("hidden")){
      closeStoreStatusModal();
      return;
    }
    var tab = event.target.closest(".log-tab");
    if(!tab){ return; }
    switch(event.key){
    case "ArrowRight":
    case "ArrowDown":
      event.preventDefault();
      focusAdjacentLogTab(tab, 1);
      break;
    case "ArrowLeft":
    case "ArrowUp":
      event.preventDefault();
      focusAdjacentLogTab(tab, -1);
      break;
    case "Home":
      event.preventDefault();
      focusAdjacentLogTab(tab, "home");
      break;
    case "End":
      event.preventDefault();
      focusAdjacentLogTab(tab, "end");
      break;
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
    if(form.id === "shutdown-form"){
      event.preventDefault();
      var button = $("shutdown-button");
      if(button){ button.disabled = true; }
      showNotice("Stopping application...", true);
      postForm("/shutdown", {}).then(function(response){
        if(!response.ok){ throw new Error(response.statusText || "Stop request failed"); }
        showNotice("Application is stopping.");
      }).catch(function(e){
        if(button){ button.disabled = false; }
        showNotice("Stop failed: " + e.message);
        showToast("Stop failed: " + e.message, "error");
      });
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
      if(form.dataset.unknownVersion === "true" && !allowUnknownVersionUpdates()){
        showNotice("Enable the global unknown-version option before updating this package.");
        return;
      }
      if(form.dataset.pinned === "true" && !allowPinnedUpdates()){
        showNotice("Enable the global pinned update option before updating this package.");
        return;
      }
      enqueueUpdateRequest(form);
      return;
    }
    if(form.id === "update-selected-form"){
      event.preventDefault();
      var params = appendGlobalUpdateOptions(new URLSearchParams(new FormData(form)));
      var keys = params.getAll("package_key");
      if(keys.length === 0){
        showNotice("Select at least one package to update.");
        return;
      }
      renderUpdatePreflight(buildUpdatePreflight("selected", keys, params, "Updating selected packages..."));
      return;
    }
    if(form.matches(".update-all-form")){
      event.preventDefault();
      var allKeys = updateableUpdateKeys();
      renderUpdatePreflight(buildUpdatePreflight("all", allKeys, appendGlobalUpdateOptions(new URLSearchParams(new FormData(form))), "Updating all packages..."));
    }
  });
  $("theme-toggle").addEventListener("click", function(){
    var next = currentTheme() === "dark" ? "light" : "dark";
    setTheme(next);
    postForm("/api/settings/theme", {theme:next}).catch(function(){});
  });
  document.addEventListener("visibilitychange", function(){
    if(document.hidden){
      stopSpinnerLoop();
      pauseToastTimers();
    }else{
      startSpinnerLoop();
      resumeToastTimers();
      if(!eventStream){ startEventStream(); }
      if(latestStatus && latestStatus.loading){ loadStatus(false); }
      if(latestPackagesLoading){ loadPackages(false); }
    }
  });
  $("scan-button").addEventListener("click", async function(){
    var button = this;
    button.disabled = true;
    showNotice("Scanning applications...", true);
    try{
      var response = await postForm("/api/scan", {});
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Scan failed"); }
      var finalStatus = await waitForJob(payload.job_id, function(status){
        showNotice(status.notice || "Scanning applications...", true);
      });
      var data = finalStatus.scan;
      if(!data){ throw new Error(finalStatus.error || "Scan did not return results"); }
      renderScan(data);
      await loadPackages(false);
      if(data.errors && data.errors.length){
        showNotice("Application scan completed with errors. Review Scan Results for details.");
        showToast("Application scan completed with errors.", "error");
      }else{
        showNotice("Application scan completed.");
        showToast("Application scan completed.", "success");
      }
    }catch(e){ showNotice("Scan failed: " + e.message); showToast("Scan failed: " + e.message, "error"); }
    button.disabled = false;
  });
  $("refresh-packages").addEventListener("click", function(){
    startInventoryRefresh().catch(function(e){
      showNotice("Could not refresh package status: " + e.message);
      showToast("Could not refresh package status: " + e.message, "error");
    });
  });
  $("store-rescan-button").addEventListener("click", function(){
    startInventoryRefresh().catch(function(e){
      showNotice("Could not rescan Store status: " + e.message);
      showToast("Could not rescan Store status: " + e.message, "error");
    });
  });
  $("store-diagnostics-export-button").addEventListener("click", function(){
    exportStoreDiagnostics();
  });
  $("update-allow-unknown").addEventListener("change", function(){ renderPackageTables(); });
  $("update-allow-pinned").addEventListener("change", function(){ renderPackageTables(); });
  $("updates-prev").addEventListener("click", function(){
    updatePage--;
    renderUpdatesTable(packages.filter(packageNeedsUpdateAttention), latestPackagesLoading);
  });
  $("updates-next").addEventListener("click", function(){
    updatePage++;
    renderUpdatesTable(packages.filter(packageNeedsUpdateAttention), latestPackagesLoading);
  });
  $("search-prev").addEventListener("click", function(){
    searchPage--;
    renderSearchTable();
  });
  $("search-next").addEventListener("click", function(){
    searchPage++;
    renderSearchTable();
  });
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
  async function toggleBooleanSetting(button, path, field, successMessage, failurePrefix){
    var enabled = button.dataset.enabled !== "true";
    var params = {};
    params[field] = enabled ? "true" : "false";
    button.disabled = true;
    try{
      await postCommandPayload(path, params, failurePrefix);
      showNotice(successMessage);
      showToast(successMessage, "success");
    }catch(e){
      showNotice(failurePrefix + ": " + e.message);
      showToast(failurePrefix + ": " + e.message, "error");
    }finally{
      button.disabled = false;
      loadStatus(true);
    }
  }
  $("startup-toggle").addEventListener("click", function(){
    toggleBooleanSetting(this, "/api/settings/startup", "enabled", "Startup setting updated.", "Could not update startup setting");
  });
  $("auto-global-toggle").addEventListener("click", function(){
    toggleBooleanSetting(this, "/api/settings/auto-update", "global", "Auto-update setting updated.", "Could not update auto-update setting");
  });
  $("auto-all").addEventListener("click", function(){ setAllAuto(true); });
  $("auto-none").addEventListener("click", function(){ setAllAuto(false); });
  $("clear-log-view").addEventListener("click", function(){
    clearCurrentLogView();
  });
  $("log-search").addEventListener("input", function(){
    logSearchQuery = this.value || "";
    renderLogLines(false);
  });
  $("copy-log-view").addEventListener("click", function(){ copyLogView(); });
  $("export-log-view").addEventListener("click", function(){ exportLogs(); });
  $("cancel-updates-button").addEventListener("click", function(){ cancelUpdateJob(); });
  $("confirm-update-job").addEventListener("click", function(){ confirmPendingUpdateJob(); });
  $("cancel-update-preflight").addEventListener("click", function(){ hideUpdatePreflight(); });
  $("retry-failed-updates").addEventListener("click", function(){ retryFailedUpdateResults(); });


  setTheme(currentTheme());
  updateSpinnerPhase();
  observeSpinnerPresence();
  startSpinnerLoop();
  if(reducedMotionQuery){
    var onReducedMotionChange = function(){
      stopSpinnerLoop();
      updateSpinnerPhase();
      startSpinnerLoop();
    };
    if(reducedMotionQuery.addEventListener){
      reducedMotionQuery.addEventListener("change", onReducedMotionChange);
    }else if(reducedMotionQuery.addListener){
      reducedMotionQuery.addListener(onReducedMotionChange);
    }
  }
  loadStatus(false);
  startEventStream();
  loadPackages(false).then(function(){ checkActiveUpdateJob(); });
  loadLogs();
  var query = new URLSearchParams(window.location.search).get("q");
  if(query){
    $("search-input").value = query;
    loadSearch(query);
  }
})();


