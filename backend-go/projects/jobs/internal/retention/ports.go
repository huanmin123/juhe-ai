package retention

import (
	"context"
	"time"
)

// Mode mirrors runtimeConfig.databaseDriver for the retention family. The
// data-retention job is dual-mode: sqlite runs the single-node cleanup chain
// in-process, postgres only dispatches record-maintenance maintenance jobs
// and runs the PostgreSQL cleanup stages directly.
type Mode string

const (
	ModeSQLite   Mode = "sqlite"
	ModePostgres Mode = "postgres"
)

// Clock injects the wall clock (Node Date.now()).
type Clock func() time.Time

// Sleeper pauses between full batches (pauseBetweenCleanupBatches). The real
// implementation sleeps CleanupBatchPause and returns ctx.Err() when the
// caller was aborted while waiting.
type Sleeper func(ctx context.Context) error

// SettingsSource mirrors getSettings()/getSettingsAsync().
type SettingsSource func(ctx context.Context) (map[string]any, error)

// TimezoneSource mirrors usageStatsTimezone()/usageStatsTimezoneAsync(): it
// returns the normalized usageStatsTimezone setting or an error
// (系统设置缺少 usageStatsTimezone / 统计时区不存在：...) when unavailable.
type TimezoneSource func(ctx context.Context) (string, error)

// PublicApiLogsCleaner mirrors the migrated gateway
// projects/gateway/internal/publicapilogs/retention.go CleanupStore port with
// the identical signature and semantics: delete public_api_logs rows with
// created_at strictly before cutoffCreatedAt, oldest first, at most limit
// rows, return the deleted row count. The two modules never import each
// other; a composition root supplies the adapter.
type PublicApiLogsCleaner interface {
	CleanupBefore(ctx context.Context, cutoffCreatedAt string, limit int) (int64, error)
}

// UsageRecordsBatch mirrors ProcessedUsageRecordsCleanupBatchResult.
type UsageRecordsBatch struct {
	CutoffCreatedAt       string
	SafetyCursorCreatedAt string
	SafetyCursorID        string
	DeletedRows           int64
	DroppedPartitions     int64
	HasMore               bool
	BlockedReason         string
}

// UsageRecordsCleaner mirrors cleanupProcessedUsageRecordsBeforeWithResultAsync:
// delete processed usage_records shard rows with created_at strictly before
// cutoffCreatedAt, guarded by the stats safety cursor.
type UsageRecordsCleaner interface {
	CleanupProcessedBefore(ctx context.Context, cutoffCreatedAt string, limit int) (UsageRecordsBatch, error)
}

// UsageStatsRetentionInput mirrors usageStatsRetentionInput: the cutoff keys
// are computed on the jobs side (business timezone) and passed to the stats
// writer unchanged.
type UsageStatsRetentionInput struct {
	AccountQualityMinuteCutoffMinute string
	MinuteCutoffMinute               string
	HourlyCutoffHour                 string
	DailyCutoffDate                  string
	WeeklyCutoffWeek                 string
	MonthlyCutoffMonth               string
	RankSnapshotCutoffIso            string
	WindowCutoffDate                 string
	WindowCutoffIso                  string
	Limit                            int
}

// SystemMetricsRetentionInput mirrors systemMetricsRetentionInput.
type SystemMetricsRetentionInput struct {
	SamplesCutoffIso      string
	HourlyCutoffHour      string
	TrendWindowCutoffDate string
	Limit                 int
}

// AccountUsageSnapshotUpsertInput mirrors AccountUsageSnapshotUpsertInput.
type AccountUsageSnapshotUpsertInput struct {
	AccountID string
	Kind      string
	Source    string
	Snapshot  map[string]any
	UpdatedAt string
}

// StatsWriter mirrors the background stats-writer operations used by the
// retention family.
type StatsWriter interface {
	CleanupUsageStatsRetention(ctx context.Context, input UsageStatsRetentionInput) (UsageStatsRetentionCounts, error)
	CleanupSystemMetricsRetention(ctx context.Context, input SystemMetricsRetentionInput) (SystemMetricsRetentionCounts, error)
	CleanupNonBusinessStatsData(ctx context.Context, cutoffAt string, limit int) (NonBusinessDataCleanupCounts, error)
	CleanupDeletedApiKeyRecordStats(ctx context.Context, input DeletedApiKeyRecordStatsCleanupInput) error
	CleanupDeletedAccountRecordStats(ctx context.Context, input DeletedAccountRecordStatsCleanupInput) error
	UpsertAccountUsageSnapshots(ctx context.Context, inputs []AccountUsageSnapshotUpsertInput) error
}

// ScheduledLeaseFence mirrors ScheduledJobLeaseFence.
type ScheduledLeaseFence struct {
	LeaseKey     string
	OwnerID      string
	FencingToken int64
}

// ChatRetentionInput mirrors the cleanup_chat_retention DB service payload.
type ChatRetentionInput struct {
	Now               string
	InterruptedBefore string
	Limit             int
	RetentionDays     int
	ScheduledLease    *ScheduledLeaseFence
	// Priority mirrors the low-priority IPC lane; the port transport decides
	// how to honor it.
	Priority string
}

// ChatRetentionResult mirrors ChatRetentionCleanupResult.
type ChatRetentionResult struct {
	DroppedPartitions    int64
	DeletedMessages      int64
	DeletedConversations int64
	RecoveredTurns       int64
	RecoveredCompactions int64
	ClaimedAssets        int64
	DeletedAssets        int64
	FailedAssets         int64
	HasMoreAssets        bool
	DeletedCheckpoints   int64
	HasMoreCheckpoints   bool
	HasMore              bool
}

// ShouldLogChatRetention mirrors the Node condition that decides whether the
// completion info log is emitted.
func (r ChatRetentionResult) ShouldLog() bool {
	return r.DroppedPartitions > 0 || r.DeletedMessages > 0 || r.DeletedConversations > 0 ||
		r.RecoveredTurns > 0 || r.RecoveredCompactions > 0 || r.DeletedCheckpoints > 0 ||
		r.ClaimedAssets > 0 || r.FailedAssets > 0
}

// CodexContextExpiredCleanup mirrors CodexContextExpiredStateCleanupResult.
type CodexContextExpiredCleanup struct {
	DeletedSessions  int64
	DeletedResponses int64
	DeletedCompacts  int64
	StorageKeys      []string
	HasMore          bool
}

// CodexContextCleanupFailure mirrors CodexContextStorageCleanupFailure.
type CodexContextCleanupFailure struct {
	StorageKey string
	Error      string
}

// CodexContextSettlement mirrors CodexContextStorageCleanupSettlement.
type CodexContextSettlement struct {
	SucceededStorageKeys []string
	Failures             []CodexContextCleanupFailure
	Now                  string
}

// CodexContextSettlementResult mirrors CodexContextStorageCleanupSettlementResult.
type CodexContextSettlementResult struct {
	Acknowledged int64
	Deferred     int64
}

// ExpiredDeletedAccountTarget mirrors DeletedAccountRecordCleanupTarget.
type ExpiredDeletedAccountTarget struct {
	AccountID         string
	SystemAccountID   string
	RelatedAccountIDs []string
	AuthorizationIDs  []string
	TeamScopeIDs      []string
}

// ExpiredDeletedAccountSummary mirrors ExpiredDeletedAccountCleanupResult
// (the fields the jobs side consumes for logging and enqueue decisions).
type ExpiredDeletedAccountSummary struct {
	CutoffDeletedAt                 string
	OrphanedAuthorizationInstances  int64
	Attempted                       int64
	Completed                       int64
	Deferred                        int64
	Failed                          int64
	DeletedRows                     int64
	PhysicallyDeletedAccounts       int64
	PhysicallyDeletedAuthorizations int64
	PhysicallyDeletedGrants         int64
	PhysicallyDeletedGroupBindings  int64
	RecordCleanupTargets            []ExpiredDeletedAccountTarget
}

// DbService mirrors the background worker DB service operations used by the
// retention family. Methods returning pointers model the Node "undefined
// result" path: a nil result without error is a transport-level miss and
// keeps its dedicated error messages at the call sites.
type DbService interface {
	CleanupChatRetention(ctx context.Context, input ChatRetentionInput) (*ChatRetentionResult, error)
	CleanupExpiredSystemSessions(ctx context.Context, expiredBefore string, limit int) (int64, error)
	CleanupExpiredCodexContextStates(ctx context.Context, expiredBefore string, limit int) (*CodexContextExpiredCleanup, error)
	SettleCodexContextStorageCleanup(ctx context.Context, settlement CodexContextSettlement) (CodexContextSettlementResult, error)
	CleanupExpiredDeletedAccounts(ctx context.Context) (*ExpiredDeletedAccountSummary, error)
}

// EnqueueResult mirrors RecordMaintenanceEnqueueResult.
type EnqueueResult struct {
	Queued        bool
	DroppedReason string
}

// RecordMaintenanceEnqueuer mirrors enqueueRecordMaintenanceJobAsync (async
// dispatch) and enqueueRecordMaintenanceJobWithResult (sync dispatch with a
// queue/drop decision). droppedReason values are the Node strings
// (worker_local_queue_full, worker_dispatch_failed, worker_ipc_unavailable,
// redis_stream_async_required, redis_stream_enqueue_failed).
type RecordMaintenanceEnqueuer interface {
	Enqueue(ctx context.Context, job RecordMaintenanceJob) EnqueueResult
	EnqueueAsync(ctx context.Context, job RecordMaintenanceJob) error
}

// RelatedCleanupResult merges DeletedApiKeyRecordCleanupResult and
// DeletedAccountRecordCleanupResult.
type RelatedCleanupResult struct {
	DeletedRows           int64
	HasMore               bool
	BlockedReason         string
	SafetyCursorCreatedAt string
	SafetyCursorID        string
}

// RelatedRecordCleaner executes the per-target record cleanup jobs. statsWriter
// mirrors the Node optional stats cleanup callback: postgres mode passes nil.
type RelatedRecordCleaner interface {
	CleanupApiKeyRelated(ctx context.Context, job RecordMaintenanceJob, statsWriter StatsWriter) (RelatedCleanupResult, error)
	CleanupAccountRelated(ctx context.Context, job RecordMaintenanceJob, statsWriter StatsWriter) (RelatedCleanupResult, error)
}

// UsageRecordsJobResult mirrors the cleanupUsageRecordsBefore return object
// in record-maintenance-queue.service.ts.
type UsageRecordsJobResult struct {
	CutoffAt      string
	DeletedRows   int64
	Batches       int
	BatchSize     int
	MaxBatches    int
	HasMore       bool
	BlockedReason string
}

// NonBusinessDataCleanupCounts mirrors NonBusinessDataHardCleanupResult.
type NonBusinessDataCleanupCounts struct {
	CutoffAt     string
	DeletedRows  int64
	DeletedFiles int64
	HasMore      bool
	TableRows    map[string]int64
	FileDeletes  map[string]int64
}

// NonBusinessDataCleaner mirrors cleanupNonBusinessDataBeforeWithResult for
// the dataset scope.
type NonBusinessDataCleaner interface {
	CleanupBefore(ctx context.Context, cutoffAt string, limit int) (NonBusinessDataCleanupCounts, error)
}

// RecordMaintenanceExecutor bundles the per-domain executors a
// record-maintenance job needs. Any nil executor makes the corresponding job
// type fail with the executor missing error instead of silently succeeding.
type RecordMaintenanceExecutor struct {
	RelatedRecords  RelatedRecordCleaner
	UsageRecords    UsageRecordsCleaner
	NonBusinessData NonBusinessDataCleaner
	StatsWriter     StatsWriter
}

// DeletedApiKeyRecordStatsCleanupInput mirrors DeletedApiKeyRecordStatsCleanupInput.
type DeletedApiKeyRecordStatsCleanupInput struct {
	Target       APIKeyCleanupTarget
	Rows         []map[string]any
	UpdatedAt    string
	ShardDeleted bool
}

// APIKeyCleanupTarget mirrors DeletedApiKeyRecordCleanupTarget.
type APIKeyCleanupTarget struct {
	APIKeyID        string
	SystemAccountID string
}

// DeletedAccountRecordStatsCleanupInput mirrors the account variant.
type DeletedAccountRecordStatsCleanupInput struct {
	Target       ExpiredDeletedAccountTarget
	Rows         []map[string]any
	UpdatedAt    string
	ShardDeleted bool
}

// PendingCleanupSummary mirrors PendingDeletedApiKeyRecordCleanupSummary /
// PendingDeletedAccountRecordCleanupSummary.
type PendingCleanupSummary struct {
	Attempted   int64
	Completed   int64
	Deferred    int64
	Failed      int64
	DeletedRows int64
}

// APIKeyRecordCleanupRetryer mirrors cleanupPendingDeletedApiKeyRecordTargetsAsync.
type APIKeyRecordCleanupRetryer interface {
	CleanupPendingTargets(ctx context.Context, limit int, statsWriter StatsWriter) (PendingCleanupSummary, error)
}

// AccountRecordCleanupRetryer mirrors cleanupPendingDeletedAccountRecordTargetsAsync.
type AccountRecordCleanupRetryer interface {
	CleanupPendingTargets(ctx context.Context, limit int, statsWriter StatsWriter) (PendingCleanupSummary, error)
}

// DatasetCheckpointer is the optional sqlite-mode WAL checkpoint hook
// (checkpointDatasetAndUsageDatabasesAfterDelete). Stores implement it when
// they own the dataset/usage-shard SQLite files.
type DatasetCheckpointer interface {
	CheckpointAfterDelete(ctx context.Context) error
}

// StorageKeyDeletionResult mirrors CodexContextStorageKeyDeletionResult.
type StorageKeyDeletionResult struct {
	Deleted              int64
	SucceededStorageKeys []string
	Failures             []CodexContextCleanupFailure
}

// CodexContextStorageKeyDeleter mirrors deleteCodexContextStorageKeys.
type CodexContextStorageKeyDeleter interface {
	DeleteStorageKeys(ctx context.Context, storageKeys []string) (StorageKeyDeletionResult, error)
}
