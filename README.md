# Windows Updater WebUI

A single distributed Go Windows updater with a browser UI for winget,
Chocolatey, and Microsoft Store apps. Microsoft Store packaged-app inventory is
enumerated in the interactive user's session through an internal same-binary
WinRT worker so blocked Store APIs can be cancelled without orphaning helpers.

## Features

- Runs as a local-only WebUI on `127.0.0.1`.
- Runs the WebUI in the interactive user session and uses elevation only for actions that require it.
- Detects winget, Chocolatey, and the native Store CLI.
- Lists installed winget, Chocolatey, and current-user Store packaged apps in one table.
- Uses the exact Microsoft Store assessment model by default. Store status is
  `Unknown` unless the app has a fresh, complete, exact, current-user scan; it
  must not guess Store update state from display names or fuzzy matches.
- Detects available updates and enables update buttons only for packages with
  updates and exact action targets. Store execution attempts verified Product
  ID first through WinGet msstore when available, with verified Store CLI exact
  targets used only as fallback.
- Searches for installable packages and filters out truncated winget IDs.
- Installs packages from winget, Chocolatey, or Store after an explicit button click.
- Updates individual packages, selected packages, or all packages.
- Supports Start with Windows through Windows Task Scheduler.
- Supports opt-in daily auto-update for individual packages or all packages
  through Windows Task Scheduler.
- Scans Windows uninstall registry plus managed package inventory and reports apps newly detected since the previous scan.
- Exports Store diagnostics from the WebUI scan-health panel without raw user SIDs, tokens, credentials, or personal install paths.
- Retains bounded session logs and exports category-specific log archives.
- Includes a dark/light WebUI theme with no separate frontend JavaScript dependency.

## Project Layout

- `main.go`: thin executable entrypoint.
- `app.manifest` / `app.syso`: Windows icon and explicit `asInvoker` manifest so the WebUI starts without startup elevation.
- `internal/updater`: application backend, WebUI, package-manager integrations, tests, and embedded assets.
- `internal/updater/assets`: app icon, favicon, CSS, and JavaScript assets.
- `dev/scripts`: developer build and distribution smoke helpers.
- `dev/tools/icongen`: icon generation utility.
- `dev/tools/spikes`: disposable Store/API validation probes, excluded from the production binary.
- `docs/architecture`: architecture decision records.
- `docs/status`: current implementation status and release-gate notes.
- `docs/testing`: manual Windows smoke-test procedures.
- `docs/troubleshooting`: user-facing troubleshooting notes.
- `dist`: local build output.

## Build

Use the Go version declared in `go.mod` on Windows. This repository currently
declares Go 1.26.

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

`dev\scripts\Build-Workspace.ps1` is read-only with respect to tracked source.
It checks formatting with `gofmt -l`, runs tests, vet, the WebUI JavaScript
syntax check, and the Windows GUI build. If formatting is needed, run:

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Format-Workspace.ps1
```

Build output is written under `dist\`. The production executable is unstripped;
the build script intentionally uses only `-H=windowsgui` and does not use
`-s`, `-w`, UPX, or packing. Each build writes a sibling `.metadata.json` file
with commit, dirty-worktree flag, Go version, target platform, byte count, and
SHA-256.

## Run

Double-click `WindowsUpdaterWebUI.exe`.

The executable starts the local WebUI and opens a tokenized browser URL. UAC is
requested only for privileged operations. No batch file, script launcher, Python
runtime, VBS launcher, or C# launcher is required.

For development without UAC:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
go run . --no-browser
```

Supported user-facing CLI options are:

- `--no-browser`: start the local WebUI and print the tokenized URL instead of
  opening a browser.
- `--port <port>`: bind the local WebUI to an explicit port; startup fails if
  it is unavailable.
- `--token <token>`: use a caller-provided bootstrap token.
- `--task auto-update`: run the scheduled auto-update task once and print JSON
  results.
- `--help`: print usage.

`--no-elevate` is not supported. The executable manifest is already
`asInvoker`; the WebUI runs unelevated and only individual privileged actions
use the typed elevated worker.

For automated distribution smoke tests, use:

```powershell
powershell -ExecutionPolicy Bypass -File .\dev\scripts\Smoke-Distribution.ps1 -Exe .\dist\WindowsUpdaterWebUI.exe
```

## License

WindowsUpdateUtility is licensed under the GNU General Public License version
3 only (`GPL-3.0-only`). See [LICENSE](LICENSE).

## Notes

- Supported OS floor: Windows 10 or newer with the App Installer/WinGet stack
  available. Windows 11 is the primary validation target.
- Architecture policy: x64 is the primary release target. ARM64 is supported by
  Go builds and must be smoke-tested on ARM64 Windows hardware before release.
- Package install/update actions may download software and require administrator rights.
- Missing winget opens Microsoft App Installer.
- Missing Store CLI opens Microsoft Store and Windows Update surfaces. Store CLI
  is required for exact Store fallback evidence and execution when WinGet
  msstore cannot provide a verified Product ID path.
- WinGet msstore Product ID execution is attempted before verified Store CLI
  exact targets.
- Missing Chocolatey installs through winget when winget is available; otherwise the app opens the Chocolatey install page.
- State is stored under `%LOCALAPPDATA%\WindowsUpdaterWebUI\state.json` by default.
- Store scan snapshots are stored under `%LOCALAPPDATA%\WindowsUpdaterWebUI\store-scans\user-<hash>\`, with `current.json` pointing at immutable generation files.
- Log retention is bounded in memory by category and job. Exported logs include
  retained tails, not an unbounded complete history.
- Internal unsupported modes such as `--elevated-worker` and
  `--store-inventory-worker` are same-binary implementation details for typed
  privilege isolation and killable current-user Store inventory. They are not a
  public automation interface.
- Hosted CI does not perform real Microsoft Store live validation. Store live
  tests are opt-in and must run on controlled Windows hardware with explicit
  environment gates.
