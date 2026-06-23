package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExactPFNFromWingetMSStorePackage(t *testing.T) {
	tests := []struct {
		name      string
		pkg       Package
		wantPFN   string
		wantID    string
		wantExact bool
	}{
		{
			name:      "package family match column with product id",
			pkg:       Package{ID: "9N4D0MSMP0PT", Match: "PackageFamilyName: Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantPFN:   "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe",
			wantID:    "9N4D0MSMP0PT",
			wantExact: true,
		},
		{
			name:      "direct pfn id",
			pkg:       Package{ID: "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"},
			wantPFN:   "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe",
			wantExact: true,
		},
		{
			name:      "full msix package id",
			pkg:       Package{ID: `MSIX\Microsoft.VP9VideoExtensions_1.2.20.0_x64__8wekyb3d8bbwe`},
			wantPFN:   "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe",
			wantExact: true,
		},
		{
			name:      "truncated msix rejected",
			pkg:       Package{ID: `MSIX\Microsoft.VP9VideoExtensions_1.2.20.0_x64__8wekyb3d8bb...`},
			wantExact: false,
		},
		{
			name:      "display name rejected",
			pkg:       Package{ID: "VP9 Video Extensions"},
			wantExact: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPFN, gotProductID, gotExact := exactPFNFromWingetMSStorePackage(tc.pkg)
			if gotPFN != tc.wantPFN || gotProductID != tc.wantID || gotExact != tc.wantExact {
				t.Fatalf("got pfn=%q product=%q exact=%t, want pfn=%q product=%q exact=%t", gotPFN, gotProductID, gotExact, tc.wantPFN, tc.wantID, tc.wantExact)
			}
		})
	}
}

func TestWingetMSStoreExactProviderEmitsExactPositive(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	scan := StoreScanGeneration{
		ScanID:           "scan-winget-msstore",
		UserSID:          "S-1-5-21-vp9",
		StartedAt:        time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		CompletedAt:      time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC),
		CompletionStatus: StoreScanCompleted,
	}
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := wingetMSStoreExactCatalogProvider{
		Now: fixedPipelineTimes(scan.StartedAt, scan.StartedAt.Add(time.Second)),
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `
Name                  ID            Version   Available  Match                                                                Source
-------------------------------------------------------------------------------------------------------------------------------
VP9 Video Extensions  9N4D0MSMP0PT  1.2.13.0  1.2.20.0   PackageFamilyName: Microsoft.VP9VideoExtensions_8wekyb3d8bbwe  msstore
`}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, pfn, "1.2.13.0").Families)
	if run.Health != StoreProviderHealthy || len(run.Observations) != 1 || len(run.Mappings) != 1 {
		t.Fatalf("run=%#v", run)
	}
	observation := run.Observations[0]
	if observation.Kind != StoreObservationPositiveUpdateOffer || observation.Target == nil {
		t.Fatalf("observation=%#v", observation)
	}
	if observation.Target.ProductID != "9N4D0MSMP0PT" || observation.Target.UpdateID != pfn {
		t.Fatalf("target=%#v", observation.Target)
	}
}

func TestWingetMSStoreExactProviderIgnoresNonExactRows(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	scan := StoreScanGeneration{ScanID: "scan-winget-msstore", UserSID: "S-1-5-21-vp9", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanCompleted}
	provider := wingetMSStoreExactCatalogProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `
Name                  ID                                      Version   Available  Source
-----------------------------------------------------------------------------------------
VP9 Video Extensions  VP9 Video Extensions                    1.2.13.0  1.2.20.0   msstore
Truncated             MSIX\Microsoft.VP9VideoExtensions_1...  1.2.13.0  1.2.20.0   msstore
`}
		},
	}
	run := provider.Observe(context.Background(), scan, testStoreInventory(scan, "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe", "1.2.13.0").Families)
	if run.Health != StoreProviderIncomplete || len(run.Observations) != 0 || !strings.Contains(run.Error, "without exact installed PFN association") {
		t.Fatalf("expected non-authoritative diagnostics only, got %#v", run)
	}
}

func TestWingetMSStoreProviderIsNotRequiredForReconciliation(t *testing.T) {
	required := requiredStoreCatalogProviders([]StoreCatalogProviderRun{
		{Provider: StoreProviderIdentity{ID: storeCLIExactProviderID}},
		{Provider: StoreProviderIdentity{ID: storeCLIUpdatesProviderID}},
		{Provider: StoreProviderIdentity{ID: wingetMSStoreExactProviderID}, Health: StoreProviderFailed},
	})
	if len(required) != 1 || required[0].ID != storeCLIUpdatesProviderID {
		t.Fatalf("Store CLI aggregate should be required while exact providers stay optional: %#v", required)
	}
}

func TestWingetMSStoreExactCatalogQueryProviderAvailable(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	provider := wingetMSStoreExactCatalogQueryProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			if !strings.Contains(command, "list --upgrade-available --id 9N4D0MSMP0PT --exact") || !strings.Contains(command, "--source msstore") {
				t.Fatalf("unexpected exact query command: %s", command)
			}
			return CommandResult{OK: true, Command: command, Stdout: `
Name                  ID            Version   Available  Match                                                              Source
-----------------------------------------------------------------------------------------------------------------------------
VP9 Video Extensions  9N4D0MSMP0PT  1.2.13.0  1.2.20.0   PackageFamilyName: Microsoft.VP9VideoExtensions_8wekyb3d8bbwe  msstore
`}
		},
	}
	got, result := provider.QueryExact(context.Background(), StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: pfn}, ProductID: "9N4D0MSMP0PT"})
	if !result.OK || !got.Authoritative || !got.OfferAvailable || got.OfferedVersion != "1.2.20.0" || !got.InstalledHealthy {
		t.Fatalf("catalog=%#v result=%#v", got, result)
	}
}

func TestWingetMSStoreExactCatalogQueryProviderNoOfferWithoutPFNIsNotAuthoritative(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	provider := wingetMSStoreExactCatalogQueryProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{Command: strings.Join(args, " "), Code: 1, Stdout: "No applicable upgrade found."}
		},
	}
	got, result := provider.QueryExact(context.Background(), StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"}, ProductID: "9N4D0MSMP0PT"})
	if result.OK || got.Authoritative || got.OfferAvailable || !strings.Contains(got.Diagnostics, "did not return an exact package family association") {
		t.Fatalf("catalog=%#v result=%#v", got, result)
	}
}

func TestWingetMSStoreExactCatalogQueryProviderNotFoundIsNotAuthoritative(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	provider := wingetMSStoreExactCatalogQueryProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{Command: strings.Join(args, " "), Code: 1, Stdout: "No installed package found matching input criteria."}
		},
	}
	got, result := provider.QueryExact(context.Background(), StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"}, ProductID: "9N4D0MSMP0PT"})
	if got.Authoritative || result.OK {
		t.Fatalf("not-found output must not verify Store state: catalog=%#v result=%#v", got, result)
	}
}

func TestWingetMSStoreExactCatalogQueryProviderRejectsMismatchedPFN(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget })
	defer restore()
	provider := wingetMSStoreExactCatalogQueryProvider{
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: `
Name                  ID            Version   Available  Match                                Source
-----------------------------------------------------------------------------------------------
VP9 Video Extensions  9N4D0MSMP0PT  1.2.13.0  1.2.20.0   PackageFamilyName: Other.App_abc123  msstore
`}
		},
	}
	got, _ := provider.QueryExact(context.Background(), StoreExactUpdateRequest{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-vp9", PackageFamilyName: "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"}, ProductID: "9N4D0MSMP0PT"})
	if got.Authoritative {
		t.Fatalf("mismatched PFN must not be authoritative: %#v", got)
	}
}
