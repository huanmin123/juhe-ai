# 问题-0278：账户列表从不返回运行态 overlay（被抑制账户显示成"可调度"）

- 编号：BUG-0278
- 状态：第一、二批已修复（2026-10-03），待发布
- 发现：BUG-0269 后的全量排查（UI←数据源可达性审计）。

## 现象

前端账户列表消费 `runtimeAvailability` / `circuitSummary` / `apiKeyRuntime` / `effectiveAvailability` 的 runtime 分支（`accountFormatters.ts` / `accountStatusPresentation.ts` / `accountListMutations.ts` / `accountRules.ts` / `AccountUsageCell.vue`），但 Go `internal/accounts/list.go` 的 `ListItem` 明确"runtime overlays … stay omitted"。后果：熔断避让/验证/恢复、调度降级/半开等运行态在列表永不出现，**实际被运行时抑制的账户可能显示成"可调度"**；探针 tooltip 与 traceId、"重新验证 Key 池"行操作入口不可见。jobs 侧数据真实存在（生产 `account_list_availability_projections` 1,484 行、`runtime_overlays` 1,536 行），只是 gateway 不读。

## 根因

Go 移植只落了列表的静态列；Node 的 `hydrateAccountListPageWithRuntimeSnapshot`（account-status-snapshot.service.ts:51-95，内联合并）与 live hydrate 面（Redis runtime + DB circuit 账本 + apiKeyRuntime 摘要 → 状态机合成）未迁移；且 Node 投影读路径存在于代码但读写开关默认关闭、生产未启用读（管理面恒 live），Go 也没有等价的 live hydrate。

## 修复（第一批切片，四者必须同批避免前端中间态）

- `ListItem` 新增 `runtimeAvailability`（status/reason/since，第一批不含 probePresentation）/ `circuitSummary` / `apiKeyRuntime` 三个 omitempty 字段；`effectiveAvailability` 在可用基线上二次合成 `runtime_*` / `api_key_pool_unavailable` 分支（前端 `accountListMutations.ts:35-56` 按此判断）。
- 新文件 `internal/accounts/list_runtime_overlay.go`：三 port + `hydrateRuntimeOverlay`（批量读、逐源失败 warn+字段缺席、不阻断页面）+ runtimeKey 派生（owner=`id`；authorized 行=`{id}:authorized:{sys}:{grp}:{authz}`）+ effective 合成状态机（对照 jobs `listavailability_effective.go`）。
- 数据源全部 gateway 既有（零新增 SQL）：suppression 快照 + configured-policy avoidance（runtime）、`gatewaycircuit.LoadPublicAccountCircuitSummaries` + circuit control plane（circuit）、`accountkeystates.Store.LoadSummariesByAccountIds`（key 池摘要）；组合根仅 `ChainEnabled` 分支装配、fail-fast。
- 行为变化（修复目标，文档 `网关错误处理完整链路.md` 已同步）：active 账户被熔断/抑制时列表状态从"可调度(绿)"变为对应运行态色（金/蓝）。
- 用户侧 `/my-accounts` 与管理面共用 `ListPage`，同源生效。

## 测试

overlay 四字段形状与无事实键集缺席回归、runtime_* / api_key_pool 分支、nil 端口降级、三源读失败降级、authorized 键派生（7 个新用例，装配前必红）。

## 修复（第二批：availabilityPresentation 投影，2026-10-03 交付）

- `ListItem` 新增 `availabilityPresentation`（omitempty，合成后每行恒产出；形状对齐前端 `AccountAvailabilityPresentation`，前端零改动）。管理面 `/accounts` 与用户面 `/my-accounts` 共用 `ListPage`，同构生效；仅列表面涉及（gateway 无 jobs 语义详情面，`FindEditBasicDetail` 是编辑凭据面不携带可用性）。
- 新文件 `internal/accounts/list_availability_presentation.go`：逐函数对照 jobs `listavailability_effective.go:283-526` 移植 `presentationPayload` / `presentationMapping` / `probeFactsPayload` / `healthObservationPayload` / `schedulePayloadState` / observationId 派生（sha256 前 24 位，kind=`health_check`、identity=账户 id，与 jobs 同源）。合成时机：`ListPage` 全部 hydrate（含批次一 `hydrateRuntimeOverlay` 的 effective 重算）之后统一执行，输入=行内 accounts 健康列 + 该行最终 `effectiveAvailability` 状态；纯进程内合成，无端口、无失败降级路径。**不发 `runtimeAvailability.probePresentation` 键**（前端零消费，批次一纪律保持）。
- 扩列边界从第一批的"零新增 SQL"放宽为"**同表加法扩列**"：`listItemColumns` 补选 accounts 表既有 8 列 `last_health_check_at` / `next_health_check_at` / `last_health_check_status_code` / `last_health_check_error_code` / `last_health_check_error_message` / `last_health_check_trace_id` / `cooldown_retest_last_at` / `cooldown_retest_last_status_code`（列名以 maintenance `pg_schema_business_tables.go:648-665` 为准），不动既有列、无 schema/DDL 变更；gateway 测试 fixture 建表已含同名列，零 fixture 变更。`cooldown_retest_last_*` 当前只被未移植的 jobs sourceProbePayload 分支消费，随行载入保持列镜像完整。
- **probe store 批量快照端口移出本批的裁决与理由**：探针 tooltip 的 lastObservation / traceId 唯一来源是 accounts 表 PG 健康列，本批经健康列直接合成即可完整交付 tooltip 数据面；probe store 无 observation 概念且其 lastObservation 链在 go-only 栈唯一写入方 `gateway-account-recovery` 为 Redis 形态、零写入方，属独立存量缺口而非本缺陷的组成；前端对 `runtimeAvailability.probePresentation` 零消费；jobs 对 `runtime_*` 分支同样不产 probe（runtime 事实由 runtimeAvailability 承载）。
- 与 jobs 的移植范围差异（取证后取舍）：`sourceProbePayload`（`source_*` 探针分支）不移植——gateway 第一批 effective 合成（`ownerEffectiveAvailability` + `applyRuntimeOverlayAvailability`）只产出 `instance_*` / `api_key_pool_unavailable` / `runtime_*` / `available`，从不产出 `source_*`，source 探针输入列也不在扩列清单；`statusBoundary` 仅保留 gateway 可达分支（`instance_expired`←account_expires_at、`instance_cooldown`←cooldown_until），jobs 的 `runtime_local_suppressed`→`policy_ttl_expiry` 需要 recoveryAt 输入（批次一 DTO 无 probePresentation）故不触发；`instance_pending_test` 探针（kind=`activation_check`）及其余分支按 jobs 原样。

## 测试（第二批）

`list_availability_presentation_test.go`：presentationMapping 词汇全表、healthObservation 成功/失败（errorCode / httpStatus≥400）/ 空值 / 96 字符截断 / traceId、schedulePayloadState none/scheduled/due_waiting/不可解析、probe 门控（仅 `instance_pending_test` 产 activation_check；`runtime_*` / `api_key_pool_unavailable` / `source_*` 不产）、observationId 派生稳定与指纹一致、boundary 取值与互斥、instance_error 动作覆写；列表端到端（扩列后 ListPage 返回 availabilityPresentation、缺列行降级为无 probe、due_waiting、runtime 分支行合成发生在批次一 effective 重算之后、`runtimeAvailability` 无 `probePresentation` 键）。合成函数不存在时无法编译（守卫形态）；禁止 go test 约束下由主代理统一执行。

## 残余项（登记）

- 第三批：已裁决不实施（2026-10-03，有据不修）——`/my-accounts` PG 投影读路径 + 503 fail-closed 门（Node useAvailabilityProjection 语义）不再实施。理由：① Node 投影读写开关默认全关（runtime.ts 两 flag 默认 false）且生产从未启用读开关，Node 实际部署语义即 admin live + user live，Go 现状与 Node 部署语义一致；② Go live 契约是投影 payload 的超集（usage 总量/oauthUsage/authorizationStats 三字段为 Node 列表从未输出的 Go 增量，前端 accountUsageFormatters.ts:45 真实消费 oauthUsage），切投影读必须补 Node 都没有的 overlay，属契约倒退风险；③ 生产约 1.5k 账户规模下投影读无可测收益；④ 第三批原始动机（overlay 同源生效）已被第一/二批在共享 ListPage 上达成。若未来单用户账户规模达数千级或 `/my-accounts` 延迟成为问题，再按选项 A（含 shadow 前置）立新计划。
- 独立存量缺口（不在本缺陷范围）：probe store 无 observation 且 go-only 栈无 `gateway-account-recovery` 写入方；如后续需要 runtime 探针 observation，需另行立项。
- 第二批复审观察（非阻断）：`runtime_local_suppressed` 在 gateway 可达但本批恒不产 `policy_ttl_expiry` 边界——jobs 分支输入 `runtimeRecoveryAt`（来自 `ProbePresentation.recoveryAt`），批次一 DTO 无该字段；后续若批次一 DTO 引入 recoveryAt，需同步补该边界分支。`cooldown_retest_last_at/last_status_code` 两列随行载入暂无 gateway 消费者（列镜像完整性取舍，随 source 分支输入面一并启用）。
