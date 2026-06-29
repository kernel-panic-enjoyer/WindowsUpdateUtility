package updater

import "context"

type elevatedPackageUpdateBatchRunnerFunc func(context.Context, []Package, func(int, Package)) ([]UpdateResult, CommandResult)

var elevatedPackageUpdateBatchRunner elevatedPackageUpdateBatchRunnerFunc = runElevatedPackageUpdateBatch
var elevatedPackageUpdateBatchEligible = defaultElevatedPackageUpdateBatchEligible

func defaultElevatedPackageUpdateBatchEligible(pkg Package) bool {
	if isAdmin() {
		return false
	}
	switch pkg.Manager {
	case managerChoco:
		return true
	case managerWinget:
		return currentUserCanElevateSameUser()
	default:
		return false
	}
}

func planElevatedPackageUpdateBatch(packages []Package, eligible func(Package) bool) ([]Package, []Package) {
	if eligible == nil {
		eligible = defaultElevatedPackageUpdateBatchEligible
	}
	batch := make([]Package, 0, len(packages))
	remaining := make([]Package, 0, len(packages))
	for _, pkg := range packages {
		if eligible(pkg) {
			batch = append(batch, pkg)
			continue
		}
		remaining = append(remaining, pkg)
	}
	if len(batch) < 2 {
		return nil, append([]Package(nil), packages...)
	}
	return batch, remaining
}
