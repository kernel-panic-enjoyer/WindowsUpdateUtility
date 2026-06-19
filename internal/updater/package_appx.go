package updater

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func appxInstalled() ([]Package, CommandResult) {
	script := `$ErrorActionPreference='Stop'
$startNames=@{}
try {
  Get-StartApps | ForEach-Object {
    $appId=[string]$_.AppID
    if ($appId -match '^([^!]+)!' -and -not $startNames.ContainsKey($matches[1])) {
      $startNames[$matches[1]]=[string]$_.Name
    }
  }
} catch {}
$packages=$null
try {
  $packages=Get-AppxPackage -AllUsers -PackageTypeFilter Main,Framework,Bundle,Optional
} catch {
  $packages=Get-AppxPackage -PackageTypeFilter Main,Framework,Bundle,Optional
}
$packages | ForEach-Object {
  $displayName=''
  $publisherDisplayName=''
  $startName=''
  if ($startNames.ContainsKey($_.PackageFamilyName)) { $startName=$startNames[$_.PackageFamilyName] }
  try {
    $manifest=Get-AppxPackageManifest -Package $_.PackageFullName
    $raw=[string]$manifest.Package.Properties.DisplayName
    if ($raw -and -not $raw.StartsWith('ms-resource:')) { $displayName=$raw }
    $publisherDisplayName=[string]$manifest.Package.Properties.PublisherDisplayName
  } catch {}
  [pscustomobject]@{Name=$_.Name;StartName=$startName;DisplayName=$displayName;PublisherDisplayName=$publisherDisplayName;PackageFullName=$_.PackageFullName;PackageFamilyName=$_.PackageFamilyName;Version=$_.Version.ToString();Publisher=$_.Publisher;InstallLocation=$_.InstallLocation}
} | ConvertTo-Json -Compress -Depth 3`
	result := runCommand(90*time.Second, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	return parseAppxPackageJSON(result.Stdout), result
}

func parseAppxPackageJSON(output string) []Package {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var items []struct {
		Name                 string
		StartName            string
		DisplayName          string
		PublisherDisplayName string
		PackageFullName      string
		PackageFamilyName    string
		Version              string
		Publisher            string
		InstallLocation      string
	}
	if strings.HasPrefix(output, "[") {
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			return nil
		}
	} else {
		var item struct {
			Name                 string
			StartName            string
			DisplayName          string
			PublisherDisplayName string
			PackageFullName      string
			PackageFamilyName    string
			Version              string
			Publisher            string
			InstallLocation      string
		}
		if err := json.Unmarshal([]byte(output), &item); err != nil {
			return nil
		}
		items = append(items, item)
	}
	var packages []Package
	for _, item := range items {
		id := strings.TrimSpace(item.PackageFullName)
		rawName := strings.TrimSpace(item.Name)
		if id == "" || rawName == "" {
			continue
		}
		packages = append(packages, Package{
			ID:              id,
			Name:            friendlyAppxName(rawName, item.DisplayName, item.StartName),
			Version:         strings.TrimSpace(item.Version),
			Manager:         managerStore,
			Source:          sourceAppX,
			Match:           strings.TrimSpace(item.PackageFamilyName),
			UpdateSupported: false,
			ActionBackend:   backendAppXInventory,
		})
	}
	sort.Slice(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	return packages
}
