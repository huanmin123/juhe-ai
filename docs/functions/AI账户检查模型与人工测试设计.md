# AI 账户检查模型与人工测试设计

## 1. 目标

本文固定 AI 账户“检查模型”“人工测试”和“后台系统检查”之间的职责边界，作为新增、编辑、列表测试、账号健康检测、激活检查、运行态恢复、账号级冷却复测和 Key 级恢复的统一业务口径。

核心目标：

- 人工测试只作为用户主动发起的诊断工具；无论已保存账户、草稿、授权实例、模型或请求形态，测试结果都不恢复或改变持久调度资格，也不作为激活或健康事实凭证。
- 后台系统检查统一使用账户保存的检查模型，并独占账户激活、健康确认和自动恢复职责。
- 模型目录中的默认机制只负责初始化账户检查模型，不能在运行时动态改变已有账户。
- 列表随账户摘要返回检查模型和检查请求形态；打开测试弹窗不发起候选目录请求，请求形态下拉立即以本地推导的账户可测试形态集展示（账户检查形态优先选中）；候选目录及其模型级可执行请求形态在用户首次展开模型下拉时一次性加载并收敛形态列表。
- 删除账户批量测试，避免用户一次性制造大量不稳定上游请求和复杂任务状态。

## 测试方式分层（2026-11-21 定案）

账户测试只有两种方式，由能力域与（仅 chat 域持有的）账户声明 endpoint modes 自动判定，不由菜单、供应商或用户选择：

- **模型测试**：能力域为 `chat`，且账户声明了任一真实对话生成形态（`chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`）。只有模型测试显示「模型 + 测试请求形态」，并真实调用上游模型。
- **连接测试**：其余全部账户——图像 / 音频 / 视频 / 向量 / 重排五域账户（2026-11-21 五域退役起无上游接口能力声明，恒连接测试），以及只声明 `embeddings_json`、`rerank_json`、`embed_content`、`count_tokens`、`message_token_counting` 等非对话形态的 chat 账户。连接测试只验证 API Key、Base URL 与认证头可达，不调用模型、不生成图片 / 音频 / 视频 / 任务，也不存在「测试协议 / 测试请求形态」概念。

派生收口为 `capabilitydomain.TestKindForAccount(domain, modes)`，`GET /accounts/{id}/test-options` 顶层返回 `testKind: "model" | "connection"`；连接测试的候选列表恒为空（`data=[]`、`total=0`）。前端只按 `testKind` 渲染测试弹窗，不自行按菜单或供应商猜测测试方式。后台自动探针同口径拆分：模型测试账户由 J1 承载，连接测试账户由 jobs `account-connectivity-health` 连通性健康检查通道承载（§17）。

## 健康检查请求形态

- 模型测试账户必须保存不可空 `healthCheckEndpointMode`，数据库字段为 `health_check_endpoint_mode`。允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`；`images_json`、`audio_speech`、`audio_transcription_json`、`audio_job_create`、`video_create`、`realtime_session`、`embeddings_json`、`rerank_json` 等媒体 / 输入型形态保留为写侧落值白名单的**记录值**：连接测试账户（媒体域、embedding/rerank 域——2026-11-21 向量/重排拆分批起新域恒连接测试——与仅声明输入型形态的 chat 账户）保存时可由后端派生并落库，但连接测试执行不依赖它，J1 也不再按这些形态派发真实上游探针。`video_create` 是视频域默认落值形态（AI 账户能力域拆分 v7 定案），`embeddings_json` 与 `rerank_json` 分别是向量域与重排域的默认落值形态（2026-11-21 拆分批），`audio_job_create` 与 `realtime_session` 分别是长音频任务账户与实时会话账户的持久化诊断形态。创建与 PATCH 写侧对模型测试账户强校验该值属于上述 10 个生成形态；连接测试账户不受此约束。克隆遵循同一分层：连接测试源账户缺省三件套时按最终目标连接派生（源账户没有三件套也能克隆），模型测试源账户复制源三件套、源与请求均缺失按显式错误拒绝。
- 音频与语音（2026-11-01 裁决、2026-11-21 扩展）：`audio_speech`、`audio_transcription_json` 与全部音频任务形态都属于连接测试，不再由人工测试或 J1 触发真实 TTS / 转写请求。连接测试按连接器注册表的预设路径验证密钥与端点（§17），`audio_transcription_json` 不再要求内置 WAV 样本。
- J1 后台激活、周期健康、冷却恢复、质量确认、运行态恢复只处理模型测试账户的真实对话形态，直接使用账户保存的精确 mode，不再从协议族推导 JSON / SSE。连接测试账户不再进入 J1；图像、音频、视频、向量、重排五域账户与仅声明输入型形态的 chat 账户由 `account-connectivity-health` 按连接器注册表执行连通性探测，不产生付费生成。
- 新账户默认：GPT 官方 API Key / OAuth 使用 `responses_sse`；通用 OpenAI-compatible、DeepSeek、当前 GLM Chat 和 Gemini OpenAI profile 使用 `chat_sse`；Anthropic profile 使用 `messages_sse`；Gemini Native 使用 `generate_content_sse`。能力域拆分 M2 起，新建账户未显式提交 `health_check_endpoint_mode` 时按能力域默认落值（AI 账户能力域拆分 v7 定案）：对话域沿用上述现行默认，图像域 `images_json`、音频域 `audio_speech`、视频域 `video_create`。2026-11-21 起媒体 / 输入型账户的该字段只是记录值：前端不再要求用户选择，后端保存时按连接器注册表派生；连接测试执行不依赖该值，存量账户库中现值不变。
- **流式优先（2026-10-19 定案）**：检查形态默认值一律优先取流式形态。理由是真实流量以流式为主，非流式 2xx 可能被上游伪造或退化（实测样例：某中转对 `stream:false` 返回空 `choices` 的 SSE 帧仍回 HTTP 200），流式形态能更早暴露上游真实异常并复用同一份完成证据判定。该口径覆盖全部缺省生产面：前端建户表单、按能力域默认落值、OAuth 管理式建户、公开推送建户、导入适配器合成与导入缺省回退、人工测试形态顺序；显式提交的非流式形态仍然生效，存量账户现值不变。
- 首选 mode 未启用时的回落解析为双口径（2026-11-21 五域退役）：chat 域账户先取 `supported_endpoint_modes` 声明集中第一个已启用的流式 mode，再取第一个已启用 JSON mode，最后取第一个可检查 mode，没有任何可检查 mode 时拒绝保存，不做旧字段兼容或运行时协议猜测；五域账户无声明集，检查形态不做声明回落，按域默认形态集（`LaneModesForDomain`）× preset 规格优先级派生记录值（见 [AI账户上游接口能力设计](AI账户上游接口能力设计.md) §5）。
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

模型测试账户（能力域 `chat` 且声明真实对话生成形态）最终保存一个检查模型。账户表单编辑态与 API Key / OAuth 直接 Token 录入创建态必须选择非空 `supportedModels` 和其中的 `healthCheckModel`；chat 域托管 OAuth 的手动授权、Refresh Token（及 Grok SSO 导入）创建态不收集这两个字段，由后端 `CreateOAuthAccount` 派生（见 §4）。连接测试账户（图像 / 音频 / 视频 / 向量 / 重排五域账户，以及只声明 embeddings / rerank / count_tokens 等输入型形态的 chat 账户）只要求选择 `supportedModels`（路由需要），表单不暴露检查模型与检查请求形态；保存时后端按「首个支持模型 + 域默认记录形态 + 唯一连接 preset」派生并落库。表单不会自动上游目录发现，用户可手动刷新补全候选。点击测试或确认时，模型测试缺少任一模型只提示并中止；连接测试不要求模型。

- 模型测试的表单输入中的 `healthCheckModel` 不可为空，必须由用户从最终 `supportedModels` 中选择；不能依赖前端先请求模型目录或默认值补全。
- `healthCheckModel` 必须属于账户最终的 `supportedModels`；连接测试账户的派生检查模型同样必须属于支持模型。
- 模型测试的检查模型必须是当前账户协议可以验证的启用模型，按账户保存的精确 endpoint mode 发起最小生成请求。连接测试不调用模型，`images_json` / `audio_speech` / `video_create` 等值仅作为落库记录值。
- 删除支持模型时如果命中当前检查模型，必须先重新选择，不能静默切换到其他模型；连接测试账户由后端重新派生。
- 目录发现后如果当前检查模型不在上游，按本地目录 `releaseDate` 倒序、同日 `catalogOrder` 升序选择下一候选；没有候选时记录“检查模型配置异常”并停止本轮探针。

只有模型测试账户向用户暴露必填的“健康检查请求形态”。普通生成模型只能选择账户已经启用的 JSON / Streaming mode；保存值就是后台探针最终使用值，人工测试仍可为单次诊断临时选择其他已启用 mode。连接测试账户不显示该字段，也不存在测试协议选择。

连接器注册表为每个 preset × endpoint mode 提供连通性探测规格；图像 `POST /images/generations`（仅 `model`，空 prompt）、语音按 preset 的 TTS / 任务查询模板、视频按 preset 的真实任务查询模板，绝不触碰创建端点。详见 §17。

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

- 模型测试（§“测试方式分层”）选择模型与测试请求形态，真实调用上游；连接测试（五域账户与仅声明输入型形态的 chat 账户）不显示模型 / 请求形态选择，只验证 API Key、Base URL 与认证头可达，不生成任何产物。
- 连接测试同样不写账户状态、健康事实、冷却或 Key 运行态；其判定与失败分类见 §17。

状态不参与（模型测试与连接测试同口径）：

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

- 连接测试账户（testKind=connection）不要求表单 `healthCheckModel`，测试弹窗不加载模型候选、不显示请求形态，直接以草稿凭据 / Base URL / 代理发起连通性探测。
- 模型测试账户的测试模型默认使用当前表单的 `healthCheckModel`，用户可在测试弹窗内为本次测试自由切换供应商目录内的其他候选模型；候选来源与 5.2 相同（账户供应商、协议档案和当前所有者作用域可见的启用可测试模型目录），按草稿表单当前配置计算，经草稿测试选项接口（`POST /accounts/test-draft-options`、`POST /my-accounts/test-draft-options`，body 携带草稿账户载荷）获取。
- 测试模型与表单“检查模型”配置解耦：本次测试选择不回写表单；表单 `healthCheckModel` 仍必须属于当前表单 `supportedModels`（保存契约不变）。
- 后端以测试请求携带的模型为准（未携带时回落表单检查模型），按供应商目录能力校验该模型与所选请求形态；测试表单支持列表之外的模型时不做支持列表成员校验（与 5.2 列表测试同语义）。
- 刚加入表单的支持模型可以作为新的检查模型立即测试，但只有用户保存表单后才成为账户正式配置。
- 测试结果不自动保存或直接恢复已保存账户。协议成功后，当前未保存草稿不是 `disabled` 时，页面自动预选“可调度”；用户仍需点击保存，且可在保存前改回“待检查”或“停用”。

### 5.2 列表操作测试

列表“测试”是面向用户的诊断工具，不属于账户健康检查，按 `testKind` 呈现两种方式：

- 连接测试账户（testKind=connection）：不加载模型候选，测试弹窗只显示「连接测试」；直接按连接器注册表探测凭证与上游可达性，结果不要求模型或请求形态。
- 模型测试账户：按下述模型候选与请求形态规则执行自由模型诊断。
- 模型候选来自该账户供应商、协议档案和当前账户所有者作用域可见的启用可测试模型目录；普通生成模型和图像模型都必须匹配其实际请求协议能力。
- 能力域拆分 v8-C 对话协议面排除：目录 mode ∈ {`image_generation`, `video`} 的行不得经对话协议族（chat_completions/responses/messages/generate_content/interactions）进入对话测试候选或获得对话测试形态（mode=`audio` 行整体排除维持既有契约）；图像行只经 images_json 测试面的图像分支（`images` 协议 + api_key 门）保持可测，且测试形态只余 `images_json`。据此对话账户的测试下拉不再把生图/视频模型当作对话协议测试目标；`images_json` 测试面的图像模型分支不受影响（图像域账户继续经它选择图像模型）。**2026-11-01 媒体域连通性裁决起**，视频行（`video` 协议 + api_key 门）经 `video_create` 连通性测试形态进入候选（仅该形态，不进对话族；gemini 原生 google_oauth 视频账户不在放开面，见 §17）。
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

账户列表返回展示、筛选和测试弹窗初始化所需的摘要字段，包括账户自己的 `healthCheckModel`、`healthCheckEndpointMode` 与服务端派生的 `testKind`；不为了测试提前加载完整支持模型、候选模型目录、endpoint modes 数组或凭据。`testKind` 由「有效连接」的 `account_endpoint_modes` 派生——资源账户取 `COALESCE(source_accounts.id, accounts.id)`（授权实例投影来源账户），连接取 `health_check_connection_id` 指向的 enabled 连接优先、否则 enabled 按 id ASC 首个，与运行时/手工测试同源；无连接行的老数据回落 `health_check_endpoint_mode` 标量，两者皆空按空集判 `connection`。回落臂已知差异（2026-11-21 独立复审登记）：列表回落读实例自身冻结标量（M10 展示语义），`test-options` 手工测试上下文回落 `COALESCE(source_accounts.*, accounts.*)` 来源投影——仅影响「无连接形态老数据 + 授权实例 + 来源标量盖章后已变」的窄存量角，两处可判不同 testKind，最终提交以 `test-options` 为准；如需统一回落来源另行裁决。前端首开以列表 `testKind` 为准（缺省时才按能力域与检查形态本地初判），服务端 `test-options` 的 `testKind` 仍为提交前的最终校验；混合形态 chat 账户的初判歧义已由「写侧禁止模型测试账户记录输入型检查形态 + 列表服务端派生」消除，历史非法行以更新保存或 `test-options` 纠正。

`test-options` 的当前落地范围是 Go 网关 `gateway` 管理面 / 公开面与 Vue 前端；Node 后端已于 2026-09-05 清零，不以 Node 契约为验收依据。

模型测试账户的候选目录在用户首次展开模型下拉时一次性加载（2026-07-30 起，打开弹窗本身零 options 请求）；打开弹窗时请求形态下拉先以本地按协议档案推导的账户可测试形态集初始化——账户检查形态优先选中，其后流式在前、JSON 在后，与 options 返回的 `testEndpointModes` 同序——options 加载完成后按服务端返回的模型级 `testEndpointModes` 收敛并保持当前选中（不可用时取首项）。测试按钮在可用模型与请求形态齐备前保持禁用（已保存账户带检查模型时首开即以检查模型与推导形态可提交），后续搜索继续复用同一接口。连接测试账户不展开模型下拉，`test-options` 直接返回 `testKind=connection` 且 `data=[]`、`total=0`：

```text
GET /__aisys__/api/accounts/:id/test-options
GET /__aisys__/api/my-accounts/:id/test-options
```

模型测试响应 `data` 为 `Array<{ id, name, testEndpointModes }>`，顶层携带 `testKind`、`total`、`hasMore`，支持 `keyword`、`limit` 和 `selectedIds`。每个 `testEndpointModes` 都由 Go 网关使用当前账户已启用 endpoint modes、该模型的 `supportedApiProtocols`、以及当前账户模型映射和映射上游模型协议计算最终可执行交集。HTTP 响应不包含凭据、原始 `supportedApiProtocols` 或完整模型定义。连接测试响应不返回任何模型候选。

用户在模型下拉中切换模型后，Vue 直接采用该 options 项的 `testEndpointModes`，并自动选择首个可用形态；加载 options 时若自动选择首个模型也同步形态，不再向浏览器发起第二个 capabilities 请求。`testEndpointModes` 的顺序固定为「账户保存的 `healthCheckEndpointMode` 优先，其后流式形态在前、JSON 形态在后」——同一协议内 `chat_sse` / `responses_sse` / `messages_sse` / `generate_content_sse` / `interactions_sse` 先于对应的 `_json` 形态。后端可保留 `GET /__aisys__/api/accounts/:id/test-options/models/:modelId` 及个人端镜像作为兼容能力接口，但页面不调用它。测试执行仍由后端按账户 ID 读取受控凭据，并在提交时通过 `resolveAccountManualTestSelectionAsync` 重新校验模型与请求形态。

合法跨协议映射按来源协议选择检查形态、按映射目标协议校验上游模型，不能用目标协议直接裁掉来源检查形态。账户目录同步不属于检查形态，不参与自动测试或健康结果；其 `requestUrl` 只说明该次同步的受控目录路径。

后台检查和人工测试不能仅凭 HTTP 2xx 判定成功。模型测试的 JSON 响应必须包含对应协议的正常完成对象，Streaming 响应必须包含对应协议的完成事件，图片响应必须满足图片协议成功证据。空正文、仅 `[DONE]`、HTML、畸形 JSON 或只有未完成数据片段都不能激活 `pending_test` 账户；framing 完整时归为 `upstream_http_failure`。人工测试只显示诊断，激活、周期健康、请求失败二次确认和质量确认则把它作为通用可用性失败。上游目录是否存在不参与该判断。连接测试（全部非对话能力账户）不要求协议完成证据：2xx 或注册表规格的预期拒绝码即通过，401/403 恒失败（§17）。

	普通文本生成检查还必须为每次实际请求生成独立的随机算术挑战：报文为「接口连通性自动测试：请计算 A+B，只回复数字」，A、B 各取 10～89，期望答案是两数之和的十进制文本。2026-10-15 起停用历史的「请原样输出固定令牌」形态——上游内容审核会把这类报文整段判为 HTTP 403，把正常账户打成临时不可调用；算术题要求模型真实作答，实测不被拦截。可见输出包含该答案即匹配（答案两侧不得是数字，避免命中更长数字的片段）；reasoning、thinking 和工具内容不属于可见输出。连接测试不发算术挑战、不校验模型输出。

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
| 非对话账户连通性健康检查（jobs `account-connectivity-health`） | 连接器注册表连通性规格（不读检查模型） | `pending_test` 或由连通性 401/403 造成的 `error` 在成功后回到 `active`；401/403 写 `error` 且不写冷却；网络 / 5xx / 其他 4xx 只记录健康事实，不改调度状态（§17） |

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
| `error` | 人工“异常恢复”只原子重置为 `pending_test` 并立即投递激活检查，不能直接恢复为 `active`（媒体域账户除外：2026-10-14 媒体域状态机下 `error` 人工清除直接恢复 `active`，不经 `pending_test` 与后台激活检查——见《核心功能设计》媒体域账户状态机） |
| `disabled` | 不主动探测，尊重人工停用 |

人工测试默认只诊断，不能直接成为任何已保存账户的状态恢复入口；新增或编辑草稿协议成功后仅在浏览器本地预选“可调度”，不写库、不清理运行态，必须由用户保存后才生效。

自有账户在所有非 `disabled` 状态都允许人工停用，包括 `pending_test` 和 `error`；授权实例继续遵守授权侧状态管理边界。

## 14. 接口与存储契约

账户创建、更新、详情、导入和导出统一使用 `healthCheckModel`。

供应商列表 / 详情返回只读 `defaultHealthCheckModel`（由模型目录按最新发布时间且可文本生成的模型派生），不提供设置供应商默认检查模型的写接口。账户不再提供独立“保存默认测试模型”接口；修改账户检查模型只能通过账户编辑保存接口完成。

人工测试接口：

- `GET /accounts/{id}/test-options`（含 `/my-accounts` 镜像）顶层返回 `testKind: "model" | "connection"`。连接测试恒返回 `data=[]`、`total=0`，不再因账户存在同供应商对话目录行等无关目录组合返回 500。
- `POST /accounts/{id}/test`、`POST /accounts/test-draft`：连接测试不要求 `model` / `testEndpointMode`，任务行 `model`、`test_endpoint_mode` 写 NULL（列 nullable），结果 JSON 带 `testKind`；模型测试沿用原选择与校验规则。
- 草稿测试请求与快照携带 `connectionPresetCode`（前端由表单协议档案映射，缺失时后端按 vendor 默认 preset 兜底）；快照同时携带凭据声明形态与支持模型。媒体与输入型 chat 草稿可只凭连接配置发起连接测试，草稿校验不要求检查模型/形态，由快照准备阶段按最终声明集派生。草稿测试选项接口（`POST /accounts/test-draft-options`、`POST /my-accounts/test-draft-options`）同样返回 `testKind`，连接测试为空候选。
- 新增 / 编辑表单的模型测试默认使用草稿 `healthCheckModel`；请求携带顶层 `model` 时以请求模型为准（按供应商目录与请求形态能力校验，与 5.1 契约一致），不回写表单。
- 模型测试的列表测试必须显式提交本次 `model`，并校验模型属于当前账户供应商、协议档案和所有者作用域可见的启用可测试模型目录；不要求属于账户 `supportedModels`。图像生成模型必须使用实际图片 endpoint，不能因为账户保存了 `responses_sse` 就发送文本 Responses 请求，也不能回退为模型目录探针。
- 连接测试按连接器注册表的 preset × endpoint mode 规格执行（§17），不接受客户端自定义路径或请求体；同一账户多 Key 时逐个 Key 探测，任一 Key 成功即取消尚未开始的请求。
- 多 API Key 的模型测试复用固定 Key 诊断器：每账户最多 `3` 个并发，真实上游超时才按 `20s -> 30s -> 40s` 阶梯重测该 Key，任一 Key 成功即取消尚未开始的请求。成功前完成的失败 Key 和成功 Key组成实际结果，已取消或未开始的 Key 明确标记为未测试；人工测试不把“未测试”解释为通过或失败，也不写入任何 Key 运行态。模型目录同步仍必须逐 Key 获取并取交集，不能使用首成功短路。
- 测试任务结果不能被后端账户保存、状态恢复或后台检查任务当作可信状态凭证；新增和编辑草稿协议成功只允许浏览器预选本地状态单选，不能直接写库。

## 15. 验证要求

- 新增、编辑、草稿、已保存自有账户和授权实例的人工测试成功或失败后，账户、授权实例、Key、健康事实和运行态均不变化；新增或编辑草稿测试协议成功仅将非 `disabled` 的未保存表单预选为 `active`，保存前不写库。
- 测试方式派生：五域账户与仅声明 embeddings / rerank / count_tokens 等形态的 chat 账户恒为 `connection`；含任一真实对话生成形态的 chat 账户为 `model`。
- 连接测试失败分类：401/403 永不判通过；2xx 或注册表预期拒绝码判通过；5xx / 网络 / 其他状态码判失败；不产生任何生成产物。
- 新增 / 编辑模型测试默认使用表单检查模型，可切换目录候选且不回写表单；列表测试关闭重开后仍默认使用账户检查模型。
- A 账户测试运行时可以打开和启动 B 账户测试，旧结果不能覆盖新弹窗。
- 列表接口返回检查模型、检查请求形态两个轻量标量与派生 `testKind`（按有效连接声明形态计算，手工测试上下文同源）；打开测试弹窗不读取 `test-options`，模型测试的请求形态下拉先以本地推导的账户可测试形态集初始化（检查形态优先选中），首次展开模型下拉时一次性读取 `{ id, name, testEndpointModes }` 的批量 `test-options` 并联动收敛形态，无检查模型且目录未加载或加载失败时禁止提交，切换模型立即联动该项请求形态。options 响应 envelope 的 `data` 保持数组，顶层携带 `testKind`、`total`（过滤去重后、limit 截断前候选总数）与 `hasMore`（T23），前端据此在下拉底部展示截断提示；草稿测试的 `test-draft-options` 同形。
- 页面和后端均不存在账户批量测试入口。
- 新账户保存后保持 `pending_test`，后台激活检查成功后自动进入 `active`。
- 人工草稿测试成功不能直接激活新账户；它只会预选表单“可调度”，用户保存后才按该显式选择创建账户。
- 定时健康检查不存在独立健康检查开关，也不存在 `health_check_enabled` 候选条件；账户级「后台自动探测」总开关是唯一关闭入口（[AI 账户后台自动探测总开关设计](AI账户后台自动探测总开关设计.md)）。`temporaryUnavailableContinuousProbeEnabled` 只由冷却复测读取，不能作为定时健康检查候选条件。
- 所有系统探针严格使用账户 `healthCheckModel`，缺失或非法时不回退其他模型。
- 修改模型目录默认检查模型只影响后续新账户初始化，不改变已有账户。
- 多 Key 人工测试不写 Key 状态，后台激活和 Key 恢复探针可以按职责写入 Key 状态。
- 模型级失败不把账户整体标记为不可用，账户级故障达到策略条件后才进入保护状态。
- 能力域拆分后（v7 定案）：新建媒体账户 `health_check_endpoint_mode` 按域默认落值，媒体域新建默认「可调度」，导入的媒体账户保持 `pending_test` 并在预览提示手动启用；存量账户全部域的激活、周期与恢复检查行为零变化。2026-10-14 媒体域状态机定案后，滞留 `pending_test` 的视频域账户还可经候选窗口媒体准入臂被视频创建请求选中，视频任务成功后 CAS 回写 `active`（手动启用不再是唯一恢复路径）；恢复准入仅限 `video_create` 检查模式账户，同步 TTS 与长音频转写不启用恢复准入。

## 16. 媒体与输入型账户检查边界（能力域拆分 v7 定案，2026-11-21 扩展）

来源：AI 账户能力域拆分计划 v7 定案（2026-10-12，账户能力域 `chat` / `video` / `audio` / `image`），2026-11-21 按测试方式分层扩展。本节固定五域账户与仅声明输入型形态的 chat 账户在检查体系中的参与范围。

- **按域默认落值**：新建账户 `health_check_endpoint_mode` 未显式提交时按域默认落值——对话 `chat_sse`（流式优先，2026-10-19 定案；原 `chat_json`）、图像 `images_json`、音频 `audio_speech`、视频 `video_create`、向量 `embeddings_json`、重排 `rerank_json`（后两域 2026-11-21 向量/重排拆分批起新增，恒连接测试）。2026-11-21 起该值对连接测试账户只是记录值，前端不要求用户选择，后端按「域默认 + 连接器注册表连通性规格」派生；仅影响新建账户，存量账户库中现值一个不改。
- **后台周期检查（J1）参与范围**：J1 只承载模型测试账户的 10 个真实对话生成形态（`chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`）。图像 `images_json` 已从 J1 候选白名单移除，音频 / 视频 / 实时会话 / 向量 / 重排等形态不再进入 J1；非对话账户统一由 jobs `account-connectivity-health` 连通性通道承载（§17）。2026-11-21 起 `volcengine_ark` 与 `qwen_dashscope` 的 chat 形态（api_key）准入 J1：探针协议复用 OpenAI v1（volcengine base 直拼 `/chat/completions`；qwen base 恒追加 `/compatible-mode/v1`，与网关出站归一逐字一致），preset→profile 走 jobs 侧 J1 专用映射（`profile_volcengine_openai_v1` / `profile_qwen_openai_v1`），共享 `ConnectionPresetProfileID` 不扩面，J3a 代理延迟与代理档案 target 不变；授权实例按来源账户的 `health_check_connection` 投影参与，J1 基线播种的连接 EXISTS 同步按资源账户判定。qwen/volcengine 的 J1 探针会真实调用上游并计费，属既有 J1 语义。
- **激活检查（pending_test 首检）**：媒体与输入型账户不再依赖 J1 激活检查；开启后台自动探测时，新建 / 导入账户落 `pending_test`，由连通性健康检查成功后的状态 CAS 推进为 `active`（§17）。手动模式（总开关关闭）创建缺省仍按总开关设计落 `active`（真实流量即验收），不产生 `pending_test`。
- **模型检测（J3b）参与范围**：按账户 endpoint mode 与 profile 协议兼容性判定参与资格，媒体形态天然不满足对话族候选条件，媒体域账户不参与模型检测（详见《模型检测设计》）。
- **检查失败对派发的影响（现状事实，零改动）**：媒体同步 lane 受候选窗健康门（status / schedulable / cooldown + endpoint mode 门）；任务面（videos / audio jobs 查询、下载）账户亲和直连不查健康。
- **存量账户行为变化**：2026-11-21 起存量五域账户与仅声明输入型形态的 chat 账户开始进入连通性健康检查（此前不进入 J1，等于无后台验证）；这是本设计的明确行为变化，回滚二进制后这些账户的健康事实列保留但不更新。

## 17. 非对话能力账户连通性探测（2026-11-01 用户裁决，2026-11-21 扩展）

媒体域账户（视频 / 图像 / 语音）、embedding / rerank 独立域账户（2026-11-21 向量/重排拆分批起，两域恒连接测试）与 chat 域非对话能力账户（只声明 `embed_content` / `count_tokens` / `message_token_counting` 等输入型形态）的人工测试与后台健康检查统一使用**密钥连通性探测**：只验证 API Key 能否访问上游，不调用模型、不真实生成图片 / 音频 / 视频 / 任务。

### 17.1 连接器注册表规格

请求构造收口 `shared/contracts` 连接器注册表 `ConnectorConnectivityProbe`，由 `accountprobe` 统一执行；规格按 preset × endpoint mode 登记，账户已声明形态中第一个命中生效。路径必须来自该 preset 生产适配器已证实的上游路径（注册表 operation 模板为优先来源；ark 图像端点 `POST /images/generations` 已登记进 ark 操作模板，火山 OpenSpeech 固定语音域的 `/api/v3` 版本前缀是唯一保留的服务根前缀例外），禁止跨供应商硬编码；任务查询使用固定占位任务 ID `probe-connectivity-check`，内容/取消形态同样复用不存在的占位任务查询，**绝不 POST 创建端点或取消真实任务**。`ConnectivityProbeCoverage` 回归遍历 registry 全部允许组合，要求每个可声明的连接测试形态都有规格；`probe 路径 ∈ 操作模板 或 已登记前缀例外`由回归自动断言（例外必须被实际使用，防止清单腐化）。

| preset 族 | 代表规格（endpoint mode → 请求） | 请求体 | 预期拒绝码 |
| --- | --- | --- | --- |
| OpenAI 兼容（gpt / xai / deepseek / glm / 通用 openai_v1） | `images_json` → `POST /images/generations`；`audio_speech` → `POST /audio/speech`；`video_create` / `video_get` → `GET /videos/{probe_id}`；`audio_job_*` → `GET /audio/jobs/{probe_id}`；`audio_transcription_json` → `POST /audio/transcriptions`；`realtime_session` → `GET /realtime`；`embeddings_json` → `POST /embeddings` | `model_only` / `empty_json` / 无 | 400 / 404 / 415 / 422 / 426（按形态） |
| Anthropic 兼容 | `message_token_counting` → `POST /messages/count_tokens` | `empty_json` | 400 / 422 |
| Gemini 原生 | `video_*` → `GET /interactions/{probe_id}`；`audio_speech` → `POST /models/{model}:generateContent`；`embed_content` → `POST /models/{model}:embedContent`；`count_tokens` → `POST /models/{model}:countTokens` | `empty_json` | 400 / 422 |
| MiniMax | `video_*` → `GET /query/video_generation?task_id={probe_id}`；`audio_speech` → `POST /t2a_v2` | 无 / `model_only` | 400 / 404 / 422 |
| volcengine ark | `video_*` → `GET /contents/generations/tasks/{probe_id}`；`images_json` → `POST /images/generations` | 无 / `model_only` | 400 / 404 / 422 |
| volcengine openspeech | 固定语音域 `https://openspeech.bytedance.com`；`audio_speech` → `POST /api/v3/tts/create`；`audio_transcription_json` / `audio_job_*`（含 content/cancel）→ `POST /api/v3/auc/bigmodel/query`；认证头 `X-Api-Key: <speech_token>`，AUC 另带 `X-Api-Resource-Id: volc.seedasr.auc`、`X-Api-Request-Id: <每次请求生成的 UUID>`、`X-Api-Sequence: -1` | `model_only` / `empty_json` | 400 / 404 / 422 |
| qwen | `video_*` / `audio_job_*` / `audio_transcription_json` → `GET /api/v1/tasks/{probe_id}`；`embeddings_json` → `POST /compatible-mode/v1/embeddings`；`rerank_json` → `POST /api/v1/services/rerank/text-rerank/text-rerank` | 无 / `model_only` | 400 / 404 / 422 |

- 规格的 `ExpectedRejectionStatus` 永远不得包含 401 / 403；执行层即使收到被错误登记的这两个码也会强制按失败处理。
- 出站沿用既有通道：账户 base_url 版本根、账户凭据认证策略与伴随头、账户代理、系统客户端身份头、SSRF 守卫、分级超时与脱敏。
- 认证头可被规格覆盖（openspeech 的 `speech_token`）；凭据缺失时本地显式失败，不回退其他凭据键。
- 认证之外的协议必需头由规格 `ExtraHeaders` 声明并支持 `{probe_id}` / `{model}` 占位（火山 AUC 的资源/请求/序号三件套）；缺少这些头时上游可能返回 200 + 业务错误，连接测试会误判为连通。

### 17.2 结果分类

探测结果 evidence 带 `connectivity: true`；`<code>` 为实际上游状态码：

| 上游状态 | 判定 | 文案 |
| --- | --- | --- |
| 2xx | 通过（极少数上游对探测请求直接放行也接受） | 密钥连通性验证通过（上游 HTTP `<code>`） |
| 规格登记的预期拒绝码（400 / 404 / 415 / 422 / 426） | 通过——请求不完整或占位任务不存在属预期，证明认证已通过 | 同上 |
| 401 / 403 | 失败——密钥无效 | 密钥连通性验证失败：上游拒绝访问（HTTP `<code>`） |
| 5xx | 失败——上游不可达 | 密钥连通性验证失败：上游服务不可用（HTTP `<code>`） |
| 其余状态码 | 失败 | 密钥连通性验证失败（HTTP `<code>`） |

- 失败文案附脱敏上游消息（有界预览）；`diagnostics=limited` 时按既有规则统一脱敏。
- 分级阶梯、多 Key 池诊断与 limited 脱敏沿用既有诊断链；连接测试不使用 `images_json` 的单次长预算档。
- 图像 `/images/generations`、语音 `/audio/speech`、转写与向量规格属于同步推理端点，只发送 `model_only` / `empty_json` 最小请求，依赖上游参数校验拒绝（400/422/415）。若上游对最小请求直接放行并产生生成/计费，当前实现仍按 2xx 判连通（2026-11-22 用户裁决：接受现状，不再登记跟踪）。

### 17.3 人工测试

- `test-options` 返回 `testKind=connection` 与空候选；测试弹窗不显示模型 / 请求形态选择，不出现「测试协议」概念。已保存账户（含授权实例）的测试方式统一按「有效连接声明形态」派生，与列表 `testKind` 同源；无连接行的老数据回落 `credentials.supported_endpoint_modes`。
- `POST /accounts/{id}/test`、`POST /accounts/test-draft` 允许不带 `model` / `testEndpointMode`；任务行对应列写 NULL。连接测试草稿必须携带 `connectionPresetCode`（前端从表单协议档案映射；缺失时后端按 vendor 默认 preset 兜底，volcengine OpenSpeech/Ark 共用 vendor 的档案仍需显式提交）。
- 连接测试草稿不要求 `healthCheckModel` / `healthCheckEndpointMode`，快照准备阶段按最终凭据声明集派生（chat 域；五域账户无声明，按域默认记录形态派生）；模型测试草稿仍要求两者。探测规格缺失、连接 preset 缺失等配置错误落失败任务并保留原始文案，不得标记为“已取消”（只有调用方上下文取消走取消语义）。
- 人工连接测试不写任何账户状态、健康事实或 Key 运行态（§5 契约零变化）。

### 17.4 后台连通性健康检查（jobs `account-connectivity-health`）

独立 jobs 任务，与 J1 模型检查通道并行，不共享候选 / 状态 / 预算：

- **候选**：`background_probe_enabled=1`；`status ∈ {active, pending_test, error}`；时间计划闸门允许；未过期；`next_health_check_at` 到期（`pending_test` 且从未检查过不受到期限制）；能力域为 `video` / `audio` / `image` / `embedding` / `rerank`（2026-11-21 向量/重排拆分批起新域入候选集合），或 `chat` 域声明了连接且不含 10 个真实对话生成形态。授权实例按资源账户判定连接归属（`CASE WHEN authorization_instance_authorization_id IS NULL THEN id ELSE authorization_instance_source_account_id END`），实例自身没有连接行也能按来源连接入选。
- **执行**：`proberepo` 装载账户视图（授权实例的凭据 / type / vendor / oauth_type / Key 运行态按来源账户投影，实例行 `"{}"` 占位凭据不再导致候选被跳过），`accountprobe.ManualConnectivityDiagnostics` 按注册表规格探测。**预算（2026-11-22 用户裁决）**：连接测试使用专用三级超时阶梯 `[10s, 20s, 30s]`（仅「真实上游尝试后超时」晋级下一级，单 Key 最坏 60s）——连接测试只是 API Key 级可达性验证，10s 级应有结果，不再回落对话诊断的 `[20s, 30s, 40s]`，也不使用 images_json 单次长预算档；runner 层单账户硬顶 `probeTimeout = 60s`（覆盖单 Key 阶梯总和；多 Key 池超出部分照旧在硬顶截断并按瞬态处理）。**并发（2026-11-22 用户裁决，同日三裁定稿）**：总并发 ≤ 512（与 J1/`ProbeConcurrency` 默认 512 对齐）、批量 ≤ 512（独立于并发的 timeout 算式钳制），并按上游分组限流——同一 `base_url` origin（`scheme://host:port`）并发 ≤ 60（同上游账户通常不多，避免波次严重串行），不同上游之间并行。最坏一轮（512 候选全同上游）= ceil(512/60) = 9 波 × 60s = 540s（叠加每波写库 ≈ 567s），小于 job 调度 15 分钟 timeout（`LeaseTTL` 同步 15 分钟）；跨上游场景峰值并发 512/轮，吞吐 512/分钟。人工连接测试（§17.3）与后台同链路同预算（单 Key 最坏 60s；多 Key 池账户人工路径无 runner 层硬顶，按 Key 串行累加）。
- **成功**：写 `last_health_check_at` / `last_health_success_at`，清零失败计数；`pending_test` 或由连通性 401/403 造成的 `error` 原子恢复 `status='active'`、`schedulable=1`、清冷却与最近错误。下一次检查按 `accountHealthCheckIntervalHours`（默认 24h）+ `accountHealthCheckJitterMinutes` 安排。
- **401 / 403**：写 `status='error'`、`schedulable=0`、`last_error_code='connectivity_auth_failed'` 与脱敏摘要；不写冷却（认证失效不是瞬态，不应进入冷却复测）。
- **网络 / 5xx / 其他非认证失败**：只记录 `last_health_check_*` 与失败计数，不改 `status` / `schedulable` / 冷却，固定按最多 5 分钟复测（不再叠加 jitter，避免把 5 分钟上限放大到抖动设置上限），避免把上游瞬态误判为账户异常。
- **无可用探测规格**：写 `last_health_check_error_code='connectivity_probe_spec_missing'` 并推进 `next_health_check_at`，不改调度状态；避免 `pending_test` 每 tick 被重复选中、批量无规格账户占满候选批次造成饥饿。规格补齐后按正常周期重新评估。
- **CAS**：状态写入带 `id + status + config_revision + dispatch_revision + deleted_at IS NULL` 守卫；探测期间配置变更或状态迁移时结果丢弃（stale），不覆盖用户编辑。写入成功后 bump 账户状态版本，让 gateway 运行态缓存失效。
- **总开关**：手动模式账户不进入候选；关闭总开关时既有过程态按总开关设计归一化。

### 17.5 与媒体状态机的关系

- 视频创建请求的媒体准入臂仍可选中滞留 `pending_test` 的 `video_create` 账户，任务成功 CAS 回写 `active`；连通性通道提供不依赖真实生成任务的通用激活 / 恢复路径。
- 同步 TTS、长音频转写与图像账户不启用媒体任务恢复准入，激活 / 恢复只由连通性健康检查或人工操作完成。
- `audio_transcription_json` 自 2026-11-21 起属于连接测试，不再发送内置 WAV 样本；原真实 TTS 请求形态仅作为探针包内历史构造保留，不进入人工测试或 J1。
