package authz

import (
	"context"
	"testing"
	"time"
)

// simulateJobsExpirySweepFlip replays exactly what the jobs
// resource-authorization-expiry-sweep writes when it flips one due grant
// (oauthrefresh.RunAuthorizationExpirySweep): status/revoked_at/updated_at
// only — the runtime projection and quota bindings are NOT touched by jobs.
// The due precondition (expires_at <= now, NULL revoked_at) is planted first
// because the sweep only selects grants with a past expiry.
func simulateJobsExpirySweepFlip(t *testing.T, f *fixture, grantID string) {
	t.Helper()
	nowText := f.now.UTC().Format("2006-01-02T15:04:05.000Z")
	past := f.now.UTC().Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants
		SET expires_at = ?, revoked_at = NULL WHERE id = ?`, past, grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants
		SET status = 'expired', revoked_at = COALESCE(revoked_at, ?), updated_at = ?
		WHERE id = ? AND status IN ('active', 'paused')`, nowText, nowText, grantID); err != nil {
		t.Fatal(err)
	}
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
	seedPhysicalAccount(t, f, "res-1", "owner")
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "res-1",
		GranteeType: "system_account", GranteeID: "grantee",
		LimitsJSON: hourlyLimits(6),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-1", "grantee")
	seedDownstreamInstance(t, f, "inst-1", "res-1", "grantee", runtimeID, "openai", "oauth", "")
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 1 {
		t.Fatalf("active grant must render its binding: %+v", bindings)
	}

	// jobs sweep flips the grant; the runtime row and binding go stale.
	simulateJobsExpirySweepFlip(t, f, created.Item.ID)
	var runtimeStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = ?`, runtimeID).Scan(&runtimeStatus); err != nil {
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
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = ?`, runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != StatusExpired {
		t.Fatalf("runtime row must be re-projected to expired, got %s", runtimeStatus)
	}
	if bindings := countGrantBindings(t, f, created.Item.ID); len(bindings) != 0 {
		t.Fatalf("stale binding must be dropped: %+v", bindings)
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
}

// TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants keeps the scan
// narrow: grants outside the lookback window and grants still active are
// invisible to the reconcile pass.
func TestReconcileExpiredGrantsIgnoresOutsideWindowAndLiveGrants(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	seedPhysicalAccount(t, f, "res-1", "owner")
	seedPhysicalAccount(t, f, "res-2", "owner")
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
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants
		SET status = 'expired', updated_at = ? WHERE id = ?`, old, created.Item.ID); err != nil {
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
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, createdLive.Item.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusActive {
		t.Fatalf("live grant must stay untouched, got %s", status)
	}
}
