package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxStateFileBytes                 = 2 * 1024 * 1024
	maxStateAutoUpdatePackages        = 2000
	maxStateScannedAppsPerBucket      = 5000
	maxStateMigrationEntries          = 1000
	maxStateSkippedPackageSummaries   = 500
	maxStateDurableUpdateSummaries    = 100
	maxStateDurableUpdateHistoryBytes = 128 * 1024
	maxStateSummaryMessageBytes       = 512
	maxStateStringBytes               = 4096
	maxCommandResultCommandBytes      = 16 * 1024
	terminalCommandResultStreamBytes  = 16 * 1024
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
	LastAutoUpdateResults    []UpdateResultSummary          `json:"last_auto_update_results"`
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
		return defaultState(), fmt.Errorf("state primary and backup could not be loaded: primary=%w backup=%v", err, backupErr)
	}
	return defaultState(), nil
}

func readStateFile(path string) (State, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return State{}, nil, err
	}
	if info.Size() > maxStateFileBytes {
		return State{}, nil, fmt.Errorf("state file exceeds %d bytes", maxStateFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return State{}, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateFileBytes+1))
	if err != nil {
		return State{}, nil, err
	}
	if len(data) > maxStateFileBytes {
		return State{}, nil, fmt.Errorf("state file exceeds %d bytes", maxStateFileBytes)
	}
	state := defaultState()
	legacy := readLegacyStateFields(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&state); err != nil {
		return State{}, data, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("state file contains trailing JSON data")
		}
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
	state.CreatedAt = clampStateString(state.CreatedAt)
	state.UpdatedAt = clampStateString(state.UpdatedAt)
	state.LastScanAt = clampStateString(state.LastScanAt)
	state.LastAutoUpdateAt = clampStateString(state.LastAutoUpdateAt)
	state.Theme = clampStateString(state.Theme)
	state.AutoUpdatePackages = trimBoolMap(state.AutoUpdatePackages, maxStateAutoUpdatePackages)
	state.RegistryApps = trimScannedAppMap(state.RegistryApps, maxStateScannedAppsPerBucket)
	state.WingetApps = trimScannedAppMap(state.WingetApps, maxStateScannedAppsPerBucket)
	state.StoreApps = trimScannedAppMap(state.StoreApps, maxStateScannedAppsPerBucket)
	state.StoreAutoUpdateMigration.Migrated = trimMigrationEntries(state.StoreAutoUpdateMigration.Migrated)
	state.StoreAutoUpdateMigration.Disabled = trimMigrationEntries(state.StoreAutoUpdateMigration.Disabled)
	state.LastAutoUpdateResults = trimUpdateResultSummaries(state.LastAutoUpdateResults)
	if state.LastAutoUpdateSummary != nil {
		state.LastAutoUpdateSummary.SkippedPackages = trimSkippedPackageSummaries(state.LastAutoUpdateSummary.SkippedPackages)
		state.LastAutoUpdateSummary.StoreScan.Error = truncateUTF8String(state.LastAutoUpdateSummary.StoreScan.Error, maxStateSummaryMessageBytes)
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
	normalizeState(&state, nil)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	for len(data) > maxStateFileBytes && len(state.LastAutoUpdateResults) > 0 {
		state.LastAutoUpdateResults = state.LastAutoUpdateResults[1:]
		data, err = json.MarshalIndent(state, "", "  ")
		if err != nil {
			return nil, err
		}
	}
	if len(data) > maxStateFileBytes {
		return nil, fmt.Errorf("state exceeds %d bytes after normalization", maxStateFileBytes)
	}
	return append(data, '\n'), nil
}

func summarizeUpdateResults(results []UpdateResult, finishedAt string) []UpdateResultSummary {
	summaries := make([]UpdateResultSummary, 0, len(results))
	for _, result := range results {
		summaries = append(summaries, summarizeUpdateResult(result, finishedAt))
	}
	return trimUpdateResultSummaries(summaries)
}

func summarizeUpdateResult(result UpdateResult, finishedAt string) UpdateResultSummary {
	manager, packageID, _ := splitPackageKey(result.Key)
	if manager == "" {
		manager = managerFromCommand(result.Result.Command)
	}
	message := ""
	if result.Result.OK {
		message = "Command succeeded."
	} else {
		message = fmt.Sprintf("Command failed with code %d. See Session Log for full output.", result.Result.Code)
	}
	return UpdateResultSummary{
		Key:             truncateUTF8String(result.Key, maxStateStringBytes),
		Manager:         truncateUTF8String(manager, maxStateStringBytes),
		PackageID:       truncateUTF8String(packageID, maxStateStringBytes),
		Success:         result.Result.OK,
		Code:            result.Result.Code,
		FinishedAt:      truncateUTF8String(finishedAt, maxStateStringBytes),
		RestartRequired: result.Result.Code == 3010,
		Message:         truncateUTF8String(sanitizeProviderDiagnostic(message), maxStateSummaryMessageBytes),
	}
}

func managerFromCommand(command string) string {
	command = strings.ToLower(strings.TrimSpace(command))
	switch {
	case strings.Contains(command, "winget"):
		return managerWinget
	case strings.Contains(command, "choco"):
		return managerChoco
	case strings.Contains(command, "store"):
		return managerStore
	default:
		return ""
	}
}

func trimUpdateResultSummaries(results []UpdateResultSummary) []UpdateResultSummary {
	if len(results) == 0 {
		return nil
	}
	if len(results) > maxStateDurableUpdateSummaries {
		results = append([]UpdateResultSummary(nil), results[len(results)-maxStateDurableUpdateSummaries:]...)
	} else {
		results = append([]UpdateResultSummary(nil), results...)
	}
	for i := range results {
		results[i].Key = truncateUTF8String(results[i].Key, maxStateStringBytes)
		results[i].Manager = truncateUTF8String(results[i].Manager, maxStateStringBytes)
		results[i].PackageID = truncateUTF8String(results[i].PackageID, maxStateStringBytes)
		results[i].FinishedAt = truncateUTF8String(results[i].FinishedAt, maxStateStringBytes)
		results[i].Message = truncateUTF8String(results[i].Message, maxStateSummaryMessageBytes)
	}
	for len(results) > 0 {
		data, err := json.Marshal(results)
		if err != nil || len(data) <= maxStateDurableUpdateHistoryBytes {
			break
		}
		results = results[1:]
	}
	return results
}

func trimBoolMap(values map[string]bool, limit int) map[string]bool {
	if values == nil {
		return map[string]bool{}
	}
	keys := sortedMapKeys(values)
	out := map[string]bool{}
	for _, key := range trimKeysToLimit(keys, limit) {
		out[truncateUTF8String(key, maxStateStringBytes)] = values[key]
	}
	return out
}

func trimScannedAppMap(values map[string]ScannedApp, limit int) map[string]ScannedApp {
	if values == nil {
		return map[string]ScannedApp{}
	}
	keys := sortedMapKeys(values)
	out := map[string]ScannedApp{}
	for _, key := range trimKeysToLimit(keys, limit) {
		app := values[key]
		app.Key = truncateUTF8String(app.Key, maxStateStringBytes)
		app.Name = truncateUTF8String(app.Name, maxStateStringBytes)
		app.Source = truncateUTF8String(app.Source, maxStateStringBytes)
		app.Manager = truncateUTF8String(app.Manager, maxStateStringBytes)
		app.PackageID = truncateUTF8String(app.PackageID, maxStateStringBytes)
		app.FirstSeen = truncateUTF8String(app.FirstSeen, maxStateStringBytes)
		out[truncateUTF8String(key, maxStateStringBytes)] = app
	}
	return out
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func trimKeysToLimit(keys []string, limit int) []string {
	if limit <= 0 || len(keys) <= limit {
		return keys
	}
	return keys[len(keys)-limit:]
}

func trimMigrationEntries(entries []StoreAutoUpdateMigrationEntry) []StoreAutoUpdateMigrationEntry {
	if len(entries) > maxStateMigrationEntries {
		entries = entries[len(entries)-maxStateMigrationEntries:]
	}
	out := append([]StoreAutoUpdateMigrationEntry(nil), entries...)
	for i := range out {
		out[i].LegacyKey = truncateUTF8String(out[i].LegacyKey, maxStateStringBytes)
		out[i].CanonicalKey = truncateUTF8String(out[i].CanonicalKey, maxStateStringBytes)
		out[i].PackageFamilyName = truncateUTF8String(out[i].PackageFamilyName, maxStateStringBytes)
		out[i].Reason = truncateUTF8String(out[i].Reason, maxStateSummaryMessageBytes)
		out[i].MigratedAt = truncateUTF8String(out[i].MigratedAt, maxStateStringBytes)
	}
	return out
}

func trimSkippedPackageSummaries(entries []ScheduledAutoUpdateSkippedPackage) []ScheduledAutoUpdateSkippedPackage {
	if len(entries) > maxStateSkippedPackageSummaries {
		entries = entries[len(entries)-maxStateSkippedPackageSummaries:]
	}
	out := append([]ScheduledAutoUpdateSkippedPackage(nil), entries...)
	for i := range out {
		out[i].Key = truncateUTF8String(out[i].Key, maxStateStringBytes)
		out[i].Manager = truncateUTF8String(out[i].Manager, maxStateStringBytes)
		out[i].PackageID = truncateUTF8String(out[i].PackageID, maxStateStringBytes)
		out[i].Reason = truncateUTF8String(out[i].Reason, maxStateSummaryMessageBytes)
	}
	return out
}

func clampStateString(value string) string {
	return truncateUTF8String(value, maxStateStringBytes)
}

func truncateUTF8String(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	tail := []byte(value[:limit])
	return strings.ToValidUTF8(string(tail), "")
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
