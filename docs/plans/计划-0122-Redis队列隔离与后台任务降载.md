# PLAN-0122 Redis 队列隔离与后台任务降载实施计划

> **给执行 Agent：** 使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans` 逐任务落地；每个任务先写失败回归，再做最小实现并运行目标验证。

**目标：** 在不删除、关闭或弱化任何页面、接口、审计、日志、用量、统计及维护能力的前提下，隔离 Redis 队列流量，解除非核心记录副作用对网关和管理请求的阻塞，并降低后台维护造成的周期性延迟尖峰。

**架构：** 高性能模式固定使用 cache、state、queue 三个独立 Redis 进程；Stream producer、blocking consumer 与管理命令使用独立连接。记录功能继续完整运行，但非核心副作用采用有界 best-effort：主请求只进行本地快速提交，超时或过载时记录 drop 后继续。大 payload 写入有界引用存储，Stream 只传小信封；维护入口保留，通过错峰、单飞、短批和压力门控降载。

**技术栈：** Node.js 22、TypeScript、Redis Streams、PostgreSQL、ingest-worker、stats-worker、ops-worker、PowerShell 7、pnpm。

---

## 基本信息

- 编号：PLAN-0122
- 状态：进行中
- 创建时间：2026-07-16
- 更新时间：2026-07-19
- 父计划：`docs/plans/计划-0120-生产管理端性能优先治理.md`
- 需求来源：生产 Redis state / queue 共享引发管理端和网关长尾；用户接受非核心记录偶发丢失，但要求功能不删减。
- 关联模块：Redis / Redis Streams / 网关副作用 / ingest-worker / stats-worker / record maintenance / 部署 / 监控 / 验证

## 现场依据

- 生产 `JUHE_AI_REDIS_STATE_URL` 与 `JUHE_AI_REDIS_QUEUE_URL` 指向同一 Redis `6380/db0`。
- 审计、usage、runtime log 和维护 Stream 的 XADD / XREADGROUP / XAUTOCLAIM 与会话、验证码、限流、并发槽和调度运行态竞争 Redis 单线程。
- 审计消息曾达到数百 KB 至约 `1.2MB`，XADD 可持续约 30 到 40ms。
- 日志反复出现 `runtime log Redis XADD timed out after 1500ms`、连接超时和断连。
- Redis commandstats 中 EVAL、XREADGROUP、XADD 和 XAUTOCLAIM 均有大量慢命令记录。
- 使用记录等 producer 的部分失败路径会等待队列或升级为进程 fatal，使非核心副作用影响主请求可用性。
- 审计 / usage 保留清理约每 10 分钟触发临时 worker，与 ingest、stats 和前台共享 PostgreSQL / CPU。

## 不删减功能的硬边界

- 不删除审计、运行日志、操作日志、公开接口日志、使用记录、统计和维护页面或接口。
- 不取消记录生产、消费、查询、详情、筛选和保留清理能力。
- 不通过返回空列表、假零值、静默隐藏错误或永久关闭 worker 达标。
- 非核心记录在队列断连、饱和、超时或超限时允许丢失；必须增加 drop count / bytes / reason 指标。
- 核心授权、API Key、会话和路由事实不进入 best-effort 队列，仍使用当前核心存储路径。
- usage 记录可能少量缺失不等于关闭用量功能；队列恢复后继续正常采集和展示。
- 维护任务只允许延期、缩小批次和错峰，不能被永久禁用。

## 数据分级

| 数据 | 处理策略 |
| --- | --- |
| session、验证码、限流、账号并发槽 | state Redis 独占，低延迟，不可与 queue 共享 |
| cache | cache Redis，允许淘汰和重建 |
| usage / audit / operation / public / runtime 记录 | queue Redis，有界 best-effort，功能保留，个别记录可丢 |
| audit / runtime 大正文 | 有界文件或 blob 引用，Stream 只传 hash / path / size |
| maintenance intent | 小信封、单飞、可合并、可延期 |
| 核心业务事实 | PostgreSQL 直接写，不使用 best-effort queue |

## 目标代码边界

| 文件 | 修改目标 |
| --- | --- |
| `backend/src/config/runtime.ts` | 生产强制 cache/state/queue 物理地址不同，移除共享生产例外 |
| `backend/src/shared/redis-client.ts` | producer / consumer / admin / state client 角色化与独立连接 |
| `backend/src/shared/redis-stream-queue.ts` | payload 上限、引用 codec、独立连接、批量 ACK / DEL |
| `backend/src/modules/runtime-logs/runtime-log-redis-producer.ts` | 抽取通用有界 best-effort producer |
| `backend/src/modules/gateway/usage/record-queue.service.ts` | 本地快速提交、drop 指标、禁止 fatal |
| `backend/src/modules/audit-logs/audit-log-queue.service.ts` | 大正文引用化、drop 指标、失败不阻塞请求 |
| `backend/src/modules/runtime-logs/runtime-log-index-queue.service.ts` | 统一 producer 和引用信封 |
| `backend/src/modules/record-maintenance/record-maintenance-queue.service.ts` | 小信封、单飞、压力门控和有界消费 |
| `backend/src/modules/background/maintenance-cleanup-jobs.ts` | 维护错峰、禁止重叠和单轮预算 |
| `docs/deploy/高性能模式部署指南.md` | 三 Redis 端口、持久化、启动和回退 |

## Task 1：固定三 Redis 物理隔离

**Files:**
- Modify: `backend/src/config/runtime.ts`
- Modify: `backend/src/scripts/regression/runtime-config-env-override-regression.ts`
- Modify: `backend/src/scripts/regression/performance-redis-boundary-regression.ts`
- Modify: `docker/.env.performance.example`
- Modify: `deploy/*`

- [x] 回归构造 cache/state 相同、state/queue 相同、cache/queue 相同、仅 DB 不同和 loopback 别名 URL。
- [x] performance 配置拒绝相同 host:port；同一 Redis 进程不同 DB 也视为共享。
- [x] 移除共享绕过开关，测试和生产使用相同物理隔离门禁。
- [x] 目标生产端口固定为 cache `6379`、state `6380`、queue `6381`；temporary 固定为 `16379/16380/16381`。
- [x] `test:runtime-config-env-override` 与 `test:performance-redis-boundary` 已通过；生产实际三 PID 验证仍待上线阶段完成。

## Task 2：拆分 Redis 连接角色

**Files:**
- Modify: `backend/src/shared/redis-client.ts`
- Modify: `backend/src/shared/redis-stream-queue.ts`
- Modify: `backend/src/scripts/regression/redis-stream-queue-regression.ts`

- [x] state URL 与 Stream queue URL 物理隔离，Stream producer、blocking consumer 和 ACK/inspect 全部只使用 queue URL。
- [ ] producer、blocking XREADGROUP consumer 和 ACK / inspect admin 使用三类 dedicated client。
- [ ] producer 禁用 offline queue，连接不可用时快速失败，不在客户端 FIFO 堆积。
- [ ] ACK 按一批 message IDs 执行 XACK；XDEL 为 best-effort 批量删除，不使用逐条大 Lua 循环阻塞 Redis。
- [x] `test:redis-stream-queue` 已通过；真实 macOS 6380/6381 commandstats 隔离待上线验证。

## Task 3：通用有界 best-effort producer

**Files:**
- Create: `backend/src/shared/bounded-redis-stream-producer.ts`
- Modify: `backend/src/modules/runtime-logs/runtime-log-redis-producer.ts`
- Create: `backend/src/scripts/regression/bounded-redis-stream-producer-regression.ts`
- Modify: `backend/package.json`

- [ ] 接口提供 `tryEnqueue(payload, estimatedBytes): boolean`，调用只执行本地容量判断和异步调度。
- [ ] 每个 producer 配置 maxItems、maxBytes、connectTimeout 和 commandTimeout。
- [ ] 本机 queue Redis 建议 connect timeout 300ms、单次 XADD 预算 100ms；主请求不 await XADD 完成。
- [ ] 断连、超时、饱和、oversize、codec failure 分别计数并记录有界日志。
- [ ] 禁止无限重试、同步回退到 IPC / 内存可靠队列和 `scheduleProcessFatalError`。
- [ ] runtime log 迁移到通用 producer 后，现有采样和功能回归保持。
- [ ] 运行新增 `test:bounded-redis-stream-producer`。
- [ ] 运行：`pnpm --filter juhe-ai-backend test:runtime-log-redis-enqueue-failure-boundary`。

## Task 4：迁移各记录 producer

**Files:**
- Modify: `backend/src/modules/gateway/usage/record-queue.service.ts`
- Modify: `backend/src/modules/audit-logs/audit-log-queue.service.ts`
- Modify: `backend/src/modules/operation-logs/operation-log-queue.service.ts`
- Modify: `backend/src/modules/public-api/public-api-log-queue.service.ts`
- Modify: `backend/src/modules/runtime-logs/runtime-log-index-queue.service.ts`
- Modify: `backend/src/modules/record-maintenance/record-maintenance-queue.service.ts`

- [ ] usage、audit、operation、public、runtime 和 maintenance producer 分别设置本地 item / byte 上限。
- [ ] 主请求只收到“本地接受 / 已丢弃”的快速结果；非核心 drop 不改变主请求 HTTP 结果。
- [ ] drop 指标按 queue、reason、count、bytes 输出到 background runtime。
- [ ] 消费和查询入口全部保留；Redis 恢复后 producer 自动恢复投递。
- [ ] 运行 usage、audit、operation、public、runtime 和 maintenance 现有队列回归。

## Task 5：大 payload 引用化

**Files:**
- Create: `backend/src/shared/redis-stream-payload-reference.ts`
- Modify: `backend/src/shared/redis-stream-queue.ts`
- Modify: `backend/src/storage/audit-log-payload-blobs.ts`
- Modify: `backend/src/modules/audit-logs/audit-log-stream-codec.ts`
- Create: `backend/src/scripts/regression/redis-stream-payload-reference-regression.ts`
- Modify: `backend/package.json`

- [ ] 编码后 `<=64KiB` 可以内联；超过后异步 gzip 写入有界 `data/queue-payloads/<shard>/<id>.json.gz`。
- [ ] Stream envelope 包含 schemaVersion、id、storageKey、sha256、rawBytes、compressedBytes 和 createdAt。
- [ ] `enqueueEncoded` 对最终 XADD 字段执行 `128KiB` 硬上限；超过直接 drop，禁止 MB 级字段进入 Redis。
- [ ] 常规 envelope 目标 `<=32KB`。
- [ ] consumer 校验 hash 和大小后还原；缺失或损坏引用计为 poison，并 ACK / DEL，不能无限重投。
- [ ] 消费成功后异步删除引用；失败最多 3 次或过龄 5 分钟后丢弃并计数。
- [ ] 引用目录清理使用 cursor / 分块窗口，不允许全目录一次性读入内存。
- [ ] 回归覆盖 1.2MB 审计正文可还原、Stream 字段不超限、ACK 后引用删除和损坏引用不阻塞。

## Task 6：维护任务降载

**Files:**
- Modify: `backend/src/modules/background/maintenance-cleanup-jobs.ts`
- Modify: `backend/src/modules/record-maintenance/record-maintenance-queue.service.ts`
- Modify: `backend/src/modules/record-maintenance/temporary-maintenance-worker-runner.ts`
- Modify: `backend/src/modules/background/worker-scheduler.ts`

- [ ] 同 job type 单飞，重复 intent 合并；禁止同表清理任务重叠。
- [ ] 审计热清理从每分钟调整为每 5 分钟，data retention 从每 10 分钟调整为每 30 分钟；入口和保留目标不变。
- [ ] 每片最多 100 行或 500ms，批间让出事件循环；maintenance PG pool 预算 1 到 2。
- [ ] queue lag >5000、producer pending >50% 或 event-loop P95 >100ms 时，本轮延期 60 秒；延期不删除任务。
- [ ] 审计 orphan 只按候选 blob ID / 索引 cursor 查找，不执行无界全局扫描。
- [ ] 运行：`pnpm --filter juhe-ai-backend test:temporary-maintenance-worker`。
- [ ] 运行：`pnpm --filter juhe-ai-backend test:record-maintenance-queue`。
- [ ] 运行：`pnpm --filter juhe-ai-backend test:audit-log-retention`。

## Task 7：观测和部署文档

**Files:**
- Modify: `backend/src/modules/background/background-ipc.ts`
- Modify: `backend/src/modules/stats/stats.routes.ts`
- Modify: `docs/deploy/高性能模式部署指南.md`
- Modify: `docs/develop/测试与验证说明.md`

- [ ] 增加 producer pendingItems / pendingBytes、enqueue P95/P99、dropTotal / dropByReason、referenceFiles / referenceBytes、Stream lag / pending 和 maintenance deferredCount。
- [ ] 文档写清 cache/state/queue 6379/6380/6381、持久化、noeviction、容量和回退。
- [ ] 记录功能页面显示 drop 和 backlog 诊断信息，不伪装为空队列。

## Task 8：验证与灰度

- [ ] 运行 runtime config、Redis boundary、Stream queue、bounded producer、payload reference、maintenance 和 audit 回归。
- [ ] 运行：`pnpm --filter juhe-ai-backend typecheck`。
- [ ] 运行：`pnpm build`。
- [ ] 发布前记录 15 分钟基线：管理接口、事件循环、Redis 6380/6381 latency、Stream lag/pending、PG active query 和错误率。
- [ ] 先只拆 Redis，A/B 30 分钟；再发布连接隔离；再发布 best-effort；最后发布 payload 引用和维护降载。
- [ ] 冒烟验证所有管理页面、日志 / 审计 / 用量 / 统计列表详情、网关请求和 maintenance runtime。

## 验收标准

- [ ] cache/state/queue 使用三个物理 Redis 进程，生产共享配置快速失败。
- [ ] state Redis P99 `<5ms`，测试窗口没有 queue 命令出现在 state 实例。
- [ ] 管理读 P95 `<300ms`、P99 `<800ms`。
- [ ] server / db-service event-loop P95 `<100ms`；ingest / stats P95 `<250ms`。
- [ ] 常规 Stream envelope `<=32KB`，内联阈值 `64KiB`，最终字段硬上限 `128KiB`。
- [ ] 非核心副作用本地提交 P99 `<1ms`。
- [ ] 队列断连、超时和饱和不会造成主请求错误或进程退出。
- [ ] 审计、日志、使用记录、统计和维护页面 / 接口全部保留。
- [ ] 1.2MB 测试审计正文通过引用可查看；过载 drop 有明确指标。
- [ ] 维护期间管理接口 P95 劣化不超过 20%。

## 发布与回退

1. 新增 6381 queue Redis，先不改 producer 语义。
2. 切换 `JUHE_AI_REDIS_QUEUE_URL`，观察 state / queue 指标。
3. 发布连接角色化和 best-effort producer。
4. 发布 payload 引用化。
5. 发布维护错峰和压力门控。

- URL 切换异常时恢复上一配置并重启同一已验证发布包。
- best-effort 异常时回滚发布包，不恢复“队列失败触发进程 fatal”。
- 引用 codec 异常时停止新 producer，允许丢弃未消费 backlog；不增加双写兼容。
- 不删除 queue Redis 和引用文件，待定位后按明确路径处理。
- 任何阶段出现核心 CRUD、授权、API Key、网关路由或页面入口不可用，立即回退。

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-07-16 | queue Redis 与 cache/state 物理隔离 | Redis 单线程大 Stream 命令会拖慢会话、限流和并发 | 生产新增 6381 queue 资源 |
| 2026-07-16 | 非核心 producer 超时/过载允许丢弃 | 日志、审计、用量副作用不能决定主请求可用性 | 记录可能缺失，但功能入口不删除 |
| 2026-07-16 | 大 payload 使用引用信封 | 避免 MB 级 XADD 阻塞 Redis | 增加有界引用存储和清理 |
| 2026-07-16 | 维护任务延期而不关闭 | 保留维护功能并削减周期性争抢 | 清理完成时间变长，主链路长尾下降 |

## 验证记录

- 2026-07-19 已完成三 URL 物理门禁、Docker 角色持久化、原子 queue fence、usage/audit 三次有界入队、六类瞬时错误不触发 process fatal、共享 Stream 契约和独立 one-shot drain CLI 的定向回归与类型检查。
- 2026-07-19 独立复核加固：物理端点按 canonical host:port 判重，unknown lag/pending fail-closed，one-shot 启动消费者前检查既有 group，坏消息保留 pending，公开接口日志在首次分流前固化幂等 ID；macOS root 服务操作与普通发布阶段分离。
- macOS 角色安装/只读验证脚本及 temporary 三实例接入已完成静态门禁；真实 macOS LaunchDaemon、生产三 PID、AOF rewrite 与性能指标仍待正式上线阶段验证，不提前声明通过。
- 现场依据见 `docs/reports/生产管理端卡顿根因与性能优先治理报告-2026-07-16.md`。
- 实施后把实际命令、A/B 数据、drop 数量和功能冒烟结果回填本节。

## 完成总结

- 当前状态：待实施。
- 关闭条件：三 Redis 隔离、连接角色化、best-effort、payload 引用和维护降载完成，功能与性能门禁全部通过。
