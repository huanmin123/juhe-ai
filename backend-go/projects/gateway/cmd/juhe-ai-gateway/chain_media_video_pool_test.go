package main

// 媒体任务面代理池有效目标解析测试（代理池设计 §5.2 授权实例覆盖 / §5.3 统一
// 解析契约，C10：消除实例 source 覆盖差异）：
//
//   - mediaJobEffectiveProxyBinding 纯函数表：实例行 source 池 > source 代理 >
//     实例自身直接代理（实例自身的池绑定恒不参与）；普通账户自身池/代理；
//     两列同时非空（自身或 source）显式失败不选边。
//   - resolveMediaJobAccountProxyURL PG 面：池绑定走当前租约成员 URL（只读，
//     不重分配）；租约缺失/成员停用/健康 dead 明确不可用，不直连；无绑定直连。
//
// 夹具在 chain_test.go 业务 schema 之上补池三表与 pool_health_status 列
// （生产 P0 schema 已有；此处按生产 DDL 同构补建，加法不影响既有用例）。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

const mediaPoolTestSecret = "chain-test-secret"

// seedMediaPoolSchema 在链级夹具库上补代理池三表与成员健康投影列（生产 DDL
// 同构；幂等）。
func seedMediaPoolSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq`,
		`CREATE TABLE IF NOT EXISTS juhe_business.proxy_pools (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			name TEXT NOT NULL,
			strategy TEXT NOT NULL CHECK (strategy IN ('round_robin', 'weighted')),
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS juhe_business.proxy_pool_members (
			pool_id TEXT NOT NULL REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT,
			proxy_profile_id TEXT NOT NULL REFERENCES juhe_business.proxy_profiles(id) ON DELETE RESTRICT,
			priority INTEGER NOT NULL CHECK (priority >= 0),
			weight INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (pool_id, proxy_profile_id))`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_proxy_leases (
			account_id TEXT PRIMARY KEY REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
			pool_id TEXT NOT NULL REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT,
			proxy_profile_id TEXT NOT NULL REFERENCES juhe_business.proxy_profiles(id) ON DELETE RESTRICT,
			generation BIGINT NOT NULL DEFAULT nextval('juhe_business.account_proxy_lease_generation_seq'),
			assignment_reason TEXT NOT NULL CHECK (assignment_reason IN ('initial', 'member_unavailable', 'member_removed', 'pool_reassigned')),
			assigned_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (pool_id, proxy_profile_id) REFERENCES juhe_business.proxy_pool_members(pool_id, proxy_profile_id) ON DELETE RESTRICT)`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS pool_health_status TEXT`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS pool_health_failure_streak INTEGER NOT NULL DEFAULT 0`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed media pool schema: %v: %s", err, statement)
		}
	}
}

// seedMediaPoolProfile 种一个解密可用的 http 代理档案（任务面成员出口）。
func seedMediaPoolProfile(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	proxyPassword, err := accounts.EncryptJSON(mediaPoolTestSecret, map[string]any{"password": "proxypass"})
	if err != nil {
		t.Fatalf("encrypt proxy password: %v", err)
	}
	if _, err := db.Exec(fixtureBind(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES (?, ?, 'http', '127.0.0.1', 18080, 'proxyuser', ?, 1, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')
		ON CONFLICT (id) DO UPDATE SET password_encrypted = EXCLUDED.password_encrypted, enabled = 1`), id, id, proxyPassword); err != nil {
		t.Fatalf("seed pool profile: %v", err)
	}
}

// seedMediaPool 种池 + 成员行。
func seedMediaPool(t *testing.T, db *sql.DB, poolID string, enabled bool, members map[string]bool) {
	t.Helper()
	if _, err := db.Exec(fixtureBind(`INSERT INTO juhe_business.proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES (?, 'sys', ?, 'round_robin', ?, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')
		ON CONFLICT (id) DO UPDATE SET enabled = EXCLUDED.enabled`), poolID, poolID, enabled); err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	priority := 0
	for profileID, memberEnabled := range members {
		seedMediaPoolProfile(t, db, profileID)
		if _, err := db.Exec(fixtureBind(`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')
			ON CONFLICT (pool_id, proxy_profile_id) DO UPDATE SET enabled = EXCLUDED.enabled, priority = EXCLUDED.priority`),
			poolID, profileID, priority, memberEnabled); err != nil {
			t.Fatalf("seed pool member: %v", err)
		}
		priority++
	}
}

// seedMediaPoolAccount 种最小账户行（租约 FK 需要 accounts.id 存在）。
func seedMediaPoolAccount(t *testing.T, db *sql.DB, accountID string) {
	t.Helper()
	if _, err := db.Exec(fixtureBind(`INSERT INTO accounts
		(id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES (?, 'sys', 'qwen', 'openai', 'v1', ?, 'api_key', 'active', 'x', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')
		ON CONFLICT (id) DO NOTHING`), accountID, accountID); err != nil {
		t.Fatalf("seed pool account: %v", err)
	}
}

// seedMediaPoolLease 写账户租约（generation 自共享序列）。
func seedMediaPoolLease(t *testing.T, db *sql.DB, accountID, poolID, memberID string) {
	t.Helper()
	seedMediaPoolAccount(t, db, accountID)
	if _, err := db.Exec(fixtureBind(`INSERT INTO juhe_business.account_proxy_leases
		(account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
		VALUES (?, ?, ?, nextval('juhe_business.account_proxy_lease_generation_seq'), 'initial', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')
		ON CONFLICT (account_id) DO UPDATE SET pool_id = EXCLUDED.pool_id, proxy_profile_id = EXCLUDED.proxy_profile_id`),
		accountID, poolID, memberID); err != nil {
		t.Fatalf("seed pool lease: %v", err)
	}
}

// TestMediaJobEffectiveProxyBinding 有效绑定纯函数表（§5.2）。
func TestMediaJobEffectiveProxyBinding(t *testing.T) {
	cases := []struct {
		name        string
		account     *chainMediaJobAccount
		wantKind    string
		wantTarget  string
		wantLease   string
		wantBindErr bool
	}{
		{
			name:      "无绑定直连",
			account:   &chainMediaJobAccount{ID: "a1"},
			wantKind:  "none",
			wantLease: "a1",
		},
		{
			name:       "自身直接代理",
			account:    &chainMediaJobAccount{ID: "a2", ProxyProfileID: "prof-1"},
			wantKind:   "profile",
			wantTarget: "prof-1",
			wantLease:  "a2",
		},
		{
			name:       "自身池绑定",
			account:    &chainMediaJobAccount{ID: "a3", ProxyPoolID: "pool-1"},
			wantKind:   "pool",
			wantTarget: "pool-1",
			wantLease:  "a3",
		},
		{
			name:       "实例 source 池覆盖（实例自身直接代理不参与）",
			account:    &chainMediaJobAccount{ID: "a4", SourceAccountID: "src-1", ProxyProfileID: "own-prof", SourcePoolID: "src-pool"},
			wantKind:   "pool",
			wantTarget: "src-pool",
			wantLease:  "src-1",
		},
		{
			name:       "实例 source 代理覆盖（实例自身代理不参与）",
			account:    &chainMediaJobAccount{ID: "a5", SourceAccountID: "src-2", ProxyProfileID: "own-prof", SourceProfileID: "src-prof"},
			wantKind:   "profile",
			wantTarget: "src-prof",
			wantLease:  "src-2",
		},
		{
			name:       "实例无 source 绑定回落实例自身直接代理",
			account:    &chainMediaJobAccount{ID: "a6", SourceAccountID: "src-3", ProxyProfileID: "own-prof"},
			wantKind:   "profile",
			wantTarget: "own-prof",
			wantLease:  "src-3",
		},
		{
			name:        "实例自身两列同时非空是数据损坏（与共享解析契约同判定，不静默选边）",
			account:     &chainMediaJobAccount{ID: "a6b", SourceAccountID: "src-3", ProxyProfileID: "own-prof", ProxyPoolID: "own-pool"},
			wantBindErr: true,
		},
		{
			name:        "实例 source 两列冲突显式失败",
			account:     &chainMediaJobAccount{ID: "a7", SourceAccountID: "src-4", SourceProfileID: "s-p", SourcePoolID: "s-pool"},
			wantBindErr: true,
		},
		{
			name:        "自身两列冲突显式失败",
			account:     &chainMediaJobAccount{ID: "a8", ProxyProfileID: "p", ProxyPoolID: "q"},
			wantBindErr: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			kind, targetID, leaseKey, bindErr := mediaJobEffectiveProxyBinding(testCase.account)
			if testCase.wantBindErr {
				if bindErr == nil {
					t.Fatalf("want conflict error, got kind=%q target=%q", kind, targetID)
				}
				if _, ok := bindErr.(*mediaJobProxyUnavailableError); !ok {
					t.Fatalf("conflict must surface as mediaJobProxyUnavailableError, got %T", bindErr)
				}
				return
			}
			if bindErr != nil {
				t.Fatalf("unexpected bind error: %v", bindErr)
			}
			if kind != testCase.wantKind || targetID != testCase.wantTarget || leaseKey != testCase.wantLease {
				t.Fatalf("binding = (%q,%q,%q), want (%q,%q,%q)", kind, targetID, leaseKey, testCase.wantKind, testCase.wantTarget, testCase.wantLease)
			}
		})
	}
}

// TestMediaJobPoolMemberProxyURL 池绑定租约成员解析（只读，§5.3）。
func TestMediaJobPoolMemberProxyURL(t *testing.T) {
	fixtureDB := newChainFixturePGDatabase(t)
	seedChainBusinessSchema(t, fixtureDB)
	seedMediaPoolSchema(t, fixtureDB)
	runtime := &mediaJobsRuntime{db: fixtureDB, secret: mediaPoolTestSecret}
	ctx := context.Background()

	wantURL := "http://proxyuser:proxypass@127.0.0.1:18080"

	// 池绑定 + 租约 → 租约成员 URL（socks 无认证/带认证均由 chainProxyURLFromRow
	// 归一；本档案带 Basic 凭据）。
	seedMediaPool(t, fixtureDB, "m-pool", true, map[string]bool{"m-member-a": true, "m-member-b": true})
	seedMediaPoolLease(t, fixtureDB, "acc-m1", "m-pool", "m-member-b")
	account := &chainMediaJobAccount{ID: "acc-m1", ProxyPoolID: "m-pool"}
	proxyURL, err := runtime.resolveMediaJobAccountProxyURL(ctx, account)
	if err != nil {
		t.Fatalf("pool lease resolve: %v", err)
	}
	if proxyURL != wantURL {
		t.Fatalf("pool member url = %q, want %q", proxyURL, wantURL)
	}

	// 租约缺失 → 明确不可用（不直连、不在任务面重分配）。
	account = &chainMediaJobAccount{ID: "acc-m2", ProxyPoolID: "m-pool"}
	if proxyURL, err := runtime.resolveMediaJobAccountProxyURL(ctx, account); err == nil {
		t.Fatalf("missing lease: want unavailable error, got url %q", proxyURL)
	} else if _, ok := err.(*mediaJobProxyUnavailableError); !ok {
		t.Fatalf("missing lease error type = %T, want *mediaJobProxyUnavailableError", err)
	}

	// 池停用 → 明确不可用。
	seedMediaPoolLease(t, fixtureDB, "acc-m3", "m-pool", "m-member-a")
	if _, err := fixtureDB.Exec(fixtureBind(`UPDATE juhe_business.proxy_pools SET enabled = FALSE WHERE id = 'm-pool'`)); err != nil {
		t.Fatalf("disable pool: %v", err)
	}
	if _, err := runtime.resolveMediaJobAccountProxyURL(ctx, &chainMediaJobAccount{ID: "acc-m3", ProxyPoolID: "m-pool"}); err == nil {
		t.Fatal("disabled pool: want unavailable error")
	}
	if _, err := fixtureDB.Exec(fixtureBind(`UPDATE juhe_business.proxy_pools SET enabled = TRUE WHERE id = 'm-pool'`)); err != nil {
		t.Fatalf("re-enable pool: %v", err)
	}

	// 租约成员健康 dead → 明确不可用（PG 投影；Redis 缺席走回退语义）。
	if _, err := fixtureDB.Exec(fixtureBind(`UPDATE proxy_profiles SET pool_health_status = 'dead' WHERE id = 'm-member-a'`)); err != nil {
		t.Fatalf("mark member dead: %v", err)
	}
	if _, err := runtime.resolveMediaJobAccountProxyURL(ctx, &chainMediaJobAccount{ID: "acc-m3", ProxyPoolID: "m-pool"}); err == nil {
		t.Fatal("dead member: want unavailable error")
	}
	if _, err := fixtureDB.Exec(fixtureBind(`UPDATE proxy_profiles SET pool_health_status = NULL WHERE id = 'm-member-a'`)); err != nil {
		t.Fatalf("reset member health: %v", err)
	}

	// 实例行 source 覆盖（C10 端到端）：实例行带自身 proxy_profile_id 诱饵
	// （source 无绑定时的回落位；实例自身池绑定不参与解析），source 账户租约
	// 成员才是有效出口。
	seedMediaPoolProfile(t, fixtureDB, "m-own-prof")
	instance := &chainMediaJobAccount{
		ID:              "acc-inst",
		ProxyProfileID:  "m-own-prof",
		SourceAccountID: "acc-m1",
		SourcePoolID:    "m-pool",
	}
	proxyURL, err = runtime.resolveMediaJobAccountProxyURL(ctx, instance)
	if err != nil {
		t.Fatalf("instance source override: %v", err)
	}
	if proxyURL != wantURL {
		t.Fatalf("instance url = %q, want source lease member %q", proxyURL, wantURL)
	}

	// 无绑定 → 直连（空 URL），既有行为不变。
	proxyURL, err = runtime.resolveMediaJobAccountProxyURL(ctx, &chainMediaJobAccount{ID: "acc-plain"})
	if err != nil || proxyURL != "" {
		t.Fatalf("unbound = (%q,%v), want (\"\",nil)", proxyURL, err)
	}

	// 自身直接代理 → 既有 profile 直读行为（含解密）。
	seedMediaPoolProfile(t, fixtureDB, "m-prof-direct")
	proxyURL, err = runtime.resolveMediaJobAccountProxyURL(ctx, &chainMediaJobAccount{ID: "acc-direct", ProxyProfileID: "m-prof-direct"})
	if err != nil || proxyURL != wantURL {
		t.Fatalf("profile binding = (%q,%v), want %q", proxyURL, err, wantURL)
	}

	// 自身直接代理停用 → 明确不可用（既有失败语义，不回落直连）。
	if _, err := fixtureDB.Exec(fixtureBind(`UPDATE proxy_profiles SET enabled = 0 WHERE id = 'm-prof-direct'`)); err != nil {
		t.Fatalf("disable profile: %v", err)
	}
	if _, err := runtime.resolveMediaJobAccountProxyURL(ctx, &chainMediaJobAccount{ID: "acc-direct", ProxyProfileID: "m-prof-direct"}); err == nil {
		t.Fatal("disabled profile: want unavailable error")
	}
}
