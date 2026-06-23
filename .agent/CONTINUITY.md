[PLANS]

- 2026-06-22T22:49:00+02:00 [USER] Active objective: clean up `C:\Users\User\Documents\Updater` so the repo tree is readable and not cluttered with obsolete sidecars, generated binaries, smoke state, or stale developer artifacts.
- 2026-06-22T22:53:39+02:00 [USER] Superseded operational preference: use normal toolchain/temp defaults and build final executables under `dist\`.
- 2026-06-23T16:31:21+02:00 [USER] Active objective: fix remaining Microsoft Store parser correctness issues only; preserve Store identity/evidence/persistence/execution/UI/privilege architecture and avoid unrelated package-manager/UI changes.
- 2026-06-23T16:39:55+02:00 [USER] Active objective: explain and correct why stale/no-target Store assessments appear in the primary `Updates Available` queue.
- 2026-06-23T16:46:42+02:00 [USER] Active objective: move per-row update diagnostics under `Updates Available` into a popup instead of inline row expansion.
- 2026-06-23T16:56:54+02:00 [USER] Active objective: stop stale retained Store evidence from rendering as `Update available` in Installed Packages.
- 2026-06-23T17:04:26+02:00 [USER] Active objective: clean the Package Managers Store row by moving the AppX inventory note into Details and aligning the Details button next to the availability badge.
- 2026-06-23T17:10:48+02:00 [USER] Active objective: move per-package Diagnostics buttons out of package name cells so the updates table name column stays readable.
- 2026-06-23T17:14:22+02:00 [USER] Active objective: match the Store manager `Details` button border height to the adjacent `Available` badge.
- 2026-06-23T17:19:07+02:00 [USER] Active objective: move the session-log connection indicator from Session Log/global notices into the top dashboard banner.

[DECISIONS]

- 2026-06-22T22:49:00+02:00 [CODE] Project is a Go Windows updater WebUI utility with tray integration, local tokenized WebUI, winget/Chocolatey/Store package operations, scheduled tasks, update queues, session logs, and Store diagnostics.
- 2026-06-22T22:49:00+02:00 [CODE] Store installed identity is `(user SID, package family name)`; display names, fuzzy matching, punctuation-stripped normalization, and search rank must not produce Store identity or update truth.
- 2026-06-22T22:49:00+02:00 [CODE] Store package inventory now runs in-process through direct Go WinRT/AppModel calls; the C# `WindowsUpdater.StoreInventoryBroker.exe` sidecar is obsolete and should not be restored to production.
- 2026-06-22T22:49:00+02:00 [CODE] WebUI/coordinator runs in the interactive user context; privileged Chocolatey/scheduler/system work goes through typed elevated-worker operations. Winget package install/update runs in the current user context.
- 2026-06-22T22:49:00+02:00 [CODE] `--no-tray` support was removed at user request; normal app startup always attempts tray integration. Avoid distribution smoke tests unless starting the real tray path is acceptable.
- 2026-06-22T22:49:00+02:00 [CODE] Developer/testing helpers live under `dev\`; production code lives under `internal\updater`; final executable output lives under `dist\`.

[PROGRESS]

- 2026-06-22T22:49:00+02:00 [CODE] Removed production Store broker path: deleted `native\store-inventory-broker`, `dev\scripts\Build-StoreInventoryBroker.ps1`, embedded broker asset, broker embed declaration, and broker JSON protocol code.
- 2026-06-22T22:49:00+02:00 [CODE] Added `internal\updater\store_packaged_inventory_winrt_windows.go`, which calls `Windows.Management.Deployment.PackageManager.FindPackagesForUser("")` via Go WinRT ABI and preserves the existing `StorePackagedAppInventoryProvider` interface.
- 2026-06-22T22:49:00+02:00 [CODE] Removed obsolete `dev\tools\spikes\store-broker-dotnet`; kept `dev\tools\spikes\store-identity-probe` as the remaining Store identity diagnostic probe.
- 2026-06-22T22:49:00+02:00 [CODE] Cleaned root clutter by deleting stale `.state`, `.tmp`, `.tmp-bin`, empty `.agents`, empty old docs folders, and stale `dist\bin\WindowsUpdater.StoreInventoryBroker.exe`.
- 2026-06-22T22:49:00+02:00 [CODE] Updated README, ADR, native Store smoke doc, smoke script output, and `.gitignore` to reflect the direct Go WinRT inventory path and remove broker exceptions.
- 2026-06-22T22:46:29+02:00 [CODE] Cleanup continuation removed obsolete tracked `dev\tools\spikes\store-broker-dotnet`, cleaned empty docs folders, compressed this continuity file, and removed ignored smoke/temp/cache folders after validation.
- 2026-06-22T22:53:39+02:00 [CODE] Removed the old cache/temp path override policy from `AGENTS.md`, `README.md`, build/smoke scripts, `.gitignore`, and app temp defaults; deleted the helper script that set those paths.
- 2026-06-23T16:31:21+02:00 [CODE] Store CLI targeted update parsing now classifies negative/inapplicable phrases before generic positive text, requires successful command outcome for authoritative states, and only allows nonzero noninteractive prompt positives for exact PFN update commands.
- 2026-06-23T16:31:21+02:00 [CODE] Store CLI aggregate parsing now starts a new adjacent record when a complete record is followed by a different PFN or Update ID, matching the existing Product-ID boundary behavior.
- 2026-06-23T16:39:55+02:00 [CODE] UI `Updates Available` filtering now requires Store assessments to be fresh `available`; stale retained positives and no-target Store diagnostics remain in Installed Packages/Store details instead of the primary update queue.
- 2026-06-23T16:46:42+02:00 [CODE] Update-table diagnostics now render as a compact `Diagnostics` button that opens `package-diagnostics-modal`; provider evidence stays inspectable without expanding rows inline.
- 2026-06-23T16:56:54+02:00 [CODE] Store compatibility adapters now expose `update_available=true` only for fresh exact Store offers; stale Store positives remain `update_state=available` with `stale=true` for diagnostics but are not updateable or sorted as updates.
- 2026-06-23T17:04:26+02:00 [CODE] Package Managers Store row now renders only the Store CLI path plus an availability cluster; the `Details` button sits beside the `Available`/`Missing` badge, and the AppX inventory note moved into the Store status modal.
- 2026-06-23T17:10:48+02:00 [CODE] Update-row Diagnostics buttons now render inside the Available cell via `packageAvailableCell(..., {diagnostics:true})`; `packageNameCell` no longer accepts or renders diagnostics.
- 2026-06-23T17:14:22+02:00 [CODE] `.manager-details-button` now uses the same compact min-height, padding, radius, and font scale as the adjacent `.badge` pill.
- 2026-06-23T17:19:07+02:00 [CODE] `log-connection-status` now renders in the dashboard hero banner as `connection-badge`; Session Log controls no longer include the badge, and log reconnects update the badge without showing a large notice.

[DISCOVERIES]

- 2026-06-22T22:49:00+02:00 [TOOL] Live read-only VP9 harness passed with direct Go WinRT inventory and Store CLI exact catalog evidence; VP9 was current at `1.2.20.0`, so destructive update execution was not run.
- 2026-06-22T22:49:00+02:00 [TOOL] `go vet` initially rejected COM object `uintptr` reuse; WinRT COM handles were changed to `unsafe.Pointer`, and HSTRING buffers are copied into Go-owned UTF-16 memory before decoding.
- 2026-06-22T22:49:00+02:00 [ASSUMPTION] Real Windows-only gaps remain unless explicitly rerun: multi-user/session behavior, Store policy/offline states, localized Store output variation, and live Store update execution when an update is actually available.
- 2026-06-23T16:31:21+02:00 [TOOL] Baseline focused Store tests passed before new regressions; after adding regressions they failed on negative phrase precedence, nonzero command trust, and PFN-first adjacent records, confirming the intended defects.
- 2026-06-23T16:31:21+02:00 [TOOL] Safe live VP9 harnesses passed: exact PFN harness reported VP9 current at `1.2.20.0` with Product ID `9N4D0MSMP0PT`; API harness stayed Unknown because aggregate Store CLI coverage was incomplete; destructive live update was not run.
- 2026-06-23T16:39:55+02:00 [CODE] Root cause for stale Store rows in `Updates Available`: `packageNeedsUpdateAttention` included stale Store assessments and `packageHasUpdate` treated any Store `available` state as an update, even when `stale=true` and no exact target was available.
- 2026-06-23T16:56:54+02:00 [CODE] Root cause for stale rows in Installed Packages: `packageAvailableCell` rendered state `available` before checking `stale`, `stateBadge` labeled stale positives as `Available (Stale)`, and `inventoryPackageSortGroup` promoted any Store `update_state=available`.

[OUTCOMES]

- 2026-06-22T22:49:00+02:00 [TOOL] Last full validation before cleanup continuation: focused WinRT provider tests passed, live read-only VP9 Store harness passed, `dev\scripts\Build-Workspace.ps1` passed, and `dist\WindowsUpdaterWebUI.exe` was rebuilt.
- 2026-06-22T22:53:39+02:00 [CODE] Repo-specific Go/cache/temp directories are no longer intentional; smoke isolation uses system temp, and app temp defaults to OS temp unless `UPDATER_TEMP_DIR` is explicitly set.
- 2026-06-22T22:46:29+02:00 [TOOL] Cleanup validation: `dev\scripts\Build-Workspace.ps1` passed, `git diff --check` passed with CRLF warnings only, no empty non-git directories remain, root now contains only `.agent`, `.git`, `dev`, `dist`, `docs`, `internal`, and project files, and `dist\` contains only `WindowsUpdaterWebUI.exe`.
- 2026-06-22T22:53:39+02:00 [TOOL] Path-policy removal validation: `dev\scripts\Build-Workspace.ps1` passed after code edits; trace search for old repo-specific cache/temp policy terms found no matches before this continuity update.
- 2026-06-23T16:31:21+02:00 [TOOL] Parser-fix validation passed: focused Store tests, `go test -count=1 ./...` outside sandbox, `go vet ./...`, bundled Node `--check internal/updater/assets/ui.js`, `git diff --check`, canonical `dev\scripts\Build-Workspace.ps1`, distribution smoke, and safe live VP9 harnesses.
- 2026-06-23T16:39:55+02:00 [TOOL] Stale Store queue validation passed: focused Store UI smoke tests, `go test -count=1 ./...`, `go vet ./...`, bundled Node `--check internal/updater/assets/ui.js`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T16:46:42+02:00 [TOOL] Diagnostics-popup validation passed: UI smoke tests, bundled Node `--check internal/updater/assets/ui.js`, `go test -count=1 ./...`, `go vet ./...`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T16:56:54+02:00 [TOOL] Stale Store evidence validation passed: focused stale regression tests, `go test -count=1 ./...`, `go vet ./...`, bundled Node `--check internal/updater/assets/ui.js`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T17:04:26+02:00 [TOOL] Store manager-row cleanup validation passed: focused UI smoke test, bundled Node `--check internal/updater/assets/ui.js`, `go test -count=1 ./...`, `go vet ./...`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T17:10:48+02:00 [TOOL] Diagnostics-button relocation validation passed: focused UI smoke test, bundled Node `--check internal/updater/assets/ui.js`, `go test -count=1 ./...`, `go vet ./...`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T17:14:22+02:00 [TOOL] Store manager `Details` button sizing validation passed: focused UI smoke test, bundled Node `--check internal/updater/assets/ui.js`, `go test -count=1 ./...`, `go vet ./...`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
- 2026-06-23T17:19:07+02:00 [TOOL] Connection-badge relocation validation passed: focused UI smoke test, bundled Node `--check internal/updater/assets/ui.js`, `go test -count=1 ./...`, `go vet ./...`, and `dev\scripts\Build-Workspace.ps1`; rebuilt `dist\WindowsUpdaterWebUI.exe`.
