# 快速开始：发出你的第一个请求

> 这一篇解决：从零开始，5 分钟内把第一个对话请求跑通。只走最小闭环——拿 Key、选模型、发请求、看回答。每一小节末尾都告诉你下一篇把哪件事讲透。

## 开始之前，你需要什么

1. 一个 juhe-ai 账号，并且已经登录管理台。
2. 一把**可用的 API Key**（形如 `sk-` 开头的本地密钥）。还没有的话，先看《API Key 与认证》。
3. 一个 HTTP 调用工具：终端里的 `curl`，或者任意能发 HTTP 请求的工具。

## juhe-ai 是什么，一句话版

它是一个 **OpenAI 兼容的中转入口**：你把请求发给 juhe-ai，它在后台挑一个可用的 AI 账号转发给真实上游，再把回答带回来。你的客户端只需要配置一次 juhe-ai 的地址和一把本地 API Key，上游怎么换、账号怎么切，都不用你改配置。

## 第一步：拿到 Base URL 和 API Key

- **Base URL**：就是你现在浏览器里管理台的地址，比如 `https://你的域名`（本地环境则是 `http://127.0.0.1:8431` 这样的地址）。
- **API Key**：登录管理台后打开 **API Keys** 页面，创建或复制一把状态正常的 Key。Key 只在创建时完整显示一次，妥善保存。

## 第二步：确认模型名

模型名要以网关实际返回的清单为准，不要凭印象猜。调用模型列表接口：

```bash
curl -s "$BASE_URL/v1/models" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY"
```

返回里的每个 `id` 就是可以直接用的模型名：

```json
{
  "object": "list",
  "data": [
    { "id": "gpt-5.4", "object": "model", ... },
    { "id": "gemini-3.5-flash", "object": "model", ... }
  ]
}
```

挑一个对话模型（比如列表里的 `gpt-5.4`，下面示例都用它；请替换成你列表里真实存在的名字）。为什么有时候列出来却没有某个模型？《模型列表》一篇专门讲。

## 第三步：发出对话请求

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

正常情况下你会拿到一段 JSON，回答在 `choices[0].message.content` 里：

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

到这一步，最小闭环已经走通了。

## 第四步（可选）：试试流式

加上 `"stream": true`，回答会一段一段地推过来（SSE 流），适合做打字机效果：

```bash
curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.4",
    "stream": true,
    "messages": [
      { "role": "user", "content": "数到 5" }
    ]
  }'
```

流式响应是一行行的 `data: {...}`，最后以 `data: [DONE]` 结束。逐行怎么解析，见《流式响应 SSE 怎么读》。

## 接入现成客户端（IDE 插件、SDK、聊天软件）

OpenAI 兼容意味着：任何支持"自定义 OpenAI Base URL"的客户端都能直接接入，只需三样东西：

| 配置项 | 填什么 |
| --- | --- |
| API Host / Base URL | juhe-ai 的地址（有的客户端要求以 `/v1` 结尾，按客户端习惯填） |
| API Key | 你的本地 API Key |
| 模型名 | 《第二步》里模型列表返回的名字 |

## 第一次就失败？按症状对号

| 症状（HTTP 状态 + 错误信息） | 最常见原因 | 去哪看 |
| --- | --- | --- |
| 401，鉴权失败 | Key 没传对：缺 `Bearer ` 前缀、Key 被停用或过期 | 《API Key 与认证》 |
| 404，资源不存在 | 路径写错：少了 `/v1` 前缀或拼写错误 | 本篇第二步 |
| 503，`missing_model`（type `service_unavailable`） | 请求体里没有 `model` 字段 | 《对话协议》 |
| 503，`unsupported_model`（type `service_unavailable`）：当前分组无账户支持请求模型 | 模型名不存在，或你的 Key 绑定的分组没有支持该模型的账户 | 《模型列表》 |
| 429 / 排队类错误 | 分组账户池忙或额度受限 | 《错误码与排障》 |

错误的完整信封格式与全部常见错误，《错误码与排障》一篇讲全。

## 走完这一篇，接下来按需选读

- 弄懂背后发生了什么：《核心概念：一次请求的旅程》
- 把对话参数用全（多轮、温度、长度上限、工具调用）：《对话协议 chat completions》
- 让回答一段段出来：《流式响应 SSE 怎么读》
- 不止对话——画画、生视频、转语音：从《视频生成》或《语音合成 TTS》进入
