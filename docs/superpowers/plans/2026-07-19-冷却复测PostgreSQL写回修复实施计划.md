# 冷却复测 PostgreSQL 写回修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 PostgreSQL 模式下冷却复测成功与失败写回的 `42P18` 参数类型错误，补齐真实 PostgreSQL 回归和队列耗尽错误日志，并完成安全的主分支集成与 macOS 生产发布。

**Architecture:** 在冷却复测 repository 内按调用方实际提供的期望值动态拼接乐观并发条件，保证 PostgreSQL 不再解析无类型的 `? IS NULL` 参数，同时让 SQLite 与 PostgreSQL 复用同一组条件语义。后台队列只增强耗尽日志，不改变批量、并发、公平性或调度策略。发布不改 schema，不清理 Redis，通过现有临时服务接管流程进行候选验证和主服务切换。

**Tech Stack:** Node.js、TypeScript、PostgreSQL、SQLite、Redis、pnpm、PowerShell 7、macOS/launchd、Nginx、Git worktree

---

## Task 1：用回归测试固定故障与并发保护契约

**Files:**
- Modify: `backend/src/scripts/regression/account-probe-postgres-smoke.ts`
- Modify: `backend/src/scripts/regression/cooldown-retest-recovery-regression.ts`

- [x] 在 PostgreSQL smoke 的冷却复测失败写回中传入当前 `configRevision` 和当前 observation timestamp，断言当前观测可以写回。
- [x] 增加冷却复测成功写回用例，携带相同两个期望值并断言账号恢复为可调度状态。
- [x] 增加陈旧 config revision 与陈旧 observation timestamp 的成功/失败写回断言，确认返回 `changed: false` 且运行态不被覆盖。
- [x] 在本地回归中增加源码守卫，禁止冷却复测写回重新出现 `? IS NULL`，并要求队列耗尽日志携带结构化错误字段。
- [x] 运行 `pnpm --filter juhe-ai-backend test:cooldown-retest-recovery`，确认新增源码守卫先失败。
- [x] 生产 trace 已提供旧实现 `42P18` 证据；测试环境通用 smoke 被既有 provider catalog fixture 漂移提前阻断后，改用 `cooldown-retest-postgres-smoke` 在 `192.168.1.203` 一次性隔离数据库验证真实 repository SQL，隔离库已删除。

## Task 2：实现动态并发保护条件与耗尽错误日志

**Files:**
- Modify: `backend/src/storage/account-cooldown-retest.repository.ts`
- Modify: `backend/src/modules/background/cooldown-account-retest.service.ts`

- [x] 在 repository 内新增文件私有条件构造器，根据 `expectedConfigRevision` 和 `expectedObservationStartedAt` 是否存在返回 SQL 片段及参数。
- [x] 让 SQLite/PostgreSQL 的成功与失败写回共同使用该构造器；成功路径始终校验 config revision，observation 仅在存在时校验，失败路径按实际提供值追加条件。
- [x] 保持 PostgreSQL 失败写回的行锁事务和 group stats dirty 标记顺序不变。
- [x] 在 `onExhausted` 日志中合并 `errorLogFields(event.error, ...)`，保留原事件、账号、任务名和尝试次数字段。
- [x] 运行 Task 1 两项回归，确认 RED 转 GREEN。

## Task 3：完成本地与真实中间件验证

**Files:**
- Verify: `backend/src/storage/account-cooldown-retest.repository.ts`
- Verify: `backend/src/modules/background/cooldown-account-retest.service.ts`
- Verify: `backend/src/scripts/regression/account-probe-postgres-smoke.ts`
- Verify: `backend/src/scripts/regression/cooldown-retest-recovery-regression.ts`

- [x] 运行 `pnpm --filter juhe-ai-backend test:database-client`。
- [x] 运行全仓 `pnpm typecheck` 及 canonical `pnpm build`。
- [x] 在隔离 PostgreSQL/Redis 环境运行专用 PG smoke，确认当前成功、当前失败、陈旧 config、陈旧 observation 与可选保护语义全部通过。
- [x] 检查退出码、测试数量、`git diff --check`、工作区差异及隔离资源清理结果，确保无密钥、临时日志或无关生成物。

执行记录：`test:postgres-schema-sql` 与 `test:sqlite-high-volume-guards` 在当前 `origin/master` 分别存在 provider catalog 整数/布尔谓词和资源授权源码守卫漂移，均与本次差异无关；本任务未扩大范围修复。

## Task 4：同步、提交并独立复核

**Files:**
- Review: 本分支相对最新 `origin/master` 的完整差异

- [ ] `git fetch origin` 后合并最新 `origin/master`，有冲突时仅解决本任务文件并重跑受影响验证。
- [ ] 提交测试与实现，提交信息准确描述 PostgreSQL 冷却复测写回修复。
- [ ] 启动两个只读复核 Agent：一方检查需求、调用链、正确性和行为回归；另一方检查测试、边界、安全、代码质量和上线风险。
- [ ] 主 Agent 核验、去重并修正已确认问题；核心逻辑变化后执行针对性再审和相关回归。

## Task 5：集成 master 与 feature 分支

**Files:**
- Integrate: `codex/cooldown-retest-pg-writeback-20260719`
- Integrate: `master`
- Integrate: `feature/20260706-go`

- [ ] 在干净的 master 集成 worktree 合并修复分支，执行要求级回归后推送 `origin/master`。
- [ ] 将最新 master 反向合并到 `feature/20260706-go`；如果当前 feature 工作区的用户未提交修改阻止安全合并，不 stash、不覆盖、不提交无关内容，改为明确报告阻塞并只执行可安全完成的远端集成步骤。
- [ ] 推送成功集成的 feature 分支，核对本地/远端提交图与分支指向。

## Task 6：按临时服务接管方案发布 macOS 生产

**Files:**
- Create: `F:\服务部署\juhe-ai\09-上线计划\2026-07-19-冷却复测PostgreSQL写回热修.md`
- Follow: `F:\服务部署\juhe-ai\03-部署流程\macOS临时服务接管发布方案.md`

- [ ] 先落地正式上线计划、回滚点和验证矩阵，构建并核对基于已推送 master 的发布包。
- [ ] 只读检查生产版本、主/临时服务状态、端口、磁盘、数据库/Redis 连通性和 WireGuard/Caddy/Nginx 入口，不做生产写入。
- [ ] 第一次生产写操作前执行 `deployment-plan-lock.ps1 acquire`；锁不可用时等待或退出，不绕过互斥机制。
- [ ] 完成数据库与 Redis 配对备份、临时服务候选部署、3101/3102 与 3099 入口验证；不运行 schema 变更，不清理业务 Redis。
- [ ] 候选验证通过后切换主服务，检查 API/worker/DB service、冷却复测日志、目标账号重新参与调度以及外部入口；持续观察至少 60 秒。
- [ ] 验收失败则按计划回滚程序与入口并复验；成功后清理本次临时资源、记录 `VERIFY_SUCCESS`，最后释放部署计划锁。
