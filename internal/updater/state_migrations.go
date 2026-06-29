package updater

import (
	"encoding/json"
	"strings"
)

type legacyStateFields struct {
	AssessmentCache map[string]legacyAssessmentCacheEntry `json:"store_update_assessment_cache"`
}

type legacyAssessmentCacheEntry struct {
	UserSID                    string `json:"user_sid"`
	PackageFamilyName          string `json:"package_family_name"`
	StoreProductID             string `json:"store_product_id,omitempty"`
	ExactActionTargetAvailable bool   `json:"exact_action_target_available"`
}

func readLegacyStateFields(data []byte) legacyStateFields {
	var legacy legacyStateFields
	if len(data) == 0 {
		return legacy
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return legacyStateFields{}
	}
	return legacy
}

func migrateStoreScanApps(state *State) {
	for key, app := range state.WingetApps {
		if !isStoreScannedApp(app) {
			continue
		}
		if app.Source == "" || app.Source == "msstore" || app.Source == "appx" {
			app.Source = "store"
		}
		if app.Manager == "" {
			app.Manager = "store"
		}
		state.StoreApps[key] = app
		delete(state.WingetApps, key)
	}
	normalizeStoreScanAppKeys(state)
}

func normalizeStoreScanAppKeys(state *State) {
	normalized := map[string]ScannedApp{}
	for key, app := range state.StoreApps {
		app.Source = "store"
		if app.Manager == "" {
			app.Manager = "store"
		}
		stableID := stableScannedStoreAppID(key, app)
		if stableID != "" {
			app.Key = "store:" + strings.ToLower(stableID)
			app.PackageID = stableID
		} else if app.Key == "" {
			app.Key = key
		}
		if existing, ok := normalized[app.Key]; ok && existing.FirstSeen != "" && (app.FirstSeen == "" || existing.FirstSeen < app.FirstSeen) {
			app.FirstSeen = existing.FirstSeen
		}
		normalized[app.Key] = app
	}
	state.StoreApps = normalized
}

func migrateAndNormalizeAutoUpdatePackageKeys(state *State, legacyAssessments map[string]legacyAssessmentCacheEntry) {
	normalized := map[string]bool{}
	report := StoreAutoUpdateMigrationReport{LastRun: utcNow()}
	for key, enabled := range state.AutoUpdatePackages {
		normalizedKey, entry, disabled := migrateAutoUpdatePackageKey(state, key, legacyAssessments)
		if normalizedKey == "" {
			if disabled {
				report.Disabled = append(report.Disabled, entry)
			}
			continue
		}
		normalized[normalizedKey] = normalized[normalizedKey] || enabled
		if enabled && normalizedKey != key {
			report.Migrated = append(report.Migrated, entry)
		}
	}
	state.AutoUpdatePackages = normalized
	if len(report.Migrated) > 0 || len(report.Disabled) > 0 {
		state.StoreAutoUpdateMigration = report
	}
}

func migrateAutoUpdatePackageKey(state *State, key string, legacyAssessments map[string]legacyAssessmentCacheEntry) (string, StoreAutoUpdateMigrationEntry, bool) {
	now := utcNow()
	entry := StoreAutoUpdateMigrationEntry{LegacyKey: key, MigratedAt: now}
	manager, id, err := splitPackageKey(key)
	if err != nil {
		entry.Reason = "invalid package key"
		return "", entry, true
	}
	if manager != managerStore {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized == "" {
			normalized = key
		}
		entry.CanonicalKey = normalized
		entry.Reason = "non-Store package key retained"
		return normalized, entry, false
	}
	if _, _, ok := splitCanonicalStoreAutoUpdateKey(key); ok {
		entry.CanonicalKey = strings.ToLower(key)
		entry.Reason = "already canonical Store auto-update key"
		return entry.CanonicalKey, entry, false
	}
	id = strings.TrimSpace(id)
	if looksLikePackageFamilyName(id) {
		userSID, err := currentUserSID()
		if err != nil {
			entry.Reason = "current user SID unavailable for Store PFN migration"
			return "", entry, true
		}
		entry.PackageFamilyName = id
		entry.CanonicalKey = canonicalStoreAutoUpdateKey(userSID, id)
		entry.Reason = "migrated exact package family name for current user"
		return entry.CanonicalKey, entry, false
	}
	if match, ok := exactAssessmentForStoreProductID(legacyAssessments, id); ok {
		entry.PackageFamilyName = match.PackageFamilyName
		entry.CanonicalKey = canonicalStoreAutoUpdateKey(match.UserSID, match.PackageFamilyName)
		entry.Reason = "migrated exact Store Product ID from verified assessment cache"
		return entry.CanonicalKey, entry, false
	}
	entry.Reason = "legacy Store key is not an exact package family name or verified Product ID"
	return "", entry, true
}

func exactAssessmentForStoreProductID(legacyAssessments map[string]legacyAssessmentCacheEntry, productID string) (legacyAssessmentCacheEntry, bool) {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return legacyAssessmentCacheEntry{}, false
	}
	var match legacyAssessmentCacheEntry
	count := 0
	for _, entry := range legacyAssessments {
		if !entry.ExactActionTargetAvailable || !strings.EqualFold(strings.TrimSpace(entry.StoreProductID), productID) {
			continue
		}
		if entry.UserSID == "" || entry.PackageFamilyName == "" {
			continue
		}
		match = entry
		count++
	}
	return match, count == 1
}

func normalizeAutoUpdatePackageKey(key string) string {
	manager, id, err := splitPackageKey(key)
	if err != nil {
		return key
	}
	if manager == managerStore {
		if _, _, ok := splitCanonicalStoreAutoUpdateKey(key); ok {
			return strings.ToLower(key)
		}
		if !looksLikePackageFamilyName(id) {
			return ""
		}
		userSID, err := currentUserSID()
		if err != nil {
			return ""
		}
		return canonicalStoreAutoUpdateKey(userSID, id)
	}
	return packageKey(manager, id)
}
