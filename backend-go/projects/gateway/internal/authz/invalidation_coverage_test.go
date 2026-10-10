// Coverage-table behavior tests (网关模型列表账户并集设计 6.3 "写路径 → 失效覆盖
// 依据"): the committed-write entry points that open their own transaction —
// the expiry sweeps (ExpireSweep, ReconcileExpiredGrants) and the standalone
// team-cascade wrappers (RevokeAllTeamSources, ReactivateTeamGrants,
// RevokeTeamSourcesForMember) — must fire the same post-commit invalidation
// fan-out as the business mutations pinned by TestWritePathsFireInvalidationFanout.
// The Tx variants stay fan-out-free on purpose: their callers (systemteams
// afterCommit, the accounts delete path) commit and invalidate.
package authz

import (
	"context"
	"testing"
	"time"
)

// TestExpirySweepsFireExpiredFanout pins both committed expiry sweeps: a
// no-due sweep publishes nothing, a committed flip publishes
// resource_authorization_expired, and the jobs-sweep convergence replay
// (ReconcileExpiredGrants) publishes again after its own commit.
func TestExpirySweepsFireExpiredFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedGroup(t, "grp_1", "owner")
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	future := f.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	created, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "system_account", GranteeID: "grantee",
		ExpiresAt: &future,
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonCreated)

	// Nothing due yet: the sweep commits no write and stays silent.
	if _, err := f.store.ExpireSweep(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	assertNoFanout(t, stats, bus, 1, 3)

	// Age the grant past its expiry so the sweep materializes the flip.
	past := f.now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET expires_at = ? WHERE id = ?`,
		past, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	swept, err := f.store.ExpireSweep(context.Background(), 10)
	if err != nil || swept != 1 {
		t.Fatalf("ExpireSweep = %d, %v", swept, err)
	}
	assertOneFanout(t, stats, bus, 1, 3, invalidationReasonExpired)
	var grantStatus string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`,
		created.Item.ID).Scan(&grantStatus); err != nil || grantStatus != StatusExpired {
		t.Fatalf("grant status = %q, %v", grantStatus, err)
	}

	// The reconcile replay re-projects the same flipped grant inside the
	// lookback window and publishes after its own commit.
	reconciled, err := f.store.ReconcileExpiredGrants(context.Background(), 0, 0)
	if err != nil || reconciled != 1 {
		t.Fatalf("ReconcileExpiredGrants = %d, %v", reconciled, err)
	}
	assertOneFanout(t, stats, bus, 2, 6, invalidationReasonExpired)
}

// TestTeamCascadeStandaloneWrappersFireFanout pins the three own-transaction
// cascade wrappers: each committed pass fires the fan-out with the business
// reason of the state change it materialized (revocation →
// resource_authorization_revoked, re-expansion → resource_authorization_updated).
func TestTeamCascadeStandaloneWrappersFireFanout(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedGroup(t, "grp_1", "owner")
	f.seedTeamWithMember(t, "team_1", "member")
	stats := &recordingStatsDirty{}
	bus := &recordingBus{}
	f.store.AttachWriteInvalidator(stats, bus)

	now := f.now.UTC().Format(time.RFC3339Nano)
	// seedTeamGrantRuntime seeds one active runtime authorization row plus one
	// active team source for the member (the unique user index forces
	// delete-before-reseed between the revocation arms).
	seedTeamGrantRuntime := func(t *testing.T) {
		t.Helper()
		for _, statement := range []string{
			`DELETE FROM resource_authorization_sources WHERE authorization_id = 'rauth_m'`,
			`DELETE FROM resource_authorizations WHERE id = 'rauth_m'`,
		} {
			if _, err := f.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		for _, statement := range []string{
			`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
				grantee_system_account_id, status, created_by, created_at, updated_at)
				VALUES ('rauth_m', 'group', 'grp_1', 'owner', 'member', 'active', 'owner', ?, ?)`,
			`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, source_team_id,
				status, created_by, created_at, updated_at)
				VALUES ('ras_m', 'rauth_m', 'team', 'team_1', 'active', 'owner', ?, ?)`,
		} {
			if _, err := f.db.Exec(statement, now, now); err != nil {
				t.Fatal(err)
			}
		}
	}

	// RevokeAllTeamSources: the committed team-disabled revocation flips the
	// runtime row to revoked and fires the revoked fan-out.
	seedTeamGrantRuntime(t)
	if err := f.store.RevokeAllTeamSources(context.Background(), "team_1", "owner", "team_disabled"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 0, 0, invalidationReasonRevoked)
	var status string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = 'rauth_m'`).Scan(&status); err != nil || status != StatusRevoked {
		t.Fatalf("runtime status after revoke = %q, %v", status, err)
	}

	// RevokeTeamSourcesForMember: same committed-write contract for the
	// member-removal cascade.
	seedTeamGrantRuntime(t)
	if err := f.store.RevokeTeamSourcesForMember(context.Background(), "team_1", "member", "owner"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 1, 3, invalidationReasonRevoked)

	// ReactivateTeamGrants: from the revoked runtime state left by the member
	// removal, the committed re-expansion upserts the member runtime row back
	// to active from the active team grant and fires the updated fan-out.
	if _, err := f.db.Exec(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id,
		resource_owner_system_account_id, grantee_type, grantee_team_id, status, created_by, created_at, updated_at)
		VALUES ('rauthgrant_t1', 'group', 'grp_1', 'owner', 'team', 'team_1', 'active', 'owner', ?, ?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReactivateTeamGrants(context.Background(), "team_1", "owner"); err != nil {
		t.Fatal(err)
	}
	assertOneFanout(t, stats, bus, 2, 6, invalidationReasonUpdated)
	var reactivated string
	if err := f.db.QueryRow(`SELECT status FROM resource_authorizations WHERE id = 'rauth_m'`).Scan(&reactivated); err != nil || reactivated != StatusActive {
		t.Fatalf("runtime status after reactivate = %q, %v", reactivated, err)
	}
}
