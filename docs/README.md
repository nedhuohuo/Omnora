# Omnora 文档

本目录是 Omnora 第一版需求与设计的权威来源。README 负责项目概览，许可证文件负责项目治理；具体业务、技术和安全规则只在下列对应文档中定义，其他文档通过链接引用，不复制规则正文。

## 文档地图

| 文档 | 读者 | 权威范围 |
| --- | --- | --- |
| [产品需求](requirements/product-requirements.md) | 产品、开发、测试 | 用户、场景、功能范围、非功能需求、非目标、成功标准 |
| [领域模型](design/domain-model.md) | 产品、后端、前端 | 空间、挂载、ACL、文件身份、分享、Token、配额与删除语义 |
| [技术架构](design/architecture.md) | 开发、运维 | 进程、模块、SQLite、索引、任务、部署、资源与恢复 |
| [安全模型](security/security-model.md) | 开发、安全、运维 | 信任边界、认证授权、路径安全、网络暴露、凭证和备份恢复 |
| [验收标准](verification/acceptance-criteria.md) | 开发、测试、发布 | 功能、安全、性能、可靠性和发布门槛 |
| [REST API 指南](api/README.md) | REST 客户端、开发、测试 | REST 接入、认证、错误、常用流程和契约入口 |
| [MCP 指南](mcp/README.md) | AI 客户端、开发、测试、安全 | 标准 Streamable HTTP、24 个工具、15 个 scope、MRTR、Transfer Ticket 和 Inspector 验收 |
| [OpenAPI 契约](../openapi/omnora.v1.yaml) | REST 客户端、代码生成、调试工具 | REST API 的机器可读唯一手工源 |
| [项目框架设计](superpowers/specs/2026-08-02-omnora-project-foundation-design.md) | 开发、测试、运维 | 工程骨架、模块依赖、技术探针、测试与 CI 门禁 |
| [完整 Web 设计](design/web-application-design.md) | 产品、设计、前端、测试 | 成员端、管理员控制台、公开分享页、响应式、状态和验收范围 |
| [P1 修复总体设计](superpowers/specs/2026-08-06-p1-remediation-design.md) | 开发、测试、安全、运维 | 当前全项目 Review 的 P1 修复边界、状态机、迁移、顺序和验收门禁 |
| [P1 身份与 HTTP 安全计划](superpowers/plans/2026-08-06-p1-identity-http-security.md) | 身份、安全、后端、Web | MFA、reauth、CSRF、可信代理、限流、审计和身份 OpenAPI |
| [P1 文件、上传与分享计划](superpowers/plans/2026-08-06-p1-files-uploads-shares.md) | 文件、存储、分享、Web | 安全 walker、挂载 claim、operation、上传、下载票据和跨挂载一致性 |
| [P1 恢复与部署计划](superpowers/plans/2026-08-06-p1-backup-recovery-deployment.md) | 运维、恢复、部署、发布 | 离线恢复、备份、路由配置、PUID/PGID/UMASK 和演练门禁 |
| [P1 Catalog、Jobs 与 API 契约计划](superpowers/plans/2026-08-06-p1-catalog-jobs-api-contracts.md) | Catalog、任务、API、前端 | DFS checkpoint、scan epoch、lease/fencing、RouteDefinition、OpenAPI 与生成类型 |
| [P2 Review 问题清单](reviews/2026-08-06-code-review-p2-findings.md) | 产品、开发、测试 | 已确认 P2 的证据、影响、修复方向、依赖和关闭条件 |
| [阿里云测试服务器](deployment/aliyun-test-server.md) | 开发、测试、运维 | 阿里云 ECS 测试服务器边界、外部 `8080` 访问方式和部署文件 |
| [安全披露](../SECURITY.md) | 安全研究者 | 漏洞报告渠道和响应范围 |
| [社区许可证](../LICENSE) | 使用者、分发者 | `AGPL-3.0-only` 许可证正文 |
| [商业许可政策](../COMMERCIAL-LICENSE.md) | 商业使用者 | 商业许可的当前状态与边界 |
| [贡献政策](../CONTRIBUTING.md) | 贡献者 | 当前贡献限制和未来 CLA 要求 |

## 决策优先级

出现冲突时按以下顺序处理：

1. `LICENSE` 与适用的第三方许可证。
2. `security/security-model.md` 中的安全边界。
3. `design/domain-model.md` 中的业务不变量。
4. `requirements/product-requirements.md` 中的产品行为。
5. `design/architecture.md` 中的实现约束。
6. `design/web-application-design.md` 中的 Web 信息架构、交互和前端验收范围。
7. `verification/acceptance-criteria.md` 中的验证方式。

若高优先级文档改变了用户可见行为，必须同步更新产品需求和验收标准。不得通过实现细节悄悄改变已确认的产品行为。

## 版本状态

当前文档描述 Omnora 第一版，状态为「设计已收敛，可运行闭环正在实现和验证」。仓库已包含 Go 后端、React 前端、OpenAPI 契约与 Docker Compose 示例；MCP Wire Protocol 已接入，Inspector/协议测试验收门槛在 Task16，尚未完成正式验收；OAuth 授权配置文件仍为 `NOT IMPLEMENTED`。尚无正式发布的产品版本，所有资源与性能数值仍为首版目标或验收门槛，不代表已经完整实测达成。
