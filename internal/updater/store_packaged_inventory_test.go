package updater

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseStorePackagedInventoryResponse(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	data := `{
		"protocol_version":1,
		"broker_version":"store-inventory-broker/test",
		"scan_id":"scan-1",
		"user_sid":"S-1-5-21-test-1001",
		"started_at":"2026-06-21T10:00:00Z",
		"completed_at":"2026-06-21T10:00:01Z",
		"complete":true,
		"records":[{
			"user_sid":"S-1-5-21-test-1001",
			"package_family_name":"OpenAI.Codex_abc",
			"package_full_name":"OpenAI.Codex_1.2.3.4_x64__abc",
			"identity_name":"OpenAI.Codex",
			"publisher":"CN=OpenAI",
			"publisher_id":"abc",
			"version":{"major":1,"minor":2,"build":3,"revision":4},
			"processor_architecture":"X64",
			"install_location":"C:\\Program Files\\WindowsApps\\OpenAI.Codex",
			"package_type":"Windows.ApplicationModel.Package",
			"is_bundle":true,
			"status":{"ok":true},
			"display_name":"Codex"
		}]
	}`
	inventory, err := parseStorePackagedInventoryResponse([]byte(data), scan)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Scan.CompletionStatus != StoreScanCompleted {
		t.Fatalf("completion = %q, want %q", inventory.Scan.CompletionStatus, StoreScanCompleted)
	}
	if len(inventory.Records) != 1 || inventory.Records[0].Version.String() != "1.2.3.4" {
		t.Fatalf("unexpected records: %#v", inventory.Records)
	}
	if got := inventory.Records[0].Classification; got != storePackageClassBundle {
		t.Fatalf("classification = %q, want bundle", got)
	}
	if len(inventory.Families) != 1 || !inventory.Families[0].ProductLike {
		t.Fatalf("unexpected families: %#v", inventory.Families)
	}
	if inventory.BrokerVersion != "store-inventory-broker/test" {
		t.Fatalf("broker version = %q", inventory.BrokerVersion)
	}
}

func TestParseStorePackagedInventoryRejectsMalformedAndWrongUserResponses(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	tests := []struct {
		name string
		data string
	}{
		{"malformed", `{"protocol_version":`},
		{"unknown field", `{"protocol_version":1,"scan_id":"scan-1","user_sid":"S-1-5-21-test-1001","complete":true,"records":[],"extra":true}`},
		{"wrong user", `{"protocol_version":1,"scan_id":"scan-1","user_sid":"S-1-5-21-test-1002","complete":true,"records":[]}`},
		{"wrong scan", `{"protocol_version":1,"scan_id":"scan-2","user_sid":"S-1-5-21-test-1001","complete":true,"records":[]}`},
		{"missing pfn", `{"protocol_version":1,"scan_id":"scan-1","user_sid":"S-1-5-21-test-1001","complete":true,"records":[{"user_sid":"S-1-5-21-test-1001","package_full_name":"A_1.0.0.0_x64__abc","identity_name":"A","version":{},"status":{"ok":true}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseStorePackagedInventoryResponse([]byte(tt.data), scan); err == nil {
				t.Fatal("expected parse rejection")
			}
		})
	}
}

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

func TestBrokerStorePackagedAppInventoryProviderTimeoutAndPartialErrors(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			return nil, []byte("timeout"), context.DeadlineExceeded
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || len(inventory.Errors) == 0 {
		t.Fatalf("expected partial timeout result, inventory=%#v result=%#v", inventory, result)
	}
}

func TestBrokerStorePackagedAppInventoryProviderParsesSuccessfulResponse(t *testing.T) {
	scan := testNativeInventoryScan("scan-1", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			if !strings.Contains(string(input), `"scan_id":"scan-1"`) {
				return nil, nil, errors.New("request did not contain scan id")
			}
			return []byte(`{"protocol_version":1,"scan_id":"scan-1","user_sid":"S-1-5-21-test-1001","complete":true,"records":[{"user_sid":"S-1-5-21-test-1001","package_family_name":"App_abc","package_full_name":"App_1.0.0.0_x64__abc","identity_name":"App","version":{"major":1},"status":{"ok":true}}]}`), nil, nil
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if !result.OK || len(inventory.Records) != 1 {
		t.Fatalf("expected successful parse, inventory=%#v result=%#v", inventory, result)
	}
}

func TestBrokerStorePackagedAppInventoryProviderParsesStructuredErrorOnNonzeroExit(t *testing.T) {
	scan := testNativeInventoryScan("scan-error", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			return []byte(`{"protocol_version":1,"broker_version":"store-inventory-broker/test","scan_id":"scan-error","user_sid":"S-1-5-21-test-1001","complete":false,"partial":true,"error":"WinRT inventory failed","records":[]}`), []byte("native stderr"), errors.New("exit status 1")
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || result.Code != 1 || !inventory.Partial {
		t.Fatalf("expected structured incomplete broker result, inventory=%#v result=%#v", inventory, result)
	}
	if len(inventory.Errors) != 1 || inventory.Errors[0] != "WinRT inventory failed" {
		t.Fatalf("broker error not preserved: %#v", inventory)
	}
	if !strings.Contains(result.Stderr, "WinRT inventory failed") || !strings.Contains(result.Stderr, "exit status 1") || !strings.Contains(result.Stderr, "native stderr") {
		t.Fatalf("stderr did not preserve structured/process diagnostics: %q", result.Stderr)
	}
	if inventory.BrokerVersion != "store-inventory-broker/test" {
		t.Fatalf("broker version not parsed from structured error: %#v", inventory)
	}
}

func TestBrokerStorePackagedAppInventoryProviderNonzeroMalformedOutputReportsBothErrors(t *testing.T) {
	scan := testNativeInventoryScan("scan-malformed", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			return []byte(`{"protocol_version":`), nil, errors.New("exit status 1")
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || !strings.Contains(result.Stderr, "exit status 1") {
		t.Fatalf("expected malformed output plus process error, inventory=%#v result=%#v", inventory, result)
	}
}

func TestBrokerStorePackagedAppInventoryProviderNonzeroWrongIdentityRejected(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"wrong scan", `{"protocol_version":1,"scan_id":"other-scan","user_sid":"S-1-5-21-test-1001","complete":false,"partial":true,"error":"wrong scan","records":[]}`},
		{"wrong user", `{"protocol_version":1,"scan_id":"scan-identity","user_sid":"S-1-5-21-other","complete":false,"partial":true,"error":"wrong user","records":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scan := testNativeInventoryScan("scan-identity", "S-1-5-21-test-1001")
			provider := brokerStorePackagedAppInventoryProvider{
				Path: "broker.exe",
				Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
					return []byte(tc.json), nil, errors.New("exit status 1")
				},
			}
			inventory, result := provider.Inventory(context.Background(), scan)
			if result.OK || !inventory.Partial || len(inventory.Errors) == 0 {
				t.Fatalf("expected rejected structured identity error, inventory=%#v result=%#v", inventory, result)
			}
			if strings.Contains(strings.Join(inventory.Errors, "\n"), "wrong user") || strings.Contains(strings.Join(inventory.Errors, "\n"), "wrong scan") {
				t.Fatalf("wrong identity response error should not be accepted as broker truth: %#v", inventory.Errors)
			}
		})
	}
}

func TestBrokerStorePackagedAppInventoryProviderZeroExitIncompleteResponse(t *testing.T) {
	scan := testNativeInventoryScan("scan-incomplete", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			return []byte(`{"protocol_version":1,"scan_id":"scan-incomplete","user_sid":"S-1-5-21-test-1001","complete":false,"partial":true,"error":"WinRT partial enumeration","records":[]}`), nil, nil
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || result.Code != 1 || !inventory.Partial || !strings.Contains(result.Stderr, "WinRT partial enumeration") {
		t.Fatalf("zero-exit incomplete response must remain incomplete: inventory=%#v result=%#v", inventory, result)
	}
}

func TestBrokerStorePackagedAppInventoryProviderEmptyStdoutProcessError(t *testing.T) {
	scan := testNativeInventoryScan("scan-empty", "S-1-5-21-test-1001")
	provider := brokerStorePackagedAppInventoryProvider{
		Path: "broker.exe",
		Runner: func(ctx context.Context, path string, input []byte) ([]byte, []byte, error) {
			return nil, nil, errors.New("exit status 1")
		},
	}
	inventory, result := provider.Inventory(context.Background(), scan)
	if result.OK || !inventory.Partial || !strings.Contains(result.Stderr, "exit status 1") {
		t.Fatalf("expected process error for empty stdout, inventory=%#v result=%#v", inventory, result)
	}
}

func TestParseStorePackagedInventoryKeepsBrokerErrorProtocolFields(t *testing.T) {
	scan := testNativeInventoryScan("wrong-user-scan", "S-1-5-21-wrong")
	data := `{"protocol_version":1,"scan_id":"wrong-user-scan","user_sid":"S-1-5-21-wrong","started_at":"2026-06-21T10:00:00Z","completed_at":"2026-06-21T10:00:01Z","complete":false,"partial":true,"error":"Broker user SID does not match request user SID.","records":[]}`
	inventory, err := parseStorePackagedInventoryResponse([]byte(data), scan)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Scan.ScanID != scan.ScanID || inventory.Scan.UserSID != scan.UserSID || !inventory.Partial || len(inventory.Errors) != 1 {
		t.Fatalf("broker error protocol fields not preserved: %#v", inventory)
	}
}

func TestEnsureEmbeddedStoreInventoryBrokerExtractsSidecar(t *testing.T) {
	binaryDir := filepath.Join(workspaceRootForTest(t), ".tmp-bin", "embedded-broker-test-"+time.Now().UTC().Format("20060102150405.000000000"))
	t.Cleanup(func() { _ = os.RemoveAll(binaryDir) })
	t.Setenv("UPDATER_BINARY_DIR", binaryDir)
	path, err := ensureEmbeddedStoreInventoryBroker()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, binaryDir) {
		t.Fatalf("broker path %q is outside binary dir %q", path, binaryDir)
	}
	if filepath.Base(path) != "WindowsUpdater.StoreInventoryBroker.exe" {
		t.Fatalf("unexpected broker path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data[:2]), "MZ") {
		t.Fatalf("extracted broker does not look like a Windows executable")
	}
}

func TestEmbeddedStoreInventoryBrokerAssetPresent(t *testing.T) {
	if len(embeddedStoreInventoryBroker) == 0 {
		t.Fatal("embedded Store inventory broker asset is empty")
	}
	if len(embeddedStoreInventoryBroker) < 2 || string(embeddedStoreInventoryBroker[:2]) != "MZ" {
		t.Fatal("embedded Store inventory broker asset is not a Windows executable")
	}
	if !bytes.Contains(embeddedStoreInventoryBroker, utf16LEForTest("store-inventory-broker/2026.06.22.1")) {
		t.Fatal("embedded Store inventory broker asset does not contain the expected broker build version")
	}
}

func workspaceRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repository root from %s", dir)
		}
		dir = parent
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

func utf16LEForTest(value string) []byte {
	out := make([]byte, 0, len(value)*2)
	for _, r := range value {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}
