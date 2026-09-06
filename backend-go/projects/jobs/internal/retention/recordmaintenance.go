package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Record-maintenance job family, ported from
// modules/record-maintenance/record-maintenance-queue.service.ts run-once
// semantics: job normalization/validation, per-type execution with the exact
// Node log events, and the two bounded cleanup loops (usage records with the
// 24-hour minimum-age guard, non-business data with merged dataset+stats
// batches).

// Record maintenance job type discriminators (Node job.type strings).
const (
	JobTypeAPIKeyRelatedCleanup         = "api_key_related_cleanup"
	JobTypeAccountRelatedCleanup        = "account_related_cleanup"
	JobTypeUsageRecordsCleanup          = "usage_records_cleanup"
	JobTypeNonBusinessDataCleanup       = "non_business_data_cleanup"
	JobTypeAccountUsageSnapshotUpsert   = "account_usage_snapshot_upsert"
	AccountUsageSnapshotKindOpenAICodex = "openai_codex"
)

// RecordMaintenanceJob mirrors the Node RecordMaintenanceJob union as one
// struct; the Type discriminator decides which fields carry meaning.
type RecordMaintenanceJob struct {
	Type              string
	ID                string
	APIKeyID          string
	AccountID         string
	SystemAccountID   string
	RelatedAccountIDs []string
	AuthorizationIDs  []string
	TeamScopeIDs      []string
	CutoffAt          string
	BatchSize         int
	MaxBatches        int
	Kind              string
	Source            string
	Snapshot          map[string]any
	UpdatedAt         string
	CreatedAt         string
}

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// parseRfc3339Instant mirrors parseRfc3339Instant: RFC3339 with a mandatory
// Z or numeric offset; bare date-times are never guessed as local time.
func parseRfc3339Instant(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return time.Time{}, false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// requiredRfc3339Instant mirrors requiredRfc3339Instant: it canonicalizes to
// the UTC ISO string or fails with the Node error text.
func requiredRfc3339Instant(value, label string) (string, error) {
	parsed, ok := parseRfc3339Instant(value)
	if !ok {
		return "", fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return ISOString(parsed), nil
}

// rfc3339InstantMilliseconds mirrors rfc3339InstantMilliseconds with the
// record-maintenance label wrapper error text.
func rfc3339InstantMilliseconds(value, label string) (int64, error) {
	parsed, ok := parseRfc3339Instant(value)
	if !ok {
		return 0, fmt.Errorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
	}
	return parsed.UnixMilli(), nil
}

// newRecordMaintenanceID mirrors newId('recmaint'):
// recmaint_<epochMillis>_<8 hex chars>.
func newRecordMaintenanceID(now time.Time) string {
	return fmt.Sprintf("recmaint_%d_%08x", now.UnixMilli(), pseudoRandomUint32())
}

// NormalizeRecordMaintenanceJob mirrors normalizeRecordMaintenanceJob: fill
// id/createdAt defaults and validate the type-specific instants.
func NormalizeRecordMaintenanceJob(input RecordMaintenanceJob, now time.Time) (RecordMaintenanceJob, error) {
	job := input
	if job.ID == "" {
		job.ID = newRecordMaintenanceID(now)
	}
	createdAt := job.CreatedAt
	if createdAt == "" {
		createdAt = ISOString(now)
	}
	normalizedCreatedAt, err := requiredRfc3339Instant(createdAt, "数据维护任务 createdAt")
	if err != nil {
		return RecordMaintenanceJob{}, err
	}
	job.CreatedAt = normalizedCreatedAt
	switch job.Type {
	case JobTypeAPIKeyRelatedCleanup, JobTypeAccountRelatedCleanup:
		return job, nil
	case JobTypeUsageRecordsCleanup, JobTypeNonBusinessDataCleanup:
		cutoffAt, err := requiredRfc3339Instant(job.CutoffAt, "数据维护清理 cutoffAt")
		if err != nil {
			return RecordMaintenanceJob{}, err
		}
		job.CutoffAt = cutoffAt
		return job, nil
	case JobTypeAccountUsageSnapshotUpsert:
		updatedAt, err := requiredRfc3339Instant(job.UpdatedAt, "账号用量快照 updatedAt")
		if err != nil {
			return RecordMaintenanceJob{}, err
		}
		job.UpdatedAt = updatedAt
		return job, nil
	default:
		return RecordMaintenanceJob{}, fmt.Errorf("未知数据维护任务：%s", job.Type)
	}
}

// ValidateRecordMaintenanceJob mirrors isRecordMaintenanceJob on a decoded
// struct: it checks the discriminator-specific required fields.
func ValidateRecordMaintenanceJob(job RecordMaintenanceJob) error {
	switch job.Type {
	case JobTypeAPIKeyRelatedCleanup:
		if job.APIKeyID == "" || job.SystemAccountID == "" {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
	case JobTypeAccountRelatedCleanup:
		if job.AccountID == "" || job.SystemAccountID == "" {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
	case JobTypeUsageRecordsCleanup, JobTypeNonBusinessDataCleanup:
		if _, ok := parseRfc3339Instant(job.CutoffAt); !ok {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
		if job.BatchSize == 0 && job.MaxBatches == 0 {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
	case JobTypeAccountUsageSnapshotUpsert:
		if job.AccountID == "" || job.Kind != AccountUsageSnapshotKindOpenAICodex || job.Snapshot == nil {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
		if _, ok := parseRfc3339Instant(job.UpdatedAt); !ok {
			return errors.New("Redis Stream 数据维护消息格式无效")
		}
	default:
		return errors.New("Redis Stream 数据维护消息格式无效")
	}
	return nil
}

// UsageRecordsCleanupJob builds a normalized usage_records_cleanup job (the
// payload the Postgres data-retention dispatch enqueues).
func UsageRecordsCleanupJob(cutoffAt string, batchSize, maxBatches int, now time.Time) (RecordMaintenanceJob, error) {
	return NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type:       JobTypeUsageRecordsCleanup,
		CutoffAt:   cutoffAt,
		BatchSize:  batchSize,
		MaxBatches: maxBatches,
	}, now)
}

// AccountRelatedCleanupJob builds a normalized account_related_cleanup job.
func AccountRelatedCleanupJob(target ExpiredDeletedAccountTarget, now time.Time) (RecordMaintenanceJob, error) {
	return NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type:              JobTypeAccountRelatedCleanup,
		AccountID:         target.AccountID,
		SystemAccountID:   target.SystemAccountID,
		RelatedAccountIDs: target.RelatedAccountIDs,
		AuthorizationIDs:  target.AuthorizationIDs,
		TeamScopeIDs:      target.TeamScopeIDs,
	}, now)
}

// APIKeyRelatedCleanupJob builds a normalized api_key_related_cleanup job.
func APIKeyRelatedCleanupJob(apiKeyID, systemAccountID string, now time.Time) (RecordMaintenanceJob, error) {
	return NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type:            JobTypeAPIKeyRelatedCleanup,
		APIKeyID:        apiKeyID,
		SystemAccountID: systemAccountID,
	}, now)
}

// RecordMaintenanceRunner owns the run-once execution semantics.
type RecordMaintenanceRunner struct {
	Mode     Mode
	Clock    Clock
	Logger   *slog.Logger
	Executor RecordMaintenanceExecutor
}

// RunOnce mirrors runRecordMaintenanceJobOnce: normalize, execute per type,
// emit the Node log events, and return the result object as a map with Node
// field names.
func (r *RecordMaintenanceRunner) RunOnce(ctx context.Context, input RecordMaintenanceJob) (map[string]any, error) {
	job, err := NormalizeRecordMaintenanceJob(input, clockNow(r.Clock))
	if err != nil {
		return nil, err
	}
	switch job.Type {
	case JobTypeAPIKeyRelatedCleanup:
		result, err := r.relatedRecords().CleanupApiKeyRelated(ctx, job, r.statsWriter())
		if err != nil {
			return nil, err
		}
		deferred := result.HasMore || result.BlockedReason != ""
		logger := r.logger()
		message := "API Key 关联数据清理完成"
		event := "record_maintenance_api_key_cleanup_completed"
		if deferred {
			message = "API Key 关联数据清理等待统计游标追平"
			event = "record_maintenance_api_key_cleanup_deferred"
		}
		logger.Info(message, append([]any{"event", event, "jobId", job.ID}, relatedCleanupResultValues(job, result)...)...)
		return relatedCleanupResultMap(job, result), nil
	case JobTypeAccountRelatedCleanup:
		result, err := r.relatedRecords().CleanupAccountRelated(ctx, job, r.statsWriter())
		if err != nil {
			return nil, err
		}
		deferred := result.HasMore || result.BlockedReason != ""
		logger := r.logger()
		message := "AI 账户关联数据清理完成"
		event := "record_maintenance_account_cleanup_completed"
		if deferred {
			message = "AI 账户关联数据清理等待统计游标追平"
			event = "record_maintenance_account_cleanup_deferred"
		}
		logger.Info(message, append([]any{"event", event, "jobId", job.ID}, relatedCleanupResultValues(job, result)...)...)
		return relatedCleanupResultMap(job, result), nil
	case JobTypeUsageRecordsCleanup:
		result, err := r.runUsageRecordsCleanup(ctx, job)
		if err != nil {
			return nil, err
		}
		r.logger().Info("使用记录后台清理完成",
			"event", "record_maintenance_usage_records_cleanup_completed",
			"jobId", job.ID,
			"cutoffAt", result.CutoffAt,
			"deletedRows", result.DeletedRows,
			"batches", result.Batches,
			"batchSize", result.BatchSize,
			"maxBatches", result.MaxBatches,
			"hasMore", result.HasMore,
			"blockedReason", result.BlockedReason,
		)
		return map[string]any{
			"cutoffAt":      result.CutoffAt,
			"deletedRows":   result.DeletedRows,
			"batches":       result.Batches,
			"batchSize":     result.BatchSize,
			"maxBatches":    result.MaxBatches,
			"hasMore":       result.HasMore,
			"blockedReason": result.BlockedReason,
		}, nil
	case JobTypeNonBusinessDataCleanup:
		result, err := r.runNonBusinessDataCleanup(ctx, job)
		if err != nil {
			return nil, err
		}
		r.logger().Info("非业务数据后台硬清理完成",
			"event", "record_maintenance_non_business_data_cleanup_completed",
			"jobId", job.ID,
			"cutoffAt", result.CutoffAt,
			"deletedRows", result.DeletedRows,
			"deletedFiles", result.DeletedFiles,
			"batches", result.Batches,
			"batchSize", result.BatchSize,
			"maxBatches", result.MaxBatches,
			"hasMore", result.HasMore,
			"tableRows", result.TableRows,
			"fileDeletes", result.FileDeletes,
		)
		return map[string]any{
			"cutoffAt":     result.CutoffAt,
			"deletedRows":  result.DeletedRows,
			"deletedFiles": result.DeletedFiles,
			"batches":      result.Batches,
			"batchSize":    result.BatchSize,
			"maxBatches":   result.MaxBatches,
			"hasMore":      result.HasMore,
			"tableRows":    result.TableRows,
			"fileDeletes":  result.FileDeletes,
		}, nil
	case JobTypeAccountUsageSnapshotUpsert:
		if err := r.processAccountUsageSnapshotUpsertJobs(ctx, []RecordMaintenanceJob{job}); err != nil {
			return nil, err
		}
		return map[string]any{"upsertedCount": 1}, nil
	default:
		return nil, fmt.Errorf("未知数据维护任务：%s", job.Type)
	}
}

// RunAccountUsageSnapshotUpserts executes a consecutive
// account_usage_snapshot_upsert run with ONE stats-writer round trip (Node
// record-maintenance-queue.service.ts:846-868, the flush loop's
// collectAccountUsageSnapshotJobs + processAccountUsageSnapshotUpsertJobs
// pairing; D5). Every job of the run is normalized exactly like RunOnce.
func (r *RecordMaintenanceRunner) RunAccountUsageSnapshotUpserts(ctx context.Context, jobs []RecordMaintenanceJob) (map[string]any, error) {
	normalized := make([]RecordMaintenanceJob, 0, len(jobs))
	for _, job := range jobs {
		item, err := NormalizeRecordMaintenanceJob(job, clockNow(r.Clock))
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, item)
	}
	if err := r.processAccountUsageSnapshotUpsertJobs(ctx, normalized); err != nil {
		return nil, err
	}
	return map[string]any{"upsertedCount": len(normalized)}, nil
}

func (r *RecordMaintenanceRunner) relatedRecords() RelatedRecordCleaner {
	if r.Executor.RelatedRecords == nil {
		return missingRelatedRecords{}
	}
	return r.Executor.RelatedRecords
}

func (r *RecordMaintenanceRunner) usageRecords() UsageRecordsCleaner {
	if r.Executor.UsageRecords == nil {
		return missingUsageRecords{}
	}
	return r.Executor.UsageRecords
}

func (r *RecordMaintenanceRunner) nonBusinessData() NonBusinessDataCleaner {
	if r.Executor.NonBusinessData == nil {
		return missingNonBusinessData{}
	}
	return r.Executor.NonBusinessData
}

func (r *RecordMaintenanceRunner) statsWriter() StatsWriter {
	if r.Mode == ModePostgres {
		return nil
	}
	return r.Executor.StatsWriter
}

func (r *RecordMaintenanceRunner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

type missingRelatedRecords struct{}

func (missingRelatedRecords) CleanupApiKeyRelated(context.Context, RecordMaintenanceJob, StatsWriter) (RelatedCleanupResult, error) {
	return RelatedCleanupResult{}, errors.New("retention related record cleaner 未初始化")
}

func (missingRelatedRecords) CleanupAccountRelated(context.Context, RecordMaintenanceJob, StatsWriter) (RelatedCleanupResult, error) {
	return RelatedCleanupResult{}, errors.New("retention related record cleaner 未初始化")
}

type missingNonBusinessData struct{}

func (missingNonBusinessData) CleanupBefore(context.Context, string, int) (NonBusinessDataCleanupCounts, error) {
	return NonBusinessDataCleanupCounts{}, errors.New("retention non-business data cleaner 未初始化")
}

// runUsageRecordsCleanup mirrors the cleanupUsageRecordsBefore helper: the
// 24-hour minimum age guard plus a batch loop without pauses (temporary
// maintenance worker context).
func (r *RecordMaintenanceRunner) runUsageRecordsCleanup(ctx context.Context, job RecordMaintenanceJob) (UsageRecordsJobResult, error) {
	result := UsageRecordsJobResult{
		CutoffAt:   job.CutoffAt,
		BatchSize:  job.BatchSize,
		MaxBatches: job.MaxBatches,
	}
	cutoffMillis, err := rfc3339InstantMilliseconds(job.CutoffAt, "使用记录清理截止时间")
	if err != nil {
		return result, err
	}
	if cutoffMillis > clockNow(r.Clock).UnixMilli()-minimumUsageRecordAgeMs {
		result.BlockedReason = "不能清理最近 1 天内的使用记录"
		return result, nil
	}
	for index := 0; index < job.MaxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		batch, err := r.usageRecords().CleanupProcessedBefore(ctx, job.CutoffAt, job.BatchSize)
		if err != nil {
			return result, err
		}
		result.DeletedRows += batch.DeletedRows
		result.HasMore = batch.HasMore
		if batch.BlockedReason != "" {
			result.BlockedReason = batch.BlockedReason
		}
		if batch.DeletedRows > 0 || batch.DroppedPartitions > 0 {
			result.Batches++
		}
		if batch.DeletedRows == 0 && batch.DroppedPartitions == 0 {
			return result, nil
		}
		if !batch.HasMore {
			return result, nil
		}
	}
	return result, nil
}

// NonBusinessDataJobResult mirrors the cleanupNonBusinessDataBefore return
// object: the merged hard-cleanup counters plus the loop bookkeeping.
type NonBusinessDataJobResult struct {
	CutoffAt     string
	DeletedRows  int64
	DeletedFiles int64
	HasMore      bool
	TableRows    map[string]int64
	FileDeletes  map[string]int64
	Batches      int
	BatchSize    int
	MaxBatches   int
}

// runNonBusinessDataCleanup mirrors cleanupNonBusinessDataBefore: every
// round merges the dataset batch with the stats-writer batch.
func (r *RecordMaintenanceRunner) runNonBusinessDataCleanup(ctx context.Context, job RecordMaintenanceJob) (NonBusinessDataJobResult, error) {
	result := NonBusinessDataJobResult{CutoffAt: job.CutoffAt, BatchSize: job.BatchSize, MaxBatches: job.MaxBatches, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}
	if _, err := rfc3339InstantMilliseconds(job.CutoffAt, "非业务数据清理截止时间"); err != nil {
		return result, err
	}
	aggregate := NonBusinessDataCleanupCounts{CutoffAt: job.CutoffAt, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}
	for index := 0; index < job.MaxBatches; index++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		batch, err := r.nonBusinessData().CleanupBefore(ctx, job.CutoffAt, job.BatchSize)
		if err != nil {
			return result, err
		}
		statsBatch, err := r.statsWriterForNonBusiness().CleanupNonBusinessStatsData(ctx, job.CutoffAt, job.BatchSize)
		if err != nil {
			return result, err
		}
		mergedBatch := mergeNonBusinessCleanupResult(batch, statsBatch)
		aggregate = mergeNonBusinessCleanupResult(aggregate, mergedBatch)
		if mergedBatch.DeletedRows > 0 || mergedBatch.DeletedFiles > 0 {
			result.Batches++
		}
		if !mergedBatch.HasMore || (mergedBatch.DeletedRows == 0 && mergedBatch.DeletedFiles == 0) {
			break
		}
	}
	result.CutoffAt = aggregate.CutoffAt
	result.DeletedRows = aggregate.DeletedRows
	result.DeletedFiles = aggregate.DeletedFiles
	result.HasMore = aggregate.HasMore
	result.TableRows = aggregate.TableRows
	result.FileDeletes = aggregate.FileDeletes
	return result, nil
}

func (r *RecordMaintenanceRunner) statsWriterForNonBusiness() StatsWriter {
	if r.Executor.StatsWriter == nil {
		return missingStatsWriter{}
	}
	return r.Executor.StatsWriter
}

// processAccountUsageSnapshotUpsertJobs mirrors
// processAccountUsageSnapshotUpsertJobs: one stats-writer round trip per
// consecutive job batch.
func (r *RecordMaintenanceRunner) processAccountUsageSnapshotUpsertJobs(ctx context.Context, jobs []RecordMaintenanceJob) error {
	if r.Executor.StatsWriter == nil {
		return errors.New("retention stats writer 未初始化")
	}
	inputs := make([]AccountUsageSnapshotUpsertInput, 0, len(jobs))
	for _, job := range jobs {
		inputs = append(inputs, AccountUsageSnapshotUpsertInput{
			AccountID: job.AccountID,
			Kind:      job.Kind,
			Source:    job.Source,
			Snapshot:  job.Snapshot,
			UpdatedAt: job.UpdatedAt,
		})
	}
	if err := r.Executor.StatsWriter.UpsertAccountUsageSnapshots(ctx, inputs); err != nil {
		return err
	}
	accountIDs := make([]string, 0, len(jobs))
	jobIDs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		accountIDs = append(accountIDs, job.AccountID)
		jobIDs = append(jobIDs, job.ID)
	}
	r.logger().Info("账号用量快照后台批量写入完成",
		"event", "record_maintenance_account_usage_snapshots_upserted",
		"jobCount", len(jobs),
		"jobIds", jobIDs,
		"accountIds", accountIDs,
	)
	return nil
}

func mergeNonBusinessCleanupResult(current, batch NonBusinessDataCleanupCounts) NonBusinessDataCleanupCounts {
	merged := NonBusinessDataCleanupCounts{
		CutoffAt:     batch.CutoffAt,
		DeletedRows:  current.DeletedRows + batch.DeletedRows,
		DeletedFiles: current.DeletedFiles + batch.DeletedFiles,
		HasMore:      current.HasMore || batch.HasMore,
		TableRows:    map[string]int64{},
		FileDeletes:  map[string]int64{},
	}
	for key, value := range current.TableRows {
		merged.TableRows[key] += value
	}
	for key, value := range batch.TableRows {
		merged.TableRows[key] += value
	}
	for key, value := range current.FileDeletes {
		merged.FileDeletes[key] += value
	}
	for key, value := range batch.FileDeletes {
		merged.FileDeletes[key] += value
	}
	return merged
}

func relatedCleanupResultValues(job RecordMaintenanceJob, result RelatedCleanupResult) []any {
	values := []any{"deletedRows", result.DeletedRows, "hasMore", result.HasMore}
	if result.BlockedReason != "" {
		values = append(values, "blockedReason", result.BlockedReason)
	}
	if result.SafetyCursorCreatedAt != "" {
		values = append(values, "safetyCursorCreatedAt", result.SafetyCursorCreatedAt)
	}
	if result.SafetyCursorID != "" {
		values = append(values, "safetyCursorId", result.SafetyCursorID)
	}
	switch job.Type {
	case JobTypeAPIKeyRelatedCleanup:
		values = append(values, "apiKeyId", job.APIKeyID, "systemAccountId", job.SystemAccountID)
	case JobTypeAccountRelatedCleanup:
		values = append(values, "accountId", job.AccountID, "systemAccountId", job.SystemAccountID)
	}
	return values
}

func relatedCleanupResultMap(job RecordMaintenanceJob, result RelatedCleanupResult) map[string]any {
	output := map[string]any{
		"deletedRows": result.DeletedRows,
		"hasMore":     result.HasMore,
	}
	if result.BlockedReason != "" {
		output["blockedReason"] = result.BlockedReason
	}
	if result.SafetyCursorCreatedAt != "" {
		output["safetyCursorCreatedAt"] = result.SafetyCursorCreatedAt
	}
	if result.SafetyCursorID != "" {
		output["safetyCursorId"] = result.SafetyCursorID
	}
	switch job.Type {
	case JobTypeAPIKeyRelatedCleanup:
		output["apiKeyId"] = job.APIKeyID
		output["systemAccountId"] = job.SystemAccountID
	case JobTypeAccountRelatedCleanup:
		output["accountId"] = job.AccountID
		output["systemAccountId"] = job.SystemAccountID
		output["relatedAccountIds"] = job.RelatedAccountIDs
		output["authorizationIds"] = job.AuthorizationIDs
		output["teamScopeIds"] = job.TeamScopeIDs
	}
	return output
}
