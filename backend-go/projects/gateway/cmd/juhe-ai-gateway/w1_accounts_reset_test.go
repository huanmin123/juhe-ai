package main

// compose_accounts_reset.go 的补充单元测试（w1 波次）：覆盖 guard 记忆、
// dialect、SQL 查询辅助函数和 AuthorizationQuotaExceeded 完整路径。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// fixture: 业务库 schema + 统计表 + 桥接实例
// ---------------------------------------------------------------------------

type w1sResetFixture struct {
	db     *sql.DB
	bridge *accountsRuntimeResetBridge
	now    time.Time
}

func w1sEnsureBusinessSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			effective_source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT, limits_json TEXT)`,
		`CREATE TABLE IF NOT EXISTS resource_authorization_grants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			grantee_type TEXT NOT NULL, grantee_team_id TEXT,
			limits_json TEXT, status TEXT NOT NULL DEFAULT 'active',
			expires_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY, authorization_instance_authorization_id TEXT,
			deleted_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS account_api_key_runtime_states (
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
		`CREATE TABLE IF NOT EXISTS usage_stats_totals (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			total_cost_usd REAL, success_cost_usd REAL, PRIMARY KEY (system_account_id, scope_type, scope_id))`,
		`CREATE TABLE IF NOT EXISTS usage_stats_daily (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_date TEXT, total_cost_usd REAL, success_cost_usd REAL)`,
		`CREATE TABLE IF NOT EXISTS usage_stats_weekly (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_week TEXT, total_cost_usd REAL, success_cost_usd REAL)`,
		`CREATE TABLE IF NOT EXISTS usage_stats_monthly (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			stat_month TEXT, total_cost_usd REAL, success_cost_usd REAL)`,
		`CREATE TABLE IF NOT EXISTS usage_quota_hourly_windows (
			system_account_id TEXT, scope_type TEXT, scope_id TEXT,
			window_hours INTEGER, total_cost_usd REAL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("seed stats schema: %v", err)
		}
	}
}

func newW1SResetFixture(t *testing.T) *w1sResetFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "w1s-reset.sqlite3"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w1sEnsureBusinessSchema(t, db)
	w1sEnsureStatsSchema(t, db)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	stats, err := gatewayquota.NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("stats store: %v", err)
	}
	guard := gatewayaccounteffects.NewAccountAPIKeyFailureGuard(
		gatewayaccounteffects.SideEffectsConfig{RuntimeStateDriver: "memory"},
		gatewayaccounteffects.SystemClock{}, nil, nil)

	composed := &composition{db: db, Bus: inval.New(time.Now)}
	bridge, err := newAccountsRuntimeResetBridge(composed,
		func(key string) (string, error) {
			if key == "usageStatsTimezone" {
				return "UTC", nil
			}
			return "", nil
		},
		&chainRuntimeServices{QuotaStats: stats, AccountAPIKeyGuard: guard},
		"w1s-test-secret",
		newChainProbeRequestOutboxWriter(nil, false, 65_000))
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	concrete := bridge.(*accountsRuntimeResetBridge)
	concrete.now = func() time.Time { return now }
	return &w1sResetFixture{db: db, bridge: concrete, now: now}
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
// table / bind 方言（postgres 前缀 + $N）
// ---------------------------------------------------------------------------

func TestW1SAccountsResetTableBindDialect(t *testing.T) {
	sqliteBridge := &accountsRuntimeResetBridge{pg: false}
	sqliteQuery := "SELECT x FROM t WHERE a = ? AND b = ?"
	if got := sqliteBridge.table("resource_authorizations"); got != "resource_authorizations" {
		t.Fatalf("sqlite table = %q, want 裸表名", got)
	}
	if got := sqliteBridge.bind(sqliteQuery); got != sqliteQuery {
		t.Fatalf("sqlite bind 必须原样返回, got %q", got)
	}

	pgBridge := &accountsRuntimeResetBridge{pg: true}
	if got := pgBridge.table("resource_authorizations"); got != "juhe_business.resource_authorizations" {
		t.Fatalf("pg table = %q", got)
	}
	if got := pgBridge.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("pg table accounts = %q", got)
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
			if got := pgBridge.bind(testCase.query); got != testCase.want {
				t.Fatalf("pg.bind(%q) = %q, want %q", testCase.query, got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// settingsTimezoneProvider.StatsTimezone：默认 UTC / 加载 / 非法 / 读错
// ---------------------------------------------------------------------------

func TestW1SAccountsResetSettingsTimezoneProvider(t *testing.T) {
	cases := []struct {
		name    string
		read    SettingValueFunc
		wantLoc string
		wantErr bool
	}{
		{"空配置默认 UTC", func(string) (string, error) { return "", nil }, "UTC", false},
		{"加载 America/New_York", func(string) (string, error) { return "America/New_York", nil }, "America/New_York", false},
		{"非法时区报错", func(string) (string, error) { return "Invalid/Zone", nil }, "", true},
		{"读取错误传播", func(string) (string, error) { return "", sql.ErrConnDone }, "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			provider := settingsTimezoneProvider{read: testCase.read}
			loc, err := provider.StatsTimezone(context.Background())
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("期望错误, got loc=%v", loc)
				}
				return
			}
			if err != nil {
				t.Fatalf("StatsTimezone: %v", err)
			}
			if loc.String() != testCase.wantLoc {
				t.Fatalf("loc = %s, want %s", loc.String(), testCase.wantLoc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// authorizationLimitsJSON：有行 / 无行 / NULL limits
// ---------------------------------------------------------------------------

func TestW1SAuthorizationLimitsJSONBranches(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()
	bridge := fixture.bridge

	// 无行：返回空串。
	limits, err := bridge.authorizationLimitsJSON(ctx, "nonexistent-auth")
	if err != nil {
		t.Fatalf("authorizationLimitsJSON: %v", err)
	}
	if limits != "" {
		t.Fatalf("无行应返回空串, got %q", limits)
	}

	// 有行 + limits_json 为 NULL：返回空串。
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, status)
		VALUES ('ra-1', 'model', 'm1', 'active')`); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	limits, err = bridge.authorizationLimitsJSON(ctx, "ra-1")
	if err != nil {
		t.Fatalf("authorizationLimitsJSON NULL: %v", err)
	}
	if limits != "" {
		t.Fatalf("NULL limits 应返回空串, got %q", limits)
	}

	// 有行 + limits_json 有值：返回该值。
	if _, err := fixture.db.Exec(
		`UPDATE resource_authorizations SET limits_json = ? WHERE id = 'ra-1'`,
		`{"daily":{"enabled":true,"limit":10}}`); err != nil {
		t.Fatalf("update limits: %v", err)
	}
	limits, err = bridge.authorizationLimitsJSON(ctx, "ra-1")
	if err != nil {
		t.Fatalf("authorizationLimitsJSON with value: %v", err)
	}
	if limits != `{"daily":{"enabled":true,"limit":10}}` {
		t.Fatalf("limits = %q", limits)
	}
}

// ---------------------------------------------------------------------------
// teamGrantLimitsJSON：有 / 无 / 过期 / effective_source_team_id=NULL
// ---------------------------------------------------------------------------

func TestW1STeamGrantLimitsJSONBranches(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()
	bridge := fixture.bridge
	now := fixture.now

	// authorization 有 effective_source_team_id 但无匹配 team grant → 空串。
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, effective_source_team_id, status)
		VALUES ('ra-tg', 'model', 'm1', 'tm-1', 'active')`); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	limits, err := bridge.teamGrantLimitsJSON(ctx, "ra-tg", now)
	if err != nil {
		t.Fatalf("teamGrantLimitsJSON: %v", err)
	}
	if limits != "" {
		t.Fatalf("无 team grant 应返回空串, got %q", limits)
	}

	// 插入活跃 team grant（resource_type/resource_id 匹配）。
	futureISO := now.Add(48*time.Hour).UTC().Format("2006-01-02T15:04:05.000") + "Z"
	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorization_grants (resource_type, resource_id, grantee_type, grantee_team_id, limits_json, status, expires_at)
		VALUES (?, 'm1', 'team', 'tm-1', '{"total":{"enabled":true,"limit":5}}', 'active', ?)`,
		"model", futureISO); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	limits, err = bridge.teamGrantLimitsJSON(ctx, "ra-tg", now)
	if err != nil {
		t.Fatalf("teamGrantLimitsJSON with grant: %v", err)
	}
	if limits != `{"total":{"enabled":true,"limit":5}}` {
		t.Fatalf("team limits = %q", limits)
	}

	// 过期 grant → 空串。
	pastISO := now.Add(-1*time.Hour).UTC().Format("2006-01-02T15:04:05.000") + "Z"
	if _, err := fixture.db.Exec(
		`UPDATE resource_authorization_grants SET expires_at = ? WHERE grantee_team_id = 'tm-1'`, pastISO); err != nil {
		t.Fatalf("expire grant: %v", err)
	}
	limits, err = bridge.teamGrantLimitsJSON(ctx, "ra-tg", now)
	if err != nil {
		t.Fatalf("teamGrantLimitsJSON expired: %v", err)
	}
	if limits != "" {
		t.Fatalf("过期 grant 应返回空串, got %q", limits)
	}

	// effective_source_team_id=NULL 的 authorization → 直接走 nil team 分支，不查 grant。
	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, effective_source_team_id, status)
		VALUES ('ra-no-team', 'model', 'm1', NULL, 'active')`); err != nil {
		t.Fatalf("seed no-team: %v", err)
	}
	limits, err = bridge.teamGrantLimitsJSON(ctx, "ra-no-team", now)
	if err != nil {
		t.Fatalf("teamGrantLimitsJSON no-team: %v", err)
	}
	if limits != "" {
		t.Fatalf("无 effective_source_team_id 应返回空串, got %q", limits)
	}
}

// ---------------------------------------------------------------------------
// authorizationInstanceIDs：有行 / 无行 / deleted_at 有效
// ---------------------------------------------------------------------------

func TestW1SAuthorizationInstanceIDsBranches(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()
	bridge := fixture.bridge

	// 无匹配行。
	ids, err := bridge.authorizationInstanceIDs(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("authorizationInstanceIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("无实例应返回空, got %v", ids)
	}

	// 插入两个活跃实例 + 一个已软删除实例。
	for _, row := range [][2]string{
		{"inst-1", "auth-x"}, {"inst-2", "auth-x"}, {"inst-deleted", "auth-x"},
	} {
		var deletedAt any
		if row[0] == "inst-deleted" {
			deletedAt = "2026-09-01T00:00:00.000Z"
		}
		if _, err := fixture.db.Exec(`INSERT INTO accounts (id, authorization_instance_authorization_id, deleted_at)
			VALUES (?, ?, ?)`, row[0], row[1], deletedAt); err != nil {
			t.Fatalf("seed account %s: %v", row[0], err)
		}
	}
	ids, err = bridge.authorizationInstanceIDs(ctx, "auth-x")
	if err != nil {
		t.Fatalf("authorizationInstanceIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "inst-1" || ids[1] != "inst-2" {
		t.Fatalf("实例列表 = %v, want [inst-1 inst-2]", ids)
	}
}

// ---------------------------------------------------------------------------
// AuthorizationQuotaExceeded：短路 / 无 limits / 超限 / 不超限 / 团队授权 / 非法 JSON
// ---------------------------------------------------------------------------

func TestW1SAuthorizationQuotaExceededShortCircuits(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   accounts.AuthorizationQuotaCheckInput
	}{
		{"空 authorization", accounts.AuthorizationQuotaCheckInput{AuthorizationID: "", GranteeSystemAccountID: "g1"}},
		{"空 grantee", accounts.AuthorizationQuotaCheckInput{AuthorizationID: "a1", GranteeSystemAccountID: ""}},
		{"两者皆空", accounts.AuthorizationQuotaCheckInput{}},
		{"空白填充", accounts.AuthorizationQuotaCheckInput{AuthorizationID: "   ", GranteeSystemAccountID: "  "}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, testCase.in)
			if err != nil || exceeded {
				t.Fatalf("短路判定 = (%v, %v), want (false, nil)", exceeded, err)
			}
		})
	}
}

func TestW1SAuthorizationQuotaExceededNoLimits(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()

	if _, err := fixture.db.Exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, status)
		VALUES ('no-lim', 'model', 'm1', 'active')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID: "no-lim", GranteeSystemAccountID: "g1"})
	if err != nil || exceeded {
		t.Fatalf("无 limits = (%v, %v), want (false, nil)", exceeded, err)
	}
}

func TestW1SAuthorizationQuotaExceededExceeded(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()

	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, status, limits_json)
		VALUES ('ex-auth', 'model', 'm1', 'active', ?)`,
		`{"daily":{"enabled":true,"limit":10}}`); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	// scope_type = account_authorization, scope_id = ex-auth, daily cost=50 >= daily limit 10。
	// 需要写入 usage_stats_daily 表（日成本由该表读 success_cost_usd 成功口径列）。
	if _, err := fixture.db.Exec(
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, total_cost_usd, success_cost_usd)
		VALUES (?, ?, ?, ?, ?, ?)`, "g1", gatewayquota.ScopeTypeAccountAuthorization, "ex-auth", "2026-09-10", 50.0, 50.0); err != nil {
		t.Fatalf("seed stats: %v", err)
	}
	exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID: "ex-auth", GranteeSystemAccountID: "g1"})
	if err != nil || !exceeded {
		t.Fatalf("direct exceeded = (%v, %v), want (true, nil)", exceeded, err)
	}
}

func TestW1SAuthorizationQuotaExceededUnderLimit(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()

	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, status, limits_json)
		VALUES ('under-auth', 'model', 'm1', 'active', ?)`,
		`{"daily":{"enabled":true,"limit":100}}`); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	// cost=1 < limit 100 → 未超限。写入 usage_stats_daily 表（读 success_cost_usd）。
	if _, err := fixture.db.Exec(
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, total_cost_usd, success_cost_usd)
		VALUES (?, ?, ?, ?, ?, ?)`, "g1", gatewayquota.ScopeTypeAccountAuthorization, "under-auth", "2026-09-10", 1.0, 1.0); err != nil {
		t.Fatalf("seed stats: %v", err)
	}
	exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID: "under-auth", GranteeSystemAccountID: "g1"})
	if err != nil || exceeded {
		t.Fatalf("under-limit = (%v, %v), want (false, nil)", exceeded, err)
	}
}

func TestW1SAuthorizationQuotaExceededTeamGrant(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()
	now := fixture.bridge.now()
	futureISO := now.Add(48*time.Hour).UTC().Format("2006-01-02T15:04:05.000") + "Z"

	// authorization: effective_source_team_id='tm-1'，自身无 limits_json。
	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, effective_source_team_id, status, expires_at)
		VALUES ('team-auth', 'model', 'm1', 'tm-1', 'active', ?)`, futureISO); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	// team grant: total limit 3, 未过期。
	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorization_grants (resource_type, resource_id, grantee_type, grantee_team_id, limits_json, status, expires_at)
		VALUES ('model', 'm1', 'team', 'tm-1', '{"total":{"enabled":true,"limit":3}}', 'active', ?)`, futureISO); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	// 两个共享该 authorization 的实例账户。
	for _, inst := range []string{"inst-a", "inst-b"} {
		if _, err := fixture.db.Exec(
			`INSERT INTO accounts (id, authorization_instance_authorization_id, deleted_at)
			VALUES (?, 'team-auth', NULL)`, inst); err != nil {
			t.Fatalf("seed instance %s: %v", inst, err)
		}
	}
	// team bucket key = <instanceId>:<teamId>；给 inst-b 超限成本（读 success_cost_usd）。
	if _, err := fixture.db.Exec(
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, total_cost_usd, success_cost_usd)
		VALUES (?, ?, ?, ?, ?)`, "g1", gatewayquota.ScopeTypeAccountAuthorizationTeam, "inst-b:tm-1", 9.0, 9.0); err != nil {
		t.Fatalf("seed team stats: %v", err)
	}
	exceeded, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID:        "team-auth",
		GranteeSystemAccountID: "g1",
		EffectiveSourceTeamID:  "tm-1"})
	if err != nil || !exceeded {
		t.Fatalf("team grant exceeded = (%v, %v), want (true, nil)", exceeded, err)
	}
}

func TestW1SAuthorizationQuotaExceededInvalidLimitsJSON(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()

	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, status, limits_json)
		VALUES ('bad-auth', 'model', 'm1', 'active', 'NOT VALID JSON{')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID: "bad-auth", GranteeSystemAccountID: "g1"})
	if err == nil {
		t.Fatal("非法 limits_json 应返回解析错误")
	}
}

func TestW1SAuthorizationQuotaExceededTeamLimitsInvalidJSON(t *testing.T) {
	fixture := newW1SResetFixture(t)
	ctx := context.Background()
	now := fixture.bridge.now()
	futureISO := now.Add(48*time.Hour).UTC().Format("2006-01-02T15:04:05.000") + "Z"

	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, effective_source_team_id, status, expires_at)
		VALUES ('bad-team-auth', 'model', 'm1', 'tm-1', 'active', ?)`, futureISO); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	if _, err := fixture.db.Exec(
		`INSERT INTO resource_authorization_grants (resource_type, resource_id, grantee_type, grantee_team_id, limits_json, status, expires_at)
		VALUES ('model', 'm1', 'team', 'tm-1', 'NOT JSON{', 'active', ?)`, futureISO); err != nil {
		t.Fatalf("seed bad grant: %v", err)
	}
	_, err := fixture.bridge.AuthorizationQuotaExceeded(ctx, accounts.AuthorizationQuotaCheckInput{
		AuthorizationID:        "bad-team-auth",
		GranteeSystemAccountID: "g1",
		EffectiveSourceTeamID:  "tm-1"})
	if err == nil {
		t.Fatal("非法 team limits_json 应返回解析错误")
	}
}
