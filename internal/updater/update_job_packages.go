package updater

import (
	"context"
	"fmt"
	"strings"
)

func (app *App) updateJobPackages(requestedPackageKeys []string, options UpdateOptions) ([]Package, string, error) {
	return app.updateJobPackagesContext(context.Background(), requestedPackageKeys, options)
}

func (app *App) updateJobPackagesContext(ctx context.Context, requestedPackageKeys []string, options UpdateOptions) ([]Package, string, error) {
	inventory, err := app.effectiveInventorySnapshot(ctx)
	if err != nil {
		return nil, updateJobModeSelected, err
	}
	inventoryPackages := inventory.Packages

	inventoryByUpdateKey := map[string]Package{}
	for _, pkg := range inventoryPackages {
		updateKey := normalizedJobPackageKey(pkg)
		if updateKey != "" {
			pkg.Key = updateKey
			inventoryByUpdateKey[updateKey] = pkg
		}
	}

	if len(requestedPackageKeys) == 0 {
		var selectedPackages []Package
		selectedKeys := map[string]bool{}
		for _, pkg := range inventoryPackages {
			updateKey := normalizedJobPackageKey(pkg)
			if updateKey == "" || selectedKeys[updateKey] || !packageAllowedInBulkUpdate(pkg, options) {
				continue
			}
			pkg.Key = updateKey
			applyPackageUpdateOptions(&pkg, options)
			selectedPackages = append(selectedPackages, pkg)
			selectedKeys[updateKey] = true
		}
		if len(selectedPackages) == 0 {
			return nil, updateJobModeAll, errNoUpdateCandidates
		}
		return selectedPackages, updateJobModeAll, nil
	}

	var selectedPackages []Package
	selectedKeys := map[string]bool{}
	for _, requestedKey := range requestedPackageKeys {
		updateKey := normalizeJobRequestPackageKey(requestedKey)
		if updateKey == "" {
			updateKey = requestedKey
		}
		if selectedKeys[updateKey] {
			continue
		}
		requestedManager, requestedID, err := splitPackageKey(updateKey)
		if err != nil {
			return nil, updateJobModeSelected, err
		}
		pkg, ok := inventoryByUpdateKey[updateKey]
		if !ok {
			pkg = Package{Key: updateKey, Manager: requestedManager, ID: requestedID, Name: requestedID, UpdateSupported: true}
		}
		policy := packageUpdatePolicy(pkg, options)
		if !policy.CanUpdateNow {
			return nil, updateJobModeSelected, fmt.Errorf(
				"%s cannot be updated now: %s",
				updateKey,
				firstNonEmpty(policy.CannotUpdateReason, "not actionable"),
			)
		}
		pkg.Key = updateKey
		applyPackageUpdateOptions(&pkg, options)
		selectedPackages = append(selectedPackages, pkg)
		selectedKeys[updateKey] = true
	}
	if len(selectedPackages) == 0 {
		return nil, updateJobModeSelected, errNoUpdateCandidates
	}
	return selectedPackages, updateJobModeSelected, nil
}

func packageAllowedInBulkUpdate(pkg Package, options UpdateOptions) bool {
	return packageUpdatePolicy(pkg, options).CanUpdateNow
}

func packageHasFreshStoreAvailableAssessment(pkg Package) bool {
	if pkg.Manager != managerStore {
		return true
	}
	updateState := strings.TrimSpace(pkg.UpdateState)
	if updateState == "" {
		return false
	}
	return strings.EqualFold(updateState, string(StoreUpdateAvailable)) && !pkg.Stale
}

func applyPackageUpdateOptions(pkg *Package, options UpdateOptions) {
	pkg.AllowUnknownVersionUpdate = options.AllowUnknownVersion
	pkg.AllowPinnedUpdate = options.AllowPinned
}

func (app *App) packageForUpdate(manager, id string) Package {
	return app.packageForUpdateContext(context.Background(), manager, id)
}

func (app *App) packageForUpdateContext(ctx context.Context, manager, id string) Package {
	requestedKey := packageKey(manager, id)
	fallbackPackage := Package{
		Key:             requestedKey,
		Manager:         manager,
		ID:              id,
		Name:            id,
		UpdateSupported: true,
	}
	normalizedRequestedKey := normalizeAutoUpdatePackageKey(requestedKey)

	inventory, err := app.effectiveInventorySnapshot(ctx)
	if err != nil {
		return fallbackPackage
	}
	for _, pkg := range inventory.Packages {
		if pkg.Manager != manager {
			continue
		}
		updateKey := normalizedJobPackageKey(pkg)
		if updateKey == "" {
			updateKey = packageKey(pkg.Manager, pkg.ID)
		}
		normalizedUpdateKey := normalizeAutoUpdatePackageKey(updateKey)
		if strings.EqualFold(pkg.ID, id) ||
			strings.EqualFold(updateKey, requestedKey) ||
			strings.EqualFold(normalizedUpdateKey, normalizedRequestedKey) ||
			(manager == managerStore && equivalentPackageKeys(updateKey, requestedKey)) {
			pkg.Key = updateKey
			if pkg.Name == "" {
				pkg.Name = id
			}
			return pkg
		}
	}
	return fallbackPackage
}

func updateJobPackageKeys(packages []Package) []string {
	updateKeys := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Key != "" {
			updateKeys = append(updateKeys, pkg.Key)
		}
	}
	return updateKeys
}

func normalizedJobPackageKey(pkg Package) string {
	if pkg.Manager == managerStore {
		return storePackagePublicKey(pkg)
	}
	if pkg.Key != "" {
		if normalizedKey := normalizeAutoUpdatePackageKey(pkg.Key); normalizedKey != "" {
			return normalizedKey
		}
		return pkg.Key
	}
	if pkg.Manager == "" || pkg.ID == "" {
		return ""
	}
	return packageKey(pkg.Manager, pkg.ID)
}

func normalizeJobRequestPackageKey(requestedKey string) string {
	manager, id, err := splitPackageKey(requestedKey)
	if err != nil {
		return requestedKey
	}
	if manager == managerStore {
		if _, packageFamilyName, ok := splitCanonicalStoreAutoUpdateKey(requestedKey); ok {
			return packageKey(managerStore, packageFamilyName)
		}
		return packageKey(managerStore, id)
	}
	if normalizedKey := normalizeAutoUpdatePackageKey(requestedKey); normalizedKey != "" {
		return normalizedKey
	}
	return requestedKey
}

func updateJobPackageName(pkg Package) string {
	for _, candidateName := range []string{pkg.Name, pkg.ID, pkg.Key} {
		candidateName = strings.TrimSpace(candidateName)
		if candidateName != "" {
			return candidateName
		}
	}
	return "package"
}
