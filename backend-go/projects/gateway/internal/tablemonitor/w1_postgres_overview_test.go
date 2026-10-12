package tablemonitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	tableMonitorOverviewPostgresURLVariable    = "JUHE_AI_TABLE_MONITOR_TEST_POSTGRES_URL"
	tableMonitorOverviewPostgresReportVariable = "JUHE_AI_TABLE_MONITOR_TEST_REPORT"
	tableMonitorOverviewPostgresRows           = 300000
	tableMonitorOverviewPostgresDBRows         = 3750
	tableMonitorOverviewPostgresKeyCount       = 400
	tableMonitorOverviewPostgresSamplesPerKey  = 750
)

var tableMonitorOverviewTestDatabasePattern = regexp.MustCompile(`^juhe_ai_sub2api_dev_[a-z][a-z0-9_]{2,39}$`)

type tableMonitorOverviewQuery struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args,omitempty"`
}

type tableMonitorOverviewTracer struct {
	mu      sync.Mutex
	enabled bool
	queries []tableMonitorOverviewQuery
}

func (tracer *tableMonitorOverviewTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.mu.Lock()
	if tracer.enabled {
		args := append([]any(nil), data.Args...)
		tracer.queries = append(tracer.queries, tableMonitorOverviewQuery{SQL: data.SQL, Args: args})
	}
	tracer.mu.Unlock()
	return ctx
}

func (tracer *tableMonitorOverviewTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func (tracer *tableMonitorOverviewTracer) beginCapture() {
	tracer.mu.Lock()
	tracer.queries = nil
	tracer.enabled = true
	tracer.mu.Unlock()
}

func (tracer *tableMonitorOverviewTracer) endCapture() []tableMonitorOverviewQuery {
	tracer.mu.Lock()
	tracer.enabled = false
	queries := append([]tableMonitorOverviewQuery(nil), tracer.queries...)
	tracer.mu.Unlock()
	return queries
}

type tableMonitorOverviewQueryPlan struct {
	Source      string          `json:"source"`
	SQL         string          `json:"sql"`
	Args        []any           `json:"args,omitempty"`
	Plan        json.RawMessage `json:"plan"`
	HeapFetches []int64         `json:"heap_fetches,omitempty"`
}

type tableMonitorOverviewBenchmark struct {
	Source     string  `json:"source"`
	Scenario   string  `json:"scenario"`
	Iterations int     `json:"iterations"`
	P95Millis  float64 `json:"p95_millis"`
}

type tableMonitorOverviewPostgresReport struct {
	DatabaseName          string                          `json:"database_name"`
	ServerVersion         string                          `json:"server_version"`
	InitialDatabaseRows   int64                           `json:"initial_database_rows"`
	InitialTableRows      int64                           `json:"initial_table_rows"`
	InitialTableKeys      int64                           `json:"initial_table_keys"`
	PerformanceDBRows     int64                           `json:"performance_database_rows"`
	PerformanceTableRows  int64                           `json:"performance_table_rows"`
	PerformanceTableKeys  int64                           `json:"performance_table_keys"`
	DatabaseRelationBytes int64                           `json:"database_relation_bytes"`
	TableRelationBytes    int64                           `json:"table_relation_bytes"`
	Plans                 []tableMonitorOverviewQueryPlan `json:"plans"`
	Benchmarks            []tableMonitorOverviewBenchmark `json:"benchmarks"`
}

type tableMonitorOverviewRequest struct {
	name     string
	page     int
	pageSize int
	keyword  string
}

type tableMonitorOverviewFunction func(context.Context, int, int, int, string, []any) (Overview, error)

// TestTableMonitorPostgresOverview compares the current SQL with the old query
// oracle and records PostgreSQL plans and timings in a manually enabled test.
// It intentionally leaves the created schema in the dedicated database; the
// caller owns disposal of that database and this test never drops a schema.
func TestTableMonitorPostgresOverview(t *testing.T) {
	url := strings.TrimSpace(os.Getenv(tableMonitorOverviewPostgresURLVariable))
	if url == "" {
		t.Skipf("未设置 %s；跳过真实 PostgreSQL overview 回归", tableMonitorOverviewPostgresURLVariable)
	}
	reportPath := strings.TrimSpace(os.Getenv(tableMonitorOverviewPostgresReportVariable))
	if reportPath == "" {
		t.Fatalf("设置 %s 时必须同时设置 %s 以保存 SQL、计划和性能报告", tableMonitorOverviewPostgresURLVariable, tableMonitorOverviewPostgresReportVariable)
	}

	pgConfig, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("解析 PostgreSQL 测试连接配置失败: %s", redactTableMonitorOverviewPostgresError(err, url, nil))
	}
	if !tableMonitorOverviewTestDatabasePattern.MatchString(pgConfig.Database) {
		t.Fatalf("拒绝 PostgreSQL 测试目标数据库名 %q；仅允许 juhe_ai_sub2api_dev_<name>", pgConfig.Database)
	}
	if strings.Contains(reportPath, url) ||
		(pgConfig.Password != "" && strings.Contains(reportPath, pgConfig.Password)) ||
		(pgConfig.User != "" && strings.Contains(reportPath, pgConfig.User)) {
		t.Fatal("PostgreSQL 测试报告路径不得包含连接串或密码")
	}

	tracer := &tableMonitorOverviewTracer{}
	pgConfig.Tracer = tracer
	pgConfig.ConnectTimeout = 10 * time.Second
	db := stdlib.OpenDB(*pgConfig)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭 PostgreSQL 测试连接失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("连接 PostgreSQL 测试数据库失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	var actualDatabase string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&actualDatabase); err != nil {
		t.Fatalf("确认 PostgreSQL 实际连接目标失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if actualDatabase != pgConfig.Database || !tableMonitorOverviewTestDatabasePattern.MatchString(actualDatabase) {
		t.Fatalf("PostgreSQL 实际连接目标不符合隔离库约束: parsed=%q actual=%q", pgConfig.Database, actualDatabase)
	}
	var schemaExists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'juhe_stats')`).Scan(&schemaExists); err != nil {
		t.Fatalf("确认 juhe_stats schema 状态失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if schemaExists {
		t.Fatal("juhe_stats 已存在；此回归要求由调用方提供新建的隔离数据库，测试不会覆盖或删除现有 schema")
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_stats`); err != nil {
		t.Fatalf("创建 juhe_stats schema 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if err := createTableMonitorOverviewPostgresSchema(ctx, db); err != nil {
		t.Fatalf("创建 table-monitor PostgreSQL 测试表失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}

	store, err := NewStore(db, true)
	if err != nil {
		t.Fatalf("创建 PostgreSQL table-monitor Store 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	baseline := &Store{db: db, pg: true, now: store.now}
	currentOverview := store.loadOverviewPostgres
	oldOverview := baseline.loadOverviewPostgresBaseline
	report := tableMonitorOverviewPostgresReport{DatabaseName: actualDatabase}
	t.Cleanup(func() {
		if err := writeTableMonitorOverviewPostgresReport(reportPath, report, pgConfig); err != nil {
			t.Errorf("写入 PostgreSQL 测试报告失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
		}
	})

	var serverVersion string
	if err := db.QueryRowContext(ctx, `SHOW server_version`).Scan(&serverVersion); err != nil {
		t.Fatalf("读取 PostgreSQL 版本失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	report.ServerVersion = serverVersion

	// The empty-schema comparison catches behavior changes independent of fixture
	// ordering and leaves non-nil empty slices observable through DeepEqual.
	emptyRequest := tableMonitorOverviewRequest{name: "empty", page: 1, pageSize: 10}
	emptyCurrent, _ := compareTableMonitorOverview(t, ctx, currentOverview, oldOverview, emptyRequest, url, pgConfig)
	if emptyCurrent.Total != 0 || len(emptyCurrent.Databases) != 0 || len(emptyCurrent.Tables) != 0 || emptyCurrent.HasMore {
		t.Fatalf("空快照期望返回零总数、空列表和 hasMore=false，实际为 %#v", emptyCurrent)
	}
	if emptyCurrent.Page != 1 || emptyCurrent.PageSize != 10 || emptyCurrent.SampledAt != nil {
		t.Fatalf("空快照分页和 sampledAt 边界不正确: %#v", emptyCurrent)
	}

	if err := seedTableMonitorOverviewSmallFixture(ctx, db, url, pgConfig); err != nil {
		t.Fatalf("写入 PostgreSQL overview 小型 fixture 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	requests := []tableMonitorOverviewRequest{
		{name: "small_first_page", page: 1, pageSize: 4},
		{name: "small_last_page", page: 4, pageSize: 4},
		{name: "small_out_of_range", page: 9, pageSize: 4},
		{name: "small_shared_prefix", page: 1, pageSize: 10, keyword: "shared"},
		{name: "small_case_insensitive", page: 1, pageSize: 10, keyword: "case"},
		{name: "small_literal_percent", page: 1, pageSize: 10, keyword: "literal%"},
		{name: "small_literal_underscore", page: 1, pageSize: 10, keyword: "literal_"},
		{name: "small_literal_backslash", page: 1, pageSize: 10, keyword: `literal\`},
	}
	results := make(map[string]Overview, len(requests))
	for _, request := range requests {
		current, _ := compareTableMonitorOverview(t, ctx, currentOverview, oldOverview, request, url, pgConfig)
		results[request.name] = current
	}
	assertTableMonitorOverviewSmallFixture(t, results)

	if err := seedTableMonitorOverviewPerformanceFixture(ctx, db, url, pgConfig); err != nil {
		t.Fatalf("生成 PostgreSQL overview 性能 fixture 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	seedCtx, seedCancel := context.WithTimeout(ctx, 120*time.Second)
	if _, err := db.ExecContext(seedCtx, `VACUUM (ANALYZE) juhe_stats.database_storage_snapshots`); err != nil {
		seedCancel()
		t.Fatalf("VACUUM ANALYZE database snapshots 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if _, err := db.ExecContext(seedCtx, `VACUUM (ANALYZE) juhe_stats.table_storage_snapshots`); err != nil {
		seedCancel()
		t.Fatalf("VACUUM ANALYZE table snapshots 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	seedCancel()
	if err := readTableMonitorOverviewFixtureStats(ctx, db, &report, url, pgConfig); err != nil {
		t.Fatalf("读取 PostgreSQL 性能 fixture 统计信息失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if report.PerformanceTableRows != tableMonitorOverviewPostgresRows ||
		report.PerformanceTableKeys != tableMonitorOverviewPostgresKeyCount ||
		report.PerformanceDBRows != tableMonitorOverviewPostgresDBRows {
		t.Fatalf("性能 fixture 行数不符合 400 keys × 750 samples 和 5 roles × 750 samples: tableRows=%d tableKeys=%d databaseRows=%d", report.PerformanceTableRows, report.PerformanceTableKeys, report.PerformanceDBRows)
	}

	performanceRequests := []tableMonitorOverviewRequest{
		{name: "perf_first_page", page: 1, pageSize: 50},
		{name: "perf_keyword", page: 1, pageSize: 50, keyword: "perf_table_03"},
	}
	for _, request := range performanceRequests {
		current, plans := captureAndExplainTableMonitorOverview(t, ctx, db, tracer, currentOverview, "current_before_update", request, url, pgConfig)
		report.Plans = append(report.Plans, plans...)
		old, plans := captureAndExplainTableMonitorOverview(t, ctx, db, tracer, oldOverview, "baseline_before_update", request, url, pgConfig)
		report.Plans = append(report.Plans, plans...)
		if !reflect.DeepEqual(current, old) {
			t.Fatalf("性能 fixture 上新旧查询结果不一致，scenario=%s: current=%#v baseline=%#v", request.name, current, old)
		}
	}

	for _, benchmark := range []struct {
		name    string
		fn      tableMonitorOverviewFunction
		from    string
		request tableMonitorOverviewRequest
	}{
		{name: "new_first_page_before_update", fn: currentOverview, from: "current", request: performanceRequests[0]},
		{name: "new_keyword_before_update", fn: currentOverview, from: "current", request: performanceRequests[1]},
		{name: "baseline_first_page_before_update", fn: oldOverview, from: "baseline", request: performanceRequests[0]},
	} {
		p95, err := measureTableMonitorOverviewP95(ctx, benchmark.fn, benchmark.request, 20)
		if err != nil {
			t.Fatalf("測量 PostgreSQL overview p95 失败，scenario=%s: %s", benchmark.name, redactTableMonitorOverviewPostgresError(err, url, pgConfig))
		}
		report.Benchmarks = append(report.Benchmarks, tableMonitorOverviewBenchmark{Source: benchmark.from, Scenario: benchmark.name, Iterations: 20, P95Millis: float64(p95) / float64(time.Millisecond)})
		if benchmark.from == "current" && p95 >= time.Second {
			t.Fatalf("新 PostgreSQL overview SQL p95 必须小于 1 秒，scenario=%s p95=%s", benchmark.name, p95)
		}
	}

	if err := updateTableMonitorOverviewLatestRows(ctx, db, url, pgConfig); err != nil {
		t.Fatalf("更新最新性能样本以观察非 all-visible 页失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	if _, err := db.ExecContext(ctx, `ANALYZE juhe_stats.table_storage_snapshots`); err != nil {
		t.Fatalf("更新性能样本后 ANALYZE 失败: %s", redactTableMonitorOverviewPostgresError(err, url, pgConfig))
	}
	updatedRequest := performanceRequests[0]
	updatedCurrent, plans := captureAndExplainTableMonitorOverview(t, ctx, db, tracer, currentOverview, "current_after_update", updatedRequest, url, pgConfig)
	report.Plans = append(report.Plans, plans...)
	updatedOld, plans := captureAndExplainTableMonitorOverview(t, ctx, db, tracer, oldOverview, "baseline_after_update", updatedRequest, url, pgConfig)
	report.Plans = append(report.Plans, plans...)
	if !reflect.DeepEqual(updatedCurrent, updatedOld) {
		t.Fatalf("更新最新行并 ANALYZE 后新旧查询结果不一致: current=%#v baseline=%#v", updatedCurrent, updatedOld)
	}
	for _, benchmark := range []struct {
		name string
		fn   tableMonitorOverviewFunction
		from string
	}{
		{name: "new_first_page_after_update", fn: currentOverview, from: "current"},
		{name: "baseline_first_page_after_update", fn: oldOverview, from: "baseline"},
	} {
		p95, err := measureTableMonitorOverviewP95(ctx, benchmark.fn, updatedRequest, 20)
		if err != nil {
			t.Fatalf("更新后測量 PostgreSQL overview p95 失败，source=%s: %s", benchmark.from, redactTableMonitorOverviewPostgresError(err, url, pgConfig))
		}
		report.Benchmarks = append(report.Benchmarks, tableMonitorOverviewBenchmark{Source: benchmark.from, Scenario: benchmark.name, Iterations: 20, P95Millis: float64(p95) / float64(time.Millisecond)})
		if benchmark.from == "current" && p95 >= time.Second {
			t.Fatalf("更新后新 PostgreSQL overview SQL p95 必须小于 1 秒，p95=%s", p95)
		}
	}
}

// loadOverviewPostgresBaseline is the pre-optimization PostgreSQL overview
// query from HEAD. Keep it independent of the current CTE shape so this
// integration test remains a semantic and timing oracle during SQL changes.
func (s *Store) loadOverviewPostgresBaseline(ctx context.Context, page, pageSize, offset int, keywordClause string, keywordParams []any) (Overview, error) {
	databaseRows, err := s.query(ctx, `
		SELECT DISTINCT ON (database_role)
			database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, free_bytes, table_count
		FROM `+s.table("database_storage_snapshots")+`
		ORDER BY database_role, sampled_at DESC, id DESC
	`)
	if err != nil {
		return Overview{}, err
	}
	databases := make([]DatabaseSnapshot, 0, len(databaseRows))
	for _, row := range databaseRows {
		databases = append(databases, databaseSnapshotFromRow(row))
	}
	sortDatabaseSnapshots(databases)
	countRows, err := s.query(ctx, `
		SELECT COUNT(*) AS total
		FROM (
			SELECT database_role, table_name
			FROM `+s.table("table_storage_snapshots")+`
			WHERE 1 = 1`+keywordClause+`
			GROUP BY database_role, table_name
		) AS table_keys
	`, keywordParams...)
	if err != nil {
		return Overview{}, err
	}
	total := 0
	if len(countRows) > 0 {
		if value := countRows[0].number("total"); value != nil {
			total = int(*value)
		}
	}
	tableRows, err := s.query(ctx, `
		WITH latest_snapshots AS (
			SELECT DISTINCT ON (database_role, table_name)
				id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
				row_count, table_bytes, index_bytes, total_bytes, growth_bytes_1h, growth_rows_1h,
				growth_bytes_24h, growth_rows_24h
			FROM `+s.table("table_storage_snapshots")+`
			WHERE 1 = 1`+keywordClause+`
			ORDER BY database_role, table_name, sampled_at DESC, id DESC
		)
		SELECT database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
			row_count, table_bytes, index_bytes, total_bytes, growth_bytes_1h, growth_rows_1h,
			growth_bytes_24h, growth_rows_24h
		FROM latest_snapshots
		ORDER BY total_bytes DESC NULLS LAST, row_count DESC NULLS LAST, table_name ASC, database_role ASC
		LIMIT ? OFFSET ?
	`, append(append([]any{}, keywordParams...), pageSize, offset)...)
	if err != nil {
		return Overview{}, err
	}
	tables := make([]TableSnapshot, 0, len(tableRows))
	for _, row := range tableRows {
		tables = append(tables, tableSnapshotFromRow(row))
	}
	return Overview{
		SampledAt: latestSampledAt(databases),
		Databases: databases,
		Tables:    tables,
		Page:      page,
		PageSize:  pageSize,
		Total:     total,
		HasMore:   offset+len(tables) < total,
	}, nil
}

func compareTableMonitorOverview(t *testing.T, ctx context.Context, currentFn, baselineFn tableMonitorOverviewFunction, request tableMonitorOverviewRequest, url string, config *pgx.ConnConfig) (Overview, Overview) {
	t.Helper()
	keywordClause, keywordParams := tableMonitorOverviewKeyword(request.keyword)
	current, err := currentFn(ctx, request.page, request.pageSize, (request.page-1)*request.pageSize, keywordClause, keywordParams)
	if err != nil {
		t.Fatalf("執行新 PostgreSQL overview 失败，scenario=%s: %s", request.name, redactTableMonitorOverviewPostgresError(err, url, config))
	}
	baseline, err := baselineFn(ctx, request.page, request.pageSize, (request.page-1)*request.pageSize, keywordClause, keywordParams)
	if err != nil {
		t.Fatalf("執行基线 PostgreSQL overview 失败，scenario=%s: %s", request.name, redactTableMonitorOverviewPostgresError(err, url, config))
	}
	if !reflect.DeepEqual(current, baseline) {
		t.Fatalf("新旧 PostgreSQL overview 不一致，scenario=%s: current=%#v baseline=%#v", request.name, current, baseline)
	}
	return current, baseline
}

func tableMonitorOverviewKeyword(keyword string) (string, []any) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "", nil
	}
	return " AND lower(table_name) LIKE lower(?) ESCAPE '\\'", []any{escapeLikePrefix(keyword) + "%"}
}

func createTableMonitorOverviewPostgresSchema(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE juhe_stats.database_storage_snapshots (
			id TEXT PRIMARY KEY, database_role TEXT NOT NULL, database_path TEXT NOT NULL, sampled_at TIMESTAMPTZ NOT NULL,
			file_bytes BIGINT, wal_bytes BIGINT, shm_bytes BIGINT, page_size INTEGER, page_count INTEGER,
			freelist_count INTEGER, used_bytes BIGINT, free_bytes BIGINT, table_count INTEGER, index_count INTEGER,
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE juhe_stats.table_storage_snapshots (
			id TEXT PRIMARY KEY, database_role TEXT NOT NULL, table_name TEXT NOT NULL, sampled_at TIMESTAMPTZ NOT NULL,
			table_kind TEXT NOT NULL DEFAULT 'table', parent_table_name TEXT, is_partition BOOLEAN NOT NULL DEFAULT FALSE,
			is_archive BOOLEAN NOT NULL DEFAULT FALSE, row_count BIGINT, table_bytes BIGINT, index_bytes BIGINT, total_bytes BIGINT,
			page_count BIGINT, index_count INTEGER NOT NULL DEFAULT 0, growth_bytes_1h BIGINT, growth_rows_1h BIGINT,
			growth_bytes_24h BIGINT, growth_rows_24h BIGINT, created_at TIMESTAMPTZ NOT NULL,
			UNIQUE(database_role, table_name, sampled_at)
		)`,
		`CREATE INDEX idx_database_storage_snapshots_role_time_id ON juhe_stats.database_storage_snapshots(database_role, sampled_at DESC, id DESC)`,
		`CREATE INDEX idx_table_storage_snapshots_latest_id ON juhe_stats.table_storage_snapshots(database_role, table_name, sampled_at DESC, id DESC)`,
		`CREATE INDEX idx_table_storage_snapshots_time ON juhe_stats.table_storage_snapshots(sampled_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

type tableMonitorOverviewFixture struct {
	id         string
	role       string
	name       string
	sampledAt  time.Time
	kind       *string
	parent     *string
	partition  bool
	archive    bool
	rowCount   *int64
	tableBytes *int64
	indexBytes *int64
	totalBytes *int64
}

func seedTableMonitorOverviewSmallFixture(ctx context.Context, db *sql.DB, url string, config *pgx.ConnConfig) error {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, snapshot := range []struct {
		id, role, path string
		sampledAt      time.Time
		fileBytes      int64
	}{
		{id: "db-business-a", role: "business", path: "/srv/business-old.db", sampledAt: base, fileBytes: 100},
		{id: "db-business-z", role: "business", path: "/srv/business-latest.db", sampledAt: base, fileBytes: 999},
		{id: "db-alpha", role: "alpha", path: "/srv/alpha.db", sampledAt: base.Add(time.Hour), fileBytes: 200},
		{id: "db-zeta", role: "zeta", path: "/srv/zeta.db", sampledAt: base.Add(2 * time.Hour), fileBytes: 300},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO juhe_stats.database_storage_snapshots
			(id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, free_bytes, table_count, created_at)
			VALUES ($1, $2, $3, $4, $5, 10, 11, 12, 13, $4)`, snapshot.id, snapshot.role, snapshot.path, snapshot.sampledAt, snapshot.fileBytes)
		if err != nil {
			return fmt.Errorf("insert database snapshot: %w", err)
		}
	}
	makeFixture := func(id, role, name string, sampled time.Time, total, rows, table, index *int64) tableMonitorOverviewFixture {
		kind := "table"
		return tableMonitorOverviewFixture{id: id, role: role, name: name, sampledAt: sampled, kind: &kind, rowCount: rows, tableBytes: table, indexBytes: index, totalBytes: total}
	}
	fixtures := []tableMonitorOverviewFixture{
		makeFixture("table-shared-business-old", "business", "shared_table", base, overviewInt64(80), overviewInt64(8), overviewInt64(64), overviewInt64(16)),
		makeFixture("table-shared-business-new", "business", "shared_table", base.Add(time.Minute), overviewInt64(500), overviewInt64(50), overviewInt64(400), overviewInt64(100)),
		makeFixture("table-shared-alpha", "alpha", "shared_table", base, overviewInt64(400), overviewInt64(50), overviewInt64(320), overviewInt64(80)),
		makeFixture("table-shared-zeta", "zeta", "shared_table", base, overviewInt64(400), overviewInt64(50), overviewInt64(320), overviewInt64(80)),
		makeFixture("table-case-upper", "business", "CaseTable", base, overviewInt64(250), overviewInt64(25), overviewInt64(200), overviewInt64(50)),
		makeFixture("table-case-lower", "business", "casetable", base, overviewInt64(250), overviewInt64(25), overviewInt64(200), overviewInt64(50)),
		makeFixture("table-literal-percent", "business", "literal%mark", base, overviewInt64(100), overviewInt64(10), overviewInt64(80), overviewInt64(20)),
		makeFixture("table-literal-underscore", "business", "literal_more", base, overviewInt64(100), overviewInt64(10), overviewInt64(80), overviewInt64(20)),
		makeFixture("table-literal-backslash", "business", `literal\path`, base, overviewInt64(100), overviewInt64(10), overviewInt64(80), overviewInt64(20)),
		makeFixture("table-literal-plain", "business", "literalxplain", base, overviewInt64(100), overviewInt64(10), overviewInt64(80), overviewInt64(20)),
		makeFixture("table-null-total-high-rows", "business", "null_total_high_rows", base, nil, overviewInt64(999), nil, nil),
		makeFixture("table-null-total-null-rows", "business", "null_total_null_rows", base, nil, nil, nil, nil),
		makeFixture("table-null-row-count", "business", "null_row_lower", base, overviewInt64(900), nil, overviewInt64(700), overviewInt64(200)),
		makeFixture("table-tie-a", "business", "tie_a", base, overviewInt64(1000), overviewInt64(50), overviewInt64(800), overviewInt64(200)),
		makeFixture("table-tie-b", "business", "tie_b", base, overviewInt64(1000), overviewInt64(50), overviewInt64(800), overviewInt64(200)),
	}
	for _, fixture := range fixtures {
		_, err := db.ExecContext(ctx, `INSERT INTO juhe_stats.table_storage_snapshots
			(id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
			 row_count, table_bytes, index_bytes, total_bytes, growth_bytes_1h, growth_rows_1h,
			 growth_bytes_24h, growth_rows_24h, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1, 2, 3, 4, $4)`,
			fixture.id, fixture.role, fixture.name, fixture.sampledAt, fixture.kind, fixture.parent, fixture.partition, fixture.archive,
			fixture.rowCount, fixture.tableBytes, fixture.indexBytes, fixture.totalBytes)
		if err != nil {
			return redactTableMonitorOverviewPostgresError(err, url, config)
		}
	}
	return nil
}

func assertTableMonitorOverviewSmallFixture(t *testing.T, results map[string]Overview) {
	t.Helper()
	first := results["small_first_page"]
	if first.Total != 14 || len(first.Tables) != 4 || !first.HasMore {
		t.Fatalf("小型 fixture 首页应有 14 个 role/table key、4 行和 hasMore=true，实际 total=%d rows=%d hasMore=%t", first.Total, len(first.Tables), first.HasMore)
	}
	if got := []string{first.Tables[0].TableName, first.Tables[1].TableName, first.Tables[2].TableName, first.Tables[3].DatabaseRole + "/" + first.Tables[3].TableName}; !reflect.DeepEqual(got, []string{"tie_a", "tie_b", "null_row_lower", "business/shared_table"}) {
		t.Fatalf("小型 fixture 必须按容量、行数 NULL 规则和名称排序，实际为 %#v", got)
	}
	if len(first.Databases) != 3 || first.Databases[0].DatabaseRole != "business" || first.Databases[1].DatabaseRole != "alpha" || first.Databases[2].DatabaseRole != "zeta" {
		t.Fatalf("数据库角色应按已知角色优先、未知角色按 SQL 稳定字典序排列，实际为 %#v", first.Databases)
	}
	if first.Databases[0].DatabasePath != "business-latest.db" || first.Databases[0].FileBytes == nil || *first.Databases[0].FileBytes != 999 {
		t.Fatalf("同 sampled_at 时数据库快照应选择 id 较大的 business-latest 行，实际为 %#v", first.Databases[0])
	}
	if first.SampledAt == nil || *first.SampledAt != "2026-10-01T02:00:00.000Z" {
		t.Fatalf("overview sampledAt 应为最新数据库快照时间，实际为 %#v", first.SampledAt)
	}
	last := results["small_last_page"]
	if last.Total != 14 || len(last.Tables) != 2 || last.HasMore {
		t.Fatalf("末页应只剩两行且 hasMore=false，实际 total=%d rows=%d hasMore=%t", last.Total, len(last.Tables), last.HasMore)
	}
	if last.Tables[0].TableName != "null_total_high_rows" || last.Tables[1].TableName != "null_total_null_rows" || last.Tables[0].RowCount == nil || last.Tables[1].RowCount != nil {
		t.Fatalf("total_bytes 为 NULL 时应排在末尾并按 row_count DESC NULLS LAST 排序，实际为 %#v", last.Tables)
	}
	outOfRange := results["small_out_of_range"]
	if outOfRange.Total != 14 || len(outOfRange.Tables) != 0 || outOfRange.HasMore || outOfRange.Page != 9 {
		t.Fatalf("越界页应保留总数和请求页码并返回空表，实际为 %#v", outOfRange)
	}
	shared := results["small_shared_prefix"]
	if shared.Total != 3 || len(shared.Tables) != 3 {
		t.Fatalf("跨角色同名表应计为三个独立 key，实际 total=%d rows=%d", shared.Total, len(shared.Tables))
	}
	if got := []string{shared.Tables[0].DatabaseRole, shared.Tables[1].DatabaseRole, shared.Tables[2].DatabaseRole}; !reflect.DeepEqual(got, []string{"business", "alpha", "zeta"}) {
		t.Fatalf("business 容量更大时应先于相同表名的未知角色，剩余相同值按 database_role 排序，实际为 %#v", got)
	}
	caseInsensitive := results["small_case_insensitive"]
	caseNames := make([]string, 0, len(caseInsensitive.Tables))
	for _, table := range caseInsensitive.Tables {
		caseNames = append(caseNames, table.TableName)
	}
	sort.Strings(caseNames)
	// 大小写名称的先后由 PostgreSQL collation 决定，顺序一致由新旧 SQL 对照验证。
	if caseInsensitive.Total != 2 || !reflect.DeepEqual(caseNames, []string{"CaseTable", "casetable"}) {
		t.Fatalf("大小写变体应分别计数且 keyword 匹配不区分大小写，实际为 %#v", caseInsensitive)
	}
	for _, scenario := range []struct{ name, table string }{
		{name: "small_literal_percent", table: "literal%mark"},
		{name: "small_literal_underscore", table: "literal_more"},
		{name: "small_literal_backslash", table: `literal\path`},
	} {
		result := results[scenario.name]
		if result.Total != 1 || len(result.Tables) != 1 || result.Tables[0].TableName != scenario.table {
			t.Fatalf("keyword 中的 LIKE 元字符必须按字面匹配，scenario=%s result=%#v", scenario.name, result)
		}
	}
}

func seedTableMonitorOverviewPerformanceFixture(ctx context.Context, db *sql.DB, url string, config *pgx.ConnConfig) error {
	seedCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	_, err := db.ExecContext(seedCtx, `
		INSERT INTO juhe_stats.database_storage_snapshots
			(id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes, free_bytes, table_count, created_at)
		SELECT 'perf-db-' || role_name || '-' || lpad(sample_no::text, 4, '0'),
			role_name, '/isolated/' || role_name || '.db',
			TIMESTAMPTZ '2026-10-02 00:00:00+00' + sample_no * INTERVAL '1 second',
			1000000 + sample_no * 100 + role_no, 100 + sample_no, 10, 200 + sample_no, 80 + role_no,
			TIMESTAMPTZ '2026-10-02 00:00:00+00' + sample_no * INTERVAL '1 second'
		FROM generate_series(1, 5) AS roles(role_no)
		CROSS JOIN LATERAL (VALUES
			(CASE role_no WHEN 1 THEN 'business' WHEN 2 THEN 'dataset' WHEN 3 THEN 'usage-catalog' WHEN 4 THEN 'stats' ELSE 'codex-context-state' END)
		) AS names(role_name)
		CROSS JOIN generate_series(1, 750) AS samples(sample_no)
	`)
	if err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	_, err = db.ExecContext(seedCtx, `
		WITH keys AS (
			SELECT key_no,
				CASE ((key_no - 1) % 5)
					WHEN 0 THEN 'business'
					WHEN 1 THEN 'dataset'
					WHEN 2 THEN 'usage-catalog'
					WHEN 3 THEN 'stats'
					ELSE 'codex-context-state'
				END AS role_name
			FROM generate_series(1, 400) AS key_rows(key_no)
		)
		INSERT INTO juhe_stats.table_storage_snapshots
			(id, database_role, table_name, sampled_at, table_kind, parent_table_name, is_partition, is_archive,
			 row_count, table_bytes, index_bytes, total_bytes, page_count, index_count,
			 growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at)
		SELECT 'perf-table-' || lpad(keys.key_no::text, 4, '0') || '-' || lpad(samples.sample_no::text, 4, '0'),
			keys.role_name, 'perf_table_' || lpad(keys.key_no::text, 4, '0'),
			TIMESTAMPTZ '2026-10-02 00:00:00+00' + samples.sample_no * INTERVAL '1 second',
			'table', NULL, (keys.key_no % 17 = 0), (keys.key_no % 29 = 0),
			1000 + keys.key_no * 100 + samples.sample_no,
			100000 + keys.key_no * 1000 + samples.sample_no * 4,
			10000 + keys.key_no * 100 + samples.sample_no,
			110000 + keys.key_no * 1100 + samples.sample_no * 5,
			100 + keys.key_no, 2 + (keys.key_no % 5),
			100 + samples.sample_no, 1 + samples.sample_no,
			200 + samples.sample_no, 2 + samples.sample_no,
			TIMESTAMPTZ '2026-10-02 00:00:00+00' + samples.sample_no * INTERVAL '1 second'
		FROM keys
		CROSS JOIN generate_series(1, 750) AS samples(sample_no)
	`)
	if err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	return nil
}

func readTableMonitorOverviewFixtureStats(ctx context.Context, db *sql.DB, report *tableMonitorOverviewPostgresReport, url string, config *pgx.ConnConfig) error {
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.database_storage_snapshots`).Scan(&report.InitialDatabaseRows); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.table_storage_snapshots`).Scan(&report.InitialTableRows); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT database_role, table_name FROM juhe_stats.table_storage_snapshots GROUP BY database_role, table_name) AS keys`).Scan(&report.InitialTableKeys); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.database_storage_snapshots WHERE left(id, 8) = 'perf-db-'`).Scan(&report.PerformanceDBRows); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_stats.table_storage_snapshots WHERE left(table_name, 11) = 'perf_table_'`).Scan(&report.PerformanceTableRows); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT database_role, table_name FROM juhe_stats.table_storage_snapshots WHERE left(table_name, 11) = 'perf_table_' GROUP BY database_role, table_name) AS keys`).Scan(&report.PerformanceTableKeys); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT pg_total_relation_size('juhe_stats.database_storage_snapshots'::regclass)`).Scan(&report.DatabaseRelationBytes); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	if err := db.QueryRowContext(ctx, `SELECT pg_total_relation_size('juhe_stats.table_storage_snapshots'::regclass)`).Scan(&report.TableRelationBytes); err != nil {
		return redactTableMonitorOverviewPostgresError(err, url, config)
	}
	return nil
}

func updateTableMonitorOverviewLatestRows(ctx context.Context, db *sql.DB, url string, config *pgx.ConnConfig) error {
	_, err := db.ExecContext(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (database_role, table_name) id
			FROM juhe_stats.table_storage_snapshots
			WHERE left(table_name, 11) = 'perf_table_'
			ORDER BY database_role, table_name, sampled_at DESC, id DESC
		)
		UPDATE juhe_stats.table_storage_snapshots AS target
		SET row_count = target.row_count
		FROM latest
		WHERE target.id = latest.id
	`)
	return redactTableMonitorOverviewPostgresError(err, url, config)
}

func captureAndExplainTableMonitorOverview(t *testing.T, ctx context.Context, db *sql.DB, tracer *tableMonitorOverviewTracer, fn tableMonitorOverviewFunction, source string, request tableMonitorOverviewRequest, url string, config *pgx.ConnConfig) (Overview, []tableMonitorOverviewQueryPlan) {
	t.Helper()
	keywordClause, keywordParams := tableMonitorOverviewKeyword(request.keyword)
	tracer.beginCapture()
	overview, callErr := fn(ctx, request.page, request.pageSize, (request.page-1)*request.pageSize, keywordClause, keywordParams)
	queries := tracer.endCapture()
	if callErr != nil {
		t.Fatalf("捕获 overview 查询失败，source=%s scenario=%s: %s", source, request.name, redactTableMonitorOverviewPostgresError(callErr, url, config))
	}
	if len(queries) != 3 {
		t.Fatalf("Store overview 必须实际执行三条 SQL，source=%s scenario=%s 实际=%d", source, request.name, len(queries))
	}
	plans := make([]tableMonitorOverviewQueryPlan, 0, len(queries))
	for index, query := range queries {
		var raw []byte
		statement := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + query.SQL
		if err := db.QueryRowContext(ctx, statement, query.Args...).Scan(&raw); err != nil {
			t.Fatalf("读取 overview EXPLAIN 计划失败，source=%s scenario=%s query=%d: %s", source, request.name, index+1, redactTableMonitorOverviewPostgresError(err, url, config))
		}
		plans = append(plans, tableMonitorOverviewQueryPlan{Source: source + "/" + request.name, SQL: query.SQL, Args: query.Args, Plan: json.RawMessage(raw), HeapFetches: tableMonitorOverviewHeapFetches(raw)})
	}
	return overview, plans
}

func tableMonitorOverviewHeapFetches(raw []byte) []int64 {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var fetches []int64
	var visit func(any)
	visit = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, nested := range typed {
				if key == "Heap Fetches" {
					if number, ok := nested.(float64); ok {
						fetches = append(fetches, int64(number))
					}
				}
				visit(nested)
			}
		case []any:
			for _, nested := range typed {
				visit(nested)
			}
		}
	}
	visit(value)
	return fetches
}

func measureTableMonitorOverviewP95(ctx context.Context, fn tableMonitorOverviewFunction, request tableMonitorOverviewRequest, iterations int) (time.Duration, error) {
	keywordClause, keywordParams := tableMonitorOverviewKeyword(request.keyword)
	durations := make([]time.Duration, 0, iterations)
	for index := 0; index < iterations; index++ {
		startedAt := time.Now()
		if _, err := fn(ctx, request.page, request.pageSize, (request.page-1)*request.pageSize, keywordClause, keywordParams); err != nil {
			return 0, err
		}
		durations = append(durations, time.Since(startedAt))
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	percentileIndex := (95*len(durations)+99)/100 - 1
	return durations[percentileIndex], nil
}

func writeTableMonitorOverviewPostgresReport(path string, report tableMonitorOverviewPostgresReport, config *pgx.ConnConfig) error {
	if config != nil && ((config.Password != "" && strings.Contains(path, config.Password)) || (config.User != "" && strings.Contains(path, config.User))) {
		return fmt.Errorf("报告路径包含 PostgreSQL 账号敏感内容")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve report path: %w", err)
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	if err := os.WriteFile(absolutePath, content, 0o600); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func redactTableMonitorOverviewPostgresError(err error, url string, config *pgx.ConnConfig) error {
	if err == nil {
		return nil
	}
	message := strings.ReplaceAll(err.Error(), url, "[redacted PostgreSQL URL]")
	if config != nil {
		if config.Host != "" {
			message = strings.ReplaceAll(message, config.Host, "[redacted host]")
		}
		if config.Password != "" {
			message = strings.ReplaceAll(message, config.Password, "[redacted password]")
		}
		if config.User != "" {
			message = strings.ReplaceAll(message, config.User, "[redacted user]")
		}
	}
	return fmt.Errorf("%s", message)
}

func overviewInt64(value int64) *int64 { return &value }
