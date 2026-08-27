# 个人空间与团队文件夹 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将成员端“我的文件”替换为可直接管理的个人空间，并提供独立的团队文件夹入口及账户内容源文件操作。

**Architecture:** 所有新增浏览器接口以 `source=personal|common_mount` 和可选 `mountId` 作为 locator，复用 `memberfiles.Service` 的实时授权、挂载身份校验、文件操作协调和审计；不接受 Space ID。前端 `MemberStorageWorkspace` 成为唯一的个人空间、团队文件夹和协作浏览器，个人空间默认直接打开根目录，团队文件夹只显示已授权共用挂载。

**Tech Stack:** Go、SQLite、`internal/memberfiles`、React、TypeScript、Vitest

---

### Task 1: 成员内容源写操作路由

**Files:**
- Modify: `internal/server/api.go`
- Modify: `internal/server/personal_files.go`
- Modify: `internal/server/route_matrix.go`
- Test: `internal/server/personal_files_test.go`

- [ ] **Step 1: 写失败的个人空间目录创建测试**

在 `internal/server/personal_files_test.go` 的现有 authenticated member fixture 后增加：

```go
req = httptest.NewRequest(http.MethodPost, "/api/v1/member/files/directories", strings.NewReader(`{"source":"personal","parentPath":".","name":"notes"}`))
req.Header.Set("Content-Type", "application/json")
req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: issued.Token})
rec = httptest.NewRecorder()
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusCreated {
    t.Fatalf("personal directory status=%d body=%s", rec.Code, rec.Body.String())
}
if _, err := os.Stat(filepath.Join(managed, "personal", memberID, "notes")); err != nil {
    t.Fatalf("personal directory missing: %v", err)
}
```

- [ ] **Step 2: 运行测试确认路由尚不存在**

```bash
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
```

Expected: FAIL，`POST /api/v1/member/files/directories` 返回 404。

- [ ] **Step 3: 提取严格 locator 解码 helper**

在 `internal/server/personal_files.go` 新增请求结构与 helper：

```go
type memberLocatorRequest struct {
    Source  string `json:"source"`
    MountID string `json:"mountId"`
    Path    string `json:"path"`
}

func memberLocatorFromRequest(req memberLocatorRequest) (access.Locator, error) {
    switch contentref.Source(strings.TrimSpace(req.Source)) {
    case contentref.SourcePersonal:
        if strings.TrimSpace(req.MountID) != "" { return access.Locator{}, personalstorage.ErrInvalidInput }
        return access.Locator{Source: contentref.SourcePersonal, Path: req.Path}, nil
    case contentref.SourceCommonMount:
        if strings.TrimSpace(req.MountID) == "" { return access.Locator{}, personalstorage.ErrInvalidInput }
        return access.Locator{Source: contentref.SourceCommonMount, MountID: req.MountID, Path: req.Path}, nil
    default:
        return access.Locator{}, personalstorage.ErrInvalidInput
    }
}
```

拒绝 `accountId`、`spaceId`、`defaultMountId` 和 collaboration locator；协作仅允许浏览。

- [ ] **Step 4: 实现创建目录 handler 并注册路由**

注册：

```go
s.mux.Handle("POST /api/v1/member/files/directories", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createMemberDirectory)))
```

handler 解码：

```go
var req struct {
    memberLocatorRequest
    ParentPath string `json:"parentPath"`
    Name string `json:"name"`
}
```

使用 `memberLocatorFromRequest` 得到 source/mount，并把 `Path` 固定为 `ParentPath`；调用：

```go
s.memberFiles.CreateDirectory(r.Context(), access.Subject{AccountID: session.AccountID}, locator, req.Name)
```

以 `recordAuditMutation` 记录 `directory_create`，返回 `{ "relativePath": result.RelativePath }` 与 201。

- [ ] **Step 5: 运行测试确认通过并提交**

```bash
gofmt -w internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
git add internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
git commit -m "feat: 支持成员内容源新建文件夹"
```

### Task 2: 成员内容源的重命名、复制、移动和删除

**Files:**
- Modify: `internal/server/api.go`
- Modify: `internal/server/personal_files.go`
- Test: `internal/server/personal_files_test.go`

- [ ] **Step 1: 写失败的个人空间重命名和删除测试**

创建 `notes.txt` 后调用：

```go
POST /api/v1/member/files/rename
{"source":"personal","path":"notes.txt","toName":"renamed.txt"}

DELETE /api/v1/member/files/object?source=personal&path=renamed.txt
```

断言重命名返回 200，原文件不存在，新文件存在；删除返回 200 且个人目录 `.omnora/trash` 有对应对象。

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
```

Expected: FAIL，成员内容源 mutation route 返回 404。

- [ ] **Step 3: 实现统一 mutation handlers**

注册：

```go
POST /api/v1/member/files/rename
POST /api/v1/member/files/copy
POST /api/v1/member/files/move
DELETE /api/v1/member/files/object
```

各 handler 均使用 `memberLocatorFromRequest`：

- rename：请求 `{source,mountId,path,toName,toPath}`；调用 `RenameSecure` 和 `resolveRenameTarget`；记录 `object_rename`。
- copy：请求 `{source,mountId,path,to:{source,mountId,path}}`；调用 `memberFiles.Copy`；记录 `object_copy`。
- move：同 copy 请求；调用 `MoveSecure`；记录 `object_move`。
- object delete：查询参数 `source,mountId,path,permanent`；`permanent=false` 调用 `TrashSecure`，否则 `DeletePermanentlySecure`；记录 `object_trash` 或 `object_delete`。

只接受 personal/common_mount；所有操作都将当前 cookie session 的 account ID 传入 `access.Subject`，不从请求读取账号或 Space ID。

- [ ] **Step 4: 运行 server 目标测试**

```bash
gofmt -w internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
git commit -m "feat: 支持成员内容源文件操作"
```

### Task 3: 成员内容源上传与下载

**Files:**
- Modify: `internal/server/api.go`
- Modify: `internal/server/personal_files.go`
- Modify: `web/src/api.ts`
- Test: `internal/server/personal_files_test.go`

- [ ] **Step 1: 写失败的个人空间上传创建测试**

向 `POST /api/v1/member/uploads` 发送：

```json
{"source":"personal","parentPath":".","fileName":"upload.txt","size":5}
```

断言 201、响应含 `id` 和 `partSize`，随后复用既有 `/api/v1/uploads/{id}/parts/1` 与 `/complete` 完成上传，并断言个人目录中出现内容为 `hello` 的文件。

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
```

Expected: FAIL，`/api/v1/member/uploads` 返回 404。

- [ ] **Step 3: 实现账户内容源上传创建与下载**

注册：

```go
POST /api/v1/member/uploads
GET /api/v1/member/files/download
```

上传创建请求解析 source/mountId/parentPath/fileName/size，通过 `pathJoinForUpload` 构造 locator 后调用 `PrepareUpload`。上传 part、状态、完成和取消复用既有 ID 型 `/api/v1/uploads` route，因为 upload session 已保存授权的 mount ID 与账户 ID。

下载 query 解析 source/mountId/path，使用 `s.guard.Authorize` 的 viewer 权限和 `files.NewService().OpenFile`，并沿用 range/inline 的 `ServeContent` 语义；不调用 legacy download route。

- [ ] **Step 4: 为前端新增 locator API helpers**

在 `web/src/api.ts` 定义：

```ts
export type MemberMutationLocator = Extract<MemberContentLocator, { source: 'personal' | 'common_mount' }>;
export function createMemberDirectory(locator: MemberMutationLocator, name: string): Promise<{ relativePath: string }>;
export function renameMemberObject(locator: MemberMutationLocator, toName: string): Promise<{ relativePath: string }>;
export function copyMemberObject(source: MemberMutationLocator, destination: MemberMutationLocator): Promise<{ relativePath: string }>;
export function moveMemberObject(source: MemberMutationLocator, destination: MemberMutationLocator): Promise<{ relativePath: string }>;
export function deleteMemberObject(locator: MemberMutationLocator, permanent?: boolean): Promise<void>;
export function createMemberUpload(locator: MemberMutationLocator, fileName: string, size: number): Promise<UploadSessionPayload>;
export function memberDownloadURL(locator: MemberMutationLocator): string;
```

所有 helper 只序列化 source、path、mountId；不得接受 spaceId。

- [ ] **Step 5: 运行目标测试并提交**

```bash
gofmt -w internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
go test ./internal/server -run '^TestMemberPersonalContentSourcesAndChildren$' -count=1
npm test --prefix web -- --run src/api.test.ts
git add internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go web/src/api.ts
git commit -m "feat: 支持成员内容源上传和下载"
```

### Task 4: 个人空间、团队文件夹和完整前端操作

**Files:**
- Modify: `web/src/member/MemberContentNavigation.tsx`
- Modify: `web/src/member/MemberContentSourceDirectory.tsx`
- Modify: `web/src/member/MemberStorageWorkspace.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`
- Test: `web/src/member/contentSourceNavigation.test.tsx`
- Create: `web/src/member/memberStorageWorkspace.test.tsx`

- [ ] **Step 1: 写失败的导航和文案测试**

将现有 navigation test 改为断言：

```ts
expect(html).toContain('个人空间');
expect(html).toContain('团队文件夹');
expect(html).toContain('协作');
expect(html).not.toContain('我的文件');
expect(html).not.toContain('可读写');
```

新增英文本地化断言 `Personal space`、`Team folders`。团队目录测试仅断言 common mount 卡片，无 personal card 和无 `common-1`。

- [ ] **Step 2: 运行测试确认失败**

```bash
npm test --prefix web -- --run src/member/contentSourceNavigation.test.tsx
```

Expected: FAIL，旧导航仍渲染“我的文件”。

- [ ] **Step 3: 更新导航、内容源卡片和 i18n**

- 将 `MemberContentTab` 改为 `personal | team-folders | collaborations`。
- `MemberContentNavigation` 渲染 `text.personalSpace`、`text.teamFolders`、`text.collaboration`。
- `MemberContentSourceDirectory` 只渲染 `commonMounts`，标题为团队文件夹、空态为暂无团队文件夹；只读卡片显示只读，可编辑卡片不显示可读写。
- 中英文成员端 `myFiles` 语义替换为个人空间，包括初始化文案、个人目录、AI Token boundary。

- [ ] **Step 4: 让个人空间默认直接打开并添加操作工具栏**

在 `MemberStorageWorkspace`：

- view 为 `personal` 时，内容源加载后立即以 `{source:'personal',path:'.'}` 调用 `loadDirectory`；
- view 为 `team-folders` 时只显示 `MemberContentSourceDirectory`；
- active source 为 personal/common mount 时显示上传、新建文件夹、刷新和返回；
- 使用新增 API helpers 加入新建文件夹、上传、下载、重命名、复制、移动、删除操作及必要确认 modal；
- collaboration 保持浏览器语义且不显示写操作；
- personal 与可编辑 common mount 不渲染 `text.readWrite`，只读 common mount 保留 `text.readOnly`。

使用现有 `uploadPart`、`completeUpload`、`FileTypeIcon` 和 `formatDirectoryChildren`；每次 mutation 完成后重新加载当前目录。

- [ ] **Step 5: 增加前端交互契约测试**

`memberStorageWorkspace.test.tsx` 使用静态渲染与 mocked API 模块，覆盖：

```ts
it('opens the personal-space root directly and exposes write actions');
it('shows only granted team folders and hides writable status text');
it('keeps write actions unavailable for a read-only team folder');
```

断言 personal source locator 为 `{source:'personal',path:'.'}`，个人空间操作文案出现，团队卡片不泄露 `mountId`，只读卡片不出现上传/新建按钮。

- [ ] **Step 6: 运行前端测试、构建并提交**

```bash
npm test --prefix web -- --run src/member/contentSourceNavigation.test.tsx src/member/memberStorageWorkspace.test.tsx src/member/i18n.test.ts
npm run build --prefix web
git add web/src/member/MemberContentNavigation.tsx web/src/member/MemberContentSourceDirectory.tsx web/src/member/MemberStorageWorkspace.tsx web/src/member/i18n.ts web/src/member/member-files.css web/src/member/contentSourceNavigation.test.tsx web/src/member/memberStorageWorkspace.test.tsx
git commit -m "feat: 增加个人空间和团队文件夹"
```

### Task 5: 全量验证与发布

**Files:**
- Verify only.

- [ ] **Step 1: 运行格式和完整发布门禁**

```bash
gofmt -w internal/server/api.go internal/server/personal_files.go internal/server/personal_files_test.go
git diff --check
./scripts/verification/release-gate.sh
```

Expected: Go 全量测试、前端测试、构建、静态发布检查均通过；动态 Docker 检查依现有环境规则跳过。

- [ ] **Step 2: 提交计划进度、推送并发布**

```bash
git push origin codex/aliyun-test-deploy
HEAD_SHA=$(git rev-parse HEAD)
RUN_ID=$(gh run list --repo nedhuohuo/Omnora --workflow publish-image.yml --branch codex/aliyun-test-deploy --event push --limit 10 --json databaseId,headSha --jq ".[] | select(.headSha == \"$HEAD_SHA\") | .databaseId" | head -n 1)
test -n "$RUN_ID"
gh run watch "$RUN_ID" --repo nedhuohuo/Omnora --exit-status
```

Expected: 发布工作流成功，`ghcr.io/nedhuohuo/omnora:latest` 更新为当前提交对应的多架构 manifest。
