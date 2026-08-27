# 管理端挂载治理与 Space 残留清理设计

状态：已确认，待实施  
日期：2026-08-09  
决策：采用独立挂载治理纵向切片，先修复管理端 501，再分阶段彻底移除用户端与服务端 Space 残留

## 1. 背景

账号—挂载模型已经通过[账号、挂载与内容授权设计](2026-08-08-account-mount-access-design.md)冻结，并由离线迁移 013 建立 `mounts`、`personal_directories`、`mount_grants` 与 `folder_collaborations`。然而当前实现只完成了部分迁移：

- 成员端已经新增“个人空间 / 团队空间 / 协作”读取入口，但旧 Space 文件工作区仍保留在同一应用中；
- 管理端导航隐藏了 Space 页面，但挂载表单仍要求选择 Space；
- 前端继续请求旧 `/api/v1/admin/spaces`、`/api/v1/admin/mounts` 和 `/api/v1/admin/host-directories`；
- 后端已取消这些旧路由的注册，请求最终落入 `/api/v1/` 占位处理器并返回 HTTP 501；
- 旧处理器仍查询已被迁移 013 删除的 `spaces`、`space_members`、`mounts.space_id` 和 `mounts.kind`，不能通过简单恢复路由继续使用。

因此，501 不是单一路由遗漏，而是管理端前端与账号—挂载后端契约未同步的表现。本设计不恢复 Space 兼容层，而是先交付新模型下完整、可测试的管理端挂载治理闭环。

## 2. 全量迁移拆分

全量 Space 清理分四个可独立验收的阶段：

1. **管理端挂载治理**：新挂载与授权 API、外部目录建议、索引任务、管理端 UI 和 REST 未匹配错误语义；本设计覆盖该阶段。
2. **成员文件操作**：浏览、预览、搜索、上传、移动、复制、删除和回收站全部改用显式内容源联合类型。
3. **分享与 Token**：分享创建、列表和治理删除 `spaceId/spaceName`；AI Token、MCP 和 REST 契约保持账号—挂载模型一致。
4. **最终清理**：删除旧 Space UI、API、服务端死代码、测试夹具、文案和失效文档引用，执行全仓库零残留验收。

每一阶段必须使用目标模型，不允许引入双写、Space ID 转换或旧接口兼容层。

## 3. 当前 Space 残留清单

### 3.1 管理端

| 区域 | 当前残留 | 目标阶段 |
| --- | --- | --- |
| `web/src/member/AdminWorkspace.tsx` | `MountForm.spaceId`、空间列表加载、空间下拉框、挂载表格空间列、Space 版挂载标签和索引任务标签 | 阶段 1 |
| `web/src/api.ts` | `AdminSpacePayload`、Space CRUD/成员 ACL 客户端、Space 版挂载请求和响应 | 阶段 1 删除挂载依赖；阶段 4 删除剩余死接口 |
| `web/src/member/AdminPanels.tsx` | 隐藏但仍编译的 `AdminSpacesPanel`、Space ACL、`manager` 内容权限 | 阶段 4 |
| `web/src/member/i18n.ts` | 挂载空间、Space ACL、空间增删改和 `manager` 文案 | 阶段 1 删除挂载文案；阶段 4 删除其余文案 |
| `internal/server/api.go` | `listAdminSpaces`、Space 版 `listAdminMounts/createMount`、索引任务 `spaceName` | 阶段 1 替换挂载与索引；阶段 4 删除死代码 |
| `internal/server/mount_admin.go` | `adminMountRecord.SpaceID/SpaceName`、`mounts.space_id`、`mounts.kind` 与 `spaces` JOIN | 阶段 1 替换 |
| `internal/server/admin_control.go` | Space CRUD、Space 成员 ACL 和审计 `spaces` JOIN | 阶段 4 |
| 路由注册 | 管理挂载、目录建议和索引路由被移除，页面落入 501 | 阶段 1 |

### 3.2 成员端

| 区域 | 当前残留 | 目标阶段 |
| --- | --- | --- |
| `web/src/member/MemberFilesApp.tsx` | `spaces/activeSpaceId` 状态、旧 Space 权限判断、Space 版文件操作、跨挂载目标空间；“个人空间 / 团队空间”展示名保留 | 阶段 2 |
| `MemberSpaceDirectory.tsx`、`spaceNavigation.ts` | 旧 Space 角色目录和 Space 坐标导航 | 阶段 2 删除 |
| `web/src/api.ts` | `/spaces/{spaceId}` 浏览、下载、搜索、写入、回收站和跨挂载操作 | 阶段 2 |
| `web/src/member/MemberSharesPanel.tsx` | 分享创建和选择器仍要求 `spaceId`，位置显示 `spaceName` | 阶段 3 |
| `web/src/member/uploadQueue.ts` | 上传恢复键包含 `spaceId` | 阶段 2 |
| `web/src/member/MemberDocsPanel.tsx` | 展示已移除的 Space REST 路径 | 阶段 2/4 |
| `web/src/member/i18n.ts` | 当前空间、目标空间、Space 坐标等旧业务文案；“个人空间 / 团队空间”作为成员端展示名保留 | 阶段 2/4 |

### 3.3 服务端与协议

| 区域 | 当前残留 | 目标阶段 |
| --- | --- | --- |
| `internal/server/api.go` | 未注册的 Space 文件处理器、`canReadSpace/hasSpacePermission` 空实现、Space 版上传和分享请求字段 | 阶段 2/3/4 |
| `internal/server/file_ops.go` | `toSpaceId` 与 Space 版跨挂载响应 | 阶段 2 |
| `internal/server/member_shares.go` | `spaceId/spaceName` DTO 与旧 JOIN | 阶段 3 |
| 服务端测试 | 大量测试仍建立 `spaces/space_members` 夹具或请求旧路径 | 各阶段随生产行为迁移，阶段 4 零残留 |
| OpenAPI | 当前主规范已基本使用新资源，但管理挂载 schema 仍过于宽松，需冻结准确字段 | 阶段 1 |
| REST catch-all | 所有未注册 `/api/v1/*` 请求返回通用 501 | 阶段 1 |

CSS 中的 `--space-*` 设计令牌和表示布局间距的 `.member-space-*` 历史类名不属于业务 Space 实体。`personal_files.go`、MCP 输入校验和离线迁移不变量中对 `spaceId/space_id` 的显式拒绝或残留检测也是目标安全守卫，不得作为残留误删。成员端可见的“个人空间 / 团队空间”是产品展示名，也不属于旧 Space 实体。最终零残留检查只禁止有效业务模型、成功 API 和用户文案中的旧 Space 坐标、空间成员、空间 ACL、空间目录等概念，不机械删除 spacing 令牌、负向兼容拒绝或已确认的展示名；历史业务类名可在相关组件删除时自然清理。

## 4. 阶段 1 范围

### 4.1 纳入

- 独立管理端挂载治理服务；
- 普通与受限共用挂载的列表、创建、修改、重验证和解除；
- 挂载 `viewer/editor` 授权的列表、设置和撤销；
- 外部容器映射目录建议；
- 按挂载管理索引任务；
- 管理端挂载与索引前端迁移；
- `isInitialAdmin` 会话能力；
- OpenAPI 管理挂载契约冻结；
- 已移除 Space 路由的 410 和未知 REST 路由的 404；
- 管理挂载相关旧 Space 类型、文案和依赖清理。

### 4.2 不纳入

- 成员文件写操作与旧文件工作区迁移；
- 分享创建、分享治理和分享 DTO 迁移；
- 默认个人挂载专用故障修复向导；
- 删除全部不可达 Space 服务端函数；
- 与本阶段无关的管理审计或治理页面重写。

默认个人挂载继续由现有启动校验创建并 fail-closed，但不会出现在普通挂载列表、授权接口或目录建议中。专用修复向导在后续独立设计中实现，不得以普通挂载接口代替。

## 5. 架构

### 5.1 独立业务边界

新增独立 `mountadmin` 业务服务，负责：

- 解析操作者是否为初始管理员；
- 应用普通/受限治理可见性；
- 查询与修改共用挂载；
- 管理挂载授权；
- 在事务中协调挂载、授权、派生能力失效和审计回调；
- 返回稳定的领域错误供 HTTP 层映射。

HTTP 层只负责会话认证、请求解码、调用服务、错误映射与响应编码。新逻辑不继续堆入 `api.go` 的 Space 版处理器，也不复用旧 `adminMountRecord`。

### 5.2 管理路由

注册以下精确路由：

```text
GET    /api/v1/admin/mounts
POST   /api/v1/admin/mounts
PATCH  /api/v1/admin/mounts/{mountId}
DELETE /api/v1/admin/mounts/{mountId}
POST   /api/v1/admin/mounts/{mountId}/reverify
GET    /api/v1/admin/mounts/{mountId}/grants
PUT    /api/v1/admin/mounts/{mountId}/grants/{accountId}
DELETE /api/v1/admin/mounts/{mountId}/grants/{accountId}
GET    /api/v1/admin/host-directories
GET    /api/v1/admin/index-jobs
POST   /api/v1/admin/index-jobs
POST   /api/v1/admin/index-jobs/{jobId}/run
```

所有路由继续受 REST 路由组和管理员完整会话保护。

### 5.3 REST 未匹配行为

`/api/v1/` 不再由“已启用但未实现”处理器返回 501。行为改为：

- 已知旧 Space 路径返回 `410 space_api_removed`；
- 其他未注册 REST 路径返回 `404 not_found`；
- REST 路由组关闭时仍由外层 gate 返回 `404 route_group_disabled`。

501 只适用于确实存在、已协商但尚未实现的具体产品能力，不能作为 REST catch-all。

## 6. 数据契约

### 6.1 管理挂载 DTO

管理端挂载响应包含：

```text
id
displayName
rootPath
governance: normal | restricted
mode: read_only | read_write
indexEnabled: boolean
shareEnabled: boolean
status: pending | active | disabled | unavailable
grantCount: integer
```

只返回 `purpose = common` 的挂载。默认个人挂载、已删除挂载和调用者不可发现的受限挂载不进入响应。

### 6.2 创建挂载

请求字段：

```text
displayName
rootPath
governance: normal | restricted
mode: read_only | read_write
indexEnabled
grants: [{ accountId, permission: viewer | editor }]
```

约束：

- `purpose` 固定为 `common`；
- `storage_kind` 固定为 `external`；
- 普通管理员只能创建 `normal`；
- 初始授权允许为空；
- 同一账号不能重复出现；
- 授权账号必须存在且处于 active；
- 创建者和管理员不会自动获得内容授权；
- 挂载与初始授权在一个数据库事务中提交，并写入审计。

### 6.3 修改挂载

`PATCH` 只允许修改：

```text
displayName
mode
indexEnabled
shareEnabled
```

`rootPath`、`purpose`、`storage_kind` 和 `governance` 创建后不可修改。需要改变治理类型或根路径时，必须解除并重新注册，生成新的挂载 ID。

### 6.4 授权

授权接口只接受 `viewer/editor`：

- `GET` 返回 `{items: [...]}`，每项包含 `accountId/email/displayName/permission`；
- `PUT` 请求为 `{permission: "viewer" | "editor"}`，幂等创建或更新并返回 200 与当前授权；
- `DELETE` 幂等撤销已有授权，无论原记录是否存在均返回 204；
- 默认个人挂载拒绝所有普通授权；
- 授权、降权或撤销后，后续 Web、REST、MCP、上传和 Transfer Ticket 立即按实时权限重验。

### 6.5 解除挂载

`DELETE` 请求必须提交当前显示名称进行精确确认。成功响应固定包含：

```json
{
  "id": "<mount-id>",
  "deleted": true,
  "deleteData": false,
  "dataDeleted": false
}
```

解除操作：

- 将挂载标记为删除并保留 tombstone；
- 撤销授权、Token boundary、分享和票据；
- 取消上传和后台任务；
- 清理索引与控制面对象；
- 记录不泄露隐藏对象的审计；
- 不删除、移动或改名 NAS 真实目录和文件。

## 7. 授权与隐藏

### 7.1 普通挂载

所有管理员都能发现和治理普通挂载，但内容访问仍要求显式 `mount_grants`。

### 7.2 受限挂载

- 只有初始管理员能列出、创建、修改、重验证、解除和配置授权；
- 普通管理员直接请求已知受限挂载 ID 时返回与不存在相同的 `404`；
- 普通管理员即使拥有内容授权，也不能看到治理类型、授权名单或管理入口；
- 初始管理员未获内容授权时也不能浏览挂载内容；
- 管理概览、索引任务、审计和普通错误信息不得向普通管理员泄露受限挂载。

### 7.3 冲突错误

路径、显示名称和身份冲突检查必须覆盖包括隐藏受限挂载在内的全部活跃挂载。普通管理员提交的候选资源若命中隐藏对象、保留对象或其他不可用条件，统一返回 `409 mount_unavailable`，不得返回冲突对象名称、ID、路径或治理类型。

## 8. 目录建议与挂载身份

目录建议接口只列出部署时显式映射、允许注册为外部共用挂载的容器目录：

- 不返回受保护的 managed personal root；
- 不暗示能浏览宿主机任意绝对路径；
- 不创建 `<space-id>`、账号 ID 或其他业务子目录；
- 选择的目录本身就是挂载根；
- 注册和重验证继续校验规范路径、符号链接、bind 来源、父子重叠、身份冲突和读写能力。

普通管理员的建议响应不能因受限挂载存在而暴露额外元数据。

## 9. 索引任务

索引任务契约只使用 `mountId` 和挂载名称，不再返回 `spaceName`：

- 只能为 `purpose = common` 且 `index_enabled = 1` 的可治理挂载排队；
- 普通管理员的列表、直接运行和状态统计必须过滤受限挂载；
- 初始管理员可管理普通与受限挂载索引；
- 默认个人挂载使用独立个人索引策略，不进入普通管理任务接口；
- 任务 payload 和响应不得新增任何 Space 字段。

## 10. 管理端界面

### 10.1 页面加载

挂载页面并行加载：

- 当前管理员可治理的共用挂载；
- 可作为授权目标的 active 账号；
- 外部目录建议。

任一请求失败时显示对应结构化错误，不保留空的必填 Space 下拉框。

### 10.2 创建表单

删除：

- Space 选择器；
- `managed/external` 类型选择；
- 插槽内自动创建 Space ID 子目录提示。

保留并调整：

- 外部目录；
- 显示名称；
- 只读/读写；
- 索引开关；
- 初始授权。

仅当会话返回 `isInitialAdmin = true` 时展示普通/受限治理选择；服务端不信任该 UI 判断。

### 10.3 列表与授权详情

挂载表格展示：名称、治理类型、模式、索引、公开分享策略、健康和操作，不再展示空间列。选中挂载后在详情区管理授权与 `shareEnabled`，权限选项仅有 `viewer/editor`。

索引任务选择器和任务列表只展示挂载名称。所有成功操作刷新当前挂载、授权或任务状态；失败保留表单输入和上下文。

## 11. 会话能力

当前会话响应增加 `isInitialAdmin`：

- 由持久化的初始管理员 ID 与当前账号 ID 比较得出；
- 只用于前端能力展示；
- 不替代任何服务端授权；
- 普通管理员的 `isAdmin` 仍为 true，`isInitialAdmin` 为 false。

## 12. OpenAPI

主 OpenAPI 与嵌入副本必须同步：

- 管理挂载请求和响应改为明确 schema，不使用无约束 `additionalProperties`；
- 增加挂载授权路由与 DTO；
- 创建请求删除 `spaceId` 和 `kind`；
- 索引任务删除 `spaceName`；
- 文档明确 restricted 的 404 隐藏语义与 `mount_unavailable`；
- 解除响应明确 `dataDeleted: false`。

## 13. 测试策略

### 13.1 服务端

所有新测试使用迁移 013 后的生产 schema，覆盖：

- 精确管理路由均不返回 501；
- 创建零授权挂载和带初始授权挂载；
- 管理员不会被自动授予内容权限；
- 普通管理员与初始管理员的普通/受限可见性矩阵；
- 受限挂载的列表、直接 ID、授权、索引和概览隐藏；
- 隐藏对象冲突统一为 `mount_unavailable`；
- 默认个人挂载不进入列表、授权和目录建议；
- 重命名、模式、索引和分享开关修改；
- 根路径与治理类型修改被拒绝；
- 重验证身份和读写探针；
- 解除挂载撤销派生能力且保留宿主文件；
- Space 路由返回 410；
- 未知 REST 路由返回 404；
- REST 路由组关闭仍返回 `route_group_disabled`。

### 13.2 前端

组件与 API 测试覆盖：

- 挂载页不出现 Space 文案、空间选择、空间列、`spaceId` 或托管共用挂载；
- 普通管理员看不到 restricted 选项；
- 授权只接受 `viewer/editor`；
- 创建、修改、重验证、授权与解除请求符合新契约；
- 索引任务不显示或提交 Space 字段；
- 结构化错误保留用户输入；
- 中文与英文文案同步。

执行聚焦 Vitest、完整 Vitest、TypeScript 检查和 Vite 生产构建。

### 13.3 回归

执行完整 Go 测试和 Web 构建。旧 Space 测试若覆盖阶段 1 已替换的行为，应迁移到新 schema；与后续阶段相关的旧测试可以暂时保留，但不得被本阶段的新路由重新激活。

## 14. 验收标准

阶段 1 完成时：

1. 管理端挂载页不再显示“空间”下拉框或空间列；
2. 页面能正常加载外部目录、挂载和授权；
3. 普通管理员可治理普通挂载，初始管理员可额外治理受限挂载；
4. 创建、修改、授权、索引、重验证和解除形成完整闭环；
5. 管理挂载请求不再返回 HTTP 501；
6. 未知 REST 路径使用 404，已移除 Space 路径使用 410；
7. 默认个人挂载和受限挂载满足不可发现性边界；
8. 解除挂载后 NAS 真实文件仍存在；
9. OpenAPI、嵌入规范、前端类型和服务端响应一致；
10. 阶段 2 至 4 的残留清单仍明确可追踪，不通过兼容层掩盖。
