# `group-dirty-cursor`

该目录只实现 `GoBusinessCapabilityManifest` 中 `group-dirty-cursor` 的三个
business writer operation：

- `mark_all_group_account_stats_dirty`
- `delete_group_account_stats_dirty_rows`
- `update_group_account_stats_all_cursor`

实现使用 `database/sql` 短事务，不创建 schema，不调用 Node、IPC、HTTP 或队列。
写入必须同时满足 `OwnerGate` 的 `Confirmed`、`SchemaReady` 和
`NodeWriterStopped`；否则返回 `ErrOwnerGate`。

语义冻结自 Node DB-service：全量 dirty marker 固定使用 `__all__`，普通删除使用
`(group_id, updated_at)` 条件，重复删除是确定性 no-op；全量游标以
`all_cursor:<group_id>` 保存并按 `group_id` 字典序前进。相同游标是幂等 no-op，
倒退和 malformed cursor 状态 fail-closed。

未覆盖：listener/HTTP 接线、Node DB-service drain/cutover、统计缓存刷新本体、
capability manifest 状态变更，以及生产数据库验证。Postgres 测试只验证 schema
qualification、placeholder 和 `FOR UPDATE` SQL 契约；真实连接验收不在本目录范围内。
