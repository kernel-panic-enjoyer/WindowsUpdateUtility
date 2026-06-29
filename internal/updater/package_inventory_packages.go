package updater

import "strings"

func packagesFromManagerInventory(state State, inventory managerInventory) []Package {
	packages := make([]Package, 0, len(inventory.installed))
	for _, pkg := range inventory.installed {
		adapted, ok := packageFromManagerInventory(state, inventory, pkg)
		if ok {
			packages = append(packages, adapted)
		}
	}
	return packages
}

func packageFromManagerInventory(state State, inventory managerInventory, pkg Package) (Package, bool) {
	effectiveManager := inventory.manager
	if inventory.manager == managerWinget {
		effectiveManager = wingetSourceManager(pkg.Source)
	}
	if effectiveManager == managerStore {
		return Package{}, false
	}
	available := inventory.updates[packageKey(effectiveManager, strings.ToLower(pkg.ID))]
	updateDetail := inventory.updateDetails[packageKey(effectiveManager, strings.ToLower(pkg.ID))]
	if available == "" && inventory.manager == managerWinget {
		available = pkg.AvailableVersion
	}
	pkg.Key = packageKey(effectiveManager, pkg.ID)
	pkg.Manager = effectiveManager
	pkg.AvailableVersion = available
	pkg.UpdateAvailable = available != ""
	pkg.UpdateSupported = true
	pkg.Installed = true
	pkg.UnknownVersion = pkg.UnknownVersion || isUnknownPackageVersion(pkg.Version)
	pkg.Pinned = pkg.Pinned || updateDetail.Pinned
	pkg.AutoUpdate = packageAutoUpdateEnabled(state, pkg)
	if pkg.ActionBackend == "" {
		pkg.ActionBackend = effectiveManager
	}
	return pkg, true
}
