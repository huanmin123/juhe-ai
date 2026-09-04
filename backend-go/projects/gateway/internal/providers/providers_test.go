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
// reads (providers, provider_protocol_profiles, provider_model_catalog) plus
// the auth tables.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, parent_code TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, base_url TEXT NOT NULL, default_health_check_model TEXT NOT NULL, account_types_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS provider_model_catalog (
		id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
		mode TEXT, catalog_order INTEGER, release_date TEXT, shutdown_date TEXT,
		supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
		supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
		supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
		default_reasoning_effort TEXT,
		context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER,
		input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL,
		supports_prompt_caching INTEGER NOT NULL DEFAULT 0,
		catalog_visible INTEGER NOT NULL DEFAULT 1,
		source TEXT NOT NULL DEFAULT 'built_in',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	db     *sql.DB
}

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
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accountStore, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	store, err := NewStore(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, db: db}
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
		if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
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

// seedCatalog mirrors the Node catalog fixtures: three providers, the gpt
// preferred-profile ordering case, the gemini preferred-id case and the
// model catalog rows.
func (e *testEnv) seedCatalog(t *testing.T) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	e.exec(t, `INSERT INTO providers (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-anthropic', 'anthropic', 'Anthropic', 'Claude 官方', 0, '[]', ?, ?)`, now, now)
	e.exec(t, `INSERT INTO providers (id, code, name, description, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gemini', 'gemini', 'Gemini', NULL, 1, '["gemini-2.5-pro"]', ?, ?)`, now, now)
	e.exec(t, `INSERT INTO providers (id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt', 'gpt', 'OpenAI', NULL, NULL, 1, '["gpt-4o-mini","gpt-4o"]', ?, ?)`, now, now)

	// gpt: a disabled profile with the newest timestamp must lose against the
	// enabled profiles (enabled DESC first); the newer enabled profile wins.
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-gpt-a', 'gpt', 'OpenAI 官方 A', 1, 'openai', 'v1', 'https://api.a.openai.com/v1',
		'gpt-4o-mini', '["api_key"]', '["tools"]', ?, ?)`, now, "2026-01-01T00:00:00.000Z")
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-gpt-b', 'gpt', 'OpenAI 官方 B', 1, 'openai', 'v1', 'https://api.b.openai.com/v1',
		'gpt-4.1-mini', '["api_key","oauth"]', '[]', ?, ?)`, now, "2026-01-02T00:00:00.000Z")
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-gpt-c', 'gpt', 'OpenAI 归档', 0, 'openai', 'v1', 'https://archive.openai.com/v1',
		'gpt-3.5-turbo', '[]', '[]', ?, ?)`, now, "2026-01-03T00:00:00.000Z")

	// gemini: the preferred id beats a newer enabled profile.
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('prof-gem-1', 'gemini', 'Gemini 新', 1, 'gemini', 'v1beta', 'https://gemini.example/v1beta',
		'gemini-2.5-flash', '[]', '[]', ?, ?)`, now, "2026-01-05T00:00:00.000Z")
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_gemini_native_v1beta', 'gemini', 'Gemini Native', 1, 'gemini', 'v1beta', 'https://native.example/v1beta',
		'gemini-2.5-pro', '[]', '[]', ?, ?)`, now, "2026-01-01T00:00:00.000Z")

	// gpt model catalog: two active rows (one carrying the preferred id
	// fields), one disabled row without catalog_order (NULL ordering branch).
	e.seedCatalogModel(t, "cat-1", "gpt", "gpt-4o", "active", ptrInt64(2), "text", "2024-05-13", "[]", nil, 0, 1, 1)
	e.seedCatalogModel(t, "cat-2", "gpt", "gpt-4o-mini", "active", ptrInt64(1), "text", "2024-07-18",
		`["chat_completions","responses"]`, ptrInt64(128000), 0.0, 1, 1)
	e.seedCatalogModel(t, "cat-3", "gpt", "gpt-4-secret", "disabled", nil, "text", "2024-01-01", "[]", nil, 0, 0, 1)
	e.seedCatalogModel(t, "cat-4", "gemini", "gemini-2.5-pro", "active", nil, "text", "2025-03-25",
		`["generate_content"]`, ptrInt64(1048576), 1.25, 1, 1)
}

func ptrInt64(value int64) *int64 { return &value }

func (e *testEnv) seedCatalogModel(t *testing.T, id, providerCode, model, status string, catalogOrder *int64,
	mode, releaseDate, protocols string, contextWindow *int64, inputUsd float64, promptCaching, catalogVisible int) {
	t.Helper()
	args := []any{id, providerCode, model, status, mode, releaseDate, protocols,
		promptCaching, catalogVisible, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"}
	columns := `id, provider_code, model, status, mode, release_date, supported_api_protocols_json,
		supports_prompt_caching, catalog_visible, created_at, updated_at`
	values := `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`
	if catalogOrder != nil {
		columns += `, catalog_order`
		values += `, ?`
		args = append(args, *catalogOrder)
	}
	if contextWindow != nil {
		columns += `, context_window_tokens, input_usd_per_1m`
		values += `, ?, ?`
		args = append(args, *contextWindow, inputUsd)
	}
	e.exec(t, `INSERT INTO provider_model_catalog (`+columns+`) VALUES (`+values+`)`, args...)
}

func TestProvidersManagementListPaginationAndKeyword(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, listed)
	}
	data := dataMap(t, listed)
	items := data["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("list items: %v", items)
	}
	// Ordered by name ASC: Anthropic (disabled), Gemini, OpenAI.
	if items[0].(map[string]any)["name"] != "Anthropic" || items[2].(map[string]any)["name"] != "OpenAI" {
		t.Fatalf("list order: %v", items)
	}
	anthropic := items[0].(map[string]any)
	if anthropic["enabled"] != false || anthropic["code"] != "anthropic" {
		t.Fatalf("anthropic row: %v", anthropic)
	}
	if anthropic["modelCatalogCount"] != float64(0) {
		t.Fatalf("anthropic catalog count: %v", anthropic["modelCatalogCount"])
	}
	gpt := items[2].(map[string]any)
	if gpt["enabled"] != true || gpt["modelCatalogCount"] != float64(3) {
		t.Fatalf("gpt row: %v", gpt)
	}
	// Preferred default profile: enabled beats newest-disabled; recency picks
	// prof-gpt-b. The baseUrl/health-check fields ride along.
	if gpt["defaultProtocolProfileId"] != "prof-gpt-b" || gpt["baseUrl"] != "https://api.b.openai.com/v1" ||
		gpt["defaultHealthCheckModel"] != "gpt-4.1-mini" || gpt["protocolVersion"] != "v1" {
		t.Fatalf("gpt default profile: %v", gpt)
	}
	gemini := items[1].(map[string]any)
	if gemini["defaultProtocolProfileId"] != "profile_gemini_native_v1beta" {
		t.Fatalf("gemini default profile: %v", gemini)
	}
	if gemini["modelCatalogCount"] != float64(1) {
		t.Fatalf("gemini catalog count: %v", gemini)
	}
	models := gemini["defaultSupportedModels"].([]any)
	if len(models) != 1 || models[0] != "gemini-2.5-pro" {
		t.Fatalf("gemini default supported models: %v", models)
	}
	if data["total"] != float64(3) || data["page"] != float64(1) || data["hasMore"] != false {
		t.Fatalf("list envelope: %v", data)
	}

	// Pagination probe.
	code, paged := env.do(t, http.MethodGet, "/__aisys__/api/providers?page=2&pageSize=1", "")
	if code != http.StatusOK {
		t.Fatalf("paged list: %d %v", code, paged)
	}
	pagedData := dataMap(t, paged)
	if len(pagedData["items"].([]any)) != 1 || pagedData["hasMore"] != true || pagedData["page"] != float64(2) {
		t.Fatalf("paged envelope: %v", pagedData)
	}

	// Keyword prefix over name (case-sensitive range, groups pattern).
	code, keyword := env.do(t, http.MethodGet, "/__aisys__/api/providers?keyword=Open", "")
	if code != http.StatusOK {
		t.Fatalf("keyword list: %d %v", code, keyword)
	}
	keywordItems := dataMap(t, keyword)["items"].([]any)
	if len(keywordItems) != 1 || keywordItems[0].(map[string]any)["code"] != "gpt" {
		t.Fatalf("keyword items: %v", keywordItems)
	}
}

func TestProvidersDetailByCodeAndID(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt", "")
	if code != http.StatusOK {
		t.Fatalf("detail by code: %d %v", code, detail)
	}
	data := dataMap(t, detail)
	if data["code"] != "gpt" || data["id"] != "prov-gpt" {
		t.Fatalf("detail row: %v", data)
	}
	profiles := data["protocolProfiles"].([]any)
	if len(profiles) != 3 {
		t.Fatalf("protocol profiles: %v", profiles)
	}
	first := profiles[0].(map[string]any)
	if first["id"] != "prof-gpt-c" || first["enabled"] != false {
		t.Fatalf("profile ordering: %v", first)
	}
	models := data["models"].([]any)
	if len(models) != 3 {
		t.Fatalf("models: %v", models)
	}
	// catalog_order ASC first, then the NULL-order rows by model name.
	if models[0].(map[string]any)["model"] != "gpt-4o-mini" ||
		models[1].(map[string]any)["model"] != "gpt-4o" ||
		models[2].(map[string]any)["model"] != "gpt-4-secret" {
		t.Fatalf("model ordering: %v", models)
	}
	mini := models[0].(map[string]any)
	if mini["status"] != "active" || mini["catalogVisible"] != true ||
		mini["contextWindowTokens"] != float64(128000) || mini["inputUsdPer1M"] != float64(0) {
		t.Fatalf("gpt-4o-mini catalog row: %v", mini)
	}
	secret := models[2].(map[string]any)
	if secret["status"] != "disabled" || secret["catalogVisible"] != true || secret["catalogOrder"] != nil {
		t.Fatalf("gpt-4-secret catalog row: %v", secret)
	}
	if protocols := mini["supportedApiProtocols"].([]any); len(protocols) != 2 || protocols[0] != "chat_completions" {
		t.Fatalf("supported protocols: %v", protocols)
	}

	// The row id resolves as well ({id} contract).
	code, byID := env.do(t, http.MethodGet, "/__aisys__/api/providers/prov-gpt", "")
	if code != http.StatusOK || dataMap(t, byID)["code"] != "gpt" {
		t.Fatalf("detail by id: %d %v", code, byID)
	}

	// Unknown provider → 404 供应商不存在.
	code, missing := env.do(t, http.MethodGet, "/__aisys__/api/providers/nope", "")
	if code != http.StatusNotFound || missing["message"] != "供应商不存在" {
		t.Fatalf("missing provider: %d %v", code, missing)
	}
}

func TestProvidersMySurfaceAndDeferredWrites(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.login(t, "user1", "user-pass", "user")

	// Self surface serves the same management catalog reads.
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/my-providers", "")
	if code != http.StatusOK {
		t.Fatalf("my list: %d %v", code, listed)
	}
	if len(dataMap(t, listed)["items"].([]any)) != 3 {
		t.Fatalf("my list items: %v", listed)
	}
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/my-providers/gpt", "")
	if code != http.StatusOK || dataMap(t, detail)["code"] != "gpt" {
		t.Fatalf("my detail: %d %v", code, detail)
	}

	// The C03-deferred write family renders 400 on the self surface.
	deferredSelf := [][2]string{
		{http.MethodPost, "/__aisys__/api/my-providers/gpt/models"},
		{http.MethodPatch, "/__aisys__/api/my-providers/gpt/models/custom_model_1"},
		{http.MethodDelete, "/__aisys__/api/my-providers/gpt/models/custom_model_1"},
		{http.MethodPut, "/__aisys__/api/my-providers/gpt/default-health-check-model"},
	}
	for _, entry := range deferredSelf {
		code, payload := env.do(t, entry[0], entry[1], `{}`)
		if code != http.StatusBadRequest || payload["message"] != "模型目录服务待迁移" {
			t.Fatalf("deferred write %s %s: %d %v", entry[0], entry[1], code, payload)
		}
	}

	// Admin surface deferrals.
	env.login(t, "root", "root-pass", "super_admin")
	deferredAdmin := [][2]string{
		{http.MethodPost, "/__aisys__/api/providers/gpt/models"},
		{http.MethodPatch, "/__aisys__/api/providers/gpt/models/custom_model_1"},
		{http.MethodDelete, "/__aisys__/api/providers/gpt/models/custom_model_1"},
		{http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model"},
	}
	for _, entry := range deferredAdmin {
		code, payload := env.do(t, entry[0], entry[1], `{}`)
		if code != http.StatusBadRequest || payload["message"] != "模型目录服务待迁移" {
			t.Fatalf("deferred admin write %s %s: %d %v", entry[0], entry[1], code, payload)
		}
	}

	// Anonymous callers stay 401 on both surfaces.
	env.mu.Lock()
	env.jar = map[string]string{}
	env.mu.Unlock()
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin list: %d %v", code, anonymous)
	}
	code, anonymous = env.do(t, http.MethodGet, "/__aisys__/api/my-providers", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous my list: %d %v", code, anonymous)
	}

	// Non-admin callers cannot read the admin surface.
	env.login(t, "user1", "user-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/providers", "")
	if code != http.StatusForbidden || forbidden["message"] != "需要管理员权限" {
		t.Fatalf("user admin list: %d %v", code, forbidden)
	}
}
