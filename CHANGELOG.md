# Changelog

本文件自首个版本 tag 起维护（《升级兼容与迁移账本设计》§16 发布契约；版本号 = CalVer `vYYYY.MM.DD`，由 `deploy.sh` 干净树部署时自动打，无需人工记忆）。每个发布版本固定四节：`Breaking` / `Upgrade notes` / `Rollback` / `Pre-1.0 behavior notes`；无内容的节写「无」。

- `Upgrade notes` 必须回答：是否需要停写、是否需要备份、`maintenance upgrade` 会自动执行哪些步骤、是否有必须手工执行的独立命令。
- `Rollback` 二值：`binary-only`（换回上一版二进制即可）或 `backup-required`（只能恢复升级前备份）。
- `Pre-1.0 behavior notes` 承载首个 tag 之前历史批次的可见行为变更披露与核验 SQL（自 `docs/deploy/部署指南.md` §4 流水账迁入）。

## Unreleased

### Breaking

- 视频对外面只保留 `/v1/video/generations`（创建/列表/轮询/content/取消）；`/v1/videos*` 已删除，回落 404，无重定向或兼容层。
- 视频/语音/长音频媒体面的扩展通道只保留 `metadata`；`provider_options` 退役并从这些面的出站报文剔除（图像面按 P2 adapter 落地时同规则开放）。
- `metadata` 扩展通道收紧为**只新增路径**：任何与规范映射路径同名的键（即使本次请求因未传参数而未写入基础报文，如未传 size 时的 `parameters.size`、未传 seconds 时的 `parameters.duration`；含 `contents`、`generationConfig` 等嵌套路径）一律 400 `metadata_conflict`，不再覆盖既有值。原先依赖 `metadata` 改写规范参数的客户端会开始收到 400；这是计费安全边界（防止低档位请求经 metadata 抬高上游实际工作量而按原快照计费）。
- 单 IP 并发限制从 `groups.scheduling_policy_json.clientIpConcurrencyLimit` 迁至 `api_keys.client_ip_concurrency_limit`（schema revision 25）；新 Key 默认 `-1`（不限制），不自动继承旧分组值。
- 非对话媒体统一计价启用（schema revision 26）：`models.media_pricing_json` 为权威价格，媒体行按 `second`/`image`/`call` + 分辨率档结算；未配置 `media_pricing_json` 的媒体行继续 legacy 口径，非法模板按无价处理、不回落静态 legacy 价。历史在途任务有创建时快照按快照结算，无快照回退当前目录（非法则 0 成本 + `usage_missing`）。

### Upgrade notes

- 需要停写并完成可恢复备份。生产当前 revision 24（revision 25 未执行）时执行 `./deploy.sh upgrade`（内部 `upgrade --phase=all`，随后自动执行 `migrate-media-pricing`）；不要用 `--phase=expand`，管线会因 revision 25 为启动前必需数据迁移而 blocked。
- 库已到 revision 25 时可用 `upgrade --phase=expand` 保留 `min_readable_binary_revision=25` 的回滚窗口，观察稳定后再 `upgrade --phase=contract --old-writers-drained --backup-confirmed` 关闭窗口。
- 手工窗口在 `upgrade` 后、启动新二进制前执行 `migrate-media-pricing`（先 `--dry-run --manifest-out` 核对 `manual_review`，再带 `--stop-write-confirmed --backup-confirmed` 正式执行）；`deploy.sh upgrade` 已自动执行该步。
- 启动前先跑 `migrate-key-client-ip-limit --dry-run` 核对 `conflicts`/`needsReview`；存在歧义时先人工收敛，避免 upgrade exit 3。
- 模型目录共享缓存键版本前缀升至 `v3:`：旧二进制写入的 24h 目录条目既缺 `mediaPricing` 字段、也不包含本批新增的 `paraformer-v2`/隐藏的 `qwen3-asr-flash` 行集，新二进制不再命中；升级后首次查询自动回源装载，旧键按 TTL 自然回收，无需手工清 Redis。
- 本批无新增 schema revision（HEAD 仍为 revision 26）；升级步骤与上述一致，无额外手工命令。

### Rollback

backup-required（revision 25 数据迁移与分组策略单键化不可逆；revision 26 加法列本身可忽略，但同一窗口已包含 revision 25）。

### Pre-1.0 behavior notes

- 媒体任务 retention 的 expired 行保留一个完整 TTL（按 `updated_at` 删除），期间客户端可观察 `expired` 与 410，请求快照/上游 job id 不再在终态化同轮被删除。
- 视频任务面边界收敛：创建响应补 `created_at`；终态 DELETE 幂等 204；路径族内未定义方法/尾斜杠/多余段与跨任务族访问均为本地 404；本地 `expired` 任务 content 返回 410；轮询 `url` 采用反代 `X-Forwarded-Proto`/`X-Forwarded-Host`。
- 视频请求 `duration` 与 `seconds` 同义：任一生效词均可作为无上游回报时的秒计量基源（此前仅识别 `seconds`）。
- 同步图片响应 `data: []` 按 0 张计量并标记 `source=upstream`，不再回落到请求 `n` 造成超收。
- Gemini 原生 `generateContent` 图像响应按 `inlineData` 图片 part 数计量（`source=upstream`）；流式请求不对分片重复计数，按请求 `candidateCount`（缺省 1）兜底并标记 `source=request`。完整非流式响应存在 `candidates` 但无图片 part 时显式按 0 张计量（`source=upstream`），不回落请求 `n`；OpenAI usage 中的显式 0 张同样保留。
- 视频创建计费快照按 adapter 实际出站值冻结：GLM `duration` 量化为 5s/10s、xAI 秒数四舍五入取整、Veo 3.0/3.1 固定 8s，xAI/Veo/Volcengine 按各自出站分辨率档（Volcengine 封顶 1080p）；被 adapter 忽略的请求 `seconds` 不再进入快照。终态上游回报的 `resolution_tier` 优先于冻结档；`unit=call` 与音频任务始终不带档位。
- 运行期 `media_pricing_json` 增加 mode/kind 校验：仅当目录 mode 可映射到媒体类型（video/image/audio）时，结构合法但单位不匹配（如 video 行配 `unit=image`）按无价处理，gateway 运行期目录扫描、管理目录、账户绑定、用量计价等五个读面同判据（0 成本 + `usage_missing`）。`chat`/`responses`/`embedding`/空 mode 不参与 kind 推断（Gemini `generate_content` 图像行由 endpoint modes 投影为 `chat`，无 endpoint modes 的迁移行投影为空），结构合法的模板保持有效，由结算按请求类型校验。新建路径仍严格拒绝非媒体 mode 新配置 `mediaPricing`；`PATCH` 已存在合法媒体模板的行允许继续编辑价格/日期，内置完整性校验不再把这类行当文本模型索要上下文容量；管理面分类在 mode 投影为文本时按 `mediaPricing.unit`/`outputModalities` 还原媒体分类。
- 视频首次终态轮询在同一响应内回填 `completed_at`/`format`/`duration`/`url` 等字段（不再滞后一轮）；并发轮询的失败方会重新加载已落库终态行再回显。冻结模板结算按任务 kind 固定媒体类型，目录 mode 后续变更不影响在途任务。
- 火山 `seed-tts-2.0` 流式面的 `metadata` 现只新增到 `req_params`（此前被静默丢弃）；规范路径冲突仍 400。
- 运行期非法 `media_pricing_json` 行按无价处理：绑定/计价目录拒绝，已发生请求显式 `cost_usd=0` + `usage_missing` 落库，不回落 legacy、不产生计费行项；统一媒体行计量缺失同样显式 0 + `usage_missing`（legacy 无价行保持 NULL）。**修复（2026-11-20，BUG-0369）**：`media_pricing_json` 为 NULL/空串的文本模型不再被当成非法模板——SQL NULL 在目录扫描中按缺失处理，`ErrEmpty` 只表示「无模板」，token 计价恢复正常；结构非法（如 `{}`）仍按无价拒绝。本批目录缓存键已升至 `v3:`，旧 `v2`/无前缀条目不会被命中，无需手工清理 Redis。
- 语音转写 multipart 请求的 `model` 表单字段现由 body 中间件提取（此前只提取图片 multipart，`/v1/audio/transcriptions`、`/v1/audio/translations` 会因缺少 model 在入口 404）；字段重复、截断、空值或含控制字符仍按缺模型处理，不放宽请求体上限（BUG-0368）。
- 火山 openspeech 同步 STT 实装（2026-11-20）：`POST /v1/audio/transcriptions` 的 multipart `file` 经 `recognize/flash` 以 `audio.data` base64 直传，响应归一为 `json`/`verbose_json`/`text`/`srt`/`vtt`；单文件上限 25MB，超限本地 400 并提示改用 `/v1/audio/jobs`；`/v1/audio/translations` 无上游对应能力，火山账户本地 400；成本按上游 `audio_info.duration` 毫秒折算秒数落库。此前该账户虽声明 `audio_transcription_json`，实际请求被转发到不存在的 `openspeech.bytedance.com/audio/transcriptions`（404 TLB）。
- 百炼长转写修复（2026-11-20）：创建请求补 `X-DashScope-Async: enable`；缺该头时上游 paraformer-v2 返回 403 `current user api does not support synchronous calls`。目录校准同批完成：恢复 `paraformer-v2` 行（0.00008 元/秒 ÷7.0 = $1.142857e-05/秒，异步文件转写真实可用，qwen 16→17 行），`qwen3-asr-flash` 保留价格行但 `catalogVisible=false`（该接口不接受 `input.file_urls`，真实上游 400 `url error`），避免账户绑定后请求必败。
- 管理面模型目录的 priced 过滤与内置模板校验改用 `pricing.HasCatalogDirectPrice`：仅 service tier 价（无基础直价或有效统一模板）的行不再显示为有价，也不能作为自定义模型模板；与账户绑定/计价目录判据一致（DR-P7）。
- 管理面自定义模型保存响应与列表同口径暴露 `mediaPricingInvalid`：列存在但解析或 mode/kind 校验失败时不再静默当作无价省略，便于诊断历史/手工写入的非法行（写入路径本身仍拒绝非法模板）。
- 前端自定义视频模型可选择 `unit=call`（按次，适用于 3D 等无时长产物）。
