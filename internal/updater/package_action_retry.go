package updater

import "strings"

func shouldTryAlternatePackageTarget(result CommandResult) bool {
	if !isRetryablePackageActionFailure(result) {
		return false
	}
	normalizedOutput := normalizedCommandOutput(result)
	if strings.Contains(normalizedOutput, "requires explicit user confirmation") {
		return false
	}
	return outputContainsAny(normalizedOutput, []string{
		"no applicable upgrade",
		"no available upgrade",
		"kein anwendbares upgrade",
		"kein verfügbares upgrade",
		"keine neueren paketversionen",
		"no installed package found",
		"no package found",
		"no product found",
		"product not found",
		"not found",
		"unable to find package",
		"unable to find",
		"could not find installed product metadata",
		"no installed package matching",
		"not recognized",
		"does not match any installed package",
	})
}

func shouldRetryWingetForceUpgrade(result CommandResult) bool {
	if result.OK {
		return false
	}
	normalizedOutput := normalizedCommandOutput(result)
	return outputContainsAny(normalizedOutput, []string{
		"no applicable upgrade",
		"kein anwendbares upgrade",
	})
}

func shouldRetryWingetIncludeUnknown(result CommandResult) bool {
	if !isRetryablePackageActionFailure(result) {
		return false
	}
	normalizedOutput := normalizedCommandOutput(result)
	if outputContainsAny(normalizedOutput, []string{"--include-unknown", "include unknown", "include-unknown"}) {
		return true
	}
	if (strings.Contains(normalizedOutput, "unknown") || strings.Contains(normalizedOutput, "unbekannt")) &&
		(strings.Contains(normalizedOutput, "version") || strings.Contains(normalizedOutput, "installed package")) {
		return true
	}
	return false
}

func shouldRetryWingetIncludePinned(result CommandResult) bool {
	if !isRetryablePackageActionFailure(result) {
		return false
	}
	normalizedOutput := normalizedCommandOutput(result)
	if outputContainsAny(normalizedOutput, []string{"--include-pinned", "include-pinned", "include pinned"}) {
		return true
	}
	return outputContainsAny(normalizedOutput, []string{"pinning", "pinned"})
}

func shouldRetryStoreUpdateWithoutApply(result CommandResult) bool {
	if !isRetryablePackageActionFailure(result) {
		return false
	}
	normalizedOutput := normalizedCommandOutput(result)
	if !strings.Contains(normalizedOutput, "apply") {
		return false
	}
	return outputContainsAny(normalizedOutput, []string{
		"unrecognized option",
		"unknown option",
		"unknown argument",
		"unrecognized argument",
		"unrecognized command or argument",
		"argument not recognized",
		"option not recognized",
		"parameter not recognized",
		"argumentname wurde",
		"nicht erkannt",
		"unbekannte option",
		"unbekanntes argument",
	})
}

func isRetryablePackageActionFailure(result CommandResult) bool {
	return !result.OK && result.Code != commandCancelledCode && result.Code != commandTimeoutCode
}
