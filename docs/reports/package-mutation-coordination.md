# Package Mutation Coordination

Package-manager mutations are serialized in the process that directly launches
the mutable command. The medium-integrity WebUI caller does not hold this lock
while waiting for an elevated worker; the elevated worker acquires it when it
runs Chocolatey or other mutable package-manager commands.

## Scope

- In-process scope: one `PackageMutationCoordinator` mutex reduces same-process
  contention before touching the Windows object.
- Cross-process scope: `Global\WindowsUpdaterWebUIPackageMutation`.
- Rationale: the lock is machine-wide rather than per-user because Chocolatey,
  source maintenance, and some installer side effects can be machine-scoped.
- ACL: the mutex security descriptor grants access to the intended current user,
  built-in Administrators, and SYSTEM.
- Separate administrator credentials: a helper elevated with administrator
  credentials can coordinate through the Administrators ACE. A different
  non-administrator account is intentionally not granted access to the mutex.

## Covered Mutations

The command classifier treats these command families as mutations:

- `winget install`, targeted or `--all` `winget upgrade`, `winget uninstall`,
  `winget import`, `winget configure`, `winget source update`, and
  `winget source reset`.
- Store CLI `install`, mutating `update`/`updates`, and `uninstall`; Store CLI
  update checks using `--apply false` remain read-only.
- Chocolatey `install`, `upgrade`, `uninstall`, and `pin`.

Read-only inventory commands continue without the package mutation coordinator.
