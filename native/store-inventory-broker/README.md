# Store Inventory Broker

Native current-user packaged-application inventory broker selected by ADR 0001.

The Go coordinator embeds the compiled broker executable and extracts it to a
`bin` directory under the running application folder. `UPDATER_BINARY_DIR` can
override that extraction root, and `UPDATER_STORE_INVENTORY_BROKER` can still
override the exact broker path for diagnostics.

Protocol:

- stdin: JSON `InventoryRequest`
- stdout: JSON `InventoryResponse`
- argument: `--inventory`

The broker uses `Windows.Management.Deployment.PackageManager.FindPackagesForUser(string.Empty)` so enumeration is current-user scoped. It must not call PowerShell or `Get-AppxPackage -AllUsers`.

Build the checked-in embedded broker from a Windows machine with the .NET
Framework compiler and system WinRT metadata:

```powershell
$root = (Resolve-Path ..\..).Path
Set-Location $root
. .\dev\scripts\Set-WorkspaceBinaryPaths.ps1 | Out-Null
New-Item -ItemType Directory -Force '.\internal\updater\assets\broker' | Out-Null
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

After this broker asset is present, normal `go build` embeds it into
`WindowsUpdaterWebUI.exe`; use `dev\scripts\Build-Workspace.ps1` for the normal
application build so Go, .NET, NuGet, and toolchain scratch binaries stay under
the repository.
No separate sidecar copy is required for the default distribution.
