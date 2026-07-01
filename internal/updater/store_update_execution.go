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

	preActionSnapshot, preActionResult := executor.Inventory.Snapshot(ctx, request.Identity)
	if err := validateStorePreActionSnapshot(request, preActionSnapshot, preActionResult); err != nil {
		result := validationCommandResult("store exact update", err)
		return appendStoreExecutionDiagnostic(result, "pre-action", preActionSnapshot, preActionResult)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	eventSubscription := executor.subscribePackageEvents(verifyCtx, request.Identity)
	if eventSubscription.cleanup != nil {
		defer eventSubscription.cleanup()
	}
	if callbacks.Starting != nil {
		callbacks.Starting(request)
	}
	updateStartedAt := time.Now().UTC()
	updateResult := executor.Runner.RunStoreUpdate(ctx, request)
	updateResult = appendStoreExecutionDiagnostic(updateResult, "pre-action", preActionSnapshot, preActionResult)
	if ctx.Err() != nil {
		updateResult.OK = false
		updateResult.Code = commandCancelledCode
		updateResult.Stderr = appendDiagnosticLine(updateResult.Stderr, "Store update cancelled.")
		return updateResult
	}
	if updateResult.Code == commandCancelledCode {
		return updateResult
	}
	if !updateResult.OK {
		return updateResult
	}
	if callbacks.Accepted != nil {
		callbacks.Accepted(request, updateResult)
	}
	updateResult.Stdout = appendDiagnosticLine(updateResult.Stdout, "Store update request accepted; verifying exact package state.")

	if callbacks.Verifying != nil {
		callbacks.Verifying(request)
	}
	verificationResult := executor.verifyAcceptedActionWithEvents(verifyCtx, request, preActionSnapshot, pollEvery, updateStartedAt, eventSubscription)
	mergedResult := mergeCommandAttemptsWithFinalResult(updateResult, verificationResult.Result, "Store post-action verification")
	mergedResult = appendStoreExecutionDiagnostic(mergedResult, "post-action", verificationResult.Post, verificationResult.PostResult)
	if verificationResult.Verified {
		mergedResult.OK = true
		mergedResult.Code = 0
		mergedResult.Stdout = appendDiagnosticLine(mergedResult.Stdout, verificationResult.Message)
		mergedResult.Stderr = strings.TrimSpace(mergedResult.Stderr)
		return mergedResult
	}
	if verifyCtx.Err() != nil && errors.Is(verifyCtx.Err(), context.Canceled) {
		mergedResult.OK = false
		mergedResult.Code = commandCancelledCode
		mergedResult.Stderr = appendDiagnosticLine(mergedResult.Stderr, "Store update verification cancelled.")
		return mergedResult
	}
	mergedResult.OK = false
	mergedResult.Code = storeUpdateAcceptedNotVerifiedCode
	mergedResult.Stderr = appendDiagnosticLine(mergedResult.Stderr, firstNonEmpty(verificationResult.Message, "Store update request was accepted but final state could not be verified."))
	return mergedResult
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

func (executor StoreExactUpdateExecutor) verifyAcceptedAction(ctx context.Context, request StoreExactUpdateRequest, preActionSnapshot StoreExactPackageSnapshot, pollEvery time.Duration) storeExactVerificationResult {
	subscription := executor.subscribePackageEvents(ctx, request.Identity)
	if subscription.cleanup != nil {
		defer subscription.cleanup()
	}
	return executor.verifyAcceptedActionWithEvents(ctx, request, preActionSnapshot, pollEvery, time.Now().UTC(), subscription)
}

func (executor StoreExactUpdateExecutor) verifyAcceptedActionWithEvents(ctx context.Context, request StoreExactUpdateRequest, preActionSnapshot StoreExactPackageSnapshot, pollEvery time.Duration, actionStartedAt time.Time, subscription storePackageEventSubscription) storeExactVerificationResult {
	verificationResult := CommandResult{OK: true, Command: "store exact post-action verification"}
	if subscription.err != nil {
		verificationResult = mergeCommandAttemptsWithFinalResult(verificationResult, CommandResult{Command: "PackageCatalog event subscription", Code: 1, Stderr: sanitizeProviderDiagnostic(subscription.err.Error())}, "PackageCatalog events unavailable")
	}
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	eventCh := subscription.events
	seenEventKeys := map[string]bool{}
	for {
		postActionSnapshot, postActionResult := executor.Inventory.Snapshot(ctx, request.Identity)
		if verified, message := storeSnapshotVerifiesUpdate(request, preActionSnapshot, postActionSnapshot); verified {
			return storeExactVerificationResult{Verified: true, Message: message, Result: verificationResult, Post: postActionSnapshot, PostResult: postActionResult}
		}
		catalogEvidence, catalogCommandResult := executor.Catalog.QueryExact(ctx, request)
		verificationResult = mergeCommandAttemptsWithFinalResult(verificationResult, catalogCommandResult, "targeted Store catalog query")
		if storeCatalogVerifiesUpdate(request, preActionSnapshot, catalogEvidence, postActionSnapshot) {
			return storeExactVerificationResult{Verified: true, Message: "Store update verified because the exact offer disappeared after a fresh targeted catalog query.", Result: verificationResult, Post: postActionSnapshot, PostResult: postActionResult}
		}
		select {
		case <-ctx.Done():
			return storeExactVerificationResult{Message: ctx.Err().Error(), Result: verificationResult, Post: postActionSnapshot, PostResult: postActionResult}
		case event, ok := <-eventCh:
			if !ok {
				eventCh = nil
				verificationResult.Stdout = appendDiagnosticLine(verificationResult.Stdout, "PackageCatalog event channel closed; continuing with polling.")
				continue
			}
			if reason := validateStorePackageChangeEvent(request, actionStartedAt, event); reason != "" {
				verificationResult.Stdout = appendDiagnosticLine(verificationResult.Stdout, "Ignored Store package event: "+reason+".")
				continue
			}
			eventKey := storePackageEventKey(event)
			if seenEventKeys[eventKey] {
				verificationResult.Stdout = appendDiagnosticLine(verificationResult.Stdout, "Ignored duplicate Store package event.")
				continue
			}
			seenEventKeys[eventKey] = true
			verificationResult.Stdout = appendDiagnosticLine(verificationResult.Stdout, "PackageCatalog event received for exact Store package; refreshing inventory and catalog.")
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
	packageFamilyName := storeInstalledPackageFamilyName(pkg)
	if packageFamilyName == "" {
		return StoreExactUpdateRequest{}, errors.New("Store update requires an exact package family name")
	}
	userSID, err := storeScanCurrentUserSID()
	if err != nil {
		return StoreExactUpdateRequest{}, fmt.Errorf("Store update user could not be identified: %w", err)
	}
	identity := StoreInstalledIdentity{UserSID: userSID, PackageFamilyName: packageFamilyName}
	productID := strings.TrimSpace(pkg.StoreProductID)
	updateID := strings.TrimSpace(pkg.StoreUpdateID)
	if productID != "" && !looksLikeStoreProductID(productID) {
		return StoreExactUpdateRequest{}, errors.New("Store update Product ID is not a valid Microsoft Store Product ID")
	}
	exactActionTarget := firstNonEmpty(productID, updateID)
	if !pkg.ExactActionTargetAvailable || exactActionTarget == "" {
		return StoreExactUpdateRequest{}, errors.New("Store update requires an exact verified Product ID or provider target")
	}
	executionProvider, err := exactStoreUpdateExecutionProvider(productID, updateID)
	if err != nil {
		return StoreExactUpdateRequest{}, err
	}
	request := StoreExactUpdateRequest{
		Identity:                identity,
		ProductID:               productID,
		UpdateID:                updateID,
		Target:                  exactActionTarget,
		Provider:                executionProvider,
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
	productID = strings.TrimSpace(productID)
	updateID = strings.TrimSpace(updateID)
	if productID != "" && !looksLikeStoreProductID(productID) {
		return StoreProviderIdentity{}, errors.New("Store update Product ID is not a valid Microsoft Store Product ID")
	}
	if productID != "" && packageActionManagerAvailable(managerWinget) {
		return StoreProviderIdentity{ID: managerWinget, Name: "WinGet Microsoft Store exact update", Backend: backendWingetMSStoreFallback}, nil
	}
	if (productID != "" || updateID != "") && packageActionManagerAvailable(managerStore) {
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

func storeSnapshotVerifiesUpdate(request StoreExactUpdateRequest, preActionSnapshot, postActionSnapshot StoreExactPackageSnapshot) (bool, string) {
	if !postActionSnapshot.Identity.Equal(request.Identity) || !postActionSnapshot.Exists || !postActionSnapshot.Healthy {
		return false, ""
	}
	baselineVersion := storeUpdateVerificationBaselineVersion(request, preActionSnapshot)
	if !storeAssessmentVersionKnown(baselineVersion) {
		return false, ""
	}
	versionComparison, ok := compareStorePackageVersions(postActionSnapshot.Version, baselineVersion)
	if !ok || versionComparison <= 0 {
		return false, ""
	}
	if storeAssessmentVersionKnown(request.OfferedVersion) {
		offeredComparison, ok := compareStorePackageVersions(postActionSnapshot.Version, request.OfferedVersion)
		if !ok || offeredComparison < 0 {
			return false, ""
		}
		return true, "Store update verified because the installed package version increased to the offered version or newer."
	}
	return true, "Store update verified because the installed package version increased."
}

func storeUpdateVerificationBaselineVersion(request StoreExactUpdateRequest, preActionSnapshot StoreExactPackageSnapshot) string {
	if preActionSnapshot.Exists {
		return strings.TrimSpace(preActionSnapshot.Version)
	}
	return strings.TrimSpace(request.InstalledVersion)
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
	packageClassification := strings.TrimSpace(event.Classification)
	if packageClassification == storePackageClassFramework || packageClassification == storePackageClassResource {
		return "framework or resource package event"
	}
	expectedPackageName := storePackageNameFromFamilyName(request.Identity.PackageFamilyName)
	if strings.TrimSpace(event.PackageFullName) == "" || expectedPackageName == "" || !strings.HasPrefix(strings.TrimSpace(event.PackageFullName), expectedPackageName+"_") {
		return "package full name mismatch"
	}
	if _, ok := compareStorePackageVersions(event.Version, event.Version); !ok {
		return "malformed package version"
	}
	return ""
}

func storePackageNameFromFamilyName(packageFamilyName string) string {
	packageFamilyName = strings.TrimSpace(packageFamilyName)
	index := strings.LastIndex(packageFamilyName, "_")
	if index <= 0 {
		return ""
	}
	return packageFamilyName[:index]
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

func validateStorePreActionSnapshot(request StoreExactUpdateRequest, preActionSnapshot StoreExactPackageSnapshot, preActionResult CommandResult) error {
	if !preActionResult.OK {
		return errors.New("Store update requires a successful fresh package enumeration before execution")
	}
	if !preActionSnapshot.Identity.Equal(request.Identity) {
		return errors.New("Store update pre-action package enumeration returned the wrong identity")
	}
	if !preActionSnapshot.Exists {
		return errors.New("Store update requires the exact package family to still be installed")
	}
	if !preActionSnapshot.Healthy {
		return errors.New("Store update requires the exact package family to be healthy before execution")
	}
	if !storeAssessmentVersionKnown(preActionSnapshot.Version) {
		return errors.New("Store update requires a known current installed version before execution")
	}
	if !strings.EqualFold(strings.TrimSpace(preActionSnapshot.Version), strings.TrimSpace(request.InstalledVersion)) {
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
	versionParts := make([]int, 0, len(segments))
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
		versionParts = append(versionParts, part)
	}
	return versionParts, true
}

func storeCatalogVerifiesUpdate(request StoreExactUpdateRequest, preActionSnapshot StoreExactPackageSnapshot, catalogEvidence StoreExactCatalogResult, postActionSnapshot StoreExactPackageSnapshot) bool {
	if !catalogEvidence.Authoritative || catalogEvidence.OfferAvailable || !catalogEvidence.InstalledHealthy || !postActionSnapshot.Exists || !postActionSnapshot.Healthy {
		return false
	}
	baselineVersion := storeUpdateVerificationBaselineVersion(request, preActionSnapshot)
	if storeAssessmentVersionKnown(baselineVersion) {
		comparison, ok := compareStorePackageVersions(postActionSnapshot.Version, baselineVersion)
		if !ok || comparison < 0 {
			return false
		}
	}
	if storeAssessmentVersionKnown(request.OfferedVersion) {
		comparison, ok := compareStorePackageVersions(postActionSnapshot.Version, request.OfferedVersion)
		return ok && comparison >= 0
	}
	return true
}

func appendStoreExecutionDiagnostic(result CommandResult, phaseLabel string, snapshot StoreExactPackageSnapshot, inventoryResult CommandResult) CommandResult {
	if inventoryResult.Command != "" {
		if result.Command == "" {
			result.Command = phaseLabel + " inventory: " + inventoryResult.Command
		} else {
			result.Command += "\n" + phaseLabel + " inventory: " + inventoryResult.Command
		}
		result.Stdout = appendDiagnosticLine(result.Stdout, inventoryResult.Stdout)
		result.Stderr = appendDiagnosticLine(result.Stderr, inventoryResult.Stderr)
	}
	if snapshot.Exists {
		result.Stdout = appendDiagnosticLine(result.Stdout, fmt.Sprintf("%s exact Store package: PFN=%s version=%s full_name=%s healthy=%t", phaseLabel, snapshot.Identity.PackageFamilyName, snapshot.Version, snapshot.PackageFullName, snapshot.Healthy))
	} else {
		result.Stdout = appendDiagnosticLine(result.Stdout, fmt.Sprintf("%s exact Store package not found: PFN=%s", phaseLabel, snapshot.Identity.PackageFamilyName))
	}
	if snapshot.Diagnostics != "" {
		result.Stdout = appendDiagnosticLine(result.Stdout, phaseLabel+" diagnostics: "+sanitizeProviderDiagnostic(snapshot.Diagnostics))
	}
	return result
}

func appendDiagnosticLine(existingText, newLine string) string {
	newLine = strings.TrimSpace(newLine)
	if newLine == "" {
		return existingText
	}
	existingText = strings.TrimRight(existingText, "\r\n")
	if existingText == "" {
		return newLine
	}
	return existingText + "\n" + newLine
}

type storeCLIExactUpdateRunner struct{}

func (storeCLIExactUpdateRunner) RunStoreUpdate(ctx context.Context, request StoreExactUpdateRequest) CommandResult {
	exactTargets := exactStoreUpdateRequestTargets(request)
	return runPackageUpdateCandidates(ctx, exactTargets, "store exact target", func(target string) CommandResult {
		return runStoreUpdateCommandWithApplyFallback(ctx, target)
	})
}

type storeProductIDFirstExactUpdateRunner struct{}

func (storeProductIDFirstExactUpdateRunner) RunStoreUpdate(ctx context.Context, request StoreExactUpdateRequest) CommandResult {
	productID := strings.TrimSpace(request.ProductID)
	if productID != "" && !looksLikeStoreProductID(productID) {
		return validationCommandResult("Store exact update", errors.New("Store update Product ID is not a valid Microsoft Store Product ID"))
	}
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
	storeCLIProvider := provider.Store
	if storeCLIProvider == nil {
		storeCLIProvider = storeCLIExactCatalogQueryProvider{}
	}
	productID := strings.TrimSpace(request.ProductID)
	if productID != "" && !looksLikeStoreProductID(productID) {
		return StoreExactCatalogResult{}, validationCommandResult("Store exact catalog query", errors.New("Store update Product ID is not a valid Microsoft Store Product ID"))
	}
	if productID == "" || !packageActionManagerAvailable(managerWinget) {
		return storeCLIProvider.QueryExact(ctx, request)
	}
	wingetProvider := provider.Winget
	if wingetProvider == nil {
		wingetProvider = wingetMSStoreExactCatalogQueryProvider{}
	}
	wingetEvidence, wingetCommandResult := wingetProvider.QueryExact(ctx, request)
	if wingetEvidence.Authoritative || ctx.Err() != nil || !packageActionManagerAvailable(managerStore) {
		return wingetEvidence, wingetCommandResult
	}
	storeEvidence, storeCommandResult := storeCLIProvider.QueryExact(ctx, request)
	mergedResult := mergeCommandAttemptsWithFinalResult(wingetCommandResult, storeCommandResult, "Store CLI exact catalog fallback")
	if storeEvidence.Authoritative {
		return storeEvidence, mergedResult
	}
	if storeEvidence.Diagnostics != "" {
		wingetEvidence.Diagnostics = firstNonEmpty(wingetEvidence.Diagnostics, storeEvidence.Diagnostics)
	}
	return wingetEvidence, mergedResult
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
	inventory, inventoryResult := inventoryProvider.Inventory(ctx, scan)
	packageSnapshot := StoreExactPackageSnapshot{Identity: identity, ObservedAt: time.Now().UTC()}
	if !inventoryResult.OK {
		packageSnapshot.Diagnostics = firstNonEmpty(inventoryResult.Stderr, inventoryResult.Stdout)
		return packageSnapshot, inventoryResult
	}
	for _, appFamily := range inventory.Families {
		if !appFamily.Identity.Equal(identity) {
			continue
		}
		packageSnapshot.Exists = true
		packageSnapshot.PackageFullName = appFamily.Primary.PackageFullName
		packageSnapshot.Version = appFamily.Primary.Version.String()
		packageSnapshot.Healthy = appFamily.Primary.Status.OK
		if !packageSnapshot.Healthy {
			packageSnapshot.Diagnostics = appFamily.Primary.Status.Raw
		}
		return packageSnapshot, inventoryResult
	}
	return packageSnapshot, inventoryResult
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
