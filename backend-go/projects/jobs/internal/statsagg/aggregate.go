package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// StatsTimezoneProvider mirrors gatewayquota.StatsTimezoneProvider / Node
// usageStatsTimezoneAsync：聚合前解析统计时区。
type StatsTimezoneProvider interface {
	StatsTimezone(ctx context.Context) (*time.Location, error)
}

// StaticTimezoneSource 是测试与固定时区的实现。
type StaticTimezoneSource struct{ Location *time.Location }

func (s StaticTimezoneSource) StatsTimezone(context.Context) (*time.Location, error) {
	if s.Location == nil {
		return nil, errors.New("statsagg static timezone source requires a location")
	}
	return s.Location, nil
}

// CacheReadCostEstimator 注入 Node estimateProviderCacheReadCostUsd
// （model-pricing 域）。返回 (cost, true) 时写入行 cache_read_cost_usd；
// (0, false) 表示无法估算，行保持 NULL（Node 语义：仅当估算值 > 0 才落字段）。
type CacheReadCostEstimator func(providerCode, model string, cacheReadTokens float64) (float64, bool)

// Aggregator 承载 usage-stats-aggregation job 的聚合写入。
type Aggregator struct {
	DB      *sql.DB
	Dialect Dialect
	Clock   StatsTimezoneProvider
	// Now 注入当前时间（测试用）；nil 时取 time.Now。
	Now                    func() time.Time
	CacheReadCostEstimator CacheReadCostEstimator
}

func (a *Aggregator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// AggregateOptions mirrors aggregate_usage_stats stats-writer 操作参数。
type AggregateOptions struct {
	BatchSize         int
	SafeCreatedBefore string // 空则在构造时按 now-15s 计算
}

// AggregateUsageStatsBatch mirrors aggregateUsageStatsBatchAsync（PG 单游标
// 权威路径；SQLite 测试共用同一 SQL 语义）。返回本批处理行数。
func (a *Aggregator) AggregateUsageStatsBatch(ctx context.Context, options AggregateOptions) (int, error) {
	batchLimit := options.BatchSize
	if batchLimit < 1 {
		batchLimit = 1
	}
	safeCreatedBefore := options.SafeCreatedBefore
	if safeCreatedBefore == "" {
		safeCreatedBefore = FormatRFC3339Millis(a.now().Add(-UsageStatsCursorSafetyDelaySeconds * time.Second))
	} else if _, ok := CanonicalizeRFC3339Instant(safeCreatedBefore); !ok {
		return 0, errors.New("用量统计 safeCreatedBefore必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	updatedAt := FormatRFC3339Millis(a.now())
	timezone, err := a.Clock.StatsTimezone(ctx)
	if err != nil {
		return 0, err
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	state, err := statsJobState(ctx, tx, a.Dialect)
	if err != nil {
		return 0, err
	}
	query := a.Dialect.bind(`
		SELECT ` + usageStatsRecordSelectColumns + `
		FROM ` + a.Dialect.UsageRecordsTable() + `
		WHERE created_at <= ?
		  AND (created_at > ? OR (created_at = ? AND id > ?))
		ORDER BY created_at ASC, id ASC
		LIMIT ?
	`)
	rows, err := tx.QueryContext(ctx, query, safeCreatedBefore, state.CursorCreatedAt, state.CursorCreatedAt, state.CursorID, batchLimit)
	if err != nil {
		return 0, err
	}
	records, err := scanUsageStatsRecordRows(rows)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		lagSeconds, err := a.latestUsageRecordLagSeconds(ctx, tx, safeCreatedBefore, state.CursorCreatedAt, state.CursorID)
		if err != nil {
			return 0, err
		}
		if err := a.updateStatsJobState(ctx, tx, statsJobStateInput{LastSuccessAt: &updatedAt, LagSeconds: &lagSeconds}); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	if err := a.aggregateRows(ctx, tx, records, updatedAt, timezone); err != nil {
		return 0, err
	}
	last := records[len(records)-1]
	lagSeconds := statsLagSecondsFromCursor(last.CreatedAt, a.now())
	if err := a.updateStatsJobState(ctx, tx, statsJobStateInput{
		CursorCreatedAt: &last.CreatedAt,
		CursorID:        &last.ID,
		LastSuccessAt:   &updatedAt,
		LagSeconds:      &lagSeconds,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(records), nil
}

// usageStatsRecordSelectColumns mirrors usage-stats-types.ts
// USAGE_STATS_RECORD_SELECT_COLUMNS（顺序一致）。
const usageStatsRecordSelectColumns = `
	id,
	system_account_id,
	trace_id,
	traffic_source,
	client_ip,
	api_key_id,
	group_id,
	account_id,
	endpoint,
	provider_code,
	provider_protocol_profile_id,
	model,
	status_code,
	success,
	failure_attribution,
	first_token_ms,
	duration_ms,
	input_tokens,
	output_tokens,
	cache_read_tokens,
	cache_read_cost_usd,
	cache_write_tokens,
	cache_write_1h_tokens,
	cache_write_cost_usd,
	thinking_tokens,
	input_image_tokens,
	output_image_tokens,
	cost_usd,
	error_code,
	error_message,
	account_owner_system_account_id,
	group_owner_system_account_id,
	account_access_type,
	group_access_type,
	account_authorization_id,
	account_authorization_source_type,
	account_authorization_source_team_id,
	group_authorization_id,
	group_authorization_source_type,
	group_authorization_source_team_id,
	created_at
`

func nullableStringScanner(rows *sql.Rows) ([]any, *UsageStatsRecordRow) {
	row := &UsageStatsRecordRow{}
	return []any{
		&row.ID, &row.SystemAccountID, &row.TraceID, &row.TrafficSource,
		&row.ClientIP, &row.APIKeyID, &row.GroupID, &row.AccountID, &row.Endpoint,
		&row.ProviderCode, &row.ProviderProtocolProfileID, &row.Model,
		&row.StatusCode, &row.Success, &row.FailureAttribution,
		&row.FirstTokenMs, &row.DurationMs,
		&row.InputTokens, &row.OutputTokens,
		&row.CacheReadTokens, &row.CacheReadCostUsd,
		&row.CacheWriteTokens, &row.CacheWrite1hTokens, &row.CacheWriteCostUsd,
		&row.ThinkingTokens, &row.InputImageTokens, &row.OutputImageTokens,
		&row.CostUsd, &row.ErrorCode, &row.ErrorMessage,
		&row.AccountOwnerSystemAccountID, &row.GroupOwnerSystemAccountID,
		&row.AccountAccessType, &row.GroupAccessType,
		&row.AccountAuthorizationID, &row.AccountAuthorizationSourceType, &row.AccountAuthorizationSourceTeamID,
		&row.GroupAuthorizationID, &row.GroupAuthorizationSourceType, &row.GroupAuthorizationSourceTeamID,
		&row.CreatedAt,
	}, row
}

func scanUsageStatsRecordRows(rows *sql.Rows) ([]UsageStatsRecordRow, error) {
	defer rows.Close()
	var result []UsageStatsRecordRow
	for rows.Next() {
		scan, row := nullableStringScanner(rows)
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		result = append(result, normalizePostgresUsageStatsRecordRow(*row))
	}
	return result, rows.Err()
}

// normalizePostgresUsageStatsRecordRow mirrors normalizePostgresUsageStatsRecordRow：
// created_at 规范化 + 数值列以 nullableNumber 承载。
func normalizePostgresUsageStatsRecordRow(row UsageStatsRecordRow) UsageStatsRecordRow {
	if normalized, ok := CanonicalizeRFC3339Instant(row.CreatedAt); ok {
		row.CreatedAt = normalized
	}
	row.StatusCode = nullableNumber(row.StatusCode)
	row.Success = orZero(&row.Success)
	row.FirstTokenMs = nullableNumber(row.FirstTokenMs)
	row.DurationMs = nullableNumber(row.DurationMs)
	row.InputTokens = nullableNumber(row.InputTokens)
	row.OutputTokens = nullableNumber(row.OutputTokens)
	row.CacheReadTokens = nullableNumber(row.CacheReadTokens)
	row.CacheReadCostUsd = nullableNumber(row.CacheReadCostUsd)
	row.CacheWriteTokens = nullableNumber(row.CacheWriteTokens)
	row.CacheWrite1hTokens = nullableNumber(row.CacheWrite1hTokens)
	row.CacheWriteCostUsd = nullableNumber(row.CacheWriteCostUsd)
	row.ThinkingTokens = nullableNumber(row.ThinkingTokens)
	row.InputImageTokens = nullableNumber(row.InputImageTokens)
	row.OutputImageTokens = nullableNumber(row.OutputImageTokens)
	row.CostUsd = nullableNumber(row.CostUsd)
	return row
}

func nullableNumber(value *float64) *float64 {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	return value
}

// applyPostgresEstimatedCacheReadCost mirrors applyPostgresEstimatedCacheReadCost：
// cache_read_cost_usd 缺失时按定价域估算，仅当 > 0 时回填。
func (a *Aggregator) applyPostgresEstimatedCacheReadCost(row *UsageStatsRecordRow) {
	if row.CacheReadCostUsd != nil {
		return
	}
	if a.CacheReadCostEstimator == nil {
		return
	}
	providerCode := ""
	if row.ProviderCode != nil {
		providerCode = *row.ProviderCode
	}
	model := ""
	if row.Model != nil {
		model = *row.Model
	}
	cost, ok := a.CacheReadCostEstimator(providerCode, model, orZero(row.CacheReadTokens))
	if !ok || cost <= 0 {
		return
	}
	value := cost
	row.CacheReadCostUsd = &value
}

type statsTotalsKey struct {
	SystemAccountID string
	ScopeType       string
	ScopeID         string
}

type statsTimeEntryKey struct {
	tableName string
	timeValue string
	scope     statsTotalsKey
}

type statsModelKey struct {
	tableName    string
	timeValue    string
	systemID     string
	providerCode string
	model        string
}

type statsErrorKey struct {
	tableName    string
	timeValue    string
	systemID     string
	errorGroup   string
	providerCode string
	errorCode    string
	statusCode   float64
}

type aggregatedAccountQualityEntry struct {
	AccountID         string
	SystemAccountID   string
	ProviderCode      string
	StatMinute        string
	RequestCount      float64
	SuccessCount      float64
	ErrorCount        float64
	FirstTokenMsSum   float64
	FirstTokenMsCount float64
	LastSampleAt      string
	LastSuccessAt     string
	LastErrorAt       string
	LastErrorMessage  string
}

type aggregatedAccountHealthEntry struct {
	AccountID       string
	SystemAccountID string
	ProviderCode    string
	StatHour        string
	Status          string
	LastObservedAt  string
	LastRecordID    string
	StatusCode      *float64
	ErrorCode       *string
	ErrorMessage    *string
}

type aggregatedUsageErrorEntry struct {
	Bucket          TimeBucketDefinition
	SystemAccountID string
	TimeValue       string
	ErrorGroup      string
	ProviderCode    string
	ErrorCode       string
	StatusCode      float64
	ErrorMessage    *string
	RequestCount    float64
	ErrorCount      float64
}

type aggregatedUsageModelEntry struct {
	Bucket          TimeBucketDefinition
	SystemAccountID string
	ProviderCode    string
	Model           string
	TimeValue       string
	Accumulator     UsageStatsAccumulator
}

type aggregatedTimeEntry struct {
	Bucket          TimeBucketDefinition
	TimeValue       string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
	Accumulator     UsageStatsAccumulator
}

// aggregateRows 是 aggregatePostgresUsageStatsRows 的移植：批内内存聚合 +
// 逐表 UPSERT。供 job 与对账测试共用。
func (a *Aggregator) aggregateRows(ctx context.Context, tx *sql.Tx, records []UsageStatsRecordRow, updatedAt string, timezone *time.Location) error {
	if len(records) == 0 {
		return nil
	}
	lookup, err := a.createUsageStatsAuthorizationLookup(ctx, tx, records)
	if err != nil {
		return err
	}
	totalEntries := map[statsTotalsKey]*UsageStatsAccumulator{}
	timeEntries := map[statsTimeEntryKey]*aggregatedTimeEntry{}
	latencyEntries := map[latencyEntryKey]*AggregatedLatencyEntry{}
	modelEntries := map[statsModelKey]*aggregatedUsageModelEntry{}
	errorEntries := map[statsErrorKey]*aggregatedUsageErrorEntry{}
	accountQualityEntries := map[string]*aggregatedAccountQualityEntry{}
	accountHealthEntries := map[string]*aggregatedAccountHealthEntry{}

	for _, record := range records {
		row := record
		if !ShouldAggregateUsageStatsRecord(row) {
			continue
		}
		a.applyPostgresEstimatedCacheReadCost(&row)
		timeKeys, err := UsageStatsTimeKeysFor(row.CreatedAt, timezone)
		if err != nil {
			return err
		}
		for _, entry := range UsageStatsEntries(row, lookup) {
			key := statsTotalsKey{entry.SystemAccountID, entry.ScopeType, entry.ScopeID}
			if existing, ok := totalEntries[key]; ok {
				if err := MergeAccumulator(existing, entry.Accumulator); err != nil {
					return err
				}
			} else {
				totalEntries[key] = &entry.Accumulator
			}
			for _, bucket := range usageStatsTimeBuckets {
				timeValue := timeKeys.timeValue(bucket.ValueKey)
				timeKey := statsTimeEntryKey{bucket.TableName, timeValue, key}
				if existing, ok := timeEntries[timeKey]; ok {
					if err := MergeAccumulator(&existing.Accumulator, entry.Accumulator); err != nil {
						return err
					}
				} else {
					timeEntries[timeKey] = &aggregatedTimeEntry{
						Bucket: bucket, TimeValue: timeValue,
						SystemAccountID: entry.SystemAccountID, ScopeType: entry.ScopeType, ScopeID: entry.ScopeID,
						Accumulator: entry.Accumulator,
					}
				}
			}
			AddAggregatedLatencyEntries(latencyEntries, entry, row, timeKeys)
		}
		if err := addPostgresAggregatedUsageModelEntries(modelEntries, row, timeKeys); err != nil {
			return err
		}
		if err := addPostgresAggregatedAccountQualityEntry(accountQualityEntries, row, timeKeys); err != nil {
			return err
		}
		addPostgresAggregatedAccountHealthEntry(accountHealthEntries, row, timeKeys.StatHour)
		if isSet(row.AccountAuthorizationID) || isSet(row.GroupAuthorizationID) {
			if err := a.upsertAuthorizationUsageReportRows(ctx, tx, row, timeKeys.StatDate, updatedAt, lookup); err != nil {
				return err
			}
		}
		if row.Success != 1 {
			addPostgresAggregatedUsageErrorEntries(errorEntries, row, timeKeys)
		}
	}

	// 收集派生窗口脏标记所需的 daily/hourly 引用（对齐
	// markPostgresDerivedWindowDirtyScopes 的入参：全部 timeEntries）。
	dailyEntries := []*aggregatedTimeEntry{}
	hourlyQuotaEntries := map[statsTotalsKey]*aggregatedTimeEntry{}
	quotaScopeTypes := map[string]bool{
		"api_key":                    true,
		"account_authorization":      true,
		"group_authorization":        true,
		"account_authorization_team": true,
		"group_authorization_team":   true,
	}
	for _, entry := range timeEntries {
		if entry.Bucket.TableName == "usage_stats_daily" {
			dailyEntries = append(dailyEntries, entry)
		}
		if entry.Bucket.TableName == "usage_stats_hourly" && quotaScopeTypes[entry.ScopeType] {
			hourlyQuotaEntries[statsTotalsKey{entry.SystemAccountID, entry.ScopeType, entry.ScopeID}] = entry
		}
	}

	if err := a.upsertUsageStatsTotals(ctx, tx, totalEntries, updatedAt); err != nil {
		return err
	}
	for _, bucket := range usageStatsTimeBuckets {
		bucketEntries := make([]*aggregatedTimeEntry, 0)
		for _, entry := range timeEntries {
			if entry.Bucket.TableName == bucket.TableName {
				bucketEntries = append(bucketEntries, entry)
			}
		}
		if err := a.upsertUsageStatsTimeBucket(ctx, tx, bucket, bucketEntries, updatedAt); err != nil {
			return err
		}
	}
	if err := a.upsertUsageLatencyEntries(ctx, tx, latencyEntries, updatedAt); err != nil {
		return err
	}
	if err := a.upsertUsageModelEntries(ctx, tx, modelEntries, updatedAt); err != nil {
		return err
	}
	if err := a.upsertUsageErrorEntries(ctx, tx, errorEntries, updatedAt); err != nil {
		return err
	}
	if err := a.upsertAccountQualityEntries(ctx, tx, accountQualityEntries, updatedAt); err != nil {
		return err
	}
	if err := a.upsertAccountHealthEntries(ctx, tx, accountHealthEntries, updatedAt); err != nil {
		return err
	}
	return a.markDerivedWindowDirtyScopes(ctx, tx, dailyEntries, hourlyQuotaEntries, updatedAt)
}

func addPostgresAggregatedUsageModelEntries(target map[statsModelKey]*aggregatedUsageModelEntry, row UsageStatsRecordRow, timeKeys UsageStatsTimeKeys) error {
	model := ""
	if row.Model != nil {
		model = strings.TrimSpace(*row.Model)
	}
	if model == "" {
		return nil
	}
	accumulator := UsageStatsAccumulatorFromRecord(row)
	providerCode := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		providerCode = *row.ProviderCode
	}
	systemAccountIds := []string{row.SystemAccountID, GlobalStatsSystemAccountID}
	for _, systemAccountID := range systemAccountIds {
		for _, bucket := range usageModelTimeBuckets {
			timeValue := timeKeys.timeValue(bucket.ValueKey)
			key := statsModelKey{bucket.TableName, timeValue, systemAccountID, providerCode, model}
			if existing, ok := target[key]; ok {
				if err := MergeAccumulator(&existing.Accumulator, accumulator); err != nil {
					return err
				}
				continue
			}
			target[key] = &aggregatedUsageModelEntry{
				Bucket: bucket, SystemAccountID: systemAccountID, ProviderCode: providerCode,
				Model: model, TimeValue: timeValue, Accumulator: accumulator,
			}
		}
	}
	return nil
}

func addPostgresAggregatedUsageErrorEntries(target map[statsErrorKey]*aggregatedUsageErrorEntry, row UsageStatsRecordRow, timeKeys UsageStatsTimeKeys) {
	errorGroup := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		errorGroup = *row.ProviderCode
	}
	providerCode := errorGroup
	errorCode := ""
	if row.ErrorCode != nil && *row.ErrorCode != "" {
		errorCode = *row.ErrorCode
	} else if row.StatusCode != nil {
		errorCode = fmt.Sprintf("%v", *row.StatusCode)
	} else {
		errorCode = "unknown"
	}
	statusCode := 0.0
	if row.StatusCode != nil {
		statusCode = *row.StatusCode
	}
	systemAccountIds := []string{row.SystemAccountID, GlobalStatsSystemAccountID}
	for _, systemAccountID := range systemAccountIds {
		for _, bucket := range usageErrorTimeBuckets {
			timeValue := timeKeys.timeValue(bucket.ValueKey)
			key := statsErrorKey{bucket.TableName, timeValue, systemAccountID, errorGroup, providerCode, errorCode, statusCode}
			if existing, ok := target[key]; ok {
				existing.RequestCount += 1
				existing.ErrorCount += 1
				if row.ErrorMessage != nil {
					existing.ErrorMessage = row.ErrorMessage
				}
				continue
			}
			target[key] = &aggregatedUsageErrorEntry{
				Bucket: bucket, SystemAccountID: systemAccountID, TimeValue: timeValue,
				ErrorGroup: errorGroup, ProviderCode: providerCode, ErrorCode: errorCode,
				StatusCode: statusCode, ErrorMessage: row.ErrorMessage,
				RequestCount: 1, ErrorCount: 1,
			}
		}
	}
}

func addPostgresAggregatedAccountQualityEntry(target map[string]*aggregatedAccountQualityEntry, row UsageStatsRecordRow, timeKeys UsageStatsTimeKeys) error {
	if !shouldRecordPostgresAccountQualityStats(row) || row.AccountID == nil || *row.AccountID == "" || row.APIKeyID == nil || *row.APIKeyID == "" {
		return nil
	}
	success := row.Success == 1
	hasFirstTokenSample := false
	firstTokenMsValue := 0.0
	if row.FirstTokenMs != nil && !math.IsNaN(*row.FirstTokenMs) && !math.IsInf(*row.FirstTokenMs, 0) && *row.FirstTokenMs >= 0 {
		hasFirstTokenSample = true
		firstTokenMsValue = *row.FirstTokenMs
	}
	firstTokenMs := 0.0
	if hasFirstTokenSample {
		firstTokenMs = firstTokenMsValue
	}
	statsSystemAccountID, err := postgresAccountQualityStatsSystemAccountID(row)
	if err != nil {
		return err
	}
	providerCode := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		providerCode = *row.ProviderCode
	}
	key := *row.AccountID + "\x00" + timeKeys.StatMinute
	existing, ok := target[key]
	if !ok {
		entry := &aggregatedAccountQualityEntry{
			AccountID: *row.AccountID, SystemAccountID: statsSystemAccountID, ProviderCode: providerCode,
			StatMinute:      timeKeys.StatMinute,
			RequestCount:    1,
			FirstTokenMsSum: firstTokenMs,
			LastSampleAt:    row.CreatedAt,
		}
		if success {
			entry.SuccessCount = 1
			entry.LastSuccessAt = row.CreatedAt
		} else {
			entry.ErrorCount = 1
			entry.LastErrorAt = row.CreatedAt
			if row.ErrorMessage != nil {
				entry.LastErrorMessage = *row.ErrorMessage
			}
		}
		entry.FirstTokenMsCount = boolCount(hasFirstTokenSample)
		target[key] = entry
		return nil
	}
	existing.RequestCount += 1
	if success {
		existing.SuccessCount += 1
	} else {
		existing.ErrorCount += 1
	}
	existing.FirstTokenMsSum += firstTokenMs
	if hasFirstTokenSample {
		existing.FirstTokenMsCount += 1
	}
	if comparison, err := CompareUsageStatsTimestamp(row.CreatedAt, existing.LastSampleAt); err == nil && comparison > 0 {
		existing.LastSampleAt = row.CreatedAt
		existing.SystemAccountID = statsSystemAccountID
		existing.ProviderCode = providerCode
	}
	if success {
		if latest, err := MaxOptionalISO(existing.LastSuccessAt, row.CreatedAt); err == nil {
			existing.LastSuccessAt = latest
		}
	} else {
		shouldReplace := existing.LastErrorAt == ""
		if !shouldReplace {
			if comparison, err := CompareUsageStatsTimestamp(row.CreatedAt, existing.LastErrorAt); err == nil && comparison >= 0 {
				shouldReplace = true
			}
		}
		if shouldReplace {
			existing.LastErrorAt = row.CreatedAt
			if row.ErrorMessage != nil {
				existing.LastErrorMessage = *row.ErrorMessage
			}
		}
	}
	return nil
}

func boolCount(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func shouldRecordPostgresAccountQualityStats(row UsageStatsRecordRow) bool {
	switch row.TrafficSource {
	case "runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring":
		return false
	}
	if row.Success == 1 {
		return true
	}
	return row.FailureAttribution != nil &&
		(*row.FailureAttribution == "account_upstream" || *row.FailureAttribution == "account_dependency")
}

func postgresAccountQualityStatsSystemAccountID(row UsageStatsRecordRow) (string, error) {
	if row.AccountAccessType == nil || *row.AccountAccessType == "" {
		return "", fmt.Errorf("使用记录 %s 缺少账户访问类型字段 account_access_type", row.ID)
	}
	if *row.AccountAccessType == "account_authorized" {
		return row.SystemAccountID, nil
	}
	if row.AccountOwnerSystemAccountID == nil || *row.AccountOwnerSystemAccountID == "" {
		return "", fmt.Errorf("使用记录 %s 缺少账户归属字段 account_owner_system_account_id", row.ID)
	}
	return *row.AccountOwnerSystemAccountID, nil
}

func addPostgresAggregatedAccountHealthEntry(target map[string]*aggregatedAccountHealthEntry, row UsageStatsRecordRow, statHour string) {
	if row.TrafficSource != "account_health_check" || row.AccountID == nil || *row.AccountID == "" {
		return
	}
	key := *row.AccountID + "\x00" + statHour
	if existing, ok := target[key]; ok {
		comparison, err := CompareUsageStatsTimestamp(existing.LastObservedAt, row.CreatedAt)
		if err == nil && (comparison > 0 || (comparison == 0 && existing.LastRecordID >= row.ID)) {
			return
		}
	}
	providerCode := "unknown"
	if row.ProviderCode != nil && *row.ProviderCode != "" {
		providerCode = *row.ProviderCode
	}
	entry := &aggregatedAccountHealthEntry{
		AccountID:       *row.AccountID,
		SystemAccountID: row.SystemAccountID,
		ProviderCode:    providerCode,
		StatHour:        statHour,
		LastObservedAt:  row.CreatedAt,
		LastRecordID:    row.ID,
		StatusCode:      row.StatusCode,
		ErrorCode:       row.ErrorCode,
		ErrorMessage:    row.ErrorMessage,
	}
	if row.Success == 1 {
		entry.Status = "success"
	} else {
		entry.Status = "failure"
	}
	target[key] = entry
}

// ---- job state ----

type statsJobStateRow struct {
	CursorCreatedAt string
	CursorID        string
	LagSeconds      *float64
}

func statsJobState(ctx context.Context, tx *sql.Tx, dialect Dialect) (statsJobStateRow, error) {
	state := statsJobStateRow{}
	query := dialect.bind(`SELECT cursor_created_at, cursor_id, lag_seconds FROM ` +
		dialect.StatsTable("stats_job_state") + ` WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'`)
	var cursorCreatedAt sql.NullString
	var cursorID sql.NullString
	var lagSeconds sql.NullFloat64
	err := tx.QueryRowContext(ctx, query).Scan(&cursorCreatedAt, &cursorID, &lagSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if cursorCreatedAt.Valid {
		normalized, ok := CanonicalizeRFC3339Instant(cursorCreatedAt.String)
		if !ok {
			return state, errors.New("用量统计 cursor_created_at必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		state.CursorCreatedAt = normalized
	}
	state.CursorID = cursorID.String
	if lagSeconds.Valid {
		state.LagSeconds = &lagSeconds.Float64
	}
	return state, nil
}

type statsJobStateInput struct {
	CursorCreatedAt  *string
	CursorID         *string
	LastSuccessAt    *string
	LastErrorMessage *string
	LagSeconds       *float64
}

func (a *Aggregator) updateStatsJobState(ctx context.Context, tx *sql.Tx, input statsJobStateInput) error {
	query := a.Dialect.bind(`
		INSERT INTO ` + a.Dialect.StatsTable("stats_job_state") + ` (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
		VALUES ('global', '', 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
		  cursor_created_at = COALESCE(excluded.cursor_created_at, ` + a.Dialect.qualifiedTarget("stats_job_state") + `.cursor_created_at),
		  cursor_id = COALESCE(excluded.cursor_id, ` + a.Dialect.qualifiedTarget("stats_job_state") + `.cursor_id),
		  last_success_at = COALESCE(excluded.last_success_at, ` + a.Dialect.qualifiedTarget("stats_job_state") + `.last_success_at),
		  last_error_message = excluded.last_error_message,
		  lag_seconds = excluded.lag_seconds,
		  updated_at = excluded.updated_at
	`)
	nullIfEmpty := func(value *string) any {
		if value == nil || *value == "" {
			return nil
		}
		return *value
	}
	var lagSeconds any
	if input.LagSeconds != nil {
		lagSeconds = *input.LagSeconds
	}
	updatedAt := FormatRFC3339Millis(a.now())
	_, err := tx.ExecContext(ctx, query,
		nullIfEmpty(input.CursorCreatedAt), nullIfEmpty(input.CursorID), nullIfEmpty(input.LastSuccessAt),
		nullIfEmpty(input.LastErrorMessage), lagSeconds, updatedAt)
	return err
}

func statsLagSecondsFromCursor(cursorCreatedAt string, now time.Time) float64 {
	milliseconds, ok := RFC3339Milliseconds(cursorCreatedAt)
	if !ok {
		panic(fmt.Sprintf("用量统计 cursor_created_at 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", cursorCreatedAt))
	}
	lag := (now.UnixMilli() - milliseconds) / 1000
	if lag < 0 {
		return 0
	}
	return float64(lag)
}

func (a *Aggregator) latestUsageRecordLagSeconds(ctx context.Context, tx *sql.Tx, safeCreatedBefore, cursorCreatedAt, cursorID string) (float64, error) {
	query := a.Dialect.bind(`
		SELECT created_at FROM ` + a.Dialect.UsageRecordsTable() + `
		WHERE created_at <= ?
		  AND (created_at > ? OR (created_at = ? AND id > ?))
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`)
	var latest sql.NullString
	err := tx.QueryRowContext(ctx, query, safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorID).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !latest.Valid || latest.String == "" {
		return 0, nil
	}
	return statsLagSecondsFromCursor(latest.String, a.now()), nil
}
