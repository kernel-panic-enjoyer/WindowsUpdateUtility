package updater

import (
	"os"
	"strings"
	"sync"
	"time"
)

func wingetInstalled() ([]Package, CommandResult) {
	var listResult CommandResult
	var tablePackages []Package
	var exportResult CommandResult
	var exported []Package
	exportPath := ""
	if tmp, err := os.CreateTemp("", "windows-updater-winget-*.json"); err == nil {
		exportPath = tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(exportPath)
		defer os.Remove(exportPath)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		listResult = runCommand(120*time.Second, managerCommand(managerWinget, "list", "--accept-source-agreements", "--disable-interactivity")...)
		tablePackages = parseWingetTable(listResult.Stdout + "\n" + listResult.Stderr)
	}()
	if exportPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exportResult = runCommand(180*time.Second, managerCommand(managerWinget, "export", "-o", exportPath, "--include-versions", "--accept-source-agreements", "--disable-interactivity")...)
			exportOutput, _ := os.ReadFile(exportPath)
			exported = parseWingetExport(string(exportOutput))
		}()
	}
	wg.Wait()

	listResult.Stderr += exportResult.Stderr
	if len(exported) > 0 {
		return mergeWingetExportWithTable(exported, tablePackages), listResult
	}
	return tablePackages, listResult
}

func wingetUpdates() (map[string]string, CommandResult) {
	updates := map[string]string{}
	result := runCommand(120*time.Second, managerCommand(managerWinget, "upgrade", "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(updates, result.Stdout+"\n"+result.Stderr, "")
	storeResult := runCommand(120*time.Second, managerCommand(managerWinget, "upgrade", "--source", sourceMSStore, "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(updates, storeResult.Stdout+"\n"+storeResult.Stderr, managerStore)
	return updates, mergeReadOnlyCommandResults(result, storeResult, "winget msstore update check")
}

func mergeWingetUpdateOutput(updates map[string]string, output, forceManager string) {
	for _, pkg := range parseWingetTable(output) {
		if pkg.AvailableVersion == "" {
			continue
		}
		if fallback, ok := wingetTruncatedMSIXPackage(pkg); ok {
			pkg = fallback
		} else if fallback, ok := wingetTruncatedNameTargetPackage(pkg); ok {
			pkg = fallback
		} else if isTruncatedID(pkg.ID) {
			continue
		}
		manager := pkg.Manager
		if forceManager != "" {
			manager = forceManager
		}
		updates[packageKey(manager, strings.ToLower(pkg.ID))] = pkg.AvailableVersion
	}
}

func mergeReadOnlyCommandResults(primary, secondary CommandResult, label string) CommandResult {
	merged := primary
	if merged.Command != "" && secondary.Command != "" {
		merged.Command += "\n" + label + ": " + secondary.Command
	} else if secondary.Command != "" {
		merged.Command = secondary.Command
	}
	merged.Stdout = strings.TrimRight(primary.Stdout, "\r\n")
	if merged.Stdout != "" && secondary.Stdout != "" {
		merged.Stdout += "\n"
	}
	merged.Stdout += secondary.Stdout
	merged.Stderr = strings.TrimRight(primary.Stderr, "\r\n")
	if merged.Stderr != "" && secondary.Stderr != "" {
		merged.Stderr += "\n"
	}
	merged.Stderr += secondary.Stderr
	if primary.OK || secondary.OK {
		merged.OK = true
		merged.Code = 0
		return merged
	}
	if secondary.Code != 0 {
		merged.Code = secondary.Code
	}
	return merged
}
