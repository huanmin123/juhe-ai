package apikeys

// w11a coverage arms (part 3): direct store-level arms (Patch/RefreshSecret/
// Delete error paths, PG dialect locks, FindSecret corruption, usage source
// degrade and empty-input branches, quota weekly/monthly/total failures).

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestW11AStorePatchDirectArms(t *testing.T) {
	seed := func(t *testing.T) (*Store, *sql.DB, string) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(context.Background(), CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		return env.store, env.db, created.ID
	}
	ctx := context.Background()

	t.Run("closed_db", func(t *testing.T) {
		store, db, id := seed(t)
		revision := storeRevision(t, db, id)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		disabled := "disabled"
		if _, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision, HasStatus: true, Status: disabled}, AccessScope{}); err == nil {
			t.Fatal("closed db must fail Patch")
		}
	})

	t.Run("pg_lock", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		pgStore, err := NewStore(env.db, true, testSecret, time.Now, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgStore.Patch(ctx, "w11a-missing", &PatchInput{ExpectedRevision: "2026-01-01T00:00:00.000Z"}, AccessScope{}); err == nil {
			t.Fatal("PG FOR UPDATE must fail on SQLite")
		}
	})

	t.Run("broken_schema", func(t *testing.T) {
		store, db, id := seed(t)
		revision := storeRevision(t, db, id)
		if _, err := db.Exec(`DROP TABLE api_keys`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision}, AccessScope{}); err == nil {
			t.Fatal("broken schema must fail Patch")
		}
	})

	t.Run("field_guards", func(t *testing.T) {
		store, db, id := seed(t)
		revision := storeRevision(t, db, id)
		long := strings.Repeat("x", 201)
		if _, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision, HasDescription: true, Description: &long}, AccessScope{}); err == nil || !strings.Contains(err.Error(), "200") {
			t.Fatalf("long description = %v", err)
		}
		badStatus := "frozen"
		if _, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision, HasStatus: true, Status: badStatus}, AccessScope{}); err == nil || !strings.Contains(err.Error(), "状态") {
			t.Fatalf("bad status = %v", err)
		}
	})

	t.Run("update_trigger_failure", func(t *testing.T) {
		store, db, id := seed(t)
		revision := storeRevision(t, db, id)
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_update BEFORE UPDATE ON api_keys
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		disabled := "disabled"
		if _, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision, HasStatus: true, Status: disabled}, AccessScope{}); err == nil {
			t.Fatal("update failure must fail Patch")
		}
	})

	t.Run("binding_upsert_marks_dirty", func(t *testing.T) {
		store, db, id := seed(t)
		revision := storeRevision(t, db, id)
		active := "active"
		limits := map[string]any{"hourly": map[string]any{"enabled": true, "limit": float64(1), "hours": float64(2)}}
		outcome, err := store.Patch(ctx, id, &PatchInput{ExpectedRevision: revision, HasStatus: true, Status: active, QuotaLimits: limits, HasQuotaLimits: true}, AccessScope{})
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Result.Revision == revision || outcome.Result.Revision == "" {
			t.Fatalf("revision must advance: %+v", outcome.Result)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("binding rows = %d", count)
		}
	})
}

func storeRevision(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var revision string
	if err := db.QueryRow(`SELECT updated_at FROM api_keys WHERE id = ?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestW11AStoreRefreshAndDeleteDirectArms(t *testing.T) {
	seed := func(t *testing.T) (*Store, *sql.DB, string) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(context.Background(), CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		return env.store, env.db, created.ID
	}
	ctx := context.Background()

	t.Run("refresh_trigger_failure", func(t *testing.T) {
		store, db, id := seed(t)
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_refresh BEFORE UPDATE ON api_keys
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RefreshSecret(ctx, id, AccessScope{}); err == nil {
			t.Fatal("refresh update failure must fail")
		}
	})

	t.Run("refresh_closed_db", func(t *testing.T) {
		store, db, id := seed(t)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RefreshSecret(ctx, id, AccessScope{}); err == nil {
			t.Fatal("closed db refresh must fail")
		}
	})

	t.Run("delete_binding_failure", func(t *testing.T) {
		store, db, id := seed(t)
		if _, err := db.Exec(`UPDATE api_keys SET quota_limits_json = ? WHERE id = ?`,
			`{"hourly":{"enabled":true,"limit":1,"hours":2}}`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Delete(ctx, id, AccessScope{}); err == nil {
			t.Fatal("binding cleanup failure must fail Delete")
		}
	})

	t.Run("delete_pg_dialect_fails_on_sqlite", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		pgStore, err := NewStore(env.db, true, testSecret, time.Now, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgStore.Delete(ctx, "w11a-any", AccessScope{}); err == nil {
			t.Fatal("pg-qualified delete must fail on the sqlite fixture")
		}
	})
}

func TestW11AStoreListAndFindArms(t *testing.T) {
	t.Run("chat_purpose_and_broken_projections", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(context.Background(), CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		id := created.ID
		// Corrupt quota projection with the strategy join intact (an inner
		// join drops rows whose strategy vanished, hiding the corruption).
		env.exec(t, `UPDATE api_keys SET quota_limits_json = '{bad' WHERE id = ?`, id)
		if _, err := env.store.ListPage(context.Background(), AccessScope{IsAdmin: true}, ListOptions{}); err == nil {
			t.Fatal("corrupt quota projection must fail the list")
		}
		env.exec(t, `UPDATE api_keys SET quota_limits_json = NULL, purpose = 'chat' WHERE id = ?`, id)
		page, err := env.store.ListPage(context.Background(), AccessScope{IsAdmin: true}, ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range page.Items {
			if item.ID == id && item.Purpose == "chat" && item.RouteStrategyName != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("chat item projection = %+v", page.Items)
		}
		if _, err := env.store.FindDetail(context.Background(), id, AccessScope{IsAdmin: true}); err != nil {
			t.Fatal(err)
		}
		env.exec(t, `DROP TABLE api_keys`)
		if _, err := env.store.FindDetail(context.Background(), id, AccessScope{IsAdmin: true}); err == nil {
			t.Fatal("broken schema must fail FindDetail")
		}
		if _, err := env.store.FindSecret(context.Background(), id, AccessScope{IsAdmin: true}); err == nil {
			t.Fatal("broken schema must fail FindSecret")
		}
	})

	t.Run("find_secret_envelope_arms", func(t *testing.T) {
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(context.Background(), CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		id := created.ID
		// Empty envelope.
		env.exec(t, `UPDATE api_keys SET key_secret_encrypted = ' ' WHERE id = ?`, id)
		if _, err := env.store.FindSecret(context.Background(), id, AccessScope{IsAdmin: true}); err == nil {
			t.Fatal("blank envelope must fail")
		}
		// Valid envelope with an empty key payload.
		envelope, err := EncryptJSON(testSecret, map[string]any{"key": ""})
		if err != nil {
			t.Fatal(err)
		}
		env.exec(t, `UPDATE api_keys SET key_secret_encrypted = ? WHERE id = ?`, envelope, id)
		if _, err := env.store.FindSecret(context.Background(), id, AccessScope{IsAdmin: true}); err == nil {
			t.Fatal("empty key payload must fail")
		}
	})
}

func TestW11AQuotaRemainingArms(t *testing.T) {
	for _, document := range []string{
		`{"weekly":null}`,
		`{"weekly":"x"}`,
		`{"monthly":null}`,
		`{"monthly":"x"}`,
		`{"total":null}`,
		`{"total":"x"}`,
	} {
		if _, err := ParseQuotaLimitsJSON(document); err == nil {
			t.Fatalf("%s must fail", document)
		}
	}
	if _, err := ParseQuotaLimitsJSON(`{"daily":{"enabled":true,"limit":0.0000001}}`); err == nil {
		t.Fatal("sub-micro limit must fail")
	}
}

func TestW11AUsageSourceArms(t *testing.T) {
	ctx := context.Background()
	source, err := NewStatsUsageSource(newStatsDB(t), false)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := source.ApiKeyListUsageSummaries(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list summaries = %v, %v", empty, err)
	}
	emptyFull, err := source.ApiKeyUsageSummaries(ctx, nil)
	if err != nil || len(emptyFull) != 0 {
		t.Fatalf("empty full summaries = %v, %v", emptyFull, err)
	}
	// A row with NULL cache_read_cost falls back to the zero-cost parse.
	t.Run("null_cost_row", func(t *testing.T) {
		db := newStatsDB(t)
		if _, err := db.Exec(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_tokens, thinking_tokens, total_cost_usd, last_used_at)
			VALUES ('sysacc_1', 'api_key', 'w11a-key', 1, 1, 1, 0, 0, 0, NULL)`); err != nil {
			t.Fatal(err)
		}
		fullSource, err := NewStatsUsageSource(db, false)
		if err != nil {
			t.Fatal(err)
		}
		full, err := fullSource.ApiKeyUsageSummaries(ctx, []UsageScope{{RowKey: "w11a-key", SystemAccountID: "sysacc_1", ScopeID: "w11a-key"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := full["w11a-key"]; !ok {
			t.Fatalf("summary missing: %+v", full)
		}
	})
	// Missing stats table inside the read worker degrades per chunk.
	t.Setenv("JUHE_AI_SQLITE_READ_WORKER", "true")
	missing, err := NewStatsUsageSource(newEmptyStatsDB(t), false)
	if err != nil {
		t.Fatal(err)
	}
	degraded, err := missing.ApiKeyUsageSummaries(ctx, []UsageScope{{RowKey: "w11a-key", SystemAccountID: "sysacc_1", ScopeID: "w11a-key"}})
	if err != nil || len(degraded) != 0 {
		t.Fatalf("degraded full summaries = %v, %v", degraded, err)
	}
	// Column-mismatched table fails the row scan.
	broken, err := NewStatsUsageSource(newEmptyStatsDB(t), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUHE_AI_SQLITE_READ_WORKER", "")
	// (read worker off: every error propagates)
	if _, err := broken.ApiKeyUsageSummaries(ctx, []UsageScope{{RowKey: "w11a-key", SystemAccountID: "sysacc_1", ScopeID: "w11a-key"}}); err == nil {
		t.Fatal("missing stats table must propagate")
	}
}

func TestW11APatchBodyTypeArms(t *testing.T) {
	const revision = "2026-01-01T00:00:00.000Z"
	if _, issue := parsePatchBody(map[string]any{"expectedRevision": revision, "expiresAt": 5}); issue == "" {
		t.Fatal("non-string expiresAt must fail")
	}
	if _, issue := parsePatchBody(map[string]any{"expectedRevision": revision, "quotaLimits": "x"}); issue == "" {
		t.Fatal("non-object quotaLimits must fail")
	}
	if _, issue := parsePatchBody(map[string]any{"expectedRevision": revision, "availabilitySchedule": 5}); issue == "" {
		t.Fatal("non-object schedule must fail")
	}
}

var _ = errors.New
