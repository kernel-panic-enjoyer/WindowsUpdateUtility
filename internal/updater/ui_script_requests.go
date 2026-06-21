package updater

const pageScriptRequests = `
  function setGlobalProgress(show, message, cancelVisible){
    var panel = $("update-progress");
    if(!panel){ return; }
    panel.setAttribute("aria-busy", show ? "true" : "false");
    var title = panel.querySelector(".progress-title");
    if(title){ title.innerHTML = show ? loadingText(message || "Updating packages...") : html(message || "Updating packages..."); }
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
    if(title){ title.innerHTML = show ? loadingText(message || "Installing package...") : html(message || "Installing package..."); }
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
    return fetch(api(path), {method:"POST", headers:{"Content-Type":"application/x-www-form-urlencoded"}, body:body});
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
`
