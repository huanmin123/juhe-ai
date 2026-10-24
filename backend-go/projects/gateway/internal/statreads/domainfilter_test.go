package statreads

// AI 账户能力域拆分 M3 配套适配（计划 §M3 配套适配）：统计读面可选 `domain`
// 筛选的行为级测试。默认（不带参 / "all"）与现状逐项一致；`domain` 四枚举
// 在服务端收敛（ai-health 列表、ai-performance 账户水合、account-usage 列表
// 与候选）；非法值 400。

import (
	"net/http"
	"testing"
)

// newDomainFixture 直接使用 PG 夹具 accounts 表的生产 capability_domain 列
// （maintenance business schema 同形态），并预置系统账户。
func newDomainFixture(t *testing.T) *wdFixture {
	t.Helper()
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
	)
	return fixture
}

// seedDomainAccount 插入一个指定域的账户。
func seedDomainAccount(t *testing.T, fixture *wdFixture, id, name, domain string) {
	t.Helper()
	fixture.exec(t, `INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status, capability_domain) VALUES ('`+id+`', '`+name+`', 'sys-owner-1', 'openai', 'api_key', 'active', '`+domain+`')`)
}

func TestOptionalCapabilityDomainFilter(t *testing.T) {
	if value, ok := optionalCapabilityDomainFilter(""); !ok || value != "" {
		t.Fatalf("空值应归一为不过滤：%v %v", value, ok)
	}
	if value, ok := optionalCapabilityDomainFilter("all"); !ok || value != "" {
		t.Fatalf("all 应归一为不过滤：%v %v", value, ok)
	}
	for _, domain := range []string{"chat", "video", "audio", "image"} {
		if value, ok := optionalCapabilityDomainFilter(domain); !ok || value != domain {
			t.Fatalf("四枚举应放行：%s → %v %v", domain, value, ok)
		}
	}
	if _, ok := optionalCapabilityDomainFilter("bogus"); ok {
		t.Fatalf("非法值应拒绝")
	}
}

// ---------------------------------------------------------------------------
// ai-health：列表按域收敛。
// ---------------------------------------------------------------------------

func TestWdAiHealthDomainFilter(t *testing.T) {
	fixture := newDomainFixture(t)
	seedDomainAccount(t, fixture, "acct-chat", "ChatAccount", "chat")
	seedDomainAccount(t, fixture, "acct-video", "VideoAccount", "video")
	fixture.exec(t,
		`INSERT INTO juhe_stats.account_health_hourly (account_id, stat_hour, status, last_observed_at)
			VALUES ('acct-chat', '2026-09-04T10', 'success', '2026-09-04T10:20:00.000Z'),
			       ('acct-video', '2026-09-04T10', 'failure', '2026-09-04T10:20:00.000Z')`,
	)
	handler := fixture.deps.aiHealthListHandler(false)

	// 默认（不带参）= 现状零变化：两行都在。
	items := wdJSONArray(t, wdData(t, wdGet(t, handler, "/?hours=24&page=1&pageSize=10", adminAuth("")))["items"], "items")
	if len(items) != 2 {
		t.Fatalf("默认应返回全部账户：%#v", items)
	}
	// domain=video 只返回视频域。
	items = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?hours=24&page=1&pageSize=10&domain=video", adminAuth("")))["items"], "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("video 域应只含视频账户：%#v", items)
	}
	// domain=all 等价默认。
	items = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?hours=24&page=1&pageSize=10&domain=all", adminAuth("")))["items"], "items")
	if len(items) != 2 {
		t.Fatalf("domain=all 应等价不过滤：%#v", items)
	}
	// 非法值 400。
	if recorder := wdGet(t, handler, "/?hours=24&domain=bogus", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法域应 400：%d", recorder.Code)
	}
}

// ---------------------------------------------------------------------------
// ai-performance：三端点共用的账户水合按域收敛（base 默认账户 + options 候选）。
// ---------------------------------------------------------------------------

func TestWdAiPerformanceDomainFilter(t *testing.T) {
	fixture := newDomainFixture(t)
	seedDomainAccount(t, fixture, "acct-chat", "ChatAccount", "chat")
	seedDomainAccount(t, fixture, "acct-video", "VideoAccount", "video")
	fixture.exec(t,
		// last7d 快照：聊天账户请求量更高，默认账户集含两账户。
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 1, 'acct-chat', 90)`,
		`INSERT INTO juhe_stats.usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value)
			VALUES ('global', 'account', 'last7d', 'request_count', '2026-09-04T00:00:00.000Z', 2, 'acct-video', 40)`,
		`INSERT INTO juhe_stats.usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count)
			VALUES ('global', 'account', 'acct-chat', '2026-09-04T10', 5),
			       ('global', 'account', 'acct-video', '2026-09-04T10', 2)`,
	)
	// base handler 挂在 deps 上（aiPerformanceBaseHandler）。
	handler := fixture.deps.aiPerformanceBaseHandler(false)

	// 默认（不带参）= 现状零变化：默认账户集含两账户。
	accounts := wdJSONArray(t, wdData(t, wdGet(t, handler, "/?startDate=2026-09-04&endDate=2026-09-04", adminAuth("")))["accounts"], "accounts")
	if len(accounts) != 2 {
		t.Fatalf("默认应返回快照前二账户：%#v", accounts)
	}
	// domain=video 收敛到视频账户。
	accounts = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?startDate=2026-09-04&endDate=2026-09-04&domain=video", adminAuth("")))["accounts"], "accounts")
	if len(accounts) != 1 || accounts[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("video 域应只含水合视频账户：%#v", accounts)
	}
	// 非法值 400。
	if recorder := wdGet(t, handler, "/?startDate=2026-09-04&endDate=2026-09-04&domain=bogus", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法域应 400：%d", recorder.Code)
	}

	// options 候选同口径。
	options := fixture.deps.aiPerformanceAccountsHandler(false)
	found := wdDataArray(t, wdGet(t, options, "/?domain=video", adminAuth("")))
	if len(found) != 1 || found[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("options video 域应只含视频账户：%#v", found)
	}
}

// ---------------------------------------------------------------------------
// account-usage：列表主查询与显式选中账户按域收敛；关键词与域 AND 交集。
// ---------------------------------------------------------------------------

func TestWdAccountUsageDomainFilter(t *testing.T) {
	fixture := newDomainFixture(t)
	seedDomainAccount(t, fixture, "acct-chat", "ChatAccount", "chat")
	seedDomainAccount(t, fixture, "acct-video", "VideoAccount", "video")
	fixture.exec(t,
		`INSERT INTO juhe_stats.usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', 'account', 'acct-chat', '2026-09-04', 3, 30, 20, 0.3),
			       ('global', 'account', 'acct-video', '2026-09-04', 7, 70, 40, 0.7)`,
	)
	handler := fixture.deps.accountUsageHandler(false)

	// 默认（不带参）= 现状零变化：两行都在（video 请求量高排前）。
	rows := wdJSONArray(t, wdData(t, wdGet(t, handler, "/?pageSize=5", adminAuth("")))["rows"], "rows")
	if len(rows) != 2 {
		t.Fatalf("默认应返回两行：%#v", rows)
	}
	// domain=chat 只返回对话账户。
	rows = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?pageSize=5&domain=chat", adminAuth("")))["rows"], "rows")
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "acct-chat" {
		t.Fatalf("chat 域应只含对话账户：%#v", rows)
	}
	// 关键词 ∩ 域（AND）：关键词命中两账户（名称共同前缀），域收敛后仅剩 video。
	rows = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?pageSize=5&keyword=Account&domain=video", adminAuth("")))["rows"], "rows")
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("关键词与域应取交集：%#v", rows)
	}
	// 显式选中账户按域收敛：域=video 下显式选中 chat 账户 → 选中项被剔除，
	// 页面只剩域内的 acct-video（主查询 + 选中交集的并集）。
	rows = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?pageSize=5&domain=video&accountIds=acct-chat", adminAuth("")))["rows"], "rows")
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("选中账户应按域收敛：%#v", rows)
	}
	// 域无账户 → 空结果不报错。
	rows = wdJSONArray(t, wdData(t, wdGet(t, handler, "/?pageSize=5&domain=image", adminAuth("")))["rows"], "rows")
	if len(rows) != 0 {
		t.Fatalf("无账户的域应返回空列表：%#v", rows)
	}
	// 非法值 400。
	if recorder := wdGet(t, handler, "/?domain=bogus", adminAuth("")); recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法域应 400：%d", recorder.Code)
	}

	// options 候选同口径。
	options := wdDataArray(t, wdGet(t, fixture.deps.accountUsageOptionsHandler(false), "/?domain=video", adminAuth("")))
	if len(options) != 1 || options[0].(map[string]any)["id"] != "acct-video" {
		t.Fatalf("options video 域应只含视频账户：%#v", options)
	}
}
