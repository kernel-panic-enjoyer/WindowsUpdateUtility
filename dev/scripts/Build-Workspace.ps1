param(
    [switch]$SkipTests,
    [switch]$SkipVet,
    [switch]$TimestampedOutput
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
    for ($start = 0; $start -lt $goFiles.Count; $start += 100) {
        $end = [Math]::Min($start + 99, $goFiles.Count - 1)
        gofmt -w $goFiles[$start..$end]
        Assert-NativeSuccess "gofmt"
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

$nodeCandidates = @(
    (Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin\node.exe'),
    'node'
)
$node = $nodeCandidates | Where-Object { $_ -eq 'node' -or (Test-Path -LiteralPath $_) } | Select-Object -First 1
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

go build -ldflags='-H=windowsgui' -o $output .
Assert-NativeSuccess "go build"
Write-Output $output
