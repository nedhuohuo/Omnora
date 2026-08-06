# Omnora MCP

## 当前实现状态

当前仓库提供的是一个小型 HTTP JSON MCP 适配器，不应描述为已经完成标准兼容的 Streamable HTTP MCP transport。当前实现是：

- 入口：`POST /mcp`。
- 认证：`Authorization: Bearer <AI_TOKEN>`。
- 前置条件：MCP 路由组必须启用；默认部署配置保持关闭。
- 请求：`{"method":"...","params":{...}}`。
- 响应：普通 JSON；当前没有标准 MCP session、SSE/流式 envelope、`jsonrpc`/`id` 协商或通用 `tools/call` 层。

标准 MCP 兼容性、会话、Origin 校验和长连接属于[架构设计](../design/architecture.md)与[安全模型](../security/security-model.md)中的目标约束；它们不等同于当前 handler 已经全部实现。

## 启用和认证

部署时明确开启 MCP 路由组：

```dotenv
OMNORA_ROUTE_MCP_ENABLED=true
```

AI Token 必须有效、未过期、未撤销，并且具备对应 scope。每次工具调用都会重新受当前账号、空间 ACL、Token scope、目录边界、挂载模式和对象状态约束；建立连接或拿到 Token 不会固定未来权限。

示例地址中的 `OMNORA_BASE_URL` 不包含 `/api/v1`：

```bash
curl -fsS -X POST "$OMNORA_BASE_URL/mcp" \
  -H "Authorization: Bearer $OMNORA_BEARER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"method":"tools/list","params":{}}'
```

当前响应形状：

```json
{"tools":["spaces.list","files.search","files.list","files.metadata","files.read_text"]}
```

## 当前方法

| 方法 | 必需 scope | `params` | 当前行为 |
| --- | --- | --- | --- |
| `tools/list` | 无额外操作 scope，但仍需有效 AI Token | 可省略或 `{}` | 返回当前可用方法名称列表 |
| `spaces.list` | `spaces:read` | 无 | 返回 Token 配置的目录边界摘要 |
| `files.search` | `search:read` | `q: string` | 在已授权且已索引范围内搜索；没有目录边界时返回空结果 |
| `files.list` | `files:list` | `spaceId`, `mountId`, `path` | 列出授权挂载目录下的对象 |
| `files.metadata` | `files:metadata` | `spaceId`, `mountId`, `path` | 读取授权对象的元数据 |
| `files.read_text` | `files:text` | `spaceId`, `mountId`, `path`, 可选 `maxBytes` | 读取文本；默认最多 65536 bytes，硬上限 1048576 bytes |

### 路径参数

`spaceId` 和 `mountId` 必须属于当前 Token 能访问的空间与挂载；`path` 是挂载内相对路径。服务会规范化路径并拒绝越过 Token 目录边界的请求，也不会允许通过 MCP 访问 `.omnora/` 保留命名空间。

### 文本读取截断

`files.read_text` 的 `maxBytes` 大于 0 时可由调用方调小或调大，但服务端不会允许超过 1 MiB。响应包含 `path`、`content` 和 `truncated`；当内容超过上限时，`truncated` 为 `true`。

示例：

```bash
curl -fsS -X POST "$OMNORA_BASE_URL/mcp" \
  -H "Authorization: Bearer $OMNORA_BEARER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "method": "files.read_text",
    "params": {
      "spaceId": "space-id",
      "mountId": "mount-id",
      "path": "docs/readme.md",
      "maxBytes": 32768
    }
  }'
```

## 当前不支持的能力

当前 MCP handler 不提供：

- 上传、上传分片和上传完成提交。
- 下载票据或大型二进制内容内联返回。
- 删除、永久移动、恢复、覆盖既有对象或回收站操作。
- 创建/撤销公开分享和管理 ACL。
- 管理员用户、挂载、备份、索引任务或路由组操作。
- 标准 MCP session、资源、prompt、通知、流式传输和通用工具调用协商。

`uploads:create` 与 `files:download_ticket` 是 AI Token 的声明 scope，但当前 REST 资源 handler 仍要求会话 Cookie，当前 MCP 也没有暴露对应方法。若需要上传或下载，应使用 [REST API](../api/README.md) 中的流程，并按当前会话认证方式调用。

## 错误处理

认证或权限失败使用 JSON 错误 envelope：

```json
{
  "error": {
    "code": "forbidden",
    "message": "scope is not allowed",
    "request_id": "request-correlation-id"
  }
}
```

客户端应按 `error.code` 判断失败类型，并保留 `error.request_id`。未知方法返回 `not_found`；无效路径、越界路径和对象不存在分别按当前 HTTP handler 的状态码与错误码处理，不能把错误 message 当作稳定机器协议。

## 变更规则

新增或修改 MCP 方法时，必须同时检查：

1. `internal/server/api.go` 的实际方法分派和权限检查。
2. 对应 Go 测试，尤其是 Token scope、目录边界、撤销/过期和路由组关闭场景。
3. `openapi/omnora.v1.yaml` 的 `/mcp` envelope（如果 HTTP envelope 变化）。
4. 本文的方法表、示例和“不支持能力”列表。
5. [安全模型](../security/security-model.md)中关于 MCP 的统一授权规则。

本地定向检查：

```bash
scripts/verification/verify-api-docs.sh
go test ./internal/server -run 'MCP|RouteGroup'
```
