# 流式响应 SSE 怎么读

> 这一篇解决：加上 stream:true 之后服务器一行行推回来的东西是什么意思——data: 行怎么解析、[DONE] 是什么、超时和断流长什么样、该怎么处理。

## 开始之前

1. 已经会发普通的 chat completions 请求（见《对话协议 chat completions》），知道非流式回答在 `choices[0].message.content`。
2. 一把可用的 API Key（请求头 `Authorization: Bearer $JUHE_AI_API_KEY`）和一个模型名（以 `GET /v1/models` 实际返回为准）。
3. 知道 SSE 是什么即可，不用先会：SSE（Server-Sent Events）就是服务器通过一个不立即关闭的 HTTP 连接，持续往外发一行行文本。

## 第一步：发起流式请求

做什么：在请求体里加 `"stream": true`，其他不变；curl 换成 `curl -sN`：

```bash
curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "stream": true,
    "messages": [
      { "role": "user", "content": "从 1 数到 5" }
    ]
  }'
```

为什么用 `-sN`：`-s` 关掉进度条（避免刷屏），`-N` 禁用 curl 的输出缓冲——不禁用的话，增量会被攒着，屏幕半天不动，你会误以为断流了。写自己的程序时同理：**读到多少就处理多少，不要等"整个响应结束"再处理**。

## 第二步：读懂推回来的原始文本

你会看到类似下面的一串输出（示意，字段以实际返回为准）：

```text
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"1"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"、"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

规则只有三条：

| 你看到的 | 含义 |
| --- | --- |
| `data: {...一段 JSON...}` | 一段增量。解析这段 JSON，从 `choices[0].delta.content` 取出这一小段新增文字 |
| 空行 | 一条事件的分隔符。解析时按「遇到空行 = 本条事件结束」处理 |
| `data: [DONE]` | 流的结束标记。看到它就可以正常关闭连接 |

两处与非流式的差别要记牢：

1. 每段 JSON 的 `object` 是 `chat.completion.chunk`，不是 `chat.completion`。
2. 增量放在 `delta.content`，不是 `message.content`；`delta` 里只有"这一小段新增的内容"，某几段里可能根本没有 `content` 字段（比如开头声明 role 的那段、结尾带 `finish_reason` 的那段），直接跳过即可。
3. **有一种特殊的段要当失败处理**：某段 JSON 里带的不是 `choices` 而是 `error` 字段——这是网关在流中途失败时补写的失败事件（比如上游过载中断）。看到它就立即按失败处理、重试整个请求，**不要**当普通段跳过。

## 流式与非流式对照

| | 非流式 | 流式 |
| --- | --- | --- |
| 请求区别 | `"stream": false`（或缺省） | `"stream": true` |
| 响应形态 | 一个完整 JSON，一次给全 | 一行行 `data:` 事件，最后 `data: [DONE]` |
| 回答字段 | `choices[0].message.content` | `choices[0].delta.content`（逐段增量，自己拼） |
| `object` 取值 | `chat.completion` | `chat.completion.chunk` |
| 适合 | 后台批处理、简单脚本 | 聊天界面的打字机效果、更早看到第一个字 |

两种形态的请求参数与认证完全一致，只是读取方式不同；同一个接口 `POST /v1/chat/completions`，靠 `stream` 一个字段切换。

## 第三步：把增量拼成完整回答

做什么：维护一个字符串变量，每收到一段就追加 `delta.content`。用伪代码表达：

```text
full = ""
逐行读响应:
    行不是 "data: " 开头   -> 跳过
    行是 "data: [DONE]"    -> 正常结束, 关闭连接
    否则:
        chunk = JSON解析( 去掉 "data: " 前缀后的部分 )
        如果 chunk 里有 "error" 字段 -> 流中途失败, 立即重试整个请求
        piece = chunk.choices[0].delta.content    # 可能不存在
        如果 piece 存在: full = full + piece
```

`curl` 只能看原始行；在程序里用任意 HTTP 库的流式读接口（读一行、处理一行）就能实现同样效果。拼完的 `full` 就等价于非流式的 `message.content`。

## 第四步：超时与断流——失败长什么样

关键认知：**HTTP 状态码在流开始前就已确定**。流式请求只要开始正常吐 `data:` 行，状态码就是 2xx；如果之后上游出错，**状态码不会再变**，失败以流内事件表达。所以不能只看状态码判断成败，要看流是怎么结束的：

| 流的结局 | 判定 | 处理 |
| --- | --- | --- |
| 收到 `data: [DONE]` 后连接正常结束 | 成功 | 用拼好的完整回答 |
| 某段 `data:` 是合法 JSON 但带 `error` 字段（没有 `choices`） | 失败 | 流中途失败事件，**立即重试整个请求**，不要再等 `[DONE]` |
| 某段 `data:` 的 JSON 解析失败（结构异常） | 失败 | 按"本次回答未完成"处理，**重试整个请求** |
| 流在没出现 `[DONE]` 的情况下提前断掉 | 失败 | 同上，重试 |
| 连接长时间没有任何新数据 | 可能卡住 | 给读操作设超时（如 60 秒无数据即断开），断开后按失败重试 |

重试提示：流式请求失败重试，会从头重新生成回答，费用按实际产生的用量计，详见《用量与统计》。

三条实践建议：

1. 判定失败的依据是「结构异常或提前结束」，不是 HTTP 状态码——流已经开始之后状态码不会再变。
2. 断流时已收到的部分内容要不要展示给用户，由你的产品决定；但重试永远是重新发起一次完整请求，而不是"接着上次继续"。
3. 读超时只管「多久没有新数据就断开」，不要把它设成「整个回答必须在多少秒内结束」——长回答本来就需要较长的总时长。

## 参数速查表

流式没有新增参数，只是 `stream` 一个开关：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `stream` | 布尔 | 否 | 缺省 `false`，一次性返回完整 JSON；`true` 时走 SSE 流 |
| （其余参数） | - | - | 与非流式完全一致，见《对话协议 chat completions》的参数速查表 |

## 完整可运行示例

```bash
# 终端直接跑：-sN 保证看到的是实时增量，而不是攒一批再吐
curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "stream": true,
    "messages": [
      { "role": "system", "content": "你是一个简洁的助手。" },
      { "role": "user", "content": "用三句话介绍 SSE。" }
    ]
  }'
```

预期结果：终端一行行蹦出 `data: {...}`，每行 `delta.content` 带一小段文字，最后出现 `data: [DONE]` 后命令结束。

## 常见问题或常见错误

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 屏幕半天不动，最后一次性吐出一大段 | curl 没加 `-N`（被缓冲），或你的代码攒着不处理 | curl 用 `-sN`；程序逐行读逐行处理 |
| JSON 解析失败，或流没等到 `[DONE]` 就断了 | 流中途失败（上游出错），而 HTTP 状态码不会变 | 按失败处理并重试整个请求，见第四步 |
| 每段 JSON 里找不到 `message.content` | 流式增量在 `delta.content` | 换字段；注意 `delta.content` 可能缺省 |
| 明明要非流式，却看到 `data:` 行 | 请求体里残留了 `"stream": true` | 删掉或改为 `false` |
| 503 `missing_model` / `unsupported_model`（type 均为 `service_unavailable`） | 与流式无关，是请求本身的问题 | 同《对话协议 chat completions》常见错误表 |
| 5xx（`server_error` / `service_unavailable` / `upstream_error`） | 网关或上游临时故障，此时流根本没开始 | 直接重试；反复失败按《错误码与排障》取证 |

所有错误共用统一信封 `{"error":{"message":"...","type":"...","code":"..."}}`，完整清单见《错误码与排障》。

## 接下来

- 温度、长度、工具调用等参数语义：《对话协议 chat completions》
- 不想手拼 SSE、客户端已用 Responses SDK：《Responses 协议》
- 理解一次流式请求在网关里经过哪几层：《核心概念：一次请求的旅程》
