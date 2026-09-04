package gatewayobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// metric key 全枚举（对齐 Node gatewayRoutingObservationMetricKey）
// ---------------------------------------------------------------------------

func TestGatewayRoutingObservationMetricKeyTable(t *testing.T) {
	cases := []struct {
		name        string
		observation Observation
		want        string
	}{
		{"transition", Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"}, "circuit.transition.suspect.open.transport"},
		{"transition-recovering", Observation{Kind: KindCircuitTransition, From: "HALF_OPEN", To: "RECOVERING", Source: "recovery"}, "circuit.transition.half_open.recovering.recovery"},
		{"mutation", Observation{Kind: KindCircuitMutation, Operation: "acquire_confirmation", Status: "stale_generation", LeaseKind: "confirmation"}, "circuit.mutation.acquire_confirmation.stale_generation.confirmation"},
		{"mutation-no-lease", Observation{Kind: KindCircuitMutation, Operation: "replace_revision", Status: "applied"}, "circuit.mutation.replace_revision.applied"},
		{"dispatch", Observation{Kind: KindCircuitDispatch, Outcome: "blocked", Phase: "OPEN"}, "circuit.dispatch.blocked.open"},
		{"dispatch-suspect", Observation{Kind: KindCircuitDispatch, Outcome: "rebuild_blocked", Phase: "SUSPECT"}, "circuit.dispatch.rebuild_blocked.suspect"},
		{"hot-quality", Observation{Kind: KindHotQualityMutation, Operation: "terminal", Status: "idempotent"}, "hot_quality.terminal.idempotent"},
		{"exploration", Observation{Kind: KindExploration, Outcome: "reserved"}, "exploration.reserved"},
		{"tier-escape", Observation{Kind: KindTierEscape, Outcome: "applied"}, "tier_escape.applied"},
		{"attempt", Observation{Kind: KindAttempt, Outcome: "transport_failure"}, "attempt.transport_failure"},
		{"budget", Observation{Kind: KindBudget, Outcome: "client_handoff"}, "budget.client_handoff"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := GatewayRoutingObservationMetricKey(testCase.observation); got != testCase.want {
				t.Fatalf("metric key = %q, want %q", got, testCase.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 内存 store（对齐 routing-observability-memory-store.ts 与官方回归脚本）
// ---------------------------------------------------------------------------

func TestMemoryStoreValidationFailureLeavesNoPartialCounters(t *testing.T) {
	store := NewMemoryGatewayRoutingObservabilityStore()
	err := store.RecordBatch(context.Background(), []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 1},
		{Observation: Observation{Kind: KindAttempt, Outcome: "completed"}, Count: 0},
	}, 1_000)
	if err == nil || !strings.Contains(err.Error(), "正安全整数") {
		t.Fatalf("批量校验失败必须报正安全整数错误，got %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RecordedEvents != 0 || len(snapshot.Counters) != 0 {
		t.Fatalf("批量校验失败时不得留下前置计数: %+v", snapshot)
	}
}

func TestMemoryStorePositiveCountAndNowValidation(t *testing.T) {
	store := NewMemoryGatewayRoutingObservabilityStore()
	cases := []struct {
		name    string
		entries []BatchEntry
		nowMs   int64
		wantErr string
	}{
		{"count-zero", []BatchEntry{{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 0}}, 1, "routing observability count 必须是正安全整数"},
		{"count-negative", []BatchEntry{{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: -1}}, 1, "routing observability count 必须是正安全整数"},
		{"count-beyond-safe", []BatchEntry{{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: MaxSafeInteger + 1}}, 1, "routing observability count 必须是正安全整数"},
		{"now-negative", []BatchEntry{{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 1}}, -1, "routing observability nowMs 必须是非负安全整数"},
		{"now-beyond-safe", []BatchEntry{{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 1}}, MaxSafeInteger + 1, "routing observability nowMs 必须是非负安全整数"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := store.RecordBatch(context.Background(), testCase.entries, testCase.nowMs)
			if err == nil || err.Error() != testCase.wantErr {
				t.Fatalf("err = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestMemoryStoreRecordAndSnapshot(t *testing.T) {
	// 与 backend/src/scripts/regression/gateway-routing-observability-regression.ts
	// 的固定枚举一致。
	fixedObservations := []Observation{
		{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"},
		{Kind: KindCircuitMutation, Operation: "acquire_confirmation", Status: "stale_generation", LeaseKind: "confirmation"},
		{Kind: KindHotQualityMutation, Operation: "terminal", Status: "idempotent"},
		{Kind: KindExploration, Outcome: "reserved"},
		{Kind: KindTierEscape, Outcome: "applied"},
		{Kind: KindAttempt, Outcome: "transport_failure"},
		{Kind: KindBudget, Outcome: "client_handoff"},
	}
	store := NewMemoryGatewayRoutingObservabilityStore()
	for index, observation := range fixedObservations {
		if err := store.Record(context.Background(), observation, int64(1_000+index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordBatch(context.Background(), []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 7},
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 5},
	}, 2_000); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), Observation{Kind: KindAttempt, Outcome: "completed"}, 1_500); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("version = %d, want 1", snapshot.Version)
	}
	if snapshot.RecordedEvents != int64(len(fixedObservations)+13) {
		t.Fatalf("recordedEvents = %d, want %d", snapshot.RecordedEvents, len(fixedObservations)+13)
	}
	if snapshot.UpdatedAtMs != 2_000 {
		t.Fatalf("乱序观测不得让 updatedAtMs 倒退: %d", snapshot.UpdatedAtMs)
	}
	if len(snapshot.Counters) != len(fixedObservations)+2 {
		t.Fatalf("counters = %v", snapshot.Counters)
	}
	for _, observation := range fixedObservations {
		key := GatewayRoutingObservationMetricKey(observation)
		if snapshot.Counters[key] != 1 {
			t.Fatalf("counters[%q] = %d, want 1", key, snapshot.Counters[key])
		}
	}
	if snapshot.Counters["attempt.started"] != 12 {
		t.Fatalf("批量写入必须按固定 key 合并计数: %d", snapshot.Counters["attempt.started"])
	}
	if snapshot.Counters["attempt.completed"] != 1 {
		t.Fatalf("attempt.completed = %d, want 1", snapshot.Counters["attempt.completed"])
	}
}

func TestMemoryStoreMetricCapacity(t *testing.T) {
	store := NewMemoryGatewayRoutingObservabilityStore()
	entries := make([]BatchEntry, 0, GatewayRoutingObservabilityMetricCapacity)
	for index := 0; index < GatewayRoutingObservabilityMetricCapacity; index += 1 {
		entries = append(entries, BatchEntry{Observation: Observation{Kind: KindAttempt, Outcome: fmt.Sprintf("outcome_%d", index)}, Count: 1})
	}
	if err := store.RecordBatch(context.Background(), entries, 1); err != nil {
		t.Fatal(err)
	}
	err := store.Record(context.Background(), Observation{Kind: KindAttempt, Outcome: "overflow"}, 2)
	if err == nil || err.Error() != "routing observability metric capacity exhausted" {
		t.Fatalf("容量耗尽必须原样报错，got %v", err)
	}
	// 已存在 key 的继续累计不受容量限制。
	if err := store.Record(context.Background(), Observation{Kind: KindAttempt, Outcome: "outcome_0"}, 3); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if snapshot.Counters["attempt.outcome_0"] != 2 || snapshot.RecordedEvents != GatewayRoutingObservabilityMetricCapacity+1 {
		t.Fatalf("容量耗尽后不得丢已存在 key 的计数: %+v", snapshot)
	}
}

func TestMemoryStoreSaturatingAdd(t *testing.T) {
	store := NewMemoryGatewayRoutingObservabilityStore()
	if err := store.Record(context.Background(), Observation{Kind: KindAttempt, Outcome: "started"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBatch(context.Background(), []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: MaxSafeInteger},
	}, 2); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if snapshot.Counters["attempt.started"] != MaxSafeInteger || snapshot.RecordedEvents != MaxSafeInteger {
		t.Fatalf("计数必须按 Number.MAX_SAFE_INTEGER 饱和: %+v", snapshot)
	}
}

func TestMemoryStoreSnapshotConcurrent(t *testing.T) {
	store := NewMemoryGatewayRoutingObservabilityStore()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker += 1 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := 0; index < 50; index += 1 {
				if err := store.Record(context.Background(), Observation{Kind: KindAttempt, Outcome: "started"}, int64(index+1)); err != nil {
					t.Error(err)
					return
				}
				if _, err := store.Snapshot(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	snapshot, _ := store.Snapshot(context.Background())
	if snapshot.Counters["attempt.started"] != 400 {
		t.Fatalf("并发计数丢失: %d", snapshot.Counters["attempt.started"])
	}
}

// ---------------------------------------------------------------------------
// Redis store（mock RedisCommandClient；Lua 行为另测 miniredis）
// ---------------------------------------------------------------------------

type mockCommandClient struct {
	mu      sync.Mutex
	evals   []mockEvalCall
	sends   []mockSendCall
	replies []interface{}
	evalErr error
	sendErr error
}

type mockEvalCall struct {
	script string
	keys   []string
	args   []string
}

type mockSendCall struct {
	args []string
}

func (client *mockCommandClient) Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.evals = append(client.evals, mockEvalCall{script: script, keys: append([]string(nil), keys...), args: append([]string(nil), args...)})
	if client.evalErr != nil {
		return nil, client.evalErr
	}
	return int64(1), nil
}

func (client *mockCommandClient) SendCommand(ctx context.Context, args ...string) (interface{}, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.sends = append(client.sends, mockSendCall{args: append([]string(nil), args...)})
	if client.sendErr != nil {
		return nil, client.sendErr
	}
	if len(client.replies) > 0 {
		reply := client.replies[0]
		client.replies = client.replies[1:]
		return reply, nil
	}
	return []interface{}{"metric:attempt.started", "12", "recordedEvents", "131", "updatedAtMs", "2000", "version", "1"}, nil
}

func newTestRedisObservabilityStore(t *testing.T, client RedisCommandClient) *RedisGatewayRoutingObservabilityStore {
	t.Helper()
	store, err := NewRedisGatewayRoutingObservabilityStore(client, "redis://state.example:6379/5", "dev", "gateway-routing-observability")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRedisStoreConstructor(t *testing.T) {
	cases := []struct {
		name      string
		redisURL  string
		nameArg   string
		namespace string
		wantErr   string
		wantKey   string
	}{
		{"missing-url", "  ", "", "dev", "performance routing observability 缺少 Redis URL", ""},
		{"bad-name", "redis://state.example:6379", "bad name!", "dev", "routing observability name 非法", ""},
		{"default-name", "redis://state.example:6379", "", "dev", "", "juhe-ai:dev:gateway-routing-observability:v1"},
		{"custom-name", "redis://state.example:6379", "Gateway-Routing-Observability", "dev", "", "juhe-ai:dev:gateway-routing-observability:v1"},
		{"namespaced-key", "redis://state.example:6379", "", "juhe-ai:dev", "", "juhe-ai:dev:gateway-routing-observability:v1"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, err := NewRedisGatewayRoutingObservabilityStore(&mockCommandClient{}, testCase.redisURL, testCase.namespace, testCase.nameArg)
			if testCase.wantErr != "" {
				if err == nil || err.Error() != testCase.wantErr {
					t.Fatalf("err = %v, want %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if store.key != testCase.wantKey {
				t.Fatalf("key = %q, want %q", store.key, testCase.wantKey)
			}
		})
	}
}

func TestRedisStoreRecordBatchScriptArgs(t *testing.T) {
	client := &mockCommandClient{}
	store := newTestRedisObservabilityStore(t, client)
	err := store.RecordBatch(context.Background(), []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 7},
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 5},
		{Observation: Observation{Kind: KindBudget, Outcome: "client_handoff"}, Count: 1},
	}, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(client.evals))
	}
	eval := client.evals[0]
	if eval.script != redisGatewayRoutingObservabilityRecordScript {
		t.Fatalf("Lua 脚本必须原样提交")
	}
	if len(eval.keys) != 1 || eval.keys[0] != "juhe-ai:dev:gateway-routing-observability:v1" {
		t.Fatalf("keys = %v", eval.keys)
	}
	// 相同 key 的计数必须先合并再进脚本。
	want := []string{"2000", "512", "attempt.started", "12", "budget.client_handoff", "1"}
	if strings.Join(eval.args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v, want %v", eval.args, want)
	}
}

func TestRedisStoreRecordBatchValidationBeforeEval(t *testing.T) {
	client := &mockCommandClient{}
	store := newTestRedisObservabilityStore(t, client)
	err := store.RecordBatch(context.Background(), []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 1},
		{Observation: Observation{Kind: KindAttempt, Outcome: "completed"}, Count: 0},
	}, 1_000)
	if err == nil || !strings.Contains(err.Error(), "正安全整数") {
		t.Fatalf("err = %v", err)
	}
	if len(client.evals) != 0 {
		t.Fatalf("校验失败不得触达 Redis")
	}
	if err := store.RecordBatch(context.Background(), nil, 1_000); err != nil {
		t.Fatalf("空批次必须是 no-op，got %v", err)
	}
	if len(client.evals) != 0 {
		t.Fatalf("空批次不得触达 Redis")
	}
}

func TestRedisStoreSnapshot(t *testing.T) {
	client := &mockCommandClient{}
	store := newTestRedisObservabilityStore(t, client)
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || snapshot.RecordedEvents != 131 || snapshot.UpdatedAtMs != 2000 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Counters["attempt.started"] != 12 {
		t.Fatalf("counters = %v", snapshot.Counters)
	}
	if len(client.sends) != 1 || strings.Join(client.sends[0].args, "|") != "HGETALL|juhe-ai:dev:gateway-routing-observability:v1" {
		t.Fatalf("sends = %v", client.sends)
	}
}

func TestRedisStoreSnapshotIgnoresNonMetricFieldsAndGarbage(t *testing.T) {
	client := &mockCommandClient{}
	client.sends = nil
	store := newTestRedisObservabilityStore(t, client)
	// 覆盖 finiteCount 的全部归零分支与对象/数组两种回复形状。
	cases := []struct {
		name  string
		reply interface{}
		want  Snapshot
	}{
		{
			"array-reply",
			[]interface{}{"metric:a", "5", "metric:bad", "-3", "metric:float", "1.5", "metric:empty", "", "metric:missing-value", "text", "recordedEvents", "9007199254740991", "updatedAtMs", "not-a-number", "version", "1"},
			// 2^53-1 可被 float64 精确表示且在 safe 范围内（与 JS Number 一致）。
			Snapshot{Version: 1, RecordedEvents: MaxSafeInteger, UpdatedAtMs: 0, Counters: map[string]int64{"a": 5, "bad": 0, "float": 0, "empty": 0, "missing-value": 0}},
		},
		{
			"beyond-safe-boundary",
			[]interface{}{"metric:a", "9007199254740992", "recordedEvents", "9007199254740993", "updatedAtMs", "1"},
			// 2^53 及以上超出 safe 范围 → 0。
			Snapshot{Version: 1, RecordedEvents: 0, UpdatedAtMs: 1, Counters: map[string]int64{"a": 0}},
		},
		{
			"object-reply",
			map[string]interface{}{"metric:a": "7", "recordedEvents": "1e3", "updatedAtMs": "200"},
			Snapshot{Version: 1, RecordedEvents: 1000, UpdatedAtMs: 200, Counters: map[string]int64{"a": 7}},
		},
		{
			"nil-reply",
			nil,
			Snapshot{Version: 1, RecordedEvents: 0, UpdatedAtMs: 0, Counters: map[string]int64{}},
		},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client.mu.Lock()
			client.sendErr = nil
			client.sends = nil
			// 通过替换 replies 队列实现逐次返回。
			client.replies = append(client.replies[:0], testCase.reply)
			client.mu.Unlock()
			snapshot, err := store.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.RecordedEvents != testCase.want.RecordedEvents || snapshot.UpdatedAtMs != testCase.want.UpdatedAtMs {
				t.Fatalf("snapshot = %+v, want %+v", snapshot, testCase.want)
			}
			if len(snapshot.Counters) != len(testCase.want.Counters) {
				t.Fatalf("counters = %v, want %v", snapshot.Counters, testCase.want.Counters)
			}
			for key, want := range testCase.want.Counters {
				if snapshot.Counters[key] != want {
					t.Fatalf("counters[%q] = %d, want %d", key, snapshot.Counters[key], want)
				}
			}
			_ = index
		})
	}
}

func TestRedisStoreSnapshotPropagatesCommandError(t *testing.T) {
	client := &mockCommandClient{}
	client.sendErr = errors.New("state redis unavailable")
	store := newTestRedisObservabilityStore(t, client)
	if _, err := store.Snapshot(context.Background()); err == nil {
		t.Fatal("性能模式不得静默回退本机数据")
	}
}

// ---------------------------------------------------------------------------
// miniredis 上的 Lua 脚本闭环（事件计数与更新时间必须在同一 Lua 内提交）
// ---------------------------------------------------------------------------

func TestRedisStoreRecordScriptAgainstMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(client), "redis://"+server.Addr(), "dev", "gateway-routing-observability")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Record(ctx, Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"}, 1_000); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBatch(ctx, []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 7},
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: 5},
	}, 500); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RecordedEvents != 13 || snapshot.UpdatedAtMs != 1_000 {
		t.Fatalf("乱序观测不得让 updatedAtMs 倒退: %+v", snapshot)
	}
	if snapshot.Counters["circuit.transition.suspect.open.transport"] != 1 || snapshot.Counters["attempt.started"] != 12 {
		t.Fatalf("counters = %v", snapshot.Counters)
	}
	// version 字段由同一 Lua 写入。
	raw, err := client.HGetAll(ctx, "juhe-ai:dev:gateway-routing-observability:v1").Result()
	if err != nil {
		t.Fatal(err)
	}
	if raw["version"] != "1" {
		t.Fatalf("version = %q, want 1", raw["version"])
	}
}

func TestRedisStoreRecordScriptCapacityAgainstMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(client), "redis://"+server.Addr(), "dev", "gateway-routing-observability")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entries := make([]BatchEntry, 0, GatewayRoutingObservabilityMetricCapacity)
	for index := 0; index < GatewayRoutingObservabilityMetricCapacity; index += 1 {
		entries = append(entries, BatchEntry{Observation: Observation{Kind: KindAttempt, Outcome: fmt.Sprintf("outcome_%d", index)}, Count: 1})
	}
	if err := store.RecordBatch(ctx, entries, 1); err != nil {
		t.Fatal(err)
	}
	err = store.Record(ctx, Observation{Kind: KindAttempt, Outcome: "overflow"}, 2)
	if err == nil || !strings.Contains(err.Error(), "routing observability metric capacity exhausted") {
		t.Fatalf("容量耗尽必须由 Lua 拒绝，got %v", err)
	}
}

func TestRedisStoreRecordScriptSaturationAgainstMiniredis(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(client), "redis://"+server.Addr(), "dev", "gateway-routing-observability")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.RecordBatch(ctx, []BatchEntry{
		{Observation: Observation{Kind: KindAttempt, Outcome: "started"}, Count: MaxSafeInteger},
	}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, Observation{Kind: KindAttempt, Outcome: "started"}, 2); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Counters["attempt.started"] != MaxSafeInteger || snapshot.RecordedEvents != MaxSafeInteger {
		t.Fatalf("Redis 计数必须按 max_safe_integer 饱和: %+v", snapshot)
	}
}
