package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ManifestFileName       = "manifest.json"
	ApplicationFileName    = "omnora"
	RecoveryFileName       = "omnora-recovery"
	FailureFileName        = "failure"
	DefaultMaxPackageBytes = int64(512 << 20)
	maxManifestBytes       = int64(128 << 10)
)

var (
	ErrDisabled          = errors.New("self-update is disabled")
	ErrPendingUpdate     = errors.New("an update is already pending restart")
	ErrNoRollback        = errors.New("no installed update can be rolled back")
	ErrInvalidPackage    = errors.New("invalid update package")
	ErrUnsupportedTarget = errors.New("update package target does not match this runtime")
)

// Manifest is the signed-content boundary for an update package. The current
// deployment does not have a release-signing key configured, so SHA-256 values
// protect against transfer/corruption and the administrator session protects
// who may install an executable. A future signing field can be added without
// changing the archive layout.
type Manifest struct {
	FormatVersion int                         `json:"formatVersion"`
	Version       string                      `json:"version"`
	TargetOS      string                      `json:"targetOS"`
	TargetArch    string                      `json:"targetArch"`
	Artifacts     map[string]ManifestArtifact `json:"artifacts"`
}

type ManifestArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Release struct {
	ID              string    `json:"id"`
	Version         string    `json:"version"`
	TargetOS        string    `json:"targetOS"`
	TargetArch      string    `json:"targetArch"`
	Path            string    `json:"-"`
	ArchiveSHA256   string    `json:"archiveSha256"`
	ArchiveSizeByte int64     `json:"archiveSizeBytes"`
	UploadedAt      time.Time `json:"uploadedAt"`
}

type Status struct {
	State   string   `json:"state"`
	Current *Release `json:"current,omitempty"`
	Pending *Release `json:"pending,omitempty"`
	Failure string   `json:"failure,omitempty"`
}

type Manager struct {
	dir                 string
	maxPackageBytes     int64
	maxUncompressedByte int64
	mu                  sync.Mutex
}

func NewManager(dir string, maxPackageBytes int64) *Manager {
	if maxPackageBytes <= 0 {
		maxPackageBytes = DefaultMaxPackageBytes
	}
	maxUncompressed := maxPackageBytes * 4
	if maxUncompressed < 1<<30 {
		maxUncompressed = 1 << 30
	}
	return &Manager{
		dir:                 filepath.Clean(strings.TrimSpace(dir)),
		maxPackageBytes:     maxPackageBytes,
		maxUncompressedByte: maxUncompressed,
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.dir != "" && m.dir != "."
}

func (m *Manager) MaxPackageBytes() int64 {
	if m == nil || m.maxPackageBytes <= 0 {
		return DefaultMaxPackageBytes
	}
	return m.maxPackageBytes
}

func (m *Manager) EnsureDirs() error {
	if !m.Enabled() {
		return ErrDisabled
	}
	return os.MkdirAll(m.releasesDir(), 0700)
}

// StageReader stores the uploaded archive first, then validates and extracts it
// into a private release directory. No pending pointer is written until the
// caller explicitly activates the validated release.
func (m *Manager) StageReader(ctx context.Context, source io.Reader) (Release, error) {
	if !m.Enabled() {
		return Release{}, ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return Release{}, err
	}
	archive, err := os.CreateTemp(m.incomingDir(), ".package-*")
	if err != nil {
		return Release{}, fmt.Errorf("create update staging file: %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	limited := io.LimitReader(&contextReader{ctx: ctx, reader: source}, m.maxPackageBytes+1)
	archiveSize, err := io.Copy(archive, limited)
	if err != nil {
		_ = archive.Close()
		return Release{}, fmt.Errorf("store update package: %w", err)
	}
	if archiveSize > m.maxPackageBytes {
		_ = archive.Close()
		return Release{}, fmt.Errorf("%w: package exceeds %d bytes", ErrInvalidPackage, m.maxPackageBytes)
	}
	if err := archive.Sync(); err != nil {
		_ = archive.Close()
		return Release{}, fmt.Errorf("sync update package: %w", err)
	}
	if err := archive.Close(); err != nil {
		return Release{}, fmt.Errorf("close update package: %w", err)
	}
	return m.stagePathLocked(ctx, archivePath, archiveSize)
}

func (m *Manager) Activate(release Release) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return err
	}
	if _, err := m.readReleaseLocked(release.Path); err != nil {
		return err
	}
	if _, err := m.readPointerLocked(m.pendingPath()); err == nil {
		return ErrPendingUpdate
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writePointerAtomic(m.pendingPath(), release.Path); err != nil {
		return fmt.Errorf("publish pending update: %w", err)
	}
	_ = os.Remove(m.failurePath())
	return nil
}

func (m *Manager) CancelPending() error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.Remove(m.pendingPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) RequestRollback() error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return err
	}
	if _, err := m.readPointerLocked(m.activePath()); errors.Is(err, os.ErrNotExist) {
		if _, previousErr := m.readPointerLocked(m.previousPath()); errors.Is(previousErr, os.ErrNotExist) {
			return ErrNoRollback
		}
	} else if err != nil {
		return err
	}
	if err := writeMarkerAtomic(m.rollbackPath()); err != nil {
		return fmt.Errorf("publish rollback request: %w", err)
	}
	return nil
}

func (m *Manager) Status() (Status, error) {
	if !m.Enabled() {
		return Status{State: "disabled"}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var status Status
	if failure, err := os.ReadFile(m.failurePath()); err == nil {
		status.Failure = strings.TrimSpace(string(failure))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if _, err := os.Stat(m.rollbackPath()); err == nil {
		status.State = "rollback_pending"
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if pointer, err := m.readPointerLocked(m.activePath()); err == nil {
		release, readErr := m.readReleaseLocked(pointer)
		if readErr != nil {
			return Status{}, readErr
		}
		status.Current = &release
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if pointer, err := m.readPointerLocked(m.pendingPath()); err == nil {
		release, readErr := m.readReleaseLocked(pointer)
		if readErr != nil {
			return Status{}, readErr
		}
		status.Pending = &release
		status.State = "pending_restart"
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if status.State == "" {
		switch {
		case status.Failure != "":
			status.State = "failed"
		case status.Current != nil:
			status.State = "active"
		default:
			status.State = "built_in"
		}
	}
	return status, nil
}

func (m *Manager) stagePathLocked(ctx context.Context, archivePath string, archiveSize int64) (Release, error) {
	archiveSHA, err := sha256File(archivePath)
	if err != nil {
		return Release{}, fmt.Errorf("hash update package: %w", err)
	}
	manifest, files, extractedDir, err := extractArchive(ctx, archivePath, m.releasesDir(), m.maxUncompressedByte)
	if err != nil {
		return Release{}, err
	}
	defer os.RemoveAll(extractedDir)
	if err := validateManifest(manifest, files); err != nil {
		return Release{}, err
	}
	releaseID, err := randomID()
	if err != nil {
		return Release{}, fmt.Errorf("create release id: %w", err)
	}
	stagingDir := filepath.Join(m.releasesDir(), ".staging-"+releaseID)
	if err := os.Mkdir(stagingDir, 0700); err != nil {
		return Release{}, fmt.Errorf("create release directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	for _, name := range []string{ManifestFileName, ApplicationFileName, RecoveryFileName} {
		if err := copyFile(filepath.Join(files[name].Path), filepath.Join(stagingDir, name), name == ManifestFileName); err != nil {
			return Release{}, err
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Release{}, fmt.Errorf("marshal update manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, ManifestFileName), append(manifestBytes, '\n'), 0600); err != nil {
		return Release{}, fmt.Errorf("write release manifest: %w", err)
	}
	for _, name := range []string{ApplicationFileName, RecoveryFileName} {
		if err := os.Chmod(filepath.Join(stagingDir, name), 0700); err != nil {
			return Release{}, fmt.Errorf("make %s executable: %w", name, err)
		}
	}
	finalDir := filepath.Join(m.releasesDir(), "release-"+releaseID)
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return Release{}, fmt.Errorf("publish release: %w", err)
	}
	return Release{
		ID:              releaseID,
		Version:         manifest.Version,
		TargetOS:        manifest.TargetOS,
		TargetArch:      manifest.TargetArch,
		Path:            finalDir,
		ArchiveSHA256:   archiveSHA,
		ArchiveSizeByte: archiveSize,
		UploadedAt:      time.Now().UTC(),
	}, nil
}

type extractedFile struct {
	Path   string
	SHA256 string
	Size   int64
}

func extractArchive(ctx context.Context, archivePath, releasesDir string, maxUncompressed int64) (Manifest, map[string]extractedFile, string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, nil, "", fmt.Errorf("open update package: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return Manifest{}, nil, "", fmt.Errorf("%w: gzip archive is invalid", ErrInvalidPackage)
	}
	defer gzipReader.Close()

	stagingDir, err := os.MkdirTemp(releasesDir, ".extract-*")
	if err != nil {
		return Manifest{}, nil, "", fmt.Errorf("create extraction directory: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	files := make(map[string]extractedFile)
	reader := tar.NewReader(gzipReader)
	var total int64
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Manifest{}, nil, "", fmt.Errorf("%w: read tar archive: %v", ErrInvalidPackage, readErr)
		}
		if err := contextError(ctx); err != nil {
			return Manifest{}, nil, "", err
		}
		name := header.Name
		if name != ManifestFileName && name != ApplicationFileName && name != RecoveryFileName {
			return Manifest{}, nil, "", fmt.Errorf("%w: unexpected archive entry %q", ErrInvalidPackage, name)
		}
		if _, exists := files[name]; exists {
			return Manifest{}, nil, "", fmt.Errorf("%w: duplicate archive entry %q", ErrInvalidPackage, name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return Manifest{}, nil, "", fmt.Errorf("%w: archive entry %q is not a regular file", ErrInvalidPackage, name)
		}
		if header.Size < 0 || header.Size > maxUncompressed || total > maxUncompressed-header.Size {
			return Manifest{}, nil, "", fmt.Errorf("%w: uncompressed content exceeds limit", ErrInvalidPackage)
		}
		if name == ManifestFileName && header.Size > maxManifestBytes {
			return Manifest{}, nil, "", fmt.Errorf("%w: manifest is too large", ErrInvalidPackage)
		}
		destination := filepath.Join(stagingDir, name)
		out, createErr := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return Manifest{}, nil, "", fmt.Errorf("create archive entry: %w", createErr)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(out, hash), &contextReader{ctx: ctx, reader: io.LimitReader(reader, header.Size)})
		closeErr := out.Close()
		if copyErr != nil {
			return Manifest{}, nil, "", fmt.Errorf("extract archive entry %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return Manifest{}, nil, "", fmt.Errorf("close archive entry %q: %w", name, closeErr)
		}
		if written != header.Size {
			return Manifest{}, nil, "", fmt.Errorf("%w: archive entry %q is truncated", ErrInvalidPackage, name)
		}
		total += written
		files[name] = extractedFile{Path: destination, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: written}
	}
	manifestFile, ok := files[ManifestFileName]
	if !ok {
		return Manifest{}, nil, "", fmt.Errorf("%w: manifest.json is missing", ErrInvalidPackage)
	}
	manifestBytes, err := os.ReadFile(manifestFile.Path)
	if err != nil {
		return Manifest{}, nil, "", fmt.Errorf("read update manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, nil, "", fmt.Errorf("%w: manifest.json is invalid: %v", ErrInvalidPackage, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, nil, "", fmt.Errorf("%w: manifest.json contains trailing data", ErrInvalidPackage)
	}
	success = true
	return manifest, files, stagingDir, nil
}

func validateManifest(manifest Manifest, files map[string]extractedFile) error {
	if manifest.FormatVersion != 1 {
		return fmt.Errorf("%w: unsupported manifest format %d", ErrInvalidPackage, manifest.FormatVersion)
	}
	if !validVersion(manifest.Version) {
		return fmt.Errorf("%w: version is empty or invalid", ErrInvalidPackage)
	}
	if manifest.TargetOS != runtime.GOOS || manifest.TargetArch != runtime.GOARCH {
		return fmt.Errorf("%w: package targets %s/%s, runtime is %s/%s", ErrUnsupportedTarget, manifest.TargetOS, manifest.TargetArch, runtime.GOOS, runtime.GOARCH)
	}
	expected := map[string]struct{}{ApplicationFileName: {}, RecoveryFileName: {}}
	if len(manifest.Artifacts) != len(expected) {
		return fmt.Errorf("%w: manifest must describe omnora and omnora-recovery", ErrInvalidPackage)
	}
	for name := range expected {
		spec, ok := manifest.Artifacts[name]
		if !ok || spec.Path != name || !validSHA256(spec.SHA256) || spec.Size <= 0 {
			return fmt.Errorf("%w: artifact %q has invalid metadata", ErrInvalidPackage, name)
		}
		actual, ok := files[name]
		if !ok || actual.Size != spec.Size || !strings.EqualFold(actual.SHA256, spec.SHA256) {
			return fmt.Errorf("%w: artifact %q failed size or SHA-256 verification", ErrInvalidPackage, name)
		}
	}
	return nil
}

func (m *Manager) readReleaseLocked(releasePath string) (Release, error) {
	if err := m.validateReleasePath(releasePath); err != nil {
		return Release{}, err
	}
	manifestBytes, err := os.ReadFile(filepath.Join(releasePath, ManifestFileName))
	if err != nil {
		return Release{}, fmt.Errorf("read release manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Release{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := validateManifestFiles(releasePath, manifest); err != nil {
		return Release{}, err
	}
	return Release{ID: filepath.Base(releasePath), Version: manifest.Version, TargetOS: manifest.TargetOS, TargetArch: manifest.TargetArch, Path: releasePath}, nil
}

func validateManifestFiles(releasePath string, manifest Manifest) error {
	if manifest.FormatVersion != 1 || !validVersion(manifest.Version) || manifest.TargetOS != runtime.GOOS || manifest.TargetArch != runtime.GOARCH {
		return fmt.Errorf("%w: installed release manifest is invalid", ErrInvalidPackage)
	}
	if len(manifest.Artifacts) != 2 {
		return fmt.Errorf("%w: installed release artifact list is invalid", ErrInvalidPackage)
	}
	for _, name := range []string{ApplicationFileName, RecoveryFileName} {
		spec, ok := manifest.Artifacts[name]
		if !ok || spec.Path != name || !validSHA256(spec.SHA256) || spec.Size <= 0 {
			return fmt.Errorf("%w: installed release metadata for %q is invalid", ErrInvalidPackage, name)
		}
		fileInfo, err := os.Lstat(filepath.Join(releasePath, name))
		if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&0111 == 0 || fileInfo.Size() != spec.Size {
			return fmt.Errorf("%w: installed release executable %q is invalid", ErrInvalidPackage, name)
		}
		actualSHA, err := sha256File(filepath.Join(releasePath, name))
		if err != nil || !strings.EqualFold(actualSHA, spec.SHA256) {
			return fmt.Errorf("%w: installed release executable %q failed SHA-256 verification", ErrInvalidPackage, name)
		}
	}
	return nil
}

func (m *Manager) validateReleasePath(value string) error {
	clean := filepath.Clean(value)
	prefix := m.releasesDir() + string(os.PathSeparator)
	if !strings.HasPrefix(clean, prefix) || clean == m.releasesDir() || strings.Contains(filepath.Base(clean), string(os.PathSeparator)) {
		return fmt.Errorf("%w: release path is outside update storage", ErrInvalidPackage)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: release directory is invalid", ErrInvalidPackage)
	}
	return nil
}

func (m *Manager) ensureDirsLocked() error {
	if err := os.MkdirAll(m.releasesDir(), 0700); err != nil {
		return err
	}
	return os.MkdirAll(m.incomingDir(), 0700)
}

func (m *Manager) releasesDir() string  { return filepath.Join(m.dir, "releases") }
func (m *Manager) incomingDir() string  { return filepath.Join(m.dir, "incoming") }
func (m *Manager) pendingPath() string  { return filepath.Join(m.dir, "pending") }
func (m *Manager) activePath() string   { return filepath.Join(m.dir, "active") }
func (m *Manager) previousPath() string { return filepath.Join(m.dir, "previous") }
func (m *Manager) rollbackPath() string { return filepath.Join(m.dir, "rollback") }
func (m *Manager) failurePath() string  { return filepath.Join(m.dir, FailureFileName) }

func (m *Manager) readPointerLocked(pointerPath string) (string, error) {
	contents, err := os.ReadFile(pointerPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", fmt.Errorf("%w: update pointer is invalid", ErrInvalidPackage)
	}
	value := filepath.Clean(strings.TrimSpace(lines[0]))
	if err := m.validateReleasePath(value); err != nil {
		return "", err
	}
	return value, nil
}

func writePointerAtomic(pointerPath, value string) error {
	directory := filepath.Dir(pointerPath)
	file, err := os.CreateTemp(directory, ".pointer-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := io.WriteString(file, value+"\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, pointerPath)
}

func writeMarkerAtomic(markerPath string) error {
	file, err := os.CreateTemp(filepath.Dir(markerPath), ".marker-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, markerPath)
}

func copyFile(source, destination string, manifest bool) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open staged %s: %w", filepath.Base(source), err)
	}
	defer input.Close()
	mode := os.FileMode(0700)
	if manifest {
		mode = 0600
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create release %s: %w", filepath.Base(source), err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy release %s: %w", filepath.Base(source), err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close release %s: %w", filepath.Base(source), err)
	}
	return nil
}

func sha256File(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := io.ReadFull(crand.Reader, bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func validVersion(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '-' || char == '_' || (index == 0 && char == 'v') {
			continue
		}
		return false
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func contextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := contextError(r.ctx); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
