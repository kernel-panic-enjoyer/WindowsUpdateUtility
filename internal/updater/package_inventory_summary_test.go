package updater

import "testing"

func TestSortInventoryPackagesPushesAppxInventoryRowsLast(t *testing.T) {
	packages := []Package{
		{
			Name:          "Current-AppX",
			Manager:       managerStore,
			ActionBackend: backendAppXInventory,
			UpdateState:   string(StoreUpdateCurrent),
		},
		{
			Name:          "App-Resolver",
			Manager:       managerStore,
			ActionBackend: backendAppXInventory,
			UpdateState:   string(StoreUpdateUnknown),
		},
		{
			Name:          "chocolatey",
			Manager:       managerChoco,
			ActionBackend: managerChoco,
		},
		{
			Name:          "Codex",
			Manager:       managerStore,
			ActionBackend: backendStoreCLI,
			UpdateState:   string(StoreUpdateCurrent),
		},
		{
			Name:          "GitHub CLI",
			Manager:       managerWinget,
			ActionBackend: managerWinget,
		},
	}

	sortInventoryPackages(packages)

	if packages[0].Name != "chocolatey" || packages[1].Name != "GitHub CLI" || packages[2].Name != "Codex" || packages[3].Name != "App-Resolver" || packages[4].Name != "Current-AppX" {
		t.Fatalf("unexpected package order: %#v", packages)
	}
}

func TestSortInventoryPackagesDoesNotPromoteStaleStoreEvidence(t *testing.T) {
	packages := []Package{
		{
			Name:            "Stale Store Evidence",
			Manager:         managerStore,
			ActionBackend:   backendAppXInventory,
			UpdateState:     string(StoreUpdateAvailable),
			Stale:           true,
			UpdateAvailable: false,
		},
		{
			Name:            "Fresh Store Update",
			Manager:         managerStore,
			ActionBackend:   backendStoreCLI,
			UpdateState:     string(StoreUpdateAvailable),
			UpdateAvailable: true,
		},
		{
			Name:    "Managed Winget Package",
			Manager: managerWinget,
		},
	}

	sortInventoryPackages(packages)

	if packages[0].Name != "Fresh Store Update" || packages[1].Name != "Managed Winget Package" || packages[2].Name != "Stale Store Evidence" {
		t.Fatalf("unexpected package order: %#v", packages)
	}
}
