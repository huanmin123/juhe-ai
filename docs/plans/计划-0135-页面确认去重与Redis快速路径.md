# PLAN-0135 页面确认去重与 Redis 快速路径

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除 KeepAlive 隐藏页面和单次刷新产生的重复确认请求，并把 performance 模式页面确认压缩为一次 Redis 原子往返。

**Architecture:** 前端按组件激活状态控制确认调度器，缓存已有有效 token 时采用“先读取业务数据、后确认一次”的稳定读取流程，并通过共享确认协调器合并同作用域同一微任务中的数据域。后端把确认接口标记为只读数据库访问，并用单个 Lua 脚本原子读取 epoch、域序列、全域 reset 序列和必要的变更日志。

**Tech Stack:** Vue 3、TypeScript、IndexedDB 页面缓存、Node.js、Express、PostgreSQL session 鉴权、Redis Lua。

---

## 当前状态

- 状态：实现完成 / 本地与真实 Redis 验证通过 / 禁止上线
- 已知待验：本地旧 SQLite schema 阻止完整业务页面启动，尚未在完整业务页请求面板复核隐藏页和人工刷新请求数。

## 文件结构

- `frontend/src/shared/pageDataCache.ts`：缓存控制器稳定读取、确认 singleflight 和共享批处理协调器。
- `frontend/src/composables/usePageDataRequestCache.ts`：KeepAlive 激活/停用时启停可见确认调度器。
- `frontend/src/composables/usePageDataCache.ts`：另一套页面缓存 composable 的相同生命周期边界。
- `frontend/src/views/accounts/useAccountListData.ts`：账户运行态快照轮询随页面激活状态启停。
- `frontend/src/scripts/regression/page-data-cache-regression.ts`：前端请求次数、并发和稳定读取回归。
- `frontend/src/scripts/regression/account-status-snapshot-polling-regression.ts`：账户状态轮询生命周期回归。
- `backend/src/modules/system-api/system-api-db-access.ts`：确认接口数据库访问模式。
- `backend/src/modules/page-data/page-data-change.service.ts`：Redis confirm Lua 快路径和结果解析。
- `backend/src/scripts/regression/system-api-db-access-regression.ts`：只读模式与 session touch 回归。
- `backend/src/scripts/regression/page-data-change-regression.ts`：Redis 一次 eval、delta/reset/epoch 语义回归。
- `backend/src/scripts/performance/page-data-confirm-benchmark.ts`：1 域、3 域顺序与并发确认基准。
- `backend/package.json`：登记确认基准命令。

### Task 1: KeepAlive 页面生命周期

- [x] 在两个页面缓存 composable 的回归中固定激活、停用、卸载和恢复即时同步。
- [x] 观察旧实现失败并完成生命周期控制器实现。
- [x] 隐藏 KeepAlive 页面停止 timer / focus 确认和账户状态轮询。
- [x] 恢复激活后立即同步，首次 mount 不额外重复确认。
- [x] `test:page-data-cache`、`test:account-status-snapshot-polling` 通过。

### Task 2: 单次刷新只确认一次

- [x] 有效 token 强刷只调用一次 confirm，冷启动仍保留双确认。
- [x] 刷新、scheduler 和 follower 请求复用本地确认链，不产生假 superseded。
- [x] changed / reset 稳定重载、迟到缓存写入 generation 防护和同步 throw 清理均有回归。
- [x] 页面缓存、delta、reset、并发回归通过。

### Task 3: 同作用域确认批处理

- [x] 同作用域微任务内跨 domain 合并。
- [x] 同 domain / 同 token 跨微任务在途复用，不同 token 隔离 delta。
- [x] broker 保持原 API 和错误传播，失败 settle 后可重试且不泄漏。

### Task 4: 后端只读模式

- [x] `POST /data-changes/confirm` 精确标记为 `read` 且不 touch session。
- [x] 保留 PostgreSQL session 权威鉴权，不使用 `noDb`。
- [x] System API 访问模式回归通过。

### Task 5: Redis 单往返确认

- [x] fake Redis 统计 1/3/4 域 confirm 均为一次 `EVAL` 且无旁路命令。
- [x] delta、日志损坏 / 缺口、range reset、旧 epoch、全域 reset 和 Redis 异常均有回归。
- [x] 单个 Lua 原子读取 epoch、sequence、reset sequence 和必要日志；TypeScript 继续统一判定动作。
- [x] HTTP 故障继续返回 `503 + Retry-After: 5`。
- [x] page-data core / HTTP / wiring 回归通过。

### Task 6: 性能与整体验证

- [x] fake 基准覆盖 1/3/4 域顺序和 20 并发，命令门禁为每 confirm 一次 `EVAL`。
- [x] 新增强制专用 URL、禁止 fake fallback 的真实 Redis 基准入口。
- [x] 前后端专项 typecheck、回归和 `git diff --check` 通过。
- [x] 根 typecheck、根 build 与最终差异检查通过。
- [x] 在专用测试 Redis state DB 执行真实 Lua / 网络基准，并确认唯一测试前缀已清理。
- [ ] 完整业务页面浏览器验收隐藏页与单次刷新请求数量。
- [x] 前端与后端分别完成规格审查、代码质量审查并修正发现。

## 验证记录

- 前端页面缓存、账户状态轮询和 typecheck：通过。
- 后端 page-data core / HTTP / wiring、System API DB access 和 typecheck：通过。
- fake Redis 1/3/4 域顺序与 20 并发：每 confirm 均为 `1 EVAL / 0 GET / 0 SET / 0 sendCommand`；该耗时只代表 fake，不代表真实网络。
- 根 typecheck、根 build、`git diff --check`：通过。
- 真实 Redis benchmark：在专用测试 state DB 完成 1/3/4 域顺序与 20 并发验证，每场景 2000 次；所有场景每 confirm 均为 `1 EVAL / 0 GET / 0 SET / 0 sendCommand`。
- 真实顺序场景 p50/p95/p99：1 域 `3.3187/5.6405/9.2010ms`，3 域 `3.2780/6.8391/16.4007ms`，4 域 `3.1352/6.3561/11.1922ms`。
- 真实 20 并发场景 p50/p95/p99：1 域 `5.0949/11.2932/78.2898ms`，3 域 `5.5351/9.2866/11.2933ms`，4 域 `5.7744/9.6160/11.5965ms`。本轮 1 域并发 p99 明显抬升；单轮 2000 样本不足以证明尾延迟稳定，上线前应重复运行真实基准，上线后继续观察端到端 HTTP 延迟。
- benchmark 前后 `SCAN MATCH benchmark:page-data-confirm:*` 均为 0 个键，确认随机唯一 epoch key 已精确清理，没有遗留测试键。
- 完整业务页面受本地旧 SQLite schema 缺少 `health_check_endpoint_mode` 阻断，未擅自迁移数据；生命周期和请求次数由可执行回归覆盖。

## 边界

- 不把确认接口标记成 `noDb`，不绕过 session 撤销、账户停用和角色变化。
- 不改变页面数据 token 协议，不牺牲 delta/reset/epoch 一致性换取速度。
- 不把相同 domain 的不同 token 强行合并成同一结果。
- 不部署生产；完成后只报告本地测试、基准和剩余风险。
