// Package personalstorage owns the system default mount and the account-scoped
// directory inside it. Public callers identify only the authenticated account;
// the default mount ID and host paths never cross the service boundary.
package personalstorage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"omnora/internal/access"
	"omnora/internal/domain"
	"omnora/internal/files"
	"omnora/internal/mountid"
	"omnora/internal/storage"
)

const (
	DefaultMountID       = "personal-default"
	DefaultMountRootPath = "personal"
)

var (
	ErrUnavailable  = errors.New("personal storage: unavailable")
	ErrInvalidInput = errors.New("personal storage: invalid input")
)

type Mount struct {
	ID       string
	RootPath string
}

type Directory struct {
	AccountID    string
	RelativePath string
	State        string
}

type PersonalSource struct {
	Source string `json:"source"`
	Label  string `json:"label"`
}

type CommonMountSource struct {
	Source      string           `json:"source"`
	MountID     string           `json:"mountId"`
	DisplayName string           `json:"displayName"`
	Permission  string           `json:"permission"`
	Mode        domain.MountMode `json:"mode"`
}

type ContentSources struct {
	Personal     PersonalSource      `json:"personal"`
	CommonMounts []CommonMountSource `json:"commonMounts"`
}

type Service struct {
	db         *sql.DB
	managedDir string
}

func New(db *sql.DB, managedDir string) *Service {
	return &Service{db: db, managedDir: filepath.Clean(strings.TrimSpace(managedDir))}
}

// EnsureDefaultMount creates the one protected personal mount on a new target
// database and validates its immutable classification on later calls.
func (s *Service) EnsureDefaultMount(ctx context.Context) (Mount, error) {
	if s == nil || s.db == nil || !filepath.IsAbs(s.managedDir) {
		return Mount{}, ErrInvalidInput
	}
	personalRoot := filepath.Join(s.managedDir, DefaultMountRootPath)
	if _, err := os.Lstat(personalRoot); errors.Is(err, os.ErrNotExist) {
		var accounts int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&accounts); err != nil || accounts != 0 {
			return Mount{}, ErrUnavailable
		}
		if err := os.MkdirAll(personalRoot, 0o700); err != nil {
			return Mount{}, fmt.Errorf("%w: create personal root", ErrUnavailable)
		}
	} else if err != nil {
		return Mount{}, ErrUnavailable
	}
	if err := ensureDirectory(personalRoot); err != nil {
		return Mount{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Mount{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mounts WHERE purpose = 'personal_default' AND status <> 'deleted'`).Scan(&count); err != nil {
		return Mount{}, err
	}
	if count == 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status)
VALUES (?, 'Personal files', ?, 'personal_default', 'managed', 'system', 'read_write', 1, 'active')
`, DefaultMountID, DefaultMountRootPath); err != nil {
			return Mount{}, err
		}
	} else if count != 1 {
		return Mount{}, ErrUnavailable
	}
	var mount Mount
	var purpose, storageKind, governance, mode, status string
	var storedIdentity sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT id, root_path, purpose, storage_kind, governance, mode, status, mount_identity_json
FROM mounts
WHERE purpose = 'personal_default' AND status <> 'deleted'
`).Scan(&mount.ID, &mount.RootPath, &purpose, &storageKind, &governance, &mode, &status, &storedIdentity); err != nil {
		return Mount{}, err
	}
	if mount.ID != DefaultMountID || mount.RootPath != DefaultMountRootPath || purpose != "personal_default" || storageKind != "managed" || governance != "system" || mode != "read_write" || (status != "active" && status != "unavailable") {
		return Mount{}, ErrUnavailable
	}
	currentIdentity, err := mountid.Capture(personalRoot)
	if err != nil {
		return Mount{}, ErrUnavailable
	}
	if !storedIdentity.Valid || strings.TrimSpace(storedIdentity.String) == "" {
		if status != "active" {
			return Mount{}, ErrUnavailable
		}
		encoded, err := json.Marshal(currentIdentity)
		if err != nil {
			return Mount{}, ErrUnavailable
		}
		if _, err := tx.ExecContext(ctx, `UPDATE mounts SET mount_identity_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND mount_identity_json IS NULL`, string(encoded), mount.ID); err != nil {
			return Mount{}, err
		}
	} else {
		var expectedIdentity mountid.Identity
		if err := json.Unmarshal([]byte(storedIdentity.String), &expectedIdentity); err != nil || !access.MountIdentityMatches(expectedIdentity, currentIdentity) {
			return Mount{}, ErrUnavailable
		}
		if status == "unavailable" {
			result, err := tx.ExecContext(ctx, `UPDATE mounts SET status = 'active', updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'unavailable'`, mount.ID)
			if err != nil {
				return Mount{}, err
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 1 {
				return Mount{}, ErrUnavailable
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Mount{}, err
	}
	return mount, nil
}

// ProvisionAccount prepares the physical directory and records its binding in
// the caller's account transaction. The returned cleanup must be called if the
// surrounding transaction fails; it removes only the newly created empty dir.
func (s *Service) ProvisionAccount(ctx context.Context, tx *sql.Tx, accountID string) (Directory, func(), error) {
	accountID = strings.TrimSpace(accountID)
	if s == nil || tx == nil || !validAccountID(accountID) || !filepath.IsAbs(s.managedDir) {
		return Directory{}, func() {}, ErrInvalidInput
	}
	root := filepath.Join(s.managedDir, DefaultMountRootPath)
	if err := ensureDirectory(root); err != nil {
		return Directory{}, func() {}, err
	}
	physical := filepath.Join(root, accountID)
	if err := os.Mkdir(physical, 0o700); err != nil {
		return Directory{}, func() {}, fmt.Errorf("%w: create account directory", ErrUnavailable)
	}
	cleanup := func() { _ = os.Remove(physical) }
	directory := Directory{AccountID: accountID, RelativePath: accountID, State: "ready"}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO personal_directories(account_id, relative_path, state)
VALUES (?, ?, 'ready')
`, accountID, accountID); err != nil {
		cleanup()
		return Directory{}, func() {}, err
	}
	return directory, cleanup, nil
}

// Resolve returns only the authenticated account's ready directory. Deleted,
// disabled, retained, missing and symlinked roots all fail closed.
func (s *Service) Resolve(ctx context.Context, accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if s == nil || s.db == nil || !validAccountID(accountID) || !filepath.IsAbs(s.managedDir) {
		return "", ErrInvalidInput
	}
	var relativePath, state, identityJSON string
	err := s.db.QueryRowContext(ctx, `
SELECT pd.relative_path, pd.state, COALESCE(m.mount_identity_json, '')
FROM personal_directories pd
JOIN accounts a ON a.id = pd.account_id
JOIN mounts m ON m.id = ?
WHERE pd.account_id = ?
  AND a.status = 'active'
  AND pd.state = 'ready'
  AND pd.relative_path = pd.account_id
  AND m.purpose = 'personal_default'
  AND m.storage_kind = 'managed'
  AND m.governance = 'system'
  AND m.mode = 'read_write'
  AND m.status = 'active'
`, DefaultMountID, accountID).Scan(&relativePath, &state, &identityJSON)
	if err != nil {
		return "", ErrUnavailable
	}
	var expectedIdentity mountid.Identity
	personalRoot := filepath.Join(s.managedDir, DefaultMountRootPath)
	currentIdentity, captureErr := mountid.Capture(personalRoot)
	if identityJSON == "" || json.Unmarshal([]byte(identityJSON), &expectedIdentity) != nil || captureErr != nil || !access.MountIdentityMatches(expectedIdentity, currentIdentity) {
		return "", ErrUnavailable
	}
	root := filepath.Join(s.managedDir, DefaultMountRootPath, relativePath)
	if err := ensureDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

// ContentSources returns member-safe discovery data. The protected default
// mount never appears in the response; common mounts require an explicit live
// grant even when the current account is a system administrator.
func (s *Service) ContentSources(ctx context.Context, accountID string) (ContentSources, error) {
	if _, err := s.Resolve(ctx, accountID); err != nil {
		return ContentSources{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.display_name, mg.permission, m.mode
FROM mount_grants mg
JOIN mounts m ON m.id = mg.mount_id
JOIN accounts a ON a.id = mg.account_id
WHERE mg.account_id = ?
  AND a.status = 'active'
  AND m.purpose = 'common'
  AND m.storage_kind = 'external'
  AND m.governance IN ('normal', 'restricted')
  AND m.status = 'active'
ORDER BY m.display_name, m.id
`, accountID)
	if err != nil {
		return ContentSources{}, err
	}
	defer rows.Close()
	result := ContentSources{
		Personal:     PersonalSource{Source: "personal", Label: "My files"},
		CommonMounts: []CommonMountSource{},
	}
	for rows.Next() {
		item := CommonMountSource{Source: "common_mount"}
		if err := rows.Scan(&item.MountID, &item.DisplayName, &item.Permission, &item.Mode); err != nil {
			return ContentSources{}, err
		}
		if item.Permission != "viewer" && item.Permission != "editor" {
			return ContentSources{}, ErrUnavailable
		}
		result.CommonMounts = append(result.CommonMounts, item)
	}
	if err := rows.Err(); err != nil {
		return ContentSources{}, err
	}
	return result, nil
}

// ListPersonal lists only the current account's isolated root. It does not
// accept a mount ID or another account ID from an external locator.
func (s *Service) ListPersonal(ctx context.Context, accountID, relativePath string) (files.DirectoryListing, error) {
	root, err := s.Resolve(ctx, accountID)
	if err != nil {
		return files.DirectoryListing{}, err
	}
	return files.NewService().ListDirectory(files.Mount{Root: root, Mode: domain.MountModeReadWrite}, relativePath)
}

// ListCommon performs a live account/grant/classification/identity check for
// every request. Missing or unauthorized mounts share ErrUnavailable so HTTP
// adapters cannot reveal whether a guessed mount exists.
func (s *Service) ListCommon(ctx context.Context, accountID, mountID, relativePath string) (files.DirectoryListing, error) {
	accountID = strings.TrimSpace(accountID)
	mountID = strings.TrimSpace(mountID)
	if s == nil || s.db == nil || !validAccountID(accountID) || mountID == "" {
		return files.DirectoryListing{}, ErrInvalidInput
	}
	var root, permission, mode, identityJSON string
	err := s.db.QueryRowContext(ctx, `
SELECT m.root_path, mg.permission, m.mode, COALESCE(m.mount_identity_json, '')
FROM mount_grants mg
JOIN mounts m ON m.id = mg.mount_id
JOIN accounts a ON a.id = mg.account_id
WHERE mg.account_id = ?
  AND mg.mount_id = ?
  AND a.status = 'active'
  AND m.purpose = 'common'
  AND m.storage_kind = 'external'
  AND m.governance IN ('normal', 'restricted')
  AND m.status = 'active'
`, accountID, mountID).Scan(&root, &permission, &mode, &identityJSON)
	if err != nil || (permission != "viewer" && permission != "editor") || (mode != "read_only" && mode != "read_write") || !filepath.IsAbs(root) || identityJSON == "" {
		return files.DirectoryListing{}, ErrUnavailable
	}
	var stored mountid.Identity
	if err := json.Unmarshal([]byte(identityJSON), &stored); err != nil {
		return files.DirectoryListing{}, ErrUnavailable
	}
	current, err := mountid.Capture(root)
	if err != nil || !access.MountIdentityMatches(stored, current) {
		return files.DirectoryListing{}, ErrUnavailable
	}
	effectiveMode := domain.MountMode(mode)
	if permission == "viewer" {
		effectiveMode = domain.MountModeReadOnly
	}
	return files.NewService().ListDirectory(files.Mount{Root: root, Mode: effectiveMode}, relativePath)
}

// ListCollaboration resolves an incoming collaboration through the recipient's
// live authorization and the owner's protected personal directory. A revoked
// collaboration, replaced root, disabled account, or guessed ID is hidden
// behind ErrUnavailable.
func (s *Service) ListCollaboration(ctx context.Context, accountID, collaborationID, relativePath string) (files.DirectoryListing, error) {
	accountID = strings.TrimSpace(accountID)
	collaborationID = strings.TrimSpace(collaborationID)
	if s == nil || s.db == nil || !validAccountID(accountID) || collaborationID == "" {
		return files.DirectoryListing{}, ErrInvalidInput
	}
	var ownerID, collaborationRoot, permission, identityJSON string
	err := s.db.QueryRowContext(ctx, `
SELECT collaboration.owner_account_id, collaboration.root_relative_path,
       collaboration.permission, collaboration.root_identity_json
FROM folder_collaborations AS collaboration
JOIN accounts AS recipient ON recipient.id = collaboration.recipient_account_id
JOIN accounts AS owner ON owner.id = collaboration.owner_account_id
JOIN personal_directories AS directory ON directory.account_id = collaboration.owner_account_id
WHERE collaboration.id = ?
  AND collaboration.recipient_account_id = ?
  AND collaboration.revoked_at IS NULL
  AND recipient.status = 'active'
  AND owner.status = 'active'
  AND directory.state = 'ready'
`, collaborationID, accountID).Scan(&ownerID, &collaborationRoot, &permission, &identityJSON)
	if err != nil || (permission != "viewer" && permission != "editor") || identityJSON == "" {
		return files.DirectoryListing{}, ErrUnavailable
	}
	cleanedRoot, err := storage.CleanRelativePath(collaborationRoot)
	if err != nil || strings.Contains(cleanedRoot, `\`) {
		return files.DirectoryListing{}, ErrUnavailable
	}
	ownerRoot, err := s.Resolve(ctx, ownerID)
	if err != nil {
		return files.DirectoryListing{}, ErrUnavailable
	}
	root := ownerRoot
	if cleanedRoot != "." {
		root = filepath.Join(ownerRoot, filepath.FromSlash(cleanedRoot))
	}
	var expected mountid.Identity
	if err := json.Unmarshal([]byte(identityJSON), &expected); err != nil {
		return files.DirectoryListing{}, ErrUnavailable
	}
	current, err := mountid.Capture(root)
	if err != nil || !access.MountIdentityMatches(expected, current) {
		return files.DirectoryListing{}, ErrUnavailable
	}
	mode := domain.MountModeReadWrite
	if permission == "viewer" {
		mode = domain.MountModeReadOnly
	}
	return files.NewService().ListDirectory(files.Mount{Root: root, Mode: mode}, relativePath)
}

// RetainAccount marks the identity and directory as deleted/retained without
// touching the physical directory or any file below it.
func (s *Service) RetainAccount(ctx context.Context, tx *sql.Tx, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if s == nil || tx == nil || !validAccountID(accountID) {
		return ErrInvalidInput
	}
	result, err := tx.ExecContext(ctx, `
UPDATE personal_directories
SET state = 'retained', updated_at = CURRENT_TIMESTAMP
WHERE account_id = ? AND state = 'ready'
`, accountID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrUnavailable
	}
	result, err = tx.ExecContext(ctx, `
UPDATE accounts
SET status = 'deleted', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status <> 'deleted'
`, accountID)
	if err != nil {
		return err
	}
	rows, err = result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrUnavailable
	}
	return nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	return nil
}

func validAccountID(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
