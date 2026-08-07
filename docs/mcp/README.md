# Omnora MCP（标准 Streamable HTTP）

## 状态与兼容性

Omnora 的 MCP 服务使用官方 Go SDK 的无状态 Streamable HTTP transport，协议版本为
`2026-07-28`，入口是同源的 `GET/POST /mcp`（客户端按照 MCP 生命周期发送请求）。
协议线由 MCP Inspector 和仓库内的协议测试验收，不绑定某个具体 AI 客户端。

已测试的兼容范围包括 `2025-11-25` 客户端协商。OAuth 授权配置文件当前明确为
**NOT IMPLEMENTED**；服务只接受下文的 AI Token Bearer，不会把 OAuth 或 MCP session ID
当作认证凭证。

## 启用、端点和认证

部署时同时设置路由组和 MCP 安全边界：

```dotenv
OMNORA_ROUTE_MCP_ENABLED=true
OMNORA_MCP_ALLOWED_HOSTS=localhost:8080
OMNORA_MCP_ALLOWED_ORIGINS=http://localhost:8080
```

端点为 `${OMNORA_BASE_URL}/mcp`，其中 `OMNORA_BASE_URL` 不包含 `/api/v1`。每个请求都必须
带有一次性创建、可撤销和可过期的 AI Token：

```text
Authorization: Bearer <AI_TOKEN>
```

Host 必须精确匹配允许列表；Origin 缺省时允许，出现时必须精确匹配允许列表。MCP 路由组
关闭、Host/Origin 不通过、Token 撤销或账号停用都会让后续请求失败。

用 MCP Inspector（Modern / Streamable HTTP）连接：

```text
http://localhost:8080/mcp
Authorization: Bearer <AI_TOKEN>
```

Inspector 应选择 Modern transport，先完成 `initialize`，再通过 `tools/list` 和 `tools/call`
验证工具；不要向 `/mcp` 发送旧的 `{method,params}` 私有 envelope。

## 工具目录（24 个）

完整目录由 `internal/mcpapi/catalog.go` 运行时生成。scope 只决定工具是否出现在
`tools/list`，每次调用都会按工具需要重新检查账号、空间 ACL、目录边界和挂载状态；传输
和高风险对象操作还会校验对象指纹。

| 工具 | scope | 高风险确认 |
| --- | --- | --- |
| `spaces.list` | `spaces:read` | 否 |
| `mounts.list` | `spaces:read` | 否 |
| `files.list` | `files:list` | 否 |
| `files.metadata` | `files:metadata` | 否 |
| `files.search` | `search:read` | 否 |
| `files.read_text` | `files:text` | 否 |
| `files.prepare_download` | `files:download_ticket` | 否 |
| `directories.create` | `files:write` | 否 |
| `files.prepare_upload` | `uploads:create` | 否 |
| `uploads.status` | `uploads:create` | 否 |
| `uploads.complete` | `uploads:create` | 否 |
| `uploads.cancel` | `uploads:create` | 否 |
| `files.rename` | `files:write` | 否 |
| `files.copy` | `files:write` | 否 |
| `trash.list` | `trash:read` | 否 |
| `trash.restore` | `files:restore` | 否 |
| `shares.list` | `shares:read` | 否 |
| `files.move` | `files:write` | 是 |
| `files.trash` | `files:trash` | 是 |
| `trash.purge` | `files:purge` | 是 |
| `trash.empty` | `files:purge` | 是 |
| `files.delete_permanently` | `files:purge` | 是 |
| `shares.create` | `shares:create` | 是 |
| `shares.revoke` | `shares:revoke` | 是 |

### 15 个 scope

```text
spaces:read             files:list              files:metadata
files:text              files:download_ticket   search:read
uploads:create          files:write              files:trash
trash:read              files:restore           files:purge
shares:read             shares:create            shares:revoke
```

没有 scope 的工具不会出现在当前调用者的目录中。永久删除 scope (`files:purge`) 必须
单独授予；默认创建的 Token 可以只授予读取 scope。

## 高风险操作与 MRTR

以下七个工具使用 MCP form elicitation 的人机确认（MRTR）：
`files.move`、`files.trash`、`trash.purge`、`trash.empty`、
`files.delete_permanently`、`shares.create`、`shares.revoke`。

第一次调用只返回 `input_required`、影响摘要和 `requestState`，不执行文件或分享变更。
Inspector 或其他具备可靠 form Elicitation 能力的客户端提交 `confirmed: true` 后，服务端
会绑定原始参数、对象指纹、账号和 Token，并原子消费一次性挑战。拒绝、取消、过期、参数
变化、对象变化、Token 变化和并发重放均不执行操作。无法可靠确认的客户端不会看到高风险
工具；直接调用返回 `client_capability_required`。

## 文件传输

MCP 工具不内联大型二进制内容。`files.prepare_download` 和 `files.prepare_upload` 返回
短期 Transfer Ticket；票据秘密只出现在 `Authorization: Bearer <publicId.secret>` 请求头，
不接受 query、URL fragment 或日志中的凭证。

```text
GET /mcp/transfers/{publicId}
PUT /mcp/transfers/{publicId}/parts/{partNumber}
```

下载支持 `Range`、`Accept-Ranges`、`ETag`、`If-Match`、`If-None-Match`、`200`、`206`、
`304` 和 `416`。上传分片受票据剩余字节、会话 part size、声明大小和 part number 限制；
重复上传同一 part 只计正向大小差额。`uploads.complete` 或 `uploads.cancel` 会立即关闭
相关票据。

每次传输请求都重新验证 AI Token、ACL、scope、目录边界、挂载身份、挂载模式、对象指纹、
票据状态和过期时间。账号停用、Token 撤销、ACL 降级、边界变化或挂载漂移会立即使票据
失效。

## 稳定错误与审计

业务错误使用 `isError: true` 和稳定 `code`（例如 `forbidden`、`boundary_violation`、
`readonly_mount`、`mount_identity_unverifiable`、`not_found`、`conflict`、`ticket_expired`、
`upload_conflict`、`confirmation_stale`）。`requestId` 用于关联日志；协议解析、未知方法
和内部错误仍由 JSON-RPC 层返回，不能把 message 当作稳定协议。

MCP intent/outcome 审计只保存 credential public ID、工具、规范化目标标签、request/trace ID、
结果和字节/分片摘要，绝不保存 AI Token、Transfer Ticket、MRTR requestState 秘密、文件内容、
checksum 或宿主机路径。终态审计失败会标记 `mcp_audit_degraded`，并使 `/readyz` 返回 `503`。

## 路由组

MCP、REST、OpenAPI、成员 Web、管理员 Web 和公开分享是独立 route group。关闭 MCP 后，
`/mcp` 与 `/mcp/transfers/*` 下一请求立即拒绝；启用 REST 或 OpenAPI 不会隐式启用 MCP。

## 修改与验收

修改工具、scope、错误或传输路径时，必须同步 `catalog.go`、本文、OpenAPI、
`internal/mcpapi/docs_contract_test.go` 和协议验收脚本：

```bash
scripts/verification/verify-api-docs.sh
GOCACHE=/private/tmp/omnora-go-cache GOMODCACHE=/private/tmp/omnora-go-modcache \
  go test ./internal/mcpapi ./internal/server -run 'MCP|Transfer' -count=1
```
