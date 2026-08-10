// Package foldercollab owns account-to-account personal-directory grants.
package foldercollab

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"omnora/internal/domain"
	"omnora/internal/mountid"
	"omnora/internal/storage"
)

var (
	ErrInvalidInput     = errors.New("folder collaboration: invalid input")
	ErrForbidden        = errors.New("folder collaboration: forbidden")
	ErrNotFound         = errors.New("folder collaboration: not found")
	ErrOverlappingGrant = errors.New("folder collaboration: overlapping grant")
	ErrUnavailable      = errors.New("folder collaboration: unavailable")
)

type Direction string

const (
	DirectionIncoming Direction = "incoming"
	DirectionOutgoing Direction = "outgoing"
)

type CreateRequest struct {
	RecipientID      string
	RootRelativePath string
	Permission       domain.ContentPermission
}

type UpdateRequest struct{ Permission domain.ContentPermission }

type Item struct {
	ID                   string                   `json:"id"`
	OwnerAccountID       string                   `json:"ownerAccountId"`
	RecipientAccountID   string                   `json:"recipientAccountId"`
	RootRelativePath     string                   `json:"rootRelativePath"`
	FolderName           string                   `json:"folderName"`
	OwnerDisplayName     string                   `json:"ownerDisplayName"`
	RecipientDisplayName string                   `json:"recipientDisplayName"`
	Permission           domain.ContentPermission `json:"permission"`
	Status               string                   `json:"status"`
}

type Service struct {
	db         *sql.DB
	managedDir string
	now        func() time.Time
}

func New(db *sql.DB, managedDir string) *Service {
	return &Service{db: db, managedDir: filepath.Clean(strings.TrimSpace(managedDir)), now: time.Now}
}

func (s *Service) Create(ctx context.Context, actorID string, req CreateRequest) (Item, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actorID) == "" || strings.TrimSpace(req.RecipientID) == "" || actorID == req.RecipientID || !req.Permission.Valid() {
		return Item{}, ErrInvalidInput
	}
	_, identityJSON, err := s.resolveOwnerRoot(ctx, actorID, req.RootRelativePath)
	if err != nil {
		return Item{}, err
	}
	if err := s.requireActiveAccount(ctx, req.RecipientID); err != nil {
		return Item{}, err
	}
	cleaned, err := cleanRelative(req.RootRelativePath)
	if err != nil {
		return Item{}, ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT root_relative_path FROM folder_collaborations
WHERE owner_account_id = ? AND recipient_account_id = ? AND revoked_at IS NULL`, actorID, req.RecipientID)
	if err != nil {
		return Item{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return Item{}, err
		}
		if pathsOverlap(existing, cleaned) {
			return Item{}, ErrOverlappingGrant
		}
	}
	if err := rows.Err(); err != nil {
		return Item{}, err
	}
	id, err := newID("collab")
	if err != nil {
		return Item{}, err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO folder_collaborations(id, owner_account_id, recipient_account_id, root_relative_path, root_identity_json, permission, created_by_account_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`, id, actorID, req.RecipientID, cleaned, identityJSON, req.Permission, actorID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Item{}, ErrOverlappingGrant
		}
		return Item{}, err
	}
	return s.load(ctx, id)
}

func (s *Service) Update(ctx context.Context, actorID, id string, req UpdateRequest) (Item, error) {
	if !req.Permission.Valid() || strings.TrimSpace(actorID) == "" || strings.TrimSpace(id) == "" {
		return Item{}, ErrInvalidInput
	}
	if !s.canManage(ctx, actorID, id) {
		return Item{}, ErrForbidden
	}
	result, err := s.db.ExecContext(ctx, `UPDATE folder_collaborations SET permission = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`, req.Permission, id)
	if err != nil {
		return Item{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Item{}, ErrNotFound
	}
	return s.load(ctx, id)
}

func (s *Service) Revoke(ctx context.Context, actorID, id string) error {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(id) == "" {
		return ErrInvalidInput
	}
	if !s.canManage(ctx, actorID, id) {
		return ErrForbidden
	}
	_, err := s.db.ExecContext(ctx, `UPDATE folder_collaborations SET revoked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`, id)
	return err
}

func (s *Service) List(ctx context.Context, accountID string, direction Direction) ([]Item, error) {
	if strings.TrimSpace(accountID) == "" || (direction != DirectionIncoming && direction != DirectionOutgoing) {
		return nil, ErrInvalidInput
	}
	column := "recipient_account_id"
	if direction == DirectionOutgoing {
		column = "owner_account_id"
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.owner_account_id, c.recipient_account_id, c.root_relative_path, c.permission,
       owner.display_name, recipient.display_name
FROM folder_collaborations c
JOIN accounts owner ON owner.id = c.owner_account_id
JOIN accounts recipient ON recipient.id = c.recipient_account_id
WHERE c.`+column+` = ? AND c.revoked_at IS NULL AND owner.status = 'active' AND recipient.status = 'active'
ORDER BY c.created_at DESC, c.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.OwnerAccountID, &item.RecipientAccountID, &item.RootRelativePath, &item.Permission, &item.OwnerDisplayName, &item.RecipientDisplayName); err != nil {
			return nil, err
		}
		item.FolderName = filepath.Base(item.RootRelativePath)
		if item.FolderName == "." || item.FolderName == string(filepath.Separator) {
			item.FolderName = "Personal files"
		}
		item.Status = "active"
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Resolve(ctx context.Context, accountID, id, relativePath string, write bool) (string, domain.ContentPermission, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(id) == "" {
		return "", "", ErrInvalidInput
	}
	var ownerID, rootRelative, identityJSON, permission string
	err := s.db.QueryRowContext(ctx, `
SELECT owner_account_id, root_relative_path, root_identity_json, permission
FROM folder_collaborations
WHERE id = ? AND recipient_account_id = ? AND revoked_at IS NULL`, id, accountID).Scan(&ownerID, &rootRelative, &identityJSON, &permission)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if permission != string(domain.ContentPermissionViewer) && permission != string(domain.ContentPermissionEditor) {
		return "", "", ErrUnavailable
	}
	if write && permission != string(domain.ContentPermissionEditor) {
		return "", "", ErrForbidden
	}
	ownerRoot, _, err := s.resolveOwnerRoot(ctx, ownerID, ".")
	if err != nil {
		return "", "", ErrUnavailable
	}
	root, err := cleanRelative(rootRelative)
	if err != nil {
		return "", "", ErrUnavailable
	}
	physical := ownerRoot
	if root != "." {
		physical = filepath.Join(ownerRoot, filepath.FromSlash(root))
	}
	if err := ensureDirectoryNoSymlink(physical); err != nil {
		return "", "", ErrUnavailable
	}
	var expected mountid.Identity
	if err := json.Unmarshal([]byte(identityJSON), &expected); err != nil {
		return "", "", ErrUnavailable
	}
	current, err := mountid.Capture(physical)
	if err != nil || !sameIdentity(expected, current) {
		return "", "", ErrUnavailable
	}
	return physical, domain.ContentPermission(permission), nil
}

func (s *Service) canManage(ctx context.Context, actorID, id string) bool {
	var ownerID string
	return s.db.QueryRowContext(ctx, `SELECT owner_account_id FROM folder_collaborations WHERE id = ? AND revoked_at IS NULL`, id).Scan(&ownerID) == nil && ownerID == actorID
}

func (s *Service) load(ctx context.Context, id string) (Item, error) {
	var item Item
	err := s.db.QueryRowContext(ctx, `
SELECT c.id, c.owner_account_id, c.recipient_account_id, c.root_relative_path, c.permission,
       owner.display_name, recipient.display_name
FROM folder_collaborations c JOIN accounts owner ON owner.id = c.owner_account_id JOIN accounts recipient ON recipient.id = c.recipient_account_id
WHERE c.id = ? AND c.revoked_at IS NULL`, id).Scan(&item.ID, &item.OwnerAccountID, &item.RecipientAccountID, &item.RootRelativePath, &item.Permission, &item.OwnerDisplayName, &item.RecipientDisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	item.FolderName = filepath.Base(item.RootRelativePath)
	if item.FolderName == "." {
		item.FolderName = "Personal files"
	}
	item.Status = "active"
	return item, nil
}

func (s *Service) resolveOwnerRoot(ctx context.Context, ownerID, relative string) (string, string, error) {
	if !validAccountID(ownerID) || !filepath.IsAbs(s.managedDir) {
		return "", "", ErrUnavailable
	}
	cleaned, err := cleanRelative(relative)
	if err != nil {
		return "", "", ErrInvalidInput
	}
	base := filepath.Join(s.managedDir, "personal", ownerID)
	physical := base
	if cleaned != "." {
		physical = filepath.Join(base, filepath.FromSlash(cleaned))
	}
	if err := ensureDirectoryNoSymlink(physical); err != nil {
		return "", "", ErrUnavailable
	}
	identity, err := mountid.Capture(physical)
	if err != nil {
		return "", "", ErrUnavailable
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", "", err
	}
	return base, string(encoded), nil
}

func (s *Service) requireActiveAccount(ctx context.Context, accountID string) error {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, accountID).Scan(&status); err != nil {
		return ErrNotFound
	}
	if status != "active" {
		return ErrNotFound
	}
	return nil
}

func cleanRelative(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		raw = "."
	}
	if strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return "", ErrInvalidInput
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", ErrInvalidInput
		}
	}
	return storage.CleanRelativePath(raw)
}

func pathsOverlap(a, b string) bool {
	return a == b || a == "." || b == "." || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func ensureDirectoryNoSymlink(value string) error {
	cleaned := filepath.Clean(value)
	for current := cleaned; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func sameIdentity(a, b mountid.Identity) bool {
	return filepath.Clean(a.Path) == filepath.Clean(b.Path) && a.Device == b.Device && a.Inode == b.Inode
}

func validAccountID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}
