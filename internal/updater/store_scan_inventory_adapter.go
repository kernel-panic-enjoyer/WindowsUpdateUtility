package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	runStoreTransactionalScanForInventory   = runDefaultStoreScanPipeline
	openStoreTransactionalStoreForInventory = openDefaultStoreScanRepository
	scheduledStoreScanWaitTimeout           = 2 * time.Minute
	scheduledStoreScanPollInterval          = 250 * time.Millisecond
)

// StoreInventoryProjectionResult describes the Store scan attempted for a
// projection and the published generation actually overlaid onto inventory.
// ScanID/StartedAt/CompletedAt/Published/CompletionStatus describe the scan
// attempt when one ran in this process. UsedGenerationID describes the published
// snapshot used for the returned Inventory. FreshGeneration is true only when
// that snapshot is acceptable for scheduled Store automation.
type StoreInventoryProjectionResult struct {
	Inventory        Inventory
	ScanID           string
	StartedAt        time.Time
	CompletedAt      time.Time
	Published        bool
	CompletionStatus StoreScanCompletionStatus
	UsedGenerationID string
	FreshGeneration  bool
	Error            error
}

func applyStoreTransactionalScanPipeline(ctx context.Context, state State, inventory Inventory) Inventory {
	return applyStoreTransactionalScanPipelineResult(ctx, state, inventory, time.Time{}).Inventory
}

func applyStoreTransactionalScanPipelineResult(ctx context.Context, state State, inventory Inventory, freshAfter time.Time) StoreInventoryProjectionResult {
	result, err := runStoreTransactionalScanForInventory(ctx)
	projection := loadLatestPublishedStoreProjection(ctx, state, inventory, freshAfter)
	projection.applyScanAttempt(result)
	if err != nil {
		appLog("Store transactional scan failed: %s", err)
		projection.Error = err
		if errors.Is(err, errStoreScanAlreadyRunning) && !freshAfter.IsZero() {
			waited, waitErr := waitForFreshPublishedStoreSnapshot(ctx, freshAfter, scheduledStoreScanWaitTimeout)
			if waitErr != nil {
				projection.Error = waitErr
				appLog("Timed out waiting for concurrent Store scan publication: %s", waitErr)
				return projection
			}
			projection = projectionFromPublishedStoreSnapshot(state, inventory, waited, freshAfter)
			projection.Error = nil
		}
		return projection
	} else if !result.Published {
		appLog("Store transactional scan %s completed but was not published.", result.Scan.ScanID)
		projection.Error = fmt.Errorf("Store scan %s completed but was not published", result.Scan.ScanID)
	}
	projection.applyScanAttempt(result)
	return projection
}

func (result *StoreInventoryProjectionResult) applyScanAttempt(scan StoreScanResult) {
	if scan.Scan.ScanID == "" {
		return
	}
	result.ScanID = scan.Scan.ScanID
	result.StartedAt = scan.Scan.StartedAt
	result.CompletedAt = scan.Scan.CompletedAt
	result.Published = scan.Published
	result.CompletionStatus = scan.Scan.CompletionStatus
}

func applyPublishedStoreScanAssessments(ctx context.Context, state State, inventory Inventory) Inventory {
	return loadLatestPublishedStoreProjection(ctx, state, inventory, time.Time{}).Inventory
}

// loadLatestPublishedStoreProjection overlays the latest published Store scan
// onto a deep-copied manager inventory. The cached base inventory remains
// manager/native truth; Store assessments are a read-time projection with their
// own freshness and authorization rules.
func loadLatestPublishedStoreProjection(ctx context.Context, state State, inventory Inventory, freshAfter time.Time) StoreInventoryProjectionResult {
	inventory = applyStateAndCapabilitiesToInventory(state, inventory.DeepCopy())
	result := StoreInventoryProjectionResult{Inventory: inventory}
	repository, openErr := openStoreTransactionalStoreForInventory()
	if openErr != nil {
		appLog("Could not open Store scan store: %s", openErr)
		result.Inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(openErr.Error())}
		result.Error = openErr
		return result
	}
	defer repository.Close()
	userSID, sidErr := storeScanCurrentUserSID()
	if sidErr != nil {
		appLog("Could not load Store scan assessments because current user SID is unavailable: %s", sidErr)
		result.Inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(sidErr.Error())}
		result.Error = sidErr
		return result
	}
	snapshot, ok, err := repository.LoadLatestPublishedSnapshot(ctx, userSID)
	if err != nil {
		appLog("Could not load published Store scan assessments: %s", err)
		result.Inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(err.Error())}
		result.Error = err
		return result
	}
	providerSummaries := providerSummariesFromRuns(snapshot.ProviderRuns)
	if !ok || len(snapshot.Assessments) == 0 {
		result.Inventory.StoreScanHealth = buildStoreScanHealthSummary(result.Inventory.Packages, providerSummaries)
		if !result.Inventory.StoreScanHealth.Active {
			result.Inventory.StoreScanHealth = StoreScanHealthSummary{
				Active:        true,
				Healthy:       false,
				Authoritative: false,
				Status:        string(StoreScanIncomplete),
				Reason:        "no published Store update assessments are available",
				Providers:     providerSummaries,
			}
		}
		return result
	}
	return projectionFromPublishedStoreSnapshot(state, result.Inventory, snapshot, freshAfter)
}

func projectionFromPublishedStoreSnapshot(state State, inventory Inventory, snapshot StoreScanSnapshot, freshAfter time.Time) StoreInventoryProjectionResult {
	providerSummaries := providerSummariesFromRuns(snapshot.ProviderRuns)
	familyNames := map[string]StorePackagedAppFamily{}
	for _, family := range snapshot.Inventory.Families {
		familyNames[strings.ToLower(family.Identity.PackageFamilyName)] = family
	}
	inventory = applyPublishedStoreAssessmentsToInventory(state, inventory, snapshot, familyNames, providerSummaries)
	inventory.StoreScanHealth = buildStoreScanHealthSummary(inventory.Packages, providerSummaries)
	return StoreInventoryProjectionResult{
		Inventory:        inventory,
		ScanID:           snapshot.Scan.ScanID,
		StartedAt:        snapshot.Scan.StartedAt,
		CompletedAt:      snapshot.Scan.CompletedAt,
		Published:        snapshot.Published,
		CompletionStatus: snapshot.Scan.CompletionStatus,
		UsedGenerationID: snapshot.Scan.ScanID,
		FreshGeneration:  publishedStoreSnapshotFreshForScheduledUse(snapshot, freshAfter),
	}
}

func waitForFreshPublishedStoreSnapshot(ctx context.Context, freshAfter time.Time, timeout time.Duration) (StoreScanSnapshot, error) {
	if timeout <= 0 {
		timeout = scheduledStoreScanWaitTimeout
	}
	repository, openErr := openStoreTransactionalStoreForInventory()
	if openErr != nil {
		return StoreScanSnapshot{}, openErr
	}
	defer repository.Close()
	userSID, sidErr := storeScanCurrentUserSID()
	if sidErr != nil {
		return StoreScanSnapshot{}, sidErr
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(scheduledStoreScanPollInterval)
	defer ticker.Stop()
	for {
		snapshot, ok, err := repository.LoadLatestPublishedSnapshot(ctx, userSID)
		if err != nil {
			return StoreScanSnapshot{}, err
		}
		if ok && publishedStoreSnapshotFreshForScheduledUse(snapshot, freshAfter) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return StoreScanSnapshot{}, ctx.Err()
		case <-timer.C:
			return StoreScanSnapshot{}, fmt.Errorf("no fresh published Store scan completed after %s", freshAfter.UTC().Format(time.RFC3339))
		case <-ticker.C:
		}
	}
}

func publishedStoreSnapshotFreshForScheduledUse(snapshot StoreScanSnapshot, freshAfter time.Time) bool {
	if !snapshot.Published || snapshot.RecoveredFromFallback || snapshot.Scan.CompletedAt.IsZero() {
		return false
	}
	switch snapshot.Scan.CompletionStatus {
	case StoreScanCompleted, StoreScanIncomplete:
	default:
		return false
	}
	if freshAfter.IsZero() {
		return true
	}
	return !snapshot.Scan.CompletedAt.Before(freshAfter.UTC())
}

func applyPublishedStoreAssessmentsToInventory(state State, inventory Inventory, snapshot StoreScanSnapshot, families map[string]StorePackagedAppFamily, scanProviders []StorePackageProviderSummary) Inventory {
	now := storeScanNow()
	byPFN := map[string]int{}
	for index, pkg := range inventory.Packages {
		if pkg.Manager != managerStore {
			continue
		}
		pfn := strings.ToLower(storeInstalledPackageFamilyName(pkg))
		if pfn != "" {
			byPFN[pfn] = index
		}
	}
	for _, assessment := range snapshot.Assessments {
		pfn := strings.TrimSpace(assessment.Identity.PackageFamilyName)
		if pfn == "" {
			continue
		}
		key := strings.ToLower(pfn)
		index, ok := byPFN[key]
		if !ok {
			// Why: Store-native AppX inventory can contain current-user packages
			// absent from winget/choco inventory. Projection may add them to the
			// UI, but only fresh exact assessments can make them actionable.
			assessment = assessmentForInventoryProjection(snapshot, assessment, "", now)
			pkg := packageFromPublishedStoreAssessment(state, assessment, families[key], scanProviders)
			inventory.Packages = append(inventory.Packages, pkg)
			byPFN[key] = len(inventory.Packages) - 1
			continue
		}
		assessment = assessmentForInventoryProjection(snapshot, assessment, inventory.Packages[index].Version, now)
		pkg := applyPublishedStoreAssessmentToPackage(inventory.Packages[index], assessment, scanProviders)
		pkg.AutoUpdate = packageAutoUpdateEnabled(state, pkg)
		inventory.Packages[index] = pkg
	}
	return inventory
}

func assessmentForInventoryProjection(snapshot StoreScanSnapshot, assessment StorePublishedAssessment, currentInstalledVersion string, now time.Time) StorePublishedAssessment {
	freshness := evaluatePublishedStoreAssessmentFreshness(snapshot, assessment, currentInstalledVersion, now)
	if freshness.Fresh {
		return assessment
	}
	return staleStoreAssessmentProjection(assessment, freshness.Reason)
}

func packageFromPublishedStoreAssessment(state State, assessment StorePublishedAssessment, family StorePackagedAppFamily, scanProviders []StorePackageProviderSummary) Package {
	name := friendlyAppxName(assessment.Identity.PackageFamilyName, "")
	version := assessment.InstalledVersion
	if family.Identity.PackageFamilyName != "" {
		name = firstNonEmpty(
			family.DisplayName,
			friendlyAppxName(family.Primary.IdentityName, family.Primary.DisplayName),
			friendlyAppxName(family.Identity.PackageFamilyName, ""),
			name,
		)
		version = firstNonEmpty(version, family.Primary.Version.String())
	}
	pkg := Package{
		Key:                        packageKey(managerStore, assessment.Identity.PackageFamilyName),
		Manager:                    managerStore,
		ID:                         assessment.Identity.PackageFamilyName,
		Name:                       name,
		Version:                    version,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		Match:                      firstNonEmpty(family.Primary.PackageFullName, assessment.Identity.PackageFamilyName),
		ActionBackend:              backendAppXInventory,
		UpdateSupported:            assessment.ExactActionTargetAvailable,
		AutoUpdate:                 false,
		InstalledPackageFamilyName: assessment.Identity.PackageFamilyName,
		ExactIdentityAvailable:     true,
		ExactActionTargetAvailable: assessment.ExactActionTargetAvailable,
	}
	pkg.AutoUpdate = packageAutoUpdateEnabled(state, pkg)
	return applyPublishedStoreAssessmentToPackage(pkg, assessment, scanProviders)
}

func applyPublishedStoreAssessmentToPackage(pkg Package, assessment StorePublishedAssessment, scanProviders []StorePackageProviderSummary) Package {
	pkg.UpdateState = string(assessment.State)
	pkg.UpdateReason = assessment.Reason
	pkg.ObservedAt = formatAssessmentTime(assessment.ObservedAt)
	pkg.Stale = assessment.Stale
	pkg.ScanID = assessment.ScanID
	pkg.ExactIdentityAvailable = true
	pkg.ExactActionTargetAvailable = assessment.ExactActionTargetAvailable
	pkg.InstalledPackageFamilyName = assessment.Identity.PackageFamilyName
	pkg.StoreProductID = assessment.StoreProductID
	pkg.StoreUpdateID = assessment.UpdateID
	pkg.InstalledVersion = firstNonEmpty(assessment.InstalledVersion, pkg.Version)
	pkg.OfferedVersion = assessment.AvailableVersion
	pkg.Applicability = assessment.Applicability
	pkg.ProviderSummaries = providerSummariesFromEvidence(assessment.Evidence, assessment.ObservedAt, scanProviders)
	// Why: UI flags mirror backend authorization, but they are not the security
	// boundary. Store execution revalidates the published assessment and exact
	// target before running any update command.
	pkg.UpdateAvailable = assessment.State == StoreUpdateAvailable && !assessment.Stale && assessment.ExactActionTargetAvailable
	pkg.AvailableVersion = assessment.AvailableVersion
	if !pkg.UpdateAvailable && assessment.State != StoreUpdatePending {
		pkg.AvailableVersion = ""
	}
	if pkg.UpdateAvailable && !assessment.ExactActionTargetAvailable {
		pkg.UpdateSupported = false
	} else if pkg.UpdateAvailable {
		pkg.UpdateSupported = true
	} else if assessment.Stale {
		pkg.UpdateSupported = false
	}
	if (assessment.StoreProductID != "" || assessment.UpdateID != "") && assessment.ExactActionTargetAvailable {
		pkg.ActionBackend = backendStoreCLI
	}
	pkg.Key = storePackagePublicKey(pkg)
	pkg = applyPackageCapabilities(pkg)
	return pkg
}

func providerSummariesFromEvidence(evidence []StoreEvidenceSummary, observedAt time.Time, scanProviders []StorePackageProviderSummary) []StorePackageProviderSummary {
	observed := ""
	if !observedAt.IsZero() {
		observed = formatAssessmentTime(observedAt)
	}
	summaries := make([]StorePackageProviderSummary, 0, len(evidence)+len(scanProviders))
	for _, item := range evidence {
		summaries = append(summaries, StorePackageProviderSummary{
			Name:       item.Provider,
			Health:     string(item.Health),
			Kind:       string(item.Kind),
			ObservedAt: observed,
		})
	}
	summaries = append(summaries, scanProviders...)
	return uniqueStoreProviderSummaries(summaries)
}
