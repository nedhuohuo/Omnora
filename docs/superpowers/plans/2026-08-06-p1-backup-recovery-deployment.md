# Omnora P1 备份恢复与部署执行计划

> **执行规则：** 每个任务先加入失败测试，再写最小实现。本文只描述实施步骤，不授权提交 Git；任何 commit 必须另行得到用户明确授权。

## 目标

把备份、恢复、路由暴露面和容器运行身份收敛为可演练的 fail-closed 流程：恢复在离线 staging 完成并通过完整性/schema 校验后才替换正式数据库；替换后旧凭证、分享、上传和外部挂载不会继续有效；路由配置不会被 Compose 默认值覆盖；只读根文件系统下可用可配置 UID/GID/UMASK 启动。

## 依赖与边界

- 依赖身份计划提供 `credential_generation`、reset flags、session/Token/share 撤销事务和审计；依赖文件计划提供 upload/share-ticket 终态撤销；依赖 Catalog 计划提供 job pause/resume。
- 当前工作区的 `006_standard_mcp.sql` 尚未纳入版本控制。执行前必须确认 `006` 已跟踪，且 `007_identity_security.sql`、`008_file_operations_and_uploads.sql`、`009_share_tickets_and_identity.sql`、`010_catalog_jobs.sql` 已按整组顺序落地并通过从旧库升级的测试；否则先拆出公共 audit expand 迁移并整体重排 007–011，不能孤立创建 011。
- 保持单 Go 进程、SQLite WAL 和现有 Compose/反向代理边界；不引入 PostgreSQL、Redis 或常驻恢复服务。
- 任何普通 API 在 recovery mode 下都不可达；finalize 只有本地 recovery CLI/受保护 Unix 或 loopback 通道可执行，绝不注册为公共 HTTP 路由。

## Task 1：先锁定恢复状态机与迁移

**文件：**

- 新建：`internal/store/migrations/011_recovery_control.sql`
- 新建：`internal/recovery/types.go`
- 新建：`internal/recovery/coordinator.go`
- 新建：`internal/recovery/coordinator_test.go`
- 修改：`internal/store/sqlite.go`
- 修改：`internal/server/server.go`
- 测试：`internal/server/server_test.go`、`internal/server/release_gate_test.go`

**步骤：**

- [ ] 先写迁移升级失败测试：低版本数据库能增加 recovery mode、restore request、backup cleanup 状态；重复应用幂等；高于当前版本的 schema 直接拒绝。
- [ ] 定义状态 `normal → preparing → recovery_required → restoring → finalize_required → normal_pending_bootstrap → normal`，并规定每个状态允许的进程入口；`ready` 是聚合健康结果，不是持久状态。数据库/manifest 不一致只能进入 `recovery_required`，不能删除记录伪造完成。
- [ ] 迁移只做 expand：增加 recovery 状态、请求 ID、staging path、source schema version、safe snapshot path、requested/completed timestamps、reason code 和 cleanup_pending；旧二进制仍可打开数据库但不得在 recovery mode 提供普通业务路由。
- [ ] `RecoveryCoordinator` 以数据库状态为权威，启动时在注册普通路由前读取并校验状态；`preparing`、`recovery_required`、`restoring`、`finalize_required` 进程只注册 health/ready，不注册公共业务或 finalize HTTP 路由。`normal_pending_bootstrap` 只允许登录、携带 `newPassword` 的密码重置登录、管理员 TOTP enrollment/confirm 和 logout，且 `/readyz` 固定 503。`/readyz` 必须聚合 recovery normal、数据库迁移/invariant、session-purpose rollout、route hydration、HTTP security configuration 和 audit health，任一未通过都返回 503。
- [ ] 为每次状态迁移写同事务 audit；audit 失败回滚状态变更，不能把恢复标记写成成功后再补日志。

**验证命令：**

```bash
go test ./internal/store ./internal/recovery ./internal/server -run 'Test(Recovery|Migration|Ready|Server)'
```

## Task 2：唯一备份文件、完整性校验和可见失败

**文件：**

- 修改：`internal/store/backup.go`
- 修改：`internal/store/backup_test.go`
- 修改：`internal/server/admin_control.go`
- 修改：`internal/server/backup_network_test.go`
- 修改：`internal/store/migrations/011_recovery_control.sql`

**步骤：**

- [ ] 先写并发创建备份测试：同一秒、同一操作者和重启后生成的文件名都不冲突；文件使用随机/单调 backup ID，落盘模式 0600，目录 0700。
- [ ] 使用 `*.tmp.<backupID>` 独占创建临时文件，Online Backup 完成后执行 integrity check、fsync 文件和目录，再以 no-replace rename 发布；任何阶段失败都保留结构化 `failed`/`cleanup_pending` 记录。
- [ ] `backups.path` 只在发布成功后写入；数据库记录与文件不一致时列表明确显示 failed/missing，不能静默返回 completed。
- [ ] 保留 `OpenSQLiteReadonly` 的 schema version、integrity check 和 `PRAGMA foreign_key_check` 校验；不允许从正式 DB 进程内直接覆盖自身文件。
- [ ] 增加失败注入点：临时文件创建、backup step、文件/目录 fsync、rename、DB insert、cleanup，每个边界都有可重试或人工诊断结果。

## Task 3：两阶段离线恢复与安全快照

**文件：**

- 新建：`internal/recovery/restore.go`
- 新建：`internal/recovery/restore_test.go`
- 新建：`cmd/omnora-recovery/main.go`
- 修改：`internal/server/admin_control.go`
- 修改：`internal/server/api.go`
- 修改：`internal/server/server.go`
- 修改：`internal/store/backup.go`
- 测试：`internal/server/backup_network_test.go`、`internal/server/release_gate_test.go`
- 文档：`docs/deployment/reinstall-data-continuity.md`、`docs/verification/acceptance-criteria.md`

**步骤：**

- [ ] 先写 HTTP 测试：`POST /api/v1/admin/backups/{backupId}/restore` 只创建 restore request 并进入 `preparing`，不能在仍监听普通业务路由的进程内直接替换数据库；重复请求返回当前 request 状态。
- [ ] 请求事务在响应前写入 `preparing`、`ready=false`、request ID 并暂停 job maintenance；服务刷出 HTTP 202 后通过受控 supervisor exit 立即退出。测试必须证明提交后旧进程不再处理普通请求，新进程在路由注册前进入 recovery-only。
- [ ] recovery coordinator 在监听前复制目标快照到唯一 staging 目录，检查文件权限、SQLite integrity、foreign keys 和 schema version；schema 高于当前版本或损坏快照拒绝并保留原 DB。
- [ ] staging 上执行向前迁移和 manifest/importer 校验，成功后创建恢复前安全快照；正式替换使用同一文件系统上的原子 rename，并保留可回滚的 safe snapshot。
- [ ] 替换后在单一事务中提升 `system_state.credential_generation`；身份计划在签发/验证 identity session、browser session 和 AI Token 时比较该 epoch，文件计划在 share/session、download ticket、upload session 上执行同样校验。恢复事务仍显式撤销每个 credential class，generation 只是防漏撤销的第二道校验。
- [ ] 同一事务清除恢复出的 active/pending TOTP、设置 `password_reset_required=1`，管理员设置 `totp_reset_required=1`，所有外部挂载设为 disabled。身份计划的登录流程要求 valid old password + newPassword 才清除 password flag；管理员随后只能进入 enrollment，旧 TOTP 永远不能认证。
- [ ] `omnora-recovery finalize --request <id>` 只能从本地 CLI/Unix 或 loopback 受保护通道执行：完成 staging/safe-snapshot 清理和 metadata 校验后将状态置为 `normal_pending_bootstrap`，然后退出。新的 bootstrap 进程只在聚合恢复、迁移/invariant、session-purpose、route hydration、HTTP security 和 audit 门禁通过后完成管理员密码/TOTP enrollment，并在同一事务中切到 `normal` 后退出；后续普通进程才开始监听。finalize 本身不能直接置 `normal` 或 `ready`。
- [ ] recovery CLI 不接受公网请求，不输出凭证/数据库内容；request、staging、safe snapshot 和日志使用 0600/0700 与唯一 ID。
- [ ] 加入故障注入：复制中断、迁移失败、校验失败、替换前进程退出、替换后审计/撤销失败、清理失败。每种情况都必须保留原 DB 或进入 `recovery_required`，禁止半替换继续服务。

**验证命令：**

```bash
go test ./internal/recovery ./internal/store ./internal/server -run 'Test(Backup|Restore|Recovery|ReleaseGate)'
go run ./cmd/omnora-recovery --help
```

## Task 4：路由组的三层配置与迁移判据

**文件：**

- 修改：`internal/store/migrations/011_recovery_control.sql`
- 修改：`internal/server/route_groups.go`
- 修改：`internal/server/server.go`
- 修改：`internal/server/route_groups_test.go`
- 修改：`internal/server/server_test.go`
- 修改：`deploy/docker-compose.yml`
- 修改：`deploy/docker-compose.nas.yml`
- 修改：`deploy/aliyun-test.env.example`
- 文档：`docs/deployment/aliyun-test-server.md`、`docs/design/architecture.md`

**步骤：**

- [ ] 先写两类迁移测试：全新 `accounts` 空表的六组路由为 `configured=false` 并使用内建首次默认；旧库即使 `initialized=false` 仍存在未删除账号时，当前六组偏好被标记 `configured=true` 且不改变可达性。
- [ ] 移除 Compose/Dockerfile 中六个非空路由环境默认值；只有显式 override 才锁定 effective 状态，不把 override 写回 SQLite。保留环境变量名称兼容，但空值代表未设置。
- [ ] `hydrateRouteGroups` 任意查询/持久化错误返回 `NewServer` error，命令入口在注册普通路由前退出；不能回退到扩大暴露面的内建值。
- [ ] RouteGroup DTO 返回 `source`、`locked`、`configured`；管理员 PATCH 在环境锁定时返回 409，取消 override 后恢复已保存的 SQLite 偏好。
- [ ] 默认矩阵固定为 `admin_web=true`、`member_web=true`、`share=false`、`rest=true`、`mcp=false`、`openapi=true`，并在 API、Web 和 release gate 中使用同一矩阵。

## Task 5：PUID/PGID/UMASK 与只读根文件系统启动

**文件：**

- 修改：`deploy/docker-entrypoint.sh`
- 修改：`deploy/docker-compose.yml`
- 修改：`deploy/docker-compose.nas.yml`
- 修改：`deploy/aliyun-test.env.example`
- 修改：`scripts/verification/test-docker-entrypoint.sh`
- 修改：`docs/superpowers/specs/2026-08-02-omnora-project-foundation-design.md`
- 修改：`docs/deployment/reinstall-data-continuity.md`

**步骤：**

- [ ] 先写 shell 测试覆盖正整数 PUID/PGID、三或四位八进制 UMASK、空值和超范围拒绝；错误配置在任何 `chown` 或应用启动前 fail closed。
- [ ] root bootstrap 只使用数字 `PUID:PGID` 调用 `su-exec`，不修改只读根文件系统的 `/etc/passwd`/`/etc/group`；Compose 继续只授予 `CHOWN, SETUID, SETGID`。
- [ ] 检查 config/data/managed/predeclared root 的所有权；不对已有 NAS 做无界递归 chown。根身份不匹配时打印迁移指引并退出。
- [ ] 统一在 exec 应用前应用 UMASK；runtime.env、instance marker 和目录权限保持 0600/0700；旧 runtime.env 升级只追加 audit key，不旋转初始化/TOTP key。
- [ ] 测试 root bootstrap、非 root 直接启动、自定义 UID/GID、重启、只读根、外部挂载不可 chown 和已有数据所有权冲突，且 stderr 不泄露 secret。

**验证命令：**

```bash
sh scripts/verification/test-docker-entrypoint.sh
docker compose -f deploy/docker-compose.yml config
docker compose -f deploy/docker-compose.nas.yml config
```

## Task 6：恢复与部署演练门禁

**文件：**

- 修改：`scripts/verification/release-gate.sh`
- 修改：`scripts/verification/release-readiness-checklist.md`
- 修改：`scripts/verification/verify-deployed-http.sh`
- 修改：`docs/deployment/reinstall-data-continuity.md`
- 修改：`docs/verification/acceptance-criteria.md`

**步骤：**

- [ ] 将 006→011 整链 migration upgrade、未知 schema 拒绝、损坏备份拒绝、recovery-only 路由不可达、受控退出、finalize 后新进程聚合 ready、旧凭证全部失效加入 release gate。
- [ ] 在临时 Compose 环境执行：创建账号/空间/挂载/分享/Token/upload，生成备份，写入新数据，再恢复旧快照，逐项确认旧能力失效、账号 reset flags 生效、外部挂载 disabled。
- [ ] 做一次容器重建/重装演练：保留 config、data、managed 和相同 NAS 容器路径，确认旧文件可浏览和下载；更换路径或来源 identity 时确认 mount unavailable。
- [ ] 反向代理环境验证 `/healthz`、`/readyz`、普通路由、Range 下载和 MCP 长连接；恢复期间普通业务请求必须拒绝。
- [ ] 记录 backup ID、restore request ID、container path、mount identity、request ID 和 result；证据中不得包含密码、Token、Cookie、分享密钥或绝对宿主敏感路径。

## 完成门槛

- `go test ./internal/store ./internal/recovery ./internal/server` 通过；若存在环境限制，必须把失败归因与已执行替代测试写入 release evidence。
- `sh scripts/verification/test-docker-entrypoint.sh`、Compose config、OpenAPI/route gate 和 migration upgrade 全部通过。
- 恢复演练至少覆盖正常、损坏快照、未知 schema、替换前退出、替换后撤销失败和清理失败六类故障。
- 只有新 normal 进程聚合通过 recovery、DB migration/invariant、session-purpose、route hydration、HTTP security 和 audit health，且 `/readyz` ready、恢复前安全快照可读、所有 credential classes 已失效、外部挂载已 disabled、审计可关联时，才允许发布。

## 回滚与风险

- 恢复切换失败时只回到恢复前 safe snapshot；不删除原 DB，不自动复活旧凭证。
- 旧二进制不能理解 recovery 状态时保持普通服务关闭，由新版本 recovery CLI 完成 finalize。
- 路由偏好迁移失败时保持普通监听关闭，禁止用环境默认值覆盖数据库偏好。
- UID/GID 变更不会自动递归修改大型 NAS；由运维根据明确迁移指引修复所有权后重试。
