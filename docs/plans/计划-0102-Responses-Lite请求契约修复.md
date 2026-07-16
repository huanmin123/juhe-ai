# PLAN-0102 Responses Lite 请求契约修复

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保证任一带 Responses Lite header 的上游请求同时满足 `reasoning.context=all_turns` 与 `parallel_tool_calls=false`，修复软件账户测试被严格上游拒绝的问题。

**Architecture:** 现有 Lite 模型集合继续作为唯一能力事实，在 Codex 适配模块新增一个小型 body 归一化函数。API Key 和 OAuth 两条请求构造路径都在确定最终模型后调用它，账户测试继续走真实网关链路，非 Lite 请求保持不变。

**Tech Stack:** Node.js、TypeScript、Express Request 适配、`tsx` 回归脚本、pnpm。

---

## 基本信息

- 编号：`PLAN-0102`
- 状态：实现完成，待发布
- 创建时间：2026-07-16
- 需求来源：生产 2chat API Key 账户人工测试返回 `X-OpenAI-Internal-Codex-Responses-Lite requires reasoning.context to be all_turns`
- 执行分支：`codex/responses-lite-contract-hotfix`

## 问题与证据

- 生产成功样本 `traceId=e93a52c1-8c6e-45f7-90c9-390061ac5ecd` 使用 `gpt-5.6-sol`，客户端画像为 `codex / codex_responses`，HTTP 状态为 `200`。
- 该样本的客户端请求与实际上游请求均包含 `reasoning.context="all_turns"` 和 `parallel_tool_calls=false`，说明网关能够保留正确的 Lite 契约。
- 软件账户测试会构造 Codex Responses 请求，但当前 payload 没有 `reasoning.context`；API Key 兼容层又会为 Lite 模型添加内部 Lite header，并把缺省 `parallel_tool_calls` 设为 `true`。
- 因此严格校验 Lite 契约的上游会拒绝账户测试；这不是 API Key 失效，也不是模型目录需要过滤。

## 目标与边界

### 本次完成

- 只要网关决定发送 Responses Lite header，上游请求体必须同步满足 Lite 契约。
- `reasoning.context` 固定为 `all_turns`，同时保留已有 `reasoning.effort` 和 `reasoning.summary`。
- `parallel_tool_calls` 固定为 `false`。
- API Key 兼容路径、OAuth Codex 路径和软件账户测试共用同一契约判断。
- 非 Lite 模型保持当前行为，不新增模型过滤、版本拦截或其他特殊校验。

### 本次不做

- 不改变 `/models` 返回的模型集合。
- 不修改账户状态、调度、模型映射、计费或审计结构。
- 不为未知模型推断 Lite 能力。
- 不改变数据库、缓存容量或生产数据。

## 方案比较与决策

1. 在共享 Codex Lite 契约层同步 header 与 body，API Key 和 OAuth 都调用该逻辑。优点是从根因保证不变量，影响面明确；采用此方案。
2. 只修改账户测试 payload。改动更小，但其他旧客户端或内部调用仍可能产生 header/body 不一致；不采用。
3. 账户测试删除 Lite header。可以绕过严格校验，但测试内容不再等价于真实 Lite 请求；不采用。

## 设计

- 以最终上游模型是否属于已登记 Lite 模型作为唯一判断，与现有 Lite header 判断保持同源。
- Lite 请求归一化时，把非对象 `reasoning` 视为缺省对象；对象值则浅复制后覆盖 `context="all_turns"`，不删除其他合法 reasoning 字段。
- Lite 请求无条件写入 `parallel_tool_calls=false`，不接受调用方的相反值继续上游。
- 非 Lite 请求不主动增加 `reasoning.context`，并保持现有并行工具默认行为。
- 账户测试仍走真实网关链路，不在测试服务里绕过 header 或上游校验。

## 执行拆解

### Task 1：建立失败回归

**Files:**

- Modify: `backend/src/scripts/regression/codex-latest-compatibility-regression.ts`
- Modify: `backend/src/scripts/regression/account-test-request-regression.ts`

- [x] 在 Codex 最新兼容回归中断言 API Key Lite 请求体包含 `reasoning.context=all_turns`、保留 `effort/summary`，并强制 `parallel_tool_calls=false`。
- [x] 在同一回归中断言 OAuth Lite 请求具备相同 body 契约，`gpt-5.5` 非 Lite 请求不新增 context。
- [x] 在账户测试回归中断言 `codex_responses + responses_sse + gpt-5.6-sol` payload 具备 Lite body 契约。
- [x] 两条回归均先因 `reasoning.context` 为 `undefined` 按预期失败，未出现导入或语法错误。

### Task 2：实现共享 Lite body 契约

**Files:**

- Modify: `backend/src/modules/gateway/adapters/gpt-codex/client-headers.ts`
- Modify: `backend/src/modules/gateway/protocols/openai-v1/api-key-client-compatibility.ts`
- Modify: `backend/src/modules/gateway/adapters/gpt-codex/oauth-normalizer.ts`
- Modify: `backend/src/modules/accounts/account-test-request.ts`

- [x] 在 Codex 适配模块导出 `normalizeOpenAICodexResponsesLiteBody(body, model)`；非 Lite 直接返回，Lite 时浅复制已有 reasoning 对象并覆盖 `context: 'all_turns'`，同时设置 `parallel_tool_calls=false`。
- [x] API Key 兼容层先应用最终模型覆盖，再调用共享归一化；5 个相关 provider driver 的 header/body 均传递同一最终模型事实。
- [x] OAuth 归一化在最终模型确定、账户请求覆盖完成后调用共享归一化，compact 和普通 Responses 使用同一 Lite 不变量。
- [x] 账户测试 payload 对 Lite 模型直接产生正确字段，使测试请求本身可读且真实网关仍再次保证不变量。
- [x] Task 1 两个命令复跑通过，并补 Lite 与非 Lite 双向模型映射真实 driver 回归。

### Task 3：关联验证与提交

**Files:**

- Modify: `docs/plans/计划-0102-Responses-Lite请求契约修复.md`
- Modify when needed: `docs/develop/测试与验证说明.md`

- [x] OAuth adapter 完整测试树临时编译为 `.js` 后运行通过；API Key 网关 mock 和账户测试请求回归通过。标准 tsx OAuth worker 命令受既有 `.js` 源模块解析问题阻断，API Key passthrough 受未启动 DB service 阻断，均已记录且未伪报通过。
- [x] 后端 `pnpm typecheck` 与 `pnpm build` 通过。
- [x] `git diff --check` 通过，无冲突标记、临时日志和额外未跟踪产物。
- [x] 已更新本计划测试结果和状态，等待实现提交。

### Task 4：集成与生产发布

**Files:**

- Update: `F:/服务部署/juhe-ai/09-上线计划/` 对应上线记录
- Update only on reusable incident: `F:/服务部署/juhe-ai/07-问题记录/`

- [ ] 在干净 master 工作树合入 hotfix，重复后端专项、类型检查和构建，推送 master。
- [ ] 使用 `pwsh ./scripts/package-release.ps1` 构建发布包，禁止从 Git Bash 打包；校验包内 API base path。
- [ ] 按现有 macOS 家庭主机流程准备临时候选、验证、切换和升级，不清 Redis、不改内存上限、不执行无关数据库迁移。
- [ ] 生产连续验证 60 秒，要求双 health 200、PID/拓扑稳定、无 watchdog 动作、无基础设施错误。
- [ ] 用软件账户测试复验严格 Lite 上游；若需要用户凭据或验证码，记录未验证边界，不伪造成功。
- [ ] 记录 release、SHA-256、验证结果和异常；将 master 反向合并到本地及远程 `feature/20260706-go`，恢复原工作区开发现场。

## 测试项

| 测试类型 | 测试项 | 预期结果 | 状态 |
| --- | --- | --- | --- |
| 回归 | API Key `gpt-5.6-sol` Codex Responses | Lite header、`context=all_turns`、`parallel_tool_calls=false` 同时存在 | 已通过 |
| 回归 | OAuth `gpt-5.6-sol` Codex Responses | 与 API Key 保持相同 Lite body 契约 | 已通过 |
| 回归 | 已有 reasoning 字段 | 保留 effort / summary，只收口 context | 已通过 |
| 回归 | 非 Lite 模型 | 不新增 context，不改变现有并行工具语义 | 已通过 |
| 回归 | 软件账户人工测试 | 严格 Lite 上游不再返回缺少 all_turns 错误 | 未执行 |
| 静态 | 后端 typecheck / build | 全部通过 | 已通过 |

## 验收标准

- 任一发往上游且包含 Responses Lite header 的请求，都同时包含 `reasoning.context="all_turns"` 和 `parallel_tool_calls=false`。
- API Key、OAuth、软件账户测试三条路径具有自动化回归。
- 非 Lite 模型请求行为不变。
- 不引入模型过滤、客户端版本限制或与本问题无关的协议改造。

## 风险与回退

- 风险集中在 Codex Responses 请求体归一化；通过 Lite 模型门禁和非 Lite 回归限制影响范围。
- 若上游出现不兼容，可回退本计划对应代码提交；无数据库、缓存或数据迁移回退要求。

## 进度记录

- 2026-07-16：完成生产成功 trace、当前账户测试构造器和最新 Codex 源码对比；用户确认采用共享契约根因修复方案。
- 2026-07-16：TDD 回归从预期失败转绿；独立审查发现并关闭模型映射后的 header/body 最终模型不一致，同时补齐 API Key/OAuth 大请求 worker 契约证据。

## 验证记录

- `pnpm test:codex-latest-compatibility`：通过。
- `pnpm test:account-test-request`：通过。
- `pnpm test:account-api-key-gateway-mock-ai`：通过。
- `pnpm typecheck`：通过。
- `pnpm build`：通过。
- 完整 `tsconfig.json` 临时编译后执行 `openai-oauth-codex-adapter-regression.js`：通过，覆盖 API Key/OAuth 大请求 worker。
- `pnpm test:openai-oauth-codex-adapter`：标准 tsx worker 因既有 `usage/reasoning-effort.js` 源路径解析问题受阻；相同测试按生产编译形态通过。
- `pnpm test:openai-api-key-passthrough`：本地 DB service 未启动，在模型能力查询前置处受阻；API Key 专项和网关 mock 已通过。
