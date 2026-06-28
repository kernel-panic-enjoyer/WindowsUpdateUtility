package updater

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInventoryResponseFlattensInventoryJSON(t *testing.T) {
	response := InventoryResponse{
		Inventory: Inventory{
			PackageLookup: PackageLookup{
				Packages: []Package{{Name: "Git", ID: "Git.Git", Manager: managerWinget}},
				Managers: map[string]ManagerStatus{
					managerWinget: {Available: true},
				},
				CommandResults: map[string]CommandResult{
					"winget_list": {OK: true},
				},
			},
			Scan: InventoryScanSummary{TrackedCount: 1},
		},
		AsyncSnapshot: AsyncSnapshot{Loading: true},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"packages", "managers", "command_results", "scan", "loading"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing flattened inventory response key %q in %s", key, encoded)
		}
	}
	if _, ok := payload["Inventory"]; ok {
		t.Fatalf("embedded Inventory should not be encoded as a nested field: %s", encoded)
	}
	if _, ok := payload["PackageLookup"]; ok {
		t.Fatalf("embedded PackageLookup should not be encoded as a nested field: %s", encoded)
	}
	if _, ok := payload["AsyncSnapshot"]; ok {
		t.Fatalf("embedded AsyncSnapshot should not be encoded as a nested field: %s", encoded)
	}
}

func TestStatusSnapshotPreservesStoreInventoryManagerDetails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	app := &App{
		status: StatusResponse{
			Managers: map[string]ManagerStatus{
				managerStore: {Available: true, ActionBackend: backendStoreCLI},
			},
		},
		statusFetchedAt: time.Now(),
		inventory: Inventory{
			PackageLookup: PackageLookup{
				Managers: map[string]ManagerStatus{
					managerStore: {
						Available:          true,
						ActionBackend:      backendStoreCLI,
						InventoryAvailable: true,
						InventoryBackend:   inventoryBackendAppX,
					},
				},
			},
		},
	}

	snapshot := app.statusSnapshot()
	store := snapshot.Managers[managerStore]
	if !store.InventoryAvailable || store.InventoryBackend != inventoryBackendAppX {
		t.Fatalf("expected status snapshot to keep Store inventory details, got %#v", store)
	}
	if app.status.Managers[managerStore].InventoryAvailable {
		t.Fatal("status snapshot should not mutate cached status managers in place")
	}
}

func TestRefreshStatusQueuesForcedRefreshWhileLoading(t *testing.T) {
	app := &App{statusLoading: true}

	app.refreshStatus(false)
	if app.statusQueued {
		t.Fatal("non-forced status refresh should not queue while loading")
	}

	app.refreshStatus(true)
	if !app.statusQueued {
		t.Fatal("forced status refresh should queue while loading")
	}
	if !app.statusLoading {
		t.Fatal("status should remain loading after queueing forced refresh")
	}
}

func TestStatusSettingsExposeAppUpdatePromptDismissedVersion(t *testing.T) {
	state := defaultState()
	state.AppUpdatePromptDismissedVersion = "1.2.3"

	settings := statusSettingsFromState(state)
	if settings.AppUpdatePromptDismissedVersion != "1.2.3" {
		t.Fatalf("expected dismissed app update version in status settings, got %#v", settings)
	}
}
