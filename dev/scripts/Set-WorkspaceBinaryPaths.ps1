param(
    [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
)

$resolvedRoot = (Resolve-Path $Root).Path

$pathEnv = @{
    GOBIN                 = Join-Path $resolvedRoot '.gobin'
    GOCACHE               = Join-Path $resolvedRoot '.gocache'
    GOMODCACHE            = Join-Path $resolvedRoot '.gomodcache'
    GOPATH                = Join-Path $resolvedRoot '.gopath'
    GOTMPDIR              = Join-Path $resolvedRoot '.gotmp'
    TEMP                  = Join-Path $resolvedRoot '.tmp'
    TMP                   = Join-Path $resolvedRoot '.tmp'
    UPDATER_TEMP_DIR      = Join-Path $resolvedRoot '.tmp'
    UPDATER_BINARY_DIR    = Join-Path $resolvedRoot '.tmp-bin'
    DOTNET_CLI_HOME       = Join-Path $resolvedRoot '.dotnet'
    NUGET_PACKAGES        = Join-Path $resolvedRoot '.nuget\packages'
    NUGET_HTTP_CACHE_PATH = Join-Path $resolvedRoot '.nuget\http-cache'
    NUGET_SCRATCH         = Join-Path $resolvedRoot '.nuget\scratch'
}

$valueEnv = @{
    DOTNET_NOLOGO                     = '1'
    DOTNET_SKIP_FIRST_TIME_EXPERIENCE = '1'
    MSBUILDDISABLENODEREUSE          = '1'
}

foreach ($entry in $pathEnv.GetEnumerator()) {
    New-Item -ItemType Directory -Force $entry.Value | Out-Null
    Set-Item -Path "Env:$($entry.Key)" -Value $entry.Value
}

foreach ($entry in $valueEnv.GetEnumerator()) {
    Set-Item -Path "Env:$($entry.Key)" -Value $entry.Value
}

New-Item -ItemType Directory -Force (Join-Path $resolvedRoot 'dist') | Out-Null

[pscustomobject]@{
    Root                  = $resolvedRoot
    GOBIN                 = $env:GOBIN
    GOCACHE               = $env:GOCACHE
    GOMODCACHE            = $env:GOMODCACHE
    GOPATH                = $env:GOPATH
    GOTMPDIR              = $env:GOTMPDIR
    TEMP                  = $env:TEMP
    TMP                   = $env:TMP
    UPDATER_TEMP_DIR      = $env:UPDATER_TEMP_DIR
    UPDATER_BINARY_DIR    = $env:UPDATER_BINARY_DIR
    DOTNET_CLI_HOME       = $env:DOTNET_CLI_HOME
    NUGET_PACKAGES        = $env:NUGET_PACKAGES
    NUGET_HTTP_CACHE_PATH = $env:NUGET_HTTP_CACHE_PATH
    NUGET_SCRATCH         = $env:NUGET_SCRATCH
}
