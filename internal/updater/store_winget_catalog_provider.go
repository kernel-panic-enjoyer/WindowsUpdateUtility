package updater

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	wingetMSStoreExactProviderID      = "winget-msstore-exact"
	wingetMSStoreExactProviderTimeout = 2 * time.Minute
)

type wingetMSStoreExactCatalogProvider struct {
	Run     func(context.Context, time.Duration, ...string) CommandResult
	Now     func() time.Time
	Version string
}

type wingetMSStoreExactCatalogQueryProvider struct {
	Run func(context.Context, time.Duration, ...string) CommandResult
}

func (provider wingetMSStoreExactCatalogProvider) Identity() StoreProviderIdentity {
	return StoreProviderIdentity{ID: wingetMSStoreExactProviderID, Name: "WinGet Microsoft Store exact catalog", Backend: backendWingetMSStoreFallback}
}

func (provider wingetMSStoreExactCatalogProvider) Observe(ctx context.Context, scan StoreScanGeneration, families []StorePackagedAppFamily) StoreCatalogProviderRun {
	now := provider.now()
	identity := provider.Identity()
	run := StoreCatalogProviderRun{
		Provider:    identity,
		Version:     provider.Version,
		StartedAt:   now,
		CompletedAt: now,
		Health:      StoreProviderHealthy,
	}
	if !packageActionManagerAvailable(managerWinget) {
		run.Health = StoreProviderUnsupported
		run.Error = "WinGet is unavailable"
		return run
	}
	result := provider.run(ctx, wingetMSStoreExactProviderTimeout, managerCommand(managerWinget, "upgrade", "--source", sourceMSStore, "--accept-source-agreements", "--disable-interactivity")...)
	run.CompletedAt = provider.now()
	if !result.OK {
		run.Health = StoreProviderFailed
		run.Error = sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout))
		return run
	}

	byPFN := map[string]StorePackagedAppFamily{}
	for _, family := range families {
		if family.ProductLike && family.Identity.Resolved() && family.Identity.UserSID == scan.UserSID {
			byPFN[strings.ToLower(family.Identity.PackageFamilyName)] = family
		}
	}
	var unmatched []string
	for _, pkg := range parseWingetTable(result.Stdout + "\n" + result.Stderr) {
		if strings.TrimSpace(pkg.AvailableVersion) == "" {
			continue
		}
		pfn, productID, ok := exactPFNFromWingetMSStorePackage(pkg)
		if !ok {
			unmatched = append(unmatched, firstNonEmpty(pkg.ID, pkg.Name))
			continue
		}
		family, found := byPFN[strings.ToLower(pfn)]
		if !found {
			unmatched = append(unmatched, pfn)
			continue
		}
		observedAt := provider.now()
		target := &ExactStoreUpdateTarget{
			Identity:   family.Identity,
			Provider:   identity,
			ProductID:  productID,
			UpdateID:   family.Identity.PackageFamilyName,
			Verified:   true,
			VerifiedBy: identity.Key(),
			VerifiedAt: observedAt,
		}
		run.Observations = append(run.Observations, StoreProviderObservation{
			Provider:         identity,
			Health:           StoreProviderHealthy,
			Kind:             StoreObservationPositiveUpdateOffer,
			Identity:         family.Identity,
			ScanID:           scan.ScanID,
			ObservedAt:       observedAt,
			InstalledVersion: family.Primary.Version.String(),
			AvailableVersion: pkg.AvailableVersion,
			Target:           target,
			Diagnostics:      "WinGet msstore update row contained an exact package family name.",
		})
		if productID != "" {
			run.Mappings = append(run.Mappings, VerifiedStoreIdentityMapping{
				InstalledIdentity: family.Identity,
				ProductID:         productID,
				Provider:          identity,
				ScanID:            scan.ScanID,
				VerifiedAt:        observedAt,
				Evidence:          "WinGet msstore update row contained exact PFN and Product ID.",
			})
		}
	}
	if len(unmatched) > 0 {
		run.Health = StoreProviderIncomplete
		run.Error = fmt.Sprintf("ignored %d WinGet msstore update row(s) without exact installed PFN association", len(unmatched))
	}
	return run
}

func (provider wingetMSStoreExactCatalogProvider) run(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
	if provider.Run != nil {
		return provider.Run(ctx, timeout, args...)
	}
	return runCommandContext(ctx, timeout, args...)
}

func (provider wingetMSStoreExactCatalogProvider) now() time.Time {
	if provider.Now != nil {
		return provider.Now().UTC()
	}
	return time.Now().UTC()
}

func (provider wingetMSStoreExactCatalogQueryProvider) QueryExact(ctx context.Context, request StoreExactUpdateRequest) (StoreExactCatalogResult, CommandResult) {
	productID := strings.TrimSpace(request.ProductID)
	if productID == "" {
		return StoreExactCatalogResult{}, validationCommandResult("winget msstore exact catalog query", fmt.Errorf("WinGet msstore exact catalog query requires verified Product ID"))
	}
	if !packageActionManagerAvailable(managerWinget) {
		return StoreExactCatalogResult{}, CommandResult{Command: "winget msstore exact catalog query", Code: 1, Stderr: "WinGet is unavailable"}
	}
	result := provider.run(ctx, wingetMSStoreExactProviderTimeout, wingetMSStoreProductIDUpgradeAvailableCommand(productID)...)
	output := result.Stdout + "\n" + result.Stderr
	for _, pkg := range parseWingetTable(output) {
		if !strings.EqualFold(strings.TrimSpace(pkg.ID), productID) {
			continue
		}
		if pfn, _, ok := exactPFNFromWingetMSStorePackage(pkg); ok && !strings.EqualFold(pfn, request.Identity.PackageFamilyName) {
			return StoreExactCatalogResult{Authoritative: false, Diagnostics: "WinGet msstore exact Product ID query returned a mismatched package family name."}, result
		}
		if strings.TrimSpace(pkg.AvailableVersion) != "" {
			return StoreExactCatalogResult{
				Authoritative:    true,
				OfferAvailable:   true,
				InstalledHealthy: true,
				OfferedVersion:   pkg.AvailableVersion,
				Diagnostics:      "WinGet msstore exact Product ID query returned an available update row.",
			}, result
		}
	}
	if wingetExactProductIDQueryIndicatesNoOffer(output) {
		return StoreExactCatalogResult{
			Authoritative: false,
			Diagnostics:   "WinGet msstore exact Product ID query reported no applicable upgrade but did not return an exact package family association.",
		}, result
	}
	return StoreExactCatalogResult{Authoritative: false, Diagnostics: sanitizeProviderDiagnostic(firstNonEmpty(result.Stderr, result.Stdout, "WinGet msstore exact Product ID query was not authoritative."))}, result
}

func (provider wingetMSStoreExactCatalogQueryProvider) run(ctx context.Context, timeout time.Duration, args ...string) CommandResult {
	if provider.Run != nil {
		return provider.Run(ctx, timeout, args...)
	}
	return runCommandContext(ctx, timeout, args...)
}

func wingetExactProductIDQueryIndicatesNoOffer(output string) bool {
	output = strings.ToLower(output)
	if outputContainsAny(output, []string{
		"no installed package found",
		"no installed package matching",
		"does not match any installed package",
		"no package found",
		"not found",
		"unable to find",
	}) {
		return false
	}
	return outputContainsAny(output, []string{
		"no applicable upgrade found",
		"no available upgrade",
		"keine neueren paketversionen",
		"kein anwendbares upgrade",
	})
}

func exactPFNFromWingetMSStorePackage(pkg Package) (pfn, productID string, ok bool) {
	for _, value := range []string{pkg.Match, pkg.ID} {
		pfn = packageFamilyNameFromWingetValue(value)
		if pfn != "" {
			if looksLikeStoreProductID(pkg.ID) {
				productID = strings.TrimSpace(pkg.ID)
			}
			return pfn, productID, true
		}
	}
	return "", "", false
}

func packageFamilyNameFromWingetValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || isTruncatedID(value) {
		return ""
	}
	if before, after, ok := strings.Cut(value, ":"); ok && strings.EqualFold(strings.TrimSpace(before), "PackageFamilyName") {
		return strings.TrimSpace(after)
	}
	if strings.HasPrefix(strings.ToLower(value), "msix\\") {
		_, fullName, _ := strings.Cut(value, "\\")
		return packageFamilyNameFromPackageFullName(fullName)
	}
	if strings.Contains(value, "__") {
		return packageFamilyNameFromPackageFullName(value)
	}
	if strings.Contains(value, "_") && !strings.Contains(value, "\\") && !strings.Contains(value, " ") {
		return value
	}
	return ""
}

func packageFamilyNameFromPackageFullName(fullName string) string {
	fullName = strings.TrimSpace(fullName)
	if fullName == "" || isTruncatedID(fullName) {
		return ""
	}
	left, publisherID, ok := strings.Cut(fullName, "__")
	if !ok || publisherID == "" {
		return ""
	}
	identity, _, ok := strings.Cut(left, "_")
	if !ok || identity == "" {
		return ""
	}
	return identity + "_" + publisherID
}

func looksLikeStoreProductID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 7 || len(value) > 32 {
		return false
	}
	hasDigit := false
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		return false
	}
	return hasDigit
}
