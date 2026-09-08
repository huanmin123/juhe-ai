package jobregistry

import (
	"strings"
	"testing"
	"time"
)

// nodeScheduledJobNames 是 Node background-job-registry.entries.ts 的
// backgroundScheduledJobs 全量名单（31 项，顺序一致）。注册表覆盖测试保证
// Go 侧登记不缺项：缺失即测试失败，不允许静默跳过。
var nodeScheduledJobNames = []string{
	"system-metrics-sample",
	"system-metrics-trend-windows-refresh",
	"usage-stats-aggregation",
	"usage-hot-window-refresh",
	"client-ip-stats-aggregation",
	"group-account-stats-refresh",
	"usage-rank-snapshots-refresh",
	"ai-performance-summary-windows-refresh",
	"usage-overview-windows-refresh",
	"usage-scope-range-windows-refresh",
	"authorization-usage-range-windows-refresh",
	"usage-stats-consistency-check",
	"background-task-run-reconcile",
	"api-key-record-cleanup-retry",
	"account-record-cleanup-retry",
	"api-key-availability-schedule-status-sync",
	"account-availability-schedule-status-sync",
	"resource-authorization-expiry-sweep",
	"account-quality-refresh",
	"account-balance-refresh",
	"account-balance-auto-detect-recovery",
	"openai-oauth-access-token-refresh",
	"account-api-key-cooldown-retest",
	"normal-route-speed-first-recovery-probe",
	"account-circuit-control-plane-maintenance",
	"account-list-availability-projection-maintenance",
	"account-circuit-recovery",
	"key-model-memory-recovery",
	"data-retention-cleanup",
	"chat-retention-cleanup",
	"expired-deleted-account-cleanup",
}

// goAddedScheduledJobNames 是 Go 侧在 Node 31 项之外新增接线的 scheduled
// 任务。配额小时窗刷新在 Node 不走 backgroundScheduledJobs 注册表（SQLite
// 由 stats-writer 聚合后内联调用、PG 由 worker 后台循环驱动 refresh...Async），
// BUG-0175 D-48 修复把它显式登记为 Go 调度任务；除名单外不允许任何其他
// Go 附加条目。
var goAddedScheduledJobNames = []string{
	"usage-quota-hourly-windows-refresh",
}

// expectedScheduledOrder 合并 Node 名单与 Go 附加任务：附加任务插在
// authorization-usage-range-windows-refresh 之后，与 ScheduledEntries 的登记
// 位置一致。
func expectedScheduledOrder() []string {
	result := make([]string, 0, len(nodeScheduledJobNames)+len(goAddedScheduledJobNames))
	for _, name := range nodeScheduledJobNames {
		result = append(result, name)
		if name == "authorization-usage-range-windows-refresh" {
			result = append(result, goAddedScheduledJobNames...)
		}
	}
	return result
}

func TestScheduledRegistryCoversAllNodeJobs(t *testing.T) {
	entries := ScheduledEntries()
	want := expectedScheduledOrder()
	if len(entries) != len(want) {
		t.Fatalf("scheduled entries=%d want=%d（Node %d 项 + Go 附加 %d 项）",
			len(entries), len(want), len(nodeScheduledJobNames), len(goAddedScheduledJobNames))
	}
	for index, name := range want {
		if entries[index].JobName != name {
			t.Fatalf("entry %d = %s want %s", index, entries[index].JobName, name)
		}
	}
}

func TestEveryScheduledEntryHasScheduleAndBinding(t *testing.T) {
	for _, entry := range ScheduledEntries() {
		if entry.GoStatus == "" || entry.GoBinding == "" {
			t.Fatalf("%s 缺少 Go 绑定状态或说明", entry.JobName)
		}
		schedule, ok := ScheduleFor(entry.JobName)
		if !ok {
			t.Fatalf("%s 缺少调度参数", entry.JobName)
		}
		if schedule.Interval <= 0 {
			t.Fatalf("%s 调度间隔非法", entry.JobName)
		}
		if entry.LeaseRequired && entry.GoStatus == GoWired && schedule.LeaseTTL <= 0 && !usesOwnLeaseOrLeaseFree(entry.JobName) {
			t.Fatalf("%s 需要 lease 但未登记 lease TTL", entry.JobName)
		}
	}
}

func usesOwnLeaseOrLeaseFree(jobName string) bool {
	// usage-hot-window-refresh 在 Node 也不包租约（runScheduledUsageHotWindowRefresh）；
	// account-balance-refresh/key-model-memory-recovery 由等价组件自有语义接管。
	// account-balance-auto-detect-recovery 在 Node 调度时不包 runWithPostgresScheduledLease，
	// 逐候选使用自有 runWithAccountBalanceLease（background-jobs.ts:327 + account-balance-auto-detect.service.ts:118）。
	// account-api-key-cooldown-retest / normal-route-speed-first-recovery-probe 同样不包
	// （background-jobs.ts:331-332），前者内部为 claim CAS（10min lease），后者为 Redis mutation/probe-claim 锁。
	// account-circuit-control-plane-maintenance / account-circuit-recovery 同样不包
	// （background-jobs.ts:333/365）：并发安全由账户电路 Redis Lua 状态机
	// （单键转移原子性 + replayOrder 幂等）与 incident outbox claim CAS 承担。
	switch jobName {
	case "usage-hot-window-refresh", "account-balance-refresh", "key-model-memory-recovery",
		"account-balance-auto-detect-recovery", "account-api-key-cooldown-retest",
		"normal-route-speed-first-recovery-probe",
		"account-circuit-control-plane-maintenance", "account-circuit-recovery":
		return true
	}
	return false
}

func TestQueueEntriesRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, entry := range QueueEntries() {
		if names[entry.JobName] {
			t.Fatalf("queue entry 重复：%s", entry.JobName)
		}
		names[entry.JobName] = true
	}
	for _, required := range []string{
		"background_worker_usage_records",
		"manual-account-test-queue",
		"account-api-key-cooldown-retest-queue",
		"account-quality-failure-precheck-queue",
		"record-maintenance:api_key_related_cleanup",
		"record-maintenance:usage_records_cleanup",
	} {
		if !names[required] {
			t.Fatalf("queue entry 缺失：%s", required)
		}
	}
}

func TestFindCoversBothCategories(t *testing.T) {
	for _, name := range []string{"usage-stats-aggregation", "background_worker_usage_records", "data-retention-cleanup"} {
		if _, ok := Find(name); !ok {
			t.Fatalf("Find(%s) 未命中", name)
		}
	}
	if _, ok := Find("not-a-job"); ok {
		t.Fatal("Find 不应命中未知任务")
	}
}

func TestResolveScheduleAppliesSettingsOverride(t *testing.T) {
	schedule, ok := ResolveSchedule("client-ip-stats-aggregation", func(jobName string) (time.Duration, bool) {
		if jobName == "client-ip-stats-aggregation" {
			return 5 * time.Second, true
		}
		return 0, false
	})
	if !ok || schedule.Interval != 5*time.Second {
		t.Fatalf("settings override 未生效: %+v", schedule)
	}
	fallback, ok := ResolveSchedule("client-ip-stats-aggregation", nil)
	if !ok || fallback.Interval != StatsAggregationInterval {
		t.Fatalf("默认间隔未生效: %+v", fallback)
	}
}

func TestNoSilentMissingEntries(t *testing.T) {
	for _, entry := range AllEntries() {
		switch entry.GoStatus {
		case GoWired, GoEquivalent, GoPartial, GoOwnedElsewhere, GoEliminatedByDesign, NodeOnly:
		default:
			t.Fatalf("%s 的 GoStatus 非法：%s", entry.JobName, entry.GoStatus)
		}
		if entry.GoStatus == NodeOnly && !strings.Contains(entry.GoBinding, "登记缺失") {
			t.Fatalf("%s 是 node-only，必须在说明中显式登记缺失", entry.JobName)
		}
	}
}
