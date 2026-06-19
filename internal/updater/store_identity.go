package updater

import "strings"

func stableStoreActionID(id string) string {
	id = strings.TrimSpace(id)
	if before, _, ok := strings.Cut(id, "_"); ok && strings.Contains(before, ".") {
		return before
	}
	return id
}

func stableScannedStoreAppID(key string, app ScannedApp) string {
	for _, value := range []string{app.PackageID, strings.TrimPrefix(key, "store:"), app.InstallLocation} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		return stableStoreScanIdentity(value)
	}
	return ""
}

func stableStoreScanIdentity(value string) string {
	stableID := stableAppxIdentity(value)
	if stableID == "" {
		return ""
	}
	return stableStoreActionID(stableID)
}

func stableAppxIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "_")
	if len(parts) >= 3 && looksLikeVersion(parts[1]) {
		name := strings.TrimSpace(parts[0])
		publisherID := strings.TrimSpace(parts[len(parts)-1])
		if name != "" && publisherID != "" {
			return name + "_" + publisherID
		}
	}
	return value
}
