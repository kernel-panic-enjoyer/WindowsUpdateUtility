param()

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

if ($goFiles.Count -eq 0) {
    Write-Output 'No Go files found.'
    exit 0
}

for ($start = 0; $start -lt $goFiles.Count; $start += 100) {
    $end = [Math]::Min($start + 99, $goFiles.Count - 1)
    gofmt -w $goFiles[$start..$end]
    Assert-NativeSuccess 'gofmt'
}

Write-Output "Formatted $($goFiles.Count) Go files."
