# 供应商参数怎么传：透传规则

> 这一篇解决：你想用某个供应商的特有参数（比如 GLM 的 `thinking`、Qwen 的 `enable_thinking`、OpenAI 的 `verbosity`）时，在 juhe-ai 里怎么传、网关会不会动它、什么场景会被拒绝或丢失。

## 一条核心规则：文本对话面原样透传

对 `/v1/chat/completions`、`/v1/responses` 这类文本对话端点，网关对请求体的处理是**原样透传**：

- 供应商特有参数**直接写在请求体顶层**，与官方 API 的写法完全一致——不需要换字段名、不需要加前缀、不需要任何特殊配置。
- 网关不认识这些参数，也**不会删除、不会校验、不会改写**它们；参数是否生效由上游供应商裁决。
- 参数写错了怎么办？上游会报错，错误原样透传回给你。

**例子：给 GLM 模型开思考模式、给 Qwen 模型开思考、给 GPT 模型传 verbosity——三种写法完全就是各官方 API 的原生写法：**

```bash
# GLM：thinking 参数（智谱官方语义）
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5.3",
    "thinking": { "type": "enabled" },
    "messages": [ { "role": "user", "content": "证明 2+2=4" } ]
  }'
```

```json
{
  "model": "qwen3.8-max",
  "enable_thinking": true,
  "messages": [ { "role": "user", "content": "…" } ]
}
```

```json
{
  "model": "gpt-5.4",
  "verbosity": "low",
  "messages": [ { "role": "user", "content": "…" } ]
}
```

三条都原样到达上游。参数语义（`thinking` 怎么配、`enable_thinking` 取什么值）查各供应商官方文档；网关这一层不解释它们。

## 网关自己会"读"哪些字段

网关读少量字段用于**调度、计量、计费**——读完照样原样转发，不剥离、不改动：

| 字段 | 网关拿它做什么 |
| --- | --- |
| `model` | 选账户调度、计费、模型映射 |
| `stream` | 判定走流式还是同步 |
| `service_tier` / `reasoning_effort` | 计量与计费分档（priority/flex/batch 等价档位） |
| `max_tokens` / `max_output_tokens` | 仅记入用量快照，**不钳制不修改** |
| `tools` / `tool_choice` / `response_format` | 能力判定（如图像工具权限） |

除此之外的一切字段——包括所有供应商特有参数——网关不碰。

## 网关会改写什么：五种例外

透传是默认，但有五种场景网关会动请求体。每种都有明确的触发条件：

**1. 模型映射改写 `model` 字段。** 你的 Key 绑定的分组里，某账户配了"请求模型 → 上游模型"的映射并命中时，出站请求体的 `model` 被替换为映射后的上游模型（其余字段不动）。你无感知，但**响应里的 `model` 可能是上游真实拼写**，与你请求的名字不同。

**2. 跨协议转换时按白名单重建请求体。** 当请求被路由到"协议不同"的账户（比如 Anthropic 客户端 → Chat 协议账户，见《协议总览：三类协议面怎么选》），网关按目标协议**重建**请求体——重建时只保留目标协议有语义的字段，多余字段要么报错要么丢失：

- Anthropic → Chat 桥：请求体带 `thinking`、`cache_control`、`top_k`、`mcp_servers` 等字段会**直接 400 拒绝**（错误信息会点名该字段）；白名单外的其他字段**静默丢失**。
- Gemini 桥：`reasoning`/`thinking`/`logprobs` 等在场即拒绝；`reasoning_effort` 的部分取值会换算为 Gemini 的思考档位。

**结论：用供应商特有参数时，请确保模型由同协议账户承接**（一般用户无需判断——只要不刻意跨协议混用，遇 400 看错误信息即可）。

**3. 特定类型账户会删字段。** 极少数账户类型（如 OpenAI OAuth 的 Codex 类账户）对字段有硬性要求，转发前会删除 `stream_options`、`temperature`、`top_p`、`user`、`metadata` 等字段——这类账户由管理员标记，普通 API Key 分组一般不涉及；你的参数"莫名没生效"时可以找管理员确认账户类型。

**4. 管理员可配置账户级覆盖。** `service_tier`、`reasoning_effort` 两个字段可能被管理员在账户上配置的覆盖值改写（客户端传了也以覆盖为准）。

**5. 个别端点的格式归一。** `/chat/completions` 与 `/responses` 上 `reasoning` 与 `reasoning_effort` 双形态会被归一为该端点的规范形态（语义不变）；Anthropic `/messages` 请求体里的 `"stream": false` 会被删除（等价语义）。

## 请求头：你的认证头到不了上游

顺带说清请求头的规则：网关转发时**删除**你带来的 `Authorization`、`x-api-key`、`x-goog-api-key`、`Cookie` 等凭据头，替换为上游账户自己的认证。你的 API Key 只用于网关鉴权，永远不出网关。

## 视频/音频是另一套规则：provider_options 通道

**媒体端点（`/v1/videos`、`/v1/audio/speech`、`/v1/audio/jobs` 等）不透传请求体**——网关按规范化结构重建出站报文，直接在请求体里写供应商原生顶层参数是**无效**的。

想传供应商专有参数（如音色复刻参考音频、情感、音量、方言等），走唯一的 `provider_options` 通道：

```json
{
  "model": "glm-tts",
  "input": "今天天气不错",
  "voice": "default",
  "provider_options": {
    "glm": {
      "ref_audio": "https://example.com/ref.wav",
      "ref_text": "参考音频对应的文字"
    }
  }
}
```

规则细节：

| 规则 | 说明 |
| --- | --- |
| 结构 | `provider_options` 是对象，键是供应商代码（`glm`/`qwen`/`openai`…，`gpt` 是 `openai` 的别名），值是参数对象 |
| 生效范围 | 网关只取**与你这次请求实际命中的供应商**匹配的那个子对象，其余子对象整体忽略（混合分组不串参数） |
| 键域 | 开放——键名直接写厂商原生参数名，网关不校验，未知键原样到上游 |
| 优先级 | 命中的子对象**覆盖同名的规范参数**（如规范层 `speed` 与 `provider_options` 里的同名键并存时，后者赢）；嵌套对象深合并 |
| 回显 | 任务/响应里的 `provider_options_applied` 回显实际生效的键名（不含值），可用来核对 |

各供应商支持哪些键（如 GLM 声音复刻的 `ref_audio`/`ref_text`）查各供应商官方文档；网关层不设白名单。

## 流式请求的重试边界

带 `"stream": true` 的请求要知道一条规则：**网关在把流提交给你之前失败（比如上游连不上、参数被拒），会自动换其他账户重试**，你只看到变慢；**一旦流已经开始往回吐数据，后续失败不会再换账户重放**——流会以 error 帧或提前结束收尾，需要你自己在客户端重试整次请求。所以同样的请求有时第一次明显更慢，那是网关在悄悄换号重试。

## 请求体大小限制

| 车道 | 上限 | 超限表现 |
| --- | --- | --- |
| 文本对话 | 16MB | 413 `request_too_large` |
| 图像 / 音频 | 64MB | 413 |

## 怎么验证参数真的生效了

1. **媒体请求**：看响应/任务对象里的 `params_applied`（规范参数生效情况）、`provider_options_applied`（专有参数命中的键名）。
2. **文本请求**：网关不改不删，效果直接看上游回答；参数不合法时上游报错会原样透传回来——错误信息里通常带字段名。
3. **不确定某参数上游认不认**：先用最小请求带上它试一次，看报错与回答差异，再上正式调用。

## 一条重要建议

供应商特有参数与**模型（进而与供应商）绑定**：`enable_thinking` 只有 Qwen 认、`thinking` 只有 GLM 认。如果你的分组里同模型名由多家供应商账户混合承接、或故障重试会切到另一家账户，特有参数可能打到不认识它的上游。要稳定用某个供应商的特有参数，找管理员把该模型固定到对应供应商的账户/分组上。

## 接下来

- 逐家供应商的特有参数清单：《各供应商参数差异速查》
- 协议面怎么选：《协议总览：三类协议面怎么选》
- 视频/音频端点基础用法：《视频生成：从提交到取片》《语音合成 TTS》
