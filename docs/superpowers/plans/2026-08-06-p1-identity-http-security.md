# P1 Identity and HTTP Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Omnora's browser identity, administrator MFA, recent reauthentication, HTTP trust boundary, rate limiting, and security audit path fail closed without breaking the current REST user journeys.

**Architecture:** SQLite remains authoritative for accounts and sessions. The rollout first lands additive schema and backfills active sessions before the HTTP listener starts, then all HTTP routes move to one declarative manifest that selects an explicit authentication mode, session purpose, CSRF rule, and recent-reauthentication requirement. Immutable request/audit context is constructed before handlers run; high-risk database changes and their success audit event commit in the same transaction, while password/TOTP work remains outside write transactions and is protected by CAS predicates.

**Tech Stack:** Go 1.26, `net/http`, SQLite through `database/sql` and `modernc.org/sqlite`, React 19, TypeScript 5.7, Vitest, OpenAPI 3.1, and the repository-wide parser/generator versions pinned by the Catalog/API contract plan.

---

## Scope and non-negotiable contracts

This plan implements workflow 5 and the identity portion of workflow 8.4 from `docs/superpowers/specs/2026-08-06-p1-remediation-design.md`. It does not implement file-operation journals, upload recovery, mount identity, catalog leases, or restore mode; those plans consume the route/audit primitives defined here.

- `internal/store/migrations/006_standard_mcp.sql` must be tracked on the target branch and must already own `audit_events.result` and `audit_events.request_id`. This plan never repeats those `ALTER TABLE` statements.
- Session purposes are exactly `full` and `totp_enrollment`. An active row with `NULL` or any other value is invalid.
- `system_state.credential_generation` is an authorization epoch, not only an audit marker. Every newly issued identity/browser session and AI Token stores the current epoch; every verifier compares it before accepting the credential. The identity plan owns the epoch reader and identity/AI-token columns; the files/share plan owns upload/share/download-ticket columns. `revoked_at` remains mandatory and generation never replaces explicit revocation.
- `accounts.password_reset_required` and `accounts.totp_reset_required` are read at login and by `requireAdmin`. A valid old password with `password_reset_required=1` must include a new password in the same login request; the new hash is computed before the transaction, then the transaction clears the flag and issues the next session. If `totp_reset_required=1`, an administrator receives only `totp_enrollment` until a new TOTP is confirmed; no old active or pending TOTP can authenticate. Members receive a full session only after the password flag is cleared.
- Administrator sessions are `full` only after confirmed TOTP. An administrator without confirmed TOTP receives only an enrollment session after the password is correct.
- Login remains one `POST /api/v1/auth/session` request with optional `totpCode`; no TOTP challenge record or second-step endpoint is introduced.
- Recent reauthentication lasts five minutes, rotates both session and CSRF tokens, and never extends the original absolute session expiry.
- Production cookie security follows configured external `https`, never untrusted forwarding headers. Explicit local HTTP is allowed only when both the public URL and listener are loopback. Session/CSRF/share-session cookies use the shared `Path=/` policy; download capability cookies use a separate `AuthShareCapability` mode with an exact download path and never reuse a `__Host-` cookie name.
- Cookie and share-cookie unsafe requests require an allowed `Origin` and double-submit CSRF. Public pre-auth unsafe requests require an allowed `Origin` but not CSRF. Bearer/MCP requests may omit `Origin`; a supplied origin must be allowed.
- Rate limits always check both trusted client IP and an HMAC-obscured subject. Reaching key capacity sends unseen keys into a shared overflow bucket; it never evicts a key to make bypass possible.
- A high-risk SQL mutation is successful only when its audit insert commits in the same transaction.
- No commit is executed automatically. Each task has a proposed checkpoint commit, but the executor runs it only after the user explicitly authorizes committing.

## File responsibility map

### Create

- `internal/store/migrations/007_identity_security.sql` — additive identity and audit schema only.
- `internal/identity/rollout.go` — pre-listener session-purpose backfill and invariant check.
- `internal/identity/rollout_test.go` — upgrade, revocation, and fail-closed rollout tests.
- `internal/identity/totp_security.go` — pending TOTP state and atomic promotion operations.
- `internal/identity/totp_security_test.go` — pending-secret and enrollment-session behavior.
- `internal/identity/secure_mutations.go` — reauthentication, password rotation, and account-disable transactions.
- `internal/identity/secure_mutations_test.go` — CAS, rollback, and credential-revocation tests.
- `internal/ratelimit/limiter.go` — bounded sharded dual-key progressive limiter.
- `internal/ratelimit/limiter_test.go` — threshold, cooldown, overflow, and concurrency tests.
- `internal/server/http_security.go` — Host, Origin, trusted-proxy client IP, HSTS, and cookie policy.
- `internal/server/http_security_test.go` — complete trust-boundary matrix.
- `internal/server/routes.go` — `RouteDefinition` manifest and wrapper composition.
- `internal/server/routes_test.go` — authentication-mode and recent-reauth route matrix tests.
- `internal/server/csrf.go` — double-submit CSRF issuance, verification, rotation, and clearing.
- `internal/server/csrf_test.go` — production/development cookie and rejection tests.
- `internal/server/auth_rate_limit_test.go` — login, enrollment, reauth, TOTP, share, and initialization limiter tests.
- `internal/server/audit_context.go` — immutable request audit context and transaction recording helpers.
- `internal/server/audit_fail_closed_test.go` — injected audit failure and transaction rollback tests.
- `openapi/omnora.v1.yaml` — Verify only; the Catalog/API plan is the sole owner of source OpenAPI writes, parity tests, embedded sync, and generated output. This plan contributes identity schema requirements and fixtures.
- `web/src/api.test.ts` — CSRF and reauthentication API-client tests.
- `web/src/member/sessionFlow.ts` — pure signed-out/enrollment/full-session state transition.
- `web/src/member/sessionFlow.test.ts` — session-purpose state tests.
- `web/src/member/RecentReauthProvider.tsx` — one modal/queue for retrying a mutation after recent reauthentication.
- `web/src/member/RecentReauthProvider.test.tsx` — retry-once and cancel behavior.

### Modify

- `internal/store/sqlite_test.go` — assert migration order and all 007 columns/indexes.
- `internal/identity/types.go` — session purpose, reauthentication timestamp, TOTP state DTOs.
- `internal/identity/service.go` — explicit-purpose session creation/verification and equal-cost authentication.
- `internal/identity/security.go` — delegate legacy mutation entry points to secure transaction methods.
- `cmd/omnora/main.go` — run the identity rollout before listener creation.
- `internal/config/config.go`, `internal/config/config_test.go` — parse and validate external URL, allowlists, proxy CIDRs, and audit key.
- `internal/audit/audit.go`, `internal/audit/audit_test.go` — keyed domain-separated hashes and `RecordTx`.
- `internal/server/server.go`, `internal/server/api.go`, `internal/server/account_security.go`, `internal/server/admin_control.go`, `internal/server/member_shares.go`, `internal/server/mount_admin.go`, `internal/server/route_groups.go` — declarative routes, context principal, secure cookies, recent reauth, rate limiting, and fail-closed audit transactions.
- `internal/server/api_test.go`, `internal/server/account_security_test.go`, `internal/server/admin_control_test.go`, `internal/server/server_test.go`, `internal/server/share_portal_test.go`, `internal/server/route_groups_test.go` — update fixtures for explicit purpose, URL/origin, CSRF, and audit behavior.
- `deploy/docker-entrypoint.sh`, `scripts/verification/test-docker-entrypoint.sh` — persist `OMNORA_AUDIT_HMAC_KEY` without rotating old secrets.
- `deploy/docker-compose.yml`, `deploy/docker-compose.nas.yml`, `deploy/docker-compose.aliyun-test.yml`, `deploy/aliyun-test.env.example` — pass explicit public URL/allowlists/proxy CIDRs and the persisted audit key.
- `web/src/api.ts`, `web/src/member/MemberFilesApp.tsx`, `web/src/member/MemberAccountPanel.tsx`, `web/src/member/AdminWorkspace.tsx`, `web/src/member/AdminPanels.tsx`, `web/src/member/i18n.ts`, `web/package.json`, `web/package-lock.json` — generated identity types, CSRF, enrollment view, and recent-reauth retry.
- `openapi/omnora.v1.yaml` — Verify only; identity schema requirements are handed to the Catalog/API owner, which performs the single source write.
- `docs/api/README.md`, `deploy/README.md` — operator configuration and identity error semantics.

## Dependency graph

1. Task 1 is a hard gate for every other task.
2. Task 2 precedes Tasks 3, 4, and 8 because wrappers and reauthentication consume typed session purposes.
3. Tasks 5, 6, and 7 may proceed in parallel after Task 1; Task 8 depends on all three.
4. Task 9 depends on Tasks 3, 4, 7, and 8 so every guarded endpoint has the final principal and limiter context.
5. Task 10 depends on Task 6 because audit hashing must use the trusted client IP.
6. Task 11 may proceed after Task 5 and must finish before deployment verification.
7. Task 12 depends on Tasks 3, 4, and 8; Task 13 depends on the final route manifest and HTTP DTOs.
8. Task 14 is serial and runs only after every focused task passes.

### Task 1: Enforce the 006 migration prerequisite and add the 007 expand migration

**Files:**
- Verify only: `internal/store/migrations/006_standard_mcp.sql`
- Create: `internal/store/migrations/007_identity_security.sql`
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 1: Prove that 006 is a tracked prerequisite before writing P1 code**

Run:

```bash
git ls-files --error-unmatch internal/store/migrations/006_standard_mcp.sql
rg -n 'ADD COLUMN (result|request_id)' internal/store/migrations/006_standard_mcp.sql
```

Expected: the first command prints the file path and the second command prints exactly one owner for each column. If the first command fails, stop this plan. Either land the Standard MCP migration first or extract the shared audit expand migration and renumber all dependent plans together; do not treat the current untracked file as delivered.

- [ ] **Step 2: Write the failing schema assertions**

Append this assertion block to `TestOpenSQLiteAppliesMigrationsAndWAL` in `internal/store/sqlite_test.go`:

```go
for table, columns := range map[string][]string{
	"accounts": {"totp_pending_secret_ciphertext", "totp_pending_expires_at", "password_reset_required", "totp_reset_required"},
	"identity_sessions": {"purpose", "reauthenticated_at", "credential_generation"},
	"browser_sessions": {"credential_generation"},
	"ai_tokens": {"credential_generation"},
	"audit_events": {"reason_code", "subject_hash"},
} {
	for _, column := range columns {
		var count int
		if err := db.SQL().QueryRow(
			"SELECT COUNT(1) FROM pragma_table_info(?) WHERE name = ?",
			table,
			column,
		).Scan(&count); err != nil {
			t.Fatalf("query %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
		}
	}
}

var migrationName string
if err := db.SQL().QueryRow(
	"SELECT name FROM schema_migrations WHERE version = 7",
).Scan(&migrationName); err != nil {
	t.Fatalf("query migration 007: %v", err)
}
if migrationName != "007_identity_security.sql" {
	t.Fatalf("migration 007 name = %q", migrationName)
}
```

- [ ] **Step 3: Run the focused test and confirm the red state**

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/store -run TestOpenSQLiteAppliesMigrationsAndWAL -count=1
```

Expected: FAIL because the 007 columns do not exist.

- [ ] **Step 4: Add the additive migration**

Create `internal/store/migrations/007_identity_security.sql` with exactly these responsibilities:

```sql
ALTER TABLE accounts ADD COLUMN totp_pending_secret_ciphertext TEXT;
ALTER TABLE accounts ADD COLUMN totp_pending_expires_at TEXT;
ALTER TABLE accounts ADD COLUMN password_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (password_reset_required IN (0, 1));
ALTER TABLE accounts ADD COLUMN totp_reset_required INTEGER NOT NULL DEFAULT 0 CHECK (totp_reset_required IN (0, 1));

ALTER TABLE identity_sessions ADD COLUMN purpose TEXT;
ALTER TABLE identity_sessions ADD COLUMN reauthenticated_at TEXT;
ALTER TABLE identity_sessions ADD COLUMN credential_generation INTEGER;
ALTER TABLE browser_sessions ADD COLUMN credential_generation INTEGER;
ALTER TABLE ai_tokens ADD COLUMN credential_generation INTEGER;

ALTER TABLE audit_events ADD COLUMN reason_code TEXT;
ALTER TABLE audit_events ADD COLUMN subject_hash TEXT;

UPDATE audit_events SET result = 'success' WHERE result IS NULL;
UPDATE identity_sessions SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER) WHERE credential_generation IS NULL;
UPDATE browser_sessions SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER) WHERE credential_generation IS NULL;
UPDATE ai_tokens SET credential_generation = CAST((SELECT value FROM system_state WHERE key = 'credential_generation') AS INTEGER) WHERE credential_generation IS NULL;

CREATE INDEX identity_sessions_account_active_idx
	ON identity_sessions(account_id, revoked_at, expires_at);
CREATE INDEX audit_events_request_id_idx ON audit_events(request_id);
CREATE INDEX audit_events_subject_hash_idx ON audit_events(subject_hash);
```

Do not add `result` or `request_id`; they belong to migration 006. Do not add `NOT NULL` or `CHECK` yet; hard constraints are a later SQLite table rebuild after a stable release.

- [ ] **Step 5: Run migration tests**

Run the focused command from Step 3 and then:

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/store -count=1
```

Expected: PASS, with migrations 001 through 007 applied once and a second open remaining idempotent.

- [ ] **Step 6: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/store/migrations/007_identity_security.sql internal/store/sqlite_test.go
git commit -m "feat(security): add identity security expand migration"
```

### Task 2: Roll out explicit session purposes before the listener starts

**Files:**
- Create: `internal/identity/rollout.go`
- Create: `internal/identity/rollout_test.go`
- Modify: `internal/identity/types.go`
- Modify: `internal/identity/service.go`
- Modify: `internal/identity/service_test.go`
- Modify: `cmd/omnora/main.go`
- Modify: all test call sites returned by `rg -n 'SessionRequest\{' --glob '*.go'`

- [ ] **Step 1: Write failing purpose and rollout tests**

Cover these named tests in `internal/identity/rollout_test.go` and `internal/identity/service_test.go`:

```go
func TestPrepareSessionPurposeRolloutRevokesAdminWithoutTOTP(t *testing.T)
func TestPrepareSessionPurposeRolloutBackfillsOtherActiveSessions(t *testing.T)
func TestPrepareSessionPurposeRolloutRejectsUnknownActivePurpose(t *testing.T)
func TestVerifySessionRejectsNullAndUnknownPurpose(t *testing.T)
func TestCreateSessionRequiresExplicitPurpose(t *testing.T)
```

Use a fixed clock and seed three rows: an unconfirmed administrator, a confirmed administrator, and a member. Assert that the first is revoked, the other active rows become `full`, expired rows need not be backfilled, and an active `purpose='unexpected'` makes startup preparation return an error.

- [ ] **Step 2: Run the tests and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity -run 'Test(PrepareSessionPurposeRollout|VerifySessionRejects|CreateSessionRequires)' -count=1
```

Expected: FAIL because session purposes and startup rollout do not exist.

- [ ] **Step 3: Add the typed session contract**

Add to `internal/identity/types.go`:

```go
type SessionPurpose string

const (
	SessionPurposeFull           SessionPurpose = "full"
	SessionPurposeTOTPEnrollment SessionPurpose = "totp_enrollment"
)

func (purpose SessionPurpose) Valid() bool {
	return purpose == SessionPurposeFull || purpose == SessionPurposeTOTPEnrollment
}
```

Extend `Session` with `Purpose SessionPurpose` and `ReauthenticatedAt time.Time`. Extend `SessionRequest` with required `Purpose SessionPurpose`, and make `CreateSession` return `ErrInvalidInput` when it is absent or invalid.

- [ ] **Step 4: Implement the rollout transaction**

Create `internal/identity/rollout.go` with this public entry point:

```go
func PrepareSessionPurposeRollout(ctx context.Context, db *sql.DB, now time.Time) error
```

Inside one transaction:

1. Revoke every unrevoked, unexpired session whose account is an active administrator with `totp_required = 0`.
2. Set `purpose = 'full'` only for the remaining unrevoked, unexpired rows whose purpose is `NULL`.
3. Count active rows where `purpose IS NULL OR purpose NOT IN ('full', 'totp_enrollment')`.
4. Roll back and return an invariant error unless the count is zero.
5. Commit before returning.

The verification query must use the same `now.UTC().Format(time.RFC3339Nano)` value as both updates so time cannot cross a boundary halfway through rollout.

- [ ] **Step 5: Make verification fail closed and avoid duplicate lookups**

Change `VerifySession` and `ListSessions` to scan nullable purpose and reauthentication columns. Convert purpose only after scan:

```go
if !purpose.Valid || !SessionPurpose(purpose.String).Valid() {
	return Session{}, ErrSessionInvalid
}
session.Purpose = SessionPurpose(purpose.String)
if reauthenticatedAt.Valid {
	session.ReauthenticatedAt, err = parseTime(reauthenticatedAt.String)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
}
```

Update the `last_used_at` statement to retain its revocation and expiry predicates; an already invalid row must never be revived.

- [ ] **Step 6: Call rollout before any listener or worker starts**

In `cmd/omnora/main.go`, immediately after `store.OpenSQLite` succeeds and before initialization-token preparation, job workers, or `listeners.Start`, call:

```go
if err := identity.PrepareSessionPurposeRollout(ctx, db.SQL(), time.Now().UTC()); err != nil {
	slog.Error("prepare identity session rollout", "error", err)
	os.Exit(1)
}
```

Add `Purpose: identity.SessionPurposeFull` to every existing trusted test/session creator. Production login chooses the purpose in Task 3; no default is retained.

- [ ] **Step 7: Verify the rollout**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity ./cmd/omnora -count=1
```

Expected: PASS. A database containing an active unknown purpose must prevent the application from reaching listener startup.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/identity/types.go internal/identity/service.go internal/identity/service_test.go internal/identity/rollout.go internal/identity/rollout_test.go cmd/omnora/main.go internal/server/api.go internal/server/api_test.go internal/transfer/service_test.go
git commit -m "feat(identity): roll out explicit session purposes"
```

### Task 3: Add administrator enrollment sessions and pending TOTP promotion

**Files:**
- Create: `internal/identity/totp_security.go`
- Create: `internal/identity/totp_security_test.go`
- Modify: `internal/identity/types.go`
- Modify: `internal/identity/service.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/account_security.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/server/account_security_test.go`

- [ ] **Step 1: Write failing end-to-end tests**

Add these exact scenarios:

```go
func TestAdminWithoutTOTPReceivesEnrollmentSession(t *testing.T)
func TestEnrollmentSessionCanOnlyReadCurrentSetupConfirmAndLogout(t *testing.T)
func TestAdminLoginUsesGenericInvalidCredentialsForMissingOrWrongTOTP(t *testing.T)
func TestTOTPSetupKeepsConfirmedSecretActiveUntilPromotion(t *testing.T)
func TestTOTPConfirmRejectsExpiredPendingSecret(t *testing.T)
func TestTOTPConfirmPromotesPendingSecretAndRotatesSession(t *testing.T)
func TestAdminCannotDisableTOTP(t *testing.T)
```

For replacement, generate codes for both old and pending secrets. Assert the old code still logs in before confirmation, the pending code cannot log in before confirmation, and only the pending code works after confirmation.

- [ ] **Step 2: Run the focused tests and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity ./internal/server -run 'Test(AdminWithoutTOTP|EnrollmentSession|AdminLoginUsesGeneric|TOTPSetupKeeps|TOTPConfirm|AdminCannotDisable)' -count=1
```

Expected: FAIL on missing pending columns/behavior and unrestricted enrollment sessions.

- [ ] **Step 3: Add pending TOTP state operations**

Define in `internal/identity/totp_security.go`:

```go
const PendingTOTPDuration = 10 * time.Minute

type TOTPState struct {
	Required          bool
	ActiveCiphertext  string
	PendingCiphertext string
	PendingExpiresAt  time.Time
}

func (s *Service) LoadTOTPState(ctx context.Context, accountID string) (TOTPState, error)
func (s *Service) SavePendingTOTP(ctx context.Context, accountID, ciphertext string, expiresAt time.Time) error
func (s *Service) PromotePendingTOTP(ctx context.Context, accountID, expectedCiphertext string, now time.Time) error
```

`PromotePendingTOTP` must update only an active account whose pending ciphertext matches and whose pending expiry is later than `now`; it copies pending to active, sets `totp_required=1`, records `totp_confirmed_at`, and clears both pending columns. A zero affected-row result is `ErrInvalidCredential`.

- [ ] **Step 4: Keep credential work outside the write transaction**

In `setupTOTP`, generate and encrypt the secret before writing it, then call `SavePendingTOTP`. In `confirmTOTP`, load/decrypt/verify the pending secret before starting promotion/rotation work. Never clear or overwrite `totp_secret_ciphertext` during setup.

The setup response remains:

```go
type TOTPSetupResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauthUri"`
}
```

- [ ] **Step 5: Issue the correct session purpose on login**

Extend `SessionRequest` with optional `newPassword`. After old-password verification, load role, reset flags, current credential epoch, and TOTP state. If `password_reset_required=1`, require and validate `newPassword`; hash it before the write transaction. Apply this exact decision table after the reset transaction clears the password flag:

```go
switch {
case account.Role == domain.AccountRoleAdmin && account.TOTPResetRequired:
	purpose = identity.SessionPurposeTOTPEnrollment
case account.Role == domain.AccountRoleAdmin && !totpState.Required:
	purpose = identity.SessionPurposeTOTPEnrollment
case totpState.Required && !validSubmittedTOTP:
	return invalidCredentials
default:
	purpose = identity.SessionPurposeFull
}
```

The reset transaction must clear `password_reset_required` and create the new session with the current credential generation; if an administrator has `totp_reset_required=1`, clear any old active/pending TOTP and issue only `totp_enrollment`. No old TOTP code may authenticate. Return `purpose`, `requiresTotpEnrollment`, and a generic reset-required error only after password verification; do not reveal whether a failed account exists or has TOTP enabled.

- [ ] **Step 6: Rotate the session after successful confirmation**

Promotion, current-session revocation, new full-session insertion, and success audit are one transaction after Task 10 provides `RecordTx`. Preserve the original `expires_at`; set the new purpose to `full`; leave `reauthenticated_at` empty. Set the new session and CSRF cookies only after commit.

- [ ] **Step 7: Enforce administrator TOTP invariants**

`requireAdmin` must require active administrator role, `password_reset_required=0`, `totp_reset_required=0`, confirmed TOTP, and `SessionPurposeFull`. `disableAccountTOTP` returns `403 admin_totp_required` for administrators even after recent reauthentication. Members may disable after the Task 4 recent-reauth guard succeeds.

- [ ] **Step 8: Verify the complete enrollment state machine**

Run the focused command from Step 2, then:

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity ./internal/server -count=1
```

Expected: PASS, including concurrent confirm attempts where exactly one session rotation succeeds.

- [ ] **Step 9: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/identity/types.go internal/identity/service.go internal/identity/totp_security.go internal/identity/totp_security_test.go internal/server/api.go internal/server/account_security.go internal/server/api_test.go internal/server/account_security_test.go
git commit -m "feat(identity): enforce admin TOTP enrollment"
```

### Task 4: Implement recent reauthentication and atomic identity mutations

**Files:**
- Create: `internal/identity/secure_mutations.go`
- Create: `internal/identity/secure_mutations_test.go`
- Modify: `internal/identity/security.go`
- Modify: `internal/server/account_security.go`
- Modify: `internal/server/admin_control.go`
- Modify: `internal/server/api.go`
- Modify: corresponding server tests

- [ ] **Step 1: Write failing service and HTTP tests**

Add:

```go
func TestReauthenticateRotatesSessionWithoutExtendingAbsoluteExpiry(t *testing.T)
func TestReauthenticateRequiresTOTPWhenEnabled(t *testing.T)
func TestEnrollmentSessionCannotReauthenticate(t *testing.T)
func TestChangePasswordSecureRollsBackWhenAnyRevocationFails(t *testing.T)
func TestChangePasswordSecureRevokesOtherSessionsAndRotatesCurrent(t *testing.T)
func TestDisableAccountSecureRevokesEveryCredentialClass(t *testing.T)
func TestEnableAccountDoesNotReviveRevokedCredentials(t *testing.T)
```

Use a trigger that aborts an `audit_events` insert or one revocation update to prove that account/session/token/share state is unchanged after rollback.

- [ ] **Step 2: Run the focused tests and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity ./internal/server -run 'Test(Reauthenticate|EnrollmentSessionCannot|ChangePasswordSecure|DisableAccountSecure|EnableAccountDoesNot)' -count=1
```

Expected: FAIL because reauthentication and transaction-wide revocation do not exist.

- [ ] **Step 3: Define credential material and secure transaction requests**

In `internal/identity/secure_mutations.go`, define:

```go
const RecentReauthenticationTTL = 5 * time.Minute

type CredentialMaterial struct {
	AccountID           string
	Role                domain.AccountRole
	Status              string
	PasswordHash        string
	TOTPRequired        bool
	TOTPSecretCiphertext string
}

type RotateSessionRequest struct {
	SessionID       string
	AccountID       string
	ExpectedHash    string
	Purpose         SessionPurpose
	ExpiresAt       time.Time
	ReauthenticatedAt time.Time
}

type ChangePasswordSecureRequest struct {
	Session          Session
	ExpectedOldHash  string
	NewPasswordHash  string
	RevokeTokens     bool
	RevokeShares     bool
}
```

Also define `LoadCredentialMaterial`, `RotateSession`, `ChangePasswordSecure`, and `DisableAccountSecure`. Password hashing, password verification, TOTP decryption, and TOTP verification occur before any write transaction.

- [ ] **Step 4: Use CAS inside every secure transaction**

The account update predicate must include the account ID, active status, and previously read password hash:

```sql
UPDATE accounts
SET password_hash = ?, updated_at = ?
WHERE id = ? AND status = 'active' AND password_hash = ?;
```

Require exactly one affected row. In the same transaction revoke every other identity session, rotate the current identity session, optionally revoke AI tokens and creator shares/share sessions, and insert the success audit event. Any error rolls back everything.

`DisableAccountSecure` must atomically update status and revoke `identity_sessions`, `browser_sessions`, `ai_tokens`, creator `shares`, and related `share_sessions`; increment each affected share generation. Re-enable only updates account status and never clears a revocation timestamp.

Every identity/session/AI-token verifier must include `credential_generation = CurrentCredentialGeneration(ctx)` in its active-row predicate. Add a test that bumps the epoch without deleting a row and proves the old credential is rejected; add a test that new login/reauth credentials carry the new epoch.

- [ ] **Step 5: Add `POST /api/v1/account/reauthenticate`**

Use named DTOs:

```go
type ReauthenticateRequest struct {
	Password string `json:"password"`
	TOTPCode string `json:"totpCode,omitempty"`
}

type ReauthenticateResponse struct {
	Status               string    `json:"status"`
	ReauthenticatedUntil time.Time `json:"reauthenticatedUntil"`
}
```

Reject enrollment sessions. Verify password and, whenever TOTP is enabled, the active TOTP secret. Rotate the session with the original expiry and `reauthenticated_at=now`. Return `401 invalid_credentials` for either credential failure and `429` from Task 7.

- [ ] **Step 6: Centralize recent-reauth checks**

Add:

```go
func sessionRecentlyReauthenticated(session identity.Session, now time.Time) bool {
	return !session.ReauthenticatedAt.IsZero() &&
		now.Before(session.ReauthenticatedAt.Add(identity.RecentReauthenticationTTL))
}
```

The route wrapper, not each handler, returns `403 reauthentication_required`. It must never gate GET/list routes, ordinary file operations/uploads/preferences, or member-created/revoked shares.

- [ ] **Step 7: Verify atomic behavior and absolute expiry**

Run the focused command from Step 2. Inspect the rotated row and cookies to confirm the expiry is byte-for-byte equivalent to the original timestamp.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/identity/security.go internal/identity/secure_mutations.go internal/identity/secure_mutations_test.go internal/server/account_security.go internal/server/admin_control.go internal/server/api.go internal/server/account_security_test.go internal/server/admin_control_test.go internal/server/api_test.go
git commit -m "feat(identity): add recent reauthentication and atomic revocation"
```

### Task 5: Parse fail-closed HTTP trust configuration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `docs/api/README.md`
- Modify: `deploy/README.md`

- [ ] **Step 1: Write failing table tests for every environment variable**

Test exact and derived behavior for:

```text
OMNORA_PUBLIC_URL
OMNORA_ALLOWED_HOSTS
OMNORA_ALLOWED_ORIGINS
OMNORA_TRUSTED_PROXY_CIDRS
OMNORA_AUDIT_HMAC_KEY
```

Cases must include HTTPS with derived host/origin, explicit lists, wildcard rejection, malformed CIDR, public HTTP rejection, `http://127.0.0.1` with loopback listener acceptance, and `http://localhost` with `0.0.0.0` listener rejection.

- [ ] **Step 2: Run config tests and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/config -run 'TestLoadEnv.*(PublicURL|Allowed|Proxy|Audit)' -count=1
```

Expected: FAIL because the fields are not parsed.

- [ ] **Step 3: Extend the typed configuration**

Use these fields in `config.Config`:

```go
type HTTPConfig struct {
	Addr              string
	PublicURL         *url.URL
	AllowedHosts      map[string]struct{}
	AllowedOrigins    map[string]struct{}
	TrustedProxyCIDRs []*net.IPNet
}

type SecretConfig struct {
	TOTPEncryptionKey string
	AuditHMACKey      string
}
```

Comma-separated allowlists are exact values after canonical lower-casing; reject `*`, empty list members, userinfo, fragments, queries, and non-HTTP schemes. When an allowlist variable is empty and `PublicURL` exists, derive exactly one authority and one serialized origin from that URL.

- [ ] **Step 4: Separate parse validity from business-route readiness**

`LoadEnv` returns an error for malformed explicit values. Missing `PublicURL` is retained as a diagnosable state because the process must still serve health/readiness. Add:

```go
func (cfg HTTPConfig) ValidateBusinessExposure() error
```

It returns an error for missing public URL, missing audit key, or non-loopback HTTP. `Server` calls it whenever any business route is enabled; `/healthz` stays 200, `/readyz` returns 503 with `http_security_config_invalid`, and every non-health route returns 503 with the same code.

- [ ] **Step 5: Document operator examples without allow-all defaults**

Document a reverse-proxy example with `OMNORA_PUBLIC_URL=https://files.example.com`, exact host/origin derivation, and proxy CIDRs restricted to the proxy network. State that leaving proxy CIDRs empty trusts no forwarded headers.

- [ ] **Step 6: Verify config behavior**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/config ./internal/server -run 'Test.*HTTP.*Config' -count=1
```

- [ ] **Step 7: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/config docs/api/README.md deploy/README.md
git commit -m "feat(http): add fail-closed external URL policy"
```

### Task 6: Resolve trusted client IP and construct HTTP security policy

**Files:**
- Create: `internal/server/http_security.go`
- Create: `internal/server/http_security_test.go`
- Modify: `internal/server/server.go`

- [ ] **Step 1: Write the trust-boundary matrix first**

Table-drive these cases:

```go
type clientIPCase struct {
	name       string
	remoteAddr string
	forwarded  string
	xff        string
	want       string
	usedHeader bool
}
```

Include untrusted peer ignoring headers, one trusted proxy, multiple rightmost trusted proxies, spoofed leftmost input, all hops trusted falling back to `RemoteAddr`, malformed IP, duplicate header values, more than eight hops, and simultaneous `Forwarded` plus XFF. Rejected chains must use `RemoteAddr`, never the leftmost value.

- [ ] **Step 2: Run the test and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/server -run 'Test(ResolveClientIP|HostPolicy|OriginPolicy|SecurityHeaders)' -count=1
```

- [ ] **Step 3: Implement right-to-left proxy stripping**

Define:

```go
const maxForwardedHops = 8

type ClientIP struct {
	Address     netip.Addr
	FromHeader bool
}

func resolveClientIP(r *http.Request, trusted []*net.IPNet) ClientIP
```

Parse `RemoteAddr` first. Only inspect forwarded headers when that peer is trusted. Reject the entire chain and return the remote peer when either header is malformed, duplicated, too long, or both standards are present. Starting at the remote peer, walk supplied hops from right to left while each current hop is trusted; return the first non-trusted hop. If every supplied hop is trusted, return the remote peer.

- [ ] **Step 4: Implement exact Host and Origin checks**

Define `HTTPPolicy.CheckHost`, `HTTPPolicy.CheckOrigin`, and `HTTPPolicy.ExternalHTTPS`. Host matches the canonical request authority exactly. Origin matches serialized scheme/host/port exactly. `Origin: null`, multiple origins, malformed origins, and wildcard values are rejected.

Unsafe mode rules are:

```go
switch authMode {
case AuthCookieSession, AuthShareCookie, AuthPublicPreAuth:
	return originPresent && originAllowed
case AuthBearer:
	return !originPresent || originAllowed
case AuthHealth:
	return true
default:
	return false
}
```

Safe methods may omit Origin, but a supplied Origin must still validate.

- [ ] **Step 5: Wrap in the required order**

Change `Server.Handler` to compose:

```go
return securityHeaders(
	requestID(
	accessLog(
	recoverPanic(
	s.httpBoundary(s.mux),
	))),
)
```

`httpBoundary` validates business readiness, Host, Origin, and attaches `ClientIP` before route auth runs. HSTS is present only when `ExternalHTTPS()` is true.

- [ ] **Step 6: Stop logging raw proxy headers or using raw `RemoteAddr` for security**

The access log records `client_ip` from context. Audit and rate limiting consume the same context value. `RemoteAddr` remains available only as diagnostic peer metadata and is never hashed as the end-user address when a trusted chain succeeds.

- [ ] **Step 7: Verify the matrix and package tests**

Run the commands from Step 2 and then `go test ./internal/server -count=1` with writable Go caches.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/server/http_security.go internal/server/http_security_test.go internal/server/server.go
git commit -m "feat(http): enforce host origin and proxy trust"
```

### Task 7: Add the bounded dual-key progressive limiter

**Files:**
- Create: `internal/ratelimit/limiter.go`
- Create: `internal/ratelimit/limiter_test.go`
- Modify: `internal/server/server.go`

- [ ] **Step 1: Write deterministic limiter tests with a fake clock**

Cover subject failures at 5/10/20 producing 1/5/30-minute cooldowns, IP failures at 20/40/80 producing 1/5/60-minute cooldowns, successful subject reset without IP reset, cooldown expiry, sharded concurrency under `go test -race`, and capacity overflow where unseen subjects share an overflow bucket.

- [ ] **Step 2: Run and confirm the red state**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/ratelimit -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the small public API**

```go
type Scope string

const (
	ScopeLogin         Scope = "login"
	ScopeReauthenticate Scope = "reauthenticate"
	ScopeTOTP          Scope = "totp"
	ScopeShareExchange Scope = "share_exchange"
	ScopeInitialize    Scope = "initialize"
)

type Keys struct {
	IP      string
	Subject string
}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

func New(opts Options) *Limiter
func (l *Limiter) Check(scope Scope, keys Keys) Decision
func (l *Limiter) Failure(scope Scope, keys Keys) Decision
func (l *Limiter) Success(scope Scope, subject string)
```

Use 64 mutex-protected shards and a fixed total key capacity. New keys beyond capacity map to a per-scope/per-dimension overflow key. Expired cooldown state may be compacted in place; active keys are never evicted to accept a new attacker-controlled key.

- [ ] **Step 4: Add one limiter to `Server`**

Construct it once in `NewServer`; do not construct a limiter per request. Add a clock option for tests. Keys are HMAC values produced by Task 10, not plaintext email, public share ID, or initialization token.

- [ ] **Step 5: Verify concurrency and race safety**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test -race ./internal/ratelimit -count=1
```

Expected: PASS with no race and no unbounded map growth.

- [ ] **Step 6: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/ratelimit internal/server/server.go
git commit -m "feat(security): add progressive authentication limiter"
```

### Task 8: Register every route with explicit auth, CSRF, and recent-reauth metadata

**Files:**
- Create: `internal/server/routes.go`
- Create: `internal/server/routes_test.go`
- Create: `internal/server/csrf.go`
- Create: `internal/server/csrf_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/openapi.go`
- Modify: `internal/server/server.go`
- Modify: all server test request helpers

- [ ] **Step 1: Write route-manifest and CSRF tests first**

Assert that every registered API route has one valid auth mode and operation ID, no method/pattern pair is duplicated, every cookie-auth unsafe route rejects missing/mismatched CSRF, Bearer ignores incidental cookies, public login/initialize/share exchange do not require CSRF, and enrollment sessions are denied from all routes except current/setup/confirm/logout.

- [ ] **Step 2: Define the route manifest**

Create these types in `internal/server/routes.go`:

```go
type AuthMode string
type ContractKind string

const (
	AuthPublicPreAuth AuthMode = "public_pre_auth"
	AuthCookieSession AuthMode = "cookie_session"
	AuthShareCookie   AuthMode = "share_cookie"
	AuthShareCapability AuthMode = "share_capability"
	AuthBearer        AuthMode = "bearer"
	AuthHealth        AuthMode = "health"
	ContractOpenAPI   ContractKind = "openapi"
	ContractMCP       ContractKind = "mcp"
	ContractInternal  ContractKind = "internal"
	ContractHealth    ContractKind = "health"
	ContractDocument  ContractKind = "openapi_document"
)

type RouteDefinition struct {
	Method                   string
	Pattern                  string
	Group                    domain.RouteGroup
	Auth                     AuthMode
	Contract                 ContractKind
	OperationID              string
	AllowedSessionPurposes   []identity.SessionPurpose
	RequiresRecentReauth     bool
	EnrollmentBypassesReauth bool
	Handler                  http.HandlerFunc
}
```

`registerRoute` applies group gate, auth/principal context, purpose check, recent reauth, and CSRF in that order before registering with `ServeMux`. Existing handlers read the principal from context rather than calling `VerifySession` again.

`AuthShareCapability` is a deliberately separate capability mode. It is valid only for `GET` and `HEAD /api/v1/share/downloads/{ticketId}` in `RouteGroupShare`; it reads the exact-path `CookieNames.DownloadCapability` cookie and never falls back to an account or share-session cookie. Missing or invalid cookie, ticket ID, secret hash, share/session binding, credential generation, mount/object identity, or transfer lease returns the generic capability failure without revealing which check failed. The mode does not use account/share-session authentication, CSRF, or recent reauthentication. A disabled share route group returns 404. A supplied `Origin` still passes the global origin policy; an absent `Origin` is accepted only for this non-browser GET/HEAD capability download. Route tests must cover missing/wrong cookie, share cookie only, POST/PUT rejection, disabled route group, and exact-path cookie scoping.

- [ ] **Step 3: Encode the exact recent-reauth matrix**

Mark only these operations:

```text
PATCH  /api/v1/account/password
POST   /api/v1/account/totp/setup
POST   /api/v1/account/totp/confirm
POST   /api/v1/account/totp/disable
DELETE /api/v1/account/sessions/{sessionId}
POST   /api/v1/ai-tokens
DELETE /api/v1/ai-tokens/{tokenId}
POST   /api/v1/admin/users
POST   /api/v1/admin/users/{userId}/disable
POST   /api/v1/admin/users/{userId}/enable
POST   /api/v1/admin/users/{userId}/revoke-sessions
POST   /api/v1/admin/spaces
PATCH  /api/v1/admin/spaces/{spaceId}
DELETE /api/v1/admin/spaces/{spaceId}
PUT    /api/v1/admin/spaces/{spaceId}/members/{accountId}
DELETE /api/v1/admin/spaces/{spaceId}/members/{accountId}
POST   /api/v1/admin/mounts
PATCH  /api/v1/admin/mounts/{mountId}
POST   /api/v1/admin/mounts/{mountId}/reverify
DELETE /api/v1/admin/mounts/{mountId}
DELETE /api/v1/admin/shares/{shareId}
DELETE /api/v1/admin/ai-tokens/{tokenId}
POST   /api/v1/admin/backups
POST   /api/v1/admin/backups/{backupId}/restore
POST   /api/v1/admin/index-jobs
POST   /api/v1/admin/index-jobs/{jobId}/run
PATCH  /api/v1/admin/route-groups/{groupId}
```

TOTP setup/confirm allow `totp_enrollment` and `full`; enrollment bypasses recent reauth, full does not. Logout is CSRF-protected but never requires recent reauth. Every GET/list remains `RequiresRecentReauth=false`.

- [ ] **Step 4: Implement production and development cookie names**

In `internal/server/csrf.go`, select names only from configured external scheme:

```go
type CookieNames struct {
	Session      string
	CSRF         string
	ShareSession string
	ShareCSRF    string
	DownloadCapability string
}

var productionCookieNames = CookieNames{
	Session: "__Host-omnora_session", CSRF: "__Host-omnora_csrf",
	ShareSession: "__Host-omnora_share_session", ShareCSRF: "__Host-omnora_share_csrf",
	DownloadCapability: "__Secure-omnora_download_ticket",
}

var developmentCookieNames = CookieNames{
	Session: "omnora_dev_session", CSRF: "omnora_dev_csrf",
	ShareSession: "omnora_dev_share_session", ShareCSRF: "omnora_dev_share_csrf",
	DownloadCapability: "omnora_dev_download_ticket",
}
```

- Session, CSRF, and share-session cookies are host-only with `Path=/` and `SameSite=Lax`. Session cookies are HttpOnly; CSRF cookies are not. Production cookies are Secure; explicit loopback development cookies are not. `AuthShareCapability` uses `CookieNames.DownloadCapability`: production `__Secure-omnora_download_ticket`, development `omnora_dev_download_ticket`; both are HttpOnly, `SameSite=Strict`, and have a Path exactly equal to `/api/v1/share/downloads/{ticketId}`. It is not a `__Host-` cookie and is never sent outside that download path. Compare cookie/header tokens with `subtle.ConstantTimeCompare`.

- [ ] **Step 5: Issue, rotate, and clear cookies at exact lifecycle points**

- Login and share exchange issue their session and CSRF pair.
- Reauthentication and TOTP confirmation rotate both values.
- Logout clears session and CSRF; share-session invalidation clears its pair.
- Failed transactions do not emit replacement cookies.

- [ ] **Step 6: Update test helpers once**

Replace raw cookie-only helpers with a helper that creates a valid configured request:

```go
func authorizedJSONRequest(t *testing.T, method, path string, body io.Reader, auth testAuth) *http.Request
```

It sets `Host`, an allowed `Origin` for unsafe requests, the selected session cookie, CSRF cookie, and `X-CSRF-Token`. Tests that intentionally omit a property construct their request directly.

- [ ] **Step 7: Verify the route and CSRF matrix**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/server -run 'Test(RouteManifest|CSRF|EnrollmentSession|RecentReauthMatrix)' -count=1
```

Expected: PASS; no direct `s.mux.Handle` remains for API, MCP, OpenAPI, health, or readiness routes.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/server/routes.go internal/server/routes_test.go internal/server/csrf.go internal/server/csrf_test.go internal/server/api.go internal/server/openapi.go internal/server/server.go internal/server/api_test.go internal/server/account_security_test.go internal/server/admin_control_test.go internal/server/server_test.go internal/server/share_portal_test.go internal/server/route_groups_test.go
git commit -m "feat(http): declare route auth and csrf contracts"
```

### Task 9: Apply limiter and enumeration-safe authentication to every pre-auth credential path

**Files:**
- Create: `internal/server/auth_rate_limit_test.go`
- Modify: `internal/identity/crypto.go`
- Modify: `internal/identity/service.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/account_security.go`
- Modify: `internal/server/share_portal.go`

- [ ] **Step 1: Write failing HTTP tests**

For login, reauthenticate, setup/confirm/disable TOTP, initialization, and share exchange, assert both the IP and subject bucket are checked, 429 includes integer `Retry-After`, a successful subject clears only its subject failures, and responses do not reveal account/share existence.

For login specifically, assert nonexistent user, wrong password, missing TOTP, and wrong TOTP all return:

```json
{"error":{"code":"invalid_credentials","message":"credentials are not valid","request_id":"<non-empty>"}}
```

- [ ] **Step 2: Add fixed-cost dummy password verification**

Add to `PasswordHasher`:

```go
func (h PasswordHasher) VerifyDummy(password string) {
	iterations := h.Iterations
	if iterations <= 0 {
		iterations = defaultIterations
	}
	_ = pbkdf2SHA256([]byte(password), []byte("omnora-dummy-salt"), iterations, derivedKeyBytes)
}
```

When account lookup returns no row, call `VerifyDummy` before returning `ErrInvalidCredential`. Existing accounts still perform exactly one real password verification. Do not compare wall-clock durations in unit tests; assert via an injected verifier spy that one expensive verification path is called.

- [ ] **Step 3: Derive opaque limiter keys**

Use Task 10's keyed helper with separate labels:

```text
ratelimit:login:ip
ratelimit:login:subject
ratelimit:reauthenticate:ip
ratelimit:reauthenticate:subject
ratelimit:totp:ip
ratelimit:totp:subject
ratelimit:share:ip
ratelimit:share:subject
ratelimit:initialize:ip
ratelimit:initialize:subject
```

Normalize login email before hashing; use share public ID rather than fragment secret; use the fixed subject `initialization` rather than the initialization token.

- [ ] **Step 4: Check, record, and clear consistently**

Before credential work, call `Check`. After any credential failure, call `Failure` and return 429 if it starts or remains in cooldown. After success, call `Success(scope, subject)` and leave the IP count intact. Infrastructure/database errors do not count as credential failures.

- [ ] **Step 5: Verify all paths**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/identity ./internal/ratelimit ./internal/server -run 'Test.*(RateLimit|InvalidCredentials|Enumeration)' -count=1
```

- [ ] **Step 6: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/identity internal/server/auth_rate_limit_test.go internal/server/api.go internal/server/account_security.go internal/server/share_portal.go
git commit -m "feat(identity): rate limit credential verification"
```

### Task 10: Make audit context keyed, immutable, transactional, and fail closed

**Files:**
- Create: `internal/server/audit_context.go`
- Create: `internal/server/audit_fail_closed_test.go`
- Modify: `internal/audit/audit.go`
- Modify: `internal/audit/audit_test.go`
- Modify: database-only high-risk handlers listed in this task

- [ ] **Step 1: Write failing recorder and rollback tests**

Add tests that prove `RecordTx` uses the caller transaction, different domain labels produce different hashes for the same input, the raw client/subject never appears in the database, and an injected audit insert failure rolls back each representative mutation: password change, account disable, ACL update, AI-token revoke, share revoke, and route-group update.

- [ ] **Step 2: Generalize the recorder without transaction reentry**

Use a minimal executor interface:

```go
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r Recorder) Record(ctx context.Context, event Event) error {
	return record(ctx, r.db, event)
}

func (r Recorder) RecordTx(ctx context.Context, tx *sql.Tx, event Event) error {
	if tx == nil {
		return errors.New("audit transaction is nil")
	}
	return record(ctx, tx, event)
}
```

Extend `Event` with `Result`, `ReasonCode`, `RequestID`, `SubjectHash`, and `CredentialPublicID`. Insert 006-owned and 007-owned columns in one statement.

- [ ] **Step 3: Replace unkeyed hashes with domain-separated HMAC**

Add:

```go
func HashForAudit(key []byte, domain, value string) string {
	if len(key) == 0 || strings.TrimSpace(domain) == "" || strings.TrimSpace(value) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(domain))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
```

Remove security decisions based on the legacy unkeyed `HashForAudit(value)` signature.

- [ ] **Step 4: Build immutable request context before handlers**

Define:

```go
type AuditContext struct {
	ActorAccountID     string
	RequestID          string
	ClientHash         string
	UserAgentHash      string
	SubjectHash        string
	RouteGroup         domain.RouteGroup
	CredentialPublicID string
}
```

The HTTP boundary fills request/client/user-agent fields; the route auth wrapper returns a new request context with actor, subject, and credential ID. `recordAuditTx` reads this value only. It must not call `optionalSession`, `VerifySession`, or `sql.DB` from inside a transaction.

- [ ] **Step 5: Convert database-only high-risk mutations**

For account/password/TOTP/session, admin account enable-disable-revoke, space create/rename/delete/member PUT/DELETE, AI-token create/revoke, share create/revoke, route-group mutation, and backup metadata mutation:

1. Start one transaction.
2. Execute business SQL.
3. Call `recordAuditTx` with `result='success'`.
4. Commit.
5. Emit the HTTP success response only after commit.

Do not swallow any `RecordTx` error. File-system operations only consume the context and recorder API here; their operation-journal audit sequencing belongs to the file-state-machine plan.

- [ ] **Step 6: Handle rejected authentication audit without changing response semantics**

Rejected login/TOTP/share/rate-limit events use the non-transactional recorder. Recording failure logs a structured error containing request ID and action and makes readiness report an active audit warning; it never changes the existing 401/429 into a success. Do not implement a permanent readiness latch.

- [ ] **Step 7: Verify fail-closed behavior**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/audit ./internal/identity ./internal/server -run 'Test.*Audit' -count=1
```

Expected: PASS; grep must find no `_ = s.recordAudit` in converted database mutation paths.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/audit/audit.go internal/audit/audit_test.go internal/server/audit_context.go internal/server/audit_fail_closed_test.go internal/server/api.go internal/server/account_security.go internal/server/admin_control.go internal/server/member_shares.go internal/server/mount_admin.go internal/server/route_groups.go internal/server/api_test.go internal/server/account_security_test.go internal/server/admin_control_test.go internal/server/share_portal_test.go internal/server/route_groups_test.go
git commit -m "feat(audit): commit high risk mutations with audit"
```

### Task 11: Persist the audit HMAC key without rewriting existing runtime secrets

**Files:**
- Modify: `deploy/docker-entrypoint.sh`
- Modify: `scripts/verification/test-docker-entrypoint.sh`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.nas.yml`
- Modify: `deploy/docker-compose.aliyun-test.yml`
- Modify: `deploy/aliyun-test.env.example`

- [ ] **Step 1: Add failing shell acceptance cases**

Extend `test-docker-entrypoint.sh` to start from an exact two-key file:

```text
OMNORA_INITIALIZATION_TOKEN=existing-init-token
OMNORA_TOTP_ENCRYPTION_KEY=existing-totp-key
```

Record those two lines before upgrade, then assert after startup that their bytes are unchanged, exactly one `OMNORA_AUDIT_HMAC_KEY` line was appended, the audit value is non-empty, and a second startup leaves the complete file byte-identical.

Also fake `mv` failure and assert the original file is unchanged. Simulate an old binary by sourcing only the two original variables, then rerun the upgraded entrypoint and assert the same audit key is reused.

- [ ] **Step 2: Run and confirm the red state**

```bash
scripts/verification/test-docker-entrypoint.sh
```

Expected: FAIL because the audit key is absent.

- [ ] **Step 3: Append missing secrets through a same-directory temporary file**

Generate `OMNORA_AUDIT_HMAC_KEY` with `random_hex`. For an existing file, copy it byte-for-byte to a unique temporary file in the same directory, append only missing key lines, `chmod 600`, and atomically `mv` it into place. Install a trap that removes only that validated temporary path. Never truncate the original file before the replacement is ready.

For a new instance, write all three keys to the temporary file and atomically install it. The existing first-start rollback may delete a runtime file only when that same first start created it and the database was never created.

- [ ] **Step 4: Pass configuration through every Compose variant**

Add:

```yaml
OMNORA_AUDIT_HMAC_KEY: "${OMNORA_AUDIT_HMAC_KEY:-}"
OMNORA_PUBLIC_URL: "${OMNORA_PUBLIC_URL:?set OMNORA_PUBLIC_URL}"
OMNORA_ALLOWED_HOSTS: "${OMNORA_ALLOWED_HOSTS:-}"
OMNORA_ALLOWED_ORIGINS: "${OMNORA_ALLOWED_ORIGINS:-}"
OMNORA_TRUSTED_PROXY_CIDRS: "${OMNORA_TRUSTED_PROXY_CIDRS:-}"
```

The Aliyun example must use HTTPS behind the intended reverse proxy before business routes are enabled. Do not preserve the current public plain-HTTP example as a deployment recommendation.

- [ ] **Step 5: Run shell and Compose checks**

```bash
scripts/verification/test-docker-entrypoint.sh
scripts/verification/verify-scaffolding.sh
```

Expected: PASS; no generated secret value appears in stdout/stderr.

- [ ] **Step 6: Proposed checkpoint commit, only after explicit approval**

```bash
git add deploy/docker-entrypoint.sh deploy/docker-compose.yml deploy/docker-compose.nas.yml deploy/docker-compose.aliyun-test.yml deploy/aliyun-test.env.example scripts/verification/test-docker-entrypoint.sh
git commit -m "feat(deploy): persist audit hmac key safely"
```

### Task 12: Integrate CSRF, enrollment, and recent-reauth retry in the Web client

**Files:**
- Create: `web/src/api.test.ts`
- Create: `web/src/member/sessionFlow.ts`
- Create: `web/src/member/sessionFlow.test.ts`
- Create: `web/src/member/RecentReauthProvider.tsx`
- Create: `web/src/member/RecentReauthProvider.test.tsx`
- Modify: `web/src/api.ts`
- Modify: `web/src/member/MemberFilesApp.tsx`
- Modify: `web/src/member/MemberAccountPanel.tsx`
- Modify: `web/src/member/AdminWorkspace.tsx`
- Modify: `web/src/member/AdminPanels.tsx`
- Modify: `web/src/member/i18n.ts`

- [ ] **Step 1: Write pure API and session-flow tests**

Test that unsafe requests read the selected CSRF cookie and add `X-CSRF-Token`, safe requests do not add it, login/reauth response handling accepts rotated cookies, and session purpose maps exactly as follows:

```ts
export type SessionState = 'signed-out' | 'enrollment' | 'ready';

export function stateForSession(session: SessionPayload): SessionState {
  if (session.purpose === 'totp_enrollment' || session.requiresTotpEnrollment) return 'enrollment';
  return session.purpose === 'full' ? 'ready' : 'signed-out';
}
```

- [ ] **Step 2: Add one security-aware fetch primitive**

Export `request` from `web/src/api.ts` and make `requestJson`, `downloadRange`, `uploadPart`, and the preview fetch in `MemberFilesApp.tsx` use it. Define the request selector once so callers cannot accidentally use the wrong CSRF pair:

```ts
export type APIRequestInit = RequestInit & { authContext?: 'account' | 'share' };
export function requestJson<T>(path: string, init?: APIRequestInit): Promise<T>;
```

Its unsafe-method branch is:

```ts
const unsafe = !['GET', 'HEAD', 'OPTIONS'].includes((init.method ?? 'GET').toUpperCase());
if (unsafe) {
  const names = init.authContext === 'share'
    ? ['__Host-omnora_share_csrf', 'omnora_dev_share_csrf']
    : ['__Host-omnora_csrf', 'omnora_dev_csrf'];
  const token = names.map(readCookie).find(Boolean);
  if (token) headers.set('X-CSRF-Token', token);
}
```

Share-portal requests pass `authContext: 'share'`; ordinary account mutations use `authContext: 'account'`; GET/HEAD/OPTIONS omit the header. No component calls `fetch` directly after this step.

- [ ] **Step 3: Add generated reauthentication API calls**

Define:

```ts
export type ReauthenticatePayload = { password: string; totpCode?: string };
export type ReauthenticateResponse = { status: 'reauthenticated'; reauthenticatedUntil: string };

export function reauthenticate(payload: ReauthenticatePayload, signal?: AbortSignal) {
  return requestJson<ReauthenticateResponse>('/api/v1/account/reauthenticate', {
    method: 'POST',
    body: JSON.stringify(payload),
    signal,
  });
}
```

- [ ] **Step 4: Implement one retry queue for sensitive mutations**

`RecentReauthProvider` exposes:

```ts
type RunSensitive = <T>(operation: () => Promise<T>) => Promise<T>;
```

It runs the operation once. Only an `ApiError` with status 403 and code `reauthentication_required` opens the modal. After successful `reauthenticate`, retry the stored operation exactly once. Cancel rejects the pending promise without executing it; a second 403 is surfaced and never loops.

- [ ] **Step 5: Route session state to enrollment UI**

`MemberFilesApp` must not call `loadSpaces` for an enrollment session. It renders only the account TOTP setup/confirm surface plus logout. After confirm rotates to a full session, call `getSession`, transition to `ready`, and then load spaces. An administrator full session never sees a TOTP-disable control.

- [ ] **Step 6: Wrap every exact recent-reauth mutation**

Use `runSensitive` for the Task 8 route matrix in account/admin panels. Do not wrap GET/list, preferences, ordinary files/uploads, or member share create/revoke.

- [ ] **Step 7: Run Web tests and build**

```bash
cd web
npm test -- --run
npm run build
```

Expected: PASS with no unhandled reauth promise and no direct `fetch(` outside `src/api.ts`.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add web/src web/package.json web/package-lock.json
git commit -m "feat(web): support csrf enrollment and recent reauth"
```

### Task 13: Contribute identity requirements to the shared OpenAPI contract and validate real responses

**Files:**
- Modify: `internal/server/api_test.go` (identity response fixtures consumed by the shared contract test)
- Verify only: `openapi/omnora.v1.yaml`, `internal/server/openapi_assets/omnora.v1.yaml`, `internal/server/openapi_contract_test.go`, `web/src/generated/omnora-api.ts`

The Catalog/API plan is the sole owner of `kin-openapi`, `openapi-typescript`, `openapi_contract_test.go`, the embedded asset sync, the generated `web/src/generated/omnora-api.ts`, and `verify-openapi-types.sh`. This task must not create a second parser, test, generator version, component alias, or generated output file.

- [ ] **Step 1: Consume the shared contract tooling**

Do not install or change parser/generator versions here. Confirm the Catalog/API plan has already pinned the repository-wide versions and generated output path. Add only identity fixtures to the shared test harness.

- [ ] **Step 2: Add identity fixtures to the shared parity test**

Do not create another `openapi_contract_test.go`. Add identity success/error fixtures and operation IDs to the shared test owned by the Catalog/API plan. The shared test must normalize the `/api/v1` server prefix and compare `RouteDefinition` entries; root `/mcp`, health/readiness, static SPA fallbacks, and OpenAPI document routes use their declared non-REST contract kind.

- [ ] **Step 3: Replace anonymous identity schemas**

Hand the following named-schema and operation requirements to the Catalog/API owner, which writes the source YAML once and owns the shared parity test:

```text
SessionResponse
CreateSessionRequest
InitializeRequest
InitializeResponse
Account
AccountSession
AccountSessionList
ChangePasswordRequest
StatusResponse
TOTPSetup
TOTPConfirmRequest
ReauthenticateRequest
ReauthenticateResponse
```

Add `purpose: [full, totp_enrollment]`, `requiresTotpEnrollment`, `reauthenticatedUntil`, and exact 401/403/409/429 responses. `POST /auth/session` remains `security: []`; cookie routes use `cookieSession`; no identity route advertises Bearer. Do not modify `openapi/omnora.v1.yaml` in this task.

- [ ] **Step 4: Validate real identity responses through the shared harness**

Add real `httptest` responses for initialization, login full, login enrollment, current session, reauthenticate, TOTP setup/confirm/disable, password change, and session list/revoke to the shared `kin-openapi` harness. Validate the primary 401 `invalid_credentials`, 403 `reauthentication_required`, and 429 error envelopes as well as successful responses.

- [ ] **Step 5: Consume the single generated TypeScript module**

Do not create `.d.ts` or a second `.ts` output. After the Catalog/API plan generates `web/src/generated/omnora-api.ts`, alias identity types from that one module in `web/src/api.ts`:

```ts
import type { components } from './generated/omnora-api';

export type SessionPayload = components['schemas']['SessionResponse'];
export type AccountPayload = components['schemas']['Account'];
export type ReauthenticatePayload = components['schemas']['ReauthenticateRequest'];
export type ReauthenticateResponse = components['schemas']['ReauthenticateResponse'];
```

- [ ] **Step 6: Hand the identity schema to the shared gate**

The Catalog/API plan owns `sync-openapi-asset.sh`, `verify-api-docs.sh`, and the generated-type gate. Add identity-specific required schema/error assertions to that shared gate through its owned task; do not edit the same scripts in parallel.

- [ ] **Step 7: Verify the shared generated artifacts**

Run the shared Catalog/API generation/check command and confirm identity aliases compile. Do not compare or commit a second generated file:

```bash
npm --prefix web run check:api-types
scripts/verification/sync-openapi-asset.sh --check
```

Expected: no diff from the single generated module and embedded asset.

- [ ] **Step 8: Proposed checkpoint commit, only after explicit approval**

```bash
git add internal/server/api_test.go
git commit -m "feat(api): add identity contract schemas"
```

### Task 14: Run release gates, upgrade rehearsal, and rollback rehearsal

**Files:**
- Verify: all files changed by Tasks 1–13
- Modify only if a failing test exposes an in-scope defect

- [ ] **Step 1: Run focused security packages**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/store ./internal/identity ./internal/ratelimit ./internal/audit ./internal/config ./internal/server -count=1
```

Expected: PASS.

- [ ] **Step 2: Run race-sensitive packages**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test -race ./internal/ratelimit ./internal/identity ./internal/server -count=1
```

Expected: PASS with no race report.

- [ ] **Step 3: Run the full Go gates**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./...
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go vet ./...
```

Expected: PASS. If a localhost-bind test is denied by the managed sandbox, rerun that same command with the required permission and report the environment limitation separately from code failures.

- [ ] **Step 4: Run Web and generated-contract gates**

```bash
cd web
npm test -- --run
npm run generate:api-types
npm run build
cd ..
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
```

Expected: PASS and generated TypeScript/OpenAPI assets remain unchanged.

- [ ] **Step 5: Run deployment-script gates**

```bash
scripts/verification/test-docker-entrypoint.sh
scripts/verification/verify-scaffolding.sh
git diff --check
```

Expected: PASS.

- [ ] **Step 6: Rehearse database upgrade**

Make a disposable copy of a pre-006 database, apply the tracked target branch, and assert:

- schema migrations contain 006 then 007 exactly once;
- old confirmed-admin/member sessions become `full`;
- unconfirmed-admin sessions are revoked;
- no active session has NULL/unknown purpose;
- audit history has `result='success'` and new nullable fields;
- the application reaches ready only with valid HTTP security configuration.

- [ ] **Step 7: Rehearse proxy HTTPS and browser behavior**

In an environment with the real reverse proxy, prove:

- external HTTPS responses issue Secure host-only session and CSRF cookies;
- HSTS is present;
- spoofed XFF from an untrusted peer cannot alter audit/limiter client identity;
- missing/wrong Host, Origin, or CSRF is rejected;
- login, enrollment, reauthenticate, sensitive mutation retry, logout, and share exchange work in supported browsers.

Local `httptest` results do not replace this external proof.

- [ ] **Step 8: Rehearse rollback without deleting additive schema**

Rollback procedure:

1. Disable external admin, REST, share, MCP, and OpenAPI exposure at the reverse proxy.
2. Preserve the database and `runtime.env`; never remove 006/007 rows or rotate TOTP/audit keys.
3. Deploy the previous binary only as a short availability rollback.
4. Expect all users to sign in again because production cookie names changed.
5. Before re-upgrading, stop the old binary so it cannot create new NULL-purpose sessions; run `PrepareSessionPurposeRollout` again and verify invariants.

- [ ] **Step 9: Final review checkpoint**

Review `git status --short`, `git diff --stat`, and `git diff`. Confirm no secrets, generated temp files, unrelated concurrent changes, or duplicate migration ownership entered the change set.

- [ ] **Step 10: Leave integration state ready for the user's commit decision**

Do not stage or commit a catch-all directory set in the shared worktree. Report the exact task-specific file list, verification output, and any concurrent changes; if the user requests a squash commit, stage only that reviewed list explicitly and use `feat(security): harden identity and http trust boundaries`.

## Acceptance checklist

- [ ] 006 is tracked and tested before 007; 007 never re-adds 006-owned columns.
- [ ] Active NULL/unknown-purpose sessions cannot survive startup; unconfirmed administrators lose old sessions.
- [ ] Credential epoch is stored on every identity/browser/AI-token issue and checked by every verifier; an epoch bump rejects old rows even before explicit revocation cleanup.
- [ ] `password_reset_required` forces new-password submission during login; `totp_reset_required` prevents old TOTP use and limits administrators to enrollment until confirmation.
- [ ] Administrator MFA is mandatory, pending setup preserves the active secret, and enrollment sessions have only four allowed capabilities.
- [ ] Recent reauthentication is a single explicit endpoint, expires after five minutes, rotates cookies, preserves absolute expiry, and gates only the exact mutation matrix.
- [ ] Password change and account disable revoke the required credentials atomically with audit; re-enable cannot revive them.
- [ ] Missing/invalid external URL configuration leaves only health/readiness diagnostics available.
- [ ] Host, Origin, CSRF, cookie security, HSTS, and trusted client IP pass the complete allow/reject matrix.
- [ ] Login, TOTP, reauthentication, sharing, and initialization have dual IP/subject progressive limits and non-enumerating credential errors.
- [ ] Audit identifiers use domain-separated HMAC; high-risk SQL mutations and success audit commit together.
- [ ] Existing initialization/TOTP secret bytes remain unchanged while the audit key is appended atomically and reused across restart/rollback/re-upgrade.
- [ ] Web unsafe requests use the shared CSRF-aware fetch path; enrollment and recent-reauth retry are observable user flows.
- [ ] Identity routes, handlers, real responses, embedded OpenAPI, and generated TypeScript types have zero drift.
- [ ] Focused tests, full Go tests/vet, Web tests/build, script gates, upgrade rehearsal, and proxy/browser proof all pass.
