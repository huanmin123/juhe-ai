package gatewayquota

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// authzSchema creates the business tables the quota joins（生产 PG 臂方言：
// juhe_business 限定，列集按 groups pgkit 先例放宽为 authzquota 实际读写的
// 列，类型照抄 maintenance 生产 DDL pg_schema_business_tables.go）。
func authzSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE juhe_business.resource_authorizations (
			id text PRIMARY KEY, resource_owner_system_account_id text NOT NULL, grantee_system_account_id text NOT NULL,
			resource_type text NOT NULL, resource_id text NOT NULL, effective_source_team_id text, limits_json text, status text NOT NULL DEFAULT 'active')`,
		`CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY, system_account_id text NOT NULL, authorization_instance_authorization_id text,
			authorization_instance_source_account_id text)`,
		`CREATE TABLE juhe_business.resource_authorization_grants (
			id text PRIMARY KEY, resource_type text NOT NULL, resource_id text NOT NULL, grantee_type text NOT NULL, grantee_team_id text,
			status text NOT NULL DEFAULT 'active', resource_owner_system_account_id text NOT NULL, limits_json text)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create authz schema: %v", err)
		}
	}
}

func seedAuthzRow(t *testing.T, db *sql.DB, id, owner, grantee, resourceType, resourceID, teamID, limitsJSON, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorizations
		(id, resource_owner_system_account_id, grantee_system_account_id, resource_type, resource_id, effective_source_team_id, limits_json, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, owner, grantee, resourceType, resourceID, teamID, limitsJSON, status); err != nil {
		t.Fatalf("seed resource_authorization %s: %v", id, err)
	}
}

func seedGrantRow(t *testing.T, db *sql.DB, id, resourceType, resourceID, teamID, owner, limitsJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.resource_authorization_grants
		(id, resource_type, resource_id, grantee_type, grantee_team_id, status, resource_owner_system_account_id, limits_json)
		VALUES ($1, $2, $3, 'team', $4, 'active', $5, $6)`,
		id, resourceType, resourceID, teamID, owner, limitsJSON); err != nil {
		t.Fatalf("seed grant %s: %v", id, err)
	}
}

func seedInstanceAccount(t *testing.T, db *sql.DB, id, systemAccountID, authorizationID, sourceAccountID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts
		(id, system_account_id, authorization_instance_authorization_id, authorization_instance_source_account_id)
		VALUES ($1, $2, $3, $4)`, id, systemAccountID, authorizationID, sourceAccountID); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

type authzFixture struct {
	service  *AuthorizationQuotaService
	business *sql.DB
	stats    *StatsStore
	statsDB  *sql.DB
	clock    *fakeClock
}

func newAuthzFixture(t *testing.T, modes Modes, clock *fakeClock) *authzFixture {
	t.Helper()
	business := newTestDB(t, "authz-biz")
	statsDB := newTestDB(t, "authz-stats")
	authzSchema(t, business)
	statsSchema(t, statsDB)
	stats, err := NewStatsStore(statsDB)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	snapshot := mustSnapshotCache(t, modes, clock)
	service, err := NewAuthorizationQuotaService(AuthorizationQuotaConfig{
		Modes:    modes,
		Business: business,
		Stats:    stats,
		Timezone: mustTZ(t, time.UTC),
		Snapshot: snapshot,
		Now:      clock.Now,
	})
	if err != nil {
		t.Fatalf("NewAuthorizationQuotaService: %v", err)
	}
	return &authzFixture{service: service, business: business, stats: stats, statsDB: statsDB, clock: clock}
}

func TestCheckAuthorizationQuotaBatchByIDs(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	// Group authorization ga1: owner sysA, grantee sysB, daily limit 10.
	seedAuthzRow(t, fixture.business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	// Account authorization aa1: daily limit 5, with its instance account.
	seedAuthzRow(t, fixture.business, "aa1", "sysA", "sysB", "account", "acc1", "", `{"daily":{"enabled":true,"limit":5}}`, "active")
	seedInstanceAccount(t, fixture.business, "inst1", "sysB", "aa1", "acc1")
	// Paused row must be ignored entirely.
	seedAuthzRow(t, fixture.business, "ga2", "sysA", "sysB", "group", "g2", "", `{"daily":{"enabled":true,"limit":0.01}}`, "paused")

	// Costs: group scope exceeded, account scope fine.
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 10})
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysB", "account_authorization", "aa1", "2026-09-04", 1})

	decisions, err := fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "ga1", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "aa1"},
		{AccountID: "u2", AccountAuthorizationID: ""},
		{AccountID: "u3", AccountAuthorizationID: "aa1"},
	}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDs: %v", err)
	}
	if len(decisions) != 3 {
		t.Fatalf("decisions length = %d", len(decisions))
	}
	// u1: group exceeded -> deny; u2: no account authz, group still exceeded -> deny;
	// u3: same scopes as u1 -> deny.
	for index, want := range []bool{false, false, false} {
		if decisions[index].Allowed != want {
			t.Fatalf("decision[%d] = %+v, want allowed=%v", index, decisions[index], want)
		}
	}
	if decisions[0].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("message = %q", decisions[0].Message)
	}

	// Drop the group cost below the limit: everything allows.
	if _, err := fixture.statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 9 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	decisions, err = fixture.service.CheckAuthorizationQuotaBatchByIDsReadOnly(ctx, "ga1", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "aa1"},
	}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDsReadOnly: %v", err)
	}
	if !decisions[0].Allowed {
		t.Fatalf("read-only fresh decision must allow: %+v", decisions[0])
	}

	// Account-scope boundary: exactly at the account limit denies.
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysB", "account_authorization", "aa1", "2026-09-04", 5})
	decisions, err = fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "ga1", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "aa1"},
	}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDs boundary: %v", err)
	}
	if decisions[0].Allowed {
		t.Fatalf("account limit reached must deny: %+v", decisions[0])
	}
}

func TestCheckAuthorizationQuotaMissingRowsAllow(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	decisions, err := fixture.service.CheckAuthorizationQuotaBatchByIDs(context.Background(), "ghost", []AccountRef{
		{AccountID: "u1", AccountAuthorizationID: "phantom"},
	}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDs: %v", err)
	}
	if !decisions[0].Allowed || decisions[0].Message != "" {
		t.Fatalf("missing authorization rows must allow: %+v", decisions[0])
	}
}

func TestTeamGrantQuota(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	// The authorization itself has no limits; the team grant does.
	seedAuthzRow(t, fixture.business, "ga1", "sysA", "sysB", "group", "g1", "team1", "", "active")
	seedGrantRow(t, fixture.business, "grant1", "group", "g1", "team1", "sysA", `{"daily":{"enabled":true,"limit":3}}`)
	// Team scope stats are recorded as resource_id:teamId on the owner.
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization_team", "g1:team1", "2026-09-04", 5})

	decisions, err := fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "ga1", []AccountRef{{AccountID: "u1"}}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDs: %v", err)
	}
	if decisions[0].Allowed || decisions[0].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("team grant limit must deny: %+v", decisions[0])
	}
}

func TestTeamGrantAccountScopeUsesInstanceAccount(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	seedAuthzRow(t, fixture.business, "aa1", "sysA", "sysB", "account", "acc1", "team1", "", "active")
	seedInstanceAccount(t, fixture.business, "inst1", "sysB", "aa1", "acc1")
	seedGrantRow(t, fixture.business, "grant1", "account", "acc1", "team1", "sysA", `{"daily":{"enabled":true,"limit":2}}`)
	// Account-scope team stats key on the instance account id and fall back to
	// the grant owner when the authorization grantee is absent from the row.
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysB", "account_authorization_team", "inst1:team1", "2026-09-04", 2})

	decisions, err := fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "", []AccountRef{{AccountID: "u1", AccountAuthorizationID: "aa1"}}, fixture.clock.Now())
	if err != nil {
		t.Fatalf("BatchByIDs: %v", err)
	}
	if decisions[0].Allowed {
		t.Fatalf("account team grant limit reached must deny: %+v", decisions[0])
	}
}

func TestCheckAuthorizationQuotaRuntimeCache(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	seedAuthzRow(t, fixture.business, "ga1", "sysA", "sysB", "group", "g1", "", `{"daily":{"enabled":true,"limit":10}}`, "active")
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysA", "group_authorization", "ga1", "2026-09-04", 1})

	first, err := fixture.service.CheckAuthorizationQuotaByIDs(ctx, "ga1", "", fixture.clock.Now())
	if err != nil || !first.Allowed {
		t.Fatalf("first check: (%+v, %v)", first, err)
	}
	// Exceed the scope; the 5s runtime cache must keep allowing.
	if _, err := fixture.statsDB.Exec(`UPDATE juhe_stats.usage_stats_daily SET success_cost_usd = 50 WHERE scope_id = 'ga1'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	second, err := fixture.service.CheckAuthorizationQuotaByIDs(ctx, "ga1", "", fixture.clock.Now())
	if err != nil || !second.Allowed {
		t.Fatalf("cached decision must persist: (%+v, %v)", second, err)
	}
	// After the TTL the fresh load denies.
	fixture.clock.Advance(6 * time.Second)
	third, err := fixture.service.CheckAuthorizationQuotaByIDs(ctx, "ga1", "", fixture.clock.Now())
	if err != nil || third.Allowed || third.Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("TTL expiry must reload: (%+v, %v)", third, err)
	}
	// Cache invalidation clears everything.
	fixture.service.ClearCache(ctx)
	fixture.clock.Advance(-24 * time.Hour) // back to the cheap window
	fourth, err := fixture.service.CheckAuthorizationQuotaByIDs(ctx, "ga1", "", fixture.clock.Now())
	if err != nil || !fourth.Allowed {
		t.Fatalf("cleared cache must reload cheap window: (%+v, %v)", fourth, err)
	}
}

func TestCheckAuthorizationQuotaBatchAsyncWorkerRole(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	seedAuthzRow(t, fixture.business, "aa1", "sysA", "sysB", "account", "acc1", "", `{"daily":{"enabled":true,"limit":5}}`, "active")
	seedCost(t, fixture.statsDB, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "success_cost_usd"},
		[]any{"sysB", "account_authorization", "aa1", "2026-09-04", 100})

	output, err := fixture.service.CheckAuthorizationQuotaBatchAsync(ctx, GroupAccessMetadata{}, []AccountAuthorizationSummary{
		{ID: "u1", AccountAuthorizationID: "aa1"},
		{ID: "u2"},
		{ID: "u3", AccountAuthorizationID: "aa1"},
	})
	if err != nil {
		t.Fatalf("BatchAsync: %v", err)
	}
	if !output["u2"].Allowed || output["u2"].Message != "" {
		t.Fatalf("account without authz must allow: %+v", output["u2"])
	}
	if output["u1"].Allowed || output["u1"].Message != AuthorizationQuotaExceededMessage {
		t.Fatalf("exceeded account must deny: %+v", output["u1"])
	}
	if output["u3"].Allowed {
		t.Fatalf("deduped scope must still deny: %+v", output["u3"])
	}
}

// TestCheckAuthorizationQuotaInvalidLimitsJSONFailsClosed pins the Node
// fail-closed semantics of authorization-quota.service.ts
// (authorizationQuotaCostChecksForAuthorizationRow /
// authorizationQuotaCostChecksForTeamRow): parseRequestQuotaLimitsJson throws
// on a malformed limits_json and the error propagates, so a parse failure must
// return an error instead of being treated as "no limits" (allow).
func TestCheckAuthorizationQuotaInvalidLimitsJSONFailsClosed(t *testing.T) {
	fixture := newAuthzFixture(t, Modes{}, newFakeClock(time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)))
	ctx := context.Background()
	seedAuthzRow(t, fixture.business, "ga-bad", "sysA", "sysB", "group", "g1", "", `{invalid`, "active")

	decisions, err := fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "ga-bad", []AccountRef{{AccountID: "u1"}}, fixture.clock.Now())
	if err == nil {
		t.Fatalf("invalid authorization limits_json must error, got decisions %+v", decisions)
	}

	// Team grant rows fail the same way (the authorization itself unlimited).
	seedAuthzRow(t, fixture.business, "ga-team", "sysA", "sysB", "group", "g2", "team1", "", "active")
	seedGrantRow(t, fixture.business, "grant-bad", "group", "g2", "team1", "sysA", `{invalid`)

	decisions, err = fixture.service.CheckAuthorizationQuotaBatchByIDs(ctx, "ga-team", []AccountRef{{AccountID: "u1"}}, fixture.clock.Now())
	if err == nil {
		t.Fatalf("invalid team grant limits_json must error, got decisions %+v", decisions)
	}
}
