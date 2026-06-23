package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	liveStoreVP9PackageFamilyName = "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	liveStoreVP9ProductID         = "9N4D0MSMP0PT"
)

func TestParseStoreCLIVersion(t *testing.T) {
	output := "██████╗ ████████╗\n\nv22605.1401.12.0 - Preview\n"
	if got := parseStoreCLIVersion(output); got != "v22605.1401.12.0" {
		t.Fatalf("version = %q", got)
	}
}

func TestLiveStoreCLIExactVP9Assessment(t *testing.T) {
	if os.Getenv("UPDATER_RUN_STORE_LIVE_TESTS") != "1" {
		t.Skip("set UPDATER_RUN_STORE_LIVE_TESTS=1 to run the live Microsoft Store VP9 exact-provider harness")
	}
	if runtime.GOOS != "windows" {
		t.Skip("live Microsoft Store harness requires Windows")
	}
	ensureLiveWorkspaceDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	userSID, err := currentUserSID()
	if err != nil {
		t.Fatalf("current user SID: %v", err)
	}
	scanStarted := time.Now().UTC()
	scan := StoreScanGeneration{
		ScanID:           "live-vp9-" + scanStarted.Format("20060102T150405.000000000Z"),
		UserSID:          userSID,
		StartedAt:        scanStarted,
		WindowsVersion:   liveCommandOutput(ctx, "cmd.exe", "/d", "/c", "ver"),
		Architecture:     runtime.GOARCH,
		ProviderVersions: map[string]string{},
		ProviderHealth:   map[string]StoreProviderHealth{},
		CompletionStatus: StoreScanRunning,
	}

	inventory, inventoryResult := storePackagedAppInventoryProvider().Inventory(ctx, scan)
	if !inventoryResult.OK {
		t.Fatalf("native current-user Store inventory failed: %s", firstNonEmpty(inventoryResult.Stderr, inventoryResult.Stdout))
	}
	vp9Family, found := findStorePackagedFamily(inventory.Families, liveStoreVP9PackageFamilyName)
	if !found {
		t.Fatalf("VP9 package family %s was not found in current-user Store inventory", liveStoreVP9PackageFamilyName)
	}

	scan.CompletedAt = time.Now().UTC()
	scan.CompletionStatus = StoreScanCompleted
	storeHelp := liveCommandOutput(ctx, managerCommand(managerStore, "--help")...)
	storeVersion := parseStoreCLIVersion(storeHelp)
	wingetVersion := liveCommandOutput(ctx, managerCommand(managerWinget, "--version")...)
	provider := storeCLIExactCatalogProvider{Concurrency: 1}
	run := provider.Observe(ctx, scan, []StorePackagedAppFamily{vp9Family})
	if run.Health != StoreProviderHealthy {
		t.Fatalf("Store CLI exact provider health=%s error=%s", run.Health, run.Error)
	}
	if len(run.Observations) != 1 {
		t.Fatalf("expected one VP9 provider observation, got %d: %#v", len(run.Observations), run.Observations)
	}
	observation := run.Observations[0]
	if observation.Identity.PackageFamilyName != liveStoreVP9PackageFamilyName {
		t.Fatalf("observation identity=%#v", observation.Identity)
	}
	if observation.Mapping == nil || observation.Mapping.ProductID != liveStoreVP9ProductID {
		t.Fatalf("expected verified VP9 Product ID %s, observation=%#v", liveStoreVP9ProductID, observation)
	}
	assessment := ReconcileStoreUpdate(StoreReconciliationInput{
		Identity:          vp9Family.Identity,
		Scan:              scan,
		RequiredProviders: []StoreProviderIdentity{run.Provider},
		Observations:      run.Observations,
	})
	apiPackage := packageFromPublishedStoreAssessment(defaultState(), StorePublishedAssessment{
		StoreUpdateAssessment:      assessment,
		ObservedAt:                 scan.CompletedAt,
		StoreProductID:             liveStoreVP9ProductID,
		UpdateID:                   targetUpdateID(assessment.Target),
		ExactActionTargetAvailable: assessment.Target != nil && assessment.Target.ExactFor(vp9Family.Identity),
		Applicability:              applicabilityForAssessment(assessment),
	}, vp9Family, nil)

	evidence := map[string]any{
		"windows_version":               strings.TrimSpace(scan.WindowsVersion),
		"architecture":                  scan.Architecture,
		"store_cli_version":             sanitizeProviderDiagnostic(firstNonEmpty(storeVersion, firstLine(storeHelp))),
		"winget_version":                sanitizeProviderDiagnostic(firstLine(wingetVersion)),
		"scan_id":                       scan.ScanID,
		"provider":                      run.Provider,
		"provider_health":               run.Health,
		"observation_kind":              observation.Kind,
		"installed_package_family_name": observation.Identity.PackageFamilyName,
		"store_product_id":              liveStoreVP9ProductID,
		"store_update_id":               targetUpdateID(assessment.Target),
		"assessment_state":              assessment.State,
		"assessment_reason":             assessment.Reason,
		"api_record":                    apiPackage,
	}
	formatted, _ := json.MarshalIndent(evidence, "", "  ")
	t.Logf("sanitized live VP9 Store evidence:\n%s", formatted)

	if liveExpectVP9Update() && assessment.State != StoreUpdateAvailable {
		t.Fatalf("VP9 was expected to be updatable, got state=%s reason=%s", assessment.State, assessment.Reason)
	}
	if assessment.State == StoreUpdateAvailable && !liveVP9APIAcceptanceFields(apiPackage) {
		t.Fatalf("VP9 API record does not satisfy live acceptance fields: %#v", apiPackage)
	}
}

func TestLiveAPIPackagesVP9Assessment(t *testing.T) {
	if os.Getenv("UPDATER_RUN_STORE_LIVE_API_TESTS") != "1" {
		t.Skip("set UPDATER_RUN_STORE_LIVE_API_TESTS=1 to run the live /api/packages VP9 acceptance harness")
	}
	if runtime.GOOS != "windows" {
		t.Skip("live Microsoft Store API harness requires Windows")
	}
	ensureLiveWorkspaceDirs(t)
	app := testSessionApp()
	app.refreshInventorySync("live VP9 API acceptance")

	request := authenticatedRequest(app, http.MethodGet, "/api/packages", nil)
	response := httptest.NewRecorder()
	app.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/api/packages returned %d: %s", response.Code, response.Body.String())
	}
	var payload InventoryResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	pkg, found := findPackageByPFN(payload.Packages, liveStoreVP9PackageFamilyName)
	if !found {
		t.Fatalf("/api/packages did not include VP9 PFN %s", liveStoreVP9PackageFamilyName)
	}
	evidence := map[string]any{
		"store_scan_health": payload.StoreScanHealth,
		"api_record":        pkg,
		"loading":           payload.Loading,
		"updated_at":        payload.UpdatedAt,
	}
	formatted, _ := json.MarshalIndent(evidence, "", "  ")
	t.Logf("sanitized live /api/packages VP9 evidence:\n%s", formatted)
	if liveExpectVP9Update() && pkg.UpdateState != string(StoreUpdateAvailable) {
		t.Fatalf("VP9 was expected to be available in /api/packages, got %#v", pkg)
	}
	if pkg.UpdateState == string(StoreUpdateAvailable) && !liveVP9APIAcceptanceFields(pkg) {
		t.Fatalf("/api/packages VP9 record does not satisfy live acceptance fields: %#v", pkg)
	}
}

func TestLiveVP9StoreCLIProductIDTargetBehavior(t *testing.T) {
	if os.Getenv("UPDATER_RUN_STORE_LIVE_TARGET_TESTS") != "1" {
		t.Skip("set UPDATER_RUN_STORE_LIVE_TARGET_TESTS=1 to run the live VP9 Store CLI target harness")
	}
	if runtime.GOOS != "windows" {
		t.Skip("live Microsoft Store target harness requires Windows")
	}
	ensureLiveWorkspaceDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	productResult := runCommandContext(ctx, 45*time.Second, storeUpdateCommand(liveStoreVP9ProductID, false)...)
	pfnResult := runCommandContext(ctx, 45*time.Second, storeUpdateCommand(liveStoreVP9PackageFamilyName, false)...)
	wingetShowResult := runCommandContext(ctx, 45*time.Second, managerCommand(managerWinget,
		"show",
		"--id", liveStoreVP9ProductID,
		"--exact",
		"--source", sourceMSStore,
		"--accept-source-agreements",
		"--disable-interactivity",
	)...)
	productState, productErr := parseStoreCLIUpdateCheck(productResult.Stdout + "\n" + productResult.Stderr)
	pfnState, pfnErr := parseStoreCLIUpdateCheck(pfnResult.Stdout + "\n" + pfnResult.Stderr)
	evidence := map[string]any{
		"product_id":          liveStoreVP9ProductID,
		"package_family_name": liveStoreVP9PackageFamilyName,
		"product_id_result": map[string]any{
			"ok":     productResult.OK,
			"code":   productResult.Code,
			"state":  productState,
			"stdout": sanitizeProviderDiagnostic(productResult.Stdout),
			"stderr": sanitizeProviderDiagnostic(productResult.Stderr),
		},
		"pfn_result": map[string]any{
			"ok":     pfnResult.OK,
			"code":   pfnResult.Code,
			"state":  pfnState,
			"stdout": sanitizeProviderDiagnostic(pfnResult.Stdout),
			"stderr": sanitizeProviderDiagnostic(pfnResult.Stderr),
		},
		"winget_msstore_product_id_show_result": map[string]any{
			"ok":     wingetShowResult.OK,
			"code":   wingetShowResult.Code,
			"stdout": sanitizeProviderDiagnostic(wingetShowResult.Stdout),
			"stderr": sanitizeProviderDiagnostic(wingetShowResult.Stderr),
		},
	}
	formatted, _ := json.MarshalIndent(evidence, "", "  ")
	t.Logf("sanitized live VP9 Store CLI target behavior:\n%s", formatted)
	if !wingetShowResult.OK || !strings.Contains(strings.ToLower(wingetShowResult.Stdout), strings.ToLower(liveStoreVP9ProductID)) {
		t.Fatalf("expected WinGet msstore show to resolve exact Product ID, result=%#v evidence=%s", wingetShowResult, formatted)
	}
	if productErr == nil || productState != StoreObservationIncompleteResult {
		t.Fatalf("expected current Store CLI Product ID target to be non-authoritative, state=%s err=%v evidence=%s", productState, productErr, formatted)
	}
	if pfnErr != nil || (pfnState != StoreObservationPositiveUpdateOffer &&
		pfnState != StoreObservationAuthoritativeNegative &&
		pfnState != StoreObservationNewerCatalogNoApplicableInstaller) {
		t.Fatalf("expected PFN/update ID target to produce authoritative Store update evidence, state=%s err=%v evidence=%s", pfnState, pfnErr, formatted)
	}
}

func TestLiveVP9ExactUpdateExecution(t *testing.T) {
	if os.Getenv("UPDATER_RUN_STORE_LIVE_EXECUTION_TESTS") != "1" {
		t.Skip("set UPDATER_RUN_STORE_LIVE_EXECUTION_TESTS=1 to run the live VP9 exact update execution harness")
	}
	if os.Getenv("UPDATER_APPLY_STORE_LIVE_UPDATE") != "1" {
		t.Skip("set UPDATER_APPLY_STORE_LIVE_UPDATE=1 to permit applying the live VP9 Store update")
	}
	if runtime.GOOS != "windows" {
		t.Skip("live Microsoft Store execution harness requires Windows")
	}
	ensureLiveWorkspaceDirs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	app := testSessionApp()
	preInventory := app.refreshInventorySync("live VP9 pre-update acceptance")
	prePackage, found := findPackageByPFN(preInventory.Packages, liveStoreVP9PackageFamilyName)
	if !found {
		t.Fatalf("VP9 package family %s was not found before update", liveStoreVP9PackageFamilyName)
	}
	if prePackage.UpdateState != string(StoreUpdateAvailable) {
		if liveExpectVP9Update() {
			t.Fatalf("VP9 was expected to be updatable before execution, got %#v", prePackage)
		}
		t.Skipf("VP9 is not currently updatable; state=%s reason=%s", prePackage.UpdateState, prePackage.UpdateReason)
	}
	if !liveVP9APIAcceptanceFields(prePackage) {
		t.Fatalf("pre-update VP9 API record lacks exact acceptance fields: %#v", prePackage)
	}

	executor := StoreExactUpdateExecutor{
		Runner:    storeProductIDFirstExactUpdateRunner{},
		Inventory: storePackagedSnapshotProvider{Provider: storePackagedAppInventoryProvider()},
		Catalog:   storeCLIExactCatalogQueryProvider{},
		Events:    noStorePackageEvents{},
		Timeout:   8 * time.Minute,
		PollEvery: 5 * time.Second,
	}
	result := executor.Execute(ctx, prePackage)
	postInventory := app.refreshInventorySync("live VP9 post-update acceptance")
	postPackage, postFound := findPackageByPFN(postInventory.Packages, liveStoreVP9PackageFamilyName)

	evidence := map[string]any{
		"pre_api_record":  prePackage,
		"post_api_record": postPackage,
		"post_found":      postFound,
		"result_ok":       result.OK,
		"result_code":     result.Code,
		"command":         sanitizeProviderDiagnostic(result.Command),
		"stdout":          sanitizeProviderDiagnostic(result.Stdout),
		"stderr":          sanitizeProviderDiagnostic(result.Stderr),
	}
	formatted, _ := json.MarshalIndent(evidence, "", "  ")
	t.Logf("sanitized live VP9 update execution evidence:\n%s", formatted)

	if !result.OK {
		t.Fatalf("live VP9 exact update was not verified: code=%d stderr=%s stdout=%s", result.Code, result.Stderr, result.Stdout)
	}
	if postFound && postPackage.UpdateState == string(StoreUpdateAvailable) {
		t.Fatalf("VP9 still appears available after verified update: %#v", postPackage)
	}
}

func ensureLiveWorkspaceDirs(t *testing.T) {
	t.Helper()
	if os.Getenv("UPDATER_STATE_DIR") == "" {
		t.Setenv("UPDATER_STATE_DIR", filepath.Join(t.TempDir(), "live-state"))
	}
}

func liveExpectVP9Update() bool {
	return os.Getenv("UPDATER_EXPECT_VP9_UPDATE") == "1"
}

func liveVP9APIAcceptanceFields(pkg Package) bool {
	return pkg.UpdateState == string(StoreUpdateAvailable) &&
		pkg.InstalledPackageFamilyName == liveStoreVP9PackageFamilyName &&
		pkg.StoreProductID == liveStoreVP9ProductID &&
		pkg.ExactIdentityAvailable &&
		pkg.ExactActionTargetAvailable
}

func findStorePackagedFamily(families []StorePackagedAppFamily, pfn string) (StorePackagedAppFamily, bool) {
	for _, family := range families {
		if strings.EqualFold(family.Identity.PackageFamilyName, pfn) {
			return family, true
		}
	}
	return StorePackagedAppFamily{}, false
}

func findPackageByPFN(packages []Package, pfn string) (Package, bool) {
	for _, pkg := range packages {
		if strings.EqualFold(pkg.InstalledPackageFamilyName, pfn) || strings.EqualFold(pkg.ID, pfn) {
			return pkg, true
		}
	}
	return Package{}, false
}

func targetUpdateID(target *ExactStoreUpdateTarget) string {
	if target == nil {
		return ""
	}
	return target.UpdateID
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line, _, _ := strings.Cut(value, "\n")
	return strings.TrimSpace(line)
}

func parseStoreCLIVersion(output string) string {
	for _, raw := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(strings.TrimSpace(raw)) {
			field = strings.Trim(field, ",;()[]")
			if len(field) < 2 || field[0] != 'v' {
				continue
			}
			if field[1] >= '0' && field[1] <= '9' {
				return field
			}
		}
	}
	return ""
}

func liveCommandOutput(ctx context.Context, args ...string) string {
	result := runCommandContext(ctx, 30*time.Second, args...)
	return firstNonEmpty(result.Stdout, result.Stderr)
}
