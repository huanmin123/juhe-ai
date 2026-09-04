package ipstats

import (
	"net/http"
	"strings"
	"testing"
)

// newDetailEnv builds the detail fixture: the account usage window table plus
// the business tables the DetailAccountLookup reads (Node sync branch reads
// the business handle for accounts/system_accounts).
func newDetailEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS client_ip_account_usage_range_windows (
			ip_hash TEXT NOT NULL,
			account_id TEXT NOT NULL,
			start_date TEXT NOT NULL,
			end_date TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_cost_usd REAL NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_cost_usd REAL NOT NULL DEFAULT 0,
			thinking_tokens INTEGER NOT NULL DEFAULT 0,
			input_image_tokens INTEGER NOT NULL DEFAULT 0,
			output_image_tokens INTEGER NOT NULL DEFAULT 0,
			total_cost_usd REAL NOT NULL DEFAULT 0,
			duration_ms_sum INTEGER NOT NULL DEFAULT 0,
			duration_ms_count INTEGER NOT NULL DEFAULT 0,
			duration_ms_max INTEGER NOT NULL DEFAULT 0,
			average_duration_ms REAL,
			first_token_ms_sum INTEGER NOT NULL DEFAULT 0,
			first_token_ms_count INTEGER NOT NULL DEFAULT 0,
			average_first_token_ms REAL,
			active_days INTEGER NOT NULL DEFAULT 0,
			last_used_at TEXT,
			last_error_at TEXT,
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (ip_hash, account_id, start_date, end_date)
		)`,
		`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL DEFAULT 'gpt', account_expires_at TEXT)`,
	} {
		if _, err := env.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// The admin login already created a system_accounts row; the detail
	// lookup reads display_name from the same table.
	env.store.SetDetailAccountLookup(NewBusinessAccountLookup(env.db, false))
	return env
}

const detailIPHash = "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3"

func seedDetailRows(t *testing.T, env *testEnv, adminID string) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	if _, err := env.db.Exec(`INSERT INTO client_ip_registry (ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at)
		VALUES (?, 1, ?, '203.0.113.7', 4, ?, ?, ?, ?)`, detailIPHash, strings.ToUpper(detailIPHash), now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO accounts (id, system_account_id, name) VALUES ('acc_alpha', ?, 'Alpha 账户')`, adminID); err != nil {
		t.Fatal(err)
	}
	// Materialize the range window row so the readiness probe passes (the
	// stats_job_state cursor is absent; the fallback checks the window table).
	if _, err := env.db.Exec(`INSERT INTO client_ip_usage_range_windows (ip_hash, start_date, end_date, updated_at)
		VALUES (?, '2026-09-01', '2026-09-03', ?)`, detailIPHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO client_ip_account_usage_range_windows
		(ip_hash, account_id, start_date, end_date, request_count, success_count, error_count,
		 input_tokens, output_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
		 first_token_ms_sum, first_token_ms_count, active_days, last_used_at, updated_at)
		VALUES (?, 'acc_alpha', '2026-09-01', '2026-09-03', 10, 8, 2, 100, 200, 1.25, 300, 3, 200, 600, 3, 2, ?, ?)`,
		detailIPHash, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`INSERT INTO client_ip_account_usage_range_windows
		(ip_hash, account_id, start_date, end_date, request_count, updated_at)
		VALUES (?, 'acc_beta', '2026-09-01', '2026-09-03', 4, ?)`, detailIPHash, now); err != nil {
		t.Fatal(err)
	}
}

// TestIPStatsDetailEndpointLocksIn mirrors getClientIpStatsDetailAsync: the
// registry header, window pagination with the inverted account_id tiebreak,
// the zero-filled usage summary projection and the 404 on a missing registry
// row.
func TestIPStatsDetailEndpointLocksIn(t *testing.T) {
	env := newDetailEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, adminID)

	status, payload := env.do(t, http.MethodGet, "/__aisys__/api/ip-stats/"+detailIPHash+"/detail?startDate=2026-09-01&endDate=2026-09-03&sortField=requestCount", "")
	if status != http.StatusOK {
		t.Fatalf("detail failed: %d %v", status, payload)
	}
	data := payload["data"].(map[string]any)
	if data["ipHash"] != detailIPHash {
		t.Fatalf("ipHash not normalized: %v", data["ipHash"])
	}
	if data["aggregateIpKey"] != strings.ToUpper(detailIPHash) {
		t.Fatalf("aggregateIpKey mismatch: %v", data["aggregateIpKey"])
	}
	items := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 detail rows, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["accountId"] != "acc_alpha" {
		t.Fatalf("requestCount DESC ordering broken: %v", first["accountId"])
	}
	if first["accountName"] != "Alpha 账户" {
		t.Fatalf("account name not hydrated: %v", first["accountName"])
	}
	if first["accountOwnerSystemAccountId"] != adminID {
		t.Fatalf("owner id not hydrated: %v", first["accountOwnerSystemAccountId"])
	}
	if first["accountOwnerSystemAccountName"] != "root_name" {
		t.Fatalf("owner name not hydrated: %v", first["accountOwnerSystemAccountName"])
	}
	usage := first["rangeUsage"].(map[string]any)
	if usage["requestCount"].(float64) != 10 || usage["errorRate"].(float64) != 0.2 || usage["totalTokens"].(float64) != 300 {
		t.Fatalf("usage projection mismatch: %v", usage)
	}
	if usage["averageDurationMs"].(float64) != 100 || usage["maxDurationMs"].(float64) != 200 || usage["averageFirstTokenMs"].(float64) != 200 {
		t.Fatalf("average/max projection mismatch: %v", usage)
	}
	// acc_beta has no account row: the name fields stay omitted.
	beta := items[1].(map[string]any)
	if beta["accountId"] != "acc_beta" {
		t.Fatalf("second row mismatch: %v", beta)
	}
	if _, present := beta["accountName"]; present {
		t.Fatalf("unknown account must omit accountName: %v", beta)
	}
	if data["page"].(float64) != 1 || data["pageSize"].(float64) != 20 || data["hasMore"].(bool) {
		t.Fatalf("pagination envelope mismatch: %v", data)
	}
	if data["pageUpperBound"].(float64) != 2 {
		t.Fatalf("pageUpperBound mismatch: %v", data["pageUpperBound"])
	}
	if data["rangeReady"] != true {
		t.Fatalf("rangeReady mismatch: %v", data["rangeReady"])
	}
	if data["range"].(map[string]any)["startDate"] != "2026-09-01" || data["range"].(map[string]any)["endDate"] != "2026-09-03" {
		t.Fatalf("range mismatch: %v", data["range"])
	}

	// Unknown hash → 404 { message } (Node sendNotFound-free plain body).
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/ip-stats/"+strings.Repeat("b", 64)+"/detail", "")
	if status != http.StatusNotFound || payload["message"] != "IP 不存在" {
		t.Fatalf("missing IP contract mismatch: %d %v", status, payload)
	}

	// Non-hex hash → 400 IP 标识无效 (zod ipHashParamSchema).
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/ip-stats/not-a-hash/detail", "")
	if status != http.StatusBadRequest || payload["message"] != "IP 标识无效" {
		t.Fatalf("invalid hash contract mismatch: %d %v", status, payload)
	}

	// Invalid sort field → 400 IP 详情参数无效.
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/ip-stats/"+detailIPHash+"/detail?sortField=bogus", "")
	if status != http.StatusBadRequest || payload["message"] != "IP 详情参数无效" {
		t.Fatalf("invalid sort contract mismatch: %d %v", status, payload)
	}

	// Not-ready window: dirty hashes force the empty-items envelope.
	if _, err := env.db.Exec(`INSERT INTO client_ip_range_window_dirty_ips (ip_hash, generation, first_dirty_at, updated_at) VALUES ('dirty', 1, '2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/ip-stats/"+detailIPHash+"/detail", "")
	if status != http.StatusOK {
		t.Fatalf("stale detail failed: %d %v", status, payload)
	}
	data = payload["data"].(map[string]any)
	if len(data["items"].([]any)) != 0 || data["rangeReady"] != false || data["pageUpperBound"].(float64) != 0 {
		t.Fatalf("not-ready envelope mismatch: %v", data)
	}
}
