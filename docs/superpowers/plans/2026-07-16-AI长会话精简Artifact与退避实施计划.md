# AI 长会话精简 Artifact 与退避 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 checkpoint artifact 限制为完整但紧凑的 32 KiB HTML，并为三次 transient attempt 增加 2 秒/5 秒有界退避。

**Architecture:** fixture 定义统一 artifact 字节合同与 prompt；attempt runner 只负责重试节奏和安全 metric；real E2E 在 completed artifact 边界执行确定性质量校验。评分逻辑和上游请求字段保持不变。

**Tech Stack:** Node.js 22、TypeScript、tsx regression scripts、PowerShell 7。

---

### Task 1: Artifact fixture contract

**Files:**
- Modify: `backend/src/scripts/regression/chat-long-session-fixture.ts`
- Modify: `backend/src/scripts/regression/chat-long-session-fixture-regression.ts`

- [ ] **Step 1: Write failing assertions**

断言 artifact prompt 包含单一 code fence、无解释/注释/重复内容和 `UTF-8 <= 32 KiB`；perfect artifact 全部不超过 32 KiB；追加超过上限 1 字节的对抗样例。

- [ ] **Step 2: Run RED**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
```

预期因 prompt 尚未声明精简与字节合同而失败。

- [ ] **Step 3: Implement minimal contract**

导出 `chatLongSessionArtifactMaxBytes = 32 * 1024`，并让 artifact prompt 明确只输出一个 HTML fence、禁止解释和注释、保持完整可读且紧凑。

- [ ] **Step 4: Run GREEN**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
```

### Task 2: Deterministic artifact quality gate

**Files:**
- Modify: `backend/src/scripts/regression/chat-long-session-fixture.ts`
- Modify: `backend/src/scripts/regression/chat-long-session-real-e2e.ts`
- Modify: `backend/src/scripts/regression/chat-long-session-fixture-regression.ts`

- [ ] **Step 1: Write failing quality-gate tests**

定义返回稳定 `artifact_too_large` failure 的纯边界函数；断言 32 KiB 通过、32 KiB + 1 拒绝，classification 为 deterministic。

- [ ] **Step 2: Run RED**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
```

- [ ] **Step 3: Apply gate in real runner**

在 completed checkpoint 进入成功分支前按 UTF-8 字节检查 assistant output；超限作为 deterministic attempt result 返回，不触发 transient replacement。

- [ ] **Step 4: Run GREEN and local preflight**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
pnpm --dir backend run test:local-chat-long-session-preflight
```

### Task 3: Budget-aware retry backoff

**Files:**
- Modify: `backend/src/scripts/regression/chat-long-session-attempts.ts`
- Modify: `backend/src/scripts/regression/chat-long-session-real-e2e.ts`
- Modify: `backend/src/scripts/regression/chat-long-session-fixture-regression.ts`

- [ ] **Step 1: Write failing delay assertions**

为 transient success 和 retry exhausted 断言 `delayMs` 分别为 `[0, 2000]`、`[0, 2000, 5000]`，deterministic 为 `[0]`，总 submit 次数不超过 3。

- [ ] **Step 2: Run RED**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
```

- [ ] **Step 3: Implement injected sleep**

`runChatLongSessionTurnAttempts` 接收 `sleep(delayMs)`；每次 submit 前记录对应 delay，非首轮调用 sleep。真实 runner 注入现有 budget-aware `sleep`。

- [ ] **Step 4: Verify full local scope**

```powershell
pnpm --dir backend run test:chat-long-session-fixture
pnpm --dir backend run test:chat-long-session-entry-recovery
pnpm --dir backend run test:chat-long-session-process-tree
pnpm --dir backend run test:local-chat-long-session-preflight
pnpm --dir backend run typecheck
pnpm --dir backend run build
git diff --check
```

预期全部 exit 0，随后执行独立只读 review；不运行真实网络请求。
