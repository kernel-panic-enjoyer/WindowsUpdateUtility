package updater

import (
	"strings"
	"time"
)

const (
	exactTargetKindNone             = "none"
	exactTargetKindProductID        = "product_id"
	exactTargetKindUpdateID         = "update_id"
	exactTargetKindProductAndUpdate = "product_and_update_id"
)

type PackageUpdatePolicy struct {
	PreferenceEligible bool
	CanUpdateNow       bool
	CannotUpdateReason string
	ExactTargetKind    string
}

func packageHasExactStoreUpdateTarget(pkg Package) bool {
	if pkg.Manager != managerStore {
		return true
	}
	if pkg.UpdateState == "" {
		return false
	}
	return pkg.ExactActionTargetAvailable &&
		storeInstalledPackageFamilyName(pkg) != "" &&
		(strings.TrimSpace(pkg.StoreProductID) != "" || strings.TrimSpace(pkg.StoreUpdateID) != "")
}

func applyPackageCapabilities(pkg Package) Package {
	policy := packageUpdatePolicy(pkg, UpdateOptions{})
	pkg.PreferenceEligible = policy.PreferenceEligible
	pkg.CanUpdateNow = policy.CanUpdateNow
	pkg.CannotUpdateReason = policy.CannotUpdateReason
	pkg.ExactTargetKind = policy.ExactTargetKind
	return pkg
}

func packageUpdatePolicy(pkg Package, options UpdateOptions) PackageUpdatePolicy {
	policy := PackageUpdatePolicy{
		PreferenceEligible: packagePreferenceEligible(pkg),
		ExactTargetKind:    exactTargetKind(pkg.StoreProductID, pkg.StoreUpdateID),
	}
	if pkg.Manager != managerStore {
		if !pkg.UpdateAvailable {
			policy.CannotUpdateReason = "No update available."
			return policy
		}
		if pkg.UpdateSupported == false {
			policy.CannotUpdateReason = "Updates are not supported for this package."
			return policy
		}
		if pkg.UnknownVersion && !options.AllowUnknownVersion {
			policy.CannotUpdateReason = "Unknown installed version requires the global unknown-version override."
			return policy
		}
		if pkg.Pinned && !options.AllowPinned {
			policy.CannotUpdateReason = "Pinned package requires the global pinned update override."
			return policy
		}
		policy.CanUpdateNow = true
		return policy
	}
	if !policy.PreferenceEligible {
		policy.CannotUpdateReason = "Store package identity is not exact."
		return policy
	}
	if !strings.EqualFold(strings.TrimSpace(pkg.UpdateState), string(StoreUpdateAvailable)) {
		state := strings.TrimSpace(pkg.UpdateState)
		if state == "" {
			state = string(StoreUpdateUnknown)
		}
		policy.CannotUpdateReason = "Store state is " + stateLabelForReason(state) + "."
		return policy
	}
	if pkg.Stale {
		policy.CannotUpdateReason = "Store update requires a fresh assessment; rescan required."
		return policy
	}
	if !pkg.ExactActionTargetAvailable || policy.ExactTargetKind == exactTargetKindNone {
		policy.CannotUpdateReason = "Store update requires an exact verified action target."
		return policy
	}
	if pkg.UpdateSupported == false {
		policy.CannotUpdateReason = "No supported executor is available for this Store target."
		return policy
	}
	if pkg.UnknownVersion && !options.AllowUnknownVersion {
		policy.CannotUpdateReason = "Unknown installed version requires the global unknown-version override."
		return policy
	}
	if pkg.Pinned && !options.AllowPinned {
		policy.CannotUpdateReason = "Pinned package requires the global pinned update override."
		return policy
	}
	policy.CanUpdateNow = true
	return policy
}

func packagePreferenceEligible(pkg Package) bool {
	if pkg.Manager != managerStore {
		return pkg.UpdateSupported != false
	}
	return storeInstalledPackageFamilyName(pkg) != "" && normalizedJobPackageKey(pkg) != ""
}

func exactTargetKind(productID, updateID string) string {
	productID = strings.TrimSpace(productID)
	updateID = strings.TrimSpace(updateID)
	switch {
	case productID != "" && updateID != "":
		return exactTargetKindProductAndUpdate
	case productID != "":
		return exactTargetKindProductID
	case updateID != "":
		return exactTargetKindUpdateID
	default:
		return exactTargetKindNone
	}
}

func stateLabelForReason(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return "unknown"
	}
	return state
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

func sanitizeProviderDiagnostic(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 220 {
		value = strings.TrimSpace(value[:217]) + "..."
	}
	return value
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
			Reason:    "No Store scan assessment has been published yet.",
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
