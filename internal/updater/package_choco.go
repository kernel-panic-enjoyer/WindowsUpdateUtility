package updater

import (
	"context"
	"strings"
	"time"
)

func parseChocoList(output string) []Package {
	var packages []Package
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		version := strings.TrimSpace(parts[1])
		if id == "" || version == "" || strings.Contains(id, " ") || strings.HasPrefix(strings.ToLower(id), "this is try") {
			continue
		}
		packages = append(packages, Package{ID: id, Name: id, Version: version, Manager: managerChoco})
	}
	return packages
}

func parseChocoOutdated(output string) map[string]string {
	updates := map[string]string{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 3 {
			id := strings.ToLower(strings.TrimSpace(parts[0]))
			available := strings.TrimSpace(parts[2])
			if id != "" && available != "" {
				updates[id] = available
			}
		}
	}
	return updates
}

func chocoInstalled() ([]Package, CommandResult) {
	return chocoInstalledContext(context.Background())
}

func chocoInstalledContext(ctx context.Context) ([]Package, CommandResult) {
	result := runCommandContext(ctx, 90*time.Second, managerCommand(managerChoco, "list", "--local-only", "--limit-output", "--no-color")...)
	return parseChocoList(result.Stdout + "\n" + result.Stderr), result
}

func chocoUpdates() (map[string]string, map[string]Package, CommandResult) {
	return chocoUpdatesContext(context.Background())
}

func chocoUpdatesContext(ctx context.Context) (map[string]string, map[string]Package, CommandResult) {
	result := runCommandContext(ctx, 120*time.Second, managerCommand(managerChoco, "outdated", "--limit-output", "--no-color")...)
	return parseChocoOutdated(result.Stdout + "\n" + result.Stderr), nil, result
}
