package updater

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type StoreInstalledIdentity struct {
	UserSID           string `json:"user_sid"`
	PackageFamilyName string `json:"package_family_name"`
}

func (identity StoreInstalledIdentity) Resolved() bool {
	return identity.UserSID != "" && identity.PackageFamilyName != ""
}

func (identity StoreInstalledIdentity) Equal(other StoreInstalledIdentity) bool {
	return identity.UserSID == other.UserSID && identity.PackageFamilyName == other.PackageFamilyName
}

type StoreScanCompletionStatus string

const (
	StoreScanPending    StoreScanCompletionStatus = "pending"
	StoreScanRunning    StoreScanCompletionStatus = "running"
	StoreScanCompleted  StoreScanCompletionStatus = "completed"
	StoreScanIncomplete StoreScanCompletionStatus = "incomplete"
	StoreScanFailed     StoreScanCompletionStatus = "failed"
)

type StoreScanGeneration struct {
	ScanID           string                         `json:"scan_id"`
	UserSID          string                         `json:"user_sid"`
	StartedAt        time.Time                      `json:"started_at"`
	CompletedAt      time.Time                      `json:"completed_at,omitempty"`
	Mode             StoreScanMode                  `json:"mode,omitempty"`
	WindowsVersion   string                         `json:"windows_version,omitempty"`
	WindowsBuild     string                         `json:"windows_build,omitempty"`
	Architecture     string                         `json:"architecture,omitempty"`
	ProviderVersions map[string]string              `json:"provider_versions,omitempty"`
	CompletionStatus StoreScanCompletionStatus      `json:"completion_status"`
	ProviderHealth   map[string]StoreProviderHealth `json:"provider_health,omitempty"`
	Metrics          StoreScanMetrics               `json:"metrics,omitempty"`
}

type StoreScanMode string

const (
	StoreScanModeOptimized StoreScanMode = "optimized"
	StoreScanModeDeep      StoreScanMode = "deep"
)

type StoreScanMetrics struct {
	ProductLikeFamilyCount       int            `json:"product_like_family_count,omitempty"`
	AggregateDurationMillis      int64          `json:"aggregate_duration_millis,omitempty"`
	ExactChecksPlanned           int            `json:"exact_checks_planned,omitempty"`
	ExactChecksCompleted         int            `json:"exact_checks_completed,omitempty"`
	MappingsReused               int            `json:"mappings_reused,omitempty"`
	MappingsRefreshed            int            `json:"mappings_refreshed,omitempty"`
	MappingsRejected             int            `json:"mappings_rejected,omitempty"`
	CommandCountByFamily         map[string]int `json:"command_count_by_family,omitempty"`
	TimeoutCount                 int            `json:"timeout_count,omitempty"`
	TotalDurationMillis          int64          `json:"total_duration_millis,omitempty"`
	ResultingStateCount          map[string]int `json:"resulting_state_count,omitempty"`
	AggregateProviderRunCount    int            `json:"aggregate_provider_run_count,omitempty"`
	ExactProviderRunCount        int            `json:"exact_provider_run_count,omitempty"`
	MappingReuseProviderRunCount int            `json:"mapping_reuse_provider_run_count,omitempty"`
}

func (scan StoreScanGeneration) CompleteFor(identity StoreInstalledIdentity) bool {
	return scan.UsableFor(identity) &&
		scan.CompletionStatus == StoreScanCompleted &&
		!scan.CompletedAt.IsZero()
}

func (scan StoreScanGeneration) UsableFor(identity StoreInstalledIdentity) bool {
	return scan.ScanID != "" &&
		scan.UserSID == identity.UserSID &&
		!scan.StartedAt.IsZero()
}

type StoreProviderIdentity struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Backend string `json:"backend,omitempty"`
}

func (provider StoreProviderIdentity) Key() string {
	if provider.ID != "" {
		return provider.ID
	}
	if provider.Backend != "" {
		return provider.Backend
	}
	return provider.Name
}

type StoreProviderHealth string

const (
	StoreProviderHealthy     StoreProviderHealth = "healthy"
	StoreProviderFailed      StoreProviderHealth = "failed"
	StoreProviderIncomplete  StoreProviderHealth = "incomplete"
	StoreProviderUnsupported StoreProviderHealth = "unsupported"
	StoreProviderStale       StoreProviderHealth = "stale"
)

type StoreObservationKind string

const (
	StoreObservationPositiveUpdateOffer               StoreObservationKind = "positive_update_offer"
	StoreObservationAuthoritativeNegative             StoreObservationKind = "authoritative_negative"
	StoreObservationProviderFailure                   StoreObservationKind = "provider_failure"
	StoreObservationIncompleteResult                  StoreObservationKind = "incomplete_result"
	StoreObservationUnsupportedProvider               StoreObservationKind = "unsupported_provider"
	StoreObservationStaleResult                       StoreObservationKind = "stale_result"
	StoreObservationNewerCatalogNoApplicableInstaller StoreObservationKind = "newer_catalog_no_applicable_installer"
	StoreObservationPendingUpdate                     StoreObservationKind = "pending_update"
	StoreObservationEmptyResult                       StoreObservationKind = "empty_result"
)

type VerifiedStoreIdentityMapping struct {
	InstalledIdentity     StoreInstalledIdentity `json:"installed_identity"`
	ProductID             string                 `json:"product_id"`
	Provider              StoreProviderIdentity  `json:"provider"`
	ScanID                string                 `json:"scan_id"`
	VerifiedAt            time.Time              `json:"verified_at"`
	Evidence              string                 `json:"evidence,omitempty"`
	IdentityName          string                 `json:"identity_name,omitempty"`
	PublisherID           string                 `json:"publisher_id,omitempty"`
	ProcessorArchitecture string                 `json:"processor_architecture,omitempty"`
	ProductLike           bool                   `json:"product_like,omitempty"`
	ProviderVersion       string                 `json:"provider_version,omitempty"`
}

func (mapping VerifiedStoreIdentityMapping) VerifiedFor(identity StoreInstalledIdentity, scan StoreScanGeneration) bool {
	return mapping.ProductID != "" &&
		looksLikeStoreProductID(mapping.ProductID) &&
		mapping.ScanID == scan.ScanID &&
		mapping.InstalledIdentity.Equal(identity) &&
		!mapping.VerifiedAt.IsZero()
}

type ExactStoreUpdateTarget struct {
	Identity   StoreInstalledIdentity `json:"identity"`
	Provider   StoreProviderIdentity  `json:"provider"`
	ProductID  string                 `json:"product_id,omitempty"`
	UpdateID   string                 `json:"update_id,omitempty"`
	Verified   bool                   `json:"verified"`
	VerifiedBy string                 `json:"verified_by,omitempty"`
	VerifiedAt time.Time              `json:"verified_at"`
}

func (target ExactStoreUpdateTarget) ExactFor(identity StoreInstalledIdentity) bool {
	productID := strings.TrimSpace(target.ProductID)
	updateID := strings.TrimSpace(target.UpdateID)
	return target.Verified &&
		target.Identity.Equal(identity) &&
		(productID == "" || looksLikeStoreProductID(productID)) &&
		(productID != "" || updateID != "") &&
		!target.VerifiedAt.IsZero()
}

type StoreProviderObservation struct {
	Provider         StoreProviderIdentity         `json:"provider"`
	Health           StoreProviderHealth           `json:"health"`
	Kind             StoreObservationKind          `json:"kind"`
	Identity         StoreInstalledIdentity        `json:"identity"`
	ScanID           string                        `json:"scan_id"`
	ObservedAt       time.Time                     `json:"observed_at"`
	InstalledVersion string                        `json:"installed_version,omitempty"`
	AvailableVersion string                        `json:"available_version,omitempty"`
	CatalogVersion   string                        `json:"catalog_version,omitempty"`
	Target           *ExactStoreUpdateTarget       `json:"target,omitempty"`
	Mapping          *VerifiedStoreIdentityMapping `json:"mapping,omitempty"`
	Diagnostics      string                        `json:"diagnostics,omitempty"`
}

func (observation StoreProviderObservation) Matches(identity StoreInstalledIdentity, scan StoreScanGeneration) bool {
	return observation.ScanID == scan.ScanID && observation.Identity.Equal(identity)
}

type StoreUpdateState string

const (
	StoreUpdateUnknown      StoreUpdateState = "unknown"
	StoreUpdateCurrent      StoreUpdateState = "current"
	StoreUpdateAvailable    StoreUpdateState = "available"
	StoreUpdateConflict     StoreUpdateState = "conflict"
	StoreUpdateInapplicable StoreUpdateState = "inapplicable"
	StoreUpdatePending      StoreUpdateState = "pending"
)

type StoreEvidenceSummary struct {
	Provider string               `json:"provider"`
	Health   StoreProviderHealth  `json:"health"`
	Kind     StoreObservationKind `json:"kind"`
}

type StoreUpdateAssessment struct {
	State                 StoreUpdateState               `json:"state"`
	Identity              StoreInstalledIdentity         `json:"identity"`
	ScanID                string                         `json:"scan_id,omitempty"`
	Reason                string                         `json:"reason,omitempty"`
	InstalledVersion      string                         `json:"installed_version,omitempty"`
	AvailableVersion      string                         `json:"available_version,omitempty"`
	Target                *ExactStoreUpdateTarget        `json:"target,omitempty"`
	Evidence              []StoreEvidenceSummary         `json:"evidence,omitempty"`
	RejectedEvidenceCount int                            `json:"rejected_evidence_count,omitempty"`
	ProviderHealth        map[string]StoreProviderHealth `json:"provider_health,omitempty"`
}

type StoreReconciliationInput struct {
	Identity          StoreInstalledIdentity
	Scan              StoreScanGeneration
	RequiredProviders []StoreProviderIdentity
	Observations      []StoreProviderObservation
}

func ReconcileStoreUpdate(input StoreReconciliationInput) StoreUpdateAssessment {
	assessment := StoreUpdateAssessment{
		State:          StoreUpdateUnknown,
		Identity:       input.Identity,
		ScanID:         input.Scan.ScanID,
		ProviderHealth: map[string]StoreProviderHealth{},
	}
	if !input.Identity.Resolved() {
		assessment.Reason = "store identity is unresolved"
		return assessment
	}
	if input.Scan.UserSID != input.Identity.UserSID {
		assessment.Reason = "scan user does not match installed identity"
		return assessment
	}
	if !input.Scan.UsableFor(input.Identity) {
		assessment.Reason = "scan generation context is incomplete"
		return assessment
	}
	scanIsComplete := input.Scan.CompleteFor(input.Identity)

	requiredProviders := requiredProviderSet(input.RequiredProviders)
	seenRequiredProviders := map[string]bool{}
	blockedRequiredProvider := ""
	firstBlockingProvider := ""
	var firstBlockingObservation StoreProviderObservation

	var exactUpdateOffers []StoreProviderObservation
	var offersWithoutExactTarget []StoreProviderObservation
	var authoritativeNegatives []StoreProviderObservation
	var noApplicableInstaller []StoreProviderObservation
	var pendingUpdates []StoreProviderObservation

	for _, observation := range input.Observations {
		if !observation.Matches(input.Identity, input.Scan) {
			assessment.RejectedEvidenceCount++
			continue
		}
		providerKey := storeProviderKey(observation.Provider)
		assessment.ProviderHealth[providerKey] = observation.Health
		assessment.Evidence = append(assessment.Evidence, StoreEvidenceSummary{
			Provider: providerKey,
			Health:   observation.Health,
			Kind:     observation.Kind,
		})
		blocksAssessment := observationBlocksAssessment(observation)
		if blocksAssessment && firstBlockingProvider == "" {
			firstBlockingProvider = providerKey
			firstBlockingObservation = observation
		}
		if requiredProviders[providerKey] {
			seenRequiredProviders[providerKey] = true
			if blocksAssessment && blockedRequiredProvider == "" {
				blockedRequiredProvider = providerKey
			}
		}

		if observation.Health != StoreProviderHealthy {
			continue
		}
		switch observation.Kind {
		case StoreObservationPositiveUpdateOffer:
			if observation.Target != nil && observation.Target.ExactFor(input.Identity) {
				exactUpdateOffers = append(exactUpdateOffers, observation)
			} else {
				offersWithoutExactTarget = append(offersWithoutExactTarget, observation)
			}
		case StoreObservationAuthoritativeNegative:
			authoritativeNegatives = append(authoritativeNegatives, observation)
		case StoreObservationNewerCatalogNoApplicableInstaller:
			noApplicableInstaller = append(noApplicableInstaller, observation)
		case StoreObservationPendingUpdate:
			pendingUpdates = append(pendingUpdates, observation)
		}
	}

	if len(assessment.Evidence) == 0 {
		assessment.Reason = "no evidence for identity in scan generation"
		return assessment
	}

	if len(exactUpdateOffers) > 0 && (len(noApplicableInstaller) > 0 || !negativeEvidenceCanYieldToExactPositive(exactUpdateOffers, authoritativeNegatives)) {
		return storeAssessmentDecision(assessment, StoreUpdateConflict, exactUpdateOffers[0], "healthy providers disagree")
	}
	if len(exactUpdateOffers) > 0 {
		consensus, err := reconcileExactStoreUpdateOffers(input.Identity, exactUpdateOffers)
		if err != nil {
			return storeExactOfferConflictAssessment(assessment, exactUpdateOffers, err.Error())
		}
		decision := storeAssessmentDecision(assessment, StoreUpdateAvailable, consensus.Observation, "fresh exact positive update evidence")
		decision.Target = consensus.Target
		decision.AvailableVersion = consensus.AvailableVersion
		return decision
	}
	if len(offersWithoutExactTarget) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdateUnknown, offersWithoutExactTarget[0], "positive update evidence has no exact verified target")
	}
	for provider := range requiredProviders {
		if !seenRequiredProviders[provider] {
			assessment.Reason = "required provider did not return evidence: " + provider
			return assessment
		}
	}
	if blockedRequiredProvider != "" {
		assessment.Reason = "required provider incomplete or failed: " + blockedRequiredProvider
		return assessment
	}
	if firstBlockingProvider != "" {
		return storeAssessmentDecision(assessment, StoreUpdateUnknown, firstBlockingObservation, "provider incomplete or failed: "+firstBlockingProvider)
	}
	if len(pendingUpdates) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdatePending, pendingUpdates[0], "update is pending verification")
	}
	if len(noApplicableInstaller) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdateInapplicable, noApplicableInstaller[0], "newer catalog version has no applicable installer")
	}
	if !scanIsComplete {
		assessment.Reason = "scan generation is incomplete"
		return assessment
	}
	if allRequiredProvidersReturnedNegatives(requiredProviders, authoritativeNegatives) {
		return storeAssessmentDecision(assessment, StoreUpdateCurrent, authoritativeNegatives[0], "all required providers returned authoritative negatives")
	}

	assessment.Reason = "evidence is not authoritative"
	return assessment
}

func negativeEvidenceCanYieldToExactPositive(exactUpdateOffers, authoritativeNegatives []StoreProviderObservation) bool {
	if len(authoritativeNegatives) == 0 {
		return true
	}
	if !hasExactCatalogPositiveOffer(exactUpdateOffers) {
		return false
	}
	for _, negative := range authoritativeNegatives {
		if !knownStoreFalseNegativeProvider(negative.Provider.Key()) {
			return false
		}
	}
	return true
}

func knownStoreFalseNegativeProvider(providerKey string) bool {
	switch providerKey {
	case storeCLIUpdatesProviderID, storeCLIExactProviderID, wingetMSStoreExactProviderID:
		return true
	default:
		return false
	}
}

func hasExactCatalogPositiveOffer(exactUpdateOffers []StoreProviderObservation) bool {
	for _, offer := range exactUpdateOffers {
		if offer.Target == nil || !offer.Target.ExactFor(offer.Identity) {
			continue
		}
		switch offer.Provider.Key() {
		case storeCLIExactProviderID, wingetMSStoreExactProviderID, storeWinRTDiscoveryProviderID:
			return true
		}
		switch offer.Target.Provider.Key() {
		case storeCLIExactProviderID, wingetMSStoreExactProviderID, storeWinRTDiscoveryProviderID:
			return true
		}
	}
	return false
}

type exactStoreUpdateOfferConsensus struct {
	Target           *ExactStoreUpdateTarget
	AvailableVersion string
	Observation      StoreProviderObservation
}

type exactStoreUpdateOfferDescriptor struct {
	Observation      StoreProviderObservation
	ProviderKey      string
	ProviderBackend  string
	ProductID        string
	UpdateID         string
	AvailableVersion string
	VerifiedBy       string
	VerifiedAt       time.Time
}

func reconcileExactStoreUpdateOffers(identity StoreInstalledIdentity, exactUpdateOffers []StoreProviderObservation) (exactStoreUpdateOfferConsensus, error) {
	offers := make([]exactStoreUpdateOfferDescriptor, 0, len(exactUpdateOffers))
	for _, offer := range exactUpdateOffers {
		if offer.Target == nil || !offer.Target.ExactFor(identity) {
			continue
		}
		offers = append(offers, exactStoreUpdateOfferDescriptor{
			Observation:      offer,
			ProviderKey:      storeProviderKey(offer.Target.Provider),
			ProviderBackend:  strings.TrimSpace(offer.Target.Provider.Backend),
			ProductID:        strings.TrimSpace(offer.Target.ProductID),
			UpdateID:         strings.TrimSpace(offer.Target.UpdateID),
			AvailableVersion: strings.TrimSpace(offer.AvailableVersion),
			VerifiedBy:       strings.TrimSpace(offer.Target.VerifiedBy),
			VerifiedAt:       offer.Target.VerifiedAt,
		})
	}
	if len(offers) == 0 {
		return exactStoreUpdateOfferConsensus{}, fmt.Errorf("positive update evidence has no exact verified target")
	}
	sort.SliceStable(offers, func(i, j int) bool {
		return exactStoreUpdateOfferSortKey(offers[i]) < exactStoreUpdateOfferSortKey(offers[j])
	})
	productIDs := map[string]string{}
	updateIDs := map[string]string{}
	knownVersions := map[string]string{}
	for _, offer := range offers {
		if offer.ProductID != "" {
			productIDs[strings.ToLower(offer.ProductID)] = offer.ProductID
		}
		if offer.UpdateID != "" {
			updateIDs[strings.ToLower(offer.UpdateID)] = offer.UpdateID
		}
		if offer.AvailableVersion != "" {
			knownVersions[strings.ToLower(offer.AvailableVersion)] = offer.AvailableVersion
		}
	}
	if len(productIDs) > 1 {
		return exactStoreUpdateOfferConsensus{}, storeUpdateOfferConflictError("product_id", offers)
	}
	if hasConflictingNonPFNUpdateIDs(identity.PackageFamilyName, updateIDs) {
		return exactStoreUpdateOfferConsensus{}, storeUpdateOfferConflictError("update_id", offers)
	}
	if len(knownVersions) > 1 {
		return exactStoreUpdateOfferConsensus{}, storeUpdateOfferConflictError("offered_version", offers)
	}
	canonicalOffer := offers[0]
	target := *canonicalOffer.Observation.Target
	target.ProductID = firstValueBySortedKey(productIDs)
	target.UpdateID = preferredUpdateID(identity.PackageFamilyName, updateIDs)
	target.Provider = canonicalOffer.Observation.Target.Provider
	target.VerifiedBy = canonicalOffer.VerifiedBy
	target.VerifiedAt = canonicalOffer.VerifiedAt
	return exactStoreUpdateOfferConsensus{
		Target:           &target,
		AvailableVersion: firstValueBySortedKey(knownVersions),
		Observation:      canonicalOffer.Observation,
	}, nil
}

func exactStoreUpdateOfferSortKey(offer exactStoreUpdateOfferDescriptor) string {
	return strings.ToLower(strings.Join([]string{
		offer.ProviderKey,
		offer.ProviderBackend,
		offer.ProductID,
		offer.UpdateID,
		offer.AvailableVersion,
	}, "|"))
}

func hasConflictingNonPFNUpdateIDs(packageFamilyName string, updateIDs map[string]string) bool {
	if len(updateIDs) <= 1 {
		return false
	}
	packageFamilyKey := strings.ToLower(strings.TrimSpace(packageFamilyName))
	if packageFamilyKey == "" {
		return true
	}
	nonPFNUpdateIDCount := 0
	for key := range updateIDs {
		if key != packageFamilyKey {
			nonPFNUpdateIDCount++
		}
	}
	return nonPFNUpdateIDCount > 1 || nonPFNUpdateIDCount == len(updateIDs)
}

func preferredUpdateID(packageFamilyName string, updateIDs map[string]string) string {
	if len(updateIDs) == 0 {
		return ""
	}
	packageFamilyKey := strings.ToLower(strings.TrimSpace(packageFamilyName))
	if value, ok := updateIDs[packageFamilyKey]; ok {
		return value
	}
	return firstValueBySortedKey(updateIDs)
}

func firstValueBySortedKey(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return values[keys[0]]
}

func storeUpdateOfferConflictError(field string, offers []exactStoreUpdateOfferDescriptor) error {
	parts := make([]string, 0, len(offers))
	for _, offer := range offers {
		value := ""
		switch field {
		case "product_id":
			value = offer.ProductID
		case "update_id":
			value = offer.UpdateID
		case "offered_version":
			value = offer.AvailableVersion
		}
		if value == "" {
			continue
		}
		parts = append(parts, storeProviderKey(offer.Observation.Provider)+"="+sanitizeProviderDiagnostic(value))
	}
	sort.Strings(parts)
	return fmt.Errorf("healthy providers returned conflicting %s values: %s", field, strings.Join(parts, ", "))
}

func storeExactOfferConflictAssessment(assessment StoreUpdateAssessment, exactUpdateOffers []StoreProviderObservation, reason string) StoreUpdateAssessment {
	observation := exactUpdateOffers[0]
	assessment.State = StoreUpdateConflict
	assessment.Reason = reason
	assessment.InstalledVersion = observation.InstalledVersion
	assessment.AvailableVersion = observation.AvailableVersion
	assessment.Target = nil
	return assessment
}

func requiredProviderSet(providers []StoreProviderIdentity) map[string]bool {
	required := map[string]bool{}
	for _, provider := range providers {
		if key := provider.Key(); key != "" {
			required[key] = true
		}
	}
	return required
}

func storeProviderKey(provider StoreProviderIdentity) string {
	if key := provider.Key(); key != "" {
		return key
	}
	return "unknown"
}

func observationBlocksAssessment(observation StoreProviderObservation) bool {
	if observation.Health != StoreProviderHealthy {
		return true
	}
	switch observation.Kind {
	case StoreObservationProviderFailure,
		StoreObservationIncompleteResult,
		StoreObservationUnsupportedProvider,
		StoreObservationStaleResult,
		StoreObservationEmptyResult:
		return true
	default:
		return false
	}
}

func allRequiredProvidersReturnedNegatives(required map[string]bool, negatives []StoreProviderObservation) bool {
	if len(required) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, negative := range negatives {
		seen[storeProviderKey(negative.Provider)] = true
	}
	for provider := range required {
		if !seen[provider] {
			return false
		}
	}
	return true
}

func storeAssessmentDecision(assessment StoreUpdateAssessment, state StoreUpdateState, observation StoreProviderObservation, reason string) StoreUpdateAssessment {
	assessment.State = state
	assessment.Reason = reason
	assessment.InstalledVersion = observation.InstalledVersion
	assessment.AvailableVersion = observation.AvailableVersion
	assessment.Target = observation.Target
	return assessment
}
