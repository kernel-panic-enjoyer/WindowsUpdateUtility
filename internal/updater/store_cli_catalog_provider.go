package updater

import (
	"context"
	"errors"
	"fmt"
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
	Run         func(context.Context, time.Duration, ...string) CommandResult
	Now         func() time.Time
	Version     string
	Concurrency int
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
	NoUpdates          bool
	Offers             []storeCLIUpdatesParsedOffer
	ExpectedOfferCount int
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
	if parsed.NoUpdates && parseErr == nil {
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
	if !result.OK {
		run.Health = StoreProviderIncomplete
		run.Error = sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, "Store CLI aggregate update check failed"))
		return run
	}
	if parseErr != nil {
		run.Health = StoreProviderIncomplete
		run.Error = sanitizeProviderDiagnostic(parseErr.Error())
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
	for _, family := range productFamilies {
		select {
		case <-ctx.Done():
			run.Health = StoreProviderIncomplete
			run.Error = ctx.Err().Error()
			close(jobs)
			wg.Wait()
			close(results)
			run.CompletedAt = provider.now()
			return run
		case jobs <- family:
		}
	}
	close(jobs)
	wg.Wait()
	close(results)

	seen := 0
	for result := range results {
		seen++
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
	run.CompletedAt = provider.now()
	return run
}

type storeCLIExactFamilyResult struct {
	Observation StoreProviderObservation
	Mapping     *VerifiedStoreIdentityMapping
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
		return storeCLIExactFamilyResult{Observation: base}
	}

	show := provider.run(ctx, storeCLIExactProviderTimeout, managerCommand(managerStore, "show", identity.PackageFamilyName)...)
	metadata, showErr := parseStoreCLIShowMetadata(show.Stdout + "\n" + show.Stderr)
	if showErr != nil || !show.OK {
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = firstNonEmpty(showErrString(showErr), show.Stderr, show.Stdout)
		return storeCLIExactFamilyResult{Observation: base}
	}
	if !strings.EqualFold(metadata.PFN, identity.PackageFamilyName) || metadata.ProductID == "" {
		base.Health = StoreProviderIncomplete
		base.Kind = StoreObservationIncompleteResult
		base.Diagnostics = fmt.Sprintf("Store CLI show did not return an exact PFN/Product ID mapping for %s", identity.PackageFamilyName)
		return storeCLIExactFamilyResult{Observation: base}
	}

	mapping := VerifiedStoreIdentityMapping{
		InstalledIdentity: identity,
		ProductID:         metadata.ProductID,
		Provider:          providerID,
		ScanID:            scan.ScanID,
		VerifiedAt:        observedAt,
		Evidence:          "store show <package-family-name> returned matching PFN and Product ID",
	}
	update := provider.run(ctx, storeCLIExactProviderTimeout, storeUpdateCommand(identity.PackageFamilyName, false)...)
	state, updateErr := parseStoreCLIUpdateCheck(update.Stdout + "\n" + update.Stderr)
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
	return storeCLIExactFamilyResult{Observation: base, Mapping: &mapping}
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
	meaningful := false
	positive := false
	inapplicable := false
	negative := false
	failureLine := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "update available"):
			positive = true
			continue
		case storeCLIUpdatePromptIndicatesOffer(lower):
			if count := storeCLIUpdatePromptOfferCount(lower); count > 1 {
				return StoreObservationIncompleteResult, fmt.Errorf("Store CLI update check mentioned %d update(s) without exact target-specific evidence", count)
			}
			positive = true
			continue
		case strings.Contains(lower, "no applicable installer") ||
			strings.Contains(lower, "not applicable"):
			inapplicable = true
			continue
		case strings.Contains(lower, "already up to date") ||
			strings.Contains(lower, "no update available") ||
			strings.Contains(lower, "no updates found"):
			negative = true
			continue
		case storeCLIOutputFailureLine(lower):
			failureLine = line
			continue
		}
		if isStoreOutputNoiseLine(line) {
			continue
		}
		meaningful = true
	}
	if positive {
		return StoreObservationPositiveUpdateOffer, nil
	}
	if inapplicable {
		return StoreObservationNewerCatalogNoApplicableInstaller, nil
	}
	if negative {
		if failureLine != "" {
			return StoreObservationIncompleteResult, errors.New(failureLine)
		}
		return StoreObservationAuthoritativeNegative, nil
	}
	if failureLine != "" {
		return StoreObservationIncompleteResult, errors.New(failureLine)
	}
	if !meaningful {
		return StoreObservationEmptyResult, errors.New("Store CLI update check returned empty or non-authoritative output")
	}
	return StoreObservationIncompleteResult, errors.New("Store CLI update check returned unrecognized output")
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
	var current storeCLIUpdatesParsedOffer
	meaningful := false
	positiveHint := false
	failureHint := false
	flush := func() {
		if current.PFN != "" && current.ProductID != "" {
			result.Offers = append(result.Offers, current)
		}
		current = storeCLIUpdatesParsedOffer{}
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "no updates found"):
			result.NoUpdates = true
			continue
		case strings.Contains(lower, "update available") ||
			strings.Contains(lower, "updates available") ||
			strings.Contains(lower, "update(s)"):
			positiveHint = true
			if count := storeCLIUpdatePromptOfferCount(lower); count > result.ExpectedOfferCount {
				result.ExpectedOfferCount = count
			}
		case storeCLIOutputFailureLine(lower):
			failureHint = true
			meaningful = true
		}
		if isStoreOutputNoiseLine(line) {
			continue
		}
		if key, value, ok := storeCLIKeyValue(line); ok {
			switch strings.ToLower(key) {
			case "product id":
				if current.PFN != "" || current.ProductID != "" {
					flush()
				}
				current.ProductID = value
			case "pfn", "package family name":
				current.PFN = value
			case "update id":
				if packageFamilyNameFromWingetValue(value) != "" {
					current.PFN = packageFamilyNameFromWingetValue(value)
				}
			case "available version", "new version":
				current.AvailableVersion = value
			case "applicability", "installer applicability", "status":
				if storeCLIInapplicableLine(value) {
					current.Inapplicable = true
				}
			}
			continue
		}
		if storeCLIInapplicableLine(line) && (current.PFN != "" || current.ProductID != "") {
			current.Inapplicable = true
			continue
		}
		meaningful = true
	}
	flush()
	if result.NoUpdates {
		if len(result.Offers) > 0 {
			return result, errors.New("Store CLI aggregate update output reported no updates and exact update offers in the same result")
		}
		if positiveHint {
			return result, errors.New("Store CLI aggregate update output reported no updates and update hints in the same result")
		}
		if failureHint {
			return result, errors.New("Store CLI aggregate update output reported no updates and failure diagnostics in the same result")
		}
		return result, nil
	}
	if result.ExpectedOfferCount > 0 && len(result.Offers) < result.ExpectedOfferCount {
		return result, fmt.Errorf("Store CLI aggregate update output mentioned %d update(s) but only %d exact PFN/Product ID association(s) were parsed", result.ExpectedOfferCount, len(result.Offers))
	}
	if len(result.Offers) > 0 {
		return result, nil
	}
	if !meaningful && !positiveHint {
		return result, errors.New("Store CLI aggregate update check returned empty or non-authoritative output")
	}
	if positiveHint {
		return result, errors.New("Store CLI aggregate update output mentioned updates without exact PFN/Product ID associations")
	}
	return result, errors.New("Store CLI aggregate update output was not recognized as authoritative")
}

func storeCLIOutputFailureLine(lower string) bool {
	return strings.Contains(lower, "could not find") ||
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
	state, err := parseStoreCLIUpdateCheck(update.Stdout + "\n" + update.Stderr)
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
