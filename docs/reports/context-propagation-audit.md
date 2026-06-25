# Context Propagation Audit

Date: 2026-06-25T13:50:15+02:00

## Cancellable Paths

- `App.refreshInventory`, queued inventory refreshes, and `refreshInventorySyncContext` pass the App/job context into inventory collection and do not publish cancelled results.
- `getInventoryContext` passes the caller context through manager detection, WinGet list/export/update checks, Chocolatey list/outdated checks, and native Store inventory enumeration.
- `App.refreshStatus`, queued status refreshes, and `refreshStatusSyncContext` pass the App/job context through scheduled-task status checks and manager detection.
- `scanInstalledApplicationsWithStore` passes its context into managed WinGet and native Store scan readers.
- Scheduled auto-update creates one top-level context in `runAutoUpdate`, then passes it into inventory collection, Store scan projection, update execution, and result persistence.

## Intentional Background Wrappers

Remaining production `context.Background()` calls are kept only at synchronous entry-point wrappers or process lifecycle boundaries:

- App root context creation and HTTP server shutdown timeout.
- Legacy-compatible wrappers such as `getInventory`, `refreshInventorySync`, `buildStatusResponse`, `refreshStatusSync`, `inventorySnapshot`, `statusSnapshot`, manager detection wrappers, command wrappers, package-action wrappers, and scan-reader wrappers.
- Top-level UI/task actions that are not passed a request/job context, including settings writes, scheduled auto-update startup, startup/auto-update task helpers, and Store install/package action convenience wrappers.
- Test/parser helpers that need a non-cancellable context for pure parsing or unit-only execution.

New cancellable code should call the `*Context` variants directly instead of these wrappers.
