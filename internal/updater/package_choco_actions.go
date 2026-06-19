package updater

import "context"

func chocoPackageCommand(action, id string) []string {
	return managerCommand(managerChoco, action, id, "-y", "--no-progress", "--no-color")
}

func runChocoUpgradePackageWithFallbackContext(ctx context.Context, pkg Package) CommandResult {
	return runPackageUpdateCandidates(ctx, chocoUpdateTargetCandidates(pkg), "choco target", func(target string) CommandResult {
		return runPackageActionCommand(ctx, managerChoco, packageActionTimeout, chocoPackageCommand("upgrade", target)...)
	})
}
