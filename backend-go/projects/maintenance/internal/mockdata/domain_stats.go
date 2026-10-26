package mockdata

// stats 域（T66 PG 造数第二波）：juhe_stats 的原始写入面样本。
//
// 为什么只写样本与脏标记：统计页读的是派生窗口族（usage_stats_*、usage_model_*、
// usage_error_*、usage_latency_*、usage_rank_snapshots、*_windows、*_hourly、
// client_ip_*_daily、account_quality_*、group_account_stats …），它们的口径必须
// 与线上读路径一致，只能由 jobs 的聚合器从明细重建（cleanup 已整表重置派生面）。
// 造数直接塞派生行会让页面口径与真实聚合分叉，所以本域只保证「明细输入足够」+
// 「派生窗口被显式标脏」，由 jobs 的聚合 / 窗口任务按真实口径重建。
//
// 写什么（列名与列型逐列对照 internal/schema/pg_schema_stats.go）：
//   - background_task_runs：后台任务运行历史样本（completed / failed / running /
//     queued 四态；running 行心跳贴近 now，避免被 background-task-run-reconcile
//     立刻改判 failed）；
//   - account_usage_snapshots：三个 kind 的读面样本——openai_codex（OAuth mock
//     账户的 5h / 7d 用量窗口，键名对齐 gateway
//     BuildOpenAICodexUsageSnapshotPayload / OAuthUsageWindowFromSnapshot）、
//     relay_balance（多 Key 余额账户的余额快照，键名对齐 jobs
//     balanceSnapshotPersist 与 shared accountbalance.Snapshot）、xai_grok（
//     Grok 用量快照形状，键名对齐 jobs xaiGrokSnapshotPayload；business 域没有
//     xai 账户，样本宿主是 OAuth 主力账户，网关按供应商取 kind，不会读它）。
//     timestamptz 列（last_attempt_at / last_success_at / next_refresh_after /
//     updated_at / created_at）写 time.Time；
//   - client_ip_registry / client_ip_policies / client_ip_policy_hits：IP 登记、
//     封禁 / 放行策略与命中样本。IP 取值来自 usage 域明细的真实 client_ip
//     （同一 IP 家族，IP 页面样本不凭空多出地址）；ip_hash 算法与 gateway
//     gatewayclientip 的 clientIpIdentity 同源；
//   - 脏标记：juhe_business.group_account_stats_dirty +
//     juhe_stats.usage_overview_dirty_scopes（含 global 作用域）/
//     ai_performance_summary_dirty_system_accounts——派生缓存的消费队列，
//     让「造数刚重写了明细」被运行时看见。
//
// 不写什么（宁可留空也不伪造）：
//   - 派生聚合族（cleanupAggregateResetTables 列出的全部窗口 / 小时 / 排行 /
//     缓存表）；
//   - stats_job_state、background_job_leases：owner 游标与租约，伪造持有者会
//     让运行时任务误判 lease 或跳过消费游标；
//   - usage_record_cleanup_deductions：记录清理任务的扣减台账，伪造行会让重试
//     任务对不存在的记录做结算；
//   - backfill_progress：success_cost_usd 一次性回填完成标记，由显式运维动作
//     写入。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// st 返回 juhe_stats schema 下的全限定表名（本文件所有 SQL 的唯一拼表口）。
func st(table string) string { return qualified(SchemaStats, table) }

// stats 域常量：kind 词表（DDL CHECK）、IP 登记分桶与样本上限、样本宿主账户。
const (
	// statsSnapshotKindCodex / RelayBalance / XAIGrok 是 account_usage_snapshots
	// 的三个 kind 样本（DDL CHECK 值域中的三档）。
	statsSnapshotKindCodex        = "openai_codex"
	statsSnapshotKindRelayBalance = "relay_balance"
	statsSnapshotKindXAIGrok      = "xai_grok"
	// statsClientIPRegistryBucketCount 与 jobs statsverify / gateway
	// gatewayclientip 的 ClientIPRegistryBucketCount 同值：bucket_no 由 ip_hash
	// 前 8 位十六进制取模。
	statsClientIPRegistryBucketCount = 4096
	// statsClientIPLimit 是 IP 登记 / 策略样本的 IP 数上限（页面样本够用）。
	statsClientIPLimit = 6
	// statsPolicyHitDays 是策略命中样本覆盖的最近天数上限。
	statsPolicyHitDays = 7
	// statsBalanceAccountID 是 relay_balance 快照的宿主（business 域开启了余额
	// 查询的多 Key 账户）。
	statsBalanceAccountID = CleanupIDPrefix + "acc_multikey"
	// statsXAIGrokHostAccountID 是 xai_grok 快照的宿主：business 域没有 xai
	// 账户，样本落在 OAuth 主力账户上（网关按账户供应商取 kind，gpt 账户不会
	// 读 grok 行；该行只用于快照表形状 / 页面词表验收）。
	statsXAIGrokHostAccountID = CleanupIDPrefix + "acc_oauth"
)

// statsBackgroundTaskSample 是一条后台任务运行样本。job_name / job_type /
// worker_role 逐字取自 jobs/internal/jobregistry 的注册条目（job_type 即
// Entry.Kind），status 覆盖 completed / failed / running / queued。
type statsBackgroundTaskSample struct {
	jobName    string
	jobType    string
	workerRole string
	status     string
	// attempt 是这次运行的尝试序号。background_task_runs 没有 attempt 列
	//（真实语义是「一次尝试一行」），因此它落在 params_json 里供页面 / 排查使用。
	attempt    int
	durationMs int
	errorText  string
	ageMinutes int
}

var statsBackgroundTaskSamples = []statsBackgroundTaskSample{
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
	{jobName: "chat-retention-cleanup", jobType: "maintenance", workerRole: "ingest-worker", status: "skipped", attempt: 1, durationMs: 300, ageMinutes: 180},
}

// statsOAuthAccount 是一条 juhe_business 里的 OAuth mock 账户（codex 快照宿主）。
type statsOAuthAccount struct {
	id        string
	owner     string
	updatedAt time.Time
}

// statsWriter 承载一次 stats 域写入：统一时钟、计数与从 usage 明细读到的 IP 集合。
type statsWriter struct {
	ctx       context.Context
	env       *env
	now       time.Time
	counts    map[string]int
	clientIPs []string
}

// seedStats 是 stats 域入口（domainRegistry 的 Run 指向它）。缺 stats schema
// （background_task_runs 表缺失）时整体跳过：Counts 为空 = 覆盖报告按未接线归属。
func seedStats(ctx context.Context, e *env) (DomainResult, error) {
	exists, err := tableExists(ctx, e.db, SchemaStats, "background_task_runs")
	if err != nil {
		return DomainResult{}, err
	}
	if !exists {
		e.logger.Info("mockdata stats 域跳过", "reason", "juhe_stats 缺少表 background_task_runs（schema 未 ensure）")
		return DomainResult{Name: DomainStats}, nil
	}
	w := &statsWriter{ctx: ctx, env: e, now: e.options.clock().UTC(), counts: map[string]int{}}
	clientIPs, err := w.loadClientIPs()
	if err != nil {
		return DomainResult{}, err
	}
	w.clientIPs = clientIPs
	if err := w.seed(); err != nil {
		return DomainResult{}, err
	}
	return DomainResult{Name: DomainStats, Counts: w.counts}, nil
}

// seed 在单事务里写入全部样本：半批数据不留在事务里。
func (w *statsWriter) seed() error {
	return w.env.tx(w.ctx, func(tx *sql.Tx) error {
		if err := w.seedBackgroundTaskRuns(tx); err != nil {
			return fmt.Errorf("后台任务运行样本: %w", err)
		}
		if err := w.seedAccountUsageSnapshots(tx); err != nil {
			return fmt.Errorf("账户用量快照样本: %w", err)
		}
		if err := w.seedClientIPSamples(tx); err != nil {
			return fmt.Errorf("IP 统计样本: %w", err)
		}
		if err := w.seedDirtyMarkers(tx); err != nil {
			return fmt.Errorf("派生窗口脏标记: %w", err)
		}
		return nil
	})
}

// put 写入一行并记账。insertMapTx 的 ON CONFLICT(主键) DO UPDATE + 确定性主键
// 让重复执行不报错也不叠加（cleanupAll 会先清理/重置，这里是第二道幂等保证）。
func (w *statsWriter) put(tx *sql.Tx, schemaTable string, columns map[string]any) error {
	if err := w.env.insertMapTx(w.ctx, tx, schemaTable, columns); err != nil {
		return err
	}
	_, table, err := splitQualified(schemaTable)
	if err != nil {
		return err
	}
	w.counts[table]++
	return nil
}

// putJSON 把值编码成 JSON 文本列；编码失败即错（造数不接受静默空 JSON）。
func (w *statsWriter) putJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// stamp 返回本次造数基准时间的毫秒 ISO 字符串（时间文本列口径）。
func (w *statsWriter) stamp() string {
	return w.now.UTC().Format(isoMillisLayout)
}

// at 返回基准时间偏移后的毫秒 ISO 字符串。
func (w *statsWriter) at(offset time.Duration) string {
	return w.now.Add(offset).UTC().Format(isoMillisLayout)
}

// ts 返回基准时间偏移后的 UTC 时刻（timestamptz 列口径）。
func (w *statsWriter) ts(offset time.Duration) time.Time {
	return w.now.Add(offset).UTC()
}

// dateKey 返回基准时间偏移后的 YYYY-MM-DD。
func (w *statsWriter) dateKey(offsetDays int) string {
	return w.now.AddDate(0, 0, offsetDays).UTC().Format("2006-01-02")
}

// seedBackgroundTaskRuns 写后台任务运行样本。文本时间列逐列对照
// background_task_runs 的 text DDL。
func (w *statsWriter) seedBackgroundTaskRuns(tx *sql.Tx) error {
	for index, sample := range statsBackgroundTaskSamples {
		submittedAt := w.ts(-time.Duration(sample.ageMinutes) * time.Minute)
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
			"params_json":  fmt.Sprintf(`{"seed":%q,"attempt":%d}`, CleanupIDPrefix+"stats", sample.attempt),
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
			resultJSON, err := w.putJSON(map[string]any{
				"seed": CleanupIDPrefix + "stats", "attempt": sample.attempt, "success": sample.status == "completed",
			})
			if err != nil {
				return err
			}
			row["result_json"] = resultJSON
		case "running":
			// running / queued 行必须留新（心跳与提交时间贴近 now）：jobs 的
			// background-task-run-reconcile 会把心跳过期的 running 行改判 failed，
			// 那样页面样本会在造数后立刻变样。
			row["started_at"] = submittedText
			row["heartbeat_at"] = w.at(-30 * time.Second)
			row["updated_at"] = row["heartbeat_at"]
		default: // queued
			row["updated_at"] = submittedText
		}
		if err := w.put(tx, st("background_task_runs"), row); err != nil {
			return err
		}
	}
	return nil
}

// seedAccountUsageSnapshots 写三个 kind 的账户用量快照样本（timestamptz 列写
// time.Time）。业务库缺 accounts 表时跳过该样本并留痕（business 域未执行的
// 数据根上 stats 域仍写 background_task_runs 与脏标记）。
func (w *statsWriter) seedAccountUsageSnapshots(tx *sql.Tx) error {
	accountsTableExists, err := tableExists(w.ctx, w.env.db, SchemaBusiness, "accounts")
	if err != nil {
		return err
	}
	if !accountsTableExists {
		w.env.logger.Info("mockdata stats 域跳过账户用量快照", "reason", "juhe_business 缺少表 accounts")
		return nil
	}
	oauthAccounts, err := w.oauthAccounts()
	if err != nil {
		return err
	}
	for index, account := range oauthAccounts {
		if err := w.putCodexSnapshot(tx, account, index); err != nil {
			return err
		}
	}
	// relay_balance / xai_grok 样本宿主缺失时留痕跳过（业务 seed 漂移会先由
	// business 域的覆盖断言暴露）。
	if err := w.putRelayBalanceSnapshot(tx); err != nil {
		return err
	}
	return w.putXAIGrokSnapshot(tx)
}

// putCodexSnapshot 给一个 OAuth mock 账户写一条 kind='openai_codex' 的 5h / 7d
// 用量快照（键名对齐 gateway BuildOpenAICodexUsageSnapshotPayload 的归一化键，
// refresh_status 对齐 UpsertAccountUsageSnapshots 的 'fresh'）。
func (w *statsWriter) putCodexSnapshot(tx *sql.Tx, account statsOAuthAccount, index int) error {
	updatedAt := account.updatedAt
	fiveHourReset := updatedAt.Add(2 * time.Hour).Add(time.Duration(index) * 37 * time.Minute)
	sevenDayReset := updatedAt.Add(3 * 24 * time.Hour).Add(time.Duration(index) * 5 * time.Hour)
	payload, err := w.putJSON(map[string]any{
		// 键名与读面 OAuthUsageWindowFromSnapshot 的 codex_<窗口>_* 前缀一致。
		"codex_usage_updated_at":       updatedAt.Format(isoMillisLayout),
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
	})
	if err != nil {
		return err
	}
	return w.put(tx, st("account_usage_snapshots"), map[string]any{
		"system_account_id":  account.owner,
		"account_id":         account.id,
		"kind":               statsSnapshotKindCodex,
		"source":             "codex",
		"snapshot_json":      payload,
		"refresh_status":     "fresh",
		"last_attempt_at":    updatedAt,
		"last_success_at":    updatedAt,
		"next_refresh_after": updatedAt.Add(5 * time.Minute),
		"last_error_message": nil,
		"updated_at":         updatedAt,
		"created_at":         updatedAt,
	})
}

// putRelayBalanceSnapshot 给多 Key 余额账户写一条 kind='relay_balance' 的余额
// 快照（键名对齐 jobs balanceSnapshotPersist / shared accountbalance.Snapshot
// 的 camelCase 词表；inputDigest 是 mock 占位，网关读端按现算摘要比对不匹配时
// 等下一轮 J2 刷新自愈）。
func (w *statsWriter) putRelayBalanceSnapshot(tx *sql.Tx) error {
	account, ok, err := w.mockAccount(statsBalanceAccountID)
	if err != nil {
		return err
	}
	if !ok {
		w.env.logger.Info("mockdata stats 域跳过 relay_balance 快照：business 域没有宿主账户", "account", statsBalanceAccountID)
		return nil
	}
	lastAttempt := w.at(-2 * time.Minute)
	keyBalance := func(index int) map[string]any {
		key := fmt.Sprintf("sk-mockdata-admin-multi-key-pool-%d-%s", index, "mmmmmmmmmmmmmmmmmmmm")
		return map[string]any{
			"keyFingerprint": secretHash(key),
			"maskedKey":      maskSecret(key),
			"status":         "fresh",
			"remainingUsd":   fmt.Sprintf("%.4f", 64.21-float64(index)*12.5),
			"rawUnit":        "usd",
			"scope":          "account",
			"basis":          "wallet",
		}
	}
	payload, err := w.putJSON(map[string]any{
		"status":          "fresh",
		"configRevision":  1,
		"inputDigest":     CleanupIDPrefix + "relay-balance-input-digest",
		"remainingUsd":    "115.9200",
		"rawRemaining":    "115.92",
		"rawUnit":         "usd",
		"basis":           "wallet",
		"lastAttemptAt":   lastAttempt,
		"lastSuccessAt":   lastAttempt,
		"keyCount":        4,
		"queriedKeyCount": 2,
		"scope":           "account",
		"aggregation":     "sum",
		"keyBalances":     []any{keyBalance(1), keyBalance(2)},
	})
	if err != nil {
		return err
	}
	return w.put(tx, st("account_usage_snapshots"), map[string]any{
		"system_account_id":  account.owner,
		"account_id":         account.id,
		"kind":               statsSnapshotKindRelayBalance,
		"source":             "upstream_api",
		"snapshot_json":      payload,
		"refresh_status":     "fresh",
		"last_attempt_at":    w.ts(-2 * time.Minute),
		"last_success_at":    w.ts(-2 * time.Minute),
		"next_refresh_after": w.ts(5 * time.Minute),
		"last_error_message": nil,
		"updated_at":         w.ts(-2 * time.Minute),
		"created_at":         w.ts(-2 * time.Minute),
	})
}

// putXAIGrokSnapshot 写一条 kind='xai_grok' 的 Grok 用量快照形状样本（键名对齐
// jobs xaiGrokSnapshotPayload，source/refresh_status 对齐 upsertSuccess）。
func (w *statsWriter) putXAIGrokSnapshot(tx *sql.Tx) error {
	account, ok, err := w.mockAccount(statsXAIGrokHostAccountID)
	if err != nil {
		return err
	}
	if !ok {
		w.env.logger.Info("mockdata stats 域跳过 xai_grok 快照：business 域没有宿主账户", "account", statsXAIGrokHostAccountID)
		return nil
	}
	payload, err := w.putJSON(map[string]any{
		"grok_credit_used_percent":    37.5,
		"grok_period_type":            "monthly",
		"grok_period_start":           w.at(-10 * 24 * time.Hour),
		"grok_period_end":             w.at(20 * 24 * time.Hour),
		"grok_subscription_tier":      "SuperGrok",
		"grok_product_usage_json":     `{"grok_code":{"creditsUsed":120,"creditsLimit":1000}}`,
		"grok_on_demand_used_percent": 12.5,
		"seed":                        CleanupIDPrefix + "stats",
	})
	if err != nil {
		return err
	}
	return w.put(tx, st("account_usage_snapshots"), map[string]any{
		"system_account_id":  account.owner,
		"account_id":         account.id,
		"kind":               statsSnapshotKindXAIGrok,
		"source":             "xai_grok_billing",
		"snapshot_json":      payload,
		"refresh_status":     "ok",
		"last_attempt_at":    w.ts(-3 * time.Minute),
		"last_success_at":    w.ts(-3 * time.Minute),
		"next_refresh_after": w.ts(5 * time.Minute),
		"last_error_message": nil,
		"updated_at":         w.ts(-3 * time.Minute),
		"created_at":         w.ts(-3 * time.Minute),
	})
}

// seedClientIPSamples 写 IP 登记、封禁 / 放行策略与命中样本。IP 取值来自 usage
// 域明细的真实 client_ip（镜像为空时用同网段固定兜底）；ip_hash 算法与 gateway
// gatewayclientip 的 clientIpIdentity 同源——策略读面按 ip_hash 关联 registry，
// 哈希算法不一致会让策略行直接不可见。
func (w *statsWriter) seedClientIPSamples(tx *sql.Tx) error {
	nowText := w.stamp()
	firstSeenText := w.at(-time.Duration(w.env.options.Days) * 24 * time.Hour)
	lastSeenText := w.at(-5 * time.Minute)
	hashByIP := map[string]string{}
	for _, clientIP := range w.clientIPs {
		ipHash, bucketNo := statsClientIPIdentity(clientIP)
		hashByIP[clientIP] = ipHash
		if err := w.put(tx, st("client_ip_registry"), map[string]any{
			"ip_hash":          ipHash,
			"bucket_no":        bucketNo,
			"aggregate_ip_key": clientIP,
			"client_ip":        clientIP,
			"ip_version":       4,
			"first_seen_at":    firstSeenText,
			"last_seen_at":     lastSeenText,
			"created_at":       nowText,
			"updated_at":       nowText,
		}); err != nil {
			return err
		}
	}
	owner := CleanupIDPrefix + "admin"
	if owners, err := w.mockOwnerSystemAccountIDs(); err != nil {
		return err
	} else if len(owners) > 0 {
		owner = owners[0]
	}
	policies := w.clientIPPolicies(hashByIP)
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
		if err := w.put(tx, st("client_ip_policies"), row); err != nil {
			return err
		}
	}
	// 命中样本只给启用中的黑名单策略：命中计数是运行时缓冲写入的派生计数，
	// 这里只提供页面样本行（同样的 (ip_hash, stat_date, policy_id) 主键，重复
	// 执行覆盖写）。
	for _, policy := range policies {
		if policy.policyType != "blacklist" || policy.status != "active" {
			continue
		}
		days := w.env.options.Days
		if days > statsPolicyHitDays {
			days = statsPolicyHitDays
		}
		for dayIndex := 0; dayIndex < days; dayIndex++ {
			if err := w.put(tx, st("client_ip_policy_hits"), map[string]any{
				"ip_hash":     policy.ipHash,
				"stat_date":   w.dateKey(-dayIndex),
				"policy_id":   policy.id,
				"hit_count":   1 + (dayIndex*7)%23,
				"last_hit_at": w.at(-time.Duration(dayIndex*3+1) * time.Hour),
				"updated_at":  nowText,
			}); err != nil {
				return err
			}
		}
	}
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

// seedDirtyMarkers 写派生缓存脏标记：juhe_business 的分组统计脏行 + juhe_stats
// 的概览 / AI 性能摘要脏范围（含 global 作用域）。脏标记是既有 jobs 任务的正常
// 输入（消费后自行重建并删除标记），它让「造数刚重写了明细」这件事被运行时
// 看见；直接写派生表才是伪造口径。
func (w *statsWriter) seedDirtyMarkers(tx *sql.Tx) error {
	updatedAt := w.stamp()
	businessReady, err := tableExists(w.ctx, w.env.db, SchemaBusiness, "groups")
	if err != nil {
		return err
	}
	if businessReady {
		groups, err := w.mockBusinessIDs(SchemaBusiness, "groups")
		if err != nil {
			return err
		}
		for _, groupID := range groups {
			if err := w.put(tx, qualified(SchemaBusiness, "group_account_stats_dirty"), map[string]any{
				"group_id": groupID,
				// reason 是运维可读的自由文本；语义：本分组的分组统计需要重建，
				// 因为造数重写了它的用量明细。
				"reason":     CleanupIDPrefix + "usage_seed",
				"updated_at": updatedAt,
			}); err != nil {
				return err
			}
		}
	} else {
		w.env.logger.Info("mockdata stats 域跳过分组统计脏标记", "reason", "juhe_business 缺少表 groups")
	}

	owners, err := w.mockOwnerSystemAccountIDs()
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		w.env.logger.Info("mockdata stats 域跳过派生窗口脏范围", "reason", "juhe_business 没有 mock 账户")
		return nil
	}
	minDate := w.dateKey(-(w.env.options.Days - 1))
	maxDate := w.dateKey(0)

	// global 作用域与线上一致（每条用量明细都会累进全局概览窗口；
	// statsagg 的 GlobalStatsSystemAccountID 即 'global'）。
	scopes := append(append([]string{}, owners...), "global")
	for _, scope := range scopes {
		if err := w.put(tx, st("usage_overview_dirty_scopes"), map[string]any{
			"system_account_id": scope,
			"scope_id":          scope,
			"min_changed_date":  minDate,
			"generation":        1,
			"first_dirty_at":    updatedAt,
			"updated_at":        updatedAt,
		}); err != nil {
			return err
		}
	}
	for _, owner := range owners {
		if err := w.put(tx, st("ai_performance_summary_dirty_system_accounts"), map[string]any{
			"system_account_id": owner,
			"min_stat_date":     minDate,
			"max_stat_date":     maxDate,
			"generation":        1,
			"first_dirty_at":    updatedAt,
			"updated_at":        updatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// loadClientIPs 从 juhe_usage.usage_records 读真实 client_ip；明细表缺失
// （usage 域未接线的数据根）时用同网段固定兜底样本；查询失败是运行错误，随域
// 整体失败而不是静默换样本。
func (w *statsWriter) loadClientIPs() ([]string, error) {
	recordsReady, err := tableExists(w.ctx, w.env.db, SchemaUsage, "usage_records")
	if err != nil {
		return nil, err
	}
	if !recordsReady {
		return append([]string{}, statsFallbackClientIPs...), nil
	}
	rows, err := w.env.db.QueryContext(w.ctx,
		`SELECT DISTINCT client_ip FROM `+ut("usage_records")+`
		 WHERE client_ip IS NOT NULL AND client_ip <> '' ORDER BY client_ip LIMIT $1`, statsClientIPLimit)
	if err != nil {
		return nil, fmt.Errorf("读取使用记录 client_ip: %w", err)
	}
	defer rows.Close()
	var ips []string
	for rows.Next() {
		var clientIP sql.NullString
		if err := rows.Scan(&clientIP); err != nil {
			return nil, err
		}
		if clientIP.String != "" {
			ips = append(ips, clientIP.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return append([]string{}, statsFallbackClientIPs...), nil
	}
	return ips, nil
}

// statsFallbackClientIPs 是 usage 明细为空时的 IP 样本兜底：取值落在使用记录的
// 10.10.x.x / 10.20.x.x 网段内，保证 IP 页面样本与用量记录的客户端 IP 家族一致。
var statsFallbackClientIPs = []string{"10.10.0.1", "10.10.0.2", "10.20.0.1"}

// oauthAccounts 读取 juhe_business 的 OAuth mock 账户；快照时间按账户错开，
// 页面上的「最近刷新」不是同一时刻。
func (w *statsWriter) oauthAccounts() ([]statsOAuthAccount, error) {
	rows, err := w.env.db.QueryContext(w.ctx,
		`SELECT id, system_account_id FROM `+bt("accounts")+`
		 WHERE id LIKE $1 AND type = 'oauth' ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("读取 OAuth mock 账户: %w", err)
	}
	defer rows.Close()
	var accounts []statsOAuthAccount
	for rows.Next() {
		var account statsOAuthAccount
		if err := rows.Scan(&account.id, &account.owner); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range accounts {
		accounts[index].updatedAt = w.ts(-time.Duration(3+index*4) * time.Minute)
	}
	return accounts, nil
}

// statsMockAccount 是 mock 账户的最小读投影（ID + 归属）。
type statsMockAccount struct {
	id    string
	owner string
}

// mockAccount 按 ID 读取一条 mock 账户；不存在返回 ok=false。
func (w *statsWriter) mockAccount(id string) (statsMockAccount, bool, error) {
	row := w.env.db.QueryRowContext(w.ctx,
		`SELECT id, system_account_id FROM `+bt("accounts")+" WHERE id = $1 AND id LIKE $2", id, CleanupIDPrefix+"%")
	var account statsMockAccount
	if err := row.Scan(&account.id, &account.owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return statsMockAccount{}, false, nil
		}
		return statsMockAccount{}, false, err
	}
	return account, true, nil
}

// mockOwnerSystemAccountIDs 返回 mock 账户归属的系统账户 ID（去重，稳定序）。
func (w *statsWriter) mockOwnerSystemAccountIDs() ([]string, error) {
	accountsReady, err := tableExists(w.ctx, w.env.db, SchemaBusiness, "accounts")
	if err != nil || !accountsReady {
		return nil, err
	}
	rows, err := w.env.db.QueryContext(w.ctx,
		`SELECT DISTINCT system_account_id FROM `+bt("accounts")+`
		 WHERE id LIKE $1 AND system_account_id <> '' ORDER BY system_account_id`, CleanupIDPrefix+"%")
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

// mockBusinessIDs 读取一个 business schema 表里带清理标识的 ID 列表。
func (w *statsWriter) mockBusinessIDs(schema, table string) ([]string, error) {
	rows, err := w.env.db.QueryContext(w.ctx,
		`SELECT id FROM `+quoteQualified(qualified(schema, table))+" WHERE id LIKE $1 ORDER BY id", CleanupIDPrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("读取 mock %s.%s: %w", schema, table, err)
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

// statsClientIPIdentity 复刻 gateway gatewayclientip 的 clientIpIdentity：
// ipHash = sha256("client-ip:" + aggregateIpKey)，
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
