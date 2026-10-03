# 问题-0278：账户列表从不返回运行态 overlay（被抑制账户显示成"可调度"）

- 编号：BUG-0278
- 状态：第一批已修复（2026-10-03），待发布
- 发现：BUG-0269 后的全量排查（UI←数据源可达性审计）。

## 现象

前端账户列表消费 `runtimeAvailability` / `circuitSummary` / `apiKeyRuntime` / `effectiveAvailability` 的 runtime 分支（`accountFormatters.ts` / `accountStatusPresentation.ts` / `accountListMutations.ts` / `accountRules.ts` / `AccountUsageCell.vue`），但 Go `internal/accounts/list.go` 的 `ListItem` 明确"runtime overlays … stay omitted"。后果：熔断避让/验证/恢复、调度降级/半开等运行态在列表永不出现，**实际被运行时抑制的账户可能显示成"可调度"**；探针 tooltip 与 traceId、"重新验证 Key 池"行操作入口不可见。jobs 侧数据真实存在（生产 `account_list_availability_projections` 1,484 行、`runtime_overlays` 1,536 行），只是 gateway 不读。

## 根因

Go 移植只落了列表的静态列；Node 的 `hydrateAccountListPageWithRuntimeSnapshot`（account-status-snapshot.service.ts:51-95，内联合并）与 live hydrate 面（Redis runtime + DB circuit 账本 + apiKeyRuntime 摘要 → 状态机合成）未迁移；且 Node 管理列表从不消费持久投影，Go 也没有等价的 live hydrate。

## 修复（第一批切片，四者必须同批避免前端中间态）

- `ListItem` 新增 `runtimeAvailability`（status/reason/since，第一批不含 probePresentation）/ `circuitSummary` / `apiKeyRuntime` 三个 omitempty 字段；`effectiveAvailability` 在可用基线上二次合成 `runtime_*` / `api_key_pool_unavailable` 分支（前端 `accountListMutations.ts:35-56` 按此判断）。
- 新文件 `internal/accounts/list_runtime_overlay.go`：三 port + `hydrateRuntimeOverlay`（批量读、逐源失败 warn+字段缺席、不阻断页面）+ runtimeKey 派生（owner=`id`；authorized 行=`{id}:authorized:{sys}:{grp}:{authz}`）+ effective 合成状态机（对照 jobs `listavailability_effective.go`）。
- 数据源全部 gateway 既有（零新增 SQL）：suppression 快照 + configured-policy avoidance（runtime）、`gatewaycircuit.LoadPublicAccountCircuitSummaries` + circuit control plane（circuit）、`accountkeystates.Store.LoadSummariesByAccountIds`（key 池摘要）；组合根仅 `ChainEnabled` 分支装配、fail-fast。
- 行为变化（修复目标，文档 `网关错误处理完整链路.md` 已同步）：active 账户被熔断/抑制时列表状态从"可调度(绿)"变为对应运行态色（金/蓝）。
- 用户侧 `/my-accounts` 与管理面共用 `ListPage`，同源生效。

## 测试

overlay 四字段形状与无事实键集缺席回归、runtime_* / api_key_pool 分支、nil 端口降级、三源读失败降级、authorized 键派生（7 个新用例，装配前必红）。

## 残余项（待补批次）

- 第二批：`availabilityPresentation` 投影 + 探针 tooltip/traceId（前置：进程内 probe store 增加批量快照端口；go-only 栈无 `gateway-account-recovery` Redis 写入方，属独立存量缺口）。
- 第三批：`/my-accounts` PG 投影读路径 + 503 fail-closed 门（Node useAvailabilityProjection 语义）——取证发现 Go 两路由共享 ListPage，该批剩余范围需按此事实重新界定。
