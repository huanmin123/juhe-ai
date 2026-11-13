# BUG-0360：F3/F4 每实例写租约 keeper 失租无复活，审计/操作日志持久化停摆至进程重启

- 级别：P1（审计面数据持续丢失，业务主链路不受影响）
- 状态：已修复（2026-11-11，随统一发布窗上线后观察）
- 登记：docs/待办与阻塞事项.md #T203

## 现象

生产（103 单机）2026-11-11 02:50 起 F3 审计持久化停摆：`juhe_dataset.audit_logs` / `audit_payload_blobs` / `audit_payload_refs` 最后一行停在 02:48-02:49，此后零写入；gateway 每小时 800-1017 条 `F3 owner lease renewal failed; dropping audit capture`（信封撤回重试耗尽 4916 条）。F4 操作日志同机制失租（9 次租约丢失 ERROR + 02:51 retention 55P03；该表仅管理员操作写入，缺口不可直接观测）。业务主链路（/v1、usage、调度）全程正常。

这不是首演：2026-11-09 23:20 同一缺陷已造成 audit_logs 停摆 25.7 小时（23:00 档 347 行后全零），直到 2026-11-11 01:05 发布重启才恢复——重启后 01:05-02:50 正常写入 509 行，恰好证明「重启即恢复、失租即永久」。

## 根因

提交 `1e9cad155`/`099a2032a`（2026-11-07 多节点 P2）把每族租约拆成两条线：

- **写租约 keeper（每实例键 `f3-audit-log-persistence:<instanceID>` / `f4-operation-log-persistence:<instanceID>`）**：main 装配（main.go :729/:797），producer 以它为 LeaseSource 门禁；
- **retention keeper（全局默认键）**：独立 store，经监督组件（`F3 audit-log-owner` / `F4 operation-log-owner`）→ `runRetentionOwnerWithLease` 壳 → `RunOwner` 运行，保留 supervisor 重启 → `RunOwner` 重入 → `keeper.reacquire` 的复活回路。

拆分时复活回路只留在了 retention keeper 上。写租约 keeper 的续租环在「连续失败超过 2×TTL 放宽窗口」或「续租 0 行命中」时 fatal 进入 terminal（`Lease()` 永回零值），而进程内没有任何组件会调用它的 `reacquire`——producer 每条记录门禁失败即丢弃，直到进程重启。生产证据：默认键行被本实例复活（fence 405→413、30s 活跃续约），实例键行冻结在 02:50:52（token 1）；同一分钟 F3 fatal 日志 token 405 与 1 交替出现，即两 keeper 同时失租、只有默认键的那个被救回。

诱因为已知 BUG-0313 形态磁盘 I/O 风暴（2026-11-11 三波：02:02 io_full≈50、02:45-56 峰值 84、06:28-35 峰值 92，PG 全线 LWLock/WALWrite 饥饿、10-100s commit），风暴归因与治理是独立存量事项，不在本缺陷范围。

## 修复

拓扑不动（多节点设计 §3.3/§7.1「每实例写入 lease + 全局 retention lease」维持），只补复活回路：

1. `auditlog.LeaseKeeper` / `operationlog.LeaseKeeper` 各导出 `Revive(ctx)`：封装既有 `reacquire`（等旧续租代退出 → 同实例键重新 `AcquireOwnerLease` → fence 递增 → 重置一次性状态并重启续租环），healthy keeper 上 no-op；keeper 已 Close 后拒绝复活（避免重建无法释放的租约）。
2. cmd 装配新增泛型复活 runner（`writer_lease_reviver.go`，接口 `Lost/LostError/Revive`）：外层每次迭代重读 `keeper.Lost()`（复活后通道换代），丢失后按 5s 周期（与 retention 壳同约定）重试 `Revive` 直到成功或 ctx 取消。
3. main 注册两个监督组件 `F3 audit-writer-lease-reviver` / `F4 operation-log-writer-lease-reviver`（F4 与 operationLease 同条件装配，nil 时跳过）。
4. 回归测试：两包各一条真实 PG 测试——双 store 复刻生产装配（实例键写 store + 默认键 retention store），真实失租（SQL 置过期 + rival 接管）→ 断言门禁丢弃 → rival 释放 → `Revive` → fence 递增 → producer 落库恢复 → **默认键行全程未被触碰**（反分裂断言）；外加 fake 单测覆盖 healthy no-op / 复活成功 / Close 后拒绝。

语义边界（不变式）：fence 仍单调递增、租约行只过期不删除（BUG-0317 契约）；跨实例同实例 ID 互斥语义不变（部署侧仍须保证实例 ID 唯一）；producer 门禁「失租即丢、不排队」语义不变；retention 组件与启动期 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT` 等待逻辑不变。

## 影响与遗留

- 生产缺口：audit_logs 缺失区间 2026-11-09 23:20 → 2026-11-11 01:05、2026-11-11 02:49:50 → 修复发布时点；按 T203 下一步④默认不补偿（评估后裁决）。
- 遗留清理（随统一发布窗执行）：两个租约表的历史实例键残行（2026-11-08/09 五个实例 ID 行，`audit_log_owner_leases` / `operation_log_owner_leases` 各 5 行）为无主旧行、永不匹配，可在发布窗内按 owner 不等于当前实例 ID 且 lease_until 已过期的口径一次性 DELETE。
- 监控补位（#T204）：`AppAuditPersistErrorRate` 等三条 2026-11-03 规则未同步 infra，本次停摆无告警；同步另行执行。
- 改进项（复审非 blocker，**已随 T207 批次/BUG-0363 完成**）：`writer_lease_reviver.go` 对「keeper 已关闭」的判定原以错误文案子串桥接，极端情况下底层错误恰含 closed 字样（如 `sql.ErrConnDone`）会被误判为关闭态而停止重试；两包已导出哨兵错误（`ErrKeeperClosed`），reviver 改 `errors.Is` 精判。

## 关联

- [多节点部署与不停机发布设计](../functions/多节点部署与不停机发布设计.md) §3.3/§3.4/§7.1
- [审计日志保全策略设计](../functions/审计日志保全策略设计.md) 数据流 §9、[操作日志设计](../functions/操作日志设计.md)
- BUG-0313（磁盘 I/O 风暴）、BUG-0317（fence 单调/行不删契约）、BUG-0228（启动租约等待）
- docker/single-server/README.md F3/F4 契约节
