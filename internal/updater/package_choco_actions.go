package updater

import "context"

func chocoPackageCommand(chocoAction, packageID string) []string {
	return managerCommand(managerChoco, chocoAction, packageID, "-y", "--no-progress", "--no-color")
}

func runChocoUpgradePackageWithFallbackContext(ctx context.Context, pkg Package) CommandResult {
	return runPackageUpdateCandidates(ctx, chocoUpdateTargetCandidates(pkg), "choco target", func(candidatePackageID string) CommandResult {
		return runPackageActionCommand(ctx, managerChoco, packageActionTimeout, chocoPackageCommand("upgrade", candidatePackageID)...)
	})
}
