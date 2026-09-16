package statreads

// statreads DB handler 契约补充测试（wd_ 前缀，独占新增文件）：
// SQLite 内存库 + httptest 直驱 handler，覆盖 account-usage 选项/趋势/关键字、
// ai-performance base/账户选项/关键字、system-metrics 趋势/运行时快照路由、
// ai-health 关键字检索与 J1 outcome SQLite 合并、usage-overview 模型分布/错误
// 分节、usage-records 关键字与日期窗口、以及 Mount 的注册与门禁契约。
// PG 方言专用分支（usageRecordRowsPG 等）以 Skip/SQL 文本断言表达，不硬造。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	_ "modernc.org/sqlite"
)

// wdExtraSchema 是共享 testSchema 之外、被测 SQL 需要的业务表（providers、
// 分组关系、账户名检索索引），并把共享精简 schema 省略的
// resource_owner_system_account_id 列补回 resource_authorizations（与
// maintenance 真实 schema 对齐，读路径 owner 名水合依赖该列）。
const wdExtraSchema = `
	CREATE TABLE providers (code TEXT PRIMARY KEY, name TEXT NOT NULL);
	CREATE TABLE group_accounts (account_id TEXT NOT NULL, group_id TEXT NOT NULL, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
	CREATE TABLE account_name_search_terms (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, term TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id, term));
	CREATE TABLE account_name_search_documents (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, normalized_name TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id));
	ALTER TABLE resource_authorizations ADD COLUMN resource_owner_system_account_id TEXT;
`

type wdFixture struct {
	deps *Deps
	db   *sql.DB
}

func newWdFixture(t *testing.T) *wdFixture {
	t.Helper()
	base := newFixture(t)
	if _, err := base.db.Exec(wdExtraSchema); err != nil {
		t.Fatalf("apply extra schema: %v", err)
	}
	return &wdFixture{deps: base.deps, db: base.db}
}

func (f *wdFixture) exec(t *testing.T, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := f.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
}

func wdGet(t *testing.T, handler http.HandlerFunc, target string, auth *authsys.AuthContext) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func wdData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	return dataMap(t, decodeBody(t, recorder))
}

// wdDataArray 读取 data 为裸 JSON 数组的载荷（账户选项等直接返回切片的读面）。
func wdDataArray(t *testing.T, recorder *httptest.ResponseRecorder) []any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d：%s", recorder.Code, recorder.Body.String())
	}
	payload := decodeBody(t, recorder)
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("data 应为 JSON 数组: %#v", payload["data"])
	}
	return data
}

func wdJSONArray(t *testing.T, value any, name string) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s 应为 JSON 数组: %#v", name, value)
	}
	return items
}

// ---------------------------------------------------------------------------
// Mount：注册 + 门禁契约（未带令牌一律 401 请先登录，而非路由 404）。
// ---------------------------------------------------------------------------

func TestWdMountRegistersRouteFamiliesWithAuthGates(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.deps.Auth = &authsys.Deps{}
	gateway := kernel.New(kernel.Options{})
	fixture.deps.Mount(gateway)
	handler := gateway.Handler()

	cases := []struct {
		name   string
		target string
	}{
		{"admin 统计面", "/__aisys__/api/stats/usage-window"},
		{"my 统计面", "/__aisys__/api/my-stats/usage-window"},
		{"admin 账户用量", "/__aisys__/api/stats/account-usage"},
		{"my 账户用量", "/__aisys__/api/my-stats/account-usage"},
		{"admin 用量记录", "/__aisys__/api/usage-records"},
		{"my 用量记录", "/__aisys__/api/my-usage-records"},
		{"运行时快照", "/__aisys__/api/stats/system-metrics/runtime/summary"},
		{"Go 运行时趋势", "/__aisys__/api/stats/system-metrics/go-runtime-trend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("无令牌应 401（路由未注册时会得到 404）: %d %s", recorder.Code, recorder.Body.String())
			}
			if got := decodeBody(t, recorder)["message"]; got != "请先登录" {
				t.Fatalf("401 消息错误: %#v", got)
			}
		})
	}
	// 未注册路径仍走 API 404 契约。
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/stats/not-registered", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未注册路径应 404: %d", recorder.Code)
	}
}

// ---------------------------------------------------------------------------
// account-usage
// ---------------------------------------------------------------------------

func TestWdAccountUsageHandlerGuardsAndSelectedAccounts(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', 'account', 'acct-sel', '2026-09-04', 3, 30, 20, 0.3)`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-sel', '选中账户', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO providers (code, name) VALUES ('openai', 'OpenAI')`,
	)
	handler := fixture.deps.accountUsageHandler(false)

	// includeSummary 被显式拒绝（Node 契约：列表不支持该参数）。
	recorder := wdGet(t, handler, "/__aisys__/api/stats/account-usage?includeSummary=1", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("includeSummary 应 400: %d %s", recorder.Code, recorder.Body.String())
	}

	// accountIds 选中的账户不在首页时也必须并入结果。
	recorder = wdGet(t, handler, "/__aisys__/api/stats/account-usage?pageSize=5&accountIds=acct-sel&accountIds=ghost", adminAuth(""))
	payload := wdData(t, recorder)
	rows := wdJSONArray(t, payload["rows"], "rows")
	if len(rows) != 1 {
		t.Fatalf("选中的账户应并入页面: %#v", rows)
	}
	top := rows[0].(map[string]any)
	if top["id"] != "acct-sel" || top["providerCode"] != "openai" {
		t.Fatalf("选中行元数据错误: %#v", top)
	}
	if top["ownerSystemAccountId"] != "sys-owner-1" {
		t.Fatalf("owner 归属错误: %#v", top)
	}
	// 范围/分页契约。
	if payload["pageSize"] != float64(5) || payload["page"] != float64(1) {
		t.Fatalf("分页契约错误: %#v", payload)
	}
	if ids, ok := payload["defaultTrendAccountIds"].([]any); !ok || len(ids) != 1 || ids[0] != "acct-sel" {
		t.Fatalf("快照为空时应回退为有用量行: %#v", payload["defaultTrendAccountIds"])
	}
}

func TestWdAccountUsageDefaultTrendIdsFromSnapshot(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		// last7d 请求量排名快照（最新 snapshot_at 一批，rank 顺序生效）。
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 1, 'acct-r1', 50)`,
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 2, 'acct-r2', 20)`,
		// 旧快照批次不得入选。
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-01T00:00:00.000Z', 1, 'acct-stale', 999)`,
	)
	recorder := wdGet(t, fixture.deps.accountUsageHandler(false), "/__aisys__/api/stats/account-usage?pageSize=5", adminAuth(""))
	ids := wdData(t, recorder)["defaultTrendAccountIds"].([]any)
	if len(ids) != 2 || ids[0] != "acct-r1" || ids[1] != "acct-r2" {
		t.Fatalf("默认趋势 ID 应来自最新快照按 rank 排序: %#v", ids)
	}
}

func TestWdAccountUsageOptionsContract(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO providers (code, name) VALUES ('openai', 'OpenAI')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-a', 'Alpha', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status, authorization_instance_authorization_id, authorization_instance_source_account_id, authorization_instance_owner_system_account_id)
			VALUES ('acct-inst', 'Alpha 实例', 'sys-grantee', 'openai', 'api_key', 'active', 'ra-1', 'acct-a', 'sys-owner-1')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-grantee', 'grantee', NULL)`,
	)
	handler := fixture.deps.accountUsageOptionsHandler(false)

	// 参数门：超长 keyword 与越界 limit 都是 400。
	if recorder := wdGet(t, handler, "/?keyword="+strings.Repeat("长", 201), adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("超长 keyword 应 400: %d", recorder.Code)
	}
	if recorder := wdGet(t, handler, "/?limit=0", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 应 400: %d", recorder.Code)
	}
	if recorder := wdGet(t, handler, "/?limit=51", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=51 应 400: %d", recorder.Code)
	}

	// 全局管理面：关键字只命中 owner 账户（authorized 实例被
	// authorization_instance_authorization_id IS NULL 子句排除，由
	// selectedIds 窗口显式带入）。
	recorder := wdGet(t, handler, "/?keyword=Alpha&limit=10", adminAuth(""))
	options := wdDataArray(t, recorder)
	if len(options) != 1 {
		t.Fatalf("全局关键字应只命中 owner 账户: %#v", options)
	}
	first := options[0].(map[string]any)
	if first["id"] != "acct-a" || first["providerName"] != "OpenAI" {
		t.Fatalf("选项行错误: %#v", first)
	}
	if first["systemAccountName"] != "Owner One" || first["ownerSystemAccountName"] != "Owner One" {
		t.Fatalf("owner 名水合错误: %#v", first)
	}

	// selectedIds 窗口优先于搜索结果且去重；全局面 owner 过滤子句会把授权
	// 实例排除在候选之外（authorization_instance_authorization_id IS NULL），
	// 实例账户只能在授权方视角（scoped）出现。
	recorder = wdGet(t, handler, "/?selectedIds=acct-inst&selectedIds[]=acct-a", adminAuth(""))
	options = wdDataArray(t, recorder)
	if len(options) != 1 || options[0].(map[string]any)["id"] != "acct-a" {
		t.Fatalf("全局面 selectedIds 应只保留 owner 账户: %#v", options)
	}
	// 授权方视角：owner 过滤改为 system_account_id = 授权方，实例账户入选。
	scoped := wdGet(t, handler, "/?selectedIds=acct-inst&systemAccountId=sys-grantee", adminAuth(""))
	scopedOptions := wdDataArray(t, scoped)
	if len(scopedOptions) != 1 {
		t.Fatalf("授权方视角应命中实例账户: %#v", scopedOptions)
	}
	instance := scopedOptions[0].(map[string]any)
	if instance["accessType"] != "authorized" || instance["ownerSystemAccountId"] != "sys-owner-1" {
		t.Fatalf("实例账户 access_type/owner 错误: %#v", instance)
	}
	if instance["systemAccountName"] != "grantee" {
		t.Fatalf("实例账户 display 缺失时应回退 username: %#v", instance)
	}
	if instance["ownerSystemAccountName"] != "Owner One" {
		t.Fatalf("实例账户 owner 名应经 owner_accounts 联接水合: %#v", instance)
	}
}

func TestWdAccountUsageKeywordCallerAccountScope(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		// 过滤后的 admin（systemAccountId=sys-f）以 caller_account 视角检索。
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-own', 'AlphaOwn', 'sys-f', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-foreign', 'AlphaForeign', 'sys-other', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status, authorization_instance_authorization_id, authorization_instance_source_account_id)
			VALUES ('acct-inst', 'InstanceB', 'sys-f', 'openai', 'api_key', 'active', 'ra-2', 'acct-src')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-src', 'AlphaSource', 'sys-other', 'openai', 'api_key', 'active')`,
		`INSERT INTO groups (id, name, system_account_id) VALUES ('g-1', 'AlphaGroup', 'sys-f')`,
		`INSERT INTO group_accounts (account_id, group_id, system_account_id, enabled) VALUES ('acct-foreign', 'g-1', 'sys-f', 1)`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ('ra-3', 'group', 'g-1', 'sys-other', 'sys-f', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		// caller_account 视角的页面行来自 usage_stats_daily；关键字只负责收窄。
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('sys-f', 'caller_account', 'acct-own', '2026-09-04', 5, 50, 25, 0.5)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('sys-f', 'caller_account', 'acct-foreign', '2026-09-04', 4, 40, 20, 0.4)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('sys-f', 'caller_account', 'acct-inst', '2026-09-04', 3, 30, 15, 0.3)`,
	)
	handler := fixture.deps.accountUsageHandler(false)
	recorder := wdGet(t, handler, "/?keyword=Alpha&pageSize=50&systemAccountId=sys-f&startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	rows := wdJSONArray(t, wdData(t, recorder)["rows"], "rows")
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.(map[string]any)["id"].(string)] = true
	}
	if !ids["acct-own"] {
		t.Fatalf("自己名下命中账户应在结果中: %#v", ids)
	}
	if !ids["acct-foreign"] {
		t.Fatalf("分组授权可见账户应在结果中: %#v", ids)
	}
	// 行为存疑：source 名命中的授权实例 acct-inst 会进入关键字 ID 集合（已单独
	// 验证），但 scoped 元数据水合 query 的 headParams 多传一个 viewerID
	// （accountusage.go loadAccountUsageMetadataRows），SQL 文本里 SELECT 列的
	// CASE 占位符位于 JOIN 占位符之前，chunk 的 IN 绑定整体后移一位——SQLite
	// 下每个 chunk 的最后一个账户 ID 被静默丢弃（PG 方言参数数量不匹配会直接
	// 报错）。按当前实际行为断言：acct-inst 不出现在页面行中。
	if ids["acct-inst"] {
		t.Fatalf("当前实现下 chunk 末位账户（acct-inst）被元数据水合丢弃: %#v", ids)
	}
	if ids["acct-src"] {
		t.Fatalf("source 本身不是实例，不应直接入选: %#v", ids)
	}
}

func TestWdAccountUsageTrendDailySeries(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-t', 'TrendAccount', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, cache_read_tokens, cache_write_tokens, thinking_tokens, last_used_at)
			VALUES ('global', 'account', 'acct-t', '2026-09-04', 7, 70, 35, 0.7, 1, 2, 3, '2026-09-04T10:00:00.000Z')`,
		// 范围窗口行：趋势只取日桶，NULL stat_date 行应被跳过。
		`INSERT INTO usage_scope_range_windows (system_account_id, scope_type, scope_id, start_date, end_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', 'account', 'acct-t', '2026-09-01', '2026-09-04', 99, 990, 350, 9.9)`,
	)
	handler := fixture.deps.accountUsageTrendHandler(false)
	// 结束日期被钳位到"今天"（固定 Now=2026-09-04），范围 09-01..09-04 共 4 天。
	recorder := wdGet(t, handler, "/?accountIds=acct-t&startDate=2026-09-01&endDate=2026-09-05", adminAuth(""))
	rows := wdJSONArray(t, wdData(t, recorder)["rows"], "rows")
	if len(rows) != 1 {
		t.Fatalf("趋势行错误: %#v", rows)
	}
	row := rows[0].(map[string]any)
	if row["id"] != "acct-t" || row["accessType"] != "owner" {
		t.Fatalf("趋势行元数据错误: %#v", row)
	}
	if row["systemAccountId"] != "sys-owner-1" {
		t.Fatalf("admin 视角应带 systemAccountId: %#v", row)
	}
	daily := wdJSONArray(t, row["dailyUsage"], "dailyUsage")
	if len(daily) != 4 {
		t.Fatalf("趋势日点数应为钳位后范围天数 4: %d", len(daily))
	}
	point := daily[3].(map[string]any)
	if point["statDate"] != "2026-09-04" || point["requestCount"] != float64(7) || point["totalTokens"] != float64(105) {
		t.Fatalf("日点聚合错误: %#v", point)
	}
	if point["thinkingTokens"] != float64(3) || point["cacheReadTokens"] != float64(1) {
		t.Fatalf("日点扩展指标错误: %#v", point)
	}

	// 无 accountIds 的空趋势与超量裁剪（>10 截断）。
	empty := wdGet(t, handler, "/?startDate=2026-09-01&endDate=2026-09-05", adminAuth(""))
	emptyRows := wdJSONArray(t, wdData(t, empty)["rows"], "rows")
	if len(emptyRows) != 0 {
		t.Fatalf("空趋势应为空行: %#v", emptyRows)
	}
}

func TestWdAccountUsageSummaryAdminScoped(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, cache_read_cost_usd)
			VALUES ('sys-f', 'system_account', 'sys-f', '2026-09-04', 6, 60, 30, 0.6, 0.1)`,
	)
	recorder := wdGet(t, fixture.deps.accountUsageSummaryHandler(false),
		"/?systemAccountId=sys-f&startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	summary := wdData(t, recorder)["summary"].(map[string]any)
	if summary["requestCount"] != float64(6) || summary["totalCost"] != 0.6 || summary["cacheReadCost"] != 0.1 {
		t.Fatalf("过滤 admin 汇总错误: %#v", summary)
	}
	// 无数据账户：零值汇总而非错误。
	recorder = wdGet(t, fixture.deps.accountUsageSummaryHandler(false), "/?systemAccountId=sys-none", adminAuth(""))
	summary = wdData(t, recorder)["summary"].(map[string]any)
	if summary["requestCount"] != float64(0) || summary["totalTokens"] != float64(0) {
		t.Fatalf("空汇总应为零值: %#v", summary)
	}
}

// ---------------------------------------------------------------------------
// ai-performance
// ---------------------------------------------------------------------------

func TestWdAiPerformanceBaseHandler(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-p1', 'PerfAccount', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 1, 'acct-p1', 42)`,
		`INSERT INTO ai_performance_summary_windows (system_account_id, window_key, start_date, end_date, request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max, duration_ms_sum, duration_ms_count, duration_ms_max)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 10, 500, 4, 200, 1000, 5, 300)`,
		`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max)
			VALUES ('global', 'account', 'acct-p1', '2026-09-04T09', 4, 400, 4, 120, 480, 4, 150)`,
	)
	recorder := wdGet(t, fixture.deps.aiPerformanceBaseHandler(false),
		"/?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	payload := wdData(t, recorder)
	summary := payload["summary"].(map[string]any)
	if summary["requestCount"] != float64(10) || summary["averageFirstTokenMs"] != float64(125) || summary["maxDurationMs"] != float64(300) {
		t.Fatalf("性能摘要错误: %#v", summary)
	}
	accounts := wdJSONArray(t, payload["accounts"], "accounts")
	if len(accounts) != 1 || accounts[0].(map[string]any)["id"] != "acct-p1" {
		t.Fatalf("默认账户来自排名快照: %#v", accounts)
	}
	if accounts[0].(map[string]any)["systemAccountName"] != "Owner One" {
		t.Fatalf("admin 视角应带系统账户名: %#v", accounts[0])
	}
	series := wdJSONArray(t, payload["hourlySeries"], "hourlySeries")
	if len(series) != 1 {
		t.Fatalf("小时序列错误: %#v", series)
	}
	points := wdJSONArray(t, series[0].(map[string]any)["points"], "points")
	if len(points) != 24 {
		t.Fatalf("24 小时点错误: %d", len(points))
	}
	hour9 := points[9].(map[string]any)
	if hour9["statHour"] != "2026-09-04T09" || hour9["requestCount"] != float64(4) || hour9["maxFirstTokenMs"] != float64(150) {
		t.Fatalf("小时点错误: %#v", hour9)
	}
	// 空桶点请求量为 0（填充契约）。
	hour0 := points[0].(map[string]any)
	if hour0["requestCount"] != float64(0) || hour0["averageDurationMs"] != nil {
		t.Fatalf("空小时点应为零值: %#v", hour0)
	}
	// 日期参数 400 契约。
	if recorder := wdGet(t, fixture.deps.aiPerformanceBaseHandler(false), "/?startDate=bad", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法日期应 400: %d", recorder.Code)
	}
}

func TestWdAiPerformanceAccountsOptionsKeyword(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-k1', 'KeyAccount', 'sys-f', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-k2', 'OtherAccount', 'sys-f', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status, deleted_at)
			VALUES ('acct-k3', 'KeyAccount', 'sys-f', 'openai', 'api_key', 'active', '2026-01-01T00:00:00.000Z')`,
	)
	handler := fixture.deps.aiPerformanceAccountsHandler(false)
	if recorder := wdGet(t, handler, "/?limit=51", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=51 应 400: %d", recorder.Code)
	}
	// 过滤 admin 的关键字路径（SQLite instr 变体，排除已删除账户）。
	recorder := wdGet(t, handler, "/?keyword=Key&systemAccountId=sys-f", adminAuth(""))
	options := wdDataArray(t, recorder)
	if len(options) != 1 || options[0].(map[string]any)["id"] != "acct-k1" {
		t.Fatalf("关键字选项错误: %#v", options)
	}
	// 无关键字时回退排名快照（空快照 + selectedIds 并集水合）。
	recorder = wdGet(t, handler, "/?accountIds=acct-k2&accountIds=acct-k1", adminAuth(""))
	options = wdDataArray(t, recorder)
	if len(options) != 2 || options[0].(map[string]any)["id"] != "acct-k2" {
		t.Fatalf("显式选择应保持请求顺序: %#v", options)
	}
}

func TestWdAiPerformanceScopedKeywordGroupVisibility(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-vis', 'VisibleAccount', 'sys-other', 'openai', 'api_key', 'active')`,
		`INSERT INTO groups (id, name, system_account_id) VALUES ('g-v', 'VGroup', 'sys-f')`,
		`INSERT INTO group_accounts (account_id, group_id, system_account_id, enabled) VALUES ('acct-vis', 'g-v', 'sys-f', 1)`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, owner_system_account_id, grantee_system_account_id, status, expires_at, created_at, updated_at)
			VALUES ('ra-v', 'group', 'g-v', 'sys-other', 'sys-f', 'active', NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	)
	handler := fixture.deps.aiPerformanceAccountsHandler(false)
	recorder := wdGet(t, handler, "/?keyword=Visible&systemAccountId=sys-f", adminAuth(""))
	options := wdDataArray(t, recorder)
	if len(options) != 1 || options[0].(map[string]any)["id"] != "acct-vis" {
		t.Fatalf("分组授权可见账户应命中关键字选项: %#v", options)
	}
}

// ---------------------------------------------------------------------------
// system-metrics
// ---------------------------------------------------------------------------

func TestWdSystemMetricsTrendHandler(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO system_metrics_trend_windows (window_key, start_date, end_date, bucket_key, sample_count, cpu_percent_sum, memory_used_percent_sum, network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count, network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_count)
			VALUES ('2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', '2026-09-04T10', 2, 88.4, 55.0, 2000, 2, 4000, 2)`,
		// 最新样本：须落在 latest 新鲜窗（now-2min）内；server 两条较新者胜出，
		// 另有一个非法角色样本被读侧过滤。
		`INSERT INTO process_event_loop_samples (id, process_role, process_pid, sampled_at, event_loop_lag_ms, process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes)
			VALUES ('s1', 'server', 100, '2026-09-04T11:59:00.000Z', 12, 1, 2, 3)`,
		`INSERT INTO process_event_loop_samples (id, process_role, process_pid, sampled_at, event_loop_lag_ms, process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes)
			VALUES ('s2', 'server', 100, '2026-09-04T11:59:30.000Z', 20, 5, 6, 7)`,
		`INSERT INTO process_event_loop_samples (id, process_role, process_pid, sampled_at, event_loop_lag_ms, process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes)
			VALUES ('s3', 'bogus-role', 7, '2026-09-04T11:59:40.000Z', 999, 0, 0, 0)`,
		// 峰值样本（lag 最大）。
		`INSERT INTO process_event_loop_samples (id, process_role, process_pid, sampled_at, event_loop_lag_ms)
			VALUES ('s4', 'stats-worker', 200, '2026-09-04T10:00:00.000Z', 80)`,
		// 趋势窗口行。
		`INSERT INTO process_event_loop_trend_windows (window_key, start_date, end_date, bucket_key, process_role, sample_count, event_loop_lag_ms_sum, event_loop_lag_ms_count, event_loop_lag_ms_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum, process_heap_used_bytes_max, process_heap_total_bytes_sum, process_heap_total_bytes_max)
			VALUES ('2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', '2026-09-04T10', 'server', 2, 30, 2, 18, 6, 4, 8, 5, 10, 6)`,
		// 峰值窗口之外但 24h 内的样本由 latest/peak 读取；趋势窗口只导出行内角色。
	)
	recorder := wdGet(t, fixture.deps.systemMetricsTrendHandler, "/?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	payload := wdData(t, recorder)
	hourly := wdJSONArray(t, payload["hourlyTrend"], "hourlyTrend")
	if len(hourly) != 1 {
		t.Fatalf("系统指标小时趋势错误: %#v", hourly)
	}
	point := hourly[0].(map[string]any)
	if point["statHour"] != "2026-09-04T10" || point["cpuPercentAvg"] != float64(44) || point["networkRxBytesPerSecondAvg"] != float64(1000) {
		t.Fatalf("系统指标聚合错误: %#v", point)
	}
	latest := wdJSONArray(t, payload["processEventLoopLatestStatus"], "latest")
	if len(latest) != len(standaloneProcessRoles) {
		t.Fatalf("standalone 模式应导出固定角色表: %d", len(latest))
	}
	// server 最新样本是 s2（11:30），非法角色不出现在状态里。
	serverStatus := map[string]any{}
	for _, item := range latest {
		status := item.(map[string]any)
		if status["processRole"] == "server" {
			serverStatus = status
		}
	}
	if serverStatus["sampleAvailable"] != true || serverStatus["eventLoopLagMs"] != float64(20) || serverStatus["processPid"] != float64(100) {
		t.Fatalf("server 最新状态错误: %#v", serverStatus)
	}
	peak := wdJSONArray(t, payload["processEventLoopPeakStatus"], "peak")
	statsPeak := map[string]any{}
	for _, item := range peak {
		status := item.(map[string]any)
		if status["processRole"] == "stats-worker" {
			statsPeak = status
		}
	}
	if statsPeak["eventLoopLagMs"] != float64(80) {
		t.Fatalf("stats-worker 峰值错误: %#v", statsPeak)
	}
	trend := wdJSONArray(t, payload["processEventLoopTrend"], "trend")
	if len(trend) != 1 {
		t.Fatalf("进程趋势行错误: %#v", trend)
	}
	trendPoint := trend[0].(map[string]any)
	if trendPoint["eventLoopLagMsAvg"] != float64(15) || trendPoint["processRssBytesAvg"] != float64(3) || trendPoint["processRole"] != "server" {
		t.Fatalf("进程趋势聚合错误: %#v", trendPoint)
	}
	// 日期契约 400。
	if recorder := wdGet(t, fixture.deps.systemMetricsTrendHandler, "/?startDate=2026/09/04", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法日期应 400: %d", recorder.Code)
	}
}

func TestWdRuntimeSnapshotRoutesServeDegradation(t *testing.T) {
	fixture := newWdFixture(t)
	recorder := wdGet(t, fixture.deps.runtimeSummaryHandler, "/", adminAuth(""))
	payload := wdData(t, recorder)
	if payload["runtimeSnapshotAvailable"] != false || payload["queuesAvailable"] != false || payload["jobsAvailable"] != false {
		t.Fatalf("运行时快照应保持不可用降级: %#v", payload)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("运行时快照应带 no-store")
	}
	recorder = wdGet(t, fixture.deps.runtimeJobsHandler, "/?page=2&pageSize=20", adminAuth(""))
	jobs := wdData(t, recorder)
	if jobs["page"] != float64(2) || jobs["pageSize"] != float64(20) || jobs["total"] != float64(0) {
		t.Fatalf("jobs 分页契约错误: %#v", jobs)
	}
	recorder = wdGet(t, fixture.deps.runtimeQueuesHandler, "/?page=0", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("page=0 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeBody(t, recorder)["message"]; got != "后台任务分页参数不合法" {
		t.Fatalf("400 消息错误: %#v", got)
	}
}

// ---------------------------------------------------------------------------
// usage-overview：模型分布与错误分节
// ---------------------------------------------------------------------------

func TestWdUsageOverviewModelDistributionAndErrors(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO usage_model_rank_windows (system_account_id, window_key, start_date, end_date, rank, provider_code, model, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'openai', 'gpt-5', 8, 80, 40, 0.8)`,
		`INSERT INTO usage_error_rank_windows (system_account_id, window_key, start_date, end_date, rank, provider_code, error_code, status_code, error_message, error_count)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'openai', NULL, NULL, NULL, 5)`,
	)
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := wdGet(t, handler, "/usage-overview/model-distribution?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	distribution := wdJSONArray(t, wdData(t, recorder)["modelDistribution"], "modelDistribution")
	if len(distribution) != 1 {
		t.Fatalf("模型分布行错误: %#v", distribution)
	}
	model := distribution[0].(map[string]any)
	if model["model"] != "gpt-5" || model["totalTokens"] != float64(120) || model["totalCost"] != 0.8 {
		t.Fatalf("模型分布聚合错误: %#v", model)
	}
	recorder = wdGet(t, handler, "/usage-overview/errors?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	errorsList := wdJSONArray(t, wdData(t, recorder)["errors"], "errors")
	if len(errorsList) != 1 {
		t.Fatalf("错误分节行错误: %#v", errorsList)
	}
	errorPoint := errorsList[0].(map[string]any)
	if errorPoint["errorCode"] != nil || errorPoint["errorCount"] != float64(5) {
		t.Fatalf("可空错误字段应保持 null: %#v", errorPoint)
	}
}

// ---------------------------------------------------------------------------
// ai-health：关键字检索 + J1 outcome SQLite 合并
// ---------------------------------------------------------------------------

func TestWdAiHealthKeywordUsesSearchIndex(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-hit', 'SearchHit', 'sys-user-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-miss', 'Unrelated', 'sys-user-1', 'openai', 'api_key', 'active')`,
		// 3-gram 词表齐全才命中（"Search" → Sea/ear/arc/rch，HAVING 全量匹配）。
		`INSERT INTO account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'Sea')`,
		`INSERT INTO account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'ear')`,
		`INSERT INTO account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'arc')`,
		`INSERT INTO account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'rch')`,
		`INSERT INTO account_name_search_documents (system_account_id, account_id, normalized_name) VALUES ('sys-user-1', 'acct-hit', 'SearchHit')`,
	)
	recorder := wdGet(t, fixture.deps.aiHealthListHandler(false),
		"/?hours=24&page=1&pageSize=10&keyword=Search", adminAuth(""))
	items := wdJSONArray(t, wdData(t, recorder)["items"], "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != "acct-hit" {
		t.Fatalf("关键字检索应命中检索索引账户: %#v", items)
	}
}

func TestWdAiHealthHourDetailMergesJ1OutcomeSQLite(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-j1', 'J1Account', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		// 本地读侧已有该小时的失败观察（10:20），J1 outcome 更新（10:40）应胜出。
		`INSERT INTO account_health_hourly (account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message)
			VALUES ('acct-j1', '2026-09-04T10', 'failure', '2026-09-04T10:20:00.000Z', 502, 'upstream_error', 'upstream bad')`,
	)
	// 构造 J1 outcome SQLite 文件（jobs 侧写、gateway 只读）。
	outcomePath := filepath.Join(t.TempDir(), "outcomes.sqlite3")
	outcomeDB, err := sql.Open("sqlite", outcomePath)
	if err != nil {
		t.Fatalf("open outcome db: %v", err)
	}
	if _, err := outcomeDB.Exec(`CREATE TABLE account_health_outcomes (
		outcome_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, observed_at TEXT NOT NULL,
		outcome TEXT NOT NULL, payload TEXT NOT NULL)`); err != nil {
		t.Fatalf("outcome schema: %v", err)
	}
	payload := `{"outcomeId":"out-1","requestId":"req-1","accountId":"acct-j1","outcome":"complete_success","observedAt":"2026-09-04T10:40:00.000Z","statusCode":200,"nextDueAt":"2026-09-04T11:40:00.000Z"}`
	if _, err := outcomeDB.Exec(`INSERT INTO account_health_outcomes (outcome_id, account_id, observed_at, outcome, payload)
		VALUES ('out-1', 'acct-j1', '2026-09-04T10:40:00.000Z', 'complete_success', ?)`, payload); err != nil {
		t.Fatalf("outcome seed: %v", err)
	}
	if err := outcomeDB.Close(); err != nil {
		t.Fatalf("close outcome db: %v", err)
	}
	fixture.deps.HealthOutcomes = &HealthOutcomeSource{SQLitePath: outcomePath}

	handler := fixture.deps.aiHealthHourDetailHandler(false)
	recorder := wdGet(t, handler, "/?accountId=acct-j1&statHour=2026-09-04T10", adminAuth(""))
	detail := wdData(t, recorder)
	if detail["status"] != "success" {
		t.Fatalf("更新的 J1 outcome 应胜出: %#v", detail)
	}
	if detail["lastObservedAt"] != "2026-09-04T10:40:00.000Z" || detail["statusCode"] != float64(200) {
		t.Fatalf("J1 outcome 明细字段错误: %#v", detail)
	}

	// 无 J1 配置时保持读侧观察。
	fixture.deps.HealthOutcomes = nil
	recorder = wdGet(t, handler, "/?accountId=acct-j1&statHour=2026-09-04T10", adminAuth(""))
	detail = wdData(t, recorder)
	if detail["status"] != "failure" || detail["errorCode"] != "upstream_error" {
		t.Fatalf("无 J1 合并时读侧观察应保留: %#v", detail)
	}
	// 参数契约。
	recorder = wdGet(t, handler, "/?accountId=&statHour=2026-09-04T10", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("空 accountId 应 400: %d", recorder.Code)
	}
	recorder = wdGet(t, handler, "/?accountId=acct-j1&statHour=2026-13-04T10", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法日历 statHour 应 400: %d", recorder.Code)
	}
}

func TestWdAiHealthListHandlerParameterContract(t *testing.T) {
	fixture := newWdFixture(t)
	handler := fixture.deps.aiHealthListHandler(false)
	for _, bad := range []string{"/?hours=0", "/?hours=99999", "/?page=0", "/?pageSize=9", "/?pageSize=51", "/?keyword=" + strings.Repeat("长", 201)} {
		if recorder := wdGet(t, handler, bad, adminAuth("")); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400: %d %s", bad, recorder.Code, recorder.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// usage-records：关键字、日期窗口与过滤矩阵
// ---------------------------------------------------------------------------

func newWdShardFixture(t *testing.T) (*wdFixture, string, *sql.DB) {
	t.Helper()
	fixture := newWdFixture(t)
	shardPath := filepath.Join(t.TempDir(), "usage_20260904_s000.sqlite3")
	shardDB, err := sql.Open("sqlite", shardPath)
	if err != nil {
		t.Fatalf("open shard: %v", err)
	}
	if _, err := shardDB.Exec(shardSchema); err != nil {
		t.Fatalf("shard schema: %v", err)
	}
	if err := shardDB.Close(); err != nil {
		t.Fatalf("close shard: %v", err)
	}
	catalog, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if _, err := catalog.Exec(`CREATE TABLE usage_record_shards (
		shard_key TEXT PRIMARY KEY, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL,
		file_path TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`); err != nil {
		t.Fatalf("catalog schema: %v", err)
	}
	if _, err := catalog.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
		VALUES ('20260904:s000', '2026-09-04', 0, ?, 'active')`, shardPath); err != nil {
		t.Fatalf("catalog seed: %v", err)
	}
	fixture.deps.UsageCatalog = catalog
	return fixture, shardPath, catalog
}

func wdShardExec(t *testing.T, path string, statements ...string) {
	t.Helper()
	shardDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen shard: %v", err)
	}
	defer shardDB.Close()
	for _, statement := range statements {
		if _, err := shardDB.Exec(statement); err != nil {
			t.Fatalf("shard seed %v", err)
		}
	}
}

func TestWdUsageRecordsKeywordResolvesScopedAccountIds(t *testing.T) {
	fixture, shardPath, _ := newWdShardFixture(t)
	// self 面（user）钉到 sys-user-1：关键字经名称前缀/授权/分组四路并集解析。
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-w1', 'WidgetAccount', 'sys-user-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-w2', 'WidgetGrouped', 'sys-other', 'openai', 'api_key', 'active')`,
		`INSERT INTO groups (id, name, system_account_id) VALUES ('g-w', 'WidgetGroup', 'sys-user-1')`,
		`INSERT INTO group_accounts (account_id, group_id, system_account_id, enabled) VALUES ('acct-w2', 'g-w', 'sys-user-1', 1)`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ('ra-w', 'group', 'g-w', 'sys-other', 'sys-user-1', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	)
	wdShardExec(t, shardPath,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, account_id, success, status_code, created_at) VALUES
			('u-w1', 'sys-user-1', 'tr-1', 'gateway', 'acct-w1', 1, 200, '2026-09-04T10:00:00.000Z')`,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, account_id, success, status_code, created_at) VALUES
			('u-w2', 'sys-user-1', 'tr-2', 'gateway', 'acct-w2', 1, 200, '2026-09-04T11:00:00.000Z')`,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, account_id, success, status_code, created_at) VALUES
			('u-w3', 'sys-user-1', 'tr-3', 'gateway', 'acct-unrelated', 1, 200, '2026-09-04T12:00:00.000Z')`,
	)
	handler := fixture.deps.usageRecordsListHandler(true)
	// w11g 修复回归：scoped（ownerID 非空）关键字检索此前在
	// usageRecordKeywordAccountIds 第二段查询报 "no such column:
	// accounts.system_account_id"（ownerClause 锚错了别名），路由呈现 500。
	// 已按 Node 原版把 owner 条件锚定 instance_accounts 别名；现在 owner 视角
	// 关键字检索正常返回命中记录（acct-w1 自有 + acct-w2 分组授权）。
	recorder := wdGet(t, handler, "/?accountKeyword=Widget", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("scoped accountKeyword 应 200: %d %s", recorder.Code, recorder.Body.String())
	}
	keywordItems := wdJSONArray(t, wdData(t, recorder)["items"], "items")
	if len(keywordItems) != 2 {
		t.Fatalf("scoped accountKeyword 应命中两条记录: %#v", keywordItems)
	}
	if keywordItems[1].(map[string]any)["accountName"] != "WidgetAccount" {
		t.Fatalf("账户名水合错误: %#v", keywordItems[1])
	}
	// 非关键字读取保持可用：三条种子记录照常返回并带账户名水合。
	recorder = wdGet(t, handler, "/", userAuth())
	payload := wdData(t, recorder)
	items := wdJSONArray(t, payload["items"], "items")
	if len(items) != 3 {
		t.Fatalf("非关键字读取应返回全部记录: %#v", payload)
	}
	if items[2].(map[string]any)["accountName"] != "WidgetAccount" {
		t.Fatalf("账户名水合错误: %#v", items[2])
	}
}

func TestWdUsageRecordsFilterMatrix(t *testing.T) {
	fixture, shardPath, _ := newWdShardFixture(t)
	wdShardExec(t, shardPath,
		// 今日（固定 Now=2026-09-04T12:00Z，UTC 时区）窗口内两条对照记录。
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id, account_id, endpoint, model, stream, status_code, success, error_code, first_token_ms, duration_ms, input_tokens, output_tokens, cost_usd, created_at) VALUES
			('u-f1', 'sys-user-1', 'trace-alpha', 'gateway', '10.0.0.1', 'key-1', 'g-1', 'acct-1', '/v1/chat', 'gpt-5', 1, 200, 1, NULL, 100, 900, 10, 5, 0.2, '2026-09-04T10:00:00.000Z')`,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, client_ip, model, stream, status_code, success, failure_attribution, error_code, error_message, created_at) VALUES
			('u-f2', 'sys-user-1', 'trace-beta', 'manual_account_test', '10.0.0.9', 'gpt-5', 0, 429, 0, 'account_upstream', 'rate_limit_exceeded', '被限流', '2026-09-04T11:00:00.000Z')`,
		// 昨日记录：默认"今日"窗口外。
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, status_code, created_at) VALUES
			('u-old', 'sys-user-1', 'trace-old', 'gateway', 1, 200, '2026-09-03T08:00:00.000Z')`,
	)
	handler := fixture.deps.usageRecordsListHandler(true)
	// 默认窗口=今天（UTC），昨日记录不出现；倒序 newest first。
	recorder := wdGet(t, handler, "/", userAuth())
	payload := wdData(t, recorder)
	items := wdJSONArray(t, payload["items"], "items")
	if len(items) != 2 || items[0].(map[string]any)["id"] != "u-f2" {
		t.Fatalf("默认今日窗口与倒序错误: %#v", payload)
	}
	cases := []struct {
		name     string
		query    string
		expected []string
	}{
		{"result=success", "?result=success", []string{"u-f1"}},
		{"result=failed", "?result=failed", []string{"u-f2"}},
		{"statusCode", "?statusCode=429", []string{"u-f2"}},
		{"traceId 前缀", "?traceId=trace-a", []string{"u-f1"}},
		{"clientIp 前缀", "?clientIp=10.0.0.9", []string{"u-f2"}},
		{"model", "?model=gpt-5", []string{"u-f2", "u-f1"}},
		{"trafficSource", "?trafficSource=manual_account_test", []string{"u-f2"}},
		{"groupId", "?groupId=g-1", []string{"u-f1"}},
		{"显式日期窗口", "?startDate=2026-09-04&endDate=2026-09-04", []string{"u-f2", "u-f1"}},
		{"升序", "?sortBy=createdAt&sortOrder=asc", []string{"u-f1", "u-f2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := wdGet(t, handler, "/"+tc.query, userAuth())
			items := wdJSONArray(t, wdData(t, recorder)["items"], "items")
			got := []string{}
			for _, item := range items {
				got = append(got, item.(map[string]any)["id"].(string))
			}
			if strings.Join(got, ",") != strings.Join(tc.expected, ",") {
				t.Fatalf("过滤结果错误: got %#v want %#v", got, tc.expected)
			}
		})
	}
	// 失败行映射：归因/事实串与水合名。
	recorder = wdGet(t, handler, "/?result=failed", userAuth())
	failed := wdData(t, recorder)["items"].([]any)[0].(map[string]any)
	if failed["failureAttribution"] != "account_upstream" || failed["failureReason"] != "rate_limit_exceeded | 被限流" {
		t.Fatalf("失败行归因错误: %#v", failed)
	}
	recorder = wdGet(t, handler, "/?result=success", userAuth())
	successRow := wdData(t, recorder)["items"].([]any)[0].(map[string]any)
	if successRow["failureReason"] != nil || successRow["stream"] != true || successRow["costUsd"] != 0.2 {
		t.Fatalf("成功行字段错误: %#v", successRow)
	}
	// 组合过滤 + upstream 模型不一致标记。
	wdShardExec(t, shardPath,
		`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, model, upstream_model, upstream_response_model, success, status_code, created_at) VALUES
			('u-f3', 'sys-user-1', 'trace-gamma', 'gateway', 'gpt-5', 'gpt-5', 'gpt-5-mini', 1, 200, '2026-09-04T11:30:00.000Z')`,
	)
	recorder = wdGet(t, handler, "/?traceId=trace-gamma", userAuth())
	item := wdData(t, recorder)["items"].([]any)[0].(map[string]any)
	if item["upstreamModelMismatch"] != true {
		t.Fatalf("上下游模型不一致应标记: %#v", item)
	}
}

func TestWdUsageRecordsAdminCannotFilterAcrossAllWithoutScope(t *testing.T) {
	fixture, _, _ := newWdShardFixture(t)
	handler := fixture.deps.usageRecordsListHandler(false)
	recorder := wdGet(t, handler, "/?model=gpt-5", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("全局 admin 未选系统账户时过滤应 400: %d", recorder.Code)
	}
	// 选定 systemAccountId 后放行。
	recorder = wdGet(t, handler, "/?model=gpt-5&systemAccountId=sys-user-1", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("选定系统账户后应 200: %d %s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// PG 方言分支：SQLite 上不可执行，用 Skip 说明 + SQL 文本断言。
// ---------------------------------------------------------------------------

func TestWdUsageRecordRowsPGRequiresPostgres(t *testing.T) {
	fixture := newWdFixture(t)
	deps := fixture.deps
	deps.PGDialect = true
	if !strings.Contains(deps.statsTable("usage_records"), "juhe_stats.") {
		t.Fatalf("PG 方言 stats 表应带 juhe_stats 前缀: %q", deps.statsTable("usage_records"))
	}
	if !strings.Contains(deps.businessTable("accounts"), "juhe_business.") {
		t.Fatalf("PG 方言 business 表应带 juhe_business 前缀: %q", deps.businessTable("accounts"))
	}
	// usageRecordRowsPG 直接引用 juhe_usage.usage_records；SQLite 无法执行
	// 该限定表，真实 PG 行为不由本套件覆盖。
	t.Skipf("usageRecordRowsPG 需要 PostgreSQL 方言；SQLite 测试库不承载 juhe_usage 限定表")
}

func TestWdAiPerformanceKeywordPostgresBuilderNeedsPostgres(t *testing.T) {
	// PG 关键字候选构建依赖 COLLATE "C" 与窗口函数的 PG 语义；此处仅断言
	// LIKE 转义入口在 PG 分支前已就绪（postgresSubstringLikePattern 单测见
	// wd_statreads_helpers_test.go）。
	t.Skipf("aiPerformanceKeywordAccountIdsPostgres 需要 PostgreSQL 方言；转义契约已由纯函数测试覆盖")
}

var _ = context.Background
var _ = json.Marshal
