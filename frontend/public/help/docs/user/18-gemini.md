# Gemini 原生协议

> 这一篇解决：Gemini SDK、Gemini Code Assist 等 Google 系客户端怎么直连 juhe-ai 网关——端点、认证、模型名写法、流式与错误形态，全部按 Gemini 原生习惯，客户端零改造。

## 开始之前

1. 一把可用的 API Key（见《API Key 与认证》）。
2. 客户端支持"自定义 Gemini Base URL"（Google Gen AI SDK、Code Assist 类工具都支持）。
3. 模型名以网关目录为准（见《模型列表：我能用什么模型》）。

## 端点清单

| 方法 + 路径 | 用途 |
| --- | --- |
| `GET /v1beta/models` | 模型列表（Gemini 形态渲染） |
| `POST /v1beta/models/{model}:generateContent` | 对话生成（同步） |
| `POST /v1beta/models/{model}:streamGenerateContent` | 对话生成（流式） |
| `POST /v1beta/models/{model}:countTokens` | token 计数 |
| `POST /v1beta/models/{model}:embedContent` | 文本嵌入 |
| `POST /v1beta/interactions` 及其查询/取消端点 | interactions 能力族 |

**模型名写在路径里**：`{model}` 段填网关目录模型名，例如 `POST /v1beta/models/gemini-3.5-flash:generateContent`。可用模型与每个模型支持的方法（`supportedGenerationMethods`）以 `GET /v1beta/models` 返回为准。

## 认证：三种写法任选

| 写法 | 形式 | 适用 |
| --- | --- | --- |
| `x-goog-api-key` 头 | `x-goog-api-key: <网关APIKey>` | Gemini SDK 默认习惯 |
| `?key=` 查询参数 | `...:generateContent?key=<网关APIKey>` | 浏览器/不方便带头的场景 |
| `Authorization: Bearer` | 标准 Bearer 头 | 与 OpenAI 族统一的习惯 |

三种写法等价，网关都认。两点安心设计：

- `?key=` 里的 Key 在网关转发上游前会被剥掉，**不会泄露到上游供应商**。
- 流式请求网关会自动补 `alt=sse` 查询参数，客户端不用操心。

## 生成请求示例

```bash
curl -s "$BASE_URL/v1beta/models/gemini-3.5-flash:generateContent" \
  -H "x-goog-api-key: $JUHE_AI_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "contents": [
      { "role": "user", "parts": [ { "text": "用一句话介绍你自己" } ] }
    ]
  }'
```

说明：

- 请求体就是标准 Gemini `generateContent` 格式（`contents`/`parts`/`generationConfig`/`tools` 等），网关解析模型元数据做调度与计量，业务字段透传。
- 流式用 `:streamGenerateContent`，响应为 SSE 流，事件解析按 Gemini 官方流式协议处理；流中途失败时网关会在流内补 `event: error` 帧，客户端按失败处理并重试。
- `gemini-3.5-flash` 只是示例，**以你的模型列表实际返回为准**。

## 模型列表 GET /v1beta/models

返回 Gemini 形态目录：

```json
{
  "models": [
    {
      "name": "models/gemini-3.5-flash",
      "supportedGenerationMethods": ["generateContent", "streamGenerateContent", "countTokens"]
    }
  ]
}
```

`name` 去掉 `models/` 前缀就是路径里用的模型名；`supportedGenerationMethods` 告诉你这个模型能走哪些方法端点。

## 错误信封

这条协议面上的错误统一渲染为 Gemini 形态：

```json
{ "error": { "message": "…", "status": "NOT_FOUND", "code": 404 } }
```

常见 `status`：`UNAUTHENTICATED`（Key 问题）、`INVALID_ARGUMENT`（请求问题）、`PERMISSION_DENIED`、`NOT_FOUND`（路径/模型不存在）、`RESOURCE_EXHAUSTED`（限流/额度）、`UNAVAILABLE`（过载/上游不可用）。模型不可调度类错误的判断思路与 OpenAI 面一致，可对照《错误码与排障》解读。

## 接入 Gemini SDK

以 Google Gen AI SDK 为例，把服务地址与密钥指向网关即可：

```python
from google import genai

client = genai.Client(
    api_key="$JUHE_AI_API_KEY",
    http_options={"base_url": "$BASE_URL"},
)
```

注意：`base_url` 只填到网关地址为止，**不要带 `/v1beta` 后缀**——SDK 会自动在后面拼接 `/v1beta` 路径；带了反而会拼出错误地址。其他 Gemini 系工具同理：找"自定义 Base URL / API Key"两个配置项填网关值（同样不带 `/v1beta`）。模型选择以 `GET /v1beta/models` 实际返回为准。

## 常见错误

| 现象 | 最常见原因 | 去哪看 |
| --- | --- | --- |
| `UNAUTHENTICATED` | Key 没传对（三种写法核对一遍） | 《API Key 与认证》 |
| `NOT_FOUND` | 路径里模型名不存在，或方法名拼错 | `GET /v1beta/models` 核对 |
| `INVALID_ARGUMENT` 且提示模型相关 | 该模型没有可承接的账户，或方法不被支持 | 《模型列表》 |
| 流里出现 `event: error` | 上游或调度失败，流已中断 | 《错误码与排障》 |

## 能力边界

- `countTokens`、`embedContent`、`interactions` 族只在管理员配置了 Gemini 协议账户时可用，不会跨协议代答。
- 是否存在"Gemini 请求自动转换到其他协议账户"的调度，是管理员在账户层的配置结果；不要假设它一定存在，模型可用性一律以目录与实际调用为准。

## 接下来

- 三类协议面的选择：《协议总览：三类协议面怎么选》
- 报错解读：《错误码与排障》
- 模型名从哪来：《模型列表：我能用什么模型》
