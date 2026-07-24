package redis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAccountCircuitIncidentRestoreFencesRevisionBeforeGeneration(t *testing.T) {
	revisionIndex := strings.Index(restoreAccountCircuitIncidentLua, "existing_revision > incoming_revision")
	generationIndex := strings.Index(restoreAccountCircuitIncidentLua, "existing_generation > incoming_generation")
	if revisionIndex < 0 || generationIndex < revisionIndex {
		t.Fatal("incident restore must compare dispatch revision before generation")
	}
	independentGenerationIndex := strings.Index(restoreAccountCircuitIncidentLua, "existing_revision == incoming_revision and existing_generation > incoming_generation")
	ledgerEqualityIndex := strings.Index(restoreAccountCircuitIncidentLua, "existing_ledger_revision == incoming_ledger_revision")
	if independentGenerationIndex < 0 || ledgerEqualityIndex < independentGenerationIndex {
		t.Fatal("same-dispatch generation regression must be rejected before ledger idempotency")
	}
	for _, fragment := range []string{
		"redis.call('HGET', revisions_key, account_id)",
		"redis.call('HGET', ledger_revisions_key, scope_key)",
		"redis.call('HLEN', states_key)",
		"capacity_exhausted",
		"ledger_conflict",
		"scope_runtime_key",
		"runtime_scopes_key",
		"account_runtimes_key",
		"runtime_accounts_key",
		"incoming_ledger_revision == 1",
		"transitionId = state['transitionId']",
		"HSETNX', index_meta_key",
	} {
		if !strings.Contains(restoreAccountCircuitIncidentLua, fragment) {
			t.Fatalf("restore script missing %q", fragment)
		}
	}
}

func TestAccountCircuitIncidentRestorerBuildsTypedRuntimeState(t *testing.T) {
	now := time.Date(2026, 7, 24, 6, 0, 0, 0, time.UTC)
	retryAt := now.Add(time.Minute)
	var state accountCircuitIncidentRuntimeState
	restorer := &AccountCircuitIncidentRestorer{
		retention: time.Minute, capacity: 10, now: func() time.Time { return now },
		restore: func(_ context.Context, _ accountCircuitRevisionKeys, _ port.GatewayAccountCircuitIncident, raw []byte, projectedAt time.Time, _ time.Duration, capacity int) ([]byte, error) {
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
			if !projectedAt.Equal(now) || capacity != 10 {
				t.Fatalf("projectedAt/capacity=%v/%d", projectedAt, capacity)
			}
			return json.Marshal(port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: 7})
		},
	}
	incident := port.GatewayAccountCircuitIncident{
		CircuitScopeKey: "scope-1", AccountID: "account-1", AccountRuntimeKey: "account-1",
		ScopeKind: "protocol_model", ProtocolCode: "openai", RequestLane: "text", ModelFamily: "gpt",
		IncidentID: "incident-1", State: "PERSISTING", Generation: 3, DispatchRevision: 7,
		LedgerRevision: 9, TransitionID: "transition-1", NextTransitionAt: &retryAt,
		BackoffLevel: 2, RecoveringSuccesses: 1, UpdatedAt: now,
	}
	result, err := restorer.RestoreGatewayAccountCircuitIncident(context.Background(), incident)
	if err != nil || result.Status != port.GatewayAccountCircuitRevisionApplied {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if state.Phase != "OPEN" || state.Scope.ProtocolProfile != "openai" || state.Scope.ModelBucket != "gpt" || state.DispatchRevision != "7" || state.LedgerRevision != "9" || state.RetryAtMS == nil || *state.RetryAtMS != retryAt.UnixMilli() {
		t.Fatalf("runtime state=%+v", state)
	}
}

func TestAccountCircuitIncidentRestorerRejectsCapacityResult(t *testing.T) {
	restorer := &AccountCircuitIncidentRestorer{
		retention: time.Minute, capacity: 1, now: time.Now,
		restore: func(context.Context, accountCircuitRevisionKeys, port.GatewayAccountCircuitIncident, []byte, time.Time, time.Duration, int) ([]byte, error) {
			return []byte(`{"status":"capacity_exhausted","currentRevision":1,"closedStates":0}`), nil
		},
	}
	incident := port.GatewayAccountCircuitIncident{CircuitScopeKey: "scope", AccountID: "account", AccountRuntimeKey: "account", ScopeKind: "account", IncidentID: "incident", State: "OPEN", DispatchRevision: 1, LedgerRevision: 1, TransitionID: "transition", UpdatedAt: time.Now()}
	if _, err := restorer.RestoreGatewayAccountCircuitIncident(context.Background(), incident); err == nil {
		t.Fatal("capacity exhaustion must fail projection for replay")
	}
}
