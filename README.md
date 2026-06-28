# WindowsUpdateUtility

[![Windows CI](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/windows-ci.yml/badge.svg)](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/windows-ci.yml)
[![Browser UI CI](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/browser-ci.yml/badge.svg)](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/browser-ci.yml)
[![Release](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/release.yml/badge.svg)](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/actions/workflows/release.yml)
[![License: GPL-3.0-only](https://img.shields.io/badge/License-GPL--3.0--only-blue.svg)](LICENSE)

WindowsUpdateUtility is a Windows app updater with a local browser UI for
WinGet, Chocolatey, and Microsoft Store apps. It runs in the interactive user
session, keeps package-manager operations explicit, and treats Microsoft Store
updates conservatively so stale or ambiguous Store evidence cannot become an
actionable update.

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Install](#install)
- [Run](#run)
- [Application updates](#application-updates)
- [Safety model](#safety-model)
- [Build from source](#build-from-source)
- [Test and validate](#test-and-validate)
- [Project layout](#project-layout)
- [Troubleshooting](#troubleshooting)
- [Release process](#release-process)
- [License](#license)

## Features

- Local-only WebUI bound to `127.0.0.1` with a tokenized browser URL.
- Unified inventory for WinGet, Chocolatey, and current-user Microsoft Store
  packaged apps.
- Individual, selected, or bulk package updates.
- Explicit package installs from WinGet, Chocolatey, or Microsoft Store.
- Optional Start with Windows and scheduled auto-update support through Windows
  Task Scheduler.
- Prompt-first application self-update from GitHub Releases with SHA-256
  verification before replacement.
- Bounded session logs with category filters and export support.
- Store scan health and diagnostics export for investigating Store provider
  failures without exposing raw SIDs, credentials, tokens, or personal install
  paths.
- Light and dark themes with embedded static assets and no separate frontend
  package pipeline.

## Requirements

- Windows 10 or newer.
- App Installer / WinGet available for WinGet-backed actions.
- Microsoft Store app stack available for Store-backed actions.
- Chocolatey is optional and used only when installed or explicitly installed
  through the app.
- Administrator rights may be required for package-manager actions that modify
  machine-wide software.

Development requirements:

- Go version declared in [go.mod](go.mod).
- PowerShell.
- Node.js for `node --check internal/updater/assets/ui.js`.

## Install

Download `WindowsUpdaterWebUI.exe` from the
[latest GitHub Release](https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility/releases/latest).

Release assets are built by GitHub Actions on `windows-latest`. Do not use
local `dist\` builds as release artifacts.

## Run

Double-click `WindowsUpdaterWebUI.exe`.

The app starts the local WebUI and opens a browser tab. The main process runs
unelevated by default; UAC is requested only for specific privileged actions.

For development or scripted startup:

```powershell
go run . --no-browser
```

Supported user-facing options:

```text
WindowsUpdaterWebUI.exe [--no-browser] [--port PORT] [--token TOKEN]
WindowsUpdaterWebUI.exe --task auto-update
```

| Option | Description |
| --- | --- |
| `--no-browser` | Start the local WebUI and print the URL instead of opening a browser. |
| `--port PORT` | Bind the WebUI to a specific local port. Startup fails if the port is unavailable. |
| `--token TOKEN` | Use a caller-provided bootstrap token instead of generating one. |
| `--task auto-update` | Run the scheduled auto-update task once and print JSON results. |
| `--help`, `-h` | Print usage. |

Internal modes such as `--elevated-worker`, `--store-inventory-worker`, and
`--self-update-apply` are implementation details. They are not public
automation interfaces.

## Application updates

The Automation panel can check GitHub Releases for a newer stable version of
`WindowsUpdaterWebUI.exe`.

The self-update flow:

1. Reads the latest non-draft, non-prerelease GitHub Release.
2. Requires `WindowsUpdaterWebUI.exe`,
   `WindowsUpdaterWebUI.metadata.json`, and
   `WindowsUpdaterWebUI.exe.sha256`.
3. Downloads the executable to a temporary directory.
4. Verifies the SHA-256 checksum.
5. Starts an internal apply helper that waits for the current process to exit,
   replaces the executable, keeps a `.bak`, and restarts the app.

The app does not silently replace itself. The user must choose **Install and
Restart**.

## Safety model

WindowsUpdateUtility is intentionally strict about Microsoft Store updates:

- Store package identity is the exact `(user SID, package family name)` pair.
- Display names and fuzzy matching never establish Store update identity.
- Unknown, stale, incomplete, or conflicting Store evidence does not become
  `Current`.
- Store execution requires fresh exact evidence and an exact action target.
- Accepted Store commands require post-action verification.
- Store live validation is opt-in and should run only on controlled Windows
  hardware.

Package-manager commands are bounded and cancellation-aware. Mutable operations
own their Windows process tree through Job Object termination semantics where
applicable, and retained command output is limited while the Session Log still
streams live output.

## Build from source

Run the workspace build script from the repository root:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

The script checks formatting, runs tests, runs `go vet`, validates embedded
JavaScript syntax, and builds the Windows GUI executable under `dist\`.

For a versioned build:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1 -Version 0.0.1
```

The production executable is intentionally unstripped. The build uses
`-H=windowsgui` and does not use `-s`, `-w`, UPX, or packing. Each build writes
`dist\WindowsUpdaterWebUI.metadata.json` with provenance including commit,
dirty-worktree state, Go version, target platform, byte count, and SHA-256.

Format Go sources with:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Format-Workspace.ps1
```

## Test and validate

Common local checks:

```powershell
gofmt -w <changed-go-files>
go test -count=1 ./...
go vet ./...
node --check internal/updater/assets/ui.js
git diff --check
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1
```

To run the root and browser suites as separate reported steps:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Run-Tests.ps1
```

Distribution smoke:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\dev\scripts\Smoke-Distribution.ps1 -Exe .\dist\WindowsUpdaterWebUI.exe
```

Browser UI tests live in [tests/browser](tests/browser) and use the
`uitestsupport` build tag:

```powershell
Push-Location .\tests\browser
go test -tags uitestsupport -count=1 .
Pop-Location
```

Some Store and PackageCatalog tests require controlled Windows hardware and
explicit opt-in environment gates. Hosted CI does not perform destructive Store
updates.

## Project layout

| Path | Purpose |
| --- | --- |
| [main.go](main.go) | Thin executable entry point. |
| [internal/updater](internal/updater) | Backend, WebUI handlers, package-manager integrations, Store logic, and tests. |
| [internal/updater/assets](internal/updater/assets) | Embedded CSS, JavaScript, icons, and static assets. |
| [dev/scripts](dev/scripts) | Formatting, build, and smoke-test scripts. |
| [dev/tools](dev/tools) | Developer-only tools and validation probes. |
| [docs/architecture](docs/architecture) | Architecture decision records. |
| [docs/status](docs/status) | Release gates and implementation status. |
| [docs/testing](docs/testing) | Manual smoke-test procedures. |
| [docs/troubleshooting](docs/troubleshooting) | User-facing troubleshooting notes. |
| [tests/browser](tests/browser) | Browser regression tests. |
| [dist](dist) | Local build output. |

## Troubleshooting

- Store status `Unknown` means the app does not have enough exact,
  current-user evidence to declare the Store package current.
- Store update buttons are disabled unless the app has fresh exact evidence and
  a verified action target.
- Use **Export Store Diagnostics** from the Store scan-health panel when Store
  updates appear missing or stale.
- Use **Export Logs** from Session Log when package-manager commands fail.

More detail:

- [Microsoft Store troubleshooting](docs/troubleshooting/microsoft-store.md)
- [Release gates](docs/status/release-gates.md)
- [Microsoft Store update status](docs/status/microsoft-store-update-status.md)

## Release process

Official release executables are built and uploaded by
[.github/workflows/release.yml](.github/workflows/release.yml).

To publish a release, dispatch the **Release** workflow from `main` with a
semantic version such as `0.0.1`. The workflow builds
`dist\WindowsUpdaterWebUI.exe`, generates
`WindowsUpdaterWebUI.exe.sha256`, and creates the `v<version>` GitHub Release
with the executable, metadata, and checksum assets.

Normal CI artifacts are not release assets and are not consumed by the
self-updater.

## Contributing

Keep changes small and reviewable. For code changes, add focused regression
tests before changing behavior where practical, run the relevant validation
commands, and keep Microsoft Store identity and freshness invariants intact.

Do not add SQLite, CGO, UPX, executable stripping, packing, or fuzzy Store
identity matching.

## License

WindowsUpdateUtility is licensed under the GNU General Public License version 3
only (`GPL-3.0-only`). See [LICENSE](LICENSE).
