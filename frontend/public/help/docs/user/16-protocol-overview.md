# 协议总览：三类协议面怎么选

> 这一篇解决：juhe-ai 网关对外有几类接口协议、各自给谁用、怎么选。读完这一篇，你就知道自己的客户端该接哪个入口；每个协议的详细请求格式在各自篇目展开。

## 一句话结论

**客户端用什么协议，就接网关的对应入口**：OpenAI 系 SDK 走 `/v1`，Claude 系客户端走 `/v1/messages`，Gemini 系 SDK 走 `/v1beta`。三类入口背后是同一套模型目录和同一把 API Key——网关按**请求路径**自动识别协议，不需要任何配置开关。

## 三类协议面

| 协议面 | 入口路径 | 给谁用 | 详细篇目 |
| --- | --- | --- | --- |
| OpenAI 兼容族 | `/v1/...` | OpenAI SDK、各类聊天客户端、自动化脚本 | 《对话协议 chat completions》《视频生成》《语音合成 TTS》《语音转文字》《实时语音 Realtime》《图像生成与文本嵌入》 |
| Anthropic 原生 | `/v1/messages` 族 | Claude SDK、Claude Code 等 Anthropic 系客户端 | 《Anthropic 原生协议》 |
| Gemini 原生 | `/v1beta/...` 族 | Gemini SDK、Gemini Code Assist 等 Google 系客户端 | 《Gemini 原生协议》 |

三类协议面共用：

- **同一把 API Key**：认证都是"把网关 API Key 作为凭据"，差别只在传法（OpenAI/Anthropic 用 `Authorization: Bearer` 或 `x-api-key`，Gemini 还支持 `x-goog-api-key` 头和 `?key=` 参数）。
- **同一份模型目录**：模型名都是网关目录名。OpenAI 形态的 `GET /v1/models` 和 Gemini 形态的 `GET /v1beta/models` 是同一目录的两种渲染。
- **同一套路由链路**：API Key → 策略路由 → 分组 → AI 账户 → 上游（见《核心概念：一次请求的旅程》）。

## 关于"各种供应商"

网关背后可能接入多个供应商的 AI 账户，但**这对你透明**：

1. **你只面对网关**。所有请求都发给网关地址，模型名都是网关目录名——不是某供应商的原始模型名，也不需要为不同供应商换地址。
2. **供应商差异由管理员消化**。某模型背后是哪家供应商的哪个账户、请求转发给谁、凭据怎么换，都是管理员的账户配置；你只管按目录调用。
3. **所以没有"按供应商分篇"的接入文档**。接入方式只按协议分（上表三类），不按供应商分。

## 怎么选：三个问题定入口

1. **你的客户端是 OpenAI 系 SDK 或通用聊天工具？** → 走 `/v1`。绝大多数场景的答案。从《快速开始》进入。
2. **你的客户端是 Claude Code、Claude SDK 等 Anthropic 系？** → 走 `/v1/messages`。客户端把 Base URL 指到网关即可，请求格式完全按 Anthropic 原生习惯（详见《Anthropic 原生协议》）。
3. **你的客户端是 Gemini SDK 等 Google 系？** → 走 `/v1beta`。同理，按 Gemini 原生习惯调用（详见《Gemini 原生协议》）。

不确定客户端属于哪类？看它要的配置：要 `base_url` + OpenAI 格式的是第一类；要 `ANTHROPIC_BASE_URL` 的是第二类；要 Gemini API key 配置的是第三类。

## 自定义的视频、音频协议在哪

视频生成、语音合成、语音转文字、实时语音这些能力，**没有各供应商的私有形态**——网关把它们统一成 OpenAI 兼容族里的自定义端点（`/v1/videos`、`/v1/audio/*`、`/v1/realtime`），请求响应结构与 OpenAI 习惯一致，上游供应商差异同样被网关消化。所以视频/音频不需要"某供应商协议"的专门篇目，直接看对应篇目即可。

## 错误信封也按协议来

网关返回的错误会自动跟随你请求的协议形态：

| 你请求的协议面 | 错误信封形态 |
| --- | --- |
| OpenAI 兼容族 | `{"error":{"message","type","code"}}` |
| Anthropic 原生 | `{"type":"error","error":{"type","message"}}` |
| Gemini 原生 | `{"error":{"message","status","code"}}` |

排障时按你所用的协议形态解读即可；通用排障思路见《错误码与排障》。

## 能力边界（不要假设）

- 某个模型能不能从 Anthropic/Gemini 客户端调用，取决于管理员配置的账户与其协议能力——**客户端协议能通，不代表任意模型都可用**，以 `GET /v1/models`（或 `GET /v1beta/models`）实际返回为准。
- 协议之间是否存在自动转换（例如 Anthropic 客户端调到 OpenAI 协议的账户），是管理员在账户层的配置结果；你没有配置它的地方，也不应依赖它一定存在。
- 端点能力差异（如 token 计数、嵌入等辅助端点）在各自篇目有说明，以篇目为准。

## 接下来

- 从零跑通第一个请求：《快速开始：发出你的第一个请求》
- Claude 系客户端接入细节：《Anthropic 原生协议》
- Gemini 系客户端接入细节：《Gemini 原生协议》
- 调用报错了：《错误码与排障》
