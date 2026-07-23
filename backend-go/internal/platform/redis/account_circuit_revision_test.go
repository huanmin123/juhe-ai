package redis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAccountCircuitRevisionKeysMatchNodeRuntimeStore(t *testing.T) {
	keys, err := accountCircuitRevisionRedisKeys("env-test", "gateway-account-circuit")
	if err != nil {
		t.Fatal(err)
	}
	if keys.states != "juhe-ai:env-test:account-circuit:gateway-account-circuit:states" || keys.due != "juhe-ai:env-test:account-circuit:gateway-account-circuit:due" || keys.revisions != "juhe-ai:env-test:account-circuit:gateway-account-circuit:dispatch-revisions" {
		t.Fatalf("keys=%+v", keys)
	}
}

func TestAccountCircuitRevisionScriptValidatesBeforeWritingTombstone(t *testing.T) {
	readIndex := strings.Index(projectAccountCircuitRevisionLua, "redis.call('HGETALL', states_key)")
	decodeIndex := strings.Index(projectAccountCircuitRevisionLua, "cjson.decode(values[index + 1])")
	writeIndex := strings.LastIndex(projectAccountCircuitRevisionLua, "redis.call('HSET', revisions_key")
	if readIndex < 0 || decodeIndex < readIndex || writeIndex < decodeIndex {
		t.Fatal("revision script must validate all runtime state before publishing the tombstone")
	}
	for _, fragment := range []string{"current_revision > incoming_revision", "status = 'stale'", "already_projected and 'idempotent'", "state['phase'] = 'CLOSED'", "redis.call('ZREM', due_key", "redis.call('HDEL', escalation_key"} {
		if !strings.Contains(projectAccountCircuitRevisionLua, fragment) {
			t.Fatalf("revision script missing %q", fragment)
		}
	}
	equalIndex := strings.Index(projectAccountCircuitRevisionLua, "local already_projected")
	scanIndex := strings.Index(projectAccountCircuitRevisionLua, "redis.call('HGETALL', states_key)")
	resultIndex := strings.Index(projectAccountCircuitRevisionLua, "local result_status")
	if equalIndex < 0 || scanIndex < equalIndex || resultIndex < scanIndex {
		t.Fatal("equal revision must scan and repair the whole runtime family before returning idempotent")
	}
	if strings.Contains(projectAccountCircuitRevisionLua, "HSET', revisions_key, runtime_key, tostring(max_seen_revision)") {
		t.Fatal("runtime state must not advance the durable outbox revision tombstone")
	}
}

func TestAccountCircuitRevisionProjectorValidatesTypedResult(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	var projectedAt time.Time
	projector := &AccountCircuitRevisionProjector{
		retention: time.Minute,
		now:       func() time.Time { return now },
		project: func(_ context.Context, _ accountCircuitRevisionKeys, _ port.GatewayAccountCircuitOutboxEvent, _ time.Duration, value time.Time) ([]byte, error) {
			projectedAt = value
			return json.Marshal(port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: 3, ClosedStates: 2})
		},
	}
	event := port.GatewayAccountCircuitOutboxEvent{
		EventID: "event-1", ProjectionKey: port.GatewayAccountCircuitProjectionKey,
		AccountID: "account-1", AccountRuntimeKey: "account-1", TransitionID: "transition-1", DispatchRevision: 3,
	}
	result, err := projector.ProjectGatewayAccountCircuitRevision(context.Background(), event)
	if err != nil || result.Status != port.GatewayAccountCircuitRevisionApplied || result.CurrentRevision != 3 || result.ClosedStates != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !projectedAt.Equal(now) {
		t.Fatalf("projected at %v, want %v", projectedAt, now)
	}
}

func TestAccountCircuitRevisionProjectorRejectsAuthorizedFormattedRuntimeKey(t *testing.T) {
	event := port.GatewayAccountCircuitOutboxEvent{
		ProjectionKey: port.GatewayAccountCircuitProjectionKey,
		AccountID:     "instance-1", AccountRuntimeKey: "instance-1:authorized:user:group:authorization",
		TransitionID: "transition", DispatchRevision: 2,
	}
	if err := validateAccountCircuitRevisionEvent(event); err == nil {
		t.Fatal("outbox projector must consume the raw account-id family tombstone key")
	}
}
