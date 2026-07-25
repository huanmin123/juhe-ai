# AI 问答上下文管理设计

> 本文定义站内 AI 问答的渲染历史、模型上下文、附件资产、主动压缩和请求体预算。本文是 [AI 问答设计](AI问答设计.md) 的第二阶段专题设计；网关级 Responses compact 继续以 [Responses 上下文压缩落地方案](Responses上下文压缩落地方案.md) 为准。
>
> 当前状态：multipart 图片资产、服务端缩放、隐藏图片说明、checkpoint + recent suffix、结构化主动压缩、真实流式 usage、本地 tokenizer 兜底、请求体字节预检、只读圆环和上游 Prompt Cache 稳定前缀均已在隔离分支实现，并通过 Mock、真实模型、真实 PostgreSQL/Redis 和浏览器验收。真实连续轮次第二轮缓存读取占输入 token 约 `90.0%`；见 [BUG-0111](../bug/问题-0111-AI问答破坏上游前缀缓存.md)。实施追踪见 [PLAN-20260713T150309000Z](../plans/计划-20260713T150309000Z-AI问答上下文与多模态降负.md)。

## 1. 目标与边界

目标不是让模型拥有真正无限的上下文，而是让会话轮数可以持续增长，同时保证每次模型请求都受 token 和 HTTP 请求体双重预算约束，并尽量保留用户目标、关键事实、偏好、决定、任务状态、重要工具结果和图片语义。

必须明确以下事实：

- 页面历史可以继续完整展示，但模型不能每轮重放全部页面历史。
- 压缩是有损的，只能保证关键信息受控保留，不能承诺逐字无损的无限记忆。
- token 超限和请求体字节超限是两类问题；HTTP gzip 不能降低模型 token。
- 图片二进制、模型可见的图片 token 和图片语义记忆必须分开管理。
- 上游 Prompt Cache / KV Cache 只复用完全一致的前缀；上下文没有超限不代表缓存友好，cache key 相同也不代表前缀必然命中。
- 本设计只服务站内 AI 问答，不改变普通客户端的网关上下文语义。

## 2. 已修复的旧实现问题

本轮改造前存在四个确定问题，现均由 PLAN-0104 主链路替换：

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
- 上下文压缩不能覆盖、改写或删除仍在当前配置保留期内的页面消息。
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
- 活动资产按用户维护 `chat_user_asset_usage` 预聚合字节和数量；上传创建与过期/主动删除在同一配额锁和事务中增减，API 路径不得扫描资产表求和。当前每用户最多 2 GiB、1024 个未过期图片资产，未提交草稿同样占用配额。

PLAN-0104 只实现本机有界文件存储；对象存储仅作为未来扩展点，不引入新的分布式依赖。

## 5. 图片处理链路

### 5.1 上传协议

新增 `POST /__aisys__/api/my-chat/conversations/:conversationId/assets`：

- 使用 multipart 流式上传，不接受聊天 JSON 中的 Data URL。
- 请求流与 Busboy 通过 pipeline 连接；客户端中断必须终止文件写入、拒绝请求并在 `finally` 清理临时目录，不能等待永远不会到达的 multipart `finish`。
- 上传接口先验证登录用户、会话归属、MIME、单图/总图预算和解码后的真实格式。
- 图像处理在有并发上限的 worker thread 或专用执行池完成；上传接口等待处理结果后返回 `assetId`。
- 聊天发送接口只传有序 `contentBlocks`，其中图片块为 `{ type: "input_image", assetId }`。

### 5.2 缩放与压缩

默认视觉模式采用成熟实现的保守交集：

- 自动旋转 EXIF 方向。
- 应用 EXIF 方向后，最长边不超过 1024 px，同时不超过 2500 个 32×32 patch；小图不放大。
- 所有 JPEG、PNG、WebP、GIF 输入统一输出 JPEG，透明区域先铺白色背景，固定质量 85。
- 不再以 WebP 保留透明度，也不再按 75/68/60/55 逐级降质；固定质量 85 后仍超过 1 MiB 时前端不插入、不上传，后端明确拒绝并提示裁剪。
- 单条消息最多 5 张图片；前端选择/连续粘贴/编辑重发与后端 HTTP、解析、绑定、草稿资产创建均独立限流，第 6 张不得落库或占用资产配额。
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

用户问题和可见回答只作为判断相关性的不可执行参考上下文，不能改变观察器的提取规则。即使用户当轮要求“只回答其他内容”“不要复述图片文字”，隐藏说明仍必须客观保存所有清晰可辨 OCR；否则下一轮会在可见正文没有该信息时永久失忆。

图片说明带独立 `observation_revision`、claim ID 和租约时间。说明完成/失败只能按 claim + revision CAS 写回；最近多模态轮次重新生成时，替换事务先失效旧说明并推进 revision，旧问题/旧回答启动的迟到任务不能覆盖新状态，新回答成功后重新生成与新问答关联的说明。

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

当前网关尚未实现 `/responses/input_tokens` 的完整入口。PLAN-0104 必须先补齐 endpoint family 识别、候选账户能力筛选、请求/响应契约、限流、使用记录和审计，再允许 AI 问答调用；Chat-only 或能力未知账户直接使用本地 tokenizer + 上游实际 usage 校准，不能把精确计数端点当成天然可达。

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

图片资产引用在组装模型请求前才解析为压缩 Data URL 或 file reference，且只存在于当次内部请求。若仍超限，不得绕过固定 JPEG 85 与 1024 最长边策略继续降质；确认是图片大小错误时明确拒绝，不能重复计费或无限压缩重试。

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
- 只读装载发现活动 checkpoint 已过期或失效时，只返回有效起点 `0`，不在读取路径改库；下一次压缩 claim 必须在同一锁和事务中解绑旧 checkpoint、把持久压缩游标与进度归零、推进 `context_revision` 后再认领，保证仍在当前配置保留期内的原消息从头参与重建。
- 软水位压缩不得阻塞下一轮发送：接受新轮次会在同一事务中把 `compacting` 原子退回 `compact_pending` 并清除旧 claim；后台任务随后通过 CAS 发现失效并停止安装。
- 压缩 claim 是可续租租约，每页成功推进时刷新 claimed time；只有超过 stale 窗口且没有续租的任务才能被重新认领或由维护任务恢复。
- `compact_failed` 的 `retry_at` 在到期前同时约束 request 和 claim；连续失败计数只在失败 CAS 真正落库时增加，正常释放、被新消息打断或 claim 失效都不计失败，成功安装后归零。
- 第二次及后续压缩必须完整重建旧 checkpoint 的 `durable_memory`、`task_state`、`tool_result` 和 `image_observation`，不能只继承任务状态。
- 结构化摘要若合法 JSON 但模型把 `currentGoal` / `recentUserIntent` 留空，服务端优先使用本页最近用户原文，再回退上一 checkpoint；连续多页都缺字段时，每页都必须推进到该页最新用户意图，不能让第一页降级值永久遮蔽后续页。补齐后再执行完整性和缩小率检查，不为格式修复额外重复调用模型，也不安装缺少意图锚点的 checkpoint。
- 本地装载达到 512 条消息或 16 MiB 上限时，先执行一次紧急压缩再重新装载，不能在压缩触发前把会话卡死。
- 失败或取消轮次的用户消息和助手消息都不参与模型上下文或压缩来源，SQL 装载和行/字节预算都以完整 user+assistant 轮次为最小单位；单轮超过 512 KiB 页预算时在 16 MiB 绝对上限内整轮受控放宽，不能拆成两个摘要页。后续成功轮次仍按 `turnId` 正确配对，不能因数组错位被连带丢弃。
- 最终请求体超过 15 MiB 时先压缩历史并重建一次请求；仍超限才返回明确错误。
- 图片说明调用使用 90 秒超时，结构化压缩调用使用 120 秒超时；超时保留旧上下文并进入可重试状态。
- 维护任务会恢复 stale `compacting` claim，再清理过期 checkpoint，避免长期占用批次导致清理饥饿。

### 7.4 用户手动压缩

用户可以通过 AI 问答 `/compact` 命令请求压缩当前对话，但这不改变只读上下文圆环、模型窗口或压缩质量门禁：

- `POST /my-chat/conversations/:id/context/compactions` 只接受当前选中模型；API Key、协议、网关地址和有效输入窗口由服务端重新解析。
- 正在准备、生成或清空的对话返回冲突；正在压缩时返回幂等接受状态，没有可压缩完整轮次时返回 `no_compactable_turn`。
- 服务端必须先持久化 `compact_pending` 再返回 `202`，随后复用同一分页读取、claim、CAS、checkpoint 安装和失败恢复链路，不能另建手动摘要表或把完整历史载入内存。
- 前端收到服务端接受结果后才显示压缩状态，并轮询 `context-status` 到 `ready` 或 `compact_failed`；选择命令本身不能乐观宣称压缩完成。
- 手动压缩会使用会话绑定的真实 API Key 调用模型，继续产生正常额度、用量和审计事实，确认界面必须明确这一点。
- 手动压缩失败继续使用旧 checkpoint 和历史，不删除页面消息，也不改变模型窗口。

## 8. 上游前缀 / KV 缓存

上下文预算解决“单次请求能否放入模型窗口”，上游 Prompt Cache 解决“相同前缀是否需要重复计算和按普通输入价格计费”，两者必须分别设计。供应商通常按 token/item 前缀匹配，不会因为语义近似、会话 ID 相同或本地 checkpoint 相同就自动命中。

### 8.1 稳定前缀与 canonicalization

每轮模型输入按以下稳定顺序组装：

```text
canonical instructions
+ stable tool schemas
+ active checkpoint / completed history
+ newly appended completed turns
+ current user content
```

- 静态内容始终在前，动态内容始终追加在后；不得把时间戳、trace、请求 ID、usage 或每轮变化的诊断 metadata 注入模型前缀。
- system instructions、工具列表、工具对象字段顺序、空字段处理和消息内容表示必须确定化；同一组工具不能因来源集合遍历顺序而重排。
- Responses 的纯文本用户消息始终使用同一种 `input_text` block 数组表示。当前轮不能用 block，进入历史后又折叠成字符串；即使文字相同，这种结构漂移也可能破坏精确前缀。
- 新轮次只在稳定历史尾部追加。重新编辑最近轮次、checkpoint 安装、图片二进制替换为隐藏说明、system prompt / 工具 schema 升级和不兼容模型切换都会形成新的 canonical prefix；这类边界允许一次冷缓存，随后必须重新稳定。
- 图片说明和结构化压缩是独立、低频且输入持续变化的模型调用。它们预期至少有一次冷缓存，不与主会话复用 key，也不能为了命中而携带无关历史；重点是避免污染主会话缓存和重复执行同一个幂等任务。

### 8.2 Capability-aware opaque key

- cache key 由服务端对固定版本、本地系统账户、API Key 和会话 ID 做 SHA-256 base64url 摘要；不得暴露标题、用户正文、凭据或可读用户标识，也不得混入具体上游账户、账户类型或分组。
- 普通消息追加不改变 key；`turnId`、`traceId`、时间戳和每轮递增的 `context_revision` 不能进入 key。
- 只有全部候选目录能力都明确声明 `supportsPromptCaching=true` 时才发送 `prompt_cache_key`；能力未知或不一致时不猜测注入。
- Responses 显式映射到 Chat Completions 时，桥接层校验并原样保留非空字符串 `prompt_cache_key`；协议转换不能让账户亲和与上游缓存键出现语义分叉。
- 模型、system prompt、工具 schema、checkpoint 或图片 projection 改变时，精确请求前缀本身自然形成新的上游缓存边界；无需新增持久化 `cache_epoch`。新前缀允许一次冷启动，随后继续使用同一会话 key 并重新稳定。
- 显式 cache breakpoint 是独立模型能力。当前 OpenAI 首期不伪造 Anthropic `cache_control`；后续只有模型目录新增明确的 breakpoint 能力字段后，才由 Anthropic driver 在协议转换边界插入。Gemini explicit cached content 同理，不能映射成 OpenAI key。

### 8.3 账户亲和与失败回退

AI 问答不新建一套缓存路由。稳定 `prompt_cache_key` 进入现有网关 session affinity，沿用“同一合法候选层级内优先上次成功账户”的软亲和：

- 亲和只调整候选顺序，不跳过授权、分组、模型/协议能力、账户代理、状态、冷却、到期、额度、并发和错误策略。
- 亲和账户不可用、请求失败或短暂等待后仍没有容量时，立即按现有网关规则回退其他账户。可用性和正确性优先于缓存命中，禁止硬绑定账户或无限等待。
- 上游缓存可能按组织、项目、区域、账户或供应商节点隔离，本地只能通过稳定 key + 软亲和提高连续性，不能承诺切号后仍命中。
- OAuth adapter 对 key 的多租户隔离继续生效，且不能混入具体上游账号 ID；否则每次故障切号都会主动制造新 key。

### 8.4 Usage、成本与验收

- 缓存命中只以上游明确 usage 为事实：OpenAI 读取 `cached_tokens`，Anthropic / compatible 读取明确的 cache read / cache write 字段，Gemini 读取其官方 cached content usage。没有字段时标记未知，不按相同前缀自行推测。
- 使用记录需保留普通输入、缓存读取、缓存写入、模型、账户、purpose、延迟和成本，后台再按既有预聚合规范统计命中率；API 和页面不能扫描原始使用记录临时汇总。
- 自动化回归验证 key 稳定、canonical prefix、能力过滤和故障回退；这类回归本身不能写成“真实上游缓存已命中”。
- 真实应用链路已覆盖同一会话连续追加和账户亲和：第二轮输入 `10948` token，其中 `9856` token 为上游明确返回的缓存读取，命中率约 `90.0%`。
- 不硬编码“节省十倍”、统一最低 token、统一 TTL 或统一价格。各供应商、模型、服务等级和保留策略会变化，运行时只读取模型/供应商能力并以真实 usage 验收。

供应商边界以官方资料为准：

- [OpenAI Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)：重复前缀、`prompt_cache_key` 和 cached token 观测。
- [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)：`cache_control` breakpoint、缓存写入/读取和供应商保留策略。
- [Gemini Context caching](https://ai.google.dev/gemini-api/docs/caching)：implicit caching 与 explicit cached content 的独立语义。

缺陷、修复和真实命中证据见 [BUG-0111](../bug/问题-0111-AI问答破坏上游前缀缓存.md)。缓存收益仍以每次上游 usage 为事实，不能把本次命中率外推为固定比例。

## 9. 存储设计

建议新增以下当前结构，不保留旧结构兼容分支：

### 9.1 `chat_context_checkpoints`

- `id`、`conversation_id`、`version`、`source_from_sequence`、`source_through_sequence`
- `recent_tail_from_sequence`、`entry_from_sequence`、`entry_through_sequence`、`payload_digest`
- `estimated_input_tokens`、`upstream_input_tokens`、`request_body_bytes`
- `model_id`、`provider_code`、`provider_profile_id`、`endpoint_family`、`compact_compatibility_hash`
- `prompt_version`、`status`、`quality_status`
- `created_at`、`expires_at`

### 9.2 `chat_context_entries`

- `conversation_id`、`checkpoint_id`、`source_message_id`
- `kind`：`verbatim`、`durable_memory`、`task_state`、`tool_result`、`image_observation`、`provider_compaction`
- `content_json`、`provenance`、`trust_level`、`token_count`、`sequence`、`created_at`、`expires_at`

`chat_context_entries` 是模型上下文内容的唯一事实源；checkpoint 只保存版本、来源范围、亲和信息、统计和 entry 范围，不重复保存摘要正文。官方 opaque item 作为 `provider_compaction` entry 保存。

用户文本、图片 OCR、图片说明和工具输出都属于不可信内容，必须保留 provenance，并以 user/tool 语义注入；不能因为进入 durable memory 就提升为 canonical system instructions，避免图片或工具内容中的提示词注入获得更高优先级。

### 9.3 `chat_assets`

- `id`、`system_account_id`、`conversation_id`、`sha256`、`mime_type`
- `original_width`、`original_height`、`model_width`、`model_height`
- `original_bytes`、`model_bytes`、`storage_key`、`thumbnail_storage_key`
- `processing_status`、`observation_status`、`observation_json`
- `created_at`、`expires_at`

### 9.4 `chat_conversations` 增量字段

- `context_revision`
- `active_checkpoint_id`
- `context_state`：`ready`、`compact_pending`、`compacting`、`compact_failed`
- `active_context_tokens`、`effective_context_limit_tokens`
- `compacted_through_sequence`
- Prompt Cache 不新增会话表字段；opaque key 可以由现有归属和会话 ID 确定性重建，前缀变化由供应商对精确输入自然判定

所有 checkpoint、entry 和资产跟随会话可配置保留策略（默认 3 天）清理。二进制文件删除必须先按索引有界定位，不扫描全目录。

## 10. 后台任务与并发

新增两个可观测任务：

- `chat-context-compact`：唯一键为 `conversationId + sourceRevision`，单会话锁，成功后原子安装 checkpoint。
- `chat-image-memory-extract`：数据库唯一执行边界为 `assetId + observationRevision + claimId`，首轮回答完成后生成隐藏说明；进程内允许旧 revision 与新 revision 任务短暂重叠，由数据库 claim/CAS 决定当前任务，旧任务结束不能吞掉新调度。

图片缩放是发送前依赖，不能等普通定时 worker；它在上传请求关联的受限执行池完成。压缩软水位走后台，硬水位只能等待同一已有任务或执行一次紧急压缩，不能并发创建多个相同压缩任务。

需要调用模型的压缩和图片说明任务仍然属于该用户的 AI 问答请求：

- 使用会话绑定的真实 API Key，重新进入现有网关，不建立内部免鉴权通道。
- 继续遵守当前路由策略、分组、账户代理、额度、并发、计费、使用记录和原始审计。
- 不跨 API Key、分组或供应商寻找所谓便宜摘要模型。
- 写入 `purpose = chat_context_compaction` 或 `chat_image_memory` 等内部用途事实，并禁止任务递归触发新的压缩任务。
- API Key 失效、额度不足或无可用账户时保留旧 checkpoint，记录可重试状态，不阻断页面读取和已有消息展示。

监控至少包含：队列长度、最老等待时间、成功/失败/重试数、单次耗时、压缩前后 token、压缩收益率、图片原始/处理后字节、413 次数、checkpoint 安装冲突和 worker 内存。

## 11. 前端状态

- 删除上下文下拉和 `contextWindowTokens` 请求字段。
- 输入框底部只显示圆形上下文进度图标，不让用户选择窗口。
- Tooltip 展示“已用 / 可用”、状态和是否为估算值；不要展示累计计费用量。
- 圆环分子是 active checkpoint + recent tail + 固定提示/工具的当前输入 token，分母是有效输入窗口。
- 压缩进行中只显示低噪音状态，不阻塞用户阅读；达到硬水位且发送必须等待时才显示明确提示。
- 图片上传、处理中、失败和重试状态在原文字顺序位置展示，不能集中到独立附件栏破坏图文关联。

## 12. API 增量

- `POST /my-chat/conversations/:id/assets`：流式上传并返回处理后的资产元数据。
- `GET /my-chat/conversations/:id/assets/:assetId/content`：按会话归属读取私有处理图。
- `GET /my-chat/conversations/:id/context-status`：返回圆环所需的 token、窗口、状态、估算标记，以及手动压缩需要的安全 `errorCode`、`retryAt` 和 `attemptCount`；不返回裸上游错误。
- `POST /my-chat/conversations/:id/context/compactions`：请求当前对话进入结构化压缩；先持久化 pending，再以 `202` 返回，禁止等待完整 120 秒压缩调用后才响应。
- `POST /my-chat/conversations/:id/stream`：删除 `contextWindowTokens`，图片块只接收 `assetId`。

手动压缩请求体严格为 `{ "model": "当前已选模型" }`。没有可压缩尾轮、当前正在发送或正在清空时返回明确 `409`；首次接受和已经运行都返回 `202`，状态分别为 `accepted`、`already_running`。前端只在服务端接受后显示低噪压缩状态，并继续轮询 `context-status`；不得先改写 token、checkpoint 或消息历史。进程重启后已持久化的 `compact_pending` 可以恢复 claim，同一会话不得启动第二个压缩任务。

服务端不信任客户端提供的 token、尺寸、MIME、asset owner 或上下文窗口。

## 13. 实施顺序

1. 修复 usage 采集和模型目录上下文元数据，删除 64K/64 轮硬限制和前端窗口选择。
2. 落地资产表、multipart 流式上传、图像缩放压缩和 asset reference 请求。
3. 落地 render history 与 model context checkpoint 分离。
4. 先实现确定性降负、结构化摘要 fallback 和原子 checkpoint。
5. 为明确支持缓存的模型增加稳定 opaque `prompt_cache_key`、Responses 前缀规范化、现有账户软亲和和 usage 回归；能力未知的目标不发送该字段。
6. 接入官方 Responses input token count 与 compact 能力探测。
7. 实现图片隐藏说明、后台任务、硬/软水位和圆形进度。
8. 完成 Mock、真实模型、多轮长上下文、图片、413、压缩失败、Prompt Cache 真实 usage 和 UI 验收。

## 14. 验收标准

- 页面仍能查看当前保留期内完整消息，压缩不改变可见正文。
- 模型请求只使用 checkpoint + suffix，不再固定读取 64 轮。
- 图片不再随聊天 JSON 上传 base64，三到四张大图不会再进入当前 24 MiB System API / 16 MiB 网关文本 lane 的多层冲突链路。
- 图片说明在后续轮次保持原有图文顺序，复制助手回答不包含隐藏说明、reasoning 或工具过程。
- 上下文窗口始终使用服务端模型目录最大值，前端无窗口下拉。
- 圆环与实际请求 usage 在容许误差内一致；上游不支持精确计数时明确显示估算。
- 压缩失败、摘要膨胀、worker 重启和版本冲突均不会覆盖原消息或安装坏 checkpoint。
- 当前输入本身过大时给出明确错误，不通过静默删除历史伪造成功。
- 压缩和图片说明的额外模型调用按普通请求计入用户 API Key 用量并可在使用记录中区分用途。
- 普通追加轮次保持稳定 opaque key 和 canonical prefix；能力未知的目标不接收 `prompt_cache_key`。
- 真实上游连续轮次逐轮核对 cached usage、账户亲和、延迟和成本；自动化结构回归不得替代真实命中结论。

## 15. 参考实现与官方资料

- OpenAI Prompt caching: <https://developers.openai.com/api/docs/guides/prompt-caching>
- Anthropic Prompt caching: <https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
- Gemini Context caching: <https://ai.google.dev/gemini-api/docs/caching>
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
