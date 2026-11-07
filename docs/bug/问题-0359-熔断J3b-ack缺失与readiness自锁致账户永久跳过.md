# BUG-0359 熔断 J3b ack 缺失与 readiness 自锁致账户永久跳过

## 基本信息

- 编号：BUG-0359
- 状态：已修复（2026-11-08 随总发布上线 `dev-15de4f1dd-dirty` / schema 21，生产验证通过；P2 后续项移交 #T183）
- 严重程度：P1（单账户永久不可调度；锁死/超级优先配置被熔断门静默绕过；同类账户存在复发面）
- 发现时间：2026-11-07
- 发现方式：用户反馈（生产账户 `oai.ycxlg.com-e4ayee` 长时间无请求，流量全部落在 `tokenrhythm.studio-74bpbf`）
- 模块：后端 / 网关 / 账户电路持久化 / J3b 投影 / jobs 控制面清理
- 关联计划：无
- 关联 bug：BUG-0318（外层重试退避）、BUG-0295（渐进恢复与 outbox）、BUG-0306（J3b GC 契约）
- 责任人：待定

## 问题概述

- 现象：生产账户 `acc_af2ec0bed14157aa`（`oai.ycxlg.com-e4ayee`，deepseek，`super_priority_enabled=1` 且已执行锁死）自 2026-11-07 18:46（本地）起没有任何上游尝试；同一 API Key `key_8ea8506896988c19` 的 `deepseek-v4.1-flash` 流量全部由 `acc_2996788e5727408d`（`tokenrhythm.studio-74bpbf`）承接。
- 期望：锁死 + 超级优先账户在上游可用时应优先承接请求；即使发生一次传输失败，也应在锁死窗口内继续尝试该账户，或按既有熔断/恢复语义在短时间内恢复。
- 实际：每次 `gateway_dispatch_decision` 仍把该账户选为第一候选，随后 `gateway_account_circuit_blocked phase=SUSPECT` 拦截，流量静默切到 tokenrhythm；该状态持续 4 小时以上且没有进程内自愈出口。
- 影响范围：单账户完全不可调度；锁死 + 超级优先配置失效；同类“曾有熔断事故、CLOSED 运行态过期、控制面墓碑滞留”的账户在下次传输失败时均可复现。

## 生产取证（只读）

### 账户与调度事实

| 项 | oai.ycxlg.com-e4ayee | tokenrhythm.studio-74bpbf |
| --- | --- | --- |
| account_id | `acc_af2ec0bed14157aa` | `acc_2996788e5727408d` |
| 分组 | `grp_48971a3f1ced644b`（deepseek） | 同组 |
| status / schedulable | `active` / `1` | `active` / `1` |
| priority / super_priority | `0` / `1` | `1` / `0` |
| 锁死 | `lock_state=ENGAGED`，deadline `2026-11-07T10:51:10.539Z`（已过期） | 无 |
| 健康探测 | 2026-11-07T14:04:13Z，`deepseek-flash` 200 | 2026-11-07T14:09:30Z，200 |
| cooldown / last_error | 空 / 空 | 空 / 空 |

### 时间线（UTC）

- `10:45:47.744Z`：oai 最后一次业务成功（`200`，duration 3211ms）。
- `10:46:10.532Z`：oai 上游传输失败，`elapsedMs=120006`，`PrimaryStartedGatewayTransportError` / `上游请求超时`；同一时刻账户锁死进入 `ENGAGED`。
- `10:46:11.056Z` 起：网关持续输出
  `gateway_account_circuit_persist_failed ... scopeKey=14:protocol_model|20:acc_af2ec0bed14157aa|9:openai:v1|4:text|19:deepseek-v4.1-flash ... 最后状态：cas_conflict`；
  截至 `15:01Z` 共 507 条（约每 30 秒一条）。
- `10:46:35.439Z` 起：同 API Key 的流量全部转向 tokenrhythm；之后 oai 零 attempt、零 usage 记录。
- 每次请求日志：`gateway_dispatch_decision` 的 `selectedAccountId=acc_af2ec0bed14157aa`，紧接着 `gateway_account_circuit_blocked accountId=acc_af2ec0bed14157aa phase=SUSPECT`（30 秒节流，累计 292 条）。

### 持久化事实

- Redis 熔断运行态（`juhe-ai:{prod:circuit}:account-circuit:gateway-account-circuit:states`）中**没有**该账户的 scope 条目。CLOSED 运行态默认只保留 5 分钟（`DefaultClosedRetentionMs`）。
- PG `juhe_business.account_circuit_incidents` 该 scope 行：
  `state=CLOSED, generation=20, dispatch_revision=12, ledger_revision=41, retained_until_ms=1794037193173`（已过期）；账户行 `accounts.dispatch_revision=13`。
- 该 scope 的 `account_circuit_outbox`：
  - 主投影行（`account_circuit_runtime_v1`）已 `dispatched`；
  - J3b 行（`account_circuit_runtime_j3b_v1`）长期 `status=processing`，`attempt_count≈900` 且持续增长，`claim_until_ms` 每轮刷新，`last_error_class` 恒空，`acknowledged_at_ms` 恒空。
- jobs 每几秒输出「账户 circuit 修复性维护完成 scanned=1 staleSkipped=1 cleanedIncidents=0」。需要区分两个不同滞留面：
  - CLOSED 墓碑（本账户）不在 orphanclose 的扫描面（只扫 `state<>'CLOSED'`），它被 Cleanup 的 `noPendingOutbox` 条件挡住，无法删除；
  - `scanned=1 staleSkipped=1` 对应的是另一条非 CLOSED 活动行 `acc_86fc2656a0a2adbc`（`RECOVERING, gen=3, incident_dr=29, account_dr=33`），属同族但独立的滞留缺口（见 §修复方案 P2）。

## 根因分析

### P0-1：J3b outbox 永不 ack，CLOSED 墓碑永不清理（直接根因）

1. J3b 投影器装配在 gateway（`backend-go/projects/gateway/cmd/juhe-ai-gateway/main.go:341`），按 `J3bProjectionKey` 认领事件（`internal/business/circuit_projector/service.go:72`），投影成功后调用 `AcknowledgeOutbox`（`service.go:88`）。
2. `AcknowledgeOutbox` 入口硬编码拒绝非主投影键：
   `backend-go/projects/gateway/internal/business/circuit_control_plane/store.go:699-701`
   ```go
   if projectionKey != ProjectionKey {
       return false, nil
   }
   ```
   J3b 行传入 `J3bProjectionKey`，因此恒返回 `(false, nil)`：无错误、无 release、不落 `last_error_class`，行保持 `processing`，30 秒租约到期后被再次认领，`attempt_count` 持续增长。
3. 该行为直接违反权威设计 `docs/functions/AI账户短窗口热质量与精准切号设计.md` §7.5 DR-33：“两侧消费者只认领自己的键……主消费者（jobs control-plane）与 J3b 消费者各自 ack”。既有契约测试也断言 J3b ack 必须成功（`circuit_control_plane/store_test.go:43-45`、`circuit_projector/wk_projector_test.go:248`），但这些测试依赖 `JUHE_AI_TEST_EXTERNAL=1` 的真实 PG，日常回归默认跳过，未能在发布前暴露该契约破坏。
4. jobs 的 CLOSED 墓碑清理条件（`backend-go/projects/jobs/internal/circuitstore/controlplane.go:843-844`）：
   ```sql
   NOT EXISTS (SELECT 1 FROM account_circuit_outbox o
               WHERE o.circuit_scope_key = incidents.circuit_scope_key
                 AND o.status <> 'dispatched')
   ```
   不区分 `projection_key`；卡在 `processing` 的 J3b 行因此永久阻断墓碑删除。

### P0-2：readiness 把“持久化写积压”误判为“运行态未知”，形成自锁（放大器）

1. `CircuitService.PrepareAttempt` 先查 `isRuntimeStateReady`（`internal/gatewaycircuit/service.go:598-617`）；未就绪且 `EnsureRuntimeStateReady` 仍失败时，合成一个 `Phase=SUSPECT`、`FailureReason=runtime_state_rebuilding` 的状态直接返回 `PrepareBlocked`。
2. `Bridge.IsAccountReady` 除了检查账户是否已加载，还检查 `persistenceFailures`（`bridge.go:606-627`）；`performAccountLoad` 即使已成功从 PG 重建事故，也会因为该标记存在返回 `account_load_persistence_failure`（`bridge.go:919-961`）。
3. `persistenceFailures` 的唯一清除点是 `drainScope` 的持久化成功路径（`bridge.go:1387-1392`）；而持久化成功需要新的状态转换，新转换又只能来自被 readiness 拦截的派发链路。于是形成闭环：
   **无 attempt → 无成功持久化 → 标记永不清除 → 永远无 attempt**。
4. 该行为与设计 §7.5 的交接不变量 7 冲突：“必须靠半开 / 后台复测把它们拉回，而不是让所有请求继续打或全部永久死亡”。设计只要求“无法确认运行态”时 fail-closed，并未要求“持久化写积压”也永久 fail-closed。

### P1：generation 与 dispatch_revision 的栅栏错位（复发面）

1. Redis 条目缺失时，Lua `suspect` 把 generation 从 1 开始（`backend-go/shared/platform/circuitstate/lua.go:379`），并携带当前 `accounts.dispatch_revision=13`。
2. 控制面 CAS 的 generation 栅栏为 `found && current.Generation > in.Generation → cas_conflict`（`circuit_control_plane/store.go:527-530`；jobs 侧同构 `circuitstore/controlplane.go:638`）。PG 墓碑 gen=20 > 运行时 gen=1，因此永久冲突。
3. 冲突对账 `incidentIsNewerThanRuntimeState` 先比较 dispatch_revision（`bridge.go:1716-1722`）：墓碑 DR=12 < 运行时 DR=13，直接判定“墓碑不比运行时新”，不会触发 restore；即使触发，Redis restore 也拒绝把 DR 从 13 降到 12（`lua.go:1062-1065`）。
4. 当前 P0-1/P0-2 修复后，该错位只会造成“墓碑滞留期间的一次临时冲突”，不再永久阻断；但只要墓碑因任何原因滞留超过运行时 CLOSED 保留期，仍会出现一次可观察的失败切换。需要按 §修复方案 P1 选定语义后根治。

### 结论

- 直接根因是 **J3b ack 缺失**：它让 CLOSED 墓碑在 07:39 之后仍无法删除，才使 10:46 的新 SUSPECT 与旧墓碑的 generation/DR 栅栏发生永久冲突。
- 放大器是 **readiness 自锁**：把一次可自愈的持久化冲突升级为账户级永久不可调度。
- P1 是复发面：即使 P0 修复，运行时条目过期与墓碑滞留的窗口仍可能产生临时冲突和失败切换。

## 修复方案

### 阶段 0：文档先行（实施前完成）

1. 在 `docs/functions/AI账户短窗口热质量与精准切号设计.md` §7.5 明确：
   - readiness 只表达“运行态是否已从权威事实恢复”；持久化写积压属于写侧重试/告警，不参与 readiness 判定，不得永久阻断派发。
   - 补充 generation 在运行时条目丢失后的重建/推进规则（按 P1 裁决结果）。
2. 本 bug 文档与 `docs/待办与阻塞事项.md` 条目先行登记。

### P0-1：修复 J3b ack（必须先做）

- 文件：`backend-go/projects/gateway/internal/business/circuit_control_plane/store.go:699-701`。
- 修改：`AcknowledgeOutbox` 接受 `ProjectionKey` 与 `J3bProjectionKey` 两个已注册投影键（或删除该入口守卫）。后续 `event.ProjectionKey != projectionKey`（:718）与 UPDATE 的 `projection_key=?`（:727）已提供行级围栏，入口守卫是第三层冗余且当前是唯一错误层。
- 诊断补强：`circuit_projector/service.go:88-96` 在 `ok==false && ackErr==nil` 时输出结构化 Warn（事件 id、projection_key、scope、claim_token 前缀），并把 `Failed` 计数暴露到组件健康/指标，避免同类问题再次静默。注意当前 `RunResult.Failed` 在 `main.go:356` 被 `if _, err := projector.RunOnce(...)` 直接丢弃，必须同时补日志/指标通道，单改返回值不会被上层看见。
- 预期效果：存量卡住的 J3b 行在下一轮被 ack → `dispatched` → `noPendingOutbox` 满足 → jobs Cleanup 删除 CLOSED 墓碑 → gateway 的 pending 持久化下一轮以 `Incident==nil` 走 W39 路径插入新行 → 成功 → 清除 `persistenceFailures` → 账户恢复派发（无需数据手术）。
- 测试：
  - `circuit_control_plane/store_test.go::TestOutboxProjectionFanoutIsolation`（J3b ack=true）；
  - `circuit_projector/wk_projector_test.go::TestRunOnceProjectsDispatchAndStaleIncident`（`Acknowledged==2`）；
  - 新增：J3b ack 后 jobs Cleanup 能删除同 scope 的 CLOSED 墓碑（扩展 jobs `internal/circuitstore/controlplane_t87r_outbox_cleanup_pg_test.go`；gateway 侧已无 Cleanup 实现，清理职责已收归 jobs，设计文档 §7.5 :660）；
  - 新增：ack 失败不再静默（返回/记录可观察）。

### P0-2：解除 readiness 自锁

- 文件：`internal/gatewaycircuit/bridge.go`。
- 修改（推荐）：
  - `IsAccountReady` 只在“账户权威加载未完成 / 加载失败”时返回 false，不再因 `persistenceFailures` 返回 false；
  - `performAccountLoad` 成功恢复事故后返回 true，不清空 `persistenceFailures`（保留重试与告警），但不再让它参与 readiness；
  - 为持久化积压增加可观察信号（结构化日志/指标），并保留 30 秒有界重试。
- 备选（更保守）：保留 fail-closed，但增加“积压超过 N 分钟且有界重试次数后放行 + 告警”的兜底；不推荐，因为引入魔法阈值且仍可能永久阻断。
- 行为影响：运行态是派发决策的事实源，账本写滞后不影响本进程的调度正确性；管理页/跨进程重建可能短暂看到旧账本，但运行态恢复逻辑与 CAS 围栏仍然生效。
- 测试：反转 `internal/gatewaycircuit/w14e_circuit_bridge_gaps_test.go:336-338` 的现断言；同批反转/重写 `internal/gatewaycircuit/bridge_test.go:213` `TestBridgePersistenceFailureMarksAccountUnready`（该测试直接断言 `persistenceFailures → IsAccountReady=false`，P0-2 实施后必红）；清理 `internal/gatewaycircuit/w11c_bridge_test.go:414` 中 `account_load_persistence_failure` reason 映射死代码；新增“持久化失败 → 账户权威加载成功 → 仍可派发 → 新观察顶掉旧 pending → 持久化成功 → 标记清除”全链路用例。

### P1：generation / dispatch_revision 栅栏（需用户裁决语义）

**背景**：文档的权威条款是：
- `docs/functions/AI账户短窗口热质量与精准切号设计.md:749`：「把电路从 `CLOSED` 转为 `SUSPECT`，递增 generation」——generation 跨事故轮次递增；
- 同文档 `:741`：「父 incident 和子 incident 都按 generation/revision 持久化，冷重建必须一起恢复」「关闭时保留 CLOSED tombstone」——墓碑是水位载体，运行时缺键时必须能从墓碑恢复水位；
- 同文档 `:596`：配置变化必须原子递增 `dispatchRevision`、递增 generation，旧 revision 的在途结果不得提交状态。

即文档语义是 **scope 级 generation 水位随墓碑持久化、冷重建继续使用**；当前 Lua 在条目缺失时从 1 重计（`lua.go:379`）与该语义冲突，控制面又没有提供 rebase 通道。两种修复路线：

- **方案 A（跨 DR 世代豁免）**：控制面 CAS 在 `current.DispatchRevision < in.DispatchRevision` 时跳过 expected-ledger 与 generation 栅栏，按新世代覆盖行（ledger 继续 +1）。改动最小、可自愈存量；代价是 generation 在 DR 边界重置，需要修订文档 :741/:749 的口径。gateway（`store.go:520/527`）与 jobs（`circuitstore/controlplane.go:626/638`）两侧 CAS 必须逐字一致修改。
- **方案 B（冲突时 rebase）**：冲突对账发现 `incident.Generation >= desiredState.Generation` 时，把待写状态 rebase 为 `incident.Generation+1`（保留当前 DR），用墓碑的 ledger revision 重试 CAS；成功后把 rebase 后的状态 `Restore` 回 Redis，保证后续转换从新世代继续。保持文档的全局单调语义；只在 gateway bridge 冲突臂实现（`bridge.go` 的 `persistenceFailures` 冲突分支），因为只有它能同时写回运行态。jobs `CircuitIncidentProjector` 不单独 rebase：它没有运行态写面，下一轮恢复扫描读到 gateway 写回的新 generation 后，会以新 mutation 按 scopeKey 覆盖旧 pending 收敛。需要处理 rebase 与 restore 之间的崩溃窗口（下一轮冲突可重试自愈）。

**推荐**：B（与文档 :741/:749 一致、无需改语义）；若用户接受“DR 世代边界 generation 重置”，A 更小更快。**该选择必须在实施前由用户裁决**，并同步 §7.5 文档。

**P1 回归矩阵（gateway 与 jobs 两侧同构）**：
1. `current.DR < in.DR` × `current.gen` 与 `in.gen` 的三种关系（>、=、<）：只能按新世代覆盖，旧世代迟到写被拒。
2. `current.DR == in.DR` × gen 三态：保留原 expected-ledger 与 generation 栅栏，不允许回退。
3. `current.DR > in.DR`：旧世代迟到写必须返回 stale/拒绝，不得写入。
4. 豁免/rebase 后 outbox 事件、`ledger_revision`、`projected_ledger_revision`、`retained_until_ms` 的继承正确（续 +1、不重置水位）。
5. `cas_conflict` 后 rebase → Restore → 再 CAS 的完整链路；模拟 CAS 成功后、Restore 前崩溃，下一轮可自愈且不丢新观察。
6. jobs 收敛：gateway rebase 写回运行态后，jobs 下一轮恢复扫描以新 generation mutation 覆盖旧 pending，不再保留低 generation 重放项。

### P2：登记并另行修复的滞留缺口

1. 非 CLOSED 活动行在 `dispatch_revision` 滞后时被 `orphanclose.go:249-253` 永久 `staleSkipped`，且 Cleanup 只处理 CLOSED/孤儿账户行，该行无任何清理出口（生产现存 1 行：`acc_86fc2656a0a2adbc`）。需设计第三类臂（活动行 + DR 失配 + 无运行态/无租约 → 结清或推进 DR）或明确人工处置流程。
2. J3b 契约测试默认跳过（依赖 `JUHE_AI_TEST_EXTERNAL=1` 的真实 PG），本次缺陷因此未被日常回归捕获。需在交付验证中强制运行受影响模块的真实 PG 测试，或把 ack 键校验抽成无 PG 的纯函数单测。
3. 现有生产数据：修复发布后无需手工改 PG；若希望在发布前解除当前账户（不推荐、需授权），只能重启 gateway 清进程态，且重启后首次失败可能再次进入同一窗口（墓碑仍在），因此不建议单独重启。

## 发布与回滚

- 发布形态：gateway + jobs 同批镜像（J3b ack 在 gateway；Cleanup/orphanclose 在 jobs）；无 schema 变更，无需 maintenance upgrade。
- 回滚：回退旧镜像即可。旧镜像会回到“J3b 不 ack + readiness 自锁”的现状，不引入更差状态；已 dispatched 的 J3b 行与已被 Cleanup 删除的墓碑对新旧镜像都无副作用（旧镜像仍按主键认领，J3b 行不再阻塞）。
- 部署文档：本次不改变 env、compose、运行时目录或初始化序列，按 AGENTS.md 的触发范围不需要同步 `docs/deploy/`；若实施中新增了任何配置项（预期不需要），再按部署变更同步规则处理。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 编译 | gateway 全模块构建 | `go build ./...`（`backend-go/projects/gateway`） | 通过 | 通过 | 通过 |
| 代码级复现 | J3b ack 契约 | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/business/circuit_control_plane/... -run TestOutboxProjectionFanoutIsolation -count=1` | 修复前 J3b ack=false；修复后通过 | 通过（0.42s） | 通过 |
| 代码级复现 | J3b 投影 RunOnce 全量 ack | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/business/circuit_projector/... -run TestRunOnceProjectsDispatchAndStaleIncident -count=1` | `Acknowledged==2` | 通过 | 通过 |
| 回归 | J3b ack 后不被重复认领 | `TestRunOnceDoesNotReplayAcknowledgedJ3bRows`（真实 PG） | 第一轮 ack=1，第二轮 claim=0 | 通过 | 通过 |
| 回归 | ack 未命中仍静默 | `TestAcknowledgeOutboxMissIsSilent`（真实 PG） | 错 token `(false,nil)`、正确 token `(true,nil)` | 通过 | 通过 |
| 回归 | readiness 不再因持久化积压永久拦截 | `go test ./internal/gatewaycircuit/... -count=1` | 新断言通过，旧断言已按方案更新 | 通过（10.2s） | 通过 |
| 回归 | rebase 边界矩阵与冲突收敛 | `TestBug0359RebaseDesiredStateOnIncident` / `TestBug0359PersistRebasesExpiredRuntimeGeneration` / `TestBug0359PersistRebaseRestoreCapacityIsReported` | 6 类边界按预期；CAS 重试 gen=21、expected ledger=41；容量不足响亮失败 | 通过 | 通过 |
| 回归 | jobs 墓碑被 J3b processing 行阻断、ack 后删除 | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/circuitstore/... -run TestT87R -count=1` | 阻断时 DeletedIncidents=0；ack 后=1 | 通过（0.26s） | 通过 |
| 生产验证 | 发布后 oai 恢复请求 | 发布 `dev-15de4f1dd-dirty`（schema 21，2026-11-08 02:12 拉起）后观察日志与 usage | 拦截消失、恢复派发 | `circuit_blocked`/`persist_failed` 0；`gateway_dispatch_decision` 重新选中该账户，usage 恢复（02:12:05 起） | 通过 |
| 生产验证 | outbox/墓碑清理 | 查 `account_circuit_outbox` J3b 行与 `account_circuit_incidents` | J3b 行 `dispatched`，CLOSED 墓碑被清理或新事故行 DR=13 | 卡死的 `:j3b` 行全部 `dispatched` 且已 ack；CLOSED 墓碑已清理（0 行）；锁死窗口成功后复位 `LOCKED_IDLE`；gateway ERROR 0 | 通过 |

验证备注：
- 真实 PG 测试通过 `pgtestkit` 连接 `192.168.1.203` dev 实例的隔离测试库（`JUHE_AI_TEST_EXTERNAL=1` 授权门控），不触碰生产。
- 同批修正了三处测试基建漂移（均在 gateway 测试内）：`wk_projector_test.go` 的 outbox DDL 缺少生产 `UNIQUE(projection_key, dedupe_key)`；`store_test.go` 两处 `?` 占位符在 PG 下恒语法错误；`w9e_store_test.go` 插入行用了错误投影键与不完整 incident 事件形态。它们此前被“测试默认跳过 / ack 恒失败”掩盖，属于本次验证暴露的测试债务。
- 未覆盖（已登记 T182 P2）：rebase 与真实 PG CAS 的组合链路、applied 后 Restore 前崩溃的显式用例、普通 CI 无 `JUHE_AI_TEST_EXTERNAL` 时契约测试默认跳过。

## 复发记录

- 2026-11-07：首次生产触发（oai 账户 4 小时+ 不可调度）。同机制可能影响其他曾有熔断事故的账户；当前仅 oai 出现完全拦截，`acc_86fc2656a0a2adbc` 存在同族滞留行（非 CLOSED）。

## 下次遇到

- 先查什么：`gateway_account_circuit_persist_failed` 的 `scopeKey` 与重复间隔；对应 `account_circuit_outbox` 是否存在 `projection_key=..._j3b_v1 AND status='processing'`；`account_circuit_incidents` 的 `state/generation/dispatch_revision` 与 `accounts.dispatch_revision` 是否失配。
- 重点看什么：`AcknowledgeOutbox` 是否对 J3b 键返回 `(false,nil)`；`performAccountLoad` 是否因 `persistenceFailures` 返回 `account_load_persistence_failure`。
- 如何避免误判：`phase=SUSPECT` 不一定是真实熔断，可能是 readiness 合成的 `runtime_state_rebuilding`；先确认 Redis 是否有该 scope 的真实状态。

## 完成总结

- 完成时间：代码实施与测试 2026-11-08；2026-11-08 随总发布上线（`dev-15de4f1dd-dirty`，schema 17→21，备份 `juhe_ai-pre-upgrade-20261108-0118.dump` 680MB）并于 02:12 拉起后完成生产验证
- 结论：P0-1（J3b ack）、P0-2（readiness 解耦）、P1-B（generation rebase）生产验证全部达成——该 scope `persist_failed`/`circuit_blocked` 0，`oai.ycxlg.com-e4ayee` 恢复派发与 usage、锁死窗口成功后复位 `LOCKED_IDLE`，卡死的 J3b outbox 行全部 `dispatched` 并 ack，CLOSED 墓碑已清理；gateway ERROR 0
- 后续建议：P2 滞留行清理、契约测试门禁、rebase×真实 PG 组合与 Restore 前崩溃用例移交 #T183；发布窗暴露的 `deploy.sh upgrade` 缺陷见 #T184
