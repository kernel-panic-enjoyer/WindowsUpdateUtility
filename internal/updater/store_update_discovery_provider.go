package updater

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	storeWinRTDiscoveryProviderID = "store-winrt-discovery"

	storeUpdateDiscoveryWorkerFlag            = "--store-update-discovery-worker"
	storeUpdateDiscoveryWorkerProtocolVersion = 1
	storeUpdateDiscoveryWorkerRequestLimit    = 64 * 1024
	storeUpdateDiscoveryWorkerResponseLimit   = 2 * 1024 * 1024
	storeUpdateDiscoveryWorkerMaxCandidates   = 12000
	storeUpdateDiscoveryWorkerMaxItems        = 12000
	storeUpdateDiscoveryWorkerMaxErrors       = 32
	storeUpdateDiscoveryWorkerMaxErrorBytes   = 1024
	storeUpdateDiscoveryWorkerMaxStringBytes  = 4096

	storeInstallStatePending            = "pending"
	storeInstallStateStarting           = "starting"
	storeInstallStateAcquiringLicense   = "acquiring_license"
	storeInstallStateDownloading        = "downloading"
	storeInstallStateRestoringData      = "restoring_data"
	storeInstallStateInstalling         = "installing"
	storeInstallStateCompleted          = "completed"
	storeInstallStateCanceled           = "canceled"
	storeInstallStatePaused             = "paused"
	storeInstallStateError              = "error"
	storeInstallStatePausedLowBattery   = "paused_low_battery"
	storeInstallStatePausedWiFiRequired = "paused_wifi_required"
	storeInstallStateReadyToDownload    = "ready_to_download"

	storeInstallTypeInstall = "install"
	storeInstallTypeUpdate  = "update"
	storeInstallTypeRepair  = "repair"
)

type storeWinRTDiscoveryCatalogProvider struct {
	Discover func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult)
	Now      func() time.Time
	Version  string
}

type storeUpdateDiscoveryCandidate struct {
	PackageFamilyName string `json:"package_family_name"`
	ProductID         string `json:"product_id,omitempty"`
}

type storeUpdateDiscoveryWorkerRequest struct {
	ProtocolVersion int                             `json:"protocol_version"`
	ScanID          string                          `json:"scan_id"`
	UserSID         string                          `json:"user_sid"`
	Deadline        time.Time                       `json:"deadline"`
	Candidates      []storeUpdateDiscoveryCandidate `json:"candidates,omitempty"`
}

type storeUpdateDiscoveryWorkerResponse struct {
	ProtocolVersion int                        `json:"protocol_version"`
	ScanID          string                     `json:"scan_id"`
	UserSID         string                     `json:"user_sid"`
	Completed       bool                       `json:"completed"`
	Partial         bool                       `json:"partial"`
	Items           []storeUpdateDiscoveryItem `json:"items,omitempty"`
	Errors          []string                   `json:"errors,omitempty"`
}

type storeUpdateDiscoveryItem struct {
	PackageFamilyName  string  `json:"package_family_name"`
	ProductID          string  `json:"product_id,omitempty"`
	PackageFullName    string  `json:"package_full_name,omitempty"`
	InstalledVersion   string  `json:"installed_version,omitempty"`
	AvailableVersion   string  `json:"available_version,omitempty"`
	InstallState       string  `json:"install_state,omitempty"`
	InstallStateCode   int     `json:"install_state_code,omitempty"`
	InstallType        string  `json:"install_type,omitempty"`
	InstallTypeCode    int     `json:"install_type_code,omitempty"`
	PercentComplete    float64 `json:"percent_complete,omitempty"`
	DownloadSizeBytes  uint64  `json:"download_size_bytes,omitempty"`
	BytesDownloaded    uint64  `json:"bytes_downloaded,omitempty"`
	ErrorCode          string  `json:"error_code,omitempty"`
	Diagnostic         string  `json:"diagnostic,omitempty"`
	OfferAvailable     bool    `json:"offer_available"`
	QueueStatusOnly    bool    `json:"queue_status_only,omitempty"`
	ItemOperationsRisk bool    `json:"item_operations_might_affect_other_items,omitempty"`
}

func (provider storeWinRTDiscoveryCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID, Name: "WinRT Store update discovery", Backend: backendWinRT}
}

func (provider storeWinRTDiscoveryCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	started := provider.now()
	identity := provider.Identity()
	run := StoreCatalogProviderRun{
		Provider:    identity,
		Version:     provider.Version,
		StartedAt:   started,
		CompletedAt: started,
		Health:      StoreProviderHealthy,
	}
	productFamilies := productLikeFamiliesByPFN(scan, families)
	if len(productFamilies) == 0 {
		run.CompletedAt = provider.now()
		return run
	}
	discover := provider.Discover
	if discover == nil {
		discover = storeUpdateDiscoveryWorkerProvider{}.Discover
	}
	response, result := discover(ctx, scan, families)
	run.CompletedAt = provider.now()
	if ctx.Err() != nil {
		run.Health = StoreProviderIncomplete
		run.Error = ctx.Err().Error()
		return run
	}
	items, validationErr := validateStoreUpdateDiscoveryWorkerResponse(scan, response, productFamilies)
	if validationErr != nil {
		run.Health = storeWinRTDiscoveryHealthForError(validationErr)
		run.Error = sanitizeProviderDiagnostic(validationErr.Error())
		return run
	}
	if !result.OK || response.Partial || !response.Completed || len(response.Errors) > 0 {
		run.Health = StoreProviderIncomplete
		run.Error = sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, strings.Join(response.Errors, "; "), "WinRT Store update discovery returned incomplete results"))
		return run
	}
	observedAt := run.CompletedAt
	if observedAt.IsZero() {
		observedAt = scan.StartedAt
	}
	for _, item := range items {
		family := productFamilies[strings.ToLower(item.PackageFamilyName)]
		observation := StoreProviderObservation{
			Provider:         identity,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationPendingUpdate,
			Identity:         family.Identity,
			ScanID:           scan.ScanID,
			ObservedAt:       observedAt,
			InstalledVersion: firstNonEmpty(item.InstalledVersion, family.Primary.Version.String()),
			AvailableVersion: item.AvailableVersion,
			CatalogVersion:   item.AvailableVersion,
			Diagnostics:      storeWinRTDiscoveryDiagnostics(item),
		}
		if item.OfferAvailable && !item.QueueStatusOnly {
			observation.Kind = StoreObservationPositiveUpdateOffer
			observation.Target = &ExactStoreUpdateTarget{
				Identity:   family.Identity,
				Provider:   identity,
				ProductID:  strings.TrimSpace(item.ProductID),
				UpdateID:   family.Identity.PackageFamilyName,
				Verified:   true,
				VerifiedBy: identity.Key(),
				VerifiedAt: observedAt,
			}
			if strings.TrimSpace(item.ProductID) != "" {
				run.Mappings = append(run.Mappings, VerifiedStoreIdentityMapping{
					InstalledIdentity:     family.Identity,
					ProductID:             strings.TrimSpace(item.ProductID),
					Provider:              identity,
					ScanID:                scan.ScanID,
					VerifiedAt:            observedAt,
					Evidence:              "WinRT AppInstallManager returned matching PFN update evidence.",
					IdentityName:          family.Primary.IdentityName,
					PublisherID:           family.Primary.PublisherID,
					ProcessorArchitecture: family.Primary.ProcessorArchitecture,
					ProductLike:           family.ProductLike,
					ProviderVersion:       provider.Version,
				})
			}
		}
		run.Observations = append(run.Observations, observation)
	}
	sortStoreCatalogRunEvidence(run.Observations, run.Mappings)
	return run
}

func (provider storeWinRTDiscoveryCatalogProvider) now() time.Time {
	if provider.Now != nil {
		return provider.Now().UTC()
	}
	return time.Now().UTC()
}

func storeWinRTDiscoveryHealthForError(err error) StoreProviderHealth {
	if err == nil {
		return StoreProviderHealthy
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unsupported") ||
		strings.Contains(message, "not registered") ||
		strings.Contains(message, "0x80040154") ||
		strings.Contains(message, "0x80004002") ||
		strings.Contains(message, "0x80070005") ||
		strings.Contains(message, "access is denied") {
		return StoreProviderUnsupported
	}
	return StoreProviderIncomplete
}

func storeWinRTDiscoveryDiagnostics(item storeUpdateDiscoveryItem) string {
	var parts []string
	if item.OfferAvailable && !item.QueueStatusOnly {
		parts = append(parts, "WinRT Store discovery found an exact update offer.")
	} else {
		parts = append(parts, "WinRT Store queue reported a pending package state.")
	}
	if item.InstallState != "" {
		parts = append(parts, "state="+item.InstallState)
	}
	if item.InstallType != "" {
		parts = append(parts, "type="+item.InstallType)
	}
	if item.PercentComplete > 0 {
		parts = append(parts, fmt.Sprintf("progress=%.0f%%", item.PercentComplete))
	}
	if item.Diagnostic != "" {
		parts = append(parts, item.Diagnostic)
	}
	if item.ErrorCode != "" {
		parts = append(parts, "error="+item.ErrorCode)
	}
	return sanitizeProviderDiagnostic(strings.Join(parts, " "))
}

func validateStoreUpdateDiscoveryWorkerResponse(scan StoreScanGeneration, response storeUpdateDiscoveryWorkerResponse, families map[string]StorePackagedAppFamily) ([]storeUpdateDiscoveryItem, error) {
	if response.ProtocolVersion != storeUpdateDiscoveryWorkerProtocolVersion {
		return nil, fmt.Errorf("unsupported Store update discovery worker protocol version %d", response.ProtocolVersion)
	}
	if response.ScanID != scan.ScanID {
		return nil, fmt.Errorf("Store update discovery worker scan ID mismatch: got %q, want %q", response.ScanID, scan.ScanID)
	}
	if !strings.EqualFold(strings.TrimSpace(response.UserSID), strings.TrimSpace(scan.UserSID)) {
		return nil, fmt.Errorf("Store update discovery worker user SID mismatch: got %q, want %q", response.UserSID, scan.UserSID)
	}
	if len(response.Items) > storeUpdateDiscoveryWorkerMaxItems {
		return nil, fmt.Errorf("Store update discovery worker returned %d records; limit is %d", len(response.Items), storeUpdateDiscoveryWorkerMaxItems)
	}
	if len(response.Errors) > storeUpdateDiscoveryWorkerMaxErrors {
		return nil, fmt.Errorf("Store update discovery worker returned %d diagnostics; limit is %d", len(response.Errors), storeUpdateDiscoveryWorkerMaxErrors)
	}
	for _, item := range response.Errors {
		if len(item) > storeUpdateDiscoveryWorkerMaxErrorBytes {
			return nil, errors.New("Store update discovery worker diagnostic exceeds limit")
		}
	}
	seen := map[string]bool{}
	validated := make([]storeUpdateDiscoveryItem, 0, len(response.Items))
	for _, item := range response.Items {
		normalized, err := normalizeStoreUpdateDiscoveryItem(item)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(normalized.PackageFamilyName)
		if seen[key] {
			return nil, fmt.Errorf("Store update discovery worker returned duplicate package family %q", normalized.PackageFamilyName)
		}
		seen[key] = true
		if families != nil {
			if _, ok := families[key]; !ok {
				continue
			}
		}
		validated = append(validated, normalized)
	}
	return validated, nil
}

func normalizeStoreUpdateDiscoveryItem(item storeUpdateDiscoveryItem) (storeUpdateDiscoveryItem, error) {
	item.PackageFamilyName = strings.TrimSpace(item.PackageFamilyName)
	item.ProductID = strings.TrimSpace(item.ProductID)
	item.PackageFullName = strings.TrimSpace(item.PackageFullName)
	item.InstalledVersion = strings.TrimSpace(item.InstalledVersion)
	item.AvailableVersion = strings.TrimSpace(item.AvailableVersion)
	item.InstallState = strings.TrimSpace(item.InstallState)
	item.InstallType = strings.TrimSpace(item.InstallType)
	item.ErrorCode = strings.TrimSpace(item.ErrorCode)
	item.Diagnostic = sanitizeProviderDiagnostic(item.Diagnostic)
	if !validStorePackageFamilyName(item.PackageFamilyName) {
		return item, fmt.Errorf("Store update discovery worker returned malformed package family name %q", item.PackageFamilyName)
	}
	if item.ProductID != "" && !looksLikeStoreProductID(item.ProductID) {
		return item, fmt.Errorf("Store update discovery worker returned malformed Product ID %q", item.ProductID)
	}
	if item.PackageFullName != "" && !validStorePackageFullName(item.PackageFullName) {
		return item, fmt.Errorf("Store update discovery worker returned malformed package full name %q", item.PackageFullName)
	}
	if !validStoreInventoryString(item.InstalledVersion) ||
		!validStoreInventoryString(item.AvailableVersion) ||
		!validStoreInventoryString(item.InstallState) ||
		!validStoreInventoryString(item.InstallType) ||
		!validStoreInventoryString(item.ErrorCode) ||
		!validStoreInventoryString(item.Diagnostic) {
		return item, errors.New("Store update discovery worker returned a malformed string field")
	}
	if item.PercentComplete < 0 || item.PercentComplete > 100 {
		return item, fmt.Errorf("Store update discovery worker returned invalid progress %.2f", item.PercentComplete)
	}
	return item, nil
}

func storeUpdateDiscoveryCandidates(scan StoreScanGeneration, families []StorePackagedAppFamily) []storeUpdateDiscoveryCandidate {
	candidates := make([]storeUpdateDiscoveryCandidate, 0, len(families))
	for _, family := range families {
		if !family.ProductLike || !family.Identity.Resolved() || family.Identity.UserSID != scan.UserSID {
			continue
		}
		candidates = append(candidates, storeUpdateDiscoveryCandidate{PackageFamilyName: family.Identity.PackageFamilyName})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].PackageFamilyName) < strings.ToLower(candidates[j].PackageFamilyName)
	})
	return candidates
}
