package transfer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnora/internal/storage"
)

const (
	defaultBufferSize = 32 * 1024
	manifestName      = "manifest.json"
)

var (
	ErrInvalidMountRoot  = errors.New("invalid mount root")
	ErrInvalidTempRoot   = errors.New("invalid temp root")
	ErrInvalidTargetPath = errors.New("invalid target path")
	ErrInvalidSessionID  = errors.New("invalid upload session id")
	ErrInvalidPartNumber = errors.New("invalid upload part number")
	ErrSessionNotFound   = errors.New("upload session not found")
	ErrUploadTooLarge    = errors.New("upload exceeds expected size")
	ErrIncompleteUpload  = errors.New("upload is incomplete")
	ErrTargetExists      = errors.New("upload target already exists")
	ErrChecksumMismatch  = errors.New("upload checksum mismatch")
)

type Service struct {
	root       *os.Root
	tempRoot   string
	bufferSize int
	now        func() time.Time
	mu         sync.Mutex
}

type Options struct {
	MountRoot  string
	TempRoot   string
	BufferSize int
	Clock      func() time.Time
}

type CreateUploadSessionRequest struct {
	TargetPath   string
	ExpectedSize int64
	Overwrite    bool
	Checksum     string
}

type UploadSession struct {
	ID           string
	TargetPath   string
	ExpectedSize int64
	ReceivedSize int64
	Parts        []UploadPart
	CreatedAt    time.Time
	Checksum     string
}

type UploadPart struct {
	Number int
	Size   int64
}

type CompletedUpload struct {
	TargetPath string
	Size       int64
	Checksum   string
}

type uploadManifest struct {
	ID           string           `json:"id"`
	TargetPath   string           `json:"targetPath"`
	ExpectedSize int64            `json:"expectedSize"`
	Overwrite    bool             `json:"overwrite"`
	CreatedAt    time.Time        `json:"createdAt"`
	Parts        map[string]int64 `json:"parts"`
	Checksum     string           `json:"checksum,omitempty"`
}

func NewService(options Options) (*Service, error) {
	mountRoot := filepath.Clean(options.MountRoot)
	if options.MountRoot == "" || mountRoot == "." {
		return nil, ErrInvalidMountRoot
	}
	root, err := os.OpenRoot(mountRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMountRoot, err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()

	tempRoot := filepath.Clean(options.TempRoot)
	if options.TempRoot == "" || tempRoot == "." {
		return nil, ErrInvalidTempRoot
	}
	if !isPathWithin(mountRoot, tempRoot) {
		return nil, ErrInvalidTempRoot
	}
	tempRelative, err := filepath.Rel(mountRoot, tempRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTempRoot, err)
	}
	tempRelative = filepath.ToSlash(tempRelative)
	if !isSafeInternalRelativePath(tempRelative) {
		return nil, ErrInvalidTempRoot
	}

	bufferSize := options.BufferSize
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}

	if err := ensureRootDirectory(root, tempRelative); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTempRoot, err)
	}

	service := &Service{
		root:       root,
		tempRoot:   tempRelative,
		bufferSize: bufferSize,
		now:        clock,
	}
	closeRoot = false
	return service, nil
}

// Close releases the mount-root descriptor retained by the upload session.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func (s *Service) CreateUploadSession(req CreateUploadSessionRequest) (UploadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return UploadSession{}, ErrInvalidMountRoot
	}

	if req.ExpectedSize < 0 {
		return UploadSession{}, ErrInvalidTargetPath
	}
	targetPath, err := cleanTargetPath(req.TargetPath)
	if err != nil {
		return UploadSession{}, err
	}
	if err := s.validateTarget(targetPath, req.Overwrite); err != nil {
		return UploadSession{}, err
	}

	id, err := newSessionID()
	if err != nil {
		return UploadSession{}, err
	}
	checksum := strings.ToLower(strings.TrimSpace(req.Checksum))
	checksum = strings.TrimPrefix(checksum, "sha256:")
	manifest := uploadManifest{
		ID:           id,
		TargetPath:   targetPath,
		ExpectedSize: req.ExpectedSize,
		Overwrite:    req.Overwrite,
		CreatedAt:    s.now().UTC(),
		Parts:        map[string]int64{},
		Checksum:     checksum,
	}
	if manifest.Checksum != "" && len(manifest.Checksum) != sha256.Size*2 {
		return UploadSession{}, ErrInvalidTargetPath
	}
	for _, r := range manifest.Checksum {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return UploadSession{}, ErrInvalidTargetPath
		}
	}

	if err := s.root.Mkdir(s.sessionDir(id), 0o700); err != nil {
		return UploadSession{}, err
	}
	if err := s.saveManifest(manifest); err != nil {
		_ = s.root.RemoveAll(s.sessionDir(id))
		return UploadSession{}, err
	}
	return manifest.session(), nil
}

func (s *Service) ResumeUploadSession(sessionID string) (UploadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return UploadSession{}, ErrInvalidMountRoot
	}

	manifest, err := s.loadManifest(sessionID)
	if err != nil {
		return UploadSession{}, err
	}
	if err := s.verifyPartFiles(manifest); err != nil {
		return UploadSession{}, err
	}
	return manifest.session(), nil
}

func (s *Service) WritePart(sessionID string, number int, reader io.Reader) (UploadPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return UploadPart{}, ErrInvalidMountRoot
	}

	if number <= 0 {
		return UploadPart{}, ErrInvalidPartNumber
	}
	manifest, err := s.loadManifest(sessionID)
	if err != nil {
		return UploadPart{}, err
	}
	oldPartSize := manifest.Parts[strconv.Itoa(number)]
	availableSize := manifest.ExpectedSize - manifest.receivedSize() + oldPartSize

	tempFile, tempName, err := s.createTempFile(sessionID, fmt.Sprintf("part-%08d-", number))
	if err != nil {
		return UploadPart{}, err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = s.root.Remove(tempName)
		}
	}()

	size, err := copyWithLimit(tempFile, reader, availableSize, s.bufferSize)
	closeErr := tempFile.Close()
	if err != nil {
		return UploadPart{}, err
	}
	if closeErr != nil {
		return UploadPart{}, closeErr
	}

	partName := s.partPath(sessionID, number)
	if err := s.root.Rename(tempName, partName); err != nil {
		return UploadPart{}, err
	}
	removeTemp = false

	if manifest.Parts == nil {
		manifest.Parts = map[string]int64{}
	}
	manifest.Parts[strconv.Itoa(number)] = size
	if err := s.saveManifest(manifest); err != nil {
		return UploadPart{}, err
	}

	return UploadPart{Number: number, Size: size}, nil
}

func (s *Service) CompleteUpload(sessionID string) (CompletedUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return CompletedUpload{}, ErrInvalidMountRoot
	}

	manifest, err := s.loadManifest(sessionID)
	if err != nil {
		return CompletedUpload{}, err
	}
	if err := s.verifyComplete(manifest); err != nil {
		return CompletedUpload{}, err
	}

	if err := s.validateTarget(manifest.TargetPath, manifest.Overwrite); err != nil {
		return CompletedUpload{}, err
	}

	finalFile, finalName, err := s.createTempFile(sessionID, "final-")
	if err != nil {
		return CompletedUpload{}, err
	}
	removeFinal := true
	defer func() {
		if removeFinal {
			_ = s.root.Remove(finalName)
		}
	}()

	written, err := s.assembleParts(finalFile, manifest)
	if closeErr := finalFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return CompletedUpload{}, err
	}
	if written != manifest.ExpectedSize {
		return CompletedUpload{}, ErrIncompleteUpload
	}
	if manifest.Checksum != "" {
		actual, checksumErr := s.fileChecksum(finalName)
		if checksumErr != nil {
			return CompletedUpload{}, checksumErr
		}
		if !strings.EqualFold(actual, manifest.Checksum) {
			return CompletedUpload{}, ErrChecksumMismatch
		}
	}

	if err := s.publish(finalName, manifest.TargetPath, manifest.Overwrite); err != nil {
		return CompletedUpload{}, err
	}
	removeFinal = false
	_ = s.root.RemoveAll(s.sessionDir(sessionID))

	return CompletedUpload{
		TargetPath: manifest.TargetPath,
		Size:       written,
		Checksum:   manifest.Checksum,
	}, nil
}

func (s *Service) CancelUpload(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return ErrInvalidMountRoot
	}

	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := s.root.RemoveAll(s.sessionDir(sessionID)); err != nil {
		return err
	}
	return nil
}

func (s *Service) assembleParts(writer io.Writer, manifest uploadManifest) (int64, error) {
	var written int64
	buffer := make([]byte, s.bufferSize)
	for number := 1; number <= len(manifest.Parts); number++ {
		partFile, err := s.root.Open(s.partPath(manifest.ID, number))
		if err != nil {
			return written, err
		}
		n, copyErr := io.CopyBuffer(writer, partFile, buffer)
		closeErr := partFile.Close()
		written += n
		if copyErr != nil {
			return written, copyErr
		}
		if closeErr != nil {
			return written, closeErr
		}
	}
	return written, nil
}

func (s *Service) fileChecksum(relativePath string) (string, error) {
	file, err := s.root.Open(relativePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, file, make([]byte, s.bufferSize)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func (s *Service) verifyComplete(manifest uploadManifest) error {
	if err := s.verifyPartFiles(manifest); err != nil {
		return err
	}
	if manifest.ExpectedSize == 0 {
		if len(manifest.Parts) == 0 {
			return nil
		}
		return ErrUploadTooLarge
	}
	if len(manifest.Parts) == 0 {
		return ErrIncompleteUpload
	}
	for number := 1; number <= len(manifest.Parts); number++ {
		if _, ok := manifest.Parts[strconv.Itoa(number)]; !ok {
			return ErrIncompleteUpload
		}
	}
	if manifest.receivedSize() != manifest.ExpectedSize {
		return ErrIncompleteUpload
	}
	return nil
}

func (s *Service) verifyPartFiles(manifest uploadManifest) error {
	for partNumber, size := range manifest.Parts {
		number, err := strconv.Atoi(partNumber)
		if err != nil || number <= 0 {
			return ErrInvalidPartNumber
		}
		info, err := s.root.Lstat(s.partPath(manifest.ID, number))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ErrIncompleteUpload
			}
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != size {
			return ErrIncompleteUpload
		}
	}
	return nil
}

func (s *Service) loadManifest(sessionID string) (uploadManifest, error) {
	if err := validateSessionID(sessionID); err != nil {
		return uploadManifest{}, err
	}
	file, err := s.root.Open(s.manifestPath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return uploadManifest{}, ErrSessionNotFound
		}
		return uploadManifest{}, err
	}
	defer file.Close()

	var manifest uploadManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return uploadManifest{}, err
	}
	if manifest.ID != sessionID {
		return uploadManifest{}, ErrInvalidSessionID
	}
	if _, err := cleanTargetPath(manifest.TargetPath); err != nil {
		return uploadManifest{}, err
	}
	if manifest.ExpectedSize < 0 {
		return uploadManifest{}, ErrInvalidTargetPath
	}
	if manifest.Checksum != "" {
		if len(manifest.Checksum) != sha256.Size*2 {
			return uploadManifest{}, ErrInvalidTargetPath
		}
		for _, r := range manifest.Checksum {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return uploadManifest{}, ErrInvalidTargetPath
			}
		}
	}
	if manifest.Parts == nil {
		manifest.Parts = map[string]int64{}
	}
	return manifest, nil
}

func (s *Service) saveManifest(manifest uploadManifest) error {
	tempFile, tempName, err := s.createTempFile(manifest.ID, "manifest-")
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = s.root.Remove(tempName)
		}
	}()

	encoder := json.NewEncoder(tempFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := s.root.Rename(tempName, s.manifestPath(manifest.ID)); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (s *Service) sessionDir(sessionID string) string {
	return path.Join(s.tempRoot, sessionID)
}

func (s *Service) manifestPath(sessionID string) string {
	return path.Join(s.sessionDir(sessionID), manifestName)
}

func (s *Service) partPath(sessionID string, number int) string {
	return path.Join(s.sessionDir(sessionID), fmt.Sprintf("part-%08d", number))
}

func (s *Service) createTempFile(sessionID, prefix string) (*os.File, string, error) {
	for range 100 {
		suffix, err := newSessionID()
		if err != nil {
			return nil, "", err
		}
		name := path.Join(s.sessionDir(sessionID), prefix+suffix)
		file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return nil, "", err
	}
	return nil, "", errors.New("could not create unique temporary upload file")
}

func (s *Service) validateTarget(targetPath string, overwrite bool) error {
	parent := path.Dir(targetPath)
	if err := rejectRootSymlinks(s.root, parent); err != nil {
		return err
	}
	info, err := s.root.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrInvalidTargetPath
	}
	info, err = s.root.Lstat(targetPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidTargetPath
		}
		if !overwrite {
			return ErrTargetExists
		}
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Service) publish(source, target string, overwrite bool) error {
	if overwrite {
		return s.root.Rename(source, target)
	}
	if err := s.root.Link(source, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrTargetExists
		}
		return err
	}
	return s.root.Remove(source)
}

func ensureRootDirectory(root *os.Root, relativePath string) error {
	current := "."
	for _, component := range strings.Split(relativePath, "/") {
		current = path.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidTempRoot
		}
	}
	return nil
}

func rejectRootSymlinks(root *os.Root, relativePath string) error {
	if relativePath == "." {
		return nil
	}
	current := "."
	for _, component := range strings.Split(relativePath, "/") {
		current = path.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidTargetPath
		}
	}
	return nil
}

func (m uploadManifest) session() UploadSession {
	parts := make([]UploadPart, 0, len(m.Parts))
	for partNumber, size := range m.Parts {
		number, err := strconv.Atoi(partNumber)
		if err != nil {
			continue
		}
		parts = append(parts, UploadPart{Number: number, Size: size})
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].Number < parts[j].Number
	})
	return UploadSession{
		ID:           m.ID,
		TargetPath:   m.TargetPath,
		ExpectedSize: m.ExpectedSize,
		ReceivedSize: m.receivedSize(),
		Parts:        parts,
		CreatedAt:    m.CreatedAt.UTC(),
		Checksum:     m.Checksum,
	}
}

func (m uploadManifest) receivedSize() int64 {
	var total int64
	for _, size := range m.Parts {
		total += size
	}
	return total
}

func cleanTargetPath(value string) (string, error) {
	cleaned, err := storage.CleanRelativePath(value)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return "", ErrInvalidTargetPath
	}
	for _, r := range cleaned {
		if r == 0 || r < 0x20 || r == 0x7f {
			return "", ErrInvalidTargetPath
		}
	}
	return cleaned, nil
}

func ensureTargetParent(targetFile string) error {
	parent := filepath.Dir(targetFile)
	return ensureDirectory(parent)
}

func ensureDirectory(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrInvalidTargetPath
	}
	return nil
}

func copyWithLimit(writer io.Writer, reader io.Reader, maxBytes int64, bufferSize int) (int64, error) {
	if maxBytes < 0 {
		return 0, ErrUploadTooLarge
	}
	limit := maxBytes + 1
	if maxBytes == math.MaxInt64 {
		limit = maxBytes
	}
	limited := &io.LimitedReader{R: reader, N: limit}
	written, err := io.CopyBuffer(writer, limited, make([]byte, bufferSize))
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, ErrUploadTooLarge
	}
	return written, nil
}

func isPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isSafeInternalRelativePath(value string) bool {
	if value == "" || value == "." || strings.HasPrefix(value, "/") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
		for _, r := range component {
			if r == 0 || r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

func newSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validateSessionID(sessionID string) error {
	if len(sessionID) != 32 {
		return ErrInvalidSessionID
	}
	for _, r := range sessionID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ErrInvalidSessionID
		}
	}
	return nil
}
