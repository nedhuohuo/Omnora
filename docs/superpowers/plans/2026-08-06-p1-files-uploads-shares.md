# P1 Files, Uploads, Mounts, and Shares Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Omnora file access, uploads, mount registration, trash/cross-mount operations, shares, and anonymous downloads fail closed and recover safely across races, process crashes, database failures, path replacement, and NAS outages.

**Architecture:** Introduce descriptor-relative storage primitives and make SQLite the authority for uploads, file operations, mount identity claims, share identities, and download tickets. Keep filesystem I/O outside long SQLite transactions, bridge database and filesystem state with persisted operations plus CAS/lease/fencing tokens, and revalidate live account/ACL/mount/object identity at every externally usable capability. Migrations remain additive; legacy upload manifests and shares are imported or invalidated before normal readiness.

**Tech Stack:** Go 1.26, `net/http`, `database/sql`, modernc SQLite, `golang.org/x/sys/unix`, React 19, TypeScript, Vitest, OpenAPI 3.1, POSIX shell verification scripts.

---

## Execution rules and dependency gates

- Implement tasks in order unless the “Parallel boundary” section explicitly permits otherwise. Every behavior task follows RED → GREEN: first add the focused failing test, run it and confirm the intended failure, then implement the smallest complete behavior.
- Do not modify or renumber existing migrations. The five P1 migrations are one coordinated block `007 → 011`; this plan owns the 008/009 positions only after tracked 006 and 007 are present and all five owners agree on the baseline. If any target branch migration occupies 007–011, stop and shift the entire block together; never locally move only 008/009 into 010 or another plan's slot.
- `006_standard_mcp.sql` is currently an untracked worktree file. It must be committed to the target branch and pass the migration upgrade test before Task 1 starts; this plan must never rely on an untracked migration.
- `007_identity_security.sql` and `audit.Recorder.RecordTx(ctx, tx, event)` are delivered by the identity/HTTP P1 plan. Tasks 5-8 may build tests against that exact transaction-aware audit contract, but production file mutations must not switch over until it exists.
- The catalog/jobs P1 plan owns catalog walker adoption and `010_catalog_jobs.sql`. Task 2 exports the secure walker for catalog; Task 8 must not edit catalog scan code. Cross-mount recovery uses `file_operations` leases directly and therefore does not require a second copy of job state. The Catalog plan's maintenance task owns the single `runJobMaintenance` orchestration; file-operation recovery and upload GC are invoked from its ordered callbacks only after job lease reclaim.
- Preserve the current REST JSON shape unless this plan explicitly introduces the approved download-ticket handshake. The separate API-contract plan remains responsible for generated DTO coverage across unrelated routes.
- Keep `status` on `upload_sessions` constrained to `active|completed|canceled|expired|failed`. Transitional states belong only in nullable `operation_phase`; do not rebuild the table in this release.
- Do not add quota reservation, parallel Range downloads, multiple simultaneous ranges, antivirus scanning, archive extraction, or cross-platform unsafe fallbacks. Those are outside this P1 scope.
- Do not add automatic `git commit` steps. Inspect every task diff and commit only if the user explicitly asks.
- Never log or audit session cookies, share fragment secrets, ticket secrets, bearer tokens, passwords, file bytes, host absolute paths, or raw NAS error payloads.

## File ownership map

| Area | Files | Responsibility |
| --- | --- | --- |
| SQLite primitives/migrations | `internal/store/immediate.go`, `internal/store/migrations/008_file_operations_and_uploads.sql`, `internal/store/migrations/009_share_tickets_and_identity.sql` | BEGIN IMMEDIATE wrapper, durable operation/upload/mount/share/ticket state |
| Storage safety | `internal/storage/root.go`, OS-specific root files, `internal/storage/identity.go` | descriptor-relative open/walk, no-symlink/no-cross-device policy, no-replace rename, fsync, object identity |
| Mount claims | `internal/mountid/claims.go` | startup audit/backfill, atomic claims, registration and re-verification |
| File operations | `internal/fileops/journal.go`, `internal/fileops/mutation.go`, `internal/fileops/cross_mount.go`, `internal/fileops/recovery.go` | persisted state machines and filesystem recovery |
| Existing file facade | `internal/files/service.go`, `internal/files/trash_crossmount.go` | stable list/open/create/trash API backed by safe roots; old cross-mount implementation removed after cutover |
| Uploads | `internal/transfer/store.go`, `internal/transfer/service.go`, `internal/transfer/importer.go`, `internal/transfer/recovery.go` | upload phase/CAS/lease/fencing, immutable part publication, legacy import, cleanup |
| Shares | `internal/share/identity.go`, `internal/share/tickets.go`, existing service/session files | target identity, three-way validation outcome, legacy binding, one-count download tickets |
| HTTP and startup | `internal/server/file_ops.go`, `internal/server/share_portal.go`, `internal/server/api.go`, `internal/server/mount_admin.go`, `internal/server/server.go`, `cmd/omnora/main.go` | authorization orchestration, handlers, startup gates, and bounded recovery callbacks; Catalog/API owns `internal/server/index_worker.go` orchestration |
| Web/API/docs | `web/src/api.ts`, `web/src/SharePortalApp.tsx`, API/security/domain/acceptance docs | ticket handshake, preview route, current contract and release evidence; Catalog/API plan is the sole owner of OpenAPI source/embed and generated artifacts |

## Canonical state and type contracts

Use these exact persisted state values throughout tests, SQL, Go constants, API diagnostics, and recovery:

```text
upload_sessions.status:
  active | completed | canceled | expired | failed

upload_sessions.operation_phase:
  initializing | writing | completing | canceling | expiring | recovery_required

upload_parts.state:
  writing | ready | superseded | cleanup_pending

file_operations.status:
  prepared | source_staged | copying | destination_staged |
  published | source_cleaned | completed | recovery_required

share_download_tickets.status:
  issued | streaming | completed | expired | canceled

share identity outcome:
  match | definitive_changed_or_not_found | transient_unavailable
```

The public Go shapes introduced by this plan are:

```go
// internal/storage
type ResolvePolicy struct {
    NoCrossDevice bool
}

type ObjectIdentity struct {
    Device      uint64 `json:"device"`
    Inode       uint64 `json:"inode"`
    Mode        uint32 `json:"mode"`
    Size        int64  `json:"size"`
    ModTimeNano int64  `json:"modTimeNano"`
}

type Root interface {
    Close() error
    Open(relative string) (*os.File, error)
    OpenDirectory(relative string) (*os.File, error)
    OpenFile(relative string, flags int, perm fs.FileMode) (*os.File, error)
    Mkdir(relative string, perm fs.FileMode) error
    Remove(relative string) error
    RemoveAll(relative string) error
    ReadDirectory(relative string) ([]DirectoryEntry, error)
    RenameNoReplace(from, to string) error
    FsyncParent(relative string) error
}

type DirectoryEntry struct {
    Name     string
    Info     fs.FileInfo
    Identity ObjectIdentity
}

func OpenRoot(rootPath string, policy ResolvePolicy) (Root, error)
func ObjectIdentityFromOpenFile(file *os.File) (ObjectIdentity, error)

// internal/store
func WithImmediate(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error

// internal/share
type IdentityOutcome string

const (
    IdentityMatch IdentityOutcome = "match"
    IdentityDefinitiveChanged IdentityOutcome = "definitive_changed_or_not_found"
    IdentityTransientUnavailable IdentityOutcome = "transient_unavailable"
)
```

### Task 0: Freeze the migration baseline and add upgrade fixtures

**Files:**
- Modify: `internal/store/sqlite_test.go`
- Create: `internal/store/migration_upgrade_test.go`

- [ ] **Step 1: Confirm the hard prerequisite on the target branch**

Run:

```bash
git ls-files internal/store/migrations/*.sql
git status --short -- internal/store/migrations
```

Expected before proceeding: `006_standard_mcp.sql` and `007_identity_security.sql` appear in `git ls-files`; no untracked migration occupies 008 or 009. If either prerequisite is absent, stop this plan and land the owning plan first.

- [ ] **Step 2: Add a pre-P1 upgrade fixture without duplicating schema SQL**

In `migration_upgrade_test.go`, add a test-only `applyMigrationsThrough(ctx, db, 7)` helper that reads the package's embedded migration files in numeric order and records them in `schema_migrations`, then inserts one active mount, one active manifest-backed upload with two parts, one file object, one active share, and one share session. Use stable IDs `mnt_upgrade`, `upl_upgrade`, `obj_upgrade`, `shr_upgrade`, and `shs_upgrade`; use only relative paths in payload fields. Close that DB, reopen it through `OpenSQLite`, and let production migration code apply 008/009.

- [ ] **Step 3: Add failing migration contract tests**

Add these tests to `internal/store/sqlite_test.go`:

```go
func TestP1FileMigrationsAreAdditive(t *testing.T)
func TestP1FileUpgradePreservesLegacyUploadAndShareRows(t *testing.T)
func TestP1FileSchemaKeepsLegacyUploadStatusConstraint(t *testing.T)
```

The first test must assert all new tables/columns/indexes listed in Task 1. The second must load `pre_p1_schema.sql`, run `OpenSQLite`, and prove the five fixture IDs remain. The third must prove `status='creating'` is rejected while `status='active', operation_phase='initializing'` succeeds.

- [ ] **Step 4: Run the migration tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/store -run 'P1File' -count=1
```

Expected: FAIL because migrations 008 and 009 do not exist.

### Task 1: Add additive operation, upload, mount, share, and ticket persistence

**Files:**
- Create: `internal/store/migrations/008_file_operations_and_uploads.sql`
- Create: `internal/store/migrations/009_share_tickets_and_identity.sql`
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 1: Create migration 008 without rebuilding legacy tables**

Add nullable columns to `upload_sessions`: `version`, `operation_phase`, `lock_token`, `lock_epoch`, `lease_expires_at`, `cleanup_pending`, `final_identity`, and `credential_generation`. Backfill `credential_generation` from `system_state` for existing rows and require the current epoch on every upload issue/acquire/put/complete/cancel/expire path. Add nullable `checksum`, `state`, and `write_token` to `upload_parts`. New application writes set all fields explicitly; legacy rows remain readable until Task 9 imports them.

Create `file_operations` with operation ID, kind, status, source/destination space/mount/path, source/destination identity JSON, manifest JSON, lock token/epoch/expiry, last error, actor account ID, timestamps, and a version. Constrain kind to `same_mount_rename|same_mount_move|delete|trash|trash_restore|cross_mount_copy|cross_mount_move` and status to the canonical state list.

Create `mount_identity_claims` with this exact key shape:

```sql
CREATE TABLE mount_identity_claims (
    mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
    claim_type TEXT NOT NULL CHECK (claim_type IN ('canonical_path', 'device_inode')),
    claim_key TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (mount_id, claim_type),
    UNIQUE (claim_type, claim_key)
);
```

Add nullable `canonical_root_path`, `identity_key`, and `mount_source_key` to `mounts`. Create `mount_claim_conflicts(mount_id, conflicting_mount_id, reason_code, details_json, detected_at)` so administrators can see why all conflicted mounts were disabled. Add indexes for pending operations, upload lease expiry/cleanup, and mount-conflict lookup.

- [ ] **Step 2: Create migration 009**

Add nullable `target_kind`, `target_identity_json`, `invalidated_at`, `invalidated_reason`, and `credential_generation` to `shares`; add nullable `credential_generation` to `share_sessions`. Backfill both from the current `system_state.credential_generation` in the same migration.

Create `share_download_tickets` with:

```text
id, secret_hash, share_id, share_session_id, generation, credential_generation,
mount_id, relative_path, etag, object_identity_json, size_bytes,
status, transfer_owner, lease_expires_at, committed_offset,
created_at, expires_at, completed_at, canceled_at
```

Require unique `secret_hash`, non-negative `size_bytes`/`committed_offset`, and the canonical ticket status values. Add `credential_generation` to `shares` and `share_sessions` as nullable columns, backfill all three share-related tables from `system_state`, and compare the stored value to the current epoch before accepting a share session, issuing/inspecting a ticket, or starting/continuing a transfer. Add indexes on `(status, expires_at)`, `(share_id, generation)`, and `(share_session_id, status)`. Foreign keys cascade when a share/session is deleted and set no secret-bearing value in any public column; explicit revoked/invalidated flags remain mandatory even when the epoch changes.

- [ ] **Step 3: Make the migration assertions GREEN**

Extend the Task 0 assertions to cover every new column, both ticket indexes, `UNIQUE(claim_type, claim_key)`, and the unchanged legacy upload status CHECK.

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/store -run 'OpenSQLite|P1File' -count=1
```

Expected: PASS; existing MCP migration assertions remain green.

### Task 2: Build descriptor-relative storage primitives

**Files:**
- Create: `internal/storage/root.go`
- Create: `internal/storage/root_linux.go`
- Create: `internal/storage/root_unix.go`
- Create: `internal/storage/root_unsupported.go`
- Create: `internal/storage/identity.go`
- Create: `internal/storage/root_test.go`
- Create: `internal/storage/root_linux_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add failing portable safety tests**

In `root_test.go`, cover normal nested open, intermediate and final symlink rejection, FIFO/socket rejection, traversal/reserved namespace rejection, target race with `RenameNoReplace`, object identity from an already-open file, file fsync plus parent fsync, and closing a root twice. Replace a checked directory with a symlink between test hooks and assert the open never reaches the outside file.

- [ ] **Step 2: Add Linux-only policy tests**

In `root_linux_test.go`, exercise `RESOLVE_BENEATH`, `RESOLVE_NO_MAGICLINKS`, `RESOLVE_NO_SYMLINKS`, and optional `RESOLVE_NO_XDEV`. If the kernel returns `ENOSYS` for `openat2`, assert the explicit safe fallback path is used; never skip to ordinary path-based `os.Open`.

- [ ] **Step 3: Run focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/storage -run 'Root|Identity|RenameNoReplace|Fsync' -count=1
```

Expected: FAIL because `Root`, `OpenRoot`, and identity/fsync helpers do not exist.

- [ ] **Step 4: Implement the portable contract and Linux backend**

Use an open directory FD as the only authority. Linux opens descendants with `unix.Openat2` and:

```go
resolve := uint64(unix.RESOLVE_BENEATH |
    unix.RESOLVE_NO_MAGICLINKS |
    unix.RESOLVE_NO_SYMLINKS)
if policy.NoCrossDevice {
    resolve |= unix.RESOLVE_NO_XDEV
}
```

Implement `RenameNoReplace` with `unix.Renameat2(..., unix.RENAME_NOREPLACE)`. Convert `EEXIST` to a stable `storage.ErrTargetExists`; return `storage.ErrUnsupported` when the platform cannot guarantee the requested primitive.

`RemoveAll` must itself recurse through already-open directory descriptors, reject symlink/special entries, and unlink children relative to their parent FD. It must never delegate a user-controlled subtree to `os.RemoveAll` or reopen a checked path by absolute name.

- [ ] **Step 5: Implement the non-Linux Unix fallback**

Walk one component at a time from a retained directory FD with `openat(O_DIRECTORY|O_NOFOLLOW)`; verify every component using `fstat`, reject special files, and compare device IDs when `NoCrossDevice` is true. Implement no-replace publication by creating a hard link at the destination and unlinking the source only after success; if link semantics are unavailable, return `ErrUnsupported`. `root_unsupported.go` must compile on unsupported targets and return `ErrUnsupported`, not a path-based substitute.

- [ ] **Step 6: Verify GREEN and race behavior**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/storage -count=1
```

Expected: PASS. No test may accept bare `os.Root` as proof of the no-symlink/no-cross-device policy.

### Task 3: Adopt the safe root in file and transfer facades

**Files:**
- Modify: `internal/files/service.go`
- Modify: `internal/files/service_test.go`
- Modify: `internal/files/trash_crossmount.go`
- Modify: `internal/files/trash_crossmount_test.go`
- Modify: `internal/transfer/service.go`
- Modify: `internal/transfer/service_test.go`

- [ ] **Step 1: Add failing facade-level race tests**

Add tests proving `ListDirectory`, `OpenFile`, `CreateDirectory`, `ValidateWritableTarget`, `ValidateShareTarget`, rename/move/delete/trash, upload part publication, and upload completion all reject an intermediate symlink swap. Add `TestMoveRejectsInvalidDestinationInsteadOfUsingRoot` for `../../x`, absolute paths, and `.omnora`.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/files ./internal/transfer -run 'Symlink|InvalidDestination|Race|Publish' -count=1
```

Expected: at least the swap and invalid-destination cases fail against `rejectSymlinkPath`, `rejectRootSymlinks`, and `mustCleanDir`.

- [ ] **Step 3: Replace path-based roots with `storage.Root`**

Change `files.Mount` to carry `NoCrossDevice bool`; have each operation open one secure root and retain the returned file descriptors until completion. Delete `openMountRoot`, `rejectSymlinkPath`, `rejectRootSymlinks`, and `mustCleanDir`. Replace `mustCleanDir` with:

```go
func cleanDirectory(relative string) (string, error) {
    cleaned, err := storage.CleanRelativePath(relative)
    if err != nil {
        return "", err
    }
    return cleaned, nil
}
```

Use `RenameNoReplace` for same-mount rename/move, trash restore, and upload final publication. Keep the current public method names until later coordinator tasks switch handlers.

- [ ] **Step 4: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/storage ./internal/files ./internal/transfer -count=1
```

Expected: PASS with no unsafe path-check-then-open helper remaining in these packages.

### Task 4: Add BEGIN IMMEDIATE and atomic mount identity claims

**Files:**
- Create: `internal/store/immediate.go`
- Create: `internal/store/immediate_test.go`
- Create: `internal/mountid/claims.go`
- Create: `internal/mountid/claims_test.go`
- Modify: `internal/mountid/mountid.go`
- Modify: `internal/server/api.go:1392`
- Modify: `internal/server/mount_admin.go:98`
- Modify: `internal/server/mount_admin_test.go`
- Modify: `internal/server/mount_allowlist_test.go`
- Modify: `cmd/omnora/main.go:29`

- [ ] **Step 1: Add failing concurrency and backfill tests**

Cover two independent DB connections concurrently claiming the same canonical path and the same device/inode; exactly one succeeds. Cover parent/child and bind-source conflicts inside one immediate transaction. For legacy backfill, insert two conflicting active mounts and assert both become `unavailable`, neither receives a claim, and both receive an administrator-readable `mount_claim_conflicts` row. A clean mount receives both claims. A reverify test must exclude only the same mount ID, never every row sharing a path.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/store ./internal/mountid ./internal/server -run 'Immediate|MountClaim|Reverify' -count=1
```

Expected: FAIL because immediate transactions and mount claims do not exist.

- [ ] **Step 3: Implement the immediate transaction wrapper**

`WithImmediate` must obtain one `*sql.Conn`, execute `BEGIN IMMEDIATE`, invoke the callback on the same connection, and run `COMMIT`; on callback/commit failure run `ROLLBACK`. It must never call `db.BeginTx` after `BEGIN IMMEDIATE` and must propagate the original callback error.

- [ ] **Step 4: Implement `mountid.ClaimStore`**

Expose:

```go
type ClaimStore struct { db *sql.DB }

type MountRegistration struct {
    ID           string
    SpaceID      string
    DisplayName  string
    RootPath     string
    Kind         string
    Mode         domain.MountMode
    IndexEnabled bool
}

func NewClaimStore(db *sql.DB) *ClaimStore
func (s *ClaimStore) Backfill(ctx context.Context) error
func (s *ClaimStore) Register(ctx context.Context, mount MountRegistration) (Identity, error)
func (s *ClaimStore) Reverify(ctx context.Context, mountID string) (Identity, error)
func (s *ClaimStore) Release(ctx context.Context, mountID string) error
```

`Register` performs an initial probe outside the transaction, then recaptures identity inside `BEGIN IMMEDIATE`, loads all non-deleted mounts, calls `CheckConflicts`, inserts the mount and both unique claims, and commits once. `Backfill` audits all exact-path, device/inode, parent/child, and bind-source conflicts before inserting any claim. `Reverify` excludes only `mountID` and replaces its claims atomically.

- [ ] **Step 5: Gate startup and route handlers**

Call `Backfill` after migrations and before `server.NewServer`, the job worker, or `listeners.Start`. A DB error or incomplete non-conflicting backfill exits startup. Route registration and re-verification delegate to `ClaimStore`; soft deletion releases claims in the same transaction that marks the mount deleted.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/store ./internal/mountid ./internal/server -run 'Immediate|MountClaim|CreateMount|Reverify|DeleteMount' -count=1
```

Expected: PASS; concurrent registrations cannot both become active.

### Task 5: Bind shares to live object identity with three-way failure classification

**Files:**
- Create: `internal/share/identity.go`
- Create: `internal/share/identity_test.go`
- Modify: `internal/share/service.go`
- Modify: `internal/share/session.go`
- Modify: `internal/share/service_test.go`
- Modify: `internal/server/api.go:1484`
- Modify: `internal/server/share_portal.go`
- Modify: `internal/server/share_portal_test.go`
- Modify: `internal/server/member_shares.go`

- [ ] **Step 1: Add failing identity lifecycle tests**

Cover file exact identity match, directory root match, same-path replacement, target deletion, `EIO`/`ESTALE`/permission/NAS-unavailable errors, and a directory child chosen for download. Assert only definitive replacement/not-found sets `invalidated_at`, `invalidated_reason='target_moved_or_replaced'`, increments generation, and revokes sessions/tickets. Transient errors fail the current request, mark the mount unavailable, and leave the share active.

Add legacy cases where automatic binding succeeds only when `shares.object_id`, `file_objects.identity_fingerprint`, and the live object agree; every other legacy row becomes fail-closed with `invalidated_reason='legacy_identity_unverifiable'`.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/share ./internal/server -run 'ShareIdentity|LegacyShare|TargetReplaced|Transient' -count=1
```

Expected: FAIL because shares currently bind only mount/path and portal requests do not compare target identity.

- [ ] **Step 3: Implement the binding service**

Expose:

```go
type TargetBinding struct {
    Kind     string
    Identity storage.ObjectIdentity
}

func CaptureTarget(root storage.Root, relativePath string) (TargetBinding, error)
func CompareTarget(root storage.Root, relativePath string, expected TargetBinding) (IdentityOutcome, error)
func (s *Service) VerifyTarget(ctx context.Context, shareID string) (IdentityOutcome, error)
func (s *Service) BackfillLegacyTargets(ctx context.Context) error
```

Classify `os.ErrNotExist` as definitive only after the mount root was opened and verified. Classify transport, permission, stale-handle, and identity-unverifiable errors as transient. `VerifyTarget` performs invalidation/revocation in one transaction and never maps a transient error to permanent invalidation.

- [ ] **Step 4: Persist identity at share creation and revalidate every use**

`createShare` opens the target once through the safe root, records `target_kind` and serialized identity with the share insert, and fails if identity capture fails. `Exchange`, `VerifySession`, `shareCurrent`, `shareChildren`, preview, and ticket creation all call the same verification service. For directory shares, verify the directory root on every request and capture the selected child identity separately when issuing its ticket.

- [ ] **Step 5: Gate startup on legacy binding**

Call `BackfillLegacyTargets` before the public listener. It may invalidate unprovable shares but must not bind the current occupant of a path without the three-way proof. Store only identity metadata, never host absolute paths, in diagnostics.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/share ./internal/server -run 'Share|Target|Legacy' -count=1
```

Expected: PASS; a temporary NAS outage cannot permanently kill a share.

### Task 6: Add the operation journal and transactional share path mutation

**Files:**
- Create: `internal/fileops/journal.go`
- Create: `internal/fileops/journal_test.go`
- Create: `internal/fileops/mutation.go`
- Create: `internal/fileops/mutation_test.go`
- Create: `internal/share/path_mutation.go`
- Create: `internal/share/path_mutation_test.go`
- Modify: `internal/server/file_ops.go:19`
- Modify: `internal/server/file_ops_test.go`
- Modify: `internal/server/loop_gaps_test.go`

- [ ] **Step 1: Add failing state/CAS tests**

Assert that every transition names the expected prior status, matching version, and matching fencing token; stale owners update zero rows. Assert DB/audit failure during prepare leaves the filesystem untouched. Inject failure after filesystem rename and prove recovery sees the durable operation. Add literal-prefix tests for `%`, `_`, and `\` so only the target share and real descendants are updated or invalidated.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/fileops ./internal/share ./internal/server -run 'Operation|PathMutation|LiteralPrefix|RenameRecovery' -count=1
```

Expected: FAIL because the journal and transactional share mutation APIs do not exist.

- [ ] **Step 3: Implement the journal contract**

Expose:

```go
type Journal struct { db *sql.DB }

type Status string

type OperationSpec struct {
    ID                 string
    Kind               string
    SourceSpaceID      string
    SourceMountID      string
    SourcePath         string
    DestinationSpaceID string
    DestinationMountID string
    DestinationPath    string
    ActorAccountID     string
}

type Operation struct {
    Spec         OperationSpec
    Status       Status
    Version      int64
    ManifestJSON string
    LastError    string
}

type Patch struct {
    SourceIdentityJSON      string
    DestinationIdentityJSON string
    ManifestJSON            string
    LastError               string
}

type Lease struct {
    OperationID string
    Token       string
    Epoch       int64
    ExpiresAt   time.Time
}

func NewJournal(db *sql.DB) *Journal
func (j *Journal) Prepare(ctx context.Context, spec OperationSpec, audit audit.Event) (Operation, error)
func (j *Journal) Acquire(ctx context.Context, operationID, owner string, ttl time.Duration) (Lease, error)
func (j *Journal) Advance(ctx context.Context, lease Lease, from, to Status, patch Patch) error
func (j *Journal) RequireRecovery(ctx context.Context, lease Lease, cause error) error
func (j *Journal) Pending(ctx context.Context, limit int) ([]Operation, error)

type Coordinator struct {
    journal *Journal
    db      *sql.DB
}

func NewCoordinator(db *sql.DB) *Coordinator
func (c *Coordinator) Rename(ctx context.Context, actor string, mount files.Mount, from, to string, event audit.Event) (string, error)
func (c *Coordinator) Move(ctx context.Context, actor string, mount files.Mount, from, toDir string, event audit.Event) (string, error)
func (c *Coordinator) Delete(ctx context.Context, actor string, mount files.Mount, relativePath string, event audit.Event) error
func (c *Coordinator) Trash(ctx context.Context, actor string, mount files.Mount, relativePath string, event audit.Event) (files.TrashItem, error)
```

`Prepare` inserts the operation, applies the planned share generation/path/invalidation mutation, revokes old share sessions/tickets, and calls `audit.Recorder.RecordTx` in one transaction. Filesystem I/O begins only after commit.

- [ ] **Step 4: Implement literal share subtree updates**

Use `escapeLike` and an explicit escape clause:

```sql
relative_path = ? OR relative_path LIKE ? ESCAPE '\'
```

For same-mount rename/move, update exact and descendant share paths, increment generation, revoke prior sessions/tickets, and keep the share active. For delete, trash, and cross-mount move, set invalidation/revocation before I/O. Failed operations remain fail closed until recovery proves whether to complete or cancel; cancellation may reactivate the share with its incremented generation but must never revive old sessions/tickets.

- [ ] **Step 5: Switch same-mount and delete handlers**

Replace direct `files.NewService().Rename/Move/Delete` plus best-effort audit/revocation with `fileops.Coordinator`. Preserve current response bodies. `renameObject` and `moveObject` keep shares active at their new path; delete/trash invalidate them. Remove `Server.revokeSharesForPath` only after every call site uses the transactional service.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/fileops ./internal/share ./internal/server -run 'Rename|Move|Delete|Operation|LiteralPrefix' -count=1
```

Expected: PASS; no high-risk file mutation ignores its audit or authorization-state error.

### Task 7: Make trash restore no-replace and crash-visible

**Files:**
- Modify: `internal/files/trash_crossmount.go:152`
- Modify: `internal/files/trash_crossmount_test.go`
- Modify: `internal/fileops/mutation.go`
- Modify: `internal/fileops/mutation_test.go`
- Modify: `internal/server/file_ops.go:192`
- Modify: `internal/server/backup_network_test.go`

- [ ] **Step 1: Add failing restore collision tests**

Create two trash items with the same original name in the same second, race a new target into place after selection, and assert restore never overwrites. The returned fallback name must include the trash ID or a cryptographically random suffix. Inject file fsync, directory fsync, and journal-finalize failures and assert a visible pending/recovery operation remains.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/files ./internal/fileops ./internal/server -run 'Trash.*Restore|Restore.*Race' -count=1
```

Expected: FAIL because `conflictSafePath` uses second-level time and restore uses overwrite-capable rename.

- [ ] **Step 3: Implement no-replace restore**

Try the original path first, then at most 100 candidates formed as `<base>.restored-<trashID>-<random><ext>`. Call `RenameNoReplace` for every attempt, fsync the restored object and parent, and remove trash metadata only after the operation reaches `published`. Keep the trash object and operation in `recovery_required` when safety cannot be proven.

- [ ] **Step 4: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/files ./internal/fileops ./internal/server -run 'Trash|Restore' -count=1
```

Expected: PASS; same-second or racing targets are preserved.

### Task 8: Replace cross-mount copy/delete with a recoverable manifest state machine

**Files:**
- Create: `internal/fileops/cross_mount.go`
- Create: `internal/fileops/cross_mount_test.go`
- Create: `internal/fileops/recovery.go`
- Create: `internal/fileops/recovery_test.go`
- Modify: `internal/files/trash_crossmount.go:263`
- Modify: `internal/files/trash_crossmount_test.go`
- Modify: `internal/server/file_ops.go:292`
- Provide a recovery callback consumed by the Catalog/API plan's `runJobMaintenance`; do not edit `internal/server/index_worker.go` in this task.
- Create: `internal/server/file_recovery_test.go`

- [ ] **Step 1: Add a failure matrix before implementation**

Cover every persisted status, crash after source staging, mid-copy crash, destination staging fsync failure, publish collision, publish success followed by source mutation through an already-open FD, unsupported symlink/FIFO/socket/device, source manifest entry added/removed/replaced, source cleanup failure, stale lease takeover, and idempotent repeated recovery. Assert no scenario deletes data that was not present in the verified manifest.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/fileops ./internal/files ./internal/server -run 'CrossMount|FileRecovery' -count=1
```

Expected: FAIL because existing `MoveAcrossMounts` copies the live path and then calls `Delete`.

- [ ] **Step 3: Implement the canonical manifest**

Persist sorted entries containing relative path, kind, object identity, mode, size, and SHA-256 checksum for regular files. Directories have no checksum; any symlink or special object aborts before publication. All names are mount-relative and `.omnora` remains excluded.

- [ ] **Step 4: Implement copy and move transitions**

For move: prepare operation and revoke affected source shares; atomically rename the source to `.omnora/operations/<operationID>/source`; capture the source manifest; copy to destination staging; fsync every file and directory; compare destination to the manifest; publish with no-replace; rewalk source staging and compare its entry set, identities, kinds, sizes, and checksums; only then remove source staging and complete.

For copy: leave the source in place, copy to destination staging, then rewalk and compare source before no-replace publication. If the source changed, retain staging under the operation ID and enter `recovery_required` without publishing.

- [ ] **Step 5: Implement recovery and maintenance integration**

`RecoverPending` acquires the operation lease and resumes only the next legal transition. When identities or manifests do not prove a safe action, it records a sanitized reason and keeps both source and destination material. Call recovery from the existing maintenance loop, but do not translate a file operation into a `jobs` row or reuse catalog checkpoints.

- [ ] **Step 6: Switch handlers and remove the old implementation**

`handleCrossMount` calls the coordinator, returns `409 cross_mount_recovery_required` with the non-secret operation ID when manual intervention is required, and reports success only after `completed`. Delete `files.Service.MoveAcrossMounts` and `CopyAcrossMounts` after all production/test call sites move.

- [ ] **Step 7: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/fileops ./internal/files ./internal/server -run 'CrossMount|FileRecovery' -count=1
```

Expected: PASS; target publication never permits a later blind `RemoveAll` of a changed source.

### Task 9: Implement DB-authoritative upload phase, lease, CAS, and immutable parts

**Files:**
- Create: `internal/transfer/store.go`
- Create: `internal/transfer/store_test.go`
- Modify: `internal/transfer/service.go`
- Modify: `internal/transfer/service_test.go`
- Create: `internal/transfer/faults_test.go`

- [ ] **Step 1: Add failing concurrency/state tests**

Use two separately constructed services against the same SQLite file. Concurrently write different data to the same part and assert exactly one current `write_token`; the losing writer cannot publish or change DB state. Race complete/cancel and assert exactly one transition wins. Cover expired leases, stale fencing epochs, missing/gapped parts, checksum mismatch, DB failure after fsync, rename failure, final fsync failure, and target collision.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/transfer -run 'Lease|CAS|Concurrent|Fault|CompleteCancel' -count=1
```

Expected: FAIL because the current service mutex and manifest are process-local authority.

- [ ] **Step 3: Implement the upload store**

Expose:

```go
type Store struct { db *sql.DB; now func() time.Time }

type StoreOption func(*Store)

type CreateSpec struct {
    ID         string
    AccountID  string
    SpaceID    string
    MountID    string
    TargetPath string
    TempDir    string
    Size       int64
    PartSize   int64
    ExpiresAt  time.Time
}

type Session struct {
    ID             string
    Status         string
    OperationPhase string
    Version        int64
    ReceivedSize   int64
}

type PartRecord struct {
    Number     int
    Size       int64
    Checksum   string
    State      string
    WriteToken string
}

type UploadLease struct {
	UploadID string
	Token    string
	Epoch    int64
	CredentialGeneration int64
	Version  int64
	Expires  time.Time
}

func NewStore(db *sql.DB, options ...StoreOption) *Store
func (s *Store) Create(ctx context.Context, spec CreateSpec) (Session, error)
func (s *Store) Acquire(ctx context.Context, uploadID, owner string, ttl time.Duration) (UploadLease, error)
func (s *Store) PutPart(ctx context.Context, lease UploadLease, part PartRecord) error
func (s *Store) BeginComplete(ctx context.Context, lease UploadLease) error
func (s *Store) Complete(ctx context.Context, lease UploadLease, identity storage.ObjectIdentity) error
func (s *Store) Cancel(ctx context.Context, uploadID, accountID string) error
func (s *Store) ExpireDue(ctx context.Context, limit int) ([]string, error)
```

All updates include `status='active'`, expected `operation_phase`, `version`, `lock_token`, `lock_epoch`, and `credential_generation = CurrentCredentialGeneration(ctx)` in the WHERE clause; affected rows must equal one. `Create` snapshots the current epoch, and every handler re-reads it before issuing or accepting an upload lease. Add tests that bump the epoch without deleting the row and prove old upload leases and sessions fail closed while explicit cancel/revoke remains observable.

- [ ] **Step 4: Publish parts without shared-name overwrite races**

Write each part to `part-<number>-<writeToken>.tmp`, hash while streaming, fsync, verify the lease/fencing token, rename to immutable `part-<number>-<writeToken>`, then transactionally make that token current in `upload_parts`. Mark the previous token `superseded`/cleanup pending. A stale file may remain for GC but can never become authoritative.

- [ ] **Step 5: Implement completion/cancel**

Complete CASes `writing → completing`, assembles only DB-current `ready` parts, verifies contiguous numbering/size/checksums, fsyncs the final staging file and parent, publishes with `RenameNoReplace`, captures final identity, then CASes to `completed`. Cancel CASes only from active phases to `canceling`, cleans safely, and writes `canceled`; if cleanup fails it leaves `cleanup_pending=1`. Any ambiguous crash moves to `status='failed', operation_phase='recovery_required'` instead of inventing a new status.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/transfer -count=1
```

Expected: PASS with two independent service instances; removing the process mutex must not change correctness.

### Task 10: Import legacy manifests and recover/garbage-collect uploads before readiness

**Files:**
- Create: `internal/transfer/importer.go`
- Create: `internal/transfer/importer_test.go`
- Create: `internal/transfer/recovery.go`
- Create: `internal/transfer/recovery_test.go`
- Provide bounded import/recovery callbacks to the Catalog/API plan; do not edit `internal/server/index_worker.go` in this task.
- Modify: `internal/server/server.go:20`
- Modify: `internal/server/server_test.go`
- Modify: `cmd/omnora/main.go:29`

- [ ] **Step 1: Add failing import/recovery tests**

Create legacy active rows plus valid, corrupt, mismatched-owner, missing-part, oversized, duplicate-part, checksum-changing, and symlinked manifests. A valid manifest imports exact parts and enters `writing`. Every unprovable row becomes `failed/recovery_required`; files move by no-replace rename to `.omnora/quarantine/uploads/<uploadID>` and are never deleted. Add restart tests for `initializing`, `completing`, `canceling`, `expiring`, cleanup pending, orphan immutable parts, and expired sessions.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/transfer ./internal/server -run 'LegacyManifest|UploadRecovery|UploadReadiness|UploadGC' -count=1
```

Expected: FAIL because startup does not import or recover upload state and expiry only updates SQL.

- [ ] **Step 3: Implement the importer**

For each active row whose `operation_phase` is NULL, open `temp_dir` under the verified mount root, decode exactly one `manifest.json`, require manifest ID/target/declared size to match the row, verify each regular part number/size/checksum, and insert `upload_parts` in one transaction. Set `system_state.upload_manifest_import_v1=complete` only after every active row is imported or fail-closed. Continue dual-writing manifests for the rollback window, but all reads after the marker use SQLite.

- [ ] **Step 4: Implement recovery and physical GC**

Resume legal phases with a fresh lease. Expiry uses `writing → expiring → expired` and removes/quarantines files before clearing cleanup pending. GC removes only immutable part tokens proven superseded and directories belonging to terminal sessions; unknown directories move to quarantine. It returns per-upload errors and never silently treats SQL-only expiry as cleanup success.

- [ ] **Step 5: Gate startup/readiness**

After mount claim backfill and share legacy binding, run upload import and one recovery pass before constructing the public handler. Store a startup error on `Server`; `/readyz` returns 503 while import/recovery is incomplete. Export bounded recovery/GC callbacks; the Catalog/API plan's single `runJobMaintenance` owner invokes them after lease reclaim and before catalog enqueue. The ordered maintenance sequence is job lease reclaim → file-operation recovery → upload import/recovery/GC → catalog enqueue; no second maintenance loop is allowed.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/transfer ./internal/server -run 'LegacyManifest|UploadRecovery|UploadReadiness|UploadGC' -count=1
```

Expected: PASS; no legacy active upload is silently interpreted from a replaced path object.

### Task 11: Switch upload HTTP handlers to the state machine

**Files:**
- Modify: `internal/server/api.go:788`
- Modify: `internal/server/release_gate_test.go`
- Create: `internal/server/upload_state_test.go`
- Modify: `web/src/api.ts:104`
- Modify: `web/src/member/uploadQueue.ts`
- Modify: `web/src/member/uploadQueue.test.ts`

- [ ] **Step 1: Add failing HTTP concurrency and failure tests**

Cover two simultaneous PUTs for one part, complete/cancel race, ACL downgrade before part and before complete, mount made read-only/unavailable, target created during completion, expired session, recovery-required response, and DB/rename/fsync fault injection. Assert handlers never return success when the authoritative DB write failed.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/server -run 'UploadState|Upload.*Race|Upload.*Failure' -count=1
```

Expected: FAIL because current handlers ignore part/status SQL errors and instantiate manifest-authoritative services.

- [ ] **Step 3: Replace handler orchestration**

`createUpload` inserts `active/initializing`, creates and fsyncs the session directory, then advances to `writing`. `getUpload` reads DB session/parts only. `uploadPart`, `completeUpload`, and `cancelUpload` reacquire live account/ACL/mount identity and use the transfer store/service CAS APIs. Map stale lease/CAS loss to 409 `upload_conflict`, recovery required to 409 `upload_recovery_required`, and transient mount unavailability to the existing mount error.

- [ ] **Step 4: Normalize Web part fields without compatibility guessing**

Change `UploadSessionPayload.parts` to `{ number: number; size: number; checksum?: string }[]`; update `uploadQueue.ts` to read the canonical lowercase fields. Preserve retry/cancel UX and treat `upload_recovery_required` as a terminal visible error, not an automatic blind retry.

- [ ] **Step 5: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/transfer ./internal/server -run 'Upload' -count=1
(cd web && npm test -- --run src/member/uploadQueue.test.ts)
```

Expected: PASS; upload HTTP correctness no longer depends on a shared in-memory mutex.

### Task 12: Implement one-count download tickets and replay-safe streaming

**Files:**
- Create: `internal/share/tickets.go`
- Create: `internal/share/tickets_test.go`
- Modify: `internal/share/session.go`
- Modify: `internal/server/share_portal.go`
- Create: `internal/server/share_ticket_test.go`
- Modify: `internal/server/api.go` through the identity-owned `registerRoute` helper

- [ ] **Step 1: Add the ticket behavior matrix**

Cover atomic last remaining download, two concurrent full GETs, replay after completion, `bytes=0-` replay, continuation exactly at committed offset, wrong offset, second file, changed ETag/identity, generation change, revoked session/share, expired ticket/lease, owner takeover after lease expiry, HEAD without consumption, and I/O interruption with committed progress. At every instant at most one owner may stream.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/share ./internal/server -run 'DownloadTicket|TicketReplay|TicketRange' -count=1
```

Expected: FAIL because downloads currently increment on every GET/Range and have no ticket state.

- [ ] **Step 3: Implement ticket issuance and claim CAS**

Expose:

```go
type IssuedTicket struct {
    ID        string
    Secret    string
    ExpiresAt time.Time
}

type StreamClaim struct {
    TicketID       string
    Owner          string
    Offset         int64
    Size           int64
    RelativePath   string
    ETag           string
    ObjectIdentity storage.ObjectIdentity
}

func (s *Service) IssueDownloadTicket(ctx context.Context, session SessionPrincipal, relativePath string) (IssuedTicket, error)
func (s *Service) InspectDownloadTicket(ctx context.Context, ticketID, secret string) (StreamClaim, error)
func (s *Service) ClaimDownload(ctx context.Context, ticketID, secret, owner string, requestedOffset *int64) (StreamClaim, error)
func (s *Service) CommitProgress(ctx context.Context, claim StreamClaim, bytesWritten int64, complete bool) error
func (s *Service) CancelTicket(ctx context.Context, ticketID, reason string) error
```

Issuance verifies share/session/generation/credential-generation/capability/live target, opens the selected file, captures identity/ETag/size, atomically reserves one download count, and inserts the ticket in one transaction. `InspectDownloadTicket`, `ClaimDownload`, and `CommitProgress` compare the ticket, share, session, and current system credential generations on every request. Network interruption never refunds the count; explicit ticket/session/share revocation remains checked separately.

- [ ] **Step 4: Implement streaming ownership**

The first GET without Range is allowed only at offset zero. A Range request must be a single open-ended range beginning exactly at `committed_offset`; reject suffix, multiple, skipped, or rewind ranges. CAS `issued → streaming` or reclaim an expired streaming lease; renew during long transfers. Advance offset only by bytes successfully written. Exact EOF sets `completed`; all later GETs and concurrent owners fail. HEAD validates secret/binding and returns metadata without changing state.

- [ ] **Step 5: Add the approved HTTP handshake**

Add handlers and route definitions through the identity-owned `registerRoute` helper; never call `s.mux.Handle` directly. Register:

```text
POST /api/v1/share/download-tickets   (Group: RouteGroupShare, AuthShareCookie, share CSRF, operationId createShareDownloadTicket)
GET /api/v1/share/downloads/{ticketId} (Group: RouteGroupShare, AuthShareCapability, no CSRF, operationId downloadShareTicket)
HEAD /api/v1/share/downloads/{ticketId} (Group: RouteGroupShare, AuthShareCapability, no CSRF, operationId headShareTicket)
GET /api/v1/share/preview              (Group: RouteGroupShare, AuthShareCookie, no mutation CSRF, operationId previewShare)
```

The POST body contains only share-relative `path`. Respond 201 with `ticketId`, `downloadUrl`, and `expiresAt`. Set the secret in a fixed-name HttpOnly, SameSite=Strict cookie whose Path is exactly `/api/v1/share/downloads/{ticketId}`; set Secure according to the shared HTTP security policy. The URL contains only the non-secret ticket ID. Preview checks `allow_preview`, never increments downloads, and never accepts a download ticket.

- [ ] **Step 6: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/share ./internal/server -run 'Share|DownloadTicket|Preview' -count=1
```

Expected: PASS; a ticket cannot create a second full transfer or change files.

### Task 13: Switch the share portal and synchronize the public contract

**Files:**
- Modify: `web/src/api.ts:940` through the shared request wrapper's explicit `share` CSRF/auth context
- Create: `web/src/api.share-ticket.test.ts`
- Modify: `web/src/SharePortalApp.tsx`
- Verify only: `openapi/omnora.v1.yaml`, `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/api/README.md`
- Modify: `docs/security/security-model.md`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/design/architecture.md`
- Modify: `docs/verification/acceptance-criteria.md`
- Verify only: `scripts/verification/verify-api-docs.sh` (the Catalog/API plan owns the shared contract gate)

- [ ] **Step 1: Add failing Web API tests**

Stub `fetch` and assert `createShareDownloadTicket(path)` sends POST JSON, receives no secret in the response/URL, and `sharePortalPreviewURL(path)` uses `/api/v1/share/preview`. Assert the UI calls ticket creation before navigation and no longer builds `/api/v1/share/download?path=...` links.

- [ ] **Step 2: Run Web tests and verify RED**

```bash
(cd web && npm test -- --run src/api.share-ticket.test.ts)
```

Expected: FAIL because the ticket helpers and preview URL do not exist.

- [ ] **Step 3: Implement the Web contract**

Add a generated-schema alias only after the Catalog/API plan has generated the single canonical module; do not declare a second wire type in this plan:

```ts
export type ShareDownloadTicket = components['schemas']['ShareDownloadTicketResponse'];

export function createShareDownloadTicket(path: string, signal?: AbortSignal) {
  return requestJson<ShareDownloadTicket>('/api/v1/share/download-tickets', {
    method: 'POST',
    body: JSON.stringify({ path }),
    authContext: 'share',
    signal,
  });
}

export function sharePortalPreviewURL(path: string) {
  const params = new URLSearchParams({ path });
  return `/api/v1/share/preview?${params.toString()}`;
}
```

Replace download anchors with buttons whose click awaits ticket creation and then calls `window.location.assign(ticket.downloadUrl)`. Keep preview anchors on the preview endpoint. Disable the clicked button while issuing, expose a generic retryable error, and never store a ticket secret in JavaScript.

- [ ] **Step 4: Hand the ticket contract to the Catalog/API owner**

Provide the canonical four-route list and schema requirements to the Catalog/API plan: the ticket-issue POST, ticket GET, ticket HEAD, and preview GET, named request/response schemas, 201 ticket response, HttpOnly capability cookie, 206/416 behavior, replay/conflict errors, and `allow_preview` versus `allow_download`. That plan removes the active old `/share/download` operation, updates the source OpenAPI, and runs the sync script; this plan must not edit the embedded copy or contract gate.

```bash
npm --prefix web run check:api-types
```

- [ ] **Step 5: Update durable design/acceptance documentation**

Document same-mount share path preservation, cross-mount/delete invalidation, identity outcome taxonomy, ticket one-count/no-refund semantics, exact Range continuation rule, upload DB authority/import/quarantine, operation recovery, mount claims/backfill, and unsupported-platform fail-closed behavior. Add the ticket assertions to the Catalog/API contract task's shared gate rather than editing that script here.

- [ ] **Step 6: Verify GREEN**

```bash
(cd web && npm test -- --run)
(cd web && npm run build)
scripts/verification/verify-api-docs.sh
git diff --check
```

Expected: all commands exit 0; source and embedded OpenAPI are byte-identical.

### Task 14: Run failure-injection, upgrade, and full release gates

**Files:**
- Create: `internal/server/file_reliability_integration_test.go`
- Modify: `internal/server/release_gate_test.go`
- Modify: `internal/store/sqlite_test.go`
- Modify: `docs/verification/acceptance-criteria.md`

- [ ] **Step 1: Add the integrated release scenarios**

Create tests for: two services writing the same upload part; complete/cancel; DB, rename, file-fsync and directory-fsync failures; same-second trash restore; every cross-mount crash state; source mutation through an open FD; same-mount share preservation; cross-mount/delete invalidation; transient NAS outage; `%`, `_`, and `\` subtree names; concurrent mount exact/parent-child/inode/bind claims; ticket concurrent full GET/replay/continuation; and startup upgrade of legacy uploads/shares.

- [ ] **Step 2: Run focused packages with the race detector**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test -race ./internal/storage ./internal/mountid ./internal/files ./internal/fileops ./internal/transfer ./internal/share ./internal/server ./internal/store -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the full repository gates**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./... -count=1
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go vet ./...
(cd web && npm test -- --run)
(cd web && npm run build)
scripts/verification/verify-api-docs.sh
scripts/verification/verify-scaffolding.sh
scripts/verification/test-docker-entrypoint.sh
git diff --check
```

Expected: every command exits 0. If a bind-dependent test fails only because the managed sandbox forbids `127.0.0.1:0`, rerun that exact command with the required permission and report the environment distinction; do not mark the release green from static checks alone.

- [ ] **Step 4: Perform environment-dependent acceptance**

On the target Linux/NAS environment, verify `openat2` flags, no-cross-device policy, NAS disconnect/reconnect classification, bind-source conflicts, large file fixed-memory transfer, HTTPS Secure ticket cookie, reverse-proxy Range forwarding, browser preview, interrupted download continuation, process kill at each operation/upload phase, and public share replay rejection. Capture sanitized command/results in the acceptance record; do not include secrets or host paths.

## Parallel boundary

After Tasks 0-3 are green:

- Task 4 (mount claims), Task 5 (share identity), and Task 9 (upload store) may run in parallel because they own different packages and schema portions.
- Task 6 waits for Task 5 and the identity plan's `RecordTx` contract.
- Task 7 waits for Task 6.
- Task 8 waits for Tasks 4, 6, and 7.
- Task 10 waits for Tasks 4 and 9.
- Task 11 waits for Tasks 9 and 10.
- Task 12 waits for Task 5, migration 009, and the identity plan's `AuthShareCapability`/`registerRoute` contract.
- Task 13 waits for Tasks 11 and 12 plus the Catalog/API plan's OpenAPI source and generated-type tasks; it must not create a second generated alias before that output exists.
- Task 8 and Task 10 wait for the Catalog/API plan's maintenance owner to wire callbacks in this fixed order: job lease reclaim → file-operation recovery → upload import/recovery/GC → catalog enqueue. Neither task may edit or duplicate `runJobMaintenance`.
- Task 14 is always last.

When parallel work finishes, run the union of each package's focused tests before starting the next dependent task. Resolve migration and shared-file conflicts serially; never let two workers edit `internal/server/api.go`, `internal/server/server.go`, `cmd/omnora/main.go`, or `internal/store/sqlite_test.go` concurrently.

## Rollback and recovery rules

- Migrations 008/009 are additive and remain applied during rollback. A legacy binary may read old columns, but it cannot safely operate on pending `file_operations`, upload phases, or tickets. Before binary rollback, disable share, REST, and MCP route groups at the proxy and keep the service unavailable until the new-version recovery tool reports no unsafe pending state.
- Never roll back by deleting `file_operations`, upload rows, ticket rows, staging directories, or quarantine directories. Resume or manually resolve them with the new-version recovery code.
- Revoked sessions/tickets are never reactivated. A same-mount operation that is safely canceled may keep the share active only at a new generation, requiring a fresh share session.
- If cross-mount publication completed but source verification failed, preserve destination, source staging, manifest, and operation row. Human resolution chooses which copy to retain after comparing them; automation must not guess.
- If legacy upload/share import cannot prove identity, fail closed and preserve bytes in quarantine. Owners may recreate the capability after review.

## Self-review checklist

Before handing this plan to an executor, confirm:

- [ ] Storage primitives cover Linux `openat2`, safe fallback, no-replace, fsync, and object identity; bare `os.Root` is never treated as sufficient.
- [ ] Upload status retains the legacy CHECK while phases, CAS, active-manifest import, recovery, and physical GC are covered.
- [ ] Trash and cross-mount flows never delete changed or unsupported source objects.
- [ ] Mount claim backfill disables every member of a legacy conflict and startup waits for clean backfill.
- [ ] Share validation separates definitive change from transient unavailability and preserves same-mount moves.
- [ ] Download tickets reserve exactly one count, keep secrets out of URLs/JavaScript/logs, reject replay, and allow only exact-offset serial continuation.
- [ ] DB, rename, file fsync, directory fsync, restart, and concurrent-owner failures have explicit tests.
- [ ] `%`, `_`, and `\` path prefixes are verified as literals.
- [ ] OpenAPI, embedded OpenAPI, Web behavior, durable docs, and acceptance criteria change together.
- [ ] No source-code or documentation cleanup outside this plan's file map was added.
