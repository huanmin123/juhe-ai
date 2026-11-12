# AI 账户检查模型与人工测试设计

## 1. 目标

本文固定 AI 账户“检查模型”“人工测试”和“后台系统检查”之间的职责边界，作为新增、编辑、列表测试、账号健康检测、激活检查、运行态恢复、账号级冷却复测和 Key 级恢复的统一业务口径。

核心目标：

- 人工测试只作为用户主动发起的诊断工具；无论已保存账户、草稿、授权实例、模型或请求形态，测试结果都不恢复或改变持久调度资格，也不作为激活或健康事实凭证。
- 后台系统检查统一使用账户保存的检查模型，并独占账户激活、健康确认和自动恢复职责。
- 模型目录中的默认机制只负责初始化账户检查模型，不能在运行时动态改变已有账户。
- 列表随账户摘要返回检查模型和检查请求形态；打开测试弹窗不发起候选目录请求，请求形态下拉立即以本地推导的账户可测试形态集展示（账户检查形态优先选中）；候选目录及其模型级可执行请求形态在用户首次展开模型下拉时一次性加载并收敛形态列表。
- 删除账户批量测试，避免用户一次性制造大量不稳定上游请求和复杂任务状态。

## 健康检查请求形态

- 当前账户必须保存不可空 `healthCheckEndpointMode`，数据库字段为 `health_check_endpoint_mode`。允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`、`images_json`、`audio_speech`、`audio_transcription_json`、`audio_job_get`、`video_create`、`embed_content`、`rerank_json`、`moderation_json`；`images_json` 必须有模型目录 `images` 能力证据，音频形态必须有模型目录 `audio` 能力证据；`video_create`、`audio_speech`、`audio_job_get` 进入 J1 无副作用连通性探测。
- 音频检查形态（M1 已实施，2026-10-04，`accountprobe` 内置，账户表单与健康检查可选）：
  - `audio_speech`：openai 协议向 `POST /v1/audio/speech` 发起短字符 TTS 请求（`response_format=wav`，最小化计费成本），判活 = 2xx + `audio/*` content-type；gemini 协议上游无该端点（恒 404），按契约 §5.1 改发 `POST /v1beta/models/{model}:generateContent`（contents 文本 + `responseModalities=["AUDIO"]` + `voiceName="Kore"`），判活 = 2xx + 响应 JSON 含 `candidates[0].content.parts[].inlineData`（不要求特定格式）。**2026-11-01 用户裁决起人工测试不再触发上述真实 TTS 生成**：`audio_speech` 人工测试改为密钥连通性探测（volcengine 走固定语音域 `POST https://openspeech.bytedance.com/api/v3/tts/create` + `X-Api-Key: speech_token` + `{"model":<model>}`；gpt/glm/xai 走 `POST {base}/audio/speech`；minimax 走 `POST {base}/t2a_v2`；gemini 走 `POST {base}/models/{model}:generateContent`，见 §17）；上述真实请求形态仅作为探针包内保留的历史构造路径存在。
  - `audio_transcription_json`：向 `POST /v1/audio/transcriptions` 发起 multipart 请求，`file` 使用探针内置小 WAV 样本（不依赖用户上传）；判活 = 2xx + JSON 信封含 `text` 字段（字段存在即判活，不要求非空——静音样本真实上游可能返回空 `text`）。不在 2026-11-01 裁决范围，保持真实请求语义。
  - `audio_transcription_json` 仍为真实付费小请求（无免费目录探针等价物），走账户保存的精确 mode 与检查模型；判活证据为上述协议成功信号，不以响应体之外的目录事实替代。
- 后台激活、周期健康、冷却恢复、质量确认、运行态恢复和账户默认测试直接使用账户保存的精确 mode，不再从协议族推导 JSON / SSE；其中 `images_json` 使用 `GET /v1/models` 并确认目录含检查模型，避免后台付费生图。
- 新账户默认：GPT 官方 API Key / OAuth 使用 `responses_sse`；通用 OpenAI-compatible、DeepSeek、当前 GLM Chat 和 Gemini OpenAI profile 使用 `chat_sse`；目标 `profile_glm_coding_openai_responses_v1` 实施后单独使用 `responses_sse`；Anthropic profile 使用 `messages_sse`；Gemini Native 使用 `generate_content_sse`。能力域拆分 M2 起，新建账户未显式提交 `health_check_endpoint_mode` 时按能力域默认落值：对话域沿用上述现行默认，图像域 `images_json`、音频域 `audio_speech`（连接允许集不含时落 `audio_job_get`）、视频域 `video_create`、向量域 `embed_content`、重排域 `rerank_json`、安全域 `moderation_json`；存量账户库中现值不变。
- **流式优先（2026-10-19 定案）**：检查形态默认值一律优先取流式形态。理由是真实流量以流式为主，非流式 2xx 可能被上游伪造或退化（实测样例：某中转对 `stream:false` 返回空 `choices` 的 SSE 帧仍回 HTTP 200），流式形态能更早暴露上游真实异常并复用同一份完成证据判定。该口径覆盖全部缺省生产面：前端建户表单、按能力域默认落值、OAuth 管理式建户、公开推送建户、导入适配器合成与导入缺省回退、人工测试形态顺序；显式提交的非流式形态仍然生效，存量账户现值不变。
- 首选 mode 未启用时，先取 `supported_endpoint_modes` 中第一个已启用的流式 mode，再取第一个已启用 JSON mode，最后取第一个可检查 mode；没有任何可检查 mode 时拒绝保存，不做旧字段兼容或运行时协议猜测。
- 历史库必须停服离线把字段从 `health_check_endpoint_family` 直接改为 `health_check_endpoint_mode`。GPT 全量写为 `responses_sse`；其他账户优先保留旧检查族对应的、已启用且可检查的 JSON / SSE mode，同族不可用时才回退到首个已启用可检查 mode。加密凭据中的 `supported_endpoint_modes` 必须先通过应用层 codec 解密改写，不能用普通 SQL 修改密文。

历史库迁移使用同一套生产环境变量和加密密钥，并按以下顺序执行：

```powershell
pnpm --filter juhe-ai-backend maintenance:migrate-account-health-check-endpoint-mode
$env:JUHE_AI_OFFLINE_MAINTENANCE_CONFIRMED = '1'
pnpm --filter juhe-ai-backend maintenance:migrate-account-health-check-endpoint-mode -- --execute
pnpm --filter juhe-ai-backend maintenance:migrate-account-health-check-endpoint-mode -- --verify
```

第一条命令只做 dry-run；正式执行前必须停止主服务和 worker。正式迁移在同一数据库事务内锁定账户表、分批解密并重写 GPT 凭据、替换字段、更新约束和校验全部账户，任一步失败都会回滚；提交后再独立执行 verify。迁移完成后移除本次 PowerShell 会话中的确认环境变量。

## 2. 名称与字段

页面名称：

- 模型目录：`默认检查模型`
- 模型目录行内标签：`默认检查`
- AI 账户表单：`检查模型`
- 人工操作入口：`测试`

代码与存储名称：

- 账户字段：`healthCheckModel`
- 数据库字段：`health_check_model`
- 供应商摘要只读字段：`defaultHealthCheckModel`（由模型目录派生，不落库、不可配置）
- 人工测试请求字段：`model`，只表达本次测试输入

删除旧语义：

- 删除账户 `defaultTestModel / default_test_model`。
- 删除供应商 `defaultTestModel / default_test_model` 命名。
- 删除 `provider_default_test_models`。
- 删除 `healthCheckEnabled / health_check_enabled`。账户级关闭入口收敛为唯一总开关 `backgroundProbeEnabled`（[AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）：手动模式账户不排入任何后台检查；“持续恢复探活”降为其子开关，仅父开关开启时生效。账户高级配置中的“持续恢复探活”不是系统检查开关：它只决定 `temporary_unavailable` 在前 10 分钟有界最终确认后是否继续长期低频恢复，周期健康检查、首次激活、人工测试、`rate_limited` 和账户内 Key 复测始终不受影响。

项目不在运行路径保留旧字段兼容、双读双写或自动迁移。**历史截面（SQLite 模式，已于 2026-10-25 退役）：** 曾有的唯一例外是 SQLite 初始化发现 `accounts.health_check_endpoint_mode` 的 CHECK 约束缺少 `images_json` 时，可在事务内重建该表、保留数据和关联对象并执行外键检查，任何失败必须回滚并中止启动；该自愈路径随 SQLite 退役删除，现行 PostgreSQL schema 由 `juhe-ai-maintenance --ensure-schema` 幂等承载，不含启动期表重建。

## 3. 检查模型

每个已保存且参与后台检查的 AI 账户最终保存一个检查模型。账户表单编辑态与 API Key / 媒体域 / OAuth 直接 Token 录入创建态必须选择非空 `supportedModels` 和其中的 `healthCheckModel`；chat 域托管 OAuth 的手动授权、Refresh Token（及 Grok SSO 导入）创建态不收集这两个字段，由后端 `CreateOAuthAccount` 派生（见 §4）。表单不会自动上游目录发现，用户可手动刷新补全候选。点击测试或确认时，缺少任一模型只提示并中止，不把目录同步当作隐式补全：

- 表单输入中的 `healthCheckModel` 不可为空，必须由用户从最终 `supportedModels` 中选择；不能依赖前端先请求模型目录或默认值补全。
- 必须属于账户最终的 `supportedModels`。
- 必须是当前账户协议可以验证的启用模型。普通文本生成模型按账户保存的精确 endpoint mode 发起最小生成请求；人工 `mode=image_generation`（`images_json`）测试自 2026-11-01 起为密钥连通性探测（§17），不再真实出图。后台 `images_json` 检查使用模型目录精确包含检查模型的证据，不能产生付费图片。
- 删除支持模型时如果命中当前检查模型，必须先重新选择，不能静默切换到其他模型。
- 目录发现后如果当前检查模型不在上游，按本地目录 `releaseDate` 倒序、同日 `catalogOrder` 升序选择下一候选；没有候选时记录“检查模型配置异常”并停止本轮探针。

账户向用户暴露必填的“健康检查请求形态”。普通生成模型只能选择账户已经启用的 JSON / Streaming mode，纯图像模型显示并保存 `images_json`。普通生成模型保存值就是后台探针最终使用值；人工测试仍可为单次诊断临时选择其他已启用 mode。纯图像模型人工测试使用实际图片请求形态，不得误调用 Chat Completions 或 Responses；后台检查使用目录探针。

人工图像检查使用实际 Images API 端点；自 2026-11-01 起按密钥连通性语义判读（§17）：请求体仅携带 `model`（空 prompt），上游 400/422 判为密钥有效，不产生真实出图与计费。调用次数与超时仍受人工测试策略约束。后续新增非 OpenAI 协议图像模型时必须为对应供应商驱动定义明确人工测试请求，并为后台检查定义无计费的模型存在性证据。

Gemini 原生账户可以从自身上游接口能力中选择 GenerateContent 或 Interactions 的 JSON / Streaming 生成形态；混合供应商账户可以选择 Chat Completions、Responses、Messages 或 GenerateContent。人工测试和后台检查必须按本次选中的精确 mode 构造下游诊断请求，再由混合账户模型映射决定实际上游协议和模型；`message_token_counting`、`count_tokens`、`embed_content` 等工具接口不进入检查协议选项。

## 4. 默认检查模型

供应商级默认检查模型不再是可配置项：`provider_default_health_check_models`、`provider_system_default_health_check_models` 与协议档案 `default_health_check_model` 已随《供应商与上游协议面重构设计》§9.11 删除，系统不恢复个人默认、管理员系统默认或协议档案默认三层配置。

模型目录页展示的供应商“默认检查模型”由系统只读派生：取该供应商启用、目录可见、未过下线日期且声明文本生成协议的内置模型，按 `release_date` 倒序（同日按 `catalog_order`、模型名升序）选择最新一个；纯媒体供应商没有文本生成模型时回落最新目录模型。该值只用于展示与新建账户参考，不提供“设为默认检查”操作。

账户配置阶段的规则保持不变：

- 新建账户选择供应商和协议档案后不预填支持模型或检查模型；页面可按需读取本地候选，但不会自动请求上游目录。
- 只有用户点击“同步上游模型”才请求上游模型目录。账户弹框打开、字段变更、取消以及测试或确认都不会触发同步；手动同步失败不清空草稿，已配置完整模型后不影响保存、人工测试或后续系统检查。
- 用户手动目录发现时，推荐模型必须同时存在于成功上游目录、当前账户支持模型和当前协议可测试目录；它只更新当前未保存表单推荐。多 API Key 账户分别固定每把 Key 探测，所有探测成功后取模型交集；任一 Key 失败时不返回部分目录，避免把某把可调度 Key 不支持的模型写入账户能力边界。
- 用户手动上游模型同步对单把 Key 都只有一次 `10s` 截止；不得复用文本生成诊断的 `10s/20s/30s` 阶梯。浏览器断开同步请求时，服务端必须把取消信号传入草稿凭据处理和上游目录请求，停止尚未开始的后续 Key 探测，不再继续占用上游连接。
- 服务端发起的上游模型目录探针必须携带不可由客户端伪造的内部标记；只有账户所属驱动能在“已标记 + 精确 `GET` 模型目录路径 + 允许的账户类型”同时满足时放行上游 URL。客户端的 `GET /models`、`GET /v1/models` 和 `GET /v1beta/models` 仍由本地目录处理，不能因账户探针而变成网关热路径的上游请求。
- chat 域托管 OAuth 建号时，前端创建表单不再提交 `supportedModels` 与 `healthCheckModel`，后端派生（`CreateOAuthAccount`）是 chat 域托管 OAuth 建号的唯一来源；创建接口仍接受显式提交并优先于派生（供 API 直调）。
- 后端创建接口在未显式提交 `supportedModels` 或 `healthCheckModel` 时，必须按同一 owner 作用域解析并持久化：在最终支持模型集内取首个声明该账户检查形态的模型，无模型声明时回落首个支持模型；解析结果仍通过支持模型和协议能力校验。
- 账户保存后，后台所有检查只读取账户自己的 `healthCheckModel`。
- 定时健康检查使用独立流量来源 `account_health_check`；`cooldown_retest` 只表示临时不可调用或限流账户的恢复复测。
- 检查模型或请求形态错误仍记录健康诊断并安排复检，但不累计账户连续失败阈值。
- 目录派生值变化不会批量改写已有账户，避免一次目录操作改变全部现有账户的探针行为。
- 编辑已有账户时优先回填账户保存值，不再重新套用供应商默认值。

## 5. 人工测试

人工测试包含：

- 新增账户表单测试。
- 编辑账户表单测试。
- 账户列表单项测试。
- 管理侧、用户侧和授权实例侧测试。

generic_http 账户（`vendor_code=NULL`）的人工测试候选与测试请求校验按 `account_supported_models.model_id` 读取真实 `models` 行，保留 modes/协议等元数据，再与账户连接的 endpoint modes 求交集；不得因 provider 为空返回空候选、伪造 `custom` 或按模型名猜 vendor。

人工测试使用账户或草稿配置作为请求输入，默认不参与账户控制状态：

- 测试成功和失败都不修改 `healthCheckModel`、`supportedModels` 或其他账户配置。
- 测试成功和失败都不直接修改账户、授权实例或来源账户的 `status`、`schedulable`、冷却、最近错误、健康检查事实和运行态。新增和编辑表单草稿测试协议成功时，浏览器仅将当前未保存表单的状态单选预选为 `active`；用户手动选择 `disabled` 时保持停用，只有后续保存才会写入账户。
- 不写入、确认、清理账户失败样本、代理桶、客户端 IP 熔断、客户端 IP 账号回避、Codex turn 账号回避或会话亲和状态。
- 不推进账户内 Key 轮换，也不写入、恢复或摘除 Key 运行态。
- 不把人工测试结果作为创建、编辑、导入、激活或恢复的后端前置凭证；表单中的 `active` 预选仍可由用户改回“待检查”或“停用”。人工测试没有任何状态恢复例外。

人工测试允许保留的事实：

- 测试任务、测试会话和脱敏诊断结果。
- 使用记录、实际 token / cost 和审计记录，`traffic_source = manual_account_test`。
- 为完成 OAuth 请求所必需的 Access Token 刷新。刷新属于凭据生命周期维护，只允许写回新 OAuth 凭据，不能改变账户状态、调度、健康事实或额度调度状态。

如果测试响应携带额度头，人工测试不更新生产额度快照，避免诊断工具间接改变调度和可用性。

### 5.1 新增和编辑表单测试

- 测试模型默认使用当前表单的 `healthCheckModel`，用户可在测试弹窗内为本次测试自由切换供应商目录内的其他候选模型；候选来源与 5.2 相同（账户供应商、协议档案和当前所有者作用域可见的启用可测试模型目录），按草稿表单当前配置计算，经草稿测试选项接口（`POST /accounts/test-draft-options`、`POST /my-accounts/test-draft-options`，body 携带草稿账户载荷）获取。
- 测试模型与表单“检查模型”配置解耦：本次测试选择不回写表单；表单 `healthCheckModel` 仍必须属于当前表单 `supportedModels`（保存契约不变）。
- 后端以测试请求携带的模型为准（未携带时回落表单检查模型），按供应商目录能力校验该模型与所选请求形态；测试表单支持列表之外的模型时不做支持列表成员校验（与 5.2 列表测试同语义）。
- 刚加入表单的支持模型可以作为新的检查模型立即测试，但只有用户保存表单后才成为账户正式配置。
- 测试结果不自动保存或直接恢复已保存账户。协议成功后，当前未保存草稿不是 `disabled` 时，页面自动预选“可调度”；用户仍需点击保存，且可在保存前改回“待检查”或“停用”。

### 5.2 列表操作测试

列表“测试”是面向用户的自由模型诊断工具，不属于账户健康检查：

- 模型候选来自该账户供应商、协议档案和当前账户所有者作用域可见的启用可测试模型目录；普通生成模型和图像模型都必须匹配其实际请求协议能力。
- 能力域拆分 v8-C 对话协议面排除：目录 mode ∈ {`image_generation`, `video`} 的行不得经对话协议族（chat_completions/responses/messages/generate_content/interactions）进入对话测试候选或获得对话测试形态；图像行只经 images_json 测试面的图像分支（`images` 协议 + api_key 门）保持可测，且测试形态只余 `images_json`。据此对话账户的测试下拉不再把生图/视频模型当作对话协议测试目标；`images_json` 测试面的图像模型分支不受影响（图像域账户继续经它选择图像模型）。**2026-11-01 媒体域连通性裁决起**，视频行（`video` 协议 + api_key 门）经 `video_create` 连通性测试形态进入候选（仅该形态，不进对话族；gemini 原生 google_oauth 视频账户不在放开面，见 §17）。**2026-11-12 B3 起**，音频目录行不再整体排除：按 `audio_speech` / `audio_transcription_json` 进入非对话连通性候选，仍不进对话协议族。
- 不受账户 `supportedModels` 限制，允许用户验证尚未加入账户支持模型的供应商模型。
- 默认选中账户 `healthCheckModel`，但用户可以为本次测试自由切换其他候选模型。
- 后端固定命中当前账户并绕过账户 `supportedModels` 调度过滤，只验证该账户凭据、Base URL、代理、协议和指定模型的实际响应。
- 草稿测试支持 `proxyPoolId`（代理池设计 §6.1/§8.3）：执行前按池策略临时选一个成员、不写 `account_proxy_leases`，选定成员只进入草稿测试信封用于本次出站；保存账户时重新按池策略分配，不承诺与测试出口一致。池停用/无 enabled 成员时 400 明确失败，不直连；本地池不可用按 `proxy_pool_unavailable` 归类，不写账户状态。
- 测试成功不会把模型追加到 `supportedModels`，不会修改检查模型，也不会自动保存任何账户配置。
- 只有默认的保存检查模型和保存检查请求形态同时未被用户临时改动，且账户当前持久不可调度时，协议完成的成功结果才可触发上述恢复例外；模型、请求形态、配置版本、任务终态或账户资格任一不匹配均不恢复。

### 5.3 删除批量测试

- 账户页面不再提供“批量测试”入口。
- 后端不再接受批量人工测试 session 或批量任务创建请求。
- 删除批量测试的共同模型计算、分批提交、并发执行、批量取消和批量结果恢复逻辑。
- 单账户测试任务仍可独立异步执行，但账户 A 的任务不能阻止账户 B 开始测试。

## 6. 人工测试会话隔离

- 每次单账户测试使用独立 `testSessionId`。
- 账户 A 的运行中任务不能阻止用户打开或启动账户 B 的测试。
- 关闭 A 弹窗只结束当前前端展示绑定；后台任务可以继续到终态，但不能占用用户级全局测试锁。
- 用户显式点击“停止测试”时，只取消当前测试会话的未完成任务。
- 同一账户的重复并发测试可以按 `accountId + requester + purpose` 去重或提示，但不能扩散为跨账户互斥。
- 旧会话结果和延迟返回的账户详情不能覆盖后来打开的账户弹窗。
- 浏览器 `sessionStorage` 只保存恢复轮询所需的 session ID、task ID、账户 ID、模型和请求形态等最小元数据；完整请求体、响应 Header、响应正文和诊断结果只保留在当前内存或由受控后端任务接口按需读取。

## 7. 列表按需加载

账户列表返回展示、筛选和测试弹窗初始化所需的摘要字段，包括账户自己的 `healthCheckModel` 和 `healthCheckEndpointMode`；不为了测试提前加载完整支持模型、候选模型目录、endpoint modes 数组或凭据。

本节按需接口的当前落地范围仅为 Node 后端与 Vue 前端；本次没有修改 Go，也不以 Go 契约作为验收依据。

候选目录在用户首次展开模型下拉时一次性加载（2026-07-30 起，打开弹窗本身零 options 请求）；打开弹窗时请求形态下拉先以本地按协议档案推导的账户可测试形态集初始化——账户检查形态优先选中，其后流式在前、JSON 在后，与 options 返回的 `testEndpointModes` 同序——options 加载完成后按服务端返回的模型级 `testEndpointModes` 收敛并保持当前选中（不可用时取首项）。测试按钮在可用模型与请求形态齐备前保持禁用（已保存账户带检查模型时首开即以检查模型与推导形态可提交），后续搜索继续复用同一接口：

```text
GET /__aisys__/api/accounts/:id/test-options
GET /__aisys__/api/my-accounts/:id/test-options
```

响应严格为 `Array<{ id, name, testEndpointModes }>`，支持 `keyword`、`limit` 和 `selectedIds`。每个 `testEndpointModes` 都由 Node 使用当前账户已启用 endpoint modes、该模型的 `supportedApiProtocols`、以及当前账户模型映射和映射上游模型协议计算最终可执行交集。HTTP 响应不包含凭据、原始 `supportedApiProtocols` 或完整模型定义。

用户在模型下拉中切换模型后，Vue 直接采用该 options 项的 `testEndpointModes`，并自动选择首个可用形态；加载 options 时若自动选择首个模型也同步形态，不再向浏览器发起第二个 capabilities 请求。`testEndpointModes` 的顺序固定为「账户保存的 `healthCheckEndpointMode` 优先，其后流式形态在前、JSON 形态在后」——同一协议内 `chat_sse` / `responses_sse` / `messages_sse` / `generate_content_sse` / `interactions_sse` 先于对应的 `_json` 形态。后端可保留 `GET /__aisys__/api/accounts/:id/test-options/models/:modelId` 及个人端镜像作为兼容能力接口，但页面不调用它。测试执行仍由后端按账户 ID 读取受控凭据，并在提交时通过 `resolveAccountManualTestSelectionAsync` 重新校验模型与请求形态。

合法跨协议映射按来源协议选择检查形态、按映射目标协议校验上游模型，不能用目标协议直接裁掉来源检查形态。账户目录同步不属于检查形态，不参与自动测试或健康结果；其 `requestUrl` 只说明该次同步的受控目录路径。

后台检查和人工测试不能仅凭 HTTP 2xx 判定成功。JSON 响应必须包含对应协议的正常完成对象，Streaming 响应必须包含对应协议的完成事件，图片响应必须满足图片协议成功证据。空正文、仅 `[DONE]`、HTML、畸形 JSON 或只有未完成数据片段都不能激活 `pending_test` 账户；framing 完整时归为 `upstream_http_failure`。人工测试只显示诊断，激活、周期健康、请求失败二次确认和质量确认则把它作为通用可用性失败。上游目录是否存在不参与该判断。媒体域三种模式（`video_create` / `images_json` / `audio_speech`）的人工测试自 2026-11-01 起是唯一例外：按 §17 的连通性状态码分类表判读，不要求协议完成证据。

	普通文本生成检查还必须为每次实际请求生成独立的随机算术挑战：报文为「接口连通性自动测试：请计算 A+B，只回复数字」，A、B 各取 10～89，期望答案是两数之和的十进制文本。2026-10-15 起停用历史的「请原样输出固定令牌」形态——上游内容审核会把这类报文整段判为 HTTP 403，把正常账户打成临时不可调用；算术题要求模型真实作答，实测不被拦截。可见输出包含该答案即匹配（答案两侧不得是数字，避免命中更长数字的片段）；reasoning、thinking 和工具内容不属于可见输出。图片与音频继续按各自的协议成功证据判断，不校验文本答案。严格 HTTP 失败结果保留 HTTP framing 诊断，但不得清理运行态、事前确认或账户电路升级证据。

新增和编辑表单直接使用当前表单中的 `supportedModels`、`healthCheckModel`、endpoint modes 和未保存配置，不额外读取已保存详情。表单测试不再请求自由模型选项。

## 8. 后台系统检查

后台系统检查默认启用；账户级「后台自动探测」总开关（[AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）关闭的手动模式账户不排入任何后台检查。不同检查场景共享底层最小请求执行器，但各自拥有明确的状态策略：

| 场景 | 模型来源 | 允许的状态副作用 |
| --- | --- | --- |
| 新账户激活检查 | 账户检查模型 | `complete_success` 后 `pending_test` 转为 `active`，并同时按账户时间计划写入派生闸门 `availability_schedule_gate`（时段外为 0，表现为「非时间段」，不改写 `status`）；完整 framing 协议失败和 `transport_incomplete` 都是可用性失败，从首次起满 24 小时后仍独立失败才原子转为 `error` |
| 关键配置变更复检 | 账户检查模型 | 按配置变更类型进入待检查或正常健康阈值流程 |
| 正常账户周期健康检查 | 账户检查模型 | 写健康事实；达到阈值且确认账户级故障后进入保护状态 |
| 运行态恢复探针 | 账户检查模型 | 只推进对应运行态状态机 |
| 账号级冷却复测 | 账户检查模型 | `complete_success` 只恢复匹配来源的自动状态；进入长期不可用后固定每 1 小时复测，只有连续独立 `transport_incomplete` 从观察起点满 7 天后仍失败才原子转为 `error` |
| API Key 恢复探针 | 账户检查模型 | 只更新目标 Key 运行态 |

手动模式（总开关关闭）账户不进入上表任何场景；关闭时已有过程态按总开关设计归一化。

底层请求执行器只返回诊断结果，不自行决定状态。上层策略服务根据 `purpose` 应用允许的副作用，不再依赖通用 `disableAccountStateMutation` 布尔参数控制所有场景。

健康检查任务入队时必须记录账户 `configRevision`。探测完成后的成功、`transport_incomplete` 失败计数和达到阈值后的保护状态写入都使用该版本及本次失败快照做 CAS；探测期间如果账户配置发生变化，旧结果归为 `stale` 并丢弃。传输失败探测开始后如果出现更新的真实协议成功信号，旧失败也不得重新累加计数或把账户置为临时不可调用。

建议服务边界：

```text
executeAccountProbe()
runManualAccountDiagnostic()
runAccountActivationProbe()
runScheduledHealthCheck()
runRuntimeRecoveryProbe()
runCooldownRecoveryProbe()
runApiKeyRecoveryProbe()
```

## 9. 新增账户与导入

- 未提交状态的新建账户和所有导入账户默认保存为 `pending_test` 且不可调度；导入载荷中的 `active` 也先规范化为 `pending_test`。
- 普通创建接口显式提交 `status = active` 是受支持的人工风险确认例外：账户直接保存为 `active`、`schedulable = true`，并跳过首次后台激活检查。该例外不把人工草稿测试当作激活凭证，也不改变默认创建、导入或后续状态转换的待检查路径。
- 待检查的创建或导入账户在保存事务完成后立即投递后台激活检查，不等待人工测试任务，也不接受人工测试任务 ID 作为激活凭证。
- 后台激活检查得到 `complete_success` 后写入 `active`，并按账户当前时间计划写入派生闸门 `availability_schedule_gate`（无计划 NULL / 时段内 1 / 时段外 0）；健康成功不能绕过时间计划——时段外账户仍不可调度，但状态保持正常。完整 framing 但协议未成功和 `transport_incomplete` 都保持 `pending_test`、写失败事实并每 1 小时复检；从首次可用性失败起满 24 小时后的再次独立失败才原子写为 `error`，错误码固定为 `account_activation_check_timeout`。账户列表从首次失败起派生显示红色“检查失败”，tooltip 展示失败详情和自动重试提示，但不新增中间持久状态枚举。
- 检查失败的 `pending_test` 提供“重新检查”：同一事务把失败计数、首次失败时间、错误摘要和检查计划重置到新账户待检查基线，随后立即投递后台激活检查。尚无失败事实的普通待检查账户不提供该操作。
- 用户可以在保存前人工测试草稿，但该结果只用于判断是否愿意保存，不参与账户激活。
- 明确创建为 `disabled` 的账户尊重人工停用，不投递激活检查。

## 10. 编辑账户

编辑保存不再要求先人工测试成功。

配置变更按影响分层：

- 凭据、Base URL、供应商协议档案或关键代理变化：保存后进入 `pending_test`，立即投递后台激活检查，避免未经系统确认的新连接配置继续参与调度。
- 检查模型变化：保持原账户状态，重置下次检查时间并立即投递后台健康检查；模型级配置失败不能直接停用整个账户。
- 支持模型变化：必须保证检查模型仍在支持模型中；保存后刷新账号能力缓存，不把人工测试结果作为保存条件。
- 名称、标签、备注、优先级等非连接配置：不改变状态，不触发激活检查。

## 11. 多 API Key 账户

- 人工测试可以返回每个 Key 的诊断明细，但不能写 Key 运行态。
- 新账户后台激活检查按系统探针策略检查 Key 池，至少一个 Key 得到 `complete_success` 时账户可以激活；只有带独立 `transport_incomplete` 证据且通过来源 CAS 的 Key 才可进入对应自动 transport 运行态，完整 HTTP / 协议失败（`upstream_http_failure`）不构成 transport 证据，不进入该运行态。
- 已保存账户新增或替换 Key 后，由后台 Key 检查初始化或更新 Key 运行态，不复用人工测试结果。
- 账号级周期健康检查只使用当前可用 Key 集合；Key 级冷却恢复固定命中目标 Key。
- 所有 Key 不可用时，账户通过 Key 池派生可用性退出调度，不需要由人工测试改变账户状态。

## 12. 失败分类

J1 账户健康探针统一返回 `complete_success`、`upstream_http_failure`、仅代表 `transport_incomplete` 的 `upstream_failure`，以及 `probe_task_failure/stale/unknown`。传输电路、账号冷却和 Key 恢复只把第三类视为负向证据；固定模型、固定协议的激活、周期健康、请求失败二次确认和质量确认还把第二类视为账户可用性失败。任务、本地配置或过期结果不计数、不改状态。历史存量 `framing_complete_neutral` 在 J1 读取侧归一为 `upstream_http_failure`；质量预检与网关运行态探针仍使用各自独立契约的 `framing_complete_neutral`。

自动 transport 失败候选：

- Base URL、代理、DNS、连接或 TLS 故障。
- lane hard timeout、读取中断、响应未完整结束或 framing 未完成。

凭据失效、账户级授权失败、限流、封禁或服务级故障不得从上游 status/body 自动推断；只有用户显式账户错误策略可以授权对应业务动作。本地可验证的凭据缺失、解密失败、配置非法和 OAuth token 生命周期错误走独立本地路径。

本地可验证的模型或检查配置故障：

- 本地目录中检查模型缺失、不可见或不属于最终支持模型。
- 本地账户能力没有可执行的检查 endpoint mode。
- 本地请求形态与已声明协议能力不匹配，或最小检查请求模板无法构造。
- 本地凭据缺失、解密失败或必要配置非法。

这些本地故障只记录“检查模型配置异常”，走 `probe_task_failure/unknown`，不能直接把整个账户写成 `temporary_unavailable`、`rate_limited` 或 `error`。上游 status/body 中的 `model_not_found`、未授权、权限不足、`unsupported_endpoint` 或类似文案不属于本地可验证配置事实；framing 完整时一律是 `upstream_http_failure`，只形成通用可用性失败，不派生具体业务语义。成功检查只证明当前检查模型和对应最小请求链路可用，不代表全部支持模型都已经逐个验证。

## 13. 状态边界

后台检查机制始终存在，但按账户状态选择任务。手动模式账户（总开关关闭，见 [AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）健康状态只有 `active` / `error` / `disabled`：无 `pending_test` 自动激活、无冷却复测，不排入下表后台检查：

| 账户状态 | 系统行为 |
| --- | --- |
| `active` | 周期健康检查和必要运行态恢复 |
| `pending_test` | 激活检查；任务 unknown 有界顺延，完整 framing 协议失败或 `transport_incomplete` 后每 1 小时重试，首次可用性失败满 24 小时后仍有独立失败才进入 `error` |
| `temporary_unavailable` | 账号级冷却恢复 |
| `rate_limited` | 限流恢复 |
| `error` | 人工“异常恢复”原子重置为 `pending_test` 并立即投递激活检查（所有场景；手动模式账户直接恢复 `active`，无后台复检） |
| `disabled` | 不主动探测，尊重人工停用 |

人工测试默认只诊断，不能直接成为任何已保存账户的状态恢复入口；新增或编辑草稿协议成功后仅在浏览器本地预选“可调度”，不写库、不清理运行态，必须由用户保存后才生效。

自有账户在所有非 `disabled` 状态都允许人工停用，包括 `pending_test` 和 `error`；授权实例继续遵守授权侧状态管理边界。

## 14. 接口与存储契约

账户创建、更新、详情、导入和导出统一使用 `healthCheckModel`。

供应商列表 / 详情返回只读 `defaultHealthCheckModel`（由模型目录按最新发布时间且可文本生成的模型派生），不提供设置供应商默认检查模型的写接口。账户不再提供独立“保存默认测试模型”接口；修改账户检查模型只能通过账户编辑保存接口完成。

人工测试接口：

- 新增 / 编辑表单测试默认使用草稿 `healthCheckModel`；请求携带顶层 `model` 时以请求模型为准（按供应商目录与请求形态能力校验，与 5.1 契约一致），不回写表单。
- 列表测试必须显式提交本次 `model`，并校验模型属于当前账户供应商、协议档案和所有者作用域可见的启用可测试模型目录；不要求属于账户 `supportedModels`。图像生成模型必须使用实际图片 endpoint，不能因为账户保存了 `responses_sse` 就发送文本 Responses 请求，也不能回退为模型目录探针。
- 多 API Key 的默认人工账户可用性测试复用固定 Key 诊断器：每账户最多 `3` 个并发，真实上游超时才按 `20s -> 30s -> 40s` 阶梯重测该 Key，任一 Key 成功即取消尚未开始的请求。成功前完成的失败 Key 和成功 Key组成实际结果，已取消或未开始的 Key 明确标记为未测试；人工测试不把“未测试”解释为通过或失败，也不写入任何 Key 运行态。模型目录同步仍必须逐 Key 获取并取交集，不能使用首成功短路。
- 测试任务结果不能被后端账户保存、状态恢复或后台检查任务当作可信状态凭证；新增和编辑草稿协议成功只允许浏览器预选本地状态单选，不能直接写库。

## 15. 验证要求

- 新增、编辑、草稿、已保存自有账户和授权实例的人工测试成功或失败后，账户、授权实例、Key、健康事实和运行态均不变化；新增或编辑草稿测试协议成功仅将非 `disabled` 的未保存表单预选为 `active`，保存前不写库。
- 新增 / 编辑测试默认使用表单检查模型，可切换目录候选且不回写表单；列表测试关闭重开后仍默认使用账户检查模型。
- A 账户测试运行时可以打开和启动 B 账户测试，旧结果不能覆盖新弹窗。
- 列表接口只增加检查模型和检查请求形态两个轻量标量；打开测试弹窗不读取 `test-options`，请求形态下拉先以本地推导的账户可测试形态集初始化（检查形态优先选中），首次展开模型下拉时一次性读取 `{ id, name, testEndpointModes }` 的批量 `test-options` 并联动收敛形态，无检查模型且目录未加载或加载失败时禁止提交，切换模型立即联动该项请求形态。options 响应 envelope 的 `data` 保持数组，顶层携带 `total`（过滤去重后、limit 截断前候选总数）与 `hasMore`（T23），前端据此在下拉底部展示截断提示；草稿测试的 `test-draft-options` 同形。
- 页面和后端均不存在账户批量测试入口。
- 新账户保存后保持 `pending_test`，后台激活检查成功后自动进入 `active`。
- 人工草稿测试成功不能直接激活新账户；它只会预选表单“可调度”，用户保存后才按该显式选择创建账户。
- 定时健康检查不存在独立健康检查开关，也不存在 `health_check_enabled` 候选条件；账户级「后台自动探测」总开关是唯一关闭入口（[AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）。`temporaryUnavailableContinuousProbeEnabled` 只由冷却复测读取，不能作为定时健康检查候选条件。
- 所有系统探针严格使用账户 `healthCheckModel`，缺失或非法时不回退其他模型。
- 修改模型目录默认检查模型只影响后续新账户初始化，不改变已有账户。
- 多 Key 人工测试不写 Key 状态，后台激活和 Key 恢复探针可以按职责写入 Key 状态。
- 模型级失败不把账户整体标记为不可用，账户级故障达到策略条件后才进入保护状态。
- 能力域拆分后（2026-11-12 D0/D6 并轨）：所有场景新建账户统一 `pending_test`，由 J1 首次激活检查转 `active`（手动模式除外）；导入账户同样保留 `pending_test`。媒体/工具账户的 `health_check_endpoint_mode` 必须是连接允许集内的安全探测形态（图像 `images_json`、视频 `video_create`、音频 `audio_speech` / `audio_transcription_json` / 长音频 `audio_job_get`、向量 `embed_content`、重排 `rerank_json`、安全 `moderation_json`）；`audio_job_create` 仅作记录，不执行 POST。视频恢复准入与成功 CAS 保留，但不再是激活的唯一路径。

## 16. 媒体账户检查边界（2026-11-12 D0/D6 并轨）

来源：AI 账户能力域拆分 v7 + 2026-11-12 D0/D6 裁决。媒体账户与对话账户共用 J1 健康状态机；差异只在探针请求形态（媒体用无副作用连通性探测）和模型/协议选择入口（人工测试不选模型/协议）。

- **按域默认落值**：新建账户 `health_check_endpoint_mode` 未显式提交时按域默认落值——对话 `chat_sse`、图像 `images_json`、视频 `video_create`、音频 `audio_speech`、向量 `embed_content`、重排 `rerank_json`、安全 `moderation_json`；若连接允许集不含该默认值（例如 qwen 长音频只有 `audio_job_*`），落允许集内的安全代表模式 `audio_job_get`。仅影响新建账户，存量账户库中现值不改。
- **后台周期检查（J1）参与范围**：J1 候选白名单含对话族、`images_json`、`video_create`、`audio_speech`、`audio_job_get`、`embed_content`、`rerank_json`、`moderation_json`；媒体账户与对话账户一样进入激活、周期、冷却恢复和锁死相关探测。`audio_job_create` 不在候选白名单，永不 POST 创建真实任务。
- **激活检查（pending_test 首检）**：所有场景新建账户默认 `pending_test`；J1 首次激活检查成功转 `active`，失败转 `error`（确定性硬门）或按锁死/冷却状态机处置。导入账户同样保持 `pending_test`。
- **模型检测（J3b）参与范围**：媒体形态仍不参与对话模型检测；媒体账户用 J1 连通性探测和人工 `kind=connectivity`。图像 `images_json` 后台探测为只读 `GET {base}/models`（厂商任务查询例外），不构造真实出图请求。
- **检查失败对派发的影响**：媒体同步 lane 受候选窗健康门（status / schedulable / cooldown + endpoint mode 门）；任务面（videos / audio jobs 查询、下载）账户亲和直连不查健康。锁定账户的可恢复探针失败不提前终结 hold（D12）。
- **存量零变化**：以上只约束新建账户的默认落值和探测形态；存量账户的激活、周期、恢复检查仍按各自保存的 mode 执行，无法安全探测的 mode 在 B0 回填后统一迁移。

## 17. 媒体域连通性探测（2026-11-01 用户裁决）

媒体域账户（视频 / 图像 / 语音）的人工测试与后台 J1 探针统一使用**密钥连通性探测**：只验证 API key 能否访问上游，不真实生成产物——视频避免真实计费任务，图像/语音避免真实消耗。`accountprobe` 承担人工测试构造，`exactkeyprobe` 承担 J1 构造；两者共享同一安全形态与状态码分类口径。

三种媒体模式的连通性请求（构造收口 `accountprobe`，经既有出站通道：账户 base_url 版本根直拼、账户凭据认证策略与伴随头、账户代理、系统客户端身份头同源）：

| 模式 | 连通性请求 | 请求体 |
| --- | --- | --- |
| `video_create` | `GET` 查询固定占位任务 `probe-connectivity-check`：gpt/xai `GET {base}/videos/{id}`；volcengine `GET {base}/contents/generations/tasks/{id}`；qwen `GET {base}/api/v1/tasks/{id}`；minimax `GET {base}/query/video_generation?task_id={id}`；glm `GET {base}/async-result/{id}`；gemini `GET {base}/models/{model}/operations/{id}` | 无 |
| `images_json` | 人工 `accountprobe`：gpt/xai/glm/volcengine `POST {base}/images/generations`，qwen `POST {base}/api/v1/services/aigc/multimodal-generation/generation`（空 prompt）；J1 `exactkeyprobe`：gpt/xai `GET {base}/models`，volcengine `GET {base}/contents/generations/tasks/{id}`，qwen `GET {base}/api/v1/tasks/{id}`（固定占位任务，404 视为可达） | 人工：gpt/xai/glm/volcengine 仅 `{"model":<model>}`（空 prompt）、qwen 空 `messages`；J1：GET 空体 |
| `audio_speech` | volcengine：`POST https://openspeech.bytedance.com/api/v3/tts/create`（固定语音域，不读账户 ark base_url；`X-Api-Key: <speech_token>`）；gpt/glm/xai：`POST {base}/audio/speech`；minimax：`POST {base}/t2a_v2`；gemini：`POST {base}/models/{model}:generateContent`（`responseModalities:["AUDIO"]`） | volcengine/gpt/glm/xai 仅 `{"model":<model>}`；minimax `{"model":<model>,"text":""}`；gemini `{"contents":[{"parts":[{"text":""}]}],"generationConfig":{"responseModalities":["AUDIO"]}}` |
| `audio_job_get` | `GET` 查询固定占位任务：qwen `GET {base}/api/v1/tasks/{id}`；其余 OpenAI 兼容任务面 `GET {base}/audio/jobs/{id}` | 无；绝不 POST `audio_job_create` |
| `audio_transcription_json` | 内置 10ms 静音 WAV 的真实转写请求（既有实现） | multipart WAV；无文本 prompt |

`kind=connectivity` 时服务端按账户场景选择代表模式：视频 `video_create`、图像 `images_json`、音频优先 `audio_speech`，其次 `audio_transcription_json`，仅长任务账户用 `audio_job_get`；向量 `embed_content`、重排 `rerank_json`、安全 `moderation_json`；不要求用户选模型/协议。

分类表（`video_create` / `images_json` / `audio_speech` / `embed_content` / `rerank_json` / `moderation_json` 的探测结果 evidence 带 `connectivity: true` 标记；`audio_transcription_json` 沿用既有 WAV 探针证据；`<code>` 为实际上游状态码）：

| 上游状态 | 判定 | 文案 |
| --- | --- | --- |
| 2xx | 通过（极少数上游对探测请求直接放行也接受） | 密钥连通性验证通过（上游 HTTP `<code>`） |
| 400 / 404（`video_create` / `audio_job_get`） | 通过——查询固定占位任务返回「任务不存在」属预期，证明密钥已通过认证 | 同上 |
| 400 / 422（`images_json` / `audio_speech`） | 通过——空 prompt/空文本被参数校验拒绝属预期，证明密钥已通过认证 | 同上 |
| 401 / 403 | 失败——密钥无效 | 密钥连通性验证失败：上游拒绝访问（HTTP `<code>`） |
| 5xx | 失败——上游不可达 | 密钥连通性验证失败：上游服务不可用（HTTP `<code>`） |
| 其余状态码 | 失败 | 密钥连通性验证失败（HTTP `<code>`） |

- `video_create` 绝不触碰创建端点（POST 创建会产生真实计费任务）；固定占位任务 ID `probe-connectivity-check` 的查询请求对任何账户都不产生产物。
- 失败文案附脱敏上游消息（有界预览，与既有响应体预览上限口径一致）；`diagnostics=limited` 时按既有规则统一脱敏为「上游请求失败」（额度规则命中显示「上游额度不足」）。
- 分级阶梯、多 Key 池诊断与 limited 脱敏沿用既有诊断链：仅「真实上游尝试后超时」按 20s→30s→40s 晋级；`images_json` 保持单次长预算档。
- 与 health check 的关系：媒体账户与对话账户共用 J1 状态机；J1 的媒体分支同样不写业务流量状态、不生成产物。人工测试仍只做诊断，不写账户状态/健康事实/Key 运行态；后台激活/周期/冷却恢复由 J1 独立写入。
- `audio_transcription_json` 可被选为音频账户的连通性代表模式，保持内置 WAV 样本的真实请求语义（极小样本、成本可接受）；`audio_speech` 原真实 TTS 请求形态自本裁决起不再被人工测试触发。
- 人工测试选项（test-options）同批放开：视频目录行（`video` 协议 + api_key 门）经 `video_create` 进入可测试候选（仅该形态）；音频目录行不再整体排除，按 `audio_speech` / `audio_transcription_json` 进入候选；图像行 `images_json` 分支契约不变。非对话账户统一返回单一“密钥连通性”选项，不展示模型/协议。
- 人工测试请求新增 `kind=connectivity`（`POST /{id}/test` 与 `POST /test-draft`）：非对话账户不要求 `model`/`testEndpointMode`，服务端按账户场景 + 健康检查模型/连接选代表模式；对话账户保持原 `model + testEndpointMode` 契约。
- gemini 原生 google_oauth 的 `ai_studio` 账户可按 `video_create` / `audio_speech` 进入媒体探测；`code_assist` / `google_one` 仍只允许 GenerateContent 探活，不开放媒体形态。
- 人工测试请求词表（`POST /{id}/test` 的 `testEndpointMode` 校验）同批增补 `video_create`；探针视图模式白名单同批保留连接声明的 `video_create` / `audio_speech`（探针侧此前会把两者过滤为空，导致媒体模式解析不到请求形态）。

### 17.1 B7 工具端点连通性探测（2026-11-12）

向量/重排/安全账户与媒体账户共用同一 `kind=connectivity` 入口、同一状态码分类和同一 J1 状态机；差异只在请求形态：

| 模式 | 连通性请求 | 请求体 |
| --- | --- | --- |
| `embed_content` | `POST` 向量端点：gpt `{base}/embeddings`；qwen `{base}/compatible-mode/v1/embeddings`（base 含 `/compatible-mode/v1`、`/api/v1`、`/v1` 时先剥到 origin） | `{"model":<health model>,"input":[]}`；空输入由上游参数校验拒绝，不产生真实向量结果 |
| `rerank_json` | `POST` qwen `{base}/api/v1/services/rerank/text-rerank/text-rerank`（base 归一规则同上） | `{"model":<health model>,"input":{"query":"","documents":[]}}`；空查询/文档由上游拒绝 |
| `moderation_json` | `POST` gpt `{base}/moderations` | `{"model":<health model>,"input":[]}`；空输入由上游拒绝或返回空结果，不产生真实审核影响 |

- 判读：2xx 或 400/422 视为 Key + Base URL 可达；401/403 为凭据失败；5xx/传输错误按既有分类。工具探测与媒体探测一样不写业务流量状态、不生成产物；`diagnostics=limited` 沿用统一脱敏。
- J1 候选白名单、写侧 `AccountHealthCheckEndpointModes` 与 DB CHECK 均含三值；创建后 `pending_test`，首次激活探测通过转 `active`，周期/冷却恢复/锁死 hold 语义与媒体账户一致。
- 工具账户人工测试只显示“密钥连通性”，不展示模型/协议选择；`test-options` 对工具场景返回空模型列表也不影响入口（服务端按健康检查模型选代表模式）。
