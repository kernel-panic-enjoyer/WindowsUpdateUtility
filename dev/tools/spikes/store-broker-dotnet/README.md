# .NET Store Broker Probe

Disposable C# probe for the recommended Store identity broker architecture.

Expected run command on a machine with the .NET SDK and Windows SDK projections available:

```powershell
dotnet run --project .\dev\tools\spikes\store-broker-dotnet\StoreBrokerProbe.csproj -- Microsoft.WindowsStore_8wekyb3d8bbwe
```

This was not compiled in the current workspace because `dotnet` is not installed in the active PATH. It is kept as a minimal broker spike source, not production code.

This folder is outside `internal/` and contains no Go package, so it is excluded from the production build.
