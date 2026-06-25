package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type StoreDiagnosticsExport struct {
	GeneratedAt         string                         `json:"generated_at"`
	SchemaVersion       int                            `json:"schema_version"`
	DetectorMode        string                         `json:"detector_mode"`
	UserScopeHash       string                         `json:"user_scope_hash,omitempty"`
	Scan                StoreDiagnosticsScan           `json:"scan"`
	Providers           []StorePackageProviderSummary  `json:"providers,omitempty"`
	Packages            []StoreDiagnosticsPackage      `json:"packages,omitempty"`
	Observations        []StoreDiagnosticsObservation  `json:"observations,omitempty"`
	Assessments         []StoreDiagnosticsAssessment   `json:"assessments,omitempty"`
	AutoUpdateMigration StoreAutoUpdateMigrationReport `json:"auto_update_migration,omitempty"`
	Errors              []string                       `json:"errors,omitempty"`
}

type StoreDiagnosticsScan struct {
	ScanID         string           `json:"scan_id,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	StartedAt      string           `json:"started_at,omitempty"`
	CompletedAt    string           `json:"completed_at,omitempty"`
	WindowsVersion string           `json:"windows_version,omitempty"`
	WindowsBuild   string           `json:"windows_build,omitempty"`
	Architecture   string           `json:"architecture,omitempty"`
	Status         string           `json:"status,omitempty"`
	Metrics        StoreScanMetrics `json:"metrics,omitempty"`
}

type StoreDiagnosticsPackage struct {
	PackageFamilyName string `json:"package_family_name"`
	DisplayName       string `json:"display_name,omitempty"`
	ProductLike       bool   `json:"product_like"`
}

type StoreDiagnosticsObservation struct {
	Provider          string `json:"provider"`
	PackageFamilyName string `json:"package_family_name"`
	Kind              string `json:"kind"`
	Health            string `json:"health"`
	ObservedAt        string `json:"observed_at"`
	InstalledVersion  string `json:"installed_version,omitempty"`
	AvailableVersion  string `json:"available_version,omitempty"`
	CatalogVersion    string `json:"catalog_version,omitempty"`
	ProductID         string `json:"product_id,omitempty"`
	UpdateID          string `json:"update_id,omitempty"`
	TargetVerified    bool   `json:"target_verified"`
	Diagnostics       string `json:"diagnostics,omitempty"`
}

type StoreDiagnosticsAssessment struct {
	PackageFamilyName          string `json:"package_family_name"`
	State                      string `json:"state"`
	Reason                     string `json:"reason,omitempty"`
	InstalledVersion           string `json:"installed_version,omitempty"`
	AvailableVersion           string `json:"available_version,omitempty"`
	Stale                      bool   `json:"stale"`
	ProductID                  string `json:"product_id,omitempty"`
	UpdateID                   string `json:"update_id,omitempty"`
	ExactActionTargetAvailable bool   `json:"exact_action_target_available"`
	Applicability              string `json:"applicability,omitempty"`
	ObservedAt                 string `json:"observed_at"`
}

func buildStoreDiagnosticsExport(ctx context.Context, state State) ([]byte, error) {
	export := StoreDiagnosticsExport{
		GeneratedAt:         formatAssessmentTime(time.Now().UTC()),
		SchemaVersion:       storeScanSchemaVersion,
		DetectorMode:        string(StoreScanModeOptimized),
		AutoUpdateMigration: sanitizeStoreAutoUpdateMigration(state.StoreAutoUpdateMigration),
	}
	userSID, sidErr := currentUserSID()
	if sidErr != nil {
		export.Errors = append(export.Errors, sanitizeProviderDiagnostic(sidErr.Error()))
	} else {
		export.UserScopeHash = hashSensitiveID(userSID)
	}
	repository, err := openDefaultStoreScanRepository()
	if err != nil {
		export.Errors = append(export.Errors, sanitizeProviderDiagnostic(err.Error()))
		return json.MarshalIndent(export, "", "  ")
	}
	defer repository.Close()
	if userSID == "" {
		return json.MarshalIndent(export, "", "  ")
	}
	snapshot, ok, err := repository.LoadLatestPublishedSnapshot(ctx, userSID)
	if err != nil {
		return nil, err
	}
	if !ok {
		export.Errors = append(export.Errors, "no published Store scan is available")
		return json.MarshalIndent(export, "", "  ")
	}
	applyStoreDiagnosticsSnapshot(&export, snapshot)
	return json.MarshalIndent(export, "", "  ")
}

func applyStoreDiagnosticsSnapshot(export *StoreDiagnosticsExport, snapshot StoreScanSnapshot) {
	if export == nil {
		return
	}
	scan := snapshot.Scan
	export.DetectorMode = firstNonEmpty(string(scan.Mode), string(StoreScanModeOptimized))
	export.Scan = StoreDiagnosticsScan{
		ScanID:         scan.ScanID,
		Mode:           string(scan.Mode),
		StartedAt:      formatStoreScanTime(scan.StartedAt),
		CompletedAt:    formatStoreScanTime(scan.CompletedAt),
		WindowsVersion: scan.WindowsVersion,
		WindowsBuild:   scan.WindowsBuild,
		Architecture:   scan.Architecture,
		Status:         string(scan.CompletionStatus),
		Metrics:        scan.Metrics,
	}
	export.Providers = providerSummariesFromRuns(snapshot.ProviderRuns)
	for _, family := range snapshot.Inventory.Families {
		export.Packages = append(export.Packages, StoreDiagnosticsPackage{
			PackageFamilyName: family.Identity.PackageFamilyName,
			DisplayName:       family.DisplayName,
			ProductLike:       family.ProductLike,
		})
	}
	for _, run := range snapshot.ProviderRuns {
		for _, observation := range run.Observations {
			productID, updateID := "", ""
			targetVerified := false
			if observation.Target != nil {
				productID = observation.Target.ProductID
				updateID = observation.Target.UpdateID
				targetVerified = observation.Target.ExactFor(observation.Identity)
			}
			export.Observations = append(export.Observations, StoreDiagnosticsObservation{
				Provider:          observation.Provider.Key(),
				PackageFamilyName: observation.Identity.PackageFamilyName,
				Kind:              string(observation.Kind),
				Health:            string(observation.Health),
				ObservedAt:        formatStoreScanTime(observation.ObservedAt),
				InstalledVersion:  observation.InstalledVersion,
				AvailableVersion:  observation.AvailableVersion,
				CatalogVersion:    observation.CatalogVersion,
				ProductID:         productID,
				UpdateID:          updateID,
				TargetVerified:    targetVerified,
				Diagnostics:       sanitizeProviderDiagnostic(observation.Diagnostics),
			})
		}
	}
	for _, assessment := range snapshot.Assessments {
		export.Assessments = append(export.Assessments, StoreDiagnosticsAssessment{
			PackageFamilyName:          assessment.Identity.PackageFamilyName,
			State:                      string(assessment.State),
			Reason:                     sanitizeProviderDiagnostic(assessment.Reason),
			InstalledVersion:           assessment.InstalledVersion,
			AvailableVersion:           assessment.AvailableVersion,
			Stale:                      assessment.Stale,
			ProductID:                  assessment.StoreProductID,
			UpdateID:                   assessment.UpdateID,
			ExactActionTargetAvailable: assessment.ExactActionTargetAvailable,
			Applicability:              assessment.Applicability,
			ObservedAt:                 formatStoreScanTime(assessment.ObservedAt),
		})
	}
}

func sanitizeStoreAutoUpdateMigration(report StoreAutoUpdateMigrationReport) StoreAutoUpdateMigrationReport {
	report.Migrated = sanitizeStoreAutoUpdateMigrationEntries(report.Migrated)
	report.Disabled = sanitizeStoreAutoUpdateMigrationEntries(report.Disabled)
	return report
}

func sanitizeStoreAutoUpdateMigrationEntries(entries []StoreAutoUpdateMigrationEntry) []StoreAutoUpdateMigrationEntry {
	sanitized := make([]StoreAutoUpdateMigrationEntry, 0, len(entries))
	for _, entry := range entries {
		if userSID, pfn, ok := splitCanonicalStoreAutoUpdateKey(entry.CanonicalKey); ok {
			entry.CanonicalKey = packageKey(managerStore, hashSensitiveID(userSID)+storeAutoUpdateKeySeparator+strings.ToLower(pfn))
		}
		entry.LegacyKey = sanitizeLegacyStorePreferenceKey(entry.LegacyKey)
		sanitized = append(sanitized, entry)
	}
	return sanitized
}

func sanitizeLegacyStorePreferenceKey(key string) string {
	manager, id, err := splitPackageKey(key)
	if err != nil || manager != managerStore {
		return key
	}
	if userSID, pfn, ok := splitCanonicalStoreAutoUpdateKey(key); ok {
		return packageKey(managerStore, hashSensitiveID(userSID)+storeAutoUpdateKeySeparator+strings.ToLower(pfn))
	}
	return packageKey(managerStore, id)
}

func hashSensitiveID(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:16]
}
