package accounts

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---- retryQueue 单元（Mock 优先：注入时钟 + 通道捕获回调） ----

func TestRetryQueueSuccessFirstAttempt(t *testing.T) {
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error { return nil },
		retryQueueCallbacks[string]{
			OnSuccess: func(ev retryQueueEvent[string]) {
				if ev.Item != "a" || ev.AttemptIndex != 0 || ev.RetryNumber != 1 {
					t.Errorf("success event = %+v", ev)
				}
			},
		})
	if !q.enqueue("a", "a", retryQueueEnqueueOptions{}) {
		t.Fatal("enqueue on fresh key must return true")
	}
	waitQueueDrained(t, q)
	if pending, running := q.counts(); pending != 0 || running != 0 {
		t.Fatalf("queue not drained: pending=%d running=%d", pending, running)
	}
}

func TestRetryQueueRetriesThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	failures := 0
	retries := []int64{}
	succeeded := false
	exhausted := false
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts < 3 {
				return errors.New("temporary")
			}
			return nil
		},
		retryQueueCallbacks[string]{
			OnSuccess: func(retryQueueEvent[string]) { succeeded = true },
			OnFailure: func(ev retryQueueEvent[string]) {
				mu.Lock()
				failures++
				mu.Unlock()
			},
			OnRetryScheduled: func(ev retryQueueEvent[string]) {
				mu.Lock()
				retries = append(retries, ev.DelayMs)
				mu.Unlock()
				if ev.RetryNumber != ev.AttemptIndex+1 {
					t.Errorf("retry event numbering: %+v", ev)
				}
			},
			OnExhausted: func(retryQueueEvent[string]) { exhausted = true },
		})
	q.enqueue("a", "a", retryQueueEnqueueOptions{})
	waitQueueDrained(t, q)
	mu.Lock()
	defer mu.Unlock()
	// Node 对每次失败尝试都触发 onFailure（attempt 0/1），消费方以 attemptIndex
	// 分叉 initial_failed/retry_failed 事件名。
	if attempts != 3 || failures != 2 || len(retries) != 2 || !succeeded || exhausted {
		t.Fatalf("attempts=%d failures=%d retries=%v succeeded=%v exhausted=%v",
			attempts, failures, retries, succeeded, exhausted)
	}
}

func TestRetryQueueExhaustsAfterDelays(t *testing.T) {
	exhausted := false
	exhaustedAttempts := 0
	q := newRetryQueue[string]("test", []int64{1, 1}, 1,
		func(item string, attemptIndex int) error { return errors.New("permanent") },
		retryQueueCallbacks[string]{
			OnExhausted: func(ev retryQueueEvent[string]) {
				exhausted = true
				exhaustedAttempts = ev.AttemptIndex + 1
			},
		})
	q.enqueue("a", "a", retryQueueEnqueueOptions{})
	waitQueueDrained(t, q)
	// 1 次首发 + 2 次重试（delays 长度=2）= 3 次尝试。
	if !exhausted || exhaustedAttempts != 3 {
		t.Fatalf("exhausted=%v exhaustedAttempts=%d, want true/3", exhausted, exhaustedAttempts)
	}
	if pending, running := q.counts(); pending != 0 || running != 0 {
		t.Fatalf("exhausted item must leave the queue: pending=%d running=%d", pending, running)
	}
}

func TestRetryQueueReplaceExistingPending(t *testing.T) {
	ran := make(chan string, 4)
	blockerStarted := make(chan struct{})
	release := make(chan struct{})
	// 并发=1：blocker 占住运行槽，使后续入队项保持 pending。
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error {
			if item == "blocker" {
				close(blockerStarted)
				<-release
			}
			ran <- item
			return nil
		}, retryQueueCallbacks[string]{})
	q.enqueue("b-blocker", "blocker", retryQueueEnqueueOptions{})
	<-blockerStarted
	// "first" 进入 pending；ReplaceExisting 替换后仅运行最新项。
	q.enqueue("a", "first", retryQueueEnqueueOptions{})
	if !q.enqueue("a", "second", retryQueueEnqueueOptions{ReplaceExisting: true}) {
		t.Fatal("replace enqueue must return true")
	}
	close(release)
	waitQueueDrained(t, q)
	close(ran)
	got := []string{}
	for item := range ran {
		got = append(got, item)
	}
	// blocker 与替换后的 "second"；被替换的 "first" 永不运行。
	if len(got) != 2 || got[0] != "blocker" || got[1] != "second" {
		t.Fatalf("replaced pending item must run once as the latest: %v", got)
	}
}

func TestRetryQueueReplaceExistingWhileRunningFollowUp(t *testing.T) {
	ran := make(chan string, 4)
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error {
			if item == "first" {
				close(firstStarted)
				<-release
			}
			ran <- item
			return nil
		}, retryQueueCallbacks[string]{})
	q.enqueue("a", "first", retryQueueEnqueueOptions{})
	<-firstStarted
	// 运行中的替换进入 followUp 单槽：本轮结束后再跑一次最新项。
	if !q.enqueue("a", "second", retryQueueEnqueueOptions{ReplaceExisting: true}) {
		t.Fatal("replace enqueue must return true")
	}
	close(release)
	waitQueueDrained(t, q)
	close(ran)
	got := []string{}
	for item := range ran {
		got = append(got, item)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("running replace must follow up: %v", got)
	}
}

func TestRetryQueueReplaceExistingRefusedWithoutFlag(t *testing.T) {
	block := make(chan struct{})
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error {
			<-block
			return nil
		}, retryQueueCallbacks[string]{})
	q.enqueue("a", "first", retryQueueEnqueueOptions{})
	if q.enqueue("a", "second", retryQueueEnqueueOptions{}) {
		t.Fatal("enqueue without ReplaceExisting on an existing key must return false")
	}
	close(block)
}

// ---- 协调器集成（Node 事件词表 + requestId 身份） ----

func TestBalanceCleanupCoordinatorRetriesAndSucceeds(t *testing.T) {
	env := newCleanupCoordEnv(t)
	env.failTimes(2)
	env.cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
		AccountID: "acc-1", ConfigRevision: 7, Reason: BalanceSnapshotCleanupReasonConfigurationChanged,
	})
	// 3 次尝试（首发 + 2 次重试）内成功。
	waitCond(t, func() bool { return env.successCount() == 1 }, "cleanup success")
	if got := env.attemptCount(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if env.exhaustedCount() != 0 {
		t.Fatalf("exhausted events = %d, want 0", env.exhaustedCount())
	}
}

func TestBalanceCleanupCoordinatorExhaustsAndKeepsSuppression(t *testing.T) {
	env := newCleanupCoordEnv(t)
	env.failAlways()
	env.cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
		AccountID: "acc-1", ConfigRevision: 7, Reason: BalanceSnapshotCleanupReasonMultipleAPIKeys,
	})
	waitCond(t, func() bool { return env.exhaustedCount() == 1 }, "cleanup exhausted")
	if got := env.attemptCount(); got != 4 {
		t.Fatalf("attempts = %d, want 4 (initial + 3 retries)", got)
	}
	env.cleaner.mu.Lock()
	_, suppressed := env.cleaner.suppressedItems["acc-1"]
	_, exhausted := env.cleaner.exhaustedAccounts["acc-1"]
	env.cleaner.mu.Unlock()
	if !suppressed || !exhausted {
		t.Fatalf("suppressed=%v exhausted=%v, want true/true", suppressed, exhausted)
	}
}

func TestBalanceCleanupCoordinatorReplaceExistingKeepsLatestRequest(t *testing.T) {
	env := newCleanupCoordEnv(t)
	env.failTimes(1)
	env.cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
		AccountID: "acc-1", ConfigRevision: 7, Reason: BalanceSnapshotCleanupReasonConfigurationChanged,
	})
	env.cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
		AccountID: "acc-1", ConfigRevision: 9, Reason: BalanceSnapshotCleanupReasonMultipleAPIKeys,
	})
	waitCond(t, func() bool { return env.successCount() >= 1 }, "cleanup success")
	env.cleaner.mu.Lock()
	_, suppressed := env.cleaner.suppressedItems["acc-1"]
	env.cleaner.mu.Unlock()
	if suppressed {
		t.Fatal("latest request success must clear the suppression entry")
	}
}

// ---- 测试脚手架 ----

type cleanupCoordEnv struct {
	t        *testing.T
	cleaner  *StoreBalanceSnapshotCleaner
	store    *Store
	mu       sync.Mutex
	failLeft int
	attempts int
	success  int
	exhaust  int
}

func newCleanupCoordEnv(t *testing.T) *cleanupCoordEnv {
	t.Helper()
	env := &cleanupCoordEnv{t: t}
	store := &Store{}
	// deleteSupersededSnapshot 经注入的 run 闭包走 env 的失败注入，不触真实 DB。
	cleaner := NewStoreBalanceSnapshotCleaner(store)
	cleaner.mu.Lock()
	cleaner.queue = newRetryQueue[cleanupQueueItem]("account-balance-snapshot-cleanup",
		cleanupRetryDelays, cleanupQueueConcurrency,
		func(item cleanupQueueItem, attemptIndex int) error {
			env.mu.Lock()
			env.attempts++
			leave := env.failLeft
			if leave > 0 {
				env.failLeft--
			}
			env.mu.Unlock()
			if leave > 0 {
				return errors.New("temporary cleanup failure")
			}
			return nil
		},
		retryQueueCallbacks[cleanupQueueItem]{
			OnSuccess: func(ev retryQueueEvent[cleanupQueueItem]) {
				cleaner.onCleanupSuccess(ev)
				env.mu.Lock()
				env.success++
				env.mu.Unlock()
			},
			OnFailure:        cleaner.onCleanupFailure,
			OnRetryScheduled: cleaner.onCleanupRetryScheduled,
			OnExhausted: func(ev retryQueueEvent[cleanupQueueItem]) {
				cleaner.onCleanupExhausted(ev)
				env.mu.Lock()
				env.exhaust++
				env.mu.Unlock()
			},
		})
	cleaner.mu.Unlock()
	env.cleaner = cleaner
	env.store = store
	return env
}

func (e *cleanupCoordEnv) failTimes(n int) {
	e.mu.Lock()
	e.failLeft = n
	e.mu.Unlock()
}

func (e *cleanupCoordEnv) failAlways() { e.failTimes(1 << 30) }

func (e *cleanupCoordEnv) successCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.success
}

func (e *cleanupCoordEnv) exhaustedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exhaust
}

func (e *cleanupCoordEnv) attemptCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts
}

func waitQueueDrained(t *testing.T, q *retryQueue[string]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, running := q.counts()
		if pending == 0 && running == 0 {
			// 让最后一次回调落地。
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("retry queue did not drain in time")
}

func waitCond(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met in time: %s", what)
}

// TestBalanceCleanupCloseRefusesAndCancels pins the shutdown lifecycle
// (Node stopAndDrain contract): after Close the queue refuses new enqueues,
// pending items never run, the lifetime context is cancelled (an in-flight
// DELETE stops touching the pool), and Close returns only after running
// invocations returned — the queue never touches a closed pool.
func TestBalanceCleanupCloseRefusesAndCancels(t *testing.T) {
	env := newCleanupCoordEnv(t)
	env.failAlways()
	// 慢删除：Close 与取消的竞态面。
	started := make(chan struct{})
	release := make(chan struct{})
	env.cleaner.mu.Lock()
	runErr := error(nil)
	env.cleaner.queue = newRetryQueue[cleanupQueueItem]("account-balance-snapshot-cleanup",
		cleanupRetryDelays, cleanupQueueConcurrency,
		func(item cleanupQueueItem, attemptIndex int) error {
			close(started)
			<-release
			return env.cleaner.lifetimeCtx.Err()
		},
		retryQueueCallbacks[cleanupQueueItem]{
			OnSuccess: env.cleaner.onCleanupSuccess,
			OnFailure: func(ev retryQueueEvent[cleanupQueueItem]) {
				runErr = ev.Err
				env.cleaner.onCleanupFailure(ev)
			},
			OnRetryScheduled: env.cleaner.onCleanupRetryScheduled,
			OnExhausted:      env.cleaner.onCleanupExhausted,
		})
	env.cleaner.mu.Unlock()

	env.cleaner.CleanupBalanceSnapshotAfterSave(BalanceSnapshotCleanupRequest{
		AccountID: "acc-close", ConfigRevision: 3, Reason: BalanceSnapshotCleanupReasonConfigurationChanged,
	})
	<-started // 删除已在飞行中。

	closed := make(chan struct{})
	go func() {
		env.cleaner.Close()
		close(closed)
	}()
	// Close 等待在飞调用返回：取消 ctx 后释放删除，Close 才能完成。
	<-time.After(20 * time.Millisecond)
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close must return after the in-flight invocation settled")
	}
	if runErr != nil && runErr != context.Canceled {
		t.Fatalf("in-flight run must observe the cancelled lifetime ctx, got %v", runErr)
	}
	// Close 后拒绝新任务。
	if env.cleaner.Queue().enqueue("acc-close-2", cleanupQueueItem{}, retryQueueEnqueueOptions{ReplaceExisting: true}) {
		t.Fatal("enqueue after Close must be refused")
	}
	// 重复 Close 幂等。
	env.cleaner.Close()
}


// TestRetryQueueStopDropsPending pins the stop contract at the queue level:
// stop() refuses new enqueues, drops every pending item (they never run),
// and the running item finishes verbatim without retry scheduling.
func TestRetryQueueStopDropsPending(t *testing.T) {
	ran := make(chan string, 4)
	blockerStarted := make(chan struct{})
	release := make(chan struct{})
	q := newRetryQueue[string]("test", []int64{1, 1, 1}, 1,
		func(item string, attemptIndex int) error {
			if item == "blocker" {
				close(blockerStarted)
				<-release
			}
			ran <- item
			return nil
		}, retryQueueCallbacks[string]{})
	q.enqueue("b", "blocker", retryQueueEnqueueOptions{})
	<-blockerStarted
	if !q.enqueue("a", "pending-item", retryQueueEnqueueOptions{}) {
		t.Fatal("enqueue while running must be accepted")
	}
	// stop 先于 release：pending 被丢弃，release 只放行在飞项收尾。
	q.stop()
	close(release)
	q.waitRunning()
	close(ran)
	got := []string{}
	for item := range ran {
		got = append(got, item)
	}
	if len(got) != 1 || got[0] != "blocker" {
		t.Fatalf("stop must drop pending items: %v", got)
	}
	if pending, running := q.counts(); pending != 0 || running != 0 {
		t.Fatalf("stopped queue must report empty: pending=%d running=%d", pending, running)
	}
	if q.enqueue("c", "after-stop", retryQueueEnqueueOptions{}) {
		t.Fatal("enqueue after stop must be refused")
	}
}
