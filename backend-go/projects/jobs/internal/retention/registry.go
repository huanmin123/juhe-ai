package retention

import "time"

// Registry metadata mirrors the Node background job registry entries and
// scheduler wiring for the retention family (background-job-registry
// entries.ts + background-jobs.ts scheduleUsageIngestJobs / ops-worker
// block). A composition root uses these to reproduce the Node scheduling
// position; the runners themselves stay independent of it.
const (
	DataRetentionScheduleInterval     = CleanupIntervalMinutes * time.Minute
	DataRetentionScheduleInitialDelay = 450 * time.Second
	DataRetentionScheduleTimeout      = 5 * time.Minute
	DataRetentionLeaseTTL             = 10 * time.Minute

	ChatRetentionScheduleInterval     = ChatRetentionInterval
	ChatRetentionScheduleInitialDelay = ChatRetentionInitialDelay

	ExpiredAccountScheduleInterval     = ExpiredDeletedAccountInterval
	ExpiredAccountScheduleInitialDelay = ExpiredDeletedAccountInitialDelay
)

// RegistryEntry describes one retention job the way the Node registry does.
type RegistryEntry struct {
	JobName     string
	Category    string
	Kind        string
	DefaultRole string
	Interval    time.Duration
	LeaseTTL    time.Duration
	Writes      []string
}

// RegistryEntries returns the retention family in Node registry order.
func RegistryEntries() []RegistryEntry {
	return []RegistryEntry{
		{
			JobName:     "data-retention-cleanup",
			Category:    "scheduled",
			Kind:        "maintenance",
			DefaultRole: "ingest-worker",
			Interval:    DataRetentionScheduleInterval,
			LeaseTTL:    DataRetentionLeaseTTL,
			Writes:      []string{"dataset:*", "stats:*", "usage-shards:usage_records"},
		},
		{
			JobName:     "chat-retention-cleanup",
			Category:    "scheduled",
			Kind:        "maintenance",
			DefaultRole: "ops-worker",
			Interval:    ChatRetentionScheduleInterval,
			LeaseTTL:    ChatRetentionLeaseTTL,
			Writes:      []string{"chat:*"},
		},
		{
			JobName:     "expired-deleted-account-cleanup",
			Category:    "scheduled",
			Kind:        "maintenance",
			DefaultRole: "ops-worker",
			Interval:    ExpiredAccountScheduleInterval,
			LeaseTTL:    ExpiredDeletedAccountLeaseTTL,
			Writes:      []string{"business:accounts", "business:resource_authorizations"},
		},
		{
			JobName:     "api-key-record-cleanup-retry",
			Category:    "scheduled",
			Kind:        "maintenance",
			DefaultRole: "ingest-worker",
			Interval:    RecordCleanupRetryInterval,
			LeaseTTL:    RecordCleanupRetryLeaseTTL,
			Writes:      []string{"dataset:api_key_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
		},
		{
			JobName:     "account-record-cleanup-retry",
			Category:    "scheduled",
			Kind:        "maintenance",
			DefaultRole: "ingest-worker",
			Interval:    RecordCleanupRetryInterval,
			LeaseTTL:    RecordCleanupRetryLeaseTTL,
			Writes:      []string{"dataset:account_record_cleanup_targets", "stats:usage_record_cleanup_deductions", "usage-shards:usage_records"},
		},
	}
}
