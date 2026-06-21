package updater

const pageScriptPackageRender = `
  function packageNameCell(pkg){
    var secondary = pkg.action_backend === "appx-inventory" ? "Store app" : pkg.id;
    if(pkg.unknown_version){
      secondary += " - unknown installed version";
    }
    if(pkg.pinned){
      secondary += " - pinned";
    }
    return '<strong>' + html(pkg.name) + '</strong><br><span class="muted">' + html(secondary) + '</span>';
  }
	function managerCell(pkg){
		var backend = pkg.action_backend ? '<br><span class="muted">' + html(backendLabel(pkg.action_backend)) + '</span>' : '';
		return '<span class="badge manager-badge">' + html(managerLabel(pkg.manager)) + '</span>' + backend;
	}
  function autoButton(pkg){
    if(pkg.update_supported === false){
      return '<span class="muted">N/A</span>';
    }
    if(pkg.unknown_version || pkg.pinned){
      return '<span class="muted">Explicit only</span>';
    }
    return '<button class="auto-package toggle-button" type="button" data-key="' + attr(pkg.key) + '" data-enabled="' + (pkg.auto_update ? 'true' : 'false') + '"' + (updateBusy ? ' disabled' : '') + '><span>' + (pkg.auto_update ? 'On' : 'Off') + '</span></button>';
  }
  function packageAvailableCell(pkg){
    var available = html(pkg.available_version);
    if(pkg.manager === "store" && pkg.update_available && String(pkg.available_version || "") === String(pkg.version || "")){
      return '<span class="muted">Pending in Microsoft Store</span>';
    }
    return available;
  }
	function updateForm(pkg){
		if(pkg.update_supported === false){
			return '<span class="muted">Inventory only</span>';
		}
    var blockedUnknown = pkg.unknown_version && !allowUnknownVersionUpdates();
    var blockedPinned = pkg.pinned && !allowPinnedUpdates();
    var updateState = rowUpdateState(pkg.key);
    var disabled = updateBusy || !!updateState || blockedUnknown || blockedPinned;
    var label = updateState === "active" ? "Updating" : (updateState === "queued" ? "Queued" : "Update");
    var title = blockedUnknown ? ' title="Enable the global unknown-version option first"' : (blockedPinned ? ' title="Enable the global pinned update option first"' : '');
		return '<form class="update-form" data-key="' + attr(pkg.key) + '" data-unknown-version="' + (pkg.unknown_version ? 'true' : 'false') + '" data-pinned="' + (pkg.pinned ? 'true' : 'false') + '" data-blocked-unknown="' + (blockedUnknown ? 'true' : 'false') + '" data-blocked-pinned="' + (blockedPinned ? 'true' : 'false') + '" method="post" action="/api/update"><input type="hidden" name="manager" value="' + attr(pkg.manager) + '"><input type="hidden" name="package_id" value="' + attr(pkg.id) + '"><button type="submit"' + (disabled ? ' disabled' : '') + title + '>' + icon("update") + '<span>' + html(label) + '</span></button><div class="row-progress' + (updateState ? '' : ' hidden') + '"><div class="progress-bar"><span></span></div></div></form>';
  }
	function installedAction(pkg){
		if(pkg.action_backend === "store-cli-resolved" && pkg.update_available){
			return updateForm(pkg);
		}
		return '<span class="muted">-</span>';
	}
  function packageMatchesInstalledSearch(pkg){
    var query = installedSearchQuery.trim().toLowerCase();
    if(!query){ return true; }
    return [pkg.name, pkg.id, pkg.manager, pkg.version, pkg.available_version].some(function(value){
      return String(value || "").toLowerCase().indexOf(query) !== -1;
    });
  }
  function renderUpdatesTable(updates, loading){
    var target = $("updates-body");
    var status = $("updates-page-status");
    var prev = $("updates-prev");
    var next = $("updates-next");
    if(!target){ return; }
    if(updates.length === 0){
      target.innerHTML = loading ? loadingTableRow(7, "Checking for updates...") : '<tr><td colspan="7">No updates available.</td></tr>';
      renderEmptyPager(status, loading ? loadingText('Checking...') : html('No updates'), prev, next);
      return;
    }
    var page = pagedItems(updates, updatePage, updatePageSize);
    updatePage = page.page;
    target.innerHTML = page.items.map(function(pkg){
      var selectable = packageBulkUpdateable(pkg);
      var rowClass = rowUpdateState(pkg.key) === "active" ? ' class="updating-current"' : '';
      return '<tr data-key="' + attr(pkg.key) + '"' + rowClass + '><td><input form="update-selected-form" type="checkbox" name="package_key" value="' + attr(pkg.key) + '"' + ((updateBusy || !selectable) ? ' disabled' : '') + '></td><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + packageAvailableCell(pkg) + '</td><td>' + autoButton(pkg) + '</td><td>' + updateForm(pkg) + '</td></tr>';
    }).join("");
    renderPager(page, status, prev, next);
  }
  function renderInstalledTable(loading){
    var target = $("packages-body");
    var status = $("installed-page-status");
    var prev = $("installed-prev");
    var next = $("installed-next");
    if(!target){ return; }
    var visiblePackages = packages.filter(packageMatchesInstalledSearch);
    if(visiblePackages.length === 0){
		target.innerHTML = loading ? loadingTableRow(7, "Loading packages...") : '<tr><td colspan="7">' + (installedSearchQuery ? 'No packages match your filter.' : 'No managed packages found.') + '</td></tr>';
      renderEmptyPager(status, loading ? loadingText('Loading...') : html(installedSearchQuery ? 'No matches' : 'No packages'), prev, next);
      return;
    }
    var page = pagedItems(visiblePackages, installedPage, installedPageSize);
    installedPage = page.page;
	target.innerHTML = page.items.map(function(pkg){
		var rowStatus = pkg.update_supported === false ? '<span class="badge">Inventory only</span>' : ((pkg.unknown_version || pkg.pinned) && pkg.update_available ? '<span class="badge warn">Explicit update</span>' : (pkg.update_available ? '<span class="badge warn">Update</span>' : '<span class="badge ok">Current</span>'));
    var rowClass = rowUpdateState(pkg.key) === "active" ? ' class="updating-current"' : '';
		return '<tr data-key="' + attr(pkg.key) + '"' + rowClass + '><td>' + packageNameCell(pkg) + '</td><td>' + managerCell(pkg) + '</td><td>' + html(pkg.version) + '</td><td>' + packageAvailableCell(pkg) + '</td><td>' + rowStatus + '</td><td>' + autoButton(pkg) + '</td><td>' + installedAction(pkg) + '</td></tr>';
	}).join("");
    renderPager(page, status, prev, next, installedSearchQuery ? " matches" : "");
  }
  function renderPackageTables(){
    var updates = packages.filter(function(pkg){ return !!pkg.update_available; });
    var updateablePackages = packages.filter(packageAutoUpdateable);
    var updateJobRunning = activeUpdateJobRunning();
    $("auto-all").disabled = updateBusy || updateablePackages.length === 0;
    $("auto-none").disabled = updateBusy || updateablePackages.length === 0;
    renderUpdatesTable(updates, latestPackagesLoading);
    renderInstalledTable(latestPackagesLoading);
    var supportedUpdates = updates.filter(packageBulkUpdateable);
    $("update-all-button").disabled = updateBusy || updateJobRunning || supportedUpdates.length === 0;
    $("update-selected-button").disabled = updateBusy || updateJobRunning || supportedUpdates.length === 0;
    renderDashboardSummary();
  }
  function renderPackages(data){
    renderManagers(data);
    packages = data.packages || [];
    latestPackagesLoading = !!data.loading;
    renderPackageTables();
  }
`
