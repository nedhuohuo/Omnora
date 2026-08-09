# MCP Inspector / 协议验收清单

> **现行契约：** 账号—挂载模型，24 tools、15 scopes、8 个逐操作 MRTR 工具；不兼容旧 Space MCP。

本清单只面向 MCP Inspector v2.1.0 Modern（Streamable HTTP）和仓库内协议测试，
不绑定任何桌面或第三方客户端。现场验收应保存脱敏后的请求摘要、响应状态和工具目录，
不得保存 AI Token、Transfer Ticket、确认 requestState 或分享 fragment。

在线门禁使用本清单的单独证据副本，而不是直接把仓库模板视为已完成。证据副本须包含：

```text
Status: COMPLETE
Redacted evidence: <脱敏后的 Inspector/protocol 摘要>
```

证据副本必须勾选本模板的全部 22 个验收项，不能保留未勾选的 `- [ ]` 项，也不能把本模板文件本身作为完成证据。

## 连接与协商

- [ ] MCP 路由组已启用，端点为 `/mcp`，Host/Origin allowlist 与反向代理配置匹配。
- [ ] Inspector 明确选择 Modern / Streamable HTTP，未静默降级到 legacy transport。
- [ ] `initialize` 成功，协商协议版本为 `2026-07-28`。
- [ ] 使用短期、最小 scope 的 AI Token；OAuth 授权配置显示为 **NOT IMPLEMENTED**。
- [ ] `tools/list` 只返回该 Token 当前 scope 且属于 24-tool contract 的工具；全 scope Token
      恰好返回 24 个工具，包含 `files.update` 且不含旧 Space 工具；`mounts.list` 的 input schema
      不接受任何字段。
- [ ] 没有可靠 form Elicitation 能力时，8 个高风险工具不会出现在目录中。

## 代表性操作

- [ ] `files.list` 或 `files.metadata` 分别使用 `{source: "personal", path}` 和
      `{source: "common_mount", mountId, path}` 成功，返回内容不含宿主机路径；未授权挂载不可发现。
- [ ] `files.read_text` 成功，返回 typed structured content，且正文受大小上限约束。
- [ ] `files.prepare_download` 返回短期 Transfer Ticket；通过 ticket HTTP GET 验证 `Range`、`ETag`、
      `If-Match`、`If-None-Match` 和 416 边界。
- [ ] `files.prepare_upload` 返回短期上传 ticket；通过 PUT 分片验证有界大小、断点续传、重复 part
      只计正向大小差额，以及 `uploads.complete`/`uploads.cancel` 后票据立即失效。

## 每次确认与拒绝

- [ ] 逐项触发 `files.move`、`files.trash`、`trash.purge`、`trash.empty`、
      `files.delete_permanently`、`shares.create`、`shares.revoke`、`files.update`，每次都出现 form MRTR 确认。
- [ ] 接受确认后 SDK/Inspector 完成 `input_required → elicitation → retry`，原始参数和对象指纹保持绑定。
- [ ] 拒绝或取消不产生文件、回收站或分享状态变化。
- [ ] 过期、重复提交、参数变化、对象变化、Token 撤销和并发重放均失败且不执行第二次变更。
- [ ] 现场记录只保留稳定错误 code、request ID 和结果，不保留 requestState 或秘密。

## 负例与安全回归

- [ ] 协议测试覆盖 `2025-11-25` 协商、parse error、invalid request、method not found、invalid params、
      malformed protocol headers、body limit、Origin/Host 拒绝、路由关闭和 stateless GET/DELETE 405。
- [ ] Token boundary 只接受 `all_account_content`、`personal`、`common_mount`；收到的目录协作不能作为
      locator、boundary、MCP 搜索或 Transfer Ticket 来源；Token 撤销、账号停用、挂载授权降级、
      只读挂载和挂载 identity 漂移会使后续 MCP/transfer 请求立即失败。
- [ ] REST 与 MCP 代表性读写/分享操作使用同一授权结果和应用服务。
- [ ] `/mcp/transfers/*` 只接受 Authorization header，不接受 query、fragment 或 Referer 中的 ticket secret。

## 证据与运行注意

- [ ] 已运行 `scripts/verification/verify-mcp-protocol.sh` 并保存退出状态和脱敏摘要。
- [ ] 需要在线验收时已运行 `scripts/verification/verify-mcp-inspector.sh`，并设置
      `OMNORA_MCP_URL` 与短期 `OMNORA_MCP_AI_TOKEN`。
- [ ] Inspector CLI 运行期间，短期 Token 会出现在本机进程表中；因此该脚本是操作员验收检查，
      不是常驻共享 runner 门禁。验收结束后立即撤销 Token。
