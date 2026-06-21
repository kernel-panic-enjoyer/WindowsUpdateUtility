package updater

const pageScriptLogConsole = `
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
      setLogConnectionState("disconnected", "Log disconnected; retrying");
      showNotice("Session log disconnected. Reconnecting...");
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
      setLogConnectionState("disconnected", "Log disconnected; retrying");
      showNotice("Session log disconnected. Reconnecting...");
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
`
