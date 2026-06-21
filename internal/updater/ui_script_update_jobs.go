package updater

const pageScriptUpdateJobs = `
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
    return pkg.available_version || "Unknown target";
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
    if(!pkg.update_available){ return "No update available."; }
    if(pkg.update_supported === false){ return "Updates are not supported for this package."; }
    if(pkg.unknown_version && !options.allowUnknown){ return "Unknown installed version requires the global unknown-version override."; }
    if(pkg.pinned && !options.allowPinned){ return "Pinned package requires the global pinned update override."; }
    return "";
  }
  function buildUpdatePreflight(mode, keys, params, message){
    var options = updateOptionsFromParams(params);
    var selected = selectedKeyMap(keys || []);
    var updates = packages.filter(function(pkg){ return !!pkg.update_available; });
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
    var failed = results.filter(function(item){ return !(item.result && item.result.ok); });
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
    return result.ok ? "succeeded" : "failed";
  }
  function updateResultText(result, statusText){
    if(!result){
      return statusText === "skipped" ? "No command was run for this package." : "No command result recorded.";
    }
    if(result.ok){ return "Command succeeded."; }
    if(result.code === 130){ return "Command cancelled."; }
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
    var counts = {succeeded:0, failed:0, skipped:0, cancelled:0};
    rows.forEach(function(row){ counts[row.state] = (counts[row.state] || 0) + 1; });
    panel.classList.remove("hidden");
    setText("update-results-summary", "Succeeded " + counts.succeeded + " - Failed " + counts.failed + " - Skipped " + counts.skipped + " - Cancelled " + counts.cancelled + ".");
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
`
