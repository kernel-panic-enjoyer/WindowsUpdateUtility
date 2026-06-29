package updater

import (
	"strings"
	"testing"
	"time"
)

func TestReconcileStoreUpdate(t *testing.T) {
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-test-1001", PackageFamilyName: "OpenAI.Codex_123abc"}
	otherIdentity := StoreInstalledIdentity{UserSID: "S-1-5-21-test-1002", PackageFamilyName: identity.PackageFamilyName}
	storeProvider := StoreProviderIdentity{ID: "store-winrt", Name: "Store WinRT", Backend: "winrt"}
	wingetProvider := StoreProviderIdentity{ID: "winget-msstore", Name: "WinGet msstore", Backend: "winget"}
	scan := completedStoreScan("scan-1", identity.UserSID, storeProvider, wingetProvider)
	olderScan := completedStoreScan("scan-0", identity.UserSID, storeProvider)
	target := exactStoreTarget(identity, storeProvider)

	tests := []struct {
		name              string
		input             StoreReconciliationInput
		wantState         StoreUpdateState
		wantRejectedCount int
	}{
		{
			name: "fresh exact positive evidence is available",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
				},
			},
			wantState: StoreUpdateAvailable,
		},
		{
			name: "healthy providers disagree is conflict",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider, wingetProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
					storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateConflict,
		},
		{
			name: "required provider failure is unknown",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderFailed, StoreObservationProviderFailure, "", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "required provider failure does not erase fresh exact positive",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderFailed, StoreObservationProviderFailure, "", "", nil),
					storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", exactStoreTarget(identity, wingetProvider)),
				},
			},
			wantState: StoreUpdateAvailable,
		},
		{
			name: "incomplete scan keeps fresh exact positive evidence available",
			input: StoreReconciliationInput{
				Identity: identity,
				Scan: StoreScanGeneration{
					ScanID:           "scan-1",
					UserSID:          identity.UserSID,
					StartedAt:        time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC),
					CompletionStatus: StoreScanIncomplete,
				},
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
				},
			},
			wantState: StoreUpdateAvailable,
		},
		{
			name: "incomplete scan cannot become current from negative evidence",
			input: StoreReconciliationInput{
				Identity: identity,
				Scan: StoreScanGeneration{
					ScanID:           "scan-1",
					UserSID:          identity.UserSID,
					StartedAt:        time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC),
					CompletionStatus: StoreScanIncomplete,
				},
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "newer catalog version without applicable installer is inapplicable",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationNewerCatalogNoApplicableInstaller, "1.0.0", "1.1.0", nil),
				},
			},
			wantState: StoreUpdateInapplicable,
		},
		{
			name: "all required providers authoritative negative is current",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider, wingetProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
					storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateCurrent,
		},
		{
			name: "evidence from another user is rejected",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(otherIdentity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", exactStoreTarget(otherIdentity, storeProvider)),
				},
			},
			wantState:         StoreUpdateUnknown,
			wantRejectedCount: 1,
		},
		{
			name: "evidence from another scan generation is rejected",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, olderScan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
				},
			},
			wantState:         StoreUpdateUnknown,
			wantRejectedCount: 1,
		},
		{
			name: "stale negative does not clear fresh positive",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
					storeObservation(identity, olderScan, wingetProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState:         StoreUpdateAvailable,
			wantRejectedCount: 1,
		},
		{
			name: "unresolved identity cannot become current",
			input: StoreReconciliationInput{
				Identity:          StoreInstalledIdentity{UserSID: identity.UserSID},
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(StoreInstalledIdentity{UserSID: identity.UserSID}, scan, storeProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "empty provider output is not authoritative negative",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationEmptyResult, "", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "positive offer without exact target is unknown",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "pending update remains pending until verified",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPendingUpdate, "1.0.0", "1.1.0", target),
				},
			},
			wantState: StoreUpdatePending,
		},
		{
			name: "missing required provider is unknown",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider, wingetProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "stale required provider result is unknown",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderStale, StoreObservationStaleResult, "1.0.0", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
		{
			name: "provider failure blocks current even when not required",
			input: StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
					storeObservation(identity, scan, wingetProvider, StoreProviderFailed, StoreObservationProviderFailure, "", "", nil),
				},
			},
			wantState: StoreUpdateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileStoreUpdate(tt.input)
			if got.State != tt.wantState {
				t.Fatalf("state = %q, want %q; assessment=%#v", got.State, tt.wantState, got)
			}
			if got.RejectedEvidenceCount != tt.wantRejectedCount {
				t.Fatalf("rejected evidence count = %d, want %d", got.RejectedEvidenceCount, tt.wantRejectedCount)
			}
		})
	}
}

func TestReconcileStoreUpdatePositiveTargetConsensus(t *testing.T) {
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-consensus", PackageFamilyName: "OpenAI.Codex_123abc"}
	storeProvider := StoreProviderIdentity{ID: "store-cli-exact", Name: "Store CLI", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: "winget-msstore-exact", Name: "WinGet msstore", Backend: backendWingetMSStoreFallback}
	scan := completedStoreScan("scan-consensus", identity.UserSID, storeProvider, wingetProvider)
	productTarget := exactStoreTarget(identity, storeProvider)
	productTarget.ProductID = "9NCODEX"
	productTarget.UpdateID = ""
	updateIDTarget := exactStoreTarget(identity, wingetProvider)
	updateIDTarget.ProductID = ""
	updateIDTarget.UpdateID = identity.PackageFamilyName
	targetProductA := exactStoreTarget(identity, storeProvider)
	targetProductA.ProductID = "9NAAAAA"
	targetProductA.UpdateID = ""
	targetProductB := exactStoreTarget(identity, wingetProvider)
	targetProductB.ProductID = "9NBBBBB"
	targetProductB.UpdateID = ""
	targetUpdateA := exactStoreTarget(identity, storeProvider)
	targetUpdateA.ProductID = ""
	targetUpdateA.UpdateID = "provider-a-target"
	targetUpdateB := exactStoreTarget(identity, wingetProvider)
	targetUpdateB.ProductID = ""
	targetUpdateB.UpdateID = "provider-b-target"

	tests := []struct {
		name       string
		targets    []*ExactStoreUpdateTarget
		versions   []string
		wantState  StoreUpdateState
		wantKind   string
		wantReason string
	}{
		{
			name:      "same Product ID from two providers",
			targets:   []*ExactStoreUpdateTarget{productTarget, cloneStoreTarget(productTarget, wingetProvider)},
			versions:  []string{"1.1.0", "1.1.0"},
			wantState: StoreUpdateAvailable,
			wantKind:  "product_id",
		},
		{
			name:      "Product ID plus PFN update ID is complementary",
			targets:   []*ExactStoreUpdateTarget{productTarget, updateIDTarget},
			versions:  []string{"1.1.0", "1.1.0"},
			wantState: StoreUpdateAvailable,
			wantKind:  "product_and_update_id",
		},
		{
			name:       "differing Product IDs conflict",
			targets:    []*ExactStoreUpdateTarget{targetProductA, targetProductB},
			versions:   []string{"1.1.0", "1.1.0"},
			wantState:  StoreUpdateConflict,
			wantReason: "product_id",
		},
		{
			name:       "differing provider update IDs conflict",
			targets:    []*ExactStoreUpdateTarget{targetUpdateA, targetUpdateB},
			versions:   []string{"1.1.0", "1.1.0"},
			wantState:  StoreUpdateConflict,
			wantReason: "update_id",
		},
		{
			name:       "differing known offered versions conflict",
			targets:    []*ExactStoreUpdateTarget{productTarget, cloneStoreTarget(productTarget, wingetProvider)},
			versions:   []string{"1.1.0", "2.0.0"},
			wantState:  StoreUpdateConflict,
			wantReason: "offered_version",
		},
		{
			name:      "known and unknown offered versions are compatible",
			targets:   []*ExactStoreUpdateTarget{productTarget, cloneStoreTarget(productTarget, wingetProvider)},
			versions:  []string{"1.1.0", ""},
			wantState: StoreUpdateAvailable,
			wantKind:  "product_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observations := make([]StoreProviderObservation, 0, len(tt.targets))
			providers := []StoreProviderIdentity{storeProvider, wingetProvider}
			for index, target := range tt.targets {
				provider := providers[index%len(providers)]
				observations = append(observations, storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", tt.versions[index], target))
			}
			got := ReconcileStoreUpdate(StoreReconciliationInput{
				Identity:          identity,
				Scan:              scan,
				RequiredProviders: []StoreProviderIdentity{storeProvider},
				Observations:      observations,
			})
			if got.State != tt.wantState {
				t.Fatalf("state=%s want %s assessment=%#v", got.State, tt.wantState, got)
			}
			if tt.wantState == StoreUpdateConflict {
				if got.Target != nil || !containsFold(got.Reason, tt.wantReason) {
					t.Fatalf("conflict should clear target and cite %q: %#v", tt.wantReason, got)
				}
				return
			}
			if got.Target == nil || exactTargetKind(got.Target.ProductID, got.Target.UpdateID) != tt.wantKind {
				t.Fatalf("canonical target kind=%q want %q target=%#v", exactTargetKind(got.Target.ProductID, got.Target.UpdateID), tt.wantKind, got.Target)
			}
		})
	}
}

func TestReconcileStoreUpdatePositiveWithAndWithoutTargetKeepsExactTarget(t *testing.T) {
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-consensus", PackageFamilyName: "OpenAI.Codex_123abc"}
	storeProvider := StoreProviderIdentity{ID: "store-cli-exact", Name: "Store CLI", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: "winget-msstore-exact", Name: "WinGet msstore", Backend: backendWingetMSStoreFallback}
	scan := completedStoreScan("scan-no-target", identity.UserSID, storeProvider, wingetProvider)
	target := exactStoreTarget(identity, storeProvider)
	target.ProductID = "9NCODEX"
	target.UpdateID = ""
	got := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          identity,
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{storeProvider},
		Observations: []StoreProviderObservation{
			storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", nil),
			storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
		},
	})
	if got.State != StoreUpdateAvailable || got.Target == nil || got.Target.ProductID != "9NCODEX" {
		t.Fatalf("targetless positive should not override exact target: %#v", got)
	}
}

func TestReconcileStoreUpdatePositiveConsensusIgnoresProviderOrder(t *testing.T) {
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-consensus", PackageFamilyName: "OpenAI.Codex_123abc"}
	storeProvider := StoreProviderIdentity{ID: "store-cli-exact", Name: "Store CLI", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: "winget-msstore-exact", Name: "WinGet msstore", Backend: backendWingetMSStoreFallback}
	scan := completedStoreScan("scan-order", identity.UserSID, storeProvider, wingetProvider)
	productTarget := exactStoreTarget(identity, storeProvider)
	productTarget.ProductID = "9NCODEX"
	productTarget.UpdateID = ""
	updateIDTarget := exactStoreTarget(identity, wingetProvider)
	updateIDTarget.ProductID = ""
	updateIDTarget.UpdateID = identity.PackageFamilyName

	for _, observations := range [][]StoreProviderObservation{
		{
			storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", productTarget),
			storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "", updateIDTarget),
		},
		{
			storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "", updateIDTarget),
			storeObservation(identity, scan, storeProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", productTarget),
		},
	} {
		got := ReconcileStoreUpdate(StoreReconciliationInput{
			Identity:          identity,
			Scan:              scan,
			RequiredProviders: []StoreProviderIdentity{storeProvider},
			Observations:      observations,
		})
		if got.State != StoreUpdateAvailable || got.Target == nil {
			t.Fatalf("expected available consensus regardless of provider order, got %#v", got)
		}
		if got.Target.ProductID != "9NCODEX" || got.Target.UpdateID != identity.PackageFamilyName || got.AvailableVersion != "1.1.0" {
			t.Fatalf("provider order changed canonical target/version: %#v", got)
		}
	}
}

func cloneStoreTarget(target *ExactStoreUpdateTarget, provider StoreProviderIdentity) *ExactStoreUpdateTarget {
	if target == nil {
		return nil
	}
	copy := *target
	copy.Provider = provider
	copy.VerifiedBy = provider.Key()
	return &copy
}

func containsFold(value, want string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(want))
}

func completedStoreScan(scanID, userSID string, providers ...StoreProviderIdentity) StoreScanGeneration {
	started := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	providerVersions := map[string]string{}
	providerHealth := map[string]StoreProviderHealth{}
	for _, provider := range providers {
		providerVersions[provider.Key()] = "test"
		providerHealth[provider.Key()] = StoreProviderHealthy
	}
	return StoreScanGeneration{
		ScanID:           scanID,
		UserSID:          userSID,
		StartedAt:        started,
		CompletedAt:      started.Add(time.Second),
		WindowsVersion:   "Windows 11",
		WindowsBuild:     "26000",
		Architecture:     "amd64",
		ProviderVersions: providerVersions,
		CompletionStatus: StoreScanCompleted,
		ProviderHealth:   providerHealth,
	}
}

func exactStoreTarget(identity StoreInstalledIdentity, provider StoreProviderIdentity) *ExactStoreUpdateTarget {
	return &ExactStoreUpdateTarget{
		Identity:   identity,
		Provider:   provider,
		ProductID:  "9NTESTPRODUCT",
		UpdateID:   "9NTESTUPDATE",
		Verified:   true,
		VerifiedBy: provider.Key(),
		VerifiedAt: time.Date(2026, 6, 21, 10, 0, 1, 0, time.UTC),
	}
}

func storeObservation(
	identity StoreInstalledIdentity,
	scan StoreScanGeneration,
	provider StoreProviderIdentity,
	health StoreProviderHealth,
	kind StoreObservationKind,
	installedVersion string,
	availableVersion string,
	target *ExactStoreUpdateTarget,
) StoreProviderObservation {
	return StoreProviderObservation{
		Provider:         provider,
		Health:           health,
		Kind:             kind,
		Identity:         identity,
		ScanID:           scan.ScanID,
		ObservedAt:       scan.CompletedAt,
		InstalledVersion: installedVersion,
		AvailableVersion: availableVersion,
		CatalogVersion:   availableVersion,
		Target:           target,
	}
}
