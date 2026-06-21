package updater

const pageScriptAuxiliaryRender = `
  function renderScan(scan){
    var panel = $("scan-panel");
    if(!panel){ return; }
    panel.classList.remove("hidden");
    var counts = scan.source_counts || {};
    var registryCount = counts.registry || scan.registry_count || 0;
    var wingetCount = counts.winget || scan.winget_count || 0;
    var storeCount = counts.store || scan.store_count || 0;
    $("scan-summary").textContent = "Tracked " + (scan.tracked_count || 0) + " apps - Registry " + registryCount + " - Winget " + wingetCount + " - Store " + storeCount;
    var errors = $("scan-errors");
    var errorText = (scan.errors || []).map(function(item){ return (item.source || "source") + ": " + (item.error || ""); }).join("\n");
    errors.textContent = errorText;
    errors.classList.toggle("hidden", !errorText);
    var apps = scan.new_apps || [];
    $("scan-body").innerHTML = apps.length ? apps.map(function(app){
      return '<tr><td>' + html(app.source) + '</td><td>' + html(app.name) + '</td><td>' + html(app.version) + '</td><td>' + html(app.publisher) + '</td><td>' + html(app.install_location) + '</td></tr>';
    }).join("") : '<tr><td colspan="5">No newly detected applications.</td></tr>';
  }
  function renderSearch(data){
    var panel = $("search-results-panel");
    var body = $("search-results-body");
    if(!panel || !body){ return; }
    panel.classList.remove("hidden");
    searchResults = data.packages || [];
    searchMetadata = data || {};
    searchPage = 1;
    renderSearchProvenance(data || {});
    renderSearchTable();
  }
  function searchCommandResult(data, manager){
    var results = data.command_results || {};
    if(manager === "store"){
      return results.store || results.store_search || null;
    }
    return results[manager] || null;
  }
  function uniqueSearchManagers(packages){
    var seen = {};
    var names = [];
    (packages || []).forEach(function(pkg){
      var manager = pkg.manager || "";
      if(!manager || seen[manager]){ return; }
      seen[manager] = true;
      names.push(manager);
    });
    return names;
  }
  function searchOriginLabel(pkg){
    var source = sourceLabel(pkg.source || pkg.manager);
    var backend = executionBackendLabel(pkg);
    return source === backend ? source : source + " via " + backend;
  }
  function uniqueSearchOrigins(packages){
    var seen = {};
    var names = [];
    (packages || []).forEach(function(pkg){
      var label = searchOriginLabel(pkg || {});
      if(!label || seen[label]){ return; }
      seen[label] = true;
      names.push(label);
    });
    return names;
  }
  function searchShownPhrase(packages){
    var origins = uniqueSearchOrigins(packages);
    if(origins.length === 0){ return "no package results are shown"; }
    return "results from " + origins.join(", ") + " are shown";
  }
  function searchManagerLabel(manager){
    if(manager === "store"){ return "Store CLI"; }
    return managerLabel(manager);
  }
  function searchFailureReason(result, managerStatus){
    if(result){
      var reason = firstMeaningfulOutputLine(result.stderr) || firstMeaningfulOutputLine(result.stdout);
      if(reason){ return " Code " + (result.code || 0) + ": " + truncateNoticeText(reason, 100); }
      return " Code " + (result.code || 0) + ".";
    }
    if(managerStatus && managerStatus.error){
      return " " + truncateNoticeText(managerStatus.error, 120);
    }
    return "";
  }
  function renderSearchProvenance(data){
    var target = $("search-provenance");
    if(!target){ return; }
    var packages = data.packages || [];
    var managers = data.managers || {};
    var shown = searchShownPhrase(packages);
    var messages = [];
    ["winget","store","choco"].forEach(function(manager){
      var status = managers[manager] || {};
      var result = searchCommandResult(data, manager);
      if(status.available === false){
        messages.push(searchManagerLabel(manager) + " unavailable;" + searchFailureReason(null, status) + " " + shown + ".");
        return;
      }
      if(result && !result.ok){
        messages.push(searchManagerLabel(manager) + " search failed;" + searchFailureReason(result, status) + " " + shown + ".");
      }
    });
    var sourceOrigins = uniqueSearchOrigins(packages).join(", ");
    if(sourceOrigins){
      messages.unshift("Showing " + packages.length + " result(s) from " + sourceOrigins + ".");
    }
    if(messages.length === 0){
      target.classList.add("hidden");
      target.innerHTML = "";
      return;
    }
    target.classList.remove("hidden");
    target.innerHTML = messages.map(function(message){
      return '<div class="provenance-item">' + html(message) + '</div>';
    }).join("");
  }
  function searchMatchCell(pkg){
    var reason = pkg.match_reason || "";
    if(!reason && pkg.match){ reason = "Matched " + pkg.match + "."; }
    if(!reason){ reason = "Returned by " + managerLabel(pkg.manager) + " search."; }
    var raw = pkg.match ? '<br><span class="muted">Raw match: ' + html(pkg.match) + '</span>' : '';
    return html(reason) + raw;
  }
  function searchManagerBackendCell(pkg){
    return '<span class="badge manager-badge">' + html(managerLabel(pkg.manager)) + '</span><br><span class="muted">Backend: ' + html(executionBackendLabel(pkg)) + '</span>';
  }
  function searchActionCell(pkg){
    return '<form class="install-form" method="post" action="/api/install" data-backend-label="' + attr(executionBackendLabel(pkg)) + '"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit" aria-label="Install ' + attr(pkg.name || pkg.id) + '">' + icon("install") + '<span>Install</span></button><span class="muted install-route">' + html(installRouteText(pkg)) + '</span></form>';
  }
  function renderSearchTable(){
    var body = $("search-results-body");
    var status = $("search-page-status");
    var prev = $("search-prev");
    var next = $("search-next");
    if(!body){ return; }
    if(searchResults.length === 0){
      body.innerHTML = '<tr><td colspan="7">No installable results.</td></tr>';
      renderEmptyPager(status, html("No results"), prev, next);
      return;
    }
    var page = pagedItems(searchResults, searchPage, searchPageSize);
    searchPage = page.page;
    body.innerHTML = page.items.map(function(pkg){
      return '<tr><td><strong>' + html(pkg.name) + '</strong></td><td>' + html(sourceLabel(pkg.source || pkg.manager)) + '</td><td>' + searchManagerBackendCell(pkg) + '</td><td><code class="package-id">' + html(pkg.id) + '</code></td><td>' + searchMatchCell(pkg) + '</td><td>' + html(pkg.version || "") + '</td><td>' + searchActionCell(pkg) + '</td></tr>';
    }).join("");
    renderPager(page, status, prev, next);
  }
`
