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
- 启动时自动写入 OpenAI 供应商、默认分组和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据
- 使用记录按每次上游尝试写入；失败记录保存 `request_snapshot_json` / `response_snapshot_json`，用于前端查看请求与返回日志

## 错误兜底策略

账户添加和编辑优先维护账号自己的 `credentials.error_handling_rules`。网关不会把未处理的上游 `4xx/5xx` 原样返回给客户端：如果当前账号的内嵌规则没有命中，当前账号会按 `defaultTemporaryUnschedulableMinutes` 进入临时不可调用，并切换到同分组内下一个可用账号重试；全部账号都不可用时，客户端只会收到“没有可用账号”的网关错误。

## 默认运行策略

系统设置默认写入：

- `defaultTemporaryUnschedulableMinutes = 5`：未知异常、策略冷却和流熔断共用的临时不可调用时长。
- `temporaryUnschedulableRetryIntervalSeconds = 3`：进入临时不可调用前的默认短暂重试间隔。
- `temporaryUnschedulableRetryAttempts = 3`：进入临时不可调用前的默认短暂重试次数。
- `streamCircuitBreakerEnabled = true`：流熔断默认开启。
- `streamRequestTimeoutSeconds = 180`：流式请求首包前的请求熔断时间，超时后会切换账号重发。
- `streamIdleTimeoutSeconds = 30`、`streamFailureThresholdCount = 3`、`streamFailureThresholdWindowMinutes = 10`：流式响应异常的轻量阈值。

旧库升级时会清理不再展示的 `defaultErrorPolicyId`、`streamFailureAction`、`streamAccountCooldownMinutes`、`overloadCooldownEnabled`、`overloadCooldownMinutes`，并通过一次性迁移把流熔断默认打开。

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码

这是单人自用系统，接口会返回前端需要展示的完整密钥；数据库中仍尽量加密保存。

API Key 明文只在创建时返回一次。

