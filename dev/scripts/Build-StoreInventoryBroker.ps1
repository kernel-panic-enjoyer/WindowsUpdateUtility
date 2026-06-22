param(
    [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
)

$ErrorActionPreference = 'Stop'
$resolvedRoot = (Resolve-Path $Root).Path
& (Join-Path $PSScriptRoot 'Set-WorkspaceBinaryPaths.ps1') -Root $resolvedRoot | Out-Null

$source = Join-Path $resolvedRoot 'native\store-inventory-broker\Program.cs'
$outputDir = Join-Path $resolvedRoot 'internal\updater\assets\broker'
$output = Join-Path $outputDir 'WindowsUpdater.StoreInventoryBroker.exe'

$cscCandidates = @(
    (Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'),
    (Join-Path $env:WINDIR 'Microsoft.NET\Framework\v4.0.30319\csc.exe')
)
$csc = $cscCandidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $csc) {
    throw 'Could not find the .NET Framework C# compiler required to build the Store inventory broker.'
}

$winMetadata = Join-Path $env:WINDIR 'System32\WinMetadata'
$frameworkDir = Split-Path -Parent $csc
$references = @(
    (Join-Path $winMetadata 'Windows.ApplicationModel.winmd'),
    (Join-Path $winMetadata 'Windows.Management.winmd'),
    (Join-Path $winMetadata 'Windows.Storage.winmd'),
    (Join-Path $winMetadata 'Windows.System.winmd'),
    (Join-Path $frameworkDir 'System.Runtime.WindowsRuntime.dll'),
    (Join-Path $frameworkDir 'System.Runtime.dll')
)
foreach ($reference in $references) {
    if (-not (Test-Path -LiteralPath $reference)) {
        throw "Missing broker compiler reference: $reference"
    }
}

New-Item -ItemType Directory -Force $outputDir | Out-Null
$args = @(
    '/nologo',
    '/target:exe',
    '/platform:anycpu',
    "/out:$output"
)
foreach ($reference in $references) {
    $args += "/reference:$reference"
}
$args += $source

& $csc @args
if ($LASTEXITCODE -ne 0) {
    throw "Store inventory broker build failed with exit code $LASTEXITCODE"
}

$file = Get-Item -LiteralPath $output
if ($file.Length -le 0) {
    throw "Store inventory broker build produced an empty executable: $output"
}

Write-Output $output
