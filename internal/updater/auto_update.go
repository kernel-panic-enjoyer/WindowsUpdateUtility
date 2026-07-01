package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

const (
	defaultScheduledAutoUpdateDeadline = 45 * time.Minute
	scheduledAutoUpdateDeadlineEnv     = "UPDATER_AUTO_UPDATE_DEADLINE_MINUTES"
	scheduledInventoryDeadline         = 10 * time.Minute
	scheduledStoreScanDeadline         = defaultStoreProviderTimeout + 3*time.Minute
	scheduledPackageActionDeadline     = 20 * time.Minute
	scheduledPersistenceDeadline       = 30 * time.Second
)

type StoreAutomationDecision struct {
	AutomationUsable    bool
	CommitSucceeded     bool
	MaintenanceWarnings []string
	BlockingError       error
}

func setAutoUpdate(globalEnabled *bool, packageKeys []string, packageAutoUpdateEnabled *bool) (State, CommandResult) {
	appLog("Auto-update settings update started.")
	store, err := defaultStateStore()
	if err != nil {
		return defaultState(), validationCommandResult("auto-update settings", err)
	}
	return setAutoUpdateWithStore(context.Background(), store, globalEnabled, packageKeys, packageAutoUpdateEnabled)
}

func setAutoUpdateWithStore(ctx context.Context, store StateStore, globalEnabled *bool, packageKeys []string, packageAutoUpdateEnabled *bool) (State, CommandResult) {
	state, err := store.Update(ctx, func(state *State) error {
		if state.AutoUpdatePackages == nil {
			state.AutoUpdatePackages = map[string]bool{}
		}
		if globalEnabled != nil {
			state.AutoUpdateGlobal = *globalEnabled
		}
		if packageAutoUpdateEnabled != nil {
			for _, rawPackageKey := range packageKeys {
				if _, _, err := splitPackageKey(rawPackageKey); err == nil {
					normalizedKey := normalizeAutoUpdatePackageKey(rawPackageKey)
					if normalizedKey == "" {
						appLog("Auto-update package key ignored because it is not an exact canonical target: %s.", rawPackageKey)
						continue
					}
					state.AutoUpdatePackages[normalizedKey] = *packageAutoUpdateEnabled
				}
			}
		}
		return nil
	})
	if err != nil {
		result := validationCommandResult("auto-update settings", err)
		appLog("Auto-update settings update failed before task change: %s.", err)
		loaded, loadErr := store.Load(ctx)
		if loadErr != nil {
			return defaultState(), result
		}
		return loaded, result
	}
	var result CommandResult
	if state.AutoUpdateGlobal {
		result = createAutoUpdateTaskRunner()
	} else {
		result = deleteTaskRunner(taskAutoUpdate)
	}
	appLog("Auto-update settings update finished with code %d.", result.Code)
	return state, result
}

func runAutoUpdate() []UpdateResult {
	appLog("Scheduled auto-update task started.")
	store, err := defaultStateStore()
	if err != nil {
		appLog("Scheduled auto-update could not open state store: %s.", err)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	deadline := configuredScheduledAutoUpdateDeadline()
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	return runAutoUpdateWithStore(ctx, store)
}

func runAutoUpdateWithStore(ctx context.Context, store StateStore) []UpdateResult {
	scheduledRunStartedAt := storeScanNow()
	state, err := store.Load(ctx)
	if err != nil {
		appLog("Scheduled auto-update could not load state: %s.", err)
		return nil
	}
	if !state.AutoUpdateGlobal {
		appLog("Scheduled auto-update skipped because global auto-update is disabled.")
		return nil
	}
	var optedInPackageKeys []string
	for packageKey, enabled := range state.AutoUpdatePackages {
		if enabled {
			optedInPackageKeys = append(optedInPackageKeys, packageKey)
		}
	}
	if len(optedInPackageKeys) == 0 {
		appLog("Scheduled auto-update skipped because no packages are opted in.")
		if _, err := store.Update(ctx, func(state *State) error {
			state.LastAutoUpdateAt = utcNow()
			state.LastAutoUpdateResults = nil
			state.LastAutoUpdateSummary = nil
			return nil
		}); err != nil {
			appLog("Could not save scheduled auto-update skip result: %s.", err)
		}
		return nil
	}
	inventoryCtx, cancelInventory := context.WithTimeout(ctx, scheduledInventoryDeadline)
	inventory := inventoryGetter(inventoryCtx)
	inventoryErr := inventoryCtx.Err()
	cancelInventory()
	if inventoryErr != nil {
		return persistScheduledAutoUpdateCancellation(ctx, store, nil, nil, "inventory collection stopped: "+inventoryErr.Error())
	}
	// The scheduled auto-update task runs as a standalone process with no running
	// server, so the App's background Store scan never fires here. getInventory
	// only overlays the last published snapshot, so run a fresh transactional
	// Store scan inline to discover currently-available Store updates before
	// deciding what to update (matching the pre-progressive-loading behavior).
	storeScanCtx, cancelStoreScan := context.WithTimeout(ctx, scheduledStoreScanDeadline)
	storeScanProjection := applyStoreTransactionalScanPipelineResult(storeScanCtx, state, inventory, scheduledRunStartedAt)
	storeScanErr := storeScanCtx.Err()
	cancelStoreScan()
	inventory = storeScanProjection.Inventory
	runSummary := scheduledAutoUpdateSummaryFromProjection(storeScanProjection)
	if storeScanErr != nil && storeScanProjection.Error == nil {
		storeScanProjection.Error = storeScanErr
		runSummary.StoreScan.Error = sanitizeProviderDiagnostic(storeScanErr.Error())
	}
	storeScanDecision := evaluateStoreAutomationDecision(storeScanProjection)
	optedInPackageKeySet := map[string]bool{}
	optedInStorePreferenceKeys := map[string]string{}
	for _, storedPackageKey := range optedInPackageKeys {
		normalizedKey := normalizeAutoUpdatePackageKey(storedPackageKey)
		if normalizedKey != "" {
			optedInPackageKeySet[normalizedKey] = true
			if manager, _, err := splitPackageKey(normalizedKey); err == nil && manager == managerStore {
				optedInStorePreferenceKeys[normalizedKey] = storedPackageKey
			}
		}
	}
	var results []UpdateResult
	runStopReason := ""
	processedInventoryKeys := map[string]bool{}
	matchedPreferenceKeys := map[string]bool{}
	for _, pkg := range inventory.Packages {
		inventoryKey := normalizedJobPackageKey(pkg)
		normalizedInventoryKey := normalizeAutoUpdatePackageKey(inventoryKey)
		if inventoryKey == "" || processedInventoryKeys[inventoryKey] || !optedInPackageKeySet[normalizedInventoryKey] {
			continue
		}
		processedInventoryKeys[inventoryKey] = true
		matchedPreferenceKeys[normalizedInventoryKey] = true
		if pkg.Manager == managerStore && !storeScanDecision.AutomationUsable {
			reason := scheduledStoreAutoUpdateSkipReason(storeScanProjection, storeScanDecision)
			appLog("Scheduled auto-update skipped %s because %s.", inventoryKey, reason)
			runSummary.addSkippedPackage(pkg, inventoryKey, reason)
			continue
		}
		if !packageAllowedInBulkUpdate(pkg, UpdateOptions{}) {
			appLog("Scheduled auto-update skipped %s because it requires explicit user confirmation or does not support updates.", inventoryKey)
			if pkg.Manager == managerStore {
				runSummary.addSkippedPackage(pkg, inventoryKey, "Store package is not currently actionable; it needs a fresh available assessment, exact target, and supported executor")
			}
			continue
		}
		pkg.Key = inventoryKey
		actionCtx, cancelAction := context.WithTimeout(ctx, scheduledPackageActionDeadline)
		actionResult := updatePackageWithMetadataContext(actionCtx, pkg)
		actionErr := actionCtx.Err()
		cancelAction()
		results = append(results, UpdateResult{Key: inventoryKey, Result: actionResult})
		if actionErr != nil || actionResult.Code == commandCancelledCode || actionResult.Code == 124 {
			runStopReason = firstNonEmpty(errorMessage(actionErr), packageActionStopReason(actionResult), "package action stopped")
			break
		}
	}
	for normalizedPreferenceKey, storedPreferenceKey := range optedInStorePreferenceKeys {
		if matchedPreferenceKeys[normalizedPreferenceKey] {
			continue
		}
		runSummary.SkippedPackages = append(runSummary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
			Key:     storedPreferenceKey,
			Manager: managerStore,
			Reason:  "No installed Store package matched this auto-update preference in the effective inventory",
		})
	}
	if runStopReason != "" {
		return persistScheduledAutoUpdateCancellation(ctx, store, results, &runSummary, runStopReason)
	}
	if err := ctx.Err(); err != nil {
		return persistScheduledAutoUpdateCancellation(ctx, store, results, &runSummary, err.Error())
	}
	persistCtx, cancelPersist := context.WithTimeout(ctx, scheduledPersistenceDeadline)
	defer cancelPersist()
	if _, err := store.Update(persistCtx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = summarizeUpdateResults(results, state.LastAutoUpdateAt)
		state.LastAutoUpdateSummary = &runSummary
		return nil
	}); err != nil {
		appLog("Could not save scheduled auto-update results: %s.", err)
	}
	appLog("Scheduled auto-update task finished with %d result(s).", len(results))
	return results
}

func configuredScheduledAutoUpdateDeadline() time.Duration {
	rawMinutes := strings.TrimSpace(os.Getenv(scheduledAutoUpdateDeadlineEnv))
	if rawMinutes == "" {
		return defaultScheduledAutoUpdateDeadline
	}
	deadlineMinutes, err := strconv.Atoi(rawMinutes)
	if err != nil || deadlineMinutes <= 0 {
		return defaultScheduledAutoUpdateDeadline
	}
	return time.Duration(deadlineMinutes) * time.Minute
}

func evaluateStoreAutomationDecision(projection StoreInventoryProjectionResult) StoreAutomationDecision {
	decision := StoreAutomationDecision{
		CommitSucceeded: projection.Published && projection.ScanID != "",
	}
	if projection.Error != nil {
		decision.BlockingError = projection.Error
		return decision
	}
	if !projection.FreshGeneration {
		decision.BlockingError = errors.New(scheduledStoreAutoUpdateFreshnessSkipReason(projection))
		return decision
	}
	if projection.CompletionStatus != StoreScanCompleted {
		decision.BlockingError = fmt.Errorf("Store scan generation %s is %s, not completed", projection.UsedGenerationID, projection.CompletionStatus)
		return decision
	}
	decision.AutomationUsable = true
	return decision
}

func scheduledStoreAutoUpdateSkipReason(projection StoreInventoryProjectionResult, decision StoreAutomationDecision) string {
	if decision.BlockingError != nil {
		return "Store scan did not produce automation-usable evidence: " + sanitizeProviderDiagnostic(decision.BlockingError.Error())
	}
	return scheduledStoreAutoUpdateFreshnessSkipReason(projection)
}

func persistScheduledAutoUpdateCancellation(ctx context.Context, store StateStore, partialResults []UpdateResult, runSummary *ScheduledAutoUpdateSummary, stopReason string) []UpdateResult {
	if runSummary == nil {
		emptySummary := ScheduledAutoUpdateSummary{}
		runSummary = &emptySummary
	}
	runSummary.SkippedPackages = append(runSummary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
		Key:    "*",
		Reason: "Scheduled auto-update cancelled: " + sanitizeProviderDiagnostic(firstNonEmpty(stopReason, contextErrorMessage(ctx), context.Canceled.Error())),
	})
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scheduledPersistenceDeadline)
	defer cancel()
	if _, err := store.Update(persistCtx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = summarizeUpdateResults(partialResults, state.LastAutoUpdateAt)
		state.LastAutoUpdateSummary = runSummary
		return nil
	}); err != nil {
		appLog("Could not save scheduled auto-update cancellation result: %s.", err)
	}
	return partialResults
}

func contextErrorMessage(ctx context.Context) string {
	if ctx == nil || ctx.Err() == nil {
		return ""
	}
	return ctx.Err().Error()
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func packageActionStopReason(commandResult CommandResult) string {
	switch commandResult.Code {
	case commandCancelledCode:
		return "package action cancelled"
	case 124:
		return "package action timed out"
	default:
		return ""
	}
}

func scheduledAutoUpdateSummaryFromProjection(projection StoreInventoryProjectionResult) ScheduledAutoUpdateSummary {
	runSummary := ScheduledAutoUpdateSummary{
		StoreScan: ScheduledAutoUpdateStoreScanSummary{
			ScanID:           projection.ScanID,
			UsedGenerationID: projection.UsedGenerationID,
			StartedAt:        formatStoreScanTime(projection.StartedAt),
			CompletedAt:      formatStoreScanTime(projection.CompletedAt),
			Published:        projection.Published,
			CompletionStatus: string(projection.CompletionStatus),
			FreshGeneration:  projection.FreshGeneration,
		},
	}
	if projection.Error != nil {
		runSummary.StoreScan.Error = sanitizeProviderDiagnostic(projection.Error.Error())
	}
	return runSummary
}

func scheduledStoreAutoUpdateFreshnessSkipReason(projection StoreInventoryProjectionResult) string {
	if projection.Error != nil {
		return "Store scan did not publish fresh evidence: " + sanitizeProviderDiagnostic(projection.Error.Error())
	}
	if projection.UsedGenerationID == "" {
		return "no published Store scan generation is available for this scheduled run"
	}
	if projection.CompletedAt.IsZero() {
		return "published Store scan generation has no completion time"
	}
	return "published Store scan generation did not complete after this scheduled run started"
}

func (summary *ScheduledAutoUpdateSummary) addSkippedPackage(pkg Package, key, reason string) {
	if summary == nil {
		return
	}
	summary.SkippedPackages = append(summary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
		Key:       key,
		Manager:   pkg.Manager,
		PackageID: pkg.ID,
		Reason:    sanitizeProviderDiagnostic(reason),
	})
}
