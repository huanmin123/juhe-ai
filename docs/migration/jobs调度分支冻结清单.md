# jobs 调度分支冻结清单（T6b）

## 1. 目的与证据边界

- 本文档冻结 Node 后台调度器按运行模式分叉注册 job 的六处分支语义，作为 Go 侧
  `backend-go/projects/jobs`（`internal/jobregistry/schedule.go` 调度参数表 +
  `cmd/juhe-ai-jobs/worker_assembly.go` 家族装配）对齐与验收的对照基线。
- Node 权威源：`migration-backup/node/final-archive/backend/src/modules/background/background-jobs.ts`
  指定行段——`:294`（PG 高性能 `usage-overview-windows-refresh`）、`:296-300`
  （PG 高性能跳过冷历史范围窗口刷新及日志）、`:310-314`（默认/SQLite 分支的五个
  派生窗口 job）、`:325-330`（`accountBalanceNodeOwnerEnabled()` 余额 owner 分支）、
  `:334`（`accountListAvailabilityProjectionEnabled` 开关）、`:366`
  （`runtimeStateDriver !== 'redis'` 的 `key-model-memory-recovery` 分支）。
- 行段引用的共享常量（`background-jobs.ts:85-116`、`:403`、`:464`、
  `gateway/runtime/key-model-memory-recovery.ts:30`）：
  `dailyIntervalMs=24h`、`minuteMs=60s`、`usageRankSnapshotRefreshIntervalMs=30min`、
  `usageOverviewWindowRefreshIntervalMs=5min`、`coldUsageRangeWindowRefreshIntervalMs=6h`、
  `usageScopeRangeWindowInitialDelayMs=31min`、`authorizationUsageRangeWindowInitialDelayMs=43min`、
  `keyModelRecoveryScanIntervalMs=1s`。
- 模式判定：`isPostgresHighPerformanceMode()` 即 `databaseDriver === 'postgres'`；
  `accountBalanceNodeOwnerEnabled()` 即非 Go owner 模式（`account-balance-handover.ts:21-23`）。
- 本文档只冻结参数与注册行为，不改变任何 job 的业务实现 owner。

## 2. 每 job 每模式参数差异表

约定：`initialDelay` / `timeout` / `backoff` / `lease` 的单位为秒（s）或分钟（min）；
`coalesce` = `overlapPolicy: 'coalesceOne'`；`lease` = `runWithPostgresScheduledLease`
的租约 TTL；"—"表示该调度位置不设置该参数。

### 2.1 两分支共享的 stats-worker job（`background-jobs.ts:286-289/302` 与 `:305-308/316`，参数逐字一致）

| job | interval | initialDelay | stableWindow | jitter | coalesce | lane | timeout | backoff | lease |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| system-metrics-sample | `systemMetricsSampleIntervalSeconds`(5..3600, 默认 30) | 4s | — | ✓ | ✓ | — | 20s | — | 15s |
| usage-stats-aggregation | `statsAggregationIntervalSeconds`(5..3600) 与 60s 上限取小 | 3s | 2s | ✓ | ✓ | stats-online | 20s | 1s→1min | 1min |
| client-ip-stats-aggregation | `statsAggregationIntervalSeconds`(5..3600, 默认 60) | 8s | 2s | ✓ | ✓ | stats-online | 20s | 1s→1min | 1min |
| group-account-stats-refresh | `groupAccountStatsRefreshIntervalSeconds`(5..3600, 默认 60) | 16s | — | ✓ | ✓ | stats-online | 30s | 1s→1min | 2min |
| usage-hot-window-refresh（`:290/:309` → `scheduleUsageHotWindowRefreshJob()`） | `usageHotWindowRefreshIntervalSeconds`(60..3600, 默认 600) | 25s | 10s | ✓ | ✓ | stats-heavy | 30s+5s | 10s→5min | — |
| account-quality-refresh（`:301/:315` → `scheduleAccountQualityRefreshJob()`，`:403-421`） | `accountQualityRefreshIntervalSeconds`(60..3600, 默认 600) | 75s | 30s | ✓ | ✓ | stats-online | 1min | 5s→5min | 5min |
| usage-stats-consistency-check | 60min 固定 | 11min | — | ✓ | — | — | — | — | 5min |

### 2.2 派生窗口 job：PG 高性能分支（`:286-295`）vs 默认/SQLite 分支（`:305-314`）

| job | 参数 | PG 高性能分支 | 默认/SQLite 分支 | 差异 |
| --- | --- | --- | --- | --- |
| usage-rank-snapshots-refresh | 全部参数 | interval 30min，initialDelay 2min30s，stable 30s，jitter ✓，coalesce ✓，lane stats-heavy，timeout 10min，backoff 30s→10min，lease 15min | 同左 | 仅 stage 集合不同（见下） |
| 同上 | stages | `postgresUsageRankSnapshotCoreStageNames`（`:109-111`，= 核心 6 stage 剔除 `ai_performance_summary_windows`） | `usageRankSnapshotCoreStageNames`（`:101-108`，含 `ai_performance_summary_windows` 共 6 stage） | PG 由独立 job（:292）承担 ai_performance_summary_windows |
| ai-performance-summary-windows-refresh | interval/initialDelay/timeout/lease | 5min / 3min / 1min / 5min，backoff 15s→5min，stable 30s | **不注册**（stage 已并入 usage-rank-snapshots-refresh） | 仅 PG 注册 |
| system-metrics-trend-windows-refresh | interval/initialDelay/stable/timeout/backoff/lease/stages | 30min / 3min20s / 30s / 10min / 30s→10min / 15min / `['system_metrics_trend_windows']` | 同左（`:311`） | 无 |
| usage-overview-windows-refresh | interval | **`usageOverviewWindowRefreshIntervalMs` = 5min**（`:294`） | **`usageRankSnapshotRefreshIntervalMs` = 30min**（`:312`） | **interval 按模式 5min vs 30min** |
| 同上 | initialDelay/stable/coalesce/lane/timeout/backoff/lease/stages | 4min10s / 30s / ✓ / stats-heavy / 10min / 30s→10min / 15min / `['usage_overview_windows']` | 同左 | 无 |
| usage-scope-range-windows-refresh | 注册 | **不注册**（`:296-300` 日志 `background_cold_range_window_refresh_disabled`，PG 在线跳过冷历史范围窗口重刷） | **注册**（`:313`）：interval 6h，initialDelay 31min，stable 30s，coalesce ✓，lane stats-heavy，timeout 10min，backoff 1min→30min，lease 15min，stages `['usage_scope_range_windows']` | **仅默认/SQLite 分支注册** |
| authorization-usage-range-windows-refresh | 全部参数 | interval 6h，initialDelay 43min，stable 30s，coalesce ✓，lane stats-heavy，timeout 10min，backoff 1min→30min，lease 15min，stages `['authorization_usage_range_windows']`（`:295` / `:314`） | 同左 | 无（两分支都注册） |

### 2.3 ops-worker 分支 job（`:320-334`、`:365-366`）

| job / 分支 | 参数 | 分支条件 | 差异说明 |
| --- | --- | --- | --- |
| chat-retention-cleanup | interval 10min，initialDelay 270s，stable 30s，jitter ✓，**scheduleMode fixedDelay**，lane storage-maintenance，timeout 2min，backoff 30s→10min，lease 5min | 无 | 无 |
| api-key-availability-schedule-status-sync | interval 10s，initialDelay 1s，jitter ✓，lease 30s | 无 | 无 |
| account-availability-schedule-status-sync | interval 10s，initialDelay 2s，jitter ✓，lease 30s | 无 | 无 |
| resource-authorization-expiry-sweep | interval 1min，initialDelay 54s，jitter ✓，lease 2min | 无 | 无 |
| expired-deleted-account-cleanup | interval 24h，initialDelay 14min，jitter ✓，lease 10min | 无 | 无 |
| account-balance-refresh | interval 1min，initialDelay 20s，stable 5s，coalesce ✓，lane external-account-maintenance，timeout 60s，backoff 10s→5min | `accountBalanceNodeOwnerEnabled()`（`:325`） | false 时**不注册**，改打日志 `account_balance_node_owner_drained {owner: 'go'}`（`:328-330`） |
| account-balance-auto-detect-recovery | interval 1min，initialDelay 25s，stable 5s，coalesce ✓，lane external-account-maintenance，timeout 45s，backoff 10s→5min | 同上 | 同上 |
| account-api-key-cooldown-retest | interval `cooldownAccountRetestIntervalSeconds`(1..3600, 默认 3)，initialDelay `accountApiKeyCooldownRetestStartupDelayMs`，jitter ✓，任务注入 `settingsNumber` | 无 | 无 |
| normal-route-speed-first-recovery-probe | interval 5s，initialDelay `normalRouteSpeedFirstProbeStartupDelayMs`，jitter ✓ | 无 | 无 |
| account-circuit-control-plane-maintenance | interval 5s，initialDelay 1s，jitter ✓ | 无 | 无 |
| account-list-availability-projection-maintenance | interval `accountListAvailabilityProjectionIntervalMs`（env 默认 1s，1s..60s），initialDelay 1s，stable 1s，coalesce ✓，lane account-list-projection，timeout 60s，backoff 1s→1min，lease 2min；批参数 batchSize=100（1..100）、maxBatchesPerRun=200（1..400）、workerConcurrency=4（1..8） | `runtimeConfig.background.accountListAvailabilityProjectionEnabled`（env `JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED`，**默认 false**，`:334`） | 默认**不注册** |
| account-circuit-recovery | interval 5s，initialDelay 5s，jitter ✓（`:365`） | 无 | 无 |
| key-model-memory-recovery | interval 1s（`keyModelRecoveryScanIntervalMs`），initialDelay 1s，**passiveJitter: false**，coalesce ✓，lane external-account-maintenance，timeout 45s，backoff 1s→5s | `runtimeConfig.runtimeStateDriver !== 'redis'`（`:366`） | Redis runtime-state driver 下**不注册**（内存恢复仅在非 Redis 模式有意义） |

## 3. Go 侧现状对照

权威表：`backend-go/projects/jobs/internal/jobregistry/schedule.go`（`schedules()`）；
注册门禁：`worker_assembly.go scheduleWiredJob` 只注册 `GoStatus = GoWired` 的 job；
设置间隔映射：`jobregistry.SettingsIntervalJobNames()`。
T6b 后模式分叉权威表：`jobregistry.ModeConstraints()` +
`ResolveScheduleForDriver(jobName, settings, databaseDriver)`
（`usage-overview-windows-refresh` SQLite=30min / PG=5min、
`usage-scope-range-windows-refresh` 仅默认/SQLite 分支（PG 分支打
`background_cold_range_window_refresh_disabled`）、
`ai-performance-summary-windows-refresh` 仅 PG 分支）；stage 集合分叉由组合根
`rankSnapshotCoreStages(postgres)` 消费。

| job | Node 两分支参数 | Go `schedules()` | 判定 |
| --- | --- | --- | --- |
| system-metrics-sample / usage-stats-aggregation / client-ip-stats-aggregation / group-account-stats-refresh / usage-hot-window-refresh / usage-stats-consistency-check / account-quality-refresh | 见 2.1 | 与 Node 一致；设置驱动间隔经 `SettingsIntervalJobNames` | 一致 |
| usage-rank-snapshots-refresh | stage 集合按模式分叉（PG 剔除 ai_performance_summary_windows） | 单一注册；stage 集合经 `rankSnapshotCoreStages(postgres)` 按模式消费（T6b） | 一致（T6b 落地） |
| ai-performance-summary-windows-refresh | 仅 PG 分支注册 | `ModeConstraint.PostgresOnly`：仅 PG 分支注册；SQLite 分支 stage 并入 usage-rank-snapshots-refresh（T6b） | 一致（T6b 落地） |
| system-metrics-trend-windows-refresh / authorization-usage-range-windows-refresh | 两分支一致 | 与 Node 一致 | 一致 |
| usage-overview-windows-refresh | interval 按模式 5min（PG）/ 30min（SQLite） | `ModeConstraint.SQLiteInterval`：PG 5min / SQLite 30min（T6b） | 一致（T6b 落地） |
| usage-scope-range-windows-refresh | 仅默认/SQLite 分支注册；PG 分支跳过并打 `background_cold_range_window_refresh_disabled` | `ModeConstraint.SQLiteOnly`：PG 分支不注册并打同款事件（T6b） | 一致（T6b 落地） |
| account-balance-refresh / account-balance-auto-detect-recovery | `accountBalanceNodeOwnerEnabled()` 分支注册 | refresh = GoEquivalent（Go owner 承担）、auto-detect-recovery = GoWired | 等价映射：Go owner 模式即 Node `:328-330` 的 drained 分支终态；`scheduleWiredJob` 门禁保证非 GoWired 不注册 |
| account-list-availability-projection-maintenance | env 开关默认 false，interval env 可变（1s..60s） | GoPartial，interval 固定 1s | **差异（开关与可变间隔未建模）**：Go 未接 `..._ENABLED` env 与 `..._INTERVAL_MS`；当前靠 GoStatus=GoPartial 不进入调度，冻结依据登记在 GoBinding |
| key-model-memory-recovery | 仅非 Redis runtime-state driver 注册；jitter 明确 false | GoEquivalent，参数一致（PassiveJitter 未设 = false ✓），无 driver 分支字段 | 等价映射：Go 侧该能力归属 `internal/keymodelrecovery`，driver 分支由接线层承担，冻结依据登记在 GoBinding |

## 4. 冻结处置与遗留项

1. **冻结点（不得回退）**：
   - `usage-overview-windows-refresh` 的 PG 分支 interval 必须是 5min（`:294`），
     `usageRankSnapshotRefreshIntervalMs`（30min）只属于默认分支与
     usage-rank/system-metrics-trend 两个 job。**T6b 已落**
     `ModeConstraint.SQLiteInterval`。
   - `usage-scope-range-windows-refresh` 只在默认/SQLite 模式注册；PG 高性能模式的
     预期行为是跳过 + `background_cold_range_window_refresh_disabled` 日志。
     **T6b 已落** `ModeConstraint.SQLiteOnly` + 组合根同款事件日志。
   - `accountListAvailabilityProjectionEnabled` 默认 false。
   - `key-model-memory-recovery` 仅非 Redis runtime-state driver；`passiveJitter=false`。
   - `usage-rank-snapshots-refresh` 的 PG stage 集合必须剔除
     `ai_performance_summary_windows`（由独立 job 承担）。**T6b 已落**
     `rankSnapshotCoreStages(postgres)` + `ModeConstraint.PostgresOnly`。
2. **Go 侧遗留（T6b 处置结果）**：
   - ~~`schedules()` 缺少按 `databaseDriver` 的模式分叉~~ **已落地**：
     `jobregistry.ModeConstraints()` + `ResolveScheduleForDriver` +
     组合根 `scheduleWiredJob`/窗口任务分支消费（usage-overview SQLite=30min、
     usage-scope-range PG 跳过+事件、ai-performance SQLite 不注册+stage 并入）。
   - `account-list-availability-projection-maintenance` 的 env 开关与可变 interval
     未接（GoPartial 不进调度，冻结依据登记在 GoBinding，接线时按
     Node `:334-336` 消费）。
   - `key-model-memory-recovery` 的 runtime-state driver 分支未在注册表建模
     （GoEquivalent 自有循环，冻结依据登记在 GoBinding）。
3. 本清单与 `internal/jobregistry/registry.go` 的 `GoStatus` 共同构成调度面验收输入；
   修改 `schedules()` 或 `worker_assembly.go` 家族装配时必须对照第 2、3 节复核。
