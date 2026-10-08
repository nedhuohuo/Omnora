# Account MCP Space Grants and Folder Denylist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make account-owned MCP Tokens grant whole-space viewer/editor access, support editable folder denylists, and preserve legacy Token boundaries without privilege expansion.

**Architecture:** Add explicit Token-space grants and per-mount folder deny records while preserving `ai_token_boundaries` for legacy Tokens. Reload grants on every authorization, apply deny rules in `AccessGuard`, and filter collection results in `MemberFiles` so direct and enumerated access agree.

**Tech Stack:** Go, SQLite, React, TypeScript, Vitest, OpenAPI 3.1, standard MCP.

---

No commit step is included because this repository owner requires explicit authorization before committing.

### Task 1: Add a backward-compatible storage migration

**Files:**
- Create: `internal/store/migrations/013_ai_token_space_grants.sql`
- Create: `internal/store/p1_ai_token_space_grants_migration_test.go`

- [ ] Write a failing migration test asserting migration 13 exists, old Tokens default to `legacy_boundary`, the two new tables and indexes exist, and foreign keys reject a deny rule whose mount is outside its space.
- [ ] Run `GOCACHE=/private/tmp/omnora-mcp-space-grants-go-cache go test ./internal/store -run TestOpenSQLiteAppliesAITokenSpaceGrantsMigration -count=1`; expect failure because migration 13 is absent.
- [ ] Add the exact schema from the approved design, including `authorization_model`, `ai_token_space_grants`, `mounts_id_space_unique`, and `ai_token_folder_denies`.
- [ ] Rerun the focused migration test; expect pass.

### Task 2: Define grants, denylists, and update requests

**Files:**
- Modify: `internal/aitoken/types.go`
- Create: `internal/aitoken/grants.go`
- Create: `internal/aitoken/grants_test.go`

- [ ] Write table tests for `viewer/editor`, normalized deny paths, root rejection, duplicate/overlapping deny collapse, and mismatched space/mount rejection.
- [ ] Add types:

```go
type AuthorizationModel string
const (
    AuthorizationModelLegacyBoundary AuthorizationModel = "legacy_boundary"
    AuthorizationModelSpaceGrant AuthorizationModel = "space_grant"
)

type SpaceGrant struct {
    SpaceID string
    Permission domain.SpacePermission
    FolderDenylist []FolderDeny
}

type FolderDeny struct {
    MountID string
    RelativePath string
    Status string
}

type UpdateRequest struct {
    AccountID string
    TokenID string
    Name string
    Scopes []Scope
    SpaceGrants []SpaceGrant
    ExpiresAt time.Time
}
```

- [ ] Implement normalization using `storage.CleanRelativePath`; reject `.` and require at least one grant.
- [ ] Rerun focused tests; expect pass.

### Task 3: Create and atomically edit space-grant Tokens

**Files:**
- Modify: `internal/aitoken/service.go`
- Create: `internal/aitoken/update_test.go`

- [ ] Write failing tests proving new Tokens use `space_grant`, grants and denylists persist, an update replaces all grants atomically, Bearer secret remains valid, the next refresh sees edits, and audit failure rolls back all rows.
- [ ] Add `CreateSpaceGrantSecure` and `UpdateSecure`; validate active account, live `space_members`, requested permission rank, active mount ownership, directory existence, and identity fingerprint before opening the write transaction.
- [ ] Inside one transaction update Token metadata, delete/reinsert grants and denylists, remove legacy boundaries only during explicit legacy conversion, and write success audit.
- [ ] Rerun `go test ./internal/aitoken -run 'Test(CreateSpaceGrant|Update)' -count=1`; expect pass.

### Task 4: Load current grants into the MCP principal

**Files:**
- Modify: `internal/aitoken/service.go`
- Modify: `internal/aitoken/types.go`
- Modify: `internal/aitoken/service_test.go`

- [ ] Extend refresh tests to prove database grant changes are visible without reconnecting and revoked/expired/credential-generation-invalid Tokens still fail.
- [ ] Add `AuthorizationModel` and `SpaceGrants` to `Token` and `Principal`; retain `Boundaries` only for legacy mode.
- [ ] Load grants and denylists by Token ID in both `VerifyBearer` and `RefreshPrincipal`.
- [ ] Reject unknown authorization models rather than falling back to broad access.
- [ ] Rerun all `internal/aitoken` tests; expect pass.

### Task 5: Enforce whole-space permissions and direct deny rules

**Files:**
- Modify: `internal/access/guard.go`
- Modify: `internal/access/guard_test.go`
- Create: `internal/access/token_folder_deny_test.go`

- [ ] Write failing tests for viewer read/editor write, account-ACL intersection, future mount inclusion, exact deny, descendant deny, sibling allow, source/destination checks, `needs_review` mount failure, and legacy-boundary compatibility.
- [ ] In `Authorize`, keep browser sessions unchanged. For Token principals branch by `AuthorizationModel`: legacy uses `withinBoundaries`; space-grant requires a matching live grant and permission rank.
- [ ] Evaluate deny paths after canonical path cleaning and before filesystem access. Return `ErrFolderDenied` for exact/descendant matches and `ErrFolderDenylistReviewRequired` when the mount has a drifted rule.
- [ ] Ensure `AuthorizePair` independently checks both locators.
- [ ] Rerun `go test ./internal/access -count=1`; expect pass.

### Task 6: Filter list, search, shares, and transfer results

**Files:**
- Modify: `internal/memberfiles/service.go`
- Modify: `internal/memberfiles/read.go`
- Modify: `internal/memberfiles/read_test.go`
- Modify: `internal/membershare/service.go`
- Modify: `internal/transferticket/service.go`

- [ ] Write tests proving `ListSpaces` returns only explicit grants, `ListMounts` includes future active mounts, parent listings omit denied directories, search omits denied descendants, and share/transfer issuance and redemption recheck deny rules.
- [ ] Replace boundary-derived collection visibility with grant-derived visibility only for `space_grant` principals.
- [ ] Filter each directory child and search result through a shared non-I/O deny matcher; direct operations still use `AccessGuard`.
- [ ] Keep legacy collection behavior unchanged.
- [ ] Run `go test ./internal/memberfiles ./internal/membershare ./internal/transferticket -count=1`; expect pass.

### Task 7: Track blacklisted directory moves and identity drift

**Files:**
- Create: `internal/folderpolicy/service.go`
- Create: `internal/folderpolicy/service_test.go`
- Modify: `internal/fileops/coordinator.go`
- Modify: `internal/memberfiles/write.go`

- [ ] Write tests proving Omnora rename/move updates deny paths, subtree moves update nested rules, external replacement sets `needs_review`, and unrelated mutations do not change rules.
- [ ] Add a focused folder-policy service with `RewritePath`, `RevokeMissing`, and `VerifyIdentity` methods.
- [ ] Register it in the existing file-operation coordination boundary so path rewrite and control-plane metadata succeed or report an audit/readiness risk consistently.
- [ ] Verify deny identity before authorizing a Token mount; never infer “allowed” from missing metadata.
- [ ] Run focused folder-policy and member-file mutation tests; expect pass.

### Task 8: Add owner REST APIs and audit coverage

**Files:**
- Modify: `internal/server/api.go`
- Modify: `internal/server/route_matrix.go`
- Create: `internal/server/ai_token_update_test.go`

- [ ] Write HTTP tests for owner-only `PATCH`, recent reauthentication, CSRF, valid atomic update, privilege escalation rejection, legacy conversion, invalid deny directory, and readable error codes.
- [ ] Register `PATCH /api/v1/ai-tokens/{tokenId}` as Cookie auth + CSRF + recent reauthentication.
- [ ] Decode `spaceGrants`, call `aitoken.UpdateSecure`, and use the existing `recordAuditTx` path in `internal/server/api.go` to record safe metadata without secrets, absolute paths, or identity fingerprints.
- [ ] Map domain errors to the approved API error codes.
- [ ] Run focused server and route-matrix tests; expect pass.

### Task 9: Build the space-first Token editor

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/member/MemberTokensPanel.tsx`
- Create: `web/src/member/TokenSpaceGrantEditor.tsx`
- Create: `web/src/member/TokenSpaceGrantEditor.test.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`

- [ ] Write component tests for multiple spaces, default viewer, independent editor selection, directory picker, deny removal, `needs_review`, legacy conversion, save, and cancel.
- [ ] Add typed `AiTokenSpaceGrant`, `AiTokenFolderDeny`, and `updateAiToken` API models.
- [ ] Replace mount/path allowlist rows for new Tokens with space cards; use existing space/mount/directory APIs for the deny folder picker.
- [ ] Add an Edit action to active Tokens and wrap save with `runSensitive`.
- [ ] Keep the Bearer secret one-time display behavior unchanged.
- [ ] Run focused component/i18n tests and `npm run build`; expect pass.

### Task 10: Synchronize OpenAPI and MCP documentation

**Files:**
- Modify: `openapi/omnora.v1.yaml`
- Regenerate: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/mcp/README.md`
- Modify: `docs/api/README.md`
- Modify: `docs/design/domain-model.md`
- Modify: `web/src/member/McpDocsBlock.tsx`

- [ ] Add create/list/update schemas, authorization model, grants, deny statuses, and error responses to OpenAPI.
- [ ] Run `./scripts/verification/sync-openapi-asset.sh sync`, then `./scripts/verification/sync-openapi-asset.sh --check`; expect no diff after sync.
- [ ] Document that whole-space grants include future mounts but not future account spaces, and that denylists are subtree denies.
- [ ] Remove UI wording that presents MCP authorization as mandatory mount/path boundaries for new Tokens.

### Task 11: Full verification and runtime acceptance

**Files:** none

- [ ] Run `GOCACHE=/private/tmp/omnora-mcp-space-grants-go-cache go test ./...`; expect pass.
- [ ] Run `GOCACHE=/private/tmp/omnora-mcp-space-grants-go-cache go vet ./...`; expect pass.
- [ ] Run `cd web && npm test -- --run`; expect pass.
- [ ] Run `cd web && npm run build`; expect pass with no new warnings.
- [ ] Run `./scripts/verification/verify-api-docs.sh`; expect pass.
- [ ] Run `./scripts/verification/verify-mcp-protocol.sh`; expect protocol checks pass.
- [ ] Exercise one real Token: personal space editor, shared space viewer, one denied folder; prove list/search/direct read/write behavior and immediate post-edit reload.
- [ ] Run `git diff --check`; expect no whitespace errors.
