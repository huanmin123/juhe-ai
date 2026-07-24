package postgres

import (
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestGatewayAccountCircuitIncidentProjectionUsesExactScopeLookup(t *testing.T) {
	for _, fragment := range []string{
		"WHERE incident.circuit_scope_key = $1::text",
		"JOIN juhe_business.accounts",
		"account.dispatch_revision",
	} {
		if !strings.Contains(loadGatewayAccountCircuitIncidentForProjectionSQL, fragment) {
			t.Fatalf("projection SQL missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"OFFSET", "LIMIT 500", "account_runtime_key IN"} {
		if strings.Contains(loadGatewayAccountCircuitIncidentForProjectionSQL, forbidden) {
			t.Fatalf("projection SQL contains full-ledger pattern %q", forbidden)
		}
	}
}

func TestGatewayAccountCircuitIncidentRebuildUsesCurrentRevisionKeyset(t *testing.T) {
	for _, fragment := range []string{
		"incident.dispatch_revision = account.dispatch_revision",
		"incident.state <> 'CLOSED' OR incident.retained_until_ms > $1::bigint",
		"incident.updated_at_ms > $2::bigint",
		"incident.circuit_scope_key > $3::text",
		"ORDER BY incident.updated_at_ms ASC, incident.circuit_scope_key ASC",
		"LIMIT $4",
	} {
		if !strings.Contains(listGatewayAccountCircuitIncidentsForRebuildSQL, fragment) {
			t.Fatalf("rebuild SQL missing %q", fragment)
		}
	}
}

func TestGatewayAccountCircuitIncidentValidationRejectsMalformedScope(t *testing.T) {
	base := port.GatewayAccountCircuitIncident{
		CircuitScopeKey: "scope", AccountID: "account", AccountRuntimeKey: "account",
		ScopeKind: "account", IncidentID: "incident", State: "OPEN", DispatchRevision: 1,
		LedgerRevision: 1, TransitionID: "transition", UpdatedAt: time.Now(),
	}
	if err := validateGatewayAccountCircuitIncident(base); err != nil {
		t.Fatal(err)
	}
	base.ScopeKind = "key"
	if err := validateGatewayAccountCircuitIncident(base); err == nil {
		t.Fatal("key scope without fingerprint must fail")
	}
}
