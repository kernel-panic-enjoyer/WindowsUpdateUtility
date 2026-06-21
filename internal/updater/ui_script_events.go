package updater

const pageScriptEvents = `
  document.addEventListener("click", function(event){
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
      pauseToastTimers();
    }else{
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
  $("update-allow-unknown").addEventListener("change", function(){ renderPackageTables(); });
  $("update-allow-pinned").addEventListener("change", function(){ renderPackageTables(); });
  $("updates-prev").addEventListener("click", function(){
    updatePage--;
    renderUpdatesTable(packages.filter(function(pkg){ return !!pkg.update_available; }), latestPackagesLoading);
  });
  $("updates-next").addEventListener("click", function(){
    updatePage++;
    renderUpdatesTable(packages.filter(function(pkg){ return !!pkg.update_available; }), latestPackagesLoading);
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
`
