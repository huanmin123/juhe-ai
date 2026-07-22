# Codex Responses 账户策略、可观测性与灰度 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Codex-capable AI 账户提供默认修复与可选严格拦截，记录黄色协议成功状态，隔离异常账户的 Responses lane，并以固定账号后台探针闭环恢复。

**Architecture:** 账户凭据保存两个用户开关，后端派生三种模式；严格拦截复用现有 pre-commit retry，只有 raw upstream provenance 建立 capability lane TTL。usage 保留 success 事实并增加独立 protocol outcome；后台 ops-worker 固定账号探针维护 capability health，不改全局账户状态。

**Tech Stack:** Node.js、TypeScript、Vue 3、Ant Design Vue、SQLite/PostgreSQL usage shards、Redis/standalone runtime state、ops-worker、pnpm/tsx。

---

## 文件结构

- `backend/src/modules/gateway/codex-responses/account-policy.ts`：解析账户开关和派生模式。
- `backend/src/storage/account-credentials-normalization.ts`：保存 `codex_protocol_guard` 非敏感配置。
- `backend/src/modules/accounts/account-request.schemas.ts`：HTTP 配置边界。
- `frontend/src/views/accounts/AccountCodexProtocolGuardSection.vue`：两开关及风险提示。
- `frontend/src/views/accounts/accountFormTypes.ts`、`accountFormDefaults.ts`、`accountEditFormPayload.ts`：表单状态与 payload。
- `frontend/src/views/accounts/accountFormatters.ts`、`AccountExtraInfoSection.vue`、`AccountsView.vue`：账户列表/详情的 Codex Responses capability health 标签。
- `backend/src/modules/gateway/codex-responses/capability-health.service.ts`：lane health / TTL / probe result。
- `backend/src/modules/background/codex-protocol-health-probe.service.ts`：固定账号协议 canary。
- `backend/src/modules/background/account-probe-jobs.ts`：ops-worker 任务登记。
- `backend/src/modules/gateway/usage/records.ts`：写入 protocol outcome/diagnostics。
- `backend/src/storage/usage-records.repository.ts`、`usage-record-shards.ts`、PostgreSQL schema/migration：字段落库与查询。
- `frontend/src/types/domain/usage-records.ts`、`UsageRecordResultCell.vue`、`UsageRecordMobileCard.vue`：绿色/黄色/红色展示。
- `backend/src/scripts/regression/codex-protocol-account-policy-regression.ts`：开关默认与派生模式。
- `backend/src/scripts/regression/codex-protocol-strict-intercept-regression.ts`：拦截、切号、provenance 与 lane 隔离。
- `backend/src/scripts/regression/codex-protocol-usage-record-regression.ts`：usage 成功事实与协议 outcome。
- `backend/src/scripts/regression/codex-protocol-health-probe-regression.ts`：固定账号探针恢复。
- `frontend/src/scripts/regression/account-codex-protocol-guard-regression.ts`：账户 UI 行为。
- `frontend/src/scripts/regression/usage-record-protocol-outcome-regression.ts`：标签和 tooltip。

## Task 1: 账户配置与派生模式

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/account-policy.ts`
- Modify: `backend/src/storage/account-credentials-normalization.ts`
- Modify: `backend/src/modules/accounts/account-request.schemas.ts`
- Create: `backend/src/scripts/regression/codex-protocol-account-policy-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写失败的默认与模式回归**

断言缺少配置、三种有效组合、非法类型、非 Responses 账户：

```ts
assert.deepEqual(resolvePolicy({}), {
  responseRepairEnabled: true,
  strictInterceptEnabled: false,
  mode: 'compatibility_repair'
})
assert.equal(resolvePolicy({ responseRepairEnabled: false, strictInterceptEnabled: false }).mode, 'observe_only')
assert.equal(resolvePolicy({ responseRepairEnabled: true, strictInterceptEnabled: true }).mode, 'strict_intercept')
```

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-account-policy-regression.ts`

Expected: FAIL，原因是 policy 模块不存在。

- [ ] **Step 3: 实现配置归一与派生模式**

凭据字段：

```ts
interface CodexProtocolGuardConfig {
  revision: 1
  responseRepairEnabled: boolean
  strictInterceptEnabled: boolean
}
```

存入 `credentials.codex_protocol_guard`；默认 repair=true / intercept=false。严格拦截优先，但不覆盖保存的 repair 值。非 Codex Responses lane 忽略该配置。

- [ ] **Step 4: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-account-policy-regression.ts
pnpm --filter juhe-ai-backend run test:account-response-credentials-redaction
pnpm --filter juhe-ai-backend typecheck
git add -- backend/src/modules/gateway/codex-responses/account-policy.ts backend/src/storage/account-credentials-normalization.ts backend/src/modules/accounts/account-request.schemas.ts backend/src/scripts/regression/codex-protocol-account-policy-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(accounts): add Codex protocol guard policy"
```

## Task 2: 账户页面两开关

**Files:**
- Create: `frontend/src/views/accounts/AccountCodexProtocolGuardSection.vue`
- Modify: `frontend/src/views/accounts/AccountEditModal.vue`
- Modify: `frontend/src/views/accounts/accountFormTypes.ts`
- Modify: `frontend/src/views/accounts/accountFormDefaults.ts`
- Modify: `frontend/src/views/accounts/accountEditFormPayload.ts`
- Modify: `frontend/src/views/accounts/useAccountEditForm.ts`
- Create: `frontend/src/scripts/regression/account-codex-protocol-guard-regression.ts`
- Modify: `frontend/package.json`

- [ ] **Step 1: 编写失败的表单行为回归**

断言：Codex-capable 账户展示两开关；默认 repair 开、intercept 关；开启 intercept 后 repair 控件 disabled 但值不变；两个都关时显示风险提示；保存 payload 精确包含 revision/boolean。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/account-codex-protocol-guard-regression.ts`

Expected: FAIL，原因是组件和表单字段不存在。

- [ ] **Step 3: 实现账户高级配置区域**

UI 文案固定为：`兼容修复`、`严格拦截`。使用 Switch；严格拦截下禁用 repair 交互。风险提示只在两开关都关时显示，不加入大段功能教学文本。

- [ ] **Step 4: 转绿、类型检查和提交**

Run:

```powershell
pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/account-codex-protocol-guard-regression.ts
pnpm --filter juhe-ai-frontend run test:account-edit-save-flow
pnpm --filter juhe-ai-frontend typecheck
git add -- frontend/src/views/accounts/AccountCodexProtocolGuardSection.vue frontend/src/views/accounts/AccountEditModal.vue frontend/src/views/accounts/accountFormTypes.ts frontend/src/views/accounts/accountFormDefaults.ts frontend/src/views/accounts/accountEditFormPayload.ts frontend/src/views/accounts/useAccountEditForm.ts frontend/src/scripts/regression/account-codex-protocol-guard-regression.ts frontend/package.json
git diff --cached --check
git commit -m "feat(accounts): configure Codex response protection"
```

## Task 3: 严格拦截、切号与 capability lane 回避

**Files:**
- Create: `backend/src/modules/gateway/codex-responses/capability-health.service.ts`
- Modify: `backend/src/modules/gateway/codex-responses/response-guard.ts`
- Modify: `backend/src/modules/gateway/response/finalization.ts`
- Modify: `backend/src/modules/gateway/dispatch/candidate-filter.ts`
- Modify: `backend/src/modules/gateway/runtime/account-dispatch-priority-order.ts`
- Create: `backend/src/scripts/regression/codex-protocol-strict-intercept-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 编写失败的严格拦截矩阵**

两个账号 fixture 覆盖：raw upstream 错误后切号成功、gateway bridge 错误后切号、semantic commit 后错误、Chat 请求不受影响。断言：

```ts
assert.equal(rawViolation.firstAccountAvoidedForCodexResponses, true)
assert.equal(rawViolation.secondAccountSucceeded, true)
assert.equal(bridgeViolation.firstAccountAvoidedForCodexResponses, false)
assert.equal(lateViolation.transparentRetry, false)
assert.equal(chatRequest.firstAccountEligible, true)
```

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-strict-intercept-regression.ts`

Expected: FAIL，原因是 capability lane 状态和严格策略未接入。

- [ ] **Step 3: 实现 lane key 与短 TTL**

lane key 固定为：

```ts
`${accountId}:codex:responses`
```

状态只进入现有 Redis/standalone 临时运行态，不修改账户主 `status`。只有 `provenance=raw_upstream` 且 deterministic contract violation 才建立短 TTL；gateway_bridge 只记录网关故障。

- [ ] **Step 4: 复用 pre-commit retry**

严格模式命中后，将 guard result 转成现有 server retry/finalization decision，设置 `excludeCurrentAccount=true`。`semanticCommitted=true` 时禁止 retry，终止流并返回 `late_violation`。不得新建并行重试循环。

- [ ] **Step 5: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-strict-intercept-regression.ts
pnpm --filter juhe-ai-backend run test:codex-turn-switch-e2e
pnpm --filter juhe-ai-backend run test:gateway-response-lifecycle-http
pnpm --filter juhe-ai-backend typecheck
git add -- backend/src/modules/gateway/codex-responses/capability-health.service.ts backend/src/modules/gateway/codex-responses/response-guard.ts backend/src/modules/gateway/response/finalization.ts backend/src/modules/gateway/dispatch/candidate-filter.ts backend/src/modules/gateway/runtime/account-dispatch-priority-order.ts backend/src/scripts/regression/codex-protocol-strict-intercept-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): intercept invalid Codex responses by capability lane"
```

## Task 4: 使用记录协议 outcome 与黄色展示

**Files:**
- Modify: `backend/src/storage/usage-record-shards.ts`
- Modify: `backend/src/storage/usage-records.repository.ts`
- Modify: `backend/src/storage/usage-record-mappers.ts`
- Modify: `backend/src/modules/gateway/usage/records.ts`
- Modify: PostgreSQL usage schema/migration files selected by current migration catalog
- Create: `backend/src/scripts/regression/codex-protocol-usage-record-regression.ts`
- Modify: `frontend/src/types/domain/usage-records.ts`
- Modify: `frontend/src/views/usage-records/UsageRecordResultCell.vue`
- Modify: `frontend/src/views/usage-records/UsageRecordMobileCard.vue`
- Create: `frontend/src/scripts/regression/usage-record-protocol-outcome-regression.ts`
- Modify: `backend/package.json`
- Modify: `frontend/package.json`

- [ ] **Step 1: 写后端 usage 红灯回归**

覆盖 clean success、repaired success、切号 success、严格失败。断言 repaired/cutover 的 `success=true`，同时 `protocolOutcome` 为对应黄色结果；diagnostics issue/rule 数量有上限，不保存完整正文。

- [ ] **Step 2: 写前端标签红灯回归**

断言：clean 为绿色“成功”；repaired 为黄色“成功 · 已修复”；切号为黄色“成功 · 已切号”；observed unknown 为黄色“成功 · 协议告警”；失败为红色。tooltip 展示 issue/provenance/trace/account，不展示正文。

- [ ] **Step 3: 运行红灯**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-usage-record-regression.ts
pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/usage-record-protocol-outcome-regression.ts
```

Expected: 两项均因字段/展示不存在失败。

- [ ] **Step 4: 实现当前 schema 字段**

新增顶层窄字段 `protocol_outcome` 和有界 JSON `protocol_diagnostics_json`。DTO 类型：

```ts
type CodexContractOutcome =
  | 'clean'
  | 'repaired_safe'
  | 'repaired_bridge'
  | 'observed_unknown'
  | 'blocked_upstream_violation'
  | 'blocked_gateway_violation'
  | 'unrecoverable_history'
  | 'late_violation'
```

数据库按项目当前 schema 直接升级，不加入运行时旧字段兜底。usage catalog/分片/PostgreSQL/worker IPC 类型同步。

- [ ] **Step 5: 实现 PC/mobile 展示**

`UsageRecordResultCell` 统一产出标签颜色、文本和 tooltip DTO，mobile card 复用同一 formatter，不复制枚举映射。

- [ ] **Step 6: 转绿、schema 和提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-usage-record-regression.ts
pnpm --filter juhe-ai-backend run test:usage-record-shard-routing
pnpm --filter juhe-ai-backend run test:usage-record-list-postgres-smoke
pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/usage-record-protocol-outcome-regression.ts
pnpm --filter juhe-ai-frontend run test:usage-record-admin-list
pnpm typecheck
git diff --check
```

Expected: 全部退出码 0；若 PostgreSQL 环境未配置，记录未运行，不伪造通过，上线前必须补齐。

- [ ] **Step 7: 提交本任务**

只暂存本任务实际变更的 schema/migration、usage 和前端文件，核对 `git diff --cached --name-only` 后提交：

```powershell
git commit -m "feat(usage): expose Codex protocol outcomes"
```

## Task 5: 固定账号后台探针与恢复闭环

**Files:**
- Create: `backend/src/modules/background/codex-protocol-health-probe.service.ts`
- Modify: `backend/src/modules/background/account-probe-jobs.ts`
- Modify: `backend/src/modules/background/background-job-registry.ts`
- Modify: `backend/src/worker.ts`
- Modify: `backend/src/modules/gateway/codex-responses/capability-health.service.ts`
- Create: `backend/src/scripts/regression/codex-protocol-health-probe-regression.ts`
- Modify: `backend/package.json`

- [ ] **Step 1: 写固定账号与阈值红灯回归**

mock 三轮 probe：同一硬违规两次进入 degraded；随后两次完整通过恢复 healthy。断言 probe 每次使用原账号，不允许切号；bridge provenance 不计入账号失败。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-health-probe-regression.ts`

Expected: FAIL，原因是 probe service 不存在。

- [ ] **Step 3: 实现 ops-worker canary**

canary 覆盖 JSON、SSE、function、custom、ID prefix、call_id pairing、usage 基础形态。任务从 capability health due queue 领取固定 account ID，调用现有账户测试/探针请求构造，但禁止候选调度。

- [ ] **Step 4: 实现恢复状态机**

状态为 `unknown/healthy/degraded/probing`；一次运行时确定性异常可建立短 TTL，连续两次相同 probe 硬违规持久诊断 degraded，连续两次完整通过恢复 healthy。TTL 到期不等于恢复。

- [ ] **Step 5: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-backend exec tsx src/scripts/regression/codex-protocol-health-probe-regression.ts
pnpm --filter juhe-ai-backend run test:account-health-check
pnpm --filter juhe-ai-backend run test:background-worker-performance
pnpm --filter juhe-ai-backend typecheck
git add -- backend/src/modules/background/codex-protocol-health-probe.service.ts backend/src/modules/background/account-probe-jobs.ts backend/src/modules/background/background-job-registry.ts backend/src/worker.ts backend/src/modules/gateway/codex-responses/capability-health.service.ts backend/src/scripts/regression/codex-protocol-health-probe-regression.ts backend/package.json
git diff --cached --check
git commit -m "feat(gateway): probe Codex response capability health"
```

## Task 6: capability health 账户展示

**Files:**
- Modify: `backend/src/modules/accounts/accounts.routes.ts`
- Modify: `backend/src/storage/account-summary.repository.ts`
- Modify: `frontend/src/types/domain/accounts.ts`
- Modify: `frontend/src/views/accounts/accountFormatters.ts`
- Modify: `frontend/src/views/accounts/AccountExtraInfoSection.vue`
- Modify: `frontend/src/views/accounts/AccountsView.vue`
- Create: `frontend/src/scripts/regression/account-codex-protocol-health-display-regression.ts`
- Modify: `frontend/package.json`

- [ ] **Step 1: 写账户摘要与展示红灯回归**

构造 `unknown`、`healthy`、`probing`、`degraded` 四种 capability snapshot，断言账户主状态不变，前端只额外显示：`Codex Responses 正常`、`Codex Responses 检查中`、`Codex Responses 协议异常`；不支持 Responses 的账户没有该标签。

- [ ] **Step 2: 运行红灯**

Run: `pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/account-codex-protocol-health-display-regression.ts`

Expected: FAIL，原因是账户摘要没有 Codex capability health 字段或 formatter 不存在。

- [ ] **Step 3: 让账户摘要读取 capability snapshot**

后端列表/详情返回可选窄字段：

```ts
interface CodexResponsesCapabilityHealthSummary {
  status: 'unknown' | 'healthy' | 'degraded' | 'probing'
  reasonCode?: string
  lastSeenAt?: string
  avoidUntil?: string
}
```

只在账户实际支持 Codex Responses 时组装；从 `capability-health.service.ts` 读取紧凑运行态，不在账户列表 map 中按行请求 DB、Redis 或 probe service。

- [ ] **Step 4: 实现紧凑状态标签**

`accountFormatters.ts` 返回标签文本/颜色/tooltip；列表和详情复用此 formatter。主账户 `status` 的颜色、文案、筛选和调度语义不得被 capability health 替代。

- [ ] **Step 5: 转绿并提交**

Run:

```powershell
pnpm --filter juhe-ai-frontend exec tsx src/scripts/regression/account-codex-protocol-health-display-regression.ts
pnpm --filter juhe-ai-frontend run test:account-status-formatters
pnpm --filter juhe-ai-backend run test:account-list-projection
pnpm --filter juhe-ai-frontend typecheck
git add -- backend/src/modules/accounts/accounts.routes.ts backend/src/storage/account-summary.repository.ts frontend/src/types/domain/accounts.ts frontend/src/views/accounts/accountFormatters.ts frontend/src/views/accounts/AccountExtraInfoSection.vue frontend/src/views/accounts/AccountsView.vue frontend/src/scripts/regression/account-codex-protocol-health-display-regression.ts frontend/package.json
git diff --cached --check
git commit -m "feat(accounts): display Codex response capability health"
```

## Task 7: 灰度、回退、文档与最终验证

**Files:**
- Modify: `docs/functions/请求处理分层设计.md`
- Modify: `docs/functions/OpenAI账号接入.md`
- Modify: `docs/functions/接口契约与权限矩阵.md`
- Modify: `docs/functions/安全与日志策略.md`
- Modify: `docs/develop/运行说明.md`
- Modify: `docs/develop/测试与验证说明.md`
- Modify: this plan

- [ ] **Step 1: 执行四阶段灰度**

顺序固定：`shadow -> safe_repair -> strict_opt_in -> probe_close_loop`。每阶段至少记录 issue rate、repair rate、blocked rate、gateway_bridge/raw_upstream 分布、guard p95 和 event-loop lag。上一阶段没有稳定证据不得进入下一阶段。

- [ ] **Step 2: 验证紧急回退**

将全局 mode 切为 `off` 后，账户配置和历史诊断保留，响应不再修复/拦截；P0 请求历史 sanitizer 仍启用。`observe_only` 仅是账户派生模式，不能写成全局 mode。验证解除 lane TTL 有操作审计。

- [ ] **Step 3: 运行仓库级门禁**

```powershell
pnpm typecheck
pnpm lint
pnpm build
Push-Location backend-go; go test ./...; Pop-Location
git diff --check
```

Expected: 全部退出码 0。

- [ ] **Step 4: 执行独立代码复核**

使用 `superpowers:requesting-code-review`，至少检查：需求覆盖、修复语义不变量、账号误伤、stream commit 竞态、SQLite/PostgreSQL/worker 一致性、性能与灰度回退。对成立发现修复并重跑对应门禁。

- [ ] **Step 5: 更新权威文档和计划证据**

只把实际运行且退出码 0 的命令记为通过；生产/真实账号未执行项单独列出。更新账户开关、usage 标签、lane health、探针和紧急模式事实。

- [ ] **Step 6: 提交文档**

```powershell
git add -- docs/functions/请求处理分层设计.md docs/functions/OpenAI账号接入.md docs/functions/接口契约与权限矩阵.md docs/functions/安全与日志策略.md docs/develop/运行说明.md docs/develop/测试与验证说明.md docs/superpowers/plans/2026-07-22-Codex-Responses账户策略可观测性与灰度实施计划.md
git diff --cached --check
git commit -m "docs(gateway): document Codex protocol guard rollout"
```

## 最终验收

- [ ] 两开关默认值、禁用交互和后端派生模式一致。
- [ ] 严格拦截仅在 semantic commit 前透明切号。
- [ ] 只有 raw upstream 建立 Codex Responses lane 回避；Chat 等能力继续可用。
- [ ] repaired/cutover success 保持 `success=true` 且前端黄色展示。
- [ ] 固定账号探针不能被切号掩盖，恢复阈值有回归。
- [ ] shadow/safe repair/strict/probe 四阶段和全局紧急回退均有证据。
