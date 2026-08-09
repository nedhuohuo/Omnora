# Omnora 项目框架设计

> **资源模型说明：** 本文只保留工程框架与技术探针的历史依据；其中账号、Space、挂载和 ACL 示例描述迁移前模型，不得作为目标业务契约。目标资源关系以[账号、挂载与内容授权设计](2026-08-08-account-mount-access-design.md)及[文档地图](../../README.md)为准。

日期：2026-08-02
状态：已确认，待实施计划

## 1. 目的与范围

本文定义 Omnora 第一阶段的工程框架。该阶段交付可构建、可测试、可部署的项目骨架，并通过 SQLite、Linux 安全文件解析和 Lucky/MCP 三项技术探针消除关键技术风险。

本阶段不实现账号、空间、挂载、分享等业务闭环，不创建返回虚假成功结果的占位业务接口。产品范围、领域语义、安全边界和发布门槛继续分别以[产品需求](../../requirements/product-requirements.md)、[领域模型](../../design/domain-model.md)、[技术架构](../../design/architecture.md)、[安全模型](../../security/security-model.md)和[验收标准](../../verification/acceptance-criteria.md)为准。

## 2. 方案选择

### 2.1 技术基线

采用以下技术基线：

- 后端：Go、标准库 `net/http`、`chi`。
- API：OpenAPI-first，使用 `oapi-codegen` 生成 Go 请求类型和服务器接口，使用 `openapi-typescript` 与 `openapi-fetch` 生成并承载 TypeScript 客户端。
- MCP：使用官方 `github.com/modelcontextprotocol/go-sdk`，并由代理探针验证 Streamable HTTP、会话、Origin、取消和长连接行为。
- 数据：`database/sql`、`sqlc`、SQLite WAL 和内嵌迁移。
- 前端：React、TypeScript、Vite、React Router 和 TanStack Query。
- 日志：标准库 `slog`，生产环境输出结构化 JSON。
- 部署：单 Go 进程、单运行容器，发布 `linux/amd64` 和 `linux/arm64`。

前端静态资源通过 `go:embed` 进入 Go 可执行文件。NAS 运行时不包含 Node.js，不引入 PostgreSQL、Redis、OpenSearch、独立 Worker、Office 转换器或视频转码服务。

### 2.2 代码组织方案

采用按业务能力划分的模块化单体。只在数据库、文件系统、HTTP 和时钟等真实外部边界定义接口，不机械套用严格 Clean Architecture。

未采用以下方案：

- 严格六边形架构：隔离更强，但首版会产生大量端口、DTO 和转换代码。
- 全局 handlers/services/repositories 技术分层：起步较快，但统一授权、文件安全解析和多协议复用容易逐渐耦合。
- Rust 或 Node.js 单体：Rust 的交付与交叉构建成本偏高；Node.js 缺少适合本项目安全边界的一等 fd-relative Linux 文件 API。

## 3. 仓库结构

```text
omnora/
├── cmd/
│   └── omnora/                 # 唯一生产入口，只负责组装与生命周期
├── api/
│   └── openapi/
│       └── v1.yaml             # REST API 唯一契约源
├── internal/
│   ├── app/                    # 启动、关闭、依赖装配
│   ├── identity/               # 账号、会话、TOTP、Token
│   ├── accesspolicy/           # 唯一授权裁决入口
│   ├── spaces/                 # 空间、成员和 ACL
│   ├── storage/                # 挂载、文件身份、安全解析
│   ├── catalog/                # 元数据、扫描、搜索
│   ├── transfer/               # 上传、下载、Range、票据
│   ├── share/                  # 分享生命周期
│   ├── preview/                # 轻量预览策略
│   ├── jobs/                   # 持久化单并发任务
│   ├── audit/                  # 审计事件
│   ├── transport/
│   │   ├── httpapi/            # Web、REST、分享及入口规则
│   │   └── mcp/                # MCP 协议适配
│   └── platform/               # SQLite、配置、日志、时间、随机数
├── migrations/                 # 内嵌 SQLite 迁移
├── web/                        # React + TypeScript + Vite
├── tests/
│   ├── integration/            # 跨模块和数据库测试
│   ├── filesystem/             # Linux 文件边界测试
│   └── e2e/                    # 浏览器与代理验收
├── tools/
│   └── probes/
│       ├── sqlite/             # WAL、百万数据、备份探针
│       ├── storage/            # openat2、statx、bind 探针
│       └── proxy/              # Lucky、Range、MCP 长连接探针
├── deploy/
│   ├── compose/                # 通用 Linux 与极空间模板
│   └── lucky/                  # Lucky 配置说明和验收样例
└── Dockerfile                  # 前端构建 + Go 构建 + 精简运行镜像
```

生产入口不能包含业务判断。技术探针的命令和测试夹具不会进入生产请求链；Storage 探针必须直接调用 `internal/storage/resolver` 的最小生产实现，不能另写一套临时解析器。探针验证通过后，安全解析器保留为生产包，攻击用例和兼容性用例迁入正式测试目录。

## 4. 模块职责

业务模块采用以下精简结构，根据实际需要省略没有内容的文件：

```text
internal/<module>/
├── model.go          # 本模块领域类型
├── service.go        # 用例与业务编排
├── ports.go          # 确实存在外部边界时定义接口
├── errors.go         # 稳定领域错误
└── sqlite/           # 本模块的 SQL、sqlc 查询和仓储实现
```

| 模块 | 负责 | 明确不负责 |
| --- | --- | --- |
| `identity` | 账号、密码、TOTP、会话、Token | 空间 ACL、文件权限 |
| `accesspolicy` | 汇总账号、ACL、scope、目录边界和挂载模式，作出唯一授权决定 | 认证、文件读写 |
| `spaces` | 个人/共享空间、成员关系、ACL | 挂载身份和文件内容 |
| `storage` | 挂载控制面、根身份、安全路径解析、实时文件操作 | 搜索索引、HTTP 响应 |
| `catalog` | 文件元数据、扫描、对账、搜索 | 将索引当成授权或文件真相 |
| `transfer` | 分片上传、Range 下载、ETag、短期票据 | 自行绕过授权读取路径 |
| `share` | 分享策略、密码、次数预占、撤销和派生会话 | 通用账号会话 |
| `preview` | 类型判定和受限内容输出 | Office 转换、视频转码 |
| `jobs` | 持久任务、优先级、检查点、重试和协作式让出 | 承载业务规则 |
| `audit` | 脱敏、只追加事件、查询和保留 | 保存正文或凭证明文 |
| `transport/httpapi` | 协议解析、中间件、状态码、流式响应 | 业务授权判断 |
| `transport/mcp` | MCP schema、会话和工具映射 | 直接访问数据库或 Storage |
| `platform` | 配置、数据库连接、时钟、随机数、日志等基础能力 | 产品业务规则 |

## 5. 依赖规则

```text
HTTP / MCP
    |
    v
应用服务（identity / spaces / storage / transfer / share / preview / catalog）
    |
    +----> accesspolicy
    |
    +----> storage / jobs / audit
    |
    v
模块端口
    |
    v
SQLite、Linux 文件系统等适配实现
```

必须遵守以下规则：

- `transport` 不能直接调用 SQL、文件路径或 Linux syscall。
- MCP 与 REST 不创建各自的业务服务，只映射同一组应用服务。
- 管理员挂载 API 调用 `storage.Service` 控制面用例；该服务统一协调系统管理员授权、挂载身份验证、持久化、审计和后续任务，不向 MCP 暴露挂载管理能力。
- `accesspolicy` 返回结构化授权结果，不依赖 HTTP 状态码。
- 模块不能导入其他模块的 SQLite 实现，只能使用其公开服务或端口。
- 跨模块事务由明确的应用用例协调，不能形成隐式数据库级联。
- 只允许 `internal/app` 组装具体实现，业务模块内不使用全局服务定位器。
- 后台任务只保存任务类型、版本化参数和检查点；执行时重新检查当前权限、挂载身份和资源状态。
- 审计事件由业务用例产生，HTTP/MCP 只能补充请求 ID、入口类型和客户端信息。

## 6. API 与前端契约

`api/openapi/v1.yaml` 是 REST 契约唯一来源。`oapi-codegen` 生成 Go 请求类型和服务器接口到 `internal/transport/httpapi/generated`；固定版本的 `openapi-typescript` 生成 `web/src/generated/openapi.ts`，`openapi-fetch` 提供轻量运行时客户端。`go generate ./...` 和 `npm run generate:api` 是唯一生成入口，CI 会重新生成并拒绝任何差异。生成代码不能手工修改。

OpenAPI DTO 与领域模型分离，由 HTTP 适配层显式转换。MCP 使用独立、版本化的工具参数 schema，再映射到相同应用服务，不直接复用 HTTP handler。首阶段固定使用官方 Go MCP SDK；若代理探针证明它不能满足 Streamable HTTP、会话、Origin 校验、请求取消或长连接资源上限，框架阶段立即停止，不自行实现协议栈，先形成替代库评估和设计修订。

- 对象 ID 使用不可猜测的 UUIDv7。
- Token、分享密钥和下载票据使用独立的 256 位安全随机值。
- 列表与搜索使用不透明 keyset cursor，不使用深层 `OFFSET`。
- cursor 只保存翻页状态，不能代替授权或对象身份复核。

前端采用以下边界：

- React Router 只负责导航、登录态和能力可见性，不作授权裁决；后端 `accesspolicy` 始终是唯一授权来源。
- TanStack Query 负责服务端状态、缓存失效和请求取消。
- OpenAPI 生成客户端提供 REST 类型，不手写重复接口类型。
- 表单状态保留在组件内；首阶段不引入 Redux 或 Zustand。
- 登录身份、入口安全提示和少量全局 UI 状态使用 React Context。
- `web/src/generated` 只保存生成代码，`web/src/features` 按业务能力组织页面逻辑，`web/src/shared` 保存无业务状态的公共组件和基础能力。

## 7. SQLite 与事务

默认先采用纯 Go `modernc.org/sqlite`。SQLite 探针验证 WAL、Online Backup API、百万行查询和 ARM64 表现；全部门槛通过后保留。若备份能力或性能门槛失败，则切换到 `mattn/go-sqlite3`，并对 CGO 的 amd64/arm64 镜像重新执行同一套验证。业务模块不能感知驱动变化。

各业务模块持有自己的 SQL，`sqlc` 生成类型安全查询。`internal/platform/sqlite` 只负责连接、迁移、写入协调、事务和备份。

- 所有写操作进入一个有界写入协调器；队列满或超过等待上限时返回可重试错误。
- 读取使用短事务，文件或网络 I/O 期间不能持有数据库事务。
- 跨模块原子操作由应用服务通过 `UnitOfWork` 协调。
- 迁移按版本向前执行并嵌入程序；迁移或启动 `quick_check` 失败时终止启动。
- 备份使用 Online Backup API 或经探针证明等价的一致性方案。

正常启动使用 `PRAGMA quick_check` 控制低功耗 NAS 的启动时长。完整 `PRAGMA integrity_check` 只在备份恢复、管理员诊断和低优先级定时维护中运行。

典型的跨模块原子操作是“创建成员、创建唯一个人空间并写入审计”。`UnitOfWork` 负责把各模块仓储绑定到同一个事务，但不向领域服务暴露 SQL 或具体驱动类型。

## 8. 请求处理流

```text
浏览器 / REST / MCP
        |
        v
入口信任检查 + 路由组开关
        |
        v
认证、限速、请求 ID
        |
        v
协议 DTO -> 应用命令
        |
        v
Access Policy 统一授权
        |
        +----> SQLite 短事务
        +----> Storage 安全文件访问
        +----> Audit 结构化事件
        |
        v
领域结果 -> HTTP / MCP 响应
```

数据库记录只能帮助定位对象。下载、预览、分享和 AI 读取在最终打开文件时，必须重新验证 ACL、挂载状态、根身份和目标对象指纹。

## 9. 运行入口

Omnora 创建单一 HTTP 服务和监听地址。局域网或公网访问范围由 Docker 端口发布、宿主防火墙、安全组和反向代理决定，应用内不维护额外 LAN 或代理入口配置。

- 系统不能依靠中间件猜测请求来源，不能使用宽泛的全局 `trust proxy`。
- 管理端、成员 Web、分享、REST、MCP 和 OpenAPI 六个路由组分别注册和控制。

HTTP 服务必须设置有限的 `ReadHeaderTimeout`、`IdleTimeout`、`MaxHeaderBytes` 和并发连接上限。每个路由单独设置请求体、结果数、并发和流量上限。流式上传、下载和 MCP 长连接不使用会误杀合法流的短全局 `ReadTimeout` 或 `WriteTimeout`，改用路由上下文、固定缓冲、字节上限、空闲超时和心跳保护。

反向代理参考链路保持为：

```text
公网 HTTPS:443 -> 反向代理 -> HTTP 127.0.0.1:8080 -> Omnora
```

公网 `80` 只执行 HTTPS 跳转。代理专用端口只能由 Lucky 访问，不能公开发布。

## 10. 配置模型

配置分为三类：

```text
部署配置：/config/omnora.yaml + OMNORA_* 环境变量
应用配置：SQLite，由管理界面修改
敏感值：显式 *_FILE 形式注入，只保存路径，不写入普通配置
```

部署配置包括监听地址、LAN/代理 CIDR、外部 URL、数据库路径、文件根、日志级别和路由组默认开关，修改后需要重启。分享策略、配额和索引开关等运行时策略保存在 SQLite。

部署配置优先级固定为“环境变量 > `omnora.yaml` > 内置安全默认值”。未知字段、无效 CIDR、重复监听端口、公开绑定代理后端、缺失密钥或相互冲突的配置会阻止启动。

敏感值不能直接出现在 YAML、普通环境变量或命令行参数中，只接受显式的 `*_FILE`。秘密文件必须具有大小上限且为普通文件；读取时逐级拒绝符号链接并使用 `O_NOFOLLOW`，只接受当前进程用户所有的 `0400` 或 `0600` 文件。秘密路径不能位于 Web 资源、托管挂载、外部挂载或任何下载根中。配置转储、错误、日志和健康端点都不能返回秘密路径或内容；同一秘密同时出现多个来源时阻止启动。

Compose 层支持 `PUID`、`PGID`、`TZ` 和 `UMASK`。`PUID`/`PGID` 用于 Compose 的 `user` 映射，运行容器不先以 root 启动再降权；`TZ` 和 `UMASK` 由应用启动时显式应用并记录非敏感生效状态。默认 Compose 不使用特权模式、Host 网络或 Docker Socket。

## 11. 生命周期

启动顺序固定为：

1. 加载并完整验证部署配置。
2. 初始化结构化日志、时钟和安全随机源。
3. 打开 SQLite，执行迁移和 `quick_check`。
4. 检查密钥、凭证代际和恢复模式标记。
5. 检测到恢复模式时，只启动 loopback 本地恢复入口；执行完整 `integrity_check`、轮换凭证代际、删除会话、撤销 Token/分享/票据、标记账号重设凭证并禁用外部挂载。管理员明确结束恢复模式前，不启动普通 Web、分享、REST 或 MCP。
6. 正常模式下重新验证所有未删除挂载。
7. 恢复持久化任务，但暂不执行。
8. 启动两个已配置的独立入口。
9. 标记服务 ready，再启动任务消费者。

支持的恢复流程由离线 `omnora restore` 命令写入数据库替换操作之外的恢复模式标记，然后再启动服务。直接覆盖数据库并绕过恢复命令不是受支持的恢复方式；启动时发现备份代际或恢复标记不一致会失败关闭。

关闭时先停止接收请求，再取消后台任务、保存检查点、等待有限时间内的流式传输结束，最后关闭数据库。

提供最小化的 `/health/live` 与 `/health/ready`。健康端点不返回版本秘密、路径、成员或挂载信息。

## 12. 错误模型

HTTP 错误使用统一结构：

```json
{
  "code": "mount_identity_unverifiable",
  "message": "无法验证挂载身份",
  "request_id": "019...",
  "details": {}
}
```

- `code` 是稳定机器标识，HTTP、MCP 和前端使用同一错误目录。
- `message` 面向用户，但不能暴露宿主路径、SQL、堆栈或凭证明文。
- `details` 只包含字段校验、可重试时间等经过白名单允许的数据。
- 领域模块只返回稳定领域错误；HTTP 和 MCP 分别转换为协议状态。
- 数据库忙、任务队列满和临时资源不足返回明确的可重试错误，不无限等待。
- 权限不足、对象替换、挂载漂移和入口信任失败一律失败关闭。
- 错误日志必须带 `request_id`；安全事件同时进入审计，但正文和秘密不进入日志。
- 流式上传、下载和 MCP 长连接响应请求取消，客户端断开后停止继续读写。

## 13. 技术探针

### 13.1 SQLite 探针

- 生成 100 万条文件元数据，验证 keyset 分页、文件名查询和索引写入。
- 持续写入期间并发执行读取、WAL checkpoint 和 Online Backup。
- 备份恢复后通过 `PRAGMA integrity_check`，数据数量和检查点一致。
- 混合负载运行 30 分钟，不出现死锁、无限 busy wait 或持续内存增长。
- 参考 NAS 上进程空闲 RSS 不超过 300 MB，混合操作不超过 500 MB。
- 每页 200 条的 keyset 分页查询在预热缓存下 P95 不超过 250 ms，常用文件名查询在预热缓存下 P95 不超过 500 ms。

初次探针在项目目标的 8 GB 极空间设备上执行，并将该设备冻结为首版参考配置。每类查询至少采集 1000 个样本，混合负载固定为 4 个读取者与 1 个受控写入者；文件名查询集由精确名称、前缀、扩展名与目录范围组合构成，并记录每类查询的选择性。报告必须记录 NAS 型号与 CPU 架构、内核、内存、SSD/HDD、文件系统、SQLite 参数、合成数据分布、查询集、并发数、预热方式、采样窗口以及 RSS/PSS 口径。延迟硬门槛应用于预热缓存结果，冷缓存结果单独记录；驱动选型使用同一设备、数据和负载进行对比。

### 13.2 Linux Storage Resolver 探针

- 验证 `openat2`、`statx`、mount ID 和 `/proc/self/mountinfo`。
- 验证不支持 `openat2` 时的逐级 `openat + O_NOFOLLOW` 回退。
- 覆盖路径穿越、符号链接根、父组件链接、magic link、bind alias、bind 父子来源和并发替换。
- 任何无法证明安全的情况都返回 `mount_identity_unverifiable`。
- 探针可以在隔离的临时 mount namespace 中使用测试权限，但生产容器保持非特权。
- amd64 与 arm64 至少各执行一次真实 Linux 验证。

### 13.3 Lucky 与 MCP 探针

- 验证公网 `80` 只跳转 HTTPS，`443` 终止 TLS 后转发到代理专用端口。
- 验证代理后端不能从公网直连，未知 Host 被拒绝。
- 验证可信代理 CIDR、外部协议头、客户端地址和冲突头的失败关闭行为。
- 验证 Range、流式下载、客户端取消、WebSocket 和 MCP Streamable HTTP 长连接。
- CI 使用可控代理夹具验证协议语义；发布前在真实极空间和 Lucky 环境执行同一验收清单。

三个探针结果记录到 `docs/verification/probes/project-foundation.md`。任一硬门槛失败时先修正选型或设计，不能进入业务功能开发。

## 14. 测试与 CI

| 层级 | 工具与范围 |
| --- | --- |
| Go 单元测试 | `go test`，领域规则、错误映射、配置校验 |
| Go 集成测试 | 临时 SQLite、迁移、写入协调器、任务恢复 |
| 文件系统测试 | Linux 专用安全解析、竞态和挂载身份 |
| API 契约测试 | OpenAPI 生成结果、错误结构、分页 cursor |
| 前端测试 | Vitest + Testing Library，路由和数据状态 |
| 浏览器测试 | Playwright，双入口、会话隔离和基础 UI |
| 性能测试 | 百万元数据、固定缓冲传输、RSS 与查询延迟 |
| 发布验收 | amd64、arm64、极空间、通用 Linux Compose |

PR 必跑门禁按以下顺序执行：

1. 格式化、静态检查和生成代码差异检查。
2. Go 单元测试、竞态测试及前端测试。
3. SQLite 与 API 集成测试。
4. Linux 文件系统安全测试。
5. 前端生产构建和 Go 静态资源嵌入验证。
6. amd64 与 arm64 镜像构建。
7. 非 root 容器启动和健康检查。
8. 依赖与容器漏洞扫描。
9. 可控代理夹具和当前平台的快速技术探针。

定时基线门禁执行百万数据、30 分钟混合负载以及 amd64/arm64 的完整 Storage Resolver 探针。发布门禁在真实 arm64、极空间和 Lucky 环境执行硬件验收，并要求 `docs/verification/probes/project-foundation.md` 中的版本、镜像摘要、配置和硬件记录与候选版本一致。

PR 必跑步骤失败会阻止合并；定时基线或真实硬件验收失败会阻止发布。需要临时 mount 权限的文件系统测试只在隔离的专用 CI 作业运行，不能改变生产容器的权限模型。

## 15. 框架阶段完成条件

框架阶段只有同时满足以下条件才完成：

1. 一个命令可以运行开发环境和全部基础检查。
2. 一个二进制可以提供前端静态页、两个独立入口、健康检查和空的 REST/MCP 协议壳。
3. OpenAPI 可以生成 Go 服务接口和 TypeScript 客户端。
4. SQLite 迁移、备份恢复、优雅关闭和结构化日志可验证。
5. amd64 与 arm64 镜像均能以非 root 用户启动。
6. 三项技术探针全部通过并形成选型结论。
7. 工程骨架不包含虚假业务成功响应，不提前实现未进入本阶段的业务功能。

框架验收后，业务功能按独立纵向切片进入后续规格与实施计划。首个建议切片是“一次性初始化和首个管理员账号”；“创建成员并原子创建其唯一个人空间”作为后续独立切片，避免在管理员是否同时具有成员身份尚未定义时错误创建个人空间。挂载注册在 Linux Storage Resolver 探针通过后单独规划。
