# 图像生成与文本嵌入

> 这一篇解决：怎么用 /v1/images 画图、怎么用 /v1/embeddings 把文本变成向量，两个端点的最小可复制请求，以及计费口径。

## 两个端点，一句话版

| 端点 | 干什么 | 兼容形态 |
| --- | --- | --- |
| `POST /v1/images/generations` | 文生图 | OpenAI images API |
| `POST /v1/embeddings` | 把文本变成向量 | OpenAI embeddings |

两者都是 **OpenAI 兼容形态**，网关收到后透传给上游账户。以 `/v1/images` 开头的其他图像端点同样按 OpenAI images API 的语义处理，是否可用取决于上游模型的能力。请求字段按 OpenAI 语义填写即可，本文只给最小示例，不展开本项目私有参数。认证方式与对话接口完全相同：`Authorization: Bearer <API Key>`（见《API Key 与认证》）。

## 图像生成：POST /v1/images/generations

最小请求只需要 `model` 和 `prompt`：

```bash
curl -s "$BASE_URL/v1/images/generations" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "图像模型名（以 /v1/models 返回为准）",
    "prompt": "一只在窗台上看雨的橘猫，水彩风格",
    "n": 1,
    "size": "1024x1024"
  }'
```

请求字段速查（均为 OpenAI 语义）：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `model` | 是 | 图像模型名，从 `GET /v1/models` 里复制 |
| `prompt` | 是 | 画面描述，写得越具体效果越稳 |
| `n` | 否 | 生成几张，默认按 OpenAI 语义处理 |
| `size` | 否 | 画布尺寸，支持哪些取值取决于上游模型 |
| 其余字段 | 否 | 如 `response_format` 等，按 OpenAI images API 文档理解 |

响应要点：

- `data` 数组，每张图一项；图片以 `url` 或 `b64_json` 形式返回，取决于请求参数与模型支持情况。
- 拿到 `url` 就下载保存（多数链接有时效），拿到 `b64_json` 就 Base64 解码成图片文件。
- 不支持的参数取值会被上游拒绝，报错按《错误码与排障》解读。

## 文本嵌入：POST /v1/embeddings

最小请求只需要 `model` 和 `input`：

```bash
curl -s "$BASE_URL/v1/embeddings" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "嵌入模型名（以 /v1/models 返回为准）",
    "input": "juhe-ai 是一个 OpenAI 兼容的中转入口"
  }'
```

请求字段速查：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `model` | 是 | 嵌入模型名，从 `GET /v1/models` 里复制 |
| `input` | 是 | 要转向量的文本；可以是单个字符串，也可以是字符串数组（批量嵌入多段） |
| 其余字段 | 否 | 按 OpenAI embeddings 文档理解 |

响应要点：

- `data[].embedding` 是一串浮点数，就是向量；`input` 传数组时 `data` 也按条对应返回。
- `usage` 里报告本次消耗的 token 数。
- 向量本身只是数字，怎么用由你决定：存进向量库做语义搜索、算余弦相似度、做聚类或 RAG 检索都可以。

批量嵌入示例——`input` 传数组，响应的 `data` 按条对应：

```bash
curl -s "$BASE_URL/v1/embeddings" \
  -H "Authorization: Bearer $JUHE_AI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "嵌入模型名（以 /v1/models 返回为准）",
    "input": ["第一段文本", "第二段文本", "第三段文本"]
  }'
```

## 与 chat completions 的差异

同样是 OpenAI 兼容形态，关注点不太一样：

| | chat completions | images / embeddings |
| --- | --- | --- |
| 核心输入 | `messages` 对话 | `prompt` 或 `input` |
| 核心输出 | `choices[].message` | `data[].url` / `b64_json` 或 `data[].embedding` |
| 成本特点 | 单次较小 | 图像单张相对贵，批量前先算预算 |
| 报错排障 | 见对话协议篇 | 认证与错误信封完全相同，参数按本文两张速查表核对 |

## 用哪些模型

- 图像和嵌入模型**同样以 `GET /v1/models` 的返回为准**，读法见《模型列表：我能用什么模型》。
- 模型的名字、说明与**单价**以管理台 [模型目录](/__aisys__/my-models) 为权威。
- 不是所有分组都配了图像/嵌入账户：你的 Key 链路上有没有，试一次或看列表便知，链路原理见《核心概念：一次请求的旅程》。

## 计费口径

- 图像**按张或按 token 计费**（依模型而定），嵌入**按 token 计费**。
- 具体维度与单价在管理台 [模型目录](/__aisys__/my-models) 查看，本文不写具体数字。
- 要批量跑很多图？分批跑，每批之间到 [用量记录](/__aisys__/my-usage-records) 确认一下单张消耗，避免一批失败拖垮整次预算。
- 画图类请求成本通常明显高于文本对话，批量跑图前建议先小批量试一次，再到 [用量记录](/__aisys__/my-usage-records) 看看单张消耗，心里有数再放量。
- 每次调用（含失败）都会留下使用记录，对账方法见《用量与统计》。

## 报错了对号入座

| 报错 | 先看哪里 |
| --- | --- |
| 401 鉴权失败 | 《API Key 与认证》的 401 检查清单 |
| 503 `unsupported_model` | 《模型列表》：模型名是否在列表里 |
| 400 参数类（`invalid_request_error`） | 核对请求体 JSON 拼写与必填字段；`size`、`n` 等取值是否被该模型支持 |
| 429 / 额度受限 | 《错误码与排障》；画图消耗大，优先怀疑额度 |
| 5xx / 偶发失败 | 《错误码与排障》：健康、重试与升级路径 |

## 接下来

- 出错了逐个对号：《错误码与排障》
- 核对这几次调用花了多少：《用量与统计》
- 确认模型名在不在列表里：《模型列表：我能用什么模型》
