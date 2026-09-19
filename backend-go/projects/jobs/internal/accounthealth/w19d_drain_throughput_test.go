package accounthealth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// D 任务②③：outbox drain 的有界并发、claim 上限与 pending 堆积告警单测
// （fake store / fake clock，风格随 outbox_drain_test.go）。

// w19dConcurrencyBoundary 记录进入 CurrentProbeInput 的峰值并发；进入第
// expected 个并发时放行全部（串行实现等 5s 兜底放行后以峰值断言失败）。
type w19dConcurrencyBoundary struct {
	enter func()
}

func (b w19dConcurrencyBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	b.enter()
	return 0, 0, 0, false, nil
}

// TestDrainProbeOutboxBoundedConcurrencyConsumesAllRows：Concurrency=3 时
// 6 行经 3 个 worker 并发消费（峰值并发恰为 3，行间无顺序依赖），全部行
// 成功出队，无错误。
func TestDrainProbeOutboxBoundedConcurrencyConsumesAllRows(t *testing.T) {
	const workers = 3
	const rowCount = 6
	rows := make([]ProbeOutboxRow, 0, rowCount)
	for index := 0; index < rowCount; index++ {
		rows = append(rows, ProbeOutboxRow{
			RequestID: "j1-concurrent-" + string(rune('a'+index)),
			AccountID: "account-" + string(rune('a'+index)),
			Reason:    "request_failure",
			Deadline:  time.Now().UTC().Add(time.Minute),
		})
	}
	outbox := &probeOutboxMemoryStore{pending: rows}

	var mu sync.Mutex
	current, peak := 0, 0
	var releaseOnce sync.Once
	release := make(chan struct{})
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	// 串行退化时的兜底放行：测试不挂死，由峰值断言报告失败。
	timer := time.AfterFunc(5*time.Second, releaseAll)
	defer timer.Stop()
	enter := func() {
		mu.Lock()
		current++
		if current > peak {
			peak = current
		}
		reached := current >= workers
		mu.Unlock()
		if reached {
			releaseAll()
		}
		<-release
		mu.Lock()
		current--
		mu.Unlock()
	}

	runner := newDrainRunner(t, "drain-secret", outbox, w19dConcurrencyBoundary{enter: enter}, nil)
	runner.probeDrain.Concurrency = workers
	lease := drainLease(t, runner)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	mu.Lock()
	observedPeak := peak
	mu.Unlock()
	if observedPeak != workers {
		t.Fatalf("peak boundary concurrency = %d want exactly %d", observedPeak, workers)
	}
	if outbox.claimCount() != 0 || len(outbox.consumed) != rowCount {
		t.Fatalf("all rows must be consumed: pending=%d consumed=%d", outbox.claimCount(), len(outbox.consumed))
	}
}

// w19dLimitRecordingStore 记录 claim 收到的上限（断言 env/默认值传递）。
type w19dLimitRecordingStore struct {
	probeOutboxMemoryStore
	claimLimit int
}

func (s *w19dLimitRecordingStore) ClaimPendingProbeRequests(ctx context.Context, limit int, now time.Time) ([]ProbeOutboxRow, error) {
	s.claimLimit = limit
	return s.probeOutboxMemoryStore.ClaimPendingProbeRequests(ctx, limit, now)
}

// TestDrainProbeOutboxClaimLimitConfiguredAndDefault：Limit 显式配置时原样
// 传递；未配置（<=0）取 defaultProbeOutboxDrainLimit 兜底（D 任务②默认上调
// 至 256）。
func TestDrainProbeOutboxClaimLimitConfiguredAndDefault(t *testing.T) {
	build := func(limit int) (*Runner, *w19dLimitRecordingStore, OwnerLease) {
		outbox := &w19dLimitRecordingStore{}
		runner := newDrainRunner(t, "drain-secret", &outbox.probeOutboxMemoryStore, probeDrainBoundary{ok: map[string]bool{}}, nil)
		runner.probeDrain.Store = outbox
		runner.probeDrain.Limit = limit
		return runner, outbox, drainLease(t, runner)
	}
	runner, outbox, lease := build(16)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if outbox.claimLimit != 16 {
		t.Fatalf("configured claim limit = %d want 16", outbox.claimLimit)
	}
	runner, outbox, lease = build(0)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if outbox.claimLimit != defaultProbeOutboxDrainLimit || defaultProbeOutboxDrainLimit != 256 {
		t.Fatalf("default claim limit = %d want %d", outbox.claimLimit, defaultProbeOutboxDrainLimit)
	}
}

// w19dBacklogStore 是带计数能力的 outbox store：pending 计数/最旧行可配置
// （堆积告警断言），claim/complete 复用内存实现。
type w19dBacklogStore struct {
	probeOutboxMemoryStore
	mu         sync.Mutex
	count      int64
	oldest     time.Time
	countErr   error
	countCalls int
}

func (s *w19dBacklogStore) CountPendingProbeRequests(context.Context) (int64, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.countCalls++
	return s.count, s.oldest, s.countErr
}

func (s *w19dBacklogStore) snapshot() (count int64, oldest time.Time, countErr error, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.oldest, s.countErr, s.countCalls
}

// newBacklogRunner 构造可注入时钟与日志的 drain runner（每周期 claim 为空，
// 只验证堆积告警面——0 行周期同样必须统计 pending）。
func newBacklogRunner(t *testing.T, store *w19dBacklogStore, clock func() time.Time, logs *bytes.Buffer, threshold int) (*Runner, OwnerLease) {
	t.Helper()
	jobsStore, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "/account-health.sqlite3"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobsStore.Close() })
	runner := NewRunner(Config{
		InputDirectory:   t.TempDir(),
		InputKeys:        map[string][]byte{"current": []byte("drain-input-signing-key-123")},
		CredentialSecret: "drain-secret",
		ProbeTimeout:     time.Second,
		MaxResponseBytes: 1024,
		MaxConcurrency:   1,
		Now:              clock,
	}, jobsStore, slog.New(slog.NewJSONHandler(logs, nil)))
	runner.directInputReader = nil
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store:                store,
		Boundary:             probeDrainBoundary{ok: map[string]bool{}},
		BacklogWarnThreshold: threshold,
	})
	return runner, drainLease(t, runner)
}

// backlogEvents 解析日志缓冲中 account_health_probe_outbox_backlog 事件的
// 结构化字段。
func backlogEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		if record["event"] == "account_health_probe_outbox_backlog" {
			events = append(events, record)
		}
	}
	return events
}

// TestDrainProbeOutboxBacklogWarnWithSuppress：达到阈值的 0 行周期告警一条
// 结构化日志（pending、oldestAgeSeconds）；频控窗口内重复 drain 不再告警；
// 窗口过后再次告警。
func TestDrainProbeOutboxBacklogWarnWithSuppress(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := &w19dBacklogStore{count: 10, oldest: now.Add(-90 * time.Second)}
	var logs bytes.Buffer
	runner, lease := newBacklogRunner(t, store, clock, &logs, 5)

	assertCount := func(stage string, want int) {
		t.Helper()
		if events := backlogEvents(t, &logs); len(events) != want {
			t.Fatalf("%s: backlog events = %d want %d", stage, len(events), want)
		}
	}
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	assertCount("first drain", 1)
	first := backlogEvents(t, &logs)[0]
	if first["pending"] != float64(10) || first["oldestAgeSeconds"] != float64(90) || first["threshold"] != float64(5) {
		t.Fatalf("backlog fields = %v", first)
	}
	// 频控窗口内：重复 drain 不重复告警。
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("second drain: %v", err)
	}
	assertCount("suppressed drain", 1)
	// 窗口过后：再次告警。
	now = now.Add(11 * time.Minute)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("third drain: %v", err)
	}
	assertCount("post-window drain", 2)
	if _, _, _, calls := store.snapshot(); calls != 3 {
		t.Fatalf("count calls = %d want one per drain cycle", calls)
	}
}

// TestDrainProbeOutboxBacklogBelowThresholdSilent：低于阈值不告警；阈值 <=0
// 关闭告警面。
func TestDrainProbeOutboxBacklogBelowThresholdSilent(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := &w19dBacklogStore{count: 4}
	var logs bytes.Buffer
	runner, lease := newBacklogRunner(t, store, clock, &logs, 5)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if events := backlogEvents(t, &logs); len(events) != 0 {
		t.Fatalf("below-threshold must stay silent, events = %v", events)
	}

	// 阈值 <=0：计数能力存在也不告警。
	disabled := &w19dBacklogStore{count: 10_000}
	var disabledLogs bytes.Buffer
	runner, lease = newBacklogRunner(t, disabled, clock, &disabledLogs, 0)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, _, _, calls := disabled.snapshot(); calls != 0 {
		t.Fatalf("threshold <=0 must skip counting, calls = %d", calls)
	}
}

// TestDrainProbeOutboxBacklogWithoutCounterStore：store 未实现
// ProbeRequestBacklogCounter（既有 fake/极简 store）时 drain 照常工作且不
// 告警——可选能力接口的兼容断言。
func TestDrainProbeOutboxBacklogWithoutCounterStore(t *testing.T) {
	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{{
		RequestID: "j1-no-counter",
		AccountID: "account-1",
		Reason:    "request_failure",
		Deadline:  time.Now().UTC().Add(time.Minute),
	}}}
	var logs bytes.Buffer
	runner := newDrainRunner(t, "drain-secret", outbox, probeDrainBoundary{ok: map[string]bool{}}, nil)
	runner.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	runner.probeDrain.BacklogWarnThreshold = 1
	lease := drainLease(t, runner)
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if events := backlogEvents(t, &logs); len(events) != 0 {
		t.Fatalf("store without counter must stay silent, events = %v", events)
	}
	if outbox.claimCount() != 0 || len(outbox.consumed) != 1 {
		t.Fatalf("row must still be consumed: pending=%d consumed=%v", outbox.claimCount(), outbox.consumed)
	}
}
