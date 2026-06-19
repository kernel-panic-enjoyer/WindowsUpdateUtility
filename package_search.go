package main

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

func searchPackages(query string) (PackageLookup, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return PackageLookup{}, errors.New("search query cannot be empty")
	}
	appLog("Package search started for %q.", query)
	managers := detectManagers()
	commandResults := map[string]CommandResult{}

	var foundPackages []Package
	for _, search := range runPackageSearches(query, managers) {
		commandResults[search.ResultKey] = search.CommandResult
		foundPackages = append(foundPackages, search.Packages...)
	}
	packages := dedupePackagesByManagerID(foundPackages)
	sortSearchPackages(query, packages)
	appLog("Package search completed for %q with %d result(s).", query, len(packages))
	return PackageLookup{Packages: packages, Managers: managers, CommandResults: commandResults}, nil
}

type packageSearchResult struct {
	ResultKey     string
	Packages      []Package
	CommandResult CommandResult
}

type packageSearchRunner struct {
	Manager string
	Run     func(string) packageSearchResult
}

var packageSearchRunners = []packageSearchRunner{
	{managerStore, searchStorePackages},
	{managerWinget, searchWingetPackages},
	{managerChoco, searchChocoPackages},
}

func runPackageSearches(query string, managers map[string]ManagerStatus) []packageSearchResult {
	searchCh := make(chan packageSearchResult, len(managedPackageManagers))
	var wg sync.WaitGroup

	for _, runner := range packageSearchRunners {
		if !managers[runner.Manager].Available {
			continue
		}
		runner := runner
		wg.Add(1)
		go func() {
			defer wg.Done()
			searchCh <- runner.Run(query)
		}()
	}

	wg.Wait()
	close(searchCh)
	var results []packageSearchResult
	for search := range searchCh {
		results = append(results, search)
	}
	return results
}

func searchStorePackages(query string) packageSearchResult {
	packages, result := storeSearch(query)
	for i := range packages {
		packages[i].Key = packageKey(managerStore, packages[i].ID)
		packages[i].UpdateSupported = true
		packages[i].ActionBackend = backendStoreCLI
	}
	return packageSearchResult{ResultKey: "store_search", Packages: packages, CommandResult: result}
}

func searchWingetPackages(query string) packageSearchResult {
	packages, result := wingetSearch(query)
	return packageSearchResult{ResultKey: managerWinget, Packages: packages, CommandResult: result}
}

func searchChocoPackages(query string) packageSearchResult {
	result := runCommand(90*time.Second, managerCommand(managerChoco, "search", query, "--limit-output", "--no-color")...)
	packages := parseChocoList(result.Stdout + "\n" + result.Stderr)
	for i := range packages {
		packages[i].Key = packageKey(managerChoco, packages[i].ID)
	}
	return packageSearchResult{ResultKey: managerChoco, Packages: packages, CommandResult: result}
}

func dedupePackagesByManagerID(packages []Package) []Package {
	seen := map[string]bool{}
	deduped := []Package{}
	for _, pkg := range packages {
		key := strings.ToLower(packageKey(pkg.Manager, pkg.ID))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, pkg)
	}
	return deduped
}

func sortSearchPackages(query string, packages []Package) {
	sort.SliceStable(packages, func(i, j int) bool {
		leftScore := packageSearchScore(query, packages[i])
		rightScore := packageSearchScore(query, packages[j])
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		if packages[i].Manager != packages[j].Manager {
			return managerSortRank(packages[i].Manager) < managerSortRank(packages[j].Manager)
		}
		if len(packages[i].Name) != len(packages[j].Name) {
			return len(packages[i].Name) < len(packages[j].Name)
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
}

func packageSearchScore(query string, pkg Package) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0
	}
	queryNorm := normalizePackageIdentity(query)
	primaryValues := []string{pkg.Name, pkg.ID}
	matchValues := []string{pkg.Match, wingetMatchValue(pkg.Match)}
	if valuesContainExact(primaryValues, query) {
		return 1200
	}
	if valuesContainExact(matchValues, query) {
		return 1100
	}
	if normalizedValuesContainExact(primaryValues, queryNorm) {
		return 1000
	}
	if normalizedValuesContainExact(matchValues, queryNorm) {
		return 950
	}
	if normalizedValuesHavePrefix(primaryValues, queryNorm) {
		return 700
	}
	if normalizedValuesHavePrefix(matchValues, queryNorm) {
		return 650
	}
	if normalizedValuesContain(primaryValues, queryNorm) {
		return 500
	}
	if normalizedValuesContain(matchValues, queryNorm) {
		return 450
	}
	return 0
}

func valuesContainExact(values []string, query string) bool {
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == query {
			return true
		}
	}
	return false
}

func normalizedValuesContainExact(values []string, query string) bool {
	for _, value := range values {
		if normalizePackageIdentity(value) == query {
			return true
		}
	}
	return false
}

func normalizedValuesHavePrefix(values []string, query string) bool {
	if query == "" {
		return false
	}
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized != "" && strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}

func normalizedValuesContain(values []string, query string) bool {
	if query == "" {
		return false
	}
	for _, value := range values {
		normalized := normalizePackageIdentity(value)
		if normalized != "" && strings.Contains(normalized, query) {
			return true
		}
	}
	return false
}

func wingetSearch(query string) ([]Package, CommandResult) {
	variants := searchQueryVariants(query)
	var cleanEmptyResult *CommandResult
	for index, candidate := range variants {
		result := runCommand(90*time.Second, managerCommand(managerWinget, "search", candidate, "--accept-source-agreements", "--disable-interactivity")...)
		packages := parseWingetSearchPackages(result)
		if len(packages) > 0 {
			return packages, result
		}
		if result.OK && cleanEmptyResult == nil {
			cleanEmptyResult = &result
		}
		if index == len(variants)-1 {
			if cleanEmptyResult != nil {
				return nil, *cleanEmptyResult
			}
			return nil, result
		}
	}
	return nil, CommandResult{Code: 1, Command: "winget search", Stderr: "no winget search variants were available"}
}

func parseWingetSearchPackages(result CommandResult) []Package {
	packages := []Package{}
	for _, pkg := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
		if !isTruncatedID(pkg.ID) {
			pkg.Manager = wingetSourceManager(pkg.Source)
			pkg.Key = packageKey(pkg.Manager, pkg.ID)
			pkg.UpdateSupported = true
			if pkg.Manager == managerStore {
				pkg.ActionBackend = backendWingetMSStoreFallback
			}
			packages = append(packages, pkg)
		}
	}
	return packages
}

func searchQueryVariants(query string) []string {
	query = strings.TrimSpace(query)
	variants := []string{query}
	normalized := normalizeSearchQuerySeparators(query)
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized != "" && !strings.EqualFold(normalized, query) {
		variants = append(variants, normalized)
	}
	return variants
}

func normalizeSearchQuerySeparators(query string) string {
	var normalized strings.Builder
	lastWasSeparator := false
	for _, r := range query {
		if r == '-' || r == '_' || r == '.' {
			if !lastWasSeparator {
				normalized.WriteRune(' ')
				lastWasSeparator = true
			}
			continue
		}
		normalized.WriteRune(r)
		lastWasSeparator = false
	}
	return normalized.String()
}
