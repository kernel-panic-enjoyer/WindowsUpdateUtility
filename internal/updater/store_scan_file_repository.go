package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	storeScanSnapshotMaxBytes   = 16 << 20
	storeScanSnapshotDirName    = "store-scans"
	storeScanCurrentPointerName = "current.json"
	storeScanGenerationsDirName = "generations"
	storeScanSnapshotTimeLayout = "20060102T150405.000000000Z"

	storeScanCurrentPointerSchemaVersion = 1
)

type StoreScanFileRepository struct {
	root        string
	maxBytes    int64
	retention   int
	mu          *sync.Mutex
	diagnostics []string
	readFile    func(string) ([]byte, error)
	replaceFile func(string, string) error
}

type StoreScanCurrentPointer struct {
	SchemaVersion      int       `json:"schema_version"`
	GenerationFilename string    `json:"generation_filename"`
	ScanID             string    `json:"scan_id"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
	SHA256             string    `json:"sha256"`
}

type storeScanRepositoryErrorKind string

const (
	storeScanRepositoryErrorMalformedJSON             storeScanRepositoryErrorKind = "malformed_json"
	storeScanRepositoryErrorChecksumMismatch          storeScanRepositoryErrorKind = "checksum_mismatch"
	storeScanRepositoryErrorFutureSchema              storeScanRepositoryErrorKind = "unsupported_future_schema"
	storeScanRepositoryErrorPermission                storeScanRepositoryErrorKind = "permission_error"
	storeScanRepositoryErrorTransientIO               storeScanRepositoryErrorKind = "transient_io_error"
	storeScanRepositoryErrorWrongUser                 storeScanRepositoryErrorKind = "wrong_user"
	storeScanRepositoryErrorDuplicateScanID           storeScanRepositoryErrorKind = "duplicate_scan_id"
	storeScanRepositoryErrorMissingPointedGeneration  storeScanRepositoryErrorKind = "missing_pointed_generation"
	storeScanRepositoryErrorInvalidGenerationContents storeScanRepositoryErrorKind = "invalid_generation_contents"
	storeScanRepositoryErrorPointerGenerationMismatch storeScanRepositoryErrorKind = "pointer_generation_mismatch"
	storeScanRepositoryErrorPostCommitMaintenance     storeScanRepositoryErrorKind = "post_commit_maintenance"
)

type storeScanRepositoryError struct {
	Kind storeScanRepositoryErrorKind
	Path string
	Err  error
}

func (err storeScanRepositoryError) Error() string {
	if err.Err == nil {
		return string(err.Kind)
	}
	if err.Path == "" {
		return fmt.Sprintf("%s: %v", err.Kind, err.Err)
	}
	return fmt.Sprintf("%s: %s: %v", err.Kind, filepath.Base(err.Path), err.Err)
}

func (err storeScanRepositoryError) Unwrap() error {
	return err.Err
}

func (err storeScanRepositoryError) Is(target error) bool {
	other, ok := target.(storeScanRepositoryError)
	return ok && err.Kind == other.Kind
}

var (
	errStoreScanRepositoryMalformedJSON             = storeScanRepositoryError{Kind: storeScanRepositoryErrorMalformedJSON}
	errStoreScanRepositoryChecksumMismatch          = storeScanRepositoryError{Kind: storeScanRepositoryErrorChecksumMismatch}
	errStoreScanRepositoryFutureSchema              = storeScanRepositoryError{Kind: storeScanRepositoryErrorFutureSchema}
	errStoreScanRepositoryPermission                = storeScanRepositoryError{Kind: storeScanRepositoryErrorPermission}
	errStoreScanRepositoryTransientIO               = storeScanRepositoryError{Kind: storeScanRepositoryErrorTransientIO}
	errStoreScanRepositoryWrongUser                 = storeScanRepositoryError{Kind: storeScanRepositoryErrorWrongUser}
	errStoreScanRepositoryDuplicateScanID           = storeScanRepositoryError{Kind: storeScanRepositoryErrorDuplicateScanID}
	errStoreScanRepositoryMissingPointedGeneration  = storeScanRepositoryError{Kind: storeScanRepositoryErrorMissingPointedGeneration}
	errStoreScanRepositoryInvalidGenerationContents = storeScanRepositoryError{Kind: storeScanRepositoryErrorInvalidGenerationContents}
	errStoreScanRepositoryPointerMismatch           = storeScanRepositoryError{Kind: storeScanRepositoryErrorPointerGenerationMismatch}
	errStoreScanRepositoryPostCommitMaintenance     = storeScanRepositoryError{Kind: storeScanRepositoryErrorPostCommitMaintenance}
)

var storeScanRepositoryLocks sync.Map

func openDefaultStoreScanFileRepository() (*StoreScanFileRepository, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	return openStoreScanFileRepository(filepath.Join(dir, storeScanSnapshotDirName))
}

func openStoreScanFileRepository(root string) (*StoreScanFileRepository, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("Store scan file repository root is empty")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return nil, err
	}
	lockKey := strings.ToLower(filepath.Clean(absoluteRoot))
	lockAny, _ := storeScanRepositoryLocks.LoadOrStore(lockKey, &sync.Mutex{})
	return &StoreScanFileRepository{
		root:        absoluteRoot,
		maxBytes:    storeScanSnapshotMaxBytes,
		retention:   storeScanRetentionRunsUser,
		mu:          lockAny.(*sync.Mutex),
		readFile:    os.ReadFile,
		replaceFile: func(tempPath, targetPath string) error { return replaceFileKeepingBackup(tempPath, targetPath, "") },
	}, nil
}

func (repo *StoreScanFileRepository) PersistCompletedScanSnapshot(ctx context.Context, snapshot StoreScanSnapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if repo == nil {
		return false, errors.New("Store scan file repository is nil")
	}
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = storeScanSchemaVersion
	}
	if snapshot.SchemaVersion != storeScanSchemaVersion {
		return false, fmt.Errorf("unsupported Store scan snapshot schema version %d", snapshot.SchemaVersion)
	}
	if err := validateStoreScanSnapshot(snapshot); err != nil {
		return false, err
	}
	snapshot = snapshotForFilePersistence(snapshot)
	userDir := repo.userDir(snapshot.Scan.UserSID)
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return false, classifyStoreScanRepositoryIOError(err, userDir)
	}
	if err := os.MkdirAll(repo.generationsDir(snapshot.Scan.UserSID), 0o700); err != nil {
		return false, classifyStoreScanRepositoryIOError(err, repo.generationsDir(snapshot.Scan.UserSID))
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	_ = repo.migrateOldLayoutLocked(ctx, snapshot.Scan.UserSID)
	existing, err := repo.loadUsableSnapshotsLocked(ctx, snapshot.Scan.UserSID)
	if err != nil {
		return false, err
	}
	for _, candidate := range existing {
		if candidate.Scan.ScanID == snapshot.Scan.ScanID {
			return false, storeScanRepositoryError{Kind: storeScanRepositoryErrorDuplicateScanID, Err: fmt.Errorf("Store scan snapshot already exists for scan ID %s", snapshot.Scan.ScanID)}
		}
	}

	published := false
	if snapshot.Published {
		latest, ok, latestErr := repo.loadLatestPublishedSnapshotLocked(ctx, snapshot.Scan.UserSID)
		if latestErr != nil && !errors.Is(latestErr, errStoreScanRepositoryMissingPointedGeneration) && !errors.Is(latestErr, errStoreScanRepositoryMalformedJSON) && !errors.Is(latestErr, errStoreScanRepositoryChecksumMismatch) {
			return false, latestErr
		}
		published = !ok || snapshotSortsAfter(snapshot, latest)
	}
	snapshot.Published = published
	data, err := marshalStoreScanSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	if int64(len(data)) > repo.snapshotMaxBytes() {
		return false, fmt.Errorf("Store scan snapshot exceeds size limit: %d bytes", len(data))
	}
	finalPath := repo.snapshotPath(snapshot)
	if _, err := os.Stat(finalPath); err == nil {
		return false, storeScanRepositoryError{Kind: storeScanRepositoryErrorDuplicateScanID, Path: finalPath, Err: fmt.Errorf("Store scan snapshot already exists for scan ID %s", snapshot.Scan.ScanID)}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, classifyStoreScanRepositoryIOError(err, finalPath)
	}
	if err := repo.writeSnapshotAtomically(repo.generationsDir(snapshot.Scan.UserSID), finalPath, data); err != nil {
		return false, err
	}
	if published {
		pointer := StoreScanCurrentPointer{
			SchemaVersion:      storeScanCurrentPointerSchemaVersion,
			GenerationFilename: filepath.Base(finalPath),
			ScanID:             snapshot.Scan.ScanID,
			StartedAt:          snapshot.Scan.StartedAt,
			CompletedAt:        snapshot.Scan.CompletedAt,
			SHA256:             storeScanSHA256Hex(data),
		}
		if err := repo.writeCurrentPointerAtomically(userDir, repo.currentPointerPath(snapshot.Scan.UserSID), pointer); err != nil {
			if downgradeErr := repo.markGenerationUnpublished(finalPath, snapshot); downgradeErr != nil {
				repo.recordDiagnostic("Store scan generation could not be marked uncommitted after pointer failure: %s", downgradeErr)
			}
			return false, err
		}
	}
	if err := repo.pruneLocked(ctx, snapshot.Scan.UserSID); err != nil {
		repo.recordDiagnostic("Store scan retention maintenance failed after commit: %s", err)
		return published, nil
	}
	return published, nil
}

func (repo *StoreScanFileRepository) LoadLatestPublishedSnapshot(ctx context.Context, userSID string) (StoreScanSnapshot, bool, error) {
	if repo == nil {
		return StoreScanSnapshot{}, false, errors.New("Store scan file repository is nil")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.loadLatestPublishedSnapshotLocked(ctx, userSID)
}

func (repo *StoreScanFileRepository) LoadPreviousSnapshot(ctx context.Context, userSID string, before StoreScanGeneration) (StoreScanSnapshot, bool, error) {
	if repo == nil {
		return StoreScanSnapshot{}, false, errors.New("Store scan file repository is nil")
	}
	if strings.TrimSpace(userSID) == "" || before.StartedAt.IsZero() {
		return StoreScanSnapshot{}, false, nil
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	snapshots, err := repo.loadUsableSnapshotsLocked(ctx, userSID)
	if err != nil {
		return StoreScanSnapshot{}, false, err
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshotSortsAfter(snapshots[i], snapshots[j])
	})
	for _, snapshot := range snapshots {
		if snapshot.Scan.StartedAt.Before(before.StartedAt) {
			return snapshot, true, nil
		}
	}
	return StoreScanSnapshot{}, false, nil
}

func (repo *StoreScanFileRepository) Close() error {
	return nil
}

func (repo *StoreScanFileRepository) Diagnostics() []string {
	if repo == nil {
		return nil
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	out := make([]string, len(repo.diagnostics))
	copy(out, repo.diagnostics)
	return out
}

func (repo *StoreScanFileRepository) loadLatestPublishedSnapshotLocked(ctx context.Context, userSID string) (StoreScanSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return StoreScanSnapshot{}, false, err
	}
	pointer, pointerErr := repo.readCurrentPointer(userSID)
	if pointerErr == nil {
		snapshot, err := repo.readPointedGeneration(ctx, userSID, pointer)
		if err == nil {
			return snapshot, true, nil
		}
		if errors.Is(err, errStoreScanRepositoryFutureSchema) || errors.Is(err, errStoreScanRepositoryPermission) || errors.Is(err, errStoreScanRepositoryTransientIO) || errors.Is(err, errStoreScanRepositoryWrongUser) {
			return StoreScanSnapshot{}, false, err
		}
		repo.recordDiagnostic("Store current pointer could not be used: %s", err)
		recovered, ok, recoverErr := repo.recoverLatestPublishedSnapshotLocked(ctx, userSID)
		if recoverErr != nil || !ok {
			return StoreScanSnapshot{}, ok, recoverErr
		}
		recovered.RecoveredFromFallback = true
		return recovered, true, nil
	}
	if errors.Is(pointerErr, os.ErrNotExist) {
		if err := repo.migrateOldLayoutLocked(ctx, userSID); err != nil {
			return StoreScanSnapshot{}, false, err
		}
		pointer, pointerErr = repo.readCurrentPointer(userSID)
		if errors.Is(pointerErr, os.ErrNotExist) {
			recovered, ok, recoverErr := repo.recoverLatestPublishedSnapshotLocked(ctx, userSID)
			if recoverErr != nil || !ok {
				return StoreScanSnapshot{}, ok, recoverErr
			}
			recovered.RecoveredFromFallback = true
			return recovered, true, nil
		}
		if pointerErr == nil {
			snapshot, err := repo.readPointedGeneration(ctx, userSID, pointer)
			if err != nil {
				return StoreScanSnapshot{}, false, err
			}
			return snapshot, true, nil
		}
	}
	if errors.Is(pointerErr, errStoreScanRepositoryMalformedJSON) || errors.Is(pointerErr, errStoreScanRepositoryChecksumMismatch) || errors.Is(pointerErr, errStoreScanRepositoryMissingPointedGeneration) {
		repo.recordDiagnostic("Store current pointer rejected: %s", pointerErr)
		recovered, ok, recoverErr := repo.recoverLatestPublishedSnapshotLocked(ctx, userSID)
		if recoverErr != nil || !ok {
			return StoreScanSnapshot{}, ok, recoverErr
		}
		recovered.RecoveredFromFallback = true
		return recovered, true, nil
	}
	return StoreScanSnapshot{}, false, pointerErr
}

func (repo *StoreScanFileRepository) recoverLatestPublishedSnapshotLocked(ctx context.Context, userSID string) (StoreScanSnapshot, bool, error) {
	snapshots, err := repo.loadUsableSnapshotsLocked(ctx, userSID)
	if err != nil {
		return StoreScanSnapshot{}, false, err
	}
	var latest StoreScanSnapshot
	found := false
	for _, snapshot := range snapshots {
		if !snapshot.Published {
			continue
		}
		if !found || snapshotSortsAfter(snapshot, latest) {
			latest = snapshot
			found = true
		}
	}
	return latest, found, nil
}

func (repo *StoreScanFileRepository) readCurrentPointer(userSID string) (StoreScanCurrentPointer, error) {
	path := repo.currentPointerPath(userSID)
	data, err := repo.readFileFunc()(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoreScanCurrentPointer{}, os.ErrNotExist
		}
		return StoreScanCurrentPointer{}, classifyStoreScanRepositoryIOError(err, path)
	}
	var pointer StoreScanCurrentPointer
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pointer); err != nil {
		return StoreScanCurrentPointer{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorMalformedJSON, Path: path, Err: err}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("current pointer contains trailing JSON data")
		}
		return StoreScanCurrentPointer{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorMalformedJSON, Path: path, Err: err}
	}
	if pointer.SchemaVersion != storeScanCurrentPointerSchemaVersion || pointer.GenerationFilename == "" || pointer.ScanID == "" || pointer.StartedAt.IsZero() || pointer.SHA256 == "" {
		return StoreScanCurrentPointer{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorMalformedJSON, Path: path, Err: errors.New("current pointer is incomplete or unsupported")}
	}
	return pointer, nil
}

func (repo *StoreScanFileRepository) readPointedGeneration(ctx context.Context, userSID string, pointer StoreScanCurrentPointer) (StoreScanSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return StoreScanSnapshot{}, err
	}
	path, err := repo.safeGenerationPath(userSID, pointer.GenerationFilename)
	if err != nil {
		return StoreScanSnapshot{}, err
	}
	data, err := repo.readFileFunc()(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorMissingPointedGeneration, Path: path, Err: err}
		}
		return StoreScanSnapshot{}, classifyStoreScanRepositoryIOError(err, path)
	}
	if !strings.EqualFold(storeScanSHA256Hex(data), pointer.SHA256) {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorChecksumMismatch, Path: path, Err: errors.New("current pointer checksum does not match generation")}
	}
	snapshot, err := repo.decodeSnapshotData(data, path, userSID)
	if err != nil {
		return StoreScanSnapshot{}, err
	}
	if snapshot.Scan.ScanID != pointer.ScanID || !snapshot.Scan.StartedAt.Equal(pointer.StartedAt) || snapshot.Scan.UserSID != userSID {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorPointerGenerationMismatch, Path: path, Err: errors.New("current pointer metadata does not match generation")}
	}
	if !snapshot.Published {
		snapshot.Published = true
	}
	return snapshot, nil
}

func (repo *StoreScanFileRepository) loadUsableSnapshotsLocked(ctx context.Context, userSID string) ([]StoreScanSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userSID = strings.TrimSpace(userSID)
	if userSID == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(repo.generationsDir(userSID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, classifyStoreScanRepositoryIOError(err, repo.generationsDir(userSID))
	}
	snapshots := make([]StoreScanSnapshot, 0, len(entries))
	rejectedStartedAt := make([]time.Time, 0)
	seenScanIDs := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lowerName := strings.ToLower(name)
		if !strings.HasSuffix(lowerName, ".json") {
			if strings.Contains(lowerName, ".json.corrupt.") {
				if startedAt, ok := snapshotStartedAtFromFileName(name); ok {
					rejectedStartedAt = append(rejectedStartedAt, startedAt)
				}
			}
			continue
		}
		path := filepath.Join(repo.generationsDir(userSID), name)
		snapshot, err := repo.readSnapshotFile(path, userSID)
		if err != nil {
			if startedAt, ok := snapshotStartedAtFromFileName(name); ok {
				rejectedStartedAt = append(rejectedStartedAt, startedAt)
			}
			repo.recordDiagnostic("Store snapshot rejected %s: %s", name, err)
			if storeScanRepositoryErrorAllowsQuarantine(err) {
				_ = repo.quarantineSnapshot(path)
			}
			continue
		}
		if previousPath, ok := seenScanIDs[snapshot.Scan.ScanID]; ok {
			repo.recordDiagnostic("Store snapshot duplicate scan ID %s in %s and %s", snapshot.Scan.ScanID, filepath.Base(previousPath), name)
			continue
		}
		seenScanIDs[snapshot.Scan.ScanID] = path
		snapshots = append(snapshots, snapshot)
	}
	markRecoveredFallbackSnapshots(snapshots, rejectedStartedAt)
	return snapshots, nil
}

func (repo *StoreScanFileRepository) readSnapshotFile(path, expectedUserSID string) (StoreScanSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return StoreScanSnapshot{}, classifyStoreScanRepositoryIOError(err, path)
	}
	if info.Size() > repo.snapshotMaxBytes() {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorInvalidGenerationContents, Path: path, Err: fmt.Errorf("snapshot exceeds size limit: %d bytes", info.Size())}
	}
	data, err := repo.readFileFunc()(path)
	if err != nil {
		return StoreScanSnapshot{}, classifyStoreScanRepositoryIOError(err, path)
	}
	if int64(len(data)) > repo.snapshotMaxBytes() {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorInvalidGenerationContents, Path: path, Err: fmt.Errorf("snapshot exceeds size limit: %d bytes", len(data))}
	}
	return repo.decodeSnapshotData(data, path, expectedUserSID)
}

func (repo *StoreScanFileRepository) decodeSnapshotData(data []byte, path, expectedUserSID string) (StoreScanSnapshot, error) {
	snapshot, err := decodeStoreScanSnapshot(data)
	if err != nil {
		if errors.Is(err, errStoreScanRepositoryFutureSchema) {
			return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorFutureSchema, Path: path, Err: err}
		}
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorMalformedJSON, Path: path, Err: err}
	}
	if snapshot.Scan.UserSID != expectedUserSID {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorWrongUser, Path: path, Err: errors.New("snapshot belongs to a different user")}
	}
	if err := validateStoreScanSnapshot(snapshot); err != nil {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorInvalidGenerationContents, Path: path, Err: err}
	}
	snapshot.Scan.ProviderHealth = providerHealthMap(snapshot.ProviderRuns)
	snapshot.Scan.ProviderVersions = providerVersionMap(snapshot.ProviderRuns)
	snapshot.Inventory.Scan = snapshot.Scan
	sortStoreScanSnapshot(&snapshot)
	return snapshot, nil
}

func decodeStoreScanSnapshot(data []byte) (StoreScanSnapshot, error) {
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return StoreScanSnapshot{}, err
	}
	if envelope.SchemaVersion == 0 {
		return StoreScanSnapshot{}, errors.New("snapshot is missing schema version")
	}
	if envelope.SchemaVersion > storeScanSchemaVersion {
		return StoreScanSnapshot{}, storeScanRepositoryError{Kind: storeScanRepositoryErrorFutureSchema, Err: fmt.Errorf("unsupported future Store scan snapshot schema version %d", envelope.SchemaVersion)}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot StoreScanSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return StoreScanSnapshot{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("snapshot contains trailing JSON data")
		}
		return StoreScanSnapshot{}, err
	}
	if snapshot.SchemaVersion < storeScanSchemaVersion {
		migrated, err := migrateStoreScanSnapshot(snapshot)
		if err != nil {
			return StoreScanSnapshot{}, err
		}
		snapshot = migrated
	}
	return snapshot, nil
}

func migrateStoreScanSnapshot(snapshot StoreScanSnapshot) (StoreScanSnapshot, error) {
	switch snapshot.SchemaVersion {
	case 1:
		snapshot.SchemaVersion = storeScanSchemaVersion
		return snapshot, nil
	default:
		return StoreScanSnapshot{}, fmt.Errorf("unsupported Store scan snapshot schema version %d", snapshot.SchemaVersion)
	}
}

func marshalStoreScanSnapshot(snapshot StoreScanSnapshot) ([]byte, error) {
	sortStoreScanSnapshot(&snapshot)
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func snapshotForFilePersistence(snapshot StoreScanSnapshot) StoreScanSnapshot {
	sortStoreScanSnapshot(&snapshot)
	for index := range snapshot.Inventory.Errors {
		snapshot.Inventory.Errors[index] = sanitizeProviderDiagnostic(snapshot.Inventory.Errors[index])
	}
	for runIndex := range snapshot.ProviderRuns {
		run := &snapshot.ProviderRuns[runIndex]
		run.Error = sanitizeProviderDiagnostic(run.Error)
		for mappingIndex := range run.Mappings {
			run.Mappings[mappingIndex].Evidence = sanitizeProviderDiagnostic(run.Mappings[mappingIndex].Evidence)
		}
		for observationIndex := range run.Observations {
			run.Observations[observationIndex].Diagnostics = sanitizeProviderDiagnostic(run.Observations[observationIndex].Diagnostics)
		}
	}
	for assessmentIndex := range snapshot.Assessments {
		assessment := &snapshot.Assessments[assessmentIndex]
		assessment.Reason = sanitizeProviderDiagnostic(assessment.Reason)
		if assessment.Target != nil {
			assessment.StoreProductID = firstNonEmpty(assessment.StoreProductID, assessment.Target.ProductID)
			assessment.UpdateID = firstNonEmpty(assessment.UpdateID, assessment.Target.UpdateID)
		}
	}
	return snapshot
}

func (repo *StoreScanFileRepository) writeSnapshotAtomically(dir, finalPath string, data []byte) error {
	temp, err := os.CreateTemp(dir, ".tmp-store-scan-*.json")
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
	if _, err := os.Stat(finalPath); err == nil {
		return fmt.Errorf("Store scan snapshot already exists: %s", filepath.Base(finalPath))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return classifyStoreScanRepositoryIOError(err, finalPath)
	}
	cleanup = false
	return nil
}

func (repo *StoreScanFileRepository) writeCurrentPointerAtomically(dir, finalPath string, pointer StoreScanCurrentPointer) error {
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".tmp-store-current-*.json")
	if err != nil {
		return classifyStoreScanRepositoryIOError(err, dir)
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
		return classifyStoreScanRepositoryIOError(err, tempPath)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return classifyStoreScanRepositoryIOError(err, tempPath)
	}
	if err := temp.Close(); err != nil {
		return classifyStoreScanRepositoryIOError(err, tempPath)
	}
	if err := repo.replaceFileFunc()(tempPath, finalPath); err != nil {
		return classifyStoreScanRepositoryIOError(err, finalPath)
	}
	cleanup = false
	return nil
}

func (repo *StoreScanFileRepository) markGenerationUnpublished(path string, snapshot StoreScanSnapshot) error {
	snapshot.Published = false
	data, err := marshalStoreScanSnapshot(snapshot)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (repo *StoreScanFileRepository) pruneLocked(ctx context.Context, userSID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshots, err := repo.loadUsableSnapshotsLocked(ctx, userSID)
	if err != nil {
		return err
	}
	if len(snapshots) <= repo.retentionLimit() {
		return nil
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshotSortsAfter(snapshots[i], snapshots[j])
	})
	keep := map[string]bool{}
	for index, snapshot := range snapshots {
		if index < repo.retentionLimit() {
			keep[snapshot.Scan.ScanID] = true
		}
	}
	if current, ok, err := repo.loadLatestPublishedSnapshotLocked(ctx, userSID); err == nil && ok {
		keep[current.Scan.ScanID] = true
	} else if latest, found := latestPublishedSnapshot(snapshots); found {
		keep[latest.Scan.ScanID] = true
	}
	for _, snapshot := range snapshots {
		if keep[snapshot.Scan.ScanID] {
			continue
		}
		if err := os.Remove(repo.snapshotPath(snapshot)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (repo *StoreScanFileRepository) quarantineSnapshot(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	target := fmt.Sprintf("%s.corrupt.%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	return os.Rename(path, target)
}

func (repo *StoreScanFileRepository) snapshotPath(snapshot StoreScanSnapshot) string {
	return filepath.Join(repo.generationsDir(snapshot.Scan.UserSID), snapshotFileName(snapshot))
}

func (repo *StoreScanFileRepository) userDir(userSID string) string {
	return filepath.Join(repo.root, userScopeHash(userSID))
}

func (repo *StoreScanFileRepository) generationsDir(userSID string) string {
	return filepath.Join(repo.userDir(userSID), storeScanGenerationsDirName)
}

func (repo *StoreScanFileRepository) currentPointerPath(userSID string) string {
	return filepath.Join(repo.userDir(userSID), storeScanCurrentPointerName)
}

func (repo *StoreScanFileRepository) oldSnapshotPath(snapshot StoreScanSnapshot) string {
	return filepath.Join(repo.userDir(snapshot.Scan.UserSID), snapshotFileName(snapshot))
}

func (repo *StoreScanFileRepository) safeGenerationPath(userSID, filename string) (string, error) {
	if filename != filepath.Base(filename) || filename == "" || strings.Contains(filename, string(filepath.Separator)) {
		return "", storeScanRepositoryError{Kind: storeScanRepositoryErrorPointerGenerationMismatch, Err: errors.New("current pointer generation filename is not local")}
	}
	generationDir := filepath.Clean(repo.generationsDir(userSID))
	path := filepath.Clean(filepath.Join(generationDir, filename))
	relative, err := filepath.Rel(generationDir, path)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		return "", storeScanRepositoryError{Kind: storeScanRepositoryErrorPointerGenerationMismatch, Path: path, Err: errors.New("current pointer escapes generation directory")}
	}
	return path, nil
}

func (repo *StoreScanFileRepository) readFileFunc() func(string) ([]byte, error) {
	if repo != nil && repo.readFile != nil {
		return repo.readFile
	}
	return os.ReadFile
}

func (repo *StoreScanFileRepository) replaceFileFunc() func(string, string) error {
	if repo != nil && repo.replaceFile != nil {
		return repo.replaceFile
	}
	return func(tempPath, targetPath string) error { return replaceFileKeepingBackup(tempPath, targetPath, "") }
}

func (repo *StoreScanFileRepository) snapshotMaxBytes() int64 {
	if repo != nil && repo.maxBytes > 0 {
		return repo.maxBytes
	}
	return storeScanSnapshotMaxBytes
}

func (repo *StoreScanFileRepository) retentionLimit() int {
	if repo != nil && repo.retention > 0 {
		return repo.retention
	}
	return storeScanRetentionRunsUser
}

func (repo *StoreScanFileRepository) recordDiagnostic(format string, args ...any) {
	if repo == nil {
		return
	}
	message := sanitizeProviderDiagnostic(fmt.Sprintf(format, args...))
	repo.diagnostics = append(repo.diagnostics, message)
	if len(repo.diagnostics) > 100 {
		repo.diagnostics = repo.diagnostics[len(repo.diagnostics)-100:]
	}
	appLog("%s", message)
}

func (repo *StoreScanFileRepository) migrateOldLayoutLocked(ctx context.Context, userSID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(repo.currentPointerPath(userSID)); err == nil {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return classifyStoreScanRepositoryIOError(err, repo.currentPointerPath(userSID))
	}
	entries, err := os.ReadDir(repo.userDir(userSID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return classifyStoreScanRepositoryIOError(err, repo.userDir(userSID))
	}
	if err := os.MkdirAll(repo.generationsDir(userSID), 0o700); err != nil {
		return classifyStoreScanRepositoryIOError(err, repo.generationsDir(userSID))
	}
	var snapshots []StoreScanSnapshot
	type migratedGeneration struct {
		snapshot StoreScanSnapshot
		data     []byte
	}
	generationsByScanID := map[string]migratedGeneration{}
	for _, entry := range entries {
		if entry.IsDir() || strings.EqualFold(entry.Name(), storeScanCurrentPointerName) || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(repo.userDir(userSID), entry.Name())
		data, readErr := repo.readFileFunc()(path)
		if readErr != nil {
			repo.recordDiagnostic("Store old-layout snapshot could not be read %s: %s", entry.Name(), readErr)
			continue
		}
		snapshot, decodeErr := repo.decodeSnapshotData(data, path, userSID)
		if decodeErr != nil {
			repo.recordDiagnostic("Store old-layout snapshot rejected %s: %s", entry.Name(), decodeErr)
			continue
		}
		finalPath := repo.snapshotPath(snapshot)
		if _, err := os.Stat(finalPath); errors.Is(err, os.ErrNotExist) {
			if err := repo.writeSnapshotAtomically(repo.generationsDir(userSID), finalPath, data); err != nil {
				return err
			}
		} else if err != nil {
			return classifyStoreScanRepositoryIOError(err, finalPath)
		}
		snapshots = append(snapshots, snapshot)
		generationsByScanID[snapshot.Scan.ScanID] = migratedGeneration{snapshot: snapshot, data: data}
	}
	latest, found := latestPublishedSnapshot(snapshots)
	if !found {
		return nil
	}
	if latest.RecoveredFromFallback {
		return nil
	}
	generation := generationsByScanID[latest.Scan.ScanID]
	if len(generation.data) == 0 {
		data, err := repo.readFileFunc()(repo.snapshotPath(latest))
		if err != nil {
			return classifyStoreScanRepositoryIOError(err, repo.snapshotPath(latest))
		}
		generation.data = data
	}
	pointer := StoreScanCurrentPointer{
		SchemaVersion:      storeScanCurrentPointerSchemaVersion,
		GenerationFilename: filepath.Base(repo.snapshotPath(latest)),
		ScanID:             latest.Scan.ScanID,
		StartedAt:          latest.Scan.StartedAt,
		CompletedAt:        latest.Scan.CompletedAt,
		SHA256:             storeScanSHA256Hex(generation.data),
	}
	return repo.writeCurrentPointerAtomically(repo.userDir(userSID), repo.currentPointerPath(userSID), pointer)
}

func classifyStoreScanRepositoryIOError(err error, path string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errStoreScanRepositoryPermission) ||
		errors.Is(err, errStoreScanRepositoryTransientIO) ||
		errors.Is(err, errStoreScanRepositoryMalformedJSON) ||
		errors.Is(err, errStoreScanRepositoryChecksumMismatch) ||
		errors.Is(err, errStoreScanRepositoryFutureSchema) ||
		errors.Is(err, errStoreScanRepositoryWrongUser) ||
		errors.Is(err, errStoreScanRepositoryDuplicateScanID) ||
		errors.Is(err, errStoreScanRepositoryMissingPointedGeneration) ||
		errors.Is(err, errStoreScanRepositoryInvalidGenerationContents) ||
		errors.Is(err, errStoreScanRepositoryPointerMismatch) ||
		errors.Is(err, errStoreScanRepositoryPostCommitMaintenance) {
		return err
	}
	if errors.Is(err, os.ErrPermission) {
		return storeScanRepositoryError{Kind: storeScanRepositoryErrorPermission, Path: path, Err: err}
	}
	return storeScanRepositoryError{Kind: storeScanRepositoryErrorTransientIO, Path: path, Err: err}
}

func storeScanRepositoryErrorAllowsQuarantine(err error) bool {
	return errors.Is(err, errStoreScanRepositoryMalformedJSON) || errors.Is(err, errStoreScanRepositoryChecksumMismatch)
}

func storeScanSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func snapshotFileName(snapshot StoreScanSnapshot) string {
	started := snapshot.Scan.StartedAt.UTC().Format(storeScanSnapshotTimeLayout)
	return started + "-" + shortHash(snapshot.Scan.ScanID) + ".json"
}

func snapshotStartedAtFromFileName(name string) (time.Time, bool) {
	lowerName := strings.ToLower(name)
	jsonIndex := strings.Index(lowerName, ".json")
	if jsonIndex < len(storeScanSnapshotTimeLayout) {
		return time.Time{}, false
	}
	startedAt, err := time.Parse(storeScanSnapshotTimeLayout, name[:len(storeScanSnapshotTimeLayout)])
	if err != nil {
		return time.Time{}, false
	}
	return startedAt, true
}

func markRecoveredFallbackSnapshots(snapshots []StoreScanSnapshot, rejectedStartedAt []time.Time) {
	if len(snapshots) == 0 || len(rejectedStartedAt) == 0 {
		return
	}
	futureCutoff := storeScanNow().Add(5 * time.Minute)
	for snapshotIndex := range snapshots {
		for _, rejected := range rejectedStartedAt {
			if rejected.After(futureCutoff) {
				continue
			}
			if rejected.After(snapshots[snapshotIndex].Scan.StartedAt) || rejected.Equal(snapshots[snapshotIndex].Scan.StartedAt) {
				snapshots[snapshotIndex].RecoveredFromFallback = true
				break
			}
		}
	}
}

func userScopeHash(userSID string) string {
	return "user-" + shortHash(userSID)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:24]
}

func snapshotSortsAfter(left, right StoreScanSnapshot) bool {
	if !left.Scan.StartedAt.Equal(right.Scan.StartedAt) {
		return left.Scan.StartedAt.After(right.Scan.StartedAt)
	}
	return left.Scan.ScanID > right.Scan.ScanID
}

func latestPublishedSnapshot(snapshots []StoreScanSnapshot) (StoreScanSnapshot, bool) {
	var latest StoreScanSnapshot
	found := false
	for _, snapshot := range snapshots {
		if !snapshot.Published {
			continue
		}
		if !found || snapshotSortsAfter(snapshot, latest) {
			latest = snapshot
			found = true
		}
	}
	return latest, found
}
