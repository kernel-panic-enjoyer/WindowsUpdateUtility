package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWinRTDiscoveryExactPFNPositiveBecomesActionable(t *testing.T) {
	userSID := "S-1-5-21-winrt-discovery"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-positive", userSID, StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	families := testStoreInventory(scan, pfn, "1.0.0.0").Families

	provider := storeWinRTDiscoveryCatalogProvider{
		Discover: func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
			return storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          scan.ScanID,
				UserSID:         userSID,
				Completed:       true,
				Items: []storeUpdateDiscoveryItem{{
					PackageFamilyName: pfn,
					ProductID:         "9WZDNCRFHVN5",
					OfferAvailable:    true,
					InstallState:      storeInstallStateReadyToDownload,
				}},
			}, CommandResult{OK: true, Command: "fake winrt discovery"}
		},
		Now: func() time.Time { return scan.CompletedAt },
	}

	run := provider.Observe(context.Background(), scan, families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 {
		t.Fatalf("unexpected run: %#v", run)
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:     StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn},
		Scan:         scan,
		Observations: run.Observations,
	})
	if assessment.State != StoreUpdateAvailable || assessment.Target == nil || assessment.Target.ProductID != "9WZDNCRFHVN5" || !assessment.Target.ExactFor(assessment.Identity) {
		t.Fatalf("WinRT exact positive was not actionable: %#v", assessment)
	}
}

func TestWinRTDiscoveryPartialPositiveIsDiagnosticOnly(t *testing.T) {
	userSID := "S-1-5-21-winrt-partial"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-partial", userSID, StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	families := testStoreInventory(scan, pfn, "1.0.0.0").Families

	provider := storeWinRTDiscoveryCatalogProvider{
		Discover: func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
			return storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          scan.ScanID,
				UserSID:         userSID,
				Completed:       true,
				Partial:         true,
				Errors:          []string{"worker returned partial Store discovery evidence"},
				Items: []storeUpdateDiscoveryItem{{
					PackageFamilyName: pfn,
					ProductID:         "9WZDNCRFHVN5",
					OfferAvailable:    true,
					InstallState:      storeInstallStateReadyToDownload,
				}},
			}, CommandResult{OK: true, Command: "fake partial winrt discovery"}
		},
		Now: func() time.Time { return scan.CompletedAt },
	}

	run := provider.Observe(context.Background(), scan, families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 0 || len(run.Mappings) != 0 {
		t.Fatalf("partial WinRT discovery must stay diagnostic-only, got %#v", run)
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:     StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn},
		Scan:         scan,
		Observations: run.Observations,
	})
	if assessment.State == StoreUpdateAvailable || assessment.Target != nil {
		t.Fatalf("partial WinRT positive became actionable: %#v", assessment)
	}
}

func TestWinRTDiscoveryPositiveSupersedesStoreCLIAggregateNegative(t *testing.T) {
	userSID := "S-1-5-21-winrt-negative-guard"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-negative-guard", userSID,
		StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID},
		StoreProviderIdentity{ID: storeCLIUpdatesProviderID},
	)
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	winrtProvider := StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID, Name: "WinRT Store update discovery", Backend: backendWinRT}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	observations := []StoreProviderObservation{
		{
			Provider:         winrtProvider,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationPositiveUpdateOffer,
			Identity:         identity,
			ScanID:           scan.ScanID,
			ObservedAt:       scan.CompletedAt,
			InstalledVersion: "1.0.0.0",
			Target: &ExactStoreUpdateTarget{
				Identity:   identity,
				Provider:   winrtProvider,
				ProductID:  "9WZDNCRFHVN5",
				UpdateID:   pfn,
				Verified:   true,
				VerifiedBy: storeWinRTDiscoveryProviderID,
				VerifiedAt: scan.CompletedAt,
			},
		},
		{
			Provider:         aggregateProvider,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationAuthoritativeNegative,
			Identity:         identity,
			ScanID:           scan.ScanID,
			ObservedAt:       scan.CompletedAt,
			InstalledVersion: "1.0.0.0",
		},
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          identity,
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{aggregateProvider},
		Observations:      observations,
	})
	if assessment.State != StoreUpdateAvailable || assessment.Target == nil {
		t.Fatalf("WinRT exact positive should supersede Store CLI aggregate negative, got %#v", assessment)
	}
}

func TestWinRTDiscoveryQueuedUpdateSupersedesStoreCLIExactNegative(t *testing.T) {
	userSID := "S-1-5-21-winrt-exact-negative-guard"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-exact-negative-guard", userSID,
		StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID},
		StoreProviderIdentity{ID: storeCLIExactProviderID},
	)
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	winrtProvider := StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID, Name: "WinRT Store update discovery", Backend: backendWinRT}
	storeCLIProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	observations := []StoreProviderObservation{
		{
			Provider:         winrtProvider,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationPositiveUpdateOffer,
			Identity:         identity,
			ScanID:           scan.ScanID,
			ObservedAt:       scan.CompletedAt,
			InstalledVersion: "1.0.0.0",
			Target: &ExactStoreUpdateTarget{
				Identity:   identity,
				Provider:   winrtProvider,
				ProductID:  "9WZDNCRFHVN5",
				UpdateID:   pfn,
				Verified:   true,
				VerifiedBy: storeWinRTDiscoveryProviderID,
				VerifiedAt: scan.CompletedAt,
			},
		},
		{
			Provider:         storeCLIProvider,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationAuthoritativeNegative,
			Identity:         identity,
			ScanID:           scan.ScanID,
			ObservedAt:       scan.CompletedAt,
			InstalledVersion: "1.0.0.0",
		},
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:     identity,
		Scan:         scan,
		Observations: observations,
	})
	if assessment.State != StoreUpdateAvailable || assessment.Target == nil {
		t.Fatalf("WinRT queued exact positive should supersede Store CLI exact false negative, got %#v", assessment)
	}
}

func TestWinRTDiscoveryQueueOnlyPendingIsDiagnosticOnly(t *testing.T) {
	userSID := "S-1-5-21-winrt-pending"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-pending", userSID, StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	families := testStoreInventory(scan, pfn, "1.0.0.0").Families
	provider := storeWinRTDiscoveryCatalogProvider{
		Discover: func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
			return storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          scan.ScanID,
				UserSID:         userSID,
				Completed:       true,
				Items: []storeUpdateDiscoveryItem{{
					PackageFamilyName: pfn,
					ProductID:         "9WZDNCRFHVN5",
					OfferAvailable:    false,
					InstallState:      storeInstallStatePaused,
				}},
			}, CommandResult{OK: true, Command: "fake winrt queue"}
		},
		Now: func() time.Time { return scan.CompletedAt },
	}
	run := provider.Observe(context.Background(), scan, families)
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:     StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn},
		Scan:         scan,
		Observations: run.Observations,
	})
	if assessment.State != StoreUpdatePending || assessment.Target != nil {
		t.Fatalf("queue-only pending evidence must stay non-actionable: %#v", assessment)
	}
}

func TestWinRTDiscoveryQueuedUpdateExactPFNBecomesActionable(t *testing.T) {
	userSID := "S-1-5-21-winrt-queued-update"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-queued-update", userSID, StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	families := testStoreInventory(scan, pfn, "1.0.0.0").Families
	provider := storeWinRTDiscoveryCatalogProvider{
		Discover: func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
			return storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          scan.ScanID,
				UserSID:         userSID,
				Completed:       true,
				Items: []storeUpdateDiscoveryItem{{
					PackageFamilyName: pfn,
					ProductID:         "9WZDNCRFHVN5",
					OfferAvailable:    true,
					InstallType:       storeInstallTypeUpdate,
					InstallTypeCode:   1,
					InstallState:      storeInstallStatePaused,
				}},
			}, CommandResult{OK: true, Command: "fake winrt queued update"}
		},
		Now: func() time.Time { return scan.CompletedAt },
	}
	run := provider.Observe(context.Background(), scan, families)
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:     StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn},
		Scan:         scan,
		Observations: run.Observations,
	})
	if assessment.State != StoreUpdateAvailable || assessment.Target == nil || !assessment.Target.ExactFor(assessment.Identity) {
		t.Fatalf("queued exact WinRT update evidence should be actionable: %#v", assessment)
	}
}

func TestWinRTDiscoveryInvalidWorkerResponseIsNotPartiallyTrusted(t *testing.T) {
	userSID := "S-1-5-21-winrt-invalid"
	pfn := "Microsoft.WindowsCalculator_8wekyb3d8bbwe"
	scan := completedStoreScan("scan-winrt-invalid", userSID, StoreProviderIdentity{ID: storeWinRTDiscoveryProviderID})
	families := testStoreInventory(scan, pfn, "1.0.0.0").Families
	provider := storeWinRTDiscoveryCatalogProvider{
		Discover: func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) (storeUpdateDiscoveryWorkerResponse, CommandResult) {
			return storeUpdateDiscoveryWorkerResponse{
				ProtocolVersion: storeUpdateDiscoveryWorkerProtocolVersion,
				ScanID:          scan.ScanID,
				UserSID:         userSID,
				Completed:       true,
				Items: []storeUpdateDiscoveryItem{
					{PackageFamilyName: pfn, ProductID: "9WZDNCRFHVN5", OfferAvailable: true},
					{PackageFamilyName: pfn, ProductID: "9WZDNCRFHVN5", OfferAvailable: true},
				},
			}, CommandResult{OK: true, Command: "fake winrt duplicate"}
		},
		Now: func() time.Time { return scan.CompletedAt },
	}
	run := provider.Observe(context.Background(), scan, families)
	if run.Health == StoreProviderHealthy || len(run.Observations) != 0 || !strings.Contains(run.Error, "duplicate") {
		t.Fatalf("invalid response should be rejected without observations: %#v", run)
	}
}
