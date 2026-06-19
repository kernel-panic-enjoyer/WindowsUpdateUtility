package updater

import (
	"os"
	"testing"
)

func TestIntegrationInventoryAndScan(t *testing.T) {
	if os.Getenv("UPDATER_INTEGRATION") != "1" {
		t.Skip("set UPDATER_INTEGRATION=1 to run real winget/choco integration test")
	}
	inventory := getInventory()
	if !inventory.Managers["winget"].Available {
		t.Fatalf("winget unavailable: %#v", inventory.Managers["winget"])
	}
	if !inventory.Managers["choco"].Available {
		t.Fatalf("choco unavailable: %#v", inventory.Managers["choco"])
	}
	var wingetCount, chocoCount, updateCount int
	for _, pkg := range inventory.Packages {
		switch pkg.Manager {
		case "winget":
			wingetCount++
			if isTruncatedID(pkg.ID) {
				t.Fatalf("inventory contained truncated winget id: %#v", pkg)
			}
		case "choco":
			chocoCount++
		}
		if pkg.UpdateAvailable {
			updateCount++
		}
	}
	if wingetCount == 0 || chocoCount == 0 {
		t.Fatalf("expected both managers to list packages, winget=%d choco=%d", wingetCount, chocoCount)
	}
	if updateCount == 0 {
		t.Fatalf("expected at least one available update in this environment")
	}
	scan := scanInstalledApplications()
	if len(scan.Errors) > 0 {
		t.Fatalf("scan errors: %#v", scan.Errors)
	}
	if scan.SourceCounts["registry"] == 0 || scan.SourceCounts["winget"] == 0 {
		t.Fatalf("expected registry and winget scan counts, got %#v", scan.SourceCounts)
	}
}
