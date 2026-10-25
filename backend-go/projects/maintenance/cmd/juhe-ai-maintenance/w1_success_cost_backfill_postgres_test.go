package main

// 真实 PostgreSQL 成功成本回填门禁（显式 opt-in）：
// JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL 为空时跳过。测试只接受专用开发库，
// 先确认 juhe_stats schema 不存在再创建；DDL 只从当前 schema.Statements()
// 筛取六张 usage_stats 表、这些表的全部 ALTER TABLE 语句及 backfill_progress，
// 以便 fixture 具备现行生产列集（包括后续 PostgreSQL 写路径使用的列）。
// 夹具使用 pg_gate_ 前缀，清理只删除本测试创建的行和 marker，不删除 schema 或库。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var successCostPGDatabasePattern = regexp.MustCompile(`^juhe_ai_sub2api_dev_[a-z][a-z0-9_]{2,39}$`)

type successCostPGTraceContextKey struct{}

type successCostPGTraceContext struct {
	SQL       string
	StartedAt time.Time
}

type successCostPGTraceEvent struct {
	SQL       string
	StartedAt time.Time
	Finished  time.Time
}

type successCostPGTracer struct {
	mu     sync.Mutex
	events []successCostPGTraceEvent
}

func (tracer *successCostPGTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, successCostPGTraceContextKey{}, successCostPGTraceContext{
		SQL:       data.SQL,
		StartedAt: time.Now(),
	})
}

func (tracer *successCostPGTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	traceContext, ok := ctx.Value(successCostPGTraceContextKey{}).(successCostPGTraceContext)
	if !ok {
		return
	}
	tracer.mu.Lock()
	tracer.events = append(tracer.events, successCostPGTraceEvent{
		SQL:       traceContext.SQL,
		StartedAt: traceContext.StartedAt,
		Finished:  time.Now(),
	})
	tracer.mu.Unlock()
}

func (tracer *successCostPGTracer) reset() {
	tracer.mu.Lock()
	tracer.events = nil
	tracer.mu.Unlock()
}

func (tracer *successCostPGTracer) snapshot() []successCostPGTraceEvent {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	return append([]successCostPGTraceEvent(nil), tracer.events...)
}

// successCostPGGateReport 不携带 DSN、用户名或密码；仅保存可复核的版本、
// 行数、结果和每表批事务时延。报告只有设置 JUHE_AI_SUCCESS_COST_TEST_REPORT
// 时才落盘。
type successCostPGGateReport struct {
	Version         int                              `json:"version"`
	PostgresVersion string                           `json:"postgresVersion"`
	RowCounts       map[string]int                   `json:"rowCounts"`
	Results         map[string]successCostPGTableRun `json:"results"`
}

type successCostPGTableRun struct {
	Rows         int     `json:"rows"`
	MatchedRows  int     `json:"matchedRows"`
	Batches      int     `json:"batches"`
	BatchTimings []int64 `json:"batchTimingsMs"`
	MaxBatchMS   int64   `json:"maxBatchMs"`
}

func writeSuccessCostPGGateReport(t *testing.T, report successCostPGGateReport) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("JUHE_AI_SUCCESS_COST_TEST_REPORT"))
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Errorf("创建 PostgreSQL 门禁报告目录失败: %v", err)
		return
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Errorf("编码 PostgreSQL 门禁报告失败: %v", err)
		return
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Errorf("写入 PostgreSQL 门禁报告失败: %v", err)
	}
}

func successCostPGURL(t *testing.T) string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL"))
	if raw == "" {
		t.Skip("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL 未设置，跳过真实 PostgreSQL 门禁")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatalf("JUHE_AI_SUCCESS_COST_TEST_POSTGRES_URL 不是合法 PostgreSQL URL（DSN 已隐藏）")
	}
	databaseName := strings.TrimPrefix(u.Path, "/")
	if !successCostPGDatabasePattern.MatchString(databaseName) {
		t.Fatalf("PostgreSQL 门禁拒绝非专用开发库名称（database=%q）", databaseName)
	}
	return raw
}

func successCostPGRedactedText(rawURL string, message string) string {
	message = strings.ReplaceAll(message, rawURL, "[redacted PostgreSQL URL]")
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return message
	}
	secrets := []string{}
	if parsed.User != nil {
		secrets = append(secrets, parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	for _, values := range parsed.Query() {
		secrets = append(secrets, values...)
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if encoded != "" {
				message = strings.ReplaceAll(message, encoded, "[redacted]")
			}
		}
	}
	return message
}

func successCostPGRedactedError(rawURL string, err error) string {
	if err == nil {
		return ""
	}
	return successCostPGRedactedText(rawURL, err.Error())
}

func successCostPGBuildDB(t *testing.T, rawURL string, tracer *successCostPGTracer, requireFreshSchema bool) *sql.DB {
	t.Helper()
	config, err := pgx.ParseConfig(rawURL)
	if err != nil {
		t.Fatalf("解析 PostgreSQL 门禁连接配置失败: %s", successCostPGRedactedError(rawURL, err))
	}
	config.Tracer = tracer
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("连接 PostgreSQL 门禁库失败: %s", successCostPGRedactedError(rawURL, err))
	}
	var currentDatabase string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		db.Close()
		t.Fatalf("读取 current_database 失败: %s", successCostPGRedactedError(rawURL, err))
	}
	parsed, _ := url.Parse(rawURL)
	wantDatabase := strings.TrimPrefix(parsed.Path, "/")
	if currentDatabase != wantDatabase {
		db.Close()
		t.Fatalf("PostgreSQL current_database=%q 与 URL database=%q 不一致", currentDatabase, wantDatabase)
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('juhe_stats') IS NOT NULL").Scan(&schemaExists); err != nil {
		db.Close()
		t.Fatalf("探测 juhe_stats schema 失败: %s", successCostPGRedactedError(rawURL, err))
	}
	if schemaExists && requireFreshSchema {
		db.Close()
		t.Fatalf("PostgreSQL 门禁要求 juhe_stats schema 预先不存在；为避免触碰其他 fixture 已停止")
	}
	if !schemaExists {
		if _, err := db.ExecContext(ctx, "CREATE SCHEMA juhe_stats"); err != nil {
			db.Close()
			t.Fatalf("创建 juhe_stats schema 失败: %s", successCostPGRedactedError(rawURL, err))
		}
	}
	if _, err := db.ExecContext(ctx, "SET search_path TO juhe_stats, public"); err != nil {
		db.Close()
		t.Fatalf("设置 juhe_stats search_path 失败: %s", successCostPGRedactedError(rawURL, err))
	}
	return db
}

func successCostPGTableName(stmt string, keyword string) string {
	lower := strings.ToLower(stmt)
	idx := strings.Index(lower, keyword)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(stmt[idx+len(keyword):])
	if strings.HasPrefix(strings.ToLower(rest), "if not exists") {
		rest = strings.TrimSpace(rest[len("if not exists"):])
	}
	if rest == "" {
		return ""
	}
	token := strings.Fields(rest)[0]
	token = strings.TrimSuffix(token, "(")
	token = strings.Trim(token, "\"`")
	return token
}

func successCostPGDDLSubset(t *testing.T) ([]string, map[string][]string, string) {
	t.Helper()
	targets := map[string]bool{}
	for _, target := range successCostBackfillTargets {
		targets[target.name] = true
	}
	create := make(map[string]string)
	alter := make(map[string][]string)
	marker := ""
	for _, stmt := range schema.Statements() {
		lower := strings.ToLower(strings.TrimSpace(stmt))
		if strings.HasPrefix(lower, "create table") {
			name := successCostPGTableName(stmt, "create table")
			if name == "backfill_progress" {
				marker = stmt
			} else if targets[name] {
				create[name] = stmt
			}
		}
		if strings.HasPrefix(lower, "alter table") {
			name := successCostPGTableName(stmt, "alter table")
			if targets[name] {
				alter[name] = append(alter[name], stmt)
			}
		}
	}
	for _, target := range successCostBackfillTargets {
		if create[target.name] == "" {
			t.Fatalf("schema.Statements() 缺少 %s CREATE TABLE", target.name)
		}
		hasSuccessCostColumn := false
		for _, stmt := range alter[target.name] {
			if strings.Contains(strings.ToLower(stmt), "success_cost_usd") {
				hasSuccessCostColumn = true
				break
			}
		}
		if !hasSuccessCostColumn {
			t.Fatalf("schema.Statements() 的 %s ALTER TABLE 未包含 success_cost_usd 列", target.name)
		}
	}
	if marker == "" {
		t.Fatal("schema.Statements() 未找到 backfill_progress CREATE TABLE")
	}
	var creates []string
	for _, target := range successCostBackfillTargets {
		creates = append(creates, create[target.name])
	}
	return creates, alter, marker
}

func successCostPGApplyDDL(t *testing.T, db *sql.DB) {
	t.Helper()
	creates, alters, marker := successCostPGDDLSubset(t)
	ctx := context.Background()
	for _, stmt := range creates {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("创建成功成本目标表失败: %v", err)
		}
	}
	for _, target := range successCostBackfillTargets {
		for _, stmt := range alters[target.name] {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("应用 %s 当前生产列 DDL 失败: %v", target.name, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, marker); err != nil {
		t.Fatalf("创建 backfill_progress 失败: %v", err)
	}
}

func successCostPGCheckPrimaryKeys(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, target := range successCostBackfillTargets {
		rows, err := db.QueryContext(ctx, `
SELECT a.attname
FROM pg_constraint c
CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS key(attnum, ord)
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = key.attnum
WHERE c.conrelid = to_regclass($1) AND c.contype = 'p'
ORDER BY key.ord`, "juhe_stats."+target.name)
		if err != nil {
			t.Fatalf("读取 %s 主键失败: %v", target.name, err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatalf("读取 %s 主键列失败: %v", target.name, err)
			}
			got = append(got, name)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("遍历 %s 主键失败: %v", target.name, err)
		}
		rows.Close()
		if strings.Join(got, "\x00") != strings.Join(target.pkColumns, "\x00") {
			t.Fatalf("%s 主键=%v，与生产 target=%v 不一致", target.name, got, target.pkColumns)
		}
	}
}

func successCostPGFixtureDimension(target successCostBackfillTarget) string {
	for _, col := range target.pkColumns {
		if strings.HasPrefix(col, "stat_") {
			formats := map[string][2]string{
				"stat_minute": {"1 minute", `YYYY-MM-DD"T"HH24:MI`},
				"stat_hour":   {"1 hour", `YYYY-MM-DD"T"HH24:00`},
				"stat_date":   {"1 day", `YYYY-MM-DD`},
				"stat_week":   {"1 week", `IYYY-"W"IW`},
				"stat_month":  {"1 month", `YYYY-MM`},
			}
			format := formats[col]
			return fmt.Sprintf("to_char(timestamp '2020-01-01 00:00:00' + (g * interval '%s'), '%s')", format[0], format[1])
		}
	}
	return ""
}

func successCostPGInsertRows(t *testing.T, db *sql.DB, target successCostBackfillTarget, prefix string, rows int) {
	t.Helper()
	keys := []string{"'pg_gate_' || $1 || '_' || lpad(g::text, 6, '0')", "'system'", "'pg_gate_' || $1 || '_' || lpad(g::text, 6, '0')"}
	if dimension := successCostPGFixtureDimension(target); dimension != "" {
		keys = append(keys, dimension)
	}
	columns := append(append([]string{}, target.pkColumns...), "error_count", "total_cost_usd", "success_cost_usd", "updated_at")
	query := fmt.Sprintf(`INSERT INTO juhe_stats.%s (%s)
SELECT %s,
       CASE WHEN g = 1 THEN 1 ELSE 0 END,
       CASE WHEN g = 1 THEN 12 ELSE 10 END,
	       CASE WHEN g = 2 THEN 10 ELSE 0 END,
       '2026-01-01T00:00:00.000Z'
FROM generate_series(1, $2::int) AS series(g)`, target.name, strings.Join(columns, ", "), strings.Join(keys, ", "))
	if _, err := db.ExecContext(context.Background(), query, prefix, rows); err != nil {
		t.Fatalf("写入 %s fixture 失败: %v", target.name, err)
	}
}

func successCostPGClearFixture(t *testing.T, db *sql.DB, clearMarkers bool) {
	t.Helper()
	ctx := context.Background()
	for _, target := range successCostBackfillTargets {
		if _, err := db.ExecContext(ctx, "DELETE FROM juhe_stats."+target.name+" WHERE system_account_id LIKE 'pg!_gate!_%' ESCAPE '!'"); err != nil {
			t.Fatalf("清理 %s fixture 失败: %v", target.name, err)
		}
	}
	if clearMarkers {
		for _, target := range successCostBackfillTargets {
			if _, err := db.ExecContext(ctx, "DELETE FROM juhe_stats.backfill_progress WHERE table_name = $1", target.name); err != nil {
				t.Fatalf("清理 %s fixture marker 失败: %v", target.name, err)
			}
		}
	}
}

func successCostPGCountRows(t *testing.T, db *sql.DB, target successCostBackfillTarget) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM juhe_stats."+target.name+" WHERE system_account_id LIKE 'pg!_gate!_%' ESCAPE '!'").Scan(&count); err != nil {
		t.Fatalf("统计 %s fixture 行数失败: %v", target.name, err)
	}
	return count
}

func successCostPGBatchTimings(tracer *successCostPGTracer, target successCostBackfillTarget) []time.Duration {
	needle := "update juhe_stats." + target.name + " set success_cost_usd"
	events := tracer.snapshot()
	var timings []time.Duration
	var begin time.Time
	var matched bool
	for _, event := range events {
		sqlText := strings.ToLower(strings.TrimSpace(event.SQL))
		switch {
		case strings.HasPrefix(sqlText, "begin"):
			begin = event.StartedAt
			matched = false
		case begin.IsZero():
		case strings.HasPrefix(sqlText, needle):
			matched = true
		case strings.HasPrefix(sqlText, "commit"):
			if matched {
				timings = append(timings, event.Finished.Sub(begin))
			}
			begin = time.Time{}
			matched = false
		}
	}
	return timings
}

func successCostPGMarkers(t *testing.T, db *sql.DB) map[string][2]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT table_name, status, cursor_json FROM juhe_stats.backfill_progress")
	if err != nil {
		t.Fatalf("读取 PG marker 失败: %v", err)
	}
	defer rows.Close()
	markers := map[string][2]string{}
	for rows.Next() {
		var name, status, cursor string
		if err := rows.Scan(&name, &status, &cursor); err != nil {
			t.Fatalf("扫描 PG marker 失败: %v", err)
		}
		markers[name] = [2]string{status, cursor}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 PG marker 失败: %v", err)
	}
	return markers
}

func successCostPGRunTarget(t *testing.T, db *sql.DB, target successCostBackfillTarget) successCostTableStat {
	t.Helper()
	stat, err := backfillSuccessCostTable(context.Background(), db, backfillDialect{}, target)
	if err != nil {
		t.Fatalf("回填 %s 失败: %v", target.name, err)
	}
	return stat
}

func TestSuccessCostBackfillPostgresGate(t *testing.T) {
	rawURL := successCostPGURL(t)
	if strings.TrimSpace(os.Getenv("JUHE_AI_SUCCESS_COST_TEST_REPORT")) == "" {
		t.Fatal("设置 JUHE_AI_SUCCESS_COST_TEST_REPORT 指向 reports 下的 JSON 文件，才能保存 PostgreSQL 门禁结果")
	}
	report := successCostPGGateReport{Version: 1, RowCounts: map[string]int{}, Results: map[string]successCostPGTableRun{}}
	t.Cleanup(func() { writeSuccessCostPGGateReport(t, report) })

	tracer := &successCostPGTracer{}
	db := successCostPGBuildDB(t, rawURL, tracer, true)
	defer func() { _ = db.Close() }()
	successCostPGApplyDDL(t, db)
	successCostPGCheckPrimaryKeys(t, db)
	if err := db.QueryRow("SHOW server_version").Scan(&report.PostgresVersion); err != nil {
		t.Fatalf("读取 PostgreSQL server_version 失败: %v", err)
	}

	t.Run("fresh empty database reaches done with zero batches", func(t *testing.T) {
		successCostPGClearFixture(t, db, true)
		for _, target := range successCostBackfillTargets {
			stat := successCostPGRunTarget(t, db, target)
			if stat.Status != "done" || stat.Batches != 0 || stat.MatchedRows != 0 {
				t.Fatalf("%s stat=%+v, want done/0/0", target.name, stat)
			}
		}
		markers := successCostPGMarkers(t, db)
		for _, target := range successCostBackfillTargets {
			if markers[target.name] != [2]string{"done", "{}"} {
				t.Fatalf("%s marker=%v, want done/{}", target.name, markers[target.name])
			}
		}
	})

	t.Run("done second call only reads markers", func(t *testing.T) {
		tracer.reset()
		for _, target := range successCostBackfillTargets {
			stat := successCostPGRunTarget(t, db, target)
			if stat.Batches != 0 || stat.MatchedRows != 0 {
				t.Fatalf("%s second-call stat=%+v, want zero work", target.name, stat)
			}
		}
		selects := 0
		for _, event := range tracer.snapshot() {
			sqlText := strings.ToLower(strings.TrimSpace(event.SQL))
			if strings.HasPrefix(sqlText, "select status, cursor_json from juhe_stats.backfill_progress") {
				selects++
			}
			for _, target := range successCostBackfillTargets {
				if strings.Contains(sqlText, "juhe_stats."+target.name) {
					t.Fatalf("done second call 触碰事实表 %s: %s", target.name, event.SQL)
				}
			}
		}
		if selects != len(successCostBackfillTargets) {
			t.Fatalf("done second call marker SELECT 数=%d, want %d", selects, len(successCostBackfillTargets))
		}
	})

	t.Run("large mixed fixture updates exact rows and records bounded batches", func(t *testing.T) {
		successCostPGClearFixture(t, db, true)
		tracer.reset()
		for _, target := range successCostBackfillTargets {
			rows := 6001
			if target.name == "usage_stats_minute" {
				rows = 240000
			}
			successCostPGInsertRows(t, db, target, target.name, rows)
			report.RowCounts[target.name] = rows
		}
		for _, target := range successCostBackfillTargets {
			stat := successCostPGRunTarget(t, db, target)
			wantBatches, wantMatched := 2, 5999
			if target.name == "usage_stats_minute" {
				wantBatches, wantMatched = 48, 239998
			}
			if stat.Batches != wantBatches || stat.MatchedRows != wantMatched {
				t.Fatalf("%s stat=%+v, want batches=%d matched=%d", target.name, stat, wantBatches, wantMatched)
			}
			timings := successCostPGBatchTimings(tracer, target)
			if len(timings) != wantBatches {
				t.Fatalf("%s traced batch timings=%d, want %d", target.name, len(timings), wantBatches)
			}
			result := successCostPGTableRun{Rows: successCostPGCountRows(t, db, target), MatchedRows: stat.MatchedRows, Batches: stat.Batches}
			var overOneSecond time.Duration
			for _, timing := range timings {
				ms := timing.Milliseconds()
				result.BatchTimings = append(result.BatchTimings, ms)
				if ms > result.MaxBatchMS {
					result.MaxBatchMS = ms
				}
				if timing >= time.Second {
					if timing > overOneSecond {
						overOneSecond = timing
					}
				}
			}
			report.Results[target.name] = result
			if overOneSecond >= time.Second {
				t.Fatalf("%s 单批 BEGIN→COMMIT=%s，要求严格小于 1s", target.name, overOneSecond)
			}
		}
		var errorCount int
		var totalCost, successCost float64
		if err := db.QueryRow(`SELECT error_count, total_cost_usd, success_cost_usd FROM juhe_stats.usage_stats_totals WHERE system_account_id = 'pg_gate_usage_stats_totals_000001'`).Scan(&errorCount, &totalCost, &successCost); err != nil {
			t.Fatalf("读取混合行失败: %v", err)
		}
		if errorCount != 1 || totalCost != 12 || successCost != 0 {
			t.Fatalf("混合行被错误改写: error=%d total=%v success=%v", errorCount, totalCost, successCost)
		}
		for _, check := range []struct {
			id   string
			want float64
		}{{"pg_gate_usage_stats_totals_000002", 10}, {"pg_gate_usage_stats_totals_000003", 10}} {
			var got float64
			if err := db.QueryRow("SELECT success_cost_usd FROM juhe_stats.usage_stats_totals WHERE system_account_id = $1", check.id).Scan(&got); err != nil {
				t.Fatalf("读取成功/已等值 fixture %s 失败: %v", check.id, err)
			}
			if got != check.want {
				t.Fatalf("fixture %s success_cost_usd=%v, want %v", check.id, got, check.want)
			}
		}
	})

	t.Run("persisted cursor resumes only pending suffix", func(t *testing.T) {
		successCostPGClearFixture(t, db, true)
		target := successCostBackfillTargets[0]
		successCostPGInsertRows(t, db, target, "resume_usage_stats_totals", 6001)
		if _, err := db.Exec(`UPDATE juhe_stats.usage_stats_totals SET success_cost_usd = total_cost_usd WHERE system_account_id LIKE 'pg_gate_resume_%' AND system_account_id <= 'pg_gate_resume_usage_stats_totals_005000'`); err != nil {
			t.Fatalf("准备 cursor 前已处理行失败: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO juhe_stats.backfill_progress (table_name, status, cursor_json, updated_at) VALUES ('usage_stats_totals', 'running', '["pg_gate_resume_usage_stats_totals_005000","system","pg_gate_resume_usage_stats_totals_005000"]', '2026-01-01T00:00:00.000Z')`); err != nil {
			t.Fatalf("准备 running cursor marker 失败: %v", err)
		}
		stat := successCostPGRunTarget(t, db, target)
		if stat.Batches != 1 || stat.MatchedRows != 1001 || stat.Status != "done" {
			t.Fatalf("续跑 stat=%+v, want done/1/1001", stat)
		}
	})

	t.Run("dry run counts rows without changing markers", func(t *testing.T) {
		successCostPGClearFixture(t, db, true)
		for _, target := range successCostBackfillTargets {
			successCostPGInsertRows(t, db, target, "dryrun", 3)
		}
		db.Close()
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "postgres", "-dsn", rawURL)
		if code != 0 {
			t.Fatalf("dry-run exit=%d, stderr=%s", code, successCostPGRedactedText(rawURL, stderr))
		}
		var cliReport successCostBackfillReport
		if err := json.Unmarshal([]byte(stdout), &cliReport); err != nil {
			t.Fatalf("dry-run stdout 不是 JSON report: %v", err)
		}
		if !cliReport.DryRun || cliReport.Completed {
			t.Fatalf("dry-run report=%+v, want dryRun=true completed=false", cliReport)
		}
		for _, target := range successCostBackfillTargets {
			stat := cliReport.Tables[target.name]
			if stat.Status != "dry-run" || stat.MatchedRows != 1 || stat.Batches != 0 {
				t.Fatalf("%s dry-run stat=%+v, want dry-run/1/0", target.name, stat)
			}
		}
		tracer.reset()
		db = successCostPGBuildDB(t, rawURL, tracer, false)
		markers := successCostPGMarkers(t, db)
		if len(markers) != 0 {
			t.Fatalf("dry-run 改变 marker 事实: %v", markers)
		}
		for _, target := range successCostBackfillTargets {
			var success float64
			if err := db.QueryRow("SELECT success_cost_usd FROM juhe_stats." + target.name + " WHERE system_account_id = 'pg_gate_dryrun_000003'").Scan(&success); err != nil {
				t.Fatalf("读取 %s dry-run 行失败: %v", target.name, err)
			}
			if success != 0 {
				t.Fatalf("%s dry-run 改写 success_cost_usd=%v", target.name, success)
			}
		}
	})

}
