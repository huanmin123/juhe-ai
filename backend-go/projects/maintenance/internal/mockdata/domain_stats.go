package mockdata

// 统计域（seedStatsRaw）：写 stats 库的原始样本面与 business 库的分组统计脏标记。
//
// 为什么只写样本与脏标记：统计页读的是派生窗口族（usage_stats_*、usage_model_*、
// usage_error_*、usage_latency_*、usage_rank_snapshots、*_windows、*_hourly、
// client_ip_*_daily、account_quality_*、group_account_stats、
// system_metrics_hourly / _trend_windows …），它们的口径必须与线上读路径一致，
// 只能由 jobs 的聚合器从明细重建（autofill.go 已把整族排除在自动补全之外）。
// 造数直接塞派生行会让页面口径与真实聚合分叉，所以本域只保证「明细输入足够」+
// 「派生窗口被显式标脏」，由 jobs 的聚合 / 窗口任务按真实口径重建。
//
// 写什么（列名逐字对齐 internal/schema/sqlite_schema_stats.go 与
// internal/schema/sqlite_schema_business.go）：
//   - stats.system_metrics_samples：监控趋势窗口族的唯一输入；
//   - stats.process_event_loop_samples：进程事件循环样本（读面按 process_role
//     取最新值与峰值）；
//   - stats.background_task_runs：系统指标页 runtime/jobs 列表的历史行
//     （gateway statreads.runtimeJobsHandler 直接读它）；
//   - stats.account_usage_snapshots（kind='openai_codex'）：账户列表的 5h / 7d
//     用量窗口（gateway accountsbalance.OAuthUsageWindowFromSnapshot 的读面键）；
//   - stats.client_ip_registry / client_ip_policies / client_ip_policy_hits：
//     IP 统计与封禁策略样本。策略读面是 registry INNER JOIN policies
//     （gatewayclientip.policy.go），只写策略不写 registry 会让策略行对读面不可见；
//   - business.group_account_stats_dirty + stats.usage_overview_dirty_scopes /
//     ai_performance_summary_dirty_system_accounts：派生缓存脏标记。
//
// 不写什么（宁可留空也不伪造）：
//   - 派生聚合族（上一段列举的全部窗口 / 小时 / 排行 / 缓存表）；
//   - stats_job_state、background_job_leases 与 *_owner_leases / *_key_cursors：
//     owner 游标与租约，伪造持有者会让运行时任务误判 lease 或跳过消费游标；
//   - usage_record_cleanup_deductions：记录清理任务的扣减台账，行语义是「该记录
//     已从分片删除并已扣减统计」；伪造行会让重试任务对不存在的记录做结算；
//   - account_health_current_state / account_health_outcomes /
//     account_health_key_cursors：J1 账户健康 owner 运行态，伪造 outcome 会让 J1
//     投影出假的账户状态（该库归 chat/codex/model-check 域）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	// statsSampleInterval 是系统指标与事件循环样本的采样间隔。设计文档要求
	// 30~60 分钟 1 条；取 30 分钟让近 31 天有 1489 个采样点，趋势窗口（小时 /
	// 天）在任意日期范围内都不会出现空洞。
	statsSampleInterval = 30 * time.Minute
	// statsClientIPRegistryBucketCount 与 jobs statsverify / gateway gatewayclientip
	// 的 ClientIPRegistryBucketCount 同值：bucket_no 由 ip_hash 前 8 位十六进制取模。
	statsClientIPRegistryBucketCount = 4096
	// statsAccountUsageSnapshotKind 是 account_usage_snapshots.kind 的 DDL CHECK
	// 值域中「OAuth 账户 5h / 7d 用量窗口」那一档。
	statsAccountUsageSnapshotKind = "openai_codex"
	// statsClientIPLimit 是 IP 登记 / 策略样本的 IP 数上限（页面样本够用，行数可控）。
	statsClientIPLimit = 6
	// statsPolicyHitDays 是策略命中样本覆盖的最近天数上限。
	statsPolicyHitDays = 7
)

// statsFallbackClientIPs 是 stats 库 usage_records 镜像为空时的 IP 样本兜底：
// 取值落在使用记录的 10.10.x.x / 10.20.x.x 网段内，保证 IP 页面样本与用量记录的
// 客户端 IP 家族一致（不做地址推断，只是同一网段的固定样本）。
var statsFallbackClientIPs = []string{"10.10.0.1", "10.10.0.2", "10.20.0.1"}

// statsProcessRoles 是 process_event_loop_samples 覆盖的进程角色值域
// （gateway statreads 的 validProcessRoles + worker 副本族正则）。
var statsProcessRoles = []string{
	"server", "ingest-worker", "stats-worker", "ops-worker", "db-service", "gateway:1",
}

// statsBackgroundTaskSample 是一条后台任务运行样本。job_name / job_type /
// worker_role 逐字取自 jobs/internal/jobregistry 的 ScheduledEntries 条目
// （job_type 即 Entry.Kind），status 覆盖 completed / failed / running / queued。
type statsBackgroundTaskSample struct {
	jobName    string
	jobType    string
	workerRole string
	status     string
	// attempt 是这次运行的尝试序号。background_task_runs 没有 attempt 列
	// （真实语义是「一次尝试一行」），因此它落在 params_json 里供页面 / 排查使用。
	attempt    int
	durationMs int
	errorText  string
	ageMinutes int
}

var statsBackgroundTaskSamples = []statsBackgroundTaskSample{
	{jobName: "system-metrics-sample", jobType: "sample", workerRole: "stats-worker", status: "completed", attempt: 1, durationMs: 420, ageMinutes: 12},
	{jobName: "usage-stats-aggregation", jobType: "stats", workerRole: "stats-worker", status: "completed", attempt: 1, durationMs: 1560, ageMinutes: 30},
	{jobName: "usage-hot-window-refresh", jobType: "snapshot", workerRole: "stats-worker", status: "completed", attempt: 2, durationMs: 2380, ageMinutes: 45},
	{jobName: "group-account-stats-refresh", jobType: "stats", workerRole: "stats-worker", status: "completed", attempt: 1, durationMs: 780, ageMinutes: 75},
	{jobName: "client-ip-stats-aggregation", jobType: "stats", workerRole: "stats-worker", status: "failed", attempt: 2, durationMs: 3120, errorText: "Mockdata 模拟分片读取超时", ageMinutes: 90},
	{jobName: "usage-rank-snapshots-refresh", jobType: "snapshot", workerRole: "stats-worker", status: "completed", attempt: 3, durationMs: 1960, ageMinutes: 110},
	{jobName: "background-task-run-reconcile", jobType: "maintenance", workerRole: "stats-worker", status: "completed", attempt: 1, durationMs: 260, ageMinutes: 150},
	{jobName: "data-retention-cleanup", jobType: "maintenance", workerRole: "ingest-worker", status: "failed", attempt: 1, durationMs: 5400, errorText: "Mockdata 模拟分片清理失败", ageMinutes: 300},
	{jobName: "account-balance-refresh", jobType: "probe", workerRole: "ops-worker", status: "running", attempt: 1, ageMinutes: 4},
	{jobName: "oauth-keepalive-token-refresh", jobType: "probe", workerRole: "ops-worker", status: "running", attempt: 2, ageMinutes: 8},
	{jobName: "account-quality-refresh", jobType: "stats", workerRole: "stats-worker", status: "queued", attempt: 1, ageMinutes: 2},
}

// statsMetricColumns 是 system_metrics_samples 的写入列。
var statsMetricColumns = []string{
	"id", "sampled_at", "cpu_percent", "memory_used_percent", "memory_total_bytes",
	"memory_free_bytes", "process_rss_bytes", "process_heap_used_bytes",
	"process_heap_total_bytes", "event_loop_lag_ms", "network_rx_bytes_per_sec",
	"network_tx_bytes_per_sec", "network_rx_total_bytes", "network_tx_total_bytes",
	"db_file_bytes", "stats_lag_seconds", "created_at",
}

// statsProcessSampleColumns 是 process_event_loop_samples 的写入列。
var statsProcessSampleColumns = []string{
	"id", "sampled_at", "process_role", "process_pid", "event_loop_lag_ms",
	"process_rss_bytes", "process_heap_used_bytes", "process_heap_total_bytes",
	"process_external_bytes", "process_array_buffers_bytes", "created_at",
}

// statsBackgroundTaskColumns 是 background_task_runs 的写入列。
var statsBackgroundTaskColumns = []string{
	"run_id", "job_name", "job_type", "worker_role", "status", "lease_key",
	"owner_id", "params_json", "result_json", "error_message", "submitted_at",
	"started_at", "heartbeat_at", "finished_at", "duration_ms", "exit_code",
	"created_at", "updated_at",
}

// statsAccountSnapshotColumns 是 account_usage_snapshots 的写入列。
var statsAccountSnapshotColumns = []string{
	"system_account_id", "account_id", "kind", "source", "snapshot_json",
	"refresh_status", "last_attempt_at", "last_success_at", "next_refresh_after",
	"last_error_message", "updated_at", "created_at",
}

// statsClientIPRegistryColumns 是 client_ip_registry 的写入列。
var statsClientIPRegistryColumns = []string{
	"ip_hash", "bucket_no", "aggregate_ip_key", "client_ip", "ip_version",
	"first_seen_at", "last_seen_at", "created_at", "updated_at",
}

// statsClientIPPolicyColumns 是 client_ip_policies 的写入列。
var statsClientIPPolicyColumns = []string{
	"id", "ip_hash", "policy_type", "status", "reason", "expires_at",
	"created_by_system_account_id", "created_at", "updated_at", "disabled_at",
	"disabled_by_system_account_id", "disabled_reason",
}

// statsClientIPPolicyHitColumns 是 client_ip_policy_hits 的写入列。
var statsClientIPPolicyHitColumns = []string{
	"ip_hash", "stat_date", "policy_id", "hit_count", "last_hit_at", "updated_at",
}

// statsOAuthAccount 是一条 business 库里的 OAuth mock 账户（账户用量快照的宿主）。
type statsOAuthAccount struct {
	id        string
	owner     string
	provider  string
	updatedAt time.Time
}

// statsStep 是一次造数步骤：失败即整体失败（半批数据比没有更难排查）。
type statsStep struct {
	name string
	run  func() error
}

// statsWriter 承载一次统计域写入：统一时钟、计数与从 usage 镜像读到的 IP 集合。
type statsWriter struct {
	ctx       context.Context
	e         *env
	now       time.Time
	counts    map[string]int
	clientIPs []string
}

// seedStatsRaw 是统计域入口。空数据根上 stats 库不存在即整体跳过（Counts 为空 =
// 该域未接线，覆盖报告据此把断言标成 not-covered，而不是报一个看不懂的失败）。
func seedStatsRaw(ctx context.Context, e *env) (DomainResult, error) {
	// openExisting 而不是 open：造数不得在空数据根上凭空创建 stats 库
	// （空根零写入契约，见 TestRunOnEmptyDataRootWritesReportAndSummary）。
	db, err := e.openExisting(StoreStats)
	if err != nil {
		return DomainResult{Name: DomainStats}, err
	}
	if db == nil {
		e.logger.Info("mockdata 统计域跳过", "reason", "stats 库文件不存在（先执行 --ensure-schema）")
		return DomainResult{Name: DomainStats}, nil
	}
	w := &statsWriter{ctx: ctx, e: e, now: e.options.clock().UTC(), counts: map[string]int{}}
	w.clientIPs = w.loadClientIPs()
	steps := []statsStep{
		{name: "系统指标样本", run: w.seedSystemMetricsSamples},
		{name: "进程事件循环样本", run: w.seedProcessEventLoopSamples},
		{name: "后台任务运行", run: w.seedBackgroundTaskRuns},
		{name: "账户用量快照", run: w.seedAccountUsageSnapshots},
		{name: "IP 统计样本", run: w.seedClientIPSamples},
		{name: "派生窗口脏标记", run: w.seedDirtyMarkers},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return DomainResult{Name: DomainStats, Counts: w.counts}, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return DomainResult{Name: DomainStats, Counts: w.counts}, nil
}

// stamp 返回本次造数的时间戳文本（毫秒 ISO，与各 store 的时间列形态一致）。
func (w *statsWriter) stamp() string {
	return w.now.Format(isoMillisLayout)
}

// add 记一次行数；0 行不入账（counts 只表达真实写入，空库上必须保持空）。
func (w *statsWriter) add(key string, rows int) {
	if rows > 0 {
		w.counts[key] += rows
	}
}

// requireStatsTable 检查 stats 库里的表是否存在；缺失时记日志并返回 false。
// 本域只写不建：stats schema 由 --ensure-schema 负责，造数自行建表会让
// 「空根零写入」与「schema 权威在一处」两条约定同时失效。
func (w *statsWriter) requireStatsTable(table string) (bool, error) {
	exists, err := w.e.existsTable(w.ctx, StoreStats, table)
	if err != nil {
		return false, err
	}
	if !exists {
		w.e.logger.Warn("mockdata 统计域跳过：stats 库缺表", "table", table)
	}
	return exists, nil
}

// seedSystemMetricsSamples 写近 Days 天、每 statsSampleInterval 一条的系统指标样本。
func (w *statsWriter) seedSystemMetricsSamples() error {
	exists, err := w.requireStatsTable("system_metrics_samples")
	if err != nil || !exists {
		return err
	}
	instants := statsSampleInstants(w.now, w.e.options.Days)
	rows := make([]map[string]any, 0, len(instants))
	for index, instant := range instants {
		createdAt := instant.Format(isoMillisLayout)
		cpu := 6 + mockPseudoRandom(index, 11)*46
		if index%89 == 0 {
			// 峰值样本：趋势页的「最近峰值」需要有可解释的高点。
			cpu = 88 + mockPseudoRandom(index, 12)*10
		}
		memory := 32 + mockPseudoRandom(index, 13)*44
		if index%131 == 0 {
			memory = 76 + mockPseudoRandom(index, 14)*18
		}
		eventLoopLag := 2 + mockPseudoRandom(index, 15)*14
		if index%97 == 0 {
			eventLoopLag = 90 + mockPseudoRandom(index, 16)*120
		}
		statsLag := index % 13
		if index%211 == 0 {
			statsLag = 42
		}
		const memoryTotalBytes = int64(16) << 30
		memoryFreeBytes := int64(float64(memoryTotalBytes) * (1 - memory/100))
		heapUsed := int64((90 + mockPseudoRandom(index, 17)*140) * float64(int64(1)<<20))
		rows = append(rows, map[string]any{
			"id":                       fmt.Sprintf("%sstats_metric_%s", CleanupIDPrefix, instant.Format("20060102150405")),
			"sampled_at":               createdAt,
			"cpu_percent":              statsRounded(cpu),
			"memory_used_percent":      statsRounded(memory),
			"memory_total_bytes":       memoryTotalBytes,
			"memory_free_bytes":        memoryFreeBytes,
			"process_rss_bytes":        int64((180 + mockPseudoRandom(index, 18)*260) * float64(int64(1)<<20)),
			"process_heap_used_bytes":  heapUsed,
			"process_heap_total_bytes": int64(float64(heapUsed) * 1.6),
			"event_loop_lag_ms":        statsRounded(eventLoopLag),
			"network_rx_bytes_per_sec": statsRounded(120000 + mockPseudoRandom(index, 19)*6800000),
			"network_tx_bytes_per_sec": statsRounded(90000 + mockPseudoRandom(index, 20)*5200000),
			"network_rx_total_bytes":   int64(index+1) * 380_000_000,
			"network_tx_total_bytes":   int64(index+1) * 290_000_000,
			"db_file_bytes":            (140 << 20) + int64(index)*196_608,
			"stats_lag_seconds":        statsLag,
			"created_at":               createdAt,
		})
	}
	inserted, err := w.insertRows("system_metrics_samples", statsMetricColumns, rows)
	if err != nil {
		return err
	}
	w.add("systemMetricsSamples", inserted)
	return nil
}

// seedProcessEventLoopSamples 与系统指标同栅格采样，逐进程角色各写一行：
// 读面对同一角色取最新值与峰值，缺角色会让「进程状态」面板缺卡片。
func (w *statsWriter) seedProcessEventLoopSamples() error {
	exists, err := w.requireStatsTable("process_event_loop_samples")
	if err != nil || !exists {
		return err
	}
	instants := statsSampleInstants(w.now, w.e.options.Days)
	rows := make([]map[string]any, 0, len(instants)*len(statsProcessRoles))
	for roleIndex, role := range statsProcessRoles {
		for index, instant := range instants {
			createdAt := instant.Format(isoMillisLayout)
			seed := index + roleIndex*1000
			lag := 1 + mockPseudoRandom(seed, 21)*9
			if index%127 == 0 {
				lag = 60 + mockPseudoRandom(seed, 22)*180
			}
			heapUsed := int64((48 + mockPseudoRandom(seed, 23)*70) * float64(int64(1)<<20))
			rows = append(rows, map[string]any{
				"id":                          fmt.Sprintf("%sstats_loop_%s_%s", CleanupIDPrefix, statsRoleSlug(role), instant.Format("20060102150405")),
				"sampled_at":                  createdAt,
				"process_role":                role,
				"process_pid":                 4200 + roleIndex*16,
				"event_loop_lag_ms":           statsRounded(lag),
				"process_rss_bytes":           int64((160 + mockPseudoRandom(seed, 24)*120) * float64(int64(1)<<20)),
				"process_heap_used_bytes":     heapUsed,
				"process_heap_total_bytes":    int64(float64(heapUsed) * 1.8),
				"process_external_bytes":      int64((12 + mockPseudoRandom(seed, 25)*40) * float64(int64(1)<<20)),
				"process_array_buffers_bytes": int64((2 + mockPseudoRandom(seed, 26)*18) * float64(int64(1)<<20)),
				"created_at":                  createdAt,
			})
		}
	}
	inserted, err := w.insertRows("process_event_loop_samples", statsProcessSampleColumns, rows)
	if err != nil {
		return err
	}
	w.add("processEventLoopSamples", inserted)
	return nil
}

// seedBackgroundTaskRuns 写后台任务运行样本。
func (w *statsWriter) seedBackgroundTaskRuns() error {
	exists, err := w.requireStatsTable("background_task_runs")
	if err != nil || !exists {
		return err
	}
	rows := make([]map[string]any, 0, len(statsBackgroundTaskSamples))
	for index, sample := range statsBackgroundTaskSamples {
		submittedAt := w.now.Add(-time.Duration(sample.ageMinutes) * time.Minute)
		submittedText := submittedAt.Format(isoMillisLayout)
		slug := statsRoleSlug(sample.jobName)
		row := map[string]any{
			"run_id":       fmt.Sprintf("%staskrun_%s_%02d", CleanupIDPrefix, slug, index+1),
			"job_name":     sample.jobName,
			"job_type":     sample.jobType,
			"worker_role":  sample.workerRole,
			"status":       sample.status,
			"lease_key":    CleanupIDPrefix + "taskrun_lease_" + slug,
			"owner_id":     CleanupIDPrefix + "jobs_" + statsRoleSlug(sample.workerRole),
			"params_json":  statsJSON(map[string]any{"seed": CleanupIDPrefix + "stats", "attempt": sample.attempt}),
			"result_json":  "{}",
			"submitted_at": submittedText,
			"created_at":   submittedText,
			"updated_at":   submittedText,
		}
		switch sample.status {
		case "completed", "failed":
			startedAt := submittedAt.Add(time.Second)
			finishedAt := startedAt.Add(time.Duration(sample.durationMs) * time.Millisecond)
			row["started_at"] = startedAt.Format(isoMillisLayout)
			row["finished_at"] = finishedAt.Format(isoMillisLayout)
			row["heartbeat_at"] = finishedAt.Format(isoMillisLayout)
			row["duration_ms"] = sample.durationMs
			row["updated_at"] = finishedAt.Format(isoMillisLayout)
			exitCode := 0
			if sample.status == "failed" {
				exitCode = 1
				row["error_message"] = sample.errorText
			}
			row["exit_code"] = exitCode
			row["result_json"] = statsJSON(map[string]any{
				"seed": CleanupIDPrefix + "stats", "attempt": sample.attempt, "success": sample.status == "completed",
			})
		case "running":
			// running / queued 行必须留新（心跳与提交时间贴近 now）：jobs 的
			// background-task-run-reconcile 会把心跳过期的 running 行改判 failed，
			// 那样页面样本会在造数后立刻变样。
			row["started_at"] = submittedAt.Format(isoMillisLayout)
			row["heartbeat_at"] = w.now.Add(-30 * time.Second).Format(isoMillisLayout)
			row["updated_at"] = row["heartbeat_at"]
		default: // queued
			row["updated_at"] = submittedText
		}
		rows = append(rows, row)
	}
	inserted, err := w.insertRows("background_task_runs", statsBackgroundTaskColumns, rows)
	if err != nil {
		return err
	}
	w.add("backgroundTaskRuns", inserted)
	return nil
}

// seedAccountUsageSnapshots 给每个 business 库里的 OAuth mock 账户写一条
// kind='openai_codex' 的 5h / 7d 用量快照。
func (w *statsWriter) seedAccountUsageSnapshots() error {
	exists, err := w.requireStatsTable("account_usage_snapshots")
	if err != nil || !exists {
		return err
	}
	accounts, err := w.oauthAccounts()
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		w.e.logger.Warn("mockdata 统计域跳过账户用量快照：business 库没有 type=oauth 的 mock 账户")
		return nil
	}
	rows := make([]map[string]any, 0, len(accounts))
	for index, account := range accounts {
		updatedAt := account.updatedAt
		updatedText := updatedAt.Format(isoMillisLayout)
		fiveHourReset := updatedAt.Add(2 * time.Hour).Add(time.Duration(index) * 37 * time.Minute)
		sevenDayReset := updatedAt.Add(3 * 24 * time.Hour).Add(time.Duration(index) * 5 * time.Hour)
		payload := map[string]any{
			// 键名与 gatewaydispatch.BuildOpenAICodexUsageSnapshotPayload 一致：
			// 读面只认 codex_<窗口>_used_percent / _reset_at / _reset_after_seconds /
			// _window_minutes。
			"codex_usage_updated_at":       updatedText,
			"source":                       "codex",
			"seed":                         CleanupIDPrefix + "stats",
			"codex_5h_used_percent":        42.5 + float64(index)*23,
			"codex_5h_reset_after_seconds": int(fiveHourReset.Sub(updatedAt).Seconds()),
			"codex_5h_window_minutes":      300,
			"codex_5h_reset_at":            fiveHourReset.Format(isoMillisLayout),
			"codex_7d_used_percent":        61 - float64(index)*15.5,
			"codex_7d_reset_after_seconds": int(sevenDayReset.Sub(updatedAt).Seconds()),
			"codex_7d_window_minutes":      10080,
			"codex_7d_reset_at":            sevenDayReset.Format(isoMillisLayout),
		}
		rows = append(rows, map[string]any{
			"system_account_id":  account.owner,
			"account_id":         account.id,
			"kind":               statsAccountUsageSnapshotKind,
			"source":             "codex",
			"snapshot_json":      statsJSON(payload),
			"refresh_status":     "ok",
			"last_attempt_at":    updatedText,
			"last_success_at":    updatedText,
			"next_refresh_after": updatedAt.Add(5 * time.Minute).Format(isoMillisLayout),
			"last_error_message": nil,
			"updated_at":         updatedText,
			"created_at":         updatedText,
		})
	}
	inserted, err := w.upsertRows(StoreStats, "account_usage_snapshots", statsAccountSnapshotColumns, rows,
		`ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
			source = excluded.source,
			snapshot_json = excluded.snapshot_json,
			refresh_status = excluded.refresh_status,
			last_attempt_at = excluded.last_attempt_at,
			last_success_at = excluded.last_success_at,
			next_refresh_after = excluded.next_refresh_after,
			last_error_message = excluded.last_error_message,
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	w.add("accountUsageSnapshots", inserted)
	return nil
}

// seedClientIPSamples 写 IP 登记、封禁 / 放行策略与命中样本。
//
// IP 取值优先来自 stats 库 usage_records 镜像里的真实 client_ip：登记行与用量
// 明细同一个 IP，IP 列表页才不是凭空多出来的地址；镜像为空（usage 域未接线）时
// 用同网段的固定兜底样本。
func (w *statsWriter) seedClientIPSamples() error {
	registryExists, err := w.requireStatsTable("client_ip_registry")
	if err != nil || !registryExists {
		return err
	}
	policyExists, err := w.requireStatsTable("client_ip_policies")
	if err != nil || !policyExists {
		return err
	}
	hitExists, err := w.requireStatsTable("client_ip_policy_hits")
	if err != nil || !hitExists {
		return err
	}
	nowText := w.stamp()
	firstSeenText := w.now.AddDate(0, 0, -w.e.options.Days).Format(isoMillisLayout)
	lastSeenText := w.now.Add(-5 * time.Minute).Format(isoMillisLayout)
	registryRows := make([]map[string]any, 0, len(w.clientIPs))
	hashByIP := map[string]string{}
	for _, clientIP := range w.clientIPs {
		ipHash, bucketNo := statsClientIPIdentity(clientIP)
		hashByIP[clientIP] = ipHash
		registryRows = append(registryRows, map[string]any{
			"ip_hash":          ipHash,
			"bucket_no":        bucketNo,
			"aggregate_ip_key": clientIP,
			"client_ip":        clientIP,
			"ip_version":       4,
			"first_seen_at":    firstSeenText,
			"last_seen_at":     lastSeenText,
			"created_at":       nowText,
			"updated_at":       nowText,
		})
	}
	inserted, err := w.upsertRows(StoreStats, "client_ip_registry", statsClientIPRegistryColumns, registryRows,
		`ON CONFLICT(ip_hash) DO UPDATE SET
			bucket_no = excluded.bucket_no,
			aggregate_ip_key = excluded.aggregate_ip_key,
			client_ip = excluded.client_ip,
			ip_version = excluded.ip_version,
			first_seen_at = MIN(client_ip_registry.first_seen_at, excluded.first_seen_at),
			last_seen_at = MAX(client_ip_registry.last_seen_at, excluded.last_seen_at),
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	w.add("clientIPRegistry", inserted)

	owner := w.ownerSystemAccountID()
	policies := w.clientIPPolicies(hashByIP)
	policyRows := make([]map[string]any, 0, len(policies))
	for _, policy := range policies {
		row := map[string]any{
			"id":                            policy.id,
			"ip_hash":                       policy.ipHash,
			"policy_type":                   policy.policyType,
			"status":                        policy.status,
			"reason":                        policy.reason,
			"expires_at":                    nil,
			"created_by_system_account_id":  owner,
			"created_at":                    nowText,
			"updated_at":                    nowText,
			"disabled_at":                   nil,
			"disabled_by_system_account_id": nil,
			"disabled_reason":               nil,
		}
		if policy.status == "disabled" {
			row["disabled_at"] = nowText
			row["disabled_by_system_account_id"] = owner
			row["disabled_reason"] = CleanupNamePrefix + "样本：验证停用展示"
		}
		policyRows = append(policyRows, row)
	}
	inserted, err = w.upsertRows(StoreStats, "client_ip_policies", statsClientIPPolicyColumns, policyRows,
		`ON CONFLICT(id) DO UPDATE SET
			ip_hash = excluded.ip_hash,
			policy_type = excluded.policy_type,
			status = excluded.status,
			reason = excluded.reason,
			updated_at = excluded.updated_at,
			disabled_at = excluded.disabled_at,
			disabled_by_system_account_id = excluded.disabled_by_system_account_id,
			disabled_reason = excluded.disabled_reason`)
	if err != nil {
		return err
	}
	w.add("clientIPPolicies", inserted)

	// 命中样本只给启用中的黑名单策略：命中计数是运行时缓冲写入的派生计数，
	// 这里只提供页面样本行（同样的 (ip_hash, stat_date, policy_id) 主键，重复执行覆盖写）。
	var hitRows []map[string]any
	for _, policy := range policies {
		if policy.policyType != "blacklist" || policy.status != "active" {
			continue
		}
		days := w.e.options.Days
		if days > statsPolicyHitDays {
			days = statsPolicyHitDays
		}
		for dayIndex := 0; dayIndex < days; dayIndex++ {
			hitRows = append(hitRows, map[string]any{
				"ip_hash":     policy.ipHash,
				"stat_date":   w.now.AddDate(0, 0, -dayIndex).Format("2006-01-02"),
				"policy_id":   policy.id,
				"hit_count":   1 + (dayIndex*7)%23,
				"last_hit_at": w.now.Add(-time.Duration(dayIndex*3+1) * time.Hour).Format(isoMillisLayout),
				"updated_at":  nowText,
			})
		}
	}
	inserted, err = w.upsertRows(StoreStats, "client_ip_policy_hits", statsClientIPPolicyHitColumns, hitRows,
		`ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
			hit_count = excluded.hit_count,
			last_hit_at = excluded.last_hit_at,
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	w.add("clientIPPolicyHits", inserted)
	return nil
}

// statsClientIPPolicy 是一条待写入的封禁 / 放行策略。
type statsClientIPPolicy struct {
	id         string
	ipHash     string
	policyType string
	status     string
	reason     string
}

// clientIPPolicies 构造策略样本：启用中的黑名单（有命中）、启用中的放行名单、
// 以及一条已停用黑名单（覆盖 status 与 disabled_* 列）。
func (w *statsWriter) clientIPPolicies(hashByIP map[string]string) []statsClientIPPolicy {
	var policies []statsClientIPPolicy
	pick := func(index int) (string, bool) {
		if index >= len(w.clientIPs) {
			return "", false
		}
		return hashByIP[w.clientIPs[index]], true
	}
	if ipHash, ok := pick(0); ok {
		policies = append(policies, statsClientIPPolicy{
			id: CleanupIDPrefix + "cip_policy_blacklist", ipHash: ipHash, policyType: "blacklist",
			status: "active", reason: CleanupNamePrefix + "样本：IP 封禁",
		})
	}
	if ipHash, ok := pick(1); ok {
		policies = append(policies, statsClientIPPolicy{
			id: CleanupIDPrefix + "cip_policy_allowlist", ipHash: ipHash, policyType: "allowlist",
			status: "active", reason: CleanupNamePrefix + "样本：IP 放行",
		})
	}
	if ipHash, ok := pick(2); ok {
		policies = append(policies, statsClientIPPolicy{
			id: CleanupIDPrefix + "cip_policy_disabled", ipHash: ipHash, policyType: "blacklist",
			status: "disabled", reason: CleanupNamePrefix + "样本：已停用封禁",
		})
	}
	return policies
}

// seedDirtyMarkers 写派生缓存脏标记：business 库的分组统计脏行 + stats 库的
// 概览 / AI 性能摘要脏范围。
//
// 为什么写脏标记而不是直接写派生表：脏标记是既有 jobs 任务的正常输入（消费后
// 自行重建并删除标记），它让「造数刚写完明细」这件事被运行时看见；直接写派生表
// 才是伪造口径。
func (w *statsWriter) seedDirtyMarkers() error {
	updatedAt := w.stamp()
	if exists, err := w.e.existsTable(w.ctx, StoreBusiness, "group_account_stats_dirty"); err != nil {
		return err
	} else if exists {
		groups, err := w.mockGroupIDs()
		if err != nil {
			return err
		}
		rows := make([]map[string]any, 0, len(groups))
		for _, groupID := range groups {
			rows = append(rows, map[string]any{
				"group_id": groupID,
				// reason 是运维可读的自由文本；语义：本分组的分组统计需要重建，
				// 因为造数重写了它的用量明细。
				"reason":     CleanupIDPrefix + "usage_seed",
				"updated_at": updatedAt,
			})
		}
		inserted, err := w.upsertRows(StoreBusiness, "group_account_stats_dirty", []string{"group_id", "reason", "updated_at"}, rows,
			`ON CONFLICT(group_id) DO UPDATE SET
				reason = excluded.reason,
				updated_at = excluded.updated_at`)
		if err != nil {
			return err
		}
		w.add("groupAccountStatsDirty", inserted)
	} else {
		w.e.logger.Warn("mockdata 统计域跳过分组统计脏标记：business 库缺表", "table", "group_account_stats_dirty")
	}

	systemAccounts, err := w.mockSystemAccountIDs()
	if err != nil {
		return err
	}
	if len(systemAccounts) == 0 {
		w.e.logger.Warn("mockdata 统计域跳过派生窗口脏范围：business 库没有 mock 账户")
		return nil
	}
	minDate := w.now.AddDate(0, 0, -(w.e.options.Days - 1)).Format("2006-01-02")
	maxDate := w.now.Format("2006-01-02")

	overviewExists, err := w.requireStatsTable("usage_overview_dirty_scopes")
	if err != nil {
		return err
	}
	if overviewExists {
		rows := make([]map[string]any, 0, len(systemAccounts)+1)
		// global 作用域与线上一致（每条用量明细都会累进全局概览窗口）。
		for _, accountID := range append(append([]string{}, systemAccounts...), "global") {
			rows = append(rows, map[string]any{
				"system_account_id": accountID,
				"scope_id":          accountID,
				"min_changed_date":  minDate,
				"generation":        1,
				"first_dirty_at":    updatedAt,
				"updated_at":        updatedAt,
			})
		}
		inserted, err := w.upsertRows(StoreStats, "usage_overview_dirty_scopes",
			[]string{"system_account_id", "scope_id", "min_changed_date", "generation", "first_dirty_at", "updated_at"}, rows,
			`ON CONFLICT(system_account_id) DO UPDATE SET
				scope_id = excluded.scope_id,
				min_changed_date = MIN(usage_overview_dirty_scopes.min_changed_date, excluded.min_changed_date),
				generation = usage_overview_dirty_scopes.generation + 1,
				updated_at = excluded.updated_at`)
		if err != nil {
			return err
		}
		w.add("usageOverviewDirtyScopes", inserted)
	}

	aiExists, err := w.requireStatsTable("ai_performance_summary_dirty_system_accounts")
	if err != nil {
		return err
	}
	if aiExists {
		rows := make([]map[string]any, 0, len(systemAccounts))
		for _, accountID := range systemAccounts {
			rows = append(rows, map[string]any{
				"system_account_id": accountID,
				"min_stat_date":     minDate,
				"max_stat_date":     maxDate,
				"generation":        1,
				"first_dirty_at":    updatedAt,
				"updated_at":        updatedAt,
			})
		}
		inserted, err := w.upsertRows(StoreStats, "ai_performance_summary_dirty_system_accounts",
			[]string{"system_account_id", "min_stat_date", "max_stat_date", "generation", "first_dirty_at", "updated_at"}, rows,
			`ON CONFLICT(system_account_id) DO UPDATE SET
				min_stat_date = MIN(ai_performance_summary_dirty_system_accounts.min_stat_date, excluded.min_stat_date),
				max_stat_date = MAX(ai_performance_summary_dirty_system_accounts.max_stat_date, excluded.max_stat_date),
				generation = ai_performance_summary_dirty_system_accounts.generation + 1,
				updated_at = excluded.updated_at`)
		if err != nil {
			return err
		}
		w.add("aiPerformanceSummaryDirtySystemAccounts", inserted)
	}
	return nil
}

// loadClientIPs 从 stats 库 usage_records 镜像读真实 client_ip；读不到时用同网段兜底。
func (w *statsWriter) loadClientIPs() []string {
	db, err := w.e.openExisting(StoreStats)
	if err != nil || db == nil {
		return append([]string{}, statsFallbackClientIPs...)
	}
	exists, err := queryExistsTable(w.ctx, db, "usage_records")
	if err != nil || !exists {
		return append([]string{}, statsFallbackClientIPs...)
	}
	rows, err := db.QueryContext(w.ctx,
		`SELECT DISTINCT client_ip FROM usage_records
		 WHERE client_ip IS NOT NULL AND client_ip <> '' ORDER BY client_ip LIMIT ?`, statsClientIPLimit)
	if err != nil {
		w.e.logger.Warn("mockdata 统计域读取 client_ip 失败，改用兜底 IP 样本", "error", err.Error())
		return append([]string{}, statsFallbackClientIPs...)
	}
	defer rows.Close()
	var ips []string
	for rows.Next() {
		var clientIP string
		if err := rows.Scan(&clientIP); err != nil {
			w.e.logger.Warn("mockdata 统计域读取 client_ip 行失败", "error", err.Error())
			break
		}
		ips = append(ips, clientIP)
	}
	if err := rows.Err(); err != nil {
		w.e.logger.Warn("mockdata 统计域遍历 client_ip 失败", "error", err.Error())
	}
	if len(ips) == 0 {
		return append([]string{}, statsFallbackClientIPs...)
	}
	return ips
}

// oauthAccounts 读取 business 库的 OAuth mock 账户；库或表缺失返回空集而不是错误
// （stats 域必须能在 business 域未跑的数据根上写样本）。
func (w *statsWriter) oauthAccounts() ([]statsOAuthAccount, error) {
	exists, err := w.e.existsTable(w.ctx, StoreBusiness, "accounts")
	if err != nil || !exists {
		return nil, err
	}
	db, err := w.e.openExisting(StoreBusiness)
	if err != nil || db == nil {
		return nil, err
	}
	rows, err := db.QueryContext(w.ctx,
		`SELECT id, system_account_id, COALESCE(provider_code,'') FROM accounts
		 WHERE (id LIKE ? OR name LIKE ?) AND type = 'oauth' ORDER BY id`,
		CleanupIDPrefix+"%", CleanupNamePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("读取 OAuth mock 账户: %w", err)
	}
	defer rows.Close()
	var accounts []statsOAuthAccount
	for rows.Next() {
		var account statsOAuthAccount
		if err := rows.Scan(&account.id, &account.owner, &account.provider); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range accounts {
		// 快照时间按账户错开，页面上的「最近刷新」不是同一时刻。
		accounts[index].updatedAt = w.now.Add(-time.Duration(3+index*4) * time.Minute)
	}
	return accounts, nil
}

// mockGroupIDs 返回 business 库里的 mock 分组 ID。
func (w *statsWriter) mockGroupIDs() ([]string, error) {
	return w.mockBusinessIDs("groups")
}

// mockSystemAccountIDs 返回 mock 账户归属的系统账户 ID（去重）。
func (w *statsWriter) mockSystemAccountIDs() ([]string, error) {
	exists, err := w.e.existsTable(w.ctx, StoreBusiness, "accounts")
	if err != nil || !exists {
		return nil, err
	}
	db, err := w.e.openExisting(StoreBusiness)
	if err != nil || db == nil {
		return nil, err
	}
	rows, err := db.QueryContext(w.ctx,
		`SELECT DISTINCT system_account_id FROM accounts
		 WHERE (id LIKE ? OR name LIKE ?) AND system_account_id <> '' ORDER BY system_account_id`,
		CleanupIDPrefix+"%", CleanupNamePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("读取 mock 账户归属系统账户: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ownerSystemAccountID 返回策略行的创建人：优先 mock 账户归属的系统账户，
// 取不到时回落到带清理标识的占位名（该列无外键约束，但取值必须可检索）。
func (w *statsWriter) ownerSystemAccountID() string {
	ids, err := w.mockSystemAccountIDs()
	if err == nil && len(ids) > 0 {
		return ids[0]
	}
	return CleanupIDPrefix + "admin"
}

// mockBusinessIDs 读取 business 库里带清理标识的 ID 列表（表不存在返回空集）。
func (w *statsWriter) mockBusinessIDs(table string) ([]string, error) {
	exists, err := w.e.existsTable(w.ctx, StoreBusiness, table)
	if err != nil || !exists {
		return nil, err
	}
	db, err := w.e.openExisting(StoreBusiness)
	if err != nil || db == nil {
		return nil, err
	}
	rows, err := db.QueryContext(w.ctx,
		`SELECT id FROM `+table+` WHERE id LIKE ? OR name LIKE ? ORDER BY id`,
		CleanupIDPrefix+"%", CleanupNamePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("读取 mock %s: %w", table, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// insertRows 批量插入（冲突即忽略），返回真实插入行数。
//
// 为什么冲突即忽略：清理步骤已删除上一批同标识行；再兜一层主键去重，保证
// 「清理被跳过」（表缺失、库被外部重建）时行数也不会翻倍——幂等由主键保证，
// 而不是只依赖清理顺序。
func (w *statsWriter) insertRows(table string, columns []string, rows []map[string]any) (int, error) {
	return statsInsertRows(w.ctx, w.e, StoreStats, table, columns, rows, "ON CONFLICT DO NOTHING")
}

// upsertRows 批量 upsert（显式冲突子句），返回受影响行数。
//
// 没有清理规则的表（account_usage_snapshots、client_ip_registry、脏标记族）靠
// 主键覆盖写保持行数稳定：第二次造数必须与第一次写入同样的行数，否则
// 「重复执行 count 键逐键相同」不成立。
func (w *statsWriter) upsertRows(storeName, table string, columns []string, rows []map[string]any, conflict string) (int, error) {
	return statsInsertRows(w.ctx, w.e, storeName, table, columns, rows, conflict)
}

// statsInsertRows 是批量写入内核：一条 INSERT 多行参数，列序与 columns 一致。
func statsInsertRows(ctx context.Context, e *env, storeName, table string, columns []string, rows []map[string]any, conflict string) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" +
		sqlitePlaceholders(len(columns)) + ")"
	if conflict != "" {
		query += " " + conflict
	}
	tx, err := e.open(storeName)
	if err != nil {
		return 0, err
	}
	transaction, err := tx.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("%s begin: %w", table, err)
	}
	statement, err := transaction.PrepareContext(ctx, query)
	if err != nil {
		_ = transaction.Rollback()
		return 0, fmt.Errorf("%s prepare: %w", table, err)
	}
	defer statement.Close()
	affected := 0
	for _, row := range rows {
		result, err := statement.ExecContext(ctx, recordValues(row, columns)...)
		if err != nil {
			_ = transaction.Rollback()
			return affected, fmt.Errorf("%s 写入: %w", table, err)
		}
		if count, err := result.RowsAffected(); err == nil && count > 0 {
			affected++
		}
	}
	if err := transaction.Commit(); err != nil {
		return affected, fmt.Errorf("%s commit: %w", table, err)
	}
	return affected, nil
}

// statsSampleInstants 返回近 days 天、间隔 statsSampleInterval 的采样时刻，
// 末尾对齐 now（截断到采样栅格）：同一 Now 下逐字节可复现。
func statsSampleInstants(now time.Time, days int) []time.Time {
	interval := statsSampleInterval
	end := now.UTC().Truncate(interval)
	count := days * 24 * 60 / int(interval.Minutes())
	if count < 1 {
		count = 1
	}
	start := end.Add(-time.Duration(count) * interval)
	instants := make([]time.Time, 0, count+1)
	for index := 0; index <= count; index++ {
		instants = append(instants, start.Add(time.Duration(index)*interval))
	}
	return instants
}

// statsRounded 保留两位小数：浮点尾差会让「同一 Now 同一结果」的比对不稳定。
func statsRounded(value float64) float64 {
	return math.Round(value*100) / 100
}

// statsRoleSlug 把角色 / 任务名转成可读的标识片段（去掉冒号等非标识字符）。
func statsRoleSlug(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

// statsJSON 序列化 JSON 列；失败时返回 "{}"（JSON 列都是 NOT NULL 的 TEXT）。
func statsJSON(document map[string]any) string {
	encoded, err := json.Marshal(document)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// statsClientIPIdentity 复刻 jobs statsverify / gateway gatewayclientip 的
// clientIpIdentity：ipHash = sha256("client-ip:" + aggregateIpKey)，
// bucketNo = parseInt(ipHash[0:8], 16) % 4096。策略读面按 ip_hash 关联
// registry，哈希算法不一致会让策略行直接不可见。
func statsClientIPIdentity(clientIP string) (string, int) {
	sum := sha256.Sum256([]byte("client-ip:" + clientIP))
	ipHash := hex.EncodeToString(sum[:])
	bucketNo, err := strconv.ParseInt(ipHash[0:8], 16, 64)
	if err != nil {
		return ipHash, 0
	}
	return ipHash, int(bucketNo % statsClientIPRegistryBucketCount)
}
