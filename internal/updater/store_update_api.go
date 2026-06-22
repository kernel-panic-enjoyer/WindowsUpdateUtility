package updater

import (
	"fmt"
	"strings"
	"time"
)

const storeUpdateAssessmentFeatureFlag = "UPDATER_STORE_UPDATE_ASSESSMENT"

var (
	storeAssessmentNow            = func() time.Time { return time.Now().UTC() }
	storeAssessmentCurrentUserSID = currentUserSID
)

func storeUpdateAssessmentEnabled() bool {
	return storeUpdateAssessmentModelEnabled || featureFlagEnabled(storeUpdateAssessmentFeatureFlag)
}

func applyStoreUpdateAssessmentProjection(state *State, inventory Inventory) (Inventory, bool) {
	if !storeUpdateAssessmentEnabled() {
		return inventory, false
	}
	now := storeAssessmentNow().UTC().Truncate(time.Second)
	userSID, sidErr := storeAssessmentCurrentUserSID()
	scanID := fmt.Sprintf("store-api-%d", now.UnixNano())
	changedState := false
	for index := range inventory.Packages {
		pkg := inventory.Packages[index]
		if pkg.Manager != managerStore {
			continue
		}
		projected, changed := projectStorePackageAssessment(state, pkg, inventory.CommandResults, userSID, sidErr, scanID, now)
		inventory.Packages[index] = projected
		changedState = changedState || changed
	}
	inventory.StoreScanHealth = buildStoreScanHealthSummary(inventory.Packages, nil)
	return inventory, changedState
}

func projectStorePackageAssessment(
	state *State,
	pkg Package,
	results map[string]CommandResult,
	userSID string,
	sidErr error,
	scanID string,
	now time.Time,
) (Package, bool) {
	if pkg.InstalledVersion == "" {
		pkg.InstalledVersion = pkg.Version
	}
	if pkg.OfferedVersion == "" {
		pkg.OfferedVersion = pkg.AvailableVersion
	}
	if pkg.ObservedAt == "" {
		pkg.ObservedAt = formatAssessmentTime(now)
	}
	if pkg.ScanID == "" {
		pkg.ScanID = scanID
	}
	if pkg.UpdateState != "" {
		return applyExplicitStoreAssessment(state, pkg)
	}

	pfn := storeInstalledPackageFamilyName(pkg)
	pkg.InstalledPackageFamilyName = pfn
	pkg.ExactIdentityAvailable = userSID != "" && pfn != ""
	pkg.ExactActionTargetAvailable = pkg.ExactActionTargetAvailable && (pkg.StoreProductID != "" || pkg.StoreUpdateID != "")
	pkg.ProviderSummaries = providerSummariesForStorePackage(pkg, results, now, sidErr)
	pkg.Applicability = "unknown"

	if !pkg.ExactIdentityAvailable {
		pkg.UpdateState = string(StoreUpdateUnknown)
		if sidErr != nil {
			pkg.UpdateReason = "Store scan user could not be identified: " + sanitizeProviderDiagnostic(sidErr.Error())
		} else {
			pkg.UpdateReason = "installed Store identity is unresolved; package family name is required"
		}
	} else if pkg.UpdateAvailable {
		pkg.UpdateState = string(StoreUpdateUnknown)
		pkg.UpdateReason = "legacy update evidence exists, but no exact verified Store action target is available"
	} else {
		pkg.UpdateState = string(StoreUpdateUnknown)
		pkg.UpdateReason = "Store update coverage is not authoritative for this package"
	}

	if cached, ok := retainedStorePositiveAssessment(state, userSID, pfn); ok && storeAssessmentCanRetainPositive(pkg.UpdateState) {
		pkg.UpdateState = cached.State
		pkg.UpdateReason = "retained last known positive update because the latest Store scan is incomplete"
		pkg.ObservedAt = cached.ObservedAt
		pkg.Stale = true
		pkg.ScanID = cached.ScanID
		pkg.AvailableVersion = cached.OfferedVersion
		pkg.OfferedVersion = cached.OfferedVersion
		pkg.StoreProductID = cached.StoreProductID
		pkg.StoreUpdateID = cached.StoreUpdateID
		pkg.Applicability = firstNonEmpty(cached.Applicability, "unknown")
		pkg.ExactActionTargetAvailable = cached.ExactActionTargetAvailable
	}

	pkg = applyStoreAssessmentCompatibility(pkg)
	return pkg, cacheStorePositiveAssessment(state, pkg, userSID)
}

func applyExplicitStoreAssessment(state *State, pkg Package) (Package, bool) {
	pkg.UpdateState = strings.ToLower(strings.TrimSpace(pkg.UpdateState))
	pkg = applyStoreAssessmentCompatibility(pkg)
	userSID := ""
	if pkg.ExactIdentityAvailable {
		if sid, err := storeAssessmentCurrentUserSID(); err == nil {
			userSID = sid
		}
	}
	return pkg, cacheStorePositiveAssessment(state, pkg, userSID)
}

func applyStoreAssessmentCompatibility(pkg Package) Package {
	if pkg.UpdateState == "" {
		return pkg
	}
	pkg.UpdateAvailable = pkg.UpdateState == string(StoreUpdateAvailable)
	if pkg.InstalledVersion == "" {
		pkg.InstalledVersion = pkg.Version
	}
	if pkg.OfferedVersion == "" {
		pkg.OfferedVersion = pkg.AvailableVersion
	}
	if pkg.UpdateAvailable && pkg.AvailableVersion == "" {
		pkg.AvailableVersion = pkg.OfferedVersion
	}
	if !pkg.UpdateAvailable && pkg.UpdateState != string(StoreUpdatePending) {
		pkg.AvailableVersion = ""
	}
	if pkg.Manager == managerStore && pkg.UpdateState == string(StoreUpdateAvailable) && !pkg.ExactActionTargetAvailable {
		pkg.UpdateSupported = false
	}
	return pkg
}

func packageHasExactStoreUpdateTarget(pkg Package) bool {
	if pkg.Manager != managerStore {
		return true
	}
	if pkg.UpdateState == "" {
		return !storeNewDetectorActive()
	}
	return pkg.ExactActionTargetAvailable &&
		storeInstalledPackageFamilyName(pkg) != "" &&
		(strings.TrimSpace(pkg.StoreProductID) != "" || strings.TrimSpace(pkg.StoreUpdateID) != "")
}

func storeInstalledPackageFamilyName(pkg Package) string {
	if strings.TrimSpace(pkg.InstalledPackageFamilyName) != "" {
		return strings.TrimSpace(pkg.InstalledPackageFamilyName)
	}
	switch {
	case pkg.Source == sourceNativeAppX:
		return strings.TrimSpace(pkg.ID)
	case pkg.Source == sourceAppX && strings.TrimSpace(pkg.Match) != "":
		return strings.TrimSpace(pkg.Match)
	case pkg.ActionBackend == backendAppXInventory && strings.TrimSpace(pkg.Match) != "":
		return strings.TrimSpace(pkg.Match)
	}
	if looksLikePackageFamilyName(pkg.ID) {
		return strings.TrimSpace(pkg.ID)
	}
	if looksLikePackageFamilyName(pkg.Match) {
		return strings.TrimSpace(pkg.Match)
	}
	return ""
}

func looksLikePackageFamilyName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, " ") || strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	if strings.Count(value, "_") > 1 {
		return false
	}
	if strings.Contains(value, "_") {
		parts := strings.Split(value, "_")
		return len(parts) == 2 && parts[0] != "" && parts[1] != ""
	}
	return strings.Contains(value, ".")
}

func providerSummariesForStorePackage(pkg Package, results map[string]CommandResult, now time.Time, sidErr error) []StorePackageProviderSummary {
	summaries := []StorePackageProviderSummary{}
	observedAt := formatAssessmentTime(now)
	if sidErr != nil {
		summaries = append(summaries, StorePackageProviderSummary{
			Name:       "current-user-context",
			Health:     string(StoreProviderFailed),
			Kind:       string(StoreObservationProviderFailure),
			ObservedAt: observedAt,
			Error:      sanitizeProviderDiagnostic(sidErr.Error()),
		})
	}
	addResult := func(name string, result CommandResult, updateProvider bool) {
		if result.Command == "" {
			return
		}
		summary := StorePackageProviderSummary{Name: name, ObservedAt: observedAt}
		if result.OK {
			summary.Health = string(StoreProviderHealthy)
			if updateProvider && pkg.UpdateAvailable {
				summary.Kind = string(StoreObservationPositiveUpdateOffer)
			} else if updateProvider {
				summary.Kind = string(StoreObservationIncompleteResult)
				summary.Error = "provider output is not an authoritative negative"
			} else {
				summary.Kind = string(StoreObservationIncompleteResult)
			}
		} else {
			summary.Health = string(StoreProviderFailed)
			summary.Kind = string(StoreObservationProviderFailure)
			summary.Error = sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout))
		}
		summaries = append(summaries, summary)
	}
	addResult("Store CLI updates", results["store_updates"], true)
	addResult("WinGet msstore", results["winget_upgrade"], true)
	addResult("Store inventory", results["appx_inventory"], false)
	addResult("Native Store inventory", results["native_store_inventory"], false)
	if len(summaries) == 0 {
		summaries = append(summaries, StorePackageProviderSummary{
			Name:       "Store assessment",
			Health:     string(StoreProviderIncomplete),
			Kind:       string(StoreObservationIncompleteResult),
			ObservedAt: observedAt,
			Error:      "no authoritative Store update provider evidence is attached to this scan",
		})
	}
	return summaries
}

func sanitizeProviderDiagnostic(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 220 {
		value = strings.TrimSpace(value[:217]) + "..."
	}
	return value
}

func retainedStorePositiveAssessment(state *State, userSID, pfn string) (StoreUpdateAssessmentCacheEntry, bool) {
	if state == nil || state.StoreUpdateAssessmentCache == nil || userSID == "" || pfn == "" {
		return StoreUpdateAssessmentCacheEntry{}, false
	}
	entry, ok := state.StoreUpdateAssessmentCache[storeAssessmentCacheKey(userSID, pfn)]
	if !ok || entry.State != string(StoreUpdateAvailable) {
		return StoreUpdateAssessmentCacheEntry{}, false
	}
	return entry, true
}

func storeAssessmentCanRetainPositive(state string) bool {
	switch state {
	case string(StoreUpdateUnknown), string(StoreUpdateConflict):
		return true
	default:
		return false
	}
}

func cacheStorePositiveAssessment(state *State, pkg Package, userSID string) bool {
	if state == nil || pkg.Manager != managerStore || pkg.UpdateState != string(StoreUpdateAvailable) || pkg.Stale || !pkg.ExactActionTargetAvailable {
		return false
	}
	pfn := strings.TrimSpace(pkg.InstalledPackageFamilyName)
	if pfn == "" || userSID == "" {
		return false
	}
	if state.StoreUpdateAssessmentCache == nil {
		state.StoreUpdateAssessmentCache = map[string]StoreUpdateAssessmentCacheEntry{}
	}
	key := storeAssessmentCacheKey(userSID, pfn)
	entry := StoreUpdateAssessmentCacheEntry{
		UserSID:                    userSID,
		PackageFamilyName:          pfn,
		ScanID:                     pkg.ScanID,
		State:                      pkg.UpdateState,
		Reason:                     pkg.UpdateReason,
		ObservedAt:                 pkg.ObservedAt,
		InstalledVersion:           pkg.InstalledVersion,
		OfferedVersion:             firstNonEmpty(pkg.OfferedVersion, pkg.AvailableVersion),
		StoreProductID:             pkg.StoreProductID,
		StoreUpdateID:              pkg.StoreUpdateID,
		Applicability:              pkg.Applicability,
		ExactActionTargetAvailable: pkg.ExactActionTargetAvailable,
	}
	if state.StoreUpdateAssessmentCache[key] == entry {
		return false
	}
	state.StoreUpdateAssessmentCache[key] = entry
	return true
}

func storeAssessmentCacheKey(userSID, pfn string) string {
	return strings.ToLower(strings.TrimSpace(userSID)) + "|" + strings.ToLower(strings.TrimSpace(pfn))
}

func formatAssessmentTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func buildStoreScanHealthSummary(packages []Package, scanProviders []StorePackageProviderSummary) StoreScanHealthSummary {
	counts := map[string]int{
		string(StoreUpdateAvailable):    0,
		string(StoreUpdateCurrent):      0,
		string(StoreUpdateUnknown):      0,
		string(StoreUpdateConflict):     0,
		string(StoreUpdateInapplicable): 0,
		string(StoreUpdatePending):      0,
		"stale":                         0,
	}
	providers := append([]StorePackageProviderSummary(nil), scanProviders...)
	active := false
	scanID := ""
	observedAt := ""
	var reasons []string
	for _, pkg := range packages {
		if pkg.Manager != managerStore || strings.TrimSpace(pkg.UpdateState) == "" {
			continue
		}
		active = true
		state := strings.ToLower(strings.TrimSpace(pkg.UpdateState))
		if _, ok := counts[state]; !ok {
			state = string(StoreUpdateUnknown)
		}
		counts[state]++
		if pkg.Stale {
			counts["stale"]++
		}
		if scanID == "" && pkg.ScanID != "" {
			scanID = pkg.ScanID
		}
		if pkg.ObservedAt > observedAt {
			observedAt = pkg.ObservedAt
		}
		if shouldSurfaceStoreHealthReason(pkg) {
			reasons = append(reasons, firstNonEmpty(pkg.UpdateReason, pkg.Name+": "+state))
		}
		providers = append(providers, pkg.ProviderSummaries...)
	}
	providers = uniqueStoreProviderSummaries(providers)
	if !active {
		return StoreScanHealthSummary{
			Active:    false,
			Reason:    "New Store assessment fields are disabled.",
			Counts:    counts,
			Providers: providers,
		}
	}
	providerIssue := false
	for _, provider := range providers {
		if strings.TrimSpace(provider.Health) != "" && !strings.EqualFold(provider.Health, string(StoreProviderHealthy)) {
			providerIssue = true
			if provider.Error != "" {
				reasons = append(reasons, provider.Name+": "+provider.Error)
			}
		}
	}
	authoritative := counts[string(StoreUpdateUnknown)] == 0 &&
		counts[string(StoreUpdateConflict)] == 0 &&
		counts[string(StoreUpdateInapplicable)] == 0 &&
		counts[string(StoreUpdatePending)] == 0 &&
		counts["stale"] == 0 &&
		!providerIssue
	status := string(StoreScanCompleted)
	if !authoritative {
		status = string(StoreScanIncomplete)
	}
	return StoreScanHealthSummary{
		Active:        true,
		Healthy:       authoritative,
		Authoritative: authoritative,
		ScanID:        scanID,
		Status:        status,
		ObservedAt:    observedAt,
		Reason:        conciseStoreHealthReason(reasons),
		Counts:        counts,
		Providers:     providers,
	}
}

func shouldSurfaceStoreHealthReason(pkg Package) bool {
	if pkg.Stale {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(pkg.UpdateState)) {
	case string(StoreUpdateUnknown), string(StoreUpdateConflict), string(StoreUpdateInapplicable), string(StoreUpdatePending):
		return true
	default:
		return false
	}
}

func uniqueStoreProviderSummaries(providers []StorePackageProviderSummary) []StorePackageProviderSummary {
	seen := map[string]bool{}
	unique := make([]StorePackageProviderSummary, 0, len(providers))
	for _, provider := range providers {
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Health = strings.TrimSpace(provider.Health)
		provider.Kind = strings.TrimSpace(provider.Kind)
		provider.ObservedAt = strings.TrimSpace(provider.ObservedAt)
		provider.Error = sanitizeProviderDiagnostic(provider.Error)
		if provider.Name == "" && provider.Health == "" && provider.Kind == "" && provider.Error == "" {
			continue
		}
		key := strings.ToLower(provider.Name + "|" + provider.Health + "|" + provider.Kind + "|" + provider.ObservedAt + "|" + provider.Error)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, provider)
	}
	return unique
}

func conciseStoreHealthReason(reasons []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, reason := range reasons {
		reason = sanitizeProviderDiagnostic(reason)
		if reason == "" {
			continue
		}
		key := strings.ToLower(reason)
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, reason)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " | ")
}

func newStoreAPIScanGeneration(userSID string, now time.Time) StoreScanGeneration {
	systemContext := currentStoreScanSystemContext()
	return StoreScanGeneration{
		ScanID:           fmt.Sprintf("store-api-%d", now.UnixNano()),
		UserSID:          userSID,
		StartedAt:        now,
		CompletedAt:      now,
		WindowsVersion:   systemContext.WindowsVersion,
		WindowsBuild:     systemContext.WindowsBuild,
		Architecture:     systemContext.Architecture,
		ProviderVersions: map[string]string{"store-api-assessment": "1"},
		ProviderHealth:   map[string]StoreProviderHealth{"store-api-assessment": StoreProviderIncomplete},
		CompletionStatus: StoreScanIncomplete,
	}
}
