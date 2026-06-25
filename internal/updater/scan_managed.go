package updater

import (
	"context"
	"errors"
	"strings"
)

func readWingetApps() ([]ScannedApp, *CommandResult, error) {
	return readWingetAppsContext(context.Background())
}

func readWingetAppsContext(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
	if !detectManagerContext(ctx, "winget").Available {
		return nil, nil, nil
	}
	packages, result := wingetInstalledContext(ctx)
	apps := []ScannedApp{}
	for _, pkg := range packages {
		if pkg.ID == "" {
			continue
		}
		manager := wingetSourceManager(pkg.Source)
		apps = append(apps, ScannedApp{
			Key:             manager + ":" + strings.ToLower(pkg.ID),
			Name:            pkg.Name,
			Version:         pkg.Version,
			InstallLocation: pkg.ID,
			Source:          manager,
			Manager:         manager,
			PackageID:       pkg.ID,
		})
	}
	return apps, &result, nil
}

func readAppxApps() ([]ScannedApp, *CommandResult, error) {
	return readAppxAppsContext(context.Background())
}

func readAppxAppsContext(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
	inventory, result := collectNativeStorePackagedInventoryContext(ctx)
	if !result.OK && len(inventory.Families) == 0 {
		errText := strings.TrimSpace(result.Stderr + result.Stdout)
		if errText == "" {
			errText = "native Store inventory returned no inventory"
		}
		return nil, &result, errors.New(errText)
	}
	apps := []ScannedApp{}
	for _, family := range inventory.Families {
		if !family.ProductLike || !family.Identity.Resolved() {
			continue
		}
		stableID := family.Identity.PackageFamilyName
		apps = append(apps, ScannedApp{
			Key:             "store:" + strings.ToLower(stableID),
			Name:            firstNonEmpty(family.DisplayName, family.Primary.IdentityName, family.Identity.PackageFamilyName),
			Version:         family.Primary.Version.String(),
			Publisher:       "",
			InstallLocation: family.Primary.InstallLocation,
			Source:          "store",
			Manager:         "store",
			PackageID:       stableID,
		})
	}
	return apps, &result, nil
}

func stableAppxScanID(pkg Package) string {
	if stableID := stableStoreScanIdentity(pkg.Match); stableID != "" {
		return stableID
	}
	if stableID := stableStoreScanIdentity(pkg.ID); stableID != "" {
		return stableID
	}
	return ""
}

func isStoreScannedApp(app ScannedApp) bool {
	source := strings.ToLower(strings.TrimSpace(app.Source))
	manager := strings.ToLower(strings.TrimSpace(app.Manager))
	return source == "store" || source == "msstore" || source == "appx" || manager == "store"
}

func splitScannedManagedApps(apps []ScannedApp) ([]ScannedApp, []ScannedApp) {
	var wingetApps []ScannedApp
	var storeApps []ScannedApp
	for _, app := range apps {
		if isStoreScannedApp(app) {
			app.Source = "store"
			app.Manager = "store"
			storeApps = append(storeApps, app)
			continue
		}
		if app.Source == "" {
			app.Source = "winget"
		}
		if app.Manager == "" {
			app.Manager = "winget"
		}
		wingetApps = append(wingetApps, app)
	}
	return wingetApps, storeApps
}

func mergeScannedManagedApps(wingetApps, appxApps []ScannedApp) []ScannedApp {
	managed := make([]ScannedApp, 0, len(wingetApps)+len(appxApps))
	seen := map[string]bool{}
	markSeen := func(app ScannedApp) {
		for _, value := range []string{app.Key, app.Name, app.PackageID} {
			normalized := normalizePackageIdentity(value)
			if normalized != "" {
				seen[normalized] = true
			}
		}
	}
	for _, app := range wingetApps {
		managed = append(managed, app)
		markSeen(app)
	}
	for _, app := range appxApps {
		if seen[normalizePackageIdentity(app.Key)] || seen[normalizePackageIdentity(app.Name)] || seen[normalizePackageIdentity(app.PackageID)] {
			continue
		}
		managed = append(managed, app)
		markSeen(app)
	}
	return managed
}
