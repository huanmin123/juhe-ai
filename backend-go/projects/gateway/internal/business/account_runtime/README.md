# account-runtime

本目录承接 `account-runtime` transaction group 的 16 个 Node
`db-service` operation，范围仅限 Business SQL 表：账户状态、账户 API Key
runtime state、探针游标、Gateway API Key 路由读取以及 API Key schedule
状态事件。所有状态写入要求 `OwnerGate{Confirmed, SchemaReady,
NodeWriterStopped}` 完整，使用 SQL 条件作为 revision/状态/探针 claim 的
CAS fence；PostgreSQL 使用显式 schema qualification 与 `$n` placeholder
转换。

实现不创建表，不调用 Node/HTTP/IPC/queue，不保存或输出明文凭据。现阶段
尚未迁移的 owner 通过显式端口 fail-closed：

- `QuotaUsagePort`：quota costs 由 stats/usage writer 提供；未配置时
  `check_api_key_quota` 与 `read_api_key_quota_costs` 返回
  `ErrOutstandingStatsOwner`。
- `CredentialResolver`：probe candidate 需要由 account owner 解密并提供
  API Key pool；未配置时 due probe 返回
  `ErrOutstandingCredentialResolver`。
- `ScheduleEvaluator`：schedule JSON 的完整时区、窗口、exception 语义由
  schedule owner 提供；未配置时 schedule sync 返回
  `ErrOutstandingScheduleEvaluator`。

`ManifestOperations` 是审计映射，不修改 capability/owner manifest；最终
handoff 仍由 `integration_owner` 审核 OwnerGate、isolated SQLite 集成和
PostgreSQL SQL qualification 后完成。
