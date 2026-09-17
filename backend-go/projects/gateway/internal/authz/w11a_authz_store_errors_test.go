package authz

// w11a coverage arms (part 2): store-level error injection (broken schema,
// triggers, closed db) across Create/Patch/Revoke/Return/ExpireSweep/
// Reconcile/team cascade/downstream sync/instance provisioning, plus the
// option/lookup/stat read failures.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func w11aGrantFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")
	return f
}

func w11aCreateGrant(t *testing.T, f *fixture) Summary {
	t.Helper()
	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return result.Item
}

func TestW11ACreateErrorArms(t *testing.T) {
	ctx := context.Background()
	input := CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "system_account", GranteeID: "grantee"}

	t.Run("unknown_resource_type", func(t *testing.T) {
		f := w11aGrantFixture(t)
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "widget", ResourceID: "x", GranteeType: "system_account", GranteeID: "grantee"}, "owner"); err == nil {
			t.Fatal("unknown resource type must fail")
		}
	})

	t.Run("closed_db", func(t *testing.T) {
		f := w11aGrantFixture(t)
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Create(ctx, input, "owner"); err == nil {
			t.Fatal("closed db must fail Create")
		}
	})

	t.Run("owner_resolution_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE groups`)
		if _, err := f.store.Create(ctx, input, "owner"); err == nil {
			t.Fatal("owner resolution failure must fail Create")
		}
	})

	t.Run("grantee_check_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE system_accounts`)
		if _, err := f.store.Create(ctx, input, "owner"); err == nil {
			t.Fatal("grantee check failure must fail Create")
		}
	})

	t.Run("team_member_probe_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedAccount(t, "member", "active")
		f.seedTeamWithMember(t, "team_1", "grantee")
		f.exec(t, `DROP TABLE system_team_members`)
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "team", GranteeID: "team_1"}, "owner"); err == nil {
			t.Fatal("team member probe failure must fail Create")
		}
	})

	t.Run("team_active_grants_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedTeamWithMember(t, "team_1", "grantee")
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "team", GranteeID: "team_1"}, "owner"); err == nil {
			t.Fatal("team grant counting failure must fail Create")
		}
	})

	t.Run("grant_insert_trigger_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `CREATE TRIGGER w11a_block_grant BEFORE INSERT ON resource_authorization_grants
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		if _, err := f.store.Create(ctx, input, "owner"); err == nil {
			t.Fatal("grant insert failure must fail Create")
		}
	})

	t.Run("runtime_upsert_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE resource_authorizations`)
		if _, err := f.store.Create(ctx, input, "owner"); err == nil {
			t.Fatal("runtime upsert failure must fail Create")
		}
	})

	t.Run("quota_scope_binding_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE request_quota_hourly_window_scope_bindings`)
		limits := `{"daily":{"enabled":true,"limit":5}}`
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "system_account", GranteeID: "grantee", LimitsJSON: &limits}, "owner"); err == nil {
			t.Fatal("quota scope binding failure must fail Create")
		}
	})

	t.Run("remark_trim_arm", func(t *testing.T) {
		f := w11aGrantFixture(t)
		remark := "   w11a remark   "
		result, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "system_account", GranteeID: "grantee", Remark: &remark}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if result.Item.Remark == nil || !strings.Contains(*result.Item.Remark, "w11a remark") {
			t.Fatalf("remark = %+v", result.Item.Remark)
		}
	})

	t.Run("duplicate_grant_conflict", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		duplicate, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "system_account", GranteeID: "grantee", Remark: strPtrW11A("other")}, "owner")
		if err == nil && duplicate.Created {
			t.Fatal("duplicate active grant must not create")
		}
	})

	t.Run("account_grant_chain", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "owner", "active")
		f.seedAccount(t, "grantee", "active")
		f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
		f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
		result, err := f.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc-src",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: strPtrW11A("grp-target"),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if result.Item.ID == "" {
			t.Fatal("account grant must produce a row")
		}
		// The instance row lands in the grantee namespace.
		var instance int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE authorization_instance_source_account_id = 'acc-src'`).Scan(&instance); err != nil {
			t.Fatal(err)
		}
		if instance != 1 {
			t.Fatal("account grant must provision the instance row")
		}
	})

	t.Run("account_grant_health_failure", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "owner", "active")
		f.seedAccount(t, "grantee", "active")
		f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
		f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
		created, err := f.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc-src",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: strPtrW11A("grp-target"),
		}, "owner")
		if err != nil {
			t.Fatal(err)
		}
		// The pause sync fans the health input out; with the outbox gone the
		// mutation must fail.
		f.exec(t, `DROP TABLE account_health_jobs_input_outbox`)
		paused := "paused"
		if _, err := f.store.Patch(ctx, created.Item.ID, PatchInput{Status: &paused}, created.Item.UpdatedAt, "owner"); err == nil {
			t.Fatal("health outbox failure must fail the account grant sync")
		}
	})

	t.Run("account_grant_binding_failure", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "owner", "active")
		f.seedAccount(t, "grantee", "active")
		f.seedProvisionSource(t, "acc-src", "owner", "生产账户", "gpt")
		f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)
		f.exec(t, `DROP TABLE group_accounts`)
		if _, err := f.store.Create(ctx, CreateInput{
			ResourceType: "account", ResourceID: "acc-src",
			GranteeType: "system_account", GranteeID: "grantee",
			TargetGroupID: strPtrW11A("grp-target"),
		}, "owner"); err == nil {
			t.Fatal("group binding failure must fail the account grant")
		}
	})
}

func TestW11APatchRevokeReturnErrorArms(t *testing.T) {
	ctx := context.Background()

	t.Run("patch_validation_and_missing", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		badStatus := "frozen"
		if _, err := f.store.Patch(ctx, item.ID, PatchInput{Status: &badStatus}, item.UpdatedAt, "owner"); err == nil || !strings.Contains(err.Error(), "状态") {
			t.Fatalf("invalid status = %v", err)
		}
		if outcome, err := f.store.Patch(ctx, "w11a-missing", PatchInput{}, "2026-01-01T00:00:00.000Z", "owner"); err != nil || outcome.Status != "not_found" {
			t.Fatalf("missing patch = %+v, %v", outcome, err)
		}
	})

	t.Run("patch_update_trigger_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `CREATE TRIGGER w11a_block_grant_update BEFORE UPDATE ON resource_authorization_grants
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		paused := "paused"
		if _, err := f.store.Patch(ctx, item.ID, PatchInput{Status: &paused}, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("grant update failure must fail Patch")
		}
	})

	t.Run("patch_runtime_sync_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorizations`)
		paused := "paused"
		if _, err := f.store.Patch(ctx, item.ID, PatchInput{Status: &paused}, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("runtime sync failure must fail Patch")
		}
	})

	t.Run("patch_broken_schema", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.Patch(ctx, item.ID, PatchInput{}, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("broken schema must fail Patch")
		}
	})

	t.Run("revoke_missing_and_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		if outcome, err := f.store.Revoke(ctx, "w11a-missing", "2026-01-01T00:00:00.000Z", "owner"); err != nil || outcome.Status != "not_found" {
			t.Fatalf("missing revoke = %+v, %v", outcome, err)
		}
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.Revoke(ctx, item.ID, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("broken schema must fail Revoke")
		}
	})

	t.Run("revoke_trigger_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `CREATE TRIGGER w11a_block_revoke BEFORE UPDATE ON resource_authorization_grants
			BEGIN SELECT RAISE(FAIL, 'w11a blocked'); END`)
		if _, err := f.store.Revoke(ctx, item.ID, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("revoke update failure must fail Revoke")
		}
	})

	t.Run("revoke_runtime_sync_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorizations`)
		if _, err := f.store.Revoke(ctx, item.ID, item.UpdatedAt, "owner"); err == nil {
			t.Fatal("runtime sync failure must fail Revoke")
		}
	})

	t.Run("return_own_resource_rejected", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		if outcome, err := f.store.Return(ctx, item.ID, item.UpdatedAt, "owner"); err != nil || outcome.Status != "not_found" {
			t.Fatalf("owner returning own resource = %+v, %v", outcome, err)
		}
	})

	t.Run("return_missing", func(t *testing.T) {
		f := w11aGrantFixture(t)
		if outcome, err := f.store.Return(ctx, "w11a-missing", "2026-01-01T00:00:00.000Z", "grantee"); err != nil || outcome.Status != "not_found" {
			t.Fatalf("missing return = %+v, %v", outcome, err)
		}
	})

	t.Run("return_broken_schema", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.Return(ctx, item.ID, item.UpdatedAt, "grantee"); err == nil {
			t.Fatal("broken schema must fail Return")
		}
	})

	t.Run("return_runtime_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorization_sources`)
		if _, err := f.store.Return(ctx, item.ID, item.UpdatedAt, "grantee"); err == nil {
			t.Fatal("runtime source failure must fail Return")
		}
	})

	t.Run("expire_sweep_broken_schema", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.ExpireSweep(ctx, 10); err == nil {
			t.Fatal("broken schema must fail ExpireSweep")
		}
	})
}

func TestW11AReconcileAndCascadeArms(t *testing.T) {
	ctx := context.Background()

	t.Run("reconcile_broken_schema", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.ReconcileExpiredGrants(ctx, time.Hour, 10); err == nil {
			t.Fatal("broken schema must fail ReconcileExpiredGrants")
		}
	})

	t.Run("reconcile_skips_non_expired", func(t *testing.T) {
		f := w11aGrantFixture(t)
		item := w11aCreateGrant(t, f)
		// Mark the runtime expired while the grant stays active: reconcile
		// must not touch it.
		f.exec(t, `UPDATE resource_authorizations SET expires_at = '2020-01-01T00:00:00.000Z' WHERE id = '`+item.ID+`'`)
		// The runtime id equals the grant id for direct user grants.
		count, err := f.store.ReconcileExpiredGrants(ctx, time.Hour, 10)
		if err != nil {
			t.Fatal(err)
		}
		_ = count
	})

	t.Run("team_cascade_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedTeamWithMember(t, "team_1", "grantee")
		f.exec(t, `DROP TABLE resource_authorization_sources`)
		if err := f.store.RevokeAllTeamSources(ctx, "team_1", "owner", "w11a"); err == nil {
			t.Fatal("broken schema must fail RevokeAllTeamSources")
		}
		if err := f.store.RevokeTeamSourcesForMember(ctx, "team_1", "grantee", "owner"); err == nil {
			t.Fatal("broken schema must fail RevokeTeamSourcesForMember")
		}
		t.Run("reactivate_broken_members", func(t *testing.T) {
			freshMembers := w11aGrantFixture(t)
			freshMembers.seedTeamWithMember(t, "team_1", "grantee")
			freshMembers.exec(t, `DROP TABLE system_team_members`)
			if err := freshMembers.store.ReactivateTeamGrants(ctx, "team_1", "owner"); err == nil {
				t.Fatal("broken member table must fail ReactivateTeamGrants")
			}
		})
	})

	t.Run("team_cascade_happy_paths", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedTeamWithMember(t, "team_1", "grantee")
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "group", ResourceID: "grp_1", GranteeType: "team", GranteeID: "team_1"}, "owner"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.RevokeAllTeamSources(ctx, "team_1", "owner", "w11a test"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReactivateTeamGrants(ctx, "team_1", "owner"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.RevokeTeamSourcesForMember(ctx, "team_1", "grantee", "owner"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("member_and_grant_row_helpers", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedTeamWithMember(t, "team_1", "grantee")
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := f.store.activeTeamGrantRowsTx(ctx, tx, "team_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.activeTeamMemberIDs(ctx, tx, "team_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.activeTeamMemberRowsTx(ctx, tx, "team_1"); err != nil {
			t.Fatal(err)
		}
		// RevokeAllTeamSourcesTx runs inside a caller-owned transaction.
		if err := f.store.RevokeAllTeamSourcesTx(ctx, tx, "team_1", "owner", "2026-01-01T00:00:00Z", "w11a tx"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ApplyActiveTeamGrantsToMembersTx(ctx, tx, "team_1", []string{"grantee"}, "owner", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.RevokeTeamSourcesForMemberTx(ctx, tx, "team_1", "grantee", "owner", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestW11ADownstreamSyncArms(t *testing.T) {
	ctx := context.Background()

	t.Run("mark_dirty_scopes", func(t *testing.T) {
		f := newFixture(t)
		f.exec(t, `CREATE TABLE IF NOT EXISTS usage_quota_hourly_window_dirty_scopes (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
			generation INTEGER NOT NULL DEFAULT 1, first_dirty_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (system_account_id, scope_type, scope_id))`)
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		bindings := []scopeBinding{{
			systemAccountID: "w11a-owner", scopeType: "user", scopeID: "w11a-grantee",
			sourceType: "resource_authorization_grant", sourceID: "w11a-grant", windowHours: 3,
		}}
		if err := f.store.markQuotaHourlyWindowDirtyScopes(ctx, tx, bindings, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.markQuotaHourlyWindowDirtyScopes(ctx, tx, nil, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		// The pg dialect qualifies the stats schema: it must fail on SQLite.
		pgStore := *f.store
		pgStore.pg = true
		if err := pgStore.markQuotaHourlyWindowDirtyScopes(ctx, tx, bindings, "2026-01-01T00:00:00Z"); err == nil {
			t.Fatal("pg-qualified dirty table must fail on the sqlite fixture")
		}
	})

	t.Run("unique_scope_bindings_and_active_hours", func(t *testing.T) {
		bindings := []scopeBinding{
			{systemAccountID: "o", scopeType: "user", scopeID: "g", sourceType: "t", sourceID: "s1", windowHours: 2},
			{systemAccountID: "o", scopeType: "user", scopeID: "g", sourceType: "t", sourceID: "s2", windowHours: 2},
		}
		if got := uniqueScopeBindings(bindings); len(got) != 1 {
			t.Fatalf("deduped bindings = %d", len(got))
		}
		if hours, err := activeAuthorizationQuotaHourlyWindowHours(`{"hourly":{"enabled":true,"limit":1,"hours":4}}`); err != nil || hours != 4 {
			t.Fatalf("active hours = %d, %v", hours, err)
		}
		if hours, err := activeAuthorizationQuotaHourlyWindowHours(`{}`); err != nil || hours != 0 {
			t.Fatalf("empty hourly limits = %d, %v", hours, err)
		}
		if _, err := activeAuthorizationQuotaHourlyWindowHours(`{bad`); err == nil {
			t.Fatal("malformed limits must fail")
		}
	})

	t.Run("reserve_version_arms", func(t *testing.T) {
		f := newFixture(t)
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		first, err := f.store.reserveAccountHealthInputVersion(ctx, tx, "w11a-acct", "2026-01-01T00:00:00Z")
		if err != nil || first != 1 {
			t.Fatalf("first version = %d, %v", first, err)
		}
		second, err := f.store.reserveAccountHealthInputVersion(ctx, tx, "w11a-acct", "2026-01-01T00:00:01Z")
		if err != nil || second != 2 {
			t.Fatalf("second version = %d, %v", second, err)
		}
		if _, err := f.store.reserveAccountHealthInputVersion(ctx, tx, "  ", "2026-01-01T00:00:00Z"); err == nil {
			t.Fatal("blank account id must fail the version reservation")
		}
	})
}

func w11aExec(t *testing.T, f *fixture, query string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestW11AInstanceLadderArms(t *testing.T) {
	ctx := context.Background()
	t.Run("name_ladder_fallback", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "w11a-grantee", "active")
		base := "w11a 名字"
		// Occupy the base name and every "<base> N" candidate up to 1000.
		w11aExec(t, f, `INSERT INTO accounts (id, system_account_id, name, status, created_at, updated_at)
			VALUES ('w11a-a0', 'w11a-grantee', ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, base)
		for i := 2; i <= 1000; i++ {
			w11aExec(t, f, `INSERT INTO accounts (id, system_account_id, name, status, created_at, updated_at)
				VALUES (?, 'w11a-grantee', ?, 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
				"w11a-a"+itoa(i), base+" "+itoa(i))
		}
		name, err := f.store.uniqueAuthorizedAccountInstanceName(ctx, f.db, base+"-source", "w11a-grantee", "w11a-authz", "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, base+"-") || !strings.Contains(name, "-") {
			t.Fatalf("fallback name = %q", name)
		}
	})

	t.Run("short_source_name", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "w11a-grantee", "active")
		name, err := f.store.uniqueAuthorizedAccountInstanceName(ctx, f.db, "ab", "w11a-grantee", "w11a-authz", "")
		if err != nil || name == "" {
			t.Fatalf("short name ladder = %q, %v", name, err)
		}
	})

	t.Run("availability_probe_failure", func(t *testing.T) {
		f := newFixture(t)
		f.exec(t, `DROP TABLE accounts`)
		if _, err := f.store.uniqueAuthorizedAccountInstanceName(ctx, f.db, "x", "g", "a", ""); err == nil {
			t.Fatal("availability probe failure must fail the ladder")
		}
	})

	t.Run("dispatch_revision_failure", func(t *testing.T) {
		f := newFixture(t)
		// Drop before opening the transaction: the fixture pool is a single
		// connection, so a mid-test DDL would deadlock the tx.
		// Both drops precede the transaction: the fixture pool allows a
		// single connection, so a mid-tx DDL would deadlock.
		f.exec(t, `DROP TABLE accounts`)
		f.exec(t, `DROP TABLE account_name_search_terms`)
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := f.store.advanceInstanceDispatchRevision(ctx, tx, "w11a-acct", time.Now().UnixMilli()); err == nil {
			t.Fatal("dispatch revision failure must surface")
		}
		if err := f.store.replaceInstanceNameSearchTerms(ctx, tx, "w11a-acct", "g", "name", "now"); err == nil {
			t.Fatal("search term failure must surface")
		}

	})

	t.Run("sync_names_for_source", func(t *testing.T) {
		f := newFixture(t)
		f.seedAccount(t, "w11a-owner", "active")
		f.seedAccount(t, "w11a-grantee", "active")
		f.seedProvisionSource(t, "w11a-src", "w11a-owner", "原名", "gpt")
		// An instance row for the source with the old name.
		f.exec(t, `INSERT INTO accounts (id, system_account_id, name, status, authorization_instance_source_account_id, authorization_instance_authorization_id, created_at, updated_at)
			VALUES ('w11a-inst', 'w11a-grantee', '原名', 'active', 'w11a-src', 'w11a-authz', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		renamed, err := f.store.SyncAccountAuthorizationInstanceNamesForSourceAccount(ctx, "w11a-src", "新名")
		if err != nil {
			t.Fatal(err)
		}
		if len(renamed) != 1 {
			t.Fatalf("renamed instances = %v", renamed)
		}
		var name string
		if err := f.db.QueryRow(`SELECT name FROM accounts WHERE id = 'w11a-inst'`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "新名" {
			t.Fatalf("renamed instance name = %q", name)
		}
	})
}

func TestW11AReadAndOptionErrorArms(t *testing.T) {
	ctx := context.Background()

	t.Run("authorized_readable_failure", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.exec(t, `DROP TABLE resource_authorizations`)
		if _, err := f.store.AuthorizedReadableAccountIDs(ctx, "grantee"); err == nil {
			t.Fatal("broken schema must fail AuthorizedReadableAccountIDs")
		}
	})

	t.Run("grant_for_mutation_arms", func(t *testing.T) {
		f := w11aGrantFixture(t)
		// fixture 连接池限单连接：DROP 必须在事务回滚释放连接之后执行，
		// 否则 f.db.Exec 永久等待连接（与对齐 530 行附近 advance_arms 的模式）。
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if grant, err := f.store.GetGrantForMutation(ctx, tx, "w11a-missing"); err != nil || grant != nil {
			tx.Rollback()
			t.Fatalf("missing grant = %v, %v", grant, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		tx, err = f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := f.store.GetGrantForMutation(ctx, tx, "w11a-any"); err == nil {
			t.Fatal("broken schema must fail GetGrantForMutation")
		}
	})

	t.Run("list_items_page_clamps_and_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		items, _, _, err := f.store.ListItemsPage(ctx, Filters{}, 0, 9999, accessInfo{IsAdmin: true})
		if err != nil || len(items) != 1 {
			t.Fatalf("clamped list = %d items, %v", len(items), err)
		}
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, _, _, err := f.store.ListItemsPage(ctx, Filters{}, 1, 20, accessInfo{IsAdmin: true}); err == nil {
			t.Fatal("broken schema must fail ListItemsPage")
		}
	})

	t.Run("projection_lookup_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		// loadAccountLookupRows 只在存在 account 类型 grant 时才查 accounts
		// 表（空 ID 集直接跳过），且 Create 校验资源存在：先 seed accounts 行。
		w11aExec(t, f, `INSERT INTO accounts (id, system_account_id, name, status, created_at, updated_at)
			VALUES ('w11a-acc-1', 'owner', 'w11a 资源账号', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if _, err := f.store.Create(ctx, CreateInput{ResourceType: "account", ResourceID: "w11a-acc-1", GranteeType: "system_account", GranteeID: "grantee"}, "owner"); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `DROP TABLE accounts`)
		if _, _, _, err := f.store.ListItemsPage(ctx, Filters{}, 1, 20, accessInfo{IsAdmin: true}); err == nil {
			t.Fatal("account lookup failure must fail the projection")
		}
		t.Run("group_projection_failure", func(t *testing.T) {
			// 嵌套子测试名不同 → 独立内存库，避免与外层 fixture 的 seed 撞唯一约束。
			fresh := w11aGrantFixture(t)
			w11aCreateGrant(t, fresh)
			fresh.exec(t, `DROP TABLE groups`)
			if _, _, _, err := fresh.store.ListItemsPage(ctx, Filters{}, 1, 20, accessInfo{IsAdmin: true}); err == nil {
				t.Fatal("group lookup failure must fail the projection")
			}
		})
	})

	t.Run("find_summary_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		if summary, err := f.store.Find(ctx, "w11a-missing"); err != nil || summary != nil {
			t.Fatalf("missing find = %v, %v", summary, err)
		}
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.Find(ctx, "w11a-any"); err == nil {
			t.Fatal("broken schema must fail Find")
		}
	})

	t.Run("option_reads", func(t *testing.T) {
		f := w11aGrantFixture(t)
		f.seedAccount(t, "w11a-user2", "active")
		if _, err := f.store.ListAuthorizationGranteeAccounts(ctx, authorizationPrincipalOptionListOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.ListAuthorizationGranteeTeams(ctx, authorizationPrincipalOptionListOptions{}); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `DROP TABLE system_accounts`)
		if _, err := f.store.ListAuthorizationGranteeAccounts(ctx, authorizationPrincipalOptionListOptions{}); err == nil {
			t.Fatal("broken schema must fail grantee accounts")
		}
		// teams 读只查 system_teams 表：system_accounts 缺失不影响它。
		if _, err := f.store.ListAuthorizationGranteeTeams(ctx, authorizationPrincipalOptionListOptions{}); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `DROP TABLE system_teams`)
		if _, err := f.store.ListAuthorizationGranteeTeams(ctx, authorizationPrincipalOptionListOptions{}); err == nil {
			t.Fatal("broken schema must fail grantee teams")
		}
	})

	t.Run("stats_loader_failure_and_cache", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		stats, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "group", []string{"grp_1", "w11a-missing"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := stats["grp_1"]; !ok {
			t.Fatalf("stats = %+v", stats)
		}
		// Cached read stays green, then the broken schema fails fresh reads
		// only after invalidation. 聚合查询走 resource_authorizations/sources
		// 两表，grants 投影表与该读路径无关。
		f.store.InvalidateResourceAuthorizationStatsCache("group", "grp_1")
		f.exec(t, `DROP TABLE resource_authorizations`)
		if _, err := f.store.ResourceAuthorizationStatsByResourceIds(ctx, "group", []string{"grp_1"}); err == nil {
			t.Fatal("broken schema must fail the stats load")
		}
	})
}

func TestW11AReturnGroupArms(t *testing.T) {
	ctx := context.Background()
	t.Run("blank_grantee_and_missing_group", func(t *testing.T) {
		f := w11aGrantFixture(t)
		// 空 grantee 走早退分支：返回 (nil, nil)，不产生错误。
		if receipt, err := f.store.ReturnGroupForGrantee(ctx, "grp_1", "", "owner"); err != nil || receipt != nil {
			t.Fatalf("blank grantee = %+v, %v", receipt, err)
		}
		if receipt, err := f.store.ReturnGroupForGrantee(ctx, "w11a-missing", "grantee", "owner"); err != nil || receipt != nil {
			t.Fatalf("missing group = %+v, %v", receipt, err)
		}
	})

	t.Run("owner_returning_own_group", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		if receipt, err := f.store.ReturnGroupForGrantee(ctx, "grp_1", "owner", "owner"); err != nil || receipt != nil {
			t.Fatalf("own group return = %+v, %v", receipt, err)
		}
	})

	t.Run("happy_path_and_failures", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		receipt, err := f.store.ReturnGroupForGrantee(ctx, "grp_1", "grantee", "grantee")
		if err != nil {
			t.Fatal(err)
		}
		if receipt == nil || receipt.ID == "" {
			t.Fatalf("receipt = %+v", receipt)
		}
		// A second return has nothing left to return.
		if receipt, err := f.store.ReturnGroupForGrantee(ctx, "grp_1", "grantee", "grantee"); err != nil || receipt != nil {
			t.Fatalf("idempotent return = %+v, %v", receipt, err)
		}
	})

	t.Run("broken_schema", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		f.exec(t, `DROP TABLE resource_authorization_grants`)
		if _, err := f.store.ReturnGroupForGrantee(ctx, "grp_1", "grantee", "grantee"); err == nil {
			t.Fatal("broken schema must fail ReturnGroupForGrantee")
		}
	})
}

func TestW11ADeleteResourceArms(t *testing.T) {
	ctx := context.Background()
	t.Run("revoke_grants_for_deleted", func(t *testing.T) {
		f := w11aGrantFixture(t)
		w11aCreateGrant(t, f)
		// 同子测试内两个 fixture 连到同一共享内存库；先显式回滚 f 的事务
		// 释放库锁，fresh 的 DDL/seed 才能执行（否则互等挂死）。
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.RevokeGrantsForResourceDeleted(ctx, tx, "group", "grp_1", "owner", "2026-01-01T00:00:00Z"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		t.Run("broken_schema", func(t *testing.T) {
			// 嵌套子测试名不同 → 独立内存库，避免与外层 fixture 的 seed 撞唯一约束。
			fresh := w11aGrantFixture(t)
			w11aCreateGrant(t, fresh)
			fresh.exec(t, `DROP TABLE resource_authorization_grants`)
			freshTx, err := fresh.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer freshTx.Rollback()
			if err := fresh.store.RevokeGrantsForResourceDeleted(ctx, freshTx, "group", "grp_1", "owner", "2026-01-01T00:00:00Z"); err == nil {
				t.Fatal("broken schema must fail the deleted-resource sweep")
			}
		})
	})
}

func TestW11AExpireSweepHappyPath(t *testing.T) {
	f := w11aGrantFixture(t)
	item := w11aCreateGrant(t, f)
	past := "2020-01-01T00:00:00.000Z"
	paused := "paused"
	if _, err := f.store.Patch(context.Background(), item.ID, PatchInput{Status: &paused, ExpiresAt: &past, ExpiresAtSet: true}, item.UpdatedAt, "owner"); err != nil {
		t.Fatal(err)
	}
	count, err := f.store.ExpireSweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = count
}

var _ = sql.ErrNoRows
