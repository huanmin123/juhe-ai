package authz

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Legacy expired grants carry legacy_basis=...legacy_expiry_unknown while
// expired_from_status stays 'active' (design §7.6 item 16). Extend must still
// demand an explicit administrator confirmation instead of silently resuming a
// grant that may have been paused.
func TestExtendLegacyExpiryUnknownRequiresAdminConfirmation(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "res-legacy-expiry", "owner", "legacy expiry", "gpt")
	grantID, sourceID := f.createAcceptedDirectOffer(t, "account", "res-legacy-expiry", "grantee")

	past := f.now.UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	future := f.now.UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	now := f.now.UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants
		SET status = 'expired', acceptance_mode = 'legacy', expires_at = ?, ended_at = ?, ended_reason = 'authorization_expired',
			revoked_at = ?, expired_from_status = 'active',
			legacy_basis = 'migration;legacy_expiry_unknown', expiry_cycle = 0
		WHERE id = ?`), past, now, now, grantID); err != nil {
		t.Fatalf("seed legacy expired grant: %v", err)
	}
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_sources
		SET status = 'expired', ended_at = ?, ended_reason = 'authorization_expired',
			expired_from_status = 'active', expiry_cycle = 0,
			accepted_source_revision = NULL, accepted_source_revision_version = NULL
		WHERE id = ?`), now, sourceID); err != nil {
		t.Fatalf("seed legacy expired source: %v", err)
	}

	err := f.store.ExtendOffer(context.Background(), "owner", grantID, ExtendInput{ExpiresAt: future}, false, now)
	var fail *Fail
	if !errors.As(err, &fail) || fail.Code != "legacy_expiry_unknown" {
		t.Fatalf("extend without confirmation = %v, want legacy_expiry_unknown", err)
	}

	if err := f.store.ExtendOffer(context.Background(), "owner", grantID, ExtendInput{
		ExpiresAt: future, ConfirmExpiredFromStatus: StatusActive,
	}, true, now); err != nil {
		t.Fatalf("admin-confirmed extend: %v", err)
	}
	var status string
	if err := f.db.QueryRow(fixtureBind(`SELECT status FROM resource_authorization_grants WHERE id = ?`), grantID).
		Scan(&status); err != nil {
		t.Fatalf("read extended grant: %v", err)
	}
	if status == StatusExpired {
		t.Fatal("confirmed extend must leave the expired state")
	}
}
