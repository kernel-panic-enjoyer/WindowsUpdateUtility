//go:build windows

package updater

import (
	"context"
	"testing"
	"time"
)

func TestPackageCatalogEventSourceOpensForCurrentUser(t *testing.T) {
	if isAdmin() {
		t.Skip("PackageCatalog event source intentionally refuses elevated parent context")
	}
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, cleanup, err := (packageCatalogEventSource{}).Subscribe(ctx, StoreInstalledIdentity{
		UserSID:           userSID,
		PackageFamilyName: "Microsoft.WindowsStore_8wekyb3d8bbwe",
	})
	if err != nil {
		t.Fatalf("PackageCatalog current-user subscription failed: %v", err)
	}
	if cleanup == nil {
		t.Fatal("PackageCatalog subscription did not return cleanup")
	}
	cleanup()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("PackageCatalog subscription channel remained open after cleanup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PackageCatalog subscription cleanup did not close event channel")
	}
}
