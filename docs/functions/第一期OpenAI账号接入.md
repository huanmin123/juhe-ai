# 第一期：OpenAI OAuth + API Key

## 范围

第一期只实现 OpenAI 供应商，账户类型只支持：

- OpenAI OAuth
- OpenAI API Key

其他供应商先只保留架构扩展位，不实现页面和接口。

对外中转入口统一使用 OpenAI 兼容协议：客户端 Base URL 填本服务 `/v1`，例如开发环境 `http://127.0.0.1:3000/v1`；API Key 填 API 密钥页生成的本地网关密钥。后续即使增加其他主流厂商，也先适配为 OpenAI 兼容请求格式。

流式响应已输出后中断时，服务端无法可靠续写不同客户端的上下文状态，因此不做续写重试，而是按 OpenAI / Codex 可识别的失败事件结束本次 SSE，由客户端按自身上下文进行重试；调研结论见 [流式中断与客户端重试调研](流式中断与客户端重试调研.md)。

## OpenAI 供应商定义

```ts
type ProviderCode = 'openai'

type OpenAIAccountType = 'oauth' | 'api_key'
```

默认能力：

- 模型列表
- Responses API 预留
- 流式响应预留
- 供应商级默认透传

## OpenAI OAuth 创建方式

第一阶段建议先支持“手动录入 OAuth 凭据”，后续再补完整授权跳转和 callback。

表单字段：

- 账户名称
- `access_token`
- `refresh_token`
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 错误策略
- 备注

`expires_at`、`account_id` / `chatgpt_account_id` 属于 OpenAI OAuth token 响应或 token 解析出的系统元数据，不作为用户表单输入项。`account_expires_at` 是本系统的账户套餐到期时间，可选填写，和 OAuth token 的 `expires_at` 不是同一个字段。

保存要求：

- token 加密存储
- `refresh_token` 按凭据指纹做数据库全局唯一约束，不能被其他系统账户重复添加；无 `refresh_token` 时兜底约束 `access_token`
- 列表不展示 Access Token 与 Refresh Token，编辑弹窗可查看和修改
- `expires_at` 由后端根据 OpenAI 返回的 `expires_in` 自动计算和刷新
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 可手动启用 / 停用
- `refresh_token` 只对 OAuth 账户需要，账户列表不展示

## OpenAI API Key 创建方式

表单字段：

- 账户名称
- `api_key`
- `base_url`
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 错误策略
- 备注

保存要求：

- API Key 加密存储
- API Key 按凭据指纹做数据库全局唯一约束，不能被其他系统账户重复添加
- 列表不展示 API Key，编辑弹窗可查看和修改
- `base_url` 默认使用 OpenAI 官方地址
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 可手动启用 / 停用

透传策略：OpenAI 账户默认按供应商网关策略透传，用户侧不提供开关；服务端只保留本地鉴权、账号调度、上游认证替换、安全头剔除、流式转发和错误兜底等必要中转职责。

## 账户归属分组

第一期的关系规则：

- 账户在创建 / 编辑时主动选择归属分组
- 一个账户同一时间归属零个或一个分组
- 账户所有者可以把账户授权给其他系统账户使用；授权不改变账户所有者，也不复制凭据
- 被授权用户可以把授权账户加入自己的同供应商分组，像使用自己账户一样参与调度；账户页提供“绑定分组 / 调整分组”入口，绑定成功后自己的 API Key 才能通过该分组调度该授权账户
- 被授权用户不能编辑、删除、查看敏感凭据、修改代理/并发/错误策略/状态/调度配置，也不能继续转授权
- 统一授权管理支持分组授权；分组所有者可以把整个分组授权给系统账户或系统团队使用，授权共享该分组内当前全部可共享账户
- 被授权用户可以把自己的 API Key 绑定到授权分组，但不能编辑分组、增删账户、调整权重或继续转授权
- 分组可以汇总多个 OpenAI 账户
- API Key 绑定一个自有分组；统一授权完成后也可以绑定个人授权或团队授权给自己的分组
- 请求进入后只能使用该 API Key 对应分组内的账户

## 页面优先级

1. 供应商页：展示 OpenAI、支持的账户类型和模型价格目录
2. 账户页：OpenAI OAuth / API Key 创建、编辑、状态切换和授权来源展示
3. 分组页：维护分组基础信息、查看账户数量与聚合状态和授权来源展示
4. 授权管理页：统一维护账户 / 分组授权、系统账户 / 团队授权对象和授权消耗聚合
5. API Key 页：创建密钥并绑定自有分组，统一授权完成后可绑定授权分组
6. 代理页：维护代理并给账户选择
7. 使用记录页：先做空状态和列表结构
8. 系统设置页：先做基础配置占位

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
11. `GET /api/authorizations`
12. `POST /api/authorizations`
13. `DELETE /api/authorizations/:id`
14. `GET /api/authorizations/:id/usage`
15. `GET /api/system-teams`
16. `POST /api/system-teams`
17. `PATCH /api/system-teams/:id`
18. `POST /api/system-teams/:id/members`
19. `DELETE /api/system-teams/:id/members/:memberId`
20. `GET /api/api-keys`
21. `POST /api/api-keys`
22. `PATCH /api/api-keys/:id`
23. `DELETE /api/api-keys/:id`
24. `GET /api/proxies`
25. `GET /api/proxies/options`
26. `POST /api/proxies`

## 暂不做

- 其他供应商
- 完整 OAuth 授权 callback
- 非 OpenAI 兼容协议的专用网关
- 复杂计费
- 多租户用户体系
- 自动账号健康检测



## OpenAI OAuth 授权方式

`juhe-ai` 第一阶段为 OpenAI OAuth 账户实现两种轻量授权方式：

### 手动授权

1. 后端生成 `state`、`code_verifier`、`code_challenge` 和授权链接。
2. 前端打开 `https://auth.openai.com/oauth/authorize`。
3. 用户登录 OpenAI 后浏览器会跳转到 `http://localhost:1455/auth/callback`。
4. 如果本机没有监听该端口，浏览器显示连接失败也没关系，复制地址栏完整 URL。
5. 前端把回调 URL 提交给后端，后端校验 `state` 并用 PKCE `code_verifier` 换取 token；Client ID 与 Redirect URI 使用后端内置默认值，不暴露给用户填写。
6. 创建 OpenAI OAuth 账户，保存 `access_token`、`refresh_token`、`expires_at`、`client_id`、邮箱和可选的 `account_expires_at`。
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
- API Key 账户继续按账户 `base_url` 转发，默认指向 `https://api.openai.com/v1`，承接通用 OpenAI `/v1/*` 兼容请求。
- OAuth 账户不把 `access_token` 当作官方 OpenAI API Key 打到 `api.openai.com/v1`；真实转发走 ChatGPT / Codex backend 专用链路 `https://chatgpt.com/backend-api/codex`。
- OAuth Codex 网关第一阶段只支持 Codex 原生 `POST /responses` 和 `POST /responses/compact`，暂不做 `/chat/completions` 到 Responses 的重型协议翻译。
- `GET /v1/models` 由本地 OpenAI 模型价格目录返回，不依赖某个上游账号是否可调度，避免 OAuth-only 分组在客户端初始化阶段失败。
- OAuth Codex 转发会补齐必要 Codex CLI 协议头，并在账号凭据包含 `chatgpt_account_id` / `account_id` 时写入 `chatgpt-account-id`；客户端传入的同名头不会透传，避免跨账号伪造。
- 网关发现 OAuth token 即将过期时，会优先用 `refresh_token` 自动刷新并写回账户，作为请求前懒刷新兜底。
- OAuth Access Token 刷新按账户串行执行；刷新前会在锁内重读账户，避免使用缓存里的旧 `refresh_token`。如果 OpenAI 返回 `refresh_token_reused` / `invalid_grant` 且重读后发现账户凭据已经被其他请求或后台任务更新，会采用最新凭据恢复，不把竞争误判为账户失效。
- 后台 worker 另有 `openai-oauth-access-token-refresh` 专职任务，默认每 60 秒扫描启用、可调度、有 `refresh_token` 且 Access Token 距离过期小于 5 分钟的账户，提前刷新并写回账户；预刷新失败但旧 token 仍有效时按退避等待，旧 token 已过期或缺失时把账户临时冷却。
- 后台 OAuth 额度快照刷新任务通过本地 OpenAI 网关链路发起模型请求；如果发现 access token 即将过期，也仍会由网关准备上游账号时按正常请求规则刷新授权。
- OAuth token 响应里的 `expires_in` 只用于计算 `credentials.expires_at`，表示 access token 过期时间；账户购买/套餐到期时间使用单独的 `account_expires_at`。
- 账户 `account_expires_at` 到期后直接停用、关闭调度，不再参与网关选号或后台 OAuth 额度探测。
- 账户页不提供常驻“刷新授权”或“刷新用量”按钮；授权续期和额度快照都由真实请求与后台任务维护。
- OAuth token 刷新、账户测试、后台冷却复测和后台额度探测会优先使用账户绑定的代理；没有绑定代理时默认直连。账户测试、后台冷却复测和后台额度探测必须复用本地 OpenAI 网关模型请求链路并写入使用记录，不能在测试/检测服务里单独直连上游。迁移旧账户时不再自动创建或绑定本机固定端口代理，避免换电脑或服务器部署后误连本机端口。

## 会话亲和调度

OpenAI 网关使用短期内存会话亲和，只影响账号排序，不绕过本地 API Key、分组授权、账号状态、冷却、到期时间、并发、错误策略和上游可用性判断。

- 会话标识来源包括请求头或请求体里的 `previous_response_id`、`session_id`、`conversation_id`、`prompt_cache_key`，以及 `metadata.session_id`、`metadata.conversation_id`、`metadata.user_id`。
- 亲和键按 `system_account_id + api_key_id + group_id + session` 隔离，避免不同本地 API Key、分组或系统账户共享同一个上游会话绑定。
- 首次成功命中账号后写入短期绑定；同一会话后续请求优先尝试同一账号，降低 Codex / Responses 多轮会话被调度到不同 OAuth 账号的概率。
- 绑定只保存在进程内存中，服务重启、缓存淘汰、账号失败、流式首包失败、流式中断、冷却、停用或到期都会自然失效或被清理。

### OpenAI OAuth 额度进度

OpenAI OAuth 账户受上游 Codex/ChatGPT 使用窗口限制，常见窗口包括约 `5h` 窗口和 `7d` 窗口；这类额度不是 API Key 的 token / 成本用量，必须单独展示和处理。

- 数据来源优先使用真实网关请求或账号测试返回的 Codex rate-limit 响应头：`x-codex-primary-used-percent`、`x-codex-primary-reset-after-seconds`、`x-codex-primary-window-minutes`、`x-codex-secondary-used-percent`、`x-codex-secondary-reset-after-seconds`、`x-codex-secondary-window-minutes`。后台额度快照探测也通过同一条网关模型请求链路获取响应头，因此产生的请求、token 和成本会按正常使用记录统计。
- 归一化规则：优先按 `window_minutes` 判断窗口，较小窗口映射为 `5h`，较大窗口映射为 `7d`；只有单侧窗口时，`<= 360` 分钟归为 `5h`，更长归为 `7d`；没有窗口长度时兼容旧语义，默认 primary 为 `7d`、secondary 为 `5h`。
- 存储字段建议保存为账号运行态快照，并按 `system_account_id + account_id + kind` 隔离：`codex_5h_used_percent`、`codex_5h_reset_after_seconds`、`codex_5h_reset_at`、`codex_5h_window_minutes`、`codex_7d_used_percent`、`codex_7d_reset_after_seconds`、`codex_7d_reset_at`、`codex_7d_window_minutes`、`codex_usage_updated_at`、`last_attempt_at`、`last_success_at`、`next_refresh_after`、`refresh_status`、`last_error_message`。
- 获取策略：列表只读已缓存快照，不因展示批量探测；新建 OAuth 账户会在创建流程里立即触发一次首次快照刷新，缺失、过期或接近恢复点的快照由后台定时器统一探测。探测使用本地 OpenAI 网关内部请求执行 `/v1/responses`，由网关转换到 Codex Responses 上游，使用 `stream: true`、`store: false`，并复用账号代理、错误策略、成本估算和使用记录写入。
- 后台策略：按系统账户分批处理，每个系统账户内默认并发为 1；快照未过期不探测，失败后按退避时间更新 `next_refresh_after`，保留旧快照继续展示。
- 429 处理：收到 OAuth Codex 429 时，先解析 header 里已耗尽窗口的 reset 时间；如果 header 不足，再解析响应体 `error.resets_at` 或 `error.resets_in_seconds`；计算出的时间写入账号 `rate_limited` 冷却截止时间，后台下次刷新不早于 reset 时间。
- UI 展示：OAuth 行在“用量情况”里显示本地请求/token/成本摘要，同时额外显示 `5h`、`7d` 两条进度条、百分比、倒计时/刷新时间、快照更新时间、后台刷新状态和下次刷新时间；API Key 行不显示这两条 OAuth 额度进度。
- UI 限制：更多菜单不提供“刷新用量”按钮；快照缺失或过期时显示“等待后台刷新”或“暂无快照”，不触发前端即时探测。

## 账户列表字段

账户列表只展示运维判断需要的信息：

- 账户名称
- 来源：自有账户 / 授权账户
- 所有者摘要：仅授权账户展示
- 账户类型
- 供应商
- 并发数
- 账户到期时间
- 状态（正常、停用、错误、限流中、临时不可调用）
- 用量情况
- 优先级
- 最近使用时间
- 操作

操作区提供编辑、删除和“更多”菜单；更多菜单第一期包含测试、迁移流量、停用/启用账户、恢复正常和切换客户端，不再提供分散的授权管理入口，授权统一进入 `授权管理` 菜单维护。迁移流量用于人工处理上游返回状态码正常但内容异常、自动错误策略未识别的情况；弹窗展示当前账户、同分组可用目标账户和迁移后原账户状态，默认把原账户改为临时不可调用，也可指定为停用账户。该动作只影响后续请求，不主动打断当前正在输出的流式连接。手动启用只针对真正已停用的账户；`限流中` 和 `临时不可调用` 可通过更多菜单的“恢复正常”手动清理冷却与最近错误并恢复调度，也可由手动测试成功或冷却到期后的后台复测成功恢复。复测失败会继续保持临时不可调用。测试入口仍是不验证当前列表状态的恢复口子，不受当前状态、`schedulable` 标记或冷却时间限制；只要账户仍绑定分组且凭据可读取，就固定测试当前账号。授权账户只保留使用相关操作，隐藏编辑、删除、授权管理和所有配置修改入口。测试会打开结果弹窗，可选择模型；弹窗终端区域展示测试过程、成败、模型返回内容，并在结束行内显示总耗时；状态码、请求 URL、代理、原始响应正文等排查字段统一通过完整 JSON 查看，不再额外展示测试结果表格。测试必须复用正常客户请求的网关调度、代理、OAuth 刷新、错误策略、用量解析和成本统计链路，并固定只测试当前账号；如果测试响应里带有 Codex rate-limit header，可作为副作用更新 OAuth 额度快照。

## 统一授权管理

OpenAI 一期不再分别设计账户授权弹窗和分组授权弹窗，授权统一进入 `授权管理` 菜单。完整规则见 [系统团队与统一授权设计](系统团队与统一授权设计.md)。

- 授权资源支持 AI 账户和分组。
- 授权对象支持系统账户和系统团队。
- 新增授权时选择资源类型、自有资源、授权对象类型、系统账户或团队和备注。
- 收回授权只把授权状态改成 `revoked`，不删除历史行；被授权用户已绑定的授权账户分组关系和授权分组 API Key 关系保留但运行时不可用，重新授权同一用户后可按同一稳定授权 ID 恢复使用。
- 使用统计按统一授权 ID 聚合展示请求次数、成功次数、错误次数、输入 Token、输出 Token、缓存读取 Token、总 Token、成本、最后使用时间和最近模型；成功、失败、账户测试和后台检测都按同一套网关使用记录聚合，未产生 token / cost 的失败记录按 0 token、0 cost 计入请求和错误次数。
- 团队授权展开为成员用户授权；统计仍按“资源 × 用户”展示，团队视图只是成员用户用量的筛选汇总。
- 授权消耗统计不包含资源归属人自己的自用消耗；资源归属人的账户 / 分组用量情况仍展示全部总消耗。
- 分组授权是动态使用权，分组所有者后续新增、移除或停用可共享账户，会直接影响被授权用户通过该分组可调度的账户集合。
- 授权分组只共享分组所有者自有账户；如果分组里包含别人授权来的账户，不能通过分组授权继续共享给第三方。
- 资源所有者只能看授权聚合用量，不能看被授权用户的请求快照、响应快照、客户端 IP、API Key 明文或业务请求内容。
## 统计口径

- 网关会记录调用方系统账户、账户所有者、分组所有者、统一授权 ID、授权对象类型、命中账户、API Key、分组、模型、状态、IP、首 token、总耗时、错误和 token。
- 授权账户调用时，请求日志归实际调用方；账户真实用量按同一个 `account_id` 统一累计；授权管理用量按 `account_authorization_id` 或被授权用户聚合。
- 授权分组调用时，请求日志归实际调用方；分组真实用量按同一个 `group_id` 统一累计；分组授权管理用量按 `group_authorization_id` 或被授权用户聚合。
- OpenAI JSON 响应读取 `usage.input_tokens`、`usage.output_tokens` 和 `usage.input_tokens_details.cached_tokens`。
- OpenAI SSE 响应读取 `response.completed` / `response.done` / `response.failed` 事件里的 `response.usage`。
- 成本按 OpenAI 官方 API 价格表做轻量估算；没有覆盖的模型先只记 token。
- OpenAI OAuth `5h` / `7d` 额度进度来自上游 Codex 限制快照，只用于判断账号剩余额度和恢复时间，不计入 `usage_records.cost_usd`，也不替代本地 token 用量统计。

