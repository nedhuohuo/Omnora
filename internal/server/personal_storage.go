package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"omnora/internal/domain"
	"omnora/internal/mountid"
)

const personalMountName = "Personal files"

// ensurePersonalStorage provisions the system-managed mount for an account's
// personal space. It is idempotent so accounts created before this feature are
// provisioned on their next authenticated space listing as well.
func (s *Server) ensurePersonalStorage(ctx context.Context, accountID string) error {
	root := filepath.Clean(strings.TrimSpace(s.cfg.Storage.PersonalStorageDir))
	if root == "" || root == "." || root == string(filepath.Separator) {
		return fmt.Errorf("personal storage root is not configured safely")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || filepath.Base(accountID) != accountID {
		return fmt.Errorf("personal storage account identifier is invalid")
	}
	var spaceID string
	err := s.sqlDB().QueryRowContext(ctx, `
SELECT id FROM spaces
WHERE kind = 'personal' AND owner_account_id = ? AND status = 'active'
`, accountID).Scan(&spaceID)
	if err != nil {
		return err
	}

	path := filepath.Join(root, accountID)
	if path == root || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("personal storage path escapes configured root")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create personal storage: %w", err)
	}
	identity, err := mountid.Capture(path)
	if err != nil {
		return fmt.Errorf("capture personal storage identity: %w", err)
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	_, err = s.sqlDB().ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, ?, 'managed', ?, 0, 'active', ?)
ON CONFLICT(space_id, display_name) DO NOTHING
`, "personal-mount-"+accountID, spaceID, personalMountName, path, domain.MountModeReadWrite, string(identityJSON))
	if err != nil {
		return err
	}
	return nil
}

func isMissingPersonalSpace(err error) bool { return err == sql.ErrNoRows }
