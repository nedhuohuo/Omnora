# Omnora 文档

本目录是 Omnora 第一版需求与设计的权威来源。README 负责项目概览，许可证文件负责项目治理；具体业务、技术和安全规则只在下列对应文档中定义，其他文档通过链接引用，不复制规则正文。

## 文档地图

| 文档 | 读者 | 权威范围 |
| --- | --- | --- |
| [产品需求](requirements/product-requirements.md) | 产品、开发、测试 | 用户、场景、功能范围、非功能需求、非目标、成功标准 |
| [账号、挂载与内容授权设计](superpowers/specs/2026-08-08-account-mount-access-design.md) | 产品、开发、测试、安全 | 默认个人目录、独立挂载授权、目录协作、管理员身份和受限挂载方案 B |
| [领域模型](design/domain-model.md) | 产品、后端、前端、管理员 | 账号、个人目录、共用挂载、内容授权、协作、文件身份、Token、配额与删除语义 |
| [技术架构](design/architecture.md) | 开发、运维 | 进程、模块、SQLite、索引、任务、部署、资源与恢复 |
| [安全模型](security/security-model.md) | 开发、安全、运维 | 信任边界、认证授权、路径安全、网络暴露、凭证和备份恢复 |
| [验收标准](verification/acceptance-criteria.md) | 开发、测试、发布 | 功能、安全、性能、可靠性和发布门槛 |
| [REST API 指南](api/README.md) | REST 客户端、开发、测试 | REST 接入、认证、错误、常用流程和契约入口 |
| [MCP 指南](mcp/README.md) | AI 客户端、开发、测试、安全 | 当前 Streamable HTTP 契约、账号—挂载目标差异、MRTR、Transfer Ticket 和 Inspector 验收 |
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
| [NAS 验证证据](deployment/nas-verification.md) | 开发、测试、运维 | NAS / Linux Compose 发布候选验收记录与 `OMNORA_IMAGE` 固定要求 |
| [挂载插槽](deployment/mount-slots.md) | 运维、部署、管理员 | 9 插槽外部挂载的设计思路、配置方式、注册流程与使用边界 |
| [根 README 部署条件](../README.md#部署条件必读) | 运维、部署 | `OMNORA_PUBLIC_URL`、动态公网 IP、反代、重启与密钥等部署前置条件 |
| [安全披露](../SECURITY.md) | 安全研究者 | 漏洞报告渠道和响应范围 |
| [社区许可证](../LICENSE) | 使用者、分发者 | `AGPL-3.0-only` 许可证正文 |
| [商业许可政策](../COMMERCIAL-LICENSE.md) | 商业使用者 | 商业许可的当前状态与边界 |
| [贡献政策](../CONTRIBUTING.md) | 贡献者 | 当前贡献限制和未来 CLA 要求 |

## 决策优先级

先按“文档地图”中的权威范围判断冲突，不使用一份文档覆盖所有领域：

1. `LICENSE` 与适用的第三方许可证始终优先。
2. 涉及账号、个人目录、挂载授权、目录协作及其资源关系时，以 `superpowers/specs/2026-08-08-account-mount-access-design.md` 的已确认决策为准。
3. `security/security-model.md` 可以增加不改变上述资源关系的 fail-closed 安全约束；不得扩大管理员内容权或取消已确认能力。
4. 领域模型、产品需求、技术架构、Web 设计和验收标准分别在文档地图声明的范围内细化权威规格；REST/MCP 的目标机器契约必须在实现时由 OpenAPI、catalog 与协议测试共同冻结。
5. 明确标注为“迁移前当前实现”或“已被取代”的内容只用于调试或历史追溯，不能覆盖目标模型。

任何权威决策变化都必须同步更新受影响的需求、安全、实现约束和验收标准。不得通过实现细节或单独提高安全级别悄悄改变已确认的产品行为。

## 版本状态

当前业务模型为“唯一默认挂载中的个人目录 + 独立授权共用挂载 + 目录协作”，并采用受限挂载方案 B。仓库 Go、React、OpenAPI 和 MCP 已按该模型实现；OAuth 授权配置文件仍为 `NOT IMPLEMENTED`，资源与性能数值仍需按验收门槛验证。
