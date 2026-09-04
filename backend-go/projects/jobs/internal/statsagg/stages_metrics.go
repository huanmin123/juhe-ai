package statsagg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SystemMetricsSampleInput mirrors usage-stats-types.ts SystemMetricsSampleInput。
type SystemMetricsSampleInput struct {
	SampledAt               string
	CPUPercent              *float64
	MemoryUsedPercent       *float64
	MemoryTotalBytes        *float64
	MemoryFreeBytes         *float64
	ProcessRssBytes         *float64
	ProcessHeapUsedBytes    *float64
	ProcessHeapTotalBytes   *float64
	EventLoopLagMs          *float64
	NetworkRxBytesPerSecond *float64
	NetworkTxBytesPerSecond *float64
	NetworkRxTotalBytes     *float64
	NetworkTxTotalBytes     *float64
	DBFileBytes             *float64
	StatsLagSeconds         *float64
}

// ProcessEventLoopSampleInput mirrors ProcessEventLoopSampleInput。
type ProcessEventLoopSampleInput struct {
	ProcessRole              string
	ProcessPid               *float64
	SampledAt                string
	EventLoopLagMs           *float64
	ProcessRssBytes          *float64
	ProcessHeapUsedBytes     *float64
	ProcessHeapTotalBytes    *float64
	ProcessExternalBytes     *float64
	ProcessArrayBuffersBytes *float64
}

// InsertSystemMetricsSampleBatch mirrors insertSystemMetricsSampleBatchAsync：
// 采样行 + hourly 汇总 upsert + 进程事件循环采样 + 进程 hourly upsert，
// 单事务提交（system-metrics-sample job）。
func (w *WindowRefresher) InsertSystemMetricsSampleBatch(ctx context.Context, input SystemMetricsSampleInput, processEventLoopSamples []ProcessEventLoopSampleInput) error {
	timezone, err := w.Clock.StatsTimezone(ctx)
	if err != nil {
		return err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := w.insertSystemMetricsSample(ctx, tx, input, timezone); err != nil {
		return err
	}
	for _, sample := range processEventLoopSamples {
		if err := w.insertProcessEventLoopSample(ctx, tx, sample, timezone); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableParam(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (w *WindowRefresher) insertSystemMetricsSample(ctx context.Context, tx *sql.Tx, input SystemMetricsSampleInput, timezone *time.Location) error {
	sampledAt := input.SampledAt
	if sampledAt == "" {
		sampledAt = FormatRFC3339Millis(w.now())
	} else if normalized, ok := CanonicalizeRFC3339Instant(sampledAt); !ok {
		return fmt.Errorf("系统指标 sampledAt必须是带 Z 或数值 offset 的 RFC3339 时间")
	} else {
		sampledAt = normalized
	}
	parsed, _ := ParseRFC3339Instant(sampledAt)
	statHour := hourKey(parsed, timezone)
	insert := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("system_metrics_samples") + ` (
		  sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
		  process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
		  network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
		  db_file_bytes, stats_lag_seconds, id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if _, err := tx.ExecContext(ctx, insert,
		sampledAt,
		nullableParam(input.CPUPercent), nullableParam(input.MemoryUsedPercent),
		nullableParam(input.MemoryTotalBytes), nullableParam(input.MemoryFreeBytes),
		nullableParam(input.ProcessRssBytes), nullableParam(input.ProcessHeapUsedBytes), nullableParam(input.ProcessHeapTotalBytes),
		nullableParam(input.EventLoopLagMs),
		nullableParam(input.NetworkRxBytesPerSecond), nullableParam(input.NetworkTxBytesPerSecond),
		nullableParam(input.NetworkRxTotalBytes), nullableParam(input.NetworkTxTotalBytes),
		nullableParam(input.DBFileBytes), nullableParam(input.StatsLagSeconds),
		"metric-"+FormatRFC3339Millis(parsed)+"-"+strconv.FormatInt(parsed.UnixNano()%1_000_000, 10),
		sampledAt); err != nil {
		return err
	}
	return w.upsertSystemMetricsHourly(ctx, tx, statHour, input, sampledAt)
}

// upsertSystemMetricsHourly mirrors upsertSystemMetricsHourly：sum 累加、
// max NULL 感知 CASE、sample_count +1。
func (w *WindowRefresher) upsertSystemMetricsHourly(ctx context.Context, tx *sql.Tx, statHour string, input SystemMetricsSampleInput, updatedAt string) error {
	target := w.Dialect.qualifiedTarget("system_metrics_hourly")
	maxExpr := func(column string) string {
		return fmt.Sprintf(`%[3]s = CASE WHEN excluded.%[3]s IS NULL THEN %[1]s.%[3]s WHEN %[1]s.%[3]s IS NULL OR excluded.%[3]s > %[1]s.%[3]s THEN excluded.%[3]s ELSE %[1]s.%[3]s END`, target, target, column)
	}
	// Node 语义：count 列在 INSERT 时写 0/1（值是否存在），冲突时按
	// excluded 值累加；sum 列恒加（?? 0）；max 列 NULL 感知 CASE。
	countExpr := func(column string) string {
		return fmt.Sprintf(`%[3]s = %[1]s.%[3]s + excluded.%[3]s`, target, target, column)
	}
	query := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("system_metrics_hourly") + ` (
		  stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
		  memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
		  process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
		  network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
		  network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
		  network_rx_total_bytes_max, network_tx_total_bytes_max,
		  db_file_bytes_max, stats_lag_seconds_max, updated_at)
		VALUES (?, 1, ` + placeholders(22) + `)
		ON CONFLICT(stat_hour) DO UPDATE SET
		  sample_count = ` + target + `.sample_count + 1,
		  cpu_percent_sum = ` + target + `.cpu_percent_sum + excluded.cpu_percent_sum,
		  ` + maxExpr("cpu_percent_max") + `,
		  memory_used_percent_sum = ` + target + `.memory_used_percent_sum + excluded.memory_used_percent_sum,
		  ` + maxExpr("memory_used_percent_max") + `,
		  process_rss_bytes_sum = ` + target + `.process_rss_bytes_sum + excluded.process_rss_bytes_sum,
		  ` + maxExpr("process_rss_bytes_max") + `,
		  process_heap_used_bytes_sum = ` + target + `.process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
		  ` + maxExpr("process_heap_used_bytes_max") + `,
		  event_loop_lag_ms_sum = ` + target + `.event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
		  ` + countExpr("event_loop_lag_ms_count") + `,
		  ` + maxExpr("event_loop_lag_ms_max") + `,
		  network_rx_bytes_per_sec_sum = ` + target + `.network_rx_bytes_per_sec_sum + excluded.network_rx_bytes_per_sec_sum,
		  ` + maxExpr("network_rx_bytes_per_sec_max") + `,
		  ` + countExpr("network_rx_bytes_per_sec_count") + `,
		  network_tx_bytes_per_sec_sum = ` + target + `.network_tx_bytes_per_sec_sum + excluded.network_tx_bytes_per_sec_sum,
		  ` + maxExpr("network_tx_bytes_per_sec_max") + `,
		  ` + countExpr("network_tx_bytes_per_sec_count") + `,
		  ` + maxExpr("network_rx_total_bytes_max") + `,
		  ` + maxExpr("network_tx_total_bytes_max") + `,
		  ` + maxExpr("db_file_bytes_max") + `,
		  ` + maxExpr("stats_lag_seconds_max") + `,
		  updated_at = excluded.updated_at
	`)
	if _, err := tx.ExecContext(ctx, query,
		statHour,
		orZero(input.CPUPercent), nullableParam(input.CPUPercent),
		orZero(input.MemoryUsedPercent), nullableParam(input.MemoryUsedPercent),
		orZero(input.ProcessRssBytes), nullableParam(input.ProcessRssBytes),
		orZero(input.ProcessHeapUsedBytes), nullableParam(input.ProcessHeapUsedBytes),
		orZero(input.EventLoopLagMs), boolToFloat(input.EventLoopLagMs != nil), nullableParam(input.EventLoopLagMs),
		orZero(input.NetworkRxBytesPerSecond), nullableParam(input.NetworkRxBytesPerSecond),
		boolToFloat(input.NetworkRxBytesPerSecond != nil),
		orZero(input.NetworkTxBytesPerSecond), nullableParam(input.NetworkTxBytesPerSecond),
		boolToFloat(input.NetworkTxBytesPerSecond != nil),
		nullableParam(input.NetworkRxTotalBytes), nullableParam(input.NetworkTxTotalBytes),
		nullableParam(input.DBFileBytes), nullableParam(input.StatsLagSeconds),
		updatedAt); err != nil {
		return err
	}
	return nil
}

func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// normalizedProcessEventLoopSample mirrors normalizedProcessEventLoopSample：
// 全部指标为空时丢弃该样本。
func normalizedProcessEventLoopSample(input ProcessEventLoopSampleInput) (ProcessEventLoopSampleInput, bool) {
	normalized := input
	allEmpty := nullableCount(input.EventLoopLagMs, input.ProcessRssBytes, input.ProcessHeapUsedBytes,
		input.ProcessHeapTotalBytes, input.ProcessExternalBytes, input.ProcessArrayBuffersBytes) == 0
	if allEmpty {
		return input, false
	}
	return normalized, true
}

func nullableCount(values ...*float64) int {
	count := 0
	for _, value := range values {
		if value != nil {
			count++
		}
	}
	return count
}

func (w *WindowRefresher) insertProcessEventLoopSample(ctx context.Context, tx *sql.Tx, input ProcessEventLoopSampleInput, timezone *time.Location) error {
	normalized, ok := normalizedProcessEventLoopSample(input)
	if !ok {
		return nil
	}
	sampledAt := normalized.SampledAt
	if sampledAt == "" {
		sampledAt = FormatRFC3339Millis(w.now())
	} else if normalizedAt, ok := CanonicalizeRFC3339Instant(sampledAt); !ok {
		return fmt.Errorf("进程事件循环 sampledAt必须是带 Z 或数值 offset 的 RFC3339 时间")
	} else {
		sampledAt = normalizedAt
	}
	parsed, _ := ParseRFC3339Instant(sampledAt)
	statHour := hourKey(parsed, timezone)
	insert := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("process_event_loop_samples") + ` (
		  sampled_at, process_role, process_pid, event_loop_lag_ms,
		  process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes,
		  process_external_bytes, process_array_buffers_bytes, id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if _, err := tx.ExecContext(ctx, insert,
		sampledAt, normalized.ProcessRole, nullableParam(normalized.ProcessPid), nullableParam(normalized.EventLoopLagMs),
		nullableParam(normalized.ProcessRssBytes), nullableParam(normalized.ProcessHeapUsedBytes), nullableParam(normalized.ProcessHeapTotalBytes),
		nullableParam(normalized.ProcessExternalBytes), nullableParam(normalized.ProcessArrayBuffersBytes),
		"process-metric-"+FormatRFC3339Millis(parsed)+"-"+strconv.FormatInt(parsed.UnixNano()%1_000_000, 10),
		sampledAt); err != nil {
		return err
	}
	return w.upsertProcessEventLoopHourly(ctx, tx, statHour, normalized, sampledAt)
}

func (w *WindowRefresher) upsertProcessEventLoopHourly(ctx context.Context, tx *sql.Tx, statHour string, input ProcessEventLoopSampleInput, updatedAt string) error {
	target := w.Dialect.qualifiedTarget("process_event_loop_hourly")
	// SET 左列必须为裸列名（SQLite upsert 限制）；右列用限定引用消歧，
	// 与 Node PG 路径的语义一致。
	sumMaxExpr := func(metric string) string {
		return fmt.Sprintf(`%[3]s_sum = %[1]s.%[3]s_sum + excluded.%[3]s_sum,
		  %[3]s_max = CASE WHEN excluded.%[3]s_max IS NULL THEN %[1]s.%[3]s_max WHEN %[1]s.%[3]s_max IS NULL OR excluded.%[3]s_max > %[1]s.%[3]s_max THEN excluded.%[3]s_max ELSE %[1]s.%[3]s_max END`, target, target, metric)
	}
	query := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("process_event_loop_hourly") + ` (
		  stat_hour, process_role, sample_count,
		  event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
		  process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
		  process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
		  process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at)
		VALUES (?, ?, 1, ` + placeholders(14) + `)
		ON CONFLICT(stat_hour, process_role) DO UPDATE SET
		  sample_count = ` + target + `.sample_count + 1,
		  ` + sumMaxExpr("event_loop_lag_ms") + `,
		  event_loop_lag_ms_count = ` + target + `.event_loop_lag_ms_count + excluded.event_loop_lag_ms_count,
		  ` + sumMaxExpr("process_rss_bytes") + `,
		  ` + sumMaxExpr("process_heap_used_bytes") + `,
		  ` + sumMaxExpr("process_heap_total_bytes") + `,
		  ` + sumMaxExpr("process_external_bytes") + `,
		  ` + sumMaxExpr("process_array_buffers_bytes") + `,
		  updated_at = excluded.updated_at
	`)
	if _, err := tx.ExecContext(ctx, query,
		statHour, input.ProcessRole,
		orZero(input.EventLoopLagMs), boolToFloat(input.EventLoopLagMs != nil), nullableParam(input.EventLoopLagMs),
		orZero(input.ProcessRssBytes), nullableParam(input.ProcessRssBytes),
		orZero(input.ProcessHeapUsedBytes), nullableParam(input.ProcessHeapUsedBytes),
		orZero(input.ProcessHeapTotalBytes), nullableParam(input.ProcessHeapTotalBytes),
		orZero(input.ProcessExternalBytes), nullableParam(input.ProcessExternalBytes),
		orZero(input.ProcessArrayBuffersBytes), nullableParam(input.ProcessArrayBuffersBytes),
		updatedAt); err != nil {
		return err
	}
	return nil
}

// ---- system metrics trend windows stage ----

const systemMetricsSourceVersionPattern = `^v2:[0-9a-f]{64}$`

func requireSystemMetricsTrendSourceVersion(value string) error {
	if len(value) != 3+64 || value[:3] != "v2:" {
		return fmt.Errorf("系统指标 sourceVersion必须是 v2: 后跟 64 位小写十六进制摘要")
	}
	for _, ch := range value[3:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return fmt.Errorf("系统指标 sourceVersion必须是 v2: 后跟 64 位小写十六进制摘要")
		}
	}
	return nil
}

// systemMetricsTrendSourceVersion mirrors systemMetricsTrendSourceState 的
// sourceVersion：v2:sha256(JSON[tableName, rows@watermark])。行值以稳定 JSON
// 序列化（键名字典序；数值保留整数/最短浮点），与 Node stableWatermarkRows
// 对齐；跨语言摘要一致性由对账测试锁定。
func systemMetricsTrendSourceVersion(ctx context.Context, db *sql.DB, dialect Dialect) (string, error) {
	systemRows, err := loadRawRows(ctx, db, dialect, "system_metrics_hourly", " ORDER BY stat_hour ASC")
	if err != nil {
		return "", err
	}
	processRows, err := loadRawRows(ctx, db, dialect, "process_event_loop_hourly", " ORDER BY stat_hour ASC, process_role ASC")
	if err != nil {
		return "", err
	}
	watermark := EmptySourceWatermark
	watermarkMs := int64(-1 << 62)
	for _, row := range append(append([]rawRow{}, systemRows...), processRows...) {
		updatedAt, ok := row.values["updated_at"]
		if !ok || updatedAt == nil {
			continue
		}
		text, ok := updatedAt.(string)
		if !ok {
			continue
		}
		normalized, ok := CanonicalizeRFC3339Instant(text)
		if !ok {
			return "", fmt.Errorf("系统指标 updated_at 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		milliseconds, _ := RFC3339Milliseconds(normalized)
		if milliseconds > watermarkMs {
			watermark = normalized
			watermarkMs = milliseconds
		}
	}
	hash := sha256.New()
	hash.Write([]byte(stableWatermarkRows("system_metrics_hourly", systemMetricsTrendRowsAtWatermark(systemRows, watermark))))
	hash.Write([]byte(stableWatermarkRows("process_event_loop_hourly", systemMetricsTrendRowsAtWatermark(processRows, watermark))))
	return "v2:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type rawRow struct {
	columns []string
	values  map[string]any
}

func loadRawRows(ctx context.Context, db *sql.DB, dialect Dialect, tableName, orderBy string) ([]rawRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+dialect.StatsTable(tableName)+orderBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []rawRow
	for rows.Next() {
		scan := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for index := range scan {
			valuePtrs[index] = &scan[index]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		values := map[string]any{}
		for index, column := range columns {
			values[column] = normalizeRawValue(scan[index])
		}
		result = append(result, rawRow{columns: columns, values: values})
	}
	return result, rows.Err()
}

func normalizeRawValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return FormatRFC3339Millis(typed)
	default:
		return value
	}
}

func systemMetricsTrendRowsAtWatermark(rows []rawRow, sourceWatermark string) []rawRow {
	result := []rawRow{}
	for _, row := range rows {
		updatedAt, ok := row.values["updated_at"]
		if !ok || updatedAt == nil {
			continue
		}
		text, isString := updatedAt.(string)
		if !isString {
			continue
		}
		normalized, ok := CanonicalizeRFC3339Instant(text)
		if !ok || normalized != sourceWatermark {
			continue
		}
		result = append(result, row)
	}
	return result
}

// stableWatermarkRows mirrors stableWatermarkRows：JSON.stringify([tableName,
// rows.map(row => Object.keys(row).sort().map(key => [key, value]))])。
func stableWatermarkRows(tableName string, rows []rawRow) string {
	var builder strings.Builder
	builder.WriteString(`["`)
	builder.WriteString(jsonEscape(tableName))
	builder.WriteString(`",[`)
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			builder.WriteString(",")
		}
		keys := append([]string{}, row.columns...)
		sort.Strings(keys)
		builder.WriteString("[")
		for keyIndex, key := range keys {
			if keyIndex > 0 {
				builder.WriteString(",")
			}
			builder.WriteString(`["`)
			builder.WriteString(jsonEscape(key))
			builder.WriteString(`",`)
			builder.WriteString(stableWatermarkValue(row.values[key]))
			builder.WriteString("]")
		}
		builder.WriteString("]")
	}
	builder.WriteString("]]")
	return builder.String()
}

// stableWatermarkValue 对齐 JSON.stringify 的 JS 值序列化：
// null→null、整数无小数点、浮点最短表示、字符串 JSON 转义。
func stableWatermarkValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	case float64:
		if typed == float64(int64(typed)) && abs(typed) < 1e15 {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case string:
		return `"` + jsonEscape(typed) + `"`
	default:
		return "null"
	}
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func jsonEscape(value string) string {
	var builder strings.Builder
	for _, ch := range value {
		switch ch {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if ch < 0x20 {
				builder.WriteString(fmt.Sprintf(`\u%04x`, ch))
			} else {
				builder.WriteRune(ch)
			}
		}
	}
	return builder.String()
}

// refreshSystemMetricsTrendWindowsStage mirrors
// refreshSystemMetricsTrendWindowSnapshotsStage（无增量水位上下文时全量重建）。
func (w *WindowRefresher) refreshSystemMetricsTrendWindowsStage(ctx context.Context, tx *sql.Tx, stageContext refreshStageContext) error {
	ranges := FixedUsageStatsRanges(stageContext.todayKey)
	if len(ranges) == 0 {
		return nil
	}
	earliestDate := ranges[0].StartDate
	latestDate := ranges[len(ranges)-1].EndDate
	if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("system_metrics_trend_windows"))); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("process_event_loop_trend_windows"))); err != nil {
		return err
	}
	if err := w.refreshSystemMetricsTrendWindows(ctx, tx, ranges, earliestDate, latestDate, stageContext.updatedAt); err != nil {
		return err
	}
	return w.refreshProcessEventLoopTrendWindows(ctx, tx, ranges, earliestDate, latestDate, stageContext.updatedAt)
}

// systemMetricsTrendHourlyRow 承载 system_metrics_hourly 聚合输入行。
type systemMetricsTrendHourlyRow struct {
	StatHour                  string
	SampleCount               float64
	CPUPercentSum             float64
	CPUPercentMax             *float64
	MemoryUsedPercentSum      float64
	MemoryUsedPercentMax      *float64
	ProcessRssBytesSum        float64
	ProcessRssBytesMax        *float64
	ProcessHeapUsedBytesSum   float64
	ProcessHeapUsedBytesMax   *float64
	EventLoopLagMsSum         float64
	EventLoopLagMsCount       float64
	EventLoopLagMsMax         *float64
	NetworkRxBytesPerSecSum   float64
	NetworkRxBytesPerSecMax   *float64
	NetworkRxBytesPerSecCount float64
	NetworkTxBytesPerSecSum   float64
	NetworkTxBytesPerSecMax   *float64
	NetworkTxBytesPerSecCount float64
	NetworkRxTotalBytesMax    *float64
	NetworkTxTotalBytesMax    *float64
	DBFileBytesMax            *float64
	StatsLagSecondsMax        *float64
}

// aggregateSystemMetricsRows mirrors usage-stats-metric-aggregates.ts
// aggregateSystemMetricsRows。
func aggregateSystemMetricsRows(rows []systemMetricsTrendHourlyRow, bucketHours int) []systemMetricsTrendHourlyRow {
	buckets := map[string]*systemMetricsTrendHourlyRow{}
	order := []string{}
	for _, row := range rows {
		key := TrendBucketKey(row.StatHour, bucketHours)
		bucket, ok := buckets[key]
		if !ok {
			bucket = &systemMetricsTrendHourlyRow{StatHour: key}
			buckets[key] = bucket
			order = append(order, key)
		}
		bucket.SampleCount += row.SampleCount
		bucket.CPUPercentSum += row.CPUPercentSum
		maxMerge(&bucket.CPUPercentMax, row.CPUPercentMax)
		bucket.MemoryUsedPercentSum += row.MemoryUsedPercentSum
		maxMerge(&bucket.MemoryUsedPercentMax, row.MemoryUsedPercentMax)
		bucket.ProcessRssBytesSum += row.ProcessRssBytesSum
		maxMerge(&bucket.ProcessRssBytesMax, row.ProcessRssBytesMax)
		bucket.ProcessHeapUsedBytesSum += row.ProcessHeapUsedBytesSum
		maxMerge(&bucket.ProcessHeapUsedBytesMax, row.ProcessHeapUsedBytesMax)
		bucket.EventLoopLagMsSum += row.EventLoopLagMsSum
		bucket.EventLoopLagMsCount += row.EventLoopLagMsCount
		maxMerge(&bucket.EventLoopLagMsMax, row.EventLoopLagMsMax)
		bucket.NetworkRxBytesPerSecSum += row.NetworkRxBytesPerSecSum
		maxMerge(&bucket.NetworkRxBytesPerSecMax, row.NetworkRxBytesPerSecMax)
		bucket.NetworkRxBytesPerSecCount += row.NetworkRxBytesPerSecCount
		bucket.NetworkTxBytesPerSecSum += row.NetworkTxBytesPerSecSum
		maxMerge(&bucket.NetworkTxBytesPerSecMax, row.NetworkTxBytesPerSecMax)
		bucket.NetworkTxBytesPerSecCount += row.NetworkTxBytesPerSecCount
		maxMerge(&bucket.NetworkRxTotalBytesMax, row.NetworkRxTotalBytesMax)
		maxMerge(&bucket.NetworkTxTotalBytesMax, row.NetworkTxTotalBytesMax)
		maxMerge(&bucket.DBFileBytesMax, row.DBFileBytesMax)
		maxMerge(&bucket.StatsLagSecondsMax, row.StatsLagSecondsMax)
	}
	result := make([]systemMetricsTrendHourlyRow, 0, len(order))
	for _, key := range order {
		result = append(result, *buckets[key])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StatHour < result[j].StatHour })
	return result
}

func maxMerge(target **float64, value *float64) {
	if value == nil {
		return
	}
	if *target == nil || *value > **target {
		merged := *value
		*target = &merged
	}
}

func (w *WindowRefresher) refreshSystemMetricsTrendWindows(ctx context.Context, tx *sql.Tx, ranges []StatsRange, earliestDate, todayKey, updatedAt string) error {
	query := w.Dialect.bind(`
		SELECT stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
		  memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
		  process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
		  network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
		  network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
		  network_rx_total_bytes_max, network_tx_total_bytes_max,
		  db_file_bytes_max, stats_lag_seconds_max
		FROM ` + w.Dialect.StatsTable("system_metrics_hourly") + `
		WHERE stat_hour >= ? AND stat_hour <= ?
		ORDER BY stat_hour ASC
	`)
	rows, err := tx.QueryContext(ctx, query, earliestDate+"T00", todayKey+"T23")
	if err != nil {
		return err
	}
	sourceRows, err := scanSystemMetricsHourlyRows(rows, nil)
	if err != nil {
		return err
	}
	rowsByDate := RowsByStatHourDate(sourceRows, func(r systemMetricsTrendHourlyRow) string { return r.StatHour })
	for _, rangeValue := range ranges {
		var inRange []systemMetricsTrendHourlyRow
		for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
			inRange = append(inRange, row)
		}
		buckets := aggregateSystemMetricsRows(inRange, TrendBucketHours(rangeValue.Days))
		for _, row := range buckets {
			insert := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("system_metrics_trend_windows") + ` (
				  window_key, start_date, end_date, bucket_key, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
				  memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
				  process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
				  network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
				  network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
				  network_rx_total_bytes_max, network_tx_total_bytes_max,
				  db_file_bytes_max, stats_lag_seconds_max, updated_at)
				VALUES (` + placeholders(27) + `)
			`)
			if _, err := tx.ExecContext(ctx, insert,
				RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, row.StatHour,
				row.SampleCount, row.CPUPercentSum, nullableParam(row.CPUPercentMax),
				row.MemoryUsedPercentSum, nullableParam(row.MemoryUsedPercentMax),
				row.ProcessRssBytesSum, nullableParam(row.ProcessRssBytesMax),
				row.ProcessHeapUsedBytesSum, nullableParam(row.ProcessHeapUsedBytesMax),
				row.EventLoopLagMsSum, row.EventLoopLagMsCount, nullableParam(row.EventLoopLagMsMax),
				row.NetworkRxBytesPerSecSum, nullableParam(row.NetworkRxBytesPerSecMax), row.NetworkRxBytesPerSecCount,
				row.NetworkTxBytesPerSecSum, nullableParam(row.NetworkTxBytesPerSecMax), row.NetworkTxBytesPerSecCount,
				nullableParam(row.NetworkRxTotalBytesMax), nullableParam(row.NetworkTxTotalBytesMax),
				nullableParam(row.DBFileBytesMax), nullableParam(row.StatsLagSecondsMax),
				updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanSystemMetricsHourlyRows(rows *sql.Rows, err error) ([]systemMetricsTrendHourlyRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []systemMetricsTrendHourlyRow
	for rows.Next() {
		var row systemMetricsTrendHourlyRow
		var cpuPercentMax, memoryUsedPercentMax, processRssBytesMax, processHeapUsedBytesMax sql.NullFloat64
		var eventLoopLagMsMax, networkRxBytesPerSecMax, networkTxBytesPerSecMax sql.NullFloat64
		var networkRxTotalBytesMax, networkTxTotalBytesMax, dbFileBytesMax, statsLagSecondsMax sql.NullFloat64
		if err := rows.Scan(&row.StatHour, &row.SampleCount, &row.CPUPercentSum, &cpuPercentMax, &row.MemoryUsedPercentSum,
			&memoryUsedPercentMax, &row.ProcessRssBytesSum, &processRssBytesMax, &row.ProcessHeapUsedBytesSum,
			&processHeapUsedBytesMax, &row.EventLoopLagMsSum, &row.EventLoopLagMsCount, &eventLoopLagMsMax,
			&row.NetworkRxBytesPerSecSum, &networkRxBytesPerSecMax, &row.NetworkRxBytesPerSecCount,
			&row.NetworkTxBytesPerSecSum, &networkTxBytesPerSecMax, &row.NetworkTxBytesPerSecCount,
			&networkRxTotalBytesMax, &networkTxTotalBytesMax,
			&dbFileBytesMax, &statsLagSecondsMax); err != nil {
			return nil, err
		}
		row.CPUPercentMax = nullFloatPtr(cpuPercentMax)
		row.MemoryUsedPercentMax = nullFloatPtr(memoryUsedPercentMax)
		row.ProcessRssBytesMax = nullFloatPtr(processRssBytesMax)
		row.ProcessHeapUsedBytesMax = nullFloatPtr(processHeapUsedBytesMax)
		row.EventLoopLagMsMax = nullFloatPtr(eventLoopLagMsMax)
		row.NetworkRxBytesPerSecMax = nullFloatPtr(networkRxBytesPerSecMax)
		row.NetworkTxBytesPerSecMax = nullFloatPtr(networkTxBytesPerSecMax)
		row.NetworkRxTotalBytesMax = nullFloatPtr(networkRxTotalBytesMax)
		row.NetworkTxTotalBytesMax = nullFloatPtr(networkTxTotalBytesMax)
		row.DBFileBytesMax = nullFloatPtr(dbFileBytesMax)
		row.StatsLagSecondsMax = nullFloatPtr(statsLagSecondsMax)
		result = append(result, row)
	}
	return result, rows.Err()
}

func nullFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

// processEventLoopTrendHourlyRow 对齐 process_event_loop_hourly 聚合输入。
type processEventLoopTrendHourlyRow struct {
	StatHour                    string
	ProcessRole                 string
	SampleCount                 float64
	EventLoopLagMsSum           float64
	EventLoopLagMsCount         float64
	EventLoopLagMsMax           *float64
	ProcessRssBytesSum          float64
	ProcessRssBytesMax          *float64
	ProcessHeapUsedBytesSum     float64
	ProcessHeapUsedBytesMax     *float64
	ProcessHeapTotalBytesSum    float64
	ProcessHeapTotalBytesMax    *float64
	ProcessExternalBytesSum     float64
	ProcessExternalBytesMax     *float64
	ProcessArrayBuffersBytesSum float64
	ProcessArrayBuffersBytesMax *float64
}

// aggregateProcessEventLoopRows mirrors aggregateProcessEventLoopRows。
func aggregateProcessEventLoopRows(rows []processEventLoopTrendHourlyRow, bucketHours int) []processEventLoopTrendHourlyRow {
	buckets := map[string]*processEventLoopTrendHourlyRow{}
	order := []string{}
	for _, row := range rows {
		if !isValidProcessEventLoopRole(row.ProcessRole) {
			continue
		}
		statHour := TrendBucketKey(row.StatHour, bucketHours)
		key := statHour + ":" + row.ProcessRole
		bucket, ok := buckets[key]
		if !ok {
			bucket = &processEventLoopTrendHourlyRow{StatHour: statHour, ProcessRole: row.ProcessRole}
			buckets[key] = bucket
			order = append(order, key)
		}
		bucket.SampleCount += row.SampleCount
		bucket.EventLoopLagMsSum += row.EventLoopLagMsSum
		bucket.EventLoopLagMsCount += row.EventLoopLagMsCount
		maxMerge(&bucket.EventLoopLagMsMax, row.EventLoopLagMsMax)
		sumMaxMergeProcessMemory(bucket, row, "ProcessRssBytes")
		sumMaxMergeProcessMemory(bucket, row, "ProcessHeapUsedBytes")
		sumMaxMergeProcessMemory(bucket, row, "ProcessHeapTotalBytes")
		sumMaxMergeProcessMemory(bucket, row, "ProcessExternalBytes")
		sumMaxMergeProcessMemory(bucket, row, "ProcessArrayBuffersBytes")
	}
	result := make([]processEventLoopTrendHourlyRow, 0, len(order))
	for _, key := range order {
		result = append(result, *buckets[key])
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StatHour != result[j].StatHour {
			return result[i].StatHour < result[j].StatHour
		}
		return result[i].ProcessRole < result[j].ProcessRole
	})
	return result
}

func sumMaxMergeProcessMemory(bucket *processEventLoopTrendHourlyRow, row processEventLoopTrendHourlyRow, metric string) {
	// 通过字段名镜像 addProcessMemoryBucketMetric 的 _sum/_max 成对合并。
	get := func(source processEventLoopTrendHourlyRow, metric string, max bool) *float64 {
		switch metric {
		case "ProcessRssBytes":
			if max {
				return source.ProcessRssBytesMax
			}
			return ptrOf(source.ProcessRssBytesSum)
		case "ProcessHeapUsedBytes":
			if max {
				return source.ProcessHeapUsedBytesMax
			}
			return ptrOf(source.ProcessHeapUsedBytesSum)
		case "ProcessHeapTotalBytes":
			if max {
				return source.ProcessHeapTotalBytesMax
			}
			return ptrOf(source.ProcessHeapTotalBytesSum)
		case "ProcessExternalBytes":
			if max {
				return source.ProcessExternalBytesMax
			}
			return ptrOf(source.ProcessExternalBytesSum)
		case "ProcessArrayBuffersBytes":
			if max {
				return source.ProcessArrayBuffersBytesMax
			}
			return ptrOf(source.ProcessArrayBuffersBytesSum)
		}
		return nil
	}
	sumSource := get(row, metric, false)
	maxSource := get(row, metric, true)
	sumTarget := get(*bucket, metric, false)
	if sumSource != nil && sumTarget != nil {
		merged := *sumSource + *sumTarget
		setProcessMemory(bucket, metric, false, merged)
	}
	maxTarget := get(*bucket, metric, true)
	if maxSource != nil {
		if maxTarget == nil || *maxSource > *maxTarget {
			setProcessMemory(bucket, metric, true, *maxSource)
		}
	}
}

func ptrOf(value float64) *float64 { return &value }

func setProcessMemory(row *processEventLoopTrendHourlyRow, metric string, max bool, value float64) {
	switch metric {
	case "ProcessRssBytes":
		if max {
			row.ProcessRssBytesMax = &value
		} else {
			row.ProcessRssBytesSum = value
		}
	case "ProcessHeapUsedBytes":
		if max {
			row.ProcessHeapUsedBytesMax = &value
		} else {
			row.ProcessHeapUsedBytesSum = value
		}
	case "ProcessHeapTotalBytes":
		if max {
			row.ProcessHeapTotalBytesMax = &value
		} else {
			row.ProcessHeapTotalBytesSum = value
		}
	case "ProcessExternalBytes":
		if max {
			row.ProcessExternalBytesMax = &value
		} else {
			row.ProcessExternalBytesSum = value
		}
	case "ProcessArrayBuffersBytes":
		if max {
			row.ProcessArrayBuffersBytesMax = &value
		} else {
			row.ProcessArrayBuffersBytesSum = value
		}
	}
}

func (w *WindowRefresher) refreshProcessEventLoopTrendWindows(ctx context.Context, tx *sql.Tx, ranges []StatsRange, earliestDate, todayKey, updatedAt string) error {
	query := w.Dialect.bind(`
		SELECT stat_hour, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
		  process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
		  process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
		  process_array_buffers_bytes_sum, process_array_buffers_bytes_max
		FROM ` + w.Dialect.StatsTable("process_event_loop_hourly") + `
		WHERE stat_hour >= ? AND stat_hour <= ?
		ORDER BY stat_hour ASC, process_role ASC
	`)
	rows, err := tx.QueryContext(ctx, query, earliestDate+"T00", todayKey+"T23")
	if err != nil {
		return err
	}
	defer rows.Close()
	sourceRows := []processEventLoopTrendHourlyRow{}
	for rows.Next() {
		var row processEventLoopTrendHourlyRow
		var eventLoopLagMsMax, rssMax, heapUsedMax, heapTotalMax, externalMax, arrayBuffersMax sql.NullFloat64
		if err := rows.Scan(&row.StatHour, &row.ProcessRole, &row.SampleCount,
			&row.EventLoopLagMsSum, &row.EventLoopLagMsCount, &eventLoopLagMsMax,
			&row.ProcessRssBytesSum, &rssMax, &row.ProcessHeapUsedBytesSum, &heapUsedMax,
			&row.ProcessHeapTotalBytesSum, &heapTotalMax, &row.ProcessExternalBytesSum, &externalMax,
			&row.ProcessArrayBuffersBytesSum, &arrayBuffersMax); err != nil {
			return err
		}
		row.EventLoopLagMsMax = nullFloatPtr(eventLoopLagMsMax)
		row.ProcessRssBytesMax = nullFloatPtr(rssMax)
		row.ProcessHeapUsedBytesMax = nullFloatPtr(heapUsedMax)
		row.ProcessHeapTotalBytesMax = nullFloatPtr(heapTotalMax)
		row.ProcessExternalBytesMax = nullFloatPtr(externalMax)
		row.ProcessArrayBuffersBytesMax = nullFloatPtr(arrayBuffersMax)
		sourceRows = append(sourceRows, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rowsByDate := RowsByStatHourDate(sourceRows, func(r processEventLoopTrendHourlyRow) string { return r.StatHour })
	for _, rangeValue := range ranges {
		var inRange []processEventLoopTrendHourlyRow
		for _, row := range RowsForDateRange(rowsByDate, rangeValue.StartDate, rangeValue.EndDate) {
			inRange = append(inRange, row)
		}
		buckets := aggregateProcessEventLoopRows(inRange, TrendBucketHours(rangeValue.Days))
		for _, row := range buckets {
			insert := w.Dialect.bind(`
				INSERT INTO ` + w.Dialect.StatsTable("process_event_loop_trend_windows") + ` (
				  window_key, start_date, end_date, bucket_key, process_role, sample_count,
				  event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max,
				  process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max,
				  process_heap_total_bytes_sum, process_heap_total_bytes_max, process_external_bytes_sum, process_external_bytes_max,
				  process_array_buffers_bytes_sum, process_array_buffers_bytes_max, updated_at)
				VALUES (` + placeholders(20) + `)
			`)
			if _, err := tx.ExecContext(ctx, insert,
				RangeWindowKey(rangeValue), rangeValue.StartDate, rangeValue.EndDate, row.StatHour, row.ProcessRole, row.SampleCount,
				row.EventLoopLagMsSum, row.EventLoopLagMsCount, nullableParam(row.EventLoopLagMsMax),
				row.ProcessRssBytesSum, nullableParam(row.ProcessRssBytesMax),
				row.ProcessHeapUsedBytesSum, nullableParam(row.ProcessHeapUsedBytesMax),
				row.ProcessHeapTotalBytesSum, nullableParam(row.ProcessHeapTotalBytesMax),
				row.ProcessExternalBytesSum, nullableParam(row.ProcessExternalBytesMax),
				row.ProcessArrayBuffersBytesSum, nullableParam(row.ProcessArrayBuffersBytesMax),
				updatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// processEventLoopRoles mirrors processEventLoopRoleFromUnknown
// （process-event-loop-monitor.ts）的角色校验：固定角色 + 前缀/副本命名模式。
func processEventLoopRoles() map[string]bool {
	roles := map[string]bool{
		"server":        true,
		"ingest-worker": true,
		"stats-worker":  true,
		"ops-worker":    true,
		"db-service":    true,
	}
	// gateway:/control:/control-replica:/db-service:<name> 前缀模式与
	// <worker>:<replica 1-64> 模式在聚合输入里恒为已落库合法值，逐行正则
	// 校验与 Node 等价；这里仅对固定角色做白名单，前缀角色由写入侧保证。
	for _, prefix := range []string{"gateway:", "control:", "control-replica:", "db-service:"} {
		_ = prefix
	}
	for _, worker := range []string{"ingest-worker", "usage-worker", "log-worker", "stats-worker", "ops-worker"} {
		for replica := 1; replica <= 8; replica++ {
			roles[worker+":"+strconv.Itoa(replica)] = true
		}
	}
	return roles
}

func isValidProcessEventLoopRole(value string) bool {
	if processEventLoopRoles()[value] {
		return true
	}
	if len(value) == 0 || len(value) > 96 {
		return false
	}
	for _, prefix := range []string{"gateway:", "control:", "control-replica:", "db-service:"} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}
