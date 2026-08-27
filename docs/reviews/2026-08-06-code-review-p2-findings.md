# Omnora P2 Code Review Findings

日期：2026-08-06

状态：已确认问题清单，待排期

来源：当前分支按身份权限、文件分享、Web/API、部署恢复四个功能域完成的只读代码 Review

## 1. 目的

本文集中保存本轮 Review 中确认的 P2 问题、证据、修复方向和验收口径。P2 表示问题不会阻止当前修复 P1 主链，但会影响长期安全性、正确性、可维护性或用户体验，应进入后续迭代而不是遗忘。

与 P1 共用基础设施的项目仍保留在本文中，并标记为“随 P1 收敛”。实施 P1 时若已经完整覆盖对应验收标准，可以直接关闭该 P2，不再重复开发。

P1 总体方案见 [P1 Remediation Design](../superpowers/specs/2026-08-06-p1-remediation-design.md)。

## 2. 状态约定

| 状态 | 含义 |
| --- | --- |
| 独立排期 | 不依赖 P1 实现即可单独修复 |
| 随 P1 收敛 | P1 计划将建立所需基础设施，必须同时覆盖本项验收 |
| 产品确认 | 技术事实已确认，但期望行为需要产品决定 |
| 待实机验证 | 代码不足以证明缺陷，不能按已确认问题实施 |

## 3. 身份、会话与权限

### P2-SEC-01：会话没有 idle timeout

- 状态：独立排期。
- 证据：[internal/identity/service.go](../../internal/identity/service.go) 会更新 last_used_at，但会话验证只检查绝对 expires_at 和 revoked_at。
- 影响：持续窃取或遗留的浏览器会话在八小时绝对有效期内不会因长期空闲失效。
- 修复方向：增加可配置 idle TTL；验证会话时同时检查 expires_at 与 last_used_at，并用原子 UPDATE 刷新最后使用时间。
- 验收：空闲超过阈值的会话返回 401；活跃会话不延长绝对过期时间；并发请求不能让已过期会话复活。

### P2-SEC-02：AI Token spaces.list 返回已经失效的边界元数据

- 状态：独立排期。
- 证据：[internal/server/api.go](../../internal/server/api.go) 的 spaces.list 直接返回 Token 创建时持久化的 boundaries；search/read 路径会重新检查当前 ACL 和挂载状态。
- 影响：撤销成员 ACL 或停用挂载后，Token 仍能枚举旧 space、mount 和 relative path 元数据。
- 修复方向：spaces.list 对每个 boundary 执行与实际文件访问相同的当前 ACL、space、mount 和 mount identity 校验，只返回当前有效项；全部失效时返回空数组。search/read 对单次目标访问继续 fail closed。
- 验收：撤销 ACL、禁用空间或挂载后，旧边界不再出现在响应中。

### P2-SEC-03：未知 role 被静默降级为 member

- 状态：独立排期。
- 证据：[internal/server/admin_control.go](../../internal/server/admin_control.go) 只在 role 等于 admin 时设置管理员，其余任意字符串都进入 member 分支。
- 影响：调用方拼写错误不会得到 400，实际创建的账号权限与请求意图不一致。
- 修复方向：请求 DTO 使用 admin/member 枚举；为了兼容当前 Web，字段缺失或空字符串继续默认为 member，非空未知值返回 invalid_input；OpenAPI 与前端类型同步。
- 验收：admin、member、缺失和空字符串按上述规则成功；非空未知值稳定返回 400 且不创建账号。

### P2-SEC-04：初始管理员 ACL PUT 的保护范围可能过宽

- 状态：产品确认。
- 证据：[internal/server/admin_control.go](../../internal/server/admin_control.go) 对初始管理员的任何空间成员 PUT 都返回 initial_admin_protected，包括新增 manager 和 manager 的幂等更新。
- 影响：实现强于安全模型“禁止降级或移除”的文字边界，可能阻止把初始管理员加入新的共享空间。
- 修复方向：产品选择“完全不可修改”或“仅禁止降级和移除”；确认后同步后端、UI、OpenAPI 和回归测试。
- 验收：所有允许和拒绝的矩阵都有明确测试，不依赖调用者猜测。

### P2-SEC-05：账号密码和分享密码仍使用 PBKDF2

- 状态：独立排期。
- 证据：[internal/identity/crypto.go](../../internal/identity/crypto.go) 和 [internal/share/crypto.go](../../internal/share/crypto.go) 使用 pbkdf2-sha256；[安全模型](../security/security-model.md) 指定 Argon2id。
- 影响：当前迭代次数并非快速明文哈希，但没有满足已确认的内存困难哈希基线，也没有 NAS 参数标定数据。
- 修复方向：引入带算法前缀和参数的 Argon2id；成功登录或分享密码验证后渐进升级旧 PBKDF2 hash，不做一次性明文迁移。
- 验收：旧 hash 可验证并自动升级；新 hash 参数可配置并在目标 NAS 完成耗时、内存标定；错误密码保持常量时间比较边界。

## 4. 文件、路径与回收站

### P2-FILE-01：Catalog 遍历存在 symlink TOCTOU

- 状态：随 P1 收敛。
- 证据：[internal/catalog/service.go](../../internal/catalog/service.go) 使用普通路径 ReadDir、Lstat 和稍后的递归读取，目录可在检查后被替换成 symlink。
- 影响：可写挂载中的竞态可能让索引读取根目录外的文件元数据。
- 修复方向：复用 P1 的安全 walker；Linux 使用 openat2 配合 RESOLVE_BENEATH、RESOLVE_NO_MAGICLINKS、RESOLVE_NO_SYMLINKS，fallback 逐组件使用目录 FD、openat、O_NOFOLLOW 和对象类型校验。裸 os.Root 不能单独满足策略。
- 验收：在检查和递归之间把目录替换为 symlink 或 magic link，扫描不得越出挂载根，也不得跨越禁止的文件系统边界。

### P2-FILE-02：管理员宿主目录浏览只检查最终路径组件

- 状态：独立排期。
- 证据：[internal/server/hostdirs.go](../../internal/server/hostdirs.go) 对最终目录执行 Lstat，未逐组件拒绝中间 symlink。
- 影响：allowed/link 指向根外目录时，请求 allowed/link/subdir 可以浏览允许根之外的目录。
- 修复方向：复用 P1 建立的 descriptor-relative walker；Linux 优先 openat2，fallback 逐组件使用目录 FD、openat、O_NOFOLLOW 和对象类型校验，不能退化为仅检查最终组件。
- 验收：任意中间组件为 symlink 时返回稳定错误，普通嵌套目录仍可浏览。

### P2-FILE-03：Rename 的 no-overwrite 是 check-then-rename

- 状态：随 P1 收敛。
- 证据：[internal/files/service.go](../../internal/files/service.go) 先 Lstat 目标不存在，再调用 Rename。
- 影响：检查后并发创建目标时，Unix rename 可以覆盖新目标，违背“不覆盖”契约。
- 修复方向：P1 共享存储原语提供 RenameNoReplace；Linux 使用 renameat2(RENAME_NOREPLACE)，不可靠平台明确返回不支持。
- 验收：并发创建目标时，源或目标都不能被静默覆盖。

### P2-FILE-04：非法目标目录被静默解释成挂载根

- 状态：独立排期。
- 证据：[internal/files/service.go](../../internal/files/service.go) 的 mustCleanDir 在 CleanRelativePath 失败时返回点目录。
- 影响：../../x 等非法输入可能把移动目标变成根目录，而不是返回错误。
- 修复方向：改为返回 (string, error)，所有移动和跨挂载调用点传播 invalid_path。
- 验收：绝对路径、父目录跳转、保留路径都返回 400，文件系统无变化。

### P2-FILE-05：EmptyTrash 隐藏部分删除失败

- 状态：独立排期。
- 证据：[internal/files/trash_crossmount.go](../../internal/files/trash_crossmount.go) 丢弃 RemoveAll 错误，只返回成功数量。
- 影响：API 返回成功时仍可能有文件残留，用户无法知道失败对象。
- 修复方向：返回成功数、失败 trash ID 和聚合错误；API 使用部分成功或冲突响应，不宣称全部清空。
- 验收：注入权限错误后响应可定位失败项，成功项不重复处理，重试幂等。

### P2-FILE-06：分享撤销 LIKE 模式没有转义百分号和下划线

- 状态：随 P1 收敛。
- 证据：[internal/server/file_ops.go](../../internal/server/file_ops.go) 直接拼接 cleaned + /%，合法文件名中的 % 和 _ 会成为 SQL 通配符。
- 影响：删除一个路径可能撤销无关分享。
- 修复方向：P1 可靠撤销查询统一使用 escapeLike 和 ESCAPE 子句。
- 验收：路径含 %、_、反斜线时只撤销目标及真实子项。

## 5. 上传与临时资源

### P2-UPLOAD-01：上传 DB/文件系统状态缺少补偿和物理 GC

- 状态：随 P1 收敛。
- 证据：[internal/server/api.go](../../internal/server/api.go) 在创建、part、complete、cancel 多处忽略数据库或文件系统错误；[internal/server/index_worker.go](../../internal/server/index_worker.go) 过期处理只改数据库状态。
- 影响：可能产生孤儿 session、错误的 completed 状态和长期占盘的分片目录。
- 修复方向：P1 上传状态机以数据库为权威，使用 lease、CAS、cleanup_pending 和恢复任务。
- 验收：在 DB、rename、fsync 各边界注入失败，重启后均能完成补偿或进入可见失败状态。

### P2-UPLOAD-02：没有服务端空间配额预占

- 状态：独立排期，可复用 P1 上传状态机。
- 证据：[internal/transfer/service.go](../../internal/transfer/service.go) 只拒绝负 expected size，客户端可以声明任意大值；OpenAPI 已出现 quota_exceeded 语义但没有实现。
- 影响：有 editor 权限的用户可以写满 managed storage 或临时分片空间。
- 修复方向：创建 session 时原子预占 declared_size；cancel、expire、failed 释放；complete 转为实际占用；外部挂载执行磁盘安全水位检查。
- 验收：并发创建 session 不能共同超额；所有终态和 GC 都准确释放预占。

## 6. 分享与流式传输

### P2-SHARE-01：单一全局分享 Cookie 不支持多个分享标签页

- 状态：独立排期。
- 证据：[internal/server/api.go](../../internal/server/api.go) 固定使用 omnora_share_session 且 Path=/。
- 影响：P1 修复 fragment 优先级后，新链接可以正确打开，但分享 B 仍会覆盖分享 A 的 Cookie，旧标签页随后失效或错绑。
- 修复方向：请求显式绑定 public share handle，并使用 share-scoped session；不能由“最后写入的全局 Cookie”隐式决定 principal。
- 验收：同一浏览器同时打开 A、B，两页刷新、预览、下载均保持各自身份。

### P2-SHARE-02：Range 流式复制错误被忽略

- 状态：独立排期。
- 证据：[internal/server/api.go](../../internal/server/api.go) 对 io.CopyN 返回值使用空白标识符。
- 影响：底层 I/O 中断只表现为截断的 206 响应，没有日志、指标或审计关联。
- 修复方向：检查复制错误，记录 request ID、mount、对象和已发送字节；响应头已发送后终止连接，不再伪装完整成功。
- 验收：故障注入能生成结构化中断事件，客户端得到截断失败且可安全重试。

## 7. Catalog 调度

### P2-INDEX-01：完整扫描每 15 分钟重复调度

- 状态：独立排期，可复用 P1 scan epoch。
- 证据：[internal/server/index_worker.go](../../internal/server/index_worker.go) 每次维护都为没有活跃任务的 indexed mount 入队，默认维护间隔 15 分钟；[技术架构](../design/architecture.md) 要求完整对账结束后至少 24 小时。
- 影响：大 NAS 会持续全盘扫描，造成 I/O、CPU 和磁盘缓存压力。
- 修复方向：持久化 last_completed_scan_at、scan kind 和 next_due_at；调度器只在完整对账完成至少 24 小时后入队。
- 验收：完成扫描后的 24 小时内不会重复创建 full reconciliation；增量任务不被错误阻止。

## 8. Web 与契约体验

### P2-WEB-01：分享密码错误后表单不可重试

- 状态：独立排期。
- 证据：[web/src/SharePortalApp.tsx](../../web/src/SharePortalApp.tsx) 声明 passwordError 但从未赋值，失败后直接进入 unavailable。
- 影响：输错一次密码后必须刷新页面才能重试。
- 修复方向：服务端继续使用通用 share_unavailable 防枚举；前端在已进入密码流程时保留表单并显示通用错误，429 单独提示等待。
- 验收：错误密码、正确重试、链接失效、429 四种状态均可恢复且不泄露分享是否存在。

### P2-WEB-02：分享过期时间文案与后端七天默认值冲突

- 状态：产品确认。
- 证据：[web/src/member/MemberSharesPanel.tsx](../../web/src/member/MemberSharesPanel.tsx) 空值不发送 expiresAt，文案写“永不过期”；[internal/server/api.go](../../internal/server/api.go) 空值固定为当前时间加七天。
- 影响：用户创建出的链接比界面承诺更早失效。
- 修复方向：首选把文案改为“空值默认七天”；如果产品确认永久分享，再引入 nullable 数据模型和生命周期策略。
- 验收：UI、OpenAPI、后端默认和列表展示一致。

### P2-WEB-03：UI 暴露未实现的 uploads:create 与标准 MCP 描述

- 状态：独立排期。
- 证据：[web/src/member/MemberTokensPanel.tsx](../../web/src/member/MemberTokensPanel.tsx) 可选择 uploads:create；本轮 Review 基线中的 MCP handler 只有读取类方法。当前工作区另有未完成的 Standard MCP 改动正在新增 scope 和迁移，不能在端到端 handler、授权和测试完成前视为能力已交付。
- 影响：用户可以创建没有可调用能力的 Token，并误认为当前接口是完整 Streamable HTTP MCP。
- 修复方向：实现前隐藏或禁用 scope；文案明确当前 JSON adapter 状态。
- 验收：UI 只显示后端真实支持且 OpenAPI/MCP 文档已定义的能力。

### P2-WEB-04：资源切换请求可能被旧响应覆盖

- 状态：独立排期。
- 证据：[web/src/member/MemberFilesApp.tsx](../../web/src/member/MemberFilesApp.tsx)、[web/src/member/AdminPanels.tsx](../../web/src/member/AdminPanels.tsx) 和 MemberSharesPanel 的空间切换请求没有统一 abort 或 request version。
- 影响：快速切换空间时可能在新页面显示旧空间的挂载或成员，并继续执行错误目标操作。
- 修复方向：复用 ShareTargetPicker 的 AbortController 模式；只有当前 request ID 可以写状态。
- 验收：人为延迟旧请求后快速切换，旧响应不得覆盖当前选择。

### P2-API-01：非核心管理 DTO 仍缺少生成类型和运行时契约测试

- 状态：独立排期，承接 P1 核心 OpenAPI 重建。
- 证据：当前 verify-api-docs.sh 主要检查关键词、状态码和嵌入副本，无法发现 handler DTO 与 schema 漂移。
- 影响：P1 修完高风险核心接口后，剩余后台接口仍可能再次漂移。
- 修复方向：逐域替换匿名 DTO，生成 TypeScript 类型，并把真实 httptest 响应纳入 schema 校验。
- 验收：每个剩余活动 operation 都使用命名 schema 并生成 TypeScript 类型；实际注册 route 与 OpenAPI path/method 零漂移；每个 operation 的成功响应和主要错误响应都用真实 httptest 样例通过 schema validation；生成类型执行后保持零 diff。

### P2-TEST-01：Web 关键用户流程缺少组件和浏览器回归

- 状态：独立排期。
- 证据：当前 Web 只有 4 个测试文件、12 个 helper 测试，主要大组件和 API 客户端没有状态流测试。
- 影响：分享门户、空间切换、路由开关和账号安全问题可以在构建全绿时继续存在。
- 修复方向：增加 React Testing Library 组件测试和少量 Playwright 生产构建 smoke，不追求截图型脆弱测试。
- 验收：覆盖分享 fragment/Cookie、密码重试、路由 override 展示、空间快速切换和 PDF 预览 smoke。

## 9. 未纳入 P2 的项目

- PDF blob iframe 是否被当前 CSP 阻止：代码不足以确认。必须在目标 Chrome/Safari/Firefox 查看控制台和网络行为后再分级。
- 非法百分号编码 fragment 导致 loading：本轮评为 P3，应作为小型健壮性修复处理。
- P1 已确认问题不在本文重复展开，统一由总体技术设计和后续四份执行计划管理。

## 10. 关闭条件

关闭任一 P2 前必须同时满足：

1. 有能复现原问题的失败测试或可重复手工脚本。
2. 修复后目标测试、相关功能域测试和全量测试通过。
3. 受影响的 OpenAPI、Web 文案、安全模型或验收标准已同步。
4. 没有通过吞错、隐藏按钮或放宽安全边界来规避问题。
5. 若由 P1 顺带收敛，P1 验收中必须显式包含该 P2 的断言。
