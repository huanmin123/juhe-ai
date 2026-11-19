# 对外协议面对齐 new-api 设计

> 状态：设计定稿（2026-11-19）；**2026-11-19 用户裁决「对外只留一套协议、不做兼容」后修订**：视频改为唯一面 `/v1/video/generations`、`/v1/videos*` 整套删除、扩展通道统一为 `metadata`。代码未实施，实施分期与验收见 §7，待办登记 `#T214`。
> 调研基线：`/Users/anminhu/temp-project/new-api` @ `0409deccfff2b0e8babc28ca552af44d526375bf`（2026-11-10，下称"new-api"）。文中每条 new-api 事实均给出文件路径，未给出官方文档出处的厂商报文一律按《媒体上游协议契约与Mock上游规格》§1 置信度规则标注，接入前必须回填。
> 本文是 `/v1` **对外协议面**（路径、请求体、响应体、扩展通道、错误语义）的目标契约；媒体域内部实现（IR、adapter、`media_jobs`、受理边界、零存储）仍以 [音频视频模型接入与统一媒体网关设计](音频视频模型接入与统一媒体网关设计.md) 与 [媒体上游协议契约与Mock上游规格](媒体上游协议契约与Mock上游规格.md) 为权威。两者冲突处，本文只接管"对外形态"，并在 §6.4 列出需同批修订的既有条款。

## 1. 结论摘要

1. **视频对外只保留一套协议：new-api 的统一面。** 现行 `POST /v1/videos`（OpenAI Sora 形态）**整套删除**，不保留兼容面：改为 `POST /v1/video/generations` + `GET /v1/video/generations/{task_id}`（轮询）+ `GET .../{task_id}/content`（产物），列表/取消作为同一路径族的方法扩展保留（`GET`/`DELETE /v1/video/generations[/{task_id}]`）。`/v1/videos*` 从协议门直接移除，回落 404；不保留 301/410 转发。底层 `media_jobs` 表、job id、`video_*` endpoint mode、调度与计费链路全部不变。
2. **统一面的请求体以 new-api 宿主层接受的结构为基线、再取超集**：new-api 统一面实际只接 `model/prompt/mode/image/images/size/duration/seconds/input_reference/metadata` 十个字段（§3.3），我们在此基础上额外接受 `width/height/fps/seed/n/response_format/user/audio/negative_prompt/resolution` 并放开「有图可省 prompt」（§4.2），并保留 `params_applied`/`params_ignored` 回显。
3. **new-api 的 OpenAPI 文档与其运行时不符**：文档写 `{task_id,status,url,format,metadata,error}`，实际 `GET` 返回 `{code:"success",data:{…TaskDto}}`（含 `channel_id`/`quota`/`data` 等内部字段），`POST` 返回 `{id,task_id,status,model,created_at}`。我们按**文档契约**实现（干净、不泄内部字段），偏差与理由见 §3.4。
4. **扩展通道只留一个：`metadata`。** 公开契约不再有 `provider_options`（不保留别名/双写）：视频、语音、长音频创建面统一用 `metadata`（new-api 同形），回显字段合并为 `metadata_applied`。代价与风险见 §5.1/§8（不再按 provider 命名空间隔离，靠「模型→厂商确定」与厂商自身校验兜底）。
5. **协议面收敛**：P0 视频单一面 + 扩展通道统一；P1 `/v1/rerank`（新协议，含 schema revision）；P2 图像/向量/音频规范化与厂商 adapter、`models/{id}`；其余 new-api 路径（completions/moderations/variants/tasks-plugin/MJ/Suno/tools-search）全量列入"不采纳"并给出理由（§4.6）。
6. 本次改造**不改变账户配置**：复用 `video_create` / `video_get` / `video_content` 三个既有 endpoint mode，存量视频账户零迁移；但**对外是破坏性变更**（`/v1/videos` 客户端需改路径），见 §4.3。

## 2. 现状与问题

### 2.1 我们当前的 `/v1` 对外协议面

| 路径族 | 方法 | 状态 | 实现位置 |
| --- | --- | --- | --- |
| `/v1/chat/completions`、`/v1/responses`（含 SSE） | POST | 已实施 | 协议门 `backend-go/projects/gateway/internal/gatewayopenai/endpoint.go:63` |
| `/v1/models`、`/v1beta/models` | GET | 已实施 | 同上 + gemini 原生族 |
| `/v1/messages`（Anthropic native） | POST | 已实施 | `gatewayanthropic.IsNativeRequest` |
| `/v1beta/models/*`（Gemini native） | POST | 已实施 | `gatewaygemini.IsNativeRequest` |
| `/v1/images/generations`、`/v1/images/edits` | POST | 已实施（**上游透传**，无厂商 adapter） | 协议门 + 通用 openai 族出站 |
| `/v1/embeddings`（含 ark `/v1/embeddings/multimodal` 子树） | POST | 已实施（透传） | 同上 |
| `/v1/audio/speech`、`/v1/audio/transcriptions`、`/v1/audio/translations` | POST | 已实施（adapter） | `backend-go/projects/gateway/internal/gatewaymedia/speechir.go` |
| `/v1/videos`（创建/列表/轮询/content/DELETE） | POST/GET/DELETE | 已实施（adapter）；**本设计删除** | `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_media_video.go` |
| `/v1/audio/jobs`（长音频任务族） | POST/GET/DELETE | 已实施（adapter） | 同上（kind 泛化） |
| `/v1/realtime`、`/v1/realtime/client_secrets` | GET WS / POST | 已实施 | `chain_realtime.go` |
| `/v1/files`、`/v1/containers`、`/v1/vector_stores` | * | 已实施（compat 族） | `chain_openaicompat.go` |
| `/v1/rerank`、`/v1/moderations`、`/v1/completions`、`/v1/video/generations`、`/v1/models/{id}` | — | **缺失** | 协议门不识别 → 404 `{"message":"资源不存在"}` |

### 2.2 问题定义

1. **视频对外形态与国内聚合层习惯不一致**：客户端的视频代码大量按 `POST /v1/video/generations` + `{task_id}` 轮询编写（new-api 生态），我们只给 `/v1/videos`（`{id,object:"video",…}`），对方需要改代码才能接入。**用户裁决（2026-11-19）：不保留两套协议，对外只留一套，`/v1/videos` 整套删除。**
2. **协议面零散、没有一张"对外协议总表"**：`/v1` 已支持十余条路径，但"哪些是规范面、哪些是透传、哪些是厂商私有"只散落在各设计文档里，新增协议无统一入口。
3. **非对话协议的"统一"程度不一致**：视频/音频有 adapter 归一，图像/向量是纯透传；同一模型在 OpenAI 兼容上游可用、在原生上游（如 DashScope 图像）不可用，对外却没有任何声明。
4. **缺重排（rerank）**：new-api 与主流聚合层早已提供 `/v1/rerank`，我们完全没有该协议面，GLM/百炼/方舟的重排模型无法接入。
5. **扩展通道自建、与生态不一致**：`provider_options`（按 provider 命名空间）是自研通道，new-api 生态用 `metadata`；同一能力存在两套写法，违背"对外一套"。

## 3. new-api 调研（事实）

### 3.1 协议面全清单

> 证据：`router/relay-router.go`、`router/video-router.go`、`router/task-router.go`、`router/task-plugin-protocol-router.go`、`pkg/jsplugin/routing.go`。只列对外的协议路径，管理面 `/api/*` 不在范围。

| 路径 | 方法 | new-api 行为 |
| --- | --- | --- |
| `/v1/models`、`/v1/models/{model}` | GET | 列表 / 单模型详情（按 header 切换 openai/anthropic/gemini 形态） |
| `/v1beta/models`、`/v1beta/openai/models` | GET | Gemini 形态列表 |
| `/v1/chat/completions`、`/v1/completions` | POST | OpenAI 对话 / 遗留补全 |
| `/v1/responses`、`/v1/responses/compact`、`/v1/responses`(WS) | POST/GET | Responses 协议、压缩、WebSocket |
| `/v1/messages` | POST | Anthropic native |
| `/v1beta/models/*` | POST | Gemini native（`:generateContent` 等） |
| `/v1/embeddings`、`/v1/engines/{model}/embeddings` | POST | 向量 |
| `/v1/images/generations`、`/v1/images/edits` | POST | OpenAI Images（`openai_image` host protocol） |
| `/v1/images/variations` | POST | 501 未实现 |
| `/v1/audio/speech`、`/transcriptions`、`/translations` | POST | OpenAI Audio |
| `/v1/rerank` | POST | 重排（Jina/Cohere 形态） |
| `/v1/moderations` | POST | OpenAI 审核 |
| **`/v1/video/generations`** | **POST** | **统一视频创建（本文重点）** |
| **`/v1/video/generations/{task_id}`** | **GET** | **统一视频轮询** |
| `/v1/videos`、`/v1/videos/{id}`、`/v1/videos/{id}/content` | POST/GET/HEAD | OpenAI 视频（`openai_video` host protocol） |
| `/v1/videos/{id}/remix` | POST | OpenAI 视频重混 |
| `/v1/realtime` | GET WS | 实时语音 |
| `/v1/tasks/{key}`、`/v1/tasks/{key}/artifacts[/{artifact_key}/content]` | POST/GET | 插件任务通用面（JS 插件声明路由） |
| `/v1/tools/search`、`/v1/tools/fetch` | POST | DeepSeek 搜索/抓取 |
| `/v1/alpha/search` | POST | Codex 独立搜索 |
| `/v1/files`、`/v1/vector_stores`、`/v1/fine-tunes` 等 | * | 未实现（`RelayNotImplemented`） |
| `/mj/*`、`/:mode/mj/*` | * | Midjourney 提交/查询 |

### 3.2 视频：两套面并存

| 面 | 端点 | 提交响应 | 轮询响应 | 产物 |
| --- | --- | --- | --- | --- |
| 统一面 | `POST /v1/video/generations` | `{id, task_id, status:"queued", model, created_at}`（`controller/relay.go` `presentTaskSubmission` 的 host fallback） | `{"code":"success","message":"","data":{…TaskDto}}`（`relay/relay_task.go` `videoFetchByIDRespBodyBuilder`，非 `/v1/videos/` 前缀都走这里） | 只有 `data.result_url`（上游直链，无代理） |
| OpenAI 面 | `/v1/videos`（`openai_video` host protocol，`pkg/jsplugin/routing.go`） | OpenAI video 对象（`presentTaskSubmission` 里 `Protocol=="openai_video"` 分支 → `task.ToOpenAIVideo()`） | `adaptor.ConvertToOpenAIVideo` → OpenAI video 对象 | `GET /v1/videos/{id}/content` 代理 |

路由注册（`router/video-router.go`）：统一面 `POST /v1/video/generations` 走 `PinTaskPluginEndpoint` → `PrepareTaskPluginEndpoint` → `Distribute` → `RelayTask`；`GET /v1/video/generations/:task_id` 直接 `RelayTaskFetch`；另有 `POST /v1/videos/:video_id/remix` 归 `RelayTask`。

**结论**：new-api 的 `/v1/video/generations` 不是 OpenAI 官方形态，而是 one-api 系沿革下来的聚合层统一面（`dto/video.go` 的 `VideoRequest`/`VideoResponse`/`VideoTaskResponse` 即为该面的文档化定义）；它**没有**删除 `/v1/videos`，两者按客户端选择并存。**本设计只采纳统一面一套**：new-api 的双面是历史沿革，不是我们要背的兼容包袱（2026-11-19 用户裁决）。

### 3.3 统一视频请求体：宿主层的真实词表

> 证据：`relay/common/relay_info.go`（`TaskSubmitReq`）、`relay/common/relay_utils.go`（`ValidateBasicTaskRequest` / `validatePrompt` / `storeTaskRequest`）。统一面提交体在宿主层被反序列化为固定结构并存进 `task_request`，**未列字段直接丢弃**，随后才交给插件 `buildSubmitRequest`。

| 字段 | 类型 | 校验 / 语义 |
| --- | --- | --- |
| `model` | string | 路由与计费 |
| `prompt` | string | **必填**（`validatePrompt`：空 → 400 `invalid_request`「prompt is required」） |
| `mode` | string | 厂商模式（Kling `std`/`pro`） |
| `image` | string | 单图（URL/base64 字符串） |
| `images` | string[] | 多图；`len(images)==0` 时用 `image` 回填 |
| `size` | string | `"WxH"` 像素串（插件自行严格解析） |
| `duration` | int（或数字字符串） | 与 `seconds` 二选一，0..3600（`validateTaskDurationBounds`） |
| `seconds` | **string** | 同上；数字形态会因反序列化失败报 400 |
| `input_reference` | string | 图引用（与 `image` 同义） |
| `metadata` | object（或 JSON 字符串） | 厂商参数包；插件 `Object.assign(body, metadata)` 直接并入厂商请求体 |

**注意边界**：`width`/`height`/`fps`/`seed`/`n`/`response_format`/`user`/`resolution`/`generate_audio`/`vendorBody` 在 `plugins/tasks/*/plugin.js` 里确实被读取，但来源是 **native 路由与 `openai_video`（`/v1/videos`，原始 body 直通）**，**不是** `/v1/video/generations`（后者只得到上表十个字段）。把插件内部字段当成统一面协议字段是常见误读，本文不对齐它们。

### 3.4 响应形态：文档与运行时不符（必须知道的坑）

- **文档**（`docs/openapi/relay.json` 的 `VideoRequest`/`VideoResponse`/`VideoTaskResponse`）声明：提交 `{task_id,status}`；轮询 `{task_id,status,url,format,metadata{duration,fps,width,height,seed},error{code,message}}`，状态枚举 `queued|in_progress|completed|failed`。
- **运行时**（2026-11-10 代码）：提交返回 host fallback `{id,task_id,status,model,created_at}`；轮询返回 `{code:"success",message:"",data:TaskDto}`，`TaskDto` 含 `user_id`/`channel_id`/`quota`/`properties`/`data`（上游原始快照）等内部字段，状态值是 `SUBMITTED|IN_PROGRESS|SUCCESS|FAILURE` 等任务词表。
- 该结构与实现脱节自加入起就存在：`git show d7ed0214a`（2026-06 新增 Kling 视频渠道）只添加了 `dto/video.go` 的定义、无任何 handler 引用；随后 `246d62aa5` 作为 dead code 删除，而 `docs/openapi/relay.json` 又把它写回文档。**即这份“文档契约”从未被实现过**，不能当作可依赖的运行时行为。
- **裁决**：我们实现**文档契约**（干净、与我们既有 status 词表完全一致：`queued|in_progress|completed|failed` + 本地终态 `cancelled|expired`），不复制 `{code,data}` 信封与内部字段；也**不提供** `data.*` 镜像（不做兼容）：依赖 new-api 运行时信封的客户端不在支持范围，准绳是文档契约。

### 3.5 扩展机制：new-api 的三层做法

| 机制 | 位置 | 语义 | 我们的裁决 |
| --- | --- | --- | --- |
| `metadata: {…}` | 视频/音频请求体 | 厂商个例参数**直接并入厂商报文**（可覆盖同名规范字段） | **采纳为唯一扩展通道**（`provider_options` 退役），但收紧：不得覆盖规范字段、冲突 400（§5） |
| `vendorBody: {…}` | 统一面请求体 | 整包厂商原生报文，绕过全部映射 | **不采纳**：绕过参数归一与审计，多厂商分组会串协议 |
| JS 插件协议声明 | `plugins/tasks/*/plugin.js` + `pkg/jsplugin` | 供应商以 JS 声明 `meta/routes/protocols`，宿主无需改代码即可新增原生路径、host protocol、查询与呈现 | **不采纳运行时**：我们以 Go adapter 注册表 + 模型目录 + endpoint mode 作扩展缝（§5.3） |

### 3.6 其它协议契约（new-api 事实）

**图像（`relaykit/dto/openai_image.go`）**：请求 `model/prompt/n/size/quality/response_format/style/user/background/moderation/output_format/output_compression/partial_images/stream/images/mask/input_fidelity` + 匿名字段透传（`Extra map[string]json.RawMessage`）；响应 `{created,data:[{url,b64_json,revised_prompt}],metadata?}`。

**向量（`relaykit/dto/embedding.go`）**：请求 `model/input(string|string[])/encoding_format(float|base64)/dimensions/user/seed/temperature/top_p/frequency_penalty/presence_penalty`；响应 `{object:"list",data:[{object:"embedding",index,embedding}],model,usage:{prompt_tokens,total_tokens}}`。

**重排（`relaykit/dto/rerank.go` + `relay/common_handler/rerank.go`）**：请求 `{model,query,documents[]（string|object）,top_n?,return_documents?,max_chunk_per_doc?,overlap_tokens?}`；响应 `{results:[{index,relevance_score,document?}],usage:{prompt_tokens,total_tokens}}`（非 OpenAI 上游按 Jina 形态归一）。

**音频（`relaykit/dto/audio.go`）**：`model/input/voice/instructions/response_format/speed/stream_format/metadata`（另含 vLLM-Omni 私有字段）；STT 为 multipart（`file/model/language/prompt/response_format/timestamp_granularities/temperature`）。

**错误信封（`relaykit/types/error.go` + `controller/relay.go`）**：`{"error":{"message","type","param","code"}}`，`type` 取 `openai_error|new_api_error|upstream_error|…`。

## 4. 目标协议面（裁决）

### 4.1 收敛表

> **"一套"的边界**：一条能力在一个协议族内只允许一套路径（视频：只留 new-api 统一面，删 Sora 面）。OpenAI / Anthropic / Gemini 三个**原生协议族**是不同客户端生态（`/v1/chat/completions`、`/v1/messages`、`/v1beta/models/*`），各自保留且互不替代——这不是"两套协议"，是三个生态各一条入口。

| 路径 | 我们的现状 | 裁决 | 优先级 |
| --- | --- | --- | --- |
| `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1beta/models/*` | 有 | 保持（不动） | — |
| `/v1/models` | 有 | 保持 | — |
| `/v1/models/{model}` | 无 | 采纳（列表同源的单模型投影，供 SDK 探测） | P2 |
| `/v1beta/openai/models` | 无 | 不采纳（`/v1/models` 已覆盖；仅 Gemini OpenAI 兼容前缀有需求时再加） | — |
| `/v1/images/generations`、`/v1/images/edits` | 有（透传） | 保持 + **规范化为统一图像面**（厂商 adapter 补齐、stream 语义显式化） | P2 |
| `/v1/images/variations` | 无 | 不采纳（上游几乎无对应能力） | — |
| `/v1/embeddings` | 有（透传） | 保持 + 规范声明（`encoding_format`/`dimensions`/多模态子路） | P2 |
| `/v1/engines/{model}/embeddings` | 无 | 不采纳（Gemini 遗留形态） | — |
| `/v1/audio/speech|transcriptions|translations` | 有 | 保持（已是 OpenAI 形态）；`provider_options` → `metadata`，补 `stream_format` 语义声明 | P0（通道统一）/ P2（其余） |
| `/v1/audio/jobs` | 有（自有扩展） | 保持（OpenAI 无对应，平台自有异步面） | — |
| `/v1/rerank` | 无 | **采纳（新增协议）** | P1 |
| `/v1/moderations` | 无 | 不采纳（仅 OpenAI 有；与内容安全策略重复） | — |
| `/v1/completions` | 无 | 不采纳（遗留补全；需要时按 OpenAI 族透传加法接入） | — |
| `/v1/video/generations`（`POST`/`GET`/`DELETE`）+ `/{task_id}` + `/{task_id}/content` | 无 | **唯一视频面**（new-api 统一面 + 同路径族的列表/取消/产物方法扩展） | **P0** |
| `/v1/videos` 全套 | 有 | **删除**（协议门移除，回落 404；不留兼容转发） | **P0** |
| `/v1/videos/{id}/remix` | 无 | 不采纳（随 `/v1/videos` 一并消失；低频，需要时再评估） | — |
| `/v1/realtime`、`/client_secrets` | 有 | 保持 | — |
| `/v1/tasks/*`（插件任务面） | 无 | 不采纳（我们无 JS 插件体系；`/v1/video/generations` + content 已覆盖） | — |
| `/v1/tools/search|fetch`、`/v1/alpha/search` | 无 | 不采纳（厂商私有能力，不是协议） | — |
| `/mj/*`、Suno | 无 | 不采纳（供应商不在注册表） | — |
| `/v1/files`、`/v1/vector_stores`、`/v1/containers` | 有（compat） | 保持 | — |

### 4.2 统一视频面契约（`/v1/video/generations`）

#### 4.2.1 `POST /v1/video/generations`

- 认证：网关 API Key（`Authorization: Bearer` 或 `x-api-key`，与其余 `/v1` 一致）。
- 请求：`application/json` 对象。**不接受 multipart**（视频面不再有文件上传形态）；非 JSON → 400。
- 车道与候选：`video` lane；候选账户须启用 `video_create`，模型须属 `mode=video` 目录（§6.1）。

| 字段 | 类型 | 层 | 语义与校验 | 内部映射 |
| --- | --- | --- | --- | --- |
| `model` | string | L1 | 必填；目录/映射解析同其他媒体创建面 | dispatch model |
| `prompt` | string | L1 | **无图必填**；有 `image`/`images`/`input_reference` 时可省（省则厂商侧只带图）。new-api 统一面恒要求 prompt（§3.3），我们是其超集 | `Prompt` |
| `image` | string | L2 | 首帧图，公网 URL 或 base64 data URL；网关只透传不下载 | `InputReference` |
| `images` | string[] | L2 | 参考图数组；P0 仅接受 1 个，`len>1` → 400（能力边界，不静默截断） | `InputReference`（唯一元素） |
| `input_reference` | string | L2 | `image` 的同义别名（OpenAI 词）；同时给出且值不同 → 400 | `InputReference` |
| `duration` | number | L2 | 时长（秒，>0）；与 `seconds` 同时给出且不等 → 400 | `Seconds` |
| `seconds` | number \| string | L2 | 同义别名（`"4"` 与 `4` 等价） | `Seconds` |
| `size` | string | L2 | `"WxH"` 像素串（严格两段正整数、小写 `x`）；与 `width/height` 不一致 → 400 | `Size` |
| `width`/`height` | int | L2 | 必须成对；仅当无 `size` 时参与换算成 `"WxH"` | `Size` |
| `fps` | int | L2 | ≥1；adapter 不支持 → `params_ignored` 回显 | 新增 `FPS`（capability 位） |
| `seed` | int | L2 | 随机种子；不支持 → ignored | `Seed` |
| `n` | int | L2 | ≥1，默认 1；`n>1` 且 adapter 不支持 → 400 | `N` |
| `response_format` | string | L2 | **仅 `url`**（零存储不返回内联 base64）；其它值 → 400 | — |
| `user` | string | L2 | 调用方标识，**只进审计**，不透传上游 | 新增 `User` |
| `audio` | bool | L2 | 原生音轨开关；不支持 → ignored | `Audio` |
| `negative_prompt` | string | L2 | 负向提示；不支持 → ignored | `NegativePrompt` |
| `resolution` | string | L2 | 档位（`480p`/`720p`/`1080p`…）；与 `size` 冲突 → 400；仅在无 `size` 时作为 adapter 换算输入 | 新增 `Resolution` |
| `mode` | string | L2-透传 | new-api 宿主词表字段（`std`/`pro` 等厂商模式）；无消费者的 adapter 进 `params_ignored` | 新增 `Mode`（透传 adapter extras） |
| `metadata` | object \| JSON 字符串 | L3（唯一扩展通道） | 厂商参数包（字符串形态按其 JSON 解析，行为与 new-api 一致）；**键名不得与上表规范字段同名**（400 `metadata_conflict`）；逐键并入厂商报文，未知键由厂商裁决 | 新增 `Metadata` |

规则要点：

0. **超集声明**：本表相对 new-api 统一面的十个字段是超集（多接受 `width/height/fps/seed/n/response_format/user/audio/negative_prompt/resolution`，并放开有图省 `prompt`）；new-api 客户端按其词表发请求可原样工作，我们多接受的字段对它们是加法。
1. **扩展只有 `metadata` 一条通道**：不分厂商、只做"补字段"；不再有 `provider_options`（退役，不保留别名、不做双写；旧请求携带该键按未知字段忽略）。多厂商混合分组下的参数串扰风险与缓解见 §5.1/§8。
2. 生效键名回显：`params_applied`（规范字段）、`params_ignored`（规范字段被忽略）、`metadata_applied`（metadata 实际并入的键名，取代原 `provider_options_applied`）。
3. 参数类错误一律 400 `invalid_request_error`，`message` 指明字段与原因；不降级、不猜测、不改写用户值（沿用媒体契约 §2.4）。

请求示例（文生视频）：

```json
{
  "model": "sora-2",
  "prompt": "宇航员站起身走了",
  "duration": 4,
  "size": "1280x720",
  "seed": 20231234,
  "metadata": { "camera_fixed": false }
}
```

图生视频（`prompt` 可省、用 `image`）：

```json
{
  "model": "glm-cogvideox-...",
  "image": "https://example.com/first-frame.jpg",
  "duration": 5,
  "size": "720x1280"
}
```

#### 4.2.2 提交响应（200）

```json
{
  "task_id": "video_3f9c1a2b4d5e6f708192a3b4",
  "status": "queued",
  "model": "sora-2",
  "created_at": 1763540000,
  "provider": "gpt",
  "provider_job_id": "video_abc123",
  "params_applied": ["model", "prompt", "duration", "size", "seed"],
  "params_ignored": [],
  "metadata_applied": ["camera_fixed"]
}
```

- 提交响应只有 `task_id`（new-api 文档字段）；**不再给 `id` 别名**（那是其运行时遗留字段，且原 `/v1/videos` 的 `id` 已随面删除）。
- `created_at` 为 Unix 秒。
- `provider`/`provider_job_id`/`params_*` 为既有诊断字段（媒体设计 §6 固定回显），对 new-api 客户端是**加法**，不影响解析。

#### 4.2.3 轮询响应（`GET /v1/video/generations/{task_id}`）

```json
{
  "task_id": "video_3f9c1a2b4d5e6f708192a3b4",
  "status": "completed",
  "url": "https://gateway.example.com/v1/video/generations/video_3f9c1a2b4d5e6f708192a3b4/content",
  "format": "mp4",
  "metadata": { "duration": 4.0, "fps": 24, "width": 1280, "height": 720, "seed": 20231234 },
  "model": "sora-2",
  "created_at": 1763540000,
  "completed_at": 1763540042,
  "provider": "gpt",
  "provider_job_id": "video_abc123",
  "params_applied": ["model", "prompt", "duration", "size", "seed"],
  "params_ignored": [],
  "metadata_applied": ["camera_fixed"]
}
```

- `status` 词表：`queued` / `in_progress` / `completed` / `failed`（new-api 文档词表）+ 本地终态 `cancelled` / `expired`（既有 `media_jobs` 词表，加法）。
- `url`：**只在 `completed` 出现**，恒为网关 content 端点绝对地址（§4.2.4）。不返回上游原始产物地址（沿既有"不透出上游 URL"裁决）。
- `format`：产物扩展名；无法判定时 `"mp4"`。
- `metadata`：**响应侧元数据**（与请求侧扩展包同名不同义，new-api 文档如此）；`duration` 优先取终态实测秒（`media_jobs.usage.outputVideoSeconds`），无实测回落请求生效值；`fps`/`width`/`height`/`seed` 取创建时冻结的请求快照；无值字段省略。
- `error`：仅在 `failed`/`cancelled`/`expired` 出现，`{code,message}`（脱敏后的终态摘要，沿用 `media_jobs.error`）。
- 任务不存在或非本人 → 404 `video_not_found`（不区分两种情况）。

#### 4.2.4 产物下载（`GET /v1/video/generations/{task_id}/content`）

- 账户亲和直连、边收边转、零存储（复用现有 `serveMediaJobContent`）。
- 非 `completed` → 409 `media_job_not_completed`；产物过期/上游清除 → 410 `media_artifact_expired`。
- 该端点是 new-api 的**方法扩展**（new-api 只回上游直链，无代理）；对我们必需：产物地址可能内嵌上游凭据，且零存储不缓存。仍属同一路径族，不构成第二套协议。

#### 4.2.5 列表与取消（同一路径族的方法扩展）

| 端点 | 响应 | 语义 |
| --- | --- | --- |
| `GET /v1/video/generations?limit=20` | `{"object":"list","data":[<§4.2.3 统一 job 对象>]}` | 按当前 API Key（或系统账户）过滤；`limit` 默认 20、上限 100、非法回落默认 |
| `DELETE /v1/video/generations/{task_id}` | `204` | 非终态取消；上游无取消端点则本地收敛 `cancelled`（并发终态守卫不变） |

两者复用现有 `serveMediaJobList` / `serveMediaJobCancel`，行为与错误码不变；**列表项改为统一 job 对象（`task_id` 取代 `id`、无 `object:"video"`）**，与轮询响应同形。

#### 4.2.6 唯一面（无兼容面）

- 视频只有这一套路径：`/v1/videos*` 在协议门移除，直接 404；不保留 301/410 转发、不保留旧响应字段别名（如 `seconds_length`）。
- 列表/取消/下载都在 `/v1/video/generations` 路径族内，是方法扩展而非第二套协议。
- 内部消费者（演练场）同批改派发路径，见 §4.3。

### 4.3 `/v1/videos` 删除与迁移（破坏性变更，P0 同批）

**裁决**：`/v1/videos*` 全部端点删除，协议门不再识别（回落统一 404），不做重定向/兼容层。

影响面与处置：

| 影响面 | 现状 | 处置 |
| --- | --- | --- |
| 外部客户端 | 按 `/v1/videos` 创建/轮询/下载 | 破坏性变更：需改到 `/v1/video/generations`；发布说明标注 major/breaking，给迁移示例 |
| 演练场（`internal/playground/videos.go`） | 内部派发 `POST /v1/videos`、`GET /v1/videos/{id}`、`.../content` | 同批改为 `/v1/video/generations` 族（前端 `/playground/videos` 自有管理接口不变） |
| 路由与判定 | 协议门、根形态、lane、endpoint mode、媒体映射族、形态判定均含 `/videos` | 全量替换为新路径（§6.2） |
| 文档与回归脚本 | `docs/functions/接口契约与权限矩阵.md`、媒体设计、架构总览、`frontend/src/scripts/regression/account-import-protocol-regression.ts` 等 | 同批更新（§6.4） |
| `media_jobs` / 计费 / 审计 | 与路径无关 | 零变更；存量任务行仍可按新路径轮询/下载（id 不变） |

**存量任务**：删除的是路径不是数据；已创建任务的 `video_*` id 仍可通过新路径查询与下载（`media_jobs` 按 id 定位，不依赖入站路径）。

**回滚**：代码回滚即恢复旧路径；数据无迁移，回滚安全。

### 4.4 重排协议（`POST /v1/rerank`，P1）

> 事实校正：重排**不是 OpenAI 家族协议**（OpenAI 无 rerank 端点）；它的行业事实标准是 Cohere/Jina 的 `{query,documents,top_n,return_documents}` → `{results[{index,relevance_score,document}],usage}` 形态，new-api 只是沿用它并作为聚合层默认面。因此本节按 Cohere/Jina 形态定义，而非按 OpenAI。

请求（JSON 对象）：

| 字段 | 类型 | 必填 | 语义 |
| --- | --- | --- | --- |
| `model` | string | 是 | 重排模型（目录 `mode=rerank`） |
| `query` | string | 是 | 查询文本 |
| `documents` | array | 是 | 元素为 string，或 `{"text": "…"}`；其它对象如实透传由 adapter 裁决 |
| `top_n` | int | 否 | 返回前 N 条；缺省返回全部 |
| `return_documents` | bool | 否 | 是否回显原文，默认 false |
| `max_chunk_per_doc` | int | 否 | 兼容透传（Jina 词） |
| `overlap_tokens` | int | 否 | 兼容透传（Jina 词） |

响应（200）：

```json
{
  "results": [
    { "index": 0, "relevance_score": 0.9912, "document": { "text": "…" } }
  ],
  "usage": { "prompt_tokens": 128, "total_tokens": 128 }
}
```

- `document` 仅在 `return_documents=true` 出现。
- 响应**不发明** `id`/`meta`（new-api OpenAPI 有、代码无；以代码为准）。
- 车道：`text` lane（同步 JSON、无流式、无会话亲和）；候选账户须启用 `rerank_json` endpoint mode（新增，见 §6.3），模型须属 `mode=rerank` 目录。
- 计费：按输入 token（`usage.prompt_tokens` 优先，缺失按既有文本 token 口径自算），行项沿用文本输入 token；目录新增秒价/单价前不虚计。
- 上游映射：Jina/Cohere/方舟/GLM 等 Cohere 形态可直接透传；DashScope `text-rerank` 需 `{model,input:{query,documents},parameters:{…}}` 适配（**置信度 C，接入前按官方文档回填**，见媒体契约 §1 规则）。
- 错误：OpenAI 信封；参数 400、模型不属目录 404 `model_not_found`（沿用既有未知模型语义，`gatewaydispatch/candidatefilter.go`）、候选缺失 503。

### 4.5 图像 / 向量 / 音频（规范化，P2）

**图像**（OpenAI Images 为规范面）：

- 规范请求 `prompt/model/n/size/quality/response_format/style/user/background/moderation/output_format/output_compression/partial_images/stream`；响应 `{created,data:[{url,b64_json,revised_prompt}]}`。
- 现状：**纯透传**，只有上游本身就是 OpenAI 兼容图像端点（gpt、volcengine ark、xai）可用；qwen（DashScope 多模态生成）与 glm（CogView）图像域在能力矩阵中是关闭的（`backend-go/projects/gateway/internal/accounts/accountscore/capabilityallowed.go` 的 `capabilityDomainMatrix`）。
- 缺口：①两厂商 adapter（请求/响应归一）；②`stream: true`（partial images SSE）支持评估——当前图像走非流式 JSON 管道；③`response_format=b64_json` 在零存储下的透传边界（上游返回 base64 时不落盘，直接转发）。

**向量**：

- 规范面即 OpenAI Embeddings；`input` 支持 string / string[]；`encoding_format`/`dimensions` 透传。
- `/v1/embeddings/multimodal`（ark 私有子路径）保持现状，**不算规范面**，文档标注为厂商私有。
- 缺口：Gemini 原生路径的 `:embedContent` 已在 Gemini native 面支持（`gatewayresponse/nonstreamjson.go` 的 `embed_content` 分支、`gatewaypreauth/metadata.go` 的路径词表），但没有公开 `/v1/embeddings` → Gemini 的桥接，`:batchEmbedContents` 未见支持；P2 核实后决定是否桥接或明确不支持（不默认新增跨协议转换）。

**音频**：

- 三个同步端点已是 OpenAI 形态，保持；新增声明：`stream_format`（speech SSE）当前不支持 → 400 而非静默降级（实施前按实际行为核实——现行透传链可能直接送上游）。speech 的扩展参数改用 `metadata`（按 §5 规则），`provider_options` 不再接受。
- STT 的 `response_format` 非 JSON（`text`/`srt`/`vtt`/`verbose_json`）为透传语义，转换型 adapter（gemini/minimax）不支持时 400。

### 4.6 不采纳清单（结论与理由）

| 项 | 理由 |
| --- | --- |
| `/v1/completions` | 遗留补全协议；我们九家供应商仅部分支持且已被 Chat/Responses 覆盖。按需以透传加法接入，不进入规范面 |
| `/v1/moderations` | 仅 OpenAI 有；平台侧内容安全由"安全策略"承担，重复建设 |
| `/v1/images/variations` | 上游几乎无对应能力 |
| `/v1/videos/{id}/remix` | 低频；且需引用原任务产物，零存储下语义复杂 |
| `/v1/tasks/*` 插件任务面 | 依附 JS 插件体系；我们以 `/v1/video/generations` + content 覆盖同类需求 |
| `/v1/tools/search|fetch`、`/v1/alpha/search` | 厂商私有能力（DeepSeek/Codex），不是可替换协议 |
| `/mj/*`、Suno | 供应商不在注册表（九家真实厂商），接入即违背供应商边界 |
| `/v1/engines/{model}/embeddings` | Gemini 遗留形态，无客户端依赖 |
| `vendorBody` | 绕过参数归一/审计/多厂商隔离（§5.2） |
| JS 插件运行时 | 见 §5.3 |

## 5. 扩展机制裁决

### 5.1 `metadata`（唯一扩展通道）

- 所有媒体创建端点接受：视频 `POST /v1/video/generations`、语音 `POST /v1/audio/speech`、长音频 `POST /v1/audio/jobs`；图像 adapter 落地时同规则开放。multipart 的 STT 不适用（无 JSON body）。
- 对象或 JSON 字符串两种形态（与 new-api 一致）。
- 只允许"新增键"：与规范字段同名 → 400 `invalid_request_error`（`message` 指明冲突键），**不覆盖**。这是相对 new-api「metadata 可覆盖 body」的唯一收紧，理由是静默覆盖会让 `params_applied`/审计失真。
- 逐键并入厂商报文，未知键由厂商裁决（可能被厂商 400，网关不改写、不丢弃、不重试）。
- 应用结果回显 `metadata_applied`（键名数组，不含值）；响应不再有 `provider_options_applied`。
- **`provider_options` 退役**：`gatewaymedia.ExtractProviderOptions` / `MergeProviderOptions` / `AppliedProviderOptionKeys` 的公开入口下线，内部参数合并改为「规范映射后按 `metadata` 逐键覆盖（不覆盖规范字段）」；`media_jobs.request_snapshot_json.providerOptionsApplied` 停止写入也不再读取（历史行的该键不展示，不影响任务执行与轮询）。
- 风险与缓解：`metadata` 不再按 provider 命名空间隔离，混合分组下同一模型若跨厂商可能把 A 的参数发给 B。缓解：①模型目录/映射保证同模型同厂；②adapter 只消费自己认识的键，未知键交给厂商拒绝；③`metadata_applied` 回显让用户可核对。该风险是"直接抄生态"的代价，已记入 §8。

### 5.2 不采纳 `vendorBody`

new-api 的 `vendorBody` 是给"原生路由"客户端直接搬运厂商报文的逃生门。我们不采纳，原因：①绕过 L1/L2 归一，`params_applied`/能力裁决/计费口径全部失效；②同分组多厂商时把厂商 A 的报文发给厂商 B；③审计将无法区分"用户原始意图"与"网关改写"。需要原生报文的用户应直连厂商 API。

### 5.3 不上 JS 插件运行时（与 new-api 的差异声明）

new-api 用 JS 插件把"供应商协议"变成数据（`meta/routes/protocols`），新增供应商无需改宿主代码。我们的扩展缝是 **Go adapter 注册表 + 模型目录 seed + endpoint mode + 能力矩阵**（媒体设计 §5、§9）：

| 维度 | new-api JS 插件 | 我们 Go adapter |
| --- | --- | --- |
| 新增供应商 | 上传 JS（含沙箱/热加载/版本栅栏） | 写 adapter + 目录 seed + 计费 policy + 前端常量 |
| 类型安全 | 运行时校验 | 编译期 |
| 测试 | 插件契约 + E2E | 纯函数单测 + Mock 上游 |
| 风险面 | 脚本执行、热加载、跨版本兼容 | 无 |

结论：不引入 JS 运行时（成本/风险远大于收益，且我们只服务九家固定厂商）。若未来厂商数量显著增长，再按"声明式协议描述符（数据，不是脚本）"单独立项，**不**直接搬 JS 引擎。

### 5.4 新增一个协议面的接入清单（可重复流程）

后续任何新协议（如 video 之外的音频变体、视觉理解、批处理任务）按同一条流水线接入，避免又散落到各文档：

| 步 | 动作 | 产出 / 校验 |
| --- | --- | --- |
| 1 | 对外契约先落文档（本文新增节 + 接口契约与权限矩阵新节） | 路径、方法、请求/响应、错误码、状态词表、优先级 |
| 2 | 协议门与根形态注册 | `gatewayopenai.IsProtocolRequestPath` + `chain_gateway_root.go` |
| 3 | 车道与候选门 | `RequestLane`（尽量复用 text/image/audio/video）+ endpoint mode（新增 token 时评估 schema revision） |
| 4 | 目录与计费 | catalog `mode`、价格字段与行项（按《模型价格与用量统计口径》口径） |
| 5 | 执行面 | 能透传就透传（上游同形）；需要跨厂商归一时写 IR + adapter，禁止在组合根写 switch |
| 6 | Mock 与验收 | Mock 上游场景 + 单测 + fullchain E2E；错误码与状态机逐条验收 |
| 7 | 前端与文档 | 账户能力常量、`docs/functions/README.md` 维护规则、部署影响（如有） |
| 8 | 弃用策略 | 一套协议一个面：旧面直接删除（不做重定向/兼容层），字段重命名同样不做双写；破坏性变更写进发布说明与迁移示例 |

## 6. 内部映射与实现要点

### 6.1 复用面（零新机制）

| 关注点 | 复用 |
| --- | --- |
| 状态存储 | `media_jobs`（唯一视频面读写该表，job id 前缀 `video_`；存量行不迁移） |
| 调度 | `StatelessMediaDispatch`（受理边界：2xx + job id） |
| 车道 | `gatewaymedia.RequestLaneForPath` 增 `/v1/video/generations` → `LaneVideo` |
| 候选过滤 | `video_create`（POST）/ `video_get`（GET 轮询）/ `video_content`（GET content）——**无新 endpoint mode、无 schema 变更** |
| 账户映射 | `video_generation` 族（`POST /v1/video/generations` 创建形态） |
| 参数归一 | `gatewaymedia.ParseVideoParams`（扩字段） |
| adapter | 现有 `videoAdapters` 注册表（openai/gemini/glm/xai/minimax/volcengine/qwen） |
| 响应渲染 | 统一面渲染器（把 `chainVideoJobObject` 映射为 §4.2 命名：`task_id`/`url`/`format`/`metadata`），不再有第二套渲染 |
| 审计/计费 | 链路不变（终态轮询驱动回填）；计价口径以计费域权威文档为准 |

### 6.2 代码改动清单（P0）

| 文件 | 改动 |
| --- | --- |
| `backend-go/projects/gateway/internal/gatewayopenai/endpoint.go` | `IsProtocolRequestPath`：删 `/videos`，增 `/video/generations` 族（含子路径） |
| `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_gateway_root.go` | 根形态：删 `/videos`、`/videos/{rest...}`，增 `/video`、`/video/{rest...}` |
| `backend-go/projects/gateway/internal/gatewaymedia/lane.go` | `RequestLaneForPath`：`/v1/videos` → `/v1/video/generations`（`LaneVideo`） |
| `backend-go/projects/gateway/internal/gatewaypreauth/metadata_endpointmodes.go` | 视频族前缀替换：POST → `video_create`；GET 列表/`{id}` → `video_get`；GET `{id}/content` → `video_content`；DELETE → `video_cancel` |
| `backend-go/projects/gateway/internal/gatewaydispatch/candfilters.go` | `mediaRequestEndpointFamily`：`/videos` → `/video/generations` |
| `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_routing.go` | `routingMediaEndpointFamilyOverrideOf` 同上 |
| `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_driver.go` | `requestMappingSourceFamilyOf` 同上 |
| `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_media_video.go` | 形态词 collection 改为 `/video/generations`（创建/列表/轮询/content/取消五形态）；统一面 job 对象渲染（`task_id`/`url`/`format`/`metadata`）；`url` 绝对化；删除 `/v1/videos` 分支 |
| `backend-go/projects/gateway/internal/gatewaymedia/videoparams.go` | 新增字段：`duration`/`seconds` 别名、`image`/`images`/`input_reference` 别名、`width`/`height`、`fps`、`resolution`、`mode`、`user`、`metadata`；`prompt` 有图可省；`SizeConflict`/`MetadataConflict` 校验；删 `provider_options` 入口 |
| `backend-go/projects/gateway/internal/gatewaymedia/provideroptions.go` | 退役公开入口（删除或改为 `metadata` 合并实现，不保留旧字段解析） |
| `backend-go/projects/gateway/internal/gatewaymedia/jobsrepo.go` | `MediaJobRequestSnapshot`：`ProviderOptionsApplied` → `MetadataApplied`（JSON 列，无 schema 迁移；旧键不再读取） |
| `backend-go/projects/gateway/internal/gatewaymedia/speechir.go` + `chain_media_video.go`（audio job 创建计划） | `provider_options` → `metadata`（公开字段改名，adapter 入参不变） |
| `backend-go/projects/gateway/internal/playground/videos.go` | 内部派发路径 `/v1/videos*` → `/v1/video/generations*`（含 poll/content）；创建响应解析 `id` → `task_id` |
| `backend-go/projects/gateway/internal/playground/media_test.go` | 断言路径同步 |
| `frontend/src/scripts/regression/account-import-protocol-regression.ts` | API 面清单 `/videos` → `/video/generations`（不新增 endpoint mode 常量） |

测试面（P0）：`gatewaymedia/videoparams_test.go`（新字段与冲突矩阵）、`chain_media_video_test.go`（形态判定、创建、轮询、content、错误码）、`gatewaypreauth/metadata_endpointmodes_test.go`（mode 映射）、`gatewayopenai` 协议门测试、Mock 上游 `media_video_*` 场景复用、fullchain E2E（`acceptance/fullchain_media_video_test.go`）加统一面用例。

### 6.3 目录 / 计费 / endpoint mode（rerank，P1）

| 项 | 改动 |
| --- | --- |
| endpoint mode | 新增 `rerank_json`：`providers/endpoint_modes.go`、`accounts/endpoint_modes.go`（+ `accountscore`）、`gatewaypreauth/metadata_endpointmodes.go`、`frontend/src/views/accounts/accountProviderCapabilities.ts` |
| schema | `accounts.health_check_endpoint_mode` 与 `account_endpoint_mode_declarations.endpoint_mode` 的 CHECK 约束需追加 `rerank_json` → **business schema revision +1**（`backend-go/projects/maintenance/internal/schema/pg_schema_business_tables.go`），随 `maintenance upgrade` 发布 |
| 目录 | `ProviderCatalogModeValues` 增 `rerank`（前端不新增 Tab，按 `embedding` 同口径处理）；rerank 模型 seed（glm/qwen/volcengine）与价格来源按《模型价格数据源与取证指南》留档 |
| 计费 | 输入 token 行项（复用文本口径），无输出 token；usage 缺失不虚计 |
| 能力矩阵 | 对应 vendor 行开 rerank（仅在其上游确有 rerank API 时） |

### 6.4 与现行权威文档的冲突与修订（同批必须完成）

| 现行条款 | 冲突 | 本设计裁决 |
| --- | --- | --- |
| [音频视频模型接入与统一媒体网关设计](音频视频模型接入与统一媒体网关设计.md) §4.2「视频采用 OpenAI Sora 形态（生态兼容最大）」 | 与「只留一套」冲突 | 改为「唯一视频面 `/v1/video/generations`（new-api 统一面）；`/v1/videos` 已删除」，并写明破坏性变更 |
| [媒体上游协议契约与Mock上游规格](媒体上游协议契约与Mock上游规格.md) §2.1「只开放 `provider_options` 这一个通道，不引入 `extra_body` 等第二通道」 | 通道改为生态的 `metadata` | 改为「唯一扩展通道 `metadata`（不得覆盖规范字段）；`provider_options` 退役」 |
| 同文 §2.2 视频创建参数表（`POST /v1/videos`，`prompt` L1 必填） | 路径与 `prompt` 条件必填 | 参数表改为 `POST /v1/video/generations`；`prompt` 改为「无图必填」；新增字段与冲突规则 |
| 同文 §4.3 创建响应 video 对象（含 `seconds_length`） | 字段名与唯一面 | 改为 §4.2 的统一面 job 对象（`task_id`/`status`/`url`/`format`/`metadata`/`error`），删除 `seconds_length` |
| [接口契约与权限矩阵](接口契约与权限矩阵.md)「异步视频任务端点」章 | 只描述了 `/v1/videos` | 整章替换为「统一视频端点」；新增「重排端点」小节 |
| [架构总览](../architecture/架构总览.md) §媒体异步任务链 | 同上 | 路径与端点集合同批替换 |
| [演练场设计](演练场设计.md) §视频 | 内部派发 `/v1/videos` | 改为 `/v1/video/generations`（前端管理接口不变） |
| `frontend/src/scripts/regression/account-import-protocol-regression.ts` | API 面清单含 `/videos` | 改为 `/video/generations` 族 |
| [模型价格与用量统计口径](模型价格与用量统计口径.md) / 媒体计费文档 | 含 `/v1/videos` 路径与 rerank 缺口 | 路径替换；增 rerank 输入 token 行项说明 |
| [模型价格与用量统计口径](模型价格与用量统计口径.md) | 无 rerank 口径 | 增 rerank 输入 token 行项说明 |
| `docs/functions/README.md` | 无本文入口 | 增索引条目 + 维护规则（新增/调整对外协议路径时优先更新本文） |

## 7. 分期与验收

### 7.1 P0：统一视频面（本轮唯一必做）

范围：§4.2 全部端点 + §6.2 代码清单 + §6.4 文档同步（视频相关条目）。

验收（全部可执行）：

1. `POST /v1/video/generations` 用 Mock 上游 `media_video_*` 场景创建成功，返回 `task_id`/`status=queued`/`created_at`；`media_jobs` 落一行，`kind=video`。
2. `GET /v1/video/generations/{task_id}` 在 queued/in_progress/completed/failed 四态各返回 §4.2.3 形态；completed 的 `url` 为网关绝对地址且可经 `GET /v1/video/generations/{task_id}/content` 取回字节。
3. `GET /v1/video/generations`（列表）与 `DELETE /v1/video/generations/{task_id}`（取消）行为与旧实现一致（仅路径变化）；取消后轮询返回 `cancelled`。
4. 创建时 `n=2`（不支持该能力的 adapter）→ 400；`images` 两个 → 400；`metadata` 键与 `prompt` 同名 → 400 `metadata_conflict`；`response_format=b64_json` → 400。
5. `metadata.camera_fixed` 被 adapter 消费且回显在 `metadata_applied`；请求携带 `provider_options` 时按未知字段忽略且不产生回显。
6. **`/v1/videos*` 全族 404**（协议门移除，不留重定向）；演练场视频页走新路径仍可用（内部派发断言 `/v1/video/generations*`）。
7. 存量视频账户无需改动即可命中候选（`video_create` 复用）；存量 `media_jobs` 行可用新路径轮询/下载。
8. 语音 `POST /v1/audio/speech` 与长音频 `POST /v1/audio/jobs` 用 `metadata` 传厂商参数生效，`provider_options` 不再生效且不回显。

### 7.2 P1：`/v1/rerank`

验收：Mock 上游重排响应归一为 `{results,usage}`；`return_documents` 两态；`top_n` 生效；候选按 `rerank_json` 过滤（未声明账户 503）；schema revision 升级脚本在 dev 库可正反向跑通。

### 7.3 P2：图像/向量/音频规范化与厂商 adapter

按 §4.5 缺口逐项立项（qwen/glm 图像 adapter、gemini 向量桥接核实、speech `stream_format` 语义、`models/{id}`）。视频面**不再评估 multipart**（统一面 JSON-only 是有意裁决）。

## 8. 风险与边界

| 风险 | 影响 | 处置 |
| --- | --- | --- |
| new-api 客户端依赖运行时 `{code,data}` 而非文档契约 | 少数客户端解析失败 | 顶层按文档契约；**不提供** `data` 镜像（用户已裁决不做兼容），文档写明 |
| `url` 依赖 `Host`/`X-Forwarded-Proto` 构造绝对地址 | 反代配置异常时 URL 不可用 | 缺失回落相对路径；部署文档登记反代需透传 `Host` 与 `X-Forwarded-Proto`（`docker/single-server/Caddyfile` 已默认） |
| **破坏性变更**：`/v1/videos` 客户端直接失效 | 外部集成中断 | 发布说明标注 breaking + 迁移示例（路径与字段对照表）；不做兼容层是用户明确裁决（2026-11-19）；如需过渡只能靠文档与通知 |
| `metadata` 不分厂商命名空间 | 混合分组下厂商参数串扰 | 模型目录/映射保证同模型同厂；adapter 只消费已知键；`metadata_applied` 可核对；厂商自身拒绝未知键 |
| 旧字段 `provider_options` 被静默忽略 | 老客户端参数失效 | 发布说明明确列出；不做别名/双写；`metadata_applied` 为空即提示未生效（可排查） |
| rerank 引入 schema revision | 发布窗口依赖 | 与下一次发布窗口合并，按《升级兼容与迁移账本设计》执行 |
| `metadata` 被当作"万能透传"滥用 | 协议面失控 | 明确只补字段、不覆盖规范字段、冲突 400、仅媒体创建面；文档与错误文案写明 |
| 原 Sora 客户端的 multipart 创建诉求 | 无法直接迁移（统一面 JSON-only） | 明确不支持；需要文件输入的客户端先自行转 base64/URL（零存储边界不变） |

## 9. 证据索引（new-api）

| 事实 | 文件 |
| --- | --- |
| 协议路由全表 | `router/relay-router.go`、`router/video-router.go`、`router/task-router.go`、`router/task-plugin-protocol-router.go` |
| host protocol 定义（`openai_video`/`openai_image`/`openai_responses`） | `pkg/jsplugin/routing.go` |
| 统一视频提交响应（host fallback） | `controller/relay.go`（`presentTaskSubmission`） |
| 统一视频轮询响应（`{code,data}` TaskDto） | `relay/relay_task.go`（`videoFetchByIDRespBodyBuilder`）、`dto/task.go`（`TaskDto`） |
| 文档化契约（与运行时不符） | `dto/video.go`、`docs/openapi/relay.json` |
| 统一视频请求字段（宿主层词表） | `relay/common/relay_info.go`（`TaskSubmitReq`）、`relay/common/relay_utils.go`（`ValidateBasicTaskRequest`） |
| 插件内部字段（native / `openai_video` 来源） | `plugins/tasks/*/plugin.js`（`buildSubmitRequest` / `decodeRequest`） |
| `metadata` 并入厂商报文 | `plugins/tasks/kling/plugin.js:526-568`；`plugins/tasks/doubao/plugin.js:802-854` |
| OpenAI 视频面响应 | `model/task.go`（`ToOpenAIVideo`）、`relaykit/dto/openai_response.go`（`OpenAIVideoResponse`） |
| 图像/向量/音频/重排 DTO | `relaykit/dto/openai_image.go`、`relaykit/dto/embedding.go`、`relaykit/dto/audio.go`、`relaykit/dto/rerank.go` |
| 重排响应归一（Jina 形态） | `relay/common_handler/rerank.go` |
| 错误信封 | `relaykit/types/error.go` |
