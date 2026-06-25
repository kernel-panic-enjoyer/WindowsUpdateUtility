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

func setAutoUpdate(global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	appLog("Auto-update settings update started.")
	store, err := defaultStateStore()
	if err != nil {
		return defaultState(), validationCommandResult("auto-update settings", err)
	}
	return setAutoUpdateWithStore(context.Background(), store, global, packageKeys, packageEnabled)
}

func setAutoUpdateWithStore(ctx context.Context, store StateStore, global *bool, packageKeys []string, packageEnabled *bool) (State, CommandResult) {
	state, err := store.Update(ctx, func(state *State) error {
		if state.AutoUpdatePackages == nil {
			state.AutoUpdatePackages = map[string]bool{}
		}
		if global != nil {
			state.AutoUpdateGlobal = *global
		}
		if packageEnabled != nil {
			for _, key := range packageKeys {
				if _, _, err := splitPackageKey(key); err == nil {
					normalized := normalizeAutoUpdatePackageKey(key)
					if normalized == "" {
						appLog("Auto-update package key ignored because it is not an exact canonical target: %s.", key)
						continue
					}
					state.AutoUpdatePackages[normalized] = *packageEnabled
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
	taskStartedAt := storeScanNow()
	state, err := store.Load(ctx)
	if err != nil {
		appLog("Scheduled auto-update could not load state: %s.", err)
		return nil
	}
	if !state.AutoUpdateGlobal {
		appLog("Scheduled auto-update skipped because global auto-update is disabled.")
		return nil
	}
	var selected []string
	for key, enabled := range state.AutoUpdatePackages {
		if enabled {
			selected = append(selected, key)
		}
	}
	if len(selected) == 0 {
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
	storeProjection := applyStoreTransactionalScanPipelineResult(storeScanCtx, state, inventory, taskStartedAt)
	storeScanErr := storeScanCtx.Err()
	cancelStoreScan()
	inventory = storeProjection.Inventory
	summary := scheduledAutoUpdateSummaryFromProjection(storeProjection)
	if storeScanErr != nil && storeProjection.Error == nil {
		storeProjection.Error = storeScanErr
		summary.StoreScan.Error = sanitizeProviderDiagnostic(storeScanErr.Error())
	}
	storeDecision := storeAutomationDecision(storeProjection)
	selectedSet := map[string]bool{}
	selectedStoreKeys := map[string]string{}
	for _, key := range selected {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized != "" {
			selectedSet[normalized] = true
			if manager, _, err := splitPackageKey(normalized); err == nil && manager == managerStore {
				selectedStoreKeys[normalized] = key
			}
		}
	}
	var results []UpdateResult
	stopReason := ""
	seen := map[string]bool{}
	seenSelected := map[string]bool{}
	for _, pkg := range inventory.Packages {
		key := normalizedJobPackageKey(pkg)
		if key == "" || seen[key] || !selectedSet[normalizeAutoUpdatePackageKey(key)] {
			continue
		}
		seen[key] = true
		seenSelected[normalizeAutoUpdatePackageKey(key)] = true
		if pkg.Manager == managerStore && !storeDecision.AutomationUsable {
			reason := scheduledStoreAutoUpdateSkipReason(storeProjection, storeDecision)
			appLog("Scheduled auto-update skipped %s because %s.", key, reason)
			summary.addSkippedPackage(pkg, key, reason)
			continue
		}
		if !packageAllowedInBulkUpdate(pkg, UpdateOptions{}) {
			appLog("Scheduled auto-update skipped %s because it requires explicit user confirmation or does not support updates.", key)
			if pkg.Manager == managerStore {
				summary.addSkippedPackage(pkg, key, "Store package is not currently actionable; it needs a fresh available assessment, exact target, and supported executor")
			}
			continue
		}
		pkg.Key = key
		actionCtx, cancelAction := context.WithTimeout(ctx, scheduledPackageActionDeadline)
		result := updatePackageWithMetadataContext(actionCtx, pkg)
		actionErr := actionCtx.Err()
		cancelAction()
		results = append(results, UpdateResult{Key: key, Result: result})
		if actionErr != nil || result.Code == commandCancelledCode || result.Code == 124 {
			stopReason = firstNonEmpty(actionErrString(actionErr), packageActionStopReason(result), "package action stopped")
			break
		}
	}
	for normalized, original := range selectedStoreKeys {
		if seenSelected[normalized] {
			continue
		}
		summary.SkippedPackages = append(summary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
			Key:     original,
			Manager: managerStore,
			Reason:  "No installed Store package matched this auto-update preference in the effective inventory",
		})
	}
	if stopReason != "" {
		return persistScheduledAutoUpdateCancellation(ctx, store, results, &summary, stopReason)
	}
	if err := ctx.Err(); err != nil {
		return persistScheduledAutoUpdateCancellation(ctx, store, results, &summary, err.Error())
	}
	persistCtx, cancelPersist := context.WithTimeout(ctx, scheduledPersistenceDeadline)
	defer cancelPersist()
	if _, err := store.Update(persistCtx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = summarizeUpdateResults(results, state.LastAutoUpdateAt)
		state.LastAutoUpdateSummary = &summary
		return nil
	}); err != nil {
		appLog("Could not save scheduled auto-update results: %s.", err)
	}
	appLog("Scheduled auto-update task finished with %d result(s).", len(results))
	return results
}

func configuredScheduledAutoUpdateDeadline() time.Duration {
	raw := strings.TrimSpace(os.Getenv(scheduledAutoUpdateDeadlineEnv))
	if raw == "" {
		return defaultScheduledAutoUpdateDeadline
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		return defaultScheduledAutoUpdateDeadline
	}
	return time.Duration(minutes) * time.Minute
}

func storeAutomationDecision(projection StoreInventoryProjectionResult) StoreAutomationDecision {
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

func persistScheduledAutoUpdateCancellation(ctx context.Context, store StateStore, results []UpdateResult, summary *ScheduledAutoUpdateSummary, reason string) []UpdateResult {
	if summary == nil {
		empty := ScheduledAutoUpdateSummary{}
		summary = &empty
	}
	summary.SkippedPackages = append(summary.SkippedPackages, ScheduledAutoUpdateSkippedPackage{
		Key:    "*",
		Reason: "Scheduled auto-update cancelled: " + sanitizeProviderDiagnostic(firstNonEmpty(reason, ctxErrString(ctx), context.Canceled.Error())),
	})
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scheduledPersistenceDeadline)
	defer cancel()
	if _, err := store.Update(persistCtx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = summarizeUpdateResults(results, state.LastAutoUpdateAt)
		state.LastAutoUpdateSummary = summary
		return nil
	}); err != nil {
		appLog("Could not save scheduled auto-update cancellation result: %s.", err)
	}
	return results
}

func ctxErrString(ctx context.Context) string {
	if ctx == nil || ctx.Err() == nil {
		return ""
	}
	return ctx.Err().Error()
}

func actionErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func packageActionStopReason(result CommandResult) string {
	switch result.Code {
	case commandCancelledCode:
		return "package action cancelled"
	case 124:
		return "package action timed out"
	default:
		return ""
	}
}

func scheduledAutoUpdateSummaryFromProjection(projection StoreInventoryProjectionResult) ScheduledAutoUpdateSummary {
	summary := ScheduledAutoUpdateSummary{
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
		summary.StoreScan.Error = sanitizeProviderDiagnostic(projection.Error.Error())
	}
	return summary
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
