# 深挖复审问题记录：媒体域与协议闭环（2026-11-06）

对应台账：`docs/plans/deep-dive-review-媒体与协议闭环-20261106.md`
模式：report-only（未修改源码）。证据分两类：`runtime`（本地 dev 网关 + 真实上游实测）与 `code`（file:line）。
实机环境：dev gateway `127.0.0.1:3300`，真实上游账户（火山方舟官号、api.zeekai.cc 中转）。审查 API Key 仅存在于 dev。

---

## F1（高｜计费缺陷）GPT 图像族成功请求零计费

- 现象（runtime）：`POST /v1/images/generations`，`model=gpt-image-2`，上游 200、返回 1.38MB b64 图、`usage.output_tokens=343`；usage 记录 `success=true`、`statusCode=200`、`inputTokens=11`、`outputTokens=343`，但 `costUsd` 字段缺失（NULL，非 0），无任何计费行项。对照同一网关 `doubao-seedream-5-0-flash` 成功请求 `costUsd=0.01714` 正常。
- 根因链：
  1. `backend-go/projects/gateway/internal/gatewayopenai/usage.go:49-72` `applyImageOutputCountFromRoot`：只要响应根 `data[]` 每项含 `b64_json` 或 `url`，就写入 `OutputImageCount=len(data)`（不区分该模型是按张价还是按 token 价）。
  2. `backend-go/projects/gateway/internal/pricing/billing.go:511-513`：`if nonNegative(input.OutputImageCount) > 0 && rates.OutputUsdPerImage == nil { return nil }` —— 有张数但无按张价时整单 breakdown 直接为 nil。
  3. `backend-go/projects/gateway/internal/pricing/catalogdata/gpt.json`：全部 7 个 `gpt-image-*` 行只有 `outputCostPerImageToken`（如 gpt-image-2 = 0.00003），没有 `outputCostPerImage`。
- 后果：所有走 `/v1/images/generations` 的 gpt-image 成功请求都不计费；Seedream/GLM 等按张价模型正常。属于“上游已产生真实成本、网关记 0”的收入/配额漏洞。
- 分类建议：A（内部正确性/计费契约，合法用户无感知；文档《模型价格与用量统计口径》“图像按张或 token 计价”已承诺）。需补测试：token 价图像行 + data[] 响应必须产出 image token 行项。

## F2（高｜调度缺陷）缺必填凭据的账户仍进入候选并终态失败，单个错配账户可让整组模型不可用

- 现象（runtime）：
  - 新建火山音频账户 A（`supported_endpoint_modes` 含 `audio_job_*`，但凭据只有 `api_key`、无 `speech_token`）与账户 B（含 `speech_token`），同组、同模型 `volc.seedasr.auc`。
  - `POST /v1/audio/jobs` 持续返回 400 `speech_credentials_missing`，错误指向 A；即使 B 完全可用，请求不会切到 B。
  - 将 A 置为 `disabled` 后，同一请求立即 200（`audiojob_4a1c879779147db69c0eece3`，随后 completed，content 返回转写全文）。
- 根因链：
  1. `chain_driver.go:1902-1920` `chainVolcengineSpeechCredentials`：`speech_token` 缺失返回 `GatewayRequestValidationError`（400，确定性配置错误）。
  2. 该验证错误被显式设计为“终态上抛、不进候选切换”（同处注释），因此不会尝试下一个账户。
  3. 候选过滤只消费账户声明的 endpoint modes，不消费凭据是否满足该 mode 的必填项；见 `internal/accounts/endpoint_modes.go:83-88` 注释“缺失即该账户无该能力，派发时显式失败”。
- 后果：一个历史/导入的错配账户可以永久遮蔽同模型的健康账户，表现为“模型存在但 400”。对用户是可用性事故，对运营是难定位的配置错误。
- 分类建议：A（防御收紧/可用性修复：缺凭据账户应被候选过滤排除，或按“账户不具备该能力”跳过而非终态）。文档需同步《火山方舟账号接入》《AI 账户上游接口能力设计》。

## F3（中高｜计费与任务语义）取消/过期任务不计上游实际成本；已完成任务的 DELETE 是 204 no-op

- 现象（runtime）：
  - 创建 4s 视频任务 `video_928bd31e6b16fce7df384470`，3 秒后 `DELETE /v1/videos/{id}` → 204；本地轮询为 `cancelled`；直接查火山上游该任务仍为 `running`；`media_jobs.cost_usd=0`。
  - 已完成任务 `video_a535341439f25b5f0398ea67` 执行 DELETE → 204；随后轮询仍 `completed`，`content` 仍 200 可下载 2.24MB mp4。
- 根因链：
  1. `chain_media_video.go:1612-1635`：`SupportsCancel=false`（火山/GLM/MiniMax/xAI 均无上游取消端点，见各 adapter）时直接本地 `UpdateStatus(cancelled)` 并返回 204，不发上游请求。
  2. `internal/gatewaymedia/jobsrepo.go:219` 终态守卫 `status NOT IN ('completed','failed','cancelled','expired')`；cancelled/expired 后即使上游完成也不会再 `UpdateTerminal`，不产生 usage 与成本。
  3. 取消路径不查询上游实际计量；`chain_media_video.go` 注释与《音频视频模型接入与统一媒体网关设计》§10“取消任务按上游实际计量为准”不一致。
  4. 已完成行 DELETE：`UpdateStatus` 0 行，但仍返回 204（`chain_media_video.go:1635-1652`），本地行不删除、产物不失效。
- 后果：上游取消不掉的任务继续消耗配额并计费，网关成本口径漏记；DELETE 语义与“删除”不一致，客户端无法区分“已取消”和“什么都没发生”。
- 分类建议：B（可观察行为语义；需产品裁决：取消后是否保留 in_progress 可观测、是否查询上游 usage、DELETE 是否物理删除本地行）。C 类产品取舍部分单列。

## F4（已证伪｜仅保留 C 类诊断建议）base_url 版本段缺失被写侧接受

- 独立证伪结论：版本段由账户 `base_url` 承载是《供应商与上游协议面重构设计》明文契约（运行时不做补插/剥离/去重）；`connection_base_url.go:19-21` 明确 `allowed_base_path_suffixes` 自 R8 起不参与写面判定。因此“写侧未强制版本段”不是缺陷，裸根对 qwen 等代理前缀是合法形态。
- 保留的可改进项（C 类，非缺陷）：错配裸根在运行时的表现是 404 循环或 200-HTML→502，缺少“请检查 base_url 是否包含版本段”的诊断提示。建议在错误文案/审计中补充配置提示，不新增校验。
- 原观察（runtime）：Anthropic base 缺 `/v1` → 502 `upstream_protocol_error`；Gemini base 缺 `/v1beta` → 404 循环；补版本段后均 200。

- 现象（runtime）：
  - Anthropic 账户 `base_url=https://api.zeekai.cc`（preset 允许该形态）→ 网关请求 `/messages`（缺 `/v1`），上游返回 `200 text/html` 首页 → 网关 502 `upstream_protocol_error`（“上游成功响应不是有效 JSON”）。
  - Gemini native 账户 `base_url=https://api.zeekai.cc`（缺 `/v1beta`）→ 网关拼出 `/models/...:generateContent` → 上游 404；usage 循环记录 404。
  - 分别改为 `https://api.zeekai.cc/v1` 与 `https://api.zeekai.cc/v1beta` 后，两条原生协议均 200（Anthropic 3.0s，Gemini 3.1s）。
- 根因：`shared/contracts/connector_registry.go:411-425` 的 `AllowedBasePathSuffixes` 对 anthropic/gemini 同时允许 `""` 与 `"/v1"`/`"/v1beta"`，但 operation template 不含版本段（`POST /messages`、`POST /models/{model}:generateContent`）；URL builder 只做 `base + opPath`，不补版本段。
- 后果：用户可保存“合法”但必然失败的连接配置；失败表现为 502 协议错误/404 循环，误排到“上游故障”，而不是配置错误。
- 分类建议：A（写侧校验：允许的 base 形态必须能拼出 operation path；或 URL builder 补版本段）。文档同步《Anthropic 账号接入》《Gemini 账号接入》《供应商与上游协议面重构设计》。

## F5（低｜文档-实现不一致）媒体 lane 的“独立并发上限”未实现

- 证据（code）：`internal/gatewaydispatch/enginehelpers.go:154-161` `accountConcurrencyLaneAcquireOptions` 仅对 `LaneImage` 使用 image lane 计数；`LaneAudio`/`LaneVideo` 一律 `Lane: "text"`。
- 契约：《音频视频模型接入与统一媒体网关设计》§3“每车道独立超时档位”“每车道独立并发上限与 raw body 限额”。
- 实际：audio/video 只有独立超时档（`gatewayrouting/timeoutprofile.go:54-62`），没有独立并发上限；并发计数记入 text lane（当前无 text 专属上限消费者，因此暂无可观察功能影响，但记账语义与文档不符）。
- 分类建议：A（文档回正）或 C（若产品确实要 media lane 独立上限）。需维护者裁决哪边为准。

## F6（低｜待定）图像 lane 的“由文本升级”路径可能绕过媒体亲和清空

- 证据（code）：`gatewaypreauth/preflight.go:938-951` 在入口 lane 判定后解析会话亲和键；`:1067-1075` 才按候选账户能力把 lane 升级为 image；`SessionAffinityKey` 原样带出（:1247）；`gatewaydispatch/preparation.go:692` Claim、`attemptoutcomes.go:100` Remember 无 lane 门。
- 影响：仅当 `/v1/chat/completions` 请求的模型最终只匹配到图像账户时触发，会把图像账户写入会话亲和。未在实机复现（需要构造该模型/账户组合）。
- 分类建议：A（内部一致性；若确认触发路径可达）。需补：针对“text 入口 + image 候选”添加测试。

## F7（信息｜待定）usage 记录混有 attempt/探针行，统计口径需确认

- 现象（runtime）：usage 列表同时出现 `trafficSource=gateway` 的终态行与 `trafficSource=cooldown_retest` 的探针行；失败尝试行 `statusCode` 为空、无成本。`statreads/usagerecords.go` 未见 attempt/final 过滤。
- 影响：若前端/统计按行计请求数，探针与失败尝试会被计入；成本不受影响（失败行无成本）。
- 分类建议：C（统计口径裁决）。需确认“使用记录=请求事实”是否包含后台探针。

## F8（信息｜契约现状）OpenAI Chat 入口请求 Anthropic/Gemini 模型返回 503 可重试语义

- 现象（runtime）：`/v1/chat/completions` + `claude-sonnet-5` → 503 `anthropic_native_unsupported`；+ `gemini-3.8-flash` → 503 `gemini_native_unsupported`。
- 契约：当前只保留 OpenAI Responses→Chat 的账户映射 bridge，chat→messages / chat→generateContent 不是受支持方向（《架构总览》§协议接入）。
- 问题：错误码为 `service_unavailable`（可重试），客户端 SDK 会持续重试一个永久性协议不匹配。建议 400/404 语义或文档明示。
- 分类建议：C（错误语义与产品边界裁决），不计入缺陷修复流。

---

## 已证伪条目（保留一行）

- 火山 TTS `duration` 毫秒/秒换算错误：实测 `seed-audio-1.0` 请求实际 mp3 时长 4.467s，计费 `audio_input_seconds=4.4`，无千倍放大（B-H4 证伪）。
- 图像 b64 响应 >256KB 必然零计费：实测 1024/2048 b64 响应均 <2MB，`costUsd=0.01714` 正常（seedream 按张价）；C-H1 的 2MB 截断路径未触发（保留代码风险，不作为确认项）。
- 视频按秒计量：4s 任务 `outputVideoSeconds=4`、`costUsd=0.5656`，`params_ignored` 的 seconds 不参与自算（F-H12/A-H13 证伪）。
- TTS 字符按 byte 误计：`speechmeter.go` 按 rune；实测 13 字符 → 13（证伪）。
- `ns_json` 与 `images_json` 互斥缺陷：词表不存在 `ns_json` token，图像只有 `images_json`（C-H15 证伪）。
- 跨 API Key 读取他人媒体任务：任务查询绑定 `api_key_id` + owner；实测用户列表不含他人任务（证伪越权）。

---

## N1（中｜性能/索引）media_jobs 缺 owner/created 索引，用户任务中心与 retention 走全表扫描

- 证据（code）：`maintenance/internal/schema/pg_schema_business_indexes.go:1214-1228` 仅有 `idx_media_jobs_api_key_created(api_key_id, created_at DESC)`、`idx_media_jobs_account(account_id)`、`idx_media_jobs_status(status)`；`gatewaymedia/jobsrepo.go:314-326` 用户面按 `owner_system_account_id` 过滤、`:363` `COUNT(*)`、`:434` `ORDER BY created_at DESC`。
- 影响：2026-10-24 归属列改为 `owner_system_account_id` 后，用户面主读路径无索引；表行量受 7 天 TTL 约束时影响有限，但创建速率高或积压时线性劣化。
- 分类建议：B（性能修复，结果等价；需确认是否补 `(owner_system_account_id, created_at DESC)` 与 `(status, created_at)`）。

## N2（中低｜可观测）任务面上游传输错误无任何服务端日志

- 证据（code）：`chain_media_video.go:2106-2113` `renderMediaVideoTransportError` 只回泛化 503，不调用 Logger；调用点 `:1466-1468`（poll）、`:1573-1575`（content）、`:1636-1639`（cancel）。
- 影响：轮询/取消/下载遇到 dial 超时、TLS、代理不可达时，运维无法定位账户/上游/成因；与“任务事实由 usage 记账与运行日志覆盖”的口径不符。
- 分类建议：A（日志补全，不改变客户端行为）。

## N3（低｜可观测）管理/用户面 content 流中断被静默吞掉

- 证据（code）：`chain_media_jobs_center.go:119` `_, _ = io.Copy(w, response.Body)`；对照 `/v1` 面 `chain_media_video.go:1598-1603` 有 `media_job_content_stream_interrupted` Warn。
- 影响：任务中心下载不完整时服务端零痕迹。
- 分类建议：A（日志补全）。

## N4（中｜容量）media-jobs retention 吞吐上限 1.2 万行/天且无追尾循环

- 证据（code）：`jobs/internal/retention/mediajobs.go:59` `mediaJobsRetentionBatchSize=500`；`:96` 单轮只执行一次 `DeleteExpiredBefore`；`jobregistry/schedule.go:169-173` interval=1h、timeout=2min。
- 影响：持续创建 >1.2 万行/天或一次性积压时，超期行永久滞后，表增长并放大 N1。
- 分类建议：B（容量修复：单轮循环清到空或按预算；需评估上游表增长）。

## 独立证伪轮记录（R2）

| 报告 | 结论 | 关键证据 |
| --- | --- | --- |
| F1 | confirmed | `chain_usage.go:616/622` 传入 OutputImageCount；`billing.go:511-513` 拒绝整单；gpt.json 无 outputCostPerImage；jobs 侧重算同构 |
| F2 | confirmed | `candfilters.go:81-110` 只查 endpoint modes/模型；`attemptoutcomes.go:553-557` validation→rethrow；`upstreamdispatch.go:1092-1094` 终止整次派发 |
| F3 | confirmed | `chain_media_video.go:1455-1458/1625-1652`；`jobsrepo.go:181-229`；jobs 无取消后回填路径 |
| F4 | refuted | `connection_base_url.go:19-21/119-124`（suffix 不参与写面）；`供应商与上游协议面重构设计.md:86/:129`（版本段由版本根承载、裸根合法） |
| F5 | confirmed | `enginehelpers.go:155-161`；组策略仅 `imageLaneMaxConcurrency` |
| F6 | confirmed（代码层） | `preflight.go:333-334/938-951/1067-1075/1247`；`preparation.go:683-734`；`attemptoutcomes.go:100` |

---

## 修复批次（2026-11-06，用户授权 fix 模式）

| 项 | 修复内容 | 落点 | 验证 |
| --- | --- | --- | --- |
| F1 | token 价图像行不再因缺按张价拒绝整单；`OutputImageCount` 只服务按张行 | `pricing/billing.go` | 单测 `TestBuildCostBreakdownTokenPricedImageRowBillsImageTokens`；实机 gpt-image-2 `costUsd=0.01035`（修复前 NULL） |
| F2 | 火山语音域候选增加 `speech_token` 凭据门；全池缺凭据时保留专用错误码与文案 | `chain_driver.go`、`gatewaydispatch/candidatefilter.go` | 单测 `TestChainDriverVolcengineSpeechCredentialCandidateGate`；实机启用错配账户后 `/v1/audio/jobs` 200 且 `media_jobs.accountId` 命中健康账户（修复前 400） |
| F3（可观测部分） | 本地取消补 `media_job_cancel_local_only` Warn；任务面传输失败补 Warn | `chain_media_video.go` | 编译通过；取消语义与成本口径仍待 C6/C7 裁决 |
| F5 | 文档回正：audio/video 无独立 lane 并发上限，仅 image 有 | `音频视频模型接入与统一媒体网关设计.md` §3 | 文档审查 |
| F6 | text 入口升级 image lane 时清空三类亲和输入 | `gatewaypreauth/preflight.go` | 单测 `TestConversationEntryLaneUpgradeToImageClearsAffinity` |
| N1 | 新增 `idx_media_jobs_owner_created` / `idx_media_jobs_status_created` | `maintenance/internal/schema/pg_schema_business_indexes.go` | schema golden / 幂等测试通过；部署时由 `maintenance upgrade`/ensure-schema 应用 |
| N2 | 任务面上游传输失败补 Warn（含代理不可用） | `chain_media_video.go` | 编译通过 |
| N3 | 任务中心 content 流中断补 Warn | `chain_media_jobs_center.go` | 编译通过 |
| N4 | retention 单轮分批清到空/上限 20 批，仍受 ctx timeout 约束 | `jobs/internal/retention/mediajobs.go` | 单测 `TestMediaJobsRetentionDrainsMultipleBatches` / `TestMediaJobsRetentionStopsOnEmptyBatch` |

未修（登记为待裁决）：F3 的上游成本对账口径（C6）、已完成任务 DELETE 语义（C7）、跨协议 503 语义（C8）、audio/video 独立 lane 上限（C9）、usage 探针统计口径（C10）。

## 裁决记录（2026-11-06，用户逐条确认）

| 项 | 裁决 | 落地 |
| --- | --- | --- |
| F3 / C6 取消成本 | 本地取消后不再追计上游成本；上游若继续计费，按可接受的一次性损失处理。 | 媒体设计 §10 回正；取消日志 Warn 保留；不实现后台对账 |
| F3 / C7 已完成 DELETE | 保持幂等 204，终态行与产物保留至 7 天 TTL。 | 媒体设计 §4.2 明确 |
| F8 / C8 跨协议 503 | 改 400 `invalid_request_error`，带 Anthropic/Gemini 原生入口提示。 | `candidatefilter.go` 已实施；单测 `TestNativeProtocolMismatchExhaustionReturns400` |
| F5 / C9 lane 并发 | 保持现状（audio/video 复用账户硬并发），文档已回正。 | 媒体设计 §3 |
| F7 / C10 usage 探针 | 保留探针/attempt 记录，成本不虚计；口径文档说明，后续可加 trafficSource 筛选。 | 模型价格与用量统计口径 §2.1 |
- C8 实机复核（2026-11-06，dev 重启后）：`/v1/chat/completions` + `claude-sonnet-5` → 400 `anthropic_native_unsupported`；+ `gemini-3.8-flash` → 400 `gemini_native_unsupported`；普通豆包对话仍 200。
