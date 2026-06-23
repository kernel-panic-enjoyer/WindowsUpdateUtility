package updater

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGroupStorePackagedAppFamiliesUsesPFNIdentity(t *testing.T) {
	userSID := "S-1-5-21-test-1001"
	records := []StorePackagedAppRecord{
		testNativeRecord(userSID, "Vendor.App_abc", "Vendor.App_1.0.0.0_x64__abc", "Same Name", StorePackageVersion{Major: 1}, storePackageClassMain),
		testNativeRecord(userSID, "Other.App_xyz", "Other.App_1.0.0.0_x64__xyz", "Same Name", StorePackageVersion{Major: 1}, storePackageClassMain),
	}
	families := groupStorePackagedAppFamilies(records)
	if len(families) != 2 {
		t.Fatalf("families = %d, want 2: %#v", len(families), families)
	}
}

func TestGroupStorePackagedAppFamiliesMultipleVersionsPickNewestPrimary(t *testing.T) {
	userSID := "S-1-5-21-test-1001"
	records := []StorePackagedAppRecord{
		testNativeRecord(userSID, "Vendor.App_abc", "Vendor.App_2.0.0.0_x64__abc", "Vendor App", StorePackageVersion{Major: 2}, storePackageClassMain),
		testNativeRecord(userSID, "Vendor.App_abc", "Vendor.App_10.0.0.0_x64__abc", "Vendor App", StorePackageVersion{Major: 10}, storePackageClassMain),
	}
	families := groupStorePackagedAppFamilies(records)
	if len(families) != 1 {
		t.Fatalf("families = %d, want 1", len(families))
	}
	if got := families[0].Primary.Version.String(); got != "10.0.0.0" {
		t.Fatalf("primary version = %s, want 10.0.0.0", got)
	}
}

func TestNativeStorePackagesSkipFrameworkResourceAndOptionalOnlyFamilies(t *testing.T) {
	userSID := "S-1-5-21-test-1001"
	inventory := StorePackagedAppInventory{
		Records: []StorePackagedAppRecord{
			testNativeRecord(userSID, "Framework.Lib_abc", "Framework.Lib_1.0.0.0_x64__abc", "Framework", StorePackageVersion{Major: 1}, storePackageClassFramework),
			testNativeRecord(userSID, "Resource.Pack_abc", "Resource.Pack_1.0.0.0_x64__abc", "Resource", StorePackageVersion{Major: 1}, storePackageClassResource),
			testNativeRecord(userSID, "Main.App_abc", "Main.App_1.0.0.0_x64__abc", "Main App", StorePackageVersion{Major: 1}, storePackageClassMain),
		},
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	packages := packagesFromNativeStorePackagedInventory(defaultState(), inventory)
	if len(packages) != 1 {
		t.Fatalf("packages = %d, want 1: %#v", len(packages), packages)
	}
	if packages[0].ID != "Main.App_abc" || packages[0].Match != "Main.App_1.0.0.0_x64__abc" {
		t.Fatalf("unexpected package identity adapter: %#v", packages[0])
	}
}

func TestNativeStorePackagesUseFriendlyPresentationNames(t *testing.T) {
	userSID := "S-1-5-21-test-1001"
	inventory := StorePackagedAppInventory{
		Records: []StorePackagedAppRecord{
			testNativeRecord(userSID, "19568ShareX.ShareX_egrzcvs15399j", "19568ShareX.ShareX_20.2.0.0_x64__egrzcvs15399j", "", StorePackageVersion{Major: 20, Minor: 2}, storePackageClassMain),
			testNativeRecord(userSID, "1527c705-839a-4832-9118-54d4bd6a0c89_cw5n1h2txyewy", "1527c705-839a-4832-9118-54d4bd6a0c89_1.0.0.0_x64__cw5n1h2txyewy", "", StorePackageVersion{Major: 1}, storePackageClassMain),
		},
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	packages := packagesFromNativeStorePackagedInventory(defaultState(), inventory)
	if len(packages) != 2 {
		t.Fatalf("packages = %d, want 2: %#v", len(packages), packages)
	}
	byID := map[string]Package{}
	for _, pkg := range packages {
		byID[pkg.ID] = pkg
	}
	if got := byID["19568ShareX.ShareX_egrzcvs15399j"].Name; got != "ShareX" {
		t.Fatalf("ShareX presentation name = %q, want ShareX", got)
	}
	if got := byID["1527c705-839a-4832-9118-54d4bd6a0c89_cw5n1h2txyewy"].Name; got != "Store app" {
		t.Fatalf("opaque Store identity presentation name = %q, want Store app", got)
	}
}

func TestNewStorePackagedAppScanRecordsSystemContext(t *testing.T) {
	restoreContext := replaceStoreScanSystemContext(storeScanSystemContext{
		WindowsVersion: "Windows 11 24H2",
		WindowsBuild:   "10.0.26200.8655",
		Architecture:   "x64",
	})
	defer restoreContext()

	scan := newStorePackagedAppScan("S-1-5-21-native-context")
	if scan.WindowsVersion != "Windows 11 24H2" || scan.WindowsBuild != "10.0.26200.8655" || scan.Architecture != "x64" {
		t.Fatalf("native inventory scan context was not recorded: %#v", scan)
	}
}

func TestCompareStorePackagedInventoryDiagnostics(t *testing.T) {
	userSID := "S-1-5-21-test-1001"
	native := StorePackagedAppInventory{
		Records: []StorePackagedAppRecord{
			testNativeRecord(userSID, "Native.Only_abc", "Native.Only_1.0.0.0_x64__abc", "Native Only", StorePackageVersion{Major: 1}, storePackageClassMain),
			testNativeRecord(userSID, "Version.Diff_abc", "Version.Diff_2.0.0.0_x64__abc", "Version Diff", StorePackageVersion{Major: 2}, storePackageClassMain),
			testNativeRecord(userSID, "Framework.Lib_abc", "Framework.Lib_1.0.0.0_x64__abc", "Framework", StorePackageVersion{Major: 1}, storePackageClassFramework),
		},
	}
	native.Families = groupStorePackagedAppFamilies(native.Records)
	legacy := []Package{
		{Match: "Legacy.Only_abc", Version: "1.0.0.0"},
		{Match: "Version.Diff_abc", Version: "1.0.0.0"},
		{Match: "Framework.Lib_abc", Version: "1.0.0.0"},
	}
	comparison := compareStorePackagedInventory(native, legacy, CommandResult{OK: true})
	if !storeInventoryContainsString(comparison.MissingNativePFNs, "Legacy.Only_abc") {
		t.Fatalf("missing native diagnostics absent: %#v", comparison)
	}
	if !storeInventoryContainsString(comparison.MissingLegacyPFNs, "Native.Only_abc") {
		t.Fatalf("missing legacy diagnostics absent: %#v", comparison)
	}
	if len(comparison.VersionDifferences) != 1 || !strings.Contains(comparison.VersionDifferences[0], "Version.Diff_abc") {
		t.Fatalf("version diagnostics absent: %#v", comparison)
	}
	if len(comparison.ClassificationNotes) != 1 || !strings.Contains(comparison.ClassificationNotes[0], "Framework.Lib_abc") {
		t.Fatalf("classification diagnostics absent: %#v", comparison)
	}
}

func TestWinRTStorePackagedAppInventoryProviderTimeoutAndPartialErrors(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := winrtStorePackagedAppInventoryProvider{
		Timeout:        time.Millisecond,
		CurrentUserSID: func() (string, error) { return "S-1-5-21-test-1001", nil },
		Enumerate: func(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || len(inventory.Errors) == 0 {
		t.Fatalf("expected partial timeout result, inventory=%#v result=%#v", inventory, result)
	}
}

func TestWinRTStorePackagedAppInventoryProviderBuildsCompleteInventory(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := winrtStorePackagedAppInventoryProvider{
		CurrentUserSID: func() (string, error) { return "S-1-5-21-test-1001", nil },
		Enumerate: func(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
			return []StorePackagedAppRecord{
				testNativeRecord(userSID, "App_abc", "App_1.0.0.0_x64__abc", "App", StorePackageVersion{Major: 1}, storePackageClassMain),
			}, nil
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if !result.OK || inventory.Partial || inventory.Scan.CompletionStatus != StoreScanCompleted || len(inventory.Records) != 1 || len(inventory.Families) != 1 {
		t.Fatalf("expected successful direct inventory, inventory=%#v result=%#v", inventory, result)
	}
}

func TestWinRTStorePackagedAppInventoryProviderRejectsWrongUser(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := winrtStorePackagedAppInventoryProvider{
		CurrentUserSID: func() (string, error) { return "S-1-5-21-test-1002", nil },
		Enumerate: func(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
			return nil, errors.New("should not enumerate")
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || !strings.Contains(result.Stderr, "user SID mismatch") {
		t.Fatalf("expected wrong-user rejection, inventory=%#v result=%#v", inventory, result)
	}
}

func TestWinRTStorePackagedAppInventoryProviderRejectsMalformedRecords(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := winrtStorePackagedAppInventoryProvider{
		CurrentUserSID: func() (string, error) { return "S-1-5-21-test-1001", nil },
		Enumerate: func(ctx context.Context, userSID string) ([]StorePackagedAppRecord, error) {
			return []StorePackagedAppRecord{{UserSID: userSID, PackageFullName: "App_1.0.0.0_x64__abc", IdentityName: "App"}}, nil
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || !strings.Contains(result.Stderr, "missing package family name") {
		t.Fatalf("expected malformed record rejection, inventory=%#v result=%#v", inventory, result)
	}
}

func testNativeInventoryScan(scanID, userSID string) StoreScanGeneration {
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	return StoreScanGeneration{
		ScanID:           scanID,
		UserSID:          userSID,
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		CompletionStatus: StoreScanCompleted,
	}
}

func testNativeRecord(userSID, pfn, fullName, display string, version StorePackageVersion, classification string) StorePackagedAppRecord {
	record := StorePackagedAppRecord{
		UserSID:           userSID,
		PackageFamilyName: pfn,
		PackageFullName:   fullName,
		IdentityName:      strings.Split(fullName, "_")[0],
		Version:           version,
		DisplayName:       display,
		Status:            StorePackageStatus{OK: true},
	}
	switch classification {
	case storePackageClassBundle:
		record.IsBundle = true
	case storePackageClassFramework:
		record.IsFramework = true
	case storePackageClassResource:
		record.IsResourcePackage = true
	case storePackageClassOptional:
		record.IsOptional = true
	}
	record.Classification = classifyStorePackagedApp(record)
	return record
}

func storeInventoryContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
