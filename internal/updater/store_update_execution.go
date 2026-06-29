package updater

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	storeExactUpdateVerifyTimeout      = 2 * time.Minute
	storeExactUpdatePollInterval       = 3 * time.Second
	storeUpdateAcceptedNotVerifiedCode = 202
)

type StoreExactUpdateRequest struct {
	Identity                StoreInstalledIdentity
	ProductID               string
	UpdateID                string
	Target                  string
	Provider                StoreProviderIdentity
	ScanID                  string
	ObservedAt              string
	InstalledVersion        string
	CurrentInstalledVersion string
	OfferedVersion          string
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
	Exists          bool
	ObservedAt      time.Time
	Classification  string
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
		Events:    packageCatalogEventSource{},
		Timeout:   storeExactUpdateVerifyTimeout,
		PollEvery: storeExactUpdatePollInterval,
	}
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
	if err := validateStorePreActionSnapshot(request, pre, preResult); err != nil {
		result := validationCommandResult("store exact update", err)
		return appendStoreExecutionDiagnostic(result, "pre-action", pre, preResult)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	subscription := executor.subscribePackageEvents(verifyCtx, request.Identity)
	if subscription.cleanup != nil {
		defer subscription.cleanup()
	}
	if callbacks.Starting != nil {
		callbacks.Starting(request)
	}
	actionStartedAt := time.Now().UTC()
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

	if callbacks.Verifying != nil {
		callbacks.Verifying(request)
	}
	verification := executor.verifyAcceptedActionWithEvents(verifyCtx, request, pre, pollEvery, actionStartedAt, subscription)
	merged := mergeCommandAttemptsWithFinalResult(action, verification.Result, "Store post-action verification")
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

type storePackageEventSubscription struct {
	events  <-chan StorePackageChangeEvent
	cleanup func()
	err     error
}

func (executor StoreExactUpdateExecutor) subscribePackageEvents(ctx context.Context, identity StoreInstalledIdentity) storePackageEventSubscription {
	events, cleanup, eventErr := executor.Events.Subscribe(ctx, identity)
	return storePackageEventSubscription{events: events, cleanup: cleanup, err: eventErr}
}

func (executor StoreExactUpdateExecutor) verifyAcceptedAction(ctx context.Context, request StoreExactUpdateRequest, pre StoreExactPackageSnapshot, pollEvery time.Duration) storeExactVerificationResult {
	subscription := executor.subscribePackageEvents(ctx, request.Identity)
	if subscription.cleanup != nil {
		defer subscription.cleanup()
	}
	return executor.verifyAcceptedActionWithEvents(ctx, request, pre, pollEvery, time.Now().UTC(), subscription)
}

func (executor StoreExactUpdateExecutor) verifyAcceptedActionWithEvents(ctx context.Context, request StoreExactUpdateRequest, pre StoreExactPackageSnapshot, pollEvery time.Duration, actionStartedAt time.Time, subscription storePackageEventSubscription) storeExactVerificationResult {
	result := CommandResult{OK: true, Command: "store exact post-action verification"}
	if subscription.err != nil {
		result = mergeCommandAttemptsWithFinalResult(result, CommandResult{Command: "PackageCatalog event subscription", Code: 1, Stderr: sanitizeProviderDiagnostic(subscription.err.Error())}, "PackageCatalog events unavailable")
	}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	events := subscription.events
	seenEvents := map[string]bool{}
	for {
		post, postResult := executor.Inventory.Snapshot(ctx, request.Identity)
		if verified, message := storeSnapshotVerifiesUpdate(request, pre, post); verified {
			return storeExactVerificationResult{Verified: true, Message: message, Result: result, Post: post, PostResult: postResult}
		}
		catalog, catalogResult := executor.Catalog.QueryExact(ctx, request)
		result = mergeCommandAttemptsWithFinalResult(result, catalogResult, "targeted Store catalog query")
		if storeCatalogVerifiesUpdate(request, pre, catalog, post) {
			return storeExactVerificationResult{Verified: true, Message: "Store update verified because the exact offer disappeared after a fresh targeted catalog query.", Result: result, Post: post, PostResult: postResult}
		}
		select {
		case <-ctx.Done():
			return storeExactVerificationResult{Message: ctx.Err().Error(), Result: result, Post: post, PostResult: postResult}
		case event, ok := <-events:
			if !ok {
				events = nil
				result.Stdout = appendDiagnosticLine(result.Stdout, "PackageCatalog event channel closed; continuing with polling.")
				continue
			}
			if reason := validateStorePackageChangeEvent(request, actionStartedAt, event); reason != "" {
				result.Stdout = appendDiagnosticLine(result.Stdout, "Ignored Store package event: "+reason+".")
				continue
			}
			key := storePackageEventKey(event)
			if seenEvents[key] {
				result.Stdout = appendDiagnosticLine(result.Stdout, "Ignored duplicate Store package event.")
				continue
			}
			seenEvents[key] = true
			result.Stdout = appendDiagnosticLine(result.Stdout, "PackageCatalog event received for exact Store package; refreshing inventory and catalog.")
		case <-ticker.C:
		}
	}
}

func exactStoreUpdateRequestFromPackage(ctx context.Context, pkg Package) (StoreExactUpdateRequest, error) {
	if pkg.Manager != managerStore {
		return StoreExactUpdateRequest{}, errors.New("exact Store update execution requires a Store package")
	}
	if policy := packageUpdatePolicy(pkg, UpdateOptions{AllowUnknownVersion: pkg.AllowUnknownVersionUpdate, AllowPinned: pkg.AllowPinnedUpdate}); !policy.CanUpdateNow {
		return StoreExactUpdateRequest{}, errors.New(firstNonEmpty(policy.CannotUpdateReason, "Store package is not actionable"))
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
		Identity:                identity,
		ProductID:               productID,
		UpdateID:                updateID,
		Target:                  target,
		Provider:                provider,
		ScanID:                  strings.TrimSpace(pkg.ScanID),
		ObservedAt:              strings.TrimSpace(pkg.ObservedAt),
		InstalledVersion:        firstNonEmpty(pkg.InstalledVersion, pkg.Version),
		CurrentInstalledVersion: firstNonEmpty(pkg.Version, pkg.InstalledVersion),
		OfferedVersion:          firstNonEmpty(pkg.OfferedVersion, pkg.AvailableVersion),
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
	repository, err := openStoreTransactionalStoreForInventory()
	if err != nil {
		return fmt.Errorf("could not open Store assessment store: %w", err)
	}
	defer repository.Close()
	snapshot, ok, err := repository.LoadLatestPublishedSnapshot(ctx, request.Identity.UserSID)
	if err != nil {
		return fmt.Errorf("could not read Store assessments: %w", err)
	}
	if !ok {
		return errors.New("no published Store assessment matches the selected package identity")
	}
	for _, assessment := range snapshot.Assessments {
		if !assessment.Identity.Equal(request.Identity) {
			continue
		}
		if assessment.ScanID != request.ScanID {
			return errors.New("Store update assessment is not the latest published scan generation")
		}
		if assessment.State != StoreUpdateAvailable || assessment.Stale || !assessment.ExactActionTargetAvailable {
			return errors.New("published Store assessment is not a fresh exact available update")
		}
		freshness := evaluatePublishedStoreAssessmentFreshness(snapshot, assessment, request.CurrentInstalledVersion, storeScanNow())
		if !freshness.Fresh {
			return fmt.Errorf("published Store assessment is not fresh: %s", freshness.Reason)
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
	baselineVersion := strings.TrimSpace(request.InstalledVersion)
	if pre.Exists {
		baselineVersion = strings.TrimSpace(pre.Version)
	}
	if !storeAssessmentVersionKnown(baselineVersion) {
		return false, ""
	}
	versionComparison, ok := compareStorePackageVersions(post.Version, baselineVersion)
	if !ok || versionComparison <= 0 {
		return false, ""
	}
	if storeAssessmentVersionKnown(request.OfferedVersion) {
		offeredComparison, ok := compareStorePackageVersions(post.Version, request.OfferedVersion)
		if !ok || offeredComparison < 0 {
			return false, ""
		}
		return true, "Store update verified because the installed package version increased to the offered version or newer."
	}
	return true, "Store update verified because the installed package version increased."
}

func validateStorePackageChangeEvent(request StoreExactUpdateRequest, actionStartedAt time.Time, event StorePackageChangeEvent) string {
	if !event.Identity.Equal(request.Identity) {
		return "identity mismatch"
	}
	if event.ObservedAt.IsZero() || !event.ObservedAt.After(actionStartedAt) {
		return "event predates Store action"
	}
	if !event.Exists {
		return "package removal is not update proof"
	}
	if !event.Healthy {
		return "package is not healthy"
	}
	classification := strings.TrimSpace(event.Classification)
	if classification == storePackageClassFramework || classification == storePackageClassResource {
		return "framework or resource package event"
	}
	identityName := storeIdentityNameFromPFN(request.Identity.PackageFamilyName)
	if strings.TrimSpace(event.PackageFullName) == "" || identityName == "" || !strings.HasPrefix(strings.TrimSpace(event.PackageFullName), identityName+"_") {
		return "package full name mismatch"
	}
	if _, ok := compareStorePackageVersions(event.Version, event.Version); !ok {
		return "malformed package version"
	}
	return ""
}

func storeIdentityNameFromPFN(pfn string) string {
	pfn = strings.TrimSpace(pfn)
	index := strings.LastIndex(pfn, "_")
	if index <= 0 {
		return ""
	}
	return pfn[:index]
}

func storePackageEventKey(event StorePackageChangeEvent) string {
	return strings.ToLower(strings.Join([]string{
		event.Identity.UserSID,
		event.Identity.PackageFamilyName,
		event.PackageFullName,
		event.Version,
		event.Classification,
	}, "\x00"))
}

func validateStorePreActionSnapshot(request StoreExactUpdateRequest, pre StoreExactPackageSnapshot, preResult CommandResult) error {
	if !preResult.OK {
		return errors.New("Store update requires a successful fresh package enumeration before execution")
	}
	if !pre.Identity.Equal(request.Identity) {
		return errors.New("Store update pre-action package enumeration returned the wrong identity")
	}
	if !pre.Exists {
		return errors.New("Store update requires the exact package family to still be installed")
	}
	if !pre.Healthy {
		return errors.New("Store update requires the exact package family to be healthy before execution")
	}
	if !storeAssessmentVersionKnown(pre.Version) {
		return errors.New("Store update requires a known current installed version before execution")
	}
	if !strings.EqualFold(strings.TrimSpace(pre.Version), strings.TrimSpace(request.InstalledVersion)) {
		return errors.New("Store update assessment no longer matches the installed package version")
	}
	return nil
}

func compareStorePackageVersions(left, right string) (int, bool) {
	leftParts, ok := parseStorePackageVersion(left)
	if !ok {
		return 0, false
	}
	rightParts, ok := parseStorePackageVersion(right)
	if !ok {
		return 0, false
	}
	maxParts := len(leftParts)
	if len(rightParts) > maxParts {
		maxParts = len(rightParts)
	}
	for index := 0; index < maxParts; index++ {
		leftPart := 0
		rightPart := 0
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		switch {
		case leftPart > rightPart:
			return 1, true
		case leftPart < rightPart:
			return -1, true
		}
	}
	return 0, true
}

func parseStorePackageVersion(value string) ([]int, bool) {
	value = strings.TrimSpace(value)
	if !storeAssessmentVersionKnown(value) {
		return nil, false
	}
	segments := strings.Split(value, ".")
	parts := make([]int, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			return nil, false
		}
		for _, char := range segment {
			if char < '0' || char > '9' {
				return nil, false
			}
		}
		part, err := strconv.Atoi(segment)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
	}
	return parts, true
}

func storeCatalogVerifiesUpdate(request StoreExactUpdateRequest, pre StoreExactPackageSnapshot, catalog StoreExactCatalogResult, post StoreExactPackageSnapshot) bool {
	if !catalog.Authoritative || catalog.OfferAvailable || !catalog.InstalledHealthy || !post.Exists || !post.Healthy {
		return false
	}
	baselineVersion := strings.TrimSpace(request.InstalledVersion)
	if pre.Exists {
		baselineVersion = strings.TrimSpace(pre.Version)
	}
	if storeAssessmentVersionKnown(baselineVersion) {
		comparison, ok := compareStorePackageVersions(post.Version, baselineVersion)
		if !ok || comparison < 0 {
			return false
		}
	}
	if storeAssessmentVersionKnown(request.OfferedVersion) {
		comparison, ok := compareStorePackageVersions(post.Version, request.OfferedVersion)
		return ok && comparison >= 0
	}
	return true
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
	return mergeCommandAttemptsWithFinalResult(wingetResult, storeResult, "Store CLI exact target fallback")
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
	merged := mergeCommandAttemptsWithFinalResult(wingetResult, storeResult, "Store CLI exact catalog fallback")
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
