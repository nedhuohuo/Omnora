# Admin Mount Governance Space Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the broken Space-scoped administrator mount flow with account-level mount governance, grants, indexing, and correct REST fallback semantics so the mount page no longer shows Space or returns HTTP 501.

**Architecture:** Add a focused `internal/mountadmin` service that owns common-mount visibility, mutation, grant, identity, and cleanup rules. Thin HTTP adapters register exact routes and map typed service errors, while dedicated React panels consume an explicit Space-free API contract. Keep later member/share Space migration out of this phase and leave legacy handlers unreachable until the final cleanup phase.

**Tech Stack:** Go 1.24, `net/http` ServeMux, SQLite/modernc, React 19, TypeScript, Vitest, Vite, OpenAPI 3.1.

---

## File Structure

### New files

- `internal/mountadmin/service.go` — mount governance types, visibility, validation, mutations, grants, re-verification, and deletion cleanup.
- `internal/mountadmin/service_test.go` — service-level tests against the migration-013 schema.
- `internal/server/admin_mounts_v2.go` — thin HTTP handlers and service-error mapping for the new admin mount/grant endpoints.
- `internal/server/admin_mounts_v2_test.go` — HTTP contract, authorization, restricted visibility, and no-501 tests.
- `web/src/member/AdminMountsPanel.tsx` — Space-free mount creation, list, settings, and grants UI.
- `web/src/member/AdminIndexJobsPanel.tsx` — mount-only index task UI.
- `web/src/member/adminMounts.test.tsx` — SSR component contract and role-visibility tests.

### Modified files

- `internal/server/server.go` — initialize `mountadmin.Service`, register REST fallback separately from generic product placeholders.
- `internal/server/api.go` — register exact admin routes, add `isInitialAdmin`, and migrate index job payload/response filtering.
- `internal/server/admin_control.go` — filter overview counts for restricted mounts.
- `internal/server/hostdirs.go` — expose only configured external mount roots.
- `internal/server/api_test.go`, `server_test.go`, `hostdirs_test.go`, `mount_allowlist_test.go`, `mount_admin_test.go` — replace skipped Space fixtures with account-mount fixtures.
- `internal/mcpapi/docs_contract_test.go` — freeze the explicit admin mount OpenAPI contract.
- `openapi/omnora.v1.yaml` and `internal/server/openapi_assets/omnora.v1.yaml` — synchronize exact management schemas and grant routes.
- `web/src/api.ts`, `web/src/api.test.ts` — Space-free TypeScript DTOs and request serialization.
- `web/src/member/AdminWorkspace.tsx` — delegate mount/index tabs to focused panels and remove their Space state.
- `web/src/member/MemberFilesApp.tsx` — retain `isInitialAdmin` from session and pass it to the admin workspace.
- `web/src/member/i18n.ts`, `i18n.test.ts` — new mount governance/grant strings and removal of mount-page Space strings.
- `docs/api/README.md` — document admin mount and grant routes.

### Intentionally unchanged in this phase

- `web/src/member/MemberFilesApp.tsx` legacy member file-operation branch beyond the `isInitialAdmin` prop.
- `web/src/member/AdminPanels.tsx` hidden Space ACL panel.
- Space-scoped share/file handlers and tests assigned to phases 2–4.
- Negative guards that explicitly reject `spaceId/space_id`.

---

### Task 1: Create the mount governance read and visibility service

**Files:**
- Create: `internal/mountadmin/service.go`
- Create: `internal/mountadmin/service_test.go`

- [ ] **Step 1: Write failing visibility tests**

Create migration-013 fixtures containing a personal default mount, a normal common mount, a restricted common mount, an initial administrator, and an ordinary administrator. The core tests must express the desired service API:

```go
func TestListMountsFiltersPersonalAndRestrictedMounts(t *testing.T) {
    fixture := newFixture(t)
    fixture.insertMount("normal", "Normal", domain.MountGovernanceNormal)
    fixture.insertMount("restricted", "Restricted", domain.MountGovernanceRestricted)

    ordinary, err := fixture.service.ListMounts(context.Background(), fixture.ordinaryAdmin.ID)
    if err != nil {
        t.Fatal(err)
    }
    if got := mountIDs(ordinary); !slices.Equal(got, []string{"normal"}) {
        t.Fatalf("ordinary mounts = %v", got)
    }

    initial, err := fixture.service.ListMounts(context.Background(), fixture.initialAdmin.ID)
    if err != nil {
        t.Fatal(err)
    }
    if got := mountIDs(initial); !slices.Equal(got, []string{"normal", "restricted"}) {
        t.Fatalf("initial mounts = %v", got)
    }
}

func TestLoadGovernableMountHidesRestrictedFromOrdinaryAdmin(t *testing.T) {
    fixture := newFixture(t)
    fixture.insertMount("restricted", "Restricted", domain.MountGovernanceRestricted)

    _, err := fixture.service.LoadMount(context.Background(), fixture.ordinaryAdmin.ID, "restricted")
    if !errors.Is(err, mountadmin.ErrNotFound) {
        t.Fatalf("LoadMount error = %v, want ErrNotFound", err)
    }
}
```

The fixture must open a fresh SQLite database with `store.OpenSQLite`, create accounts with `identity.Service`, persist `system_state.initial_admin_account_id`, and point the service at a real workspace-local external root.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/mountadmin -run 'TestListMounts|TestLoadGovernableMount' -v
```

Expected: FAIL because `mountadmin.New`, `ListMounts`, `LoadMount`, `Mount`, and `ErrNotFound` do not exist.

- [ ] **Step 3: Implement typed records and visibility**

Add these public contracts to `service.go`:

```go
var (
    ErrNotFound             = errors.New("mount not found")
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

type Service struct {
    db           *sql.DB
    externalRoot string
}

func New(db *sql.DB, externalRoot string) *Service {
    return &Service{db: db, externalRoot: filepath.Clean(strings.TrimSpace(externalRoot))}
}
```

Implement `isInitialAdmin`, `ListMounts`, and `LoadMount` with migration-013 columns only. The list query must be equivalent to:

```sql
SELECT m.id, m.display_name, m.root_path, m.governance, m.mode,
       m.index_enabled, m.share_enabled, m.status, COUNT(mg.account_id)
FROM mounts m
LEFT JOIN mount_grants mg ON mg.mount_id = m.id
WHERE m.purpose = 'common'
  AND m.status <> 'deleted'
  AND (m.governance = 'normal' OR ? = 1)
GROUP BY m.id
ORDER BY lower(m.display_name), m.id
```

`LoadMount` must return `ErrNotFound` both for a missing mount and for a restricted mount queried by an ordinary administrator.

- [ ] **Step 4: Run tests and verify GREEN**

Run:

```bash
go test ./internal/mountadmin -run 'TestListMounts|TestLoadGovernableMount' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mountadmin/service.go internal/mountadmin/service_test.go
git commit -m "feat: add account mount governance service"
```

---

### Task 2: Add create, update, and grant mutations with TDD

**Files:**
- Modify: `internal/mountadmin/service.go`
- Modify: `internal/mountadmin/service_test.go`

- [ ] **Step 1: Write failing create and grant tests**

Add tests for zero-grant creation, explicit initial grants, no implicit creator grant, ordinary-admin restricted denial, hidden conflict normalization, mutable settings, and `viewer/editor` only:

```go
func TestCreateMountAllowsZeroGrantsWithoutImplicitAdminAccess(t *testing.T) {
    fixture := newFixture(t)
    root := fixture.externalDirectory("photos")

    created, err := fixture.service.CreateMount(context.Background(), fixture.ordinaryAdmin.ID, mountadmin.CreateRequest{
        ID: "mnt-photos", DisplayName: "Photos", RootPath: root,
        Governance: domain.MountGovernanceNormal,
        Mode: domain.MountModeReadWrite,
        IndexEnabled: true,
    }, fixture.audit())
    if err != nil {
        t.Fatal(err)
    }
    if created.GrantCount != 0 {
        t.Fatalf("grant count = %d, want 0", created.GrantCount)
    }
    assertRowCount(t, fixture.db, `SELECT COUNT(*) FROM mount_grants WHERE mount_id = 'mnt-photos'`, 0)
}

func TestCreateMountStoresExplicitViewerAndEditorGrants(t *testing.T) {
    fixture := newFixture(t)
    created, err := fixture.service.CreateMount(context.Background(), fixture.initialAdmin.ID, mountadmin.CreateRequest{
        ID: "mnt-shared", DisplayName: "Shared", RootPath: fixture.externalDirectory("shared"),
        Governance: domain.MountGovernanceRestricted,
        Mode: domain.MountModeReadOnly,
        Grants: []mountadmin.GrantInput{
            {AccountID: fixture.member.ID, Permission: domain.ContentPermissionViewer},
            {AccountID: fixture.ordinaryAdmin.ID, Permission: domain.ContentPermissionEditor},
        },
    }, fixture.audit())
    if err != nil || created.GrantCount != 2 {
        t.Fatalf("created = %#v, err = %v", created, err)
    }
}

func TestOrdinaryAdminCannotCreateRestrictedMount(t *testing.T) {
    fixture := newFixture(t)
    _, err := fixture.service.CreateMount(context.Background(), fixture.ordinaryAdmin.ID, mountadmin.CreateRequest{
        ID: "mnt-secret", DisplayName: "Secret", RootPath: fixture.externalDirectory("secret"),
        Governance: domain.MountGovernanceRestricted, Mode: domain.MountModeReadOnly,
    }, fixture.audit())
    if !errors.Is(err, mountadmin.ErrRestrictedGovernance) {
        t.Fatalf("error = %v", err)
    }
}

func TestGrantMutationsOnlyAcceptViewerOrEditor(t *testing.T) {
    fixture := newFixture(t)
    fixture.insertMount("normal", "Normal", domain.MountGovernanceNormal)
    if _, err := fixture.service.PutGrant(context.Background(), fixture.ordinaryAdmin.ID, "normal", fixture.member.ID, domain.ContentPermission("manager"), fixture.audit()); !errors.Is(err, mountadmin.ErrInvalidInput) {
        t.Fatalf("manager grant error = %v", err)
    }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./internal/mountadmin -run 'TestCreateMount|TestOrdinaryAdmin|TestGrantMutations' -v
```

Expected: FAIL because mutation requests and methods are undefined.

- [ ] **Step 3: Implement mutation contracts**

Add:

```go
type GrantInput struct {
    AccountID  string
    Permission domain.ContentPermission
}

type Grant struct {
    AccountID  string
    Email      string
    DisplayName string
    Permission domain.ContentPermission
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

type AuditEvent struct {
    Action   string
    TargetID string
    Metadata string
}

type AuditWriter func(context.Context, *sql.Tx, AuditEvent) error
```

Implement `CreateMount`, `UpdateMount`, `ListGrants`, `PutGrant`, and `DeleteGrant`. Creation must validate and insert these fixed classifications:

```sql
INSERT INTO mounts(
    id, display_name, root_path, purpose, storage_kind, governance,
    mode, index_enabled, share_enabled, status, mount_identity_json
) VALUES (?, ?, ?, 'common', 'external', ?, ?, ?, 1, 'active', ?)
```

Required implementation details:

- normalize display names with trim, control-character rejection, and 128-rune limit;
- accept only `normal|restricted` and `read_only|read_write`;
- verify the candidate is under `externalRoot` and compare its `mountid.Identity` against every non-deleted mount;
- probe write access for `read_write`;
- reject duplicate grant accounts before starting the transaction;
- query every grant target as an active account;
- use one transaction for mount, grants, and audit;
- map any conflict encountered by an ordinary administrator to `ErrMountUnavailable`, and a conflict visible to the initial administrator to `ErrMountConflict`;
- roll back the mount and every initial grant when the audit callback fails;
- never auto-insert a creator/admin grant;
- implement `DeleteGrant` as idempotent;
- merge patch values with the current record and update only `display_name`, `mode`, `index_enabled`, `share_enabled`, and `updated_at`.

- [ ] **Step 4: Verify the mutation suite passes**

Run:

```bash
go test ./internal/mountadmin -run 'TestCreateMount|TestOrdinaryAdmin|TestGrantMutations|TestUpdateMount' -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mountadmin/service.go internal/mountadmin/service_test.go
git commit -m "feat: manage common mounts and grants"
```

---

### Task 3: Add re-verification and non-destructive deletion

**Files:**
- Modify: `internal/mountadmin/service.go`
- Modify: `internal/mountadmin/service_test.go`

- [ ] **Step 1: Write failing lifecycle tests**

Add tests proving failed re-verification does not reactivate a mount, successful re-verification refreshes identity, deletion requires the exact display name, physical files survive, and derived control-plane rows are invalidated:

```go
func TestDeleteMountKeepsFilesAndRevokesDerivedState(t *testing.T) {
    fixture := newFixture(t)
    root := fixture.externalDirectory("archive")
    keep := filepath.Join(root, "keep.txt")
    if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
        t.Fatal(err)
    }
    fixture.insertActiveMountWithDerivedRows("archive", "Archive", root)

    result, err := fixture.service.DeleteMount(context.Background(), fixture.initialAdmin.ID, "archive", "Archive", fixture.audit())
    if err != nil {
        t.Fatal(err)
    }
    if !result.Deleted || result.DeleteData || result.DataDeleted {
        t.Fatalf("result = %#v", result)
    }
    if _, err := os.Stat(keep); err != nil {
        t.Fatalf("physical file was removed: %v", err)
    }
    assertRowCount(t, fixture.db, `SELECT COUNT(*) FROM mount_grants WHERE mount_id='archive'`, 0)
    assertRowCount(t, fixture.db, `SELECT COUNT(*) FROM ai_token_boundaries WHERE mount_id='archive'`, 0)
}

func TestDeleteMountRequiresExactDisplayName(t *testing.T) {
    fixture := newFixture(t)
    fixture.insertMount("normal", "Normal", domain.MountGovernanceNormal)
    _, err := fixture.service.DeleteMount(context.Background(), fixture.ordinaryAdmin.ID, "normal", "normal", fixture.audit())
    if !errors.Is(err, mountadmin.ErrConfirmationRequired) {
        t.Fatalf("error = %v", err)
    }
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
go test ./internal/mountadmin -run 'TestDeleteMount|TestReverifyMount' -v
```

Expected: FAIL because lifecycle methods are missing.

- [ ] **Step 3: Implement lifecycle methods**

Add:

```go
type Deletion struct {
    ID          string
    Deleted     bool
    DeleteData  bool
    DataDeleted bool
}

func (s *Service) ReverifyMount(ctx context.Context, actorID, mountID string, audit AuditWriter) (Mount, error)
func (s *Service) DeleteMount(ctx context.Context, actorID, mountID, displayName string, audit AuditWriter) (Deletion, error)
```

`ReverifyMount` must load all identities except the target, call `mountid.VerifyCandidateRoot`, probe writable mode, update `mount_identity_json/status/updated_at`, and audit in one transaction. It must not update immutable `root_path`.

`DeleteMount` must perform this transaction in order:

```sql
DELETE FROM mount_grants WHERE mount_id = ?;
DELETE FROM ai_token_boundaries WHERE mount_id = ?;
UPDATE shares SET revoked_at = COALESCE(revoked_at, ?), updated_at = ? WHERE mount_id = ?;
UPDATE share_download_tickets SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?) WHERE mount_id = ? AND status IN ('issued','streaming');
UPDATE mcp_transfer_tickets SET status = 'canceled', closed_at = COALESCE(closed_at, ?) WHERE mount_id = ? AND status = 'active';
UPDATE upload_sessions SET status = 'canceled', canceled_at = COALESCE(canceled_at, ?) WHERE mount_id = ? AND status = 'active';
DELETE FROM file_operations WHERE source_mount_id = ? OR destination_mount_id = ?;
DELETE FROM catalog_entries WHERE mount_id = ?;
DELETE FROM file_objects WHERE mount_id = ?;
DELETE FROM mount_identity_claims WHERE mount_id = ?;
DELETE FROM mount_claim_conflicts WHERE mount_id = ? OR conflicting_mount_id = ?;
UPDATE mounts SET status = 'deleted', display_name = ?, updated_at = ? WHERE id = ? AND status <> 'deleted';
```

Cancel queued/running/paused jobs whose JSON payload contains the exact mount ID using the existing dual-key search. Generate a unique tombstone display name with the existing 128-rune rule. Do not call any filesystem deletion API.

- [ ] **Step 4: Run service tests**

Run:

```bash
go test ./internal/mountadmin -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mountadmin/service.go internal/mountadmin/service_test.go
git commit -m "feat: complete mount governance lifecycle"
```

---

### Task 4: Register exact HTTP routes, expose initial-admin capability, and remove REST 501

**Files:**
- Create: `internal/server/admin_mounts_v2.go`
- Create: `internal/server/admin_mounts_v2_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write failing HTTP and fallback tests**

Cover all exact route methods, JSON field names, ordinary/restricted behavior, session capability, legacy 410, unknown 404, and disabled-route precedence:

```go
func TestAdminMountRoutesUseAccountMountContract(t *testing.T) {
    fixture := newAdminMountHTTPFixture(t)
    body := `{"displayName":"Photos","rootPath":` + quoteJSON(fixture.externalDirectory("photos")) + `,"governance":"normal","mode":"read_only","indexEnabled":true,"grants":[]}`
    rec := fixture.request(http.MethodPost, "/api/v1/admin/mounts", body, fixture.initialAdmin)
    if rec.Code != http.StatusCreated {
        t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
    }
    if strings.Contains(rec.Body.String(), "space") {
        t.Fatalf("response contains Space contract: %s", rec.Body.String())
    }
}

func TestRESTFallbackReturnsGoneForSpaceAndNotFoundForUnknown(t *testing.T) {
    handler := New(config.Config{Routes: map[domain.RouteGroup]bool{domain.RouteGroupREST: true}}, nil)
    for _, tc := range []struct{ path string; status int; code string }{
        {"/api/v1/spaces", http.StatusGone, "space_api_removed"},
        {"/api/v1/admin/spaces/legacy", http.StatusGone, "space_api_removed"},
        {"/api/v1/unknown", http.StatusNotFound, "not_found"},
    } {
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
        assertErrorResponse(t, rec, tc.status, tc.code)
    }
}
```

Extend `sessionResponse` and session tests with `IsInitialAdmin bool` and assert initial admin `true`, ordinary admin/member `false`.

- [ ] **Step 2: Run and verify RED**

Run:

```bash
go test ./internal/server -run 'TestAdminMountRoutes|TestRESTFallback|TestSessionResponses' -v
```

Expected: FAIL because routes still fall through to 501 and `isInitialAdmin` is absent.

- [ ] **Step 3: Initialize the service and register routes**

Add to `Server`:

```go
mountAdmin *mountadmin.Service
```

Initialize it when `db != nil`:

```go
s.mountAdmin = mountadmin.New(db.SQL(), cfg.Storage.PredeclaredMountRoot)
```

Register exact patterns in `apiRoutes()`:

```go
s.mux.Handle("GET /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminListMounts)))
s.mux.Handle("POST /api/v1/admin/mounts", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminCreateMount)))
s.mux.Handle("PATCH /api/v1/admin/mounts/{mountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminUpdateMount)))
s.mux.Handle("DELETE /api/v1/admin/mounts/{mountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminDeleteMount)))
s.mux.Handle("POST /api/v1/admin/mounts/{mountId}/reverify", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminReverifyMount)))
s.mux.Handle("GET /api/v1/admin/mounts/{mountId}/grants", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminListMountGrants)))
s.mux.Handle("PUT /api/v1/admin/mounts/{mountId}/grants/{accountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminPutMountGrant)))
s.mux.Handle("DELETE /api/v1/admin/mounts/{mountId}/grants/{accountId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.adminDeleteMountGrant)))
s.mux.Handle("GET /api/v1/admin/host-directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listAdminHostDirectories)))
s.mux.Handle("GET /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listIndexJobs)))
s.mux.Handle("POST /api/v1/admin/index-jobs", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.enqueueIndexJob)))
s.mux.Handle("POST /api/v1/admin/index-jobs/{jobId}/run", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.runIndexJob)))
```

- [ ] **Step 4: Implement thin HTTP handlers and exact error mapping**

`admin_mounts_v2.go` must define JSON DTOs matching the design and a single mapper:

```go
func (s *Server) writeMountAdminError(w http.ResponseWriter, r *http.Request, err error) {
    switch {
    case errors.Is(err, mountadmin.ErrNotFound):
        httpx.WriteError(w, r, http.StatusNotFound, "not_found", "mount was not found")
    case errors.Is(err, mountadmin.ErrRestrictedGovernance):
        httpx.WriteError(w, r, http.StatusForbidden, "forbidden", "restricted mounts require the initial administrator")
    case errors.Is(err, mountadmin.ErrRootNotAllowed):
        httpx.WriteError(w, r, http.StatusForbidden, "mount_root_not_allowed", "mount root is outside the external storage root")
    case errors.Is(err, mountadmin.ErrMountUnavailable):
        httpx.WriteError(w, r, http.StatusConflict, "mount_unavailable", "mount is unavailable")
    case errors.Is(err, mountadmin.ErrMountConflict):
        httpx.WriteError(w, r, http.StatusConflict, "mount_conflict", "mount conflicts with an existing mount")
    case errors.Is(err, mountadmin.ErrIdentityUnverifiable):
        httpx.WriteError(w, r, http.StatusConflict, "mount_identity_unverifiable", "mount identity could not be verified")
    case errors.Is(err, mountadmin.ErrNotWritable):
        httpx.WriteError(w, r, http.StatusConflict, "mount_not_writable", "mount root is not writable")
    case errors.Is(err, mountadmin.ErrConfirmationRequired):
        httpx.WriteError(w, r, http.StatusConflict, "confirmation_required", "displayName must match the current mount name")
    case errors.Is(err, mountadmin.ErrInvalidInput):
        httpx.WriteError(w, r, http.StatusBadRequest, "invalid_input", err.Error())
    default:
        writeDBError(w, r, err)
    }
}
```

Every handler must call `requireAdmin`, pass `session.AccountID`, use `decodeJSON` with unknown-field rejection, and pass an audit closure that invokes `recordAuditTx` with target type `mount`.

- [ ] **Step 5: Replace the REST product fallback and add `isInitialAdmin`**

In `server.go`, replace `handleProductGroup(REST, "/api/v1")` with `handleRESTFallback()`:

```go
func (s *Server) handleRESTFallback() {
    handler := s.gate(domain.RouteGroupREST, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        clean := path.Clean(r.URL.Path)
        if clean == "/api/v1/spaces" || strings.HasPrefix(clean, "/api/v1/spaces/") ||
            clean == "/api/v1/admin/spaces" || strings.HasPrefix(clean, "/api/v1/admin/spaces/") {
            httpx.WriteError(w, r, http.StatusGone, "space_api_removed", "Space-scoped REST APIs were removed; use account content sources")
            return
        }
        httpx.WriteError(w, r, http.StatusNotFound, "not_found", "route was not found")
    }))
    s.mux.Handle("/api/v1", handler)
    s.mux.Handle("/api/v1/", handler)
}
```

Add `isInitialAdmin` to both login and current-session responses using `initialAdminID(ctx)`. Resolve it before writing cookies or successful response bodies; database failure must remain fail-closed.

- [ ] **Step 6: Run HTTP tests**

Run:

```bash
go test ./internal/server -run 'TestAdminMountRoutes|TestRESTFallback|TestSessionResponses|TestEnabledProductGroupFallbacks' -v
```

Expected: PASS; OpenAPI unknown fallback remains 501 until separately designed, REST unknown is now 404.

- [ ] **Step 7: Commit**

```bash
git add internal/server/server.go internal/server/api.go internal/server/api_test.go internal/server/server_test.go internal/server/admin_mounts_v2.go internal/server/admin_mounts_v2_test.go
git commit -m "feat: expose account mount admin routes"
```

---

### Task 5: Migrate directory suggestions, index tasks, and overview visibility

**Files:**
- Modify: `internal/server/hostdirs.go`
- Modify: `internal/server/hostdirs_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/api_test.go`
- Modify: `internal/server/admin_control.go`
- Modify: `internal/server/admin_control_test.go`
- Modify: `internal/server/mount_allowlist_test.go`
- Modify: `internal/server/mount_admin_test.go`

- [ ] **Step 1: Rewrite skipped tests first**

Remove `skipLegacySpaceRESTTest(t)` only from the mount, host-directory, and index tests covered by this phase. Replace Space fixtures with migration-013 inserts:

```sql
INSERT INTO mounts(id, display_name, root_path, purpose, storage_kind, governance, mode, index_enabled, status, mount_identity_json)
VALUES (?, ?, ?, 'common', 'external', ?, ?, ?, 'active', ?);
```

Add assertions that:

```go
if got := hostDirectoryRootKind(payload.RootDetails, managed); got != "" {
    t.Fatalf("managed personal root leaked: %#v", payload.RootDetails)
}
if got := hostDirectoryRootKind(payload.RootDetails, external); got != "external" {
    t.Fatalf("external root kind = %q", got)
}
if strings.Contains(indexJobResponseBody, "spaceName") {
    t.Fatalf("index response contains Space field: %s", indexJobResponseBody)
}
```

Create an ordinary admin plus restricted mount and verify it is absent from index lists and overview counts and returns 404 for enqueue/run.

- [ ] **Step 2: Run and verify RED**

Run:

```bash
go test ./internal/server -run 'TestAdminHostDirectory|TestEnqueueIndex|TestIndexJob|TestAdminOverview|TestCreateMountEnforces|TestAdminMount' -v
```

Expected: FAIL on managed-root leakage, old Space SQL, missing routes, and `spaceName`.

- [ ] **Step 3: Restrict host-directory suggestions to the external root**

Change `storageRootDetails` to emit only:

```go
func (s *Server) storageRootDetails() []hostDirectoryRoot {
    root := filepath.Clean(strings.TrimSpace(s.cfg.Storage.PredeclaredMountRoot))
    if root == "" || root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
        return []hostDirectoryRoot{}
    }
    return []hostDirectoryRoot{{Path: root, Kind: "external"}}
}
```

Change `listAdminHostDirectories` to use `requireAdmin`. Keep bound-slot and symlink checks, but remove all UI/API behavior that exposes `ManagedDir`.

- [ ] **Step 4: Make index jobs mount-only and governance-aware**

Change `jobResponse` to accept only `mountName` and remove `spaceName`:

```go
func jobResponse(job jobs.Job, claimedAt, claimedBy, lastError, completedAt, mountName string) map[string]any {
    return map[string]any{
        "id": job.ID, "kind": job.Kind, "priority": job.Priority, "status": job.Status,
        "mountName": mountName, "payload": json.RawMessage(job.PayloadJSON),
        "checkpoint": json.RawMessage(job.CheckpointJSON), "attempts": job.Attempts,
        "maxAttempts": job.MaxAttempts, "claimedAt": claimedAt, "claimedBy": claimedBy,
        "lastError": lastError, "createdAt": job.CreatedAt, "updatedAt": job.UpdatedAt,
        "completedAt": completedAt,
    }
}
```

Before listing, enqueueing, or running, resolve the target through `s.mountAdmin.LoadMount(ctx, session.AccountID, mountID)`. Skip hidden jobs in lists; return 404 for direct hidden jobs. Reject non-common, inactive, or index-disabled mounts. Do not permit the personal default mount.

- [ ] **Step 5: Filter overview counts**

After `requireAdmin`, determine initial-admin status and add `governance = 'normal'` to common-mount and health queries for ordinary administrators. The initial administrator may count both normal and restricted common mounts; neither query includes `purpose = 'personal_default'`.

- [ ] **Step 6: Run migrated server tests**

Run:

```bash
go test ./internal/server -run 'TestAdminHostDirectory|TestEnqueueIndex|TestIndexJob|TestAdminOverview|TestCreateMountEnforces|TestAdminMount' -v
```

Expected: PASS without skips in the migrated tests.

- [ ] **Step 7: Commit**

```bash
git add internal/server/hostdirs.go internal/server/hostdirs_test.go internal/server/api.go internal/server/api_test.go internal/server/admin_control.go internal/server/admin_control_test.go internal/server/mount_allowlist_test.go internal/server/mount_admin_test.go
git commit -m "fix: enforce mount governance across admin views"
```

---

### Task 6: Freeze and synchronize the OpenAPI contract

**Files:**
- Modify: `internal/mcpapi/docs_contract_test.go`
- Modify: `openapi/omnora.v1.yaml`
- Modify: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/api/README.md`

- [ ] **Step 1: Add a failing OpenAPI contract test**

Add:

```go
func TestOpenAPIDefinesAdminMountGovernanceContract(t *testing.T) {
    root := docsRoot(t)
    source := readContractDoc(t, root, "openapi/omnora.v1.yaml")
    embedded := readContractDoc(t, root, "internal/server/openapi_assets/omnora.v1.yaml")
    if source != embedded {
        t.Fatal("embedded OpenAPI differs from source")
    }
    for _, required := range []string{
        "  /admin/mounts/{mountId}/grants:",
        "  /admin/mounts/{mountId}/grants/{accountId}:",
        "governance:", "grantCount:", "shareEnabled:",
        "enum: [normal, restricted]", "enum: [viewer, editor]",
    } {
        if !strings.Contains(source, required) {
            t.Errorf("OpenAPI missing %q", required)
        }
    }
    adminMountSection := contractSection(t, source, "    AdminMount:\n", "    MountGrant:\n")
    for _, obsolete := range []string{"spaceId", "kind:", "additionalProperties: true"} {
        if strings.Contains(adminMountSection, obsolete) {
            t.Errorf("admin mount schema contains obsolete/loose contract %q", obsolete)
        }
    }
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
go test ./internal/mcpapi -run TestOpenAPIDefinesAdminMountGovernanceContract -v
```

Expected: FAIL because grant routes and explicit schemas are absent.

- [ ] **Step 3: Update the source OpenAPI**

Define exact `AdminMount`, `CreateAdminMountRequest`, `UpdateAdminMountRequest`, `MountGrant`, `PutMountGrantRequest`, `MountGrantList`, and `MountDeletion` schemas. `CreateAdminMountRequest` must require:

```yaml
required: [displayName, rootPath, governance, mode, indexEnabled, grants]
```

Add responses for `404` restricted hiding, `409 mount_unavailable`, and confirmation-required deletion. Remove loose `additionalProperties: true` from the affected mount paths and remove `spaceName` from `Job`.

- [ ] **Step 4: Synchronize the embedded asset and API guide**

Run:

```bash
cp openapi/omnora.v1.yaml internal/server/openapi_assets/omnora.v1.yaml
```

Document the exact routes, the normal/restricted visibility rule, zero-grant creation, and `dataDeleted: false` in `docs/api/README.md`.

- [ ] **Step 5: Verify the contract**

Run:

```bash
go test ./internal/mcpapi -run 'TestOpenAPI' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/mcpapi/docs_contract_test.go openapi/omnora.v1.yaml internal/server/openapi_assets/omnora.v1.yaml docs/api/README.md
git commit -m "docs: freeze admin mount governance API"
```

---

### Task 7: Replace the frontend admin API and session types

**Files:**
- Modify: `web/src/api.test.ts`
- Modify: `web/src/api.ts`
- Modify: `web/src/member/sessionFlow.test.ts`
- Modify: `web/src/member/MemberFilesApp.tsx`

- [ ] **Step 1: Add failing API serialization tests**

Extend `web/src/api.test.ts`:

```ts
it('creates an account-level mount without a Space selector', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ id: 'mount-1' }), {
    status: 201,
    headers: { 'Content-Type': 'application/json' },
  }));
  vi.stubGlobal('fetch', fetchMock);

  await registerAdminMount({
    displayName: 'Photos',
    rootPath: '/mnt/omnora/photos',
    governance: 'normal',
    mode: 'read_write',
    indexEnabled: true,
    grants: [{ accountId: 'account-1', permission: 'editor' }],
  });

  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/admin/mounts');
  const body = String(fetchMock.mock.calls[0][1]?.body);
  expect(JSON.parse(body)).toEqual({
    displayName: 'Photos', rootPath: '/mnt/omnora/photos', governance: 'normal',
    mode: 'read_write', indexEnabled: true,
    grants: [{ accountId: 'account-1', permission: 'editor' }],
  });
  expect(body).not.toMatch(/spaceId|kind|managed/);
});

it('uses mount-scoped grant routes', async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetchMock);
  await putAdminMountGrant('mount / 1', 'account / 1', 'viewer');
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/admin/mounts/mount%20%2F%201/grants/account%20%2F%201');
});
```

- [ ] **Step 2: Run and verify RED**

Run from `web/`:

```bash
npm test -- --run src/api.test.ts
```

Expected: FAIL because the new payload and grant functions do not exist.

- [ ] **Step 3: Define exact TypeScript contracts and functions**

Replace the old admin mount payload/list types with:

```ts
export type MountGovernance = 'normal' | 'restricted';
export type MountGrantPermission = 'viewer' | 'editor';
export type AdminMountGrantInput = { accountId: string; permission: MountGrantPermission };
export type AdminMountGrant = AdminMountGrantInput & { email: string; displayName: string };
export type AdminMountPayload = {
  displayName: string;
  rootPath: string;
  governance: MountGovernance;
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  grants: AdminMountGrantInput[];
};
export type AdminMountListItem = {
  id: string;
  displayName: string;
  rootPath: string;
  governance: MountGovernance;
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  shareEnabled: boolean;
  status: 'pending' | 'active' | 'disabled' | 'unavailable';
  grantCount: number;
};
```

Add `isInitialAdmin?: boolean` to `SessionPayload`. Implement `updateAdminMount`, `listAdminMountGrants`, `putAdminMountGrant`, and `deleteAdminMountGrant`; change deletion to send `{displayName}`. Remove Space fields from `JobPayload` and admin mount helper types.

- [ ] **Step 4: Wire session capability through the application shell**

In `MemberFilesApp.tsx`, add:

```ts
const [isInitialAdmin, setIsInitialAdmin] = useState(false);
```

Every successful session load/login must set both admin flags; logout and failed session resolution must clear both. Pass the value only to the admin workspace:

```tsx
<AdminWorkspace tab={activeTab as AdminTab} locale={locale} isInitialAdmin={isInitialAdmin} />
```

Do not alter member content-source behavior in this task.

- [ ] **Step 5: Run focused tests and type-check**

Run from `web/`:

```bash
npm test -- --run src/api.test.ts src/member/sessionFlow.test.ts
npm run build
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/api.test.ts web/src/member/sessionFlow.test.ts web/src/member/MemberFilesApp.tsx
git commit -m "feat: add frontend mount governance client"
```

---

### Task 8: Build the Space-free mount panel

**Files:**
- Create: `web/src/member/AdminMountsPanel.tsx`
- Create: `web/src/member/adminMounts.test.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/i18n.test.ts`

- [ ] **Step 1: Write failing render tests**

Create:

```tsx
it('renders account-level mount controls without Space or managed mount choices', () => {
  const html = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin={false} />);
  expect(html).toContain('挂载管理');
  expect(html).toContain('外部目录');
  expect(html).not.toContain('空间');
  expect(html).not.toContain('托管挂载');
  expect(html).not.toContain('受限挂载');
});

it('shows restricted governance only to the initial administrator', () => {
  const ordinary = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin={false} />);
  const initial = renderToStaticMarkup(<AdminMountsPanel locale="zh-CN" isInitialAdmin />);
  expect(ordinary).not.toContain('受限挂载');
  expect(initial).toContain('受限挂载');
});
```

- [ ] **Step 2: Run and verify RED**

Run from `web/`:

```bash
npm test -- --run src/member/adminMounts.test.tsx
```

Expected: FAIL because `AdminMountsPanel` does not exist.

- [ ] **Step 3: Implement loading and creation state**

The panel must load `listAdminMounts`, `listAdminUsers`, and `listAdminHostDirectories('/')` in parallel. Use this form state:

```ts
type MountForm = {
  displayName: string;
  rootPath: string;
  governance: MountGovernance;
  mode: 'read_only' | 'read_write';
  indexEnabled: boolean;
  grants: AdminMountGrantInput[];
};
```

Render only external path suggestions. Provide an initial-grant draft account selector and `viewer/editor` selector; prevent duplicate accounts before adding to `form.grants`. When `isInitialAdmin` is false, force governance to `normal` and omit the restricted option entirely.

- [ ] **Step 4: Implement list, settings, grants, re-verification, and deletion**

The table columns are:

```text
Name | Governance | Mode | Index | Public sharing | Health | Actions
```

Selecting a row loads grants. Support:

- patching display name, mode, index, and `shareEnabled`;
- adding/updating/removing `viewer/editor` grants;
- re-verifying unavailable mounts;
- deleting only after exact display-name confirmation and sending `{displayName}`;
- refreshing only the affected mount/grant data after success;
- preserving form/modal input after structured errors.

Do not import `AdminSpacePayload`, call `listAdminSpaces`, render `mountSpace`, or maintain `spaceId/kind` state.

- [ ] **Step 5: Add bilingual strings and parity assertions**

Add paired zh-CN/en-US keys for external directory, governance, normal/restricted mount, initial grants, grant account, grant permission, no grants, share policy, and exact-name deletion confirmation. Update the i18n test so mount-page strings contain no business Space terminology:

```ts
expect(localeMessages['zh-CN'].mountManagementDetail).not.toContain('空间');
expect(localeMessages['en-US'].mountManagementDetail).not.toMatch(/\bSpace\b/);
```

- [ ] **Step 6: Run component and i18n tests**

Run from `web/`:

```bash
npm test -- --run src/member/adminMounts.test.tsx src/member/i18n.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/member/AdminMountsPanel.tsx web/src/member/adminMounts.test.tsx web/src/member/i18n.ts web/src/member/i18n.test.ts
git commit -m "feat: replace admin Space mount form"
```

---

### Task 9: Extract the mount-only index panel and clean AdminWorkspace

**Files:**
- Create: `web/src/member/AdminIndexJobsPanel.tsx`
- Modify: `web/src/member/adminMounts.test.tsx`
- Modify: `web/src/member/AdminWorkspace.tsx`
- Modify: `web/src/member/i18n.ts`

- [ ] **Step 1: Add a failing index render contract**

Add:

```tsx
it('renders index targets as mounts without Space labels', () => {
  const html = renderToStaticMarkup(<AdminIndexJobsPanel locale="zh-CN" />);
  expect(html).toContain('索引任务');
  expect(html).toContain('挂载');
  expect(html).not.toContain('空间');
});
```

- [ ] **Step 2: Run and verify RED**

Run from `web/`:

```bash
npm test -- --run src/member/adminMounts.test.tsx
```

Expected: FAIL because the panel is missing.

- [ ] **Step 3: Implement the mount-only index panel**

Load `listIndexJobs()` and `listAdminMounts()`; derive eligible mounts with:

```ts
const indexableMounts = mounts.filter((mount) => mount.indexEnabled && mount.status === 'active');
```

Queue with only `{mountId}`. Render `job.mountName` without `spaceName`, and retain queued-job inline run behavior.

- [ ] **Step 4: Remove mount/index Space state from AdminWorkspace**

Change the signature:

```ts
export default function AdminWorkspace({ tab, locale, isInitialAdmin }: {
  tab: AdminTab;
  locale: MemberLocale;
  isInitialAdmin: boolean;
})
```

Return focused panels before the remaining route/audit workspace:

```tsx
if (tab === 'mounts') return <AdminMountsPanel locale={locale} isInitialAdmin={isInitialAdmin} />;
if (tab === 'index-jobs') return <AdminIndexJobsPanel locale={locale} />;
```

Delete from `AdminWorkspace.tsx` all mount-form state, `spaces`, host-directory state, mount create/update/delete handlers, old mount table JSX, index `spaceName` labels, `defaultRootForKind`, and imports for `AdminSpacePayload/listAdminSpaces/registerAdminMount`. Leave hidden `AdminSpacesPanel` cleanup for phase 4.

- [ ] **Step 5: Run frontend tests and build**

Run from `web/`:

```bash
npm test -- --run src/member/adminMounts.test.tsx src/member/i18n.test.ts src/api.test.ts
npm run build
```

Expected: PASS; TypeScript reports no stale admin-mount Space fields.

- [ ] **Step 6: Commit**

```bash
git add web/src/member/AdminIndexJobsPanel.tsx web/src/member/adminMounts.test.tsx web/src/member/AdminWorkspace.tsx web/src/member/i18n.ts
git commit -m "refactor: isolate mount-only admin workspaces"
```

---

### Task 10: Full verification and residual-scope audit

**Files:**
- No planned production changes; verification failures are fixed in the owning file from Tasks 1–9
- Review: `docs/superpowers/specs/2026-08-09-admin-mount-governance-space-removal-design.md`

- [ ] **Step 1: Run formatting and static checks**

```bash
gofmt -w \
  internal/mountadmin/service.go internal/mountadmin/service_test.go \
  internal/server/admin_mounts_v2.go internal/server/admin_mounts_v2_test.go \
  internal/server/server.go internal/server/server_test.go \
  internal/server/api.go internal/server/api_test.go \
  internal/server/admin_control.go internal/server/admin_control_test.go \
  internal/server/hostdirs.go internal/server/hostdirs_test.go \
  internal/server/mount_allowlist_test.go internal/server/mount_admin_test.go \
  internal/mcpapi/docs_contract_test.go
git diff --check
go vet ./...
```

Expected: no output from `git diff --check`; `go vet` exits 0.

- [ ] **Step 2: Run all Go tests**

```bash
go test ./...
```

Expected: all packages PASS. Any remaining skipped legacy tests must belong only to phases 2–4; no mount/host-directory/index test added or migrated in this plan may remain skipped.

- [ ] **Step 3: Run all frontend tests and production build**

```bash
cd web
npm test -- --run
npm run build
```

Expected: all Vitest files PASS and Vite production build succeeds. The existing bundle-size advisory is non-blocking; no TypeScript error is allowed.

- [ ] **Step 4: Run focused residual searches**

From repository root:

```bash
rg -n "spaceId|listAdminSpaces|mountSpace|SpaceName|spaceName" \
  web/src/member/AdminMountsPanel.tsx \
  web/src/member/AdminIndexJobsPanel.tsx \
  web/src/member/AdminWorkspace.tsx \
  internal/mountadmin \
  internal/server/admin_mounts_v2.go

rg -n "route group is enabled but no product handler" internal/server/server.go
```

Expected: first command returns no matches in phase-1 components; the second may still find the generic OpenAPI placeholder implementation but REST routing no longer calls it. Confirm `/api/v1/unknown` is covered by a passing 404 test.

- [ ] **Step 5: Perform an HTTP smoke test against a migrated test server**

Exercise login, session `isInitialAdmin`, mount list, host directories, create with zero grants, grant editor, enqueue index, reverify, and exact-name delete. Confirm:

```text
No response is 501
No successful mount/index response contains spaceId or spaceName
Deletion returns dataDeleted=false
The external test file remains on disk
```

Use the automated HTTP tests as the recorded smoke evidence; do not add an unauthenticated production backdoor or fixture endpoint.

- [ ] **Step 6: Review scope boundaries**

Verify that remaining `spaceId` hits are only in the documented phases 2–4 member/share/dead-code paths or explicit negative guards. Do not “clean up” those paths in this branch because doing so would mix independently testable migration slices.

- [ ] **Step 7: Commit final verification fixes**

If verification required no source changes, do not create an empty commit. Otherwise:

```bash
git add -u
git commit -m "test: verify admin mount migration"
```
