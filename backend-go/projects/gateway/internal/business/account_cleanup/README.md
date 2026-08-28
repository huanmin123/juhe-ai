# `account-cleanup` Business owner

本目录只承接 `cleanup_expired_deleted_accounts`，对应
`BusinessSQLite-owner-manifest.json` 的 `account-cleanup` transaction group。
当前没有接入 Gateway main/HTTP，也没有修改 Node、manifest 或 shared schema。

## 已冻结的 Node 行为

迁移前的 Node DB-service 顺序为：

1. 按 `updated_at,id` 扫描孤立 authorization instance；逐条在事务中撤销授权、撤销 source/grant 影响、清理 Business binding，并用
   `id + system_account_id + updated_at + deleted_at IS NULL` CAS 软删除账户。
2. 按 `deleted_at,updated_at,id` 分页，先 root、后 instance，构造 root/related account、authorization、team scope 和 grant 集合。
3. dataset、usage、stats 记录仍存在或尚无完成证据时 deferred；只有外部 owner 提供带 token 的 cleared fence，才在同一 Business 事务内删除 Business 行。

## 所有权边界

Node 的 `account_record_cleanup_targets` 在 `juhe_dataset`，由 record-cleanup owner 写入和维护；它不是 Business SQLite 表。当前包不创建、不更新该表，也不检查 dataset/usage/stats 表。

候选目标通过 `CleanupResult.RecordCleanupTargets` 返回，供外部 record owner 持久化；`RecordFenceReader` 只能提供只读完成证据。reader 缺失、状态为 `unknown/pending` 或返回错误时，目标保留为 deferred/failed；`cleared` 必须有非空 token，否则 fail-closed。该部分是迁移尚未完成的 pending durable target/fence，不代表跨库记录已清理。

## API 和安全门

- `New` 不执行 DDL；`CheckContract` 只验证既有 Business relations。
- `OwnerGate` 的 `Confirmed`、`SchemaReady`、`NodeWriterStopped` 必须同时为 true，才允许写入。
- 每个孤立账户软删除和每个过期候选的物理删除均在独立事务中执行；物理删除重读 tombstone，并校验 `system_account_id/deleted_at/updated_at` CAS。
- 只使用 `database/sql`；不调用 Node、HTTP、IPC、队列或真实外部数据库。
- `Limit` 默认 20，最大 200；物理保留期默认一个月，时间格式与 Node 的 UTC 毫秒 ISO 表示一致。

## 验证

本目录包含隔离 SQLite 集成测试，以及不连接数据库的 PostgreSQL schema qualification/placeholder 测试。执行：

```text
gofmt -w internal/business/account_cleanup/*.go
go test -race ./internal/business/account_cleanup
go vet ./internal/business/account_cleanup
git diff --check -- backend-go/projects/gateway/internal/business/account_cleanup
```
