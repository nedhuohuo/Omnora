package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ManifestFileName       = "manifest.json"
	SignatureFileName      = "manifest.sig"
	ApplicationFileName    = "omnora"
	RecoveryFileName       = "omnora-recovery"
	ReleaseMetadataName    = "release.json"
	ReleaseVersionName     = "version"
	FailureFileName        = "failure"
	DefaultMaxPackageBytes = int64(512 << 20)
	maxManifestBytes       = int64(128 << 10)
)

var (
	ErrDisabled           = errors.New("self-update is disabled")
	ErrPendingUpdate      = errors.New("an update is already pending restart")
	ErrUpdateBusy         = errors.New("another update operation is in progress")
	ErrNoRollback         = errors.New("no installed update can be rolled back")
	ErrInvalidPackage     = errors.New("invalid update package")
	ErrInvalidSignature   = errors.New("update package signature is invalid")
	ErrUnsupportedTarget  = errors.New("update package target does not match this runtime")
	ErrIncompatibleSchema = errors.New("update package schema is incompatible")
	ErrVersionNotNewer    = errors.New("update package version is not newer")
)

// Manifest is the signed-content boundary for an update package. Web updates
// intentionally require the exact live schema version so binary rollback never
// leaves an older executable facing a database changed by the candidate.
type Manifest struct {
	FormatVersion int                         `json:"formatVersion"`
	Version       string                      `json:"version"`
	TargetOS      string                      `json:"targetOS"`
	TargetArch    string                      `json:"targetArch"`
	SchemaVersion int64                       `json:"schemaVersion"`
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
	SchemaVersion   int64     `json:"schemaVersion"`
	Path            string    `json:"-"`
	ArchiveSHA256   string    `json:"archiveSha256,omitempty"`
	ArchiveSizeByte int64     `json:"archiveSizeBytes,omitempty"`
	UploadedAt      time.Time `json:"uploadedAt,omitempty"`
	BackupID        string    `json:"backupId,omitempty"`
}

type Status struct {
	State     string   `json:"state"`
	Current   *Release `json:"current,omitempty"`
	Preparing *Release `json:"preparing,omitempty"`
	Pending   *Release `json:"pending,omitempty"`
	Failure   string   `json:"failure,omitempty"`
}

type Manager struct {
	dir                 string
	maxPackageBytes     int64
	maxUncompressedByte int64
	currentVersion      string
	currentSchema       int64
	signingPublicKey    ed25519.PublicKey
	mu                  sync.Mutex
}

// Preparation holds the cross-process update lock across backup and audit work.
// A second process therefore cannot mistake a live preparation for an orphan.
type Preparation struct {
	manager     *Manager
	releasePath string
	stateLock   *os.File
	mu          sync.Mutex
	closed      bool
}

type Option func(*Manager)

func WithCurrentVersion(version string) Option {
	return func(manager *Manager) { manager.currentVersion = strings.TrimSpace(version) }
}

func WithCurrentSchemaVersion(version int64) Option {
	return func(manager *Manager) { manager.currentSchema = version }
}

func WithSigningPublicKey(key ed25519.PublicKey) Option {
	return func(manager *Manager) { manager.signingPublicKey = append(ed25519.PublicKey(nil), key...) }
}

func NewManager(dir string, maxPackageBytes int64, options ...Option) *Manager {
	if maxPackageBytes <= 0 {
		maxPackageBytes = DefaultMaxPackageBytes
	}
	maxUncompressed := maxPackageBytes * 4
	if maxUncompressed < 1<<30 {
		maxUncompressed = 1 << 30
	}
	manager := &Manager{
		dir:                 filepath.Clean(strings.TrimSpace(dir)),
		maxPackageBytes:     maxPackageBytes,
		maxUncompressedByte: maxUncompressed,
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func LoadSigningPublicKey(filePath string) (ed25519.PublicKey, error) {
	filePath = strings.TrimSpace(filePath)
	before, err := os.Lstat(filePath)
	if err != nil {
		return nil, fmt.Errorf("inspect update signing public key: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 {
		return nil, errors.New("update signing public key must be a regular file that is not a symlink or group/other-writable")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open update signing public key: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("update signing public key changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return nil, fmt.Errorf("read update signing public key: %w", err)
	}
	block, rest := pem.Decode(contents)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("update signing public key must contain one PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse update signing public key: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("update signing public key must be Ed25519")
	}
	return append(ed25519.PublicKey(nil), key...), nil
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureDirsLocked()
}

func (m *Manager) acquireStateLockLocked() (*os.File, error) {
	file, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open update state lock: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect update state lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrUpdateBusy
		}
		return nil, fmt.Errorf("acquire update state lock: %w", err)
	}
	return file, nil
}

func releaseStateLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
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
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return Release{}, err
	}
	defer releaseStateLock(stateLock)
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

func (m *Manager) BeginPreparation(release Release) (*Preparation, error) {
	if !m.Enabled() {
		return nil, ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return nil, err
	}
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			releaseStateLock(stateLock)
		}
	}()
	if _, err := m.readReleaseLocked(release.Path); err != nil {
		return nil, err
	}
	for _, pointerPath := range []string{m.preparingPath(), m.pendingPath()} {
		if _, err := m.readPointerLocked(pointerPath); err == nil {
			return nil, ErrPendingUpdate
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if _, err := os.Stat(m.rollbackPath()); err == nil {
		return nil, ErrUpdateBusy
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := writePointerAtomic(m.preparingPath(), release.Path); err != nil {
		return nil, err
	}
	failed = false
	return &Preparation{manager: m, releasePath: release.Path, stateLock: stateLock}, nil
}

func (p *Preparation) Commit(release Release) error {
	if p == nil || p.manager == nil {
		return ErrDisabled
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrUpdateBusy
	}
	if filepath.Clean(p.releasePath) != filepath.Clean(release.Path) {
		return ErrPendingUpdate
	}
	p.manager.mu.Lock()
	err := p.manager.commitPreparedLocked(release)
	p.manager.mu.Unlock()
	if err != nil {
		return err
	}
	p.closed = true
	releaseStateLock(p.stateLock)
	p.stateLock = nil
	return nil
}

func (p *Preparation) Cancel() error {
	if p == nil || p.manager == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.manager.mu.Lock()
	current, err := p.manager.readPointerLocked(p.manager.preparingPath())
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(p.manager.preparingPath()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = removeErr
		} else {
			err = nil
		}
	} else if err == nil && filepath.Clean(current) == filepath.Clean(p.releasePath) {
		err = os.Remove(p.manager.preparingPath())
	} else if err == nil {
		err = ErrPendingUpdate
	}
	p.manager.mu.Unlock()
	p.closed = true
	releaseStateLock(p.stateLock)
	p.stateLock = nil
	return err
}

func (m *Manager) RecoverInterruptedPreparation() error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return err
	}
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return err
	}
	defer releaseStateLock(stateLock)
	if _, err := os.Lstat(m.preparingPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	releasePath, readErr := m.readPointerLocked(m.preparingPath())
	if err := os.Remove(m.preparingPath()); err != nil {
		return fmt.Errorf("clear interrupted preparation: %w", err)
	}
	if readErr != nil {
		return writeTextAtomic(m.failurePath(), "invalid interrupted update preparation was discarded\n", 0600)
	}
	referenced := false
	for _, pointerPath := range []string{m.pendingPath(), m.activePath(), m.previousPath()} {
		value, readErr := m.readPointerLocked(pointerPath)
		if readErr == nil && filepath.Clean(value) == filepath.Clean(releasePath) {
			referenced = true
			break
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
	}
	if !referenced {
		if err := m.validateReleasePath(releasePath); err == nil {
			if err := os.RemoveAll(releasePath); err != nil {
				return fmt.Errorf("discard interrupted release: %w", err)
			}
		}
	}
	return writeTextAtomic(m.failurePath(), "interrupted update preparation was discarded\n", 0600)
}

func (m *Manager) commitPreparedLocked(release Release) error {
	preparedPath, err := m.readPointerLocked(m.preparingPath())
	if err != nil {
		return fmt.Errorf("read prepared update: %w", err)
	}
	if filepath.Clean(preparedPath) != filepath.Clean(release.Path) {
		return ErrPendingUpdate
	}
	if _, err := m.readPointerLocked(m.pendingPath()); err == nil {
		return ErrPendingUpdate
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.writeReleaseMetadataLocked(release); err != nil {
		return err
	}
	if err := os.Rename(m.preparingPath(), m.pendingPath()); err != nil {
		return fmt.Errorf("publish pending update: %w", err)
	}
	_ = os.Remove(m.failurePath())
	return nil
}

// Activate is retained for non-HTTP callers that do not need to insert work
// between reservation and commit.
func (m *Manager) Activate(release Release) error {
	preparation, err := m.BeginPreparation(release)
	if err != nil {
		return err
	}
	defer preparation.Cancel()
	return preparation.Commit(release)
}

func (m *Manager) DiscardRelease(release Release) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return err
	}
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return err
	}
	defer releaseStateLock(stateLock)
	for _, pointerPath := range []string{m.preparingPath(), m.pendingPath(), m.activePath(), m.previousPath()} {
		value, readErr := m.readPointerLocked(pointerPath)
		if readErr == nil && filepath.Clean(value) == filepath.Clean(release.Path) {
			return ErrPendingUpdate
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
	}
	if err := m.validateReleasePath(release.Path); err != nil {
		return err
	}
	return os.RemoveAll(release.Path)
}

func (m *Manager) RequestRollback() error {
	_, err := m.RequestRollbackToken()
	return err
}

func (m *Manager) RequestRollbackToken() (string, error) {
	if !m.Enabled() {
		return "", ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return "", err
	}
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return "", err
	}
	defer releaseStateLock(stateLock)
	for _, pointerPath := range []string{m.preparingPath(), m.pendingPath()} {
		if _, err := m.readPointerLocked(pointerPath); err == nil {
			return "", ErrUpdateBusy
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if _, err := os.Stat(m.rollbackPath()); err == nil {
		return "", ErrUpdateBusy
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := m.readPointerLocked(m.activePath()); errors.Is(err, os.ErrNotExist) {
		if _, previousErr := m.readPointerLocked(m.previousPath()); errors.Is(previousErr, os.ErrNotExist) {
			return "", ErrNoRollback
		}
	} else if err != nil {
		return "", err
	}
	token, err := randomID()
	if err != nil {
		return "", err
	}
	if err := writeTextAtomic(m.rollbackPath(), token+"\n", 0600); err != nil {
		return "", fmt.Errorf("publish rollback request: %w", err)
	}
	return token, nil
}

func (m *Manager) CancelRollback(token string) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return err
	}
	stateLock, err := m.acquireStateLockLocked()
	if err != nil {
		return err
	}
	defer releaseStateLock(stateLock)
	contents, err := os.ReadFile(m.rollbackPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(contents)) != strings.TrimSpace(token) {
		return ErrUpdateBusy
	}
	return os.Remove(m.rollbackPath())
}

func (m *Manager) Status() (Status, error) {
	if !m.Enabled() {
		return Status{State: "disabled"}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureDirsLocked(); err != nil {
		return Status{}, err
	}
	var status Status
	if failure, err := os.ReadFile(m.failurePath()); err == nil {
		status.Failure = strings.TrimSpace(string(failure))
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	rollbackPending := false
	if _, err := os.Stat(m.rollbackPath()); err == nil {
		rollbackPending = true
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
	if pointer, err := m.readPointerLocked(m.preparingPath()); err == nil {
		release, readErr := m.readReleaseLocked(pointer)
		if readErr != nil {
			return Status{}, readErr
		}
		status.Preparing = &release
		status.State = "preparing"
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
	if rollbackPending {
		status.State = "rollback_pending"
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
	if err := m.verifyManifestSignature(files); err != nil {
		return Release{}, err
	}
	if err := m.validateManifest(manifest, files); err != nil {
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

	for _, name := range []string{ManifestFileName, SignatureFileName, ApplicationFileName, RecoveryFileName} {
		file, exists := files[name]
		if !exists {
			continue
		}
		if err := copyFile(file.Path, filepath.Join(stagingDir, name), name == ManifestFileName || name == SignatureFileName); err != nil {
			return Release{}, err
		}
	}
	for _, name := range []string{ApplicationFileName, RecoveryFileName} {
		if err := os.Chmod(filepath.Join(stagingDir, name), 0700); err != nil {
			return Release{}, fmt.Errorf("make %s executable: %w", name, err)
		}
	}
	finalDir := filepath.Join(m.releasesDir(), "release-"+releaseID)
	release := Release{
		ID:              releaseID,
		Version:         manifest.Version,
		TargetOS:        manifest.TargetOS,
		TargetArch:      manifest.TargetArch,
		SchemaVersion:   manifest.SchemaVersion,
		Path:            finalDir,
		ArchiveSHA256:   archiveSHA,
		ArchiveSizeByte: archiveSize,
		UploadedAt:      time.Now().UTC(),
	}
	if err := writeReleaseFiles(stagingDir, release); err != nil {
		return Release{}, err
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return Release{}, fmt.Errorf("publish release: %w", err)
	}
	return release, nil
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
		if name != ManifestFileName && name != SignatureFileName && name != ApplicationFileName && name != RecoveryFileName {
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
		if name == SignatureFileName && header.Size != ed25519.SignatureSize {
			return Manifest{}, nil, "", fmt.Errorf("%w: manifest signature has an invalid size", ErrInvalidPackage)
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

func (m *Manager) verifyManifestSignature(files map[string]extractedFile) error {
	if len(m.signingPublicKey) == 0 {
		return nil
	}
	manifestFile, manifestOK := files[ManifestFileName]
	signatureFile, signatureOK := files[SignatureFileName]
	if !manifestOK || !signatureOK {
		return fmt.Errorf("%w: manifest signature is missing", ErrInvalidSignature)
	}
	manifestBytes, err := os.ReadFile(manifestFile.Path)
	if err != nil {
		return fmt.Errorf("read signed manifest: %w", err)
	}
	signature, err := os.ReadFile(signatureFile.Path)
	if err != nil {
		return fmt.Errorf("read manifest signature: %w", err)
	}
	if !ed25519.Verify(m.signingPublicKey, manifestBytes, signature) {
		return ErrInvalidSignature
	}
	return nil
}

func (m *Manager) validateManifest(manifest Manifest, files map[string]extractedFile) error {
	if manifest.FormatVersion != 1 {
		return fmt.Errorf("%w: unsupported manifest format %d", ErrInvalidPackage, manifest.FormatVersion)
	}
	if !validVersion(manifest.Version) || (len(m.signingPublicKey) > 0 && !IsReleaseVersion(manifest.Version)) {
		return fmt.Errorf("%w: version is empty or invalid", ErrInvalidPackage)
	}
	if manifest.TargetOS != runtime.GOOS || manifest.TargetArch != runtime.GOARCH {
		return fmt.Errorf("%w: package targets %s/%s, runtime is %s/%s", ErrUnsupportedTarget, manifest.TargetOS, manifest.TargetArch, runtime.GOOS, runtime.GOARCH)
	}
	if m.currentSchema > 0 && manifest.SchemaVersion != m.currentSchema {
		return fmt.Errorf("%w: package schema %d, live schema %d", ErrIncompatibleSchema, manifest.SchemaVersion, m.currentSchema)
	}
	if knownVersion(m.currentVersion) && compareVersions(manifest.Version, m.currentVersion) <= 0 {
		return fmt.Errorf("%w: package %s, current %s", ErrVersionNotNewer, manifest.Version, m.currentVersion)
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
	if len(m.signingPublicKey) > 0 {
		signature, readErr := os.ReadFile(filepath.Join(releasePath, SignatureFileName))
		if readErr != nil || !ed25519.Verify(m.signingPublicKey, manifestBytes, signature) {
			return Release{}, ErrInvalidSignature
		}
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Release{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := validateManifestFiles(releasePath, manifest); err != nil {
		return Release{}, err
	}
	release := Release{
		ID:            strings.TrimPrefix(filepath.Base(releasePath), "release-"),
		Version:       manifest.Version,
		TargetOS:      manifest.TargetOS,
		TargetArch:    manifest.TargetArch,
		SchemaVersion: manifest.SchemaVersion,
		Path:          releasePath,
	}
	metadata, metadataErr := os.ReadFile(filepath.Join(releasePath, ReleaseMetadataName))
	if metadataErr == nil {
		if err := json.Unmarshal(metadata, &release); err != nil {
			return Release{}, fmt.Errorf("decode release metadata: %w", err)
		}
	} else if !errors.Is(metadataErr, os.ErrNotExist) {
		return Release{}, fmt.Errorf("read release metadata: %w", metadataErr)
	}
	release.ID = strings.TrimPrefix(filepath.Base(releasePath), "release-")
	release.Version = manifest.Version
	release.TargetOS = manifest.TargetOS
	release.TargetArch = manifest.TargetArch
	release.SchemaVersion = manifest.SchemaVersion
	release.Path = releasePath
	return release, nil
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

func (m *Manager) writeReleaseMetadataLocked(release Release) error {
	if err := m.validateReleasePath(release.Path); err != nil {
		return err
	}
	return writeReleaseFiles(release.Path, release)
}

func writeReleaseFiles(directory string, release Release) error {
	metadata, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release metadata: %w", err)
	}
	if err := writeTextAtomic(filepath.Join(directory, ReleaseMetadataName), string(metadata)+"\n", 0600); err != nil {
		return fmt.Errorf("write release metadata: %w", err)
	}
	if err := writeTextAtomic(filepath.Join(directory, ReleaseVersionName), release.Version+"\n", 0600); err != nil {
		return fmt.Errorf("write release version: %w", err)
	}
	return nil
}

func (m *Manager) releasesDir() string   { return filepath.Join(m.dir, "releases") }
func (m *Manager) incomingDir() string   { return filepath.Join(m.dir, "incoming") }
func (m *Manager) preparingPath() string { return filepath.Join(m.dir, "preparing") }
func (m *Manager) pendingPath() string   { return filepath.Join(m.dir, "pending") }
func (m *Manager) activePath() string    { return filepath.Join(m.dir, "active") }
func (m *Manager) previousPath() string  { return filepath.Join(m.dir, "previous") }
func (m *Manager) rollbackPath() string  { return filepath.Join(m.dir, "rollback") }
func (m *Manager) failurePath() string   { return filepath.Join(m.dir, FailureFileName) }
func (m *Manager) lockPath() string      { return filepath.Join(m.dir, "update.lock") }

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
	return writeTextAtomic(pointerPath, value+"\n", 0600)
}

func writeTextAtomic(targetPath, contents string, mode os.FileMode) error {
	directory := filepath.Dir(targetPath)
	file, err := os.CreateTemp(directory, ".atomic-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := io.WriteString(file, contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
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
	if err := os.Rename(temporary, targetPath); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(directory string) error {
	opened, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer opened.Close()
	return opened.Sync()
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

func IsReleaseVersion(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		value = strings.TrimPrefix(value, "v")
	}
	if value == "" || value[0] < '0' || value[0] > '9' {
		return false
	}
	separator := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			separator = false
		case char == '.', char == '-', char == '_':
			if separator {
				return false
			}
			separator = true
		default:
			return false
		}
	}
	return !separator
}

func knownVersion(value string) bool {
	return IsReleaseVersion(value)
}

func compareVersions(left, right string) int {
	leftParts := versionParts(left)
	rightParts := versionParts(right)
	maxParts := len(leftParts)
	if len(rightParts) > maxParts {
		maxParts = len(rightParts)
	}
	for index := 0; index < maxParts; index++ {
		leftPart := "0"
		rightPart := "0"
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		leftNumeric := numericPart(leftPart)
		rightNumeric := numericPart(rightPart)
		switch {
		case leftNumeric && rightNumeric:
			if comparison := compareNumericParts(leftPart, rightPart); comparison != 0 {
				return comparison
			}
		case leftNumeric && !rightNumeric:
			return 1
		case !leftNumeric && rightNumeric:
			return -1
		case leftPart < rightPart:
			return -1
		case leftPart > rightPart:
			return 1
		}
	}
	return 0
}

func versionParts(value string) []string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "v")
	parts := make([]string, 0, 8)
	start := -1
	digits := false
	for index, char := range value {
		isDigit := char >= '0' && char <= '9'
		isLetter := char >= 'a' && char <= 'z'
		if !isDigit && !isLetter {
			if start >= 0 {
				parts = append(parts, value[start:index])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = index
			digits = isDigit
			continue
		}
		if digits != isDigit {
			parts = append(parts, value[start:index])
			start = index
			digits = isDigit
		}
	}
	if start >= 0 {
		parts = append(parts, value[start:])
	}
	return parts
}

func numericPart(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func compareNumericParts(left, right string) int {
	left = strings.TrimLeft(left, "0")
	right = strings.TrimLeft(right, "0")
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
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
