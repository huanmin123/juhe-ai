package groups

// w11f 覆盖波次（文件 1/2）：脚本化 sqlite 连接层故障注入设施 + 纯函数分支。
// 连接器默认直通真实 sqlite，按查询子串注入 Query/Exec/Begin/Commit 故障、
// 受影响行数覆盖与罐装行（Scan 错误 / rows.Err 错误臂）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	execErr  error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	affected int64
	hasAff   bool
	limit    int
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w11fScript struct {
	mu        sync.Mutex
	t         *testing.T
	rules     []*w11fRule
	beginErr  error
	commitErr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w11fRule) allowUnfired() *w11fRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注入规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w11fScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w11f 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w11fScript) rule(r *w11fRule) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) failQuery(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom})
}

func (s *w11fScript) failQueryAlways(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom, limit: -1})
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	return s.rule(&w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr})
}

func (s *w11fScript) failExec(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fScript) execAffected(substr string, affected int64) *w11fRule {
	return s.rule(&w11fRule{substr: substr, affected: affected, hasAff: true})
}

func (s *w11fScript) failBegin() {
	s.mu.Lock()
	s.beginErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) failCommit() {
	s.mu.Lock()
	s.commitErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) take(query string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

// beginFailure 是一次性的。
func (s *w11fScript) beginFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.beginErr
	s.beginErr = nil
	return err
}

// commitFailure 是一次性的。
func (s *w11fScript) commitFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitErr
	s.commitErr = nil
	return err
}

type w11fConnector struct {
	base   driver.Connector
	script *w11fScript
}

func (c w11fConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fConn{base: conn, script: c.script}, nil
}

func (c w11fConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fConn struct {
	base   driver.Conn
	script *w11fScript
}

func (c *w11fConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fConn) Close() error                              { return c.base.Close() }

func (c *w11fConn) Begin() (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w11fTx{base: tx, script: c.script}, nil
}

func (c *w11fConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &w11fTx{base: tx, script: c.script}, nil
	}
	return c.Begin()
}

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.execErr != nil {
			return nil, rule.execErr
		}
		if rule.hasAff {
			return w11fResult{affected: rule.affected}, nil
		}
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w11fConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.queryErr != nil {
			return nil, rule.queryErr
		}
		if rule.cols != nil {
			return &w11fRows{cols: rule.cols, values: rule.rows, nextErr: rule.nextErr}, nil
		}
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w11fTx struct {
	base   driver.Tx
	script *w11fScript
}

func (t *w11fTx) Commit() error {
	if err := t.script.commitFailure(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w11fTx) Rollback() error { return t.base.Rollback() }

type w11fResult struct{ affected int64 }

func (r w11fResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11fResult) RowsAffected() (int64, error) { return r.affected, nil }

type w11fRows struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
	closed  bool
}

func (r *w11fRows) Columns() []string { return r.cols }
func (r *w11fRows) Close() error      { r.closed = true; return nil }
func (r *w11fRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.values[r.next]
	r.next++
	copy(dest, row)
	return nil
}

// ---------------------------------------------------------------------------
// 环境（与 newTestEnv 等价，但物理连接经过故障脚本）
// ---------------------------------------------------------------------------

var w11fDDL = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
	`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, deleted_at TEXT, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name)`,
	`CREATE TABLE IF NOT EXISTS group_accounts (system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (group_id, account_id))`,
	`CREATE TABLE IF NOT EXISTS group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL, activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL, source_type TEXT NOT NULL, source_id TEXT NOT NULL, window_hours INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, scope_type, scope_id))`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL, reserved_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (event_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, input_version INTEGER NOT NULL, event_kind TEXT NOT NULL, reason TEXT NOT NULL, config_revision INTEGER NOT NULL, dispatch_revision INTEGER NOT NULL, status TEXT NOT NULL, claim_token TEXT, claimed_until TEXT, attempt_count INTEGER NOT NULL DEFAULT 0, available_at TEXT NOT NULL, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`ALTER TABLE accounts ADD COLUMN resource_owner_system_account_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE accounts ADD COLUMN authorization_instance_authorization_id TEXT`,
	`ALTER TABLE accounts ADD COLUMN authorization_instance_source_account_id TEXT`,
	`CREATE TABLE IF NOT EXISTS group_account_stats (system_account_id TEXT NOT NULL DEFAULT 'sys_admin', group_id TEXT NOT NULL, total INTEGER NOT NULL DEFAULT 0, available INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0, error INTEGER NOT NULL DEFAULT 0, rate_limited INTEGER NOT NULL DEFAULT 0, current_concurrency INTEGER NOT NULL DEFAULT 0, concurrency_limit INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, group_id))`,
}

type w11fEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
	authz  *authz.Store
	store  *Store
	script *w11fScript
}

func newW11FEnv(t *testing.T) *w11fEnv {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range w11fDDL {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO providers (code, enabled) VALUES ('openai', 1), ('anthropic', 1), ('disabled-provider', 0)`); err != nil {
		t.Fatal(err)
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, false, nil, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	authzStore, err := authz.NewStore(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink, Authz: authzStore}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &w11fEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, authz: authzStore, store: store, script: script}
}

func (e *w11fEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
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

func (e *w11fEnv) login(t *testing.T, username, password, role string) string {
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

func (e *w11fEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *w11fEnv) queryString(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value string
	if err := e.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// w11fSeedGroup 直接插入一个分组行，返回 updated_at。
func (e *w11fEnv) w11fSeedGroup(t *testing.T, id, owner, name, provider string, enabled, isDefault int, groupType string) string {
	t.Helper()
	revision := "2024-01-01T00:00:00.000Z"
	e.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, id, owner, name, provider, enabled, isDefault, groupType, revision, revision)
	return revision
}

// ---------------------------------------------------------------------------
// scheduling.go 纯函数
// ---------------------------------------------------------------------------

func TestW11FSchedulingPure(t *testing.T) {
	// numericPolicyBounds: maxQueueWaitMs 上限 / 零下限键 / 默认。
	if bound := numericPolicyBounds("maxQueueWaitMs"); bound.min != 1 || bound.max != 3_600_000 {
		t.Fatalf("maxQueueWaitMs bound = %+v", bound)
	}
	for _, key := range []string{"breakAffinityOnQueueWaitMs", "clientIpConcurrencyLimit", "imageLaneMaxConcurrency"} {
		if bound := numericPolicyBounds(key); bound.min != 0 {
			t.Fatalf("%s bound = %+v", key, bound)
		}
	}
	if bound := numericPolicyBounds("maxQueueSize"); bound.min != 1 || bound.max != 1_000_000 {
		t.Fatalf("default bound = %+v", bound)
	}

	// normalizeGroupType。
	if got, err := normalizeGroupType(nil); err != nil || got != GroupTypePersonal {
		t.Fatalf("normalizeGroupType nil = %s/%v", got, err)
	}
	personal := GroupTypePersonal
	if got, err := normalizeGroupType(&personal); err != nil || got != GroupTypePersonal {
		t.Fatalf("normalizeGroupType personal = %s/%v", got, err)
	}
	bogus := "bogus"
	if _, err := normalizeGroupType(&bogus); err == nil {
		t.Fatal("normalizeGroupType bogus must fail")
	}
	// normalizeStoredGroupType。
	if _, err := normalizeStoredGroupType("bogus"); err == nil {
		t.Fatal("normalizeStoredGroupType bogus must fail")
	}
	if got, err := normalizeStoredGroupType("high_concurrency"); err != nil || got != GroupTypeHighConcurrency {
		t.Fatalf("normalizeStoredGroupType hc = %s/%v", got, err)
	}

	// schedulingPolicyJSON: personal → nil; 非对象输入; 未知字段; 高并发默认。
	if raw, err := schedulingPolicyJSON(GroupTypePersonal, map[string]any{}, 5000); raw != nil || err != nil {
		t.Fatalf("personal policy = %v/%v", raw, err)
	}
	if _, err := schedulingPolicyJSON(GroupTypeHighConcurrency, "nope", 5000); err == nil {
		t.Fatal("non-object policy must fail")
	}
	if _, err := schedulingPolicyJSON(GroupTypeHighConcurrency, map[string]any{"mode": "balanced_fast"}, 5000); err == nil {
		t.Fatal("unknown writable key must fail")
	}
	raw, err := schedulingPolicyJSON(GroupTypeHighConcurrency, nil, 7000)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["defaultSoftConcurrency"] != float64(7000) || decoded["maxQueueSize"] != float64(7000) {
		t.Fatalf("policy globalMax = %v", decoded)
	}

	// parseStoredSchedulingPolicy: personal nil / 缺失 / 空白 / 坏 JSON / 非对象 / 完整。
	if value, err := parseStoredSchedulingPolicy("x", true, GroupTypePersonal, 5000); value != nil || err != nil {
		t.Fatalf("personal stored = %v/%v", value, err)
	}
	if _, err := parseStoredSchedulingPolicy("", false, GroupTypeHighConcurrency, 5000); err == nil {
		t.Fatal("missing policy must fail")
	}
	if _, err := parseStoredSchedulingPolicy("  ", true, GroupTypeHighConcurrency, 5000); err == nil {
		t.Fatal("blank policy must fail")
	}
	if _, err := parseStoredSchedulingPolicy("{bad", true, GroupTypeHighConcurrency, 5000); err == nil {
		t.Fatal("malformed policy must fail")
	}
	if _, err := parseStoredSchedulingPolicy("[]", true, GroupTypeHighConcurrency, 5000); err == nil {
		t.Fatal("non-object policy must fail")
	}
	full, marshalErr := json.Marshal(defaultHighConcurrencyPolicy(5000))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	parsedAny, err := parseStoredSchedulingPolicy(string(full), true, GroupTypeHighConcurrency, 5000)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := parsedAny.(map[string]any)
	if parsed["mode"] != "balanced_fast" || parsed["maxQueueSize"] != 5000 {
		t.Fatalf("parsed policy = %v", parsed)
	}

	// resolveHighConcurrencyPolicy stored 严格闸门。
	if _, err := resolveHighConcurrencyPolicy(map[string]any{"extra": 1}, 5000, true); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("stored unknown key = %v", err)
	}
	partial := map[string]any{"mode": "balanced_fast"}
	if _, err := resolveHighConcurrencyPolicy(partial, 5000, true); err == nil || !strings.Contains(err.Error(), "缺少字段") {
		t.Fatalf("stored missing key = %v", err)
	}
	// 非法输入值。
	badObject := map[string]any{"defaultSoftConcurrency": "x"}
	if _, err := resolveHighConcurrencyPolicy(badObject, 5000, false); err == nil {
		t.Fatal("non-number concurrency must fail")
	}
	fractional := map[string]any{"maxQueueSize": 1.5}
	if _, err := resolveHighConcurrencyPolicy(fractional, 5000, false); err == nil {
		t.Fatal("fractional queue size must fail")
	}
	outOfRange := map[string]any{"maxQueueSize": 1_000_001}
	if _, err := resolveHighConcurrencyPolicy(outOfRange, 5000, false); err == nil {
		t.Fatal("oversized queue size must fail")
	}
	badBool := map[string]any{"fastFirstEnabled": "yes"}
	if _, err := resolveHighConcurrencyPolicy(badBool, 5000, false); err == nil {
		t.Fatal("non-bool flag must fail")
	}
	badMode := map[string]any{"mode": "turbo"}
	if _, err := resolveHighConcurrencyPolicy(badMode, 5000, false); err == nil {
		t.Fatal("bogus mode must fail")
	}
	nonStringMode := map[string]any{"mode": 7}
	if _, err := resolveHighConcurrencyPolicy(nonStringMode, 5000, false); err == nil {
		t.Fatal("non-string mode must fail")
	}
	badOverflow := map[string]any{"clientIpConcurrencyOverflowMode": "bogus"}
	if _, err := resolveHighConcurrencyPolicy(badOverflow, 5000, false); err == nil {
		t.Fatal("bogus overflow must fail")
	}
	nonStringOverflow := map[string]any{"clientIpConcurrencyOverflowMode": 3}
	if _, err := resolveHighConcurrencyPolicy(nonStringOverflow, 5000, false); err == nil {
		t.Fatal("non-string overflow must fail")
	}
	// perApiKeyQueueLimit: 非整数 / 越界 / 合法。
	badPerKey := map[string]any{"maxQueueSize": 100, "perApiKeyQueueLimit": "x"}
	if _, err := resolveHighConcurrencyPolicy(badPerKey, 5000, false); err == nil {
		t.Fatal("non-number perApiKeyQueueLimit must fail")
	}
	fractionalPerKey := map[string]any{"maxQueueSize": 100, "perApiKeyQueueLimit": 1.5}
	if _, err := resolveHighConcurrencyPolicy(fractionalPerKey, 5000, false); err == nil {
		t.Fatal("fractional perApiKeyQueueLimit must fail")
	}
	overPerKey := map[string]any{"maxQueueSize": 100, "perApiKeyQueueLimit": 101}
	if _, err := resolveHighConcurrencyPolicy(overPerKey, 5000, false); err == nil {
		t.Fatal("oversized perApiKeyQueueLimit must fail")
	}
	underPerKey := map[string]any{"maxQueueSize": 100, "perApiKeyQueueLimit": 0}
	if _, err := resolveHighConcurrencyPolicy(underPerKey, 5000, false); err == nil {
		t.Fatal("zero perApiKeyQueueLimit must fail")
	}
	validPerKey := map[string]any{"maxQueueSize": float64(100), "perApiKeyQueueLimit": float64(80)}
	policy, err := resolveHighConcurrencyPolicy(validPerKey, 5000, false)
	if err != nil || policy["perApiKeyQueueLimit"] != 80 {
		t.Fatalf("valid perApiKeyQueueLimit = %v/%v", policy, err)
	}
	// nil 值字段回落默认。
	nilFields := map[string]any{"maxQueueSize": nil, "fastFirstEnabled": nil}
	policy, err = resolveHighConcurrencyPolicy(nilFields, 5000, false)
	if err != nil || policy["maxQueueSize"] != 5000 || policy["fastFirstEnabled"] != true {
		t.Fatalf("nil fields = %v/%v", policy, err)
	}
	// 覆盖 queue 溢出模式。
	queueOverflow := map[string]any{"clientIpConcurrencyOverflowMode": "queue"}
	if policy, err = resolveHighConcurrencyPolicy(queueOverflow, 5000, false); err != nil || policy["clientIpConcurrencyOverflowMode"] != "queue" {
		t.Fatalf("queue overflow = %v/%v", policy, err)
	}

	// boundedPolicyInteger / policyBoolean / policyMode / policyOverflowMode 缺省回落。
	if got, err := boundedPolicyInteger(map[string]any{}, "maxQueueSize", 1, 100, 42); err != nil || got != 42 {
		t.Fatalf("bounded fallback = %v/%v", got, err)
	}
	if got, err := policyBoolean(map[string]any{}, "fastFirstEnabled", true); err != nil || !got {
		t.Fatalf("boolean fallback = %v/%v", got, err)
	}
	if got, err := policyMode(map[string]any{}, map[string]any{"mode": "balanced_fast"}); err != nil || got != "balanced_fast" {
		t.Fatalf("mode fallback = %v/%v", got, err)
	}
	if got, err := policyOverflowMode(map[string]any{}, map[string]any{"clientIpConcurrencyOverflowMode": "reject"}); err != nil || got != "reject" {
		t.Fatalf("overflow fallback = %v/%v", got, err)
	}

	// validateSchedulingPolicyInput。
	if validateSchedulingPolicyInput(map[string]any{"mode": "x"}) {
		t.Fatal("unknown key must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"defaultSoftConcurrency": nil}) {
		t.Fatal("null value must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"defaultSoftConcurrency": "x"}) {
		t.Fatal("non-number must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"defaultSoftConcurrency": 0}) {
		t.Fatal("below min must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"defaultSoftConcurrency": 1_000_001}) {
		t.Fatal("above max must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"maxQueueWaitMs": 3_600_001}) {
		t.Fatal("above maxQueueWaitMs max must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"defaultSoftConcurrency": 1.5}) {
		t.Fatal("fractional must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"clientIpConcurrencyLimit": float64(0), "imageLaneMaxConcurrency": float64(0)}) != true {
		t.Fatal("zero ip/image limits must be valid")
	}
	if validateSchedulingPolicyInput(map[string]any{"clientIpConcurrencyOverflowMode": nil}) {
		t.Fatal("null overflow must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{"clientIpConcurrencyOverflowMode": "queue"}) != true {
		t.Fatal("queue overflow must be valid")
	}
	if validateSchedulingPolicyInput(map[string]any{"clientIpConcurrencyOverflowMode": "bogus"}) {
		t.Fatal("bogus overflow must be invalid")
	}
	if validateSchedulingPolicyInput(map[string]any{}) != true {
		t.Fatal("empty policy must be valid")
	}

	// policyInputObject / joinCN。
	if object, err := policyInputObject(nil); err != nil || len(object) != 0 {
		t.Fatalf("policyInputObject nil = %v/%v", object, err)
	}
	if object, err := policyInputObject(map[string]any{"a": 1}); err != nil || object["a"] != 1 {
		t.Fatalf("policyInputObject map = %v/%v", object, err)
	}
	if _, err := policyInputObject([]any{}); err == nil {
		t.Fatal("policyInputObject slice must fail")
	}
	if joinCN([]string{"a", "b"}) != "a、b" || joinCN(nil) != "" {
		t.Fatal("joinCN drift")
	}
}

// ---------------------------------------------------------------------------
// stats_reader.go
// ---------------------------------------------------------------------------

func TestW11FStatsReader(t *testing.T) {
	env := newW11FEnv(t)
	reader := NewGroupAccountStatsDBReader(env.db, false)
	ctx := context.Background()

	// 空 ID / nil DB → 空结果。
	if got, err := reader.ReadGroupAccountStats(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty ids = %v/%v", got, err)
	}
	nilReader := &GroupAccountStatsDBReader{}
	if got, err := nilReader.ReadGroupAccountStats(ctx, []string{"g1"}); err != nil || len(got) != 0 {
		t.Fatalf("nil db = %v/%v", got, err)
	}

	// 命中：无时效过滤（对齐 Node groupAccountStatsSelectColumns——旧实现的
	// updated_at > 读时刻恒假且 today_usage/usage 两列不存在，均移植错误），
	// 旧行/新行都返回，total/available 等计数透传。
	fresh := time.Now().UTC().Add(time.Minute).Format("2006-01-02 15:04:05")
	stale := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	env.exec(t, `INSERT INTO group_account_stats
		(system_account_id, group_id, total, available, active, disabled, error, rate_limited, current_concurrency, concurrency_limit, updated_at)
		VALUES ('sys_admin', 'w11f-g1', 5, 4, 3, 1, 1, 0, 2, 9, ?)`, fresh)
	env.exec(t, `INSERT INTO group_account_stats
		(system_account_id, group_id, total, available, active, disabled, error, rate_limited, current_concurrency, concurrency_limit, updated_at)
		VALUES ('sys_admin', 'w11f-g2', 1, 1, 1, 0, 0, 0, 0, 1, ?)`, stale)

	stats, err := reader.ReadGroupAccountStats(ctx, []string{"w11f-g1", "w11f-g2", "w11f-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats = %v", stats)
	}
	g1 := stats["w11f-g1"]
	if g1.Total != 5 || g1.Available != 4 || g1.Active != 3 || g1.Disabled != 1 || g1.Error != 1 || g1.RateLimited != 0 || g1.CurrentConcurrency != 2 || g1.ConcurrencyLimit != 9 {
		t.Fatalf("g1 = %+v", g1)
	}
	g2 := stats["w11f-g2"]
	if g2.Total != 1 || g2.Available != 1 || g2.ConcurrencyLimit != 1 {
		t.Fatalf("g2 = %+v", g2)
	}
	// TodayUsage/Usage 属 usage-summary hydrate 键，不在本投影，保持零值。
	if g1.TodayUsage != nil || g1.Usage != nil || g2.TodayUsage != nil || g2.Usage != nil {
		t.Fatalf("usage hydrate 键必须为零值: %+v %+v", g1, g2)
	}
	// accountStatsFromGroupRow 直连。
	row := groupAccountStatsRow{GroupID: "g", Total: 1, Available: 2, Active: 3, Disabled: 4, Error: 5, RateLimited: 6, CurrentConcurrency: 7, ConcurrencyLimit: 8}
	fromRow := accountStatsFromGroupRow(row)
	if fromRow.RateLimited != 6 || fromRow.Total != 1 || fromRow.TodayUsage != nil || fromRow.Usage != nil {
		t.Fatalf("fromRow = %+v", fromRow)
	}

	// 查询错误 / Scan 错误 / rows.Err。
	env.script.failQuery("FROM group_account_stats")
	if _, err := reader.ReadGroupAccountStats(ctx, []string{"w11f-g1"}); err == nil {
		t.Fatal("stats query fault must fail")
	}
	env.script.canned("FROM group_account_stats",
		[]string{"group_id", "total", "available", "active", "disabled", "error", "rate_limited", "current_concurrency", "concurrency_limit"},
		[][]driver.Value{{nil, 1, 1, 1, 1, 1, 1, 1, 1}}, nil)
	if _, err := reader.ReadGroupAccountStats(ctx, []string{"w11f-g1"}); err == nil {
		t.Fatal("stats scan fault must fail")
	}
	env.script.canned("FROM group_account_stats",
		[]string{"group_id", "total", "available", "active", "disabled", "error", "rate_limited", "current_concurrency", "concurrency_limit"},
		[][]driver.Value{{"g", 1, 1, 1, 1, 1, 1, 1, 1}}, w11fBoom)
	if _, err := reader.ReadGroupAccountStats(ctx, []string{"w11f-g1"}); err == nil {
		t.Fatal("stats rows.Err fault must fail")
	}
}

// ---------------------------------------------------------------------------
// store.go 纯辅助
// ---------------------------------------------------------------------------

func TestW11FStoreHelpers(t *testing.T) {
	// Error() 方法。
	if (&ValidationError{Message: "m"}).Error() != "m" || (&ConflictError{Message: "m"}).Error() != "m" {
		t.Fatal("Error() drift")
	}
	// NewStore(nil) 与 PG 方言。
	if _, err := NewStore(nil, false, nil, nil, nil); err == nil {
		t.Fatal("NewStore(nil) must fail")
	}
	pgStore := &Store{pg: true}
	if pgStore.table("x") != "juhe_business.x" {
		t.Fatal("pg table drift")
	}
	if got := pgStore.bind("UPDATE g SET a = ?, b = ?"); got != "UPDATE g SET a = $1, b = $2" {
		t.Fatalf("pg bind = %q", got)
	}
	// 默认 newI 与 nowISO。
	db, err := sql.Open("sqlite", "file:w11f-groups-default-newid?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defaultStore, err := NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaultStore.newI("grp") == "" || defaultStore.nowISO() == "" {
		t.Fatal("default newI/now drift")
	}
	// writeSystemAccountID: filter → viewer → 缺上下文。
	if got, err := (AccessScope{IsAdmin: true, FilterID: "f"}).writeSystemAccountID(); err != nil || got != "f" {
		t.Fatalf("filter writer = %s/%v", got, err)
	}
	if got, err := (AccessScope{ViewerID: "v"}).writeSystemAccountID(); err != nil || got != "v" {
		t.Fatalf("viewer writer = %s/%v", got, err)
	}
	if _, err := (AccessScope{}).writeSystemAccountID(); err == nil {
		t.Fatal("empty scope writer must fail")
	}
	// itoa / ensureCtx。
	if itoa(0) != "0" || itoa(97) != "97" {
		t.Fatal("itoa drift")
	}
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx nil drift")
	}
	// ownerClause。
	if clause, args, ok := ownerClause(AccessScope{IsAdmin: true}); !ok || clause != "" || args != nil {
		t.Fatalf("admin unscoped = %q/%v/%v", clause, args, ok)
	}
	if clause, args, ok := ownerClause(AccessScope{IsAdmin: true, FilterID: "f"}); !ok || clause == "" || args[0] != "f" {
		t.Fatalf("admin filtered = %q/%v/%v", clause, args, ok)
	}
	if _, _, ok := ownerClause(AccessScope{}); ok {
		t.Fatal("empty viewer must not be ok")
	}
	if clause, _, ok := ownerClause(AccessScope{ViewerID: "v"}); !ok || clause == "" {
		t.Fatalf("viewer clause = %q/%v", clause, ok)
	}
	// 分页窗口。
	if listPageBounds(0) != 1 || listPageBounds(2000) != 1 || listPageBounds(10) != 100 {
		t.Fatal("listPageBounds drift")
	}
	if page, size := normalizeListPageValues(0, 0); page != 1 || size != 50 {
		t.Fatalf("normalize defaults = %d/%d", page, size)
	}
	if page, size := normalizeListPageValues(1, 600); page != 1 || size != 500 {
		t.Fatalf("normalize clamp = %d/%d", page, size)
	}
	if page, size := normalizeListPageValues(5000, 1); page != 1000 || size != 1 {
		t.Fatalf("normalize page bound = %d/%d", page, size)
	}
	// textPrefixUpperBound / keywordFilter。
	if got := textPrefixUpperBound(string(rune(0x10ffff))); got != string(rune(0x10ffff))+"\uffff" {
		t.Fatalf("max rune upper = %q", got)
	}
	if clause, args := keywordFilter(""); clause != "" || args != nil {
		t.Fatalf("empty keyword = %q/%v", clause, args)
	}
	if clause, args := keywordFilter("ab"); clause == "" || len(args) != 4 {
		t.Fatalf("keyword = %q/%v", clause, args)
	}
	if clause, args := keywordFilterAlias("", "ab"); clause == "" || len(args) != 4 {
		t.Fatalf("keyword alias empty = %q/%v", clause, args)
	}
	if clause, _ := keywordFilterAlias("g", "ab"); !strings.Contains(clause, "g.name") {
		t.Fatalf("keyword alias prefix = %q", clause)
	}
	// 文本辅助。
	if got := uniqueStrings([]string{"", "a", "a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("uniqueStrings = %v", got)
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 || boolText(true) != "true" || boolText(false) != "false" {
		t.Fatal("bool helpers drift")
	}
	if ptrString("") != nil || ptrString("x") == nil {
		t.Fatal("ptrString drift")
	}
	if nullPtrString(sql.NullString{}) != nil || nullPtrString(sql.NullString{String: "y", Valid: true}) == nil {
		t.Fatal("nullPtrString drift")
	}
	if nullText(sql.NullString{}) != "" || nullText(sql.NullString{String: "z", Valid: true}) != "z" {
		t.Fatal("nullText drift")
	}
	if derefOrEmpty(nil) != "" || derefOrEmpty(func() *string { v := "d"; return &v }()) != "d" {
		t.Fatal("derefOrEmpty drift")
	}
	// sameNullableText: NULL 列与 nil 指针相同、与空串指针不同、与等值指针相同。
	if !sameNullableText(sql.NullString{}, nil) || sameNullableText(sql.NullString{}, func() *string { v := ""; return &v }()) {
		t.Fatal("sameNullableText nil arms drift")
	}
	if sameNullableText(sql.NullString{String: "x", Valid: true}, nil) || !sameNullableText(sql.NullString{String: "x", Valid: true}, func() *string { v := "x"; return &v }()) {
		t.Fatal("sameNullableText value arms drift")
	}
	// requiredText / nullableText。
	if _, err := requiredText(nil, "分组名称"); err == nil {
		t.Fatal("requiredText nil must fail")
	}
	blank := "  "
	if _, err := requiredText(&blank, "分组名称"); err == nil {
		t.Fatal("requiredText blank must fail")
	}
	if got, err := nullableText(nil, "分组说明"); got != nil || err != nil {
		t.Fatal("nullableText nil drift")
	}
	if got, err := nullableText(&blank, "分组说明"); got != nil || err != nil {
		t.Fatal("nullableText blank drift")
	}
	// nextGroupUpdatedAt: 坏格式 / floor。
	if _, err := nextGroupUpdatedAt("zzz", time.Now()); err == nil {
		t.Fatal("nextGroupUpdatedAt garbage must fail")
	}
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, err := nextGroupUpdatedAt("2024-01-02T03:04:05.500Z", now); err != nil || got != "2024-01-02T03:04:05.501Z" {
		t.Fatalf("floor = %s/%v", got, err)
	}
	// duplicateGroupNameError。
	if duplicateGroupNameError(nil, "n") != nil {
		t.Fatal("nil duplicate drift")
	}
	if got := duplicateGroupNameError(errors.New("UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name"), "n"); got == nil {
		t.Fatal("sqlite duplicate must map")
	}
	if duplicateGroupNameError(errors.New("idx_groups_owner_provider_name_unique_lower violated"), "n") == nil {
		t.Fatal("lower index duplicate must map")
	}
	if duplicateGroupNameError(errors.New("something else"), "n") != nil {
		t.Fatal("unrelated error must pass through")
	}
	// gatewayRuntimeChanged: 仅 providerCode/enabled/groupType/schedulingPolicy 触发。
	if !gatewayRuntimeChanged([]string{"name", "providerCode"}) || gatewayRuntimeChanged([]string{"name", "description"}) {
		t.Fatal("gatewayRuntimeChanged drift")
	}
	// columnChanged。
	if columnChanged([]string{"name = ?", "provider_code = ?"}, "provider_code") != true {
		t.Fatal("columnChanged drift")
	}
	// includeSystemAccountFieldsForList — admin 含 owner 字段。
	if !includeSystemAccountFieldsForList(AccessScope{IsAdmin: true}, nil) {
		t.Fatal("admin must include owner fields")
	}
}

// TestW11FPermissionHelpers 覆盖 options.go 权限与授权视图辅助。
func TestW11FPermissionHelpers(t *testing.T) {
	env := newW11FEnv(t)
	store := env.store

	// authorizedGroupPermissions。
	permissions := authorizedGroupPermissions(true, false)
	if !permissions.CanUse || !permissions.CanEdit || permissions.CanDelete || permissions.CanReturnAuthorization || permissions.CanAuthorize {
		t.Fatalf("authorized permissions = %+v", permissions)
	}
	// canBindAccessRowValues: 禁用 / 状态非 active / 过期 / 未来 / 坏时间。
	invalidStatus := sql.NullString{String: "paused", Valid: true}
	if bound, err := store.canBindAccessRowValues(false, sql.NullString{String: "active", Valid: true}, sql.NullString{}); err != nil || bound {
		t.Fatalf("disabled row = %v/%v", bound, err)
	}
	if bound, err := store.canBindAccessRowValues(true, invalidStatus, sql.NullString{}); err != nil || bound {
		t.Fatalf("paused row = %v/%v", bound, err)
	}
	past := sql.NullString{String: "2001-01-01T00:00:00Z", Valid: true}
	if bound, err := store.canBindAccessRowValues(true, sql.NullString{String: "active", Valid: true}, past); err != nil || bound {
		t.Fatalf("expired row = %v/%v", bound, err)
	}
	future := sql.NullString{String: "2999-01-01T00:00:00Z", Valid: true}
	if bound, err := store.canBindAccessRowValues(true, sql.NullString{String: "active", Valid: true}, future); err != nil || !bound {
		t.Fatalf("future row = %v/%v", bound, err)
	}
	bad := sql.NullString{String: "not-a-time", Valid: true}
	if _, err := store.canBindAccessRowValues(true, sql.NullString{String: "active", Valid: true}, bad); err == nil {
		t.Fatal("malformed expiresAt must fail")
	}
	// authorizationExpired: 缺失 / 空白。
	if expired, err := store.authorizationExpired(sql.NullString{}); err != nil || expired {
		t.Fatalf("absent expiresAt = %v/%v", expired, err)
	}
	blank := sql.NullString{String: "  ", Valid: true}
	if expired, err := store.authorizationExpired(blank); err != nil || expired {
		t.Fatalf("blank expiresAt = %v/%v", expired, err)
	}
	// parseAuthorizationLimitsView: 缺失 / 空白 / 坏 JSON / 完整文档。
	if value, err := parseAuthorizationLimitsView(sql.NullString{}); err != nil || value == nil {
		t.Fatalf("absent limits = %v/%v", value, err)
	}
	if value, err := parseAuthorizationLimitsView(sql.NullString{String: "  ", Valid: true}); err != nil || value == nil {
		t.Fatalf("blank limits = %v/%v", value, err)
	}
	if _, err := parseAuthorizationLimitsView(sql.NullString{String: "{bad", Valid: true}); err == nil {
		t.Fatal("malformed limits must fail")
	}
	full := `{"hourly":{"enabled":true,"hours":1,"limit":5},"daily":{"enabled":true,"limit":100},"weekly":{"enabled":true,"limit":50},"monthly":{"enabled":true,"limit":1000},"total":{"enabled":true,"limit":10000}}`
	value, err := parseAuthorizationLimitsView(sql.NullString{String: full, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if !strings.Contains(string(encoded), `"hourly"`) || !strings.Contains(string(encoded), `"monthly"`) {
		t.Fatalf("limits view = %s", encoded)
	}
	if !strings.Contains(string(encoded), `"weekly"`) {
		t.Fatalf("weekly entry missing: %s", encoded)
	}
}
