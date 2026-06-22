using System.Security.Principal;
using System.Text.Json;
using Windows.ApplicationModel;
using Windows.Management.Deployment;

var packageFamilyName = args.Length > 0 ? args[0] : string.Empty;
var identity = WindowsIdentity.GetCurrent();

var output = new
{
    timestamp_utc = DateTimeOffset.UtcNow.ToString("O"),
    user = new
    {
        sid = identity.User?.Value ?? string.Empty,
        name = identity.Name,
        is_administrator_token = new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator)
    },
    requested_package_family_name = packageFamilyName,
    package_manager = ProbePackageManager(packageFamilyName),
    package_catalog = ProbePackageCatalog()
};

Console.WriteLine(JsonSerializer.Serialize(output, new JsonSerializerOptions { WriteIndented = true }));

static object ProbePackageManager(string packageFamilyName)
{
    try
    {
        var manager = new PackageManager();
        var packages = string.IsNullOrWhiteSpace(packageFamilyName)
            ? manager.FindPackagesForUser(string.Empty)
            : manager.FindPackagesForUser(string.Empty, packageFamilyName);

        return new
        {
            available = true,
            find_packages_for_user_current_user = true,
            packages = packages.Select(package => new
            {
                family_name = package.Id.FamilyName,
                full_name = package.Id.FullName,
                name = package.Id.Name,
                publisher_id = package.Id.PublisherId,
                version = $"{package.Id.Version.Major}.{package.Id.Version.Minor}.{package.Id.Version.Build}.{package.Id.Version.Revision}",
                installed_location = package.InstalledLocation?.Path ?? string.Empty
            }).ToArray()
        };
    }
    catch (Exception ex)
    {
        return new { available = false, error = ex.Message };
    }
}

static object ProbePackageCatalog()
{
    try
    {
        var catalog = PackageCatalog.OpenForCurrentUser();
        return new { available = catalog is not null, open_for_current_user = catalog is not null };
    }
    catch (Exception ex)
    {
        return new { available = false, error = ex.Message };
    }
}
