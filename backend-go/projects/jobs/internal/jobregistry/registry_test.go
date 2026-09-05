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

func TestScheduledRegistryCoversAllNodeJobs(t *testing.T) {
	entries := ScheduledEntries()
	if len(entries) != len(nodeScheduledJobNames) {
		t.Fatalf("scheduled entries=%d want=%d", len(entries), len(nodeScheduledJobNames))
	}
	for index, name := range nodeScheduledJobNames {
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
	switch jobName {
	case "usage-hot-window-refresh", "account-balance-refresh", "key-model-memory-recovery":
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
