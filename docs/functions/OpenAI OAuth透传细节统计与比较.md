# OpenAI OAuth 透传细节统计与比较

> 创建时间：2026-05-08
> 关联文档：[中转透传机制调研与定位修正](中转透传机制调研与定位修正.md)、[OpenAI 一期计划](第一期OpenAI账号接入.md)、[原始审计日志设计](原始审计日志设计.md)

本文用于二次复查 `juhe-ai` 的 OpenAI OAuth / Codex 透传链路，并对比 `F:\temp-project` 下几个中转项目与主流网关项目的实现取舍。结论只用于协议兼容、账号安全边界、可观测性和降低异常请求噪声，不用于规避平台限制、绕过风控或伪装身份。

## 一句话结论

当前 `juhe-ai` 的 OAuth 透传更接近“官方 Responses 请求体原样透传 + 补 Codex backend 头”，而成熟的 Codex / OAuth 中转更接近“把 OAuth 账号单独当作 Codex backend 适配器处理”：限制可走的接口，清洗请求头，归一化请求体，隔离 `session_id` / `prompt_cache_key`，并控制后台探活流量。

这不意味着封号一定由透传实现导致；没有上游侧审计日志时不能做因果断言。但从代码形态看，当前实现确实存在几类会让上游看到“不像 Codex 客户端、也不像公开 OpenAI API”的混合请求形态，需要优先打磨。

## 本次复查范围

### 本项目

| 范围 | 关键文件 | 关注点 |
| --- | --- | --- |
| OAuth 上游 URL | `backend/src/modules/gateway/openai-gateway-route-helpers.ts` | OAuth 账号转到 `https://chatgpt.com/backend-api/codex`，只支持 `POST /responses` 与 `POST /responses/compact` |
| 请求头构造 | `backend/src/modules/gateway/openai-gateway-upstream.ts` | 入站头跳过危险头后复制，替换 `authorization`，OAuth 时补 Codex 默认头 |
| 请求体构造 | `backend/src/modules/gateway/openai-gateway-upstream.ts` | 透传开启时优先使用 `req.rawBody`，OAuth 无专门 body normalize |
| 会话亲和 | `backend/src/modules/gateway/openai-gateway-session-affinity.service.ts` | 用 `session_id` / `prompt_cache_key` 等做本地账号排序，不改写发往上游的会话标识 |
| 账号测试与后台探活 | `backend/src/modules/accounts/account-test.service.ts`、`backend/src/modules/background/background-jobs.ts`、`backend/src/modules/openai-oauth/openai-oauth-usage-refresh.service.ts` | 测试、额度刷新、质量探测都会产生真实上游请求 |

### 本地参考项目

| 项目 | 本地源码时间或来源 | 与本次主题的关系 |
| --- | --- | --- |
| `CLIProxyAPI` | `da6c599e`，2026-05-05 | Codex executor 明确适配 ChatGPT / Codex backend |
| `sub2api_source` | `47fb38bc`，2026-05-03 | 最接近本项目定位，已有 OAuth passthrough、`codex_cli_only`、会话隔离、body normalize |
| `new-api` | `dac55f0`，2026-04-30 | 已有独立 `codex` channel，路径、header、body 都有专门规则 |
| `one-api` | `8df4a26`，2025-02-21 | 传统 OpenAI-compatible relay，无 Codex / OAuth 专用链路 |
| `openai-codex-main` | 本地源码目录，无 git 元信息 | Codex 客户端自身的请求形态参考 |
| `litellm` | `c011a7e`，2026-05-03 | 主流企业网关参考，不是 Codex OAuth 中转 |
| `portkey-gateway` | `351692fd`，2026-03-25 | 主流企业网关参考，强调 header override / observability |
| `helicone-ai-gateway` | `9649b27`，2025-11-20 | 主流观测型网关参考 |
| `envoy-ai-gateway` | `d63a020f`，2026-04-30 | 基础设施型 AI Gateway 参考 |
| `agentgateway` | `7735866`，2026-05-01 | AI / Agent Gateway 参考 |

### 官方边界

OpenAI 官方文档只把 `https://api.openai.com/v1/responses` 描述为公开 Responses API，并使用 `Authorization: Bearer $OPENAI_API_KEY`。官方 Responses 文档也说明 `store: false`、`previous_response_id` 属于公开 API 语义。`https://chatgpt.com/backend-api/codex` 不在公开 OpenAI API 文档中，因此 OAuth / Codex backend 不能简单按公开 Responses API 的完整请求体照搬。

参考：

- [OpenAI Prompting 文档中的 Responses API 调用示例](https://developers.openai.com/api/docs/guides/prompting#create-a-prompt)
- [Migrate to the Responses API: Additional differences](https://developers.openai.com/api/docs/guides/migrate-to-responses#additional-differences)
- [Responses WebSocket Mode](https://developers.openai.com/api/docs/guides/websocket-mode)

## 统计摘要

| 维度 | 统计结论 |
| --- | --- |
| 直接处理 ChatGPT / Codex backend 的参考实现 | `juhe-ai`、`sub2api_source`、`CLIProxyAPI`、`new-api`、`openai-codex-main`，共 5 类 |
| 传统 OpenAI-compatible relay | `one-api`、`LiteLLM`、`Portkey`、`Helicone`、`Envoy AI Gateway`、`agentgateway`，共 6 类 |
| 成熟项目是否无条件全量复制所有请求头 | 未看到适合作为账号池网关主线的全量裸透传；即使 `one-api` 的 proxy adaptor 也会删除 `Host`、`Content-Length`、`Accept-Encoding`、`Connection` 并重写认证 |
| OAuth / Codex 是否做 body normalize | `sub2api_source`、`CLIProxyAPI`、`new-api` 都做；`juhe-ai` 当前基本不做 |
| 是否把 OAuth / Codex 当独立 channel / adapter | `sub2api_source`、`CLIProxyAPI`、`new-api` 都是专门链路；`juhe-ai` 当前仍挂在通用 OpenAI 账户透传函数里 |
| 是否处理上游会话标识 | `sub2api_source` 会隔离 `session_id` / `conversation_id`；`CLIProxyAPI` 会生成或写入 `prompt_cache_key` / `Session_id`；Codex 客户端会用会话 ID；`juhe-ai` 当前只用于本地亲和排序，不改写上游值 |
| 是否允许任意 OpenAI SDK 形态直接打 OAuth 账号 | 成熟 Codex 链路通常限制 endpoint 或客户端形态；`juhe-ai` 当前容易让普通 OpenAI Responses 请求以 OAuth 账号打到 Codex backend |

## 上游路径比较

| 项目 | API Key 账号 | OAuth / Codex 账号 | 路径限制 |
| --- | --- | --- | --- |
| `juhe-ai` | 使用账号 `baseUrl`，默认 OpenAI 兼容 | `https://chatgpt.com/backend-api/codex` | OAuth 只支持 `POST /responses`、`POST /responses/compact` |
| `sub2api_source` | 默认 `https://api.openai.com/v1/responses` 或账号自定义 base URL | `https://chatgpt.com/backend-api/codex/responses` | 针对 `/responses`、compact、WS 等分支分别处理 |
| `CLIProxyAPI` | 可用 API Key / base URL | 默认 `https://chatgpt.com/backend-api/codex/responses` | Codex executor 专用 |
| `new-api` | 常规 OpenAI channel | 独立 `codex` channel，`/backend-api/codex/responses` | 只支持 `/v1/responses` 和 `/v1/responses/compact`，拒绝 chat、embedding、image 等 |
| `one-api` | 常规 OpenAI-compatible relay | 无 Codex OAuth 专用链路 | 传统 channel/adaptor |
| Codex 客户端 | 公开 API 或配置 provider | ChatGPT 登录态相关内部调用 | Codex 自身按 Responses / WebSocket / compact 生成请求 |

关键判断：OAuth 账号不是“OpenAI API Key 的另一种凭据”。它实际打到 ChatGPT / Codex backend，应当单独建 adapter 语义，而不是复用公开 API Key 的完整请求体策略。

## 请求头策略比较

| 项目 | 请求头来源 | 认证处理 | Codex 关键头 | 危险头处理 | 备注 |
| --- | --- | --- | --- | --- | --- |
| `juhe-ai` | 入站头跳过黑名单后复制 | 删除本地 `authorization`，写上游 token | 缺省补 `accept`、`content-type`、`user-agent`、`originator`、`version`、`openai-beta`、`chatgpt-account-id` | 黑名单跳过 `host`、`authorization`、`content-length`、`connection`、`accept-encoding`、`cookie`、`x-forwarded-*` 等 | 策略偏“黑名单复制”；如果客户端传了 `content-type: application/json; charset=utf-8`，OAuth 链路不会强制改成精确 `application/json` |
| `sub2api_source` | 透传白名单 | 删除本地认证，写上游 token | 补 `OpenAI-Beta`、`originator`、`chatgpt-account-id`、`accept`，必要时设置 `version` | 只允许低风险头，超时类头默认可过滤 | 白名单更稳，避免非标准环境噪声头进入上游 |
| `CLIProxyAPI` | 只取少数 Codex 客户端头 | 写 `Authorization` | 写 `Content-Type: application/json`、`Accept`、`Originator`、`Chatgpt-Account-Id`，保留 `X-Codex-*`、`Version`、`X-Client-Request-Id` | 不走全量复制 | 更像 Codex client adapter |
| `new-api` | 通用 setup 后叠加 channel 规则和 header override | OAuth key 必须是 JSON，提取 `access_token` 和 `account_id` | 写 `Authorization`、`chatgpt-account-id`、`OpenAI-Beta`、`originator` | header override 支持透传规则，但跳过危险头 | 明确注释 Codex backend 对 `Content-Type` 严格，强制 `application/json` |
| `one-api` | 通用 adaptor 只转 `Content-Type` / `Accept` 等；proxy adaptor 复制较多头 | 按 channel 重写 | 无 Codex 专用头 | proxy adaptor 删除 `Host`、`Content-Length`、`Accept-Encoding`、`Connection` | 传统 relay 参考，不适合作为 Codex OAuth 直接模板 |
| Codex 客户端 | 默认 client header | 按 provider auth 注入 | 默认 `originator`，`User-Agent`，请求会带 `session_id`、`prompt_cache_key` 等 | 客户端自身生成，不处理多租户中转问题 | `originator` 默认是 `codex_cli_rs`，一等客户端还包括 `codex_vscode`、`codex_atlas`、`codex_chatgpt_desktop` 等 |

当前主要差距不是“少传某个神秘头”，而是策略层级不对：OAuth / Codex 应该使用稳定的 allowlist + adapter 默认头，而不是把普通 OpenAI SDK 的所有语义头尽量搬过去。

## 请求体策略比较

| 项目 | `/responses` body | `/responses/compact` body | 不支持字段处理 | `instructions` | `store` / `stream` |
| --- | --- | --- | --- | --- | --- |
| `juhe-ai` | 透传时优先原始 `rawBody` | 同样原始 body | OAuth 当前无专门删除逻辑 | 不保证存在；账号测试请求会带默认 instructions | 账号测试 OAuth 会设 `store=false`、`stream=true`；真实客户端请求不保证 |
| `sub2api_source` | OAuth passthrough 会 normalize | compact 会删除 `store` / `stream` | passthrough normalize 删除 `user`、`metadata`、`prompt_cache_retention`、`safety_identifier`、`stream_options`；其他 Codex transform 还会处理 `max_output_tokens`、`temperature` 等 | Codex passthrough 对缺失或空 `instructions` 有本地拒绝逻辑 | 非 compact 设 `store=false`、`stream=true` |
| `CLIProxyAPI` | 先翻译到 Codex 格式，再写模型和 stream | 有 compact 专用分支 | 删除 `previous_response_id`、`prompt_cache_retention`、`safety_identifier`、`stream_options` | 缺失时补空字符串 | 设 `stream=true` |
| `new-api` | 独立 Codex channel 转换 Responses request | compact 直接走 compact 规则 | 非 compact 删除 `max_output_tokens`、`temperature` | 缺失时补空字符串 | 非 compact 设 `store=false`；stream 按请求与 relay info |
| Codex 客户端 | 构造 `ResponsesApiRequest`，包含 `model`、`instructions`、`input`、`tools`、`store`、`stream`、`prompt_cache_key` | compact 构造 `ApiCompactionInput`，只带 compact 需要的字段 | 客户端按自身 schema 生成 | 来自 base instructions，空字符串可跳过序列化 | 对普通 OpenAI endpoint 通常 `store=false`、`stream=true`；Azure 例外 |

结论：公开 Responses API 的合法字段，不一定适合 ChatGPT / Codex internal backend。当前 `juhe-ai` 最大风险就是没有 OAuth 专用请求体收敛，可能把普通 SDK 的 `temperature`、`metadata`、`user`、`previous_response_id`、`store=true`、`stream=false`、缺失 `instructions` 等混合形态直接送到 Codex backend。

## 会话与缓存标识比较

| 项目 | 本地亲和 | 上游 `session_id` / `conversation_id` | Body `prompt_cache_key` | 多用户隔离 |
| --- | --- | --- | --- | --- |
| `juhe-ai` | 有，使用 `session_id`、`conversation_id`、`prompt_cache_key`、`previous_response_id` 等生成本地账号排序 key | 当前不改写，客户端传什么上游基本看到什么 | 不改写 | 本地排序 key 混入 `apiKeyId` 和 `groupId`，但上游标识未隔离 |
| `sub2api_source` | 有 | 对 OAuth 账号把 `apiKeyID` 混入原始 session 后重写 `session_id` / `conversation_id` | 可作为 fallback | 有上游隔离 |
| `CLIProxyAPI` | 有 cache helper | 写 `Session_id` | 对 OpenAI responses 使用 body `prompt_cache_key`，对 OpenAI 格式按本地 API key 生成稳定 UUID | 通过本地 API key 派生 cache ID |
| `new-api` | 通过 header override / mapping 规则支持 | 可透传或同步 `Session_id` | 支持从 header 同步到 body | 依赖配置 |
| Codex 客户端 | 单客户端会话天然隔离 | `session_id` 等于当前会话 ID | `prompt_cache_key` 等于当前会话 ID | 不处理中转多租户 |

`juhe-ai` 的本地亲和只能保证“同一会话优先用同一账号”，不能保证“不同本地 API Key 到上游时不会共享相同 session 标识”。如果多个用户或客户端默认使用固定值、空值回填、同名项目 ID、同一个 `prompt_cache_key`，上游会看到跨用户复用的会话/cache 标识，这属于高优先级风险。

## 后台请求与探活比较

| 来源 | 当前行为 | 风险点 | 建议 |
| --- | --- | --- | --- |
| 手动账号测试 | 通过真实 `/v1/responses` 测试账号 | 管理面操作会产生上游请求，但频率可控 | 保留，展示清楚请求模型和结果 |
| OAuth 额度快照刷新 | 后台定时调用 `testOpenAIAccount()`，默认 `gpt-5.5` + `hi` | 为了拿额度响应头主动制造请求；账号多时会形成周期性低价值流量 | 优先使用真实业务响应头被动更新；主动刷新加总开关、抖动、退避、并发和每日预算 |
| 账号质量探测 | 对候选账号重复请求，默认 repeats=3 | 质量探测可能比真实业务更频繁，且请求内容高度一致 | 用真实流量统计优先；主动探测只用于冷启动或 tie-break，且降低 repeats |
| 冷却账号复测 | 冷却到期后真实请求复测 | 冷却账号反复失败会形成噪声 | 对 401/403/封禁类错误进入更长人工复核冷却，不自动高频复测 |

当前“账户都被封了”的排查不能只看透传 header/body，后台主动请求也要纳入审计。尤其是 OAuth 额度刷新和质量探测都复用账号测试请求，虽然请求体本身比真实用户请求更干净，但周期性、低语义、重复模型和重复 prompt 也可能增加异常流量噪声。

## 当前实现风险排序

| 优先级 | 风险 | 当前表现 | 推荐处理 |
| --- | --- | --- | --- |
| P0 | OAuth / Codex body 未归一化 | 透传时直接用 `rawBody`，普通 OpenAI Responses SDK 的完整字段可能进入 ChatGPT / Codex backend | 新增 OAuth Codex body normalizer |
| P0 | 上游会话标识未隔离 | 本地 affinity key 隔离了排序，但上游 `session_id`、`conversation_id`、`prompt_cache_key` 仍可能跨用户碰撞 | 对 OAuth 上游重写 session/cache 标识，混入本地 API Key / 分组 / 系统账户 |
| P1 | OAuth 账号与 API Key 账号 adapter 语义混在一起 | OAuth 虽然换了 URL 和 header，但 body 与通用 passthrough 共用 | 拆出 `openai-oauth-codex` adapter 层 |
| P1 | 非 Codex 客户端可直接驱动 OAuth 账号 | 普通 SDK 请求可能被路由到 OAuth Codex backend | 对 OAuth 账号启用 Codex client shape 检测或本地转换，不符合时本地拒绝或改走 API Key 账号 |
| P1 | 后台主动请求噪声 | OAuth usage refresh、质量探测、冷却复测都会真实打上游 | 增加主动请求预算、抖动、退避和可观测报表，优先被动更新 |
| P2 | `Content-Type` 精确性 | 客户端带 `application/json; charset=utf-8` 时不会强制改成 `application/json` | OAuth Codex 链路强制精确 `application/json` |
| P2 | Codex 头版本漂移 | 当前默认 `codex_cli_rs/0.125.0`，真实 Codex UA 更丰富，版本会变化 | 不做盲目伪装；允许真实 Codex 客户端头透传，默认值集中配置并可升级 |
| P2 | 错误形态缺少本地拦截 | 缺少 `instructions`、`store=true` 等错误直接打上游 | 本地快速拒绝或修正协议必需字段，并记录审计 |

## 推荐打磨方案

### 1. 拆出 OAuth Codex adapter

把当前 OpenAI 账号链路拆成两个内部策略：

| 策略 | 适用账号 | 上游 | 请求语义 |
| --- | --- | --- | --- |
| `openai_api_key_platform` | OpenAI API Key | `api.openai.com/v1` 或账号 base URL | 公开 OpenAI API 兼容 |
| `openai_oauth_codex` | OpenAI OAuth | `chatgpt.com/backend-api/codex` | Codex backend 兼容，不承诺公开 API 全字段 |

用户侧仍然只看到 OpenAI 供应商和本地 `/v1`，不新增透传开关。

### 2. OAuth Codex 请求头使用 allowlist

推荐 allowlist：

- `accept`
- `accept-language`
- `content-type`
- `openai-beta`
- `originator`
- `user-agent`
- `version`
- `session_id`
- `conversation_id`
- `x-codex-beta-features`
- `x-codex-turn-state`
- `x-codex-turn-metadata`
- `x-client-request-id`

强制覆盖：

- `authorization: Bearer <access_token>`
- `chatgpt-account-id`
- `content-type: application/json`
- compact 请求的 `accept: application/json`
- 非 compact 请求在缺省或不兼容时使用 `accept: text/event-stream`

继续禁止：

- `x-forwarded-*`
- `x-real-ip`
- `forwarded`
- `via`
- `cookie`
- `accept-encoding`
- 本地 API Key 类认证头
- hop-by-hop headers

### 3. OAuth Codex 请求体归一化

建议先做最小 normalize，不做业务内容注入：

| 场景 | 处理 |
| --- | --- |
| 非 compact `/responses` | 解析 JSON；确保 `store=false`；确保 `stream=true`；确保 `instructions` 字段存在且为字符串，缺失时补空字符串；移除 ChatGPT / Codex backend 不支持或高风险字段 |
| compact `/responses/compact` | 删除 `store`、`stream`；确保只保留 compact endpoint 需要的字段 |
| 非 JSON body | 本地返回 400，不向 OAuth Codex backend 发送 |
| 缺少 `model` 或 `input` | 本地返回 400 |
| 明确 `previous_response_id` | HTTP `/responses` 上先谨慎处理；如未接入 WebSocket 连接态缓存，优先本地拒绝或转换为完整上下文请求，不盲目透传 |
| `instructions` 处理 | 协议必需时只补空字符串；不要补默认系统提示，避免破坏透传语义 |

首批建议移除字段：

- `user`
- `metadata`
- `prompt_cache_retention`
- `safety_identifier`
- `stream_options`
- `max_output_tokens`
- `max_completion_tokens`
- `temperature`
- `top_p`
- `frequency_penalty`
- `presence_penalty`

字段集合要通过审计日志继续验证，避免一次性删除过多造成兼容性倒退。

### 4. 上游会话隔离

推荐生成稳定隔离标识：

```text
isolated_session = hash(system_account_id + api_key_id + group_id + raw_session_or_prompt_cache_key)
```

应用位置：

- OAuth Codex 上游 header `session_id`
- OAuth Codex 上游 header `conversation_id`
- 必要时同步 body `prompt_cache_key`

优先级：

1. 客户端 header `session_id`
2. 客户端 header `conversation_id`
3. body `prompt_cache_key`
4. body `metadata.session_id`
5. 本地 API Key + group 派生 fallback

注意：本地使用记录可以保存原始客户端会话标识的哈希用于排查，但不要把原始值直接暴露到上游或日志。

### 5. Codex 客户端形态检测

OAuth 账号可以增加内部保护策略，不作为普通用户透传选项暴露：

| 策略 | 行为 | 建议默认 |
| --- | --- | --- |
| `codex_only` | 只允许 Codex 客户端形态或已被本地转换成 Codex 形态的请求进入 OAuth 账号 | 建议开启 |
| `generic_responses_to_codex` | 普通 OpenAI Responses 请求可经过 normalizer 后进入 OAuth 账号 | 谨慎开启，需要审计 |
| `reject_generic_oauth` | 非 Codex 形态请求本地 400/403，并提示使用 API Key 账号或 Codex 客户端 | 适合账号保护优先 |

可识别的客户端形态参考：

- `originator` 以 `codex_` 或 `codex ` 开头
- `user-agent` 以 `codex_cli_rs/`、`codex_vscode/`、`codex_app/`、`codex_exec/`、`codex_sdk_ts/` 等开头
- 请求体存在 Codex 典型字段，如 `prompt_cache_key`、`tools`、`include`、`instructions`，且路径为 `/v1/responses`

不要把客户端识别做成“伪装开关”。它应该是路由保护和兼容性判断。

### 6. 后台探活降噪

推荐改造顺序：

1. OAuth 额度快照优先由真实业务响应头被动更新。
2. 主动刷新加系统总开关，默认低频。
3. 对同一账号加入每日主动请求上限。
4. 对 401/403/疑似封禁类错误进入人工复核冷却，不自动频繁复测。
5. 对 429 使用上游 reset header 退避，没有 reset 时指数退避。
6. 质量探测优先用真实流量统计，只在缺样本或同分组 tie-break 时主动探测。
7. 主动探测请求也走同一 OAuth Codex normalizer，避免后台请求和真实请求形态不一致。

### 7. 增加请求形态审计

原始审计日志已经能捕获 upstream request。建议再增加一个低敏统计视图：

| 字段 | 说明 |
| --- | --- |
| `adapter` | `openai_api_key_platform` / `openai_oauth_codex` |
| `request_shape` | `codex_native` / `responses_api_compat` / `rejected` |
| `normalized_fields` | 被设置、删除、重写的字段名列表 |
| `header_policy` | `allowlist` / `fallback_defaults` |
| `session_isolated` | 是否重写上游 session/cache 标识 |
| `background_source` | `none` / `usage_refresh` / `quality_probe` / `cooldown_retest` |

这样后续不用猜“透传不对劲”到底是哪一类不对劲，可以直接按请求形态统计。

## 不建议采用的方向

- 不建议把 `X-Forwarded-For`、`X-Real-IP` 等代理链路头透给上游；它不能改变 TCP 出口 IP，只会暴露代理链路。
- 不建议做用户可见的“透传模式大杂烩”选项；策略应该挂在供应商 adapter 内部。
- 不建议盲目复制真实 Codex UA 的完整操作系统、终端、版本细节；默认值要集中配置，优先保留真实客户端自己传来的低风险头。
- 不建议为了拿额度频繁主动请求 OAuth 账号；额度快照应尽量被动更新。
- 不建议把 `previous_response_id` 直接当普通 HTTP 透传字段处理，除非已明确解决 store=false、连接态缓存和重试恢复策略。

## 推荐实施优先级

### 第一批：止血与可观测

- 新增 OAuth Codex body normalizer。
- OAuth Codex header 改为 allowlist。
- 强制 `content-type: application/json`。
- 对 OAuth 上游 `session_id` / `conversation_id` / `prompt_cache_key` 做隔离。
- 审计记录 normalized fields、request shape、background source。
- 后台 OAuth usage refresh 加主动请求预算和退避。

### 第二批：兼容性打磨

- 增加 Codex 客户端形态检测。
- 对非 Codex 客户端请求决定“本地转换”或“本地拒绝”。
- 对 compact endpoint 单独写请求体与响应测试。
- 建立 sanitized upstream request 快照测试，不保存 token 和完整敏感正文。

### 第三批：更复杂能力

- 研究 Responses WebSocket mode 与 `previous_response_id` 的连接态缓存。
- 对不同客户端类型建立兼容 profile。
- 将 OAuth Codex 额度、限流、账号状态和后台刷新策略统一到账号质量模型。

## 验证建议

| 测试类型 | 用例 | 预期 |
| --- | --- | --- |
| 单元测试 | 普通 `/v1/responses` OAuth 请求带 `store=true`、`temperature`、`metadata` | 输出 body 中 `store=false`，删除不支持字段 |
| 单元测试 | compact 请求带 `store`、`stream` | 输出 body 删除这两个字段 |
| 单元测试 | 客户端传 `application/json; charset=utf-8` | 上游 header 为精确 `application/json` |
| 单元测试 | 两个 API Key 使用同一个 `prompt_cache_key` | 上游 `session_id` / `conversation_id` 不同 |
| 单元测试 | 非 JSON body 打 OAuth 账号 | 本地 400，不访问上游 |
| 集成测试 | Codex-like 请求走 OAuth 账号 | 上游请求形态接近 Codex 客户端，审计记录 `codex_native` |
| 集成测试 | 普通 OpenAI SDK 请求走 OAuth 账号 | 按策略本地转换或拒绝，不能 raw 打到 Codex backend |
| 后台任务测试 | OAuth usage refresh 连续失败或 429 | 进入退避，不重复高频请求 |
| 回归测试 | API Key 账号请求 | 仍走公开 OpenAI-compatible 策略，不受 OAuth normalizer 影响 |

## 结论

本次复查后，最值得立刻打磨的不是找更多“像真客户端”的表面头，而是把 OAuth / Codex 从通用 OpenAI passthrough 中拆出来，做到四件事：

1. 只让适合 Codex backend 的请求形态进入 OAuth 账号。
2. 请求体按 Codex backend 最小兼容面归一化。
3. 上游会话和缓存标识按本地 API Key / 分组隔离。
4. 后台主动请求降噪，并把请求形态审计出来。

这些属于协议正确性和多租户隔离，不是绕限制。做到这一步后，再看账号状态、出口代理、请求频率、模型选择、错误码分布，才能更接近真实原因。
