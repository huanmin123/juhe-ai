# Codex Responses 协议防火墙与会话修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除网关自产的 Codex Responses 会话污染，增加请求历史自愈、响应契约检查、账户策略和诊断展示；测试通过后合并 master 并推远程，生产上线等待用户通知。

**Architecture:** 版本化 registry 是唯一协议规则源。请求侧始终执行轻量 L0 扫描，只对可证明安全的 item 删除 ID；响应侧验证与修复分离，Chat→Responses bridge 从源头按类型生成 `fc_*`/`ctc_*`。干净路径使用单次遍历、copy-on-write 和 SSE 增量状态机。

**Tech Stack:** Node.js、TypeScript、Express、Vue 3、pnpm/tsx、SQLite、PostgreSQL、Redis。

**工作区:** `F:\sub2api-lite\.worktrees\codex-responses-protocol-firewall-20260722`

**分支:** `codex/codex-responses-protocol-firewall-20260722`

**设计:** `docs/superpowers/specs/2026-07-22-Codex-Responses协议防火墙与会话修复设计.md`

---

## 文件结构

- 新建 `backend/src/modules/gateway/codex-protocol/contract-registry.ts`：类型前缀与 revision。
- 新建 `backend/src/modules/gateway/codex-protocol/request-history-sanitizer.ts`：请求 L0/R0。
- 新建 `backend/src/modules/gateway/codex-protocol/response-contract-validator.ts`：JSON/SSE 诊断。
- 新建 `backend/src/modules/gateway/codex-protocol/stream-identity-state.ts`：流式身份生命周期。
- 新建 `backend/src/modules/gateway/codex-protocol/account-guard-config.ts`：账户开关默认值。
- 新建 `backend/src/modules/gateway/codex-protocol/diagnostics.ts`：outcome 与 issue code。
- 修改 `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`：源头生成正确工具 ID。
- 修改 `backend/src/modules/gateway/codex-responses/chat-bridge-state.ts`：bridge→native 历史清洗。
- 修改网关 response/usage 与账户前端：检查、拦截和诊断展示。
- 新增独立回归脚本并在 `backend/package.json` 注册。

### Task 1: Bridge 源头修复

- [ ] 写失败回归：custom added/done/JSON 必须为 `ctc_*`，function 必须为 `fc_*`。
- [ ] 验证当前实现因先写死 `fc_*` 而失败。
- [ ] 先确定 adapter.kind，再生成 item ID；未知工具按 function 处理，不猜 custom。
- [ ] 运行回归转绿并提交。

### Task 2: 请求历史 Sanitizer

- [ ] 写失败回归：错误前缀、`store=false`、跨上游作用域、不可恢复历史。
- [ ] 实现 registry 与 copy-on-write sanitizer。
- [ ] 断言不修改 `call_id`、正文、顺序和工具结果。
- [ ] 运行回归转绿并提交。

### Task 3: 接入出站与 Bridge→Native

- [ ] 写失败回归：internal `previous_response_id` 从 bridge 切到原生账号时不得重放 wire item ID。
- [ ] 在 native hand-off 和 Codex Responses 出站前调用 sanitizer。
- [ ] 仅 ID item 显式失败，不静默删除。
- [ ] 运行 `codex-cross-protocol-context`、`openai-api-key-passthrough` 并提交。

### Task 4: 响应契约 Shadow 检查

- [ ] 写 validator 单元回归：`custom+fc`、生命周期漂移、重复 identity、未知类型。
- [ ] 实现 JSON validator 与 SSE identity state。
- [ ] 在 raw upstream 与 bridge output 设置双检查点，默认只诊断。
- [ ] 验证 clean 路径不深拷贝/全文哈希并提交。

### Task 5: 修复、账户策略和日志

- [ ] 实现 `responseRepairEnabled=true`、`strictInterceptEnabled=false` 默认配置。
- [ ] R0 修复只允许 ID 路径；严格拦截优先于修复。
- [ ] 修复成功保持 `success=true`，新增 protocol outcome 并显示黄色标签。
- [ ] `gateway_bridge` 不处罚账号；`raw_upstream` 才可建立 Codex Responses lane 回避。
- [ ] 运行账户、usage、response inspection 回归并提交。

### Task 6: 验证、同步和合并

- [ ] 运行全部新增回归和 Codex/bridge/context 相关回归。
- [ ] 运行 typecheck、lint、build 与必要的 PG/Redis smoke。
- [ ] 若需要，使用本地凭据文件进行真实模型测试，密钥不进入日志和 Git。
- [ ] 定期 `git fetch` 并 merge 最新 master；每次同步后重跑关键回归。
- [ ] 最终复查需求、性能、边界、并发、安全、文档与测试有效性。
- [ ] 测试通过后合并到 master 并 push 远程。
- [ ] 不部署生产，等待用户上线通知。

## 验证命令

```powershell
pnpm --filter juhe-ai-backend run test:codex-responses-chat-bridge-tool-id
pnpm --filter juhe-ai-backend run test:codex-responses-history-sanitizer
pnpm --filter juhe-ai-backend run test:codex-responses-protocol-guard
pnpm --filter juhe-ai-backend run test:codex-cross-protocol-context
pnpm --filter juhe-ai-backend run test:openai-api-key-passthrough
pnpm --filter juhe-ai-backend run test:codex-latest-compatibility
pnpm typecheck
pnpm lint
pnpm build
```

## 环境边界

- 本地 SQLite 因缺表或测试脏状态失败时，只删除测试数据库并重建。
- `192.168.1.203` 只用于测试 PostgreSQL/Redis；仅操作专用测试库和测试表。
- `F:\服务部署\服务器账户密码.txt` 与 `D:\模型密钥.txt` 仅运行时读取，不复制到文档、终端输出、提交或审计附件。
- 生产部署不属于本计划自动动作。

## 进度记录

| 日期 | 事件 |
| --- | --- |
| 2026-07-22 | 从 master `17b1af5c6` 创建独立 worktree 和功能分支，完善设计模块地图并落地本计划 |
