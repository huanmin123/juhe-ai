package main

// compose_accounts_reset.go 的补充单元测试（w1 波次）：覆盖 guard 记忆与
// dialect 渲染。（AuthorizationQuotaExceeded / limits_json 读取 / settings
// 时区读点与 usage stats 夹具面随授权额度域退役删除。）

import (
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// ---------------------------------------------------------------------------
// fixture: 业务库 schema + 桥接实例
// ---------------------------------------------------------------------------

type w1sResetFixture struct {
	db     *sql.DB
	bridge *accountsRuntimeResetBridge
	now    time.Time
}

// w1sEnsureBusinessSchema 建授权/实例/Key 运行态的最小夹具面。pgtestkit
// 隔离 PG 库 + search_path 连接池（newChainFixturePGDatabase 同款惯例）：
// 夹具 CREATE 显式限定 juhe_business（CREATE 只落 search_path 首位 schema），
// 生产桥的限定名读取与测试的裸表名种子/删除经 search_path 同 schema 解析。
// resource_authorization_grants.id 沿生产 text 主键，缺省表达式承载
// 「免 id 种子」。
func w1sEnsureBusinessSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			effective_source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
			id TEXT PRIMARY KEY NOT NULL DEFAULT 'w1s-grant-' || md5(random()::text),
			resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			grantee_type TEXT NOT NULL, grantee_team_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.accounts (
			id TEXT PRIMARY KEY, authorization_instance_authorization_id TEXT,
			deleted_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
			id TEXT PRIMARY KEY, system_account_id TEXT, account_id TEXT,
			key_fingerprint TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'unverified',
			cooldown_until TEXT, next_probe_at TEXT,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed business schema: %v", err)
		}
	}
}

func w1sEnsureStatsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	// usage_stats 四表带 success_cost_usd：quota 读点已切成功口径列；
	// usage_quota_hourly_windows 的 total_cost_usd 本身承载成功口径成本。
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_totals (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			total_cost_usd DOUBLE PRECISION, success_cost_usd DOUBLE PRECISION, PRIMARY KEY (system_account_id, scope_type, scope_id))`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_daily (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_date TEXT, total_cost_usd DOUBLE PRECISION, success_cost_usd DOUBLE PRECISION)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_weekly (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_week TEXT, total_cost_usd DOUBLE PRECISION, success_cost_usd DOUBLE PRECISION)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_monthly (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_month TEXT, total_cost_usd DOUBLE PRECISION, success_cost_usd DOUBLE PRECISION)`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_windows (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			window_hours INTEGER, total_cost_usd DOUBLE PRECISION)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed stats schema: %v", err)
		}
	}
}

func newW1SResetFixture(t *testing.T) *w1sResetFixture {
	t.Helper()
	db := newChainFixturePGDatabase(t)
	w1sEnsureBusinessSchema(t, db)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "memory"},
		gatewayaccounteffects.SystemClock{}, nil, nil)

	composed := &composition{db: db, Bus: inval.New(time.Now)}
	// （quota stats 与 settings 读取器参数随授权额度域退役摘除。）
	bridge, err := newAccountsRuntimeResetBridge(composed,
		&chainRuntimeServices{AccountAPIKeyGuard: guard},
		"w1s-test-secret",
		newChainProbeRequestOutboxWriter(nil, 65_000))
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	// newAccountsRuntimeResetBridge 直接返回具体桥类型（同时满足
	// RuntimeResetEffects 与 BackgroundProbeRuntimeCleanup 两个端口）。
	//（桥已无 now 字段：额度门的时钟读点随授权额度域退役删除。）
	return &w1sResetFixture{db: db, bridge: bridge, now: now}
}

// ---------------------------------------------------------------------------
// guardAccount：纯逻辑构造 OpenAIAccountSecret
// ---------------------------------------------------------------------------

func TestW1SAccountsResetGuardAccount(t *testing.T) {
	fixture := newW1SResetFixture(t)
	bridge := fixture.bridge

	// 无 generation：fingerprint 指针非 nil，generation 字段缺省。
	result := bridge.guardAccount("acc-1", "fp-aaa", "")
	if result.ID != "acc-1" {
		t.Fatalf("guardAccount ID = %q", result.ID)
	}
	if result.SelectedAPIKeyFingerprint == nil || *result.SelectedAPIKeyFingerprint != "fp-aaa" {
		t.Fatalf("fingerprint = %v, want fp-aaa", result.SelectedAPIKeyFingerprint)
	}
	if result.SelectedAPIKeyTransientGeneration != nil {
		t.Fatalf("空 generation 不应设置字段, got %v", *result.SelectedAPIKeyTransientGeneration)
	}

	// 有 generation：透传。
	withGen := bridge.guardAccount("acc-1", "fp-bbb", "gen-42")
	if withGen.SelectedAPIKeyTransientGeneration == nil || *withGen.SelectedAPIKeyTransientGeneration != "gen-42" {
		t.Fatalf("generation = %v, want gen-42", withGen.SelectedAPIKeyTransientGeneration)
	}

	// 空 fingerprint 仍传入（guard 侧决定忽略）。
	emptyFP := bridge.guardAccount("acc-2", "", "")
	if emptyFP.SelectedAPIKeyFingerprint == nil || *emptyFP.SelectedAPIKeyFingerprint != "" {
		t.Fatalf("空 fingerprint 应保持空串")
	}
}

// ---------------------------------------------------------------------------
// rememberTransientGeneration / rememberedTransientGeneration：内存 map 往返
// ---------------------------------------------------------------------------

func TestW1SAccountsResetTransientGenerationMemory(t *testing.T) {
	fixture := newW1SResetFixture(t)
	bridge := fixture.bridge

	// 初始：未知 (account, fingerprint) 返回空串。
	if got := bridge.rememberedTransientGeneration("acc-1", "fp-x"); got != "" {
		t.Fatalf("初始 generation 应为空, got %q", got)
	}

	bridge.rememberTransientGeneration("acc-1", "fp-x", "gen-1")
	if got := bridge.rememberedTransientGeneration("acc-1", "fp-x"); got != "gen-1" {
		t.Fatalf("读回 generation = %q, want gen-1", got)
	}

	// 不同 fingerprint 隔离。
	bridge.rememberTransientGeneration("acc-1", "fp-y", "gen-2")
	if got := bridge.rememberedTransientGeneration("acc-1", "fp-y"); got != "gen-2" {
		t.Fatalf("fp-y generation = %q, want gen-2", got)
	}
	if got := bridge.rememberedTransientGeneration("acc-1", "fp-x"); got != "gen-1" {
		t.Fatalf("fp-x generation 被意外覆盖: %q", got)
	}

	// 不同 account 隔离。
	bridge.rememberTransientGeneration("acc-2", "fp-x", "gen-3")
	if got := bridge.rememberedTransientGeneration("acc-2", "fp-x"); got != "gen-3" {
		t.Fatalf("acc-2 generation = %q, want gen-3", got)
	}

	// 覆盖写入。
	bridge.rememberTransientGeneration("acc-1", "fp-x", "gen-99")
	if got := bridge.rememberedTransientGeneration("acc-1", "fp-x"); got != "gen-99" {
		t.Fatalf("覆盖写入 generation = %q, want gen-99", got)
	}

	// 空串 generation 读取返回 ""（未记忆）。
	if got := bridge.rememberedTransientGeneration("acc-1", "missing-fp"); got != "" {
		t.Fatalf("未记忆组合应返回空, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// table / bind 渲染（juhe_business 前缀 + $N）
// ---------------------------------------------------------------------------

func TestW1SAccountsResetTableBindDialect(t *testing.T) {
	bridge := &accountsRuntimeResetBridge{}
	if got := bridge.table("resource_authorizations"); got != "juhe_business.resource_authorizations" {
		t.Fatalf("table = %q", got)
	}
	if got := bridge.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("table accounts = %q", got)
	}
	cases := []struct {
		name, query, want string
	}{
		{"空串", "", ""},
		{"无占位符", "SELECT 1", "SELECT 1"},
		{"单占位符", "WHERE id = ?", "WHERE id = $1"},
		{"多占位符", "WHERE a = ? AND b = ? AND c = ?", "WHERE a = $1 AND b = $2 AND c = $3"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := bridge.bind(testCase.query); got != testCase.want {
				t.Fatalf("bind(%q) = %q, want %q", testCase.query, got, testCase.want)
			}
		})
	}
}
