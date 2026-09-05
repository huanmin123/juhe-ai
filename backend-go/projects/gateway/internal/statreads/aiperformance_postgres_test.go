package statreads

// P0 修复的渲染与语义测试：PG 关键词候选查询按 Node
// usage-stats-ai-performance.repository.ts:711-791 的完整 SQL 逐字符断言
// （捕获驱动 + 测试内独立 ?→$n 重写器，参照 jobs cleanuprepo 模式，避免
// 循环论证）；显式账户请求顺序、rank NULL→0、ai-health 参数 400 门、
// performance 角色导出与 worker:N 家族用真实内存库或纯函数互证。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- PG 捕获驱动 ----

type capturedQuery struct {
	query string
	args  []driver.Value
}

type scriptedQuery struct {
	match   string
	columns []string
	rows    [][]driver.Value
}

type pgCapture struct {
	mu       sync.Mutex
	queries  []capturedQuery
	scripted []scriptedQuery
}

func (r *pgCapture) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, capturedQuery{query: query, args: values})
}

func (r *pgCapture) all() []capturedQuery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedQuery{}, r.queries...)
}

func (r *pgCapture) script(match string, columns []string, rows [][]driver.Value) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripted = append(r.scripted, scriptedQuery{match: match, columns: columns, rows: rows})
}

func (r *pgCapture) popScripted(query string) (scriptedQuery, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, item := range r.scripted {
		if strings.Contains(query, item.match) {
			r.scripted = append(r.scripted[:index], r.scripted[index+1:]...)
			return item, true
		}
	}
	return scriptedQuery{}, false
}

type captureConnector struct{ rec *pgCapture }

func (c captureConnector) Connect(context.Context) (driver.Conn, error) {
	return &captureConn{rec: c.rec}, nil
}

func (c captureConnector) Driver() driver.Driver { return captureDriver{rec: c.rec} }

type captureDriver struct{ rec *pgCapture }

func (d captureDriver) Open(string) (driver.Conn, error) { return &captureConn{rec: d.rec}, nil }

type captureConn struct{ rec *pgCapture }

func (c *captureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("capture: Prepare 不应被调用（走 QueryerContext）")
}

func (c *captureConn) Close() error { return nil }

func (c *captureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("capture: Begin 不应被调用")
}

func (c *captureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	if scripted, ok := c.rec.popScripted(query); ok {
		return &captureRows{columns: scripted.columns, values: scripted.rows}, nil
	}
	return &captureRows{columns: []string{"value"}}, nil
}

func (c *captureConn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case nil, int64, float64, bool, []byte, string:
		return nil
	case int:
		value.Value = int64(value.Value.(int))
		return nil
	}
	return fmt.Errorf("capture: 不支持的参数类型 %T", value.Value)
}

type captureRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
}

func (r *captureRows) Columns() []string { return r.columns }

func (r *captureRows) Close() error { return nil }

func (r *captureRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

// bindTestPG 是测试内独立的 ?→$n 重写器（不复用生产渲染逻辑，避免循环论证）。
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

func urlValuesFrom(keyword string) url.Values {
	values := url.Values{}
	values.Set("keyword", keyword)
	return values
}

func newPGCaptureDeps(rec *pgCapture) *Deps {
	return &Deps{
		Business:  sql.OpenDB(captureConnector{rec: rec}),
		PGDialect: true,
		Now: func() time.Time {
			return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
		},
	}
}

func pgCaptureRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/ai-performance/accounts", nil)
}

// ---- 渲染断言：全局 scope（Node candidateSql 分支 0 + 实例分支 priority 1） ----

func TestAiPerformanceKeywordPostgresGlobalRendersNodeSQL(t *testing.T) {
	rec := &pgCapture{}
	deps := newPGCaptureDeps(rec)
	rec.script("ROW_NUMBER() OVER", []string{"id"}, [][]driver.Value{
		{"acct-2"}, {"acct-1"},
	})
	ids, err := deps.aiPerformanceKeywordAccountIdsPostgres(
		pgCaptureRequest(),
		perfScopeState{SystemAccountID: globalStatsSystemAccountID, ScopeType: "account", IncludeSystemAccountName: true},
		"p er%f_", 50,
	)
	if err != nil {
		t.Fatalf("global keyword query: %v", err)
	}
	if len(ids) != 2 || ids[0] != "acct-2" || ids[1] != "acct-1" {
		t.Fatalf("ids = %#v, 期望 [acct-2 acct-1]", ids)
	}

	statements := rec.all()
	if len(statements) != 1 {
		t.Fatalf("PG 关键词搜索只应执行一条候选查询，实际 %d 条", len(statements))
	}
	statement := statements[0]
	wantSQL := bindTestPG(`
		SELECT id
		FROM (
			SELECT
				id,
				sort_name,
				source_priority,
				ROW_NUMBER() OVER (
					PARTITION BY id
					ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
				) AS duplicate_rank
			FROM (
			SELECT accounts.id, accounts.name AS sort_name, 0 AS source_priority
			FROM juhe_business.accounts accounts
			WHERE accounts.deleted_at IS NULL
				AND accounts.name COLLATE "C" LIKE '%' || $1 || '%' ESCAPE '\'
		
UNION ALL

			SELECT instance_accounts.id, source_accounts.name AS sort_name, 1 AS source_priority
			FROM juhe_business.accounts source_accounts
			INNER JOIN juhe_business.accounts instance_accounts
				ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
			WHERE source_accounts.deleted_at IS NULL
				AND instance_accounts.deleted_at IS NULL
				AND source_accounts.name COLLATE "C" LIKE '%' || $2 || '%' ESCAPE '\'
		) candidate_rows
		) ranked_candidates
		WHERE duplicate_rank = 1
		ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
		LIMIT $3
	`)
	if got := bindTestPG(statement.query); got != wantSQL {
		t.Fatalf("全局候选 SQL 不匹配：\ngot:\n%q\nwant:\n%q", got, wantSQL)
	}
	// 参数：LIKE 模式（%/_/\ 转义 + 原样空白）出现两次，随后 LIMIT。
	wantArgs := []string{"p er\\%f\\_", "p er\\%f\\_", "50"}
	if len(statement.args) != len(wantArgs) {
		t.Fatalf("参数数 = %d, 期望 %d", len(statement.args), len(wantArgs))
	}
	for i, want := range wantArgs {
		if got := fmt.Sprintf("%v", statement.args[i]); got != want {
			t.Fatalf("参数[%d] = %q, 期望 %q", i, got, want)
		}
	}
}

// ---- 渲染断言：租户 scope（source_priority 0/1/2/3 四分支 + 分组授权 EXISTS） ----

func TestAiPerformanceKeywordPostgresScopedRendersNodeSQL(t *testing.T) {
	rec := &pgCapture{}
	deps := newPGCaptureDeps(rec)
	rec.script("ROW_NUMBER() OVER", []string{"id"}, [][]driver.Value{
		{"acct-own"}, {"acct-auth"},
	})
	ids, err := deps.aiPerformanceKeywordAccountIdsPostgres(
		pgCaptureRequest(),
		perfScopeState{SystemAccountID: "sys-caller-1", ScopeType: "caller_account", IncludeSystemAccountName: false},
		"needle", 20,
	)
	if err != nil {
		t.Fatalf("scoped keyword query: %v", err)
	}
	if len(ids) != 2 || ids[0] != "acct-own" || ids[1] != "acct-auth" {
		t.Fatalf("ids = %#v", ids)
	}

	statements := rec.all()
	if len(statements) != 1 {
		t.Fatalf("PG 关键词搜索只应执行一条候选查询，实际 %d 条", len(statements))
	}
	statement := statements[0]
	wantSQL := bindTestPG(`
		SELECT id
		FROM (
			SELECT
				id,
				sort_name,
				source_priority,
				ROW_NUMBER() OVER (
					PARTITION BY id
					ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
				) AS duplicate_rank
			FROM (
			SELECT accounts.id, accounts.name AS sort_name, 0 AS source_priority
			FROM juhe_business.accounts accounts
			WHERE accounts.system_account_id = $1
				AND accounts.deleted_at IS NULL
				AND accounts.authorization_instance_authorization_id IS NULL
				AND accounts.name COLLATE "C" LIKE '%' || $2 || '%' ESCAPE '\'
		
UNION ALL

			SELECT accounts.id, accounts.name AS sort_name, 1 AS source_priority
			FROM juhe_business.accounts accounts
			WHERE accounts.system_account_id = $3
				AND accounts.deleted_at IS NULL
				AND accounts.authorization_instance_authorization_id IS NOT NULL
				AND accounts.name COLLATE "C" LIKE '%' || $4 || '%' ESCAPE '\'
		
UNION ALL

			SELECT accounts.id, accounts.name AS sort_name, 2 AS source_priority
			FROM juhe_business.accounts accounts
			WHERE accounts.deleted_at IS NULL
				AND accounts.name COLLATE "C" LIKE '%' || $5 || '%' ESCAPE '\'
				AND EXISTS (
					SELECT 1
					FROM juhe_business.group_accounts visible_group_accounts
					INNER JOIN juhe_business.resource_authorizations visible_group_authorization_rows
						ON visible_group_authorization_rows.resource_type = 'group'
						AND visible_group_authorization_rows.resource_id = visible_group_accounts.group_id
						AND visible_group_authorization_rows.grantee_system_account_id = $6
						AND visible_group_authorization_rows.status = 'active'
						AND (visible_group_authorization_rows.expires_at IS NULL OR visible_group_authorization_rows.expires_at > $7)
					WHERE visible_group_accounts.account_id = accounts.id
						AND visible_group_accounts.enabled = 1
				)
		
UNION ALL

			SELECT instance_accounts.id, source_accounts.name AS sort_name, 3 AS source_priority
			FROM juhe_business.accounts source_accounts
			INNER JOIN juhe_business.accounts instance_accounts
				ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
			WHERE source_accounts.deleted_at IS NULL
				AND instance_accounts.deleted_at IS NULL
				AND source_accounts.name COLLATE "C" LIKE '%' || $8 || '%' ESCAPE '\'
				AND instance_accounts.system_account_id = $9
		) candidate_rows
		) ranked_candidates
		WHERE duplicate_rank = 1
		ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
		LIMIT $10
	`)
	if got := bindTestPG(statement.query); got != wantSQL {
		t.Fatalf("租户候选 SQL 不匹配：\ngot:\n%q\nwant:\n%q", got, wantSQL)
	}
	// 参数序列对齐 Node candidateParams：sysID, pattern, sysID, pattern,
	// pattern, sysID, now, pattern, sysID, limit。
	now := rfc3339Millis(deps.Now())
	wantArgs := []string{
		"sys-caller-1", "needle", "sys-caller-1", "needle",
		"needle", "sys-caller-1", now, "needle", "sys-caller-1", "20",
	}
	if len(statement.args) != len(wantArgs) {
		t.Fatalf("参数数 = %d, 期望 %d", len(statement.args), len(wantArgs))
	}
	for i, want := range wantArgs {
		if got := fmt.Sprintf("%v", statement.args[i]); got != want {
			t.Fatalf("参数[%d] = %q, 期望 %q", i, got, want)
		}
	}
}

// TestAiPerformanceKeywordPostgresEscapeKeepsWildcardsLiteral：%/_/\ 之外，
// 已转义关键词不再引入通配语义（Node postgresSubstringLikePattern）。
func TestAiPerformanceKeywordPostgresEscapeKeepsWildcardsLiteral(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"perf%", `perf\%`},
		{"perf_name", `perf\_name`},
		{`a\b`, `a\\b`},
		{"100%_", `100\%\_`},
	}
	for _, testCase := range cases {
		if got := postgresSubstringLikePattern(testCase.raw); got != testCase.want {
			t.Fatalf("postgresSubstringLikePattern(%q) = %q, 期望 %q", testCase.raw, got, testCase.want)
		}
	}
}

// ---- 显式账户请求顺序（Node loadExplicitAiPerformanceAccounts* 496-529） ----

func TestAiPerformanceSeriesKeepsRequestedAccountOrder(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-a', '账户A', 'sys-owner-1', 'openai', 'api_key', 'active'),
			       ('acct-b', '账户B', 'sys-owner-1', 'openai', 'api_key', 'active'),
			       ('acct-c', '账户C', 'sys-owner-1', 'openai', 'api_key', 'active')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.aiPerformanceSeriesHandler(false)
	// 名称序为 A<B<C；请求序 b, c, a 必须原样导出。
	recorder := invoke(t, handler, http.MethodGet,
		"/__aisys__/api/stats/ai-performance/series?startDate=2026-09-04&endDate=2026-09-04&accountIds=acct-b&accountIds=acct-c&accountIds=acct-a",
		adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("series not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	accounts := dataMap(t, decodeBody(t, recorder))["accounts"].([]any)
	got := []string{}
	for _, account := range accounts {
		got = append(got, account.(map[string]any)["id"].(string))
	}
	want := []string{"acct-b", "acct-c", "acct-a"}
	if len(got) != len(want) {
		t.Fatalf("accounts = %#v, 期望 %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("accounts = %#v, 期望 %#v（请求顺序未保留）", got, want)
		}
	}
}

// ---- rank NULL → 0（Node Number(row.rank ?? 0)，NULL rank 排最前） ----

func TestDefaultAiPerformanceAccountsNullRankSortsFirst(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `
		CREATE TABLE usage_rank_snapshots (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, window_key TEXT NOT NULL,
			metric TEXT NOT NULL, snapshot_at TEXT NOT NULL, rank INTEGER, scope_id TEXT NOT NULL,
			metric_value REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id));
		CREATE TABLE accounts (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL,
			provider_code TEXT NOT NULL, deleted_at TEXT,
			authorization_instance_authorization_id TEXT);
		CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed := []string{
		// 同一快照批次：acct-a rank=1/100 次，acct-z rank 为 NULL/5 次。
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-01T00:00:00.000Z', 1, 'acct-a', 100)`,
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-01T00:00:00.000Z', NULL, 'acct-z', 5)`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code)
			VALUES ('acct-a', '账户A', 'sys-owner-1', 'openai'),
			       ('acct-z', '账户Z', 'sys-owner-1', 'openai')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	deps := &Deps{
		Business: db,
		Stats:    db,
		Now: func() time.Time {
			return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
		},
	}
	rows, err := deps.defaultAiPerformanceAccounts(httptest.NewRequest(http.MethodGet, "/", nil),
		perfScopeState{SystemAccountID: globalStatsSystemAccountID, ScopeType: "account", IncludeSystemAccountName: true}, 10)
	if err != nil {
		t.Fatalf("defaultAiPerformanceAccounts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	// NULL rank 按 Number(rank ?? 0) = 0 排最前（Go 旧实现排最后）。
	if rows[0].ID != "acct-z" || rows[1].ID != "acct-a" {
		t.Fatalf("顺序 = [%s %s], 期望 [acct-z acct-a]（NULL rank 应为 0 排最前）", rows[0].ID, rows[1].ID)
	}
	if rows[0].Rank == nil || *rows[0].Rank != 0 {
		t.Fatalf("NULL rank 应固化为 0，实际 %#v", rows[0].Rank)
	}
	if rows[0].RequestCountLast7d != 5 || rows[1].RequestCountLast7d != 100 {
		t.Fatalf("request_count_last_7d 漂移：%#v", rows)
	}
}

// ---- ai-health / 账户选项参数门（Node zod 契约） ----

func TestAiHealthQueryValidationRejectsOutOfRange(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.aiHealthListHandler(false)
	longKeyword := strings.Repeat("关", 201)
	cases := []struct {
		query string
		note  string
	}{
		{"hours=0", "hours 下界"},
		{"hours=745", "hours 上界"},
		{"hours=1.5", "hours 非整数"},
		{"hours=abc", "hours 非数字"},
		{"page=0", "page 下界"},
		{"pageSize=9", "pageSize 下界"},
		{"pageSize=51", "pageSize 上界"},
		{"keyword=" + longKeyword, "keyword 超 200"},
	}
	for _, testCase := range cases {
		recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health?"+testCase.query, adminAuth(""))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s (%s) 应 400，实际 %d", testCase.query, testCase.note, recorder.Code)
		}
		if got := decodeBody(t, recorder)["message"]; got != "AI 健康监控参数不合法" {
			t.Fatalf("%s 错误消息 = %#v", testCase.query, got)
		}
	}
	// 上边界值保持 200。
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health?hours=744&pageSize=50&page=1", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("边界值应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	// 缺省值不变。
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("缺省参数应 200: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAiPerformanceAccountsLimitNonIntegerFallsBackToDefault(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.aiPerformanceAccountsHandler(false)
	// Node integerQueryValue：limit 非整数 → undefined → 缺省 50，不报 400。
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-performance/accounts?keyword=x&limit=abc", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("limit=abc 应回退缺省 200，实际 %d %s", recorder.Code, recorder.Body.String())
	}
	// 越界仍 400（Node zod min(1).max(50)）。
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-performance/accounts?limit=51", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=51 应 400，实际 %d", recorder.Code)
	}
}

func TestAccountUsageOptionsKeywordTooLongRejected(t *testing.T) {
	// 边界：200 字通过，201 字标记为过长（Node zod .max(200)）。
	longKeyword := strings.Repeat("k", 201)
	if _, tooLong := boundedKeyword(urlValuesFrom(longKeyword), "keyword"); !tooLong {
		t.Fatalf("201 字 keyword 应标记过长")
	}
	if _, tooLong := boundedKeyword(urlValuesFrom(strings.Repeat("k", 200)), "keyword"); tooLong {
		t.Fatalf("200 字 keyword 不应标记过长")
	}
	fixture := newFixture(t)
	handler := fixture.deps.accountUsageOptionsHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/account-usage/options?keyword="+longKeyword, adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("超长 keyword 应 400，实际 %d", recorder.Code)
	}
}

// ---- trendStatusRoles performance 分支 + worker:N 家族 ----

func TestTrendStatusRolesPerformanceExportsDataRows(t *testing.T) {
	rows := []Row{
		{"process_role": "ops-worker:2", "process_pid": int64(102), "sampled_at": "2026-09-04T11:59:00.000Z"},
		{"process_role": "gateway:a1", "process_pid": int64(101), "sampled_at": "2026-09-04T11:58:00.000Z"},
		{"process_role": "ingest-worker:1", "process_pid": int64(103), "sampled_at": "2026-09-04T11:57:00.000Z"},
		{"process_role": "bogus-role", "process_pid": int64(104), "sampled_at": "2026-09-04T11:56:00.000Z"},
	}

	performance := &Deps{RuntimeMode: "performance"}
	roles := performance.trendStatusRoles(rows)
	want := []string{"gateway:a1", "ingest-worker:1", "ops-worker:2"}
	if len(roles) != len(want) {
		t.Fatalf("performance roles = %#v, 期望 %#v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("performance roles = %#v, 期望 %#v", roles, want)
		}
	}
	statuses := performance.buildProcessEventLoopTrendLatestStatus(rows)
	if len(statuses) != 3 {
		t.Fatalf("performance latest = %#v", statuses)
	}
	for index, status := range statuses {
		if status.ProcessRole != want[index] || !status.SampleAvailable {
			t.Fatalf("latest[%d] = %#v, 期望角色 %s 且有样本", index, status, want[index])
		}
	}
	if statuses[0].ProcessPid == nil || *statuses[0].ProcessPid != 101 {
		t.Fatalf("gateway:a1 样本字段 = %#v", statuses[0])
	}
	peaks := performance.buildProcessEventLoopTrendPeakStatus(rows)
	if len(peaks) != 3 || peaks[2].ProcessRole != "ops-worker:2" || !peaks[2].SampleAvailable {
		t.Fatalf("performance peak = %#v", peaks)
	}

	// standalone 保持固定角色清单并为缺失角色补占位。
	standalone := &Deps{}
	standaloneRoles := standalone.trendStatusRoles(rows)
	wantStandalone := []string{"server", "ingest-worker", "stats-worker", "ops-worker", "db-service"}
	for i := range wantStandalone {
		if standaloneRoles[i] != wantStandalone[i] {
			t.Fatalf("standalone roles = %#v", standaloneRoles)
		}
	}
	standaloneStatuses := standalone.buildProcessEventLoopTrendLatestStatus(rows)
	if len(standaloneStatuses) != len(wantStandalone) {
		t.Fatalf("standalone latest 长度 = %d", len(standaloneStatuses))
	}
	for index, status := range standaloneStatuses {
		if status.ProcessRole != wantStandalone[index] {
			t.Fatalf("standalone latest[%d] = %#v", index, status)
		}
		// 固定清单只有裸角色；数据行是 ingest-worker:1，不命中任何固定角色。
		if status.SampleAvailable {
			t.Fatalf("standalone 模式 %s 不应有样本（数据角色为 ingest-worker:1）：%#v", status.ProcessRole, status)
		}
	}
}

func TestIsValidProcessRoleWorkerReplicaFamily(t *testing.T) {
	valid := []string{
		"server", "ingest-worker", "stats-worker", "ops-worker", "db-service",
		"gateway:gw-1", "control:c1", "control-replica:r1",
		"ingest-worker:1", "ingest-worker:9", "ingest-worker:10", "ingest-worker:64",
		"usage-worker:1", "log-worker:3", "stats-worker:27", "ops-worker:64",
	}
	invalid := []string{
		"", "worker", "worker:1", "ingest-worker:", "usage-worker",
		"ingest-worker:0", "ingest-worker:01", "ingest-worker:65", "ingest-worker:100",
		"usage-worker:1a", "log-worker:-1", "stats-worker:1.5", "ops-worker: 4",
		"usage-worker:96",
	}
	for _, value := range valid {
		if !isValidProcessRole(value) {
			t.Fatalf("%q 应为合法 process role", value)
		}
	}
	for _, value := range invalid {
		if isValidProcessRole(value) {
			t.Fatalf("%q 应为非法 process role", value)
		}
	}
}

// ---- NFKC 关键词归一化（Node normalizeAccountNameKeyword） ----

func TestNFKCTrimNormalizesKeyword(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"ＡＢＣ１２３", "ABC123"},
		{"ﬁle", "file"},
		{"①", "1"},
		{"ｶﾞ", "ガ"},
		{"  ｐｅｒｆ  ", "perf"},
		{"\u00A0Ａ\u00A0", "A"},
		{"plain", "plain"},
	}
	for _, testCase := range cases {
		if got := nfkcTrim(testCase.raw); got != testCase.want {
			t.Fatalf("nfkcTrim(%q) = %q, 期望 %q", testCase.raw, got, testCase.want)
		}
	}
}
