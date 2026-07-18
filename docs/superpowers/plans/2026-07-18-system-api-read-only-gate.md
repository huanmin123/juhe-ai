# System API Read-Only Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在临时接管发布期间，以单一运行时开关阻止 System/Public API 的非读取方法，同时保持 `/v1` 客户端网关可用。

**Architecture:** 门禁位于 `createSystemApiApp()` 的两个前缀最早公共位置，早于 JSON body parser、认证和 DB admission。只按 HTTP 方法放行 `GET/HEAD/OPTIONS`，其他方法返回固定 503；默认关闭，不写数据库、不做跨实例同步。

**Tech Stack:** Node.js、TypeScript、Express、Zod 回归脚本、Nginx/macOS 发布脚本文档。

---

### Task 1: Runtime contract and middleware

**Files:**
- Modify: `backend/src/config/runtime.ts`
- Create: `backend/src/modules/system-api/system-api-read-only.middleware.ts`
- Modify: `backend/src/modules/system-api/system-api-app.ts`

- [ ] Add `runtimeConfig.systemApi.readOnly` backed by `JUHE_AI_SYSTEM_API_READ_ONLY`, default `false`, using the existing strict boolean parser.
- [ ] Add middleware that checks `req.method`; allow `GET`, `HEAD`, `OPTIONS`, otherwise return status `503`, `Cache-Control: no-store`, `Retry-After: 60`, and `{message, code}` JSON before parsing the request body.
- [ ] Mount the middleware before the system `my-chat` route and before both system/public JSON parsers; leave `/v1` outside this Express app.

### Task 2: Regression coverage

**Files:**
- Create: `backend/src/scripts/regression/system-api-read-only-regression.ts`
- Modify: `backend/package.json`

- [ ] Test the middleware in disabled mode with GET and POST behavior.
- [ ] Test enabled mode for GET/HEAD/OPTIONS pass-through and POST/PUT/PATCH/DELETE rejection, including the exact response headers/body.
- [ ] Assert source ordering proves the gate precedes body parsers and applies to both prefixes.

### Task 3: Deployment documentation and plan state

**Files:**
- Modify: `docs/deploy/部署指南.md`
- Modify: `docs/deploy/macos/macOS部署指南.md`
- Modify: `docs/develop/测试与验证说明.md`
- Modify: `docs/plans/计划-0131-临时接管系统接口只读门禁与日志时间.md`

- [ ] Document the temporary-only environment variable, forbidden production default, and rollback behavior.
- [ ] Add the regression command to the test index and update PLAN-0131 with actual implementation and remaining real-environment gates.

### Task 4: Verification

- [ ] Run `pnpm --filter juhe-ai-backend test:system-api-read-only`.
- [ ] Run backend/frontend type checks and `pnpm build`.
- [ ] Run `git diff --check` and the release-source/migration compatibility gates when their scripts are present in the release worktree.
- [ ] Do not deploy, migrate, commit, push, or reverse merge until the user explicitly authorizes the release window.
