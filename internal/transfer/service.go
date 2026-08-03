package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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
)

type Service struct {
	mountRoot  string
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
}

type UploadSession struct {
	ID           string
	TargetPath   string
	ExpectedSize int64
	ReceivedSize int64
	Parts        []UploadPart
	CreatedAt    time.Time
}

type UploadPart struct {
	Number int
	Size   int64
}

type CompletedUpload struct {
	TargetPath string
	Size       int64
}

type uploadManifest struct {
	ID           string           `json:"id"`
	TargetPath   string           `json:"targetPath"`
	ExpectedSize int64            `json:"expectedSize"`
	Overwrite    bool             `json:"overwrite"`
	CreatedAt    time.Time        `json:"createdAt"`
	Parts        map[string]int64 `json:"parts"`
}

func NewService(options Options) (*Service, error) {
	mountRoot := filepath.Clean(options.MountRoot)
	if options.MountRoot == "" || mountRoot == "." {
		return nil, ErrInvalidMountRoot
	}
	if err := ensureDirectory(mountRoot); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMountRoot, err)
	}
	tempRoot := filepath.Clean(options.TempRoot)
	if options.TempRoot == "" || tempRoot == "." {
		return nil, ErrInvalidTempRoot
	}
	if !isPathWithin(mountRoot, tempRoot) {
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

	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTempRoot, err)
	}

	return &Service{
		mountRoot:  mountRoot,
		tempRoot:   tempRoot,
		bufferSize: bufferSize,
		now:        clock,
	}, nil
}

func (s *Service) CreateUploadSession(req CreateUploadSessionRequest) (UploadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.ExpectedSize < 0 {
		return UploadSession{}, ErrInvalidTargetPath
	}
	targetPath, err := cleanTargetPath(req.TargetPath)
	if err != nil {
		return UploadSession{}, err
	}
	targetFile := s.targetFilePath(targetPath)
	if !req.Overwrite {
		if _, err := os.Lstat(targetFile); err == nil {
			return UploadSession{}, ErrTargetExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return UploadSession{}, err
		}
	}
	if err := ensureTargetParent(targetFile); err != nil {
		return UploadSession{}, err
	}

	id, err := newSessionID()
	if err != nil {
		return UploadSession{}, err
	}
	manifest := uploadManifest{
		ID:           id,
		TargetPath:   targetPath,
		ExpectedSize: req.ExpectedSize,
		Overwrite:    req.Overwrite,
		CreatedAt:    s.now().UTC(),
		Parts:        map[string]int64{},
	}

	if err := os.Mkdir(s.sessionDir(id), 0o700); err != nil {
		return UploadSession{}, err
	}
	if err := s.saveManifest(manifest); err != nil {
		_ = os.RemoveAll(s.sessionDir(id))
		return UploadSession{}, err
	}
	return manifest.session(), nil
}

func (s *Service) ResumeUploadSession(sessionID string) (UploadSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	if number <= 0 {
		return UploadPart{}, ErrInvalidPartNumber
	}
	manifest, err := s.loadManifest(sessionID)
	if err != nil {
		return UploadPart{}, err
	}
	oldPartSize := manifest.Parts[strconv.Itoa(number)]
	availableSize := manifest.ExpectedSize - manifest.receivedSize() + oldPartSize

	sessionDir := s.sessionDir(sessionID)
	tempFile, err := os.CreateTemp(sessionDir, fmt.Sprintf("part-%08d-", number))
	if err != nil {
		return UploadPart{}, err
	}
	tempName := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
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
	if err := os.Rename(tempName, partName); err != nil {
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

	manifest, err := s.loadManifest(sessionID)
	if err != nil {
		return CompletedUpload{}, err
	}
	if err := s.verifyComplete(manifest); err != nil {
		return CompletedUpload{}, err
	}

	targetFile := s.targetFilePath(manifest.TargetPath)
	if !manifest.Overwrite {
		if _, err := os.Lstat(targetFile); err == nil {
			return CompletedUpload{}, ErrTargetExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return CompletedUpload{}, err
		}
	}
	if err := ensureTargetParent(targetFile); err != nil {
		return CompletedUpload{}, err
	}

	sessionDir := s.sessionDir(sessionID)
	finalFile, err := os.CreateTemp(sessionDir, "final-*")
	if err != nil {
		return CompletedUpload{}, err
	}
	finalName := finalFile.Name()
	removeFinal := true
	defer func() {
		if removeFinal {
			_ = os.Remove(finalName)
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

	if err := os.Rename(finalName, targetFile); err != nil {
		return CompletedUpload{}, err
	}
	removeFinal = false
	_ = os.RemoveAll(sessionDir)

	return CompletedUpload{
		TargetPath: manifest.TargetPath,
		Size:       written,
	}, nil
}

func (s *Service) CancelUpload(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := os.RemoveAll(s.sessionDir(sessionID)); err != nil {
		return err
	}
	return nil
}

func (s *Service) assembleParts(writer io.Writer, manifest uploadManifest) (int64, error) {
	var written int64
	buffer := make([]byte, s.bufferSize)
	for number := 1; number <= len(manifest.Parts); number++ {
		partFile, err := os.Open(s.partPath(manifest.ID, number))
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
		info, err := os.Lstat(s.partPath(manifest.ID, number))
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
	file, err := os.Open(s.manifestPath(sessionID))
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
	if manifest.Parts == nil {
		manifest.Parts = map[string]int64{}
	}
	return manifest, nil
}

func (s *Service) saveManifest(manifest uploadManifest) error {
	tempFile, err := os.CreateTemp(s.sessionDir(manifest.ID), "manifest-*")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
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
	if err := os.Rename(tempName, s.manifestPath(manifest.ID)); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (s *Service) targetFilePath(targetPath string) string {
	return filepath.Join(s.mountRoot, filepath.FromSlash(targetPath))
}

func (s *Service) sessionDir(sessionID string) string {
	return filepath.Join(s.tempRoot, sessionID)
}

func (s *Service) manifestPath(sessionID string) string {
	return filepath.Join(s.sessionDir(sessionID), manifestName)
}

func (s *Service) partPath(sessionID string, number int) string {
	return filepath.Join(s.sessionDir(sessionID), fmt.Sprintf("part-%08d", number))
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
