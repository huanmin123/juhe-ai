# Gateway 账户管理 Aggregate

本包是 Gateway 内的直接 `database/sql` 事务模块，不经 Node、IPC、队列或跨进程调用。所有 CRUD 调用在 `OwnerGate{Confirmed, SchemaReady, NodeWriterStopped}` 三项均为真时才可执行；任一缺失返回 `ErrOwnerGate`。

已覆盖的最小原子边界：

| 命令 | 同一 SQL transaction 内的 Business 关系 | 并发/幂等规则 |
| --- | --- | --- |
| `Create` | `accounts`、supported models、model mappings、tags/bindings、API-key pool runtime binding 初始行 | 调用方提供 `accounts.id` 作为 receipt key；完全相同重试返回原 aggregate，差异内容冲突 |
| `Patch` | 主记录字段及显式替换的 supported models、mappings、tags 或 runtime binding 行 | `config_revision` CAS；任何子关系替换同样递增 revision |
| `Delete` | 逻辑删除并禁用调度；保留 child relation 以符合 Node 的 tombstone/replay 语义 | `config_revision` CAS；没有先 patch 后 delete 的跨事务窗口 |
| `Get` / `List` | 只读 aggregate 快照 | DTO 不返回 `credentials_encrypted` |

`APIKeyBindings` 指 `account_api_key_runtime_states` 的账户内 key-pool identity snapshot，不是 `api_keys -> route_strategies -> groups` 的控制面绑定。后一条关系以及 `group_accounts`、可用性投影、探针 cooldown/claim、授权、健康/余额、物理清理仍属于其他 transaction group，**本包没有也不能宣称接管它们**。

因此 `GoBusinessCapabilityManifest.json` 中 `business-account-runtime`、`business-account-cleanup`、`business-availability-authorization` 的状态保持 `missing`；该模块只有在这些完整组收口、Node active-path-zero 和 SQLite/PG 切换演练完成后，才能成为 Business owner handoff 的一部分。
