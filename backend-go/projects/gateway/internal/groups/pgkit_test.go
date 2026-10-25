package groups

// pgkit_test.go 是 groups 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，groups 测试迁移切片）：
// pgtestkit 每测试独立库 + 生产 store 的 PG 臂（NewStore postgres=true，
// juhe_business schema 限定 + $n 占位符，与生产 PG 装配同一方言）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：HTTP/store 级测试走本文件
//     夹具，测试体断言逐字节保持；夹具 seed/校验手写 SQL 经 fixtureSQL 对齐
//     生产 PG 臂（juhe_business 限定 + $n）。schema 与 maintenance 生产 DDL
//     精确对齐（pg_schema_business_tables.go / pg_schema_stats.go），按
//     statreads 夹具先例放宽两处：不建 FOREIGN KEY（store 删除级联为显式
//     DELETE，seed 行不满足生产外键链）；只保留本包与 authsys/authz PG 臂
//     实际读写的列集（route_strategies / accounts 为放宽列集，其余表为生产
//     全列）。行为性约束逐字保留：idx_groups_owner_name_unique（重名 409
//     映射依赖该约束名）、super_group_members 的 UNIQUE/CHECK、grants 行的
//     grantee_type CHECK、quota 绑定与 health outbox 的生产 CHECK。
//   - 组B（SQLite-only 生产臂为被测对象）：w11f 四文件（scripted 故障注入
//     连接器族）、TestGroupRefreshStatsAfterWriteNeverFailsTheRequestOnPostgres
//     的 SQLite 同步臂段（关闭句柄故障注入）、stats_reader_pg_dialect_test /
//     usage_reader_pg_dialect_test 的 SQLite 裸表名分支段——按 D17 不提前迁
//     移也不提前删除，随生产 sqlite 臂删除批次同批退役。
//   - 纯函数测试与 fake 端口（fakeGroupStatsReader / fakeConcurrencyReader /
//     fakeGroupUsageSource）不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// groupsPGFixtureSchema 是 groups/authsys/authz 面测试的 minimal PG schema。
// 列集与类型照抄 maintenance 生产 DDL（pg_schema_business_tables.go /
// pg_schema_stats.go），FK 按夹具先例放宽（见文件头注释）；生产索引
// idx_groups_owner_name_unique 必须同名创建——duplicateGroupNameError 的
// PG 臂依赖报错文本中的约束名映射 409。
const groupsPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE SCHEMA IF NOT EXISTS juhe_stats;
	CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (
      id text PRIMARY KEY,
      username text NOT NULL UNIQUE,
      display_name text NOT NULL,
      description text,
      role text NOT NULL DEFAULT 'user',
      status text NOT NULL DEFAULT 'active',
      password_hash text NOT NULL,
      must_change_password integer NOT NULL DEFAULT 0,
      image_generation_enabled integer NOT NULL DEFAULT 0,
      ai_account_limit integer CHECK (ai_account_limit BETWEEN 0 AND 1000000),
      expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100),
      request_limits_json text,
      last_login_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      expires_at text NOT NULL,
      created_at text NOT NULL,
      last_seen_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      deleted_at text,
      resource_owner_system_account_id text NOT NULL DEFAULT '',
      authorization_instance_authorization_id text,
      authorization_instance_source_account_id text,
      created_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.groups (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      description text,
      enabled integer NOT NULL DEFAULT 1,
      is_default integer NOT NULL DEFAULT 0,
      group_type text NOT NULL DEFAULT 'personal',
      group_kind text NOT NULL DEFAULT 'standard' CHECK (group_kind IN ('standard','super')),
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_name_unique
      ON juhe_business.groups (system_account_id, name);
	CREATE TABLE IF NOT EXISTS juhe_business.super_group_members (
      id text PRIMARY KEY,
      super_group_id text NOT NULL,
      member_group_id text NOT NULL,
      priority integer NOT NULL CHECK (priority >= 0),
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CONSTRAINT super_group_members_unique_member UNIQUE (super_group_id, member_group_id),
      CONSTRAINT super_group_members_unique_priority UNIQUE (super_group_id, priority)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      account_id text NOT NULL,
      account_authorization_id text,
      enabled integer NOT NULL DEFAULT 1,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (group_id, account_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_authorization_settings (
      authorization_id text PRIMARY KEY,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      enabled integer NOT NULL DEFAULT 1,
      group_type text NOT NULL DEFAULT 'personal',
      scheduling_policy_json text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
      id text PRIMARY KEY,
      resource_type text NOT NULL,
      resource_id text NOT NULL,
      resource_owner_system_account_id text NOT NULL,
      grantee_system_account_id text NOT NULL,
      scope text NOT NULL DEFAULT 'use',
      status text NOT NULL DEFAULT 'active',
      effective_source_type text,
      effective_source_team_id text,
      activated_at text,
      last_source_changed_at text,
      remark text,
      expires_at text,
      limits_json text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      revoked_reason text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_sources (
      id text PRIMARY KEY,
      authorization_id text NOT NULL,
      source_type text NOT NULL,
      source_team_id text,
      status text NOT NULL DEFAULT 'active',
      activated_at text,
      ended_at text,
      ended_reason text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
      id text PRIMARY KEY,
      resource_type text NOT NULL,
      resource_id text NOT NULL,
      resource_owner_system_account_id text NOT NULL,
      grantee_type text NOT NULL,
      grantee_system_account_id text,
      grantee_team_id text,
      scope text NOT NULL DEFAULT 'use',
      status text NOT NULL DEFAULT 'active',
      remark text,
      expires_at text,
      limits_json text,
      created_by text NOT NULL,
      created_at text NOT NULL,
      revoked_by text,
      revoked_at text,
      updated_at text NOT NULL,
      CHECK (
        (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
        OR
        (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
      )
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
      id text PRIMARY KEY,
      name text NOT NULL,
      description text,
      status text NOT NULL DEFAULT 'active',
      created_by text NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.route_strategies (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      name text NOT NULL,
      status text NOT NULL DEFAULT 'active'
    );
	CREATE TABLE IF NOT EXISTS juhe_business.route_strategy_groups (
      id text PRIMARY KEY,
      route_strategy_id text NOT NULL,
      system_account_id text NOT NULL,
      group_id text NOT NULL,
      priority integer NOT NULL DEFAULT 1,
      weight integer NOT NULL DEFAULT 1,
      status text NOT NULL DEFAULT 'active',
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
      system_account_id text NOT NULL,
      scope_type text NOT NULL,
      scope_id text NOT NULL,
      source_type text NOT NULL,
      source_id text NOT NULL,
      window_hours integer NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720),
      CHECK (scope_type IN ('api_key', 'account_authorization', 'group_authorization', 'account_authorization_team', 'group_authorization_team')),
      CHECK (source_type IN ('api_key', 'resource_authorization_grant')),
      PRIMARY KEY (system_account_id, scope_type, scope_id),
      UNIQUE (source_type, source_id, system_account_id, scope_type, scope_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_versions (
      account_id text PRIMARY KEY,
      current_version integer NOT NULL CHECK (current_version >= 1),
      reserved_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_outbox (
      event_id text PRIMARY KEY,
      account_id text NOT NULL,
      input_version integer NOT NULL CHECK (input_version >= 1),
      event_kind text NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')),
      reason text NOT NULL,
      config_revision integer NOT NULL CHECK (config_revision >= 1),
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      status text NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
      claim_token text,
      claimed_until text,
      attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      available_at text NOT NULL,
      last_error text,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      UNIQUE (account_id, input_version),
      CHECK ((status = 'leased' AND claim_token IS NOT NULL AND claimed_until IS NOT NULL) OR (status <> 'leased' AND claim_token IS NULL AND claimed_until IS NULL))
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
      system_account_id text NOT NULL,
      key text NOT NULL,
      value_json text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, key)
    );
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id text NOT NULL,
      scope_type text NOT NULL,
      scope_id text NOT NULL DEFAULT '',
      generation bigint NOT NULL DEFAULT 1,
      first_dirty_at text NOT NULL,
      updated_at text NOT NULL,
      PRIMARY KEY (system_account_id, scope_type, scope_id)
    );
`

// fixtureTablePattern 是夹具手写 seed/校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_business. 限定同形式）。同前缀族按长名优先排列。
var fixtureTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(group_account_stats_dirty|group_authorization_settings|group_accounts|resource_authorization_sources|resource_authorization_grants|resource_authorizations|route_strategy_groups|route_strategies|super_group_members|system_accounts|system_sessions|system_settings|system_teams|account_supported_models|models|accounts|groups)\b`)

// fixtureBind 把夹具手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind
// 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func fixtureBind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// fixtureSQL 把夹具手写 SQL 对齐生产 PG 臂方言（juhe_business 限定 + $n）。
func fixtureSQL(query string) string {
	return fixtureBind(fixtureTablePattern.ReplaceAllString(query, "$1 juhe_business.$2"))
}

// newGroupsPGDatabase 创建 pgtestkit 隔离 PG 测试库并注入 groups 夹具 schema。
func newGroupsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, groupsPGFixtureSchema)
		return err
	})
}

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

var mustChangeFalse = false

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, entry := range s.entries {
		out = append(out, entry.Module+"."+entry.Action)
	}
	return out
}

type recordingInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *recordingInvalidator) Invalidate(_ string, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
}

func (i *recordingInvalidator) has(reason string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, candidate := range i.reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
	authz  *authz.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := newGroupsPGDatabase(t)
	service, err := businessauth.New(db, modelcheckauth.Postgres, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, nil, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	authzStore, err := authz.NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink, Authz: authzStore}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, authz: authzStore}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return created.ID
}

func (e *testEnv) createGroup(t *testing.T, path, name string) (string, map[string]any) {
	t.Helper()
	code, payload := e.do(t, http.MethodPost, path, `{"name":"`+name+`","providerCode":"openai"}`)
	if code != http.StatusCreated {
		t.Fatalf("create %s via %s: %d %v", name, path, code, payload)
	}
	data := payload["data"].(map[string]any)
	return data["id"].(string), data
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(fixtureSQL(statement), args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(fixtureSQL(query), args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// queryRow 是夹具手写单行校验查询的入口（fixtureSQL 对齐生产 PG 方言）。
func (e *testEnv) queryRow(query string, args ...any) *sql.Row {
	return e.db.QueryRow(fixtureSQL(query), args...)
}

func (e *testEnv) bindAccount(t *testing.T, ownerID, groupID, accountID, bindingStatus string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, deleted_at, created_at) VALUES (?, ?, NULL, ?)`, accountID, ownerID, now)
	e.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		ownerID, groupID, accountID, now, now)
	_ = bindingStatus
}

func (e *testEnv) bindRouteStrategy(t *testing.T, strategyID, ownerID, groupID, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		strategyID+"-"+groupID, strategyID, ownerID, groupID, status, now, now)
}

func boolPtr(v bool) *bool { return &v }

// storedPolicyJSON builds a full strict stored scheduling policy document
// (the shape Node parseGroupSchedulingPolicyJson demands on read: every
// stored key present, only stored keys present).
func storedPolicyJSON(t *testing.T, overrides map[string]any) string {
	t.Helper()
	policy := map[string]any{
		"mode":                            "balanced_fast",
		"defaultSoftConcurrency":          5000,
		"fastFirstEnabled":                true,
		"fallbackOnQueueEnabled":          true,
		"breakAffinityOnSoftLimit":        true,
		"breakAffinityOnQueueWaitMs":      0,
		"slowRequestThresholdMs":          30_000,
		"firstOutputSlowThresholdMs":      15_000,
		"recentTimeoutWindowSeconds":      120,
		"recentTimeoutPenaltyThreshold":   2,
		"maxQueueWaitMs":                  60_000,
		"maxQueueSize":                    5000,
		"perApiKeyQueueLimit":             5000,
		"clientIpConcurrencyLimit":        0,
		"clientIpConcurrencyOverflowMode": "reject",
		"imageLaneMaxConcurrency":         0,
	}
	for key, value := range overrides {
		policy[key] = value
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}
