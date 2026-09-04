package apikeys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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

const testSecret = "m07-apikeys-test-secret"

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

// sensitive verifies the "key" change entries are flagged sensitive and never
// carry key material.
func (s *recordingSink) sensitive(field string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, entry := range s.entries {
		for _, change := range entry.Changes {
			if change.Field != field {
				continue
			}
			if strings.Contains(change.After, "sk-") || strings.Contains(change.Before, "sk-") {
				return false
			}
			if change.Sensitive {
				found = true
			}
		}
	}
	return found
}

type recordingInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *recordingInvalidator) InvalidateValidation(apiKeyID, reason string, _ []string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason+" "+apiKeyID)
	return nil
}

func (i *recordingInvalidator) InvalidateQuota(apiKeyID, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason+" "+apiKeyID)
}

func (i *recordingInvalidator) InvalidateRuntime(apiKeyID, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason+" "+apiKeyID)
}

func (i *recordingInvalidator) has(reason string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, candidate := range i.reasons {
		if strings.HasPrefix(candidate, reason+" ") {
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
	store  *Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:apikeys-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			route_strategy_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT,
			key_hash TEXT NOT NULL UNIQUE,
			key_prefix TEXT NOT NULL,
			key_suffix TEXT NOT NULL,
			key_secret_encrypted TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			is_default INTEGER NOT NULL DEFAULT 0,
			purpose TEXT NOT NULL DEFAULT 'general',
			expires_at TEXT,
			quota_limits_json TEXT,
			availability_schedule_json TEXT,
			availability_schedule_next_check_at TEXT,
			last_used_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
		`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
			system_account_id TEXT NOT NULL,
			scope_type TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			window_hours INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id)
		)`,
		`CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
			api_key_id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_attempt_at TEXT,
			last_blocked_reason TEXT,
			last_error_message TEXT
		)`,
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
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, false, testSecret, nil, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, store: store}
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
	id := ""
	if existing, err := e.deps.Accounts.FindByUsername(context.Background(), username); err == nil {
		id = existing.ID
	}
	if id == "" {
		created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
			MustChangePassword: boolPtr(false),
		})
		if err != nil {
			t.Fatal(err)
		}
		id = created.ID
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return id
}

// seedDefaultRouteStrategy mirrors the preferred default the Node create flow
// falls back to: an active default strategy over the owner's default enabled
// gpt group.
func (e *testEnv) seedDefaultRouteStrategy(t *testing.T, ownerID, strategyID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, '默认分组', 'gpt', 1, 1, 'personal', ?, ?)`, "grp-"+strategyID, ownerID, now, now)
	e.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES (?, ?, 'GPT 默认策略路由', 'normal', 'active', 1, ?, ?)`, strategyID, ownerID, now, now)
	e.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)`, "rsg-"+strategyID, strategyID, ownerID, "grp-"+strategyID, now, now)
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

func (e *testEnv) queryCell(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value sql.NullString
	if err := e.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value.String
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

func TestAPIKeyAdminLifecycleAndSealedSecret(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	// Create (201): plaintext leaves exactly once, message contract included.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"alpha"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	created := dataMap(t, payload)
	if payload["message"] != "API Key 已创建，请立即复制完整密钥" {
		t.Fatalf("create message: %v", payload["message"])
	}
	plainKey := created["key"].(string)
	if !strings.HasPrefix(plainKey, "sk-") || len(plainKey) != 67 {
		t.Fatalf("key format: %v", plainKey)
	}
	if created["keyPrefix"] != plainKey[:8] || created["keySuffix"] != plainKey[len(plainKey)-8:] {
		t.Fatalf("prefix/suffix: %v", created)
	}
	revision := created["revision"].(string)
	if len(revision) != 27 || !strings.HasSuffix(revision, "Z") || revision[19] != '.' {
		t.Fatalf("revision format: %v", revision)
	}
	keyID := created["id"].(string)

	// Stored row: hash matches, ciphertext differs from plaintext, decrypts back.
	var storedHash, storedSealed string
	if err := env.db.QueryRow(`SELECT key_hash, key_secret_encrypted FROM api_keys WHERE id = ?`, keyID).
		Scan(&storedHash, &storedSealed); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(plainKey))
	if storedHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("key_hash: %v", storedHash)
	}
	if strings.Contains(storedSealed, plainKey) {
		t.Fatal("ciphertext must not contain the plaintext key")
	}
	var unsealed secretPayload
	if err := DecryptJSON(testSecret, storedSealed, &unsealed); err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if unsealed.Key != plainKey {
		t.Fatalf("round trip mismatch: %v", unsealed.Key)
	}
	if env.count(t, `SELECT COUNT(*) FROM api_keys WHERE id = ? AND is_default = 0 AND purpose = 'general' AND status = 'active' AND route_strategy_id = 'rs-default'`, keyID) != 1 {
		t.Fatal("create row contract violated")
	}

	// Encryption is non-deterministic: same plaintext → different ciphertexts,
	// both (and the stored seal) decrypt to the same plaintext.
	first, err := EncryptJSON(testSecret, secretPayload{Key: plainKey})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncryptJSON(testSecret, secretPayload{Key: plainKey})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two encryptions of the same plaintext must differ")
	}
	for _, envelope := range []string{first, second, storedSealed} {
		var check secretPayload
		if err := DecryptJSON(testSecret, envelope, &check); err != nil || check.Key != plainKey {
			t.Fatalf("envelope round trip: %v %v", err, check.Key)
		}
	}

	// List: masked key fields only, revision mirrors updated_at.
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, list)
	}
	listData := dataMap(t, list)
	items := listData["items"].([]any)
	if len(items) != 1 || listData["total"] != float64(1) || listData["hasMore"] != false || listData["page"] != float64(1) || listData["pageSize"] != float64(50) {
		t.Fatalf("list payload: %v", list)
	}
	item := items[0].(map[string]any)
	if _, leaks := item["key"]; leaks {
		t.Fatalf("list item must not carry the plaintext key: %v", item)
	}
	if item["keyPrefix"] != plainKey[:8] || item["keySuffix"] != plainKey[len(plainKey)-8:] {
		t.Fatalf("masked key fields: %v", item)
	}
	if item["status"] != "active" || item["isDefault"] != false || item["purpose"] != "general" ||
		item["routeStrategyId"] != "rs-default" || item["revision"] == "" || item["name"] != "alpha" {
		t.Fatalf("item contract: %v", item)
	}
	usage := item["usage"].(map[string]any)
	if usage["requestCount"] != float64(0) || usage["totalTokens"] != float64(0) || usage["totalCost"] != float64(0) {
		t.Fatalf("usage zero value: %v", usage)
	}

	// Detail (owner fields included for admins) + one-shot secret reveal.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID, "")
	detailData := dataMap(t, detail)
	if code != 200 || detailData["systemAccountId"] != adminID || detailData["name"] != "alpha" {
		t.Fatalf("detail: %d %v", code, detail)
	}
	code, secret := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID+"/secret", "")
	if code != 200 || dataMap(t, secret)["key"] != plainKey {
		t.Fatalf("secret reveal: %d %v", code, secret)
	}

	// Quota limits round trip + hourly binding sync.
	code, quotaKey := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"quota-key","quotaLimits":{"hourly":{"enabled":true,"hours":3,"limit":12.5},"daily":{"enabled":true,"limit":40}}}`)
	if code != http.StatusCreated {
		t.Fatalf("quota create: %d %v", code, quotaKey)
	}
	quotaID := dataMap(t, quotaKey)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_type='api_key' AND source_id = ? AND window_hours = 3`, quotaID) != 1 {
		t.Fatal("hourly quota binding missing")
	}
	code, quotaDetail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+quotaID, "")
	quotaLimits := dataMap(t, quotaDetail)["quotaLimits"].(map[string]any)
	hourly := quotaLimits["hourly"].(map[string]any)
	if hourly["enabled"] != true || hourly["hours"] != float64(3) || hourly["limit"] != float64(12.5) {
		t.Fatalf("quota round trip: %v", quotaLimits)
	}
	if _, hasDaily := quotaLimits["daily"]; !hasDaily {
		t.Fatalf("daily quota missing: %v", quotaLimits)
	}

	// Refresh: brand new plaintext, new revision, required invalidation.
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/api-keys/"+keyID+"/refresh-key", "")
	if code != 200 {
		t.Fatalf("refresh: %d %v", code, refreshed)
	}
	refreshData := dataMap(t, refreshed)
	if refreshed["message"] != "API Key 密钥已刷新，请立即复制完整密钥" {
		t.Fatalf("refresh message: %v", refreshed["message"])
	}
	newKey := refreshData["key"].(string)
	if newKey == plainKey || refreshData["revision"] == revision {
		t.Fatalf("refresh must rotate key and revision: %v", refreshData)
	}
	if env.queryCell(t, `SELECT key_secret_encrypted FROM api_keys WHERE id = ?`, keyID) == storedSealed {
		t.Fatal("refresh must re-seal the stored secret")
	}
	var refreshedSecret secretPayload
	if err := DecryptJSON(testSecret, env.queryCell(t, `SELECT key_secret_encrypted FROM api_keys WHERE id = ?`, keyID), &refreshedSecret); err != nil {
		t.Fatal(err)
	}
	if refreshedSecret.Key != newKey {
		t.Fatalf("refreshed secret mismatch: %v", refreshedSecret.Key)
	}
	if !env.inval.has("api_key_secret_refreshed") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}

	// Delete: 204, cleanup target enqueued, invalidation fired.
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, deleted)
	}
	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusNotFound || gone["message"] != "API Key 不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	if env.count(t, `SELECT COUNT(*) FROM api_key_record_cleanup_targets WHERE api_key_id = ? AND system_account_id = ?`, keyID, adminID) != 1 {
		t.Fatal("cleanup target row missing")
	}
	if !env.inval.has("api_key_deleted") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d", code)
	}

	// Operation logs: create/refresh/delete/reveal recorded, key material masked.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	for _, want := range []string{"api_keys.create", "api_keys.refresh_key", "api_keys.delete", "api_keys.reveal_secret"} {
		if !seen[want] {
			t.Fatalf("operation log actions: %v", env.sink.actions())
		}
	}
	if !env.sink.sensitive("key") {
		t.Fatal("key changes must be recorded sensitive without key material")
	}
}

func TestAPIKeyCreateValidationAndGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// Missing default route strategy.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"lonely"}`)
	if code != http.StatusBadRequest || missing["message"] != "当前用户缺少可用的默认策略路由" {
		t.Fatalf("missing default strategy: %d %v", code, missing)
	}

	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	// Idempotent payload → dedupe guard 409 (same as Node's mutationGuard).
	code, idempotent := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"alpha"}`)
	if code != http.StatusCreated {
		t.Fatalf("first create: %d %v", code, idempotent)
	}
	code, deduped := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"alpha"}`)
	if code != http.StatusConflict {
		t.Fatalf("idempotent create: %d %v", code, deduped)
	}
	// Same owner + name under a different guard scope reaches the store and
	// surfaces the owner-scoped unique-name conflict (409).
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/api-keys?systemAccountId="+adminID, `{"name":"alpha"}`)
	if code != http.StatusConflict || duplicate["message"] != "API Key 名称已存在：alpha" {
		t.Fatalf("duplicate name: %d %v", code, duplicate)
	}

	// Body validation contract. Each attempt keeps a distinct name: the
	// mutation guard fingerprints {owner, name}, so a failed create poisons
	// that name for the failed-TTL window (same as Node).
	code, noName := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"providerCode":"x"}`)
	if code != http.StatusBadRequest || noName["message"] != "请填写 API Key 名称" {
		t.Fatalf("missing name: %d %v", code, noName)
	}
	code, unknownField := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"ok","bogus":1}`)
	if code != http.StatusBadRequest || unknownField["message"] != "API Key 参数无效" {
		t.Fatalf("unknown field: %d %v", code, unknownField)
	}
	code, badExpiry := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"ok-expiry","expiresAt":"yesterday"}`)
	if code != http.StatusBadRequest || badExpiry["message"] != "API Key 过期时间必须是有效时间字符串" {
		t.Fatalf("bad expiry: %d %v", code, badExpiry)
	}
	code, okExpiry := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"expiring","expiresAt":"2030-06-01T00:00:00+08:00"}`)
	if code != http.StatusCreated {
		t.Fatalf("expiry create: %d %v", code, okExpiry)
	}
	expiringID := dataMap(t, okExpiry)["id"].(string)
	if got := env.queryCell(t, `SELECT expires_at FROM api_keys WHERE id = ?`, expiringID); got != "2030-05-31T16:00:00.000Z" {
		t.Fatalf("expiry canonicalization: %v", got)
	}

	// Route strategy binding guards.
	code, badStrategy := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"bound","routeStrategyId":"rs-missing"}`)
	if code != http.StatusBadRequest || badStrategy["message"] != "API Key 绑定的策略路由不存在或不属于当前用户" {
		t.Fatalf("missing strategy: %d %v", code, badStrategy)
	}
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-disabled', ?, '停用策略', 'normal', 'disabled', 0, '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`, adminID)
	code, disabledStrategy := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"bound2","routeStrategyId":"rs-disabled"}`)
	if code != http.StatusBadRequest || disabledStrategy["message"] != "API Key 只能绑定启用状态的策略路由" {
		t.Fatalf("disabled strategy: %d %v", code, disabledStrategy)
	}

	// Quota normalization errors surface as 400.
	code, badQuota := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"badquota","quotaLimits":{"daily":{"enabled":true,"limit":0}}}`)
	if code != http.StatusBadRequest || badQuota["message"] != "日额度金额必须是大于 0 的数字" {
		t.Fatalf("bad quota: %d %v", code, badQuota)
	}
	code, disabledQuota := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"badquota2","quotaLimits":{"daily":{"enabled":false,"limit":5}}}`)
	if code != http.StatusBadRequest || disabledQuota["message"] != "日额度启用状态必须为 true" {
		t.Fatalf("disabled quota: %d %v", code, disabledQuota)
	}

	// List filters: keyword prefix, status, routeStrategyId.
	code, bravo := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"bravo","status":"disabled"}`)
	if code != http.StatusCreated {
		t.Fatalf("bravo create: %d %v", code, bravo)
	}
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?keyword=bra", "")
	if items := dataMap(t, filtered)["items"].([]any); code != 200 || len(items) != 1 || items[0].(map[string]any)["name"] != "bravo" {
		t.Fatalf("keyword filter: %d %v", code, filtered)
	}
	code, disabledOnly := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?status=disabled", "")
	if items := dataMap(t, disabledOnly)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("status filter: %d %v", code, disabledOnly)
	}
	code, strategyFiltered := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?routeStrategyId=rs-default", "")
	if items := dataMap(t, strategyFiltered)["items"].([]any); code != 200 || len(items) != 3 {
		t.Fatalf("strategy filter: %d %v", code, strategyFiltered)
	}
	// The requested disabled status persisted for the explicit status create.
	code, listed := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	for _, entry := range dataMap(t, listed)["items"].([]any) {
		item := entry.(map[string]any)
		if item["name"] == "bravo" && item["status"] != "disabled" {
			t.Fatalf("requested status must persist: %v", item)
		}
	}

	// Unknown detail ids 404 on both surfaces.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("admin detail 404: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("my detail 404: %d", code)
	}
}

func TestAPIKeyAvailabilityScheduleContract(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	// Always-allowed window → active, next check pinned to the window end.
	alwaysOn := `{"name":"scheduled-on","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]}}`
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", alwaysOn)
	if code != http.StatusCreated {
		t.Fatalf("schedule create: %d %v", code, created)
	}
	onID := dataMap(t, created)["id"].(string)
	if status := env.queryCell(t, `SELECT status FROM api_keys WHERE id = ?`, onID); status != "active" {
		t.Fatalf("schedule status: %v", status)
	}
	nextCheck := env.queryCell(t, `SELECT availability_schedule_next_check_at FROM api_keys WHERE id = ?`, onID)
	if nextCheck == "" {
		t.Fatal("next check timestamp missing")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, nextCheck); err != nil || time.Until(parsed) > 24*time.Hour {
		t.Fatalf("next check must fall on the window end (<=24h): %v", nextCheck)
	}

	// Date range fully in the past → disabled now, fallback check in ~7 days.
	expired := `{"name":"scheduled-off","status":"active","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],
		"dateRange":{"startDate":"2000-01-01","endDate":"2000-01-02"}}}`
	code, disabledCreate := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", expired)
	if code != http.StatusCreated {
		t.Fatalf("expired schedule create: %d %v", code, disabledCreate)
	}
	offID := dataMap(t, disabledCreate)["id"].(string)
	if status := env.queryCell(t, `SELECT status FROM api_keys WHERE id = ?`, offID); status != "disabled" {
		t.Fatalf("expired schedule must disable the key: %v", status)
	}
	fallback := env.queryCell(t, `SELECT availability_schedule_next_check_at FROM api_keys WHERE id = ?`, offID)
	if parsed, err := time.Parse(time.RFC3339Nano, fallback); err != nil || time.Until(parsed) < 6*24*time.Hour {
		t.Fatalf("fallback next check ~7 days: %v", fallback)
	}

	// Read mapper echoes the normalized schedule.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+onID, "")
	schedule := dataMap(t, detail)["availabilitySchedule"].(map[string]any)
	if schedule["enabled"] != true || schedule["mode"] != "allow_windows" || schedule["timezone"] != "UTC" {
		t.Fatalf("schedule echo: %v", schedule)
	}
	window := schedule["windows"].([]any)[0].(map[string]any)
	if window["start"] != "00:00" || window["end"] != "23:59" {
		t.Fatalf("window echo: %v", window)
	}

	// Validation: disabled schedule flag, identical window bounds.
	code, disabledFlag := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"bad-schedule","availabilitySchedule":{"enabled":false,"mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"00:00","end":"01:00"}]}}`)
	if code != http.StatusBadRequest || !strings.Contains(disabledFlag["message"].(string), "启用状态必须为 true") {
		t.Fatalf("disabled schedule flag: %d %v", code, disabledFlag)
	}
	code, sameBounds := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"bad-schedule2","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"01:00"}]}}`)
	if code != http.StatusBadRequest || sameBounds["message"] != "API Key 时间计划开始时间和停止时间不能相同" {
		t.Fatalf("same bounds: %d %v", code, sameBounds)
	}
}

func TestAPIKeyMySurfaceAndPermissionMatrix(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous requests are refused on both surfaces (no cookie jar yet).
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my list: %d %v", code, anonymous)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin list: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys/does-not-exist/secret", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous my secret: %d", code)
	}

	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedDefaultRouteStrategy(t, aliceID, "rs-alice")

	// Alice creates on the self surface; the owner is the caller.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-api-keys", `{"name":"alice-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, created)
	}
	aliceKeyID := dataMap(t, created)["id"].(string)
	alicePlain := dataMap(t, created)["key"].(string)

	// Bob cannot see or mutate alice's key through my-*.
	env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys/"+aliceKeyID+"/secret", "")
	if code != http.StatusNotFound || forbidden["message"] != "API Key 不存在" {
		t.Fatalf("bob reveal alice key: %d %v", code, forbidden)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys/"+aliceKeyID, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob detail alice key: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/my-api-keys/"+aliceKeyID+"/refresh-key", "")
	if code != http.StatusNotFound {
		t.Fatalf("bob refresh alice key: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-api-keys/"+aliceKeyID, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob delete alice key: %d", code)
	}

	// Alice still sees the matching plaintext on her own surface.
	env.login(t, "alice", "alice-pass", "user")
	code, mine := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys/"+aliceKeyID+"/secret", "")
	if code != 200 || dataMap(t, mine)["key"] != alicePlain {
		t.Fatalf("alice reveal: %d %v", code, mine)
	}
	code, myList := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys", "")
	myItems := dataMap(t, myList)["items"].([]any)
	if code != 200 || len(myItems) != 1 || myItems[0].(map[string]any)["id"] != aliceKeyID {
		t.Fatalf("alice list: %d %v", code, myList)
	}
	// Self surface never includes owner fields.
	if _, hasOwner := myItems[0].(map[string]any)["systemAccountId"]; hasOwner {
		t.Fatal("self surface must omit systemAccountId")
	}

	// user role cannot reach the admin surface (permission matrix).
	code, adminDenied := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if code != http.StatusForbidden || adminDenied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, adminDenied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"x"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin create as user: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/api-keys/"+aliceKeyID+"/refresh-key", "")
	if code != http.StatusForbidden {
		t.Fatalf("admin refresh as user: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+aliceKeyID+"/secret", "")
	if code != http.StatusForbidden {
		t.Fatalf("admin reveal as user: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/"+aliceKeyID, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete as user: %d", code)
	}
}

func TestAPIKeyAdminScopeFilterAndView(t *testing.T) {
	env := newTestEnv(t)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedDefaultRouteStrategy(t, aliceID, "rs-alice")
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-api-keys", `{"name":"alice-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, created)
	}
	aliceKeyID := dataMap(t, created)["id"].(string)
	alicePlain := dataMap(t, created)["key"].(string)

	env.login(t, "root", "root-pass", "super_admin")
	code, all := env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if items := dataMap(t, all)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("admin unscoped list: %d %v", code, all)
	}
	// Admin surface includes owner fields.
	item := dataMap(t, all)["items"].([]any)[0].(map[string]any)
	if item["systemAccountId"] != aliceID {
		t.Fatalf("owner fields: %v", item)
	}
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?systemAccountId="+aliceID, "")
	if items := dataMap(t, filtered)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("admin filtered list: %d %v", code, filtered)
	}
	code, empty := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?systemAccountId=someone-else", "")
	if items := dataMap(t, empty)["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("admin other-owner filter: %d %v", code, empty)
	}
	// Admin without a filter can reveal any owner's key (Node contract).
	code, revealed := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+aliceKeyID+"/secret", "")
	if code != 200 || dataMap(t, revealed)["key"] != alicePlain {
		t.Fatalf("admin reveal: %d %v", code, revealed)
	}
	// Admin filtered to another owner cannot.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+aliceKeyID+"/secret?systemAccountId=someone-else", "")
	if code != http.StatusNotFound {
		t.Fatalf("admin filtered reveal: %d", code)
	}
	// my-* for an admin is force-self-scoped: the admin owns nothing here.
	code, adminSelf := env.do(t, http.MethodGet, "/__aisys__/api/my-api-keys", "")
	if items := dataMap(t, adminSelf)["items"].([]any); code != 200 || len(items) != 0 {
		t.Fatalf("admin my list must be self-scoped: %d %v", code, adminSelf)
	}

	// A blank systemAccountId is ignored on the list route (Node reads the
	// filter without scope-query parsing) but rejected on the secret route.
	code, blankList := env.do(t, http.MethodGet, "/__aisys__/api/api-keys?systemAccountId=", "")
	if items := dataMap(t, blankList)["items"].([]any); code != 200 || len(items) != 1 {
		t.Fatalf("blank scope on list is ignored: %d %v", code, blankList)
	}
	code, blankSecret := env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+aliceKeyID+"/secret?systemAccountId=", "")
	if code != http.StatusBadRequest || blankSecret["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("blank scope on secret: %d %v", code, blankSecret)
	}
}

func TestAPIKeyDeleteGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := EncryptJSON(testSecret, secretPayload{Key: "sk-default"})
	if err != nil {
		t.Fatal(err)
	}

	// Default key (is_default=1) refuses deletion.
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-default', ?, 'rs-default', '默认 API Key', 'hash', 'sk-defau', 'lt', ?, 'active', 1, 'general', ?, ?)`,
		adminID, sealed, now, now)
	code, defaultGuard := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/key-default", "")
	if code != http.StatusConflict || defaultGuard["message"] != "默认 API Key 不允许删除" {
		t.Fatalf("default delete guard: %d %v", code, defaultGuard)
	}

	// Chat purpose key refuses deletion.
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES ('key-chat', ?, 'rs-default', 'AI 对话 API Key', 'hash2', 'sk-chatk', 'ey-1', ?, 'active', 0, 'chat', ?, ?)`,
		adminID, sealed, now, now)
	code, chatGuard := env.do(t, http.MethodDelete, "/__aisys__/api/api-keys/key-chat", "")
	if code != http.StatusConflict || chatGuard["message"] != "AI 对话 API Key 不允许删除" {
		t.Fatalf("chat delete guard: %d %v", code, chatGuard)
	}
	if env.count(t, `SELECT COUNT(*) FROM api_keys WHERE id IN ('key-default','key-chat')`) != 2 {
		t.Fatal("guarded keys must survive")
	}
	// Cleanup target is only written on successful deletes.
	if env.count(t, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`) != 0 {
		t.Fatal("no cleanup target may exist for guarded deletes")
	}
}

func TestCryptoAndScheduleUnits(t *testing.T) {
	// Known sha256 vector pins HashSecret to Node hashSecret.
	if got := HashSecret("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("hashSecret vector: %v", got)
	}

	envelope, err := EncryptJSON(testSecret, secretPayload{Key: "sk-roundtrip"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("envelope shape: %v", envelope)
	}
	decode := base64.RawURLEncoding.DecodeString
	iv, ivErr := decode(parts[1])
	tag, tagErr := decode(parts[2])
	if ivErr != nil || tagErr != nil || len(iv) != 12 || len(tag) != 16 {
		t.Fatalf("iv/tag layout: %v %v", ivErr, tagErr)
	}

	// Tampering with the ciphertext fails authentication.
	tampered := strings.Replace(envelope, parts[3][:4], "AAAA", 1)
	var payload secretPayload
	if err := DecryptJSON(testSecret, tampered, &payload); err == nil {
		t.Fatal("tampered envelope must fail")
	}
	if err := DecryptJSON(testSecret, "v0:x:y:z", &payload); err == nil {
		t.Fatal("unknown envelope version must fail")
	}
	if err := DecryptJSON("other-secret", envelope, &payload); err == nil {
		t.Fatal("wrong secret must fail")
	}

	// Cross-check against an independent v1 construction (Node-compatible
	// layout: sha256(secret) AES-256-GCM, 12-byte IV, tag appended last).
	nodeIv := make([]byte, 12)
	for i := range nodeIv {
		nodeIv[i] = byte(i)
	}
	material := sha256.Sum256([]byte(testSecret))
	block, err := aes.NewCipher(material[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"key":"sk-node-written"}`)
	sealed := gcm.Seal(nil, nodeIv, plain, nil)
	nodeEnvelope := "v1:" + base64.RawURLEncoding.EncodeToString(nodeIv) + ":" +
		base64.RawURLEncoding.EncodeToString(sealed[len(sealed)-16:]) + ":" +
		base64.RawURLEncoding.EncodeToString(sealed[:len(sealed)-16])
	var cross secretPayload
	if err := DecryptJSON(testSecret, nodeEnvelope, &cross); err != nil || cross.Key != "sk-node-written" {
		t.Fatalf("node envelope decrypt: %v %v", err, cross.Key)
	}

	// Revision helpers keep the microsecond RFC3339 shape and monotonicity.
	now := time.Date(2026, 9, 3, 12, 0, 0, 123_000_000, time.UTC)
	if got := revisionFromMillis(now.UnixMilli()); got != "2026-09-03T12:00:00.123000Z" {
		t.Fatalf("revision format: %v", got)
	}
	next, err := nextRevision("2026-09-03T12:00:00.123000Z", now)
	if err != nil || next != "2026-09-03T12:00:00.124000Z" {
		t.Fatalf("next revision: %v %v", next, err)
	}
	if _, err := nextRevision("not-a-time", now); err == nil {
		t.Fatal("invalid revision must fail")
	}

	// Schedule unit: cross-midnight windows cover the following morning.
	schedule := &AvailabilitySchedule{
		Enabled: true, Timezone: "UTC", Mode: "allow_windows",
		Windows: []ScheduleWindow{{DaysOfWeek: []int{1, 2, 3, 4, 5, 6, 7}, Start: "22:00", End: "06:00"}},
	}
	early := scheduleZonedParts(time.Date(2026, 9, 3, 5, 0, 0, 0, time.UTC), "UTC")
	if !scheduleAllows(schedule, early) {
		t.Fatal("cross-midnight window must cover the early morning")
	}
	noon := scheduleZonedParts(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), "UTC")
	if scheduleAllows(schedule, noon) {
		t.Fatal("cross-midnight window must exclude noon")
	}
}
