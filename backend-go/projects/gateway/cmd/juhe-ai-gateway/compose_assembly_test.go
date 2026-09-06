package main

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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// TestComposeSystemAPIWiresAPIKeyHandovers drives the M05/M07 assembly
// closures end to end in SQLite standalone mode:
//
//  1. M07 StatsUsageSource: the api-keys detail renders the usage aggregate
//     from the stats database (a nil source would render zeros).
//  2. M07 cleanup handover: the delete registers the cleanup target in the
//     DATASET database through the wired dataset handle + cleanup submitter.
//  3. M05 WithGlobalConcurrencyMax: the parsed JUHE_AI_CONCURRENCY_GLOBAL_MAX
//     lands in the stored high-concurrency default policy projection.
func TestComposeSystemAPIWiresAPIKeyHandovers(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.ConcurrencyGlobalMax = 4321
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	// Admin session (captcha disabled contract).
	mustChangePasswordFlag := false
	admin, err := composed.authDeps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: "handover-admin", DisplayName: "handover-admin_name", Password: "handover-admin-password-123", Role: "admin",
		MustChangePassword: &mustChangePasswordFlag,
	})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	// The default-resource bootstrap (BUG-0170.1) already provisioned the
	// fresh admin's default gpt group + default route strategy, so the api-key
	// create resolves its owner default chain without extra seeding.

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	login, err := client.Post(server.URL+"/__aisys__/api/auth/login", "application/json",
		strings.NewReader(`{"username":"handover-admin","password":"handover-admin-password-123"}`))
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	cookies := login.Cookies()
	_ = login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("admin login status=%d", login.StatusCode)
	}

	do := func(method, path, body string) (int, map[string]any) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request, err := http.NewRequest(method, server.URL+path, reader)
		if err != nil {
			t.Fatalf("build %s %s: %v", method, path, err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		var payload map[string]any
		_ = json.NewDecoder(response.Body).Decode(&payload)
		return response.StatusCode, payload
	}
	data := func(payload map[string]any) map[string]any {
		object, _ := payload["data"].(map[string]any)
		if object == nil {
			t.Fatalf("data envelope missing: %#v", payload)
		}
		return object
	}

	// M05 WithGlobalConcurrencyMax: a high-concurrency group stores the
	// default policy projection with the configured global max.
	code, payload := do(http.MethodPost, "/__aisys__/api/groups",
		`{"name":"hc-handover","providerCode":"gpt","groupType":"high_concurrency"}`)
	if code != http.StatusCreated {
		t.Fatalf("create group: %d %v", code, payload)
	}
	groupID := data(payload)["id"].(string)
	var policyJSON string
	if err := composed.DB.QueryRow(`SELECT scheduling_policy_json FROM groups WHERE id = ?`, groupID).Scan(&policyJSON); err != nil {
		t.Fatalf("read group policy: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		t.Fatalf("decode group policy %q: %v", policyJSON, err)
	}
	if policy["maxQueueSize"] != float64(4321) || policy["perApiKeyQueueLimit"] != float64(4321) {
		t.Fatalf("global max handover drift: %v", policy)
	}

	// The api-key under test.
	code, payload = do(http.MethodPost, "/__aisys__/api/api-keys", `{"name":"handover-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create api key: %d %v", code, payload)
	}
	keyID := data(payload)["id"].(string)

	// M07 StatsUsageSource: a stats-database aggregate row renders through the
	// detail projection (a nil source would render the zero summary).
	statsDB, err := sql.Open("sqlite", "file:"+cfg.StatsDatabasePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open stats database: %v", err)
	}
	t.Cleanup(func() { _ = statsDB.Close() })
	if _, err := statsDB.Exec(`INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_cost_usd, total_cost_usd, updated_at)
		VALUES (?, 'api_key', ?, 3, 10, 20, 0.25, 0.5, ?)`,
		admin.ID, keyID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed usage aggregate: %v", err)
	}
	code, payload = do(http.MethodGet, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusOK {
		t.Fatalf("api key detail: %d %v", code, payload)
	}
	detail := data(payload)
	usage, _ := detail["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("detail usage missing: %#v", detail)
	}
	if usage["requestCount"] != float64(3) || usage["totalTokens"] != float64(30) {
		t.Fatalf("stats usage source handover drift: %v", usage)
	}

	// M07 cleanup handover: the delete lands the cleanup target in the dataset
	// database (business database stays clean) and answers 204.
	code, payload = do(http.MethodDelete, "/__aisys__/api/api-keys/"+keyID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete api key: %d %v", code, payload)
	}
	datasetDB, err := sql.Open("sqlite", "file:"+cfg.DatasetDatabasePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open dataset database: %v", err)
	}
	t.Cleanup(func() { _ = datasetDB.Close() })
	var targets int
	if err := datasetDB.QueryRow(`SELECT COUNT(*) FROM api_key_record_cleanup_targets WHERE api_key_id = ?`, keyID).Scan(&targets); err != nil {
		t.Fatalf("read dataset cleanup target: %v", err)
	}
	if targets != 1 {
		t.Fatalf("cleanup target must land in the dataset database, got %d", targets)
	}
}
