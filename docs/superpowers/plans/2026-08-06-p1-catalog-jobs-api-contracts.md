# P1 Catalog, Jobs, and API Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make catalog scans resumable and deletion-safe, make background jobs lease-owned and fenced, and make the runtime route/JSON surface mechanically agree with OpenAPI and generated TypeScript types.

**Architecture:** Catalog traversal becomes a versioned depth-first stack checkpoint tied to one mount identity and one scan epoch. The worker reads a filesystem batch outside SQLite, then commits entries plus the fenced job checkpoint in one transaction; only a completed traversal can commit tombstones plus job completion. HTTP registration moves to one `RouteDefinition` manifest, which becomes the source for route/OpenAPI drift tests, while named Go DTOs and generated TypeScript schema types preserve the current runtime JSON as the compatibility baseline.

**Tech Stack:** Go 1.26, `database/sql`, modernc SQLite, descriptor-relative storage primitives, `net/http` `ServeMux`, OpenAPI 3.1, `github.com/getkin/kin-openapi`, React 19, TypeScript 5.7, Vitest, `openapi-typescript`, POSIX shell verification scripts.

---

## Execution rules and hard dependencies

- Implement Tasks 0-7 before Tasks 8-12. Catalog/jobs work and HTTP-contract work touch different production files until the worker integration task, but each task itself is serial and RED → GREEN.
- Do not create, amend, or push a Git commit while executing this plan. At the end of each task, inspect the scoped diff; commit only after the user explicitly authorizes it.
- Preserve runtime JSON field names, status codes, and optionality unless this plan explicitly corrects an already-proven OpenAPI mismatch. The Web client is the compatibility baseline.
- Do not generate Go server handlers from OpenAPI. Go handlers remain handwritten; only TypeScript schema types are generated.
- Do not copy the descriptor-relative walker into `internal/catalog`. Task 5 consumes the shared `internal/storage` primitive delivered by the P1 file/upload/mount plan. That primitive must reject symlinks, magic links, special files, mount escapes, and cross-filesystem traversal when mount policy says `no_xdev`, and it must return metadata obtained from the opened object (`fstat`), not a pre-open path lookup.
- Task 8 requires the P1 identity/HTTP plan's `registerRoute(RouteDefinition)` helper and wrapper composition. `AuthMode`, `RouteDefinition`, `AllowedSessionPurposes`, `EnrollmentBypassesReauth`, and the manifest are owned by the identity/HTTP plan; this plan consumes those types and must not declare duplicates.
- `internal/store/migrations/006_standard_mcp.sql` is currently present but not tracked. It is a hard prerequisite, not an implicit dependency. Task 0 must stop execution if it has not landed in the target branch.
- Migration numbering is resolved from the target branch at execution time. If tracked migrations `006_standard_mcp.sql` through `009_share_tickets_and_identity.sql` exist and no later migration claims `010`, use `010_catalog_jobs.sql` as approved in the design. If any prerequisite differs, stop and rebase the coordinated P1 migration allocation before editing SQL; do not silently choose another number.
- A legacy checkpoint such as `{"cursor":"a/file-199"}` is not converted. The worker starts a new scan ID at root, leaves old rows visible, and tombstones only after the new traversal completes.
- A lease loss is a normal fencing outcome: return `jobs.ErrLeaseLost`, cancel filesystem work, and do not call `Fail` with an obsolete token.

## File structure and ownership

| Path | Responsibility |
| --- | --- |
| `internal/store/migrations/010_catalog_jobs.sql` | Add nullable scan-epoch and job lease/fencing columns plus claim indexes. |
| `internal/store/sqlite_test.go` | Upgrade and schema assertions for the additive migration. |
| `internal/jobs/jobs.go` | Atomic lease claim, heartbeat, fenced state transitions, and expired-lease recovery. |
| `internal/jobs/jobs_test.go` | Contention, token mismatch, expiry, retry, checkpoint preservation, and pause fencing tests. |
| `internal/catalog/checkpoint.go` | Versioned DFS checkpoint encoding/decoding and legacy-checkpoint detection. |
| `internal/catalog/checkpoint_test.go` | Checkpoint round-trip, validation, and old-cursor restart policy. |
| `internal/catalog/walker.go` | Small catalog-facing traversal interface and deterministic DFS state machine. |
| `internal/catalog/service.go` | Batch preparation, transactional upsert, and completed-scan reconciliation. |
| `internal/catalog/service_test.go` | Exact 200-item DFS regression, scan epochs, interruption, deletion/move, and identity drift. |
| `internal/server/index_worker.go` | Lease heartbeat lifecycle and transaction coordinator for catalog batch/finalization. |
| `internal/server/index_worker_test.go` | Crash boundaries, rollback, pause/lease loss, recovery, and scheduler behavior. |
| `internal/server/routes.go` | Consume the identity-owned `RouteDefinition` manifest; add only Catalog/API route definitions through its helper. |
| `internal/server/routes_test.go` | Extend the identity-owned manifest tests with Catalog/API, health/document, and ticket route parity. |
| `internal/server/server.go` | Register health/ready and product routes through the manifest helper. |
| `internal/server/api.go` | Replace the hand-maintained route list and anonymous/map payloads at selected handlers. |
| `internal/server/openapi.go` | Register OpenAPI document routes through `RouteDefinition`. |
| `internal/server/http_dto.go` | Named request/response DTOs that encode the current runtime JSON contract. |
| `internal/server/http_dto_test.go` | Exact JSON shape/optionality tests for the first contract slice. |
| `internal/server/openapi_contract_test.go` | Manifest-to-OpenAPI parity and real `httptest` response schema validation. |
| `openapi/omnora.v1.yaml` | Runtime-accurate paths, operation IDs, status variants, and named schemas. |
| `internal/server/openapi_assets/omnora.v1.yaml` | Embedded byte-for-byte copy produced only by the sync script. |
| `scripts/verification/verify-api-docs.sh` | Invoke route/schema/type gates in addition to embedded-copy checks. |
| `scripts/verification/verify-openapi-types.sh` | Regenerate TypeScript schema types in a temporary directory and compare. |
| `web/package.json`, `web/package-lock.json` | Pin `openapi-typescript` and expose generation/check scripts. |
| `web/src/generated/omnora-api.ts` | Generated OpenAPI schema types; never edit by hand. |
| `web/src/api.ts` | Request wrappers and UI adapters that alias generated wire types. |
| `web/src/member/types.ts`, `web/src/types.ts` | UI-only view models; wire fields move to generated aliases. |
| `web/src/api.contract.test.ts` | Compile/runtime checks for response normalization and union status handling. |

## Transaction and state invariants

1. One catalog job owns a random `claim_token` until requeue, pause, completion, failure, or lease reclamation clears it.
2. `SaveCheckpoint`, `Requeue`, `Complete`, `Fail`, and `Heartbeat` match job ID, `status='running'`, claim token, and an unexpired lease. Affected rows other than one return `jobs.ErrLeaseLost`.
3. A successful nonterminal catalog batch commits entry upserts and the next checkpoint in the same SQLite transaction.
4. A successful terminal batch commits entry upserts, unseen-entry tombstones, and job completion in the same SQLite transaction.
5. Filesystem reads and password/identity checks occur outside the write transaction. The transaction contains bounded SQL only.
6. Interrupted, paused, identity-drifted, crashed, or lease-lost scans never execute reconciliation.
7. All entries observed during one full traversal use the same `scan_id`; `last_seen_scan_id` changes only when that entry is upserted.
8. Running rows with expired leases do not block a new claim. Maintenance preserves their checkpoints, increments attempts once, and either queues them or marks them failed at `max_attempts`.

### Task 0: Freeze prerequisites and prove the baseline

**Files:**
- Read: `docs/superpowers/specs/2026-08-06-p1-remediation-design.md`
- Read: `internal/store/migrations/`
- Read: `internal/storage/`
- Read: `internal/server/http_security.go`
- Read: `go.mod`
- Read: `web/package.json`

- [ ] **Step 1: Verify the coordinated migration prerequisite**

Run:

```bash
git ls-files internal/store/migrations
git status --short internal/store/migrations
```

Expected before implementation: `internal/store/migrations/006_standard_mcp.sql`, `007_identity_security.sql`, `008_file_operations_and_uploads.sql`, and `009_share_tickets_and_identity.sql` appear in `git ls-files`, and migration number `010` is not already tracked. If any prerequisite is only shown by `git status` as untracked, stop this plan until the owning migration plan is merged.

- [ ] **Step 2: Verify the shared safe-walker contract**

Run:

```bash
rg -n 'ReadDirectory|OpenRoot|RESOLVE_BENEATH|NO_MAGICLINKS|NO_SYMLINKS|NO_XDEV|O_NOFOLLOW' internal/storage internal/files
```

Expected before Task 5: the shared implementation and tests are present. If absent, Tasks 1-4 may proceed, but Task 5 and all later catalog integration tasks remain blocked.

- [ ] **Step 3: Verify the route-auth dependency**

Run:

```bash
rg -n 'registerRoute|AllowedHosts|AllowedOrigins|TrustedProxyCIDRs|AuthMode' internal/server internal/config
```

Expected before Task 8: `registerRoute(RouteDefinition)` and the identity-owned `AuthMode`, `AllowedSessionPurposes`, `EnrollmentBypassesReauth`, and manifest validation exist. Do not add a pass-through authentication wrapper or duplicate route table.

- [ ] **Step 4: Record a clean baseline without changing files**

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog ./internal/jobs ./internal/server ./internal/store -count=1
npm --prefix web test -- --run
npm --prefix web run build
scripts/verification/verify-api-docs.sh
```

Expected: record every pre-existing failure verbatim. New work must not relabel an existing environment/dependency failure as a regression.

### Task 1: Add the catalog epoch and job lease schema

**Files:**
- Create: `internal/store/migrations/010_catalog_jobs.sql`
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 1: Add failing migrated-schema assertions**

Add `TestCatalogJobsMigrationAddsEpochAndLeaseColumns` to `internal/store/sqlite_test.go`. Open a database through `store.OpenSQLite`, use `PRAGMA table_info`, and assert these exact nullable columns:

```go
wantCatalog := map[string]bool{"last_seen_scan_id": true}
wantJobs := map[string]bool{
	"claim_token":     true,
	"lease_expires_at": true,
	"heartbeat_at":     true,
}
```

Also assert indexes named `catalog_entries_scan_epoch_idx` and `jobs_lease_claim_idx` exist in `sqlite_master`.

- [ ] **Step 2: Run the migration test and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/store -run TestCatalogJobsMigrationAddsEpochAndLeaseColumns -count=1
```

Expected: FAIL because the three columns and two indexes do not exist.

- [ ] **Step 3: Create the additive migration**

Create `internal/store/migrations/010_catalog_jobs.sql` with exactly additive statements:

```sql
ALTER TABLE catalog_entries ADD COLUMN last_seen_scan_id TEXT;

CREATE INDEX catalog_entries_scan_epoch_idx
ON catalog_entries(mount_id, last_seen_scan_id, deleted_at);

ALTER TABLE jobs ADD COLUMN claim_token TEXT;
ALTER TABLE jobs ADD COLUMN lease_expires_at TEXT;
ALTER TABLE jobs ADD COLUMN heartbeat_at TEXT;

CREATE INDEX jobs_lease_claim_idx
ON jobs(status, lease_expires_at, priority, created_at, id);
```

Do not rebuild either table and do not backfill a fake scan ID. Existing catalog rows stay visible until a new full scan finishes.

- [ ] **Step 4: Add an upgrade-from-009 test**

Extend the migration test to copy/open a database at the immediately previous tracked migration, insert one catalog row and one queued job, then run the migrator. Assert the old values survive and all new fields are `NULL`.

- [ ] **Step 5: Run store tests and inspect only the scoped diff**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/store -count=1
git diff -- internal/store/migrations/010_catalog_jobs.sql internal/store/sqlite_test.go
```

Expected: tests pass; the SQL contains no `DROP`, `DELETE`, or table rebuild.

### Task 2: Make job claims atomic leases

**Files:**
- Modify: `internal/jobs/jobs.go`
- Modify: `internal/jobs/jobs_test.go`

- [ ] **Step 1: Expand the test schema and add deterministic time**

In `newJobsDB`, add the three nullable columns from Task 1. In tests, set `store.now` to a captured UTC time so lease boundaries are exact:

```go
now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
store := NewStore(db)
store.now = func() time.Time { return now }
```

- [ ] **Step 2: Add failing atomic-claim tests**

Add these tests:

```go
func TestClaimIssuesUniqueTokenAndLease(t *testing.T)
func TestClaimIgnoresExpiredRunningLease(t *testing.T)
func TestClaimBlocksOnUnexpiredRunningLease(t *testing.T)
func TestConcurrentClaimReturnsOneOwner(t *testing.T)
func TestClaimByIDNeverSubstitutesAnotherJob(t *testing.T)
```

`TestConcurrentClaimReturnsOneOwner` must use a file-backed SQLite database, two `*sql.DB` handles, a start barrier, and two workers. Assert exactly one non-nil claim, its token is nonempty, and only one unexpired running row exists.

- [ ] **Step 3: Run the claim tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/jobs -run 'Test(Claim|ConcurrentClaim)' -count=1
```

Expected: FAIL because `ClaimOptions`, `ClaimToken`, and lease timestamps do not exist.

- [ ] **Step 4: Add the lease model**

Add these exact declarations to `internal/jobs/jobs.go`:

```go
var ErrLeaseLost = errors.New("job lease lost")

type ClaimOptions struct {
	WorkerID     string
	LeaseDuration time.Duration
}

type Job struct {
	ID             string
	Kind           string
	Priority       int
	Status         Status
	PayloadJSON    string
	CheckpointJSON string
	Attempts       int
	MaxAttempts    int
	ClaimedAt      sql.NullString
	ClaimedBy      sql.NullString
	ClaimToken     sql.NullString
	LeaseExpiresAt sql.NullString
	HeartbeatAt    sql.NullString
	LastError      sql.NullString
	CreatedAt      string
	UpdatedAt      string
	CompletedAt    sql.NullString
}

func normalizeLeaseDuration(value time.Duration) time.Duration {
	if value <= 0 {
		return 30 * time.Second
	}
	return value
}
```

Reuse `newID()` to generate a fresh claim token for every claim, but store it without the `job_` prefix by adding a private `newClaimToken()` backed by `crypto/rand`.

- [ ] **Step 5: Replace select-then-update with one `UPDATE ... RETURNING`**

Change `Claim` to accept `ClaimOptions` and use one statement:

```sql
UPDATE jobs
SET status = 'running', claimed_at = ?, claimed_by = ?, claim_token = ?,
    lease_expires_at = ?, heartbeat_at = ?, updated_at = ?
WHERE id = (
    SELECT id FROM jobs
    WHERE status = 'queued'
    ORDER BY priority ASC, created_at ASC, id ASC
    LIMIT 1
)
AND status = 'queued'
AND NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE status = 'running' AND lease_expires_at > ?
)
RETURNING id, kind, priority, status, payload_json, checkpoint_json,
          attempts, max_attempts, claimed_at, claimed_by, claim_token,
          lease_expires_at, heartbeat_at, last_error, created_at,
          updated_at, completed_at;
```

Use the same atomic form for `ClaimByID`, with `id = ?` rather than the priority subquery. Treat `sql.ErrNoRows` as no claim.

- [ ] **Step 6: Update all scan/row helpers and run tests**

Update `Get`, `getTx`, and `getRow` so column order exactly matches the `Job` declaration. Then run:

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/jobs -count=1
```

Expected: all job tests pass and the race test consistently observes one owner.

### Task 3: Fence every owner transition and reclaim expired leases

**Files:**
- Modify: `internal/jobs/jobs.go`
- Modify: `internal/jobs/jobs_test.go`

- [ ] **Step 1: Add failing fencing and recovery tests**

Add the following table-driven cases:

```go
func TestOwnerMutationsRejectWrongOrExpiredClaimToken(t *testing.T)
func TestPauseClearsLeaseAndFencesOldWorker(t *testing.T)
func TestReclaimExpiredPreservesCheckpointAndRetries(t *testing.T)
func TestReclaimExpiredFailsAtMaxAttempts(t *testing.T)
func TestSuccessfulRequeueDoesNotIncrementAttempts(t *testing.T)
```

For every owner mutation, assert a wrong token changes no columns and returns `ErrLeaseLost`. Advance `store.now` to exactly `lease_expires_at`; equality counts as expired.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/jobs -run 'Test(Owner|Pause|Reclaim|SuccessfulRequeue)' -count=1
```

Expected: FAIL because transitions do not require ownership and no reclaim API exists.

- [ ] **Step 3: Add the shared affected-row guard**

Add:

```go
func requireOwnedRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLeaseLost
	}
	return nil
}
```

- [ ] **Step 4: Fence checkpoint, heartbeat, requeue, completion, and failure**

Use these public signatures:

```go
func (s Store) Heartbeat(ctx context.Context, id, claimToken string, leaseDuration time.Duration) error
func (s Store) SaveCheckpoint(ctx context.Context, id, claimToken, checkpointJSON string) error
func (s Store) Requeue(ctx context.Context, id, claimToken, checkpointJSON string) error
func (s Store) Complete(ctx context.Context, id, claimToken string) error
func (s Store) Fail(ctx context.Context, id, claimToken string, cause error) error
```

Every SQL statement must end with:

```sql
WHERE id = ?
  AND status = 'running'
  AND claim_token = ?
  AND lease_expires_at > ?
```

`Heartbeat` updates `heartbeat_at`, `lease_expires_at`, and `updated_at`. Requeue/complete/fail clear `claimed_at`, `claimed_by`, `claim_token`, `lease_expires_at`, and `heartbeat_at`. `Fail` increments attempts once and queues or fails according to `max_attempts`; successful requeue never increments attempts.

- [ ] **Step 5: Add transaction variants for worker atomicity**

Define a private executor and these exact methods:

```go
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s Store) RequeueTx(ctx context.Context, tx *sql.Tx, id, claimToken, checkpointJSON string) error
func (s Store) CompleteTx(ctx context.Context, tx *sql.Tx, id, claimToken string) error
```

The non-transactional `Requeue` and `Complete` begin a transaction, call the corresponding `Tx` method, and commit. Keep the fenced SQL in one private implementation so the two paths cannot drift.

- [ ] **Step 6: Implement expired-lease recovery**

Add:

```go
type ReclaimResult struct {
	Requeued int
	Failed   int
}

func (s Store) ReclaimExpired(ctx context.Context) (ReclaimResult, error)
```

Within one transaction, select rows with `status='running' AND lease_expires_at <= now`, then update each by matching both `id` and the selected `claim_token`. Preserve `checkpoint_json`, increment attempts once, set `last_error='job lease expired'`, clear lease ownership, and set status to `queued` or `failed`. Return counts only after commit.

- [ ] **Step 7: Run all job tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/jobs -count=1
```

Expected: all tests pass; no owner mutation can succeed with a stale token.

### Task 4: Encode a versioned DFS checkpoint and fix the 200-item traversal bug

**Files:**
- Create: `internal/catalog/checkpoint.go`
- Create: `internal/catalog/checkpoint_test.go`
- Create: `internal/catalog/walker.go`
- Modify: `internal/catalog/service.go`
- Modify: `internal/catalog/service_test.go`

- [ ] **Step 1: Add checkpoint round-trip and rejection tests**

Use this contract:

```go
const ScanCheckpointVersion = 1

var (
	ErrInvalidCheckpoint = errors.New("invalid catalog checkpoint")
	ErrLegacyCheckpoint  = errors.New("legacy catalog checkpoint")
	ErrMountIdentityDrift = errors.New("mount identity changed during scan")
)

type ScanFrame struct {
	Directory string `json:"directory"`
	AfterName string `json:"afterName,omitempty"`
}

type ScanCheckpoint struct {
	Version       int         `json:"version"`
	ScanID        string      `json:"scanId"`
	MountIdentity string      `json:"mountIdentity"`
	Frames        []ScanFrame `json:"frames"`
}
```

Test valid round-trip; empty input; malformed JSON; wrong version; empty scan ID; empty identity; absolute/`..` frame directories; duplicate root; and legacy `{"cursor":"a/198"}` returning `ErrLegacyCheckpoint`.

- [ ] **Step 2: Run checkpoint tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog -run 'TestScanCheckpoint' -count=1
```

Expected: FAIL because checkpoint types and codecs are absent.

- [ ] **Step 3: Implement strict checkpoint codecs**

Add:

```go
func NewScanCheckpoint(scanID, mountIdentity string) ScanCheckpoint {
	return ScanCheckpoint{
		Version: ScanCheckpointVersion,
		ScanID: scanID,
		MountIdentity: mountIdentity,
		Frames: []ScanFrame{{Directory: "."}},
	}
}

func EncodeScanCheckpoint(value ScanCheckpoint) (string, error)
func DecodeScanCheckpoint(value string) (ScanCheckpoint, error)
```

Encode directly as canonical JSON because `jobs.checkpoint_json` is constrained to a JSON object. Decode must reject unknown JSON fields with `json.Decoder.DisallowUnknownFields`, validate every directory through `storage.CleanRelativePath`, require the first frame to be `.`, and recognize a decoded object containing `cursor` as legacy.

- [ ] **Step 4: Add the exact DFS regression fixture**

In `internal/catalog/service_test.go`, create:

```text
mount root
├── a/
│   ├── 000.txt
│   ├── ...
│   └── 198.txt
└── a.txt
```

Add `TestCollectBatchRetainsParentFrameAcrossExactBoundary`. The first batch size is exactly 200 and must return `a` plus `a/000.txt` through `a/198.txt`; the checkpoint stack must retain the root frame after `a` and a child frame after `198.txt`. The second batch must return only root `a.txt` and `Done=true`. Across both batches, assert 200 unique paths.

- [ ] **Step 5: Run the DFS test and verify RED against the old cursor**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog -run TestCollectBatchRetainsParentFrameAcrossExactBoundary -count=1
```

Expected: FAIL because the existing single relative-path cursor skips root `a.txt` after the first batch.

- [ ] **Step 6: Add the catalog-facing walker interface and DFS state machine**

Define:

```go
type WalkEntry struct {
	Name     string
	Info     fs.FileInfo
	Identity storage.ObjectIdentity
}

type SafeWalker interface {
	ReadDirectory(ctx context.Context, relativeDir string) ([]WalkEntry, error)
	Close() error
}

type PreparedBatch struct {
	Entries    []Entry
	Checkpoint ScanCheckpoint
	Done       bool
}
```

Implement `collectBatch(ctx, mount, walker, checkpoint, limit)`. Always sort by bytewise `Name`. The production adapter calls `storage.Root.ReadDirectory` and maps each `storage.DirectoryEntry` to `WalkEntry.Info` and `WalkEntry.Identity`; it must not re-stat the path by name. For each next entry: update the current frame's `AfterName`; use the opened-object metadata and identity; append a catalog entry for regular files/directories; and, for a directory, push its child frame before checking the batch limit. Pop an exhausted frame. `Done` is true only when the stack is empty.

- [ ] **Step 7: Replace cursor-bearing scan types**

Use:

```go
type ScanOptions struct {
	BatchSize  int
	Checkpoint ScanCheckpoint
}

type ScanResult struct {
	Indexed    int
	Checkpoint ScanCheckpoint
	Done       bool
}
```

Remove `Cursor` and `NextCursor` from catalog scan APIs only; search pagination keeps its separate opaque cursor unchanged.

- [ ] **Step 8: Run checkpoint and DFS tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog -run 'Test(ScanCheckpoint|CollectBatch)' -count=1
```

Expected: all focused tests pass.

### Task 5: Persist scan epochs and reconcile only at completion

**Files:**
- Modify: `internal/catalog/service.go`
- Modify: `internal/catalog/service_test.go`
- Modify: `internal/storage/` only if the shared walker exposes an adapter bug; do not duplicate it in catalog

- [ ] **Step 1: Add failing epoch/reconciliation tests**

Add these tests against migrated SQLite:

```go
func TestUpsertBatchTxWritesOneScanEpochAndRevivesEntry(t *testing.T)
func TestInterruptedScanDoesNotTombstoneUnseenEntries(t *testing.T)
func TestFinalizeScanTombstonesOnlyUnseenEntries(t *testing.T)
func TestCompletedScanRepresentsMoveAsNewEntryAndOldTombstone(t *testing.T)
func TestMountIdentityDriftRejectsCheckpointWithoutWrites(t *testing.T)
func TestDescriptorWalkerRejectsSymlinkMagicLinkAndCrossMount(t *testing.T)
```

The interruption test seeds two visible rows, observes only one in a nonterminal batch, commits the batch, and asserts both remain visible. The finalization test uses the same setup but calls finalization and asserts only the unseen row gets `deleted_at`.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog -run 'Test(Upsert|Interrupted|Finalize|Completed|MountIdentity|Descriptor)' -count=1
```

Expected: FAIL because scan epochs, transactional entry APIs, and the production safe-walker adapter are absent.

- [ ] **Step 3: Separate filesystem preparation from database commit**

Add these methods:

```go
func (s Service) PrepareBatch(
	ctx context.Context,
	mount Mount,
	checkpoint ScanCheckpoint,
	batchSize int,
) (PreparedBatch, error)

func (s Service) UpsertBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	mount Mount,
	scanID string,
	entries []Entry,
) error

func (s Service) FinalizeScanTx(
	ctx context.Context,
	tx *sql.Tx,
	mountID, scanID string,
) (int64, error)
```

`PrepareBatch` validates the mount, compares checkpoint identity with `mount.IdentityJSON`, opens the shared descriptor-relative walker, and performs no SQL. If the checkpoint identity differs, return `ErrMountIdentityDrift` before opening a transaction.

- [ ] **Step 4: Add the scan epoch to the upsert**

Use this column behavior in `UpsertBatchTx`:

```sql
INSERT INTO catalog_entries(
    id, space_id, mount_id, relative_path, name, entry_kind, preview_kind,
    size_bytes, modified_at, identity_fingerprint, indexed_at,
    last_seen_scan_id, deleted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(mount_id, relative_path) DO UPDATE SET
    space_id = excluded.space_id,
    name = excluded.name,
    entry_kind = excluded.entry_kind,
    preview_kind = excluded.preview_kind,
    size_bytes = excluded.size_bytes,
    modified_at = excluded.modified_at,
    identity_fingerprint = excluded.identity_fingerprint,
    indexed_at = excluded.indexed_at,
    last_seen_scan_id = excluded.last_seen_scan_id,
    deleted_at = NULL;
```

- [ ] **Step 5: Implement terminal reconciliation**

`FinalizeScanTx` executes only:

```sql
UPDATE catalog_entries
SET deleted_at = ?
WHERE mount_id = ?
  AND deleted_at IS NULL
  AND (last_seen_scan_id IS NULL OR last_seen_scan_id <> ?);
```

Return affected rows. Do not physically delete catalog rows.

- [ ] **Step 6: Adapt the shared descriptor walker**

Implement the production `SafeWalker` adapter using the already-landed `internal/storage` primitive. Its `ReadDirectory` result must contain only regular files and directories whose metadata came from the opened descriptor. Treat symlink, magic-link, special-file, and forbidden mount-point errors as skipped entries only when policy classifies them as non-indexable; propagate mount identity/root errors so the worker fails or yields without reconciliation.

- [ ] **Step 7: Run catalog tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog -count=1
```

Expected: all catalog tests pass, including exact-boundary traversal and deletion safety.

### Task 6: Commit catalog batches and job state in one fenced transaction

**Files:**
- Modify: `internal/server/index_worker.go`
- Modify: `internal/server/index_worker_test.go`
- Modify: `internal/server/api.go` for updated manual run calls

- [ ] **Step 1: Add failing crash-boundary integration tests**

Add:

```go
func TestCatalogBatchRollsBackEntriesWhenCheckpointFenceIsLost(t *testing.T)
func TestCatalogFinalizationRollsBackTombstonesWhenCompletionFenceIsLost(t *testing.T)
func TestCatalogPauseDuringBatchNeverTombstones(t *testing.T)
func TestCatalogIdentityDriftNeverTombstones(t *testing.T)
func TestLegacyCursorRestartsAtRootWithNewScanID(t *testing.T)
func TestCatalogWorkerResumesExactDFSCheckpoint(t *testing.T)
```

Inject a test hook immediately before `RequeueTx`/`CompleteTx` that changes the claim token from a second connection. Assert the whole transaction rolls back: no batch entries for the lost owner and no tombstones for the lost finalizer.

- [ ] **Step 2: Run worker tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'TestCatalog(Batch|Finalization|Pause|Identity|Worker)|TestLegacyCursor' -count=1
```

Expected: FAIL because the worker currently commits catalog rows before requeue/complete and has no claim token.

- [ ] **Step 3: Add worker lease timing**

Extend options:

```go
type JobWorkerOptions struct {
	WorkerID          string
	PollInterval      time.Duration
	ScheduleInterval  time.Duration
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
}
```

Defaults: lease `30s`, heartbeat `10s`; reject or normalize a heartbeat interval not less than half the lease. Pass `jobs.ClaimOptions{WorkerID: workerID, LeaseDuration: leaseDuration}` to claims.

- [ ] **Step 4: Decode or restart the scan checkpoint**

Add:

```go
func catalogCheckpoint(job jobs.Job, mount catalog.Mount) (catalog.ScanCheckpoint, error) {
	checkpoint, err := catalog.DecodeScanCheckpoint(job.CheckpointJSON)
	if err == nil {
		return checkpoint, nil
	}
	if errors.Is(err, catalog.ErrLegacyCheckpoint) || job.CheckpointJSON == "{}" {
		scanID, idErr := newScanID()
		if idErr != nil {
			return catalog.ScanCheckpoint{}, idErr
		}
		return catalog.NewScanCheckpoint(scanID, mount.IdentityJSON), nil
	}
	return catalog.ScanCheckpoint{}, err
}
```

`newScanID` returns `scan_` plus 16 random bytes encoded as lowercase hex.

- [ ] **Step 5: Heartbeat while the filesystem batch is prepared**

Add `prepareCatalogBatchWithHeartbeat`. Start a child context and ticker before `catalog.Service.PrepareBatch`; each tick calls `store.Heartbeat(job.ID, job.ClaimToken.String, leaseDuration)`. On `ErrLeaseLost`, cancel the child context and return `ErrLeaseLost`. Stop and join the heartbeat goroutine before opening the write transaction.

- [ ] **Step 6: Make the nonterminal commit atomic**

After preparation, encode `prepared.Checkpoint`, begin one `sql.Tx`, call:

```go
catalogService.UpsertBatchTx(ctx, tx, mount, prepared.Checkpoint.ScanID, prepared.Entries)
store.RequeueTx(ctx, tx, job.ID, job.ClaimToken.String, checkpointJSON)
tx.Commit()
```

Any error rolls back. If the fenced call returns `ErrLeaseLost`, propagate it without calling `Fail`.

- [ ] **Step 7: Make terminal reconciliation atomic**

For `prepared.Done`, in one transaction call:

```go
catalogService.UpsertBatchTx(ctx, tx, mount, prepared.Checkpoint.ScanID, prepared.Entries)
catalogService.FinalizeScanTx(ctx, tx, mount.ID, prepared.Checkpoint.ScanID)
store.CompleteTx(ctx, tx, job.ID, job.ClaimToken.String)
tx.Commit()
```

The order matters: a failed completion fence rolls back tombstones.

- [ ] **Step 8: Fence error handling**

Update every `store.Fail` call in `runNextJobBatch` and `runCatalogScanBatch` to pass `job.ClaimToken.String`. If the primary error or the failure transition is `ErrLeaseLost`, log ownership loss at info/warn level and stop. Never overwrite the new owner's state.

- [ ] **Step 9: Run worker and server tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server ./internal/catalog ./internal/jobs -count=1
```

Expected: all tests pass; exact 200-item traversal retains root `a.txt`, and every injected fence loss rolls back the paired catalog mutation.

### Task 7: Reclaim leases before scheduling and define active work correctly

**Files:**
- Modify: `internal/server/index_worker.go`
- Modify: `internal/server/index_worker_test.go`

- [ ] **Step 1: Add failing maintenance tests**

Add:

```go
func TestMaintenanceReclaimsExpiredLeaseBeforeScheduling(t *testing.T)
func TestSchedulerIgnoresExpiredRunningLease(t *testing.T)
func TestRecoveredCatalogJobKeepsCheckpoint(t *testing.T)
func TestExpiredLeaseAtMaxAttemptsDoesNotRescheduleMount(t *testing.T)
```

The first test seeds an expired running scan and asserts maintenance requeues that same job rather than enqueueing a duplicate. The max-attempt test asserts the job becomes failed and a fresh scheduled scan may be enqueued with a new scan ID on the next scheduling pass.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'Test(Maintenance|Scheduler|Recovered|ExpiredLease)' -count=1
```

Expected: FAIL because maintenance neither reclaims leases nor distinguishes expired running rows.

- [ ] **Step 3: Reclaim before enqueue**

Change `runJobMaintenance` order to:

```go
store := jobs.NewStore(s.sqlDB())
if _, err := store.ReclaimExpired(ctx); err != nil {
	return err
}
if err := s.recoverPendingFileOperations(ctx); err != nil {
	return err
}
if err := s.recoverAndGarbageCollectUploads(ctx); err != nil {
	return err
}
if _, err := s.enqueueCatalogScanJobs(ctx); err != nil {
	return err
}
return s.expireOperationalRows(ctx)
```

The file/upload plan owns `recoverPendingFileOperations` and `recoverAndGarbageCollectUploads` as bounded callbacks; this task is the sole owner of `runJobMaintenance` and invokes them after lease reclaim and before catalog enqueue. No second maintenance loop may be introduced.

- [ ] **Step 4: Correct the active-job query**

Replace the scheduler filter with:

```sql
WHERE kind = 'catalog_scan'
  AND (
      status IN ('queued', 'paused')
      OR (status = 'running' AND lease_expires_at > ?)
  )
```

Pass one captured `now` value. A failed/completed/canceled job and an expired running lease do not count as active.

- [ ] **Step 5: Run worker tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'Test.*(Job|Catalog|Scheduler|Maintenance|Lease)' -count=1
```

Expected: all focused tests pass with checkpoint preservation and no duplicate active scan.

### Task 8: Extend the identity-owned `RouteDefinition` manifest with Catalog/API routes

**Files:**
- Consume: `internal/server/routes.go` and `internal/server/routes_test.go` from the identity/HTTP plan
- Modify: `internal/server/server.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/openapi.go`

- [ ] **Step 1: Add failing manifest invariant tests**

Add:

```go
func TestRouteDefinitionsAreUniqueAndComplete(t *testing.T)
func TestEveryOpenAPIRouteHasUniqueOperationID(t *testing.T)
func TestEveryAPIRouteIsRegisteredFromManifest(t *testing.T)
func TestStaticAndSPAFallbacksAreExcludedFromManifest(t *testing.T)
```

Assert the manifest contains the explicit approved route set from `apiRoutes`, plus `GET /healthz`, `GET /readyz`, the OpenAPI document routes, and the file-plan ticket/preview routes. Do not use a stale numeric count; compare an explicit sorted `METHOD path` slice and require every newly approved route (including separate GET/HEAD ticket operations) to appear exactly once.

- [ ] **Step 2: Run route tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'TestRouteDefinitions|TestEvery(OpenAPI|API)|TestStatic' -count=1
```

Expected: FAIL because the identity-owned manifest is missing the complete Catalog/API, health/document, and file-plan ticket entries or their parity assertions; this task must extend it rather than create a second manifest.

- [ ] **Step 3: Consume the identity-owned route metadata types**

Do not create a second `RouteDefinition`, `AuthMode`, or `routes_test.go`. The identity/HTTP plan owns the single manifest and wrapper composition, including `AllowedSessionPurposes`, `EnrollmentBypassesReauth`, `RequiresRecentReauth`, `ContractKind`, and `AuthShareCapability`. This task only adds Catalog/API route definitions to that existing manifest and extends its tests. The shared manifest must also include file-plan ticket routes: `POST /api/v1/share/download-tickets` uses `AuthShareCookie` and share CSRF; `GET` and `HEAD /api/v1/share/downloads/{ticketId}` use `AuthShareCapability` without CSRF; `GET /api/v1/share/preview` uses `AuthShareCookie`.

- [ ] **Step 4: Extend the existing validating registration helper**

Do not add another route table or wrapper. Extend the identity-owned helper only where needed for `ContractHealth`, `ContractDocument`, and `AuthShareCapability`. The existing helper remains the sole writer of both `ServeMux` and `routeDefinitions`. Validation must also require a ticket `HEAD` operation to have its own operation ID.

- [ ] **Step 5: Convert all explicit registrations**

Replace every direct registration in `apiRoutes`, `routes`, and `registerOpenAPIRoutes` with `registerRoute`. Use handler function names as exact operation IDs for OpenAPI routes, for example:

```go
{Method: http.MethodGet, Pattern: "/api/v1/spaces", Group: domain.RouteGroupREST, Auth: AuthCookieSession, Contract: ContractOpenAPI, OperationID: "listSpaces", Handler: s.listSpaces},
{Method: http.MethodPatch, Pattern: "/api/v1/account/password", Group: domain.RouteGroupREST, Auth: AuthCookieSession, RequiresRecentReauth: true, Contract: ContractOpenAPI, OperationID: "changeAccountPassword", Handler: s.changeAccountPassword},
{Method: http.MethodPost, Pattern: "/api/v1/share-sessions", Group: domain.RouteGroupShare, Auth: AuthPublicPreAuth, Contract: ContractOpenAPI, OperationID: "createShareSession", Handler: s.createShareSession},
{Method: http.MethodGet, Pattern: "/api/v1/share/current", Group: domain.RouteGroupShare, Auth: AuthShareCookie, Contract: ContractOpenAPI, OperationID: "shareCurrent", Handler: s.shareCurrent},
{Method: http.MethodPost, Pattern: "/mcp", Group: domain.RouteGroupMCP, Auth: AuthBearer, Contract: ContractMCP, Handler: s.handleMCP},
{Method: http.MethodGet, Pattern: "/healthz", Auth: AuthHealth, Contract: ContractHealth, OperationID: "health", Handler: s.health},
{Method: http.MethodGet, Pattern: "/readyz", Auth: AuthHealth, Contract: ContractHealth, OperationID: "ready", Handler: s.ready},
{Method: http.MethodGet, Pattern: "/openapi/omnora.v1.yaml", Group: domain.RouteGroupOpenAPI, Auth: AuthHealth, Contract: ContractDocument, Handler: s.serveOpenAPISpec},
```

Classify `initialize`, login/session creation, and identity-plan enrollment entrypoints as `public_pre_auth`; `POST /api/v1/account/reauthenticate` is `cookie_session` and does not itself require recent reauth. Account/member/admin routes are `cookie_session`; share portal resource routes are `share_cookie`; MCP is `bearer`. Mark exactly the mutation matrix in design section 5.1 as `RequiresRecentReauth=true` and add a test comparing an explicit sorted `METHOD path` slice, so no path-prefix inference is possible. Keep SPA/static/product-group fallback registrations outside the manifest.

- [ ] **Step 6: Prove there is no second API route table**

Run:

```bash
rg -n 'mux\.Handle(Func)?\(' internal/server --glob '*.go' --glob '!**/*_test.go'
```

Expected: direct calls remain only inside `registerRoute`, `handleWebGroup`, `handleProductGroup`, and the root/static fallback. No explicit health, OpenAPI document, `/api/v1/*`, or `/mcp` route is registered elsewhere.

- [ ] **Step 7: Run route and server tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'Test(Route|API|Health|Ready|OpenAPI)' -count=1
```

Expected: all tests pass and disabled route groups retain their current 404 behavior.

### Task 9: Capture and name the runtime DTO contract

**Files:**
- Create: `internal/server/http_dto.go`
- Create: `internal/server/http_dto_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/account_security.go`
- Modify: `internal/server/admin_control.go`
- Modify: `internal/server/share_portal.go`
- Modify: `internal/server/route_groups.go`

- [ ] **Step 1: Add exact JSON compatibility tests before refactoring handlers**

Add table-driven marshal tests for health, ready, session, account password status, TOTP setup/confirm/disable, enrollment/reauth responses supplied by the identity plan, spaces, mounts, directory listings, shares, share exchange, AI Tokens, admin users, and route groups. Assert keys and omission rules by unmarshalling into `map[string]json.RawMessage`; compare timestamps as RFC3339 strings.

Use explicit golden payloads for the ambiguous share exchange:

```json
{"status":"password_required"}
```

at HTTP 200, and:

```json
{"status":"created","shareSessionId":"ssn_1","expiresAt":"2026-08-06T13:00:00Z"}
```

at HTTP 201.

- [ ] **Step 2: Run DTO tests against current handlers**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'TestHTTPDTO|Test.*JSONContract' -count=1
```

Expected: handler contract snapshots pass; compile-time tests for named DTOs fail because the types do not exist.

- [ ] **Step 3: Add named identity and health DTOs**

Create `internal/server/http_dto.go` with:

```go
type healthResponse struct { Status string `json:"status"` }
type statusResponse struct { Status string `json:"status"` }

type sessionRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	TOTPCode string `json:"totpCode,omitempty"`
}

type sessionResponse struct {
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
	IsAdmin   bool      `json:"isAdmin"`
}

type accountPasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	RevokeTokens    bool   `json:"revokeTokens,omitempty"`
	RevokeShares    bool   `json:"revokeShares,omitempty"`
}

type totpSetupResponse struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauthUri"`
}

type totpCodeRequest struct { Code string `json:"code"` }
type totpDisableRequest struct {
	Password string `json:"password"`
	Code     string `json:"code,omitempty"`
}

type reauthenticateRequest struct {
	Password string `json:"password"`
	TOTPCode string `json:"totpCode,omitempty"`
}

type reauthenticateResponse struct {
	Status               string    `json:"status"`
	ReauthenticatedUntil time.Time `json:"reauthenticatedUntil"`
}

type enrollmentSessionResponse struct {
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
	IsAdmin   bool      `json:"isAdmin"`
	Purpose   string    `json:"purpose"`
}
```

`reauthenticateResponse.Status` is exactly `reauthenticated`; `enrollmentSessionResponse.Purpose` is exactly `totp_enrollment`. The identity handlers must return these named DTOs rather than a second anonymous shape.

- [ ] **Step 4: Add named resource DTOs**

Add:

```go
type itemListResponse[T any] struct { Items []T `json:"items"` }

type spaceDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

type directoryListingDTO struct {
	RelativePath string        `json:"relativePath"`
	ReadOnly     bool          `json:"readOnly"`
	Entries      []files.Entry `json:"entries"`
}

type createShareSessionRequest struct {
	PublicID string `json:"public_id"`
	Secret   string `json:"secret"`
	Password string `json:"password,omitempty"`
}

type sharePasswordRequiredResponse struct { Status string `json:"status"` }
type shareSessionCreatedResponse struct {
	Status         string    `json:"status"`
	ShareSessionID string    `json:"shareSessionId"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type aiTokenBoundaryDTO struct {
	SpaceID string `json:"spaceId"`
	MountID string `json:"mountId"`
	Path    string `json:"path"`
}

type createAITokenRequest struct {
	Name       string               `json:"name"`
	Scopes     []string             `json:"scopes"`
	Boundaries []aiTokenBoundaryDTO `json:"boundaries"`
	ExpiresAt  string               `json:"expiresAt"`
}

type createAdminUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	Role        string `json:"role,omitempty"`
}
```

Move the existing complete `mountDTO`, `shareDTO`, `adminUserDTO`, and `routeGroupDTO` declarations into `http_dto.go` without changing JSON tags. Add named response structs for create-share and create-AI-token that exactly preserve their current fields, including one-time secrets.

- [ ] **Step 5: Replace anonymous structs and response maps in the first slice**

Update the named handlers to decode/encode the DTOs. Remove snake_case compatibility aliases only where the Web client and documented API already use camelCase; keep `createShareSessionRequest` snake_case because it is the current public exchange contract. Do not change business logic in this task.

- [ ] **Step 6: Run DTO and full server tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -count=1
```

Expected: all pre-refactor JSON snapshots still pass byte-for-byte after canonical JSON normalization.

### Task 10: Make OpenAPI paths and real responses agree with the manifest

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/server/openapi_contract_test.go`
- Modify: `openapi/omnora.v1.yaml`
- Modify: `internal/server/openapi_assets/omnora.v1.yaml` only through the sync script

- [ ] **Step 1: Pin the OpenAPI test parser**

Run:

```bash
go get github.com/getkin/kin-openapi@v0.133.0
go mod tidy
```

Expected: `go.mod` and `go.sum` record the parser; no production handler imports it.

- [ ] **Step 2: Add failing manifest parity tests**

Load `openapi/omnora.v1.yaml`, validate it, and compare only definitions with `ContractOpenAPI`. Normalize runtime `/api/v1/foo` to OpenAPI `/foo` using the top-level server URL `/api/v1`. Health/ready use their operation-level `/` server override; `/mcp` is excluded and covered by MCP protocol tests; OpenAPI document routes and SPA/static fallbacks are excluded.

Assert for every compared operation:

```go
manifest.OperationID == openAPIOperation.OperationID
```

Also assert there are no OpenAPI operations absent from the manifest and no `x-omnora-stale` keys anywhere in the document.

- [ ] **Step 3: Run parity tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'TestOpenAPIManifestParity|TestOpenAPIHasNoStaleOperations' -count=1
```

Expected: FAIL on stale/nonexistent paths and missing operation IDs.

- [ ] **Step 4: Remove nonexistent operations without deleting real ones**

Remove `/health`, `/route-groups`, `/spaces/{spaceId}`, `/spaces/{spaceId}/acl`, `/files/{objectId}`, `/files/{objectId}/text`, `/files/{objectId}/download-ticket`, and `/downloads/{ticketId}`. At `/shares/{shareId}`, remove stale GET/PATCH operations but retain the runtime DELETE operation and remove the path-level stale marker.

- [ ] **Step 5: Add exact operation IDs and correct schemas**

For each retained OpenAPI operation, set `operationId` to the manifest handler name. Correct the first DTO slice so required/optional fields match `http_dto.go`. Specifically:

- Remove fictional `targetObjectId` from share request/response schemas; use `spaceId`, `mountId`, and `relativePath`.
- Model `POST /share-sessions` with HTTP 200 `ShareSessionPasswordRequiredResponse` and HTTP 201 `ShareSessionCreatedResponse`.
- Model `POST /share/download-tickets`, `GET` and `HEAD /share/downloads/{ticketId}`, and `GET /share/preview` with `ShareDownloadTicketRequest`, `ShareDownloadTicketResponse`, `ShareDownloadResponse`, `ShareDownloadHeadResponse`, and `SharePreviewResponse`. The GET and HEAD operations use the file plan's `AuthShareCapability` route metadata and separate operation IDs; the secret is never an OpenAPI response field or URL value.
- Model session, account password, TOTP setup/confirm/disable, reauthenticate, enrollment session, space, mount, directory listing, share, AI Token, admin user, route group, health, and ready with named component schemas.
- Keep runtime error media type `application/json` and the existing `{code,message,request_id}` envelope.
- Preserve `/mcp` root-server override, but do not include it in REST schema parity.

- [ ] **Step 6: Add real-response schema validation**

In `openapi_contract_test.go`, create a helper:

```go
func assertJSONResponseMatchesOpenAPI(
	t *testing.T,
	doc *openapi3.T,
	method, runtimePath string,
	status int,
	body []byte,
)
```

Resolve the manifest operation, select the exact status and `application/json` schema, decode with `json.Decoder.UseNumber`, and call the resolved schema's `VisitJSON`. Exercise real `httptest` responses for both share-session variants and at least one success response from every named DTO family. Authentication setup may reuse existing server test helpers.

- [ ] **Step 7: Sync the embedded asset and run contract tests**

```bash
scripts/verification/sync-openapi-asset.sh
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'TestOpenAPI|Test.*MatchesOpenAPI' -count=1
scripts/verification/sync-openapi-asset.sh --check
```

Expected: all commands pass; source and embedded YAML are byte-identical.

### Task 11: Generate TypeScript wire types and keep UI adapters handwritten

**Files:**
- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Create: `web/src/generated/omnora-api.ts`
- Modify: `web/src/api.ts`
- Modify: `web/src/member/types.ts`
- Modify: `web/src/types.ts`
- Create: `web/src/api.contract.test.ts`
- Create: `scripts/verification/verify-openapi-types.sh`

- [ ] **Step 1: Add compile-time contract tests**

Add a Vitest file importing `components` from the generated module and compile-time assertions such as:

```ts
type Schemas = components['schemas'];
const passwordRequired: Schemas['ShareSessionPasswordRequiredResponse'] = {
  status: 'password_required',
};
const created: Schemas['ShareSessionCreatedResponse'] = {
  status: 'created',
  shareSessionId: 'ssn_1',
  expiresAt: '2026-08-06T13:00:00Z',
};
```

Add runtime tests for the small adapters that turn optional wire arrays into empty UI arrays and that branch on the two share-session statuses.

- [ ] **Step 2: Run Web tests and verify RED**

```bash
npm --prefix web test -- --run src/api.contract.test.ts
npm --prefix web run build
```

Expected: FAIL because the generated module and aliases do not exist.

- [ ] **Step 3: Pin and expose `openapi-typescript`**

Run:

```bash
npm --prefix web install --save-dev --save-exact openapi-typescript@7.10.1
```

Add scripts:

```json
{
  "generate:api-types": "openapi-typescript ../openapi/omnora.v1.yaml -o src/generated/omnora-api.ts",
  "check:api-types": "../scripts/verification/verify-openapi-types.sh"
}
```

- [ ] **Step 4: Generate the committed schema types**

```bash
npm --prefix web run generate:api-types
```

Expected: `web/src/generated/omnora-api.ts` is created. Do not manually edit it.

- [ ] **Step 5: Replace handwritten wire types with generated aliases**

At the top of `web/src/api.ts`:

```ts
import type { components } from './generated/omnora-api';

type Schemas = components['schemas'];
export type HealthPayload = Schemas['HealthResponse'];
export type SessionPayload = Schemas['SessionResponse'];
export type DirectoryChildrenPayload = Schemas['DirectoryListing'];
export type CreateSharePayload = Schemas['CreateShareRequest'];
export type CreateShareResponse = Schemas['CreateShareResponse'];
export type ShareExchangePayload =
  | Schemas['ShareSessionPasswordRequiredResponse']
  | Schemas['ShareSessionCreatedResponse'];
export type ShareDownloadTicketResponse = Schemas['ShareDownloadTicketResponse'];
export type SharePreviewResponse = Schemas['SharePreviewResponse'];
export type CreateAiTokenPayload = Schemas['CreateAITokenRequest'];
export type CreateAiTokenResponse = Schemas['CreateAITokenResponse'];
export type AdminUserPayload = Schemas['AdminUser'];
export type AdminRouteGroupItem = Schemas['RouteGroup'];
```

Keep `api.ts` request functions and explicit UI normalization functions. Remove duplicate wire declarations from `member/types.ts` and `types.ts` only when generated aliases replace them; retain presentation-only `Tone`, formatted sizes, progress state, and display labels.

- [ ] **Step 6: Add a non-mutating generated-file check**

Create `scripts/verification/verify-openapi-types.sh`. It must make a `mktemp -d`, run the repository-pinned generator into that directory, `cmp` it with `web/src/generated/omnora-api.ts`, print a regeneration command on mismatch, and remove only the exact temp directory via `trap`.

- [ ] **Step 7: Run Web contract gates**

```bash
npm --prefix web test -- --run
npm --prefix web run build
scripts/verification/verify-openapi-types.sh
```

Expected: all commands pass and the checked-in generated file has zero diff from fresh generation.

### Task 12: Wire the unified contract gate and perform final acceptance

**Files:**
- Modify: `scripts/verification/verify-api-docs.sh`
- Test: all files changed by Tasks 1-11

- [ ] **Step 1: Extend the API verification script**

After the embedded asset check, add:

```sh
GOCACHE=${GOCACHE:-/private/tmp/omnora-go-cache} \
GOMODCACHE=${GOMODCACHE:-/private/tmp/omnora-go-modcache} \
go test ./internal/server -run 'TestOpenAPI|TestRouteDefinitions|Test.*MatchesOpenAPI' -count=1

"$ROOT/scripts/verification/verify-openapi-types.sh"
```

Retain existing documentation/MCP assertions. Remove only assertions for the deliberately deleted stale OpenAPI operations.

- [ ] **Step 2: Run focused failure-recovery acceptance**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/catalog ./internal/jobs ./internal/server ./internal/store -count=1
```

Expected: DFS exact-boundary, epoch deletion, lease contention/reclaim, fence rollback, route parity, and real response schema tests all pass.

- [ ] **Step 3: Run repository gates**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./... -count=1
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go vet ./...
npm --prefix web test -- --run
npm --prefix web run build
scripts/verification/verify-api-docs.sh
scripts/verification/verify-scaffolding.sh
scripts/verification/test-docker-entrypoint.sh
git diff --check
```

Expected: every command exits 0. If an environment restriction blocks a command, preserve the exact error and rerun only with the required approval; do not report an unexecuted gate as passing.

- [ ] **Step 4: Verify migration and generated-artifact cleanliness**

```bash
git status --short
git diff -- internal/store/migrations/010_catalog_jobs.sql openapi/omnora.v1.yaml internal/server/openapi_assets/omnora.v1.yaml web/src/generated/omnora-api.ts
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-openapi-types.sh
```

Expected: only intended files are changed; OpenAPI copies and generated TypeScript agree exactly.

## Acceptance matrix

| Requirement | Proof |
| --- | --- |
| Exact DFS boundary does not skip root siblings | `TestCollectBatchRetainsParentFrameAcrossExactBoundary` returns all 200 unique paths. |
| Scan resume is identity-bound and versioned | Checkpoint codec tests plus identity-drift worker test. |
| Interrupted/paused/crashed scan cannot delete rows | Epoch tests and fenced transaction rollback tests. |
| Completed scan detects delete and move | Finalization and move tests assert tombstones only at terminal commit. |
| Claims are atomic and exclusive while live | Two-connection contention test and unexpired-running test. |
| Stale workers cannot mutate job/catalog state | Wrong-token, expiry, pause, and injected pre-commit fence tests. |
| Expired leases recover deterministically | Reclaim tests preserve checkpoint and enforce `max_attempts`. |
| One runtime route source exists | Direct `mux.Handle` scan and explicit manifest test. |
| Runtime and OpenAPI have zero path/method/operation-ID drift | `TestOpenAPIManifestParity`. |
| Real JSON bodies satisfy schemas | `httptest` schema validation across every first-slice DTO family. |
| Share exchange documents both valid successes | HTTP 200 password-required and HTTP 201 created tests. |
| Frontend wire types are generated and reproducible | Web compile tests and `verify-openapi-types.sh`. |
| Embedded OpenAPI remains synchronized | `sync-openapi-asset.sh --check` and `verify-api-docs.sh`. |

## Rollback and incident procedure

- The migration is expand-only. Roll back binaries without dropping `last_seen_scan_id`, `claim_token`, `lease_expires_at`, or `heartbeat_at`; old binaries ignore nullable columns.
- Before rolling back a worker, pause catalog scheduling and wait for or explicitly expire current leases. Do not let old unfenced workers overlap with new leased workers.
- If checkpoint decoding, mount identity, or safe traversal is uncertain, pause the affected catalog job. Keep existing catalog rows visible and do not run `FinalizeScanTx`.
- If a new checkpoint must be abandoned, clear only that job's checkpoint through an authorized maintenance operation and restart at root with a new scan ID. Never translate the old scalar cursor.
- If reconciliation produced an unexpected tombstone set but the transaction committed, disable the worker, preserve rows, and restore visibility by a new verified full scan; do not hard-delete catalog history.
- Roll back `RouteDefinition`, OpenAPI YAML, embedded YAML, generated TypeScript, and Web wrapper aliases as one compatibility unit. Never serve a reverted runtime with a newer incompatible schema or vice versa.
- Reverting generated TypeScript alone is safe only when runtime and OpenAPI did not change; otherwise revert the whole contract unit.
- Do not reverse the additive migration during incident response. A future cleanup migration may enforce non-null/check constraints only after one stable release and separate approval.

## Final self-review checklist

- Every P1 catalog requirement maps to Tasks 4-7 and has a named test.
- Every owner mutation in `internal/jobs` takes a claim token and checks the lease.
- Every reconciliation path is terminal and in the same transaction as `CompleteTx`.
- Shared storage and identity/HTTP work are dependencies, not duplicated implementations.
- Every explicit API route is registered once through `registerRoute`.
- Contract comparison excludes MCP, OpenAPI documents, and SPA/static routes by typed metadata rather than path guesses.
- Runtime DTO names, OpenAPI component names, and TypeScript aliases use the same JSON fields and success statuses.
- The plan introduces no destructive schema rollback and no automatic commit step.
