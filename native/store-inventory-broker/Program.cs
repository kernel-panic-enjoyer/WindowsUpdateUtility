using System;
using System.Collections.Generic;
using System.Security.Principal;
using System.Text;
using Windows.ApplicationModel;
using Windows.Management.Deployment;

public static class Program
{
    private const int ProtocolVersion = 1;

    public static int Main(string[] args)
    {
        if (args.Length != 1 || args[0] != "--inventory")
        {
            Console.Error.WriteLine("Usage: WindowsUpdater.StoreInventoryBroker.exe --inventory");
            return 2;
        }

        string input = Console.In.ReadToEnd();
        InventoryRequest request = InventoryRequest.Parse(input);
        string currentSid = CurrentUserSid();
        DateTimeOffset started = DateTimeOffset.UtcNow;

        try
        {
            if (request.ProtocolVersion != ProtocolVersion)
            {
                throw new InvalidOperationException("Unsupported protocol version " + request.ProtocolVersion + ".");
            }
            if (!String.Equals(currentSid, request.UserSid, StringComparison.OrdinalIgnoreCase))
            {
                throw new InvalidOperationException("Broker user SID does not match request user SID.");
            }

            PackageManager manager = new PackageManager();
            List<PackageRecord> records = new List<PackageRecord>();
            foreach (Package package in manager.FindPackagesForUser(String.Empty))
            {
                records.Add(PackageRecord.FromPackage(currentSid, package));
            }

            InventoryResponse response = new InventoryResponse();
            response.ProtocolVersion = ProtocolVersion;
            response.ScanId = request.ScanId;
            response.UserSid = currentSid;
            response.StartedAt = started;
            response.CompletedAt = DateTimeOffset.UtcNow;
            response.Complete = true;
            response.Partial = false;
            response.Records = records;
            Console.WriteLine(response.ToJson());
            return 0;
        }
        catch (Exception ex)
        {
            InventoryResponse response = new InventoryResponse();
            response.ProtocolVersion = ProtocolVersion;
            response.ScanId = request.ScanId;
            response.UserSid = request.UserSid.Length == 0 ? currentSid : request.UserSid;
            response.StartedAt = started;
            response.CompletedAt = DateTimeOffset.UtcNow;
            response.Complete = false;
            response.Partial = true;
            response.Error = ex.Message;
            response.Records = new List<PackageRecord>();
            Console.WriteLine(response.ToJson());
            return 1;
        }
    }

    private static string CurrentUserSid()
    {
        SecurityIdentifier sid = WindowsIdentity.GetCurrent().User;
        return sid == null ? String.Empty : sid.Value;
    }
}

public sealed class InventoryRequest
{
    public int ProtocolVersion;
    public string ScanId = String.Empty;
    public string UserSid = String.Empty;

    public static InventoryRequest Parse(string input)
    {
        InventoryRequest request = new InventoryRequest();
        request.ProtocolVersion = JsonHelpers.ReadInt(input, "protocol_version", 0);
        request.ScanId = JsonHelpers.ReadString(input, "scan_id");
        request.UserSid = JsonHelpers.ReadString(input, "user_sid");
        return request;
    }
}

public sealed class InventoryResponse
{
    public int ProtocolVersion;
    public string ScanId = String.Empty;
    public string UserSid = String.Empty;
    public DateTimeOffset StartedAt;
    public DateTimeOffset CompletedAt;
    public bool Complete;
    public bool Partial;
    public string Error;
    public List<PackageRecord> Records = new List<PackageRecord>();

    public string ToJson()
    {
        StringBuilder builder = new StringBuilder();
        builder.Append("{");
        JsonHelpers.AppendNumber(builder, "protocol_version", ProtocolVersion, false);
        JsonHelpers.AppendString(builder, "scan_id", ScanId, true);
        JsonHelpers.AppendString(builder, "user_sid", UserSid, true);
        JsonHelpers.AppendString(builder, "started_at", StartedAt.ToString("O"), true);
        JsonHelpers.AppendString(builder, "completed_at", CompletedAt.ToString("O"), true);
        JsonHelpers.AppendBool(builder, "complete", Complete, true);
        JsonHelpers.AppendBool(builder, "partial", Partial, true);
        if (!String.IsNullOrEmpty(Error))
        {
            JsonHelpers.AppendString(builder, "error", Error, true);
        }
        builder.Append(",\"records\":[");
        for (int i = 0; i < Records.Count; i++)
        {
            if (i > 0)
            {
                builder.Append(",");
            }
            Records[i].AppendJson(builder);
        }
        builder.Append("]}");
        return builder.ToString();
    }
}

public sealed class PackageRecord
{
    public string UserSid = String.Empty;
    public string PackageFamilyName = String.Empty;
    public string PackageFullName = String.Empty;
    public string IdentityName = String.Empty;
    public string Publisher = String.Empty;
    public string PublisherId = String.Empty;
    public PackageVersionRecord Version = new PackageVersionRecord();
    public string ProcessorArchitecture = String.Empty;
    public string InstallLocation = String.Empty;
    public string PackageType = String.Empty;
    public string Classification = String.Empty;
    public bool IsFramework;
    public bool IsResourcePackage;
    public bool IsOptional;
    public bool IsBundle;
    public bool IsDevelopmentMode;
    public bool IsStaged;
    public PackageStatusRecord Status = new PackageStatusRecord();
    public string DisplayName = String.Empty;

    public static PackageRecord FromPackage(string userSid, Package package)
    {
        PackageId id = package.Id;
        PackageRecord record = new PackageRecord();
        record.UserSid = userSid;
        record.PackageFamilyName = id.FamilyName;
        record.PackageFullName = id.FullName;
        record.IdentityName = id.Name;
        record.Publisher = id.Publisher;
        record.PublisherId = id.PublisherId;
        record.Version = new PackageVersionRecord(id.Version.Major, id.Version.Minor, id.Version.Build, id.Version.Revision);
        record.ProcessorArchitecture = id.Architecture.ToString();
        record.InstallLocation = package.InstalledLocation == null ? String.Empty : package.InstalledLocation.Path;
        record.PackageType = package.GetType().FullName ?? "Windows.ApplicationModel.Package";
        record.IsFramework = package.IsFramework;
        record.IsResourcePackage = package.IsResourcePackage;
        record.IsOptional = package.IsOptional;
        record.IsBundle = package.IsBundle;
        record.IsDevelopmentMode = package.IsDevelopmentMode;
        record.IsStaged = package.Status.IsPartiallyStaged;
        record.Classification = record.IsResourcePackage ? "resource" :
            record.IsFramework ? "framework" :
            record.IsOptional ? "optional" :
            record.IsBundle ? "bundle" :
            "main";
        record.Status = PackageStatusRecord.FromStatus(package.Status);
        record.DisplayName = package.DisplayName ?? String.Empty;
        return record;
    }

    public void AppendJson(StringBuilder builder)
    {
        builder.Append("{");
        JsonHelpers.AppendString(builder, "user_sid", UserSid, false);
        JsonHelpers.AppendString(builder, "package_family_name", PackageFamilyName, true);
        JsonHelpers.AppendString(builder, "package_full_name", PackageFullName, true);
        JsonHelpers.AppendString(builder, "identity_name", IdentityName, true);
        JsonHelpers.AppendString(builder, "publisher", Publisher, true);
        JsonHelpers.AppendString(builder, "publisher_id", PublisherId, true);
        builder.Append(",\"version\":");
        Version.AppendJson(builder);
        JsonHelpers.AppendString(builder, "processor_architecture", ProcessorArchitecture, true);
        JsonHelpers.AppendString(builder, "install_location", InstallLocation, true);
        JsonHelpers.AppendString(builder, "package_type", PackageType, true);
        JsonHelpers.AppendString(builder, "classification", Classification, true);
        JsonHelpers.AppendBool(builder, "is_framework", IsFramework, true);
        JsonHelpers.AppendBool(builder, "is_resource_package", IsResourcePackage, true);
        JsonHelpers.AppendBool(builder, "is_optional", IsOptional, true);
        JsonHelpers.AppendBool(builder, "is_bundle", IsBundle, true);
        JsonHelpers.AppendBool(builder, "is_development_mode", IsDevelopmentMode, true);
        JsonHelpers.AppendBool(builder, "is_staged", IsStaged, true);
        builder.Append(",\"status\":");
        Status.AppendJson(builder);
        JsonHelpers.AppendString(builder, "display_name", DisplayName, true);
        builder.Append("}");
    }
}

public sealed class PackageVersionRecord
{
    public ushort Major;
    public ushort Minor;
    public ushort Build;
    public ushort Revision;

    public PackageVersionRecord()
    {
    }

    public PackageVersionRecord(ushort major, ushort minor, ushort build, ushort revision)
    {
        Major = major;
        Minor = minor;
        Build = build;
        Revision = revision;
    }

    public void AppendJson(StringBuilder builder)
    {
        builder.Append("{");
        JsonHelpers.AppendNumber(builder, "major", Major, false);
        JsonHelpers.AppendNumber(builder, "minor", Minor, true);
        JsonHelpers.AppendNumber(builder, "build", Build, true);
        JsonHelpers.AppendNumber(builder, "revision", Revision, true);
        builder.Append("}");
    }
}

public sealed class PackageStatusRecord
{
    public bool Ok;
    public string Raw = String.Empty;
    public string VerifyError = String.Empty;
    public bool DataOffline;
    public bool DependencyIssue;
    public bool DeploymentInProgress;
    public bool Disabled;
    public bool IsPartiallyStaged;
    public bool LicenseIssue;
    public bool Modified;
    public bool NeedsRemediation;
    public bool NotAvailable;
    public bool PackageOffline;
    public bool Servicing;
    public bool Tampered;

    public static PackageStatusRecord FromStatus(Windows.ApplicationModel.PackageStatus status)
    {
        PackageStatusRecord record = new PackageStatusRecord();
        try
        {
            record.Ok = status.VerifyIsOK();
        }
        catch (Exception ex)
        {
            record.VerifyError = ex.Message;
        }
        record.Raw = status.ToString();
        record.DataOffline = status.DataOffline;
        record.DependencyIssue = status.DependencyIssue;
        record.DeploymentInProgress = status.DeploymentInProgress;
        record.Disabled = status.Disabled;
        record.IsPartiallyStaged = status.IsPartiallyStaged;
        record.LicenseIssue = status.LicenseIssue;
        record.Modified = status.Modified;
        record.NeedsRemediation = status.NeedsRemediation;
        record.NotAvailable = status.NotAvailable;
        record.PackageOffline = status.PackageOffline;
        record.Servicing = status.Servicing;
        record.Tampered = status.Tampered;
        return record;
    }

    public void AppendJson(StringBuilder builder)
    {
        builder.Append("{");
        JsonHelpers.AppendBool(builder, "ok", Ok, false);
        JsonHelpers.AppendString(builder, "raw", Raw, true);
        JsonHelpers.AppendString(builder, "verify_error", VerifyError, true);
        JsonHelpers.AppendBool(builder, "data_offline", DataOffline, true);
        JsonHelpers.AppendBool(builder, "dependency_issue", DependencyIssue, true);
        JsonHelpers.AppendBool(builder, "deployment_in_progress", DeploymentInProgress, true);
        JsonHelpers.AppendBool(builder, "disabled", Disabled, true);
        JsonHelpers.AppendBool(builder, "is_partially_staged", IsPartiallyStaged, true);
        JsonHelpers.AppendBool(builder, "license_issue", LicenseIssue, true);
        JsonHelpers.AppendBool(builder, "modified", Modified, true);
        JsonHelpers.AppendBool(builder, "needs_remediation", NeedsRemediation, true);
        JsonHelpers.AppendBool(builder, "not_available", NotAvailable, true);
        JsonHelpers.AppendBool(builder, "package_offline", PackageOffline, true);
        JsonHelpers.AppendBool(builder, "servicing", Servicing, true);
        JsonHelpers.AppendBool(builder, "tampered", Tampered, true);
        builder.Append("}");
    }
}

public static class JsonHelpers
{
    public static string ReadString(string input, string key)
    {
        string quotedKey = "\"" + key + "\"";
        int keyIndex = input.IndexOf(quotedKey, StringComparison.Ordinal);
        if (keyIndex < 0)
        {
            return String.Empty;
        }
        int colon = input.IndexOf(':', keyIndex + quotedKey.Length);
        if (colon < 0)
        {
            return String.Empty;
        }
        int start = input.IndexOf('"', colon + 1);
        if (start < 0)
        {
            return String.Empty;
        }
        StringBuilder value = new StringBuilder();
        bool escaped = false;
        for (int index = start + 1; index < input.Length; index++)
        {
            char c = input[index];
            if (escaped)
            {
                switch (c)
                {
                    case '"':
                    case '\\':
                    case '/':
                        value.Append(c);
                        break;
                    case 'b':
                        value.Append('\b');
                        break;
                    case 'f':
                        value.Append('\f');
                        break;
                    case 'n':
                        value.Append('\n');
                        break;
                    case 'r':
                        value.Append('\r');
                        break;
                    case 't':
                        value.Append('\t');
                        break;
                    default:
                        value.Append(c);
                        break;
                }
                escaped = false;
                continue;
            }
            if (c == '\\')
            {
                escaped = true;
                continue;
            }
            if (c == '"')
            {
                return value.ToString();
            }
            value.Append(c);
        }
        return String.Empty;
    }

    public static int ReadInt(string input, string key, int fallback)
    {
        string quotedKey = "\"" + key + "\"";
        int keyIndex = input.IndexOf(quotedKey, StringComparison.Ordinal);
        if (keyIndex < 0)
        {
            return fallback;
        }
        int colon = input.IndexOf(':', keyIndex + quotedKey.Length);
        if (colon < 0)
        {
            return fallback;
        }
        int start = colon + 1;
        while (start < input.Length && Char.IsWhiteSpace(input[start]))
        {
            start++;
        }
        int end = start;
        while (end < input.Length && Char.IsDigit(input[end]))
        {
            end++;
        }
        int value;
        if (end == start || !Int32.TryParse(input.Substring(start, end - start), out value))
        {
            return fallback;
        }
        return value;
    }

    public static void AppendString(StringBuilder builder, string key, string value, bool comma)
    {
        if (comma)
        {
            builder.Append(",");
        }
        builder.Append("\"");
        builder.Append(Escape(key));
        builder.Append("\":\"");
        builder.Append(Escape(value ?? String.Empty));
        builder.Append("\"");
    }

    public static void AppendBool(StringBuilder builder, string key, bool value, bool comma)
    {
        if (comma)
        {
            builder.Append(",");
        }
        builder.Append("\"");
        builder.Append(Escape(key));
        builder.Append("\":");
        builder.Append(value ? "true" : "false");
    }

    public static void AppendNumber(StringBuilder builder, string key, int value, bool comma)
    {
        if (comma)
        {
            builder.Append(",");
        }
        builder.Append("\"");
        builder.Append(Escape(key));
        builder.Append("\":");
        builder.Append(value);
    }

    public static string Escape(string value)
    {
        if (value == null)
        {
            return String.Empty;
        }
        StringBuilder builder = new StringBuilder();
        foreach (char c in value)
        {
            switch (c)
            {
                case '\\':
                    builder.Append("\\\\");
                    break;
                case '"':
                    builder.Append("\\\"");
                    break;
                case '\r':
                    builder.Append("\\r");
                    break;
                case '\n':
                    builder.Append("\\n");
                    break;
                case '\t':
                    builder.Append("\\t");
                    break;
                default:
                    if (c < 32)
                    {
                        builder.Append("\\u");
                        builder.Append(((int)c).ToString("x4"));
                    }
                    else
                    {
                        builder.Append(c);
                    }
                    break;
            }
        }
        return builder.ToString();
    }
}
