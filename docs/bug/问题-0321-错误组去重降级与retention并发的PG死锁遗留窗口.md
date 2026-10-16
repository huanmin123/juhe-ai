# BUG-0321 错误组去重降级与 retention 并发的 PG 死锁遗留窗口

- 级别/状态：P3（未触发的理论窗口，兜底完备），登记已知问题，未排期
- 引入：BUG-0320 批次的错误组存储去重（`store.go` `dedupePreviousErrorGroupSample`，随下次发布上线）

## 窗口形态

PG 形态下两个事务的加锁顺序在窄场景互逆：

- Persist(新失败事件 E_new)：先持自身 payload 的 blob 通知锁（`lockBlobLifecyclePlans`），事务末尾去重降级时再取旧保留样本 E_prev 的 per-audit-ID 生命周期门（`dedupePreviousErrorGroupSample` → `lockAuditLogLifecycleTx`）；
- retention 删除 E_prev（7 天硬过期批）：先取 E_prev 的 audit 门（`lockAuditLogLifecycleIDs`），后对候选 blob 取通知锁（retention.go:105→124→139）。

当 E_prev 与 E_new 共享 canonical blob（同一错误形态重复正文命中内容寻址去重时常见）且两个事务毫秒级重叠，构成 AB-BA 死锁。

## 兜底与影响

PG 死锁检测器自动中止一方（40P01）：persist 侧该条审计按既有 best-effort 语义丢弃并计数（不影响 /v1 业务），retention 侧下一轮 tick 重试。无数据损坏、无 GC 误删（GC 意图均经 NOT EXISTS 复核）。SQLite 形态由 `writeMu` 全量串行，天然免疫。

触发需同时满足：E_prev 恰为当前保留样本、已进入 retention 删除批、同指纹新事件恰在该删除事务窗口内 finalize——概率低，当前修复（重排 Persist 全局锁序：把 audit(E_prev) 门提前到 `lockBlobLifecyclePlans` 之前）涉及全锁序审计，改动面大，故登记不排期。

## 验证与观察

- 若生产日志出现 persist 侧 `40P01`/`deadlock detected` 且 trace 落在 `dedupePreviousErrorGroupSample`，即本窗口触发实例；单发可忽略，密集出现再升级排期。
- 相关文档：`docs/functions/原始审计日志设计.md` 错误组存储去重节。

## 后记（2026-10-16）

去重语义改为"样本按 10 分钟窗口移交、同窗口重复出生即降级"（见 `原始审计日志设计.md` 错误组存储去重节）后：

- 降级路径从"每条新事件"收敛为"仅跨窗口移交时"，触发频率大幅下降，本互锁窗口随之收窄；锁序本身未变，结论（P3、兜底完备、不排期）维持。
- PG 写入侧新增同指纹事务级 advisory 门（`lockErrorGroupFingerprintTx`，seed 763847296）：同指纹并发 Persist 在窗口分类读取前串行化。该门在 persist 锁序头部（audit-ID 门之后、blob 锁之前）获取，retention 不取此门，不引入新锁环；同窗口重复不再并发物化 payload，也消除了"窗口内两个 complete 样本"的并发偏差。
- 行号引用改为函数名引用（行号随代码演化漂移，不再维护）。
