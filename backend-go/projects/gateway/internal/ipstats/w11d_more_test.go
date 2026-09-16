package ipstats

// w11d 覆盖补齐（二）：Detail/List 的查询与扫描错误臂、路由参数臂补全、
// CreatePolicy 事务错误臂与 normalizeRange/daysBetween 助手。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// w11dDetailScanDB 构造允许 NULL 的账户窗口表（触发 Detail 扫描错误）。
func w11dDetailScanDB(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:w11d-ipstats-scan-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`,
		`INSERT INTO system_settings VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`,
		`CREATE TABLE client_ip_registry (ip_hash TEXT, aggregate_ip_key TEXT, client_ip TEXT, bucket_no INTEGER, ip_version INTEGER, first_seen_at TEXT, last_seen_at TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE client_ip_range_window_dirty_ips (ip_hash TEXT PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 1, first_dirty_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '', job_name TEXT NOT NULL, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, last_error_message TEXT, lag_seconds INTEGER, updated_at TEXT NOT NULL, PRIMARY KEY (scope_type, scope_id, job_name))`,
		`CREATE TABLE client_ip_usage_range_windows (ip_hash TEXT, start_date TEXT, end_date TEXT, updated_at TEXT)`,
		`CREATE TABLE client_ip_account_usage_range_windows (ip_hash TEXT, account_id TEXT, start_date TEXT, end_date TEXT, request_count INTEGER, success_count INTEGER, error_count INTEGER, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_read_cost_usd REAL, cache_write_tokens INTEGER, cache_write_1h_tokens INTEGER, cache_write_cost_usd REAL, thinking_tokens INTEGER, input_image_tokens INTEGER, output_image_tokens INTEGER, total_cost_usd REAL, duration_ms_sum INTEGER, duration_ms_count INTEGER, duration_ms_max INTEGER, average_duration_ms REAL, first_token_ms_sum INTEGER, first_token_ms_count INTEGER, average_first_token_ms REAL, active_days INTEGER, last_used_at TEXT, last_error_at TEXT, updated_at TEXT)`,
		`CREATE TABLE client_ip_policies (id TEXT PRIMARY KEY, ip_hash TEXT, policy_type TEXT, status TEXT, reason TEXT, expires_at TEXT, created_by_system_account_id TEXT, created_at TEXT, updated_at TEXT, disabled_at TEXT, disabled_by_system_account_id TEXT, disabled_reason TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("建表: %v\n%s", err, statement)
		}
	}
	store, err := NewStore(db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

const w11dScanHash = "1111111111111111111111111111111111111111111111111111111111111111"

func TestW11DDetailScanAndQueryErrorArms(t *testing.T) {
	store := w11dDetailScanDB(t)
	db := store.db
	ctx := context.Background()
	now := "2026-09-04T00:00:00.000Z"
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC().Format("2006-01-02")
	exec(`INSERT INTO client_ip_registry VALUES (?, ?, '203.0.113.9', 0, 4, ?, ?, ?, ?)`, w11dScanHash, strings.ToUpper(w11dScanHash), now, now, now, now)
	exec(`INSERT INTO client_ip_usage_range_windows (ip_hash, start_date, end_date, updated_at) VALUES (?, ?, ?, ?)`, w11dScanHash, today, today, now)
	// 坏 hash → nil。
	detail, err := store.Detail(ctx, DetailOptions{IPHash: "bad"})
	if err != nil || detail != nil {
		t.Fatalf("坏 hash = %v err=%v", detail, err)
	}
	// 扫描错误：account_id NULL。
	exec(`INSERT INTO client_ip_account_usage_range_windows (ip_hash, account_id, start_date, end_date, updated_at) VALUES (?, NULL, ?, ?, ?)`, w11dScanHash, today, today, now)
	if _, err := store.Detail(ctx, DetailOptions{IPHash: w11dScanHash, StartDate: today, EndDate: today}); err == nil {
		t.Fatal("NULL account_id 扫描必须报错")
	}
	// 名称水合错误：接入坏 lookup。
	exec(`DELETE FROM client_ip_account_usage_range_windows`)
	exec(`INSERT INTO client_ip_account_usage_range_windows (ip_hash, account_id, start_date, end_date, updated_at) VALUES (?, 'acc1', ?, ?, ?)`, w11dScanHash, today, today, now)
	store.SetDetailAccountLookup(w11dFailingLookup{})
	if _, err := store.Detail(ctx, DetailOptions{IPHash: w11dScanHash, StartDate: today, EndDate: today}); err == nil {
		t.Fatal("账户名查询失败必须上抛")
	}
	store.SetDetailAccountLookup(w11dNamesFailingLookup{})
	if _, err := store.Detail(ctx, DetailOptions{IPHash: w11dScanHash, StartDate: today, EndDate: today}); err == nil {
		t.Fatal("owner 名查询失败必须上抛")
	}
	// rangeReady 错误：drop dirty 表。
	store.SetDetailAccountLookup(nil)
	if _, err := db.Exec(`DROP TABLE client_ip_range_window_dirty_ips`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Detail(ctx, DetailOptions{IPHash: w11dScanHash}); err == nil {
		t.Fatal("rangeReady 错误必须上抛")
	}
	// registry 查询错误。
	if _, err := db.Exec(`DROP TABLE client_ip_registry`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Detail(ctx, DetailOptions{IPHash: w11dScanHash}); err == nil {
		t.Fatal("registry 查询错误必须上抛")
	}
}

// w11dFailingLookup 账户名查询恒失败。
type w11dFailingLookup struct{}

func (w11dFailingLookup) LookupAccounts(context.Context, []string) (map[string]AccountLookup, error) {
	return nil, sql.ErrConnDone
}
func (w11dFailingLookup) SystemAccountNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// w11dNamesFailingLookup owner 名查询恒失败。
type w11dNamesFailingLookup struct{}

func (w11dNamesFailingLookup) LookupAccounts(context.Context, []string) (map[string]AccountLookup, error) {
	return map[string]AccountLookup{}, nil
}
func (w11dNamesFailingLookup) SystemAccountNames(context.Context, []string) (map[string]string, error) {
	return nil, sql.ErrConnDone
}

func TestW11DListQueryAndScanErrorArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
	env.markWindowReady(t, todayKey(), todayKey())

	// 扫描错误：聚合键 NULL（独立可空 schema）。
	t.Run("list-scan-error", func(t *testing.T) {
		scanStore := w11dDetailScanDB(t)
		scanDB := scanStore.db
		now := "2026-09-04T00:00:00.000Z"
		today := time.Now().UTC().Format("2006-01-02")
		if _, err := scanDB.Exec(`INSERT INTO client_ip_registry VALUES (?, NULL, '203.0.113.9', 0, 4, ?, ?, ?, ?)`, w11dScanHash, now, now, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := scanDB.Exec(`INSERT INTO client_ip_usage_range_windows (ip_hash, start_date, end_date, updated_at) VALUES (?, ?, ?, ?)`, w11dScanHash, today, today, now); err != nil {
			t.Fatal(err)
		}
		if _, err := scanStore.List(context.Background(), ListOptions{Page: 1, PageSize: 20}); err == nil {
			t.Fatal("NULL aggregate_ip_key 扫描必须报错")
		}
	})
	// lastUsed 过滤：窗口外行被剔除（continue 臂）。
	result, err := env.store.List(ctx, ListOptions{Page: 1, PageSize: 20, LastUsedStartDate: "2020-01-01", LastUsedEndDate: "2020-01-02"})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("窗口外过滤 = %d err=%v", len(result.Items), err)
	}
	// lastUsedAt 坏时间戳 → sortRows 错误。
	if _, err := env.db.Exec(`UPDATE client_ip_registry SET last_seen_at = 'not-a-time' WHERE ip_hash = ?`, testHashA); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.List(ctx, ListOptions{Page: 1, PageSize: 20, SortField: "lastUsedAt", LastUsedSortScope: "global"}); err == nil {
		t.Fatal("坏 lastSeenAt 必须报错")
	}
	// rangeReady 错误：drop dirty 表。
	t.Run("range-ready-error", func(t *testing.T) {
		drop := newTestEnv(t)
		if _, err := drop.db.Exec(`DROP TABLE client_ip_range_window_dirty_ips`); err != nil {
			t.Fatal(err)
		}
		if _, err := drop.store.List(context.Background(), ListOptions{Page: 1, PageSize: 20}); err == nil {
			t.Fatal("rangeReady 错误必须上抛")
		}
	})
	// 活跃策略扫描错误：policy_type NULL。
	t.Run("policy-scan-error", func(t *testing.T) {
		scan := newTestEnv(t)
		scan.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
		if _, err := scan.db.Exec(`DROP TABLE client_ip_policies`); err != nil {
			t.Fatal(err)
		}
		if _, err := scan.db.Exec(`CREATE TABLE client_ip_policies (id TEXT PRIMARY KEY, ip_hash TEXT, policy_type TEXT, status TEXT, reason TEXT, expires_at TEXT, created_by_system_account_id TEXT, created_at TEXT, updated_at TEXT, disabled_at TEXT, disabled_by_system_account_id TEXT, disabled_reason TEXT)`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := scan.db.Exec(`INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, created_by_system_account_id, created_at, updated_at) VALUES ('p1', ?, NULL, 'active', 'sys', ?, ?)`, testHashA, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := scan.store.List(context.Background(), ListOptions{Page: 1, PageSize: 20}); err == nil {
			t.Fatal("策略扫描错误必须上抛")
		}
	})
	// 列表查询错误：drop registry。
	if _, err := env.db.Exec(`DROP TABLE client_ip_registry`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.List(ctx, ListOptions{Page: 1, PageSize: 20}); err == nil {
		t.Fatal("列表查询错误必须上抛")
	}
}

func TestW11DRouteDetailParamArmsWithPaths(t *testing.T) {
	env := newDetailEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, admin)
	deps := &Deps{Store: env.store, Auth: env.deps, Sink: env.sink}
	run := func(target string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.SetPathValue("ipHash", detailIPHash)
		deps.handleDetail(recorder, request)
		return recorder.Code
	}
	detailPath := "/__aisys__/api/ip-stats/" + detailIPHash + "/detail"
	if code := run(detailPath + "?pageSize=101"); code != http.StatusBadRequest {
		t.Fatalf("坏 pageSize = %d", code)
	}
	if code := run(detailPath + "?pageSize=abc"); code != http.StatusBadRequest {
		t.Fatalf("非数字 pageSize = %d", code)
	}
	if code := run(detailPath + "?page=abc"); code != http.StatusBadRequest {
		t.Fatalf("坏 page = %d", code)
	}
	if code := run(detailPath + "?page=1.5"); code != http.StatusBadRequest {
		t.Fatalf("小数 page = %d", code)
	}
	if code := run(detailPath + "?page=0x10"); code != http.StatusOK {
		t.Fatalf("十六进制 page = %d", code)
	}
	if code := run(detailPath + "?sortField=weird"); code != http.StatusBadRequest {
		t.Fatalf("坏 sortField = %d", code)
	}
	if code := run(detailPath + "?sortOrder=weird"); code != http.StatusBadRequest {
		t.Fatalf("坏 sortOrder = %d", code)
	}
	if code := run(detailPath + "?pageSize=0"); code != http.StatusBadRequest {
		t.Fatalf("零 pageSize = %d", code)
	}
}

func TestW11DDisablePolicyHandlerArms(t *testing.T) {
	env := newDetailEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, "sys")
	deps := &Deps{Store: env.store, Auth: env.deps, Sink: env.sink}

	post := func(body string) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+detailIPHash+"/unallowlist", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
		request.SetPathValue("ipHash", detailIPHash)
		deps.handleDisablePolicy(recorder, request, PolicyTypeAllowlist)
		return recorder.Code
	}
	// parsePolicyBody 失败 → 400。
	if code := post(`{"unknown": 1}`); code != http.StatusBadRequest {
		t.Fatalf("未知字段 = %d", code)
	}
	// 合法 body（无登录上下文）→ 401。
	if code := post(`{}`); code != http.StatusUnauthorized {
		t.Fatalf("未登录停用 = %d", code)
	}
	// 坏 hash → 400。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/zz/detail", strings.NewReader(`{}`))
	request.SetPathValue("ipHash", "zz")
	deps.handleDisablePolicy(recorder, request, PolicyTypeAllowlist)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("坏 hash 停用 = %d", recorder.Code)
	}
}

func TestW11DCreatePolicyTransactionArms(t *testing.T) {
	env := newTestEnv(t)
	env.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
	ctx := context.Background()

	// BeginTx 错误：关闭连接池后新事务失败。
	closed, err := NewStore(env.db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: "sys"}); err == nil {
		t.Fatal("BeginTx 失败必须上抛")
	}
	// PG lockSuffix 臂 + registry 查询错误：pg=true + 独立 SQLite 库。
	t.Run("pg-lock-suffix", func(t *testing.T) {
		fresh := newTestEnv(t)
		fresh.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
		pgStore, err := NewStore(fresh.db, true, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgStore.CreatePolicy(context.Background(), PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: "sys"}); err == nil {
			t.Fatal("PG 绑定在 SQLite 上必须失败")
		}
	})
}

func TestW11DNormalizeRangeHelperArms(t *testing.T) {
	location := time.UTC
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	// 反转区间塌缩。
	collapsed := normalizeRange("2026-09-10", "2026-09-01", now, location)
	if collapsed.StartDate != collapsed.EndDate {
		t.Fatalf("反转区间 = %+v", collapsed)
	}
	// 坏日期回退今天。
	badDates := normalizeRange("nope", "nada", now, location)
	if badDates.StartDate != "2026-09-16" || badDates.EndDate != "2026-09-16" {
		t.Fatalf("坏日期回退 = %+v", badDates)
	}
	// daysBetween 下限钳制（直接调用允许反转）。
	if got := daysBetweenInclusive("2026-09-03", "2026-09-01", "2026-09-01"); got != 1 {
		t.Fatalf("下限钳制 = %d", got)
	}
	// clampDateKey 双向钳制。
	if got := clampDateKey("2099-01-01", "2026-09-16", "2026-08-17"); got != "2026-09-16" {
		t.Fatalf("上界钳制 = %q", got)
	}
	if got := clampDateKey("2020-01-01", "2026-09-16", "2026-08-17"); got != "2026-08-17" {
		t.Fatalf("下界钳制 = %q", got)
	}
	if got := clampDateKey("2026-09-10", "2026-09-16", "2026-08-17"); got != "2026-09-10" {
		t.Fatalf("区间内 = %q", got)
	}
	// boundedPage 大页钳制。
	if got := boundedPage(1, 2000); got != 1 {
		t.Fatalf("超窗口页钳制 = %d", got)
	}
}
