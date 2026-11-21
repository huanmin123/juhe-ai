package authz

import (
	"context"
	"testing"
	"time"
)

// simulateJobsExpirySweepFlip replays exactly what the jobs
// resource-authorization-expiry-sweep writes when it flips one due grant
// (oauthrefresh.RunAuthorizationExpirySweep): the grant and every
// non-terminal source move to expired with the next expiry cycle. The runtime
// projection, notifications and quota bindings are NOT touched by jobs — those
// are the gateway reconcile's responsibility. The due precondition
// (expires_at <= now, NULL revoked_at) is planted first because the sweep only
// selects grants with a past expiry.
func simulateJobsExpirySweepFlip(t *testing.T, f *fixture, grantID string) {
	t.Helper()
	nowText := f.now.UTC().Format("2006-01-02T15:04:05.000Z")
	past := f.now.UTC().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET expires_at = ?, revoked_at = NULL WHERE id = ?`), past, grantID); err != nil {
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
			expired_from_status = COALESCE(NULLIF(expired_from_status, ''), status),
			expiry_cycle = ?,
			ended_at = COALESCE(ended_at, ?),
			ended_reason = COALESCE(NULLIF(ended_reason, ''), 'offer_expired'),
			pending_expires_at = NULL,
			revoked_at = COALESCE(revoked_at, ?),
			updated_at = ?
		WHERE id = ? AND status IN ('active', 'paused')`), nextCycle, nowText, nowText, nowText, grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_sources
		SET status = 'expired', expired_from_status = status, expiry_cycle = ?,
			ended_at = COALESCE(ended_at, ?), ended_reason = 'offer_expired',
			pending_expires_at = NULL, updated_at = ?
		WHERE grant_id = ? AND status IN ('pending','active','dormant','needs_review')`),
		nextCycle, nowText, nowText, grantID); err != nil {
		t.Fatal(err)
	}
}

func countExpiryNotifications(t *testing.T, f *fixture, grantID, granteeID string) int {
	t.Helper()
	var count int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM authorization_notifications
		WHERE grant_id = ? AND system_account_id = ? AND type = 'offer_expired'`), grantID, granteeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestReconcileExpiredGrantsResyncsRuntimeAndBindings pins the T6d gateway
// consumption face: after the jobs sweep flips a due grant, the gateway-side
// reconcile re-projects the stale-active runtime row to expired, drops the
// quota scope binding, and does NOT re-enqueue the health input fanout (jobs
// already wrote it). A second pass over the same window stays idempotent.
func TestReconcileExpiredGrantsResyncsRuntimeAndBindings(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "res-1", "owner", "reconcile expiry", "gpt")
	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeUsername: "grantee",
		LimitsJSON: hourlyLimits(6),
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", offer.SourceID); err != nil {
		t.Fatal(err)
	}
	grantID := offer.GrantID
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	if bindings := countGrantBindings(t, f, grantID); len(bindings) != 1 {
		t.Fatalf("active grant must render its binding: %+v", bindings)
	}

	// jobs sweep flips the grant; the runtime row and binding go stale.
	simulateJobsExpirySweepFlip(t, f, grantID)
	var runtimeStatus string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusActive {
		t.Fatalf("precondition: runtime row must still be stale-active, got %s", runtimeStatus)
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
	if bindings := countGrantBindings(t, f, grantID); len(bindings) != 0 {
		t.Fatalf("stale binding must be dropped: %+v", bindings)
	}
	if got := countExpiryNotifications(t, f, grantID, "grantee"); got != 1 {
		t.Fatalf("expiry notification count = %d, want 1", got)
	}
	var dedupeKey string
	if err := f.db.QueryRow(fixtureBind(`SELECT dedupe_key FROM authorization_notifications
		WHERE grant_id = ? AND system_account_id = ? AND type = 'offer_expired'`), grantID, "grantee").Scan(&dedupeKey); err != nil {
		t.Fatal(err)
	}
	if want := "expired:" + grantID + ":1:grantee"; dedupeKey != want {
		t.Fatalf("expiry dedupe key = %q, want %q (cycle must participate)", dedupeKey, want)
	}
	// jobs sweep 已经写入 fanout：重放不得重复入队（本测试没模拟 jobs 写
	// outbox，因此断言恒空——任何一行都是 reconcile 越权重放）。
	if rows := countOutboxRows(t, f); len(rows) != 0 {
		t.Fatalf("reconcile must not re-enqueue health inputs: %+v", rows)
	}

	// 幂等重放：窗口内的第二轮不报错、不新增副作用。
	again, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again != 1 {
		t.Fatalf("second pass reconciled = %d, want 1 (idempotent replay)", again)
	}
	if rows := countOutboxRows(t, f); len(rows) != 0 {
		t.Fatalf("second pass must not enqueue health inputs: %+v", rows)
	}
	if got := countExpiryNotifications(t, f, grantID, "grantee"); got != 1 {
		t.Fatalf("second pass duplicated expiry notification: %d", got)
	}
}

// TestReconcileExpiredGrantKeepsActiveTeamSource pins the multi-source rule:
// expiring one grant must re-project the runtime from all sources. A personal
// source can be dormant/expired while an accepted team source keeps the same
// runtime active; the legacy per-grant sync wrote the runtime straight to
// expired and also expired the team source.
func TestReconcileExpiredGrantKeepsActiveTeamSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedAccount(t, "approver", "active")
	f.seedGroup(t, "grp-multi", "owner")
	f.seedTeamWithMember(t, "team_multi", "grantee")
	if _, err := f.db.Exec(fixtureBind(`UPDATE system_teams SET created_by = 'approver' WHERE id = 'team_multi'`)); err != nil {
		t.Fatalf("set team approver: %v", err)
	}
	manualGrant, manualSource := f.createAcceptedDirectOffer(t, "group", "grp-multi", "grantee")

	teamOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp-multi",
		GranteeType: "team", TeamID: "team_multi",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create team offer: %v", err)
	}
	if err := f.store.TeamApprove(context.Background(), "team_multi", teamOffer.GrantID, "approver", false,
		f.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("team approve: %v", err)
	}
	var teamSourceID string
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorization_sources
		WHERE grant_id = ? AND source_type = 'team' AND status = 'pending'`), teamOffer.GrantID).Scan(&teamSourceID); err != nil {
		t.Fatalf("load team source: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", teamSourceID); err != nil {
		t.Fatalf("accept team source: %v", err)
	}
	runtimeID := ""
	if err := f.db.QueryRow(fixtureBind(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp-multi' AND grantee_system_account_id = 'grantee'`)).
		Scan(&runtimeID); err != nil {
		t.Fatalf("load group runtime: %v", err)
	}

	// The team source is effective; the accepted personal source became dormant.
	var status, effective string
	if err := f.db.QueryRow(fixtureBind(`SELECT status, COALESCE(effective_source_type,'')
		FROM resource_authorizations WHERE id = ?`), runtimeID).Scan(&status, &effective); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive || effective != "team" {
		t.Fatalf("precondition runtime = %s/%s, want active/team", status, effective)
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
	if got := countExpiryNotifications(t, f, manualGrant, "grantee"); got != 1 {
		t.Fatalf("personal expiry notification count = %d, want 1", got)
	}

	// Expiring the team grant finally terminates the runtime: bindings are
	// disabled and the second notification lands under the team grant's key.
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
	if got := countExpiryNotifications(t, f, teamOffer.GrantID, "grantee"); got != 1 {
		t.Fatalf("team expiry notification count = %d, want 1", got)
	}
}

// TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants keeps the scan
// narrow: grants outside the lookback window and grants still active are
// invisible to the reconcile pass.
func TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	seedGranteeDefaultGroup(t, f, "grantee", "unknown_provider")
	seedPhysicalAccount(t, f, "res-2", "owner")
	seedGranteeDefaultGroup(t, f, "grantee", "unknown_provider")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	createdLive, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-2",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	// 窗口外的旧 expired grant（jobs 已收敛，不再重放）。
	old := f.now.UTC().Add(-30 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET status = 'expired', updated_at = ? WHERE id = ?`), old, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	// 仍 active 的 due grant 不属于 reconcile 面（翻转归 jobs sweep）。
	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 10*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled != 0 {
		t.Fatalf("reconciled = %d, want 0", reconciled)
	}
	var status string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_grants WHERE id = ?`), createdLive.Item.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive {
		t.Fatalf("live grant must stay untouched, got %s", status)
	}
}
