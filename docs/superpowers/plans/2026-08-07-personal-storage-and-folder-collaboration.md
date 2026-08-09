# Personal Storage and Folder Collaboration Implementation Plan

> **Status: superseded.** This plan assumes one Space/default mount per account and must not be executed. It is replaced by [the account, mount, and content authorization design](../specs/2026-08-08-account-mount-access-design.md); any new implementation plan must be written from that confirmed design.

> Historical record only. The remaining steps are non-executable examples of the superseded implementation.

**Goal:** Make external mounts exact-directory mounts, provision protected personal storage, and add member-to-member folder collaboration with viewer/editor access and a local mock preview.

**Architecture:** Add a dedicated personal-storage root and an idempotent per-account provisioner. Directory collaboration is a separate relational resource; `access.Guard` grants access only to the exact authorized relative-path subtree, while existing member-file handlers remain the filesystem implementation. The member “分享与协作” page presents public links and incoming directory collaborations as separate sections.

**Tech Stack:** Go, SQLite migrations, React, TypeScript, Vitest, Vite.

---

### Task 1: Direct external mounts and personal storage provisioning

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/slot_share_test.go`
- Create: `internal/server/personal_storage.go`
- Create: `internal/server/personal_storage_test.go`
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.nas.yml`

- [ ] **Step 1: Write failing direct-slot and personal-storage tests**

```go
func TestRegisterSlotUsesExactDirectory(t *testing.T) {
  rec := register("space-a", slot, "NAS")
  require.Equal(t, http.StatusCreated, rec.Code)
  require.NoDirExists(t, filepath.Join(slot, "space-a"))
  require.Equal(t, slot, storedMountRoot(t, db, "space-a"))
}

func TestEnsurePersonalStorageCreatesProtectedManagedMount(t *testing.T) {
  mount, err := server.ensurePersonalStorage(ctx, account, personalSpace)
  require.NoError(t, err)
  require.Equal(t, filepath.Join(personalRoot, account.ID), mount.RootPath)
}
```

- [ ] **Step 2: Run focused tests and verify the old slot expectation fails**

Run: `go test ./internal/server -run 'TestRegisterSlotUsesExactDirectory|TestEnsurePersonalStorageCreatesProtectedManagedMount' -count=1`

- [ ] **Step 3: Add `PersonalStorageDir` configuration and exact-directory registration**

```go
type StorageConfig struct {
  ManagedDir string
  PersonalStorageDir string
  PredeclaredMountRoot string
}

// validateMountRequest must verify rootPath itself and must not rewrite it or create a child directory.
```

- [ ] **Step 4: Implement idempotent personal-directory provisioning**

```go
func (s *Server) ensurePersonalStorage(ctx context.Context, account identity.Account, space identity.Space) (validatedMount, error) {
  root := filepath.Join(s.cfg.Storage.PersonalStorageDir, account.ID)
  // create only this account ID child; verify it as a managed mount; insert or return its protected mount row.
}
```

- [ ] **Step 5: Run focused tests**

Run: `go test ./internal/server -run 'TestRegisterSlotUsesExactDirectory|TestEnsurePersonalStorage' -count=1`

### Task 2: Persist directory collaboration grants

**Files:**
- Create: `internal/store/migrations/012_folder_collaborations.sql`
- Create: `internal/foldercollab/service.go`
- Create: `internal/foldercollab/service_test.go`
- Modify: `internal/server/api.go`
- Create: `internal/server/folder_collaborations.go`
- Create: `internal/server/folder_collaborations_test.go`

- [ ] **Step 1: Write failing service tests for create, overlap rejection, update and revoke**

```go
func TestCreateRejectsOverlappingRecipientGrants(t *testing.T) {
  _, err := svc.Create(ctx, CreateInput{RootRelativePath: "photos", RecipientID: recipient, Permission: domain.SpacePermissionViewer})
  require.NoError(t, err)
  _, err = svc.Create(ctx, CreateInput{RootRelativePath: "photos/2026", RecipientID: recipient, Permission: domain.SpacePermissionEditor})
  require.ErrorIs(t, err, ErrOverlappingGrant)
}
```

- [ ] **Step 2: Add migration and service**

```sql
CREATE TABLE folder_collaborations (
  id TEXT PRIMARY KEY,
  source_space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  source_mount_id TEXT NOT NULL REFERENCES mounts(id) ON DELETE CASCADE,
  root_relative_path TEXT NOT NULL,
  grantor_account_id TEXT NOT NULL REFERENCES accounts(id),
  recipient_account_id TEXT NOT NULL REFERENCES accounts(id),
  permission TEXT NOT NULL CHECK (permission IN ('viewer','editor')),
  revoked_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

- [ ] **Step 3: Register member CRUD routes and audit writes**

```go
s.mux.Handle("GET /api/v1/folder-collaborations", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listFolderCollaborations)))
s.mux.Handle("POST /api/v1/folder-collaborations", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createFolderCollaboration)))
s.mux.Handle("PATCH /api/v1/folder-collaborations/{collaborationId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.updateFolderCollaboration)))
s.mux.Handle("DELETE /api/v1/folder-collaborations/{collaborationId}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.revokeFolderCollaboration)))
```

- [ ] **Step 4: Run migration and handler tests**

Run: `go test ./internal/store ./internal/foldercollab ./internal/server -run 'Test.*FolderCollaboration' -count=1`

### Task 3: Enforce grants in all member file operations

**Files:**
- Modify: `internal/access/guard.go`
- Modify: `internal/access/guard_test.go`
- Modify: `internal/server/file_ops.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/member_shares.go`
- Modify: `internal/server/folder_collaborations.go`

- [ ] **Step 1: Write failing authorization tests**

```go
func TestDirectoryGrantAllowsOnlyItsSubtree(t *testing.T) {
  _, err := guard.Authorize(ctx, readRequest(recipient, "personal", "mount", "photos/2026"))
  require.NoError(t, err)
  _, err = guard.Authorize(ctx, readRequest(recipient, "personal", "mount", "documents"))
  require.ErrorIs(t, err, access.ErrForbidden)
}
```

- [ ] **Step 2: Extend `Guard.Authorize` with active directory-grant fallback**

```go
// A browser session without a space ACL may continue only when an active folder_collaborations row
// matches account, source space/mount and exact-or-descendant relative path. Token principals never use this fallback.
```

- [ ] **Step 3: Block root mutation and unsupported operations for collaborators**

```go
// write requests authorized by a folder grant reject root path mutation, trash, global search and cross-mount operations.
```

- [ ] **Step 4: Revoke affected grants after owner moves or deletes a collaboration root**

```go
// after a successful owner operation, revoke rows whose root equals or is below the moved/deleted path.
```

- [ ] **Step 5: Run focused security tests**

Run: `go test ./internal/access ./internal/server -run 'TestDirectoryGrant|TestFolderCollaboration' -count=1`

### Task 4: Build the sharing-and-collaboration member UI

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/member/MemberFilesApp.tsx`
- Modify: `web/src/member/MemberSharesPanel.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`
- Create: `web/src/member/folderCollaboration.test.tsx`

- [ ] **Step 1: Write failing UI tests**

```tsx
it('renders public links and incoming collaboration folders separately', async () => {
  render(<MemberSharesPanel locale="zh-CN" />)
  expect(await screen.findByText('我的分享')).toBeVisible()
  expect(await screen.findByText('与我共享')).toBeVisible()
  expect(screen.getByText('仅查看')).toBeVisible()
  expect(screen.getByText('可编辑')).toBeVisible()
})
```

- [ ] **Step 2: Add typed collaboration API client and data states**

```ts
export type FolderCollaborationPayload = {
  id: string; sourceSpaceId: string; sourceMountId: string; rootRelativePath: string;
  folderName: string; grantorDisplayName: string; permission: 'viewer' | 'editor'; status: 'active' | 'revoked';
};
export function listFolderCollaborations(direction: 'incoming' | 'outgoing', signal?: AbortSignal) { /* GET endpoint */ }
```

- [ ] **Step 3: Rename the navigation label and add the two UI sections**

```tsx
<h1>{text.sharesTitle}</h1>
<section aria-labelledby="public-shares"><h2 id="public-shares">{text.publicSharesTitle}</h2>{publicShareRows}</section>
<section aria-labelledby="incoming-collaborations"><h2 id="incoming-collaborations">{text.sharedWithMeTitle}</h2>{incomingCollaborationRows}</section>
```

- [ ] **Step 4: Wire “打开” to the existing browser with source IDs and granted relative path**

```tsx
onOpen({ spaceId: item.sourceSpaceId, mountId: item.sourceMountId, relativePath: item.rootRelativePath })
```

- [ ] **Step 5: Run frontend tests**

Run: `cd web && npm test -- --run src/member/folderCollaboration.test.tsx`

### Task 5: Add a development-only mock preview and update contracts/docs

**Files:**
- Create: `web/src/member/ShareCollaborationPreview.tsx`
- Modify: `web/src/App.tsx`
- Modify: `openapi/omnora.v1.yaml`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/deployment/mount-slots.md`
- Modify: `docs/requirements/product-requirements.md`

- [ ] **Step 1: Add a Vite-development-only preview route with two deterministic mock collaborations**

```tsx
if (import.meta.env.DEV && new URLSearchParams(window.location.search).get('demo') === 'sharing') {
  return <ShareCollaborationPreview />
}
```

- [ ] **Step 2: Synchronize OpenAPI and user-facing documentation**

```text
Document exact external mounts, /var/lib/omnora/personal/<account-id>, directory-level collaboration,
the viewer/editor boundary, and the “分享与协作” member entry.
```

- [ ] **Step 3: Run full verification and start preview**

Run: `go test ./... && cd web && npm test -- --run && npm run build`

Run: `cd web && npm run dev -- --host 127.0.0.1`

Expected: local preview is available at `http://127.0.0.1:5173/?demo=sharing`.
