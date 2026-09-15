//go:build windows

package accountbalance

// w7c PostgreSQL gated coverage: real dev-PG contract for the jobs store
// (EnsureSchema/CheckSchema, lease renewal/release, outcome CAS), the
// PostgresDirectInputReader (candidate SQL, scan arms, proxy resolution) and
// the Service composition (NewService, RunManual bridge, runCycle).
//
// 隔离铁律（与 gateway 侧 w1 先例一致）：
//   - 只触碰临时子库 juhe_ai_sub2api_dev_w1cover；主库一个字节不动。
//   - 连接配置只从 .local/project-resources/dev/env/shared.env 读取；任何
//     输出不得携带连接串或密码。
//   - env 缺失或 PG 不可达一律 t.Skip；业务行只使用 w7c- 前缀并在结束时
//     删除自己写入的行，绝不 DROP 共享对象。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	w7cCoverDB         = "juhe_ai_sub2api_dev_w1cover"
	w7cEnvPath         = "../../../../.local/project-resources/dev/env/shared.env"
	w7cBalanceSecret   = "w7c-balance-secret"
	w7cOwnerID         = "w7c-pg-owner"
	w7cNegDBName       = "juhe_ai_sub2api_dev_w7cneg"
	w7cScanLimitBudget = 30 * time.Second
)

func w7cSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(w7cEnvPath)
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

// w7cCoverPostgresURL ensures the temp cover database and the two schemas the
// balance package needs exist, then returns an application connection URL.
func w7cCoverPostgresURL(t *testing.T) string {
	t.Helper()
	env := w7cSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || port == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, port))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, w7cCoverDB).Scan(&exists); err != nil {
		t.Fatalf("查询临时子库失败: %v", err)
	}
	if !exists {
		owner := env["DEV_POSTGRES_APP_USERNAME"]
		if owner == "" {
			owner = adminUser
		}
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, w7cCoverDB, owner)); err != nil {
			t.Fatalf("创建临时子库失败: %v", err)
		}
	}
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	tempURL := appURL[:sep+1] + w7cCoverDB

	db, err := sql.Open("pgx", tempURL)
	if err != nil {
		t.Fatalf("打开临时子库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_jobs`); err != nil {
		t.Fatalf("补建 juhe_jobs schema 失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
		t.Fatalf("补建 juhe_business schema 失败: %v", err)
	}
	// Minimal stand-in tables matching the bootstrap contract columns this
	// package reads; no-ops when the bootstrap schema already exists.
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  config_revision integer NOT NULL DEFAULT 1,
  dispatch_revision bigint NOT NULL DEFAULT 1,
  provider_code text NOT NULL,
  provider_protocol_profile_id text NOT NULL DEFAULT '',
  protocol_code text NOT NULL DEFAULT '',
  protocol_version text NOT NULL DEFAULT '',
  name text NOT NULL DEFAULT '',
  type text NOT NULL,
  status text NOT NULL DEFAULT 'active',
  credentials_encrypted text NOT NULL,
  schedulable integer NOT NULL DEFAULT 1,
  balance_query_enabled integer NOT NULL DEFAULT 0,
  balance_query_config_json text NOT NULL DEFAULT '{}',
  balance_query_next_refresh_at text,
  proxy_profile_id text,
  authorization_instance_authorization_id text,
  deleted_at text,
  health_check_model text NOT NULL DEFAULT '',
  health_check_endpoint_mode text NOT NULL DEFAULT 'chat_json',
  created_at text NOT NULL DEFAULT '1970-01-01T00:00:00Z',
  updated_at text NOT NULL DEFAULT '1970-01-01T00:00:00Z'
)`); err != nil {
		t.Fatalf("补建 accounts 表失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS juhe_business.proxy_profiles (
  id text PRIMARY KEY,
  system_account_id text NOT NULL DEFAULT '',
  name text NOT NULL DEFAULT '',
  type text NOT NULL,
  host text NOT NULL,
  port integer NOT NULL,
  username text,
  password_encrypted text,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
  updated_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'
)`); err != nil {
		t.Fatalf("补建 proxy_profiles 表失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS juhe_business.provider_protocol_profiles (
  id text PRIMARY KEY,
  provider_code text NOT NULL DEFAULT 'openai',
  name text NOT NULL DEFAULT '',
  enabled integer NOT NULL DEFAULT 1,
  protocol_code text NOT NULL DEFAULT 'chat_completions',
  protocol_version text NOT NULL DEFAULT 'v1',
  base_url text NOT NULL DEFAULT 'https://w7c.invalid',
  default_health_check_model text NOT NULL DEFAULT '',
  account_types_json text NOT NULL DEFAULT '[]',
  capabilities_json text NOT NULL DEFAULT '{}',
  created_at text NOT NULL DEFAULT '1970-01-01T00:00:00Z',
  updated_at text NOT NULL DEFAULT '1970-01-01T00:00:00Z'
)`); err != nil {
		t.Fatalf("补建 provider_protocol_profiles 表失败: %v", err)
	}
	return tempURL
}

func w7cOpenCoverDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", w7cCoverPostgresURL(t))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("临时子库不可达: %v", err)
	}
	return db
}

func w7cCleanupBalanceRows(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
		defer cancel()
		for _, statement := range []string{
			`DELETE FROM juhe_jobs.account_balance_outcomes WHERE account_id LIKE 'w7c-%' OR request_id LIKE 'w7c-%'`,
			`DELETE FROM juhe_jobs.account_balance_snapshots WHERE account_id LIKE 'w7c-%'`,
			`DELETE FROM juhe_jobs.account_balance_account_leases WHERE account_id LIKE 'w7c-%' OR owner_id LIKE 'w7c-%'`,
			`DELETE FROM juhe_jobs.account_balance_owner_leases WHERE owner_id LIKE 'w7c-%'`,
			`DELETE FROM juhe_business.accounts WHERE id LIKE 'w7c-%'`,
			`DELETE FROM juhe_business.proxy_profiles WHERE id LIKE 'w7c-%'`,
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Logf("清理临时行失败（不影响断言）: %v", err)
			}
		}
	})
}

func TestW7CBalancePostgresStoreContract(t *testing.T) {
	db := w7cOpenCoverDB(t)
	w7cCleanupBalanceRows(t, db)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: w7cCoverPostgresURL(t), PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()

	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema 只读契约: %v", err)
	}

	owner, acquired, err := store.AcquireOwnerLease(ctx, w7cOwnerID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("owner lease: %v %t", err, acquired)
	}
	if _, acquiredAgain, err := store.AcquireOwnerLease(ctx, "w7c-pg-other", time.Minute); err != nil || acquiredAgain {
		t.Fatalf("active owner must fence second owner: %v %t", err, acquiredAgain)
	}
	if ok, err := store.RenewOwnerLease(ctx, owner, time.Minute); err != nil || !ok {
		t.Fatalf("renew owner (pg arm): %t %v", ok, err)
	}
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatalf("release owner: %v", err)
	}

	owner2, acquired, err := store.AcquireOwnerLease(ctx, w7cOwnerID, time.Minute)
	if err != nil || !acquired || owner2.FenceToken <= owner.FenceToken {
		t.Fatalf("takeover bumps fence: %#v %t %v", owner2, acquired, err)
	}
	account, acquired, err := store.AcquireAccountLease(ctx, owner2, "w7c-pg-acct", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("account lease: %v %t", err, acquired)
	}
	if ok, err := store.RenewAccountLease(ctx, owner2, account, time.Minute); err != nil || !ok {
		t.Fatalf("renew account (pg arm): %t %v", ok, err)
	}

	now := time.Now().UTC()
	inserted, err := store.AppendOutcome(ctx, owner2, account, Outcome{
		OutcomeID: "w7c-pg-outcome", RequestID: "w7c-pg-request", AccountID: account.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now,
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.25"},
	})
	if err != nil || !inserted {
		t.Fatalf("outcome: %v %t", err, inserted)
	}
	if outcome, found, err := store.LoadOutcome(ctx, "w7c-pg-outcome"); err != nil || !found || outcome.Snapshot.RemainingUSD != "1.25" {
		t.Fatalf("load outcome: %#v %t %v", outcome, found, err)
	}
	if err := store.ReleaseAccountLease(ctx, owner2, account); err != nil {
		t.Fatalf("release account: %v", err)
	}
}

func w7cSeedCandidate(t *testing.T, db *sql.DB, id, baseURL, credentialJSON, configJSON string, enabled int, nextRefresh any, proxyID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	// The bootstrap accounts table references provider_protocol_profiles;
	// reuse a seeded profile id (or create a minimal one) so the FK holds.
	var profileID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.provider_protocol_profiles ORDER BY id LIMIT 1`).Scan(&profileID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("读取 provider_protocol_profiles 失败: %v", err)
		}
		profileID = "w7c-profile"
		if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.provider_protocol_profiles (id, provider_code, name, enabled, protocol_code, protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
VALUES ('w7c-profile','openai','w7c',1,'chat_completions','v1','https://w7c.invalid','gpt-4o-mini','["api_key"]','{}',$1,$1)
ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("补建 provider_protocol_profile 失败: %v", err)
		}
	}
	// The seeded database keeps an availability-dirty trigger whose FK needs a
	// real system account; a fresh minimal database has neither.
	systemID := "w7c-sys"
	var seededSystem string
	if err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.system_accounts ORDER BY id LIMIT 1`).Scan(&seededSystem); err == nil {
		systemID = seededSystem
	} else if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("读取 system_accounts 失败: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.accounts (
  id, system_account_id, config_revision, dispatch_revision, provider_code, provider_protocol_profile_id,
  protocol_code, protocol_version, name, type, status, credentials_encrypted, schedulable,
  balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, proxy_profile_id,
  deleted_at, authorization_instance_authorization_id, health_check_model, health_check_endpoint_mode,
  created_at, updated_at
) VALUES ($1,$9,1,1,'openai',$8,'chat_completions','v1',$1,'api_key','active',$2,1,$3,$4,$5,NULLIF($6,''),NULL,NULL,'','chat_json',$7,$7)
ON CONFLICT (id) DO UPDATE SET credentials_encrypted=excluded.credentials_encrypted,
  balance_query_enabled=excluded.balance_query_enabled, balance_query_config_json=excluded.balance_query_config_json,
  balance_query_next_refresh_at=excluded.balance_query_next_refresh_at, proxy_profile_id=excluded.proxy_profile_id,
  updated_at=excluded.updated_at`,
		id, credentialJSON, enabled, configJSON, nextRefresh, proxyID, time.Now().UTC().Format(time.RFC3339Nano), profileID, systemID); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func w7cSeedProxy(t *testing.T, db *sql.DB, id, kind, host string, port int64, username, encryptedPassword string, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
VALUES ($1,'w7c-sys',$1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$8)
ON CONFLICT (id) DO UPDATE SET type=excluded.type, host=excluded.host, port=excluded.port,
  username=excluded.username, password_encrypted=excluded.password_encrypted, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		id, kind, host, port, username, encryptedPassword, enabled, time.Now().UTC()); err != nil {
		t.Fatalf("seed proxy %s: %v", id, err)
	}
}

func TestW7CDirectInputReaderContract(t *testing.T) {
	db := w7cOpenCoverDB(t)
	w7cCleanupBalanceRows(t, db)

	credential, err := NewCredentialEnvelope(w7cBalanceSecret, "api_key", map[string]string{"api_key": "sk-w7c", "base_url": "https://w7c-upstream.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	password, err := NewCredentialEnvelope(w7cBalanceSecret, "proxy_password", map[string]string{"password": "w7c-pass"})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)

	w7cSeedCandidate(t, db, "w7c-acct-due", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "")
	w7cSeedCandidate(t, db, "w7c-acct-first", "", credential.Ciphertext, `{}`, 0, past, "")
	w7cSeedCandidate(t, db, "w7c-acct-recovery", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, nil, "")
	w7cSeedProxy(t, db, "w7c-proxy-http", "http", "10.0.0.8", 8080, "w7c-user", password.Ciphertext, true)
	w7cSeedCandidate(t, db, "w7c-acct-proxy-http", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-http")
	w7cSeedProxy(t, db, "w7c-proxy-socks", "socks5", "10.0.0.9", 1080, "", "", true)
	w7cSeedCandidate(t, db, "w7c-acct-proxy-socks", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-socks")
	w7cSeedProxy(t, db, "w7c-proxy-bad", "gre", "10.0.0.10", 47, "", "", true)
	w7cSeedCandidate(t, db, "w7c-acct-proxy-bad", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-bad")
	w7cSeedProxy(t, db, "w7c-proxy-off", "http", "10.0.0.11", 8080, "", "", false)
	w7cSeedCandidate(t, db, "w7c-acct-proxy-off", "", credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "w7c-proxy-off")
	w7cSeedCandidate(t, db, "w7c-acct-badcred", "", "not-an-envelope", `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "")
	w7cSeedCandidate(t, db, "w7c-acct-badconfig", "", credential.Ciphertext, `{"adapter":5}`, 1, past, "")
	w7cSeedCandidate(t, db, "w7c-acct-nobaseurl", "", `{"api_key":"sk-w7c"}`, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "")

	reader, err := NewPostgresDirectInputReader(db, w7cBalanceSecret, 15*time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("CheckContract: %v", err)
	}

	due, err := reader.LoadDue(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	gotDue := map[string]Candidate{}
	for _, candidate := range due {
		gotDue[candidate.AccountID] = candidate
	}
	for _, id := range []string{"w7c-acct-due", "w7c-acct-proxy-http", "w7c-acct-proxy-socks"} {
		if _, ok := gotDue[id]; !ok {
			t.Fatalf("due candidates missing %s: %v", id, keysOf(gotDue))
		}
	}
	for _, id := range []string{"w7c-acct-proxy-bad", "w7c-acct-proxy-off", "w7c-acct-badcred", "w7c-acct-badconfig", "w7c-acct-nobaseurl"} {
		if _, ok := gotDue[id]; ok {
			t.Fatalf("malformed candidate %s must be skipped", id)
		}
	}
	if proxy := gotDue["w7c-acct-proxy-http"].Proxy; proxy == nil {
		t.Fatal("http proxy candidate must carry an envelope")
	} else {
		var payload struct {
			URL string `json:"url"`
		}
		if err := openCredential(w7cBalanceSecret, *proxy, "proxy_url", &payload); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(payload.URL, "http://w7c-user:") || !strings.Contains(payload.URL, "@10.0.0.8:8080") {
			t.Fatalf("proxy url: %q", payload.URL)
		}
	}
	if proxy := gotDue["w7c-acct-proxy-socks"].Proxy; proxy == nil {
		t.Fatal("socks candidate must carry an envelope")
	} else {
		var payload struct {
			URL string `json:"url"`
		}
		if err := openCredential(w7cBalanceSecret, *proxy, "proxy_url", &payload); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(payload.URL, "socks5h://10.0.0.9:1080") {
			t.Fatalf("socks5 aliasing: %q", payload.URL)
		}
	}

	first, err := reader.LoadFirstProbe(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || first[0].AccountID != "w7c-acct-first" || !first[0].FirstProbe {
		t.Fatalf("first probe: %#v", first)
	}

	recovery, err := reader.LoadRecovery(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	foundRecovery := false
	for _, candidate := range recovery {
		if candidate.AccountID == "w7c-acct-recovery" {
			foundRecovery = candidate.Recovery && candidate.NextRefreshAt == nil
		}
	}
	if !foundRecovery {
		t.Fatalf("recovery: %#v", recovery)
	}

	account, err := reader.LoadAccount(ctx, "w7c-acct-due")
	if err != nil || account.AccountID != "w7c-acct-due" {
		t.Fatalf("load account: %#v %v", account, err)
	}
	if _, err := reader.LoadAccount(ctx, "w7c-acct-missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing account: %v", err)
	}
	if _, err := reader.LoadAccount(ctx, " "); err == nil {
		t.Fatal("empty id must fail")
	}

	if _, err := reader.LoadDue(ctx, 0); err == nil {
		t.Fatal("limit below 1 must fail")
	}
	if _, err := reader.LoadDue(ctx, 5097); err == nil {
		t.Fatal("limit above 5096 must fail")
	}

	if _, err := NewPostgresDirectInputReader(nil, w7cBalanceSecret, time.Minute, nil); err == nil {
		t.Fatal("nil db must fail")
	}
	if _, err := NewPostgresDirectInputReader(db, " ", time.Minute, nil); err == nil {
		t.Fatal("empty secret must fail")
	}
	if _, err := NewPostgresDirectInputReader(db, w7cBalanceSecret, 16*time.Minute, nil); err == nil {
		t.Fatal("ttl above 15m must fail")
	}
	if _, err := NewPostgresDirectInputReader(db, w7cBalanceSecret, time.Minute, nil); err != nil {
		t.Fatalf("nil now defaults: %v", err)
	}
}

func keysOf(values map[string]Candidate) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestW7CServiceComposeAndManualBridgeOnPostgres(t *testing.T) {
	db := w7cOpenCoverDB(t)
	w7cCleanupBalanceRows(t, db)

	// Local upstream only; nothing leaves the machine.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"unit":"USD","remaining":"9.75"}`))
	}))
	defer upstream.Close()

	credential, err := NewCredentialEnvelope(w7cBalanceSecret, "api_key", map[string]string{"api_key": "sk-w7c", "base_url": upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	w7cSeedCandidate(t, db, "w7c-svc-due", upstream.URL, credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "")
	w7cSeedCandidate(t, db, "w7c-svc-first", upstream.URL, credential.Ciphertext, `{}`, 0, past, "")
	w7cSeedCandidate(t, db, "w7c-svc-recovery", upstream.URL, credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, nil, "")

	storeConfig := StoreConfig{Mode: StorePostgres, PostgresURL: w7cCoverPostgresURL(t), PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2}
	config := RuntimeConfig{
		Enabled: true, OwnerID: w7cOwnerID, Store: storeConfig,
		BusinessPostgresURL: w7cCoverPostgresURL(t), CredentialSecret: w7cBalanceSecret,
		ScanInterval: time.Hour, OwnerLease: 10 * time.Minute, AccountLease: time.Minute,
		InputTTL: 15 * time.Minute, ProbeTimeout: 15 * time.Second, CycleBudget: 30 * time.Second,
		MaxConcurrency: 4, IOConcurrency: 2, DBConcurrency: 2, DBQueueSize: 8,
		BatchSize: 8, RecoveryBatchSize: 4,
	}
	service, err := NewService(config, nil)
	if err != nil {
		t.Fatalf("NewService on temp PG: %v", err)
	}
	defer service.Close()
	if service.Ready() {
		t.Fatal("service must not be ready before the first cycle")
	}

	// One full recovery+due+first-probe cycle against the local upstream.
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := service.runCycle(ctx); err != nil {
		t.Fatalf("runCycle: %v", err)
	}
	snapshot, found, err := service.store.LoadSnapshot(ctx, "w7c-svc-due")
	if err != nil || !found || snapshot.Snapshot.Status != StatusFresh || snapshot.Snapshot.RemainingUSD != "9.750000" {
		t.Fatalf("cycle snapshot: %#v %t %v", snapshot, found, err)
	}

	// Manual bridge happy path.
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	manualInput := Input{
		AccountID: "w7c-svc-manual", SystemAccountID: "w7c-sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: upstream.URL, Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Credential: credential, Trigger: TriggerManual,
		IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), NextRefreshAt: &due,
	}
	record, report, err := service.RunManual(ctx, manualInput)
	if err != nil || report.Executed != 1 {
		t.Fatalf("manual bridge: %#v %v", report, err)
	}
	if record.Snapshot.Status != StatusFresh || record.Snapshot.RemainingUSD != "9.750000" {
		t.Fatalf("manual record: %#v", record)
	}

	// Replay of the same input resolves through the immutable outcome.
	record, report, err = service.RunManual(ctx, manualInput)
	if err != nil || record.Snapshot.Status != StatusFresh {
		t.Fatalf("manual replay: %#v %v", record, err)
	}

	// A stale manual refresh (older input version) reports ErrOutcomeStale.
	staleInput := manualInput
	staleInput.InputVersion = 0
	if _, _, err := service.RunManual(ctx, staleInput); err == nil {
		t.Fatal("invalid stale input must fail")
	}
	advancedInput := manualInput
	advancedInput.AccountID = "w7c-svc-manual"
	advancedInput.InputVersion = 5
	if _, _, err := service.RunManual(ctx, advancedInput); err != nil {
		t.Fatalf("advanced manual: %v", err)
	}
	regressedInput := manualInput
	regressedInput.InputVersion = 2
	if _, _, err := service.RunManual(ctx, regressedInput); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("regressed manual must be stale: %v", err)
	}

	// A held account lease skips the run and reports ErrAccountLeaseHeld.
	owner, acquired, err := service.store.AcquireOwnerLease(ctx, w7cOwnerID+"-manual", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("manual owner: %v %t", err, acquired)
	}
	if _, acquired, err := service.store.AcquireAccountLease(ctx, owner, "w7c-svc-manual", time.Minute); err != nil || !acquired {
		t.Fatalf("manual account lease: %v %t", err, acquired)
	}
	if _, _, err := service.RunManual(ctx, advancedInput); !errors.Is(err, ErrAccountLeaseHeld) {
		t.Fatalf("held lease must surface: %v", err)
	}

	// Run with a canceled context fails the first cycle and restores not-ready.
	canceled, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	if err := service.Run(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled run: %v", err)
	}
	if service.Ready() {
		t.Fatal("failed cycle must clear readiness")
	}
}

// TestW7CServiceComposeFailureArms covers the NewService fail-closed arms:
// schema check failure, business ping failure and runner capacity rejection.
// Each arm must close the resources it opened.
func TestW7CServiceComposeFailureArms(t *testing.T) {
	coverURL := w7cCoverPostgresURL(t)
	base := RuntimeConfig{
		Enabled: true, OwnerID: w7cOwnerID, Store: StoreConfig{Mode: StorePostgres, PostgresURL: coverURL},
		CredentialSecret: w7cBalanceSecret, OwnerLease: 10 * time.Minute, AccountLease: time.Minute,
		InputTTL: 15 * time.Minute, ProbeTimeout: 15 * time.Second, CycleBudget: 30 * time.Second,
	}

	// Business pool cannot be pinged.
	pingFail := base
	pingFail.BusinessPostgresURL = "postgres://127.0.0.1:1/nope?sslmode=disable&connect_timeout=2"
	if _, err := NewService(pingFail, nil); err == nil {
		t.Fatal("unreachable business pool must fail composition")
	}

	// Store schema check fails on a database without the juhe_jobs schema.
	freshURL := w7cCreateNegativeDatabase(t)
	schemaFail := base
	schemaFail.Store = StoreConfig{Mode: StorePostgres, PostgresURL: freshURL}
	schemaFail.BusinessPostgresURL = freshURL
	if _, err := NewService(schemaFail, nil); err == nil || !strings.Contains(err.Error(), "juhe_jobs") {
		t.Fatalf("missing jobs schema must fail composition: %v", err)
	}

	// Runner capacity bound fires after every other composition step passed.
	capacity := base
	capacity.BusinessPostgresURL = coverURL
	capacity.MaxConcurrency = maxBalanceRunnerConcurrency + 1
	if _, err := NewService(capacity, nil); err == nil || !strings.Contains(err.Error(), "最大并发") {
		t.Fatalf("runner capacity must fail composition: %v", err)
	}
}

// w7cCreateNegativeDatabase creates a short-lived dedicated database (created
// and force-dropped by this test only) for schema-validation failure arms.
func w7cCreateNegativeDatabase(t *testing.T) string {
	t.Helper()
	env := w7cSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || port == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, port))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+w7cNegDBName+` WITH (FORCE)`); err != nil {
		t.Fatalf("清理负臂临时库失败: %v", err)
	}
	owner := env["DEV_POSTGRES_APP_USERNAME"]
	if owner == "" {
		owner = adminUser
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+w7cNegDBName+` OWNER `+owner); err != nil {
		t.Fatalf("创建负臂临时库失败: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, `DROP DATABASE IF EXISTS `+w7cNegDBName+` WITH (FORCE)`)
	})
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	return appURL[:sep+1] + w7cNegDBName
}

func TestW7CBalancePostgresSchemaValidationArms(t *testing.T) {
	freshURL := w7cCreateNegativeDatabase(t)
	db, err := sql.Open("pgx", freshURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("负臂临时库不可达: %v", err)
	}

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: freshURL})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Missing juhe_jobs schema at all.
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "缺少外部 bootstrap") {
		t.Fatalf("missing schema: %v", err)
	}

	// juhe_jobs owned by the admin role instead of the application role.
	env := w7cSharedEnv(t)
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", env["DEV_POSTGRES_ADMIN_USERNAME"], env["DEV_POSTGRES_ADMIN_PASSWORD"], env["DEV_POSTGRES_HOST"], env["DEV_POSTGRES_DIRECT_PORT"], w7cNegDBName))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA juhe_jobs`); err != nil {
		t.Fatalf("admin 建 schema 失败: %v", err)
	}
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "owner 必须是当前 jobs role") {
		t.Fatalf("schema owner mismatch: %v", err)
	}
	// Rebuild owned by the application role.
	if _, err := admin.ExecContext(ctx, `DROP SCHEMA juhe_jobs CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_jobs`); err != nil {
		t.Fatal(err)
	}

	// Tables missing entirely: CheckSchema must enumerate them.
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "缺少预置表") {
		t.Fatalf("missing tables: %v", err)
	}

	// All four tables must exist before column-level validation runs; plant
	// three correct tables and one with a wrong column type.
	if _, err := db.ExecContext(ctx, `CREATE TABLE juhe_jobs.account_balance_owner_leases (lease_key text PRIMARY KEY, owner_id text NOT NULL, fence_token text NOT NULL, lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE juhe_jobs.account_balance_account_leases (account_id text PRIMARY KEY, owner_id text NOT NULL, fence_token bigint NOT NULL, lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL)`,
		`CREATE TABLE juhe_jobs.account_balance_snapshots (account_id text PRIMARY KEY, input_version bigint NOT NULL, config_revision bigint NOT NULL, "trigger" text NOT NULL, snapshot_json jsonb NOT NULL, next_refresh_at timestamptz, updated_at timestamptz NOT NULL)`,
		`CREATE TABLE juhe_jobs.account_balance_outcomes (outcome_id text PRIMARY KEY, request_id text NOT NULL UNIQUE, account_id text NOT NULL, input_version bigint NOT NULL, config_revision bigint NOT NULL, "trigger" text NOT NULL, observed_at timestamptz NOT NULL, payload jsonb NOT NULL, committed boolean NOT NULL)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "类型不符") {
		t.Fatalf("column type mismatch: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.account_balance_owner_leases ALTER COLUMN fence_token TYPE bigint USING fence_token::bigint`); err != nil {
		t.Fatal(err)
	}
	// With every table type-correct, dropping a required column triggers the
	// missing-column arm.
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.account_balance_outcomes DROP COLUMN committed`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "缺少列") {
		t.Fatalf("missing column: %v", err)
	}
	// Re-adding it as nullable triggers the NOT NULL arm.
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.account_balance_outcomes ADD COLUMN committed boolean`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_jobs.account_balance_owner_leases ALTER COLUMN lease_until DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "必须为 NOT NULL") {
		t.Fatalf("nullable column: %v", err)
	}

	// A canceled context fails the read-only schema transactions.
	canceled, cancelCheck := context.WithCancel(context.Background())
	cancelCheck()
	if err := store.CheckSchema(canceled); err == nil {
		t.Fatal("canceled schema check must fail")
	}
	if err := store.EnsureSchema(canceled); err == nil {
		t.Fatal("canceled ensure must fail")
	}
}

func TestW7CBalancePostgresLeaseContextArms(t *testing.T) {
	db := w7cOpenCoverDB(t)
	w7cCleanupBalanceRows(t, db)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: w7cCoverPostgresURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if _, _, err := store.AcquireOwnerLease(ctx, w7cOwnerID, time.Minute); err != nil {
		t.Fatalf("warm owner lease: %v", err)
	}

	canceled, cancelLease := context.WithCancel(context.Background())
	cancelLease()
	if _, _, err := store.AcquireOwnerLease(canceled, "w7c-pg-canceled", time.Minute); err == nil {
		t.Fatal("canceled owner lease must fail")
	}
	if _, err := store.RenewOwnerLease(canceled, OwnerLease{OwnerID: w7cOwnerID, FenceToken: 1}, time.Minute); err == nil {
		t.Fatal("canceled renew must fail")
	}

	// existingOutcomeMatchesTx committed decode arm: SQLite's dynamic typing
	// allows storing an undecodable committed value in the INTEGER column.
	sqlite := w7cNewSQLiteStore(t)
	sqliteOwner := w7cOwner(t, sqlite, "w7c-committed-owner")
	sqliteAccount := w7cAccount(t, sqlite, sqliteOwner, "w7c-committed-acct")
	if _, err := sqlite.AppendOutcome(context.Background(), sqliteOwner, sqliteAccount, Outcome{
		OutcomeID: "w7c-committed-1", RequestID: "w7c-committed-req", AccountID: sqliteAccount.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.db.Exec(`UPDATE account_balance_outcomes SET committed='weird' WHERE request_id='w7c-committed-req'`); err != nil {
		t.Fatal(err)
	}
	conflict := Outcome{
		OutcomeID: "w7c-committed-2", RequestID: "w7c-committed-req", AccountID: sqliteAccount.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(),
	}
	if _, err := sqlite.AppendOutcome(context.Background(), sqliteOwner, sqliteAccount, conflict); err == nil || !strings.Contains(err.Error(), "未知 SQL 布尔类型") {
		t.Fatalf("undecodable committed value: %v", err)
	}

	// Runner.Run dispatches first_probe through the common entry too. The
	// active owner lease from the outcome section must be released first:
	// acquiring never re-grants a live lease, not even to the same owner.
	if err := sqlite.ReleaseOwnerLease(context.Background(), sqliteOwner); err != nil {
		t.Fatal(err)
	}
	runner := w7cNewRunner(t, sqlite, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, func(c *RunnerConfig) {
		c.OwnerID = "w7c-committed-owner"
	})
	credential, err := NewCredentialEnvelope(w7cBalanceSecret, "api_key", map[string]string{"api_key": "sk-w7c"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	first := Candidate{
		AccountID: "w7c-run-first", SystemAccountID: "w7c-sys", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BalanceEnabled: false, FirstProbe: true, APIKeyCount: 1, APIKey: credential, BaseURL: "https://example.test",
		Config: QueryConfig{}, IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due,
	}
	report, err := runner.Run(ctx, TriggerFirstProbe, []Candidate{first})
	if err != nil {
		t.Fatalf("first probe via Run: %v", err)
	}
	if report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("first probe report: %#v", report)
	}
}

func TestW7CBalanceRemainingErrorArms(t *testing.T) {
	// CheckContract on a database without the business relations.
	freshURL := w7cCreateNegativeDatabase(t)
	negDB, err := sql.Open("pgx", freshURL)
	if err != nil {
		t.Fatal(err)
	}
	defer negDB.Close()
	reader, err := NewPostgresDirectInputReader(negDB, w7cBalanceSecret, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if err := reader.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "缺少 juhe_business.accounts") {
		t.Fatalf("missing business relation: %v", err)
	}
	canceled, cancelCheck := context.WithCancel(context.Background())
	cancelCheck()
	if err := reader.CheckContract(canceled); err == nil {
		t.Fatal("canceled contract check must fail")
	}

	// A non-text payload decodes through the JSON fallback and then fails the
	// identity check.
	sqlite := w7cNewSQLiteStore(t)
	owner := w7cOwner(t, sqlite, "w7c-arm-owner")
	account := w7cAccount(t, sqlite, owner, "w7c-arm-acct")
	if _, err := sqlite.AppendOutcome(context.Background(), owner, account, Outcome{
		OutcomeID: "w7c-arm-outcome", RequestID: "w7c-arm-request", AccountID: account.AccountID,
		InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.db.Exec(`UPDATE account_balance_outcomes SET payload=12345 WHERE outcome_id='w7c-arm-outcome'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.LoadOutcome(context.Background(), "w7c-arm-outcome"); err == nil {
		t.Fatal("non-text payload must fail the identity check")
	}

	// A garbage owner lease timestamp fails the owner verification.
	if _, err := sqlite.db.Exec(`UPDATE account_balance_owner_leases SET lease_until='garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.AcquireAccountLease(context.Background(), owner, "w7c-arm-acct-2", time.Minute); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("garbage lease timestamp must lose the owner: %v", err)
	}

	// Restore a parseable, live owner lease before the next section.
	if _, err := sqlite.db.Exec(`UPDATE account_balance_owner_leases SET lease_until=? WHERE lease_key='account-balance-owner'`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	// A trigger that silently ignores the committed flag surfaces the
	// missing-marker error after a successful snapshot write.
	if _, err := sqlite.db.Exec(`CREATE TRIGGER w7c_ignore_committed BEFORE UPDATE ON account_balance_outcomes WHEN new.committed=1 BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	account2 := w7cAccount(t, sqlite, owner, "w7c-arm-acct-3")
	if _, err := sqlite.AppendOutcome(context.Background(), owner, account2, Outcome{
		OutcomeID: "w7c-arm-outcome-3", RequestID: "w7c-arm-request-3", AccountID: account2.AccountID,
		InputVersion: 9, ConfigRevision: 9, Trigger: TriggerPeriodic, ObservedAt: time.Now().UTC(),
		Snapshot: Snapshot{Status: StatusFresh},
	}); err == nil || !strings.Contains(err.Error(), "committed 标记缺失") {
		t.Fatalf("ignored committed marker: %v", err)
	}
	sqlite.db.Exec(`DROP TRIGGER w7c_ignore_committed`)
}

func TestW7CServiceRunReturnsOnLateCancel(t *testing.T) {
	db := w7cOpenCoverDB(t)
	w7cCleanupBalanceRows(t, db)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"unit":"USD","remaining":"1"}`))
	}))
	defer upstream.Close()
	credential, err := NewCredentialEnvelope(w7cBalanceSecret, "api_key", map[string]string{"api_key": "sk-w7c", "base_url": upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	w7cSeedCandidate(t, db, "w7c-run-due", upstream.URL, credential.Ciphertext, `{"adapter":"builtin","intervalMinutes":5}`, 1, past, "")

	config := RuntimeConfig{
		Enabled: true, OwnerID: w7cOwnerID + "-late", Store: StoreConfig{Mode: StorePostgres, PostgresURL: w7cCoverPostgresURL(t)},
		BusinessPostgresURL: w7cCoverPostgresURL(t), CredentialSecret: w7cBalanceSecret,
		ScanInterval: time.Hour, OwnerLease: 10 * time.Minute, AccountLease: time.Minute,
		InputTTL: 15 * time.Minute, ProbeTimeout: 15 * time.Second, CycleBudget: 30 * time.Second,
		MaxConcurrency: 2, IOConcurrency: 1, DBConcurrency: 1, DBQueueSize: 4,
		BatchSize: 4, RecoveryBatchSize: 2,
	}
	service, err := NewService(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	ctx, cancelRun := context.WithCancel(context.Background())
	deadline := time.Now().Add(w7cScanLimitBudget)
	go func() {
		<-time.After(w7cScanLimitBudget)
		cancelRun()
	}()
	runErr := make(chan error, 1)
	go func() { runErr <- service.Run(ctx) }()
	for !service.Ready() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancelRun()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancel: %v", err)
	}
	if !service.Ready() {
		t.Fatal("successful cycle must mark the service ready")
	}
}
