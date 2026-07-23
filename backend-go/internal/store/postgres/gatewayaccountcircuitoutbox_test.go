package postgres

import (
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestGatewayAccountCircuitOutboxSQLClaimsOnlySupportedRevisionEvents(t *testing.T) {
	for _, fragment := range []string{
		"projection_key = $1::text",
		"event_type = 'dispatch_revision_changed'",
		"status = 'pending'",
		"status = 'processing'",
		"claim_until_ms <=",
		"ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(claimGatewayAccountCircuitOutboxSQL, fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
	}
	if strings.Contains(claimGatewayAccountCircuitOutboxSQL, "incident_changed") {
		t.Fatal("Go revision projector must not claim Node incident events")
	}
	for _, fragment := range []string{"WITH candidates AS", "claimed AS", "md5($4::text || ':' || candidates.event_id)", "attempt_count = outbox.attempt_count + 1", "RETURNING"} {
		if !strings.Contains(claimGatewayAccountCircuitOutboxSQL, fragment) {
			t.Fatalf("atomic claim SQL missing %q", fragment)
		}
	}
}

func TestGatewayAccountCircuitOutboxSQLUsesClaimCASAndMonotonicAck(t *testing.T) {
	for _, fragment := range []string{"status = 'dispatched'", "claim_token = $3::text", "acknowledged_at_ms", "last_error_class = NULL"} {
		if !strings.Contains(acknowledgeGatewayAccountCircuitOutboxSQL, fragment) {
			t.Fatalf("ack SQL missing %q", fragment)
		}
	}
	if !strings.Contains(advanceGatewayAccountCircuitProjectionRevisionSQL, "GREATEST(circuit_projection_revision") || !strings.Contains(advanceGatewayAccountCircuitProjectionRevisionSQL, "dispatch_revision >=") {
		t.Fatal("projection revision SQL must be monotonic and fenced by durable dispatch revision")
	}
	for _, fragment := range []string{"status = 'pending'", "available_at_ms = $4::bigint", "last_error_class = $3::text", "status = 'processing'", "claim_token = $2::text"} {
		if !strings.Contains(releaseGatewayAccountCircuitOutboxSQL, fragment) {
			t.Fatalf("release SQL missing %q", fragment)
		}
	}
}

func TestGatewayAccountCircuitOutboxInputsAreBounded(t *testing.T) {
	now := time.Now().UTC()
	if err := validateGatewayAccountCircuitOutboxClaim(port.GatewayAccountCircuitOutboxClaimInput{OwnerID: "worker", Now: now, Lease: 30 * time.Second, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if err := validateGatewayAccountCircuitOutboxClaim(port.GatewayAccountCircuitOutboxClaimInput{OwnerID: "worker", Now: now, Lease: 30 * time.Second, Limit: 501}); err == nil {
		t.Fatal("expected unsafe claim limit rejection")
	}
	if err := validateGatewayAccountCircuitOutboxRelease(port.GatewayAccountCircuitOutboxReleaseInput{EventID: "event", ClaimToken: "claim", ErrorClass: strings.Repeat("x", 65), Now: now, RetryDelay: time.Second}); err == nil {
		t.Fatal("expected oversized error class rejection")
	}
}
