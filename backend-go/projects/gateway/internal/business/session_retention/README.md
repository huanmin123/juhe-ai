# session-retention

该目录是方案 A `session-retention` transaction group 的独立 Go 运行时原语，覆盖 manifest operation `cleanup_expired_system_sessions`。

冻结的 Node 语义：

- 默认 `expiredBefore` 使用当前 UTC 时间的毫秒 ISO 表示；显式 cutoff 先验证为 RFC3339，再保留原始字符串，避免改变 Node/SQLite 的文本比较语义。
- `limit` 等价于 `Math.max(1, Math.trunc(limit))`；Go 接口使用整数，因此只做下限 1 归一化，不增加 Node 未证明的上限。
- 单事务内按 `expires_at ASC`、SQLite `rowid ASC` 或 PostgreSQL `ctid ASC` 选择并删除至多一个批次。
- 删除条件是严格 `expires_at < expiredBefore`；等于 cutoff 的行不删。
- 返回提交事务实际 `RowsAffected`；重复执行只删除剩余候选，天然可重放。

所有写操作要求 `OwnerGate{Confirmed, SchemaReady, NodeWriterStopped}` 三项完整。该原语不调用 `businessauth`，不调用 Node、IPC、队列或外部服务，也不创建 schema。Gateway main 在同一 Business owner 连接上完成 `CheckContract` 后，以 supervisor component 周期调用该原语；组件失败会使 Gateway retention readiness 变为 false 并进入受控重试。`CheckContract` 仅验证既有关系是否可读。

`CoveredManifestOperations` 仅为覆盖证据；能力 manifest 仍须同时满足 Node active-path-zero、隔离存储、回滚和发布证据，不能因该组件已装配而宣称切换完成。
