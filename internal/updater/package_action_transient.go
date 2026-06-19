package updater

func isTransientPackageManagerFailure(manager string, result CommandResult) bool {
	if result.OK {
		return false
	}
	if manager == managerWinget || manager == managerStore {
		if isWingetTransientFailure(result) {
			return true
		}
	}
	return isCommonInstallerTransientFailure(result) || (manager == managerChoco && isChocoTransientFailure(result))
}

func isWingetSourceFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	return outputContainsAny(normalizedCommandOutput(result), []string{
		"failed when opening source",
		"failed when searching source",
		"failed in attempting to update the source",
		"failed to update source",
		"failed to open source",
		"failed to get source",
		"failed to query source",
		"source data is corrupted",
		"source is not configured",
		"source not found",
		"no sources are configured",
		"data required by the source is missing",
		"the source agreements were not agreed to",
		"0x8a15000f",
		"0x8a150010",
		"0x8a150011",
		"0x8a150014",
	})
}

func isCommonInstallerTransientFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	output := normalizedCommandOutput(result)
	return result.Code == 1618 || outputContainsAny(output, []string{
		"another installation is already in progress",
		"another install is already in progress",
		"another transaction",
		"currently running",
		"installation is in progress",
		"installer is running",
		"being used by another process",
		"locked by another process",
		"unable to acquire lock",
		"could not acquire lock",
		"exit code 1618",
		"error 1618",
	}) || isNetworkTransientFailure(result)
}

func isNetworkTransientFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	return outputContainsAny(normalizedCommandOutput(result), []string{
		"connection timed out",
		"operation timed out",
		"request timed out",
		"timeout was reached",
		"timed out while",
		"connection reset",
		"connection was reset",
		"connection aborted",
		"connection closed",
		"connection refused",
		"temporarily unavailable",
		"temporary failure",
		"remote name could not be resolved",
		"name resolution failure",
		"could not resolve host",
		"unable to resolve",
		"unable to connect to the remote server",
		"underlying connection was closed",
		"ssl connection could not be established",
		"tls connection could not be established",
		"transport connection",
		"too many requests",
		"rate limit",
		" http 429",
		"status 429",
		"response status code does not indicate success: 429",
		" http 502",
		"status 502",
		"bad gateway",
		" http 503",
		"status 503",
		"service unavailable",
		" http 504",
		"status 504",
		"gateway timeout",
	})
}

func isChocoTransientFailure(result CommandResult) bool {
	if result.OK {
		return false
	}
	return outputContainsAny(normalizedCommandOutput(result), []string{
		"the process cannot access the file",
		"chocolatey is already running",
		"existing chocolatey process",
	})
}
