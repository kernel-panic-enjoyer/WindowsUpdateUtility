param(
    [switch]$SkipTests,
    [switch]$SkipVet,
    [switch]$TimestampedOutput,
    [switch]$Strip,
    [string]$Version = ''
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $root

function Assert-NativeSuccess {
    param(
        [string]$Label
    )
    if ($LASTEXITCODE -ne 0) {
        throw "$Label failed with exit code $LASTEXITCODE"
    }
}

$excludedPathFragments = @(
    '\.git\',
    '\dist\'
)

$goFiles = @(
    Get-ChildItem -Path $root -Recurse -Filter '*.go' -File -ErrorAction SilentlyContinue |
        Where-Object {
            $path = $_.FullName
            -not ($excludedPathFragments | Where-Object { $path.Contains($_) })
        } |
        ForEach-Object { $_.FullName }
)
if ($goFiles.Count -gt 0) {
    $unformatted = @()
    for ($start = 0; $start -lt $goFiles.Count; $start += 100) {
        $end = [Math]::Min($start + 99, $goFiles.Count - 1)
        $unformatted += @(gofmt -l $goFiles[$start..$end])
        Assert-NativeSuccess "gofmt -l"
    }
    if ($unformatted.Count -gt 0) {
        Write-Error ("Go files need formatting. Run dev\scripts\Format-Workspace.ps1:`n" + ($unformatted -join "`n"))
        exit 1
    }
}

if (-not $SkipTests) {
    go test -count=1 ./...
    Assert-NativeSuccess "go test"
}

if (-not $SkipVet) {
    go vet ./...
    Assert-NativeSuccess "go vet"
}

$bundledNode = Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe'
$node = $null
if (Test-Path -LiteralPath $bundledNode) {
    $node = $bundledNode
} else {
    $nodeCommand = Get-Command node -ErrorAction SilentlyContinue
    if ($nodeCommand) {
        $node = $nodeCommand.Source
    }
}
if ($node) {
    & $node --check internal/updater/assets/ui.js
    Assert-NativeSuccess "node --check"
}

if ($TimestampedOutput) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $output = Join-Path $root "dist\WindowsUpdaterWebUI-$stamp.exe"
} else {
    $output = Join-Path $root 'dist\WindowsUpdaterWebUI.exe'
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $output) | Out-Null
$ldflagsParts = @('-H=windowsgui')
if ($Version.Trim() -ne '') {
    if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') {
        throw "Version must be semantic, for example 0.0.1"
    }
    $ldflagsParts += "-X windows-updater-webui/internal/updater.appVersion=$Version"
}
if ($Strip) {
    $ldflagsParts += @('-s', '-w')
}
$ldflags = $ldflagsParts -join ' '
go build -ldflags $ldflags -o $output .
Assert-NativeSuccess "go build"
$builtBinary = Get-Item -LiteralPath $output
$builtMiB = [string]::Format([Globalization.CultureInfo]::InvariantCulture, '{0:N3}', ($builtBinary.Length / 1MB))
$commit = (& git rev-parse HEAD 2>$null)
if ($LASTEXITCODE -ne 0 -or -not $commit) {
    $commit = 'unknown'
}
$dirtyOutput = @(& git status --porcelain 2>$null)
$dirty = $dirtyOutput.Count -gt 0
$goVersion = (& go version)
$goos = (& go env GOOS)
$goarch = (& go env GOARCH)
$cgo = (& go env CGO_ENABLED)
$sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $output).Hash.ToLowerInvariant()
$stripped = [bool]$Strip
$metadata = [ordered]@{
    artifact = (Resolve-Path -LiteralPath $output).Path
    commit = $commit.Trim()
    dirty = $dirty
    go_version = $goVersion.Trim()
    goos = $goos.Trim()
    goarch = $goarch.Trim()
    cgo_enabled = $cgo.Trim()
    bytes = $builtBinary.Length
    sha256 = $sha256
    version = $(if ($Version.Trim() -ne '') { $Version.Trim() } else { '0.0.0-dev' })
    stripped = $stripped
    unstripped = -not $stripped
    license = 'GPL-3.0-only'
    repository = 'https://github.com/kernel-panic-enjoyer/WindowsUpdateUtility'
    linker_flags = $ldflags
    generated_at = (Get-Date).ToUniversalTime().ToString('o')
}
$metadataPath = [IO.Path]::ChangeExtension($output, '.metadata.json')
$metadata | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $metadataPath -Encoding UTF8
Write-Output "Binary size: $($builtBinary.Length) bytes ($builtMiB MiB)"
Write-Output "SHA-256: $sha256"
Write-Output "Metadata: $metadataPath"
Write-Output $output
