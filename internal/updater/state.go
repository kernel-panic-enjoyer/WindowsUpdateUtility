package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type StoreAutoUpdateMigrationReport struct {
	LastRun  string                          `json:"last_run,omitempty"`
	Migrated []StoreAutoUpdateMigrationEntry `json:"migrated,omitempty"`
	Disabled []StoreAutoUpdateMigrationEntry `json:"disabled,omitempty"`
}

type StoreAutoUpdateMigrationEntry struct {
	LegacyKey         string `json:"legacy_key"`
	CanonicalKey      string `json:"canonical_key,omitempty"`
	PackageFamilyName string `json:"package_family_name,omitempty"`
	Reason            string `json:"reason"`
	MigratedAt        string `json:"migrated_at"`
}

type ScheduledAutoUpdateSummary struct {
	StoreScan       ScheduledAutoUpdateStoreScanSummary `json:"store_scan,omitempty"`
	SkippedPackages []ScheduledAutoUpdateSkippedPackage `json:"skipped_packages,omitempty"`
}

type ScheduledAutoUpdateStoreScanSummary struct {
	ScanID           string `json:"scan_id,omitempty"`
	UsedGenerationID string `json:"used_generation_id,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
	Published        bool   `json:"published"`
	CompletionStatus string `json:"completion_status,omitempty"`
	FreshGeneration  bool   `json:"fresh_generation"`
	Error            string `json:"error,omitempty"`
}

type ScheduledAutoUpdateSkippedPackage struct {
	Key       string `json:"key"`
	Manager   string `json:"manager,omitempty"`
	PackageID string `json:"package_id,omitempty"`
	Reason    string `json:"reason"`
}

type State struct {
	CreatedAt                string                         `json:"created_at"`
	UpdatedAt                string                         `json:"updated_at"`
	AutoUpdateGlobal         bool                           `json:"auto_update_global"`
	AutoUpdatePackages       map[string]bool                `json:"auto_update_packages"`
	RegistryApps             map[string]ScannedApp          `json:"registry_apps"`
	WingetApps               map[string]ScannedApp          `json:"winget_apps"`
	StoreApps                map[string]ScannedApp          `json:"store_apps"`
	StoreAutoUpdateMigration StoreAutoUpdateMigrationReport `json:"store_auto_update_migration,omitempty"`
	LastScanAt               string                         `json:"last_scan_at"`
	LastAutoUpdateAt         string                         `json:"last_auto_update_at"`
	LastAutoUpdateResults    []UpdateResult                 `json:"last_auto_update_results"`
	LastAutoUpdateSummary    *ScheduledAutoUpdateSummary    `json:"last_auto_update_summary,omitempty"`
	Theme                    string                         `json:"theme"`
}

func utcNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func defaultState() State {
	now := utcNow()
	return State{
		CreatedAt:          now,
		UpdatedAt:          now,
		AutoUpdatePackages: map[string]bool{},
		RegistryApps:       map[string]ScannedApp{},
		WingetApps:         map[string]ScannedApp{},
		StoreApps:          map[string]ScannedApp{},
		Theme:              "dark",
	}
}

type StateStore interface {
	Load(context.Context) (State, error)
	Update(context.Context, func(*State) error) (State, error)
}

type FileStateStore struct {
	dir     string
	replace func(tempPath, targetPath, backupPath string) error
}

var statePathLocks sync.Map

func defaultStateStore() (StateStore, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	return NewFileStateStore(dir), nil
}

func NewFileStateStore(dir string) *FileStateStore {
	return &FileStateStore{dir: dir, replace: replaceStateFile}
}

func (store *FileStateStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	dir, err := store.stateDir()
	if err != nil {
		return State{}, err
	}
	lock := statePathMutex(dir)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquireStateStoreProcessLock(ctx, dir)
	if err != nil {
		return State{}, err
	}
	defer release()
	return store.loadLocked(ctx, dir)
}

func (store *FileStateStore) Update(ctx context.Context, mutate func(*State) error) (State, error) {
	if mutate == nil {
		return State{}, errors.New("state mutation is nil")
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	dir, err := store.stateDir()
	if err != nil {
		return State{}, err
	}
	lock := statePathMutex(dir)
	lock.Lock()
	defer lock.Unlock()
	release, err := acquireStateStoreProcessLock(ctx, dir)
	if err != nil {
		return State{}, err
	}
	defer release()

	state, err := store.loadLocked(ctx, dir)
	if err != nil {
		return State{}, err
	}
	if err := mutate(&state); err != nil {
		return State{}, err
	}
	normalizeState(&state, nil)
	state.UpdatedAt = utcNow()
	if err := store.writeLocked(ctx, dir, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func loadState() State {
	return loadStateContext(context.Background())
}

func loadStateContext(ctx context.Context) State {
	store, err := defaultStateStore()
	if err != nil {
		appLog("Could not open state store: %s.", err)
		return defaultState()
	}
	state, err := store.Load(ctx)
	if err != nil {
		appLog("Could not load state: %s.", err)
		return defaultState()
	}
	return state
}

func (store *FileStateStore) loadLocked(ctx context.Context, dir string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	path := filepath.Join(dir, "state.json")
	backupPath := filepath.Join(dir, "state.json.bak")
	state, _, err := readStateFile(path)
	if err == nil {
		return state, nil
	}
	if !os.IsNotExist(err) {
		backupState, backupData, backupErr := readStateFile(backupPath)
		if backupErr == nil {
			appLog("Recovered state.json from backup after primary load failed: %s.", err)
			if restoreErr := store.writeBytesLocked(ctx, dir, backupData, "state-recover-", "state.json.corrupt"); restoreErr != nil {
				appLog("Could not restore recovered state backup to state.json: %s.", restoreErr)
			}
			return backupState, nil
		}
		appLog("State primary and backup could not be loaded; using defaults. primary=%s backup=%s.", err, backupErr)
		return defaultState(), nil
	}
	return defaultState(), nil
}

func readStateFile(path string) (State, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, nil, err
	}
	state := defaultState()
	legacy := readLegacyStateFields(data)
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, data, err
	}
	normalizeState(&state, legacy.AssessmentCache)
	return state, data, nil
}

func normalizeState(state *State, legacyAssessments map[string]legacyAssessmentCacheEntry) {
	if state.CreatedAt == "" {
		state.CreatedAt = utcNow()
	}
	if state.UpdatedAt == "" {
		state.UpdatedAt = state.CreatedAt
	}
	if state.AutoUpdatePackages == nil {
		state.AutoUpdatePackages = map[string]bool{}
	}
	normalizeAutoUpdatePackageKeys(state, legacyAssessments)
	if state.RegistryApps == nil {
		state.RegistryApps = map[string]ScannedApp{}
	}
	if state.WingetApps == nil {
		state.WingetApps = map[string]ScannedApp{}
	}
	if state.StoreApps == nil {
		state.StoreApps = map[string]ScannedApp{}
	}
	migrateStoreScanApps(state)
	if state.Theme == "" {
		state.Theme = "dark"
	}
}

func (store *FileStateStore) writeLocked(ctx context.Context, dir string, state State) error {
	data, err := marshalState(state)
	if err != nil {
		return err
	}
	return store.writeBytesLocked(ctx, dir, data, "state-", "state.json.bak")
}

func (store *FileStateStore) writeBytesLocked(ctx context.Context, dir string, data []byte, tempPattern, backupName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	replacer := replaceStateFile
	if store != nil && store.replace != nil {
		replacer = store.replace
	}
	backupPath := ""
	if backupName != "" {
		backupPath = filepath.Join(dir, backupName)
	}
	if err := replacer(tempPath, filepath.Join(dir, "state.json"), backupPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func marshalState(state State) ([]byte, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (store *FileStateStore) stateDir() (string, error) {
	if store != nil && store.dir != "" {
		if err := os.MkdirAll(store.dir, 0o755); err != nil {
			return "", err
		}
		return store.dir, nil
	}
	return stateDir()
}

func statePathMutex(dir string) *sync.Mutex {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		absolute = dir
	}
	value, _ := statePathLocks.LoadOrStore(absolute, &sync.Mutex{})
	return value.(*sync.Mutex)
}
