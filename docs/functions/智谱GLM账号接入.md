# 智谱 GLM 账号接入

> 2026-06-27 路由分层更新：本文旧段落里提到的 API Key 默认客户端画像和显式桥接配置只作为历史背景；当前目标是客户端画像由网关内部识别，API Key 只绑定策略路由，OpenAI v1 Responses 到 Chat Completions 可由普通 GLM 账号模型别名显式声明，其他跨协议转换落到混合供应商账户。
> 当前代码已移除 API Key / 策略路由层的显式跨协议桥接入口；本文后续如果仍出现“API Key 默认客户端画像”或“API Key 显式混合路由”，均表示待迁移历史设计，不得作为新增实现、测试断言或页面配置依据。除 OpenAI v1 普通账号 `responses -> chat_completions` 外，跨协议承接统一迁移到混合供应商账户。

## 范围

本文记录智谱 GLM 供应商的接入结论、账户创建类型、协议档案、网关透传边界、Codex bridge、模型目录和后续实现注意事项。当前代码已落地 `glm` 供应商、通用 GLM API Key、GLM Coding Plan OpenAI Chat 和 GLM Coding Plan Anthropic v1 三个协议档案；GLM Coding Plan Key 只保存真实 Chat Completions 上游能力，OpenAI v1 Responses 到 Chat Completions 可由普通 GLM 账号模型别名显式声明，而 Anthropic v1 档案面向 Anthropic / Claude Code 请求。运行事实以本文和当前实现为准。

官方资料显示，智谱当前面向本项目最相关的接入形态有三类：

- 通用 GLM API：支持 OpenAI API 兼容调用，OpenAI SDK 只需要替换 API Key 和 `base_url`，默认地址为 `https://open.bigmodel.cn/api/paas/v4/`。参考 [OpenAI API 兼容](https://docs.bigmodel.cn/cn/guide/develop/openai/introduction) 和 [HTTP API](https://docs.bigmodel.cn/cn/guide/develop/http/introduction)。
- GLM Coding Plan 的 OpenAI Chat Completions 端点：官方文档明确给出 `OpenAI Chat Completions` 协议端点 `https://open.bigmodel.cn/api/coding/paas/v4`，用于支持 OpenAI Compatible 的编程工具。参考 [GLM Coding Plan 接入工具](https://docs.bigmodel.cn/cn/coding-plan/tool/others)。
- GLM Coding Plan 的 Anthropic Messages 端点：官方文档给出 `Anthropic Messages` 协议端点 `https://open.bigmodel.cn/api/anthropic`。该端点已落地为独立 Anthropic v1 协议档案，不混入 OpenAI v1 档案。

结论：

- 智谱支持 OpenAI 兼容协议；当前可核验的上游直连接口包含 OpenAI Chat Completions 和 Anthropic v1 Messages，但 OpenAI Responses 仍不作为 GLM 原生上游能力声明。
- Codex / OpenAI 客户端必须使用 Responses 协议时，不能再通过 API Key 或策略路由显式桥接；目标应是一个真实上游为 GLM Chat 且显式配置 `responses -> chat_completions` 的普通 GLM OpenAI v1 账户。
- 通用 GLM API、GLM Coding Plan OpenAI Chat 和 GLM Coding Plan Anthropic v1 都用 API Key 鉴权，不是 OAuth；创建账户时应像 GPT 一样先区分接入类型，但底层 `accounts.type` 仍保存为 `api_key`。
- 通用 GLM API、GLM Coding Plan OpenAI Chat 和 GLM Coding Plan Anthropic v1 必须使用不同 `provider_protocol_profile_id`，不能只靠同一个 `glm` 供应商和不同 `base_url` 混用，否则分组、额度、模型目录、账号测试和排障都会混在一起。

## 供应商与协议档案

新增供应商：

```ts
type ProviderCode = 'glm'
```

建议显示名称为 `智谱 GLM`。`glm` 是独立供应商，不能复用 `gpt` 供应商、`profile_gpt_openai_v1` 或 GPT / Codex 客户端策略。`providerCode=openai` 仍作为 OpenAI v1 聚合目录；GLM 的 OpenAI v1 模型可以进入 `openai` 聚合目录，但 GLM 自身账号池、分组、模型价格和响应策略归属于 `glm`。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 客户端画像 | 真实 endpoint mode | 跨协议转换 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `profile_glm_general_openai_v1` | `glm` | `openai/v1` | `https://open.bigmodel.cn/api/paas/v4/` | 通用 GLM API Key | 网关内部识别 | `chat_json`、`chat_sse` | 不启用 |
| `profile_glm_coding_openai_v1` | `glm` | `openai/v1` | `https://open.bigmodel.cn/api/coding/paas/v4` | GLM Coding Plan Key | 网关内部识别 | `chat_json`、`chat_sse` | 普通账户可显式配置 Responses -> Chat |
| `profile_glm_coding_anthropic_v1` | `glm` | `anthropic/v1` | `https://open.bigmodel.cn/api/anthropic` | GLM Coding Anthropic Key | Anthropic API / Claude Code | `messages_json`、`messages_sse` | 不启用 |

OpenAI Chat 两套档案的账户真实能力都只声明 `chat_completions` 端点族。Anthropic v1 档案只声明 Messages / Models，不默认写入 `message_token_counting`。不要默认写入 `responses_json` 或 `responses_sse`，除非官方后续明确提供 OpenAI Responses 兼容接口并完成真实验证。普通 GLM 账户不会隐式启用 Codex bridge；需要把 Responses / Codex Responses 转到 GLM Chat 时，必须通过账号模型别名显式声明 `responses -> chat_completions`，账户真实 endpoint modes 仍保持 `chat_json/chat_sse`。

存储实现必须允许同一供应商在同一协议版本下存在多条协议档案。当前如果 `provider_protocol_profiles` 仍用 `UNIQUE(provider_code, protocol_code, protocol_version)` 作为唯一约束，GLM 两条 `glm + openai/v1` 档案会互相冲突；落地前应改为以 `id` 作为稳定唯一键，或新增 `profile_kind` / `connection_type` 后按 `provider_code + connection_type` 做业务唯一约束。分组名称唯一、默认分组唯一和账户加入分组校验按 `provider_code` 维度执行。

`https://open.bigmodel.cn/api/anthropic` 已接入为独立 Anthropic v1 档案：

- 已新增 `protocolCode=anthropic`、对应协议版本、`messages` 端点族和 Anthropic adapter。
- Anthropic Messages 请求不再塞进 OpenAI v1 Chat Completions 档案。
- 网关仍不把 OpenAI Chat Completions 自动翻译为 Anthropic Messages；两个档案是并列协议入口，而不是互相隐式翻译。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

选择 `智谱 GLM` 后展示三个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 下游客户端画像 | 凭据字段 | 默认检查模型 |
| --- | --- | --- | --- | --- | --- |
| 通用 GLM API Key | `api_key` | `profile_glm_general_openai_v1` | 由 API Key / 请求识别 | `api_key`、`base_url` | `glm-5.2` |
| GLM Coding Plan Key | `api_key` | `profile_glm_coding_openai_v1` | 由 API Key / 请求识别 | `api_key`、`base_url` | `glm-5.2` |
| GLM Coding Anthropic Key | `api_key` | `profile_glm_coding_anthropic_v1` | Anthropic API / Claude Code | `api_key`、`base_url` | `glm-5.2` |

这里的“接入类型”是产品表单概念，不是新增 OAuth 类型。后端保存时仍加密保存 `credentials.api_key`，并通过 `provider_protocol_profile_id` 区分通用 GLM API 与 GLM Coding Plan。

保存规则：

- 新建 GLM 账户默认写入 `pending_test`，测试通过后才允许正常调度。
- `base_url` 默认按所选接入类型填充，允许用户修改为同协议的代理地址或专属部署地址，但必须继续通过 SSRF 防护和 OpenAI-compatible base URL 校验。
- `credentials.supported_endpoint_modes` 省略时，GLM 两种 OpenAI 接入类型都默认 `['chat_json', 'chat_sse']`。
- `profile_glm_coding_anthropic_v1` 的 `credentials.supported_endpoint_modes` 省略时默认 `['messages_json', 'messages_sse']`，不默认启用 `message_token_counting`。
- GLM 账号不再保存账户级客户端兼容开关；通用 GLM 和 GLM Coding OpenAI 档案都只保存真实 `chat_json/chat_sse` 上游能力。Codex Responses 客户端画像由网关内部识别；是否把该下游请求转换到 GLM Chat 上游，由普通 GLM 账号模型别名的 `responses -> chat_completions` 配置决定。
- 管理 API、导入协议和公开外部账号推送必须直接接收 `providerProtocolProfileId = profile_glm_general_openai_v1 | profile_glm_coding_openai_v1 | profile_glm_coding_anthropic_v1`。旧接入类型别名已从接口契约移除，不能再解析。
- 已保存 GLM 账户不建议在编辑时切换通用 / Coding OpenAI / Coding Anthropic 接入类型；如果后续允许切换，必须要求账户未绑定分组、无授权实例或完成独立清理计划，不能在已有调度和统计归属上原地改档案。
- GLM 账户不显示 GPT OAuth 字段，不显示 Refresh Token、Access Token 或 ChatGPT Account ID。
- 通用 GLM 账户只具备 Chat Completions 请求能力。GLM OpenAI v1 账户如需承接 Responses / Codex Responses，应在普通账号模型别名中声明 `responses -> chat_completions`，并继续只保存真实 GLM Chat 上游配置；这不是 GPT API Key 的原生 Responses 能力，也不开放完整 Responses API。
- GLM Coding Anthropic 账户只具备 Anthropic v1 Messages 请求能力，不承接 Codex Responses。
- Coding Plan Key 与平台其他 API Key 不通用；如果用户把通用 API Key 填到 Coding Plan 档案，可能无法使用 Coding 套餐额度，应通过表单提示和测试结果提示区分。

## 网关请求边界

GLM OpenAI v1 档案复用 OpenAI v1 Chat Completions 协议适配器，但有独立供应商策略。

请求路径：

- 客户端可请求 `/chat/completions` 或 `/v1/chat/completions`。
- 网关把请求发往所选档案的 `base_url + /chat/completions`。GLM 不能直接复用会给非 `/v1` Base URL 自动追加 `/v1` 的 OpenAI helper，否则 `https://open.bigmodel.cn/api/paas/v4/` 会被错误拼成 `/api/paas/v4/v1/chat/completions`。
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求智谱上游模型列表。
- `/responses` 和 `/v1/responses` 默认不进入未配置映射的 GLM 通用 API 或 GLM Coding OpenAI 账号候选；只有普通 GLM OpenAI v1 账户明确声明 `responses -> chat_completions` 且真实上游支持 `chat_sse` 时，才走本地 bridge 改写到该账号自己的上游 `/chat/completions`。

请求体：

- 默认保持 raw body passthrough，保留智谱扩展字段，例如 `thinking`、`reasoning_effort`、`tool_stream`、`do_sample` 等官方支持字段。
- 账户测试请求必须使用 Chat Completions 形态，避免沿用 GPT OAuth / Responses 测试路径。
- GLM 对 Chat Completions `messages[].role` 的支持应以官方文档和真实测试为准。若客户端传入 OpenAI 新式 `developer` role，GLM 可能返回角色错误；应在 GLM 供应商兼容层中把 `developer` 限定性降级为 `system`，或在本地提前返回清晰错误，不能把该规则写进全局 OpenAI v1 协议层。
- 一些 OpenAI 客户端允许 `temperature` 大于 `1`，而 GLM 可能只接受更窄范围。默认透传时让上游返回真实错误；如果后续需要本地纠偏，应作为 GLM 供应商兼容规则配置，不能影响 GPT 或其他 OpenAI-compatible 供应商。

请求头：

- 上游鉴权统一写入 `Authorization: Bearer <GLM API Key>`。
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩相关头、SDK / tracing / 部署平台噪声头。
- 不从账号配置生成 `OpenAI-Organization`、`OpenAI-Project` 或 GPT / Codex 专属 header。
- Coding Plan 面向指定工具和产品环境；网关不应无故覆盖客户端 `User-Agent`。在安全过滤之后，尽量保留工具侧用于官方识别的普通请求形态，避免影响套餐额度识别。

## Codex Responses 到 Chat 桥接

GLM Coding Plan 当前需要支持 Codex 客户端时，Codex 客户端只发送 OpenAI Responses 协议。智谱和 vsllm 的 GLM Chat 上游没有验证到可直接承接 `/v1/responses`，因此该能力必须通过普通 GLM OpenAI v1 账户的模型别名显式表达：下游仍看见 Responses SSE，账号真实上游调用 GLM Chat Completions SSE。API Key 和策略路由层不再保存 `responses -> chat_completions` 显式规则。

通用转换设计见 [Codex Responses 转 Chat 协议转换设计](Codex%20Responses转Chat协议转换设计.md)。GLM、DeepSeek 等只支持 Chat Completions 的 OpenAI v1 供应商通过普通账号 `responses -> chat_completions` 显式模型别名复用共享转换层；Anthropic/Gemini native 等其他跨协议方向仍由混合供应商账户复用共享转换层。

启用条件：

- 命中的账户必须是 GLM OpenAI v1 账户，且该账户显式配置 `responses -> chat_completions` 模型别名。
- 客户端画像由网关内部自动识别；账号模型别名只声明允许的下游协议和模型映射，不接收 API Key 默认画像。
- 下游请求必须命中账号允许的 OpenAI Responses 来源模型。
- 请求必须是 `POST /responses` 或 `POST /v1/responses`，并且是流式 Responses 请求。
- 目标账户真实能力必须支持 `chat_sse`。

请求转换：

- `/v1/responses` 改写为上游 `/chat/completions`，保留查询参数。
- `instructions` 转为 system message。
- Responses `input` 转为 Chat `messages`；`function_call` / `function_call_output` 转为 Chat 工具调用历史。
- 透传 `type=function` 的工具；`custom` 工具包装为 `custom__<name>` Chat function；`web_search`、MCP、image generation、computer use 等 Responses 原生托管工具不由 Chat-only bridge 代执行或伪造。GLM 搜索如走官方 MCP，应由调用方在本地 agent / MCP 配置后自行调用。
- `max_output_tokens` / `max_completion_tokens` 转为 `max_tokens`。
- 清理 Codex 专属 header，并把上游请求固定为 Chat SSE。

响应转换：

- Chat `delta.content` 和 `delta.refusal` 转为 Responses `response.output_text.delta`。
- Chat `delta.reasoning_content` 不转为普通文本，避免把 GLM 推理字段泄露给 Codex 普通输出；后续如需展示 reasoning，必须单独实现 Responses reasoning item 映射。
- Chat `delta.tool_calls` 累积后转为最终 `response.output_item.done` 的 `type=function_call` item。根据本次核对的 Codex 源码，普通 function call 不依赖 `response.function_call_arguments.delta`。
- Chat `web_search` wrapper 只代表普通 Chat function 调用能力，不能等价为 Responses 原生 `web_search`。当前桥接层会对下游传入的 `web_search` 返回正常 agent guidance，不向 GLM Chat 上游发送伪工具，也不输出 `web_search_call` 或 citation。
- Chat usage 转为 Responses usage；`reasoning_tokens` 保留在 `output_tokens_details.reasoning_tokens`。

边界：

- GLM Coding bridge 不等于 GLM 原生支持 OpenAI Responses；账户 `supported_endpoint_modes` 仍保存 `chat_json/chat_sse`。
- GLM Coding bridge 不由普通 profile 或账号字段隐式开启；必须由普通账号模型别名显式启用。审计和排障应展示命中的账号、真实 GLM 上游和协议转换方向。
- 非 Codex 客户端、非流式 Responses、`web_search`、MCP、image generation 和 computer use 不在 Chat-only bridge 执行范围内；`/responses/compact` 由网关 summary compact 状态层处理，不是上游原生 compact。
- 通用 GLM API Key 不启用 Codex bridge。
- 审计和排障应同时展示下游 `/v1/responses` 与上游 `/chat/completions`，并保留 provider/profile/model 维度。

## 返回与 usage

GLM Chat Completions 返回应优先按 OpenAI v1 Chat 语义解析：

- JSON 响应读取 `choices[].message.content`、`choices[].message.tool_calls`、`finish_reason` 和 `usage`。
- 流式响应读取 `choices[].delta.content`、`choices[].delta.tool_calls` 和最后 usage chunk；如果上游没有返回 usage，只在已有可见输出时按现有估算兜底。
- 如果 GLM 返回 `reasoning_content` 或类似推理文本字段，响应语义检查和诊断展示应把它作为可见 / 诊断语义来源之一，但是否写给下游要遵循客户端协议兼容策略。
- usage 解析在保留 OpenAI Chat 通用字段的同时，要额外识别 GLM 可能返回的缓存命中、推理 token 或厂商扩展 usage 字段；如果字段未被本地归一化，必须在审计摘要中保留有界原始 usage 片段用于后续修正。

成本与统计：

- 通用 GLM API 的 token 成本按 GLM 模型目录价格估算。
- GLM Coding Plan 是套餐权益和 prompt / 周期额度语义，不等同于普通 API 余额扣费。本地仍可记录 token、请求数、错误数和成本估算，但不能把本地美元成本当作 Coding Plan 官方额度。
- Coding Plan 官方文档提到 5 小时和每周使用额度、不同套餐 prompts 额度和模型倍率；这些属于 Coding Plan 专属展示信息。当前不主动抓取或猜测套餐额度，只记录真实上游响应和本地使用统计。
- 不要臆造智谱 rate-limit header。只有真实请求或官方文档确认的响应头才能进入额度快照设计。

## 模型目录

GLM 模型目录必须单独维护在 `glm` 供应商下，不要混进 GPT 价格文件。

当前 GLM Chat 档案目录按官方文本模型维护；视觉、图像、视频、音频模型不混入这两个只声明 Chat Completions 的档案。当前可见文本模型包括，列表顺序同时作为账户测试和模型下拉框的默认优先顺序，按当前官方模型从新到旧维护：

- `glm-5.2`
- `glm-5.1`
- `glm-5`
- `glm-5-turbo`
- `glm-4.7`
- `glm-4.7-flashx`
- `glm-4.7-flash`
- `glm-4.6`
- `glm-4.5`
- `glm-4.5-x`
- `glm-4.5-air`
- `glm-4.5-airx`
- `glm-4.5-flash`

`glm-5.2-free` 未出现在本次复核的官方模型和价格页里，已从内置模型与价格目录移除，也不再作为通用 GLM 默认检查模型。第三方中转仍暴露该别名时，只能由用户显式创建自定义模型和映射。

按当前产品目录边界，GLM 只内置 4.5 及更新系列。`glm-4-32b-0414-128k`、`glm-4-flashx-250414`、`glm-4-flash-250414` 已从内置事实源删除，不再为单一旧模型生成批量处理或美元换算列；第三方上游仍提供这些 ID 时只能显式创建自定义模型。

模型目录字段要求：

- `providerCode = glm`
- `model` 使用智谱官方模型 ID，小写和连字符按官方写法保留。
- `supportedApiProtocols` 当前填 `chat_completions`。
- `contextWindowTokens` 以官方模型页或 Coding Plan 工具文档为准；例如 Coding Plan 文档提示 `glm-5.2` 可按 `1000000` 上下文配置，其他模型常见为 `200000`，实际落库前仍需以官方模型页复核。
- `releaseDate` 用官方发布日期或官方文档可确认的上线日期，不确定时留空，不为了排序编造；同一代或日期不确定的模型使用内置目录顺序字段保持下拉框从新到旧。
- 价格只采信智谱官方价格页或官方模型页；第三方价格库和社区表格只能作为线索。

智谱开源模型，例如 `GLM-4.5`、`GLM-5` 仓库和 Hugging Face / ModelScope 权重，可以作为自托管、vLLM、SGLang 或 Transformers 接入参考，但不等于智谱云 API 的模型 ID、上下文窗口或价格。自托管 GLM 如果通过 OpenAI-compatible vLLM / SGLang 暴露，应作为自定义 OpenAI-compatible 上游或独立自托管供应商处理，不直接套用智谱云价格。

## 分组、授权与 API Key 路由

GLM 默认只创建一个供应商默认分组：默认 GLM 分组。通用 GLM API、GLM Coding OpenAI Chat 和 GLM Coding Anthropic Messages 的差异由 AI 账户接入类型和 `provider_protocol_profile_id` 表达，不再拆成多个默认分组。

账户加入分组只校验同一 `providerCode=glm`。分组只负责调度、并发、排序和 API Key 号池，不承接通用 / Coding / Anthropic 的协议语义。

API Key 绑定的路由策略允许跨供应商分组；一个本地 API Key 可以通过策略路由同时使用 GPT、GLM、DeepSeek 等供应商分组。请求进入网关后先按模型和账户能力筛选目标供应商，再在目标供应商分组内按账户自身接入类型、endpoint mode、支持模型和模型映射过滤候选账户。跨供应商路由详见 [自定义模型与模型映射设计](自定义模型与模型映射设计.md) 和 [策略路由设计](策略路由设计.md)。

## 账号测试

GLM 账户测试必须复用真实网关链路：

- OpenAI 标准测试路径使用 `/v1/chat/completions`。
- GLM Coding 账户的普通测试路径使用 `/v1/chat/completions`；需要验证 Codex Responses bridge 时，应在普通 GLM OpenAI v1 账户配置 `responses -> chat_completions` 模型别名后从 `/v1/responses` 进入本地 bridge，下游请求带 Codex turn metadata，上游实际仍是该账号自己的 `/chat/completions`。
- 新建账户的检查模型按“个人默认 > 管理员系统默认 > 协议档案默认”初始化；当前档案默认均为 `glm-5.2`，保存后后台检查严格使用账户自己的 `healthCheckModel`。
- OpenAI 标准测试请求不发送 Responses 字段，例如 `input`、`instructions`、`max_output_tokens`、`store` 或 Codex 专属 metadata；Codex bridge 测试只发送最小 Responses SSE 请求。
- 人工测试成功或失败都不改写账户状态、检查模型、调度、冷却或最近错误。
- Coding Plan 测试会消耗套餐资源，应保持最小 prompt、低输出上限和明确的用户提示；账户页面不提供多账户批量测试。

## 接入前缺口审计

下列事项是正式实现前必须补齐或明确的缺口，避免把 GLM 当成 GPT API Key 的简单换壳：

| 范围 | 必补事项 | 风险 |
| --- | --- | --- |
| 官方资料 | 实现前再次核对智谱 OpenAI 兼容、HTTP API、Coding Plan 工具接入、错误码和模型价格页。当前只声明可核验的 Chat Completions 上游能力，不声明原生 Responses；Codex 走本地 bridge。 | 官方文档存在“兼容 OpenAI Endpoint”这类宽泛表述，但实际示例和 API Reference 仍需逐项确认；误开原生 Responses 会导致客户端请求被错误调度。 |
| SQLite schema | `provider_protocol_profiles` 必须支持同一 `provider_code + protocol_code + protocol_version` 下多条档案，或新增 `connection_type/profile_kind` 参与业务唯一约束。 | 现有唯一约束如果只按供应商和协议版本，会阻止 `profile_glm_general_openai_v1` 与 `profile_glm_coding_openai_v1` 同时 seed；Anthropic Messages 档案也必须独立保存。 |
| 供应商档案 | 代码中新增 `glm`、`profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1`、`profile_glm_coding_anthropic_v1`、单个默认 GLM 分组和 profile family，不复用 GPT、通用 OpenAI 或官方 Anthropic 档案。 | 只靠 `base_url` 区分会导致模型目录、价格、账号测试和错误排障混用。 |
| Driver 注册 | 新增 GLM provider driver、credential driver、model pricing/catalog driver 和前端 provider capability，不只新增数据库 seed。 | 未注册 driver 时会在凭据归一化、上游 URL 构造、模型目录或价格查找处失败。 |
| 账户创建 | 前端、后端写接口、导入协议和外部推送接口都要显式接收 `providerProtocolProfileId`，编辑时默认不允许原地切换接入类型。 | 只保存 `accounts.type = api_key` 无法区分通用 API Key、Coding Plan Key 与 Coding Anthropic Key，后续无法判断额度、默认模型和测试入口。 |
| 导入导出 | GLM 导出必须输出可 round-trip 的 `providerProtocolProfileId`，导入预览、确认导入和导出 JSON 的字段白名单保持一致。 | 如果导出只保留 `providerCode/type/base_url`，重新导入时只能猜档案，容易把 Coding Key 导成通用账号。 |
| Base URL | 通用档案默认 `https://open.bigmodel.cn/api/paas/v4/`，Coding OpenAI 档案默认 `https://open.bigmodel.cn/api/coding/paas/v4`，Coding Anthropic 档案默认 `https://open.bigmodel.cn/api/anthropic`；路径拼接必须验证不会生成重复 `/v1`、漏掉 `/chat/completions` 或误拼 Anthropic `/messages`。 | OpenAI SDK 的 `base_url` 语义与上游 HTTP API 原始路径容易混淆；复用自动追加 `/v1` 的 helper 会造成 `/v4/v1/chat/completions`。 |
| Endpoint mode | GLM 两个 OpenAI v1 档案真实能力默认仅 `chat_json/chat_sse`；GLM Coding 只在普通账号显式 `responses -> chat_completions` 模型别名命中且请求侧为 Responses 语义时，通过共享 bridge 承接流式 `/v1/responses`。 | 如果沿用 GPT API Key 默认四项能力，Responses 请求可能被误认为上游原生支持；如果把 bridge 隐藏在 profile 里，路由和审计无法判断该请求为什么跨协议。 |
| 协议转换 | Codex Responses -> Chat Completions 转换必须复用共享 adapter，供应商 driver 只配置启用条件、默认模型、Base URL 和局部差异。 | 如果把转换写死在 GLM，后续 DeepSeek 等供应商会复制协议逻辑，工具调用、reasoning、usage 和错误事件容易分叉。 |
| 请求兼容 | 默认透传 GLM 扩展字段；`developer` role、`response_format`、`temperature`、`thinking/reasoning_effort` 等差异只在 GLM 供应商层处理或提示，不写进全局 OpenAI v1 规则。 | 全局改写会影响 GPT、DeepSeek 和其他 OpenAI-compatible 上游；完全忽略差异会让常见客户端错误难排查。 |
| 错误处理 | 通用客户端完整 `2xx` / 普通 JSON / SSE payload 透明；安全文本完整非 `2xx` 只按 `response.ok=false` 做内容无关的请求级候选接管，transport / timeout 进入统一错误输入。图片、文件和其他副作用 POST 派发后保持 at-most-once。只有精确客户端画像或用户显式策略才允许解释声明范围内的 `finish_reason` / Coding Plan 错误码，且不能越过请求语义门禁。 | 直接按状态码或错误正文硬编码限流 / 异常会破坏协议中立边界。 |
| 账号切换与恢复 | GLM 失败后先走本地短暂屏蔽、候选切号、事前确认和冷却复测；手动测试、健康检测和恢复探活都使用最小 Chat Completions 请求，并标记 `manual_account_test` 或 `cooldown_retest`。 | Coding Plan 测试会消耗套餐；后台批量探测或错误恢复如果不节流，会把套餐额度浪费用在恢复任务上。 |
| 模型目录 | GLM 云 API 模型、Coding Plan 模型、历史别名和 `glm-5.2[1m]` 这类工具侧模型后缀要逐项确认；开源权重只作为自托管参考，不等于云 API 模型。 | 把开源模型 ID、工具侧别名或已自动升级旧模型当成官方 API 计价模型，会导致 `/v1/models`、模型映射和成本估算失真。 |
| 价格与 usage | 按官方价格页维护 GLM 价格；解析 `usage.prompt_tokens_details.cached_tokens`、推理 token 和 GLM 扩展 usage 字段；Coding Plan 的本地成本只作估算，不代表官方套餐余额。 | 普通 API 计费和 Coding 套餐权益语义不同，不能把本地美元成本展示成 Coding Plan 额度；漏解析推理 / 缓存字段会导致成本和诊断偏差。 |
| 统计聚合 | `usage_records`、审计尝试、模型排行、错误排行和 AI 性能窗口都必须带 `providerCode=glm`、`provider_protocol_profile_id` 和实际 `model`；业务统计继续由 worker 预聚合，不在接口实时扫明细。 | GLM 通用和 Coding 可能有同名模型，仅按 `providerCode + model` 聚合会混掉不同档案的用量和错误。 |
| 模型映射 | 下游模型名必须路由到目标 `provider_protocol_profile_id`；账户级模型映射要保存下游模型和 GLM 实际上游模型，用实际上游模型查价。 | 误把 `providerCode=openai` 聚合目录当作账号池，或让模型名在多个供应商档案中含义不清，会导致跨供应商错调度。 |
| 审计排障 | 审计尝试、使用记录详情和错误详情应展示 `providerProtocolProfileId`、GLM 接入类型、下游模型、实际上游模型、价格模型和有界 usage 摘要。 | Coding Plan Key 错档案、同名模型路由、usage 字段异常时，只靠账号和分组反查定位成本高。 |
| 授权与分组 | GLM 账户按供应商加入 GLM 分组；授权实例继承来源账户档案和上游凭据事实，调度时再按账户接入类型、endpoint mode、支持模型和模型映射过滤。 | 如果把通用 / Coding / Anthropic 写成分组语义，会导致同一供应商默认分组膨胀，用户创建账户时难以选择正确号池。 |
| 前端体验 | 账户创建页、编辑页、批量导入说明、模型选择、测试连接、错误提示、状态标签和日志筛选都要显示中文，并明确区分“通用 GLM API Key”、“GLM Coding Plan Key”和“GLM Coding Anthropic Key”。 | 用户把 Key 填错档案时，如果前端只显示“API Key”，无法定位是 Key 无效还是接入类型选错。 |
| Mock 与验证 | Mock AI 回归脚本和真实烟测需要覆盖三套 GLM 档案、Chat JSON/SSE、Messages JSON/SSE、未配置映射的普通账号拒绝 Responses、普通账号显式 Responses -> Chat bridge、错误响应、无 usage 流式、错档案 Key、分组硬边界和统计落库。 | 只测保存账户会漏掉网关调度、协议转换、切号、恢复、usage 和统计链路的集成问题。 |
| Anthropic 端点 | Coding Plan 的 Anthropic Messages 端点已纳入独立 `profile_glm_coding_anthropic_v1`，只启用 Messages JSON/SSE。 | 该端点不能混进 OpenAI Chat 档案；公开 schema、header、usage 和 stream 细节仍需按 mock / 真实上游持续验收。 |
| 套餐规则 | Coding Plan 官方定位为指定开发工具和产品环境使用；中转暴露给通用业务调用前需要确认使用边界。 | 错把 Coding Plan 当普通 GLM API 池对外共享，可能不符合套餐规则或触发公平使用限制。 |

## 导入导出协议

账户导入协议新增 GLM 示例时，应使用项目自定义 JSON，不直接兼容智谱控制台导出格式。

推荐写法：

```json
{
  "name": "智谱 GLM Coding 账号 1",
  "providerCode": "glm",
  "providerProtocolProfileId": "profile_glm_coding_openai_v1",
  "type": "api_key",
  "status": "pending_test",
  "groupName": "默认 GLM 分组",
  "credentials": {
    "api_key": "zhipu-api-key",
    "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
    "supported_endpoint_modes": ["chat_json", "chat_sse"]
  }
}
```

`providerProtocolProfileId` 是导入协议层用于区分同供应商多 API Key 接入类型的唯一字段。后端落库时以 `providerCode + providerProtocolProfileId` 校验目标档案，再保存 `accounts.type = api_key`；不能只靠用户手填 `base_url` 推断档案。

导出 GLM 账户时也必须写回 `providerProtocolProfileId`，保证“导出 JSON -> 导入预览 -> 确认导入”能还原到原 `provider_protocol_profile_id`。

## 实施清单

- [x] 新增 `glm` 供应商种子、供应商常量和 profile family。
- [x] 调整 `provider_protocol_profiles` 唯一约束，允许同一 `glm + openai/v1` 下存在通用和 Coding 两套档案。
- [x] 新增 `profile_glm_general_openai_v1` 和 `profile_glm_coding_openai_v1`。
- [x] 新增 `profile_glm_coding_anthropic_v1`。
- [x] 新增单个默认 GLM 分组，账户按接入类型区分协议档案。
- [x] 新增 GLM provider driver、credential driver、模型价格 / 目录 driver 和前端 provider capability。
- [x] 前端账户创建在选择 `智谱 GLM` 后展示“通用 GLM API Key”、“GLM Coding Plan Key”和“GLM Coding Anthropic Key”。
- [x] 后端账户创建、编辑、导入、导出和公开推送接口按 `providerProtocolProfileId` 校验 GLM 协议档案，并保证导出导入 round-trip。
- [x] GLM 默认 `supported_endpoint_modes` 只启用 Chat JSON/SSE。
- [x] GLM Coding Anthropic 默认 `supported_endpoint_modes` 只启用 Messages JSON/SSE。
- [x] 普通 GLM OpenAI v1 账号模型别名可显式承接 `responses -> chat_completions`；Anthropic / Gemini native 等其他跨协议桥接继续由混合供应商账户承接。
- [x] GLM 上游 URL 构造专门覆盖 `/api/paas/v4` 和 `/api/coding/paas/v4`，不会自动追加 `/v1`。
- [x] GLM Anthropic v1 档案复用共享 Anthropic adapter，但保持独立供应商 / 分组 / 统计语义。
- [x] 抽出通用 `Codex Responses -> Chat Completions` bridge；普通 GLM Coding 档案不再隐式启用，由账号模型别名显式复用。
- [x] Mock 回归覆盖普通 GLM Coding OpenAI 标准账号拒绝 Codex / Responses，避免把 Chat-only 账号误认为原生 Responses 上游。
- [x] 账号测试、手动诊断、健康检测和冷却复测默认走 Chat Completions。
- [x] GLM 模型目录和价格目录单独建文件，成本估算按 `providerCode=glm` 查找。
- [x] 模型映射和账号支持模型限制按 GLM 目录校验，并能把下游模型路由到正确 GLM 档案。
- [x] 使用记录、审计尝试、错误处理、冷却恢复和统计链路复用现有 provider / profile / model 维度。
- [x] 导入示例、mock AI 回归脚本、真实烟测脚本和测试说明同步更新。
- [x] 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗和测试说明同步更新。

## 实测记录

2026-06-20 使用真实 `https://vsllm.com/v1` 上游做过验证：

- 直接 Chat Completions：`glm-5.2` 和 `glm-5-turbo` 可返回 200 和 usage；`glm-5.2-free` 在 60 秒窗口内超时。
- 直接 `/v1/responses`：上游不按 Codex Responses 协议成功返回；此前最小 Responses 请求在付费模型上返回上游 `输入不能为空` 类错误。
- 历史本地 GLM Coding Codex bridge 曾验证 `glm-5.2` 和 `glm-5-turbo` 可返回 Responses SSE；该路径现已从 API Key / 策略路由层移除，当前真实验证应改为普通 GLM OpenAI v1 账号显式 `responses -> chat_completions` 模型别名。
- Mock AI 已覆盖 Chat JSON/SSE、GLM Coding OpenAI 标准账号拒绝 Codex / Responses、错误切号和分组硬边界。

2026-06-24 本轮修订复测：

- `pnpm --filter juhe-ai-backend test:glm-real-gateway-e2e` 当前只覆盖 GLM Chat JSON/SSE 真实能力；旧 bridge 真实记录仅作为历史，不再作为当前验收项。
- 同轮 mock 回归继续覆盖 `web_search` guidance 且不命中 GLM Chat 上游；GLM 搜索不由本地 bridge 代执行，应走调用方本地 MCP、GLM 官方 MCP 或未来上游原生能力。

## 验证要求

当前代码落地后至少覆盖：

- 创建通用 GLM API Key 账户，保存后落到 `profile_glm_general_openai_v1`。
- 创建 GLM Coding Plan Key 账户，保存后落到 `profile_glm_coding_openai_v1`。
- 两类账户默认 `supported_endpoint_modes` 均为 `chat_json/chat_sse`；通用 GLM 和 GLM Coding 普通账号只有在显式 `responses -> chat_completions` 模型别名命中时才通过 bridge 承接流式 Responses。
- 两类账户绑定同一 GLM 供应商分组，账户协议档案由账户接入类型区分。
- OpenAI 标准账户测试使用 `/v1/chat/completions`；GLM Coding 普通账户测试也使用 `/v1/chat/completions`；通用 GLM API 默认模型为 `glm-5.2`，GLM Coding Plan 默认模型为 `glm-5.2`。
- 网关请求通用 GLM API 时命中 `https://open.bigmodel.cn/api/paas/v4/chat/completions`。
- 网关请求 GLM Coding Plan 时命中 `https://open.bigmodel.cn/api/coding/paas/v4/chat/completions`。
- Codex 客户端请求 GLM Coding 普通账户 `/v1/responses` 且未命中 `responses -> chat_completions` 模型别名时必须被本地拒绝或跳过；命中显式映射时，才允许本地改写到该账号自己的 GLM Coding `/chat/completions`。
- GLM Coding bridge 不向下游泄露 `chat.completion.chunk`，也不把 `reasoning_content` 作为普通 `output_text`。
- `GET /v1/models` 返回本地可见 GLM 模型，不请求智谱上游。
- GLM 响应 usage 能写入使用记录；无 usage 的流式可见输出按现有估算兜底。
- Coding Plan Key 错填到通用档案或通用 Key 错填到 Coding 档案时，测试失败信息能让用户区分接入类型错误。
