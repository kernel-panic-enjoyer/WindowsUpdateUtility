# Microsoft Store Update Status

The Store update model separates **Unknown** from **Current** on purpose.

`Current` means the app completed a fresh Store scan in the interactive user's session, all required Store providers were healthy, the installed package identity was exact, and the providers returned authoritative negative update evidence.

`Unknown` means the app does not have enough trustworthy evidence to say the Store app is current. Common causes include provider failure, incomplete scans, stale data, unresolved package identity, missing exact Store action targets, or user-context mismatch.

An update can remain visible as stale after an incomplete rescan. That prevents a failed or partial scan from erasing a previously verified positive update offer.

Store update buttons are enabled only when an exact verified action target is available. The WebUI must not silently fall back to display-name Store searches for updates.

## Detector Cutover

The transactional Store detector is the default. Legacy Store heuristics are not
used as a silent fallback. If the new detector cannot complete, Store package
status remains `Unknown` and the scan-health panel explains which provider or
identity requirement failed.

The one-release emergency rollback is explicit:

```cmd
set UPDATER_STORE_LEGACY_DETECTOR=1
```

This flag re-enables the old Store detector path for emergency diagnosis only.
It should not be used to treat display-name or fuzzy matches as durable Store
identity.

## Cutover And Compliance Notes

The old Store detector could infer update truth from AppX inventory,
human-readable Store CLI or WinGet output, display-name Store searches, and
normalized name or ID matching. The new detector intentionally reports
`Unknown` where that evidence is ambiguous. This can reduce apparent coverage,
but it prevents false `Current` results and cross-user Store identity mixing.

Core enforcement points:

- Store identity is the exact `(user SID, package family name)` pair, enforced
  by the native inventory model, scan pipeline, and Store update request
  validation.
- `Package.UpdateAvailable` is a compatibility field and is true only when
  `update_state == available`.
- Provider failures, incomplete scans, unresolved identities, stale evidence,
  and parser rejection become `Unknown`, not `Current`.
- Exact Store update execution is gated by a fresh available assessment and a
  verified Product ID or provider-specific exact target.
- Legacy Store heuristics require explicit `UPDATER_STORE_LEGACY_DETECTOR=1`.

Release-gate checks before shipping:

- `powershell -ExecutionPolicy Bypass -File .\dev\scripts\Build-Workspace.ps1`
  passes.
- `dev\scripts\Smoke-Distribution.ps1` passes against the built distribution.
- Store diagnostics export contains no raw user SID, credentials, tokens,
  account identifiers, or unnecessary personal paths.
- Store packages never show `Current` when provider coverage is failed,
  incomplete, stale, unresolved, unsupported, or conflicting.
- Store update buttons are disabled unless an exact verified action target
  exists.

Windows matrix validation is still required for Windows 10/11, administrator
and standard-user accounts, separate elevation credentials, multiple signed-in
users, Store signed in/out, offline or intermittent networking, Store disabled
by policy, localized UI, Store/WinGet version variation, and
main/framework/optional/resource/bundle packages.

## Exact Update Execution

For Store packages with assessment data, update execution uses only the verified
Store Product ID or provider-specific exact target. A successful command return
means the request was accepted. Final success is reported only after
post-action verification.

Execution attempts the verified Product ID first through WinGet msstore when
WinGet is available. If that exact Product ID action is not accepted, the
executor may try Store CLI exact targets from the same assessment: first the
verified Product ID, then the verified provider-specific update ID. It must not
search by display name or use ranked Store results as an update target.

On the current Store CLI tested locally (`v22605.1401.12.0`), `store show
<PFN>` returns an exact PFN/Product ID association, while `store update
<update-id> --apply false` accepts the installed package family name as the
exact update ID for VP9 Video Extensions. The Product ID remains catalog
identity evidence; the provider-specific Store CLI update target is stored
separately as `store_update_id`.

The Store CLI aggregate provider also runs `store updates --apply false`. Its
output is used as corroborating evidence only when it is explicit: `No updates
found.` can support authoritative negative coverage, and positive rows are used
only when they include exact PFN/Product ID data. Exact rows that report a newer
catalog version with no applicable installer become `Inapplicable`, not an
updateable package. Empty output, parser rejection, display names, prompts, and
rank/order evidence do not become Store update truth.

WinGet Microsoft Store update rows are retained only when they include an exact
installed package family association, such as a `PackageFamilyName:` match
column or a full MSIX package identity that can be reduced to the same PFN. Rows
that only contain a display name, truncated ID, or rank/order evidence are kept
as diagnostics and do not produce update truth. A WinGet Product-ID query that
only says there is no applicable upgrade is also diagnostic unless it returns an
exact PFN association for the installed package.

Current verification supports exact current-user inventory polling and targeted
catalog checks. The targeted Store CLI fallback re-runs `store show <PFN>` and
requires the PFN plus verified Product ID to match before it treats
`store update <PFN> --apply false` output as authoritative. Event-based
verification is not implemented in this build; the event source returns an
explicit unavailable diagnostic instead of claiming PackageCatalog event
coverage.

Exact current-user re-enumeration remains based on the documented
`Windows.Management.Deployment.PackageManager.FindPackagesForUser` family of
APIs:
https://learn.microsoft.com/en-us/uwp/api/windows.management.deployment.packagemanager.findpackagesforuser

If the Store command is accepted but inventory/catalog verification cannot prove
completion, the job reports `accepted_not_verified` rather than success.

## Scan-Scope Measurement

Measured during local distribution smoke on `2026-06-22` with Store CLI
`v22605.1401.12.0` and WinGet `v1.28.240`:

- Product-like current-user Store package families: `112`.
- Native current-user inventory provider duration: about `2s`.
- Store CLI exact provider duration: about `21s`.
- Store CLI aggregate provider duration: about `4s`.
- WinGet msstore exact provider duration: about `1s`.
- Store CLI exact provider package-level results: `46` authoritative negatives
  and `66` incomplete results.
- Verified PFN/Product-ID mappings persisted in that scan: `50`.
- Persisted mapping reuse in the current scan path: `0`; mappings are freshly
  verified from provider output in the active scan before use.

No caching optimization was added in this hardening pass. Reusing persisted
mappings safely needs explicit expiry, conflict, same-user/same-PFN validation,
and separate rules ensuring cached mappings cannot manufacture `Current`.

## VP9 Live Acceptance Evidence

The VP9 Video Extensions acceptance fixture was validated on Windows
`10.0.26200.8655` with Store CLI `v22605.1401.12.0` and WinGet `v1.28.240`.

- Installed PFN: `Microsoft.VP9VideoExtensions_8wekyb3d8bbwe`
- Verified Store Product ID: `9N4D0MSMP0PT`
- Pre-update `/api/packages` state: `available`, installed version `1.2.13.0`,
  exact identity available, exact action target available.
- Execution target behavior: Product ID `9N4D0MSMP0PT` is attempted first
  through WinGet msstore exact ID execution when available. On this Store CLI
  build, direct `store update 9N4D0MSMP0PT` is rejected for VP9; the verified
  exact update ID `Microsoft.VP9VideoExtensions_8wekyb3d8bbwe` remains the Store
  CLI fallback target.
- Successful execution command: `store update Microsoft.VP9VideoExtensions_8wekyb3d8bbwe --apply true`
- Post-update state: installed version `1.2.20.0`, fresh scan returned
  `current`.

The successful live execution used the provider-specific exact Store CLI update
ID associated with the verified Product ID. On this Store CLI build, direct
`store update 9N4D0MSMP0PT --apply false` did not target the installed VP9
metadata. Current-state probing showed `winget show --id 9N4D0MSMP0PT --source
msstore` resolves the exact Product ID, and `winget upgrade --id
9N4D0MSMP0PT --source msstore` accepts the exact Product ID while reporting no
upgrade because VP9 is already current.

## Diagnostics Export

The WebUI Store scan-health panel exposes `Export Store Diagnostics`. The export
includes scan context, provider health, canonical package family names,
sanitized observations, final assessments, and Store auto-update migration
notes. It excludes raw user SIDs, tokens, credentials, account identifiers, and
personal install locations.
