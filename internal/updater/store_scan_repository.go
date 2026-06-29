package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type StoreScanSnapshot struct {
	SchemaVersion int  `json:"schema_version"`
	Published     bool `json:"published"`
	// RecoveredFromFallback is set in memory when a published snapshot is used
	// only because a newer snapshot file could not be decoded. Recovered
	// evidence is retained for diagnostics, but must not authorize updates.
	RecoveredFromFallback bool                       `json:"recovered_from_fallback,omitempty"`
	Scan                  StoreScanGeneration        `json:"scan"`
	Inventory             StorePackagedAppInventory  `json:"inventory"`
	ProviderRuns          []StoreCatalogProviderRun  `json:"provider_runs"`
	Assessments           []StorePublishedAssessment `json:"assessments"`
}

type StorePublishedAssessment struct {
	StoreUpdateAssessment
	ObservedAt                 time.Time
	Stale                      bool
	StoreProductID             string
	UpdateID                   string
	ExactActionTargetAvailable bool
	Applicability              string
}

type StoreScanRepository interface {
	PersistCompletedScanSnapshot(context.Context, StoreScanSnapshot) (bool, error)
	LoadLatestPublishedSnapshot(context.Context, string) (StoreScanSnapshot, bool, error)
	LoadPreviousSnapshot(context.Context, string, StoreScanGeneration) (StoreScanSnapshot, bool, error)
	Close() error
}

const (
	storeScanSchemaVersion        = 2
	storeScanRetentionRunsUser    = 50
	legacyStoreScanSQLiteFileName = "store-scans.sqlite"
)

func openDefaultStoreScanRepository() (StoreScanRepository, error) {
	if err := retireLegacyStoreScanSQLiteCache(); err != nil {
		appLog("Legacy Store SQLite cache was left untouched: %s", sanitizeProviderDiagnostic(err.Error()))
	}
	return openDefaultStoreScanFileRepository()
}

func retireLegacyStoreScanSQLiteCache() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, legacyStoreScanSQLiteFileName)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	target := path + ".legacy-cache." + time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, target); err != nil {
		return err
	}
	appLog("Retired legacy Store SQLite cache to %s.", filepath.Base(target))
	return nil
}

func snapshotFromScanResult(scan StoreScanGeneration, inventory StorePackagedAppInventory, providerRuns []StoreCatalogProviderRun, assessments []StorePublishedAssessment, publish bool) StoreScanSnapshot {
	return StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     publish,
		Scan:          scan,
		Inventory:     inventory,
		ProviderRuns:  providerRuns,
		Assessments:   assessments,
	}
}

func previousAssessmentsFromSnapshot(snapshot StoreScanSnapshot) map[StoreInstalledIdentity]StorePublishedAssessment {
	previous := map[StoreInstalledIdentity]StorePublishedAssessment{}
	for _, assessment := range snapshot.Assessments {
		previous[assessment.Identity] = assessment
	}
	return previous
}

func providerSummariesFromRuns(runs []StoreCatalogProviderRun) []StorePackageProviderSummary {
	summaries := make([]StorePackageProviderSummary, 0, len(runs))
	for _, run := range runs {
		name := firstNonEmpty(run.Provider.Name, run.Provider.Key())
		observedAt := run.CompletedAt
		if observedAt.IsZero() {
			observedAt = run.StartedAt
		}
		summaries = append(summaries, StorePackageProviderSummary{
			Name:       name,
			Version:    strings.TrimSpace(run.Version),
			Health:     string(run.Health),
			Kind:       providerRunSummaryKind(run.Health),
			ObservedAt: formatStoreScanTime(observedAt),
			Error:      sanitizeProviderDiagnostic(run.Error),
		})
	}
	return uniqueStoreProviderSummaries(summaries)
}

func providerRunSummaryKind(health StoreProviderHealth) string {
	switch health {
	case StoreProviderHealthy:
		return "provider_run"
	case StoreProviderFailed:
		return string(StoreObservationProviderFailure)
	case StoreProviderUnsupported:
		return string(StoreObservationUnsupportedProvider)
	case StoreProviderStale:
		return string(StoreObservationStaleResult)
	default:
		return string(StoreObservationIncompleteResult)
	}
}

func formatStoreScanTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339Nano)
}

func validateStoreScanSnapshot(snapshot StoreScanSnapshot) error {
	if snapshot.Scan.ScanID == "" {
		return errors.New("Store scan snapshot is missing scan ID")
	}
	if snapshot.Scan.UserSID == "" {
		return errors.New("Store scan snapshot is missing user SID")
	}
	if snapshot.Scan.StartedAt.IsZero() {
		return errors.New("Store scan snapshot is missing start time")
	}
	for _, family := range snapshot.Inventory.Families {
		if !family.Identity.Resolved() || family.Identity.UserSID != snapshot.Scan.UserSID {
			return errors.New("Store scan snapshot contains cross-user or unresolved inventory family")
		}
	}
	for _, run := range snapshot.ProviderRuns {
		for _, mapping := range run.Mappings {
			if !mapping.VerifiedFor(mapping.InstalledIdentity, snapshot.Scan) {
				return errors.New("Store scan snapshot contains unverifiable identity mapping")
			}
		}
		for _, observation := range run.Observations {
			if observation.ScanID != snapshot.Scan.ScanID || observation.Identity.UserSID != snapshot.Scan.UserSID || !observation.Identity.Resolved() {
				return errors.New("Store scan snapshot contains cross-user or cross-generation observation")
			}
		}
	}
	for _, assessment := range snapshot.Assessments {
		if assessment.ScanID != snapshot.Scan.ScanID || assessment.Identity.UserSID != snapshot.Scan.UserSID || !assessment.Identity.Resolved() {
			return errors.New("Store scan snapshot contains cross-user or cross-generation assessment")
		}
	}
	return nil
}

func sortStoreScanSnapshot(snapshot *StoreScanSnapshot) {
	sort.Slice(snapshot.Inventory.Records, func(i, j int) bool {
		left, right := snapshot.Inventory.Records[i], snapshot.Inventory.Records[j]
		if left.PackageFamilyName != right.PackageFamilyName {
			return left.PackageFamilyName < right.PackageFamilyName
		}
		return left.PackageFullName < right.PackageFullName
	})
	sort.Slice(snapshot.Inventory.Families, func(i, j int) bool {
		return snapshot.Inventory.Families[i].Identity.PackageFamilyName < snapshot.Inventory.Families[j].Identity.PackageFamilyName
	})
	for index := range snapshot.Inventory.Families {
		sort.Slice(snapshot.Inventory.Families[index].Instances, func(i, j int) bool {
			return snapshot.Inventory.Families[index].Instances[i].PackageFullName < snapshot.Inventory.Families[index].Instances[j].PackageFullName
		})
	}
	sort.Slice(snapshot.ProviderRuns, func(i, j int) bool {
		return snapshot.ProviderRuns[i].Provider.Key() < snapshot.ProviderRuns[j].Provider.Key()
	})
	for index := range snapshot.ProviderRuns {
		sort.Slice(snapshot.ProviderRuns[index].Observations, func(i, j int) bool {
			left, right := snapshot.ProviderRuns[index].Observations[i], snapshot.ProviderRuns[index].Observations[j]
			if left.Identity.PackageFamilyName != right.Identity.PackageFamilyName {
				return left.Identity.PackageFamilyName < right.Identity.PackageFamilyName
			}
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			return left.ObservedAt.Before(right.ObservedAt)
		})
		sort.Slice(snapshot.ProviderRuns[index].Mappings, func(i, j int) bool {
			left, right := snapshot.ProviderRuns[index].Mappings[i], snapshot.ProviderRuns[index].Mappings[j]
			if left.InstalledIdentity.PackageFamilyName != right.InstalledIdentity.PackageFamilyName {
				return left.InstalledIdentity.PackageFamilyName < right.InstalledIdentity.PackageFamilyName
			}
			if left.ProductID != right.ProductID {
				return left.ProductID < right.ProductID
			}
			return left.Provider.Key() < right.Provider.Key()
		})
	}
	sort.Slice(snapshot.Assessments, func(i, j int) bool {
		return snapshot.Assessments[i].Identity.PackageFamilyName < snapshot.Assessments[j].Identity.PackageFamilyName
	})
	for index := range snapshot.Assessments {
		sort.Slice(snapshot.Assessments[index].Evidence, func(i, j int) bool {
			left, right := snapshot.Assessments[index].Evidence[i], snapshot.Assessments[index].Evidence[j]
			if left.Provider != right.Provider {
				return left.Provider < right.Provider
			}
			if left.Health != right.Health {
				return left.Health < right.Health
			}
			return left.Kind < right.Kind
		})
	}
}

func targetFromPersistedObservation(identity StoreInstalledIdentity, provider StoreProviderIdentity, productID, updateID string, verified bool, verifiedAt time.Time) *ExactStoreUpdateTarget {
	productID = strings.TrimSpace(productID)
	updateID = strings.TrimSpace(updateID)
	if !verified || (productID == "" && updateID == "") || (productID != "" && !looksLikeStoreProductID(productID)) {
		return nil
	}
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	return &ExactStoreUpdateTarget{
		Identity:   identity,
		Provider:   provider,
		ProductID:  productID,
		UpdateID:   updateID,
		Verified:   true,
		VerifiedBy: firstNonEmpty(provider.Key(), provider.Name),
		VerifiedAt: verifiedAt,
	}
}
