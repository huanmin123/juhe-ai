package oauthrefresh

// proxy_pool_test.go 覆盖池绑定账户的有效目标解析（代理池设计 §5.2/§5.3/
// §6.6 只读入口）：refresh/keepalive 经 Store.proxyRequestURLForAccount 使用
// 共享 proxyresolve 解析器（Writer=nil，不补租约）；无有效租约/池不可用返回
// ProxyTargetUnavailableError 且绝不直连；直接代理与无绑定保持既有
// proxyProfileRequestURL 语义。池表 DDL 取生产被查列子集（resolver
// LoadProfiles 需要 pool_health_status 事实源列）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

const proxyPoolFixtureDDL = `
CREATE TABLE juhe_business.proxy_pools (
	id text PRIMARY KEY,
	system_account_id text NOT NULL DEFAULT 'sys_admin',
	name text NOT NULL,
	strategy text NOT NULL DEFAULT 'round_robin',
	enabled boolean NOT NULL DEFAULT true,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE juhe_business.proxy_profiles (
	id text PRIMARY KEY,
	system_account_id text NOT NULL,
	name text NOT NULL,
	description text,
	type text NOT NULL,
	host text NOT NULL,
	port integer NOT NULL,
	username text,
	password_encrypted text,
	enabled boolean NOT NULL DEFAULT true,
	pool_health_status text,
	pool_health_failure_streak integer NOT NULL DEFAULT 0,
	test_status text NOT NULL DEFAULT 'unknown',
	latency_ms integer,
	outbound_ip text,
	outbound_region text,
	last_test_message text,
	last_tested_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE juhe_business.proxy_pool_members (
	pool_id text NOT NULL,
	proxy_profile_id text NOT NULL,
	priority integer NOT NULL DEFAULT 0,
	weight integer NOT NULL DEFAULT 1,
	enabled boolean NOT NULL DEFAULT true,
	PRIMARY KEY (pool_id, proxy_profile_id)
);
CREATE TABLE juhe_business.account_proxy_leases (
	account_id text PRIMARY KEY,
	pool_id text NOT NULL,
	proxy_profile_id text NOT NULL,
	generation bigint NOT NULL DEFAULT 1,
	assignment_reason text NOT NULL DEFAULT 'initial',
	assigned_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
)`

type proxyPoolSeed struct {
	PoolID       string
	PoolEnabled  *bool
	MemberID     string
	MemberHost   string
	MemberPort   int64
	MemberOn     *bool
	MemberHealth string
	LeaseKey     string
	LeasePool    string
	LeaseMember  string
	LeaseAbsent  bool
}

func poolEnabled(value bool) *bool { return &value }

func seedProxyPool(t *testing.T, db *sql.DB, seed proxyPoolSeed) {
	t.Helper()
	poolEnabledValue := true
	if seed.PoolEnabled != nil {
		poolEnabledValue = *seed.PoolEnabled
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.proxy_pools (id, name, strategy, enabled) VALUES ($1, $1, 'round_robin', $2)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled`, seed.PoolID, poolEnabledValue); err != nil {
		t.Fatal(err)
	}
	if seed.MemberID != "" {
		// 成员档案行幂等 upsert（成员可能跨臂重复播种，如 dead→healthy）。
		sealed, err := EncryptJSON(cryptoTestSecret, map[string]any{"password": "pool-pass"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO juhe_business.proxy_profiles (id, type, host, port, username, password_encrypted, enabled, system_account_id, name, created_at, updated_at)
			VALUES ($1, 'socks5', $2, $3, 'pool-user', $4, true, 'sys_admin', $1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
			ON CONFLICT (id) DO UPDATE SET host = excluded.host, port = excluded.port, password_encrypted = excluded.password_encrypted`,
			seed.MemberID, seed.MemberHost, seed.MemberPort, sealed); err != nil {
			t.Fatal(err)
		}
		health := sql.NullString{}
		if seed.MemberHealth != "" {
			health = sql.NullString{String: seed.MemberHealth, Valid: true}
		}
		if _, err := db.Exec(`UPDATE juhe_business.proxy_profiles SET pool_health_status = $2 WHERE id = $1`, seed.MemberID, health); err != nil {
			t.Fatal(err)
		}
		memberEnabled := seed.MemberOn == nil || *seed.MemberOn
		if _, err := db.Exec(`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled) VALUES ($1, $2, 0, 1, $3)
			ON CONFLICT (pool_id, proxy_profile_id) DO UPDATE SET enabled = excluded.enabled`,
			seed.PoolID, seed.MemberID, memberEnabled); err != nil {
			t.Fatal(err)
		}
	}
	if !seed.LeaseAbsent && seed.LeaseKey != "" {
		if _, err := db.Exec(`INSERT INTO juhe_business.account_proxy_leases (account_id, pool_id, proxy_profile_id, generation, assignment_reason)
			VALUES ($1, $2, $3, 1, 'initial')
			ON CONFLICT (account_id) DO UPDATE SET pool_id = excluded.pool_id, proxy_profile_id = excluded.proxy_profile_id`,
			seed.LeaseKey, seed.LeasePool, seed.LeaseMember); err != nil {
			t.Fatal(err)
		}
	}
}

func countProxyPoolLeases(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_proxy_leases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestProxyRequestURLForAccountPoolArms 逐分支覆盖池绑定的只读解析语义。
func TestProxyRequestURLForAccountPoolArms(t *testing.T) {
	store, db, _ := newTestStore(t)
	if _, err := db.Exec(proxyPoolFixtureDDL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seedAccountRow(t, db, accountRowSeed{ID: "acc-pool", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a"}, Now: time.Now()})
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-1", MemberID: "pp-member", MemberHost: "member.example", MemberPort: 1080,
		LeaseKey: "acc-pool", LeasePool: "pool-1", LeaseMember: "pp-member"})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-1' WHERE id = 'acc-pool'`); err != nil {
		t.Fatal(err)
	}

	// 有租约：解析到租约成员 URL；socks5 升级 socks5h 且带认证。
	account, err := store.FindRotationAccount(ctx, "acc-pool")
	if err != nil || account == nil {
		t.Fatalf("find=%v err=%v", account, err)
	}
	url, err := store.proxyRequestURLForAccount(ctx, account)
	if err != nil || url != "socks5h://pool-user:pool-pass@member.example:1080" {
		t.Fatalf("pool member url=%q err=%v", url, err)
	}

	// 只读性：解析不得补租约/写租约。
	if count := countProxyPoolLeases(t, db); count != 1 {
		t.Fatalf("解析后租约行数必须保持 1，得到 %d", count)
	}

	// 无租约：明确失败（非本地配置错误），不直连。
	if _, err := db.Exec(`DELETE FROM juhe_business.account_proxy_leases WHERE account_id = 'acc-pool'`); err != nil {
		t.Fatal(err)
	}
	var unavailable *ProxyTargetUnavailableError
	url, err = store.proxyRequestURLForAccount(ctx, account)
	if err == nil || url != "" || !errors.As(err, &unavailable) || IsLocalConfigurationError(err) || !strings.Contains(err.Error(), "proxy_pool_unavailable") {
		t.Fatalf("missing lease url=%q err=%v", url, err)
	}

	// 池停用：明确失败。
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-off", PoolEnabled: poolEnabled(false), MemberID: "pp-off-member", MemberHost: "off.example", MemberPort: 1080,
		LeaseKey: "acc-pool", LeasePool: "pool-off", LeaseMember: "pp-off-member"})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-off' WHERE id = 'acc-pool'`); err != nil {
		t.Fatal(err)
	}
	if account, err = store.FindRotationAccount(ctx, "acc-pool"); err != nil || account == nil {
		t.Fatalf("refind=%v err=%v", account, err)
	}
	if _, err := store.proxyRequestURLForAccount(ctx, account); err == nil || !errors.As(err, &unavailable) {
		t.Fatalf("disabled pool err=%v", err)
	}

	// 租约成员健康 dead：不可分配 → 明确失败。
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-1", MemberID: "pp-member", MemberHost: "member.example", MemberPort: 1080, MemberHealth: "dead",
		LeaseKey: "acc-pool", LeasePool: "pool-1", LeaseMember: "pp-member"})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-1' WHERE id = 'acc-pool'`); err != nil {
		t.Fatal(err)
	}
	if account, err = store.FindRotationAccount(ctx, "acc-pool"); err != nil || account == nil {
		t.Fatalf("refind=%v err=%v", account, err)
	}
	if _, err := store.proxyRequestURLForAccount(ctx, account); err == nil || !errors.As(err, &unavailable) {
		t.Fatalf("dead member err=%v", err)
	}

	// 授权实例：source pool 参与解析且租约键 = source account；实例自身池不参与。
	seedAccountRow(t, db, accountRowSeed{ID: "acc-source", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-src", "refresh_token": "rt-src"}, Now: time.Now()})
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-2", MemberID: "pp-src-member", MemberHost: "src.example", MemberPort: 1080,
		LeaseKey: "acc-source", LeasePool: "pool-2", LeaseMember: "pp-src-member"})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-1', authorization_instance_source_account_id = 'acc-source' WHERE id = 'acc-pool'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-1' WHERE id = 'acc-source'`); err != nil {
		t.Fatal(err)
	}
	if account, err = store.FindRotationAccount(ctx, "acc-pool"); err != nil || account == nil {
		t.Fatalf("refind instance=%v err=%v", account, err)
	}
	// source（acc-source）当前池 pool-1 没有 acc-source 的租约：实例自身
	// pool 候补也不得被使用 → 明确失败。
	if _, err := store.proxyRequestURLForAccount(ctx, account); err == nil || !errors.As(err, &unavailable) {
		t.Fatalf("source lease missing (instance own pool must not be used) err=%v", err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-2' WHERE id = 'acc-source'`); err != nil {
		t.Fatal(err)
	}
	if account, err = store.FindRotationAccount(ctx, "acc-pool"); err != nil || account == nil {
		t.Fatalf("refind instance=%v err=%v", account, err)
	}
	url, err = store.proxyRequestURLForAccount(ctx, account)
	if err != nil || url != "socks5h://pool-user:pool-pass@src.example:1080" {
		t.Fatalf("source pool member url=%q err=%v", url, err)
	}

	// 配置双列（数据损坏）：明确失败，不选边、不直连。
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_profile_id = 'pp-src-member' WHERE id = 'acc-source'`); err != nil {
		t.Fatal(err)
	}
	source, err := store.FindRotationAccount(ctx, "acc-source")
	if err != nil || source == nil {
		t.Fatalf("find source=%v err=%v", source, err)
	}
	if _, err := store.proxyRequestURLForAccount(ctx, source); err == nil || !errors.As(err, &unavailable) {
		t.Fatalf("conflict err=%v", err)
	}
}

// TestKeepalivePoolBindingRefreshesViaLeaseMember：保活链路对池绑定账户使用
// 租约成员出站，且解析全程只读。
func TestKeepalivePoolBindingRefreshesViaLeaseMember(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	if _, err := db.Exec(proxyPoolFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-ka", MemberID: "pp-ka-member", MemberHost: "member.example", MemberPort: 1080,
		LeaseKey: "an-pool", LeasePool: "pool-ka", LeaseMember: "pp-ka-member"})
	seedAccountRow(t, db, accountRowSeed{ID: "an-pool", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-ka' WHERE id = 'an-pool'`); err != nil {
		t.Fatal(err)
	}
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a2","refresh_token":"rt-a2","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if got := exchanger.lastRequest().ProxyURL; got != "socks5h://pool-user:pool-pass@member.example:1080" {
		t.Fatalf("exchanger proxyURL=%q", got)
	}
	if count := countProxyPoolLeases(t, db); count != 1 {
		t.Fatalf("保活解析不得写租约，租约行数=%d", count)
	}
}

// TestKeepalivePoolUnavailableFailsWithoutDirectDial：无租约时保活失败且不发
// 起任何上游调用（不直连、不用旧成员）。
func TestKeepalivePoolUnavailableFailsWithoutDirectDial(t *testing.T) {
	job, db, clock, exchanger := newKeepaliveJobForTest(t)
	if _, err := db.Exec(proxyPoolFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-ka-off", MemberID: "pp-ka-member", MemberHost: "member.example", MemberPort: 1080, LeaseAbsent: true})
	seedAccountRow(t, db, accountRowSeed{ID: "an-pool-no-lease", ProviderCode: "anthropic", Type: "oauth",
		Credentials: map[string]any{"access_token": "at-a", "refresh_token": "rt-a", "expires_at": expiresInMillis(0)}, Now: clock.Now()})
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-ka-off' WHERE id = 'an-pool-no-lease'`); err != nil {
		t.Fatal(err)
	}
	result, err := job.RunOnce(context.Background(), KeepalivePlans()[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 0 || result.Failed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if exchanger.callCount() != 0 {
		t.Fatalf("池租约不可用必须在上游调用前失败，calls=%d", exchanger.callCount())
	}
}

// TestRefreshJobPoolBindingUsesLeaseMember：OpenAI 刷新链路对池绑定账户使用
// 租约成员出站。
func TestRefreshJobPoolBindingUsesLeaseMember(t *testing.T) {
	job, _, db, clock, exchanger := newRefreshJobForTest(t)
	if _, err := db.Exec(proxyPoolFixtureDDL); err != nil {
		t.Fatal(err)
	}
	seedProxyPool(t, db, proxyPoolSeed{PoolID: "pool-openai", MemberID: "pp-openai-member", MemberHost: "member.example", MemberPort: 8080,
		LeaseKey: "acc-pool-openai", LeasePool: "pool-openai", LeaseMember: "pp-openai-member"})
	seedOpenAIOAuthAccount(t, db, "acc-pool-openai", openAICredentials(expiresInMillis(0)), clock.Now())
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'pool-openai' WHERE id = 'acc-pool-openai'`); err != nil {
		t.Fatal(err)
	}
	exchanger.respond = func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-new","refresh_token":"rt-new","expires_in":3600}`}, nil
	}
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("result=%+v", result)
	}
	if got := exchanger.lastRequest().ProxyURL; got != "socks5h://pool-user:pool-pass@member.example:8080" {
		t.Fatalf("exchanger proxyURL=%q", got)
	}
	if count := countProxyPoolLeases(t, db); count != 1 {
		t.Fatalf("刷新解析不得写租约，租约行数=%d", count)
	}
}
