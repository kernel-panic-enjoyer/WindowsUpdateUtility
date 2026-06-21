package updater

const pageScriptDataLoading = `
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
    return status && !status.running && status.state !== "queued" && status.state !== "running" && status.state !== "refreshing";
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
`
