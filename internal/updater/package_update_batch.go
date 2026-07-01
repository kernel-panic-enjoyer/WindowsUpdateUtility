package updater

import "context"

type elevatedPackageUpdateBatchRunnerFunc func(ctx context.Context, packages []Package, reportProgress func(int, Package)) ([]UpdateResult, CommandResult)

var elevatedPackageUpdateBatchRunner elevatedPackageUpdateBatchRunnerFunc = runElevatedPackageUpdateBatch
var elevatedPackageUpdateBatchEligible = defaultElevatedPackageUpdateBatchEligible

func defaultElevatedPackageUpdateBatchEligible(candidate Package) bool {
	if isAdmin() {
		return false
	}
	return candidate.Manager == managerChoco
}

func planElevatedPackageUpdateBatch(packages []Package, isBatchEligible func(Package) bool) ([]Package, []Package) {
	if isBatchEligible == nil {
		isBatchEligible = defaultElevatedPackageUpdateBatchEligible
	}
	batchPackages := make([]Package, 0, len(packages))
	remainingPackages := make([]Package, 0, len(packages))
	for _, candidate := range packages {
		if isBatchEligible(candidate) {
			batchPackages = append(batchPackages, candidate)
		} else {
			remainingPackages = append(remainingPackages, candidate)
		}
	}
	if len(batchPackages) < 2 {
		return nil, append([]Package(nil), packages...)
	}
	return batchPackages, remainingPackages
}
