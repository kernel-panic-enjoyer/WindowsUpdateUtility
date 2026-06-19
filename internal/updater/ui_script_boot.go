package updater

const pageScriptBoot = `
  setTheme(currentTheme());
  loadStatus(false);
  loadPackages(false).then(function(){ checkActiveUpdateJob(); });
  loadLogs();
  setInterval(loadLogs, 750);
  var query = new URLSearchParams(window.location.search).get("q");
  if(query){
    $("search-input").value = query;
    loadSearch(query);
  }
})();
`
