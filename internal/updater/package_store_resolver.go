package updater

import (
	"strings"
	"sync"
	"time"
)

const (
	storeResolveCacheTTL    = 6 * time.Hour
	storeUnresolvedCacheTTL = 0
)

type storeSearchFunc func(query string) ([]Package, CommandResult)

func resolveStoreAppxPackages(state *State, packages []Package, storeAvailable bool, search storeSearchFunc) ([]Package, map[string]CommandResult, bool) {
	commandResults := map[string]CommandResult{}
	if !storeAvailable || search == nil {
		return packages, commandResults, false
	}
	if state.StoreResolveCache == nil {
		state.StoreResolveCache = map[string]StoreResolveCacheEntry{}
	}

	type job struct {
		index     int
		pkg       Package
		key       string
		cached    StoreResolveCacheEntry
		hasCached bool
	}
	var jobs []job
	cacheChanged := false
	for i := range packages {
		if packages[i].Source != sourceAppX || packages[i].UpdateSupported {
			continue
		}
		cacheKey := strings.ToLower(packages[i].ID)
		if entry, ok := state.StoreResolveCache[cacheKey]; ok && entry.AppXVersion == packages[i].Version {
			if entry.Resolved && validStoreResolvedTargetForPackage(packages[i], entry) {
				packages[i] = applyStoreResolution(packages[i], entry)
				if storeResolveCacheFresh(entry) {
					continue
				}
				jobs = append(jobs, job{index: i, pkg: packages[i], key: cacheKey, cached: entry, hasCached: true})
				continue
			}
			if entry.Resolved {
				delete(state.StoreResolveCache, cacheKey)
				cacheChanged = true
				appLog("Store resolver discarded stale invalid mapping for %q.", packages[i].Name)
			} else if storeResolveUnresolvedCacheFresh(entry) {
				continue
			}
		}
		jobs = append(jobs, job{index: i, pkg: packages[i], key: cacheKey})
	}
	if len(jobs) == 0 {
		return packages, commandResults, cacheChanged
	}

	appLog("Store resolver started for %d inventory-only app(s).", len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	changed := cacheChanged

	for _, item := range jobs {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			query := item.pkg.Name
			results, result := search(query)

			mu.Lock()
			commandResults["store_resolve_"+normalizePackageIdentity(item.pkg.Name)] = result
			if result.OK {
				entry := StoreResolveCacheEntry{
					AppXVersion: item.pkg.Version,
					ResolvedAt:  utcNow(),
				}
				if match, ok := chooseStoreResolution(item.pkg, results); ok {
					entry.Resolved = true
					entry.StoreID = strings.TrimSpace(match.ID)
					entry.StoreName = strings.TrimSpace(match.Name)
					entry.StoreVersion = latestPackageVersion(match)
				} else {
					if item.hasCached && item.cached.Resolved && validStoreResolvedTargetForPackage(item.pkg, item.cached) {
						entry = item.cached
						entry.AppXVersion = item.pkg.Version
						entry.ResolvedAt = utcNow()
					}
				}
				state.StoreResolveCache[item.key] = entry
				changed = true
				if entry.Resolved {
					packages[item.index] = applyStoreResolution(item.pkg, entry)
					appLog("Store resolver mapped %q to %q.", item.pkg.Name, resolvedStoreTarget(entry))
				} else {
					appLog("Store resolver kept %q as inventory-only.", item.pkg.Name)
				}
			} else {
				appLog("Store resolver search failed for %q with code %d.", item.pkg.Name, result.Code)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	appLog("Store resolver completed for %d app(s).", len(jobs))
	return packages, commandResults, changed
}

func applyStoreResolution(pkg Package, entry StoreResolveCacheEntry) Package {
	target := resolvedStoreTarget(entry)
	if target == "" || !validStoreResolvedTarget(entry) {
		return pkg
	}
	pkg.ID = target
	pkg.UpdateSupported = true
	pkg.ActionBackend = backendStoreCLIResolved
	pkg.Source = sourceAppX
	pkg.Match = strings.TrimSpace(entry.StoreName)
	storeVersion := strings.TrimSpace(entry.StoreVersion)
	if versionGreater(storeVersion, pkg.Version) {
		pkg.AvailableVersion = storeVersion
		pkg.UpdateAvailable = true
	} else {
		pkg.AvailableVersion = ""
		pkg.UpdateAvailable = false
	}
	return pkg
}

func storeResolveCacheFresh(entry StoreResolveCacheEntry) bool {
	if entry.ResolvedAt == "" {
		return false
	}
	resolvedAt, err := time.Parse(time.RFC3339, entry.ResolvedAt)
	if err != nil {
		return false
	}
	return time.Since(resolvedAt) < storeResolveCacheTTL
}

func storeResolveUnresolvedCacheFresh(entry StoreResolveCacheEntry) bool {
	if entry.ResolvedAt == "" {
		return false
	}
	resolvedAt, err := time.Parse(time.RFC3339, entry.ResolvedAt)
	if err != nil {
		return false
	}
	return time.Since(resolvedAt) < storeUnresolvedCacheTTL
}

func resolvedStoreTarget(entry StoreResolveCacheEntry) string {
	if strings.TrimSpace(entry.StoreID) != "" {
		return strings.TrimSpace(entry.StoreID)
	}
	return strings.TrimSpace(entry.StoreName)
}

func validStoreResolvedTarget(entry StoreResolveCacheEntry) bool {
	target := resolvedStoreTarget(entry)
	if target == "" || len(target) > 160 || isStoreSearchNoiseLine(target) || containsBlockedPackageActionChar(target) {
		return false
	}
	if entry.StoreName != "" && (isStoreSearchNoiseLine(entry.StoreName) || containsBlockedPackageActionChar(entry.StoreName)) {
		return false
	}
	return true
}

func validStoreResolvedTargetForPackage(pkg Package, entry StoreResolveCacheEntry) bool {
	if !validStoreResolvedTarget(entry) {
		return false
	}
	score := storeResolutionScore(storeResolutionCandidates(pkg), Package{
		Name:    entry.StoreName,
		ID:      entry.StoreID,
		Manager: managerStore,
	}, 0)
	return score >= 70
}

func latestPackageVersion(pkg Package) string {
	if strings.TrimSpace(pkg.AvailableVersion) != "" {
		return strings.TrimSpace(pkg.AvailableVersion)
	}
	return strings.TrimSpace(pkg.Version)
}

func chooseStoreResolution(appx Package, results []Package) (Package, bool) {
	candidates := storeResolutionCandidates(appx)
	bestScore := 0
	bestIndex := -1
	for i, result := range results {
		if !validStoreResolvedTarget(StoreResolveCacheEntry{StoreID: result.ID, StoreName: result.Name, Resolved: true}) {
			continue
		}
		score := storeResolutionScore(candidates, result, i)
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}
	if bestIndex >= 0 && bestScore >= 70 {
		return results[bestIndex], true
	}
	return Package{}, false
}

func storeResolutionCandidates(pkg Package) []string {
	values := []string{pkg.Name, pkg.ID, pkg.Match}
	for _, value := range []string{pkg.ID, pkg.Match} {
		base := strings.Split(strings.TrimSpace(value), "_")[0]
		values = append(values, base)
		if strings.Contains(base, ".") {
			parts := strings.Split(base, ".")
			values = append(values, parts[len(parts)-1])
		}
	}

	seen := map[string]bool{}
	var candidates []string
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		candidates = append(candidates, normalized)
	}
	return candidates
}

func storeResolutionScore(candidates []string, result Package, rank int) int {
	resultValues := []string{
		normalizePackageIdentity(result.Name),
		normalizePackageIdentity(result.ID),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, value := range resultValues {
			if value == "" {
				continue
			}
			if value == candidate {
				return 100
			}
			if len(candidate) >= 5 && rank == 0 && strings.Contains(value, candidate) {
				return 70
			}
			if len(candidate) >= 5 && rank == 0 && strings.Contains(candidate, value) && len(value)*100/len(candidate) >= 80 {
				return 70
			}
		}
	}
	return 0
}
