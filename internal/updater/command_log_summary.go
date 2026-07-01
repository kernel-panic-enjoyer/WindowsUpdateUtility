package updater

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func suppressCommandStdoutInSessionLog(args []string) bool {
	commandManager, commandVerb, _ := packageManagerCommandParts(args)
	return commandManager == managerStore && (commandVerb == "show" || (isStoreUpdateCheckVerb(commandVerb) && commandHasApplyFalse(args)))
}

func logStoreDetectionCommandSummary(ctx context.Context, args []string, result CommandResult, categories []string, duration time.Duration) {
	commandManager, commandVerb, commandTail := packageManagerCommandParts(args)
	if commandManager != managerStore {
		return
	}
	requestedTarget := firstStoreCommandPositionalTarget(commandTail)
	commandOutput := result.Stdout + "\n" + result.Stderr
	switch commandVerb {
	case "show":
		showMetadata, parseErr := parseStoreCLIShowMetadata(commandOutput)
		parserStatus := "parsed"
		if parseErr != nil || !result.OK {
			parserStatus = "incomplete"
		}
		summaryMessage := fmt.Sprintf("Store scan show summary: PFN=%s product_id=%s exact_match=%t parser=%s duration=%s",
			firstNonEmpty(requestedTarget, showMetadata.PFN),
			firstNonEmpty(showMetadata.ProductID, "unknown"),
			requestedTarget != "" && strings.EqualFold(requestedTarget, showMetadata.PFN),
			parserStatus,
			duration.Round(time.Millisecond),
		)
		summaryMessage = appendStoreCommandSummaryError(summaryMessage, parseErr, result, "Store CLI show failed")
		sessionLogs.AppendContext(ctx, "app", summaryMessage, append(categories, logCategoryStoreScan))
	case "update", "updates":
		observationKind, parseErr := parseStoreCLIUpdateCheckResult(ctx, commandOutput, result)
		observationLabel := storeObservationKindLogLabel(observationKind)
		summaryMessage := fmt.Sprintf("Store scan update-check summary: PFN=%s state=%s duration=%s",
			firstNonEmpty(requestedTarget, "aggregate"),
			observationLabel,
			duration.Round(time.Millisecond),
		)
		summaryMessage = appendStoreCommandSummaryError(summaryMessage, parseErr, result, "Store CLI update check failed")
		sessionLogs.AppendContext(ctx, "app", summaryMessage, append(categories, logCategoryStoreScan))
	}
}

func isStoreUpdateCheckVerb(verb string) bool {
	return verb == "update" || verb == "updates"
}

func appendStoreCommandSummaryError(summaryMessage string, parseErr error, result CommandResult, commandFailureFallback string) string {
	if parseErr != nil {
		return summaryMessage + " error=" + sanitizeProviderDiagnostic(parseErr.Error())
	}
	if !result.OK {
		return summaryMessage + " error=" + sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, commandFailureFallback))
	}
	return summaryMessage
}

func firstStoreCommandPositionalTarget(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") || strings.EqualFold(arg, "false") || strings.EqualFold(arg, "true") {
			continue
		}
		return arg
	}
	return ""
}

func storeObservationKindLogLabel(kind StoreObservationKind) string {
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
