package updater

import (
	"testing"
	"time"
)

func TestReconcileStoreUpdate(t *testing.T) {
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-test-1001", PackageFamilyName: "OpenAI.Codex_123abc"}
	otherIdentity := StoreInstalledIdentity{UserSID: "S-1-5-21-test-1002", PackageFamilyName: identity.PackageFamilyName}
	storeProvider := StoreProviderIdentity{ID: "store-broker", Name: "Store broker", Backend: "winrt"}
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

func TestStoreAssessmentToLegacyPackage(t *testing.T) {
	base := Package{
		Key:             "store:OpenAI.Codex_123abc",
		Manager:         managerStore,
		ID:              "OpenAI.Codex_123abc",
		Name:            "Codex",
		Version:         "1.0.0",
		UpdateSupported: true,
	}
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-test-1001", PackageFamilyName: "OpenAI.Codex_123abc"}

	tests := []struct {
		state               StoreUpdateState
		wantUpdateAvailable bool
		wantUpdateSupported bool
	}{
		{StoreUpdateAvailable, true, true},
		{StoreUpdateCurrent, false, true},
		{StoreUpdateUnknown, false, false},
		{StoreUpdateConflict, false, false},
		{StoreUpdateInapplicable, false, false},
		{StoreUpdatePending, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			got := StoreAssessmentToLegacyPackage(base, StoreUpdateAssessment{
				State:            tt.state,
				Identity:         identity,
				AvailableVersion: "1.1.0",
			})
			if got.UpdateAvailable != tt.wantUpdateAvailable {
				t.Fatalf("UpdateAvailable = %v, want %v", got.UpdateAvailable, tt.wantUpdateAvailable)
			}
			if got.UpdateSupported != tt.wantUpdateSupported {
				t.Fatalf("UpdateSupported = %v, want %v", got.UpdateSupported, tt.wantUpdateSupported)
			}
		})
	}
}

func TestStoreUpdateAssessmentModelFeatureFlagDisabled(t *testing.T) {
	if storeUpdateAssessmentModelEnabled {
		t.Fatal("store update assessment model must remain disabled until the active detector is migrated")
	}
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
