package authz

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// A new, never-expiring grant must clear the previous cycle's runtime expiry.
// Before the projection fix an empty decision kept the old column value, so a
// re-accepted authorization stayed active but permanently failed every expiry
// gate with a past timestamp.
func TestReacceptWithoutExpiryClearsStaleRuntimeExpiry(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "res-expiry-reset", "owner", "expiry reset", "gpt")
	future := f.now.UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	past := f.now.UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)

	offer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "res-expiry-reset",
		GranteeType: "system_account", GranteeUsername: "grantee",
		ExpiresAt: &future,
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create first offer: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", offer.SourceID); err != nil {
		t.Fatalf("accept first offer: %v", err)
	}
	runtimeID := accountRuntimeIDFor(t, f, "res-expiry-reset", "grantee")
	var runtimeExpires sql.NullString
	if err := f.db.QueryRow(fixtureBind(`SELECT expires_at FROM resource_authorizations WHERE id = ?`), runtimeID).
		Scan(&runtimeExpires); err != nil {
		t.Fatalf("read first runtime expiry: %v", err)
	}
	if !runtimeExpires.Valid || runtimeExpires.String == "" {
		t.Fatalf("first accepted offer must project its expiry, got %v", runtimeExpires)
	}

	if err := f.store.ReturnSourceForGrantee(context.Background(), offer.SourceID, "grantee"); err != nil {
		t.Fatalf("return first source: %v", err)
	}
	// Simulate the stale audit value a prior finite cycle leaves behind.
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorizations SET expires_at = ? WHERE id = ?`), past, runtimeID); err != nil {
		t.Fatalf("plant stale runtime expiry: %v", err)
	}
	f.now = f.now.Add(25 * time.Hour)

	newOffer, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "account", ResourceID: "res-expiry-reset",
		GranteeType: "system_account", GranteeUsername: "grantee",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatalf("create replacement offer: %v", err)
	}
	if _, err := f.acceptSourceForTest(t, "grantee", newOffer.SourceID); err != nil {
		t.Fatalf("accept replacement offer: %v", err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT expires_at FROM resource_authorizations WHERE id = ?`), runtimeID).
		Scan(&runtimeExpires); err != nil {
		t.Fatalf("read replacement runtime expiry: %v", err)
	}
	if runtimeExpires.Valid {
		t.Fatalf("never-expiring replacement must clear runtime expiry, got %q", runtimeExpires.String)
	}
}
