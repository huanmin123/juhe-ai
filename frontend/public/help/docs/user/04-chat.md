# 对话协议 chat completions

> 这一篇解决：把 POST /v1/chat/completions 的参数逐个讲清楚——messages 怎么写、多轮上下文怎么带、温度和长度怎么控、工具调用怎么入门。

## 开始之前

1. 一把可用的 API Key。认证只有一种方式：请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感）。还没有 Key 先看《API Key 与认证》。
2. 模型名以 `GET /v1/models` 实际返回为准。本篇示例统一写 `gpt-5.4`，请替换成你列表里真实存在的名字，详见《模型列表：我能用什么模型》。
3. 一个能发 HTTP 请求的工具，本篇用 `curl`。示例里的 `$BASE_URL` 代表网关地址、`$JUHE_AI_API_KEY` 代表你的 Key，先设置成环境变量再执行。

先建立一个整体认知：juhe-ai 是**无状态透传**的网关。它只解析请求里的 `model`、`stream` 等元数据，用来决定调度到哪个账户、怎么计量；`messages`、`tools` 等字段原样转交上游。所以本篇讲的参数就是 OpenAI 的通用语义，实际效果以你所用模型的支持情况为准。

## 第一步：发出最小可用请求

做什么：只带 `model` 和 `messages` 两个必填字段，问一个最简单的问题。

```bash
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      { "role": "user", "content": "用一句话介绍你自己" }
    ]
  }'
```

为什么：`messages` 就是这段对话的全部输入，网关和上游都只看它。结果长这样，回答在 `choices[0].message.content`：

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "我是一个语言模型，可以帮你回答问题、写文案、写代码。" },
      "finish_reason": "stop"
    }
  ],
  "usage": { "prompt_tokens": 18, "completion_tokens": 27, "total_tokens": 45 }
}
```

`finish_reason` 告诉你为什么停止：`stop` 是正常说完；`length` 是被 `max_tokens` 截断；`tool_calls` 是模型要求你先执行工具（见第五步）。

## 第二步：写好 messages——消息结构

`messages` 是一个数组，按时间顺序排列，每条消息由 `role` 和 `content` 组成：

| role | 谁在说话 | 典型用途 |
| --- | --- | --- |
| `system` | 你给模型下的全局指令 | 定人设、定规则，通常放第一条 |
| `user` | 用户输入 | 提问、下达任务 |
| `assistant` | 模型的历史回答 | 配合多轮上下文使用（见第三步） |
| `tool` | 工具执行结果 | 只在工具调用流程中出现（见第五步） |

## 第三步：多轮上下文——把历史原样带上

做什么：想让它"记得"前面聊过什么，就把历史消息**原样、按顺序**放进 `messages` 再发一次。

为什么：网关与上游都**不保存会话状态**。服务器不记得上一轮聊了什么，你发什么模型就看什么；所谓"多轮对话"，其实是你的代码每一轮都把完整历史重发一遍。

```bash
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      { "role": "system", "content": "你是一个简洁的翻译助手。" },
      { "role": "user", "content": "把「你好世界」翻译成英文" },
      { "role": "assistant", "content": "Hello, world." },
      { "role": "user", "content": "再翻译「再见」" }
    ]
  }'
```

两个推论：

1. 历史越长，`prompt_tokens` 越多、费用越高——与当前问题无关的旧消息应及时裁剪。
2. 维护这份历史数组是你的代码的责任：谁在哪一轮说了什么，由你自己拼接，接口不会替你记。

## 第四步：控温控长——temperature 与 max_tokens

这两个参数控制回答的"发挥程度"和"长度上限"，按 OpenAI 语义生效，模型不支持时以模型实际行为为准：

| 参数 | 类型 | 作用 | 怎么选 |
| --- | --- | --- | --- |
| `temperature` | 数字（0~2） | 越低回答越稳定、越确定；越高越发散、越有创造性 | 抽取、分类、代码用低值（如 0~0.3）；文案、头脑风暴用高值（如 0.7~1） |
| `max_tokens` | 整数 | 限制回答最多生成多少 token，防超长回答与失控费用 | 日常对话可不设；批处理、摘要等场景设一个安全上限 |

```bash
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "temperature": 0.2,
    "max_tokens": 500,
    "messages": [
      { "role": "user", "content": "从这段文字里抽出所有人名：……" }
    ]
  }'
```

设了 `max_tokens` 之后留意 `finish_reason`：出现 `length` 说明回答被截断，可提高上限，或让模型分步输出。

## 第五步：工具调用入门——tools 与 tool_choice

工具调用让模型"会查资料、会调接口"：你先声明有哪些工具可用；模型需要时**不会自己执行**，而是返回一个调用请求，由你的代码执行后把结果喂回去。完整一轮分四步：

1. **发请求时带上 `tools`**：用 JSON Schema 描述每个工具的名字、用途和参数；`tool_choice` 控制调用策略（`auto` 让模型自己决定是否调用，也可以强制指定某个工具）。
2. **模型返回 `tool_calls`**：`finish_reason` 为 `tool_calls`，`message.tool_calls` 里带工具名和参数（JSON 字符串）。
3. **你的代码执行工具**：解析参数、真的去查数据或调接口，拿到结果。
4. **把结果以 `tool` 角色消息追加进 `messages`，再发一次请求**：模型基于结果给出最终回答。

```bash
# 第 1 步：声明工具
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "messages": [ { "role": "user", "content": "北京今天多少度？" } ],
    "tools": [ {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "查询指定城市的当前天气",
        "parameters": {
          "type": "object",
          "properties": { "city": { "type": "string", "description": "城市名" } },
          "required": [ "city" ]
        }
      }
    } ],
    "tool_choice": "auto"
  }'

# 第 2 步：模型返回（示意）
# choices[0].message.tool_calls[0].function = {"name":"get_weather","arguments":"{\"city\":\"北京\"}"}

# 第 3 步：你的代码执行 get_weather("北京")，得到结果 {"temp":"23℃","weather":"晴"}

# 第 4 步：把工具结果喂回去，拿到最终回答（tools 要一并再带上）
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "messages": [
      { "role": "user", "content": "北京今天多少度？" },
      { "role": "assistant", "content": null, "tool_calls": [ { "id": "call_1", "type": "function", "function": { "name": "get_weather", "arguments": "{\"city\":\"北京\"}" } } ] },
      { "role": "tool", "tool_call_id": "call_1", "content": "{\"temp\":\"23℃\",\"weather\":\"晴\"}" }
    ],
    "tools": [ "……同第 1 步的 tools 数组……" ]
  }'
```

复杂任务就是第 2~4 步的循环：模型可以继续发 `tool_calls`，你继续执行、继续喂，直到 `finish_reason` 变成 `stop`。

## 参数速查表

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | 模型名，必须是你的 Key 绑定分组支持的模型；缺失报 503 `missing_model`，分组无账户支持报 503 `unsupported_model`（type 均为 `service_unavailable`） |
| `messages` | 数组 | 是 | 完整对话历史，`role` + `content`，顺序即语义 |
| `stream` | 布尔 | 否 | `true` 时走 SSE 流式返回，读取方法见《流式响应 SSE 怎么读》 |
| `temperature` | 数字 | 否 | 随机性 0~2，越低越稳定；以模型支持为准 |
| `max_tokens` | 整数 | 否 | 回答长度上限；以模型支持为准 |
| `tools` | 数组 | 否 | 可用工具的 JSON Schema 描述列表 |
| `tool_choice` | 字符串/对象 | 否 | 工具调用策略，`auto` 为模型自行决定，也可强制指定工具 |

表里没列的 OpenAI 通用参数同样按透传语义生效，以模型支持为准；网关没有自己的私有参数需要额外学习。

## 完整可运行示例

保存为 `chat.sh` 直接执行（先 `export BASE_URL=...` 和 `export JUHE_AI_API_KEY=sk-...`）：

```bash
#!/usr/bin/env bash
curl -s "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "temperature": 0.3,
    "max_tokens": 800,
    "messages": [
      { "role": "system", "content": "你是一个简洁的中文技术助手，回答不超过三句话。" },
      { "role": "user", "content": "什么是 SSE？" },
      { "role": "assistant", "content": "SSE 是服务器通过 HTTP 向客户端单向持续推送文本事件的技术。" },
      { "role": "user", "content": "那它和 WebSocket 有什么区别？" }
    ]
  }'
```

想看回答一段段往外蹦，在请求体里加 `"stream": true` 即可，解析方法见《流式响应 SSE 怎么读》。

## 常见问题或常见错误

所有错误都是同一个信封格式：

```json
{ "error": { "message": "...", "type": "...", "code": "..." } }
```

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 503 `missing_model`（type `service_unavailable`） | 请求体没写 `model` 字段 | 补上模型名 |
| 503 `unsupported_model`（type `service_unavailable`），message 形如「当前分组无账户支持请求模型：X」 | 模型名写错，或你的 Key 绑定的分组没有支持该模型的账户 | 用 `GET /v1/models` 核对模型名；仍不行找管理员确认分组配置 |
| 400 `invalid_request_error` | JSON 格式错、字段类型不对、messages 结构不合法 | 检查 JSON 引号转义与 role 拼写 |
| 回答"失忆"，不记得上文 | 历史消息没带进 `messages`（协议无状态） | 每轮把完整历史原样带上 |
| `finish_reason` 总是 `length` | `max_tokens` 设太小 | 提高上限或拆分任务 |
| 模型回了 `tool_calls` 但流程卡住 | 你没执行工具、没回传 `tool` 消息 | 按第五步的四步循环补全 |
| 5xx，`type` 为 `server_error` / `service_unavailable` / `upstream_error` | 网关或上游临时故障 | 原样重试，仍失败按《错误码与排障》取证 |

## 接下来

- 让回答一段段实时显示：《流式响应 SSE 怎么读》
- 客户端已用 OpenAI Responses SDK、或需要 Responses 特有能力：《Responses 协议》
- 错误信封与全部常见错误：《错误码与排障》
- 看每次调用花了多少 token、多少钱：《用量与统计》
