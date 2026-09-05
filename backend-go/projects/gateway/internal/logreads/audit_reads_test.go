package logreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// readsAuthTables is the minimal auth schema required by authsys (mirrors the
// groups test harness).
var readsAuthTables = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
}

// readsTestEnv is the shared route test harness: one in-memory SQLite handle
// backing the auth tables, the dataset tables seeded per family, and the
// logreads routes mounted behind the real requireAdmin chain.
type readsTestEnv struct {
	server   *httptest.Server
	db       *sql.DB
	jar      map[string]string
	accounts *authsys.AccountStore
	hotDir   string
	blobDir  string
	logDir   string
	grep     *RuntimeLogGrep
}

// newReadsTestEnv builds the env over the given dataset DDL. The optional
// mutate hook runs after the readers are constructed and before Mount (used
// to pin the runtime reader clock).
func newReadsTestEnv(t *testing.T, datasetDDL []string, mutate func(audit AuditLogReader, runtime RuntimeLogReader, public PublicApiLogReader), login bool) *readsTestEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:logreads-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range append(readsAuthTables, datasetDDL...) {
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
	directories := t.TempDir()
	hotDir := filepath.Join(directories, "audit-hot")
	blobDir := filepath.Join(directories, "audit-blobs")
	logDir := filepath.Join(directories, "logs")
	audit, err := NewAuditLogQueryReader(db, ReadSQLite, AuditQueryDirectories{
		HotSearchDirectory:   hotDir,
		PayloadBlobDirectory: blobDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeReader, err := NewRuntimeLogSQLReader(db, ReadSQLite)
	if err != nil {
		t.Fatal(err)
	}
	public, err := NewPublicApiLogSQLStore(db, ReadSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(audit, runtimeReader, public)
	}
	grep := NewRuntimeLogGrep(RuntimeLogGrepConfig{FileEnabled: true, Directory: logDir, MaxFiles: 500, RetentionDays: 30})
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&ReadsDeps{Audit: audit, Runtime: runtimeReader, Public: public, Grep: grep, Auth: deps}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	env := &readsTestEnv{server: server, db: db, jar: map[string]string{}, accounts: accounts, hotDir: hotDir, blobDir: blobDir, logDir: logDir, grep: grep}
	if login {
		if _, err := accounts.Create(context.Background(), authsys.CreateInput{
			Username: "admin", DisplayName: "admin_name", Password: "admin-password-123", Role: "admin",
			MustChangePassword: boolPtr(false),
		}); err != nil {
			t.Fatal(err)
		}
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"admin","password":"admin-password-123"}`)
		if code != http.StatusOK {
			t.Fatalf("admin login failed: %d %v", code, payload)
		}
	}
	return env
}

func boolPtr(value bool) *bool { return &value }

// resetSession drops the cookie jar so subsequent calls act anonymously.
func (e *readsTestEnv) resetSession() { e.jar = map[string]string{} }

func (e *readsTestEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
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
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Value != "" {
			e.jar[cookie.Name] = cookie.Value
		} else {
			delete(e.jar, cookie.Name)
		}
	}
	var payload map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("response is not JSON: %v (%s)", err, string(raw))
		}
	}
	return response.StatusCode, payload
}

func (e *readsTestEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

// wantData returns the {data} envelope payload.
func wantData(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data envelope: %v", payload)
	}
	return data
}

func wantFloat(t *testing.T, source map[string]any, key string) float64 {
	t.Helper()
	value, ok := source[key].(float64)
	if !ok {
		t.Fatalf("field %q is not a number: %v", key, source[key])
	}
	return value
}

func wantString(t *testing.T, source map[string]any, key string) string {
	t.Helper()
	value, ok := source[key].(string)
	if !ok {
		t.Fatalf("field %q is not a string: %v", key, source[key])
	}
	return value
}

func wantBool(t *testing.T, source map[string]any, key string) bool {
	t.Helper()
	value, ok := source[key].(bool)
	if !ok {
		t.Fatalf("field %q is not a bool: %v", key, source[key])
	}
	return value
}

// wantItems returns data.items as a slice of objects and fails on null items.
func wantItems(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing items array: %v", data)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("item is not an object: %v", entry)
		}
		items = append(items, item)
	}
	return items
}

// ---------------------------------------------------------------------------
// Audit family
// ---------------------------------------------------------------------------

var auditReadsDDL = []string{
	`CREATE TABLE IF NOT EXISTS audit_logs (
	  id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, traffic_source TEXT NOT NULL,
	  system_account_id TEXT, api_key_id TEXT, conversation_key TEXT, session_id TEXT,
	  session_client_type TEXT, group_id TEXT, account_id TEXT, provider_code TEXT,
	  method TEXT NOT NULL, path TEXT NOT NULL, query_string TEXT, model TEXT,
	  upstream_model TEXT, pricing_model TEXT, model_mapping_applied INTEGER NOT NULL DEFAULT 0,
	  model_mapping_source TEXT, source_endpoint_family TEXT, upstream_endpoint_family TEXT,
	  stream INTEGER NOT NULL DEFAULT 0, client_ip TEXT, user_agent TEXT,
	  audit_outcome TEXT NOT NULL, success INTEGER NOT NULL DEFAULT 0, final_status_code INTEGER,
	  error_phase TEXT, error_code TEXT, error_message TEXT, sample_bucket INTEGER NOT NULL,
	  sample_reason TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
	  payload_count INTEGER NOT NULL DEFAULT 0, raw_payload_bytes INTEGER NOT NULL DEFAULT 0,
	  compressed_payload_bytes INTEGER NOT NULL DEFAULT 0, compression_saved_bytes INTEGER NOT NULL DEFAULT 0,
	  error_group_id TEXT, capture_status TEXT NOT NULL DEFAULT 'complete',
	  lifecycle_status TEXT NOT NULL DEFAULT 'finalized', started_at TEXT NOT NULL,
	  ended_at TEXT NOT NULL, duration_ms INTEGER, http_completed_at TEXT, http_duration_ms INTEGER,
	  first_token_ms INTEGER, created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS audit_log_attempts (
	  id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL, attempt_index INTEGER NOT NULL,
	  account_id TEXT, account_owner_system_account_id TEXT, group_id TEXT, proxy_url TEXT,
	  provider_code TEXT, attempt_model TEXT, attempt_upstream_model TEXT, attempt_pricing_model TEXT,
	  attempt_model_mapping_applied INTEGER NOT NULL DEFAULT 0, attempt_model_mapping_source TEXT,
	  attempt_source_endpoint_family TEXT, attempt_upstream_endpoint_family TEXT,
	  upstream_method TEXT NOT NULL, upstream_url TEXT NOT NULL, upstream_status_code INTEGER,
	  success INTEGER NOT NULL DEFAULT 0, error_phase TEXT, error_code TEXT, error_message TEXT,
	  started_at TEXT NOT NULL, ended_at TEXT, duration_ms INTEGER
	)`,
	`CREATE TABLE IF NOT EXISTS audit_payload_blobs (
	  id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, raw_size_bytes INTEGER NOT NULL,
	  compressed_size_bytes INTEGER NOT NULL, content_type TEXT NOT NULL, content_encoding TEXT,
	  compression TEXT NOT NULL DEFAULT 'none', storage_key TEXT NOT NULL,
	  ref_count INTEGER NOT NULL DEFAULT 0, first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
	  created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS audit_payload_refs (
	  id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL, attempt_id TEXT, part_type TEXT NOT NULL,
	  sequence_index INTEGER NOT NULL, content_type TEXT, content_encoding TEXT,
	  headers_blob_id TEXT, body_blob_id TEXT, headers_sha256 TEXT, body_sha256 TEXT,
	  raw_size_bytes INTEGER NOT NULL DEFAULT 0, compressed_size_bytes INTEGER NOT NULL DEFAULT 0,
	  capture_status TEXT NOT NULL, drop_reason TEXT, created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS audit_error_groups (
	  id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, window_started_at TEXT NOT NULL,
	  window_ended_at TEXT NOT NULL, system_account_id TEXT, api_key_id TEXT, group_id TEXT,
	  account_id TEXT, provider_code TEXT, path TEXT, model TEXT, status_code INTEGER,
	  error_phase TEXT, error_code TEXT, error_type TEXT, request_fingerprint TEXT,
	  error_fingerprint TEXT, count INTEGER NOT NULL DEFAULT 0, first_event_id TEXT,
	  last_event_id TEXT, sample_event_id TEXT, last_message TEXT, created_at TEXT NOT NULL,
	  updated_at TEXT NOT NULL
	)`,
}

func seedAuditReads(t *testing.T, env *readsTestEnv) {
	t.Helper()
	// log-2: newest persisted gateway success row.
	env.exec(t, `INSERT INTO audit_logs (id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key,
		session_id, session_client_type, group_id, account_id, provider_code, method, path, model, upstream_model,
		model_mapping_applied, stream, client_ip, audit_outcome, success, final_status_code, sample_bucket, sample_reason,
		attempt_count, payload_count, raw_payload_bytes, compressed_payload_bytes, compression_saved_bytes,
		lifecycle_status, started_at, ended_at, duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at)
		VALUES ('log-2', 'trace-abcd', 'gateway', 'sys-1', 'key-1', 'conv-2', 'sess-1', 'web', 'grp-1', 'acc-1', 'openai',
		'POST', '/v1/chat/completions', 'gpt-4o', 'gpt-4o-2024', 1, 1, '10.0.0.1', 'success', 1, 200, 1, 'sampled',
		0, 0, 512, 256, 256, 'finalized', '2026-06-02T10:00:00.000Z', '2026-06-02T10:00:00.200Z', 200,
		'2026-06-02T10:00:00.220Z', 220, 40, '2026-06-02T10:00:01.000Z')`)
	// log-1: older gateway failure row tied to an error group, two attempts,
	// two payload refs.
	env.exec(t, `INSERT INTO audit_logs (id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id,
		provider_code, method, path, model, model_mapping_applied, stream, audit_outcome, success, final_status_code,
		error_phase, error_code, error_message, sample_bucket, sample_reason, attempt_count, payload_count,
		raw_payload_bytes, compressed_payload_bytes, error_group_id, capture_status, started_at, ended_at,
		duration_ms, created_at)
		VALUES ('log-1', 'trace-ffff', 'gateway', 'sys-1', 'key-1', 'grp-1', 'acc-1', 'openai', 'POST',
		'/v1/chat/completions', 'gpt-4o', 0, 0, 'upstream_failed', 0, 502, 'upstream', 'upstream_error',
		'upstream exploded', 2, 'error', 2, 2, 1024, 512, 'eg-1', 'complete', '2026-06-01T10:00:00.000Z',
		'2026-06-01T10:00:00.900Z', 900, '2026-06-01T10:00:02.000Z')`)
	// log-3: internal probe traffic that the persisted-traffic clause hides.
	env.exec(t, `INSERT INTO audit_logs (id, trace_id, traffic_source, method, path, audit_outcome, success,
		sample_bucket, sample_reason, started_at, ended_at, created_at)
		VALUES ('log-3', 'trace-internal', 'account_health_check', 'GET', '/v1/models', 'success', 1, 0,
		'internal', '2026-06-03T10:00:00.000Z', '2026-06-03T10:00:00.100Z', '2026-06-03T10:00:01.000Z')`)
	env.exec(t, `INSERT INTO audit_log_attempts (id, audit_log_id, attempt_index, account_id,
		account_owner_system_account_id, group_id, proxy_url, provider_code, attempt_model, attempt_upstream_model,
		attempt_pricing_model, attempt_model_mapping_applied, attempt_model_mapping_source,
		attempt_source_endpoint_family, attempt_upstream_endpoint_family, upstream_method, upstream_url,
		upstream_status_code, success, error_phase, error_code, error_message, started_at, ended_at, duration_ms)
		VALUES ('att-1', 'log-1', 0, 'acc-1', 'sys-1', 'grp-1', 'http://proxy:8080', 'openai', 'gpt-4o-mini',
		'gpt-4o-mini-2024', 'gpt-4o-mini-price', 1, 'alias', 'chat', 'chat', 'POST', 'https://upstream/v1/chat', 429,
		0, 'upstream', 'rate_limited', '429', '2026-06-01T10:00:00.000Z', '2026-06-01T10:00:00.300Z', 300)`)
	env.exec(t, `INSERT INTO audit_log_attempts (id, audit_log_id, attempt_index, account_id, group_id, provider_code,
		attempt_model, upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code,
		started_at, ended_at, duration_ms)
		VALUES ('att-2', 'log-1', 1, 'acc-1', 'grp-1', 'openai', 'gpt-4o-mini', 'POST',
		'https://upstream/v1/chat', 502, 0, 'upstream', 'bad_gateway', '2026-06-01T10:00:00.400Z',
		'2026-06-01T10:00:00.900Z', 500)`)
	env.exec(t, `INSERT INTO audit_payload_refs (id, audit_log_id, attempt_id, part_type, sequence_index, content_type,
		content_encoding, headers_blob_id, body_blob_id, headers_sha256, body_sha256, raw_size_bytes,
		compressed_size_bytes, capture_status, drop_reason, created_at)
		VALUES ('pay-1', 'log-1', 'att-1', 'client_request', 0, 'application/json', 'identity', 'blob-h1', 'blob-b1',
		'hash-h', 'hash-b', 100, 50, 'complete', NULL, '2026-06-01T10:00:01.000Z')`)
	env.exec(t, `INSERT INTO audit_payload_refs (id, audit_log_id, part_type, sequence_index, content_type,
		raw_size_bytes, compressed_size_bytes, capture_status, created_at)
		VALUES ('pay-2', 'log-1', 'upstream_response', 1, 'application/json', 900, 450, 'summary_only',
		'2026-06-01T10:00:01.500Z')`)
	env.exec(t, `INSERT INTO audit_error_groups (id, fingerprint, window_started_at, window_ended_at,
		system_account_id, api_key_id, group_id, account_id, provider_code, path, model, status_code, error_phase,
		error_code, error_type, request_fingerprint, error_fingerprint, count, first_event_id, last_event_id,
		sample_event_id, last_message, created_at, updated_at)
		VALUES ('eg-1', 'fp-1', '2026-06-01T09:00:00.000Z', '2026-06-01T10:00:00.000Z', 'sys-1', 'key-1', 'grp-1',
		'acc-1', 'openai', '/v1/chat/completions', 'gpt-4o', 502, 'upstream', 'upstream_error', 'upstream',
		'req-fp', 'err-fp', 3, 'log-1', 'log-1', 'log-1', 'upstream exploded', '2026-06-01T09:00:01.000Z',
		'2026-06-01T10:00:02.000Z')`)
}

func TestAuditLogReadsListContracts(t *testing.T) {
	env := newReadsTestEnv(t, auditReadsDDL, nil, true)
	seedAuditReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs", "")
	if code != http.StatusOK {
		t.Fatalf("list status: %d %v", code, payload)
	}
	data := wantData(t, payload)
	items := wantItems(t, data)
	if len(items) != 2 {
		t.Fatalf("persisted traffic filter should hide log-3, items: %v", items)
	}
	if id := wantString(t, items[0], "id"); id != "log-2" {
		t.Fatalf("expected newest first (log-2), got %s", id)
	}
	first := items[0]
	if got := wantString(t, first, "traceId"); got != "trace-abcd" {
		t.Fatalf("traceId: %q", got)
	}
	if got := wantString(t, first, "trafficSource"); got != "gateway" {
		t.Fatalf("trafficSource: %q", got)
	}
	if !wantBool(t, first, "modelMappingApplied") || !wantBool(t, first, "stream") || !wantBool(t, first, "success") {
		t.Fatalf("bool fields must be real JSON booleans: %v", first)
	}
	if got := wantFloat(t, first, "finalStatusCode"); got != 200 {
		t.Fatalf("finalStatusCode: %v", got)
	}
	if got := wantString(t, first, "lifecycleStatus"); got != "finalized" {
		t.Fatalf("lifecycleStatus: %q", got)
	}
	if _, exists := first["systemAccountName"]; exists {
		t.Fatalf("unresolved *Name joins must be omitted: %v", first)
	}
	if wantFloat(t, data, "total") != 2 || wantFloat(t, data, "page") != 1 || wantFloat(t, data, "pageSize") != 100 {
		t.Fatalf("pagination envelope: %v", data)
	}
	if wantBool(t, data, "hasMore") {
		t.Fatalf("hasMore should be false: %v", data)
	}

	// Filters: outcome, statusCode, model, traceId prefix.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?outcome=upstream_failed", "")
	if code != http.StatusOK {
		t.Fatalf("outcome filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "log-1" {
		t.Fatalf("outcome filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?statusCode=502", "")
	if code != http.StatusOK {
		t.Fatalf("statusCode filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "log-1" {
		t.Fatalf("statusCode filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?traceId=trace-a", "")
	if code != http.StatusOK {
		t.Fatalf("traceId filter status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "log-2" {
		t.Fatalf("traceId prefix filter items: %v", items)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?outcome=bogus-outcome", "")
	if code != http.StatusOK {
		t.Fatalf("invalid outcome is ignored, status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 2 {
		t.Fatalf("invalid outcome should behave like no filter: %v", items)
	}

	// Strict time range: invalid bound is a 400 with the Node message; a
	// reversed pair is swapped.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?startAt=not-a-time", "")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid startAt status: %d %v", code, payload)
	}
	if message := wantString(t, payload, "message"); !strings.Contains(message, "开始时间") {
		t.Fatalf("invalid startAt message: %q", message)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?startAt=2026-06-02T00:00:00Z&endAt=2026-06-01T00:00:00Z", "")
	if code != http.StatusOK {
		t.Fatalf("reversed range status: %d", code)
	}
	// After the swap the window is [06-01, 06-02], which selects log-1 only;
	// without the swap it would have returned log-2.
	if items = wantItems(t, wantData(t, payload)); len(items) != 1 || wantString(t, items[0], "id") != "log-1" {
		t.Fatalf("reversed range should be swapped (log-1 expected): %v", items)
	}

	// trafficSource validation.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?trafficSource=account_health_check", "")
	if code != http.StatusBadRequest {
		t.Fatalf("non-persisted trafficSource status: %d", code)
	}
	if message := wantString(t, payload, "message"); message != "审计日志来源筛选无效，仅支持网关请求、AI 账户测试、混合路由选型或回答质量复核" {
		t.Fatalf("trafficSource message: %q", message)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?trafficSource=manual_account_test", "")
	if code != http.StatusOK {
		t.Fatalf("valid trafficSource status: %d", code)
	}
	if items = wantItems(t, wantData(t, payload)); len(items) != 0 {
		t.Fatalf("manual_account_test filter should be empty here: %v", items)
	}

	// Page window clamp: default page size caps the page at 10.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?page=99999", "")
	if code != http.StatusOK {
		t.Fatalf("clamped page status: %d", code)
	}
	data = wantData(t, payload)
	if wantFloat(t, data, "page") != 10 {
		t.Fatalf("page should clamp to the 1001-row window: %v", data)
	}

	// Session filter unlocks deep pagination.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs?sessionId=sess-1&page=500", "")
	if code != http.StatusOK {
		t.Fatalf("session page status: %d", code)
	}
	if data = wantData(t, payload); wantFloat(t, data, "page") != 500 {
		t.Fatalf("sessionId filter should allow deep pages: %v", data)
	}
}

func TestAuditLogReadsRuntimeErrorGroupsAndDetail(t *testing.T) {
	env := newReadsTestEnv(t, auditReadsDDL, nil, true)
	seedAuditReads(t, env)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/runtime", "")
	if code != http.StatusOK {
		t.Fatalf("runtime status: %d", code)
	}
	runtime := wantData(t, payload)
	if wantString(t, runtime, "mode") != "sqlite" || !wantBool(t, runtime, "readOnly") ||
		!wantBool(t, runtime, "queryOnly") || !wantBool(t, runtime, "schemaReady") || !wantBool(t, runtime, "available") {
		t.Fatalf("runtime payload: %v", runtime)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/error-groups", "")
	if code != http.StatusOK {
		t.Fatalf("error-groups status: %d", code)
	}
	groups := wantItems(t, wantData(t, payload))
	if len(groups) != 1 {
		t.Fatalf("error groups: %v", groups)
	}
	group := groups[0]
	if wantString(t, group, "id") != "eg-1" || wantFloat(t, group, "count") != 3 ||
		wantFloat(t, group, "statusCode") != 502 || wantString(t, group, "errorFingerprint") != "err-fp" {
		t.Fatalf("error group mapping: %v", group)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/error-groups/eg-1/events", "")
	if code != http.StatusOK {
		t.Fatalf("error group events status: %d", code)
	}
	events := wantItems(t, wantData(t, payload))
	if len(events) != 1 || wantString(t, events[0], "id") != "log-1" {
		t.Fatalf("error group events: %v", events)
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/log-1", "")
	if code != http.StatusOK {
		t.Fatalf("detail status: %d %v", code, payload)
	}
	detail := wantData(t, payload)
	if wantString(t, detail, "id") != "log-1" || wantString(t, detail, "errorMessage") != "upstream exploded" {
		t.Fatalf("detail mapping: %v", detail)
	}
	if wantFloat(t, detail, "sampleBucket") != 2 || wantString(t, detail, "sampleReason") != "error" {
		t.Fatalf("sample mapping: %v", detail)
	}
	if wantFloat(t, detail, "attemptCount") != 2 || wantFloat(t, detail, "payloadCount") != 2 {
		t.Fatalf("counts mapping: %v", detail)
	}
	// compression_saved_bytes is stored as 0 on this row; the Node mapper only
	// falls back to raw-compressed when the column is NULL, so 0 stays 0.
	if wantFloat(t, detail, "rawPayloadBytes") != 1024 || wantFloat(t, detail, "compressedPayloadBytes") != 512 ||
		wantFloat(t, detail, "compressionSavedBytes") != 0 {
		t.Fatalf("payload size mapping: %v", detail)
	}
	attempts, ok := detail["attempts"].([]any)
	if !ok || len(attempts) != 2 {
		t.Fatalf("attempts: %v", detail["attempts"])
	}
	firstAttempt := attempts[0].(map[string]any)
	if wantString(t, firstAttempt, "id") != "att-1" || wantFloat(t, firstAttempt, "attemptIndex") != 0 ||
		wantString(t, firstAttempt, "model") != "gpt-4o-mini" ||
		wantString(t, firstAttempt, "upstreamUrl") != "https://upstream/v1/chat" ||
		wantFloat(t, firstAttempt, "upstreamStatusCode") != 429 || !wantBool(t, firstAttempt, "modelMappingApplied") {
		t.Fatalf("attempt mapping: %v", firstAttempt)
	}
	secondAttempt := attempts[1].(map[string]any)
	if wantString(t, secondAttempt, "id") != "att-2" || wantString(t, secondAttempt, "endedAt") != "2026-06-01T10:00:00.900Z" {
		t.Fatalf("attempt ordering/endedAt: %v", secondAttempt)
	}
	payloads, ok := detail["payloads"].([]any)
	if !ok || len(payloads) != 2 {
		t.Fatalf("payloads: %v", detail["payloads"])
	}
	firstPayload := payloads[0].(map[string]any)
	if wantString(t, firstPayload, "id") != "pay-1" || wantString(t, firstPayload, "partType") != "client_request" ||
		!wantBool(t, firstPayload, "hasHeaders") || !wantBool(t, firstPayload, "hasBody") ||
		wantFloat(t, firstPayload, "sizeBytes") != 100 || wantFloat(t, firstPayload, "compressedSizeBytes") != 50 {
		t.Fatalf("payload mapping: %v", firstPayload)
	}
	secondPayload := payloads[1].(map[string]any)
	if wantString(t, secondPayload, "captureStatus") != "summary_only" || wantBool(t, secondPayload, "hasBody") {
		t.Fatalf("second payload mapping: %v", secondPayload)
	}
	errorGroup, ok := detail["errorGroup"].(map[string]any)
	if !ok || wantString(t, errorGroup, "id") != "eg-1" {
		t.Fatalf("detail errorGroup: %v", detail["errorGroup"])
	}

	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/audit-logs/missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing detail status: %d", code)
	}
	if message := wantString(t, payload, "message"); message != "审计日志不存在" {
		t.Fatalf("missing detail message: %q", message)
	}
}

func TestAuditLogReadsRequireAdmin(t *testing.T) {
	env := newReadsTestEnv(t, auditReadsDDL, nil, false)
	for _, path := range []string{
		"/__aisys__/api/audit-logs",
		"/__aisys__/api/audit-logs/runtime",
		"/__aisys__/api/audit-logs/error-groups",
		"/__aisys__/api/audit-logs/log-1",
		"/__aisys__/api/runtime-logs",
		"/__aisys__/api/runtime-logs/facets",
		"/__aisys__/api/runtime-logs/some-id",
		"/__aisys__/api/public-api-logs",
		"/__aisys__/api/public-api-logs/some-id",
	} {
		code, payload := env.do(t, http.MethodGet, path, "")
		if code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s should be 401, got %d %v", path, code, payload)
		}
	}
}
