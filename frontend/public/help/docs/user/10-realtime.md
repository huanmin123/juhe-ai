# 实时语音 Realtime

> 这一篇解决：Realtime 双向语音会话怎么建立——先用 client_secrets 换临时令牌，再 WebSocket 直连，之后按 OpenAI Realtime 事件协议收发。

## 开始之前

1. 一把可用的 API Key。HTTP 接口的认证是请求头 `Authorization: Bearer <API Key>`（Bearer 大小写不敏感），见《API Key 与认证》。
2. 一个 Realtime 模型名，以 `GET /v1/models` 实际返回为准——不是所有模型都在 Realtime 目录里。
3. 一个支持 WebSocket 的环境：浏览器页面、Node/Python/Go 等任意语言的 WebSocket 客户端均可。
4. 认知预期：Realtime 是**双向长连接**。连上之后两边随时互发事件，不再是 HTTP 那种"一问一答"。

## 两步握手总览

| 步骤 | 做什么 | 用什么 |
| --- | --- | --- |
| 1 | 换临时令牌 | `POST /v1/realtime/client_secrets` |
| 2 | 建立 WebSocket 连接 | `GET /v1/realtime?model=...`（写成 wss 地址） |

第 1 步用你的正式 API Key 换一个**短期临时令牌**；第 2 步连 WebSocket 时认证二选一：能自定义请求头的环境直接用 API Key 的 Bearer 头，浏览器等不能自定义请求头的环境用临时令牌走 URL 参数。

## 第一步：换临时令牌 POST /v1/realtime/client_secrets

做什么：告诉网关你要用哪个 Realtime 模型，换一个临时令牌：

```bash
curl -s "$BASE_URL/v1/realtime/client_secrets" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{ "model": "gpt-5.4" }'
```

（`model` 请替换成 Realtime 目录里真实存在的模型名。）

结果长这样：

```json
{
  "value": "<临时令牌>",
  "expires_at": 1770000000
}
```

怎么读：`value` 就是临时令牌，第二步连接时用；`expires_at` 是过期时间（Unix 秒），过了这个时刻令牌作废，重新调一次本接口签发新的即可。

注意：这里的 `model` 必须是 Realtime 目录里的模型，否则报 400 `model_not_in_realtime_catalog`。

## 第二步：建立 WebSocket 连接

连接地址是 `GET /v1/realtime?model=...`，套上 ws/wss 协议就是：

```text
wss://你的网关域名/v1/realtime?model=<realtime 模型名>
```

认证二选一：

| 方式 | 怎么做 | 适合 |
| --- | --- | --- |
| 标准请求头 | WebSocket 握手时带 `Authorization: Bearer <API Key>` 头 | 服务端程序、脚本等能自定义请求头的环境 |
| URL 参数 | 地址后拼 `&token=<临时令牌>` | 浏览器页面——浏览器 WebSocket 无法自定义请求头 |

浏览器 JavaScript 连接示例：

```javascript
const url = "wss://你的网关域名/v1/realtime?model=<realtime 模型名>&token=" + token;
const ws = new WebSocket(url);
ws.onopen = () => console.log("已连接");
ws.onmessage = (event) => {
  const serverEvent = JSON.parse(event.data); // 服务端事件
  console.log(serverEvent.type);
};
```

## 第三步：按 OpenAI Realtime 事件协议收发

连接建立后，进出连接的都是 OpenAI Realtime 的事件——网关原样透传，客户端事件上行、服务端事件下行。几个最核心的事件先混个脸熟：

| 事件 | 方向 | 干什么 |
| --- | --- | --- |
| `session.update` | 客户端 → 服务端 | 配置会话（音色、指令等），通常连上后先发一条 |
| `input_audio_buffer.append` | 客户端 → 服务端 | 把本地采集的一段段音频喂给模型 |
| `response.done` | 服务端 → 客户端 | 一轮回复结束的标记 |

完整的客户端/服务端事件清单与收发节奏，直接看 OpenAI Realtime 官方文档即可——网关是透传，事件语义与官方一致。

## 参数表

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | 字符串 | 是 | Realtime 目录模型名。换令牌时放在 JSON body 里；建连时放在 `?model=` 查询参数里；两者要一致 |
| `token` | 字符串 | 视环境必填 | WebSocket URL 参数 `?token=<临时令牌>`；浏览器等无法自定义请求头的环境必须用 |
| Authorization 头 | 请求头 | 服务端环境 | 标准 `Authorization: Bearer <API Key>`，与 `token` 参数二选一 |

## 完整可运行示例

浏览器端的两步握手最小流程（示意）：

```javascript
// 1. 换临时令牌。
//    正式 API Key 不要暴露在浏览器里——推荐由你自己的后端代理这一步，
//    这里为演示直接从页面发起：
const secretResp = await fetch("/v1/realtime/client_secrets", {
  method: "POST",
  headers: {
    "Authorization": "Bearer <API Key>",
    "Content-Type": "application/json"
  },
  body: JSON.stringify({ model: "<realtime 模型名>" })
});
const { value: token, expires_at } = await secretResp.json();
// expires_at 是 Unix 秒，用之前可以先检查是否临近过期

// 2. 浏览器拿临时令牌直连 WebSocket（浏览器无法自定义请求头，走 token 参数）
const ws = new WebSocket(
  "wss://你的网关域名/v1/realtime?model=<realtime 模型名>&token=" + token
);

// 3. 连上后先配置会话，再按事件协议收发
ws.onopen = () => {
  ws.send(JSON.stringify({
    type: "session.update"
    // 会话配置字段按 OpenAI Realtime 官方文档填写
  }));
};
ws.onmessage = (event) => {
  const evt = JSON.parse(event.data);
  if (evt.type === "response.done") console.log("一轮回复结束");
};
```

预期结果：`onopen` 触发代表握手成功；之后你发送客户端事件、接收服务端事件，双向会话就跑起来了。

## 常见问题或常见错误

统一错误信封（HTTP 接口）：`{"error":{"message":"...","type":"...","code":"..."}}`

| 症状 | 原因 | 处理 |
| --- | --- | --- |
| 400 `model_not_in_realtime_catalog` | `client_secrets` 里的 model 不在 Realtime 目录 | 换 Realtime 目录里的模型名 |
| 令牌连不上，或之前能连现在不能 | 临时令牌已过期（对照 `expires_at`） | 重新调 `client_secrets` 签发新令牌 |
| WebSocket 握手被拒 | 认证没带（头和 token 都没带）或模型名不对 | 检查认证二选一与 `?model=` 参数 |
| 连上了但一直没反应 | 没有发客户端事件（如 `session.update`、音频输入） | 按 OpenAI Realtime 文档的收发节奏发事件 |
| 503 `missing_model` / `unsupported_model`（type 均为 `service_unavailable`） | 通用调度规则：没传 model 或分组不支持该模型 | 以 `GET /v1/models` 为准核对 |
| 5xx（`server_error` / `service_unavailable` / `upstream_error`） | 网关或上游临时故障 | 原样重试，反复失败按《错误码与排障》取证 |

## 接下来

- API Key 的创建、保管与认证细节：《API Key 与认证》
- 一问一答的文本对话协议：《对话协议 chat completions》
- 按症状排查错误：《错误码与排障》
