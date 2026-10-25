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

	t.Run("pg_lock_row_read", func(t *testing.T) {
		// 迁真实 PG 后本臂升级为 FOR UPDATE 行锁读的真实执行：缺失键按契约
		// no-op（nil, nil）；存量键沿 PG 臂 FOR UPDATE 读后按 revision 冲突
		// 映射（原「PG 方言在 sqlite 上必失败」语义随 sqlite 夹具退役）。
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(ctx, CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		pgStore, err := NewStore(env.db, testSecret, time.Now, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgStore.Patch(ctx, "w11a-missing", &PatchInput{ExpectedRevision: "2026-01-01T00:00:00.000Z"}, AccessScope{}); err != nil {
			t.Fatalf("missing key must stay a no-op on the PG arm: %v", err)
		}
		if _, err := pgStore.Patch(ctx, created.ID, &PatchInput{ExpectedRevision: "stale-revision"}, AccessScope{}); err == nil {
			t.Fatal("FOR UPDATE row read must surface the revision conflict")
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
		if _, err := db.Exec(`CREATE FUNCTION w11a_block_update() RETURNS trigger AS $trig$
			BEGIN RAISE EXCEPTION 'w11a blocked'; END
			$trig$ LANGUAGE plpgsql`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_update BEFORE UPDATE ON api_keys
			FOR EACH ROW EXECUTE FUNCTION w11a_block_update()`); err != nil {
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
		if err := db.QueryRow(`SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_id = $1`, id).Scan(&count); err != nil {
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
	if err := db.QueryRow(`SELECT updated_at FROM api_keys WHERE id = $1`, id).Scan(&revision); err != nil {
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
		if _, err := db.Exec(`CREATE FUNCTION w11a_block_refresh() RETURNS trigger AS $trig$
			BEGIN RAISE EXCEPTION 'w11a blocked'; END
			$trig$ LANGUAGE plpgsql`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TRIGGER w11a_block_refresh BEFORE UPDATE ON api_keys
			FOR EACH ROW EXECUTE FUNCTION w11a_block_refresh()`); err != nil {
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
		if _, err := db.Exec(`UPDATE api_keys SET quota_limits_json = $1 WHERE id = $2`,
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

	t.Run("delete_pg_dialect_existing_key", func(t *testing.T) {
		// 迁真实 PG 后本臂升级为 PG 臂删除路径的真实执行：缺失键按契约
		// 幂等 no-op（&DeleteResult{}, nil）；存量键沿 PG 臂事务删除并落
		// juhe_dataset cleanup 目标（原「pg-qualified 在 sqlite 必失败」语义
		// 随 sqlite 夹具退役）。
		env := newTestEnv(t)
		ownerID := env.login(t, "root", "root-pass", "super_admin")
		env.seedDefaultRouteStrategy(t, ownerID, "rs-w11a")
		created, _, err := env.store.Create(ctx, CreateInput{Name: "w11a-key"}, AccessScope{ViewerID: ownerID})
		if err != nil {
			t.Fatal(err)
		}
		pgStore, err := NewStore(env.db, testSecret, time.Now, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pgStore.Delete(ctx, "w11a-any", AccessScope{}); err != nil {
			t.Fatalf("missing key delete must stay a no-op on the PG arm: %v", err)
		}
		outcome, err := pgStore.Delete(ctx, created.ID, AccessScope{})
		if err != nil || !outcome.Deleted {
			t.Fatalf("pg delete on the existing key must succeed: %+v %v", outcome, err)
		}
		var count int
		if err := env.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.api_key_record_cleanup_targets WHERE api_key_id = $1`, created.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("cleanup target rows = %d", count)
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
	source, err := NewStatsUsageSource(newStatsDB(t))
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
		if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_tokens, thinking_tokens, total_cost_usd, last_used_at)
			VALUES ('sysacc_1', 'api_key', 'w11a-key', 1, 1, 1, 0, 0, 0, NULL)`); err != nil {
			t.Fatal(err)
		}
		fullSource, err := NewStatsUsageSource(db)
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
	// 缺表错误直接传播（无 read-worker 降级臂——已随 sqlite 生产臂删除）。
	broken, err := NewStatsUsageSource(newEmptyStatsDB(t))
	if err != nil {
		t.Fatal(err)
	}
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
