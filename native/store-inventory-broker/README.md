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

Build the embedded broker asset from a Windows machine with the repo script.
The script uses the .NET Framework compiler and system WinRT metadata, and
keeps temporary/output files under the workspace:

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-StoreInventoryBroker.ps1
```

`dev\scripts\Build-Workspace.ps1` invokes this broker build before Go tests and
the final Go build, so normal validation embeds a broker executable generated
from the checked-in source. No separate sidecar copy is required for the default
distribution.
