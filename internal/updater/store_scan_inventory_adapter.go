package updater

import (
	"context"
	"strings"
	"time"
)

var (
	runStoreTransactionalScanForInventory   = runDefaultStoreScanPipeline
	openStoreTransactionalStoreForInventory = openDefaultStoreScanStore
)

func applyStoreTransactionalScanPipeline(ctx context.Context, state State, inventory Inventory) Inventory {
	if !storeTransactionalScanEnabled() {
		return inventory
	}
	result, err := runStoreTransactionalScanForInventory(ctx)
	if err != nil {
		appLog("Store transactional scan failed: %s", err)
	} else if !result.Published {
		appLog("Store transactional scan %s completed but was not published.", result.Scan.ScanID)
	}
	return applyPublishedStoreScanAssessments(ctx, state, inventory, result.Inventory)
}

func applyPublishedStoreScanAssessments(ctx context.Context, state State, inventory Inventory, resultInventory StorePackagedAppInventory) Inventory {
	if !storeTransactionalScanEnabled() {
		return inventory
	}
	store, openErr := openStoreTransactionalStoreForInventory()
	if openErr != nil {
		appLog("Could not open Store scan store: %s", openErr)
		inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(openErr.Error())}
		return inventory
	}
	defer store.Close()
	userSID, sidErr := storeScanCurrentUserSID()
	if sidErr != nil {
		appLog("Could not load Store scan assessments because current user SID is unavailable: %s", sidErr)
		inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(sidErr.Error())}
		return inventory
	}
	assessments, err := store.PublishedAssessments(ctx, userSID)
	if err != nil {
		appLog("Could not load published Store scan assessments: %s", err)
		inventory.StoreScanHealth = StoreScanHealthSummary{Active: true, Healthy: false, Authoritative: false, Status: string(StoreScanFailed), Reason: sanitizeProviderDiagnostic(err.Error())}
		return inventory
	}
	providerSummaries, providerErr := store.LatestPublishedProviderSummaries(ctx, userSID)
	if providerErr != nil {
		appLog("Could not load published Store provider diagnostics: %s", providerErr)
	}
	if len(assessments) == 0 {
		inventory.StoreScanHealth = buildStoreScanHealthSummary(inventory.Packages, providerSummaries)
		if !inventory.StoreScanHealth.Active {
			inventory.StoreScanHealth = StoreScanHealthSummary{
				Active:        true,
				Healthy:       false,
				Authoritative: false,
				Status:        string(StoreScanIncomplete),
				Reason:        "no published Store update assessments are available",
				Providers:     providerSummaries,
			}
		}
		return inventory
	}
	familyNames := map[string]StorePackagedAppFamily{}
	if resultInventory.Scan.UserSID == userSID {
		for _, family := range resultInventory.Families {
			familyNames[strings.ToLower(family.Identity.PackageFamilyName)] = family
		}
	}
	inventory = applyPublishedStoreAssessmentsToInventory(state, inventory, assessments, familyNames, providerSummaries)
	inventory.StoreScanHealth = buildStoreScanHealthSummary(inventory.Packages, providerSummaries)
	return inventory
}

func applyPublishedStoreAssessmentsToInventory(state State, inventory Inventory, assessments []StorePublishedAssessment, families map[string]StorePackagedAppFamily, scanProviders []StorePackageProviderSummary) Inventory {
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
	for _, assessment := range assessments {
		pfn := strings.TrimSpace(assessment.Identity.PackageFamilyName)
		if pfn == "" {
			continue
		}
		key := strings.ToLower(pfn)
		index, ok := byPFN[key]
		if !ok {
			pkg := packageFromPublishedStoreAssessment(state, assessment, families[key], scanProviders)
			inventory.Packages = append(inventory.Packages, pkg)
			byPFN[key] = len(inventory.Packages) - 1
			continue
		}
		inventory.Packages[index] = applyPublishedStoreAssessmentToPackage(inventory.Packages[index], assessment, scanProviders)
	}
	return inventory
}

func packageFromPublishedStoreAssessment(state State, assessment StorePublishedAssessment, family StorePackagedAppFamily, scanProviders []StorePackageProviderSummary) Package {
	name := assessment.Identity.PackageFamilyName
	version := assessment.InstalledVersion
	if family.Identity.PackageFamilyName != "" {
		name = firstNonEmpty(family.DisplayName, family.Primary.IdentityName, family.Identity.PackageFamilyName)
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
	pkg.UpdateAvailable = assessment.State == StoreUpdateAvailable
	pkg.AvailableVersion = assessment.AvailableVersion
	if pkg.UpdateAvailable && !assessment.ExactActionTargetAvailable {
		pkg.UpdateSupported = false
	} else if pkg.UpdateAvailable {
		pkg.UpdateSupported = true
	}
	if (assessment.StoreProductID != "" || assessment.UpdateID != "") && assessment.ExactActionTargetAvailable {
		pkg.ActionBackend = backendStoreCLI
	}
	pkg.Key = storePackagePublicKey(pkg)
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
