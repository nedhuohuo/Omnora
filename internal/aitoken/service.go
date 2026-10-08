package aitoken

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"omnora/internal/domain"
)

const timestampLayout = time.RFC3339Nano

type Service struct {
	db  *sql.DB
	now func() time.Time
}

type Option func(*Service)

// TxAuditWriter lets callers commit a token mutation and its success audit
// event atomically without coupling this service to the audit package.
type TxAuditWriter func(context.Context, *sql.Tx) error

type TokenAuditWriter func(context.Context, *sql.Tx, Token) error

func WithClock(clock func() time.Time) Option {
	return func(s *Service) {
		if clock != nil {
			s.now = clock
		}
	}
}

func NewService(db *sql.DB, opts ...Option) *Service {
	s := &Service{
		db:  db,
		now: time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func InstallSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, SchemaSQL)
	return err
}

func AllowlistedScopes() []Scope {
	return []Scope{
		ScopeSpacesRead,
		ScopeFilesList,
		ScopeFilesMetadata,
		ScopeFilesText,
		ScopeFilesDownloadTicket,
		ScopeSearchRead,
		ScopeUploadsCreate,
		ScopeFilesWrite,
		ScopeFilesTrash,
		ScopeTrashRead,
		ScopeFilesRestore,
		ScopeFilesPurge,
		ScopeSharesRead,
		ScopeSharesCreate,
		ScopeSharesRevoke,
	}
}

func ValidateScopes(scopes []Scope) ([]Scope, error) {
	if len(scopes) == 0 {
		return nil, ErrInvalidScope
	}
	allowed := make(map[Scope]bool, len(AllowlistedScopes()))
	for _, scope := range AllowlistedScopes() {
		allowed[scope] = true
	}
	seen := make(map[Scope]bool, len(scopes))
	validated := make([]Scope, 0, len(scopes))
	for _, scope := range scopes {
		if !allowed[scope] || seen[scope] {
			return nil, ErrInvalidScope
		}
		seen[scope] = true
		validated = append(validated, scope)
	}
	return validated, nil
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (IssuedToken, error) {
	return s.CreateSecure(ctx, req, nil)
}

func (s *Service) CreateSecure(ctx context.Context, req CreateRequest, auditWriter TokenAuditWriter) (IssuedToken, error) {
	if s == nil || s.db == nil {
		return IssuedToken{}, ErrInvalidInput
	}
	accountID := strings.TrimSpace(req.AccountID)
	name := strings.TrimSpace(req.Name)
	now := s.now().UTC().Round(0)
	// A zero ExpiresAt means the token never expires; any other value must be
	// strictly in the future.
	if accountID == "" || name == "" || (!req.ExpiresAt.IsZero() && !now.Before(req.ExpiresAt)) {
		return IssuedToken{}, ErrInvalidInput
	}
	scopes, err := ValidateScopes(req.Scopes)
	if err != nil {
		return IssuedToken{}, err
	}
	boundaries, err := normalizeBoundaries(req.Boundaries)
	if err != nil {
		return IssuedToken{}, err
	}

	var active int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM accounts WHERE id = ? AND status = 'active'", accountID).Scan(&active); err != nil {
		return IssuedToken{}, err
	}
	if active != 1 {
		return IssuedToken{}, ErrInvalidInput
	}
	var credentialGeneration int64
	if err := s.db.QueryRowContext(ctx, `
SELECT CAST(value AS INTEGER)
FROM system_state
WHERE key = 'credential_generation'
`).Scan(&credentialGeneration); err != nil {
		return IssuedToken{}, err
	}

	publicID, err := newPrefixedID("ait")
	if err != nil {
		return IssuedToken{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return IssuedToken{}, err
	}
	tokenID, err := newPrefixedID("aitok")
	if err != nil {
		return IssuedToken{}, err
	}
	scopeJSON, err := marshalScopes(scopes)
	if err != nil {
		return IssuedToken{}, err
	}

	token := Token{
		ID:                   tokenID,
		PublicID:             publicID,
		SecretHash:           hashSecret(secret),
		AccountID:            accountID,
		Name:                 name,
		Scopes:               scopes,
		AuthorizationModel:   AuthorizationModelLegacyBoundary,
		Boundaries:           boundaries,
		CredentialGeneration: credentialGeneration,
		CreatedAt:            now,
		ExpiresAt:            req.ExpiresAt.UTC().Round(0),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedToken{}, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, authorization_model, credential_generation, created_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, token.ID, token.PublicID, token.SecretHash, token.AccountID, token.Name, scopeJSON, token.AuthorizationModel, token.CredentialGeneration, formatTime(token.CreatedAt), formatTokenExpiry(token.ExpiresAt), formatTime(now))
	if err != nil {
		return IssuedToken{}, err
	}
	for _, boundary := range boundaries {
		_, err = tx.ExecContext(ctx, `
INSERT INTO ai_token_boundaries(token_id, space_id, mount_id, relative_path)
VALUES (?, ?, ?, ?)
`, token.ID, boundary.SpaceID, boundary.MountID, boundary.RelativePath)
		if err != nil {
			return IssuedToken{}, err
		}
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, token); err != nil {
			return IssuedToken{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IssuedToken{}, err
	}

	return IssuedToken{
		Token:       token,
		Secret:      secret,
		BearerToken: publicID + "." + secret,
	}, nil
}

func (s *Service) CreateSpaceGrant(ctx context.Context, req CreateSpaceGrantRequest) (IssuedToken, error) {
	return s.CreateSpaceGrantSecure(ctx, req, nil)
}

// VerifySpaceGrantDirectories resolves and fingerprints denylisted directories
// on the server. API callers submit paths only; the returned grants contain
// the server-captured identity fingerprints required by CreateSpaceGrantSecure.
func (s *Service) VerifySpaceGrantDirectories(ctx context.Context, grants []SpaceGrant) ([]SpaceGrant, error) {
	normalized, err := normalizeSpaceGrants(grants)
	if err != nil {
		return nil, err
	}
	verified := make([]SpaceGrant, len(normalized))
	roots := make(map[spaceMountKey]string)
	for index, grant := range normalized {
		verified[index] = grant
		verified[index].FolderDenylist = append([]FolderDeny(nil), grant.FolderDenylist...)
		for denyIndex, deny := range grant.FolderDenylist {
			key := spaceMountKey{spaceID: grant.SpaceID, mountID: deny.MountID}
			rootPath, exists := roots[key]
			if !exists {
				if err := s.db.QueryRowContext(ctx, `
SELECT root_path FROM mounts WHERE id = ? AND space_id = ? AND status = 'active'
				`, deny.MountID, grant.SpaceID).Scan(&rootPath); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return nil, ErrInvalidInput
					}
					return nil, err
				}
				roots[key] = strings.TrimSpace(rootPath)
			}
			fingerprint, err := captureDenyDirectoryFingerprint(rootPath, deny.RelativePath)
			if err != nil {
				return nil, ErrInvalidInput
			}
			verified[index].FolderDenylist[denyIndex].IdentityFingerprint = fingerprint
		}
	}
	return verified, nil
}

func (s *Service) CreateSpaceGrantSecure(ctx context.Context, req CreateSpaceGrantRequest, auditWriter TokenAuditWriter) (IssuedToken, error) {
	if s == nil || s.db == nil {
		return IssuedToken{}, ErrInvalidInput
	}
	accountID := strings.TrimSpace(req.AccountID)
	name := strings.TrimSpace(req.Name)
	now := s.now().UTC().Round(0)
	if accountID == "" || name == "" || (!req.ExpiresAt.IsZero() && !now.Before(req.ExpiresAt)) {
		return IssuedToken{}, ErrInvalidInput
	}
	scopes, err := ValidateScopes(req.Scopes)
	if err != nil {
		return IssuedToken{}, err
	}
	grants, err := normalizeSpaceGrants(req.SpaceGrants)
	if err != nil {
		return IssuedToken{}, err
	}
	grants, verifiedMountRoots, err := s.verifySpaceGrantDirectories(ctx, grants)
	if err != nil {
		return IssuedToken{}, err
	}
	scopeJSON, err := marshalScopes(scopes)
	if err != nil {
		return IssuedToken{}, err
	}
	publicID, err := newPrefixedID("ait")
	if err != nil {
		return IssuedToken{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return IssuedToken{}, err
	}
	tokenID, err := newPrefixedID("aitok")
	if err != nil {
		return IssuedToken{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedToken{}, err
	}
	defer tx.Rollback()
	var credentialGeneration int64
	if err := tx.QueryRowContext(ctx, `
SELECT CAST(ss.value AS INTEGER)
FROM accounts a
CROSS JOIN system_state ss
WHERE a.id = ? AND a.status = 'active' AND ss.key = 'credential_generation'
`, accountID).Scan(&credentialGeneration); errors.Is(err, sql.ErrNoRows) {
		return IssuedToken{}, ErrInvalidInput
	} else if err != nil {
		return IssuedToken{}, err
	}
	grants, err = validateSpaceGrantAccess(ctx, tx, accountID, grants, verifiedMountRoots)
	if err != nil {
		return IssuedToken{}, err
	}
	token := Token{
		ID:                   tokenID,
		PublicID:             publicID,
		SecretHash:           hashSecret(secret),
		AccountID:            accountID,
		Name:                 name,
		Scopes:               scopes,
		AuthorizationModel:   AuthorizationModelSpaceGrant,
		SpaceGrants:          grants,
		CredentialGeneration: credentialGeneration,
		CreatedAt:            now,
		ExpiresAt:            req.ExpiresAt.UTC().Round(0),
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, authorization_model, credential_generation, created_at, expires_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, token.ID, token.PublicID, token.SecretHash, token.AccountID, token.Name, scopeJSON, token.AuthorizationModel, token.CredentialGeneration, formatTime(token.CreatedAt), formatTokenExpiry(token.ExpiresAt), formatTime(now)); err != nil {
		return IssuedToken{}, err
	}
	if err := insertSpaceGrants(ctx, tx, token.ID, grants, now); err != nil {
		return IssuedToken{}, err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, token); err != nil {
			return IssuedToken{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token, Secret: secret, BearerToken: publicID + "." + secret}, nil
}

func (s *Service) UpdateSecure(ctx context.Context, req UpdateRequest, auditWriter TokenAuditWriter) (Token, error) {
	if s == nil || s.db == nil {
		return Token{}, ErrInvalidInput
	}
	accountID := strings.TrimSpace(req.AccountID)
	tokenID := strings.TrimSpace(req.TokenID)
	name := strings.TrimSpace(req.Name)
	now := s.now().UTC().Round(0)
	if accountID == "" || tokenID == "" || name == "" || (!req.ExpiresAt.IsZero() && !now.Before(req.ExpiresAt)) {
		return Token{}, ErrInvalidInput
	}
	scopes, err := ValidateScopes(req.Scopes)
	if err != nil {
		return Token{}, err
	}
	grants, err := normalizeSpaceGrants(req.SpaceGrants)
	if err != nil {
		return Token{}, err
	}
	grants, verifiedMountRoots, err := s.verifySpaceGrantDirectories(ctx, grants)
	if err != nil {
		return Token{}, err
	}
	scopeJSON, err := marshalScopes(scopes)
	if err != nil {
		return Token{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback()
	var token Token
	var createdAt string
	var lastUsedAt sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT t.public_id, t.secret_hash, t.credential_generation, t.created_at, t.last_used_at
FROM ai_tokens t
JOIN accounts a ON a.id = t.account_id
WHERE t.id = ? AND t.account_id = ? AND t.revoked_at IS NULL AND a.status = 'active'
  AND t.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, tokenID, accountID).Scan(&token.PublicID, &token.SecretHash, &token.CredentialGeneration, &createdAt, &lastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrInvalidInput
	}
	if err != nil {
		return Token{}, err
	}
	grants, err = validateSpaceGrantAccess(ctx, tx, accountID, grants, verifiedMountRoots)
	if err != nil {
		return Token{}, err
	}
	token.ID = tokenID
	token.AccountID = accountID
	token.Name = name
	token.Scopes = scopes
	token.AuthorizationModel = AuthorizationModelSpaceGrant
	token.SpaceGrants = grants
	token.ExpiresAt = req.ExpiresAt.UTC().Round(0)
	token.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Token{}, err
	}
	if lastUsedAt.Valid {
		token.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return Token{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE ai_tokens
SET name = ?, scopes = ?, authorization_model = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND account_id = ? AND revoked_at IS NULL
`, token.Name, scopeJSON, token.AuthorizationModel, formatTokenExpiry(token.ExpiresAt), formatTime(now), token.ID, token.AccountID)
	if err != nil {
		return Token{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Token{}, err
	} else if affected != 1 {
		return Token{}, ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_token_folder_denies WHERE token_id = ?`, token.ID); err != nil {
		return Token{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_token_space_grants WHERE token_id = ?`, token.ID); err != nil {
		return Token{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_token_boundaries WHERE token_id = ?`, token.ID); err != nil {
		return Token{}, err
	}
	if err := insertSpaceGrants(ctx, tx, token.ID, grants, now); err != nil {
		return Token{}, err
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx, token); err != nil {
			return Token{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Token{}, err
	}
	return token, nil
}

type spaceMountKey struct {
	spaceID string
	mountID string
}

func (s *Service) verifySpaceGrantDirectories(ctx context.Context, grants []SpaceGrant) ([]SpaceGrant, map[spaceMountKey]string, error) {
	verified := make([]SpaceGrant, len(grants))
	verifiedMountRoots := make(map[spaceMountKey]string)
	for index, grant := range grants {
		verified[index] = grant
		verified[index].FolderDenylist = append([]FolderDeny(nil), grant.FolderDenylist...)
		for denyIndex, deny := range grant.FolderDenylist {
			key := spaceMountKey{spaceID: grant.SpaceID, mountID: deny.MountID}
			rootPath, exists := verifiedMountRoots[key]
			if !exists {
				err := s.db.QueryRowContext(ctx, `
SELECT root_path
FROM mounts
WHERE id = ? AND space_id = ? AND status = 'active'
				`, deny.MountID, grant.SpaceID).Scan(&rootPath)
				if errors.Is(err, sql.ErrNoRows) {
					return nil, nil, ErrInvalidInput
				}
				if err != nil {
					return nil, nil, err
				}
				rootPath = strings.TrimSpace(rootPath)
				verifiedMountRoots[key] = rootPath
			}
			fingerprint, err := captureDenyDirectoryFingerprint(rootPath, deny.RelativePath)
			if err != nil || strings.TrimSpace(deny.IdentityFingerprint) == "" || strings.TrimSpace(deny.IdentityFingerprint) != fingerprint {
				return nil, nil, ErrInvalidInput
			}
			verified[index].FolderDenylist[denyIndex].IdentityFingerprint = fingerprint
		}
	}
	return verified, verifiedMountRoots, nil
}

func validateSpaceGrantAccess(ctx context.Context, tx *sql.Tx, accountID string, grants []SpaceGrant, verifiedMountRoots map[spaceMountKey]string) ([]SpaceGrant, error) {
	validated := make([]SpaceGrant, len(grants))
	validatedMounts := make(map[spaceMountKey]struct{}, len(verifiedMountRoots))
	for index, grant := range grants {
		validated[index] = grant
		validated[index].FolderDenylist = append([]FolderDeny(nil), grant.FolderDenylist...)
		var accountPermission domain.SpacePermission
		err := tx.QueryRowContext(ctx, `
SELECT sm.permission
FROM space_members sm
JOIN spaces sp ON sp.id = sm.space_id
WHERE sm.account_id = ? AND sm.space_id = ? AND sp.status = 'active'
	`, accountID, grant.SpaceID).Scan(&accountPermission)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidInput
		}
		if err != nil {
			return nil, err
		}
		if !grantPermissionAllows(accountPermission, grant.Permission) {
			return nil, ErrInvalidInput
		}
		for _, deny := range grant.FolderDenylist {
			key := spaceMountKey{spaceID: grant.SpaceID, mountID: deny.MountID}
			if _, alreadyValidated := validatedMounts[key]; alreadyValidated {
				continue
			}
			var rootPath string
			err := tx.QueryRowContext(ctx, `
SELECT root_path
FROM mounts
WHERE id = ? AND space_id = ? AND status = 'active'
			`, deny.MountID, grant.SpaceID).Scan(&rootPath)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrInvalidInput
			}
			if err != nil {
				return nil, err
			}
			verifiedRoot, exists := verifiedMountRoots[key]
			if !exists || strings.TrimSpace(rootPath) != verifiedRoot {
				return nil, ErrInvalidInput
			}
			validatedMounts[key] = struct{}{}
		}
	}
	return validated, nil
}

func captureDenyDirectoryFingerprint(rootPath, relativePath string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(rootPath))
	if root == "." || !filepath.IsAbs(root) {
		return "", ErrInvalidInput
	}
	relative := filepath.FromSlash(strings.TrimSpace(relativePath))
	if relative == "" || relative == "." || filepath.IsAbs(relative) {
		return "", ErrInvalidInput
	}
	target := filepath.Clean(filepath.Join(root, relative))
	within, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(within) || within == ".." || strings.HasPrefix(within, ".."+string(os.PathSeparator)) {
		return "", ErrInvalidInput
	}
	initialInfo, err := lstatDenyDirectoryPath(root, filepath.ToSlash(within))
	if err != nil {
		return "", err
	}
	initialFingerprint, err := fingerprintFromFileInfo(initialInfo)
	if err != nil {
		return "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	canonicalRelative, err := filepath.Rel(canonicalRoot, canonicalTarget)
	if err != nil || filepath.IsAbs(canonicalRelative) || canonicalRelative == ".." || strings.HasPrefix(canonicalRelative, ".."+string(os.PathSeparator)) {
		return "", ErrInvalidInput
	}
	expectedTarget := filepath.Clean(filepath.Join(canonicalRoot, filepath.FromSlash(within)))
	if canonicalTarget != expectedTarget {
		return "", ErrInvalidInput
	}
	finalInfo, err := lstatDenyDirectoryPath(root, filepath.ToSlash(within))
	if err != nil {
		return "", err
	}
	finalFingerprint, err := fingerprintFromFileInfo(finalInfo)
	if err != nil || finalFingerprint != initialFingerprint {
		return "", ErrInvalidInput
	}
	return finalFingerprint, nil
}

func fingerprintFromFileInfo(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Dev == 0 || stat.Ino == 0 {
		return "", ErrInvalidInput
	}
	return fmt.Sprintf("dev:%d:ino:%d", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func lstatDenyDirectoryPath(root, relative string) (os.FileInfo, error) {
	current := root
	info, err := os.Lstat(current)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, ErrInvalidInput
	}
	for _, component := range strings.Split(filepath.FromSlash(relative), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, ErrInvalidInput
		}
	}
	return info, nil
}

func grantPermissionAllows(actual, requested domain.SpacePermission) bool {
	switch requested {
	case domain.SpacePermissionViewer:
		return actual == domain.SpacePermissionViewer || actual == domain.SpacePermissionEditor || actual == domain.SpacePermissionManager
	case domain.SpacePermissionEditor:
		return actual == domain.SpacePermissionEditor || actual == domain.SpacePermissionManager
	default:
		return false
	}
}

func insertSpaceGrants(ctx context.Context, tx *sql.Tx, tokenID string, grants []SpaceGrant, now time.Time) error {
	for _, grant := range grants {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO ai_token_space_grants(token_id, space_id, permission, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
`, tokenID, grant.SpaceID, grant.Permission, formatTime(now), formatTime(now)); err != nil {
			return err
		}
		for _, deny := range grant.FolderDenylist {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO ai_token_folder_denies(token_id, space_id, mount_id, relative_path, identity_fingerprint, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, tokenID, grant.SpaceID, deny.MountID, deny.RelativePath, deny.IdentityFingerprint, deny.Status, formatTime(now), formatTime(now)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) VerifyBearer(ctx context.Context, bearer string) (Principal, error) {
	publicID, secret, err := parseBearer(bearer)
	if err != nil {
		return Principal{}, err
	}
	token, err := s.loadToken(ctx, publicID)
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC().Round(0)
	if token.RevokedAt.Valid || (!token.ExpiresAt.IsZero() && !now.Before(token.ExpiresAt)) || !secretMatches(secret, token.SecretHash) {
		return Principal{}, ErrInvalidToken
	}

	result, err := s.db.ExecContext(ctx, `
UPDATE ai_tokens
SET last_used_at = ?, updated_at = ?
WHERE id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), token.ID)
	if err != nil {
		return Principal{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Principal{}, err
	}
	if rows != 1 {
		return Principal{}, ErrInvalidToken
	}

	return Principal{
		AccountID:          token.AccountID,
		TokenID:            token.ID,
		PublicID:           token.PublicID,
		Scopes:             token.Scopes,
		AuthorizationModel: token.AuthorizationModel,
		Boundaries:         token.Boundaries,
		SpaceGrants:        token.SpaceGrants,
		ExpiresAt:          token.ExpiresAt,
		LastUsedAt:         now,
	}, nil
}

// RefreshPrincipal reloads a token by its internal ID for a real-time
// authorization check. It deliberately does not advance last_used_at; callers
// use it to revalidate an already-authenticated transfer operation.
func (s *Service) RefreshPrincipal(ctx context.Context, tokenID string) (Principal, error) {
	if s == nil || s.db == nil || strings.TrimSpace(tokenID) == "" {
		return Principal{}, ErrInvalidToken
	}
	token, err := s.loadTokenByID(ctx, strings.TrimSpace(tokenID))
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC().Round(0)
	if token.RevokedAt.Valid || (!token.ExpiresAt.IsZero() && !now.Before(token.ExpiresAt)) {
		return Principal{}, ErrInvalidToken
	}
	return Principal{
		AccountID:          token.AccountID,
		TokenID:            token.ID,
		PublicID:           token.PublicID,
		Scopes:             token.Scopes,
		AuthorizationModel: token.AuthorizationModel,
		Boundaries:         token.Boundaries,
		SpaceGrants:        token.SpaceGrants,
		ExpiresAt:          token.ExpiresAt,
		LastUsedAt:         token.LastUsedAt,
	}, nil
}

func (s *Service) Revoke(ctx context.Context, tokenID string) error {
	return s.RevokeSecure(ctx, tokenID, nil)
}

func (s *Service) RevokeSecure(ctx context.Context, tokenID string, auditWriter TxAuditWriter) error {
	if s == nil || s.db == nil || strings.TrimSpace(tokenID) == "" {
		return ErrInvalidInput
	}
	now := s.now().UTC().Round(0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE ai_tokens
SET revoked_at = ?, updated_at = ?
WHERE id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), strings.TrimSpace(tokenID))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrInvalidInput
	}
	if auditWriter != nil {
		if err := auditWriter(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) loadToken(ctx context.Context, publicID string) (loadedToken, error) {
	if s == nil || s.db == nil {
		return loadedToken{}, ErrInvalidToken
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return loadedToken{}, err
	}
	defer tx.Rollback()
	token, err := s.loadTokenTx(ctx, tx, publicID)
	if err != nil {
		return loadedToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadTokenTx(ctx context.Context, tx *sql.Tx, publicID string) (loadedToken, error) {
	var token loadedToken
	var scopesJSON, createdAt, expiresAt string
	var lastUsedAt sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT t.id, t.public_id, t.secret_hash, t.account_id, t.name, t.scopes, t.authorization_model,
		       t.credential_generation, t.created_at, t.expires_at, t.last_used_at, t.revoked_at
FROM ai_tokens t
JOIN accounts a ON a.id = t.account_id
WHERE t.public_id = ?
  AND a.status = 'active'
  AND t.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, publicID).Scan(
		&token.ID,
		&token.PublicID,
		&token.SecretHash,
		&token.AccountID,
		&token.Name,
		&scopesJSON,
		&token.AuthorizationModel,
		&token.CredentialGeneration,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
		&token.RevokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return loadedToken{}, ErrInvalidToken
	}
	if err != nil {
		return loadedToken{}, err
	}

	token.Scopes, err = unmarshalScopes(scopesJSON)
	if err != nil {
		return loadedToken{}, ErrInvalidToken
	}
	token.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return loadedToken{}, err
	}
	// An empty stored value means the token never expires.
	if expiresAt != "" {
		token.ExpiresAt, err = parseTime(expiresAt)
		if err != nil {
			return loadedToken{}, err
		}
	}
	if lastUsedAt.Valid {
		token.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return loadedToken{}, err
		}
	}
	if err := s.loadAuthorizationTx(ctx, tx, &token); err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadTokenByID(ctx context.Context, tokenID string) (loadedToken, error) {
	if s == nil || s.db == nil {
		return loadedToken{}, ErrInvalidToken
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return loadedToken{}, err
	}
	defer tx.Rollback()
	token, err := s.loadTokenByIDTx(ctx, tx, tokenID)
	if err != nil {
		return loadedToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadTokenByIDTx(ctx context.Context, tx *sql.Tx, tokenID string) (loadedToken, error) {
	var token loadedToken
	var scopesJSON, createdAt, expiresAt string
	var lastUsedAt sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT t.id, t.public_id, t.secret_hash, t.account_id, t.name, t.scopes, t.authorization_model,
		       t.credential_generation, t.created_at, t.expires_at, t.last_used_at, t.revoked_at
FROM ai_tokens t
JOIN accounts a ON a.id = t.account_id
WHERE t.id = ?
  AND a.status = 'active'
  AND t.credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER)
`, tokenID).Scan(
		&token.ID,
		&token.PublicID,
		&token.SecretHash,
		&token.AccountID,
		&token.Name,
		&scopesJSON,
		&token.AuthorizationModel,
		&token.CredentialGeneration,
		&createdAt,
		&expiresAt,
		&lastUsedAt,
		&token.RevokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return loadedToken{}, ErrInvalidToken
	}
	if err != nil {
		return loadedToken{}, err
	}
	token.Scopes, err = unmarshalScopes(scopesJSON)
	if err != nil {
		return loadedToken{}, ErrInvalidToken
	}
	token.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return loadedToken{}, err
	}
	// An empty stored value means the token never expires.
	if expiresAt != "" {
		token.ExpiresAt, err = parseTime(expiresAt)
		if err != nil {
			return loadedToken{}, err
		}
	}
	if lastUsedAt.Valid {
		token.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return loadedToken{}, err
		}
	}
	if err := s.loadAuthorizationTx(ctx, tx, &token); err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadBoundariesTx(ctx context.Context, tx *sql.Tx, tokenID string) ([]DirectoryBoundary, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT space_id, mount_id, relative_path
FROM ai_token_boundaries
WHERE token_id = ?
ORDER BY space_id, mount_id, relative_path
`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var boundaries []DirectoryBoundary
	for rows.Next() {
		var boundary DirectoryBoundary
		if err := rows.Scan(&boundary.SpaceID, &boundary.MountID, &boundary.RelativePath); err != nil {
			return nil, err
		}
		boundaries = append(boundaries, boundary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return boundaries, nil
}

func (s *Service) loadAuthorizationTx(ctx context.Context, tx *sql.Tx, token *loadedToken) error {
	switch token.AuthorizationModel {
	case AuthorizationModelLegacyBoundary:
		boundaries, err := s.loadBoundariesTx(ctx, tx, token.ID)
		if err != nil {
			return err
		}
		token.Boundaries = boundaries
		token.SpaceGrants = nil
		return nil
	case AuthorizationModelSpaceGrant:
		grants, err := s.loadSpaceGrantsTx(ctx, tx, token.ID)
		if err != nil || len(grants) == 0 {
			return ErrInvalidToken
		}
		token.Boundaries = nil
		token.SpaceGrants = grants
		return nil
	default:
		return ErrInvalidToken
	}
}

func (s *Service) loadSpaceGrantsTx(ctx context.Context, tx *sql.Tx, tokenID string) ([]SpaceGrant, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT g.space_id, g.permission,
       d.mount_id, d.relative_path, d.identity_fingerprint, d.status
FROM ai_token_space_grants g
LEFT JOIN ai_token_folder_denies d
  ON d.token_id = g.token_id AND d.space_id = g.space_id
WHERE g.token_id = ?
ORDER BY g.space_id, d.mount_id, d.relative_path
`, tokenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []SpaceGrant
	grantIndex := make(map[string]int)
	for rows.Next() {
		var spaceID string
		var permission domain.SpacePermission
		var mountID, relativePath, identityFingerprint, status sql.NullString
		if err := rows.Scan(&spaceID, &permission, &mountID, &relativePath, &identityFingerprint, &status); err != nil {
			return nil, err
		}
		index, exists := grantIndex[spaceID]
		if !exists {
			index = len(grants)
			grantIndex[spaceID] = index
			grants = append(grants, SpaceGrant{SpaceID: spaceID, Permission: permission})
		}
		if mountID.Valid {
			grants[index].FolderDenylist = append(grants[index].FolderDenylist, FolderDeny{
				MountID:             mountID.String,
				RelativePath:        relativePath.String,
				IdentityFingerprint: identityFingerprint.String,
				Status:              FolderDenyStatus(status.String),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	normalized, err := normalizeSpaceGrants(grants)
	if err != nil {
		return nil, ErrInvalidToken
	}
	for _, grant := range normalized {
		for _, deny := range grant.FolderDenylist {
			if strings.TrimSpace(deny.IdentityFingerprint) == "" {
				return nil, ErrInvalidToken
			}
		}
	}
	return normalized, nil
}

type loadedToken struct {
	Token
	RevokedAt sql.NullString
}

func parseBearer(bearer string) (string, string, error) {
	value := strings.TrimSpace(bearer)
	if len(value) >= 7 && strings.EqualFold(value[:7], "bearer ") {
		value = strings.TrimSpace(value[7:])
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return "", "", ErrInvalidToken
	}
	publicID, secret, ok := strings.Cut(value, ".")
	if !ok || publicID == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", ErrInvalidToken
	}
	return publicID, secret, nil
}

func normalizeBoundaries(boundaries []DirectoryBoundary) ([]DirectoryBoundary, error) {
	if len(boundaries) == 0 {
		return nil, ErrInvalidInput
	}
	seen := make(map[string]bool, len(boundaries))
	normalized := make([]DirectoryBoundary, 0, len(boundaries))
	for _, boundary := range boundaries {
		spaceID := strings.TrimSpace(boundary.SpaceID)
		mountID := strings.TrimSpace(boundary.MountID)
		relativePath, err := normalizeRelativePath(boundary.RelativePath)
		if err != nil || spaceID == "" || mountID == "" {
			return nil, ErrInvalidInput
		}
		key := spaceID + "\x00" + mountID + "\x00" + relativePath
		if seen[key] {
			return nil, ErrInvalidInput
		}
		seen[key] = true
		normalized = append(normalized, DirectoryBoundary{
			SpaceID:      spaceID,
			MountID:      mountID,
			RelativePath: relativePath,
		})
	}
	return normalized, nil
}

func normalizeRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "", nil
	}
	if strings.Contains(value, "\x00") || strings.HasPrefix(value, "/") {
		return "", ErrInvalidInput
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", ErrInvalidInput
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".omnora" {
			return "", ErrInvalidInput
		}
	}
	return cleaned, nil
}

func marshalScopes(scopes []Scope) (string, error) {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, string(scope))
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalScopes(value string) ([]Scope, error) {
	var raw []string
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("%w: stored scopes", ErrInvalidScope)
	}
	scopes := make([]Scope, 0, len(raw))
	for _, scope := range raw {
		scopes = append(scopes, Scope(scope))
	}
	return ValidateScopes(scopes)
}

func newPrefixedID(prefix string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	return prefix + "_" + secret, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}

// formatTokenExpiry renders the stored expires_at value. The zero value means
// a token that never expires and is persisted as an empty string.
func formatTokenExpiry(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(timestampLayout, value)
	if err == nil {
		return parsed.UTC(), nil
	}
	parsed, err = time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}
