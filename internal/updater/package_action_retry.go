package updater

import "strings"

func shouldTryAlternatePackageTarget(result CommandResult) bool {
	if result.OK || result.Code == commandCancelledCode || result.Code == 124 {
		return false
	}
	output := normalizedCommandOutput(result)
	if strings.Contains(output, "requires explicit user confirmation") {
		return false
	}
	return outputContainsAny(output, []string{
		"no applicable upgrade",
		"kein anwendbares upgrade",
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

func shouldForceInstallAfterWingetUpgrade(result CommandResult) bool {
	if result.OK {
		return false
	}
	return outputContainsAny(normalizedCommandOutput(result), []string{
		"no applicable upgrade",
		"kein anwendbares upgrade",
	})
}

func shouldRetryWingetIncludeUnknown(result CommandResult) bool {
	if result.OK || result.Code == commandCancelledCode || result.Code == 124 {
		return false
	}
	output := normalizedCommandOutput(result)
	if outputContainsAny(output, []string{"--include-unknown", "include unknown", "include-unknown"}) {
		return true
	}
	if (strings.Contains(output, "unknown") || strings.Contains(output, "unbekannt")) &&
		(strings.Contains(output, "version") || strings.Contains(output, "installed package")) {
		return true
	}
	return false
}

func shouldRetryWingetIncludePinned(result CommandResult) bool {
	if result.OK || result.Code == commandCancelledCode || result.Code == 124 {
		return false
	}
	output := normalizedCommandOutput(result)
	if outputContainsAny(output, []string{"--include-pinned", "include-pinned", "include pinned"}) {
		return true
	}
	return outputContainsAny(output, []string{"pinning", "pinned"})
}

func shouldRetryStoreUpdateWithoutApply(result CommandResult) bool {
	if result.OK || result.Code == commandCancelledCode || result.Code == 124 {
		return false
	}
	output := normalizedCommandOutput(result)
	if !strings.Contains(output, "apply") {
		return false
	}
	return outputContainsAny(output, []string{
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
