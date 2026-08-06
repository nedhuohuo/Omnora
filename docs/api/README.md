# Omnora REST API

本文档是 REST API 的使用入口。字段、路径参数、请求体和响应 schema 以 [OpenAPI 3.1 契约](../../openapi/omnora.v1.yaml) 为准；本文只说明接入方式、调用顺序和容易踩到的边界。

## 契约与入口

- 机器可读的唯一手工维护源：[`openapi/omnora.v1.yaml`](../../openapi/omnora.v1.yaml)。
- 服务运行时的 YAML：`GET /openapi` 或 `GET /openapi/omnora.v1.yaml`。
- REST 基础路径：`/api/v1`。
- 健康检查不属于 REST 路由组：`GET /healthz`、`GET /readyz`。
- OpenAPI 文档路由和 REST 路由是两个独立的路由组。启用 OpenAPI 不会自动启用 REST，反之亦然。

运行时嵌入文件 [`internal/server/openapi_assets/omnora.v1.yaml`](../../internal/server/openapi_assets/omnora.v1.yaml) 由 [`sync-openapi-asset.sh`](../../scripts/verification/sync-openapi-asset.sh) 从根契约生成，不应单独手工修改。

## 前置条件

REST 必须在部署配置中启用：

```dotenv
OMNORA_ROUTE_REST_ENABLED=true
```

远程访问应通过 HTTPS 反向代理提供。不要把 AI Token、密码、TOTP 秘密或分享片段放入 URL、查询参数、日志或 `Referer`。

## 认证

Omnora 当前提供两种 REST 认证方式：

| 方式 | 获取方式 | 适用场景 |
| --- | --- | --- |
| 浏览器会话 Cookie | `POST /api/v1/auth/session` 成功后由服务设置 `omnora_session` | Web 或需要登录态的人工调用 |
| Bearer AI Token | 成员通过 `POST /api/v1/ai-tokens` 创建，明文只在创建响应中返回一次 | 当前用于 `/mcp`；REST Bearer 资源访问尚未接入 |

当前 REST 资源 handler（空间、文件、搜索、上传等）要求 `omnora_session` Cookie；只有 `/mcp` 当前调用 AI Token Bearer 校验。AI Token 的权限模型仍以当前账号状态、空间 ACL、Token scope、目录边界、挂载模式和对象状态的交集为目标约束，具体安全规则以[安全模型](../security/security-model.md)为准。

### 健康检查

```bash
curl -fsS "$OMNORA_BASE_URL/healthz"
curl -fsS "$OMNORA_BASE_URL/readyz"
```

其中 `OMNORA_BASE_URL` 不包含 `/api/v1`，例如 `https://omnora.example.com`。

### 创建浏览器会话

```bash
curl -i -c /tmp/omnora.cookies \
  -H 'Content-Type: application/json' \
  -d '{"login":"member@example.com","password":"replace-me","totpCode":"123456"}' \
  "$OMNORA_BASE_URL/api/v1/auth/session"
```

如果账号尚未启用 TOTP 或当前登录流程不要求验证码，可以按 OpenAPI schema 省略 `totpCode`。不要把真实密码写入脚本仓库或 shell 历史。

使用会话访问空间：

```bash
curl -fsS -b /tmp/omnora.cookies \
  "$OMNORA_BASE_URL/api/v1/spaces"
```

### 创建 AI Token（当前用于 MCP）

创建响应中的 `bearerToken` 当前用于 [MCP HTTP 适配器](../mcp/README.md)，不能直接替代 REST 资源接口所需的会话 Cookie。

创建 Token 时必须显式指定有效期、scope 和挂载目录边界：

```bash
curl -fsS -b /tmp/omnora.cookies \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "local-reader",
    "scopes": ["spaces:read", "files:list", "files:metadata", "files:text", "search:read"],
    "boundaries": [{"spaceId":"space-id","mountId":"mount-id","path":"docs"}],
    "expiresAt": "2026-09-01T00:00:00Z"
  }' \
  "$OMNORA_BASE_URL/api/v1/ai-tokens"
```

创建响应中的 `bearerToken` 只应安全保存；撤销使用 `DELETE /api/v1/ai-tokens/{tokenId}`。Token 默认只读。`uploads:create` scope 会在 Token 创建时校验成员编辑权限和挂载可写条件，但当前 REST 上传 handler 仍要求会话 Cookie；这部分是 REST Bearer 接入的后续契约，不应提前当作已完成能力。

## 响应约定

### 成功响应

成功响应的具体 JSON 结构以 OpenAPI 为准。列表接口通常返回 `items`，可分页接口还会返回 `nextCursor`。

常见分页参数：

- `limit`：请求返回数量，当前契约允许范围是 1 到 200。
- `cursor`：使用上一页响应中的 `nextCursor`，不要自行拼接或解码。

### 错误响应

服务运行时返回 `application/json`，结构固定为：

```json
{
  "error": {
    "code": "forbidden",
    "message": "scope is not allowed",
    "request_id": "request-correlation-id"
  }
}
```

客户端应按 `error.code` 做机器判断，把 `error.message` 作为诊断信息，并在工单或日志关联时保留 `error.request_id`。常见错误包括 `unauthorized`、`forbidden`、`route_group_disabled`、`not_found`、`invalid_input`、`readonly_mount`、`mount_identity_unverifiable` 和 `upload_conflict`；完整路径级响应仍以 OpenAPI 为准。

## 常用业务流程

### 浏览、搜索与读取

1. `GET /api/v1/spaces` 获取当前主体可见空间。
2. `GET /api/v1/spaces/{spaceId}/mounts` 获取挂载。
3. `GET /api/v1/spaces/{spaceId}/mounts/{mountId}/children` 按目录浏览。
4. `GET /api/v1/spaces/{spaceId}/search` 搜索已启用索引的授权范围。
5. 按对象需要使用文件元数据、文本读取、下载票据或受限下载接口。

路径参数是挂载内的规范化相对路径，不是宿主机绝对路径；调用方不能通过 API 提交任意宿主路径，也不能访问 `.omnora/` 保留命名空间。

### 分片上传

REST 上传当前采用会话 Cookie 认证的会话式流程：

1. `POST /api/v1/uploads` 创建上传会话。
2. `PUT /api/v1/uploads/{uploadId}/parts/{partNumber}` 上传每个分片。
3. `POST /api/v1/uploads/{uploadId}/complete` 提交完成。
4. 失败或取消时使用 `DELETE /api/v1/uploads/{uploadId}`。

创建阶段、每个分片和最终提交阶段都会重新检查权限、挂载模式、配额和会话状态。不要把上传能力理解成覆盖既有文件的通用权限；首版 AI Token 不开放覆盖既有对象。

### 分享

分享创建、撤销和分享会话交换属于独立的 REST/Share 能力。分享 URL 的秘密只应通过受保护的片段和一次性交换流程处理，具体边界见 OpenAPI 的 `shares` 与 `share-sessions` 定义以及[安全模型](../security/security-model.md)。

## 变更与验证

新增或修改 REST 路径时，至少同步：

1. `openapi/omnora.v1.yaml`。
2. 对应后端路由与测试。
3. 本文档中的流程或边界说明（仅在行为变化时更新，不复制 schema）。
4. OpenAPI 嵌入副本和校验门禁。

本地定向检查：

```bash
scripts/verification/sync-openapi-asset.sh --check
scripts/verification/verify-api-docs.sh
```
