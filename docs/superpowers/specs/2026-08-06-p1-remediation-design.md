# Omnora P1 Remediation Design

> **资源模型说明：** 本文保留迁移前 P1 问题与安全整改背景；其中 Space、`spaceId`、空间 ACL 和旧资源边界不属于目标契约。目标账号、个人目录、共用挂载和协作关系以[账号、挂载与内容授权设计](2026-08-08-account-mount-access-design.md)为准，安全约束以[安全模型](../../security/security-model.md)为准。

日期：2026-08-06

状态：待用户评审

范围：当前分支全项目功能 Review 中确认的 P1 安全、数据一致性、恢复和契约问题

## 1. 目标

本设计把已确认的 P1 从分散的 handler 修补收敛为四条可独立验收、按依赖顺序交付的可靠性主链：

1. 身份、HTTP 信任边界与安全审计。
2. 文件、上传、挂载与分享授权。
3. 备份恢复、路由配置与容器运行身份。
4. Catalog、Jobs 与 REST/OpenAPI 契约。

完成后必须达到以下结果：

- 管理员不能绕过 MFA，敏感凭证变化不会留下仍然有效的旧能力。
- Cookie、Host、Origin、CSRF、客户端 IP 和限流具有显式可信边界。
- 文件移动、上传、恢复、分享和索引在并发、进程崩溃与数据库错误下不会静默丢数据或泄露替换对象。
- 备份恢复不会复活旧会话、Token、分享和上传，也不会在未知 schema 上继续提供服务。
- OpenAPI、真实 handler 和前端类型描述同一份当前契约。

P2 清单独立保存在 [P2 Code Review Findings](../../reviews/2026-08-06-code-review-p2-findings.md)，不得在 P1 实施时顺手扩大为无边界重构。

## 2. 约束与权威来源

- 安全边界以 [安全模型](../../security/security-model.md) 为最高优先级。
- 文件身份、分享和删除语义以 [领域模型](../../design/domain-model.md) 为准。
- 任务、索引、部署和恢复以 [技术架构](../../design/architecture.md) 为准。
- 发布完成标准以 [验收标准](../../verification/acceptance-criteria.md) 为准。
- 继续维持单 Go 进程、SQLite WAL、单容器和 React/Vite 前端，不引入 Redis、PostgreSQL、独立 Worker 或全量服务端代码生成重写。
- 当前项目尚未正式发布。修正错误 OpenAPI 时以真实且已被 Web 使用的 JSON 为兼容基线，不为从未实现的 stale API 保留虚假兼容层。
- 文档和计划不授权 Git commit；提交仍需用户另行明确要求。

## 3. 方案选择

### 3.1 方案 A：逐 handler 局部修补

改动最少，但会继续保留重复 SQL、best-effort 撤销、匿名 DTO、路径式文件操作和分散的认证判断。一个入口修好后，另一个入口仍可能绕过同一安全不变量。拒绝采用。

### 3.2 方案 B：分域状态机与共享安全原语

采用集中身份安全服务、HTTP 信任中间件、数据库权威状态、operation journal、lease/fencing token、对象身份和契约测试。改动比局部修补大，但每条主链都能独立测试、迁移和回滚。采用本方案。

### 3.3 方案 C：按项目框架设计整体重写

直接引入完整生成式 OpenAPI 服务端、统一 UnitOfWork 和新的模块目录，长期结构最整齐，但会把缺陷修复变成大规模架构迁移，难以控制回归和上线顺序。拒绝作为本轮方案。

## 4. 总体架构

~~~mermaid
flowchart TD
    A[HTTP Trust Boundary] --> B[Identity Security Service]
    A --> C[Rate Limiter]
    B --> D[Transactional Audit]
    C --> D
    E[Storage Primitives] --> F[Upload and File Operations]
    E --> G[Share Identity and Tickets]
    F --> H[Operation Journal]
    G --> H
    I[Job Lease and Fencing] --> J[Catalog Scan Epoch]
    E --> J
    K[Recovery Coordinator] --> B
    K --> D
    K --> I
    L[Runtime DTO Contract Tests] --> A
    L --> G
    L --> M[Generated Web Types]
~~~

核心选择：

- 数据库是会话、上传、Job、分享票据和 operation 状态的权威来源；JSON manifest 和内存 mutex 不能承担跨请求正确性。
- 文件系统 I/O 期间不持有长 SQLite 事务；通过持久 operation、CAS、lease 和 fencing token 连接两个世界。
- 所有对外能力在使用时重新验证账号、ACL、挂载身份、对象身份和 generation。
- 高风险数据库变更与 audit 在同一事务提交；文件系统变更先写 operation 与失效授权，再执行 I/O，失败进入可恢复状态。
- 数据迁移统一使用 expand、backfill、enforce、cleanup，不在同一发布中先删除旧结构。

## 5. 工作流一：身份、HTTP 安全与审计

### 5.1 管理员 MFA、pending TOTP 与近期重新认证

> **已由产品决策取代（2026-08-07）：** 管理员与成员的 TOTP **均为可选**。本节原先“强制管理员 MFA / enrollment / 禁止停用”的目标作废；权威规则见 `docs/security/security-model.md` §4.2。下列仍有效的部分仅保留 pending TOTP 与近期重新认证机制。

覆盖仍有效的 P1：setup 立即覆盖有效密钥；敏感 mutation 缺近期重新认证。

目标设计：

- identity_sessions 先增加 nullable purpose 和 reauthenticated_at；新代码只接受显式 full 或 totp_enrollment，NULL 一律 fail closed，不能兼容解释为 full。accounts 同时增加 password_reset_required 和 totp_reset_required；恢复后的登录必须先完成密码重置。`totp_reset_required` 不再把管理员锁进 enrollment。
- accounts 增加 totp_pending_secret_ciphertext 和 totp_pending_expires_at。
- 未启用 TOTP 的管理员在密码正确后获得 full session，与成员相同；不得强制 enrollment 页。
- setup 只写 pending；confirm 在事务中验证 pending、提升为 active、清空 pending、轮换 session。确认前旧 TOTP 始终有效。
- requireAdmin 检查 active admin、full session，以及 `password_reset_required=0`。管理员可停用 TOTP。
- 密码、TOTP 替换和下表列出的敏感 mutation 要求最近五分钟内重新认证；轮换 session 不延长原绝对过期时间。普通 GET/list、成员文件操作、上传、偏好、创建或撤销普通分享不要求近期重新认证，只要求对应的 full session 和业务授权。

近期重新认证协议固定为 POST /api/v1/account/reauthenticate：

- 请求为 password 和可选 totpCode；任意角色验证密码，**仅在该账号已启用 TOTP 时**再验证当前 TOTP。
- 遗留 `totp_enrollment` session 不能调用 reauthenticate；新登录不再签发该 purpose。
- 若 password_reset_required，登录请求在旧密码验证成功后必须同时提交符合策略的新密码；密码哈希在事务外计算，服务端在同一事务清除 reset flag 并创建 full session。没有新密码时不得签发 full session。
- 成功后轮换 session token 和 CSRF token，在新 session 写 reauthenticated_at，并返回 status=reauthenticated 与 reauthenticatedUntil。
- 过期返回 403 reauthentication_required；前端只能在该错误后进入 reauthenticate 流程，不能用固定延时猜测有效期。
- 凭证错误统一返回 401 invalid_credentials，限流返回 429 和 Retry-After，前端保留原操作并引导完成重新认证后重试。

近期重新认证路由矩阵固定为：

| 方法与路径 | 说明 |
| --- | --- |
| PATCH /api/v1/account/password | 修改当前账号密码 |
| POST /api/v1/account/totp/setup; POST /api/v1/account/totp/confirm; POST /api/v1/account/totp/disable | full session 的登记、替换或停用；totp_enrollment 首次登记例外，不调用 reauthenticate |
| DELETE /api/v1/account/sessions/{sessionId} | 撤销其他登录会话；DELETE /api/v1/auth/session 退出当前会话不要求 |
| POST /api/v1/ai-tokens、DELETE /api/v1/ai-tokens/{tokenId} | 创建或撤销长期 Bearer 凭证 |
| POST /api/v1/admin/users、POST /api/v1/admin/users/{userId}/disable、/enable、/revoke-sessions | 管理员账号 mutation |
| POST /api/v1/admin/spaces; PATCH /api/v1/admin/spaces/{spaceId}; DELETE /api/v1/admin/spaces/{spaceId}; PUT /api/v1/admin/spaces/{spaceId}/members/{accountId}; DELETE /api/v1/admin/spaces/{spaceId}/members/{accountId} | ACL 和空间 mutation |
| POST /api/v1/admin/mounts; PATCH /api/v1/admin/mounts/{mountId}; POST /api/v1/admin/mounts/{mountId}/reverify; DELETE /api/v1/admin/mounts/{mountId} | 挂载创建、改名、重验和删除 |
| DELETE /api/v1/admin/shares/{shareId}、DELETE /api/v1/admin/ai-tokens/{tokenId} | 管理员撤销外部能力 |
| POST /api/v1/admin/backups、POST /api/v1/admin/backups/{backupId}/restore | 备份和恢复 mutation |
| POST /api/v1/admin/index-jobs、POST /api/v1/admin/index-jobs/{jobId}/run | 管理员调度 mutation |
| PATCH /api/v1/admin/route-groups/{groupId} | 改变网络可达面 |

所有 GET/list/overview/audit 查询只要求 full MFA session 和管理员授权，不要求 recent reauth；新增管理员 mutation 必须在 RouteDefinition 中显式标记 requires_recent_reauth，契约测试禁止依赖路径前缀猜测。

主要文件：

- internal/identity/types.go
- internal/identity/service.go
- internal/identity/security.go
- internal/server/api.go
- internal/server/account_security.go
- internal/server/admin_control.go
- web/src/member/MemberAccountPanel.tsx
- web/src/member/MemberFilesApp.tsx

兼容策略采用 expand、backfill、switch、enforce：007 先增加 nullable 列；随后在普通 HTTP 监听前，用事务撤销所有未确认 TOTP 管理员的旧 session，把其余仍有效旧 session 回填为 full，并验证不存在 active 且 purpose 为 NULL/未知值的记录。验证失败则启动失败。新代码切换后 NULL 永远无效，reauthenticated_at 为空的旧 session 首次敏感操作必须重新认证。跨一个稳定发布后再通过 SQLite table rebuild 增加 purpose 的 NOT NULL/CHECK 约束；该清理迁移不属于本轮五个加法迁移，不能提前删除兼容列。

### 5.2 改密、禁用账号和凭证撤销原子化

覆盖 P1：改密不撤销其他会话、撤销错误被吞、禁用账号后分享仍可用、重新启用后旧 Token 恢复。

目标设计：

- identity 安全服务提供事务化 ChangePasswordSecure 和 DisableAccount。
- 改密始终撤销其他 session，轮换当前 session；Token 和分享按用户选择撤销，但任一 SQL 失败都回滚整个改密。所有新签发的 session、browser session、AI Token、share session、upload session 和 download ticket 记录当前 system_state.credential_generation，验证时必须与当前 epoch 相等；旧 epoch 不能仅靠未设置 revoked_at 继续使用。
- 禁用账号原子撤销 identity_sessions、browser_sessions、ai_tokens、该账号创建的 shares 和 share_sessions，并增加 share generation。
- recovery finalize 在同一事务提升 credential_generation，并显式撤销所有旧 credential classes；generation 是防漏撤销的第二道校验，不替代各表的 revoked_at/revoked_reason。
- 重新启用只改变账号状态，不清除 revoked_at，不允许旧能力复活。
- 初始管理员保护查询、账号状态改变、撤销和 audit 位于同一事务。

### 5.3 HTTP 信任边界、Secure Cookie、Host、Origin 与 CSRF

覆盖 P1：TLS 反代下 Secure=false，缺少 Host、Origin 和 CSRF。

目标设计：

- HTTPConfig 增加 PublicURL、AllowedHosts、AllowedOrigins 和 TrustedProxyCIDRs。TrustedProxyCIDRs 为空表示不信任任何代理，不是信任全部。
- 只要任一业务路由组可达，PublicURL 必填；AllowedHosts 或 AllowedOrigins 为空时分别只从 PublicURL 派生一个精确 authority 或 origin，不支持通配符和 allow-all。PublicURL 缺失或无法解析时只允许 health/ready 启动诊断，普通监听 fail closed。显式 http 仅允许 loopback 开发配置；部署环境必须使用外部 https PublicURL。
- Cookie Secure 和 HSTS 由配置的外部 HTTPS 决定，不信任任意 X-Forwarded-Proto。
- Host 在路由前校验；浏览器 API 和 MCP 校验 Origin。畸形、null 和非白名单 Origin fail closed。cookie_session、share_cookie 和 public_pre_auth 的非安全方法必须携带允许的 Origin；Bearer/MCP 非浏览器请求可以不带 Origin，但一旦携带就必须命中白名单；安全方法未携带 Origin 可以继续，携带时仍需校验。
- 路由注册显式声明 public_pre_auth、cookie_session、share_cookie、bearer 或 health 五种 auth mode，并在注册时套用对应 wrapper。Host/Origin 是全局入口检查；CSRF 只包裹 cookie_session 和 share_cookie 的非安全方法。login、initialize、share exchange 等 public_pre_auth POST 不要求 CSRF；Bearer 和 health 路由不因请求中偶然携带 Cookie 而改变模式。
- Cookie-auth 的 POST、PUT、PATCH、DELETE 使用 double-submit CSRF。生产 HTTPS 使用 host-only Secure Cookie；本地显式 HTTP 开发使用独立非 Secure 名称。
- Bearer-only 请求不要求 CSRF，但仍执行 Host/Origin 策略。
- 只有 RemoteAddr 命中 TrustedProxyCIDRs 时才解析转发客户端 IP；否则忽略所有 forwarded headers。可信链算法从 RemoteAddr 开始向左剥离连续可信代理，选择最右侧第一个非可信地址作为 client IP；如果整条链都标记为可信，则回退到 RemoteAddr，不采信可伪造的 leftmost 值。畸形地址、重复/超长链和 Forwarded 与 X-Forwarded-For 同时出现或冲突时拒绝该转发链并回退到 RemoteAddr。链长度设置小型硬上限并纳入测试。

新增 internal/server/http_security.go；Server.Handler 固定 request ID、access log、安全响应头和 Host/Origin，认证与 CSRF 由 routes 注册时的精确 wrapper 在执行 handler 前完成。

前端统一在 web/src/api.ts 的请求封装中读取可由 JavaScript 访问的 CSRF Cookie，并为 POST、PUT、PATCH、DELETE 设置 X-CSRF-Token。上传、预览和其他直接 fetch 调用必须复用同一安全请求函数；登录、session rotation、reauthenticate 和 logout 分别签发、轮换或清除 CSRF Cookie。CSRF Cookie 不设置 HttpOnly，session Cookie 继续保持 HttpOnly。

### 5.4 双维渐进限流

覆盖 P1：登录、TOTP、分享密码和初始化没有来源 IP＋主体限流。

目标设计：

- 新建 internal/ratelimit，Server 持有有界分片 limiter。
- 每次认证同时检查可信 client IP bucket 和 HMAC 后的账号、分享 public ID 或 initialization 主体 bucket。
- 默认 subject 在 5、10、20 次失败后分别进入 1、5、30 分钟 cooldown；IP bucket 从 20 次失败开始，最大一小时。
- 成功只清除 subject 失败计数，不清除 IP 计数；429 返回 Retry-After。
- limiter 达到容量时进入 overflow bucket，不能简单淘汰旧 key 造成绕过。
- 当前单实例 v1 使用内存 limiter，接受进程重启清空计数的权衡，避免攻击请求放大 SQLite 写入。
- 不存在账号、错误密码、缺失或错误 TOTP 使用相同的 401 invalid_credentials 响应，并执行近似验证成本。登录保持单次 POST：前端始终提供可选 totpCode 输入，服务端不签发第二步 challenge，也不暴露账号是否启用 TOTP。只有密码正确且管理员尚未登记 TOTP 时，才返回 enrollment session。

### 5.5 Audit fail-closed 与可关联性

覆盖 P1：高风险 mutation 忽略 recordAudit 错误。

目标设计：

- audit Recorder 同时接受 DB 和 Tx，增加 RecordTx。
- 当前并行但尚未纳入版本控制的 006_standard_mcp.sql 已增加可空的 result 和 request_id。当前编号方案把该迁移成功落入目标分支并通过 migration upgrade test 作为硬前置；满足前置后，P1 只补 reason_code、subject_hash、索引和历史值回填，不能重复 ALTER 同名列。若 006 没有先落地，必须先把共享 audit expand 提取为下一可用的公共迁移，并让 Standard MCP 与 P1 共同依赖，再整体重排后续编号；禁止 007 隐式依赖工作区未跟踪文件。
- HTTP wrapper 在打开事务前构造不可变 AuditContext，包含 actor、request ID、可信 client hash、route group 和 credential public ID。RecordTx 只能使用调用方传入的同一 Tx 和 AuditContext，不得通过 optionalSession、VerifySession 或 sql.DB 再次查询；SQLite 单连接下禁止事务内重入数据库。
- 账号、ACL、Token、分享、恢复等数据库高风险操作与 success audit 同一事务；audit 失败则业务回滚。
- 文件系统高风险操作先写 file_operations 和授权失效 audit，再执行 I/O；最终结果通过 operation journal 可靠补记。
- 认证拒绝和限流事件不改变既有 401/429，但写失败必须产生结构化 error 和健康告警，不能静默吞掉。
- IP、User-Agent 和 subject 使用独立 domain label 的 HMAC；新增 OMNORA_AUDIT_HMAC_KEY，由 entrypoint 生成并持久化。
- 密码 Argon2/PBKDF2 计算和 TOTP 加解密在开启写事务前完成；事务内通过旧 hash、账号状态或 version 做 CAS，避免昂贵计算长期占用 SQLite 写锁。
- 旧 runtime.env 缺少 audit key 时，entrypoint 以临时文件加原子 rename 方式保留原初始化 token 和 TOTP encryption key，并只追加新生成的 audit key；升级和回滚都不得旋转既有 TOTP key。

## 6. 工作流二：文件、上传、挂载与分享

### 6.1 共享存储原语

先在 internal/storage 或 internal/files 建立：

- RenameNoReplace
- ObjectIdentityFromOpenFile
- FsyncFileAndParent
- descriptor-relative OpenRootWalk
- SQLite immediate transaction 和 fencing token 辅助

Linux 使用 renameat2(RENAME_NOREPLACE)。OpenRootWalk 使用 openat2 和 RESOLVE_BENEATH、RESOLVE_NO_MAGICLINKS、RESOLVE_NO_SYMLINKS，并按挂载策略增加 RESOLVE_NO_XDEV；fallback 必须逐级使用目录 FD、openat、O_NOFOLLOW 和对象类型验证。裸 os.Root 只能保证不逃出 root，不能单独满足本项目的 symlink、magic link、跨文件系统和特殊文件策略。无法提供可靠 no-replace 或安全遍历的平台返回明确不支持，不退化为 Lstat 加 Rename。

### 6.2 上传 lease、CAS 与恢复状态机

覆盖 P1：每请求 NewService 让 mutex 失效，并发 part 覆盖 manifest，complete/cancel 竞态。

数据库成为上传状态和 part 清单的权威来源。upload_sessions 增加 version、operation_phase、lock_token、lock_epoch、lease_expires_at、cleanup_pending、final_identity；upload_parts 增加 checksum、state、write_token。现有 status CHECK 保持 active、completed、canceled、expired、failed，不在本轮重建表；中间态全部由 operation_phase 表示，failed_safe 映射为 status=failed 且 operation_phase=recovery_required。

固定状态流：

1. status=active、phase=initializing 到 phase=writing。
2. status=active、phase=writing 到 phase=completing，再到 status=completed。
3. status=active 到 phase=canceling，再到 status=canceled。
4. status=active 到 phase=expiring，再到 status=expired。
5. 中间态崩溃由 recovery worker 继续，或转 status=failed、phase=recovery_required。

每个 upload 同时只允许一个 lease owner。进程内 keyed mutex 只降低竞争，正确性由数据库 CAS 和 fencing token 保证。Part 在 fsync 后重新验证 token，再发布 part 并事务写 upload_parts。Complete 与 Cancel 最多一个 CAS 成功。

旧 active upload 的 manifest 不能由 SQL 迁移读取。新版本在进入 ready 前运行一次应用级 importer：按 upload_sessions.temp_dir 打开并验证 JSON manifest、目录归属、part 编号、size 和 checksum，再导入 upload_parts。无法证明一致的 session 标记为 status=failed、phase=recovery_required，原文件移入隔离目录而不是删除。过渡发布先双写 DB 和 manifest；所有 active session 导入完成后再让 DB 成为唯一读取来源。

### 6.3 回收站恢复与跨挂载移动

覆盖 P1：同秒恢复覆盖、跨挂载 copy 后删除新内容、symlink/特殊文件被跳过。

- 回收站冲突名包含 trash ID 或随机后缀，并通过 RenameNoReplace 循环尝试。
- 跨挂载 move 不再直接 copy 原路径后 delete。先把源原子 rename 到源挂载内部 staging，再复制到目标 staging，保存完整 manifest，fsync、验证、no-replace 发布，最后清理源 staging。
- symlink、FIFO、socket、device 等不支持对象直接失败，不得跳过后删除源树。
- 新增 file_operations，状态为 prepared、source_staged、copying、destination_staged、published、source_cleaned、completed；任一阶段可进入 recovery_required。
- 进程崩溃后根据 operation ID 对应的 staging、源和目标身份幂等恢复；无法证明安全时保留数据并要求人工处理。
- 目标发布后、删除源 staging 前必须重新遍历源，与复制 manifest 比较条目集合、身份、类型、size 和 checksum；任何新增、删除或变化都进入 recovery_required，禁止 RemoveAll。rename 到 staging 不能被当作宿主机或 NAS 客户端停止写入的证明。

### 6.4 挂载注册原子化

覆盖 P1：检查与 INSERT 分离导致并发同根或重叠根注册。

- mountid 继续负责 capture 和纯函数冲突判断。
- 文件系统初次 probe 可在事务外；写入前 BEGIN IMMEDIATE，再次 capture、读取所有未删除挂载、检查 path、父子关系、device/inode 和 bind source，再 INSERT。
- mounts 增加 canonical_root_path、identity_key、mount_source_key；新增 mount_identity_claims，以 UNIQUE(claim_type, claim_key) 原子占用 exact path 和 device/inode identity。父子路径和 bind source 仍由事务内扫描判断。
- parent-child 和 bind-source overlap 仍由事务内全量冲突检查保证。
- reverify 按 mount ID 排除自身，不能按 path 排除所有重复记录。
- 008 迁移只增加 nullable 规范化列和空 claims 表。应用启动回填前先审计 exact path、device/inode、父子路径和 bind source 冲突；涉及冲突的全部挂载标记 unavailable，不为它们创建 claim，并产生管理员可读诊断，不能任意保留一个 active。无冲突挂载在 BEGIN IMMEDIATE 中回填列和 claims；普通路由只在全部有效挂载完成回填后启动。

### 6.5 分享能力、下载票据和对象身份

覆盖 P1：preview/download 语义错位、Range 重复计数、分享只绑定路径、撤销失败被忽略。

- Preview 要求 allow_preview，不增加 used_downloads；附件下载要求 allow_download。
- 新增 share_download_tickets。创建 ticket 时在同一事务验证 share/session/generation/能力，实时获取对象身份，原子预占一次下载额度并写 ticket。
- 下载握手固定为 POST /api/v1/share/download-tickets，请求包含 share-relative path，响应 201 返回非敏感 ticketId、downloadUrl、expiresAt，并设置名称固定但 Path 精确到 /api/v1/share/downloads/{ticketId} 的 HttpOnly ticket secret Cookie。downloadUrl 只包含不可用作认证的 public handle；secret 不进入 URL、日志或 Referer。Web 阻止原下载链接默认跳转，先创建 ticket，再用返回 URL 触发浏览器下载。HEAD 和后续 Range 请求都访问同一 URL。
- Ticket 状态为 issued、streaming、completed、expired、canceled，包含 transfer_owner、lease_expires_at 和 committed_offset。每个 ticket 同时最多一个 stream owner；首次完整 GET 只允许 committed_offset=0，续传 Range 起点必须等于 committed_offset。服务端只按实际成功写出的字节推进 offset；完成后任何 GET、bytes=0- 或并发重放都拒绝。v1 不支持并行分段下载。
- Range 请求复用同一个短期 ticket，不再增加计数；ticket 绑定 share、session、generation、mount、path、ETag 和文件身份。Preview 使用独立不计数入口，不复用 download ticket。
- shares 持久化 target_kind、target_identity_json、invalidated_at、invalidated_reason。每次 current/list/preview/download 都重新验证根对象身份。
- 文件分享要求目标完全同一；目录分享要求目录根身份同一，目录内每个下载 ticket 再绑定选中文件身份。
- 身份检查返回 match、definitive_changed_or_not_found、transient_unavailable 三态。只有确定变化或不存在时进入 target_moved_or_replaced、增加 generation 并撤销 sessions/tickets；NAS 离线、权限错误和瞬时 I/O 只让本次请求失败并标记挂载 unavailable，不永久改变分享。
- 同挂载 rename/move 在 file operation 中按对象身份原子更新目标及目录后代分享路径，增加 generation 使旧 session/ticket 重新绑定，但保持分享 active；崩溃恢复按 operation 和对象身份完成或回滚路径映射。删除、跨挂载 move 和外部同路径替换则可靠撤销分享。任何授权更新失败时文件系统操作不得开始。

旧分享只有在 object_id、历史 fingerprint 和实时对象三者一致时才自动绑定；无法证明的旧分享 fail closed 并要求所有者重新确认，不能把当前同路径对象当作原对象。

### 6.6 分享 fragment 优先级

覆盖 P1：已有 A Cookie 时打开 B fragment，页面继续显示 A。

前端初始化固定为：先解析 fragment；有合法 fragment 就交换新分享，成功后 replaceState 清除 fragment，再读取 current；只有没有 fragment 时才恢复旧 Cookie。错误密码仍按 P2 文档保留表单；多标签页独立 session 属于 P2-SHARE-01。

## 7. 工作流三：备份恢复、路由和容器身份

### 7.1 两阶段离线恢复

覆盖 P1：当前 restore 只做 SQLite integrity check，恢复后旧凭证、分享、上传和挂载继续有效。

不继续在正在服务请求的进程中原地替换数据库。采用两阶段恢复：

1. 管理员以 full MFA session 和近期重新认证创建 restore request；API 在同一事务写入 `preparing`、ready=false 和 0600 恢复请求文件，暂停 job maintenance，刷出 202 后触发受控退出。旧进程不再处理普通请求。
2. 下一次启动在监听 HTTP 前，由 recovery coordinator 复制目标快照到 staging，检查完整性和 schema 版本，在 staging 上执行向前迁移，并创建恢复前安全快照。
3. 恢复 staging 后，在单一事务中提升 `system_state.credential_generation`，并由各计划在签发/验证时比较该 epoch；同时显式撤销所有会话、AI Token、分享、share session、download ticket 和 upload session。账号设置 `password_reset_required`，管理员同时设置 `totp_reset_required` 并清除旧 active/pending TOTP；外部挂载全部 disabled。
4. 写入恢复 audit 和 `finalize_required`。普通 Web、分享、REST、MCP 不启动；recovery-only 进程只提供 health/ready，不提供公共 finalize HTTP 路由。
5. 宿主机通过本地 `omnora recovery finalize` 完成元数据清理，将状态切为 `normal_pending_bootstrap` 后退出；bootstrap 进程只开放登录、携带 `newPassword` 的密码重置登录、管理员 TOTP enrollment/confirm 和 logout，`/readyz` 固定 503。它先完成 password-reset/TOTP enrollment、挂载回填、session-purpose、route hydration、HTTP security 和 audit readiness 聚合校验，全部通过后在同一事务切回 `normal` 并退出；后续 normal 进程才监听普通路由。

新版本拒绝恢复 schema 版本高于当前二进制的快照；低版本快照必须在 staging 迁移成功后才能替换正式数据库。恢复请求、staging 和安全快照都使用唯一 ID 与 0600 权限。

当前 P1 只处理现有 SQLite snapshot 的安全恢复。配置、密钥和托管文件组成的完整备份包仍以架构文档为最终目标，不把未实现的 bundle 能力伪装成已完成。

### 7.2 备份文件唯一性和权限

覆盖 P1：秒级文件名导致并发备份相互覆盖。

- 文件名包含 backup ID，不只使用时间戳。
- 目标使用独占创建；backups 目录 0700，快照 0600。
- 记录入库失败时清理孤儿文件；清理失败进入可见 cleanup_pending。
- 同一实例可以串行执行 backup/restore，禁止并发恢复。

### 7.3 路由组三层配置

覆盖 P1：标准 Compose 的默认环境变量每次重启覆盖 UI 状态。

路由状态按显式环境 override、SQLite 管理员偏好、内建首次默认三层计算：

- Compose 和 Dockerfile 不再为六组注入非空默认环境变量。
- 环境变量只有部署者显式设置时才是 override。
- override 只改变 effective 状态，不回写 SQLite。
- 内建首次默认固定为 admin_web=true、member_web=true、share=false、rest=true、mcp=false、openapi=true，与当前标准 Compose 的行为一致。
- route_groups 增加 configured 字段表示是否存在管理员偏好。迁移不能依赖当前不可靠的 initialized marker：空 accounts 表的新数据库把六行设为 configured=false；存在任一未删除账号的旧实例把现有六行设为 configured=true，避免升级改变既有可达性。初始管理员受保护，因此已初始化实例始终有可靠账号判据。管理员 PATCH 写 enabled 并设置 configured=true。
- RouteGroup DTO 增加 source 和 locked。环境锁定时 PATCH 返回 409 route_group_env_locked，UI 显示对应变量来源。
- 移除 override 后自动恢复之前保存的 SQLite 偏好。
- hydrateRouteGroups 返回的任何查询或持久化错误必须阻止普通 Server 构造和监听；NewServer 改为返回 error，cmd 入口在注册路由前 fail closed。不得继续忽略 hydrate 错误或回退到扩大暴露面的默认值。

### 7.4 PUID、PGID 与 UMASK

覆盖 P1：入口脚本硬编码 1000:1000，UMASK 未统一应用。

- entrypoint 验证 PUID、PGID 为受支持的正整数，验证 UMASK 为三或四位八进制。
- root bootstrap 不修改只读根文件系统中的 /etc/passwd 或 /etc/group；入口脚本直接使用经过验证的数字 PUID:PGID 执行 su-exec，并检查 config、data、managed 和 predeclared root 的所有权。Compose 继续只授予 CHOWN、SETUID、SETGID 能力。
- 已有大目录不执行无界递归 chown；根身份不匹配时给出明确迁移命令并 fail closed，避免启动时扫完整 NAS。
- bootstrap secrets 和 marker 继续显式使用 0600/0700；完成 bootstrap 后，在 exec 应用前统一应用配置 UMASK。
- entrypoint 测试必须覆盖首次启动、重启、自定义 UID/GID、非法配置和已有数据所有权冲突。
- 项目框架文档原先约定 Compose user 映射且不经 root bootstrap；本方案选择与当前受限入口一致的 root bootstrap 加数字 UID:GID，并要求同步修订该文档，不能保留两套相互冲突的部署承诺。

## 8. 工作流四：Catalog、Jobs 与 OpenAPI

### 8.1 可恢复 DFS checkpoint

覆盖 P1：单 relative_path 游标与 DFS 顺序不一致，跨批次永久漏项。

Job checkpoint 改为版本化 DFS 栈，保存 scan ID、mount identity 和每层 directory/afterName。每个 frame 只在自己的目录内续扫；进入子目录时保留父 frame。复杂度与目录深度相关，不要求一次加载百万路径。

每批通过 descriptor-relative walker 收集，打开后 fstat 获取身份；同一事务 upsert entries 并保存 checkpoint。旧 cursor checkpoint 不能安全转换，升级后从头开启新 scan ID。

### 8.2 Scan epoch 与删除对账

覆盖 P1：完整扫描只 UPSERT，外部删除和移动永远残留。

- catalog_entries 增加 last_seen_scan_id。
- 每轮完整扫描生成固定 scan ID，每个 upsert 写 last_seen_scan_id。
- 只有遍历完整结束后，才把同 mount 中未被本轮看到的 entry 标记 deleted_at。
- 最终 tombstone 和 job completed 在同一事务；中断、暂停、身份漂移和崩溃都不能执行 tombstone。

### 8.3 Job lease、heartbeat 与 fencing token

覆盖 P1：Worker claim 后崩溃留下 running，整个队列永久阻塞。

- jobs 增加 claim_token、lease_expires_at、heartbeat_at。
- Claim 使用 BEGIN IMMEDIATE 或原子 UPDATE RETURNING；running 判断只考虑未过期 lease。
- SaveCheckpoint、Requeue、Complete、Fail 全部要求匹配 claim_token。影响行数不是 1 说明 worker 已失去所有权，必须停止。
- 维护任务回收过期 lease；未达到 max_attempts 的任务保留 checkpoint 并重新 queued，达到上限进入 failed。
- entries 与 checkpoint、最终 tombstone 与 completed 分别同事务，避免崩溃窗口。

### 8.4 REST/OpenAPI 契约重建

覆盖 P1：分享、挂载、目录、管理员用户等 OpenAPI 与真实 handler 不兼容。

- 保持当前 Web 已实际使用的 REST JSON 为兼容基线。
- 第一批为 session、account password、TOTP setup/confirm/disable、reauthenticate、enrollment session、space、mount、directory listing、share、share session、AI Token、admin user、route group、health/ready 建立命名 HTTP DTO。
- 删除所有不存在的 x-omnora-stale 活动 paths，不为 targetObjectId 等未实现契约编写假 handler。
- OpenAPI 如实描述分享交换的 200 password_required 和 201 created 两条成功分支。
- 从 OpenAPI 生成 TypeScript 基础类型，api.ts 保留请求封装和 UI ViewModel；本轮不生成 Go handler，避免扩大重写。
- 新增 RouteDefinition manifest，字段包含 method、ServeMux pattern、route group、auth mode、contract kind 和 operation ID。所有 API 路由通过同一个注册 helper 同时写入 ServeMux 和 manifest，测试不得维护第二份手写路由表。
- OpenAPI 比较只选择 contract kind=openapi 的 definition：规范化 /api/v1 前缀后与 OpenAPI server URL 对齐；根级 /mcp 使用独立 MCP contract 测试；health/ready 和 OpenAPI 文档入口按各自 server override 规则校验；SPA/static fallback 不参与 REST operation 比较。
- 新契约测试基于 manifest 比较实际注册 route 与 OpenAPI path/method，并用 httptest 响应执行 schema validation。verify-api-docs.sh 继续检查嵌入副本，但不再作为唯一证明。

## 9. 数据迁移分配

当前工作区已有 004、005，以及另一项并行工作尚未纳入版本控制的 006_standard_mcp.sql。下表只在 006 先落入目标分支并通过迁移升级测试后成立；这是 P1 当前编号的硬前置，不得把工作区未跟踪文件当作已交付依赖。满足前置后，为避免覆盖用户改动和四个执行计划争用同一文件，P1 从下一可用版本 007 开始预留：

| 迁移 | 职责 |
| --- | --- |
| 007_identity_security.sql | pending TOTP、session purpose/reauth、password_reset_required/totp_reset_required、身份/AI 凭证 epoch、补充 audit 字段和凭证撤销索引 |
| 008_file_operations_and_uploads.sql | file_operations、上传 lease/CAS、upload credential_generation、挂载规范化身份列和 mount_identity_claims |
| 009_share_tickets_and_identity.sql | 分享目标身份、share/share session/download ticket credential_generation、失效原因和 download tickets |
| 010_catalog_jobs.sql | last_seen_scan_id、job lease/token/heartbeat |
| 011_recovery_control.sql | recovery mode、恢复请求、备份清理状态和 route_groups.configured；reset flags 归属 007，不得重复增加 |

迁移发布规则：

1. expand 只增加 nullable 字段、新表和索引，旧代码仍能读取；006 硬前置和列归属先由迁移测试证明。
2. 在普通 HTTP 监听前完成 session purpose、route group configured 等回填和一致性检查；任何 active session NULL/未知状态或 route preference 冲突都 fail closed。挂载 claim 冲突按 6.4 标记相关挂载 unavailable、禁止其路由，并保留管理员诊断，不得任意选择一个继续 active。
3. 新代码切换后对未回填值 fail closed，并启用应用层状态机、CAS 和唯一 claim。
4. 至少跨一个稳定发布后，才用独立 SQLite table rebuild 清理旧 manifest、cursor、兼容列并增加 NOT NULL/CHECK 等硬约束。

若 006 没有先落地，先把共享 audit 列提取到公共迁移，让 Standard MCP 和 P1 都依赖它；若实施前已有其他迁移占用这些版本，五个文件整体顺延并保持内部顺序，不拆散依赖。每份执行计划开始时必须重新列出目标分支的已跟踪迁移，不能沿用本文编号猜测。

## 10. 实施顺序与并行边界

### 阶段 0：失败测试与加法迁移

先加入所有当前问题的最小复现、故障注入夹具、配置解析和五个加法迁移。此阶段不切换生产行为。

### 阶段 1：共享安全和存储基础

串行完成 HTTP trust boundary、transactional audit、storage no-replace/object identity、operation journal、lease/fencing helper。后续工作流依赖这些原语。

### 阶段 2：三个可并行功能包

- 身份 MFA、改密与禁用账号。
- Catalog scan epoch 与 Jobs lease。
- 路由组配置、PUID/PGID/UMASK 和 OpenAPI 契约测试骨架。

这些包修改不同模块和不同迁移文件，可以并行；合并时先运行迁移升级测试。

### 阶段 3：文件与分享状态机

上传、挂载、分享身份与 ticket 可在共享原语完成后并行。跨挂载 move 最后接入，因为它依赖 operation journal、对象身份、no-replace、Jobs recovery 和分享可靠撤销。

### 阶段 4：恢复流程

恢复依赖凭证撤销、ticket、upload 状态、job pause 和 audit 全部完成，因此最后切换。恢复演练通过前，旧原地 restore API 保持禁用或仅返回明确 unavailable，不能继续暴露不安全成功路径。

## 11. 验证门禁

每个执行计划都必须先写失败测试，再实现最小行为。最终发布至少包含：

### 身份与 HTTP

- 未登记 TOTP 管理员只能进入 enrollment。
- pending 不影响 active secret，confirm 原子 promote 并轮换 session。
- 旧 session 的 purpose 回填后不存在 active NULL/未知值；未登记 TOTP 管理员旧 session 已撤销，回填或验证失败阻止普通监听。
- 改密和禁用账号在撤销或 audit 失败时整体回滚。
- HTTPS 外部配置始终产生 Secure Cookie；伪造转发头无效。
- PublicURL/allowlist 空值、HTTP loopback、Origin 缺失或冲突、Host、CSRF、可信 client IP 和双维 limiter 的允许/拒绝矩阵。
- X-Forwarded-For 从右向左剥离可信代理；leftmost 伪造、畸形/超长链以及 Forwarded/XFF 冲突不得改变 client IP。
- recent reauth 只拦截路由矩阵中的 mutation；所有 GET/list 和普通成员文件操作不会因 reauthenticated_at 过期被阻断。
- 旧 runtime.env 升级时初始化 token 和 TOTP key 字节不变，只追加 audit key；第二次启动幂等，临时写入或 rename 失败保留原文件，旧二进制回滚后再次升级仍复用同一 audit key。

### 文件与分享

- 两个独立 Service 并发 part 不丢失；complete/cancel 只有一个终态。
- 在 DB 写入、rename、文件或目录 fsync 边界逐点注入失败；重启后 operation/upload 必须完成补偿或进入管理员可见的 recovery_required，过期临时目录可安全回收。
- Trash 同秒并发恢复和 rename 目标竞态不覆盖。
- 跨挂载每个状态点崩溃后可恢复，源替换和特殊文件不丢数据。
- preview/download 四组合、并发最后一次下载额度、同路径替换和撤销 DB 失败。
- 同一 ticket 的并发完整 GET、完成后重放、bytes=0- 重放和从精确 committed_offset 续传分别验证；仅后者可继续，任一时刻最多一个 stream owner。
- 并发挂载同根、父子根、同 inode 和 bind source 只有一个成功。

### Catalog、Jobs 与契约

- a/000..198 加根 a.txt 的 200 项跨批次复现通过。
- 扫描打开目录前后把目录替换为 symlink 或 magic link，不得索引挂载根外对象；禁止跨文件系统时不得越过 mount point。
- 删除、移动、中断、暂停、身份漂移和最终对账行为正确。
- 分享路径前缀包含百分号、下划线和反斜杠时，LIKE 查询必须按字面量匹配，只撤销目标及其真实后代。
- Worker 在 claim、批次提交、finalize 任一点崩溃后队列可继续。
- 实际 route 与 OpenAPI path/method 零漂移，真实响应通过 schema validation，生成前端类型零 diff。

### 恢复与部署

- 旧 schema staging migration、新 schema 拒绝、损坏备份拒绝。
- 恢复后所有 credential classes 失效、账号 reset flags 生效、外部挂载 disabled。
- 普通路由在 recovery mode 不可达；本地 finalize 不能直接置 ready，必须由新 normal 进程聚合恢复、数据库、身份、路由、HTTP 和审计门禁后才 ready。
- 路由迁移覆盖“旧实例仍保留 initialized=false 但已有账号”和“全新 accounts 空表”两类数据库：前者保留当前偏好并 configured=true，后者使用内建首次默认且 configured=false。
- 并发备份路径唯一；自定义 UID/GID、首次启动、重启和 UMASK 权限一致。

统一命令门禁：

- go test ./...
- go vet ./...
- web 目录 npm test -- --run
- web 目录 npm run build
- scripts/verification/verify-api-docs.sh
- scripts/verification/verify-scaffolding.sh
- scripts/verification/test-docker-entrypoint.sh
- git diff --check

Docker/Compose、目标 NAS、反向代理 HTTPS、浏览器分享与真实恢复演练必须在具备对应环境时执行，不能用本机静态脚本冒充完成。

## 12. 回滚策略

- 加法迁移不删除旧列；旧二进制可读取，但回滚会重新打开已修复的安全绕过，只允许作为短期可用性措施，并应在代理层关闭管理、分享、REST 和 MCP。
- 已撤销的 Token、分享和 session 不自动恢复；用户重新创建能力比复活旧凭证更安全。
- 文件 operation 和 upload 中间态不得通过删数据库行“回滚”；旧制品不能理解新状态时，保持服务关闭并使用新版本 recovery 工具完成。
- 路由 override 回滚时保留 SQLite 偏好；不得恢复“环境默认值回写数据库”的旧行为。
- 恢复流程一旦替换数据库，只能使用恢复前安全快照回退，不能继续在不一致数据库上提供普通服务。

## 13. 可观察性

- /readyz 在 recovery mode、migration failure或过期 job lease 无法回收时返回 not ready。单次关键 audit 写失败使当前 mutation 回滚并增加 audit_write_failures 健康计数与结构化告警，但不设置永久 readiness latch；底层数据库不可写时原有 DB readiness 检查自然失败。
- 管理诊断展示 pending file/upload operation、stale lease、cleanup_pending、最近失败原因和 recovery phase，不泄露宿主绝对路径或凭证。
- Audit 记录 request ID、route group、result 和 reason；日志使用相同 request ID 关联，但不记录密码、TOTP、Cookie、Bearer、fragment secret 或 ticket secret。
- 限流只记录进入新 cooldown、解除和聚合摘要，避免攻击者制造数据库写放大。

## 14. 文档同步范围

每个 P1 完成时同步：

- docs/security/security-model.md
- docs/design/architecture.md
- docs/design/domain-model.md
- docs/verification/acceptance-criteria.md
- docs/api/README.md
- docs/mcp/README.md
- openapi/omnora.v1.yaml 与嵌入副本
- 受影响的部署文档、env 示例和 Web 文案

## 15. 用户评审后续

本设计经用户确认后，生成四份执行计划：

1. p1-identity-http-security
2. p1-files-uploads-shares
3. p1-backup-recovery-deployment
4. p1-catalog-jobs-api-contracts

每份计划使用 TDD 小步任务、精确文件路径、测试命令和验收结果，并明确跨计划依赖。计划中的任何 commit 步骤都必须标记为“仅在用户明确授权后执行”。
