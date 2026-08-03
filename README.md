# Omnora / 万境

Omnora is a lightweight, self-hosted digital space for files, people, and AI.

万境是面向个人、家庭和小团队的轻量自托管数字空间。它以 NAS 文件为核心，为成员、访客和 AI 提供统一、受控的文件访问能力。

> 项目状态：需求与架构设计阶段，暂无可运行版本。

## 第一版范围

- 通过空间组织个人文件、共享文件和 NAS 已有目录；一个空间可包含多个挂载。
- 只有系统管理员可以添加或修改挂载；添加时明确选择只读或读写，并手动决定是否加入轻量元数据索引。
- 提供账号、空间 ACL、文件管理、大文件续传、托管挂载回收站和审计。
- 支持文件与文件夹分享，可选密码、有效期、访问/下载次数限制和主动撤销。
- 在浏览器中预览图片、PDF、文本、Markdown 和原生支持的音视频；Office 文件仅下载。
- 提供版本化 REST API、OpenAPI 3.1 和 Streamable HTTP MCP，为 AI Token 设置操作范围与目录边界。
- 可同时启用受限的局域网 HTTP 入口和可信反向代理 HTTPS 入口；系统不自动判断请求是否来自公网。Lucky 可在公网终止 HTTPS 后转发到仅供代理使用的内网入口。

第一版不提供 Office 转换、视频转码、OCR、正文索引、向量检索、RAG、WebDAV、S3/SMB 网关或多节点集群。

## 轻量部署

默认部署只有一个应用容器：Go 进程提供 Web、API、MCP、传输和后台任务，前端静态资源嵌入可执行文件，SQLite WAL 保存业务数据与轻量元数据。文件内容始终保留在 NAS 文件系统。

- 首要验证平台：8 GB RAM 的极空间 NAS，同时兼容普通 Linux Docker Compose。
- 镜像架构：`linux/amd64`、`linux/arm64`。
- 空闲内存目标：100 至 300 MB；普通浏览、搜索和传输目标不超过 500 MB。
- 不依赖 GPU、外部数据库、搜索集群、Office 转换器或转码服务。
- 索引按挂载开启，仅读取文件元数据；未索引挂载仍可逐目录浏览，但不进入全局搜索。

以上均为首版验收目标，尚未经过可运行版本实测。

## 文档

从[文档地图](docs/README.md)进入分层文档：

- [产品需求](docs/requirements/product-requirements.md)：用户、场景、功能范围和非目标。
- [领域模型](docs/design/domain-model.md)：空间、挂载、权限、对象和生命周期。
- [技术架构](docs/design/architecture.md)：部署、模块、数据、任务和资源约束。
- [安全模型](docs/security/security-model.md)：信任边界、认证授权和安全基线。
- [验收标准](docs/verification/acceptance-criteria.md)：第一版发布门槛。

## 许可证与贡献

社区版本使用 GNU Affero General Public License v3.0 only（`AGPL-3.0-only`）。AGPL 允许个人和商业使用，但使用者必须遵守其条款。

项目计划未来提供独立商业许可，但目前尚未指定法律许可方，因此没有可购买或申请的商业许可证，也暂不接收外部版权贡献。

详见[社区许可证](LICENSE)、[商业许可政策](COMMERCIAL-LICENSE.md)、[贡献政策](CONTRIBUTING.md)、[安全披露政策](SECURITY.md)和[项目声明](NOTICE)。
