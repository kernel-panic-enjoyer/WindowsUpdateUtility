package updater

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

func searchPackages(query string) (PackageLookup, error) {
	query = strings.TrimSpace(query)
	if err := validatePackageSearchQuery(query); err != nil {
		return PackageLookup{}, err
	}
	appLog("Package search started for %q.", query)
	managerStatuses := detectManagers()
	commandResults := map[string]CommandResult{}

	var discoveredPackages []Package
	for _, searchResult := range runPackageSearches(query, managerStatuses) {
		commandResults[searchResult.ResultKey] = searchResult.CommandResult
		for _, searchedPackage := range searchResult.Packages {
			annotateSearchPackage(query, &searchedPackage)
			discoveredPackages = append(discoveredPackages, searchedPackage)
		}
	}
	packages := dedupePackagesByManagerID(discoveredPackages)
	sortSearchPackages(query, packages)
	appLog("Package search completed for %q with %d result(s).", query, len(packages))
	return PackageLookup{Packages: packages, Managers: managerStatuses, CommandResults: commandResults}, nil
}

func validatePackageSearchQuery(query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return errors.New("search query cannot be empty")
	}
	if len(query) > 240 || containsBlockedPackageActionChar(query) || isOptionLikePackageTarget(query) {
		return errors.New("search query contains unsupported characters")
	}
	return nil
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

func runPackageSearches(query string, managerStatuses map[string]ManagerStatus) []packageSearchResult {
	resultsCh := make(chan packageSearchResult, len(managedPackageManagers))
	var waitGroup sync.WaitGroup

	for _, runner := range packageSearchRunners {
		if !managerStatuses[runner.Manager].Available {
			continue
		}
		searchRunner := runner
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			resultsCh <- searchRunner.Run(query)
		}()
	}

	waitGroup.Wait()
	close(resultsCh)
	var results []packageSearchResult
	for searchResult := range resultsCh {
		results = append(results, searchResult)
	}
	return results
}

func searchStorePackages(query string) packageSearchResult {
	packages, commandResult := storeSearch(query)
	for i := range packages {
		packages[i].Key = packageKey(managerStore, packages[i].ID)
		packages[i].UpdateSupported = true
		packages[i].ActionBackend = backendStoreCLI
	}
	return packageSearchResult{ResultKey: managerStore, Packages: packages, CommandResult: commandResult}
}

func searchWingetPackages(query string) packageSearchResult {
	packages, commandResult := wingetSearch(query)
	return packageSearchResult{ResultKey: managerWinget, Packages: packages, CommandResult: commandResult}
}

func searchChocoPackages(query string) packageSearchResult {
	commandResult := runCommand(90*time.Second, managerCommand(managerChoco, "search", query, "--limit-output", "--no-color")...)
	packages := parseChocoList(commandResult.Stdout + "\n" + commandResult.Stderr)
	for i := range packages {
		packages[i].Key = packageKey(managerChoco, packages[i].ID)
		packages[i].Source = managerChoco
	}
	return packageSearchResult{ResultKey: managerChoco, Packages: packages, CommandResult: commandResult}
}

func annotateSearchPackage(query string, pkg *Package) {
	if pkg == nil {
		return
	}
	if pkg.Source == "" {
		switch pkg.Manager {
		case managerStore:
			pkg.Source = sourceStoreCLI
		case managerWinget:
			pkg.Source = sourceWinget
		case managerChoco:
			pkg.Source = managerChoco
		}
	}
	if pkg.ActionBackend == "" && pkg.Manager == managerStore && pkg.Source == sourceMSStore {
		pkg.ActionBackend = backendWingetMSStoreFallback
	}
	if pkg.MatchReason != "" {
		return
	}
	pkg.MatchReason = searchMatchReason(query, *pkg)
}

func searchMatchReason(query string, pkg Package) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "Returned by package-manager search."
	}
	if match := strings.TrimSpace(pkg.Match); match != "" {
		return "Matched " + humanSearchMatch(match) + "."
	}
	lowerQuery := strings.ToLower(query)
	normalizedQuery := normalizePackageIdentity(query)
	packageName := strings.TrimSpace(pkg.Name)
	packageID := strings.TrimSpace(pkg.ID)
	lowerPackageName := strings.ToLower(packageName)
	lowerPackageID := strings.ToLower(packageID)
	switch {
	case strings.EqualFold(packageName, query):
		return "Exact package name match."
	case strings.EqualFold(packageID, query):
		return "Exact package ID match."
	case normalizePackageIdentity(packageName) == normalizedQuery:
		return "Normalized package name match."
	case normalizePackageIdentity(packageID) == normalizedQuery:
		return "Normalized package ID match."
	case strings.HasPrefix(lowerPackageName, lowerQuery):
		return "Package name starts with the search text."
	case strings.HasPrefix(lowerPackageID, lowerQuery):
		return "Package ID starts with the search text."
	case strings.Contains(lowerPackageName, lowerQuery):
		return "Package name contains the search text."
	case strings.Contains(lowerPackageID, lowerQuery):
		return "Package ID contains the search text."
	}
	return "Returned by " + searchManagerName(pkg.Manager) + " search for this query."
}

func humanSearchMatch(match string) string {
	match = strings.TrimSpace(match)
	if label, value, ok := strings.Cut(match, ":"); ok {
		label = strings.TrimSpace(label)
		value = strings.TrimSpace(value)
		if label != "" && value != "" {
			return strings.ToLower(label) + " " + value
		}
	}
	return match
}

func searchManagerName(manager string) string {
	switch manager {
	case managerWinget:
		return "winget"
	case managerStore:
		return "Store"
	case managerChoco:
		return "Chocolatey"
	default:
		return "package-manager"
	}
}

func dedupePackagesByManagerID(packages []Package) []Package {
	seenManagerIDs := map[string]bool{}
	uniquePackages := []Package{}
	for _, pkg := range packages {
		managerID := strings.ToLower(packageKey(pkg.Manager, pkg.ID))
		if seenManagerIDs[managerID] {
			continue
		}
		seenManagerIDs[managerID] = true
		uniquePackages = append(uniquePackages, pkg)
	}
	return uniquePackages
}

func sortSearchPackages(query string, packages []Package) {
	sort.SliceStable(packages, func(i, j int) bool {
		leftPackage := packages[i]
		rightPackage := packages[j]
		leftPackageScore := packageSearchScore(query, leftPackage)
		rightPackageScore := packageSearchScore(query, rightPackage)
		if leftPackageScore != rightPackageScore {
			return leftPackageScore > rightPackageScore
		}
		if leftPackage.Manager != rightPackage.Manager {
			return managerSortRank(leftPackage.Manager) < managerSortRank(rightPackage.Manager)
		}
		if len(leftPackage.Name) != len(rightPackage.Name) {
			return len(leftPackage.Name) < len(rightPackage.Name)
		}
		return strings.ToLower(leftPackage.Name) < strings.ToLower(rightPackage.Name)
	})
}

func packageSearchScore(query string, pkg Package) int {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return 0
	}
	normalizedQuery := normalizePackageIdentity(lowerQuery)
	packageIdentityValues := []string{pkg.Name, pkg.ID}
	managerMatchValues := []string{pkg.Match, wingetMatchValue(pkg.Match)}
	if valuesContainExact(packageIdentityValues, lowerQuery) {
		return 1200
	}
	if valuesContainExact(managerMatchValues, lowerQuery) {
		return 1100
	}
	if normalizedValuesContainExact(packageIdentityValues, normalizedQuery) {
		return 1000
	}
	if normalizedValuesContainExact(managerMatchValues, normalizedQuery) {
		return 950
	}
	if normalizedValuesHavePrefix(packageIdentityValues, normalizedQuery) {
		return 700
	}
	if normalizedValuesHavePrefix(managerMatchValues, normalizedQuery) {
		return 650
	}
	if normalizedValuesContain(packageIdentityValues, normalizedQuery) {
		return 500
	}
	if normalizedValuesContain(managerMatchValues, normalizedQuery) {
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
	queryVariants := searchQueryVariants(query)
	var firstSuccessfulResult CommandResult
	var hasSuccessfulResult bool
	var commandResults []CommandResult
	var packages []Package
	for _, queryVariant := range queryVariants {
		commandResult := runCommand(90*time.Second, managerCommand(managerWinget, "search", queryVariant, "--accept-source-agreements", "--disable-interactivity")...)
		commandResults = append(commandResults, commandResult)
		foundPackages := parseWingetSearchPackages(commandResult)
		if len(foundPackages) > 0 {
			packages = append(packages, foundPackages...)
		}
		if commandResult.OK && !hasSuccessfulResult {
			firstSuccessfulResult = commandResult
			hasSuccessfulResult = true
		}
	}
	if len(packages) > 0 {
		return packages, combineWingetSearchResults(commandResults)
	}
	if hasSuccessfulResult {
		return nil, firstSuccessfulResult
	}
	if len(commandResults) > 0 {
		return nil, commandResults[len(commandResults)-1]
	}
	return nil, CommandResult{Code: 1, Command: "winget search", Stderr: "no winget search variants were available"}
}

func combineWingetSearchResults(results []CommandResult) CommandResult {
	if len(results) == 0 {
		return CommandResult{Code: 1, Command: "winget search", Stderr: "no winget search variants were available"}
	}
	if len(results) == 1 {
		return results[0]
	}
	var commandTexts, stdoutParts, stderrParts []string
	for _, result := range results {
		if strings.TrimSpace(result.Command) != "" {
			commandTexts = append(commandTexts, result.Command)
		}
		if strings.TrimSpace(result.Stdout) != "" {
			stdoutParts = append(stdoutParts, result.Stdout)
		}
		if strings.TrimSpace(result.Stderr) != "" {
			stderrParts = append(stderrParts, result.Stderr)
		}
	}
	return CommandResult{
		OK:      true,
		Code:    0,
		Command: strings.Join(commandTexts, " | "),
		Stdout:  strings.Join(stdoutParts, "\n"),
		Stderr:  strings.Join(stderrParts, "\n"),
	}
}

func parseWingetSearchPackages(result CommandResult) []Package {
	packages := []Package{}
	for _, parsedPackage := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
		if !isTruncatedID(parsedPackage.ID) {
			parsedPackage.Manager = wingetSourceManager(parsedPackage.Source)
			parsedPackage.Key = packageKey(parsedPackage.Manager, parsedPackage.ID)
			parsedPackage.UpdateSupported = true
			if parsedPackage.Manager == managerStore {
				parsedPackage.ActionBackend = backendWingetMSStoreFallback
			}
			packages = append(packages, parsedPackage)
		}
	}
	return packages
}

func searchQueryVariants(query string) []string {
	query = strings.TrimSpace(query)
	variants := []string{query}
	spacedVariant := normalizeSearchQuerySeparators(query)
	spacedVariant = strings.Join(strings.Fields(spacedVariant), " ")
	if spacedVariant != "" && !strings.EqualFold(spacedVariant, query) {
		variants = append(variants, spacedVariant)
	}
	compactVariant := strings.Join(strings.Fields(spacedVariant), "")
	if compactVariant != "" {
		alreadyIncluded := false
		for _, variant := range variants {
			if strings.EqualFold(variant, compactVariant) {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			variants = append(variants, compactVariant)
		}
	}
	return variants
}

func normalizeSearchQuerySeparators(query string) string {
	var normalizedQuery strings.Builder
	previousWasSeparator := false
	for _, char := range query {
		if char == '-' || char == '_' || char == '.' {
			if !previousWasSeparator {
				normalizedQuery.WriteRune(' ')
				previousWasSeparator = true
			}
			continue
		}
		normalizedQuery.WriteRune(char)
		previousWasSeparator = false
	}
	return normalizedQuery.String()
}
