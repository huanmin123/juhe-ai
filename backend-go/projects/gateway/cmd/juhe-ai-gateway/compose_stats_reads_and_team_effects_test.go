package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/systemteams"
)

// composeStatsWiringFixture assembles the SQLite standalone composition the
// same way compose_assembly_test.go does and logs in a fresh admin, returning
// the composition plus a JSON helper over the httptest server.
type composeStatsWiringFixture struct {
	composed *composition
	do       func(method, path, body string) (int, map[string]any)
	data     func(payload map[string]any) map[string]any
}

func newComposeStatsWiringFixture(t *testing.T) *composeStatsWiringFixture {
	t.Helper()
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	t.Cleanup(func() { composed.Shutdown() })
	seedSystemSettings(t, composed.DB)

	mustChangePasswordFlag := false
	if _, err := composed.authDeps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: "wiring-admin", DisplayName: "wiring-admin_name", Password: "wiring-admin-password-123", Role: "admin",
		MustChangePassword: &mustChangePasswordFlag,
	}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	server := httptest.NewServer(composed.Kernel)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	login, err := client.Post(server.URL+"/__aisys__/api/auth/login", "application/json",
		strings.NewReader(`{"username":"wiring-admin","password":"wiring-admin-password-123"}`))
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
	return &composeStatsWiringFixture{composed: composed, do: do, data: data}
}

// usageWindowDateKeys resolves the trailing 31-day fixed window keys exactly
// like defaultUsageStatsRange does under the seeded usageStatsTimezone=UTC
// (today-30 .. today).
func usageWindowDateKeys() (string, string) {
	today := time.Now().UTC()
	start := today.AddDate(0, 0, -30)
	return start.Format("2006-01-02"), today.Format("2006-01-02")
}

// TestComposeSystemAPIWiresAuthzUsageStatsReads is the assembly assertion for
// the authorization usage-window reads: in SQLite standalone mode the
// authzStore must carry the dedicated stats database handle
// (authz.Store.AttachStatsDatabase), so a seeded
// authorization_team_usage_range_windows row in the stats file renders through
// GET /authorizations/usage/team-details. A missing injection would fall back
// to the business handle where the stats table does not exist (500).
func TestComposeSystemAPIWiresAuthzUsageStatsReads(t *testing.T) {
	fixture := newComposeStatsWiringFixture(t)
	composed := fixture.composed

	startDate, endDate := usageWindowDateKeys()
	// Seed through the composition-owned stats handle — the exact handle the
	// authzStore must have received through AttachStatsDatabase.
	if composed.statsDB == nil {
		t.Fatal("composition must own a stats database handle in sqlite mode")
	}
	if _, err := composed.statsDB.Exec(`INSERT INTO authorization_team_usage_range_windows
		(system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
		 request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens,
		 cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens,
		 total_cost_usd, last_used_at, updated_at)
		VALUES ('global', ?, ?, 'team_seed', 'group', 'grp_seed',
		 3, 10, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0.75, NULL, ?)`,
		startDate, endDate, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed authorization usage window row: %v", err)
	}

	code, payload := fixture.do(http.MethodGet, "/__aisys__/api/authorizations/usage/team-details", "")
	if code != http.StatusOK {
		t.Fatalf("authorization team usage rows: %d %v", code, payload)
	}
	rows, _ := fixture.data(payload)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 seeded usage row, got %d: %#v", len(rows), fixture.data(payload)["rows"])
	}
	row, _ := rows[0].(map[string]any)
	if row["teamId"] != "team_seed" {
		t.Fatalf("usage row team drift: %#v", row)
	}
	usage, _ := row["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("usage row missing usage projection: %#v", row)
	}
	if usage["requestCount"] != float64(3) || usage["totalTokens"] != float64(30) || usage["totalCost"] != float64(0.75) {
		t.Fatalf("stats-backed usage read drift: %v", usage)
	}
	rng, _ := fixture.data(payload)["range"].(map[string]any)
	if rng["startDate"] != startDate || rng["endDate"] != endDate {
		t.Fatalf("usage range drift: %#v (want %s..%s)", rng, startDate, endDate)
	}
}

// TestComposeSystemAPIWiresSystemTeamsSideEffects is the assembly assertion
// for the system-teams committed-write side effects (C9): a team status
// transition through the composed store must mark the group-account-stats
// dirty row (groupdirtycursor.Store via WithSideEffects) and invalidate the
// gateway runtime + authorization quota caches through the K5 bus.
func TestComposeSystemAPIWiresSystemTeamsSideEffects(t *testing.T) {
	fixture := newComposeStatsWiringFixture(t)
	composed := fixture.composed

	var mu sync.Mutex
	var invalidations []string
	for _, topic := range []string{inval.TopicGatewayRuntime, inval.TopicAuthorizationQuota} {
		watched := topic
		composed.Bus.Subscribe(watched, func(topic, reason string) {
			mu.Lock()
			defer mu.Unlock()
			invalidations = append(invalidations, topic+"|"+reason)
		})
	}

	actor := "sysacc_team_effects"
	created, err := composed.teamStore.Create(context.Background(), "sideeffects-team", nil, nil, actor)
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	disabled := "disabled"
	outcome, err := composed.teamStore.Patch(context.Background(), created.ID,
		systemteams.PatchInput{ExpectedUpdatedAt: created.EditVersion, Status: &disabled},
		systemteams.AccessScope{ViewerID: actor, IsAdmin: true})
	if err != nil {
		t.Fatalf("patch team status: %v", err)
	}
	if outcome == nil || outcome.Status != "updated" {
		t.Fatalf("patch outcome drift: %+v", outcome)
	}

	var reason string
	if err := composed.DB.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id = '__all__'`).Scan(&reason); err != nil {
		t.Fatalf("group-stats dirty marker must land in the business database: %v", err)
	}
	if reason != "team_authorization_changed" {
		t.Fatalf("dirty marker reason drift: %q", reason)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{
		inval.TopicGatewayRuntime + "|team_authorization_changed":     false,
		inval.TopicAuthorizationQuota + "|team_authorization_changed": false,
	}
	for _, call := range invalidations {
		if _, ok := want[call]; ok {
			want[call] = true
		}
	}
	for call, seen := range want {
		if !seen {
			t.Fatalf("missing bus invalidation %q, got %v", call, invalidations)
		}
	}
}
