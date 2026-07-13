# AI 问答上下文管理设计

> 本文定义站内 AI 问答的渲染历史、模型上下文、附件资产、主动压缩和请求体预算。本文是 [AI 问答设计](AI问答设计.md) 的第二阶段专题设计；网关级 Responses compact 继续以 [Responses 上下文压缩落地方案](Responses上下文压缩落地方案.md) 为准。
>
> 当前状态：multipart 图片资产、服务端缩放、隐藏图片说明、checkpoint + recent suffix、结构化主动压缩、真实流式 usage、本地 tokenizer 兜底、请求体字节预检和只读圆环已经在隔离分支实现，并通过 Mock、真实模型、真实 PostgreSQL/Redis 和桌面/移动端浏览器验收。实施追踪见 [PLAN-0095](../plans/计划-0095-AI问答上下文与多模态降负.md)。

## 1. 目标与边界

目标不是让模型拥有真正无限的上下文，而是让会话轮数可以持续增长，同时保证每次模型请求都受 token 和 HTTP 请求体双重预算约束，并尽量保留用户目标、关键事实、偏好、决定、任务状态、重要工具结果和图片语义。

必须明确以下事实：

- 页面历史可以继续完整展示，但模型不能每轮重放全部页面历史。
- 压缩是有损的，只能保证关键信息受控保留，不能承诺逐字无损的无限记忆。
- token 超限和请求体字节超限是两类问题；HTTP gzip 不能降低模型 token。
- 图片二进制、模型可见的图片 token 和图片语义记忆必须分开管理。
- 本设计只服务站内 AI 问答，不改变普通客户端的网关上下文语义。

## 2. 已修复的旧实现问题

本轮改造前存在四个确定问题，现均由 PLAN-0095 主链路替换：

1. `chat_messages.content_text` 同时承担页面展示和模型历史，渲染历史与模型上下文没有隔离。
2. 上下文只读取最近 64 个完整轮次，历史再被硬限制为 64K token；模型即使支持 272K 或 1M，也无法使用真实窗口。
3. token 预算使用 `UTF-8 bytes / 3` 和固定图片/工具预留，没有使用上游真实 usage、Responses input token 计数或模型图像策略。
4. 图片原文件直接转 Data URL。4 MiB 图片转 base64 后约为 5.33 MiB；聊天 System API 当前允许 24 MiB，但内部 `/v1/responses` 仍进入默认 16 MiB 的网关文本 lane，三张接近上限的图片叠加 JSON 后就可能在内部网关失败，四张还会逼近或超过 System API / 反向代理限制。提高某一层 body limit 不能解决多层最小值和长期上下文负担。

## 3. 成熟实现复用决策

本项目不从零发明压缩算法，按许可证允许的范围复用成熟实现，并保留来源说明。

| 来源 | 许可证 | 直接借鉴 | 不复制 |
| --- | --- | --- | --- |
| OpenAI Codex | Apache-2.0 | 渲染历史与模型 transcript 分离、`replacement_history` checkpoint、checkpoint + suffix 恢复、压缩成功后原子替换、真实 input usage 优先、图片 patch/尺寸预算 | Codex rollout、app-server 和完整 Rust session 运行时 |
| Hermes Agent | MIT | 图片 JPEG 质量 85 与渐进缩放、base64 4/3 膨胀预检、确认 413 后单次恢复、工具结果去重/摘要、失败回滚、最近原文尾部 | Python session 分叉、provider 插件和整套 agent runtime |
| OpenClaw | MIT | token-share 分块、多阶段摘要、tool call/result 不拆分、请求体与单图/总图双预算、图片 description 注入、压缩前 memory flush | 插件 host、自动回复系统和通用 worker 框架 |
| Gemini CLI | Apache-2.0 | 结构化 state snapshot、摘要后二次校验、压缩后反而变大则拒绝安装、工具上下文分级 | Gemini 专用 SDK 与临时文件交互体系 |

不采用旧版 OpenCode 的“累计 usage 达 95% 才压缩”方案。累计计费用量不等于当前请求上下文，95% 触发也没有为输出、工具和请求体留下可靠余量。

默认只借鉴算法思想并按本项目语言和边界重新实现。确需复制源码时，实施前必须锁定来源仓库 commit/tag、逐文件确认许可证和版权头，并在第三方清单中记录来源与修改；MIT 保留许可文本，Apache-2.0 保留版权、许可证和修改声明。不得仅凭仓库级许可证推断任意文件都可直接复制，也不得整包引入上述 Agent 运行时。

## 4. 三层数据模型

### 4.1 渲染历史 `render_history`

`chat_messages` 继续作为页面事实源：

- 保存用户和助手最终可见正文、工具投影、时间、状态和轮次关系。
- 页面分页、复制、重新编辑和会话详情只读取该层。
- 上下文压缩不能覆盖、改写或删除仍在 7 天保留期内的页面消息。
- reasoning、工具过程和隐藏图片说明不混入用户复制的最终回答。

### 4.2 模型上下文 `model_context`

模型上下文是可替换的派生视图，不再直接把最近 64 轮 `chat_messages` 拼成请求。

每次请求固定按以下顺序组装：

```text
canonical system instructions
+ latest successful context checkpoint
+ checkpoint 后的 recent verbatim tail
+ 当前用户的有序内容块
+ 当前模型工具定义
```

模型上下文至少包含：

- 结构化长期记忆：用户偏好、稳定事实、关键实体和约束。
- 当前任务状态：目标、已完成事项、未完成事项、下一步和阻塞。
- 关键决定与不可逆操作记录。
- 重要工具结果摘要；不保留旧 reasoning 和无意义过程日志。
- 图片隐藏说明与附件引用。
- 最近完整成功轮次的原文尾部，避免纯摘要造成指令漂移。

### 4.3 附件资产 `attachment_store`

图片必须先成为独立资产：

- 原图和模型输入图不写入 `chat_messages` JSON，也不通过聊天 JSON 反复发送 base64。
- 数据库存元数据、归属、hash、尺寸、MIME、状态、到期时间和有界文件引用；二进制落独立聊天资产目录或对象存储。
- UI 消息保存 `assetId`、缩略图和顺序信息。
- 模型首轮按需加载压缩图；后续默认只携带隐藏图片说明。
- 用户明确要求重新查看、对比或读取细节时，才按 `assetId` 再加载模型输入图。

PLAN-0095 只实现本机有界文件存储；对象存储仅作为未来扩展点，不引入新的分布式依赖。

## 5. 图片处理链路

### 5.1 上传协议

新增 `POST /__aisys__/api/my-chat/conversations/:conversationId/assets`：

- 使用 multipart 流式上传，不接受聊天 JSON 中的 Data URL。
- 上传接口先验证登录用户、会话归属、MIME、单图/总图预算和解码后的真实格式。
- 图像处理在有并发上限的 worker thread 或专用执行池完成；上传接口等待处理结果后返回 `assetId`。
- 聊天发送接口只传有序 `contentBlocks`，其中图片块为 `{ type: "image", assetId }`。

### 5.2 缩放与压缩

默认视觉模式采用成熟实现的保守交集：

- 自动旋转 EXIF 方向。
- 默认最长边不超过 2048 px，同时不超过 2500 个 32×32 patch。
- JPEG 初始质量 85；需要继续降负时按质量和尺寸阶梯收敛，而不是无限循环。
- PNG 仅在透明度确有价值时保留；普通截图或照片允许转 WebP/JPEG。
- 默认显式发送 `detail: high`；缩略理解可用 `low`。不能省略 `detail`，因为 GPT-5.5/5.6 的 `auto` 可能采用原始尺寸策略。
- 同一原图按 hash + 处理策略缓存模型输入图，避免重复编码。

服务端同时限制：原始上传字节、解码像素、压缩后单图字节、单轮图片总字节、单轮图片数量和最终模型请求体字节。只提高 Express、网关或 Nginx 的 body limit 不是修复方案。

### 5.3 图片语义卸载

图片与文字必须保持原始顺序，例如：

```text
文字 A -> 图片 1 -> 文字 B -> 图片 2
```

首轮模型看到压缩图片。成功完成后，后台产生用户不可见的结构化 `image_observation`：

```json
{
  "assetId": "asset_xxx",
  "summary": "图片整体说明",
  "ocr": ["关键文字"],
  "objects": ["关键对象"],
  "questionRelevantFacts": ["与本轮问题有关的事实"],
  "uncertainties": ["无法确认的细节"]
}
```

后续上下文用该说明替换图片二进制，但仍放在原来的内容块位置。提取失败不影响首轮可见回答；后台按暂时错误重试，确定性错误记录状态并停止重试。

图片说明使用独立后台模型调用，而不是在可见回答里混入隐藏分隔符，原因是通用 Chat / Responses 不能可靠保证“正文 Markdown + 隐藏结构化字段”同时返回且绝不泄漏。代价是增加一次受计费、受审计的图像调用。

如果用户在说明生成完成前立即发送下一轮：

- 先等待同一已有任务最多 1 秒，不新建重复任务。
- 仍未完成时返回可重试的中文状态，不把历史图片重新编码成 Data URL，也不使用空占位假装模型仍记得图片。
- 未完成隐藏说明的图片轮次禁止进入 checkpoint；说明完成后再允许压缩，避免图片二进制被移出上下文后永久丢失语义。
- 说明生成后，后续轮次只携带说明；用户明确要求精确重看时再重新加载压缩图。

## 6. Token 与请求体双预算

### 6.1 Token 事实来源

优先级固定为：

1. 上一轮上游返回的真实 `usage.input_tokens`，作为已发送上下文基线。
2. 与模型目录绑定的本地 tokenizer / 图像 token policy，计算本轮增量。
3. Responses `/v1/responses/input_tokens` 的精确请求计数，用于阈值附近、压缩验收和估算偏差校准。
4. 仅在 tokenizer 或上游能力不可用时使用本地保守估算，并明确标记为 estimated。

工具定义按最终序列化内容计数；图片按压缩后的尺寸、`detail` 和模型图像策略计数。累计计费用量与当前 active context 必须分开存储。

`/responses/input_tokens` 能力按 provider profile + model 缓存探测结果，不能在每次圆环刷新时调用上游；圆环读取已保存基线与本地增量估算。

当前网关尚未实现 `/responses/input_tokens` 的完整入口。PLAN-0095 必须先补齐 endpoint family 识别、候选账户能力筛选、请求/响应契约、限流、使用记录和审计，再允许 AI 问答调用；Chat-only 或能力未知账户直接使用本地 tokenizer + 上游实际 usage 校准，不能把精确计数端点当成天然可达。

### 6.2 有效窗口

前端不再让用户选择上下文大小。服务端先使用模型目录明确给出的最大输入；厂商只给总窗口和最大输出时，再派生保守输入上限：

```text
effectiveInputLimit = modelMaxInputTokens
  ?? (modelContextWindow - maxOutputTokens)

historyBudget = effectiveInputLimit
  - protocolAndToolReserve
  - emergencyReserve
```

未知模型不能静默按 16K 长期运行；应优先刷新模型目录或使用协议档案的保守窗口，并在 UI 标注“估算”。

切换模型时必须重新计算有效窗口、tokenizer、system instructions 和工具 schema：

- 新模型窗口更小时，在发送前完成一次有界重压缩。
- 结构化 checkpoint 可以按新模型重新计数并继续使用。
- 官方 opaque compact 只有 provider、protocol profile、endpoint family、模型兼容组和 compact capability 全部匹配时才能继续使用。
- 不兼容的 opaque compact 不得发给新上游；应从仍在保留期内的 render history 重建结构化 checkpoint。无法重建时明确提示该会话不能安全切换模型。

### 6.3 请求体字节预算

在真正调用网关前，对最终序列化 JSON 实测字节数，并取以下限制的最小值：

- System API 聊天入口限制。
- 主进程代理限制。
- 网关目标 lane 限制。
- 反向代理限制。
- 目标上游已知限制。

图片资产引用在组装模型请求前才解析为压缩 Data URL 或 file reference，且只存在于当次内部请求。若仍超限，先降低图片尺寸/质量；确认是图片大小错误时最多恢复重试一次，不能重复计费或无限压缩重试。

## 7. 主动压缩机制

### 7.1 水位

初始建议值：

| 水位 | 默认值 | 行为 |
| --- | --- | --- |
| 软水位 | 70% | 后台幂等创建压缩任务，不阻塞当前已成功轮次 |
| 压缩目标 | 45% | 摘要 + 结构化记忆 + 最近原文尾部收敛到该范围 |
| 硬水位 | 85% | 下一轮发送前等待同一会话已有任务，或执行一次有界紧急压缩 |
| 绝对上限 | 100% 有效输入预算 | 当前输入本身仍超限时返回 422，不静默丢历史 |

阈值最终应按模型、工具数量和真实运行数据调整，不写死在前端。

### 7.2 分阶段降负顺序

1. 图片二进制替换为已有 `image_observation`。
2. 删除旧 reasoning、重复搜索过程和无长期价值的工具日志。
3. 将大型工具输出替换为结构化结果摘要和可追溯引用。
4. 按 token share 分块压缩中段历史，不能拆开 tool call/result。
5. 合并长期记忆、任务状态、旧摘要和新的部分摘要。
6. 保留最近完整成功轮次原文尾部。
7. 对新 checkpoint 重新计数并执行质量校验。

站内 AI 问答默认使用可跨同一 API Key 候选账户安全重建的本站结构化摘要 checkpoint。只有路由能够保证后续请求继续命中兼容的 provider / protocol profile / endpoint family / 模型兼容组，并且 capability probe 已验证时，才允许使用官方 `context_management` 或 `/responses/compact`；opaque compaction item 必须原样保存，不能编辑、跨供应商或交给 Chat bridge。

### 7.3 质量门禁

本站结构化摘要必须通过以下检查后才能安装：

- 包含当前目标、关键约束、未完成任务和最近用户意图。
- 未破坏 tool call/result 邻接和未闭合工具状态。
- checkpoint token/字节数确实下降；如果反而膨胀则拒绝安装。
- source revision 与创建任务时一致，防止旧任务覆盖新会话状态。
- 结构化字段通过 schema 校验；必要时做一次摘要 audit/repair。

官方 opaque compact 使用另一套门禁，不能检查其不可读语义内容：

- 收到 completed 且恰好一个合法 compaction output。
- 来源 provider、profile、endpoint family、模型兼容组和 capability 与后续路由亲和一致。
- usage、体积和 token 结果满足预算，且通过一次可续接协议验证。
- 任一条件不满足时不安装，回退到本站结构化 checkpoint。

压缩在临时上下文上完成，只有成功后才原子推进 `context_revision`。失败继续使用旧 checkpoint，不能删除页面消息或静默截断最老轮次。

已落地的恢复门禁还包括：

- 每个已接受轮次都推进 `context_revision`；usage 写回和 checkpoint 安装都按期望 revision 做 CAS，旧任务不能覆盖新状态。
- 第二次及后续压缩必须完整重建旧 checkpoint 的 `durable_memory`、`task_state`、`tool_result` 和 `image_observation`，不能只继承任务状态。
- 本地装载达到 512 条消息或 16 MiB 上限时，先执行一次紧急压缩再重新装载，不能在压缩触发前把会话卡死。
- 失败或取消的助手轮次不参与模型上下文，但后续成功轮次仍按 `turnId` 正确配对，不能因数组错位被连带丢弃。
- 最终请求体超过 15 MiB 时先压缩历史并重建一次请求；仍超限才返回明确错误。
- 图片说明调用使用 90 秒超时，结构化压缩调用使用 120 秒超时；超时保留旧上下文并进入可重试状态。
- 维护任务会恢复 stale `compacting` claim，再清理过期 checkpoint，避免长期占用批次导致清理饥饿。

## 8. 存储设计

建议新增以下当前结构，不保留旧结构兼容分支：

### 8.1 `chat_context_checkpoints`

- `id`、`conversation_id`、`version`、`source_from_sequence`、`source_through_sequence`
- `recent_tail_from_sequence`、`entry_from_sequence`、`entry_through_sequence`、`payload_digest`
- `estimated_input_tokens`、`upstream_input_tokens`、`request_body_bytes`
- `model_id`、`provider_code`、`provider_profile_id`、`endpoint_family`、`compact_compatibility_hash`
- `prompt_version`、`status`、`quality_status`
- `created_at`、`expires_at`

### 8.2 `chat_context_entries`

- `conversation_id`、`checkpoint_id`、`source_message_id`
- `kind`：`verbatim`、`durable_memory`、`task_state`、`tool_result`、`image_observation`、`provider_compaction`
- `content_json`、`provenance`、`trust_level`、`token_count`、`sequence`、`created_at`、`expires_at`

`chat_context_entries` 是模型上下文内容的唯一事实源；checkpoint 只保存版本、来源范围、亲和信息、统计和 entry 范围，不重复保存摘要正文。官方 opaque item 作为 `provider_compaction` entry 保存。

用户文本、图片 OCR、图片说明和工具输出都属于不可信内容，必须保留 provenance，并以 user/tool 语义注入；不能因为进入 durable memory 就提升为 canonical system instructions，避免图片或工具内容中的提示词注入获得更高优先级。

### 8.3 `chat_assets`

- `id`、`system_account_id`、`conversation_id`、`sha256`、`mime_type`
- `original_width`、`original_height`、`model_width`、`model_height`
- `original_bytes`、`model_bytes`、`storage_key`、`thumbnail_storage_key`
- `processing_status`、`observation_status`、`observation_json`
- `created_at`、`expires_at`

### 8.4 `chat_conversations` 增量字段

- `context_revision`
- `active_checkpoint_id`
- `context_state`：`ready`、`compact_pending`、`compacting`、`compact_failed`
- `active_context_tokens`、`effective_context_limit_tokens`
- `compacted_through_sequence`

所有 checkpoint、entry 和资产跟随会话 7 天保留策略清理。二进制文件删除必须先按索引有界定位，不扫描全目录。

## 9. 后台任务与并发

新增两个可观测任务：

- `chat-context-compact`：唯一键为 `conversationId + sourceRevision`，单会话锁，成功后原子安装 checkpoint。
- `chat-image-memory-extract`：唯一键为 `assetId + turnId + promptVersion`，首轮回答完成后生成隐藏说明。

图片缩放是发送前依赖，不能等普通定时 worker；它在上传请求关联的受限执行池完成。压缩软水位走后台，硬水位只能等待同一已有任务或执行一次紧急压缩，不能并发创建多个相同压缩任务。

需要调用模型的压缩和图片说明任务仍然属于该用户的 AI 问答请求：

- 使用会话绑定的真实 API Key，重新进入现有网关，不建立内部免鉴权通道。
- 继续遵守当前路由策略、分组、账户代理、额度、并发、计费、使用记录和原始审计。
- 不跨 API Key、分组或供应商寻找所谓便宜摘要模型。
- 写入 `purpose = chat_context_compaction` 或 `chat_image_memory` 等内部用途事实，并禁止任务递归触发新的压缩任务。
- API Key 失效、额度不足或无可用账户时保留旧 checkpoint，记录可重试状态，不阻断页面读取和已有消息展示。

监控至少包含：队列长度、最老等待时间、成功/失败/重试数、单次耗时、压缩前后 token、压缩收益率、图片原始/处理后字节、413 次数、checkpoint 安装冲突和 worker 内存。

## 10. 前端状态

- 删除上下文下拉和 `contextWindowTokens` 请求字段。
- 输入框底部只显示圆形上下文进度图标，不让用户选择窗口。
- Tooltip 展示“已用 / 可用”、状态和是否为估算值；不要展示累计计费用量。
- 圆环分子是 active checkpoint + recent tail + 固定提示/工具的当前输入 token，分母是有效输入窗口。
- 压缩进行中只显示低噪音状态，不阻塞用户阅读；达到硬水位且发送必须等待时才显示明确提示。
- 图片上传、处理中、失败和重试状态在原文字顺序位置展示，不能集中到独立附件栏破坏图文关联。

## 11. API 增量

- `POST /my-chat/conversations/:id/assets`：流式上传并返回处理后的资产元数据。
- `GET /my-chat/conversations/:id/assets/:assetId/content`：按会话归属读取私有处理图。
- `GET /my-chat/conversations/:id/context-status`：返回圆环所需的 token、窗口、状态和估算标记。
- `POST /my-chat/conversations/:id/stream`：删除 `contextWindowTokens`，图片块只接收 `assetId`。

服务端不信任客户端提供的 token、尺寸、MIME、asset owner 或上下文窗口。

## 12. 实施顺序

1. 修复 usage 采集和模型目录上下文元数据，删除 64K/64 轮硬限制和前端窗口选择。
2. 落地资产表、multipart 流式上传、图像缩放压缩和 asset reference 请求。
3. 落地 render history 与 model context checkpoint 分离。
4. 先实现确定性降负、结构化摘要 fallback 和原子 checkpoint。
5. 接入官方 Responses input token count 与 compact 能力探测。
6. 实现图片隐藏说明、后台任务、硬/软水位和圆形进度。
7. 完成 Mock、真实模型、多轮长上下文、图片、413、压缩失败和 UI 验收。

## 13. 验收标准

- 页面仍能查看 7 天内完整消息，压缩不改变可见正文。
- 模型请求只使用 checkpoint + suffix，不再固定读取 64 轮。
- 图片不再随聊天 JSON 上传 base64，三到四张大图不会再进入当前 24 MiB System API / 16 MiB 网关文本 lane 的多层冲突链路。
- 图片说明在后续轮次保持原有图文顺序，复制助手回答不包含隐藏说明、reasoning 或工具过程。
- 上下文窗口始终使用服务端模型目录最大值，前端无窗口下拉。
- 圆环与实际请求 usage 在容许误差内一致；上游不支持精确计数时明确显示估算。
- 压缩失败、摘要膨胀、worker 重启和版本冲突均不会覆盖原消息或安装坏 checkpoint。
- 当前输入本身过大时给出明确错误，不通过静默删除历史伪造成功。
- 压缩和图片说明的额外模型调用按普通请求计入用户 API Key 用量并可在使用记录中区分用途。

## 14. 参考实现与官方资料

- OpenAI Compaction guide: <https://developers.openai.com/api/docs/guides/compaction>
- OpenAI Images and Vision guide: <https://developers.openai.com/api/docs/guides/images-vision>
- OpenAI API `/v1/responses/input_tokens` 与 `/v1/responses/compact` OpenAPI 规范。
- `F:/temp-project/agent/openai-codex-main/codex-rs/core/src/context_manager/history.rs`
- `F:/temp-project/agent/openai-codex-main/codex-rs/core/src/compact.rs`
- `F:/temp-project/agent/hermes-agent/agent/context_compressor.py`
- `F:/temp-project/agent/hermes-agent/agent/conversation_compression.py`
- `F:/temp-project/agent/openclaw/src/agents/compaction-planning.ts`
- `F:/temp-project/agent/openclaw/src/agents/compaction.ts`
- `F:/temp-project/agent/gemini-cli/packages/core/src/context/chatCompressionService.ts`
