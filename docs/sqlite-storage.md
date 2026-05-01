# SQLite 存储说明

## 为什么用 SQLite

当前项目只给个人使用，不需要复杂部署、水平扩展或极限并发。SQLite 足够稳定，文件备份也简单，更符合轻量项目定位。

## 默认位置

后端默认数据库文件：

```text
backend/data/sub2api-lite.sqlite3
```

可以通过环境变量指定：

```powershell
$env:SQLITE_PATH = "F:\sub2api-lite-data\sub2api-lite.sqlite3"
```

也兼容：

```powershell
$env:DATABASE_PATH = "F:\sub2api-lite-data\sub2api-lite.sqlite3"
```

## 当前实现

- 使用 Node 22 内置 `node:sqlite`
- 启动时自动建表
- 启动时自动写入 OpenAI 供应商、默认分组、默认错误策略和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据

## 默认错误策略

启动时会写入两条内置策略：

- `ep_default_passthrough`：默认透传策略，上游 `4xx/5xx` 原样返回。
- `ep_default_safe`：默认安全策略，上游 `4xx/5xx` 转成本地 `502` 自定义错误。

系统设置 `defaultErrorPolicyId` 默认指向 `ep_default_passthrough`，新建账号默认引用该策略；账号编辑里可以单独切换或清空为运行时默认。

## 默认运行策略

系统设置默认写入：

- `defaultTemporaryUnschedulableMinutes = 5`：未知异常、策略冷却和流熔断共用的临时不可调用时长。
- `streamCircuitBreakerEnabled = true`：流熔断默认开启。
- `streamIdleTimeoutSeconds = 180`、`streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：流式响应异常的轻量阈值。

旧库升级时会清理不再展示的 `streamFailureAction`、`streamAccountCooldownMinutes`、`overloadCooldownEnabled`、`overloadCooldownMinutes`，并通过一次性迁移把流熔断默认打开。

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码

这是单人自用系统，接口会返回前端需要展示的完整密钥；数据库中仍尽量加密保存。

API Key 明文只在创建时返回一次。

