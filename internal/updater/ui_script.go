package updater

const pageJS = pageScriptShell +
	pageScriptLogConsole +
	pageScriptRequests +
	pageScriptThemeAndLabels +
	pageScriptStatusRender +
	pageScriptPackageRender +
	pageScriptAuxiliaryRender +
	pageScriptDataLoading +
	pageScriptUpdateJobs +
	pageScriptActions +
	pageScriptEvents +
	pageScriptBoot

const pageScriptShell = `
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
  function loadingText(message){
    return '<span class="loading-text">' + spinner() + '<span>' + html(message) + '</span></span>';
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
    if(loading && message){
      notice.innerHTML = loadingText(message);
    }else{
      notice.textContent = message || "";
    }
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
      return '<article class="toast toast-' + attr(toast.kind) + '" data-toast-id="' + attr(toast.id) + '" style="--toast-progress:' + attr(toastProgress(toast)) + '"><div><strong>' + html(toastTitle(toast.kind)) + '</strong><p>' + html(toast.message) + '</p></div><button class="toast-close ghost" type="button" aria-label="Dismiss notification">&times;</button><span class="toast-progress" aria-hidden="true"><span></span></span></article>';
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
`
