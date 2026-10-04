# 媒体上游协议契约与 Mock 上游规格

> 状态：M0 契约基线（2026-10-04），随各期接入回填核实。
> 本文是《[音频视频模型接入与统一媒体网关设计](音频视频模型接入与统一媒体网关设计.md)》§5 媒体 IR/adapter 的**报文级展开**：每家上游厂商的认证、端点、请求/响应报文、任务状态机、usage 抽取、错误形态，以及**Mock 上游的行为规格**。
> **为什么必须有本文**：媒体链路的验收完全依赖 Mock 上游（不使用真实账户），Mock 的响应报文直接从本文各厂商章节派生；字段写错 Mock 即失真。实现 adapter 前必须先回填核实该厂商章节的"待核实"项。

## 1. 置信度标注与维护规则

每家章节头部标注来源置信度：

- **A 官方文档确认**：字段来自厂商官方 API 参考或多个独立官方渠道交叉确认；可直接实现与 Mock。
- **B 多源确认**：官方文档入口确认存在 + 独立第三方/SDK 文档交叉确认主干字段；实现前允许细小出入，Mock 按本文实现。
- **C 模式确认/细节待回填**：交互模式（如异步任务轮询）确认，但报文字段未取得官方原文；**接入前必须访问官方文档回填本章并更新本表**，回填前不得写死实现。

维护规则：

1. 任一厂商章节更新（回填/修正），同步更新 §2.6 状态归一总表、§2.7 参数映射表与 §2.8 计量来源表。
2. Mock 上游场景行为只允许引用本文规格，不得在测试代码里私造字段名。
3. 厂商 API 变更引起的行为修正，按《文档与代码一致性》规则同批更新本文与 adapter。

## 2. 统一参数规范（自建规范 + 业界对标）

### 2.0 设计立场与对标结论（2026-10-04 调研）

**立场**：文本域 OpenAI 协议已是事实标准，网关无需再统一；视频/音频域当前**无正式行业标准**，网关自建统一参数规范——用户请求只面向网关规范，**绝不透传厂商原生参数面**（否则网关失去存在意义）。业界出现正式标准后再迁移（见 §2.5）。

**业界对标事实**（2026-10 检索）：

| 先行者 | 做法 | 对我们的支撑 |
| --- | --- | --- |
| OpenRouter Video Generation API | 统一 submit job → poll status → download，改 `model` 即切 Sora/Veo/Kling/Runway | 骨架与"模型目录路由"决策一致 |
| LiteLLM video endpoints | 显式实现 OpenAI-API-compatible 的 submit/poll/result 跨厂商归一 | OpenAI 形态作为规范骨架是业界共同选择 |
| SiliconFlow `/v1/video/submit` + `/v1/video/status` | 国内统一视频 API：`model/prompt/image/imageSize(像素串)/seed/batchSize`；产物时效仅 10 分钟 | 像素串 `size`、`seed`、`n`(≈batchSize) 进规范层；短时效先例支撑"过期报错"契约 |
| Eden AI / CometAPI / AIML API 等 | job id + 轮询/webhook 统一模式 | 异步 job 是域内唯一通用模式 |

结论：无标准 → **L1/L2 由本规范定义，L3 扩展通道承接厂商个例**；骨架对齐 OpenAI（与业界聚合层同选），迁移成本最小。

### 2.1 分层定义

- **L1 核心参数**：跨厂商语义一致（`model`、`prompt`、`input`、`file` 等），网关直接消费。
- **L2 规范参数**：**网关统一定义名与值域**，由 adapter 映射/换算到厂商字段（§2.7 映射表为唯一事实源）；厂商不支持时按 §2.4 规则处理。L2 是"自建规范"的主体，禁止厂商原生参数名冒充 L2。
- **L3 provider_options**：厂商个例参数，用户显式按 provider 命名空间配置（§6 设计文档），命中账户的子对象 deep-merge 覆盖同名 L2 值，未知键原样透传由厂商裁决。

### 2.2 视频创建参数表（`POST /v1/videos`）

| 参数 | 层 | 类型 / 值域 | 厂商不支持时 |
| --- | --- | --- | --- |
| `model` | L1 | string（模型目录路由） | —— |
| `prompt` | L1 | string | —— |
| `seconds` | L2 | number；厂商固定档（如 6/10）时取档并回显 `params_applied` 实际值 | 用厂商默认时长并回显 |
| `size` | L2 | 像素串 `"1280x720"` / `"720x1280"` / `"960x960"`；adapter 换算 `aspectRatio`+`resolution`/档位，无精确档取最近档 | 取厂商默认并回显 |
| `n` | L2 | int ≥1，默认 1（≈SiliconFlow `batchSize`） | `n>1` → **400**（语义无法表达） |
| `input_reference` | L2 | 首帧图：`image_url` 或 base64 data URL（≈SiliconFlow `image`、veo `image.bytesBase64Encoded`） | 无图生视频 → **400**（能力缺失不静默） |
| `negative_prompt` | L2 | string | 忽略 + 回显 `params_ignored` |
| `seed` | L2 | int（可复现；SiliconFlow/部分厂商支持） | 忽略 + 回显 |
| `audio` | L2 | bool（视频原生音频开关；Sora 2/Veo 3/Grok 原生带音频） | 忽略 + 回显 |
| `provider_options` | L3 | 见 §2.1 | —— |

### 2.3 音频参数表

**TTS（`POST /v1/audio/speech`）**：

| 参数 | 层 | 值域 | 不支持时 |
| --- | --- | --- | --- |
| `model`、`input` | L1 | string | —— |
| `voice` | L1 | 值域按模型目录各 TTS 模型标注的 `voices` 清单校验（跨厂商音色名不通用是事实；清单为目录元数据，管理面/文档暴露，`/v1/models` 不暴露） | 不在清单 → 400 |
| `speed` | L2 | 0.25–4.0（OpenAI 词表） | 超厂商区间 → **400**（不做 clamp，不改变用户值） |
| `response_format` | L2 | `mp3/opus/aac/flac/wav/pcm` | 厂商无该输出 → **400**（零转码：不做服务端格式转换） |
| `instructions` | L2 | string（风格指令，gpt-4o-mini-tts 系） | 忽略 + 回显 |
| `language` | L2 | BCP-47 | 忽略 + 回显 |
| `volume`/`pitch`/`emotion`/SSML 等个例 | L3 | provider_options | —— |

**STT（`POST /v1/audio/transcriptions`、`/translations`）**：

| 参数 | 层 | 值域 | 不支持时 |
| --- | --- | --- | --- |
| `model`、`file`（multipart） | L1 | —— | —— |
| `language` | L2 | BCP-47 | 自动检测厂商忽略 + 回显 |
| `prompt` | L2 | 热词/上下文 | 忽略 + 回显 |
| `response_format` | L2 | `json/text/verbose_json/srt/vtt` | 无该格式 → **400** |
| `timestamp_granularities` | L2 | `word`/`segment` | 忽略 + 回显 |
| 说话人分离（diarization）、多声道等个例 | L3 | provider_options | —— |

### 2.4 忽略 / 拒绝 / 回显通用规则（契约级）

1. **L2 可选语义参数**（`negative_prompt`/`seed`/`audio`/`instructions` 等）：厂商不支持 → 忽略，创建响应 job 对象回显 `params_ignored`（键名列表）——**不静默**。
2. **L2 语义无法表达的取值**（`n>1`、`input_reference` 无对应能力、`response_format` 无该输出、`speed` 超区间）：**400**，错误信息指明"该模型/厂商能力边界"，不降级不猜测。
3. **换算类参数**（`seconds` 档位、`size` → 宽高比/分辨率）：按 §2.7 映射换算，实际生效值回显 `params_applied`。
4. **L3**：仅命中 provider 的子对象生效（覆盖同名 L2），其余子对象忽略；响应回显已生效子对象键名摘要（不含值）。
5. job 对象固定含 `params_applied` 与 `params_ignored` 两个字段（同步响应同样适用）。

### 2.5 规范演进策略

骨架（端点形态、job 语义、L1 词表）对齐 OpenAI videos/audio——与 OpenRouter/LiteLLM 的聚合层选择一致；自有扩展（`provider_options`、`params_applied/params_ignored` 回显、`seed`/`audio`/`negative_prompt` 归一）保持最小集。未来行业标准落地（OpenAI videos 全面普及或新标准出现）时：只调整 L2 词表与 §2.7 映射表，端点骨架与 L1 不动，`provider_options` 原样保留。

### 2.6 MediaJobIR 状态归一总表（厂商状态 → 统一 status）

| 统一 status | 语义 | openai | gemini(veo) | minimax | volcengine | qwen(dashscope) | glm |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `queued` | 已受理未开始 | `queued` | operation 未 `done` 且无进展字段 | `Preparing` / `Queueing` | `queued` | `PENDING` | 待回填 |
| `in_progress` | 生成中 | `in_progress`（含 `progress`） | operation 未 `done` | `Processing` | `running` | `RUNNING` | 待回填 |
| `completed` | 成功 | `completed`（`content` 有下载定位） | `done:true` 且 `response.generateVideoResponse.generatedSamples[].video.uri` 存在 | `Success`（`file_id`/`file_download_url`） | `succeeded`（`content.video_url`） | `SUCCEEDED`（`output.video_url` 等） | 待回填 |
| `failed` | 失败 | `failed`（`error`） | `done:true` 且 `error` 非空 | `Fail`（`base_resp`） | `failed`（`error`） | `FAILED`（`message`/`code`） | 待回填 |
| `cancelled` / `expired` | 本地终态 | （上游删除后查询 404 → 本地收敛） | 同左 | 同左 | 同左 | 同左 | 同左 |

归一规则：未知状态值一律归 `in_progress` 并记录原始值（不猜测失败）；上游 404 查询且本地非终态 → 保持本地状态直至 TTL 过期（不得伪造终态）。

### 2.7 公共参数 → 厂商字段映射表（L1/L2 的派生事实源）

| 公共参数 | openai | gemini tts | minimax 视频 | volcengine 视频 | qwen 视频 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| `model` | 同名 | URL 路径段（`:generateContent` 前） | `model` | `model` | URL/model 字段 | — |
| `prompt` | 同名 | `contents[].parts[].text` | `prompt` | `content[].text` | `input.prompt` 等 | — |
| `seconds` | `seconds`（响应回显 `seconds_length`） | —— | `duration` | —— | —— | 无对应者的厂商按其默认时长，不做换算猜测 |
| `size` | `size`（如 `1280x720`） | `parameters.aspectRatio`（`16:9`/`9:16`） | —— | `resolution` 档 | —— | 分辨率→宽高比换算仅此一处，写入 adapter |
| `n` | `n` | —— | —— | —— | —— | 多数厂商固定 1；请求 `n>1` 且厂商不支持时创建即 400 |
| `input_reference`（图生视频首帧） | `input_reference`（multipart 文件） | `instances[].image.bytesBase64Encoded` | `first_frame_image`（base64） | `content[].image_url` | `input.img_url` | 网关只透传不解析存储 |
| `voice`（TTS） | `voice` | `generationConfig.speechConfig.prebuiltVoiceConfig.voiceName` | （TTS 章） | （TTS 章） | （TTS 章） | — |

### 2.8 usage / 计量来源表（谁出数字）

| 厂商 | TTS | STT | 视频 | 说明 |
| --- | --- | --- | --- | --- |
| openai | 双口径（M1 已实施，按厂商真实定价维度）：`gpt-4o-mini-tts` 按 **token 口径**计价（复用既有 `audioInputUsdPer1M`/`audioOutputUsdPer1M` 单价，seed 已填价；二进制响应无 usage 回报，网关亦无 token 自算维度 → 0 计费 + `usage_missing` 标记，不猜测）；`gpt-4o-tts` 未收录（官方无定价，待官方价后补行，不编造）；`tts-1`/`tts-1-hd` 上游无 usage 回报，**网关自算**按请求 `input` 字符数（新行项 `tts_input_chars`） | 双口径：`gpt-4o-transcribe` 系按 **token 口径**（`usage.input_tokens`/`output_tokens`，同一对 audio token 单价）；`whisper-1` 按 `duration` 秒（verbose_json；新行项 `audio_input_seconds`）；usage 优先 → duration → 0 + `usage_missing` 标记，不猜测 | **网关自算**：按任务参数 `seconds` × 档位；失败任务不虚计 | — |
| gemini | 对话式 token 计量（`usageMetadata`，已有链路） | 同左（audio token） | 按输出秒（目录价格档） | — |
| minimax | 按字符（网关自算或上游回报，接入回填） | 待回填 | 任务响应含用量/按次（回填） | — |
| volcengine | 按字符（回填） | 长转写按时长（回填） | 任务响应 `usage`（检索确认部分包含；字段回填） | — |
| qwen | CosyVoice 按字符（回填） | paraformer 按时长（回填） | 万相按次/时长（回填） | — |
| glm | 回填 | 回填 | 回填 | — |

规则：上游回报 usage 优先；上游不回报的维度网关按可观察参数（字符数/请求秒数）自算；两者都缺 → 0 计费 + 记录 `usage_missing` 标记，**不猜测**。

## 3. Mock 上游规格（测试基建扩展契约）

### 3.1 现状事实（2026-10-04 取证）

- 集中 Mock 引擎：`backend-go/shared/platform/mockupstream`（17 场景、`X-Mock-Scenario`/`?scenario=` 选择、端点白名单仅 chat/responses/embeddings/models，其余 404）。
- E2E：`acceptance` 目录三件套——场景控制器 `fullchainMockUpstream`（按上游 Bearer key 的场景队列）、`createAccount`（凭据 `base_url` 注入 httptest 地址）、`JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true` 私网放行。
- `mockdata` 为 DB 造数体系（非请求 mock）；新页面/列表/用量必须同步扩展对应域（《Mockdata造数设计》强制约定）。
- **缺口**：mockupstream 无媒体端点、无异步任务多态响应（状态随轮询推进）、无二进制载荷通道；`fullchainMockUpstream` 的 scriptable 判定只认 4 条 POST 路径。

### 3.2 mockupstream 引擎扩展规格

1. **端点白名单扩展**（`mockupstream.go` `acceptedEndpoints`）：`POST /v1/audio/speech`、`POST /v1/audio/transcriptions`、`POST /v1/audio/translations`、`POST /v1/videos`、`GET /v1/videos`、`GET /v1/videos/{id}`、`GET /v1/videos/{id}/content`、`DELETE /v1/videos/{id}`，以及 `/v1/audio/jobs` 族与 §4-§10 各厂商原生端点（每厂商一章的"Mock 端点"小节为准）。音频三端点 M1 已交付；`/v1/videos` 全 5 端点 M2 已交付（2026-10-04）；`/v1/audio/jobs` 族与厂商原生端点随对应期。
2. **异步任务场景能力（新增机制）**：按场景脚本驱动任务状态机——创建请求返回该厂商创建响应（含任务 id，id 由 mock 生成并登记内存任务表）；轮询请求按"第 N 次查询返回状态 X"脚本推进（如 `video_poll_twice_then_success`：前两次 `in_progress`，第三次终态）；同一 mock 实例内任务表隔离，支持并发用例。（M2 已交付，2026-10-04：视频任务状态机按场景脚本推进。）
3. **二进制载荷通道（新增机制）**：场景可声明响应为 `audio/wav`、`audio/mpeg`、`video/mp4` 等二进制（内置小体积合成载荷：合法 WAV 头+静音帧、最小 MP4 box 结构），供 speech/content/下载端点流式返回；断言侧校验 magic bytes 与 content-type。（音频载荷 M1 已交付；`video/mp4` 载荷 M2 增补交付，2026-10-04。）
4. **场景命名规范**（进 `mockupstream` 内置清单，格式 `media_<域>_<行为>`）。**M1 已交付 8 个同步音频场景与二进制载荷通道（2026-10-04）**：`media_tts_ok`、`media_tts_400_voice_invalid`、`media_tts_429_before_accept`、`media_stt_ok`、`media_stt_ok_verbose`、`media_stt_400_bad_file`、`media_gemini_tts_ok`、`media_gemini_tts_400_format`；**视频场景已随 M2 交付（2026-10-04，共 9 个）**，厂商原生异步任务场景属 M3+：
   - `media_tts_ok`（二进制音频）、`media_tts_400_voice_invalid`、`media_tts_429_before_accept`——M1 已交付
   - `media_stt_ok`（JSON `text`）、`media_stt_ok_verbose`（`verbose_json` 含 `duration`/`usage`）、`media_stt_400_bad_file`——M1 已交付
   - `media_gemini_tts_ok`（inlineData base64 PCM）、`media_gemini_tts_400_format`（`response_format` 非 `pcm` → 400）——M1 已交付
   - `media_video_ok_poll3`（#1/#2 `in_progress` → #3 `completed`）、`media_video_ok_poll1`（#1 即 `completed`，快路径）、`media_video_fail_after_accept`（受理后轮询 `failed`）、`media_video_429_create`（创建 429，可换账户重试）、`media_video_create_400_bad_size`（`size` 值域外 → 400，参数类不换账户）、`media_video_create_500`（创建 5xx）、`media_video_poll_500`（受理后轮询 5xx，不得换账户）、`media_video_content_expired`（content 404/410 → 网关透出"产物已过期"）、`media_video_cancel_ok`（保持 `queued`，验证 DELETE 流程）——M2 已交付（2026-10-04；其中 `media_video_ok_poll1` 与 `media_video_create_400_bad_size` 为 W1 批次交付、本清单同日补登记）
   - 厂商原生形态场景：`media_<provider>_create_ok` / `media_<provider>_poll_running` / `media_<provider>_poll_success` / `media_<provider>_poll_fail`（报文按 §4-§10 各章）——M3+（§5.1 的 `media_gemini_tts_*` 已随 M1 交付；§5.2 的 `media_gemini_video_*` 随 M3）
5. **slow/abort 基建复用**：首字节延迟、分块延迟、中途断连直接复用现有 `slow_first_byte`/`mid_stream_close` 机制，用于受理边界与流中断用例。

### 3.3 acceptance E2E 扩展规格

1. `fullchainMockUpstream` 的 scriptable 路径判定扩至 §3.2.1 全部端点（含 GET/DELETE 方法）。（已交付，2026-10-04：`fullchainMediaScriptable` 覆盖音频 3 端点 + `/v1/videos` 全 5 端点 + Gemini `:generateContent`，与 mockupstream `acceptsMediaEndpoint` 同一端点清单；视频任务面 GET/DELETE 计入判定但不读场景值，轮询按引擎任务表脚本推进。）
2. 场景控制器新增"任务型脚本"队列语义：创建请求消耗一个队列条目决定创建结果，后续轮询按任务表脚本推进（与 mockupstream 3.2.2 同一状态机实现，不重复实现两份）。
3. E2E 断言包：`media_jobs` 行状态推进、终态 usage 落库金额、审计无资源字节（`rg` 级别断言快照字段白名单）、账户亲和（轮询请求打回原 mock key 的请求记录）。

### 3.4 mockdata 域扩展规格

- `domain_usage`：TTS/STT/视频任务 usage 行样本（新计量列、新行项种类）。
- `domain_observability`：媒体任务审计/日志样本行。
- `domain_business`：`media_jobs` 各状态样本行、音频/视频模型目录行、mock 上游账户样本。
- 以上随 M1/M2 对应管理面能力同批交付（页面有数据的唯一来源）。

## 4. openai / gpt（置信度 A：官方文档确认）

对外契约即本体，网关直连转发；本节同时是 Mock 上游的权威模板。

### 4.1 TTS：`POST /v1/audio/speech`

请求：`{"model":"gpt-4o-mini-tts","input":"...","voice":"alloy","speed":1.0,"response_format":"mp3","instructions":"..."}`。
响应：音频二进制（content-type 按 `response_format`，如 `audio/mpeg`）；**无 JSON usage**。
计费（M1 双口径，按厂商真实定价维度）：`tts-1`、`tts-1-hd` 按 `input` 字符数（网关自算，行项 `tts_input_chars`）；`gpt-4o-mini-tts` 按 token 口径（既有 `audioInputUsdPer1M`/`audioOutputUsdPer1M` 单价，seed 已填价；二进制响应无 usage，0 计费 + `usage_missing` 标记，不猜测）；`gpt-4o-tts` 未收录（官方无定价，待官方价后补行，不编造）。
Mock：`media_tts_ok` 返回内置合成 WAV/MP3 载荷。

### 4.2 STT：`POST /v1/audio/transcriptions`（multipart：`file`、`model`、`language`、`prompt`、`response_format`、`timestamp_granularities[]`）

响应（`json`，默认）：`{"text":"..."}`；（`verbose_json`）：`{"text","language","duration"(秒),"segments":[...],"words":[...],"usage":{"input_tokens","output_tokens"}(gpt-4o-transcribe 系)}`。
`/v1/audio/translations` 同构。计费：`usage` 优先，否则按 `duration` 秒（whisper 系，官方按分钟定价、计量落秒）。
Mock：`media_stt_ok` / `media_stt_ok_verbose`。

### 4.3 视频：`POST /v1/videos`（sora）

请求：`{"model":"sora-2","prompt":"...","seconds":"4","size":"1280x720","n":1,"input_reference": <multipart 文件>}`（图生视频模型 `sora-2/image-to-video`）。
创建响应（video 对象）：`{"id":"video_...","object":"video","status":"queued","progress":0,"model":"sora-2","prompt":"...","seconds_length":4,"size":"1280x720",...}`。
轮询 `GET /v1/videos/{id}`：同对象，`status ∈ queued|in_progress|completed|failed`，`progress` 推进；`completed` 时 `content` 数组提供下载定位。
下载 `GET /v1/videos/{id}/content`：视频字节流（需上游账户认证；保留时效约 1 小时）。
删除 `DELETE /v1/videos/{id}`。列表 `GET /v1/videos`。
计费：网关按 `seconds` × 分辨率档自算；失败不虚计。
Mock：`media_video_*` 全套 + 创建响应必须含 `id`/`status:"queued"`（受理凭据字段，E2E 断言点）。

## 5. gemini（置信度 A/B：TTS 官方文档确认；Veo REST 多源确认）

### 5.1 TTS：`POST /v1beta/models/{model}:generateContent`

请求：

```json
{
  "contents":[{"parts":[{"text":"Say cheerfully: ..."}]}],
  "generationConfig":{
    "responseModalities":["AUDIO"],
    "speechConfig":{"prebuiltVoiceConfig":{"voiceName":"Kore"}}
  }
}
```

模型：`gemini-2.5-flash-preview-tts` / `gemini-2.5-pro-preview-tts` 系。voiceName 词表（Kore/Puck/Charon/Fenrir/Aoede 等 30 个）映射公共 `voice`；`speed` 无对应（不支持时忽略+回显，不换算）。
响应：`candidates[0].content.parts[0].inlineData.data` = base64 **PCM 24kHz 16bit mono**（`mimeType: audio/L16;rate=24000`），无 WAV 头、无 MP3。
**`response_format` 裁决（2026-10-04 定稿）**：Gemini TTS 目录元数据标注支持格式 `["pcm"]`，请求其他格式（`mp3`/`wav` 等）→ **400**（零转码：网关不做服务端格式转换）；`pcm` 语义 = 上游原始裸 PCM（24kHz/16bit/mono），与 OpenAI `response_format=pcm`（24kHz/16bit）语义对齐。OpenAI TTS 模型目录标注支持格式 `["mp3","opus","aac","flac","wav","pcm"]`（官方全集）。
计费：`usageMetadata` token 计量（既有链路）。
Mock：`media_gemini_tts_ok`（inlineData base64 PCM 载荷）、`media_gemini_tts_400_format`（`response_format` 非 `pcm` → 400 错误包）。

### 5.2 视频（Veo）：`POST /v1beta/models/{model}:predictLongRunning`

请求：

```json
{
  "instances":[{"prompt":"...","image":{"bytesBase64Encoded":"<可选首帧>"}}],
  "parameters":{"aspectRatio":"16:9","negativePrompt":"...","resolution":"1080p"}
}
```

模型：`veo-3.0-generate-preview` / `veo-3.0-fast-generate-preview` / `veo-3.1` 系（以目录 seed 为准）。
创建响应：`{"name":"models/<model>/operations/<op_id>"}`——**受理凭据 = `name` 字段**。
轮询：`GET /v1beta/{name}` → `{"done":bool,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"<GCS 签名 URL>"}}]}},"error":{...}}`；uri 时效约 2 天。
下载：直连 uri（经网关流式代理，认证按 GCS 签名内嵌，不需要额外凭据）。
计费：按输出秒（目录档）；usage 不回报，网关按请求时长参数自算。
Mock：`media_gemini_video_create_ok` / `media_gemini_video_poll_running` / `media_gemini_video_poll_done_uri`。

## 6. xai（置信度 C：能力确认，端点待官方回填）

能力事实（2026-10 检索）：Grok Voice API 提供 Speech-to-Speech、Speech-to-Text、Text-to-Speech；grok-imagine 视频生成支持文生视频/图生视频（6/10 秒、480p/720p、视频原生带音频）。
**接入前必做**：以 `docs.x.ai` 官方 API 参考回填端点路径、请求/响应字段、任务状态机与 usage 字段；回填前本节不作为实现依据。M3 排期时先完成回填再写 adapter。

## 7. glm / 智谱（置信度 C：模式确认，报文待回填）

模式事实：CogVideoX 系视频生成走异步任务（提交得 `id` → 轮询 → 视频 URL），端点族为 `/api/paas/v4/videos`（`open.bigmodel.cn`）；ASR 存在流式接口；TTS 端点待核实。
**接入前必做**：以 `open.bigmodel.cn` 官方文档回填本节（创建/查询请求响应 JSON、状态词表、usage 字段、语音端点与计费单位）。既有 `glm` 供应商档案沿用，仅新增媒体 profile 能力（endpoint families + modes），不新增 provider_code。

## 8. minimax（置信度 B：官方文档多源确认）

### 8.1 视频：`POST /v1/video_generation`（host `api.minimax.chat` 或 `api.minimaxi.com`，以账户 base_url 为准）

请求：`{"model":"MiniMax-Hailuo-2.3","prompt":"...","prompt_optimizer":true,"duration":6,"first_frame_image":"<base64>"}`（`first_frame_image` 承接公共 `input_reference`）。
创建响应：`{"task_id":"...","base_resp":{"status_code":...,"status_msg":"..."}}`——**受理凭据 = `task_id`**；`base_resp.status_code != 0` 视为失败（受理前错误）。
轮询：`GET /v1/query/video_generation?task_id=...` → `{"task_id","status":"Preparing|Queueing|Processing|Success|Fail","file_id":"...","file_download_url":"..."}`。
下载：`GET /v1/files/retrieve?file_id=...`（或 `file_download_url`，以回填为准）。
计费：任务响应用量字段待回填（§2.8）；按官方定价档。
`provider_options.minimax` 示例：`{"prompt_optimizer":true}`。
Mock：`media_minimax_create_ok` / `_poll_running` / `_poll_success(file_id)` / `_poll_fail(base_resp)`。

### 8.2 TTS / 长转写

`POST /v1/t2a_v2`（同步/流式 TTS，`voice_setting`、`text`、`audio_format`）与异步长音频任务：端点与字段**接入前以 platform.minimax.io 回填**；计费按字符（§2.8）。
Mock 场景随回填补齐。

## 9. volcengine / 火山方舟（置信度 B：官方格式多源确认）

host：`ark.cn-beijing.volces.com`；认证 `Authorization: Bearer <ARK_API_KEY>`。

### 9.1 视频（seedance 系）：`POST /api/v3/contents/generations/tasks`

请求：`{"model":"seedance-1.0-pro" 系,"content":[{"type":"text","text":"..."},{"type":"image_url","image_url":{"url":"..."}}]}`（图生视频经 content 数组）。
创建响应：`{"id":"cgt-...","status":"queued",...}`——**受理凭据 = `id`**。
轮询：`GET /api/v3/contents/generations/tasks/{id}` → `status ∈ queued|running|succeeded|failed`；成功 `content.video_url`（时效内下载 URL）；失败 `error{code,message}`；部分任务含 `usage`（字段名接入回填）。
下载：`video_url` 直连流式代理。
计费：seedance 按秒 × 分辨率档；`usage` 回报优先。
`provider_options.volcengine` 示例：`{"resolution":"720p","ratio":"16:9"}`（以官方参数名为准回填）。
Mock：`media_volcengine_create_ok` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error`。

### 9.2 TTS / 长转写

豆包 TTS（`/api/v3/tts` 族）与异步长转写：端点与字段接入前以火山官方文档回填；计费 TTS 按字符、转写按时长。

## 10. qwen / 通义百炼（置信度 B：DashScope 通用任务模式确认，端点细节回填）

host：`dashscope.aliyuncs.com`；兼容模式认证 `Authorization: Bearer <DASHSCOPE_API_KEY>`；**异步任务约定：创建请求带 `X-DashScope-Async: enable` 头**。

### 10.1 视频（万相 wanx / wan 系）

创建：`POST /api/v1/services/aigc/video-generation/video-synthesis`（或新版 `/api/v1/videos`，接入时以百炼文档定稿）+ `X-DashScope-Async: enable` → `{"output":{"task_id":"...","task_status":"PENDING"},"request_id":"..."}`——**受理凭据 = `output.task_id`**。
轮询：`GET /api/v1/tasks/{task_id}` → `{"output":{"task_status":"PENDING|RUNNING|SUCCEEDED|FAILED","video_url":"..."},"usage":"<原始 JSON 字符串>","code","message"}`（DashScope 的 `usage` 为 JSON 字符串形态，解析时注意）。
下载：`video_url` 直连。
计费：万相按次/时长（档位回填）；`usage` 回报优先。

### 10.2 TTS（CosyVoice 系）/ 长转写（paraformer 系）

CosyVoice 同步/流式（按字符）；paraformer 文件转写同为 DashScope 异步任务模式（提交 → `task_id` → `GET /api/v1/tasks/{id}`，转写结果 JSON 在 `output.results` 形态，接入回填）。
Mock：`media_qwen_create_pending` / `_poll_running` / `_poll_succeeded_video_url` / `_tts_ok`。

## 11. 与设计的衔接及前置裁决项清单

1. ~~§5.1 Gemini TTS 对外 `response_format` 约束（PCM 透传 vs 400）——M1 实施首日裁决并回填本节~~ **已裁决（2026-10-04）：pcm 透传、其余 400、零转码，见 §5.1**。
2. §6 xai、§7 glm、§8.2、§9.2、§10.2 的"待回填"项——对应期接入前完成，回填前不得实现该 adapter。
3. Mock 上游基建扩展（§3.2/§3.3）为 M1 首个交付物（先有 Mock 再写链路）；mockdata 域扩展随管理面能力同批。
4. 本文与《音频视频模型接入与统一媒体网关设计》冲突时，以设计文档的目标/边界为准、以本文的报文字段为准。

## 12. 来源记录（2026-10-04 检索）

- OpenAI audio/videos：OpenAI 平台 API 参考（Audio create transcription `verbose_json` 字段；video 对象 `id/object/status/progress/seconds_length/size/content`）。
- Gemini TTS：`ai.google.dev/gemini-api/docs/speech-generation`（speechConfig/prebuiltVoiceConfig/voiceName、PCM 24kHz 输出、30 预置音色）。
- Veo REST：多源交叉（`:predictLongRunning` → operation name → `GET /v1beta/{name}` → `generateVideoResponse.generatedSamples[].video.uri`，uri 时效约 2 天）。
- MiniMax：`platform.minimax.io` API 参考（video_generation / query/video_generation / files/retrieve，状态词表）。
- 火山方舟：官方任务接口格式（`/api/v3/contents/generations/tasks` 创建与 `{id}` 查询、`queued|running|succeeded|failed`、`content.video_url`）。
- 百炼：DashScope 异步任务约定（`X-DashScope-Async`、`GET /api/v1/tasks/{task_id}`、PENDING/RUNNING/SUCCEEDED/FAILED）。
- xAI：Grok API 定价页与评测（Voice API 三能力、grok-imagine 6/10s 480p/720p）。
- 智谱：社区与聚合层证据（CogVideoX 异步任务、ASR 流式）；官方文档搜索引擎收录有限，列为 C 级回填。
