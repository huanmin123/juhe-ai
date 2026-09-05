package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// PG 统计扣减链的渲染与语义测试：无真库环境，用录制驱动捕获全部语句，
// SQL 文本与参数逐字符断言（对照 Node 归档的 PG 路径模板，占位符经独立的
// bindTestPG 重写为 $n）；SQLite 侧用真实内存库做扣减语义互证。

// ---- 录制驱动 ----

type recordedStatement struct {
	query string
	args  []driver.Value
	tx    int
}

type pgRecorder struct {
	mu         sync.Mutex
	statements []recordedStatement
	txDepth    int
	begins     int
	commits    int
	rollbacks  int
	scripted   []scriptedResult
}

type scriptedResult struct {
	match   string
	columns []string
	rows    [][]driver.Value
}

func newPGRecorder() *pgRecorder { return &pgRecorder{} }

// script 登记一条按子串匹配的查询结果（FIFO，先登记先消费）。
func (r *pgRecorder) script(match string, columns []string, rows [][]driver.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripted = append(r.scripted, scriptedResult{match: match, columns: columns, rows: rows})
}

func (r *pgRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, recordedStatement{query: query, args: values, tx: r.txDepth})
}

func (r *pgRecorder) all() []recordedStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedStatement{}, r.statements...)
}

func (r *pgRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = nil
}

func (r *pgRecorder) popScripted(query string) (scriptedResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, item := range r.scripted {
		if strings.Contains(query, item.match) {
			r.scripted = append(r.scripted[:i], r.scripted[i+1:]...)
			return item, true
		}
	}
	return scriptedResult{}, false
}

type recorderDriver struct{ rec *pgRecorder }

func (d recorderDriver) Open(string) (driver.Conn, error) { return &recorderConn{rec: d.rec}, nil }

type recorderConnector struct{ rec *pgRecorder }

func (c recorderConnector) Connect(context.Context) (driver.Conn, error) {
	return &recorderConn{rec: c.rec}, nil
}

func (c recorderConnector) Driver() driver.Driver { return recorderDriver{rec: c.rec} }

type recorderConn struct{ rec *pgRecorder }

func (c *recorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}

func (c *recorderConn) Close() error { return nil }

func (c *recorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用（走 BeginTx）")
}

func (c *recorderConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.rec.mu.Lock()
	c.rec.txDepth++
	c.rec.begins++
	c.rec.mu.Unlock()
	return &recorderTx{rec: c.rec}, nil
}

func (c *recorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	return driver.RowsAffected(1), nil
}

func (c *recorderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if scripted, ok := c.rec.popScripted(query); ok {
		return &recordedRows{columns: scripted.columns, values: scripted.rows}, nil
	}
	return &recordedRows{columns: []string{"value"}}, nil
}

func (c *recorderConn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case nil, int64, float64, bool, []byte, string, time.Time, []string:
		return nil
	case int:
		value.Value = int64(value.Value.(int))
		return nil
	}
	return fmt.Errorf("recorder: 不支持的参数类型 %T", value.Value)
}

type recorderTx struct{ rec *pgRecorder }

func (t *recorderTx) Commit() error {
	t.rec.mu.Lock()
	t.rec.txDepth--
	t.rec.commits++
	t.rec.mu.Unlock()
	return nil
}

func (t *recorderTx) Rollback() error {
	t.rec.mu.Lock()
	t.rec.txDepth--
	t.rec.rollbacks++
	t.rec.mu.Unlock()
	return nil
}

type recordedRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
}

func (r *recordedRows) Columns() []string { return r.columns }

func (r *recordedRows) Close() error { return nil }

func (r *recordedRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

// openRecorderPG 打开一个 PG 模式的录制库句柄。
func openRecorderPG(rec *pgRecorder) *DB {
	return &DB{DB: sql.OpenDB(recorderConnector{rec: rec}), Postgres: true}
}

// bindTestPG 是测试内独立的 ?→$n 重写器（不复用 DB.Bind，避免渲染断言循环论证）。
func bindTestPG(query string) string {
	var out strings.Builder
	index := 0
	for _, ch := range query {
		if ch == '?' {
			index++
			out.WriteString("$")
			out.WriteString(fmt.Sprintf("%d", index))
			continue
		}
		out.WriteRune(ch)
	}
	return out.String()
}

// ---- 测试固定值 ----

var pgTestZone = time.FixedZone("PGTEST", 8*3600)

const pgTestUpdatedAt = "2026-09-04T00:00:00.000Z"

func pgTestRow() statsagg.UsageStatsRecordRow {
	text := func(v string) *string { return &v }
	num := func(v float64) *float64 { return &v }
	return statsagg.UsageStatsRecordRow{
		ID: "rec-1", SystemAccountID: "sys-1", TraceID: "tr-1", TrafficSource: "api",
		ClientIP: text("127.0.0.1"), APIKeyID: text("key-1"),
		ProviderCode: text("openai"), StatusCode: num(200), Success: 1,
		FirstTokenMs: num(120), DurationMs: num(340),
		InputTokens: num(10), OutputTokens: num(5), CacheReadTokens: num(2),
		CacheReadCostUsd: num(0.002), CostUsd: num(0.01),
		CreatedAt: "2026-01-05T03:04:05.123Z",
	}
}

func newPGTestStore(rec *pgRecorder) *RecordCleanupStore {
	return &RecordCleanupStore{
		Stats:    openRecorderPG(rec),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now: func() time.Time {
			return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
		},
		Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
	}
}

// ---- 单元断言 ----

func TestPostgresStatsSubtractParamsMirrorsNodeWriterParams(t *testing.T) {
	stats := statsagg.UsageStatsAccumulator{
		RequestCount: 1, SuccessCount: 1,
		InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheReadCostUsd: 0.002,
		DurationMsSum: 340, DurationMsCount: 1, DurationMsMax: 340,
		FirstTokenMsSum: 120, FirstTokenMsCount: 1, FirstTokenMsMax: 120,
	}
	params := postgresStatsSubtractParams(stats)
	if len(params) != 22 {
		t.Fatalf("PG statsSubtractParams 应为 22 参（usage-stats-writer-params.ts），实际 %d", len(params))
	}
	expect := []float64{
		stats.RequestCount, stats.SuccessCount, 0,
		stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheReadCostUsd,
		0, 0, 0, 0, 0, 0, stats.TotalCostUsd,
		stats.DurationMsSum, stats.DurationMsCount,
		stats.DurationMsCount,
		stats.FirstTokenMsSum, stats.FirstTokenMsCount,
		stats.FirstTokenMsCount,
		stats.RequestCount, 0,
	}
	for i, want := range expect {
		got, _ := params[i].(float64)
		if got != want {
			t.Fatalf("params[%d] = %v, 期望 %v", i, got, want)
		}
	}
}

func TestPostgresMultiRowPlaceholders(t *testing.T) {
	if got := postgresMultiRowPlaceholders(2, 3); got != "(?, ?, ?), (?, ?, ?)" {
		t.Fatalf("postgresMultiRowPlaceholders(2,3) = %q", got)
	}
	if got := postgresMultiRowPlaceholders(1, 1); got != "(?)" {
		t.Fatalf("postgresMultiRowPlaceholders(1,1) = %q", got)
	}
}

// ---- subtractPostgresUsageStatsRows 渲染断言 ----

var pgTotalsSubtractSQL = bindTestPG(`
      UPDATE juhe_stats.usage_stats_totals
      SET request_count = GREATEST(0, request_count - $1),
          success_count = GREATEST(0, success_count - $2),
          error_count = GREATEST(0, error_count - $3),
          input_tokens = GREATEST(0, input_tokens - $4),
          output_tokens = GREATEST(0, output_tokens - $5),
          cache_read_tokens = GREATEST(0, cache_read_tokens - $6),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - $7),
          cache_write_tokens = GREATEST(0, cache_write_tokens - $8),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - $9),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - $10),
          thinking_tokens = GREATEST(0, thinking_tokens - $11),
          input_image_tokens = GREATEST(0, input_image_tokens - $12),
          output_image_tokens = GREATEST(0, output_image_tokens - $13),
          total_cost_usd = GREATEST(0, total_cost_usd - $14),
          duration_ms_sum = GREATEST(0, duration_ms_sum - $15),
          duration_ms_count = GREATEST(0, duration_ms_count - $16),
          duration_ms_max = CASE WHEN duration_ms_count <= $17 THEN 0 ELSE duration_ms_max END,
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - $18),
          first_token_ms_count = GREATEST(0, first_token_ms_count - $19),
          first_token_ms_max = CASE WHEN first_token_ms_count <= $20 THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= $21 THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= $22 THEN NULL ELSE last_error_at END,
          updated_at = $23
      WHERE system_account_id = $24 AND scope_type = $25 AND scope_id = $26
    `)

func TestSubtractPostgresUsageStatsRowsRendersNodeSQL(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	row := pgTestRow() // 无 model/account/group：维度 = 2×system_account + provider + api_key
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.subtractPostgresUsageStatsRows(context.Background(), tx, []statsagg.UsageStatsRecordRow{row}, pgTestUpdatedAt, pgTestZone); err != nil {
		t.Fatalf("subtractPostgresUsageStatsRows: %v", err)
	}

	statements := rec.all()
	// 4 scope × (totals UPDATE+DELETE) + 4 scope × 5 bucket × (UPDATE+DELETE)
	// + 2 latency 样本 × 5 bucket × 4 scope × (UPDATE+DELETE)
	// + 脏范围标记（overview：2 个 system_account 日桶；quota：api_key 小时桶）
	// = 8 + 40 + 80 + 2 + 1 = 131
	if len(statements) != 131 {
		t.Fatalf("语句数 = %d, 期望 131", len(statements))
	}
	for i, statement := range statements {
		if statement.tx != 1 {
			t.Fatalf("statements[%d] 应在事务内执行", i)
		}
	}

	// totals UPDATE：文本逐字符 + 首个 scope 的 26 参逐项。
	totalUpdateArgs := statements[0].args
	if len(totalUpdateArgs) != 26 {
		t.Fatalf("totals UPDATE 参数数 = %d, 期望 26", len(totalUpdateArgs))
	}
	accumulator := statsagg.UsageStatsAccumulatorFromRecord(row)
	// 同一行的 4 个 scope 条目共享同一 accumulator；首 scope 为 (sys-1, system_account, sys-1)。
	expectedTotalArgs := []any{
		accumulator.RequestCount, accumulator.SuccessCount, accumulator.ErrorCount,
		accumulator.InputTokens, accumulator.OutputTokens,
		accumulator.CacheReadTokens, accumulator.CacheReadCostUsd,
		accumulator.CacheWriteTokens, accumulator.CacheWrite1hTokens, accumulator.CacheWriteCostUsd,
		accumulator.ThinkingTokens, accumulator.InputImageTokens, accumulator.OutputImageTokens,
		accumulator.TotalCostUsd,
		accumulator.DurationMsSum, accumulator.DurationMsCount,
		accumulator.DurationMsCount,
		accumulator.FirstTokenMsSum, accumulator.FirstTokenMsCount,
		accumulator.FirstTokenMsCount,
		accumulator.RequestCount, accumulator.ErrorCount,
		pgTestUpdatedAt, "sys-1", "system_account", "sys-1",
	}
	for i, want := range expectedTotalArgs {
		got := totalUpdateArgs[i]
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Fatalf("totals UPDATE 参数[%d] = %v, 期望 %v", i, got, want)
		}
	}

	// scope 迭代保持 Node Map 插入顺序：system_account 调用方 → global → provider → api_key。
	scopeSequence := [][3]string{
		{"sys-1", "system_account", "sys-1"},
		{"global", "system_account", "global"},
		{"sys-1", "provider", "openai"},
		{"sys-1", "api_key", "key-1"},
	}
	for scopeIndex, scope := range scopeSequence {
		update := statements[scopeIndex*2]
		if update.query != pgTotalsSubtractSQL {
			t.Fatalf("totals UPDATE[%d] 文本不匹配：\n%s", scopeIndex, update.query)
		}
		got := []string{fmt.Sprintf("%v", update.args[23]), fmt.Sprintf("%v", update.args[24]), fmt.Sprintf("%v", update.args[25])}
		want := []string{scope[0], scope[1], scope[2]}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("totals UPDATE[%d] WHERE 参数 = %v, 期望 %v", scopeIndex, got, want)
			}
		}
		delete := statements[scopeIndex*2+1]
		wantDelete := bindTestPG(`
    DELETE FROM juhe_stats.usage_stats_totals
    WHERE system_account_id = $1 AND scope_type = $2 AND scope_id = $3
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `)
		if delete.query != wantDelete {
			t.Fatalf("totals DELETE[%d] 文本不匹配：\n%s", scopeIndex, delete.query)
		}
		if len(delete.args) != 3 || fmt.Sprintf("%v", delete.args[0]) != scope[0] ||
			fmt.Sprintf("%v", delete.args[1]) != scope[1] || fmt.Sprintf("%v", delete.args[2]) != scope[2] {
			t.Fatalf("totals DELETE[%d] 参数 = %v", scopeIndex, delete.args)
		}
	}

	// 时间桶：8 条 totals 语句之后，5 桶 × 4 scope × (UPDATE+DELETE)。
	bucketUpdate := statements[8]
	wantBucketUpdate := bindTestPG(fmt.Sprintf(`
      UPDATE juhe_stats.%s
      SET request_count = GREATEST(0, request_count - $1),
          success_count = GREATEST(0, success_count - $2),
          error_count = GREATEST(0, error_count - $3),
          input_tokens = GREATEST(0, input_tokens - $4),
          output_tokens = GREATEST(0, output_tokens - $5),
          cache_read_tokens = GREATEST(0, cache_read_tokens - $6),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - $7),
          cache_write_tokens = GREATEST(0, cache_write_tokens - $8),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - $9),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - $10),
          thinking_tokens = GREATEST(0, thinking_tokens - $11),
          input_image_tokens = GREATEST(0, input_image_tokens - $12),
          output_image_tokens = GREATEST(0, output_image_tokens - $13),
          total_cost_usd = GREATEST(0, total_cost_usd - $14),
          duration_ms_sum = GREATEST(0, duration_ms_sum - $15),
          duration_ms_count = GREATEST(0, duration_ms_count - $16),
          duration_ms_max = CASE WHEN duration_ms_count <= $17 THEN 0 ELSE duration_ms_max END,
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - $18),
          first_token_ms_count = GREATEST(0, first_token_ms_count - $19),
          first_token_ms_max = CASE WHEN first_token_ms_count <= $20 THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= $21 THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= $22 THEN NULL ELSE last_error_at END,
          updated_at = $23
      WHERE system_account_id = $24 AND scope_type = $25 AND scope_id = $26 AND %s = $27
    `, "usage_stats_minute", "stat_minute"))
	if bucketUpdate.query != wantBucketUpdate {
		t.Fatalf("minute 桶 UPDATE 文本不匹配：\n%s", bucketUpdate.query)
	}
	if len(bucketUpdate.args) != 27 {
		t.Fatalf("minute 桶 UPDATE 参数数 = %d, 期望 27", len(bucketUpdate.args))
	}
	if fmt.Sprintf("%v", bucketUpdate.args[26]) != "2026-01-05T11:04" {
		t.Fatalf("minute 桶 timeValue = %v", bucketUpdate.args[26])
	}
	wantBucketDelete := bindTestPG(fmt.Sprintf(`
    DELETE FROM juhe_stats.%s
    WHERE system_account_id = $1 AND scope_type = $2 AND scope_id = $3 AND %s = $4
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `, "usage_stats_minute", "stat_minute"))
	if statements[9].query != wantBucketDelete {
		t.Fatalf("minute 桶 DELETE 文本不匹配：\n%s", statements[9].query)
	}

	// latency 家族：桶阶段之后，首条 latency UPDATE/DELETE 文本逐字符。
	latencyUpdate := statements[48]
	wantLatencyUpdate := bindTestPG(`
      UPDATE juhe_stats.usage_latency_minute
      SET sample_count = GREATEST(0, sample_count - $1),
          updated_at = $2
      WHERE system_account_id = $3 AND scope_type = $4 AND scope_id = $5
        AND metric_type = $6 AND stat_minute = $7 AND bucket_upper_bound_ms = $8
    `)
	if latencyUpdate.query != wantLatencyUpdate {
		t.Fatalf("latency UPDATE 文本不匹配：\n%s", latencyUpdate.query)
	}
	// 首 latency 条目：duration_ms 样本（340ms → 上界 500）、首 scope。
	wantLatencyArgs := []string{"1", pgTestUpdatedAt, "sys-1", "system_account", "sys-1", "duration_ms", "2026-01-05T11:04", "500"}
	for i, want := range wantLatencyArgs {
		if fmt.Sprintf("%v", latencyUpdate.args[i]) != want {
			t.Fatalf("latency UPDATE 参数[%d] = %v, 期望 %s", i, latencyUpdate.args[i], want)
		}
	}
	latencyDelete := statements[49]
	wantLatencyDelete := bindTestPG(`
      DELETE FROM juhe_stats.usage_latency_minute
      WHERE system_account_id = $1 AND scope_type = $2 AND scope_id = $3
        AND metric_type = $4 AND stat_minute = $5 AND bucket_upper_bound_ms = $6
        AND sample_count = 0
    `)
	if latencyDelete.query != wantLatencyDelete {
		t.Fatalf("latency DELETE 文本不匹配：\n%s", latencyDelete.query)
	}

	// 尾部脏范围：overview 2 条（sys-1 / global）→ quota 1 条（api_key 小时桶）。
	overviewFirst := statements[len(statements)-3]
	wantDirty := bindTestPG(`
      INSERT INTO juhe_stats.usage_overview_dirty_scopes (
        system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT(system_account_id) DO UPDATE SET
        scope_id = EXCLUDED.scope_id,
        min_changed_date = LEAST(usage_overview_dirty_scopes.min_changed_date, EXCLUDED.min_changed_date),
        generation = usage_overview_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `)
	if overviewFirst.query != wantDirty {
		t.Fatalf("overview 脏范围 INSERT 文本不匹配：\n%s", overviewFirst.query)
	}
	wantDirtyArgs := []string{"sys-1", "sys-1", "2026-01-05", "1", pgTestUpdatedAt, pgTestUpdatedAt}
	for i, want := range wantDirtyArgs {
		if fmt.Sprintf("%v", overviewFirst.args[i]) != want {
			t.Fatalf("overview 脏范围参数[%d] = %v, 期望 %v", i, overviewFirst.args[i], want)
		}
	}
	if fmt.Sprintf("%v", statements[len(statements)-2].args[0]) != "global" {
		t.Fatalf("overview 第二条应为 global scope：%v", statements[len(statements)-2].args)
	}
	quotaDirty := statements[len(statements)-1]
	wantQuotaDirty := bindTestPG(`
      INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
        system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `)
	if quotaDirty.query != wantQuotaDirty {
		t.Fatalf("quota 脏范围 INSERT 文本不匹配：\n%s", quotaDirty.query)
	}
	wantQuotaArgs := []string{"sys-1", "api_key", "key-1", "1", pgTestUpdatedAt, pgTestUpdatedAt}
	for i, want := range wantQuotaArgs {
		if fmt.Sprintf("%v", quotaDirty.args[i]) != want {
			t.Fatalf("quota 脏范围参数[%d] = %v, 期望 %v", i, quotaDirty.args[i], want)
		}
	}
}

// TestSubtractPostgresUsageStatsRowsFullFamily 覆盖 model / error / quality /
// latency / account health / 授权日报 / quota+AI 脏范围的完整家族。
func TestSubtractPostgresUsageStatsRowsFullFamily(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	text := func(v string) *string { return &v }
	num := func(v float64) *float64 { return &v }
	row := statsagg.UsageStatsRecordRow{
		ID: "rec-2", SystemAccountID: "sys-1", TrafficSource: "account_health_check",
		APIKeyID: text("key-1"), AccountID: text("acc-1"), ProviderCode: text("openai"),
		Model: text("gpt-x"), StatusCode: num(404), Success: 0,
		FailureAttribution: text("account_upstream"),
		FirstTokenMs:       num(120), DurationMs: num(340),
		InputTokens: num(10), OutputTokens: num(5), CostUsd: num(0.01),
		AccountOwnerSystemAccountID: text("owner-2"), AccountAccessType: text("account_authorized"),
		AccountAuthorizationID: text("auth-1"), AccountAuthorizationSourceType: text("manual"),
		CreatedAt: "2026-01-05T03:04:05.123Z",
	}
	rec.script("resource_authorizations", []string{"id", "resource_id", "instance_account_id"}, nil)

	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.subtractPostgresUsageStatsRows(context.Background(), tx, []statsagg.UsageStatsRecordRow{row}, pgTestUpdatedAt, pgTestZone); err != nil {
		t.Fatalf("subtractPostgresUsageStatsRows: %v", err)
	}

	statements := rec.all()
	// 首条为授权查找（juhe_business），其余为 tx 内扣减语句。
	if !strings.Contains(statements[0].query, "juhe_business.resource_authorizations") {
		t.Fatalf("首条应为授权查找，实际：%s", statements[0].query)
	}
	wantLookup := bindTestPG(`
      SELECT
        authorizations.id,
        authorizations.resource_id,
        instance_accounts.id AS instance_account_id
      FROM juhe_business.resource_authorizations authorizations
      LEFT JOIN juhe_business.accounts instance_accounts
        ON instance_accounts.authorization_instance_authorization_id = authorizations.id
        AND instance_accounts.system_account_id = authorizations.grantee_system_account_id
      WHERE authorizations.resource_type = 'account'
        AND authorizations.id = ANY($1::text[])
	`)
	if statements[0].query != wantLookup {
		t.Fatalf("授权查找文本不匹配：\n%s", statements[0].query)
	}

	// 语句文本按出现顺序归类，断言家族顺序（Node subtract 尾部序列）。
	texts := make([]string, 0, len(statements))
	for _, statement := range statements[1:] {
		texts = append(texts, statement.query)
	}
	assertSequence := func(description string, order []string) {
		t.Helper()
		cursor := 0
		for _, want := range order {
			found := -1
			for i := cursor; i < len(texts); i++ {
				if texts[i] == want {
					found = i
					break
				}
			}
			if found < 0 {
				t.Fatalf("%s：序列中找不到语句（自 %d 起）\n%s", description, cursor, want)
			}
			cursor = found + 1
		}
	}
	// 家族顺序：totals → 桶 → latency → model → error → quality → health → 脏范围。
	assertSequence("家族顺序", []string{
		pgTotalsSubtractSQL,
		bindTestPG(`
      UPDATE juhe_stats.usage_stats_minute
      SET request_count = GREATEST(0, request_count - $1),
          success_count = GREATEST(0, success_count - $2),
          error_count = GREATEST(0, error_count - $3),
          input_tokens = GREATEST(0, input_tokens - $4),
          output_tokens = GREATEST(0, output_tokens - $5),
          cache_read_tokens = GREATEST(0, cache_read_tokens - $6),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - $7),
          cache_write_tokens = GREATEST(0, cache_write_tokens - $8),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - $9),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - $10),
          thinking_tokens = GREATEST(0, thinking_tokens - $11),
          input_image_tokens = GREATEST(0, input_image_tokens - $12),
          output_image_tokens = GREATEST(0, output_image_tokens - $13),
          total_cost_usd = GREATEST(0, total_cost_usd - $14),
          duration_ms_sum = GREATEST(0, duration_ms_sum - $15),
          duration_ms_count = GREATEST(0, duration_ms_count - $16),
          duration_ms_max = CASE WHEN duration_ms_count <= $17 THEN 0 ELSE duration_ms_max END,
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - $18),
          first_token_ms_count = GREATEST(0, first_token_ms_count - $19),
          first_token_ms_max = CASE WHEN first_token_ms_count <= $20 THEN 0 ELSE first_token_ms_max END,
          last_used_at = CASE WHEN request_count <= $21 THEN NULL ELSE last_used_at END,
          last_error_at = CASE WHEN error_count <= $22 THEN NULL ELSE last_error_at END,
          updated_at = $23
      WHERE system_account_id = $24 AND scope_type = $25 AND scope_id = $26 AND stat_minute = $27
    `),
		bindTestPG(`
      UPDATE juhe_stats.usage_latency_minute
      SET sample_count = GREATEST(0, sample_count - $1),
          updated_at = $2
      WHERE system_account_id = $3 AND scope_type = $4 AND scope_id = $5
        AND metric_type = $6 AND stat_minute = $7 AND bucket_upper_bound_ms = $8
    `),
		bindTestPG(`
      UPDATE juhe_stats.usage_model_minute
      SET request_count = GREATEST(0, request_count - $1),
          success_count = GREATEST(0, success_count - $2),
          error_count = GREATEST(0, error_count - $3),
          input_tokens = GREATEST(0, input_tokens - $4),
          output_tokens = GREATEST(0, output_tokens - $5),
          cache_read_tokens = GREATEST(0, cache_read_tokens - $6),
          cache_read_cost_usd = GREATEST(0, cache_read_cost_usd - $7),
          cache_write_tokens = GREATEST(0, cache_write_tokens - $8),
          cache_write_1h_tokens = GREATEST(0, cache_write_1h_tokens - $9),
          cache_write_cost_usd = GREATEST(0, cache_write_cost_usd - $10),
          thinking_tokens = GREATEST(0, thinking_tokens - $11),
          input_image_tokens = GREATEST(0, input_image_tokens - $12),
          output_image_tokens = GREATEST(0, output_image_tokens - $13),
          total_cost_usd = GREATEST(0, total_cost_usd - $14),
          updated_at = $15
      WHERE system_account_id = $16 AND stat_minute = $17 AND provider_code = $18 AND model = $19
    `),
		bindTestPG(`
      UPDATE juhe_stats.usage_error_minute
      SET request_count = GREATEST(0, request_count - $1),
          error_count = GREATEST(0, error_count - $2),
          updated_at = $3
      WHERE system_account_id = $4 AND stat_minute = $5 AND error_group = $6
        AND provider_code = $7 AND error_code = $8 AND status_code = $9
    `),
		bindTestPG(`
      UPDATE juhe_stats.account_quality_minute_stats
      SET request_count = GREATEST(0, request_count - $1),
          success_count = GREATEST(0, success_count - $2),
          error_count = GREATEST(0, error_count - $3),
          first_token_ms_sum = GREATEST(0, first_token_ms_sum - $4),
          first_token_ms_count = GREATEST(0, first_token_ms_count - $5),
          updated_at = $6
      WHERE account_id = $7 AND stat_minute = $8
    `),
		bindTestPG(`DELETE FROM juhe_stats.account_health_hourly WHERE account_id = $1 AND last_record_id = $2`),
		bindTestPG(`
      INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (
        system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT(system_account_id) DO UPDATE SET
        min_stat_date = LEAST(ai_performance_summary_dirty_system_accounts.min_stat_date, EXCLUDED.min_stat_date),
        max_stat_date = GREATEST(ai_performance_summary_dirty_system_accounts.max_stat_date, EXCLUDED.max_stat_date),
        generation = ai_performance_summary_dirty_system_accounts.generation + 1,
        updated_at = EXCLUDED.updated_at
    `),
		bindTestPG(`
      INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
        system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6)
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `),
	})

	// error 扣减参数：errorCode = String(status_code) = "404"（Node ?? 语义）。
	findFirst := func(fragment string, from int) int {
		for i := from; i < len(statements); i++ {
			if strings.Contains(statements[i].query, fragment) {
				return i
			}
		}
		return -1
	}
	errorIndex := findFirst("usage_error_minute", 0)
	if errorIndex < 0 {
		t.Fatalf("未找到 error 扣减语句")
	}
	errorArgs := statements[errorIndex].args
	// 首 scope = 调用方（sys-1）；request/error 各 1，errorCode "404"，statusCode 404。
	wantErrorArgs := []string{"1", "1", pgTestUpdatedAt, "sys-1", "2026-01-05T11:04", "openai", "openai", "404", "404"}
	for i, want := range wantErrorArgs {
		if fmt.Sprintf("%v", errorArgs[i]) != want {
			t.Fatalf("error UPDATE 参数[%d] = %v, 期望 %s", i, errorArgs[i], want)
		}
	}

	// model 扣减参数：model 已 trim，providerCode 取原值。
	modelIndex := findFirst("usage_model_minute", 0)
	modelArgs := statements[modelIndex].args
	wantModelTail := []string{pgTestUpdatedAt, "sys-1", "2026-01-05T11:04", "openai", "gpt-x"}
	for i, want := range wantModelTail {
		if fmt.Sprintf("%v", modelArgs[len(modelArgs)-len(wantModelTail)+i]) != want {
			t.Fatalf("model UPDATE 尾参[%d] = %v, 期望 %s", i, modelArgs[len(modelArgs)-len(wantModelTail)+i], want)
		}
	}

	// quality 扣减参数：失败行 request 1 / success 0 / error 1，首 token 样本不计。
	qualityIndex := findFirst("account_quality_minute_stats", 0)
	qualityArgs := statements[qualityIndex].args
	wantQuality := []string{"1", "0", "1", "0", "0", pgTestUpdatedAt, "acc-1", "2026-01-05T11:04"}
	for i, want := range wantQuality {
		if fmt.Sprintf("%v", qualityArgs[i]) != want {
			t.Fatalf("quality UPDATE 参数[%d] = %v, 期望 %s", i, qualityArgs[i], want)
		}
	}

	// 授权日报：3 filter × user 2 键 × 2 scope（owner + global）= 12 条 user UPDATE。
	userUpdateCount := 0
	for _, statement := range statements[1:] {
		if strings.Contains(statement.query, "authorization_user_usage_summary_daily") &&
			strings.Contains(statement.query, "GREATEST(0, request_count - ") {
			userUpdateCount++
		}
	}
	if userUpdateCount != 12 {
		t.Fatalf("授权 user 扣减语句数 = %d, 期望 12", userUpdateCount)
	}
	authUpdateIndex := findFirst("authorization_user_usage_summary_daily", 0)
	authArgs := statements[authUpdateIndex].args
	// 首键：filter all + grantee 空 + owner-2。
	wantAuthTail := []string{pgTestUpdatedAt, "owner-2", "2026-01-05", "", "", "all", ""}
	for i, want := range wantAuthTail {
		got := authArgs[len(authArgs)-len(wantAuthTail)+i]
		if fmt.Sprintf("%v", got) != want {
			t.Fatalf("授权 UPDATE 尾参[%d] = %v, 期望 %s", i, got, want)
		}
	}

	// account health：traffic_source = account_health_check → 删除小时健康行。
	healthIndex := findFirst("account_health_hourly", 0)
	if fmt.Sprintf("%v", statements[healthIndex].args[0]) != "acc-1" ||
		fmt.Sprintf("%v", statements[healthIndex].args[1]) != "rec-2" {
		t.Fatalf("account health DELETE 参数 = %v", statements[healthIndex].args)
	}

	// quota 脏范围：api_key 与 account_authorization 两个 scope（hourly 桶）。
	quotaCount := 0
	for _, statement := range statements[1:] {
		if strings.Contains(statement.query, "usage_quota_hourly_window_dirty_scopes") &&
			strings.Contains(statement.query, "INSERT INTO") {
			quotaCount++
		}
	}
	if quotaCount != 2 {
		t.Fatalf("quota 脏范围 INSERT 数 = %d, 期望 2", quotaCount)
	}
}

// ---- api-key PG 主流程 ----

func usageRecordColumns() []string {
	return []string{
		"id", "system_account_id", "trace_id", "traffic_source", "client_ip", "api_key_id", "group_id",
		"account_id", "endpoint", "provider_code", "provider_protocol_profile_id", "model", "status_code",
		"success", "failure_attribution", "first_token_ms", "duration_ms", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens", "cache_write_1h_tokens",
		"cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens", "cost_usd",
		"error_code", "error_message", "account_owner_system_account_id", "group_owner_system_account_id",
		"account_access_type", "group_access_type", "account_authorization_id", "account_authorization_source_type",
		"account_authorization_source_team_id", "group_authorization_id", "group_authorization_source_type",
		"group_authorization_source_team_id", "created_at",
	}
}

func usageRecordDriverRow(row statsagg.UsageStatsRecordRow) []driver.Value {
	text := func(v *string) driver.Value {
		if v == nil {
			return nil
		}
		return *v
	}
	num := func(v *float64) driver.Value {
		if v == nil {
			return nil
		}
		return *v
	}
	return []driver.Value{
		row.ID, row.SystemAccountID, row.TraceID, row.TrafficSource, text(row.ClientIP),
		text(row.APIKeyID), text(row.GroupID), text(row.AccountID), text(row.Endpoint),
		text(row.ProviderCode), text(row.ProviderProtocolProfileID), text(row.Model),
		num(row.StatusCode), row.Success, text(row.FailureAttribution),
		num(row.FirstTokenMs), num(row.DurationMs),
		num(row.InputTokens), num(row.OutputTokens),
		num(row.CacheReadTokens), num(row.CacheReadCostUsd),
		num(row.CacheWriteTokens), num(row.CacheWrite1hTokens), num(row.CacheWriteCostUsd),
		num(row.ThinkingTokens), num(row.InputImageTokens), num(row.OutputImageTokens),
		num(row.CostUsd), text(row.ErrorCode), text(row.ErrorMessage),
		text(row.AccountOwnerSystemAccountID), text(row.GroupOwnerSystemAccountID),
		text(row.AccountAccessType), text(row.GroupAccessType),
		text(row.AccountAuthorizationID), text(row.AccountAuthorizationSourceType), text(row.AccountAuthorizationSourceTeamID),
		text(row.GroupAuthorizationID), text(row.GroupAuthorizationSourceType), text(row.GroupAuthorizationSourceTeamID),
		row.CreatedAt,
	}
}

func TestCleanupAPIKeyRelatedPostgresFlow(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	row := pgTestRow()
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})

	result, err := store.CleanupAPIKeyRelatedPostgres(context.Background(), "key-1", "sys-1")
	if err != nil {
		t.Fatalf("CleanupAPIKeyRelatedPostgres: %v", err)
	}
	if result.DeletedRows != 1 {
		t.Fatalf("DeletedRows = %d, 期望 1", result.DeletedRows)
	}
	if result.HasMore {
		t.Fatalf("无残余行时 HasMore 应为 false")
	}

	statements := rec.all()
	// 事务外首条：upsert 清理目标（juhe_dataset）。
	if !strings.Contains(statements[0].query, "juhe_dataset.api_key_record_cleanup_targets") ||
		!strings.Contains(statements[0].query, "INSERT INTO") {
		t.Fatalf("首条应为目标 upsert：%s", statements[0].query)
	}
	if statements[0].args[0] != "key-1" || statements[0].args[1] != "sys-1" {
		t.Fatalf("目标 upsert 参数 = %v", statements[0].args)
	}
	// 事务内：floor 游标 → 批次选择 → 台账 → FOR UPDATE → 扣减 → 目录 → 分区删除 → 台账标记。
	cursorQuery := statements[1]
	if !strings.Contains(cursorQuery.query, "juhe_stats.stats_job_state") {
		t.Fatalf("第二条应为 floor 游标查询：%s", cursorQuery.query)
	}
	wantCursor := bindTestPG(`
    SELECT job_name, cursor_created_at, cursor_id
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY($1::text[])
      AND cursor_created_at IS NOT NULL
      AND cursor_id IS NOT NULL
    ORDER BY cursor_created_at ASC, cursor_id ASC
  `)
	if cursorQuery.query != wantCursor {
		t.Fatalf("floor 游标文本不匹配：\n%s", cursorQuery.query)
	}
	selectIndex := -1
	for i, statement := range statements {
		if strings.Contains(statement.query, "FROM juhe_usage.usage_records") {
			selectIndex = i
			break
		}
	}
	if selectIndex < 0 {
		t.Fatalf("未找到批次选择语句")
	}
	selectStatement := statements[selectIndex]
	wantSelect := bindTestPG(fmt.Sprintf(`
    SELECT %s
    FROM juhe_usage.usage_records
    WHERE api_key_id = $1
      AND system_account_id = $2
      AND (created_at < $3 OR (created_at = $4 AND id <= $5))
    ORDER BY created_at ASC, id ASC
    LIMIT $6
  `, usageStatsRecordSelectColumns))
	if selectStatement.query != wantSelect {
		t.Fatalf("批次选择文本不匹配：\n%s", selectStatement.query)
	}
	wantSelectArgs := []string{"key-1", "sys-1", "2026-01-05T03:00:00.000Z", "2026-01-05T03:00:00.000Z", "rec-0", "100"}
	if len(selectStatement.args) != len(wantSelectArgs) {
		t.Fatalf("批次选择参数数 = %d", len(selectStatement.args))
	}
	for i, want := range wantSelectArgs {
		if fmt.Sprintf("%v", selectStatement.args[i]) != want {
			t.Fatalf("批次选择参数[%d] = %v, 期望 %s", i, selectStatement.args[i], want)
		}
	}
	// 台账 INSERT：shard key 固定 postgres。
	deductionIndex := -1
	for i := selectIndex + 1; i < len(statements); i++ {
		if strings.Contains(statements[i].query, "usage_record_cleanup_deductions") {
			deductionIndex = i
			break
		}
	}
	deduction := statements[deductionIndex]
	wantDeduction := bindTestPG(`
      INSERT INTO juhe_stats.usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, NULL, NULL, $7, $8)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        api_key_id = EXCLUDED.api_key_id,
        account_id = COALESCE(usage_record_cleanup_deductions.account_id, EXCLUDED.account_id),
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `)
	if deduction.query != wantDeduction {
		t.Fatalf("台账 INSERT 文本不匹配：\n%s", deduction.query)
	}
	if fmt.Sprintf("%v", deduction.args[0]) != "rec-1" || fmt.Sprintf("%v", deduction.args[1]) != "key-1" ||
		fmt.Sprintf("%v", deduction.args[4]) != "postgres" || fmt.Sprintf("%v", deduction.args[7]) != pgTestUpdatedAt {
		t.Fatalf("台账 INSERT 参数 = %v", deduction.args)
	}
	// 分区键删除。
	partitionIndex := -1
	for i := deductionIndex; i < len(statements); i++ {
		if strings.Contains(statements[i].query, "DELETE FROM juhe_usage.usage_records") {
			partitionIndex = i
			break
		}
	}
	if partitionIndex < 0 {
		t.Fatalf("未找到分区键删除")
	}
	wantPartition := bindTestPG(`DELETE FROM juhe_usage.usage_records
    WHERE (created_at, id) IN (($1, $2))`)
	if statements[partitionIndex].query != wantPartition {
		t.Fatalf("分区键删除文本不匹配：\n%s", statements[partitionIndex].query)
	}
	if fmt.Sprintf("%v", statements[partitionIndex].args[0]) != row.CreatedAt ||
		fmt.Sprintf("%v", statements[partitionIndex].args[1]) != "rec-1" {
		t.Fatalf("分区键删除参数 = %v", statements[partitionIndex].args)
	}
	// 事务提交后：hasUsageMore → final stats → 残余检查 → 清除目标。
	clearIndex := len(statements) - 1
	if !strings.Contains(statements[clearIndex].query, "DELETE FROM juhe_dataset.api_key_record_cleanup_targets") {
		t.Fatalf("末条应为目标清除：%s", statements[clearIndex].query)
	}
	finalStatsDeletes := 0
	for _, statement := range statements {
		if strings.Contains(statement.query, "scope_type = 'api_key'") && strings.Contains(statement.query, "DELETE FROM juhe_stats.") {
			finalStatsDeletes++
		}
	}
	// 14 张 scope 表 + stats_job_state = 15。
	if finalStatsDeletes != 15 {
		t.Fatalf("final stats 删除语句数 = %d, 期望 15", finalStatsDeletes)
	}
	if rec.commits != 2 || rec.begins != 2 {
		t.Fatalf("事务数 begins=%d commits=%d, 期望 2/2", rec.begins, rec.commits)
	}
}

// TestCleanupAPIKeyRelatedPostgresSkipsSubtractedRows：台账已有
// stats_subtracted_at 时不再扣减（单次扣减语义）。
func TestCleanupAPIKeyRelatedPostgresSkipsSubtractedRows(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	row := pgTestRow()
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
	rec.script("SELECT stats_subtracted_at", []string{"stats_subtracted_at"}, [][]driver.Value{
		{"2026-01-05T04:00:00.000Z"},
	})

	if _, err := store.CleanupAPIKeyRelatedPostgres(context.Background(), "key-1", "sys-1"); err != nil {
		t.Fatalf("CleanupAPIKeyRelatedPostgres: %v", err)
	}
	for _, statement := range rec.all() {
		if strings.Contains(statement.query, "GREATEST(0, request_count - ") {
			t.Fatalf("已扣减行不应再触发统计扣减：%s", statement.query)
		}
	}
}

// TestCleanupPendingAPIKeyTargetsPostgresSummary：pending 汇总与失败标记。
func TestCleanupPendingAPIKeyTargetsPostgresSummary(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	rec.script("juhe_dataset.api_key_record_cleanup_targets", []string{"api_key_id", "system_account_id"}, [][]driver.Value{
		{"key-1", "sys-1"},
		{"key-2", "sys-1"},
	})
	// key-1：无游标（floor nil）→ 0 行删除、无 usage 残余、final stats 清空、无残余 → completed。
	// key-2：游标齐备但批次选择无行 → usageIDs 空 → 同样 completed。
	result, err := store.CleanupPendingAPIKeyTargetsPostgres(context.Background(), 50)
	if err != nil {
		t.Fatalf("CleanupPendingAPIKeyTargetsPostgres: %v", err)
	}
	if result.Attempted != 2 || result.Completed != 2 || result.Failed != 0 || result.Deferred != 0 {
		t.Fatalf("summary = %+v", result)
	}
}

// ---- account PG 主流程 ----

func TestCleanupAccountRelatedPostgresFlow(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	text := func(v string) *string { return &v }
	row := pgTestRow()
	row.AccountID = text("acc-1")
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})

	result, err := store.CleanupAccountRelatedPostgres(context.Background(), retentionTarget())
	if err != nil {
		t.Fatalf("CleanupAccountRelatedPostgres: %v", err)
	}
	if result.DeletedRows != 1 {
		t.Fatalf("DeletedRows = %d, 期望 1", result.DeletedRows)
	}
	statements := rec.all()
	selectIndex := -1
	for i, statement := range statements {
		if strings.Contains(statement.query, "FROM juhe_usage.usage_records") {
			selectIndex = i
			break
		}
	}
	wantSelect := bindTestPG(fmt.Sprintf(`
    SELECT %s
    FROM juhe_usage.usage_records
    WHERE (account_id = ANY($1::text[]) OR account_authorization_id = ANY($2::text[]))
      AND (created_at < $3 OR (created_at = $4 AND id <= $5))
    ORDER BY created_at ASC, id ASC
    LIMIT $6
  `, usageStatsRecordSelectColumns))
	if statements[selectIndex].query != wantSelect {
		t.Fatalf("account 批次选择文本不匹配：\n%s", statements[selectIndex].query)
	}
	// ANY 数组参数按 []string 原样传递。
	accounts, ok := statements[selectIndex].args[0].([]string)
	if !ok || len(accounts) != 2 || accounts[0] != "acc-1" || accounts[1] != "acc-rel-1" {
		t.Fatalf("account ANY 参数 = %v", statements[selectIndex].args[0])
	}
	authorizations, ok := statements[selectIndex].args[1].([]string)
	if !ok || len(authorizations) != 1 || authorizations[0] != "auth-1" {
		t.Fatalf("authorization ANY 参数 = %v", statements[selectIndex].args[1])
	}
	// account 台账 INSERT 的 COALESCE 方向与 api-key 变体相反。
	deductionIndex := -1
	for i := selectIndex + 1; i < len(statements); i++ {
		if strings.Contains(statements[i].query, "usage_record_cleanup_deductions") {
			deductionIndex = i
			break
		}
	}
	wantDeduction := bindTestPG(`
      INSERT INTO juhe_stats.usage_record_cleanup_deductions (
        usage_id, api_key_id, account_id, system_account_id, source_shard_key, record_json,
        stats_subtracted_at, shard_deleted_at, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, NULL, NULL, $7, $8)
      ON CONFLICT(usage_id, source_shard_key) DO UPDATE SET
        api_key_id = COALESCE(usage_record_cleanup_deductions.api_key_id, EXCLUDED.api_key_id),
        account_id = EXCLUDED.account_id,
        system_account_id = EXCLUDED.system_account_id,
        record_json = EXCLUDED.record_json,
        updated_at = EXCLUDED.updated_at
    `)
	if statements[deductionIndex].query != wantDeduction {
		t.Fatalf("account 台账 INSERT 文本不匹配：\n%s", statements[deductionIndex].query)
	}
	// final stats：account scope 清理（LIKE + ANY 混合）。
	accountScopeDeletes := 0
	for _, statement := range statements {
		if strings.Contains(statement.query, "scope_type IN ('account', 'caller_account') AND scope_id = ANY") {
			accountScopeDeletes++
		}
	}
	// 14 张 scope 表 + stats_job_state = 15。
	if accountScopeDeletes != 15 {
		t.Fatalf("account scope 删除语句数 = %d, 期望 15", accountScopeDeletes)
	}
	likeTeamDeletes := 0
	for _, statement := range statements {
		if strings.Contains(statement.query, "scope_id LIKE ? ESCAPE") || strings.Contains(statement.query, "scope_id LIKE $") {
			likeTeamDeletes++
		}
	}
	// accountIds 2 个 × (14 张表 + stats_job_state) = 30 条 LIKE 前缀清理。
	if likeTeamDeletes != 30 {
		t.Fatalf("LIKE 前缀清理语句数 = %d, 期望 30", likeTeamDeletes)
	}
	// 残余检查（account scope）之后清除目标。
	if !strings.Contains(statements[len(statements)-1].query, "DELETE FROM juhe_dataset.account_record_cleanup_targets") {
		t.Fatalf("末条应为 account 目标清除：%s", statements[len(statements)-1].query)
	}
}

func retentionTarget() retention.ExpiredDeletedAccountTarget {
	return retention.ExpiredDeletedAccountTarget{
		AccountID:         "acc-1",
		SystemAccountID:   "sys-1",
		RelatedAccountIDs: []string{"acc-rel-1"},
		AuthorizationIDs:  []string{"auth-1"},
	}
}

// ---- SQLite 对照语义互证 ----

const sqliteInterlockSchema = `
CREATE TABLE usage_stats_totals (
  system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id)
);
CREATE TABLE usage_stats_minute (stat_minute TEXT NOT NULL, system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_minute));
CREATE TABLE usage_stats_hourly (stat_hour TEXT NOT NULL, system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour));
CREATE TABLE usage_stats_daily (stat_date TEXT NOT NULL, system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date));
CREATE TABLE usage_stats_weekly (stat_week TEXT NOT NULL, system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week));
CREATE TABLE usage_stats_monthly (stat_month TEXT NOT NULL, system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  input_tokens REAL DEFAULT 0, output_tokens REAL DEFAULT 0,
  cache_read_tokens REAL DEFAULT 0, cache_read_cost_usd REAL DEFAULT 0,
  cache_write_tokens REAL DEFAULT 0, cache_write_1h_tokens REAL DEFAULT 0,
  cache_write_cost_usd REAL DEFAULT 0, thinking_tokens REAL DEFAULT 0,
  input_image_tokens REAL DEFAULT 0, output_image_tokens REAL DEFAULT 0,
  total_cost_usd REAL DEFAULT 0, duration_ms_sum REAL DEFAULT 0,
  duration_ms_count REAL DEFAULT 0, duration_ms_max REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  first_token_ms_max REAL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month));
CREATE TABLE usage_latency_minute (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  stat_minute TEXT NOT NULL, metric_type TEXT NOT NULL, bucket_upper_bound_ms INTEGER NOT NULL,
  sample_count REAL DEFAULT 0, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_minute, metric_type, bucket_upper_bound_ms));
CREATE TABLE usage_latency_hourly (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  stat_hour TEXT NOT NULL, metric_type TEXT NOT NULL, bucket_upper_bound_ms INTEGER NOT NULL,
  sample_count REAL DEFAULT 0, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour, metric_type, bucket_upper_bound_ms));
CREATE TABLE usage_latency_daily (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  stat_date TEXT NOT NULL, metric_type TEXT NOT NULL, bucket_upper_bound_ms INTEGER NOT NULL,
  sample_count REAL DEFAULT 0, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date, metric_type, bucket_upper_bound_ms));
CREATE TABLE usage_latency_weekly (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  stat_week TEXT NOT NULL, metric_type TEXT NOT NULL, bucket_upper_bound_ms INTEGER NOT NULL,
  sample_count REAL DEFAULT 0, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_week, metric_type, bucket_upper_bound_ms));
CREATE TABLE usage_latency_monthly (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
  stat_month TEXT NOT NULL, metric_type TEXT NOT NULL, bucket_upper_bound_ms INTEGER NOT NULL,
  sample_count REAL DEFAULT 0, updated_at TEXT,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_month, metric_type, bucket_upper_bound_ms));
CREATE TABLE account_quality_minute_stats (
  account_id TEXT NOT NULL, stat_minute TEXT NOT NULL,
  request_count REAL DEFAULT 0, success_count REAL DEFAULT 0, error_count REAL DEFAULT 0,
  first_token_ms_sum REAL DEFAULT 0, first_token_ms_count REAL DEFAULT 0,
  last_sample_at TEXT, last_success_at TEXT, last_error_at TEXT, last_error_message TEXT, updated_at TEXT,
  PRIMARY KEY (account_id, stat_minute)
);
CREATE TABLE account_quality_dirty_accounts (
  account_id TEXT PRIMARY KEY, first_dirty_at TEXT, updated_at TEXT
);
CREATE TABLE usage_record_cleanup_deductions (
  usage_id TEXT NOT NULL, source_shard_key TEXT NOT NULL,
  api_key_id TEXT, account_id TEXT, system_account_id TEXT, record_json TEXT,
  stats_subtracted_at TEXT, shard_deleted_at TEXT, created_at TEXT, updated_at TEXT,
  PRIMARY KEY (usage_id, source_shard_key)
);
`

func openSQLiteInterlockDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/interlock.sqlite3")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range strings.Split(sqliteInterlockSchema, ";") {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" {
			continue
		}
		if _, err := db.ExecContext(context.Background(), trimmed); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// TestSQLiteInterlockSubtractSemantics：同一行在 SQLite 路径（真实库执行）与
// PG 路径（录制驱动捕获的扣减参数）下产生一致的扣减量与台账门控。
func TestSQLiteInterlockSubtractSemantics(t *testing.T) {
	row := pgTestRow()
	row.DurationMs = nil
	row.FirstTokenMs = nil // SQLite 路径无 latency 表写入（与本测试 schema 一致）
	accumulator := statsagg.UsageStatsAccumulatorFromRecord(row)

	// -- SQLite 侧 --
	sqliteDB := openSQLiteInterlockDB(t)
	rowWithShard := row
	rowWithShard.SourceShardKey = "shard-a"
	if _, err := sqliteDB.ExecContext(context.Background(), `
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, success_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd, updated_at)
    VALUES ('sys-1', 'system_account', 'sys-1', 5, 4, 100, 50, 10, 1, 2, '2026-01-01T00:00:00.000Z')
  `); err != nil {
		t.Fatalf("seed totals: %v", err)
	}
	store := &RecordCleanupStore{
		Stats: &DB{DB: sqliteDB},
		Now: func() time.Time {
			return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
		},
		Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
	}
	rows := []map[string]any{statsaggRowToMap(rowWithShard, "shard-a")}
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, pgTestUpdatedAt, false, pgTestZone); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData (SQLite): %v", err)
	}
	var sqliteRequest, sqliteInput float64
	if err := sqliteDB.QueryRowContext(context.Background(), `
    SELECT request_count, input_tokens FROM usage_stats_totals
    WHERE system_account_id = 'sys-1' AND scope_type = 'system_account' AND scope_id = 'sys-1'
  `).Scan(&sqliteRequest, &sqliteInput); err != nil {
		t.Fatalf("read totals: %v", err)
	}
	if sqliteRequest != 4 || sqliteInput != 90 {
		t.Fatalf("SQLite 扣减结果 request=%v input=%v, 期望 4/90", sqliteRequest, sqliteInput)
	}
	var subtractedAt sql.NullString
	if err := sqliteDB.QueryRowContext(context.Background(), `
    SELECT stats_subtracted_at FROM usage_record_cleanup_deductions WHERE usage_id = 'rec-1'
  `).Scan(&subtractedAt); err != nil || !subtractedAt.Valid {
		t.Fatalf("SQLite 台账 stats_subtracted_at 缺失：%v", err)
	}
	// 第二次结算同一行：stats_subtracted_at 门控，不再扣减。
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, pgTestUpdatedAt, false, pgTestZone); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	var requestAgain float64
	if err := sqliteDB.QueryRowContext(context.Background(), `
    SELECT request_count FROM usage_stats_totals
    WHERE system_account_id = 'sys-1' AND scope_type = 'system_account' AND scope_id = 'sys-1'
  `).Scan(&requestAgain); err != nil {
		t.Fatalf("re-read totals: %v", err)
	}
	if requestAgain != 4 {
		t.Fatalf("台账门控失效：二次结算后 request_count = %v, 期望 4", requestAgain)
	}

	// -- PG 侧：同一行的扣减参数必须与 SQLite 扣减量同源 --
	rec := newPGRecorder()
	pgStore := newPGTestStore(rec)
	tx, err := pgStore.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := pgStore.subtractPostgresUsageStatsRows(context.Background(), tx, []statsagg.UsageStatsRecordRow{row}, pgTestUpdatedAt, pgTestZone); err != nil {
		t.Fatalf("subtractPostgresUsageStatsRows: %v", err)
	}
	var totalsUpdate *recordedStatement
	for i, statement := range rec.all() {
		if strings.Contains(statement.query, "UPDATE juhe_stats.usage_stats_totals") {
			if statement.args[24] == "system_account" && statement.args[25] == "sys-1" {
				totalsUpdate = &rec.all()[i]
				break
			}
		}
	}
	if totalsUpdate == nil {
		t.Fatalf("未找到 (sys-1, system_account, sys-1) 的 totals 扣减")
	}
	// 扣减量互证：SQLite 实际减量 == PG 参数（同一 accumulator）。
	pgRequest := totalsUpdate.args[0].(float64)
	pgInput := totalsUpdate.args[3].(float64)
	if 5-pgRequest != sqliteRequest || 100-pgInput != sqliteInput {
		t.Fatalf("扣减量不一致：SQLite 减量 request=%v input=%v；PG 参数 request=%v input=%v",
			5-sqliteRequest, 100-sqliteInput, pgRequest, pgInput)
	}
	if accumulator.RequestCount != 1 || accumulator.InputTokens != 10 {
		t.Fatalf("accumulator 漂移：%+v", accumulator)
	}
}

// ---- UpsertAccountUsageSnapshots PG 语句 ----

func TestUpsertAccountUsageSnapshotsPostgresStatement(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	business := openRecorderPG(rec)
	rec.script("juhe_business.accounts", []string{"system_account_id"}, [][]driver.Value{
		{"owner-1"},
	})
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 1.0}},
	}); err != nil {
		t.Fatalf("UpsertAccountUsageSnapshots: %v", err)
	}
	statements := rec.all()
	var snapshot *recordedStatement
	for i := range statements {
		if strings.Contains(statements[i].query, "INSERT INTO juhe_stats.account_usage_snapshots") {
			snapshot = &statements[i]
		}
	}
	if snapshot == nil {
		t.Fatalf("未找到 PG 快照 upsert 语句")
	}
	want := bindTestPG(`
      INSERT INTO juhe_stats.account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, refresh_status,
        last_success_at, last_error_message, updated_at, created_at
      )
      VALUES ($1, $2, $3, $4, $5, 'fresh', $6, NULL, $7, $8)
      ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
        system_account_id = EXCLUDED.system_account_id,
        source = EXCLUDED.source,
        snapshot_json = EXCLUDED.snapshot_json,
        refresh_status = 'fresh',
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = NULL,
        updated_at = EXCLUDED.updated_at
		`)
	if snapshot.query != want {
		t.Fatalf("PG 快照 upsert 文本不匹配：\n%s", snapshot.query)
	}
	if fmt.Sprintf("%v", snapshot.args[0]) != "owner-1" || fmt.Sprintf("%v", snapshot.args[1]) != "acc-1" {
		t.Fatalf("快照 upsert 参数 = %v", snapshot.args)
	}
}
