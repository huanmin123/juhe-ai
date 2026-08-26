# OpenAI 兼容 Files 与 File Search 本地运行时设计

## 1. 背景

OpenAI 到 Anthropic Messages 桥接已经覆盖 Chat / Responses 四类入口、function tools、structured output、thinking、inline 图片和部分 inline 文件输入。剩余的关键缺口是 OpenAI `file_id` 和 `file_search`：

- OpenAI 图片输入可以用 URL、data URL 或 Files API 生成的 `file_id`。
- OpenAI `file_search` 是 Responses 托管工具，依赖 Files API 和 Vector Stores。
- Anthropic Messages 不认识 OpenAI `file_id`，也没有 OpenAI Vector Store / `file_search_call` 等托管检索对象。

因此，`file_id` 和 `file_search` 不能靠字段映射解决。长期方案必须在网关侧提供 OpenAI 兼容的本地 Files / Vector Store / Retrieval 运行时，再由 Anthropic bridge 在请求转换前解析文件或执行检索。

## 2. 目标

- 客户端继续按 OpenAI 协议调用 `/v1/files`、`/v1/vector_stores`、`/v1/responses` 和 `/v1/chat/completions`。
- Chat / Responses 中的 `file_id` 在进入 Anthropic Messages 前由本地 resolver 解析为 image / document block。
- Responses `file_search` 工具在本地执行检索，结果以 OpenAI Responses 形态返回 `file_search_call`、`annotations` 和可选 `file_search_call.results`。
- 权限、授权边界、审计、错误语义和大文件处理符合当前网关规范，不把上游 OpenAI / Anthropic 私有引用混用。

## 3. 官方契约依据

- OpenAI Images and Vision：图片输入支持 URL、base64 data URL 和 Files API 创建的 `file_id`。
- OpenAI Files API：`/v1/files` 上传文件供不同 API 使用，上传请求是 `multipart/form-data`，单文件可达 512 MB。
- OpenAI File Search：`file_search` 是 Responses API 可用的托管工具，基于已上传文件、Vector Stores、语义和关键词检索。
- OpenAI Retrieval / Vector Stores：Vector Store 是语义检索索引，文件加入后会被分块、嵌入和索引；查询最多默认返回 10 条结果，可配置到 50 条。

项目内实现不需要复刻 OpenAI 后端全部能力，但必须保持客户端可见对象、错误形态和可诊断边界稳定。

## 4. 能力分层

| 能力 | 等级 | 本地承接方式 |
| --- | --- | --- |
| `/v1/files` 上传 / 列表 / 获取 / 删除 / 内容下载 | L3 | 本地文件元数据表 + 流式磁盘对象 |
| Chat `content[].type=file` + `file_id` | L3 | resolver 读取本地文件，按 MIME 转 Anthropic document |
| Responses `input_image.file_id` | L3 | resolver 读取本地图片，转 Anthropic image source |
| Responses `input_file.file_id` | L3 | resolver 读取本地 PDF / text，转 Anthropic document |
| `/v1/vector_stores` 基础 CRUD | L3 | 本地 vector store 元数据表 |
| `/v1/vector_stores/{id}/files` 绑定文件 | L3 | 本地索引任务，完成后生成 chunk 表 |
| `/v1/vector_stores/{id}/search` | L3 | 本地 keyword + 后续 embedding 混合检索 |
| Responses `tools[].type=file_search` | L3 | 预检索后注入 Anthropic system context，并渲染 OpenAI file_search 输出 |
| Office / CSV / PPTX 深度解析 | L3 | 后续按 extractor 插件补齐；未配置 extractor 时受控失败 |
| OpenAI 托管向量语义完全等价 | L4 | 不承诺；只承诺本地检索结果和协议形态稳定 |

## 5. OpenAI 兼容接口范围

### 5.1 Files API

首批实现：

- `POST /v1/files`
  - 接收 `multipart/form-data`。
  - 必填字段：`file`、`purpose`。
  - 支持 `purpose=vision`、`assistants`、`user_data`、`batch`、`fine-tune` 的元数据保存；实际可用于 bridge 的首批只启用 `vision`、`assistants`、`user_data`。
  - 返回 OpenAI `file` 对象：`id`、`object=file`、`bytes`、`created_at`、`filename`、`purpose`。
- `GET /v1/files`
  - 支持 `purpose`、`limit`、`order`、`after` 分页。
  - 返回 `object=list`、`data`、`first_id`、`last_id`、`has_more`。
- `GET /v1/files/{file_id}`
  - 返回文件元数据。
- `GET /v1/files/{file_id}/content`
  - 用流式响应下载原始内容。
- `DELETE /v1/files/{file_id}`
  - 软删除元数据并移除本地对象引用。

后续实现：

- `/v1/uploads` 大文件分片上传。
- 文件过期策略 `expires_after`。
- 文件内容引用计数和后台清理。

### 5.2 Vector Stores API

首批实现：

- `POST /v1/vector_stores`
- `GET /v1/vector_stores`
- `GET /v1/vector_stores/{vector_store_id}`
- `DELETE /v1/vector_stores/{vector_store_id}`
- `POST /v1/vector_stores/{vector_store_id}/files`
- `GET /v1/vector_stores/{vector_store_id}/files`
- `GET /v1/vector_stores/{vector_store_id}/files/{file_id}`
- `DELETE /v1/vector_stores/{vector_store_id}/files/{file_id}`
- `GET /v1/vector_stores/{vector_store_id}/files/{file_id}/content`
- `POST /v1/vector_stores/{vector_store_id}/search`

首批搜索先做本地 keyword / BM25-like 检索和 chunk 引用，接口形态对齐 OpenAI 当前 Vector Stores / Retrieval 契约。后续再引入 embedding 索引，作为可配置增强，不阻塞基础 `file_search` 协议闭环。

首批只支持可安全转文本的文本类文件，包括 `text/*`、`application/json`、`application/typescript` 和 `application/x-sh`。文本索引单文件读取上限为 2 MB，chunk 数量受控。文件加入 vector store 时先返回 `in_progress`，随后异步索引；成功后更新为 `completed`，不支持 MIME、文本过大、空文件或 chunk 超限时更新为 `failed` 并写入 `last_error`。PDF / Office / PPTX 深度解析作为 extractor 插件后续补齐；未配置 extractor 时，加入 vector store 会稳定进入失败状态，不在请求链路临时整文件解析。

## 6. 存储设计

### 6.1 文件元数据

当前表 `openai_compatible_files`：

| 字段 | 说明 |
| --- | --- |
| `id` | OpenAI 兼容 file id，例如 `file_xxx` |
| `system_account_id` | 系统账户边界 |
| `api_key_id` | 上传方 API Key 边界 |
| `container_id` | 可选容器文件归属，用于 `/v1/containers/{container_id}/files` 兼容壳 |
| `purpose` | OpenAI 文件用途 |
| `filename` | 原始文件名 |
| `media_type` | 解析后的 MIME / media type |
| `bytes` | 文件大小 |
| `sha256` | 内容哈希 |
| `storage_key` | 本地对象 key |
| `status` | 当前默认 `processed`；删除通过文件记录状态和对象清理流程处理 |
| `created_at` | 创建时间 |
| `expires_at` | 可选过期时间 |

### 6.2 文件对象

文件对象落在 `data/openai-compatible-files/` 下，按 hash 或 file id 分层：

```text
data/openai-compatible-files/
  ab/
    file_xxx.bin
```

运行路径必须流式写入和读取：

- 上传时边读边写、边统计 bytes、边计算 sha256。
- 下载时使用 `createReadStream`，不一次性读入内存。
- resolver 只有在目标上游需要 base64 时才按受控大小上限读取并编码。
- 大文件超出 Anthropic image / document 可承接限制时，本地拒绝，不尝试强行内联。

### 6.3 Vector Store

建议新增表：

- `openai_compatible_vector_stores`
- `openai_compatible_vector_store_files`
- `openai_compatible_vector_store_chunks`

chunk 字段至少包含：

| 字段 | 说明 |
| --- | --- |
| `id` | chunk id |
| `vector_store_id` | 所属 store |
| `file_id` | 来源文件 |
| `chunk_index` | 文件内顺序 |
| `content_text` | 当前 chunk 文本 |
| `content_preview` | 受限长度摘要，用于列表和审计 |
| `token_estimate` | 粗略 token 估算 |
| `keyword_index_text` | 本地检索文本 |
| `created_at` | 创建时间 |

大文本 chunk 不直接塞进请求链路缓存；检索读取按 topN 逐条读取，避免扫描和拼接全量文件。

容器文件兼容壳已覆盖 `/v1/containers/{container_id}/files` 及对应读取入口；它只复用同一文件表的 `container_id` 边界，不新增另一套文件元数据 schema。

### 6.4 Vector Store 文件生命周期

`POST /v1/vector_stores/{vector_store_id}/files` 采用轻量异步生命周期，贴近 OpenAI SDK 的轮询使用方式：

1. 校验 vector store 和 file 归属。
2. 创建或更新 `openai_compatible_vector_store_files` 为 `in_progress` 并立即返回 `vector_store.file`。
3. 同进程异步执行受控文本抽取和 chunk 索引。
4. 索引成功时写入 chunks，并将状态更新为 `completed`。
5. 索引失败时清空旧 chunks，将状态更新为 `failed`，`last_error` 记录 OpenAI 形态的 `code`、`type` 和 `message`。

首批异步执行仍在网关进程内完成，不引入分布式队列。后续如果需要跨进程恢复、重试或取消，再把 `in_progress` 文件交给 background worker，并增加游标和重试次数。客户端在状态不是 `completed` 前可以轮询文件详情或列表；`file_search` 不消费 `in_progress` 或 `failed` 文件。

## 7. 权限与边界

- 上传文件绑定当前 API Key、系统账户和分组。
- 查询 / 下载 / 删除文件时必须验证同一系统账户和 API Key 授权边界；管理端能力后续另行设计。
- `file_id` resolver 只能解析当前请求 API Key 可访问的文件。
- Vector Store 绑定同一授权边界，`file_search.vector_store_ids` 不能跨 API Key 或跨分组读取。
- 文件名、MIME、大小、hash 可入审计；文件正文默认不入普通操作日志。
- 原始审计如捕获文件相关请求，只记录 file id、filename、bytes、purpose、vector store id 和 chunk 引用，不记录完整文件内容。

## 8. Bridge 集成

### 8.1 `file_id` resolver

Anthropic bridge 在构造 Messages body 前调用 resolver：

```ts
interface OpenAICompatibleFileResolver {
  resolveFile(fileId: string, context: FileAccessContext): Promise<ResolvedFile>
}
```

`ResolvedFile` 至少包含：

- `id`
- `filename`
- `mimeType`
- `bytes`
- `createReadStream()`
- `readAsBase64(limitBytes)`
- `readText(limitBytes, encoding)`

转换规则：

- `input_image.file_id`：只接受 PNG / JPEG / WEBP / non-animated GIF，转 Anthropic image block。
- Chat / Responses 文件：PDF 转 Anthropic document base64；`text/*` 转 Anthropic document text。
- 不支持 MIME、文件过大、权限不匹配、文件已删除时返回 OpenAI 形态本地错误。

### 8.2 `file_search`

执行顺序：

1. 解析 Responses 请求中的 `file_search` tool。
2. 校验 `vector_store_ids` 权限和索引状态。
3. 从当前用户输入提取 query；`/search` 支持直接传入 `query`，后续支持模型改写 query 和多 query。
4. 调用本地检索服务返回 chunks。
5. 将检索结果作为 Anthropic system context 注入，不把 `file_search` tool 发送给 Anthropic。
6. 响应渲染时增加 `file_search_call` output item。
7. 对输出文本中的 `[F1]`、`[F2]` 等引用标记添加 `file_citation` annotations；如果 `include` 包含 `file_search_call.results`，在 `file_search_call.results` 返回受限结果摘要。

首批实现是“预检索模拟”，不是 OpenAI 托管工具的完全等价多轮自主工具循环。该边界必须写入审计 metadata。

当目标 vector store 仍有 `in_progress` 文件时，`file_search` 返回本地 `openai_anthropic_bridge_file_search_vector_store_not_ready` 错误，避免把未就绪知识库当成空结果。只有 `failed` 文件且没有任何可检索 chunk 时，返回 `openai_anthropic_bridge_file_search_vector_store_failed`，错误信息包含受限的失败 code，不包含文件正文。

## 9. 错误语义

统一错误码建议：

- `openai_compatible_file_not_found`
- `openai_compatible_file_forbidden`
- `openai_compatible_file_deleted`
- `openai_compatible_file_too_large`
- `openai_compatible_file_mime_unsupported`
- `openai_compatible_file_upload_invalid`
- `openai_compatible_vector_store_not_found`
- `openai_compatible_vector_store_forbidden`
- `openai_compatible_vector_store_not_ready`
- `openai_compatible_vector_store_file_failed`
- `openai_compatible_file_search_unavailable`

OpenAI 到 Anthropic bridge 内部继续使用 `openai_anthropic_bridge_*` 前缀描述桥接错误；Files / Vector Store 运行时错误使用 `openai_compatible_*` 前缀，便于定位是本地运行时还是上游桥接问题。

## 10. 分阶段落地

### 阶段 A：文档和接口边界

- 新增本设计文档和 `PLAN-0060`。
- 更新高兼容能力矩阵和桥接设计。
- 新增 bridge `file_id` resolver 抽象和未配置时的受控失败语义；不配置本地 Files runtime 时不改变生产运行行为。

### 阶段 B：本地 Files resolver

- 已新增 Files 元数据存储、对象流式落盘、列表 / 获取 / 删除 / 内容下载。
- 已将 resolver 接入本地 Files 存储、API Key 归属、MIME 和受控大小校验。
- bridge 层已支持 image `file_id`、PDF `file_id`、text `file_id` 转换，并已使用上传后的本地 `file_id` 覆盖 Chat document 和 Responses image mock 回归。
- 后续补真实 E2E、越权访问、超大文件和不支持 MIME 的专门边界回归。

### 阶段 C：Vector Store 和 keyword retrieval

- 已新增 Vector Store 基础接口、文件绑定、文件详情、解绑、chunk 内容查看和 `POST /search`。
- 已实现 text / markdown / json / code 类文件的受控文本抽取、chunk 和 keyword 检索首批子集。
- 已实现 Responses `file_search` 本地预检索，JSON / SSE 均返回 `file_search_call`；Chat `tools[].type=file_search` 先注入本地检索上下文，再返回 Chat message annotations。
- 本轮补齐 vector store file 轻量异步生命周期：创建先返回 `in_progress`，索引结果落 `completed` / `failed`，客户端可按 OpenAI SDK 习惯轮询。
- 后续补 PDF / Office extractor、embedding / hybrid search、跨进程索引恢复和真实上游专项 E2E。

### 阶段 D：增强检索和成本审计

- 引入 embedding provider 配置和重建任务。
- 增加 hybrid search、score threshold、attributes filter。
- 增加本地工具成本 / 延迟审计字段。
- 增加后台清理、过期和索引重建脚本。

## 11. 测试矩阵

| 类别 | 必测项 |
| --- | --- |
| Files API | 上传、列表分页、获取元数据、下载内容、删除 |
| 大文件边界 | 上传流式写入、超限拒绝、下载流式返回 |
| 权限 | 不同 API Key / 分组不能互读 file / vector store |
| Bridge file_id | Chat file、Responses input_image、Responses input_file 成功和失败 |
| Vector Store | 创建、绑定文件、索引完成、搜索、删除 |
| File Search | Responses JSON / SSE 返回 `file_search_call`、annotations、include results |
| 回归 | 现有 inline 文件、web_search、structured output、thinking 不回归 |
| 真实联调 | 真实 Anthropic 上游收到解析后的 image / document，不看到 OpenAI `file_id` |
| 安全 | 凭据扫描、文件内容不写入文档和普通日志 |

## 12. 不做范围

- 不把 OpenAI 官方 Files API 的远端 `file_id` 直接传给 Anthropic。
- 不让 Anthropic 上游访问本地文件路径。
- 不在网关请求链路全量扫描文件库或向量库。
- 不在首批实现 OpenAI 托管 File Search 的完整语义等价。
- 不引入分布式对象存储、外部向量数据库或队列作为必需依赖。

## 13. 参考资料

- [OpenAI Images and Vision](https://developers.openai.com/api/docs/guides/images-vision)
- [OpenAI File Search](https://developers.openai.com/api/docs/guides/tools-file-search)
- [OpenAI Retrieval and Vector Stores](https://developers.openai.com/api/docs/guides/retrieval)
- [OpenAI Files API](https://developers.openai.com/api/docs/api-reference/files)
