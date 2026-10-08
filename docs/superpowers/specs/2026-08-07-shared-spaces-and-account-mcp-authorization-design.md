# 共享空间与账号级 MCP 授权设计

## 1. 背景

Omnora 当前已经有 `personal` 与 `shared` 两类空间，也已经把 AI Token 归属于账号。但成员端把所有空间混在文件页的次级列表中；AI Token 又要求在创建时逐条绑定 `space + mount + relative_path`，创建后只能撤销，不能编辑。

本设计完成两项统一调整：

1. 将“共享空间”提升为与“个人空间”平级的主文件工作区，并在左侧切换多个共享空间。
2. 将 MCP Token 明确为账号凭据，以整个空间为主要授权粒度；每个空间单独设置仅查看或可编辑，并支持文件夹黑名单。

## 2. 术语

- **个人空间**：一个账号独占的 `personal` 空间。
- **共享空间**：通过 `space_members` 管理多个成员的 `shared` 空间。
- **挂载**：空间中的数据来源，可以是系统托管目录或外部 NAS 目录；挂载不是第三类空间。
- **AI Token**：归属于账号、用于 MCP Bearer 认证的凭据。
- **空间授权**：某枚 Token 对一个空间的权限上限，取值为 `viewer` 或 `editor`。
- **文件夹黑名单**：某枚 Token 在已授权空间内不得访问的目录；目录自身及全部后代均被拒绝。

## 3. 产品信息架构

成员端左侧一级导航：

```text
个人空间
  我的个人空间

共享空间
  研发资料库
  2026 旅行共建
  客户项目 A

公开分享
AI Token
文档
账号
```

规则：

- 个人空间和共享空间都进入完整文件工作台。
- “共享空间”下直接列出当前账号可访问的所有共享空间，点击即切换。
- 当前空间高亮；切换空间时重置挂载、目录、搜索、回收站和待执行操作状态。
- 共享空间为零时显示空态，不隐藏主入口。
- 外部 NAS 仍表现为空间内的挂载，不出现“挂载空间”术语。

## 4. MCP 授权模型

### 4.1 归属与权限交集

Token 继续归属于 `ai_tokens.account_id`。有效权限为：

```text
账号有效状态
∩ 账号实时空间 ACL
∩ Token 全局 scopes
∩ Token 对目标空间的 viewer/editor 授权
∩ 挂载状态与读写模式
∩ 非黑名单路径
∩ 挂载身份与安全路径校验
∩ 高风险操作逐次确认
```

Token 不能提升账号权限：账号是 `viewer` 时，即使 Token 的空间授权为 `editor` 且含写 scope，实际仍只读。

### 4.2 整空间授权

- 每枚 Token 必须显式选择至少一个空间。
- 账号后来加入的新共享空间不会自动加入旧 Token。
- 已授权空间后来新增的挂载自动纳入该 Token。
- 每个空间独立设置 `viewer` 或 `editor`。
- `viewer` 强制拒绝写入、上传、改名、移动、删除、回收站变更和分享创建。
- `editor` 仍受 Token scopes、账号 ACL 和挂载读写模式限制。
- 管理员或空间 manager 移除账号后，该 Token 下一次请求立即失去该空间。

### 4.3 文件夹黑名单

黑名单记录定位为：

```text
token_id + space_id + mount_id + normalized_relative_path
```

原因是不同挂载中可能存在相同相对路径。

规则：

- 只允许选择已授权空间中的现存目录。
- 不允许 `.`、空路径或挂载根；若不允许整个挂载，应取消空间授权。
- 命中条件是请求路径等于黑名单路径，或位于 `blacklisted_path/` 之下。
- 目录列表不返回黑名单目录；直接请求返回 `folder_denied`。
- 搜索、元数据、文本读取、下载票据、上传、创建目录、改名、复制、移动、删除、回收站、分享和传输票据都必须执行同一拒绝规则。
- 源路径和目标路径分别校验；任一命中即拒绝跨路径操作。
- 黑名单只约束对应 Token 的 MCP 访问，不改变成员 Web 会话权限。

### 4.4 黑名单身份漂移

仅按字符串保存路径会被重命名绕过，因此保存时同时记录目录身份指纹。

- 通过 Omnora 执行的改名或移动，在同一协调流程中更新黑名单路径。
- 目录被外部 NAS 操作删除、替换或移走时，黑名单状态变为 `needs_review`。
- 存在 `needs_review` 记录时，该 Token 对对应挂载失败关闭，直到账号重新选择目录或删除规则。
- 旧路径仍保持拒绝，避免在用户修复前重新暴露。

## 5. 数据模型

新增迁移 `013_ai_token_space_grants.sql`：

```sql
ALTER TABLE ai_tokens
ADD COLUMN authorization_model TEXT NOT NULL DEFAULT 'legacy_boundary'
CHECK (authorization_model IN ('legacy_boundary', 'space_grant'));

CREATE TABLE ai_token_space_grants (
  token_id TEXT NOT NULL REFERENCES ai_tokens(id) ON DELETE CASCADE,
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  permission TEXT NOT NULL CHECK (permission IN ('viewer', 'editor')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (token_id, space_id)
);

CREATE UNIQUE INDEX mounts_id_space_unique ON mounts(id, space_id);

CREATE TABLE ai_token_folder_denies (
  token_id TEXT NOT NULL,
  space_id TEXT NOT NULL,
  mount_id TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  identity_fingerprint TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'needs_review')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (token_id, mount_id, relative_path),
  FOREIGN KEY (token_id, space_id)
    REFERENCES ai_token_space_grants(token_id, space_id) ON DELETE CASCADE,
  FOREIGN KEY (mount_id, space_id)
    REFERENCES mounts(id, space_id) ON DELETE CASCADE
);
```

现有 `ai_token_boundaries` 暂时保留，专门服务 `legacy_boundary` Token。不能自动把旧目录边界扩大为整空间权限。

## 6. API

### 6.1 创建

`POST /api/v1/ai-tokens`

```json
{
  "name": "Codex 工作凭据",
  "scopes": ["spaces:read", "files:list", "files:text", "files:write"],
  "spaceGrants": [
    {
      "spaceId": "space-personal",
      "permission": "editor",
      "folderDenylist": [
        {"mountId": "mount-personal", "path": "证件"}
      ]
    },
    {
      "spaceId": "space-team",
      "permission": "viewer",
      "folderDenylist": [
        {"mountId": "mount-team", "path": "财务"}
      ]
    }
  ]
}
```

新建 Token 固定写入 `authorization_model = 'space_grant'`。

### 6.2 编辑

`PATCH /api/v1/ai-tokens/{tokenId}` 原子替换以下字段：

- `name`
- `scopes`
- `expiresAt`
- `spaceGrants`

要求：

- 浏览器会话属于 Token 账号。
- 近期重新认证。
- 至少一个空间授权。
- Token 账号当前对每个空间具有相应权限。
- 黑名单目录存在、属于对应挂载且身份可验证。
- 更新成功后保留原 Bearer 密钥；下一次 MCP 请求立即生效。

### 6.3 列表

`GET /api/v1/ai-tokens` 返回 `authorizationModel`、`spaceGrants`、黑名单状态及可读空间/挂载名称。不得返回根目录绝对路径和目录身份指纹。

### 6.4 错误

- `token_space_not_allowed`：账号无该空间权限。
- `token_permission_exceeds_account`：Token 权限超过账号 ACL。
- `folder_denied`：目标位于黑名单目录树。
- `folder_denylist_invalid`：黑名单不是可验证目录。
- `folder_denylist_review_required`：黑名单身份漂移，挂载暂停访问。
- `legacy_token_edit_required`：旧 Token 必须在界面确认迁移后才能按新模型保存。

## 7. 授权链路

```text
POST /mcp
→ Bearer 验证并加载账号、scopes、authorization_model
→ MCP tool 构造可信 Principal
→ MemberFiles / MemberShares / TransferTicket
→ AccessGuard.Authorize
→ 校验账号空间 ACL
→ legacy_boundary 或 space_grant 分支
→ 校验空间 grant 与 viewer/editor
→ 校验 folder denylist
→ 校验挂载和路径身份
→ 执行操作
```

集合型操作需要额外过滤：

- `ListSpaces`：只返回显式授权空间。
- `ListMounts`：返回授权空间当前所有活动挂载。
- `ListDirectory`：过滤黑名单子目录。
- `Search`：搜索边界为授权空间全部挂载，再排除黑名单前缀。
- 分享列表：只返回 Token 可访问路径对应的分享。

## 8. 前端交互

AI Token 创建/编辑页以空间卡片为主：

```text
☑ 我的个人空间       [可编辑]
   排除文件夹：证件、私人备份        [管理]

☑ 研发共享空间       [仅查看]
   排除文件夹：财务                    [管理]

☐ 客户项目空间       [不允许]
```

- 默认权限为“仅查看”。
- 黑名单通过目录选择器添加，不允许手输宿主路径。
- Token 列表提供“编辑”和“撤销”。
- 旧 Token 显示“旧版目录边界”，进入编辑后要求用户选择整空间权限，再保存迁移。
- `needs_review` 黑名单在 Token 列表和编辑弹窗中显著提示。

## 9. 兼容与迁移

- 数据库升级不改变现有 Token 行为。
- `legacy_boundary` 继续使用旧 `ai_token_boundaries`。
- 新 Token 只使用 `space_grant`。
- 旧 Token 第一次编辑时，服务端不自动扩大权限；前端把原边界作为参考，用户必须显式选择空间、权限和黑名单。
- 转换事务成功后删除该 Token 的旧边界并改为 `space_grant`。
- OpenAPI、嵌入副本、MCP 文档与产品术语同步更新。

## 10. 安全约束

- 权限永远取交集，不允许 Token 提升账号能力。
- 所有 Token 更新写入审计日志，并与业务更新同事务提交。
- 黑名单采用失败关闭；身份或元数据不可用时不能推断为允许。
- 高风险永久删除仍要求逐次 MRTR 确认，空间 `editor` 不能绕过。
- 账号停用、Token 撤销、凭据代次变化、空间 ACL 删除和挂载不可用均立即生效。
- Web 会话和 MCP Token 使用同一底层文件服务，但 Token 特有的空间 grant 与黑名单只在 Principal 非空时执行。

## 11. 验收标准

1. 左侧可在个人空间和多个共享空间之间切换。
2. 新 Token 可对多个空间分别配置 viewer/editor。
3. 已授权空间新增挂载后，Token 自动可见；账号新加入空间不会自动授权。
4. 黑名单目录及全部子目录不会出现在列表和搜索中，所有直接操作均被拒绝。
5. viewer Token 不能写，editor Token 仍受账号 ACL、scope 和挂载模式限制。
6. Token 编辑后无需重新生成密钥，下一次请求使用新权限。
7. 旧 Token 权限不因迁移自动扩大。
8. 黑名单身份漂移时对应挂载失败关闭并可在 UI 修复。
9. REST 与 MCP 对同一 Token、空间和路径得出一致授权结果。

