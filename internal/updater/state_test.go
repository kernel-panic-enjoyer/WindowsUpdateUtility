package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	got, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("expected override %s, got %s", dir, got)
	}
}

func TestAppTempDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_TEMP_DIR", dir)
	got, err := appTempDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("expected temp override %s, got %s", dir, got)
	}
}

func TestAppTempDirUsesSystemTempByDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPDATER_TEMP_DIR", "")
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	got, err := appTempDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, appDirName)
	if got != want {
		t.Fatalf("expected temp dir %s, got %s", want, got)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected temp dir to be created: %v", err)
	}
}

func TestLoadStateMigratesStoreAppsOutOfWingetBucket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {},
  "registry_apps": {},
  "winget_apps": {
    "winget:git.git": {"key":"winget:git.git","name":"Git","source":"winget","manager":"winget"},
    "store:openai.codex": {"key":"store:openai.codex","name":"Codex","source":"store","manager":"store"}
  },
  "store_resolve_cache": {},
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	if _, ok := state.WingetApps["store:openai.codex"]; ok {
		t.Fatalf("store app was not migrated out of winget bucket: %#v", state.WingetApps)
	}
	if state.StoreApps["store:openai.codex"].Name != "Codex" {
		t.Fatalf("store app missing after migration: %#v", state.StoreApps)
	}
}

func TestLoadStateNormalizesVersionedStoreAppKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {},
  "registry_apps": {},
  "winget_apps": {},
  "store_apps": {
    "store:openai.codex_1.0.0.0_x64__abc123": {
      "key": "store:openai.codex_1.0.0.0_x64__abc123",
      "name": "Codex",
      "source": "store",
      "manager": "store",
      "package_id": "OpenAI.Codex_1.0.0.0_x64__abc123",
      "first_seen": "2026-06-14T12:00:00Z"
    }
  },
  "store_resolve_cache": {},
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	if _, ok := state.StoreApps["store:openai.codex_1.0.0.0_x64__abc123"]; ok {
		t.Fatalf("versioned store key was not normalized: %#v", state.StoreApps)
	}
	app := state.StoreApps["store:openai.codex"]
	if app.PackageID != "OpenAI.Codex" || app.FirstSeen != "2026-06-14T12:00:00Z" {
		t.Fatalf("unexpected normalized store app: %#v", app)
	}
}

func TestLoadStateNormalizesStoreAutoUpdateKeys(t *testing.T) {
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	raw := `{
  "created_at": "2026-06-14T12:00:00Z",
  "updated_at": "2026-06-14T12:00:00Z",
  "auto_update_packages": {
    "store:OpenAI.Codex_abc123": true,
    "store:9NCODEX": true,
    "store:Ambiguous Display Name": true,
    "winget:Git.Git": true
  },
  "registry_apps": {},
  "winget_apps": {},
  "store_apps": {},
  "store_resolve_cache": {},
  "store_update_assessment_cache": {
    "exact": {
      "user_sid": "S-1-5-21-exact",
      "package_family_name": "Exact.App_abc123",
      "scan_id": "scan-exact",
      "state": "available",
      "observed_at": "2026-06-14T12:00:00Z",
      "store_product_id": "9NCODEX",
      "exact_action_target_available": true
    }
  },
  "theme": "dark"
}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	state := loadState()
	codexKey := canonicalStoreAutoUpdateKey(userSID, "OpenAI.Codex_abc123")
	productKey := canonicalStoreAutoUpdateKey("S-1-5-21-exact", "Exact.App_abc123")
	if !state.AutoUpdatePackages[codexKey] || !state.AutoUpdatePackages[productKey] || !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("auto-update keys were not normalized correctly: %#v", state.AutoUpdatePackages)
	}
	if state.AutoUpdatePackages["store:Ambiguous Display Name"] {
		t.Fatalf("ambiguous Store auto-update key should not remain: %#v", state.AutoUpdatePackages)
	}
	if len(state.StoreAutoUpdateMigration.Disabled) != 1 {
		t.Fatalf("expected one disabled ambiguous Store preference, got %#v", state.StoreAutoUpdateMigration)
	}
}

func TestRunAutoUpdateSkipsWhenNoPackagesOptedIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	state := defaultState()
	state.AutoUpdateGlobal = true
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}

	results := runAutoUpdate()
	if len(results) != 0 {
		t.Fatalf("expected no auto-update results without opted-in packages, got %#v", results)
	}

	updated := loadState()
	if updated.LastAutoUpdateAt == "" {
		t.Fatal("expected skipped auto-update to record a run timestamp")
	}
}

func TestRunAutoUpdateSkipsUnknownVersionPackages(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	state := defaultState()
	state.AutoUpdateGlobal = true
	state.AutoUpdatePackages["winget:Vendor.Unknown"] = true
	if err := saveState(state); err != nil {
		t.Fatal(err)
	}

	oldGetter := inventoryGetter
	inventoryGetter = func(ctx context.Context) Inventory {
		return Inventory{PackageLookup: PackageLookup{Packages: []Package{{
			Key:              "winget:Vendor.Unknown",
			Manager:          managerWinget,
			ID:               "Vendor.Unknown",
			Name:             "Unknown Version App",
			Version:          "Unknown",
			AvailableVersion: "1.2.0",
			UpdateAvailable:  true,
			UpdateSupported:  true,
			UnknownVersion:   true,
		}}}}
	}
	defer func() { inventoryGetter = oldGetter }()
	// runAutoUpdate now runs a fresh Store scan inline (standalone task process
	// has no server). Stub it so the test does not spawn real Store subprocesses.
	oldStoreScan := runStoreTransactionalScanForInventory
	runStoreTransactionalScanForInventory = func(ctx context.Context) (StoreScanResult, error) {
		return StoreScanResult{}, nil
	}
	defer func() { runStoreTransactionalScanForInventory = oldStoreScan }()

	results := runAutoUpdate()
	if len(results) != 0 {
		t.Fatalf("unknown-version package should require individual confirmation and be skipped by auto-update, got %#v", results)
	}
	updated := loadState()
	if updated.LastAutoUpdateAt == "" {
		t.Fatal("expected skipped unknown-version auto-update to record a run timestamp")
	}
	if len(updated.LastAutoUpdateResults) != 0 {
		t.Fatalf("expected no persisted update results for skipped unknown-version package, got %#v", updated.LastAutoUpdateResults)
	}
}

func TestSetAutoUpdateRejectsAmbiguousStorePackageKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UPDATER_STATE_DIR", dir)
	if err := saveState(defaultState()); err != nil {
		t.Fatal(err)
	}

	oldDelete := deleteTaskRunner
	deleteTaskRunner = func(name string) CommandResult {
		return CommandResult{OK: true, Command: "delete " + name}
	}
	defer func() { deleteTaskRunner = oldDelete }()

	enabled := true
	state, result := setAutoUpdate(nil, []string{"store:Ambiguous Display Name", "winget:Git.Git"}, &enabled)
	if !result.OK {
		t.Fatalf("unexpected task result: %#v", result)
	}
	if state.AutoUpdatePackages[""] {
		t.Fatalf("ambiguous Store key was saved as an empty canonical key: %#v", state.AutoUpdatePackages)
	}
	if state.AutoUpdatePackages["store:Ambiguous Display Name"] {
		t.Fatalf("ambiguous Store key should not be persisted: %#v", state.AutoUpdatePackages)
	}
	if !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("non-Store package key should still be persisted: %#v", state.AutoUpdatePackages)
	}
	loaded := loadState()
	if loaded.AutoUpdatePackages[""] || loaded.AutoUpdatePackages["store:Ambiguous Display Name"] {
		t.Fatalf("ambiguous Store key persisted to disk: %#v", loaded.AutoUpdatePackages)
	}
}

func TestNormalizeStatePreservesBoundedAppUpdatePromptDismissal(t *testing.T) {
	state := defaultState()
	state.AppUpdatePromptDismissedVersion = strings.Repeat("v", maxStateStringBytes+64)

	normalizeState(&state, nil)

	if len(state.AppUpdatePromptDismissedVersion) > maxStateStringBytes {
		t.Fatalf("dismissed app update version was not bounded: %d", len(state.AppUpdatePromptDismissedVersion))
	}
	if state.AppUpdatePromptDismissedVersion == "" {
		t.Fatal("dismissed app update version should be preserved when non-empty")
	}
}

func TestSetAutoUpdateSaveFailureDoesNotMutateScheduledTask(t *testing.T) {
	oldCreate := createAutoUpdateTaskRunner
	oldDelete := deleteTaskRunner
	taskCalls := 0
	createAutoUpdateTaskRunner = func() CommandResult {
		taskCalls++
		return CommandResult{OK: true, Command: "create task"}
	}
	deleteTaskRunner = func(name string) CommandResult {
		taskCalls++
		return CommandResult{OK: true, Command: "delete " + name}
	}
	defer func() {
		createAutoUpdateTaskRunner = oldCreate
		deleteTaskRunner = oldDelete
	}()

	store := newMemoryStateStore(defaultState())
	store.updateErr = errors.New("state save failed")
	global := true
	packageEnabled := true
	state, result := setAutoUpdateWithStore(context.Background(), store, &global, []string{"winget:Git.Git"}, &packageEnabled)

	if result.OK || result.Code != 2 || !strings.Contains(result.Stderr, "state save failed") {
		t.Fatalf("expected validation-style save failure result, got %#v", result)
	}
	if taskCalls != 0 {
		t.Fatalf("scheduled task should not be changed when settings fail to save, calls=%d", taskCalls)
	}
	if state.AutoUpdateGlobal || state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("response should contain persisted settings, not unsaved request: %#v", state)
	}
}
