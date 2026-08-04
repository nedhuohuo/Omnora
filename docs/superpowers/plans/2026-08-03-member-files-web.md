# Member Files Web Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or inline execution task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a Chinese-first, real member file manager at `/` and `/app/`, backed by Omnora's session, space, mount, directory, upload, search, and download APIs.

**Architecture:** Keep HTTP route-group enforcement in Go, serve the embedded SPA only through enabled member/admin/share entry handlers, and make the root member entry fail closed. Replace the current mixed demo console with a member-only React file workspace that receives all file state from authenticated APIs. Put filesystem operations behind Go 1.26 `os.Root` so every file operation remains beneath the verified mount root.

**Tech Stack:** Go 1.26, `net/http`, `os.Root`, SQLite, React 19, TypeScript, Vite, CSS, existing Docker Compose deployment.

---

### Task 1: Route-group-specific SPA entries

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Write failing route-entry tests**

Add tests that configure only `member_web` and assert `GET /` and `GET /app/` return the embedded `index.html`, while `GET /admin/` returns `404 route_group_disabled`. Add a second test with no member route and assert `GET /` and `GET /app/` return JSON error code `route_group_disabled`, not HTML.

```go
routes := map[domain.RouteGroup]bool{domain.RouteGroupMemberWeb: true}
handler := New(config.Config{Routes: routes}, nil)

for _, target := range []string{"/", "/app/"} {
  rec := httptest.NewRecorder()
  handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
  if rec.Code != http.StatusOK { t.Fatalf("%s = %d, want 200", target, rec.Code) }
}
```

- [ ] **Step 2: Run the focused tests and confirm the root currently bypasses `member_web`**

Run: `go test ./internal/server -run 'Test(MemberWeb|StaticRoot)' -count=1`

Expected: the new disabled-root assertion fails because the current catch-all static handler returns `200`.

- [ ] **Step 3: Implement explicit SPA entry handlers**

Replace `handleGroup`'s `501` product handler with `serveGroupSPA`, wrap it in `gate`, and register exact plus slash paths for `/app`, `/app/`, `/admin`, `/admin/`, `/share`, and `/share/`. Register root through `gate(domain.RouteGroupMemberWeb, ...)`, retain a separate static-asset handler for emitted Vite assets, and retain `404` for unknown filenames with extensions.

```go
func (s *Server) serveGroupSPA(w http.ResponseWriter, r *http.Request) {
  s.serveStaticPath(w, r, "index.html")
}

func (s *Server) memberRoot() http.Handler {
  return s.gate(domain.RouteGroupMemberWeb, http.HandlerFunc(s.serveGroupSPA))
}
```

- [ ] **Step 4: Run the server test package**

Run: `go test ./internal/server -count=1`

Expected: PASS, with health and REST route tests unchanged.

- [ ] **Step 5: Commit the routing change**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(web): gate member SPA entry by route group"
```

### Task 2: Safe mount-root file primitives and create-directory endpoint

**Files:**
- Modify: `internal/files/service.go`
- Modify: `internal/files/service_test.go`
- Modify: `internal/transfer/service.go`
- Modify: `internal/transfer/service_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Add failing symlink and directory-creation tests**

Add service tests with a directory symlink inside a temporary mount pointing outside it. Assert listing `linked/subdir` fails, downloading through `linked/file.txt` fails, and upload target `linked/new.txt` fails. Add a create-directory test that succeeds in `docs/new` on a read-write mount and returns the expected normalized relative path; assert read-only mounts return `readonly_mount`.

```go
outside := t.TempDir()
if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil { t.Skip(err) }
if _, err := NewService().ListDirectory(mount, "linked"); err == nil {
  t.Fatal("symlinked directory must be rejected")
}
```

- [ ] **Step 2: Run only the new tests and confirm path joins do not reject intermediate links**

Run: `go test ./internal/files ./internal/transfer ./internal/server -run 'Test.*Symlink|TestCreateDirectory' -count=1`

Expected: the new intermediate-symlink tests fail against direct `filepath.Join` and `os.Open` usage.

- [ ] **Step 3: Implement a mount-root helper using `os.OpenRoot`**

Create an unexported `openMountRoot` helper in `internal/files/service.go` that normalizes the user path with `storage.CleanRelativePath`, calls `os.OpenRoot(mount.Root)`, and delegates `Stat`, `Open`, `Mkdir`, and `ReadDir` through that root. Return explicit sentinel errors for invalid mount, invalid mode, non-directory, and mount-root escape. Use it in `ListDirectory` and add `CreateDirectory(mount, parentPath, name)`.

```go
root, err := os.OpenRoot(mount.Root)
if err != nil { return err }
defer root.Close()
info, err := root.Stat(relativePath)
```

- [ ] **Step 4: Route all read/write file paths through the safe root**

Use the helper before `downloadFile`, `validateShareTarget`, and all upload session/complete operations. Refactor `transfer.Service` to keep an `*os.Root` for targets and its `.omnora/tmp/uploads` directory, so `CreateUploadSession` and `CompleteUpload` never use a joined host path. Register `POST /api/v1/spaces/{spaceId}/mounts/{mountId}/directories` and require editor permission, active read-write mount, and `verifyLoadedMountIdentity` before calling `files.CreateDirectory`.

```go
s.mux.Handle("POST /api/v1/spaces/{spaceId}/mounts/{mountId}/directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createDirectory)))
```

- [ ] **Step 5: Run file, transfer, and server tests**

Run: `go test ./internal/files ./internal/transfer ./internal/server -count=1`

Expected: PASS, including no-follow behavior for every member file operation.

- [ ] **Step 6: Commit the filesystem boundary change**

```bash
git add internal/files internal/transfer internal/server/api.go internal/server/server_test.go
git commit -m "feat(files): confine member operations to verified mount roots"
```

### Task 3: Typed member API client and locale state

**Files:**
- Modify: `web/src/api.ts`
- Create: `web/src/member/types.ts`
- Create: `web/src/member/i18n.ts`
- Create: `web/src/member/useLocale.ts`
- Test: `web/src/member/i18n.test.ts`

- [ ] **Step 1: Write locale and response-normalization tests**

Add Vitest only if the repository has no existing frontend test runner; otherwise use the existing runner. Assert `resolveLocale(null)` returns `zh-CN`, a stored `en-US` is retained, and format helpers convert `DirectoryChildrenPayload.entries` into typed `MemberEntry` values without fallback data.

```ts
expect(resolveLocale(null)).toBe('zh-CN');
expect(resolveLocale('en-US')).toBe('en-US');
```

- [ ] **Step 2: Run the focused frontend tests and confirm the members API is not typed yet**

Run: `npm test -- --run web/src/member/i18n.test.ts`

Expected: FAIL until the locale module and test command are added.

- [ ] **Step 3: Add concrete API methods and types**

Add `listSpaces`, `listMounts`, `createDirectory`, and `downloadURL` to `web/src/api.ts`. Define `MemberSpace`, `MemberMount`, `MemberEntry`, `TransferItem`, and `SearchResult` in `web/src/member/types.ts`; all list methods return `{items: T[]}` rather than `unknown[]`. Make `i18n.ts` hold both `zh-CN` and `en-US` labels, with Chinese as the default, and keep the selected locale in `localStorage` under `omnora.member.locale`.

```ts
export function listSpaces(signal?: AbortSignal) {
  return requestJson<{ items: MemberSpace[] }>('/api/v1/spaces', { signal });
}
```

- [ ] **Step 4: Run TypeScript validation and frontend tests**

Run: `npm run build && npm test -- --run web/src/member/i18n.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit the typed client and locale foundation**

```bash
git add web/src/api.ts web/src/member web/package.json web/package-lock.json
git commit -m "feat(member): add typed file API client and Chinese locale"
```

### Task 4: Replace the console preview with a real member file workspace

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css`
- Create: `web/src/member/MemberFilesApp.tsx`
- Create: `web/src/member/member-files.css`

- [ ] **Step 1: Write failing component tests for member states**

Render the member workspace with a mocked API client and assert: unauthenticated users see the Chinese login panel, authenticated users see only their spaces and mounts, opening a directory updates the breadcrumb, and a read-only mount disables upload and create-directory controls.

```tsx
render(<MemberFilesApp api={api} />)
expect(await screen.findByRole('heading', { name: '登录 Omnora' })).toBeVisible()
```

- [ ] **Step 2: Run the focused component tests**

Run: `npm test -- --run web/src/member/MemberFilesApp.test.tsx`

Expected: FAIL because the member component does not exist.

- [ ] **Step 3: Implement member-only application routing and layout**

Replace the `member/admin/share` in-page switcher in `App.tsx` with pathname resolution. Render `MemberFilesApp` for `/` and `/app/`; render route-specific minimal states for `/admin/` and `/share/` without surfacing admin controls in the member bundle. Implement the approved layout in `MemberFilesApp.tsx`: top search, Chinese/English toggle, left space/mount navigation, breadcrumb, list/grid selector, file table, selection actions, and empty/error/loading states. Remove all `mockBootstrap` fallback behavior from the member path.

```tsx
const memberPath = pathname === '/' || pathname === '/app' || pathname.startsWith('/app/');
return memberPath ? <MemberFilesApp /> : <RouteUnavailable />;
```

- [ ] **Step 4: Implement responsive and accessible styling**

Use CSS grid for desktop, collapse the sidebar below 900px, keep toolbar icon controls at fixed dimensions, and ensure the Chinese and English labels wrap rather than overlap. Preserve visible focus styles and use native buttons, inputs, menus, and checkboxes for interaction states.

- [ ] **Step 5: Run the frontend test suite and production build**

Run: `npm test -- --run && npm run build`

Expected: PASS and `web/dist` contains the member app bundle.

- [ ] **Step 6: Commit the member workspace**

```bash
git add web/src/App.tsx web/src/styles.css web/src/member
git commit -m "feat(member): deliver Chinese-first file workspace"
```

### Task 5: Real upload, download, search, and transfer center interactions

**Files:**
- Modify: `web/src/member/MemberFilesApp.tsx`
- Create: `web/src/member/uploadQueue.ts`
- Create: `web/src/member/uploadQueue.test.ts`
- Modify: `web/src/member/member-files.css`

- [ ] **Step 1: Write failing upload queue tests**

Use a fake `File` and mocked API client. Assert a new transfer calls `createUpload`, uploads each missing part in `partSize` slices, invokes `completeUpload`, serializes an active session to local storage, and on bootstrap resumes by calling `getUpload` before posting only missing parts.

```ts
expect(uploadPart).toHaveBeenCalledWith('up_1', 1, file.slice(0, 32768), expect.anything());
expect(completeUpload).toHaveBeenCalledWith('up_1', expect.anything());
```

- [ ] **Step 2: Run the focused queue tests**

Run: `npm test -- --run web/src/member/uploadQueue.test.ts`

Expected: FAIL because no resumable browser queue exists.

- [ ] **Step 3: Implement the transfer queue and file controls**

Create `uploadQueue.ts` with explicit `queued`, `uploading`, `paused`, `failed`, `completed`, and `cancelled` states. Use `AbortController` per transfer, persist only upload session metadata and file identity hints, and remove persistence on completion/cancel. Wire the toolbar file picker to upload, the per-row browser download URL to native download, the search box to `searchSpace`, and new-folder modal to `createDirectory`. After every completed filesystem operation, refresh the active directory.

- [ ] **Step 4: Run component, queue, and production build checks**

Run: `npm test -- --run && npm run build`

Expected: PASS; no UI action uses mock data or a JSON debug form.

- [ ] **Step 5: Commit the real interactions**

```bash
git add web/src/member
git commit -m "feat(member): wire resumable transfers and file actions"
```

### Task 6: End-to-end verification and Aliyun test deployment

**Files:**
- Modify: `docs/deployment/aliyun-test-server.md`
- Modify: `docs/verification/acceptance-criteria.md`

- [ ] **Step 1: Add deployment acceptance cases**

Document exact checks for disabled member root, member login, real directory listing, read-only restrictions, a multi-part upload that resumes after refresh, native download, Chinese default locale, and English persistence.

- [ ] **Step 2: Run local verification**

Run: `go test ./...`

Run: `npm --prefix web run build`

Expected: both commands PASS. If the local module cache is incomplete, run the same Go test command inside the project Docker build environment configured with the approved Go proxy rather than silently skipping it.

- [ ] **Step 3: Build and deploy the exact commit to the test ECS**

Run the existing `deploy/release.sh` workflow over the established SSH connection. Recreate Compose services using `docker-compose down` followed by `up -d` because the server uses Docker Compose 1.29.2 with Docker 29.

- [ ] **Step 4: Verify public and browser behavior**

Check `http://120.26.88.7:8080/` returns the member login/files page, not `NAS Console Preview`. Use browser automation at desktop and mobile widths to verify Chinese is the initial language, English persists after reload, toolbar labels do not overlap, directory navigation works, and the upload/download controls expose actual backend results.

- [ ] **Step 5: Commit verification documentation**

```bash
git add docs/deployment/aliyun-test-server.md docs/verification/acceptance-criteria.md
git commit -m "docs: verify member files web deployment"
```
