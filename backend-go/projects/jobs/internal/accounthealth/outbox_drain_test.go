package accounthealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// account_health_probe_request_outbox 消费面单测（去跨进程战役第二刀）：
// drain 复刻被删 HTTP 派发 handler 的语义——claim 幂等、J1 冻结范围外的
// fence=unknown 结算、范围内经 runExplicitRequest 全链到 AppendOutcome 落库
// （mock 探针），逐行失败保持 pending。

// probeOutboxMemoryStore 是 ProbeRequestOutboxStore 的内存实现：行状态可见，
// complete 调用次数可断言。
type probeOutboxMemoryStore struct {
	mu       sync.Mutex
	pending  []ProbeOutboxRow
	consumed []string
	complete int
}

func (s *probeOutboxMemoryStore) ClaimPendingProbeRequests(_ context.Context, limit int, _ time.Time) ([]ProbeOutboxRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > len(s.pending) {
		limit = len(s.pending)
	}
	rows := append([]ProbeOutboxRow{}, s.pending[:limit]...)
	return rows, nil
}

func (s *probeOutboxMemoryStore) CompleteProbeRequest(_ context.Context, requestID string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.complete++
	kept := s.pending[:0]
	for _, row := range s.pending {
		if row.RequestID != requestID {
			kept = append(kept, row)
		}
	}
	s.pending = kept
	s.consumed = append(s.consumed, requestID)
	return true, nil
}

func (s *probeOutboxMemoryStore) claimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// probeDrainBoundary 是 ProbeRequestBoundary 的 map 实现。
type probeDrainBoundary struct {
	facts map[string][3]int64
	ok    map[string]bool
}

func (b probeDrainBoundary) CurrentProbeInput(_ context.Context, accountID string) (configRevision, dispatchRevision, inputVersion int64, ok bool, err error) {
	if !b.ok[accountID] {
		return 0, 0, 0, false, nil
	}
	facts := b.facts[accountID]
	return facts[0], facts[1], facts[2], true, nil
}

// probeDrainDirectReader 是 directInputLoader 的单账户实现（LoadDue 恒空，
// drain 与 runCycle 的显式请求路径都只走 LoadAccount）。
type probeDrainDirectReader struct {
	input Input
}

func (r probeDrainDirectReader) LoadDue(context.Context, int) ([]Input, error) { return nil, nil }
func (r probeDrainDirectReader) LoadAccount(context.Context, string) ([]Input, error) {
	return []Input{r.input}, nil
}

func newDrainRunner(t *testing.T, secret string, outbox *probeOutboxMemoryStore, boundary ProbeRequestBoundary, reader directInputLoader) *Runner {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "/account-health.sqlite3"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner := NewRunner(Config{
		InputDirectory:   t.TempDir(),
		InputKeys:        map[string][]byte{"current": []byte("drain-input-signing-key-123")},
		CredentialSecret: secret,
		ProbeTimeout:     time.Second,
		MaxResponseBytes: 1024,
		MaxConcurrency:   1,
		Now:              time.Now,
	}, store, nil)
	runner.directInputReader = reader
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: outbox, Boundary: boundary})
	return runner
}

func drainLease(t *testing.T, runner *Runner) OwnerLease {
	t.Helper()
	lease, acquired, err := runner.store.AcquireOwnerLease(context.Background(), "drain-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	return lease
}

// TestDrainProbeOutboxOutOfScopeSettlesFenceUnknown：J1 冻结范围外的账户
// （无 input epoch）不发布探针、不写 outcome，仍结算 source fence = unknown
// （被删 HTTP handler 的范围外语义），行幂等落 consumed。
func TestDrainProbeOutboxOutOfScopeSettlesFenceUnknown(t *testing.T) {
	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{{
		RequestID:   "j1-out-of-scope",
		AccountID:   "account-1",
		Reason:      "request_failure",
		SourceFence: &SourceFence{StateKey: "sk", AccountID: "account-1", SourceGeneration: 1, SourceFenceID: "fence-1", RuntimeKey: "rt", ProbeGeneration: 3, ConfigRevision: 1},
		Deadline:    time.Now().UTC().Add(time.Minute),
	}}}
	settled := make(chan string, 1)
	runner := newDrainRunner(t, "drain-secret", outbox, probeDrainBoundary{ok: map[string]bool{}}, nil)
	runner.probeDrain.SettleFence = func(_ context.Context, fence SourceFence, state string) error {
		if fence.SourceFenceID != "fence-1" {
			t.Fatalf("settled fence = %+v", fence)
		}
		settled <- state
		return nil
	}
	lease := drainLease(t, runner)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	select {
	case state := <-settled:
		if state != "unknown" {
			t.Fatalf("fence state = %s want unknown", state)
		}
	default:
		t.Fatal("source fence was not settled")
	}
	if outbox.claimCount() != 0 {
		t.Fatalf("row stayed pending: %d", outbox.claimCount())
	}
	if len(outbox.consumed) != 1 || outbox.consumed[0] != "j1-out-of-scope" {
		t.Fatalf("consumed = %v", outbox.consumed)
	}
	// 范围外不写任何 outcome。
	var count int
	if err := runner.store.db.QueryRow(`SELECT COUNT(*) FROM account_health_outcomes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("out-of-scope drain wrote %d outcomes", count)
	}
}

// TestDrainProbeOutboxInScopeRunsExplicitRequest：范围内行组装显式请求走
// runExplicitRequest 全链——mock 探针成功 → request_failure_reason 的
// mutate_account=(no fence) 请求 → AppendOutcome 落库 → 行 consumed；
// 重复 drain 靠 HasRequest 幂等不重复写 outcome。
func TestDrainProbeOutboxInScopeRunsExplicitRequest(t *testing.T) {
	secret := "drain-credential-secret"
	probeHits := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		probeHits++
		mu.Unlock()
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	input := testInput(server.URL, "chat_json")
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-test"}`)}}}
	input.KeySetFingerprint = "keyset-1"
	input.Eligibility = Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true}
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)}

	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{{
		RequestID: "j1-in-scope",
		AccountID: input.AccountID,
		Reason:    "request_failure",
		Deadline:  time.Now().UTC().Add(time.Minute),
	}}}
	runner := newDrainRunner(t, secret, outbox, probeDrainBoundary{
		facts: map[string][3]int64{input.AccountID: {input.ConfigRevision, input.DispatchRevision, input.InputVersion}},
		ok:    map[string]bool{input.AccountID: true},
	}, probeDrainDirectReader{input: input})
	lease := drainLease(t, runner)

	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	mu.Lock()
	hits := probeHits
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("probe hits = %d want exactly one real probe", hits)
	}
	if outbox.claimCount() != 0 || len(outbox.consumed) != 1 {
		t.Fatalf("row not consumed: pending=%d consumed=%v", outbox.claimCount(), outbox.consumed)
	}
	state, found, err := runner.store.LoadCurrentState(context.Background(), input.AccountID)
	if err != nil || !found {
		t.Fatalf("outcome missing: found=%t err=%v", found, err)
	}
	if state.Outcome != OutcomeSuccess || state.AccountStatus != "active" {
		t.Fatalf("explicit request outcome = %#v", state)
	}

	// 幂等回放：同一行再次入队（模拟消费前崩溃重启）→ HasRequest 短路，
	// 不再发探针、不重复写 outcome。
	outbox.pending = append(outbox.pending, ProbeOutboxRow{
		RequestID: "j1-in-scope",
		AccountID: input.AccountID,
		Reason:    "request_failure",
		Deadline:  time.Now().UTC().Add(time.Minute),
	})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("replay drain: %v", err)
	}
	mu.Lock()
	hits = probeHits
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("replay must not re-probe, hits = %d", hits)
	}
}

// TestDrainProbeOutboxRowFailureKeepsPending：boundary 读失败 → 行保持
// pending（下周期重试），其余行继续处理，drain 返回首个错误。
func TestDrainProbeOutboxRowFailureKeepsPending(t *testing.T) {
	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{{
		RequestID: "j1-poison",
		AccountID: "account-1",
		Reason:    "request_failure",
		Deadline:  time.Now().UTC().Add(time.Minute),
	}}}
	runner := newDrainRunner(t, "drain-secret", outbox, probeDrainBoundary{ok: map[string]bool{}}, nil)
	lease := drainLease(t, runner)
	runner.probeDrain.Boundary = failingBoundary{}
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err == nil {
		t.Fatal("drain must surface the boundary failure")
	}
	if outbox.claimCount() != 1 || len(outbox.consumed) != 0 {
		t.Fatalf("failed row must stay pending: pending=%d consumed=%v", outbox.claimCount(), outbox.consumed)
	}
}

type failingBoundary struct{}

func (failingBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	return 0, 0, 0, false, context.DeadlineExceeded
}

// TestDrainProbeOutboxFenceRevisionMismatch：fence 与当前账户 config revision
// 不一致（写行后账户变更）→ 按被删桥的 payload 断言失败收敛：不发布探针、
// 不写 outcome，确定性失败按已处理出队（保持 pending 只会形成毒丸）。
func TestDrainProbeOutboxFenceRevisionMismatch(t *testing.T) {
	input := testInput("http://127.0.0.1:9", "chat_json")
	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{{
		RequestID:   "j1-stale-fence",
		AccountID:   input.AccountID,
		Reason:      "request_failure",
		SourceFence: &SourceFence{StateKey: "sk", AccountID: input.AccountID, SourceGeneration: 1, SourceFenceID: "fence-1", RuntimeKey: "rt", ProbeGeneration: 3, ConfigRevision: 99},
		Deadline:    time.Now().UTC().Add(time.Minute),
	}}}
	runner := newDrainRunner(t, "drain-secret", outbox, probeDrainBoundary{
		facts: map[string][3]int64{input.AccountID: {input.ConfigRevision, input.DispatchRevision, input.InputVersion}},
		ok:    map[string]bool{input.AccountID: true},
	}, nil)
	lease := drainLease(t, runner)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("stale fence must converge without an error: %v", err)
	}
	if outbox.claimCount() != 0 || len(outbox.consumed) != 1 {
		t.Fatalf("stale fence row must be consumed: pending=%d consumed=%v", outbox.claimCount(), outbox.consumed)
	}
	var count int
	if err := runner.store.db.QueryRow(`SELECT COUNT(*) FROM account_health_outcomes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale fence drain wrote %d outcomes", count)
	}
}

// TestParseProbeOutboxSourceFence：行内 fence JSON 解析与空投影。
func TestParseProbeOutboxSourceFence(t *testing.T) {
	if fence, err := ParseProbeOutboxSourceFence(""); err != nil || fence != nil {
		t.Fatalf("empty fence = %#v, %v", fence, err)
	}
	raw, err := json.Marshal(SourceFence{StateKey: "sk", AccountID: "a", SourceGeneration: 1, SourceFenceID: "f", RuntimeKey: "rt", ProbeGeneration: 2, ConfigRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := ParseProbeOutboxSourceFence(string(raw))
	if err != nil || fence == nil || fence.ConfigRevision != 3 || fence.ProbeGeneration != 2 {
		t.Fatalf("fence = %#v, %v", fence, err)
	}
	if _, err := ParseProbeOutboxSourceFence("{not-json"); err == nil {
		t.Fatal("corrupt fence must error")
	}
}
