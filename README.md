# Windows Updater WebUI

A single-binary Go Windows updater with a browser UI for winget, Chocolatey,
and Microsoft Store apps.

## Features

- Runs as a local-only WebUI on `127.0.0.1`.
- Relaunches itself as administrator when needed.
- Detects winget, Chocolatey, and the native Store CLI.
- Lists installed winget, Chocolatey, and Store/AppX packages in one table.
- Detects available updates and enables update buttons only for packages with updates.
- Searches for installable packages and filters out truncated winget IDs.
- Installs packages from winget, Chocolatey, or Store after an explicit button click.
- Updates individual packages, selected packages, or all packages.
- Supports Start with Windows through Windows Task Scheduler.
- Supports opt-in daily auto-update for individual packages or all packages.
- Scans Windows uninstall registry plus winget/Store inventory and reports apps newly detected since the previous scan.
- Includes a dark/light WebUI theme with no separate frontend JavaScript dependency.

## Project Layout

- `main.go`: thin executable entrypoint.
- `internal/updater`: application backend, WebUI, package-manager integrations, tests, and embedded assets.
- `internal/updater/assets`: app icon and favicon source assets.
- `tools/icongen`: icon generation utility.
- `dist`: local build output.

## Build

Use Go 1.22+ on Windows:

```cmd
set GOCACHE=%CD%\.gocache
go test ./...
go build -ldflags="-H=windowsgui" -o dist\WindowsUpdaterWebUI.exe .
```

If your Windows folder policy blocks writing `.exe` files into this directory,
build to another folder:

```cmd
go build -ldflags="-H=windowsgui" -o "%TEMP%\WindowsUpdaterWebUI.exe" .
```

## Run

Double-click `WindowsUpdaterWebUI.exe`.

The executable starts the local WebUI, opens a tokenized browser URL, and uses
Windows UAC to relaunch as administrator when needed. No batch file, script
launcher, Python runtime, VBS launcher, or C# launcher is required.

For development without UAC:

```cmd
set GOCACHE=%CD%\.gocache
go run . --no-elevate
```

## Notes

- Package install/update actions may download software and require administrator rights.
- Missing winget opens Microsoft App Installer.
- Missing Store CLI opens Microsoft Store and Windows Update surfaces.
- Missing Chocolatey installs through winget when winget is available; otherwise the app opens the Chocolatey install page.
- State is stored under `%LOCALAPPDATA%\WindowsUpdaterWebUI` by default.
