package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestStageReaderValidatesManifestAndPublishesPendingRelease(t *testing.T) {
	manager := NewManager(t.TempDir(), 1<<20)
	application := []byte("omnora-test-binary")
	recovery := []byte("omnora-recovery-test-binary")
	archive := buildArchive(t, Manifest{
		FormatVersion: 1,
		Version:       "2026.08.09",
		TargetOS:      runtime.GOOS,
		TargetArch:    runtime.GOARCH,
		Artifacts: map[string]ManifestArtifact{
			ApplicationFileName: {Path: ApplicationFileName, SHA256: hash(application), Size: int64(len(application))},
			RecoveryFileName:    {Path: RecoveryFileName, SHA256: hash(recovery), Size: int64(len(recovery))},
		},
	}, map[string][]byte{ApplicationFileName: application, RecoveryFileName: recovery})

	release, err := manager.StageReader(context.Background(), bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("StageReader() error = %v", err)
	}
	if release.Version != "2026.08.09" || release.ID == "" {
		t.Fatalf("release = %#v", release)
	}
	if _, err := os.Stat(release.Path + "/" + ApplicationFileName); err != nil {
		t.Fatalf("staged application: %v", err)
	}
	before, err := manager.Status()
	if err != nil || before.State != "built_in" {
		t.Fatalf("status before activation = %#v, %v", before, err)
	}
	if err := manager.Activate(release); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatalf("status after activation: %v", err)
	}
	if status.State != "pending_restart" || status.Pending == nil || status.Pending.Version != release.Version {
		t.Fatalf("status after activation = %#v", status)
	}
}

func TestStageReaderRejectsUnexpectedEntryAndHashMismatch(t *testing.T) {
	manager := NewManager(t.TempDir(), 1<<20)
	baseManifest := Manifest{
		FormatVersion: 1,
		Version:       "v1",
		TargetOS:      runtime.GOOS,
		TargetArch:    runtime.GOARCH,
		Artifacts: map[string]ManifestArtifact{
			ApplicationFileName: {Path: ApplicationFileName, SHA256: hash([]byte("app")), Size: 3},
			RecoveryFileName:    {Path: RecoveryFileName, SHA256: hash([]byte("rec")), Size: 3},
		},
	}
	validFiles := map[string][]byte{ApplicationFileName: []byte("app"), RecoveryFileName: []byte("rec")}

	t.Run("hash mismatch", func(t *testing.T) {
		manifest := baseManifest
		manifest.Artifacts = map[string]ManifestArtifact{
			ApplicationFileName: {Path: ApplicationFileName, SHA256: hex.EncodeToString(make([]byte, 32)), Size: 3},
			RecoveryFileName:    baseManifest.Artifacts[RecoveryFileName],
		}
		_, err := manager.StageReader(context.Background(), bytes.NewReader(buildArchive(t, manifest, validFiles)))
		if !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("error = %v, want ErrInvalidPackage", err)
		}
	})

	t.Run("unexpected entry", func(t *testing.T) {
		archive := buildArchive(t, baseManifest, map[string][]byte{
			ManifestFileName:    mustJSON(t, baseManifest),
			ApplicationFileName: validFiles[ApplicationFileName],
			RecoveryFileName:    validFiles[RecoveryFileName],
			"unexpected":        []byte("nope"),
		})
		_, err := manager.StageReader(context.Background(), bytes.NewReader(archive))
		if !errors.Is(err, ErrInvalidPackage) {
			t.Fatalf("error = %v, want ErrInvalidPackage", err)
		}
	})
}

func TestRollbackRequestIsDurable(t *testing.T) {
	manager := NewManager(t.TempDir(), 1<<20)
	if err := manager.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	releaseDir := manager.releasesDir() + "/release-old"
	if err := os.Mkdir(releaseDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		FormatVersion: 1,
		Version:       "old",
		TargetOS:      runtime.GOOS,
		TargetArch:    runtime.GOARCH,
		Artifacts: map[string]ManifestArtifact{
			ApplicationFileName: {Path: ApplicationFileName, SHA256: hash([]byte("app")), Size: 3},
			RecoveryFileName:    {Path: RecoveryFileName, SHA256: hash([]byte("rec")), Size: 3},
		},
	}
	if err := os.WriteFile(releaseDir+"/"+ManifestFileName, mustJSON(t, manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{ApplicationFileName: []byte("app"), RecoveryFileName: []byte("rec")} {
		if err := os.WriteFile(releaseDir+"/"+name, content, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := writePointerAtomic(manager.activePath(), releaseDir); err != nil {
		t.Fatal(err)
	}
	if err := writePointerAtomic(manager.previousPath(), releaseDir); err != nil {
		t.Fatal(err)
	}
	if err := manager.RequestRollback(); err != nil {
		t.Fatalf("RequestRollback() error = %v", err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "rollback_pending" || status.Current == nil {
		t.Fatalf("rollback status = %#v", status)
	}
}

func buildArchive(t *testing.T, manifest Manifest, files map[string][]byte) []byte {
	t.Helper()
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	all := make(map[string][]byte, len(files)+1)
	all[ManifestFileName] = manifestBytes
	for name, content := range files {
		all[name] = content
	}
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range []string{ManifestFileName, ApplicationFileName, RecoveryFileName, "unexpected"} {
		content, ok := all[name]
		if !ok {
			continue
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
