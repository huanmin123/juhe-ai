package oauthmgmt

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

const testSecret = "m17-oauthmgmt-test-secret"

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

func (s *recordingSink) has(operationKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.OperationKey == operationKey || entry.Module+"."+entry.Action == operationKey {
			return true
		}
	}
	return false
}

// exchangeCall records one upstream token request.
type exchangeCall struct {
	URL     string
	Body    string
	Headers map[string]string
}

type mockExchanger struct {
	mu    sync.Mutex
	calls []exchangeCall
	// respond renders the canned upstream response per call index.
	respond func(index int, call exchangeCall) (int, string)
}

func (m *mockExchanger) Do(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	call := exchangeCall{URL: request.URL, Body: request.Body, Headers: request.Headers}
	m.calls = append(m.calls, call)
	status, body := m.respond(len(m.calls)-1, call)
	return TokenHTTPResponse{StatusCode: status, Body: body}, nil
}

func (m *mockExchanger) recorded() []exchangeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]exchangeCall{}, m.calls...)
}

// staticToken is the common responder: every call gets the same token payload.
func staticToken(payload string) func(int, exchangeCall) (int, string) {
	return func(int, exchangeCall) (int, string) { return http.StatusOK, payload }
}

// scriptedSSO walks the grok device flow with an ordered script.
type scriptedSSORequester struct {
	mu    sync.Mutex
	calls []SSODeviceRequest
	steps []SSODeviceResponse
}

func (s *scriptedSSORequester) Do(_ context.Context, request SSODeviceRequest) (SSODeviceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, request)
	if len(s.steps) == 0 {
		return SSODeviceResponse{StatusCode: 500, Body: "script exhausted"}, nil
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	return step, nil
}

func (s *scriptedSSORequester) recorded() []SSODeviceRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SSODeviceRequest{}, s.calls...)
}

type testEnv struct {
	deps      *authsys.Deps
	k         *kernel.Kernel
	server    *httptest.Server
	jar       map[string]string
	mu        sync.Mutex
	sink      *recordingSink
	exchanger *mockExchanger
	sso       *scriptedSSORequester
	db        *sql.DB
	store     *Store
	rotation  *rotationInvalidatorRecorder
}

// rotationInvalidatorRecorder captures the rotation post-commit invalidation
// channels (T2 audit) so the route tests can assert topic + reason parity
// with oauth-credential-rotation.repository.ts:223-226.
type rotationInvalidatorRecorder struct {
	mu         sync.Mutex
	lookups    []string
	runtime    []string
	validation []string
}

func (r *rotationInvalidatorRecorder) InvalidateAccountLookup(accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups = append(r.lookups, accountID)
	return nil
}

func (r *rotationInvalidatorRecorder) InvalidateRuntime(reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runtime = append(r.runtime, reason)
	return nil
}

func (r *rotationInvalidatorRecorder) InvalidateAPIKeyValidation(reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validation = append(r.validation, reason)
	return nil
}

func (r *rotationInvalidatorRecorder) channels() (lookups, runtime, validation []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.lookups...), append([]string{}, r.runtime...), append([]string{}, r.validation...)
}

var testSchema = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	// vendors（供应商与上游协议面重构设计 §1.1/§2.1）：accounts 写入面
	// createInTx 经 assertVendorEnabled（internal/accounts write.go）校验真实
	// 厂商 provider 必须存在 enabled=1 的 vendors 行；DDL 与 maintenance
	// sqlite_schema_business.go 的 vendors 表同构（无 parent_code）。
	// T30/B4 ⑧：档案域已切 vendors + 编译期 registry，夹具不再创建或播种
	// providers / provider_protocol_profiles 旧表（目标 schema 无此二表）。
	`CREATE TABLE IF NOT EXISTS vendors (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, test_status TEXT NOT NULL DEFAULT 'unknown', latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	// groups 目标形态（供应商与上游协议面重构设计 §8）：无 provider_code 列，
	// 分组与 provider 无关；与 maintenance sqlite_schema_business.go 的 groups
	// 表同构（group_kind/super_group_members 属超组切片，本夹具不涉及）。
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', group_kind TEXT NOT NULL DEFAULT 'standard', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 1,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
		system_account_id TEXT NOT NULL,
		-- 迁移窗口 DDL（accounts fixture 同形，见 internal/accounts
		-- accounts_test.go）：写入面 createInTx 落 vendor_code /
		-- connection_driver_code（generic 账户 vendor 为 NULL）+ oauth_type
		-- 物理列与 health_check_connection_id（§2.1 复合外键列），创建链路
		-- （CreateOAuthAccount → accounts.Create）经该写入面，fixture 同步携带。
		vendor_code TEXT,
		connection_driver_code TEXT,
		oauth_type TEXT,
		health_check_connection_id TEXT,
		provider_protocol_profile_id TEXT NOT NULL DEFAULT '',
		protocol_code TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending_test',
		credentials_encrypted TEXT NOT NULL,
		credential_fingerprint TEXT,
		credential_mask TEXT NOT NULL DEFAULT '',
		oauth_access_token_expires_at TEXT,
		oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
		proxy_profile_id TEXT,
		concurrency_limit INTEGER NOT NULL DEFAULT 5000,
		priority INTEGER NOT NULL DEFAULT 0,
		super_priority_enabled INTEGER NOT NULL DEFAULT 0,
		fallback_enabled INTEGER NOT NULL DEFAULT 0,
		client_compatibility TEXT NOT NULL DEFAULT 'openai_standard',
		schedulable INTEGER NOT NULL DEFAULT 1,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		notes TEXT,
		account_expires_at TEXT,
		last_used_at TEXT,
		cooldown_until TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		last_error_trace_id TEXT,
		health_check_model TEXT NOT NULL DEFAULT '',
		health_check_endpoint_mode TEXT NOT NULL DEFAULT 'chat_json',
		health_check_failure_count INTEGER NOT NULL DEFAULT 0,
		health_check_failure_started_at TEXT,
		cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
		cooldown_retest_observation_started_at TEXT,
		cooldown_retest_generation TEXT,
		cooldown_retest_last_at TEXT,
		cooldown_retest_last_status_code INTEGER,
		temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1,
		expedited_recovery_enabled INTEGER NOT NULL DEFAULT 0,
		capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image')),
		last_health_check_at TEXT,
		last_health_success_at TEXT,
		last_health_check_status_code INTEGER,
		last_health_check_error_code TEXT,
		last_health_check_error_message TEXT,
		last_health_check_trace_id TEXT,
		stream_failure_count INTEGER NOT NULL DEFAULT 0,
		stream_failure_window_started_at TEXT,
		next_health_check_at TEXT,
		balance_query_enabled INTEGER NOT NULL DEFAULT 0,
		balance_query_next_refresh_at TEXT,
		balance_query_config_json TEXT NOT NULL DEFAULT '{}',
		authorization_instance_source_account_id TEXT,
		authorization_instance_authorization_id TEXT,
		authorization_instance_owner_system_account_id TEXT,
		deleted_at TEXT,
		deleted_by TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL`,
	`CREATE TABLE IF NOT EXISTS group_accounts (
		system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL,
		account_authorization_id TEXT, local_priority INTEGER NOT NULL DEFAULT 0,
		local_super_priority_enabled INTEGER NOT NULL DEFAULT 0, local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (group_id, account_id)
	)`,
	`CREATE TABLE IF NOT EXISTS account_supported_models (
		account_id TEXT NOT NULL, model_id TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, model_id),
		FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
		FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE RESTRICT
	)`,
	// models（§4 单表，写入面 resolveEntryModelID / 健康检查三件套模型解析的
	// 身份键：vendor_code + upstream_model + owner 可见性 + status=active）。
	`CREATE TABLE IF NOT EXISTS models (
		id TEXT PRIMARY KEY,
		vendor_code TEXT,
		public_model TEXT NOT NULL,
		upstream_model TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT 'builtin',
		system_account_id TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		supported_endpoint_modes_json TEXT,
		release_date TEXT,
		shutdown_date TEXT,
		catalog_visible INTEGER NOT NULL DEFAULT 1,
		catalog_order INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// account_connections / account_endpoint_modes（§2.1）：createInTx 连接行
	// 写入 + 健康检查 preset → enabled 连接行解析的写入面两表；列集与
	// internal/accounts fixture 同形（复合唯一键支撑 accounts 复合外键，
	// 测试 SQLite 夹具不带该 FK）。
	`CREATE TABLE IF NOT EXISTS account_connections (
		id TEXT PRIMARY KEY,
		account_id TEXT NOT NULL,
		connection_preset_code TEXT NOT NULL,
		protocol_surface_code TEXT,
		base_url TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE (account_id, connection_preset_code),
		UNIQUE (account_id, id),
		FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS account_endpoint_modes (
		connection_id TEXT NOT NULL,
		endpoint_mode TEXT NOT NULL,
		PRIMARY KEY (connection_id, endpoint_mode),
		FOREIGN KEY (connection_id) REFERENCES account_connections(id) ON DELETE CASCADE
	)`,
	// account_entry_mappings（§5.1 目标形态）：replaceAccountModelMappings 的
	// 整集合替换 DELETE 需要该表存在（OAuth 建户映射通常为空）。
	`CREATE TABLE IF NOT EXISTS account_entry_mappings (
		id TEXT PRIMARY KEY,
		account_id TEXT NOT NULL,
		source_model TEXT NOT NULL,
		source_endpoint_family TEXT NOT NULL,
		target_connection_id TEXT NOT NULL,
		target_model_id TEXT NOT NULL,
		target_endpoint_family TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE (account_id, source_model, source_endpoint_family)
	)`,
	`CREATE TABLE IF NOT EXISTS account_tags (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique ON account_tags(system_account_id, name)`,
	`CREATE TABLE IF NOT EXISTS account_tag_bindings (
		account_id TEXT NOT NULL, tag_id TEXT NOT NULL, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, tag_id)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_terms (
		account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, term TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, term)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_documents (
		account_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, normalized_name TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS account_lock_states (
		account_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
		lock_state TEXT NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
		lock_death_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
		lock_retry_interval_seconds INTEGER NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
		incident_id TEXT,
		generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
		incident_started_at TEXT,
		deadline_at TEXT,
		original_status TEXT,
		provenance TEXT,
		next_retry_at_ms INTEGER,
		lease_id TEXT,
		lease_until_ms INTEGER,
		updated_at TEXT NOT NULL
	)`,
	// T2 audit: the rotation dispatch-revision fence writes the pending
	// outbox rows through the accounts family advance (Node
	// business-schema.ts account_circuit_outbox).
	`CREATE TABLE IF NOT EXISTS account_circuit_outbox (
		event_id TEXT PRIMARY KEY,
		projection_key TEXT NOT NULL,
		dedupe_key TEXT NOT NULL,
		event_type TEXT NOT NULL CHECK (event_type IN ('dispatch_revision_changed', 'incident_changed')),
		account_id TEXT NOT NULL,
		account_runtime_key TEXT NOT NULL,
		circuit_scope_key TEXT,
		incident_id TEXT,
		transition_id TEXT NOT NULL,
		dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1),
		generation INTEGER,
		ledger_revision INTEGER,
		status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'dispatched')),
		available_at_ms INTEGER NOT NULL,
		claim_token TEXT,
		claimed_by TEXT,
claim_until_ms INTEGER,
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			last_error_class TEXT,
			acknowledged_at_ms INTEGER,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
			UNIQUE (projection_key, dedupe_key)
		)`,
	// Missing from oauthmgmt test schema but required by accounts store CREATE.
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER NOT NULL CHECK (current_version >= 1), reserved_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_health_jobs_input_outbox (event_id TEXT PRIMARY KEY, account_id TEXT NOT NULL, input_version INTEGER NOT NULL CHECK (input_version >= 1), event_kind TEXT NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')), reason TEXT NOT NULL, config_revision INTEGER NOT NULL CHECK (config_revision >= 1), dispatch_revision INTEGER NOT NULL CHECK (dispatch_revision >= 1), status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')), claim_token TEXT, claimed_until TEXT, attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0), available_at TEXT NOT NULL, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE (account_id, input_version))`,
	// Batch-edit side effects: stats dirty marker (Node business-schema.ts:553-584).
	`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (
			group_id TEXT PRIMARY KEY,
			reason TEXT,
			updated_at TEXT NOT NULL
		)`,
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:oauthmgmt-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range testSchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := seedOAuthCatalog(db); err != nil {
		t.Fatal(err)
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	authAccounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: authAccounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	accountStore, err := accounts.NewStore(db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	exchanger := &mockExchanger{respond: staticToken(`{}`)}
	sso := &scriptedSSORequester{}
	rotation := &rotationInvalidatorRecorder{}
	store, err := NewStore(db, false, testSecret, accountStore, exchanger, nil, nil,
		WithSSODeviceTransport(sso), WithSSOSleep(func(context.Context, time.Duration) error { return nil }),
		// T2 audit: the rotation post-commit channels + the in-transaction
		// circuit dispatch revision family advance (same accounts store).
		WithCacheInvalidator(rotation),
		WithDispatchRevisionAdvancer(accountStore),
		// 既有夹具大量依赖无代理建户/刷新路径：默认关拦截，requiresProxy
		// 的强制行为由 proxy_required_test.go 单独覆盖。
		WithProxyRequired(false))
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{
		deps: deps, k: k, server: server, jar: map[string]string{},
		sink: sink, exchanger: exchanger, sso: sso, db: db, store: store,
		rotation: rotation,
	}
}

// seedOAuthCatalog inserts the four vendors and their default model rows.
// T30/B4 ⑧：目标 schema 已删除 providers / provider_protocol_profiles；
// 档案身份由编译期 registry 派生，夹具只播种 vendors 行。
func seedOAuthCatalog(db *sql.DB) error {
	now := "2026-01-01T00:00:00.000Z"
	for _, provider := range []struct {
		id, code, models string
	}{
		{"prov-gpt", "gpt", `["gpt-4o-mini","gpt-5"]`},
		{"prov-anthropic", "anthropic", `["claude-sonnet-4-5"]`},
		{"prov-gemini", "gemini", `["gemini-2.5-pro"]`},
		{"prov-xai", "xai", `["grok-4"]`},
	} {
		// vendors 行（assertVendorEnabled 只读 enabled；档案域读 enabled +
		// default_supported_models_json）：id=code 对齐 maintenance
		// pgSeedProviders 的权威播种口径，enabled=1 与九厂商内置行一致。
		if _, err := db.Exec(`INSERT INTO vendors (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?)`, provider.code, provider.code, provider.code, provider.models, now, now); err != nil {
			return err
		}
	}
	// models 行（§4 身份键：vendor_code + upstream_model）：写入面
	// replaceAccountSupportedModels / 健康检查三件套经 resolveEntryModelID 解析
	// 每个支持/检查模型名，vendor 默认支持模型与测试显式模型名都要有 active
	// 行；supported_endpoint_modes_json 覆盖各 vendor chat 域全部对话形态。
	for _, model := range []struct {
		vendor, name, modes string
	}{
		{"gpt", "gpt-4o-mini", `["chat_json","chat_sse","responses_json","responses_sse"]`},
		{"gpt", "gpt-5", `["chat_json","chat_sse","responses_json","responses_sse"]`},
		{"gpt", "m1", `["chat_json","chat_sse","responses_json","responses_sse"]`},
		{"gpt", "m2", `["chat_json","chat_sse","responses_json","responses_sse"]`},
		{"anthropic", "claude-sonnet-4-5", `["messages_json","messages_sse","message_token_counting"]`},
		{"gemini", "gemini-2.5-pro", `["generate_content_json","generate_content_sse","interactions_json","interactions_sse","count_tokens","embed_content"]`},
		{"xai", "grok-4", `["chat_json","chat_sse","responses_json","responses_sse"]`},
	} {
		if _, err := db.Exec(`INSERT INTO models (id, vendor_code, public_model, upstream_model, scope,
			status, supported_endpoint_modes_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'builtin', 'active', ?, ?, ?)`,
			"mod-"+model.vendor+"-"+model.name, model.vendor, model.name, model.name,
			model.modes, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *testEnv) seedDefaultGroups(t *testing.T, ownerID string) {
	t.Helper()
	// 目标形态（§8）：分组无 provider 维度，owner 至多一个 is_default=1 分组；
	// 其余按 provider 命名的行只是普通命名分组（ID 保留 provider 词根仅为沿
	// 用既有测试标识符）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, provider := range []string{"gpt", "anthropic", "gemini", "xai"} {
		isDefault := 0
		if provider == "gpt" {
			isDefault = 1
		}
		e.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, created_at, updated_at)
			VALUES (?, ?, '默认分组', 1, ?, 'personal', ?, ?)`,
			"grp-default-"+provider+"-"+ownerID, ownerID, isDefault, now, now)
	}
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
	e.seedDefaultGroups(t, created.ID)
	return created.ID
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (e *testEnv) accountCredentials(t *testing.T, accountID string) map[string]any {
	t.Helper()
	var sealed string
	if err := e.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE id = ?`, accountID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{}
	if err := decryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("credentials must decrypt: %v", err)
	}
	return credentials
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

// fakeJWT builds a three-part unsigned JWT whose claims the services decode.
func fakeJWT(claims map[string]any) string {
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(map[string]string{"alg": "none", "typ": "JWT"}) + "." + encode(claims) + ".signature"
}

func mustForm(t *testing.T, raw string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("form body: %v", err)
	}
	return values
}

// openAITokenPayload renders the openai token response with JWT claims.
func openAITokenPayload(accessToken string) string {
	claims := map[string]any{
		"email": "dev@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-1",
			"chatgpt_user_id":    "user-1",
			"chatgpt_plan_type":  "plus",
		},
	}
	return fmt.Sprintf(`{"access_token":%q,"id_token":%q,"refresh_token":"openai-refresh-2","expires_in":3600,"token_type":"Bearer"}`,
		accessToken, fakeJWT(claims))
}

func TestOpenAIOAuthFamily(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != OpenAIOAuthTokenURL {
			return http.StatusNotFound, `{"error":"wrong_endpoint"}`
		}
		return http.StatusOK, openAITokenPayload("openai-access-1")
	}

	// auth-url: strict body + authorize URL shape.
	code, badBody := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{"bogus":1}`)
	if code != http.StatusBadRequest || badBody["message"] != "OpenAI 授权链接参数无效" {
		t.Fatalf("auth-url strict: %d %v", code, badBody)
	}
	code, authURLPayload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("auth-url: %d %v", code, authURLPayload)
	}
	authData := dataMap(t, authURLPayload)
	parsed, err := url.Parse(authData["authUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "auth.openai.com" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("authorize url: %v", parsed)
	}
	query := parsed.Query()
	if query.Get("client_id") != OpenAIOAuthClientID ||
		query.Get("redirect_uri") != OpenAIOAuthDefaultRedirect ||
		query.Get("scope") != OpenAIOAuthDefaultScopes ||
		query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" ||
		query.Get("code_challenge") == "" || query.Get("state") == "" ||
		query.Get("id_token_add_organizations") != "true" ||
		query.Get("codex_cli_simplified_flow") != "true" {
		t.Fatalf("authorize params: %v", query)
	}
	sessionID := authData["sessionId"].(string)
	state := query.Get("state")

	// create-from-code: exchange via mock → account row + credentials.
	createBody := fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"http://localhost:1455/auth/callback?code=auth-code-1&state=%s","providerProtocolProfileId":"profile_gpt_openai_v1"}`, sessionID, url.QueryEscape(state))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code", createBody)
	if code != http.StatusCreated {
		t.Fatalf("create-from-code: %d %v", code, created)
	}
	createData := dataMap(t, created)
	accountID := createData["id"].(string)
	if createData["status"] != "pending_test" {
		t.Fatalf("create status: %v", createData)
	}
	var vendorCode, accountType, name, ownerID string
	var configRevision int64
	// 行身份键 = vendor_code（§2.1：真实厂商 == provider_code）；profile 归属
	// 由路由层 resolveProviderProfile 校验，目标列集已无该列（§9.11）。
	if err := env.db.QueryRow(`SELECT vendor_code, type, name, system_account_id, config_revision
		FROM accounts WHERE id = ?`, accountID).Scan(&vendorCode, &accountType, &name, &ownerID, &configRevision); err != nil {
		t.Fatal(err)
	}
	if vendorCode != "gpt" || accountType != "oauth" ||
		!strings.HasPrefix(name, "api.openai.com-") || ownerID != adminID || configRevision != 1 {
		t.Fatalf("account row: %s %s %s %s %d", vendorCode, accountType, name, ownerID, configRevision)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND enabled = 1`, accountID) != 1 {
		t.Fatal("account must join the owner default group")
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "openai-access-1" ||
		credentials["refresh_token"] != "openai-refresh-2" ||
		credentials["base_url"] != "https://api.openai.com/v1" ||
		credentials["client_id"] != OpenAIOAuthClientID ||
		credentials["email"] != "dev@example.com" ||
		credentials["account_id"] != "acct-1" ||
		credentials["chatgpt_user_id"] != "user-1" ||
		credentials["plan_type"] != "plus" {
		t.Fatalf("credentials: %v", credentials)
	}
	if _, ok := credentials["expires_at"].(string); !ok {
		t.Fatalf("expires_at missing: %v", credentials)
	}
	// Exchange request shape: PKCE form POST against the token endpoint.
	calls := env.exchanger.recorded()
	if len(calls) != 1 {
		t.Fatalf("exchange calls: %d", len(calls))
	}
	form := mustForm(t, calls[0].Body)
	if calls[0].URL != OpenAIOAuthTokenURL ||
		form.Get("grant_type") != "authorization_code" ||
		form.Get("code") != "auth-code-1" ||
		form.Get("state") != "" ||
		form.Get("redirect_uri") != OpenAIOAuthDefaultRedirect ||
		form.Get("client_id") != OpenAIOAuthClientID ||
		form.Get("code_verifier") == "" {
		t.Fatalf("exchange request: %v %v", calls[0].URL, form)
	}
	if calls[0].Headers["content-type"] != "application/x-www-form-urlencoded" {
		t.Fatalf("exchange headers: %v", calls[0].Headers)
	}
	if !env.sink.has("openai_oauth.create_from_code") {
		t.Fatalf("operation log: %v", env.sink.actions())
	}

	// Session single consumption: same session, fresh callback → 502 fallback.
	code, consumed := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"http://localhost:1455/auth/callback?code=auth-code-2&state=%s","providerProtocolProfileId":"profile_gpt_openai_v1"}`, sessionID, url.QueryEscape(state)))
	if code != http.StatusBadGateway || consumed["message"] != "OpenAI 授权码交换失败" {
		t.Fatalf("consumed session: %d %v", code, consumed)
	}

	// Wrong state → 502 fallback.
	code, freshAuth := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	freshSession := dataMap(t, freshAuth)["sessionId"].(string)
	code, wrongState := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"http://localhost:1455/auth/callback?code=auth-code-3&state=wrong","providerProtocolProfileId":"profile_gpt_openai_v1"}`, freshSession))
	if code != http.StatusBadGateway || wrongState["message"] != "OpenAI 授权码交换失败" {
		t.Fatalf("wrong state: %d %v", code, wrongState)
	}

	// create-from-refresh-token: default client id + refresh scopes.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		form := mustForm(t, call.Body)
		if form.Get("grant_type") != "refresh_token" || form.Get("scope") != OpenAIOAuthRefreshScopes ||
			form.Get("client_id") != "custom-openai-client" {
			t.Fatalf("refresh request: %v", form)
		}
		return http.StatusOK, openAITokenPayload("openai-access-from-refresh")
	}
	code, fromRefresh := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"openai-refresh-A","clientId":"custom-openai-client","providerProtocolProfileId":"profile_gpt_openai_v1","name":"Named Account"}`)
	if code != http.StatusCreated {
		t.Fatalf("create-from-refresh-token: %d %v", code, fromRefresh)
	}
	refreshAccountID := dataMap(t, fromRefresh)["id"].(string)
	if credentials := env.accountCredentials(t, refreshAccountID); credentials["access_token"] != "openai-access-from-refresh" {
		t.Fatalf("refresh credentials: %v", credentials)
	}

	// Manual refresh: revision +1 and rotated credentials.
	env.exchanger.respond = staticToken(openAITokenPayload("openai-access-rotated"))
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+refreshAccountID+"/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("refresh-token: %d %v", code, refreshed)
	}
	receipt := dataMap(t, refreshed)
	if receipt["id"] != refreshAccountID || receipt["configRevision"] != float64(2) {
		t.Fatalf("refresh receipt: %v", receipt)
	}
	if credentials := env.accountCredentials(t, refreshAccountID); credentials["access_token"] != "openai-access-rotated" {
		t.Fatalf("rotated credentials: %v", credentials)
	}
	if env.sink.has("openai_oauth.refresh_token") {
		// logged below via actions assertion
	} else {
		t.Fatalf("refresh log missing: %v", env.sink.actions())
	}

	// Stale revision → 409 with the openai copy.
	code, stale := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+refreshAccountID+"/refresh-token",
		`{"expectedConfigRevision":1}`)
	if code != http.StatusConflict || stale["message"] != "OpenAI OAuth 账户已被其他操作更新，请刷新页面后重试" {
		t.Fatalf("stale revision: %d %v", code, stale)
	}

	// reauthorize-from-refresh-token keeps the current refresh token when the
	// upstream response omits one.
	env.exchanger.respond = staticToken(`{"access_token":"openai-access-reauth","expires_in":7200}`)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+refreshAccountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"openai-refresh-B","expectedConfigRevision":2}`)
	if code != http.StatusOK {
		t.Fatalf("reauthorize-from-refresh-token: %d %v", code, reauthorized)
	}
	if receipt := dataMap(t, reauthorized); receipt["configRevision"] != float64(3) {
		t.Fatalf("reauthorize receipt: %v", receipt)
	}
	credentials = env.accountCredentials(t, refreshAccountID)
	if credentials["access_token"] != "openai-access-reauth" {
		t.Fatalf("reauth credentials: %v", credentials)
	}
	// The rotated credentials keep the last upstream refresh token; the request
	// fallback only applies when the response omits one (it did).
	if credentials["refresh_token"] != "openai-refresh-B" {
		t.Fatalf("reauth refresh token fallback: %v", credentials)
	}

	// Unknown account → 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/acc-missing/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusNotFound || missing["message"] != "OpenAI OAuth 账户不存在或无权操作" {
		t.Fatalf("missing account: %d %v", code, missing)
	}

	if !env.sink.has("openai_oauth.reauthorize_from_refresh_token") {
		t.Fatalf("reauthorize log missing: %v", env.sink.actions())
	}
}

func TestAnthropicOAuthFamily(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	anthropicPayload := `{"access_token":"ant-access-1","refresh_token":"ant-refresh-1","expires_in":3600,
		"scope":"user:profile user:inference","token_type":"bearer",
		"account":{"email_address":"claude@example.com","uuid":"acc-uuid-1"},
		"organization":{"uuid":"org-uuid-1"}}`
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != AnthropicOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		if call.Headers["content-type"] != "application/json" || call.Headers["user-agent"] != "axios/1.13.6" {
			t.Fatalf("anthropic request headers: %v", call.Headers)
		}
		return http.StatusOK, anthropicPayload
	}

	// auth-url shape.
	code, authURLPayload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("anthropic auth-url: %d %v", code, authURLPayload)
	}
	parsed, err := url.Parse(dataMap(t, authURLPayload)["authUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "claude.ai" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("anthropic authorize url: %v", parsed)
	}
	query := parsed.Query()
	if query.Get("code") != "true" || query.Get("client_id") != AnthropicOAuthClientID ||
		query.Get("redirect_uri") != AnthropicOAuthRedirectURI ||
		query.Get("scope") != AnthropicOAuthBrowserScope ||
		query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("anthropic authorize params: %v", query)
	}
	sessionID := dataMap(t, authURLPayload)["sessionId"].(string)
	state := query.Get("state")

	// create-from-code via the query callback form (and a bare fragment form
	// for the reauthorize path).
	callback := "https://platform.claude.com/oauth/code/callback?code=anthropic-code&state=" + url.QueryEscape(state)
	createBody := fmt.Sprintf(`{"sessionId":%q,"callbackUrl":%q,"providerProtocolProfileId":"profile_anthropic_anthropic_v1"}`, sessionID, callback)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-code", createBody)
	if code != http.StatusCreated {
		t.Fatalf("anthropic create-from-code: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	if name := dataMap(t, created); name["status"] != "pending_test" {
		t.Fatalf("anthropic create status: %v", name)
	}
	exchange := env.exchanger.recorded()[0]
	body := map[string]any{}
	if err := json.Unmarshal([]byte(exchange.Body), &body); err != nil {
		t.Fatalf("anthropic body: %v", err)
	}
	if body["grant_type"] != "authorization_code" || body["code"] != "anthropic-code" ||
		body["state"] != state || body["client_id"] != AnthropicOAuthClientID ||
		body["redirect_uri"] != AnthropicOAuthRedirectURI || body["code_verifier"] == "" {
		t.Fatalf("anthropic exchange body: %v", body)
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "ant-access-1" ||
		credentials["base_url"] != "https://api.anthropic.com/v1" ||
		credentials["client_id"] != AnthropicOAuthClientID ||
		credentials["refresh_token"] != "ant-refresh-1" ||
		credentials["email"] != "claude@example.com" ||
		credentials["account_id"] != "acc-uuid-1" ||
		credentials["organization_id"] != "org-uuid-1" ||
		credentials["scope"] != "user:profile user:inference" ||
		credentials["token_type"] != "bearer" {
		t.Fatalf("anthropic credentials: %v", credentials)
	}
	var accountName string
	if err := env.db.QueryRow(`SELECT name FROM accounts WHERE id = ?`, accountID).Scan(&accountName); err != nil {
		t.Fatal(err)
	}
	// 未显式命名时按“上游域名-毫秒时间戳后 6 位”自动命名（四家 OAuth 统一）。
	if !strings.HasPrefix(accountName, "api.anthropic.com-") {
		t.Fatalf("anthropic account name: %s", accountName)
	}

	// refresh-token + reauthorize-from-refresh-token.
	env.exchanger.respond = staticToken(`{"access_token":"ant-access-2","refresh_token":"ant-refresh-2","expires_in":3600,"token_type":"bearer"}`)
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, refreshed)["configRevision"] != float64(2) {
		t.Fatalf("anthropic refresh: %d %v", code, refreshed)
	}
	credentials = env.accountCredentials(t, accountID)
	if credentials["access_token"] != "ant-access-2" || credentials["base_url"] != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic rotated credentials: %v", credentials)
	}
	env.exchanger.respond = staticToken(`{"access_token":"ant-access-3","refresh_token":"ant-refresh-3","expires_in":3600,"token_type":"bearer"}`)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"ant-refresh-3","expectedConfigRevision":2}`)
	if code != http.StatusOK || dataMap(t, reauthorized)["configRevision"] != float64(3) {
		t.Fatalf("anthropic reauthorize: %d %v", code, reauthorized)
	}
	credentials = env.accountCredentials(t, accountID)
	if credentials["refresh_token"] != "ant-refresh-3" {
		t.Fatalf("anthropic reauth refresh token: %v", credentials)
	}
	if !env.sink.has("anthropic_oauth.create_account") || !env.sink.has("anthropic_oauth.refresh_token") ||
		!env.sink.has("anthropic_oauth.reauthorize_from_refresh_token") {
		t.Fatalf("anthropic logs: %v", env.sink.actions())
	}
}

func TestGeminiOAuthCapabilitiesAndFamily(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// capabilities document.
	code, capabilities := env.do(t, http.MethodGet, "/__aisys__/api/gemini-oauth/capabilities", "")
	if code != http.StatusOK {
		t.Fatalf("capabilities: %d %v", code, capabilities)
	}
	caps := dataMap(t, capabilities)
	if caps["defaultOAuthType"] != "code_assist" {
		t.Fatalf("capabilities default: %v", caps)
	}
	entries := caps["oauthTypes"].([]any)
	if len(entries) != 3 {
		t.Fatalf("capabilities types: %v", entries)
	}
	aiStudio := entries[2].(map[string]any)
	if aiStudio["oauthType"] != "ai_studio" || aiStudio["usesBuiltInClient"] != false ||
		aiStudio["requiresClientCredentials"] != true ||
		aiStudio["redirectUri"] != GeminiOAuthRedirectURI ||
		aiStudio["scope"] != GeminiOAuthScope ||
		len(aiStudio["supportedEndpointModes"].([]any)) != 0 {
		t.Fatalf("ai_studio capability: %v", aiStudio)
	}
	codeAssist := entries[0].(map[string]any)
	if codeAssist["redirectUri"] != GeminiCLIOAuthRedirectURI ||
		codeAssist["scope"] != GeminiCodeAssistOAuthScope ||
		len(codeAssist["supportedEndpointModes"].([]any)) != 2 {
		t.Fatalf("code_assist capability: %v", codeAssist)
	}

	// auth-url (code_assist default) shape.
	code, authURLPayload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{}`)
	if code != http.StatusOK {
		t.Fatalf("gemini auth-url: %d %v", code, authURLPayload)
	}
	authData := dataMap(t, authURLPayload)
	parsed, err := url.Parse(authData["authUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "accounts.google.com" || parsed.Path != "/o/oauth2/v2/auth" {
		t.Fatalf("gemini authorize url: %v", parsed)
	}
	query := parsed.Query()
	if query.Get("client_id") != GeminiCLIOAuthClientID ||
		query.Get("redirect_uri") != GeminiCLIOAuthRedirectURI ||
		query.Get("scope") != GeminiCodeAssistOAuthScope ||
		query.Get("access_type") != "offline" || query.Get("prompt") != "consent" ||
		query.Get("include_granted_scopes") != "true" ||
		query.Get("code_challenge_method") != "S256" || query.Get("state") == "" {
		t.Fatalf("gemini authorize params: %v", query)
	}
	if authData["state"] == "" || authData["sessionId"] == "" {
		t.Fatalf("gemini auth-url payload: %v", authData)
	}

	// ai_studio without client credentials → 500 with the resolver message.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{"oauthType":"ai_studio"}`)
	if code != http.StatusInternalServerError || missing["message"] != "Gemini AI Studio OAuth 需要同时配置 Client ID 和 Client Secret" {
		t.Fatalf("ai_studio missing credentials: %d %v", code, missing)
	}

	// create-from-refresh-token (code_assist default): builtin client form,
	// tier defaults, endpoint modes, project binding.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		form := mustForm(t, call.Body)
		if form.Get("grant_type") != "refresh_token" || form.Get("client_secret") == "" ||
			form.Get("client_id") == "" {
			t.Fatalf("gemini refresh request: %v", form)
		}
		return http.StatusOK, `{"access_token":"gem-access-1","refresh_token":"gem-refresh-1","expires_in":3600,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`
	}
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gemini-refresh-A","projectId":"proj-1","providerProtocolProfileId":"profile_gemini_native_v1beta"}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini create-from-refresh-token: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	var accountName string
	if err := env.db.QueryRow(`SELECT name FROM accounts WHERE id = ?`, accountID).Scan(&accountName); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(accountName, "cloudcode-pa.googleapis.com-") {
		t.Fatalf("gemini account name: %s", accountName)
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "gem-access-1" ||
		credentials["oauth_type"] != "code_assist" ||
		credentials["base_url"] != "https://cloudcode-pa.googleapis.com" ||
		credentials["project_id"] != "proj-1" ||
		credentials["tier_id"] != "gcp_standard" ||
		credentials["client_id"] != GeminiCLIOAuthClientID {
		t.Fatalf("gemini credentials: %v", credentials)
	}
	modes, ok := credentials["supported_endpoint_modes"].([]any)
	if !ok || len(modes) != 2 || modes[0] != "generate_content_json" {
		t.Fatalf("gemini endpoint modes: %v", credentials["supported_endpoint_modes"])
	}

	// ai_studio path: explicit client credentials, generativelanguage base URL,
	// no endpoint modes.
	code, createdStudio := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gemini-refresh-B","oauthType":"ai_studio","clientId":"studio-client","clientSecret":"studio-secret","providerProtocolProfileId":"profile_gemini_native_v1beta","name":"Studio"}`)
	if code != http.StatusCreated {
		t.Fatalf("gemini ai_studio create: %d %v", code, createdStudio)
	}
	studioID := dataMap(t, createdStudio)["id"].(string)
	studioCredentials := env.accountCredentials(t, studioID)
	if studioCredentials["oauth_type"] != "ai_studio" ||
		studioCredentials["base_url"] != "https://generativelanguage.googleapis.com" ||
		studioCredentials["client_id"] != "studio-client" ||
		studioCredentials["tier_id"] != "aistudio_free" {
		t.Fatalf("studio credentials: %v", studioCredentials)
	}
	// The ai_studio builder must not pin the CLI mode pair; the stored record
	// still carries the google_oauth normalization defaults (Node
	// normalizeGoogleOAuthAccountCredentials always writes
	// supported_endpoint_modes; absent input falls back to
	// defaultGeminiEndpointModes' five values).
	if modes, ok := studioCredentials["supported_endpoint_modes"].([]any); !ok || len(modes) != 5 ||
		modes[0] != "generate_content_json" || modes[1] != "generate_content_sse" {
		t.Fatalf("ai_studio endpoint modes must be the five gemini defaults, not the CLI pair: %v", studioCredentials["supported_endpoint_modes"])
	}

	// refresh-token on the code_assist account keeps the stored base_url.
	env.exchanger.respond = staticToken(`{"access_token":"gem-access-2","expires_in":3600,"token_type":"Bearer"}`)
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/accounts/"+accountID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, refreshed)["configRevision"] != float64(2) {
		t.Fatalf("gemini refresh: %d %v", code, refreshed)
	}
	credentials = env.accountCredentials(t, accountID)
	if credentials["access_token"] != "gem-access-2" || credentials["base_url"] != "https://cloudcode-pa.googleapis.com" {
		t.Fatalf("gemini rotated credentials: %v", credentials)
	}
	if !env.sink.has("gemini_oauth.create_account") || !env.sink.has("gemini_oauth.refresh_token") {
		t.Fatalf("gemini logs: %v", env.sink.actions())
	}
}
