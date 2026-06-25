# Release Gates

This project has correctness boundaries that hosted CI can verify and boundaries
that require controlled Windows hardware.

## Automated CI Gates

| Invariant | Automated check |
| --- | --- |
| Source formatting is intentional | `gofmt -l` through `dev/scripts/Build-Workspace.ps1` |
| Go unit and integration regressions pass | `go test -count=1 ./...` on Windows |
| Static checks pass | `go vet ./...` |
| Embedded WebUI JavaScript parses | `node --check internal/updater/assets/ui.js` |
| The Windows GUI executable builds unstripped | `go build -ldflags=-H=windowsgui` |
| Process-tree cancellation works | focused Windows process-tree helper test |
| File-backed state survives concurrent writers | focused StateStore helper-process tests |
| Package mutations are machine-wide serialized | focused package-mutation helper-process tests |
| Distribution smoke starts the built executable | `dev/scripts/Smoke-Distribution.ps1` |
| SQLite/modernc stay out of the default binary | dependency and binary metadata assertions |
| Artifact provenance is recorded | build metadata JSON with commit, dirty flag, byte count, SHA-256 |

## Manual or Controlled-Hardware Gates

| Invariant | Required validation |
| --- | --- |
| Real Microsoft Store live updates remain exact and current-user scoped | Opt-in Store live tests on controlled Windows hardware |
| PackageCatalog events do not leak across users | Multi-user Windows validation |
| ARM64 support | Native ARM64 Windows smoke and package-manager scan/update dry runs |
| Store CLI version differences are understood | Safe Store provider tests against the installed Store CLI version |

Hosted CI must not claim that it executed destructive Store updates or real
Store live validation unless those opt-in gates are explicitly enabled on the
runner.
