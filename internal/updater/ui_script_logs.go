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
  function filteredLogLines(){
    var query = logSearchQuery.trim().toLowerCase();
    var clearedBefore = clearedLogBeforeByCategory[activeLogCategory || "all"] || 0;
    return logEntries.filter(function(entry){
      return Number(entry.id || 0) > clearedBefore && logEntryInActiveCategory(entry);
    }).map(formatLogEntry).filter(function(line){
      return !query || line.toLowerCase().indexOf(query) !== -1;
    });
  }
  function appendLogEntries(entries){
    if(!entries || entries.length === 0){ return; }
    entries.forEach(function(entry){
      lastLogID = Math.max(lastLogID, Number(entry.id || 0));
      logEntries.push(entry);
    });
    var auto = $("log-autoscroll");
    renderLogLines(!auto || auto.checked);
  }
  async function loadLogs(){
    try{
      var data = await (await fetch(api("/api/logs", {since:String(lastLogID)}))).json();
      appendLogEntries(data.entries || []);
      if(typeof data.latest_id === "number" && data.latest_id > lastLogID && (!data.entries || data.entries.length === 0)){
        lastLogID = data.latest_id;
      }
    }catch(e){}
  }
  function setActiveLogCategory(category){
    activeLogCategory = category || "all";
    document.querySelectorAll(".log-tab").forEach(function(button){
      var active = button.dataset.logCategory === activeLogCategory;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", active ? "true" : "false");
    });
    renderLogLines(false);
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
