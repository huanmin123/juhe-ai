package accounts

// 账户面 list usage 读口的 PG 方言契约回归（fixture 模式沿用 groups
// usage_reader_pg_dialect_test.go：SQLite 句柄 ATTACH ':memory:' AS juhe_stats
// 承载生产同款限定形式，SetMaxOpenConns(1) 保证 ATTACH 对所有查询可见；列
// 定义照抄 maintenance pg_schema_stats.go 的 usage_stats_daily /
// usage_stats_totals 建表，只保留读口 SELECT 的列）。
//
// 契约（docs/functions/缓存率感知调度与用量缓存率展示设计.md 9.1）：PG 上必须
// 以 juhe_stats. 限定 usage_stats_daily（AND stat_date = ?）/
// usage_stats_totals（无时间过滤）；totalTokens 保持 input+output 语义，今日行
// 拆出 input_tokens 与 cache_read_tokens 两列并按样本门装配 cacheReadRate；
// SQLite 分支的裸表名与双臂覆盖在 list_usage_test.go 锁定。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const accountUsageDailyFixtureDDL = `CREATE TABLE juhe_stats.usage_stats_daily (
		system_account_id text NOT NULL,
		scope_type text NOT NULL,
		scope_id text NOT NULL DEFAULT '',
		stat_date text NOT NULL,
		request_count bigint NOT NULL DEFAULT 0,
		input_tokens bigint NOT NULL DEFAULT 0,
		output_tokens bigint NOT NULL DEFAULT 0,
		cache_read_tokens bigint NOT NULL DEFAULT 0,
		success_cost_usd double precision NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
	)`

const accountUsageTotalsFixtureDDL = `CREATE TABLE juhe_stats.usage_stats_totals (
		system_account_id text NOT NULL,
		scope_type text NOT NULL,
		scope_id text NOT NULL DEFAULT '',
		request_count bigint NOT NULL DEFAULT 0,
		input_tokens bigint NOT NULL DEFAULT 0,
		output_tokens bigint NOT NULL DEFAULT 0,
		cache_read_tokens bigint NOT NULL DEFAULT 0,
		success_cost_usd double precision NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`

func newAccountUsagePGFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "account-usage-dialect.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`ATTACH ':memory:' AS juhe_stats`,
		accountUsageDailyFixtureDDL,
		accountUsageTotalsFixtureDDL,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture exec %q: %v", statement, err)
		}
	}
	return db
}

// TestAccountListUsageSummariesPGDialectBranches locks the production dialect
// contract for the account list usage projection: the PG reader resolves
// juhe_stats.usage_stats_daily (stat_date filtered, split input/cache-read
// columns, sample-gated cacheReadRate) and juhe_stats.usage_stats_totals (no
// time filter, raw columns carried; the three-field collapse happens in
// hydrateListUsage, locked by TestListPageUsageHydrationLocksIn).
func TestAccountListUsageSummariesPGDialectBranches(t *testing.T) {
	db := newAccountUsagePGFixture(t)
	// 达门有效值行：input 200k / cache 100k → rate 0.5；totalTokens=input+output。
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_daily
		(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens,
		 cache_read_tokens, success_cost_usd)
		VALUES ('owner-1', 'account', 'acc-1', '2026-10-08', 3, 200000, 40, 100000, 1.5)`); err != nil {
		t.Fatal(err)
	}
	// 另一日期的行：证明 daily 分支的 AND stat_date = ? 过滤不串桶。
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_daily
		(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens,
		 cache_read_tokens, success_cost_usd)
		VALUES ('owner-1', 'account', 'acc-1', '2026-10-07', 999, 999999, 999, 999999, 999)`); err != nil {
		t.Fatal(err)
	}
	// 门下样本行：input 99_999 → rate null。
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_daily
		(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens,
		 cache_read_tokens, success_cost_usd)
		VALUES ('owner-1', 'account', 'acc-gate', '2026-10-08', 2, 99999, 10, 50000, 0.1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens,
		 cache_read_tokens, success_cost_usd)
		VALUES ('owner-1', 'account_authorization', 'ra_1', 9, 500, 100, 60, 12.5)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewStatsUsageSource(db, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// daily + stat_date 分支：juhe_stats. 限定解析成功，新两列与 rate 就位。
	todays, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-1"},
	}, "2026-10-08")
	if err != nil {
		t.Fatalf("pg daily branch must resolve juhe_stats.usage_stats_daily: %v", err)
	}
	today, ok := todays["acc-1"]
	if !ok {
		t.Fatalf("daily branch lost acc-1: %+v", todays)
	}
	if today.RequestCount != 3 || today.TotalTokens != 200_040 || today.TotalCost != 1.5 ||
		today.InputTokens != 200_000 || today.CacheReadTokens != 100_000 {
		t.Fatalf("daily summary mismatch: %+v", today)
	}
	if today.CacheReadRate == nil || *today.CacheReadRate != 0.5 {
		t.Fatalf("daily branch must render the valid cache rate: %+v", today.CacheReadRate)
	}

	// stat_date 过滤：另一日期（异常大样本行）不串桶。
	other, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-1"},
	}, "2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	if row := other["acc-1"]; row.RequestCount != 999 {
		t.Fatalf("daily stat_date filter leaked across buckets: %+v", row)
	}

	// 门下样本：PG 臂同样按 CacheMinSampleInputTokens 判 null。
	gated, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-gate", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-gate"},
	}, "2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	if row := gated["acc-gate"]; row.CacheReadRate != nil {
		t.Fatalf("below-gate input must render null rate: %+v", row)
	}

	// totals 分支：无时间过滤，读 usage_stats_totals（account_authorization scope），
	// 原始列原样投影（rate 由样本门判 null：input 500 < 100_000）。
	totals, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-2", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccountAuthorization, ScopeID: "ra_1"},
	}, "")
	if err != nil {
		t.Fatalf("pg totals branch must resolve juhe_stats.usage_stats_totals: %v", err)
	}
	total, ok := totals["acc-2"]
	if !ok {
		t.Fatalf("totals branch lost acc-2: %+v", totals)
	}
	if total != (TodayUsageSummary{RequestCount: 9, TotalTokens: 600, TotalCost: 12.5, InputTokens: 500, CacheReadTokens: 60}) {
		t.Fatalf("totals summary mismatch: %+v", total)
	}

	// 缺行 → LEFT JOIN 零值形状（caller 的零值回退等价 Node zeroed COALESCE）。
	missing, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-missing", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-missing"},
	}, "2026-10-08")
	if err != nil {
		t.Fatal(err)
	}
	if empty, ok := missing["acc-missing"]; !ok || empty != (TodayUsageSummary{}) {
		t.Fatalf("missing row must still join to the zero shape: %+v ok=%v", empty, ok)
	}
}
