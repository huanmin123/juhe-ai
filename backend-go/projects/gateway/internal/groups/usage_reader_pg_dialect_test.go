package groups

// 分组面 usage summary 读口的 SQL 方言契约回归（fixture 模式沿用
// stats_reader_pg_dialect_test.go：SQLite 句柄 ATTACH ':memory:' AS juhe_stats
// 承载生产同款限定形式，SetMaxOpenConns(1) 保证 ATTACH 对所有查询可见；列
// 定义照抄 maintenance pg_schema_stats.go 的 usage_stats_daily /
// usage_stats_totals 建表，只保留读口 SELECT 的列）。
//
// 契约（docs/functions/统计指标与分层聚合设计.md）：PG 上必须以 juhe_stats.
// 限定 usage_stats_daily / usage_stats_totals（生产 PG 无 search_path，裸表名
// 42P01 → hydrate 整体回退空投影 → "用量(日)"列恒 0）；daily 分支带
// AND stat_date = ?，totals 分支无时间过滤；SQLite 分支保持裸表名。
//
// 修复前必红：GroupListUsageSummaries / UsageSource / WithUsageSource 不存在，
// 分组列表 todayUsage/usage 从未被真实 hydrate（恒 0），本文件无法编译，
// 等价于全红。

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const groupUsageDailyFixtureDDL = `CREATE TABLE juhe_stats.usage_stats_daily (
		system_account_id text NOT NULL,
		scope_type text NOT NULL,
		scope_id text NOT NULL DEFAULT '',
		stat_date text NOT NULL,
		request_count bigint NOT NULL DEFAULT 0,
		input_tokens bigint NOT NULL DEFAULT 0,
		output_tokens bigint NOT NULL DEFAULT 0,
		cache_read_tokens bigint NOT NULL DEFAULT 0,
		cache_read_cost_usd double precision NOT NULL DEFAULT 0,
		cache_write_tokens bigint NOT NULL DEFAULT 0,
		cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
		cache_write_cost_usd double precision NOT NULL DEFAULT 0,
		thinking_tokens bigint NOT NULL DEFAULT 0,
		input_image_tokens bigint NOT NULL DEFAULT 0,
		output_image_tokens bigint NOT NULL DEFAULT 0,
		total_cost_usd double precision NOT NULL DEFAULT 0,
		last_used_at text,
		PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date)
	)`

const groupUsageTotalsFixtureDDL = `CREATE TABLE juhe_stats.usage_stats_totals (
		system_account_id text NOT NULL,
		scope_type text NOT NULL,
		scope_id text NOT NULL DEFAULT '',
		request_count bigint NOT NULL DEFAULT 0,
		input_tokens bigint NOT NULL DEFAULT 0,
		output_tokens bigint NOT NULL DEFAULT 0,
		cache_read_tokens bigint NOT NULL DEFAULT 0,
		cache_read_cost_usd double precision NOT NULL DEFAULT 0,
		cache_write_tokens bigint NOT NULL DEFAULT 0,
		cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
		cache_write_cost_usd double precision NOT NULL DEFAULT 0,
		thinking_tokens bigint NOT NULL DEFAULT 0,
		input_image_tokens bigint NOT NULL DEFAULT 0,
		output_image_tokens bigint NOT NULL DEFAULT 0,
		total_cost_usd double precision NOT NULL DEFAULT 0,
		last_used_at text,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`

func newGroupUsagePGFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "group-usage-dialect.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`ATTACH ':memory:' AS juhe_stats`,
		groupUsageDailyFixtureDDL,
		groupUsageTotalsFixtureDDL,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture exec %q: %v", statement, err)
		}
	}
	return db
}

// TestGroupListUsageSummariesPGDialectBranches locks the production dialect
// contract: the PG reader resolves juhe_stats.usage_stats_daily (stat_date
// filtered) and juhe_stats.usage_stats_totals (no time filter), renders the
// full usageSummaryFromAggregate projection and degrades missing rows to the
// empty 13-key shape without lastUsedAt.
func TestGroupListUsageSummariesPGDialectBranches(t *testing.T) {
	db := newGroupUsagePGFixture(t)
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_daily
		(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens,
		 cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		 cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		 total_cost_usd, last_used_at)
		VALUES ('owner-1', 'group', 'grp_1', '2026-10-02', 3, 100, 40, 7, 0.5, 2, 1, 0.25, 5, 0, 0, 1.5,
		 '2026-10-02T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	// 另一日期的行：证明 daily 分支的 AND stat_date = ? 过滤不串桶。
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_daily
		(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens,
		 total_cost_usd, last_used_at)
		VALUES ('owner-1', 'group', 'grp_1', '2026-10-01', 999, 9999, 9999, 999, '2026-10-01T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens,
		 cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		 cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		 total_cost_usd, last_used_at)
		VALUES ('owner-1', 'group_authorization', 'ra_1', 9, 500, 100, 3, 1.5, 1, 0, 0.5, 2, 1, 1, 12.5, NULL)`); err != nil {
		t.Fatal(err)
	}
	reader := NewGroupAccountStatsDBReader(db, true)
	ctx := context.Background()

	// daily + stat_date 分支：全字段投影，totalTokens = input + output（不含 cache）。
	todays, err := reader.GroupListUsageSummaries(ctx, []UsageScope{
		{RowKey: "grp_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_1"},
	}, "2026-10-02")
	if err != nil {
		t.Fatalf("pg daily branch must resolve juhe_stats.usage_stats_daily: %v", err)
	}
	today, ok := todays["grp_1"]
	if !ok {
		t.Fatalf("daily branch lost grp_1: %+v", todays)
	}
	if today.RequestCount != 3 || today.InputTokens != 100 || today.OutputTokens != 40 ||
		today.TotalTokens != 140 || today.CacheReadTokens != 7 || today.CacheReadCost != 0.5 ||
		today.CacheWriteTokens != 2 || today.CacheWrite1hTokens != 1 || today.CacheWriteCost != 0.25 ||
		today.ThinkingTokens != 5 || today.InputImageTokens != 0 || today.OutputImageTokens != 0 ||
		today.TotalCost != 1.5 {
		t.Fatalf("daily summary mismatch: %+v", today)
	}
	if today.LastUsedAt == nil || *today.LastUsedAt != "2026-10-02T08:00:00.000Z" {
		t.Fatalf("daily lastUsedAt must render when non-null: %+v", today.LastUsedAt)
	}

	// stat_date 过滤：另一日期的行不串桶。
	other, err := reader.GroupListUsageSummaries(ctx, []UsageScope{
		{RowKey: "grp_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_1"},
	}, "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if row := other["grp_1"]; row.RequestCount != 999 {
		t.Fatalf("daily stat_date filter leaked across buckets: %+v", row)
	}

	// totals 分支：无时间过滤，读 usage_stats_totals（group_authorization scope）。
	totals, err := reader.GroupListUsageSummaries(ctx, []UsageScope{
		{RowKey: "ra_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroupAuthorization, ScopeID: "ra_1"},
	}, "")
	if err != nil {
		t.Fatalf("pg totals branch must resolve juhe_stats.usage_stats_totals: %v", err)
	}
	total, ok := totals["ra_1"]
	if !ok {
		t.Fatalf("totals branch lost ra_1: %+v", totals)
	}
	if total.RequestCount != 9 || total.TotalTokens != 600 || total.TotalCost != 12.5 {
		t.Fatalf("totals summary mismatch: %+v", total)
	}
	if total.LastUsedAt != nil {
		t.Fatalf("null last_used_at must stay omitted: %+v", total.LastUsedAt)
	}

	// 缺行 → LEFT JOIN 空 13 键形状、无 lastUsedAt 键（Node empty shape）。
	missing, err := reader.GroupListUsageSummaries(ctx, []UsageScope{
		{RowKey: "grp_missing", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_missing"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	empty, ok := missing["grp_missing"]
	if !ok {
		t.Fatalf("missing row must still join to the empty shape: %+v", missing)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 13 {
		t.Fatalf("empty shape must carry exactly the 13 keys: %v", decoded)
	}
	if _, present := decoded["lastUsedAt"]; present {
		t.Fatalf("empty shape must omit lastUsedAt: %v", decoded)
	}
	if decoded["requestCount"] != float64(0) || decoded["totalTokens"] != float64(0) {
		t.Fatalf("empty shape must be all zero: %v", decoded)
	}

	// 空 scope / 无效 scope → 空结果不报错；nil db 句柄 → 空结果（W11F nil-db 同款）。
	if got, err := reader.GroupListUsageSummaries(ctx, []UsageScope{{RowKey: "x"}}, ""); err != nil || len(got) != 0 {
		t.Fatalf("invalid scopes = %v/%v", got, err)
	}
	nilReader := &GroupAccountStatsDBReader{}
	if got, err := nilReader.GroupListUsageSummaries(ctx, []UsageScope{
		{RowKey: "g", SystemAccountID: "o", ScopeType: usageScopeTypeGroup, ScopeID: "g"},
	}, ""); err != nil || len(got) != 0 {
		t.Fatalf("nil db = %v/%v", got, err)
	}
}

// TestGroupListUsageSummariesSQLiteBareTables locks the SQLite branch: bare
// table names (现状契约不回退), both the daily and totals branches resolve.
func TestGroupListUsageSummariesSQLiteBareTables(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "group-usage-dialect-sqlite.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE usage_stats_daily (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			stat_date text NOT NULL, request_count bigint NOT NULL DEFAULT 0,
			input_tokens bigint NOT NULL DEFAULT 0, output_tokens bigint NOT NULL DEFAULT 0,
			cache_read_tokens bigint NOT NULL DEFAULT 0, cache_read_cost_usd double precision NOT NULL DEFAULT 0,
			cache_write_tokens bigint NOT NULL DEFAULT 0, cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
			cache_write_cost_usd double precision NOT NULL DEFAULT 0, thinking_tokens bigint NOT NULL DEFAULT 0,
			input_image_tokens bigint NOT NULL DEFAULT 0, output_image_tokens bigint NOT NULL DEFAULT 0,
			total_cost_usd double precision NOT NULL DEFAULT 0, last_used_at text,
			PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date))`,
		`CREATE TABLE usage_stats_totals (
			system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL DEFAULT '',
			request_count bigint NOT NULL DEFAULT 0, input_tokens bigint NOT NULL DEFAULT 0,
			output_tokens bigint NOT NULL DEFAULT 0, cache_read_tokens bigint NOT NULL DEFAULT 0,
			cache_read_cost_usd double precision NOT NULL DEFAULT 0, cache_write_tokens bigint NOT NULL DEFAULT 0,
			cache_write_1h_tokens bigint NOT NULL DEFAULT 0, cache_write_cost_usd double precision NOT NULL DEFAULT 0,
			thinking_tokens bigint NOT NULL DEFAULT 0, input_image_tokens bigint NOT NULL DEFAULT 0,
			output_image_tokens bigint NOT NULL DEFAULT 0, total_cost_usd double precision NOT NULL DEFAULT 0,
			last_used_at text, PRIMARY KEY (system_account_id, scope_type, scope_id))`,
		`INSERT INTO usage_stats_daily
			(system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('owner-1', 'group', 'grp_sq', '2026-10-02', 5, 10, 20, 0.75)`,
		`INSERT INTO usage_stats_totals
			(system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('owner-1', 'group', 'grp_sq', 7, 30, 40, 3.5)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture exec %q: %v", statement, err)
		}
	}
	reader := NewGroupAccountStatsDBReader(db, false)
	scopes := []UsageScope{
		{RowKey: "grp_sq", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_sq"},
	}
	todays, err := reader.GroupListUsageSummaries(context.Background(), scopes, "2026-10-02")
	if err != nil {
		t.Fatalf("sqlite reader must keep the bare usage_stats_daily name: %v", err)
	}
	if row := todays["grp_sq"]; row.RequestCount != 5 || row.TotalTokens != 30 || row.TotalCost != 0.75 {
		t.Fatalf("sqlite daily summary mismatch: %+v", row)
	}
	totals, err := reader.GroupListUsageSummaries(context.Background(), scopes, "")
	if err != nil {
		t.Fatalf("sqlite reader must keep the bare usage_stats_totals name: %v", err)
	}
	if row := totals["grp_sq"]; row.RequestCount != 7 || row.TotalTokens != 70 || row.TotalCost != 3.5 {
		t.Fatalf("sqlite totals summary mismatch: %+v", row)
	}
}
