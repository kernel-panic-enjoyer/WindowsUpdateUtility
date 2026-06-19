package updater

const pageScriptUpdateJobs = `
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
    return packages.filter(packageBulkUpdateable).map(function(pkg){ return pkg.key; });
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
    if(active || !!(status && status.refresh_started)){
      showNotice("");
    }else{
      showNotice(message);
    }
  }
  async function finishUpdateJob(status){
    clearUpdateJobPoll();
    renderUpdateJobStatus(status);
    try{
      if(status && status.refresh_started){
        setGlobalProgress(true, updateJobMessage(status), false);
        await refreshPackagesAfterUpdate(true);
      }
      showUpdateJobToast(status);
    }finally{
      activeUpdateKeys = [];
      activeUpdateJobID = "";
      setUpdateBusy(false, [], "");
      setGlobalProgress(false, "", false);
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
    showNotice("");
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
      showToast("Update failed: " + e.message, "error");
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
    setGlobalProgress(true, "Cancelling after current command stops...", false);
    showNotice("");
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
`
