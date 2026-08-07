# Standard MCP File Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Omnora's private `/mcp` JSON adapter with a standard, Inspector-verifiable MCP `2026-07-28` Streamable HTTP service that exposes complete member file management through existing AI Tokens, per-operation confirmation, and short-lived transfer tickets.

**Architecture:** Pin the official Go MCP SDK v1.7.0 and keep `/mcp` as a thin protocol adapter. Move authorization and member file/share behavior into shared application services used by both REST and MCP. Keep large file bytes on guarded `/mcp/transfers/*` HTTP routes. Persist one-time confirmation challenges and transfer tickets in SQLite, revalidate live account/token/ACL/mount state on every request, and fail closed when a client cannot perform reliable human confirmation.

**Tech Stack:** Go 1.26, official `github.com/modelcontextprotocol/go-sdk` v1.7.0, `net/http`, SQLite via `database/sql`, React 19 + TypeScript + Vitest, OpenAPI 3.1, MCP Inspector v2.1.0, POSIX shell verification scripts.

---

## Execution rules

- Implement tasks in order. Tasks 2-8 create the shared security and application layers required by the MCP adapter; do not register production MCP tools before those dependencies exist.
- Follow RED → GREEN for each behavior task. A failing test must fail for the intended missing behavior before production code is added.
- Do not preserve the old `{ "method": "...", "params": {} }` protocol on another route.
- Do not expose administrator control-plane tools.
- Keep the full help center, interactive Swagger/ReDoc/Scalar UI, and complete project operation manual in the already agreed second documentation phase; this plan only synchronizes MCP, transfer HTTP, security, product, and acceptance documentation required for the protocol release.
- Do not add automatic `git commit` steps. Review each task's diff; commit only if the user explicitly asks.
- Never put AI Token, confirmation secret, transfer-ticket secret, password, file content, or share fragment secret in logs, audit metadata, URLs, test failure messages, or screenshots.

## Canonical tool contract

`internal/mcpapi/catalog.go` is the executable source for this exact 24-tool mapping. Scope grants call eligibility only; the listed live permission and all account/token/boundary/mount checks still apply.

| Tool | Scope | Live permission | MRTR confirmation |
| --- | --- | --- | --- |
| `spaces.list` | `spaces:read` | Return only currently visible spaces | No |
| `mounts.list` | `spaces:read` | Viewer+ in requested space | No |
| `files.list` | `files:list` | Viewer+ | No |
| `files.metadata` | `files:metadata` | Viewer+ | No |
| `files.search` | `search:read` | Viewer+ and indexed boundary | No |
| `files.read_text` | `files:text` | Viewer+ | No |
| `files.prepare_download` | `files:download_ticket` | Viewer+ | No |
| `directories.create` | `files:write` | Editor+ and read-write mount | No |
| `files.prepare_upload` | `uploads:create` | Editor+ and read-write mount | No |
| `uploads.status` | `uploads:create` | Owning token, current Editor+ | No |
| `uploads.complete` | `uploads:create` | Owning token, current Editor+ | No |
| `uploads.cancel` | `uploads:create` | Owning token, current Editor+ | No |
| `files.rename` | `files:write` | Editor+ and read-write mount | No |
| `files.copy` | `files:write` | Source Viewer+, destination Editor+ | No |
| `files.move` | `files:write` | Source and destination Editor+ | Yes |
| `files.trash` | `files:trash` | Editor+ on managed read-write mount | Yes |
| `trash.list` | `trash:read` | Editor+ on managed mount | No |
| `trash.restore` | `files:restore` | Editor+ on managed read-write mount | No |
| `trash.purge` | `files:purge` | Editor+ on managed read-write mount | Yes |
| `trash.empty` | `files:purge` | Editor+ on managed read-write mount | Yes |
| `files.delete_permanently` | `files:purge` | Editor+ and read-write mount | Yes |
| `shares.list` | `shares:read` | Created by caller or caller manages space | No |
| `shares.create` | `shares:create` | Manager | Yes |
| `shares.revoke` | `shares:revoke` | Creator or current Manager | Yes |

Every tool uses `OpenWorldHint=false` except share creation/revocation, which use `OpenWorldHint=true` because they change public reachability. Read/list/status tools and `files.prepare_download` use `ReadOnlyHint=true`. Creation/copy/restore use `ReadOnlyHint=false` and `DestructiveHint=false`. Rename/move/trash/cancel/purge/delete/share revoke use `ReadOnlyHint=false` and `DestructiveHint=true`. `shares.create` is non-read-only and non-destructive but still requires MRTR because it creates public exposure. These annotations are model hints only and never replace server authorization or confirmation.

### Task 1: Pin the SDK, expand scopes, and add persistence

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `NOTICE`
- Modify: `internal/aitoken/types.go`
- Modify: `internal/aitoken/service.go`
- Modify: `internal/aitoken/service_test.go`
- Create: `internal/store/migrations/006_standard_mcp.sql`
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 1: Add failing scope and migration tests**

Extend `internal/aitoken/service_test.go` so `AllowlistedScopes()` and `ValidateScopes()` accept exactly these 15 values and continue rejecting empty, duplicate, and unknown values:

```go
spaces:read
files:list
files:metadata
files:text
files:download_ticket
search:read
uploads:create
files:write
files:trash
trash:read
files:restore
files:purge
shares:read
shares:create
shares:revoke
```

Extend `internal/store/sqlite_test.go` to open a migrated database and assert that `mcp_confirmations`, `mcp_transfer_tickets`, their indexes, and the new MCP audit columns exist.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/aitoken ./internal/store -count=1
```

Expected: FAIL because the eight new scopes and migration 006 are absent.

- [ ] **Step 3: Add the eight scope constants and active-principal refresh**

Add these exact constants to `internal/aitoken/types.go` and include them in `AllowlistedScopes()`:

```go
ScopeFilesWrite   Scope = "files:write"
ScopeFilesTrash   Scope = "files:trash"
ScopeTrashRead    Scope = "trash:read"
ScopeFilesRestore Scope = "files:restore"
ScopeFilesPurge   Scope = "files:purge"
ScopeSharesRead   Scope = "shares:read"
ScopeSharesCreate Scope = "shares:create"
ScopeSharesRevoke Scope = "shares:revoke"
```

Add `RefreshPrincipal(ctx context.Context, tokenID string) (Principal, error)` to `internal/aitoken/service.go`. It must reload the token by internal ID, reject revoked/expired tokens and inactive accounts, and reload current boundaries without requiring the original secret. It exists only for validating a previously issued transfer ticket; normal MCP authentication must continue using `VerifyBearer`.

- [ ] **Step 4: Add migration 006**

Create the two tables with foreign keys to `accounts`, `ai_tokens`, `spaces`, `mounts`, and `upload_sessions` where applicable. The migration must include these persisted fields:

```text
mcp_confirmations:
  id, public_id, secret_hash, account_id, ai_token_id, tool_name,
  args_hash, object_fingerprint, impact_json, status,
  created_at, expires_at, consumed_at

mcp_transfer_tickets:
  id, public_id, secret_hash, account_id, ai_token_id, operation,
  required_scope, space_id, mount_id, relative_path,
  object_fingerprint, upload_id, max_bytes, consumed_bytes,
  status, created_at, expires_at, closed_at
```

Constrain confirmation status to `pending|accepted|declined|expired`, ticket operation to `download|upload`, and ticket status to `active|completed|canceled|expired`. Add unique indexes on both public IDs, an index for pending confirmation expiry, and an index for active ticket expiry. Extend `audit_events` with nullable `credential_public_id`, `tool_name`, `result`, `request_id`, `trace_id`, and `parent_event_id` columns; avoid a column name containing `secret` or raw bearer data.

- [ ] **Step 5: Pin the official SDK and record its license**

Run:

```bash
go get github.com/modelcontextprotocol/go-sdk@v1.7.0
go mod tidy
```

Update `NOTICE` to identify the official MCP Go SDK v1.7.0 and its Apache-2.0/MIT transition notice. Do not copy dependency source into this repository.

- [ ] **Step 6: Verify GREEN and dependency integrity**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/aitoken ./internal/store -count=1
go mod verify
```

Expected: all commands exit 0.

### Task 2: Build the shared AccessGuard

**Files:**
- Modify: `internal/access/policy.go`
- Create: `internal/access/guard.go`
- Create: `internal/access/guard_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/file_ops.go`

- [ ] **Step 1: Add a failing authorization matrix**

Cover active/inactive account, revoked token refresh, missing scope, viewer/editor/manager permissions, disabled space/mount, wrong space-to-mount relation, read-only write, root boundary, adjacent-prefix boundary (`docs` must not authorize `docs-private`), absolute path, `..`, `.omnora/`, symlink path, and mount identity drift. Include source/destination tests for copy and move.

- [ ] **Step 2: Run the AccessGuard tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/access -run 'Guard' -count=1
```

Expected: FAIL because `Guard` does not exist.

- [ ] **Step 3: Implement the exact shared types**

```go
type Locator struct {
    SpaceID string
    MountID string
    Path    string
}

type Subject struct {
    AccountID string
    Principal *aitoken.Principal
}

type CheckRequest struct {
    Subject            Subject
    Scope              aitoken.Scope
    Locator            Locator
    RequiredPermission domain.SpacePermission
    Write              bool
}

type AuthorizedMount struct {
    ID             string
    SpaceID        string
    Root           string
    Kind           string
    Mode           domain.MountMode
    IdentityJSON   string
    RelativePath   string
}

func NewGuard(db *sql.DB) *Guard
func (g *Guard) Authorize(ctx context.Context, req CheckRequest) (AuthorizedMount, error)
func (g *Guard) AuthorizePair(ctx context.Context, source, destination CheckRequest) (AuthorizedPair, error)
```

`Subject.Principal == nil` represents an authenticated browser session: enforce live account/ACL/mount checks but no AI Token scope/boundary. A non-nil principal represents MCP or ticket access: additionally require account ID equality, live scope, and boundary containment.

- [ ] **Step 4: Move shared checks behind AccessGuard**

Move the reusable behavior currently represented by `mountForListing`, `loadMountForListing`, `verifyLoadedMountIdentity`, `hasSpacePermission`, and token-boundary path checks into `internal/access`. Keep temporary server wrappers only where an existing handler still needs them; they must delegate to the guard rather than retain a second authorization implementation.

- [ ] **Step 5: Verify GREEN**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/access ./internal/server -run 'Guard|Boundary|MountIdentity' -count=1
```

Expected: PASS with all fail-closed cases covered.

### Task 3: Add MCP intent/outcome auditing and readiness degradation

**Files:**
- Modify: `internal/audit/audit.go`
- Create: `internal/audit/mcp.go`
- Create: `internal/audit/mcp_test.go`
- Modify: `internal/server/server.go`
- Create: `internal/server/readiness_test.go`

- [ ] **Step 1: Add failing audit and readiness tests**

Assert that MCP audit writes include public credential ID, tool name, target, request ID, trace ID, and result without secret-bearing metadata. Assert that an intent write failure prevents execution. Assert that a failed outcome write after a simulated filesystem mutation records `mcp_audit_risk` in `system_state` and makes `/readyz` return `503 mcp_audit_degraded`.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/audit ./internal/server -run 'MCPAudit|Readiness' -count=1
```

Expected: FAIL because the MCP-specific audit methods and readiness state do not exist.

- [ ] **Step 3: Implement MCP audit records**

```go
type MCPEvent struct {
    AccountID          string
    CredentialPublicID string
    ToolName           string
    Result             string
    RequestID          string
    TraceID            string
    TargetType         string
    TargetID           string
    MetadataJSON       string
}

func (r Recorder) RecordMCPIntent(ctx context.Context, event MCPEvent) (int64, error)
func (r Recorder) RecordMCPOutcome(ctx context.Context, intentID int64, event MCPEvent) error
func (r Recorder) MarkMCPReadinessRisk(ctx context.Context, cause error) error
```

Intent rows use `result='intent'`; terminal rows use `succeeded|failed|declined`. `parent_event_id` links the outcome to the intent. `MarkMCPReadinessRisk` stores only a sanitized reason class and timestamp in `system_state`; it must not store the original error if that error may contain a host path.

- [ ] **Step 4: Make readiness fail closed after audit integrity loss**

Extend `Server.ready` to query `system_state.mcp_audit_risk`. Return `503` until the risk has been deliberately cleared after investigation; a later successful unrelated audit write must not clear it automatically.

- [ ] **Step 5: Verify GREEN**

Run the focused command from Step 2. Expected: PASS.

### Task 4: Implement one-time confirmation challenges

**Files:**
- Create: `internal/confirmation/types.go`
- Create: `internal/confirmation/service.go`
- Create: `internal/confirmation/service_test.go`

- [ ] **Step 1: Add failing lifecycle and replay tests**

Test challenge creation, hash-only secret persistence, two-minute TTL, canonical argument binding, account binding, token binding, tool binding, object fingerprint drift, explicit decline, expired state, malformed state, repeated consume, and two concurrent consumers where exactly one succeeds.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/confirmation -count=1
```

Expected: FAIL because the package is absent.

- [ ] **Step 3: Implement the service contract**

```go
type Impact struct {
    Summary   string         `json:"summary"`
    ItemCount int            `json:"itemCount"`
    TotalSize int64          `json:"totalSize"`
    Details   map[string]any `json:"details,omitempty"`
}

type Preview struct {
    ToolName          string
    Args              json.RawMessage
    ObjectFingerprint string
    Impact            Impact
}

type Challenge struct {
    RequestState string
    Impact       Impact
    ExpiresAt    time.Time
}

type ConsumeRequest struct {
    RequestState      string
    ToolName          string
    Args              json.RawMessage
    ObjectFingerprint string
    Decision          string
}

func NewService(db *sql.DB, opts ...Option) *Service
func (s *Service) Begin(ctx context.Context, principal aitoken.Principal, preview Preview) (Challenge, error)
func (s *Service) Consume(ctx context.Context, principal aitoken.Principal, req ConsumeRequest) error
```

Use `publicID.secret` for `RequestState`, store only the secret hash, canonicalize JSON before hashing, and atomically change `pending` to `accepted` or `declined`. Never treat `confirm: true` in normal tool arguments as confirmation.

- [ ] **Step 4: Verify GREEN**

Run the command from Step 2. Expected: PASS, including the race/replay case.

### Task 5: Implement short-lived transfer tickets

**Files:**
- Create: `internal/transferticket/types.go`
- Create: `internal/transferticket/service.go`
- Create: `internal/transferticket/service_test.go`
- Modify: `internal/transfer/range.go`
- Create: `internal/transfer/range_test.go`

- [ ] **Step 1: Add failing ticket tests**

Cover hash-only storage, download TTL 10 minutes, upload TTL 30 minutes, clamping to remaining AI Token/upload-session lifetime, wrong operation, expired/closed ticket, revoked AI Token, inactive account, ACL downgrade, boundary change, mount identity drift, object fingerprint drift, byte-budget overflow, reusable download ranges, and upload closure on complete/cancel.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/transferticket ./internal/transfer -count=1
```

Expected: FAIL because the ticket package is absent.

- [ ] **Step 3: Implement issuance and verification**

```go
type Operation string

const (
    OperationDownload Operation = "download"
    OperationUpload   Operation = "upload"
)

func NewService(db *sql.DB, tokens *aitoken.Service, guard *access.Guard, opts ...Option) *Service
func (s *Service) IssueDownload(ctx context.Context, principal aitoken.Principal, locator access.Locator, maxBytes int64) (IssuedTicket, error)
func (s *Service) IssueUpload(ctx context.Context, principal aitoken.Principal, uploadID string, locator access.Locator, maxBytes int64) (IssuedTicket, error)
func (s *Service) Verify(ctx context.Context, bearer string, expected Operation) (VerifiedTicket, error)
func (s *Service) AddBytes(ctx context.Context, ticketID string, count int64) error
func (s *Service) Close(ctx context.Context, ticketID string, status Status) error
```

`Verify` must call `aitoken.Service.RefreshPrincipal`, then AccessGuard, then object/upload state checks. Ticket URLs contain only `publicId`; the secret is returned separately for an `Authorization: Bearer` header.

- [ ] **Step 4: Keep Range accounting safe**

Reuse `internal/transfer/range.go` for HTTP byte ranges. Reject unsatisfiable/multiple unsupported ranges deterministically, increment `consumed_bytes` atomically before writing the range, and allow multiple valid ranges while the ticket remains within TTL, unchanged object fingerprint, and byte budget.

- [ ] **Step 5: Verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 6: Build MemberFileService read operations

**Files:**
- Modify: `internal/files/service.go`
- Modify: `internal/files/service_test.go`
- Create: `internal/memberfiles/types.go`
- Create: `internal/memberfiles/service.go`
- Create: `internal/memberfiles/read.go`
- Create: `internal/memberfiles/read_test.go`

- [ ] **Step 1: Add failing shared-service tests**

Test `ListSpaces`, `ListMounts`, `List`, `Metadata`, `Search`, and `ReadText` with both a session subject and token subject. Assert live ACL filtering, boundary filtering, catalog-result filtering, no host root leakage, a 64 KiB default, a 1 MiB hard limit, and `truncated=true` only when more bytes exist.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/files ./internal/memberfiles -run 'Stat|List|Metadata|Search|ReadText' -count=1
```

Expected: FAIL because `files.Service.Stat` and `memberfiles` are absent.

- [ ] **Step 3: Add low-level Stat and the application service**

Add:

```go
func (Service) Stat(mount Mount, relativePath string) (Entry, error)
```

Then implement:

```go
func NewService(db *sql.DB, guard *access.Guard, catalog catalog.Service, opts ...Option) *Service
func (s *Service) ListSpaces(ctx context.Context, subject access.Subject) ([]Space, error)
func (s *Service) ListMounts(ctx context.Context, subject access.Subject, spaceID string) ([]Mount, error)
func (s *Service) List(ctx context.Context, subject access.Subject, locator access.Locator) (files.DirectoryListing, error)
func (s *Service) Metadata(ctx context.Context, subject access.Subject, locator access.Locator) (files.Entry, error)
func (s *Service) Search(ctx context.Context, subject access.Subject, req SearchRequest) (SearchResult, error)
func (s *Service) ReadText(ctx context.Context, subject access.Subject, locator access.Locator, maxBytes int64) (TextResult, error)
```

Every method must use AccessGuard; none may accept a host filesystem path.

- [ ] **Step 4: Verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 7: Build MemberFileService mutations, upload lifecycle, and trash

**Files:**
- Create: `internal/memberfiles/write.go`
- Create: `internal/memberfiles/trash.go`
- Create: `internal/memberfiles/write_test.go`
- Create: `internal/memberfiles/trash_test.go`
- Modify: `internal/files/service.go`
- Modify: `internal/files/service_test.go`
- Modify: `internal/files/trash_crossmount.go`
- Modify: `internal/files/trash_crossmount_test.go`
- Modify: `internal/transfer/service.go`
- Modify: `internal/transfer/service_test.go`

- [ ] **Step 1: Add failing mutation tests**

Cover directory creation, prepare/status/complete/cancel upload, rename, same-mount and cross-mount copy/move, managed trash/list/restore/purge/empty, external permanent delete, conflicts, read-only mounts, checksum failure, upload target race, and cancellation. Assert no overwrite and no `.omnora/` access.

- [ ] **Step 2: Add failing share-invalidation tests through an interface**

Define a narrow dependency:

```go
type ShareInvalidator interface {
    RevokePath(ctx context.Context, spaceID, mountID, relativePath string) error
}
```

Use a fake to assert that rename, move, trash, and permanent delete invalidate the old path only after the file mutation succeeds; copy must not invalidate the source.

- [ ] **Step 3: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/memberfiles ./internal/files ./internal/transfer -run 'Create|Upload|Rename|Copy|Move|Trash|Delete|Share' -count=1
```

Expected: FAIL because the application mutation methods are absent.

- [ ] **Step 4: Implement the mutation surface**

Add application methods for:

```text
CreateDirectory, PrepareUpload, UploadStatus, CompleteUpload, CancelUpload,
Rename, Copy, Move, Trash, ListTrash, RestoreTrash, PurgeTrash,
EmptyTrash, DeletePermanently
```

Each method receives `access.Subject` plus locators/IDs, performs live AccessGuard checks, calls existing `internal/files` or `internal/transfer` primitives, and returns stable domain errors. `CompleteUpload` must recheck checksum, current permission, mount identity, and target nonexistence immediately before publish.

- [ ] **Step 5: Normalize mutation results**

Return normalized relative paths, object identity/fingerprint, affected item count, and total bytes where known. `EmptyTrash` must calculate count and total bytes before deletion so the confirmation summary can display them accurately.

- [ ] **Step 6: Verify GREEN**

Run the command from Step 3. Expected: PASS.

### Task 8: Build MemberShareService

**Files:**
- Create: `internal/membershare/types.go`
- Create: `internal/membershare/service.go`
- Create: `internal/membershare/service_test.go`
- Modify: `internal/share/service.go`
- Modify: `internal/share/service_test.go`

- [ ] **Step 1: Add failing member-share tests**

Test manager-only creation, creator-or-manager revocation, list visibility, password/options validation, expiry, preview/download flags, visit/download limits, path and identity validation, one-time fragment return on create, no fragment/secret on list, and share invalidation by old file path.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/membershare ./internal/share -count=1
```

Expected: FAIL because `internal/membershare` is absent.

- [ ] **Step 3: Implement the service contract**

```go
func NewService(db *sql.DB, guard *access.Guard, opts ...Option) *Service
func (s *Service) List(ctx context.Context, subject access.Subject, filter ListFilter) ([]Share, error)
func (s *Service) Create(ctx context.Context, subject access.Subject, req CreateRequest) (IssuedShare, error)
func (s *Service) Revoke(ctx context.Context, subject access.Subject, shareID string) error
func (s *Service) RevokePath(ctx context.Context, spaceID, mountID, relativePath string) error
```

Keep `internal/share` responsible for public visitor exchange/session access. `internal/membershare` owns authenticated member management and implements `memberfiles.ShareInvalidator`.

- [ ] **Step 4: Verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 9: Make existing REST handlers use the shared services

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/file_ops.go`
- Modify: `internal/server/member_shares.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/server/file_ops_test.go`
- Modify: `internal/server/member_shares_test.go`
- Modify: `internal/server/loop_gaps_test.go`
- Modify: `internal/server/release_gate_test.go`

- [ ] **Step 1: Add path-parity regression tests**

For list, metadata, create directory, upload complete, rename, copy/move, trash/restore/purge, permanent delete, and share create/list/revoke, assert that existing REST responses and stable errors remain unchanged when the service implementation is swapped. Include viewer/editor/manager and read-only cases.

- [ ] **Step 2: Run the focused server suite as a baseline**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'File|Upload|Download|Trash|Share' -count=1
```

Expected before refactor: PASS. Save the test names/results for comparison.

- [ ] **Step 3: Wire services once in `NewServer`**

Construct AccessGuard, AI Token service, MemberShareService, MemberFileService, confirmation service, transfer-ticket service, and audit recorder as explicit `Server` dependencies. Resolve the `MemberFileService`/`MemberShareService` invalidator dependency during construction; do not create a service per request.

- [ ] **Step 4: Reduce handlers to transport responsibilities**

Each affected REST handler must follow:

```text
authenticate session → decode/validate HTTP input → call shared application service → encode HTTP response
```

Remove duplicated SQL, path-boundary logic, mount identity checks, and `revokeSharesForPath` orchestration from handlers once the shared service owns them.

- [ ] **Step 5: Re-run the baseline and full affected packages**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/memberfiles ./internal/membershare ./internal/server -count=1
```

Expected: PASS with the same REST-observable behavior.

### Task 10: Install the standard MCP transport, authentication, and security shell

**Files:**
- Create: `internal/mcpapi/context.go`
- Create: `internal/mcpapi/server.go`
- Create: `internal/mcpapi/catalog.go`
- Create: `internal/mcpapi/errors.go`
- Create: `internal/mcpapi/server_test.go`
- Create: `internal/server/mcp.go`
- Create: `internal/server/mcp_security.go`
- Create: `internal/server/mcp_security_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/route_groups_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `Dockerfile`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.nas.yml`
- Modify: `deploy/docker-compose.aliyun-test.yml`
- Modify: `deploy/aliyun-test.env.example`

- [ ] **Step 1: Add failing transport/security tests**

Test route disabled `404`, missing/invalid/revoked/expired AI Token `401`, disallowed Host `403`, present but disallowed Origin `403`, request body over 1 MiB `413`, unsupported method `405`, and valid standard discovery/initialize. Assert the legacy `{method,params}` payload no longer succeeds.

- [ ] **Step 2: Run focused tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/mcpapi ./internal/server -run 'MCP|RouteGroup|Host|Origin|BodyLimit' -count=1
```

Expected: FAIL because the standard handler is absent.

- [ ] **Step 3: Implement the official SDK server and Bearer verifier**

Use the exact v1.7.0 APIs:

```go
server := mcp.NewServer(
    &mcp.Implementation{Name: "omnora", Version: implementationVersion},
    &mcp.ServerOptions{},
)

streamable := mcp.NewStreamableHTTPHandler(
    func(*http.Request) *mcp.Server { return server },
    &mcp.StreamableHTTPOptions{
        Stateless:                    true,
        JSONResponse:                 true,
        MaxRequestBodyBytes:          1 << 20,
        PropagateRequestCancellation: true,
    },
)

handler := auth.RequireBearerToken(tokenVerifier, nil)(streamable)
```

Define `const implementationVersion = "0.1.0"` in `internal/mcpapi/server.go` for this release; do not reference a nonexistent build-version variable. A later release-versioning task may centralize this constant with other product version metadata.

The `auth.TokenVerifier` must call `aitoken.Service.VerifyBearer` and return:

```go
&auth.TokenInfo{
    UserID:     principal.AccountID,
    Scopes:     scopeStrings(principal.Scopes),
    Expiration: principal.ExpiresAt,
    Extra:      map[string]any{principalExtraKey: principal},
}
```

Return `auth.ErrInvalidToken` for rejected credentials. Tool handlers read the exact `aitoken.Principal` from `req.Extra.TokenInfo.Extra`; they must not trust only the SDK scope slice.

- [ ] **Step 4: Add explicit MCP network configuration**

Add:

```go
type MCPConfig struct {
    AllowedHosts   []string
    AllowedOrigins []string
    MaxBodyBytes   int64
}
```

Parse comma-separated `OMNORA_MCP_ALLOWED_HOSTS` and `OMNORA_MCP_ALLOWED_ORIGINS`; default `MaxBodyBytes` to `1048576`. Requests without Origin are allowed for non-browser clients. Requests with Origin require an exact configured scheme/host/port match. Host matching is exact after safe normalization and ignores no untrusted forwarding header. If MCP is enabled without an allowed Host, fail closed with `403` and a sanitized configuration error. Pass the variables through each Compose file and document fail-closed defaults in the test env example.

- [ ] **Step 5: Replace route ownership**

Remove `handleMCP` and the `POST /mcp` custom registration from `internal/server/api.go`. Register the official handler for `/mcp`, reserve `/mcp/transfers/` for Task 13, and remove the generic `handleProductGroup(RouteGroupMCP, "/mcp")` fallback so it cannot intercept transfer routes. Keep global request ID, logging, recovery, and security headers outside the MCP-specific middleware chain.

- [ ] **Step 6: Verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 11: Register the 17 read and ordinary-mutation tools

**Files:**
- Create: `internal/mcpapi/types.go`
- Create: `internal/mcpapi/tools_read.go`
- Create: `internal/mcpapi/tools_write.go`
- Create: `internal/mcpapi/tools_transfer.go`
- Create: `internal/mcpapi/tools_share.go`
- Create: `internal/mcpapi/catalog_test.go`
- Create: `internal/mcpapi/tools_test.go`

- [ ] **Step 1: Add failing catalogue/schema tests**

Assert exact names, required scope, JSON Schema 2020-12-compatible typed input/output, lowerCamelCase fields, and annotations for these 17 tools:

```text
spaces.list
mounts.list
files.list
files.metadata
files.search
files.read_text
files.prepare_download
directories.create
files.prepare_upload
uploads.status
uploads.complete
uploads.cancel
files.rename
files.copy
trash.list
trash.restore
shares.list
```

Assert that `tools/list` filters by current AI Token scope and returns `TTLMs=0` plus `CacheScope="private"`. Direct calls must repeat scope and AccessGuard checks even if the tool was listed earlier.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/mcpapi -run 'Catalogue|Schema|Scope|Read|Write|Transfer|Share' -count=1
```

Expected: FAIL because no tools are registered.

- [ ] **Step 3: Register typed tools with the exact handler shape**

```go
mcp.AddTool(
    server,
    &mcp.Tool{
        Name:        spec.Name,
        Title:       spec.Title,
        Description: spec.Description,
        Annotations: spec.Annotations,
    },
    func(
        ctx context.Context,
        req *mcp.CallToolRequest,
        input ToolInput,
    ) (*mcp.CallToolResult, ToolOutput, error) {
        // principal → shared service → typed output
    },
)
```

Use a common locator shape `{spaceId,mountId,path}`. `files.read_text` defaults to 65536 and caps at 1048576. `files.prepare_download` returns URL, ticket bearer header, size, ETag/fingerprint, and expiry. `files.prepare_upload` returns upload ID, part size, URL template, ticket bearer header, and expiry. Existing shares are listed without secrets.

- [ ] **Step 4: Map stable tool errors**

Create a typed business-error output with `code`, `message`, `requestId`, `retryable`, and sanitized `details`. Map at least `forbidden`, `boundary_violation`, `readonly_mount`, `mount_identity_unverifiable`, `not_found`, `conflict`, `ticket_expired`, and `upload_conflict` to `isError:true`. Keep JSON-RPC parse/method/params/internal errors at the protocol layer.

- [ ] **Step 5: Audit ordinary mutations and ticket issuance**

Record MCP audit events for directory creation, upload preparation/completion/cancellation, rename, copy, and download-ticket issuance. Include only credential public ID, tool, normalized object ID/path label, request/trace IDs, and result; do not include the bearer, ticket secret, file content, checksum material, or host path. If a terminal audit write fails after a mutation, invoke the Task 3 readiness-risk path. Keep the seven high-risk intent/outcome sequences in Task 12.

- [ ] **Step 6: Verify GREEN**

Run the command from Step 2. Expected: PASS and exactly 17 ordinary tools present for a full-scope token.

### Task 12: Add MRTR confirmation to all seven high-risk tools

**Files:**
- Create: `internal/mcpapi/confirmation.go`
- Create: `internal/mcpapi/tools_destructive.go`
- Create: `internal/mcpapi/confirmation_test.go`
- Modify: `internal/mcpapi/catalog.go`
- Modify: `internal/mcpapi/server.go`

- [ ] **Step 1: Add failing high-risk catalogue and MRTR tests**

Cover all seven tools, not only permanent deletion:

```text
files.move
files.trash
trash.purge
trash.empty
files.delete_permanently
shares.create
shares.revoke
```

For each, assert first-call `input_required`, accurate impact text, no mutation before acceptance, new JSON-RPC request ID on retry, same original args, `inputResponses`, `requestState`, reauthorization, fingerprint recheck, accepted execution, decline, timeout, changed args, changed object, changed token, replay, and concurrent replay.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/mcpapi -run 'Confirmation|Move|Trash|Purge|DeletePermanently|ShareCreate|ShareRevoke' -count=1
```

Expected: FAIL because the seven handlers/MRTR flow are absent.

- [ ] **Step 3: Implement the shared MRTR gate**

```go
func (a *Adapter) requireConfirmation(
    ctx context.Context,
    req *mcp.CallToolRequest,
    principal aitoken.Principal,
    toolName string,
    args any,
    preview confirmation.Preview,
) (*mcp.CallToolResult, bool, error)
```

On the first call, persist the challenge and return only:

```go
&mcp.CallToolResult{
    InputRequests: mcp.InputRequestMap{
        "confirm": &mcp.ElicitParams{
            Message: preview.Impact.Summary,
            RequestedSchema: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "confirmed": map[string]any{"type": "boolean"},
                },
                "required": []string{"confirmed"},
            },
        },
    },
    RequestState: challenge.RequestState,
}
```

Do not include `Content` or structured output on this response; the SDK sets wire `resultType: "input_required"` automatically.

Record a sanitized `confirmation_requested` audit event containing the confirmation public ID, tool, target label, item count, and expiry, never `RequestState` or its secret component.

- [ ] **Step 4: Consume acceptance safely**

On retry, require `*mcp.ElicitResult`, `Action == "accept"`, and `Content["confirmed"] == true`. Re-run full AccessGuard and preview, atomically consume the bound challenge, write the MCP intent, execute once, and write the terminal outcome. Decline/cancel returns a stable `confirmation_declined` tool result, records a sanitized `confirmation_declined` audit event, and performs no mutation.

- [ ] **Step 5: Filter incapable clients and fail closed**

Add a `mcp.Server.AddReceivingMiddleware` wrapper around `tools/list`. Inspect `*mcp.ListToolsRequest`, `req.ProtocolVersion()`, and `req.ClientCapabilities()`. Treat form Elicitation as available only when `Capabilities.Elicitation != nil` and either `Form != nil` or both `Form` and `URL` are nil (the SDK's backward-compatible form default); URL-only Elicitation is insufficient. The SDK may adapt MRTR for `2025-11-25` clients with that reliable form capability; omit high-risk tools for clients without it. A direct call without reliable form Elicitation returns `client_capability_required` and never accepts a boolean fallback.

- [ ] **Step 6: Verify GREEN**

Run the command from Step 2. Expected: PASS and 24 total tools for a full-scope, confirmation-capable client.

### Task 13: Add guarded download and multipart-upload HTTP routes

**Files:**
- Create: `internal/server/mcp_transfers.go`
- Create: `internal/server/mcp_transfers_test.go`
- Modify: `internal/server/mcp.go`
- Modify: `internal/server/server.go`
- Modify: `internal/transfer/service.go`
- Modify: `internal/transfer/service_test.go`

- [ ] **Step 1: Add failing HTTP transfer tests**

Test:

```text
GET /mcp/transfers/{publicId}
PUT /mcp/transfers/{publicId}/parts/{partNumber}
```

Cover missing/wrong ticket bearer, ticket secret in query rejection, expired/closed ticket, wrong operation, route group disabled, revoked AI Token, ACL downgrade, boundary change, mount identity drift, ETag drift, valid `200`/`206`, invalid `416`, byte budget, part number/size validation, repeated part policy, upload complete, and upload cancel.

- [ ] **Step 2: Run tests and verify RED**

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./internal/server -run 'MCPTransfer|Range|UploadTicket' -count=1
```

Expected: FAIL because the routes are absent.

- [ ] **Step 3: Implement download streaming**

Validate the transfer-ticket bearer, revalidate live AI Token and AccessGuard state, verify object identity, account bytes atomically, then stream with `Accept-Ranges`, `ETag`, `Content-Length`, and correct `Content-Range`. Never log the Authorization header or ticket secret.

- [ ] **Step 4: Implement upload parts**

Validate ticket/upload ownership and expiry on every part, cap request bytes to the session part size/remaining declared size, and delegate to `internal/transfer.Service.WritePart`. `uploads.complete` and `uploads.cancel` remain MCP tools; completion/cancellation closes the ticket immediately.

- [ ] **Step 5: Audit ticket consumption without credentials**

Record download-ticket and upload-part intent/outcome using ticket public ID, operation, normalized target label, range/part number, byte count, request ID, and result. Block the transfer if its pre-operation audit record cannot be written. Never include the ticket bearer, AI Token, checksum, host root, or response bytes.

- [ ] **Step 6: Verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 14: Update the member/admin frontend MCP surfaces

**Files:**
- Create: `web/src/member/mcpIntegration.ts`
- Create: `web/src/member/mcpIntegration.test.ts`
- Modify: `web/src/api.ts`
- Modify: `web/src/member/MemberTokensPanel.tsx`
- Modify: `web/src/member/AdminWorkspace.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/i18n.test.ts`
- Modify: `web/src/member/member-files.css`

- [ ] **Step 1: Add failing pure-contract tests**

In `mcpIntegration.test.ts`, lock `MCP_PATH='/mcp'`, protocol `2026-07-28`, transport `Streamable HTTP`, era `modern`, OAuth `NOT IMPLEMENTED`, same-origin endpoint generation, connection-text generation, stable scope ordering, and this preset mapping:

```text
read-only:
  spaces:read, files:list, files:metadata, files:text,
  files:download_ticket, search:read

file-management:
  read-only + uploads:create, files:write, files:trash,
  trash:read, files:restore

share-management:
  read-only + shares:read, shares:create, shares:revoke

permanent-delete:
  files:purge only; separate opt-in, false by default
```

- [ ] **Step 2: Run frontend tests and verify RED**

```bash
cd web
npm test -- --run src/member/mcpIntegration.test.ts src/member/i18n.test.ts
```

Expected: FAIL because the integration module and new locale keys do not exist.

- [ ] **Step 3: Tighten API types and update the Token page**

Add an `AiTokenScope` union containing all 15 scopes and use it in `CreateAiTokenPayload`. Replace the `unknown` issue response with this exact shape:

```ts
type CreateAiTokenResponse = {
  token: {
    id: string;
    publicId: string;
    name: string;
    scopes: AiTokenScope[];
    expiresAt: string;
  };
  secret: string;
  bearerToken: string;
};
```

In `MemberTokensPanel`, reuse `getBootstrap()` to show current MCP route state, endpoint, protocol, Bearer auth, and OAuth status. Replace the old read/upload checkboxes with the four presets. Keep permanent delete independent and warn that every high-risk operation still requires MRTR confirmation.

- [ ] **Step 4: Expand the one-time creation result**

Keep the bearer visible once, plus endpoint, transport, modern era, `Authorization: Bearer <AI_TOKEN>`, and a copyable Inspector connection block. Clear the bearer and derived connection block from component state when the modal closes.

- [ ] **Step 5: Correct the admin route-group display**

Describe MCP as standard Streamable HTTP `2026-07-28` with restricted AI Tokens. Display `${window.location.origin}/mcp` instead of treating `group.entry === "http"` as a URL. Add copy feedback and state that disabling the group rejects the next request. Describe OpenAPI as raw OpenAPI 3.1 YAML, not Swagger/ReDoc.

- [ ] **Step 6: Add Chinese/English strings and focused styles**

Maintain identical locale keys. Add only MCP-specific status card, preset grid, high-risk warning, connection block, and copy-feedback styles; do not introduce a generic UI utility layer.

- [ ] **Step 7: Verify GREEN and production build**

```bash
cd web
npm test -- --run
npm run build
```

Expected: all tests pass and TypeScript/Vite build exits 0.

### Task 15: Synchronize OpenAPI, MCP/API docs, security, and product contracts

**Files:**
- Modify: `openapi/omnora.v1.yaml`
- Generated: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/mcp/README.md`
- Modify: `docs/api/README.md`
- Modify: `README.md`
- Modify: `docs/README.md`
- Modify: `docs/design/architecture.md`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/design/web-application-design.md`
- Modify: `docs/security/security-model.md`
- Modify: `docs/requirements/product-requirements.md`
- Modify: `docs/verification/acceptance-criteria.md`
- Modify: `scripts/verification/verify-api-docs.sh`
- Create: `internal/mcpapi/docs_contract_test.go`

- [ ] **Step 1: Replace the obsolete OpenAPI MCP envelope**

Remove the old `required: [method]`, custom method enum, and custom MCP response schema. Keep `/mcp` only as a transport-level operation whose description points to the MCP guide and whose JSON body is not falsely modeled as the old envelope. Do not duplicate all 24 Tool schemas into OpenAPI.

- [ ] **Step 2: Add ordinary HTTP transfer contracts**

Document the exact Task 13 GET/PUT paths, Range/ETag/status responses, maximum request behavior, and a distinct `ticketBearer` security scheme. Extend the AI Token create scope enum to all 15 scopes. Make it explicit that `/mcp` uses AI Token Bearer while transfer endpoints use Transfer Ticket Bearer.

- [ ] **Step 3: Rewrite the MCP and REST guides from runtime behavior**

`docs/mcp/README.md` must document `2026-07-28`, tested `2025-11-25` compatibility, AI Token auth, OAuth `NOT IMPLEMENTED`, all 24 tools, all 15 scopes, scope filtering, MRTR for seven tools, transfer tickets, stable errors, Inspector modern setup, and route-group behavior. `docs/api/README.md` must remove the private envelope and distinguish session Cookie, MCP AI Token, and transfer ticket authentication.

- [ ] **Step 4: Update architecture/security/product/acceptance documents**

Remove statements that first-version AI Tokens cannot delete/share. Document shared application services, AccessGuard, three isolated secret types, live ticket revalidation, confirmation anti-replay, audit/readiness behavior, non-goal administrator tools, and separate acceptance labels:

```text
MCP Wire Protocol: implemented and protocol-tested
Omnora AI Token Authorization: implemented, non-OAuth
MCP OAuth Authorization Profile: NOT IMPLEMENTED
```

- [ ] **Step 5: Make the Go catalogue the drift-check source**

In `docs_contract_test.go`, read the MCP guide, OpenAPI source, security model, requirements, and acceptance criteria. Assert every `catalog.go` tool/scope appears where required, the old `{method,params}` contract is absent, transfer paths exist, and OAuth is explicitly not implemented. Update `verify-api-docs.sh` to run this test and remove its old six-method/private-envelope checks.

- [ ] **Step 6: Sync the embedded OpenAPI copy**

```bash
scripts/verification/sync-openapi-asset.sh sync
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
```

Expected: source and embedded OpenAPI are byte-identical; documentation verification exits 0.

### Task 16: Add protocol, Inspector, and release acceptance gates

**Files:**
- Create: `internal/server/mcp_e2e_test.go`
- Create: `internal/server/mcp_protocol_negative_test.go`
- Create: `scripts/verification/verify-mcp-protocol.sh`
- Create: `scripts/verification/verify-mcp-inspector.sh`
- Create: `docs/verification/mcp-inspector-checklist.md`
- Modify: `scripts/verification/release-gate.sh`
- Modify: `scripts/verification/release-readiness-checklist.md`

- [ ] **Step 1: Add official SDK Client end-to-end tests**

Use `mcp.NewClient`, `mcp.StreamableClientTransport`, and an HTTP `RoundTripper` that adds the AI Token header without logging it. Assert default negotiation returns `2026-07-28`, discovery succeeds, the full-scope/form-Elicitation client lists exactly 24 tools, a read call returns typed structured content, and the SDK automatically completes `input_required → ElicitationHandler → retry` for one high-risk test operation.

- [ ] **Step 2: Add legacy and raw HTTP protocol tests**

Use raw JSON-RPC/HTTP only for behavior the public SDK test API cannot force: negotiate `2025-11-25`, complete initialize/tool lifecycle, assert high-risk tools are absent without form Elicitation, and cover parse error, invalid request, method not found, invalid params, malformed protocol headers, body limit, Origin/Host rejection, and stateless method behavior. Never implement a second protocol stack in production.

- [ ] **Step 3: Add cross-channel security and parity tests**

Cover Token revocation, account disable, ACL downgrade, route disable, read-only change, identity drift, Range download, resumable upload, all confirmation replay variants, audit intent/outcome, and REST/MCP equivalence for representative read/write/share operations.

- [ ] **Step 4: Create the automated protocol verifier**

`verify-mcp-protocol.sh` must run the MCP packages and server E2E/negative suites with writable Go caches, then run the catalogue/docs contract. It must be deterministic and require no external client or network.

- [ ] **Step 5: Pin Inspector v2.1.0 smoke verification**

`verify-mcp-inspector.sh` must require `OMNORA_MCP_URL` and a short-lived, scoped `OMNORA_MCP_AI_TOKEN`, never echo either secret, and invoke the pinned official CLI for `initialize` and `tools/list`:

```bash
npx --yes @modelcontextprotocol/inspector@2.1.0 --cli \
  "$OMNORA_MCP_URL" \
  --transport http \
  --method initialize \
  --header "Authorization: Bearer $OMNORA_MCP_AI_TOKEN" \
  --format json
```

Repeat with `--method tools/list`; validate modern protocol and the expected scope-filtered catalogue. Document that the short-lived token is visible to the local process table while Inspector runs, so this script is an operator acceptance check rather than an always-on shared-runner gate.

- [ ] **Step 6: Add the Inspector Web checklist**

Using MCP Inspector v2.1.0 in modern mode, require evidence for connection, protocol `2026-07-28`, scope-filtered tools, one read, one upload/download ticket flow, all seven confirmation prompts, decline/no-mutation, stale/replay failure, and OAuth shown as not implemented. The checklist must not mention or require Claude Desktop, Cursor, ChatGPT, or any other product client.

- [ ] **Step 7: Integrate deterministic gates and run full verification**

Add `verify-mcp-protocol.sh` to `release-gate.sh`. Keep live Inspector verification conditional on supplied URL/token and a completed operator checklist.

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache \
GOMODCACHE=/private/tmp/omnora-go-modcache \
go test ./...

cd web
npm test -- --run
npm run build
cd ..

scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
scripts/verification/verify-mcp-protocol.sh
scripts/verification/release-gate.sh
git diff --check
```

Expected: every deterministic command exits 0. If the local environment blocks listener tests, record that limitation and rerun the unskipped suite in CI or an environment that permits loopback listeners before claiming release acceptance.

- [ ] **Step 8: Perform final security and scope review**

Verify all 24 tools and exactly 15 scopes; seven high-risk tools use MRTR; no admin control-plane tool exists; the old private protocol is absent; Bearer/ticket/confirmation secrets are isolated; no host path or secret leaks; REST and MCP share application services; OpenAPI describes only the transport/HTTP endpoints it can model; frontend wording does not claim OAuth; and no commit was created without explicit user instruction.
