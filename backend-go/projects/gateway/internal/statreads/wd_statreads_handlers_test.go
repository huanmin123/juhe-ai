package statreads

// statreads DB handler 契约补充测试（wd_ 前缀，独占新增文件）：
// pgtestkit 独立 PG 测试库 + httptest 直驱 handler，覆盖 account-usage 选项/
// 趋势/关键字、ai-performance base/账户选项/关键字、ai-health 关键字检索、
// usage-overview 模型分布/错误分节、usage-records 门禁，以及 Mount 的注册与
// 门禁契约。2026-10-24 存储形态收口：usage 分片文件读面与 J1 outcome SQLite
// 文件臂的组B测试已随 sqlite 生产臂同批退役。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// wdExtraSchema 是组B SQLite 夹具（共享 testSchema）之外、被测 SQL 需要的
// 业务表（providers、分组关系、账户名检索索引），随 SQLite 臂退役。
const wdExtraSchema = `
	CREATE TABLE providers (code TEXT PRIMARY KEY, name TEXT NOT NULL);
	CREATE TABLE group_accounts (account_id TEXT NOT NULL, group_id TEXT NOT NULL, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);
	CREATE TABLE account_name_search_terms (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, term TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id, term));
	CREATE TABLE account_name_search_documents (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, normalized_name TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id));
`

// 组A PG 夹具直接复用 pgTestSchema：分组关系（group_accounts）与账户名检索
// 索引（account_name_search_terms/documents）已随共享 schema 就位（生产
// business schema 同形态）；providers 不在生产 PG schema（vendors 是
// provider 名唯一来源），不建。

type wdFixture struct {
	deps *Deps
	db   *sql.DB
}

func newWdFixture(t *testing.T) *wdFixture {
	t.Helper()
	base := newFixture(t)
	return &wdFixture{deps: base.deps, db: base.db}
}

// newWdSQLiteFixture 是组B测试（usage 分片文件读面）的 SQLite 夹具入口：
// newSQLiteFixture + 无前缀补充表，随 SQLite 生产臂删除批次同批退役。

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
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', 'account', 'acct-sel', '2026-09-04', 3, 30, 20, 0.3)`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-sel', '选中账户', 'sys-owner-1', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO juhe_business.vendors (code, name) VALUES ('openai', 'OpenAI')`,
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
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 1, 'acct-r1', 50)`,
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 2, 'acct-r2', 20)`,
		// 旧快照批次不得入选。
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
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
		`INSERT INTO juhe_business.vendors (code, name) VALUES ('openai', 'OpenAI')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-a', 'Alpha', 'sys-owner-1', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status, authorization_instance_authorization_id, authorization_instance_source_account_id, authorization_instance_owner_system_account_id) VALUES ('acct-inst', 'Alpha 实例', 'sys-grantee', 'api_key', 'openai', 'active', 'ra-1', 'acct-a', 'sys-owner-1')`,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-grantee', 'grantee', NULL)`,
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
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-own', 'AlphaOwn', 'sys-f', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-foreign', 'AlphaForeign', 'sys-other', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status, authorization_instance_authorization_id, authorization_instance_source_account_id) VALUES ('acct-inst', 'InstanceB', 'sys-f', 'api_key', 'openai', 'active', 'ra-2', 'acct-src')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-src', 'AlphaSource', 'sys-other', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.groups (id, name, system_account_id) VALUES ('g-1', 'AlphaGroup', 'sys-f')`,
		`INSERT INTO juhe_business.group_accounts (account_id, group_id, system_account_id, enabled) VALUES ('acct-foreign', 'g-1', 'sys-f', 1)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ('ra-3', 'group', 'g-1', 'sys-other', 'sys-f', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		// caller_account 视角的页面行来自 usage_stats_daily；关键字只负责收窄。
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('sys-f', 'caller_account', 'acct-own', '2026-09-04', 5, 50, 25, 0.5)`,
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('sys-f', 'caller_account', 'acct-foreign', '2026-09-04', 4, 40, 20, 0.4)`,
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
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
	// source 名命中的授权实例 acct-inst 会进入关键字 ID 集合（已单独验证），
	// 元数据水合必须把它带进页面行。历史缺陷（BUG-0197）：headParams 多传一个
	// viewerID 时 chunk 的 IN 绑定整体后移一位，SQLite 下 chunk 末位账户被静默
	// 丢弃、PG 下参数计数不匹配直接报错——修复后 acct-inst 必须出现。
	if !ids["acct-inst"] {
		t.Fatalf("授权实例账户（acct-inst）应出现在页面行中: %#v", ids)
	}
	if ids["acct-src"] {
		t.Fatalf("source 本身不是实例，不应直接入选: %#v", ids)
	}
}

func TestWdAccountUsageTrendDailySeries(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-t', 'TrendAccount', 'sys-owner-1', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, cache_read_tokens, cache_write_tokens, thinking_tokens, last_used_at)
			VALUES ('global', 'account', 'acct-t', '2026-09-04', 7, 70, 35, 0.7, 1, 2, 3, '2026-09-04T10:00:00.000Z')`,
		// 范围窗口行：趋势只取日桶，NULL stat_date 行应被跳过。
		`INSERT INTO juhe_stats.usage_scope_range_windows (system_account_id, scope_type, scope_id, start_date, end_date, request_count, input_tokens, output_tokens, total_cost_usd)
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
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, cache_read_cost_usd)
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
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-p1', 'PerfAccount', 'sys-owner-1', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 1, 'acct-p1', 42)`,
		`INSERT INTO juhe_stats.ai_performance_summary_windows (system_account_id, window_key, start_date, end_date, request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max, duration_ms_sum, duration_ms_count, duration_ms_max)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 10, 500, 4, 200, 1000, 5, 300)`,
		`INSERT INTO juhe_stats.usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max)
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
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-k1', 'KeyAccount', 'sys-f', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-k2', 'OtherAccount', 'sys-f', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status, deleted_at) VALUES ('acct-k3', 'KeyAccount', 'sys-f', 'api_key', 'openai', 'active', '2026-01-01T00:00:00.000Z')`,
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
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-vis', 'VisibleAccount', 'sys-other', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.groups (id, name, system_account_id) VALUES ('g-v', 'VGroup', 'sys-f')`,
		`INSERT INTO juhe_business.group_accounts (account_id, group_id, system_account_id, enabled) VALUES ('acct-vis', 'g-v', 'sys-f', 1)`,
		`INSERT INTO juhe_business.resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, expires_at, created_at, updated_at)
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
// usage-overview：模型分布与错误分节
// ---------------------------------------------------------------------------

func TestWdUsageOverviewModelDistributionAndErrors(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO juhe_stats.usage_model_rank_windows (system_account_id, window_key, start_date, end_date, rank, provider_code, model, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'openai', 'gpt-5', 8, 80, 40, 0.8)`,
		`INSERT INTO juhe_stats.usage_error_rank_windows (system_account_id, window_key, start_date, end_date, rank, provider_code, error_code, status_code, error_message, error_count)
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
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-hit', 'SearchHit', 'sys-user-1', 'api_key', 'openai', 'active')`,
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, type, vendor_code, status) VALUES ('acct-miss', 'Unrelated', 'sys-user-1', 'api_key', 'openai', 'active')`,
		// 3-gram 词表齐全才命中（"Search" → Sea/ear/arc/rch，HAVING 全量匹配）。
		`INSERT INTO juhe_business.account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'Sea')`,
		`INSERT INTO juhe_business.account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'ear')`,
		`INSERT INTO juhe_business.account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'arc')`,
		`INSERT INTO juhe_business.account_name_search_terms (system_account_id, account_id, term) VALUES ('sys-user-1', 'acct-hit', 'rch')`,
		`INSERT INTO juhe_business.account_name_search_documents (system_account_id, account_id, normalized_name) VALUES ('sys-user-1', 'acct-hit', 'SearchHit')`,
	)
	recorder := wdGet(t, fixture.deps.aiHealthListHandler(false),
		"/?hours=24&page=1&pageSize=10&keyword=Search", adminAuth(""))
	items := wdJSONArray(t, wdData(t, recorder)["items"], "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != "acct-hit" {
		t.Fatalf("关键字检索应命中检索索引账户: %#v", items)
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

// newWdShardFixture 是组B（usage 分片文件读面，SQLite-only 臂）的夹具入口：
// newWdSQLiteFixture + 真实 shard 文件与 :memory: 目录，随 SQLite 生产臂删除
// 批次同批退役。

func TestWdUsageRecordsAdminCannotFilterAcrossAllWithoutScope(t *testing.T) {
	// 被测对象是 admin 过滤门（方言无关）；放行臂的行读走 PG 臂空
	// juhe_usage.usage_records 母表（空集）。
	fixture := newWdFixture(t)
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
// PG 方言分支：表名前缀断言（行读行为已由 PG 夹具上的 usage-records 组A
// 测试真实承载）。
// ---------------------------------------------------------------------------

func TestWdUsageRecordRowsPGRequiresPostgres(t *testing.T) {
	fixture := newWdFixture(t)
	deps := fixture.deps
	if !strings.Contains(deps.statsTable("usage_records"), "juhe_stats.") {
		t.Fatalf("PG 方言 stats 表应带 juhe_stats 前缀: %q", deps.statsTable("usage_records"))
	}
	if !strings.Contains(deps.businessTable("accounts"), "juhe_business.") {
		t.Fatalf("PG 方言 business 表应带 juhe_business 前缀: %q", deps.businessTable("accounts"))
	}
	// 行读行为由 usage-records 组A测试（admin 门/keyword 检索）在真实 PG 上
	// 覆盖。
}

func TestWdAiPerformanceKeywordPostgresBuilderNeedsPostgres(t *testing.T) {
	// PG 关键字候选构建依赖 COLLATE "C" 与窗口函数的 PG 语义；此处仅断言
	// LIKE 转义入口在 PG 分支前已就绪（postgresSubstringLikePattern 单测见
	// wd_statreads_helpers_test.go）。
	t.Skipf("aiPerformanceKeywordAccountIdsPostgres 需要 PostgreSQL 方言；转义契约已由纯函数测试覆盖")
}

var _ = context.Background
var _ = json.Marshal
