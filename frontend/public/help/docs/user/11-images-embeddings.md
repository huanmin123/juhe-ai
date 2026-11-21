# 图像生成、文本嵌入与重排

> 这一篇解决：怎么用 /v1/images 画图、怎么用 /v1/embeddings 把文本变成向量、怎么用 /v1/rerank 给候选文档打分排序，三个端点的最小可复制请求，以及计费口径。

## 三个端点，一句话版

| 端点 | 干什么 | 兼容形态 |
| --- | --- | --- |
| `POST /v1/images/generations` | 文生图 | OpenAI images API |
| `POST /v1/embeddings` | 把文本变成向量 | OpenAI embeddings |
| `POST /v1/rerank` | 按查询给文档重排打分 | Cohere / Jina rerank |

三者都按各自的行业标准形态对外；网关收到后按账户厂商适配或透传给上游。以 `/v1/images` 开头的其他图像端点同样按 OpenAI images API 的语义处理，是否可用取决于上游模型的能力。请求字段按 OpenAI 语义填写即可，本文只给最小示例，不展开本项目私有参数。认证方式与对话接口完全相同：`Authorization: Bearer <API Key>`（见《API Key 与认证》）。

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

## 文本重排：POST /v1/rerank

重排不是 OpenAI 协议，而是检索场景的行业标准形态（Cohere / Jina）：你给一个查询和一批候选文档，模型返回每篇文档的相关性分数，按分数从高到低排序。

最小请求只需要 `model`、`query` 和 `documents`：

```bash
curl -s "$BASE_URL/v1/rerank"   -H "Authorization: Bearer $JUHE_AI_API_KEY"   -H "Content-Type: application/json"   -d '{
    "model": "重排模型名（以 /v1/models 返回为准）",
    "query": "如何重置密码",
    "documents": ["重置密码的步骤…", "发票申请流程…", "账号安全设置…"],
    "top_n": 2,
    "return_documents": true
  }'
```

请求字段速查：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `model` | 是 | 重排模型名，从 `GET /v1/models` 里复制 |
| `query` | 是 | 查询文本 |
| `documents` | 是 | 候选文档数组：字符串，或 `{"text": "…"}` 对象；至少一项 |
| `top_n` | 否 | 只返回得分最高的前 N 条；不传返回全部 |
| `return_documents` | 否 | 是否在结果里回显原文，默认 `false` |

响应要点：

- `results[]` 每项含 `index`（对应原 `documents` 下标）与 `relevance_score`（相关性分数，越高越相关）。
- `return_documents=true` 时结果里带 `document`；`top_n` 生效后只保留前 N 条。
- `usage` 报告本次消耗的输入 token；重排没有输出 token。

## 与 chat completions 的差异

同样是文本类接口，关注点不太一样：

| | chat completions | images / embeddings / rerank |
| --- | --- | --- |
| 核心输入 | `messages` 对话 | `prompt`、`input` 或 `query`+`documents` |
| 核心输出 | `choices[].message` | `data[].url` / `b64_json`、`data[].embedding` 或 `results[]` |
| 成本特点 | 单次较小 | 图像单张相对贵；嵌入/重排按输入 token，批量前先算预算 |
| 报错排障 | 见对话协议篇 | 认证与错误信封完全相同，参数按本文速查表核对 |

## 用哪些模型

- 图像、嵌入和重排模型**同样以 `GET /v1/models` 的返回为准**，读法见《模型列表：我能用什么模型》。
- 模型的名字、说明与**单价**以管理台 [模型广场](/__aisys__/my-models) 为权威。
- 不是所有分组都配了图像/嵌入/重排账户：你的 Key 链路上有没有，试一次或看列表便知，链路原理见《核心概念：一次请求的旅程》。

## 计费口径

- 图像**按张计费**（分辨率档位会乘倍率），嵌入与重排**按输入 token 计费**（重排无输出 token）。
- 具体维度与单价在管理台 [模型广场](/__aisys__/my-models) 查看，本文不写具体数字。
- 画图类请求成本通常明显高于文本对话。要批量跑很多图，先小批量试一次，到 [用量记录](/__aisys__/my-usage-records) 确认单张消耗、心里有数再放量。
- 每次调用（含失败）都会留下使用记录，对账方法见《用量与统计》。

## 报错了对号入座

| 报错 | 先看哪里 |
| --- | --- |
| 401 鉴权失败 | 《API Key 与认证》的 401 检查清单 |
| 503 `unsupported_model` | 《模型列表》：模型名是否在列表里 |
| 400 参数类（`invalid_request_error`） | 核对请求体 JSON 拼写与必填字段；`size`、`n`、`query`/`documents` 等取值是否被该模型支持 |
| 429 / 额度受限 | 《错误码与排障》；画图消耗大，优先怀疑额度 |
| 5xx / 偶发失败 | 《错误码与排障》：健康、重试与升级路径 |

## 接下来

- 出错了逐个对号：《错误码与排障》
- 核对这几次调用花了多少：《用量与统计》
- 确认模型名在不在列表里：《模型列表：我能用什么模型》
