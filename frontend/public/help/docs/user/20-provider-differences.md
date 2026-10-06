# 各供应商参数差异速查

> 这一篇解决：九类供应商各自的特有参数、回包差异、以及"同一个意图在不同供应商那里写法不同"的对照。透传总规则见《供应商参数怎么传：透传规则》——本篇是逐家明细。**所有条目都来自本系统接入实现与接入文档，没有的会明确标"项目内无记载"，不猜。**

## 先懂三个词：透传 / 改写 / 受控拒绝

| 状态 | 含义 |
| --- | --- |
| 透传 | 你怎么写，上游收到的就是什么（绝大多数参数的状态） |
| 改写 | 网关会替换或归一（仅模型映射改 `model`、账户级 `service_tier`/`reasoning_effort` 覆盖、少量格式归一——见透传规则篇） |
| 受控拒绝 | 跨协议转换时目标协议没有等价语义的字段，网关直接报错点名，不伪装成功 |

**逐家速查里，没有标"受控拒绝"的一律是透传。**

---

## OpenAI / GPT（provider：`openai`，`gpt` 是其别名）

| 参数 | 说明 | 写法 |
| --- | --- | --- |
| `service_tier` | 服务档位：`default` / `priority` / `flex`；计费按档分价 | 请求体顶层 |
| `reasoning_effort` | Chat Completions 思考级别：`none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` | 请求体顶层 |
| `reasoning.effort` | 同上，Responses 端点的形态（`reasoning` 对象） | Responses 请求体顶层 |
| `verbosity` | 响应详长度 | Responses 请求体顶层 |
| Multi-agent beta | GPT-5.6 系多智能体公测能力 | 请求体 `multi_agent.enabled` + 头 `OpenAI-Beta: responses_multi_agent=v1` |

回包：`usage.input_tokens_details.cached_tokens`（缓存命中）；流式 usage 在 `response.completed` / `response.failed` 事件里。

## Anthropic

| 参数 | 说明 | 写法 |
| --- | --- | --- |
| `max_tokens` | **必填**（Anthropic 官方要求，网关不代补） | 顶层 |
| `thinking` | 扩展思考对象 | 顶层，如 `{ "type": "enabled" }` 类形态 |
| `output_config.effort` | 思考强度 | 顶层 `output_config` 对象 |
| `cache_control` | prompt 缓存标记 | `messages[]` / `system` / `tools` 的 content block 内 |
| `system` 数组形态 | 顶层 `system` 是 block 数组，不是字符串 | `[{ "type": "text", "text": "…" }]` |
| `top_k` / `mcp_servers` / `container` / `context_management` / `metadata` | 原生字段，透传 | 顶层 |

请求头：`anthropic-version` 可省（上游自动补 `2023-06-01`）；`anthropic-beta` 客户端带则透传。回包：`content[]` block 结构（`text` / `tool_use` / `thinking`）、`stop_reason`、usage 带 `cache_creation_input_tokens` / `cache_read_input_tokens` / `output_tokens_details.thinking_tokens`。注意流式 `message_delta.usage` 是**累计值**，不要逐帧相加。

## Gemini

| 参数 | 说明 | 写法 |
| --- | --- | --- |
| `contents[].parts[]` | 消息体：parts 数组（`text` / `inlineData` / `fileData` / `functionCall` / `functionResponse`） | 顶层 `contents` |
| `generationConfig` | 生成配置：`maxOutputTokens` / `temperature` / `topP` / `stopSequences` / `responseMimeType` / `responseSchema` | 顶层 |
| `generationConfig.thinkingConfig.thinkingLevel` | Gemini 思考级别 | 嵌套 |
| `systemInstruction` | 系统提示词（独立顶层字段，不是 messages 里的 role） | 顶层 |
| `tools[].functionDeclarations` / `toolConfig` / `safetySettings` / `cachedContent` / `labels` | 原生字段，透传 | 顶层 |

**模型名在路径不在 body**：`/v1beta/models/{model}:generateContent`。回包：`usageMetadata` 含 `thoughtsTokenCount` / `cachedContentTokenCount` / `toolUsePromptTokenCount` 等细分。

## GLM / 智谱

| 参数 | 说明 | 写法 |
| --- | --- | --- |
| `thinking` | GLM 思考开关（厂商扩展） | 顶层对象，如 `{ "type": "enabled" }` |
| `reasoning_effort` | GLM 推理强度 | 顶层 |
| `tool_stream` / `do_sample` | GLM 官方字段，透传 | 顶层 |
| `temperature` | GLM 可能不接受大于 1 的值——网关照传，由上游报真实错误 | 顶层 |
| `developer` role | OpenAI 新式 `developer` role 可能被 GLM 拒绝——对 GLM 模型用 `system` 更稳 | `messages[].role` |

回包：思考内容在 `message.reasoning_content`（诊断字段，不拼入正文 `content`）。媒体面专有键：`provider_options.glm`（如 TTS 声音复刻 `ref_audio` / `ref_text`）。

## Qwen / 通义百炼

对话面是 **OpenAI Chat 报文零改写直传**——各家官方参数（如思考开关类字段）直接按阿里云百炼官方文档写即可，网关不校验不转码。

| 项 | 说明 |
| --- | --- |
| 回包特例 | DashScope 的 `usage` 是 JSON 字符串（内嵌一层），网关已兼容解析，你看到的仍是正常对象 |
| 媒体专有键 | `provider_options.qwen`：视频 `prompt_extend`；ASR `diarization_enabled` / `disfluency_removal_enabled` / `vocabulary_id` |
| 视频参数映射 | 网关自动转换：`negative_prompt` 生效（Qwen 是支持它的视频供应商）、`size` 的 `WxH` 会转为其 `W*H` 星号形态 |

## DeepSeek

| 参数 | 说明 | 写法 |
| --- | --- | --- |
| `thinking` | 思考模式开关：`{ "type": "enabled" }` / `{ "type": "disabled" }`；不传时思考模式默认开启 | 顶层 |
| `reasoning_effort` | 官方语义只有 `high` / `max`——传 `low`/`medium` 官方按 `high` 处理，`xhigh` 按 `max` 处理 | 顶层 |
| `user_id` | 官方滥用检测辅助字段，允许客户端传，透传 | 顶层 |
| `frequency_penalty` / `presence_penalty` | 官方已废弃；**思考模式下 `temperature` / `top_p` / penalty 均不生效** | 顶层 |
| Beta 能力 | 前缀续写（末条 assistant 消息加 `prefix: true`）与 FIM 补全，属上游 beta 面，可用性以管理员配置为准 | — |

回包：思考内容在 `choices[].message.reasoning_content` / `delta.reasoning_content`（不拼入正文）；usage 带 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`；`finish_reason` 可能出现官方值 `insufficient_system_resource`。

## MiniMax

**注意：账户层面 MiniMax 只承接媒体流量**（视频/TTS），模型目录里虽然列了 chat 协议的模型行，实际能不能调以管理员账户配置为准。

| 项 | 说明 |
| --- | --- |
| 视频专有键 | `provider_options.minimax`（如 `prompt_optimizer`） |
| `input_reference` | **仅接受 base64 / data URL**，http(s) URL 会被拒 |
| TTS `speed` | 有效区间 **0.5–2.0**，越界本地 400 |
| 回包特例 | TTS 音频为 hex 编码字符串，网关解码后你拿到的已是二进制音频 |

## 火山方舟 / 豆包

| 项 | 说明 |
| --- | --- |
| 对话面 | OpenAI Chat 零改写直传 |
| 视频专有键 | `provider_options.volcengine`（如 `camera_fixed`） |
| 视频分辨率 | `size` 的 WxH 会换算为 `resolution` + `ratio`；**ratio 必须精确命中词表**（16:9 / 9:16 / 1:1 / 4:3 / 3:4 / 21:9），不命中本地 400 |
| `input_reference` | URL / base64 都可以 |
| TTS `speed` | 有效区间 **0.2–3.0**（对应 `speed_ratio`） |
| TTS 专有键 | `provider_options.volcengine`（如 `emotion`） |
| 回包特例 | TTS 音频为 base64（与 MiniMax 的 hex 不同，但你拿到的都是网关解码后的二进制） |

## xAI / Grok

| 项 | 说明 |
| --- | --- |
| `reasoning_effort` | 枚举**按模型不同**：grok-4.7/4.6/4.5 是 `low`–`xhigh`（默认 high）；grok-4.3 多一个 `none`（默认 low）；multi-agent 模型的 effort 语义是**协作 agent 数量**（low/medium=4，high/xhigh=16），不是推理深度 |
| 工具声明 | `responses` 协议下声明了 `web_search` 工具（本系统各家目录里独一份） |
| 其余参数 | 项目内无额外记载——对话面零改写透传，按 xAI 官方文档写 |

## "思考级别"在四种协议里的字段对照

同一个"让模型多想/少想"的意图，不同协议写字段的位置不同——这是最容易写错的地方：

| 协议 | 字段 |
| --- | --- |
| Chat Completions | 顶层 `reasoning_effort` |
| Responses | 顶层 `reasoning.effort` |
| Anthropic Messages | 顶层 `output_config.effort`（另有 `thinking` 对象） |
| Gemini generateContent | `generationConfig.thinkingConfig.thinkingLevel` |
| GLM / DeepSeek（Chat 形态） | 顶层 `reasoning_effort`（GLM 另有 `thinking` 开关；DeepSeek 是 `thinking` 开关 + effort） |

你在哪个协议面上发请求，就写哪个字段；网关做跨协议转换时的换算规则见下一节。

## 跨协议时的字段限制

只有当请求被转换到"协议不同"的账户时才需要关心这一节（同协议直连永远透传）：

- **Anthropic → Chat 协议**：`thinking`、`cache_control`、`top_k`、`mcp_servers`、`container`、`context_management`、`service_tier` **无 Chat 等价语义 → 400 受控拒绝**（错误信息点名字段）；白名单外的其他字段会被丢弃。可自动映射的只有少数结构字段（`system`→system message、`stop_sequences`→`stop` 等）。
- **OpenAI → Gemini**：`reasoning_effort` 的 `minimal/low/medium/high` 会换算为 Gemini 思考档位；`logprobs` / `top_logprobs` 受控拒绝。
- **OpenAI → Anthropic**：`verbosity`、`n>1`、`logprobs` 等无等价能力，本地受控错误或明确忽略。
- `count_tokens` / `embedContent` / 文件与缓存类辅助端点**不参与任何跨协议转换**——只有原生同协议账户承接。

## 两句话收尾

1. **同协议直连 = 官方怎么写你就怎么写**，网关不删不改（透传规则篇的五条例外除外）。
2. **参数语义查供应商官方文档，能不能路由到该供应商查你的模型目录**——目录里没有的模型不要硬调。

## 接下来

- 透传总规则与 provider_options 通道：《供应商参数怎么传：透传规则》
- 三类协议面怎么选：《协议总览：三类协议面怎么选》
- 模型目录与可用性：《模型列表：我能用什么模型》
