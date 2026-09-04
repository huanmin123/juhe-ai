# 存储层业务域归属清单（2026-09-04 子代理盘点）

来源：`backend/src/storage/` 顶层 226 + `runtime/` 4 + `schema/` 7 = 237 个非测试 .ts 文件，27 个业务域。用途：WP 卡片的 store 范围裁剪；*writer pool / IPC 胶水*类文件在 Go 化时消除而非移植。

## accounts (62)
account-advanced-detail.repository.ts, account-api-key-pool-probe-cursor.repository.ts, account-api-key-rotation.ts, account-api-key-runtime-state.repository.ts, account-api-key-runtime.repository.ts, account-authorized-dispatch.repository.ts, account-availability-schedule-status-sync.repository.ts, account-availability-schedule.ts, account-balance-jobs-outcome.repository.ts, account-balance-projection-cursor.repository.ts, account-balance.repository.ts, account-batch-edit-context.repository.ts, account-batch-update.repository.ts, account-circuit-control-plane.repository.ts, account-config-revision.ts, account-credentials-normalization.ts, account-delete-cleanup.repository.ts, account-derived-status-sql.ts, account-edit-basic.repository.ts, account-group-binding-write.repository.ts, account-health-jobs-input-authorization-fanout.repository.ts, account-health-jobs-input-outbox.repository.ts, account-health-jobs-input-version.repository.ts, account-health-jobs-input.repository.ts, account-health-jobs-outcome.repository.ts, account-health-monitor.repository.ts, account-health-projection-cursor.repository.ts, account-health-projection.repository.ts, account-health-projector-fixture.ts, account-identity.ts, account-interaction-context.repository.ts, account-list-availability-projection.repository.ts, account-list-options.ts, account-lock.repository.ts, account-management-list-usage.repository.ts, account-management-list.repository.ts, account-management-patch.repository.ts, account-manual-test-context.repository.ts, account-model-mapping-protocol-matrix.ts, account-model-mappings.repository.ts, account-model-normalization.ts, account-model-validation.repository.ts, account-name-search.repository.ts, account-options.repository.ts, account-quality.repository.ts, account-read.repository.ts, account-record-cleanup.ts, account-runtime-mutation-helpers.ts, account-runtime-mutation.repository.ts, account-runtime-status.ts, account-status-snapshot.repository.ts, account-status.ts, account-summary.repository.ts, account-supported-models.repository.ts, account-sweep-limits.ts, account-tags.repository.ts, account-test-tasks.repository.ts, account-usage-snapshot.repository.ts, account-usage.repository.ts, account-write-input.ts, oauth-credential-rotation.repository.ts, oauth-usage-loaders.ts

备注：最大域；M08（CRUD/详情/运行态/锁定）、M09（批量/导入导出/克隆）、M10（选项/标签/列表投影/搜索）按此裁剪。account-health/balance jobs 的 outbox/outcome 属 J1/J2 Go 桥接胶水（退役而非移植）。

## api-keys (8)
api-key-access.ts, api-key-availability-schedule.ts, api-key-list-mappers.ts, api-key-list-query.ts, api-key-mappers.ts, api-key-record-cleanup.ts, api-key-schedule-status-sync.repository.ts, api-key.repository.ts

## groups (8)
default-group.repository.ts, group-account-stats-cache.repository.ts, group-account-stats-write-invalidation.ts, group-account-stats.mapper.ts, group-read-loaders.ts, group-read.repository.ts, group-summary.repository.ts, group-write.repository.ts

## route-strategies (3)
route-strategy.repository.ts, route-strategy-availability-guard.ts, route-strategy-group-binding-limits.ts

## authorizations / system-teams / system-accounts (17)
access-scope.ts, authorization-options.repository.ts, authorization-read-loaders.ts, authorization-sweep-limits.ts, authorization-usage.repository.ts, system-team-limits.ts, system-team.repository.ts, system-account-mappers.ts, system-accounts.repository.ts, resource-permissions.ts, resource-authorization-helpers.ts, resource-authorization-list-helpers.ts, resource-authorization-read.repository.ts, resource-authorization-return.repository.ts, resource-authorization-usage.repository.ts, resource-authorization-write-state.repository.ts, resource-authorization-write.repository.ts

## announcements (2)
announcements.repository.ts, announcement-management-write.repository.ts

## providers (7)
provider.repository.ts, provider-model-catalog.repository.ts, provider-model-catalog-id.ts, provider-model-default-reference-cleanup.repository.ts, provider-default-health-check-model.repository.ts, provider-system-default-health-check-model.repository.ts, custom-provider-models.repository.ts

## proxies (1)
proxy.repository.ts

## openai-compatible 资源 (2)
openai-compatible-files.repository.ts, openai-compatible-vector-stores.repository.ts

## usage-records (14)
usage-records.repository.ts, usage-record-mappers.ts, usage-record-list-query.ts, usage-record-shards.ts, usage-record-access-metadata.ts, usage-record-catalog-cleanup.ts, usage-derived-window-rollover.ts, usage-overview-windows.repository.ts, usage-range-windows.repository.ts, usage-range-window-requests.repository.ts, usage-record-writer-pool.ts, usage-record-writer-pool.types.ts, usage-record-writer-worker.ts, postgres-usage-record-partitions.ts

备注：writer pool 为 fork 子进程池（按 shard 键写 usage-catalog），Go 化时消除；shard 语义本身移植（J-F）。

## usage-stats (21)
usage-stats.repository.ts, usage-stats-aggregation.ts, usage-stats-mappers.ts, usage-stats-helpers.ts, usage-stats-types.ts, usage-stats-time-buckets.ts, usage-stats-window-aggregates.ts, usage-stats-window-helpers.ts, usage-stats-metric-aggregates.ts, usage-stats-runtime-helpers.ts, usage-stats-snapshot-helpers.ts, usage-stats-writer-params.ts, usage-stats-writers.ts, usage-stats-model-writer.ts, usage-stats-latency-writer.ts, usage-stats-error-writer.ts, usage-stats-authorization-daily-writer.ts, usage-stats-account-quality-writer.ts, usage-stats-ai-performance.repository.ts, usage-summary-loaders.ts, usage-window-loaders.ts

## stats / system-metrics + 任务记录 (3)
system-metrics.repository.ts, background-task-runs.repository.ts, scheduled-job-lease.repository.ts
（后两个 PG 专用。）

## client-ip (8)
client-ip-stats.repository.ts, client-ip-stats-writer.ts, client-ip-stats-aggregation.repository.ts, client-ip-stats-detail.repository.ts, client-ip-stats-list.repository.ts, client-ip-usage-range-windows.repository.ts, client-ip-policy.repository.ts, client-ip-normalization.ts

## operation-logs (1)
operation-log-types.ts（类型定义；写路径经 F4 Go owner）

## audit-logs (6)
audit-log-f3-query.repository.ts, audit-log-f3-mappers.ts, audit-log-f3-query-helpers.ts, audit-log-f3-types.ts, audit-log-types.ts, audit-log-stable-json.ts（F3 已 Go owner，读侧对照用）

## runtime-logs (2)
runtime-logs.repository.ts, runtime-log-query.repository.ts（写者角色已预留 go-runtime-log）

## public-api-logs (1)
public-api-logs.repository.ts

## chat (8)
chat.repository.ts, chat-context.repository.ts, chat-api-key.repository.ts, chat-assets.repository.ts, chat-asset-storage.ts, chat-image-generations.repository.ts, chat-client.ts, postgres-chat-message-partitions.ts

## codex-context (5)
codex-context-state.repository.ts, codex-context-state-writer-pool.ts, codex-context-state-writer-pool.types.ts, codex-context-state-writer-worker.ts, codex-context-state-writer-diagnostics.ts（writer pool 为 db-service 角色的 fork 子进程池，Go 化消除）

## gateway 运行态 (10)
gateway-api-key.repository.ts, gateway-quota-snapshot.repository.ts, gateway-dispatch-candidate-window.repository.ts, request-quota-checker.ts, request-quota-limits.ts, request-quota-sql.ts, request-quota-hourly-windows.repository.ts, openai-account-selector.repository.ts, openai-account-selector.types.ts, availability-schedule-cache.ts

## external-integrations (8)
external-integration-source.repository.ts, external-integration-source-auth.repository.ts, external-integration-source-token.repository.ts, external-integration-source-mappers.ts, external-integration-source-normalizers.ts, external-integration-source-types.ts, external-integration-source-constants.ts, external-integration-source-write-helpers.ts

## table-monitor (1)
table-monitor.repository.ts（写者角色已预留 go-table-monitor）

## settings (2)
settings.repository.ts, response-inspection-policy.repository.ts

## data-retention (2)
data-retention.repository.ts, data-retention-hard-cleanup.ts

## schema (9)
schema/7 文件 + schema.ts(barrel) + schema-defaults.ts（另顶层 postgres-schema.ts、postgres-seed-defaults.ts、postgres-goose-schema-gate.ts、postgres-schema-owner-gate.ts 归 PG 门禁组）

## db-service / 连接层 / 胶水 (18)
database.ts, database-client.ts, postgres-client.ts, sqlite-config.ts, sqlite-maintenance.ts, sqlite-read-worker-pool.ts, sqlite-read-worker-pool.types.ts, sqlite-read-worker.ts, repositories.ts, shared-cache-read-batching.ts, runtime/storage-runtime.ts, runtime/index.ts, runtime/postgres-redis-runtime.ts, runtime/sqlite-memory-runtime.ts, postgres-schema.ts, postgres-seed-defaults.ts, postgres-goose-schema-gate.ts, postgres-schema-owner-gate.ts
备注：连接中枢与多进程胶水；SqliteWriterOwner 角色（db-service|ingest-worker|stats-writer|usage-shard-writer|go-runtime-log|go-table-monitor）；读池为 fork 子进程。全部属 X01/X02 退役面，语义迁移进 Go store。

## 其他 / 共享工具 (8)
crypto.ts, query-utils.ts, repository-lookups.ts, repository-row-types.ts, repository-input-normalization.ts, value-utils.ts, model-cache-sync-warning.ts, user-reference-data.repository.ts

## 迁移接缝提示
- `go-runtime-log` / `go-table-monitor` 写者角色是 F1/F2 已接管的现成接缝。
- oidc 仅在 business-schema 建表（8 张 oauth_* 表），模块代码在 modules/oidc-provider。
- model-pricing 无独立 storage 文件，逻辑散在 usage-records/usage-stats（C03/J-F 需覆盖）。
