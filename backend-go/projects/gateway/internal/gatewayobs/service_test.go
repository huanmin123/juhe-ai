package gatewayobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// mock
// ---------------------------------------------------------------------------

type capturedLog struct {
	level  string
	fields map[string]interface{}
	msg    string
}

type mockLogger struct {
	mu   sync.Mutex
	logs []capturedLog
}

func (logger *mockLogger) Info(fields map[string]interface{}, msg string) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.logs = append(logger.logs, capturedLog{level: "info", fields: fields, msg: msg})
}

func (logger *mockLogger) Warn(fields map[string]interface{}, msg string) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.logs = append(logger.logs, capturedLog{level: "warn", fields: fields, msg: msg})
}

func (logger *mockLogger) Debug(fields map[string]interface{}, msg string) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.logs = append(logger.logs, capturedLog{level: "debug", fields: fields, msg: msg})
}

func (logger *mockLogger) count(level string, event string) int {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	total := 0
	for _, item := range logger.logs {
		if item.level == level && item.fields["event"] == event {
			total += 1
		}
	}
	return total
}

type mockStore struct {
	mu        sync.Mutex
	batches   [][]BatchEntry
	singles   []Observation
	nows      []int64
	recordErr error
}

func (store *mockStore) Record(ctx context.Context, observation Observation, nowMs int64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.singles = append(store.singles, observation)
	store.nows = append(store.nows, nowMs)
	return store.recordErr
}

func (store *mockStore) RecordBatch(ctx context.Context, entries []BatchEntry, nowMs int64) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.batches = append(store.batches, entries)
	store.nows = append(store.nows, nowMs)
	return store.recordErr
}

func (store *mockStore) Snapshot(ctx context.Context) (Snapshot, error) {
	return Snapshot{Version: 1}, nil
}

// synchronousObserver 构造确定性 observer：schedule 只记录代数（等价
// microtask 入队），由 pumpFlushes 显式冲刷——保持「同一同步段内多次观察
// 只批量 flush 一次」的 Node 语义。
func synchronousObserver(store Store, logger Logger, now func() int64, contextSource func() context.Context) (*Observer, *flushPump) {
	observer := NewObserver(ObserverOptions{Store: store, Logger: logger, Now: now, ContextSource: contextSource})
	pump := &flushPump{}
	observer.schedule = func(target *Observer, generation uint64) {
		pump.generations = append(pump.generations, generation)
	}
	return observer, pump
}

type flushPump struct {
	generations []uint64
}

func (pump *flushPump) count() int { return len(pump.generations) }

func (pump *flushPump) drain(observer *Observer) {
	generations := pump.generations
	pump.generations = nil
	for _, generation := range generations {
		observer.FlushPending(generation)
	}
}

// routingObserverPort 是 gatewayrouting.RoutingObserver 的同形本地声明，
// 用于结构化签名核对（不得 import 该包）。
type routingObserverPort interface {
	ObserveRouting(kind, outcome string, nowMs int64)
}

var _ routingObserverPort = (*Observer)(nil)

// ---------------------------------------------------------------------------
// dispatch summary（字段完整性 + 取样/丢弃边界）
// ---------------------------------------------------------------------------

func TestCaptureRequestDispatchSummaryFieldCompleteness(t *testing.T) {
	cases := []struct {
		name        string
		observation Observation
		check       func(t *testing.T, summary *GatewayRoutingDispatchSummary)
	}{
		{"attempt-started", Observation{Kind: KindAttempt, Outcome: "started"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.AttemptsStarted, 1, "attemptsStarted")
			wantField(t, s.AttemptsFailed, 0, "attemptsFailed")
		}},
		{"attempt-completed", Observation{Kind: KindAttempt, Outcome: "completed"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.AttemptsCompleted, 1, "attemptsCompleted")
		}},
		{"attempt-failure-outcomes", Observation{Kind: KindAttempt, Outcome: "client_canceled"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.AttemptsFailed, 1, "attemptsFailed")
		}},
		{"transition-different", Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitTransitions, 1, "circuitTransitions")
		}},
		{"transition-same", Observation{Kind: KindCircuitTransition, From: "OPEN", To: "OPEN", Source: "recovery"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitTransitions, 0, "同 phase 不得冒充状态转换")
		}},
		{"mutation-skip", Observation{Kind: KindCircuitMutation, Operation: "acquire_canary", Status: "lease_mismatch"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitSkips, 1, "circuitSkips")
			wantField(t, s.CircuitCasConflicts, 0, "circuitCasConflicts")
		}},
		{"mutation-cas", Observation{Kind: KindCircuitMutation, Operation: "acquire_confirmation", Status: "stale_dispatch_revision"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitSkips, 1, "circuitSkips")
			wantField(t, s.CircuitCasConflicts, 1, "circuitCasConflicts")
		}},
		{"mutation-lease-rejected", Observation{Kind: KindCircuitMutation, Operation: "acquire_confirmation", Status: "stale_generation", LeaseKind: "confirmation"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitCasConflicts, 1, "circuitCasConflicts")
			wantField(t, s.CircuitLeasesRejected, 1, "circuitLeasesRejected")
			wantField(t, s.CircuitLeasesAcquired, 0, "circuitLeasesAcquired")
		}},
		{"mutation-lease-acquired", Observation{Kind: KindCircuitMutation, Operation: "acquire_canary", Status: "applied", LeaseKind: "half_open"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitSkips, 0, "circuitSkips")
			wantField(t, s.CircuitLeasesAcquired, 1, "circuitLeasesAcquired")
		}},
		{"mutation-state-mismatch-cas", Observation{Kind: KindCircuitMutation, Operation: "complete_canary", Status: "state_mismatch"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitCasConflicts, 1, "circuitCasConflicts")
		}},
		{"dispatch-skip", Observation{Kind: KindCircuitDispatch, Outcome: "blocked", Phase: "OPEN"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.CircuitSkips, 1, "circuitSkips")
		}},
		{"hot-quality-idempotent", Observation{Kind: KindHotQualityMutation, Operation: "terminal", Status: "idempotent"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.HotQualityDeduplications, 1, "hotQualityDeduplications")
		}},
		{"hot-quality-conflict", Observation{Kind: KindHotQualityMutation, Operation: "attempt", Status: "conflict"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.HotQualityConflicts, 1, "hotQualityConflicts")
		}},
		{"exploration-reserved", Observation{Kind: KindExploration, Outcome: "reserved"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.ExplorationsReserved, 1, "explorationsReserved")
		}},
		{"exploration-dispatched", Observation{Kind: KindExploration, Outcome: "dispatched"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.ExplorationsDispatched, 1, "explorationsDispatched")
		}},
		{"tier-escape-applied", Observation{Kind: KindTierEscape, Outcome: "applied"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.TierEscapes, 1, "tierEscapes")
		}},
		{"tier-escape-blocked", Observation{Kind: KindTierEscape, Outcome: "blocked"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.TierEscapes, 0, "tierEscapes")
		}},
		{"budget-wall-exhausted", Observation{Kind: KindBudget, Outcome: "wall_exhausted"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.WallBudgetExhausted, 1, "wallBudgetExhausted")
		}},
		{"budget-precommit-clipped", Observation{Kind: KindBudget, Outcome: "precommit_clipped"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.PrecommitClipped, 1, "precommitClipped")
		}},
		{"budget-client-handoff", Observation{Kind: KindBudget, Outcome: "client_handoff"}, func(t *testing.T, s *GatewayRoutingDispatchSummary) {
			wantField(t, s.ClientHandoffs, 1, "clientHandoffs")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := WithDispatchSummaryHolder(context.Background())
			captureRequestDispatchSummary(ctx, testCase.observation)
			summary := DispatchSummaryFromContext(ctx)
			if summary == nil {
				t.Fatal("summary 必须被惰性创建")
			}
			wantField(t, summary.ObservedEvents, 1, "observedEvents")
			testCase.check(t, summary)
			// 摘要不得携带敏感或无关字段。
			serialized, err := json.Marshal(summary)
			if err != nil {
				t.Fatal(err)
			}
			if containsSensitiveSummaryMarker(string(serialized)) {
				t.Fatalf("summary 不得包含 authorization/api key/clientIp 等字段: %s", serialized)
			}
		})
	}
}

func containsSensitiveSummaryMarker(content string) bool {
	text := strings.ToLower(content)
	for _, marker := range []string{"authorization", "apikey", "api_key", "clientip", "response", "body", "credential"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func wantField(t *testing.T, got int64, want int64, name string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}

func TestCaptureRequestDispatchSummaryDropsBeyondLimit(t *testing.T) {
	ctx := WithDispatchSummaryHolder(context.Background())
	for index := 0; index < 128; index += 1 {
		captureRequestDispatchSummary(ctx, Observation{Kind: KindAttempt, Outcome: "started"})
	}
	for index := 0; index < 3; index += 1 {
		captureRequestDispatchSummary(ctx, Observation{Kind: KindBudget, Outcome: "client_handoff"})
	}
	summary := DispatchSummaryFromContext(ctx)
	if summary.ObservedEvents != 128 {
		t.Fatalf("每请求 dispatch summary 必须有固定事件上限: %d", summary.ObservedEvents)
	}
	if summary.DroppedEvents != 3 {
		t.Fatalf("超过上限的观察只累计 dropped，不扩展摘要: %d", summary.DroppedEvents)
	}
	if summary.ClientHandoffs != 0 {
		t.Fatalf("丢弃的事件不得进入摘要字段: %d", summary.ClientHandoffs)
	}
}

func TestDispatchSummaryLazyCreation(t *testing.T) {
	ctx := WithDispatchSummaryHolder(context.Background())
	if DispatchSummaryFromContext(ctx) != nil {
		t.Fatal("观察发生前 summary 必须保持未创建（等价 Node ??= 语义）")
	}
	if DispatchSummaryFromContext(context.Background()) != nil {
		t.Fatal("无请求上下文时不得采集 summary")
	}
}

// ---------------------------------------------------------------------------
// Observer：批量 flush、日志、失败节流
// ---------------------------------------------------------------------------

func TestObserverBatchesAndMergesByMetricKey(t *testing.T) {
	store := &mockStore{}
	logger := &mockLogger{}
	observer, pump := synchronousObserver(store, logger, func() int64 { return 42 }, nil)

	observer.Observe(Observation{Kind: KindAttempt, Outcome: "started"}, 1_000)
	observer.Observe(Observation{Kind: KindAttempt, Outcome: "started"}, 1_100)
	observer.Observe(Observation{Kind: KindBudget, Outcome: "client_handoff"}, 1_200)

	if pump.count() != 1 {
		t.Fatalf("同一同步段必须只调度一次 flush，got %d", pump.count())
	}
	if len(store.singles) != 0 {
		t.Fatalf("observe 路径不得直写 store")
	}
	if len(store.batches) != 0 {
		t.Fatalf("microtask 等价调度不得提前 flush")
	}
	pump.drain(observer)
	if len(store.batches) != 1 {
		t.Fatalf("同一轮调度必须只 flush 一次，got %d", len(store.batches))
	}
	if store.nows[0] != 1_200 {
		t.Fatalf("flush 必须取 pending 最大 nowMs，got %d", store.nows[0])
	}
	if len(store.batches[0]) != 2 {
		t.Fatalf("batch = %v", store.batches[0])
	}
	for _, entry := range store.batches[0] {
		switch GatewayRoutingObservationMetricKey(entry.Observation) {
		case "attempt.started":
			if entry.Count != 2 {
				t.Fatalf("attempt.started count = %d, want 2", entry.Count)
			}
		case "budget.client_handoff":
			if entry.Count != 1 {
				t.Fatalf("budget.client_handoff count = %d, want 1", entry.Count)
			}
		}
	}
	// 首个观察作为代表事件被保留。
	if store.batches[0][0].Observation.Outcome != "started" {
		t.Fatalf("代表事件必须保留: %+v", store.batches[0][0])
	}
}

func TestObserverNormalizesInvalidNow(t *testing.T) {
	store := &mockStore{}
	clockNow := int64(9_000)
	observer, pump := synchronousObserver(store, &mockLogger{}, func() int64 { return clockNow }, nil)
	observer.Observe(Observation{Kind: KindAttempt, Outcome: "started"}, -5)
	pump.drain(observer)
	if store.nows[0] != 9_000 {
		t.Fatalf("非法 nowMs 必须回退注入时钟，got %d", store.nows[0])
	}
}

func TestObserverAbandonDiscardsPendingFlush(t *testing.T) {
	store := &mockStore{}
	observer := NewObserver(ObserverOptions{Store: store, Logger: NopLogger(), Now: func() int64 { return 1 }})
	flushed := make(chan struct{})
	observer.schedule = func(target *Observer, generation uint64) {
		target.abandon() // 模拟 reset 先于 flush 执行
		target.FlushPending(generation)
		close(flushed)
	}
	observer.Observe(Observation{Kind: KindAttempt, Outcome: "started"}, 100)
	<-flushed
	if len(store.batches) != 0 {
		t.Fatalf("reset 后在途 flush 必须作废: %v", store.batches)
	}
}

func TestObserverRoutingLogBranches(t *testing.T) {
	cases := []struct {
		name           string
		observation    Observation
		wantLevel      string
		wantMsg        string
		wantFields     map[string]interface{}
		absentField    string
		wantTransition string // 期望的 to==='OPEN' 日志级别（warn/info），空串表示不校验
	}{
		{
			"transition-to-open-warn",
			Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"},
			"warn", "账户短电路状态转换",
			map[string]interface{}{"event": "gateway_account_circuit_transition", "from": "SUSPECT", "to": "OPEN", "source": "transport"},
			"", "",
		},
		{
			"transition-to-half-open-info",
			Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "HALF_OPEN", Source: "explicit_policy"},
			"info", "账户短电路状态转换",
			map[string]interface{}{"event": "gateway_account_circuit_transition", "from": "SUSPECT", "to": "HALF_OPEN", "source": "explicit_policy"},
			"", "",
		},
		{
			"transition-same-phase-silent",
			Observation{Kind: KindCircuitTransition, From: "OPEN", To: "OPEN", Source: "recovery"},
			"", "", nil, "", "",
		},
		{
			"mutation-skip-debug-with-lease",
			Observation{Kind: KindCircuitMutation, Operation: "acquire_confirmation", Status: "stale_generation", LeaseKind: "confirmation"},
			"debug", "账户短电路派发被跳过",
			map[string]interface{}{"event": "gateway_account_circuit_dispatch_skipped", "operation": "acquire_confirmation", "status": "stale_generation", "leaseKind": "confirmation"},
			"", "",
		},
		{
			"mutation-skip-debug-without-lease",
			Observation{Kind: KindCircuitMutation, Operation: "replace_revision", Status: "not_due"},
			"debug", "账户短电路派发被跳过",
			map[string]interface{}{"event": "gateway_account_circuit_dispatch_skipped", "operation": "replace_revision", "status": "not_due"},
			"leaseKind", "",
		},
		{
			"mutation-applied-silent",
			Observation{Kind: KindCircuitMutation, Operation: "replace_revision", Status: "applied"},
			"", "", nil, "", "",
		},
		{
			"circuit-dispatch-debug",
			Observation{Kind: KindCircuitDispatch, Outcome: "blocked", Phase: "OPEN"},
			"debug", "账户短电路派发被跳过",
			map[string]interface{}{"event": "gateway_account_circuit_dispatch_skipped", "outcome": "blocked", "phase": "OPEN"},
			"", "",
		},
		{
			"attempt-silent",
			Observation{Kind: KindAttempt, Outcome: "started"},
			"", "", nil, "", "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logger := &mockLogger{}
			observer, _ := synchronousObserver(&mockStore{}, logger, func() int64 { return 1 }, nil)
			observer.Observe(testCase.observation, 1)
			if testCase.wantLevel == "" {
				if len(logger.logs) != 0 {
					t.Fatalf("不得输出路由日志: %+v", logger.logs)
				}
				return
			}
			if len(logger.logs) != 1 {
				t.Fatalf("logs = %+v", logger.logs)
			}
			entry := logger.logs[0]
			if entry.level != testCase.wantLevel || entry.msg != testCase.wantMsg {
				t.Fatalf("log = %+v, want level %q msg %q", entry, testCase.wantLevel, testCase.wantMsg)
			}
			for key, want := range testCase.wantFields {
				if entry.fields[key] != want {
					t.Fatalf("fields[%q] = %v, want %v", key, entry.fields[key], want)
				}
			}
			if testCase.absentField != "" {
				if _, exists := entry.fields[testCase.absentField]; exists {
					t.Fatalf("fields 不得包含缺省字段 %q（Pino 丢弃 undefined）", testCase.absentField)
				}
			}
		})
	}
}

func TestObserverWriteFailureThrottle(t *testing.T) {
	store := &mockStore{recordErr: errors.New("state write failed")}
	logger := &mockLogger{}
	observer, _ := synchronousObserver(store, logger, func() int64 { return 0 }, nil)

	// 边界：nowMs - 0 >= 30000 才允许首条失败日志。
	if observer.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindAttempt, Outcome: "started"}, 29_999) {
		t.Fatal("store 失败必须返回 false")
	}
	if got := logger.count("warn", "gateway_routing_observability_write_failed"); got != 0 {
		t.Fatalf("节流窗口内不得输出失败日志，got %d", got)
	}
	if observer.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindAttempt, Outcome: "started"}, 30_000) {
		t.Fatal("store 失败必须返回 false")
	}
	if got := logger.count("warn", "gateway_routing_observability_write_failed"); got != 1 {
		t.Fatalf("失败日志 = %d, want 1", got)
	}
	entry := logger.logs[0]
	if entry.fields["observationKind"] != "attempt" {
		t.Fatalf("observationKind = %v", entry.fields["observationKind"])
	}
	if _, ok := entry.fields["error"].(error); !ok {
		t.Fatalf("error 字段必须保留原始错误: %v", entry.fields["error"])
	}
	if entry.msg != "网关路由观测写入失败" {
		t.Fatalf("msg = %q", entry.msg)
	}
	// 30s 节流窗口内再次失败不重复输出。
	observer.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindBudget, Outcome: "client_handoff"}, 59_999)
	if got := logger.count("warn", "gateway_routing_observability_write_failed"); got != 1 {
		t.Fatalf("节流窗口内不得重复输出，got %d", got)
	}
	observer.RecordGatewayRoutingObservation(context.Background(), Observation{Kind: KindBudget, Outcome: "client_handoff"}, 60_000)
	if got := logger.count("warn", "gateway_routing_observability_write_failed"); got != 2 {
		t.Fatalf("跨过窗口必须再次输出，got %d", got)
	}

	// 批量失败使用独立文案与 batch kind。
	store.batches = nil
	observer.Observe(Observation{Kind: KindAttempt, Outcome: "started"}, 120_000)
	observer.FlushPending(observer.CurrentFlushGeneration())
	if got := logger.count("warn", "gateway_routing_observability_write_failed"); got != 3 {
		t.Fatalf("批量失败必须输出，got %d", got)
	}
	last := logger.logs[len(logger.logs)-1]
	if last.fields["observationKind"] != "batch" || last.msg != "网关路由观测批量写入失败" {
		t.Fatalf("批量失败文案必须对齐: %+v", last)
	}
}

func TestRecordGatewayRoutingObservationCapturesSummaryAndLogs(t *testing.T) {
	store := &mockStore{}
	logger := &mockLogger{}
	observer, _ := synchronousObserver(store, logger, func() int64 { return 5 }, nil)
	ctx := WithDispatchSummaryHolder(context.Background())
	if !observer.RecordGatewayRoutingObservation(ctx, Observation{Kind: KindCircuitTransition, From: "SUSPECT", To: "OPEN", Source: "transport"}, 1_000) {
		t.Fatal("成功写入必须返回 true")
	}
	if len(store.singles) != 1 || store.nows[0] != 1_000 {
		t.Fatalf("record 路径必须直写 store: singles=%d", len(store.singles))
	}
	summary := DispatchSummaryFromContext(ctx)
	if summary.CircuitTransitions != 1 {
		t.Fatalf("record 路径必须采集 summary: %+v", summary)
	}
	if got := logger.count("warn", "gateway_account_circuit_transition"); got != 1 {
		t.Fatalf("record 路径必须输出路由日志，got %d", got)
	}
}

// ---------------------------------------------------------------------------
// 包级单例与 store identity
// ---------------------------------------------------------------------------

func TestRoutingObservabilityStoreIdentity(t *testing.T) {
	identity, err := RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"})
	if err != nil || identity != "standalone:memory" {
		t.Fatalf("identity = %q err = %v", identity, err)
	}
	digest := sha256.Sum256([]byte("redis://state.example:6379/5"))
	want := "performance:redis:" + hex.EncodeToString(digest[:])
	identity, err = RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis", RedisStateURL: "redis://state.example:6379/5"})
	if err != nil || identity != want {
		t.Fatalf("identity = %q err = %v, want %q", identity, err, want)
	}
	// identity 校验先行：performance+memory 必须报 identity 错误（与 Node 一致）。
	_, err = RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"})
	if err == nil || err.Error() != "performance routing observability 要求 Redis runtime state" {
		t.Fatalf("err = %v", err)
	}
	_, err = RoutingObservabilityStoreIdentity(RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "redis", RedisStateURL: "  "})
	if err == nil || err.Error() != "performance routing observability 要求 Redis runtime state" {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildStoreConfigErrors(t *testing.T) {
	cases := []struct {
		name   string
		config RuntimeDriverConfig
		want   string
	}{
		{"standalone-non-memory", RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "redis"}, "standalone routing observability 要求 memory runtime state driver"},
		{"performance-non-redis", RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"}, "performance routing observability 要求 Redis runtime state"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := buildRoutingObservabilityStore(context.Background(), testCase.config)
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestGetGatewayRoutingObservabilityStoreSingleton(t *testing.T) {
	ResetGatewayRoutingObservabilityForTest()
	defer ResetGatewayRoutingObservabilityForTest()
	config := RuntimeDriverConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"}
	first, err := GetGatewayRoutingObservabilityStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GetGatewayRoutingObservabilityStore(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("同一 identity 必须复用单例 store")
	}
	observer, err := GetGatewayRoutingObservability(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := observer.store.(*MemoryGatewayRoutingObservabilityStore); !ok {
		t.Fatalf("standalone 必须使用 memory store, got %T", observer.store)
	}
	snapshot, err := GetGatewayRoutingObservabilitySnapshot(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestGetGatewayRoutingObservabilitySnapshotWithoutStore(t *testing.T) {
	ResetGatewayRoutingObservabilityForTest()
	defer ResetGatewayRoutingObservabilityForTest()
	_, err := GetGatewayRoutingObservabilitySnapshot(context.Background(), RuntimeDriverConfig{RuntimeMode: "performance", RuntimeStateDriver: "memory"})
	if err == nil || err.Error() != "performance routing observability 要求 Redis runtime state" {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 观察入口并发（-race 采样点）
// ---------------------------------------------------------------------------

func TestObserverConcurrentObserveAndFlush(t *testing.T) {
	store := &mockStore{}
	logger := &mockLogger{}
	observer := NewObserver(ObserverOptions{Store: store, Logger: logger, Now: func() int64 { return 1 }})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker += 1 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := 0; index < 20; index += 1 {
				observer.ObserveRouting("budget", "client_handoff", int64(1_000+worker))
				observer.ObserveGatewayRouting(RoutingObservation{Kind: "attempt", Outcome: "started"})
				_ = observer.RecordGatewayRoutingObservation(nil, Observation{Kind: KindAttempt, Outcome: "completed"}, 2_000)
				observer.FlushPending(observer.CurrentFlushGeneration())
			}
		}(worker)
	}
	wg.Wait()
	observer.FlushPending(observer.CurrentFlushGeneration())
}
