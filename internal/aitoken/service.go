package aitoken

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const timestampLayout = time.RFC3339Nano

type Service struct {
	db  *sql.DB
	now func() time.Time
}

type Option func(*Service)

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
	if s == nil || s.db == nil {
		return IssuedToken{}, ErrInvalidInput
	}
	accountID := strings.TrimSpace(req.AccountID)
	name := strings.TrimSpace(req.Name)
	now := s.now().UTC().Round(0)
	if accountID == "" || name == "" || !now.Before(req.ExpiresAt) {
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
	INSERT INTO ai_tokens(id, public_id, secret_hash, account_id, name, scopes, credential_generation, created_at, expires_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, token.ID, token.PublicID, token.SecretHash, token.AccountID, token.Name, scopeJSON, token.CredentialGeneration, formatTime(token.CreatedAt), formatTime(token.ExpiresAt), formatTime(now))
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
	if err := tx.Commit(); err != nil {
		return IssuedToken{}, err
	}

	return IssuedToken{
		Token:       token,
		Secret:      secret,
		BearerToken: publicID + "." + secret,
	}, nil
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
	if token.RevokedAt.Valid || !now.Before(token.ExpiresAt) || !secretMatches(secret, token.SecretHash) {
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
		AccountID:  token.AccountID,
		TokenID:    token.ID,
		PublicID:   token.PublicID,
		Scopes:     token.Scopes,
		Boundaries: token.Boundaries,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: now,
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
	if token.RevokedAt.Valid || !now.Before(token.ExpiresAt) {
		return Principal{}, ErrInvalidToken
	}
	return Principal{
		AccountID:  token.AccountID,
		TokenID:    token.ID,
		PublicID:   token.PublicID,
		Scopes:     token.Scopes,
		Boundaries: token.Boundaries,
		ExpiresAt:  token.ExpiresAt,
		LastUsedAt: token.LastUsedAt,
	}, nil
}

func (s *Service) Revoke(ctx context.Context, tokenID string) error {
	if s == nil || s.db == nil || strings.TrimSpace(tokenID) == "" {
		return ErrInvalidInput
	}
	now := s.now().UTC().Round(0)
	_, err := s.db.ExecContext(ctx, `
UPDATE ai_tokens
SET revoked_at = ?, updated_at = ?
WHERE id = ? AND revoked_at IS NULL
`, formatTime(now), formatTime(now), strings.TrimSpace(tokenID))
	return err
}

func (s *Service) loadToken(ctx context.Context, publicID string) (loadedToken, error) {
	if s == nil || s.db == nil {
		return loadedToken{}, ErrInvalidToken
	}
	var token loadedToken
	var scopesJSON, createdAt, expiresAt string
	var lastUsedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
	SELECT t.id, t.public_id, t.secret_hash, t.account_id, t.name, t.scopes,
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
	token.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return loadedToken{}, err
	}
	if lastUsedAt.Valid {
		token.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return loadedToken{}, err
		}
	}
	token.Boundaries, err = s.loadBoundaries(ctx, token.ID)
	if err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadTokenByID(ctx context.Context, tokenID string) (loadedToken, error) {
	if s == nil || s.db == nil {
		return loadedToken{}, ErrInvalidToken
	}
	var token loadedToken
	var scopesJSON, createdAt, expiresAt string
	var lastUsedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
	SELECT t.id, t.public_id, t.secret_hash, t.account_id, t.name, t.scopes,
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
	token.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return loadedToken{}, err
	}
	if lastUsedAt.Valid {
		token.LastUsedAt, err = parseTime(lastUsedAt.String)
		if err != nil {
			return loadedToken{}, err
		}
	}
	token.Boundaries, err = s.loadBoundaries(ctx, token.ID)
	if err != nil {
		return loadedToken{}, err
	}
	return token, nil
}

func (s *Service) loadBoundaries(ctx context.Context, tokenID string) ([]DirectoryBoundary, error) {
	rows, err := s.db.QueryContext(ctx, `
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
