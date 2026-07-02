package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	storeProviderTimeoutEnv       = "UPDATER_STORE_PROVIDER_TIMEOUT_SECONDS"
	defaultStoreCatalogProviderID = "store-catalog-unimplemented"
	storeMappingReuseProviderID   = "store-mapping-reuse"
	defaultStoreProviderTimeout   = 6 * time.Minute
	// Store PFN-to-ProductID mappings are only reused while the installed
	// family fingerprint and Store CLI provider version still match recent
	// evidence. Old mappings remain diagnostics but cannot authorize updates.
	storeMappingFreshnessWindow = 14 * 24 * time.Hour
)

var (
	errStoreScanAlreadyRunning = errors.New("a Store scan is already running")
	storeScanNow               = func() time.Time { return time.Now().UTC() }
	storeScanCurrentUserSID    = currentUserSID
)

// StoreCatalogProvider supplies Store update evidence for already-installed
// current-user package families. Providers are evidence sources only; the
// reconciler decides whether their output is authoritative enough to display or
// execute an update.
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

	// PositiveUpdateHint means a provider saw non-authoritative update text
	// without enough identity to attach it to a package. It is only a planner
	// signal for exact PFN checks; it is never persisted or treated as evidence.
	PositiveUpdateHint bool `json:"-"`
}

// StoreScanPipeline owns one complete Store assessment generation. It joins
// current-user AppX inventory with Store catalog/discovery providers, persists
// immutable evidence, and never mutates the base package-manager inventory.
type StoreScanPipeline struct {
	Repository        StoreScanRepository
	InventoryProvider StorePackagedAppInventoryProvider
	CatalogProviders  []StoreCatalogProvider
	ProviderTimeout   time.Duration
	Now               func() time.Time
	NewScanID         func(time.Time) string
	BeforeCommit      func(context.Context, StoreScanSnapshot) error
	DeepExactScan     bool
	PlanningState     *State

	mu      sync.Mutex
	running bool
}

type storeScanProviderPlan struct {
	aggregateProviders []StoreCatalogProvider
	winRTProviders     []StoreCatalogProvider
	exactProvider      *storeCLIExactCatalogProvider
}

type storeExactWorkPlan struct {
	families          []StorePackagedAppFamily
	stateCheckPFNs    map[string]bool
	mappingReuseRun   *StoreCatalogProviderRun
	mappingsReused    int
	mappingsRefreshed int
	mappingsRejected  int
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
			storeWinRTDiscoveryCatalogProvider{},
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
		Mode:             StoreScanModeOptimized,
		WindowsVersion:   systemContext.WindowsVersion,
		WindowsBuild:     systemContext.WindowsBuild,
		Architecture:     systemContext.Architecture,
		ProviderVersions: map[string]string{},
		ProviderHealth:   map[string]StoreProviderHealth{},
		CompletionStatus: StoreScanRunning,
	}
	if pipeline.DeepExactScan {
		scan.Mode = StoreScanModeDeep
	}
	ctx = withLogMetadata(ctx, logMetadata{Activity: logCategoryStoreScan, ScanID: scan.ScanID, Manager: managerStore})

	inventory, inventoryRun := pipeline.collectInventory(ctx, scan)
	previousSnapshot, previousFound, err := pipeline.previousSnapshot(ctx, userSID)
	if err != nil {
		// Why: hysteresis relies on the prior published snapshot. If it cannot
		// be loaded, returning diagnostics without publishing prevents a new
		// generation from accidentally clearing or replacing authoritative state.
		scan.CompletedAt = pipeline.now()
		scan.ProviderHealth = providerHealthMap([]StoreCatalogProviderRun{inventoryRun})
		scan.ProviderVersions = providerVersionMap([]StoreCatalogProviderRun{inventoryRun})
		scan.CompletionStatus = scanCompletionStatus(inventory, []StoreCatalogProviderRun{inventoryRun})
		assessments := reconcileStoreScanAssessments(scan, inventory.Families, []StoreCatalogProviderRun{inventoryRun}, nil)
		return StoreScanResult{Scan: scan, Inventory: inventory, ProviderRuns: []StoreCatalogProviderRun{inventoryRun}, Assessments: assessments}, fmt.Errorf("could not load previous published Store assessments for hysteresis: %w", err)
	}
	previous := map[StoreInstalledIdentity]StorePublishedAssessment(nil)
	if previousFound {
		previous = previousAssessmentsFromSnapshot(previousSnapshot)
	}
	plan := pipeline.planCatalogProviders()
	exactProviderVersion := ""
	if plan.exactProvider != nil {
		exactProviderVersion = plan.exactProvider.Version
	}
	aggregateProviders := catalogProvidersWithStoreMappingContext(plan.aggregateProviders, previousSnapshot, previousFound, exactProviderVersion, nil)
	aggregateStarted := pipeline.now()
	aggregateRuns := pipeline.runCatalogProviders(ctx, scan, inventory.Families, aggregateProviders)
	aggregateDuration := pipeline.now().Sub(aggregateStarted)
	exactPlan := pipeline.planExactWork(ctx, scan, inventory.Families, aggregateRuns, previousSnapshot, previousFound, exactProviderVersion, len(plan.winRTProviders) > 0)
	providerRuns := []StoreCatalogProviderRun{inventoryRun}
	providerRuns = append(providerRuns, aggregateRuns...)
	if exactPlan.mappingReuseRun != nil {
		providerRuns = append(providerRuns, *exactPlan.mappingReuseRun)
	}
	if plan.exactProvider != nil && len(exactPlan.families) > 0 {
		exactProvider := *plan.exactProvider
		exactProvider.StateCheckPFNs = exactPlan.stateCheckPFNs
		exactRuns := pipeline.runCatalogProviders(ctx, scan, exactPlan.families, []StoreCatalogProvider{exactProvider})
		providerRuns = append(providerRuns, exactRuns...)
	}
	if len(plan.winRTProviders) > 0 {
		currentMappings := storeMappingsFromProviderRuns(providerRuns)
		winRTProviders := catalogProvidersWithStoreMappingContext(plan.winRTProviders, previousSnapshot, previousFound, exactProviderVersion, currentMappings)
		winRTStarted := pipeline.now()
		winRTRuns := pipeline.runCatalogProviders(ctx, scan, inventory.Families, winRTProviders)
		aggregateDuration += pipeline.now().Sub(winRTStarted)
		aggregateRuns = append(aggregateRuns, winRTRuns...)
		providerRuns = append(providerRuns, winRTRuns...)
	}
	scan.CompletedAt = pipeline.now()
	scan.ProviderHealth = providerHealthMap(providerRuns)
	scan.ProviderVersions = providerVersionMap(providerRuns)
	scan.CompletionStatus = scanCompletionStatus(inventory, providerRuns)
	assessments := reconcileStoreScanAssessments(scan, inventory.Families, providerRuns, previous)
	scan.Metrics = buildStoreScanMetrics(scan, inventory.Families, providerRuns, aggregateDuration, exactPlan, assessments)
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

func (pipeline *StoreScanPipeline) runCatalogProviders(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily, providers []StoreCatalogProvider) []StoreCatalogProviderRun {
	if len(providers) == 0 {
		return nil
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
			run = attachMappingFingerprints(scan, run, families)
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
	if run.Provider.Key() == storeWinRTDiscoveryProviderID {
		return run
	}
	// Why: a failed required provider is package-level evidence for every
	// product-like family it should have covered. Recording explicit incomplete
	// observations keeps Unknown visible without pretending the packages are
	// Current.
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

func (pipeline *StoreScanPipeline) planCatalogProviders() storeScanProviderPlan {
	providers := pipeline.CatalogProviders
	if len(providers) == 0 {
		providers = []StoreCatalogProvider{unsupportedStoreCatalogProvider{}}
	}
	plan := storeScanProviderPlan{}
	for _, provider := range providers {
		switch typed := provider.(type) {
		case storeCLIExactCatalogProvider:
			copied := typed
			plan.exactProvider = &copied
		case *storeCLIExactCatalogProvider:
			if typed != nil {
				copied := *typed
				plan.exactProvider = &copied
			}
		case storeWinRTDiscoveryCatalogProvider:
			plan.winRTProviders = append(plan.winRTProviders, typed)
		case *storeWinRTDiscoveryCatalogProvider:
			if typed != nil {
				plan.winRTProviders = append(plan.winRTProviders, typed)
			}
		default:
			plan.aggregateProviders = append(plan.aggregateProviders, provider)
		}
	}
	return plan
}

func catalogProvidersWithStoreMappingContext(providers []StoreCatalogProvider, previous StoreScanSnapshot, previousFound bool, exactProviderVersion string, currentMappings []VerifiedStoreIdentityMapping) []StoreCatalogProvider {
	if len(providers) == 0 {
		return nil
	}
	copied := make([]StoreCatalogProvider, len(providers))
	for index, provider := range providers {
		switch typed := provider.(type) {
		case storeWinRTDiscoveryCatalogProvider:
			typed.PreviousSnapshot = previous
			typed.PreviousSnapshotFound = previousFound
			typed.MappingProviderVersion = exactProviderVersion
			typed.CurrentMappings = currentMappings
			copied[index] = typed
		case *storeWinRTDiscoveryCatalogProvider:
			if typed == nil {
				copied[index] = provider
				continue
			}
			clone := *typed
			clone.PreviousSnapshot = previous
			clone.PreviousSnapshotFound = previousFound
			clone.MappingProviderVersion = exactProviderVersion
			clone.CurrentMappings = currentMappings
			copied[index] = &clone
		default:
			copied[index] = provider
		}
	}
	return copied
}

func (pipeline *StoreScanPipeline) planExactWork(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily, aggregateRuns []StoreCatalogProviderRun, previousSnapshot StoreScanSnapshot, previousFound bool, exactProviderVersion string, ensureMappingCoverage bool) storeExactWorkPlan {
	plan := storeExactWorkPlan{stateCheckPFNs: map[string]bool{}}
	byPFN := productLikeFamiliesByPFN(scan, families)
	if pipeline.DeepExactScan {
		for _, family := range families {
			if !family.ProductLike || !family.Identity.Resolved() || family.Identity.UserSID != scan.UserSID {
				continue
			}
			plan.families = append(plan.families, family)
			plan.stateCheckPFNs[strings.ToLower(family.Identity.PackageFamilyName)] = true
		}
		return plan
	}

	state := pipeline.planningState(ctx)
	incompleteAggregate := aggregateCoverageIncomplete(aggregateRuns)
	displayOnlyPositiveHint := aggregatePositiveHintRequiresExactSweep(aggregateRuns)
	aggregateNegativeGuard := aggregateNegativeRequiresExactSweep(aggregateRuns)
	reusableMappings := reusableStoreMappings(previousSnapshot, previousFound, byPFN, scan.StartedAt, exactProviderVersion)
	positiveNeedingTargets := positiveAggregateObservationsWithoutExactTarget(scan, aggregateRuns)
	planned := map[string]bool{}
	for _, observation := range positiveNeedingTargets {
		key := strings.ToLower(observation.Identity.PackageFamilyName)
		family, ok := byPFN[key]
		if !ok {
			continue
		}
		if mapping, ok := reusableMappings[family.Identity]; ok {
			// Why: aggregate Store output can say "an update exists" without an
			// executable target. A reused mapping is accepted only when it was
			// verified for the same SID/PFN fingerprint and provider version.
			plan.mappingsReused++
			plan.mappingReuseRun = appendMappingReuseObservation(scan, plan.mappingReuseRun, family, mapping, observation)
			continue
		}
		if previousFound {
			plan.mappingsRejected++
		}
		planned[key] = true
		plan.stateCheckPFNs[key] = true
	}
	if displayOnlyPositiveHint || aggregateNegativeGuard {
		// Why: display-only positives and known false-negative aggregate output
		// are planner signals, not evidence. They expand exact PFN checks but do
		// not directly create actionable update rows.
		for _, family := range byPFN {
			key := strings.ToLower(family.Identity.PackageFamilyName)
			planned[key] = true
			plan.stateCheckPFNs[key] = true
		}
	}
	if incompleteAggregate {
		for _, family := range byPFN {
			if !storeFamilyAutoUpdateEnabled(state, family.Identity) {
				continue
			}
			key := strings.ToLower(family.Identity.PackageFamilyName)
			planned[key] = true
			plan.stateCheckPFNs[key] = true
		}
	}
	if ensureMappingCoverage {
		for key, family := range byPFN {
			if _, ok := reusableMappings[family.Identity]; ok || planned[key] {
				continue
			}
			planned[key] = true
		}
	}
	for key := range planned {
		if family, ok := byPFN[key]; ok {
			plan.families = append(plan.families, family)
		}
	}
	sort.Slice(plan.families, func(i, j int) bool {
		return strings.ToLower(plan.families[i].Identity.PackageFamilyName) < strings.ToLower(plan.families[j].Identity.PackageFamilyName)
	})
	plan.mappingsRefreshed = len(plan.families)
	return plan
}

func storeMappingsFromProviderRuns(providerRuns []StoreCatalogProviderRun) []VerifiedStoreIdentityMapping {
	var mappings []VerifiedStoreIdentityMapping
	for _, run := range providerRuns {
		mappings = append(mappings, run.Mappings...)
	}
	return mappings
}

func (pipeline *StoreScanPipeline) planningState(ctx context.Context) State {
	if pipeline.PlanningState != nil {
		return *pipeline.PlanningState
	}
	store, err := defaultStateStore()
	if err != nil {
		return defaultState()
	}
	state, err := store.Load(ctx)
	if err != nil {
		return defaultState()
	}
	return state
}

func productLikeFamiliesByPFN(scan StoreScanGeneration, families []StorePackagedAppFamily) map[string]StorePackagedAppFamily {
	byPFN := map[string]StorePackagedAppFamily{}
	for _, family := range families {
		if family.ProductLike && family.Identity.Resolved() && family.Identity.UserSID == scan.UserSID {
			byPFN[strings.ToLower(family.Identity.PackageFamilyName)] = family
		}
	}
	return byPFN
}

func aggregateCoverageIncomplete(runs []StoreCatalogProviderRun) bool {
	for _, run := range runs {
		if storeCatalogProviderRequired(run.Provider) && run.Health != StoreProviderHealthy {
			return true
		}
	}
	return false
}

func aggregatePositiveHintRequiresExactSweep(runs []StoreCatalogProviderRun) bool {
	for _, run := range runs {
		if run.PositiveUpdateHint && run.Provider.Key() == storeCLIUpdatesProviderID && run.Health != StoreProviderUnsupported {
			return true
		}
	}
	return false
}

func aggregateNegativeRequiresExactSweep(runs []StoreCatalogProviderRun) bool {
	for _, run := range runs {
		if run.Provider.Key() != storeCLIUpdatesProviderID || run.Health != StoreProviderHealthy {
			continue
		}
		for _, observation := range run.Observations {
			if observation.Health == StoreProviderHealthy && observation.Kind == StoreObservationAuthoritativeNegative {
				return true
			}
		}
	}
	return false
}

func positiveAggregateObservationsWithoutExactTarget(scan StoreScanGeneration, runs []StoreCatalogProviderRun) []StoreProviderObservation {
	var positives []StoreProviderObservation
	for _, run := range runs {
		for _, observation := range run.Observations {
			if observation.ScanID != scan.ScanID || observation.Identity.UserSID != scan.UserSID || observation.Kind != StoreObservationPositiveUpdateOffer || observation.Health != StoreProviderHealthy {
				continue
			}
			if observation.Target != nil && observation.Target.ExactFor(observation.Identity) {
				continue
			}
			positives = append(positives, observation)
		}
	}
	return positives
}

func storeFamilyAutoUpdateEnabled(state State, identity StoreInstalledIdentity) bool {
	if !identity.Resolved() || state.AutoUpdatePackages == nil {
		return false
	}
	return state.AutoUpdatePackages[canonicalStoreAutoUpdateKey(identity.UserSID, identity.PackageFamilyName)]
}

func appendMappingReuseObservation(scan StoreScanGeneration, run *StoreCatalogProviderRun, family StorePackagedAppFamily, mapping VerifiedStoreIdentityMapping, source StoreProviderObservation) *StoreCatalogProviderRun {
	provider := StoreProviderIdentity{ID: storeMappingReuseProviderID, Name: "Verified Store mapping reuse", Backend: "snapshot"}
	if run == nil {
		started := source.ObservedAt
		if started.IsZero() {
			started = scan.StartedAt
		}
		run = &StoreCatalogProviderRun{
			Provider:    provider,
			Version:     mapping.ProviderVersion,
			StartedAt:   started,
			CompletedAt: started,
			Health:      StoreProviderHealthy,
		}
	}
	verifiedAt := mapping.VerifiedAt
	if verifiedAt.IsZero() {
		verifiedAt = source.ObservedAt
	}
	target := &ExactStoreUpdateTarget{
		Identity:   family.Identity,
		Provider:   mapping.Provider,
		ProductID:  mapping.ProductID,
		UpdateID:   family.Identity.PackageFamilyName,
		Verified:   true,
		VerifiedBy: storeProviderKey(mapping.Provider),
		VerifiedAt: verifiedAt,
	}
	reusedMapping := mapping
	reusedMapping.ScanID = scan.ScanID
	reusedMapping.Evidence = "reused verified Store Product ID mapping for aggregate positive evidence"
	run.Observations = append(run.Observations, StoreProviderObservation{
		Provider:         provider,
		Health:           StoreProviderHealthy,
		Kind:             StoreObservationPositiveUpdateOffer,
		Identity:         family.Identity,
		ScanID:           scan.ScanID,
		ObservedAt:       source.ObservedAt,
		InstalledVersion: firstNonEmpty(source.InstalledVersion, family.Primary.Version.String()),
		AvailableVersion: source.AvailableVersion,
		CatalogVersion:   source.CatalogVersion,
		Target:           target,
		Diagnostics:      "Aggregate Store update evidence reused a fresh verified PFN/Product ID mapping.",
	})
	run.Mappings = append(run.Mappings, reusedMapping)
	return run
}

func reusableStoreMappings(snapshot StoreScanSnapshot, found bool, families map[string]StorePackagedAppFamily, now time.Time, currentProviderVersion string) map[StoreInstalledIdentity]VerifiedStoreIdentityMapping {
	reusable := map[StoreInstalledIdentity]VerifiedStoreIdentityMapping{}
	if !found || !snapshot.Published || snapshot.RecoveredFromFallback {
		return reusable
	}
	for _, run := range snapshot.ProviderRuns {
		for _, mapping := range run.Mappings {
			family, ok := families[strings.ToLower(mapping.InstalledIdentity.PackageFamilyName)]
			if !ok || !mappingReusableForFamily(mapping, run.Version, family, now, currentProviderVersion) {
				continue
			}
			reusable[mapping.InstalledIdentity] = mapping
		}
	}
	return reusable
}

func mappingReusableForFamily(mapping VerifiedStoreIdentityMapping, runVersion string, family StorePackagedAppFamily, now time.Time, currentProviderVersion string) bool {
	if mapping.ProductID == "" || !mapping.InstalledIdentity.Equal(family.Identity) || mapping.VerifiedAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = storeScanNow()
	}
	if now.Sub(mapping.VerifiedAt) > storeMappingFreshnessWindow {
		return false
	}
	providerVersion := firstNonEmpty(mapping.ProviderVersion, runVersion)
	if providerVersion == "" || strings.TrimSpace(mapping.ProviderVersion) == "" || strings.TrimSpace(currentProviderVersion) == "" || mapping.Provider.Key() == "" {
		return false
	}
	if !strings.EqualFold(providerVersion, strings.TrimSpace(currentProviderVersion)) {
		return false
	}
	if !strings.EqualFold(mapping.IdentityName, family.Primary.IdentityName) {
		return false
	}
	if mapping.PublisherID != "" && family.Primary.PublisherID != "" && !strings.EqualFold(mapping.PublisherID, family.Primary.PublisherID) {
		return false
	}
	if !strings.EqualFold(mapping.ProcessorArchitecture, family.Primary.ProcessorArchitecture) {
		return false
	}
	return mapping.ProductLike == family.ProductLike
}

func attachMappingFingerprints(scan StoreScanGeneration, run StoreCatalogProviderRun, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	byPFN := productLikeFamiliesByPFN(scan, families)
	for index := range run.Mappings {
		mapping := &run.Mappings[index]
		family, ok := byPFN[strings.ToLower(mapping.InstalledIdentity.PackageFamilyName)]
		if !ok {
			continue
		}
		if mapping.IdentityName == "" {
			mapping.IdentityName = family.Primary.IdentityName
		}
		if mapping.PublisherID == "" {
			mapping.PublisherID = family.Primary.PublisherID
		}
		if mapping.ProcessorArchitecture == "" {
			mapping.ProcessorArchitecture = family.Primary.ProcessorArchitecture
		}
		mapping.ProductLike = family.ProductLike
		if mapping.ProviderVersion == "" {
			mapping.ProviderVersion = run.Version
		}
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
			// Why: Store state is scoped to a user and scan generation. Partial
			// trust after a provider returns cross-user or cross-scan evidence
			// would let old or unrelated Store state authorize updates.
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

func (pipeline *StoreScanPipeline) previousSnapshot(ctx context.Context, userSID string) (StoreScanSnapshot, bool, error) {
	snapshot, ok, err := pipeline.Repository.LoadLatestPublishedSnapshot(ctx, userSID)
	if err != nil || !ok {
		return StoreScanSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func buildStoreScanMetrics(scan StoreScanGeneration, families []StorePackagedAppFamily, providerRuns []StoreCatalogProviderRun, aggregateDuration time.Duration, exactPlan storeExactWorkPlan, assessments []StorePublishedAssessment) StoreScanMetrics {
	metrics := StoreScanMetrics{
		ProductLikeFamilyCount:  productLikeFamilyCount(families),
		AggregateDurationMillis: aggregateDuration.Milliseconds(),
		ExactChecksPlanned:      len(exactPlan.stateCheckPFNs),
		MappingsReused:          exactPlan.mappingsReused,
		MappingsRefreshed:       exactPlan.mappingsRefreshed,
		MappingsRejected:        exactPlan.mappingsRejected,
		CommandCountByFamily:    map[string]int{},
		ResultingStateCount:     map[string]int{},
		TotalDurationMillis:     scan.CompletedAt.Sub(scan.StartedAt).Milliseconds(),
	}
	if metrics.TotalDurationMillis < 0 {
		metrics.TotalDurationMillis = 0
	}
	for _, run := range providerRuns {
		switch run.Provider.Key() {
		case storeCLIExactProviderID:
			metrics.ExactProviderRunCount++
			for _, observation := range run.Observations {
				if observation.Health == StoreProviderHealthy && (observation.Kind == StoreObservationPositiveUpdateOffer || observation.Kind == StoreObservationAuthoritativeNegative || observation.Kind == StoreObservationNewerCatalogNoApplicableInstaller) {
					metrics.ExactChecksCompleted++
				}
			}
		case storeMappingReuseProviderID:
			metrics.MappingReuseProviderRunCount++
		default:
			if run.Provider.Key() != "store-current-user-inventory" {
				metrics.AggregateProviderRunCount++
			}
		}
		if run.Health == StoreProviderFailed && strings.Contains(strings.ToLower(run.Error), "timeout") {
			metrics.TimeoutCount++
		}
	}
	metrics.CommandCountByFamily["store-show"] = len(exactPlan.families)
	metrics.CommandCountByFamily["store-update-targeted"] = len(exactPlan.stateCheckPFNs)
	for _, assessment := range assessments {
		metrics.ResultingStateCount[string(assessment.State)]++
	}
	if len(metrics.CommandCountByFamily) == 0 {
		metrics.CommandCountByFamily = nil
	}
	if len(metrics.ResultingStateCount) == 0 {
		metrics.ResultingStateCount = nil
	}
	return metrics
}

func reconcileStoreScanAssessments(scan StoreScanGeneration, families []StorePackagedAppFamily, providerRuns []StoreCatalogProviderRun, previous map[StoreInstalledIdentity]StorePublishedAssessment) []StorePublishedAssessment {
	required := requiredStoreCatalogProviders(providerRuns)
	observations := allStoreProviderObservations(providerRuns)
	verifiedProductIDs, verifiedProductIDConflicts := verifiedProductIDsByIdentity(providerRuns, scan)
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
			// Why: incomplete scans should not erase a previously seen positive,
			// but retained evidence is marked stale so it stays diagnostic and
			// cannot authorize Store execution.
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
		if conflictReason := verifiedProductIDConflicts[identity]; conflictReason != "" && assessment.State == StoreUpdateAvailable {
			assessment.State = StoreUpdateConflict
			assessment.Reason = conflictReason
			assessment.Target = nil
			assessment.AvailableVersion = ""
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

func verifiedProductIDsByIdentity(providerRuns []StoreCatalogProviderRun, scan StoreScanGeneration) (map[StoreInstalledIdentity]string, map[StoreInstalledIdentity]string) {
	type productIDState struct {
		productID string
		conflict  bool
		values    []string
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
				state.values = append(state.values, storeProviderKey(mapping.Provider)+"="+sanitizeProviderDiagnostic(mapping.ProductID))
			} else if !strings.EqualFold(state.productID, mapping.ProductID) {
				state.conflict = true
				state.values = append(state.values, storeProviderKey(mapping.Provider)+"="+sanitizeProviderDiagnostic(mapping.ProductID))
			}
			states[mapping.InstalledIdentity] = state
		}
	}
	verified := map[StoreInstalledIdentity]string{}
	conflicts := map[StoreInstalledIdentity]string{}
	for identity, state := range states {
		if state.productID != "" && !state.conflict {
			verified[identity] = state.productID
		} else if state.conflict {
			sort.Strings(state.values)
			conflicts[identity] = "verified Store product mapping conflict: product_id " + strings.Join(state.values, ", ")
		}
	}
	return verified, conflicts
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
	case storeCLIExactProviderID, wingetMSStoreExactProviderID, storeMappingReuseProviderID, storeWinRTDiscoveryProviderID:
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
