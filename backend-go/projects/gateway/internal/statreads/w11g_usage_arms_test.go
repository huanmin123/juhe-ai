package statreads

// w11g 覆盖补充（第二批）：selfOnly handler 分支、分页/候选钳制、
// usage-records 日期窗口与 PG 方言分支、shard 目录细节、错误臂。

import (
	"context"
	"net/http"
	"testing"
)

func requestCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestW11GAccountUsageSelfOnlyAndClampArms(t *testing.T) {
	fixture := newWdFixture(t)
	// my-stats 面：selfOnly 分支 + includeSummary 拒绝。
	recorder := invoke(t, fixture.deps.accountUsageHandler(true), http.MethodGet, "/account-usage?includeSummary=1", userAuth())
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("includeSummary = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageHandler(true), http.MethodGet, "/account-usage", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self usage = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// options：keyword 超长 400、limit 越界 400、selectedIds 截断、selfOnly。
	recorder = invoke(t, fixture.deps.accountUsageOptionsHandler(false), http.MethodGet, "/options?keyword="+repeat("k", 201), adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("long keyword = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageOptionsHandler(false), http.MethodGet, "/options?limit=0", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageOptionsHandler(false), http.MethodGet, "/options?limit=51", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("limit=51 = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageOptionsHandler(true), http.MethodGet, "/options?selectedIds="+repeat("a,", 30)+"last", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self options = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// summary/trend selfOnly 与 trend 账户截断。
	recorder = invoke(t, fixture.deps.accountUsageSummaryHandler(true), http.MethodGet, "/summary", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self summary = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageTrendHandler(true), http.MethodGet, "/trend?accountIds="+repeat("a,", 12)+"last", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self trend = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// page 越界钳制：大 page 被拉回窗口上限。
	recorder = invoke(t, fixture.deps.accountUsageHandler(false), http.MethodGet, "/account-usage?page=99999&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("big page = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func repeat(text string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += text
	}
	return out
}

func TestW11GAccountUsageErrorArms(t *testing.T) {
	fixture := newWdFixture(t)
	// keyword 检索失败：drop accounts 表（instr 检索底表）。
	if _, err := fixture.db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	recorder := invoke(t, fixture.deps.accountUsageHandler(false), http.MethodGet, "/account-usage?keyword=w11g", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("keyword search error = %d", recorder.Code)
	}
	// 元数据水合失败：先 seed 聚合行使 scope_ids 非空，再 drop accounts。
	fixture = newWdFixture(t)
	fixture.exec(t, `INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, total_cost_usd)
		VALUES ('global', 'account', 'w11g-acct-1', '2026-09-04', 5, 0.5)`)
	if _, err := fixture.db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	recorder = invoke(t, fixture.deps.accountUsageHandler(false), http.MethodGet, "/account-usage", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("metadata error = %d", recorder.Code)
	}
	// summary 与 trend 读链失败：drop 统计表。
	fixture = newWdFixture(t)
	if _, err := fixture.db.Exec(`DROP TABLE usage_stats_daily`); err != nil {
		t.Fatal(err)
	}
	recorder = invoke(t, fixture.deps.accountUsageSummaryHandler(false), http.MethodGet, "/summary", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("summary error = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.accountUsageTrendHandler(false), http.MethodGet, "/trend?accountIds=w11g-acct-1", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("trend error = %d", recorder.Code)
	}
	// timezone 读取失败：drop settings。
	fixture = newWdFixture(t)
	if _, err := fixture.db.Exec(`DROP TABLE system_settings`); err != nil {
		t.Fatal(err)
	}
	recorder = invoke(t, fixture.deps.accountUsageSummaryHandler(false), http.MethodGet, "/summary", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("timezone error = %d", recorder.Code)
	}
}

func TestW11GAiHealthSelfOnlyAndErrorArms(t *testing.T) {
	fixture := newWdFixture(t)
	recorder := invoke(t, fixture.deps.aiHealthListHandler(true), http.MethodGet, "/ai-health", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self list = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = invoke(t, fixture.deps.aiHealthHourDetailHandler(true), http.MethodGet, "/ai-health/hour-detail?accountId=w11g-a&statHour=2026-09-04T08", userAuth())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("self detail = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 读链错误：seed 账户让页项非空后 drop health 表。
	fixture.exec(t, `INSERT INTO accounts (id, name, system_account_id, provider_code, type, status) VALUES ('w11g-acct-1', 'w11g 账户', 'sys-admin-1', 'openai', 'api_key', 'active')`)
	if _, err := fixture.db.Exec(`DROP TABLE account_health_hourly`); err != nil {
		t.Fatal(err)
	}
	recorder = invoke(t, fixture.deps.aiHealthListHandler(false), http.MethodGet, "/ai-health", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list error = %d", recorder.Code)
	}
	recorder = invoke(t, fixture.deps.aiHealthHourDetailHandler(false), http.MethodGet, "/ai-health/hour-detail?accountId=w11g-acct-1&statHour=2026-09-04T08", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("detail error = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW11GAiPerformanceArms(t *testing.T) {
	fixture := newWdFixture(t)
	// selfOnly 分支。
	recorder := invoke(t, fixture.deps.aiPerformanceBaseHandler(true), http.MethodGet, "/ai-performance", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self base = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = invoke(t, fixture.deps.aiPerformanceSeriesHandler(true), http.MethodGet, "/ai-performance/series?accountIds=w11g-a&accountIds=w11g-b", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self series = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// accounts 选项：limit 钳制 + keyword 空结果。
	recorder = invoke(t, fixture.deps.aiPerformanceAccountsHandler(false), http.MethodGet, "/ai-performance/accounts?limit=1", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("accounts limit = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = invoke(t, fixture.deps.aiPerformanceAccountsHandler(true), http.MethodGet, "/ai-performance/accounts?keyword=w11g-none", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("accounts keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 读链错误：drop 汇总表。
	fixture = newWdFixture(t)
	if _, err := fixture.db.Exec(`DROP TABLE ai_performance_summary_windows`); err != nil {
		t.Fatal(err)
	}
	recorder = invoke(t, fixture.deps.aiPerformanceBaseHandler(false), http.MethodGet, "/ai-performance", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("base error = %d", recorder.Code)
	}
	// series 日期默认回填（startDate 取 endDate）。
	fixture = newWdFixture(t)
	recorder = invoke(t, fixture.deps.aiPerformanceSeriesHandler(false), http.MethodGet, "/ai-performance/series?endDate=2026-09-02&accountIds=w11g-a&accountIds=w11g-b", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("series end-only = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW11GUsageRecordDateRangeArms(t *testing.T) {
	fixture := newFixture(t)
	// 非法日期：保留原值存在但解析为空 → 返回空窗口（触发未过滤 400 或空集）。
	request := func(target string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		return req
	}
	values := request("/usage-records?startDate=bad&endDate=bad").URL.Query()
	start, end, err := fixture.deps.usageRecordDateRange(request("/x").Context(), values)
	if err != nil || start != "" || end != "" {
		t.Fatalf("invalid dates = %s/%s err=%v", start, end, err)
	}
	// 单边非法：回填对边。
	values = request("/usage-records?startDate=bad&endDate=2026-09-02").URL.Query()
	start, end, err = fixture.deps.usageRecordDateRange(request("/x").Context(), values)
	if err != nil || start == "" || end == "" {
		t.Fatalf("one-sided invalid = %s/%s err=%v", start, end, err)
	}
	values = request("/usage-records?startDate=2026-09-01&endDate=bad").URL.Query()
	start, end, err = fixture.deps.usageRecordDateRange(request("/x").Context(), values)
	if err != nil || start == "" || end == "" {
		t.Fatalf("one-sided invalid end = %s/%s err=%v", start, end, err)
	}
	// 起止交换。
	values = request("/usage-records?startDate=2026-09-04&endDate=2026-09-01").URL.Query()
	start, end, err = fixture.deps.usageRecordDateRange(request("/x").Context(), values)
	if err != nil || start > end {
		t.Fatalf("swapped = %s/%s err=%v", start, end, err)
	}
}

func TestW11GUsageRecordPGDialectArm(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.deps.PGDialect = true
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet, "/usage-records", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("pg dialect rows = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW11GUsageShardLocationArms(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.deps.UsageCatalog = fixture.db
	if _, err := fixture.db.Exec(`CREATE TABLE usage_record_shards (shard_key TEXT NOT NULL, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL, file_path TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	seed := func(rows ...string) {
		if _, err := fixture.db.Exec(`DELETE FROM usage_record_shards`); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if _, err := fixture.db.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status) VALUES (` + row + `)`); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx := requestCtx(t)
	// 只有 start：startKey=endKey 起点；只有 end：endKey 兜底今天；start>end 交换；超窗钳制。
	locations, err := fixture.deps.usageShardLocations(ctx, "2026-09-04T00:00:00.000Z", "")
	if err != nil || len(locations) != 0 {
		t.Fatalf("start-only = %v err=%v", locations, err)
	}
	locations, err = fixture.deps.usageShardLocations(ctx, "", "2026-09-04T00:00:00.000Z")
	if err != nil || len(locations) != 0 {
		t.Fatalf("end-only = %v err=%v", locations, err)
	}
	locations, err = fixture.deps.usageShardLocations(ctx, "2026-10-04T00:00:00.000Z", "2026-01-04T00:00:00.000Z")
	if err != nil {
		t.Fatalf("swapped err=%v", err)
	}
	// 无效行（空 shard_key / 非整数 shard_id）被跳过，有效行保留。
	seed(`'w11g-shard', '2026-09-04', 1, 'w11g-path', 'active'`,
		`'', '2026-09-04', 2, 'w11g-path-2', 'active'`,
		`'w11g-shard-3', '2026-09-04', 2.5, 'w11g-path-3', 'active'`)
	locations, err = fixture.deps.usageShardLocations(ctx, "2026-09-04T00:00:00.000Z", "2026-09-04T23:00:00.000Z")
	if err != nil || len(locations) != 1 || locations[0].ShardKey != "w11g-shard" {
		t.Fatalf("locations=%+v err=%v", locations, err)
	}
	// 目录查询失败。
	if _, err := fixture.db.Exec(`DROP TABLE usage_record_shards`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.deps.usageShardLocations(ctx, "2026-09-04T00:00:00.000Z", "2026-09-04T23:00:00.000Z"); err == nil {
		t.Fatal("catalog error must propagate")
	}
}
