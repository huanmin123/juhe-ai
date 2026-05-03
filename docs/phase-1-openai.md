# 第一期：OpenAI OAuth + API Key

## 范围

第一期只实现 OpenAI 供应商，账户类型只支持：

- OpenAI OAuth
- OpenAI API Key

其他供应商先只保留架构扩展位，不实现页面和接口。

对外中转入口统一使用 OpenAI 兼容协议：客户端 Base URL 填本服务 `/v1`，例如开发环境 `http://127.0.0.1:3000/v1`；API Key 填 API 密钥页生成的本地网关密钥。后续即使增加其他主流厂商，也先适配为 OpenAI 兼容请求格式。

## OpenAI 供应商定义

```ts
type ProviderCode = 'openai'

type OpenAIAccountType = 'oauth' | 'api_key'
```

默认能力：

- 模型列表
- Responses API 预留
- 流式响应预留
- 透传预留

## OpenAI OAuth 创建方式

第一阶段建议先支持“手动录入 OAuth 凭据”，后续再补完整授权跳转和 callback。

表单字段：

- 账户名称
- `access_token`
- `refresh_token`
- `expires_at`
- 代理
- 并发上限
- 是否启用透传
- 错误策略
- 备注

`account_id` / `chatgpt_account_id` 属于 OpenAI OAuth token 解析出的系统元数据，不作为用户表单输入项。

保存要求：

- token 加密存储
- 列表不展示 Access Token 与 Refresh Token，编辑弹窗可查看和修改
- 可设置过期时间
- 可手动启用 / 停用
- `refresh_token` 只对 OAuth 账户需要，账户列表不展示

## OpenAI API Key 创建方式

表单字段：

- 账户名称
- `api_key`
- `base_url`
- 代理
- 并发上限
- 是否启用透传
- 错误策略
- 备注

保存要求：

- API Key 加密存储
- 列表不展示 API Key，编辑弹窗可查看和修改
- `base_url` 默认使用 OpenAI 官方地址
- 可手动启用 / 停用

## 账户归属分组

第一期的关系规则：

- 账户在创建 / 编辑时主动选择归属分组
- 一个账户同一时间归属零个或一个分组
- 分组可以汇总多个 OpenAI 账户
- API Key 绑定一个分组
- 请求进入后只能使用该 API Key 对应分组内的账户

## 页面优先级

1. 供应商页：展示 OpenAI、支持的账户类型和模型价格目录
2. 账户页：OpenAI OAuth / API Key 创建、编辑、状态切换
3. 分组页：维护分组基础信息并查看账户数量与聚合状态
4. API Key 页：创建密钥并绑定分组
5. 代理页：维护代理并给账户选择
6. 使用记录页：先做空状态和列表结构
7. 系统设置页：先做基础配置占位

## 接口优先级

1. `GET /api/providers`
2. `GET /api/providers/:code/models`
3. `GET /api/accounts`
4. `POST /api/accounts`
5. `PATCH /api/accounts/:id`
6. `DELETE /api/accounts/:id`
7. `GET /api/groups`
8. `POST /api/groups`
9. `PATCH /api/groups/:id`
10. `DELETE /api/groups/:id`
11. `GET /api/api-keys`
12. `POST /api/api-keys`
13. `PATCH /api/api-keys/:id`
14. `DELETE /api/api-keys/:id`
15. `GET /api/proxies`
16. `POST /api/proxies`

## 暂不做

- 其他供应商
- 完整 OAuth 授权 callback
- 非 OpenAI 兼容协议的专用网关
- 复杂计费
- 多租户用户体系
- 自动账号健康检测



## OpenAI OAuth 授权方式

参考 `sub2api` 的 OpenAI OAuth 账户语义，`juhe-ai` 第一阶段实现两种轻量授权方式：

### 手动授权

1. 后端生成 `state`、`code_verifier`、`code_challenge` 和授权链接。
2. 前端打开 `https://auth.openai.com/oauth/authorize`。
3. 用户登录 OpenAI 后浏览器会跳转到 `http://localhost:1455/auth/callback`。
4. 如果本机没有监听该端口，浏览器显示连接失败也没关系，复制地址栏完整 URL。
5. 前端把回调 URL 提交给后端，后端校验 `state` 并用 PKCE `code_verifier` 换取 token；Client ID 与 Redirect URI 使用后端内置默认值，不暴露给用户填写。
6. 创建 OpenAI OAuth 账户，保存 `access_token`、`refresh_token`、`expires_at`、`client_id` 和邮箱。
7. 账户落库后立即触发一次首次额度快照刷新；刷新失败只更新快照状态和退避时间，不影响账户创建结果。

### Refresh Token 授权

1. 用户直接粘贴已有 `refresh_token`。
2. 后端使用内置默认 Client ID 和 `grant_type=refresh_token` 向 OpenAI token endpoint 刷新。
3. 刷新成功后创建 OpenAI OAuth 账户。
4. 账户落库后立即触发一次首次额度快照刷新；刷新失败只更新快照状态和退避时间，不影响账户创建结果。
5. 如果 OpenAI 没返回新的 `refresh_token`，继续保留用户输入的原始 `refresh_token`。

### 调度与授权刷新

- API Key 账户使用 `credentials.api_key` 作为上游 Bearer token。
- OAuth 账户使用 `credentials.access_token` 作为上游 Bearer token。
- 网关发现 OAuth token 即将过期时，会优先用 `refresh_token` 自动刷新并写回账户。
- 后台 OAuth 额度快照刷新任务探测前，如果发现 access token 即将过期，也会先自动刷新授权。
- 账户页不提供常驻“刷新授权”或“刷新用量”按钮；授权续期和额度快照都由真实请求与后台任务维护。
- OAuth token 刷新、账户测试和后台额度探测会优先使用账户绑定的代理；没有绑定代理时默认直连。迁移旧账户时不再自动创建或绑定本机固定端口代理，避免换电脑或服务器部署后误连本机端口。

### OpenAI OAuth 额度进度

OpenAI OAuth 账户受上游 Codex/ChatGPT 使用窗口限制，常见窗口包括约 `5h` 窗口和 `7d` 窗口；这类额度不是 API Key 的 token / 成本用量，必须单独展示和处理。

- 数据来源优先使用真实网关请求或账号测试返回的 Codex rate-limit 响应头：`x-codex-primary-used-percent`、`x-codex-primary-reset-after-seconds`、`x-codex-primary-window-minutes`、`x-codex-secondary-used-percent`、`x-codex-secondary-reset-after-seconds`、`x-codex-secondary-window-minutes`。
- 归一化规则：优先按 `window_minutes` 判断窗口，较小窗口映射为 `5h`，较大窗口映射为 `7d`；只有单侧窗口时，`<= 360` 分钟归为 `5h`，更长归为 `7d`；没有窗口长度时兼容旧语义，默认 primary 为 `7d`、secondary 为 `5h`。
- 存储字段建议保存为账号运行态快照，并按 `system_account_id + account_id + kind` 隔离：`codex_5h_used_percent`、`codex_5h_reset_after_seconds`、`codex_5h_reset_at`、`codex_5h_window_minutes`、`codex_7d_used_percent`、`codex_7d_reset_after_seconds`、`codex_7d_reset_at`、`codex_7d_window_minutes`、`codex_usage_updated_at`、`last_attempt_at`、`last_success_at`、`next_refresh_after`、`refresh_status`、`last_error_message`。
- 获取策略：列表只读已缓存快照，不因展示批量探测；新建 OAuth 账户会在创建流程里立即触发一次首次快照刷新，缺失、过期或接近恢复点的快照由后台定时器统一探测，可对 `https://chatgpt.com/backend-api/codex/responses` 做节流探测，使用 `stream: true`、`store: false`、Codex CLI 相关 header，并复用账号代理。
- 后台策略：按系统账户分批处理，每个系统账户内默认并发为 1；快照未过期不探测，失败后按退避时间更新 `next_refresh_after`，保留旧快照继续展示。
- 429 处理：收到 OAuth Codex 429 时，先解析 header 里已耗尽窗口的 reset 时间；如果 header 不足，再解析响应体 `error.resets_at` 或 `error.resets_in_seconds`；计算出的时间写入账号 `rate_limited` 冷却截止时间，后台下次刷新不早于 reset 时间。
- UI 展示：OAuth 行在“用量情况”里显示本地请求/token/成本摘要，同时额外显示 `5h`、`7d` 两条进度条、百分比、倒计时/刷新时间、快照更新时间、后台刷新状态和下次刷新时间；API Key 行不显示这两条 OAuth 额度进度。
- UI 限制：更多菜单不提供“刷新用量”按钮；快照缺失或过期时显示“等待后台刷新”或“暂无快照”，不触发前端即时探测。

## 账户列表字段

账户列表只展示运维判断需要的信息：

- 账户名称
- 账户类型
- 供应商
- 并发数
- 状态（正常、停用、错误、限流中、临时不可调用）
- 用量情况
- 优先级
- 最近使用时间
- 操作

操作区提供编辑、删除和“更多”菜单；更多菜单第一期包含测试、恢复正常/停用、暂停/恢复调度和切换客户端，不提供“刷新用量”按钮。测试会打开结果弹窗，可选择模型；弹窗终端区域只展示测试过程、成败和模型返回内容，状态码、耗时、请求 URL、代理、原始响应正文等排查字段统一通过完整 JSON 查看，不再额外展示测试结果表格；如果测试响应里带有 Codex rate-limit header，可作为副作用更新 OAuth 额度快照。

## 统计口径

- 网关会记录命中账户、API Key、分组、模型、状态、IP、首 token、总耗时、错误和 token。
- OpenAI JSON 响应读取 `usage.input_tokens`、`usage.output_tokens` 和 `usage.input_tokens_details.cached_tokens`。
- OpenAI SSE 响应读取 `response.completed` / `response.done` / `response.failed` 事件里的 `response.usage`。
- 成本按 OpenAI 官方 API 价格表做轻量估算；没有覆盖的模型先只记 token。
- OpenAI OAuth `5h` / `7d` 额度进度来自上游 Codex 限制快照，只用于判断账号剩余额度和恢复时间，不计入 `usage_records.cost_usd`，也不替代本地 token 用量统计。



