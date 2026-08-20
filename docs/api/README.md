# Omnora REST API

状态：账号—挂载模型的现行 REST 与 MCP 认证契约

REST 的机器可读唯一手工契约是 [OpenAPI 3.1](../../openapi/omnora.v1.yaml)。运行时由
`GET /openapi` 或 `GET /openapi/omnora.v1.yaml` 提供同一份 YAML；嵌入副本由
`scripts/verification/sync-openapi-asset.sh` 生成，不能单独修改。

## 账号—挂载契约

现行模型直接以账号、个人目录、共用挂载和协作为业务资源：

- 普通共用挂载使用 `mountId + relativePath`；
- “我的文件”使用当前账号绑定的虚拟个人根，客户端不能指定其他账号；
- 收到的目录协作使用 `collaborationId + relativePath`；
- AI Token 可选择动态覆盖当前账号个人目录和全部获权共用挂载，但收到的协作不能进入 Token boundary；
- 文件访问、挂载发现和 Token scope 均直接基于账号与挂载授权；
- `mounts.list` 只返回当前账号获权的共用挂载；
- 受限挂载管理路由只对初始管理员可发现，普通管理员直接请求返回不泄露存在性的 `404`。
- 普通管理员创建或重命名普通挂载时，候选资源与隐藏挂载或其他不可用条件冲突统一返回 `mount_unavailable`，不得返回冲突对象信息，并执行限速与审计；该主动占用探测是明确记录的残余侧信道。
- 解除挂载要求精确名称确认，只删控制面且允许同路径以新挂载 ID 重新注册；旧授权不得恢复。

REST 使用三个不可混用的资源族：账号作用域“我的文件”、`mountId` 作用域共用挂载、`collaborationId` 作用域收到的协作。客户端不得用可选 `mountId`、默认挂载 ID 或账号 ID 推断资源类型；个人资源永远绑定当前账号，协作资源每次重验接收账号和协作根。管理接口按挂载维护 `mount_grants`，只接受 `viewer/editor`，禁止为默认个人挂载创建授权；普通挂载由任一管理员治理，受限挂载仅初始管理员可发现和治理。

允许 AI Token Bearer 的文件 REST 路由必须在 OpenAPI 中逐条白名单化，并复用与 MCP 相同的 scope 和资源边界；AI Token 不得进入账号、会话或管理员控制面。完整决策见[账号、挂载与内容授权设计](../superpowers/specs/2026-08-08-account-mount-access-design.md)。

## 管理端挂载治理

管理员挂载接口使用账号—挂载模型，不接受 `spaceId`、`space_id` 或旧 `kind` 字段：

- `GET /api/v1/admin/mounts`：列出当前管理员可治理的共用挂载；默认个人挂载不返回；
- `POST /api/v1/admin/mounts`：注册已映射的外部目录，允许零条初始授权；创建者不会自动获得内容权限；
- `PATCH /api/v1/admin/mounts/{mountId}`：修改显示名称、只读/读写、索引和公开分享开关；根路径与治理类型不可修改；
- `GET/PUT/DELETE /api/v1/admin/mounts/{mountId}/grants/{accountId}`：维护 `viewer/editor` 授权；
- `DELETE /api/v1/admin/mounts/{mountId}`：请求体必须提交当前 `displayName` 精确确认。成功返回 `dataDeleted: false`，只清理 Omnora 控制面和派生能力，不删除 NAS 真实文件；
- `POST /api/v1/admin/mounts/{mountId}/reverify`：重新验证挂载身份；
- `GET /api/v1/admin/host-directories`：只返回部署映射的外部根和已绑定插槽；
- `GET/POST /api/v1/admin/index-jobs`：只针对可治理的共用挂载，响应只含 `mountName`。

普通管理员不能发现受限挂载；直接管理请求和相关索引请求统一表现为 `404`。候选路径或名称与隐藏对象冲突时返回不泄露对象信息的 `mount_unavailable`。

## 管理端版本更新

管理员版本更新使用会话 Cookie，并要求 CSRF、近期重新认证和管理员角色：

- `GET /api/v1/admin/updates`：读取当前、待重启和失败状态；
- `POST /api/v1/admin/updates`：以 `multipart/form-data` 的 `package` 字段上传 `.tar.gz` 发布包，成功返回 `202` 后服务受控重启；
- `POST /api/v1/admin/updates/rollback`：排队恢复上一发布版本，成功返回 `202` 后服务受控重启。

发布包只允许 `manifest.json`、`omnora` 和 `omnora-recovery`，并校验目标平台、大小和 SHA-256。更新文件保存在 `/var/lib/omnora/updates`，不会替换数据库、用户文件或只读镜像层。完整部署约束见[Web Self-Update](../deployment/self-update.md)。

## 路由与三种认证

REST 基础路径是 `/api/v1`，健康检查 `GET /healthz`、`GET /readyz` 和 OpenAPI/MCP 入口
使用根路径。REST 路由组必须显式开启：

```dotenv
OMNORA_ROUTE_REST_ENABLED=true
```

| 凭证 | 放置位置 | 用途 | 失效条件 |
| --- | --- | --- | --- |
| 会话 Cookie | `omnora_session` | 浏览器和 `/api/v1` 资源 handler | 登出、轮换、账号禁用 |
| AI Token | `Authorization: Bearer <AI_TOKEN>` | `/mcp` Streamable HTTP | 撤销、过期、账号/挂载授权/scope/边界变化 |
| Transfer Ticket | `Authorization: Bearer <publicId.secret>` | `/mcp/transfers/*` 字节传输 | 短期过期、关闭、对象/挂载/权限变化 |

AI Token Bearer 不是受保护 REST 资源 Cookie 的替代品；REST 资源 handler 当前要求会话 Cookie，公开分享/健康检查等入口按各自契约认证。
Transfer Ticket 只允许传输路由使用，秘密禁止放在 query、fragment、日志或 Referer 中。

## 会话和 AI Token

```bash
curl -i -c /tmp/omnora.cookies \
  -H 'Content-Type: application/json' \
  -d '{"login":"member@example.com","password":"replace-me","totpCode":"123456"}' \
  "$OMNORA_BASE_URL/api/v1/auth/session"

curl -fsS -b /tmp/omnora.cookies \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"mcp-reader",
    "scopes":["mounts:read","files:list","files:metadata","files:text","files:download_ticket","search:read"],
    "boundaries":[{"source":"all_account_content"}],
    "expiresAt":"2026-09-01T00:00:00Z"
  }' "$OMNORA_BASE_URL/api/v1/ai-tokens"
```

AI Token scope 的完整枚举为：
`mounts:read`、`files:list`、`files:metadata`、`files:text`、`files:download_ticket`、
`search:read`、`uploads:create`、`files:write`、`files:trash`、`trash:read`、
`files:restore`、`files:purge`、`shares:read`、`shares:create`、`shares:revoke`。
Token boundary 的 `source` 只允许 `all_account_content`、`personal`、`common_mount`：
`all_account_content` 动态覆盖当前账号个人目录和全部实时获权共用挂载，`personal` 可限制个人根内
相对路径，`common_mount` 必须携带获权的 `mountId` 且可限制相对路径。收到的目录协作永远不能
加入 AI Token boundary，也不能通过 MCP 访问或搜索。
明文 secret 和 `bearerToken` 只在创建响应显示一次；成员只能管理自己的 Token，管理员只能
查看元数据和撤销。

## 常用 REST 流程

1. `GET /api/v1/member/content-sources` 获取“我的文件”和实时获权的共用挂载，随后按显式内容源列出目录。
2. 使用元数据、搜索和受限文本接口浏览内容；路径始终是挂载内相对路径，不接受宿主绝对路径。
3. 会话 Cookie 调用 `POST /api/v1/uploads`、`PUT /api/v1/uploads/{uploadId}/parts/{partNumber}`、
   `POST /api/v1/uploads/{uploadId}/complete` 和取消接口完成分片上传。
4. 分享创建、撤销和访客换票使用独立 share 路径；fragment 秘密不进入服务器日志。

写入、删除、回收站、跨挂载复制/移动和分享按实时内容授权、挂载身份、只读模式、目录边界
和对象状态检查。高风险 MCP 工具还需要每次 MRTR 确认；REST 解除挂载也要求精确名称确认。

挂载注册直接把管理员选中的外部目录登记为挂载根，不创建任何隐式业务子目录。插槽只在其为独立 bind mount 时出现在宿主目录建议中，未绑定插槽不展示。

## 错误、分页和传输

JSON 错误使用：

```json
{"error":{"code":"forbidden","message":"permission denied","request_id":"request-correlation-id"}}
```

按 `error.code` 和 HTTP 状态判断，保留 `request_id`；不要依赖 message。常见 code 包括
`unauthorized`、`forbidden`、`route_group_disabled`、`not_found`、`invalid_input`、
`readonly_mount`、`mount_conflict`、`mount_identity_unverifiable`、`mount_not_writable`、
`upload_conflict`、`confirmation_required` 和 `mcp_audit_degraded`。列表使用 `limit`/`cursor`，当前 limit 范围
为 1–200。

MCP 大文件不通过 MCP JSON 内联返回，而是由 `files.prepare_download` 或
`files.prepare_upload` 发放 Transfer Ticket。精确的 Range、ETag、状态码和响应头见
OpenAPI 中的 `/mcp/transfers/{publicId}` 与 `/mcp/transfers/{publicId}/parts/{partNumber}`；
这两个路径使用 `ticketBearer`，不是 Cookie 或 AI Token 方案。

## OpenAPI 与验证

OpenAPI 只描述 REST 和可建模的 HTTP 传输边界；`/mcp` 是标准 MCP transport，不再描述旧的
`{method,params}` 私有 envelope，也不重复 24 个 Tool 的 JSON schema。工具、scope、MRTR 和
协议协商以 [MCP 指南](../mcp/README.md) 与 Inspector/协议测试为准。

```bash
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
```
