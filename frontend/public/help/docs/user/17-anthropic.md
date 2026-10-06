# Anthropic 原生协议

> 这一篇解决：Claude SDK、Claude Code 等 Anthropic 系客户端怎么直连 juhe-ai 网关——端点、认证、请求格式、流式与错误形态，全部按 Anthropic 原生习惯，客户端零改造。

## 开始之前

1. 一把可用的 API Key（见《API Key 与认证》）。
2. 客户端支持"自定义 Anthropic Base URL"（Claude Code、Anthropic SDK 都支持）。
3. 模型名以网关目录为准（见《模型列表：我能用什么模型》）。

## 端点清单

| 方法 + 路径 | 用途 |
| --- | --- |
| `POST /v1/messages` | 对话主端点（JSON 同步 + SSE 流式） |
| `POST /v1/messages/count_tokens` | 消息 token 计数 |
| `GET /v1/models` | 模型列表（Anthropic 形态渲染） |

`/v1` 前缀可省：`/messages` 与 `/v1/messages` 等价受理，客户端用哪种习惯都行。

## 认证：两种写法任选

网关把你的本地 API Key 当作凭据，两种传法等价：

```bash
# 写法一：Anthropic SDK 默认习惯（x-api-key 头）
curl -s "$BASE_URL/v1/messages" \
  -H "x-api-key: $JUHE_AI_API_KEY" \
  -H "content-type: application/json" \
  -d '{...}'

# 写法二：标准 Bearer（与 OpenAI 族一致）
curl -s "$BASE_URL/v1/messages" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "content-type: application/json" \
  -d '{...}'
```

关于 `anthropic-version` 头：可以不带。客户端显式携带时网关原样透传给上游；不带时上游方向会自动补默认版本，请求不受影响。

## 对话请求 POST /v1/messages

请求体就是标准 Anthropic Messages 格式，`model` 填网关目录模型名：

```bash
curl -s "$BASE_URL/v1/messages" \
  -H "x-api-key: $JUHE_AI_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-…",
    "max_tokens": 1024,
    "messages": [
      { "role": "user", "content": "用一句话介绍你自己" }
    ]
  }'
```

说明：

- `model` 以 `GET /v1/models` 实际返回为准（返回的是 Anthropic 形态目录，`data[].id` 即可用模型名）。
- 其余字段（`system`、`messages`、`max_tokens`、`temperature`、`tools` 等）按 Anthropic 官方语义理解；网关解析 `model` 等元数据做调度与计量，业务字段透传。
- **哪个模型可从这条端点调用，取决于管理员配置的账户协议能力**，目录里没有的模型不要硬调。

响应是标准 Anthropic Messages 形态（`content` 数组、`stop_reason`、`usage` 等）。

## 流式：stream 参数与 SSE

请求体加 `"stream": true` 后，响应为 Anthropic 形态 SSE——`event:` 行标明事件类型（`message_start`、`content_block_delta`、`message_stop` 等），`data:` 行携带 JSON 增量。增量解析按 Anthropic 官方事件协议处理。

流中途失败时，网关会在流内补一条 `event: error` 帧（错误信封见下节），HTTP 状态码不再变化——客户端读到异常中断或 error 帧时按失败处理并重试。

## 错误信封

这条协议面上的错误（含鉴权失败、模型不可调度、上游错误等）统一渲染为 Anthropic 形态：

```json
{ "type": "error", "error": { "type": "…", "message": "…" } }
```

常见 `error.type`：`authentication_error`（Key 问题）、`invalid_request_error`（请求问题）、`rate_limit_error`（限流）、`overloaded_error`（过载）、`api_error`（上游/服务错误）。模型不可调度类错误的判断思路与 OpenAI 面一致，可对照《错误码与排障》解读。

## 接入 Claude Code

Claude Code 只需把 Anthropic 服务地址与密钥指向网关：

```bash
export ANTHROPIC_BASE_URL="$BASE_URL"
export ANTHROPIC_AUTH_TOKEN="$JUHE_AI_API_KEY"
```

之后正常使用即可；模型可用性以网关模型目录与实际调用为准——Claude Code 里选不到某个模型时，先用 `GET /v1/models` 核对模型名是否存在。

## 常见错误

| 现象 | 最常见原因 | 去哪看 |
| --- | --- | --- |
| `authentication_error` | Key 没传对（`x-api-key` 与 Bearer 都试过仍失败则查 Key 状态） | 《API Key 与认证》 |
| `invalid_request_error` 且提示模型相关 | 模型名不在目录，或该模型没有可承接的账户 | 《模型列表》 |
| `not_found_error` | 路径拼写错（如少了 `/v1` 或把 `messages` 写错） | 本篇端点清单 |
| 流里出现 `event: error` | 上游或调度失败，流已中断 | 《错误码与排障》 |

## 能力边界

- `count_tokens` 端点只在管理员配置了 Anthropic 协议账户时可用，不会跨协议代答。
- 是否存在"Anthropic 请求自动转换到其他协议账户"的调度，是管理员在账户层的配置结果；不要假设它一定存在，模型可用性一律以目录与实际调用为准。

## 接下来

- 三类协议面的选择：《协议总览：三类协议面怎么选》
- 报错解读：《错误码与排障》
- 模型名从哪来：《模型列表：我能用什么模型》
