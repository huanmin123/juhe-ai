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
| `profile_minimax_openai_v1` | `minimax` | `openai/v1` | `https://api.minimax.chat` | `api_key` | `video_generation`、`tts`（仅媒体） | `video_generation`、`tts` | `speech-02-turbo` |

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
- 默认集 `audio_speech`（与档案默认健康检查模型 `speech-02-turbo` 对应；同 glm 默认 chat 对的先例结构）。
- 前端（`accountProviderCapabilities.ts`）：minimax 档案的 endpoint modes 按 families 推导（`video_generation` → video 四值、`tts` → `audio_speech`），不回退 chat 词表；新账户默认集过滤后为空。

## 模型目录

M3 收录 2 行（`maintenance/internal/schema/model_catalog_data.go` + gateway 静态层 `pricing/data_minimax.go`）：

| 模型 | mode | 协议 | 定价 |
| --- | --- | --- | --- |
| `MiniMax-Hailuo-2.3` | `video` | `video` | 不落价（见下） |
| `speech-02-turbo` | `audio` | `audio_speech` | 不落价（见下） |

收录与维护规则见《厂商模型目录更新与清洗指南》MiniMax 节。

## 计价口径（不编造）

- **speech-02-turbo**：官方按量口径 ¥2.00/万字符（platform.minimax.cn 按量计费页「同步语音合成 T2A speech-2.6-turbo / speech-02-turbo 2.00」），官方无美元价；网关计价引擎仅 USD 且无官方汇率折算链 → **不落 `TtsInputUsdPer1MChars`**（自选汇率折算即编造）。字符计量照常落 usage（`extra_info.usage_characters` 回报优先，契约 §2.8），目录无价 → 成本不虚计（0）；官方美元口径可查证后再补字符价。
- **MiniMax-Hailuo-2.3**：官方按次/档位计费（分辨率 × 时长），platform.minimax.io 定价页为登录态 SPA，无可查证官方精确 USD 秒价（检索所得均为第三方聚合转售口径，不作数）；且轮询响应不回报时长 → **不落 `VideoOutputCostPerSecond`**，终态计费走契约 §2.8 兜底（0 计费 + `usage_missing` 标记），同 glm cogvideox 先例。

## 账号测试

- 档案默认健康检查模型 `speech-02-turbo`（TTS 面）。
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
