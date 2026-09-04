package authsys

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// sinkFakeStore captures the full Input handed to Persist so the M12
// deferral contract (detailLevel / visibilityScope / metadata pass-through)
// can be asserted end to end through OperationLogProducerSink.
type sinkFakeStore struct {
	mu     sync.Mutex
	inputs []operationlog.Input
}

func (f *sinkFakeStore) EnsureSchema(context.Context) error { return nil }
func (f *sinkFakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (operationlog.OwnerLease, bool, error) {
	return operationlog.OwnerLease{}, true, nil
}
func (f *sinkFakeStore) RenewOwnerLease(context.Context, operationlog.OwnerLease, time.Duration) (bool, error) {
	return true, nil
}
func (f *sinkFakeStore) ReleaseOwnerLease(context.Context, operationlog.OwnerLease) error {
	return nil
}
func (f *sinkFakeStore) Persist(_ context.Context, _ operationlog.OwnerLease, input operationlog.Input) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, input)
	return true, nil
}
func (f *sinkFakeStore) List(context.Context, operationlog.ListOptions) (operationlog.ListResult, error) {
	return operationlog.ListResult{}, nil
}
func (f *sinkFakeStore) Detail(context.Context, string, string) (operationlog.DetailSupplement, bool, error) {
	return operationlog.DetailSupplement{}, false, nil
}
func (f *sinkFakeStore) CleanupRetention(context.Context, operationlog.OwnerLease, time.Time, int) (int64, error) {
	return 0, nil
}
func (f *sinkFakeStore) RetentionDays(context.Context, int) (int, error) { return 0, nil }
func (f *sinkFakeStore) Close() error                                    { return nil }

func (f *sinkFakeStore) recorded() []operationlog.Input {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]operationlog.Input(nil), f.inputs...)
}

func waitForSinkInputs(t *testing.T, store *sinkFakeStore, count int) []operationlog.Input {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if inputs := store.recorded(); len(inputs) >= count {
			return inputs
		}
		time.Sleep(10 * time.Millisecond)
	}
	return store.recorded()
}

// TestOperationLogProducerSinkDeferredFieldsLockedIn mirrors the M12
// deferral closure: entries carrying Node OperationLogInput detailLevel /
// visibilityScope / metadata (storage/operation-log-types.ts) reach the F4
// Input untouched, while empty fields keep the historical producer contract.
func TestOperationLogProducerSinkDeferredFieldsLockedIn(t *testing.T) {
	store := &sinkFakeStore{}
	sink := &OperationLogProducerSink{Producer: operationlog.NewProducer(store, operationlog.OwnerLease{}, operationlog.Config{InstanceID: "test"}, nil)}

	metadata := json.RawMessage(`{"ipHash":"abc","policyId":"ip_policy_1"}`)
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sysacc_admin",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "client_ip_stats",
		Action:               "blacklist",
		OperationKey:         "client_ip_stats.blacklist",
		ResourceType:         "client_ip",
		ResourceID:           "abc",
		ResourceName:         "abc",
		Summary:              "封禁 IP：abc",
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Metadata:             metadata,
	}, httptest.NewRequest("POST", "/__aisys__/api/ip-stats/x/blacklist", nil))

	// Legacy producer shape: no deferred fields set, pre-extension contract
	// ("summary"/"targeted", no metadata) stays byte-identical.
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sysacc_admin",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "api_keys",
		Action:               "create",
		OperationKey:         "api_keys.create",
		ResourceType:         "api_key",
		Summary:              "创建 API Key",
	}, httptest.NewRequest("POST", "/__aisys__/api/api-keys", nil))

	inputs := waitForSinkInputs(t, store, 2)
	if len(inputs) < 2 {
		t.Fatalf("expected 2 persisted entries, got %d", len(inputs))
	}
	if inputs[0].DetailLevel != "full" || inputs[0].VisibilityScope != "admin_only" {
		t.Fatalf("deferred fields not passed through: detailLevel=%q visibilityScope=%q", inputs[0].DetailLevel, inputs[0].VisibilityScope)
	}
	if string(inputs[0].Metadata) != `{"ipHash":"abc","policyId":"ip_policy_1"}` {
		t.Fatalf("metadata not passed through: %s", string(inputs[0].Metadata))
	}
	if inputs[1].DetailLevel != "summary" || inputs[1].VisibilityScope != "targeted" {
		t.Fatalf("legacy default drift: detailLevel=%q visibilityScope=%q", inputs[1].DetailLevel, inputs[1].VisibilityScope)
	}
	if len(inputs[1].Metadata) != 0 {
		t.Fatalf("legacy metadata must stay empty, got %s", string(inputs[1].Metadata))
	}

	// The metadata copy must be defensive: mutating the entry afterwards
	// cannot retro-alter the persisted payload.
	entry := OperationLogEntry{DetailLevel: "full", VisibilityScope: "all_users", Metadata: json.RawMessage(`{"k":1}`)}
	sink.Record(entry, httptest.NewRequest("POST", "/x", nil))
	copyOfMetadata := json.RawMessage(`{"k":2}`)
	entry.Metadata = copyOfMetadata
	inputs = waitForSinkInputs(t, store, 3)
	if len(inputs) < 3 {
		t.Fatalf("expected 3 persisted entries, got %d", len(inputs))
	}
	if string(inputs[2].Metadata) != `{"k":1}` {
		t.Fatalf("metadata snapshot semantics broken: %s", string(inputs[2].Metadata))
	}
}
