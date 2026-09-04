package ipstats

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

func (s *recordingSink) list() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry(nil), s.entries...)
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
	store  *Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:ipstats-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`,
		`CREATE TABLE IF NOT EXISTS client_ip_registry (ip_hash TEXT PRIMARY KEY, bucket_no INTEGER NOT NULL, aggregate_ip_key TEXT NOT NULL, client_ip TEXT NOT NULL, ip_version INTEGER NOT NULL, first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS client_ip_usage_range_windows (ip_hash TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL, request_count INTEGER NOT NULL DEFAULT 0, success_count INTEGER NOT NULL DEFAULT 0, error_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_read_cost_usd REAL NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0, cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0, cache_write_cost_usd REAL NOT NULL DEFAULT 0, thinking_tokens INTEGER NOT NULL DEFAULT 0, input_image_tokens INTEGER NOT NULL DEFAULT 0, output_image_tokens INTEGER NOT NULL DEFAULT 0, total_cost_usd REAL NOT NULL DEFAULT 0, duration_ms_sum INTEGER NOT NULL DEFAULT 0, duration_ms_count INTEGER NOT NULL DEFAULT 0, duration_ms_max INTEGER NOT NULL DEFAULT 0, average_duration_ms REAL, first_token_ms_sum INTEGER NOT NULL DEFAULT 0, first_token_ms_count INTEGER NOT NULL DEFAULT 0, average_first_token_ms REAL, active_days INTEGER NOT NULL DEFAULT 0, last_used_at TEXT, last_error_at TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (ip_hash, start_date, end_date))`,
		`CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips (ip_hash TEXT PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 1, first_dirty_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS stats_job_state (scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '', job_name TEXT NOT NULL, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, last_error_message TEXT, lag_seconds INTEGER, updated_at TEXT NOT NULL, PRIMARY KEY (scope_type, scope_id, job_name))`,
		`CREATE TABLE IF NOT EXISTS client_ip_policies (id TEXT PRIMARY KEY, ip_hash TEXT NOT NULL, policy_type TEXT NOT NULL, status TEXT NOT NULL, reason TEXT, expires_at TEXT, created_by_system_account_id TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, disabled_at TEXT, disabled_by_system_account_id TEXT, disabled_reason TEXT)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique ON client_ip_policies(ip_hash) WHERE status = 'active'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`); err != nil {
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
	store, err := NewStore(db, false, nil, nil, invalidator, nil)
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
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: boolPtr(false),
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

func boolPtr(v bool) *bool { return &v }

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func (e *testEnv) insertRegistry(t *testing.T, ipHash, ip string, lastSeen time.Time) {
	t.Helper()
	now := lastSeen.Format("2006-01-02T15:04:05.000Z07:00")
	e.exec(t, `INSERT INTO client_ip_registry (ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at)
		VALUES (?, 0, ?, ?, 4, ?, ?, ?, ?)`, ipHash, ip, ip, now, now, now, now)
}

func (e *testEnv) insertWindow(t *testing.T, ipHash string, startDate, endDate string, requestCount int) {
	t.Helper()
	e.exec(t, `INSERT INTO client_ip_usage_range_windows (ip_hash, start_date, end_date, request_count, success_count, error_count,
		input_tokens, output_tokens, total_cost_usd, active_days, last_used_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 100, 200, 0.5, 1, ?, ?)`,
		ipHash, startDate, endDate, requestCount, requestCount, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
}

func (e *testEnv) markWindowReady(t *testing.T, startDate, endDate string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO stats_job_state (scope_type, scope_id, job_name, last_success_at, updated_at)
		VALUES ('client_ip_range_window', ?, 'client_ip_range_window_refresh', ?, ?)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET last_success_at = excluded.last_success_at, updated_at = excluded.updated_at`,
		startDate+":"+endDate, now, now)
}

func todayKey() string { return time.Now().UTC().Format("2006-01-02") }

const (
	listPath   = "/__aisys__/api/ip-stats"
	policyPath = "/__aisys__/api/ip-stats/%s/%s"
	testHashA  = "aaaa" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB  = "bbbb" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHashC  = "cccc" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testHashD  = "dddd" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestIPStatsListAuthAndEmpty(t *testing.T) {
	env := newTestEnv(t)

	code, payload := env.do(t, http.MethodGet, listPath, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %v", code, payload)
	}
	env.login(t, "user", "user-pass", "user")
	code, payload = env.do(t, http.MethodGet, listPath, "")
	if code != http.StatusForbidden {
		t.Fatalf("user list: %d %v", code, payload)
	}
	env.login(t, "root", "root-pass", "super_admin")
	code, payload = env.do(t, http.MethodGet, listPath, "")
	if code != http.StatusOK {
		t.Fatalf("admin list: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	items, ok := data["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("expected empty items, got %v", data["items"])
	}
	if data["page"] != float64(1) || data["pageSize"] != float64(20) ||
		data["hasMore"] != false || data["pageUpperBound"] != float64(0) || data["rangeReady"] != false {
		t.Fatalf("unexpected pagination payload: %v", data)
	}
	searchRange := data["range"].(map[string]any)
	today := todayKey()
	if searchRange["startDate"] != today || searchRange["endDate"] != today ||
		searchRange["days"] != float64(1) || searchRange["maxDays"] != float64(31) {
		t.Fatalf("unexpected range: %v", searchRange)
	}
}

func TestIPStatsListRowsFiltersSortPagination(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	today := todayKey()
	now := time.Now().UTC()
	env.insertRegistry(t, testHashA, "1.2.3.4", now)
	env.insertRegistry(t, testHashB, "5.6.7.8", now.Add(-48*time.Hour))
	env.insertRegistry(t, testHashC, "9.9.9.9", now.Add(-1*time.Hour))
	env.insertWindow(t, testHashA, today, today, 10)
	env.insertWindow(t, testHashB, today, today, 5)
	env.markWindowReady(t, today, today)

	// Default sort requestCount desc; hashC has no window row -> zeroed usage.
	code, payload := env.do(t, http.MethodGet, listPath, "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["rangeReady"] != true {
		t.Fatalf("expected rangeReady, got %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %v", items)
	}
	first := items[0].(map[string]any)
	if first["ipHash"] != testHashA || first["status"] != "normal" {
		t.Fatalf("unexpected first row: %v", first)
	}
	usage := first["rangeUsage"].(map[string]any)
	if usage["requestCount"] != float64(10) || usage["errorRate"] != float64(0) ||
		usage["totalTokens"] != float64(300) || usage["totalCost"] != float64(0.5) {
		t.Fatalf("unexpected usage: %v", usage)
	}
	third := items[2].(map[string]any)
	thirdUsage := third["rangeUsage"].(map[string]any)
	if thirdUsage["requestCount"] != float64(0) || thirdUsage["lastUsedAt"] != nil {
		t.Fatalf("expected zeroed usage for window-less row: %v", third)
	}
	if third["lastSeenAt"] == nil {
		t.Fatalf("expected registry lastSeenAt on row: %v", third)
	}

	// Policy labels: blacklist wins over allowlist.
	env.exec(t, `INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, created_by_system_account_id, created_at, updated_at)
		VALUES ('p_b', ?, 'blacklist', 'active', 'admin', ?, ?)`, testHashB, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	env.exec(t, `INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, created_by_system_account_id, created_at, updated_at)
		VALUES ('p_a', ?, 'allowlist', 'active', 'admin', ?, ?)`, testHashA, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	code, payload = env.do(t, http.MethodGet, listPath+"?status=blacklisted", "")
	items = dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["ipHash"] != testHashB {
		t.Fatalf("blacklisted filter: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, listPath+"?status=allowlisted", "")
	items = dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["ipHash"] != testHashA {
		t.Fatalf("allowlisted filter: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, listPath+"?status=normal", "")
	items = dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["ipHash"] != testHashC {
		t.Fatalf("normal filter: %d %v", code, payload)
	}

	// Keyword prefix on client_ip.
	code, payload = env.do(t, http.MethodGet, listPath+"?keyword=5.6", "")
	items = dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["ipHash"] != testHashB {
		t.Fatalf("keyword filter: %d %v", code, payload)
	}

	// Ascending request sort.
	code, payload = env.do(t, http.MethodGet, listPath+"?sortField=requestCount&sortOrder=asc", "")
	items = dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || items[0].(map[string]any)["ipHash"] != testHashC {
		t.Fatalf("requestCount asc: %d %v", code, payload)
	}

	// lastUsedAt global sort uses registry.last_seen_at.
	code, payload = env.do(t, http.MethodGet, listPath+"?sortField=lastUsedAt&sortOrder=desc", "")
	items = dataMap(t, payload)["items"].([]any)
	got := []string{items[0].(map[string]any)["ipHash"].(string), items[1].(map[string]any)["ipHash"].(string), items[2].(map[string]any)["ipHash"].(string)}
	if got[0] != testHashA || got[1] != testHashC || got[2] != testHashB {
		t.Fatalf("lastUsedAt desc order: %v", got)
	}

	// Progressive pagination window.
	code, payload = env.do(t, http.MethodGet, listPath+"?pageSize=2&page=1", "")
	data = dataMap(t, payload)
	items = data["items"].([]any)
	if len(items) != 2 || data["hasMore"] != true || data["pageUpperBound"] != float64(3) {
		t.Fatalf("page 1: %v", data)
	}
	code, payload = env.do(t, http.MethodGet, listPath+"?pageSize=2&page=2", "")
	data = dataMap(t, payload)
	items = data["items"].([]any)
	if len(items) != 1 || data["hasMore"] != false || data["pageUpperBound"] != float64(3) {
		t.Fatalf("page 2: %v", data)
	}

	// Invalid query contracts.
	for _, query := range []string{
		"?page=0", "?page=abc", "?pageSize=0", "?pageSize=101", "?pageSize=",
		"?status=bogus", "?sortField=bogus", "?sortOrder=sideways", "?page=1&page=2",
	} {
		code, payload = env.do(t, http.MethodGet, listPath+query, "")
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s: %d %v", query, code, payload)
		}
		if payload["message"] != "IP 统计参数无效" {
			t.Fatalf("expected contract message for %s: %v", query, payload)
		}
	}
}

func TestIPStatsPolicyLifecycle(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.insertRegistry(t, testHashA, "1.2.3.4", time.Now().UTC())

	// blacklist with a day duration.
	code, payload := env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "blacklist"),
		`{"reason":" 滥用 ","durationDays":1}`)
	if code != http.StatusOK {
		t.Fatalf("blacklist: %d %v", code, payload)
	}
	created := dataMap(t, payload)
	if created["policyType"] != "blacklist" || created["status"] != "active" || created["reason"] != "滥用" {
		t.Fatalf("blacklist payload: %v", created)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, created["expiresAt"].(string))
	if err != nil || time.Until(expiresAt) < 23*time.Hour || time.Until(expiresAt) > 25*time.Hour {
		t.Fatalf("unexpected expiresAt: %v", created["expiresAt"])
	}
	if created["createdBySystemAccountId"] != adminID {
		t.Fatalf("unexpected actor: %v", created)
	}
	if env.count(t, `SELECT COUNT(*) FROM client_ip_policies WHERE ip_hash = ? AND status = 'active'`, testHashA) != 1 {
		t.Fatal("expected one active policy")
	}

	// allowlist replaces the active blacklist without a manual unblock
	// (Node replacement semantics inside one transaction).
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "allowlist"), `{"reason":"trust"}`)
	if code != http.StatusOK {
		t.Fatalf("allowlist: %d %v", code, payload)
	}
	created = dataMap(t, payload)
	if created["policyType"] != "allowlist" || created["status"] != "active" {
		t.Fatalf("allowlist payload: %v", created)
	}
	if env.count(t, `SELECT COUNT(*) FROM client_ip_policies WHERE ip_hash = ? AND status = 'active' AND policy_type = 'allowlist'`, testHashA) != 1 {
		t.Fatal("expected one active allowlist")
	}
	if env.count(t, `SELECT COUNT(*) FROM client_ip_policies WHERE policy_type = 'blacklist' AND disabled_reason = '被新的白名单策略替换'`) != 1 {
		t.Fatal("expected the blacklist to be disabled by the allowlist replacement")
	}

	// unallowlist -> 1, then 0 (second run finds no active policy; the
	// fingerprint changes with the reason so the mutation guard lets it
	// through, exactly like the W6 real smoke's per-attempt reasons).
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "unallowlist"), `{}`)
	if code != http.StatusOK || dataMap(t, payload)["disabledCount"] != float64(1) {
		t.Fatalf("unallowlist: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "unallowlist"), `{"reason":"retry"}`)
	if code != http.StatusOK || dataMap(t, payload)["disabledCount"] != float64(0) {
		t.Fatalf("second unallowlist: %d %v", code, payload)
	}

	// unblock with no active blacklist also succeeds with 0.
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "unblock"), `{"reason":"calm"}`)
	if code != http.StatusOK || dataMap(t, payload)["disabledCount"] != float64(0) {
		t.Fatalf("unblock: %d %v", code, payload)
	}

	// blacklist on an unknown registry hash -> 400 IP 不存在.
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashD, "blacklist"), `{}`)
	if code != http.StatusBadRequest || payload["message"] != "IP 不存在" {
		t.Fatalf("unknown hash blacklist: %d %v", code, payload)
	}

	// Body contracts.
	for _, body := range []string{
		`{"durationMinutes":1,"durationDays":2}`,
		`{"durationMinutes":0}`,
		`{"durationMinutes":525601}`,
		`{"durationDays":0}`,
		`{"durationDays":3651}`,
		`{"durationMinutes":"30"}`,
		`{"reason":null}`,
		`{"unknownField":1}`,
		`{"reason":"` + strings.Repeat("长", 501) + `"}`,
	} {
		code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "blacklist"), body)
		if code != http.StatusBadRequest {
			t.Fatalf("expected 400 for body %s: %d %v", body, code, payload)
		}
	}
	// reason at the 500 UTF-16 code unit boundary is accepted.
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "allowlist"),
		`{"reason":"`+strings.Repeat("长", 500)+`"}`)
	if code != http.StatusOK {
		t.Fatalf("500-unit reason: %d %v", code, payload)
	}

	// Invalid path hashes.
	for _, hash := range []string{"short", strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, hash, "blacklist"), `{}`)
		if code != http.StatusBadRequest || payload["message"] != "IP 标识无效" {
			t.Fatalf("invalid hash %s: %d %v", hash, code, payload)
		}
	}

	// Writes stay admin-only.
	env.login(t, "user", "user-pass", "user")
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "blacklist"), `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("user blacklist: %d %v", code, payload)
	}
}

func TestIPStatsExpiredPolicyAndDefaultSortContract(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	today := todayKey()
	now := time.Now().UTC()
	env.insertRegistry(t, testHashA, "1.2.3.4", now)
	env.markWindowReady(t, today, today)
	past := now.Add(-time.Hour).Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, expires_at, created_by_system_account_id, created_at, updated_at)
		VALUES ('p_expired', ?, 'blacklist', 'active', ?, 'admin', ?, ?)`, testHashA, past, past, past)

	// An expired-but-active policy does not label the row blacklisted.
	code, payload := env.do(t, http.MethodGet, listPath+"?status=blacklisted", "")
	items := dataMap(t, payload)["items"].([]any)
	if code != http.StatusOK || len(items) != 0 {
		t.Fatalf("expired blacklist should not match the active filter: %d %v", code, payload)
	}

	// unblock still disables the expired active row (Node single UPDATE).
	code, payload = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "unblock"), `{}`)
	if code != http.StatusOK || dataMap(t, payload)["disabledCount"] != float64(1) {
		t.Fatalf("unblock expired: %d %v", code, payload)
	}
}

func TestIPStatsOperationLogAndInvalidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.insertRegistry(t, testHashA, "1.2.3.4", time.Now().UTC())

	code, _ := env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "blacklist"), `{"reason":"abuse","durationMinutes":30}`)
	if code != http.StatusOK {
		t.Fatalf("blacklist: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, fmt.Sprintf(policyPath, testHashA, "unblock"), `{"reason":"done"}`)
	if code != http.StatusOK {
		t.Fatalf("unblock: %d", code)
	}

	entries := env.sink.list()
	if len(entries) != 2 {
		t.Fatalf("expected 2 operation log entries, got %d", len(entries))
	}
	created, disabled := entries[0], entries[1]
	if created.Module != "client_ip_stats" || created.Action != "blacklist" ||
		created.OperationKey != "client_ip_stats.blacklist" || created.ResourceType != "client_ip" ||
		created.ResourceID != testHashA || created.ResourceName != testHashA[:12] ||
		created.Mode != "admin" || created.Summary != "封禁 IP："+testHashA[:12] {
		t.Fatalf("unexpected create log: %+v", created)
	}
	changeFields := map[string]bool{}
	for _, change := range created.Changes {
		changeFields[change.Field] = true
	}
	if !changeFields["reason"] || !changeFields["policyType"] || !changeFields["duration"] || !changeFields["expiresAt"] {
		t.Fatalf("create log changes missing: %+v", created.Changes)
	}
	if disabled.Action != "unblock" || disabled.OperationKey != "client_ip_stats.unblock" ||
		disabled.Summary != "解除 IP 封禁："+testHashA[:12] {
		t.Fatalf("unexpected disable log: %+v", disabled)
	}
	disableFields := map[string]string{}
	for _, change := range disabled.Changes {
		disableFields[change.Field] = change.After + "|" + change.Before
	}
	if disableFields["disabledCount"] != "1|" {
		t.Fatalf("expected disabledCount=1 change: %+v", disabled.Changes)
	}
	if !env.inval.has("client_ip_policy_created") || !env.inval.has("client_ip_policies_disabled") {
		t.Fatalf("expected policy cache invalidations: %v", env.inval)
	}
}

func TestIPStatsMutationGuardDuplicates(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.insertRegistry(t, testHashC, "9.9.9.9", time.Now().UTC())
	path := fmt.Sprintf(policyPath, testHashC, "blacklist")
	code, _ := env.do(t, http.MethodPost, path, `{"reason":"guard"}`)
	if code != http.StatusOK {
		t.Fatalf("first blacklist: %d", code)
	}
	code, payload := env.do(t, http.MethodPost, path, `{"reason":"guard"}`)
	if code != http.StatusConflict {
		t.Fatalf("duplicate blacklist should hit the mutation guard: %d %v", code, payload)
	}
}
