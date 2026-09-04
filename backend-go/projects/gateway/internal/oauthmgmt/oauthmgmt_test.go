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
}

var testSchema = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, parent_code TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, base_url TEXT NOT NULL, default_health_check_model TEXT NOT NULL, account_types_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, test_status TEXT NOT NULL DEFAULT 'unknown', latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 1,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
		system_account_id TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL,
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
		cooldown_retest_last_at TEXT,
		cooldown_retest_last_status_code INTEGER,
		temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1,
		next_health_check_at TEXT,
		balance_query_enabled INTEGER NOT NULL DEFAULT 0,
		balance_query_next_refresh_at TEXT,
		balance_query_config_json TEXT NOT NULL DEFAULT '{}',
		authorization_instance_source_account_id TEXT,
		authorization_instance_authorization_id TEXT,
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
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, model)
	)`,
	`CREATE TABLE IF NOT EXISTS account_model_mappings (
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, source_model TEXT NOT NULL,
		source_endpoint_family TEXT NOT NULL, upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (account_id, source_model, source_endpoint_family)
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
	store, err := NewStore(db, false, testSecret, accountStore, exchanger, nil, nil,
		WithSSODeviceTransport(sso), WithSSOSleep(func(context.Context, time.Duration) error { return nil }))
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
	}
}

// seedOAuthCatalog inserts the four providers plus their OAuth protocol
// profiles (provider_protocol_profiles rows).
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
		if _, err := db.Exec(`INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?)`, provider.id, provider.code, provider.code, provider.models, now, now); err != nil {
			return err
		}
	}
	for _, profile := range []struct {
		id, provider, protocol, version, accountTypes, healthCheck string
	}{
		{"profile_gpt_openai_v1", "gpt", "openai", "v1", `["api_key","oauth"]`, "gpt-4o-mini"},
		{"profile_anthropic_anthropic_v1", "anthropic", "anthropic", "v1", `["api_key","oauth"]`, "claude-sonnet-4-5"},
		{"profile_gemini_native_v1beta", "gemini", "gemini", "v1beta", `["api_key","google_oauth"]`, "gemini-2.5-pro"},
		{"profile_xai_openai_v1", "xai", "openai", "v1", `["oauth"]`, "grok-4"},
	} {
		if _, err := db.Exec(`INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
			protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
			capabilities_json, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, 'https://example.invalid', ?, ?, '[]', ?, ?)`,
			profile.id, profile.provider, profile.id, profile.protocol, profile.version,
			profile.healthCheck, profile.accountTypes, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *testEnv) seedDefaultGroups(t *testing.T, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, provider := range []string{"gpt", "anthropic", "gemini", "xai"} {
		e.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
			VALUES (?, ?, '默认分组', ?, 1, 1, 'personal', ?, ?)`,
			"grp-default-"+provider+"-"+ownerID, ownerID, provider, now, now)
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
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
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
	var providerCode, profileID, accountType, name, ownerID string
	var configRevision int64
	if err := env.db.QueryRow(`SELECT provider_code, provider_protocol_profile_id, type, name, system_account_id, config_revision
		FROM accounts WHERE id = ?`, accountID).Scan(&providerCode, &profileID, &accountType, &name, &ownerID, &configRevision); err != nil {
		t.Fatal(err)
	}
	if providerCode != "gpt" || profileID != "profile_gpt_openai_v1" || accountType != "oauth" ||
		name != "dev@example.com" || ownerID != adminID || configRevision != 1 {
		t.Fatalf("account row: %s %s %s %s %s %d", providerCode, profileID, accountType, name, ownerID, configRevision)
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
	if accountName != "claude@example.com" {
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
	if accountName != "Gemini OAuth Account" {
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
	if _, exists := studioCredentials["supported_endpoint_modes"]; exists {
		t.Fatalf("ai_studio must not pin endpoint modes: %v", studioCredentials)
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
