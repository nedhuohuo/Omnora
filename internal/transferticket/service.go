// Package transferticket issues and revalidates short-lived credentials for
// the MCP download and upload byte-stream endpoints.
package transferticket

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"omnora/internal/access"
	"omnora/internal/aitoken"
	"omnora/internal/contentref"
	"omnora/internal/domain"
	"omnora/internal/storage"
)

const (
	defaultDownloadTTL = 10 * time.Minute
	defaultUploadTTL   = 30 * time.Minute
	timestampLayout    = time.RFC3339Nano
)

var (
	ErrInvalidInput       = errors.New("transfer ticket: invalid input")
	ErrUnauthorized       = errors.New("transfer ticket: unauthorized")
	ErrTicketNotFound     = errors.New("transfer ticket: not found")
	ErrTicketInvalid      = errors.New("transfer ticket: invalid")
	ErrWrongOperation     = errors.New("transfer ticket: wrong operation")
	ErrTicketExpired      = errors.New("transfer ticket: expired")
	ErrTicketClosed       = errors.New("transfer ticket: closed")
	ErrByteBudgetExceeded = errors.New("transfer ticket: byte budget exceeded")
	ErrObjectDrift        = errors.New("transfer ticket: object changed")
	ErrUploadInvalid      = errors.New("transfer ticket: upload session invalid")
	ErrInvalidStatus      = errors.New("transfer ticket: invalid close status")
	ErrNotRegularFile     = errors.New("transfer ticket: object is not a regular file")
)

// Service is intentionally independent from the HTTP server. A ticket may
// only be used after Verify performs all live token, ACL, boundary, mount and
// object checks.
type Service struct {
	db          *sql.DB
	tokens      *aitoken.Service
	guard       *access.Guard
	now         func() time.Time
	downloadTTL time.Duration
	uploadTTL   time.Duration
}

// Option configures ticket expiry and clock behavior.
type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

// WithTTL sets both operation lifetimes. It is useful in focused tests and
// for deployments that intentionally use a shorter bounded lifetime.
func WithTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.downloadTTL = ttl
			s.uploadTTL = ttl
		}
	}
}

func WithDownloadTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.downloadTTL = ttl
		}
	}
}

func WithUploadTTL(ttl time.Duration) Option {
	return func(s *Service) {
		if ttl > 0 {
			s.uploadTTL = ttl
		}
	}
}

func NewService(db *sql.DB, tokens *aitoken.Service, guard *access.Guard, opts ...Option) *Service {
	s := &Service{
		db:          db,
		tokens:      tokens,
		guard:       guard,
		now:         time.Now,
		downloadTTL: defaultDownloadTTL,
		uploadTTL:   defaultUploadTTL,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// IssueDownload issues a reusable ticket for one regular file. The supplied
// principal is refreshed before the ticket is persisted, so stale scopes or
// boundaries cannot be used to mint a ticket.
func (s *Service) IssueDownload(ctx context.Context, principal aitoken.Principal, locator access.Locator, maxBytes int64) (IssuedTicket, error) {
	if err := s.validate(maxBytes, principal, locator); err != nil {
		return IssuedTicket{}, err
	}
	fresh, err := s.tokens.RefreshPrincipal(ctx, principal.TokenID)
	if err != nil || fresh.AccountID != principal.AccountID {
		return IssuedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject:            access.Subject{AccountID: fresh.AccountID, Principal: &fresh},
		Scope:              aitoken.ScopeFilesDownloadTicket,
		Locator:            locator,
		RequiredPermission: domain.ContentPermissionViewer,
	})
	if err != nil {
		return IssuedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	objectFingerprint, err := fingerprintObject(mount)
	if err != nil {
		return IssuedTicket{}, err
	}
	if objectFingerprint == missingFingerprint(mount) {
		return IssuedTicket{}, ErrTicketNotFound
	}
	if err := requireRegularFile(mount); err != nil {
		return IssuedTicket{}, err
	}
	size, err := objectSize(mount)
	if err != nil {
		return IssuedTicket{}, err
	}
	// A zero maxBytes means the caller did not provide a narrower budget. Bound
	// the ticket to the current object size so a prepared download remains
	// usable while still preventing reads beyond the authorized object.
	if maxBytes == 0 {
		maxBytes = size
	}
	expiresAt, err := clampExpiry(s.now().UTC(), s.downloadTTL, fresh.ExpiresAt)
	if err != nil {
		return IssuedTicket{}, err
	}
	issued, err := s.insert(ctx, fresh, mount, OperationDownload, aitoken.ScopeFilesDownloadTicket, "", objectFingerprint, maxBytes, expiresAt)
	if err != nil {
		return IssuedTicket{}, err
	}
	issued.Size = size
	return issued, nil
}

// IssueUpload issues a ticket bound to an existing, owner-only active upload
// session and its target locator. The upload-session expiry is another hard
// upper bound on the ticket.
func (s *Service) IssueUpload(ctx context.Context, principal aitoken.Principal, uploadID string, locator access.Locator, maxBytes int64) (IssuedTicket, error) {
	if err := s.validate(maxBytes, principal, locator); err != nil {
		return IssuedTicket{}, err
	}
	if strings.TrimSpace(uploadID) == "" {
		return IssuedTicket{}, ErrInvalidInput
	}
	fresh, err := s.tokens.RefreshPrincipal(ctx, principal.TokenID)
	if err != nil || fresh.AccountID != principal.AccountID {
		return IssuedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	mount, err := s.guard.Authorize(ctx, access.CheckRequest{
		Subject:            access.Subject{AccountID: fresh.AccountID, Principal: &fresh},
		Scope:              aitoken.ScopeUploadsCreate,
		Locator:            locator,
		RequiredPermission: domain.ContentPermissionEditor,
		Write:              true,
	})
	if err != nil {
		return IssuedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	upload, err := s.loadUpload(ctx, uploadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedTicket{}, ErrUploadInvalid
		}
		return IssuedTicket{}, err
	}
	now := s.now().UTC()
	if upload.accountID != fresh.AccountID || upload.mountID != mount.ID || upload.targetPath != mount.StorageRelativePath {
		return IssuedTicket{}, ErrUploadInvalid
	}
	if upload.status != string(StatusActive) {
		return IssuedTicket{}, ErrUploadInvalid
	}
	if !now.Before(upload.expiresAt) {
		return IssuedTicket{}, ErrTicketExpired
	}
	objectFingerprint, err := fingerprintObject(mount)
	if err != nil {
		return IssuedTicket{}, err
	}
	expiresAt, err := clampExpiry(now, s.uploadTTL, fresh.ExpiresAt, upload.expiresAt)
	if err != nil {
		return IssuedTicket{}, err
	}
	return s.insert(ctx, fresh, mount, OperationUpload, aitoken.ScopeUploadsCreate, uploadID, objectFingerprint, maxBytes, expiresAt)
}

// Verify parses a ticket bearer and replays every live authorization check.
// A successful result is safe for a transfer handler to use until it changes
// the ticket byte budget or closes it.
func (s *Service) Verify(ctx context.Context, bearer string, expected Operation) (VerifiedTicket, error) {
	if s == nil || s.db == nil || s.tokens == nil || s.guard == nil {
		return VerifiedTicket{}, ErrInvalidInput
	}
	if expected != OperationDownload && expected != OperationUpload {
		return VerifiedTicket{}, ErrInvalidInput
	}
	publicID, secret, err := parseBearer(bearer)
	if err != nil {
		return VerifiedTicket{}, err
	}
	record, err := s.load(ctx, publicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return VerifiedTicket{}, ErrTicketNotFound
		}
		return VerifiedTicket{}, err
	}
	if record.operation != expected {
		return VerifiedTicket{}, ErrWrongOperation
	}
	if !secretMatches(secret, record.secretHash) {
		return VerifiedTicket{}, ErrTicketInvalid
	}
	if record.status != StatusActive {
		return VerifiedTicket{}, ErrTicketClosed
	}
	now := s.now().UTC()
	if !now.Before(record.expiresAt) {
		_, _ = s.db.ExecContext(ctx, `UPDATE mcp_transfer_tickets SET status = 'expired', closed_at = ? WHERE id = ? AND status = 'active'`, formatTime(now), record.id)
		return VerifiedTicket{}, ErrTicketExpired
	}
	record.locator, err = s.locatorForStoredTarget(ctx, record.accountID, record.locator.MountID, record.locator.Path)
	if err != nil {
		return VerifiedTicket{}, ErrTicketInvalid
	}

	principal, err := s.tokens.RefreshPrincipal(ctx, record.tokenID)
	if err != nil || principal.AccountID != record.accountID {
		return VerifiedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	check := access.CheckRequest{
		Subject:            access.Subject{AccountID: principal.AccountID, Principal: &principal},
		Scope:              record.requiredScope,
		Locator:            record.locator,
		RequiredPermission: domain.ContentPermissionViewer,
	}
	if record.operation == OperationUpload {
		check.RequiredPermission = domain.ContentPermissionEditor
		check.Write = true
	}
	mount, err := s.guard.Authorize(ctx, check)
	if err != nil {
		return VerifiedTicket{}, errors.Join(ErrUnauthorized, err)
	}
	if mount.RelativePath != record.locator.Path {
		return VerifiedTicket{}, ErrObjectDrift
	}
	currentFingerprint, err := fingerprintObject(mount)
	if err != nil {
		return VerifiedTicket{}, err
	}
	if currentFingerprint != record.objectFingerprint {
		return VerifiedTicket{}, ErrObjectDrift
	}
	if record.operation == OperationDownload {
		if err := requireRegularFile(mount); err != nil {
			return VerifiedTicket{}, errors.Join(ErrObjectDrift, err)
		}
	} else {
		upload, uploadErr := s.loadUpload(ctx, record.uploadID)
		if uploadErr != nil {
			if errors.Is(uploadErr, sql.ErrNoRows) {
				return VerifiedTicket{}, ErrUploadInvalid
			}
			return VerifiedTicket{}, uploadErr
		}
		if upload.accountID != record.accountID || upload.mountID != mount.ID || upload.targetPath != mount.StorageRelativePath {
			return VerifiedTicket{}, ErrUploadInvalid
		}
		if upload.status != string(StatusActive) {
			return VerifiedTicket{}, ErrTicketClosed
		}
		if !now.Before(upload.expiresAt) {
			return VerifiedTicket{}, ErrTicketExpired
		}
	}
	return VerifiedTicket{
		ID:                record.id,
		TicketID:          record.id,
		PublicID:          record.publicID,
		Operation:         record.operation,
		RequiredScope:     record.requiredScope,
		AccountID:         record.accountID,
		TokenID:           record.tokenID,
		Principal:         principal,
		Locator:           record.locator,
		Mount:             mount,
		UploadID:          record.uploadID,
		ObjectFingerprint: record.objectFingerprint,
		MaxBytes:          record.maxBytes,
		ConsumedBytes:     record.consumedBytes,
		Status:            record.status,
		ExpiresAt:         record.expiresAt,
	}, nil
}

// AddBytes atomically reserves transfer budget. It is safe to call before
// writing a range/part; a failed write can close the ticket or let the caller
// report the transfer failure without ever exceeding the configured budget.
func (s *Service) AddBytes(ctx context.Context, ticketID string, count int64) error {
	if s == nil || s.db == nil || strings.TrimSpace(ticketID) == "" || count < 0 {
		return ErrInvalidInput
	}
	record, err := s.loadByID(ctx, strings.TrimSpace(ticketID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTicketNotFound
		}
		return err
	}
	now := s.now().UTC()
	if record.status != StatusActive {
		return ErrTicketClosed
	}
	if !now.Before(record.expiresAt) {
		_, _ = s.db.ExecContext(ctx, `UPDATE mcp_transfer_tickets SET status = 'expired', closed_at = ? WHERE id = ? AND status = 'active'`, formatTime(now), record.id)
		return ErrTicketExpired
	}
	updated, err := s.db.ExecContext(ctx, `
UPDATE mcp_transfer_tickets
SET consumed_bytes = consumed_bytes + ?
WHERE id = ?
  AND status = 'active'
  AND expires_at > ?
  AND ? <= (max_bytes - consumed_bytes)
`, count, record.id, formatTime(now), count)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	latest, latestErr := s.loadByID(ctx, record.id)
	if latestErr == nil {
		if latest.status != StatusActive {
			return ErrTicketClosed
		}
		if !now.Before(latest.expiresAt) {
			return ErrTicketExpired
		}
	}
	return ErrByteBudgetExceeded
}

// Close performs a single active -> terminal transition. Closing an already
// closed ticket is always an error, which prevents replaying upload completion
// or cancellation.
func (s *Service) Close(ctx context.Context, ticketID string, status Status) error {
	if s == nil || s.db == nil || strings.TrimSpace(ticketID) == "" {
		return ErrInvalidInput
	}
	if status != StatusCompleted && status != StatusCanceled && status != StatusExpired {
		return ErrInvalidStatus
	}
	record, err := s.loadByID(ctx, strings.TrimSpace(ticketID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTicketNotFound
		}
		return err
	}
	if record.status != StatusActive {
		return ErrTicketClosed
	}
	updated, err := s.db.ExecContext(ctx, `
UPDATE mcp_transfer_tickets
SET status = ?, closed_at = ?
WHERE id = ? AND status = 'active'
`, string(status), formatTime(s.now().UTC()), record.id)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrTicketClosed
	}
	return nil
}

// CloseUploadTickets closes every active upload ticket associated with an
// upload session. Upload cancellation/completion is a control-plane action;
// closing by session ID ensures no previously issued bearer remains usable.
func (s *Service) CloseUploadTickets(ctx context.Context, uploadID string, status Status) error {
	if s == nil || s.db == nil || strings.TrimSpace(uploadID) == "" {
		return ErrInvalidInput
	}
	if status != StatusCanceled && status != StatusCompleted {
		return ErrInvalidStatus
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE mcp_transfer_tickets
SET status = ?, closed_at = ?
WHERE upload_id = ? AND operation = 'upload' AND status = 'active'
`, string(status), formatTime(s.now().UTC()), uploadID)
	return err
}

type ticketRecord struct {
	id                string
	publicID          string
	secretHash        string
	accountID         string
	tokenID           string
	operation         Operation
	requiredScope     aitoken.Scope
	locator           access.Locator
	objectFingerprint string
	uploadID          string
	maxBytes          int64
	consumedBytes     int64
	status            Status
	createdAt         time.Time
	expiresAt         time.Time
	closedAt          sql.NullString
}

type uploadRecord struct {
	accountID  string
	mountID    string
	targetPath string
	status     string
	expiresAt  time.Time
}

func (s *Service) insert(ctx context.Context, principal aitoken.Principal, mount access.AuthorizedMount, operation Operation, scope aitoken.Scope, uploadID, objectFingerprint string, maxBytes int64, expiresAt time.Time) (IssuedTicket, error) {
	if s == nil || s.db == nil {
		return IssuedTicket{}, ErrInvalidInput
	}
	publicID, err := newID("mcptkt")
	if err != nil {
		return IssuedTicket{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return IssuedTicket{}, err
	}
	id, err := newID("mcptktrow")
	if err != nil {
		return IssuedTicket{}, err
	}
	now := s.now().UTC().Round(0)
	_, err = s.db.ExecContext(ctx, `
INSERT INTO mcp_transfer_tickets(
    id, public_id, secret_hash, account_id, ai_token_id, operation,
    required_scope, mount_id, relative_path, object_fingerprint,
    upload_id, max_bytes, consumed_bytes, status, created_at, expires_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, 0, 'active', ?, ?)
`, id, publicID, hashSecret(secret), principal.AccountID, principal.TokenID,
		string(operation), string(scope), mount.ID, mount.StorageRelativePath,
		objectFingerprint, uploadID, maxBytes, formatTime(now), formatTime(expiresAt))
	if err != nil {
		return IssuedTicket{}, err
	}
	url := "/mcp/transfers/" + publicID
	return IssuedTicket{
		ID:                id,
		TicketID:          id,
		PublicID:          publicID,
		Secret:            secret,
		BearerToken:       publicID + "." + secret,
		URL:               url,
		TicketURL:         url,
		Operation:         operation,
		RequiredScope:     scope,
		Locator:           access.Locator{Source: mount.Source, MountID: visibleMountID(mount), Path: mount.RelativePath},
		ObjectFingerprint: objectFingerprint,
		MaxBytes:          maxBytes,
		ExpiresAt:         expiresAt,
	}, nil
}

func (s *Service) validate(maxBytes int64, principal aitoken.Principal, locator access.Locator) error {
	if s == nil || s.db == nil || s.tokens == nil || s.guard == nil {
		return ErrInvalidInput
	}
	if maxBytes < 0 || strings.TrimSpace(principal.AccountID) == "" || strings.TrimSpace(principal.TokenID) == "" {
		return ErrInvalidInput
	}
	if _, err := contentref.NormalizeForAutomation(locator); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func (s *Service) load(ctx context.Context, publicID string) (ticketRecord, error) {
	var item ticketRecord
	var operation, scope, pathValue, status, createdAt, expiresAt string
	var uploadID sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT id, public_id, secret_hash, account_id, ai_token_id, operation,
       required_scope, mount_id, relative_path, object_fingerprint,
       upload_id, max_bytes, consumed_bytes, status, created_at, expires_at, closed_at
FROM mcp_transfer_tickets WHERE public_id = ?
`, publicID).Scan(&item.id, &item.publicID, &item.secretHash, &item.accountID, &item.tokenID,
		&operation, &scope, &item.locator.MountID, &pathValue,
		&item.objectFingerprint, &uploadID, &item.maxBytes, &item.consumedBytes, &status,
		&createdAt, &expiresAt, &item.closedAt)
	if err != nil {
		return ticketRecord{}, err
	}
	item.operation = Operation(operation)
	item.requiredScope = aitoken.Scope(scope)
	item.locator.Path = pathValue
	item.uploadID = uploadID.String
	item.status = Status(status)
	item.createdAt, err = parseTime(createdAt)
	if err != nil {
		return ticketRecord{}, err
	}
	item.expiresAt, err = parseTime(expiresAt)
	if err != nil {
		return ticketRecord{}, err
	}
	return item, nil
}

func (s *Service) loadByID(ctx context.Context, id string) (ticketRecord, error) {
	var publicID, operation, scope, pathValue, status, createdAt, expiresAt string
	var uploadID sql.NullString
	var item ticketRecord
	err := s.db.QueryRowContext(ctx, `
SELECT id, public_id, secret_hash, account_id, ai_token_id, operation,
       required_scope, mount_id, relative_path, object_fingerprint,
       upload_id, max_bytes, consumed_bytes, status, created_at, expires_at, closed_at
FROM mcp_transfer_tickets WHERE id = ? OR public_id = ?
`, id, id).Scan(&item.id, &publicID, &item.secretHash, &item.accountID, &item.tokenID,
		&operation, &scope, &item.locator.MountID, &pathValue,
		&item.objectFingerprint, &uploadID, &item.maxBytes, &item.consumedBytes, &status,
		&createdAt, &expiresAt, &item.closedAt)
	if err != nil {
		return ticketRecord{}, err
	}
	item.publicID = publicID
	item.operation = Operation(operation)
	item.requiredScope = aitoken.Scope(scope)
	item.locator.Path = pathValue
	item.uploadID = uploadID.String
	item.status = Status(status)
	item.createdAt, err = parseTime(createdAt)
	if err != nil {
		return ticketRecord{}, err
	}
	item.expiresAt, err = parseTime(expiresAt)
	if err != nil {
		return ticketRecord{}, err
	}
	return item, nil
}

func (s *Service) loadUpload(ctx context.Context, uploadID string) (uploadRecord, error) {
	var item uploadRecord
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `
SELECT account_id, mount_id, target_relative_path, status, expires_at
FROM upload_sessions WHERE id = ?
`, uploadID).Scan(&item.accountID, &item.mountID, &item.targetPath, &item.status, &expiresAt)
	if err != nil {
		return uploadRecord{}, err
	}
	item.expiresAt, err = parseTime(expiresAt)
	if err != nil {
		return uploadRecord{}, err
	}
	return item, nil
}

func clampExpiry(now time.Time, ttl time.Duration, limits ...time.Time) (time.Time, error) {
	if ttl <= 0 {
		return time.Time{}, ErrInvalidInput
	}
	expiresAt := now.Add(ttl)
	for _, limit := range limits {
		if !limit.IsZero() && limit.Before(expiresAt) {
			expiresAt = limit
		}
	}
	if !now.Before(expiresAt) {
		return time.Time{}, ErrTicketExpired
	}
	return expiresAt.UTC().Round(0), nil
}

func requireRegularFile(mount access.AuthorizedMount) error {
	info, err := os.Lstat(filepath.Join(mount.Root, filepath.FromSlash(mount.RelativePath)))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegularFile
	}
	return nil
}

func objectSize(mount access.AuthorizedMount) (int64, error) {
	info, err := os.Lstat(filepath.Join(mount.Root, filepath.FromSlash(mount.RelativePath)))
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, ErrNotRegularFile
	}
	return info.Size(), nil
}

func fingerprintObject(mount access.AuthorizedMount) (string, error) {
	info, err := lstatObject(mount)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return missingFingerprint(mount), nil
		}
		return "", err
	}
	return FingerprintFileInfo(mount, info), nil
}

// lstatObject inspects the authorized object through the host path the ticket
// database was populated from. It rejects symlinks exactly like the
// authorization boundary, so a swapped path cannot be fingerprinted.
func lstatObject(mount access.AuthorizedMount) (os.FileInfo, error) {
	pathValue := filepath.Join(mount.Root, filepath.FromSlash(mount.RelativePath))
	info, err := os.Lstat(pathValue)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, access.ErrBoundaryViolation
	}
	return info, nil
}

// FingerprintFileInfo encodes the ticket-v2 object fingerprint from metadata
// that a verified descriptor (or the equivalent path metadata) reports for the
// authorized object. It binds the mount ID and storage-relative path together
// with the file's type, permissions, size, UTC nanosecond mtime and its
// device/inode identity, then hashes the raw record. The format is stable and
// MUST stay byte-identical: the server compares the fingerprint of an
// already-open descriptor against the one stored on the ticket, so both sides
// must agree on every field below. It deliberately does not reuse the
// differently-shaped files.Fingerprint.
func FingerprintFileInfo(mount access.AuthorizedMount, info os.FileInfo) string {
	if info == nil {
		return missingFingerprint(mount)
	}
	sys := ""
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		sys = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	raw := fmt.Sprintf("omnora-object-v2|%s|%s|%s|%o|%d|%d|%s|%s", mount.ID, mount.StorageRelativePath,
		info.Mode().Type(), info.Mode().Perm(), info.Size(), info.ModTime().UTC().UnixNano(), sys, info.Mode().String())
	return hashBytes([]byte(raw))
}

func missingFingerprint(mount access.AuthorizedMount) string {
	return hashBytes([]byte("omnora-object-missing-v2|" + mount.ID + "|" + mount.StorageRelativePath))
}

func visibleMountID(mount access.AuthorizedMount) string {
	if mount.Source == contentref.SourcePersonal {
		return ""
	}
	return mount.ID
}

func (s *Service) locatorForStoredTarget(ctx context.Context, accountID, mountID, storagePath string) (access.Locator, error) {
	var purpose domain.MountPurpose
	if err := s.db.QueryRowContext(ctx, `SELECT purpose FROM mounts WHERE id = ? AND status = 'active'`, mountID).Scan(&purpose); err != nil {
		return access.Locator{}, err
	}
	cleaned, err := storage.CleanRelativePath(storagePath)
	if err != nil {
		return access.Locator{}, err
	}
	switch purpose {
	case domain.MountPurposePersonalDefault:
		if cleaned == accountID {
			cleaned = "."
		} else if strings.HasPrefix(cleaned, accountID+"/") {
			cleaned = strings.TrimPrefix(cleaned, accountID+"/")
		} else {
			return access.Locator{}, ErrTicketInvalid
		}
		return access.Locator{Source: contentref.SourcePersonal, Path: cleaned}, nil
	case domain.MountPurposeCommon:
		return access.Locator{Source: contentref.SourceCommonMount, MountID: mountID, Path: cleaned}, nil
	default:
		return access.Locator{}, ErrTicketInvalid
	}
}

func parseBearer(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "bearer ") {
		value = strings.TrimSpace(value[7:])
	}
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", "", ErrTicketInvalid
	}
	publicID, secret, ok := strings.Cut(value, ".")
	if !ok || publicID == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", ErrTicketInvalid
	}
	return publicID, secret, nil
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func newID(prefix string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	return prefix + "_" + secret, nil
}

func hashSecret(value string) string {
	return hashBytes([]byte(value))
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func secretMatches(secret, encodedHash string) bool {
	want := hashSecret(secret)
	return subtle.ConstantTimeCompare([]byte(want), []byte(encodedHash)) == 1
}

func formatTime(value time.Time) string {
	return value.UTC().Round(0).Format(timestampLayout)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(timestampLayout, value)
	if err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err = time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, err
}
