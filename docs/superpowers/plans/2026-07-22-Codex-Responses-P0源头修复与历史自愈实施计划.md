# Codex Responses P0 源头修复与历史自愈 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除网关已确认会自行制造的 `custom_tool_call + fc_*` 和 bridge 合成 item ID 跨账号重放问题，并以失败优先方式保护不可恢复历史。

**Architecture:** 在 Chat->Responses bridge 内先确定工具 adapter 类型再创建 wire ID；在每个账号的上游请求准备阶段，根据目标持久化作用域对本次 Responses input 做 copy-on-write 历史自愈。P0 不引入账户开关、响应拦截或账号处罚，只修确定性根因并记录有界诊断。

**Tech Stack:** Node.js、TypeScript、Express、OpenAI Responses JSON/SSE、pnpm/tsx 回归脚本。

---

## 文件结构

- `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge-tool-identity.ts`：根据 adapter kind 创建稳定 item ID、call ID 和事件身份。
- `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`：接入工具身份，不再预先写死 `fc_*`。
- `backend/src/modules/gateway/codex-responses/request-history-sanitizer.ts`：纯函数扫描和 copy-on-write 删除不安全 item ID。
- `backend/src/modules/gateway/codex-responses/request-history-types.ts`：目标持久化作用域、诊断和结果类型。
- `backend/src/modules/gateway/codex-responses/chat-bridge-state.ts`：bridge -> native 渲染时接入 sanitizer，不再直接重放 wire ID。
- `backend/src/modules/gateway/dispatch/account-preparation.ts`：每个账号 attempt 确定目标作用域后执行 sanitizer。
- `backend/src/scripts/regression/codex-responses-tool-item-identity-regression.ts`：function/custom JSON 与 SSE 精确前缀回归。
- `backend/src/scripts/regression/codex-responses-history-sanitizer-regression.ts`：跨账号、`store=false`、不可恢复历史和幂等回归。
- `backend/src/scripts/regression/codex-responses-bridge-native-switch-regression.ts`：真实网关 bridge -> native mock E2E。
- `backend/package.json`：登记三个测试入口。

## Task 1: 固定 function/custom 工具身份生成顺序

**Files:**
- Create: `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge-tool-identity.ts`
- Modify: `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts:77`
- Create: `backend/src/scripts/regression/codex-responses-tool-item-identity-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写失败的工具身份回归**

测试通过现有公开 bridge 入口分别发送 function 与 custom 工具，消费上游 Chat SSE 后解析 Responses SSE/JSON，使用以下断言：

```ts
assert.match(functionAdded.item.id, /^fc_/)
assert.equal(functionAdded.item.type, 'function_call')
assert.match(customAdded.item.id, /^ctc_/)
assert.equal(customAdded.item.type, 'custom_tool_call')
assert.equal(customDone.item.id, customAdded.item.id)
assert.equal(customDone.item.call_id, customAdded.item.call_id)
assert.equal(customDone.output_index, customAdded.output_index)
```

同一 fixture 还要断言非流式 `response.output[]` 使用相同规则，并精确复现当前失败形态：修复前 custom ID 为 `fc_*`。

- [ ] **Step 2: 运行测试并确认红灯来自当前缺陷**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-tool-item-identity-regression.ts
```

Expected: FAIL，失败断言必须是 custom item ID 不以 `ctc_` 开头；测试启动、SSE 解析或 fixture 错误不算有效红灯。

- [ ] **Step 3: 抽取类型先行的身份创建器**

新增稳定接口：

```ts
export interface CodexBridgeToolIdentityInput {
  adapterKind: 'function' | 'custom'
  idPrefix: string
  index: number
  upstreamCallId?: string
  suffix: string
}

export interface CodexBridgeToolIdentity {
  itemId: string
  callId: string
  itemType: 'function_call' | 'custom_tool_call'
}

export function createCodexBridgeToolIdentity(
  input: CodexBridgeToolIdentityInput
): CodexBridgeToolIdentity {
  const itemPrefix = input.adapterKind === 'custom' ? 'ctc' : 'fc'
  return {
    itemId: `${itemPrefix}_${input.idPrefix}_${input.index}_${input.suffix}`,
    callId: input.upstreamCallId ?? `call_${input.idPrefix}_${input.index}_${input.suffix}`,
    itemType: input.adapterKind === 'custom' ? 'custom_tool_call' : 'function_call'
  }
}
```

suffix 在调用点创建一次并复用，不能分别调用 `Date.now()` 造成 item/call 身份漂移。

- [ ] **Step 4: 改造 bridge 工具状态**

`ChatToolCallState` 增加已确定的 `itemType`，删除“默认 function、以后再变 custom”的语义：

```ts
interface ChatToolCallState {
  id: string
  itemType: 'function_call' | 'custom_tool_call'
  callId: string
  name: string
  arguments: string
  adapter: CodexResponsesChatBridgeToolAdapter
  outputIndex: number
  added: boolean
  done: boolean
}
```

只有解析到非空 Chat tool name 且能在 `toolAdaptersByChatName` 找到 adapter 后才创建 state 和发 `output_item.added`。上游返回未声明工具名时显式失败为 `codex_bridge_unknown_tool_call`，不得默认为 function。

- [ ] **Step 5: 运行工具身份回归与相关 bridge 回归**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-tool-item-identity-regression.ts
pnpm --filter juhe-ai-backend run test:codex-turn-switch-e2e
pnpm --filter juhe-ai-backend typecheck
```

Expected: 全部退出码 0；function/custom added/done/JSON output 的前缀、类型、call_id、output_index 均稳定。

- [ ] **Step 6: 提交本任务**

```powershell
git add -- backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge-tool-identity.ts backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts backend/src/scripts/regression/codex-responses-tool-item-identity-regression.ts backend/package.json
git diff --cached --check
git commit -m "fix(gateway): generate Codex tool IDs by item type"
```

## Task 2: 实现纯函数请求历史 sanitizer

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/request-history-types.ts`
- Create: `backend/src/modules/gateway/codex-responses/request-history-sanitizer.ts`
- Create: `backend/src/scripts/regression/codex-responses-history-sanitizer-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写失败的 sanitizer 表格回归**

使用表格 fixture 覆盖：

```ts
const cases = [
  { type: 'custom_tool_call', id: 'fc_bad', replayable: true, expectId: undefined },
  { type: 'custom_tool_call', id: 'ctc_good', replayable: true, expectId: 'ctc_good' },
  { type: 'reasoning', id: 'rs_unstored', replayable: true, store: false, expectId: undefined },
  { type: 'function_call_output', id: 'fco_x', call_id: 'call_x', replayable: true, crossScope: true, expectId: undefined },
  { type: 'reasoning', id: 'rs_only', replayable: false, crossScope: true, expectError: 'codex_history_item_unrecoverable' }
]
```

每个成功 fixture 断言 `call_id`、正文、reasoning、工具 input/output、item 数量和顺序不变；clean input 断言结果 `items === input`，证明零拷贝。

- [ ] **Step 2: 运行测试并确认因模块不存在失败**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-history-sanitizer-regression.ts`

Expected: FAIL，原因是 sanitizer 导出不存在。

- [ ] **Step 3: 定义输入、诊断和结果类型**

```ts
export type ResponsesPersistenceScope =
  | 'none'
  | 'account'
  | 'upstream_bucket'
  | 'provider_global'
  | 'websocket_connection'

export interface CodexHistorySanitizerContext {
  store: boolean
  sourceScopeKey?: string
  targetScopeKey?: string
  targetPersistenceScope: ResponsesPersistenceScope
  contractRevision: string
}

export interface CodexHistorySanitizerResult {
  items: unknown[]
  changed: boolean
  removedIdCount: number
  issueCodes: string[]
}
```

不可恢复历史抛出 `GatewayRequestValidationError`，code 固定为 `codex_history_item_unrecoverable`。

- [ ] **Step 4: 实现单遍扫描和 copy-on-write**

规则只允许删除 `id`：

```ts
if (!mustStripId(item, context)) return item
if (!hasReplayablePayload(item)) throw unrecoverableHistoryError(item.type)
changed = true
removedIdCount += 1
const { id: _removed, ...copy } = item
return copy
```

`mustStripId` 在以下任一条件成立时返回 true：已知类型前缀不匹配；`store=false` 且目标 scope 为 `none`；source/target scope key 不同；legacy ID 没有合法前缀。不得修改 `call_id`，不得删除完整 item。

- [ ] **Step 5: 运行回归、幂等和类型检查**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-history-sanitizer-regression.ts
pnpm --filter juhe-ai-backend typecheck
```

Expected: 退出码 0；第二次 sanitizer 的 `changed=false`，clean input 保持引用相等。

- [ ] **Step 6: 提交本任务**

```powershell
git add -- backend/src/modules/gateway/codex-responses/request-history-types.ts backend/src/modules/gateway/codex-responses/request-history-sanitizer.ts backend/src/scripts/regression/codex-responses-history-sanitizer-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): sanitize Codex response history IDs"
```

## Task 3: 在账号 attempt 边界接入历史自愈

**Files:**
- Modify: `backend/src/modules/gateway/codex-responses/chat-bridge-state.ts:596`
- Modify: `backend/src/modules/gateway/dispatch/account-preparation.ts:233`
- Modify: `backend/src/modules/gateway/protocols/openai-v1/api-key-client-compatibility.ts:111`
- Modify: `backend/src/modules/gateway/adapters/gpt-codex/oauth-normalizer.ts:50`
- Create: `backend/src/scripts/regression/codex-responses-bridge-native-switch-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写 bridge -> native E2E 红灯回归**

启动本地网关和两个 mock 账号：第一次强制 Chat bridge 产生 reasoning、message、custom tool；第二次携带 internal `previous_response_id` 强制切到原生 Responses mock。原生 mock 断言：

```ts
for (const item of requestBody.input) {
  if (['reasoning', 'message', 'function_call', 'custom_tool_call'].includes(item.type)) {
    assert.equal(Object.hasOwn(item, 'id'), false)
  }
}
assert.equal(customCall.call_id, originalCallId)
assert.equal(customCall.input, originalInput)
```

再发送只有 `id`、无可重放负载的 item，断言上游未命中且本地返回 400 / `codex_history_item_unrecoverable`。

- [ ] **Step 2: 运行 E2E 并确认当前会把合成 ID 发给 native mock**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-bridge-native-switch-regression.ts`

Expected: FAIL，mock 收到 `rs_chat_bridge_*`、`msg_chat_bridge_*` 或 tool item ID。

- [ ] **Step 3: 在每个账号 attempt 渲染后接入 sanitizer**

`buildPreparedUpstreamRequestParts()` 在 `prepareCodexResponsesContextForAccount()` 之后、driver 序列化之前，构造目标上下文：

```ts
const context: CodexHistorySanitizerContext = {
  store: false,
  sourceScopeKey: codexResponsesSourceScopeKey(req),
  targetScopeKey: codexResponsesTargetScopeKey(account),
  targetPersistenceScope: codexResponsesPersistenceScope(account),
  contractRevision: 'codex-responses-2026-07-11-r1'
}
```

只对 `clientProfile=codex + endpointFamily=responses` 生效。第一版 OpenAI-compatible HTTP/SSE 默认 scope 为 `none`，不得从上游响应字段自动升级。

- [ ] **Step 4: 删除 bridge/native 两套隐式 ID 规则重复**

`chat-bridge-state.ts` 继续负责 materialize 与 internal compaction 转换，但不自行做另一套 prefix 判断；所有普通 item ID 清洗统一调用 sanitizer。OAuth 和 API Key normalizer 只保留字段兼容，不复制 sanitizer 逻辑。

- [ ] **Step 5: 运行定向与现有状态恢复回归**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-responses-bridge-native-switch-regression.ts
pnpm --filter juhe-ai-backend run test:codex-context-state-writer-pool
pnpm --filter juhe-ai-backend run test:codex-turn-switch-e2e
pnpm --filter juhe-ai-backend run test:openai-api-key-passthrough
pnpm --filter juhe-ai-backend run test:openai-oauth-codex-adapter
pnpm --filter juhe-ai-backend typecheck
```

Expected: 全部退出码 0；不可恢复历史本地失败，未命中任一上游。

- [ ] **Step 6: 提交本任务**

```powershell
git add -- backend/src/modules/gateway/codex-responses/chat-bridge-state.ts backend/src/modules/gateway/dispatch/account-preparation.ts backend/src/modules/gateway/protocols/openai-v1/api-key-client-compatibility.ts backend/src/modules/gateway/adapters/gpt-codex/oauth-normalizer.ts backend/src/scripts/regression/codex-responses-bridge-native-switch-regression.ts backend/package.json
git diff --cached --check
git commit -m "fix(gateway): strip synthetic Codex IDs before native replay"
```

## Task 4: P0 性能与发布门禁

**Files:**
- Create: `backend/src/scripts/performance/codex-history-sanitizer-performance.ts`
- Modify: `backend/package.json`
- Modify: `docs/develop/测试与验证说明.md`

- [ ] **Step 1: 编写性能基准脚本**

生成 10、100、1000、10000 item 的 clean 与 1% dirty 序列，分别测：扫描耗时、分配字节、clean 引用相等率、dirty 复制 item 数。脚本输出 JSON：

```ts
interface SanitizerBenchmarkResult {
  itemCount: number
  dirtyRate: number
  iterations: number
  p50Ms: number
  p95Ms: number
  copiedItemCount: number
  cleanReferenceReuseRate: number
}
```

- [ ] **Step 2: 运行基准并保存本机基线**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/performance/codex-history-sanitizer-performance.ts`

Expected: 输出所有矩阵；clean `copiedItemCount=0`、引用复用率 100%，耗时随 item 数近似线性。计划不预设脱离机器的绝对毫秒门槛。

- [ ] **Step 3: 运行 P0 汇总门禁**

Run:

```powershell
pnpm --filter juhe-ai-backend run test:codex-responses-tool-item-identity
pnpm --filter juhe-ai-backend run test:codex-responses-history-sanitizer
pnpm --filter juhe-ai-backend run test:codex-responses-bridge-native-switch
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-backend build
git diff --check
```

Expected: 全部退出码 0。

- [ ] **Step 4: 记录上线与回退边界**

文档明确 P0 不依赖数据库 schema 或账户开关；回退为代码版本回退。P0 没有独立运行时关闭开关；若 sanitizer 出现误判，停止发布并回退 P0 代码，不能恢复为静默删除完整 item。

- [ ] **Step 5: 提交性能与文档**

```powershell
git add -- backend/src/scripts/performance/codex-history-sanitizer-performance.ts backend/package.json docs/develop/测试与验证说明.md
git diff --cached --check
git commit -m "test(gateway): benchmark Codex history sanitization"
```

## P0 验收

- [ ] custom tool 在 JSON/SSE 中均使用 `ctc_*`，function tool 使用 `fc_*`。
- [ ] added/done 的 ID、type、call_id、output_index 不漂移。
- [ ] bridge -> native、跨账号和 `store=false` 重放不携带合成 item ID。
- [ ] sanitizer 只删除 ID，不修改正文、reasoning、usage、工具数据或 call_id。
- [ ] 只有 ID、无可重放负载的历史显式失败。
- [ ] clean 路径零拷贝、单遍线性扫描。
- [ ] P0 可独立发布和独立回退，不处罚账号、不改变使用记录颜色。
