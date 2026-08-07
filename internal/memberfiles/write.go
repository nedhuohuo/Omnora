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
	"omnora/internal/domain"
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
	RevokePath(ctx context.Context, spaceID, mountID, relativePath string) error
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
	Locator      access.Locator `json:"locator"`
	ExpectedSize int64          `json:"expectedSize"`
	Checksum     string         `json:"checksum,omitempty"`
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
	ID           string
	AccountID    string
	SpaceID      string
	MountID      string
	TargetPath   string
	ExpectedSize int64
	PartSize     int
	Checksum     string
	TempDir      string
	ExpiresAt    time.Time
	Mount        access.AuthorizedMount
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
		Locator:            access.Locator{SpaceID: req.Locator.SpaceID, MountID: req.Locator.MountID, Path: parent},
		RequiredPermission: domain.SpacePermissionEditor, Write: true,
	})
	if err != nil {
		return UploadResult{}, err
	}
	if err := s.files.ValidateWritableTarget(toFilesMount(mount), target); err != nil {
		return UploadResult{}, err
	}
	checksum, err := normalizeChecksum(req.Checksum)
	if err != nil {
		return UploadResult{}, err
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
	})
	if err != nil {
		return UploadResult{}, mapTransferError(err)
	}
	// Upload sessions retain the existing REST contract (24 hours). MCP's
	// short-lived 30-minute transfer ticket is a separate credential and does
	// not shorten the resumable session itself.
	expires := s.now().UTC().Add(24 * time.Hour)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO upload_sessions(id, account_id, space_id, mount_id, target_relative_path,
    declared_size, part_size, temp_dir, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, upload.ID, subject.AccountID, req.Locator.SpaceID, req.Locator.MountID,
		upload.TargetPath, upload.ExpectedSize, 32*1024,
		filepathJoin(mount.Root, storage.ReservedNamespace, "tmp", "uploads"), formatMemberTime(expires))
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
	if err := s.authorizeUploadMount(ctx, subject, record); err != nil {
		return UploadStatusResult{}, err
	}
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
	if err := s.authorizeUploadMount(ctx, subject, record); err != nil {
		return err
	}
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
	if err := s.authorizeUploadMount(ctx, subject, record); err != nil {
		return MutationResult{}, err
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
	if err := s.authorizeUploadMount(ctx, subject, record); err != nil {
		return err
	}
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
	src, err := s.authorizeMutationSource(ctx, subject, source, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	dest, err := s.authorizeDestinationParent(ctx, subject, source, destination, aitoken.ScopeFilesWrite)
	if err != nil {
		return MutationResult{}, err
	}
	if src.ID != dest.ID {
		return MutationResult{}, ErrInvalidInput
	}
	cleaned, err := cleanMutationPath(destination)
	if err != nil || cleaned == "." {
		return MutationResult{}, ErrInvalidInput
	}
	result := MutationResult{RelativePath: cleaned}
	if _, err := s.files.Rename(toFilesMount(src), src.RelativePath, cleaned); err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	if entry, statErr := s.files.Stat(toFilesMount(src), cleaned); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, source, src.RelativePath); err != nil {
		return result, err
	}
	return result, nil
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
		return MutationResult{}, mapMutationError(err)
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
		return MutationResult{}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	return result, nil
}

func (s *Service) Move(ctx context.Context, subject access.Subject, source, destination access.Locator) (MutationResult, error) {
	src, err := s.authorizeMutationSource(ctx, subject, source, aitoken.ScopeFilesWrite)
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
	var created string
	if src.ID == dest.ID {
		created, err = s.files.Move(toFilesMount(src), src.RelativePath, dest.RelativePath)
	} else {
		created, err = s.files.MoveAcrossMounts(toFilesMount(src), toFilesMount(dest), src.RelativePath, dest.RelativePath)
	}
	if err != nil {
		return MutationResult{}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, source, src.RelativePath); err != nil {
		return result, err
	}
	return result, nil
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
		return MutationResult{}, mapMutationError(err)
	}
	result := MutationResult{RelativePath: created}
	if entry, statErr := s.files.Stat(toFilesMount(dest), created); statErr == nil {
		result.ObjectFingerprint = entry.ObjectFingerprint
	}
	if err := s.invalidatePath(ctx, source, src.RelativePath); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) authorizeMutationSource(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	if err := s.validate(subject); err != nil {
		return access.AuthorizedMount{}, err
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.SpacePermissionEditor, Write: true})
}

func (s *Service) authorizeReadSource(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	if err := s.validate(subject); err != nil {
		return access.AuthorizedMount{}, err
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.SpacePermissionViewer})
}

func (s *Service) authorizeWriteParent(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	parent, err := cleanMutationPath(locator.Path)
	if err != nil {
		return access.AuthorizedMount{}, ErrInvalidInput
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope,
		Locator:            access.Locator{SpaceID: locator.SpaceID, MountID: locator.MountID, Path: parent},
		RequiredPermission: domain.SpacePermissionEditor, Write: true})
}

func (s *Service) authorizeDestinationParent(ctx context.Context, subject access.Subject, source access.Locator, destination string, scope aitoken.Scope) (access.AuthorizedMount, error) {
	cleaned, err := cleanMutationPath(destination)
	if err != nil || cleaned == "." {
		return access.AuthorizedMount{}, ErrInvalidInput
	}
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope,
		Locator:            access.Locator{SpaceID: source.SpaceID, MountID: source.MountID, Path: path.Dir(cleaned)},
		RequiredPermission: domain.SpacePermissionEditor, Write: true})
}

func (s *Service) authorizeDestinationDir(ctx context.Context, subject access.Subject, locator access.Locator, scope aitoken.Scope) (access.AuthorizedMount, error) {
	return s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: scope, Locator: locator,
		RequiredPermission: domain.SpacePermissionEditor, Write: true})
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
	var identity, kind string
	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.account_id, u.space_id, u.mount_id, u.target_relative_path,
       u.declared_size, u.part_size, u.temp_dir, u.expires_at,
       m.root_path, m.kind, m.mode, COALESCE(m.mount_identity_json, '')
FROM upload_sessions u
JOIN mounts m ON m.id = u.mount_id AND m.space_id = u.space_id
WHERE u.id = ? AND u.account_id = ? AND u.status = 'active'
`, uploadID, subject.AccountID).Scan(&record.ID, &record.AccountID, &record.SpaceID, &record.MountID,
		&record.TargetPath, &record.ExpectedSize, &record.PartSize, &record.TempDir, &expires,
		&record.Mount.Root, &kind, &record.Mount.Mode, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return uploadRecord{}, ErrUploadNotFound
	}
	if err != nil {
		return uploadRecord{}, err
	}
	record.Mount.ID, record.Mount.SpaceID, record.Mount.Kind, record.Mount.IdentityJSON = record.MountID, record.SpaceID, kind, identity
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

func (s *Service) authorizeUploadMount(ctx context.Context, subject access.Subject, record uploadRecord) error {
	parent := path.Dir(record.TargetPath)
	if parent == "." {
		parent = "."
	}
	_, err := s.guard.Authorize(ctx, access.CheckRequest{Subject: subject, Scope: aitoken.ScopeUploadsCreate,
		Locator:            access.Locator{SpaceID: record.SpaceID, MountID: record.MountID, Path: parent},
		RequiredPermission: domain.SpacePermissionEditor, Write: true})
	return err
}

func (s *Service) newTransferService(mount access.AuthorizedMount) (*transfer.Service, error) {
	return transfer.NewService(transfer.Options{MountRoot: mount.Root, TempRoot: filepathJoin(mount.Root, storage.ReservedNamespace, "tmp", "uploads")})
}

func (s *Service) invalidatePath(ctx context.Context, locator access.Locator, relativePath string) error {
	if s.shares == nil {
		return nil
	}
	if err := s.shares.RevokePath(ctx, locator.SpaceID, locator.MountID, relativePath); err != nil {
		return fmt.Errorf("%w: %v", ErrShareInvalidation, err)
	}
	return nil
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
