# Responses 协议

> 这一篇解决：POST /v1/responses 怎么用，以及它和 chat completions 各适合什么场景、怎么选。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 模型名以 `GET /v1/models` 实际返回与管理台模型目录为准，详见《模型列表：我能用什么模型》。
3. 建议先读过《对话协议 chat completions》——两篇的调度、计量、错误信封完全一致，本篇只讲差异。

透传逻辑先说清：网关对 `/v1/responses` 只解析 `model`、`stream` 等元数据做调度与计量，`input`、`tools` 等字段按 OpenAI Responses API 语义原样转交上游。因此参数细节以 OpenAI Responses API 文档和你所用模型的支持情况为准，网关没有自己的私有参数。

## 第一步：发出最小请求

做什么：`model` + `input` 两个字段，问一个问题。

```bash
curl -s "$BASE_URL/v1/responses" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "input": "用一句话介绍你自己"
  }'
```

与 chat completions 最直观的区别：不用组 `messages` 数组，一句 `input` 字符串就能问。

`input` 也支持传结构化的消息数组，多轮上下文与《对话协议 chat completions》同理——协议无状态，历史由你的代码拼进数组：

```bash
curl -s "$BASE_URL/v1/responses" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "input": [
      { "role": "user", "content": "我叫小聚。" },
      { "role": "assistant", "content": "你好，小聚！" },
      { "role": "user", "content": "我刚才说我叫什么？" }
    ]
  }'
```

更复杂的 `input` 形态（如多模态内容块）按 OpenAI Responses API 格式书写，以官方文档为准。

## 第二步：读响应

响应是 OpenAI Responses API 的标准格式，由上游原样返回，网关不做改写。结构示意（字段细节以 OpenAI Responses API 文档为准）：

```json
{
  "id": "resp_...",
  "object": "response",
  "status": "completed",
  "output": [ "……模型的输出内容在这里……" ]
}
```

回答内容从 `output` 里取，常用的 OpenAI 官方 SDK 一般提供直接取全文的便捷方法，不必手工遍历。具体字段结构以 OpenAI Responses API 文档为准，本篇不逐字段展开。

## 常用参数

按 OpenAI 语义讲解，实际生效以模型支持为准：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 模型名；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `input` | 字符串或数组 | 是 | 你的输入。字符串最简单；数组形态按 OpenAI Responses API 格式 |
| `instructions` | 字符串 | 否 | 系统级指令，作用类似 chat completions 里的 system 消息 |
| `temperature` | 数字 | 否 | 随机性，越低越稳定 |
| `max_output_tokens` | 整数 | 否 | 回答长度上限 |
| `stream` | 布尔 | 否 | `true` 时走 SSE 流式，读法与《流式响应 SSE 怎么读》一致 |
| `tools` | 数组 | 否 | 按 OpenAI Responses API 格式声明工具（含内置工具语义），细节以官方文档为准 |

## 与 chat completions 的请求体对照

两协议意图相同的字段，名字与位置有差异：

| 你想做的事 | chat completions | Responses |
| --- | --- | --- |
| 提问 | `messages` 数组（user 角色消息） | `input`（字符串或数组） |
| 给系统级指令 | `messages` 里的 `system` 消息 | `instructions` |
| 控制随机性 | `temperature` | `temperature` |
| 控制长度上限 | `max_tokens` | `max_output_tokens` |
| 流式输出 | `stream: true` | `stream: true` |
| 声明工具 | `tools`（function 格式） | `tools`（Responses 格式） |

## 怎么选：chat completions 还是 Responses

| 你的情况 | 选哪个 |
| --- | --- |
| 普通对话、一般集成，用的 SDK/工具只支持 chat completions | chat completions——生态最广、资料最多，大多数场景的首选 |
| 客户端已经在用 OpenAI Responses SDK，不想改代码 | Responses |
| 需要 OpenAI Responses 特有能力（如内置工具语义、stateful 的使用习惯） | Responses |
| 说不清 | 先用 chat completions，遇到具体能力缺口再切 Responses |

两者在网关侧的认证、调度、计量、错误信封完全相同，切换成本只在请求体格式。某模型是否支持 Responses 协议，以 `GET /v1/models` 与管理台模型目录为准。

## 完整可运行示例

字符串输入、带系统级指令的完整示例（保存为 `responses.sh` 执行，先 `export BASE_URL=...` 和 `export JUHE_AI_API_KEY=sk-...`）：

```bash
#!/usr/bin/env bash
curl -s "$BASE_URL/v1/responses" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "instructions": "你是一个简洁的中文技术助手，回答不超过三句话。",
    "input": "什么是无状态网关？",
    "temperature": 0.3,
    "stream": false
  }'
```

数组输入、走流式的变体：

```bash
curl -sN "$BASE_URL/v1/responses" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "instructions": "你是一个简洁的中文技术助手。",
    "input": [
      { "role": "user", "content": "什么是 SSE？" }
    ],
    "stream": true
  }'
```

流式响应同样是 `data:` 行 + `data: [DONE]` 的读法，见《流式响应 SSE 怎么读》。

## 常见问题或常见错误

统一错误信封：`{"error":{"message":"...","type":"...","code":"..."}}`

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 503 `missing_model`（type `service_unavailable`） | 没传 `model`（分组账户需按支持模型匹配） | 补上模型名 |
| 503 `unsupported_model`（type `service_unavailable`），message 形如「当前分组无账户支持请求模型：X」 | 模型名不存在，或你的 Key 绑定分组不支持 | 以 `GET /v1/models` 与管理台模型目录为准核对 |
| 400 `invalid_request_error` | 请求体不符合 Responses 格式（如 `input` 结构写错） | 对照 OpenAI Responses API 文档检查字段 |
| 不确定某模型能不能走 Responses | 各模型支持范围不同 | 查 `GET /v1/models` 与管理台模型目录；不支持就换 chat completions |
| chat completions 的请求体原样搬过来报错 | 两协议字段不通用（`messages` 对应 `input`/`instructions`，`max_tokens` 对应 `max_output_tokens`） | 按上文对照表改写字段 |
| 5xx，`type` 为 `server_error` / `service_unavailable` / `upstream_error` | 网关或上游临时故障 | 原样重试，反复失败按《错误码与排障》取证 |

## 接下来

- 大多数场景的首选协议，参数语义更详细：《对话协议 chat completions》
- 两协议通用的流式读取方法：《流式响应 SSE 怎么读》
- 确认哪些模型可用、模型名从哪来：《模型列表：我能用什么模型》
