# MiniMax 账号接入

> 2026-10-04 M3 第三批接入（媒体设计 §9、《媒体上游协议契约与Mock上游规格》§8）：本文记录 MiniMax 供应商的接入结论、协议档案、能力边界、测试与计价口径。MiniMax 是 M3 新增的媒体专用供应商——只承接视频生成（Hailuo 系）与语音合成（speech 系），**不承接聊天流量**（MiniMax 聊天端点非 OpenAI Chat Completions 形态，档案与目录均不声明 chat 能力）。

## 范围

本文记录 MiniMax 供应商的接入结论、账户创建类型、协议档案、网关请求边界、模型目录与计价口径。运行事实以当前 Go 实现（M3 第三批，2026-10-04 交付）为准；报文级契约的权威来源是《媒体上游协议契约与Mock上游规格》§8（置信度 B：官方文档多源确认），本文不重复报文字段全文。

官方接入面（契约 §8 回填依据）：

- 国内平台 `platform.minimax.cn` / 海外平台 `platform.minimax.io`；API host 国内 `api.minimax.chat`、海外 `api.minimaxi.com`。
- 视频生成：`POST /v1/video_generation` + `GET /v1/query/video_generation?task_id=` + 产物下载（`file_download_url` 直连，契约裁决见下）。
- 语音合成：`POST /v1/t2a_v2`（同步）。
- 鉴权：API Key（`Authorization: Bearer <key>`）；无 OAuth 接入面。

## 凭据与 base_url

- 账户类型只有 `api_key`（MiniMax 无 OAuth 面向 API 接入，媒体设计 §9）。
- 凭据字段：`api_key`（必填）、`base_url`（可选覆盖）。
- 默认 `base_url` 为 `https://api.minimax.chat`（国内官方根，seed 档案值）；海外账户显式覆盖为 `https://api.minimaxi.com`。
- 出站 URL 归一：minimax 出站路径（`/v1/video_generation`、`/v1/t2a_v2`、`/v1/query/video_generation`）是 `/v1` 前缀的 openai 族形态，走 `gatewayopenai.BuildUpstreamURL`（base 强制 `/v1` 结尾、path 剥 `/v1` 前缀去重）——`https://api.minimax.chat` 与 `https://api.minimaxi.com`、以及账户带或不带 `/v1` 尾缀的 base_url 均归一到同一 URL，无需专用归一函数（对比 glm 的 `/api/paas/v4` 服务根专用归一 `chainGlmVideoUpstreamURL`）。

## 供应商与协议档案

```ts
type ProviderCode = 'minimax'
```

显示名称 `MiniMax`。`minimax` 是独立供应商（seed `pgSeedProviders`，M3 增），不复用 `openai` 聚合供应商；账号池、分组、模型价格与响应策略归属 `minimax`。

唯一协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | Capabilities | Endpoint Families | 默认健康检查模型 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `profile_minimax_openai_v1` | `minimax` | `openai/v1` | `https://api.minimax.chat` | `api_key` | `video_generation`、`tts`（仅媒体） | `video_generation`、`tts` | `speech-2.8-turbo` |

要点：

- **Capabilities 只声明媒体**（`video_generation`/`tts`，与 Endpoint Families 同名）：不声明 `chat`/`responses`/`passthrough`——MiniMax 聊天端点非 `/v1/chat/completions`，档案不得让聊天流量路由进来；模型门双保险（目录无 chat 模型行）。
- 协议 `openai/v1` 是媒体 adapter 的报文承载协议（出站路径经 openai 族 URL 归一），不是聊天透传声明。
- 内置分组：`grp_default_minimax_sys_admin`（默认 MiniMax 分组，seed `pgSeedGroups`）。
- 新增 openai 协议端点族（seed `pgSeedEndpointFamilies`，M3）：`openai_v1_video_generation`（code `video_generation`）、`openai_v1_tts`（code `tts`）——统一 `/v1/videos`、`/v1/audio/speech` 面承载的厂商原生媒体形态。

## 网关请求边界

### 视频（`POST /v1/videos` 统一面，M3 已实施 adapter）

- 创建报文改写（`gatewaymedia/minimax_video.go`）：`model`/`prompt` 直传；`seconds` → `duration` **数值秒直传**（Hailuo 支持数值档，不做换算猜测）；`input_reference` → `first_frame_image`（**仅 base64/data URL 直传**，url 形态本地 400——网关零资源存储不代为下载，沿 veo 裁决）；`prompt_optimizer` 等厂商个例经 `provider_options.minimax` deep-merge；`negative_prompt`/`n`/`seed`/`audio` 请求面无对应字段 → `params_ignored` 回显。
- 受理凭据 = 创建响应 `task_id`；`base_resp.status_code != 0` 视为受理前错误（不落 `media_jobs`）。
- 轮询 `GET /v1/query/video_generation?task_id=`：`Preparing`/`Queueing` → `queued`、`Processing` → `in_progress`、`Success` → `completed`（`file_download_url` 冻结进 `Artifact.ContentURL`）、`Fail` → `failed`（错误摘要 code 取 `base_resp.status_code` 数字串）；未知状态归 `in_progress` 记 `RawStatus`。
- 产物下载：契约 §8.1"`GET /v1/files/retrieve?file_id=` 或 `file_download_url`，以回填为准"——**取 `file_download_url` 直连**（无凭据，与 Veo uri / glm video_result.url 同族先例）。
- 取消：MiniMax 无上游取消端点 → `SupportsCancel=false`，网关不发上游请求直接本地收敛 `cancelled`。
- Capabilities：`NegativePrompt=false`、`Audio=false`、`Seconds=true`、`InputReference=true`、`N=false`、`Seed=false`。

### 语音合成（`POST /v1/audio/speech` 统一面，M3 已实施 adapter）

- 报文改写（`gatewaymedia/minimax_tts.go`）：`input` → `text`、`voice` → `voice_setting.voice_id`、`response_format` → `audio_setting.format`（词表 `mp3`/`pcm`/`flac`/`wav`，零转码，空缺省按 OpenAI 默认 `mp3`）；`speed` 厂商区间 **0.5–2.0**，公共区间 0.25–4.0 超出部分本地 400（契约 §2.4 规则 2，不裁剪不猜测）；`vol`/`pitch` 等厂商个例经 `provider_options.minimax` deep-merge。
- 响应转换：`data.audio` 是 **hex 编码**音频字符串——网关 hex→bytes 解码后以二进制透传（不得把 hex 字符串当音频下发，契约 §8.2 加粗约束）；content-type 按请求 `response_format` 推导（`mp3`→`audio/mpeg`、`wav`→`audio/wav`、`flac`→`audio/flac`、`pcm`→`audio/L16;rate=32000`——t2a_v2 响应不回显 mime）。`base_resp.status_code != 0` → 上游错误上抛。
- 异步长音频（长转写）：契约 §8.2 待回填，未实现。

### 账户 endpoint modes

- 词表（写侧 `normalizeMinimaxEndpointModesForWrite` + 读侧投影）：`audio_speech` + `video_create`/`video_get`/`video_content`/`video_cancel`——全部 opt-in；写侧对 chat/responses 等对话模式报错。
- 默认集 `audio_speech`（与档案默认健康检查模型 `speech-2.8-turbo` 对应；同 glm 默认 chat 对的先例结构；原 `speech-02-turbo` 随 2025 前模型清理批移除）。
- 前端（`accountProviderCapabilities.ts`）：minimax 档案的 endpoint modes 按 families 推导（`video_generation` → video 四值、`tts` → `audio_speech`），不回退 chat 词表；新账户默认集过滤后为空。

## 模型目录

模型目录唯一数据源为 `pricing/catalogdata/minimax.json`（go:embed 运行时 + maintenance DB 种子双通道，2026-10-17 缺口补录第二批后 19 行）；关键行：

| 模型 | mode | 协议 | 定价 |
| --- | --- | --- | --- |
| `MiniMax-Hailuo-2.3` | `video` | `video` | 按次主档（见下） |
| `MiniMax-Hailuo-2.3-Fast` | `video` | `video` | 按次主档（见下；2026-10-17 缺口补录批） |
| `MiniMax-M2.5` / `M2.5-highspeed` / `M2.1` / `M2.1-highspeed` / `M2` | `chat` | `chat_completions` | 历史模型 2.1/8.4 元、缓存读 0.21、缓存写 2.625 元/百万 tokens ÷7.0（highspeed 4.2/16.8；2026-10-17 缺口补录第二批，官方页无发布日期与上下文窗口，字段留空） |
| `MiniMax-H3-Regeneration` | `video` | `video` | 768P→2K 再生成 0.30 元/秒 ÷7.0（2026-10-17 缺口补录第二批；输入素材重计费见下） |
| `MiniMax-H3-Context-IR` | `chat` | `chat_completions` | 任务价 5.80/23.00 元/百万 tokens ÷7.0（2026-10-17 缺口补录第二批；任务归属接口官方未明示，mode 按 token 计价口径归 chat） |

收录与维护规则见《厂商模型目录更新与清洗指南》MiniMax 节。

## 计价口径（不编造）

2026-10-17 全供应商口径核对批后，MiniMax 目录主口径为**国内站人民币明文 ÷ 约定汇率 7.0**：账户协议档案实连 `api.minimax.chat` 国内站（账单人民币），国际站 USD 明文与国内价并不同步（实证：speech-2.8-hd 国内 3.50 元/万字符 ≈ $5e-05/字符 vs 国际 $1e-04/字符），不再直落国际价。全部 19 行单价与分档留档见 `catalogdata/minimax.json` 各行 `sourcePricingNote`（数据源与核对方式见《模型价格数据源与取证指南》台账），锚点钉值在 `catalogdata_price_anchors_test.go`。要点：

- **TTS 四行**（speech-2.8-hd/turbo、speech-2.6-hd/turbo；`speech-02-turbo` 已随 2025 前模型清理批移除）：国内同价 hd 3.50 元/万字符、turbo 2.00 元/万字符（同步/异步同价），÷7.0 落 `TtsInputUsdPer1MChars`；计费 1 汉字=2 字符。字符计量照常落 usage（`extra_info.usage_characters` 回报优先，契约 §2.8）。
- **asr-1.0 已随 2025 前模型清理批移除**（2.50 元/小时 ÷3600 ÷7.0 的秒价口径随行留档于 git 历史；现役 STT 秒价载体见 qwen3-asr-flash）。
- **MiniMax-H3 / H3-Max**：768P 主档 0.50 元/秒 ÷7.0 落 `VideoOutputUsdPerSecond`（H3-Max 的 480P 低档 0.33 元/秒留注记；输入素材：H3 图片 5 张内免费/超出 0.20 元/张、H3-Max 2 张内/0.50 元，留注记）。
- **MiniMax-Hailuo-2.3-Fast**（2026-10-17 缺口补录批）：按次/档位计费与 Hailuo-2.3 同规则，按 768P-6s 主档 1.35 元/条 ÷7.0 落 `VideoOutputUsdPerCall`（768P-10s 2.25 元、1080P-6s 2.31 元留注记）；不落每秒价（按次 → 每秒是推导，裁决 §2.4 维持）。
- **MiniMax-Hailuo-2.3**：按次/档位计费（分辨率 × 时长），按 768P-6s 主档 2.00 元/条 ÷7.0 落 `VideoOutputUsdPerCall`（一次任务 = 一次调用，契约 §2.8 按次计费；768P-10s 4.00 元、1080P-6s 3.50 元留注记）；按次 → 每秒是推导，禁止折算（裁决 §2.4 维持）——不落 `VideoOutputCostPerSecond`；轮询响应不回报时长（契约 §8.1 query 响应仅 status/file_id/file_download_url），按次行不依赖秒计量。
- **image-01**：0.025 元/张 ÷7.0 落 `OutputUsdPerImage`（image-01-live 同价留注记）。
- **M3 / M2.7 / M2.7-highspeed**：国内 2.10/8.40（M3 ≤512k 永久五折实付，恰与国际明文 $0.3/$1.2 等价）、M2.7 2.1/8.4、highspeed 4.2/16.8 元；缓存读 0.42 元、缓存写 2.625 元/百万 tokens；M3 >512k 恢复刊例（恰为 ×2 长上下文分档）、优先服务 1.5 倍留注记。
- **M2 家族五行**（M2.5/M2.5-highspeed/M2.1/M2.1-highspeed/M2；2026-10-17 缺口补录第二批）：定价概览历史模型小节 2.1/8.4 元、缓存读 0.21、缓存写 2.625 元/百万 tokens（highspeed 4.2/16.8，缓存价同档）÷7.0；官方页无发布日期与上下文窗口，`releaseDate`/`contextWindowTokens` 不臆造留空（公开发布时间线 M2 2025-10 起满足 2025-01 收录下限）。
- **MiniMax-H3-Regeneration / H3-Context-IR**（2026-10-17 缺口补录第二批）：前者 768P→2K 再生成按输出秒 0.30 元/秒 ÷7.0 落 `VideoOutputUsdPerSecond`（原 768P 任务输入素材重计费：视频 0.30 元/秒、图片 5 张内免费/超出 0.15 元/张、音频免费，无槽位留注记）；后者为「任务价格」小节 token 计价 5.80/23.00 元/百万 ÷7.0，任务归属接口官方未明示，mode 按 token 计价口径归 `chat`。
- **不收录裁决**：`speech-02-hd`（speech-02 系已随 2025 前模型清理批用户裁决删除，保持一致）、Music 系（官方明文已下线，2026-08-20 起不再面向新用户）、`Hailuo-02`（2024 年发布，早于 2025-01 收录下限）。

## 账号测试

- 档案默认健康检查模型 `speech-2.8-turbo`（TTS 面；2025 前模型清理批换入）。
- 视频不做自动真实生成探针（按秒/按次计费成本高，媒体设计 §11.9），仅支持手动测试。

## 测试与验证

- Mock 上游（`shared/platform/mockupstream/video_minimax.go`，契约 §8.1/§8.2 Mock 行）：`media_minimax_create_ok`（#1 Preparing → #2 Queueing → #3+ Success）、`media_minimax_poll_running`、`media_minimax_poll_success`、`media_minimax_poll_fail`（base_resp 1004）、`media_minimax_tts_ok`（data.audio hex 载荷 + extra_info；usage_characters 刻意 +1 偏离请求字符数以证明网关回报优先）。
- 单测：`gatewaymedia/minimax_video_test.go`（视频/TTS adapter 报文与归一）；链级：`cmd/juhe-ai-gateway/chain_media_video_minimax_test.go`（生命周期/Fail/取消/TTS 全链 hex 解码/speed 400）；E2E：`acceptance/fullchain_media_video_test.go` `TestFullchainMediaVideoMinimaxHailuo`。

## 实施清单

- seed：`pgSeedProviders`/`pgSeedEndpointFamilies`/`pgSeedProfiles`/`pgSeedGroups` 增 minimax 行（计数门禁 9/12/14/9、profile family 绑定 32）。
- 后端：`gatewaymedia/minimax_video.go`、`gatewaymedia/minimax_tts.go`、注册表与 `chainVideoAdapterKeyOfProvider` 映射、`chain_driver.go` minimax speech 分派、`gatewayresponse/mediastream.go`+`nonstream.go` 转换与计量、写侧 `endpoint_modes.go` minimax driver、`model_mapping_protocol_matrix.go` minimax 协议承接。
- 计费：`pricing/data_minimax.go` + `providerCatalog`/`billingPolicies` minimax 条目。
- 前端：`providerProtocol.ts` minimax 常量、`accountProviderCapabilities.ts` minimax 分支。

## 验证要求

- `go build ./projects/gateway/... ./projects/maintenance/...`。
- `go test -count=1 -timeout 240s -run 'Minimax|Hailuo|Video|MediaJob|Speech' ./projects/gateway/cmd/juhe-ai-gateway/ ./projects/gateway/internal/gatewaymedia/... ./projects/gateway/internal/pricing/... ./shared/platform/mockupstream/... ./projects/maintenance/...`。
- `JUHE_AI_E2E_FULLCHAIN=1 go test -count=1 -timeout 420s -run 'TestFullchainMediaVideo' ./projects/gateway/cmd/juhe-ai-gateway/acceptance/`。
- `cd frontend && pnpm typecheck`。
