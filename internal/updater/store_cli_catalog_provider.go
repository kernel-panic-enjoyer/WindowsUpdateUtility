package updater

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	storeCLIExactProviderID          = "store-cli-exact"
	storeCLIUpdatesProviderID        = "store-cli-updates"
	storeCLIExactProviderTimeout     = 25 * time.Second
	storeCLIUpdatesProviderTimeout   = 90 * time.Second
	storeCLIExactProviderConcurrency = 4
)

type storeCLIExactCatalogProvider struct {
	Run            func(context.Context, time.Duration, ...string) CommandResult
	Now            func() time.Time
	Version        string
	Concurrency    int
	StateCheckPFNs map[string]bool
}

type storeCLIProductMetadata struct {
	ProductID string
	PFN       string
	Name      string
}

type storeCLIUpdatesCatalogProvider struct {
	Run     func(context.Context, time.Duration, ...string) CommandResult
	Now     func() time.Time
	Version string
}

type storeCLIUpdatesParsedOffer struct {
	ProductID        string
	PFN              string
	AvailableVersion string
	Inapplicable     bool
}

type storeCLIUpdatesParseResult struct {
	NoUpdates            bool
	ExplicitNoUpdates    bool
	Offers               []storeCLIUpdatesParsedOffer
	ExpectedOfferCount   int
	FailureDiagnostics   []string
	Contradictory        bool
	CompleteCoverage     bool
	UnrecognizedLines    []string
	MalformedRecords     []string
	IncompleteReasonText string
}

func (provider storeCLIExactCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: storeCLIExactProviderID, Name: "Store CLI exact catalog", Backend: backendStoreCLI}
}

func (provider storeCLIUpdatesCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: storeCLIUpdatesProviderID, Name: "Store CLI aggregate updates", Backend: backendStoreCLI}
}

func (provider storeCLIUpdatesCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	now := provider.now()
	identity := provider.Identity()
	run := StoreCatalogProviderRun{
		Provider:    identity,
		Version:     provider.Version,
		StartedAt:   now,
		CompletedAt: now,
		Health:      StoreProviderHealthy,
	}
	if !packageActionManagerAvailable(managerStore) {
		run.Health = StoreProviderUnsupported
		run.Error = "Store CLI is unavailable"
		return run
	}
	productFamilies := make([]StorePackagedAppFamily, 0, len(families))
	byPFN := map[string]StorePackagedAppFamily{}
	for _, family := range families {
		if family.ProductLike && family.Identity.Resolved() && family.Identity.UserSID == scan.UserSID {
			productFamilies = append(productFamilies, family)
			byPFN[strings.ToLower(family.Identity.PackageFamilyName)] = family
		}
	}
	if len(productFamilies) == 0 {
		run.CompletedAt = provider.now()
		return run
	}

	result := provider.run(ctx, storeCLIUpdatesProviderTimeout, storeUpdatesCommand()...)
	run.CompletedAt = provider.now()
	parsed, parseErr := parseStoreCLIUpdatesOutput(result.Stdout + "\n" + result.Stderr)
	if parseErr != nil && len(parsed.Offers) == 0 {
		run.Health = StoreProviderIncomplete
		run.Error = sanitizeProviderDiagnostic(firstNonEmpty(showErrString(parseErr), result.Stderr, result.Stdout))
		return run
	}
	coverageComplete := result.OK && parsed.CompleteCoverage && parseErr == nil && ctx.Err() == nil
	if parsed.NoUpdates && coverageComplete {
		if !result.OK {
			run.Health = StoreProviderIncomplete
			run.Error = sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, "Store CLI aggregate update check failed"))
			return run
		}
		observedAt := provider.now()
		for _, family := range productFamilies {
			run.Observations = append(run.Observations, StoreProviderObservation{
				Provider:         identity,
				Health:           StoreProviderHealthy,
				Kind:             StoreObservationAuthoritativeNegative,
				Identity:         family.Identity,
				ScanID:           scan.ScanID,
				ObservedAt:       observedAt,
				InstalledVersion: family.Primary.Version.String(),
				Diagnostics:      "Store CLI aggregate update check reported no updates found.",
			})
		}
		return run
	}

	matchedPFNs := map[string]bool{}
	var unmatched []string
	for _, offer := range parsed.Offers {
		family, found := byPFN[strings.ToLower(offer.PFN)]
		if !found {
			unmatched = append(unmatched, firstNonEmpty(offer.PFN, offer.ProductID))
			continue
		}
		matchedPFNs[strings.ToLower(family.Identity.PackageFamilyName)] = true
		observedAt := provider.now()
		target := &ExactStoreUpdateTarget{
			Identity:   family.Identity,
			Provider:   identity,
			ProductID:  offer.ProductID,
			UpdateID:   family.Identity.PackageFamilyName,
			Verified:   true,
			VerifiedBy: identity.Key(),
			VerifiedAt: observedAt,
		}
		kind := StoreObservationPositiveUpdateOffer
		if offer.Inapplicable {
			kind = StoreObservationNewerCatalogNoApplicableInstaller
			target = nil
		}
		run.Observations = append(run.Observations, StoreProviderObservation{
			Provider:         identity,
			Health:           StoreProviderHealthy,
			Kind:             kind,
			Identity:         family.Identity,
			ScanID:           scan.ScanID,
			ObservedAt:       observedAt,
			InstalledVersion: family.Primary.Version.String(),
			AvailableVersion: offer.AvailableVersion,
			CatalogVersion:   offer.AvailableVersion,
			Target:           target,
			Diagnostics:      storeCLIUpdatesOfferDiagnostics(offer),
		})
		run.Mappings = append(run.Mappings, VerifiedStoreIdentityMapping{
			InstalledIdentity: family.Identity,
			ProductID:         offer.ProductID,
			Provider:          identity,
			ScanID:            scan.ScanID,
			VerifiedAt:        observedAt,
			Evidence:          "store updates --apply false returned matching PFN and Product ID.",
		})
	}
	if len(unmatched) > 0 {
		run.Health = StoreProviderIncomplete
		run.Error = fmt.Sprintf("ignored %d Store CLI aggregate update row(s) without matching installed PFN", len(unmatched))
		return run
	}
	if !coverageComplete {
		run.Health = StoreProviderIncomplete
		run.Error = sanitizeProviderDiagnostic(firstNonEmpty(showErrString(parseErr), parsed.IncompleteReasonText, result.Stderr, result.Stdout, "Store CLI aggregate update coverage was incomplete"))
		return run
	}
	observedAt := provider.now()
	for _, family := range productFamilies {
		if matchedPFNs[strings.ToLower(family.Identity.PackageFamilyName)] {
			continue
		}
		run.Observations = append(run.Observations, StoreProviderObservation{
			Provider:         identity,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationAuthoritativeNegative,
			Identity:         family.Identity,
			ScanID:           scan.ScanID,
			ObservedAt:       observedAt,
			InstalledVersion: family.Primary.Version.String(),
			Diagnostics:      "Store CLI aggregate update output did not list this PFN in a successful exact update listing.",
		})
	}
	return run
}

func (provider storeCLIExactCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	now := provider.now()
	identity := provider.Identity()
	run := StoreCatalogProviderRun{
		Provider:    identity,
		Version:     provider.Version,
		StartedAt:   now,
		CompletedAt: now,
		Health:      StoreProviderHealthy,
	}
	if !packageActionManagerAvailable(managerStore) {
		run.Health = StoreProviderUnsupported
		run.Error = "Store CLI is unavailable"
		return run
	}
	productFamilies := make([]StorePackagedAppFamily, 0, len(families))
	for _, family := range families {
		if family.ProductLike {
			productFamilies = append(productFamilies, family)
		}
	}
	sort.Slice(productFamilies, func(i, j int) bool {
		return strings.ToLower(productFamilies[i].Identity.PackageFamilyName) < strings.ToLower(productFamilies[j].Identity.PackageFamilyName)
	})
	if len(productFamilies) == 0 {
		run.CompletedAt = provider.now()
		return run
	}

	concurrency := provider.Concurrency
	if concurrency <= 0 {
		concurrency = storeCLIExactProviderConcurrency
	}
	if concurrency > len(productFamilies) {
		concurrency = len(productFamilies)
	}
	jobs := make(chan StorePackagedAppFamily)
	results := make(chan storeCLIExactFamilyResult, len(productFamilies))
	scheduled := map[string]StorePackagedAppFamily{}
	completed := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for family := range jobs {
				results <- provider.observeFamily(ctx, scan, identity, family)
			}
		}()
	}
	cancelled := false
	for _, family := range productFamilies {
		select {
		case <-ctx.Done():
			cancelled = true
			goto closeJobs
		case jobs <- family:
			scheduled[strings.ToLower(family.Identity.PackageFamilyName)] = family
		}
	}
closeJobs:
	close(jobs)
	wg.Wait()
	close(results)

	seen := 0
	for result := range results {
		seen++
		if result.PFN != "" {
			completed[strings.ToLower(result.PFN)] = true
		}
		if result.Observation.Identity.Resolved() {
			run.Observations = append(run.Observations, result.Observation)
		}
		if result.Mapping != nil {
			run.Mappings = append(run.Mappings, *result.Mapping)
		}
	}
	if seen != len(productFamilies) {
		run.Health = StoreProviderIncomplete
		run.Error = "Store CLI exact provider did not return evidence for every product-like package family"
	}
	if cancelled || ctx.Err() != nil {
		run.Health = StoreProviderIncomplete
	}
	var unresolved []string
	for _, family := range productFamilies {
		pfn := family.Identity.PackageFamilyName
		key := strings.ToLower(pfn)
		if completed[key] {
			continue
		}
		unresolved = append(unresolved, pfn)
		diagnostics := "Store CLI exact provider did not inspect this PFN before cancellation or timeout"
		if _, ok := scheduled[key]; ok {
			diagnostics = "Store CLI exact provider scheduled this PFN but did not complete before cancellation or timeout"
		}
		run.Observations = append(run.Observations, provider.incompleteObservation(scan, identity, family, diagnostics))
	}
	if run.Health != StoreProviderHealthy && run.Error == "" {
		run.Error = fmt.Sprintf("Store CLI exact provider completed %d/%d PFN(s); %d unresolved", len(completed), len(productFamilies), len(unresolved))
		if ctx.Err() != nil {
			run.Error += ": " + ctx.Err().Error()
		}
	}
	sortStoreCatalogRunEvidence(run.Observations, run.Mappings)
	run.CompletedAt = provider.now()
	return run
}

type storeCLIExactFamilyResult struct {
	Observation StoreProviderObservation
	Mapping     *VerifiedStoreIdentityMapping
	PFN         string
}

func (provider storeCLIExactCatalogProvider) incompleteObservation(scan StoreScanGeneration, providerID StoreProviderIdentity, family StorePackagedAppFamily, diagnostics string) StoreProviderObservation {
	observedAt := provider.now()
	if observedAt.IsZero() {
		observedAt = scan.CompletedAt
	}
	return StoreProviderObservation{
		Provider:         providerID,
		Health:           StoreProviderIncomplete,
		Kind:             StoreObservationIncompleteResult,
		Identity:         family.Identity,
		ScanID:           scan.ScanID,
		ObservedAt:       observedAt,
		InstalledVersion: family.Primary.Version.String(),
		Diagnostics:      diagnostics,
	}
}

func sortStoreCatalogRunEvidence(observations []StoreProviderObservation, mappings []VerifiedStoreIdentityMapping) {
	sort.SliceStable(observations, func(i, j int) bool {
		left := strings.ToLower(observations[i].Identity.PackageFamilyName)
		right := strings.ToLower(observations[j].Identity.PackageFamilyName)
		if left != right {
			return left < right
		}
		return observations[i].Kind < observations[j].Kind
	})
	sort.SliceStable(mappings, func(i, j int) bool {
		left := strings.ToLower(mappings[i].InstalledIdentity.PackageFamilyName)
		right := strings.ToLower(mappings[j].InstalledIdentity.PackageFamilyName)
		if left != right {
			return left < right
		}
		return strings.ToLower(mappings[i].ProductID) < strings.ToLower(mappings[j].ProductID)
	})
}

func (provider storeCLIExactCatalogProvider) observeFamily(ctx context.Context, scan StoreScanGeneration, providerID StoreProviderIdentity, family StorePackagedAppFamily) storeCLIExactFamilyResult {
	identity := family.Identity
	observedAt := provider.now()
	base := StoreProviderObservation{
		Provider:         providerID,
		Health:           StoreProviderHealthy,
		Identity:         identity,
		ScanID:           scan.ScanID,
		ObservedAt:       observedAt,
		InstalledVersion: family.Primary.Version.String(),
	}
	if !identity.Resolved() || identity.UserSID != scan.UserSID {
		base.Health = StoreProviderFailed
		base.Kind = StoreObservationProviderFailure
		base.Diagnostics = "Store CLI exact provider received unresolved or wrong-user package identity"
		return storeCLIExactFamilyResult{Observation: base, PFN: identity.PackageFamilyName}
	}

	show := provider.run(ctx, storeCLIExactProviderTimeout, managerCommand(managerStore, "show", identity.PackageFamilyName)...)
	metadata, showErr := parseStoreCLIShowMetadata(show.Stdout + "\n" + show.Stderr)
	if showErr != nil || !show.OK {
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = firstNonEmpty(showErrString(showErr), show.Stderr, show.Stdout)
		return storeCLIExactFamilyResult{Observation: base, PFN: identity.PackageFamilyName}
	}
	if !strings.EqualFold(metadata.PFN, identity.PackageFamilyName) || metadata.ProductID == "" {
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = fmt.Sprintf("Store CLI show did not return an exact PFN/Product ID mapping for %s", identity.PackageFamilyName)
		return storeCLIExactFamilyResult{Observation: base, PFN: identity.PackageFamilyName}
	}

	mapping := VerifiedStoreIdentityMapping{
		InstalledIdentity: identity,
		ProductID:         metadata.ProductID,
		Provider:          providerID,
		ScanID:            scan.ScanID,
		VerifiedAt:        observedAt,
		Evidence:          "store show <package-family-name> returned matching PFN and Product ID",
	}
	if provider.StateCheckPFNs != nil && !provider.StateCheckPFNs[strings.ToLower(identity.PackageFamilyName)] {
		base.Mapping = &mapping
		return storeCLIExactFamilyResult{Mapping: &mapping, PFN: identity.PackageFamilyName}
	}
	update := provider.run(ctx, storeCLIExactProviderTimeout, storeUpdateCommand(identity.PackageFamilyName, false)...)
	state, updateErr := parseStoreCLIUpdateCheckResult(ctx, update.Stdout+"\n"+update.Stderr, update)
	base.Mapping = &mapping
	base.Diagnostics = sanitizeProviderDiagnostic(firstNonEmpty(update.Stderr, update.Stdout))
	switch {
	case updateErr != nil:
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = showErrString(updateErr)
	case state == StoreObservationPositiveUpdateOffer:
		base.Kind = StoreObservationPositiveUpdateOffer
		base.Target = &ExactStoreUpdateTarget{
			Identity:   identity,
			Provider:   providerID,
			ProductID:  metadata.ProductID,
			UpdateID:   identity.PackageFamilyName,
			Verified:   true,
			VerifiedBy: providerID.Key(),
			VerifiedAt: observedAt,
		}
	case state == StoreObservationAuthoritativeNegative:
		base.Kind = StoreObservationAuthoritativeNegative
	case state == StoreObservationNewerCatalogNoApplicableInstaller:
		base.Kind = StoreObservationNewerCatalogNoApplicableInstaller
		base.CatalogVersion = "newer"
	default:
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = "Store CLI update check returned no authoritative update state"
	}
	return storeCLIExactFamilyResult{Observation: base, Mapping: &mapping, PFN: identity.PackageFamilyName}
}

func (provider storeCLIExactCatalogProvider) run(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
	if provider.Run != nil {
		return provider.Run(ctx, timeout, args...)
	}
	return runCommandContext(ctx, timeout, args...)
}

func (provider storeCLIUpdatesCatalogProvider) run(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
	if provider.Run != nil {
		return provider.Run(ctx, timeout, args...)
	}
	return runCommandContext(ctx, timeout, args...)
}

func (provider storeCLIExactCatalogProvider) now() time.Time {
	if provider.Now != nil {
		return provider.Now().UTC()
	}
	return time.Now().UTC()
}

func (provider storeCLIUpdatesCatalogProvider) now() time.Time {
	if provider.Now != nil {
		return provider.Now().UTC()
	}
	return time.Now().UTC()
}

func parseStoreCLIShowMetadata(output string) (storeCLIProductMetadata, error) {
	var metadata storeCLIProductMetadata
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			metadata.Name = value
		case "product id":
			metadata.ProductID = value
		case "pfn":
			metadata.PFN = value
		}
	}
	if metadata.PFN == "" {
		return metadata, fmt.Errorf("Store CLI show output did not include PFN")
	}
	if metadata.ProductID == "" {
		return metadata, fmt.Errorf("Store CLI show output did not include Product ID")
	}
	return metadata, nil
}

func parseStoreCLIUpdateCheck(output string) (StoreObservationKind, error) {
	return parseStoreCLIUpdateCheckResult(context.Background(), output, CommandResult{OK: true, Stdout: output})
}

type storeCLIUpdateCheckClassification struct {
	meaningful            bool
	positive              bool
	inapplicable          bool
	negative              bool
	expectedPromptFailure string
	failureLines          []string
}

func parseStoreCLIUpdateCheckResult(ctx context.Context, output string, command CommandResult) (StoreObservationKind, error) {
	classified, classifyErr := classifyStoreCLIUpdateCheckOutput(output)
	if classifyErr != nil {
		return StoreObservationIncompleteResult, classifyErr
	}
	if ctx.Err() != nil {
		return StoreObservationIncompleteResult, ctx.Err()
	}
	if command.Code == 124 {
		return StoreObservationIncompleteResult, errors.New("Store CLI update check timed out")
	}
	if command.Code == commandCancelledCode {
		return StoreObservationIncompleteResult, errors.New("Store CLI update check was cancelled")
	}
	if len(classified.failureLines) > 0 {
		return StoreObservationIncompleteResult, errors.New(strings.Join(classified.failureLines, "; "))
	}
	if !storeCLICommandResultSuccessful(command) {
		if storeCLIAllowsNoninteractivePromptPositive(command, classified) {
			return StoreObservationPositiveUpdateOffer, nil
		}
		return StoreObservationIncompleteResult, errors.New(firstNonEmpty(command.Stderr, command.Stdout, classified.expectedPromptFailure, "Store CLI update check failed"))
	}
	if classified.positive {
		return StoreObservationPositiveUpdateOffer, nil
	}
	if classified.inapplicable {
		if classified.expectedPromptFailure != "" {
			return StoreObservationIncompleteResult, errors.New(classified.expectedPromptFailure)
		}
		return StoreObservationNewerCatalogNoApplicableInstaller, nil
	}
	if classified.negative {
		if classified.expectedPromptFailure != "" {
			return StoreObservationIncompleteResult, errors.New(classified.expectedPromptFailure)
		}
		return StoreObservationAuthoritativeNegative, nil
	}
	if classified.expectedPromptFailure != "" {
		return StoreObservationIncompleteResult, errors.New(classified.expectedPromptFailure)
	}
	if !classified.meaningful {
		return StoreObservationEmptyResult, errors.New("Store CLI update check returned empty or non-authoritative output")
	}
	return StoreObservationIncompleteResult, errors.New("Store CLI update check returned unrecognized output")
}

func classifyStoreCLIUpdateCheckOutput(output string) (storeCLIUpdateCheckClassification, error) {
	var classified storeCLIUpdateCheckClassification
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case storeCLIExpectedNoninteractivePromptFailureLine(lower):
			classified.expectedPromptFailure = line
			continue
		case storeCLIOutputFailureLine(lower):
			classified.failureLines = append(classified.failureLines, line)
			continue
		case storeCLIInapplicableLine(line) || storeCLINoApplicableUpdateLine(lower):
			classified.inapplicable = true
			continue
		case storeCLIUpdateNegativeLine(lower):
			classified.negative = true
			continue
		case storeCLIUpdatePromptIndicatesOffer(lower):
			if count := storeCLIUpdatePromptOfferCount(lower); count > 1 {
				return classified, fmt.Errorf("Store CLI update check mentioned %d update(s) without exact target-specific evidence", count)
			}
			classified.positive = true
			continue
		case storeCLIUpdatePositiveLine(lower):
			classified.positive = true
			continue
		}
		if isStoreOutputNoiseLine(line) {
			continue
		}
		classified.meaningful = true
	}
	return classified, nil
}

func storeCLIExpectedNoninteractivePromptFailureLine(lower string) bool {
	return strings.Contains(lower, "failed to read input in non-interactive mode")
}

func storeCLICommandResultSuccessful(command CommandResult) bool {
	return command.OK && command.Code == 0
}

func storeCLIAllowsNoninteractivePromptPositive(command CommandResult, classified storeCLIUpdateCheckClassification) bool {
	return classified.positive &&
		!classified.negative &&
		!classified.inapplicable &&
		classified.expectedPromptFailure != "" &&
		len(classified.failureLines) == 0 &&
		storeCLICommandTargetsExactPFN(command.Command)
}

func storeCLICommandTargetsExactPFN(command string) bool {
	fields := strings.Fields(command)
	for index, field := range fields {
		if !strings.EqualFold(strings.Trim(field, "\"'"), "update") {
			continue
		}
		for _, candidate := range fields[index+1:] {
			candidate = strings.Trim(candidate, "\"'")
			if candidate == "" || strings.HasPrefix(candidate, "-") {
				continue
			}
			return packageFamilyNameFromWingetValue(candidate) != ""
		}
	}
	return false
}

func storeCLINoApplicableUpdateLine(lower string) bool {
	return strings.Contains(lower, "no applicable update available")
}

func storeCLIUpdateNegativeLine(lower string) bool {
	return strings.Contains(lower, "already up to date") ||
		strings.Contains(lower, "no update available") ||
		strings.Contains(lower, "no updates available") ||
		strings.Contains(lower, "no available update") ||
		strings.Contains(lower, "no updates found") ||
		strings.Contains(lower, "no update found")
}

func storeCLIUpdatePositiveLine(lower string) bool {
	return strings.Contains(lower, "update available") ||
		strings.Contains(lower, "updates available")
}

func storeCLIUpdatePromptIndicatesOffer(lower string) bool {
	if !strings.Contains(lower, "update") {
		return false
	}
	if strings.Contains(lower, "would you like") && (strings.Contains(lower, "apply") || strings.Contains(lower, "install")) {
		return true
	}
	if strings.Contains(lower, "mochten sie") && (strings.Contains(lower, "anwenden") || strings.Contains(lower, "installieren")) {
		return true
	}
	if strings.Contains(lower, "möchten sie") && (strings.Contains(lower, "anwenden") || strings.Contains(lower, "installieren")) {
		return true
	}
	return false
}

func parseStoreCLIUpdatesOutput(output string) (storeCLIUpdatesParseResult, error) {
	var result storeCLIUpdatesParseResult
	meaningful := false
	positiveHint := false
	var current storeCLIUpdatesRecordBuilder
	flush := func(reason string) {
		offer, complete, problems := current.finish()
		if complete {
			result.Offers = append(result.Offers, offer)
		} else if current.hasAny() || len(problems) > 0 {
			if len(problems) == 0 {
				problems = append(problems, "partial Store CLI aggregate record")
			}
			if reason != "" {
				problems[0] = problems[0] + " before " + reason
			}
			result.MalformedRecords = append(result.MalformedRecords, problems...)
		}
		current = storeCLIUpdatesRecordBuilder{}
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			flush("blank line")
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "no updates found"):
			result.NoUpdates = true
			result.ExplicitNoUpdates = true
			continue
		case strings.Contains(lower, "update available") ||
			strings.Contains(lower, "updates available") ||
			strings.Contains(lower, "update(s)"):
			if current.hasAny() {
				flush("new update record")
			}
			positiveHint = true
			if count := storeCLIUpdatePromptOfferCount(lower); count > result.ExpectedOfferCount {
				result.ExpectedOfferCount = count
			}
			continue
		case storeCLIExpectedNoninteractivePromptFailureLine(lower) || storeCLIOutputFailureLine(lower):
			result.FailureDiagnostics = append(result.FailureDiagnostics, line)
			meaningful = true
			continue
		}
		if isStoreOutputNoiseLine(line) {
			continue
		}
		if key, value, ok := storeCLIKeyValue(line); ok {
			var problems []string
			switch strings.ToLower(key) {
			case "product id":
				if current.shouldStartNextProductID(value) {
					flush("next Product ID")
				}
				problems = current.setProductID(value)
			case "pfn", "package family name":
				if current.shouldStartNextPFN(value) {
					flush("next PFN")
				}
				problems = current.setPFN(value)
			case "update id":
				if pfn := packageFamilyNameFromWingetValue(value); pfn != "" {
					if current.shouldStartNextPFN(pfn) {
						flush("next Update ID")
					}
					problems = current.setPFN(pfn)
				}
			case "available version", "new version":
				problems = current.setAvailableVersion(value)
			case "applicability", "installer applicability", "status":
				if storeCLIInapplicableLine(value) {
					current.Inapplicable = true
				}
			}
			result.MalformedRecords = append(result.MalformedRecords, problems...)
			continue
		}
		if storeCLIInapplicableLine(line) && (current.PFN != "" || current.ProductID != "") {
			current.Inapplicable = true
			continue
		}
		result.UnrecognizedLines = append(result.UnrecognizedLines, line)
		meaningful = true
	}
	flush("end of output")

	if result.NoUpdates && (len(result.Offers) > 0 || positiveHint) {
		result.Contradictory = true
	}
	problems := []string{}
	if result.Contradictory {
		problems = append(problems, "Store CLI aggregate update output reported no updates and exact update offers or update hints in the same result")
	}
	if len(result.FailureDiagnostics) > 0 {
		problems = append(problems, "Store CLI aggregate update output contained failure diagnostics: "+strings.Join(result.FailureDiagnostics, "; "))
	}
	if result.ExpectedOfferCount > 0 && len(result.Offers) < result.ExpectedOfferCount {
		problems = append(problems, fmt.Sprintf("Store CLI aggregate update output mentioned %d update(s) but only %d exact PFN/Product ID association(s) were parsed", result.ExpectedOfferCount, len(result.Offers)))
	}
	if len(result.MalformedRecords) > 0 {
		problems = append(problems, strings.Join(result.MalformedRecords, "; "))
	}
	if len(result.UnrecognizedLines) > 0 {
		problems = append(problems, "Store CLI aggregate update output contained unrecognized line(s): "+strings.Join(result.UnrecognizedLines, "; "))
	}
	if len(problems) == 0 {
		switch {
		case result.NoUpdates:
			result.CompleteCoverage = true
			return result, nil
		case len(result.Offers) > 0:
			result.CompleteCoverage = true
			return result, nil
		case !meaningful && !positiveHint:
			result.IncompleteReasonText = "Store CLI aggregate update check returned empty or non-authoritative output"
			return result, errors.New(result.IncompleteReasonText)
		case positiveHint:
			result.IncompleteReasonText = "Store CLI aggregate update output mentioned updates without exact PFN/Product ID associations"
			return result, errors.New(result.IncompleteReasonText)
		default:
			result.IncompleteReasonText = "Store CLI aggregate update output was not recognized as authoritative"
			return result, errors.New(result.IncompleteReasonText)
		}
	}
	if len(result.Offers) > 0 {
		result.IncompleteReasonText = strings.Join(problems, "; ")
		return result, errors.New(result.IncompleteReasonText)
	}
	result.IncompleteReasonText = strings.Join(problems, "; ")
	return result, errors.New(result.IncompleteReasonText)
}

type storeCLIUpdatesRecordBuilder struct {
	storeCLIUpdatesParsedOffer
	problems []string
}

func (builder storeCLIUpdatesRecordBuilder) hasAny() bool {
	return builder.ProductID != "" || builder.PFN != "" || builder.AvailableVersion != "" || builder.Inapplicable || len(builder.problems) > 0
}

func (builder storeCLIUpdatesRecordBuilder) complete() bool {
	return builder.ProductID != "" && builder.PFN != ""
}

func (builder storeCLIUpdatesRecordBuilder) shouldStartNextProductID(value string) bool {
	value = strings.TrimSpace(value)
	return builder.complete() && value != "" && builder.ProductID != "" && !strings.EqualFold(builder.ProductID, value)
}

func (builder storeCLIUpdatesRecordBuilder) shouldStartNextPFN(value string) bool {
	value = strings.TrimSpace(value)
	return builder.complete() && value != "" && builder.PFN != "" && !strings.EqualFold(builder.PFN, value)
}

func (builder *storeCLIUpdatesRecordBuilder) setProductID(value string) []string {
	return builder.setField("Product ID", &builder.ProductID, value)
}

func (builder *storeCLIUpdatesRecordBuilder) setPFN(value string) []string {
	return builder.setField("PFN", &builder.PFN, value)
}

func (builder *storeCLIUpdatesRecordBuilder) setAvailableVersion(value string) []string {
	return builder.setField("Available Version", &builder.AvailableVersion, value)
}

func (builder *storeCLIUpdatesRecordBuilder) setField(label string, target *string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if *target != "" && !strings.EqualFold(*target, value) {
		problem := fmt.Sprintf("conflicting %s values %q and %q", label, *target, value)
		builder.problems = append(builder.problems, problem)
		return []string{problem}
	}
	*target = value
	return nil
}

func (builder storeCLIUpdatesRecordBuilder) finish() (storeCLIUpdatesParsedOffer, bool, []string) {
	problems := append([]string(nil), builder.problems...)
	if !builder.hasAny() {
		return storeCLIUpdatesParsedOffer{}, false, problems
	}
	if builder.ProductID == "" || builder.PFN == "" {
		problems = append(problems, "partial Store CLI aggregate record missing exact Product ID or PFN")
		return storeCLIUpdatesParsedOffer{}, false, problems
	}
	return builder.storeCLIUpdatesParsedOffer, len(problems) == 0, problems
}

func storeCLIOutputFailureLine(lower string) bool {
	lower = strings.TrimSpace(lower)
	if lower == "" ||
		storeCLIExpectedNoninteractivePromptFailureLine(lower) ||
		storeCLIUpdateNegativeLine(lower) ||
		storeCLIInapplicableLine(lower) ||
		storeCLINoApplicableUpdateLine(lower) {
		return false
	}
	return strings.Contains(lower, "access is denied") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "exception") ||
		strings.Contains(lower, "hresult") ||
		strings.Contains(lower, "package not found") ||
		strings.Contains(lower, "product not found") ||
		strings.Contains(lower, "no product found") ||
		strings.Contains(lower, "could not find") ||
		strings.Contains(lower, "error:") ||
		strings.Contains(lower, "failed") ||
		strings.Contains(lower, "failure") ||
		strings.Contains(lower, "fehlgeschlagen") ||
		strings.Contains(lower, "fehler:")
}

func storeCLIUpdatesOfferDiagnostics(offer storeCLIUpdatesParsedOffer) string {
	if offer.Inapplicable {
		return "Store CLI aggregate update output contained an exact PFN/Product ID association with no applicable installer."
	}
	return "Store CLI aggregate update output contained an exact PFN/Product ID association."
}

func storeCLIInapplicableLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(lower, "no applicable installer") ||
		strings.Contains(lower, "not applicable") ||
		strings.Contains(lower, "kein anwendbares installationsprogramm") ||
		strings.Contains(lower, "nicht anwendbar")
}

func storeCLIUpdatePromptOfferCount(line string) int {
	if !storeCLIUpdatePromptIndicatesOffer(line) {
		return 0
	}
	for _, field := range strings.Fields(line) {
		field = strings.Trim(field, " \t\r\n.,:;!?()[]{}")
		if field == "" {
			continue
		}
		value := 0
		for _, r := range field {
			if r < '0' || r > '9' {
				value = 0
				break
			}
			value = value*10 + int(r-'0')
		}
		if value > 0 {
			return value
		}
	}
	return 0
}

func storeCLIKeyValue(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func showErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type storeCLIExactCatalogQueryProvider struct {
	Provider storeCLIExactCatalogProvider
}

func (provider storeCLIExactCatalogQueryProvider) QueryExact(ctx context.Context, request StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	if !request.Identity.Resolved() {
		return StoreExactCatalogResult{}, validationCommandResult("targeted Store catalog query", fmt.Errorf("Store exact catalog query requires installed PFN"))
	}
	runner := provider.Provider
	show := runner.run(ctx, storeCLIExactProviderTimeout, managerCommand(managerStore, "show", request.Identity.PackageFamilyName)...)
	metadata, showErr := parseStoreCLIShowMetadata(show.Stdout + "\n" + show.Stderr)
	if showErr != nil || !show.OK {
		show.OK = false
		if show.Code == 0 {
			show.Code = 2
		}
		show.Stderr = appendDiagnosticLine(show.Stderr, showErrString(showErr))
		return StoreExactCatalogResult{Authoritative: false, Diagnostics: sanitizeProviderDiagnostic(firstNonEmpty(show.Stderr, show.Stdout))}, show
	}
	if !strings.EqualFold(metadata.PFN, request.Identity.PackageFamilyName) {
		result := CommandResult{Command: show.Command, Code: 2, Stderr: fmt.Sprintf("Store CLI show returned PFN %q for requested PFN %q", metadata.PFN, request.Identity.PackageFamilyName)}
		return StoreExactCatalogResult{Authoritative: false, Diagnostics: sanitizeProviderDiagnostic(result.Stderr)}, mergeCommandResults(show, result, "Store CLI exact catalog identity check")
	}
	if request.ProductID != "" && !strings.EqualFold(metadata.ProductID, request.ProductID) {
		result := CommandResult{Command: show.Command, Code: 2, Stderr: fmt.Sprintf("Store CLI show returned Product ID %q for requested Product ID %q", metadata.ProductID, request.ProductID)}
		return StoreExactCatalogResult{Authoritative: false, Diagnostics: sanitizeProviderDiagnostic(result.Stderr)}, mergeCommandResults(show, result, "Store CLI exact catalog identity check")
	}

	update := runner.run(ctx, storeCLIExactProviderTimeout, storeUpdateCommand(request.Identity.PackageFamilyName, false)...)
	state, err := parseStoreCLIUpdateCheckResult(ctx, update.Stdout+"\n"+update.Stderr, update)
	update = mergeCommandResults(show, update, "Store CLI exact catalog state check")
	if err != nil {
		update.OK = false
		if update.Code == 0 {
			update.Code = 2
		}
		update.Stderr = appendDiagnosticLine(update.Stderr, err.Error())
		return StoreExactCatalogResult{Authoritative: false, Diagnostics: sanitizeProviderDiagnostic(firstNonEmpty(update.Stderr, update.Stdout))}, update
	}
	result := StoreExactCatalogResult{
		Authoritative:    state == StoreObservationPositiveUpdateOffer || state == StoreObservationAuthoritativeNegative,
		OfferAvailable:   state == StoreObservationPositiveUpdateOffer,
		InstalledHealthy: true,
		Diagnostics:      sanitizeProviderDiagnostic(firstNonEmpty(update.Stdout, update.Stderr)),
	}
	return result, update
}
