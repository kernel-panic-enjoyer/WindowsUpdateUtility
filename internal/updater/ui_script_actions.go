package updater

const pageScriptActions = `
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
    body.innerHTML = loadingTableRow(5, "Searching...");
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
    setInstallProgress(true, "Installing package...");
    try{
      var response = await postForm("/api/install", new URLSearchParams(new FormData(form)));
      var payload = await response.json();
      if(!response.ok){ throw new Error(payload.error || "Install failed"); }
      var notice = resultNotice("Install command completed. Refreshing package status...", "Install finished with errors", payload.result);
      setInstallProgress(true, notice);
      await refreshPackagesAfterUpdate(!!payload.refresh_started);
      showNotice(notice);
      showToast(payload.result && payload.result.ok ? "Install completed successfully." : "Install finished with errors. See Session Log for full output.", payload.result && payload.result.ok ? "success" : "error");
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
      var notice = resultNotice("Package manager install action completed. Refreshing manager status...", "Package manager install finished with errors", payload.result);
      showNotice(notice, !!(payload.result && payload.result.ok));
      if(payload.result && payload.result.ok){
        await refreshStatusAfterManagerInstall();
        await refreshPackagesAfterUpdate(!!payload.refresh_started);
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
      button.innerHTML = '<span>' + (enabled ? 'On' : 'Off') + '</span>';
      showNotice("Auto-update setting updated.");
      showToast("Auto-update setting updated.", "success");
    }catch(e){
      showNotice("Could not update auto setting: " + e.message);
      showToast("Could not update auto setting: " + e.message, "error");
      loadStatus(true);
      loadPackages(true);
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
      loadPackages(true);
    }
  }
`
