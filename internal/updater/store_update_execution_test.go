package updater

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStoreExactUpdateVerifiesVersionChange(t *testing.T) {
	targets := []string{}
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{targets: &targets, result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if !result.OK {
		t.Fatalf("expected verified exact update, got %#v", result)
	}
	if strings.Join(targets, "|") != "9NCODEX" {
		t.Fatalf("expected only exact Product ID target, got %#v", targets)
	}
}

func TestStoreExactUpdatePrefersProductIDWhenProviderUpdateIDPresent(t *testing.T) {
	targets := []string{}
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{targets: &targets, result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	pkg := testExactStorePackage()
	pkg.StoreUpdateID = pkg.InstalledPackageFamilyName
	result := executeStoreExactUpdateForTest(t, executor, pkg)
	if !result.OK {
		t.Fatalf("expected verified exact update, got %#v", result)
	}
	if strings.Join(targets, "|") != pkg.StoreProductID {
		t.Fatalf("expected Product ID target first, got %#v", targets)
	}
}

func TestStoreCLIExactUpdateRunnerFallsBackToVerifiedUpdateID(t *testing.T) {
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if target == "OpenAI.Codex_abc123" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "accepted"}
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Checking updates...\nError: Could not find installed product metadata."}
		},
		func(manager string) bool { return manager == managerStore },
	)
	defer restore()

	request := testExactStoreRequest()
	request.UpdateID = "OpenAI.Codex_abc123"
	result := storeCLIExactUpdateRunner{}.RunStoreUpdate(context.Background(), request)
	if !result.OK {
		t.Fatalf("expected verified update ID fallback to succeed, got %#v", result)
	}
	if strings.Join(targets, "|") != "9NCODEX|OpenAI.Codex_abc123" {
		t.Fatalf("expected Product ID then exact update ID, got %#v", targets)
	}
}

func TestExactStoreUpdateRequestAllowsUpdateIDOnlyTarget(t *testing.T) {
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	restoreAvailability := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerStore })
	defer restoreAvailability()

	pkg := testExactStorePackage()
	pkg.ID = pkg.InstalledPackageFamilyName
	pkg.Key = packageKey(managerStore, pkg.InstalledPackageFamilyName)
	pkg.StoreProductID = ""
	pkg.StoreUpdateID = pkg.InstalledPackageFamilyName
	pkg.ExactTargetKind = exactTargetKindUpdateID
	pkg = applyPackageCapabilities(pkg)
	writePublishedExactStoreAssessment(t, pkg, storeScanNow(), nil)

	request, err := exactStoreUpdateRequestFromPackage(context.Background(), pkg)
	if err != nil {
		t.Fatalf("update-ID-only Store target should authorize request creation: %v", err)
	}
	if request.ProductID != "" || request.UpdateID != pkg.StoreUpdateID || request.Target != pkg.StoreUpdateID {
		t.Fatalf("unexpected update-ID-only request: %#v", request)
	}
	if request.Provider.ID != managerStore || request.Provider.Backend != backendStoreCLI {
		t.Fatalf("update-ID-only execution should use Store CLI provider, got %#v", request.Provider)
	}
}

func TestExactStoreUpdateRequestRejectsMalformedProductID(t *testing.T) {
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()

	pkg := testExactStorePackage()
	pkg.StoreProductID = "OpenAI.Codex"
	pkg.StoreUpdateID = pkg.InstalledPackageFamilyName

	if _, err := exactStoreUpdateRequestFromPackage(context.Background(), pkg); err == nil || !strings.Contains(err.Error(), "Product ID") {
		t.Fatalf("expected malformed Product ID to be rejected before execution, got %v", err)
	}
}

func TestStoreProductIDFirstExactUpdateRunnerUsesWingetProductID(t *testing.T) {
	var commands []string
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			commands = append(commands, strings.Join(args, " "))
			targets = append(targets, packageActionTargetFromArgs(args))
			if !isWingetCommand(args) {
				t.Fatalf("expected Product ID attempt through winget only, got %v", args)
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "accepted"}
		},
		func(manager string) bool { return manager == managerWinget || manager == managerStore },
	)
	defer restore()

	result := storeProductIDFirstExactUpdateRunner{}.RunStoreUpdate(context.Background(), testExactStoreRequest())
	if !result.OK {
		t.Fatalf("expected winget Product ID action to be accepted, got %#v", result)
	}
	if strings.Join(targets, "|") != "9NCODEX" {
		t.Fatalf("expected exact Product ID target only, targets=%#v commands=%#v", targets, commands)
	}
	if !strings.Contains(commands[0], "--id 9NCODEX --exact") || !strings.Contains(commands[0], "--source msstore") {
		t.Fatalf("expected winget msstore exact Product ID command, got %q", commands[0])
	}
}

func TestStoreProductIDFirstExactUpdateRunnerRejectsMalformedProductID(t *testing.T) {
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			t.Fatalf("malformed Product ID must not run a package-manager command: %v", args)
			return CommandResult{}
		},
		func(manager string) bool { return true },
	)
	defer restore()

	request := testExactStoreRequest()
	request.ProductID = "OpenAI.Codex"
	request.Target = request.ProductID
	result := storeProductIDFirstExactUpdateRunner{}.RunStoreUpdate(context.Background(), request)
	if result.OK || !strings.Contains(result.Stderr, "Product ID") {
		t.Fatalf("expected malformed Product ID validation failure, got %#v", result)
	}
}

func TestStoreProductIDFirstExactUpdateRunnerFallsBackToStoreCLIExactTargets(t *testing.T) {
	var managers []string
	var targets []string
	restore := replacePackageActionHooks(
		func(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
			target := packageActionTargetFromArgs(args)
			targets = append(targets, target)
			if isWingetCommand(args) {
				managers = append(managers, managerWinget)
				return CommandResult{Code: 1, Command: strings.Join(args, " "), Stdout: "No applicable upgrade found."}
			}
			managers = append(managers, managerStore)
			if target == "OpenAI.Codex_abc123" {
				return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "accepted"}
			}
			return CommandResult{OK: true, Command: strings.Join(args, " "), Stdout: "Checking updates...\nError: Could not find installed product metadata."}
		},
		func(manager string) bool { return manager == managerWinget || manager == managerStore },
	)
	defer restore()

	request := testExactStoreRequest()
	request.UpdateID = "OpenAI.Codex_abc123"
	result := storeProductIDFirstExactUpdateRunner{}.RunStoreUpdate(context.Background(), request)
	if !result.OK {
		t.Fatalf("expected Store CLI exact update ID fallback to succeed, got %#v", result)
	}
	if strings.Join(managers, "|") != "winget|store|store" {
		t.Fatalf("expected winget Product ID then Store CLI targets, managers=%#v targets=%#v", managers, targets)
	}
	if strings.Join(targets, "|") != "9NCODEX|9NCODEX|OpenAI.Codex_abc123" {
		t.Fatalf("unexpected exact target sequence: %#v", targets)
	}
}

func TestStoreExactUpdateValidationAllowsWingetOnlyProductIDTarget(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "winget upgrade --id 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	pkg := testExactStorePackage()
	preparePublishedExactStoreAssessment(t, pkg)
	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(manager string) bool { return manager == managerWinget }
	defer func() { packageActionManagerAvailable = oldAvailable }()

	var provider StoreProviderIdentity
	result := executor.ExecuteWithCallbacks(context.Background(), pkg, StoreExactUpdateCallbacks{
		Starting: func(request StoreExactUpdateRequest) {
			provider = request.Provider
		},
	})
	if !result.OK {
		t.Fatalf("expected winget-only exact Product ID update path to validate, got %#v", result)
	}
	if provider.ID != managerWinget || provider.Backend != backendWingetMSStoreFallback {
		t.Fatalf("expected winget msstore exact provider, got %#v", provider)
	}
}

func TestStoreExactUpdateValidationRejectsWhenNoExactExecutorAvailable(t *testing.T) {
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	pkg := testExactStorePackage()
	preparePublishedExactStoreAssessment(t, pkg)
	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(string) bool { return false }
	defer func() { packageActionManagerAvailable = oldAvailable }()

	result := executor.Execute(context.Background(), pkg)
	if result.OK || !strings.Contains(result.Stderr, "no exact Store update executor") {
		t.Fatalf("expected missing exact executor validation failure, got %#v", result)
	}
}

func TestStoreExactUpdateCallbacksExposeExecutionPhases(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	preparePublishedExactStoreAssessment(t, testExactStorePackage())
	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(manager string) bool { return manager == managerStore }
	defer func() { packageActionManagerAvailable = oldAvailable }()
	var phases []string
	result := executor.ExecuteWithCallbacks(context.Background(), testExactStorePackage(), StoreExactUpdateCallbacks{
		Starting:  func(StoreExactUpdateRequest) { phases = append(phases, jobStateStarting) },
		Accepted:  func(StoreExactUpdateRequest, CommandResult) { phases = append(phases, jobStateAccepted) },
		Verifying: func(StoreExactUpdateRequest) { phases = append(phases, jobStateVerifying) },
	})
	if !result.OK {
		t.Fatalf("expected verified update, got %#v", result)
	}
	if strings.Join(phases, "|") != "starting|accepted|verifying" {
		t.Fatalf("unexpected phases: %#v", phases)
	}
}

func TestStoreExactUpdateAcceptedWithoutPackageChangeIsNotVerified(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("expected accepted_not_verified, got %#v", result)
	}
}

func TestStoreExactUpdateDowngradeIsNotVerified(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("0.9.0", "OpenAI.Codex_0.9.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "catalog query 9NCODEX", Stdout: "no offer"}},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("downgrade must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateMalformedVersionIsNotVerified(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.x.0", "OpenAI.Codex_1.x.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("malformed version must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateFullNameOnlyChangeIsNotVerified(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__def456", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("full-name-only change must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateOfferedVersionNotReachedIsNotVerified(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.5", "OpenAI.Codex_1.0.5_x64__abc123", true),
		}},
		fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "catalog query 9NCODEX", Stdout: "no offer"}},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("post version below offered version must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateAcceptedButTargetedRescanFails(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{command: CommandResult{Command: "catalog query 9NCODEX", Code: 1, Stderr: "catalog unavailable"}},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode || !strings.Contains(result.Command, "catalog query") {
		t.Fatalf("expected accepted_not_verified with catalog diagnostics, got %#v", result)
	}
}

func TestStoreExactUpdateFailedTargetedRescanWithNegativeTextDoesNotVerify(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: false, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{Command: "catalog query 9NCODEX", Code: 1, Stdout: "No update available"}},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("failed targeted no-offer text must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateNilCatalogUsesProductionProductIDFirstQuery(t *testing.T) {
	catalogCalls := 0
	oldDefaultCatalogProvider := defaultStoreExactCatalogProvider
	defaultStoreExactCatalogProvider = func() StoreExactCatalogProvider {
		return storeExactCatalogFunc(func(context.Context, StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
			catalogCalls++
			return StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, CommandResult{OK: true, Command: "production default Product ID catalog query", Stdout: "no offer"}
		})
	}
	defer func() { defaultStoreExactCatalogProvider = oldDefaultCatalogProvider }()
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	pkg := testExactStorePackage()
	preparePublishedExactStoreAssessment(t, pkg)
	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(manager string) bool { return manager == managerWinget || manager == managerStore }
	defer func() { packageActionManagerAvailable = oldAvailable }()
	pkg.OfferedVersion = "1.0.0"
	pkg.AvailableVersion = "1.0.0"
	executor := StoreExactUpdateExecutor{
		Runner: fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		Inventory: &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		Events:    fakeStoreEvents{},
		Timeout:   25 * time.Millisecond,
		PollEvery: time.Millisecond,
	}
	result := executor.Execute(context.Background(), pkg)
	if !result.OK || !strings.Contains(result.Stdout, "exact offer disappeared") {
		t.Fatalf("expected nil catalog to use production Product ID query for verification, result=%#v calls=%d", result, catalogCalls)
	}
	if catalogCalls == 0 || !strings.Contains(result.Command, "production default Product ID catalog query") {
		t.Fatalf("expected production catalog query provider to run, calls=%d command=%q", catalogCalls, result.Command)
	}
}

func TestStoreExactUpdatePollingVerifiesWhenEventIsLost(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if !result.OK {
		t.Fatalf("expected polling fallback to verify update, got %#v", result)
	}
}

func TestStoreExactUpdateSameVisibleVersionWithOfferRemoved(t *testing.T) {
	pkg := testExactStorePackage()
	pkg.AvailableVersion = "1.0.0"
	pkg.OfferedVersion = "1.0.0"
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "catalog query 9NCODEX", Stdout: "no offer"}},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, pkg)
	if !result.OK || !strings.Contains(result.Stdout, "exact offer disappeared") {
		t.Fatalf("expected offer removal to verify same-version update, got %#v", result)
	}
}

func TestStoreProductIDFirstExactCatalogQueryProviderUsesWingetProductID(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget || manager == managerStore })
	defer restore()
	request := testExactStoreRequest()
	query := storeProductIDFirstExactCatalogQueryProvider{
		Winget: fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "winget list --upgrade-available --id 9NCODEX", Stdout: "no offer"}},
		Store:  fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: true, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "store update OpenAI.Codex_abc123", Stdout: "should not run"}},
	}
	got, result := query.QueryExact(context.Background(), request)
	if !got.Authoritative || got.OfferAvailable || result.Command != "winget list --upgrade-available --id 9NCODEX" {
		t.Fatalf("expected authoritative winget Product ID query without Store CLI fallback: catalog=%#v result=%#v", got, result)
	}
}

func TestStoreProductIDFirstExactCatalogQueryProviderFallsBackToStoreCLI(t *testing.T) {
	restore := replacePackageActionManagerAvailable(func(manager string) bool { return manager == managerWinget || manager == managerStore })
	defer restore()
	request := testExactStoreRequest()
	query := storeProductIDFirstExactCatalogQueryProvider{
		Winget: fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: false, Diagnostics: "winget not authoritative"}, command: CommandResult{Command: "winget list --upgrade-available --id 9NCODEX", Code: 1, Stderr: "not authoritative"}},
		Store:  fakeStoreExactCatalog{result: StoreExactCatalogResult{Authoritative: true, OfferAvailable: false, InstalledHealthy: true}, command: CommandResult{OK: true, Command: "store update OpenAI.Codex_abc123", Stdout: "already up to date"}},
	}
	got, result := query.QueryExact(context.Background(), request)
	if !got.Authoritative || got.OfferAvailable || !strings.Contains(result.Command, "Store CLI exact catalog fallback") {
		t.Fatalf("expected Store CLI exact catalog fallback: catalog=%#v result=%#v", got, result)
	}
}

func TestStoreExactUpdateIgnoresWrongUserAndUnrelatedEvents(t *testing.T) {
	eventTime := time.Now().UTC().Add(time.Second)
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{events: []StorePackageChangeEvent{
			{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-other", PackageFamilyName: "OpenAI.Codex_abc123"}, Version: "1.1.0", PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123", Healthy: true, Exists: true, ObservedAt: eventTime},
			{Identity: StoreInstalledIdentity{UserSID: "S-1-5-21-exec", PackageFamilyName: "Other.App_abc123"}, Version: "1.1.0", PackageFullName: "Other.App_1.1.0_x64__abc123", Healthy: true, Exists: true, ObservedAt: eventTime},
		}},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != storeUpdateAcceptedNotVerifiedCode {
		t.Fatalf("wrong-user and unrelated events must not verify update, got %#v", result)
	}
}

func TestStoreExactUpdateExactEventAcceleratesVersionVerification(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	actionStartedAt := time.Now().UTC()
	inventory := &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
	}}
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, inventory, fakeStoreExactCatalog{}, fakeStoreEvents{})
	verification := executor.verifyAcceptedActionWithEvents(context.Background(), request, pre, time.Hour, actionStartedAt, storePackageEventSubscription{
		events: fakeStoreEventChannel([]StorePackageChangeEvent{{
			Identity:        request.Identity,
			PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
			Version:         "1.1.0",
			Healthy:         true,
			Exists:          true,
			ObservedAt:      actionStartedAt.Add(time.Millisecond),
		}}),
	})
	if !verification.Verified || inventory.calls != 2 {
		t.Fatalf("expected exact event to accelerate inventory verification, calls=%d verification=%#v", inventory.calls, verification)
	}
	if !strings.Contains(verification.Result.Stdout, "PackageCatalog event received") {
		t.Fatalf("expected event wake diagnostic, got %q", verification.Result.Stdout)
	}
}

func TestStoreExactUpdatePreActionEventIgnored(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	actionStartedAt := time.Now().UTC()
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	verification := executor.verifyAcceptedActionWithEvents(ctx, request, pre, time.Hour, actionStartedAt, storePackageEventSubscription{
		events: fakeStoreEventChannel([]StorePackageChangeEvent{{
			Identity:        request.Identity,
			PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
			Version:         "1.1.0",
			Healthy:         true,
			Exists:          true,
			ObservedAt:      actionStartedAt.Add(-time.Millisecond),
		}}),
	})
	if verification.Verified || !strings.Contains(verification.Result.Stdout, "predates") {
		t.Fatalf("pre-action event should be ignored, got %#v stdout=%q", verification, verification.Result.Stdout)
	}
}

func TestStoreExactUpdateDuplicateEventHarmless(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	actionStartedAt := time.Now().UTC()
	event := StorePackageChangeEvent{
		Identity:        request.Identity,
		PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
		Version:         "1.1.0",
		Healthy:         true,
		Exists:          true,
		ObservedAt:      actionStartedAt.Add(time.Millisecond),
	}
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	duplicate := event
	duplicate.ObservedAt = duplicate.ObservedAt.Add(time.Millisecond)
	verification := executor.verifyAcceptedActionWithEvents(ctx, request, pre, time.Hour, actionStartedAt, storePackageEventSubscription{events: fakeStoreEventChannel([]StorePackageChangeEvent{event, duplicate})})
	if verification.Verified || !strings.Contains(verification.Result.Stdout, "duplicate") {
		t.Fatalf("duplicate event should be harmless, got %#v stdout=%q", verification, verification.Result.Stdout)
	}
}

func TestStoreExactUpdateEventWithNoInventoryOrCatalogProofDoesNotSucceed(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	actionStartedAt := time.Now().UTC()
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	verification := executor.verifyAcceptedActionWithEvents(ctx, request, pre, time.Hour, actionStartedAt, storePackageEventSubscription{events: fakeStoreEventChannel([]StorePackageChangeEvent{{
		Identity:        request.Identity,
		PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
		Version:         "1.1.0",
		Healthy:         true,
		Exists:          true,
		ObservedAt:      actionStartedAt.Add(time.Millisecond),
	}})})
	if verification.Verified {
		t.Fatalf("event alone must not verify update, got %#v", verification)
	}
}

func TestStoreExactUpdatePackageRemovalEventDoesNotSucceed(t *testing.T) {
	request := testExactStoreRequest()
	reason := validateStorePackageChangeEvent(request, time.Now().UTC(), StorePackageChangeEvent{
		Identity:        request.Identity,
		PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
		Version:         "1.1.0",
		Healthy:         true,
		Exists:          false,
		ObservedAt:      time.Now().UTC().Add(time.Second),
	})
	if !strings.Contains(reason, "removal") {
		t.Fatalf("package removal event should be rejected, got %q", reason)
	}
}

func TestStoreExactUpdateFullNameOnlyEventDoesNotSucceedWithoutCatalogProof(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	actionStartedAt := time.Now().UTC()
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__def456", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	verification := executor.verifyAcceptedActionWithEvents(ctx, request, pre, time.Hour, actionStartedAt, storePackageEventSubscription{events: fakeStoreEventChannel([]StorePackageChangeEvent{{
		Identity:        request.Identity,
		PackageFullName: "OpenAI.Codex_1.0.0_x64__def456",
		Version:         "1.0.0",
		Healthy:         true,
		Exists:          true,
		ObservedAt:      actionStartedAt.Add(time.Millisecond),
	}})})
	if verification.Verified {
		t.Fatalf("full-name-only event must not verify without catalog proof, got %#v", verification)
	}
}

func TestStoreExactUpdateSubscriptionFailureAndClosureFallBack(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	verification := executor.verifyAcceptedActionWithEvents(context.Background(), request, pre, time.Hour, time.Now().UTC(), storePackageEventSubscription{
		events: fakeStoreEventChannel(nil),
		err:    context.Canceled,
	})
	if !verification.Verified || !strings.Contains(verification.Result.Command, "PackageCatalog events unavailable") {
		t.Fatalf("subscription failure should fall back to inventory verification, got %#v", verification)
	}
}

func TestStoreExactUpdateCancellationCleansSubscriptionAndIgnoresLateEvent(t *testing.T) {
	cleaned := false
	ctx, cancel := context.WithCancel(context.Background())
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{cleanup: func() { cleaned = true }})
	executor.Runner = fakeStoreExactRunner{after: cancel, result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}}
	result := executeStoreExactUpdateForTestWithContext(t, ctx, executor, testExactStorePackage())
	if result.Code != commandCancelledCode {
		t.Fatalf("expected cancellation, got %#v", result)
	}
	if !cleaned {
		t.Fatal("subscription cleanup was not called")
	}
}

func TestStoreExactUpdateResourceFrameworkEventIgnored(t *testing.T) {
	request := testExactStoreRequest()
	for _, classification := range []string{storePackageClassResource, storePackageClassFramework} {
		reason := validateStorePackageChangeEvent(request, time.Now().UTC(), StorePackageChangeEvent{
			Identity:        request.Identity,
			PackageFullName: "OpenAI.Codex_1.1.0_x64__abc123",
			Version:         "1.1.0",
			Healthy:         true,
			Exists:          true,
			ObservedAt:      time.Now().UTC().Add(time.Second),
			Classification:  classification,
		})
		if !strings.Contains(reason, "framework or resource") {
			t.Fatalf("expected %s event to be ignored, got %q", classification, reason)
		}
	}
}

func TestStoreExactUpdateVerificationCanResumeAfterAcceptedAction(t *testing.T) {
	request := testExactStoreRequest()
	pre := testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
		testStoreExactSnapshot("1.1.0", "OpenAI.Codex_1.1.0_x64__abc123", true),
	}}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	verification := executor.verifyAcceptedAction(context.Background(), request, pre, time.Millisecond)
	if !verification.Verified {
		t.Fatalf("expected resumed verification to succeed, got %#v", verification)
	}
}

func TestStoreExactUpdateCancellationStopsVerification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{after: cancel, result: CommandResult{OK: true, Command: "store update 9NCODEX", Stdout: "accepted"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
			testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true),
		}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTestWithContext(t, ctx, executor, testExactStorePackage())
	if result.Code != commandCancelledCode {
		t.Fatalf("expected cancelled verification, got %#v", result)
	}
}

func TestStoreExactUpdateRejectedTargetFails(t *testing.T) {
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{result: CommandResult{Command: "store update 9NCODEX", Code: 1, Stderr: "target rejected"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	result := executeStoreExactUpdateForTest(t, executor, testExactStorePackage())
	if result.OK || result.Code != 1 || !strings.Contains(result.Stderr, "target rejected") {
		t.Fatalf("expected exact target rejection to fail, got %#v", result)
	}
}

func TestStoreExactUpdateDisplayNameFallbackIsImpossible(t *testing.T) {
	targets := []string{}
	executor := testStoreExactExecutor(
		fakeStoreExactRunner{targets: &targets, result: CommandResult{Command: "store update 9NCODEX", Code: 1, Stderr: "target rejected"}},
		&fakeStoreExactInventory{snapshots: []StoreExactPackageSnapshot{testStoreExactSnapshot("1.0.0", "OpenAI.Codex_1.0.0_x64__abc123", true)}},
		fakeStoreExactCatalog{},
		fakeStoreEvents{},
	)
	pkg := testExactStorePackage()
	pkg.Name = "Display Name Must Never Be Used"
	result := executeStoreExactUpdateForTest(t, executor, pkg)
	if result.OK {
		t.Fatalf("expected rejected exact target, got %#v", result)
	}
	if strings.Join(targets, "|") != "9NCODEX" {
		t.Fatalf("display-name fallback target was attempted: %#v", targets)
	}
}

func TestStoreExactUpdateValidationRequiresFreshAvailableAssessment(t *testing.T) {
	executor := testStoreExactExecutor(fakeStoreExactRunner{}, &fakeStoreExactInventory{}, fakeStoreExactCatalog{}, fakeStoreEvents{})
	pkg := testExactStorePackage()
	pkg.Stale = true
	result := executeStoreExactUpdateForTest(t, executor, pkg)
	if result.OK || !strings.Contains(result.Stderr, "fresh assessment") {
		t.Fatalf("expected stale assessment validation failure, got %#v", result)
	}
}

func TestVerifyPublishedStoreUpdateAssessmentEnforcesFreshness(t *testing.T) {
	for _, tc := range []struct {
		name           string
		now            time.Time
		currentVersion string
		mutate         func(*StoreScanSnapshot)
	}{
		{
			name:           "old evidence",
			now:            storeScanNow().Add(-storeAssessmentFreshnessWindow - time.Minute),
			currentVersion: "1.0.0",
		},
		{
			name:           "recovered fallback",
			now:            storeScanNow(),
			currentVersion: "1.0.0",
			mutate: func(snapshot *StoreScanSnapshot) {
				snapshot.RecoveredFromFallback = true
			},
		},
		{
			name:           "installed version mismatch",
			now:            storeScanNow(),
			currentVersion: "2.0.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UPDATER_STATE_DIR", t.TempDir())
			restoreSID := replaceStoreScanSID("S-1-5-21-exec")
			defer restoreSID()
			writePublishedExactStoreAssessment(t, testExactStorePackage(), tc.now, tc.mutate)
			request := testExactStoreRequest()
			request.CurrentInstalledVersion = tc.currentVersion
			if err := verifyPublishedStoreUpdateAssessment(context.Background(), request); err == nil {
				t.Fatal("freshness failure was accepted for Store update authorization")
			}
		})
	}
}

func executeStoreExactUpdateForTest(t *testing.T, executor StoreExactUpdateExecutor, pkg Package) CommandResult {
	t.Helper()
	return executeStoreExactUpdateForTestWithContext(t, context.Background(), executor, pkg)
}

func executeStoreExactUpdateForTestWithContext(t *testing.T, ctx context.Context, executor StoreExactUpdateExecutor, pkg Package) CommandResult {
	t.Helper()
	restoreSID := replaceStoreScanSID("S-1-5-21-exec")
	defer restoreSID()
	preparePublishedExactStoreAssessment(t, pkg)
	oldAvailable := packageActionManagerAvailable
	packageActionManagerAvailable = func(manager string) bool { return manager == managerStore }
	defer func() { packageActionManagerAvailable = oldAvailable }()
	return executor.Execute(ctx, pkg)
}

func preparePublishedExactStoreAssessment(t *testing.T, pkg Package) {
	t.Helper()
	t.Setenv("UPDATER_STATE_DIR", t.TempDir())
	writePublishedExactStoreAssessment(t, pkg, storeScanNow(), nil)
}

func writePublishedExactStoreAssessment(t *testing.T, pkg Package, now time.Time, mutate func(*StoreScanSnapshot)) {
	t.Helper()
	store, err := openDefaultStoreScanRepository()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity := StoreInstalledIdentity{UserSID: "S-1-5-21-exec", PackageFamilyName: pkg.InstalledPackageFamilyName}
	scan := StoreScanGeneration{
		ScanID:           pkg.ScanID,
		UserSID:          identity.UserSID,
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		CompletionStatus: StoreScanCompleted,
	}
	assessment := StorePublishedAssessment{
		StoreUpdateAssessment: StoreUpdateAssessment{
			State:            StoreUpdateState(pkg.UpdateState),
			Identity:         identity,
			ScanID:           pkg.ScanID,
			Reason:           pkg.UpdateReason,
			InstalledVersion: pkg.InstalledVersion,
			AvailableVersion: pkg.OfferedVersion,
			Target: &ExactStoreUpdateTarget{
				Identity:   identity,
				Provider:   StoreProviderIdentity{ID: managerStore, Name: "Store CLI", Backend: backendStoreCLI},
				ProductID:  pkg.StoreProductID,
				UpdateID:   pkg.StoreUpdateID,
				Verified:   true,
				VerifiedBy: "test",
				VerifiedAt: now,
			},
		},
		ObservedAt:                 now,
		StoreProductID:             pkg.StoreProductID,
		UpdateID:                   pkg.StoreUpdateID,
		ExactActionTargetAvailable: pkg.ExactActionTargetAvailable,
		Applicability:              pkg.Applicability,
	}
	inventory := StorePackagedAppInventory{
		Scan: scan,
		Families: []StorePackagedAppFamily{{
			Identity:    identity,
			DisplayName: pkg.Name,
			ProductLike: true,
		}},
	}
	snapshot := StoreScanSnapshot{
		SchemaVersion: storeScanSchemaVersion,
		Published:     true,
		Scan:          scan,
		Inventory:     inventory,
		Assessments:   []StorePublishedAssessment{assessment},
	}
	if mutate != nil {
		mutate(&snapshot)
	}
	if _, err := store.PersistCompletedScanSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func testStoreExactExecutor(runner StoreExactUpdateActionRunner, inventory StoreExactInventoryProvider, catalog StoreExactCatalogProvider, events StorePackageEventSource) StoreExactUpdateExecutor {
	return StoreExactUpdateExecutor{
		Runner:    runner,
		Inventory: inventory,
		Catalog:   catalog,
		Events:    events,
		Timeout:   25 * time.Millisecond,
		PollEvery: time.Millisecond,
	}
}

func testExactStorePackage() Package {
	return Package{
		Key:                        packageKey(managerStore, "9NCODEX"),
		Manager:                    managerStore,
		ID:                         "9NCODEX",
		Name:                       "Codex",
		Version:                    "1.0.0",
		AvailableVersion:           "1.1.0",
		UpdateAvailable:            true,
		UpdateSupported:            true,
		Installed:                  true,
		Source:                     sourceNativeAppX,
		ActionBackend:              backendStoreCLI,
		UpdateState:                string(StoreUpdateAvailable),
		UpdateReason:               "fresh exact positive update evidence",
		ObservedAt:                 "2026-06-21T12:00:00Z",
		ScanID:                     "scan-exec",
		ExactIdentityAvailable:     true,
		ExactActionTargetAvailable: true,
		InstalledPackageFamilyName: "OpenAI.Codex_abc123",
		StoreProductID:             "9NCODEX",
		InstalledVersion:           "1.0.0",
		OfferedVersion:             "1.1.0",
		Applicability:              "applicable",
	}
}

func testExactStoreRequest() StoreExactUpdateRequest {
	return StoreExactUpdateRequest{
		Identity:                StoreInstalledIdentity{UserSID: "S-1-5-21-exec", PackageFamilyName: "OpenAI.Codex_abc123"},
		ProductID:               "9NCODEX",
		Target:                  "9NCODEX",
		Provider:                StoreProviderIdentity{ID: managerStore, Name: "Store CLI", Backend: backendStoreCLI},
		ScanID:                  "scan-exec",
		InstalledVersion:        "1.0.0",
		CurrentInstalledVersion: "1.0.0",
		OfferedVersion:          "1.1.0",
	}
}

func testStoreExactSnapshot(version, fullName string, healthy bool) StoreExactPackageSnapshot {
	return StoreExactPackageSnapshot{
		Identity:        StoreInstalledIdentity{UserSID: "S-1-5-21-exec", PackageFamilyName: "OpenAI.Codex_abc123"},
		PackageFullName: fullName,
		Version:         version,
		Healthy:         healthy,
		Exists:          true,
		ObservedAt:      time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
	}
}

type fakeStoreExactRunner struct {
	targets *[]string
	result  CommandResult
	after   func()
}

func (runner fakeStoreExactRunner) RunStoreUpdate(ctx context.Context, request StoreExactUpdateRequest) CommandResult {
	if runner.targets != nil {
		*runner.targets = append(*runner.targets, request.Target)
	}
	if runner.after != nil {
		runner.after()
	}
	return runner.result
}

type fakeStoreExactInventory struct {
	snapshots []StoreExactPackageSnapshot
	results   []CommandResult
	calls     int
}

func (inventory *fakeStoreExactInventory) Snapshot(ctx context.Context, identity StoreInstalledIdentity) (StoreExactPackageSnapshot, CommandResult) {
	if inventory.calls >= len(inventory.snapshots) && len(inventory.snapshots) > 0 {
		inventory.calls++
		return inventory.snapshots[len(inventory.snapshots)-1], fakeStoreInventoryResult(inventory, len(inventory.snapshots)-1)
	}
	index := inventory.calls
	inventory.calls++
	if len(inventory.snapshots) == 0 {
		return StoreExactPackageSnapshot{Identity: identity}, fakeStoreInventoryResult(inventory, index)
	}
	snapshot := inventory.snapshots[index]
	if !snapshot.Identity.Resolved() {
		snapshot.Identity = identity
	}
	return snapshot, fakeStoreInventoryResult(inventory, index)
}

func fakeStoreInventoryResult(inventory *fakeStoreExactInventory, index int) CommandResult {
	if index >= 0 && index < len(inventory.results) {
		return inventory.results[index]
	}
	return CommandResult{OK: true, Command: "fake inventory"}
}

type fakeStoreExactCatalog struct {
	result  StoreExactCatalogResult
	command CommandResult
}

func (catalog fakeStoreExactCatalog) QueryExact(context.Context, StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	command := catalog.command
	if command.Command == "" {
		command = CommandResult{Command: "fake catalog", Code: 1, Stderr: "not implemented"}
	}
	return catalog.result, command
}

type storeExactCatalogFunc func(context.Context, StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult)

func (fn storeExactCatalogFunc) QueryExact(ctx context.Context, request StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	return fn(ctx, request)
}

type fakeStoreEvents struct {
	events  []StorePackageChangeEvent
	err     error
	cleanup func()
}

func (events fakeStoreEvents) Subscribe(context.Context, StoreInstalledIdentity) (<-chan StorePackageChangeEvent, func(), error) {
	ch := fakeStoreEventChannel(events.events)
	cleanup := events.cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	return ch, cleanup, events.err
}

func fakeStoreEventChannel(events []StorePackageChangeEvent) <-chan StorePackageChangeEvent {
	ch := make(chan StorePackageChangeEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch
}
