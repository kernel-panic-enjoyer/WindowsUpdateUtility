package updater

const pageScriptThemeAndLabels = `
  function setTheme(theme){
    document.documentElement.dataset.theme = theme;
    try{ localStorage.setItem("windows-updater-theme", theme); }catch(e){}
    var button = $("theme-toggle");
    if(button){ button.innerHTML = icon(theme === "dark" ? "sun" : "moon") + '<span>' + (theme === "dark" ? "Light Mode" : "Dark Mode") + '</span>'; }
  }
  function currentTheme(){
    return document.documentElement.dataset.theme === "light" ? "light" : "dark";
  }
  function setText(id, value){
    var node = $(id);
    if(node){ node.textContent = value; }
  }
  function managerLabel(value){
    var labels = {
      choco: "Chocolatey",
      winget: "winget",
      store: "Store"
    };
    return labels[value] || value;
  }
  function backendLabel(value){
    var labels = {
      "appx-inventory": "AppX inventory",
      "store-cli": "Store CLI",
      "store-cli-resolved": "Store resolved",
      "winget-msstore-fallback": "winget Store fallback"
    };
    return labels[value] || value;
  }
  function sourceLabel(value){
    var labels = {
      winget: "winget repository",
      msstore: "Microsoft Store",
      "store-cli": "Store CLI",
      appx: "AppX inventory",
      choco: "Chocolatey sources"
    };
    return labels[value] || value || "Unknown source";
  }
  function executionBackendLabel(pkg){
    pkg = pkg || {};
    if(pkg.action_backend){ return backendLabel(pkg.action_backend); }
    if(pkg.manager === "store" && pkg.source === "msstore"){ return backendLabel("winget-msstore-fallback"); }
    return managerLabel(pkg.manager);
  }
  function installRouteText(pkg){
    pkg = pkg || {};
    if(pkg.manager === "store"){
      if(pkg.action_backend === "winget-msstore-fallback" || pkg.source === "msstore"){
        return "Installs through winget Store fallback.";
      }
      if(pkg.action_backend === "store-cli" || pkg.source === "store-cli"){
        return "Installs through native Store CLI.";
      }
      return "Installs through Store backend.";
    }
    return "Installs through " + managerLabel(pkg.manager) + ".";
  }
  function allowUnknownVersionUpdates(){
    var control = $("update-allow-unknown");
    return !!control && !!control.checked;
  }
  function allowPinnedUpdates(){
    var control = $("update-allow-pinned");
    return !!control && !!control.checked;
  }
  function appendGlobalUpdateOptions(params){
    params.delete("allow_unknown_version");
    params.delete("allow_pinned");
    if(allowUnknownVersionUpdates()){ params.set("allow_unknown_version", "true"); }
    if(allowPinnedUpdates()){ params.set("allow_pinned", "true"); }
    return params;
  }
  function packageAutoUpdateable(pkg){
    return pkg.update_supported !== false && !pkg.unknown_version && !pkg.pinned;
  }
  function packageBulkUpdateable(pkg){
    var options = arguments.length > 1 && arguments[1] ? arguments[1] : {allowUnknown:allowUnknownVersionUpdates(), allowPinned:allowPinnedUpdates()};
    return !!pkg.update_available &&
      pkg.update_supported !== false &&
      (!pkg.unknown_version || options.allowUnknown) &&
      (!pkg.pinned || options.allowPinned);
  }
  function pagedItems(items, currentPage, pageSize){
    var total = items.length;
    var totalPages = Math.max(1, Math.ceil(total / pageSize));
    var page = Math.min(Math.max(currentPage, 1), totalPages);
    var start = (page - 1) * pageSize;
    return {
      page: page,
      total: total,
      totalPages: totalPages,
      start: start,
      end: Math.min(start + pageSize, total),
      items: items.slice(start, start + pageSize)
    };
  }
  function renderPager(page, status, prev, next, suffix){
    if(status){ status.textContent = "Showing " + (page.start + 1) + "-" + page.end + " of " + page.total + (suffix || ""); }
    if(prev){ prev.disabled = page.page <= 1; }
    if(next){ next.disabled = page.page >= page.totalPages; }
  }
  function renderEmptyPager(status, statusHTML, prev, next){
    if(status){ status.innerHTML = statusHTML; }
    if(prev){ prev.disabled = true; }
    if(next){ next.disabled = true; }
  }
`
