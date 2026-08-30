package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"omnora/internal/audit"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/mountid"
)

type mountDiscoveryFunc func(managedRoot, externalRoot string) ([]mountid.MountInfo, error)

func discoverConfiguredMounts(managedRoot, externalRoot string) ([]mountid.MountInfo, error) {
	entries, err := mountid.ListMountPoints()
	if err != nil {
		return nil, err
	}
	entries = addManagedRootFallback(entries, managedRoot, runtime.GOOS != "linux")
	return selectConfiguredMounts(entries, managedRoot, externalRoot), nil
}

func addManagedRootFallback(entries []mountid.MountInfo, managedRoot string, enabled bool) []mountid.MountInfo {
	if !enabled || len(entries) != 0 {
		return entries
	}
	managedRoot = filepath.Clean(strings.TrimSpace(managedRoot))
	if managedRoot == "" || managedRoot == "." {
		return entries
	}
	info, err := os.Stat(managedRoot)
	if err != nil || !info.IsDir() {
		return entries
	}
	return append(entries, mountid.MountInfo{Available: true, Point: managedRoot})
}

func selectConfiguredMounts(entries []mountid.MountInfo, managedRoot, externalRoot string) []mountid.MountInfo {
	managedRoot = filepath.Clean(strings.TrimSpace(managedRoot))
	externalRoot = filepath.Clean(strings.TrimSpace(externalRoot))
	roots := uniqueSorted([]string{managedRoot, externalRoot})
	byPath := make(map[string]mountid.MountInfo)
	for _, entry := range entries {
		point := filepath.Clean(strings.TrimSpace(entry.Point))
		if !entry.Available || point == "" || point == "." || point == string(filepath.Separator) {
			continue
		}
		if !isUnderAnyRoot(point, roots) {
			continue
		}
		entry.Point = point
		byPath[point] = entry
	}

	if _, rootMounted := byPath[externalRoot]; rootMounted {
		for point := range byPath {
			if point != externalRoot && strings.HasPrefix(point, externalRoot+string(filepath.Separator)) {
				delete(byPath, externalRoot)
				break
			}
		}
	}

	candidates := make([]mountid.MountInfo, 0, len(byPath))
	for _, entry := range byPath {
		candidates = append(candidates, entry)
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftDepth := pathDepth(candidates[i].Point)
		rightDepth := pathDepth(candidates[j].Point)
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return candidates[i].Point < candidates[j].Point
	})

	selected := make([]mountid.MountInfo, 0, len(candidates))
	for _, candidate := range candidates {
		overlaps := false
		for _, current := range selected {
			if candidate.Point == current.Point || strings.HasPrefix(candidate.Point, current.Point+string(filepath.Separator)) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			selected = append(selected, candidate)
		}
	}
	return selected
}

func pathDepth(path string) int {
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return 0
	}
	return len(strings.Split(strings.Trim(cleaned, string(filepath.Separator)), string(filepath.Separator)))
}

func (s *Server) autoRegisterDockerMounts(ctx context.Context, accountID, spaceID string) (int, error) {
	if s == nil || s.sqlDB() == nil || s.mountDiscovery == nil {
		return 0, nil
	}
	roots := s.storageRoots()
	if len(roots) == 0 {
		return 0, nil
	}
	candidates, err := s.mountDiscovery(s.cfg.Storage.ManagedDir, s.cfg.Storage.PredeclaredMountRoot)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(spaceID) == "" {
		accountID, spaceID, err = s.firstAdminPersonalSpace(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
	}

	mountRegistrationMu.Lock()
	defer mountRegistrationMu.Unlock()

	existing, err := s.loadMountIdentitiesContext(ctx)
	if err != nil {
		return 0, err
	}
	existingPaths := mountIdentityPathSet(existing)

	registered := 0
	var registrationErrors []error
	for _, candidate := range candidates {
		rootPath := filepath.Clean(candidate.Point)
		if _, ok := existingPaths[rootPath]; ok {
			mountID, changed, grantErr := s.ensureAutoMountGrant(ctx, rootPath, accountID, spaceID)
			if grantErr != nil {
				registrationErrors = append(registrationErrors, fmt.Errorf("grant %s: %w", rootPath, grantErr))
				continue
			}
			if changed {
				metadata, _ := audit.MetadataFromMap(map[string]any{"automatic": true, "rootPath": rootPath})
				if err := audit.NewRecorder(s.sqlDB()).Record(ctx, audit.Event{
					ActorAccountID: accountID,
					Action:         "mount_auto_grant",
					TargetType:     "mount",
					TargetID:       mountID,
					MetadataJSON:   metadata,
				}); err != nil {
					slog.Warn("record automatic mount grant audit", "mount_id", mountID, "error", err)
				}
			}
			continue
		}
		identity, err := mountid.VerifyCandidateRoot(rootPath, existing)
		if err != nil {
			registrationErrors = append(registrationErrors, fmt.Errorf("%s: %w", rootPath, err))
			continue
		}
		kind, err := s.inferMountKind(identity.Path)
		if err != nil {
			registrationErrors = append(registrationErrors, fmt.Errorf("%s: %w", rootPath, err))
			continue
		}

		mode := domain.MountModeReadWrite
		if candidate.ReadOnly || probeMountWritable(identity.Path) != nil {
			mode = domain.MountModeReadOnly
		}
		displayName, err := s.availableAutoMountName(ctx, spaceID, autoMountBaseName(identity.Path, kind, s.cfg.Storage.ManagedDir, s.cfg.Storage.PredeclaredMountRoot))
		if err != nil {
			registrationErrors = append(registrationErrors, err)
			continue
		}
		identityJSON, err := json.Marshal(identity)
		if err != nil {
			registrationErrors = append(registrationErrors, err)
			continue
		}

		mountID := "mnt_" + httpx.NewRequestID()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		tx, err := s.sqlDB().BeginTx(ctx, nil)
		if err != nil {
			registrationErrors = append(registrationErrors, err)
			continue
		}
		var duplicate int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM mounts WHERE root_path = ? AND status <> 'deleted'`, identity.Path).Scan(&duplicate)
		if err == nil && duplicate == 0 {
			_, err = tx.ExecContext(ctx, `
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, allow_public_shares, status, mount_identity_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 0, 0, 'active', ?, ?, ?)
`, mountID, spaceID, displayName, identity.Path, kind, mode, string(identityJSON), now, now)
		}
		if err == nil && duplicate == 0 {
			_, err = tx.ExecContext(ctx, `
INSERT INTO mount_account_grants(mount_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, 'manager', ?, ?)
`, mountID, accountID, now, now)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			_ = tx.Rollback()
		}
		if err != nil {
			if isActiveMountRootConflict(err) {
				current, loadErr := s.loadMountIdentitiesContext(ctx)
				if loadErr != nil {
					registrationErrors = append(registrationErrors, loadErr)
				} else {
					existing = current
					existingPaths = mountIdentityPathSet(current)
				}
				continue
			}
			registrationErrors = append(registrationErrors, fmt.Errorf("register %s: %w", rootPath, err))
			continue
		}
		if duplicate != 0 {
			continue
		}

		existing = append(existing, identity)
		existingPaths[rootPath] = struct{}{}
		registered++
		metadata, _ := audit.MetadataFromMap(map[string]any{"automatic": true, "kind": kind, "mode": mode})
		if err := audit.NewRecorder(s.sqlDB()).Record(ctx, audit.Event{
			ActorAccountID: accountID,
			Action:         "mount_auto_register",
			TargetType:     "mount",
			TargetID:       mountID,
			MetadataJSON:   metadata,
		}); err != nil {
			slog.Warn("record automatic mount registration audit", "mount_id", mountID, "error", err)
		}
	}
	return registered, errors.Join(registrationErrors...)
}

func (s *Server) ensureAutoMountGrant(ctx context.Context, rootPath, accountID, spaceID string) (string, bool, error) {
	var mountID string
	var permission sql.NullString
	err := s.sqlDB().QueryRowContext(ctx, `
SELECT m.id, g.permission
FROM mounts m
LEFT JOIN mount_account_grants g ON g.mount_id = m.id AND g.account_id = ?
WHERE m.root_path = ? AND m.space_id = ? AND m.status <> 'deleted'
LIMIT 1
`, accountID, rootPath, spaceID).Scan(&mountID, &permission)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if permission.Valid && permission.String == string(domain.SpacePermissionManager) {
		return mountID, false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.sqlDB().ExecContext(ctx, `
INSERT INTO mount_account_grants(mount_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, 'manager', ?, ?)
ON CONFLICT(mount_id, account_id) DO UPDATE SET permission = 'manager', updated_at = excluded.updated_at
`, mountID, accountID, now, now)
	if err != nil {
		return "", false, err
	}
	return mountID, true, nil
}

func (s *Server) firstAdminPersonalSpace(ctx context.Context) (string, string, error) {
	var accountID, spaceID string
	err := s.sqlDB().QueryRowContext(ctx, `
SELECT a.id, sp.id
FROM accounts a
JOIN spaces sp ON sp.owner_account_id = a.id AND sp.kind = 'personal' AND sp.status = 'active'
JOIN space_members sm ON sm.space_id = sp.id AND sm.account_id = a.id AND sm.permission = 'manager'
WHERE a.role = 'admin' AND a.status = 'active'
ORDER BY a.created_at, a.id, sp.created_at, sp.id
LIMIT 1
`).Scan(&accountID, &spaceID)
	return accountID, spaceID, err
}

func (s *Server) availableAutoMountName(ctx context.Context, spaceID, base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "Storage"
	}
	base = truncateRunes(base, 128)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			ending := fmt.Sprintf(" (%d)", suffix)
			candidate = truncateRunes(base, 128-len([]rune(ending))) + ending
		}
		var count int
		if err := s.sqlDB().QueryRowContext(ctx, `SELECT COUNT(1) FROM mounts WHERE space_id = ? AND display_name = ?`, spaceID, candidate).Scan(&count); err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}
}

func autoMountBaseName(rootPath, kind, managedRoot, externalRoot string) string {
	rootPath = filepath.Clean(rootPath)
	if kind == "managed" && rootPath == filepath.Clean(managedRoot) {
		return "Managed Storage"
	}
	if kind == "external" && rootPath == filepath.Clean(externalRoot) {
		return "External Storage"
	}
	return filepath.Base(rootPath)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func mountIdentityPathSet(identities []mountid.Identity) map[string]struct{} {
	paths := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		paths[filepath.Clean(identity.Path)] = struct{}{}
	}
	return paths
}

func isActiveMountRootConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: mounts.root_path")
}
