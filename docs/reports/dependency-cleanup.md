# Dependency / Build-Graph Cleanup Report

**This is a repository / build-graph cleanup, not a production-executable
optimization.** chromedp was never linked into the shipped binary, so the
expected — and measured — executable-size delta is **zero**. The win is that the
root module no longer carries browser-only or live-only test dependencies in its
module graph, `go.mod`, or `go.sum`.

Measured with Go `go1.26.0 windows/amd64`.

> Environment note: the host's system Go was `1.19.4`, which cannot compile this
> `go 1.26` module (`golang.org/x/sys@v0.44.0` requires Go ≥ 1.25) and predates
> toolchain auto-download (Go ≥ 1.21). A Go 1.26.0 toolchain was provisioned
> locally to build/test/measure; no system state was changed. CI/owner
> environments are expected to already provide Go ≥ 1.26.

## What changed

| Concern | Before | After |
| --- | --- | --- |
| chromedp browser tests | `internal/updater/browser_test.go` (white-box, `package updater`) | `tests/browser/` — separate module `windows-updater-webui/tests/browser` (black-box, `package browser`) |
| chromedp / cdproto | direct `require` in root `go.mod` | only in `tests/browser/go.mod` |
| Browser-test access to internals | white-box (unexported symbols) | build-tag-gated exported surface `internal/updater/uitestsupport.go` (`//go:build uitestsupport`); imports only stdlib |
| Destructive live Store tests | always-compiled (env-gated only) | `//go:build storelive` tag **and** existing `UPDATER_RUN_STORE_LIVE_*` env gates |
| `parseStoreCLIVersion` regression | inside the live-test file | `internal/updater/store_cli_version_test.go` (stays in default suite) |
| Large/repeated Store CLI samples | inline raw strings in `store_cli_catalog_provider_test.go` | `internal/updater/testdata/storecli/<version>/<locale>/*.txt` + `loadStoreCLIFixture` |

## Task 9 measurements

### Root `go.mod` — before

```
module windows-updater-webui

go 1.26

require (
	github.com/chromedp/cdproto v0.0.0-20260321001828-e3e3800016bc
	github.com/chromedp/chromedp v0.15.1
	golang.org/x/sys v0.44.0
)

require (
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
)
```

### Root `go.mod` — after

```
module windows-updater-webui

go 1.26

require golang.org/x/sys v0.44.0
```

### Root transitive module count (`go list -m all`)

| | Before | After |
| --- | ---: | ---: |
| Modules in graph | **11** | **2** |
| `go.sum` lines | 21 | 2 |

Before (11):

```
windows-updater-webui
github.com/chromedp/cdproto v0.0.0-20260321001828-e3e3800016bc
github.com/chromedp/chromedp v0.15.1
github.com/chromedp/sysutil v1.1.0
github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433
github.com/gobwas/httphead v0.1.0
github.com/gobwas/pool v0.2.1
github.com/gobwas/ws v1.4.0
github.com/ledongthuc/pdf v0.0.0-20220302134840-0c2507a12d80
github.com/orisano/pixelmatch v0.0.0-20220722002657-fb0b55479cde
golang.org/x/sys v0.44.0
```

After (2):

```
windows-updater-webui
golang.org/x/sys v0.44.0
```

The nine removed modules (chromedp, cdproto, sysutil, go-json-experiment,
gobwas/{httphead,pool,ws}, ledongthuc/pdf, orisano/pixelmatch) were all reached
**only** through the browser tests. They now live in the `tests/browser` module's
graph instead.

### Production executable size

Build command: `go build -ldflags=-H=windowsgui -o <out> .`

| | Bytes | MiB |
| --- | ---: | ---: |
| Before | 14,727,168 | 14.045 |
| After | 14,727,168 | 14.045 |
| **Delta** | **0** | **0.000** |

**Expected delta ≈ 0, observed delta = 0 bytes (byte-identical).** Confirms
chromedp/cdproto never contributed to the production binary; this work changed
the module/test build graph only.

### Production package graph (`go list -deps ./...`)

- 215 packages, unchanged before/after.
- `chromedp` packages in the production graph: **0** (before and after).

## Test coverage preserved (no tests deleted)

| Suite | How to run | Count |
| --- | --- | ---: |
| Root unit/integration | `go test ./...` | unchanged minus the moved browser/live tests |
| Browser UI | `cd tests/browser && go test -tags uitestsupport ./...` | 6 tests (was 6, relocated intact) |
| Live Microsoft Store | `go test -tags storelive ./internal/updater/ -run TestLive` + `UPDATER_RUN_STORE_LIVE_*` | 4 tests (relocated intact, env-gated) |

`TestParseStoreCLIVersion` (non-destructive) remains in the default suite. All
named semantic regressions still run in the default suite: VP9 positive, negative
phrase precedence, nonzero command rejection, exact prompt exception,
failure-tainted aggregate output, adjacent mixed-order records, timeout
preservation, inapplicable results, identity mismatch, and stale evidence.

## Running everything

`.\Run-Tests.ps1` runs the root and browser suites independently and reports
each result; the browser suite is auto-skipped when no Chromium/Edge is present.
Add `-Live` to include the gated Store tests.
