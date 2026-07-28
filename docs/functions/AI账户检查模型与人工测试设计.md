# AI 账户检查模型与人工测试设计

> 多模型账户的完整目标以 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 为准：`healthCheckModel` 只定义 active 周期哨兵，并在 `pending_test` 的 Catalog activation selection 中作为候选优先级；它不再是激活唯一模型。请求失败确认与模型能力恢复必须使用失败请求映射后的精确上游模型和 endpoint mode，单模型结论不得直接扩散为整号状态。

## 1. 目标

本文固定 AI 账户“检查模型”“人工测试”和“后台系统检查”之间的职责边界，作为新增、编辑、列表测试、账号健康检测、激活检查、运行态恢复、账号级冷却复测和 Key 级恢复的统一业务口径。

核心目标：

- 人工测试只作为用户主动发起的诊断工具，不再承担账户激活、状态恢复、健康事实维护或默认模型保存职责。
- 后台系统检查统一使用账户保存的检查模型，并独占账户激活、健康确认和自动恢复职责。
- 模型目录中的默认机制只负责初始化账户检查模型，不能在运行时动态改变已有账户。
- 列表随账户摘要返回检查模型和检查请求形态；打开测试弹窗不加载候选目录，用户展开模型或请求形态下拉时再按对应范围加载。
- 删除账户批量测试，避免用户一次性制造大量不稳定上游请求和复杂任务状态。

## 健康检查请求形态

- 当前账户必须保存不可空 `healthCheckEndpointMode`，数据库字段为 `health_check_endpoint_mode`。允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`。
- 后台激活、周期健康、冷却恢复、质量确认、运行态恢复和账户默认测试直接使用账户保存的精确 mode，不再从协议族推导 JSON / SSE。
- 新账户默认：GPT 官方 API Key / OAuth 使用 `responses_sse`；通用 OpenAI-compatible、DeepSeek、GLM 和 Gemini OpenAI profile 使用 `chat_json`；Anthropic profile 使用 `messages_json`；Gemini Native 使用 `generate_content_json`。
- 首选 mode 未启用时，优先取 `supported_endpoint_modes` 中第一个已启用 JSON mode，再取第一个可检查 mode；没有任何可检查 mode 时拒绝保存，不做旧字段兼容或运行时协议猜测。
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
- 供应商个人默认字段：`defaultHealthCheckModel`
- 供应商个人默认表：`provider_default_health_check_models`
- 供应商管理员系统默认表：`provider_system_default_health_check_models`
- 人工测试请求字段：`model`，只表达本次测试输入

删除旧语义：

- 删除账户 `defaultTestModel / default_test_model`。
- 删除供应商 `defaultTestModel / default_test_model` 命名。
- 删除 `provider_default_test_models`。
- 删除 `healthCheckEnabled / health_check_enabled`。系统检查是基础机制，不提供账户级关闭开关。账户高级配置中的“持续恢复探活”不是系统检查开关：它只决定 `temporary_unavailable` 在前 10 分钟有界最终确认后是否继续长期低频恢复，周期健康检查、首次激活、人工测试、`rate_limited` 和账户内 Key 复测始终不受影响。

项目不在运行路径保留旧字段兼容、双读双写或自动迁移。既有数据需要处理时由上线流程执行一次性离线字段同步。

## 3. 检查模型

每个已保存且参与后台检查的 AI 账户最终保存一个检查模型；创建表单可以暂时不选择，由成功的上游目录发现自动推荐：

- 表单输入中的 `healthCheckModel` 可为空；保存前若目录发现提供了有效推荐，后端持久化该推荐值。
- 必须属于账户最终的 `supportedModels`。
- 必须是当前账户协议可以验证的启用模型。普通文本生成模型按账户保存的精确 endpoint mode 发起最小生成请求；OpenAI v1 档案中的 `mode=image_generation` 模型改用上游 `GET /v1/models` 目录探针，并要求标准 `object=list` 响应中存在精确模型 ID。
- 删除支持模型时如果命中当前检查模型，必须先重新选择，不能静默切换到其他模型。
- 目录发现后如果当前检查模型不在上游，按本地目录 `releaseDate` 倒序、同日 `catalogOrder` 升序选择下一候选；没有候选时记录“检查模型配置异常”并停止本轮探针。

账户向用户暴露必填的“健康检查请求形态”，只能选择账户已经启用的 JSON / Streaming mode。普通生成模型保存值就是后台探针最终使用值；人工测试仍可为单次诊断临时选择其他已启用 mode。纯图像模型仍保留该字段作为任务和历史记录元数据，但实际探针固定使用模型目录，不得按该字段误调用 Chat Completions 或 Responses。

图像模型目录探针是低成本存在性和凭据连通性检查，不生成图片，也不证明生成质量。真实 `/v1/images/generations` 与 `/v1/images/edits` 只在用户主动对话或显式验收时调用；后台周期任务禁止通过定时生图消耗额度。后续新增非 OpenAI 协议图像模型时必须为对应供应商驱动定义明确探针，不能猜测或复用文本端点。

Gemini 原生账户可以从自身上游接口能力中选择 GenerateContent 或 Interactions 的 JSON / Streaming 生成形态；混合供应商账户可以选择 Chat Completions、Responses、Messages 或 GenerateContent。人工测试和后台检查必须按本次选中的精确 mode 构造下游诊断请求，再由混合账户模型映射决定实际上游协议和模型；`message_token_counting`、`count_tokens`、`embed_content` 等工具接口不进入检查协议选项。

## 4. 默认检查模型

模型目录允许为供应商设置默认检查模型，当前有效优先级：

```text
当前系统账户的个人默认检查模型
> 管理员维护的供应商系统默认检查模型
> 供应商协议档案内置默认检查模型
```

默认机制只在账户配置阶段生效：

- 新建账户选择供应商和协议档案后不初始化支持模型或检查模型，等待 Base URL/API Key 目录发现。
- 自动推荐模型必须同时存在于成功上游目录、当前账户支持模型和当前协议可测试目录。
- 后端创建接口在未显式提交 `healthCheckModel` 时，可以按同一作用域解析默认值并持久化，但解析后仍必须通过支持模型和协议能力校验。
- 账户保存后，active 周期哨兵、legacy / 专属 account-global 冷却复测和 Key 恢复只读取账户自己的 `healthCheckModel`；`pending_test` 激活读取 ready Catalog 的持久 selection，模型子 scope 读取持久 ProbeRecipe，二者都不得回退目录默认。
- 定时健康检查使用独立流量来源 `account_health_check`；`cooldown_retest` 只表示临时不可调用或限流账户的恢复复测。
- 检查模型或请求形态错误仍记录健康诊断并安排复检，但不累计账户连续失败阈值。
- 后续修改模型目录默认值不会批量改写已有账户，避免一次目录操作改变全部现有账户的探针行为。
- 编辑已有账户时优先回填账户保存值，不再重新套用个人或管理员默认值。

## 5. 人工测试

人工测试包含：

- 新增账户表单测试。
- 编辑账户表单测试。
- 账户列表单项测试。
- 管理侧、用户侧和授权实例侧测试。

人工测试使用账户或草稿配置作为请求输入，但不参与账户控制状态：

- 测试成功和失败都不修改 `healthCheckModel`、`supportedModels` 或其他账户配置。
- 测试成功和失败都不修改账户、授权实例或来源账户的 `status`、`schedulable`、冷却、最近错误、健康检查事实和运行态。
- 不写入、确认、清理账户失败样本、代理桶、客户端 IP 熔断、客户端 IP 账号回避、Codex turn 账号回避或会话亲和状态。
- 不推进账户内 Key 轮换，也不写入、恢复或摘除 Key 运行态。
- 不把人工测试结果作为创建、编辑、导入、激活或恢复的前置凭证。

人工测试允许保留的事实：

- 测试任务、测试会话和脱敏诊断结果。
- 使用记录、实际 token / cost 和审计记录，`traffic_source = manual_account_test`。
- 为完成 OAuth 请求所必需的 Access Token 刷新。刷新属于凭据生命周期维护，只允许写回新 OAuth 凭据，不能改变账户状态、调度、健康事实或额度调度状态。

如果测试响应携带额度头，人工测试不更新生产额度快照，避免诊断工具间接改变调度和可用性。

### 5.1 新增和编辑表单测试

- 固定使用当前表单的 `healthCheckModel`。
- 不展示可临时切换的模型下拉。
- 用户需要测试其他模型时，必须先修改表单中的“检查模型”，再执行测试。
- 检查模型必须属于当前表单 `supportedModels`。
- 刚加入表单的支持模型可以作为新的检查模型立即测试，但只有用户保存表单后才成为账户正式配置。
- 测试结果不作为保存、激活或状态恢复凭证。

### 5.2 列表操作测试

列表“测试”是面向用户的自由模型诊断工具，不属于账户健康检查：

- 模型候选来自该账户供应商、协议档案和当前账户所有者作用域可见的启用可测试模型目录；普通生成模型必须匹配协议能力，OpenAI v1 纯图像模型按模型目录探针能力进入候选。
- 不受账户 `supportedModels` 限制，允许用户验证尚未加入账户支持模型的供应商模型。
- 默认选中账户 `healthCheckModel`，但用户可以为本次测试自由切换其他候选模型。
- 后端固定命中当前账户并绕过账户 `supportedModels` 调度过滤，只验证该账户凭据、Base URL、代理、协议和指定模型的实际响应。
- 测试成功不会把模型追加到 `supportedModels`，不会修改检查模型，也不会自动保存任何账户配置。

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

用户打开测试弹窗时直接使用列表项中的检查模型和检查请求形态，不发起 options 请求。只有用户展开模型下拉时才调用专用模型选项接口；后续搜索继续复用同一接口：

```text
GET /__aisys__/api/accounts/:id/test-options
GET /__aisys__/api/my-accounts/:id/test-options
```

响应严格为 `Array<{ id, name }>`，支持 `keyword`、`limit` 和 `selectedIds`。批量接口只返回当前账户供应商作用域内可测试的模型名称，不携带 `supportedApiProtocols`、`testEndpointModes`、完整模型定义或凭据。默认模型和请求形态继续直接使用列表行的 `healthCheckModel` 与 `healthCheckEndpointMode`。

以下管理端或个人端镜像能力接口用于请求形态下拉的定点加载。用户展开请求形态下拉时只查询当前模型，不加载完整模型目录；模型批量选项是否已经加载不改变这条边界：

```text
GET /__aisys__/api/accounts/:id/test-options/models/:modelId
GET /__aisys__/api/my-accounts/:id/test-options/models/:modelId
```

能力响应包含 `{ id, name, supportedApiProtocols, testEndpointModes }`。从模型下拉切换模型时只清空旧模型能力，不自动加载新模型能力；用户随后展开请求形态下拉时才按新模型定点请求。列表、批量模型选项和能力接口均不得返回凭据；测试执行仍由后端按账户 ID 读取受控凭据，并在提交时重新校验模型与请求形态。

`testEndpointModes` 必须由单模型能力接口基于完整账户的上游接口能力返回；普通生成模型的结果是有效模型映射、模型协议和账户上游能力的交集。前端不能从列表裁剪账户或 `{ id, name }` 模型选项重新推导。合法跨协议映射按来源协议选择检查形态、按映射目标协议校验上游模型，不能用目标协议直接裁掉来源检查形态。模型目录探针不使用生成 endpoint mode 做能力过滤，只沿用账户已启用 mode 作为任务元数据，结果中的 `requestUrl=/v1/models` 才是实际请求形态的权威证据。

后台检查和人工测试不能仅凭 HTTP 2xx 判定成功。JSON 响应必须包含对应协议的正常完成对象，Streaming 响应必须包含对应协议的完成事件；模型目录探针必须得到标准模型列表且精确包含目标模型 ID。空正文、仅 `[DONE]`、HTML、畸形 JSON 或只有未完成数据片段都不能作为 `pending_test` 激活成功；framing 完整时归为 `framing_complete_neutral`。人工测试只显示诊断；激活由持久 Catalog activation selection 轮转当前免费 execution Attempt，单项失败只提交给对应精确模型能力 scope；active 周期检查和请求失败确认也只更新各自精确 scope。目录缺少目标只形成 visibility unknown，不得直接证明 execution unavailable。

新增和编辑表单直接使用当前表单中的 `supportedModels`、`healthCheckModel`、endpoint modes 和未保存配置，不额外读取已保存详情。表单测试不再请求自由模型选项。

## 8. 后台系统检查

后台系统检查始终启用，不提供用户关闭入口。不同检查场景共享底层最小请求执行器，但各自拥有明确的状态策略：

| 场景 | 模型来源 | 允许的状态副作用 |
| --- | --- | --- |
| 新账户激活检查 | 当前 Catalog 的免费 execution Attempt，`healthCheckModel` 仅优先 | 任一当前 definition / binding 的 `complete_success` 后按账户时间计划将 `pending_test` 转为 `active` 或 `disabled`；失败只更新精确能力，连续 24 小时无成功只产生 `owner_action_required` 告警，不写整号 `error` |
| 关键配置变更复检 | 重建后的 Catalog activation selection | 按配置变更类型进入待检查；旧 selection 变为 stale，不能把旧成功写入新 definition |
| 正常账户周期健康检查 | 账户检查模型 | 写 v1 哨兵事实并只推进精确 model_capability；不写整号状态 |
| 模型子 scope 恢复探针 | 失败 attempt 的持久 ProbeRecipe | 只推进精确 protocol_model / model_capability，不回退哨兵 |
| 真正账户全局运行态恢复 | 账户检查模型 | 只推进对应 account-scope owner；模型子 incident 不得创建该状态 |
| 账号级冷却复测 | 账户检查模型 | `complete_success` 只恢复匹配来源的自动状态；进入长期不可用后固定每 1 小时复测，只有连续独立 `transport_incomplete` 从观察起点满 7 天后仍失败才原子转为 `error` |
| API Key 恢复探针 | 账户检查模型 | 只更新目标 Key 运行态 |

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

- 新增账户默认保存为 `pending_test` 且不可调度。
- 导入请求中的 `active` 同样先落为 `pending_test`。
- 保存事务完成后立即投递后台激活检查，不等待人工测试任务，也不接受人工测试任务 ID 作为激活凭证。
- 后台激活先为当前 ready Catalog / credential baseline 建立持久 activation selection，`healthCheckModel` 对应 Route 只排在前面，不是唯一生死判据。selection 按稳定 Route / credential 顺序、每页最多物化 8 个无需额外成本授权的 execution Attempt；所有真实发送仍经过统一 durable admission、每物理账户 `running=1`、自动物理探针 5 分钟启动门禁和全局预算。单项完整 framing 失败或 `transport_incomplete` 只写该精确能力并继续后续候选；任一当前 definition / binding 的 `complete_success` 才按账户时间计划写入 `active` 或 `disabled`，健康成功不能绕过时间计划。
- Catalog 无可执行 Route、仅有 `catalog_only / manual_costed_execution / unsupported`，或所有免费 execution Attempt 尚未成功时，账户保持 `pending_test`。从 selection 创建起连续 24 小时仍无成功只显示 `activation_unconfirmed / owner_action_required`，不写 `account_activation_check_timeout`，也不把单模型失败升级为整号 `error`。只有本地可证明且覆盖全部 Route / credential 的共用配置错误，才允许专属 account-global configuration owner 写 `error`。
- `pending_test` 提供“重新检查”时，在同一事务把当前 activation selection 标记 stale，并按当前 publication / definition 重建 selection 和计划；不得清除已有精确能力 incident、绕过 5 分钟物理门禁或把人工测试结果当作激活凭证。已有 current selection 的重复命令应返回或唤醒同一资源，不能制造并行激活风暴。
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
- 新账户 activation selection 按 Catalog 的 Attempt 轮转 Key 池，至少一个当前 Key + Route 得到 `complete_success` 时账户可以激活；只有带独立 `transport_incomplete` 证据且通过来源 CAS 的 Key 才可进入对应自动 transport 运行态，完整 HTTP / 协议失败对 Key owner 保持中性，但可使本次精确 execution Attempt unavailable。
- 已保存账户新增或替换 Key 后，由后台 Key 检查初始化或更新 Key 运行态，不复用人工测试结果。
- 账号级周期健康检查只使用当前可用 Key 集合；Key 级冷却恢复固定命中目标 Key。
- 所有 Key 不可用时，账户通过 Key 池派生可用性退出调度，不需要由人工测试改变账户状态。

## 12. 失败分类

系统探针统一返回 `complete_success`、`framing_complete_neutral`、仅代表 `transport_incomplete` 的 `upstream_failure`，以及 `probe_task_failure/stale/unknown`。传输电路、账号冷却和 Key 恢复只把第三类视为负向证据；`pending_test` activation item、active 周期哨兵、请求派生探针和质量确认只把第二、三类作为匹配 Route / Attempt 的通用 execution 不可用证据，不得形成整号激活失败计数。任务、本地配置、过期结果和 revision 不匹配不计数、不改能力状态；selection owner 只将 item 安全终结、退避或因 revision 变化置 stale。request_failure 只能携带持久 `ProbeRecipe` 进入精确能力 owner，不能回退到账户哨兵或账户阈值。

自动 transport 失败候选：

- Base URL、代理、DNS、连接或 TLS 故障。
- lane hard timeout、读取中断、响应未完整结束或 framing 未完成。

凭据失效、账户级授权失败、限流、封禁或服务级故障不得从上游 status/body 自动推断；只有用户显式账户错误策略可以授权对应业务动作。本地可验证的凭据缺失、解密失败、配置非法和 OAuth token 生命周期错误走独立本地路径。

本地可验证的模型或检查配置故障：

- 本地目录中检查模型缺失、不可见或不属于最终支持模型。
- 本地账户能力没有可执行的检查 endpoint mode。
- 本地请求形态与已声明协议能力不匹配，或最小检查请求模板无法构造。
- 本地凭据缺失、解密失败或必要配置非法。

这些本地故障只记录“检查模型配置异常”，走 `probe_task_failure/unknown`，不能直接把整个账户写成 `temporary_unavailable`、`rate_limited` 或 `error`；只有本地可证明且覆盖全部 Route / credential 的共用配置错误，才交给专属 account-global configuration owner。上游 status/body 中的 `model_not_found`、未授权、权限不足、`unsupported_endpoint` 或类似文案不属于本地可验证配置事实；framing 完整时一律是 `framing_complete_neutral`，在激活与 active execution 探针中都只表达当前精确 Route / Attempt 不可用，不派生具体业务语义。成功检查只证明当前 Attempt 和对应最小请求链路可用，不代表全部支持模型都已经逐个验证。

## 13. 状态边界

后台检查机制始终存在，但按账户状态选择任务：

| 账户状态 | 系统行为 |
| --- | --- |
| `active` | 周期健康检查和必要运行态恢复 |
| `pending_test` | 持久 activation selection 轮转当前免费 execution Attempt；任务 unknown 有界顺延，精确失败不升级整号，连续 24 小时无成功只产生 `owner_action_required` 告警 |
| `temporary_unavailable` | 账号级冷却恢复 |
| `rate_limited` | 限流恢复 |
| `error` | 人工“异常恢复”只原子重置为 `pending_test` 并立即投递激活检查，不能直接恢复为 `active` |
| `disabled` | 不主动探测，尊重人工停用 |

人工测试在所有状态下都只诊断，不能成为任何状态的恢复入口。

自有账户在所有非 `disabled` 状态都允许人工停用，包括 `pending_test` 和 `error`；授权实例继续遵守授权侧状态管理边界。

## 14. 接口与存储契约

账户创建、更新、详情、导入和导出统一使用 `healthCheckModel`。

供应商选项和默认检查模型接口统一使用 `defaultHealthCheckModel`：

```text
PUT /__aisys__/api/providers/:code/default-health-check-model
```

账户不再提供独立“保存默认测试模型”接口。修改账户检查模型只能通过账户编辑保存接口完成。

人工测试接口：

- 新增 / 编辑表单测试必须使用草稿 `healthCheckModel`，后端拒绝测试其他模型。
- 列表测试必须显式提交本次 `model`，并校验模型属于当前账户供应商、协议档案和所有者作用域可见的启用可测试模型目录；不要求属于账户 `supportedModels`。OpenAI v1 图像生成模型只能使用模型目录探针，不能因为账户保存了 `responses_sse` 就发送文本 Responses 请求。
- 测试任务结果不能被账户保存、状态恢复或后台检查任务当作可信状态凭证。

## 15. 验证要求

- 新增、编辑和列表人工测试成功或失败后，账户、授权实例、Key、健康事实和运行态均不变化。
- 新增 / 编辑测试只能使用表单检查模型；列表测试关闭重开后仍默认使用账户检查模型。
- A 账户测试运行时可以打开和启动 B 账户测试，旧结果不能覆盖新弹窗。
- 列表接口只增加检查模型和检查请求形态两个轻量标量；打开测试弹窗零 options 请求，模型下拉读取严格 `{ id, name }` 的批量 `test-options`，请求形态下拉只读取当前模型能力。
- 页面和后端均不存在账户批量测试入口。
- 新账户保存后保持 `pending_test`，后台激活检查成功后自动进入 `active`。
- 人工草稿测试成功不能直接激活新账户。
- 定时健康检查不存在关闭开关，也不存在 `health_check_enabled` 候选条件；`temporaryUnavailableContinuousProbeEnabled` 只由冷却复测读取，不能作为定时健康检查候选条件。
- 激活严格使用 ready Catalog 的持久 activation selection，并仅把 `healthCheckModel` 对应 Route 排在前面；周期哨兵和真正账户全局探针严格使用 `healthCheckModel`，模型子 scope 探针严格使用持久 ProbeRecipe。后两者缺失或非法时不猜测其他模型，激活 selection 则按自身稳定候选顺序继续。
- 修改模型目录默认检查模型只影响后续新账户初始化，不改变已有账户。
- 多 Key 人工测试不写 Key 状态，后台激活和 Key 恢复探针可以按职责写入 Key 状态。
- 模型能力层永不把账户整体标记为不可用；全部能力阻断只形成派生门禁。只有用户显式策略或真正账户全局事实 owner 可以进入账户级保护状态。
