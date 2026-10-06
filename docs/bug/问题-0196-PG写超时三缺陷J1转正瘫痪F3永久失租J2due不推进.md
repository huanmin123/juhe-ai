# 问题-0196：PG 写超时三缺陷——J1 转正瘫痪 / F3 永久失租 / J2 due 不推进

- 发现：2026-09-27 00:30 前后（用户反馈生产新增 `supeai.cc` 账户配置后不可用，排查实锤）。与 0184/0192 同族但不同层：死锁已被 0192 的 advisory 锁闭环治愈（生产 `pg_stat_database.deadlocks` 稳定在 1283 零增长），本问题是**锁串行化 + 阵发性写超时**之下三个组件的脆弱语义被引爆。
- 状态：已上线（随 2026-10-06 前后批次发布；2026-10-06 状态同步），真实环境观察项保留（due 列前进、J1 新账户转正、F3 无长期 503、deadlocks 零增长）

## 现象与根因（生产取证 + 代码取证）

1. **J1 cursor 保存失败丢弃整个探针 outcome**（新账户卡 `pending_test` 的直接原因）。
   - 链条：探针上游成功 → `SaveKeyCursor`（独立 5s 窗口，`accounthealth/executor.go`）遇 PG 争用超时 → 错误上抛 → 该账户 outcome（含 `activation_success`）整体不落库 → 投影器无物可投 → `status=pending_test、schedulable=0` 永不转正。
   - 生产实锤：J1 每轮扫描死在同一句（00:35:51、00:48:04）；`supeai.cc` 账户（`acc_97b2f9afbf388ab2`）无 cursor 行、无健康检查时间；而 `juhe_jobs.account_health_key_cursors` 全表 316 行——失败只发生在争用窗口。
   - 定性：cursor 只是"下轮从第几个 key 开始"的轮询记账，丢失代价是回到 key 0，不应对冲业务结果。
2. **F3 audit owner lease 一次续租超时即永久 terminal**（gateway 持续 unhealthy + 审计丢弃）。
   - 链条：一次 5s 续租超时 → `fatalOnce` 永久关闭 keeper → supervisor 重启只重放存储错误、不再触库 → gateway owner-less 直到进程重启。生产实锤："连续失败 25 次"中仅首次是真实 SQL 超时，其余全是重放；期间链审计逐条丢弃（仅 warn）、`/__aisys__/health` 持续 503。
3. **J2 周期余额刷新从不回写 due 游标**（持续写放大源）。
   - 全仓库 `balance_query_next_refresh_at` 的 UPDATE 只有首探使能链/网关管理面 4 处，周期刷新路径零写回。生产实锤：68+ 启用账户 due 列最大值停在 2026-09-25 01:38，全部恒 due → 每 5s 扫描周期无限重复探测同一批账户（约 200 写事务/轮 + 等量上游 HTTP）。

## 修复（2026-09-27，随 gateway+jobs 二进制发布）

1. **J1**：`saveProbeKeyCursor` 失败降级为 warn 日志（`account_health_key_cursor_save_failed`），两处调用点不再上抛，outcome 照常返回；5s 独立窗口与 `context.WithoutCancel`、lease fence 语义不变。契约同步：`docs/migration/J1-账号健康探活完整迁移契约.md` §5。
2. **F3**：续租传输错误在 `2×TTL`（`ownerRenewGraceFactor=2`）放宽窗口内按 TTL/3 节拍重试（每次尝试仍 5s bounded context）；0 行更新（过期/被接管，SQL 守卫 owner_id+fence_token+lease_until>now 保证不可能续错）或超窗才按既有 terminal 放弃；keeper 终态后 `RunOwner` 入口 `reacquire` 等旧代退出后全新竞选（等代际 done 建立 happens-before，-race 通过）。契约同步：`docs/migration/F3-原始审计日志持久化与保留迁移计划.md` §3。
3. **J2**：新增 `BusinessDueStore`（`shared/platform/accountbalance/business_due.go`），周期触发（`TriggerPeriodic`）已结算 outcome（成功/失败/unsupported，对齐 J2 契约"失败也按用户配置周期安排下次刷新"）后独立短事务推进 due：毫秒截断 UTC RFC3339 文本写 `balance_query_next_refresh_at`（text 列）与 `updated_at`；fence 用读侧同款 `::timestamptz` 等值（recovery 候选 `IS NULL`）+ `config_revision` + `j2CandidateSQL` 同款守卫；replay/stale/first_probe/manual 不推进。死上游/unsupported 账户从"每 5s 一轮"回归"按配置周期（缺省 5min ±jitter）重试"。契约同步：`docs/functions/AI账户上游余额查询设计.md` §5、`docs/migration/J2-余额刷新完整迁移契约.md` 两处事实性修订。

## 验证

- 包测试全绿：`accounthealth`（含 cursor 失败保 outcome 两用例）、`auditlog`（含 `-race` 下放宽窗口/重取/真实失租生命周期 17 用例）、`accountbalance`（BusinessDue 6 用例）；四模块 `go build` 通过；独立终审（glm_5.3_max）三修复 PASS / PASS-with-notes / PASS，无 blocker。
- 生产列类型核对：`accounts.updated_at`、`balance_query_next_refresh_at` 均为 text，SQL 绑定类型匹配。
- 未闭环→已闭环：`TestPostgresBusinessDueAdvanceSmoke`（真实 PG 语句级）于 2026-09-27 复查时对刚恢复的 dev PG 实例执行 PASS（text due 列、`::timestamptz` fence、候选守卫全部实库验证）。期间发现 dev PG 曾整体缺位（容器被删、6432 无监听），后被拉起为全新未 bootstrap 实例（缺 `juhe_ai_sub2api_dev_app` 角色）——此环境状态导致各包 PG 门禁用例（w7c/w10b/w16a）报"创建临时子库失败: role does not exist"，属环境失败而非回归，dev bootstrap 完成后自愈。

## 遗留（定级均不阻塞）

1. **producer 旧 fence 快照缺口**：真实失租 + reacquire 换新 fence 后，gateway 进程内 producer 仍持启动时 lease 快照（`main.go:557 NewProducer(auditLease.Lease(),...)`），审计逐条被拒直到进程重启。放宽窗口使真实失租罕见，定级低概率×中影响；修复涉及 `cmd/juhe-ai-gateway/main.go`（当时有并行在途改动，避开），留独立任务。
2. `LoadKeyCursor` 读取失败仍硬错误（窄窗口、可自愈，定级低）。
3. J1 runCycle/outbox drain 其余"首错中止整轮"路径（收敛为本轮缺失、下轮重试，无永久卡死形态，未改）。
4. `worker_balance_detect.go` PG 分支把 `time.Time` 直接绑进 text due 列（存成 PG 渲染文本），与读侧只认 RFC3339 存在预存方言分歧。复查锐化其后果：此类存量值账户被读侧永久跳过，**不进入"探针→J2 推进"闭环**，对这些账户"发布后核对 due 列前进"必然不成立——建议把格式核查/回填安排在发布观察之前（排除类已补记入 `docs/functions/AI账户上游余额查询设计.md`）。
5. `account-list-availability-projection-maintenance` 的 7001001 单键排队压力未调优（1s×4 并发×长扫描事务持锁）；复查定量：C 落地后新增 due 推进约 1.6 行/s 稳态（首批积压突发约 11 行/s），消费容量高出 2-3 个数量级，且修复前周期路径约 95 写事务/s 降为约 1.6/s，总写压净降约 60 倍，发布后照常关注队列深度即可。
6. **F4 `operationlog.LeaseKeeper` 同族缺口（复查新登记）**：`renewed=false` terminal 后无 reacquire，共享租约路径 owner-less 持续到进程重启（`gateway/internal/operationlog/retention.go:37-43`、`main.go:600`）；触发需持续 >TTL(30s) 的续租中断，比修复前 F3 窄、未因本次发布恶化。建议后续任务移植 B 的 reacquire/loopDone 模式。另 supervisor 的 `consecutiveFailures` 跨成功运行不归零（重启延迟恒 30s+jitter，仅抬高分量恢复延迟上界，一并随该任务处理）。
7. A 降级副作用（复查定级 minor）：cursor 持续写失败期间多 Key 账户恒从 key 0 重探、key[0] 承担全部探针流量（轮询失衡、探针周期可能拉长）；保存恢复即自愈，不影响网关请求侧 Key 轮换。
8. 理论缝隙记录（复查）：`reacquire` 未重置 `closeOnce`（owner.go:245-253）；生产路径 `auditLease.Close()` 仅进程退出调用一次，不构成实际问题。

## 复查（2026-09-27）

第二轮独立对抗性复审（全新 glm_5.3_max 实例）结论：**可部署，无新增 blocker/major**。修复 B 恢复上界量化为 45-105s（租约过期 ~T+20s → terminal ~T+60-70s → supervisor 退避 → reacquire ≤5s；0 行拒绝时租约必然已过期，reacquire 立即成功，不存在等待旧行过期的路径）；修复 C 与 J1 病灶的交互经定量核算为净减压（见遗留 #5）。生产侧确认截至复查时仍运行 00:04-00:52 构建的旧二进制（容器内无修复特征串），J1 于 02:05:51 再现同款 cursor 超时、due 列仍停在 2026-09-25 01:38 (+08)——缺陷持续复现，修复待发布。

## 关联

- 0184/0192：advisory 锁闭环（本次前置，未回改）。
- 生产观察项见 0192 同款流程：发布后核对 due 列前进、J1 新账户转正、F3 无长期 503、deadlocks 零增长。

2026-10-06 状态同步：滚动全量发布默认规则（修复已入库）；HEAD 复核「reacquire（auditlog/owner.go）」命中。
