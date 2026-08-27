package fileops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"

	"omnora/internal/files"
)

// ShareInvalidator revokes shares rooted at a path (and its descendants)
// using the caller's transaction, so revocation is atomic with the durable
// operation row that authorized it.
type ShareInvalidator interface {
	RevokePathTx(ctx context.Context, tx *sql.Tx, mountID, storageRelativePath string) error
}

// AuditWriter records the audit event for the durable operation that
// authorized a filesystem mutation. It runs inside the same transaction as
// the operation insert and share invalidation.
type AuditWriter func(ctx context.Context, tx *sql.Tx) error

// Coordinator wires the durable operation journal, share invalidation, and
// audit trail to the underlying filesystem primitives in files.Service. It
// prepares every mutation durably before touching the filesystem, and
// reconciles the journal afterward instead of leaving audit as a best-effort
// side effect of a completed filesystem call.
type Coordinator struct {
	journal *Journal
	files   files.Service
	shares  ShareInvalidator
	newID   func() string
}

type Option func(*Coordinator)

// WithShareInvalidator configures transactional share revocation for
// mutations that must invalidate shares at their source path.
func WithShareInvalidator(invalidator ShareInvalidator) Option {
	return func(c *Coordinator) { c.shares = invalidator }
}

// WithIDGenerator overrides the default random operation ID generator. It
// exists primarily so tests can assert on deterministic IDs.
func WithIDGenerator(fn func() string) Option {
	return func(c *Coordinator) {
		if fn != nil {
			c.newID = fn
		}
	}
}

// NewCoordinator constructs a Coordinator backed by db.
func NewCoordinator(db *sql.DB, opts ...Option) *Coordinator {
	c := &Coordinator{
		journal: NewJournal(db),
		files:   files.NewService(),
		newID:   newOperationID,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Rename durably journals a same-mount rename, invalidates shares rooted at
// the source path, records the caller's audit event, and only then performs
// the filesystem rename. A filesystem failure leaves the operation in
// recovery_required rather than silently losing track of the attempt.
func (c *Coordinator) Rename(ctx context.Context, mount files.Mount, mountID, from, to string, audit AuditWriter) (string, error) {
	return c.mutatePath(ctx, KindRename, mountID, from, to, audit, func() (string, error) {
		return c.files.Rename(mount, from, to)
	})
}

// Move durably journals a same-mount move; see Rename for the transactional
// contract.
func (c *Coordinator) Move(ctx context.Context, mount files.Mount, mountID, from, toDir string, audit AuditWriter) (string, error) {
	return c.mutatePath(ctx, KindMove, mountID, from, toDir, audit, func() (string, error) {
		return c.files.Move(mount, from, toDir)
	})
}

// MoveAcrossMounts durably records a cross-mount move before the filesystem
// operation starts. The files service keeps the source in operation-scoped
// staging until destination publication and source cleanup are both proven;
// an incomplete result remains visible as recovery_required.
func (c *Coordinator) MoveAcrossMounts(ctx context.Context, source, destination files.Mount,
	sourceMountID, destinationMountID, from, toDir string, audit AuditWriter) (string, error) {
	if c == nil || c.journal == nil {
		return "", ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{
		ID: operationID, Kind: KindCrossMountMove,
		SourceMountID: sourceMountID, DestinationMountID: destinationMountID,
		SourceStorageRelativePath: from, DestinationStorageRelativePath: toDir,
	}
	if err := c.journal.Prepare(ctx, spec, c.shareEffect(sourceMountID, from), TxFunc(audit)); err != nil {
		return "", err
	}
	result, err := c.files.MoveAcrossMountsWithOperationID(source, destination, from, toDir, operationID)
	if err != nil {
		return result, c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return result, err
	}
	return result, nil
}

func (c *Coordinator) mutatePath(ctx context.Context, kind Kind, mountID, from, to string, audit AuditWriter, perform func() (string, error)) (string, error) {
	if c == nil || c.journal == nil {
		return "", ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{ID: operationID, Kind: kind, SourceMountID: mountID, DestinationMountID: mountID, SourceStorageRelativePath: from, DestinationStorageRelativePath: to}
	if err := c.journal.Prepare(ctx, spec, c.shareEffect(mountID, from), TxFunc(audit)); err != nil {
		return "", err
	}
	result, err := perform()
	if err != nil {
		return "", c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return result, err
	}
	return result, nil
}

// Delete durably journals a permanent delete; see Rename for the
// transactional contract.
func (c *Coordinator) Delete(ctx context.Context, mount files.Mount, mountID, relativePath string, audit AuditWriter) error {
	if c == nil || c.journal == nil {
		return ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{ID: operationID, Kind: KindDelete, SourceMountID: mountID, SourceStorageRelativePath: relativePath}
	if err := c.journal.Prepare(ctx, spec, c.shareEffect(mountID, relativePath), TxFunc(audit)); err != nil {
		return err
	}
	if err := c.files.Delete(mount, relativePath); err != nil {
		return c.recover(ctx, operationID, err)
	}
	return c.journal.Complete(ctx, operationID)
}

// Trash durably journals a soft delete; see Rename for the transactional
// contract. The trash item keeps its own generated ID, independent of the
// operation ID, so the REST/MCP trash contract is unaffected.
func (c *Coordinator) Trash(ctx context.Context, mount files.Mount, mountID, relativePath string, audit AuditWriter) (files.TrashItem, error) {
	if c == nil || c.journal == nil {
		return files.TrashItem{}, ErrJournalNotConfigured
	}
	operationID := c.newID()
	trashID := "trash_" + newRandomID()
	spec := OperationSpec{ID: operationID, Kind: KindTrash, SourceMountID: mountID, SourceStorageRelativePath: relativePath, DestinationMountID: mountID, DestinationStorageRelativePath: trashID}
	if err := c.journal.Prepare(ctx, spec, c.shareEffect(mountID, relativePath), TxFunc(audit)); err != nil {
		return files.TrashItem{}, err
	}
	item, err := c.files.SoftDeleteWithID(mount, relativePath, trashID)
	if err != nil {
		return files.TrashItem{}, c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return item, err
	}
	return item, nil
}

// TrashToPersonal durably records a deletion whose protected trash belongs to
// the deleting account rather than to the source common mount.
func (c *Coordinator) TrashToPersonal(ctx context.Context, source, personal files.Mount,
	sourceMountID, personalMountID, sourceStorageRelativePath, personalStorageRoot, sourceRelativePath string,
	audit AuditWriter) (files.TrashItem, error) {
	if c == nil || c.journal == nil {
		return files.TrashItem{}, ErrJournalNotConfigured
	}
	operationID := c.newID()
	trashID := "trash_" + newRandomID()
	spec := OperationSpec{
		ID: operationID, Kind: KindTrash,
		SourceMountID: sourceMountID, SourceStorageRelativePath: sourceStorageRelativePath,
		DestinationMountID:             personalMountID,
		DestinationStorageRelativePath: path.Join(personalStorageRoot, ".omnora", "trash", trashID),
	}
	if err := c.journal.Prepare(ctx, spec, c.shareEffect(sourceMountID, sourceStorageRelativePath), TxFunc(audit)); err != nil {
		return files.TrashItem{}, err
	}
	var item files.TrashItem
	var err error
	if sourceMountID == personalMountID && source.Root == personal.Root {
		item, err = c.files.SoftDeleteWithID(source, sourceRelativePath, trashID)
	} else {
		item, err = c.files.SoftDeleteToPersonalTrashWithID(source, personal, sourceRelativePath, trashID)
	}
	if err != nil {
		return files.TrashItem{}, c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return item, err
	}
	return item, nil
}

// RestoreTrash durably journals a trash restore; see Rename for the
// transactional contract. Restore never needs to invalidate shares: any
// share at the original path was already invalidated when the object was
// trashed.
func (c *Coordinator) RestoreTrash(ctx context.Context, mount files.Mount, mountID, trashID string, audit AuditWriter) (string, error) {
	if c == nil || c.journal == nil {
		return "", ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{ID: operationID, Kind: KindTrashRestore, SourceMountID: mountID, SourceStorageRelativePath: trashID}
	if err := c.journal.Prepare(ctx, spec, TxFunc(audit)); err != nil {
		return "", err
	}
	restored, err := c.files.RestoreTrash(mount, trashID)
	if err != nil {
		return "", c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return restored, err
	}
	return restored, nil
}

// PurgeTrash durably journals permanent removal of one trash item.
func (c *Coordinator) PurgeTrash(ctx context.Context, mount files.Mount, mountID, storageRelativePath, trashID string, audit AuditWriter) error {
	if c == nil || c.journal == nil {
		return ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{ID: operationID, Kind: KindTrashPurge, SourceMountID: mountID, SourceStorageRelativePath: storageRelativePath}
	if err := c.journal.Prepare(ctx, spec, TxFunc(audit)); err != nil {
		return err
	}
	if err := c.files.PurgeTrash(mount, trashID); err != nil {
		return c.recover(ctx, operationID, err)
	}
	return c.journal.Complete(ctx, operationID)
}

// EmptyTrash durably journals permanent removal of the account's full trash.
func (c *Coordinator) EmptyTrash(ctx context.Context, mount files.Mount, mountID, storageRelativePath string, audit AuditWriter) (int, error) {
	if c == nil || c.journal == nil {
		return 0, ErrJournalNotConfigured
	}
	operationID := c.newID()
	spec := OperationSpec{ID: operationID, Kind: KindTrashEmpty, SourceMountID: mountID, SourceStorageRelativePath: storageRelativePath}
	if err := c.journal.Prepare(ctx, spec, TxFunc(audit)); err != nil {
		return 0, err
	}
	removed, err := c.files.EmptyTrash(mount)
	if err != nil {
		return 0, c.recover(ctx, operationID, err)
	}
	if err := c.journal.Complete(ctx, operationID); err != nil {
		return removed, err
	}
	return removed, nil
}

// recover marks the operation recovery_required and returns the original
// filesystem error, joined with any failure to record that recovery state
// so callers never silently drop a durable-tracking failure.
func (c *Coordinator) recover(ctx context.Context, operationID string, cause error) error {
	if recErr := c.journal.RequireRecovery(ctx, operationID, cause); recErr != nil {
		return errors.Join(cause, fmt.Errorf("fileops: mark recovery_required: %w", recErr))
	}
	return cause
}

func (c *Coordinator) shareEffect(mountID, relativePath string) TxFunc {
	if c.shares == nil {
		return nil
	}
	return func(ctx context.Context, tx *sql.Tx) error {
		return c.shares.RevokePathTx(ctx, tx, mountID, relativePath)
	}
}

func newOperationID() string {
	return "fop_" + newRandomID()
}

func newRandomID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(buf[:])
}
