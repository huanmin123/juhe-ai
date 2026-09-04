package policyreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

func (s *recordingSink) has(action string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.Module+"."+entry.Action == action {
			return true
		}
	}
	return false
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

type policyTestEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
}

func newPolicyTestEnv(t *testing.T) *policyTestEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:policyreads-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS providers (code TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, protocol_code TEXT NOT NULL, protocol_version TEXT, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS response_inspection_policies (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL, scope_type TEXT NOT NULL, protocol_code TEXT NOT NULL, provider_code TEXT, match_json TEXT NOT NULL, action TEXT NOT NULL, notes TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS external_integration_sources (id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL, scopes_json TEXT NOT NULL, rate_limits_json TEXT, expires_at TEXT, notes TEXT, last_used_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS external_integration_source_tokens (id TEXT PRIMARY KEY, source_ref_id TEXT NOT NULL, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, token_secret_encrypted TEXT, token_prefix TEXT NOT NULL, token_suffix TEXT NOT NULL, status TEXT NOT NULL, scopes_json TEXT NOT NULL, expires_at TEXT, last_used_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, revoked_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS oauth_clients (id TEXT PRIMARY KEY, client_id TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, client_type TEXT NOT NULL, client_secret_hash TEXT, client_secret_ciphertext TEXT, redirect_uris_json TEXT NOT NULL, allowed_scopes_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
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
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	env := &policyTestEnv{deps: deps, k: k, sink: sink, inval: invalidator, jar: map[string]string{}, db: db}
	env.server = httptest.NewServer(k.Handler())
	t.Cleanup(env.server.Close)
	return env
}

func (e *policyTestEnv) mountInspection(t *testing.T) *InspectionStore {
	store, err := NewInspectionStore(e.db, false, nil, nil, e.inval)
	if err != nil {
		t.Fatal(err)
	}
	(&InspectionDeps{Store: store, Auth: e.deps, Sink: e.sink}).Mount(e.k)
	return store
}

func (e *policyTestEnv) mountExternal(t *testing.T) *ExternalStore {
	store, err := NewExternalStore(e.db, false, nil, nil, e.inval, "test-crypto-secret")
	if err != nil {
		t.Fatal(err)
	}
	(&ExternalDeps{Store: store, Auth: e.deps, Sink: e.sink}).Mount(e.k)
	return store
}

func (e *policyTestEnv) mountOAuth(t *testing.T, enabled bool, issuer string) *OAuthStore {
	store, err := NewOAuthStore(e.db, false, nil, nil, e.inval, "test-oidc-secret")
	if err != nil {
		t.Fatal(err)
	}
	(&OAuthDeps{Store: store, Auth: e.deps, OIDCEnabled: enabled, OIDCIssuer: issuer}).Mount(e.k)
	return store
}

func dataSlice(t *testing.T, payload map[string]any) []any {
	t.Helper()
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("missing data array: %v", payload)
	}
	return data
}

func (e *policyTestEnv) do(t *testing.T, method, path, body string) (int, map[string]any, http.Header) {
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
	return response.StatusCode, payload, response.Header
}

func (e *policyTestEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, payload, _ := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return created.ID
}

func (e *policyTestEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *policyTestEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func boolPtr(v bool) *bool { return &v }

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

// localizedBadRequest mirrors the gateway boundary contract: non-CJK issue
// messages (zod v3 defaults) are rewritten to the status default by
// localizeSystemErrorMessage, same as Node.
const localizedBadRequest = "请求参数无效"

func seedInspectionProviders(t *testing.T, e *policyTestEnv) {
	t.Helper()
	e.exec(t, `INSERT INTO providers (code, name, enabled) VALUES ('gpt', 'GPT 官方', 1), ('openai-res', 'OpenAI Res', 1), ('disabled-provider', '停用供应商', 0)`)
	e.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, protocol_code, protocol_version, enabled) VALUES
		('ppp-1', 'gpt', 'openai', 'v1', 1),
		('ppp-2', 'openai-res', 'openai', 'v1', 1),
		('ppp-3', 'openai-res', 'anthropic', 'v1', 1)`)
}

func TestInspectionListDefaultsAndProviderOptions(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies", "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, list)
	}
	result := dataMap(t, list)
	defaults, ok := result["defaultRules"].([]any)
	if !ok || len(defaults) != len(systemDefaultRules) {
		t.Fatalf("default rules: %v", result)
	}
	first := defaults[0].(map[string]any)
	if first["id"] != "default_openai_transient_precommit_error" || first["defaultRule"] != true ||
		first["editable"] != false || first["priority"] != float64(0) {
		t.Fatalf("first default rule: %v", first)
	}
	if _, hasUpdatedAt := first["updatedAt"]; hasUpdatedAt {
		t.Fatalf("default rule must omit updatedAt: %v", first)
	}
	for _, item := range defaults {
		rule := item.(map[string]any)
		if rule["id"] == "default_gpt_cyber_policy" && rule["providerName"] != "GPT 官方" {
			t.Fatalf("default gpt rule provider name: %v", rule)
		}
	}
	if policies, ok := result["policies"].([]any); !ok || len(policies) != 0 {
		t.Fatalf("policies must start empty: %v", result["policies"])
	}

	// Provider options: only enabled providers with an enabled profile for the
	// requested protocol, prefix keyword filter, case-insensitive.
	code, options, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=provider", "")
	if code != 200 {
		t.Fatalf("provider options status: %d %v", code, options)
	}
	optionItems := dataSlice(t, options)
	codes := []string{}
	for _, item := range optionItems {
		codes = append(codes, item.(map[string]any)["code"].(string))
	}
	if len(codes) != 2 || codes[0] != "gpt" || codes[1] != "openai-res" {
		t.Fatalf("provider options: %v", codes)
	}
	code, keywordOptions, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=provider&keyword=GPT", "")
	optionItems = dataSlice(t, keywordOptions)
	if code != 200 || len(optionItems) != 1 {
		t.Fatalf("keyword options: %d %v", code, keywordOptions)
	}
	code, protocolOptions, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=anthropic&scopeType=provider", "")
	if code != 200 || len(dataSlice(t, protocolOptions)) != 1 {
		t.Fatalf("anthropic options: %d %v", code, protocolOptions)
	}
	code, protocolScope, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=protocol", "")
	if code != 200 || len(dataSlice(t, protocolScope)) != 0 {
		t.Fatalf("protocol scope options: %d %v", code, protocolScope)
	}
	code, missing, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?scopeType=provider", "")
	if code != http.StatusBadRequest || missing["message"] != "请选择响应检查策略协议" {
		t.Fatalf("missing protocol: %d %v", code, missing)
	}
	code, badScope, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=elsewhere", "")
	if code != http.StatusBadRequest || badScope["message"] != localizedBadRequest {
		t.Fatalf("bad scope: %d %v", code, badScope)
	}
	code, extra, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=provider&page=2", "")
	if code != http.StatusBadRequest || extra["message"] != localizedBadRequest {
		t.Fatalf("extra query: %d %v", code, extra)
	}
}

func TestInspectionCreateAndDetail(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	body := `{"name":"策略A","scopeType":"provider","protocolCode":"openai","providerCode":"gpt","priority":7,` +
		`"match":{"errorCodes":["server_error","server_error"]},"action":"retry_next_account","notes":"备注"}`
	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	overview := dataMap(t, created)
	policyID := overview["id"].(string)
	if !strings.HasPrefix(policyID, "rip_") {
		t.Fatalf("policy id: %v", overview)
	}
	if overview["defaultRule"] != false || overview["editable"] != true || overview["providerName"] != "GPT 官方" ||
		overview["priority"] != float64(7) || overview["action"] != "retry_next_account" {
		t.Fatalf("create overview: %v", overview)
	}

	// Identical payload → guarded duplicate (409).
	code, duplicate, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies", body)
	if code != http.StatusConflict {
		t.Fatalf("duplicate create: %d %v", code, duplicate)
	}

	// Default rules are matched before DB rows.
	code, defaultDetail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/default_openai_error_object", "")
	detail := dataMap(t, defaultDetail)
	if code != 200 || detail["name"] != "OpenAI error 对象" || detail["notes"] == "" {
		t.Fatalf("default detail: %d %v", code, defaultDetail)
	}

	code, row, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	rowDetail := dataMap(t, row)
	match := rowDetail["match"].(map[string]any)
	if code != 200 || rowDetail["name"] != "策略A" || rowDetail["notes"] != "备注" {
		t.Fatalf("detail: %d %v", code, row)
	}
	errorCodes := match["errorCodes"].([]any)
	if len(errorCodes) != 1 || errorCodes[0] != "server_error" {
		t.Fatalf("match must dedupe items: %v", match)
	}

	code, missing, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/rip_missing", "")
	if code != http.StatusNotFound || missing["message"] != "响应检查策略不存在" {
		t.Fatalf("missing detail: %d %v", code, missing)
	}

	// Validation matrix. Non-CJK issue messages (zod v3 defaults) are
	// localized to the status default by the gateway boundary, exactly like
	// Node's localizeSystemErrorMessage.
	cases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"missing name", `{"scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`, 400, localizedBadRequest},
		{"empty name", `{"name":"  ","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`, 400, "规则名称不能为空"},
		{"unknown key", `{"name":"ok","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe","bogus":1}`, 400, localizedBadRequest},
		{"protocol binds provider", `{"name":"ok","scopeType":"protocol","protocolCode":"openai","providerCode":"gpt","match":{"errorCodes":["x"]},"action":"observe"}`, 400, "协议层响应检查策略不能绑定供应商"},
		{"provider missing provider", `{"name":"ok","scopeType":"provider","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`, 400, "供应商层响应检查策略必须选择供应商"},
		{"membership missing", `{"name":"ok","scopeType":"provider","protocolCode":"anthropic","providerCode":"gpt","match":{"errorCodes":["x"]},"action":"observe"}`, 400, "响应检查策略供应商必须使用同协议启用档案"},
		{"no matcher", `{"name":"ok","scopeType":"protocol","protocolCode":"openai","match":{},"action":"observe"}`, 400, "至少需要填写一个匹配条件"},
		{"bad protocol", `{"name":"ok","scopeType":"protocol","protocolCode":"ollama","match":{"errorCodes":["x"]},"action":"observe"}`, 400, localizedBadRequest},
		{"bad action", `{"name":"ok","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"explode"}`, 400, localizedBadRequest},
		{"unknown client profile", `{"name":"ok","scopeType":"protocol","protocolCode":"openai","match":{"clientProfiles":["unknown"]},"action":"observe"}`, 400, localizedBadRequest},
	}
	for _, tc := range cases {
		code, payload, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies", tc.body)
		if code != tc.status || payload["message"] != tc.message {
			t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, tc.status, tc.message)
		}
	}

	if !env.sink.has("response_inspection_policies.create") {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.inval.has("response_inspection_policy_created") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
}

func TestInspectionPatchLifecycle(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"策略P","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["a"]},"action":"observe"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	policyID := dataMap(t, created)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)

	// No-op patch: same normalized values.
	code, noop, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"策略P","match":{"errorCodes":["a","a"]}}`)
	if code != 200 || dataMap(t, noop)["name"] != "策略P" {
		t.Fatalf("noop patch: %d %v", code, noop)
	}
	if env.sink.has("response_inspection_policies.update") {
		t.Fatalf("noop patch must not log: %v", env.sink.actions())
	}

	// Real update.
	code, updated, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"策略P2","priority":42,"notes":null}`)
	if code != 200 || dataMap(t, updated)["name"] != "策略P2" || dataMap(t, updated)["priority"] != float64(42) {
		t.Fatalf("patch: %d %v", code, updated)
	}
	nextUpdatedAt := dataMap(t, updated)["updatedAt"].(string)
	if nextUpdatedAt == updatedAt {
		t.Fatal("patch must bump updatedAt")
	}
	if _, hasNotes := dataMap(t, updated)["notes"]; hasNotes {
		t.Fatalf("cleared notes must be omitted: %v", updated)
	}
	code, after, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	if _, hasNotes := dataMap(t, after)["notes"]; hasNotes {
		t.Fatalf("notes must be cleared: %v", after)
	}

	// Stale version → 409.
	code, stale, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"late"}`)
	if code != http.StatusConflict || stale["message"] != "响应检查策略已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", code, stale)
	}

	// Missing/blank expectedUpdatedAt → "Required"; no fields → refine issue.
	code, missing, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID, `{"name":"x"}`)
	if code != http.StatusBadRequest || missing["message"] != localizedBadRequest {
		t.Fatalf("missing version: %d %v", code, missing)
	}
	code, noFields, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`"}`)
	if code != http.StatusBadRequest || noFields["message"] != "至少需要提交一个变化字段" {
		t.Fatalf("no fields: %d %v", code, noFields)
	}
	code, badVersion, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"2026-01-01T00:00:00","name":"x"}`)
	if code != http.StatusBadRequest || badVersion["message"] != "响应检查策略版本无效" {
		t.Fatalf("bad version: %d %v", code, badVersion)
	}

	// Unknown id → 404.
	code, notFound, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/rip_nope",
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","name":"x"}`)
	if code != http.StatusNotFound || notFound["message"] != "响应检查策略不存在" {
		t.Fatalf("unknown patch: %d %v", code, notFound)
	}

	// Membership revalidation runs when the patch touches scope fields.
	code, membership, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","scopeType":"provider","providerCode":"disabled-provider"}`)
	if code != http.StatusBadRequest || membership["message"] != "响应检查策略供应商必须使用同协议启用档案" {
		t.Fatalf("membership: %d %v", code, membership)
	}
	if !env.sink.has("response_inspection_policies.update") {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.inval.has("response_inspection_policy_updated") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}

	// Store-level capacity guard (100 management rows).
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for i := 0; i < maxManagementResponseInspectionPolicies; i++ {
		env.exec(t, `INSERT INTO response_inspection_policies (id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, notes, created_at, updated_at)
			VALUES (?, ?, 1, 100, 'protocol', 'openai', NULL, '{"errorCodes":["x"]}', 'observe', NULL, ?, ?)`,
			fmt.Sprintf("rip_seed_%d", i), fmt.Sprintf("seed-%d", i), now, now)
	}
	code, capacity, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"overflow","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`)
	if code != http.StatusBadRequest || capacity["message"] != "响应检查策略最多允许 100 条" {
		t.Fatalf("capacity: %d %v", code, capacity)
	}
	_ = store
}

func TestInspectionDeleteAndPermissions(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	// Non-admin surfaces are refused; do the user checks before switching to
	// the admin session (the cookie jar keeps a single session).
	env.login(t, "alice", "alice-pass", "user")
	code, forbidden, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies", "")
	if code != http.StatusForbidden || forbidden["message"] != "需要管理员权限" {
		t.Fatalf("user list: %d %v", code, forbidden)
	}
	code, _, _ = env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"x","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`)
	if code != http.StatusForbidden {
		t.Fatalf("user create: %d", code)
	}

	env.login(t, "root", "root-pass", "super_admin")
	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"doomed","scopeType":"protocol","protocolCode":"openai","match":{"jsonPathsExists":["error"]},"action":"retry_no_avoidance"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	policyID := dataMap(t, created)["id"].(string)

	code, deleted, _ := env.do(t, http.MethodDelete, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	if code != 200 || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("delete: %d %v", code, deleted)
	}
	// An identical repeat hits the mutation guard (Node parity: dedupe 409
	// before the handler runs).
	code, repeated, _ := env.do(t, http.MethodDelete, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	if code != http.StatusConflict || repeated["message"] == "" {
		t.Fatalf("repeat delete must hit the guard: %d %v", code, repeated)
	}
	code, gone, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	if code != http.StatusNotFound || gone["message"] != "响应检查策略不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	if env.count(t, `SELECT COUNT(*) FROM response_inspection_policies WHERE id = ?`, policyID) != 0 {
		t.Fatal("row must be hard deleted")
	}
	if !env.sink.has("response_inspection_policies.delete") {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.inval.has("response_inspection_policy_deleted") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}

	// Anonymous access is refused.
	fresh := newPolicyTestEnv(t)
	fresh.mountInspection(t)
	code, anonymous, _ := fresh.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %v", code, anonymous)
	}
}
