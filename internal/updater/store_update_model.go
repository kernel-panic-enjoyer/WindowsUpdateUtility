package updater

import "time"

const storeUpdateAssessmentModelEnabled = false

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
	WindowsVersion   string                         `json:"windows_version,omitempty"`
	WindowsBuild     string                         `json:"windows_build,omitempty"`
	Architecture     string                         `json:"architecture,omitempty"`
	ProviderVersions map[string]string              `json:"provider_versions,omitempty"`
	CompletionStatus StoreScanCompletionStatus      `json:"completion_status"`
	ProviderHealth   map[string]StoreProviderHealth `json:"provider_health,omitempty"`
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
	InstalledIdentity StoreInstalledIdentity `json:"installed_identity"`
	ProductID         string                 `json:"product_id"`
	Provider          StoreProviderIdentity  `json:"provider"`
	ScanID            string                 `json:"scan_id"`
	VerifiedAt        time.Time              `json:"verified_at"`
	Evidence          string                 `json:"evidence,omitempty"`
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
		providerKey := observation.Provider.Key()
		if providerKey == "" {
			providerKey = "unknown"
		}
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
		return storeAssessmentFromObservation(StoreUpdateConflict, input, positives[0], "healthy providers disagree")
	}
	if len(positives) > 0 {
		return storeAssessmentFromObservation(StoreUpdateAvailable, input, positives[0], "fresh exact positive update evidence")
	}
	if len(positivesWithoutTarget) > 0 {
		return storeAssessmentFromObservation(StoreUpdateUnknown, input, positivesWithoutTarget[0], "positive update evidence has no exact verified target")
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
		return storeAssessmentFromObservation(StoreUpdateUnknown, input, blockedObservation, "provider incomplete or failed: "+blockedProvider)
	}
	if len(pending) > 0 {
		return storeAssessmentFromObservation(StoreUpdatePending, input, pending[0], "update is pending verification")
	}
	if len(inapplicable) > 0 {
		return storeAssessmentFromObservation(StoreUpdateInapplicable, input, inapplicable[0], "newer catalog version has no applicable installer")
	}
	if !scanComplete {
		assessment.Reason = "scan generation is incomplete"
		return assessment
	}
	if allRequiredProvidersNegative(required, negatives) {
		return storeAssessmentFromObservation(StoreUpdateCurrent, input, negatives[0], "all required providers returned authoritative negatives")
	}

	assessment.Reason = "evidence is not authoritative"
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
		seen[negative.Provider.Key()] = true
	}
	for provider := range required {
		if !seen[provider] {
			return false
		}
	}
	return true
}

func storeAssessmentFromObservation(state StoreUpdateState, input StoreReconciliationInput, observation StoreProviderObservation, reason string) StoreUpdateAssessment {
	assessment := StoreUpdateAssessment{
		State:            state,
		Identity:         input.Identity,
		ScanID:           input.Scan.ScanID,
		Reason:           reason,
		InstalledVersion: observation.InstalledVersion,
		AvailableVersion: observation.AvailableVersion,
		Target:           observation.Target,
		ProviderHealth:   map[string]StoreProviderHealth{},
	}
	for _, candidate := range input.Observations {
		if !candidate.Matches(input.Identity, input.Scan) {
			assessment.RejectedEvidenceCount++
			continue
		}
		providerKey := candidate.Provider.Key()
		if providerKey == "" {
			providerKey = "unknown"
		}
		assessment.ProviderHealth[providerKey] = candidate.Health
		assessment.Evidence = append(assessment.Evidence, StoreEvidenceSummary{
			Provider: providerKey,
			Health:   candidate.Health,
			Kind:     candidate.Kind,
		})
	}
	return assessment
}

func StoreAssessmentToLegacyPackage(pkg Package, assessment StoreUpdateAssessment) Package {
	pkg.UpdateAvailable = assessment.State == StoreUpdateAvailable
	if assessment.AvailableVersion != "" {
		pkg.AvailableVersion = assessment.AvailableVersion
	} else if !pkg.UpdateAvailable {
		pkg.AvailableVersion = ""
	}
	switch assessment.State {
	case StoreUpdateAvailable, StoreUpdateCurrent:
		pkg.UpdateSupported = true
	default:
		pkg.UpdateSupported = false
	}
	return pkg
}
