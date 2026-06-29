package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func runPackageUpdateCandidates(ctx context.Context, candidates []string, label string, run func(string) CommandResult) CommandResult {
	candidates = uniqueUpdateTargets(candidates)
	if len(candidates) == 0 {
		return validationCommandResult("update", errors.New("package id contains unsupported characters"))
	}
	var merged CommandResult
	for index, target := range candidates {
		result := run(target)
		if index == 0 {
			merged = result
		} else {
			merged = mergeCommandAttemptsWithFinalResult(merged, result, fmt.Sprintf("%s %q", label, target))
		}
		if result.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(result) {
			return merged
		}
		appLog("Update target %q was not accepted; trying alternate target.", target)
	}
	return merged
}

func wingetUpdateTargetCandidates(pkg Package) []string {
	return uniqueUpdateTargets([]string{
		pkg.ID,
		pkg.Match,
	})
}

func wingetNameFallbackTarget(pkg Package) string {
	name := strings.TrimSpace(pkg.Name)
	if name == "" || len(name) > 160 || containsBlockedPackageActionChar(name) {
		return ""
	}
	for _, existing := range []string{pkg.ID, pkg.Match} {
		if strings.EqualFold(strings.TrimSpace(existing), name) {
			return ""
		}
	}
	return name
}

func chocoUpdateTargetCandidates(pkg Package) []string {
	id := strings.TrimSpace(pkg.ID)
	if id == "" {
		return nil
	}
	values := []string{id}
	lower := strings.ToLower(id)
	for _, suffix := range []string{".install", ".portable"} {
		if strings.HasSuffix(lower, suffix) && len(id) > len(suffix) {
			values = append(values, id[:len(id)-len(suffix)])
		}
	}
	if !strings.HasSuffix(lower, ".install") && !strings.HasSuffix(lower, ".portable") && isSafePackageID(id) {
		values = append(values, id+".install", id+".portable")
	}
	return uniquePackageIDTargets(values)
}

func uniqueUpdateTargets(values []string) []string {
	seen := map[string]bool{}
	var targets []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 240 || containsBlockedPackageActionChar(value) {
			continue
		}
		normalized := strings.ToLower(value)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		targets = append(targets, value)
	}
	return targets
}

func uniquePackageIDTargets(values []string) []string {
	seen := map[string]bool{}
	var targets []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !isSafePackageID(value) {
			continue
		}
		normalized := strings.ToLower(value)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		targets = append(targets, value)
	}
	return targets
}
