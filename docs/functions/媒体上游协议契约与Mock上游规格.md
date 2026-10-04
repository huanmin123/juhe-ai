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
| `queued` | 已受理未开始 | `queued` | operation 未 `done`（Veo 无进展字段，M3 adapter 归一：未 done 恒 queued） | `Preparing` / `Queueing` | `queued` | `PENDING`（M3 已实施） | （不可达：创建响应即 `PROCESSING`，M3 已实施） |
| `in_progress` | 生成中 | `in_progress`（含 `progress`） | （不可达：无进展字段，见 queued 行） | `Processing` | `running` | `RUNNING`（M3 已实施） | `PROCESSING`（M3 已实施） |
| `completed` | 成功 | `completed`（`content` 有下载定位） | `done:true` 且 `response.generateVideoResponse.generatedSamples[].video.uri` 存在（M3 已实施：uri 冻结进 Artifact.ContentURL，直连下载无凭据） | `Success`（`file_id`/`file_download_url`） | `succeeded`（`content.video_url`） | `SUCCEEDED`（`output.video_url`，M3 已实施：url 冻结进 Artifact.ContentURL，直连下载无凭据） | `SUCCESS` 且 `video_result.url` 存在（M3 已实施：url 冻结进 Artifact.ContentURL，直连下载无凭据） |
| `failed` | 失败 | `failed`（`error`） | `done:true` 且 `error` 非空（M3 已实施：error 摘要 code 取 status） | `Fail`（`base_resp`） | `failed`（`error`） | `FAILED`（顶层 `message`/`code`，M3 已实施） | `FAIL`（M3 已实施：错误摘要 code 取 task_status 原值，error 对象在场则优先） |
| `cancelled` / `expired` | 本地终态 | （上游删除后查询 404 → 本地收敛） | 同左（`:cancel` 上游 2xx/404/405 均收敛本地 cancelled，M3 裁决） | 同左（minimax 无取消 API：不发上游请求直接本地收敛，M3 裁决，§8.1） | 同左 | 同左（qwen 无取消 API：不发上游请求直接本地收敛，M3 裁决，§10.1） | 同左（glm 无取消 API：不发上游请求直接本地收敛，M3 裁决，§7.1） |

归一规则：未知状态值一律归 `in_progress` 并记录原始值（不猜测失败）；上游 404 查询且本地非终态 → 保持本地状态直至 TTL 过期（不得伪造终态）。

### 2.7 公共参数 → 厂商字段映射表（L1/L2 的派生事实源）

| 公共参数 | openai | gemini tts | minimax 视频 | volcengine 视频 | qwen 视频 | 说明 |
| --- | --- | --- | --- | --- | --- | --- |
| `model` | 同名 | URL 路径段（`:generateContent` 前） | `model` | `model` | URL/model 字段 | — |
| `prompt` | 同名 | `contents[].parts[].text` | `prompt` | `content[].text` | `input.prompt` 等 | — |
| `seconds` | `seconds`（响应回显 `seconds_length`） | —— | `duration` | `duration`（数值秒直传，2026-10-04 官方核实；M3 已实施） | `parameters.duration`（数值秒直传，2026-10-04 官方核实；M3 已实施——模型级档位由上游裁决） | 无对应者的厂商按其默认时长，不做换算猜测 |
| `size` | `size`（如 `1280x720`） | `parameters.aspectRatio`（`16:9`/`9:16`） | —— | `resolution` + `ratio` 档（M3 已实施：ratio 约分精确命中官方词表，词表外 400；resolution 短边最近档） | `parameters.size` W\*H 星号形态（M3 已实施：WxH→W\*H 纯格式转换，档位由上游裁决） | 分辨率→宽高比/档位换算写入 adapter（gemini、volcengine） |
| `n` | `n` | —— | —— | —— | —— | 多数厂商固定 1；请求 `n>1` 且厂商不支持时创建即 400 |
| `input_reference`（图生视频首帧） | `input_reference`（multipart 文件） | `instances[].image.bytesBase64Encoded` | `first_frame_image`（base64） | `content[].image_url`（url/base64 双形态直传，M3 已实施） | `input.img_url`（url/base64 双形态直传，M3 已实施——图生视频形态同端点） | 网关只透传不解析存储 |
| `voice`（TTS） | `voice` | `generationConfig.speechConfig.prebuiltVoiceConfig.voiceName` | （TTS 章） | （TTS 章） | （TTS 章） | — |

### 2.8 usage / 计量来源表（谁出数字）

| 厂商 | TTS | STT | 视频 | 说明 |
| --- | --- | --- | --- | --- |
| openai | 双口径（M1 已实施，按厂商真实定价维度）：`gpt-4o-mini-tts` 按 **token 口径**计价（复用既有 `audioInputUsdPer1M`/`audioOutputUsdPer1M` 单价，seed 已填价；二进制响应无 usage 回报，网关亦无 token 自算维度 → 0 计费 + `usage_missing` 标记，不猜测）；`gpt-4o-tts` 未收录（官方无定价，待官方价后补行，不编造）；`tts-1`/`tts-1-hd` 上游无 usage 回报，**网关自算**按请求 `input` 字符数（新行项 `tts_input_chars`） | 双口径：`gpt-4o-transcribe` 系按 **token 口径**（`usage.input_tokens`/`output_tokens`，同一对 audio token 单价）；`whisper-1` 按 `duration` 秒（verbose_json；新行项 `audio_input_seconds`）；usage 优先 → duration → 0 + `usage_missing` 标记，不猜测 | **网关自算**：按任务参数 `seconds` × 档位；失败任务不虚计 | — |
| gemini | 对话式 token 计量（`usageMetadata`，已有链路） | 同左（audio token） | 按输出秒（目录价格档） | — |
| minimax | 按字符（M3 已实施：`extra_info.usage_characters` 回报优先 → `tts_input_chars`，缺失回落请求字符自算；官方口径 ¥2.00/万字符人民币、无官方美元价 → 目录不落字符价，计量照落成本不虚计） | 待回填（长转写未接入） | 任务响应不回报时长（M3 已实施：无可查证官方 USD 秒价 → 0 计费 + `usage_missing`，不编造） | — |
| volcengine | 按字符（回填） | 长转写按时长（回填） | 任务响应 `usage`（检索确认部分包含；字段回填）。M3 已实施口径：usage/duration 字段未回填前不抽秒 → 0 计费 + `usage_missing`（不落秒价，官方口径为人民币无 USD 秒价），字段回填后再启用 | — |
| qwen | CosyVoice 按字符（回填） | paraformer 按时长（回填） | 万相按次/时长人民币口径，无可查证 USD 秒价（M3 已实施：目录不落秒价，`usage` JSON 字符串的 `video_duration`/`output_video_duration`/`duration` 字段族秒计量照抽、成本 0 不虚计；无秒可抽时才落 usage_missing 兜底） | — |
| glm | 回填 | 回填 | **0 计费 + `usage_missing`**（M3 已实施：CogVideoX 按次计费、官方精确秒价不可查证且轮询响应无时长回报——不编造秒价，目录行不落价；官方口径可查证后补秒价，§7.1 计费落法） | — |

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
   - 厂商原生形态场景：`media_<provider>_create_ok` / `media_<provider>_poll_running` / `media_<provider>_poll_success` / `media_<provider>_poll_fail`（报文按 §4-§10 各章）——M3+（§5.1 的 `media_gemini_tts_*` 已随 M1 交付；**§5.2 的 `media_gemini_video_create_ok` / `_poll_running` / `_poll_done_uri` / `_poll_error` 已随 M3 交付（2026-10-04）**；**§7.1 的 `media_glm_video_*` 已随 M3 交付；§8 的 `media_minimax_create_ok` / `_poll_running` / `_poll_success` / `_poll_fail` 与 §8.2 的 `media_minimax_tts_ok` 已随 M3 第三批交付（2026-10-04）**；**§9.1 的 `media_volcengine_create_ok` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error` 已随 M3 第四批交付（2026-10-04）**；**§10 的 `media_qwen_create_pending` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error` 已随 M3 第五批交付（2026-10-04）**）
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

### 4.4 Realtime（WS 双向流，M5b；置信度 B：官方文档多源确认 2026-10-04）

- 端点：`wss://api.openai.com/v1/realtime?model=gpt-realtime`（另有 WebRTC 变体，网关不做）；上游认证 Bearer（账户凭据）。
- ephemeral token：`POST /v1/realtime/client_secrets`（2026 年端点；历史文档为 `/v1/realtime/sessions`——网关对外提供 `client_secrets` 形态，接入时以官方页复核）。
- 事件协议（JSON 文本帧双向）：客户端 `session.update`（model/voice/modalities/tools 配置）、`input_audio_buffer.append`、`response.create` 等；服务端 `session.created`、`response.audio_transcript.delta`/`response.audio.delta`、`response.done`（含 `response.usage.input_tokens/output_tokens` 与 `*_token_details.audio_tokens`——网关旁路解析计量点）。注意 2026-09 后的 GPT Live 系模型会话事件与 gpt-realtime 不同——**网关首版目录只收 gpt-realtime 系**，GPT Live 系待其事件语义回填。
- 计费：audio token 口径（官方 $32/$64 每 1M audio token 量级，接入时以官方定价页为准落价；查不到不编造）；usage 从事件流累计，连接关闭终态落库；空会话 0 计费+usage_missing。
- 受理凭据：**上游 WS 101 升级成功**（拨号/握手失败或升级拒绝=受理前可换账户）。
- Mock：`media_realtime_echo` / `media_realtime_reject_upgrade` / `media_realtime_close_after_established` / `media_realtime_idle`（行为规格见《实时语音Realtime网关设计》§7；**基建已交付**（M5b1，2026-10-04）：`mockupstream/realtime.go` 四场景 + gorilla/websocket v1.5.3 依赖落地，echo 支持脚本参数 `usage_events`（逗号分隔帧序号，到达后发 response.done 含 usage）与 `close_after`（N 帧后 close 1000），`realtime_test.go` 真客户端断言；**网关侧消费已随 M5b2 WS 桥接交付**（2026-10-04：`cmd/juhe-ai-gateway/chain_realtime.go`——观测器按本节 usage 字段累计会话汇总、受理边界按本节凭据语义实施；场景经 `?scenario=` 查询参数由网关透传驱动）。

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

**M3 已实施 adapter**（`backend-go/projects/gateway/internal/gatewaymedia/veo_video.go`，注册键 `gemini`；api_key 账户 `X-Goog-Api-Key`、google_oauth 账户 Bearer，沿 gemini 主链认权分支）。参数映射（§2.2/§2.4 裁决）：`size` WxH → `aspectRatio`（宽 ≥ 高 `16:9`、反之 `9:16`）+ `resolution`（短边 ≥1080 → `1080p`，否则 `720p`，两档取最近档）；`negative_prompt` → `parameters.negativePrompt` 原生支持；`input_reference` 仅支持 base64/data URL 直传 `instances[0].image.bytesBase64Encoded`（url 形态 400——网关零存储不代为下载）；`seconds`/`n`/`seed`/`audio` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。

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
下载：直连 uri（经网关流式代理，认证按 GCS 签名内嵌，不需要额外凭据；M3 已实施——uri 冻结进 Artifact.ContentURL，`ContentFromArtifact` 形态不经 base_url 拼接）。
取消：`POST /v1beta/{name}:cancel`（上游不支持时网关本地收敛 cancelled）。
计费：按输出秒（目录档）；usage 不回报，且 `seconds` 属 ignored 参数不构成计量基源——网关按 §2.8 兜底（0 计费 + `usage_missing` 标记，不猜测；目录秒价 `VideoOutputUsdPerSecond` 已落，待 Veo 回报时长字段或固定档裁决后生效）。
Mock：`media_gemini_video_create_ok` / `media_gemini_video_poll_running` / `media_gemini_video_poll_done_uri` / `media_gemini_video_poll_error`（M3 已交付，创建场景冻结任务脚本）。

## 6. xai（置信度 B-：模式与字段多源确认，精确端点待官方页回填）

能力与模式事实（2026-10-04 检索升级）：Grok Voice API 提供 TTS/STT/STS；视频为 Grok Imagine Video（`grok-imagine-video` / `grok-imagine-video-1.5`，2026-06 发布），**submit-then-poll 异步任务模式**：POST 创建（请求字段 `model`、`prompt`、源图（图生视频）、`duration`、`aspect_ratio`，最长约 60 秒）→ 返回任务 `id` → GET 轮询 `status`（pending/in-progress → 终态 `succeeded`/`failed`）→ 终态经 `file_id`/视频 URL 下载。
**待回填**：精确端点路径与响应字段全名（`docs.x.ai` Video Generation guide 未被搜索引擎索引）；接入前以官方页回填本节并升 B/A 级。M3 排期时先完成回填再写 adapter。

## 7. glm / 智谱（视频 B 级：报文多源确认；语音 C 级：模型确认字段待回填）

### 7.1 视频（CogVideoX / 清影系，2026-10-04 回填；M3 已实施 adapter）

**M3 已实施**（`backend-go/projects/gateway/internal/gatewaymedia/glm_video.go`，注册键 `glm`；认证 Bearer API Key，沿链上 openai 族认权分支）。参数映射（§2.2/§2.4/§2.7 裁决）：`negative_prompt` 原生直传；`input_reference`→`image_url`（url/base64 字符串均透传，glm 原生即 URL 形态，网关不代为下载）；`seconds`→`duration`（数值取 5s/10s **最近档**换算，等距取小档）；`audio`→`with_audio`；`size` 原样直传（glm 的 size 即 WxH 像素串形态，档位由上游裁决，网关不硬编码）；`n`/`seed` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。出站路径自带 `/api/paas/v4` 服务根，账户 base_url 已含该根时链上去重（`chainGlmVideoUpstreamURL`，不得走 openai /v1 强制补缀归一）。

- 创建：`POST https://open.bigmodel.cn/api/paas/v4/videos/generations`，`Authorization: Bearer <KEY>`。请求：`model`（cogvideox / cogvideox-2 / cogvideox-3 系）、`prompt`、`negative_prompt`、`image_url`（图生视频，承接公共 `input_reference`）、`size`、`duration`（如 5s/10s）、`fps`、`quality`（speed/quality）、`with_audio`（承接公共 `audio`）。响应含 `id`、`request_id`、`task_status`——**受理凭据 = `id`**。
- 轮询：`GET https://open.bigmodel.cn/api/paas/v4/async-result/{id}` → `task_status ∈ PROCESSING | SUCCESS | FAIL`；SUCCESS 返回 `video_result{url, cover_image_url}`（url 即产物下载定位；M3 已实施：url 冻结进 Artifact.ContentURL，`ContentFromArtifact` 直连下载无凭据）。
- 状态归一：PROCESSING→`in_progress`、SUCCESS→`completed`、FAIL→`failed`（§2.6 总表 glm 列以此回填；M3 已实施：FAIL 错误摘要 code 取 task_status 原值，error 对象在场则优先）。
- 取消：glm 无取消 API（回填面无取消端点，M3 裁决）——`SupportsCancel=false`，链上不发上游请求、直接本地收敛 `cancelled`（§2.6 本地终态语义）。
- 计费（M3 落法，不编造）：CogVideoX 按次计费（2026-09 第三方聚合口径称 ¥1/次，官方定价页 bigmodel.cn 为 SPA 无法直接核实精确单价），网关计费面只有秒价维度（`VideoOutputCostPerSecond`）且轮询响应无时长回报——目录行 `cogvideox-3` 不落秒价，终态计费走 §2.8 兜底（0 计费 + `usage_missing` 标记）；官方秒价/时长口径可查证后再补。
- Mock：`media_glm_video_create_ok` / `_poll_processing` / `_poll_success_url` / `_poll_fail`（M3 已交付，端点 `/api/paas/v4/videos/generations`、`/api/paas/v4/async-result/{id}` 与 `/content` 产物通道）。

### 7.2 语音（模型确认，报文待回填）

模型事实（2026-10-04 检索）：`GLM-ASR-2512`（新一代语音识别，实时转写）、`GLM-TTS`（2025-12 发布，两阶段生成，3 秒样本复刻音色）已上线开放平台 API（docs.bigmodel.cn「语音能力」章节）。**待回填**：端点路径、请求/响应字段、音频编码形态、计费单位——接入前以 docs.bigmodel.cn 回填，回填前不得实现 adapter。

既有 `glm` 供应商档案沿用，仅新增媒体 profile 能力（endpoint families + modes），不新增 provider_code。

## 8. minimax（置信度 B：官方文档多源确认；M3 第三批已实施 adapter，2026-10-04）

### 8.1 视频：`POST /v1/video_generation`（host `api.minimax.chat` 或 `api.minimaxi.com`，以账户 base_url 为准）——M3 已实施 adapter（`gatewaymedia/minimax_video.go`；账号接入见《MiniMax账号接入.md》）

请求：`{"model":"MiniMax-Hailuo-2.3","prompt":"...","prompt_optimizer":true,"duration":6,"first_frame_image":"<base64>"}`（`first_frame_image` 承接公共 `input_reference`）。
创建响应：`{"task_id":"...","base_resp":{"status_code":...,"status_msg":"..."}}`——**受理凭据 = `task_id`**；`base_resp.status_code != 0` 视为失败（受理前错误）。
轮询：`GET /v1/query/video_generation?task_id=...` → `{"task_id","status":"Preparing|Queueing|Processing|Success|Fail","file_id":"...","file_download_url":"..."}`。
下载：`GET /v1/files/retrieve?file_id=...`（或 `file_download_url`，以回填为准）。**M3 裁决：取 `file_download_url` 直连**（无凭据，与 Veo uri / glm video_result.url 同族先例；`ContentFromArtifact=true`）。
计费：任务响应用量字段待回填（§2.8）；按官方定价档。M3 已实施口径：官方无可查证精确 USD 秒价（按次/档位计费，定价页为登录态 SPA）且轮询响应不回报时长——不落秒价，终态 0 计费 + `usage_missing`，不编造（依据见 `pricing/data_minimax.go` 注释）。
`provider_options.minimax` 示例：`{"prompt_optimizer":true}`。
取消：无上游取消端点（M3 裁决：`SupportsCancel=false`，不发上游请求直接本地收敛 `cancelled`，沿 glm §7.1 先例）。
Mock：`media_minimax_create_ok` / `_poll_running` / `_poll_success(file_id)` / `_poll_fail(base_resp)`——M3 已交付（`mockupstream/video_minimax.go`）。

### 8.2 TTS / 长转写（t2a_v2 已回填 B 级；M3 已实施 adapter，2026-10-04；长音频待回填）

**同步 TTS：`POST /v1/t2a_v2`**（host `api.minimax.chat` 或 `api.minimaxi.com`，Bearer 认证）。请求：

```json
{
  "model": "speech-02-turbo",
  "text": "<10000 字符；>3000 建议流式>",
  "stream": false,
  "voice_setting": { "voice_id": "...", "speed": 1.0, "vol": 1.0, "pitch": 0 },
  "audio_setting": { "sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1 }
}
```

公共参数映射：`input`→`text`、`voice`→`voice_setting.voice_id`、`speed`（厂商区间 0.5–2.0，公共 0.25–4.0 超区间按 §2.4 规则 400）、`response_format`→`audio_setting.format`（mp3/pcm/flac/wav）；厂商个例（`vol`/`pitch`/情感）走 `provider_options.minimax`。
响应：`data.audio`（**hex 编码**音频字符串——adapter 必须 hex→bytes 解码后透传，不得把 hex 字符串当音频下发）、`data.status`、`extra_info`（时长/占用字符数——TTS 字符计量优先来源）、`trace_id`。
计费：按字符（`extra_info` 回报优先，§2.8）。M3 已实施口径：TTS 字符计量按 `extra_info.usage_characters` 回报优先、缺失回落请求字符自算；官方口径为 ¥2.00/万字符（人民币、无官方美元价），引擎无官方汇率折算链——不落字符价，计量照落、成本不虚计，不编造（依据见 `pricing/data_minimax.go` 注释）。
Mock：`media_minimax_tts_ok`（data.audio hex 载荷，断言解码后 magic bytes）——M3 已交付（`mockupstream/video_minimax.go`；`usage_characters` 刻意 +1 偏离请求字符数，以证明网关回报优先）。

异步长音频（长转写）：端点与字段接入前以 platform.minimax.io 回填；`/v1/audio/jobs` 长音频面首个上游以回填结果定（候选：百炼 paraformer / 火山长转写 / MiniMax）。

## 9. volcengine / 火山方舟（置信度 B：官方格式多源确认；M3 第四批已实施视频 adapter，2026-10-04）

host：`ark.cn-beijing.volces.com`；认证 `Authorization: Bearer <ARK_API_KEY>`。

### 9.1 视频（seedance 系）：`POST /api/v3/contents/generations/tasks`——M3 已实施 adapter（`gatewaymedia/volcengine_video.go`；账号接入见《火山方舟账号接入.md》）

**M3 已实施**（注册键 `volcengine`；api_key Bearer 认证，沿链上 openai 族认权分支）。参数映射（§2.2/§2.4/§2.7 裁决；`resolution`/`ratio`/`duration`/`seed` 参数名与 `content` 数组的 url/base64 双形态已于 2026-10-04 经官方 API 参考页核实）：`prompt` → `content[0]`（`{"type":"text"}`）；`input_reference` → `content` 追加 `{"type":"image_url","image_url":{"url":...}}`（公网 URL 与 Base64 data URL 双形态字符串直传，官方由上游拉取，网关不代为下载——与 minimax 仅 base64 不同）；`size` WxH → `resolution` + `ratio` 档位换算（ratio 约分须精确命中官方词表 16:9/9:16/1:1/4:3/3:4/21:9，词表外本地 400 不近似贴合；resolution 官方词表 480p/720p/1080p，按短边取最近档，合法组合由上游裁决）；`seconds` → `duration` 数值秒直传（官方默认 5）；`seed` 整数直传；`camera_fixed` 等厂商个例经 `provider_options.volcengine` deep-merge；`negative_prompt`/`audio`（`generate_audio` 仅 seedance 2.0/1.5 pro，M3 目录 1.0 pro 不声明）/`n` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。出站路径自带 `/api/v3` 服务根，账户 base_url 已含该根时链上去重（`chainVolcengineVideoUpstreamURL`，不得走 openai /v1 强制补缀归一——同 glm §7.1 先例）。

- 创建：`POST https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks`，请求 `{"model":"doubao-seedance-1-0-pro-250528" 系,"content":[{"type":"text","text":"..."},{"type":"image_url","image_url":{"url":"..."}}],"resolution":"720p","ratio":"16:9","duration":5,"seed":42,...}`。创建响应：`{"id":"cgt-...","status":"queued",...}`——**受理凭据 = `id`**（任务记录保留 7 天）。
- 轮询：`GET /api/v3/contents/generations/tasks/{id}` → `status ∈ queued|running|succeeded|failed`；成功 `content.video_url`（时效内下载 URL；M3 已实施：冻结进 Artifact.ContentURL，直连下载无凭据）；失败 `error{code,message}`（M3 已实施：错误摘要 code/message 透传，缺席以 status 原值兜底）；官方回调面另有 `expired` 等终态词，轮询面未回填——未知状态归 `in_progress` 记 RawStatus（§2.6 归一规则）。部分任务含 `usage`（字段名接入回填）。
- 下载：`video_url` 直连流式代理（M3 已实施：`ContentFromArtifact=true`）。
- 计费：seedance 按秒 × 分辨率档；`usage` 回报优先。M3 已实施口径：官方计费口径为人民币（按 token 或按秒×档）且轮询响应 usage/duration 字段未回填——不落秒价、不抽秒，终态 0 计费 + `usage_missing`，不编造（依据见 `pricing/data_volcengine.go` 注释；字段回填后再启用）。
- `provider_options.volcengine` 示例：`{"resolution":"720p","ratio":"16:9","camera_fixed":true}`（以官方参数名为准）。
- 取消：§9.1 回填面无取消端点（M3 裁决：`SupportsCancel=false`，不发上游请求直接本地收敛 `cancelled`，沿 glm/minimax 先例；官方回调文档提及任务存在 cancelled 终态、仅排队中任务可取消，取消端点回填后升级）。
- Mock：`media_volcengine_create_ok` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error`——M3 第四批已交付（`mockupstream/video_volcengine.go`，端点 `/api/v3/contents/generations/tasks` 族与 `/content` 产物通道）。

### 9.2 TTS / 长转写

豆包 TTS（`/api/v3/tts` 族）与异步长转写：端点与字段接入前以火山官方文档回填；计费 TTS 按字符、转写按时长。**M3 第四批未实施**（档案与目录不声明 audio 能力）。

## 10. qwen / 通义百炼（置信度 B：DashScope 通用任务模式确认；§10.1 端点与参数面已于 2026-10-04 经百炼官方 legacy 万相 API 参考核实并实施）

host：`dashscope.aliyuncs.com`；兼容模式认证 `Authorization: Bearer <DASHSCOPE_API_KEY>`；**异步任务约定：创建请求带 `X-DashScope-Async: enable` 头**（缺失上游报 "current user api does not support synchronous calls"）。

### 10.1 视频（万相 wanx / wan 系）——**M3 第五批已实施 adapter**（`gatewaymedia/qwen_video.go`，2026-10-04）

创建：`POST /api/v1/services/aigc/video-generation/video-synthesis`（端点已定稿：百炼 legacy 万相 API 参考，wan2.1～2.6 系走本端点；wan2.7 新版协议同为 `/api/v1/services/aigc/` 前缀，不采用 `/api/v1/videos` 形态）+ `X-DashScope-Async: enable`，body `{model,input:{prompt,negative_prompt?,img_url?},parameters:{size?,duration?,seed?}}` → `{"output":{"task_id":"...","task_status":"PENDING"},"request_id":"..."}`——**受理凭据 = `output.task_id`**（uuid 形态）。
轮询：`GET /api/v1/tasks/{task_id}` → `{"output":{"task_status":"PENDING|RUNNING|SUCCEEDED|FAILED","video_url":"..."},"usage":"<原始 JSON 字符串>","code","message"}`（DashScope 的 `usage` 为 JSON 字符串形态——字符串内再嵌一层 JSON；官方文档示例亦展示对象形态，adapter 双形态兼容解析）。
下载：`video_url` 直连（官方为 OSS 签名地址、约 24 小时时效，凭据内嵌）。
计费：万相按次/时长人民币口径，无可查证 USD 秒价——M3 目录不落秒价；`usage` 计量照抽（wan2.5 及以下字段族 `video_duration`/`video_ratio`、wan2.6 字段族 `duration`/`output_video_duration`/`SR`/`size`/`video_count`，adapter 按 `video_duration` → `output_video_duration` → `duration` 顺序取第一个正值），成本 0 不虚计（§2.8）。

M3 第五批实施面（保守面，检索核实）：
- **参数**：`negative_prompt` → `input.negative_prompt`（官方示例章节"所有模型"）；`seed` → `parameters.seed`（可选整数 [0, 2147483647]）；`size` WxH → `parameters.size` "W\*H" 星号形态（档位合法性由上游裁决，不硬编码词表）；`seconds` → `parameters.duration` 数值直传（模型级档位由上游裁决：wan2.2-t2v-plus 固定 5 秒、wan2.5 取 5/10、wan2.6 取 [2,15]）；`input_reference` → `input.img_url`（图生视频形态同端点，公网 URL 与 Base64 data URL 双形态直传）；`n`/`audio` 请求面无对应（`input.audio_url` 是 wan2.6/2.5 的输入素材字段非生成开关，经 L3 provider_options 传递）→ `params_ignored` 回显。
- **取消**：§10.1 面无取消端点（官方词表有 CANCELED 终态词但未回填可调用 API）→ `SupportsCancel=false` 本地收敛 `cancelled`；CANCELED/UNKNOWN 轮询值按 §2.6 归一规则记 `RawStatus` 归 `in_progress`（不猜测失败，同 volcengine `expired` 先例）。
- **TTS 不承接**：CosyVoice/长转写面（§10.2）未回填，档案与目录不声明 audio 能力。

### 10.2 TTS（CosyVoice 系，待回填）/ 长转写（paraformer 系，B 级已回填 2026-10-04；M3f 已实施）

**长转写（`/v1/audio/jobs` 首个上游）——M3f 已实施（2026-10-04，`gatewaymedia/qwen_asr.go`，对外端点族与任务面泛化见链上 `chain_media_video.go`/`chain_driver.go`；usage 时长字段钉 `duration` 秒（JSON 字符串/对象双形态兼容解析，沿 §10.1 先例）；创建无 X-DashScope-Async 头（天然异步，adapter ExtraHeaders 恒 nil）；取消面无 API 回填 → SupportsCancel=false 本地收敛 cancelled）**：
- 创建：`POST https://dashscope.aliyuncs.com/api/v1/services/audio/asr/transcription`，Bearer，**无需 X-DashScope-Async 头**（该服务天然异步）。请求：`{"model":"paraformer-v2","input":{"file_urls":["<公网音频 URL>"]},"parameters":{"language_hints":[...],"disfluency_removal_enabled":true,"diarization_enabled":true,"vocabulary_id":"..."}}`——**只接受公网 URL 数组，不支持文件上传**（与零存储对齐：对外 `/v1/audio/jobs` 首版输入只支持 `input_url`，multipart 文件输入待有文件上传型上游再开）。响应：`{"output":{"task_id":"...","task_status":"PENDING"},"request_id":"..."}`——受理凭据=`output.task_id`。
- 轮询：`GET /api/v1/tasks/{task_id}`（与万相视频同一 DashScope 任务接口）：`PENDING|RUNNING|SUCCEEDED|FAILED`；SUCCEEDED → `output.results[].transcription_url`（**转写结果 JSON 文件的下载链接**，内容含 `transcripts[].text`/句级时间戳/说话人——content 端点流式代理该 URL，产物为 JSON 非 audio）；FAILED → `output.message`；`usage`（JSON 字符串，时长计量）。
- 状态归一与万相同表（§2.6 qwen 列）；计费：paraformer 按时长人民币口径——无可查证 USD 不落价（0 计费+usage_missing 不编造），usage 时长计量照抽（`AudioInputSeconds`）。
- Mock：`media_qwen_asr_create_pending` / `_poll_running` / `_poll_succeeded_transcription_url` / `_poll_failed`——**M3f 已交付**（`mockupstream/asr_qwen.go`，创建端点按契约 §10.2 校验 model + input.file_urls 非空数组（缺失 400，参数错误透传断言点）；轮询/产物通道与万相共用 `/api/v1/tasks/{id}` 族（任务表按 id 分表查询），content 回转写结果 JSON 文件）。

CosyVoice TTS 同步/流式（按字符）：端点字段仍待官方页回填（C 级）。
Mock（视频四场景 **M3 第五批已交付**，`mockupstream/video_qwen.go`——创建端点按契约 §10.1 校验 `X-DashScope-Async` 头，缺失即 400 同步调用拒绝；`_tts_ok` 随 TTS 接入交付）。

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
- 百炼：DashScope 异步任务约定（`X-DashScope-Async`、`GET /api/v1/tasks/{task_id}`、PENDING/RUNNING/SUCCEEDED/FAILED）。M3 第五批接入时经百炼官方 legacy 万相文生视频/图生视频 API 参考补全并实施：创建端点 `/api/v1/services/aigc/video-generation/video-synthesis`（无异步头时上游报 "current user api does not support synchronous calls"）、`input.prompt/negative_prompt/img_url`（图生视频公网 URL 与 Base64 data URL 双形态）、`parameters.size`（`1920*1080` 星号形态）/`duration`/`seed`、轮询 usage 字符串/对象双形态与 `video_duration`（wan2.5 及以下）/`duration`/`output_video_duration`（wan2.6）字段族、wan2.2-t2v-plus 档位（480P/1080P 全档、duration 固定 5 秒）。
- xAI：Grok API 定价页与评测（Voice API 三能力、grok-imagine 6/10s 480p/720p）。
- 智谱：社区与聚合层证据（CogVideoX 异步任务、ASR 流式）；官方文档搜索引擎收录有限，列为 C 级回填。
