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
	return target.Verified &&
		target.Identity.Equal(identity) &&
		(target.ProductID != "" || target.UpdateID != "") &&
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
	scanComplete := input.Scan.CompleteFor(input.Identity)

	required := requiredProviderSet(input.RequiredProviders)
	requiredSeen := map[string]bool{}
	requiredBlocked := ""
	blockedProvider := ""
	var blockedObservation StoreProviderObservation

	var positives []StoreProviderObservation
	var positivesWithoutTarget []StoreProviderObservation
	var negatives []StoreProviderObservation
	var inapplicable []StoreProviderObservation
	var pending []StoreProviderObservation

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
		if observationBlocksAssessment(observation) && blockedProvider == "" {
			blockedProvider = providerKey
			blockedObservation = observation
		}
		if required[providerKey] {
			requiredSeen[providerKey] = true
			if observationBlocksAssessment(observation) && requiredBlocked == "" {
				requiredBlocked = providerKey
			}
		}

		if observation.Health != StoreProviderHealthy {
			continue
		}
		switch observation.Kind {
		case StoreObservationPositiveUpdateOffer:
			if observation.Target != nil && observation.Target.ExactFor(input.Identity) {
				positives = append(positives, observation)
			} else {
				positivesWithoutTarget = append(positivesWithoutTarget, observation)
			}
		case StoreObservationAuthoritativeNegative:
			negatives = append(negatives, observation)
		case StoreObservationNewerCatalogNoApplicableInstaller:
			inapplicable = append(inapplicable, observation)
		case StoreObservationPendingUpdate:
			pending = append(pending, observation)
		}
	}

	if len(assessment.Evidence) == 0 {
		assessment.Reason = "no evidence for identity in scan generation"
		return assessment
	}

	if len(positives) > 0 && (len(negatives) > 0 || len(inapplicable) > 0) {
		return storeAssessmentDecision(assessment, StoreUpdateConflict, positives[0], "healthy providers disagree")
	}
	if len(positives) > 0 {
		consensus, err := reconcilePositiveStoreTargets(input.Identity, positives)
		if err != nil {
			return storeConflictAssessmentDecision(assessment, positives, err.Error())
		}
		decision := storeAssessmentDecision(assessment, StoreUpdateAvailable, consensus.Observation, "fresh exact positive update evidence")
		decision.Target = consensus.Target
		decision.AvailableVersion = consensus.AvailableVersion
		return decision
	}
	if len(positivesWithoutTarget) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdateUnknown, positivesWithoutTarget[0], "positive update evidence has no exact verified target")
	}
	for provider := range required {
		if !requiredSeen[provider] {
			assessment.Reason = "required provider did not return evidence: " + provider
			return assessment
		}
	}
	if requiredBlocked != "" {
		assessment.Reason = "required provider incomplete or failed: " + requiredBlocked
		return assessment
	}
	if blockedProvider != "" {
		return storeAssessmentDecision(assessment, StoreUpdateUnknown, blockedObservation, "provider incomplete or failed: "+blockedProvider)
	}
	if len(pending) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdatePending, pending[0], "update is pending verification")
	}
	if len(inapplicable) > 0 {
		return storeAssessmentDecision(assessment, StoreUpdateInapplicable, inapplicable[0], "newer catalog version has no applicable installer")
	}
	if !scanComplete {
		assessment.Reason = "scan generation is incomplete"
		return assessment
	}
	if allRequiredProvidersNegative(required, negatives) {
		return storeAssessmentDecision(assessment, StoreUpdateCurrent, negatives[0], "all required providers returned authoritative negatives")
	}

	assessment.Reason = "evidence is not authoritative"
	return assessment
}

type positiveStoreTargetConsensus struct {
	Target           *ExactStoreUpdateTarget
	AvailableVersion string
	Observation      StoreProviderObservation
}

type positiveStoreTargetDescriptor struct {
	Observation     StoreProviderObservation
	ProviderKey     string
	ProviderBackend string
	ProductID       string
	UpdateID        string
	OfferedVersion  string
	VerificationBy  string
	VerifiedAt      time.Time
}

func reconcilePositiveStoreTargets(identity StoreInstalledIdentity, positives []StoreProviderObservation) (positiveStoreTargetConsensus, error) {
	descriptors := make([]positiveStoreTargetDescriptor, 0, len(positives))
	for _, positive := range positives {
		if positive.Target == nil || !positive.Target.ExactFor(identity) {
			continue
		}
		descriptors = append(descriptors, positiveStoreTargetDescriptor{
			Observation:     positive,
			ProviderKey:     storeProviderKey(positive.Target.Provider),
			ProviderBackend: strings.TrimSpace(positive.Target.Provider.Backend),
			ProductID:       strings.TrimSpace(positive.Target.ProductID),
			UpdateID:        strings.TrimSpace(positive.Target.UpdateID),
			OfferedVersion:  strings.TrimSpace(positive.AvailableVersion),
			VerificationBy:  strings.TrimSpace(positive.Target.VerifiedBy),
			VerifiedAt:      positive.Target.VerifiedAt,
		})
	}
	if len(descriptors) == 0 {
		return positiveStoreTargetConsensus{}, fmt.Errorf("positive update evidence has no exact verified target")
	}
	sort.SliceStable(descriptors, func(i, j int) bool {
		return positiveStoreTargetSortKey(descriptors[i]) < positiveStoreTargetSortKey(descriptors[j])
	})
	productIDs := map[string]string{}
	updateIDs := map[string]string{}
	knownVersions := map[string]string{}
	for _, descriptor := range descriptors {
		if descriptor.ProductID != "" {
			productIDs[strings.ToLower(descriptor.ProductID)] = descriptor.ProductID
		}
		if descriptor.UpdateID != "" {
			updateIDs[strings.ToLower(descriptor.UpdateID)] = descriptor.UpdateID
		}
		if descriptor.OfferedVersion != "" {
			knownVersions[strings.ToLower(descriptor.OfferedVersion)] = descriptor.OfferedVersion
		}
	}
	if len(productIDs) > 1 {
		return positiveStoreTargetConsensus{}, storeTargetConflictError("product_id", descriptors)
	}
	if hasConflictingNonPFNUpdateIDs(identity.PackageFamilyName, updateIDs) {
		return positiveStoreTargetConsensus{}, storeTargetConflictError("update_id", descriptors)
	}
	if len(knownVersions) > 1 {
		return positiveStoreTargetConsensus{}, storeTargetConflictError("offered_version", descriptors)
	}
	canonical := descriptors[0]
	target := *canonical.Observation.Target
	target.ProductID = firstValueBySortedKey(productIDs)
	target.UpdateID = preferredUpdateID(identity.PackageFamilyName, updateIDs)
	target.Provider = canonical.Observation.Target.Provider
	target.VerifiedBy = canonical.VerificationBy
	target.VerifiedAt = canonical.VerifiedAt
	return positiveStoreTargetConsensus{
		Target:           &target,
		AvailableVersion: firstValueBySortedKey(knownVersions),
		Observation:      canonical.Observation,
	}, nil
}

func positiveStoreTargetSortKey(descriptor positiveStoreTargetDescriptor) string {
	return strings.ToLower(strings.Join([]string{
		descriptor.ProviderKey,
		descriptor.ProviderBackend,
		descriptor.ProductID,
		descriptor.UpdateID,
		descriptor.OfferedVersion,
	}, "|"))
}

func hasConflictingNonPFNUpdateIDs(pfn string, updateIDs map[string]string) bool {
	if len(updateIDs) <= 1 {
		return false
	}
	pfnKey := strings.ToLower(strings.TrimSpace(pfn))
	if pfnKey == "" {
		return true
	}
	nonPFN := 0
	for key := range updateIDs {
		if key != pfnKey {
			nonPFN++
		}
	}
	return nonPFN > 1 || nonPFN == len(updateIDs)
}

func preferredUpdateID(pfn string, updateIDs map[string]string) string {
	if len(updateIDs) == 0 {
		return ""
	}
	if value, ok := updateIDs[strings.ToLower(strings.TrimSpace(pfn))]; ok {
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

func storeTargetConflictError(field string, descriptors []positiveStoreTargetDescriptor) error {
	parts := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		value := ""
		switch field {
		case "product_id":
			value = descriptor.ProductID
		case "update_id":
			value = descriptor.UpdateID
		case "offered_version":
			value = descriptor.OfferedVersion
		}
		if value == "" {
			continue
		}
		parts = append(parts, storeProviderKey(descriptor.Observation.Provider)+"="+sanitizeProviderDiagnostic(value))
	}
	sort.Strings(parts)
	return fmt.Errorf("healthy providers returned conflicting %s values: %s", field, strings.Join(parts, ", "))
}

func storeConflictAssessmentDecision(assessment StoreUpdateAssessment, positives []StoreProviderObservation, reason string) StoreUpdateAssessment {
	observation := positives[0]
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

func allRequiredProvidersNegative(required map[string]bool, negatives []StoreProviderObservation) bool {
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
