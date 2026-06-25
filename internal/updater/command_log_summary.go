package updater

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func suppressCommandStdoutInSessionLog(args []string) bool {
	manager, verb, _ := packageManagerCommandParts(args)
	return manager == managerStore && (verb == "show" || ((verb == "update" || verb == "updates") && commandHasApplyFalse(args)))
}

func logStoreDetectionCommandSummary(ctx context.Context, args []string, result CommandResult, categories []string, duration time.Duration) {
	manager, verb, tail := packageManagerCommandParts(args)
	if manager != managerStore {
		return
	}
	target := firstStoreCommandTarget(tail)
	switch verb {
	case "show":
		metadata, parseErr := parseStoreCLIShowMetadata(result.Stdout + "\n" + result.Stderr)
		status := "parsed"
		if parseErr != nil || !result.OK {
			status = "incomplete"
		}
		message := fmt.Sprintf("Store scan show summary: PFN=%s product_id=%s exact_match=%t parser=%s duration=%s",
			firstNonEmpty(target, metadata.PFN),
			firstNonEmpty(metadata.ProductID, "unknown"),
			target != "" && strings.EqualFold(target, metadata.PFN),
			status,
			duration.Round(time.Millisecond),
		)
		if parseErr != nil {
			message += " error=" + sanitizeProviderDiagnostic(parseErr.Error())
		} else if !result.OK {
			message += " error=" + sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, "Store CLI show failed"))
		}
		sessionLogs.AppendContext(ctx, "app", message, append(categories, logCategoryStoreScan))
	case "update", "updates":
		state, parseErr := parseStoreCLIUpdateCheckResult(ctx, result.Stdout+"\n"+result.Stderr, result)
		status := storeObservationKindSummary(state)
		message := fmt.Sprintf("Store scan update-check summary: PFN=%s state=%s duration=%s",
			firstNonEmpty(target, "aggregate"),
			status,
			duration.Round(time.Millisecond),
		)
		if parseErr != nil {
			message += " error=" + sanitizeProviderDiagnostic(parseErr.Error())
		} else if !result.OK {
			message += " error=" + sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, "Store CLI update check failed"))
		}
		sessionLogs.AppendContext(ctx, "app", message, append(categories, logCategoryStoreScan))
	}
}

func firstStoreCommandTarget(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") || strings.EqualFold(arg, "false") || strings.EqualFold(arg, "true") {
			continue
		}
		return arg
	}
	return ""
}

func storeObservationKindSummary(kind StoreObservationKind) string {
	switch kind {
	case StoreObservationPositiveUpdateOffer:
		return "available"
	case StoreObservationAuthoritativeNegative:
		return "current"
	case StoreObservationNewerCatalogNoApplicableInstaller:
		return "inapplicable"
	case StoreObservationIncompleteResult:
		return "incomplete"
	case StoreObservationEmptyResult:
		return "empty"
	default:
		return "unknown"
	}
}
