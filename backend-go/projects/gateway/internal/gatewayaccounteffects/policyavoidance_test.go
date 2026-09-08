package gatewayaccounteffects

// D-132（BUG-0175 W4-B）配置策略账号避让写读面的 Mock 回归：可回放的内存
// store + 可注入的 dirty 标记与失效回调，覆盖正常/边界/降级路径。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakePolicyAvoidanceStore struct {
	values   map[string]json.RawMessage
	ttls     map[string]int64
	getErr   error
	setErr   error
	setCalls int
}

func newFakePolicyAvoidanceStore() *fakePolicyAvoidanceStore {
	return &fakePolicyAvoidanceStore{values: map[string]json.RawMessage{}, ttls: map[string]int64{}}
}

func (s *fakePolicyAvoidanceStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.values[key], nil
}

func (s *fakePolicyAvoidanceStore) SetJSON(_ context.Context, key string, value any, ttlMs int64) error {
	if s.setErr != nil {
		return s.setErr
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.values[key] = encoded
	s.ttls[key] = ttlMs
	s.setCalls++
	return nil
}

type fakeDirtyMarker struct {
	calls []dirtyCall
	err   error
}

type dirtyCall struct {
	accountID     string
	reason        string
	availableAtMs int64
}

func (m *fakeDirtyMarker) Mark(_ context.Context, accountID string, reason string, availableAtMs int64) error {
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, dirtyCall{accountID: accountID, reason: reason, availableAtMs: availableAtMs})
	return nil
}

// NewFakeClockMs 是测试时钟的最小构造（fixedFakeClock 已在包内测试中存在时
// 复用同名形状，避免依赖顺序）。
func NewFakeClockMs(nowMs int64) Clock {
	return fakeClockMs{nowMs: nowMs}
}

type fakeClockMs struct{ nowMs int64 }

func (c fakeClockMs) Now() time.Time { return time.UnixMilli(c.nowMs) }

func TestSuppressGatewayAccountLocallyForSecondsWritesStateAndDirty(t *testing.T) {
	store := newFakePolicyAvoidanceStore()
	dirty := &fakeDirtyMarker{}
	invalidations := 0
	service := NewConfiguredPolicyAvoidanceService(store, dirty.Mark, func() { invalidations++ }, fakeClockMs{nowMs: 5_000_000})

	seconds := int64(120)
	err := service.SuppressGatewayAccountLocallyForSeconds(context.Background(), SuppressibleGatewayAccount{ID: "acc-1"}, &seconds, "响应检查策略命中：测试")
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	raw, ok := store.values["acc-1"]
	if !ok {
		t.Fatal("state key missing")
	}
	var state ConfiguredPolicyAvoidanceState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.RuntimeKey != "acc-1" || state.AccountID != "acc-1" || state.Reason != "响应检查策略命中：测试" {
		t.Errorf("state = %+v", state)
	}
	if state.StartedAtMs != 5_000_000 || state.UntilMs != 5_000_000+120_000 {
		t.Errorf("ttl window = %d..%d", state.StartedAtMs, state.UntilMs)
	}
	if store.ttls["acc-1"] != 120_000 {
		t.Errorf("store ttl = %d, want 120000", store.ttls["acc-1"])
	}
	if len(dirty.calls) != 1 || dirty.calls[0].accountID != "acc-1" || dirty.calls[0].reason != "runtime_availability_changed" {
		t.Errorf("dirty calls = %+v", dirty.calls)
	}
	if invalidations != 1 {
		t.Errorf("invalidations = %d, want 1", invalidations)
	}
}

func TestSuppressGatewayAccountLocallyForSecondsDefaultsAndClamps(t *testing.T) {
	if got := normalizePolicyAvoidanceSeconds(nil); got != 60 {
		t.Errorf("nil seconds = %d, want 60", got)
	}
	zero := int64(0)
	if got := normalizePolicyAvoidanceSeconds(&zero); got != 1 {
		t.Errorf("zero seconds = %d, want 1", got)
	}
	negative := int64(-5)
	if got := normalizePolicyAvoidanceSeconds(&negative); got != 1 {
		t.Errorf("negative seconds = %d, want 1", got)
	}
}

func TestSuppressDirtyMarkerFailureAbortsBeforeStoreWrite(t *testing.T) {
	store := newFakePolicyAvoidanceStore()
	dirty := &fakeDirtyMarker{err: errors.New("db down")}
	service := NewConfiguredPolicyAvoidanceService(store, dirty.Mark, nil, fakeClockMs{nowMs: 1})
	err := service.SuppressGatewayAccountLocallyForSeconds(context.Background(), SuppressibleGatewayAccount{ID: "acc-1"}, nil, "r")
	if err == nil {
		t.Fatal("dirty failure must propagate（投影必须先于 Redis 不可用）")
	}
	if store.setCalls != 0 {
		t.Errorf("store writes = %d, want 0", store.setCalls)
	}
}

func TestLoadConfiguredPolicyAvoidanceStatesReadsAndCaches(t *testing.T) {
	store := newFakePolicyAvoidanceStore()
	service := NewConfiguredPolicyAvoidanceService(store, nil, nil, fakeClockMs{nowMs: 10_000})
	state := ConfiguredPolicyAvoidanceState{RuntimeKey: "acc-2", AccountID: "acc-2", Reason: "r", StartedAtMs: 9_000, UntilMs: 20_000}
	encoded, _ := json.Marshal(state)
	store.values["acc-2"] = encoded

	states, err := service.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-1", "acc-2"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if states[0] != nil {
		t.Errorf("missing key should be nil, got %+v", states[0])
	}
	if states[1] == nil || states[1].UntilMs != 20_000 {
		t.Fatalf("states[1] = %+v", states[1])
	}
	// 第二次读取命中进程内正缓存，不再触达 store。
	delete(store.values, "acc-2")
	cached, err := service.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-2"})
	if err != nil || cached[0] == nil || cached[0].UntilMs != 20_000 {
		t.Fatalf("cached = %+v err %v", cached, err)
	}
	// 未命中的键进入负缓存窗口：同样不再触达 store。
	store.values["acc-1"] = encoded
	missAgain, err := service.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"acc-1"})
	if err != nil {
		t.Fatalf("negative cache load: %v", err)
	}
	if missAgain[0] != nil {
		t.Errorf("negative cache should hold nil within the window, got %+v", missAgain[0])
	}
}

func TestLoadConfiguredPolicyAvoidanceStatesIgnoresCorruptRows(t *testing.T) {
	store := newFakePolicyAvoidanceStore()
	store.values["bad"] = json.RawMessage(`{"runtimeKey":"bad"}`)
	store.values["broken"] = json.RawMessage(`{not-json`)
	service := NewConfiguredPolicyAvoidanceService(store, nil, nil, fakeClockMs{nowMs: 1})
	states, err := service.LoadConfiguredPolicyAvoidanceStates(context.Background(), []string{"bad", "broken"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for index, state := range states {
		if state != nil {
			t.Errorf("states[%d] = %+v, want nil", index, state)
		}
	}
}

func TestDecodeConfiguredPolicyAvoidanceStateShape(t *testing.T) {
	if decodeConfiguredPolicyAvoidanceState(json.RawMessage(`{"runtimeKey":"","accountId":"a","startedAtMs":1,"untilMs":2}`)) != nil {
		t.Error("empty runtimeKey must be rejected")
	}
	if decodeConfiguredPolicyAvoidanceState(json.RawMessage(`{"runtimeKey":"a","accountId":"a","startedAtMs":5,"untilMs":2}`)) != nil {
		t.Error("untilMs < startedAtMs must be rejected")
	}
	valid := decodeConfiguredPolicyAvoidanceState(json.RawMessage(`{"runtimeKey":"a","accountId":"a","reason":"r","startedAtMs":1,"untilMs":2}`))
	if valid == nil || valid.UntilMs != 2 || valid.Reason != "r" {
		t.Errorf("valid = %+v", valid)
	}
}
