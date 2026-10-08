# Account Team Spaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让任意有效账号创建带默认托管存储的团队空间，由空间 manager 管理成员权限，并在账号级“分享”页完成创建、管理和打开空间的闭环。

**Architecture:** 在成员 REST 面新增 `/api/v1/team-spaces` 控制面，复用现有 `spaces`、`space_members`、`mounts` 和审计表，但使用独立的 `team_spaces.go` 保持成员授权与系统管理员控制面隔离。前端由 `MemberFilesApp` 维护唯一账号空间列表，分享页负责团队空间管理；左侧 scope Tab 只选择个人/团队范围，具体空间在文件内容区以第一层文件夹进入。公开分享的现有 `space_grant + editor` 行为只补充契约、测试和文档，不改变浏览器 manager-only 规则。

**Tech Stack:** Go 1.x、`net/http`、SQLite、React 19、TypeScript、Vitest、Vite、OpenAPI 3.1。

**Approved spec:** `docs/superpowers/specs/2026-08-08-account-team-spaces-design.md`

**Working tree:** `/Users/huohuo/.config/superpowers/worktrees/Omnora/personal-storage-collaboration`

**Commit policy:** 本计划执行期间只修改和验证文件。除非用户再次明确授权，不运行 `git add` 或 `git commit`。

---

## File map

- Create `internal/server/team_spaces.go`: 成员端团队空间创建、托管目录、manager 校验和成员 ACL handlers。
- Create `internal/server/team_spaces_test.go`: 团队空间创建、存储、授权、最后 manager 与审计集成测试。
- Modify `internal/server/api.go`: 注册成员端团队空间路由。
- Modify `internal/server/route_matrix.go`: 为团队空间写请求启用 cookie 认证和 CSRF。
- Modify `internal/server/route_matrix_test.go`: 固定新路由认证矩阵。
- Modify `internal/membershare/service.go`: 修正公开分享授权注释，使浏览器与 `space_grant` 规则一致。
- Modify `internal/server/mcp_security_parity_test.go`: 用真实 MCP transport 固定 editor Token 成功及 viewer/黑名单/只读挂载拒绝契约。
- Modify `web/src/api.ts`: 团队空间和成员管理 DTO/客户端函数。
- Create `web/src/api.team-spaces.test.ts`: 验证请求方法、路径编码和请求体。
- Create `web/src/member/teamSpaces.ts`: 纯函数形式的 shared-space 过滤、manager 判断和默认成员权限。
- Create `web/src/member/teamSpaces.test.ts`: 团队空间纯状态规则测试。
- Create `web/src/member/MemberTeamSpacesSection.tsx`: 团队空间列表、创建弹窗和 manager 成员管理弹窗。
- Create `web/src/member/MemberTeamSpacesSection.test.tsx`: 角色可见性、默认 viewer 和“打开文件”回调测试。
- Create `web/src/member/MemberSpaceDirectory.tsx`: 文件页第一层的个人/团队空间文件夹列表。
- Create `web/src/member/MemberSpaceDirectory.test.tsx`: 固定空间文件夹筛选、排序和打开行为。
- Modify `web/src/member/MemberSharesPanel.tsx`: 用团队空间区域替换硬编码“与我共享”，并接收账号级空间回调。
- Modify `web/src/member/MemberFilesApp.tsx`: 刷新账号空间并打开指定团队空间的文件内容。
- Modify `web/src/member/spaceSelection.ts`: 增加“从账号功能打开空间”纯状态转换。
- Modify `web/src/member/spaceSelection.test.ts`: 固定打开团队空间时切回 `files` 并清理旧文件状态。
- Modify `web/src/member/spaceNavigation.ts`: 将空间分组改为个人/团队内容区目录筛选规则。
- Modify `web/src/member/spaceNavigation.test.ts`: 固定空间根目录筛选和排序。
- Modify `web/src/member/MemberWorkspaceNavigation.tsx`: 只渲染“个人空间/团队空间”两个 scope Tab，并嵌入账号主导航。
- Modify `web/src/member/MemberWorkspaceNavigation.test.tsx`: 固定 scope Tab，不再在侧栏逐个列出空间。
- Modify `web/src/member/i18n.ts`: 中英文团队空间、成员角色、错误与操作文案。
- Modify `web/src/member/member-files.css`: 团队空间列表和成员弹窗样式，复用现有设计变量。
- Modify `web/package.json`, `web/package-lock.json`: 增加最小 React DOM 交互测试依赖。
- Modify `web/src/member/ShareCollaborationPreview.tsx`: 左侧改为单一连续导航容器。
- Modify `web/src/member/ShareCollaborationPreview.css`: 移除两个导航组之间的额外间距。
- Modify `web/src/member/ShareCollaborationPreview.test.tsx`: 固定单一导航结构。
- Modify `openapi/omnora.v1.yaml`: 新增团队空间 REST 契约。
- Regenerate `internal/server/openapi_assets/omnora.v1.yaml`: 从唯一 OpenAPI 源同步嵌入副本。
- Modify `docs/api/README.md`, `docs/design/domain-model.md`, `docs/design/web-application-design.md`, `docs/design/personal-storage-and-folder-collaboration.md`, `docs/mcp/README.md`, `docs/security/security-model.md`: 同步账号级团队空间与公开分享授权语义。
- Create `docs/design/account-space-known-issues.md`: 登记两个明确延期的 P2，不修改对应代码。

---

### Task 1: 固定路由认证契约

**Files:**
- Modify: `internal/server/route_matrix_test.go`
- Modify: `internal/server/route_matrix.go`

- [ ] **Step 1: 先写失败的路由矩阵测试**

在 `TestRouteMatrixDeclaresCredentialBoundaries` 的表格中加入以下精确用例：

```go
{http.MethodPost, "/api/v1/team-spaces", RouteAuthCookie, true, false},
{http.MethodGet, "/api/v1/team-spaces/sp1/members", RouteAuthCookie, false, false},
{http.MethodPut, "/api/v1/team-spaces/sp1/members/u1", RouteAuthCookie, true, false},
{http.MethodDelete, "/api/v1/team-spaces/sp1/members/u1", RouteAuthCookie, true, false},
```

再加入通配符匹配断言：

```go
func TestTeamSpaceMemberRoutePatternMatches(t *testing.T) {
	if !routePatternMatches("/api/v1/team-spaces/*/members/*", "/api/v1/team-spaces/sp1/members/u1") {
		t.Fatal("expected team-space member route to match")
	}
	if routePatternMatches("/api/v1/team-spaces/*/members/*", "/api/v1/team-spaces/sp1/members") {
		t.Fatal("member collection must not match member mutation rule")
	}
}
```

新增 `TestTeamSpaceRoutesAreExplicitlyDeclared`，直接遍历 `routeMatrix`，断言上述四条 method/pattern 都有独立规则，避免它们只依赖兜底 `/api/v1/*`。

- [ ] **Step 2: 运行测试并确认 RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestRouteMatrixDeclaresCredentialBoundaries|TestTeamSpaceRoutesAreExplicitlyDeclared|TestTeamSpaceMemberRoutePatternMatches' -count=1
```

Expected: FAIL；成员列表 GET 会落入带 CSRF 的兜底规则，且四条显式规则尚不存在。

- [ ] **Step 3: 添加最小路由矩阵**

在 `routeMatrix` 的通用默认规则之前加入：

```go
{Method: "POST", Pattern: "/api/v1/team-spaces", Mode: RouteAuthCookie, CSRF: true},
{Method: "GET", Pattern: "/api/v1/team-spaces/*/members", Mode: RouteAuthCookie},
{Method: "PUT", Pattern: "/api/v1/team-spaces/*/members/*", Mode: RouteAuthCookie, CSRF: true},
{Method: "DELETE", Pattern: "/api/v1/team-spaces/*/members/*", Mode: RouteAuthCookie, CSRF: true},
```

- [ ] **Step 4: 运行路由测试并确认 GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestRouteMatrixDeclaresCredentialBoundaries|TestTeamSpaceRoutesAreExplicitlyDeclared|TestTeamSpaceMemberRoutePatternMatches' -count=1
```

Expected: PASS。

- [ ] **Step 5: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- internal/server/route_matrix.go internal/server/route_matrix_test.go
```

Expected: 仅包含团队空间显式认证矩阵；没有临时 handler 或 `501` 占位实现。

---

### Task 2: 普通账号创建带托管挂载的团队空间

**Files:**
- Create: `internal/server/team_spaces.go`
- Create: `internal/server/team_spaces_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/admin_control.go`

- [ ] **Step 1: 添加可配置托管根的测试服务器 helper**

在 `team_spaces_test.go` 创建局部 fixture，避免改变所有现有测试：

```go
func newTeamSpaceAPITestServer(t *testing.T) (*store.DB, http.Handler, string) {
	t.Helper()
	managedRoot := filepath.Join(t.TempDir(), "managed")
	if err := os.MkdirAll(managedRoot, 0o750); err != nil {
		t.Fatalf("create managed root: %v", err)
	}
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path: filepath.Join(t.TempDir(), "team-spaces.db"), BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SQL().Exec(`UPDATE recovery_control SET ready = 1 WHERE id = 1`); err != nil {
		t.Fatalf("mark recovery ready: %v", err)
	}
	handler := New(config.Config{
		Routes: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		Storage: config.StorageConfig{ManagedDir: managedRoot},
	}, db)
	return db, handler, managedRoot
}
```

- [ ] **Step 2: 写普通 member 创建空间的失败测试**

新增 `TestMemberCreatesTeamSpaceWithManagedMount`，执行以下断言：

```go
body := bytes.NewBufferString(`{"name":" 研发协作 "}`)
req := httptest.NewRequest(http.MethodPost, "/api/v1/team-spaces", body)
req.Header.Set("Content-Type", "application/json")
req.AddCookie(issueAPITestSession(t, db, member.ID))
rec := httptest.NewRecorder()
handler.ServeHTTP(rec, req)
if rec.Code != http.StatusCreated {
	t.Fatalf("create team space status = %d body=%s", rec.Code, rec.Body.String())
}
```

解码响应并查询数据库，必须同时满足：

```go
created.Space.Type == "shared"
created.Space.Name == "研发协作"
created.Space.Role == "manager"
created.Mount.Kind == "managed"
created.Mount.Mode == "read-write"
```

数据库中的 `owner_account_id` 和唯一 manager 都是 `member.ID`；挂载根等于 `filepath.Join(managedRoot, "team-spaces", created.Space.ID)`；目录真实存在；`mount_identity_json` 非空；审计 action 为 `team_space_create`。

同一测试文件增加 `TestTeamSpaceCreateRejectsInvalidOrUnauthorizedRequests`、`TestTeamSpaceCreateRejectsUnsafeOrOverlappingManagedStorage` 和 `TestTeamSpaceCreateRollsBackEmptyDirectoryOnDatabaseFailure`：无 cookie 返回 `401`，停用账号返回 `401`，控制字符名称返回 `400 invalid_input`，不安全托管根 `/` 返回 `503 managed_storage_unavailable`，与已有挂载重叠也返回不泄露路径的 `503 managed_storage_unavailable`。使用数据库约束故意让 mount insert 失败，断言空间事务已回滚且本次新建的空目录已被非递归删除。

回滚测试用 fixture 内的临时 trigger 精确制造失败，测试结束由临时数据库整体清理：

```sql
CREATE TRIGGER fail_team_mount
BEFORE INSERT ON mounts
WHEN NEW.kind = 'managed'
BEGIN
  SELECT RAISE(ABORT, 'forced team mount failure');
END;
```

- [ ] **Step 3: 运行创建测试并确认 RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestMemberCreatesTeamSpaceWithManagedMount|TestTeamSpaceCreate' -count=1
```

Expected: FAIL，真实创建路由和 handler 尚不存在。

- [ ] **Step 4: 实现安全托管目录和创建事务**

先把 `normalizeAdminSpaceName` 重命名为 `normalizeSpaceName`，管理员创建/重命名与成员创建共同复用同一规则。然后在 `team_spaces.go` 定义明确响应类型：

```go
const teamMountName = "Team files"

type teamSpaceDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type teamSpaceCreateDTO struct {
	Space teamSpaceDTO `json:"space"`
	Mount mountDTO     `json:"mount"`
}
```

实现路径解析 helper：

```go
func teamSpaceRoot(managedRoot, spaceID string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(managedRoot))
	if root == "" || root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
		return "", errManagedStorageUnavailable
	}
	if strings.TrimSpace(spaceID) == "" || filepath.Base(spaceID) != spaceID {
		return "", errManagedStorageUnavailable
	}
	path := filepath.Join(root, "team-spaces", spaceID)
	prefix := root + string(filepath.Separator)
	if path == root || !strings.HasPrefix(path, prefix) {
		return "", errManagedStorageUnavailable
	}
	return path, nil
}
```

本任务只在 `Server.apiRoutes()` 注册已实现的创建 handler；三个成员 handler 由 Task 3 实现完成后再注册。不得加入临时成功响应或 `501` 占位：

```go
s.mux.Handle("POST /api/v1/team-spaces", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.createTeamSpace)))
```

`createTeamSpace` 必须按以下顺序执行：

1. `requireSession` 获取账号。
2. 使用共享的 `normalizeSpaceName` 统一名称规则。
3. 生成 `spaceID := "spc_" + httpx.NewRequestID()` 和 `mountID := "mnt_" + httpx.NewRequestID()`。
4. 在开启事务前调用 `s.loadMountIdentities(r)`，避免 SQLite 单连接在事务内重入；随后 `os.MkdirAll(filepath.Dir(rootPath), 0o750)` 并用 `os.Mkdir(rootPath, 0o750)` 创建唯一目录。
5. 用 `mountid.VerifyCandidateRoot(rootPath, existingIdentities)` 同时采集身份并拒绝路径、设备/inode 和 bind source 重叠；读写挂载还必须通过 `probeMountWritable`，再 JSON 编码身份。
6. 单事务插入 `spaces(kind='shared')`、创建者 `manager`、默认 managed/read_write mount 和 `team_space_create` 审计事件；事务内所有查询和写入只使用 `*sql.Tx`。
7. 从目录创建完成到事务提交之间的任意失败，只调用 `os.Remove(rootPath)`；该调用不会递归删除，目录非空时保留孤儿目录并记录错误，不得扩大删除范围。
8. 返回 `201` 的 `{space, mount}`；`mount.mode` 使用前端值 `read-write`，响应不得包含 `root_path` 或身份指纹。

核心 SQL 固定为参数化查询：

```sql
INSERT INTO spaces(id, kind, name, owner_account_id, status, created_at, updated_at)
VALUES (?, 'shared', ?, ?, 'active', ?, ?)
```

```sql
INSERT INTO space_members(space_id, account_id, permission, created_at, updated_at)
VALUES (?, ?, 'manager', ?, ?)
```

```sql
INSERT INTO mounts(id, space_id, display_name, root_path, kind, mode, index_enabled, status, mount_identity_json, created_at, updated_at)
VALUES (?, ?, ?, ?, 'managed', 'read_write', 0, 'active', ?, ?, ?)
```

- [ ] **Step 5: 运行创建测试并确认 GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestMemberCreatesTeamSpaceWithManagedMount|TestTeamSpaceCreate' -count=1
```

Expected: PASS，且测试临时目录外没有文件写入。

- [ ] **Step 6: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- internal/server/team_spaces.go internal/server/team_spaces_test.go internal/server/api.go internal/server/admin_control.go internal/server/route_matrix.go internal/server/route_matrix_test.go
```

Expected: 不包含管理员挂载入口改动，不接受客户端宿主机路径。

---

### Task 3: 空间 manager 管理成员并保护最后一个 manager

**Files:**
- Modify: `internal/server/team_spaces.go`
- Modify: `internal/server/team_spaces_test.go`
- Modify: `internal/server/api.go`

- [ ] **Step 1: 写 manager 成员管理失败测试**

新增以下独立测试：

```go
func TestTeamSpaceManagerListsAndMutatesMembers(t *testing.T)
func TestTeamSpaceViewerAndOtherManagerCannotManageMembers(t *testing.T)
func TestTeamSpaceRejectsRemovingOrDemotingLastManager(t *testing.T)
func TestTeamSpaceMemberRoutesRejectPersonalSpace(t *testing.T)
```

测试数据包含三个账号：创建者 manager、同空间 editor、另一空间 manager。验证：

- manager 的 GET 返回账号 ID、邮箱、显示名和 permission。
- `PUT .../members/<uppercase-email>` 可以解析 active 账号，`{"permission":"viewer"}` 成功。
- 再次 PUT 把 viewer 改为 editor。
- DELETE 移除 editor 成功并写 `team_space_member_remove` 审计。
- editor、viewer 和另一空间 manager 对目标空间均返回 `403 forbidden`。
- 最后一个 manager 自降级或自删除返回 `409 last_manager_required`，数据库仍保留 manager。
- 个人空间 GET/PUT/DELETE 返回 `409 personal_space_protected`。
- 未知账号和未知空间返回 `404 not_found`；停用账号不能被加入。

- [ ] **Step 2: 运行成员测试并确认 RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestTeamSpaceManager|TestTeamSpaceViewer|TestTeamSpaceRejects|TestTeamSpaceMemberRoutes' -count=1
```

Expected: FAIL，因为成员 handlers 尚未实现 manager 授权和最后 manager 保护。

- [ ] **Step 3: 实现统一 manager 授权 helper**

只读列表使用 `requireTeamSpaceManager`；写操作在事务内使用 `requireTeamSpaceManagerTx`。两者执行相同的一条参数化查询，同时确认空间类型、状态、账号状态和角色：

```sql
SELECT sp.kind, sm.permission
FROM spaces sp
JOIN space_members sm ON sm.space_id = sp.id AND sm.account_id = ?
JOIN accounts a ON a.id = sm.account_id AND a.status = 'active'
WHERE sp.id = ? AND sp.status = 'active'
```

映射规则固定为：空间不存在 `404`；`kind != shared` 返回 `409 personal_space_protected`；permission 不是 manager 返回 `403 forbidden`。

- [ ] **Step 4: 实现列表、写入和删除事务**

`listTeamSpaceMembers` 查询：

```sql
SELECT sm.account_id, a.email, a.display_name, sm.permission
FROM space_members sm
JOIN accounts a ON a.id = sm.account_id AND a.status = 'active'
WHERE sm.space_id = ?
ORDER BY lower(a.display_name), lower(a.email), sm.account_id
```

`putTeamSpaceMember` 必须：先开启事务；通过 `requireTeamSpaceManagerTx` 校验当前账号；按 ID 或 `lower(email)` 解析 active 账号；校验 permission 白名单；若目标当前是唯一 manager 且新权限不是 manager，返回 `last_manager_required`；随后 UPSERT membership 并在同一事务写 `team_space_member_set` 审计。不要复用 `resolveAdminSpaceMemberAccount`，因为它允许停用账号且包含全局管理员语义。

最后 manager 计数必须在同一事务中执行：

```sql
SELECT COUNT(1) FROM space_members WHERE space_id = ? AND permission = 'manager'
```

`deleteTeamSpaceMember` 在同一事务完成 manager 授权、读取目标权限和 manager 计数；目标是唯一 manager 时拒绝；否则 DELETE 并写审计。不要在事务打开时调用 `s.guard`、`s.sqlDB()` 或任何会重新占用 SQLite 单连接的 helper。所有 SQL 使用占位参数，错误响应不泄露数据库或绝对路径。

三个 handler 完成后再在 `Server.apiRoutes()` 注册。PUT/DELETE 复用同一个 `{accountRef}` path；DELETE 调用方传成员列表返回的规范账号 ID：

```go
s.mux.Handle("GET /api/v1/team-spaces/{spaceId}/members", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.listTeamSpaceMembers)))
s.mux.Handle("PUT /api/v1/team-spaces/{spaceId}/members/{accountRef}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.putTeamSpaceMember)))
s.mux.Handle("DELETE /api/v1/team-spaces/{spaceId}/members/{accountRef}", s.gate(domain.RouteGroupREST, http.HandlerFunc(s.deleteTeamSpaceMember)))
```

- [ ] **Step 5: 运行成员测试并确认 GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run 'TestTeamSpaceManager|TestTeamSpaceViewer|TestTeamSpaceRejects|TestTeamSpaceMemberRoutes' -count=1
```

Expected: PASS。

- [ ] **Step 6: 运行服务端回归测试**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -count=1
```

Expected: PASS；现有 `/api/v1/admin/spaces/...` 行为不变。

- [ ] **Step 7: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- internal/server/team_spaces.go internal/server/team_spaces_test.go internal/server/api.go
```

Expected: 成员控制面只接受当前 space manager，不因账号全局角色为 admin 自动绕过该检查。

---

### Task 4: 固定 AI Token editor 通过真实 MCP 创建公开分享的契约

**Files:**
- Modify: `internal/membershare/service.go`
- Modify: `internal/server/mcp_security_parity_test.go`

- [ ] **Step 1: 先确认已有 service 级授权基线**

已有 `TestSpaceGrantSharesRecheckGrantDenyAndCreatorACL` 已覆盖 editor 成功、viewer 拒绝、黑名单拒绝和账号 ACL 撤销后拒绝。先运行它，避免重复写同层测试：

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/membershare -run TestSpaceGrantSharesRecheckGrantDenyAndCreatorACL -count=1
```

Expected: PASS；这只是 service 层基线，不算 MCP 入口验收。

- [ ] **Step 2: 写真实 MCP 入口失败测试**

在 `mcp_security_parity_test.go` 新增 `TestMCPSpaceGrantShareCreateRequiresEditorWritableAllowedTarget`：

- 使用 `newMCPProtocolFixture` 的真实 `/mcp` Streamable HTTP transport 和 SDK client。
- 用 `aitoken.NewService(...).CreateSpaceGrant` 分别签发仅含 `shares:create` scope 的 editor 和 viewer Token，并把 `fixture.bearer` 切换为当前场景的一次性 bearer；不得输出 bearer 或分享 fragment。
- editor + read_write + allowed path 经 MRTR 确认后成功创建，`shares` 表记录增加一条。
- viewer 返回 MCP typed forbidden error，记录不增加。
- editor 的 denylist 指向目标目录且指纹正确时返回 `folder_denied`，记录不增加。
- 将挂载改为 `read_only` 后 editor 返回 `readonly_mount`，记录不增加。

- [ ] **Step 3: 运行 MCP 测试并确认 RED/GREEN 边界**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server -run TestMCPSpaceGrantShareCreateRequiresEditorWritableAllowedTarget -count=1
```

Expected: 当前生产授权已具备该能力，补齐 space-grant bearer fixture 后应 PASS；若失败，先定位 MCP 鉴权/工具入口差距，不在未复现前修改授权策略。

- [ ] **Step 4: 修正代码注释，不改变授权逻辑**

把 `Create` 上方的 manager-only 注释改为：

```go
// Create validates a browser-manager target or a space-grant editor target,
// stores only a hash of the fragment secret, and returns the capability once.
```

保留现有逻辑：浏览器 subject 使用 `SpacePermissionManager`；`AuthorizationModelSpaceGrant` 使用 `SpacePermissionEditor` 且 `Write: true`。

- [ ] **Step 5: 运行分享与访问控制测试**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/membershare ./internal/access ./internal/memberfiles ./internal/server -count=1
```

Expected: PASS。

- [ ] **Step 6: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- internal/membershare/service.go internal/server/mcp_security_parity_test.go
```

Expected: 只有真实 MCP 验收测试和注释变化，没有放宽 viewer、黑名单、挂载模式或 MRTR。

---

### Task 5: 新增前端团队空间 API 契约

**Files:**
- Modify: `web/src/api.ts`
- Create: `web/src/api.team-spaces.test.ts`
- Create: `web/src/member/teamSpaces.ts`
- Create: `web/src/member/teamSpaces.test.ts`

- [ ] **Step 1: 写 API 客户端失败测试**

使用 `vi.stubGlobal('fetch', ...)` 返回 JSON `Response`，依次调用计划中的客户端函数并断言 URL、method 和 body：

```ts
await createTeamSpace({ name: '研发协作' });
await listTeamSpaceMembers('sp/1');
await putTeamSpaceMember('sp/1', 'USER+ONE@example.test', 'viewer');
await removeTeamSpaceMember('sp/1', 'acct/1');

expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/team-spaces', expect.objectContaining({
  method: 'POST',
  body: JSON.stringify({ name: '研发协作' }),
}));
expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/team-spaces/sp%2F1/members', expect.any(Object));
expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/team-spaces/sp%2F1/members/USER%2BONE%40example.test', expect.objectContaining({ method: 'PUT' }));
expect(fetch).toHaveBeenNthCalledWith(4, '/api/v1/team-spaces/sp%2F1/members/acct%2F1', expect.objectContaining({ method: 'DELETE' }));
```

- [ ] **Step 2: 写团队空间纯规则失败测试**

在 `teamSpaces.test.ts` 固定：

```ts
expect(teamSpacesForAccount(spaces).map((space) => space.id)).toEqual(['shared-a', 'shared-b']);
expect(canManageTeamMembers({ id: 'shared-a', type: 'shared', name: 'A', role: 'manager' })).toBe(true);
expect(canManageTeamMembers({ id: 'shared-b', type: 'shared', name: 'B', role: 'editor' })).toBe(false);
expect(defaultTeamMemberPermission).toBe('viewer');
```

- [ ] **Step 3: 运行前端测试并确认 RED**

Run:

```bash
cd web
npx vitest run src/api.team-spaces.test.ts src/member/teamSpaces.test.ts
```

Expected: FAIL，因为函数和类型尚不存在。

- [ ] **Step 4: 实现 DTO、API 函数和纯规则**

在 `api.ts` 定义：

```ts
export type TeamSpaceCreatePayload = { name: string };
export type TeamSpaceCreateResponse = { space: MemberSpace; mount: MemberMount };
export type TeamSpaceMemberRole = 'viewer' | 'editor' | 'manager';
export type TeamSpaceMemberPayload = {
  accountId: string;
  email?: string;
  displayName?: string;
  permission: TeamSpaceMemberRole;
};
```

新增函数：

```ts
export function createTeamSpace(payload: TeamSpaceCreatePayload, signal?: AbortSignal) {
  return requestJson<TeamSpaceCreateResponse>('/api/v1/team-spaces', {
    method: 'POST', body: JSON.stringify(payload), signal,
  });
}

export function listTeamSpaceMembers(spaceId: string, signal?: AbortSignal) {
  return requestJson<{ items: TeamSpaceMemberPayload[] }>(
    `/api/v1/team-spaces/${encodeURIComponent(spaceId)}/members`, { signal },
  );
}

export function putTeamSpaceMember(spaceId: string, accountRef: string, permission: TeamSpaceMemberRole, signal?: AbortSignal) {
  return requestJson<TeamSpaceMemberPayload>(
    `/api/v1/team-spaces/${encodeURIComponent(spaceId)}/members/${encodeURIComponent(accountRef)}`,
    { method: 'PUT', body: JSON.stringify({ permission }), signal },
  );
}

export function removeTeamSpaceMember(spaceId: string, accountId: string, signal?: AbortSignal) {
  return requestJson<void>(
    `/api/v1/team-spaces/${encodeURIComponent(spaceId)}/members/${encodeURIComponent(accountId)}`,
    { method: 'DELETE', signal },
  );
}
```

在 `teamSpaces.ts` 实现：

```ts
export const defaultTeamMemberPermission = 'viewer' as const;
export const teamSpacesForAccount = (spaces: MemberSpace[]) => spaces.filter((space) => space.type === 'shared');
export const canManageTeamMembers = (space: MemberSpace) => space.type === 'shared' && space.role === 'manager';
```

- [ ] **Step 5: 运行测试并确认 GREEN**

Run:

```bash
cd web
npx vitest run src/api.team-spaces.test.ts src/member/teamSpaces.test.ts
```

Expected: PASS。

- [ ] **Step 6: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- web/src/api.ts web/src/api.team-spaces.test.ts web/src/member/teamSpaces.ts web/src/member/teamSpaces.test.ts
```

Expected: 新客户端只调用成员端路由，不复用 `/api/v1/admin/spaces`。

---

### Task 6: 构建账号级团队空间区域

**Files:**
- Create: `web/src/member/MemberTeamSpacesSection.tsx`
- Create: `web/src/member/MemberTeamSpacesSection.test.tsx`
- Modify: `web/src/member/MemberSharesPanel.tsx`
- Modify: `web/src/member/i18n.ts`
- Modify: `web/src/member/member-files.css`
- Modify: `web/package.json`
- Modify: `web/package-lock.json`

- [ ] **Step 1: 安装最小交互测试依赖**

Run:

```bash
cd web
npm install --save-dev @testing-library/react @testing-library/user-event jsdom
```

Expected: 只修改 `package.json` 和 `package-lock.json`；不引入生产依赖。

- [ ] **Step 2: 写组件交互失败测试**

在测试文件首行声明 `// @vitest-environment jsdom`，使用 Testing Library 输入一个 manager 和一个 editor 空间。断言：

```ts
expect(screen.getByRole('heading', { name: '团队空间' })).toBeTruthy();
expect(screen.getByRole('button', { name: '创建团队空间' })).toBeTruthy();
expect(screen.getByText('研发协作')).toBeTruthy();
expect(screen.getAllByRole('button', { name: '管理成员' })).toHaveLength(1);
expect(screen.queryByText('与我共享')).toBeNull();
```

点击 editor 空间的“打开文件”并断言 `onOpenSpace('shared-editor')`。点击“创建团队空间”、输入名称并提交，mock `createTeamSpace` 成功后断言 `onAccountSpacesChanged` 被调用一次。

点击 manager 的“管理成员”，断言此时才调用 `listTeamSpaceMembers`；新增成员表单默认 permission 为 `viewer`。mock `putTeamSpaceMember` 返回 `409 last_manager_required`，断言弹窗保留并显示对应文案。最后 manager 约束仍以服务端为准，前端人数只用于提示。

- [ ] **Step 3: 运行组件测试并确认 RED**

Run:

```bash
cd web
npx vitest run src/member/MemberTeamSpacesSection.test.tsx
```

Expected: FAIL，因为组件尚不存在。

- [ ] **Step 4: 实现团队空间组件**

组件 props 固定为：

```ts
type MemberTeamSpacesSectionProps = {
  locale: MemberLocale;
  spaces: MemberSpace[];
  onOpenSpace: (spaceId: string) => void;
  onAccountSpacesChanged: () => Promise<void>;
  service?: TeamSpaceService;
};
```

在 `teamSpaces.ts` 定义 `TeamSpaceService`（`create`、`listMembers`、`putMember`、`removeMember` 四个方法）和调用 `api.ts` 的 `defaultTeamSpaceService`；测试和本地预览注入内存实现，禁止 preview 发真实写请求。

组件行为：

- 只显示 `teamSpacesForAccount(spaces)`。
- 创建弹窗只提交 `{name: trimmedName}`；成功后关闭弹窗并等待唯一的 `onAccountSpacesChanged()`，账号空间列表只由 `MemberFilesApp` 维护。
- 空列表仍显示创建按钮和明确空态。
- 每行显示空间名、当前角色和“打开文件”。
- 只有 `canManageTeamMembers(space)` 为 true 时显示“管理成员”。
- 打开管理弹窗后才调用 `listTeamSpaceMembers(space.id)`。
- 添加成员默认 `viewer`，支持 viewer/editor/manager；保存后重新加载成员列表并调用 `onAccountSpacesChanged()`，以刷新当前账号可能变化的角色。
- 删除前显示成员可读名称；服务端 `last_manager_required` 返回时不关闭弹窗。
- 如果刷新后的当前空间角色不再是 manager，立即关闭成员管理弹窗，避免保留陈旧管理能力。
- 错误使用现有 `ApiError`/`describeError` 风格，不显示响应堆栈或绝对路径。

在 `i18n.ts` 增加完整中英文键：`teamSpacesTitle`、`teamSpacesDetail`、`teamSpaceCreate`、`teamSpaceName`、`teamSpaceEmpty`、`teamSpaceOpenFiles`、`teamSpaceManageMembers`、`teamSpaceMembersTitle`、`teamSpaceMemberAccount`、`teamSpaceMemberPermission`、`teamSpaceMemberAdd`、`teamSpaceMemberRemove`、`teamSpaceLastManager`。`navShares` 恢复为“分享 / Shares”，删除不再使用的 `sharedWithMeTitle/sharedWithMeDetail/sharedWithMeEmpty`。

CSS 使用现有 `--surface`、`--border-*`、`--space-*`、`--control-*`、`--accent-*` 变量；不引入新字体、渐变或新的全局颜色。

- [ ] **Step 5: 用团队空间替换硬编码空态并统一空间数据源**

把 `MemberSharesPanel` 签名改为：

```ts
export default function MemberSharesPanel({
  locale,
  spaces,
  onOpenSpace,
  onAccountSpacesChanged,
}: {
  locale: MemberLocale;
  spaces: MemberSpace[];
  onOpenSpace: (spaceId: string) => void;
  onAccountSpacesChanged: () => Promise<void>;
})
```

删除 `MemberSharesPanel` 内部的 `spaces` state 和 `listSpaces()` 请求，把现有 `load()` 拆为只加载公开分享的 `loadShares()`。`managerSpaces` 和创建分享挂载选择均使用 App 传入的账号空间。删除原 `shared-with-me-title` section，替换为：

```tsx
<MemberTeamSpacesSection
  locale={locale}
  spaces={spaces}
  onOpenSpace={onOpenSpace}
  onAccountSpacesChanged={onAccountSpacesChanged}
/>
```

公开分享列表、创建、链接展示和撤销逻辑保持不变。
当 `spaces` props 变化后，如果 `formSpaceId` 已不存在或不再是 manager，必须切换到首个可管理空间或清空，避免浏览器分享表单保留陈旧空间权限。

- [ ] **Step 6: 运行组件与 i18n 测试并确认 GREEN**

Run:

```bash
cd web
npx vitest run src/member/MemberTeamSpacesSection.test.tsx src/member/i18n.test.ts
```

Expected: PASS；组件 DOM 不再出现“与我共享”或旧个人目录协作文案。

- [ ] **Step 7: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- web/src/member/MemberTeamSpacesSection.tsx web/src/member/MemberTeamSpacesSection.test.tsx web/src/member/MemberSharesPanel.tsx web/src/member/i18n.ts web/src/member/member-files.css
```

Expected: 团队空间是账号级 section，不读取 `MemberFilesApp.activeSpaceId`。

---

### Task 7: 生产侧连续 Tab 与“空间作为文件夹”内容层

**Files:**
- Modify: `web/src/member/spaceSelection.ts`
- Modify: `web/src/member/spaceSelection.test.ts`
- Modify: `web/src/member/spaceNavigation.ts`
- Modify: `web/src/member/spaceNavigation.test.ts`
- Modify: `web/src/member/MemberWorkspaceNavigation.tsx`
- Modify: `web/src/member/MemberWorkspaceNavigation.test.tsx`
- Create: `web/src/member/MemberSpaceDirectory.tsx`
- Create: `web/src/member/MemberSpaceDirectory.test.tsx`
- Modify: `web/src/member/MemberFilesApp.tsx`
- Modify: `web/src/member/MemberSharesPanel.tsx`
- Modify: `web/src/member/member-files.css`

- [ ] **Step 1: 写 scope、目录层和打开空间的失败测试**

在 `spaceNavigation.test.ts` 固定 `spacesForScope`：`personal` 只返回 personal 空间；`team` 只返回 shared 空间并按名称、ID 稳定排序；未知类型不进入任一目录。

在 `MemberWorkspaceNavigation.test.tsx` 反转旧断言：侧栏只出现“个人空间”“团队空间”两个 scope Tab，不再出现“产品设计”“研发协作”等具体空间名；点击团队空间调用 `onSelectScope('team')`。

在 `MemberSpaceDirectory.test.tsx` 使用 Testing Library 断言：团队根显示多个空间为 `data-space-id` 文件夹行；点击“研发协作”调用 `onOpenSpace('shared-alpha')`；根目录不显示上传、新建文件夹或删除操作。

在 `spaceSelection.test.ts` 添加同空间和跨空间两种转换：

```ts
expect(resolveOpenSpaceFilesTransition('shared-team', 'shared-team')).toEqual({
  activeTab: 'files', spaceRootScope: null, activeSpaceId: 'shared-team', resetSpaceState: false,
});
expect(resolveOpenSpaceFilesTransition('personal', 'shared-team')).toEqual({
  activeTab: 'files', spaceRootScope: null, activeSpaceId: 'shared-team', resetSpaceState: true,
});
```

这条区别很重要：打开当前空间时不能先清空 mounts，否则 `activeSpaceId` 未变化，加载 effect 不会重跑，文件页会变空。

- [ ] **Step 2: 运行相关测试并确认 RED**

Run:

```bash
cd web
npx vitest run src/member/spaceNavigation.test.ts src/member/MemberWorkspaceNavigation.test.tsx src/member/MemberSpaceDirectory.test.tsx src/member/spaceSelection.test.ts
```

Expected: FAIL；当前生产侧仍逐个把空间列在独立导航区，也没有空间根目录状态。

- [ ] **Step 3: 实现空间 scope 和文件夹目录纯规则**

在 `spaceNavigation.ts` 定义：

```ts
export type MemberSpaceScope = 'personal' | 'team';

export function spacesForScope(spaces: MemberSpace[], scope: MemberSpaceScope): MemberSpace[];
export function scopeForSpace(space: MemberSpace | null | undefined): MemberSpaceScope;
```

`spacesForScope` 不修改输入数组；团队空间按现有 collator 的名称、ID 稳定排序。`scopeForSpace` 仅把 `type === 'personal'` 映射为 personal，其余当前可访问 shared 空间映射为 team。

把 `MemberWorkspaceNavigation` 改成只返回两个 `.member-nav` 按钮的 Fragment：

```ts
type MemberWorkspaceNavigationProps = {
  activeScope: MemberSpaceScope;
  onSelectScope: (scope: MemberSpaceScope) => void;
  personalLabel: string;
  teamLabel: string;
};
```

组件不得再渲染 section 标题、空态或具体空间名，以便它和账号功能按钮位于同一个 `<nav>`。

- [ ] **Step 4: 实现文件页第一层空间目录**

`MemberSpaceDirectory` 接收 `scope`、已筛选 spaces、loading/error 和 `onOpenSpace`。每个空间使用文件夹图标、名称和当前角色；团队根可以有多个文件夹，个人根通常只有一个。这里是导航层，不提供文件上传、删除或新建文件夹。

在 `MemberFilesApp` 新增 `spaceRootScope: MemberSpaceScope | null`：

- 点击“个人空间/团队空间”时将主 Tab 切到 `files`，设置对应 `spaceRootScope`，但不把空间状态写入分享、Token、文档或账户页。
- `activeTab === 'files' && spaceRootScope !== null` 时渲染 `MemberSpaceDirectory`；点击空间文件夹后进入现有文件工作台，并设置 `spaceRootScope = null`。
- 在具体空间面包屑提供返回 scope 根目录的入口；返回只改变目录层，不删除空间或挂载数据。
- `loadSpaces()` 在当前空间失去访问权时修正 `activeSpaceId`；当正在显示 scope 根时，不应强制跳进某个空间。

- [ ] **Step 5: 把空间 scope 和账号功能合并为同一连续 nav**

生产侧结构固定为一个 `<nav aria-label="Member workspace">`：先渲染 `MemberWorkspaceNavigation` 的“个人空间/团队空间”，紧接着渲染“文件、回收站、分享、AI Token、文档、账户”。所有按钮使用同一 `.member-nav` 和 `gap: var(--space-1)`；不得再出现 `.member-workspace-group`、section margin 或第二个导航容器。

空间 scope 选中态与账号 Tab 选中态是两个维度，可以同时高亮；它们只在语义上分别表示文件范围和账号功能，不形成视觉分组。

- [ ] **Step 6: 从分享页打开空间，避免同空间空白页**

在 `spaceSelection.ts` 实现 `resolveOpenSpaceFilesTransition(currentSpaceId, targetSpaceId)`，返回 `activeTab='files'`、`spaceRootScope=null` 和 `resetSpaceState`。

在 `MemberFilesApp.openAccountSpace(spaceId)` 中：

1. 设置 `activeTabRef.current` 和 React `activeTab` 为 `files`。
2. 设置 `spaceRootScope` 为 `null`。
3. 仅当 `resetSpaceState` 为 true 时清空 `pendingDirectoryPathRef` 并调用现有 `applySpaceTransition(spaceId)`；同一空间保留已加载 mounts 和路径。
4. 向 `MemberSharesPanel` 传入 App 的 `spaces`、`openAccountSpace` 和 `loadSpaces`。

- [ ] **Step 7: 运行生产导航、目录和分享回调测试**

Run:

```bash
cd web
npx vitest run src/member/spaceNavigation.test.ts src/member/MemberWorkspaceNavigation.test.tsx src/member/MemberSpaceDirectory.test.tsx src/member/spaceSelection.test.ts src/member/MemberTeamSpacesSection.test.tsx
```

Expected: PASS；具体空间只在内容区出现，左侧是一个连续 Tab 列表，从分享页打开当前空间不会出现空白文件页。

- [ ] **Step 8: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- web/src/member/spaceSelection.ts web/src/member/spaceSelection.test.ts web/src/member/spaceNavigation.ts web/src/member/spaceNavigation.test.ts web/src/member/MemberWorkspaceNavigation.tsx web/src/member/MemberWorkspaceNavigation.test.tsx web/src/member/MemberSpaceDirectory.tsx web/src/member/MemberSpaceDirectory.test.tsx web/src/member/MemberFilesApp.tsx web/src/member/MemberSharesPanel.tsx web/src/member/member-files.css
```

Expected: 左侧不列具体空间；空间以内容区第一层文件夹进入；普通空间刷新不改变 shares/tokens/docs/account 的账号级状态。

---

### Task 8: 修正预览左侧连续导航

**Files:**
- Modify: `web/src/member/ShareCollaborationPreview.tsx`
- Modify: `web/src/member/ShareCollaborationPreview.css`
- Modify: `web/src/member/ShareCollaborationPreview.test.tsx`

- [ ] **Step 1: 把测试改成期望单一导航并确认 RED**

首个预览测试改为：

```ts
const html = renderToStaticMarkup(<ShareCollaborationPreview />);
expect(html).toContain('aria-label="成员导航"');
expect((html.match(/<nav/g) ?? []).length).toBe(1);
expect(html).not.toContain('aria-label="空间选择"');
expect(html).not.toContain('aria-label="账户功能"');
expect(html).toContain('个人空间');
expect(html).toContain('团队空间');
expect(html).toContain('文件');
expect(html).toContain('账户');
```

再用 `<ShareCollaborationPreview initialAccountTab="shares" />` 断言同一个账号级内容页同时出现“公开分享”和“团队空间”，包含 manager/editor/viewer mock 数据；只有 manager 卡片显示“管理成员”。

- [ ] **Step 2: 运行预览测试并确认 RED**

Run:

```bash
cd web
npx vitest run src/member/ShareCollaborationPreview.test.tsx
```

Expected: FAIL，当前 sidebar 仍渲染两个 `<nav>`。

- [ ] **Step 3: 合并导航结构并移除视觉断层**

`ShareCollaborationPreviewSidebar` 只保留：

```tsx
<nav className="member-preview-sidebar-nav" aria-label="成员导航">
  {(Object.keys(spaceScopeLabels) as SpaceScope[]).map((scope) => (
    <button className={`member-nav ${activeScope === scope ? 'active' : ''}`} type="button" role="tab" aria-selected={activeScope === scope} onClick={() => onSelectScope(scope)} key={scope}>
      {spaceScopeLabels[scope]}
    </button>
  ))}
  {accountTabs.map((tab) => (
    <button className={`member-nav ${activeAccountTab === tab.id ? 'active' : ''}`} type="button" aria-current={activeAccountTab === tab.id ? 'page' : undefined} onClick={() => onSelectAccountTab(tab.id)} key={tab.id}>
      {tab.label}
    </button>
  ))}
</nav>
```

保留 scope 的 `aria-selected` 与账号功能的 `aria-current`，但视觉上使用同一个 `.member-nav` 列表。CSS 将 `.member-preview-sidebar-nav` 的 `gap` 改为 `var(--space-1)`，删除 `.member-preview-scope-switcher`、`.member-preview-main-nav` 和两个 nav 容器造成的额外间距规则。MCP 提示卡仍作为导航后的次要说明，不插入 Tab 列表中。

为 `ShareCollaborationPreview` 增加 `initialAccountTab?: AccountTab`。当 Tab 为 `shares` 时渲染本地 mock 的账号级分享工作区：

- “公开分享”区域展示至少一条 mock 分享，不发网络请求。
- 复用 `MemberTeamSpacesSection`，注入内存 `TeamSpaceService`；预置 manager/editor/viewer 团队空间和成员。
- 创建空间、打开成员弹窗、默认 viewer、修改/移除成员均只更新 React 内存状态，刷新页面即复原。
- “打开文件”调用现有 preview `changeSpace` 并切回 `files`，证明分享页到文件内容的跳转。

不得为预览复制一套生产权限判断；角色按钮可见性必须来自同一个 `MemberTeamSpacesSection`。

- [ ] **Step 4: 运行预览测试与生产构建**

Run:

```bash
cd web
npx vitest run src/member/ShareCollaborationPreview.test.tsx
npm run build
```

Expected: 测试和 build 均 PASS。

- [ ] **Step 5: 检查本任务差异并保持未提交**

Run:

```bash
git diff -- web/src/member/ShareCollaborationPreview.tsx web/src/member/ShareCollaborationPreview.css web/src/member/ShareCollaborationPreview.test.tsx
```

Expected: 左侧只有一个 nav，个人/团队与账号功能之间没有 section gap。

---

### Task 9: 更新 OpenAPI、产品文档与延期 P2 文件

**Files:**
- Modify: `openapi/omnora.v1.yaml`
- Regenerate: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `docs/api/README.md`
- Modify: `docs/design/domain-model.md`
- Modify: `docs/design/web-application-design.md`
- Modify: `docs/design/personal-storage-and-folder-collaboration.md`
- Modify: `docs/mcp/README.md`
- Modify: `docs/security/security-model.md`
- Create: `docs/design/account-space-known-issues.md`

- [ ] **Step 1: 先让文档验证识别缺失契约**

在 `openapi/omnora.v1.yaml` 的 paths 设计位置加入三个 path item、四个 operation 的测试目标，并先运行现有验证：

```bash
scripts/verification/verify-api-docs.sh
```

Expected: 在源文件尚未同步到 embedded asset 时失败，报告 OpenAPI 副本不一致。

- [ ] **Step 2: 写完整 OpenAPI 契约**

新增三个 path item，其中成员详情 path 同时承载 PUT 和 DELETE，避免 OpenAPI 中出现仅参数名不同的歧义模板：

```yaml
/team-spaces:
  post:
    summary: Create a team space with managed storage.
/team-spaces/{spaceId}/members:
  get:
    summary: List team-space members as a space manager.
/team-spaces/{spaceId}/members/{accountRef}:
  put:
    summary: Add or update a team-space member.
  delete:
    summary: Remove a team-space member.
```

DELETE 客户端仍必须传成员列表返回的规范 `accountId`，但线上的 path 参数统一命名为 `accountRef`。

每个写操作声明 cookie session、CSRF、JSON body、成功 DTO，以及 `400/401/403/404/409/503`。Schemas 增加 `TeamSpaceCreateRequest`、`TeamSpaceCreateResponse`、`TeamSpaceMember`、`TeamSpaceMemberUpdateRequest`；响应不得含 `rootPath`、身份指纹或宿主机信息。

- [ ] **Step 3: 同步文档语义**

逐文件做以下确定修改：

- `domain-model.md`: 改为浏览器创建分享要求 manager；`space_grant` Token 在账号 ACL 和 Token grant 均至少 editor 且具备 scope/MRTR 时可创建。
- `web-application-design.md`: 分享页第二部分为团队空间；任意 active 账号可创建；创建者 manager；manager 管成员；文件、回收站、分享、Token、文档、账户仍是账号级功能；删除“双层导航”旧决策。
- `personal-storage-and-folder-collaboration.md`: 删除个人目录协作作为当前实现方向的描述，改为团队空间 ACL；外部 NAS 插槽仍不得自动创建空间子目录；团队空间托管目录只位于 managed root。
- `mcp/README.md`: `shares.create` 写明 editor space grant、账号实时 ACL、scope、黑名单、挂载写模式和 MRTR 的交集。
- `security-model.md`: 浏览器 manager 与 Token editor 分开表述；团队成员接口只允许当前 space manager；最后 manager fail-closed；成员不能配置宿主路径。
- `api/README.md`: 增加四个成员端团队空间接口及错误码。

- [ ] **Step 4: 创建延期问题文件**

`docs/design/account-space-known-issues.md` 必须写成可验收的问题记录，而不是模糊待办：

```markdown
# 账号与空间已知问题

## P2-1 回收站导航与内容状态不一致

- 状态：已确认，按产品决策延期，不在账号级团队空间任务中修复。
- 触发：处于 `trash` 时切换到不支持回收站的挂载。
- 当前结果：Tab 入口隐藏，但 `activeTab` 仍为 `trash`，内容继续显示回收站空态。
- 期望验收：导航和内容状态必须一致，并明确不支持回收站的原因。
- 代码证据：`web/src/member/MemberFilesApp.tsx`、`web/src/member/spaceSelection.ts`。

## P2-2 AI Token 黑名单指纹采集早于账号 ACL 校验

- 状态：已确认，按产品决策延期，不在账号级团队空间任务中修复。
- 触发：创建或编辑 Token 时提交无权访问空间的 denylist 目录。
- 当前结果：服务端先解析挂载并采集目录指纹，再在保存阶段校验账号空间权限。
- 期望验收：先确认账号 ACL 和 grant 上限，再读取挂载或目录身份。
- 代码证据：`internal/server/api.go`、`internal/aitoken/service.go`。
```

- [ ] **Step 5: 同步 OpenAPI 嵌入副本并验证**

Run:

```bash
scripts/verification/sync-openapi-asset.sh sync
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
```

Expected: 全部 PASS。

- [ ] **Step 6: 检查文档差异并保持未提交**

Run:

```bash
git diff -- openapi/omnora.v1.yaml internal/server/openapi_assets/omnora.v1.yaml docs/api/README.md docs/design/domain-model.md docs/design/web-application-design.md docs/design/personal-storage-and-folder-collaboration.md docs/mcp/README.md docs/security/security-model.md docs/design/account-space-known-issues.md
```

Expected: 不再把“与我共享/个人目录协作”描述为当前功能；两个 P2 明确标注延期且没有对应代码修改。

---

### Task 10: 完整验证与本地预览验收

**Files:**
- Verify only; no new production files.

- [ ] **Step 1: 格式化本任务 Go 文件**

Run:

```bash
gofmt -w internal/server/team_spaces.go internal/server/team_spaces_test.go internal/server/route_matrix.go internal/server/route_matrix_test.go internal/server/api.go internal/server/admin_control.go internal/server/mcp_security_parity_test.go internal/membershare/service.go
```

Expected: 命令无错误；只格式化本计划涉及的 Go 文件。

- [ ] **Step 2: 运行相关 Go 包测试**

Run:

```bash
GOCACHE=/private/tmp/omnora-team-space-go-cache go test ./internal/server ./internal/membershare ./internal/access ./internal/memberfiles ./internal/aitoken ./internal/mcpapi -count=1
```

Expected: 全部 PASS。

- [ ] **Step 3: 运行完整前端测试和生产构建**

Run:

```bash
cd web
npx vitest run
npm run build
```

Expected: 全部测试 PASS；TypeScript 与 Vite build 无错误。

- [ ] **Step 4: 验证文档和协议副本**

Run:

```bash
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
scripts/verification/verify-mcp-protocol.sh
```

Expected: OpenAPI 同步与本地 MCP 协议验证 PASS；如脚本明确要求在线 URL/Token，只记录该在线项为未运行，不把它表述为通过。

- [ ] **Step 5: 验证本地预览**

确认 Vite 使用本工作树，在 `http://127.0.0.1:5176/preview/sharing` 检查：

1. 左侧个人空间、团队空间、文件、回收站、分享、AI Token、文档、账户连续排列。
2. 空间与账号功能之间没有额外分组间距。
3. “分享”设计文案使用“团队空间”，不再出现“与我共享”。
4. 切到“分享”后同时看到 mock“公开分享”和“团队空间”；manager/editor/viewer 标签、manager-only 成员入口、默认 viewer 和“打开文件”跳转符合规则。

- [ ] **Step 6: 检查格式和工作树范围**

Run:

```bash
git diff --check
git status --short
```

Expected: `git diff --check` 无输出；状态只包含当前工作树原有改动和本计划列出的新增改动，没有自动提交或暂存。

---

## Execution order and parallel boundaries

- Tasks 1–3 必须串行：共享同一组 Go routes/handlers/tests。
- Task 4 可在 Task 2–3 期间独立执行：只改 `internal/membershare/service.go` 与独立的 MCP 测试文件，不写团队空间 handler。
- Task 5 可与后端 Tasks 1–4 并行：只改 API 客户端和纯规则文件。
- Task 6 先固定 `MemberTeamSpacesSection` 与 service 注入契约；随后 Task 7 接生产 App，Task 8 接 mock preview，二者可并行且不写同一文件。
- Task 9 在 API/行为稳定后执行，避免 OpenAPI 与实现漂移。
- Task 10 最后串行执行，统一验证所有结果。
