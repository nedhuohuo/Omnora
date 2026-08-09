# 初始化 Token 显式日志输出 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 增加默认关闭的 `OMNORA_LOG_INITIALIZATION_TOKEN` 开关，仅当数据库确认尚未初始化时在日志输出完整初始化 Token。

**Architecture:** Docker entrypoint 继续只负责生成、持久化和注入密钥，不读取业务数据库，也不输出 Token。Go 后端在 `PrepareInitializationWithToken` 成功后才通过独立的 WARN logger 输出 Token；该 logger 不受全局 `OMNORA_LOG_LEVEL=error` 抑制，从而满足显式开启后的必达契约。配置、启动行为、Compose 透传和部署文档分别由单元测试与 release gate 静态检查保护。

**Tech Stack:** Go 1.26、`log/slog`、SQLite、POSIX shell、Docker Compose、GitHub Actions/GHCR

---

## 文件职责与变更范围

- `internal/config/config.go`：声明并解析初始化 Token 日志开关。
- `internal/config/config_test.go`：锁定默认关闭及显式真值行为。
- `cmd/omnora/main.go`：在数据库确认初始化可用后触发日志。
- `cmd/omnora/logging.go`：构造不受全局 error 阈值抑制的专用 WARN logger。
- `cmd/omnora/main_test.go`：覆盖未初始化、开关关闭、已初始化和数据库错误路径。
- `deploy/docker-compose.yml`、`deploy/docker-compose.nas.yml`：默认关闭并透传开关。
- `deploy/aliyun-test.env.example`：记录默认值和风险。
- `docs/deployment/aliyun-test-server.md`：说明受控启用、日志风险和初始化后关闭流程。
- `scripts/verification/verify-scaffolding.sh`：把默认关闭、Compose 透传和文档约束纳入 release gate。

### Task 1: 配置层显式开关

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写默认关闭和显式开启的失败测试**

在 `TestLoadEnvDefaultsFailClosed` 中增加：

```go
if cfg.Initialization.LogToken {
    t.Fatal("initialization token logging must be disabled by default")
}
```

新增：

```go
func TestLoadEnvEnablesInitializationTokenLoggingOnlyForExplicitTruthyValue(t *testing.T) {
    for _, tc := range []struct {
        value string
        want  bool
    }{
        {value: "true", want: true},
        {value: "1", want: true},
        {value: "false", want: false},
        {value: "unexpected", want: false},
    } {
        clearEnv(t)
        t.Setenv("OMNORA_LOG_INITIALIZATION_TOKEN", tc.value)
        cfg, err := LoadEnv()
        if err != nil {
            t.Fatalf("LoadEnv(%q) error = %v", tc.value, err)
        }
        if cfg.Initialization.LogToken != tc.want {
            t.Fatalf("LogToken for %q = %v, want %v", tc.value, cfg.Initialization.LogToken, tc.want)
        }
    }
}
```

并将 `OMNORA_LOG_INITIALIZATION_TOKEN` 加入 `clearEnv`。

- [ ] **Step 2: 运行测试确认因字段缺失而失败**

Run:

```bash
go test ./internal/config -run 'TestLoadEnvDefaultsFailClosed|TestLoadEnvEnablesInitializationTokenLoggingOnlyForExplicitTruthyValue' -count=1
```

Expected: FAIL，错误指向 `cfg.Initialization.LogToken undefined`。

- [ ] **Step 3: 实现最小配置解析**

将配置结构改为：

```go
type InitializationConfig struct {
    Token    string
    TTL      time.Duration
    LogToken bool
}
```

在初始化 Token/TTL 解析旁增加：

```go
cfg.Initialization.LogToken = envTruthy("OMNORA_LOG_INITIALIZATION_TOKEN")
```

- [ ] **Step 4: 运行配置测试确认通过**

Run:

```bash
go test ./internal/config -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交配置层**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: 增加初始化令牌日志开关"
```

### Task 2: 数据库确认后的日志输出

**Files:**
- Modify: `cmd/omnora/main.go`
- Modify: `cmd/omnora/logging.go`
- Test: `cmd/omnora/main_test.go`

- [ ] **Step 1: 写未初始化且显式开启时必须输出的失败测试**

在 `cmd/omnora/main_test.go` 增加使用真实 SQLite 和 JSON slog logger 的测试：

```go
func TestPrepareInitializationLogsTokenWhenEnabledAndDatabaseIsUninitialized(t *testing.T) {
    ctx := context.Background()
    db, err := store.OpenSQLite(ctx, store.SQLiteOptions{
        Path: filepath.Join(t.TempDir(), "initialization-log.db"), BusyTimeout: time.Second,
    })
    if err != nil {
        t.Fatal(err)
    }
    defer db.Close()
    token := "visible-initialization-token"
    var output bytes.Buffer
    logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))

    err = prepareInitialization(ctx, identity.New(db.SQL(), identity.Options{}), config.InitializationConfig{
        Token: token, TTL: time.Hour, LogToken: true,
    }, logger)
    if err != nil {
        t.Fatalf("prepareInitialization() error = %v", err)
    }
    if strings.Count(output.String(), token) != 1 || !strings.Contains(output.String(), `"initialization_token"`) {
        t.Fatalf("initialization token log = %q", output.String())
    }
}
```

- [ ] **Step 2: 运行测试确认 helper 缺失**

Run:

```bash
go test ./cmd/omnora -run '^TestPrepareInitializationLogsTokenWhenEnabledAndDatabaseIsUninitialized$' -count=1
```

Expected: FAIL，错误为 `undefined: prepareInitialization`。

- [ ] **Step 3: 实现初始化准备 helper 并接入 main**

在 `cmd/omnora/main.go` 增加：

```go
func prepareInitialization(ctx context.Context, svc *identity.Service, cfg config.InitializationConfig, logger *slog.Logger) error {
    if cfg.Token == "" {
        return nil
    }
    _, err := svc.PrepareInitializationWithToken(ctx, cfg.Token, cfg.TTL)
    switch {
    case err == nil:
        if cfg.LogToken {
            logger.Warn("initialization token logging explicitly enabled", "initialization_token", cfg.Token)
        }
        return nil
    case errors.Is(err, identity.ErrAlreadyInitialized):
        return nil
    default:
        return err
    }
}
```

把原有初始化准备块替换为调用该 helper。`main` 传入使用当前日志格式、最低 WARN 阈值和 `os.Stdout` 构造的专用 logger。

在 `cmd/omnora/logging.go` 抽取：

```go
func newLogger(format string, level slog.Leveler, output io.Writer) *slog.Logger {
    options := &slog.HandlerOptions{Level: level}
    if format == "json" {
        return slog.New(slog.NewJSONHandler(output, options))
    }
    return slog.New(slog.NewTextHandler(output, options))
}
```

`configureLogger` 继续按全局配置设置默认 logger；初始化专用 logger 固定使用 `slog.LevelWarn`，确保显式开启后不被全局 error 阈值屏蔽。

- [ ] **Step 4: 运行目标测试确认通过**

Run:

```bash
go test ./cmd/omnora -run '^TestPrepareInitializationLogsTokenWhenEnabledAndDatabaseIsUninitialized$' -count=1
```

Expected: PASS。

- [ ] **Step 5: 写关闭、已初始化和错误路径测试**

新增三个测试：

```go
func TestPrepareInitializationDoesNotLogTokenWhenDisabled(t *testing.T)
func TestPrepareInitializationDoesNotLogTokenAfterInitializationWasConsumed(t *testing.T)
func TestPrepareInitializationDoesNotLogTokenWhenDatabaseCheckFails(t *testing.T)
```

分别断言：

- `LogToken=false` 时输出为空；
- 先准备初始化并把 `identity_initialization.consumed_at` 更新为 `CURRENT_TIMESTAMP` 后，再以 `LogToken=true` 调用，输出为空且返回 nil；
- 关闭 SQLite 后调用，返回非 nil 且输出不含 Token。

- [ ] **Step 6: 运行 cmd 测试确认全部通过**

Run:

```bash
go test ./cmd/omnora -count=1
```

Expected: PASS。

- [ ] **Step 7: 提交启动行为**

```bash
git add cmd/omnora/main.go cmd/omnora/logging.go cmd/omnora/main_test.go
git commit -m "feat: 在未初始化时按需记录初始化令牌"
```

### Task 3: Compose、文档与发布门禁

**Files:**
- Modify: `deploy/docker-compose.yml`
- Modify: `deploy/docker-compose.nas.yml`
- Modify: `deploy/aliyun-test.env.example`
- Modify: `docs/deployment/aliyun-test-server.md`
- Modify: `scripts/verification/verify-scaffolding.sh`

- [ ] **Step 1: 先增加会失败的静态门禁**

在 `scripts/verification/verify-scaffolding.sh` 增加精确检查：

```sh
grep -Fq 'OMNORA_LOG_INITIALIZATION_TOKEN: "${OMNORA_LOG_INITIALIZATION_TOKEN:-false}"' "$COMPOSE" ||
  fail "base compose must default initialization token logging to false"
grep -Fq 'OMNORA_LOG_INITIALIZATION_TOKEN: "${OMNORA_LOG_INITIALIZATION_TOKEN:-false}"' "$NAS_COMPOSE" ||
  fail "NAS compose must default initialization token logging to false"
grep -Fq 'OMNORA_LOG_INITIALIZATION_TOKEN=false' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun env example must default initialization token logging to false"
grep -Fq '数据库确认尚未初始化' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun deployment doc must require database confirmation before token logging"
```

- [ ] **Step 2: 运行静态门禁确认失败**

Run:

```bash
sh scripts/verification/verify-scaffolding.sh
```

Expected: FAIL，首先报告 base Compose 缺少该变量。

- [ ] **Step 3: 增加 Compose 透传和默认关闭值**

在两份 Compose 的初始化 Token 配置旁增加：

```yaml
OMNORA_LOG_INITIALIZATION_TOKEN: "${OMNORA_LOG_INITIALIZATION_TOKEN:-false}"
```

在 `deploy/aliyun-test.env.example` 增加：

```text
# High-risk compatibility switch. Keep false by default. When true, Omnora
# logs the full initialization token only after the database confirms that the
# instance is still uninitialized. Set back to false after first setup.
OMNORA_LOG_INITIALIZATION_TOKEN=false
```

- [ ] **Step 4: 更新部署文档**

在 `docs/deployment/aliyun-test-server.md` 写明：

```text
只有 OMNORA_LOG_INITIALIZATION_TOKEN=true 且数据库确认尚未初始化时，
后端才会在 WARN 日志的 initialization_token 字段输出完整 Token。
默认值必须保持 false；初始化完成后恢复 false，并清理包含 Token 的历史日志。
TOTP 与审计密钥在任何情况下都禁止输出。
```

同时保留读取 `runtime.env` 作为推荐方式。

- [ ] **Step 5: 运行 entrypoint 与静态门禁**

Run:

```bash
sh scripts/verification/test-docker-entrypoint.sh
sh scripts/verification/verify-scaffolding.sh
```

Expected: 两项均 PASS；entrypoint 仍不直接输出任何密钥。

- [ ] **Step 6: 提交部署契约**

```bash
git add deploy/docker-compose.yml deploy/docker-compose.nas.yml deploy/aliyun-test.env.example docs/deployment/aliyun-test-server.md scripts/verification/verify-scaffolding.sh
git commit -m "docs: 固化初始化令牌日志安全约束"
```

### Task 4: 全量验证、推送与镜像发布

**Files:**
- Verify only; no additional source files expected.

- [ ] **Step 1: 运行格式和差异检查**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/omnora/main.go cmd/omnora/logging.go cmd/omnora/main_test.go
git diff --check
git status --short
```

Expected: 无格式错误；只有两张既有未跟踪截图，不纳入提交。

- [ ] **Step 2: 运行完整发布门禁**

```bash
./scripts/verification/release-gate.sh
```

Expected: Go、前端 61 项测试、前端构建、entrypoint、API/MCP 文档和静态 Compose 检查全部通过；本地无 Docker 的动态检查允许按现有规则跳过。

- [ ] **Step 3: 推送功能分支触发镜像工作流**

```bash
git push origin codex/aliyun-test-deploy
```

Expected: 远端分支更新到当前 HEAD，并触发 `.github/workflows/publish-image.yml`。

- [ ] **Step 4: 等待 GitHub Actions 完成**

```bash
HEAD_SHA=$(git rev-parse HEAD)
RUN_ID=$(gh run list --repo nedhuohuo/Omnora --workflow publish-image.yml \
  --branch codex/aliyun-test-deploy --event push --limit 10 \
  --json databaseId,headSha --jq ".[] | select(.headSha == \"$HEAD_SHA\") | .databaseId" | head -n 1)
test -n "$RUN_ID"
gh run watch "$RUN_ID" --repo nedhuohuo/Omnora --exit-status
```

Expected: `Publish Docker image` completed/success。

- [ ] **Step 5: 核对 latest 和不可变 tag 的 digest**

```bash
SHORT_SHA=$(git rev-parse --short=7 HEAD)
gh run view "$RUN_ID" --repo nedhuohuo/Omnora --log | \
  rg "pushing manifest for ghcr.io/nedhuohuo/omnora:(latest|sha-$SHORT_SHA)|containerimage.digest"
```

Expected: `ghcr.io/nedhuohuo/omnora:latest` 和本次 `sha-$SHORT_SHA` 指向同一多架构 manifest digest。
