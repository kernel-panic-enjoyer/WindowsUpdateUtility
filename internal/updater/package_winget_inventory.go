package updater

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

func wingetInstalled() ([]Package, CommandResult) {
	return wingetInstalledContext(context.Background())
}

func wingetInstalledContext(ctx context.Context) ([]Package, CommandResult) {
	var listCommandResult CommandResult
	var listedPackages []Package
	var exportCommandResult CommandResult
	var exportedPackages []Package
	var exportFilePath string
	if tempDir, err := appTempDir(); err == nil {
		if exportFile, err := os.CreateTemp(tempDir, "windows-updater-winget-*.json"); err == nil {
			exportFilePath = exportFile.Name()
			_ = exportFile.Close()
			_ = os.Remove(exportFilePath)
			defer os.Remove(exportFilePath)
		}
	}

	var inventoryCommands sync.WaitGroup
	inventoryCommands.Add(1)
	go func() {
		defer inventoryCommands.Done()
		listCommandResult = runCommandContext(ctx, 120*time.Second, managerCommand(managerWinget, "list", "--accept-source-agreements", "--disable-interactivity")...)
		listedPackages = parseWingetTable(listCommandResult.Stdout + "\n" + listCommandResult.Stderr)
	}()
	if exportFilePath != "" {
		inventoryCommands.Add(1)
		go func() {
			defer inventoryCommands.Done()
			exportCommandResult = runCommandContext(ctx, 180*time.Second, managerCommand(managerWinget, "export", "-o", exportFilePath, "--include-versions", "--accept-source-agreements", "--disable-interactivity")...)
			exportData, _ := os.ReadFile(exportFilePath)
			exportedPackages = parseWingetExport(string(exportData))
		}()
	}
	inventoryCommands.Wait()

	listCommandResult.Stderr += exportCommandResult.Stderr
	if len(exportedPackages) > 0 {
		return mergeWingetExportWithTable(exportedPackages, listedPackages), listCommandResult
	}
	return listedPackages, listCommandResult
}

func wingetUpdates() (map[string]string, map[string]Package, CommandResult) {
	return wingetUpdatesContext(context.Background())
}

func wingetUpdatesContext(ctx context.Context) (map[string]string, map[string]Package, CommandResult) {
	availableVersionsByKey := map[string]string{}
	updateDetailsByKey := map[string]Package{}
	defaultUpgradeResult := runCommandContext(ctx, 120*time.Second, managerCommand(managerWinget, "upgrade", "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(availableVersionsByKey, updateDetailsByKey, defaultUpgradeResult.Stdout+"\n"+defaultUpgradeResult.Stderr, "")
	storeUpgradeResult := runCommandContext(ctx, 120*time.Second, managerCommand(managerWinget, "upgrade", "--source", sourceMSStore, "--accept-source-agreements", "--disable-interactivity")...)
	mergeWingetUpdateOutput(availableVersionsByKey, updateDetailsByKey, storeUpgradeResult.Stdout+"\n"+storeUpgradeResult.Stderr, managerStore)
	return availableVersionsByKey, updateDetailsByKey, mergeReadOnlyCommandResults(defaultUpgradeResult, storeUpgradeResult, "winget msstore update check")
}

func mergeWingetUpdateOutput(availableVersionsByKey map[string]string, updateDetailsByKey map[string]Package, output, managerOverride string) {
	for _, parsedPackage := range parseWingetTable(output) {
		if parsedPackage.AvailableVersion == "" {
			continue
		}
		if storeFallback, ok := wingetTruncatedMSIXPackage(parsedPackage); ok {
			parsedPackage = storeFallback
		} else if isTruncatedID(parsedPackage.ID) {
			continue
		}
		effectiveManager := parsedPackage.Manager
		if managerOverride != "" {
			effectiveManager = managerOverride
		}
		key := packageKey(effectiveManager, strings.ToLower(parsedPackage.ID))
		parsedPackage.Key = key
		parsedPackage.Manager = effectiveManager
		availableVersionsByKey[key] = parsedPackage.AvailableVersion
		if updateDetailsByKey != nil {
			updateDetailsByKey[key] = parsedPackage
		}
	}
}

func mergeReadOnlyCommandResults(primaryResult, secondaryResult CommandResult, secondaryLabel string) CommandResult {
	mergedResult := primaryResult
	if mergedResult.Command != "" && secondaryResult.Command != "" {
		mergedResult.Command += "\n" + secondaryLabel + ": " + secondaryResult.Command
	} else if secondaryResult.Command != "" {
		mergedResult.Command = secondaryResult.Command
	}
	mergedResult.Stdout = appendReadOnlyCommandOutput(primaryResult.Stdout, secondaryResult.Stdout)
	mergedResult.Stderr = appendReadOnlyCommandOutput(primaryResult.Stderr, secondaryResult.Stderr)
	if primaryResult.OK || secondaryResult.OK {
		mergedResult.OK = true
		mergedResult.Code = 0
		return mergedResult
	}
	if secondaryResult.Code != 0 {
		mergedResult.Code = secondaryResult.Code
	}
	return mergedResult
}

func appendReadOnlyCommandOutput(primaryOutput, secondaryOutput string) string {
	mergedOutput := strings.TrimRight(primaryOutput, "\r\n")
	if mergedOutput != "" && secondaryOutput != "" {
		mergedOutput += "\n"
	}
	return mergedOutput + secondaryOutput
}
