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
	CreatedAt                       string                         `json:"created_at"`
	UpdatedAt                       string                         `json:"updated_at"`
	AutoUpdateGlobal                bool                           `json:"auto_update_global"`
	AutoUpdatePackages              map[string]bool                `json:"auto_update_packages"`
	RegistryApps                    map[string]ScannedApp          `json:"registry_apps"`
	WingetApps                      map[string]ScannedApp          `json:"winget_apps"`
	StoreApps                       map[string]ScannedApp          `json:"store_apps"`
	StoreAutoUpdateMigration        StoreAutoUpdateMigrationReport `json:"store_auto_update_migration,omitempty"`
	LastScanAt                      string                         `json:"last_scan_at"`
	LastAutoUpdateAt                string                         `json:"last_auto_update_at"`
	LastAutoUpdateResults           []UpdateResultSummary          `json:"last_auto_update_results"`
	LastAutoUpdateSummary           *ScheduledAutoUpdateSummary    `json:"last_auto_update_summary,omitempty"`
	Theme                           string                         `json:"theme"`
	AppUpdatePromptDismissedVersion string                         `json:"app_update_prompt_dismissed_version,omitempty"`
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
	dir         string
	replaceFile func(tempPath, targetPath, backupPath string) error
}

var stateDirectoryLocks sync.Map

func defaultStateStore() (StateStore, error) {
	stateDirectory, err := stateDir()
	if err != nil {
		return nil, err
	}
	return NewFileStateStore(stateDirectory), nil
}

func NewFileStateStore(stateDirectory string) *FileStateStore {
	return &FileStateStore{dir: stateDirectory, replaceFile: replaceFileKeepingBackup}
}

func (store *FileStateStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	stateDirectory, err := store.stateDir()
	if err != nil {
		return State{}, err
	}
	directoryLock := stateDirectoryMutex(stateDirectory)
	directoryLock.Lock()
	defer directoryLock.Unlock()
	releaseProcessLock, err := acquireStateStoreProcessLock(ctx, stateDirectory)
	if err != nil {
		return State{}, err
	}
	defer releaseProcessLock()
	return store.loadLocked(ctx, stateDirectory)
}

func (store *FileStateStore) Update(ctx context.Context, applyMutation func(*State) error) (State, error) {
	if applyMutation == nil {
		return State{}, errors.New("state mutation is nil")
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	stateDirectory, err := store.stateDir()
	if err != nil {
		return State{}, err
	}
	directoryLock := stateDirectoryMutex(stateDirectory)
	directoryLock.Lock()
	defer directoryLock.Unlock()
	releaseProcessLock, err := acquireStateStoreProcessLock(ctx, stateDirectory)
	if err != nil {
		return State{}, err
	}
	defer releaseProcessLock()

	state, err := store.loadLocked(ctx, stateDirectory)
	if err != nil {
		return State{}, err
	}
	if err := applyMutation(&state); err != nil {
		return State{}, err
	}
	normalizeState(&state, nil)
	state.UpdatedAt = utcNow()
	if err := store.writeLocked(ctx, stateDirectory, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func loadState() State {
	return loadStateContext(context.Background())
}

func loadStateContext(ctx context.Context) State {
	stateStore, err := defaultStateStore()
	if err != nil {
		appLog("Could not open state store: %s.", err)
		return defaultState()
	}
	state, err := stateStore.Load(ctx)
	if err != nil {
		appLog("Could not load state: %s.", err)
		return defaultState()
	}
	return state
}

func (store *FileStateStore) loadLocked(ctx context.Context, stateDirectory string) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	statePath := filepath.Join(stateDirectory, "state.json")
	backupPath := filepath.Join(stateDirectory, "state.json.bak")
	state, _, err := readStateFile(statePath)
	if err == nil {
		return state, nil
	}
	if !os.IsNotExist(err) {
		recoveredState, recoveredData, backupLoadErr := readStateFile(backupPath)
		if backupLoadErr == nil {
			appLog("Recovered state.json from backup after primary load failed: %s.", err)
			if restoreErr := store.writeBytesLocked(ctx, stateDirectory, recoveredData, "state-recover-", "state.json.corrupt"); restoreErr != nil {
				appLog("Could not restore recovered state backup to state.json: %s.", restoreErr)
			}
			return recoveredState, nil
		}
		appLog("State primary and backup could not be loaded; using defaults. primary=%s backup=%s.", err, backupLoadErr)
		return defaultState(), fmt.Errorf("state primary and backup could not be loaded: primary=%w backup=%v", err, backupLoadErr)
	}
	return defaultState(), nil
}

func readStateFile(statePath string) (State, []byte, error) {
	fileInfo, err := os.Stat(statePath)
	if err != nil {
		return State{}, nil, err
	}
	if fileInfo.Size() > maxStateFileBytes {
		return State{}, nil, fmt.Errorf("state file exceeds %d bytes", maxStateFileBytes)
	}
	stateFile, err := os.Open(statePath)
	if err != nil {
		return State{}, nil, err
	}
	defer stateFile.Close()
	stateData, err := io.ReadAll(io.LimitReader(stateFile, maxStateFileBytes+1))
	if err != nil {
		return State{}, nil, err
	}
	if len(stateData) > maxStateFileBytes {
		return State{}, nil, fmt.Errorf("state file exceeds %d bytes", maxStateFileBytes)
	}
	state := defaultState()
	legacyFields := readLegacyStateFields(stateData)
	decoder := json.NewDecoder(bytes.NewReader(stateData))
	if err := decoder.Decode(&state); err != nil {
		return State{}, stateData, err
	}
	var trailingJSON json.RawMessage
	if err := decoder.Decode(&trailingJSON); err != io.EOF {
		if err == nil {
			err = errors.New("state file contains trailing JSON data")
		}
		return State{}, stateData, err
	}
	normalizeState(&state, legacyFields.AssessmentCache)
	return state, stateData, nil
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
	migrateAndNormalizeAutoUpdatePackageKeys(state, legacyAssessments)
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
	state.CreatedAt = truncateStateString(state.CreatedAt)
	state.UpdatedAt = truncateStateString(state.UpdatedAt)
	state.LastScanAt = truncateStateString(state.LastScanAt)
	state.LastAutoUpdateAt = truncateStateString(state.LastAutoUpdateAt)
	state.Theme = truncateStateString(state.Theme)
	state.AppUpdatePromptDismissedVersion = truncateStateString(strings.TrimSpace(state.AppUpdatePromptDismissedVersion))
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

func (store *FileStateStore) writeLocked(ctx context.Context, stateDirectory string, state State) error {
	stateJSON, err := marshalState(state)
	if err != nil {
		return err
	}
	return store.writeBytesLocked(ctx, stateDirectory, stateJSON, "state-", "state.json.bak")
}

func (store *FileStateStore) writeBytesLocked(ctx context.Context, stateDirectory string, stateJSON []byte, tempPattern, backupName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDirectory, 0o755); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(stateDirectory, tempPattern)
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	removeTempFile := true
	defer func() {
		if removeTempFile {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(stateJSON); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	replaceStateFile := replaceFileKeepingBackup
	if store != nil && store.replaceFile != nil {
		replaceStateFile = store.replaceFile
	}
	backupPath := ""
	if backupName != "" {
		backupPath = filepath.Join(stateDirectory, backupName)
	}
	if err := replaceStateFile(tempPath, filepath.Join(stateDirectory, "state.json"), backupPath); err != nil {
		return err
	}
	removeTempFile = false
	return nil
}

func marshalState(state State) ([]byte, error) {
	normalizeState(&state, nil)
	for {
		stateJSON, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return nil, err
		}
		if len(stateJSON) <= maxStateFileBytes {
			return append(stateJSON, '\n'), nil
		}
		if len(state.LastAutoUpdateResults) == 0 {
			return nil, fmt.Errorf("state exceeds %d bytes after normalization", maxStateFileBytes)
		}
		state.LastAutoUpdateResults = state.LastAutoUpdateResults[1:]
	}
}

func summarizeUpdateResults(updateResults []UpdateResult, finishedAt string) []UpdateResultSummary {
	summaries := make([]UpdateResultSummary, 0, len(updateResults))
	for _, updateResult := range updateResults {
		summaries = append(summaries, summarizeUpdateResult(updateResult, finishedAt))
	}
	return trimUpdateResultSummaries(summaries)
}

func summarizeUpdateResult(updateResult UpdateResult, finishedAt string) UpdateResultSummary {
	manager, packageID, _ := splitPackageKey(updateResult.Key)
	if manager == "" {
		manager = managerFromCommand(updateResult.Result.Command)
	}
	summaryMessage := ""
	if updateResult.Result.OK {
		summaryMessage = "Command succeeded."
	} else {
		summaryMessage = fmt.Sprintf("Command failed with code %d. See Session Log for full output.", updateResult.Result.Code)
	}
	return UpdateResultSummary{
		Key:             truncateUTF8String(updateResult.Key, maxStateStringBytes),
		Manager:         truncateUTF8String(manager, maxStateStringBytes),
		PackageID:       truncateUTF8String(packageID, maxStateStringBytes),
		Success:         updateResult.Result.OK,
		Code:            updateResult.Result.Code,
		FinishedAt:      truncateUTF8String(finishedAt, maxStateStringBytes),
		RestartRequired: updateResult.Result.Code == 3010,
		Message:         truncateUTF8String(sanitizeProviderDiagnostic(summaryMessage), maxStateSummaryMessageBytes),
	}
}

func managerFromCommand(command string) string {
	normalizedCommand := strings.ToLower(strings.TrimSpace(command))
	switch {
	case strings.Contains(normalizedCommand, "winget"):
		return managerWinget
	case strings.Contains(normalizedCommand, "choco"):
		return managerChoco
	case strings.Contains(normalizedCommand, "store"):
		return managerStore
	default:
		return ""
	}
}

func trimUpdateResultSummaries(summaries []UpdateResultSummary) []UpdateResultSummary {
	if len(summaries) == 0 {
		return nil
	}
	if len(summaries) > maxStateDurableUpdateSummaries {
		summaries = summaries[len(summaries)-maxStateDurableUpdateSummaries:]
	}
	summaries = append([]UpdateResultSummary(nil), summaries...)
	for i := range summaries {
		summaries[i].Key = truncateUTF8String(summaries[i].Key, maxStateStringBytes)
		summaries[i].Manager = truncateUTF8String(summaries[i].Manager, maxStateStringBytes)
		summaries[i].PackageID = truncateUTF8String(summaries[i].PackageID, maxStateStringBytes)
		summaries[i].FinishedAt = truncateUTF8String(summaries[i].FinishedAt, maxStateStringBytes)
		summaries[i].Message = truncateUTF8String(summaries[i].Message, maxStateSummaryMessageBytes)
	}
	for len(summaries) > 0 {
		summariesJSON, err := json.Marshal(summaries)
		if err != nil || len(summariesJSON) <= maxStateDurableUpdateHistoryBytes {
			break
		}
		summaries = summaries[1:]
	}
	return summaries
}

func trimBoolMap(entries map[string]bool, limit int) map[string]bool {
	if entries == nil {
		return map[string]bool{}
	}
	keys := sortedMapKeys(entries)
	trimmed := map[string]bool{}
	for _, key := range trimKeysToLimit(keys, limit) {
		trimmed[truncateUTF8String(key, maxStateStringBytes)] = entries[key]
	}
	return trimmed
}

func trimScannedAppMap(entries map[string]ScannedApp, limit int) map[string]ScannedApp {
	if entries == nil {
		return map[string]ScannedApp{}
	}
	keys := sortedMapKeys(entries)
	trimmed := map[string]ScannedApp{}
	for _, key := range trimKeysToLimit(keys, limit) {
		app := entries[key]
		app.Key = truncateUTF8String(app.Key, maxStateStringBytes)
		app.Name = truncateUTF8String(app.Name, maxStateStringBytes)
		app.Source = truncateUTF8String(app.Source, maxStateStringBytes)
		app.Manager = truncateUTF8String(app.Manager, maxStateStringBytes)
		app.PackageID = truncateUTF8String(app.PackageID, maxStateStringBytes)
		app.FirstSeen = truncateUTF8String(app.FirstSeen, maxStateStringBytes)
		trimmed[truncateUTF8String(key, maxStateStringBytes)] = app
	}
	return trimmed
}

func sortedMapKeys[V any](entries map[string]V) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
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

func trimMigrationEntries(migrationEntries []StoreAutoUpdateMigrationEntry) []StoreAutoUpdateMigrationEntry {
	if len(migrationEntries) > maxStateMigrationEntries {
		migrationEntries = migrationEntries[len(migrationEntries)-maxStateMigrationEntries:]
	}
	trimmedEntries := append([]StoreAutoUpdateMigrationEntry(nil), migrationEntries...)
	for i := range trimmedEntries {
		trimmedEntries[i].LegacyKey = truncateUTF8String(trimmedEntries[i].LegacyKey, maxStateStringBytes)
		trimmedEntries[i].CanonicalKey = truncateUTF8String(trimmedEntries[i].CanonicalKey, maxStateStringBytes)
		trimmedEntries[i].PackageFamilyName = truncateUTF8String(trimmedEntries[i].PackageFamilyName, maxStateStringBytes)
		trimmedEntries[i].Reason = truncateUTF8String(trimmedEntries[i].Reason, maxStateSummaryMessageBytes)
		trimmedEntries[i].MigratedAt = truncateUTF8String(trimmedEntries[i].MigratedAt, maxStateStringBytes)
	}
	return trimmedEntries
}

func trimSkippedPackageSummaries(skippedPackages []ScheduledAutoUpdateSkippedPackage) []ScheduledAutoUpdateSkippedPackage {
	if len(skippedPackages) > maxStateSkippedPackageSummaries {
		skippedPackages = skippedPackages[len(skippedPackages)-maxStateSkippedPackageSummaries:]
	}
	trimmedPackages := append([]ScheduledAutoUpdateSkippedPackage(nil), skippedPackages...)
	for i := range trimmedPackages {
		trimmedPackages[i].Key = truncateUTF8String(trimmedPackages[i].Key, maxStateStringBytes)
		trimmedPackages[i].Manager = truncateUTF8String(trimmedPackages[i].Manager, maxStateStringBytes)
		trimmedPackages[i].PackageID = truncateUTF8String(trimmedPackages[i].PackageID, maxStateStringBytes)
		trimmedPackages[i].Reason = truncateUTF8String(trimmedPackages[i].Reason, maxStateSummaryMessageBytes)
	}
	return trimmedPackages
}

func truncateStateString(value string) string {
	return truncateUTF8String(value, maxStateStringBytes)
}

func truncateUTF8String(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	truncatedBytes := []byte(value[:limit])
	return strings.ToValidUTF8(string(truncatedBytes), "")
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

func stateDirectoryMutex(stateDirectory string) *sync.Mutex {
	absoluteDirectory, err := filepath.Abs(stateDirectory)
	if err != nil {
		absoluteDirectory = stateDirectory
	}
	lock, _ := stateDirectoryLocks.LoadOrStore(absoluteDirectory, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
