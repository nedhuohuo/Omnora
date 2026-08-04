# Omnora / 万境

万境是面向个人、家庭和小团队的轻量自托管数字空间。它以 NAS 文件为核心，为成员、访客和 AI 提供统一、受控的文件访问能力。

> **项目状态：** 需求与架构设计已收敛；工程骨架与 OpenAPI / Docker Compose 脚手架已落地。首版业务闭环尚未完成，暂无正式发布版本。

## 特性

- **空间与挂载：** 用空间组织个人文件、共享文件和 NAS 已有目录；一个空间可包含多个挂载。
- **管理员控制面：** 仅系统管理员可添加或修改挂载；添加时明确只读 / 读写，并手动决定是否启用轻量元数据索引。
- **文件管理：** 账号、空间 ACL、文件浏览、大文件续传、托管挂载回收站与审计。
- **受控分享：** 支持文件与文件夹分享，可选密码、有效期、访问 / 下载次数限制和主动撤销。
- **浏览器预览：** 图片、PDF、文本、Markdown 和原生支持的音视频；Office 文件仅支持下载。
- **AI 与 API：** 版本化 REST API、OpenAPI 3.1 与 Streamable HTTP MCP；AI Token 可限定操作范围与目录边界。
- **部署入口：** Docker 端口发布或反向代理统一决定访问范围；应用内只维护路由组开关，不再提供额外 LAN / 代理入口配置。

### 首版非目标

不提供 Office 转换、视频转码、OCR、正文索引、向量检索、RAG、WebDAV、S3 / SMB 网关或多节点集群。

## 架构概览

默认部署只有一个应用容器：Go 进程提供 Web、API、MCP、传输和后台任务，前端静态资源嵌入可执行文件，SQLite WAL 保存业务数据与轻量元数据。文件内容始终保留在 NAS 文件系统。

| 项目 | 说明 |
| --- | --- |
| 首要验证平台 | 8 GB RAM 的极空间 NAS，同时兼容普通 Linux Docker Compose |
| 镜像架构 | `linux/amd64`、`linux/arm64` |
| 空闲内存目标 | 100 至 300 MB；普通浏览、搜索和传输目标不超过 500 MB |
| 外部依赖 | 不依赖 GPU、外部数据库、搜索集群、Office 转换器或转码服务 |
| 索引策略 | 按挂载开启，仅读取文件元数据；未索引挂载仍可逐目录浏览，但不进入全局搜索 |

以上均为首版验收目标，尚未经过可运行产品版本实测。

### 仓库结构

```text
Omnora/
├── cmd/omnora/          # 后端入口
├── internal/            # 业务与平台模块（identity、access、files、share 等）
├── web/                 # React + TypeScript + Vite 前端
├── openapi/             # OpenAPI 3.1 契约
├── deploy/              # Docker Compose 与测试环境示例
├── docs/                # 产品、设计、安全与验收文档
└── scripts/verification # 脚手架与发布就绪检查
```

## 快速开始

### 环境要求

- Go 1.26+
- Node.js 20+（仅本地前端开发需要；NAS 运行时不包含 Node.js）
- Docker Compose（可选，用于容器化部署验证）

### 后端

```bash
# 克隆仓库
git clone https://github.com/nedhuohuo/Omnora.git
cd Omnora

# 运行测试
go test ./...

# 本地启动（默认监听 127.0.0.1:8080）
export OMNORA_DB_PATH=./data/omnora.db
export OMNORA_INITIALIZATION_TOKEN=dev-init-token
go run ./cmd/omnora
```

### 前端

```bash
cd web
npm install
npm run dev
```

### Docker Compose

```bash
cd deploy
cp aliyun-test.env.example aliyun-test.env   # 按需修改密钥与令牌
# 基础单容器示例
docker compose -f docker-compose.yml config

# 阿里云测试覆盖（当前测试机对外暴露 8080，见部署文档）
docker compose --env-file aliyun-test.env \
  -f docker-compose.yml -f docker-compose.aliyun-test.yml up -d
```

正式镜像标签尚未发布；Compose 文件中的 `ghcr.io/omnora/omnora:0.1.0-dev` 为占位。阿里云测试服务器的边界与访问方式见 [阿里云测试服务器](docs/deployment/aliyun-test-server.md)（外部访问：`http://120.26.88.7:8080`）。

### 脚手架自检

```bash
./scripts/verification/verify-scaffolding.sh
```

## 文档

从 [文档地图](docs/README.md) 进入分层文档：

| 文档 | 内容 |
| --- | --- |
| [产品需求](docs/requirements/product-requirements.md) | 用户、场景、功能范围和非目标 |
| [领域模型](docs/design/domain-model.md) | 空间、挂载、权限、对象和生命周期 |
| [技术架构](docs/design/architecture.md) | 部署、模块、数据、任务和资源约束 |
| [安全模型](docs/security/security-model.md) | 信任边界、认证授权和安全基线 |
| [完整 Web 设计](docs/design/web-application-design.md) | 成员端、管理控制台、公开分享页 |
| [项目框架设计](docs/superpowers/specs/2026-08-02-omnora-project-foundation-design.md) | 工程骨架、模块依赖与技术探针 |
| [验收标准](docs/verification/acceptance-criteria.md) | 第一版发布门槛 |
| [OpenAPI](openapi/omnora.v1.yaml) | REST 契约草案 |

出现冲突时，以许可证、安全模型、领域模型、产品需求、技术架构、Web 设计、验收标准的顺序裁决，详见 [文档地图](docs/README.md)。

## 许可证与贡献

社区版本使用 GNU Affero General Public License v3.0 only（`AGPL-3.0-only`）。AGPL 允许个人和商业使用，但使用者必须遵守其条款。

项目计划未来提供独立商业许可，但目前尚未指定法律许可方，因此没有可购买或申请的商业许可证，也暂不接收外部版权贡献。

详见 [社区许可证](LICENSE)、[商业许可政策](COMMERCIAL-LICENSE.md)、[贡献政策](CONTRIBUTING.md)、[安全披露政策](SECURITY.md) 和 [项目声明](NOTICE)。
