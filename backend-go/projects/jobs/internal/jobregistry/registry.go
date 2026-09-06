// Package jobregistry 对齐 Node modules/background/background-job-registry.ts
// 与 background-job-registry.entries.ts：jobs 二进制的任务注册表。每个 Node
// scheduled job 一条注册条目，并显式登记 Go 侧绑定状态——已装配、等价组件、
// 包已迁移但组合根适配器未接、由其他 Go 进程接管或按总设计消灭；没有实现
// 的一律登记为 node-only，不允许静默跳过。
//
// 调度参数（间隔/initial delay/lease TTL/超时/退避/lane/overlap）取自 Node
// background-jobs.ts 的 scheduler.schedule(...) 实参，见 schedule.go；组合根
// （cmd/juhe-ai-jobs）以 ScheduleFor(name) 构建调度器 Spec。
package jobregistry

import "time"

// GoStatus 登记每个 Node job 在 Go 侧的绑定状态。
type GoStatus string

const (
	// GoWired：Go 实现已存在且 jobs 组合根可装配（本组合根默认可启动）。
	GoWired GoStatus = "go-wired"
	// GoEquivalent：由 Go 等价组件接管（自有调度循环，不进 worker 调度器）。
	GoEquivalent GoStatus = "go-equivalent"
	// GoPartial：Go 任务包已迁移，但组合根所需 store/adapter 尚未接线。
	GoPartial GoStatus = "go-partial"
	// GoOwnedElsewhere：由 gateway 等其他 Go 进程接管，jobs 禁止启用。
	GoOwnedElsewhere GoStatus = "go-owned-elsewhere"
	// GoEliminatedByDesign：该 Node IPC/进程池机制按 Go 总设计消灭，
	// 由进程内单路径替代。
	GoEliminatedByDesign GoStatus = "go-eliminated-by-design"
	// NodeOnly：Go 尚无实现（显式登记缺失，不静默跳过）。
	NodeOnly GoStatus = "node-only"
)

// Category 与 Node BackgroundJobCategory 一致。
type Category string

const (
	CategoryScheduled       Category = "scheduled"
	CategoryIPCQueue        Category = "ipc-queue"
	CategoryControlIPC      Category = "control-ipc"
	CategoryLocalQueue      Category = "local-queue"
	CategoryMaintenanceTask Category = "maintenance-task"
)

// Entry 对齐 Node BackgroundJobRegistryEntry，并附加 Go 绑定信息。
type Entry struct {
	JobName                    string
	Category                   Category
	Kind                       string
	DefaultRole                string
	Hotspot                    bool
	SingleOwner                bool
	Shardable                  bool
	LeaseRequired              bool
	BlocksUserVisibleFreshness bool
	Writes                     []string
	GoStatus                   GoStatus
	// GoPackage 是 Go 实现所在包（backend-go-jobs/internal/... 或其他进程）。
	GoPackage string
	// GoBinding 说明绑定方式或缺口（组合根适配器、启用前置条件等）。
	GoBinding string
}

// ScheduledEntries 返回全部 Node scheduled job 的注册条目（保持 Node 顺序）。
func ScheduledEntries() []Entry {
	return []Entry{
		{
			JobName: "system-metrics-sample", Category: CategoryScheduled, Kind: "sample", DefaultRole: "stats-worker",
			Hotspot: true, SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes: []string{"stats:system_metrics_samples", "stats:process_event_loop_samples"},
			// Node 采样写 stats 库 system_metrics_samples；Go 采样器写
			// go-runtime-metrics（gometricsstore），是 F 系列确定的等价接管。
			GoStatus: GoEquivalent, GoPackage: "gometricsstore",
			GoBinding: "由 jobs 组件 Go runtime metrics sampler 等价接管（自有 ticker）",
		},
		{
			JobName: "system-metrics-trend-windows-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:system_metrics_trend_windows"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages([system_metrics_trend_windows])",
		},
		{
			JobName: "usage-stats-aggregation", Category: CategoryScheduled, Kind: "stats", DefaultRole: "stats-worker",
			Hotspot: true, SingleOwner: false, Shardable: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:usage_stats_*", "stats:usage_model_*", "stats:usage_error_*", "stats:usage_latency_*"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "Aggregator.AggregateUsageStatsBatch（单游标权威路径）",
		},
		{
			JobName: "usage-hot-window-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			Hotspot: true, SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:usage_overview_summary_windows", "stats:usage_overview_trend_windows", "stats:usage_scope_range_windows"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages(热窗口阶段 overview+scope)",
		},
		{
			JobName: "client-ip-stats-aggregation", Category: CategoryScheduled, Kind: "stats", DefaultRole: "stats-worker",
			Hotspot: true, SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:client_ip_stats_daily", "stats:client_ip_account_stats_daily", "stats:client_ip_usage_range_windows", "stats:client_ip_account_usage_range_windows"},
			GoStatus: GoWired, GoPackage: "statsverify",
			GoBinding: "Store.RunClientIPStatsAggregation（fencing lease 由组合根经 taskruns 提供）",
		},
		{
			JobName: "group-account-stats-refresh", Category: CategoryScheduled, Kind: "stats", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:group_account_stats_dirty", "stats:group_account_stats"},
			GoStatus: GoWired, GoPackage: "statsverify",
			GoBinding: "Store.RunGroupAccountStatsRefresh",
		},
		{
			JobName: "usage-rank-snapshots-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:usage_rank_snapshots"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages(rank 阶段集合按 databaseDriver 分叉（T6b，冻结清单 §4.1）：PG 分支剔除 ai_performance_summary_windows（由独立 job 承担）；默认/SQLite 分支并入该 stage），组合根 rankSnapshotCoreStages(postgres) 消费",
		},
		{
			JobName: "ai-performance-summary-windows-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:ai_performance_summary_windows", "stats:ai_performance_summary_dirty_system_accounts"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages([ai_performance_summary_windows])；仅 PG 分支注册（T6b 已落 ModeConstraint.PostgresOnly），默认/SQLite 分支该 stage 并入 usage-rank-snapshots-refresh（Node background-jobs.ts:310），执行语义按 stage 幂等",
		},
		{
			JobName: "usage-overview-windows-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:usage_overview_summary_windows", "stats:usage_overview_trend_windows"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages([usage_overview_windows])；interval 按 databaseDriver 分叉（T6b 已落 ModeConstraint.SQLiteInterval）：PG=5min（Node :294 usageOverviewWindowRefreshIntervalMs）、SQLite=30min（Node :312 usageRankSnapshotRefreshIntervalMs）",
		},
		{
			JobName: "usage-scope-range-windows-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			Hotspot: true, SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:usage_scope_range_windows"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages([usage_scope_range_windows])；仅默认/SQLite 分支注册（T6b 已落 ModeConstraint.SQLiteOnly），PG 高性能分支跳过并打 background_cold_range_window_refresh_disabled（Node :296-300，冻结清单 §4.1 不得回退）",
		},
		{
			JobName: "authorization-usage-range-windows-refresh", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:authorization_team_usage_range_windows", "stats:authorization_user_usage_range_windows"},
			GoStatus: GoWired, GoPackage: "statsagg",
			GoBinding: "WindowRefresher.RunStages([authorization_usage_range_windows])",
		},
		{
			JobName: "usage-stats-consistency-check", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{},
			GoStatus: GoWired, GoPackage: "statsverify",
			GoBinding: "Store.RunUsageStatsConsistencyCheck（只检测上报，不修复）",
		},
		{
			JobName: "background-task-run-reconcile", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"stats:background_task_runs", "stats:background_job_leases"},
			GoStatus: GoWired, GoPackage: "opsjobs + taskruns",
			GoBinding: "RunTaskRunReconcile 经 taskruns.Store 对账；进程启动另跑 taskruns.RecoverOnStartup",
		},
		{
			JobName: "api-key-record-cleanup-retry", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ingest-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"dataset:api_key_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "RecordCleanupRetryJob.RunAPIKey + cleanuprepo.RecordCleanupStore（SQLite 全链：targets/分片游标/统计扣减结算；PostgreSQL 全链：CleanupAPIKeyRelatedPostgres/CleanupPendingAPIKeyTargetsPostgres，subtractPostgresUsageStatsRows 扣减家族 + usage_record_cleanup_deductions 台账 + scope stats 清理）",
		},
		{
			JobName: "account-record-cleanup-retry", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ingest-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"dataset:account_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "RecordCleanupRetryJob.RunAccount + cleanuprepo.RecordCleanupStore（SQLite 全链：targets/分片游标/统计扣减结算；PostgreSQL 全链：CleanupAccountRelatedPostgres/CleanupPendingAccountTargetsPostgres，subtractPostgresUsageStatsRows 扣减家族 + usage_record_cleanup_deductions 台账 + scope stats 清理）",
		},
		{
			JobName: "api-key-availability-schedule-status-sync", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:api_keys"},
			GoStatus: GoWired, GoPackage: "oauthrefresh",
			GoBinding: "Store.SyncApiKeyScheduleStatuses",
		},
		{
			JobName: "account-availability-schedule-status-sync", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:accounts"},
			GoStatus: GoWired, GoPackage: "oauthrefresh",
			GoBinding: "Store.SyncAccountScheduleStatuses",
		},
		{
			JobName: "resource-authorization-expiry-sweep", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:resource_authorizations", "business:account_health_jobs_input_outbox", "business:account_health_jobs_input_versions", "business:group_account_stats_dirty"},
			GoStatus: GoWired, GoPackage: "oauthrefresh",
			GoBinding: "Store.RunAuthorizationExpirySweep + 组合根 GrantFinalizer（authorizationGrantHealthFanout：事务内 J1 输入 fanout，kind=snapshot/reason=authorization_grant_changed）+ expired>0 后 markAllGroupAccountStatsDirty('authorization_expired')（对齐 Node refreshAfterResourceAuthorizationBusinessWriteAsync）。T6d 冻结交接：grant 翻转后的 runtime 投影（resource_authorizations 行 + effective source 重算）与 quota 窗口 scope bindings 属 gateway authz sync 域（gateway internal/authz，jobs 不可 import，且无既有 jobs→gateway 交接表消费端，不新建死表面）；Go gateway 读路径以 expires_at > now 门禁兜底（chain_accounts activeResourceAuthorization*），待 gateway 侧接入 authz 交接消费时收敛",
		},
		{
			JobName: "account-quality-refresh", Category: CategoryScheduled, Kind: "stats", DefaultRole: "stats-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"stats:account_quality_*"},
			GoStatus: GoWired, GoPackage: "accountquality + proberepo + accountprobe",
			GoBinding: "RefreshRunner + PrecheckRunner 经组合根接线：AccountReader（find_account_for_test/find_openai_account_for_group 派生）、Prober（accountprobe 协议诊断）、PrecheckMutation（dispatch_revision 围栏 CAS）由 proberepo/accountprobe 提供；effectiveAvailability 的 gateway 运行态分支与授权额度分支不可达 jobs，仅影响探针跳过判断（写入路径保留 CAS 围栏）",
		},
		{
			JobName: "account-balance-refresh", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			SingleOwner: false, Shardable: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:accounts", "stats:account_usage_snapshots"},
			GoStatus: GoEquivalent, GoPackage: "accountbalance",
			GoBinding: "J2 Service 已由 jobs 组件装配（自有调度循环与账户租约）",
		},
		{
			JobName: "account-balance-auto-detect-recovery", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			SingleOwner: false, Shardable: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:accounts", "stats:account_usage_snapshots"},
			GoStatus: GoWired, GoPackage: "opsjobs + accountbalance + taskruns",
			GoBinding: "RunBalanceAutoDetectionRecovery 经组合根适配器接线（探测意图仓储直查业务库 + background_job_leases 候选租约 + J2 ExecuteBalanceQuery builtin 探测）",
		},
		{
			JobName: "openai-oauth-access-token-refresh", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:accounts"},
			GoStatus: GoWired, GoPackage: "oauthrefresh",
			GoBinding: "RefreshJob.RunOnce + HTTPTokenExchanger（Node 兼容凭据封套）",
		},
		{
			JobName: "account-api-key-cooldown-retest", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			Hotspot: true, SingleOwner: false, Shardable: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:account_api_key_runtime_states", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "accountquality + proberepo + accountprobe",
			GoBinding: "CooldownRetestRunner 经组合根接线：CooldownCandidateSource（到期候选 claim CAS）与 CooldownMutation（record/defer + config_revision CAS）由 proberepo 提供，探针走 accountprobe limited 诊断",
		},
		{
			JobName: "normal-route-speed-first-recovery-probe", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			Hotspot: true, SingleOwner: false, Shardable: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"runtime-state:gateway-normal-route-latency-degradation", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "opsjobs + proberepo + accountprobe",
			GoBinding: "SpeedFirstProbeRunner 经组合根接线：SpeedFirstClaimStore 由 proberepo 的 Redis 降级运行态实现提供（与 Node 网关同键同 Lua 契约，state/probe-index/claim 键形状逐字段一致），探针走 accountprobe 完整分级诊断；缺 JUHE_AI_REDIS_STATE_URL 时组合根登记 disabled",
		},
		{
			JobName: "account-circuit-control-plane-maintenance", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:account_circuit_outbox", "runtime:account_circuit"},
			GoStatus: GoWired, GoPackage: "opsjobs + circuitstore",
			GoBinding: "ControlPlaneMaintenance 经组合根接线：Redis CircuitStore 由 circuitstore 提供（Lua 与键形状逐字节对照 gatewaycircuit/Node account-circuit-redis-store.ts，同键空间单实现；容量随 JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_CAPACITY），ControlPlaneLedger/Outbox 由 circuitstore 业务库双模适配器提供（ack 回写投影 revision 水位）；reconcile 游标持久化为加法扩展（Node 内存语义保留：游标缺失即从头幂等回放）；缺 JUHE_AI_REDIS_STATE_URL 时组合根登记 disabled",
		},
		{
			JobName: "account-list-availability-projection-maintenance", Category: CategoryScheduled, Kind: "snapshot", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true, BlocksUserVisibleFreshness: true,
			Writes:   []string{"business:account_list_availability_projections", "business:account_list_availability_projection_tags", "business:account_list_availability_dirty"},
			GoStatus: GoWired, GoPackage: "opsjobs + circuitstore",
			GoBinding: "RunListAvailabilityMaintenance 经组合根接线：ListAvailabilityRepo（17 方法 runtime dependency fail-closed 状态机/dirty claim 围栏/tombstone 删除/重放退避）、overlay Redis 对账（account-concurrency-v2 同键 Lua）与 LoadItems 物化载荷（circuitstore.ProjectionItemLoader：management list 同源 SQL 双模 + accountEffectiveAvailability 状态机/quota/usage/balance/apiKeyRuntime + Redis 运行态读 gateway-account-recovery/policy-avoidance 只读，payload 逐字段对照 Node AccountListItem）全部就绪。T6b 冻结依据已落代码（worker_config.go）：JUHE_AI_BACKGROUND_ACCOUNT_LIST_AVAILABILITY_PROJECTION_ENABLED env 默认 false 不注册（Node background-jobs.ts:334）、interval=accountListAvailabilityProjectionIntervalMs（env 1s..60s 默认 1s）、batchSize=100(1..100)/maxBatchesPerRun=200(1..400)/workerConcurrency=4(1..8)；非 PG driver 不注册（Node PG-only 物化器）。登记差异：payload 的 isAccountBalanceSnapshotSuppressed 依赖网关进程内清理协调器内存态，jobs 进程无该组件（等价 Node 空协调器恒 false）",
		},
		{
			JobName: "account-circuit-recovery", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			SingleOwner: true, Shardable: true, LeaseRequired: true,
			Writes:   []string{"runtime:account_circuit"},
			GoStatus: GoWired, GoPackage: "opsjobs + circuitstore + proberepo + accountprobe",
			GoBinding: "CircuitRecoveryService 经组合根接线：CircuitStore 同 control-plane（Redis 单实现同键空间）；恢复目标解析由 proberepo 账户域读取链（LoadAccountForTest/LoadAccountForGroup ignoreAvailability + dispatch revision 围栏）与 accountprobe limited 诊断提供；已知限制：gatewayAccountRuntimeKey 复核退化为 identity 一致性（CandidateAccount 未暴露授权绑定上下文，由 store 侧 CAS 围栏兜底），protocol_model scope 的 modelBucket 钉住模型不可表达（走健康检查模型）；缺 JUHE_AI_REDIS_STATE_URL 时组合根登记 disabled",
		},
		{
			JobName: "key-model-memory-recovery", Category: CategoryScheduled, Kind: "probe", DefaultRole: "ops-worker",
			Hotspot: true, SingleOwner: true, LeaseRequired: false,
			Writes:   []string{"runtime:key_model_memory"},
			GoStatus: GoEquivalent, GoPackage: "keymodelrecovery",
			GoBinding: "Runner 已由 jobs 组件装配（自有扫描循环，lease-free）。T6b 冻结依据：Node 仅在 runtimeStateDriver !== 'redis' 注册该 job（background-jobs.ts:366），Go runtime-state 键空间随 JUHE_AI_REDIS_STATE_URL 归一，driver 分支由接线层承担，注册表不建 driver 字段",
		},
		{
			JobName: "data-retention-cleanup", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ingest-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"dataset:*", "stats:*", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "DataRetentionJob + cleanuprepo（public_api_logs/usage records 分区裁剪与分片清理/stats+metrics 保留/system_sessions/codex-context 清理结算/非业务数据硬清理）双模接线；SQLite settings 读模型暂用 Node 默认策略值",
		},
		{
			JobName: "chat-retention-cleanup", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"chat:*"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "ChatRetentionJob + cleanuprepo.ChatStore（cleanupChatRetention 全链：PG 每日分区/轮次/幂等键/容量窗口/标题/压缩恢复/检查点/资产认领删除）双模接线；isActiveTurn 钩子跨进程不可见保持 nil",
		},
		{
			JobName: "expired-deleted-account-cleanup", Category: CategoryScheduled, Kind: "maintenance", DefaultRole: "ops-worker",
			SingleOwner: true, LeaseRequired: true,
			Writes:   []string{"business:accounts", "business:resource_authorizations"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "ExpiredDeletedAccountJob + cleanuprepo.DeletedAccountStore（候选/相关记录守卫/物理删除双模）+ 本地 record maintenance 队列投递；孤儿授权实例扫尾仅 PG（SQLite 依赖 resource-authorization 运行态同步域，跳过时显式 warn）",
		},
	}
}

// QueueEntries 登记非 scheduled 注册表条目（ipc/control/local queue 与
// maintenance-task）的 Go 归属。
func QueueEntries() []Entry {
	batch := func(name string, writes []string, status GoStatus, pkg, binding string) Entry {
		return Entry{JobName: name, Category: CategoryIPCQueue, Kind: "ingest", DefaultRole: "ingest-worker",
			BlocksUserVisibleFreshness: true, Writes: writes, GoStatus: status, GoPackage: pkg, GoBinding: binding}
	}
	control := func(name, role string, status GoStatus, binding string) Entry {
		return Entry{JobName: name, Category: CategoryControlIPC, Kind: "control", DefaultRole: role,
			Writes: []string{}, GoStatus: status, GoBinding: binding}
	}
	return []Entry{
		batch("background_worker_usage_records", []string{"usage-shards:usage_records"}, GoWired, "usagewriter",
			"Redis Stream/IPC 分派按总设计消灭；usagewriter 直接异步写分片（组合根 Start/Close）"),
		batch("background_worker_public_api_logs", []string{"dataset:public_api_logs"}, GoOwnedElsewhere, "gateway/internal/publicapilogs",
			"W7 已随 gateway 进程内写路径接管"),
		{JobName: "background_worker_record_maintenance", Category: CategoryIPCQueue, Kind: "maintenance", DefaultRole: "ingest-worker",
			Writes: []string{"dataset:*", "stats:*", "usage-shards:usage_records"}, GoStatus: GoWired, GoPackage: "cleanuprepo + retention + recordmaintenance",
			GoBinding: "recordmaintenance runner + cleanuprepo 执行器 + 组合根本地队列 flush 循环 + record_maintenance_jobs 交接表 drain（gateway cleanup POST 落行：ORDER BY created_at、成功删行、失败保留；Redis Stream/IPC 按 Go 总设计消灭）"},
		batch("background_worker_account_test_tasks", []string{"business:account_test_tasks"}, GoWired, "opsjobs + manualtestrepo + manualtest + accountprobe",
			"ManualTestQueue + manualtestrepo 双模仓储 + manualtest 执行器（draft v1 信封解密 → accountprobe.ManualDiagnostics 分级诊断 → result_json 信封写回 → 取消响应）全链路接线；internalapi loopback 派发回调接 DispatchAccountTestTask，族 disabled 时派发保持 503 不可用语义"),
		control("background_worker_account_test_cancel", "ops-worker", GoWired, "ManualTestQueue.CancelLocal 已随手动测试族执行器接线（internalapi loopback POST /v1/account-test/cancel 扩展路由，对齐 Node worker IPC background_worker_account_test_cancel 的取消语义；DB 侧 cancel_requested 标记与会话取消语义在 manualtestrepo 覆盖）"),
		control("background_worker_codex_source_fence_settled", "ops-worker", GoWired, "fence settlement 已由 circuitstore.ProbeStateStore 提供（gateway-availability-probe-coordinator 同键 Lua 子集：get/acquireGenerationRun/commitGenerationRun + source fence CAS 结算，等价 Node worker→gateway IPC 的 settleDispatchedAvailabilityProbeBySourceFence）；J1/J3 durable outcome 生产者在 jobs 侧落地后直接调用该结算入口"),
		control("background_worker_status_request", "worker-control", GoEliminatedByDesign, "Node worker IPC 控制面在 Go 单进程侧消灭"),
		control("background_worker_ready", "worker-control", GoEliminatedByDesign, "由 jobs /health readiness 取代"),
		control("background_worker_status_response", "worker-control", GoEliminatedByDesign, "Node worker IPC 控制面在 Go 单进程侧消灭"),
		control("background_worker_ingest_status_request", "ingest-worker", GoEliminatedByDesign, "Go 单进程内直接读 usagewriter.Runtime()"),
		control("background_worker_ingest_status_response", "ingest-worker", GoEliminatedByDesign, "Go 单进程内直接读 usagewriter.Runtime()"),
		control("background_worker_db_service_request", "worker-control", GoEliminatedByDesign, "DB service IPC 由 Go store 直连取代"),
		control("background_worker_db_service_response", "worker-control", GoEliminatedByDesign, "DB service IPC 由 Go store 直连取代"),
		control("background_worker_dataset_write_request", "ingest-worker", GoEliminatedByDesign, "Go 单进程内直写 dataset"),
		control("background_worker_dataset_write_response", "ingest-worker", GoEliminatedByDesign, "Go 单进程内直写 dataset"),
		control("background_worker_stats_write_request", "stats-worker", GoEliminatedByDesign, "Go 单进程内直写 stats（statsagg/statsverify）"),
		control("background_worker_stats_write_response", "stats-worker", GoEliminatedByDesign, "Go 单进程内直写 stats（statsagg/statsverify）"),
		control("background_worker_process_event_loop_request", "stats-worker", GoEliminatedByDesign, "Go 采样器直接采集本进程"),
		control("background_worker_process_event_loop_response", "stats-worker", GoEliminatedByDesign, "Go 采样器直接采集本进程"),
		control("server_account_runtime_clear", "ingest-worker", GoOwnedElsewhere, "gateway runtime cache 归 gateway"),
		control("gateway_runtime_cache_invalidate", "ingest-worker", GoOwnedElsewhere, "gateway runtime cache 归 gateway"),
		{JobName: "gateway_quota_snapshot_update", Category: CategoryControlIPC, Kind: "stats", DefaultRole: "stats-worker",
			Writes: []string{"server:gateway_quota_snapshot_cache"}, GoStatus: GoOwnedElsewhere, GoPackage: "gateway",
			GoBinding: "gateway 配额快照缓存归 gateway"},
		{JobName: "manual-account-test-queue", Category: CategoryLocalQueue, Kind: "probe", DefaultRole: "ops-worker",
			Writes: []string{"business:account_test_tasks"}, GoStatus: GoWired, GoPackage: "opsjobs + manualtestrepo + manualtest + accountprobe",
			GoBinding: "ManualTestQueue 引擎 + manualtestrepo 双模仓储 + manualtest 执行器全链路接线：组合根长驻组件（Start 启动维护 + sweep + Run 消费），诊断经 accountprobe.ManualDiagnostics（Key 池 / 单凭据分级）"},
		{JobName: "account-api-key-cooldown-retest-queue", Category: CategoryLocalQueue, Kind: "probe", DefaultRole: "ops-worker",
			Hotspot: true, LeaseRequired: true, Writes: []string{"business:account_api_key_runtime_states", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "accountquality + proberepo + accountprobe", GoBinding: "CooldownRetestRunner 队列随组合根探针家族接线执行"},
		{JobName: "account-quality-failure-precheck-queue", Category: CategoryLocalQueue, Kind: "probe", DefaultRole: "ops-worker",
			LeaseRequired: true, Writes: []string{"business:accounts"}, GoStatus: GoWired, GoPackage: "accountquality + proberepo + accountprobe",
			GoBinding: "PrecheckRunner 队列随组合根探针家族接线执行"},
		{JobName: "public-api-log-queue", Category: CategoryLocalQueue, Kind: "log", DefaultRole: "ingest-worker", Hotspot: true,
			Writes: []string{"dataset:public_api_logs"}, GoStatus: GoOwnedElsewhere, GoPackage: "gateway/internal/publicapilogs",
			GoBinding: "W7 gateway 进程内队列"},
		{JobName: "client-ip-policy-hit-buffer", Category: CategoryLocalQueue, Kind: "stats", DefaultRole: "stats-worker", Hotspot: true,
			Writes: []string{"stats:client_ip_policy_hits"}, GoStatus: NodeOnly, GoBinding: "Go 尚无实现（gateway 策略命中缓冲，登记缺失）"},
		{JobName: "gateway-account-side-effects", Category: CategoryLocalQueue, Kind: "maintenance", DefaultRole: "ops-worker",
			LeaseRequired: true, Writes: []string{"business:accounts"}, GoStatus: GoOwnedElsewhere, GoPackage: "gateway",
			GoBinding: "gateway 账户副作用队列归 gateway"},
		{JobName: "record-maintenance:api_key_related_cleanup", Category: CategoryMaintenanceTask, Kind: "maintenance", DefaultRole: "ops-worker",
			LeaseRequired: true, Writes: []string{"dataset:api_key_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention", GoBinding: "recordmaintenance runner + cleanuprepo 相关清理执行器（SQLite；PG 扣减链显式未迁移）"},
		{JobName: "record-maintenance:account_related_cleanup", Category: CategoryMaintenanceTask, Kind: "maintenance", DefaultRole: "ops-worker",
			LeaseRequired: true, Writes: []string{"dataset:account_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention", GoBinding: "recordmaintenance runner + cleanuprepo 相关清理执行器（SQLite；PG 扣减链显式未迁移）"},
		{JobName: "record-maintenance:usage_records_cleanup", Category: CategoryMaintenanceTask, Kind: "maintenance", DefaultRole: "ingest-worker",
			LeaseRequired: true, Writes: []string{"usage-shards:usage_records"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention", GoBinding: "cleanuprepo.UsageRecordsStore（SQLite 目录+分片；PG 分区裁剪+行删除）"},
		{JobName: "record-maintenance:non_business_data_cleanup", Category: CategoryMaintenanceTask, Kind: "maintenance", DefaultRole: "ingest-worker",
			LeaseRequired: true, Writes: []string{"dataset:*", "stats:*", "usage-shards:usage_records", "audit-payload-files:*"},
			GoStatus: GoWired, GoPackage: "cleanuprepo + retention", GoBinding: "cleanuprepo.NonBusinessDatasetStore + StatsRetentionStore（dataset/usage-catalog/stats 三 scope 双模；audit payload 归 F3）"},
		{JobName: "record-maintenance:account_usage_snapshot_upsert", Category: CategoryMaintenanceTask, Kind: "maintenance", DefaultRole: "ingest-worker",
			Writes: []string{"stats:account_usage_snapshots"}, GoStatus: GoWired, GoPackage: "cleanuprepo + retention",
			GoBinding: "cleanuprepo.RecordCleanupStore.UpsertAccountUsageSnapshots（owners 查 business accounts）"},
	}
}

// AllEntries 返回 scheduled + queue 全量注册表。
func AllEntries() []Entry {
	return append(ScheduledEntries(), QueueEntries()...)
}

// Find 按名字查注册条目。
func Find(name string) (Entry, bool) {
	for _, entry := range AllEntries() {
		if entry.JobName == name {
			return entry, true
		}
	}
	return Entry{}, false
}

// WiredJobNames 返回组合根当前可装配的 scheduled job 名集合（GoWired）。
func WiredJobNames() map[string]bool {
	wired := map[string]bool{}
	for _, entry := range ScheduledEntries() {
		if entry.GoStatus == GoWired {
			wired[entry.JobName] = true
		}
	}
	return wired
}

// DefaultSettingsSecondJobIntervals 是依赖 Node 系统设置默认值的调度间隔
// （schema-defaults.ts DEFAULT_SYSTEM_SETTINGS）。
const (
	StatsAggregationInterval     = 60 * time.Second
	SystemMetricsSampleInterval  = 30 * time.Second
	GroupAccountStatsInterval    = 60 * time.Second
	UsageHotWindowInterval       = 600 * time.Second
	CooldownRetestInterval       = 3 * time.Second
	OAuthTokenRefreshInterval    = 60 * time.Second
	AccountQualityRefreshDefault = 600 * time.Second
)
