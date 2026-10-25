package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// schemaStatements mirrors the Node-owned catalog schema subset the slice
// reads, aligned with backend/src/storage/schema/business-schema.ts:
// providers, provider_protocol_profiles, provider_protocol_profile_families,
// protocol_endpoint_families, provider_model_catalog, custom_provider_models,
// provider_default_health_check_models,
// provider_system_default_health_check_models plus the auth tables.
// schemaStatements 是目录读写的目标 schema 子集（供应商与上游协议面重构设计
// §3/§8 的 fresh DDL 等价形态）：vendors（只读种子）+ models 单表 +
// account_supported_models / account_entry_mappings 目标形状 + auth 表。旧
// providers/provider_protocol_profiles/provider_model_catalog/
// custom_provider_models 与两张默认检查模型偏好表已随重构退役。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS vendors (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS models (
		id TEXT PRIMARY KEY, vendor_code TEXT, public_model TEXT NOT NULL, upstream_model TEXT NOT NULL,
		scope TEXT NOT NULL, system_account_id TEXT,
		status TEXT NOT NULL DEFAULT 'active', catalog_visible INTEGER NOT NULL DEFAULT 1,
		catalog_order INTEGER, release_date TEXT, shutdown_date TEXT,
		supported_endpoint_modes_json TEXT NOT NULL DEFAULT '[]',
		supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
		supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
		default_reasoning_effort TEXT,
		codex_supported_reasoning_levels_json TEXT NOT NULL DEFAULT '[]',
		codex_default_reasoning_level TEXT,
		codex_multi_agent_version TEXT,
		context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER, max_tokens INTEGER,
		input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL,
		cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL, cache_storage_usd_per_1m_per_hour REAL,
		service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
		long_context_input_token_threshold INTEGER, long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
		long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL,
		image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL,
		audio_output_usd_per_1m REAL, output_usd_per_image REAL,
		supports_prompt_caching INTEGER NOT NULL DEFAULT 0,
		source TEXT, pricing_notes TEXT, capability_notes TEXT, notes TEXT,
		created_by TEXT, updated_by TEXT,
		currency TEXT NOT NULL DEFAULT 'USD',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_models_identity_unique ON models (COALESCE(vendor_code, ''), scope, COALESCE(system_account_id, ''), upstream_model)`,
	`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT, name TEXT NOT NULL, deleted_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS account_supported_models (account_id TEXT NOT NULL, model_id TEXT NOT NULL, PRIMARY KEY (account_id, model_id))`,
	`CREATE TABLE IF NOT EXISTS account_entry_mappings (id TEXT PRIMARY KEY, account_id TEXT NOT NULL, source_model TEXT NOT NULL, target_model_id TEXT, enabled INTEGER NOT NULL DEFAULT 1)`,
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	db     *sql.DB
	// providersDeps is the mounted providers route bundle (the Sink hook).
	providersDeps *Deps
}

var mustChangeFalse = false

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:providers-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range schemaStatements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.Postgres, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := authsys.NewAccountStore(db, modelcheckauth.Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accountStore, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	store, err := NewStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	providersDeps := &Deps{Store: store, Auth: deps}
	providersDeps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, db: db, providersDeps: providersDeps}
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
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err != nil || existing.ID == "" {
		if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
		}); err != nil {
			t.Fatal(err)
		}
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	auth := payload["data"].(map[string]any)
	return auth["id"].(string)
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func dataArray(t *testing.T, payload map[string]any) []any {
	t.Helper()
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("missing data array: %v", payload)
	}
	return data
}

func jsonStringItems(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	output := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			output = append(output, text)
		}
	}
	return output
}

func clearSession(t *testing.T, e *testEnv) {
	t.Helper()
	e.mu.Lock()
	e.jar = map[string]string{}
	e.mu.Unlock()
}

func ptrInt64(value int64) *int64 { return &value }

func ptrInt(value int) *int { return &value }

func ptrFloat64(value float64) *float64 { return &value }

// seedCatalog 预置 vendors（anthropic 停用、gpt/gemini 启用；hybrid 不在
// vendors 值域，随设计退役）与 models 单表的 builtin/global/personal 行。协议
// 列表经包内 ForwardProtocolsToEndpointModes 正向落 supported_endpoint_modes_json
// （与 maintenance 种子投影同源）；旧偏好表/协议档案随表退役，overlay 断言
// 相应收空形状。
func (e *testEnv) seedCatalog(t *testing.T) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	e.exec(t, `INSERT INTO vendors (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-anthropic', 'anthropic', 'Anthropic', 'Claude 官方', 0, '[]', ?, ?)`, now, now)
	e.exec(t, `INSERT INTO vendors (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gemini', 'gemini', 'Gemini', NULL, 1, '["gemini-2.5-pro"]', ?, ?)`, now, now)
	e.exec(t, `INSERT INTO vendors (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', NULL, 1, '["gpt-4o-mini","gpt-4o"]', ?, ?)`, now, now)
	user1 := e.requireAccount(t, "user1", "user-pass", "user")
	// Built-in model catalog rows for gpt + gemini（models 单表 builtin 行）。
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-1", ProviderCode: "gpt", Model: "gpt-4o", Status: "active",
		CatalogOrder: ptrInt64(2), ReleaseDate: "2024-05-13", Protocols: `["chat_completions"]`,
		ContextWindow: ptrInt64(128000), InputUsd: ptrFloat64(5.0)})
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-2", ProviderCode: "gpt", Model: "gpt-4o-mini", Status: "active",
		CatalogOrder: ptrInt64(1), ReleaseDate: "2024-07-18", Protocols: `["chat_completions","responses"]`,
		ContextWindow: ptrInt64(128000), InputUsd: ptrFloat64(0),
		Tiers: `["priority","flex"]`, Efforts: `["low","medium"]`, DefaultEffort: "medium"})
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-3", ProviderCode: "gpt", Model: "gpt-4-secret", Status: "disabled",
		ReleaseDate: "2024-01-01", Protocols: `[]`, InputUsd: ptrFloat64(1.0)})
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-4", ProviderCode: "gemini", Model: "gemini-2.5-pro", Status: "active",
		ReleaseDate: "2025-03-25", Protocols: `["generate_content"]`, ContextWindow: ptrInt64(1048576), InputUsd: ptrFloat64(1.25)})
	// gpt-4o-realtime 行经逆映射派生 mode=audio + protocols=[realtime]，沿 M5b
	// 裁决按显式标注行收录进目录（models 单表无 NULL mode 列，与 chat 面同一
	// 派生语义）。
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-5", ProviderCode: "gpt", Model: "gpt-4o-realtime", Status: "active",
		ReleaseDate: "2025-01-01", Protocols: `["realtime"]`, InputUsd: ptrFloat64(5.0)})
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-6", ProviderCode: "gpt", Model: "gpt-4o-unpriced", Status: "active",
		Protocols: `["chat_completions"]`})
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-7", ProviderCode: "gpt", Model: "gpt-4o-expired", Status: "active",
		ReleaseDate: "2024-02-01", Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(2.0), ShutdownDate: "2020-01-01"})
	// Catalog-invisible row: the built-in availability filter must hide it
	// unless includeInactive lifts the whole predicate.
	e.seedBuiltinModel(t, builtinModelSeed{ID: "cat-8", ProviderCode: "gpt", Model: "gpt-4o-hidden", Status: "active",
		Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(2.5), CatalogVisible: ptrInt(0)})

	// Custom models: user1 personal, one global competing with the built-in
	// gpt-4o, one audio-named global row (unsupported-name filter).
	e.seedCustomModel(t, customModelSeed{ID: "cu-1", ProviderCode: "gpt", Model: "gpt-4o-personal", Scope: "personal",
		SystemAccountID: &user1, ReleaseDate: "2025-06-01", Protocols: `["chat_completions"]`,
		ContextWindow: ptrInt64(64000), InputUsd: ptrFloat64(3.0)})
	e.seedCustomModel(t, customModelSeed{ID: "cu-2", ProviderCode: "gpt", Model: "gpt-4o", Scope: "global",
		ReleaseDate: "2024-05-13", Protocols: `["chat_completions","responses"]`, InputUsd: ptrFloat64(6.0)})
	e.seedCustomModel(t, customModelSeed{ID: "cu-3", ProviderCode: "gpt", Model: "gpt-whisper-box", Scope: "global",
		Protocols: `["chat_completions"]`, InputUsd: ptrFloat64(1.5)})
}

// requireAccount creates the account on first use (the login helper reuses
// the same create-on-missing logic) so seeds can reference account ids.
func (e *testEnv) requireAccount(t *testing.T, username, password, role string) string {
	t.Helper()
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err == nil && existing.ID != "" {
		return existing.ID
	}
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{MustChangePassword: &mustChangeFalse,
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
	}); err != nil {
		t.Fatal(err)
	}
	account, err := e.deps.Accounts.FindByUsername(context.Background(), username)
	if err != nil || account.ID == "" {
		t.Fatalf("account %s missing after create: %v", username, err)
	}
	return account.ID
}

type builtinModelSeed struct {
	ID             string
	ProviderCode   string
	Model          string
	Status         string
	CatalogOrder   *int64
	ReleaseDate    string
	ShutdownDate   string
	Protocols      string
	Tiers          string
	Efforts        string
	DefaultEffort  string
	ContextWindow  *int64
	InputUsd       *float64
	CatalogVisible *int
}

// seedBuiltinModel 把 builtin 行写入 models 单表：Protocols（旧协议列表 JSON）
// 经 ForwardProtocolsToEndpointModes 正向落 supported_endpoint_modes_json（与
// maintenance 种子投影同源；未知协议直接失败暴露 fixture 错误）。
func (e *testEnv) seedBuiltinModel(t *testing.T, seed builtinModelSeed) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	if seed.Status == "" {
		seed.Status = "active"
	}
	catalogVisible := 1
	if seed.CatalogVisible != nil {
		catalogVisible = *seed.CatalogVisible
	}
	if seed.Tiers == "" {
		seed.Tiers = "[]"
	}
	if seed.Efforts == "" {
		seed.Efforts = "[]"
	}
	endpointModes := endpointModesJSONForProtocols(t, seed.Protocols)
	e.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status, catalog_order,
		release_date, shutdown_date, supported_endpoint_modes_json, supported_service_tiers_json,
		supported_reasoning_efforts_json, default_reasoning_effort, context_window_tokens, input_usd_per_1m,
		catalog_visible, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'builtin', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'built_in', ?, ?)`,
		seed.ID, seed.ProviderCode, seed.Model, seed.Model, seed.Status, seed.CatalogOrder,
		nullable(seed.ReleaseDate), nullable(seed.ShutdownDate), endpointModes, seed.Tiers, seed.Efforts,
		nullable(seed.DefaultEffort), seed.ContextWindow, seed.InputUsd, catalogVisible, now, now)
}

type customModelSeed struct {
	ID              string
	ProviderCode    string
	Model           string
	Scope           string
	SystemAccountID *string
	Status          string
	ReleaseDate     string
	Protocols       string
	ContextWindow   *int64
	InputUsd        *float64
}

// seedCustomModel 把 custom 行写入 models 单表（global 行 system_account_id
// NULL、personal 行携带 owner；public_model 与 upstream_model 同值写入）。
func (e *testEnv) seedCustomModel(t *testing.T, seed customModelSeed) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	if seed.Status == "" {
		seed.Status = "active"
	}
	endpointModes := endpointModesJSONForProtocols(t, seed.Protocols)
	e.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		release_date, supported_endpoint_modes_json, context_window_tokens, input_usd_per_1m,
		created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'seed', ?, ?)`,
		seed.ID, seed.ProviderCode, seed.Model, seed.Model, seed.Scope, seed.SystemAccountID, seed.Status,
		nullable(seed.ReleaseDate), endpointModes, seed.ContextWindow, seed.InputUsd, now, now)
}

// endpointModesJSONForProtocols 是测试 fixture 的协议→modes 正向投影（生产
// 同源：ForwardProtocolsToEndpointModes；空协议列表落 '[]'）。
func endpointModesJSONForProtocols(t *testing.T, protocolsJSON string) string {
	t.Helper()
	var protocols []string
	if strings.TrimSpace(protocolsJSON) != "" {
		if err := json.Unmarshal([]byte(protocolsJSON), &protocols); err != nil {
			t.Fatalf("parse protocols %s: %v", protocolsJSON, err)
		}
	}
	modes, err := ForwardProtocolsToEndpointModes(protocols)
	if err != nil {
		t.Fatalf("forward protocols %s: %v", protocolsJSON, err)
	}
	if modes == nil {
		return "[]"
	}
	encoded, err := json.Marshal(modes)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// TestProvidersListContract covers GET /list: the management fork
// (viewScope=admin) sees disabled providers, ordinary callers see enabled
// only, the list DTO carries no management extras, and capabilities /
// defaultHealthCheckModel are derived from the vendor's built-in models.
// TestProvidersDeriveVendorCatalogSummaries 钉住供应商摘要的只读派生口径：
// capabilities 是 builtin 可见目录行的协议并集（排除 hidden/expired/disabled），
// defaultHealthCheckModel 是最新发布时间且可用于文本生成的模型；纯媒体
// vendor 没有文本模型时回落最新目录模型。
func TestProvidersDeriveVendorCatalogSummaries(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	const now = "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO vendors (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-minimax', 'minimax', 'MiniMax', NULL, 1, '[]', ?, ?)`, now, now)
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-mm-1", ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3",
		Status: "active", ReleaseDate: "2026-07-31", Protocols: `["video"]`, InputUsd: ptrFloat64(1.0)})
	env.seedBuiltinModel(t, builtinModelSeed{ID: "cat-mm-2", ProviderCode: "minimax", Model: "speech-2.8-turbo",
		Status: "active", ReleaseDate: "2026-01-01", Protocols: `["audio_speech"]`, InputUsd: ptrFloat64(1.0)})

	items, err := env.providersDeps.Store.ListCatalogListItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]ProviderListItem{}
	for _, item := range items {
		byCode[item.Code] = item
	}
	gpt := byCode["gpt"]
	if got := strings.Join(gpt.Capabilities, ","); got != "chat_completions,realtime,responses" {
		t.Fatalf("gpt capabilities: %q", got)
	}
	if gpt.DefaultHealthCheckModel != "gpt-4o-mini" {
		t.Fatalf("gpt default health check model: %q", gpt.DefaultHealthCheckModel)
	}
	gemini := byCode["gemini"]
	if got := strings.Join(gemini.Capabilities, ","); got != "generate_content,stream_generate_content" {
		t.Fatalf("gemini capabilities: %q", got)
	}
	if gemini.DefaultHealthCheckModel != "gemini-2.5-pro" {
		t.Fatalf("gemini default health check model: %q", gemini.DefaultHealthCheckModel)
	}
	anthropic := byCode["anthropic"]
	if len(anthropic.Capabilities) != 0 || anthropic.DefaultHealthCheckModel != "" {
		t.Fatalf("model-less vendor summary: %+v", anthropic)
	}
	minimax := byCode["minimax"]
	if got := strings.Join(minimax.Capabilities, ","); got != "audio_speech,video" {
		t.Fatalf("minimax capabilities: %q", got)
	}
	if minimax.DefaultHealthCheckModel != "MiniMax-Hailuo-2.3" {
		t.Fatalf("media-only vendor must fall back to the newest catalog model: %q", minimax.DefaultHealthCheckModel)
	}
}

func TestProvidersListContract(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?viewScope=admin", "")
	if code != http.StatusOK {
		t.Fatalf("admin list: %d %v", code, listed)
	}
	items := dataArray(t, listed)
	// hybrid 不在 vendors 值域（设计 §8），管理面列表只含种子 vendors。
	if len(items) != 3 {
		t.Fatalf("admin list items: %v", items)
	}
	anthropic := items[0].(map[string]any)
	if anthropic["code"] != "anthropic" || anthropic["enabled"] != false {
		t.Fatalf("anthropic row: %v", anthropic)
	}
	// Node ProviderListItem keys: no protocolProfiles / modelCatalogCount /
	// defaultProtocolProfileId.
	for _, forbidden := range []string{"protocolProfiles", "modelCatalogCount", "defaultProtocolProfileId", "protocolVersion", "createdAt"} {
		if _, exists := anthropic[forbidden]; exists {
			t.Fatalf("list row carries %s: %v", forbidden, anthropic)
		}
	}
	if anthropic["protocolCode"] != "" || anthropic["baseUrl"] != "" || anthropic["defaultHealthCheckModel"] != "" {
		t.Fatalf("profile-less row defaults: %v", anthropic)
	}
	// 协议档案随表退役；capabilities/defaultHealthCheckModel 由 builtin 目录派生。
	gpt := items[2].(map[string]any)
	if gpt["code"] != "gpt" || gpt["baseUrl"] != "" || gpt["protocolCode"] != "" {
		t.Fatalf("gpt row: %v", gpt)
	}
	if got := strings.Join(jsonStringItems(gpt["capabilities"]), ","); got != "chat_completions,realtime,responses" {
		t.Fatalf("gpt capabilities: %q (%v)", got, gpt)
	}
	if gpt["defaultHealthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("gpt default health check model: %v", gpt)
	}
	gemini := items[1].(map[string]any)
	if got := strings.Join(jsonStringItems(gemini["capabilities"]), ","); got != "generate_content,stream_generate_content" {
		t.Fatalf("gemini capabilities: %q (%v)", got, gemini)
	}
	if gemini["defaultHealthCheckModel"] != "gemini-2.5-pro" {
		t.Fatalf("gemini default health check model: %v", gemini)
	}

	// user1: enabled only; derived values are scope-independent.
	env.login(t, "user1", "user-pass", "user")
	code, userListed := env.do(t, http.MethodGet, "/__aisys__/api/providers/list", "")
	if code != http.StatusOK {
		t.Fatalf("user list: %d %v", code, userListed)
	}
	userItems := dataArray(t, userListed)
	if len(userItems) != 2 {
		t.Fatalf("user list items (enabled only): %v", userItems)
	}
	for _, item := range userItems {
		if item.(map[string]any)["code"] == "anthropic" {
			t.Fatalf("disabled provider leaked to non-management list: %v", userItems)
		}
	}
	for _, item := range userItems {
		row := item.(map[string]any)
		if row["code"] == "gpt" && row["defaultHealthCheckModel"] != "gpt-4o-mini" {
			t.Fatalf("user1 derived default: %v", row)
		}
	}

	// viewScope values other than admin keep the enabled-only fork.
	code, selfListed := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?viewScope=self", "")
	if code != http.StatusOK || len(dataArray(t, selfListed)) != 2 {
		t.Fatalf("viewScope=self list: %d %v", code, selfListed)
	}

	// Anonymous callers are rejected by the global session auth.
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/providers/list", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous list: %d %v", code, anonymous)
	}
}

// TestProvidersRootFlatArrayAdminOnly covers GET /providers (requireAdmin):
// a flat ProviderDefinition array with protocolProfiles + endpointFamilies
// and the preferred default profile fields.
func TestProvidersRootFlatArrayAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusOK {
		t.Fatalf("admin root list: %d %v", code, listed)
	}
	definitions := dataArray(t, listed)
	if len(definitions) != 3 {
		t.Fatalf("root list rows: %v", definitions)
	}
	var gpt, gemini, anthropic map[string]any
	for _, row := range definitions {
		entry := row.(map[string]any)
		if _, exists := entry["protocolProfiles"]; !exists {
			t.Fatalf("definition without protocolProfiles: %v", entry)
		}
		switch entry["code"] {
		case "gpt":
			gpt = entry
		case "gemini":
			gemini = entry
		case "anthropic":
			anthropic = entry
		}
	}
	// 协议档案维度随表退役；capabilities/defaultHealthCheckModel 由目录派生。
	if gpt["defaultProtocolProfileId"] != "" || gpt["baseUrl"] != "" || gpt["protocolVersion"] != "" {
		t.Fatalf("gpt profile fields: %v", gpt)
	}
	if gpt["defaultHealthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("gpt derived default: %v", gpt)
	}
	if _, hasSystemDefault := gpt["systemDefaultHealthCheckModel"]; hasSystemDefault {
		t.Fatalf("gpt must not carry a system default: %v", gpt)
	}
	if _, hasSystemDefault := gemini["systemDefaultHealthCheckModel"]; hasSystemDefault {
		t.Fatalf("gemini must not carry a system default (retired storage): %v", gemini)
	}
	profiles := gpt["protocolProfiles"].([]any)
	if len(profiles) != 0 {
		t.Fatalf("gpt profiles: %v", profiles)
	}
	if anthropic["defaultProtocolProfileId"] != "" || len(anthropic["protocolProfiles"].([]any)) != 0 {
		t.Fatalf("anthropic empty profile defaults: %v", anthropic)
	}

	// Non-admin callers cannot read the admin surface.
	env.login(t, "user1", "user-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusForbidden || forbidden["message"] != "需要管理员权限" {
		t.Fatalf("user root list: %d %v", code, forbidden)
	}

	// Anonymous callers stay 401.
	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous root list: %d %v", code, anonymous)
	}
}

// TestProvidersOptionsAndDefinitions covers GET /options ({id, code, name,
// enabled}, enabled only) and GET /definitions (enabled ProviderDefinition
// rows with the overlay).
func TestProvidersOptionsAndDefinitions(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "user1", "user-pass", "user")

	code, options := env.do(t, http.MethodGet, "/__aisys__/api/providers/options", "")
	if code != http.StatusOK {
		t.Fatalf("options: %d %v", code, options)
	}
	optionRows := dataArray(t, options)
	if len(optionRows) != 2 {
		t.Fatalf("enabled options only: %v", optionRows)
	}
	first := optionRows[0].(map[string]any)
	if first["code"] != "gemini" || first["name"] != "Gemini" || first["enabled"] != true {
		t.Fatalf("option order/shape: %v", optionRows)
	}
	if len(first) != 4 {
		t.Fatalf("option key set: %v", first)
	}

	code, definitions := env.do(t, http.MethodGet, "/__aisys__/api/providers/definitions", "")
	if code != http.StatusOK {
		t.Fatalf("definitions: %d %v", code, definitions)
	}
	definitionRows := dataArray(t, definitions)
	if len(definitionRows) != 2 {
		t.Fatalf("enabled definitions only: %v", definitionRows)
	}
	for _, row := range definitionRows {
		entry := row.(map[string]any)
		if entry["code"] == "anthropic" {
			t.Fatalf("disabled definition leaked: %v", definitionRows)
		}
		if _, exists := entry["protocolProfiles"]; !exists {
			t.Fatalf("definition without protocolProfiles: %v", entry)
		}
		if entry["code"] == "gemini" {
			if _, hasSystemDefault := entry["systemDefaultHealthCheckModel"]; hasSystemDefault {
				t.Fatalf("gemini definition overlay (retired storage): %v", entry)
			}
		}
	}

	clearSession(t, env)
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/providers/options", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous options: %d %v", code, anonymous)
	}
	code, anonymous = env.do(t, http.MethodGet, "/__aisys__/api/providers/definitions", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous definitions: %d %v", code, anonymous)
	}
}

// TestProvidersDetailByCode covers GET /{code}: code-only lookup, the
// disabled-provider 404 fork gated by viewScope=admin and the preference
// overlay.
func TestProvidersDetailByCode(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Management fork: viewScope=admin sees the disabled provider.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic?viewScope=admin", "")
	if code != http.StatusOK {
		t.Fatalf("admin disabled detail: %d %v", code, detail)
	}
	data := dataMap(t, detail)
	if data["code"] != "anthropic" || data["enabled"] != false || data["id"] != "prov-anthropic" {
		t.Fatalf("anthropic detail: %v", data)
	}

	// Without viewScope=admin the disabled provider is 404 for everyone.
	code, missing := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic", "")
	if code != http.StatusNotFound || missing["message"] != "供应商不存在或已停用" {
		t.Fatalf("admin no-viewScope disabled detail: %d %v", code, missing)
	}
	env.login(t, "user1", "user-pass", "user")
	code, missing = env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic?viewScope=admin", "")
	if code != http.StatusNotFound || missing["message"] != "供应商不存在或已停用" {
		t.Fatalf("user viewScope detail: %d %v", code, missing)
	}

	// Enabled provider with derived catalog summary.
	code, gptDetail := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt", "")
	if code != http.StatusOK {
		t.Fatalf("gpt detail: %d %v", code, gptDetail)
	}
	gptData := dataMap(t, gptDetail)
	if gptData["defaultHealthCheckModel"] != "gpt-4o-mini" {
		t.Fatalf("gpt derived default: %v", gptData)
	}
	if _, hasModels := gptData["models"]; hasModels {
		t.Fatalf("detail must not embed models (Node /:code shape): %v", gptData)
	}

	// The row id no longer resolves (Node looks up by code only).
	code, byID := env.do(t, http.MethodGet, "/__aisys__/api/providers/prov-gpt", "")
	if code != http.StatusNotFound || byID["message"] != "供应商不存在或已停用" {
		t.Fatalf("id lookup must not resolve: %d %v", code, byID)
	}

	// Unknown provider.
	code, unknown := env.do(t, http.MethodGet, "/__aisys__/api/providers/nope", "")
	if code != http.StatusNotFound || unknown["message"] != "供应商不存在或已停用" {
		t.Fatalf("unknown provider: %d %v", code, unknown)
	}
}

// TestProvidersModelOptions covers GET /models/options: query validation,
// the disabled-provider 404, scope visibility, keyword/selectedIds/limit and
// the merged built-in/custom ranking.
func TestProvidersModelOptions(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	code, options := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt", "")
	if code != http.StatusOK {
		t.Fatalf("user options: %d %v", code, options)
	}
	rows := dataArray(t, options)
	want := []string{"gpt-4o-personal", "gpt-4o-realtime", "gpt-4o-mini", "gpt-4o", "gpt-4o-unpriced", "gpt-whisper-box"}
	if len(rows) != len(want) {
		t.Fatalf("options rows: %v", rows)
	}
	for index, model := range want {
		row := rows[index].(map[string]any)
		if row["id"] != model || row["name"] != model {
			t.Fatalf("option %d: %v (want %s)", index, row, model)
		}
	}
	personal := rows[0].(map[string]any)
	if personal["defaultReasoningEffort"] != nil {
		t.Fatalf("personal option default effort: %v", personal)
	}
	mini := rows[2].(map[string]any)
	if mini["defaultReasoningEffort"] != "medium" {
		t.Fatalf("mini option default effort: %v", mini)
	}

	// Admin without a filter does not see user1's personal model and the
	// global custom row wins the gpt-4o merge.
	env.login(t, "root", "root-pass", "super_admin")
	code, adminOptions := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt", "")
	if code != http.StatusOK {
		t.Fatalf("admin options: %d %v", code, adminOptions)
	}
	adminRows := dataArray(t, adminOptions)
	adminWant := []string{"gpt-4o-realtime", "gpt-4o-mini", "gpt-4o", "gpt-4o-unpriced", "gpt-whisper-box"}
	if len(adminRows) != len(adminWant) {
		t.Fatalf("admin options rows: %v", adminRows)
	}
	for index, model := range adminWant {
		if adminRows[index].(map[string]any)["id"] != model {
			t.Fatalf("admin option %d: %v (want %s)", index, adminRows[index], model)
		}
	}
	merged := adminRows[2].(map[string]any)
	if protocols := merged["supportedApiProtocols"].([]any); len(protocols) != 2 || protocols[1] != "responses" {
		t.Fatalf("global custom gpt-4o wins the merge: %v", merged)
	}

	// Keyword filter.
	code, keyword := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&keyword=mini", "")
	if code != http.StatusOK || len(dataArray(t, keyword)) != 1 || dataArray(t, keyword)[0].(map[string]any)["id"] != "gpt-4o-mini" {
		t.Fatalf("keyword options: %d %v", code, keyword)
	}

	// selectedIds survive keyword misses.
	code, selected := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&keyword=zzz&selectedIds=gpt-4o", "")
	if code != http.StatusOK || len(dataArray(t, selected)) != 1 || dataArray(t, selected)[0].(map[string]any)["id"] != "gpt-4o" {
		t.Fatalf("selectedIds options: %d %v", code, selected)
	}

	// limit caps the visible non-selected models.
	code, limited := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&limit=2", "")
	if code != http.StatusOK || len(dataArray(t, limited)) != 2 {
		t.Fatalf("limit options: %d %v", code, limited)
	}

	// Without a keyword selectedIds never narrow the WHERE: the response is
	// the selected model plus the first `limit` non-selected ones (Node
	// merges selected ∪ top-limit, the SQL selectedIds only pin the order).
	code, selectedOnly := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&selectedIds=gpt-4o", "")
	if code != http.StatusOK {
		t.Fatalf("selected-only options: %d %v", code, selectedOnly)
	}
	selectedRows := dataArray(t, selectedOnly)
	selectedWant := []string{"gpt-4o-realtime", "gpt-4o-mini", "gpt-4o", "gpt-4o-unpriced", "gpt-whisper-box"}
	if len(selectedRows) != len(selectedWant) {
		t.Fatalf("selected-only rows: %v", selectedRows)
	}
	for index, model := range selectedWant {
		if selectedRows[index].(map[string]any)["id"] != model {
			t.Fatalf("selected-only %d: %v (want %s)", index, selectedRows[index], model)
		}
	}

	// The same semantics with a tight limit: selected + the top-2 ranking
	// non-selected models.
	code, selectedLimited := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=gpt&selectedIds=gpt-4o&limit=2", "")
	if code != http.StatusOK {
		t.Fatalf("selected+limit options: %d %v", code, selectedLimited)
	}
	selectedLimitedRows := dataArray(t, selectedLimited)
	selectedLimitedWant := []string{"gpt-4o-realtime", "gpt-4o-mini", "gpt-4o"}
	if len(selectedLimitedRows) != len(selectedLimitedWant) {
		t.Fatalf("selected+limit rows: %v", selectedLimitedRows)
	}
	for index, model := range selectedLimitedWant {
		if selectedLimitedRows[index].(map[string]any)["id"] != model {
			t.Fatalf("selected+limit %d: %v (want %s)", index, selectedLimitedRows[index], model)
		}
	}

	// protocol 入参不再收窄源集（vendors 不携带协议维度，源扩展随
	// providers/provider_protocol_profiles 退役）：gemini 臂与默认臂同为全部
	// 启用 vendor，行级协议信息由 modes 派生投影承载。
	code, protocol := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?protocol=gemini", "")
	if code != http.StatusOK {
		t.Fatalf("protocol options: %d %v", code, protocol)
	}
	protocolRows := dataArray(t, protocol)
	protocolSeen := map[string]bool{}
	for _, row := range protocolRows {
		protocolSeen[row.(map[string]any)["id"].(string)] = true
	}
	if !protocolSeen["gemini-2.5-pro"] || !protocolSeen["gpt-4o-mini"] {
		t.Fatalf("protocol options must cover all enabled vendors: %v", protocolRows)
	}

	// Invalid queries render Node's verbatim messages.
	code, badLimit := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?limit=0", "")
	if code != http.StatusBadRequest || badLimit["message"] != "limit 必须是 1 到 50 的整数" {
		t.Fatalf("limit=0: %d %v", code, badLimit)
	}
	code, badLimit = env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?limit=abc", "")
	if code != http.StatusBadRequest || badLimit["message"] != "limit 必须是 1 到 50 的整数" {
		t.Fatalf("limit=abc: %d %v", code, badLimit)
	}
	code, badProtocol := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?protocol=bad", "")
	if code != http.StatusBadRequest || badProtocol["message"] != "protocol 必须是 openai、anthropic 或 gemini" {
		t.Fatalf("protocol=bad: %d %v", code, badProtocol)
	}

	// Disabled or unknown providerCode renders the 404 fork.
	code, disabled := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=anthropic", "")
	if code != http.StatusNotFound || disabled["message"] != "供应商不存在或已停用" {
		t.Fatalf("disabled provider options: %d %v", code, disabled)
	}
	code, unknown := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options?providerCode=nope", "")
	if code != http.StatusNotFound || unknown["message"] != "供应商不存在或已停用" {
		t.Fatalf("unknown provider options: %d %v", code, unknown)
	}
}

// TestProvidersModelsCatalog covers GET /{code}/models: merged scope
// priority, the active/priced/supported filters with the includeInactive /
// includeUnpriced forks, the disabled-provider fork and the DTO shape.
func TestProvidersModelsCatalog(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	env.login(t, "user1", "user-pass", "user")
	code, models := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", "")
	if code != http.StatusOK {
		t.Fatalf("user models: %d %v", code, models)
	}
	rows := dataArray(t, models)
	// gpt-4o-realtime 经 modes 逆映射派生 mode=audio + realtime 协议，沿 M5b
	// 裁决收录（models 单表无 NULL mode 列）。
	userWant := []string{"gpt-4o-personal", "gpt-4o-realtime", "gpt-4o-mini", "gpt-4o"}
	if len(rows) != len(userWant) {
		t.Fatalf("user model rows: %v", rows)
	}
	for index, model := range userWant {
		if rows[index].(map[string]any)["model"] != model {
			t.Fatalf("user model %d: %v (want %s)", index, rows[index], model)
		}
	}
	personal := rows[0].(map[string]any)
	if personal["scope"] != "personal" || personal["source"] != "custom-personal" || personal["systemAccountId"] != env.requireAccount(t, "user1", "user-pass", "user") {
		t.Fatalf("personal model row: %v", personal)
	}
	if _, exists := personal["catalogVisible"]; exists {
		t.Fatalf("custom rows carry no catalogVisible: %v", personal)
	}
	if personal["defaultReasoningEffort"] != nil {
		t.Fatalf("personal default effort must be null: %v", personal)
	}

	// Admin: no personal rows; the global custom gpt-4o wins over built-in.
	env.login(t, "root", "root-pass", "super_admin")
	code, adminModels := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models", "")
	if code != http.StatusOK {
		t.Fatalf("admin models: %d %v", code, adminModels)
	}
	adminRows := dataArray(t, adminModels)
	adminWant := []string{"gpt-4o-realtime", "gpt-4o-mini", "gpt-4o"}
	if len(adminRows) != len(adminWant) {
		t.Fatalf("admin model rows: %v", adminRows)
	}
	for index, model := range adminWant {
		if adminRows[index].(map[string]any)["model"] != model {
			t.Fatalf("admin model %d: %v (want %s)", index, adminRows[index], model)
		}
	}
	fourO := adminRows[2].(map[string]any)
	if fourO["scope"] != "global" || fourO["source"] != "custom-global" || fourO["inputUsdPer1M"] != float64(6) {
		t.Fatalf("merged gpt-4o row: %v", fourO)
	}
	mini := adminRows[1].(map[string]any)
	if mini["scope"] != "built_in" || mini["source"] != "built_in" || mini["inputUsdPer1M"] != float64(0) {
		t.Fatalf("gpt-4o-mini row: %v", mini)
	}
	if mini["supportsServiceTier"] != true || mini["defaultReasoningEffort"] != "medium" {
		t.Fatalf("gpt-4o-mini service tier fields: %v", mini)
	}
	for _, key := range []string{"supportedApiProtocols", "supportedServiceTiers", "supportedReasoningEfforts",
		"codexSupportedReasoningLevels", "inputModalities", "outputModalities", "supportedToolsByProtocol", "serviceTierPrices"} {
		if _, exists := mini[key]; !exists {
			t.Fatalf("built-in row missing %s: %v", key, mini)
		}
	}
	// catalogDisplay rides the merged catalog rows (withCatalogDisplay) with
	// the openai policy sections: token prices, reasoning levels, capacity.
	display, ok := mini["catalogDisplay"].([]any)
	if !ok || len(display) != 3 {
		t.Fatalf("catalogDisplay sections: %v", mini["catalogDisplay"])
	}
	firstSection := display[0].(map[string]any)
	if firstSection["key"] != "token_pricing" || firstSection["label"] != "Token 计费" {
		t.Fatalf("token pricing section: %v", firstSection)
	}
	items := firstSection["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["key"] != "input" || items[0].(map[string]any)["value"] != float64(0) {
		t.Fatalf("token pricing items: %v", items)
	}
	if display[1].(map[string]any)["key"] != "reasoning" || display[2].(map[string]any)["key"] != "capacity" {
		t.Fatalf("section order: %v", display)
	}

	// includeInactive lifts the SQL availability predicate: the disabled row,
	// the shutdown-expired row and the catalog-invisible row all come back
	// (dated rows by release desc, the release-less hidden row last).
	code, inactive := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models?includeInactive=true", "")
	if code != http.StatusOK {
		t.Fatalf("includeInactive: %d %v", code, inactive)
	}
	inactiveWant := []string{"gpt-4o-realtime", "gpt-4o-mini", "gpt-4o", "gpt-4o-expired", "gpt-4-secret", "gpt-4o-hidden"}
	inactiveRows := dataArray(t, inactive)
	if len(inactiveRows) != len(inactiveWant) {
		t.Fatalf("includeInactive rows: %v", inactiveRows)
	}
	for index, model := range inactiveWant {
		if inactiveRows[index].(map[string]any)["model"] != model {
			t.Fatalf("includeInactive %d: %v (want %s)", index, inactiveRows[index], model)
		}
	}

	// includeUnpriced keeps the availability predicate: the shutdown-expired
	// and catalog-invisible rows stay hidden, the unpriced row joins last.
	code, unpriced := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models?includeUnpriced=true", "")
	if code != http.StatusOK {
		t.Fatalf("includeUnpriced: %d %v", code, unpriced)
	}
	unpricedRows := dataArray(t, unpriced)
	if len(unpricedRows) != 4 || unpricedRows[3].(map[string]any)["model"] != "gpt-4o-unpriced" {
		t.Fatalf("includeUnpriced rows: %v", unpricedRows)
	}

	// Unsupported names stay out of the catalog（realtime 行沿 M5b 收录，见上）。
	for _, row := range adminRows {
		model := row.(map[string]any)["model"]
		if model == "gpt-whisper-box" {
			t.Fatalf("unsupported model leaked: %v", adminRows)
		}
	}

	// hybrid 不在 vendors 值域（设计 §8）：目录路由按未知供应商 404。
	code, hybrid := env.do(t, http.MethodGet, "/__aisys__/api/providers/hybrid/models?viewScope=admin", "")
	if code != http.StatusNotFound || hybrid["message"] != "供应商不存在" {
		t.Fatalf("hybrid models must be 404 (retired dimension): %d %v", code, hybrid)
	}

	// Disabled provider fork: Node uses the shorter message on this route.
	env.login(t, "user1", "user-pass", "user")
	code, disabled := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic/models", "")
	if code != http.StatusNotFound || disabled["message"] != "供应商不存在" {
		t.Fatalf("user disabled models: %d %v", code, disabled)
	}
	env.login(t, "root", "root-pass", "super_admin")
	code, disabledAdmin := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic/models?viewScope=admin", "")
	if code != http.StatusOK || len(dataArray(t, disabledAdmin)) != 0 {
		t.Fatalf("admin disabled models: %d %v", code, disabledAdmin)
	}
}

// TestProvidersModelCapabilities covers GET
// /{code}/models/{modelId}/capabilities: enabled-provider gate (no
// management bypass), the merged test-catalog resolution and scope
// visibility.
func TestProvidersModelCapabilities(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	env.login(t, "user1", "user-pass", "user")
	code, capability := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4o-mini/capabilities", "")
	if code != http.StatusOK {
		t.Fatalf("mini capabilities: %d %v", code, capability)
	}
	data := dataMap(t, capability)
	if data["id"] != "gpt-4o-mini" || data["name"] != "gpt-4o-mini" {
		t.Fatalf("capability identity: %v", data)
	}
	if protocols := data["supportedApiProtocols"].([]any); len(protocols) != 2 || protocols[1] != "responses" {
		t.Fatalf("capability protocols: %v", data)
	}
	if data["defaultReasoningEffort"] != "medium" {
		t.Fatalf("capability default effort: %v", data)
	}
	if _, exists := data["supportedServiceTiers"]; !exists {
		t.Fatalf("capability missing tiers: %v", data)
	}

	// Personal custom capabilities resolve for the owner.
	code, personal := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4o-personal/capabilities", "")
	if code != http.StatusOK || dataMap(t, personal)["id"] != "gpt-4o-personal" {
		t.Fatalf("personal capabilities: %d %v", code, personal)
	}

	// Admin cannot resolve user1's personal model.
	env.login(t, "root", "root-pass", "super_admin")
	code, personalAdmin := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4o-personal/capabilities", "")
	if code != http.StatusNotFound || personalAdmin["message"] != "模型不存在" {
		t.Fatalf("admin personal capabilities: %d %v", code, personalAdmin)
	}

	// Disabled provider is 404 for everyone (no management bypass).
	code, disabled := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic/models/claude-3/capabilities?viewScope=admin", "")
	if code != http.StatusNotFound || disabled["message"] != "供应商不存在或已停用" {
		t.Fatalf("disabled provider capabilities: %d %v", code, disabled)
	}

	// Disabled / expired / unknown models render 模型不存在.
	code, secret := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4-secret/capabilities?viewScope=admin", "")
	if code != http.StatusNotFound || secret["message"] != "模型不存在" {
		t.Fatalf("disabled model capabilities: %d %v", code, secret)
	}
	code, expired := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/gpt-4o-expired/capabilities", "")
	if code != http.StatusNotFound || expired["message"] != "模型不存在" {
		t.Fatalf("expired model capabilities: %d %v", code, expired)
	}
	code, unknown := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models/nope/capabilities", "")
	if code != http.StatusNotFound || unknown["message"] != "模型不存在" {
		t.Fatalf("unknown model capabilities: %d %v", code, unknown)
	}
}

// TestProvidersWriteSurfaceRemoved keeps the my-providers assertion of the
// former deferral test; the write contract itself lives in write_test.go.
func TestProvidersWriteSurfaceRemoved(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/my-providers", "")
	if code != http.StatusNotFound {
		t.Fatalf("my-providers must be gone: %d %v", code, gone)
	}
	code, gone = env.do(t, http.MethodGet, "/__aisys__/api/my-providers/gpt", "")
	if code != http.StatusNotFound {
		t.Fatalf("my-providers detail must be gone: %d %v", code, gone)
	}
}
