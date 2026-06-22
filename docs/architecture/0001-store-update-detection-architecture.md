# ADR 0001: Microsoft Store Update Detection

Date: 2026-06-21

Status: Active

## Decision

Use a medium-integrity Go WebUI/coordinator in the interactive user's session. Keep Microsoft Store inventory, Store catalog checks, and ordinary Store actions in that user context. Delegate only admin-required winget, Chocolatey, scheduler, and system actions to the typed elevated worker.

Use a C#/.NET WinRT broker for current-user packaged-app inventory. The broker is embedded in the Go executable and extracted at runtime.

## Identity Rules

- Installed Store identity is `(user SID, package family name)`.
- Package full name is a versioned installed instance.
- Store Product ID is catalog/action identity.
- Provider-specific update IDs are action targets, not installed identity.
- Display names, localized names, fuzzy matches, normalized punctuation-free strings, and search result rank are never Store identity.

## Update State Rules

- `Current` requires a fresh complete scan in the correct user context with required providers healthy and authoritative negative evidence.
- Provider failure, parser rejection, stale evidence, unresolved identity, incomplete coverage, or user mismatch produces `Unknown`.
- Stale or incomplete negative evidence cannot clear a fresh positive update observation.
- Store update execution requires a fresh `Available` assessment and exact verified action target.
- Store command success means accepted. Final success requires post-action verification.

## Providers

- Native current-user packaged inventory broker enumerates installed Store package families.
- Store CLI exact provider verifies PFN/Product ID with `store show <PFN>` and checks exact update state with Store CLI commands.
- Store CLI aggregate provider uses `store updates --apply false` only when output contains explicit exact PFN/Product ID evidence or explicit no-update coverage.
- WinGet msstore evidence is accepted only when it can be associated to the installed PFN exactly.
- Legacy display-name Store resolution is available only through the explicit rollback flag `UPDATER_STORE_LEGACY_DETECTOR=1`.

## Persistence

Store scan state is persisted transactionally. Published scan generations are immutable. Failed or incomplete scans do not overwrite the last completed generation. Mappings may be persisted only from exact structured evidence or verified provider associations.

## Distribution

The normal build embeds `internal/updater/assets/broker/WindowsUpdater.StoreInventoryBroker.exe`. Runtime extraction writes the broker beside the app or under `UPDATER_BINARY_DIR`.

## Known Gaps

- PackageCatalog event verification is not implemented in this build; verification uses exact inventory polling and targeted catalog checks.
- Broader Windows matrix validation remains release-gate work.
- Store CLI behavior varies by version. Product ID is attempted first through WinGet msstore when available, with verified Store CLI exact targets used as fallback.
