package main

// T93-1（C3-H1）+ T93-4（C3-H8）链级验证（真实 PG fixture + mock 上游）：
// 在选定账户、拿到 vendor 与上游映射模型之后、真正发上游请求之前按真实成本
// 预留；额度不足时 429（api_key_inflight_quota_exceeded）且上游零命中；
// 无额度配置时不预留、请求照常成功。

import (
	"net/http"
	"strings"
	"testing"
)

// t93PriceTheCatalogRow 给 fixture 的 gpt-test 目录行补输出价，使派发期估算
// 可算出完整成本（T93-2 起启用态要求 input+output 齐备；fixture 种子行只有
// 输入价）。
func t93PriceTheCatalogRow(t *testing.T, fixture *chainFixture) {
	t.Helper()
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE models SET output_usd_per_1m = 2.0 WHERE id = 'mdl_gpt-test'`)); err != nil {
		t.Fatalf("update models output price: %v", err)
	}
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE custom_provider_models SET output_usd_per_1m = 2.0 WHERE id = 'cat_1'`)); err != nil {
		t.Fatalf("update custom model output price: %v", err)
	}
}

// t93SeedQuotaStatsTables 补配额读面的最小 stats 表（链夹具只建了账户质量表；
// 在途预占要读 API Key 已结算成本，缺表会按保护策略恒拒绝）。
func t93SeedQuotaStatsTables(t *testing.T, fixture *chainFixture) {
	t.Helper()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_totals (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			total_cost_usd double precision NOT NULL DEFAULT 0, success_cost_usd double precision NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_daily (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			stat_date text NOT NULL, total_cost_usd double precision NOT NULL DEFAULT 0, success_cost_usd double precision NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_weekly (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			stat_week text NOT NULL, total_cost_usd double precision NOT NULL DEFAULT 0, success_cost_usd double precision NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_monthly (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			stat_month text NOT NULL, total_cost_usd double precision NOT NULL DEFAULT 0, success_cost_usd double precision NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_windows (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			window_hours integer NOT NULL DEFAULT 1, total_cost_usd double precision NOT NULL DEFAULT 0)`,
	}
	for _, statement := range statements {
		if _, err := fixture.statsDB.Exec(statement); err != nil {
			t.Fatalf("seed quota stats table: %v", err)
		}
	}
}

func TestT93InflightQuotaDeniesBeforeUpstream(t *testing.T) {
	fixture := newChainFixture(t)
	t93SeedQuotaStatsTables(t, fixture)
	t93PriceTheCatalogRow(t, fixture)
	upstream := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.server.URL)
	// 总额度 0.000001 USD 远小于单次真实估算（输入按 body 字节、输出按目录
	// 输出价）：在途预占必须拒绝，且拒绝发生在任何上游请求之前。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET quota_limits_json = ? WHERE id = 'key_1'`),
		`{"total":{"enabled":true,"limit":0.000001}}`); err != nil {
		t.Fatalf("seed quota limits: %v", err)
	}
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{}, false)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s, want 429", status, body)
	}
	// 对外契约与 preflight 已结算额度拒绝同源（同一文案；API Key 在途拒绝的
	// 审计 error_code 为 api_key_inflight_quota_exceeded，由派发层单测覆盖）。
	if !strings.Contains(body, "额度已用完") || !strings.Contains(body, "rate_limit_exceeded") {
		t.Fatalf("body = %s, want the quota 429 contract", body)
	}
	if upstream.hits != 0 {
		t.Fatalf("upstream hits = %d, want 0（在途额度拒绝必须先于上游请求）", upstream.hits)
	}
}

func TestT93InflightQuotaAllowsWithinLimit(t *testing.T) {
	fixture := newChainFixture(t)
	t93SeedQuotaStatsTables(t, fixture)
	t93PriceTheCatalogRow(t, fixture)
	upstream := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.server.URL)
	// 额度足够：预占通过，请求正常打到上游。
	if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET quota_limits_json = ? WHERE id = 'key_1'`),
		`{"total":{"enabled":true,"limit":100}}`); err != nil {
		t.Fatalf("seed quota limits: %v", err)
	}
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{}, false)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", status, body)
	}
	if upstream.hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstream.hits)
	}
}

func TestT93InflightQuotaSkipsWithoutLimits(t *testing.T) {
	fixture := newChainFixture(t)
	t93SeedQuotaStatsTables(t, fixture)
	t93PriceTheCatalogRow(t, fixture)
	upstream := newChatDispatchUpstream(t, chatDispatchSmokeBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.server.URL)
	// 无额度配置（fixture 默认 quota_limits_json 为空）：不预留、不拒绝。
	chain, _ := composeChatDispatchChain(t, fixture)

	status, body := chatDispatchRequest(t, chain, fixture, chatDispatchTarget{}, false)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", status, body)
	}
	if upstream.hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstream.hits)
	}
}
