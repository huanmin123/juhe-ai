# 开发运行说明

## 启动

```powershell
cd F:\juhe-ai
pnpm install
pnpm dev
```

打开：

```text
http://127.0.0.1:5173
```

## 后端接口

```powershell
curl.exe http://127.0.0.1:3000/api/health
curl.exe http://127.0.0.1:3000/api/providers
curl.exe http://127.0.0.1:3000/api/accounts
curl.exe http://127.0.0.1:3000/api/groups
curl.exe http://127.0.0.1:3000/api/api-keys
curl.exe http://127.0.0.1:3000/api/proxies
curl.exe http://127.0.0.1:3000/api/settings
```

## SQLite

默认数据库：

```text
F:\juhe-ai\backend\data\juhe-ai.sqlite3
```

指定数据库位置：编辑 `backend/.env`，不设置系统环境变量。

```dotenv
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
```

## 验证

```powershell
pnpm typecheck
pnpm build
```

## 本地网关快速验证

`$LiteKey` 使用 API Key 页面新建后返回的明文密钥，或同步脚本首次输出的 `sync.apiKey`。

客户端接入时使用 OpenAI 兼容协议：

- Base URL：`http://127.0.0.1:3000/v1`
- API Key：API 密钥页生成的本地网关密钥
- 常用路径：`/models`、`/responses`、`/chat/completions`

```powershell
$LiteKey = "你的本地网关 API Key"
Invoke-RestMethod http://127.0.0.1:3000/v1/models `
  -Headers @{ Authorization = "Bearer $LiteKey" }

$payload = @{
  model = "gpt-5.4-mini"
  input = "Reply OK only."
} | ConvertTo-Json -Compress

Invoke-RestMethod -Method Post `
  -Uri "http://127.0.0.1:3000/v1/responses" `
  -Headers @{ Authorization = "Bearer $LiteKey" } `
  -ContentType "application/json" `
  -Body $payload
```

当前已验证 `dli.li-300-15` 可用，并已作为同步分组的优先账户。PowerShell 的 POST 请求也已覆盖验证，网关会过滤 `Expect: 100-continue` 等不应透传的请求头。



## OpenAI OAuth 账户授权

前端路径：

```text
http://127.0.0.1:5173/accounts
```

点击 `OpenAI 账户授权`，可选两种方式：

- `手动授权`：生成授权链接，登录完成后复制浏览器地址栏里的 `http://localhost:1455/auth/callback?code=...&state=...` 回调 URL，粘贴回弹窗创建账户。
- `Refresh Token`：直接粘贴已有 OpenAI `refresh_token`，后端刷新成功后创建 OAuth 账户。

后端冒烟验证：

```powershell
Invoke-RestMethod -Method Post `
  -Uri "http://127.0.0.1:3000/api/openai-oauth/auth-url" `
  -ContentType "application/json" `
  -Body "{}"
```

OAuth token 交换、刷新、账户测试会使用账户绑定代理；网关真实转发也会使用账号绑定代理。

### 账户测试

账户列表“更多”菜单里的“测试”会调用后端 `POST /api/accounts/:id/test`，实际访问 `/models` 来验证账户是否可用；OAuth 账户会先尝试刷新 token。

### 代理绑定

OAuth 账户如需代理，可在账户编辑弹窗中绑定代理配置；若代理不可用，刷新 token 和测试会失败。


## 完整测试方案


### 自动烟测

```powershell
cd F:\juhe-ai
pnpm test:smoke
```

默认检查 `dli.li-300-15`、可见网关 API Key、`/v1/models`、`/v1/responses` 非流式和流式、使用记录 token/cost 入库。覆盖烟测参数时编辑 `backend/.env`：

```dotenv
JUHE_AI_BACKEND_URL=http://127.0.0.1:3000
JUHE_AI_SMOKE_ACCOUNT_NAME=dli.li-300-15
JUHE_AI_SMOKE_MODEL=gpt-5.4-mini
```

```powershell
pnpm test:smoke
```

烟测还会检查系统设置不再返回 `defaultErrorPolicyId`，并确认这些字段存在：`defaultTemporaryUnschedulableMinutes`、`temporaryUnschedulableRetryIntervalSeconds`、`temporaryUnschedulableRetryAttempts`、`streamCircuitBreakerEnabled`、`streamRequestTimeoutSeconds`、`streamIdleTimeoutSeconds`、`streamFailureThresholdCount`、`streamFailureThresholdWindowMinutes`。

### 错误策略验证

```powershell
$settings = Invoke-RestMethod http://127.0.0.1:3000/api/settings
$settings.data.PSObject.Properties.Name -contains 'defaultErrorPolicyId'
```

检查点：系统设置不再返回 `defaultErrorPolicyId`；账户添加/编辑弹窗的“错误处理策略”应展示账号内嵌规则列表，并保存到 `credentials.error_handling_rules`。未命中账号规则的上游错误不透传给客户端，而是让当前账号进入临时不可调用并切换账号；全部账号不可用时返回没有可用账号。

### 流熔断设置验证

```powershell
$settings = Invoke-RestMethod http://127.0.0.1:3000/api/settings
$settings.data.defaultTemporaryUnschedulableMinutes
$settings.data.temporaryUnschedulableRetryIntervalSeconds
$settings.data.temporaryUnschedulableRetryAttempts
$settings.data.streamCircuitBreakerEnabled
$settings.data.streamRequestTimeoutSeconds

Invoke-RestMethod -Method Patch `
  -Uri "http://127.0.0.1:3000/api/settings" `
  -ContentType "application/json" `
  -Body (@{
    defaultTemporaryUnschedulableMinutes = 5
    temporaryUnschedulableRetryIntervalSeconds = 3
    temporaryUnschedulableRetryAttempts = 3
    streamCircuitBreakerEnabled = $true
    streamRequestTimeoutSeconds = 180
    streamIdleTimeoutSeconds = 30
    streamFailureThresholdCount = 3
    streamFailureThresholdWindowMinutes = 10
  } | ConvertTo-Json -Compress)
```

检查点：系统设置页能保存并回显；流式异常达到阈值后账号列表会显示临时不可调用/错误摘要，更多菜单可执行“清理错误/临时状态”。

### 代码级验证

```powershell
cd F:\juhe-ai
pnpm typecheck
pnpm build
pnpm test:smoke
```

### 管理面验证

```powershell
Invoke-RestMethod http://127.0.0.1:3000/api/health
Invoke-RestMethod http://127.0.0.1:3000/api/accounts
Invoke-RestMethod http://127.0.0.1:3000/api/api-keys
Invoke-RestMethod http://127.0.0.1:3000/api/usage-records
```

检查点：

- 账户列表只显示名称、类型、供应商、并发、状态、用量、优先级、最近使用时间、操作。
- `Access/API Key` 与 `Refresh Token` 不在列表展示，只在编辑弹窗展示和修改。
- API Key 页面直接展示完整本地网关密钥，方便复制。
- OAuth 账户只在编辑弹窗展示 `Refresh Token`。

### 账户验证

```powershell
$account = (Invoke-RestMethod http://127.0.0.1:3000/api/accounts).data |
  Where-Object { $_.name -eq "dli.li-300-15" } |
  Select-Object -First 1

Invoke-RestMethod -Method Post "http://127.0.0.1:3000/api/accounts/$($account.id)/test"
```

检查点：`dli.li-300-15` 应返回 `success = true`。

### 网关验证

```powershell
$LiteKey = ((Invoke-RestMethod http://127.0.0.1:3000/api/api-keys).data | Select-Object -First 1).key
Invoke-RestMethod http://127.0.0.1:3000/v1/models -Headers @{ Authorization = "Bearer $LiteKey" }

$payload = @{
  model = "gpt-5.4-mini"
  input = "只输出 OK"
  max_output_tokens = 16
  stream = $false
} | ConvertTo-Json -Compress

Invoke-RestMethod -Method Post `
  -Uri "http://127.0.0.1:3000/v1/responses" `
  -Headers @{ Authorization = "Bearer $LiteKey" } `
  -ContentType "application/json" `
  -Body $payload

$streamBody = '{"model":"gpt-5.4-mini","input":"只输出 OK","max_output_tokens":16,"stream":true}'
$bodyFile = New-TemporaryFile
Set-Content -LiteralPath $bodyFile -Value $streamBody -NoNewline -Encoding utf8
curl.exe -sS -N -X POST "http://127.0.0.1:3000/v1/responses" `
  -H "Authorization: Bearer $LiteKey" `
  -H "Content-Type: application/json" `
  --data-binary "@$bodyFile" `
  --max-time 60
```

检查点：

- `/v1/models` 返回模型列表。
- 非流式 `/v1/responses` 返回 `status = completed`。
- 流式 `/v1/responses` 返回 `response.completed`。
- `http://127.0.0.1:3000/api/usage-records` 能看到 `endpoint`、`inputTokens`、`outputTokens`、`cacheReadTokens`、`costUsd`。

