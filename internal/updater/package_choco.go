package updater

import (
	"context"
	"strings"
	"time"
)

func parseChocoList(output string) []Package {
	var installedPackages []Package
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		columns := strings.SplitN(line, "|", 3)
		if len(columns) < 2 {
			continue
		}
		packageID := strings.TrimSpace(columns[0])
		installedVersion := strings.TrimSpace(columns[1])
		if packageID == "" || installedVersion == "" || strings.Contains(packageID, " ") || strings.HasPrefix(strings.ToLower(packageID), "this is try") {
			continue
		}
		installedPackages = append(installedPackages, Package{ID: packageID, Name: packageID, Version: installedVersion, Manager: managerChoco})
	}
	return installedPackages
}

func parseChocoOutdated(output string) map[string]string {
	availableVersionsByPackageID := map[string]string{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		columns := strings.SplitN(line, "|", 4)
		if len(columns) < 3 {
			continue
		}
		packageID := strings.ToLower(strings.TrimSpace(columns[0]))
		availableVersion := strings.TrimSpace(columns[2])
		if packageID != "" && availableVersion != "" {
			availableVersionsByPackageID[packageID] = availableVersion
		}
	}
	return availableVersionsByPackageID
}

func chocoInstalled() ([]Package, CommandResult) {
	return chocoInstalledContext(context.Background())
}

func chocoInstalledContext(ctx context.Context) ([]Package, CommandResult) {
	result := runCommandContext(ctx, 90*time.Second, managerCommand(managerChoco, "list", "--local-only", "--limit-output", "--no-color")...)
	return parseChocoList(chocoCombinedOutput(result)), result
}

func chocoUpdates() (map[string]string, map[string]Package, CommandResult) {
	return chocoUpdatesContext(context.Background())
}

func chocoUpdatesContext(ctx context.Context) (map[string]string, map[string]Package, CommandResult) {
	result := runCommandContext(ctx, 120*time.Second, managerCommand(managerChoco, "outdated", "--limit-output", "--no-color")...)
	return parseChocoOutdated(chocoCombinedOutput(result)), nil, result
}

func chocoCombinedOutput(result CommandResult) string {
	return result.Stdout + "\n" + result.Stderr
}
