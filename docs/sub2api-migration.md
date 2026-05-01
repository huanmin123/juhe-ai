# 从 Mac sub2api 同步 OpenAI 账户

## 迁移范围

当前不做 `sub2api` 兼容层，也不提供单独导入页面。只从 Mac 生产库读取 OpenAI 账户必要信息，写入 `sub2api-lite` 本地 SQLite。

API Key 账户读取：

- 名称：`accounts.name`
- 描述：`accounts.notes`
- Base URL：`accounts.credentials.base_url`
- API Key：`accounts.credentials.api_key`

OAuth 账户读取：

- 名称：`accounts.name`
- 描述：`accounts.notes`
- `access_token` / `refresh_token` / `id_token`
- `expires_at` / `client_id` / `email`
- `chatgpt_account_id` / `chatgpt_user_id` / `organization_id` / `plan_type`

其他复杂配置暂不迁移。

## 数据来源

当前生产 `sub2api` 运行在 Mac：

- 主机：`192.168.1.156`
- SSH 用户：`huanmin`
- Postgres 容器：`sub2api-postgres`
- 数据库：`sub2api`
- 表：`accounts`

同步脚本只查询 `deleted_at IS NULL` 且 `platform = openai` 的账户：

- API Key：`type IN ('apikey', 'api_key')` 且 `credentials.api_key` 非空
- OAuth：`type = 'oauth'` 且 `credentials.access_token`、`credentials.refresh_token` 非空

## 执行同步

在 Windows PowerShell 7 中：

```powershell
cd F:\sub2api-lite
pnpm --filter sub2api-lite-backend sync:openai-from-mac
```

常用参数：

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac -- `
  --host 192.168.1.156 `
  --user huanmin `
  --group-name "Mac 同步 OpenAI API Key 分组" `
  --gateway-key-name "Mac 同步 OpenAI 网关 Key"
```

预演不写库：

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac -- --dry-run
```

如果需要指定 SSH 私钥：

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac -- --identity-file C:\Users\Administrator\.ssh\id_ed25519
```

## 同步结果

脚本会：

- 创建或复用目标分组
- 写入 OpenAI API Key 与 OAuth 账户
- 自动把这些账户绑定到目标分组
- OAuth 账户默认绑定本地 `socks5h://127.0.0.1:7897` 代理配置，方便刷新 token 和测试账户
- 创建或复用一个绑定该分组的本地网关 API Key
- API Key 用 `provider + type + base_url + api_key` 指纹去重，OAuth 用 `provider + type + refresh_token` 指纹去重，重复执行不会重复创建账户
- 如果同名网关 Key 已存在，不再重复创建；已保存明文的 Key 会继续在前端完整显示

输出中的 `sync.apiKey` 是 `sub2api-lite` 对外调用用的本地网关 Key；前端 API 密钥列表也会完整显示已保存的 Key。

## 最小可用测试

启动服务：

```powershell
pnpm dev
```

健康检查：

```powershell
curl.exe http://127.0.0.1:3000/api/health
```

模型列表：

```powershell
$LiteKey = "同步脚本输出的 sync.apiKey"
curl.exe http://127.0.0.1:3000/v1/models -H "Authorization: Bearer $LiteKey"
```

Responses 非流式：

```powershell
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

当前本机验证记录（2026-05-01）：

- 已同步：6 个 OpenAI API Key 账户、3 个 OpenAI OAuth 账户
- 分组：`Mac 同步 OpenAI API Key 分组`
- 优先账户：`dli.li-300-15`，`group_accounts.weight = 100`
- `/v1/models`：返回 `200`，使用账户 `dli.li-300-15`
- `/v1/responses` 非流式：模型 `gpt-5.4-mini` 返回 `200`，输出 `OK`，使用账户 `dli.li-300-15`
- `/v1/responses` 流式：模型 `gpt-5.4-mini` 返回 `200`，SSE 包含 `response.completed`，使用账户 `dli.li-300-15`

使用记录：

```powershell
curl.exe http://127.0.0.1:3000/api/usage-records
```

## 完整测试方案

### 1. 源库读取验证

- SSH 能连接 `huanmin@192.168.1.156`
- `sub2api-postgres` 容器运行中
- SQL 能返回 OpenAI API Key 账户和 OAuth 账户
- 查询结果不包含非 OpenAI 账户

### 2. Dry Run

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac -- --dry-run
```

通过标准：

- 输出 `source.openaiApiKeyAccounts` 和 `source.openaiOAuthAccounts` 符合 Mac 旧库数量
- 输出 `sync.imported` 符合预期
- SQLite 不新增账户、分组或 Key

### 3. 正式同步

```powershell
pnpm --filter sub2api-lite-backend sync:openai-from-mac
```

通过标准：

- 输出 `sync.groupId` 和 `sync.groupName`
- 首次输出 `sync.apiKey`
- `sync.imported` 大于 0 或 `sync.skipped` 等于已有数量

### 4. 管理端验证

- 打开 `http://127.0.0.1:5173`
- 账户管理页能看到同步账户
- 名称和备注与 Mac `sub2api` 一致
- 账户列表不展示 `Access/API Key` 与 `Refresh Token`；编辑账户时可查看和修改
- OAuth 账户编辑页可看到 `Refresh Token` 和 `Client ID`
- 分组页能看到目标分组绑定了这些账户
- API Key 页能看到本地网关 Key 记录和完整密钥

### 5. 网关验证

- `/api/health` 返回 `ok`
- `/v1/models` 能透传到上游
- `/v1/responses` 能返回正常响应
- 使用记录页能看到请求记录
- 账户列表的用量情况、最近使用时间会随网关请求更新
- 账户操作菜单里的“测试”会调用 `GET /models` 验证该账户

### 6. 幂等验证

重复执行同一个同步命令。

通过标准：

- 第二次 `sync.imported` 为 `0`
- 第二次 `sync.skipped` 等于同一批账户数
- 不重复创建相同 `base_url + api_key` API Key 账户
- 不重复创建相同 `refresh_token` OAuth 账户
- 不重复创建同名本地网关 Key
