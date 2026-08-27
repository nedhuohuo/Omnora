# 初始化 Token 显式日志输出设计

## 1. 目标

为无法直接读取容器持久化配置文件的受控部署提供首次初始化 Token 日志输出能力，同时保持默认不泄露，并确保已初始化实例永远不会再次输出该 Token。

## 2. 不可弱化的安全约束

以下约束属于发布门禁覆盖的固定契约，后续实现和文档不得绕过或弱化：

1. `OMNORA_LOG_INITIALIZATION_TOKEN` 默认必须为 `false`；未设置、空值或非真值均视为关闭。
2. 只有 `OMNORA_LOG_INITIALIZATION_TOKEN=true` 且数据库确认尚未初始化时，日志才必须输出完整初始化 Token。
3. 数据库已初始化时，无论开关值如何，都不得输出初始化 Token。
4. 未配置数据库、初始化 Token 为空、初始化准备失败或初始化状态无法确认时，不得输出初始化 Token。
5. TOTP 加密密钥、审计 HMAC 密钥及其他运行时密钥永远不得写入日志。
6. 日志能力不得改变 `runtime.env` 的持久化、权限、原子写入和重启复用语义。

## 3. 配置契约

新增环境变量：

```text
OMNORA_LOG_INITIALIZATION_TOKEN=false
```

配置层将其解析为初始化配置中的布尔字段。Compose、NAS Compose 和 Aliyun 测试环境模板均显式声明默认值 `false`，并说明只有受控首次初始化窗口才能临时设为 `true`。

## 4. 输出时机与数据流

1. Docker entrypoint 继续生成或读取 `OMNORA_INITIALIZATION_TOKEN`，并通过进程环境传给后端；entrypoint 本身不打印 Token。
2. 后端打开数据库并执行 `PrepareInitializationWithToken`。
3. 只有该调用成功，才能证明当前数据库没有账号、初始化记录尚未消费，并且本次初始化凭据已准备完成。
4. 此时若日志开关为 `true`，后端输出一条 WARN 级结构化日志：

```json
{
  "level": "WARN",
  "msg": "initialization token logging explicitly enabled",
  "initialization_token": "<完整 Token>"
}
```

5. 若准备调用返回 `ErrAlreadyInitialized`，后端继续正常启动，但不得记录 Token。
6. 其他准备错误保持现有启动失败行为，且错误日志不得包含 Token。

通过让 Go 后端在数据库判定之后负责输出，而不是让 entrypoint 猜测数据库状态，可以保证日志条件与身份初始化事务使用同一事实来源。

## 5. 组件变更

### 配置层

- `internal/config`：解析 `OMNORA_LOG_INITIALIZATION_TOKEN`。
- 默认值保持关闭。

### 启动层

- `cmd/omnora`：仅在初始化准备成功后按开关记录 Token。
- 已初始化、无数据库和失败路径均不记录。

### 部署层

- `deploy/docker-compose.yml`
- `deploy/docker-compose.nas.yml`
- `deploy/aliyun-test.env.example`

以上文件增加变量透传和风险注释。

### 文档层

部署文档必须同时说明：

- 推荐方式仍是读取受保护的 `runtime.env`；
- 日志输出是显式的高风险兼容方式；
- 启用后 Token 会进入 Docker 日志、日志轮转和日志采集系统；
- 初始化完成后应将开关恢复为 `false`，并按日志系统策略清理含 Token 的历史日志。

## 6. 测试与发布门禁

自动化测试至少覆盖：

1. 开关未设置或为 `false`，数据库未初始化：日志不含 Token。
2. 开关为 `true`，数据库未初始化且初始化准备成功：日志恰好包含一次完整 Token。
3. 开关为 `true`，数据库已有账号或初始化已消费：日志不含 Token。
4. 初始化准备失败：日志不含 Token，启动仍按现有规则失败。
5. entrypoint 测试继续保证 TOTP 和审计密钥从不出现在日志中。
6. Compose 静态校验保证变量默认关闭并正确透传。

上述测试纳入现有 `release-gate.sh`，使不可弱化的安全约束成为镜像发布前的硬门禁。

## 7. 非目标

- 不在 Web API、Bootstrap 响应或前端页面返回 Token。
- 不增加运行时查看、重新生成或轮换初始化 Token 的管理接口。
- 不输出 TOTP、审计密钥或 `runtime.env` 的完整内容。
- 不改变初始化 Token 的默认有效期和消费语义。
