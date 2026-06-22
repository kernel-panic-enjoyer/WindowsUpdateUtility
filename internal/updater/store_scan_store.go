package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	storeScanSchemaVersion     = 2
	storeScanRetentionRunsUser = 50
)

type StoreScanStore struct {
	db   *sql.DB
	path string
}

type StorePublishedAssessment struct {
	StoreUpdateAssessment
	ObservedAt                 time.Time
	Stale                      bool
	StoreProductID             string
	UpdateID                   string
	ExactActionTargetAvailable bool
	Applicability              string
}

func defaultStoreScanDBPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store-scans.sqlite"), nil
}

func openDefaultStoreScanStore() (*StoreScanStore, error) {
	path, err := defaultStoreScanDBPath()
	if err != nil {
		return nil, err
	}
	return openStoreScanStore(path)
}

func openStoreScanStore(path string) (*StoreScanStore, error) {
	store, err := openStoreScanStoreOnce(path)
	if err == nil {
		return store, nil
	}
	if recoverErr := recoverCorruptStoreScanDB(path); recoverErr != nil {
		return nil, err
	}
	return openStoreScanStoreOnce(path)
}

func openStoreScanStoreOnce(path string) (*StoreScanStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &StoreScanStore{db: db, path: path}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func recoverCorruptStoreScanDB(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.corrupt.%s", path, time.Now().UTC().Format("20060102T150405"))
	return os.Rename(path, backup)
}

func (store *StoreScanStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *StoreScanStore) migrate(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	row := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err := row.Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		if err := applyStoreScanMigration1(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 1, formatStoreScanTime(time.Now().UTC())); err != nil {
			return err
		}
		version = 1
	}
	if version < 2 {
		if err := applyStoreScanMigration2(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, 2, formatStoreScanTime(time.Now().UTC())); err != nil {
			return err
		}
	}
	if err := migrateJSONStoreAssessmentCache(ctx, tx, loadState()); err != nil {
		return err
	}
	return tx.Commit()
}

func applyStoreScanMigration1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS scan_runs(
			scan_id TEXT PRIMARY KEY,
			user_sid TEXT NOT NULL,
			started_at TEXT NOT NULL,
			completed_at TEXT,
			windows_version TEXT,
			windows_build TEXT,
			architecture TEXT,
			status TEXT NOT NULL,
			published INTEGER NOT NULL DEFAULT 0,
			published_at TEXT
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS scan_runs_published_user ON scan_runs(user_sid, published, started_at, scan_id)`,
		`CREATE TABLE IF NOT EXISTS installed_package_families(
			scan_id TEXT NOT NULL,
			user_sid TEXT NOT NULL,
			package_family_name TEXT NOT NULL,
			display_name TEXT,
			product_like INTEGER NOT NULL,
			PRIMARY KEY(scan_id, user_sid, package_family_name),
			FOREIGN KEY(scan_id) REFERENCES scan_runs(scan_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS installed_package_instances(
			scan_id TEXT NOT NULL,
			user_sid TEXT NOT NULL,
			package_family_name TEXT NOT NULL,
			package_full_name TEXT NOT NULL,
			identity_name TEXT NOT NULL,
			version TEXT,
			processor_architecture TEXT,
			install_location TEXT,
			package_type TEXT,
			classification TEXT,
			status_json TEXT,
			PRIMARY KEY(scan_id, user_sid, package_family_name, package_full_name),
			FOREIGN KEY(scan_id, user_sid, package_family_name) REFERENCES installed_package_families(scan_id, user_sid, package_family_name) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS verified_identity_mappings(
			user_sid TEXT NOT NULL,
			package_family_name TEXT NOT NULL,
			product_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			scan_id TEXT NOT NULL,
			verified_at TEXT NOT NULL,
			evidence TEXT,
			PRIMARY KEY(user_sid, package_family_name, product_id, provider_id)
		)`,
		`CREATE TABLE IF NOT EXISTS provider_runs(
			scan_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			provider_name TEXT,
			backend TEXT,
			started_at TEXT NOT NULL,
			completed_at TEXT,
			health TEXT NOT NULL,
			error TEXT,
			PRIMARY KEY(scan_id, provider_id),
			FOREIGN KEY(scan_id) REFERENCES scan_runs(scan_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS provider_observations(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scan_id TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			user_sid TEXT NOT NULL,
			package_family_name TEXT NOT NULL,
			kind TEXT NOT NULL,
			health TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			installed_version TEXT,
			available_version TEXT,
			catalog_version TEXT,
			product_id TEXT,
			update_id TEXT,
			target_verified INTEGER NOT NULL DEFAULT 0,
			diagnostics TEXT,
			FOREIGN KEY(scan_id) REFERENCES scan_runs(scan_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS provider_observations_identity ON provider_observations(scan_id, user_sid, package_family_name, provider_id)`,
		`CREATE TABLE IF NOT EXISTS update_assessments(
			scan_id TEXT NOT NULL,
			user_sid TEXT NOT NULL,
			package_family_name TEXT NOT NULL,
			state TEXT NOT NULL,
			reason TEXT,
			installed_version TEXT,
			available_version TEXT,
			stale INTEGER NOT NULL DEFAULT 0,
			product_id TEXT,
			update_id TEXT,
			exact_action_target_available INTEGER NOT NULL DEFAULT 0,
			applicability TEXT,
			observed_at TEXT NOT NULL,
			evidence_json TEXT,
			PRIMARY KEY(scan_id, user_sid, package_family_name),
			FOREIGN KEY(scan_id) REFERENCES scan_runs(scan_id) ON DELETE CASCADE
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applyStoreScanMigration2(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE provider_runs ADD COLUMN provider_version TEXT`)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

func migrateJSONStoreAssessmentCache(ctx context.Context, tx *sql.Tx, state State) error {
	if len(state.StoreUpdateAssessmentCache) == 0 {
		return nil
	}
	for _, entry := range state.StoreUpdateAssessmentCache {
		if entry.UserSID == "" || entry.PackageFamilyName == "" || entry.State != string(StoreUpdateAvailable) || !entry.ExactActionTargetAvailable {
			continue
		}
		scanID := firstNonEmpty(entry.ScanID, "json-state-migration")
		observedAt := firstNonEmpty(entry.ObservedAt, utcNow())
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO scan_runs(scan_id, user_sid, started_at, completed_at, status, published, published_at) VALUES(?, ?, ?, ?, ?, 0, NULL)`,
			scanID, entry.UserSID, observedAt, observedAt, string(StoreScanCompleted)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO update_assessments(scan_id, user_sid, package_family_name, state, reason, installed_version, available_version, stale, product_id, exact_action_target_available, applicability, observed_at, evidence_json) VALUES(?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			scanID, entry.UserSID, entry.PackageFamilyName, entry.State, entry.Reason, entry.InstalledVersion, entry.OfferedVersion, entry.StoreProductID, boolToInt(entry.ExactActionTargetAvailable), entry.Applicability, observedAt, `[]`); err != nil {
			return err
		}
	}
	return nil
}

type storeScanPersistInput struct {
	Scan         StoreScanGeneration
	Inventory    StorePackagedAppInventory
	ProviderRuns []StoreCatalogProviderRun
	Assessments  []StorePublishedAssessment
	Publish      bool
}

func (store *StoreScanStore) PersistScan(ctx context.Context, input storeScanPersistInput) (bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.persistScanTx(ctx, tx, input); err != nil {
		return false, err
	}
	published := false
	if input.Publish {
		allowed, err := store.publishScanTx(ctx, tx, input.Scan)
		if err != nil {
			return false, err
		}
		published = allowed
	}
	if err := store.pruneOldScanRunsTx(ctx, tx, input.Scan.UserSID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return published, nil
}

func (store *StoreScanStore) persistScanTx(ctx context.Context, tx *sql.Tx, input storeScanPersistInput) error {
	completedAt := ""
	if !input.Scan.CompletedAt.IsZero() {
		completedAt = formatStoreScanTime(input.Scan.CompletedAt)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scan_runs(scan_id, user_sid, started_at, completed_at, windows_version, windows_build, architecture, status, published, published_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0, NULL)
		ON CONFLICT(scan_id) DO UPDATE SET
			user_sid = excluded.user_sid,
			started_at = excluded.started_at,
			completed_at = excluded.completed_at,
			windows_version = excluded.windows_version,
			windows_build = excluded.windows_build,
			architecture = excluded.architecture,
			status = excluded.status`,
		input.Scan.ScanID, input.Scan.UserSID, formatStoreScanTime(input.Scan.StartedAt), completedAt, input.Scan.WindowsVersion, input.Scan.WindowsBuild, input.Scan.Architecture, string(input.Scan.CompletionStatus)); err != nil {
		return err
	}
	for _, family := range input.Inventory.Families {
		if !family.Identity.Resolved() || family.Identity.UserSID != input.Scan.UserSID {
			return fmt.Errorf("inventory family belongs to wrong user or is unresolved: %s", family.Identity.PackageFamilyName)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO installed_package_families(scan_id, user_sid, package_family_name, display_name, product_like) VALUES(?, ?, ?, ?, ?)`,
			input.Scan.ScanID, family.Identity.UserSID, family.Identity.PackageFamilyName, family.DisplayName, boolToInt(family.ProductLike)); err != nil {
			return err
		}
		for _, instance := range family.Instances {
			statusJSON, err := json.Marshal(instance.Status)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO installed_package_instances(scan_id, user_sid, package_family_name, package_full_name, identity_name, version, processor_architecture, install_location, package_type, classification, status_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				input.Scan.ScanID, instance.UserSID, instance.PackageFamilyName, instance.PackageFullName, instance.IdentityName, instance.Version.String(), instance.ProcessorArchitecture, instance.InstallLocation, instance.PackageType, instance.Classification, string(statusJSON)); err != nil {
				return err
			}
		}
	}
	for _, run := range input.ProviderRuns {
		completed := ""
		if !run.CompletedAt.IsZero() {
			completed = formatStoreScanTime(run.CompletedAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO provider_runs(scan_id, provider_id, provider_name, backend, provider_version, started_at, completed_at, health, error) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			input.Scan.ScanID, run.Provider.Key(), run.Provider.Name, run.Provider.Backend, strings.TrimSpace(run.Version), formatStoreScanTime(run.StartedAt), completed, string(run.Health), sanitizeProviderDiagnostic(run.Error)); err != nil {
			return err
		}
		for _, mapping := range run.Mappings {
			if !mapping.VerifiedFor(mapping.InstalledIdentity, input.Scan) {
				return errors.New("refusing to persist unverifiable Store identity mapping")
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO verified_identity_mappings(user_sid, package_family_name, product_id, provider_id, scan_id, verified_at, evidence) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				mapping.InstalledIdentity.UserSID, mapping.InstalledIdentity.PackageFamilyName, mapping.ProductID, mapping.Provider.Key(), input.Scan.ScanID, formatStoreScanTime(mapping.VerifiedAt), mapping.Evidence); err != nil {
				return err
			}
		}
		for _, observation := range run.Observations {
			if observation.ScanID != input.Scan.ScanID || observation.Identity.UserSID != input.Scan.UserSID || !observation.Identity.Resolved() {
				return errors.New("refusing to persist cross-user or cross-scan Store observation")
			}
			productID, updateID, targetVerified := "", "", false
			if observation.Target != nil {
				productID = observation.Target.ProductID
				updateID = observation.Target.UpdateID
				targetVerified = observation.Target.ExactFor(observation.Identity)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO provider_observations(scan_id, provider_id, user_sid, package_family_name, kind, health, observed_at, installed_version, available_version, catalog_version, product_id, update_id, target_verified, diagnostics) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				input.Scan.ScanID, observation.Provider.Key(), observation.Identity.UserSID, observation.Identity.PackageFamilyName, string(observation.Kind), string(observation.Health), formatStoreScanTime(observation.ObservedAt), observation.InstalledVersion, observation.AvailableVersion, observation.CatalogVersion, productID, updateID, boolToInt(targetVerified), sanitizeProviderDiagnostic(observation.Diagnostics)); err != nil {
				return err
			}
		}
	}
	for _, assessment := range input.Assessments {
		evidenceJSON, err := json.Marshal(assessment.Evidence)
		if err != nil {
			return err
		}
		productID, updateID := assessment.StoreProductID, assessment.UpdateID
		if assessment.Target != nil {
			productID = firstNonEmpty(productID, assessment.Target.ProductID)
			updateID = firstNonEmpty(updateID, assessment.Target.UpdateID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO update_assessments(scan_id, user_sid, package_family_name, state, reason, installed_version, available_version, stale, product_id, update_id, exact_action_target_available, applicability, observed_at, evidence_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			input.Scan.ScanID, assessment.Identity.UserSID, assessment.Identity.PackageFamilyName, string(assessment.State), assessment.Reason, assessment.InstalledVersion, assessment.AvailableVersion, boolToInt(assessment.Stale), productID, updateID, boolToInt(assessment.ExactActionTargetAvailable), assessment.Applicability, formatStoreScanTime(assessment.ObservedAt), string(evidenceJSON)); err != nil {
			return err
		}
	}
	return nil
}

func (store *StoreScanStore) publishScanTx(ctx context.Context, tx *sql.Tx, scan StoreScanGeneration) (bool, error) {
	var latestStarted string
	err := tx.QueryRowContext(ctx, `SELECT started_at FROM scan_runs WHERE user_sid = ? AND published = 1 ORDER BY started_at DESC, scan_id DESC LIMIT 1`, scan.UserSID).Scan(&latestStarted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if latestStarted != "" {
		latest, err := parseStoreScanTime(latestStarted)
		if err != nil {
			return false, err
		}
		if latest.After(scan.StartedAt) {
			return false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scan_runs SET published = 0 WHERE user_sid = ?`, scan.UserSID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scan_runs SET published = 1, published_at = ? WHERE scan_id = ?`, formatStoreScanTime(time.Now().UTC()), scan.ScanID); err != nil {
		return false, err
	}
	return true, nil
}

func (store *StoreScanStore) pruneOldScanRunsTx(ctx context.Context, tx *sql.Tx, userSID string) error {
	if userSID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM scan_runs
		WHERE user_sid = ?
		  AND published = 0
		  AND scan_id NOT IN (
			SELECT scan_id FROM scan_runs
			WHERE user_sid = ?
			ORDER BY started_at DESC, scan_id DESC
			LIMIT ?
		  )`, userSID, userSID, storeScanRetentionRunsUser)
	return err
}

func (store *StoreScanStore) PublishedAssessments(ctx context.Context, userSID string) ([]StorePublishedAssessment, error) {
	query := `SELECT a.scan_id, a.user_sid, a.package_family_name, a.state, COALESCE(a.reason,''), COALESCE(a.installed_version,''), COALESCE(a.available_version,''), a.stale, COALESCE(a.product_id,''), COALESCE(a.update_id,''), a.exact_action_target_available, COALESCE(a.applicability,''), a.observed_at, COALESCE(a.evidence_json,'[]')
		FROM update_assessments a
		JOIN scan_runs s ON s.scan_id = a.scan_id
		WHERE s.published = 1 AND a.user_sid = ?
		ORDER BY a.package_family_name`
	rows, err := store.db.QueryContext(ctx, query, userSID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assessments []StorePublishedAssessment
	for rows.Next() {
		var assessment StorePublishedAssessment
		var state, observedAt, evidenceJSON string
		var stale, exact int
		if err := rows.Scan(&assessment.ScanID, &assessment.Identity.UserSID, &assessment.Identity.PackageFamilyName, &state, &assessment.Reason, &assessment.InstalledVersion, &assessment.AvailableVersion, &stale, &assessment.StoreProductID, &assessment.UpdateID, &exact, &assessment.Applicability, &observedAt, &evidenceJSON); err != nil {
			return nil, err
		}
		assessment.State = StoreUpdateState(state)
		assessment.Stale = stale != 0
		assessment.ExactActionTargetAvailable = exact != 0
		assessment.ObservedAt, _ = parseStoreScanTime(observedAt)
		_ = json.Unmarshal([]byte(evidenceJSON), &assessment.Evidence)
		assessments = append(assessments, assessment)
	}
	return assessments, rows.Err()
}

func (store *StoreScanStore) LatestPublishedProviderSummaries(ctx context.Context, userSID string) ([]StorePackageProviderSummary, error) {
	scan, ok, err := store.LatestPublishedScan(ctx, userSID)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT COALESCE(provider_name, provider_id), COALESCE(provider_version, ''), health, COALESCE(completed_at, started_at), COALESCE(error, '') FROM provider_runs WHERE scan_id = ? ORDER BY provider_id`, scan.ScanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []StorePackageProviderSummary
	for rows.Next() {
		var summary StorePackageProviderSummary
		if err := rows.Scan(&summary.Name, &summary.Version, &summary.Health, &summary.ObservedAt, &summary.Error); err != nil {
			return nil, err
		}
		summary.Kind = providerRunSummaryKind(StoreProviderHealth(summary.Health))
		summary.Error = sanitizeProviderDiagnostic(summary.Error)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (store *StoreScanStore) LatestPublishedScan(ctx context.Context, userSID string) (StoreScanGeneration, bool, error) {
	row := store.db.QueryRowContext(ctx, `SELECT scan_id, user_sid, started_at, COALESCE(completed_at,''), COALESCE(windows_version,''), COALESCE(windows_build,''), COALESCE(architecture,''), status FROM scan_runs WHERE user_sid = ? AND published = 1 ORDER BY started_at DESC, scan_id DESC LIMIT 1`, userSID)
	var scan StoreScanGeneration
	var started, completed, status string
	if err := row.Scan(&scan.ScanID, &scan.UserSID, &started, &completed, &scan.WindowsVersion, &scan.WindowsBuild, &scan.Architecture, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StoreScanGeneration{}, false, nil
		}
		return StoreScanGeneration{}, false, err
	}
	scan.StartedAt, _ = parseStoreScanTime(started)
	scan.CompletedAt, _ = parseStoreScanTime(completed)
	scan.CompletionStatus = StoreScanCompletionStatus(status)
	return scan, true, nil
}

func providerRunSummaryKind(health StoreProviderHealth) string {
	switch health {
	case StoreProviderHealthy:
		return "provider_run"
	case StoreProviderFailed:
		return string(StoreObservationProviderFailure)
	case StoreProviderUnsupported:
		return string(StoreObservationUnsupportedProvider)
	case StoreProviderStale:
		return string(StoreObservationStaleResult)
	default:
		return string(StoreObservationIncompleteResult)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatStoreScanTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Truncate(time.Second).Format(time.RFC3339Nano)
}

func parseStoreScanTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
