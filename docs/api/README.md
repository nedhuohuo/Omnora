# Omnora REST API

REST 的机器可读唯一手工契约是 [OpenAPI 3.1](../../openapi/omnora.v1.yaml)。运行时由
`GET /openapi` 或 `GET /openapi/omnora.v1.yaml` 提供同一份 YAML；嵌入副本由
`scripts/verification/sync-openapi-asset.sh` 生成，不能单独修改。

## 路由与三种认证

REST 基础路径是 `/api/v1`，健康检查 `GET /healthz`、`GET /readyz` 和 OpenAPI/MCP 入口
使用根路径。REST 路由组必须显式开启：

```dotenv
OMNORA_ROUTE_REST_ENABLED=true
```

| 凭证 | 放置位置 | 用途 | 失效条件 |
| --- | --- | --- | --- |
| 会话 Cookie | `omnora_session` | 浏览器和 `/api/v1` 资源 handler | 登出、轮换、账号禁用 |
| AI Token | `Authorization: Bearer <AI_TOKEN>` | `/mcp` Streamable HTTP | 撤销、过期、账号/ACL/scope/边界变化 |
| Transfer Ticket | `Authorization: Bearer <publicId.secret>` | `/mcp/transfers/*` 字节传输 | 短期过期、关闭、对象/挂载/权限变化 |

AI Token Bearer 不是受保护 REST 资源 Cookie 的替代品；受保护的 REST 资源 handler 当前要求会话 Cookie，公开分享/健康检查等入口按各自契约认证。
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
    "scopes":["spaces:read","files:list","files:metadata","files:text","files:download_ticket","search:read"],
    "boundaries":[{"spaceId":"space-id","mountId":"mount-id","path":"docs"}],
    "expiresAt":"2026-09-01T00:00:00Z"
  }' "$OMNORA_BASE_URL/api/v1/ai-tokens"
```

AI Token scope 的完整枚举为：
`spaces:read`、`files:list`、`files:metadata`、`files:text`、`files:download_ticket`、
`search:read`、`uploads:create`、`files:write`、`files:trash`、`trash:read`、
`files:restore`、`files:purge`、`shares:read`、`shares:create`、`shares:revoke`。
明文 secret 和 `bearerToken` 只在创建响应显示一次；成员只能管理自己的 Token，管理员只能
查看元数据和撤销。

## 常用 REST 流程

1. `GET /api/v1/spaces` 获取可见空间，随后列出挂载和目录。
2. 使用元数据、搜索和受限文本接口浏览内容；路径始终是挂载内相对路径，不接受宿主绝对路径。
3. 会话 Cookie 调用 `POST /api/v1/uploads`、`PUT /api/v1/uploads/{uploadId}/parts/{partNumber}`、
   `POST /api/v1/uploads/{uploadId}/complete` 和取消接口完成分片上传。
4. 分享创建、撤销和访客换票使用独立 share 路径；fragment 秘密不进入服务器日志。

写入、删除、回收站、跨挂载复制/移动和分享都按实时 ACL、挂载身份、只读模式、目录边界
和对象状态检查。高风险 MCP 工具还需要每次 MRTR 确认；REST 管理空间删除也有服务端确认。

外部挂载注册到预声明根的直接子路径（插槽）时，系统自动在该插槽下创建以空间 ID 命名的
子目录并注册为挂载，多个空间可共享同一插槽；插槽本身不注册。插槽只在其为独立 bind
mount（部署者已映射 NAS 文件夹）时出现在宿主目录建议中，未绑定的插槽不展示。

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
