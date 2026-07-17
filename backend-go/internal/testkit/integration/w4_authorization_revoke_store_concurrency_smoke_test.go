//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w4RevokeStoreApplicationName = "w4-authorization-revoke-store-concurrency"
	w4RevokeStoreAdvisoryLockKey = int64(4_017_202_607_17)

	w4RevokeStoreOwnerID   = "sys_w4_revoke_store_owner"
	w4RevokeStoreGranteeID = "sys_w4_revoke_store_grantee"
	w4RevokeStoreActor1ID  = "sys_w4_revoke_store_actor_1"
	w4RevokeStoreActor2ID  = "sys_w4_revoke_store_actor_2"
)

type w4RevokeStoreCallResult struct {
	found bool
	err   error
}

func TestW4AuthorizationRevokeStoreConcurrencyPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start authorization revoke postgres: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := container.Terminate(cleanupCtx); err != nil {
			t.Errorf("terminate authorization revoke postgres: %v", err)
		}
	})

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("authorization revoke postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close authorization revoke postgres: %v", err)
		}
	})
	runGooseMigrations(t, db)
	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("read authorization revoke Goose version: %v", err)
	}
	if version != 55 {
		t.Fatalf("authorization revoke Goose version = %d, want 55", version)
	}

	t1 := time.Date(2026, 7, 17, 13, 15, 0, 123456000, time.UTC)
	t2 := t1.Add(time.Second)
	insertW4RevokeStoreFixtures(t, ctx, db, t1)

	storeURL := w4RevokeStoreURLWithApplicationName(t, postgresURL)
	store, err := postgresstore.Open(ctx, storeURL)
	if err != nil {
		t.Fatalf("open authorization revoke postgres store: %v", err)
	}
	t.Cleanup(store.Close)

	assertW4RevokeStoreTerminalFixturesUnchanged(t, ctx, db, store, t1)
	assertW4RevokeStoreExpiredFixtureSucceeds(t, ctx, db, store, t1.Add(-time.Minute))

	installW4RevokeStoreBlockingTrigger(t, ctx, db)
	t.Cleanup(func() {
		cleanupW4RevokeStoreBlockingTrigger(t, db)
	})

	lockConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open authorization revoke advisory lock connection: %v", err)
	}
	lockReleased := false
	t.Cleanup(func() {
		cleanupW4RevokeStoreAdvisoryLock(t, lockConn, lockReleased)
	})
	if _, err := lockConn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin authorization revoke advisory lock transaction: %v", err)
	}
	if _, err := lockConn.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", w4RevokeStoreAdvisoryLockKey); err != nil {
		t.Fatalf("acquire authorization revoke advisory lock: %v", err)
	}

	beforeCounts := readW4RevokeStoreRowCounts(t, ctx, db)
	firstDone := make(chan w4RevokeStoreCallResult, 1)
	go func() {
		_, found, callErr := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
			AuthorizationID:      w4RevokeStoreGrantID("active"),
			ActorSystemAccountID: w4RevokeStoreActor1ID,
			CanAccessAll:         true,
			RevokedAt:            t1,
		})
		firstDone <- w4RevokeStoreCallResult{found: found, err: callErr}
	}()

	waitForW4RevokeStoreActivity(t, ctx, db, "first call entered trigger", func(rows []w4RevokeStoreActivity) bool {
		return countW4RevokeStoreWaitingQueries(rows, "update juhe_business.resource_authorization_grants", "advisory") == 1
	})

	secondDone := make(chan w4RevokeStoreCallResult, 1)
	go func() {
		_, found, callErr := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
			AuthorizationID:      w4RevokeStoreGrantID("active"),
			ActorSystemAccountID: w4RevokeStoreActor2ID,
			CanAccessAll:         true,
			RevokedAt:            t2,
		})
		secondDone <- w4RevokeStoreCallResult{found: found, err: callErr}
	}()

	waitForW4RevokeStoreActivity(t, ctx, db, "second call entered row lock wait", func(rows []w4RevokeStoreActivity) bool {
		firstWaiting := countW4RevokeStoreWaitingQueries(rows, "update juhe_business.resource_authorization_grants", "advisory") == 1
		secondWaiting := countW4RevokeStoreWaitingQueries(rows, "from juhe_business.resource_authorization_grants", "") >= 1
		return firstWaiting && secondWaiting
	})

	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := lockConn.ExecContext(releaseCtx, "COMMIT"); err != nil {
		releaseCancel()
		t.Fatalf("release authorization revoke advisory lock: %v", err)
	}
	releaseCancel()
	lockReleased = true

	first := receiveW4RevokeStoreCallResult(t, ctx, "first", firstDone)
	second := receiveW4RevokeStoreCallResult(t, ctx, "second", secondDone)
	if first.err != nil || !first.found {
		t.Fatalf("first authorization revoke found=%t err=%v, want true nil", first.found, first.err)
	}
	if second.err != nil || second.found {
		t.Fatalf("second authorization revoke found=%t err=%v, want false nil", second.found, second.err)
	}

	assertW4RevokeStoreFinalActiveRows(t, ctx, db, t1)
	if afterCounts := readW4RevokeStoreRowCounts(t, ctx, db); afterCounts != beforeCounts {
		t.Fatalf("authorization revoke row counts changed: before=%+v after=%+v", beforeCounts, afterCounts)
	}
}

func w4RevokeStoreURLWithApplicationName(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse authorization revoke postgres URL: %v", err)
	}
	query := parsed.Query()
	query.Set("application_name", w4RevokeStoreApplicationName)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func insertW4RevokeStoreFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	createdAt := now.Add(-2 * time.Hour)
	terminalAt := now.Add(-time.Hour)
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES
  ($1, 'w4-revoke-store-owner', 'W4 Revoke Store Owner', 'user', 'active', 'hash', false, false, $5, $5),
  ($2, 'w4-revoke-store-grantee', 'W4 Revoke Store Grantee', 'user', 'active', 'hash', false, false, $5, $5),
  ($3, 'w4-revoke-store-actor-1', 'W4 Revoke Store Actor 1', 'admin', 'active', 'hash', false, false, $5, $5),
  ($4, 'w4-revoke-store-actor-2', 'W4 Revoke Store Actor 2', 'admin', 'active', 'hash', false, false, $5, $5)
`, w4RevokeStoreOwnerID, w4RevokeStoreGranteeID, w4RevokeStoreActor1ID, w4RevokeStoreActor2ID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke store accounts: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, enabled, is_default,
  group_type, created_at, updated_at
) VALUES
  ($1, $5, 'W4 Revoke Store Active', 'openai', true, false, 'personal', $6, $6),
  ($2, $5, 'W4 Revoke Store Expired', 'openai', true, false, 'personal', $6, $6),
  ($3, $5, 'W4 Revoke Store Revoked', 'openai', true, false, 'personal', $6, $6),
  ($4, $5, 'W4 Revoke Store Returned', 'openai', true, false, 'personal', $6, $6)
`, w4RevokeStoreGroupID("active"), w4RevokeStoreGroupID("expired"), w4RevokeStoreGroupID("revoked"), w4RevokeStoreGroupID("returned"), w4RevokeStoreOwnerID, createdAt); err != nil {
		t.Fatalf("insert authorization revoke store groups: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.resource_authorizations (
  id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
  scope, status, effective_source_type, activated_at, last_source_changed_at, remark,
  expires_at, created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
) VALUES
  ($1, 'group', $5, $9, $10, 'use', 'active', 'manual', $11, $11, 'active fixture', NULL, $9, $11, NULL, NULL, NULL, $11),
  ($2, 'group', $6, $9, $10, 'use', 'expired', 'manual', $11, $11, 'expired fixture', $12, $9, $11, NULL, NULL, 'authorization_expired', $11),
  ($3, 'group', $7, $9, $10, 'use', 'revoked', NULL, $11, $12, 'revoked fixture', NULL, $9, $11, $9, $12, 'authorization_revoked', $12),
  ($4, 'group', $8, $9, $10, 'use', 'returned', NULL, $11, $12, 'returned fixture', NULL, $9, $11, $10, $12, 'grantee_returned', $12)
`, w4RevokeStoreRuntimeID("active"), w4RevokeStoreRuntimeID("expired"), w4RevokeStoreRuntimeID("revoked"), w4RevokeStoreRuntimeID("returned"),
		w4RevokeStoreGroupID("active"), w4RevokeStoreGroupID("expired"), w4RevokeStoreGroupID("revoked"), w4RevokeStoreGroupID("returned"),
		w4RevokeStoreOwnerID, w4RevokeStoreGranteeID, createdAt, terminalAt); err != nil {
		t.Fatalf("insert authorization revoke store runtime rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.resource_authorization_sources (
  id, authorization_id, source_type, status, activated_at, ended_at, ended_reason,
  created_by, created_at, revoked_by, revoked_at, updated_at
) VALUES
  ($1, $5, 'manual', 'active', $9, NULL, NULL, $10, $9, NULL, NULL, $9),
  ($2, $6, 'manual', 'active', $9, NULL, NULL, $10, $9, NULL, NULL, $9),
  ($3, $7, 'manual', 'revoked', $9, $11, 'authorization_revoked', $10, $9, $10, $11, $11),
  ($4, $8, 'manual', 'revoked', $9, $11, 'grantee_returned', $10, $9, $12, $11, $11)
`, w4RevokeStoreSourceID("active"), w4RevokeStoreSourceID("expired"), w4RevokeStoreSourceID("revoked"), w4RevokeStoreSourceID("returned"),
		w4RevokeStoreRuntimeID("active"), w4RevokeStoreRuntimeID("expired"), w4RevokeStoreRuntimeID("revoked"), w4RevokeStoreRuntimeID("returned"),
		createdAt, w4RevokeStoreOwnerID, terminalAt, w4RevokeStoreGranteeID); err != nil {
		t.Fatalf("insert authorization revoke store sources: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO juhe_business.resource_authorization_grants (
  id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
  grantee_system_account_id, scope, status, remark, expires_at, created_by, created_at,
  revoked_by, revoked_at, updated_at
) VALUES
  ($1, 'group', $5, $9, 'system_account', $10, 'use', 'active', 'active fixture', NULL, $9, $11, NULL, NULL, $11),
  ($2, 'group', $6, $9, 'system_account', $10, 'use', 'expired', 'expired fixture', $12, $9, $11, NULL, NULL, $11),
  ($3, 'group', $7, $9, 'system_account', $10, 'use', 'revoked', 'revoked fixture', NULL, $9, $11, $9, $12, $12),
  ($4, 'group', $8, $9, 'system_account', $10, 'use', 'returned', 'returned fixture', NULL, $9, $11, $10, $12, $12)
`, w4RevokeStoreGrantID("active"), w4RevokeStoreGrantID("expired"), w4RevokeStoreGrantID("revoked"), w4RevokeStoreGrantID("returned"),
		w4RevokeStoreGroupID("active"), w4RevokeStoreGroupID("expired"), w4RevokeStoreGroupID("revoked"), w4RevokeStoreGroupID("returned"),
		w4RevokeStoreOwnerID, w4RevokeStoreGranteeID, createdAt, terminalAt); err != nil {
		t.Fatalf("insert authorization revoke store grants: %v", err)
	}
}

func assertW4RevokeStoreTerminalFixturesUnchanged(t *testing.T, ctx context.Context, db *sql.DB, store *postgresstore.Store, now time.Time) {
	t.Helper()
	for _, status := range []string{"revoked", "returned"} {
		before := readW4RevokeStoreFixtureSnapshot(t, ctx, db, status)
		_, found, err := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
			AuthorizationID:      w4RevokeStoreGrantID(status),
			ActorSystemAccountID: w4RevokeStoreActor1ID,
			CanAccessAll:         true,
			RevokedAt:            now,
		})
		if err != nil || found {
			t.Fatalf("terminal %s authorization revoke found=%t err=%v, want false nil", status, found, err)
		}
		after := readW4RevokeStoreFixtureSnapshot(t, ctx, db, status)
		if after != before {
			t.Fatalf("terminal %s fixture changed:\nbefore=%s\nafter=%s", status, before, after)
		}
	}
}

func assertW4RevokeStoreExpiredFixtureSucceeds(t *testing.T, ctx context.Context, db *sql.DB, store *postgresstore.Store, now time.Time) {
	t.Helper()
	_, found, err := store.RevokeManagementResourceAuthorization(ctx, port.ManagementResourceAuthorizationRevokeInput{
		AuthorizationID:      w4RevokeStoreGrantID("expired"),
		ActorSystemAccountID: w4RevokeStoreActor1ID,
		CanAccessAll:         true,
		RevokedAt:            now,
	})
	if err != nil || !found {
		t.Fatalf("expired authorization revoke found=%t err=%v, want true nil", found, err)
	}
	assertW4RevokeStoreRowsRevokedBy(t, ctx, db, "expired", w4RevokeStoreActor1ID, now)
}

func installW4RevokeStoreBlockingTrigger(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
CREATE FUNCTION public.w4_revoke_store_block_first_update() RETURNS trigger
LANGUAGE plpgsql AS $function$
BEGIN
  IF OLD.id = '%s' THEN
    PERFORM pg_advisory_xact_lock(%d);
  END IF;
  RETURN NEW;
END
$function$;
CREATE TRIGGER w4_revoke_store_block_first_update
BEFORE UPDATE ON juhe_business.resource_authorization_grants
FOR EACH ROW EXECUTE FUNCTION public.w4_revoke_store_block_first_update();
`, w4RevokeStoreGrantID("active"), w4RevokeStoreAdvisoryLockKey)); err != nil {
		t.Fatalf("install authorization revoke blocking trigger: %v", err)
	}
}

func cleanupW4RevokeStoreBlockingTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	triggerCtx, triggerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer triggerCancel()
	if _, err := db.ExecContext(triggerCtx, `DROP TRIGGER IF EXISTS w4_revoke_store_block_first_update ON juhe_business.resource_authorization_grants`); err != nil {
		t.Errorf("drop authorization revoke blocking trigger: %v", err)
	}
	functionCtx, functionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer functionCancel()
	if _, err := db.ExecContext(functionCtx, `DROP FUNCTION IF EXISTS public.w4_revoke_store_block_first_update()`); err != nil {
		t.Errorf("drop authorization revoke blocking function: %v", err)
	}
}

func cleanupW4RevokeStoreAdvisoryLock(t *testing.T, conn *sql.Conn, released bool) {
	t.Helper()
	if !released {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := conn.ExecContext(rollbackCtx, "ROLLBACK"); err != nil {
			t.Errorf("rollback authorization revoke advisory lock: %v", err)
		}
		rollbackCancel()
	}
	if err := conn.Close(); err != nil {
		t.Errorf("close authorization revoke advisory lock connection: %v", err)
	}
}

type w4RevokeStoreActivity struct {
	pid           int
	waitEventType string
	waitEvent     string
	query         string
}

func waitForW4RevokeStoreActivity(t *testing.T, ctx context.Context, db *sql.DB, label string, ready func([]w4RevokeStoreActivity) bool) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last []w4RevokeStoreActivity
	for {
		last = readW4RevokeStoreActivities(t, waitCtx, db)
		if ready(last) {
			return
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("wait for %s: %v; activities=%+v", label, waitCtx.Err(), last)
		case <-ticker.C:
		}
	}
}

func readW4RevokeStoreActivities(t *testing.T, ctx context.Context, db *sql.DB) []w4RevokeStoreActivity {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT pid, COALESCE(wait_event_type, ''), COALESCE(wait_event, ''), query
FROM pg_stat_activity
WHERE application_name = $1 AND state = 'active'
ORDER BY pid
`, w4RevokeStoreApplicationName)
	if err != nil {
		t.Fatalf("read authorization revoke pg_stat_activity: %v", err)
	}
	defer rows.Close()
	var activities []w4RevokeStoreActivity
	for rows.Next() {
		var activity w4RevokeStoreActivity
		if err := rows.Scan(&activity.pid, &activity.waitEventType, &activity.waitEvent, &activity.query); err != nil {
			t.Fatalf("scan authorization revoke pg_stat_activity: %v", err)
		}
		activities = append(activities, activity)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization revoke pg_stat_activity: %v", err)
	}
	return activities
}

func countW4RevokeStoreWaitingQueries(rows []w4RevokeStoreActivity, queryFragment string, waitEvent string) int {
	count := 0
	for _, row := range rows {
		if !strings.EqualFold(row.waitEventType, "Lock") || !strings.Contains(strings.ToLower(row.query), queryFragment) {
			continue
		}
		if waitEvent != "" && !strings.EqualFold(row.waitEvent, waitEvent) {
			continue
		}
		count++
	}
	return count
}

func receiveW4RevokeStoreCallResult(t *testing.T, ctx context.Context, label string, results <-chan w4RevokeStoreCallResult) w4RevokeStoreCallResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		t.Fatalf("wait for %s authorization revoke result: %v", label, ctx.Err())
		return w4RevokeStoreCallResult{}
	}
}

type w4RevokeStoreRowCounts struct {
	grants  int
	runtime int
	sources int
}

func readW4RevokeStoreRowCounts(t *testing.T, ctx context.Context, db *sql.DB) w4RevokeStoreRowCounts {
	t.Helper()
	var counts w4RevokeStoreRowCounts
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM juhe_business.resource_authorization_grants WHERE id LIKE 'rauthgrant_w4_revoke_store_%'),
  (SELECT count(*) FROM juhe_business.resource_authorizations WHERE id LIKE 'rauth_w4_revoke_store_%'),
  (SELECT count(*) FROM juhe_business.resource_authorization_sources WHERE id LIKE 'rauthsrc_w4_revoke_store_%')
`).Scan(&counts.grants, &counts.runtime, &counts.sources); err != nil {
		t.Fatalf("read authorization revoke row counts: %v", err)
	}
	return counts
}

func readW4RevokeStoreFixtureSnapshot(t *testing.T, ctx context.Context, db *sql.DB, status string) string {
	t.Helper()
	var snapshot string
	if err := db.QueryRowContext(ctx, `
SELECT jsonb_build_object(
  'grant', (SELECT to_jsonb(g) FROM juhe_business.resource_authorization_grants g WHERE g.id = $1),
  'runtime', (SELECT to_jsonb(r) FROM juhe_business.resource_authorizations r WHERE r.id = $2),
  'source', (SELECT to_jsonb(s) FROM juhe_business.resource_authorization_sources s WHERE s.id = $3)
)::text
`, w4RevokeStoreGrantID(status), w4RevokeStoreRuntimeID(status), w4RevokeStoreSourceID(status)).Scan(&snapshot); err != nil {
		t.Fatalf("read authorization revoke %s fixture snapshot: %v", status, err)
	}
	return snapshot
}

func assertW4RevokeStoreFinalActiveRows(t *testing.T, ctx context.Context, db *sql.DB, wantTime time.Time) {
	t.Helper()
	assertW4RevokeStoreRowsRevokedBy(t, ctx, db, "active", w4RevokeStoreActor1ID, wantTime)

	var runtimeChangedAt, sourceEndedAt time.Time
	if err := db.QueryRowContext(ctx, `
SELECT r.last_source_changed_at, s.ended_at
FROM juhe_business.resource_authorizations r
JOIN juhe_business.resource_authorization_sources s ON s.authorization_id = r.id
WHERE r.id = $1 AND s.id = $2
`, w4RevokeStoreRuntimeID("active"), w4RevokeStoreSourceID("active")).Scan(&runtimeChangedAt, &sourceEndedAt); err != nil {
		t.Fatalf("read authorization revoke derived timestamps: %v", err)
	}
	if !runtimeChangedAt.Equal(wantTime) || !sourceEndedAt.Equal(wantTime) {
		t.Fatalf("authorization revoke derived timestamps runtime=%s source=%s, want %s", runtimeChangedAt, sourceEndedAt, wantTime)
	}
}

func assertW4RevokeStoreRowsRevokedBy(t *testing.T, ctx context.Context, db *sql.DB, fixture, wantActor string, wantTime time.Time) {
	t.Helper()
	queries := []struct {
		name  string
		query string
		id    string
	}{
		{name: "grant", query: `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorization_grants WHERE id = $1`, id: w4RevokeStoreGrantID(fixture)},
		{name: "runtime", query: `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorizations WHERE id = $1`, id: w4RevokeStoreRuntimeID(fixture)},
		{name: "source", query: `SELECT status, revoked_by, revoked_at, updated_at FROM juhe_business.resource_authorization_sources WHERE id = $1`, id: w4RevokeStoreSourceID(fixture)},
	}
	for _, item := range queries {
		var status, actor string
		var revokedAt, updatedAt time.Time
		if err := db.QueryRowContext(ctx, item.query, item.id).Scan(&status, &actor, &revokedAt, &updatedAt); err != nil {
			t.Fatalf("read authorization revoke %s %s row: %v", fixture, item.name, err)
		}
		if status != "revoked" || actor != wantActor || !revokedAt.Equal(wantTime) || !updatedAt.Equal(wantTime) {
			t.Fatalf("authorization revoke %s %s status=%q actor=%q revokedAt=%s updatedAt=%s, want revoked/%s/%s", fixture, item.name, status, actor, revokedAt, updatedAt, wantActor, wantTime)
		}
	}
}

func w4RevokeStoreGrantID(status string) string {
	return "rauthgrant_w4_revoke_store_" + status
}

func w4RevokeStoreRuntimeID(status string) string {
	return "rauth_w4_revoke_store_" + status
}

func w4RevokeStoreSourceID(status string) string {
	return "rauthsrc_w4_revoke_store_" + status
}

func w4RevokeStoreGroupID(status string) string {
	return "grp_w4_revoke_store_" + status
}
