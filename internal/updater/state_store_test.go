package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestStateStoreConcurrentThemeAndAutoUpdateSettings(t *testing.T) {
	store := newTestFileStateStore(t)
	defer stubAutoUpdateTaskRunners()()
	if err := saveStateToStore(t, store, defaultState()); err != nil {
		t.Fatal(err)
	}
	enabled := true
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := setThemePreferenceWithStore(context.Background(), store, "light"); err != nil {
			t.Errorf("theme update: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		state, result := setAutoUpdateWithStore(context.Background(), store, nil, []string{"winget:Git.Git"}, &enabled)
		if !result.OK {
			t.Errorf("auto-update result: %#v state=%#v", result, state)
		}
	}()
	wg.Wait()
	state := mustLoadStoreState(t, store)
	if state.Theme != "light" || !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("concurrent settings lost fields: %#v", state)
	}
}

func TestStateStoreConcurrentScanAndThemeUpdate(t *testing.T) {
	store := newTestFileStateStore(t)
	if err := saveStateToStore(t, store, defaultState()); err != nil {
		t.Fatal(err)
	}
	restoreReaders := replaceScanReadersForStateStoreTest()
	defer restoreReaders()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = scanInstalledApplicationsWithStore(context.Background(), store)
	}()
	go func() {
		defer wg.Done()
		if _, err := setThemePreferenceWithStore(context.Background(), store, "light"); err != nil {
			t.Errorf("theme update: %v", err)
		}
	}()
	wg.Wait()
	state := mustLoadStoreState(t, store)
	if state.Theme != "light" || len(state.RegistryApps) != 1 || state.LastScanAt == "" {
		t.Fatalf("concurrent scan/theme lost fields: %#v", state)
	}
}

func TestStateStoreConcurrentAutoUpdateResultAndWebUISettings(t *testing.T) {
	store := newTestFileStateStore(t)
	defer stubAutoUpdateTaskRunners()()
	state := defaultState()
	state.AutoUpdateGlobal = true
	if err := saveStateToStore(t, store, state); err != nil {
		t.Fatal(err)
	}
	enabled := true
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = persistAutoUpdateResultsForTest(context.Background(), store, []UpdateResult{{Key: "winget:Git.Git", Result: CommandResult{OK: true, Command: "update git"}}})
	}()
	go func() {
		defer wg.Done()
		resultState, result := setAutoUpdateWithStore(context.Background(), store, nil, []string{"winget:Git.Git"}, &enabled)
		if !result.OK {
			t.Errorf("auto-update settings: %#v state=%#v", result, resultState)
		}
	}()
	wg.Wait()
	loaded := mustLoadStoreState(t, store)
	if len(loaded.LastAutoUpdateResults) != 1 || !loaded.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("concurrent auto-update persistence lost fields: %#v", loaded)
	}
}

func TestStateStoreHelperProcessesUpdateDifferentFields(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cross-process StateStore lock is Windows-specific")
	}
	store := newTestFileStateStore(t)
	if err := saveStateToStore(t, store, defaultState()); err != nil {
		t.Fatal(err)
	}
	dir := store.dir
	theme := stateStoreHelperCommand(t, dir, "theme")
	auto := stateStoreHelperCommand(t, dir, "auto")
	if err := theme.Start(); err != nil {
		t.Fatal(err)
	}
	if err := auto.Start(); err != nil {
		t.Fatal(err)
	}
	if err := theme.Wait(); err != nil {
		t.Fatalf("theme helper: %v", err)
	}
	if err := auto.Wait(); err != nil {
		t.Fatalf("auto helper: %v", err)
	}
	state := mustLoadStoreState(t, store)
	if state.Theme != "light" || !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("helper process updates lost fields: %#v", state)
	}
}

func TestStateStoreHelperProcess(t *testing.T) {
	mode := os.Getenv("UPDATER_STATE_STORE_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("UPDATER_STATE_DIR")
	if dir == "" {
		t.Fatal("UPDATER_STATE_DIR is required")
	}
	store := NewFileStateStore(dir)
	defer stubAutoUpdateTaskRunners()()
	switch mode {
	case "theme":
		if _, err := setThemePreferenceWithStore(context.Background(), store, "light"); err != nil {
			t.Fatal(err)
		}
	case "auto":
		enabled := true
		if _, result := setAutoUpdateWithStore(context.Background(), store, nil, []string{"winget:Git.Git"}, &enabled); !result.OK {
			t.Fatalf("auto helper result: %#v", result)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	os.Exit(0)
}

func TestStateStoreFailureBeforeReplacementLeavesOriginal(t *testing.T) {
	store := newTestFileStateStore(t)
	original := defaultState()
	original.Theme = "dark"
	if err := saveStateToStore(t, store, original); err != nil {
		t.Fatal(err)
	}
	store.replaceFile = func(tempPath, targetPath, backupPath string) error {
		return errors.New("before replace failure")
	}
	if _, err := setThemePreferenceWithStore(context.Background(), store, "light"); err == nil {
		t.Fatal("expected replacement failure")
	}
	loaded := mustLoadStoreState(t, store)
	if loaded.Theme != "dark" {
		t.Fatalf("original state changed after failed replacement: %#v", loaded)
	}
}

func TestStateStoreFailureDuringReplacementLeavesOriginalOrBackup(t *testing.T) {
	store := newTestFileStateStore(t)
	original := defaultState()
	original.Theme = "dark"
	if err := saveStateToStore(t, store, original); err != nil {
		t.Fatal(err)
	}
	store.replaceFile = func(tempPath, targetPath, backupPath string) error {
		data, readErr := os.ReadFile(targetPath)
		if readErr != nil {
			return readErr
		}
		if backupPath != "" {
			if writeErr := os.WriteFile(backupPath, data, 0o644); writeErr != nil {
				return writeErr
			}
		}
		return errors.New("during replace failure")
	}
	if _, err := setThemePreferenceWithStore(context.Background(), store, "light"); err == nil {
		t.Fatal("expected replacement failure")
	}
	loaded := mustLoadStoreState(t, store)
	if loaded.Theme != "dark" {
		t.Fatalf("original state changed after failed replacement: %#v", loaded)
	}
	if _, err := os.Stat(filepath.Join(store.dir, "state.json.bak")); err != nil {
		t.Fatalf("expected backup after simulated replacement failure: %v", err)
	}
}

func TestStateStoreCorruptPrimaryRecoversBackup(t *testing.T) {
	store := newTestFileStateStore(t)
	first := defaultState()
	first.Theme = "dark"
	if err := saveStateToStore(t, store, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Theme = "light"
	second.AutoUpdatePackages["winget:Git.Git"] = true
	if err := saveStateToStore(t, store, second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "state.json"), []byte(`{"broken":`), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Theme != "dark" {
		t.Fatalf("backup recovery did not load last-known-good backup: %#v", loaded)
	}
}

func TestStateStoreUpdateKeepsCanonicalAutoUpdatePackageKeys(t *testing.T) {
	store := newTestFileStateStore(t)
	defer stubAutoUpdateTaskRunners()()
	if err := saveStateToStore(t, store, defaultState()); err != nil {
		t.Fatal(err)
	}
	enabled := true
	state, result := setAutoUpdateWithStore(context.Background(), store, nil, []string{"store:Ambiguous Display Name", "winget:Git.Git"}, &enabled)
	if !result.OK {
		t.Fatalf("unexpected result: %#v", result)
	}
	if state.AutoUpdatePackages["store:Ambiguous Display Name"] || state.AutoUpdatePackages[""] || !state.AutoUpdatePackages["winget:Git.Git"] {
		t.Fatalf("auto-update keys were not canonical: %#v", state.AutoUpdatePackages)
	}
	loaded := mustLoadStoreState(t, store)
	if loaded.AutoUpdatePackages["store:Ambiguous Display Name"] || loaded.AutoUpdatePackages[""] {
		t.Fatalf("non-canonical keys persisted: %#v", loaded.AutoUpdatePackages)
	}
}

func stubAutoUpdateTaskRunners() func() {
	oldCreate := createAutoUpdateTaskRunner
	oldDelete := deleteTaskRunner
	createAutoUpdateTaskRunner = func() CommandResult {
		return CommandResult{OK: true, Command: "create auto-update task"}
	}
	deleteTaskRunner = func(name string) CommandResult {
		return CommandResult{OK: true, Command: "delete " + name}
	}
	return func() {
		createAutoUpdateTaskRunner = oldCreate
		deleteTaskRunner = oldDelete
	}
}

func newTestFileStateStore(t *testing.T) *FileStateStore {
	t.Helper()
	dir := t.TempDir()
	return NewFileStateStore(dir)
}

func saveStateToStore(t *testing.T, store StateStore, state State) error {
	t.Helper()
	_, err := store.Update(context.Background(), func(current *State) error {
		*current = state
		return nil
	})
	return err
}

func mustLoadStoreState(t *testing.T, store StateStore) State {
	t.Helper()
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func replaceScanReadersForStateStoreTest() func() {
	oldRegistryReader := registryAppsReader
	oldWingetReader := wingetAppsReader
	oldAppxReader := appxAppsReader
	registryAppsReader = func() ([]ScannedApp, error) {
		return []ScannedApp{{Key: "registry:example", Name: "Example", Source: "registry"}}, nil
	}
	wingetAppsReader = func(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
		return nil, &CommandResult{OK: true, Command: "winget export"}, nil
	}
	appxAppsReader = func(ctx context.Context) ([]ScannedApp, *CommandResult, error) {
		return nil, &CommandResult{OK: true, Command: "Get-AppxPackage"}, nil
	}
	return func() {
		registryAppsReader = oldRegistryReader
		wingetAppsReader = oldWingetReader
		appxAppsReader = oldAppxReader
	}
}

func persistAutoUpdateResultsForTest(ctx context.Context, store StateStore, results []UpdateResult) error {
	_, err := store.Update(ctx, func(state *State) error {
		state.LastAutoUpdateAt = utcNow()
		state.LastAutoUpdateResults = summarizeUpdateResults(results, state.LastAutoUpdateAt)
		return nil
	})
	return err
}

func stateStoreHelperCommand(t *testing.T, dir, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestStateStoreHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(),
		"UPDATER_STATE_DIR="+dir,
		"UPDATER_STATE_STORE_HELPER="+mode,
	)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	t.Cleanup(func() {
		if output.Len() > 0 && cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			t.Log(output.String())
		}
	})
	return cmd
}
