# Codex Responses 协议防火墙核心与性能 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立版本化、可归因、增量执行的 Codex Responses contract validator 与安全修复器，在不改变正文语义的前提下保护 JSON/SSE 输出，并满足 clean 路径性能门禁。

**Architecture:** contract registry 作为类型/事件规则唯一事实；validator 只产出诊断，repair planner 只选择 R0，executor 只在副本上修改允许 ID 路径。响应检查分别接在 raw upstream 与 bridge 后输出边界，SSE 使用按 output identity 增量的状态机，不缓存完整流。

**Tech Stack:** Node.js、TypeScript、OpenAI Responses JSON/SSE、现有 response inspection / pre-commit stream retry、pnpm/tsx。

---

## 文件结构

- `backend/src/modules/gateway/codex-responses/contract-registry.ts`：contract revision、item 前缀、事件阶段和允许修复路径。
- `backend/src/modules/gateway/codex-responses/contract-types.ts`：诊断、provenance、outcome、repair plan 类型。
- `backend/src/modules/gateway/codex-responses/contract-validator.ts`：JSON / item 结构检查。
- `backend/src/modules/gateway/codex-responses/stream-contract-state.ts`：SSE 增量状态机。
- `backend/src/modules/gateway/codex-responses/repair-planner.ts`：只生成 R0 plan。
- `backend/src/modules/gateway/codex-responses/repair-executor.ts`：copy-on-write、局部不变量与二次验证。
- `backend/src/modules/gateway/codex-responses/response-guard.ts`：JSON/SSE 适配层和双检查点入口。
- `backend/src/modules/gateway/response/non-stream-json-inspection.ts`：接入 JSON guard。
- `backend/src/modules/gateway/response/stream.ts`：接入 SSE guard 和 pre-commit 拦截结果。
- `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`：bridge 后检查点 B。
- `backend/src/scripts/regression/codex-responses-contract-registry-regression.ts`：registry 与 Codex fixture 回归。
- `backend/src/scripts/regression/codex-responses-contract-json-regression.ts`：JSON 诊断与 R0 回归。
- `backend/src/scripts/regression/codex-responses-contract-sse-regression.ts`：SSE 生命周期与 commit 边界回归。
- `backend/src/scripts/performance/codex-responses-contract-guard-performance.ts`：clean JSON/SSE 负载基准。

## Task 1: 建立 contract registry 与离线 fixture

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/contract-types.ts`
- Create: `backend/src/modules/gateway/codex-responses/contract-registry.ts`
- Create: `backend/src/scripts/regression/codex-responses-contract-registry-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写失败的所有已知类型 fixture 回归**

fixture 明确覆盖 `at/msg/amsg/rs/lsh/fc/tsc/fco/ctc/ctco/tso/ws/ig/cmp`。测试断言：

```ts
assert.equal(contractRevision, 'codex-responses-2026-07-11-r1')
assert.equal(registry.item('custom_tool_call').prefix, 'ctc')
assert.equal(registry.item('context_compaction').prefix, 'cmp')
assert.equal(registry.item('other'), undefined)
```

fixture 注释记录 Codex 源码 commit `1bbdb32789e1f79932df44941236ea3658f6e965` 与 `models.rs:id_prefix()` 来源。

- [ ] **Step 2: 运行并确认模块不存在**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-registry-regression.ts`

Expected: FAIL，原因是 registry 导出不存在。

- [ ] **Step 3: 实现只读 registry**

```ts
export type CodexContractRevision = 'codex-responses-2026-07-11-r1'

export interface CodexItemContract {
  type: string
  prefix?: string
  eventStages: readonly ('added' | 'delta' | 'done')[]
  repairableIdPaths: readonly string[]
}

export const codexResponsesContractRegistry = createCodexResponsesContractRegistry(
  'codex-responses-2026-07-11-r1',
  [/* fixture table */]
)
```

未知 type 不写入 registry；caller 必须将其作为 `unknown_item_type`，而不是转换为 `other`。

- [ ] **Step 4: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-registry-regression.ts
pnpm --filter juhe-ai-backend typecheck
git add -- backend/src/modules/gateway/codex-responses/contract-types.ts backend/src/modules/gateway/codex-responses/contract-registry.ts backend/src/scripts/regression/codex-responses-contract-registry-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): add versioned Codex Responses contract registry"
```

## Task 2: JSON validator、R0 repair plan 和 executor

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/contract-validator.ts`
- Create: `backend/src/modules/gateway/codex-responses/repair-planner.ts`
- Create: `backend/src/modules/gateway/codex-responses/repair-executor.ts`
- Create: `backend/src/scripts/regression/codex-responses-contract-json-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 写失败的 JSON 契约与语义不变量回归**

覆盖 `custom_tool_call + fc_*`、重复 item identity、未知 item type、orphan output、完整可重放历史带错误 ID。断言：

```ts
assert.deepEqual(result.issues.map((issue) => issue.code), ['item_id_prefix_mismatch'])
assert.equal(plan.level, 'R0')
assert.equal(repaired.output[0].id, 'ctc_generated_1')
assert.equal(repaired.output[0].call_id, before.call_id)
assert.equal(semanticProjection(repaired), semanticProjection(before))
assert.throws(() => executeRepair(unrecoverablePlan), /codex_history_item_unrecoverable/)
```

`semanticProjection` 删除 only-allowed ID paths 后比较，不能包含正文替换或全文 hashing。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-json-regression.ts`

Expected: FAIL，原因是 validator/planner/executor 不存在。

- [ ] **Step 3: 实现 validator 与 provenance 输入**

```ts
export type CodexProtocolIssueProvenance =
  | 'request_history'
  | 'raw_upstream'
  | 'gateway_bridge'
  | 'unknown'

export function validateCodexResponsesJson(input: {
  response: Record<string, unknown>
  provenance: CodexProtocolIssueProvenance
  revision: CodexContractRevision
}): CodexContractValidationResult
```

validator 只读取结构字段并返回 issue；不修改 input、不判断物理模型身份、不调用账号运行态。

- [ ] **Step 4: 实现 R0 planner/executor**

planner 只允许：删除请求历史 ID；在下游尚未暴露的响应 item 上按已确定 type 生成新 client ID；同一 item 生命周期的纯引用字段同步改写。任何工具类型猜测、call_id、正文、usage、reasoning 变化生成 `R2 forbidden`，不执行。

executor 必须：只复制被 plan 覆盖的 item；执行后完整再校验；验证 item 数、顺序、type、call_id、output_index 与语义投影不变。

- [ ] **Step 5: 运行转绿与已有 Responses 回归**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-json-regression.ts
pnpm --filter juhe-ai-backend run test:codex-latest-compatibility
pnpm --filter juhe-ai-backend run test:gateway-response-lifecycle
pnpm --filter juhe-ai-backend typecheck
```

Expected: 全部退出码 0。

- [ ] **Step 6: 提交本任务**

```powershell
git add -- backend/src/modules/gateway/codex-responses/contract-validator.ts backend/src/modules/gateway/codex-responses/repair-planner.ts backend/src/modules/gateway/codex-responses/repair-executor.ts backend/src/scripts/regression/codex-responses-contract-json-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): validate and safely repair Codex response items"
```

## Task 3: SSE 增量状态机与语义提交边界

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/stream-contract-state.ts`
- Create: `backend/src/scripts/regression/codex-responses-contract-sse-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 写失败的流式生命周期回归**

使用完整事件 fixture，覆盖 added/delta/done/completed、type/ID 中途变更、不同 output index 重复 ID、semantic commit 前后。断言：

```ts
assert.equal(consume(added).outcome, 'clean')
assert.equal(consume(changedIdDelta).issue?.code, 'event_item_id_inconsistent')
assert.equal(consume(duplicateOtherIndex).issue?.code, 'duplicate_item_identity')
assert.equal(guard.canTransparentRetry({ semanticCommitted: false }), true)
assert.equal(guard.canTransparentRetry({ semanticCommitted: true }), false)
```

也覆盖 SSE comment heartbeat：只提交 transport 时保持可 retry。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-sse-regression.ts`

Expected: FAIL，原因是流式状态机不存在。

- [ ] **Step 3: 实现 O(1) 事件状态机**

内部键固定为 `responseResourceId + outputIndex`：

```ts
interface CodexStreamItemIdentity {
  itemId?: string
  itemType?: string
  callId?: string
  outputIndex: number
  stage: 'added' | 'delta' | 'done'
}
```

Map 仅保存身份字段与固定数量的诊断，禁止保存正文或完整事件。每个事件只读取协议字段并返回 `clean`、`repairable`、`blocked` 或 `observed_unknown`。

- [ ] **Step 4: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-contract-sse-regression.ts
pnpm --filter juhe-ai-backend run test:gateway-response-lifecycle-http
pnpm --filter juhe-ai-backend typecheck
git add -- backend/src/modules/gateway/codex-responses/stream-contract-state.ts backend/src/scripts/regression/codex-responses-contract-sse-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): track Codex SSE item identity incrementally"
```

## Task 4: 接入 raw upstream / bridge 双检查点

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/response-guard.ts`
- Modify: `backend/src/modules/gateway/response/non-stream-json-inspection.ts`
- Modify: `backend/src/modules/gateway/response/stream.ts`
- Modify: `backend/src/modules/gateway/response/finalization.ts`
- Modify: `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`
- Create: `backend/src/scripts/regression/codex-responses-provenance-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 写双检查点和 pre-commit 红灯回归**

构造三条链路：raw native 响应已经错误、raw Chat 正常但 bridge 输出错误、未知新 item。断言：

```ts
assert.equal(nativeResult.provenance, 'raw_upstream')
assert.equal(bridgeResult.provenance, 'gateway_bridge')
assert.equal(unknownResult.outcome, 'observed_unknown')
assert.equal(preCommitBadResult.retryable, true)
assert.equal(postCommitBadResult.outcome, 'late_violation')
```

严格策略尚未在本阶段启用；这里只验证 guard 把结果传入现有 pre-commit retry 决策，不更改账号状态。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-provenance-regression.ts`

Expected: FAIL，原因是双检查点 guard 不存在。

- [ ] **Step 3: 接入 JSON 与 SSE guard**

`response-guard.ts` 的入口接收 `checkpoint: 'raw_upstream' | 'gateway_bridge'`。raw native Responses 放在上游原始响应解析前；Chat bridge 仅在转换结果写给客户端前调用 bridge checkpoint。guard 返回现有 stream/non-stream inspection 能承载的失败或观察结果，复用 `DownstreamCommitState`，不自建第二套 commit 标记。

- [ ] **Step 4: 接入最终审计元数据但不加使用记录字段**

把 `contractRevision`、provenance、issue code、修复规则 ID 附在现有 audit metadata 结构，数量使用现有 observation 上限。P2 再把它持久化到 usage record。

- [ ] **Step 5: 运行转绿**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-provenance-regression.ts
pnpm --filter juhe-ai-backend run test:response-inspection-gateway-e2e
pnpm --filter juhe-ai-backend run test:gateway-response-lifecycle
pnpm --filter juhe-ai-backend typecheck
```

Expected: 全部退出码 0；bridge 诊断不写任何账户失败/禁用状态。

- [ ] **Step 6: 提交本任务**

```powershell
git add -- backend/src/modules/gateway/codex-responses/response-guard.ts backend/src/modules/gateway/response/non-stream-json-inspection.ts backend/src/modules/gateway/response/stream.ts backend/src/modules/gateway/response/finalization.ts backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts backend/src/scripts/regression/codex-responses-provenance-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): inspect Codex responses at upstream and bridge boundaries"
```

## Task 5: shadow 性能门禁与紧急降级

**Files:**
- Create: `backend/src/scripts/performance/codex-responses-contract-guard-performance.ts`
- Modify: `backend/src/config/runtime.ts`
- Modify: `backend/.env.example`
- Modify: `docs/develop/运行说明.md`
- Modify: `docs/develop/测试与验证说明.md`

- [ ] **Step 1: 实现 runtime mode**

新增仅服务端环境变量：

```ts
type CodexProtocolGuardGlobalMode = 'off' | 'shadow' | 'safe_repair'
```

默认 `shadow`。`off` 只在紧急回退使用，仍保留 P0 请求历史 sanitizer；`safe_repair` 只执行 R0。配置解析使用现有严格枚举模式，非法值启动失败。

- [ ] **Step 2: 编写性能/功能回归**

基准分别跑 guard off/shadow/safe_repair，输出 p50/p95、每秒 item 数、诊断数量、heap 增量。断言 clean JSON/SSE 不深拷贝、不积累完整流事件；item 数增加 10 倍时扫描成本不得出现二次增长特征。

- [ ] **Step 3: 运行 shadow 门禁**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/performance/codex-responses-contract-guard-performance.ts
pnpm --filter juhe-ai-backend run test:codex-responses-contract-registry
pnpm --filter juhe-ai-backend run test:codex-responses-contract-json
pnpm --filter juhe-ai-backend run test:codex-responses-contract-sse
pnpm --filter juhe-ai-backend run test:codex-responses-provenance
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-backend build
```

Expected: 全部退出码 0，并输出本机对比数据；没有数据不能从 shadow 升级到 safe_repair。

- [ ] **Step 4: 提交本任务**

```powershell
git add -- backend/src/scripts/performance/codex-responses-contract-guard-performance.ts backend/src/config/runtime.ts backend/.env.example docs/develop/运行说明.md docs/develop/测试与验证说明.md backend/package.json
git diff --cached --check
git commit -m "feat(gateway): add shadow mode for Codex protocol guard"
```

## 核心阶段验收

- [ ] registry 覆盖当前 Codex 已知 item 前缀，未知类型不被错误转换。
- [ ] validator、planner、executor 分离；executor 只执行 R0。
- [ ] JSON/SSE 只有结构扫描进入 clean 热路径；无全文重解析、无完整流缓存。
- [ ] raw upstream 与 gateway bridge provenance 可区分。
- [ ] semantic commit 前可交由既有重试机制处理，之后只产生 late violation。
- [ ] shadow 有可观测结果，safe repair 有紧急全局降级。
