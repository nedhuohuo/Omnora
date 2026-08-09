# 账号—挂载模型离线迁移设计

状态：已确认，实施中  
日期：2026-08-08  
上位业务规格：[账号、挂载与内容授权设计](2026-08-08-account-mount-access-design.md)

## 1. 目标与边界

本设计定义 Omnora 从 Space 模型迁移到账号—挂载模型之前必须具备的离线迁移安全设施。迁移设施必须先于破坏性数据库迁移交付；在设施完成并通过验收前，不得把破坏性 `013` 放入普通启动会扫描的迁移目录。

执行顺序固定为：

1. 普通启动迁移门禁；
2. 三卷实例身份校验；
3. 完整回滚包；
4. staging 数据库离线迁移、完整性与领域不变量校验；
5. journal 保护的原子替换；
6. 唯一默认挂载与每账号个人目录的首个可见闭环。

Omnora 尚未正式发布，不保留旧 Space 数据、API、Token、分享或前端状态兼容层。但“不兼容”不等于可以丢失真实文件、密钥或失去可恢复性。

## 2. 为什么不能直接添加 013

当前 `store.OpenSQLite` 会自动执行 `migrations/*.sql` 中所有待执行迁移。若直接加入破坏性 `013`，正常服务启动、恢复命令的辅助打开以及其他调用点都可能在正式数据库上执行它，无法保证停机、回滚包、managed 卷身份和 staging 校验已完成。

因此迁移必须有显式分类和执行策略：

| 策略 | 使用者 | 行为 |
| --- | --- | --- |
| `validate_only` | 离线协调器读取 live DB、替换后复核 | 只校验迁移历史与 catalog，不执行 SQL |
| `apply_online_safe` | 普通 `OpenSQLite` | 只执行显式登记为在线安全的迁移；只要发现待执行离线迁移，就在执行任何待执行迁移前返回 `offline_migration_required` |
| `apply_offline` | 离线协调器的 staging DB | 允许执行已登记的在线和离线迁移 |

每个内嵌 SQL 文件必须在迁移 catalog 中登记精确版本、文件名和类别。未登记文件、重复版本、文件名漂移和未知类别都 fail-closed，不能默认当作在线安全。

### 2.1 真正空数据库

新实例需要直接建立最新目标 schema。仅当打开前数据库没有任何用户表、视图、索引或触发器，且没有迁移历史时，`apply_online_safe` 才可把它视为 fresh database，并执行完整 catalog，包括离线类别迁移。

已有数据库即使业务表为空，也不是 fresh database；只要存在 schema 对象或迁移历史，就必须遵守离线门禁。门禁必须在执行任何待执行迁移之前完成，避免先执行 012、再因 013 拒绝而形成部分升级。

## 3. 离线迁移协调器

账号—挂载迁移使用独立的 `offlinemigration` 领域，不伪装成一次 backup restore，也不复用 `restore_requests` 状态。

协调器按以下顺序执行：

1. 获取数据库旁的 OS 级独占锁；Omnora 主进程在整个生命周期持有同一把锁。
2. 校验 config、data、managed 三份 instance marker 完全一致。
3. 捕获 managed 根与全部外部挂载的 durable identity。
4. 以 `validate_only` 打开 live DB，确认源 schema 与预期相符。
5. 在用户显式指定、且位于 config/data/managed/外部挂载之外的目录发布完整回滚包。
6. 从 SQLite Online Backup 生成的数据库快照创建与 live DB 同目录、同文件系统的 staging DB。
7. 仅对 staging DB 使用 `apply_offline`。
8. checkpoint staging，关闭连接，确认 staging 不依赖 WAL/SHM。
9. 执行 `integrity_check`、`foreign_key_check`、目标 migration 精确校验和账号—挂载领域不变量校验。
10. 替换前重新校验三份 marker、managed durable identity、外部挂载 identity 和独占锁。
11. 清除旧数据库 WAL 依附风险后，以 journal 记录提交阶段；同目录原子替换并 fsync 数据库目录。
12. 以 `validate_only` 重开正式 DB，重复结构与领域校验。
13. journal 标记完成；回滚包保留，只有显式人工操作才可清理。

任一提交前失败都必须保持 live DB 和 managed 内容不变。rename 已成功但目录 fsync、重开或最终校验失败时，状态必须是 `commit_indeterminate`，普通服务继续 fail-closed，由恢复命令依据 journal 和回滚包处理，不能把它当作可安全重试的普通失败。

## 4. 完整回滚包

回滚包根目录由操作者显式提供。它不能位于 config、data、managed、任何外部挂载或这些目录的子树中，避免递归、自覆盖和源目标同故障域。包目录权限为 `0700`，普通文件为 `0600`；所有内容写入、校验并 fsync 后，最后原子发布 `ROLLBACK_READY`。没有该标记的包不可用于自动恢复。

回滚包至少包含：

- SQLite Online Backup 生成的迁移前数据库快照；
- config 的完整字节副本，包括 `runtime.env` 和 instance marker；
- data 中除 live DB、WAL/SHM、本次临时文件外的持久 sidecar 与 marker；
- managed 的完整独立副本或经过验证、可独立恢复的文件系统快照；
- 外部挂载的注册信息与 durable identity 清单，但不复制外部挂载内容，因为迁移不得修改它们；
- `manifest.json`、逐文件摘要、文件数量、总字节数、权限、源/目标 schema、迁移名称、实例 ID、二进制版本和阶段；
- 离线恢复说明。

`runtime.env` 在包内保留原始字节，不能脱敏后备份；日志、manifest 展示字段和终端输出不得打印密钥值，只记录存在性、长度和摘要。回滚包离开可信本机磁盘前必须由运维另行加密。

构建前必须核算可用空间；复制中源 identity 或内容发生变化、摘要不一致、磁盘不足、权限无法收紧或 fsync 失败都不得发布 `ROLLBACK_READY`。

## 5. 三卷实例身份

实例身份必须同时存在于：

```text
<config>/.omnora-instance-id
<data>/.omnora-instance-id
<managed>/.omnora-instance-id
```

marker 必须是非符号链接的普通文件，内容为同一个 64 位小写十六进制 ID。任何缺失、非法或不匹配都必须在打开 SQLite 前停止。

- 新实例只有在 config、data、managed 都没有既有持久状态时，才创建三份相同 marker。
- 三份 marker 分步落盘中发生崩溃后，下一次启动看到部分状态必须 fail-closed。
- 只有 config/data 两份 marker 的旧开发实例不能由普通 entrypoint 静默补齐。
- 旧实例只能由离线迁移命令在完整回滚包已发布、managed 内容清单已验证后显式认领。
- 非空且无 marker 的 managed 必须要求显式认领参数，不能因为目录看似为空或当前没有个人文件就自动认领。
- marker 之外还要保存并复核 `mountid.Capture` 可获得的 durable identity；临时计算的 mount ID 不能代替持久身份。

## 6. 账号—挂载目标不变量

破坏性迁移 SQL 落地时，staging 和替换后的正式数据库必须至少满足：

- 不存在 Space 业务表、Space ACL、`space_id` 外键或以 Space 为边界的 Token/分享/索引记录；
- 恰好一个 active、受系统保护的 `personal_default + managed + system + read_write` 默认挂载；
- 默认挂载不能出现在普通 `mount_grants`；
- 每个未被物理清理的账号恰好对应一个不可复用的 `personal_directories` 记录；active 账号的个人目录状态可用，删除账号保留 tombstone 与物理目录绑定；
- 个人目录物理相对路径只由稳定 `account_id` 决定，不能使用显示名、邮箱或登录名；
- 共用挂载只能是 `common + external + normal|restricted`；新建和迁移后的共用挂载允许零授权；
- `mount_grants` 和 `folder_collaborations` 的内容权限只能是 `viewer|editor`；
- 同一挂载—账号最多一条有效授权，同一接收者不能持有重叠的父子个人目录协作；
- AI/MCP 的动态“当前账号全部挂载”边界只包含本人个人目录与实时有效 `mount_grants`，不包含 `folder_collaborations`；
- 文件系统中的真实文件不因控制面迁移、Space 移除或解除挂载而删除。

具体表结构和数据映射在 `013` 实施计划中冻结；本阶段只交付能安全承载该迁移的设施。

## 7. 验收门槛

在允许添加破坏性 `013` 前，必须证明：

- 普通打开已有数据库遇到离线迁移时，任何待执行迁移都未执行；
- 只有 staging 路径能显式执行离线迁移，真正空数据库语义有独立测试；
- 未分类、未来版本和历史名称漂移全部拒绝；
- 主进程与离线命令互斥；未完成 journal 阻止普通启动；
- managed marker 缺失、不匹配、非法、符号链接、部分写入和处理中换卷全部拒绝；
- 回滚包的权限、摘要、空间核算、敏感信息输出和 `ROLLBACK_READY` 发布顺序通过故障测试；
- SQL、完整性、外键或领域校验失败时 live DB 与 managed 不变；
- WAL/SHM、rename 和 fsync 的每个故障点都有确定恢复状态；
- 原有 recovery、entrypoint 和全量 Go 测试保持通过。

完成这些门槛只代表“可以安全开始写 013”，不代表账号—挂载产品迁移已经完成。
