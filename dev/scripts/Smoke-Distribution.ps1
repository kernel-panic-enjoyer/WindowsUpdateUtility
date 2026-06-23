param(
    [string]$Exe,
    [int]$Port = 4344,
    [string]$Token = "smoke-token",
    [int]$TimeoutSeconds = 240,
    [int]$StoreProviderTimeoutSeconds = 90
)

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Set-Location $root

if ([string]::IsNullOrWhiteSpace($Exe)) {
    $Exe = Join-Path $root 'dist\WindowsUpdaterWebUI.exe'
}
$Exe = (Resolve-Path $Exe).Path

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$smokeRoot = Join-Path ([System.IO.Path]::GetTempPath()) "WindowsUpdaterWebUI\smoke-$stamp"
$stateDir = Join-Path $smokeRoot 'state'
$tmpDir = Join-Path $smokeRoot 'tmp'
New-Item -ItemType Directory -Force $stateDir, $tmpDir | Out-Null

$env:UPDATER_PORT = [string]$Port
$env:UPDATER_TOKEN = $Token
$env:UPDATER_STATE_DIR = $stateDir
$env:UPDATER_TEMP_DIR = $tmpDir
$env:UPDATER_STORE_PROVIDER_TIMEOUT_SECONDS = [string]$StoreProviderTimeoutSeconds
$env:TEMP = $tmpDir
$env:TMP = $tmpDir

$stdout = Join-Path $tmpDir 'app.stdout.txt'
$stderr = Join-Path $tmpDir 'app.stderr.txt'
$arguments = @('--no-browser')

function Quote-SmokeArgument {
    param([string]$Value)
    if ($Value.Contains(' ') -or $Value.Contains('"') -or $Value.Contains("`t")) {
        return '"' + $Value.Replace('"', '\"') + '"'
    }
    return $Value
}

function Set-SmokeEnvironment {
    param(
        [System.Diagnostics.ProcessStartInfo]$StartInfo,
        [hashtable]$Overrides
    )
    $environment = $StartInfo.Environment
    if ($null -eq $environment) {
        $environment = $StartInfo.EnvironmentVariables
    }
    if ($null -eq $environment) {
        throw 'ProcessStartInfo environment collection is unavailable'
    }
    $environment.Clear()
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($key in [System.Environment]::GetEnvironmentVariables().Keys) {
        $name = [string]$key
        if ($seen.Add($name)) {
            $environment[$name] = [string][System.Environment]::GetEnvironmentVariable($name, 'Process')
        }
    }
    foreach ($entry in $Overrides.GetEnumerator()) {
        $environment[$entry.Key] = [string]$entry.Value
    }
}

function Save-SmokeProcessOutput {
    if ($null -ne $script:stdoutTask) {
        [System.IO.File]::WriteAllText($stdout, $script:stdoutTask.Result)
        $script:stdoutTask = $null
    }
    if ($null -ne $script:stderrTask) {
        [System.IO.File]::WriteAllText($stderr, $script:stderrTask.Result)
        $script:stderrTask = $null
    }
}

$startInfo = [System.Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $Exe
$startInfo.Arguments = (($arguments | ForEach-Object { Quote-SmokeArgument $_ }) -join ' ')
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
Set-SmokeEnvironment $startInfo @{
    UPDATER_PORT = [string]$Port
    UPDATER_TOKEN = $Token
    UPDATER_STATE_DIR = $stateDir
    UPDATER_TEMP_DIR = $tmpDir
    UPDATER_STORE_PROVIDER_TIMEOUT_SECONDS = [string]$StoreProviderTimeoutSeconds
    TEMP = $tmpDir
    TMP = $tmpDir
}

$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $startInfo
if (-not $process.Start()) {
    throw "failed to start $Exe"
}
$script:stdoutTask = $process.StandardOutput.ReadToEndAsync()
$script:stderrTask = $process.StandardError.ReadToEndAsync()
$base = "http://127.0.0.1:$Port"
$session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$summary = $null

try {
    $ready = $false
    for ($i = 0; $i -lt 90; $i++) {
        if ($process.HasExited) {
            $process.WaitForExit()
            Save-SmokeProcessOutput
            $out = Get-Content -LiteralPath $stdout -Raw -ErrorAction SilentlyContinue
            $err = Get-Content -LiteralPath $stderr -Raw -ErrorAction SilentlyContinue
            throw "process exited early code=$($process.ExitCode) stdout=$out stderr=$err"
        }
        try {
            $response = Invoke-WebRequest -Uri "$base/?token=$Token" -WebSession $session -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    if (-not $ready) {
        throw 'server did not accept bootstrap token'
    }

    $packages = $null
    for ($i = 0; $i -lt $TimeoutSeconds; $i++) {
        $response = Invoke-WebRequest -Uri "$base/api/packages" -WebSession $session -UseBasicParsing -TimeoutSec 15
        $packages = $response.Content | ConvertFrom-Json
        if (-not $packages.loading) {
            break
        }
        Start-Sleep -Seconds 1
    }
    if ($packages.loading) {
        throw "packages still loading after $TimeoutSeconds second(s)"
    }

    $vp9 = @($packages.packages | Where-Object {
        $_.installed_package_family_name -eq 'Microsoft.VP9VideoExtensions_8wekyb3d8bbwe' -or
        $_.id -eq 'Microsoft.VP9VideoExtensions_8wekyb3d8bbwe'
    }) | Select-Object -First 1
    $health = $packages.store_scan_health
    $summary = [pscustomobject]@{
        pid                 = $process.Id
        port                = $Port
        state_dir           = $stateDir
        temp_dir            = $tmpDir
        store_provider_timeout_seconds = $StoreProviderTimeoutSeconds
        store_inventory_backend = 'go-winrt'
        package_count       = @($packages.packages).Count
        loading             = [bool]$packages.loading
        store_health        = $health.status
        store_authoritative = $health.authoritative
        store_message       = $health.reason
        vp9_found           = [bool]$vp9
        vp9_state           = if ($vp9) { $vp9.update_state } else { $null }
        vp9_reason          = if ($vp9) { $vp9.update_reason } else { $null }
        vp9_version         = if ($vp9) { $vp9.version } else { $null }
        vp9_available       = if ($vp9) { $vp9.available_version } else { $null }
        vp9_product_id      = if ($vp9) { $vp9.store_product_id } else { $null }
        vp9_exact_identity  = if ($vp9) { $vp9.exact_identity_available } else { $null }
        vp9_exact_target    = if ($vp9) { $vp9.exact_action_target_available } else { $null }
    }
} finally {
    try {
        Invoke-WebRequest -Uri "$base/shutdown" -Method Post -WebSession $session -Headers @{ Origin = $base } -UseBasicParsing -TimeoutSec 5 | Out-Null
    } catch {
    }
    if ($process -and -not $process.WaitForExit(15000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $process.WaitForExit()
    }
    Save-SmokeProcessOutput
}

if ($null -eq $summary) {
    throw 'smoke did not produce summary'
}
if (Get-Process -Id $process.Id -ErrorAction SilentlyContinue) {
    throw "smoke process $($process.Id) is still running"
}

$summary | ConvertTo-Json -Depth 6
