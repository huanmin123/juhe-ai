# Codex Responses 协议防火墙分阶段实施总 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 分阶段交付 Codex Responses 请求历史自愈、协议防火墙、账户严格拦截、黄色使用记录和固定账号探针，修复已确认污染而不牺牲正常网关性能或误伤账号。

**Architecture:** 实施顺序从确定性源头缺陷开始，再建立可观测的 protocol core，最后把账户策略、副作用和 UI 接入。所有响应检查按 raw upstream/bridge 后双检查点归因，所有自动修复遵守 R0-only 和 semantic commit 边界。

**Tech Stack:** Node.js、TypeScript、Express、Vue 3、Ant Design Vue、SQLite/PostgreSQL、Redis/standalone runtime、ops-worker、OpenAI Responses JSON/SSE、pnpm/tsx。

---

## 关联设计与审计

- [Codex Responses 协议防火墙与会话修复设计](../specs/2026-07-22-Codex-Responses协议防火墙与会话修复设计.md)
- [Codex Responses 协议转换自审报告](../specs/2026-07-22-Codex-Responses协议转换自审报告.md)
- 本地 Codex 源码基线：`F:\temp-project\agent\openai-codex-main`，commit `1bbdb32789e1f79932df44941236ea3658f6e965`

## 阶段与依赖

```text
P0 源头修复与历史自愈
  -> 协议核心 shadow / 性能门禁
    -> safe_repair
      -> 账户严格拦截、usage 和 lane health
        -> 固定账号探针闭环
```

| 阶段 | 子计划 | 可独立发布 | 进入条件 | 回退 |
| --- | --- | --- | --- | --- |
| P0 | `2026-07-22-Codex-Responses-P0源头修复与历史自愈实施计划.md` | 是 | 精确回归、历史重放 E2E、线性性能基准 | 回退代码版本；不改账户状态 |
| P1 | `2026-07-22-Codex-Responses协议防火墙核心与性能实施计划.md` | 是，先 shadow | P0 已发布、shadow 性能数据可用 | 全局 guard mode 降级；P0 sanitizer 保留 |
| P2 | `2026-07-22-Codex-Responses账户策略可观测性与灰度实施计划.md` | 是，严格模式 opt-in | P1 safe repair 稳定、usage schema/worker 验证 | 全局 `off`，解除 lane TTL，不删诊断 |

## 非协商不变量

1. 不修改 `call_id`、工具名称、工具参数、工具输出、消息正文、reasoning、usage、finish reason。
2. custom tool 从源头生成 `ctc_*`；function tool 从源头生成 `fc_*`。
3. 请求历史修复只删除 `id`，且只有 item 具有完整可重放负载时允许；否则显式失败。
4. raw upstream 与 gateway bridge 必须分开归因；gateway bridge 缺陷不得处罚账号。
5. semantic commit 前才能透明换号；之后只能终止并记录 late violation。
6. capability lane 回避范围是 `accountId + codex + responses`，不得改账户主状态或影响 Chat。
7. clean 热路径只做单遍结构扫描；不得全文深拷贝、全文 hash、重复 parse 或缓冲完整 SSE。
8. repaired/cutover 的最终客户端成功仍为 `success=true`；黄色是协议质量维度，不是账务失败。

## Task 1: 执行 P0

**Files:**
- Follow: `docs/superpowers/plans/2026-07-22-Codex-Responses-P0源头修复与历史自愈实施计划.md`

- [ ] **Step 1: 建立隔离工作区并记录基线**

执行阶段先使用 `superpowers:using-git-worktrees`；不得在当前含用户修改的工作区实现。记录 `git status --short` 与当前 commit，确认 P0 只触及 bridge、sanitizer、请求准备和回归文件。

- [ ] **Step 2: 按 P0 Task 1-4 逐项 TDD 实施**

每项必须按“失败测试 -> 最小实现 -> 同一测试转绿 -> commit”执行。禁止将 P1 registry、账户开关或 usage schema 混入 P0。

- [ ] **Step 3: P0 发布门禁**

```powershell
pnpm --filter juhe-ai-backend run test:codex-responses-tool-item-identity
pnpm --filter juhe-ai-backend run test:codex-responses-history-sanitizer
pnpm --filter juhe-ai-backend run test:codex-responses-bridge-native-switch
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-backend build
git diff --check
```

Expected: 全部退出码 0。性能脚本必须证明 clean input 零拷贝、扫描成本近似线性。

## Task 2: 执行 P1 shadow 与 safe repair

**Files:**
- Follow: `docs/superpowers/plans/2026-07-22-Codex-Responses协议防火墙核心与性能实施计划.md`

- [ ] **Step 1: 先完成 registry/validator/stream state**

在任何账户副作用接入前，完成 P1 Task 1-3。未知类型必须 pass-with-warning，R2 必须明确不可执行。

- [ ] **Step 2: 接入双检查点并只启用 shadow**

完成 P1 Task 4-5 后，运行环境必须为 `shadow`：记录问题、不改响应、不拦截、不处罚账号。观察指标：issue rate、provenance 分布、parser/budget skipped、guard p95、event-loop lag。

- [ ] **Step 3: 评审 shadow 证据并启用 safe_repair**

只有在以下证据同时存在时才允许把全局 mode 改为 `safe_repair`：已知 fixture 100% 通过；未知 type 未造成阻断；clean 路径无二次增长；没有 gateway_bridge 误归因为 raw upstream。safe repair 仅启用 R0。

- [ ] **Step 4: P1 发布门禁**

```powershell
pnpm --filter juhe-ai-backend run test:codex-responses-contract-registry
pnpm --filter juhe-ai-backend run test:codex-responses-contract-json
pnpm --filter juhe-ai-backend run test:codex-responses-contract-sse
pnpm --filter juhe-ai-backend run test:codex-responses-provenance
pnpm --filter juhe-ai-backend typecheck
pnpm --filter juhe-ai-backend build
```

Expected: 全部退出码 0；性能输出作为发布记录保存到 `docs/reports/`，不把报告正文写进本计划。

## Task 3: 执行 P2 账户策略、usage 与探针

**Files:**
- Follow: `docs/superpowers/plans/2026-07-22-Codex-Responses账户策略可观测性与灰度实施计划.md`

- [ ] **Step 1: 先接账户开关和 UI，不启用严格拦截**

完成 P2 Task 1-2 后，既有账号默认兼容修复开启、严格拦截关闭。确认账户不支持 Responses 时不会显示配置。

- [ ] **Step 2: 接 usage schema 与黄色展示**

完成 P2 Task 4，当前 schema 同步完成后再灰度，避免 SQLite/PostgreSQL/worker IPC DTO 不一致。

- [ ] **Step 3: strict opt-in**

完成 P2 Task 3 后，只开放账户级 opt-in。首次上线只允许 test/mock 或明确指定账户；检查 raw upstream 才建立 lane TTL，bridge 绝不建立。

- [ ] **Step 4: probe close loop**

完成 P2 Task 5 后启用固定账号 probe。探针失败/成功阈值达到要求前，状态只显示 probing/unknown，不修改账户主状态。

- [ ] **Step 5: P2 发布门禁**

```powershell
pnpm --filter juhe-ai-backend run test:codex-protocol-account-policy
pnpm --filter juhe-ai-backend run test:codex-protocol-strict-intercept
pnpm --filter juhe-ai-backend run test:codex-protocol-usage-record
pnpm --filter juhe-ai-backend run test:codex-protocol-health-probe
pnpm --filter juhe-ai-frontend run test:account-codex-protocol-guard
pnpm --filter juhe-ai-frontend run test:usage-record-protocol-outcome
pnpm typecheck
pnpm lint
pnpm build
```

Expected: 全部退出码 0；PostgreSQL smoke 和真实账号试验分别记录，未配置环境不能伪造为通过。

## Task 4: 回退演练与最终复核

**Files:**
- Modify: 三个子计划的验证证据段落
- Modify: 相关 `docs/functions/` 与 `docs/develop/` 权威文档

- [ ] **Step 1: 演练全局降级**

设置全局 mode 为 `off`，确认：账户配置仍在、历史诊断仍在、P0 sanitizer 仍在、响应不再 R0 修复/严格拦截。账户 `observe_only` 是局部策略，不能替代系统紧急模式。

- [ ] **Step 2: 演练 capability lane 解除**

模拟 raw upstream violation 后 lane TTL，确认 Chat 不受影响；管理员解除后只影响该 lane，并留下操作审计。

- [ ] **Step 3: 做独立复核**

使用 `superpowers:requesting-code-review` 对最终差异审查：类型一致性、stream commit、账号误伤、性能、数据库 schema 和前端标签。成立问题修复后重跑受影响门禁。

- [ ] **Step 4: 同步权威事实**

更新请求处理、OpenAI 账号、接口/使用记录、运行与验证文档；只记录真实执行证据。

## 完成证据矩阵

| 需求 | 证明方式 |
| --- | --- |
| `fc/ctc` 源头根治 | P0 tool identity JSON/SSE 回归 |
| `rs_* not persisted` 与跨账号重放 | P0 bridge -> native E2E 和不可恢复负例 |
| 不是逐错误码修补 | P1 registry/validator/planner/executor 结构与 fixture |
| 不误伤正常账号 | provenance + strict intercept 回归 |
| 两账户开关与严格优先 | account policy + 前端表单回归 |
| 黄色成功而非伪失败 | usage record + PC/mobile 展示回归 |
| 探针不切号掩盖异常 | fixed-account probe 回归 |
| 性能可控 | P0/P1 基准与 clean 单遍/零拷贝断言 |
| 可灰度可回退 | global mode、phase evidence、回退演练 |

## 执行约束

- P0/P1/P2 不并行修改同一网关 response 主链路。
- 每个小任务先红后绿，并在逻辑边界提交；不混入工作区无关修改。
- 不以真实生产异常作为唯一验证；mock/E2E fixture 必须可稳定重放。
- 不依赖黑盒方式判断物理模型身份；协议异常只更新 protocol status。
- 不写运行时旧 schema 兼容或一次性数据迁移兜底；当前 schema 由发布时同步。
