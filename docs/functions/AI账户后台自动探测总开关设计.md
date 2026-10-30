# AI 账户后台自动探测总开关设计

> 状态：已确认（2026-10-30 用户拍板），实施以本文为准；行为变更先改本文。
> 计划载体：[PLAN-20261030T062141000Z](../plans/PLAN-20261030T062141000Z-AI账户后台自动探测总开关.md)。

## 1. 目标与口径

新增账户级「后台自动探测」总开关。关闭后，系统不再对该账户发起任何**后台自动探测**请求；真实用户请求和用户显式操作（手动测试、手动余额刷新、手动模型检测、手动同步上游模型目录）不受影响。余额刷新、xAI Grok 用量刷新与 OAuth token 刷新不是健康探测，不纳入本开关。

- **口径对齐**：用户所说「禁止被动」= 关闭系统后台自动探测；「不禁止主动」= 用户显式点击的操作保持可用。
- **后台自动探测**：由系统定时、状态机、队列或后台任务自动发起、无需用户当次操作的健康/恢复类上游请求。本开关只控制这一类。
- 上位文档：[AI 账户检查模型与人工测试设计](AI账户检查模型与人工测试设计.md)（检查三件套与人工测试边界）、[AI 账户运行态探针恢复设计](AI账户运行态探针恢复设计.md)（状态机）、[账号健康检测设计](账号健康检测设计.md)（J1）、[AI 账户特供快速恢复通道设计](AI账户特供快速恢复通道设计.md)（特供）、[AI 账户锁死调度机制设计与实现前复审](AI账户锁死调度机制设计与实现前复审.md)（锁死）。本文与上述文档冲突时，以「本开关只关后台探测、不碰账户自有策略」为仲裁原则回正各自条文。

## 2. 数据与 API

- `accounts` 新增 `background_probe_enabled integer NOT NULL DEFAULT 1 CHECK (background_probe_enabled IN (0,1))`；`1` = 允许后台自动探测（默认），`0` = 禁止（手动模式）。存量账户默认 `1`，行为零变化。
- 该字段属于账户配置：创建 / 更新 / PATCH 接受 `backgroundProbeEnabled`；详情、列表、克隆上下文、导入导出、健康监控响应返回 `backgroundProbeEnabled`。
- 变更走既有账户更新链路（`config_revision + 1`、审计、缓存失效）；在途探针结果因版本 / 代次不匹配自动作废。
- **授权实例**只读继承来源账户值，不允许实例单独设置；来源翻转时沿 `temporaryUnavailableContinuousProbeEnabled` 的既有传播链同步实例行。
- **媒体域（audio/video）不适用**：前端隐藏开关与「手动」标签；后端对媒体域账户提交该字段拒绝或忽略（取统一裁决：创建/编辑拒绝，PATCH 幂等忽略并审计）。媒体域保持现有状态机与视频准入 / CAS 通道。**image 域适用（2026-10-30 二轮复审裁决）**：图像账户存在真实后台探测（激活检查、冷却恢复、电路 / 质量预检——仅豁免 active 周期巡检），二态规则天然适配；后端守卫按 `SkipsConversationHealthStateMachine`（恒为 audio/video）判定，image 域开关可翻转、显示「手动」标签。

## 3. 关闭后的探测门禁（双重）

所有后台探针家族统一两层设门，缺一不可：

1. **候选筛选**：候选 SQL 增加 `background_probe_enabled = 1`；授权实例同时检查 `source.background_probe_enabled = 1`。
2. **派发前守卫**：真正构造上游请求前重读账户 `config_revision + background_probe_enabled`；关闭则返回 `probe_skipped_disabled`——不发请求、不写健康事实、不写 usage、不推进状态，任务按终态消费（ack，不重试）。开关位读取失败一律失败关闭（2026-10-30 复审修订：读失败按手动模式跳过——后台探针是可重试的后台任务，读失败窗口跳过零成本，fail-open 会让手动账户收到真实上游请求或建出无恢复探针的 incident）。

覆盖家族与落点：

| 家族 | 候选筛选 | 派发前守卫 / 写入点 |
| --- | --- | --- |
| J1 周期健康检查 / 首次激活 / 配置变更复检 / 冷却复测 | `accounthealth/direct_input_reader.go` | `scheduler.go` `checkInputFreshness` 扩展为同时校验开关 |
| 请求失败二次确认 | — | gateway `chain_request_failure_health.go` 不投递；`worker_health_probe_outbox` 消费跳过并 ack |
| 运行态（`recovery_wait` / `precheck_pending` / `runtime_degraded`） | — | gateway 状态机建立前检查开关 |
| account circuit 恢复 | `account-circuit-recovery` | 关闭时关闭该账户 incident |
| Key 冷却复测 / key-model memory 恢复 | `account-api-key-cooldown-retest` / `key-model-memory-recovery` | 候选过滤 + 派发前守卫；关闭时清理派生 Key 状态 |
| 速度优先首字恢复 | `normal-route-speed-first-recovery-probe` | 恢复探针不运行；`latency_degraded` 仍可请求级建立（见 §5） |
| 质量失败预检 | `account-quality-failure-precheck` | 入队与消费两端检查 |
| J3b 定时模型检测 / 质量隔离恢复 | `modelcheckowner/business_scheduler.go` | 暂停；手动「立即检测」不受影响 |
| 账户创建 / 配置变更的 J1 输入 | — | `account_health_jobs_input_outbox` 写入前检查开关；关闭时删除 pending outbox 与 due 输入版本 |

不设门（明确排除）：真实 `/v1` 请求、手动测试 / 手动刷新 / 手动检测、余额查询与刷新、xAI Grok 用量刷新、OAuth token 刷新 / keepalive、J3a 代理延迟检测、时间计划同步。

## 4. 关闭时的状态归一化（同事务）

关闭总开关的事务内，除改字段外还需：

| 现状 | 处理 |
| --- | --- |
| `pending_test` | → `active`，不执行首次激活检查 |
| `temporary_unavailable` / `rate_limited`（无论来源、无论有无 fence） | → `error`，`last_error_message` 注明「手动模式：原冷却/限流状态转为异常，需人工恢复」（2026-10-30 复审修订：来源标记不可靠——显式策略 / 系统额度冷却与探针冷却共用同一 `cooldown_retest_generation` fence（`markCooldown` 同写 `cooldown:` 代际），无法按 fence 区分来源，全部转异常以不覆盖用户显式策略意图） |
| `quality_isolated` | → `error`（保留质量处罚，质量隔离恢复探针不再运行），清理 enforcement |
| `error` / `disabled` | 保持不变 |
| 同请求显式 `status` 输入 | 优先于归一化（本请求显式提交的状态是该次操作意图；二态校验已拒绝过程态提交），不被覆盖为 `error` |
| `recovery_wait` / `failure_observed` / `precheck_pending` / `runtime_degraded` | 清理，取消 due 任务与半开租约；已有 `latency_degraded` 一并清理 |
| circuit `SUSPECT` / `OPEN` / `HALF_OPEN` / `RECOVERING` | 关闭该账户相关 incident（含子 scope）：禁用事务内推进 dispatch fence 并入队 `dispatch_revision_changed` 事件（既有运行时重置链路 `AdvanceBatchDispatchRevision`），jobs 控制面投影消费事件后按账户关闭全部非 CLOSED circuit 运行态，写审计原因 |
| Key 冷却 / key-model memory 状态 | 清理探针派生状态 |
| 特供恢复 | 自动取消并释放名额；手动模式禁止重新设置（400） |
| 账户锁死 | 自动解除并清理 `account_lock_states` / lock 运行态（来源与授权实例行同款）；手动模式禁止重新锁死（400） |
| 健康事实 | `next_health_check_at` 置空、失败计数清零、停止更新；`last_health_check_at` / `last_health_success_at` 保留为历史展示 |
| Redis 运行态 / 速度优先降级 / 探针派生 Key 运行态池 | 禁用事务提交后经装配的运行时重置链路端口清理（latency_degraded、`account_api_key_runtime_states` 按新版本重验证、网关运行时缓存投影失效）；端口未装配降级为仅数据库侧清理并在审计 details 注明，清理失败按 best-effort 留痕 |

归一化必须复用现有账户状态变更服务（`schedulable` 联动、运行态列族归一化、`config_revision` 递增、缓存失效、分组统计脏标、审计），禁止裸 UPDATE。禁用事务内同时推进 dispatch fence（`AdvanceBatchDispatchRevision`，入队 circuit 关闭事件）；Redis 运行态 / latency 降级 / Key 运行态池在提交后经运行时重置链路端口清理（见上表）。

**二态健康状态**：关闭后健康状态只有 `active`（正常）/ `error`（异常）；`disabled` 是人工停用操作，单独展示，不计入健康状态。`pending_test`、`temporary_unavailable`、`rate_limited`、`quality_isolated` 不允许作为手动模式账户的健康状态，创建 / 编辑提交返回 400。

- `error` 只由人工操作、本地可验证硬错误、显式策略 / 系统额度派生冷却转换或已有质量隔离转换写入；真实请求失败不自动写异常。
- 手动账户「异常恢复」直接写 `active`，不经过 `pending_test`，不投递后台检查。
- **连接变更不再落 `pending_test`**：`patch.go` 的 `connectionChanged` 分支对手动账户跳过（对齐媒体域 `SkipsConversationHealthStateMachine` 先例），凭据 / 代理编辑保持原状态，由真实流量验收。

## 5. 真实请求边界（零变化）

上游失败仍按现有候选规则排除当前账户并切换后续候选；`retry_next`、同账户兄弟 Key 重放、多分组后备、`ServerRetryBudget`、请求墙钟、attempt 上限、本地 IP / transport 局部回避、请求级审计与错误语义全部保持现状。真实请求失败不写账户级冷却、限流、质量隔离。

`latency_degraded` 属请求级速度排序：仍可由真实流量建立，按 TTL 或真实流量恢复；仅其后台恢复探针不运行，且不再写 `nextProbeAt` / probe-index，不作为账户健康状态展示。

## 6. 与账户自有策略的关系（零影响）

账户错误策略（`retry_next` / `temp_unschedulable` / `rate_limited` / `error_disabled`）、响应拦截策略（含 `avoid_account_ttl`）、迁移流量选项、手动模型检测处罚（`quality_isolate` / `disable` / `fallback`）、系统额度规则 `system.upstream_insufficient_quota`，都是账户所有者的显式配置或既有系统行为，**本开关不限制、不修改**。

唯一后果：手动模式下这些策略产生的冷却 / 限流 / 质量隔离状态没有自动恢复（对应恢复探针已关闭），只能人工「恢复正常」或临时重新开启探测。这是既有策略语义的自然结果，须在关闭确认弹窗与策略页说明，不静默。

## 7. 手动模式功能可用性

| 功能 | 手动模式处理 |
| --- | --- |
| 特供恢复 | 不可用；进入手动模式自动取消并释放名额；列表操作置灰 |
| 账户锁死 | 不可用；进入手动模式自动解除并清理 lock 运行态；列表操作置灰，死期 / 重试间隔字段隐藏或只读 |
| 持续恢复探活 | 子开关置灰不生效；已保存值保留，重新开启父开关后恢复原值 |
| 自动激活 / 待检查 | 不执行；账户直接正常 |
| 冷却 / 限流自动恢复 | 不执行；已有过程态按 §4 归一化 |
| Key 冷却 / key-model 自动恢复 | 不执行；已有状态清理，不再建立探针派生 Key 运行态 |
| 质量隔离自动恢复 | 不执行；已有 `quality_isolated` 转异常 |
| 速度优先首字恢复探针 | 不执行；`latency_degraded` 仍可请求级建立 |
| 定时模型检测 | 暂停；schedule 配置保留，重新开启后按原计划恢复 |
| 手动模型检测 / 账户错误策略 / 响应拦截 / 迁移流量 / 系统额度规则 | 完整保留，行为零变化；产生的状态仅人工恢复 |
| 时间计划 / 超级优先 / 降级备用 / 会话亲和 / 并发 / 代理 / 模型映射 | 保留 |
| 余额查询 / 刷新、xAI Grok 用量刷新、OAuth token 刷新 | 不纳入 / 不属于探测，保持现状 |
| 手动测试 / 手动同步上游模型目录 | 保留，不写账户状态 |

**重新开启**：状态为 `active` 时设置 `next_health_check_at = now` 并投递一次健康检查；`error` / `disabled` 按人工路径恢复。手动模式不允许 `pending_test`，不存在「补激活检查」语义。

## 8. 创建 / 导入语义

- 新建（表单 / API 直调 / OAuth 托管）：手动模式默认状态 `active`（跳过首次激活检查，真实流量即验收）；表单状态只提供「正常 / 停用」。
- 导入：手动模式默认 `disabled`（2026-10-30 决策，保留「凭据未验证」安全门），导入预览提示手动启用；显式状态仅允许 `active` / `error` / `disabled`。
- 克隆：继承源账户 `backgroundProbeEnabled` 与二态状态。
- `patch.go` 连接变更分支、`runtime_reset.go` 异常恢复分支、`accountscore/writeinput.go` 创建默认值、`oauthmgmt` / `aipublic` 创建状态、导入状态重映射（`active → pending_test`）都必须感知该开关：手动模式一律不产生 `pending_test`、不投递激活 / 复检。

## 9. 展示

- 账户列表、健康监控、账户详情：状态标签组增加「手动」模式标签（灰色），tooltip 说明「系统不主动探测，仅真实流量；特供 / 锁死等自动恢复功能不可用；异常需手动处理」。健康状态只显示正常 / 异常 / 停用，隐藏 `待检查` / `临时不可调用` / `限流中` / `质量隔离` / `待探针确认` / `调度降级` / `恢复中` 等过程态。
- 状态 tooltip 不显示「下次检查」，改显「主动探测：已关闭」；已有历史检查时间可标注「关闭前」。
- 授权实例显示「跟随来源账户」，只读。
- 健康监控页：手动账户保留历史小时条与可用率，并标注「手动模式，关闭后无新检查」；最近 / 下次独立检查显示「不适用」。
- 状态卡片折叠摘要显示 `手动模式`；「重新检查」入口隐藏；「异常恢复」直接恢复正常。
- 正交状态（授权到期、授权失效、账户到期、授权额度用完、时间计划停用）是独立展示状态，不属于二态健康状态，保持现有展示。

## 10. 观测与审计

- 审计事件：`account_background_probe_disabled` / `account_background_probe_enabled`，记录归一化的状态与清理的运行态 / 待处理任务数量；运行态清理端口的执行结果（已清理 / 清理失败留痕 / 端口未装配降级为仅数据库清理）随 details 落事件；特供取消、锁死解除各自沿用既有审计动作并附带原因。
- 派发前守卫拒绝记 `probe_skipped_disabled` 日志（按账户限频，调度臂与 outbox 消费臂共用同一限频窗口），不计健康失败、不写 usage。
- 健康告警 / 看板按「无检查数据」可能空窗：手动账户需可识别（列表 / 健康页「手动」标签即标识）。

## 11. 验证要求

- Mock 上游记录请求数：关闭后 J1 / 激活 / 失败确认 / 冷却复测 / 运行态 / 电路 / Key / 速度恢复 / 质量预检 / 定时模型检测请求数均为 0；余额 / OAuth / 手动操作请求不受影响。
- 关闭前已入队、关闭后派发：零上游请求，任务 ack，无健康事实写入（`config_revision` 围栏 + 开关守卫双保险）。
- 关闭归一化（2026-10-30 复审修订）：`pending_test` → `active`；`temporary_unavailable` / `rate_limited` 一律 → `error`（显式策略 / 系统额度冷却与探针冷却共用同一 fence，来源标记不可靠，全部转异常以不覆盖策略意图）；`quality_isolated` → `error`；`error` / `disabled` 不变；同请求显式 `status` 输入优先；`schedulable` 联动正确。
- 关闭清理：dispatch fence 推进入队 circuit 关闭事件、投影后 incident（含子 scope）收敛 CLOSED；实例行锁死解除与 fence 推进同事务落地；提交后运行态清理端口清 latency 降级 / Key 运行态池 / 运行时缓存，端口缺席审计注明降级。
- 特供取消释放名额、锁死解除清理运行态；手动模式重新设置返回 400。
- 授权实例继承来源、不能单独关闭；实例状态同步收敛二态。
- 手动测试 / 手动刷新 / 手动检测（含处罚）/ 错误策略 / 响应拦截 / 迁移流量 / 系统额度在关闭状态下行为与开启状态完全一致。
- 连接变更、配置变更不再产生 `pending_test`；创建 / 导入 / 克隆 / OAuth / 公开 API 全写入口不落过程态。
- 前端两段模板分支、折叠摘要、二态映射、「手动」标签、健康页展示、依赖置灰正确；媒体域（audio/video）不展示，image 域正常展示与生效。
- 构建、类型检查、定向包测试、前端回归全绿；文档同步完成（本文 + 检查模型 / 健康检测 / 运行态 / 特供 / 模型检测 / 核心功能 / 接口契约 / 架构总览）。

## 12. 明确不做

- 不做三态探测策略（标准 / 仅恢复 / 完全关闭）。
- 不限制账户错误策略、响应拦截、迁移流量、手动模型检测处罚、系统额度规则（2026-10-30 用户澄清：这些是账户自己的配置，开关只管后台探测）。
- 不修改余额查询、xAI Grok 用量刷新、OAuth token 刷新、J3a 代理延迟检测。
- 不引入 `health_check_enabled` 旧字段或兼容双写。
