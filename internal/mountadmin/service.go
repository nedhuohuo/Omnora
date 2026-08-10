package mountadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"omnora/internal/domain"
	"omnora/internal/mountid"
)

var (
	ErrNotFound             = errors.New("mount not found")
	ErrForbidden            = errors.New("administrator permission is required")
	ErrInvalidInput         = errors.New("invalid mount input")
	ErrRestrictedGovernance = errors.New("restricted mount governance requires the initial administrator")
	ErrMountUnavailable     = errors.New("mount unavailable")
	ErrMountConflict        = errors.New("mount conflict")
	ErrIdentityUnverifiable = errors.New("mount identity unverifiable")
	ErrRootNotAllowed       = errors.New("mount root is not allowed")
	ErrNotWritable          = errors.New("mount root is not writable")
	ErrConfirmationRequired = errors.New("mount name confirmation is required")
)

type Mount struct {
	ID           string
	DisplayName  string
	RootPath     string
	Governance   domain.MountGovernance
	Mode         domain.MountMode
	IndexEnabled bool
	ShareEnabled bool
	Status       string
	GrantCount   int
}

type GrantInput struct {
	AccountID  string
	Permission domain.ContentPermission
}

type Grant struct {
	AccountID   string
	Email       string
	DisplayName string
	Permission  domain.ContentPermission
}

type CreateRequest struct {
	ID           string
	DisplayName  string
	RootPath     string
	Governance   domain.MountGovernance
	Mode         domain.MountMode
	IndexEnabled bool
	Grants       []GrantInput
}

type UpdateRequest struct {
	DisplayName  *string
	Mode         *domain.MountMode
	IndexEnabled *bool
	ShareEnabled *bool
}

type Deletion struct {
	ID          string `json:"id"`
	Deleted     bool   `json:"deleted"`
	DeleteData  bool   `json:"deleteData"`
	DataDeleted bool   `json:"dataDeleted"`
}

type AuditEvent struct {
	Action   string
	TargetID string
	Metadata string
}

type AuditWriter func(context.Context, *sql.Tx, AuditEvent) error

type Service struct {
	db           *sql.DB
	externalRoot string
}

func New(db *sql.DB, externalRoot string) *Service {
	return &Service{db: db, externalRoot: filepath.Clean(strings.TrimSpace(externalRoot))}
}

func (s *Service) isInitialAdmin(ctx context.Context, accountID string) (bool, error) {
	var initialID string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = 'initial_admin_account_id'`).Scan(&initialID)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM accounts ORDER BY created_at ASC, id ASC LIMIT 1`).Scan(&initialID)
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(initialID) == strings.TrimSpace(accountID), nil
}

func (s *Service) authorize(ctx context.Context, accountID string) (bool, error) {
	var role, status string
	if err := s.db.QueryRowContext(ctx, `SELECT role, status FROM accounts WHERE id = ?`, strings.TrimSpace(accountID)).Scan(&role, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrForbidden
		}
		return false, err
	}
	if role != string(domain.AccountRoleAdmin) || status != "active" {
		return false, ErrForbidden
	}
	return s.isInitialAdmin(ctx, accountID)
}

func (s *Service) ListMounts(ctx context.Context, accountID string) ([]Mount, error) {
	initial, err := s.authorize(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.display_name, m.root_path, m.governance, m.mode,
       m.index_enabled, m.share_enabled, m.status, COUNT(mg.account_id)
FROM mounts m
LEFT JOIN mount_grants mg ON mg.mount_id = m.id
WHERE m.purpose = 'common'
  AND m.status <> 'deleted'
  AND (m.governance = 'normal' OR ? = 1)
GROUP BY m.id
ORDER BY lower(m.display_name), m.id
`, initial)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Mount, 0)
	for rows.Next() {
		item, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) LoadMount(ctx context.Context, accountID, mountID string) (Mount, error) {
	initial, err := s.authorize(ctx, accountID)
	if err != nil {
		return Mount{}, err
	}
	var item Mount
	var indexEnabled, shareEnabled int
	var grantCount int
	err = s.db.QueryRowContext(ctx, `
SELECT m.id, m.display_name, m.root_path, m.governance, m.mode,
       m.index_enabled, m.share_enabled, m.status,
       (SELECT COUNT(1) FROM mount_grants mg WHERE mg.mount_id = m.id)
FROM mounts m
WHERE m.id = ?
  AND m.purpose = 'common'
  AND m.status <> 'deleted'
  AND (m.governance = 'normal' OR ? = 1)
`, strings.TrimSpace(mountID), initial).Scan(
		&item.ID, &item.DisplayName, &item.RootPath, &item.Governance, &item.Mode,
		&indexEnabled, &shareEnabled, &item.Status, &grantCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Mount{}, ErrNotFound
	}
	if err != nil {
		return Mount{}, err
	}
	item.IndexEnabled = indexEnabled == 1
	item.ShareEnabled = shareEnabled == 1
	item.GrantCount = grantCount
	return item, nil
}

func (s *Service) CreateMount(ctx context.Context, actorID string, req CreateRequest, audit AuditWriter) (Mount, error) {
	initial, err := s.authorize(ctx, actorID)
	if err != nil {
		return Mount{}, err
	}
	if req.ID = strings.TrimSpace(req.ID); req.ID == "" {
		return Mount{}, fmt.Errorf("%w: id is required", ErrInvalidInput)
	}
	name, err := normalizeDisplayName(req.DisplayName)
	if err != nil {
		return Mount{}, err
	}
	if req.Governance != domain.MountGovernanceNormal && req.Governance != domain.MountGovernanceRestricted {
		return Mount{}, fmt.Errorf("%w: governance must be normal or restricted", ErrInvalidInput)
	}
	if req.Governance == domain.MountGovernanceRestricted && !initial {
		return Mount{}, ErrRestrictedGovernance
	}
	if req.Mode != domain.MountModeReadOnly && req.Mode != domain.MountModeReadWrite {
		return Mount{}, fmt.Errorf("%w: mode must be read_only or read_write", ErrInvalidInput)
	}
	for index, grant := range req.Grants {
		if strings.TrimSpace(grant.AccountID) == "" || !grant.Permission.Valid() {
			return Mount{}, fmt.Errorf("%w: grants[%d] is invalid", ErrInvalidInput, index)
		}
		for prior := 0; prior < index; prior++ {
			if req.Grants[prior].AccountID == grant.AccountID {
				return Mount{}, fmt.Errorf("%w: duplicate grant account", ErrInvalidInput)
			}
		}
	}
	// Probe once before opening the transaction so invalid or unwritable paths do
	// not hold SQLite's writer lock. The identity and conflict scan are repeated
	// after taking that lock below; only the transaction-local result is stored.
	if _, err := s.validateRoot(ctx, req.RootPath, req.Mode, req.ID, initial); err != nil {
		return Mount{}, err
	}
	for _, grant := range req.Grants {
		if err := s.validateGrantAccount(ctx, grant.AccountID); err != nil {
			return Mount{}, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Mount{}, err
	}
	defer tx.Rollback()
	// database/sql does not expose SQLite's BEGIN IMMEDIATE mode on an existing
	// *sql.DB. This harmless write promotes the transaction to a writer before
	// the authoritative identity scan, which provides the same serialization
	// point while preserving *sql.Tx for the audit callback.
	if _, err := tx.ExecContext(ctx, `UPDATE system_state SET updated_at = updated_at WHERE key = 'initialized'`); err != nil {
		return Mount{}, err
	}
	identity, err := s.validateRootWith(ctx, tx, req.RootPath, req.Mode, req.ID, initial)
	if err != nil {
		return Mount{}, err
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		return Mount{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance,
                   mode, index_enabled, share_enabled, status, mount_identity_json)
VALUES (?, ?, ?, 'common', 'external', ?, ?, ?, 1, 'active', ?)
`, req.ID, name, identity.Path, req.Governance, req.Mode, boolInt(req.IndexEnabled), string(identityJSON))
	if err != nil {
		if isUniqueError(err) {
			if initial {
				return Mount{}, fmt.Errorf("%w: %v", ErrMountConflict, err)
			}
			return Mount{}, ErrMountUnavailable
		}
		return Mount{}, err
	}
	claims := []struct {
		claimType string
		claimKey  string
	}{
		{claimType: "canonical_path", claimKey: identity.Path},
		{claimType: "device_inode", claimKey: fmt.Sprintf("%d:%d", identity.Device, identity.Inode)},
	}
	for _, claim := range claims {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mount_identity_claims(mount_id, claim_type, claim_key) VALUES (?, ?, ?)`, req.ID, claim.claimType, claim.claimKey); err != nil {
			if isUniqueError(err) {
				if initial {
					return Mount{}, fmt.Errorf("%w: %v", ErrMountConflict, err)
				}
				return Mount{}, ErrMountUnavailable
			}
			return Mount{}, err
		}
	}
	for _, grant := range req.Grants {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mount_grants(mount_id, account_id, permission) VALUES (?, ?, ?)`, req.ID, grant.AccountID, grant.Permission); err != nil {
			return Mount{}, err
		}
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_create", TargetID: req.ID, Metadata: "{}"}); err != nil {
			return Mount{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Mount{}, err
	}
	return Mount{
		ID: req.ID, DisplayName: name, RootPath: identity.Path, Governance: req.Governance,
		Mode: req.Mode, IndexEnabled: req.IndexEnabled, ShareEnabled: true, Status: "active", GrantCount: len(req.Grants),
	}, nil
}

func (s *Service) UpdateMount(ctx context.Context, actorID, mountID string, req UpdateRequest, audit AuditWriter) (Mount, error) {
	initial, err := s.isInitialAdmin(ctx, actorID)
	if err != nil {
		return Mount{}, err
	}
	current, err := s.LoadMount(ctx, actorID, mountID)
	if err != nil {
		return Mount{}, err
	}
	name := current.DisplayName
	if req.DisplayName != nil {
		name, err = normalizeDisplayName(*req.DisplayName)
		if err != nil {
			return Mount{}, err
		}
	}
	mode := current.Mode
	if req.Mode != nil {
		if *req.Mode != domain.MountModeReadOnly && *req.Mode != domain.MountModeReadWrite {
			return Mount{}, fmt.Errorf("%w: mode must be read_only or read_write", ErrInvalidInput)
		}
		if *req.Mode == domain.MountModeReadWrite && current.Mode != domain.MountModeReadWrite {
			if err := probeWritable(current.RootPath); err != nil {
				return Mount{}, err
			}
		}
		mode = *req.Mode
	}
	indexEnabled := current.IndexEnabled
	if req.IndexEnabled != nil {
		indexEnabled = *req.IndexEnabled
	}
	shareEnabled := current.ShareEnabled
	if req.ShareEnabled != nil {
		shareEnabled = *req.ShareEnabled
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Mount{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
UPDATE mounts
SET display_name = ?, mode = ?, index_enabled = ?, share_enabled = ?, updated_at = ?
WHERE id = ? AND status <> 'deleted'
`, name, mode, boolInt(indexEnabled), boolInt(shareEnabled), time.Now().UTC().Format(time.RFC3339Nano), current.ID)
	if err != nil {
		if isUniqueError(err) {
			if !initial {
				return Mount{}, ErrMountUnavailable
			}
			return Mount{}, fmt.Errorf("%w: %v", ErrMountConflict, err)
		}
		return Mount{}, err
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_update", TargetID: current.ID, Metadata: "{}"}); err != nil {
			return Mount{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Mount{}, err
	}
	current.DisplayName, current.Mode = name, mode
	current.IndexEnabled, current.ShareEnabled = indexEnabled, shareEnabled
	return current, nil
}

func (s *Service) ListGrants(ctx context.Context, actorID, mountID string) ([]Grant, error) {
	if _, err := s.LoadMount(ctx, actorID, mountID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT mg.account_id, a.email, a.display_name, mg.permission
FROM mount_grants mg
JOIN accounts a ON a.id = mg.account_id
WHERE mg.mount_id = ?
ORDER BY lower(a.display_name), a.id
`, mountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Grant, 0)
	for rows.Next() {
		var item Grant
		if err := rows.Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.Permission); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) PutGrant(ctx context.Context, actorID, mountID, accountID string, permission domain.ContentPermission, audit AuditWriter) (Grant, error) {
	if !permission.Valid() {
		return Grant{}, fmt.Errorf("%w: permission must be viewer or editor", ErrInvalidInput)
	}
	if _, err := s.LoadMount(ctx, actorID, mountID); err != nil {
		return Grant{}, err
	}
	if err := s.validateGrantAccount(ctx, accountID); err != nil {
		return Grant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO mount_grants(mount_id, account_id, permission)
VALUES (?, ?, ?)
ON CONFLICT(mount_id, account_id) DO UPDATE SET permission = excluded.permission, updated_at = CURRENT_TIMESTAMP
`, mountID, accountID, permission); err != nil {
		return Grant{}, err
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_grant_set", TargetID: mountID, Metadata: fmt.Sprintf(`{"accountId":%q,"permission":%q}`, accountID, permission)}); err != nil {
			return Grant{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, err
	}
	var item Grant
	err = s.db.QueryRowContext(ctx, `SELECT mg.account_id, a.email, a.display_name, mg.permission FROM mount_grants mg JOIN accounts a ON a.id = mg.account_id WHERE mg.mount_id = ? AND mg.account_id = ?`, mountID, accountID).Scan(&item.AccountID, &item.Email, &item.DisplayName, &item.Permission)
	return item, err
}

func (s *Service) DeleteGrant(ctx context.Context, actorID, mountID, accountID string, audit AuditWriter) error {
	if _, err := s.LoadMount(ctx, actorID, mountID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM mount_grants WHERE mount_id = ? AND account_id = ?`, mountID, accountID); err != nil {
		return err
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_grant_remove", TargetID: mountID, Metadata: fmt.Sprintf(`{"accountId":%q}`, accountID)}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) ReverifyMount(ctx context.Context, actorID, mountID string, audit AuditWriter) (Mount, error) {
	current, err := s.LoadMount(ctx, actorID, mountID)
	if err != nil {
		return Mount{}, err
	}
	initial, err := s.isInitialAdmin(ctx, actorID)
	if err != nil {
		return Mount{}, err
	}
	identity, err := s.validateRoot(ctx, current.RootPath, current.Mode, current.ID, initial)
	if err != nil {
		return Mount{}, err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return Mount{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Mount{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE mounts SET status = 'active', mount_identity_json = ?, updated_at = ? WHERE id = ? AND status <> 'deleted'`, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), current.ID)
	if err != nil {
		return Mount{}, err
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_reverify", TargetID: current.ID, Metadata: "{}"}); err != nil {
			return Mount{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Mount{}, err
	}
	current.Status = "active"
	return current, nil
}

func (s *Service) DeleteMount(ctx context.Context, actorID, mountID, displayName string, audit AuditWriter) (Deletion, error) {
	current, err := s.LoadMount(ctx, actorID, mountID)
	if err != nil {
		return Deletion{}, err
	}
	if displayName != current.DisplayName {
		return Deletion{}, ErrConfirmationRequired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deletion{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM mount_grants WHERE mount_id = ?`, []any{current.ID}},
		{`DELETE FROM ai_token_boundaries WHERE mount_id = ?`, []any{current.ID}},
		{`UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ? WHERE mount_id = ?`, []any{now, now, current.ID}},
		{`UPDATE share_download_tickets SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?) WHERE mount_id = ? AND status IN ('issued', 'streaming')`, []any{now, current.ID}},
		{`UPDATE mcp_transfer_tickets SET status = 'canceled', closed_at = COALESCE(closed_at, ?) WHERE mount_id = ? AND status = 'active'`, []any{now, current.ID}},
		{`UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?) WHERE mount_id = ? AND status = 'active'`, []any{now, current.ID}},
		{`DELETE FROM file_operations WHERE source_mount_id = ? OR destination_mount_id = ?`, []any{current.ID, current.ID}},
		{`DELETE FROM catalog_entries WHERE mount_id = ?`, []any{current.ID}},
		{`DELETE FROM file_objects WHERE mount_id = ?`, []any{current.ID}},
		{`DELETE FROM mount_identity_claims WHERE mount_id = ?`, []any{current.ID}},
		{`DELETE FROM mount_claim_conflicts WHERE mount_id = ? OR conflicting_mount_id = ?`, []any{current.ID, current.ID}},
		{`UPDATE jobs SET status = 'canceled', updated_at = ?, completed_at = COALESCE(completed_at, ?) WHERE status IN ('queued', 'running', 'paused') AND (instr(payload_json, ?) > 0 OR instr(payload_json, ?) > 0)`, []any{now, now, `"mountId":"` + current.ID + `"`, `"mount_id":"` + current.ID + `"`}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return Deletion{}, err
		}
	}
	tombstone := deletedDisplayName(current.DisplayName, current.ID)
	result, err := tx.ExecContext(ctx, `UPDATE mounts SET status = 'deleted', display_name = ?, updated_at = ? WHERE id = ? AND status <> 'deleted'`, tombstone, now, current.ID)
	if err != nil {
		return Deletion{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Deletion{}, err
	}
	if affected != 1 {
		return Deletion{}, ErrNotFound
	}
	if audit != nil {
		if err := audit(ctx, tx, AuditEvent{Action: "mount_delete", TargetID: current.ID, Metadata: `{"deleteData":false,"dataDeleted":false}`}); err != nil {
			return Deletion{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Deletion{}, err
	}
	return Deletion{ID: current.ID, Deleted: true}, nil
}

func (s *Service) validateGrantAccount(ctx context.Context, accountID string) error {
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, strings.TrimSpace(accountID)).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: account was not found", ErrInvalidInput)
		}
		return err
	}
	if status != "active" {
		return fmt.Errorf("%w: account is not active", ErrInvalidInput)
	}
	return nil
}

func (s *Service) validateRoot(ctx context.Context, rawPath string, mode domain.MountMode, mountID string, initial bool) (mountid.Identity, error) {
	return s.validateRootWith(ctx, s.db, rawPath, mode, mountID, initial)
}

type identityQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) validateRootWith(ctx context.Context, queryer identityQueryer, rawPath string, mode domain.MountMode, mountID string, initial bool) (mountid.Identity, error) {
	root := filepath.Clean(strings.TrimSpace(rawPath))
	configured := filepath.Clean(strings.TrimSpace(s.externalRoot))
	if root == "." || configured == "." || root == string(filepath.Separator) || configured == string(filepath.Separator) || !filepath.IsAbs(root) || !filepath.IsAbs(configured) || !underRoot(root, configured) {
		return mountid.Identity{}, ErrRootNotAllowed
	}
	existing, err := s.loadIdentities(ctx, queryer, mountID)
	if err != nil {
		return mountid.Identity{}, err
	}
	identity, err := mountid.VerifyCandidateRoot(root, existing)
	if err != nil {
		if errors.Is(err, mountid.ErrMountConflict) {
			if initial {
				return mountid.Identity{}, fmt.Errorf("%w: %v", ErrMountConflict, err)
			}
			return mountid.Identity{}, ErrMountUnavailable
		}
		return mountid.Identity{}, fmt.Errorf("%w: %v", ErrIdentityUnverifiable, err)
	}
	if mode == domain.MountModeReadWrite {
		if err := probeWritable(identity.Path); err != nil {
			return mountid.Identity{}, err
		}
	}
	return identity, nil
}

func (s *Service) loadIdentities(ctx context.Context, queryer identityQueryer, excludeID string) ([]mountid.Identity, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT root_path, COALESCE(mount_identity_json, '')
FROM mounts
WHERE purpose = 'common' AND status <> 'deleted' AND id <> ?
`, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make([]mountid.Identity, 0)
	for rows.Next() {
		var root, encoded string
		if err := rows.Scan(&root, &encoded); err != nil {
			return nil, err
		}
		var identity mountid.Identity
		if strings.TrimSpace(encoded) != "" {
			if err := json.Unmarshal([]byte(encoded), &identity); err != nil {
				return nil, fmt.Errorf("%w: existing mount identity is invalid", ErrIdentityUnverifiable)
			}
		} else {
			captured, err := mountid.Capture(root)
			if err != nil {
				return nil, fmt.Errorf("%w: existing mount identity is unavailable", ErrIdentityUnverifiable)
			}
			identity = captured
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func normalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: displayName is required", ErrInvalidInput)
	}
	if utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("%w: displayName is too long", ErrInvalidInput)
	}
	for _, char := range value {
		if char == 0 || char < 0x20 || char == 0x7f {
			return "", fmt.Errorf("%w: displayName contains invalid characters", ErrInvalidInput)
		}
	}
	return value, nil
}

func probeWritable(root string) error {
	file, err := os.CreateTemp(root, ".omnora-write-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	return nil
}

func underRoot(candidate, root string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isUniqueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func deletedDisplayName(displayName, mountID string) string {
	suffix := "__deleted__" + mountID
	maxRunes := 128
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = "mount"
	}
	available := maxRunes - utf8.RuneCountInString(suffix)
	if available < 1 {
		runes := []rune(suffix)
		return string(runes[len(runes)-maxRunes:])
	}
	runes := []rune(base)
	if len(runes) > available {
		runes = runes[:available]
	}
	return string(runes) + suffix
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMount(row scanner) (Mount, error) {
	var item Mount
	var indexEnabled, shareEnabled int
	if err := row.Scan(
		&item.ID, &item.DisplayName, &item.RootPath, &item.Governance, &item.Mode,
		&indexEnabled, &shareEnabled, &item.Status, &item.GrantCount,
	); err != nil {
		return Mount{}, err
	}
	item.IndexEnabled = indexEnabled == 1
	item.ShareEnabled = shareEnabled == 1
	return item, nil
}
