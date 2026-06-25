# Native Store Inventory Smoke Test

Scope: manual validation for the current-user packaged-application inventory provider.

## Build

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

The app uses direct Go WinRT/AppModel calls for current-user packaged-app
inventory through the same executable's internal `--store-inventory-worker`
mode. It should not build, extract, or launch a separate
`WindowsUpdater.StoreInventoryBroker.exe` sidecar.

## Native Inventory

1. Start the app without elevation.
2. Confirm no PowerShell AppX inventory command and no separate Store inventory
   sidecar binary is run for normal Store inventory. A short-lived hidden
   `WindowsUpdaterWebUI.exe --store-inventory-worker` child process is expected.
3. Refresh inventory.
4. Confirm Store packages are grouped by package family name and framework/resource/optional-only families are not shown as independent Store products.
5. Confirm Store update state comes from the Store scan-health providers, not from display-name searches.

Expected result: current-user native inventory is the only Store inventory path.

## Wrong User / Session

1. Sign in as user A and start the app.
2. Switch to user B and repeat.
3. Confirm diagnostics do not mix package family names across user sessions.

Expected result: native responses with the wrong user SID are rejected.

## Direct Provider Failure

1. Force a provider failure with a local diagnostic build or debugger.
2. Refresh inventory.

Expected result: native provider failure is recorded as diagnostics and Store update status remains `Unknown`; no legacy inventory fallback is used.
