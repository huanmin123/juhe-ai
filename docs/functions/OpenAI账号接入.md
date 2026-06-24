# OpenAI 账号接入

## 范围

当前版本启用两个使用 OpenAI v1 协议的供应商，并通过 OpenAI-compatible 入口对外提供兼容网关：

- `openai`：通用 OpenAI-compatible 供应商，只支持 API Key 透传；模型目录聚合自身和显式纳入聚合的 OpenAI-compatible 子供应商模型，不自动包含 DeepSeek 等独立供应商。
- `gpt`：GPT 子供应商，父供应商为 `openai`，支持 GPT API Key 和 GPT OAuth，并叠加 Codex Responses 等 GPT 专属能力；模型目录只看 GPT 自身模型。

智谱 GLM 的接入细节单独写在 [智谱 GLM 账号接入](智谱GLM账号接入.md)，DeepSeek 的接入细节单独写在 [DeepSeek 账号接入](DeepSeek账号接入.md)。本文只维护 OpenAI 与 GPT 语义，避免把 GLM、DeepSeek 的厂商兼容差异和 OpenAI / GPT 账户接入混在一起。

这里的 `openai` 有两种层级语义：`protocolCode=openai` 表示客户端入口和上游适配遵循 OpenAI-compatible / v1 形态；`providerCode=openai` 表示通用 OpenAI-compatible 供应商。两者同名但字段不同，不能混淆。AI 账户、分组、模型目录和价格目录归属在供应商层；后续如果增加 Qwen 等 OpenAI-compatible 厂商，应新增各自供应商编码并声明 `protocolCode=openai`、`protocolVersion=v1`。智谱 GLM 和 DeepSeek 虽然都提供 OpenAI-compatible surface，但已按独立专题接入。

本文覆盖的可创建和可调度供应商协议档案有两个：`profile_openai_openai_v1` 和 `profile_gpt_openai_v1`。GLM 档案 `profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1` 与 `profile_glm_coding_anthropic_v1` 单独见 [智谱 GLM 账号接入](智谱GLM账号接入.md)；DeepSeek 档案 `profile_deepseek_openai_v1` 与 `profile_deepseek_anthropic_v1` 单独见 [DeepSeek 账号接入](DeepSeek账号接入.md)。账户、分组、账号测试任务、导入协议和公开推送接口都必须带上或由供应商解析出档案；后端会把档案冗余为 `providerProtocolProfileId`、`protocolCode` 和 `protocolVersion` 返回给前端和外部接口。`providerCode` 只说明供应商，不能单独表达上游协议、端点族和客户端策略。

对外中转入口统一使用 OpenAI 兼容协议：客户端 Base URL 可填服务根地址或 `/v1`，例如开发环境 `http://127.0.0.1:3000` 或 `http://127.0.0.1:3000/v1`；API Key 填 `API Key 管理` 或 `我的 API Key` 页面生成的本地网关密钥。后续即使增加其他主流厂商，也先适配为 OpenAI 兼容请求格式。

“OpenAI 兼容”在这里指本地客户端入口、认证方式和返回错误结构尽量兼容 OpenAI 协议；不同账户类型的上游能力不完全相同，具体以能力矩阵为准。

| 账户类型 | 上游链路 | 当前路径策略 | 主要限制 |
| --- | --- | --- | --- |
| 通用 OpenAI 兼容 API Key | 任意 OpenAI-compatible API，默认上游归一到 `/v1/*` | 网关不维护逐路径白名单；客户端访问根路径或 `/v1/*` 时，都会按账户 `base_url` 归一到上游 `/v1` 后透传。`GET /models` / `/v1/models` 由本地模型目录直接返回。 | 只支持 `api_key`；请求体默认 raw passthrough；本地过滤危险 header 和本地认证 header；不启用 GPT OAuth、Codex adapter 或 Codex 专属客户端策略。某条路径最终能否成功取决于该 API Key 上游 `base_url` 本身是否支持。 |
| GPT API Key | 公开 OpenAI-compatible API，默认上游归一到 `/v1/*` | 网关不维护逐路径白名单；客户端访问根路径或 `/v1/*` 时，都会按账户 `base_url` 归一到上游 `/v1` 后透传，例如 `/responses`、`/v1/responses`、`/chat/completions`、`/v1/chat/completions`。`GET /models` / `/v1/models` 由本地模型目录直接返回。 | 账号能力同时覆盖 OpenAI 标准和 Codex Responses；只有请求被识别为 Codex Responses 客户端形态时才改写 `/responses` 请求，普通 OpenAI Responses 请求仍按 raw body 透传。本地过滤危险 header 和本地认证 header；不自动生成 OpenAI 组织、项目或 Beta 配置。某条路径最终能否成功取决于该 API Key 上游 `base_url` 本身是否支持。 |
| GPT OAuth | ChatGPT / Codex backend 的 `openai_oauth_codex` adapter | 只为 `POST /responses`、`POST /v1/responses`、`POST /responses/compact` 和 `POST /v1/responses/compact` 构造 Codex 上游请求；`GET /models` / `/v1/models` 由本地模型目录返回。 | 账号能力固定为 Codex Responses，不提供用户侧切换；不等价于公开 OpenAI API Key；不承诺 `/chat/completions` 到 Responses 的重型协议翻译，也不承诺公开 Responses API 全字段原样进入 Codex backend。OAuth-only 分组请求不支持的路径时不会落到公开 OpenAI API。 |

混合分组中如果同时存在 API Key 账号和 OAuth 账号，某条路径是否可用取决于调度命中的账号类型：OAuth 账号不支持的路径会跳过该账号，仍可由同分组内可用的 API Key 账号承接；如果当前 API Key 还绑定了后续号池，且当前分组没有任何账号类型能承接该路径，网关应继续尝试下一号池；如果所有号池都没有可用账号，则返回“没有可用的上游账户”一类网关错误，而不是自动协议翻译。多号池混合规则见 [API Key 多分组路由设计](APIKey多分组路由设计.md)。

`GET /models` / `GET /v1/models` 始终读取本地供应商模型目录，不请求某个上游账号。默认 OpenAI-compatible 客户端返回标准 `{"object":"list","data":[...]}`；Codex 模型刷新请求会携带 `client_version` query 参数，或带有可识别的 Codex `originator` / User-Agent，此时返回 Codex `{"models":[...]}` 包装和 `ModelInfo` 字段，避免 Codex 客户端初始化阶段把标准 OpenAI 列表解析失败。两种响应使用同一套本地可见模型目录，只改变客户端响应形态。

单次流式响应收到首段上游内容后，如果本次响应超过输出停顿上限仍没有任何上游新数据，或连接读取异常中断，持续有 raw chunk 但暂未形成完整 SSE 事件时只记录诊断并继续转发。当前运行时对下游尚未提交的流式失败优先做服务端内部重试：关闭当前上游，本次请求排除失败账号，继续尝试后续账号 / 后续分组；只有服务端可承接账号耗尽后，才按客户端策略写最终失败。Codex 的 `response.failed/upstream_retryable_error` 只在命中 Codex profile、Responses SSE 和可解析的 `x-codex-turn-metadata.turn_id` 时作为最终可见兜底；未命中 Codex profile 的 OpenAI-compatible 请求不伪造 Codex 可重试码。调研结论见 [流式中断与客户端重试调研](流式中断与客户端重试调研.md)。

Codex Responses SSE 请求如果在建流前遇到上游 HTTP 非 `2xx`，仍先进入普通账户错误处理、同账号确认、切后续账号和后续分组流程；当所有可承接账号都失败时，命中 Codex profile 的最终响应不返回裸 `503/429/400` JSON，而是返回 `200 text/event-stream`，写入 `response.failed/upstream_retryable_error` 后立即结束连接，防止 Codex 客户端因初始化或建流阶段错误断开整轮。该兜底只处理已通过本地认证、额度、JSON 校验和调度预检后的上游耗尽；无效本地 API Key、缺少 Bearer、本地 JSON 非法、额度不可用等本地硬失败仍按 OpenAI-compatible JSON 错误返回。

## 协议与供应商定义

```ts
type ProtocolCode = 'openai'
type ProtocolVersion = 'v1'
type ProviderCode = 'openai' | 'gpt'
type ProviderProtocolProfileId = 'profile_openai_openai_v1' | 'profile_gpt_openai_v1'
type EndpointFamily = 'chat_completions' | 'responses'
type AccountSupportedEndpointMode = 'chat_json' | 'chat_sse' | 'responses_json' | 'responses_sse'
type OpenAICompatibleAccountType = 'api_key'
type GptAccountType = 'api_key' | 'oauth'
```

上面的类型块只表达本文覆盖的 OpenAI / GPT 范围；GLM 已扩展为 `ProviderCode = 'glm'`，并包含 `profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1` 与 `profile_glm_coding_anthropic_v1`，详见 GLM 专题文档；DeepSeek 已扩展为 `ProviderCode = 'deepseek'`，并包含 `profile_deepseek_openai_v1` 与 `profile_deepseek_anthropic_v1`，详见 DeepSeek 专题文档。

默认协议定义：

- `protocolCode`: `openai`
- `protocolVersion`: `v1`
- `endpointFamilies`: `chat_completions`、`responses`

内置供应商定义：

- `openai`：通用 OpenAI-compatible 供应商，作为同协议优化的父层，只提供 API Key 透传，模型目录聚合自身和显式纳入聚合的 OpenAI-compatible 子供应商模型；DeepSeek 等独立供应商不进入该聚合目录。
- `gpt`：GPT 子供应商，`parent_code = openai`，继承通用 OpenAI-compatible 能力，并叠加 GPT OAuth / Codex 专属能力，模型目录只看 GPT 自身模型。

内置供应商协议档案：

- `profile_openai_openai_v1`：`providerCode = openai`，`accountTypes = api_key`，用于通用 OpenAI-compatible 透传。
- `profile_gpt_openai_v1`：`providerCode = gpt`，`accountTypes = api_key / oauth`，用于 GPT API Key 透传和 GPT OAuth / Codex 适配。

默认能力：

- Responses
- Chat

账户级接口能力限制保存于凭据的非敏感字段 `credentials.supported_endpoint_modes`，用于表达当前上游实际支持的 OpenAI v1 请求形态：`chat_json`、`chat_sse`、`responses_json`、`responses_sse`。网关候选账号筛选和账户测试都必须遵守该矩阵；例如只支持 `chat_json/chat_sse` 的通用 OpenAI-compatible 上游不会参与 `/v1/responses` 调度，手动测试也会改用 `/v1/chat/completions`。省略该字段时，通用 `openai` API Key 默认启用 Chat JSON/SSE，GPT API Key 默认四项全开，GPT OAuth 默认 Responses JSON/SSE。Codex Responses 请求必须命中同时具备 `responses_sse` 和 Codex Responses 兼容能力的账号；OAuth 账户只能选择 Responses JSON/SSE，不支持 Chat Completions。

默认测试模型：

- `gpt-5.5`，通过供应商定义的 `default_test_model` 提供给前端账户测试和后台系统复测兜底使用。

## GPT OAuth 创建方式

当前支持手动授权链接和直接粘贴 Refresh Token 两种创建方式。

表单字段：

- 账户名称
- `access_token`
- `refresh_token`
- 支持模型（可选，不选择表示不限制）
- 账户标签（可选，可输入新标签）
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 时间计划（可选，按系统时区在指定星期和时间段内参与调度）
- 备注

`expires_at`、`account_id` 属于 OpenAI OAuth token 响应或 token 解析出的系统元数据，不作为用户表单输入项。`account_expires_at` 是本系统的账户套餐到期时间，可选填写，和 OAuth token 的 `expires_at` 不是同一个字段。

保存要求：

- token 加密存储
- 新建 OAuth 账户默认写入 `status = pending_test` 且 `schedulable = false`，不参与调度；创建后必须手动测试通过才恢复为正常。OAuth token 刷新成功只更新凭据，不自动把待测试账户恢复正常。
- OAuth 账户允许重复添加相同凭据；同一个 `refresh_token` 或兜底 `access_token` 可以创建多个账户。系统只保留凭据指纹用于排查相同 token，不承担唯一约束。
- 列表不展示 Access Token 与 Refresh Token，编辑弹窗可查看和修改
- `expires_at` 由后端根据 OpenAI 返回的 `expires_in` 自动计算和刷新
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 时间计划不改写账户 `status`；后台同步任务维护 `availability_schedule_active` 派生字段，网关候选 SQL 只读取该字段。后台只在计划开始 / 结束边界自动切换该派生字段，时段外也可通过“提前启用”或提交 `availabilityScheduleActive: true` 立即参与调度，计划内也可通过“提前关闭”或提交 `availabilityScheduleActive: false` 立即退出调度；后续仍按下一次计划边界自动切换。时区跟随系统默认值，用户表单不提供时区配置。
- 可手动启用 / 停用
- `refresh_token` 只对 OAuth 账户需要，账户列表不展示

## GPT API Key 创建方式

表单字段：

- 账户名称
- `api_key`，新增账户时支持在同一个账户内配置多个 API Key；多个 Key 共享同一 Base URL 和账户调度配置
- `base_url`
- 支持模型（可选，不选择表示不限制）
- 账户标签（可选，可输入新标签）
- 代理
- 并发上限
- 账户到期时间（可选，套餐/账号购买到期时间）
- 时间计划（可选，按系统时区在指定星期和时间段内参与调度）
- 备注

保存要求：

- API Key 加密存储；单 Key 账户保存 `credentials.api_key`，多 Key 账户额外保存 `credentials.api_keys`、`credentials.api_key_strategy` 和可选 `credentials.api_key_weights`
- 新增 API Key 账户时默认展示一个 API Key 输入框；可继续添加输入框，也可粘贴多行文本，前端会提取 `sk-` 开头的密钥并生成多条输入。多个密钥只创建一个账户，复用同一 Base URL、分组、代理、支持模型、时间计划和错误处理策略。
- 只有配置多个 API Key 时才显示账户内 Key 策略；默认 `round_robin` 轮询，每次请求在该账户内部选择下一个上游 Key；可切换为 `weighted_round_robin`，按每个 Key 的 `1-100` 权重做平滑加权轮询。账户内 Key 选择发生在系统已选中该账户之后，不改变分组内账户切号、并发和授权边界。后续 Key 级故障隔离按 [账户内 API Key 故障隔离设计](账户内APIKey故障隔离设计.md) 落地：Key 失败后只摘除当前 Key 并让当前请求切后续账户，不在同一请求内遍历账户内全部 Key。
- 新建 API Key 账户默认写入 `status = pending_test` 且 `schedulable = false`，不参与调度；只有在创建前对同一份草稿完成账户测试且测试成功，创建请求才可以携带该测试任务 ID 直接落成正常账户。
- API Key 账户允许重复添加相同凭据；同一个固定 API Key 即使指向同一上游域名，也可以创建多个账户。系统只保留凭据指纹用于排查相同 API Key，不承担唯一约束。
- 列表不展示 API Key，编辑弹窗可查看和修改
- `base_url` 默认使用 OpenAI 官方地址
- 高级配置中的“接口能力限制”用于声明该上游支持的 Chat / Responses 与 JSON / SSE 组合；测试和网关调度都会按该配置筛选，不做 Chat 与 Responses 自动互转。
- `base_url` 保存时按 OpenAI-compatible 上游根地址校验：必须是完整绝对地址，协议允许 `http` 和 `https`，主机允许域名和公网 IP；默认仍通过 SSRF 防护拒绝本机、内网、链路本地和保留地址，这类地址只有本地 mock / 回归测试才可通过私网上游放行配置使用。禁止用户名密码、查询参数、片段、反斜杠、协议后多余斜杠、路径连续斜杠、`.` / `..` 路径段和编码后的斜杠。可填写服务根地址或 `/v1` 版本根地址，例如 `https://api.openai.com`、`https://api.openai.com/v1`、`http://103.236.84.213:48222/v1`、`https://example.com/openai`、`https://example.com/openai/v1`；不能填写 `/responses`、`/chat/completions` 等具体接口路径。
- 不提供 `OpenAI-Organization`、`OpenAI-Project` 和 `OpenAI-Beta` 的账号表单配置；组织 / 项目属于 OpenAI 账号上下文，服务端不凭空生成，Beta 由客户端按公开 API 需求显式传入
- `account_expires_at` 表示本地套餐/账号购买到期时间；未填写则不过期，到期后账户自动改为停用并退出调度
- 时间计划不改写账户 `status`；后台同步任务维护 `availability_schedule_active` 派生字段，网关候选 SQL 只读取该字段。后台只在计划开始 / 结束边界自动切换该派生字段，时段外也可通过“提前启用”或提交 `availabilityScheduleActive: true` 立即参与调度，计划内也可通过“提前关闭”或提交 `availabilityScheduleActive: false` 立即退出调度；后续仍按下一次计划边界自动切换。时区跟随系统默认值，用户表单不提供时区配置。
- 可手动启用 / 停用

透传策略：GPT 账户默认按供应商网关策略透传，用户侧不提供通用透传开关；服务端只保留本地鉴权、账号调度、上游认证替换、安全头剔除、流式转发和错误兜底等必要中转职责。GPT API Key 账号同时具备 OpenAI 标准和 Codex Responses 兼容能力；普通 OpenAI 请求继续使用客户端 `rawBody`，只有请求侧 `requestClientCompatibility = codex_responses` 时，才对 `POST /responses` / `POST /v1/responses` 按 Codex Responses 形态补齐请求体和必要流式请求头。Header 会过滤本地认证、代理链路、SDK / tracing 噪声和客户端传入的 `OpenAI-Organization` / `OpenAI-Project`，不从账号凭据生成这些上游账号上下文头。`OpenAI-Beta` 保留客户端显式传入值，服务端不做账号级覆盖。OAuth 账号固定进入 `openai_oauth_codex` adapter，不读取也不接受用户侧账号兼容覆盖。

### 账户支持模型限制

账户可以配置支持模型列表，选项来自账号所有者可见的所属供应商模型目录；不选择任何模型表示不限制。网关调度时在账号池缓存快照内按客户端请求体里的下游 `model` 做内存过滤，只有不限制账号或精确支持该下游模型的账号才进入上游尝试。因模型不匹配被跳过的账号不算失败，不触发账户错误处理策略、本地屏蔽或临时不可调用。

如果分组内所有可用账号都不支持当前请求模型，网关直接返回本地错误，不继续逐个请求上游。`GET /models` / `GET /v1/models` 仍由本地供应商模型目录返回，不参与账号模型限制过滤。账号模型映射发生在选中具体账号之后，只把下游模型改写为该账号的上游模型，不能反向改变本轮模型限制过滤结果。完整设计见 [账户模型限制设计](账户模型限制设计.md) 和 [自定义模型与模型映射设计](自定义模型与模型映射设计.md)。

## 账户分组绑定

当前关系规则：

- `accounts.system_account_id` 表示当前账户行所属系统账户；账户所有者把 AI 账户授权给其他系统账户后，系统会为被授权人创建独立授权实例账户。
- `accounts.provider_protocol_profile_id` 和 `groups.provider_protocol_profile_id` 是账户加入分组、授权实例绑定、API Key 号池路由和网关候选过滤的硬边界；本文覆盖值为 `profile_openai_openai_v1` 和 `profile_gpt_openai_v1`，GLM 当前另有 `profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1` 与 `profile_glm_coding_anthropic_v1`，DeepSeek 当前另有 `profile_deepseek_openai_v1` 与 `profile_deepseek_anthropic_v1`。
- `group_accounts` 表示某个使用方的本地分组绑定；自有账户绑定自有账户 ID，授权账户绑定被授权人自己的授权实例账户 ID，并记录稳定的 `account_authorization_id`。
- 一个账户在同一个使用方作用域内同一时间只保留一个有效分组绑定；自有账户在所有者作用域内创建 / 编辑时选择分组，被授权账户在授权创建时必须绑定被授权用户自己的本地分组，后续通过账户编辑弹窗调整分组和分组内优先级。
- 账户标签按系统账户维度保存，一个账户可以绑定多个标签；新增和编辑弹窗支持从下拉选择已有标签，也支持直接输入新标签。下拉内可删除未绑定任何账户的标签；标签已绑定账户时禁止删除。
- 被授权用户把授权实例加入自己的同协议档案分组后，自己的 API Key 才能通过该分组调度该授权实例；这只是本地调度绑定，不改变授权方原账户的分组绑定。
- 授权实例账户自己的状态、冷却、错误和流失败诊断窗口与授权方原账户隔离；真实网关失败产生的调度降级和事前确认运行态只保存在当前 Web 进程内。当前分组内优先级、超级优先和降级备用来自 `group_accounts.local_priority / local_super_priority_enabled / local_fallback_enabled`，只影响当前使用方当前分组绑定。
- 授权实例运行时的 OpenAI 凭据、`base_url`、账号类型、支持模型、代理、并发、可用时段和账户追加流式规则从来源账户补齐；父账户 API Key、OAuth token、模型或代理更新后，被授权实例列表、测试和网关缓存都应读取最新来源资源事实。
- 被授权用户不能编辑来源账户、查看敏感凭据、修改来源账户代理 / 并发 / 模型 / 凭据 / 账户追加流式规则，也不能继续转授权；被授权用户可以在自己的分组内绑定 / 调整授权实例、执行账户测试、停用或恢复自己的授权实例，并调整当前分组内优先级、超级优先和降级备用。被授权用户不想继续使用个人直授权账户时只能归还个人授权，不删除授权实例账户行或授权方原账户；团队来源授权账户不提供个人归还入口。
- 统一授权管理支持分组授权；分组所有者可以把整个分组授权给系统账户或系统团队使用，授权共享该分组内当前全部可共享账户；有效授权分组可直接作为被授权方新建或编辑 API Key 的号池
- 被授权用户可以把自己的 API Key 直接绑定到有效授权分组；也可以在分组页查看授权分组信息，并本地调整该授权分组的启用状态、分组类型和高并发调度配置。该本地配置只影响当前被授权人的 API Key 号池和网关调度，不修改授权方原分组。
- 授权暂停、过期、回收、归还、额度不可用或被授权人本地停用授权分组时，该号池配置保留但运行态不可用。个人直授权分组可从分组页归还；团队来源授权分组不提供个人归还入口。
- 分组可以汇总多个 GPT 账户，但只能汇总同一供应商协议档案下的账户；不能只凭 `providerCode` 相同或 `protocolCode` 相同混用账户
- API Key 可以绑定调用方自己的一个或多个分组，也可以绑定有效授权给调用方的分组；统一授权提供使用权，不提供编辑、删除或转授权权限
- 请求进入后先按 API Key 号池优先级选择一个可承接分组，再只能使用该分组内调用方有权使用的账户

## 当前页面入口

1. 供应商页：展示 OpenAI 兼容、GPT、支持的账户类型和本地供应商模型目录。
2. 账户页：通用 OpenAI 兼容 API Key、GPT API Key、GPT OAuth 创建、编辑、状态切换、测试、删除、批量删除、授权来源和 OAuth 额度进度展示。
3. 分组页：维护分组基础信息、查看账户数量与聚合状态、绑定自有或授权账户。
4. 统一授权页：统一维护账户 / 分组授权、系统账户 / 团队授权对象、授权到期和授权额度。
5. 团队消耗明细 / 用户消耗明细：按日期范围查看授权团队和最终用户消耗。
6. API Key 页：创建本地网关密钥并绑定当前系统账户可用的一个或多个分组号池，包含自有分组和有效授权分组；授权账户需要先加入该用户自己的分组后再参与自有分组调度。
7. 代理页：维护代理并给账户选择，支持手动代理检测和出口信息缓存。
8. 使用记录页、用量统计页、AI 性能监控页、日志和审计页面：查看 OpenAI 网关请求事实、统计缓存、性能趋势和排障链路。
9. 系统设置页：维护当前白名单内的网关、后台任务、操作日志和数据保留策略。

## 当前接口入口

- 系统后台 API 统一在 `/__aisys__/api/*` 下，用户侧接口使用 `/__aisys__/api/my-*`，管理侧接口使用 `/__aisys__/api/*` 并按需要求管理员权限。
- OpenAI 兼容 / GPT 账号接入相关接口包括 `providers`、`accounts` / `my-accounts`、`openai-oauth` / `my-openai-oauth`、`groups` / `my-groups`、`api-keys` / `my-api-keys`、`authorizations` / `my-authorizations`、`system-teams` / `my-teams`、`proxies` 和 `stats` / `my-stats`。账户、分组和测试相关 DTO 需要返回 `providerProtocolProfileId`、`protocolCode` 和 `protocolVersion`，用于前端筛选、表单默认值、分组绑定和运行时诊断。
- 授权消耗明细接口固定使用 `startDate=YYYY-MM-DD`、`endDate=YYYY-MM-DD`、`page` 和 `pageSize` 参数读取窗口分页，管理侧为 `/__aisys__/api/authorizations/usage/team-details`、`/__aisys__/api/authorizations/usage/user-details`，用户侧为 `/__aisys__/api/my-authorizations/usage/team-details`、`/__aisys__/api/my-authorizations/usage/user-details`。
- 客户端网关入口是根路径和 `/v1/*`，不使用后台登录态，只使用本地 API Key。

## 当前不包含

- 其他供应商的页面创建和网关适配。
- 非 OpenAI 兼容协议的专用客户端网关。
- 重型计费结算系统。
- 多实例 / 多租户商业化体系。
- 面向排序或展示的账号质量主动测速；账号质量只来自真实请求统计。近窗口真实请求频繁失败只会触发后台故障确认探针，确认流量标记为 `cooldown_retest`，不写入普通账号质量样本。



## OpenAI OAuth 授权方式

`juhe-ai` 为 GPT OAuth 账户实现两种轻量授权方式，底层使用 OpenAI OAuth 授权服务：

### 手动授权

1. 后端生成 `state`、`code_verifier`、`code_challenge` 和授权链接。
2. 前端打开 `https://auth.openai.com/oauth/authorize`。
3. 用户登录 OpenAI 后浏览器会跳转到 `http://localhost:1455/auth/callback`。
4. 如果本机没有监听该端口，浏览器显示连接失败也没关系，复制地址栏完整 URL。
5. 前端把回调 URL 提交给后端，后端校验 `state` 并用 PKCE `code_verifier` 换取 token；Client ID 与 Redirect URI 使用后端内置默认值，不暴露给用户填写。
6. 创建 GPT OAuth 账户，保存 `access_token`、`refresh_token`、`expires_at`、`client_id`、邮箱和可选的 `account_expires_at`。
7. 账户落库后不主动请求模型接口获取额度；额度快照等待第一次真实网关请求或账户测试返回 Codex rate-limit 响应头后被动更新。
8. 创建接口允许通过客户端补丁写入账户本地错误处理规则；`access_token`、`refresh_token`、`expires_at`、`client_id` 和 `base_url` 必须以 OpenAI token endpoint 返回或服务端 fallback 为准，不能被请求体覆盖。账户错误处理规则只进入 `credentials.error_handling_rules`。

### Refresh Token 授权

1. 用户直接粘贴已有 `refresh_token`。
2. 后端使用内置默认 Client ID 和 `grant_type=refresh_token` 向 OpenAI token endpoint 刷新。
3. 刷新成功后创建 GPT OAuth 账户。
4. 账户落库后不主动请求模型接口获取额度；额度快照等待第一次真实网关请求或账户测试返回 Codex rate-limit 响应头后被动更新。
5. 如果 OpenAI 没返回新的 `refresh_token`，继续保留用户输入的原始 `refresh_token`。

### 调度与授权刷新

- API Key 账户使用 `credentials.api_key` 作为上游 Bearer token。
- OAuth 账户使用 `credentials.access_token` 作为上游 Bearer token。
- API Key 账户继续按账户 `base_url` 转发，默认指向 `https://api.openai.com/v1`，承接客户端网关 `/*` 和 `/v1/*` 请求；GPT API Key 上游仍归一到 `/v1/*`。
- OAuth 账户不把 `access_token` 当作官方 OpenAI API Key 打到 `api.openai.com/v1`；真实转发走 ChatGPT / Codex backend 专用链路 `https://chatgpt.com/backend-api/codex`，账号兼容能力固定为 Codex Responses。
- OAuth Codex 网关当前支持 Codex 原生 `POST /responses` 和 `POST /responses/compact`，暂不做 `/chat/completions` 到 Responses 的重型协议翻译。
- `GET /v1/models` 由本地供应商模型目录返回，并叠加调用方可见的内置、全局自定义和个人自定义模型；它不依赖某个上游账号是否可调度，避免 OAuth-only 分组在客户端初始化阶段失败。
- OAuth Codex 转发会补齐必要 Codex CLI 协议头，并在账号凭据包含 `account_id` 时写入 `chatgpt-account-id`；客户端传入的同名头不会透传，避免跨账号伪造。
- 网关发现 OAuth Access Token 缺失、过期、过期时间无效或距离过期不超过 5 秒时，会在当前请求内用 `refresh_token` 刷新并写回账户，避免把已不可用 token 发给上游。
- 网关发现 OAuth Access Token 距离过期小于 60 秒但仍超过 5 秒时，不阻塞当前请求；当前请求继续使用现有 Access Token，同时按账号触发一次后台预热刷新。预热刷新按账号去重，同一账号已有预热任务时不会重复排队；预热成功只写回新凭据并清理运行缓存，预热失败只写运行日志，不把当前请求卡在 token endpoint 上。
- OAuth Access Token 刷新按账户串行执行；刷新前会在锁内重读账户，避免使用缓存里的旧 `refresh_token`。刷新成功后 server 进程会短 TTL 记住最近新凭据，同一波临期请求复用该结果，不再逐个重复读写 DB service。如果 OpenAI 返回 `refresh_token_reused` / `invalid_grant` 且重读后发现账户凭据已经被其他请求或后台任务更新，会采用最新凭据恢复，不把竞争误判为账户失效。
- 后台 worker 另有 `openai-oauth-access-token-refresh` 专职任务，默认每 60 秒扫描所有仍存在、未删除、有 `refresh_token` 且 Access Token 距离过期小于 5 分钟的 GPT OAuth 账户，提前刷新并写回凭据；扫描不受 `active`、`pending_test`、`disabled`、`error`、`rate_limited`、`temporary_unavailable` 或 `schedulable` 状态影响。后台预刷新只做 token 保活，成功时不恢复普通冷却状态、不清理无关错误；失败时按退避等待并累计连续失败次数，连续 3 次失败后把非停用、非待测试账户写入 `status = error`，`last_error_code = oauth_token_refresh_failed`，`last_error_message` 记录最近失败摘要，后续后台刷新成功会自动恢复该异常。手动停用账户不会被后台刷新失败覆盖成异常。
- 待测试账户不属于冷却恢复状态；OAuth 后台刷新、后台冷却复测、“恢复正常”和手动启用都不能把 `pending_test` 改成 `active`。只有手动账户测试成功，或创建时引用同一份草稿的成功测试任务，才能让账户进入正常调度。
- 后台不再为了 OAuth 额度快照发起模型请求；额度快照只从真实网关请求或账户测试返回的 Codex rate-limit 响应头被动更新。账户测试和模型检测展示用上游响应体只保留 `256KB` 有界预览，避免 DB service 在诊断请求内同步解析大文本。OpenAI OAuth token endpoint 的响应体同样只允许收集 `256KB`，异常大响应会主动中断并返回错误，避免刷新 token 时无界占用内存。access token 临近过期时，真实网关请求会按 5 秒硬阻塞阈值和 60 秒后台预热阈值处理。
- OAuth token 响应里的 `expires_in` 只用于计算 `credentials.expires_at`，表示 access token 过期时间；账户购买/套餐到期时间使用单独的 `account_expires_at`。
- 账户 `account_expires_at` 到期后直接停用、关闭调度，不再参与网关选号；OAuth 额度快照只会在真实请求命中该账号时被动更新。
- 账户时间计划只作为网关候选过滤条件，不改变手动启用/停用、异常、冷却和到期状态。后台 `account-availability-schedule-status-sync` 维护候选派生字段并在变更后清理 runtime cache；请求热链路不解析计划 JSON，允许一个后台同步周期的切换延迟。
- 账户页不提供常驻“刷新授权”或“刷新用量”按钮；授权续期由请求前懒刷新和后台 Access Token 预刷新维护，额度快照由真实请求响应头被动维护。
- OAuth token 刷新和账户测试会优先使用账户绑定的代理；没有绑定代理时默认直连。账户创建、导入和离线修复都不得自动绑定本机固定端口代理，代理必须由用户显式配置。账户测试必须复用本地 OpenAI 网关模型请求链路并写入使用记录，不能在测试服务里单独直连上游；前端测试模型默认来自账户所属供应商的 `default_test_model`，手动测试成功后把本次成功模型写入账户 `last_successful_test_model`。API Key 账户测试会优先复用最近真实请求的 endpoint/stream 形态，但手动测试模型以显式传入值为准，测试请求形态的临时选择只对 API Key 生效；OAuth 账户测试固定使用 Codex Responses。API Key 账户的 Responses 测试按当前契约不发送 `max_output_tokens`；测试输入由后端使用默认探活输入生成。手动账户测试、后台冷却复测和事前确认探针的诊断等待策略固定为 `10s -> 20s -> 30s` 三次真实网关请求尝试，总等待不超过 60 秒；未保存草稿 OAuth 测试如果需要先刷新 Access Token，刷新请求也纳入同一次诊断 attempt 的等待上限。每次尝试仍使用账号自己的凭据、Base URL、代理、账号兼容能力、分组上下文和请求形态，只按测试成功与否决定是否继续，不按上游状态码、错误码或错误文案分类重试。后台账号质量主动探测能力已删除；后台冷却复测固定启用，复用同一网关模型请求链路去恢复冷却到期的 `temporary_unavailable` 和 `rate_limited` 账号；复测模型优先使用账户 `last_successful_test_model`，没有手动成功记录时使用供应商 `default_test_model`。账号进入冷却态后先按 3 秒进入快速恢复通道，复测失败后按 `3s -> 6s -> 12s -> 24s -> ...` 翻倍；超过快速阈值后退化为慢速恢复通道，单次等待不超过 `defaultTemporaryUnschedulableMinutes` 表达的最大暂停时间。后台复测成功恢复正常；失败会继续按指数退避，超过 `cooldownAccountRetestMaxBackoffHours` 表达的长期不可用观察阈值后保持原状态并按 `cooldownAccountRetestLongTermIntervalHours` 低频复测，`last_error_code` 记为 `cooldown_retest_long_term_unavailable`。恢复探活使用 `traffic_source = cooldown_retest` 写入使用记录和审计，避免污染业务统计、账户质量和真实请求形态学习；写入账号 `last_error_code/last_error_message` 时使用本次上游真实错误摘要，避免把网关最终兜底 503 覆盖成账号原因；如果响应里带有 Codex 额度头，额度快照来源也记录为 `cooldown_retest`，不伪装成真实网关流量。

账号质量主动探测指为排序或展示而主动测速；这类能力仍不恢复。频繁失败确认只由真实网关质量样本触发，按账户所属系统账户和绑定分组上下文执行确认探针；确认成功不改状态，确认失败且属于账号故障时才写入 `temporary_unavailable`。该确认探针使用 `traffic_source = cooldown_retest`，不进入业务统计、账号质量统计或真实请求形态学习。

## 会话亲和调度

OpenAI 网关使用短期内存会话亲和，只影响账号排序，不绕过本地 API Key、分组授权、账号状态、冷却、到期时间、并发、账户错误处理策略和上游可用性判断。

- 会话标识来源包括请求头或请求体里的 `previous_response_id`、`session_id`、`conversation_id`、`prompt_cache_key`，以及 `metadata.session_id`、`metadata.conversation_id`、`metadata.user_id`。
- 亲和键按 `system_account_id + api_key_id + session` 隔离，避免不同本地 API Key 或系统账户共享同一个上游会话绑定；`group_id` 不参与亲和键。
- 手动迁移流量时，会话亲和迁移按源账号、系统账户和可选 API Key 反向索引定位候选绑定，不扫描全部亲和缓存；默认的“不影响原账户”只把已有且当前命中源账号的客户端会话迁到目标账户，不写源账号状态，也不记录整组新请求目标偏向。选择“临时不可调用”或“停用账户”时，才同时按本地系统账户和分组记录短期运行态目标偏向，在源账号仍未回到候选池前，后续请求会优先尝试迁移目标账户。迁移目标偏向是当前作用域内的短期最高排序覆盖，优先于会话亲和、超级优先、主池 / 备用池分层、账号优先级和质量分；但不绕过 API Key / 系统账户 / 分组 / 授权作用域 / 模型 / 状态 / 额度 / 硬并发等候选硬条件。同一 API Key 下不同分组仍共享同一亲和绑定。
- OAuth Codex adapter 写入上游的 `session_id`、`conversation_id` 和 `prompt_cache_key` 也按同一层本地边界隔离，不把上游账号 ID、账号类型或分组 ID 写进隔离 key；同一个本地 API Key 路由下因失败、冷却或并发切换上游账号时，尽量保留客户端会话和 prompt cache 连续性。
- 首次成功命中账号后写入短期绑定；同一会话后续请求在同一调度层级内优先尝试同一账号，降低 Codex / Responses 多轮会话被调度到不同 OAuth 账号的概率。
- 客户可用性优先于粘性：会话亲和不会跨过超级优先、账号优先级和更优质量候选。绑定账号并发满时会先在本请求内做很短的同账号等待和重查，尽量复用上游会话 / 缓存；短等后仍满、账号不可用或请求失败时才让后续候选继续尝试。
- 绑定只保存在进程内存中，服务重启、缓存淘汰、账号失败、流式首包失败、流式中断、冷却、停用或到期都会自然失效或被清理。
- 会话亲和不是客户端身份认证，也不是 Codex 重试计数依据。服务端隐藏重试成功时不记录 Codex turn 失败；只有最终可见的 Codex `upstream_retryable_error` 才进入 turn 级失败账号避让。Codex turn 级策略只能使用可解析的 `x-codex-turn-metadata.turn_id` 加本地 API Key / endpoint / 请求体哈希边界；识别不到时不使用 `session_id` 或 `x-client-request-id` 回退。Codex turn 级切号在选择备用账号前会先做真实账号探针，探针等待档位同样使用 `10s -> 20s -> 30s`；但切号探针只在本地超时时同账号递进等待，一旦拿到明确失败结果就立即淘汰当前候选并尝试下一个账号。

### OpenAI OAuth 额度进度

GPT OAuth 账户受上游 Codex/ChatGPT 使用窗口限制，常见窗口包括约 `5h` 窗口和 `7d` 窗口；这类额度不是 API Key 的 token / 成本用量，必须单独展示和处理。

- 数据来源使用真实网关请求或账号测试返回的 Codex rate-limit 响应头：`x-codex-primary-used-percent`、`x-codex-primary-reset-after-seconds`、`x-codex-primary-window-minutes`、`x-codex-secondary-used-percent`、`x-codex-secondary-reset-after-seconds`、`x-codex-secondary-window-minutes`。后台不再为了额度快照主动探测。
- 归一化规则：只按 `window_minutes` 判断窗口，`<= 360` 分钟归为 `5h`，更长归为 `7d`；没有窗口长度的 primary / secondary 原始 header 只保存原始字段，不生成 `codex_5h_*` 或 `codex_7d_*` 归一化字段。
- 存储字段保存为账号运行态快照，并按 `system_account_id + account_id + kind` 隔离：`codex_5h_used_percent`、`codex_5h_reset_after_seconds`、`codex_5h_reset_at`、`codex_5h_window_minutes`、`codex_7d_used_percent`、`codex_7d_reset_after_seconds`、`codex_7d_reset_at`、`codex_7d_window_minutes`、`codex_usage_updated_at`、`last_attempt_at`、`last_success_at`、`next_refresh_after`、`refresh_status`、`last_error_message`。
- 获取策略：列表只读已缓存快照，不因展示批量探测；新建 OAuth 账户不触发首次快照刷新，缺失或过期时等待真实请求或账户测试的响应头更新。
- 后台策略：不再注册 OAuth 额度快照主动探测任务；后台只保留 Access Token 预刷新。
- 官方限额处理：收到 OpenAI OAuth / Codex 额度响应头时，只更新 OAuth 额度快照和下次刷新参考时间；账号状态仍走统一上游错误处理链路。命中上游账号后发生错误时，先按账户错误处理策略形成待确认目标；未命中策略或策略动作为 `retry_next` 时，统一写当前账号 `temporary_unavailable`，不在代码里内置官方限额、余额不足、状态码、错误码或错误文案判断。
- UI 展示：OAuth 行在“用量情况”里显示本地请求/token/成本摘要，同时额外显示 `5h`、`7d` 两条进度条、百分比、倒计时/恢复时间、快照更新时间和快照来源；API Key 行不显示这两条 OAuth 额度进度。
- 授权展示：OAuth 额度快照是授权实例的非敏感运行态，被授权用户获得该 OAuth 账户使用权后，会在自己的授权实例账户上看到 `5h` / `7d` 额度进度，但仍不能查看 Access Token、Refresh Token 或完整请求内容。
- UI 限制：更多菜单不提供“刷新用量”按钮；快照缺失或过期时显示“等待真实请求更新”或“暂无快照”，不触发前端即时探测。

## 账户列表字段

账户列表只展示运维判断需要的信息：

- 账户名称
- 来源：自有账户 / 授权账户
- 所有者摘要：仅授权账户展示
- 账户类型
- 供应商
- 并发数
- 状态（正常、待测试、停用、异常、限流中、临时不可调用；授权账户额外按有效性展示授权到期、授权已失效、授权额度已用完，账户套餐过期展示账户到期）
- 用量情况
- 标签
- 优先级
- 最近使用时间
- 到期时间（授权账户优先展示授权到期时间；没有授权到期时间时展示账户到期时间）
- 操作

操作区提供编辑、删除和“更多”菜单；更多菜单包含测试、迁移流量、停用/启用账户、时间计划提前启用/提前关闭和恢复正常，不再提供分散的授权入口，授权关系统一到管理侧 `统一授权管理 / 统一授权` 或用户侧 `我的授权 / 授权操作` 维护。编辑弹窗只维护名称、凭据、分组、标签、并发、优先级、代理、过期时间、备注、账户错误处理规则、账户响应检查规则和接口能力限制等配置；客户端兼容只展示账号可承接能力，不提供账号级手工切换，创建、编辑和草稿测试账号 payload 都不得提交 `clientCompatibility`。不提供状态修改；保存编辑时也不得提交 `status`，避免覆盖测试、后台自动恢复、错误处理或网关冷却刚写入的状态。迁移流量用于人工处理上游返回状态码正常但内容异常、自动响应检查或账户错误处理策略未识别的情况；弹窗展示当前账户、同分组可用目标账户和迁移后原账户状态，默认“不影响原账户”，只把已识别且当前命中源账户的客户端会话迁到目标账户；也可把原账户改为临时不可调用或停用账户。该动作只影响后续请求，不主动打断当前正在输出的流式连接；只有选择临时不可调用或停用时，迁移后当前分组才会在源账号仍不可候选时短期偏向目标账户，目标不可用或并发不可承接时继续按原有候选顺序降级。`待测试` 是新建账户的默认隔离状态，不能参与调度，不能通过启用账户或恢复正常绕过测试；手动测试失败仍保持待测试，手动测试通过后才恢复为正常并开启调度。手动启用只针对真正已停用的账户；停用态是人工硬边界，不能被账户测试、后台冷却复测、OAuth 刷新成功或网关异步错误处理自动恢复，也不能被这些后台路径改为临时不可调用。时间计划提前启用/提前关闭只改 `availabilityScheduleActive`，不改 `status`、冷却或错误状态；只对正常自有账户开放，授权账户的可用时段由来源账户控制。`限流中` 和 `临时不可调用` 可通过更多菜单的“恢复正常”手动清理冷却与最近错误并恢复调度，也可由手动测试成功恢复；后台冷却复测固定启用，会在冷却时间到期后复测 `temporary_unavailable` 和 `rate_limited` 账号。复测失败会先短重试确认，再按指数退避延长下一次复测时间；超过长期不可用观察阈值后不会转异常，而是显示“长期不可用”并继续低频自动复测。`异常` 使用 `status = error` 作为统一硬状态，页面状态标签显示“异常”，tooltip 展示 `last_error_code` 对应的异常类型和 `last_error_message` 详情；`oauth_token_refresh_failed` 这类后台刷新异常会在后台刷新成功后自动恢复，其它显式硬异常仍保留编辑（状态锁定为异常）、删除、测试和“恢复异常”入口，非停用自有异常账户手动测试成功也可作为人工恢复入口。授权额度耗尽是授权关系的展示层状态，只显示“授权额度已用完”并由网关按授权额度返回 429，不改变物理 AI 账户状态；只有上游账号本身触发账户错误处理策略 `rate_limited` 时才显示“限流中”。账户套餐到期显示“账户到期”，授权到期或绑定的稳定授权 ID 失效显示“授权到期 / 授权已失效”。

“频繁失败”和“近期不稳”是账号质量反馈标签，不是新的物理状态；状态筛选仍按 `status`、冷却和实际可用性判断。频繁失败标签会触发后台故障确认，确认失败后才会升级为“临时不可调用”，确认成功则继续保持正常。

批量工具栏支持“批量恢复”，只处理选中账户中可恢复的异常、限流、临时不可调用、冷却或网关运行态避让账户；动作逐个复用单账户恢复语义，跳过待测试、停用、到期、授权失效、授权暂停或无权恢复的账户。授权实例恢复只清理被授权用户自己的实例运行态，不修改授权方原账户。

自有账户测试入口不因停用状态隐藏；停用账户仍可手动测试凭据、代理和上游链路，但测试结果只作为诊断，不会恢复或改写停用状态。在非停用状态下，自有账户测试不受 `schedulable` 标记或冷却时间限制，只要账户仍绑定分组且凭据可读取，就固定测试当前账号；测试成功会清理待测试、临时不可调用、限流、异常、不可调度和最近错误状态并恢复正常调度。授权账户测试还必须满足授权可用、额度未耗尽、已绑定当前用户分组、账户未到期，停用的授权实例同样可以保留测试入口用于诊断；授权用户测试时只返回脱敏后的状态码、耗时、成败和简短错误，完整上游响应头、响应体、上游地址、代理和请求体诊断只对所有者或管理员开放。授权账户只保留使用相关操作、授权实例状态、本地分组调度入口和个人直授权归还入口；授权实例账户不能删除，归还个人授权只隐藏自己的授权实例并把授权状态标记为 `returned`，不删除授权方原账户。

测试会打开结果弹窗，可选择模型；弹窗终端区域展示测试过程、后台任务状态、排队等待、`10s + 20s + 30s` 运行等待窗口、成败、模型返回内容，并在结束行内显示总耗时；所有者或管理员可通过完整 JSON 查看状态码、请求 URL、代理、原始响应正文等排查字段，不再额外展示测试结果表格。测试必须复用正常客户请求的网关调度、代理、OAuth 刷新、账户错误处理策略、用量解析和成本统计链路，并固定只测试当前账号；测试接口会等待本次测试使用记录、审计和必要副作用写入完成后再回读状态。

人工测试或批量测试失败不会直接把当前账号写为 `temporary_unavailable`。如果失败不是分组绑定、凭据读取、取消、权限、授权额度或其它配置 / 操作问题，且目标仍为正常可调度的自有账号或授权实例，则先进入事前确认运行态；确认探针成功时清理运行态并保持原状态，确认探针连续失败后才写入 `temporary_unavailable` 和冷却时间。`pending_test` 测试失败继续保持待测试，测试成功才进入正常调度；`disabled` 测试失败只保留诊断；`error` 测试成功可以作为人工恢复入口，失败不改成临时不可调用。授权实例确认和落库只影响当前被授权用户自己的实例账户，不改授权方原账户，也不写 `group_accounts.local_*`。如果测试响应里带有 Codex rate-limit header，可作为副作用更新 OAuth 额度快照。

## 统一授权管理

GPT 账户不再分别设计账户授权弹窗和分组授权弹窗，授权关系统一进入管理侧 `统一授权管理 / 统一授权` 或用户侧 `我的授权 / 授权操作`。完整规则见 [系统团队与统一授权设计](系统团队与统一授权设计.md)。

- 授权资源支持 AI 账户和分组。
- 授权对象支持系统账户和系统团队。
- 新增授权时选择资源类型、自有资源、授权对象类型、系统账户或团队和备注。
- 回收授权只把授权状态改成 `revoked`，归还授权只把授权状态改成 `returned`，都不删除历史行；被授权用户已绑定的授权账户分组关系和 API Key 授权分组绑定关系会保留但运行时不可用，重新授权同一用户后可按同一稳定授权 ID 恢复使用。
- 分组页对授权分组提供本地使用配置编辑和个人直授权归还入口。被授权人只能修改自己的 `enabled`、`groupType` 和 `schedulingPolicy`，不能修改授权方原分组的名称、供应商、说明、默认状态或账户集合；个人直授权分组归还后只隐藏被授权人侧可见资源，不删除授权方原分组。
- 使用统计按统一授权 ID 聚合展示请求次数、成功次数、错误次数、输入 Token、输出 Token、缓存读取 Token、总 Token、成本、最后使用时间和最近模型；统计 worker 只聚合 `traffic_source = gateway` 的真实网关请求和 `manual_account_test` 的手动账户测试，`cooldown_retest` 后台恢复探活只保留明细和审计，不进入授权消耗统计、业务统计或账户质量统计。未产生 token / cost 的失败记录按 0 token、0 cost 计入对应统计口径的请求和错误次数。
- 团队授权展开为成员用户授权；统计仍按“资源 × 用户”展示，团队视图只是成员用户用量的筛选汇总。
- 授权消耗统计不包含授权方自己的自用消耗；账号授权实例的 `account_authorization` 额度与授权账户列表用量按被授权实例所属系统账户聚合，授权方查看团队 / 用户消耗明细时读取授权报表窗口。
- 分组授权是动态使用权，分组所有者后续新增、移除或停用可共享账户，会直接影响被授权用户通过该分组可调度的账户集合。
- 授权分组只共享分组所有者自有账户；如果分组里包含别人授权来的账户，不能通过分组授权继续共享给第三方。
- 资源所有者只能看授权聚合用量，不能看被授权用户的请求快照、响应快照、客户端 IP、API Key 明文或业务请求内容。
## 统计口径

- 网关会记录调用方系统账户、账户所有者、分组所有者、统一授权 ID、授权对象类型、命中账户、API Key、分组、模型、状态、IP、首 token、总耗时、错误和 token。
- 授权账户调用时，请求日志归实际调用方；命中账户 ID 是被授权人的授权实例账户 ID，实例账户用量和账号授权额度按被授权实例所属系统账户聚合；授权方需要查看对外授权消耗时读取授权团队 / 用户报表窗口。
- 授权分组调用时，请求日志归实际调用方；分组真实用量按同一个 `group_id` 统一累计；授权消耗明细按 `group_authorization_id` 或被授权用户聚合。
- OpenAI JSON 响应读取 `usage.input_tokens`、`usage.output_tokens` 和 `usage.input_tokens_details.cached_tokens`。
- OpenAI SSE 响应读取 `response.completed` / `response.done` / `response.failed` 事件里的 `response.usage`。
- 成本按 OpenAI 官方 API 价格表做轻量估算；没有覆盖的模型先只记 token。
- OpenAI OAuth `5h` / `7d` 额度进度来自上游 Codex 限制快照，只用于判断账号剩余额度和恢复时间，不计入 `usage_records.cost_usd`，也不替代本地 token 用量统计。
