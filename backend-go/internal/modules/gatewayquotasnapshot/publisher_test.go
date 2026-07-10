package gatewayquotasnapshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRuntimeStatePublisherPublishesNodeCompatibleSnapshot(t *testing.T) {
	setter := &runtimeStateSetterStub{}
	publisher, err := NewRuntimeStatePublisher(RuntimeStatePublisherOptions{
		State:     setter,
		Namespace: "test-ns",
		TTL:       2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewRuntimeStatePublisher() error = %v", err)
	}

	snapshot := Snapshot{
		GeneratedAt: "2026-07-09T12:00:00.000Z",
		CostEntries: []CostSnapshotEntry{{
			SystemAccountID: "sys_1",
			ScopeType:       "api_key",
			ScopeID:         "key_1",
		}},
		AuthorizationEntries: []AuthorizationSnapshotEntry{{
			ScopeType:       "account_authorization",
			AuthorizationID: "auth_1",
			Decision:        GatewayQuotaDecision{Allowed: false, Message: "额度已用完，请联系管理员提升额度"},
		}},
		CostEntriesComplete:          true,
		AuthorizationEntriesComplete: true,
		Timezone:                     "UTC",
		StatDate:                     "2026-07-09",
		StatWeek:                     "2026-07-06",
		StatMonth:                    "2026-07",
	}

	if err := publisher.Publish(context.Background(), snapshot); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if setter.key != "juhe-ai:test-ns:state:gateway_quota_snapshot:current" {
		t.Fatalf("published key = %q", setter.key)
	}
	if setter.ttl != 2*time.Minute {
		t.Fatalf("published ttl = %s", setter.ttl)
	}
	var decoded Snapshot
	if err := json.Unmarshal(setter.value, &decoded); err != nil {
		t.Fatalf("published payload is not snapshot JSON: %v", err)
	}
	if decoded.GeneratedAt != snapshot.GeneratedAt ||
		len(decoded.CostEntries) != 1 ||
		len(decoded.AuthorizationEntries) != 1 ||
		!decoded.CostEntriesComplete ||
		!decoded.AuthorizationEntriesComplete {
		t.Fatalf("decoded snapshot = %+v", decoded)
	}
}

func TestRuntimeStatePublisherValidatesInputs(t *testing.T) {
	if _, err := NewRuntimeStatePublisher(RuntimeStatePublisherOptions{}); err == nil || !strings.Contains(err.Error(), "redis setter") {
		t.Fatalf("missing setter error = %v", err)
	}
	if _, err := NewRuntimeStatePublisher(RuntimeStatePublisherOptions{State: &runtimeStateSetterStub{}}); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("missing namespace error = %v", err)
	}
	if _, err := NewRuntimeStatePublisher(RuntimeStatePublisherOptions{State: &runtimeStateSetterStub{}, Namespace: "test-ns", TTL: -time.Second}); err == nil || !strings.Contains(err.Error(), "ttl") {
		t.Fatalf("invalid ttl error = %v", err)
	}
}

type runtimeStateSetterStub struct {
	key   string
	value []byte
	ttl   time.Duration
}

func (s *runtimeStateSetterStub) SetRaw(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.key = key
	s.value = append([]byte(nil), value...)
	s.ttl = ttl
	return nil
}
