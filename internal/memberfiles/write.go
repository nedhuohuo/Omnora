package memberfiles

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/fileops"
	"omnora/internal/files"
	"omnora/internal/storage"
	"omnora/internal/transfer"
)

var (
	ErrUploadNotFound      = errors.New("member files: upload session not found")
	ErrUploadExpired       = errors.New("member files: upload session expired")
	ErrUploadConflict      = errors.New("member files: upload conflict")
	ErrMutationConflict    = errors.New("member files: mutation conflict")
	ErrMutationInvalidPath = errors.New("member files: mutation path is invalid")
	ErrCrossMountSameMount = errors.New("member files: source and destination mounts must differ")
	ErrChecksumMismatch    = errors.New("member files: upload checksum mismatch")
	ErrShareInvalidation   = errors.New("member files: share invalidation failed")
)

// ShareInvalidator is deliberately narrow. Member file mutations invalidate
// old-path shares only after the filesystem mutation has succeeded.
type ShareInvalidator interface {
	RevokePath(ctx context.Context, mountID, storageRelativePath string) error
}

// WithShareInvalidator connects the member-file service to authenticated share
// management without making the file package depend on the share package.
func WithShareInvalidator(invalidator ShareInvalidator) Option {
	return func(s *Service) { s.shares = invalidator }
}

type MutationResult struct {
	RelativePath      string `json:"relativePath"`
	ObjectFingerprint string `json:"objectFingerprint,omitempty"`
	AffectedCount     int    `json:"affectedCount,omitempty"`
	TotalBytes        int64  `json:"totalBytes,omitempty"`
}

type UploadRequest struct {
	Locator                   access.Locator `json:"locator"`
	ExpectedSize              int64          `json:"expectedSize"`
	Checksum                  string         `json:"checksum,omitempty"`
	Overwrite                 bool           `json:"overwrite,omitempty"`
	ExpectedObjectFingerprint string         `json:"expectedObjectFingerprint,omitempty"`
}

type UploadResult struct {
	ID           string    `json:"id"`
	TargetPath   string    `json:"targetPath"`
	ExpectedSize int64     `json:"expectedSize"`
	Checksum     string    `json:"checksum,omitempty"`
	PartSize     int       `json:"partSize"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type UploadStatusResult struct {
	UploadResult
	ReceivedSize int64                 `json:"receivedSize"`
	Parts        []transfer.UploadPart `json:"parts"`
}

type TrashResult struct {
	TrashID           string          `json:"trashId"`
	OriginalPath      string          `json:"originalPath"`
	Name              string          `json:"name,omitempty"`
	Kind              files.EntryKind `json:"kind,omitempty"`
	DeletedAt         time.Time       `json:"deletedAt,omitempty"`
	TrashRelativePath string          `json:"trashRelativePath,omitempty"`
	ObjectFingerprint string          `json:"objectFingerprint,omitempty"`
	Size              int64           `json:"size"`
}

type TrashListResult struct {
	Items      []files.TrashItem `json:"items"`
	TotalCount int               `json:"totalCount"`
	TotalBytes int64             `json:"totalBytes"`
}

type uploadRecord struct {
	ID                        string
	AccountID                 string
	Source                    contentref.Source
	MountID                   string
	TargetPath                string
	StorageTargetPath         string
	ExpectedSize              int64
	PartSize                  int
	Checksum                  string
	TempDir                   string
	ExpiresAt                 time.Time
	ExpectedTargetFingerprint string
	Mount                     access.AuthorizedMount
}

func (s *Service) CreateDirectory(ctx context.Context, subject access.Subject, locator access.Locator, name string) (MutationResult, error) {
	mount, err := s.authorizeWriteParent(ctx, subject, locator, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	created, err := s.files.CreateDirectory(toFilesMount(mount), mount.RelativePath, name)
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{RelativePath: created}, nil
}

func (s *Service) PrepareUpload(ctx context.Context, subject access.Subject, req UploadRequest) (UploadResult, error) {
	if err := s.validate(subject); err != nil {
		return UploadResult{}, err
	}
	if req.ExpectedSize < 0 {
		return UploadResult{}, ErrInvalidInput
	}
	target, err := cleanMutationPath(req.Locator.Path)
	if err != nil || target == "." {
		return UploadResult{}, ErrInvalidInput
	}
	parent := path.Dir(target)
	if parent == "." {
		parent = "."
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject: subject, Scope: aitoken.ScopeUploadsCreate,
		Locator:            access.Locator{Source: req.Locator.Source, MountID: req.Locator.MountID, Path: parent},
		RequiredPermission: domain.ContentPermissionEditor, Write: true,
	})
	if err != nil {
		return UploadResult{}, err
	}
	if req.Overwrite {
		if strings.TrimSpace(req.ExpectedObjectFingerprint) == "" {
			return UploadResult{}, ErrInvalidInput
		}
		entry, statErr := s.files.Stat(toFilesMount(mount), target)
		if statErr != nil {
			return UploadResult{}, statErr
		}
		if entry.Kind != files.EntryKindFile {
			return UploadResult{}, files.ErrNotFile
		}
		if entry.ObjectFingerprint != req.ExpectedObjectFingerprint {
			return UploadResult{}, ErrMutationConflict
		}
	} else if err := s.files.ValidateWritableTarget(toFilesMount(mount), target); err != nil {
		return UploadResult{}, err
	}
	checksum, err := normalizeChecksum(req.Checksum)
	if err != nil {
		return UploadResult{}, err
	}
	if req.Overwrite {
		// Replacement invalidates shares before any upload bytes are written;
		// a stale share must never survive a confirmed content update or a
		// later filesystem failure that leaves the result ambiguous.
		if err := s.invalidatePath(ctx, mount.ID, storagePathFor(mount, subject.AccountID, target)); err != nil {
			return UploadResult{}, err
		}
	}
	transferService, err := transfer.NewService(transfer.Options{
		MountRoot: mount.Root,
		TempRoot:  filepathJoin(mount.Root, storage.ReservedNamespace, "tmp", "uploads"),
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	defer transferService.Close()
	upload, err := transferService.CreateUploadSession(transfer.CreateUploadSessionRequest{
		TargetPath: target, ExpectedSize: req.ExpectedSize, Checksum: checksum,
		Overwrite: req.Overwrite, ExpectedTargetFingerprint: req.ExpectedObjectFingerprint,
	})
	if err != nil {
		return UploadResult{}, mapTransferError(err)
	}
	// Upload sessions retain the existing REST contract (24 hours). MCP's
	// short-lived 30-minute transfer ticket is a separate credential and does
	// not shorten the resumable session itself.
	expires := s.now().UTC().Add(24 * time.Hour)
	_, err = s.db.ExecContext(ctx, `
	INSERT INTO upload_sessions(id, account_id, mount_id, target_relative_path,
	    declared_size, part_size, temp_dir, expires_at, expected_target_identity, credential_generation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
	    CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER))
	`, upload.ID, subject.AccountID, mount.ID, storagePathFor(mount, subject.AccountID, upload.TargetPath),
		upload.ExpectedSize, 32*1024,
		filepathJoin(mount.Root, storage.ReservedNamespace, "tmp", "uploads"), formatMemberTime(expires), req.ExpectedObjectFingerprint)
	if err != nil {
		_ = transferService.CancelUpload(upload.ID)
		return UploadResult{}, fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	return UploadResult{ID: upload.ID, TargetPath: upload.TargetPath, ExpectedSize: upload.ExpectedSize,
		Checksum: checksum, PartSize: 32 * 1024, ExpiresAt: expires}, nil
}

func (s *Service) UploadStatus(ctx context.Context, subject access.Subject, uploadID string) (UploadStatusResult, error) {
	record, err := s.loadUpload(ctx, subject, uploadID)
	if err != nil {
		return UploadStatusResult{}, err
	}
	mount, err := s.authorizeUploadMount(ctx, subject, record)
	if err != nil {
		return UploadStatusResult{}, err
	}
	record.Mount = mount
	transferService, err := s.newTransferService(record.Mount)
	if err != nil {
		return UploadStatusResult{}, fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	defer transferService.Close()
	status, err := transferService.ResumeUploadSession(record.ID)
	if err != nil {
		return UploadStatusResult{}, mapTransferError(err)
	}
	return UploadStatusResult{UploadResult: UploadResult{ID: record.ID, TargetPath: record.TargetPath,
		ExpectedSize: record.ExpectedSize, Checksum: status.Checksum, PartSize: record.PartSize,
		ExpiresAt: record.ExpiresAt}, ReceivedSize: status.ReceivedSize, Parts: status.Parts}, nil
}

// WriteUploadPart is used by the transfer adapter; the MCP control-plane
// tools never accept file bytes directly. It remains guarded by the same
// current editor, token, boundary, and mount-identity checks as status/complete.
func (s *Service) WriteUploadPart(ctx context.Context, subject access.Subject, uploadID string, number int, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := s.loadUpload(ctx, subject, uploadID)
	if err != nil {
		return err
	}
	mount, err := s.authorizeUploadMount(ctx, subject, record)
	if err != nil {
		return err
	}
	record.Mount = mount
	transferService, err := s.newTransferService(record.Mount)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	defer transferService.Close()
	part, err := transferService.WritePart(record.ID, number, body)
	if err != nil {
		return mapTransferError(err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO upload_parts(upload_id, part_number, size_bytes)
VALUES (?, ?, ?)
ON CONFLICT(upload_id, part_number) DO UPDATE SET size_bytes = excluded.size_bytes, created_at = CURRENT_TIMESTAMP
`, record.ID, part.Number, part.Size)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	return nil
}

func (s *Service) CompleteUpload(ctx context.Context, subject access.Subject, uploadID string) (MutationResult, error) {
	record, err := s.loadUpload(ctx, subject, uploadID)
	if err != nil {
		return MutationResult{}, err
	}
	mount, err := s.authorizeUploadMount(ctx, subject, record)
	if err != nil {
		return MutationResult{}, err
	}
	record.Mount = mount
	if record.ExpectedTargetFingerprint != "" {
		entry, statErr := s.files.Stat(toFilesMount(record.Mount), record.TargetPath)
		if statErr != nil {
			return MutationResult{}, statErr
		}
		if entry.ObjectFingerprint != record.ExpectedTargetFingerprint {
			return MutationResult{}, ErrMutationConflict
		}
	}
	if err := s.claimUpload(ctx, record.ID); err != nil {
		return MutationResult{}, err
	}
	transferService, err := s.newTransferService(record.Mount)
	if err != nil {
		_ = s.restoreUploadClaim(ctx, record.ID)
		return MutationResult{}, fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	defer transferService.Close()
	completed, err := transferService.CompleteUpload(record.ID)
	if err != nil {
		if restoreErr := s.restoreUploadClaim(ctx, record.ID); restoreErr != nil {
			return MutationResult{}, errors.Join(mapTransferError(err), restoreErr)
		}
		return MutationResult{}, mapTransferError(err)
	}
	result := MutationResult{RelativePath: completed.TargetPath, TotalBytes: completed.Size}
	if entry, statErr := s.files.Stat(toFilesMount(record.Mount), completed.TargetPath); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	resultErr := error(nil)
	update, updateErr := s.db.ExecContext(ctx, `UPDATE upload_sessions SET status = 'completed', completed_at = ? WHERE id = ? AND status = 'failed'`, formatMemberTime(s.now().UTC()), record.ID)
	if updateErr != nil {
		resultErr = fmt.Errorf("%w: %v", ErrUploadConflict, updateErr)
	} else if affected, affectedErr := update.RowsAffected(); affectedErr != nil || affected != 1 {
		resultErr = fmt.Errorf("%w: upload state changed", ErrUploadConflict)
	}
	if resultErr != nil {
		return result, resultErr
	}
	return result, nil
}

func (s *Service) CancelUpload(ctx context.Context, subject access.Subject, uploadID string) error {
	record, err := s.loadUpload(ctx, subject, uploadID)
	if err != nil {
		return err
	}
	mount, err := s.authorizeUploadMount(ctx, subject, record)
	if err != nil {
		return err
	}
	record.Mount = mount
	if err := s.claimUpload(ctx, record.ID); err != nil {
		return err
	}
	transferService, err := s.newTransferService(record.Mount)
	if err != nil {
		_ = s.restoreUploadClaim(ctx, record.ID)
		return fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	defer transferService.Close()
	if err := transferService.CancelUpload(record.ID); err != nil && !errors.Is(err, transfer.ErrSessionNotFound) {
		if restoreErr := s.restoreUploadClaim(ctx, record.ID); restoreErr != nil {
			return errors.Join(fmt.Errorf("%w: %v", ErrUploadConflict, err), restoreErr)
		}
		return fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	update, updateErr := s.db.ExecContext(ctx, `UPDATE upload_sessions SET status = 'canceled', canceled_at = ? WHERE id = ? AND status = 'failed'`, formatMemberTime(s.now().UTC()), record.ID)
	if updateErr != nil {
		return fmt.Errorf("%w: %v", ErrUploadConflict, updateErr)
	}
	if affected, affectedErr := update.RowsAffected(); affectedErr != nil || affected != 1 {
		return fmt.Errorf("%w: upload state changed", ErrUploadConflict)
	}
	return nil
}

// claimUpload closes the active window before any filesystem completion or
// cancellation work begins. The legacy status enum has no "completing"
// value, so failed is used as an internal in-flight claim; callers may only
// restore it when no filesystem publication occurred.
func (s *Service) claimUpload(ctx context.Context, uploadID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE upload_sessions SET status = 'failed' WHERE id = ? AND status = 'active'`, uploadID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUploadConflict, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("%w: upload is already claimed", ErrUploadConflict)
	}
	return nil
}

func (s *Service) restoreUploadClaim(ctx context.Context, uploadID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE upload_sessions SET status = 'active' WHERE id = ? AND status = 'failed'`, uploadID)
	if err != nil {
		return fmt.Errorf("%w: claim restore failed", ErrUploadConflict)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("%w: claim restore failed", ErrUploadConflict)
	}
	return nil
}

func (s *Service) Rename(ctx context.Context, subject access.Subject, source access.Locator, destination string) (MutationResult, error) {
	src, cleaned, err := s.prepareRename(ctx, subject, source, destination)
	if err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{RelativePath: cleaned}
	if _, err := s.files.Rename(toFilesMount(src), src.RelativePath, cleaned); err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	if entry, statErr := s.files.Stat(toFilesMount(src), cleaned); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, src.ID, src.StorageRelativePath); err != nil {
		return result, err
	}
	return result, nil
}

// RenameSecure behaves like Rename but, when a fileops.Coordinator is
// configured, durably journals the rename, invalidates affected shares, and
// records auditWriter's event all in one transaction before any filesystem
// I/O begins. A filesystem failure afterward leaves a recovery_required
// operation row instead of an audit trail that no longer matches disk state.
func (s *Service) RenameSecure(ctx context.Context, subject access.Subject, source access.Locator, destination string, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.Rename(ctx, subject, source, destination)
	}
	src, cleaned, err := s.prepareRename(ctx, subject, source, destination)
	if err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{RelativePath: cleaned}
	if _, err := s.fileOps.Rename(ctx, toStorageFilesMount(src), src.ID, src.StorageRelativePath, storagePathFor(src, subject.AccountID, cleaned), auditWriter); err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	if entry, statErr := s.files.Stat(toFilesMount(src), cleaned); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

func (s *Service) prepareRename(ctx context.Context, subject access.Subject, source access.Locator, destination string) (access.AuthorizedMount, string, error) {
	src, err := s.authorizeMutationSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return access.AuthorizedMount{}, "", err
	}
	dest, err := s.authorizeDestinationParent(ctx, subject, source, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return access.AuthorizedMount{}, "", err
	}
	if src.ID != dest.ID {
		return access.AuthorizedMount{}, "", ErrInvalidInput
	}
	cleaned, err := cleanMutationPath(destination)
	if err != nil || cleaned == "." {
		return access.AuthorizedMount{}, "", ErrInvalidInput
	}
	return src, cleaned, nil
}

func (s *Service) Copy(ctx context.Context, subject access.Subject, source, destination access.Locator) (MutationResult, error) {
	src, err := s.authorizeReadSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	dest, err := s.authorizeDestinationDir(ctx, subject, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID == dest.ID && (dest.RelativePath == src.RelativePath || strings.HasPrefix(dest.RelativePath, src.RelativePath+"/")) {
		return MutationResult{}, ErrMutationInvalidPath
	}
	created, err := s.files.CopyAcrossMounts(toFilesMount(src), toFilesMount(dest), src.RelativePath, dest.RelativePath)
	if err != nil {
		return MutationResult{RelativePath: created}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

// CrossMountCopy preserves the REST cross-mount contract while keeping both
// authorization checks and the same-mount rejection inside the application
// service. Callers cannot accidentally perform a pre-check with weaker
// semantics and then bypass the service's live Guard checks.
func (s *Service) CrossMountCopy(ctx context.Context, subject access.Subject, source, destination access.Locator) (MutationResult, error) {
	src, err := s.authorizeReadSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	dest, err := s.authorizeDestinationDir(ctx, subject, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID == dest.ID {
		return MutationResult{}, ErrCrossMountSameMount
	}
	created, err := s.files.CopyAcrossMounts(toFilesMount(src), toFilesMount(dest), src.RelativePath, dest.RelativePath)
	if err != nil {
		return MutationResult{RelativePath: created}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

func (s *Service) Move(ctx context.Context, subject access.Subject, source, destination access.Locator) (MutationResult, error) {
	src, dest, err := s.prepareMove(ctx, subject, source, destination)
	if err != nil {
		return MutationResult{}, err
	}
	var created string
	if src.ID == dest.ID {
		created, err = s.files.Move(toFilesMount(src), src.RelativePath, dest.RelativePath)
	} else {
		created, err = s.files.MoveAcrossMounts(toFilesMount(src), toFilesMount(dest), src.RelativePath, dest.RelativePath)
	}
	if err != nil {
		return MutationResult{RelativePath: created}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, src.ID, src.StorageRelativePath); err != nil {
		return result, err
	}
	return result, nil
}

// MoveSecure behaves like Move but durably journals both same-mount and
// cross-mount moves when a Coordinator is configured.
func (s *Service) MoveSecure(ctx context.Context, subject access.Subject, source, destination access.Locator, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.Move(ctx, subject, source, destination)
	}
	src, dest, err := s.prepareMove(ctx, subject, source, destination)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID != dest.ID {
		createdStorage, moveErr := s.fileOps.MoveAcrossMounts(ctx, toStorageFilesMount(src), toStorageFilesMount(dest),
			src.ID, dest.ID, src.StorageRelativePath, dest.StorageRelativePath, auditWriter)
		created, pathErr := clientPathFor(dest, subject.AccountID, createdStorage)
		if pathErr != nil && moveErr == nil {
			moveErr = pathErr
		}
		result := MutationResult{RelativePath: created}
		if moveErr != nil {
			return result, mapMutationError(moveErr)
		}
		if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
			result.ObjectFingerprint = entry.ObjectFingerprint
		}
		return result, nil
	}
	createdStorage, err := s.fileOps.Move(ctx, toStorageFilesMount(src), src.ID, src.StorageRelativePath, dest.StorageRelativePath, auditWriter)
	created, pathErr := clientPathFor(dest, subject.AccountID, createdStorage)
	if pathErr != nil && err == nil {
		err = pathErr
	}
	if err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

func (s *Service) prepareMove(ctx context.Context, subject access.Subject, source, destination access.Locator) (access.AuthorizedMount, access.AuthorizedMount, error) {
	src, err := s.authorizeMutationSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return access.AuthorizedMount{}, access.AuthorizedMount{}, err
	}
	dest, err := s.authorizeDestinationDir(ctx, subject, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return access.AuthorizedMount{}, access.AuthorizedMount{}, err
	}
	if src.ID == dest.ID && (dest.RelativePath == src.RelativePath || strings.HasPrefix(dest.RelativePath, src.RelativePath+"/")) {
		return access.AuthorizedMount{}, access.AuthorizedMount{}, ErrMutationInvalidPath
	}
	return src, dest, nil
}

// CrossMountMove is the REST-compatible move variant. It requires editor
// permission on both sides and invalidates shares at the source path only
// after the filesystem move has completed.
func (s *Service) CrossMountMove(ctx context.Context, subject access.Subject, source, destination access.Locator) (MutationResult, error) {
	src, err := s.authorizeMutationSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	dest, err := s.authorizeDestinationDir(ctx, subject, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID == dest.ID {
		return MutationResult{}, ErrCrossMountSameMount
	}
	created, err := s.files.MoveAcrossMounts(toFilesMount(src), toFilesMount(dest), src.RelativePath, dest.RelativePath)
	if err != nil {
		return MutationResult{RelativePath: created}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, src.ID, src.StorageRelativePath); err != nil {
		return result, err
	}
	return result, nil
}

// CrossMountMoveSecure is the REST-compatible cross-mount entry point backed
// by the durable file-operation coordinator when configured.
func (s *Service) CrossMountMoveSecure(ctx context.Context, subject access.Subject, source, destination access.Locator, auditWriter fileops.AuditWriter) (MutationResult, error) {
	if s.fileOps == nil {
		return s.CrossMountMove(ctx, subject, source, destination)
	}
	src, dest, err := s.prepareMove(ctx, subject, source, destination)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID == dest.ID {
		return MutationResult{}, ErrCrossMountSameMount
	}
	createdStorage, err := s.fileOps.MoveAcrossMounts(ctx, toStorageFilesMount(src), toStorageFilesMount(dest),
		src.ID, dest.ID, src.StorageRelativePath, dest.StorageRelativePath, auditWriter)
	created, pathErr := clientPathFor(dest, subject.AccountID, createdStorage)
	if pathErr != nil && err == nil {
		err = pathErr
	}
	result := MutationResult{RelativePath: created}
	if err != nil {
		return result, mapMutationError(err)
	}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

func (s *Service) authorizeMutationSource(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	if err := s.validate(subject); err != nil {
		return access.AuthorizedMount{}, err
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.ContentPermissionEditor, Write: true})
}

func (s *Service) authorizeReadSource(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	if err := s.validate(subject); err != nil {
		return access.AuthorizedMount{}, err
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.ContentPermissionViewer})
}

func (s *Service) authorizeWriteParent(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	parent, err := cleanMutationPath(locator.Path)
	if err != nil {
		return access.AuthorizedMount{}, ErrInvalidInput
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope,
		Locator:            access.Locator{Source: locator.Source, MountID: locator.MountID, Path: parent},
		RequiredPermission: domain.ContentPermissionEditor, Write: true})
}

func (s *Service) authorizeDestinationParent(ctx context.Context, subject access.Subject, source access.Locator, destination string, scope aitoken.Scope) (access.AuthorizedMount, error) {
	cleaned, err := cleanMutationPath(destination)
	if err != nil || cleaned == "." {
		return access.AuthorizedMount{}, ErrInvalidInput
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope,
		Locator:            access.Locator{Source: source.Source, MountID: source.MountID, Path: path.Dir(cleaned)},
		RequiredPermission: domain.ContentPermissionEditor, Write: true})
}

func (s *Service) authorizeDestinationDir(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.ContentPermissionEditor, Write: true})
}

func (s *Service) loadUpload(ctx context.Context, subject access.Subject, uploadID string) (uploadRecord, error) {
	if err := s.validate(subject); err != nil {
		return uploadRecord{}, err
	}
	uploadID = strings.TrimSpace(uploadID)
	if uploadID == "" {
		return uploadRecord{}, ErrUploadNotFound
	}
	var record uploadRecord
	var expires string
	var purpose domain.MountPurpose
	// The credential_generation check fails an in-flight upload closed the
	// same way an expired lease does: if the account's credential epoch was
	// bumped since the upload started (password reset, security event,
	// ...), the session simply stops resolving instead of letting a stale
	// upload continue writing into the mount.
	err := s.db.QueryRowContext(ctx, `
	SELECT u.id, u.account_id, u.mount_id, u.target_relative_path,
	       u.declared_size, u.part_size, u.temp_dir, u.expires_at, COALESCE(u.expected_target_identity, ''),
       m.purpose
FROM upload_sessions u
JOIN mounts m ON m.id = u.mount_id
WHERE u.id = ? AND u.account_id = ? AND u.status = 'active'
  AND u.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, uploadID, subject.AccountID).Scan(&record.ID, &record.AccountID, &record.MountID,
		&record.StorageTargetPath, &record.ExpectedSize, &record.PartSize, &record.TempDir, &expires, &record.ExpectedTargetFingerprint, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return uploadRecord{}, ErrUploadNotFound
	}
	if err != nil {
		return uploadRecord{}, err
	}
	switch purpose {
	case domain.MountPurposePersonalDefault:
		record.Source = contentref.SourcePersonal
		prefix := subject.AccountID + "/"
		if !strings.HasPrefix(record.StorageTargetPath, prefix) {
			return uploadRecord{}, ErrUploadNotFound
		}
		record.TargetPath = strings.TrimPrefix(record.StorageTargetPath, prefix)
	case domain.MountPurposeCommon:
		record.Source = contentref.SourceCommonMount
		record.TargetPath = record.StorageTargetPath
	default:
		return uploadRecord{}, ErrUploadNotFound
	}
	record.ExpiresAt, err = parseMemberTime(expires)
	if err != nil {
		return uploadRecord{}, ErrUploadConflict
	}
	if !s.now().UTC().Before(record.ExpiresAt) {
		_, _ = s.db.ExecContext(ctx, `UPDATE upload_sessions SET status = 'expired' WHERE id = ? AND status = 'active'`, record.ID)
		return uploadRecord{}, ErrUploadExpired
	}
	return record, nil
}

func (s *Service) authorizeUploadMount(ctx context.Context, subject access.Subject, record uploadRecord) (access.AuthorizedMount, error) {
	parent := path.Dir(record.TargetPath)
	if parent == "." {
		parent = "."
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeUploadsCreate,
		Locator:            access.Locator{Source: record.Source, MountID: commonMountID(record.Source, record.MountID), Path: parent},
		RequiredPermission: domain.ContentPermissionEditor, Write: true})
}

func (s *Service) newTransferService(mount access.AuthorizedMount) (*transfer.Service, error) {
	return transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: filepathJoin(mount.Root, storage.ReservedNamespace, "tmp", "uploads")})
}

func (s *Service) invalidatePath(ctx context.Context, mountID, storageRelativePath string) error {
	if s.shares == nil {
		return nil
	}
	if err := s.shares.RevokePath(ctx, mountID, storageRelativePath); err != nil {
		return fmt.Errorf("%w: %v", ErrShareInvalidation, err)
	}
	return nil
}

func storagePathFor(mount access.AuthorizedMount, accountID, clientPath string) string {
	if mount.Source == contentref.SourcePersonal {
		return path.Join(accountID, clientPath)
	}
	return clientPath
}

func commonMountID(source contentref.Source, mountID string) string {
	if source == contentref.SourceCommonMount {
		return mountID
	}
	return ""
}

func clientPathFor(mount access.AuthorizedMount, accountID, storagePath string) (string, error) {
	if mount.Source != contentref.SourcePersonal {
		return storagePath, nil
	}
	prefix := accountID + "/"
	if storagePath == accountID {
		return ".", nil
	}
	if !strings.HasPrefix(storagePath, prefix) {
		return "", ErrMutationConflict
	}
	return strings.TrimPrefix(storagePath, prefix), nil
}

func cleanMutationPath(value string) (string, error) {
	if strings.Contains(value, `\`) {
		return "", ErrInvalidInput
	}
	cleaned, err := storage.CleanRelativePath(value)
	if err != nil {
		return "", ErrInvalidInput
	}
	return cleaned, nil
}

func normalizeChecksum(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "sha256:")
	if value == "" {
		return "", nil
	}
	if len(value) != 64 {
		return "", ErrInvalidInput
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", ErrInvalidInput
		}
	}
	return value, nil
}

func mapTransferError(err error) error {
	switch {
	case errors.Is(err, transfer.ErrTargetExists):
		return errors.Join(ErrUploadConflict, err)
	case errors.Is(err, transfer.ErrChecksumMismatch):
		return ErrChecksumMismatch
	case errors.Is(err, transfer.ErrTargetChanged):
		return ErrMutationConflict
	default:
		return err
	}
}

func mapMutationError(err error) error {
	if errors.Is(err, files.ErrInvalidMount) {
		return errors.Join(ErrMutationConflict, err)
	}
	return err
}

func filepathJoin(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, part := range parts[1:] {
		result = path.Join(result, part)
	}
	return result
}

func formatMemberTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseMemberTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	return parsed.UTC(), err
}
