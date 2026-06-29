package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testStoreScanIDCounter int64

func TestStoreScanPipelineInterruptedTransactionKeepsPreviousGeneration(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-pipeline"
	pfn := "OpenAI.Codex_abc123"
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	pipeline.NewScanID = func(time.Time) string { return "scan-interrupted" }
	pipeline.BeforeCommit = func(context.Context, StoreScanSnapshot) error {
		return errors.New("boom before commit")
	}
	if _, err := pipeline.Run(context.Background()); err == nil {
		t.Fatal("expected interrupted scan error")
	}
	if snapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID); err != nil || ok {
		t.Fatalf("interrupted transaction published snapshot: snapshot=%#v ok=%t err=%v", snapshot, ok, err)
	}
}

func TestStoreScanPipelineDoesNotPersistWhenPreviousAssessmentsFail(t *testing.T) {
	userSID := "S-1-5-21-hysteresis-fail"
	pfn := "OpenAI.Codex_abc123"
	store := &failingPreviousStoreScanRepository{err: errors.New("prior published snapshot unreadable")}
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	result, err := pipeline.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hysteresis") {
		t.Fatalf("expected hysteresis load failure, got result=%#v err=%v", result, err)
	}
	if store.persistCalls != 0 {
		t.Fatalf("PersistCompletedScanSnapshot was called %d times after prior snapshot load failure", store.persistCalls)
	}
	if result.Scan.ScanID == "" || len(result.ProviderRuns) == 0 || len(result.Assessments) == 0 {
		t.Fatalf("scan diagnostics were not returned with hysteresis failure: %#v", result)
	}
}

func TestStoreScanCoordinatorPreventsConcurrentPipelineEntryPoints(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-scan-singleflight"
	pfn := "OpenAI.Codex_abc123"
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := fakeCatalogProvider{id: "blocking-singleflight", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return positiveProvider(pfn, "1.0.0", "1.1.0").Observe(ctx, scan, families)
	}}
	first := newTestStoreScanPipeline(store, userSID, pfn, blocking)
	second := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	restore := replaceStoreScanSID(userSID)
	defer restore()
	errs := make(chan error, 1)
	go func() {
		_, err := first.Run(context.Background())
		errs <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first scan did not start")
	}
	if _, err := second.Run(context.Background()); !errors.Is(err, errStoreScanAlreadyRunning) {
		close(release)
		t.Fatalf("expected concurrent scan rejection, got %v", err)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatalf("first scan failed: %v", err)
	}
}

func TestStoreScanCoordinatorCancellationReleasesEntryPoint(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-scan-cancel-release"
	pfn := "OpenAI.Codex_abc123"
	started := make(chan struct{})
	blocking := fakeCatalogProvider{id: "blocking-cancel", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		close(started)
		<-ctx.Done()
		return StoreCatalogProviderRun{
			Provider:    StoreProviderIdentity{ID: "blocking-cancel", Name: "blocking-cancel", Backend: "fake"},
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderFailed,
			Error:       ctx.Err().Error(),
		}
	}}
	first := newTestStoreScanPipeline(store, userSID, pfn, blocking)
	restore := replaceStoreScanSID(userSID)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := first.Run(ctx)
		errs <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first scan did not start")
	}
	cancel()
	if err := <-errs; err == nil {
		t.Fatal("cancelled scan unexpectedly succeeded")
	}
	second := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if _, err := second.Run(context.Background()); err != nil {
		t.Fatalf("coordinator was not released after cancellation: %v", err)
	}
}

func TestStoreScanOldStateFixturePreservesDurablePreferences(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_global": true,
  "auto_update_packages": {
    "winget:Git.Git": true,
    "store:s-1-5-21-json~openai.codex_abc123": true
  },
  "registry_apps": {},
  "winget_apps": {},
  "store_apps": {},
  "store_resolve_cache": {
    "legacy": {"appx_version":"1.0.0","store_id":"9NOLD","resolved":true,"resolved_at":"2026-06-14T12:00:00Z"}
  },
  "store_update_assessment_cache": {
    "json": {
      "user_sid": "S-1-5-21-json",
      "package_family_name": "OpenAI.Codex_abc123",
      "scan_id": "json-scan",
      "state": "available",
      "reason": "json positive",
      "observed_at": "2026-06-21T10:00:00Z",
      "installed_version": "1.0.0",
      "offered_version": "1.1.0",
      "store_product_id": "9NCODEX",
      "applicability": "applicable",
      "exact_action_target_available": true
    }
  },
  "unknown_future_field": {"must":"not break startup"},
  "theme": "light"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := loadState()
	if loaded.Theme != "light" || !loaded.AutoUpdateGlobal {
		t.Fatalf("old state fixture did not preserve durable settings: %#v", loaded)
	}
	if !loaded.AutoUpdatePackages["winget:Git.Git"] || !loaded.AutoUpdatePackages["store:s-1-5-21-json~openai.codex_abc123"] {
		t.Fatalf("auto-update preferences were not preserved: %#v", loaded.AutoUpdatePackages)
	}
	if err := saveState(loaded); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "store_update_assessment_cache") {
		t.Fatalf("legacy assessment cache should be omitted after load/save migration: %s", saved)
	}
}

func TestProviderVersionMapUsesActualRunVersions(t *testing.T) {
	runs := []StoreCatalogProviderRun{
		{Provider: StoreProviderIdentity{ID: "store-cli-exact"}, Version: "v22605.1401.12.0"},
		{Provider: StoreProviderIdentity{ID: "winget-msstore-exact"}, Version: "v1.28.240"},
		{Provider: StoreProviderIdentity{ID: "fake-no-version"}},
	}
	got := providerVersionMap(runs)
	if got["store-cli-exact"] != "v22605.1401.12.0" || got["winget-msstore-exact"] != "v1.28.240" {
		t.Fatalf("provider versions not preserved: %#v", got)
	}
	if _, ok := got["fake-no-version"]; ok {
		t.Fatalf("empty provider version should not be advertised: %#v", got)
	}
}

func TestStoreScanPipelineRecordsSystemContext(t *testing.T) {
	restoreContext := replaceStoreScanSystemContext(storeScanSystemContext{
		WindowsVersion: "Windows 11 24H2",
		WindowsBuild:   "10.0.26200.8655",
		Architecture:   "arm64",
	})
	defer restoreContext()

	store := newTestStoreScanRepository(t)
	result := runTestPipeline(t, store, "S-1-5-21-context", "OpenAI.Codex_abc123", negativeProvider("OpenAI.Codex_abc123", "1.0.0"))

	if result.Scan.WindowsVersion != "Windows 11 24H2" || result.Scan.WindowsBuild != "10.0.26200.8655" || result.Scan.Architecture != "arm64" {
		t.Fatalf("scan context was not recorded: %#v", result.Scan)
	}
	persisted, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), "S-1-5-21-context")
	if err != nil || !ok {
		t.Fatalf("latest scan not persisted: scan=%#v ok=%t err=%v", persisted, ok, err)
	}
	if persisted.Scan.WindowsVersion != "Windows 11 24H2" || persisted.Scan.WindowsBuild != "10.0.26200.8655" || persisted.Scan.Architecture != "arm64" {
		t.Fatalf("persisted scan context was not recorded: %#v", persisted.Scan)
	}
}

func TestOptimizedStoreScanHealthyAggregateNoUpdatesAvoidsExactChecks(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-optimized-no-updates"
	pfns := []string{
		"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe",
		"OpenAI.Codex_2p2nqsd0c76g0",
	}
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, pfns, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{
			pfns[0]: "9N4D0MSMP0PT",
			pfns[1]: "9NCODEX",
		}, nil),
		storeCLIUpdatesCatalogProvider{
			Version: "store-cli-test-v1",
			Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
				counts["store-updates"]++
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "No updates found"}
			},
		},
	})
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 0 || counts["store-update-targeted"] != 0 {
		t.Fatalf("optimized no-update scan ran exact work: counts=%#v", counts)
	}
	if result.Scan.Metrics.ExactChecksPlanned != 0 || result.Scan.Metrics.CommandCountByFamily["store-show"] != 0 {
		t.Fatalf("scan metrics reported exact work for complete no-update aggregate: %#v", result.Scan.Metrics)
	}
	for _, assessment := range result.Assessments {
		if assessment.State != StoreUpdateCurrent {
			t.Fatalf("complete aggregate no-update should project current assessments: %#v", result.Assessments)
		}
	}
}

func TestOptimizedStoreScanAggregateDisplayOnlyPositiveTriggersExactSweep(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-display-only-positive"
	codexPFN := "OpenAI.Codex_2p2nqsd0c76g0"
	otherPFN := "Microsoft.GamingApp_8wekyb3d8bbwe"
	counts := map[string]int{}
	displayOnlyAggregateOutput := strings.Join([]string{
		"Checking for updates...",
		"",
		"Updates available (1 found)",
		"",
		"Store-managed update available",
		"This Store app update can be installed immediately.",
		"Name  Publisher  Version       Date",
		"Codex OpenAI     26.623.3245.0 2026-06-28",
		"",
		"Would you like to install the 1 Store update(s) now? [y/n] (y):",
		"Failed to read input in non-interactive mode.",
	}, "\n")
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{codexPFN, otherPFN}, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{
			codexPFN: "9NCODEX",
			otherPFN: "9MV0B5HZVK9Z",
		}, map[string]string{
			codexPFN: strings.Join([]string{
				"Checking updates...",
				"Checking updates for Codex...",
				"Update available for 'Codex'",
				"Would you like to apply the update? [y/n] (y):",
				"Failed to read input in non-interactive mode.",
			}, "\n"),
			otherPFN: "Already up to date",
		}),
		storeCLIUpdatesCatalogProvider{
			Version: "store-cli-test-v1",
			Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
				counts["store-updates"]++
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: displayOnlyAggregateOutput}
			},
		},
	})
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 2 || counts["store-update-targeted"] != 2 {
		t.Fatalf("display-only aggregate positive should trigger exact checks for product-like PFNs: counts=%#v", counts)
	}
	byPFN := assessmentsByPFN(result.Assessments)
	if got := byPFN[codexPFN]; got.State != StoreUpdateAvailable || !got.ExactActionTargetAvailable || got.StoreProductID != "9NCODEX" {
		t.Fatalf("exact Codex evidence did not become actionable: %#v", got)
	}
	if got := byPFN[otherPFN]; got.State == StoreUpdateAvailable || got.ExactActionTargetAvailable {
		t.Fatalf("display-only aggregate hint manufactured an update for unrelated PFN: %#v", got)
	}
	if result.Scan.Metrics.ExactChecksPlanned != 2 || result.Scan.Metrics.MappingsRefreshed != 2 {
		t.Fatalf("unexpected exact sweep metrics: %#v", result.Scan.Metrics)
	}
}

func TestOptimizedStoreScanVP9AggregatePositiveReusesCachedMapping(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-vp9-reuse"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	base := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	persistPublishedMappingSnapshot(t, store, userSID, pfn, productID, "store-cli-test-v1", base)
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{pfn}, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{pfn: productID}, map[string]string{pfn: "Update available"}),
		positiveAggregateWithoutTargetProvider(pfn, "1.2.13.0", "1.2.20.0"),
	})
	pipeline.Now = fixedPipelineTimes(base.Add(time.Hour), base.Add(time.Hour+time.Second), base.Add(time.Hour+2*time.Second), base.Add(time.Hour+3*time.Second))
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 0 || counts["store-update-targeted"] != 0 {
		t.Fatalf("valid cached mapping should avoid exact Store CLI refresh: counts=%#v", counts)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || !got.ExactActionTargetAvailable {
		t.Fatalf("cached mapping was not reused to make aggregate positive actionable: %#v", got)
	}
	if result.Scan.Metrics.MappingsReused != 1 || result.Scan.Metrics.MappingsRefreshed != 0 {
		t.Fatalf("unexpected mapping reuse metrics: %#v", result.Scan.Metrics)
	}
}

func TestOptimizedStoreScanVP9AggregatePositiveRefreshesExpiredMapping(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-vp9-expired"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	base := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	persistPublishedMappingSnapshot(t, store, userSID, pfn, productID, "store-cli-test-v1", base.Add(-storeMappingFreshnessWindow-24*time.Hour))
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{pfn}, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{pfn: productID}, map[string]string{pfn: "Update available"}),
		positiveAggregateWithoutTargetProvider(pfn, "1.2.13.0", "1.2.20.0"),
	})
	pipeline.Now = fixedPipelineTimes(base, base.Add(time.Second), base.Add(2*time.Second), base.Add(3*time.Second))
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 1 || counts["store-update-targeted"] != 1 {
		t.Fatalf("expired cached mapping should force one exact refresh and state check: counts=%#v", counts)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || !got.ExactActionTargetAvailable {
		t.Fatalf("refreshed mapping did not produce actionable positive: %#v", got)
	}
	if result.Scan.Metrics.MappingsRejected == 0 || result.Scan.Metrics.MappingsRefreshed != 1 {
		t.Fatalf("expired mapping rejection/refresh metrics missing: %#v", result.Scan.Metrics)
	}
}

func TestOptimizedStoreScanProviderVersionInvalidatesCachedMapping(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-vp9-version-change"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	base := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	persistPublishedMappingSnapshot(t, store, userSID, pfn, productID, "store-cli-test-v1", base)
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{pfn}, []StoreCatalogProvider{
		countingStoreCLIExactProviderWithVersion(t, counts, "store-cli-test-v2", map[string]string{pfn: productID}, map[string]string{pfn: "Update available"}),
		positiveAggregateWithoutTargetProvider(pfn, "1.2.13.0", "1.2.20.0"),
	})
	pipeline.Now = fixedPipelineTimes(base.Add(time.Hour), base.Add(time.Hour+time.Second), base.Add(time.Hour+2*time.Second), base.Add(time.Hour+3*time.Second))
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 1 || counts["store-update-targeted"] != 1 {
		t.Fatalf("provider version change should invalidate cached mapping and refresh exact evidence: counts=%#v", counts)
	}
	if result.Assessments[0].State != StoreUpdateAvailable || result.Scan.Metrics.MappingsRejected == 0 {
		t.Fatalf("provider-version refresh did not preserve positive with rejection metric: assessment=%#v metrics=%#v", result.Assessments[0], result.Scan.Metrics)
	}
}

func TestCachedStoreMappingCannotManufactureCurrentOrAvailable(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-mapping-alone"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	base := time.Date(2026, 6, 25, 8, 0, 0, 0, time.UTC)
	persistPublishedMappingSnapshot(t, store, userSID, pfn, productID, "store-cli-test-v1", base)
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{pfn}, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{pfn: productID}, nil),
		incompleteAggregateProvider("aggregate coverage incomplete"),
	})
	pipeline.Now = fixedPipelineTimes(base.Add(time.Hour), base.Add(time.Hour+time.Second), base.Add(time.Hour+2*time.Second))
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 0 || counts["store-update-targeted"] != 0 {
		t.Fatalf("cached mapping without aggregate positive or opted-in state should not schedule exact work: counts=%#v", counts)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown || got.StoreProductID != "" || got.ExactActionTargetAvailable {
		t.Fatalf("cached mapping alone manufactured actionable or current state: %#v", got)
	}
}

func TestOptimizedStoreScanIncompleteAggregateTargetsOnlyOptedInPackage(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-incomplete-targeted"
	optedPFN := "OpenAI.Codex_2p2nqsd0c76g0"
	otherPFN := "Microsoft.GamingApp_8wekyb3d8bbwe"
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, []string{optedPFN, otherPFN}, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{
			optedPFN: "9NCODEX",
			otherPFN: "9MV0B5HZVK9Z",
		}, map[string]string{optedPFN: "No updates found"}),
		incompleteAggregateProvider("Store CLI aggregate update coverage was incomplete"),
	})
	pipeline.PlanningState = &State{AutoUpdatePackages: map[string]bool{
		canonicalStoreAutoUpdateKey(userSID, optedPFN): true,
	}}
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != 1 || counts["store-update-targeted"] != 1 || counts["show:"+otherPFN] != 0 || counts["update:"+otherPFN] != 0 {
		t.Fatalf("incomplete aggregate should target only opted-in package: counts=%#v", counts)
	}
	byPFN := assessmentsByPFN(result.Assessments)
	if got := byPFN[optedPFN]; got.State != StoreUpdateUnknown || !strings.Contains(got.Reason, storeCLIUpdatesProviderID) {
		t.Fatalf("negative targeted check under incomplete required aggregate should remain fail-closed unknown: %#v", got)
	}
	if got := byPFN[otherPFN]; got.State != StoreUpdateUnknown || got.StoreProductID != "" {
		t.Fatalf("non-opted package under incomplete aggregate should remain unknown without mapping/actionability: %#v", got)
	}
	if result.Scan.Metrics.ExactChecksPlanned != 1 || result.Scan.Metrics.CommandCountByFamily["store-show"] != 1 {
		t.Fatalf("unexpected exact planning metrics: %#v", result.Scan.Metrics)
	}
}

func TestDeepStoreScanChecksEveryProductFamily(t *testing.T) {
	restoreAvailable := replacePackageActionManagerAvailable(func(manager string) bool {
		return manager == managerStore
	})
	defer restoreAvailable()

	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-deep-scan"
	pfns := []string{
		"Microsoft.VP9VideoExtensions_8wekyb3d8bbwe",
		"OpenAI.Codex_2p2nqsd0c76g0",
		"Microsoft.GamingApp_8wekyb3d8bbwe",
	}
	counts := map[string]int{}
	pipeline := newMultiFamilyStoreScanPipeline(store, userSID, pfns, []StoreCatalogProvider{
		countingStoreCLIExactProvider(t, counts, map[string]string{
			pfns[0]: "9N4D0MSMP0PT",
			pfns[1]: "9NCODEX",
			pfns[2]: "9MV0B5HZVK9Z",
		}, nil),
		storeCLIUpdatesCatalogProvider{
			Version: "store-cli-test-v1",
			Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "No updates found"}
			},
		},
	})
	pipeline.DeepExactScan = true
	restoreSID := replaceStoreScanSID(userSID)
	defer restoreSID()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["store-show"] != len(pfns) || counts["store-update-targeted"] != len(pfns) {
		t.Fatalf("deep scan should inspect every product-like family: counts=%#v", counts)
	}
	if result.Scan.Mode != StoreScanModeDeep || result.Scan.Metrics.ExactChecksPlanned != len(pfns) {
		t.Fatalf("deep scan mode/metrics not recorded: scan=%#v metrics=%#v", result.Scan, result.Scan.Metrics)
	}
}

func TestStoreScanRetentionPrunesOldGenerationsForUser(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-retention"
	pfn := "OpenAI.Codex_abc123"
	base := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	for index := 0; index < storeScanRetentionRunsUser+5; index++ {
		scan := StoreScanGeneration{
			ScanID:           fmt.Sprintf("scan-retention-%02d", index),
			UserSID:          userSID,
			StartedAt:        base.Add(time.Duration(index) * time.Second),
			CompletedAt:      base.Add(time.Duration(index)*time.Second + time.Millisecond),
			CompletionStatus: StoreScanCompleted,
		}
		identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
		assessment := StorePublishedAssessment{
			StoreUpdateAssessment: StoreUpdateAssessment{
				State:    StoreUpdateCurrent,
				Identity: identity,
				ScanID:   scan.ScanID,
				Reason:   "authoritative negative",
			},
			ObservedAt: scan.CompletedAt,
		}
		if _, err := store.PersistCompletedScanSnapshot(context.Background(), StoreScanSnapshot{
			SchemaVersion: storeScanSchemaVersion,
			Published:     true,
			Scan:          scan,
			Inventory:     testStoreInventory(scan, pfn, "1.0.0"),
			Assessments:   []StorePublishedAssessment{assessment},
		}); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID)
	if err != nil || !ok || snapshot.Scan.ScanID != fmt.Sprintf("scan-retention-%02d", storeScanRetentionRunsUser+4) {
		t.Fatalf("latest published scan not preserved: snapshot=%#v ok=%v err=%v", snapshot, ok, err)
	}
}

func TestStoreScanRejectsTwoSimultaneousRequests(t *testing.T) {
	store := newTestStoreScanRepository(t)
	block := make(chan struct{})
	started := make(chan struct{})
	userSID := "S-1-5-21-concurrent"
	pipeline := newTestStoreScanPipeline(store, userSID, "OpenAI.Codex_abc123", positiveProvider("OpenAI.Codex_abc123", "1.0.0", "1.1.0"))
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		close(started)
		<-block
		return testStoreInventory(scan, "OpenAI.Codex_abc123", "1.0.0"), CommandResult{OK: true, Command: "inventory"}
	}}
	done := make(chan error)
	go func() {
		_, err := pipeline.Run(context.Background())
		done <- err
	}()
	<-started
	if _, err := pipeline.Run(context.Background()); !errors.Is(err, errStoreScanAlreadyRunning) {
		t.Fatalf("second scan error=%v, want errStoreScanAlreadyRunning", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("first scan failed: %v", err)
	}
}

func TestStoreScanOlderCompletionDoesNotPublishOverNewerScan(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-order"
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pfn := "OpenAI.Codex_abc123"
	base := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	older := newTestStoreScanPipeline(store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	older.Now = fixedPipelineTimes(base, base.Add(3*time.Second))
	older.NewScanID = func(time.Time) string { return "older-scan" }
	newer := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	newer.Now = fixedPipelineTimes(base.Add(time.Second), base.Add(2*time.Second))
	newer.NewScanID = func(time.Time) string { return "newer-scan" }
	if result, err := newer.Run(context.Background()); err != nil || !result.Published {
		t.Fatalf("newer scan publish failed: result=%#v err=%v", result, err)
	}
	if result, err := older.Run(context.Background()); err != nil || result.Published {
		t.Fatalf("older stale scan should persist but not publish: result=%#v err=%v", result, err)
	}
	snapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID)
	if err != nil || !ok || snapshot.Scan.ScanID != "newer-scan" {
		t.Fatalf("latest published scan=%#v ok=%v err=%v", snapshot.Scan, ok, err)
	}
}

func TestStoreScanCrossUserEvidenceBecomesUnknown(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-user-a"
	pfn := "OpenAI.Codex_abc123"
	provider := fakeCatalogProvider{id: "wrong-user", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		other := StoreInstalledIdentity{UserSID: "S-1-5-21-user-b", PackageFamilyName: pfn}
		target := exactStoreTarget(other, StoreProviderIdentity{ID: "wrong-user"})
		return StoreCatalogProviderRun{
			Provider:     StoreProviderIdentity{ID: "wrong-user"},
			StartedAt:    scan.StartedAt,
			CompletedAt:  scan.StartedAt.Add(time.Second),
			Health:       StoreProviderHealthy,
			Observations: []StoreProviderObservation{storeObservation(other, scan, StoreProviderIdentity{ID: "wrong-user"}, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target)},
		}
	}}
	result := runTestPipeline(t, store, userSID, pfn, provider)
	if got := result.Assessments[0]; got.State != StoreUpdateUnknown {
		t.Fatalf("wrong-user evidence state=%s, want unknown; assessment=%#v", got.State, got)
	}
	if result.ProviderRuns[1].Health != StoreProviderFailed {
		t.Fatalf("wrong-user provider run health=%s", result.ProviderRuns[1].Health)
	}
}

func TestStoreScanProviderPartialFailureIsUnknown(t *testing.T) {
	store := newTestStoreScanRepository(t)
	result := runTestPipeline(t, store, "S-1-5-21-partial", "OpenAI.Codex_abc123", failingProvider("catalog unavailable"))
	if got := result.Assessments[0]; got.State != StoreUpdateUnknown || got.Stale {
		t.Fatalf("partial provider assessment=%#v", got)
	}
}

func TestStoreScanOptionalProviderFailureDoesNotAllowCurrent(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-optional-failure"
	pfn := "OpenAI.Codex_abc123"
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: storeCLIUpdatesProviderID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{
			Provider:    StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI},
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "aggregate update output mentioned updates without exact PFN/Product ID associations",
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown {
		t.Fatalf("optional provider failure without package rows produced %s, want unknown; assessment=%#v", got.State, got)
	}
	if !strings.Contains(got.Reason, storeCLIUpdatesProviderID) {
		t.Fatalf("expected provider failure reason to cite %s, got %#v", storeCLIUpdatesProviderID, got)
	}
}

func TestStoreScanRequiredProviderPackageFailurePreventsOtherCurrent(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-required-package-failure"
	healthyPFN := "OpenAI.Codex_abc123"
	failedPFN := "Vendor.Broken_abc123"
	providerID := StoreProviderIdentity{ID: "required-exact", Name: "Required exact provider"}
	pipeline := newTestStoreScanPipeline(store, userSID, healthyPFN, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		healthyIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: healthyPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(healthyIdentity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				storeObservation(failedIdentity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, healthyPFN, "1.0.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	for _, assessment := range result.Assessments {
		if assessment.State == StoreUpdateCurrent {
			t.Fatalf("incomplete required provider scan emitted current assessment: %#v", assessment)
		}
	}
}

func TestStoreScanOptionalExactFailureDoesNotBlockUnrelatedCurrentWithAggregateCoverage(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-optional-exact-unrelated"
	currentPFN := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	failedPFN := "Vendor.Broken_abc123"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, currentPFN, fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		currentIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: currentPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    exactProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(currentIdentity, scan, exactProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				storeObservation(failedIdentity, scan, exactProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: currentIdentity,
				ProductID:         "9N4D0MSMP0PT",
				Provider:          exactProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		currentIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: currentPFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(currentIdentity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				storeObservation(failedIdentity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, currentPFN, "1.2.20.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanCompleted {
		t.Fatalf("scan status=%s, want completed; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	byPFN := map[string]StorePublishedAssessment{}
	for _, assessment := range result.Assessments {
		byPFN[assessment.Identity.PackageFamilyName] = assessment
	}
	if got := byPFN[currentPFN]; got.State != StoreUpdateCurrent || got.StoreProductID != "9N4D0MSMP0PT" {
		t.Fatalf("healthy exact negative plus aggregate coverage should be current: %#v", got)
	}
	if got := byPFN[failedPFN]; got.State != StoreUpdateUnknown || !strings.Contains(got.Reason, "store-cli-exact") {
		t.Fatalf("package with incomplete exact evidence should remain unknown: %#v", got)
	}
}

func TestStoreScanFreshPositiveSurvivesUnrelatedIncompletePackage(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-positive-unrelated-failure"
	updatePFN := "OpenAI.Codex_abc123"
	failedPFN := "Vendor.Broken_abc123"
	providerID := StoreProviderIdentity{ID: "required-exact-positive", Name: "Required exact provider"}
	pipeline := newTestStoreScanPipeline(store, userSID, updatePFN, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		updateIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: updatePFN}
		failedIdentity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: failedPFN}
		target := &ExactStoreUpdateTarget{Identity: updateIdentity, Provider: providerID, ProductID: "9NCODEX", Verified: true, VerifiedBy: providerID.ID, VerifiedAt: scan.StartedAt.Add(time.Second)}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(updateIdentity, scan, providerID, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
				storeObservation(failedIdentity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	pipeline.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, updatePFN, "1.0.0")
		broken := testStoreInventory(scan, failedPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	byPFN := map[string]StorePublishedAssessment{}
	for _, assessment := range result.Assessments {
		byPFN[assessment.Identity.PackageFamilyName] = assessment
	}
	if got := byPFN[updatePFN]; got.State != StoreUpdateAvailable || got.Stale || got.StoreProductID != "9NCODEX" || !got.ExactActionTargetAvailable {
		t.Fatalf("fresh exact positive should stay available despite unrelated package failure: %#v", got)
	}
	if got := byPFN[failedPFN]; got.State != StoreUpdateUnknown || got.Stale {
		t.Fatalf("failed package should remain unknown: %#v", got)
	}
}

func TestStoreScanOptionalExactPositiveSurvivesRequiredProviderIncompleteForSamePackage(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-optional-positive-required-incomplete"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			target := &ExactStoreUpdateTarget{Identity: identity, Provider: exactProvider, ProductID: productID, UpdateID: pfn, Verified: true, VerifiedBy: exactProvider.ID, VerifiedAt: scan.StartedAt.Add(time.Second)}
			return StoreCatalogProviderRun{
				Provider:    exactProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderHealthy,
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, exactProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.2.13.0", "1.2.20.0", target),
				},
				Mappings: []VerifiedStoreIdentityMapping{{
					InstalledIdentity: identity,
					ProductID:         productID,
					Provider:          exactProvider,
					ScanID:            scan.ScanID,
					VerifiedAt:        target.VerifiedAt,
					Evidence:          "store show exact PFN/Product ID association",
				}},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "store updates timed out",
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, aggregateProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.13.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || got.UpdateID != pfn || !got.ExactActionTargetAvailable {
		t.Fatalf("optional exact positive did not survive required incomplete provider: %#v", got)
	}
	if got.Stale {
		t.Fatalf("fresh exact positive should not be marked stale: %#v", got)
	}
}

func TestStoreScanWingetExactPositiveSurvivesRequiredProviderIncompleteForSamePackage(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-winget-positive-required-incomplete"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			return StoreCatalogProviderRun{
				Provider:    aggregateProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderIncomplete,
				Error:       "store updates timed out",
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, aggregateProvider, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.13.0", "", nil),
				},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: wingetProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: wingetProvider, ProductID: productID, UpdateID: pfn, Verified: true, VerifiedBy: wingetProvider.ID, VerifiedAt: scan.StartedAt.Add(2 * time.Second)}
		return StoreCatalogProviderRun{
			Provider:    wingetProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, wingetProvider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.2.13.0", "1.2.20.0", target),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          wingetProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        target.VerifiedAt,
				Evidence:          "winget msstore exact PFN/Product ID association",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.CompletionStatus != StoreScanIncomplete {
		t.Fatalf("scan status=%s, want incomplete; providerRuns=%#v", result.Scan.CompletionStatus, result.ProviderRuns)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || got.StoreProductID != productID || got.UpdateID != pfn || !got.ExactActionTargetAvailable {
		t.Fatalf("WinGet exact positive did not survive required incomplete provider: %#v", got)
	}
	if got.Stale {
		t.Fatalf("fresh exact positive should not be marked stale: %#v", got)
	}
}

func TestStoreScanWingetNonExactPositivePreventsCurrent(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-winget-unresolved-positive"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	wingetProvider := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn,
		fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
			identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
			return StoreCatalogProviderRun{
				Provider:    aggregateProvider,
				StartedAt:   scan.StartedAt,
				CompletedAt: scan.StartedAt.Add(time.Second),
				Health:      StoreProviderHealthy,
				Observations: []StoreProviderObservation{
					storeObservation(identity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
				},
			}
		}},
	)
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: wingetProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{
			Provider:    wingetProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderIncomplete,
			Error:       "ignored 1 WinGet msstore update row(s) without exact installed PFN association",
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown || !strings.Contains(got.Reason, wingetMSStoreExactProviderID) {
		t.Fatalf("unresolved WinGet Store positive must prevent current: %#v", got)
	}
}

func TestStoreScanPublishesVerifiedProductIDForAuthoritativeNegative(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-current-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	exactProvider := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	aggregateProvider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: exactProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    exactProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, exactProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          exactProvider,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: aggregateProvider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    aggregateProvider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(2 * time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, aggregateProvider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateCurrent || got.StoreProductID != productID {
		t.Fatalf("verified Product ID should be published for current assessment: %#v", got)
	}
	if got.ExactActionTargetAvailable {
		t.Fatalf("current assessment must not become updatable just because Product ID is known: %#v", got)
	}
	persistedSnapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(persistedSnapshot.Assessments) != 1 || persistedSnapshot.Assessments[0].StoreProductID != productID || persistedSnapshot.Assessments[0].ExactActionTargetAvailable {
		t.Fatalf("persisted assessment lost verified Product ID or changed target availability: %#v", persistedSnapshot.Assessments)
	}
}

func TestStoreScanPublishesVerifiedProductIDForUnknownIncompleteAssessment(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-unknown-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	productID := "9N4D0MSMP0PT"
	providerID := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         productID,
				Provider:          providerID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association before update-state failure",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateUnknown || got.StoreProductID != productID {
		t.Fatalf("verified Product ID should be published for unknown assessment: %#v", got)
	}
	if got.ExactActionTargetAvailable {
		t.Fatalf("unknown assessment must not become updatable just because Product ID is known: %#v", got)
	}
}

func TestStoreScanDoesNotPublishConflictingVerifiedProductIDs(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-conflicting-product-id"
	pfn := "Microsoft.VP9VideoExtensions_8wekyb3d8bbwe"
	providerID := StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
	otherProviderID := StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(time.Second)
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.2.20.0", "", nil),
			},
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         "9N4D0MSMP0PT",
				Provider:          providerID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "store show exact PFN/Product ID association",
			}},
		}
	}})
	pipeline.CatalogProviders = append(pipeline.CatalogProviders, fakeCatalogProvider{id: otherProviderID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		verifiedAt := scan.StartedAt.Add(2 * time.Second)
		return StoreCatalogProviderRun{
			Provider:    otherProviderID,
			StartedAt:   scan.StartedAt,
			CompletedAt: verifiedAt,
			Health:      StoreProviderHealthy,
			Mappings: []VerifiedStoreIdentityMapping{{
				InstalledIdentity: identity,
				ProductID:         "9NCONFLICT",
				Provider:          otherProviderID,
				ScanID:            scan.ScanID,
				VerifiedAt:        verifiedAt,
				Evidence:          "conflicting exact PFN/Product ID association",
			}},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()

	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.StoreProductID != "" {
		t.Fatalf("conflicting verified Product IDs must not be published: %#v", got)
	}
}

func TestStoreScanPositiveHysteresisRetainsUpdateOnIncompleteScan(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-hysteresis"
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}
	second := runTestPipeline(t, store, userSID, pfn, failingProvider("catalog timeout"))
	if got := second.Assessments[0]; got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("hysteresis assessment=%#v", got)
	}
}

func TestStoreScanHealthyRetractionStopsPositiveHysteresis(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-retraction"
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-retraction", Name: "Catalog retraction"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
			},
		}
	}})
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateCurrent || got.Stale {
		t.Fatalf("complete healthy retraction should clear stale positive as current: %#v", got)
	}
}

func TestStoreScanIncompleteRetractionDoesNotClearPreviousPositive(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-incomplete-retraction"
	pfn := "OpenAI.Codex_abc123"
	otherPFN := "Vendor.Broken_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-retraction", Name: "Catalog retraction"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		other := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: otherPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
				storeObservation(other, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	second.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, pfn, "1.0.0")
		broken := testStoreInventory(scan, otherPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("incomplete retraction should retain stale positive: %#v", got)
	}
}

func TestStoreScanIncompleteInapplicableDoesNotClearPreviousPositive(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-incomplete-inapplicable"
	pfn := "OpenAI.Codex_abc123"
	otherPFN := "Vendor.Broken_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if first.Assessments[0].State != StoreUpdateAvailable || first.Assessments[0].Stale {
		t.Fatalf("initial positive assessment=%#v", first.Assessments[0])
	}

	providerID := StoreProviderIdentity{ID: "catalog-inapplicable", Name: "Catalog inapplicable"}
	second := newTestStoreScanPipeline(store, userSID, pfn, fakeCatalogProvider{id: providerID.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		other := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: otherPFN}
		return StoreCatalogProviderRun{
			Provider:    providerID,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, providerID, StoreProviderHealthy, StoreObservationNewerCatalogNoApplicableInstaller, "1.0.0", "1.2.0", nil),
				storeObservation(other, scan, providerID, StoreProviderIncomplete, StoreObservationIncompleteResult, "2.0.0", "", nil),
			},
		}
	}})
	second.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := testStoreInventory(scan, pfn, "1.0.0")
		broken := testStoreInventory(scan, otherPFN, "2.0.0")
		inventory.Records = append(inventory.Records, broken.Records...)
		inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
		return inventory, CommandResult{OK: true, Command: "fake inventory"}
	}}
	restore := replaceStoreScanSID(userSID)
	defer restore()
	result, err := second.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := result.Assessments[0]
	if got.State != StoreUpdateAvailable || !got.Stale || got.AvailableVersion != "1.1.0" {
		t.Fatalf("incomplete inapplicable evidence should retain stale positive: %#v", got)
	}
}

func TestStoreScanConflictResolution(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-conflict"
	pfn := "OpenAI.Codex_abc123"
	result := runTestPipeline(t, store, userSID, pfn, positiveAndNegativeProvider(pfn))
	if got := result.Assessments[0]; got.State != StoreUpdateConflict {
		t.Fatalf("conflict assessment=%#v", got)
	}
}

func TestStoreScanCancellationDoesNotPublish(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-cancel"
	pfn := "OpenAI.Codex_abc123"
	ctx, cancel := context.WithCancel(context.Background())
	provider := fakeCatalogProvider{id: "blocking", fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		cancel()
		<-ctx.Done()
		return StoreCatalogProviderRun{Provider: StoreProviderIdentity{ID: "blocking"}, Health: StoreProviderFailed, Error: ctx.Err().Error()}
	}}
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, provider)
	if _, err := pipeline.Run(ctx); err == nil {
		t.Fatal("expected cancelled scan to fail before publish")
	}
	if snapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID); err != nil || ok {
		t.Fatalf("cancelled scan published snapshot=%#v ok=%t err=%v", snapshot, ok, err)
	}
}

func TestConfiguredStoreProviderTimeoutFromEnvironment(t *testing.T) {
	t.Setenv(storeProviderTimeoutEnv, "17")
	if got := configuredStoreProviderTimeout(); got != 17*time.Second {
		t.Fatalf("timeout=%s, want 17s", got)
	}
	t.Setenv(storeProviderTimeoutEnv, "0")
	if got := configuredStoreProviderTimeout(); got != defaultStoreProviderTimeout {
		t.Fatalf("zero timeout=%s, want default %s", got, defaultStoreProviderTimeout)
	}
	t.Setenv(storeProviderTimeoutEnv, "not-a-number")
	if got := configuredStoreProviderTimeout(); got != defaultStoreProviderTimeout {
		t.Fatalf("invalid timeout=%s, want default %s", got, defaultStoreProviderTimeout)
	}
}

func TestStoreScanPreviousGenerationFallbackOnFatalInventoryFailure(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-fallback"
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pfn := "OpenAI.Codex_abc123"
	first := runTestPipeline(t, store, userSID, pfn, positiveProvider(pfn, "1.0.0", "1.1.0"))
	if !first.Published {
		t.Fatal("initial scan did not publish")
	}
	failed := newTestStoreScanPipeline(store, userSID, pfn, negativeProvider(pfn, "1.0.0"))
	failed.NewScanID = func(time.Time) string { return "failed-inventory-scan" }
	failed.InventoryProvider = fakeInventoryProvider{fn: func(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
		inventory := StorePackagedAppInventory{Scan: scan, Partial: true, Errors: []string{"inventory failed"}}
		return inventory, CommandResult{Command: "inventory", Code: 1, Stderr: "inventory failed"}
	}}
	result, err := failed.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Published {
		t.Fatalf("fatal inventory scan should not publish: %#v", result)
	}
	snapshot, ok, err := store.LoadLatestPublishedSnapshot(context.Background(), userSID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(snapshot.Assessments) != 1 || snapshot.Assessments[0].State != StoreUpdateAvailable || snapshot.Assessments[0].ScanID != first.Scan.ScanID {
		t.Fatalf("previous generation fallback broken: %#v", snapshot.Assessments)
	}
}

func TestStoreScanInapplicableAndUnresolvedIdentity(t *testing.T) {
	store := newTestStoreScanRepository(t)
	userSID := "S-1-5-21-states"
	pfn := "OpenAI.Codex_abc123"
	inapplicable := runTestPipeline(t, store, userSID, pfn, inapplicableProvider(pfn))
	if inapplicable.Assessments[0].State != StoreUpdateInapplicable {
		t.Fatalf("inapplicable assessment=%#v", inapplicable.Assessments[0])
	}
	unresolved := runTestPipeline(t, store, userSID, pfn, positiveWithoutTargetProvider(pfn))
	if unresolved.Assessments[0].State != StoreUpdateUnknown || !strings.Contains(unresolved.Assessments[0].Reason, "no exact verified target") {
		t.Fatalf("unresolved target assessment=%#v", unresolved.Assessments[0])
	}
}

func TestStoreTransactionalScanInventoryAdapter(t *testing.T) {
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	now := storeScanNow()
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: false,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "scan-adapter",
			Reason:           "fresh exact positive update evidence",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
		},
		ObservedAt:                 now,
		StoreProductID:             "9NCODEX",
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan: StoreScanGeneration{
			ScanID:           "scan-adapter",
			UserSID:          "S-1-5-21-adapter",
			CompletedAt:      now,
			CompletionStatus: StoreScanCompleted,
		},
		Assessments: []StorePublishedAssessment{assessment},
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, snapshot, nil, nil)
	got := adapted.Packages[0]
	if got.UpdateState != string(StoreUpdateAvailable) || !got.UpdateAvailable || !got.ExactActionTargetAvailable || got.ID != pfn || got.StoreProductID != "9NCODEX" {
		t.Fatalf("published assessment was not adapted into package response: %#v", got)
	}
}

func TestStoreTransactionalScanInventoryAdapterProjectsCapabilities(t *testing.T) {
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	now := storeScanNow()
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: false,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "scan-adapter-kind",
			Reason:           "fresh exact positive update evidence",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
			Target: &ExactStoreUpdateTarget{
				Identity:   StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
				Provider:   StoreProviderIdentity{ID: managerStore, Backend: backendStoreCLI},
				UpdateID:   pfn,
				Verified:   true,
				VerifiedBy: "test",
				VerifiedAt: now,
			},
		},
		ObservedAt:                 now,
		UpdateID:                   pfn,
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan: StoreScanGeneration{
			ScanID:           "scan-adapter-kind",
			UserSID:          "S-1-5-21-adapter",
			StartedAt:        now.Add(-time.Second),
			CompletedAt:      now,
			CompletionStatus: StoreScanCompleted,
		},
		Assessments: []StorePublishedAssessment{assessment},
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, snapshot, nil, nil)
	got := adapted.Packages[0]
	if !got.PreferenceEligible || !got.CanUpdateNow || got.CannotUpdateReason != "" || got.ExactTargetKind != "update_id" {
		t.Fatalf("capabilities were not projected for update-ID-only Store target: %#v", got)
	}
}

func TestStoreTransactionalScanInventoryAdapterProjectsConflictAsNonActionable(t *testing.T) {
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	now := storeScanNow()
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: true,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateConflict,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "scan-conflict",
			Reason:           "healthy providers returned conflicting product_id values",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
		},
		ObservedAt:    now,
		Applicability: "unknown",
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan: StoreScanGeneration{
			ScanID:           "scan-conflict",
			UserSID:          "S-1-5-21-adapter",
			StartedAt:        now.Add(-time.Second),
			CompletedAt:      now,
			CompletionStatus: StoreScanCompleted,
		},
		Assessments: []StorePublishedAssessment{assessment},
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, snapshot, nil, nil)
	got := adapted.Packages[0]
	if !got.PreferenceEligible || got.CanUpdateNow || got.ExactTargetKind != "none" || !strings.Contains(got.CannotUpdateReason, "conflict") {
		t.Fatalf("conflict capabilities should be preference-only and non-actionable: %#v", got)
	}
}

func TestPublishedStoreAssessmentStalePositiveIsDiagnosticOnly(t *testing.T) {
	state := defaultState()
	pfn := "OpenAI.Codex_abc123"
	now := storeScanNow()
	inventory := Inventory{PackageLookup: PackageLookup{Packages: []Package{{
		Key:             packageKey(managerStore, pfn),
		Manager:         managerStore,
		ID:              pfn,
		Name:            "Codex",
		Version:         "1.0.0",
		Source:          sourceNativeAppX,
		UpdateSupported: true,
	}}}}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateAvailable,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: pfn},
			ScanID:           "previous-scan",
			Reason:           "retained previous positive update because the latest scan was incomplete",
			InstalledVersion: "1.0.0",
			AvailableVersion: "1.1.0",
		},
		ObservedAt:                 now,
		Stale:                      true,
		StoreProductID:             "9NCODEX",
		ExactActionTargetAvailable: true,
		Applicability:              "applicable",
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan: StoreScanGeneration{
			ScanID:           "previous-scan",
			UserSID:          "S-1-5-21-adapter",
			CompletedAt:      now,
			CompletionStatus: StoreScanCompleted,
		},
		Assessments: []StorePublishedAssessment{assessment},
	}
	adapted := applyPublishedStoreAssessmentsToInventory(state, inventory, snapshot, nil, nil)
	got := adapted.Packages[0]
	if got.UpdateState != string(StoreUpdateAvailable) || !got.Stale {
		t.Fatalf("stale assessment state was not preserved for diagnostics: %#v", got)
	}
	if got.UpdateAvailable || got.UpdateSupported || got.AvailableVersion != "" || got.OfferedVersion != "1.1.0" {
		t.Fatalf("stale assessment must not be exposed as an available update: %#v", got)
	}
}

func TestPublishedStoreAssessmentUsesFriendlyPFNPresentationFallback(t *testing.T) {
	state := defaultState()
	pkg := packageFromPublishedStoreAssessment(state, StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateCurrent,
			Identity:         StoreInstalledIdentity{UserSID: "S-1-5-21-adapter", PackageFamilyName: "19568ShareX.ShareX_egrzcvs15399j"},
			ScanID:           "scan-friendly-name",
			Reason:           "authoritative negative",
			InstalledVersion: "20.2.0.0",
		},
		ObservedAt: time.Date(2026, 6, 22, 17, 0, 0, 0, time.UTC),
	}, StorePackagedAppFamily{}, nil)

	if pkg.Name != "ShareX" {
		t.Fatalf("package assessment name = %q, want ShareX", pkg.Name)
	}
	if pkg.InstalledPackageFamilyName != "19568ShareX.ShareX_egrzcvs15399j" {
		t.Fatalf("canonical PFN was not preserved: %#v", pkg)
	}
}

func newTestStoreScanRepository(t *testing.T) StoreScanRepository {
	t.Helper()
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	store, err := openStoreScanFileRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestStoreScanPipeline(store StoreScanRepository, userSID, pfn string, provider StoreCatalogProvider) *StoreScanPipeline {
	return &StoreScanPipeline{
		Repository:        store,
		InventoryProvider: fakeInventoryProvider{inventory: testStoreInventory(StoreScanGeneration{ScanID: "placeholder", UserSID: userSID, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanCompleted}, pfn, "1.0.0")},
		CatalogProviders:  []StoreCatalogProvider{provider},
		ProviderTimeout:   500 * time.Millisecond,
		Now:               fixedPipelineTimes(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 2, 0, time.UTC)),
		NewScanID: func(now time.Time) string {
			return fmt.Sprintf("scan-%s%s-%06d", strings.ReplaceAll(pfn, ".", "-"), fmtTimeForID(now), atomic.AddInt64(&testStoreScanIDCounter, 1))
		},
	}
}

func newMultiFamilyStoreScanPipeline(store StoreScanRepository, userSID string, pfns []string, providers []StoreCatalogProvider) *StoreScanPipeline {
	scan := StoreScanGeneration{ScanID: "placeholder", UserSID: userSID, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), CompletionStatus: StoreScanCompleted}
	inventory := StorePackagedAppInventory{Scan: scan}
	for _, pfn := range pfns {
		next := testStoreInventory(scan, pfn, "1.0.0")
		inventory.Records = append(inventory.Records, next.Records...)
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	return &StoreScanPipeline{
		Repository:        store,
		InventoryProvider: fakeInventoryProvider{inventory: inventory},
		CatalogProviders:  providers,
		ProviderTimeout:   500 * time.Millisecond,
		Now:               fixedPipelineTimes(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 1, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 2, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 3, 0, time.UTC), time.Date(2026, 6, 21, 12, 0, 4, 0, time.UTC)),
		NewScanID: func(now time.Time) string {
			return fmt.Sprintf("scan-multi-%s-%06d", fmtTimeForID(now), atomic.AddInt64(&testStoreScanIDCounter, 1))
		},
	}
}

func runTestPipeline(t *testing.T, store StoreScanRepository, userSID, pfn string, provider StoreCatalogProvider) StoreScanResult {
	t.Helper()
	restore := replaceStoreScanSID(userSID)
	defer restore()
	pipeline := newTestStoreScanPipeline(store, userSID, pfn, provider)
	result, err := pipeline.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type fakeInventoryProvider struct {
	inventory StorePackagedAppInventory
	result    CommandResult
	fn        func(context.Context, StoreScanGeneration) (StorePackagedAppInventory, CommandResult)
}

type failingPreviousStoreScanRepository struct {
	err          error
	persistCalls int
}

func (repo *failingPreviousStoreScanRepository) PersistCompletedScanSnapshot(context.Context, StoreScanSnapshot) (bool, error) {
	repo.persistCalls++
	return true, nil
}

func (repo *failingPreviousStoreScanRepository) LoadLatestPublishedSnapshot(context.Context, string) (StoreScanSnapshot, bool, error) {
	return StoreScanSnapshot{}, false, repo.err
}

func (repo *failingPreviousStoreScanRepository) LoadPreviousSnapshot(context.Context, string, StoreScanGeneration) (StoreScanSnapshot, bool, error) {
	return StoreScanSnapshot{}, false, nil
}

func (repo *failingPreviousStoreScanRepository) Close() error {
	return nil
}

func (provider fakeInventoryProvider) Inventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, CommandResult) {
	if provider.fn != nil {
		return provider.fn(ctx, scan)
	}
	inventory := provider.inventory
	inventory.Scan = scan
	inventory.Scan.CompletedAt = scan.StartedAt.Add(time.Second)
	inventory.Scan.CompletionStatus = StoreScanCompleted
	for index := range inventory.Records {
		inventory.Records[index].UserSID = scan.UserSID
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	result := provider.result
	if result.Command == "" {
		result = CommandResult{OK: true, Command: "fake inventory"}
	}
	return inventory, result
}

type fakeCatalogProvider struct {
	id string
	fn func(context.Context, StoreScanGeneration, []StorePackagedAppFamily) StoreCatalogProviderRun
}

func (provider fakeCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: provider.id, Name: provider.id, Backend: "fake"}
}

func (provider fakeCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	return provider.fn(ctx, scan, families)
}

func testStoreInventory(scan StoreScanGeneration, pfn, version string) StorePackagedAppInventory {
	if scan.CompletedAt.IsZero() {
		scan.CompletedAt = scan.StartedAt.Add(time.Second)
	}
	scan.CompletionStatus = StoreScanCompleted
	record := StorePackagedAppRecord{
		UserSID:               scan.UserSID,
		PackageFamilyName:     pfn,
		PackageFullName:       pfn + "_" + version + "_x64__abc123",
		IdentityName:          strings.Split(pfn, "_")[0],
		Version:               parseTestStoreVersion(version),
		ProcessorArchitecture: "x64",
		PackageType:           "Main",
		Classification:        storePackageClassMain,
		DisplayName:           "Codex",
		Status:                StorePackageStatus{OK: true},
	}
	inventory := StorePackagedAppInventory{
		Scan:    scan,
		Records: []StorePackagedAppRecord{record},
	}
	inventory.Families = groupStorePackagedAppFamilies(inventory.Records)
	return inventory
}

func countingStoreCLIExactProvider(t *testing.T, counts map[string]int, productIDs map[string]string, updateOutputs map[string]string) StoreCatalogProvider {
	return countingStoreCLIExactProviderWithVersion(t, counts, "store-cli-test-v1", productIDs, updateOutputs)
}

func countingStoreCLIExactProviderWithVersion(t *testing.T, counts map[string]int, version string, productIDs map[string]string, updateOutputs map[string]string) StoreCatalogProvider {
	t.Helper()
	return storeCLIExactCatalogProvider{
		Version:     version,
		Concurrency: 1,
		Run: func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			command := strings.Join(args, " ")
			switch {
			case storeCLICommandContains(args, "show"):
				pfn := storeCommandTargetFromArgs(args)
				counts["store-show"]++
				counts["show:"+pfn]++
				productID := productIDs[pfn]
				if productID == "" {
					t.Fatalf("unexpected store show target %q in command %q", pfn, command)
				}
				return CommandResult{OK: true, Command: command, Stdout: "Product ID : " + productID + "\nPFN : " + pfn}
			case storeCLICommandContains(args, "update"):
				pfn := storeCommandTargetFromArgs(args)
				counts["store-update-targeted"]++
				counts["update:"+pfn]++
				output := updateOutputs[pfn]
				if output == "" {
					output = "Already up to date"
				}
				return CommandResult{OK: true, Command: command, Stdout: output}
			default:
				t.Fatalf("unexpected Store CLI exact command: %q", command)
			}
			return CommandResult{Command: command, Code: 1, Stderr: "unexpected command"}
		},
	}
}

func storeCLICommandContains(args []string, verb string) bool {
	for _, arg := range args {
		if strings.EqualFold(arg, verb) {
			return true
		}
	}
	return false
}

func storeCommandTargetFromArgs(args []string) string {
	for i, arg := range args {
		if (strings.EqualFold(arg, "show") || strings.EqualFold(arg, "update") || strings.EqualFold(arg, "upgrade")) && i+1 < len(args) {
			return args[i+1]
		}
	}
	return packageActionTargetFromArgs(args)
}

func positiveAggregateWithoutTargetProvider(pfn, installed, available string) StoreCatalogProvider {
	provider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	return fakeCatalogProvider{id: provider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{
			Provider:    provider,
			Version:     "store-cli-test-v1",
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, installed, available, nil),
			},
		}
	}}
}

func incompleteAggregateProvider(message string) StoreCatalogProvider {
	provider := StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
	return fakeCatalogProvider{id: provider.ID, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{
			Provider:    provider,
			Version:     "store-cli-test-v1",
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderIncomplete,
			Error:       message,
		}
	}}
}

func persistPublishedMappingSnapshot(t *testing.T, store StoreScanRepository, userSID, pfn, productID, providerVersion string, verifiedAt time.Time) {
	t.Helper()
	scan := StoreScanGeneration{
		ScanID:           "mapping-seed-" + shortHash(userSID+pfn+verifiedAt.String()),
		UserSID:          userSID,
		StartedAt:        verifiedAt.Add(-time.Second),
		CompletedAt:      verifiedAt,
		ProviderVersions: map[string]string{storeCLIExactProviderID: providerVersion},
		ProviderHealth:   map[string]StoreProviderHealth{storeCLIExactProviderID: StoreProviderHealthy},
		CompletionStatus: StoreScanCompleted,
	}
	inventory := testStoreInventory(scan, pfn, "1.0.0")
	family := inventory.Families[0]
	mapping := VerifiedStoreIdentityMapping{
		InstalledIdentity:     family.Identity,
		ProductID:             productID,
		Provider:              StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI},
		ScanID:                scan.ScanID,
		VerifiedAt:            verifiedAt,
		Evidence:              "store show exact PFN/Product ID association",
		IdentityName:          family.Primary.IdentityName,
		PublisherID:           family.Primary.PublisherID,
		ProcessorArchitecture: family.Primary.ProcessorArchitecture,
		ProductLike:           family.ProductLike,
		ProviderVersion:       providerVersion,
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan:          scan,
		Inventory:     inventory,
		ProviderRuns: []StoreCatalogProviderRun{{
			Provider:    mapping.Provider,
			Version:     providerVersion,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.CompletedAt,
			Health:      StoreProviderHealthy,
			Mappings:    []VerifiedStoreIdentityMapping{mapping},
		}},
	}
	if _, err := store.PersistCompletedScanSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func assessmentsByPFN(assessments []StorePublishedAssessment) map[string]StorePublishedAssessment {
	byPFN := map[string]StorePublishedAssessment{}
	for _, assessment := range assessments {
		byPFN[assessment.Identity.PackageFamilyName] = assessment
	}
	return byPFN
}

func parseTestStoreVersion(value string) StorePackageVersion {
	parts := versionParts(value)
	version := StorePackageVersion{}
	if len(parts) > 0 {
		version.Major = uint16(parts[0])
	}
	if len(parts) > 1 {
		version.Minor = uint16(parts[1])
	}
	if len(parts) > 2 {
		version.Build = uint16(parts[2])
	}
	if len(parts) > 3 {
		version.Revision = uint16(parts[3])
	}
	return version
}

func positiveProvider(pfn, installed, available string) StoreCatalogProvider {
	id := "catalog-positive"
	provider := StoreProviderIdentity{ID: id, Name: id, Backend: "fake"}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: provider, ProductID: "9NCODEX", Verified: true, VerifiedBy: id, VerifiedAt: scan.StartedAt.Add(time.Second)}
		mapping := VerifiedStoreIdentityMapping{InstalledIdentity: identity, ProductID: "9NCODEX", Provider: provider, ScanID: scan.ScanID, VerifiedAt: target.VerifiedAt, Evidence: "fake exact PFN/product mapping"}
		return StoreCatalogProviderRun{
			Provider:    provider,
			StartedAt:   scan.StartedAt,
			CompletedAt: scan.StartedAt.Add(time.Second),
			Health:      StoreProviderHealthy,
			Observations: []StoreProviderObservation{
				storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, installed, available, target),
			},
			Mappings: []VerifiedStoreIdentityMapping{mapping},
		}
	}}
}

func negativeProvider(pfn, installed string) StoreCatalogProvider {
	id := "catalog-negative"
	provider := StoreProviderIdentity{ID: id, Name: id, Backend: "fake"}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, installed, "", nil),
		}}
	}}
}

func failingProvider(message string) StoreCatalogProvider {
	id := "catalog-failed"
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		return StoreCatalogProviderRun{Provider: StoreProviderIdentity{ID: id}, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderFailed, Error: message}
	}}
}

func positiveAndNegativeProvider(pfn string) StoreCatalogProvider {
	id := "catalog-conflict"
	provider := StoreProviderIdentity{ID: id, Name: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		target := &ExactStoreUpdateTarget{Identity: identity, Provider: provider, ProductID: "9NCODEX", Verified: true, VerifiedBy: id, VerifiedAt: scan.StartedAt.Add(time.Second)}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", target),
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationAuthoritativeNegative, "1.0.0", "", nil),
		}}
	}}
}

func inapplicableProvider(pfn string) StoreCatalogProvider {
	id := "catalog-inapplicable"
	provider := StoreProviderIdentity{ID: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationNewerCatalogNoApplicableInstaller, "1.0.0", "1.1.0", nil),
		}}
	}}
}

func positiveWithoutTargetProvider(pfn string) StoreCatalogProvider {
	id := "catalog-no-target"
	provider := StoreProviderIdentity{ID: id}
	return fakeCatalogProvider{id: id, fn: func(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
		identity := StoreInstalledIdentity{UserSID: scan.UserSID, PackageFamilyName: pfn}
		return StoreCatalogProviderRun{Provider: provider, StartedAt: scan.StartedAt, CompletedAt: scan.StartedAt.Add(time.Second), Health: StoreProviderHealthy, Observations: []StoreProviderObservation{
			storeObservation(identity, scan, provider, StoreProviderHealthy, StoreObservationPositiveUpdateOffer, "1.0.0", "1.1.0", nil),
		}}
	}}
}

func replaceStoreScanSID(sid string) func() {
	old := storeScanCurrentUserSID
	storeScanCurrentUserSID = func() (string, error) { return sid, nil }
	return func() { storeScanCurrentUserSID = old }
}

func replaceStoreScanNow(now time.Time) func() {
	old := storeScanNow
	storeScanNow = func() time.Time { return now.UTC() }
	return func() { storeScanNow = old }
}

func replaceStoreScanSystemContext(context storeScanSystemContext) func() {
	old := storeScanSystemContextProvider
	storeScanSystemContextProvider = func() storeScanSystemContext { return context }
	return func() { storeScanSystemContextProvider = old }
}

func fixedPipelineTimes(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Now().UTC()
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func fmtTimeForID(value time.Time) string {
	return value.UTC().Format("150405")
}
