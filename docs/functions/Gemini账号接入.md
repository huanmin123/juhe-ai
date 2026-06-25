# Gemini 账号接入

## 范围

本文记录 Google Gemini 供应商的接入设计、协议档案、账户创建类型、网关透传边界、`gemini-cli` 兼容方式、模型目录、usage 统计和后续验证要求。后端和前端 mock 阶段实现已落地；后续真实联调以本文和 [Gemini 协议兼容设计](Gemini协议兼容设计.md) 为长期功能事实。

本次目标包含两个 Gemini 供应商档案：默认 Gemini 原生协议直连，以及 Gemini 官方 OpenAI Chat 兼容直连。Gemini 官方 OpenAI compatibility 已可承接 OpenAI Chat Completions 客户端，因此本项目不为 Gemini 单独实现 OpenAI Chat / Responses 到 Gemini native 的协议映射，也不把 Gemini native 伪装成 OpenAI Responses。客户端发送什么协议，网关就按对应协议档案直连对应上游：

- Gemini native 客户端：走 `gemini/v1beta` 原生协议档案。
- OpenAI Chat 客户端：走 Gemini 供应商下的 `profile_gemini_openai_chat_v1beta`，上游为 Gemini 官方 OpenAI compatibility endpoint。
- Codex / OpenAI Responses 客户端：如果要使用 Gemini，必须在 Gemini OpenAI Chat 账号上显式配置 `responses -> chat_completions` 模型映射；不允许走 Gemini native 转换，也不允许 Anthropic Messages 转到 Gemini。

本次参考：

- 本地 `gemini-cli` 源码：`F:\temp-project\agent\gemini-cli`
- Gemini CLI 版本：`@google/gemini-cli` `0.49.0-nightly.20260617.g4d3dcdce1`
- Gemini CLI SDK 依赖：`@google/genai` `1.30.0`
- 官方 Gemini API 文档：见本文末尾“官方资料”。

## 结论

- 新增供应商：`gemini`。
- 新增原生协议：`gemini/v1beta`，优先支持 Gemini API Developer API 的 REST / SSE 形态。
- 默认账户类型：Gemini API Key，底层仍为 `accounts.type = api_key`。
- 默认协议档案：`profile_gemini_native_v1beta`。
- 默认 Base URL：`https://generativelanguage.googleapis.com`。
- 默认本地调用路径支持 `/v1beta/models/{model}:generateContent`、`/v1beta/models/{model}:streamGenerateContent`、`/v1beta/models/{model}:countTokens`、`/v1beta/models/{model}:embedContent`、`/v1beta/models`。
- Files、Cached Contents、Batch / long-running operations、Live API / Interactions 等能力按 Gemini native endpoint family 预留；实现时必须逐项落到 endpoint mode，不能把未验证能力默认标为稳定。
- `gemini-cli` 验收主路径使用 `GOOGLE_GEMINI_BASE_URL` 指向本项目本地网关，并用 `GEMINI_API_KEY` 填本地 API Key；不要使用 Gemini CLI 的 Google 登录 / Code Assist 内部接口作为本次目标。

## 当前落地状态

截至 `2026-06-25`：

- 已完成设计文档、计划文档、后端 Gemini native 协议 / 供应商驱动、Gemini OpenAI Chat 兼容档案、默认种子、模型目录、usage / error / SSE 解析和 mockai 回归。
- 已完成前端 Gemini fallback provider、协议识别、endpoint mode 默认值与校验。
- 已完成 Gemini OpenAI Chat mockai 覆盖：OpenAI Chat 直连、Codex Responses 显式映射到 Chat、Anthropic Messages 映射禁用。
- 已安装本机 `gemini` CLI `0.47.0`。
- 尚未执行真实 `gemini-cli` E2E，等待用户提供真实 Gemini 账号。

## 供应商与协议档案

新增官方直连供应商：

```ts
type ProviderCode = 'gemini'
type NativeProtocolCode = 'gemini'
type NativeProtocolVersion = 'v1beta'
type OpenAIChatProtocolCode = 'openai'
type OpenAIChatProtocolVersion = 'v1'
type ProviderProtocolProfileId = 'profile_gemini_native_v1beta' | 'profile_gemini_openai_chat_v1beta'
type GeminiAccountType = 'api_key'
```

建议显示名称为 `Google Gemini`。`gemini` 是 Gemini 官方 API Key 账号池、分组、模型目录、价格目录和响应策略归属。它不能复用 `openai`、`gpt`、`anthropic`、`deepseek` 或 `glm` 的账号池、默认分组和价格目录。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_gemini_native_v1beta` | `gemini` | `gemini/v1beta` | `https://generativelanguage.googleapis.com` | Gemini API Key | `generate_content_json`、`generate_content_sse`、`count_tokens` |
| `profile_gemini_openai_chat_v1beta` | `gemini` | `openai/v1` | `https://generativelanguage.googleapis.com/v1beta/openai` | Gemini API Key | `chat_json`、`chat_sse` |

可选 endpoint modes：

| mode | Gemini 原生端点 | 当前策略 |
| --- | --- | --- |
| `generate_content_json` | `POST /v1beta/models/{model}:generateContent` | 默认启用 |
| `generate_content_sse` | `POST /v1beta/models/{model}:streamGenerateContent?alt=sse` | 默认启用 |
| `count_tokens` | `POST /v1beta/models/{model}:countTokens` | 默认启用 |
| `models` | `GET /v1beta/models`、`GET /v1beta/models/{model}` | 本地目录响应，不作为账户 endpoint mode 保存 |
| `embed_content` | `POST /v1beta/models/{model}:embedContent`、`batchEmbedContents` | 账号可选启用，先 mock 验证 |
| `files` | `/upload/v1beta/files`、`/v1beta/files/*` | 账号可选启用，必须流式处理上传 |
| `cached_contents` | `/v1beta/cachedContents` | 账号可选启用，必须保留 cache usage |
| `batch` | batch / long-running operations | 后续专项启用 |
| `live` | Live API / WebSocket | 后续专项启用，不混入 HTTP SSE |

版本策略：

- 当前已实现版本为 `v1beta`。
- 真实 `gemini-cli` 验收时显式设置 `GOOGLE_GENAI_API_VERSION=v1beta`。
- 后续如需支持 Gemini `/v1/*`，应新增或扩展 Gemini 协议档案并补 mock / 真实回归；不要把 Gemini `v1beta` 当成 OpenAI `/v1`。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 |
| --- | --- | --- | --- | --- |
| Gemini API Key | `api_key` | `profile_gemini_native_v1beta` | `api_key`、`base_url`、`supported_endpoint_modes` | `gemini-3.5-flash` |
| Gemini OpenAI Chat API Key | `api_key` | `profile_gemini_openai_chat_v1beta` | `api_key`、`base_url`、`supported_endpoint_modes`、可选 `modelMappings` | `gemini-3.5-flash` |

保存规则：

- API Key 加密保存；列表不展示，编辑弹窗可查看和修改。
- Gemini native `base_url` 默认 `https://generativelanguage.googleapis.com`；Gemini OpenAI Chat `base_url` 默认 `https://generativelanguage.googleapis.com/v1beta/openai`。二者都允许修改为同协议代理地址，但必须通过 SSRF 防护。
- Gemini native `credentials.supported_endpoint_modes` 省略时默认 `['generate_content_json', 'generate_content_sse', 'count_tokens']`；Gemini OpenAI Chat 省略时默认 `['chat_json', 'chat_sse']`。模型列表由本地目录响应，不写入账户 endpoint mode。
- 新建账户默认写入 `pending_test`，测试通过后才允许调度。
- Gemini native API Key 账户不展示 OpenAI Organization、OpenAI Project、Anthropic Version、Anthropic Beta、Codex Responses、Claude Code 客户端兼容或 GPT OAuth 字段。
- Gemini OpenAI Chat API Key 账户只保存真实上游能力 `chat_json`、`chat_sse`。如果承接 Codex / Responses，必须显式配置 `sourceEndpointFamily = responses`、`upstreamEndpointFamily = chat_completions` 的模型映射。
- 同一上游 Gemini Key 如需同时给 Gemini native 和 OpenAI-compatible 客户端使用，建议创建两个本地账户：一个走 `profile_gemini_native_v1beta`，另一个走 `profile_gemini_openai_chat_v1beta`。不要用模型映射把 Gemini native 和 OpenAI Chat 混在一个账户里。

当前不纳入目标：

- Google 个人账号 OAuth / Gemini CLI `LOGIN_WITH_GOOGLE`。
- Code Assist 内部接口 `https://cloudcode-pa.googleapis.com/v1internal:*`。
- Vertex AI Gemini。
- Google Workspace / 企业授权。
- Gemini Live API WebSocket 代理。

这些能力涉及 OAuth、Google Cloud project、内部 quota / tier、WebSocket 或 Vertex 路由语义，必须作为独立供应商协议档案或独立需求验证，不能塞进 Gemini API Key native 档案。

## 本地网关入口

Gemini native 入口使用 Google Gemini API 路径，不复用 OpenAI Chat / Responses 字段路径。

当前目标入口：

| 本地路径 | 方法 | 语义 | 上游路径 |
| --- | --- | --- | --- |
| `/v1beta/models` | `GET` | Gemini 模型列表 | 本地模型目录 |
| `/v1beta/models/{model}` | `GET` | Gemini 单模型信息 | 本地模型目录 |
| `/v1beta/models/{model}:generateContent` | `POST` | 非流式生成 | `<base_url>/v1beta/models/{model}:generateContent` |
| `/v1beta/models/{model}:streamGenerateContent` | `POST` | SSE 流式生成 | `<base_url>/v1beta/models/{model}:streamGenerateContent?alt=sse` |
| `/v1beta/models/{model}:countTokens` | `POST` | Token 计数 | `<base_url>/v1beta/models/{model}:countTokens` |
| `/v1beta/models/{model}:embedContent` | `POST` | Embedding | `<base_url>/v1beta/models/{model}:embedContent` |

本地认证：

- 优先支持 `Authorization: Bearer <本地 API Key>`。
- 同时支持 Gemini SDK 常见的 `x-goog-api-key: <本地 API Key>`。
- 兼容官方 curl 示例中的 `?key=<本地 API Key>`，但进入上游前必须移除本地 query key。
- 如果多种凭据同时存在，优先级为 `Authorization` -> `x-goog-api-key` -> query `key`，并在审计摘要记录本地认证来源。
- 上游请求必须替换为命中账户的 Gemini API Key，优先写 `x-goog-api-key`；不能把本地 API Key 或本地 query `key` 透传上游。

请求体：

- 原生 Gemini 请求体尽量 raw passthrough，支持 `contents`、`systemInstruction`、`tools`、`toolConfig`、`safetySettings`、`generationConfig`、`cachedContent`、`labels` 等字段。
- 模型来自路径 `{model}`，不是请求体 `model`。
- Gemini native 不配置账号级协议模型映射；本次不设计 OpenAI -> Gemini native 或 Gemini native -> OpenAI / Anthropic 的模型映射。
- JSON body 小请求可按现有网关解析阈值读取元数据；文件上传、二进制和大 payload 必须按 stream / 分块处理，不能完整读入内存再分页或解析。

响应体：

- JSON 响应保留 Gemini `GenerateContentResponse` 形态。
- SSE 响应按 `data: <GenerateContentResponse JSON>` 逐块透传；不要改成 OpenAI `chat.completion.chunk` 或 Responses 事件。
- 本地错误使用 Google API error shape：`{ "error": { "code": number, "message": string, "status": string, "details": [] } }`。

## gemini-cli 兼容方式

`gemini-cli` 当前关键源码观察：

- `AuthType.GATEWAY` 会在存在 `GOOGLE_GEMINI_BASE_URL` 时启用。
- `GATEWAY` 模式会把 `GEMINI_API_KEY` 当作网关 API Key 传给 `GoogleGenAI`。
- `@google/genai` client 会使用 `models.generateContent`、`models.generateContentStream`、`countTokens`、`embedContent` 等方法。
- `GOOGLE_GENAI_API_VERSION` 可覆盖 API 版本，例如 `v1beta` 或 `v1`。
- `GEMINI_API_KEY_AUTH_MECHANISM=bearer` 可让 SDK 用 `Authorization: Bearer`；默认更接近 `x-goog-api-key`。
- 非交互输出支持 `--output-format json` 和 `--output-format stream-json`，适合真实 E2E。

后续真实验证建议：

```powershell
$env:GOOGLE_GEMINI_BASE_URL = "http://127.0.0.1:3000"
$env:GEMINI_API_KEY = "<本地网关 API Key>"
$env:GOOGLE_GENAI_API_VERSION = "v1beta"
gemini -m gemini-3.5-flash -p "只回复 JUHE_GEMINI_OK" --output-format json
gemini -m gemini-3.5-flash -p "分两段输出 JUHE 和 GEMINI" --output-format stream-json
Remove-Item Env:\GOOGLE_GEMINI_BASE_URL, Env:\GEMINI_API_KEY, Env:\GOOGLE_GENAI_API_VERSION -ErrorAction SilentlyContinue
```

如果本机未安装：

```powershell
npm install -g @google/gemini-cli@latest
gemini --version
```

验收重点：

- `gemini-cli` 请求命中本地 `/v1beta/models/{model}:generateContent` 或 `:streamGenerateContent`。
- 本地 API Key 可以通过 `x-goog-api-key` 或 `Authorization` 认证。
- 上游收到的是账户 Gemini API Key，不是本地 Key。
- 流式输出能被 `gemini-cli --output-format stream-json` 正常解析。
- 工具调用、function call、图片 / 文件输入不被 OpenAI adapter 改写。
- 使用记录和审计保存 Gemini native protocol / endpoint / downstream model / upstream model / usage。

## 模型目录

Gemini 模型目录必须单独维护在 `providerCode = gemini` 下。

初始目录可从官方 Models 文档、本地 `gemini-cli` 当前模型常量和后续真实 `/v1beta/models` 核对得到。设计上优先记录这些类别：

- 文本 / 多模态生成模型：`gemini-3.5-flash`、`gemini-3.1-pro-preview`、`gemini-3.1-flash-lite`、`gemini-2.5-pro`、`gemini-2.5-flash` 等，以官方当前可见模型为准。
- Embedding 模型：`gemini-embedding-001` 等。
- Gemma 模型：如果通过 Gemini API 暴露，应仍归属 `gemini` 或后续独立 `gemma` 供应商，不能混进 GPT / OpenAI 目录。

字段要求：

- `providerCode = gemini`。
- `model` 使用官方模型 ID，保留连字符和大小写。
- 文本生成模型的 `supportedApiProtocols` 同时包含 `chat_completions`、`generate_content`、`stream_generate_content`、`count_tokens`；`chat_completions` 仅表示可作为 Gemini OpenAI Chat 档案上游，不表示 Gemini native 可接收 OpenAI 请求。
- Embedding 模型按能力填 `embed_content`。
- `contextWindowTokens`、`maxOutputTokens`、`releaseDate`、`shutdownDate` 以官方模型文档或 Models API 为准；不确定时留空，不编造。
- Gemini native 模型不进入官方 Anthropic 目录；Gemini OpenAI Chat 档案作为 `protocolCode = openai` 的直连档案，会进入 OpenAI 协议模型池以便配置 `responses -> chat_completions` 映射。
- `GET /v1beta/models` 默认由本地目录返回，不在网关热路径请求上游。

## usage 与计费

Gemini native usage 语义不能按 OpenAI `prompt_tokens/completion_tokens` 或 Anthropic cache 字段硬套。

初始字段映射建议：

| Gemini 字段 | 本地语义 |
| --- | --- |
| `usageMetadata.promptTokenCount` | 输入 token |
| `usageMetadata.candidatesTokenCount` | 输出 token |
| `usageMetadata.totalTokenCount` | 总 token |
| `usageMetadata.cachedContentTokenCount` | 缓存命中 / cached content token |
| `usageMetadata.thoughtsTokenCount` | thinking / reasoning token |
| `usageMetadata.toolUsePromptTokenCount` | 工具相关输入 token |
| `usageMetadata.promptTokensDetails[]` | 多模态输入明细 |
| `usageMetadata.candidatesTokensDetails[]` | 多模态输出明细 |

策略：

- 使用记录保存 `usage_semantic = gemini`。
- 保存 Gemini 原生 usage 摘要，保留未知 usage 字段的有界 raw 片段用于后续修正。
- 成本按 `providerCode = gemini` 和实际计价模型查找。
- 缓存、thinking、多模态 token 不要折叠成普通 input / output 后丢失来源。
- 业务统计、授权额度、趋势和 TopN 仍必须由 worker 增量聚合，不在 API 路由扫描明细。

## 分组、授权与 API Key 路由

默认创建：

- 默认 Gemini 分组：绑定 `profile_gemini_native_v1beta`。
- 默认 Gemini OpenAI Chat 分组：绑定 `profile_gemini_openai_chat_v1beta`。

规则：

- Gemini native 账户只能加入同 `provider_protocol_profile_id` 的分组。
- Gemini OpenAI Chat 账户只能加入 `profile_gemini_openai_chat_v1beta` 分组，真实能力仍是 Chat Completions。
- API Key 可以绑定 Gemini native、OpenAI、Anthropic、GLM、DeepSeek 等多个供应商档案分组。
- Gemini native 请求通过路径协议和路径模型定位 `profile_gemini_native_v1beta`，再只在当前本地 API Key 已绑定的 Gemini native 分组内调度。
- OpenAI Chat 请求不会因为模型名是 `gemini-*` 就自动进入 Gemini native 分组；如要用 Gemini 官方 OpenAI compatibility，必须进入 Gemini OpenAI Chat 分组。
- Codex / Responses 请求要用 Gemini 时，通过本地 API Key 绑定 Gemini OpenAI Chat 分组，并在账号上配置 `responses -> chat_completions` 模型映射。
- Anthropic Messages 请求不允许桥接到 Gemini OpenAI Chat；Gemini native 也不允许桥接到 OpenAI / Anthropic 上游。
- 模型不匹配、端点不支持、本地认证失败、额度不足和分组无账号都不写 Gemini 账号状态。

## 账号测试

Gemini 账户测试必须复用真实网关链路。

测试请求：

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        { "text": "请只回复 OK" }
      ]
    }
  ],
  "generationConfig": {
    "maxOutputTokens": 16
  }
}
```

测试要求：

- 测试路径使用 `/v1beta/models/{model}:generateContent`。
- 默认测试模型优先使用本地 Gemini 目录中最新可用 Flash 模型，初始可用 `gemini-3.5-flash`；如果用户账号不支持该模型，允许在表单选择其他 Gemini 模型。
- 测试请求必须走账户绑定代理。
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则。
- `pending_test` 账户测试失败继续保持待测试，测试成功才进入正常调度。
- 停用账户测试只保留诊断，不自动恢复停用状态。

## 易遗漏风险矩阵

| 风险点 | 易出错表现 | 约束 |
| --- | --- | --- |
| 协议误混 | `/v1beta/models/*:generateContent` 被 OpenAI adapter 解析 | Gemini native 必须有独立 ProtocolDriver |
| 本地 Key 泄漏 | query `key` 或 `x-goog-api-key` 原样打到上游 | 本地认证后必须替换为账户 Key |
| OpenAI 映射误扩散 | OpenAI Chat 请求自动翻译成 Gemini native | 不做 Gemini 专属协议映射；OpenAI 客户端走 Gemini OpenAI Chat 直连档案 |
| 模型来源错误 | 只从请求体 `model` 找 Gemini 模型 | Gemini native 模型来自路径 `{model}` |
| SSE 事件错误 | 把 Gemini SSE 改成 OpenAI chunk 或要求 `[DONE]` | Gemini SSE 透传 `data: JSON`，不合成 OpenAI 事件 |
| usage 口径错误 | `totalTokenCount` 又叠加 prompt / candidates 导致重复 | 按 Gemini usage semantic 单独归一 |
| Files 上传爆内存 | resumable upload 先完整读入内存 | 必须按 stream / 分块窗口处理 |
| gemini-cli 验证走错链路 | 使用 Google 登录导致请求打 Code Assist 内部接口 | 本次真实验收使用 `GOOGLE_GEMINI_BASE_URL + GEMINI_API_KEY` |
| Vertex 混入 | Vertex AI Base URL / project / location 和 Gemini API Key 账户混用 | Vertex 后续单独档案 |
| 模型目录不稳定 | 把 preview / shutdown 模型永久硬编码 | 目录按官方文档和 Models API 清洗，有 shutdown date 的到期隐藏 |

## 实施清单

- 新增 `gemini` 供应商种子。
- 新增 `protocolCode = gemini`、`protocolVersion = v1beta`。
- 新增 `profile_gemini_native_v1beta`、`profile_gemini_openai_chat_v1beta`、默认 Gemini 分组和默认 Gemini OpenAI Chat 分组。
- 新增 Gemini native ProtocolDriver：路径识别、模型提取、本地错误 shape、JSON / SSE 语义帧、Gemini 模型列表响应。
- 新增 Gemini ProviderDriver：凭据归一化、Base URL 拼接、`x-goog-api-key` 上游认证、query `key` 清理、账号测试请求。
- 前端账户创建支持 Gemini API Key、Base URL、endpoint modes，并区分 Gemini 原生和 Gemini OpenAI Chat。
- 网关本地认证支持 `Authorization`、`x-goog-api-key`、query `key`。
- 新增 Gemini native response semantic：文本、thought、functionCall、functionResponse、inlineData、fileData、finishReason、safetyRatings、promptFeedback、usageMetadata。
- 新增 Gemini 模型目录和价格目录。
- 新增 `usage_semantic = gemini`。
- Gemini OpenAI Chat 档案使用 OpenAI usage semantic 和通用 Responses-to-Chat bridge，不新增 Gemini native 互转。
- mockai 覆盖 JSON、SSE、countTokens、模型列表、Gemini error object、流内坏 chunk、usage。
- 新增真实 `gemini-cli` E2E 脚本，等待真实 Gemini 账号后执行。
- 更新导入协议、接口契约、SQLite 存储说明、模型目录清洗、模型价格和测试说明。

## 验证要求

正式落地后至少覆盖：

- 默认创建 Gemini API Key 账户后落到 `profile_gemini_native_v1beta`；显式选择 Gemini OpenAI Chat 时落到 `profile_gemini_openai_chat_v1beta`。
- 新 Gemini native 账户默认 `supported_endpoint_modes = generate_content_json/generate_content_sse/count_tokens`；新 Gemini OpenAI Chat 账户默认 `chat_json/chat_sse`。
- Gemini 账户只能加入相同供应商协议档案的分组。
- 本地 `x-goog-api-key`、`Authorization` 和 query `key` 都可作为本地 API Key 认证来源。
- 上游请求使用账户 `x-goog-api-key`，不泄漏本地 Key。
- `POST /v1beta/models/gemini-3.5-flash:generateContent` JSON 返回 Gemini 原生 response。
- `POST /v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse` 能增量转发 SSE。
- `POST /v1beta/models/gemini-3.5-flash:countTokens` 返回 Gemini token count shape。
- `GET /v1beta/models` 返回本地 Gemini 模型目录。
- Function calling、thought、inlineData、fileData、safetyRatings、promptFeedback、usageMetadata 不被 OpenAI / Anthropic 适配器破坏。
- OpenAI Chat 请求不会自动路由到 Gemini native。
- Codex / Responses 使用 Gemini 时只能显式映射到 Gemini OpenAI Chat；Anthropic Messages 不允许映射到 Gemini。
- `gemini-cli` 通过 `GOOGLE_GEMINI_BASE_URL` 真实调用本项目成功。

## 官方资料

- Gemini API 文档入口：<https://ai.google.dev/gemini-api/docs>
- Generate content API：<https://ai.google.dev/api/generate-content>
- Text generation：<https://ai.google.dev/gemini-api/docs/text-generation>
- Function calling：<https://ai.google.dev/gemini-api/docs/function-calling>
- OpenAI compatibility：<https://ai.google.dev/gemini-api/docs/openai>
- Files API：<https://ai.google.dev/gemini-api/docs/files>
- Context caching：<https://ai.google.dev/gemini-api/docs/caching>
- Embeddings：<https://ai.google.dev/gemini-api/docs/embeddings>
- Token counting：<https://ai.google.dev/gemini-api/docs/token-counting>
- Models：<https://ai.google.dev/gemini-api/docs/models>
- Live API：<https://ai.google.dev/gemini-api/docs/live>
- Gemini CLI GitHub：<https://github.com/google-gemini/gemini-cli>
