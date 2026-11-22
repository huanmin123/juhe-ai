package authz

import (
	"context"
	"testing"
	"time"
)

// simulateJobsExpirySweepFlip replays exactly what the jobs
// resource-authorization-expiry-sweep writes when it flips one due grant
// (oauthrefresh.Store.RunAuthorizationExpirySweep): the grant and every
// non-terminal source move to expired with the next expiry cycle and the
// authorization_expired end reason. The runtime projection is NOT touched by
// jobs — that is the gateway reconcile's responsibility. The due precondition
// (expires_at <= now, status active/paused) is planted first because the sweep
// only selects grants with a past expiry.
func simulateJobsExpirySweepFlip(t *testing.T, f *fixture, grantID string) {
	t.Helper()
	nowText := f.now.UTC().Format("2006-01-02T15:04:05.000Z")
	past := f.now.UTC().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET expires_at = ? WHERE id = ?`), past, grantID); err != nil {
		t.Fatal(err)
	}
	var cycle int
	if err := f.db.QueryRow(fixtureBind(`SELECT COALESCE(expiry_cycle, 0)
		FROM resource_authorization_grants WHERE id = ?`), grantID).Scan(&cycle); err != nil {
		t.Fatal(err)
	}
	nextCycle := cycle + 1
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET status = 'expired',
			expiry_cycle = ?,
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(NULLIF(ended_reason, ''), 'authorization_expired'),
			revoked_at = COALESCE(revoked_at, ?),
			updated_at = ?
		WHERE id = ? AND status IN ('active', 'paused')`), nextCycle, nowText, nowText, nowText, grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_sources
		SET status = 'expired', expiry_cycle = ?,
			ended_at = COALESCE(ended_at, ?), ended_reason = 'authorization_expired',
			updated_at = ?
		WHERE grant_id = ? AND status IN ('pending','active')`),
		nextCycle, nowText, nowText, grantID); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileExpiredGrantsResyncsRuntime pins the T6d gateway consumption
// face: after the jobs sweep flips a due grant, the gateway-side reconcile
// re-projects the stale-active runtime row to expired. A second pass over the
// same window stays idempotent.
func TestReconcileExpiredGrantsResyncsRuntime(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "res-1", "owner", "reconcile expiry", "gpt")
	seedGranteeDefaultGroup(t, f, "grantee", "gpt")
	grantID := f.claimAccountOffer(t, "res-1", "grantee", "grp-default-grantee")
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	var runtimeStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive {
		t.Fatalf("precondition: claimed grant must be active, got %s", runtimeStatus)
	}

	// jobs sweep flips the grant; the runtime row goes stale.
	simulateJobsExpirySweepFlip(t, f, grantID)
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive {
		t.Fatalf("precondition: runtime row must still be stale-active, got %s", runtimeStatus)
	}
	var grantStatus, endedReason string
	if err := f.db.QueryRow(fixtureBind(`SELECT status, COALESCE(ended_reason,'') FROM resource_authorization_grants WHERE id = ?`), grantID).Scan(&grantStatus, &endedReason); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusExpired || endedReason != "authorization_expired" {
		t.Fatalf("sweep flip = %s/%s, want expired/authorization_expired", grantStatus, endedReason)
	}

	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 1 {
		t.Fatalf("reconciled = %d, want 1", reconciled)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusExpired {
		t.Fatalf("runtime row must be re-projected to expired, got %s", runtimeStatus)
	}
	// 终态投影必须停机实例行与分组绑定（disableRuntimeBindings）。
	var instanceStatus string
	var instanceSchedulable int
	if err := f.db.QueryRow(fixtureBind(`SELECT status, schedulable FROM accounts
		WHERE authorization_instance_authorization_id = ? AND deleted_at IS NULL`), runtimeID).Scan(&instanceStatus, &instanceSchedulable); err != nil {
		t.Fatal(err)
	}
	if instanceStatus != "disabled" || instanceSchedulable != 0 {
		t.Fatalf("terminal runtime must disable its instance: %s/%d", instanceStatus, instanceSchedulable)
	}
	var bindingCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM group_accounts WHERE account_authorization_id = ? AND enabled = 1`), runtimeID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 0 {
		t.Fatalf("terminal runtime must disable its group bindings: %d", bindingCount)
	}

	// 幂等重放：窗口内的第二轮不报错、不新增副作用。
	again, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again != 1 {
		t.Fatalf("second pass reconciled = %d, want 1 (idempotent replay)", again)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusExpired {
		t.Fatalf("second pass runtime = %s, want expired", runtimeStatus)
	}
}

// TestReconcileExpiredGrantKeepsActiveTeamSource pins the multi-source rule:
// expiring one grant must re-project the runtime from all of its sources. The
// personal source expires with its grant while the claimed team source keeps
// the same runtime active; the runtime only lands expired once every source
// is terminal.
func TestReconcileExpiredGrantKeepsActiveTeamSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp-multi", "owner")
	f.seedTeamWithMember(t, "team_multi", "grantee")
	manualGrant, manualSource := f.createAcceptedDirectOffer(t, "group", "grp-multi", "grantee")

	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp-multi",
		GranteeType: "team", TeamID: "team_multi",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create team offer: %v", err)
	}
	var teamSourceID string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorization_sources
		WHERE grant_id = ? AND source_type = 'team' AND status = 'pending'`), teamOffer.GrantID).Scan(&teamSourceID); err != nil {
		t.Fatalf("load team source: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", teamSourceID, ""); err != nil {
		t.Fatalf("accept team source: %v", err)
	}
	runtimeID := ""
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp-multi' AND grantee_system_account_id = 'grantee'`)).
		Scan(&runtimeID); err != nil {
		t.Fatalf("load group runtime: %v", err)
	}

	// The team source is effective (earliest activated manual source excepted
	// only if it activated first — the runtime must be active either way).
	var status, effective string
	if err := f.db.QueryRow(fixtureBind(`SELECT status, COALESCE(effective_source_type,'')
		FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&status, &effective); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive || effective == "" {
		t.Fatalf("precondition runtime = %s/%s, want active with a source", status, effective)
	}

	// jobs expires only the personal grant: the runtime must stay active via the
	// accepted team source, and the team source must stay active.
	simulateJobsExpirySweepFlip(t, f, manualGrant)
	if _, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT status, COALESCE(effective_source_type,'')
		FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&status, &effective); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive || effective != "team" {
		t.Fatalf("runtime after personal expiry = %s/%s, want active/team", status, effective)
	}
	var manualStatus, teamStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_sources WHERE id = ?`), manualSource).Scan(&manualStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_sources WHERE id = ?`), teamSourceID).Scan(&teamStatus); err != nil {
		t.Fatal(err)
	}
	if manualStatus != StatusExpired || teamStatus != StatusActive {
		t.Fatalf("source statuses = manual:%s team:%s, want expired/active", manualStatus, teamStatus)
	}

	// Expiring the team grant finally terminates the runtime.
	simulateJobsExpirySweepFlip(t, f, teamOffer.GrantID)
	if _, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusExpired {
		t.Fatalf("runtime after all sources expired = %s, want expired", status)
	}
}

// TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants keeps the scan
// narrow: grants outside the lookback window and grants still active are
// invisible to the reconcile pass.
func TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp-win-1", "owner")
	f.seedGroup(t, "grp-win-2", "owner")
	outsideGrant, _ := f.createAcceptedDirectOffer(t, "group", "grp-win-1", "grantee")
	liveGrant, _ := f.createAcceptedDirectOffer(t, "group", "grp-win-2", "grantee")
	// 窗口外的旧 expired grant（jobs 已收敛，不再重放）。
	old := f.now.UTC().Add(-30 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET status = 'expired', updated_at = ? WHERE id = ?`), old, outsideGrant); err != nil {
		t.Fatal(err)
	}
	// 仍 active 的 grant 不属于 reconcile 面（翻转归 jobs sweep）。
	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 10*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 0 {
		t.Fatalf("reconciled = %d, want 0", reconciled)
	}
	var status string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_grants WHERE id = ?`), liveGrant).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive {
		t.Fatalf("live grant must stay untouched, got %s", status)
	}
}
