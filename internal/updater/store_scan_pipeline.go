package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	storeProviderTimeoutEnv       = "UPDATER_STORE_PROVIDER_TIMEOUT_SECONDS"
	defaultStoreCatalogProviderID = "store-catalog-unimplemented"
	defaultStoreProviderTimeout   = 6 * time.Minute
)

var (
	errStoreScanAlreadyRunning = errors.New("a Store scan is already running")
	storeScanNow               = func() time.Time { return time.Now().UTC() }
	storeScanCurrentUserSID    = currentUserSID
)

type StoreCatalogProvider interface {
	Identity() StoreProviderIdentity
	Observe(context.Context, StoreScanGeneration, []StorePackagedAppFamily) StoreCatalogProviderRun
}

type StoreCatalogProviderRun struct {
	Provider     StoreProviderIdentity
	Version      string
	StartedAt    time.Time
	CompletedAt  time.Time
	Health       StoreProviderHealth
	Error        string
	Observations []StoreProviderObservation
	Mappings     []VerifiedStoreIdentityMapping
}

type StoreScanPipeline struct {
	Repository        StoreScanRepository
	InventoryProvider StorePackagedAppInventoryProvider
	CatalogProviders  []StoreCatalogProvider
	ProviderTimeout   time.Duration
	Now               func() time.Time
	NewScanID         func(time.Time) string
	BeforeCommit      func(context.Context, StoreScanSnapshot) error

	mu      sync.Mutex
	running bool
}

type StoreScanResult struct {
	Scan         StoreScanGeneration
	Published    bool
	Assessments  []StorePublishedAssessment
	ProviderRuns []StoreCatalogProviderRun
	Inventory    StorePackagedAppInventory
}

func defaultStoreScanPipeline(repository StoreScanRepository) *StoreScanPipeline {
	return defaultStoreScanPipelineContext(context.Background(), repository)
}

func defaultStoreScanPipelineContext(ctx context.Context, repository StoreScanRepository) *StoreScanPipeline {
	managers := detectManagersContext(ctx)
	storeVersion := managers[managerStore].Version
	wingetVersion := managers[managerWinget].Version
	return &StoreScanPipeline{
		Repository:        repository,
		InventoryProvider: storePackagedAppInventoryProvider(),
		CatalogProviders: []StoreCatalogProvider{
			storeCLIExactCatalogProvider{Version: storeVersion},
			storeCLIUpdatesCatalogProvider{Version: storeVersion},
			wingetMSStoreExactCatalogProvider{Version: wingetVersion},
		},
		ProviderTimeout: configuredStoreProviderTimeout(),
		Now:             storeScanNow,
	}
}

func runDefaultStoreScanPipeline(ctx context.Context) (StoreScanResult, error) {
	repository, err := openDefaultStoreScanRepository()
	if err != nil {
		return StoreScanResult{}, err
	}
	defer repository.Close()
	return defaultStoreScanPipelineContext(ctx, repository).Run(ctx)
}

func (pipeline *StoreScanPipeline) Run(ctx context.Context) (StoreScanResult, error) {
	if pipeline == nil || pipeline.Repository == nil {
		return StoreScanResult{}, errors.New("Store scan pipeline has no repository")
	}
	userSID, err := storeScanCurrentUserSID()
	if err != nil {
		return StoreScanResult{}, err
	}
	releaseScan, err := defaultStoreScanCoordinator.acquire(ctx, userSID)
	if err != nil {
		return StoreScanResult{}, err
	}
	defer releaseScan()
	if !pipeline.tryStart() {
		return StoreScanResult{}, errStoreScanAlreadyRunning
	}
	defer pipeline.finish()

	now := pipeline.now()
	systemContext := currentStoreScanSystemContext()
	scan := StoreScanGeneration{
		ScanID:           pipeline.scanID(now),
		UserSID:          userSID,
		StartedAt:        now,
		WindowsVersion:   systemContext.WindowsVersion,
		WindowsBuild:     systemContext.WindowsBuild,
		Architecture:     systemContext.Architecture,
		ProviderVersions: map[string]string{},
		ProviderHealth:   map[string]StoreProviderHealth{},
		CompletionStatus: StoreScanRunning,
	}

	inventory, inventoryRun := pipeline.collectInventory(ctx, scan)
	providerRuns := append([]StoreCatalogProviderRun{inventoryRun}, pipeline.runCatalogProviders(ctx, scan, inventory.Families)...)
	scan.CompletedAt = pipeline.now()
	scan.ProviderHealth = providerHealthMap(providerRuns)
	scan.ProviderVersions = providerVersionMap(providerRuns)
	scan.CompletionStatus = scanCompletionStatus(inventory, providerRuns)

	previous, err := pipeline.previousAssessments(ctx, userSID)
	if err != nil {
		assessments := reconcileStoreScanAssessments(scan, inventory.Families, providerRuns, nil)
		return StoreScanResult{Scan: scan, Inventory: inventory, ProviderRuns: providerRuns, Assessments: assessments}, fmt.Errorf("could not load previous published Store assessments for hysteresis: %w", err)
	}
	assessments := reconcileStoreScanAssessments(scan, inventory.Families, providerRuns, previous)
	publish := scanShouldPublish(scan, inventory)
	snapshot := snapshotFromScanResult(scan, inventory, providerRuns, assessments, publish)
	if pipeline.BeforeCommit != nil {
		if err := pipeline.BeforeCommit(ctx, snapshot); err != nil {
			return StoreScanResult{Scan: scan, Inventory: inventory, ProviderRuns: providerRuns, Assessments: assessments}, err
		}
	}
	published, err := pipeline.Repository.PersistCompletedScanSnapshot(ctx, snapshot)
	if err != nil {
		return StoreScanResult{Scan: scan, Inventory: inventory, ProviderRuns: providerRuns, Assessments: assessments}, err
	}
	return StoreScanResult{Scan: scan, Published: published, Inventory: inventory, ProviderRuns: providerRuns, Assessments: assessments}, nil
}

func (pipeline *StoreScanPipeline) tryStart() bool {
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.running {
		return false
	}
	pipeline.running = true
	return true
}

func (pipeline *StoreScanPipeline) finish() {
	pipeline.mu.Lock()
	pipeline.running = false
	pipeline.mu.Unlock()
}

func (pipeline *StoreScanPipeline) now() time.Time {
	if pipeline.Now != nil {
		return pipeline.Now().UTC()
	}
	return time.Now().UTC()
}

func (pipeline *StoreScanPipeline) scanID(now time.Time) string {
	if pipeline.NewScanID != nil {
		return pipeline.NewScanID(now)
	}
	return fmt.Sprintf("store-scan-%d", now.UnixNano())
}

func (pipeline *StoreScanPipeline) collectInventory(ctx context.Context, scan StoreScanGeneration) (StorePackagedAppInventory, StoreCatalogProviderRun) {
	started := pipeline.now()
	provider := pipeline.InventoryProvider
	if provider == nil {
		provider = storePackagedAppInventoryProvider()
	}
	inventory, result := provider.Inventory(ctx, scan)
	run := StoreCatalogProviderRun{
		Provider:    StoreProviderIdentity{ID: "store-current-user-inventory", Name: "Current-user packaged app inventory", Backend: "winrt"},
		StartedAt:   started,
		CompletedAt: pipeline.now(),
		Health:      StoreProviderHealthy,
	}
	if ctx.Err() != nil {
		run.Health = StoreProviderFailed
		run.Error = ctx.Err().Error()
		inventory.Partial = true
		inventory.Errors = append(inventory.Errors, run.Error)
		return inventory, run
	}
	if !result.OK || inventory.Partial || inventory.Scan.CompletionStatus != StoreScanCompleted {
		run.Health = StoreProviderIncomplete
		if result.Stderr != "" {
			run.Error = result.Stderr
		} else if len(inventory.Errors) > 0 {
			run.Error = inventory.Errors[0]
		} else {
			run.Error = "inventory provider returned incomplete results"
		}
	}
	for _, family := range inventory.Families {
		if family.Identity.UserSID != scan.UserSID || family.Identity.PackageFamilyName == "" {
			run.Health = StoreProviderFailed
			run.Error = "inventory provider returned wrong-user or unresolved package identity"
			inventory.Partial = true
			break
		}
	}
	return inventory, run
}

func (pipeline *StoreScanPipeline) runCatalogProviders(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) []StoreCatalogProviderRun {
	providers := pipeline.CatalogProviders
	if len(providers) == 0 {
		providers = []StoreCatalogProvider{unsupportedStoreCatalogProvider{}}
	}
	timeout := pipeline.ProviderTimeout
	if timeout <= 0 {
		timeout = configuredStoreProviderTimeout()
	}
	runs := make([]StoreCatalogProviderRun, len(providers))
	var wg sync.WaitGroup
	for index, provider := range providers {
		index, provider := index, provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			started := pipeline.now()
			run := provider.Observe(runCtx, scan, families)
			if run.Provider.Key() == "" {
				run.Provider = provider.Identity()
			}
			if run.StartedAt.IsZero() {
				run.StartedAt = started
			}
			if run.CompletedAt.IsZero() {
				run.CompletedAt = pipeline.now()
			}
			if runCtx.Err() != nil {
				run.Health = StoreProviderFailed
				run.Error = runCtx.Err().Error()
			}
			run = sanitizeCatalogProviderRun(scan, run)
			run = synthesizeMissingProviderObservations(scan, run, families)
			runs[index] = sanitizeCatalogProviderRun(scan, run)
		}()
	}
	wg.Wait()
	return runs
}

func synthesizeMissingProviderObservations(scan StoreScanGeneration, run StoreCatalogProviderRun, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	if run.Health == StoreProviderHealthy {
		return run
	}
	kind := observationKindForProviderHealth(run.Health)
	seen := map[string]bool{}
	for _, observation := range run.Observations {
		if observation.Identity.UserSID == scan.UserSID && observation.Identity.PackageFamilyName != "" {
			seen[strings.ToLower(observation.Identity.PackageFamilyName)] = true
		}
	}
	observedAt := run.CompletedAt
	if observedAt.IsZero() {
		observedAt = scan.CompletedAt
	}
	if observedAt.IsZero() {
		observedAt = scan.StartedAt
	}
	diagnostics := sanitizeProviderDiagnostic(firstNonEmpty(run.Error, "provider did not return complete evidence for this package family"))
	for _, family := range families {
		if !family.ProductLike || !family.Identity.Resolved() || family.Identity.UserSID != scan.UserSID {
			continue
		}
		key := strings.ToLower(family.Identity.PackageFamilyName)
		if seen[key] {
			continue
		}
		run.Observations = append(run.Observations, StoreProviderObservation{
			Provider:         run.Provider,
			Health:           run.Health,
			Kind:             kind,
			Identity:         family.Identity,
			ScanID:           scan.ScanID,
			ObservedAt:       observedAt,
			InstalledVersion: family.Primary.Version.String(),
			Diagnostics:      diagnostics,
		})
	}
	return run
}

func observationKindForProviderHealth(health StoreProviderHealth) StoreObservationKind {
	switch health {
	case StoreProviderFailed:
		return StoreObservationProviderFailure
	case StoreProviderUnsupported:
		return StoreObservationUnsupportedProvider
	case StoreProviderStale:
		return StoreObservationStaleResult
	default:
		return StoreObservationIncompleteResult
	}
}

func sanitizeCatalogProviderRun(scan StoreScanGeneration, run StoreCatalogProviderRun) StoreCatalogProviderRun {
	if run.Provider.Key() == "" {
		run.Provider.ID = "unknown-provider"
	}
	if run.Health == "" {
		run.Health = StoreProviderIncomplete
	}
	filtered := run.Observations[:0]
	for _, observation := range run.Observations {
		if observation.Provider.Key() == "" {
			observation.Provider = run.Provider
		}
		if observation.ScanID != scan.ScanID || observation.Identity.UserSID != scan.UserSID || !observation.Identity.Resolved() {
			run.Health = StoreProviderFailed
			run.Error = firstNonEmpty(run.Error, "provider returned cross-user, cross-scan, or unresolved evidence")
			continue
		}
		if observation.ObservedAt.IsZero() {
			observation.ObservedAt = run.CompletedAt
		}
		filtered = append(filtered, observation)
	}
	run.Observations = filtered
	mappings := run.Mappings[:0]
	for _, mapping := range run.Mappings {
		if mapping.Provider.Key() == "" {
			mapping.Provider = run.Provider
		}
		if !mapping.VerifiedFor(mapping.InstalledIdentity, scan) {
			run.Health = StoreProviderFailed
			run.Error = firstNonEmpty(run.Error, "provider returned unverifiable identity mapping")
			continue
		}
		mappings = append(mappings, mapping)
	}
	run.Mappings = mappings
	run = downgradeRunHealthForBlockingObservations(run)
	return run
}

func downgradeRunHealthForBlockingObservations(run StoreCatalogProviderRun) StoreCatalogProviderRun {
	if run.Health != StoreProviderHealthy {
		return run
	}
	for _, observation := range run.Observations {
		if !observationBlocksAssessment(observation) {
			continue
		}
		run.Health = StoreProviderIncomplete
		providerKey := observation.Provider.Key()
		if providerKey == "" {
			providerKey = run.Provider.Key()
		}
		run.Error = firstNonEmpty(run.Error, fmt.Sprintf("%s returned incomplete package-level evidence", providerKey))
		return run
	}
	return run
}

func providerHealthMap(runs []StoreCatalogProviderRun) map[string]StoreProviderHealth {
	health := map[string]StoreProviderHealth{}
	for _, run := range runs {
		health[run.Provider.Key()] = run.Health
	}
	return health
}

func providerVersionMap(runs []StoreCatalogProviderRun) map[string]string {
	versions := map[string]string{}
	for _, run := range runs {
		if key := run.Provider.Key(); key != "" && strings.TrimSpace(run.Version) != "" {
			versions[key] = strings.TrimSpace(run.Version)
		}
	}
	return versions
}

func scanCompletionStatus(inventory StorePackagedAppInventory, runs []StoreCatalogProviderRun) StoreScanCompletionStatus {
	if inventory.Scan.ScanID == "" || len(inventory.Families) == 0 && (inventory.Partial || len(inventory.Errors) > 0) {
		return StoreScanFailed
	}
	for _, run := range runs {
		if run.Provider.Key() == "store-current-user-inventory" || storeCatalogProviderRequired(run.Provider) {
			if run.Health != StoreProviderHealthy {
				return StoreScanIncomplete
			}
			continue
		}
		if run.Health != StoreProviderHealthy && len(runs) == 1 {
			return StoreScanIncomplete
		}
	}
	return StoreScanCompleted
}

func configuredStoreProviderTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv(storeProviderTimeoutEnv))
	if value == "" {
		return defaultStoreProviderTimeout
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return defaultStoreProviderTimeout
	}
	return time.Duration(seconds) * time.Second
}

func scanShouldPublish(scan StoreScanGeneration, inventory StorePackagedAppInventory) bool {
	if scan.CompletionStatus == StoreScanFailed {
		return false
	}
	return len(inventory.Families) > 0 || scan.CompletionStatus == StoreScanCompleted
}

func (pipeline *StoreScanPipeline) previousAssessments(ctx context.Context, userSID string) (map[StoreInstalledIdentity]StorePublishedAssessment, error) {
	snapshot, ok, err := pipeline.Repository.LoadLatestPublishedSnapshot(ctx, userSID)
	if err != nil || !ok {
		return nil, err
	}
	return previousAssessmentsFromSnapshot(snapshot), nil
}

func reconcileStoreScanAssessments(scan StoreScanGeneration, families []StorePackagedAppFamily, providerRuns []StoreCatalogProviderRun, previous map[StoreInstalledIdentity]StorePublishedAssessment) []StorePublishedAssessment {
	required := requiredStoreCatalogProviders(providerRuns)
	observations := allStoreProviderObservations(providerRuns)
	verifiedProductIDs := verifiedProductIDsByIdentity(providerRuns, scan)
	assessments := make([]StorePublishedAssessment, 0, len(families))
	for _, family := range families {
		if !family.ProductLike {
			continue
		}
		identity := family.Identity
		assessment := ReconcileStoreUpdate(StoreReconciliationInput{
			Identity:          identity,
			Scan:              scan,
			RequiredProviders: required,
			Observations:      observations,
		})
		if previousAssessment, ok := previous[identity]; ok && shouldRetainPreviousPositive(scan, assessment) && !hasCurrentHealthyRetraction(scan, identity, providerRuns) {
			assessment.State = StoreUpdateAvailable
			assessment.Reason = "retained previous positive update because the latest scan was incomplete"
			assessment.AvailableVersion = previousAssessment.AvailableVersion
			assessment.Target = previousAssessment.Target
			assessment.Evidence = append(assessment.Evidence, StoreEvidenceSummary{Provider: "previous-generation", Health: StoreProviderStale, Kind: StoreObservationStaleResult})
			assessments = append(assessments, StorePublishedAssessment{
				StoreUpdateAssessment:      assessment,
				ObservedAt:                 previousAssessment.ObservedAt,
				Stale:                      true,
				StoreProductID:             previousAssessment.StoreProductID,
				UpdateID:                   previousAssessment.UpdateID,
				ExactActionTargetAvailable: previousAssessment.ExactActionTargetAvailable,
				Applicability:              previousAssessment.Applicability,
			})
			continue
		}
		observedAt := scan.CompletedAt
		if observedAt.IsZero() {
			observedAt = scan.StartedAt
		}
		productID, updateID, exact := "", "", false
		if assessment.Target != nil {
			productID = assessment.Target.ProductID
			updateID = assessment.Target.UpdateID
			exact = assessment.Target.ExactFor(identity)
		}
		if productID == "" {
			productID = verifiedProductIDs[identity]
		}
		assessments = append(assessments, StorePublishedAssessment{
			StoreUpdateAssessment:      assessment,
			ObservedAt:                 observedAt,
			StoreProductID:             productID,
			UpdateID:                   updateID,
			ExactActionTargetAvailable: exact,
			Applicability:              applicabilityForAssessment(assessment),
		})
	}
	return assessments
}

func hasCurrentHealthyRetraction(scan StoreScanGeneration, identity StoreInstalledIdentity, providerRuns []StoreCatalogProviderRun) bool {
	if scan.CompletionStatus != StoreScanCompleted {
		return false
	}
	for _, run := range providerRuns {
		if run.Health != StoreProviderHealthy {
			continue
		}
		for _, observation := range run.Observations {
			if !observation.Matches(identity, scan) || observation.Health != StoreProviderHealthy {
				continue
			}
			switch observation.Kind {
			case StoreObservationAuthoritativeNegative, StoreObservationNewerCatalogNoApplicableInstaller:
				return true
			}
		}
	}
	return false
}

func verifiedProductIDsByIdentity(providerRuns []StoreCatalogProviderRun, scan StoreScanGeneration) map[StoreInstalledIdentity]string {
	type productIDState struct {
		productID string
		conflict  bool
	}
	states := map[StoreInstalledIdentity]productIDState{}
	for _, run := range providerRuns {
		for _, mapping := range run.Mappings {
			if !mapping.VerifiedFor(mapping.InstalledIdentity, scan) {
				continue
			}
			state := states[mapping.InstalledIdentity]
			if state.productID == "" {
				state.productID = mapping.ProductID
			} else if !strings.EqualFold(state.productID, mapping.ProductID) {
				state.conflict = true
			}
			states[mapping.InstalledIdentity] = state
		}
	}
	verified := map[StoreInstalledIdentity]string{}
	for identity, state := range states {
		if state.productID != "" && !state.conflict {
			verified[identity] = state.productID
		}
	}
	return verified
}

func requiredStoreCatalogProviders(runs []StoreCatalogProviderRun) []StoreProviderIdentity {
	required := []StoreProviderIdentity{}
	for _, run := range runs {
		if run.Provider.Key() == "store-current-user-inventory" {
			continue
		}
		if !storeCatalogProviderRequired(run.Provider) {
			continue
		}
		required = append(required, run.Provider)
	}
	return required
}

func storeCatalogProviderRequired(provider StoreProviderIdentity) bool {
	switch provider.Key() {
	case storeCLIExactProviderID, wingetMSStoreExactProviderID:
		return false
	case storeCLIUpdatesProviderID:
		return true
	default:
		return true
	}
}

func allStoreProviderObservations(runs []StoreCatalogProviderRun) []StoreProviderObservation {
	var observations []StoreProviderObservation
	for _, run := range runs {
		observations = append(observations, run.Observations...)
	}
	return observations
}

func shouldRetainPreviousPositive(scan StoreScanGeneration, assessment StoreUpdateAssessment) bool {
	if assessment.State == StoreUpdateConflict {
		return false
	}
	if scan.CompletionStatus == StoreScanCompleted {
		return false
	}
	return assessment.State == StoreUpdateUnknown ||
		assessment.State == StoreUpdateCurrent ||
		assessment.State == StoreUpdateInapplicable
}

func applicabilityForAssessment(assessment StoreUpdateAssessment) string {
	switch assessment.State {
	case StoreUpdateInapplicable:
		return "not_applicable"
	case StoreUpdateAvailable, StoreUpdatePending:
		return "applicable"
	default:
		return "unknown"
	}
}

type unsupportedStoreCatalogProvider struct{}

func (unsupportedStoreCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: defaultStoreCatalogProviderID, Name: "Store catalog provider", Backend: "unimplemented"}
}

func (provider unsupportedStoreCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	now := time.Now().UTC()
	return StoreCatalogProviderRun{
		Provider:    provider.Identity(),
		StartedAt:   now,
		CompletedAt: now,
		Health:      StoreProviderUnsupported,
		Error:       "exact Store catalog provider is not implemented in this build",
	}
}
