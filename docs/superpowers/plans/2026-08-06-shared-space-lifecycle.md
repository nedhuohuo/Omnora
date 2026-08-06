# Shared Space Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add administrator-only shared-space rename and permanent control-plane deletion while preserving all server-side files and excluding personal spaces.

**Architecture:** Add `PATCH` and `DELETE` collection-resource handlers beside the existing admin space handlers. Rename updates the active shared row; deletion uses one transaction that snapshots audit metadata, cancels related jobs, removes mount/control-plane rows, then deletes the shared space. React exposes actions only for selected shared spaces and requires exact-name confirmation for deletion.

**Tech Stack:** Go `net/http`, SQLite via `database/sql`, React + TypeScript, OpenAPI 3.1, shell verification scripts.

---

### Task 1: Lock backend behavior with failing tests

**Files:**
- Modify: `internal/server/admin_control_test.go`

- [ ] **Step 1: Add tests for rename and delete contracts**

Create a shared space with an active mount, catalog row, share, token boundary, upload session, and queued job. Assert that:

```go
PATCH /api/v1/admin/spaces/shared-id {"name":"Renamed"} -> 200
DELETE /api/v1/admin/spaces/shared-id {"name":"Renamed"} -> 200
```

The test must verify the renamed row, `deleted=true`, `dataDeleted=false`, removal of the space and child control-plane rows, cancellation of the job, and preservation of a real file in the mount root. Add separate assertions for personal-space `409 personal_space_protected`, mismatched delete confirmation `409 confirmation_required`, and a non-admin `403`.

- [ ] **Step 2: Run the focused test and verify the expected RED state**

Run:

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/server -run 'TestAdminSpaceRenameAndDelete|TestAdminSpaceProtection' -count=1
```

Expected: FAIL because the new routes and handlers do not exist yet.

### Task 2: Implement transactional Go handlers

**Files:**
- Modify: `internal/server/api.go:176-203`
- Modify: `internal/server/admin_control.go` near the existing space handlers

- [ ] **Step 1: Register the new routes**

Register `PATCH /api/v1/admin/spaces/{spaceId}` and `DELETE /api/v1/admin/spaces/{spaceId}` through `s.requireAdmin`-protected handlers.

- [ ] **Step 2: Add shared-space lookup and name validation**

Load only `status='active'`; return `404 not_found` for missing spaces. Reject `kind='personal'` with `409 personal_space_protected`. Trim the name, reject empty input and control characters with `400 invalid_input`, and cap it at the same display-name limit used by the existing admin naming APIs.

- [ ] **Step 3: Implement rename**

Run a parameterized `UPDATE spaces SET name=?, updated_at=? WHERE id=? AND kind='shared' AND status='active'`, return the updated `{id,type,name}`, and record `admin_space_rename` with old/new names.

- [ ] **Step 4: Implement deletion in one transaction**

Inside one SQLite transaction, load the shared-space name and mount IDs, insert a readable `admin_space_delete` audit event, cancel queued/running/paused jobs whose payload identifies those mounts, revoke shares, remove catalog/file rows, token boundaries, upload sessions, and upload parts, delete mount registrations, then delete the space. Never remove or modify `root_path` contents. Roll back on every SQL failure. Return `{id, deleted:true, deleteData:false, dataDeleted:false}` after commit.

- [ ] **Step 5: Run the focused tests and verify GREEN**

Run the same `go test ./internal/server -run ...` command. Expected: PASS with zero failures.

### Task 3: Add frontend API and shared-space actions

**Files:**
- Modify: `web/src/api.ts:150-165,997-1028`
- Modify: `web/src/member/AdminPanels.tsx:349-480`
- Modify: `web/src/member/i18n.ts` in both locale objects
- Modify: `web/src/member/member-files.css` only if the existing modal/action styles need a missing layout rule

- [ ] **Step 1: Add typed API functions**

Add `renameAdminSpace(spaceId, name)` using `PATCH`, and `deleteAdminSpace(spaceId, name)` using `DELETE` with `{name}`. Add a `SpaceDeletionPayload` type matching the response.

- [ ] **Step 2: Add rename and delete state/actions**

Track selected-space rename/delete targets and input values. Show action buttons only when the selected item has `type === 'shared'`; keep personal spaces without those controls. Rename pre-fills the name. Delete enables submit only when the typed value exactly equals the current name. On success close the modal and call `loadSpaces`; on failure keep the modal/input and show `describeError` inside the modal.

- [ ] **Step 3: Add Chinese and English copy**

Add labels for rename, permanent delete, exact-name confirmation, retained-server-files warning, cancel, save, and confirm-delete in both locale objects. Do not mention stop/restore.

- [ ] **Step 4: Run the web build**

Run:

```bash
npm run build
```

Expected: exit 0 with no TypeScript errors.

### Task 4: Synchronize API and product documentation

**Files:**
- Modify: `openapi/omnora.v1.yaml`
- Generated: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/api/README.md`
- Modify: `docs/requirements/product-requirements.md`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/design/web-application-design.md`
- Modify: `docs/security/security-model.md`
- Modify: `docs/verification/acceptance-criteria.md`

- [ ] **Step 1: Document the two resource operations and stable errors**

Add the `PATCH`/`DELETE` path, request/response schemas, `personal_space_protected`, and `confirmation_required` to the hand-maintained OpenAPI source.

- [ ] **Step 2: Update lifecycle wording**

Replace shared-space stop/restore wording with create/rename/permanent-delete wording, state that deletion removes control-plane registrations and preserves server files, and state that personal spaces cannot be edited or deleted. Preserve unrelated account/mount/index stop/restore semantics.

- [ ] **Step 3: Synchronize and verify the embedded OpenAPI copy**

Run:

```bash
scripts/verification/sync-openapi-asset.sh sync
scripts/verification/sync-openapi-asset.sh --check
```

Expected: the check reports the two OpenAPI files are identical.

### Task 5: Full verification and review

**Files:**
- No additional production files; inspect the complete diff and existing untracked files.

- [ ] **Step 1: Run backend package tests**

```bash
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache go test ./internal/server ./internal/identity ./internal/store
```

- [ ] **Step 2: Run API documentation verification**

```bash
scripts/verification/verify-api-docs.sh
```

- [ ] **Step 3: Review security properties**

Confirm parameterized SQL, admin authorization before mutation, personal-space protection, exact-name server-side confirmation, no filesystem deletion, transaction rollback, and audit metadata with no secrets.

- [ ] **Step 4: Inspect the final diff and request independent code review**

Use `git diff --check`, inspect all changed paths, and dispatch a reviewer against the pre-change and post-change SHAs. Fix all critical/important findings before reporting completion. Do not commit unless explicitly requested.
