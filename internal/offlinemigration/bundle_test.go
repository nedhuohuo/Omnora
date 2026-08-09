package offlinemigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnora/internal/recovery"
	"omnora/internal/store"
)

func TestBundleRejectsIncompleteRequest(t *testing.T) {
	if _, err := BuildBundle(context.Background(), BundleRequest{}); err == nil {
		t.Fatal("BuildBundle accepted an incomplete request")
	}
}

func TestBundlePublishesCompleteValidatedSnapshotAndManifest(t *testing.T) {
	request := newBundleTestRequest(t)
	secret := "TOP_SECRET_DO_NOT_RENDER"
	writeTestFile(t, filepath.Join(request.ConfigDir, "runtime.env"), []byte("TOKEN="+secret+"\n"), 0o644)
	writeTestFile(t, filepath.Join(request.DataDir, "recovery.journal"), []byte("persistent-sidecar\n"), 0o644)
	writeTestFile(t, filepath.Join(request.ManagedDir, "accounts", "acct-1", "personal.txt"), []byte("personal-data\n"), 0o644)
	emptyDir := filepath.Join(request.ManagedDir, "accounts", "acct-2", "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	externalRoot := t.TempDir()
	writeTestFile(t, filepath.Join(externalRoot, "must-not-copy.txt"), []byte("external-data\n"), 0o600)
	request.ExternalIdentities = []ExternalIdentity{{
		RegistrationID: "mount-1", Root: externalRoot, Device: 21, Inode: 34, MountID: 55,
		Filesystem: "ext4", Source: "/dev/example",
	}}

	if _, err := request.DB.SQL().ExecContext(context.Background(), `CREATE TABLE bundle_probe(id TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := request.DB.SQL().ExecContext(context.Background(), `INSERT INTO bundle_probe(id, value) VALUES ('committed-in-wal', 'present')`); err != nil {
		t.Fatal(err)
	}

	result, err := BuildBundle(context.Background(), request)
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "ROLLBACK_READY")); err != nil {
		t.Fatalf("ready marker: %v", err)
	}
	if err := recovery.ValidateSnapshot(context.Background(), result.SnapshotPath); err != nil {
		t.Fatalf("snapshot validation: %v", err)
	}
	snapshot, err := store.OpenSQLiteReadonly(context.Background(), result.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var value string
	if err := snapshot.SQL().QueryRowContext(context.Background(), `SELECT value FROM bundle_probe WHERE id = 'committed-in-wal'`).Scan(&value); err != nil {
		t.Fatalf("query rollback snapshot: %v", err)
	}
	if value != "present" {
		t.Fatalf("snapshot value = %q", value)
	}
	for _, forbidden := range []string{
		filepath.Join(result.Path, "data", filepath.Base(request.DBPath)),
		filepath.Join(result.Path, "data", filepath.Base(request.DBPath)+"-wal"),
		filepath.Join(result.Path, "data", filepath.Base(request.DBPath)+"-shm"),
		filepath.Join(result.Path, "external", "must-not-copy.txt"),
	} {
		if _, err := os.Lstat(forbidden); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("forbidden duplicate exists at %s: %v", forbidden, err)
		}
	}
	assertFileContent(t, filepath.Join(result.Path, "config", "runtime.env"), "TOKEN="+secret+"\n")
	assertFileContent(t, filepath.Join(result.Path, "data", "recovery.journal"), "persistent-sidecar\n")
	assertFileContent(t, filepath.Join(result.Path, "managed", "accounts", "acct-1", "personal.txt"), "personal-data\n")
	assertMode(t, filepath.Join(result.Path, "managed", "accounts", "acct-2", "empty"), 0o700)

	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifestBytes), secret) {
		t.Fatal("manifest leaked runtime.env secret value")
	}
	var manifest BundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.InstanceID != request.InstanceID || manifest.SourceMigration != "012" || manifest.TargetMigration != "013" {
		t.Fatalf("unexpected manifest identity/migrations: %#v", manifest)
	}
	if len(manifest.ExternalIdentities) != 1 || manifest.ExternalIdentities[0].RegistrationID != "mount-1" {
		t.Fatalf("external identities = %#v", manifest.ExternalIdentities)
	}
	if manifest.FileCount == 0 || manifest.TotalBytes == 0 {
		t.Fatalf("manifest totals = files %d bytes %d", manifest.FileCount, manifest.TotalBytes)
	}
	assertBundleModes(t, result.Path)
	if entries, err := os.ReadDir(request.RollbackRoot); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".incomplete") {
				t.Fatalf("successful bundle left incomplete directory: %s", entry.Name())
			}
		}
	}
}

func TestBundleRejectsRollbackSourceOverlap(t *testing.T) {
	request := newBundleTestRequest(t)
	base := filepath.Dir(request.ConfigDir)
	for name, rollbackRoot := range map[string]string{
		"same":          request.ConfigDir,
		"inside":        filepath.Join(request.ManagedDir, "rollback"),
		"contains":      base,
		"external root": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.RollbackRoot = rollbackRoot
			if name == "external root" {
				candidate.ExternalIdentities = []ExternalIdentity{{RegistrationID: "external", Root: rollbackRoot}}
			}
			if _, err := BuildBundle(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("BuildBundle error = %v, want overlap rejection", err)
			}
		})
	}
}

func TestBundleRejectsPersistentSymlinkWithoutReadyMarker(t *testing.T) {
	request := newBundleTestRequest(t)
	target := filepath.Join(t.TempDir(), "secret")
	writeTestFile(t, target, []byte("outside\n"), 0o600)
	if err := os.Symlink(target, filepath.Join(request.ManagedDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildBundle(context.Background(), request); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("BuildBundle error = %v, want symlink rejection", err)
	}
	assertNoReadyMarker(t, request.RollbackRoot)
}

func TestBundleRejectsInsufficientSpaceWithoutReadyMarker(t *testing.T) {
	request := newBundleTestRequest(t)
	request.setTestHooks(bundleTestHooks{availableBytes: func(string) (uint64, error) { return 0, nil }})
	if _, err := BuildBundle(context.Background(), request); err == nil || !strings.Contains(err.Error(), "insufficient") {
		t.Fatalf("BuildBundle error = %v, want insufficient space", err)
	}
	assertNoReadyMarker(t, request.RollbackRoot)
}

func TestBundleFailureDoesNotPublishReadyMarker(t *testing.T) {
	request := newBundleTestRequest(t)
	request.setTestHooks(bundleTestHooks{afterCopy: func(string) error { return errors.New("injected failure") }})
	if _, err := BuildBundle(context.Background(), request); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("BuildBundle error = %v, want injected failure", err)
	}
	assertNoReadyMarker(t, request.RollbackRoot)
}

func newBundleTestRequest(t *testing.T) BundleRequest {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	managedDir := filepath.Join(root, "managed")
	rollbackRoot := filepath.Join(root, "rollback")
	for _, directory := range []string{configDir, dataDir, managedDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(dataDir, "omnora.db")
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{Path: dbPath, BusyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return BundleRequest{
		DB: db, DBPath: dbPath, ConfigDir: configDir, DataDir: dataDir,
		ManagedDir: managedDir, RollbackRoot: rollbackRoot,
		InstanceID: strings.Repeat("a", 64), SourceMigration: "012", TargetMigration: "013",
	}
}

func writeTestFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("%s content = %q, want %q", path, content, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func assertBundleModes(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			assertMode(t, path, 0o700)
		} else {
			assertMode(t, path, 0o600)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertNoReadyMarker(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "ROLLBACK_READY" {
			t.Fatalf("failure published ready marker: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
