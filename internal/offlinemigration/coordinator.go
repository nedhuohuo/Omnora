package offlinemigration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"omnora/internal/instanceid"
	"omnora/internal/mountid"
	"omnora/internal/store"
)

// MigrationRequest names the four persistent roots involved in an offline
// migration. RollbackRoot must not overlap any of them.
type MigrationRequest struct {
	DBPath       string
	ConfigDir    string
	DataDir      string
	ManagedDir   string
	RollbackRoot string
	Invariants   []DomainInvariant
}

type MigrationResult struct {
	NoPendingMigration bool
	Target             MigrationTarget
	RollbackBundlePath string
	JournalPath        string
}

type migrationInspection struct {
	Source  store.MigrationInfo
	Target  store.MigrationInfo
	Pending bool
}

type coordinatorDeps struct {
	now              func() time.Time
	inspect          func(context.Context, *store.DB) (migrationInspection, error)
	migrateStaging   func(context.Context, string, string) error
	validateDatabase func(context.Context, string, MigrationTarget, []DomainInvariant) error
	buildBundle      func(context.Context, BundleRequest) (BundleResult, error)
	captureManaged   func(string) (mountid.Identity, error)
	validateMarkers  func(instanceid.MarkerPaths) (string, error)
	rename           func(string, string) error
	syncDir          func(string) error
	randomID         func() (string, error)
}

type MigrationCoordinator struct{ deps coordinatorDeps }

func NewMigrationCoordinator() *MigrationCoordinator {
	return &MigrationCoordinator{deps: coordinatorDeps{
		now: time.Now,
		inspect: func(ctx context.Context, db *store.DB) (migrationInspection, error) {
			source, target, pending, err := store.InspectOfflineMigration(ctx, db)
			return migrationInspection{Source: source, Target: target, Pending: pending}, err
		},
		migrateStaging:   migrateProductionStaging,
		validateDatabase: ValidateDatabase,
		buildBundle:      BuildBundle,
		captureManaged:   mountid.Capture,
		validateMarkers:  instanceid.ValidateMarkers,
		rename:           os.Rename,
		syncDir:          syncDirectory,
		randomID:         randomMigrationID,
	}}
}

func (c *MigrationCoordinator) Run(ctx context.Context, request MigrationRequest) (result MigrationResult, returnErr error) {
	request = trimMigrationRequest(request)
	if err := validateMigrationRequest(request); err != nil {
		return result, err
	}

	lock, err := AcquireLock(request.DBPath)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := lock.Close(); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()
	if err := EnsureNoUnfinishedJournal(request.DBPath); err != nil {
		return result, err
	}

	markerPaths := instanceid.MarkerPaths{
		Config:  filepath.Join(request.ConfigDir, ".omnora-instance-id"),
		Data:    filepath.Join(request.DataDir, ".omnora-instance-id"),
		Managed: filepath.Join(request.ManagedDir, ".omnora-instance-id"),
	}
	instanceID, err := c.deps.validateMarkers(markerPaths)
	if err != nil {
		return result, fmt.Errorf("validate persistent volume markers: %w", err)
	}
	managedIdentity, err := c.deps.captureManaged(request.ManagedDir)
	if err != nil {
		return result, fmt.Errorf("capture managed volume identity: %w", err)
	}

	live, err := store.OpenSQLiteValidatedReadonly(ctx, request.DBPath)
	if err != nil {
		return result, fmt.Errorf("open validated live database: %w", err)
	}
	liveOpen := true
	defer func() {
		if liveOpen {
			_ = live.Close()
		}
	}()

	inspection, err := c.deps.inspect(ctx, live)
	if err != nil {
		return result, fmt.Errorf("inspect pending offline migration: %w", err)
	}
	if !inspection.Pending {
		if err := live.Close(); err != nil {
			return result, fmt.Errorf("close live database: %w", err)
		}
		liveOpen = false
		return MigrationResult{NoPendingMigration: true}, nil
	}
	target := MigrationTarget{Version: inspection.Target.Version, Name: inspection.Target.Name}
	if inspection.Source.Version <= 0 || strings.TrimSpace(inspection.Source.Name) == "" || target.Version <= inspection.Source.Version || strings.TrimSpace(target.Name) == "" {
		return result, errors.New("pending offline migration inspection is invalid")
	}

	now := c.deps.now().UTC()
	journalPath := DefaultJournalPath(request.DBPath)
	journal := Journal{
		MigrationName:       "account_mount",
		Phase:               PhasePreparing,
		SourceSchemaVersion: inspection.Source.Version,
		TargetSchemaVersion: target.Version,
		TargetMigrationName: target.Name,
		StartedAt:           now,
		UpdatedAt:           now,
	}
	if err := WriteJournal(journalPath, journal); err != nil {
		return result, fmt.Errorf("write preparing migration journal: %w", err)
	}
	result.JournalPath = journalPath
	result.Target = target

	externalIdentities, err := loadExternalIdentities(ctx, live.SQL())
	if err != nil {
		return result, fmt.Errorf("load external mount identities: %w", err)
	}
	bundle, err := c.deps.buildBundle(ctx, BundleRequest{
		DB: live, DBPath: request.DBPath, ConfigDir: request.ConfigDir, DataDir: request.DataDir,
		ManagedDir: request.ManagedDir, RollbackRoot: request.RollbackRoot, InstanceID: instanceID,
		SourceMigration: inspection.Source.Name, TargetMigration: target.Name, ExternalIdentities: externalIdentities,
	})
	if err != nil {
		return result, fmt.Errorf("build rollback bundle: %w", err)
	}
	result.RollbackBundlePath = bundle.Path

	id, err := c.deps.randomID()
	if err != nil {
		return result, fmt.Errorf("create staging identity: %w", err)
	}
	stagingPath := request.DBPath + ".offline-migration-" + id + ".db"
	if _, err := AdvanceJournal(journalPath, PhaseRollbackReady, c.deps.now(), func(j *Journal) {
		j.RollbackBundlePath = bundle.Path
		j.StagingPath = stagingPath
	}); err != nil {
		return result, fmt.Errorf("record rollback-ready migration phase: %w", err)
	}
	if err := copyFileDurable(bundle.SnapshotPath, stagingPath, 0o600); err != nil {
		return result, fmt.Errorf("create same-directory staging database: %w", err)
	}
	if err := c.deps.migrateStaging(ctx, request.DBPath, stagingPath); err != nil {
		return result, fmt.Errorf("apply offline migration to staging database: %w", err)
	}
	if _, err := AdvanceJournal(journalPath, PhaseStagingMigrated, c.deps.now(), nil); err != nil {
		return result, fmt.Errorf("record staging-migrated phase: %w", err)
	}
	rollbackPersonalDirectories, err := provisionTargetPersonalDirectories(ctx, stagingPath, request.ManagedDir)
	if err != nil {
		return result, fmt.Errorf("provision target personal directories: %w", err)
	}
	preservePersonalDirectories := false
	defer func() {
		if !preservePersonalDirectories {
			rollbackPersonalDirectories()
		}
	}()
	if err := c.deps.validateDatabase(ctx, stagingPath, target, request.Invariants); err != nil {
		return result, fmt.Errorf("validate migrated staging database: %w", err)
	}
	if _, err := AdvanceJournal(journalPath, PhaseValidated, c.deps.now(), nil); err != nil {
		return result, fmt.Errorf("record validated migration phase: %w", err)
	}

	recheckedID, err := c.deps.validateMarkers(markerPaths)
	if err != nil || recheckedID != instanceID {
		return result, errors.New("persistent volume markers changed during offline migration")
	}
	recheckedManaged, err := c.deps.captureManaged(request.ManagedDir)
	if err != nil || !sameManagedIdentity(managedIdentity, recheckedManaged) {
		return result, errors.New("managed volume identity changed during offline migration")
	}
	if err := live.Close(); err != nil {
		return result, fmt.Errorf("close read-only live database before commit: %w", err)
	}
	liveOpen = false
	quiesceDB, err := store.OpenSQLiteValidatedForOfflineSource(ctx, request.DBPath)
	if err != nil {
		return result, fmt.Errorf("reopen live database for WAL checkpoint: %w", err)
	}
	if err := quiesceAndCloseDatabase(ctx, quiesceDB, request.DBPath); err != nil {
		return result, fmt.Errorf("quiesce live database before commit: %w", err)
	}
	if _, err := AdvanceJournal(journalPath, PhaseCommitStarted, c.deps.now(), nil); err != nil {
		return result, fmt.Errorf("record commit-started phase: %w", err)
	}
	if err := c.deps.rename(stagingPath, request.DBPath); err != nil {
		return result, fmt.Errorf("atomically replace live database: %w", err)
	}
	preservePersonalDirectories = true
	commitFailure := func(cause error) (MigrationResult, error) {
		_, journalErr := AdvanceJournal(journalPath, PhaseCommitIndeterminate, c.deps.now(), nil)
		if journalErr != nil {
			return result, fmt.Errorf("%v; also failed to record commit-indeterminate phase: %w", cause, journalErr)
		}
		return result, cause
	}
	if err := c.deps.syncDir(filepath.Dir(request.DBPath)); err != nil {
		return commitFailure(fmt.Errorf("sync database directory after commit: %w", err))
	}
	if err := c.deps.validateDatabase(ctx, request.DBPath, target, request.Invariants); err != nil {
		return commitFailure(fmt.Errorf("post-commit database validation failed: %w", err))
	}
	if _, err := AdvanceJournal(journalPath, PhaseCompleted, c.deps.now(), nil); err != nil {
		return result, fmt.Errorf("record completed migration phase: %w", err)
	}
	return result, nil
}

func trimMigrationRequest(request MigrationRequest) MigrationRequest {
	request.DBPath = strings.TrimSpace(request.DBPath)
	request.ConfigDir = strings.TrimSpace(request.ConfigDir)
	request.DataDir = strings.TrimSpace(request.DataDir)
	request.ManagedDir = strings.TrimSpace(request.ManagedDir)
	request.RollbackRoot = strings.TrimSpace(request.RollbackRoot)
	return request
}

func validateMigrationRequest(request MigrationRequest) error {
	for name, value := range map[string]string{
		"database": request.DBPath, "config": request.ConfigDir, "data": request.DataDir,
		"managed": request.ManagedDir, "rollback root": request.RollbackRoot,
	} {
		if value == "" {
			return fmt.Errorf("offline migration %s path is required", name)
		}
	}
	return nil
}

func migrateProductionStaging(ctx context.Context, livePath, stagingPath string) error {
	db, err := store.ApplyOfflineMigrationsToStaging(ctx, livePath, stagingPath)
	if err != nil {
		return err
	}
	return quiesceAndCloseDatabase(ctx, db, stagingPath)
}

func quiesceAndCloseDatabase(ctx context.Context, db *store.DB, path string) error {
	var busy, logFrames, checkpointed int
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		_ = db.Close()
		return err
	}
	if busy != 0 {
		_ = db.Close()
		return errors.New("SQLite WAL checkpoint is busy")
	}
	var journalMode string
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&journalMode); err != nil {
		_ = db.Close()
		return err
	}
	if !strings.EqualFold(journalMode, "delete") {
		_ = db.Close()
		return fmt.Errorf("SQLite journal mode is %q after checkpoint", journalMode)
	}
	if err := db.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(filepath.Dir(path))
}

func loadExternalIdentities(ctx context.Context, db *sql.DB) ([]ExternalIdentity, error) {
	storageKindColumn, err := externalMountStorageKindColumn(ctx, db)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, root_path, mount_identity_json FROM mounts WHERE ` + storageKindColumn + ` = 'external' AND status <> 'deleted' ORDER BY id`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ExternalIdentity
	for rows.Next() {
		var id, root string
		var encoded sql.NullString
		if err := rows.Scan(&id, &root, &encoded); err != nil {
			return nil, err
		}
		if !encoded.Valid || strings.TrimSpace(encoded.String) == "" {
			return nil, fmt.Errorf("external mount %q has no verified identity", id)
		}
		var identity mountid.Identity
		if err := json.Unmarshal([]byte(encoded.String), &identity); err != nil || identity.Device == 0 || identity.Inode == 0 {
			return nil, fmt.Errorf("external mount %q has an invalid identity", id)
		}
		mountID := identity.Statx.MountID
		if mountID == 0 && identity.Mount.ID > 0 {
			mountID = uint64(identity.Mount.ID)
		}
		result = append(result, ExternalIdentity{
			RegistrationID: id, Root: root, Device: identity.Device, Inode: identity.Inode,
			MountID: mountID, Filesystem: identity.Mount.FSType, Source: identity.Mount.Source,
		})
	}
	return result, rows.Err()
}

func externalMountStorageKindColumn(ctx context.Context, db *sql.DB) (string, error) {
	for _, column := range []string{"storage_kind", "kind"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('mounts') WHERE name = ?`, column).Scan(&count); err != nil {
			return "", err
		}
		if count == 1 {
			return column, nil
		}
	}
	return "", errors.New("mount storage-kind column is missing")
}

func sameManagedIdentity(left, right mountid.Identity) bool {
	return left.Path == right.Path && left.Device == right.Device && left.Inode == right.Inode &&
		reflect.DeepEqual(left.Statx, right.Statx) && reflect.DeepEqual(left.Mount, right.Mount)
}

func randomMigrationID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func copyFileDurable(source, destination string, mode os.FileMode) error {
	if filepath.Dir(source) == filepath.Dir(destination) && source == destination {
		return errors.New("staging source and destination are identical")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		_ = output.Close()
		if !installed {
			_ = os.Remove(destination)
		}
	}()
	if _, err := output.ReadFrom(input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	installed = true
	return nil
}
