package authsys

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// findSinkInputByModule returns the persisted entry with the given module.
// Node recordOperationLog persists fire-and-forget (void
// recordOperationLogUnsafe -> void dispatchOperationLogToGo) with no
// ordering contract, and the Go Producer.Record goroutine-per-entry mirror
// inherits that: assertions must match by identity, never by slice index.
func findSinkInputByModule(inputs []operationlog.Input, module string) *operationlog.Input {
	for i := range inputs {
		if inputs[i].Module == module {
			return &inputs[i]
		}
	}
	return nil
}

func findSinkInputByMetadata(inputs []operationlog.Input, metadata string) *operationlog.Input {
	for i := range inputs {
		if string(inputs[i].Metadata) == metadata {
			return &inputs[i]
		}
	}
	return nil
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
	deferred := findSinkInputByModule(inputs, "client_ip_stats")
	if deferred == nil {
		t.Fatalf("client_ip_stats entry not persisted, got %d entries", len(inputs))
	}
	if deferred.DetailLevel != "full" || deferred.VisibilityScope != "admin_only" {
		t.Fatalf("deferred fields not passed through: detailLevel=%q visibilityScope=%q", deferred.DetailLevel, deferred.VisibilityScope)
	}
	if string(deferred.Metadata) != `{"ipHash":"abc","policyId":"ip_policy_1"}` {
		t.Fatalf("metadata not passed through: %s", string(deferred.Metadata))
	}
	legacy := findSinkInputByModule(inputs, "api_keys")
	if legacy == nil {
		t.Fatalf("api_keys entry not persisted, got %d entries", len(inputs))
	}
	if legacy.DetailLevel != "summary" || legacy.VisibilityScope != "targeted" {
		t.Fatalf("legacy default drift: detailLevel=%q visibilityScope=%q", legacy.DetailLevel, legacy.VisibilityScope)
	}
	if len(legacy.Metadata) != 0 {
		t.Fatalf("legacy metadata must stay empty, got %s", string(legacy.Metadata))
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
	snapshot := findSinkInputByMetadata(inputs, `{"k":1}`)
	if snapshot == nil {
		t.Fatalf("metadata snapshot entry not persisted, got %d entries", len(inputs))
	}
	if string(snapshot.Metadata) != `{"k":1}` {
		t.Fatalf("metadata snapshot semantics broken: %s", string(snapshot.Metadata))
	}
}

// TestProducerSinkGeneratesOperationLogIDs is the X05 regression for the
// management-plane operation log path: Node recordOperationLog defaults the
// entry id (newId('oplog')) before dispatch; the Go sink must do the same or
// every record fails F4 input normalization ("operation log input missing
// id") and the operation-logs surface stays empty.
func TestProducerSinkGeneratesOperationLogIDs(t *testing.T) {
	store := &sinkFakeStore{}
	producer := operationlog.NewProducer(store, operationlog.OwnerLease{}, operationlog.Config{}, nil)
	sink := &OperationLogProducerSink{Producer: producer, MaxChanges: 100}
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", nil)
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "super_admin",
		Module:               "announcements",
		Action:               "create",
		OperationKey:         "announcements.create",
		ResourceType:         "announcement",
		Summary:              "发布公告",
	}, request)
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "super_admin",
		Module:               "groups",
		Action:               "update",
		OperationKey:         "groups.update",
		ResourceType:         "group",
		Summary:              "编辑分组",
	}, request)
	inputs := waitForSinkInputs(t, store, 2)
	seen := map[string]bool{}
	for _, input := range inputs {
		if input.ID == "" {
			t.Fatal("sink must default the operation log id (Node newId('oplog') mirror)")
		}
		if !strings.HasPrefix(input.ID, "oplog_") {
			t.Fatalf("operation log id prefix wrong: %q", input.ID)
		}
		if input.CreatedAt == "" {
			t.Fatal("sink must default createdAt")
		}
		if seen[input.ID] {
			t.Fatalf("operation log ids must be unique, got duplicate %q", input.ID)
		}
		seen[input.ID] = true
	}
}
