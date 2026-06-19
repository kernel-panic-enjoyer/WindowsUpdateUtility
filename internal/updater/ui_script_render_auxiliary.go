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
    searchPage = 1;
    renderSearchTable();
  }
  function renderSearchTable(){
    var body = $("search-results-body");
    var status = $("search-page-status");
    var prev = $("search-prev");
    var next = $("search-next");
    if(!body){ return; }
    if(searchResults.length === 0){
      body.innerHTML = '<tr><td colspan="5">No installable results.</td></tr>';
      renderEmptyPager(status, html("No results"), prev, next);
      return;
    }
    var page = pagedItems(searchResults, searchPage, searchPageSize);
    searchPage = page.page;
    body.innerHTML = page.items.map(function(pkg){
      return '<tr><td>' + html(pkg.name) + '</td><td>' + html(managerLabel(pkg.manager)) + (pkg.action_backend ? '<br><span class="muted">' + html(backendLabel(pkg.action_backend)) + '</span>' : '') + '</td><td>' + html(pkg.id) + '</td><td>' + html(pkg.version) + '</td><td><form class="install-form" method="post" action="/api/install"><input type="hidden" name="token" value="' + attr(token) + '"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit">' + icon("install") + '<span>Install</span></button></form></td></tr>';
    }).join("");
    renderPager(page, status, prev, next);
  }
`
