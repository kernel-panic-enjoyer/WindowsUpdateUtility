# Native Store Inventory Smoke Test

Scope: manual validation for the current-user packaged-application inventory provider.

## Build Broker

Requires a Windows SDK/.NET Framework machine. Build the broker directly into
the repo-embedded asset path so temporary and final binaries stay under the
workspace:

```powershell
New-Item -ItemType Directory -Force .\internal\updater\assets\broker | Out-Null
. .\dev\scripts\Set-WorkspaceBinaryPaths.ps1 | Out-Null
& 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe' /nologo /target:exe `
  /out:'.\internal\updater\assets\broker\WindowsUpdater.StoreInventoryBroker.exe' `
  /reference:'C:\Windows\System32\WinMetadata\Windows.ApplicationModel.winmd' `
  /reference:'C:\Windows\System32\WinMetadata\Windows.Management.winmd' `
  /reference:'C:\Windows\System32\WinMetadata\Windows.System.winmd' `
  /reference:'C:\Windows\System32\WinMetadata\Windows.Storage.winmd' `
  /reference:'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\System.Runtime.WindowsRuntime.dll' `
  /reference:'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\System.Runtime.dll' `
  .\native\store-inventory-broker\Program.cs
```

Build the app with repo-local Go cache and temporary directories:

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

The app embeds the checked-in broker asset and extracts it at runtime to:

```text
dist\bin\WindowsUpdater.StoreInventoryBroker.exe
```

Alternatively, for diagnostics only, set an explicit repo-local broker path:

```powershell
$env:UPDATER_STORE_INVENTORY_BROKER="$PWD\internal\updater\assets\broker\WindowsUpdater.StoreInventoryBroker.exe"
```

## Diagnostic Dual Run

1. Start the app without elevation.
2. Set `UPDATER_NATIVE_STORE_INVENTORY_DUAL_RUN=1`.
3. Refresh inventory.
4. Export logs.
5. Confirm diagnostics include `native_store_inventory` and, when there are differences, `native_store_inventory_compare`.
6. Confirm update decisions and package rows still match the legacy AppX path.

Expected result: comparison diagnostics are produced, but no Store update state changes.

## Native Inventory Flag

1. Set `UPDATER_NATIVE_STORE_INVENTORY=1`.
2. Start the app without elevation.
3. Confirm no PowerShell AppX inventory command is run for normal Store inventory.
4. Confirm Store packages are grouped by package family name and framework/resource/optional-only families are not shown as independent Store products.
5. Confirm Store update detection is not added by this phase.

Expected result: current-user native inventory is available behind the flag, without catalog update detection.

## Wrong User / Session

1. Sign in as user A and start the app with dual-run enabled.
2. Switch to user B and repeat.
3. Confirm diagnostics do not mix package family names across user sessions.

Expected result: native responses with the wrong user SID are rejected.

## Broker Failure

1. Set `UPDATER_STORE_INVENTORY_BROKER` to a missing or crashing executable.
2. Enable dual-run.
3. Refresh inventory.

Expected result: native provider failure is recorded as diagnostics. Legacy inventory remains the active source unless native inventory was explicitly enabled.
