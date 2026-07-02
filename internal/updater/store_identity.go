package updater

import "strings"

// stableStoreActionID strips a package full name down to a stable package-name
// key only for scan/app preference bookkeeping. It must not be used as Store
// update identity; update evidence is bound by StoreInstalledIdentity instead.
func stableStoreActionID(actionID string) string {
	actionID = strings.TrimSpace(actionID)
	if packageName, _, ok := strings.Cut(actionID, "_"); ok && strings.Contains(packageName, ".") {
		return packageName
	}
	return actionID
}

func stableScannedStoreAppID(key string, app ScannedApp) string {
	scanIdentityCandidates := []string{
		app.PackageID,
		strings.TrimPrefix(key, "store:"),
		app.InstallLocation,
	}
	for _, candidateIdentity := range scanIdentityCandidates {
		candidateIdentity = strings.TrimSpace(candidateIdentity)
		if candidateIdentity == "" {
			continue
		}
		return stableStoreScanIdentity(candidateIdentity)
	}
	return ""
}

func stableStoreScanIdentity(value string) string {
	familyCandidate := stableAppxFamilyCandidate(value)
	if familyCandidate == "" {
		return ""
	}
	return stableStoreActionID(familyCandidate)
}

func stableAppxFamilyCandidate(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	identityParts := strings.Split(identity, "_")
	if len(identityParts) >= 3 && looksLikeVersion(identityParts[1]) {
		packageName := strings.TrimSpace(identityParts[0])
		publisherID := strings.TrimSpace(identityParts[len(identityParts)-1])
		if packageName != "" && publisherID != "" {
			return packageName + "_" + publisherID
		}
	}
	return identity
}
