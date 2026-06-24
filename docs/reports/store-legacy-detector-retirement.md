# Store Legacy Detector Retirement

Recorded on 2026-06-23T20:18:12+02:00 from branch `debloat`.

## Result

The default production build no longer contains the retired Microsoft Store
display-name detector, AppX/Store fuzzy merge path, legacy Store update target
fallback, or disabled detector feature flags.

Store update truth now comes from the transactional Store scan pipeline:

1. `internal/updater/store_scan_pipeline.go` captures scan context and runs
   current-user packaged inventory plus exact catalog providers.
2. `internal/updater/store_packaged_inventory_winrt_windows.go` enumerates
   current-user packages through direct Go WinRT/AppModel calls.
3. `internal/updater/store_cli_catalog_provider.go` and
   `internal/updater/store_winget_catalog_provider.go` produce exact provider
   observations.
4. `internal/updater/store_update_model.go` reconciles evidence into explicit
   states.
5. `internal/updater/store_scan_file_repository.go` persists published scan
   snapshots atomically.
6. `internal/updater/store_update_api.go` projects Store assessments to the
   compatibility package API without manufacturing `Current` from missing
   evidence.
7. `internal/updater/store_update_execution.go` executes Store updates only
   through exact verified targets and post-action verification.

## Removed Production Paths

- Deleted `internal/updater/package_store_resolver.go`.
- Deleted legacy PowerShell AppX inventory files
  `internal/updater/package_appx.go` and
  `internal/updater/package_appx_merge.go`.
- Removed `resolveStoreAppxPackages`, `chooseStoreResolution`,
  `storeResolutionScore`, and display-name Store resolution tests.
- Removed legacy Store package update functions:
  `runStoreUpdatePackageWithFallbackContext`, `runNativeStoreUpdate`,
  `runWingetStoreUpdateFallback`, and `storeUpdateTargetCandidates`.
- Removed obsolete Store installed/update package-list parsers:
  `parseStoreInstalled`, `parseStoreUpdates`, `parseStoreUpdatePackages`,
  `storeInstalled`, and `storeUpdates`.
- Removed `store-cli-resolved` backend display/support.
- Removed disabled detector flags:
  `UPDATER_STORE_LEGACY_DETECTOR`,
  `UPDATER_STORE_DISABLE_TRANSACTIONAL_SCAN`,
  `UPDATER_NATIVE_STORE_INVENTORY_DUAL_RUN`,
  `UPDATER_STORE_UPDATE_ASSESSMENT`, and
  `UPDATER_STORE_LEGACY_UPDATE_EXECUTION`.

## Call-Graph Classification

| Area | Classification | Outcome |
| --- | --- | --- |
| `internal/updater/package_store_resolver.go`, `resolveStoreAppxPackages`, `chooseStoreResolution`, `storeResolutionScore` | Legacy detector only | Deleted. |
| `internal/updater/package_appx.go`, `internal/updater/package_appx_merge.go`, PowerShell `Get-AppxPackage` inventory | Legacy detector only | Deleted from normal detection. |
| Legacy Store/AppX merge and update-version joins | Legacy detector only | Deleted with the legacy AppX/Store merge path. |
| `runStoreSearchUpdateFallback`, `runStoreUpdatePackageWithFallbackContext`, `runNativeStoreUpdate`, `runWingetStoreUpdateFallback`, `storeUpdateTargetCandidates` | Legacy display-name update fallback | Deleted. |
| `parseStoreInstalled`, `parseStoreUpdates`, `parseStoreUpdatePackages`, `storeInstalled`, `storeUpdates`, `latestPackageVersion` | Legacy Store installed/update table parsers | Deleted. |
| `StoreResolveCache`, `StoreUpdateAssessmentCache` runtime fields | Retired runtime state | Removed from `State`; old JSON fields are read only by `internal/updater/state_migrations.go`. |
| `store_update_assessment_cache`, `store_resolve_cache` JSON loading | State migration only | Retained so old `state.json` files load safely and canonical auto-update settings survive. Saving omits retired fields. |
| `retireLegacyStoreScanSQLiteCache` and `store-scans.sqlite` handling | State/cache migration only | Retained to rename obsolete SQLite cache evidence; no SQLite rows are imported as Store truth. |
| `internal/updater/package_store.go` Store search parsing and `internal/updater/package_store_actions.go` Store install path | Shared Store search/install helper | Retained for user-initiated Store search and installation. |
| `storeUpdatesCommand` and exact Store CLI catalog provider queries | Current exact detector | Retained for read-only exact provider evidence, not installed identity matching. |
| `internal/updater/store_scan_pipeline.go`, `store_cli_catalog_provider.go`, `store_winget_catalog_provider.go`, `store_update_model.go`, `store_update_api.go` | Current exact detector | Retained. |
| `internal/updater/store_update_execution.go` and `package_actions.go` Store update path | Current exact update execution | Retained; update requests require exact Store assessment/target evidence. |
| Store resolver/AppX merge tests | Legacy detector tests | Deleted with legacy detector. |
| Exact Store provider, pipeline, API, execution, migration, and auto-update tests | Current behavior tests | Retained and updated. |

## Retained Compatibility

- `internal/updater/state_migrations.go` still reads old
  `store_update_assessment_cache` and `store_resolve_cache` JSON fields only to
  migrate settings and then save without those runtime caches.
- `internal/updater/store_scan_repository.go` still detects old
  `store-scans.sqlite` cache files and renames them to a legacy-cache filename.
  It does not import SQLite rows as Store truth and does not link SQLite.
- Store search remains available for user-initiated install search only.
- The Store CLI aggregate update command remains in the exact provider path as
  provider evidence. It no longer creates installed identity from display names.

## Verification

Commands run:

- `go mod tidy`
- `go test -count=1 ./...`
- `go vet ./...`
- bundled Node `--check internal/updater/assets/ui.js`
- `powershell -NoProfile -ExecutionPolicy Bypass -File ./dev/scripts/Build-Workspace.ps1`
- `git diff --check`
- `go tool nm dist/WindowsUpdaterWebUI.exe` retired-symbol search
- `rg -a` retired-text search against `dist/WindowsUpdaterWebUI.exe`
- `go list -deps ./...` modernc/SQLite absence check
- `go version -m dist/WindowsUpdaterWebUI.exe` modernc/SQLite absence check
- `powershell -NoProfile -ExecutionPolicy Bypass -File ./dev/scripts/Smoke-Distribution.ps1 -Port 4355 -TimeoutSeconds 300 -StoreProviderTimeoutSeconds 120`
- Safe live VP9 Store tests:
  `TestLiveStoreCLIExactVP9Assessment`,
  `TestLiveAPIPackagesVP9Assessment`, and
  `TestLiveVP9StoreCLIProductIDTargetBehavior`

Retired source/binary search patterns returned no matches for:

- `resolveStoreAppxPackages`
- `chooseStoreResolution`
- `storeResolutionScore`
- `runStoreSearchUpdateFallback`
- `runStoreUpdatePackageWithFallbackContext`
- `storeUpdateTargetCandidates`
- `Get-AppxPackage -AllUsers`
- `store-cli-resolved`
- retired `UPDATER_STORE_*` detector flags

Distribution smoke passed with the production executable and reported:

- package count: 181
- Store inventory backend: `go-winrt`
- Store health: incomplete because `store-cli-updates` was incomplete
- VP9 present with state `unknown`, reason
  `required provider incomplete or failed: store-cli-updates`

This is the expected safe failure mode: incomplete Store coverage surfaces as
Unknown, not Current.

Safe live VP9 tests passed. The exact VP9 provider test reported VP9 current at
`1.2.20.0` with Product ID `9N4D0MSMP0PT` and PFN
`Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`. The live API test kept VP9
`unknown` when aggregate Store CLI coverage was incomplete, preserving the
required Unknown-not-Current behavior. The target-behavior test confirmed that
the exact PFN target produced an authoritative negative and the Product ID was
resolvable through WinGet msstore metadata.

## Size Impact

The rebuilt production executable is 14,665,728 bytes (13.986 MiB). The
preceding SQLite-cutover build was 14,789,632 bytes, so this cleanup removes
123,904 additional bytes from the unstripped executable. Compared with the
pre-file-repository SQLite baseline of 20,980,224 bytes, the current branch is
6,314,496 bytes smaller.

The source diff currently reports 47 files changed with 409 insertions and
3,478 deletions, excluding `dist/`.

## Remaining Non-Production/Compatibility Paths

- `state_migrations.go` retains legacy JSON field readers only for old-state
  loading. Unknown legacy fields are ignored safely, and saved state omits the
  retired runtime caches.
- `retireLegacyStoreScanSQLiteCache` retains the string `store-scans.sqlite`
  only to quarantine obsolete cache files. It is not a detector and does not
  link SQLite.
- Store search remains because it is the supported user-facing path for finding
  and installing new Store packages.

## Not Run

Destructive live Store update execution was not run. It requires an actually
available Store update and explicit apply-enabled live-test flags. The safe live
VP9 scan/update-target harnesses were run instead.
