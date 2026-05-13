# OpenAI OAuth 透传细节统计与比较

> 创建时间：2026-05-08
> 复查时间：2026-05-08
> 关联文档：[中转透传机制调研与定位修正](中转透传机制调研与定位修正.md)、[OpenAI 账号接入](OpenAI账号接入.md)、[OpenAI API Key 透传细节统计与比较](OpenAI%20API%20Key透传细节统计与比较.md)、[原始审计日志设计](原始审计日志设计.md)

本文用于复查 `juhe-ai` 的 OpenAI OAuth / Codex 透传链路，并对比 `F:\temp-project\中转` 下几个主流中转项目的实现取舍。结论只用于协议兼容、账号隔离、异常请求降噪和可观测性，不用于绕过平台限制、规避风控或伪装身份。

## 一句话结论

OpenAI OAuth 账号不能当成“API Key 的另一种凭据”来裸透传。它打的是 `https://chatgpt.com/backend-api/codex` 这类 ChatGPT / Codex backend，而公开 OpenAI API 的官方 base URL 是 `https://api.openai.com/v1`。因此 OAuth 链路应按内部 `openai_oauth_codex` adapter 处理：限制 endpoint、白名单 Header、归一化 body、隔离上游 session/cache 标识，并尽量减少后台主动请求。

本次复查后，`juhe-ai` 已经吸收了 `new-api`、`sub2api_source`、`CLIProxyAPI` 中最关键的做法：OAuth 专用 adapter、Header allowlist、`store=false`、非 compact `stream=true`、compact 清理字段、系统角色转 developer、web search tool 名归一化、上游 session/cache 隔离，以及本地 400 校验。进一步复查后，OAuth 额度快照已经改为只从真实网关请求响应头被动更新；后台主动 usage refresh、建号后主动 usage refresh 已删除，账号质量主动探测也已直接删掉，不保留 dormant 开关；冷却复测只保留恢复性路径。Codex 客户端形态识别本轮明确不做，避免误伤用户体验。

## 官方边界

OpenAI 官方 OpenAPI spec 当前公开 base URL 是 `https://api.openai.com/v1`，公开 endpoint 包含 `/responses` 和 `/responses/compact`。官方 Conversation state 文档说明 Response 对象默认会保存一段时间，可通过 `store=false` 关闭；`previous_response_id` 在 HTTP 和 WebSocket mode 里都有上下文续链语义，WebSocket 还存在连接本地缓存语义。

因此：

- API Key 账号应按公开 OpenAI-compatible API 处理，尽量保留客户端公开 API 语义。
- OAuth / Codex 账号应按 ChatGPT / Codex backend 适配处理，不承诺公开 Responses API 全字段原样上游。
- `store=false` 是公开 API 也支持的隐私/保留期控制，在 OAuth / Codex adapter 里强制使用是合理的。
- `previous_response_id` 不能简单 hash 或盲目改写；没有上下文缓存设计前，应谨慎保留并继续观察。

参考：

- [OpenAI API endpoint list](https://api.openai.com/v1)
- [Conversation state: previous_response_id in WebSocket mode](https://developers.openai.com/api/docs/guides/conversation-state#previous_response_id-in-websocket-mode)

## 本项目当前实现

| 范围 | 关键文件 | 当前状态 |
| --- | --- | --- |
| OAuth 上游 URL | `backend/src/modules/gateway/openai-gateway-route-helpers.ts` | OAuth 账号只支持 `POST /responses` 与 `POST /responses/compact`，上游为 `https://chatgpt.com/backend-api/codex` |
| OAuth adapter | `backend/src/modules/gateway/openai-oauth-codex-adapter.ts` | 已拆出专用 adapter，不再复用 API Key raw passthrough 策略 |
| OAuth 请求体 | `backend/src/modules/gateway/openai-oauth-codex-adapter.ts` | 解析 JSON 对象，校验 `model`，非 compact 校验 `input`，补 `instructions` 空字符串，归一化 input/tools，删除高风险或不兼容字段 |
| OAuth Header | `backend/src/modules/gateway/openai-oauth-codex-adapter.ts` | 使用 allowlist + 默认值，强制 `content-type: application/json`，按 stream/compact 设置 `accept`，重写认证与 `chatgpt-account-id` |
| OAuth 会话隔离 | `backend/src/modules/gateway/openai-oauth-codex-adapter.ts` | 对 `session_id`、`conversation_id`、`prompt_cache_key` 混入系统账户、本地 API Key、分组、上游账号后生成隔离值 |
| API Key 链路 | `backend/src/modules/gateway/openai-gateway-upstream.ts` | 保留 raw body 真透传；Header 过滤危险头、代理链路、SDK/tracing 噪声和组织/项目头 |
| 回归脚本 | `backend/src/scripts/regression/openai-oauth-codex-adapter-regression.ts` | 覆盖 body normalize、Header allowlist、session isolation、compact、非法 body、缺 `model`/`input` |

## 参考项目统计

| 项目 | OAuth / Codex 做法 | 可吸收经验 | 当前 juhe-ai 对齐情况 |
| --- | --- | --- | --- |
| `new-api` | 独立 `codex` channel，只支持 `/v1/responses` 和 `/v1/responses/compact`；OAuth key 是 JSON，提取 `access_token` 和 `account_id`；强制 `Content-Type: application/json` | OAuth 必须和 API Key 分 channel；Codex backend 对 content-type 严格 | 已对齐：专用 adapter、endpoint 限制、content-type 强制、auth/chatgpt-account-id 重写 |
| `CLIProxyAPI` | Codex Responses 转换器设置 `stream=true`、`store=false`、`parallel_tool_calls=true`、`include=["reasoning.encrypted_content"]`，删除 token/采样字段，system role 转 developer | body 不是裸透传，要按 Codex backend 收敛 | 已部分对齐：已设置 `stream/store`、删除高风险字段、转换 system role、归一化 web search；暂未默认加入 `include` 和 `parallel_tool_calls` |
| `sub2api_source` | OAuth passthrough 有 body normalize、session isolation、compact 处理、Codex-only 检测和后台请求控制思路 | 多租户中转必须隔离上游 session/cache；后台主动流量也要看 | 已对齐 session/body/compact；Codex-only 检测和主动请求预算仍待后续计划 |
| `one-api` | 传统 OpenAI-compatible relay，无 Codex OAuth 专用链路；默认 Header 很克制 | 常规 OpenAI relay 不能直接照搬到 OAuth Codex | 只作为 API Key Header 边界参考 |
| `LiteLLM` / `Portkey` / `Helicone` / `Envoy` | 企业网关更重，强调 provider options、观测、治理和显式 header 策略 | 不做全量 Header 裸透传；Header 策略应可审计 | 已吸收 Header 边界，暂不引入重型企业治理 |

## 请求头比较

| 项目 | Header 策略 | 关键差异 |
| --- | --- | --- |
| `juhe-ai` 当前 OAuth | allowlist 复制少数低风险客户端头：`accept-language`、`x-client-request-id`、`x-codex-*`；Codex 头缺省补齐；认证、content-type、accept 强制覆盖 | 比旧黑名单复制稳，不把 cookie、代理链路、SDK/tracing、组织/项目等噪声带给 Codex backend |
| `new-api` Codex | 通用 header setup 后叠加 Codex channel 规则，强制 `Content-Type: application/json`，写 `OpenAI-Beta` 和 `originator` | 更像 channel adapter，配置能力更重 |
| `CLIProxyAPI` Codex | 只取少数 Codex 客户端头，写必要 Codex headers | 最像专用客户端适配器 |
| `sub2api_source` | 透传白名单，过滤超时类和敏感头 | 更重视多租户边界和后台流量治理 |

当前 `juhe-ai` OAuth allowlist：

- 可复制：`accept-language`、`x-client-request-id`、`x-codex-beta-features`、`x-codex-turn-state`、`x-codex-turn-metadata`
- 有条件保留：Codex-like `originator`、Codex-like `user-agent`、Codex-like `version`、包含 `responses` 的 `openai-beta`
- 强制覆盖：`authorization`、`content-type`、`accept`、`chatgpt-account-id`
- 不透传：本地 API Key、cookie、代理链路、`x-forwarded-*`、tracing、SDK 噪声、OpenAI 组织/项目、任意浏览器或部署环境噪声

## 请求体比较

| 项目 | `/responses` | `/responses/compact` | 字段策略 |
| --- | --- | --- | --- |
| `juhe-ai` 当前 OAuth | JSON 对象校验；`model` 非空；`input` 必须是字符串或数组；string input 转 message array；`store=false`；`stream=true` | `model` 非空；删除 `store`、`stream`、tools、reasoning 等 compact 不需要字段 | 删除 `metadata`、`user`、`temperature`、`top_p`、token limit、`stream_options`、`context_management` 等 |
| `new-api` Codex | 补 `instructions`，非 compact 强制 `store=false`，删除 `max_output_tokens`、`temperature` | compact 单独 request DTO | 更保守，主要删除已知拒绝字段 |
| `CLIProxyAPI` | 设置 `stream=true`、`store=false`、`parallel_tool_calls=true`、`include`，删除采样/token/context 字段 | 有 compact compatibility | 对 Codex 执行器更激进 |
| `sub2api_source` | normalize OAuth passthrough，缺失特定 Codex instructions 时可本地拒绝 | compact 删除 `store/stream` | 更偏账号保护和 Codex-only 策略 |

`juhe-ai` 当前非 compact 删除字段：

- `background`
- `conversation`
- `context_management`
- `frequency_penalty`
- `max_completion_tokens`
- `max_output_tokens`
- `metadata`
- `presence_penalty`
- `prompt_cache_retention`
- `safety_identifier`
- `stream_options`
- `temperature`
- `top_p`
- `truncation`
- `user`

compact 会额外删除：

- `include`
- `parallel_tool_calls`
- `prompt_cache_key`
- `reasoning`
- `store`
- `stream`
- `text`
- `tool_choice`
- `tools`
- `top_logprobs`

## 会话与缓存隔离

| 来源 | 旧问题 | 当前处理 |
| --- | --- | --- |
| header `session_id` | 不同本地 API Key 可能把相同 session 送到同一 OAuth 账号上游 | hash `systemAccountId + apiKeyId + groupId + accountId + raw` |
| header `conversation_id` | 多租户 conversation 标识可能碰撞 | 同样 hash 隔离后再上游 |
| body `prompt_cache_key` | 客户端默认值或固定值可能跨用户共用 | 非 compact 写入隔离后的 `prompt_cache_key` |
| body `metadata.session_id` | metadata 不适合上游，但可作为原始 session 来源 | 用于生成隔离 session 后删除 metadata |

这一步是 OAuth 账号池最重要的“透传修正”：客户端会话语义可以保留，但上游看到的是按本地授权边界隔离后的标识。

## 他们的好还是我们的好

| 维度 | 他们更好 | 我们更好 | 结论 |
| --- | --- | --- | --- |
| OAuth 分层 | `new-api`、`CLIProxyAPI`、`sub2api_source` 很早就把 Codex 当专门 channel | 当前已补齐专用 adapter | OAuth 后续继续按 adapter 打磨，不回到通用 passthrough |
| API Key body | 多数中转会解析再重组 | 我们 API Key 链路保留 raw body 字节级透传 | API Key 这点我们更好，应保留 |
| OAuth Header | 成熟项目普遍更克制 | 当前 allowlist 已接近 | 不追求“所有头都透”，追求协议形态稳定 |
| 会话隔离 | `sub2api_source` 思路更完整 | 当前已 hash 隔离上游 session/cache | 继续加强审计和统计，而不是暴露开关给用户 |
| 后台主动请求 | `sub2api_source` 有更多预算/模式设计 | 当前默认更克制 | OAuth usage refresh 已移除；质量探测代码已删除；冷却复测仅保留冷却到期后的恢复性确认 |
| 用户配置复杂度 | 企业网关配置多 | 我们保持轻量，没有给 API Key 表单加组织/项目/Beta | 用户不知道的字段不放表单，复杂逻辑在后端策略内收口 |

## 当前剩余风险排序

| 优先级 | 剩余风险 | 当前表现 | 建议 |
| --- | --- | --- | --- |
| P1 | 主动请求恢复边界 | OAuth usage refresh 已移除；质量探测代码已删除；冷却复测仍会产生恢复性请求 | 继续把主动请求收敛到单一恢复用途：只保留冷却到期后的复测；如未来想恢复其它主动请求，必须重新立项，不复用旧实现 |
| P1 | `previous_response_id` 续链语义 | 当前不改写，避免破坏语义，但没有连接态缓存设计 | 后续研究 WebSocket/HTTP 上下文缓存；无法解析时要求客户端传完整上下文 |
| P2 | 非流式客户端体验 | OAuth 非 compact 强制上游 `stream=true`，非流式客户端可能收到 SSE | 如需兼容非流式客户端，增加 SSE 聚合成 JSON 的本地转换 |
| P2 | Codex 默认版本漂移 | 默认 `codex_cli_rs/0.125.0` 会随真实客户端变化 | 默认值集中配置并定期复查；优先保留真实 Codex 客户端低风险头 |
| P2 | 审计统计还不够结构化 | 能看上游请求，但缺少请求形态摘要字段 | 增加 `adapter`、`request_shape`、`normalized_fields`、`session_isolated`、`background_source` |

## 不建议的方向

- 不建议重新把 OAuth 请求体改成 raw body 裸透传；那会退回“公开 API 字段 + Codex backend 头”的混合形态。
- 不建议在 API Key 账号表单增加 OpenAI 组织、项目、Beta 字段；组织/项目不能由系统生产，用户不知道时也不该被迫填写。
- 不建议把 `X-Forwarded-For`、`X-Real-IP`、`Via` 等代理链路头透给上游；这不会改变 TCP 出口，只会暴露中转链路。
- 不建议盲目复制完整真实客户端 UA、系统信息或版本细节；兼容默认头和伪装身份是两回事。
- 不建议为了拿额度主动请求 OAuth 账号；本项目已移除后台和建号后的 OAuth usage refresh，额度快照只从真实业务响应头被动更新。

## 本次已落地事项

- OAuth 账号已走 `openai_oauth_codex` 专用 adapter。
- OAuth Header 从黑名单复制改为 allowlist + 默认值。
- OAuth body 已做 JSON 对象校验、`model`/`input` 校验、`instructions` 类型校验和字段收敛。
- 非 compact 固定 `store=false`、`stream=true`；compact 删除 `store/stream` 等不需要字段。
- 上游 `session_id`、`conversation_id`、`prompt_cache_key` 已做多租户隔离。
- API Key 账号仍保留 raw body 真透传，并且不暴露 OpenAI 组织/项目/Beta 表单字段。
- OAuth 额度快照已改为纯被动更新：真实网关响应头写入 `account_usage_snapshots`，后台和建号流程不再主动发模型请求拿额度。
- Codex 请求形态识别本轮不做；OAuth adapter 继续以兼容体验为先，只保留协议必要校验。

## 验证建议

| 测试类型 | 用例 | 预期 |
| --- | --- | --- |
| 回归脚本 | OAuth `/v1/responses` 带 `metadata`、`temperature`、`store=true`、`stream=false` | 上游 body 删除不支持字段，`store=false`、`stream=true` |
| 回归脚本 | OAuth 缺 `model`、缺 `input`、`input` 类型错误 | 本地 400，不请求上游 |
| 回归脚本 | OAuth compact 带 `store`、`stream`、tools | 上游 compact body 删除这些字段 |
| 回归脚本 | 两个本地 API Key 使用同一 `prompt_cache_key` | 上游 session/cache 标识不同 |
| 回归脚本 | API Key 账号请求 | 保持 raw body 和公开 API Header 语义，不走 OAuth adapter |
| 回归脚本 | OAuth 后台 usage refresh | 后台任务和 OAuth 建号接口不再引用主动 usage refresh 服务 |
| 回归脚本 | OAuth 被动额度快照 | 真实网关响应和上游错误响应仍调用 `persistOpenAICodexHeadersIfNeeded` 写快照 |
| 后续审计 | 请求形态统计 | 能按 `openai_oauth_codex` / `openai_api_key_platform` 聚合查看过滤和归一化行为 |

## 结论

当前最重要的透传原则已经明确：API Key 追求公开 OpenAI API 的 raw body 真透传；OAuth 追求 Codex backend 的协议适配和多租户隔离。两条线不能混用。

从主流中转吸收下来的“好经验”不是某个神秘 Header，而是四个稳定动作：专用 adapter、Header allowlist、body normalize、session isolation。`juhe-ai` 现在已经把这四项落到 OAuth 链路里，并进一步把 OAuth 额度快照改成纯被动来源。下一轮真正值得继续磨的是审计统计和 `previous_response_id` 续链语义，让“账户异常”不再靠感觉猜，而是能按请求来源、错误码和后台任务来源拆开看。
