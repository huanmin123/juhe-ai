//go:build windows

package accountbalance

// w7c PostgreSQL gated coverage: real dev-PG contract for the jobs store
// (EnsureSchema/CheckSchema, lease renewal/release, outcome CAS), the
// PostgresDirectInputReader (candidate SQL, scan arms, proxy resolution) and
// the Service composition (NewService, RunManual bridge, runCycle).
//
// 存储载体（PLAN-20261024T051925945Z 迁移，原共享 cover 库退役）：
//   - 每测试一个 pgtestkit 独立库，测试结束自动删库；业务行不再需要自清理，
//     也不存在跨测试的 schema 漂移与 w11e 夹具 DROP/重建 juhe_business 的顺序
//     耦合。
//   - juhe_business 表族 DDL 以 golden 手法从 maintenance 权威源文件
//     （pg_schema_business_tables.go）只读提取原文执行：本包与 maintenance 无
//     模块依赖（platform 不得 import maintenance），跨模块文件读取是唯一不
//     复制 DDL 的防漂移手段——2026-10-26 共享 cover 库即因缺
//     credential_fingerprint 暴露 stand-in 列集漂移。执行模型与
//     schema.EnsurePostgres 相同（SET search_path + 原文语句）。
//   - juhe_jobs 命名空间由 prepare 建，四表由生产 EnsureSchema 自建（与
//     accountbalance_pgkit_test.go 同一装配）。
//   - env 缺失或 PG 不可达一律 t.Fatalf：存储测试迁移 PG 后即核心回归面，
//     静默跳过等于假绿（pgtestkit 契约）。
//   - 连接配置由 pgtestkit 自 JUHE_AI_TEST_PG_ADMIN_URL 或 .local dev env
//     解析；任何输出不得携带连接串或密码。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const (
	w7cBalanceSecret   = "w7c-balance-secret"
	w7cOwnerID         = "w7c-pg-owner"
	w7cScanLimitBudget = 30 * time.Second

	// w7cBusinessSchemaSource 是 maintenance 权威业务 DDL 源文件相对本测试
	// 包目录的路径（golden 只读提取，跨模块文件读取而非模块依赖）。
	w7cBusinessSchemaSource = "../../../projects/maintenance/internal/schema/pg_schema_business_tables.go"
)

// w7cBusinessDDLTables 是本包读写面所需的业务表，按建表依赖序排列：accounts
// 的内联外键要求 vendors / proxy_profiles / resource_authorizations /
// account_connections 先建；system_accounts 是 resource_authorizations 的
// 外键目标。只取 CREATE TABLE 原文，守卫 DO 块/触发器不在本包读写面。
var w7cBusinessDDLTables = []string{
	"system_accounts", "vendors", "proxy_profiles", "resource_authorizations", "account_connections", "accounts",
}

// w7cGoldenBusinessDDL 从 maintenance 权威源文件逐表提取 CREATE TABLE 原文
// （SQL 原始字面量不含反引号，提取到首个收尾反引号即语句全文）。任一表缺失
// 即 Fatal：源文件结构调整时必须显式跟进，不得静默降级为本地 stand-in。
func w7cGoldenBusinessDDL(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(w7cBusinessSchemaSource)
	if err != nil {
		t.Fatalf("读取 maintenance 权威业务 schema 源文件失败: %v", err)
	}
	statements := make([]string, 0, len(w7cBusinessDDLTables))
	for _, table := range w7cBusinessDDLTables {
		pattern := regexp.MustCompile("(?s)SQL: `CREATE TABLE IF NOT EXISTS " + table + " \\(.*?`")
		matched := pattern.FindString(string(raw))
		if matched == "" {
			t.Fatalf("golden 提取失败：CREATE TABLE IF NOT EXISTS %s 不在 %s", table, w7cBusinessSchemaSource)
		}
		statements = append(statements, strings.TrimSuffix(strings.TrimPrefix(matched, "SQL: `"), "`"))
	}
	return statements
}

// w7cBusinessGoldenPrepare 返回注入 golden 业务 schema 的 prepare：执行模型
// 与 schema.EnsurePostgres 相同（SET search_path + 原文语句，专用连接保证
// search_path 生效），随后种子 'gpt' vendor——候选 SQL 以 vendor_code 过滤且
// 权威 accounts.vendor_code 带 FK→vendors.code（契约来源：供应商注册表九家
// 真实厂商，'gpt' 保证候选通过 Input.Validate）。
func w7cBusinessGoldenPrepare(statements []string) pgtestkit.PrepareFunc {
	return func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		for _, statement := range statements {
			if _, err := conn.ExecContext(ctx, "SET search_path TO juhe_business, public;\n"+statement); err != nil {
				return fmt.Errorf("golden business DDL 执行失败: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO juhe_business.vendors (id, code, name, enabled, created_at, updated_at)
VALUES ('w7c-vendor-gpt','gpt','GPT',1,$1,$1)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("种子 gpt vendor 失败: %w", err)
		}
		return nil
	}
}

// w7cGateDatabase 打开本测试的 pgtestkit 独立库：golden 业务 schema +
// juhe_jobs 命名空间由 prepare 注入，四张 juhe_jobs 表由生产 EnsureSchema
// 自建（NewService 组装走只读 CheckSchema，必须在夹具内预建）。返回连接池
// 与 DSN；测试结束 pgtestkit 自动关池删库。
func w7cGateDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	statements := w7cGoldenBusinessDDL(t)
	testDB := pgtestkit.NewTestDatabase(t, w7cBusinessGoldenPrepare(statements), func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_jobs`)
		return err
	})
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatalf("accountbalance PG EnsureSchema 失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	testDB.DB.SetMaxOpenConns(4)
	return testDB.DB, testDB.DSN
}

// w7cEmptyDatabase 打开本测试的 pgtestkit 独立空库（零 prepare），供缺失
// schema/关系的失败臂使用。
func w7cEmptyDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t)
	return testDB.DB, testDB.DSN
}

func TestW7CBalancePostgresStoreContract(t *testing.T) {
	_, dsn := w7cGateDatabase(t)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
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
	// 目标 schema 身份列：vendor_code + connection_driver_code（设计 §9.11，
	// provider_code/provider_protocol_profile_id 已删除）。'gpt' 是注册表
	// 真实厂商（golden prepare 已种子 vendors 行），非空 vendor 保证候选通过
	// Input.Validate；accounts.system_account_id 无外键，'w7c-sys' 字面量即
	// 满足 NOT NULL 契约。
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.accounts (
  id, system_account_id, config_revision, dispatch_revision, vendor_code, connection_driver_code,
  protocol_code, protocol_version, name, type, status, credentials_encrypted, schedulable,
  balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, proxy_profile_id,
  deleted_at, authorization_instance_authorization_id, health_check_model, health_check_endpoint_mode,
  created_at, updated_at
) VALUES ($1,'w7c-sys',1,1,'gpt','gpt','chat_completions','v1',$1,'api_key','active',$2,1,$3::integer,$4,$5,NULLIF($6,''),NULL,NULL,'','chat_json',$7,$7)
ON CONFLICT (id) DO UPDATE SET credentials_encrypted=excluded.credentials_encrypted,
  balance_query_enabled=excluded.balance_query_enabled, balance_query_config_json=excluded.balance_query_config_json,
  balance_query_next_refresh_at=excluded.balance_query_next_refresh_at, proxy_profile_id=excluded.proxy_profile_id,
  updated_at=excluded.updated_at`,
		id, credentialJSON, enabled, configJSON, nextRefresh, proxyID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func w7cSeedProxy(t *testing.T, db *sql.DB, id, kind, host string, port int64, username, encryptedPassword string, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
VALUES ($1,'w7c-sys',$1,$2,$3,$4::integer,NULLIF($5,''),NULLIF($6,''),$7::boolean,$8::timestamptz,$8::timestamptz)
ON CONFLICT (id) DO UPDATE SET type=excluded.type, host=excluded.host, port=excluded.port,
  username=excluded.username, password_encrypted=excluded.password_encrypted, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		id, kind, host, port, username, encryptedPassword, enabled, time.Now().UTC()); err != nil {
		t.Fatalf("seed proxy %s: %v", id, err)
	}
}

func TestW7CDirectInputReaderContract(t *testing.T) {
	db, _ := w7cGateDatabase(t)

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
	db, dsn := w7cGateDatabase(t)

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

	storeConfig := StoreConfig{Mode: StorePostgres, PostgresURL: dsn, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2}
	config := RuntimeConfig{
		Enabled: true, OwnerID: w7cOwnerID, Store: storeConfig,
		BusinessPostgresURL: dsn, CredentialSecret: w7cBalanceSecret,
		ScanInterval: time.Hour, OwnerLease: 10 * time.Minute, AccountLease: time.Minute,
		InputTTL: 15 * time.Minute, ProbeTimeout: 15 * time.Second, CycleBudget: 30 * time.Second,
		MaxConcurrency: 4, IOConcurrency: 2, DBConcurrency: 2, DBQueueSize: 8,
		BatchSize: 8, RecoveryBatchSize: 4,
	}
	service, err := NewService(config, nil)
	if err != nil {
		t.Fatalf("NewService on isolated PG: %v", err)
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
	_, coverURL := w7cGateDatabase(t)
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
	_, freshURL := w7cEmptyDatabase(t)
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

func TestW7CBalancePostgresSchemaValidationArms(t *testing.T) {
	db, dsn := w7cEmptyDatabase(t)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()

	// Missing juhe_jobs schema at all.
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "缺少外部 bootstrap") {
		t.Fatalf("missing schema: %v", err)
	}

	// juhe_jobs owned by another role instead of the current jobs role. The
	// pgtestkit database has a single provisioning role, so the mismatch is
	// injected with the pg_database_owner pseudo-role as schema owner（PG14+，
	// 库 owner 即其成员，无需创建集群级角色）.
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_jobs AUTHORIZATION pg_database_owner`); err != nil {
		t.Fatalf("建外主 schema 失败: %v", err)
	}
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "owner 必须是当前 jobs role") {
		t.Fatalf("schema owner mismatch: %v", err)
	}
	// Rebuild owned by the application role.
	if _, err := db.ExecContext(ctx, `DROP SCHEMA juhe_jobs CASCADE`); err != nil {
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
	_, dsn := w7cGateDatabase(t)
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w7cScanLimitBudget)
	defer cancel()
	ownerLease, _, err := store.AcquireOwnerLease(ctx, w7cOwnerID, time.Minute)
	if err != nil {
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

	// Runner.Run dispatches first_probe through the common entry too. The
	// active owner lease from the warm-up must be released first: acquiring
	// never re-grants a live lease, not even to the same owner.
	// （2026-10-25 sqlite 臂退役：committed 列坏类型解码臂为 SQLite 动态类型
	// 专属语义，无 PG 等价断言（D9 裁决）随生产臂删除；first-probe 派发断言
	// 与生产行为无关，改挂本测试既有的 PG cover store，断言逐字节保持。）
	if err := store.ReleaseOwnerLease(ctx, ownerLease); err != nil {
		t.Fatal(err)
	}
	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, func(c *RunnerConfig) {
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
	// （2026-10-25 sqlite 臂退役：非文本 payload 解码、垃圾时间戳租约、
	// RAISE(IGNORE) 触发器三类注入臂为 SQLite 动态类型/触发器专属语义，
	// 无 PG 等价断言（D9 裁决），随生产臂删除。）
	negDB, _ := w7cEmptyDatabase(t)
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
}

func TestW7CServiceRunReturnsOnLateCancel(t *testing.T) {
	db, dsn := w7cGateDatabase(t)
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
		Enabled: true, OwnerID: w7cOwnerID + "-late", Store: StoreConfig{Mode: StorePostgres, PostgresURL: dsn},
		BusinessPostgresURL: dsn, CredentialSecret: w7cBalanceSecret,
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
