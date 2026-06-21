package updater

import (
	"fmt"
	"strings"
)

func (app *App) updateJobPackages(packageKeys []string, options UpdateOptions) ([]Package, string, error) {
	app.mu.RLock()
	inventoryPackages := append([]Package(nil), app.inventory.Packages...)
	app.mu.RUnlock()

	byKey := map[string]Package{}
	for _, pkg := range inventoryPackages {
		key := normalizedJobPackageKey(pkg)
		if key != "" {
			pkg.Key = key
			byKey[key] = pkg
		}
	}

	if len(packageKeys) == 0 {
		var packages []Package
		seen := map[string]bool{}
		for _, pkg := range inventoryPackages {
			key := normalizedJobPackageKey(pkg)
			if key == "" || seen[key] || !packageAllowedInBulkUpdate(pkg, options) {
				continue
			}
			pkg.Key = key
			applyUpdateOptions(&pkg, options)
			packages = append(packages, pkg)
			seen[key] = true
		}
		if len(packages) == 0 {
			return nil, updateJobModeAll, errNoUpdateCandidates
		}
		return packages, updateJobModeAll, nil
	}

	var packages []Package
	seen := map[string]bool{}
	for _, key := range packageKeys {
		normalized := normalizeAutoUpdatePackageKey(key)
		if normalized == "" {
			normalized = key
		}
		if seen[normalized] {
			continue
		}
		manager, id, err := splitPackageKey(normalized)
		if err != nil {
			return nil, updateJobModeSelected, err
		}
		pkg, ok := byKey[normalized]
		if !ok {
			pkg = Package{Key: normalized, Manager: manager, ID: id, Name: id, UpdateSupported: true}
		}
		if pkg.UpdateSupported == false {
			return nil, updateJobModeSelected, fmt.Errorf("%s does not support updates", normalized)
		}
		if pkg.UnknownVersion && !options.AllowUnknownVersion {
			return nil, updateJobModeSelected, fmt.Errorf("%s has an unknown installed version and requires an explicit global unknown-version update choice", normalized)
		}
		if pkg.Pinned && !options.AllowPinned {
			return nil, updateJobModeSelected, fmt.Errorf("%s is pinned and requires an explicit global pinned update choice", normalized)
		}
		pkg.Key = normalized
		applyUpdateOptions(&pkg, options)
		packages = append(packages, pkg)
		seen[normalized] = true
	}
	if len(packages) == 0 {
		return nil, updateJobModeSelected, errNoUpdateCandidates
	}
	return packages, updateJobModeSelected, nil
}

func packageAllowedInBulkUpdate(pkg Package, options UpdateOptions) bool {
	return pkg.UpdateAvailable &&
		pkg.UpdateSupported != false &&
		(!pkg.UnknownVersion || options.AllowUnknownVersion) &&
		(!pkg.Pinned || options.AllowPinned)
}

func applyUpdateOptions(pkg *Package, options UpdateOptions) {
	pkg.AllowUnknownVersionUpdate = options.AllowUnknownVersion
	pkg.AllowPinnedUpdate = options.AllowPinned
}

func (app *App) packageForUpdate(manager, id string) Package {
	fallback := Package{
		Key:             packageKey(manager, id),
		Manager:         manager,
		ID:              id,
		Name:            id,
		UpdateSupported: true,
	}
	requestedKey := packageKey(manager, id)
	normalizedRequested := normalizeAutoUpdatePackageKey(requestedKey)

	app.mu.RLock()
	defer app.mu.RUnlock()
	for _, pkg := range app.inventory.Packages {
		if pkg.Manager != manager {
			continue
		}
		key := normalizedJobPackageKey(pkg)
		if key == "" {
			key = packageKey(pkg.Manager, pkg.ID)
		}
		normalizedKey := normalizeAutoUpdatePackageKey(key)
		if strings.EqualFold(pkg.ID, id) ||
			strings.EqualFold(key, requestedKey) ||
			strings.EqualFold(normalizedKey, normalizedRequested) ||
			(manager == managerStore && equivalentPackageKeys(key, requestedKey)) {
			pkg.Key = key
			if pkg.Name == "" {
				pkg.Name = id
			}
			return pkg
		}
	}
	return fallback
}

func updateJobPackageKeys(packages []Package) []string {
	keys := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if pkg.Key != "" {
			keys = append(keys, pkg.Key)
		}
	}
	return keys
}

func normalizedJobPackageKey(pkg Package) string {
	if pkg.Key != "" {
		if normalized := normalizeAutoUpdatePackageKey(pkg.Key); normalized != "" {
			return normalized
		}
		return pkg.Key
	}
	if pkg.Manager == "" || pkg.ID == "" {
		return ""
	}
	return packageKey(pkg.Manager, pkg.ID)
}

func updateJobPackageName(pkg Package) string {
	for _, value := range []string{pkg.Name, pkg.ID, pkg.Key} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "package"
}
