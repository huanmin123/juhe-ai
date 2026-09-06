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

// TestOperationLogProducerSinkHandoverFields locks in the M05/M07 sink
// extension: statusCode / targets / native change values reach the F4 Input
// untouched (Node OperationLogInput.statusCode, .targets and the
// normalizeSafeValue unknown forms), while the unset shapes keep the
// pre-extension contract.
func TestOperationLogProducerSinkHandoverFields(t *testing.T) {
	store := &sinkFakeStore{}
	sink := &OperationLogProducerSink{Producer: operationlog.NewProducer(store, operationlog.OwnerLease{}, operationlog.Config{InstanceID: "test"}, nil)}

	status := 204
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sysacc_admin",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "api_keys",
		Action:               "delete",
		OperationKey:         "api_keys.delete",
		ResourceType:         "api_key",
		ResourceID:           "key_1",
		Summary:              "删除 API Key：key_1",
		StatusCode:           &status,
		Changes: []OperationLogChange{{
			Field: "deleted", Label: "删除状态", BeforeValue: false, AfterValue: true,
		}},
	}, httptest.NewRequest(http.MethodDelete, "/__aisys__/api/api-keys/key_1", nil))

	// Object/array change values flatten to JSON text exactly like Node
	// normalizeSafeValue (native null/number/boolean stay native).
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sysacc_admin",
		ActorRole:            "admin",
		Mode:                 "admin",
		Module:               "groups",
		Action:               "create",
		OperationKey:         "groups.create",
		ResourceType:         "group",
		Summary:              "创建分组",
		Changes: []OperationLogChange{{
			Field: "schedulingPolicy", Label: "调度策略",
			// Pre-encoded JSON keeps the Node insertion order byte-exact.
			AfterValue: json.RawMessage(`{"mode":"fast_first","maxQueueSize":120}`),
		}},
		Targets: []OperationLogTarget{
			{TargetType: "route_strategy", TargetID: "rs_1", TargetName: "默认策略", TargetOwnerSystemAccountID: "sysacc_admin", Relation: "affected"},
			{TargetType: "system_account", TargetID: "sysacc_user", TargetOwnerSystemAccountID: "sysacc_user", Relation: "grantee"},
		},
	}, httptest.NewRequest(http.MethodPost, "/__aisys__/api/groups", nil))

	// Legacy entry without the handover fields: StatusCode and Targets stay at
	// their zero contracts (nil → SQL NULL / primary-target normalization).
	sink.Record(OperationLogEntry{
		ActorSystemAccountID: "sysacc_admin",
		ActorRole:            "admin",
		Module:               "announcements",
		Action:               "create",
		OperationKey:         "announcements.create",
		ResourceType:         "announcement",
		Summary:              "发布公告",
	}, httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", nil))

	inputs := waitForSinkInputs(t, store, 3)
	if len(inputs) < 3 {
		t.Fatalf("expected 3 persisted entries, got %d", len(inputs))
	}
	deleted := findSinkInputByModule(inputs, "api_keys")
	if deleted == nil {
		t.Fatalf("api_keys entry not persisted, got %d entries", len(inputs))
	}
	if deleted.StatusCode == nil || *deleted.StatusCode != 204 {
		t.Fatalf("statusCode not passed through: %v", deleted.StatusCode)
	}
	if len(deleted.Changes) != 1 {
		t.Fatalf("unexpected change count: %+v", deleted.Changes)
	}
	if deleted.Changes[0].Before != false || deleted.Changes[0].After != true {
		t.Fatalf("native boolean change values must stay native: before=%v(%T) after=%v(%T)",
			deleted.Changes[0].Before, deleted.Changes[0].Before, deleted.Changes[0].After, deleted.Changes[0].After)
	}

	groups := findSinkInputByModule(inputs, "groups")
	if groups == nil {
		t.Fatalf("groups entry not persisted, got %d entries", len(inputs))
	}
	if len(groups.Targets) != 2 {
		t.Fatalf("targets not passed through: %+v", groups.Targets)
	}
	if groups.Targets[0].TargetType != "route_strategy" || groups.Targets[0].TargetID != "rs_1" ||
		groups.Targets[0].TargetName != "默认策略" || groups.Targets[0].Relation != "affected" ||
		groups.Targets[0].TargetOwnerSystemAccountID != "sysacc_admin" {
		t.Fatalf("route_strategy target drift: %+v", groups.Targets[0])
	}
	if groups.Targets[1].TargetType != "system_account" || groups.Targets[1].TargetID != "sysacc_user" ||
		groups.Targets[1].Relation != "grantee" {
		t.Fatalf("grantee target drift: %+v", groups.Targets[1])
	}
	if len(groups.Changes) != 1 {
		t.Fatalf("unexpected groups change count: %+v", groups.Changes)
	}
	encoded, err := json.Marshal(groups.Changes[0].After)
	if err != nil {
		t.Fatalf("marshal change after: %v", err)
	}
	// normalizeSafeValue flattens objects to JSON text (a string, not an
	// object) — the same double serialization Node applies.
	if string(encoded) != `"{\"mode\":\"fast_first\",\"maxQueueSize\":120}"` {
		t.Fatalf("object change value must flatten to JSON text, got %s", encoded)
	}

	legacy := findSinkInputByModule(inputs, "announcements")
	if legacy == nil {
		t.Fatalf("announcements entry not persisted, got %d entries", len(inputs))
	}
	if legacy.StatusCode != nil {
		t.Fatalf("legacy statusCode must stay nil, got %v", *legacy.StatusCode)
	}
	if legacy.Targets != nil {
		t.Fatalf("legacy targets must stay nil, got %+v", legacy.Targets)
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
