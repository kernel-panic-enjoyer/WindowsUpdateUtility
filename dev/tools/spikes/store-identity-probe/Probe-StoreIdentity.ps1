param(
    [string]$PackageFamilyName = ""
)

$ErrorActionPreference = "Stop"

function Get-ProcessIntegrity {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        return "administrator-token"
    }
    return "standard-token"
}

function Convert-Version {
    param($Version)
    if ($null -eq $Version) {
        return ""
    }
    return ("{0}.{1}.{2}.{3}" -f $Version.Major, $Version.Minor, $Version.Build, $Version.Revision)
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$result = [ordered]@{
    timestamp_utc = (Get-Date).ToUniversalTime().ToString("o")
    user = [ordered]@{
        sid = [string]$identity.User.Value
        name = [string]$identity.Name
        integrity = Get-ProcessIntegrity
    }
    requested_package_family_name = $PackageFamilyName
    package_manager = [ordered]@{
        available = $false
        find_packages_for_user_current_user = $false
        error = ""
        packages = @()
    }
    package_catalog = [ordered]@{
        available = $false
        open_for_current_user = $false
        error = ""
    }
}

try {
    [void][Windows.Management.Deployment.PackageManager, Windows.Management.Deployment, ContentType = WindowsRuntime]
    [void][Windows.ApplicationModel.Package, Windows.ApplicationModel, ContentType = WindowsRuntime]
    $pm = [Windows.Management.Deployment.PackageManager]::new()
    $result.package_manager.available = $true
    if ([string]::IsNullOrWhiteSpace($PackageFamilyName)) {
        $packages = $pm.FindPackagesForUser("")
    } else {
        $packages = $pm.FindPackagesForUser("", $PackageFamilyName)
    }
    $result.package_manager.find_packages_for_user_current_user = $true
    foreach ($package in $packages) {
        $result.package_manager.packages += [ordered]@{
            family_name = [string]$package.Id.FamilyName
            full_name = [string]$package.Id.FullName
            name = [string]$package.Id.Name
            publisher_id = [string]$package.Id.PublisherId
            version = Convert-Version $package.Id.Version
            installed_location = if ($null -eq $package.InstalledLocation) { "" } else { [string]$package.InstalledLocation.Path }
        }
    }
} catch {
    $result.package_manager.error = $_.Exception.Message
}

try {
    [void][Windows.ApplicationModel.PackageCatalog, Windows.ApplicationModel, ContentType = WindowsRuntime]
    $catalog = [Windows.ApplicationModel.PackageCatalog]::OpenForCurrentUser()
    if ($null -ne $catalog) {
        $result.package_catalog.available = $true
        $result.package_catalog.open_for_current_user = $true
    }
} catch {
    $result.package_catalog.error = $_.Exception.Message
}

$result | ConvertTo-Json -Depth 5
