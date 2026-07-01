package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func runPackageUpdateCandidates(ctx context.Context, rawCandidates []string, attemptLabel string, runTargetUpdate func(string) CommandResult) CommandResult {
	targets := uniqueUpdateTargets(rawCandidates)
	if len(targets) == 0 {
		return validationCommandResult("update", errors.New("package id contains unsupported characters"))
	}
	var mergedResult CommandResult
	for attemptIndex, target := range targets {
		attemptResult := runTargetUpdate(target)
		if attemptIndex == 0 {
			mergedResult = attemptResult
		} else {
			mergedResult = mergeCommandAttemptsWithFinalResult(mergedResult, attemptResult, fmt.Sprintf("%s %q", attemptLabel, target))
		}
		if attemptResult.OK || ctx.Err() != nil || !shouldTryAlternatePackageTarget(attemptResult) {
			return mergedResult
		}
		appLog("Update target %q was not accepted; trying alternate target.", target)
	}
	return mergedResult
}

func wingetUpdateTargetCandidates(pkg Package) []string {
	return uniqueUpdateTargets([]string{
		pkg.ID,
		pkg.Match,
	})
}

func wingetNameFallbackTarget(pkg Package) string {
	displayName := strings.TrimSpace(pkg.Name)
	if displayName == "" || len(displayName) > 160 || containsBlockedPackageActionChar(displayName) || isOptionLikePackageTarget(displayName) {
		return ""
	}
	for _, primaryTarget := range []string{pkg.ID, pkg.Match} {
		if strings.EqualFold(strings.TrimSpace(primaryTarget), displayName) {
			return ""
		}
	}
	return displayName
}

func chocoUpdateTargetCandidates(pkg Package) []string {
	packageID := strings.TrimSpace(pkg.ID)
	if packageID == "" {
		return nil
	}
	candidateIDs := []string{packageID}
	lowerPackageID := strings.ToLower(packageID)
	variantSuffixes := [...]string{".install", ".portable"}
	hasVariantSuffix := false
	for _, variantSuffix := range variantSuffixes {
		if strings.HasSuffix(lowerPackageID, variantSuffix) {
			hasVariantSuffix = true
			if len(packageID) > len(variantSuffix) {
				candidateIDs = append(candidateIDs, packageID[:len(packageID)-len(variantSuffix)])
			}
		}
	}
	if !hasVariantSuffix && isSafePackageID(packageID) {
		for _, variantSuffix := range variantSuffixes {
			candidateIDs = append(candidateIDs, packageID+variantSuffix)
		}
	}
	return uniquePackageIDTargets(candidateIDs)
}

func uniqueUpdateTargets(values []string) []string {
	seenTargets := map[string]bool{}
	var uniqueTargets []string
	for _, candidate := range values {
		target := strings.TrimSpace(candidate)
		if target == "" || len(target) > 240 || containsBlockedPackageActionChar(target) || isOptionLikePackageTarget(target) {
			continue
		}
		normalizedTarget := strings.ToLower(target)
		if seenTargets[normalizedTarget] {
			continue
		}
		seenTargets[normalizedTarget] = true
		uniqueTargets = append(uniqueTargets, target)
	}
	return uniqueTargets
}

func uniquePackageIDTargets(values []string) []string {
	seenTargets := map[string]bool{}
	var uniqueTargets []string
	for _, candidate := range values {
		target := strings.TrimSpace(candidate)
		if target == "" || isOptionLikePackageTarget(target) || !isSafePackageID(target) {
			continue
		}
		normalizedTarget := strings.ToLower(target)
		if seenTargets[normalizedTarget] {
			continue
		}
		seenTargets[normalizedTarget] = true
		uniqueTargets = append(uniqueTargets, target)
	}
	return uniqueTargets
}
