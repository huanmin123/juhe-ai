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

### 2.2 视频创建参数表（`POST /v1/video/generations`，2026-11-12 New API 兼容面）

对外字段按 New API；内部先归一为 `seconds` / `size` / `input_reference` 再交给各厂商 adapter（adapter 映射见 §2.7）。

| 参数 | 层 | 类型 / 值域 | 厂商不支持时 |
| --- | --- | --- | --- |
| `model` | L1 | string（模型目录路由） | —— |
| `prompt` | L1 | string | —— |
| `duration` | L2 | number（秒）；归一为内部 `seconds`；厂商固定档时取档并回显 `params_applied` 实际值 | 用厂商默认时长并回显 |
| `width` + `height` | L2 | int；归一为内部 `size` 像素串 `"WxH"`（缺一不可，单边缺失忽略并回显） | 取厂商默认并回显 |
| `image` | L2 | 首帧图：URL 或 base64 data URL；归一为内部 `input_reference` | 无图生视频 → **400**（能力缺失不静默） |
| `seed` | L2 | int（可复现） | 忽略 + 回显 `params_ignored` |
| `n` | L2 | int ≥1，默认 1 | `n>1` 且厂商不支持 → **400** |
| `fps` | L2 | int；当前无统一语义 | 进 `metadata` 回显 + `params_ignored` |
| `response_format` | L2 | `mp4` 等；当前无统一语义 | 进 `metadata` 回显 + `params_ignored` |
| `user` | L2 | string；仅标识 | 进 `metadata` 回显 + `params_ignored` |
| `metadata` | L3 | JSON object（唯一扩展通道）：已知 L2 键（`negative_prompt` / `audio` / `seed`）提升为公共参数，其余键在选择账户后并入该厂商 `provider_options` 子对象 deep-merge；原样随任务快照冻结并回显 | —— |

规则：

1. 对外公开词表为 New API 字段（`duration` / `width`+`height` / `image`）；旧 OpenAI 字段名 `seconds` / `size` / `input_reference` 按别名容忍（映射进同一内部归一，params 回显按实际生效键）；`provider_options` 出现即 400（旧 `/v1/videos` 面已 404）；内部归一后各 adapter 仍按 §2.7 映射。
2. 创建响应 `{id, task_id, status:"queued", object:"video", model, created_at, metadata}`（长音频 object 为 `"task"`）；查询响应同时给扁平字段与 `{code:"success",message:"",data:{...}}` 包裹，`data` 内为 `task_id/status/url/format/metadata/error`。
3. `status` 为 `queued/in_progress/completed/failed`；`cancelled`/`expired` 归一 `failed`。`url` 优先上游产物直链，缺失且 completed 时回退网关 content 端点：配置 `JUHE_AI_GATEWAY_PUBLIC_BASE_URL` 时为绝对地址（New API `BuildProxyURL` 的 `ServerAddress` 同构），未配置为相对路径（长音频为 `/v1/tasks/audio/{task_id}/content`）。
4. `metadata` 回显 = 请求原值 + 诊断键 `provider` / `provider_job_id` / `params_applied` / `params_ignored` / `provider_options_applied`（只键名，不含资源字节）。

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
5. **L3 计费字段封禁（2026-11-13 T219 收口）**：`metadata`（视频/图像的唯一 L3 通道）中会改变计费数量/时长/分辨率/产物输入的键一律本地 400，不静默丢弃——视频：`duration`/`durationSeconds`/`seconds`/`resolution`/`size`/`ratio`/`sampleCount`/`n`/`content`/`image`/`images`/`video`/`video_url`/`first_frame_image`/`first_frame`/`last_frame`/`reference_image`/`reference_video`/`audio_url`（嵌套层同样扫描）；图像：`size`（`image`/`images` 是契约 §9.3 允许的参考图通道，其张数由响应侧请求计数覆盖 `metadata.image/images` 纳入输入图计费）。计费基源只按顶层 `seconds`/`size`/`n`/`input_reference`/`image` 冻结。
6. job 对象固定含 `params_applied` 与 `params_ignored` 两个字段（同步响应同样适用）。

### 2.5 规范演进策略

骨架（端点形态、job 语义、L1 词表）对齐 OpenAI videos/audio——与 OpenRouter/LiteLLM 的聚合层选择一致；自有扩展（`provider_options`、`params_applied/params_ignored` 回显、`seed`/`audio`/`negative_prompt` 归一）保持最小集。未来行业标准落地（OpenAI videos 全面普及或新标准出现）时：只调整 L2 词表与 §2.7 映射表，端点骨架与 L1 不动，`provider_options` 原样保留。

### 2.6 MediaJobIR 状态归一总表（厂商状态 → 统一 status）

| 统一 status | 语义 | openai | gemini(veo) | minimax | volcengine | qwen(dashscope) | glm | xai |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `queued` | 已受理未开始 | `queued` | operation 未 `done`（Veo 无进展字段，M3 adapter 归一：未 done 恒 queued） | `Preparing` / `Queueing` | `queued` | `PENDING`（M3 已实施） | （不可达：创建响应即 `PROCESSING`，M3 已实施） |
| `in_progress` | 生成中 | `in_progress`（含 `progress`） | （不可达：无进展字段，见 queued 行） | `Processing` | `running` | `RUNNING`（M3 已实施） | `PROCESSING`（M3 已实施） | （不可达：创建响应仅 `request_id` 无 `status`，M3 已实施空归一 in_progress） |
| `completed` | 成功 | `completed`（`content` 有下载定位） | `done:true` 且 `response.generateVideoResponse.generatedSamples[].video.uri` 存在（M3 已实施：uri 冻结进 Artifact.ContentURL，直连下载无凭据） | `Success`（`file_id`/`file_download_url`） | `succeeded`（`content.video_url`） | `SUCCEEDED`（`output.video_url`，M3 已实施：url 冻结进 Artifact.ContentURL，直连下载无凭据） | `SUCCESS` 且 `video_result.url` 存在（M3 已实施：url 冻结进 Artifact.ContentURL，直连下载无凭据） | `done` 且 `video.url` 存在（M3 已实施：url 冻结 Artifact.ContentURL 无凭据直连；`video.duration` 秒计量照抽） |
| `failed` | 失败 | `failed`（`error`） | `done:true` 且 `error` 非空（M3 已实施：error 摘要 code 取 status） | `Fail`（`base_resp`） | `failed`（`error`） | `FAILED`（顶层 `message`/`code`，M3 已实施） | `FAIL`（M3 已实施：错误摘要 code 取 task_status 原值，error 对象在场则优先） | `failed`（`error.code`/`error.message`，M3 已实施） |
| `cancelled` / `expired` | 本地终态 | （上游删除后查询 404 → 本地收敛） | 同左（`:cancel` 上游 2xx/404/405 均收敛本地 cancelled，M3 裁决） | 同左（minimax 无取消 API：不发上游请求直接本地收敛，M3 裁决，§8.1） | 同左 | 同左（qwen 无取消 API：不发上游请求直接本地收敛，M3 裁决，§10.1） | 同左（glm 无取消 API：不发上游请求直接本地收敛，M3 裁决，§7.1） | `expired` 为上游原生终态直接收敛（M3 已实施，§6.1）；cancelled：无取消 API 不发上游请求本地收敛 |

归一规则：未知状态值一律归 `in_progress` 并记录原始值（不猜测失败）；上游 404 查询且本地非终态 → 保持本地状态直至 TTL 过期（不得伪造终态）。volcengine 长转写臂（§9.2，2026-10-17 承接批）：头驱动协议无状态词——`X-Api-Status-Code` `20000001`/`20000002` → `in_progress`、`20000000` → `completed`、`450000xx` → `failed`，未知码归 `in_progress` 记 RawStatus。

### 2.7 公共参数 → 厂商字段映射表（L1/L2 的派生事实源）

| 公共参数 | openai | gemini tts | minimax 视频 | volcengine 视频 | qwen 视频 | xai 视频 | 说明 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `model` | 同名 | URL 路径段（`:generateContent` 前） | `model` | `model` | URL/model 字段 | `model`（同名） | — |
| `prompt` | 同名 | `contents[].parts[].text` | `prompt` | `content[].text` | `input.prompt` 等 | `prompt`（同名） | — |
| `seconds` | `seconds`（响应回显 `seconds_length`） | —— | `duration` | `duration`（数值秒直传，2026-10-04 官方核实；M3 已实施） | `parameters.duration`（数值秒直传，2026-10-04 官方核实；M3 已实施——模型级档位由上游裁决） | `duration`（整数秒直传，1–15 区间由上游裁决，M3 已实施） | 无对应者的厂商按其默认时长，不做换算猜测 |
| `size` | `size`（如 `1280x720`） | `parameters.aspectRatio`（`16:9`/`9:16`） | —— | `resolution` + `ratio` 档（M3 已实施：ratio 约分精确命中官方词表，词表外 400；resolution 短边最近档） | `parameters.size` W\*H 星号形态（M3 已实施：WxH→W\*H 纯格式转换，档位由上游裁决） | `aspect_ratio` + `resolution` 档（M3 已实施：ratio 约分精确命中七值词表，词表外 400；resolution 短边最近档——同 volcengine 形态） | 分辨率→宽高比/档位换算写入 adapter（gemini、volcengine、xai） |
| `n` | `n` | —— | —— | —— | —— | 多数厂商固定 1；请求 `n>1` 且厂商不支持时创建即 400 | `——`（请求面无 n 字段） |
| `input_reference`（图生视频首帧） | `input_reference`（multipart 文件） | `instances[].image.bytesBase64Encoded` | `first_frame_image`（base64） | `content[].image_url`（url/base64 双形态直传，M3 已实施） | `input.img_url`（url/base64 双形态直传，M3 已实施——图生视频形态同端点） | `image`（url/base64 双形态直传，M3 已实施——图生视频首帧） | 网关只透传不解析存储 |
| `voice`（TTS） | `voice` | `generationConfig.speechConfig.prebuiltVoiceConfig.voiceName` | （TTS 章） | （TTS 章） | （TTS 章） | （TTS 章，§6.2 待回填） | — |

### 2.8 usage / 计量来源表（谁出数字）

| 厂商 | TTS | STT | 视频 | 说明 |
| --- | --- | --- | --- | --- |
| openai | 双口径（M1 已实施，按厂商真实定价维度）：`gpt-4o-mini-tts` 官方按 **token 口径**（`audioInputUsdPer1M`/`audioOutputUsdPer1M`，seed 已填价），但二进制响应无 usage、网关不解析音频 → **按次固定价 $0.3/次兜底**（2026-11-12 用户裁决：按次固定价，不空着；价格参考 new-api `defaultModelPrice` 同口径，非 OpenAI 官方按次价；`usage_missing` 仍置位记录上游无 usage 事实，行项 `tts_output_calls`）；`gpt-4o-tts` 未收录（官方无定价，待官方价后补行，不编造）；上游无 usage 回报的 TTS 模型（原 `tts-1`/`tts-1-hd` 已随 2025 前模型清理批移除；现役例 minimax speech 系）**网关自算**按请求 `input` 字符数（行项 `tts_input_chars`） | 双口径：`gpt-4o-transcribe` 系按 **token 口径**（`usage.input_tokens`/`output_tokens`，同一对 audio token 单价）；按 `duration` 秒的 STT 模型（原 `whisper-1` 已随 2025 前模型清理批移除；现役例 minimax asr-1.0；行项 `audio_input_seconds`）；usage 优先 → duration → 0 + `usage_missing` 标记，不猜测 | 轮询 `seconds_length` 回报优先；无回报按生效请求参数 `seconds` 或厂商默认时长基源（§2.8.2）× 档位秒价；失败任务不虚计 | — |
| gemini | 对话式 token 计量（`usageMetadata`，已有链路） | 同左（audio token） | 按输出秒（目录价格档）；Veo 系轮询无 usage 回报，`seconds` 属 ignored 参数不构成基源，改用 adapter 声明的厂商默认 8 秒基源（§2.8.2/§5.2）× 秒价 | — |
| minimax | 按字符（M3 已实施：`extra_info.usage_characters` 回报优先 → `tts_input_chars`，缺失回落请求字符自算；官方口径 ¥2.00/万字符人民币、无官方美元价 → 目录不落字符价，计量照落成本不虚计） | 待回填（长转写未接入） | **按次计费**（§2.8.1，2026-10-05 按次计费批已实施）：目录行 `VideoOutputCostPerCall` 落国际站按条明文主档 768P-6s $0.28/条（分档注释登记）；轮询响应不回报时长 → 一次任务 = 一次调用，成本 = 次价 × 1，usage 行不落秒计量 | — |
| volcengine | 按时长（2026-10-17 维度修正批）：官方计费维度=**生成原始时长**（计费文档明文"按模型生成的原始时长计费，倍速不影响计费时长"，精确至秒）——响应顶层 `duration` 秒回报 → `audio_input_seconds`（音频秒行项，费率 `audioInputUsdPerSecond` 按模型落值），**不再落字符口径**（字符非官方计费维度）；duration 缺失/非正值 → 0 计量 + `usage_missing` 标记，不猜测；单价已落值（2026-10-17 浏览器渲染抓取豆包语音计费页明文）：后付费 1 元/分钟（按时长计费、精确至秒、倍速不影响）÷ 60 ÷ 约定汇率 7.0 → $0.00238095/秒落 audioInputCostPerSecond；落值锚点测试 TestVolcengineCatalogPriceAnchors 钉住 | 长转写按时长（**2026-10-17 承接批已实施**：录音文件识别 2.0 终态 `audio_info.duration` **毫秒** → ÷1000 秒落 `audio_input_seconds`，usage_source=upstream；缺失/非正值 → 0 计量 + `usage_missing` 不猜测；秒价已落值 0.8 元/小时 ÷3600 ÷7.0 = $3.1746031746031745e-05/秒，见 §9.2） | **2026-10-17 实测回填批**：轮询 succeeded 顶层 `duration` 正值 → 秒计量回报优先（`OutputVideoSeconds`，usage_source=upstream）；无秒可抽（缺/0 值）且创建请求 `seconds` 生效时按请求参数自算（§2.8.2）；2026-10-17 国内价口径批：720p 主档 USD 秒价已落值（国内元/秒 ÷ 7.0，与 seedream 图像行同口径——账户为国内方舟账单人民币）：2.5 $0.2157（1.51 元/s）、2.0 $0.1414（0.99 元/s）、fast $0.1143（0.80 元/s）、mini $0.0714（0.50 元/s）；1-0-pro 无官方每秒示例价不落值（token 牌价注记留档：国内 15.00 元/百万 token 在线、7.50 离线）。官方计费=token 单价×usage.completion_tokens（分档随分辨率/是否含输入视频），引擎视频行只消费秒价，分档差价随专门计价批；字段全貌见 §9.1 | — |
| qwen | CosyVoice 按字符（回填） | paraformer 按时长（回填） | `usage` JSON 字符串字段族（`video_duration`/`output_video_duration`/`duration`）秒计量回报优先；无秒可抽且请求 `seconds` 生效时按请求参数自算（§2.8.2）；目录已落 USD 秒价（2026-10-05 补价：wan2.2-t2v-plus $0.1/s 等）按秒计费 | — |
| glm | 回填 | 回填 | **按次计费**（§2.8.1，2026-10-05 按次计费批已实施）：目录行 `VideoOutputCostPerCall`——cogvideox-3 $0.2/次（z.ai USD 明文）、cogvideox-2 $0.0714/次（国内 0.5 元 ÷ 7.0 换算）；轮询响应不回报时长（§7.1）→ 一次任务 = 一次调用，成本 = 次价 × 1，usage 行不落秒计量 | — |
| xai | 待回填（§6.2 语音面未回填） | 待回填（§6.2） | done 的 `video.duration` 秒计量回报优先；无秒可抽且请求 `seconds` 生效时按请求参数自算（§2.8.2）；目录已落 USD 秒价（grok-imagine-video-1.5 三档明文主档取 720p $0.14/s，2026-10-05 核对批回正）按秒计费（§6.1） | — |

规则（2026-10-05 按次计费 + 请求参数自算批修订）：

1. 上游回报 usage 优先。
2. 上游不回报的维度网关按**生效**请求参数自算（见 §2.8.2；ignored 参数不构成计量基源）。
3. 按次上游按 §2.8.1 计费，一次任务 = 一次调用，不依赖秒计量。
4. 两者都缺 → 0 计费 + 记录 `usage_missing` 标记，**不猜测**。

#### 2.8.1 视频按次计费（VideoOutputCostPerCall，2026-10-05 用户裁决；2026-11-12 时长档批）

- **裁决**：无法返回 usage 的上游按官网计费规则、按输入计。cogvideox/Hailuo 等上游不返回时长 usage，但官方按次/按条计费且创建请求携带时长参数（快照已冻结）→ 落按次单价，按任务粒度计费。
- **槽位**：静态目录 `rawModel.VideoOutputCostPerCall` / `PriceSet.VideoOutputUsdPerCall`（USD/次字面量），与 `OutputCostPerImage`（按张=按次语义）同构的按次单槽位；目录表（provider_model_catalog）无按次列，只落 gateway 静态层（同秒价先例，计费真实读取处）。
- **时长档表（2026-11-12）**：厂商按“条 × 时长档”定价时，用 `rawModel.VideoOutputCostPerCallBySeconds` / `PriceSet.VideoOutputUsdPerCallBySeconds`（键=秒数串 "6"/"10"，值=USD/条）表达；`VideoOutputUsdPerCall` 保留为默认/主档价。创建时按最近档收敛请求 `seconds`（等距取小档，`pricing.VideoCallTierSeconds`），出站报文与 `billingSeconds` 同步用收敛后的档位；结算按 `billingSeconds` 选档。档位表存在但秒数不命中 → 不产 `video_output_calls` 行项，不回退主档（宁可不计，不按错档收费）。
- **分辨率×时长组合档（2026-11-13 Hailuo 1080P 批）**：厂商按“分辨率 × 时长”组合定价时，用 `rawModel.VideoOutputCostPerCallByResolutionSeconds` / `PriceSet.VideoOutputUsdPerCallByResolutionSeconds`（键 `"1080p-6"`）表达；创建时 adapter 冻结 `billingResolution`（Hailuo size 短边 ≥900→1080P，其余 768P），结算先按组合键选档，未命中回落 `BySeconds` 主档；已知分辨率有专门组合档但该时长组合不存在（如 1080P-10s）→ 不产行项。Hailuo 2.3 v1 出站显式发送 `resolution`，实际产出与计费档一致。
- **互斥门禁**：同一目录行 `VideoOutputCostPerSecond` 与 `VideoOutputCostPerCall` 只落其一（按次 ≠ 按秒；门禁测试 `TestVideoCallSecondPriceMutualExclusion` 遍历全部快照行钉住）——同行双价会让秒/次两条结算路径对同一任务重复计费。
- **结算语义**：任务终态 completed 且目录行有按次价 → 成本 = 档位价（按 `billingSeconds`）× 1（一次任务 = 一次调用），计费行项 `video_output_calls`（unit=call）；usage 行**不落** `outputVideoSeconds`（按次上游无秒计量概念）、不打 `usage_missing`（有计量计费依据）。failed 仍不虚计；cancelled/expired 仍不产生 usage 行。
- **按次上游清单（当前）**：glm `cogvideox-3`（$0.2/次，z.ai USD 明文）、`cogvideox-2`（$0.0714/次，国内 0.5 元/次 ÷ 7.0 换算）、minimax `MiniMax-Hailuo-2.3`（768P-6s 2.00 元/条、768P-10s 4.00 元/条、1080P-6s 3.50 元/条，÷7.0 → $0.2857/$0.5714/$0.5）、`MiniMax-Hailuo-2.3-Fast`（1.35/2.25/1080P-6s 2.31 元/条 ÷7.0 → $0.1929/$0.3214/$0.33）；来源 `platform.minimax.cn/docs/pricing/overview`（2026-11-12 核对、2026-11-13 复核，人民币计价）。seedance-1-0-pro 维持无价不动。

#### 2.8.2 视频计费秒数基源（请求值 / 厂商默认时长，2026-10-05 用户裁决；2026-11-12 默认时长批）

- **规则**：任务终态 completed 且上游无 usage 回报（IR `OutputVideoSeconds` 为空）时，按创建时冻结的**计费秒数基源**（`request_snapshot_json.billingSeconds`）结算：请求 `seconds` 生效则用请求值；否则用 adapter 声明的厂商默认输出时长（`VideoCapabilities.DefaultSeconds`）。上游回报在场时回报优先，基源不覆盖。
- **来源标注**：usage_records/IR 无计量来源字段——「计量来源 = 创建时冻结基源（请求值或厂商默认）」以结算链注释与链级测试断言语义钉住，**不加 schema 列**。`billingSeconds` 是快照内的计费事实，与请求回显 `seconds` 分开保存。
- **厂商默认输出时长（当前）**：Veo 3 系 8 秒（`SupportsSeconds=false`，请求面不接收 seconds）；minimax 6 秒（请求未携带 duration）；volcengine 5 秒（官方默认）。这些默认值是厂商请求面的既定行为，不是对上游响应的猜测；未声明默认值的厂商仍维持 `usage_missing`。
- **ignored 不构成计量基源**：`seconds` 属 ignored 参数（如 Veo）时用户值未生效，网关**不按 ignored 请求值**计费，改按厂商默认时长基源计费；未声明默认值的 adapter 才落 `usage_missing`。
- **无价兜底**：基源在场但目录无 USD 秒价（如 seedance 1.0 pro）→ 计量照落、成本 0 不虚计；真正无任何依据（无回报且无基源）才 `usage_missing`。
- **不做跨维度换算、不解析产物字节**：2026-11-12 用户裁决——统一的是最终美元成本与行项，不是计量单位；各家按自己的计费维度和目录价结算（按秒、按次、按张、按 token 各用各的），网关不把按次折成秒、也不解析视频/音频字节反推时长。

#### 2.8.3 视频秒价分辨率档（2026-11-12 分辨率档批）

- **规则**：目录行用 `rawModel.VideoOutputCostPerSecondByResolution` / `PriceSet.VideoOutputUsdPerSecondByResolution`（键 "480p"/"720p"/"768p"/"1080p"/"2k"/"4k"，大小写不敏感）表达按输出分辨率分档的秒价；`VideoOutputUsdPerSecond` 仍是主档价。
- **冻结**：创建时由 adapter 的可选接口 `VideoBillingResolutionProvider.BillingResolution(params)` 给出本次计费档，随 `request_snapshot_json.billingResolution` 冻结；未实现该接口或无法确定（空串）→ 结算用主档价。
- **结算**：终态 completed 且 `billingResolution` 非空 → 按该档秒价 × 输出秒；档位表存在但档位不命中 → 不产 `video_output_seconds` 行项（不回退主档，宁可不计不按错档收）。
- **选档同源**：xAI/Volcengine/Qwen 的 `BillingResolution` 与出站 resolution 换算同源（短边 ≥900→1080p、≥600→720p、其余 480p）；xAI 未传 size 时官方默认 480p，Volcengine/Qwen 未传 size 时返回空串（回落主档）。minimax H3 走 v2 官方词表：H3 短边 ≥1440→2K、其余 768P，H3-Max 短边 ≥720→768P、其余 480P；未传 size 返回主档 768P（2026-11-13 H3 批）。
- **当前档位价（官方，2026-11-12 核对；minimax 2026-11-13）**：
  - xAI `docs.x.ai/docs/pricing`（USD）：`grok-imagine-video-1.5` 480p/720p/1080p = $0.08/$0.14/$0.25 每秒；lite = $0.02/$0.03/$0.14；classic = $0.05/$0.07（无 1080p）。
  - Volcengine `docs.volcengine.com/docs/82379/1099320`（CNY，÷7.0）：seedance-2.5 = 0.67/1.51/3.74 元/s；2.0 = 0.46/0.99/2.48；2.0-fast = 0.37/0.80（无 1080p）；2.0-mini = 0.23/0.50（无 1080p）。
  - Qwen/Alibaba（CNY，÷7.0）：wan3.0-video = 0.3/0.6/1.2 元/s；wan2.7-t2v = 720P/1080P 0.6/1.0；wan2.2-t2v-plus = 480P/1080P 0.14/0.70。
  - minimax `platform.minimax.cn/docs/pricing/overview`（CNY，÷7.0）：H3 768P/2K = 0.50/0.80 元/s；H3-Max 480P/768P = 0.33/0.50 元/s。

### 2.9 图像参数表（`POST /v1/images/generations`，2026-11-11 同步批；`POST /v1/images/edits`，2026-11-12 T216/P0）

| 参数 | 层 | 类型 / 值域 | 厂商不支持时 |
| --- | --- | --- | --- |
| `model` / `prompt` | L1 | string；prompt 必填 | 上游错误透传 |
| `n` | L2 | int ≥1，默认 1 | 语义无法表达 → 400 |
| `size` | L2 | `1024x1024` / `1536x1024` / `1024x1536` / `auto`，或厂商档位/WxH | 取厂商默认并回显；qwen 的 `auto` 忽略并进 `params_ignored.size` |
| `quality` | L2 | `low/medium/high/xhigh/max/auto`（gpt 词表） | 忽略 + `params_ignored` |
| `background` | L2 | `transparent/opaque/auto` | 忽略 + `params_ignored` |
| `output_format` | L2 | `png/jpeg/webp` | 忽略 + `params_ignored` |
| `output_compression` | L2 | int 0-100 | 忽略 + `params_ignored` |
| `response_format` | L2（legacy） | `url/b64_json` | 忽略 + `params_ignored`（qwen 只回 URL） |
| `stream` / `partial_images` | L2 | 图像统一契约不支持 stream（2026-11-12 用户裁决） | 四家全部接受但忽略 + `params_ignored`，上游请求不携带，统一返回普通 JSON |
| `safety_identifier` | L2 | string（OpenAI 新字段） | 忽略 + `params_ignored` |
| `metadata` | L3 | JSON object；链上投影为该厂商 `provider_options` 子对象 deep-merge（编辑面不接管 `image`/`images`；生成面 L3 命中按各 adapter 既有装配规则处理）；公开 `provider_options` 出现即 400 | —— |

规则：

1. 图像同步响应统一 OpenAI Images `{created, data:[{url|b64_json}], usage?}`；**响应宽容**：`data[]` 至少一项有 `url`/`b64_json` 即可用，不校验 `model`/厂商/`mime_type`，缺 `created`/`model`/`usage` 不拦截。
2. 回显走响应头 `X-Juhe-Image-Provider` / `X-Juhe-Image-Params-Applied` / `X-Juhe-Image-Params-Ignored` / `X-Juhe-Image-Provider-Options-Applied`（逗号分隔、字典序、只键名），不写 body——保持 gpt `b64_json` 大响应纯透传。
3. usage 统一字段 `output_image_count`：xAI `data[]` 长度，火山 `usage.generated_images`，Qwen 以转换后实际 `data[]` 图片数为准（2.0 `usage.image_count` / 3.0 `usage.output_image_count` 仅作形状兼容提示），gpt 现有图像 token/张数策略。
4. 计费跟随模型行：`OutputUsdPerImage` 按张；`ImageOutputUsdPer1M` 按图像 token。不因响应 `model` 与配置模型不一致改价。
5. **按输出像素分档（2026-11-12 档位批）**：目录行 `rawModel.OutputCostPerImageTiers` / `PriceSet.OutputUsdPerImageTiers`（`{maxPixels, usdPerImage}`，`maxPixels=0` 表示无上限）；结算用请求 `size` W×H 的像素数（usage `OutputImagePixels`）选第一个 `pixels ≤ maxPixels` 档；像素未知回落主档 `OutputUsdPerImage`；像素已知但无档命中 → 不产按张行项（不回退主档）。当前落值：Volcengine Seedream 5.0 Pro ≤2.61M 像素 0.30 元/张、>2.61M 0.60 元/张（÷7.0，来源 `docs.volcengine.com/docs/82379/1544106`，2026-11-12 核对）；Qwen `qwen-image-3.0-pro` ≤1024×1024 像素 0.25 元/张、>1024×1024 0.50 元/张（÷7.0；官方只标 1k/2k 与最大 2048×2048，未给像素边界，目录取 1024² 为 1K 上限，2026-11-13）。
6. **输入图按张计费（2026-11-12 输入素材批）**：目录行 `rawModel.InputCostPerImage` / `PriceSet.InputUsdPerImage` 与每请求免费张数 `InputImageFreeCount`；结算从请求侧输入图张数（usage `InputImageCount`）扣免费张数后按张产 `image_input_unit` 行项。计数覆盖 JSON 生成请求顶层 `image`（字符串 1 张、数组按元素数）、JSON 编辑请求 `images[]`（按元素数）、multipart 编辑请求 `image`/`image[]` 文件 part（`mask` 不计），以及生成面 `metadata.image`/`metadata.images`（L3 参考图通道，投影为 provider_options 后 deep-merge 进上游报文；编辑面会剔除该通道、不重复计数）。当前落值：Volcengine Seedream 5.0 Pro 首张免费、后续 0.02 元/张（÷7.0）；Qwen `qwen-image-3.0` / `qwen-image-3.0-pro` 输入图 0.02 元/张（÷7.0，无每请求免费张数；账号级免费额度不进单请求计费）。
7. `usage_records.upstream_model` = 本次实际发送的 wire model；`upstream_response_model` = 上游响应 `model`（缺失为空）；`pricing_model` = 配置模型行。

#### 2.9.1 图像编辑参数表（`POST /v1/images/edits`）

请求体双形态：`multipart/form-data`（OpenAI legacy：`image`/`image[]` + `mask` + `prompt` + `model`）与 `application/json`（OpenAI 现行：`images:[{image_url|file_id}]` + `mask` + `prompt` + `model`）。JSON 形态只接受 `images[]` 数组或顶层 `image` 字符串；顶层 `image` 数组 → 400。请求体总上限沿用 `/v1` 入口 64MB（超总限由 body 管道先拒绝，不保证 16×10MB 可同时满足）。

| 参数 | 层 | 类型 / 值域 | 厂商不支持时 |
| --- | --- | --- | --- |
| `model` / `prompt` | L1 | string；均必填 | 缺失 → 400 `invalid_image_request` |
| `image` / `images[]` | L1 | multipart 文件或 JSON `{image_url}`；URL 或 data URL；最多 16 张、单张 ≤10MB | `file_id` 非 gpt → 400（无法跨厂商解析）；超限 → 400 |
| `mask` | L2 | multipart 文件或 JSON `{image_url}` | 仅 gpt 原生支持；xai/volcengine/qwen 忽略 + `params_ignored.mask` |
| `n` | L2 | int ≥1，默认 1 | 原样透传，由上游裁决（与生成面一致） |
| `size` | L2 | `1024x1024` 等或厂商档位 | xai 原样透传（官方未文档化，待实测）；qwen 转 `W*H`；volcengine 透传 |
| `response_format` | L2（legacy） | `url/b64_json` | qwen 忽略 + `params_ignored`；gpt/xai/volcengine 透传 |
| `quality` / `background` / `output_format` / `output_compression` / `input_fidelity` / `safety_identifier` / `user` | L2 | OpenAI 词表 | 按厂商白名单保留或忽略 + `params_ignored` |
| `stream` / `partial_images` | L2 | 统一图像契约不支持 stream | 全部接受但删除 + `params_ignored`，上游请求不携带 |
| `metadata` | L3 | JSON 请求体；链上投影为该厂商 `provider_options` 子对象 deep-merge；不接管 `image`/`images`；公开 `provider_options` 出现即 400 | multipart 不承载：非 gpt 忽略 + `params_ignored`，不报错；gpt 保持字节透传（未知字段由上游裁决） |

未处理的顶层参数（厂商白名单外且非结构键）一律进 `params_ignored`，不静默丢弃；`stream`/`partial_images` 即使只由 `metadata` 注入也必须删除并回显。**下文各厂商章节出现的 `provider_options` 均指对外 `metadata` 在链上的内部投影，不是公开请求字段。**

**multipart 编辑与账户模型映射互斥**：模型映射改写走 JSON 报文面（`buildModelMappedJSONBody`），multipart 请求体上不可用——配置了模型映射的账户收到 multipart 编辑请求返回 400「账号模型映射要求请求体是 JSON 对象」（与 STT multipart 口径同构）；JSON 编辑不受影响，需要模型映射时改用 JSON 编辑形态。

厂商映射（报文依据见 §4.5 / §6.3 / §9.3 / §10.3）：

| 厂商 | 上游端点 | 图片字段 | 边界 |
| --- | --- | --- | --- |
| gpt | `POST /v1/images/edits`（原路径） | multipart 原样；JSON 原样 | 唯一支持 `mask`；multipart 仅删除 `stream`/`partial_images` 文本字段 |
| xai | `POST /v1/images/edits` | 单图 `image:{url}`、多图 `images:[{url}]` | `mask`/`quality`/`background`/`output_format`/`output_compression`/`input_fidelity`/`safety_identifier`/`user` 忽略；`file_id` 400 |
| volcengine | `POST /api/v3/images/generations` | `image` 字符串或数组（URL/data URL） | 无独立 edits 端点；`mask` 忽略；`watermark` 默认 false |
| qwen | `POST /api/v1/services/aigc/multimodal-generation/generation` | `input.messages[0].content[].image` | 图片须 http(s)/data URL；`mask`/`response_format` 等忽略 |

规则：

1. 响应仍统一 OpenAI Images JSON；qwen 复用生成面响应转换（`output.choices[].message.content[].image` → `data[]`，`output_image_count` = 实际图片数），其余厂商透传并按 §2.9 规则 1 宽容判定。
2. 回显头与生成面完全一致；multipart 请求的回显事实由网关重新解析原始 body 计算（不缓存、不落盘）。
3. 零存储：上传文件只在内存中转为 data URL，不落盘、不缓存、不写审计 body；`FullBodyCaptureEnabled` 既有 opt-in 行为不变。
4. 计费沿用模型行（`OutputUsdPerImage` / `ImageOutputUsdPer1M`），不新增行项；qwen 专用编辑模型未收录目录，新增须先补官方价。

## 3. Mock 上游规格（测试基建扩展契约）

### 3.1 现状事实（2026-10-04 取证）

- 集中 Mock 引擎：`backend-go/shared/platform/mockupstream`（17 场景、`X-Mock-Scenario`/`?scenario=` 选择、端点白名单仅 chat/responses/embeddings/models，其余 404）。
- E2E：`acceptance` 目录三件套——场景控制器 `fullchainMockUpstream`（按上游 Bearer key 的场景队列）、`createAccount`（凭据 `base_url` 注入 httptest 地址）、`JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true` 私网放行。
- `mockdata` 为 DB 造数体系（非请求 mock）；新页面/列表/用量必须同步扩展对应域（《Mockdata造数设计》强制约定）。
- **缺口**：mockupstream 无媒体端点、无异步任务多态响应（状态随轮询推进）、无二进制载荷通道；`fullchainMockUpstream` 的 scriptable 判定只认 4 条 POST 路径。

### 3.2 mockupstream 引擎扩展规格

1. **端点白名单扩展**（`mockupstream.go` `acceptedEndpoints`）：`POST /v1/audio/speech`、`POST /v1/audio/transcriptions`、`POST /v1/audio/translations`、`POST /v1/videos`、`GET /v1/videos`、`GET /v1/videos/{id}`、`GET /v1/videos/{id}/content`、`DELETE /v1/videos/{id}`，以及 `/v1/tasks/audio` 族与 §4-§10 各厂商原生端点（每厂商一章的"Mock 端点"小节为准）。音频三端点 M1 已交付；`/v1/videos` 全 5 端点 M2 已交付（2026-10-04）；`/v1/tasks/audio` 族与厂商原生端点随对应期。
2. **异步任务场景能力（新增机制）**：按场景脚本驱动任务状态机——创建请求返回该厂商创建响应（含任务 id，id 由 mock 生成并登记内存任务表）；轮询请求按"第 N 次查询返回状态 X"脚本推进（如 `video_poll_twice_then_success`：前两次 `in_progress`，第三次终态）；同一 mock 实例内任务表隔离，支持并发用例。（M2 已交付，2026-10-04：视频任务状态机按场景脚本推进。）
3. **二进制载荷通道（新增机制）**：场景可声明响应为 `audio/wav`、`audio/mpeg`、`video/mp4` 等二进制（内置小体积合成载荷：合法 WAV 头+静音帧、最小 MP4 box 结构），供 speech/content/下载端点流式返回；断言侧校验 magic bytes 与 content-type。（音频载荷 M1 已交付；`video/mp4` 载荷 M2 增补交付，2026-10-04。）
4. **场景命名规范**（进 `mockupstream` 内置清单，格式 `media_<域>_<行为>`）。**M1 已交付 8 个同步音频场景与二进制载荷通道（2026-10-04）**：`media_tts_ok`、`media_tts_400_voice_invalid`、`media_tts_429_before_accept`、`media_stt_ok`、`media_stt_ok_verbose`、`media_stt_400_bad_file`、`media_gemini_tts_ok`、`media_gemini_tts_400_format`；**视频场景已随 M2 交付（2026-10-04，共 9 个）**，厂商原生异步任务场景属 M3+：
   - `media_tts_ok`（二进制音频）、`media_tts_400_voice_invalid`、`media_tts_429_before_accept`——M1 已交付
   - `media_stt_ok`（JSON `text`）、`media_stt_ok_verbose`（`verbose_json` 含 `duration`/`usage`）、`media_stt_400_bad_file`——M1 已交付
   - `media_gemini_tts_ok`（inlineData base64 PCM）、`media_gemini_tts_400_format`（`response_format` 非 `pcm` → 400）——M1 已交付
   - `media_video_ok_poll3`（#1/#2 `in_progress` → #3 `completed`）、`media_video_ok_poll1`（#1 即 `completed`，快路径）、`media_video_fail_after_accept`（受理后轮询 `failed`）、`media_video_429_create`（创建 429，可换账户重试）、`media_video_create_400_bad_size`（`size` 值域外 → 400，参数类不换账户）、`media_video_create_500`（创建 5xx）、`media_video_poll_500`（受理后轮询 5xx，不得换账户）、`media_video_content_expired`（content 404/410 → 网关透出"产物已过期"）、`media_video_cancel_ok`（保持 `queued`，验证 DELETE 流程）——M2 已交付（2026-10-04；其中 `media_video_ok_poll1` 与 `media_video_create_400_bad_size` 为 W1 批次交付、本清单同日补登记）
   - 厂商原生形态场景：`media_<provider>_create_ok` / `media_<provider>_poll_running` / `media_<provider>_poll_success` / `media_<provider>_poll_fail`（报文按 §4-§10 各章）——M3+（§5.1 的 `media_gemini_tts_*` 已随 M1 交付；**§5.2 的 `media_gemini_video_create_ok` / `_poll_running` / `_poll_done_uri` / `_poll_error` 已随 M3 交付（2026-10-04）**；**§7.1 的 `media_glm_video_*` 已随 M3 交付；§8 的 `media_minimax_create_ok` / `_poll_running` / `_poll_success` / `_poll_fail` 与 §8.2 的 `media_minimax_tts_ok` 已随 M3 第三批交付（2026-10-04）**；**§9.1 的 `media_volcengine_create_ok` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error` 已随 M3 第四批交付（2026-10-04）；§9.2 长转写臂的 `media_volcengine_asr_create_ok` / `_poll_running` / `_poll_succeeded_text` / `_poll_failed` 已随语音识别承接批交付（2026-10-17，`mockupstream/asr_volcengine.go`，头驱动协议——响应回 `X-Api-Status-Code`/`X-Api-Message` 并回显 `X-Api-Request-Id`，submit 成功 body 空）**；**§9.2 流式 TTS 臂的 `media_volcengine_tts_stream_ok` / `_stream_failed` 已随火山流式语音桥接 V1a 交付（2026-10-18，`/api/v3/tts/unidirectional` NDJSON 分块）**；**§10 的 `media_qwen_create_pending` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error` 已随 M3 第五批交付（2026-10-04）**；**§6.1 的 `media_xai_video_create_request_id` / `_poll_pending` / `_poll_done_url_duration` / `_poll_failed_error` / `_poll_expired` 已随 M3 回填批交付（2026-10-04）**）
   - 同步图像场景（2026-11-11 同步批）：`media_images_ok`（已有）、`media_images_b64_ok`、`media_images_no_image`、`media_images_relay_model_mismatch`（gpt/中转）、`media_xai_image_ok` / `media_xai_image_no_image`、`media_volcengine_image_ok` / `media_volcengine_image_no_image`、`media_qwen_image_ok` / `_v3_ok` / `_no_image`；图像 mock 端点白名单覆盖 `/v1/images/generations`、火山 Ark 的 `/api/v3/images/generations` 与 qwen 原生 `/api/v1/services/aigc/multimodal-generation/generation`。
   - 图像编辑场景（2026-11-12 T216）：`media_images_edit_ok`（OpenAI/gpt 形态 `created+data[].url`，供 gpt/xai 编辑端点与 volcengine/qwen 复用同一 200 判定）、`media_images_edit_no_image`（2xx 无 `data[]`）；白名单新增 `/v1/images/edits`（gpt 与 xai 共用该路径，按账户主机区分）。
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
计费（M1 双口径，按厂商真实定价维度；2026-11-12 按次兜底批）：`tts-1`、`tts-1-hd` 按 `input` 字符数（网关自算，行项 `tts_input_chars`）；`gpt-4o-mini-tts` 官方按 token（`audioInputUsdPer1M`/`audioOutputUsdPer1M`），但二进制响应无 usage、网关不解析音频 → **按次固定价 $0.3/次**（行项 `tts_output_calls`，价格参考 new-api 同口径，非 OpenAI 官方按次价；`usage_missing` 仍置位记录上游无 usage 事实）；`gpt-4o-tts` 未收录（官方无定价，待官方价后补行，不编造）。
Mock：`media_tts_ok` 返回内置合成 WAV/MP3 载荷。

### 4.2 STT：`POST /v1/audio/transcriptions`（multipart：`file`、`model`、`language`、`prompt`、`response_format`、`timestamp_granularities[]`）

响应（`json`，默认）：`{"text":"..."}`；（`verbose_json`）：`{"text","language","duration"(秒),"segments":[...],"words":[...],"usage":{"input_tokens","output_tokens"}(gpt-4o-transcribe 系)}`。
`/v1/audio/translations` 同构。计费：`usage` 优先，否则按 `duration` 秒（whisper 系，官方按分钟定价、计量落秒）。
Mock：`media_stt_ok` / `media_stt_ok_verbose`。

### 4.3 视频：上游 OpenAI Sora 面（内部 adapter；对外不再暴露）

> 2026-11-12 用户裁决：对外视频面只保留 New API 形态 `/v1/video/generations`（§2.2），旧 `/v1/videos` 硬 404。本节仅描述 gpt/Sora **上游**报文与内部 adapter，供出站构造与 Mock 使用。

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

### 4.5 同步图像：`POST /v1/images/generations`（置信度 A：官方文档 + 2026-11-11 真实探针）

- 请求：`{"model":"gpt-image-2","prompt":"...","n":1,"size":"1024x1024","quality":"...","background":"...","output_format":"...","safety_identifier":"..."}`；`provider_options` 由网关消费，不转发上游。2026-11-12 用户裁决图像统一只返回 JSON，`stream`/`partial_images` 不属于支持字段；客户端若携带，仅作为兼容输入，网关删除并回显 `params_ignored`，不转发上游。
- 响应：`{created, data:[{b64_json|url, revised_prompt?}], usage?}`；gpt-image 实测 `data[0].b64_json` 约 1.58MB，usage 为 prompt/completion/input/output tokens + `output_tokens_details.image_tokens`。
- 图像统一契约**不产生 SSE**：`stream: true` / `partial_images` 对 gpt/xai/volcengine/qwen 全部接受但忽略，回显 `params_ignored`，统一返回普通 JSON；gpt 不再直通 SSE。后续确有需要再立扩展项。
- OpenAI 兼容中转可能返回 `url` 或 `b64_json`，usage 可能缺省/带额外字段；按 §2.9 响应宽容归一，不按模型名/厂商拦截。
- 计费：gpt-image 按模型行图像 token 价（`image_output_usd_per_1m`）或按张价，行内策略决定。
- **编辑（`POST /v1/images/edits`，2026-11-12 T216）**：OpenAI 官方同期存在两代形态——legacy `multipart/form-data`（`image` 文件、`mask` 文件、`prompt`、`model`、`n`、`size`、`response_format`、`user`）与现行 JSON（`images:[{image_url|file_id}]`、`mask`、`prompt`、`model`、`n`、`size`、`quality`、`background`、`output_format`、`output_compression`、`input_fidelity`、`stream`、`partial_images`）。网关 multipart 保持字节级透传（只删除 `stream`/`partial_images` 文本字段），JSON 走生成面同一 adapter；响应同 `ImagesResponse`，token usage 照抽。`mask` 原生支持。
- Mock：`media_images_ok`（`{"created":1700000000,"data":[{"url":"..."}]}`）；新增 `media_images_b64_ok`、`media_images_no_image`（2xx 无 data）、`media_images_relay_model_mismatch`（响应 model 为其他厂商，仍应成功）。

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
计费：按输出秒（目录档）；usage 不回报，且 `seconds` 属 ignored 参数不构成计量基源——网关按 §2.8.2 用 adapter 声明的**厂商默认输出时长 8 秒**作为计费基源（`VideoCapabilities.DefaultSeconds=8`），成本 = 8 × 目录秒价；不再落 0。用户显式传入的 seconds 仍进 `params_ignored`，不参与计费。**计费字段封禁（2026-11-13 T219）**：`provider_options.gemini` 子对象中的 `durationSeconds` / `sampleCount`（含 `parameters` 嵌套层）会改变实际产出时长/段数，但计费基源已按顶层参数冻结——命中即本地 400（`ErrParamUnsupported`），不静默丢弃；非计费键（如 `personGeneration`）照常 deep-merge。
Mock：`media_gemini_video_create_ok` / `media_gemini_video_poll_running` / `media_gemini_video_poll_done_uri` / `media_gemini_video_poll_error`（M3 已交付，创建场景冻结任务脚本）。

### 5.3 Live（实时语音，M6a；置信度 B：多源确认 2026-10-04）

上游端点 `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent`（API key 头；google_oauth 同面）。客户端消息：`setup{model,generationConfig}`（首帧，会话配置 voice/modalities/systemInstruction/tools）、`realtimeInput{audio}`（音频分块 base64）、`clientContent{turns,turnComplete}`（文本/轮次）、`toolResponse`。服务端事件：`setupComplete`、`serverContent`（modelTurn.inlineData 音频、outputTranscription、interrupted、turnComplete）、`toolCall`。**Live 无 usage 事件**（turnComplete 不携带计量）→ 网关终态 usage_missing（不估算）。事件映射矩阵与不映射面见《流式语音跨协议转换设计》§3（M6 实施契约唯一事实源）。Mock：`media_gemini_live_setup_audio_turn`（setup 断言/audio 回吐/turnComplete）等随 M6a 交付。

## 6. xai（置信度 A：官方文档原文抓取 2026-10-04，经本地代理 web reader）

### 6.1 视频（Grok Imagine Video）——**M3 回填池已实施 adapter**（`backend-go/projects/gateway/internal/gatewaymedia/xai_video.go`，注册键 `xai`，2026-10-04）

**M3 回填池实施面**（参数映射按 §2.2/§2.4/§2.7 裁决）：`seconds`→`duration`（1–15 整数秒，小数取整直传、区间由上游裁决）；`size` WxH → `aspect_ratio`（gcd 约分精确命中七值词表，词表外本地 400）+ `resolution`（短边最近档 ≥900→1080p / ≥600→720p / 其余→480p）；`input_reference`→`image`（url/base64 字符串直传）；`audio`→`generate_audio` 布尔直传（nil 不传，上游默认 true）；`negative_prompt`/`seed`/`n` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。URL 为版本根直拼：xai base 默认 `https://api.x.ai/v1`（版本根），视频操作路径不含 `/v1` 前缀，经 `JoinUpstreamURL` 直拼（同 minimax 先例）；无取消端点 → `SupportsCancel=false` 本地收敛 `cancelled`。

- 创建：`POST https://api.x.ai/v1/videos/generations`，Bearer（版本根直拼同族）。请求：`model`（`grok-imagine-video-1.5` 主推；classic `grok-imagine-video` 仍收 `last_frame` 拒绝限制）、`prompt`（文生视频必填；带帧/引用输入时可选）、`duration`（1–15 秒）、`aspect_ratio`（`1:1/16:9/9:16/4:3/3:4/3:2/2:3`，默认 16:9）、`resolution`（`1080p/720p/480p`，默认 480p；1080p 仅 1.5 的文/图生视频）、`image`（图生视频首帧）、`reference_images`/`reference_audios`（reference-to-video，`reference_audios` 预置 `voice_id` 最多 3）、`last_frame`（首尾帧钉定，1.5）、`generate_audio`（默认 true 音轨）。响应：`{"request_id":"<uuid>"}`——**受理凭据 = `request_id`**。
- 轮询：`GET /v1/videos/{request_id}` → `status ∈ pending | done | expired | failed`；`done` 返回 `video{url（vidgen.x.ai 临时 URL）, duration（秒——**时长计量基源**）, respect_moderation}`；`failed` 返回 `error{code,message}`（code 词表：`invalid_argument/permission_denied/failed_precondition/service_unavailable/internal_error`；鉴权/限流错误在创建时同步返回，不进任务终态）。状态归一：pending→`in_progress`、done→`completed`、failed→`failed`、**expired→`expired`（上游原生终态，直接收敛）**。
- 产物：临时 URL 直连下载（ContentFromArtifact 无凭据）。
- 计费：done 的 `video.duration` → `OutputVideoSeconds`（终态秒计量照抽）；官方 USD 秒价未在能力页（定价子页未抓取）→ 不落价 cost=0，价格可查证后补（qwen 先例）。
- 范围外（记录不实施）：`/v1/videos/edits`（编辑）、`/v1/videos/extensions`（扩展）专用端点；`reference_audios` 自带音频引用（L3 透传 `provider_options.xai` 可达）。
- Mock：`media_xai_video_create_request_id` / `_poll_pending` / `_poll_done_url_duration` / `_poll_failed_error` / `_poll_expired`。

### 6.2 语音（Grok Voice API）

TTS/STT/STS 能力已确认（2026-10 检索），报文仍待官方页抓取（`docs.x.ai` 语音能力页）——待回填，不编造。

### 6.3 图像（Grok Imagine Image；2026-11-11 同步批，置信度 A：官方指南 + 真实探针）

- 端点：`POST https://api.x.ai/v1/images/generations`，Bearer（账户 base_url 版本根 `https://api.x.ai/v1` 直拼 `/images/generations`）。xAI 图像面本身是 OpenAI Images 兼容形态，网关不重写路径。
- 请求：`model`（`grok-imagine-image-2.0` 主推）、`prompt`、`n`（每请求最多 10）、`response_format`（`url|b64_json`）、`size`/aspect ratio（字段名以官方 OpenAPI 为准；未确认字段原样透传，由上游裁决）。
- 2026-11-11 真实探针：`grok-imagine-image-2.0` + `n:1` + `response_format:"url"` → 200；响应 `data[0].url` + `mime_type`，`usage.cost_in_usd_ticks`；**没有 `created`/`model`**。按响应宽容口径，缺字段不影响成功。
- 网关参数处理：`provider_options.xai` deep-merge 进顶层 body；`stream`/`partial_images`/`quality`/`background`/`output_compression`/`safety_identifier` 等 xAI 未文档化参数忽略并回显 `params_ignored`；`model`/`prompt`/`n`/`size`/`response_format` 透传。响应不改写。
- 计费：`data[]` 长度 → `output_image_count`，按模型行 `output_usd_per_image` 计费；`usage.cost_in_usd_ticks` 仅保留原始提示，不替代目录价。
- **编辑（`POST /v1/images/edits`，2026-11-12 T216；置信度 A：xAI OpenAPI `EditImageRequest`）**：官方原生编辑端点 `POST https://api.x.ai/v1/images/edits`，Bearer。请求 `prompt`（必填）、`image`(ImageUrl) 与 `images[]` 互斥、`model`、`n`、`response_format`(`url|b64_json`)、`aspect_ratio`、`resolution`、`output`、`storage_options`；`ImageUrl = {url, file_id}`，`url` 接受公网 URL 或 base64 data URL。响应 `data:[{url|b64_json,mime_type}] + usage(MediaUsage)`，**无 `created`**。网关把 multipart 文件转 data URL 后写 `image:{url}`（单图）或 `images:[{url}]`（多图）；`mask`/`quality`/`background`/`output_format`/`output_compression`/`input_fidelity`/`safety_identifier`/`user` 忽略并回显；`size` 原样透传（官方未文档化，待实测）；`file_id` 无法跨厂商解析 → 400。
- Mock：`media_xai_image_ok`（`data[].url` + `mime_type` + cost ticks，无 created/model）、`media_xai_image_no_image`（2xx 无 data）。

## 7. glm / 智谱（视频 B 级：报文多源确认；语音 C 级：模型确认字段待回填）

### 7.1 视频（CogVideoX / 清影系，2026-10-04 回填；M3 已实施 adapter）

**M3 已实施**（`backend-go/projects/gateway/internal/gatewaymedia/glm_video.go`，注册键 `glm`；认证 Bearer API Key，沿链上 openai 族认权分支）。参数映射（§2.2/§2.4/§2.7 裁决）：`negative_prompt` 原生直传；`input_reference`→`image_url`（url/base64 字符串均透传，glm 原生即 URL 形态，网关不代为下载）；`seconds`→`duration`（数值取 5s/10s **最近档**换算，等距取小档）；`audio`→`with_audio`；`size` 原样直传（glm 的 size 即 WxH 像素串形态，档位由上游裁决，网关不硬编码）；`n`/`seed` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。出站操作路径不含版本/服务前缀（`/videos/generations`、`/async-result/{id}`），账户 base_url 为版本根（官方 `https://open.bigmodel.cn/api/paas/v4`），经 `JoinUpstreamURL` 直拼、无服务根去重或 `/v1` 补缀分支（旧 `chainGlmVideoUpstreamURL` 服务根去重条款随版本根语义退役）。

- 创建：`POST https://open.bigmodel.cn/api/paas/v4/videos/generations`，`Authorization: Bearer <KEY>`。请求：`model`（cogvideox / cogvideox-2 / cogvideox-3 系）、`prompt`、`negative_prompt`、`image_url`（图生视频，承接公共 `input_reference`）、`size`、`duration`（如 5s/10s）、`fps`、`quality`（speed/quality）、`with_audio`（承接公共 `audio`）。响应含 `id`、`request_id`、`task_status`——**受理凭据 = `id`**。
- 轮询：`GET https://open.bigmodel.cn/api/paas/v4/async-result/{id}` → `task_status ∈ PROCESSING | SUCCESS | FAIL`；SUCCESS 返回 `video_result{url, cover_image_url}`（url 即产物下载定位；M3 已实施：url 冻结进 Artifact.ContentURL，`ContentFromArtifact` 直连下载无凭据）。
- 状态归一：PROCESSING→`in_progress`、SUCCESS→`completed`、FAIL→`failed`（§2.6 总表 glm 列以此回填；M3 已实施：FAIL 错误摘要 code 取 task_status 原值，error 对象在场则优先）。
- 取消：glm 无取消 API（回填面无取消端点，M3 裁决）——`SupportsCancel=false`，链上不发上游请求、直接本地收敛 `cancelled`（§2.6 本地终态语义）。
- 计费（2026-10-05 按次计费批已实施，§2.8.1）：CogVideoX 官方按次计费——目录行 `VideoOutputCostPerCall`：cogvideox-3 $0.2/次（z.ai docs.z.ai/guides/overview/pricing USD 明文；国内 1 元/次人民币原句留档）、cogvideox-2 $0.0714/次（国内 0.5 元/次 ÷ 7.0 换算）。轮询响应不回报时长（video_result 仅 url/cover_image_url）→ 任务终态 completed 成本 = 次价 × 1（一次任务 = 一次调用，一次任务只产一条视频），usage 行不落秒计量、不打 `usage_missing`；每秒槽位保持空（按次 ≠ 按秒，互斥门禁）。
- Mock：`media_glm_video_create_ok` / `_poll_processing` / `_poll_success_url` / `_poll_fail`（M3 已交付，端点 `/api/paas/v4/videos/generations`、`/api/paas/v4/async-result/{id}` 与 `/content` 产物通道）。

### 7.2 语音（B 级已回填 2026-10-04：OpenAI 兼容端点官方确认；**M6 已实施**）

- **TTS**：`POST https://open.bigmodel.cn/api/paas/v4/audio/speech`，Bearer——**OpenAI 兼容形态**（官方 api-reference「文本转语音」页 curl 确认）：`{"model":"cogtts","input":"...","voice":"tongtong","response_format":"wav",...}`；厂商扩展 `ref_audio`/`ref_text`（3 秒样本声音复刻）走 `provider_options.glm`；GLM-TTS 模型（`GLM-TTS`/开源权重部署）同面。计费按字符（官方页可查证后落价，查不到不编造）。
- **ASR**：对应 `audio/transcriptions` 路径（OpenAI 兼容同面；GLM-ASR-2512 系模型）；字段全集以官方页为准（B 级：端点形态官方确认、字段全集多源）。
- 接入形态：**openai 透传分支**（无独立 adapter 需求）——glm 账户 TTS/STT 走 openai 形态直连（base 携带 glm 版本根 `/api/paas/v4`，操作路径不含版本前缀直拼，同 CogVideo 先例）。
- Mock：`media_glm_tts_ok`（OpenAI speech 形态回 wav 载荷）。
- **M6 实施注（2026-10-04）**：glm 账户的 `/v1/audio/speech|transcriptions|translations` 走链上透传分支（`chain_driver.go` glm speech URL 构造 + `provider_options.glm` 子对象深合并进上游 body、通道键不透传；URL 直拼复用 glm 版本根语义——base 携带 `/api/paas/v4` 版本根，操作路径不含版本前缀，无服务根去重分支）。端点模式 `audio_speech`/`audio_transcription_json` opt-in（写侧词表 `glmSpeechEndpointModeSet` 放行）。目录行 `cogtts`（mode=audio、协议 audio_speech、不落字符价——计量照落成本不虚计）；GLM-ASR 目录行待官方 API 模型名可查证后补（第三方口径 `glm-asr` 不作数）。`response_format` 词表由上游裁决（透传零转码，无本地门禁）。账号接入见《智谱GLM账号接入.md》语音节。

既有 `glm` 供应商档案沿用，仅新增媒体 profile 能力（endpoint families + modes），不新增 provider_code。

## 8. minimax（置信度 B：官方文档多源确认；M3 第三批已实施 adapter，2026-10-04）

### 8.1 视频：Hailuo v1 `POST /v1/video_generation` + H3 v2 `POST /v2/video_generation`（host `api.minimax.chat` / `api.minimax.cn` / `api.minimaxi.com`，以账户 base_url 为准）——M3 已实施 adapter；2026-11-13 H3 v2 批（`gatewaymedia/minimax_video.go`；账号接入见《MiniMax账号接入.md》）

**Hailuo-2.3 v1（现状；2026-11-13 分辨率批）**：请求 `{"model":"MiniMax-Hailuo-2.3","prompt":"...","prompt_optimizer":true,"duration":6,"resolution":"768P|1080P","first_frame_image":"<base64>"}`（`first_frame_image` 承接公共 `input_reference`；官方 v1 支持 `resolution`：768P 默认、1080P 仅 6s、720P 对 2.3 不可用）。网关按 `size` 短边 ≥900 → 1080P、其余 768P 显式发送，并冻结同值 `billingResolution`。创建响应 `{"task_id":"...","base_resp":{...}}`；轮询 `GET /v1/query/video_generation?task_id=...` → `{"task_id","status":"Preparing|Queueing|Processing|Success|Fail","file_id","file_download_url"}`；下载取 `file_download_url` 直连（`ContentFromArtifact=true`）。`provider_options.minimax` 示例 `{"prompt_optimizer":true}`。

**H3 / H3-Max v2（2026-11-13 实施；官方指南：`platform.minimax.cn/docs/guides/video-generation`、`platform.minimax.io/docs/guides/video-generation`）**：同一 provider 注册键按模型分派 v2 协议。
- 创建：`POST {host}/v2/video_generation`，请求 `{"model","content":[...],"duration","resolution","ratio"?}`。`prompt` → 首个 `{"type":"text","text":...}`；`input_reference` → `{"type":"image_url","image_url":{"url":...},"role":"first_frame"}`（http(s) URL / data URL 直传；裸 base64 无 mime → 本地 400）。`duration` 恒显式发送（用户 seconds，否则默认 6 秒，官方区间 4–15）。`resolution` 官方词表：H3 768P/2K、H3-Max 480P/768P；按 `size` 短边换算（H3 ≥1440→2K，H3-Max ≥720→768P），未传 size 用主档 768P。`ratio` 仅文生视频发送（官方要求且不能 adaptive），按 `size` 精确约分到六档词表（16:9/9:16/1:1/4:3/3:4/21:9），无 size 默认 16:9；图生视频省略 ratio、跟随首帧。
- 版本根：H3 v2 操作路径在 host 根，而账户 base_url 承载 Hailuo 的 `/v1` 版本根。链上层对 minimax 的 `/v2/` 路径做**版本根特例**：剥离 base 末尾 `/v1` 后直拼（`/v1/v2/...` 实测 404；`/v2/...` 实测 401 鉴权信封，端点存在）。base 已是 host 根时直拼不变。
- 创建响应 `{"task_id":"..."}`；轮询 `GET {host}/v2/query/video_generation/{task_id}`（路径参数）→ 官方嵌套形态 `{"task":{"status","content":{"url"},"error"}}`，兼容顶层扁平形态。状态词表：`succeeded/success/completed`→completed、`failed/fail/error`→failed、`cancelled/canceled`→cancelled、`queued/pending/created/preparing/waiting/submitted`→queued、`processing/running/in_progress`→in_progress；空状态（创建受理）→queued；未知非空→in_progress + RawStatus。completed 缺 `task.content.url` → 协议违约报错。取消仍无端点（`SupportsCancel=false`）。
- 任务面分派：`VideoModelTaskAdapter` 可选接口按创建时冻结的模型选轮询路径/响应解析；Hailuo 模型保持 v1 面。创建响应解析仍走 v1 形态（受理凭据同为顶层 task_id）。
- **范围裁决（T219 已收口）**：H3 `reference_image`/`reference_video`/音频输入多素材、H3-Max 输入视频 0.97/0.37 元每秒与超出免费张数的输入图计费不在统一入口开放——当前只表达单张首帧（H3 前 5 张、H3-Max 前 2 张免费，均落在免费额度内），且 `metadata` L3 注入 `content`/`image`/`reference_*` 已被计费字段封禁（§2.4 规则 5）直接 400，不存在漏计请求；未来开放多素材属新功能，需同时补输入视频秒价维度。H3-Regeneration / H3-Context-IR 是独立任务面，未接入。

计费（2026-10-05 按次计费批已实施，§2.8.1；2026-11-12 默认时长 + 时长档批；2026-11-13 H3 分辨率档 + Hailuo 1080P 批）：按目录行维度分两条——Hailuo-2.3 官方按条/档位计费，目录行 `VideoOutputCostPerCall` 落主档 768P-6s 2.00 元/条（÷7.0=$0.2857），`VideoOutputCostPerCallBySeconds` 落 6s/10s 两档（10s 4.00 元/条 ÷7.0=$0.5714），`VideoOutputCostPerCallByResolutionSeconds` 落 768P-6s/10s 与 1080P-6s（3.50 元/条 ÷7.0=$0.5；Fast 对应 $0.1929/$0.3214/$0.33）；创建时请求秒数按最近档收敛，结算先按 `billingResolution + billingSeconds` 组合选档、未命中回落秒数主档，轮询响应不回报时长，usage 行不落秒计量、不打 `usage_missing`；H3/H3-Max 目录行是**秒价 + 分辨率档表**：H3 768P $0.0714/2K $0.1143、H3-Max 480P $0.0471/768P $0.0714（国内人民币 ÷7.0），创建时 `BillingResolution` 冻结档位，结算按档秒价 × 秒，档位不命中不回退主档；轮询不回报时长，创建请求未携带 `duration` 时按**厂商默认 6 秒**（`VideoCapabilities.DefaultSeconds=6`）冻结计费基源，成本 = 6 × 档位秒价（§2.8.2）。按条行禁止折秒，秒价行不做次数换算。
取消：无上游取消端点（M3 裁决：`SupportsCancel=false`，不发上游请求直接本地收敛 `cancelled`，沿 glm §7.1 先例）。
Mock：`media_minimax_create_ok` / `_poll_running` / `_poll_success(file_id)` / `_poll_fail(base_resp)`——M3 已交付（`mockupstream/video_minimax.go`）；H3 v2 复用同一组场景名，按请求路径（`/v2/video_generation`、`/v2/query/video_generation/{id}`）切换创建/轮询报文形态（创建只回 `task_id`、轮询回嵌套 `task.status/content.url`，2026-11-13 交付，`TestMediaMinimaxH3V2FullChain` 钉住）。

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

异步长音频（长转写）：端点与字段接入前以 platform.minimax.io 回填；`/v1/tasks/audio` 长音频面首个上游以回填结果定（候选：百炼 paraformer / 火山长转写 / MiniMax）。

## 9. volcengine / 火山方舟（置信度 B：官方格式多源确认；M3 第四批已实施视频 adapter，2026-10-04）

host：`ark.cn-beijing.volces.com`；认证 `Authorization: Bearer <ARK_API_KEY>`。

### 9.1 视频（seedance 系）：`POST /api/v3/contents/generations/tasks`——M3 已实施 adapter（`gatewaymedia/volcengine_video.go`；账号接入见《火山方舟账号接入.md》）

**M3 已实施**（注册键 `volcengine`；api_key Bearer 认证，沿链上 openai 族认权分支）。参数映射（§2.2/§2.4/§2.7 裁决；`resolution`/`ratio`/`duration`/`seed` 参数名与 `content` 数组的 url/base64 双形态已于 2026-10-04 经官方 API 参考页核实）：`prompt` → `content[0]`（`{"type":"text"}`）；`input_reference` → `content` 追加 `{"type":"image_url","image_url":{"url":...}}`（公网 URL 与 Base64 data URL 双形态字符串直传，官方由上游拉取，网关不代为下载——与 minimax 仅 base64 不同）；`size` WxH → `resolution` + `ratio` 档位换算（ratio 约分须精确命中官方词表 16:9/9:16/1:1/4:3/3:4/21:9，词表外本地 400 不近似贴合；resolution 官方词表 480p/720p/1080p，按短边取最近档，合法组合由上游裁决）；`seconds` → `duration` 数值秒直传（官方默认 5）；`seed` 整数直传；`camera_fixed` 等厂商个例经 `provider_options.volcengine` deep-merge；`negative_prompt`/`audio`（`generate_audio` 仅 seedance 2.0/1.5 pro，M3 目录 1.0 pro 不声明）/`n` 请求面无对应字段 → 忽略 + 回显 `params_ignored`。出站操作路径不含版本/服务前缀（`/contents/generations/tasks` 族），账户 base_url 为版本根（官方 `https://ark.cn-beijing.volces.com/api/v3`），经 `JoinUpstreamURL` 直拼、无服务根去重或 `/v1` 补缀分支（旧 `chainVolcengineVideoUpstreamURL` 服务根归一条款随版本根语义退役——同 glm §7.1 先例）。

- 创建：`POST https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks`，请求 `{"model":"doubao-seedance-1-0-pro-250528" 系,"content":[{"type":"text","text":"..."},{"type":"image_url","image_url":{"url":"..."}}],"resolution":"720p","ratio":"16:9","duration":5,"seed":42,...}`。创建响应：`{"id":"cgt-...","status":"queued",...}`——**受理凭据 = `id`**（任务记录保留 7 天）。
- 轮询：`GET /api/v3/contents/generations/tasks/{id}` → `status ∈ queued|running|succeeded|failed`；成功 `content.video_url`（时效内下载 URL；M3 已实施：冻结进 Artifact.ContentURL，直连下载无凭据）；失败 `error{code,message}`（M3 已实施：错误摘要 code/message 透传，缺席以 status 原值兜底）；官方回调面另有 `expired` 等终态词，轮询面未回填——未知状态归 `in_progress` 记 RawStatus（§2.6 归一规则）。**usage/duration 字段全貌已回填（2026-10-17 真实上游实测）**：succeeded 响应顶层 `duration`（生成秒数，整数）、`usage.completion_tokens`/`usage.total_tokens`、`resolution`/`ratio`/`framespersecond`/`output_format`/`seed`/`service_tier` 等元数据（实测例：720p 5 秒 mini → `duration:5`、`usage.completion_tokens:108900`）——adapter 抽 `duration` 正值 → `Usage.OutputVideoSeconds`（usage_source=upstream）。
- 下载：`video_url` 直连流式代理（M3 已实施：`ContentFromArtifact=true`）。
- 计费：seedance 按秒 × 分辨率档（目录 720p 主档 USD 秒价已落值，见 §9 总表）；`usage` 回报优先。**2026-10-17 实测回填批**：轮询 succeeded 顶层 `duration` 正值 → 秒计量照抽（`OutputVideoSeconds`，usage_source=upstream）。**2026-11-12 默认时长批**：上游未回报 duration 且创建请求未带 `seconds` 时，按 adapter 声明的厂商默认 5 秒（`VideoCapabilities.DefaultSeconds=5`）冻结计费基源（§2.8.2），不再落 0。failed 仍不虚计。**2026-11-13 真实探针（seedance-2.0-mini）**：`480p`/`16:9`/`duration:5` 创建 200（`cgt-...`），轮询 8 次约 70s 后 succeeded；终态回报 `duration:5`、`resolution:480p`、`ratio:16:9`、`usage.completion_tokens:50638`。目录 480p 秒价 0.23 元/s × 5 = ¥1.15；官方 token 口径 50638×23/1e6 = ¥1.1647，差 1.3%（秒价是 token 价的示例折算，最终以秒价落账）。
- `provider_options.volcengine` 示例：`{"resolution":"720p","ratio":"16:9","camera_fixed":true}`（以官方参数名为准）。
- 取消：§9.1 回填面无取消端点（M3 裁决：`SupportsCancel=false`，不发上游请求直接本地收敛 `cancelled`，沿 glm/minimax 先例；官方回调文档提及任务存在 cancelled 终态、仅排队中任务可取消，取消端点回填后升级）。
- Mock：`media_volcengine_create_ok` / `_poll_running` / `_poll_succeeded_video_url` / `_poll_failed_error`——M3 第四批已交付（`mockupstream/video_volcengine.go`，端点 `/api/v3/contents/generations/tasks` 族与 `/content` 产物通道）。

### 9.2 TTS / 长转写

**TTS（2026-10-17 迁移批回填并实施；置信度 A：官方文档 + 真实上游实测）**：`POST https://openspeech.bytedance.com/api/v3/tts/create`——**注意 host 为 openspeech.bytedance.com（语音服务域）非 ark**。**迁移事实（2026-10-17 真实上游实测）**：M6 依据的旧端点 `POST /api/v3/tts` 已 404 下线（"Endpoint 'tts' does not exist"），旧版控制台 appid+token 双值凭据与 `Authorization: Bearer;<token>` 分号形态一并废弃；现役面为豆包音频生成（音频生成 HTTP 接口），鉴权 `X-Api-Key: <api-key>` 单头单值（新版语音控制台 API Key，凭据字段沿用 `speech_token` 单值；`speech_appid` 退役为可选遗留键，不再参与出站）。请求：`{"model":"seed-audio-1.0","text_prompt":"...","speaker":"<音色名>","audio_config":{"format":"mp3","speech_rate":1.0}}`——`model` **真实透传**（现役 `seed-audio-1.0`；统一面历史占位名 `doubao-tts` 由 adapter 归一映射为 `seed-audio-1.0` 兼容既有账户；**派发模型匹配同构等价（2026-10-17 实测回填批）**：账户 `supportedModels` 声明两名字之一即可承接任一名字的请求——占位名与现役名视为同一能力，等价在**五层**匹配面同构生效：候选窗口 SQL（preflight 选组的 `direct_models.model` 谓词按等价组展开 `IN`）、路由选组过滤（`gatewayrouting.FilterAccountsByRequestedModel`）、派发候选过滤（`gatewaydispatch.FilterGatewayAccountsByRequestedModel`）、driver 能力判定链（`gatewayRequestCapabilityMismatchReasonFor`——实测断层正是此层以 model_unsupported 拒绝占位声明账户）、切号承接（`switchtarget.go` 冻结目标候选检查）、**计价解析（2026-10-17 实弹取证补第六消费点）**（usage 记账的目录规范名解析按等价组变体回退：gateway `chain_pricing.go findChainCatalogItem` 与 jobs `shared/platform/usagepricing/catalog.go` 镜像同构——占位名 `doubao-tts` 请求的用量此前按目录 miss 落 0 成本，与「计费按归一后 seed-audio-1.0 秒价」的宣称不符，实弹复测 5.9s → $0.014047605 按现役秒价计费），与 adapter 归一共用该等价事实，不再出现"目录现役行可请求、存量占位账户 503"的断层）。**大小写边界**：Go 四层匹配组内 EqualFold，候选窗口 SQL 的 `IN` 为规范名字面量精确匹配（PG 大小写敏感）——大小写变体请求（如 `Seed-Audio-1.0`）经 Go 层但被窗口剔除（与等价前行为一致）；出站 adapter 占位归一为精确比较，等价组命中后模型名原样出站（现役名透传、占位名归一）；`speaker` 上游可选（缺省默认音色）。公共参数映射：`input`→text_prompt、`voice`→speaker、`response_format`→audio_config.format（**实测词表 mp3/wav/ogg_opus/opus**；opus 上游返回 OggS 容器、content-type 按 audio/ogg——**2026-10-17 实测注记：上游对 format=opus 偶发返回 WAV 容器字节（RIFF 头，同请求重试即恢复 OggS，上游内部编码产出不稳定），下游客户端应按字节头识别容器而非信任 content-type，网关零转码不纠正**；pcm 实测上游挂起超时不采纳，词表外按 §2.4 规则 2 本地 400 零转码）、`speed`→audio_config.speech_rate（倍率形态；3.0 实测接受，上游对越界值静默接受不拒绝——网关维持声明区间 [0.2,3.0] 预校验 400，不猜上游真实边界）。响应 200：`{"audio":"<base64>","duration":3.28,"original_duration":3.28,"url":"<签名 CDN 直链，约 2 小时时效>"}`——成功响应**无 code 字段**，`audio` base64 解码后透传（不得把 base64 字符串当音频下发）；HTTP 非 200 错误信封为平铺 `{"code":45000010,"message":"Invalid X-Api-Key"}` 形态（链上按状态码统一错误上抛，同步面无任务受理语义）。**分派语义（2026-10-17 实测回填批）**：volcengine 账户 + speech 路径 + 请求体为 JSON 对象时，`ParseSpeechRequest` 校验失败（`voice` 必填缺失、`model`/`input` 缺失等 L1 面）→ 网关 400 `invalid_gateway_request` 透传校验消息（如"语音合成请求缺少 voice"），**不再静默落 openai 通用透传链**（旧行为会把参数错误送上游 404 后以 503 upstream_retryable_error 伪装，错误语义与可重试性双重错乱）。计费按时长（**2026-10-17 维度修正批**：官方计费维度为生成原始时长、精确至秒——响应顶层 `duration` 秒落 `audio_input_seconds` 音频秒计量（费率 `audioInputUsdPerSecond` 按模型），不再落字符口径 `TtsInputChars`；duration 缺失/非正值 → 0 计量 + `usage_missing`，不猜测；目录秒价已落值（1 元/分钟 ÷ 60 ÷ 7.0 = $0.00238095/秒）——见《火山方舟账号接入.md》计价口径节）。Mock：`media_volcengine_tts_ok`（200 + audio base64 + duration 载荷）+ `media_volcengine_tts_failed`（401 平铺错误信封）。
**流式 TTS（豆包语音合成模型 2.0 / `seed-tts-2.0`；2026-10-18 火山流式语音桥接 V1a 回填并实施；置信度 A：端点/鉴权/错误形态实弹探测 + 帧名 B 级待实弹回填）**：`POST https://openspeech.bytedance.com/api/v3/tts/unidirectional`（语音服务域同 host；**HTTP chunked NDJSON 流**，非 WS）。鉴权 `X-Api-Key` 单头——**与同步面同一 `speech_token` 实测穿过鉴权层（零新凭据）** + `X-Api-Resource-Id: seed-tts-2.0`（模型名即资源 ID）。请求 `{"user":{"uid"},"req_params":{"text","speaker","audio_params":{"format","speech_rate"}}}`——统一面映射 `input`→req_params.text、`voice`→req_params.speaker、`response_format`→audio_params.format（**2.0 词表 mp3/pcm/ogg_opus/wav：无 opus、pcm 正式支持**，词表外本地 400）、`speed`→audio_params.speech_rate（**换算 `(speed-1)×100`——2026-10-23 官方文档字段表回填（A 级：官方文档字段表明文）：speech_rate 默认 0、取值 [-50,100]、100 代表 2.0 倍速、-50 代表 0.5 倍速**；流式面官方可表达倍速 **[0.5,2.0]**，越界本地 400（§2.4 规则 2）——同步面 `audio_config.speech_rate` 倍率形态 [0.2,3.0] 是另一 API 区间，不适用于流式面；字段名实弹复核可后续补充）。响应 200 `text/plain` **NDJSON**（**2026-10-18 真实 2.0 出音 A 级回填**，16 音频帧 + 句级帧 + 终止帧实测）：音频帧 `{"code":0,"message":"","data":"<base64 分块>"}`——**`data` 是 base64 字符串非对象**，按序解码拼接透传；句级帧 `{"code":0,"data":null,"sentence":{"phonemes","text","words"}}` 丢弃；终止帧 `{"code":20000000,"message":"OK","data":null}`；错误帧 `{"reqid","code","message"}`（**55000000 "resource ID is mismatched with speaker related resource" = 音色未授权 2.0**）→ 网关 502 upstream_error 透传 message。**音色配对规则（A 级实测 + 官方配对表）**：`*_uranus_bigtts`/`saturn_*` 系 2.0 音色 ↔ `seed-tts-2.0`；`*_mars_bigtts`/`*_moon_bigtts`/`ICL_*` 系 1.0 音色 ↔ `seed-tts-1.0`——1.0/2.0 音色与资源错配即 55000000（两库不互通）。**分派与计量**：同一 volcengine adapter 按 model 分支（`IsVolcengineTTSStreamModel`）；计量走**字符口径**（官方计费维度语音合成 2.0 按字符 3 元/万字符 ÷7.0 = $4.2857e-05/字符，目录行 `ttsInputCostPerChar`——与 1.0 同步面的时长口径按 model 分流）。**经典资源对照（实测）**：`volc.service_type.10029` 对该语音账户 403 `45000030 not granted`。Mock：`media_volcengine_tts_stream_ok`（NDJSON 双分块）/ `media_volcengine_tts_stream_failed`（55000000 错误行）。设计：[火山流式语音桥接设计](火山流式语音桥接设计.md)（V1b 字符级流式回流与 WS 基座展望见该文档）。
**异步长转写（录音文件识别 2.0；2026-10-17 回填并经 `/v1/tasks/audio` 第二上游承接实施；置信度 A：官方文档 + 零计费探测 + 真实转写实测）**：现役资源 `volc.seedasr.auc`（豆包录音文件识别模型 2.0；**1.0 系资源 `volc.bigasr.auc`/`.auc.duration`/`.auc.predict`/`.auc.techo`/`.auc_idle`/`.auc_turbo` 与 sauc 流式系对该语音账户全部 45000030 not granted——未开通不是故障，承接不覆盖**）。通用大模型录音文件识别面（语音服务域，host 恒 `openspeech.bytedance.com`，与 ark base_url 无关——同 TTS URL 构造特例）：`POST /api/v3/auc/bigmodel/submit` 提交 + `POST /api/v3/auc/bigmodel/query` 轮询，**异步两段、无取消端点**（`SupportsCancel=false` 本地收敛 cancelled，沿 qwen/glm/minimax 先例）。鉴权 `X-Api-Key: <api-key>` 单头（凭据沿用 `speech_token` 单值，与 TTS 同一把新版语音控制台 API Key；旧版三头 `X-Api-App-Key`+`X-Api-Access-Key` 实测同通但网关不采用——零新凭据字段）+ `X-Api-Resource-Id: volc.seedasr.auc` + `X-Api-Sequence: "-1"`（官方示例固定值）。**头驱动受理协议（与本网关既有 body 驱动任务面不同构，adapter 按响应头判定）**：submit body `{"user":{"uid":"<网关标识>"},"audio":{"url":"<公网音频直链>","format":"<wav|mp3|ogg>"},"request":{"model_name":"bigmodel"}}`，**成功响应 body 为空 `{}`，成功标志在响应头 `X-Api-Status-Code: 20000000`**；**任务 ID = 网关自定义的请求头 `X-Api-Request-Id`**（非上游返回；query 用同一值轮询，网关落 `upstream_job_id`）。query 空 body `{}`：响应头 `X-Api-Status-Code` `20000001`（处理中）/`20000002`（队列中）→ 统一 `in_progress`；终态 `20000000` → body `{"audio_info":{"duration":<毫秒>},"result":{"text":"<全文>","utterances":[{"text","start_time","end_time","words":[...词级时间戳]}]}}`。状态归一（§2.6 volcengine 长转写臂）：`20000000`→`completed`、`20000001`/`20000002`→`in_progress`、`450000xx`（如 `45000001` 参数无效、`45000006` 未文档化——实测为音频 URL 拉取失败）→`failed`、未知值→`in_progress` 记 RawStatus 不猜测。统一面映射：`model`（目录行 `volc.seedasr.auc`，资源 ID 即 API 真实标识）→ resource id 头恒 `volc.seedasr.auc`、body `request.model_name` 恒 `bigmodel`（官方示例值）；`input_url` → `audio.url`（**只接受公网 URL，multipart 文件输入本地 400——上游只收 URL，与零存储约束对齐**）；`audio.format` 由 URL 后缀（`.wav`/`.mp3`/`.ogg`）推断，可经 `provider_options.volcengine.audio_format` 覆盖，都缺省 `wav`（上游不识别时显式报错回传，不静默）。**计量计费**：终态 `audio_info.duration` **毫秒**→÷1000 秒落 `audio_input_seconds`；缺失/非正值 → 0 计量 + `usage_missing` 不猜测。官方计费（2026-10-17 计费页核实，`getDocDetail` API 直取 MDContent）：后付费 **0.8 元/小时**（按音频时长、精确至秒、折小时计费；资源包折算 0.67~0.77 元/小时）÷3600 ÷7.0 → **$3.1746031746031745e-05/秒** 落目录行 `audioInputCostPerSecond`（模型 `volc.seedasr.auc`，CNY 四件套）。**境外音频源坑（实测）**：上游境内机房自拉 `audio.url`，境外源（upload.wikimedia.org）约 4 分钟 `in_progress` 后终态 `45000006` 失败；境内源即时成功——接入文档必须提示境内可达音频源。content 端点：火山无产物下载 URL（qwen `transcription_url` 同构物不存在），终态转写 JSON 即产物——content 请求 = adapter 产出的同构 query 请求（再查一次上游、响应体直接代理）。Mock：`media_volcengine_asr_create_ok` / `_poll_running` / `_poll_succeeded_text` / `_poll_failed`——随本承接批交付（`mockupstream/asr_volcengine.go`，端点 `/api/v3/auc/bigmodel/submit|query`，响应按头驱动协议回 `X-Api-Status-Code`，submit 成功 body 空）。
**Agent Plan 语音面未承接（2026-10-17 实测记录；2026-10-18 部分修正）**：Agent Plan 套餐语音走 `wss|https://openspeech.bytedance.com/api/v3/plan/tts/*` 与 `/api/v3/plan/sauc/*` 专属路径族，鉴权为三头形态（`X-Api-App-Key` + `X-Api-Access-Key` + `X-Api-Resource-Id`，豆包语音 2.0 控制台形态）+ 独立凭据体系（实测：ark API Key 作 `Authorization: Bearer` 或三头同值均 401 "grant not found"；通用面 `X-Api-Key` 亦不适用）——**该 plan 路径族仍不承接**。修正：语音合成 2.0 的**标准面**（非 plan）为 HTTP `/api/v3/tts/unidirectional` + `X-Api-Key` 单头（speech_token 可用，见上——"seed-tts-2.0 以 WS 流式为主、需三头"的旧论断仅对 plan 面成立），已随流式语音桥接 V1a 承接。

**M6 实施注（2026-10-04，TTS 面；2026-10-17 迁移批更新）**：`gatewaymedia/volcengine_tts.go` adapter 挂 `speechAdapters` 的 `volcengine` 条目；链上 `volcengineSpeechRequest` 分派（沿 minimax 同模式）。落地事实：出站 URL 恒 `https://openspeech.bytedance.com` 语音服务域 + `/api/v3/tts/create` 路径（与账户 ark base_url 无关的固定上游特例——语音域鉴权（`X-Api-Key`）与资源模型独立于 ark OpenAI 兼容根，账户 `base_url` 只承载 ark `/api/v3` 版本根，不经该出站拼接；`JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL` 仅为验收 E2E 的测试 seam，生产不设置）；凭据单值 `speech_token` = 新版语音控制台 API Key（api_key 账户凭据可选键，写侧仅 volcengine 放行，缺失即该账户无 audio_speech 能力——派发时显式失败，不静默回退；`speech_appid` 退役为可选遗留键，不再参与出站报文与鉴权）；统一面 `model` 真实透传（现役 seed-audio-1.0 目录行承载；历史占位名 doubao-tts 由 adapter 归一映射，见上）；`speed` 区间 [0.2,3.0] 词表外/越界本地 400（§2.4 规则 2）；响应转换 audio 缺失/解码失败错误上抛。档案 Capabilities 含 `tts` family；`audio_speech` 端点模式 opt-in（默认集维持 video_get）。计费：上游无字符回报 → 网关按请求字符自算计量照落、成本按字符价照计（§2.8）。账号接入见《火山方舟账号接入.md》TTS 节。

### 9.3 同步图像（Seedream；2026-11-11 同步批，置信度 A：官方文档 + 真实探针）

- 端点：`POST https://ark.cn-beijing.volces.com/api/v3/images/generations`，Bearer（账户 base_url 版本根 `/api/v3` 直拼 `/images/generations`）；Ark 图像面为 OpenAI Images 兼容形态，网关不重写路径。
- 请求：`model`、`prompt`、`image`（URL/Base64 图生图）、`size`（`1K/2K/4K` 档位或 `WxH`）、`response_format`（`url|b64_json`，默认 url）、`watermark`（bool，厂商默认 **true**）、`output_format`（png/jpeg）、`background`、`sequential_image_generation` + `sequential_image_generation_options.max_images`、`stream`（仅 Seedream 5.0 lite/4.5/4.0 支持，5.0 pro/flash 不支持）。
- 2026-11-11 真实探针：`doubao-seedream-5-0-flash-260915` + `n:1` + `size:"1024x1024"` + `response_format:"url"` + `watermark:false` → 200；响应 `created`/`model`/`data[].url`（TOS 签名 URL，`X-Tos-Expires=86400`）/`size`/`output_format:"jpeg"`/`background:"opaque"`；usage `input_images:0, generated_images:1, output_tokens:4096, total_tokens:4096`。
- 网关参数处理：`provider_options.volcengine` deep-merge（`image`、`sequential_image_generation*`、`layer_decomposition`、`optimize_prompt_options`、`tools`）；**网关首版默认显式传 `watermark:false`**，`provider_options.volcengine.watermark=true` 可覆盖；`quality`/`output_compression`/`safety_identifier`/`stream`/`partial_images` 忽略并回显 `params_ignored`；`size`/`response_format`/`output_format`/`background`/`n` 透传。`seed`/`guidance_scale` 官方页未文档化，首批不承诺。响应不改写。
- usage：`usage.generated_images` → `output_image_count`（缺失时 `data[]` 长度兜底）；按模型行 `output_usd_per_image` / 像素档表计费；`output_tokens` 为图像内部 token 核算，按张行由计费引擎置零豁免。输入图按张价（Seedream 5.0 Pro 首张免费、后续 0.02 元/张）从请求侧输入图张数扣免费额度后落 `image_input_unit`；生成面顶层 `image`、编辑面 JSON `images[]` 与 multipart `image`/`image[]` part 均计数（`mask` 不计）。
- **编辑（`/v1/images/edits`，2026-11-12 T216；置信度 A：官方「豆包的图生图也走 generations 接口」+ 插件实现交叉）**：Ark **无独立 edits 端点**，编辑请求改写为同一 `POST /api/v3/images/generations`：`image` 字段接受 HTTP URL 或 `data:image/<小写格式>;base64,...`，可为单字符串或数组（参考图上限按模型，≤15）；`mask` 不支持（忽略并回显 `params_ignored.mask`）；`n` 原样透传（上游以 `sequential_image_generation` 表达多图，网关不代改）；`watermark` 默认 false；响应同生成面 `data[].url` + `usage.generated_images`。
- Mock：`media_volcengine_image_ok`（created/model/data[].url/size/output_format/background + generated_images）、`media_volcengine_image_no_image`（2xx 无 data）。

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

**长转写（`/v1/tasks/audio` 首个上游）——M3f 已实施（2026-10-04，`gatewaymedia/qwen_asr.go`，对外端点族与任务面泛化见链上 `chain_media_video.go`/`chain_driver.go`；usage 时长字段钉 `duration` 秒（JSON 字符串/对象双形态兼容解析，沿 §10.1 先例）；创建无 X-DashScope-Async 头（天然异步，adapter ExtraHeaders 恒 nil）；取消面无 API 回填 → SupportsCancel=false 本地收敛 cancelled）**：
- 创建：`POST https://dashscope.aliyuncs.com/api/v1/services/audio/asr/transcription`，Bearer，**无需 X-DashScope-Async 头**（该服务天然异步）。请求：`{"model":"paraformer-v2","input":{"file_urls":["<公网音频 URL>"]},"parameters":{"language_hints":[...],"disfluency_removal_enabled":true,"diarization_enabled":true,"vocabulary_id":"..."}}`——**只接受公网 URL 数组，不支持文件上传**（与零存储对齐：对外 `/v1/tasks/audio` 首版输入只支持 `input_url`，multipart 文件输入待有文件上传型上游再开）。响应：`{"output":{"task_id":"...","task_status":"PENDING"},"request_id":"..."}`——受理凭据=`output.task_id`。
- 轮询：`GET /api/v1/tasks/{task_id}`（与万相视频同一 DashScope 任务接口）：`PENDING|RUNNING|SUCCEEDED|FAILED`；SUCCEEDED → `output.results[].transcription_url`（**转写结果 JSON 文件的下载链接**，内容含 `transcripts[].text`/句级时间戳/说话人——content 端点流式代理该 URL，产物为 JSON 非 audio）；FAILED → `output.message`；`usage`（JSON 字符串，时长计量）。
- 状态归一与万相同表（§2.6 qwen 列）；计费：paraformer 按时长人民币口径——无可查证 USD 不落价（0 计费+usage_missing 不编造），usage 时长计量照抽（`AudioInputSeconds`）。
- Mock：`media_qwen_asr_create_pending` / `_poll_running` / `_poll_succeeded_transcription_url` / `_poll_failed`——**M3f 已交付**（`mockupstream/asr_qwen.go`，创建端点按契约 §10.2 校验 model + input.file_urls 非空数组（缺失 400，参数错误透传断言点）；轮询/产物通道与万相共用 `/api/v1/tasks/{id}` 族（任务表按 id 分表查询），content 回转写结果 JSON 文件）。

CosyVoice（**契约事实修正 2026-10-04**）：官方分类为"实时语音合成"，接入协议为 **AOQ（QUIC）/WebSocket 双向事件流**（客户端事件/服务端事件），**非 REST/SSE 同步 TTS**——从同步 TTS 回填池移出，归 M6 流式语音转换池（与 Gemini Live 同类，届时经 Realtime 转换层接入）。
Mock（视频四场景 **M3 第五批已交付**，`mockupstream/video_qwen.go`——创建端点按契约 §10.1 校验 `X-DashScope-Async` 头，缺失即 400 同步调用拒绝；`_tts_ok` 随 TTS 接入交付）。

### 10.3 同步图像（Qwen-Image；2026-11-11 同步批，置信度 A：官方文档 + 真实探针）

- 端点：`POST https://dashscope.aliyuncs.com/api/v1/services/aigc/multimodal-generation/generation`，Bearer；**同步接口，无 task_id 轮询，无 OpenAI 兼容模式**。账户 base_url 为 origin（`https://dashscope.aliyuncs.com`）；若用户配置了 `/compatible-mode/v1` 或 `/v1` 版本根，adapter 出站前剥离该后缀再拼原生全路径。
- 请求：`{"model":"qwen-image-3.0","input":{"messages":[{"role":"user","content":[{"text":"..."}]}]},"parameters":{"size":"1024*1024","n":1,"negative_prompt":"...","prompt_extend":true,"watermark":false,"seed":42}}`。公共参数映射：`prompt`→`input.messages[0].content[0].text`；`size` WxH→`W*H`；`n`→`parameters.n`（1-6）；`provider_options.qwen` 的 `negative_prompt`/`prompt_extend`/`watermark`/`seed` 等 deep-merge 进 `parameters`。`quality`/`background`/`output_format`/`output_compression`/`stream`/`partial_images`/`safety_identifier`/`response_format` 忽略并回显 `params_ignored`（响应只回 URL）。网关首版默认 `watermark:false`。
- 响应：`{"output":{"choices":[{"message":{"content":[{"image":"https://..."}]}}]},"usage":{...},"request_id":"..."}`；2.0 usage `image_count/width/height`，3.0 usage `output_image_count/output_width/output_height/input_image_count/input_image_type/output_image_type`；3.0 `content[0].type="image"`、`output.rewrite_status="not_use"`。
- 网关响应转换：收集 `output.choices[].message.content[].image` → `{created, data:[{url}], usage:{output_image_count}}`；`output_image_count` 取实际收集到的图片数（2.0/3.0 的原生 usage 字段只作形状兼容提示，与图片数不一致时按实际图片数计费）；不合成 `model`（响应无 model 时 `upstream_response_model` 留空）；无可用图片 → 502 `upstream_protocol_error`；非 2xx 原状态/错误体透传。
- 2026-11-11 真实探针：`qwen-image-2.0`、`qwen-image-3.0` 同端点均 200；3.0 usage 形状与 2.0 不同，adapter 兼容两种。
- 计费：`output_image_count` → 按模型行 `output_usd_per_image`（qwen-image-3.0 输出 0.18 元/张 ÷7.0 已落；1K/2K 同价，无输出档）；输入图按张：qwen-image-3.0 / pro 输入图 0.02 元/张 ÷7.0 已落 `InputUsdPerImage`（无每请求免费张数），与 Seedream 同走 `image_input_unit`，计数覆盖 JSON `images[]` / multipart `image` part / `metadata.image`（§2.9 规则 6）。qwen-image-3.0-pro 已收录：输出 1K 0.25 / 2K 0.50 元/张落 `OutputUsdPerImageTiers`（1K 上限按 1024×1024 像素；官方只标 1k/2k 与最大 2048×2048，未给像素边界，目录口径已在 `sourcePricingNote` 注明）。
- **编辑（`/v1/images/edits`，2026-11-12 T216；置信度 A：插件实现 + 官方文档）**：与生成共用 `POST /api/v1/services/aigc/multimodal-generation/generation`（qwen profile 强制同步，禁用 async）。`input.messages[0].content` 由 `[{image:...}, ...] + [{text:...}]` 组成；图片必须是 `http(s)://`、`data:image/...` 或 `oss://`，**不接受无前缀裸 base64**；单图 ≤10MB（base64 上限）；`mask` 不消费（仅 wanx2.1-imageedit 使用，忽略并回显）；`quality`/`background`/`output_format`/`output_compression`/`response_format`/`input_fidelity`/`safety_identifier`/`user`/`stream`/`partial_images` 忽略并回显；`n`/`size`（`W*H`）/provider_options 同生成面。响应转换复用生成面（content[].image → data[]，实际图片数计费）。qwen 专用编辑模型（qwen-image-edit 系）未收录目录行，新增须先补官方价。
- Mock：`media_qwen_image_ok`（2.0 usage `image_count`）、`media_qwen_image_v3_ok`（3.0 usage `output_image_count`）、`media_qwen_image_no_image`（2xx 无 image）。

## 11. 与设计的衔接及前置裁决项清单

1. ~~§5.1 Gemini TTS 对外 `response_format` 约束（PCM 透传 vs 400）——M1 实施首日裁决并回填本节~~ **已裁决（2026-10-04）：pcm 透传、其余 400、零转码，见 §5.1**。
2. 剩余"待回填"项——§6.2 xai 语音（Grok Voice API 报文）、§7.2 GLM-ASR 模型名、§10.2 CosyVoice（归 M6 流式池）：对应期接入前完成回填，回填前不得实现该 adapter（§6.1 xai 视频、§7.2 glm 语音、§8.2、§9.2 TTS、§9.2 火山长转写、§10.2 长转写均已回填并实施）。
3. Mock 上游基建扩展（§3.2/§3.3）为 M1 首个交付物（先有 Mock 再写链路）；mockdata 域扩展随管理面能力同批。
4. 本文与《音频视频模型接入与统一媒体网关设计》冲突时，以设计文档的目标/边界为准、以本文的报文字段为准。

## 12. 来源记录（2026-10-04 检索）

- OpenAI audio/videos：OpenAI 平台 API 参考（Audio create transcription `verbose_json` 字段；video 对象 `id/object/status/progress/seconds_length/size/content`）。
- Gemini TTS：`ai.google.dev/gemini-api/docs/speech-generation`（speechConfig/prebuiltVoiceConfig/voiceName、PCM 24kHz 输出、30 预置音色）。
- Veo REST：多源交叉（`:predictLongRunning` → operation name → `GET /v1beta/{name}` → `generateVideoResponse.generatedSamples[].video.uri`，uri 时效约 2 天）。
- MiniMax：`platform.minimax.io` API 参考（video_generation / query/video_generation / files/retrieve，状态词表）。
- 火山方舟：官方任务接口格式（`/api/v3/contents/generations/tasks` 创建与 `{id}` 查询、`queued|running|succeeded|failed`、`content.video_url`）。
- 火山豆包语音长转写与计费（2026-10-17 承接批）：豆包语音文档站「录音文件识别标准版 HTTP」（submit/query 协议、资源 ID `volc.seedasr.auc`、响应头状态码语义）与「计费说明」（录音文件识别 2.0 后付费 0.8 元/小时、精确至秒折小时计费；1.0 对照 2.3/极速 4.5/闲时 1.2 元/小时）——SPA 正文 web_reader/WebFetch 均取不到，实际经文档中心 `GET https://docs.volcengine.com/api/doc/getDocDetail?LibraryCode=DoubaoVoice&DocumentCode=<Code>&lang=zh` 直取 `Result.MDContent`（参数名大小写敏感；计费页 `Billinginstructions-21`、概述页 `BillingOverview-15`，响应另带官方 PDFURL 可旁证）；协议与授权状态另经零计费探测（参数错误 vs 45000030）+ 真实转写实测复核，详见《火山方舟账号接入.md》语音识别节。
- 百炼：DashScope 异步任务约定（`X-DashScope-Async`、`GET /api/v1/tasks/{task_id}`、PENDING/RUNNING/SUCCEEDED/FAILED）。M3 第五批接入时经百炼官方 legacy 万相文生视频/图生视频 API 参考补全并实施：创建端点 `/api/v1/services/aigc/video-generation/video-synthesis`（无异步头时上游报 "current user api does not support synchronous calls"）、`input.prompt/negative_prompt/img_url`（图生视频公网 URL 与 Base64 data URL 双形态）、`parameters.size`（`1920*1080` 星号形态）/`duration`/`seed`、轮询 usage 字符串/对象双形态与 `video_duration`（wan2.5 及以下）/`duration`/`output_video_duration`（wan2.6）字段族、wan2.2-t2v-plus 档位（480P/1080P 全档、duration 固定 5 秒）。
- xAI：Grok API 定价页与评测（Voice API 三能力、grok-imagine 6/10s 480p/720p）。
- 智谱：社区与聚合层证据（CogVideoX 异步任务、ASR 流式）；官方文档搜索引擎收录有限，列为 C 级回填。
