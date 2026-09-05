package cleanuprepo

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// usage-stats.repository.ts subtractPostgresUsageStatsRows 及其 subtract 家族的
// PostgreSQL 移植（Node 权威：final-archive/backend/src/storage/
// usage-stats.repository.ts + usage-stats-authorization-daily-writer.ts PG 分支）。
//
// 与 SQLite 路径（statssubtract.go subtractUsageStatsRecord 家族）的差异：
//   - PG 先把本批行聚合为 totals / time bucket / latency / model / error /
//     quality 维度条目，再对每个聚合条目执行 GREATEST(0, col - ?) 扣减与
//     空行 DELETE；
//   - 方言函数 SQLite 用 MAX/标量，PG 用 GREATEST/LEAST；
//   - stats 库表统一 juhe_stats. schema 限定，占位符经 DB.Bind 改写 $n；
//   - 扣减后标记 usage_overview_dirty_scopes /
//     ai_performance_summary_dirty_system_accounts /
//     usage_quota_hourly_window_dirty_scopes 三类派生窗口脏范围
//     （markPostgresDerivedWindowDirtyScopes）；
//   - 授权日报扣减走 resource_authorizations 查找（account 授权 resource_id
//     覆盖与 instance_account 归并），语句同 Node subtractAuthorization*Async。
//
// 语句文本与参数顺序逐字段对照 Node PG 路径；渲染断言见
// statssubtractpostgres_test.go（录制驱动逐字符比对 $n 形态）。

// postgresStatsSubtractParams 照 usage-stats-writer-params.ts
// statsSubtractParams（PG 22 参：duration_ms_count / first_token_ms_count 各
// 出现两次，尾部补 request_count / error_count 供 CASE 条件使用）。
func postgresStatsSubtractParams(stats statsagg.UsageStatsAccumulator) []any {
	return []any{
		stats.RequestCount, stats.SuccessCount, stats.ErrorCount,
		stats.InputTokens, stats.OutputTokens,
		stats.CacheReadTokens, stats.CacheReadCostUsd,
		stats.CacheWriteTokens, stats.CacheWrite1hTokens, stats.CacheWriteCostUsd,
		stats.ThinkingTokens, stats.InputImageTokens, stats.OutputImageTokens,
		stats.TotalCostUsd,
		stats.DurationMsSum, stats.DurationMsCount,
		stats.DurationMsCount,
		stats.FirstTokenMsSum, stats.FirstTokenMsCount,
		stats.FirstTokenMsCount,
		stats.RequestCount, stats.ErrorCount,
	}
}

// postgresMultiRowPlaceholders 照 postgresMultiRowPlaceholders：生成
// `(?, ?), (?, ?)` 形态的多行 VALUES 占位符（经 Bind 改写为 $n）。
func postgresMultiRowPlaceholders(rowCount, columnCount int) string {
	if rowCount <= 0 || columnCount <= 0 {
		return ""
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?, ", columnCount), ", ") + ")"
	rows := make([]string, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		rows = append(rows, row)
	}
	return strings.Join(rows, ", ")
}

// nullableNumberPG 照 nullableNumber：非有限数值归一为 NULL。
func nullableNumberPG(value *float64) *float64 {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	return value
}

// normalizePostgresUsageStatsRecordRow 照 normalizePostgresUsageStatsRecordRow：
// created_at 规范化为毫秒精度 RFC3339，数值列做非有限归一。
func normalizePostgresUsageStatsRecordRow(row statsagg.UsageStatsRecordRow) (statsagg.UsageStatsRecordRow, error) {
	createdAt, err := statsagg.RequiredRFC3339Instant(row.CreatedAt, "使用记录 created_at")
	if err != nil {
		return statsagg.UsageStatsRecordRow{}, err
	}
	normalized := row
	normalized.CreatedAt = createdAt
	normalized.StatusCode = nullableNumberPG(row.StatusCode)
	normalized.Success = row.Success
	normalized.FirstTokenMs = nullableNumberPG(row.FirstTokenMs)
	normalized.DurationMs = nullableNumberPG(row.DurationMs)
	normalized.InputTokens = nullableNumberPG(row.InputTokens)
	normalized.OutputTokens = nullableNumberPG(row.OutputTokens)
	normalized.CacheReadTokens = nullableNumberPG(row.CacheReadTokens)
	normalized.CacheReadCostUsd = nullableNumberPG(row.CacheReadCostUsd)
	normalized.CacheWriteTokens = nullableNumberPG(row.CacheWriteTokens)
	normalized.CacheWrite1hTokens = nullableNumberPG(row.CacheWrite1hTokens)
	normalized.CacheWriteCostUsd = nullableNumberPG(row.CacheWriteCostUsd)
	normalized.ThinkingTokens = nullableNumberPG(row.ThinkingTokens)
	normalized.InputImageTokens = nullableNumberPG(row.InputImageTokens)
	normalized.OutputImageTokens = nullableNumberPG(row.OutputImageTokens)
	normalized.CostUsd = nullableNumberPG(row.CostUsd)
	return normalized, nil
}

// applyPostgresEstimatedCacheReadCost 照 applyPostgresEstimatedCacheReadCost：
// cache_read_cost_usd 缺失时按定价域估算，仅当 > 0 时回填（estimator 为 nil
// 时与 Node 估算返回 undefined 同语义：不回填）。
func applyPostgresEstimatedCacheReadCost(row *statsagg.UsageStatsRecordRow, estimator statsagg.CacheReadCostEstimator) {
	if row.CacheReadCostUsd != nil {
		return
	}
	if estimator == nil {
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
	cost, ok := estimator(providerCode, model, orZeroFloat(row.CacheReadTokens))
	if !ok || cost <= 0 {
		return
	}
	value := cost
	row.CacheReadCostUsd = &value
}

func orZeroFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// ---- 聚合条目（Node PostgresAggregated*Entry）----

type postgresStatsEntryKey struct {
	systemAccountID string
	scopeType       string
	scopeID         string
}

type postgresAggregatedStatsEntry struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	accumulator     statsagg.UsageStatsAccumulator
}

type postgresAggregatedTimeEntry struct {
	bucket          statsagg.TimeBucketDefinition
	timeValue       string
	systemAccountID string
	scopeType       string
	scopeID         string
	accumulator     statsagg.UsageStatsAccumulator
}

type postgresAggregatedModelEntry struct {
	bucket          statsagg.TimeBucketDefinition
	systemAccountID string
	providerCode    string
	model           string
	timeValue       string
	accumulator     statsagg.UsageStatsAccumulator
}

type postgresAggregatedErrorEntry struct {
	bucket          statsagg.TimeBucketDefinition
	systemAccountID string
	timeValue       string
	errorGroup      string
	providerCode    string
	errorCode       string
	statusCode      float64
	requestCount    float64
	errorCount      float64
}

// postgresAggregatedAccountQualityEntry 只聚合扣减 SQL 消费的计数字段
// （Node 条目的 systemAccountId/providerCode/last* 字段不进入任何扣减语句）。
type postgresAggregatedAccountQualityEntry struct {
	accountID         string
	statMinute        string
	requestCount      float64
	successCount      float64
	errorCount        float64
	firstTokenMsSum   float64
	firstTokenMsCount float64
}

type postgresAccountHealthRecordKey struct {
	accountID string
	recordID  string
}

// orderedEntryMap 保持 Node Map 的首次插入迭代顺序（Go map 无序，
// 扣减语句序列必须确定）。
type orderedEntryMap[K comparable, V any] struct {
	keys   []K
	values map[K]V
}

func (m *orderedEntryMap[K, V]) get(key K) (V, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *orderedEntryMap[K, V]) set(key K, value V) {
	if m.values == nil {
		m.values = map[K]V{}
	}
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *orderedEntryMap[K, V]) list() []V {
	output := make([]V, 0, len(m.keys))
	for _, key := range m.keys {
		output = append(output, m.values[key])
	}
	return output
}

type postgresTimeEntryKey struct {
	tableName       string
	timeValue       string
	systemAccountID string
	scopeType       string
	scopeID         string
}

type postgresModelEntryKey struct {
	tableName       string
	timeValue       string
	systemAccountID string
	providerCode    string
	model           string
}

type postgresErrorEntryKey struct {
	tableName       string
	timeValue       string
	systemAccountID string
	errorGroup      string
	providerCode    string
	errorCode       string
	statusCode      float64
}

type postgresQualityEntryKey struct {
	accountID  string
	statMinute string
}

type postgresLatencyEntryKey struct {
	tableName       string
	timeValue       string
	systemAccountID string
	scopeType       string
	scopeID         string
	metricType      string
	upperBound      int
}

// postgresSubtractState 承载本批行的维度聚合（insertion-ordered）。
type postgresSubtractState struct {
	totals      orderedEntryMap[postgresStatsEntryKey, *postgresAggregatedStatsEntry]
	timeBuckets orderedEntryMap[postgresTimeEntryKey, *postgresAggregatedTimeEntry]
	latency     orderedEntryMap[postgresLatencyEntryKey, *statsagg.AggregatedLatencyEntry]
	models      orderedEntryMap[postgresModelEntryKey, *postgresAggregatedModelEntry]
	errors      orderedEntryMap[postgresErrorEntryKey, *postgresAggregatedErrorEntry]
	quality     orderedEntryMap[postgresQualityEntryKey, *postgresAggregatedAccountQualityEntry]
	health      orderedEntryMap[postgresAccountHealthRecordKey, postgresAccountHealthRecordKey]
}

func (state *postgresSubtractState) addStatsEntry(target *orderedEntryMap[postgresStatsEntryKey, *postgresAggregatedStatsEntry], entry statsagg.UsageStatsEntry) {
	key := postgresStatsEntryKey{entry.SystemAccountID, entry.ScopeType, entry.ScopeID}
	if existing, ok := target.get(key); ok {
		_ = statsagg.MergeAccumulator(&existing.accumulator, entry.Accumulator)
		return
	}
	accumulator := entry.Accumulator
	target.set(key, &postgresAggregatedStatsEntry{
		systemAccountID: entry.SystemAccountID,
		scopeType:       entry.ScopeType,
		scopeID:         entry.ScopeID,
		accumulator:     accumulator,
	})
}

func (state *postgresSubtractState) addTimeEntry(bucket statsagg.TimeBucketDefinition, timeValue string, entry statsagg.UsageStatsEntry) {
	key := postgresTimeEntryKey{bucket.TableName, timeValue, entry.SystemAccountID, entry.ScopeType, entry.ScopeID}
	if existing, ok := state.timeBuckets.get(key); ok {
		_ = statsagg.MergeAccumulator(&existing.accumulator, entry.Accumulator)
		return
	}
	accumulator := entry.Accumulator
	state.timeBuckets.set(key, &postgresAggregatedTimeEntry{
		bucket:          bucket,
		timeValue:       timeValue,
		systemAccountID: entry.SystemAccountID,
		scopeType:       entry.ScopeType,
		scopeID:         entry.ScopeID,
		accumulator:     accumulator,
	})
}

func (state *postgresSubtractState) addLatencyEntries(entry statsagg.UsageStatsEntry, row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys) {
	// Node addAggregatedLatencyEntries：duration_ms / first_token_ms 各产生
	// 一个有限非负样本，逐 latency 桶展开。
	type latencySample struct {
		metricType string
		upperBound int
	}
	var samples []latencySample
	if row.DurationMs != nil && !math.IsNaN(*row.DurationMs) && !math.IsInf(*row.DurationMs, 0) && *row.DurationMs >= 0 {
		samples = append(samples, latencySample{"duration_ms", statsagg.LatencyBucketUpperBound(*row.DurationMs)})
	}
	if row.FirstTokenMs != nil && !math.IsNaN(*row.FirstTokenMs) && !math.IsInf(*row.FirstTokenMs, 0) && *row.FirstTokenMs >= 0 {
		samples = append(samples, latencySample{"first_token_ms", statsagg.LatencyBucketUpperBound(*row.FirstTokenMs)})
	}
	for _, sample := range samples {
		for _, bucket := range usageLatencyBucketDefs {
			timeValue := timeKeyValue(timeKeys, bucket.ValueKey)
			key := postgresLatencyEntryKey{
				tableName:       bucket.TableName,
				timeValue:       timeValue,
				systemAccountID: entry.SystemAccountID,
				scopeType:       entry.ScopeType,
				scopeID:         entry.ScopeID,
				metricType:      sample.metricType,
				upperBound:      sample.upperBound,
			}
			if existing, ok := state.latency.get(key); ok {
				existing.SampleCount += 1
				continue
			}
			state.latency.set(key, &statsagg.AggregatedLatencyEntry{
				Bucket:             bucket,
				SystemAccountID:    entry.SystemAccountID,
				ScopeType:          entry.ScopeType,
				ScopeID:            entry.ScopeID,
				MetricType:         statsagg.LatencyMetricType(sample.metricType),
				TimeValue:          timeValue,
				BucketUpperBoundMs: sample.upperBound,
				SampleCount:        1,
			})
		}
	}
}

func (state *postgresSubtractState) addModelEntries(row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys) {
	model := ""
	if row.Model != nil {
		model = strings.TrimSpace(*row.Model)
	}
	if model == "" {
		return
	}
	accumulator := statsagg.UsageStatsAccumulatorFromRecord(row)
	providerCode := "unknown"
	if row.ProviderCode != nil {
		providerCode = *row.ProviderCode
	}
	for _, systemAccountID := range []string{row.SystemAccountID, statsagg.GlobalStatsSystemAccountID} {
		for _, bucket := range usageModelBucketDefs {
			timeValue := timeKeyValue(timeKeys, bucket.ValueKey)
			key := postgresModelEntryKey{bucket.TableName, timeValue, systemAccountID, providerCode, model}
			if existing, ok := state.models.get(key); ok {
				_ = statsagg.MergeAccumulator(&existing.accumulator, accumulator)
				continue
			}
			merged := accumulator
			state.models.set(key, &postgresAggregatedModelEntry{
				bucket:          bucket,
				systemAccountID: systemAccountID,
				providerCode:    providerCode,
				model:           model,
				timeValue:       timeValue,
				accumulator:     merged,
			})
		}
	}
}

func (state *postgresSubtractState) addErrorEntries(row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys) {
	errorGroup := "unknown"
	if row.ProviderCode != nil {
		errorGroup = *row.ProviderCode
	}
	providerCode := errorGroup
	errorCode := "unknown"
	if row.ErrorCode != nil {
		errorCode = *row.ErrorCode
	} else if row.StatusCode != nil {
		errorCode = trimFloatStatus(*row.StatusCode)
	}
	statusCode := 0.0
	if row.StatusCode != nil {
		statusCode = *row.StatusCode
	}
	for _, systemAccountID := range []string{row.SystemAccountID, statsagg.GlobalStatsSystemAccountID} {
		for _, bucket := range usageErrorBucketDefs {
			timeValue := timeKeyValue(timeKeys, bucket.ValueKey)
			key := postgresErrorEntryKey{bucket.TableName, timeValue, systemAccountID, errorGroup, providerCode, errorCode, statusCode}
			if existing, ok := state.errors.get(key); ok {
				existing.requestCount += 1
				existing.errorCount += 1
				continue
			}
			state.errors.set(key, &postgresAggregatedErrorEntry{
				bucket:          bucket,
				systemAccountID: systemAccountID,
				timeValue:       timeValue,
				errorGroup:      errorGroup,
				providerCode:    providerCode,
				errorCode:       errorCode,
				statusCode:      statusCode,
				requestCount:    1,
				errorCount:      1,
			})
		}
	}
}

// trimFloatStatus 照 Node String(row.status_code ?? 'unknown') 的数值字符串化
// （整数值不带小数点）。
func trimFloatStatus(value float64) string {
	if value == math.Trunc(value) && !math.IsInf(value, 0) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%v", value)
}

func (state *postgresSubtractState) addQualityEntry(row statsagg.UsageStatsRecordRow, timeKeys statsagg.UsageStatsTimeKeys) {
	if !shouldRecordAccountQualityStats(row) || row.AccountID == nil || row.APIKeyID == nil {
		return
	}
	success := row.Success == 1
	firstTokenMs := 0.0
	firstTokenCount := 0.0
	if success && row.FirstTokenMs != nil && !math.IsNaN(*row.FirstTokenMs) && !math.IsInf(*row.FirstTokenMs, 0) && *row.FirstTokenMs >= 0 {
		firstTokenMs = *row.FirstTokenMs
		firstTokenCount = 1
	}
	key := postgresQualityEntryKey{*row.AccountID, timeKeys.StatMinute}
	if existing, ok := state.quality.get(key); ok {
		existing.requestCount += 1
		if success {
			existing.successCount += 1
		} else {
			existing.errorCount += 1
		}
		existing.firstTokenMsSum += firstTokenMs
		existing.firstTokenMsCount += firstTokenCount
		return
	}
	successCount := 0.0
	errorCount := 0.0
	if success {
		successCount = 1
	} else {
		errorCount = 1
	}
	state.quality.set(key, &postgresAggregatedAccountQualityEntry{
		accountID:         *row.AccountID,
		statMinute:        timeKeys.StatMinute,
		requestCount:      1,
		successCount:      successCount,
		errorCount:        errorCount,
		firstTokenMsSum:   firstTokenMs,
		firstTokenMsCount: firstTokenCount,
	})
}

// ---- 授权查找（createPostgresUsageStatsAuthorizationLookup）----

func postgresAuthorizationIDs(rows []statsagg.UsageStatsRecordRow) []string {
	seen := map[string]bool{}
	var ids []string
	for _, row := range rows {
		if row.AccountAuthorizationID == nil {
			continue
		}
		normalized := strings.TrimSpace(*row.AccountAuthorizationID)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		ids = append(ids, normalized)
	}
	return ids
}

// createPostgresUsageStatsAuthorizationLookup 照
// createPostgresUsageStatsAuthorizationLookup（juhe_business 授权查找，
// 900 一批 ANY 查询）。business 句柄缺失时报错（fail closed）。
func (s *RecordCleanupStore) createPostgresUsageStatsAuthorizationLookup(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, rows []statsagg.UsageStatsRecordRow) (*statsagg.AuthorizationLookup, error) {
	lookup := &statsagg.AuthorizationLookup{
		AccountAuthorizationInstanceAccountIDs: map[string]string{},
	}
	ids := postgresAuthorizationIDs(rows)
	if len(ids) == 0 {
		return lookup, nil
	}
	if s.Business == nil {
		return nil, fmt.Errorf("PostgreSQL 统计扣减链缺少 juhe_business 句柄（resource_authorizations 授权查找）")
	}
	query := s.Business.Bind(`
      SELECT
        authorizations.id,
        authorizations.resource_id,
        instance_accounts.id AS instance_account_id
      FROM juhe_business.resource_authorizations authorizations
      LEFT JOIN juhe_business.accounts instance_accounts
        ON instance_accounts.authorization_instance_authorization_id = authorizations.id
        AND instance_accounts.system_account_id = authorizations.grantee_system_account_id
      WHERE authorizations.resource_type = 'account'
        AND authorizations.id = ANY(?::text[])
	`)
	for _, chunk := range chunkValues(ids, 900) {
		dbRows, err := queryer.QueryContext(ctx, query, chunk)
		if err != nil {
			return nil, err
		}
		func() {
			defer dbRows.Close()
			for dbRows.Next() {
				var authID, resourceID, instanceAccountID sql.NullString
				if err := dbRows.Scan(&authID, &resourceID, &instanceAccountID); err != nil {
					return
				}
				if authID.Valid && resourceID.Valid && resourceID.String != "" {
					if lookup.AccountAuthorizationResourceIDs == nil {
						lookup.AccountAuthorizationResourceIDs = map[string]string{}
					}
					lookup.AccountAuthorizationResourceIDs[authID.String] = resourceID.String
				}
				if authID.Valid && instanceAccountID.Valid && instanceAccountID.String != "" {
					lookup.AccountAuthorizationInstanceAccountIDs[authID.String] = instanceAccountID.String
				}
			}
		}()
	}
	return lookup, nil
}

// ---- subtractPostgresUsageStatsRows 主入口 ----

// subtractPostgresUsageStatsRows 照 subtractPostgresUsageStatsRows：
// 先聚合后扣减；timezone 为空时回落 store 时区来源（Node
// usageStatsTimezoneAsync 的组合根等价）。
func (s *RecordCleanupStore) subtractPostgresUsageStatsRows(ctx context.Context, tx *sql.Tx, inputRows []statsagg.UsageStatsRecordRow, updatedAt string, timezone *time.Location) error {
	normalizedUpdatedAt, err := statsagg.RequiredRFC3339Instant(updatedAt, "用量统计 updatedAt")
	if err != nil {
		return err
	}
	if len(inputRows) == 0 {
		return nil
	}
	rows := make([]statsagg.UsageStatsRecordRow, 0, len(inputRows))
	for _, row := range inputRows {
		normalized, err := normalizePostgresUsageStatsRecordRow(row)
		if err != nil {
			return err
		}
		rows = append(rows, normalized)
	}
	lookup, err := s.createPostgresUsageStatsAuthorizationLookup(ctx, tx, rows)
	if err != nil {
		return err
	}
	location := timezone
	if location == nil {
		if s.Timezone == nil {
			return fmt.Errorf("PostgreSQL 统计扣减链缺少统计时区来源")
		}
		location, err = s.Timezone(ctx)
		if err != nil {
			return err
		}
	}

	state := &postgresSubtractState{}
	for i := range rows {
		if !statsagg.ShouldAggregateUsageStatsRecord(rows[i]) {
			continue
		}
		applyPostgresEstimatedCacheReadCost(&rows[i], s.CacheReadCostEstimator)
		timeKeys, err := statsagg.UsageStatsTimeKeysFor(rows[i].CreatedAt, location)
		if err != nil {
			return err
		}
		for _, entry := range statsagg.UsageStatsEntries(rows[i], lookup) {
			state.addStatsEntry(&state.totals, entry)
			for _, bucket := range usageStatsBucketDefs {
				state.addTimeEntry(bucket, timeKeyValue(timeKeys, bucket.ValueKey), entry)
			}
			state.addLatencyEntries(entry, rows[i], timeKeys)
		}
		state.addModelEntries(rows[i], timeKeys)
		state.addQualityEntry(rows[i], timeKeys)
		if rows[i].TrafficSource == "account_health_check" && rows[i].AccountID != nil {
			state.health.set(postgresAccountHealthRecordKey{*rows[i].AccountID, rows[i].ID},
				postgresAccountHealthRecordKey{*rows[i].AccountID, rows[i].ID})
		}
		if textSet(rows[i].AccountAuthorizationID) || textSet(rows[i].GroupAuthorizationID) {
			if err := s.subtractAuthorizationUsageReportRowsPostgres(ctx, tx, rows[i], timeKeys.StatDate, normalizedUpdatedAt, lookup); err != nil {
				return err
			}
		}
		if rows[i].Success != 1 {
			state.addErrorEntries(rows[i], timeKeys)
		}
	}

	if err := s.subtractPostgresUsageStatsTotals(ctx, tx, state.totals.list(), normalizedUpdatedAt); err != nil {
		return err
	}
	for _, bucket := range usageStatsBucketDefs {
		entries := make([]*postgresAggregatedTimeEntry, 0)
		for _, entry := range state.timeBuckets.list() {
			if entry.bucket.TableName == bucket.TableName {
				entries = append(entries, entry)
			}
		}
		if err := s.subtractPostgresUsageStatsTimeBucket(ctx, tx, bucket, entries, normalizedUpdatedAt); err != nil {
			return err
		}
	}
	if err := s.subtractPostgresUsageLatencyEntries(ctx, tx, state.latency.list(), normalizedUpdatedAt); err != nil {
		return err
	}
	if err := s.subtractPostgresUsageModelEntries(ctx, tx, state.models.list(), normalizedUpdatedAt); err != nil {
		return err
	}
	if err := s.subtractPostgresUsageErrorEntries(ctx, tx, state.errors.list(), normalizedUpdatedAt); err != nil {
		return err
	}
	if err := s.subtractPostgresAccountQualityEntries(ctx, tx, state.quality.list(), normalizedUpdatedAt); err != nil {
		return err
	}
	if err := s.deletePostgresAccountHealthRecords(ctx, tx, state.health.list()); err != nil {
		return err
	}
	return s.markPostgresDerivedWindowDirtyScopes(ctx, tx, state.timeBuckets.list(), normalizedUpdatedAt)
}

func textSet(value *string) bool {
	return value != nil && *value != ""
}

// ---- 扣减语句家族 ----

func (s *RecordCleanupStore) subtractPostgresUsageStatsTotals(ctx context.Context, tx *sql.Tx, entries []*postgresAggregatedStatsEntry, updatedAt string) error {
	for _, entry := range entries {
		_, err := tx.ExecContext(ctx, s.Stats.Bind(`
      UPDATE juhe_stats.usage_stats_totals
      SET request_count = GREATEST(0, request_count - ?),
          success_count = GREATEST(0, success_count - ?),
          error_count = GREATEST(0, error_count - ?),
          input_tokens = GREATEST(0, input_tokens - ?),
          output_tokens = GREATEST(0, output_tokens - ?),
          cache_read_tokens = GREATEST(0, cache_read_tokens - ?),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - ?),
          cache_write_tokens = GREATEST(0, cache_write_tokens - ?),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - ?),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - ?),
          thinking_tokens = GREATEST(0, thinking_tokens - ?),
          input_image_tokens = GREATEST(0, input_image_tokens - ?),
          output_image_tokens = GREATEST(0, output_image_tokens - ?),
          total_cost_usd = GREATEST(0, total_cost_usd - ?),
          duration_ms_sum = GREATEST(0, duration_ms_sum - ?),
          duration_ms_count = GREATEST(0, duration_ms_count - ?),
          duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - ?),
          first_token_ms_count = GREATEST(0, first_token_ms_count - ?),
          first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
          updated_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `), append(postgresStatsSubtractParams(entry.accumulator), updatedAt,
			entry.systemAccountID, entry.scopeType, entry.scopeID)...)
		if err != nil {
			return err
		}
		if err := s.deleteEmptyPostgresUsageStatsTotal(ctx, tx, entry.systemAccountID, entry.scopeType, entry.scopeID); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deleteEmptyPostgresUsageStatsTotal(ctx context.Context, tx *sql.Tx, systemAccountID, scopeType, scopeID string) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(`
    DELETE FROM juhe_stats.usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `), systemAccountID, scopeType, scopeID)
	return err
}

func (s *RecordCleanupStore) subtractPostgresUsageStatsTimeBucket(ctx context.Context, tx *sql.Tx, bucket statsagg.TimeBucketDefinition, entries []*postgresAggregatedTimeEntry, updatedAt string) error {
	for _, entry := range entries {
		_, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      UPDATE juhe_stats.%s
      SET request_count = GREATEST(0, request_count - ?),
          success_count = GREATEST(0, success_count - ?),
          error_count = GREATEST(0, error_count - ?),
          input_tokens = GREATEST(0, input_tokens - ?),
          output_tokens = GREATEST(0, output_tokens - ?),
          cache_read_tokens = GREATEST(0, cache_read_tokens - ?),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - ?),
          cache_write_tokens = GREATEST(0, cache_write_tokens - ?),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - ?),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - ?),
          thinking_tokens = GREATEST(0, thinking_tokens - ?),
          input_image_tokens = GREATEST(0, input_image_tokens - ?),
          output_image_tokens = GREATEST(0, output_image_tokens - ?),
          total_cost_usd = GREATEST(0, total_cost_usd - ?),
          duration_ms_sum = GREATEST(0, duration_ms_sum - ?),
          duration_ms_count = GREATEST(0, duration_ms_count - ?),
          duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - ?),
          first_token_ms_count = GREATEST(0, first_token_ms_count - ?),
          first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
          updated_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND %s = ?
    `, bucket.TableName, bucket.ColumnName)),
			append(postgresStatsSubtractParams(entry.accumulator), updatedAt,
				entry.systemAccountID, entry.scopeType, entry.scopeID, entry.timeValue)...)
		if err != nil {
			return err
		}
		if err := s.deleteEmptyPostgresUsageStatsTimeBucket(ctx, tx, bucket, entry.timeValue, entry.systemAccountID, entry.scopeType, entry.scopeID); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deleteEmptyPostgresUsageStatsTimeBucket(ctx context.Context, tx *sql.Tx, bucket statsagg.TimeBucketDefinition, timeValue, systemAccountID, scopeType, scopeID string) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
    DELETE FROM juhe_stats.%s
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND %s = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `, bucket.TableName, bucket.ColumnName)), systemAccountID, scopeType, scopeID, timeValue)
	return err
}

func (s *RecordCleanupStore) subtractPostgresUsageLatencyEntries(ctx context.Context, tx *sql.Tx, entries []*statsagg.AggregatedLatencyEntry, updatedAt string) error {
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      UPDATE juhe_stats.%s
      SET sample_count = GREATEST(0, sample_count - ?),
          updated_at = ?
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
        AND metric_type = ? AND %s = ? AND bucket_upper_bound_ms = ?
    `, entry.Bucket.TableName, entry.Bucket.ColumnName)),
			entry.SampleCount, updatedAt,
			entry.SystemAccountID, entry.ScopeType, entry.ScopeID,
			string(entry.MetricType), entry.TimeValue, entry.BucketUpperBoundMs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      DELETE FROM juhe_stats.%s
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
        AND metric_type = ? AND %s = ? AND bucket_upper_bound_ms = ?
        AND sample_count = 0
    `, entry.Bucket.TableName, entry.Bucket.ColumnName)),
			entry.SystemAccountID, entry.ScopeType, entry.ScopeID,
			string(entry.MetricType), entry.TimeValue, entry.BucketUpperBoundMs); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) subtractPostgresUsageModelEntries(ctx context.Context, tx *sql.Tx, entries []*postgresAggregatedModelEntry, updatedAt string) error {
	for _, entry := range entries {
		stats := entry.accumulator
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      UPDATE juhe_stats.%s
      SET request_count = GREATEST(0, request_count - ?),
          success_count = GREATEST(0, success_count - ?),
          error_count = GREATEST(0, error_count - ?),
          input_tokens = GREATEST(0, input_tokens - ?),
          output_tokens = GREATEST(0, output_tokens - ?),
          cache_read_tokens = GREATEST(0, cache_read_tokens - ?),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - ?),
          cache_write_tokens = GREATEST(0, cache_write_tokens - ?),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - ?),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - ?),
          thinking_tokens = GREATEST(0, thinking_tokens - ?),
          input_image_tokens = GREATEST(0, input_image_tokens - ?),
          output_image_tokens = GREATEST(0, output_image_tokens - ?),
          total_cost_usd = GREATEST(0, total_cost_usd - ?),
          updated_at = ?
      WHERE system_account_id = ? AND %s = ? AND provider_code = ? AND model = ?
    `, entry.bucket.TableName, entry.bucket.ColumnName)),
			stats.RequestCount, stats.SuccessCount, stats.ErrorCount,
			stats.InputTokens, stats.OutputTokens,
			stats.CacheReadTokens, stats.CacheReadCostUsd,
			stats.CacheWriteTokens, stats.CacheWrite1hTokens, stats.CacheWriteCostUsd,
			stats.ThinkingTokens, stats.InputImageTokens, stats.OutputImageTokens,
			stats.TotalCostUsd,
			updatedAt,
			entry.systemAccountID, entry.timeValue, entry.providerCode, entry.model); err != nil {
			return err
		}
		if err := s.deleteEmptyPostgresUsageModelBucket(ctx, tx, entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deleteEmptyPostgresUsageModelBucket(ctx context.Context, tx *sql.Tx, entry *postgresAggregatedModelEntry) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
    DELETE FROM juhe_stats.%s
    WHERE system_account_id = ? AND %s = ? AND provider_code = ? AND model = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `, entry.bucket.TableName, entry.bucket.ColumnName)),
		entry.systemAccountID, entry.timeValue, entry.providerCode, entry.model)
	return err
}

func (s *RecordCleanupStore) subtractPostgresUsageErrorEntries(ctx context.Context, tx *sql.Tx, entries []*postgresAggregatedErrorEntry, updatedAt string) error {
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      UPDATE juhe_stats.%s
      SET request_count = GREATEST(0, request_count - ?),
          error_count = GREATEST(0, error_count - ?),
          updated_at = ?
      WHERE system_account_id = ? AND %s = ? AND error_group = ?
        AND provider_code = ? AND error_code = ? AND status_code = ?
    `, entry.bucket.TableName, entry.bucket.ColumnName)),
			entry.requestCount, entry.errorCount, updatedAt,
			entry.systemAccountID, entry.timeValue, entry.errorGroup,
			entry.providerCode, entry.errorCode, entry.statusCode); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      DELETE FROM juhe_stats.%s
      WHERE system_account_id = ? AND %s = ? AND error_group = ?
        AND provider_code = ? AND error_code = ? AND status_code = ?
        AND request_count = 0 AND error_count = 0
    `, entry.bucket.TableName, entry.bucket.ColumnName)),
			entry.systemAccountID, entry.timeValue, entry.errorGroup,
			entry.providerCode, entry.errorCode, entry.statusCode); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) subtractPostgresAccountQualityEntries(ctx context.Context, tx *sql.Tx, entries []*postgresAggregatedAccountQualityEntry, updatedAt string) error {
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      UPDATE juhe_stats.account_quality_minute_stats
      SET request_count = GREATEST(0, request_count - ?),
          success_count = GREATEST(0, success_count - ?),
          error_count = GREATEST(0, error_count - ?),
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - ?),
          first_token_ms_count = GREATEST(0, first_token_ms_count - ?),
          updated_at = ?
      WHERE account_id = ? AND stat_minute = ?
    `), entry.requestCount, entry.successCount, entry.errorCount,
			entry.firstTokenMsSum, entry.firstTokenMsCount,
			updatedAt, entry.accountID, entry.statMinute); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      DELETE FROM juhe_stats.account_quality_minute_stats
      WHERE account_id = ? AND stat_minute = ?
        AND request_count = 0 AND success_count = 0 AND error_count = 0
        AND first_token_ms_sum = 0 AND first_token_ms_count = 0
    `), entry.accountID, entry.statMinute); err != nil {
			return err
		}
	}
	dirtyAccountIDs := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.accountID == "" || seen[entry.accountID] {
			continue
		}
		seen[entry.accountID] = true
		dirtyAccountIDs = append(dirtyAccountIDs, entry.accountID)
	}
	for _, chunk := range chunkValues(dirtyAccountIDs, 900) {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
      INSERT INTO juhe_stats.account_quality_dirty_accounts (account_id, first_dirty_at, updated_at)
      VALUES %s
      ON CONFLICT(account_id) DO UPDATE SET
        updated_at = EXCLUDED.updated_at
    `, postgresMultiRowPlaceholders(len(chunk), 3))), flattenTriples(chunk, updatedAt, updatedAt)...); err != nil {
			return err
		}
	}
	return nil
}

// flattenTriples 生成 [id, updatedAt, updatedAt, id, updatedAt, updatedAt, ...]
// （Node chunk.flatMap 的等价展开）。
func flattenTriples(ids []string, firstDirtyAt, updatedAt string) []any {
	args := make([]any, 0, len(ids)*3)
	for _, id := range ids {
		args = append(args, id, firstDirtyAt, updatedAt)
	}
	return args
}

func (s *RecordCleanupStore) deletePostgresAccountHealthRecords(ctx context.Context, tx *sql.Tx, entries []postgresAccountHealthRecordKey) error {
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`DELETE FROM juhe_stats.account_health_hourly WHERE account_id = ? AND last_record_id = ?`),
			entry.accountID, entry.recordID); err != nil {
			return err
		}
	}
	return nil
}

// markPostgresDerivedWindowDirtyScopes 照 markPostgresDerivedWindowDirtyScopes：
// overview（system_account 日桶）/ ai-performance（account 日桶）/ quota
// hourly（配额 scope 类型的小时桶）三类脏范围标记，500 一批多行 VALUES。
func (s *RecordCleanupStore) markPostgresDerivedWindowDirtyScopes(ctx context.Context, tx *sql.Tx, entries []*postgresAggregatedTimeEntry, updatedAt string) error {
	type overviewScope struct {
		systemAccountID string
		scopeID         string
		minDate         string
	}
	type aiScope struct {
		systemAccountID string
		minDate         string
		maxDate         string
	}
	overviewScopes := orderedEntryMap[string, *overviewScope]{}
	aiPerformanceScopes := orderedEntryMap[string, *aiScope]{}
	for _, entry := range entries {
		if entry.bucket.TableName != "usage_stats_daily" {
			continue
		}
		if existing, ok := overviewScopes.get(entry.systemAccountID); ok && entry.scopeType == "system_account" {
			existing.scopeID = entry.scopeID
			if entry.timeValue < existing.minDate {
				existing.minDate = entry.timeValue
			}
		} else if entry.scopeType == "system_account" {
			overviewScopes.set(entry.systemAccountID, &overviewScope{
				systemAccountID: entry.systemAccountID,
				scopeID:         entry.scopeID,
				minDate:         entry.timeValue,
			})
		}
		if entry.scopeType == "account" {
			if existing, ok := aiPerformanceScopes.get(entry.systemAccountID); ok {
				if entry.timeValue < existing.minDate {
					existing.minDate = entry.timeValue
				}
				if entry.timeValue > existing.maxDate {
					existing.maxDate = entry.timeValue
				}
			} else {
				aiPerformanceScopes.set(entry.systemAccountID, &aiScope{
					systemAccountID: entry.systemAccountID,
					minDate:         entry.timeValue,
					maxDate:         entry.timeValue,
				})
			}
		}
	}
	for _, scope := range overviewScopes.list() {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      INSERT INTO juhe_stats.usage_overview_dirty_scopes (
        system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id) DO UPDATE SET
        scope_id = EXCLUDED.scope_id,
        min_changed_date = LEAST(usage_overview_dirty_scopes.min_changed_date, EXCLUDED.min_changed_date),
        generation = usage_overview_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `), scope.systemAccountID, scope.scopeID, scope.minDate, 1, updatedAt, updatedAt); err != nil {
			return err
		}
	}
	for _, scope := range aiPerformanceScopes.list() {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (
        system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id) DO UPDATE SET
        min_stat_date = LEAST(ai_performance_summary_dirty_system_accounts.min_stat_date, EXCLUDED.min_stat_date),
        max_stat_date = GREATEST(ai_performance_summary_dirty_system_accounts.max_stat_date, EXCLUDED.max_stat_date),
        generation = ai_performance_summary_dirty_system_accounts.generation + 1,
        updated_at = EXCLUDED.updated_at
    `), scope.systemAccountID, scope.minDate, scope.maxDate, 1, updatedAt, updatedAt); err != nil {
			return err
		}
	}

	quotaScopeTypes := map[string]bool{
		"api_key":                    true,
		"account_authorization":      true,
		"group_authorization":        true,
		"account_authorization_team": true,
		"group_authorization_team":   true,
	}
	quotaScopes := orderedEntryMap[postgresStatsEntryKey, *postgresAggregatedTimeEntry]{}
	for _, entry := range entries {
		if entry.bucket.TableName != "usage_stats_hourly" || !quotaScopeTypes[entry.scopeType] {
			continue
		}
		quotaScopes.set(postgresStatsEntryKey{entry.systemAccountID, entry.scopeType, entry.scopeID}, entry)
	}
	for _, scope := range quotaScopes.list() {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(`
      INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
        system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?)
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `), scope.systemAccountID, scope.scopeType, scope.scopeID, 1, updatedAt, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

// ---- 授权日报扣减（usage-stats-authorization-daily-writer.ts PG 分支）----

type postgresAuthorizationReportRow struct {
	authorizationID string
	owner           string
	grantee         string
	resourceType    string
	resourceID      string
	sourceType      *string
	sourceTeamID    *string
}

type postgresAuthorizationFilter struct {
	resourceFilterType string
	resourceFilterID   string
}

type postgresAuthorizationSummaryKey struct {
	teamFilterID string
	grantee      string
	filter       postgresAuthorizationFilter
}

// subtractAuthorizationUsageReportRowsPostgres 照
// subtractAuthorizationUsageReportRowsAsync（PG 逐行扣减；owner + global 双 scope）。
func (s *RecordCleanupStore) subtractAuthorizationUsageReportRowsPostgres(ctx context.Context, tx *sql.Tx, row statsagg.UsageStatsRecordRow, statDate, updatedAt string, lookup *statsagg.AuthorizationLookup) error {
	stats := statsagg.UsageStatsAccumulatorFromRecord(row)
	for _, reportRow := range postgresAuthorizationReportRows(row, lookup) {
		scopeRows := []postgresAuthorizationReportRow{reportRow}
		if reportRow.owner != statsagg.GlobalStatsSystemAccountID {
			globalRow := reportRow
			globalRow.owner = statsagg.GlobalStatsSystemAccountID
			scopeRows = append(scopeRows, globalRow)
		}
		filters := []postgresAuthorizationFilter{
			{resourceFilterType: "all", resourceFilterID: ""},
			{resourceFilterType: reportRow.resourceType, resourceFilterID: ""},
			{resourceFilterType: reportRow.resourceType, resourceFilterID: reportRow.resourceID},
		}
		for _, scopedReportRow := range scopeRows {
			if err := s.subtractAuthorizationSummaryRowsPostgres(ctx, tx, scopedReportRow, filters, stats, statDate, updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// postgresAuthorizationReportRows 照 authorizationReportRows(row, context)：
// account 授权 resource_id 覆盖走 lookup，group 授权 resource_id 取 group_id，
// 按 authorizationId 去重。
func postgresAuthorizationReportRows(row statsagg.UsageStatsRecordRow, lookup *statsagg.AuthorizationLookup) []postgresAuthorizationReportRow {
	var rows []postgresAuthorizationReportRow
	seen := map[string]bool{}
	add := func(row postgresAuthorizationReportRow) {
		if seen[row.authorizationID] {
			return
		}
		seen[row.authorizationID] = true
		rows = append(rows, row)
	}
	if textSet(row.AccountAuthorizationID) && textSet(row.AccountID) && textSet(row.AccountOwnerSystemAccountID) &&
		*row.AccountOwnerSystemAccountID != row.SystemAccountID {
		resourceID := *row.AccountID
		if lookup != nil {
			if covered, ok := lookup.AccountAuthorizationResourceIDs[*row.AccountAuthorizationID]; ok {
				resourceID = covered
			}
		}
		add(postgresAuthorizationReportRow{
			authorizationID: "account:" + *row.AccountAuthorizationID,
			owner:           *row.AccountOwnerSystemAccountID,
			grantee:         row.SystemAccountID,
			resourceType:    "account",
			resourceID:      resourceID,
			sourceType:      row.AccountAuthorizationSourceType,
			sourceTeamID:    row.AccountAuthorizationSourceTeamID,
		})
	}
	if textSet(row.GroupAuthorizationID) && textSet(row.GroupID) && textSet(row.GroupOwnerSystemAccountID) &&
		*row.GroupOwnerSystemAccountID != row.SystemAccountID {
		add(postgresAuthorizationReportRow{
			authorizationID: "group:" + *row.GroupAuthorizationID,
			owner:           *row.GroupOwnerSystemAccountID,
			grantee:         row.SystemAccountID,
			resourceType:    "group",
			resourceID:      *row.GroupID,
			sourceType:      row.GroupAuthorizationSourceType,
			sourceTeamID:    row.GroupAuthorizationSourceTeamID,
		})
	}
	return rows
}

// subtractAuthorizationSummaryRowsPostgres 照 subtractAuthorizationSummaryRowsAsync：
// 先聚合 team/user 过滤键，再先 team 后 user 逐键扣减 + 空行清理。
func (s *RecordCleanupStore) subtractAuthorizationSummaryRowsPostgres(ctx context.Context, tx *sql.Tx, reportRow postgresAuthorizationReportRow, filters []postgresAuthorizationFilter, stats statsagg.UsageStatsAccumulator, statDate, updatedAt string) error {
	var userSummaryKeys, teamSummaryKeys []postgresAuthorizationSummaryKey
	for _, filter := range filters {
		userSummaryKeys = append(userSummaryKeys,
			postgresAuthorizationSummaryKey{teamFilterID: "", grantee: "", filter: filter},
			postgresAuthorizationSummaryKey{teamFilterID: "", grantee: reportRow.grantee, filter: filter})
		if reportRow.sourceType != nil && *reportRow.sourceType == "team" && reportRow.sourceTeamID != nil {
			teamSummaryKeys = append(teamSummaryKeys,
				postgresAuthorizationSummaryKey{teamFilterID: "", filter: filter},
				postgresAuthorizationSummaryKey{teamFilterID: *reportRow.sourceTeamID, filter: filter})
			userSummaryKeys = append(userSummaryKeys,
				postgresAuthorizationSummaryKey{teamFilterID: *reportRow.sourceTeamID, grantee: "", filter: filter},
				postgresAuthorizationSummaryKey{teamFilterID: *reportRow.sourceTeamID, grantee: reportRow.grantee, filter: filter})
		}
	}
	for _, key := range teamSummaryKeys {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
    UPDATE juhe_stats.authorization_team_usage_summary_daily
    SET request_count = GREATEST(0, request_count - ?),
        success_count = GREATEST(0, success_count - ?),
        error_count = GREATEST(0, error_count - ?),
        input_tokens = GREATEST(0, input_tokens - ?),
        output_tokens = GREATEST(0, output_tokens - ?),
        cache_read_tokens = GREATEST(0, cache_read_tokens - ?),
        cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - ?),
        cache_write_tokens = GREATEST(0, cache_write_tokens - ?),
        cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - ?),
        cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - ?),
        thinking_tokens = GREATEST(0, thinking_tokens - ?),
        input_image_tokens = GREATEST(0, input_image_tokens - ?),
        output_image_tokens = GREATEST(0, output_image_tokens - ?),
        total_cost_usd = GREATEST(0, total_cost_usd - ?),
        duration_ms_sum = GREATEST(0, duration_ms_sum - ?),
        duration_ms_count = GREATEST(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = GREATEST(0, first_token_ms_sum - ?),
        first_token_ms_count = GREATEST(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `)), append(postgresStatsSubtractParams(stats), updatedAt,
			reportRow.owner, statDate, key.teamFilterID, key.filter.resourceFilterType, key.filter.resourceFilterID)...); err != nil {
			return err
		}
		if err := s.deleteEmptyPostgresAuthorizationTeamSummaryRow(ctx, tx, reportRow.owner, statDate, key); err != nil {
			return err
		}
	}
	for _, key := range userSummaryKeys {
		if _, err := tx.ExecContext(ctx, s.Stats.Bind(fmt.Sprintf(`
    UPDATE juhe_stats.authorization_user_usage_summary_daily
    SET request_count = GREATEST(0, request_count - ?),
        success_count = GREATEST(0, success_count - ?),
        error_count = GREATEST(0, error_count - ?),
        input_tokens = GREATEST(0, input_tokens - ?),
        output_tokens = GREATEST(0, output_tokens - ?),
        cache_read_tokens = GREATEST(0, cache_read_tokens - ?),
        cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - ?),
        cache_write_tokens = GREATEST(0, cache_write_tokens - ?),
        cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - ?),
        cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - ?),
        thinking_tokens = GREATEST(0, thinking_tokens - ?),
        input_image_tokens = GREATEST(0, input_image_tokens - ?),
        output_image_tokens = GREATEST(0, output_image_tokens - ?),
        total_cost_usd = GREATEST(0, total_cost_usd - ?),
        duration_ms_sum = GREATEST(0, duration_ms_sum - ?),
        duration_ms_count = GREATEST(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = GREATEST(0, first_token_ms_sum - ?),
        first_token_ms_count = GREATEST(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `)), append(postgresStatsSubtractParams(stats), updatedAt,
			reportRow.owner, statDate, key.teamFilterID, key.grantee, key.filter.resourceFilterType, key.filter.resourceFilterID)...); err != nil {
			return err
		}
		if err := s.deleteEmptyPostgresAuthorizationUserSummaryRow(ctx, tx, reportRow.owner, statDate, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *RecordCleanupStore) deleteEmptyPostgresAuthorizationTeamSummaryRow(ctx context.Context, tx *sql.Tx, systemAccountID, statDate string, key postgresAuthorizationSummaryKey) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(`
    DELETE FROM juhe_stats.authorization_team_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `), systemAccountID, statDate, key.teamFilterID, key.filter.resourceFilterType, key.filter.resourceFilterID)
	return err
}

func (s *RecordCleanupStore) deleteEmptyPostgresAuthorizationUserSummaryRow(ctx context.Context, tx *sql.Tx, systemAccountID, statDate string, key postgresAuthorizationSummaryKey) error {
	_, err := tx.ExecContext(ctx, s.Stats.Bind(`
    DELETE FROM juhe_stats.authorization_user_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `), systemAccountID, statDate, key.teamFilterID, key.grantee, key.filter.resourceFilterType, key.filter.resourceFilterID)
	return err
}
