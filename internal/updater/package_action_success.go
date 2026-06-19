package updater

import "strings"

func normalizePackageActionResult(manager string, result CommandResult) CommandResult {
	if result.OK {
		return result
	}
	if isPackageActionSuccessCode(manager, result.Code) || isPackageActionSemanticSuccess(result) {
		result.OK = true
		note := packageActionSuccessCodeNote(result.Code)
		if note != "" && !strings.Contains(result.Stdout+result.Stderr, note) {
			if strings.TrimSpace(result.Stdout) != "" {
				result.Stdout = strings.TrimRight(result.Stdout, "\r\n") + "\n" + note
			} else {
				result.Stdout = note
			}
		}
	}
	return result
}

func isPackageActionSemanticSuccess(result CommandResult) bool {
	if result.OK {
		return true
	}
	output := normalizedCommandOutput(result)
	if outputContainsAny(output, []string{
		"failed",
		"failure",
		"error",
		"exception",
		"denied",
		"not found",
		"no package found",
		"no product found",
		"could not find",
	}) {
		return false
	}
	return outputContainsAny(output, []string{
		"already up to date",
		"already installed",
		"no available update",
		"no update available",
		"no updates available",
		"no updates found",
		"no newer package versions are available",
		"the package is already installed",
		"successfully installed",
		"successfully upgraded",
		"successfully updated",
		"installation completed",
		"upgrade completed",
		"update completed",
	})
}

func isPackageActionSuccessCode(manager string, code int) bool {
	switch code {
	case 1641, 3010:
		return manager == managerWinget || manager == managerChoco || manager == managerStore
	default:
		return false
	}
}

func packageActionSuccessCodeNote(code int) string {
	switch code {
	case 1641:
		return "Command completed successfully and initiated a restart."
	case 3010:
		return "Command completed successfully; restart required."
	default:
		return ""
	}
}
