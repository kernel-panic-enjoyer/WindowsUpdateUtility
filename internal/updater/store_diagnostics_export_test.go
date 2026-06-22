package updater

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStoreDiagnosticsExportSanitizesUserSIDAndIncludesEvidence(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pfn := "OpenAI.Codex_abc123"
	store, err := openDefaultStoreScanStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	scan := StoreScanGeneration{ScanID: "diag-scan", UserSID: userSID, StartedAt: now, CompletedAt: now.Add(time.Second), CompletionStatus: StoreScanCompleted}
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	provider := StoreProviderIdentity{ID: "catalog-test", Name: "Catalog Test", Backend: "fake"}
	target := &ExactStoreUpdateTarget{Identity: identity, Provider: provider, ProductID: "9NCODEX", Verified: true, VerifiedBy: "test", VerifiedAt: now}
	observation := StoreProviderObservation{
		Provider:         provider,
		Health:           StoreProviderHealthy,
		Kind:             StoreObservationPositiveUpdateOffer,
		Identity:         identity,
		ScanID:           scan.ScanID,
		ObservedAt:       now,
		InstalledVersion: "1.0.0",
		AvailableVersion: "1.1.0",
		Target:           target,
	}
	input := storeScanPersistInput{
		Scan: scan,
		Inventory: StorePackagedAppInventory{Scan: scan, Families: []StorePackagedAppFamily{{
			Identity:    identity,
			DisplayName: "Codex",
			ProductLike: true,
		}}},
		ProviderRuns: []StoreCatalogProviderRun{{
			Provider:     provider,
			Version:      "v1.2.3",
			StartedAt:    now,
			CompletedAt:  now.Add(time.Second),
			Health:       StoreProviderHealthy,
			Observations: []StoreProviderObservation{observation},
		}},
		Assessments: []StorePublishedAssessment{{
			StoreUpdateAssessment: StoreUpdateAssessment{
				State:            StoreUpdateAvailable,
				Identity:         identity,
				ScanID:           scan.ScanID,
				Reason:           "fresh exact positive update evidence",
				InstalledVersion: "1.0.0",
				AvailableVersion: "1.1.0",
				Target:           target,
			},
			ObservedAt:                 now,
			StoreProductID:             "9NCODEX",
			ExactActionTargetAvailable: true,
			Applicability:              "applicable",
		}},
		Publish: true,
	}
	if _, err := store.PersistScan(context.Background(), input); err != nil {
		t.Fatal(err)
	}

	data, err := buildStoreDiagnosticsExport(context.Background(), loadState())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), userSID) {
		t.Fatalf("diagnostics export leaked raw user SID: %s", string(data))
	}
	var export StoreDiagnosticsExport
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	if export.Scan.ScanID != "diag-scan" || len(export.Packages) != 1 || len(export.Observations) != 1 || len(export.Assessments) != 1 {
		t.Fatalf("diagnostics export missing scan evidence: %#v", export)
	}
	if export.UserScopeHash == "" {
		t.Fatalf("diagnostics export should include a user scope hash: %#v", export)
	}
	if len(export.Providers) != 1 || export.Providers[0].Version != "v1.2.3" {
		t.Fatalf("diagnostics export missing provider version: %#v", export.Providers)
	}
}
