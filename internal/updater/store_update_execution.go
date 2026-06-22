package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	storeExactUpdateRollbackFlag       = "UPDATER_STORE_LEGACY_UPDATE_EXECUTION"
	storeExactUpdateVerifyTimeout      = 2 * time.Minute
	storeExactUpdatePollInterval       = 3 * time.Second
	storeUpdateAcceptedNotVerifiedCode = 202
)

type StoreExactUpdateRequest struct {
	Identity         StoreInstalledIdentity
	ProductID        string
	UpdateID         string
	Target           string
	Provider         StoreProviderIdentity
	ScanID           string
	ObservedAt       string
	InstalledVersion string
	OfferedVersion   string
}

type StoreExactPackageSnapshot struct {
	Identity        StoreInstalledIdentity
	PackageFullName string
	Version         string
	Healthy         bool
	Exists          bool
	ObservedAt      time.Time
	Diagnostics     string
}

type StorePackageChangeEvent struct {
	Identity        StoreInstalledIdentity
	PackageFullName string
	Version         string
	Healthy         bool
	ObservedAt      time.Time
}

type StoreExactCatalogResult struct {
	Authoritative    bool
	OfferAvailable   bool
	InstalledHealthy bool
	OfferedVersion   string
	Diagnostics      string
}

type StoreExactUpdateActionRunner interface {
	RunStoreUpdate(context.Context, StoreExactUpdateRequest) CommandResult
}

type StoreExactInventoryProvider interface {
	Snapshot(context.Context, StoreInstalledIdentity) (StoreExactPackageSnapshot, CommandResult)
}

type StoreExactCatalogProvider interface {
	QueryExact(context.Context, StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult)
}

type StorePackageEventSource interface {
	Subscribe(context.Context, StoreInstalledIdentity) (<-chan StorePackageChangeEvent, func(), error)
}

type StoreExactUpdateExecutor struct {
	Runner    StoreExactUpdateActionRunner
	Inventory StoreExactInventoryProvider
	Catalog   StoreExactCatalogProvider
	Events    StorePackageEventSource
	Timeout   time.Duration
	PollEvery time.Duration
}

type StoreExactUpdateCallbacks struct {
	Starting  func(StoreExactUpdateRequest)
	Accepted  func(StoreExactUpdateRequest, CommandResult)
	Verifying func(StoreExactUpdateRequest)
}

var (
	storeExactUpdateExecutor         = defaultStoreExactUpdateExecutor()
	defaultStoreExactCatalogProvider = func() StoreExactCatalogProvider { return storeProductIDFirstExactCatalogQueryProvider{} }
)

func defaultStoreExactUpdateExecutor() StoreExactUpdateExecutor {
	return StoreExactUpdateExecutor{
		Runner:    storeProductIDFirstExactUpdateRunner{},
		Inventory: storePackagedSnapshotProvider{Provider: storePackagedAppInventoryProvider()},
		Catalog:   defaultStoreExactCatalogProvider(),
		Events:    noStorePackageEvents{},
		Timeout:   storeExactUpdateVerifyTimeout,
		PollEvery: storeExactUpdatePollInterval,
	}
}

func storeExactUpdateExecutionRollbackEnabled() bool {
	return featureFlagEnabled(storeExactUpdateRollbackFlag)
}

func runExactStoreUpdateWithVerification(ctx context.Context, pkg Package) CommandResult {
	return storeExactUpdateExecutor.Execute(ctx, pkg)
}

func (executor StoreExactUpdateExecutor) Execute(ctx context.Context, pkg Package) CommandResult {
	return executor.ExecuteWithCallbacks(ctx, pkg, StoreExactUpdateCallbacks{})
}

func (executor StoreExactUpdateExecutor) ExecuteWithCallbacks(ctx context.Context, pkg Package, callbacks StoreExactUpdateCallbacks) CommandResult {
	request, err := exactStoreUpdateRequestFromPackage(ctx, pkg)
	if err != nil {
		return validationCommandResult("store exact update", err)
	}
	if executor.Runner == nil {
		executor.Runner = storeProductIDFirstExactUpdateRunner{}
	}
	if executor.Inventory == nil {
		executor.Inventory = storePackagedSnapshotProvider{Provider: storePackagedAppInventoryProvider()}
	}
	if executor.Catalog == nil {
		executor.Catalog = defaultStoreExactCatalogProvider()
	}
	if executor.Events == nil {
		executor.Events = noStorePackageEvents{}
	}
	timeout := executor.Timeout
	if timeout <= 0 {
		timeout = storeExactUpdateVerifyTimeout
	}
	pollEvery := executor.PollEvery
	if pollEvery <= 0 {
		pollEvery = storeExactUpdatePollInterval
	}

	pre, preResult := executor.Inventory.Snapshot(ctx, request.Identity)
	if callbacks.Starting != nil {
		callbacks.Starting(request)
	}
	action := executor.Runner.RunStoreUpdate(ctx, request)
	action = appendStoreExecutionDiagnostic(action, "pre-action", pre, preResult)
	if ctx.Err() != nil {
		action.OK = false
		action.Code = commandCancelledCode
		action.Stderr = appendDiagnosticLine(action.Stderr, "Store update cancelled.")
		return action
	}
	if action.Code == commandCancelledCode {
		return action
	}
	if !action.OK {
		return action
	}
	if callbacks.Accepted != nil {
		callbacks.Accepted(request, action)
	}
	action.Stdout = appendDiagnosticLine(action.Stdout, "Store update request accepted; verifying exact package state.")

	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if callbacks.Verifying != nil {
		callbacks.Verifying(request)
	}
	verification := executor.verifyAcceptedAction(verifyCtx, request, pre, pollEvery)
	merged := mergeCommandResults(action, verification.Result, "Store post-action verification")
	merged = appendStoreExecutionDiagnostic(merged, "post-action", verification.Post, verification.PostResult)
	if verification.Verified {
		merged.OK = true
		merged.Code = 0
		merged.Stdout = appendDiagnosticLine(merged.Stdout, verification.Message)
		merged.Stderr = strings.TrimSpace(merged.Stderr)
		return merged
	}
	if verifyCtx.Err() != nil && errors.Is(verifyCtx.Err(), context.Canceled) {
		merged.OK = false
		merged.Code = commandCancelledCode
		merged.Stderr = appendDiagnosticLine(merged.Stderr, "Store update verification cancelled.")
		return merged
	}
	merged.OK = false
	merged.Code = storeUpdateAcceptedNotVerifiedCode
	merged.Stderr = appendDiagnosticLine(merged.Stderr, firstNonEmpty(verification.Message, "Store update request was accepted but final state could not be verified."))
	return merged
}

type storeExactVerificationResult struct {
	Verified   bool
	Message    string
	Result     CommandResult
	Post       StoreExactPackageSnapshot
	PostResult CommandResult
}

func (executor StoreExactUpdateExecutor) verifyAcceptedAction(ctx context.Context, request StoreExactUpdateRequest, pre StoreExactPackageSnapshot, pollEvery time.Duration) storeExactVerificationResult {
	events, cleanup, eventErr := executor.Events.Subscribe(ctx, request.Identity)
	if cleanup != nil {
		defer cleanup()
	}
	result := CommandResult{OK: true, Command: "store exact post-action verification"}
	if eventErr != nil {
		result = mergeCommandResults(result, CommandResult{Command: "PackageCatalog event subscription", Code: 1, Stderr: sanitizeProviderDiagnostic(eventErr.Error())}, "PackageCatalog events unavailable")
	}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		post, postResult := executor.Inventory.Snapshot(ctx, request.Identity)
		if verified, message := storeSnapshotVerifiesUpdate(request, pre, post); verified {
			return storeExactVerificationResult{Verified: true, Message: message, Result: result, Post: post, PostResult: postResult}
		}
		catalog, catalogResult := executor.Catalog.QueryExact(ctx, request)
		result = mergeCommandResults(result, catalogResult, "targeted Store catalog query")
		if storeCatalogVerifiesUpdate(catalog, post) {
			return storeExactVerificationResult{Verified: true, Message: "Store update verified because the exact offer disappeared after a fresh targeted catalog query.", Result: result, Post: post, PostResult: postResult}
		}
		select {
		case <-ctx.Done():
			return storeExactVerificationResult{Message: ctx.Err().Error(), Result: result, Post: post, PostResult: postResult}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !event.Identity.Equal(request.Identity) {
				result.Stdout = appendDiagnosticLine(result.Stdout, "Ignored unrelated Store package event.")
				continue
			}
			eventSnapshot := StoreExactPackageSnapshot{
				Identity:        event.Identity,
				PackageFullName: event.PackageFullName,
				Version:         event.Version,
				Healthy:         event.Healthy,
				Exists:          true,
				ObservedAt:      event.ObservedAt,
			}
			if verified, message := storeSnapshotVerifiesUpdate(request, pre, eventSnapshot); verified {
				return storeExactVerificationResult{Verified: true, Message: "Store update verified from current-user package event: " + message, Result: result, Post: eventSnapshot, PostResult: CommandResult{OK: true, Command: "PackageCatalog event"}}
			}
		case <-ticker.C:
		}
	}
}

func exactStoreUpdateRequestFromPackage(ctx context.Context, pkg Package) (StoreExactUpdateRequest, error) {
	if storeExactUpdateExecutionRollbackEnabled() {
		return StoreExactUpdateRequest{}, errors.New("exact Store update execution is disabled by rollback flag")
	}
	if pkg.Manager != managerStore {
		return StoreExactUpdateRequest{}, errors.New("exact Store update execution requires a Store package")
	}
	if strings.ToLower(strings.TrimSpace(pkg.UpdateState)) != string(StoreUpdateAvailable) || !pkg.UpdateAvailable {
		return StoreExactUpdateRequest{}, errors.New("Store update requires a fresh available assessment")
	}
	if pkg.Stale {
		return StoreExactUpdateRequest{}, errors.New("Store update requires a fresh assessment; stale updates must be rescanned first")
	}
	pfn := storeInstalledPackageFamilyName(pkg)
	if pfn == "" {
		return StoreExactUpdateRequest{}, errors.New("Store update requires an exact package family name")
	}
	userSID, err := storeScanCurrentUserSID()
	if err != nil {
		return StoreExactUpdateRequest{}, fmt.Errorf("Store update user could not be identified: %w", err)
	}
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: pfn}
	target := firstNonEmpty(pkg.StoreProductID, pkg.StoreUpdateID)
	if !pkg.ExactActionTargetAvailable || target == "" {
		return StoreExactUpdateRequest{}, errors.New("Store update requires an exact verified Product ID or provider target")
	}
	productID := strings.TrimSpace(pkg.StoreProductID)
	updateID := strings.TrimSpace(pkg.StoreUpdateID)
	provider, err := exactStoreUpdateExecutionProvider(productID, updateID)
	if err != nil {
		return StoreExactUpdateRequest{}, err
	}
	request := StoreExactUpdateRequest{
		Identity:         identity,
		ProductID:        productID,
		UpdateID:         updateID,
		Target:           target,
		Provider:         provider,
		ScanID:           strings.TrimSpace(pkg.ScanID),
		ObservedAt:       strings.TrimSpace(pkg.ObservedAt),
		InstalledVersion: firstNonEmpty(pkg.InstalledVersion, pkg.Version),
		OfferedVersion:   firstNonEmpty(pkg.OfferedVersion, pkg.AvailableVersion),
	}
	if err := verifyPublishedStoreUpdateAssessment(ctx, request); err != nil {
		return StoreExactUpdateRequest{}, err
	}
	return request, nil
}

func exactStoreUpdateExecutionProvider(productID, updateID string) (StoreProviderIdentity, error) {
	if strings.TrimSpace(productID) != "" && packageActionManagerAvailable(managerWinget) {
		return StoreProviderIdentity{ID: managerWinget, Name: "WinGet Microsoft Store exact update", Backend: backendWingetMSStoreFallback}, nil
	}
	if (strings.TrimSpace(productID) != "" || strings.TrimSpace(updateID) != "") && packageActionManagerAvailable(managerStore) {
		return StoreProviderIdentity{ID: managerStore, Name: "Store CLI exact update", Backend: backendStoreCLI}, nil
	}
	return StoreProviderIdentity{}, errors.New("no exact Store update executor is available for the verified target")
}

func verifyPublishedStoreUpdateAssessment(ctx context.Context, request StoreExactUpdateRequest) error {
	if !storeTransactionalScanEnabled() {
		return nil
	}
	store, err := openStoreTransactionalStoreForInventory()
	if err != nil {
		return fmt.Errorf("could not open Store assessment store: %w", err)
	}
	defer store.Close()
	assessments, err := store.PublishedAssessments(ctx, request.Identity.UserSID)
	if err != nil {
		return fmt.Errorf("could not read Store assessments: %w", err)
	}
	for _, assessment := range assessments {
		if !assessment.Identity.Equal(request.Identity) {
			continue
		}
		if assessment.ScanID != request.ScanID {
			return errors.New("Store update assessment is not the latest published scan generation")
		}
		if assessment.State != StoreUpdateAvailable || assessment.Stale || !assessment.ExactActionTargetAvailable {
			return errors.New("published Store assessment is not a fresh exact available update")
		}
		if assessment.StoreProductID != "" && request.ProductID != "" && assessment.StoreProductID != request.ProductID {
			return errors.New("Store update Product ID does not match the verified published assessment")
		}
		if assessment.UpdateID != "" && request.UpdateID != "" && assessment.UpdateID != request.UpdateID {
			return errors.New("Store update ID does not match the verified published assessment")
		}
		return nil
	}
	return errors.New("no published Store assessment matches the selected package identity")
}

func storeSnapshotVerifiesUpdate(request StoreExactUpdateRequest, pre, post StoreExactPackageSnapshot) (bool, string) {
	if !post.Identity.Equal(request.Identity) || !post.Exists || !post.Healthy {
		return false, ""
	}
	if pre.Exists {
		if post.PackageFullName != "" && pre.PackageFullName != "" && !strings.EqualFold(post.PackageFullName, pre.PackageFullName) {
			return true, "Store update verified because the installed package full name changed."
		}
		if versionChanged(pre.Version, post.Version) {
			return true, "Store update verified because the installed package version changed."
		}
		return false, ""
	}
	if request.InstalledVersion != "" && versionChanged(request.InstalledVersion, post.Version) {
		return true, "Store update verified because the installed package version changed from the pre-action assessment."
	}
	return false, ""
}

func versionChanged(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && !strings.EqualFold(left, right)
}

func storeCatalogVerifiesUpdate(catalog StoreExactCatalogResult, post StoreExactPackageSnapshot) bool {
	return catalog.Authoritative && !catalog.OfferAvailable && catalog.InstalledHealthy && post.Exists && post.Healthy
}

func appendStoreExecutionDiagnostic(result CommandResult, label string, snapshot StoreExactPackageSnapshot, snapshotResult CommandResult) CommandResult {
	if snapshotResult.Command != "" {
		if result.Command == "" {
			result.Command = label + " inventory: " + snapshotResult.Command
		} else {
			result.Command += "\n" + label + " inventory: " + snapshotResult.Command
		}
		result.Stdout = appendDiagnosticLine(result.Stdout, snapshotResult.Stdout)
		result.Stderr = appendDiagnosticLine(result.Stderr, snapshotResult.Stderr)
	}
	if snapshot.Exists {
		result.Stdout = appendDiagnosticLine(result.Stdout, fmt.Sprintf("%s exact Store package: PFN=%s version=%s full_name=%s healthy=%t", label, snapshot.Identity.PackageFamilyName, snapshot.Version, snapshot.PackageFullName, snapshot.Healthy))
	} else {
		result.Stdout = appendDiagnosticLine(result.Stdout, fmt.Sprintf("%s exact Store package not found: PFN=%s", label, snapshot.Identity.PackageFamilyName))
	}
	if snapshot.Diagnostics != "" {
		result.Stdout = appendDiagnosticLine(result.Stdout, label+" diagnostics: "+sanitizeProviderDiagnostic(snapshot.Diagnostics))
	}
	return result
}

func appendDiagnosticLine(existing, line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return existing
	}
	existing = strings.TrimRight(existing, "\r\n")
	if existing == "" {
		return line
	}
	return existing + "\n" + line
}

type storeCLIExactUpdateRunner struct{}

func (storeCLIExactUpdateRunner) RunStoreUpdate(ctx context.Context, request StoreExactUpdateRequest) CommandResult {
	candidates := exactStoreUpdateRequestTargets(request)
	return runPackageUpdateCandidates(ctx, candidates, "store exact target", func(target string) CommandResult {
		return runStoreUpdateCommandWithApplyFallback(ctx, target)
	})
}

type storeProductIDFirstExactUpdateRunner struct{}

func (storeProductIDFirstExactUpdateRunner) RunStoreUpdate(ctx context.Context, request StoreExactUpdateRequest) CommandResult {
	productID := strings.TrimSpace(request.ProductID)
	if productID == "" || !packageActionManagerAvailable(managerWinget) {
		return storeCLIExactUpdateRunner{}.RunStoreUpdate(ctx, request)
	}
	wingetResult := runPackageActionCommand(ctx, managerWinget, packageActionTimeout, wingetMSStoreProductIDUpgradeCommand(productID)...)
	if wingetResult.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(wingetResult) {
		return wingetResult
	}
	appLog("WinGet msstore exact Product ID target %q was not accepted; trying Store CLI exact targets.", productID)
	storeResult := storeCLIExactUpdateRunner{}.RunStoreUpdate(ctx, request)
	return mergeCommandResults(wingetResult, storeResult, "Store CLI exact target fallback")
}

func exactStoreUpdateRequestTargets(request StoreExactUpdateRequest) []string {
	return uniqueUpdateTargets([]string{
		request.ProductID,
		request.UpdateID,
		request.Target,
	})
}

type storeProductIDFirstExactCatalogQueryProvider struct {
	Winget StoreExactCatalogProvider
	Store  StoreExactCatalogProvider
}

func (provider storeProductIDFirstExactCatalogQueryProvider) QueryExact(ctx context.Context, request StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	storeProvider := provider.Store
	if storeProvider == nil {
		storeProvider = storeCLIExactCatalogQueryProvider{}
	}
	if strings.TrimSpace(request.ProductID) == "" || !packageActionManagerAvailable(managerWinget) {
		return storeProvider.QueryExact(ctx, request)
	}
	wingetProvider := provider.Winget
	if wingetProvider == nil {
		wingetProvider = wingetMSStoreExactCatalogQueryProvider{}
	}
	wingetCatalog, wingetResult := wingetProvider.QueryExact(ctx, request)
	if wingetCatalog.Authoritative || ctx.Err() != nil || !packageActionManagerAvailable(managerStore) {
		return wingetCatalog, wingetResult
	}
	storeCatalog, storeResult := storeProvider.QueryExact(ctx, request)
	merged := mergeCommandResults(wingetResult, storeResult, "Store CLI exact catalog fallback")
	if storeCatalog.Authoritative {
		return storeCatalog, merged
	}
	if storeCatalog.Diagnostics != "" {
		wingetCatalog.Diagnostics = firstNonEmpty(wingetCatalog.Diagnostics, storeCatalog.Diagnostics)
	}
	return wingetCatalog, merged
}

type storePackagedSnapshotProvider struct {
	Provider StorePackagedAppInventoryProvider
}

func (provider storePackagedSnapshotProvider) Snapshot(ctx context.Context, identity StoreInstalledIdentity) (StoreExactPackageSnapshot, CommandResult) {
	inventoryProvider := provider.Provider
	if inventoryProvider == nil {
		inventoryProvider = storePackagedAppInventoryProvider()
	}
	scan := newStorePackagedAppScan(identity.UserSID)
	inventory, result := inventoryProvider.Inventory(ctx, scan)
	snapshot := StoreExactPackageSnapshot{Identity: identity, ObservedAt: time.Now().UTC()}
	if !result.OK {
		snapshot.Diagnostics = firstNonEmpty(result.Stderr, result.Stdout)
		return snapshot, result
	}
	for _, family := range inventory.Families {
		if !family.Identity.Equal(identity) {
			continue
		}
		snapshot.Exists = true
		snapshot.PackageFullName = family.Primary.PackageFullName
		snapshot.Version = family.Primary.Version.String()
		snapshot.Healthy = family.Primary.Status.OK
		if !snapshot.Healthy {
			snapshot.Diagnostics = family.Primary.Status.Raw
		}
		return snapshot, result
	}
	return snapshot, result
}

type unsupportedStoreExactCatalogProvider struct{}

func (unsupportedStoreExactCatalogProvider) QueryExact(context.Context, StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	return StoreExactCatalogResult{}, CommandResult{Command: "targeted Store catalog query", Code: 1, Stderr: "exact Store catalog provider is not implemented in this build"}
}

type noStorePackageEvents struct{}

func (noStorePackageEvents) Subscribe(context.Context, StoreInstalledIdentity) (<-chan StorePackageChangeEvent, func(), error) {
	ch := make(chan StorePackageChangeEvent)
	close(ch)
	return ch, nil, errors.New("current-user PackageCatalog event subscription is not implemented in this build")
}
