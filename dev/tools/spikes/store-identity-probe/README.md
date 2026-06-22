# Store Identity Probe

Disposable read-only probe for validating Windows Runtime Store/AppX identity behavior.

Run from PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\dev\tools\spikes\store-identity-probe\Probe-StoreIdentity.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\dev\tools\spikes\store-identity-probe\Probe-StoreIdentity.ps1 -PackageFamilyName Microsoft.WindowsStore_8wekyb3d8bbwe
```

The script validates whether the current process can call:

- `Windows.Management.Deployment.PackageManager.FindPackagesForUser("")`
- `Windows.Management.Deployment.PackageManager.FindPackagesForUser("", packageFamilyName)`
- `Windows.ApplicationModel.PackageCatalog.OpenForCurrentUser()`

This folder is intentionally outside `internal/` and contains no Go package, so it is excluded from the production build.
