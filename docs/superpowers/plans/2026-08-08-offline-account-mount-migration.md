# 账号—挂载模型离线迁移设施 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不添加破坏性 `013` 的前提下，交付普通启动迁移门禁、三卷实例身份、完整回滚包和 journal 保护的 staging 数据库离线迁移协调器，使后续账号—挂载迁移不会在正式数据库上被自动执行且具备完整回滚条件。

**Architecture:** `internal/store` 将内嵌迁移的目录发现、显式 catalog 和执行策略分离；普通打开只执行 online-safe，既有库遇 offline migration 在任何待执行 SQL 之前失败。`internal/instanceid` 统一校验 config/data/managed marker。新的 `internal/offlinemigration` 负责锁、回滚包、journal、staging、验证和原子替换，现有 recovery 只复用 SQLite Online Backup、snapshot 校验与 fsync 原语，不伪造 restore request。

**Tech Stack:** Go 1.26、SQLite（`modernc.org/sqlite`）、标准库 `io/fs`/`fstest`/`syscall`、POSIX shell、现有 `internal/store`、`internal/recovery`、`internal/mountid`。

**Authoritative design:** [账号—挂载模型离线迁移设计](../specs/2026-08-08-offline-account-mount-migration-design.md)

**Working-tree rule:** 当前工作区有大量未提交的 MCP、文件、Web 和文档改动，且 `internal/store/migrations/012_upload_target_identity.sql` 是已有未跟踪文件。本计划不得重写、改名或删除这些改动；只修改任务列出的文件。不提交 Git，除非用户另行明确要求。

**Migration allocation:** `012` 已被当前工作区占用。本计划不创建 `013`。迁移 catalog 必须把当前 001–012 精确登记为 `online_safe`；未来添加 `013` 时再单独登记为 `offline_required`。

---

### Task 1: 显式迁移 catalog 与普通启动门禁

**Files:**
- Create: `internal/store/migrations.go`
- Create: `internal/store/migrations_test.go`
- Modify: `internal/store/sqlite.go`
- Modify: `internal/store/sqlite_test.go`

- [x] **Step 1: 写 catalog 完整性和策略 RED 测试**

在 `migrations_test.go` 使用 `testing/fstest.MapFS` 构造小型迁移集合，覆盖：

1. 每个 SQL 文件必须有精确的版本、文件名和类别登记；多登记、漏登记、重复版本、重复名称、unknown class 全部拒绝。
2. 已有 v1 数据库面对 pending online v2 + offline v3 时，`apply_online_safe` 返回 `ErrOfflineMigrationRequired`，且 v2/v3 的 SQL 和 migration row 都没有执行。
3. 同一数据库使用 `apply_offline` 时按版本执行 v2/v3。
4. 真正空数据库使用 `apply_online_safe` 时允许执行完整 catalog。
5. 已有但业务行为空的数据库不算 fresh。
6. `validate_only` 只校验历史，不执行 pending SQL。
7. 历史 migration name 漂移、数据库未来版本和内嵌文件漂移全部 fail-closed。

错误断言使用 `errors.Is(err, ErrOfflineMigrationRequired)`，同时检查结构化错误携带当前版本和首个待执行离线迁移，不能依赖错误字符串解析。

- [x] **Step 2: 运行 focused test 并确认 RED**

Run:

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/store -run 'TestMigrationCatalog|TestMigrationPolicy'
```

Expected: FAIL，因为迁移 catalog、策略和 sentinel 尚不存在。

- [x] **Step 3: 实现最小迁移模型**

在 `migrations.go` 定义包内模型：

```go
type migrationClass string

const (
	migrationOnlineSafe      migrationClass = "online_safe"
	migrationOfflineRequired migrationClass = "offline_required"
)

type migrationPolicy uint8

const (
	migrationValidateOnly migrationPolicy = iota
	migrationApplyOnlineSafe
	migrationApplyOffline
)

type migrationDescriptor struct {
	Version int
	Name    string
	Class   migrationClass
}

var ErrOfflineMigrationRequired = errors.New("offline migration required")
```

增加实现 `Is` 的结构化 `OfflineMigrationRequiredError`，并为 production catalog 精确登记 001–012。catalog 验证必须对 embedded FS 做双向一一对应校验，且在打开事务执行 SQL 前完成。

- [x] **Step 4: 重构 migrate 为“发现 → 校验 → preflight → 执行”**

把 `sqlite.go` 中当前单体 `migrate` 拆成可注入 `fs.FS + []migrationDescriptor + migrationPolicy` 的包内 runner：

1. 打开连接后先判断打开前是否 truly fresh；判断依据是 `sqlite_schema` 中没有任何非 `sqlite_%` 对象，不能用业务表是否有行来推断。
2. fresh database 才创建 `schema_migrations` 并允许完整 catalog。
3. existing database 必须已经有 `schema_migrations`；读取全部历史并校验未来版本与名称。
4. 先计算完整 pending 列表。online policy 只要包含 offline migration 就直接返回 sentinel，不执行任何 pending SQL。
5. validate-only 不执行任何 pending SQL。
6. 获准执行时仍保持“每个迁移一个事务”的现有行为。

`OpenSQLite` 固定使用 `apply_online_safe`。Task 1 只增加包私有 `openSQLiteForOfflineMigration`，避免在协调器门禁完成前暴露可对 live DB 调用的旁路；Task 5 再通过受控 staging 协调器集成。另增加只读且校验迁移历史的 `OpenSQLiteValidatedReadonly`。

- [x] **Step 5: 运行迁移测试并确认 GREEN**

Run:

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/store
```

Expected: PASS，包括当前 001–012 migration tests；不创建 013。

- [x] **Step 6: 审查无部分升级保证**

检查 diff 并确认 offline sentinel 的返回点在任何 pending transaction 之前：

```bash
git diff -- internal/store/sqlite.go internal/store/migrations.go internal/store/sqlite_test.go internal/store/migrations_test.go
```

---

### Task 2: 三卷 instance marker 库与 entrypoint 门禁

**Files:**
- Create: `internal/instanceid/marker.go`
- Create: `internal/instanceid/marker_test.go`
- Modify: `deploy/docker-entrypoint.sh`
- Modify: `scripts/verification/test-docker-entrypoint.sh`
- Modify: `docs/deployment/reinstall-data-continuity.md`

- [x] **Step 1: 写 marker RED 测试**

Go 测试覆盖三个相同合法 marker、缺失、空、非 64 位小写 hex、不匹配、目录、FIFO 和符号链接。API 返回实例 ID，但错误不得打印 marker 内容以外的敏感路径内容。

shell 测试先增加以下失败场景：existing config/data + 缺失 managed marker；managed marker 不匹配；部分新建 marker；非空无 marker 的 managed；符号链接 marker。新实例成功路径断言三份 marker 相同。

- [x] **Step 2: 运行并确认 RED**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/instanceid
sh scripts/verification/test-docker-entrypoint.sh
```

- [x] **Step 3: 实现严格 marker 校验**

`internal/instanceid` 使用 `Lstat` 拒绝 symlink 和非普通文件，读取并验证 `^[0-9a-f]{64}\n?$`，要求三者相同。

entrypoint 增加 `MANAGED_INSTANCE_FILE`：

- 新实例只在三卷均无持久状态时创建同一个 ID；
- existing instance 缺少任一 marker 都 fail-closed；
- 不再保留“发现旧 DB 就静默创建 marker”的普通启动认领逻辑；
- child 首次启动失败且 DB 未创建时，清理本次创建的三份 marker；
- 不能删除或改写既有 marker。

旧双 marker 开发实例的显式认领属于 Task 5 CLI，entrypoint 只给出不含数据内容的修复提示。

- [x] **Step 4: GREEN 与回归**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/instanceid
sh scripts/verification/test-docker-entrypoint.sh
```

---

### Task 3: 完整回滚包发布器

**Files:**
- Create: `internal/offlinemigration/types.go`
- Create: `internal/offlinemigration/bundle.go`
- Create: `internal/offlinemigration/bundle_test.go`
- Reuse: `internal/store/backup.go`
- Reuse: `internal/recovery/backup.go`

- [ ] **Step 1: 写回滚包 RED 测试**

测试使用独立临时 config/data/managed/rollback 根，覆盖：

- 输出根与任一源根相同、位于源内或包含源时拒绝；
- 创建前可用空间不足拒绝；
- config 原始字节、data sidecar、managed 嵌套文件、空目录、权限和 symlink 处理符合 manifest；
- live DB 使用 Online Backup，WAL 中已提交数据包含在快照；
- live DB/WAL/SHM 和本次临时文件不会作为普通 data 文件重复复制；
- 外部挂载只记录注册信息与 durable identity，不复制内容；
- 文件权限 0600、目录 0700；manifest 不包含 runtime secret 值；
- 复制中源 identity 或摘要变化拒绝；
- 任一注入失败都不会生成 `ROLLBACK_READY`；
- 全部文件、manifest、父目录 fsync 后最后发布 ready marker。

- [ ] **Step 2: 运行并确认 RED**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration -run 'TestBundle'
```

- [ ] **Step 3: 实现 bundle builder**

`BundleRequest` 必须显式携带 DB、config/data/managed、rollback root、instance ID、source/target migration 和外部 identity 列表。`BuildBundle` 使用唯一临时目录，先核算源字节数与目标文件系统余量，再复制并逐项 SHA-256；SQLite 使用 `DB.BackupTo` 后调用 `recovery.ValidateSnapshot`。

manifest 只保存 runtime.env 的路径、长度、mode 和摘要，不保存解析后的键值。复制 symlink 时不得跟随越界；第一版直接拒绝所有持久根内 symlink，避免把包边界扩展到未声明路径。

完成后 fsync 每个文件与目录，原子发布最终 bundle 目录并最后写 `ROLLBACK_READY`。任何失败保留 journal 可识别的 incomplete 临时目录，但不得把它报告为可恢复包。

- [ ] **Step 4: GREEN 与敏感信息审查**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration -run 'TestBundle'
git diff --check -- internal/offlinemigration
```

---

### Task 4: 独占锁、迁移 journal 与完整校验器

**Files:**
- Create: `internal/offlinemigration/lock.go`
- Create: `internal/offlinemigration/lock_test.go`
- Create: `internal/offlinemigration/journal.go`
- Create: `internal/offlinemigration/journal_test.go`
- Create: `internal/offlinemigration/validate.go`
- Create: `internal/offlinemigration/validate_test.go`

- [ ] **Step 1: 写 RED 测试**

覆盖同进程和子进程锁竞争、锁释放、journal 0600/原子替换/fsync、非法阶段跳转和损坏 journal。阶段固定为：

```text
preparing
rollback_ready
staging_migrated
validated
commit_started
commit_indeterminate
completed
```

验证器覆盖 `integrity_check`、`foreign_key_check`、目标版本与名称精确匹配，并预留 `DomainInvariant` 函数列表；任一不变量返回命名错误且不泄露行内容。

- [ ] **Step 2: 运行并确认 RED**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration -run 'TestLock|TestJournal|TestValidate'
```

- [ ] **Step 3: 实现锁、journal 和验证器**

锁文件固定为 `<db>.offline.lock`，使用非阻塞 OS advisory exclusive lock；锁对象生命周期内持有打开的 fd。journal 固定为 `<db>.offline-migration.json`，写入同目录临时文件、chmod 0600、file fsync、rename、directory fsync。

任何 existing unfinished journal 都阻止普通启动和新迁移。`commit_started` 后发生无法确认原子替换持久性的错误必须转 `commit_indeterminate`，不能回退到早期阶段。

- [ ] **Step 4: GREEN**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration -run 'TestLock|TestJournal|TestValidate'
```

---

### Task 5: staging 协调器与 CLI

**Files:**
- Create: `internal/offlinemigration/coordinator.go`
- Create: `internal/offlinemigration/coordinator_test.go`
- Modify: `cmd/omnora-recovery/main.go`
- Modify: `cmd/omnora-recovery/main_test.go`
- Modify: `cmd/omnora/main.go`
- Modify: `Dockerfile`

- [ ] **Step 1: 写 end-to-end RED 测试**

用测试 migration set（仍不创建生产 013）验证：

- live v12 + pending offline 在普通 opener 下拒绝且不变；
- CLI 在主进程持锁时失败，主进程在 CLI 持锁或 unfinished journal 时失败；
- 回滚包 ready 前不会创建 staging；
- staging 与 live DB 同目录，只有 staging 执行 offline migration；
- staging checkpoint 后关闭且无可附着 WAL；
- marker、managed identity 或外部 identity 在迁移中变化时 rename 前终止；
- SQL/完整性/外键/领域校验失败时 live DB 字节、schema 和 managed 内容不变；
- rename 前后故障注入得到确定 journal 阶段；
- rename 成功而 dir fsync/重开/最终验证失败进入 `commit_indeterminate`；
- 成功后 live DB 为目标版本、journal completed、回滚包保留；
- 重复执行按 completed 状态幂等返回，不重复迁移。

- [ ] **Step 2: 运行并确认 RED**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration ./cmd/omnora-recovery -run 'TestOfflineMigration|TestMigrateAccountMount'
```

- [ ] **Step 3: 实现协调器**

严格按设计的 13 步编排。live DB 只允许 validated readonly 与 Online Backup；`OpenSQLiteForOfflineMigration` 只接收自动生成且通过 same-directory 校验的 staging path。替换前关闭全部连接、checkpoint 并处理 live/staging sidecar；在 journal `commit_started` 持久化后才进入不可逆窗口。

不要调用 restore coordinator，不写 `restore_requests`。失败返回值必须包含稳定 reason code，不能包含 runtime secret、受限挂载名称或真实路径清单。

- [ ] **Step 4: 增加 CLI 与主进程门禁**

在 `omnora-recovery` 增加 `migrate-account-mount` 子命令，参数从受保护环境或配置读取 config/data/managed/db；rollback root 必须显式提供。增加独立 `adopt-managed-marker` 离线动作：只在完整回滚包 ready、双 marker 合法一致、managed 清单与 identity 已记录后允许写第三 marker；非空无 marker 还必须有 `--adopt-unmarked-managed`。

主进程打开 DB 前获取同一锁，检查三 marker 与 unfinished journal。Dockerfile 继续只构建现有两个二进制，不新增第三个工具镜像。

- [ ] **Step 5: GREEN 与恢复回归**

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/offlinemigration ./cmd/omnora-recovery ./cmd/omnora
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./internal/store ./internal/recovery
```

---

### Task 6: 部署契约、release gate 与全量验证

**Files:**
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.nas.yml`
- Modify: `deploy/docker-compose.aliyun-test.yml`
- Modify: `docs/deployment/reinstall-data-continuity.md`
- Modify: `scripts/verification/release-gate.sh`
- Modify: `scripts/verification/release-readiness-checklist.md`

- [ ] **Step 1: 更新运维契约**

文档明确停机、外置 rollback root、三 marker、显式旧 managed 认领、迁移命令、`commit_indeterminate` 恢复和回滚包保留策略。Compose 只增加必要的只读/读写挂载与注释，不把 rollback root 默认放入 managed。

- [ ] **Step 2: 将动态迁移验证加入 release gate**

release gate 必须运行 store/offlinemigration/recovery/CLI/entrypoint focused suites，并从 MCP catalog 派生 Inspector 工具数量；不能继续硬编码迁移前的 24/25 工具数字。

- [ ] **Step 3: 全量验证**

Run:

```bash
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go test ./...
GOCACHE=/private/tmp/omnora-offline-migration-go-cache go vet ./...
sh scripts/verification/test-docker-entrypoint.sh
sh scripts/verification/verify-api-docs.sh
git diff --check
```

如果仅有 sandbox 禁止 `127.0.0.1:0` 导致 server 测试失败，只在获得所需权限后重跑同一命令，并明确区分环境限制与产品失败。

- [ ] **Step 4: 停止并评审 013 准入**

只有本计划全部验收通过后，才新建独立的 `013` 与“唯一默认挂载 + 每账号个人目录”垂直切片计划。此处不得顺手添加 SQL、Space 兼容层或文件系统删除逻辑。

---

## 串行与并行边界

- Task 1 → Task 2 → Task 3 → Task 4 → Task 5 具有安全依赖，生产代码串行实施。
- 每个 Task 完成后依次做规格审查、代码质量审查，再进入下一 Task。
- 只读审计、互不写文件的 focused test 可以并行；`internal/store`、`internal/offlinemigration`、entrypoint 和 CLI 的写入不能并行。
- 任一测试失败先按根因修复，不通过放宽断言、隐藏错误或回退 fail-closed 规则使其变绿。
