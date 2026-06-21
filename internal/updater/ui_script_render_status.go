package updater

const pageScriptStatusRender = `
  function renderDashboardSummary(){
    var managerMap = latestStatus && latestStatus.managers ? latestStatus.managers : {};
    var managerNames = Object.keys(managerMap);
    var availableManagers = managerNames.filter(function(name){ return managerMap[name] && managerMap[name].available; }).length;
    var updates = packages.filter(function(pkg){ return !!pkg.update_available; });
    var supportedUpdates = updates.filter(packageBulkUpdateable);
    var updateablePackages = packages.filter(packageAutoUpdateable);
    var inventoryOnly = packages.filter(function(pkg){ return pkg.update_supported === false; }).length;
    var statusLoading = !!(latestStatus && latestStatus.loading);
    var loading = latestPackagesLoading || statusLoading;

    setText("summary-updates", loading ? "-" : String(updates.length));
    var updatesDetail = $("summary-updates-detail");
    if(updatesDetail){ updatesDetail.innerHTML = loading ? loadingText("Checking package status") : html(supportedUpdates.length + " updateable"); }
    setText("summary-packages", loading ? "-" : String(packages.length));
    var packagesDetail = $("summary-packages-detail");
    if(packagesDetail){ packagesDetail.innerHTML = loading ? loadingText("Inventory loading") : html(updateablePackages.length + " managed, " + inventoryOnly + " inventory-only"); }
    setText("summary-managers", statusLoading ? "-" : availableManagers + "/" + managerNames.length);
    var managersDetail = $("summary-managers-detail");
    if(managersDetail){ managersDetail.innerHTML = statusLoading ? loadingText("Checking tools") : "Available package managers"; }
    var startupEnabled = !!(latestStatus && latestStatus.startup_enabled);
    var autoTaskEnabled = !!(latestStatus && latestStatus.auto_task_enabled);
    setText("summary-automation", statusLoading ? "-" : ((startupEnabled || autoTaskEnabled) ? "On" : "Off"));
    var automationDetail = $("summary-automation-detail");
    if(automationDetail){ automationDetail.innerHTML = statusLoading ? loadingText("Loading tasks") : html("Startup " + (startupEnabled ? "on" : "off") + ", daily updates " + (autoTaskEnabled ? "on" : "off")); }
  }
  function renderManagers(data){
    var target = $("manager-list");
    if(!target){ return; }
    var managers = data.managers || {};
    var names = Object.keys(managers).sort();
    if(names.length === 0){
      if(managersRendered){ return; }
      var placeholder = '<p class="muted">' + (data.loading ? loadingText('Checking package managers...') : 'No package manager status yet.') + '</p>';
      if(target.innerHTML !== placeholder){ target.innerHTML = placeholder; }
      return;
    }
    managersRendered = true;
    function managerAvailabilityText(name, manager){
      var version = String(manager.version || "").trim();
      if(name === "store" && (!version || /^usage:/i.test(version) || /<command>/i.test(version))){
        return "Available";
      }
      return "Available" + (version ? " " + version : "");
    }
    function managerDisplayDetails(name, manager){
      var details = [];
      if(name === "store"){
        if(manager.inventory_available){
          details.push('<span class="muted">Store apps detected via ' + html(manager.inventory_backend || 'AppX') + ' inventory</span>');
        }
        if(!manager.available && manager.action_backend === "winget-msstore-fallback"){
          details.push('<span class="muted">Store installs and updates can fall back to winget for compatible Store IDs.</span>');
        }
        return details.join("");
      }
      if(manager.inventory_available){ details.push('<span class="badge ok">Inventory available</span>'); }
      return details.join("");
    }
    var markup = names.map(function(name){
      var manager = managers[name] || {};
      var details = managerDisplayDetails(name, manager);
      if(manager.available){
        return '<div class="manager manager-ok"><div class="manager-main"><span class="manager-dot">' + icon("check") + '</span><div><strong>' + html(managerLabel(name)) + '</strong><span class="muted">' + html(manager.path || '') + '</span></div></div><span class="badge ok">' + html(managerAvailabilityText(name, manager)) + '</span>' + details + '</div>';
      }
      return '<div class="manager manager-missing"><div class="manager-main"><span class="manager-dot">' + icon("alert") + '</span><div><strong>' + html(managerLabel(name)) + '</strong><span class="muted">' + html(manager.error || '') + '</span></div></div><span class="badge error">Missing</span>' + details + '<form class="manager-install-form" method="post" action="/api/managers/install"><input type="hidden" name="manager" value="' + attr(name) + '"><button type="submit">' + icon("install") + '<span>Install ' + html(managerLabel(name)) + '</span></button></form></div>';
    }).join("");
    if(target.innerHTML !== markup){ target.innerHTML = markup; }
  }
  function renderStatus(data){
    latestStatus = data || {};
    renderManagers(data);
    var startup = $("startup-toggle");
    if(startup){
      startup.disabled = !!data.loading;
      startup.dataset.enabled = data.startup_enabled ? "true" : "false";
      startup.setAttribute("aria-pressed", data.startup_enabled ? "true" : "false");
      startup.innerHTML = data.loading ? loadingText("Checking startup...") : icon("refresh") + '<span>' + (data.startup_enabled ? "Disable Start With Windows" : "Enable Start With Windows") + '</span>';
    }
    var auto = $("auto-global-toggle");
    var globalEnabled = !!(data.settings && data.settings.auto_update_global);
    if(auto){
      auto.disabled = !!data.loading;
      auto.dataset.enabled = globalEnabled ? "true" : "false";
      auto.setAttribute("aria-pressed", globalEnabled ? "true" : "false");
      auto.innerHTML = data.loading ? loadingText("Checking auto-update...") : icon("update") + '<span>' + (globalEnabled ? "Disable Daily Auto-Update" : "Enable Daily Auto-Update") + '</span>';
    }
    var status = $("automation-status");
    if(status){
      status.innerHTML = data.loading ? loadingText("Loading task status...") : html("Startup task: " + (data.startup_enabled ? "enabled" : "disabled") + " - Daily update task: " + (data.auto_task_enabled ? "enabled" : "disabled"));
    }
    renderDashboardSummary();
  }
`
