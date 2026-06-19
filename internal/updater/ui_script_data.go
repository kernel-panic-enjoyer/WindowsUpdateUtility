package updater

const pageScriptDataLoading = `
  async function loadStatus(force){
    try{
      var data = await (await fetch(api("/api/status", force ? {refresh:"1"} : {}))).json();
      renderStatus(data);
      if(data.loading){ setTimeout(function(){ loadStatus(false); }, 800); }
      return data;
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
  async function refreshStatusAfterManagerInstall(){
    var data = await loadStatus(true);
    while(data && data.loading){
      await new Promise(function(resolve){ setTimeout(resolve, 800); });
      data = await loadStatus(false);
    }
    return data;
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
    if(rowUpdateActive && rowUpdateActive.key === key){ return "active"; }
    for(var i = 0; i < rowUpdateQueue.length; i++){
      if(rowUpdateQueue[i].key === key){ return "queued"; }
    }
    return "";
  }
  function rowUpdateQueueActive(){
    return !!rowUpdateActive || rowUpdateQueue.length > 0;
  }
  function queuedRowUpdateKeys(){
    var keys = [];
    if(rowUpdateActive && rowUpdateActive.key){ keys.push(rowUpdateActive.key); }
    rowUpdateQueue.forEach(function(item){
      if(item && item.key){ keys.push(item.key); }
    });
    return keys;
  }
  function rowUpdateProgressMessage(){
    if(!rowUpdateActive){ return "Updating queued packages..."; }
    var name = packageNameForKey(rowUpdateActive.key);
    var queued = rowUpdateQueue.length;
    return queued > 0 ? "Updating package: " + name + " (" + queued + " queued)" : "Updating package: " + name;
  }
  function enqueueUpdateRequest(form){
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
    rowUpdateQueue.push({
      key: key,
      params: appendGlobalUpdateOptions(new URLSearchParams(new FormData(form))).toString()
    });
    showNotice("Queued update for " + packageNameForKey(key) + ".");
    showToast("Queued update for " + packageNameForKey(key) + ".", "info");
    renderPackageTables();
    processRowUpdateQueue();
  }
  async function processRowUpdateQueue(){
    if(rowUpdateActive || rowUpdateQueue.length === 0){ return; }
    rowUpdateActive = rowUpdateQueue.shift();
    var activeName = packageNameForKey(rowUpdateActive.key);
    renderPackageTables();
    setGlobalProgress(true, rowUpdateProgressMessage(), false);
    showNotice("");
    try{
      var response = await postForm("/api/update", new URLSearchParams(rowUpdateActive.params));
      var payload = await response.json();
      if(!response.ok && !payload.result && !payload.results){
        throw new Error(payload.error || "Update failed");
      }
      var updateSucceeded = updatePayloadSucceeded(payload);
      if(!updateSucceeded || !payload.refresh_started){
        showNotice(summarizeUpdatePayload(payload));
      }
      if(payload.refresh_started){
        setGlobalProgress(true, "Refreshing package status...");
        showNotice("");
        await refreshPackagesAfterUpdate(true);
      }
      if(updateSucceeded){
        showToast("Update completed: " + activeName + ".", "success");
      }else{
        showToast("Update finished with errors for " + activeName + ". See Session Log for full output.", "error");
      }
    }catch(e){
      showNotice("Update failed: " + e.message);
      showToast("Update failed: " + e.message, "error");
    }finally{
      rowUpdateActive = null;
      renderPackageTables();
      if(rowUpdateQueue.length > 0){
        setTimeout(processRowUpdateQueue, 0);
      }else{
        setGlobalProgress(false);
      }
    }
  }
`
