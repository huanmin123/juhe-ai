package accounts

import (
	"sync"
	"time"
)

// 键控有界重试队列（Node→Go 迁移缺口登记项：shared/retry-queue.ts 的
// RetryQueueEnqueueOptions / RetryQueueRetryEvent 消费面）。
//
// 归档唯一生产消费者是余额快照清理协调器
// （account-balance-snapshot-cleanup.service.ts:78-136），它实际使用的
// 选项面为：enqueue(key, item, { replaceExisting: true, delayMs: 0 })、
// 序列重试策略 [250,1000,1000]ms × 3、四类回调（onSuccess/onFailure/
// onRetryScheduled/onExhausted）与 snapshot 的 pending/running 计数。
// 本移植只承载该消费面：归档的 priority / reservedPriorityConcurrency /
// replaceExistingOnlyIfHigherPriority / setConcurrency / hasFollowUp /
// stopAndDrain 在生产路径零调用，未移植（架构性裁剪，与归档行为无差异；
// 运行中替换语义经 followUp 单槽保留——cleanupAfterSave 对同一账户的
// 连续保存依赖它）。
//
// run 返回 nil 即成功（Node run 返回 true 或 throw；Node 默认 retry=true，
// 即失败恒进入重试判定）。并发默认 4：归档为 pLimit(globalMax=5000) +
// 全局后台并发槽双重限流，Go 无并发治理器，队列并发本身就是限流面。

// retryQueueEvent mirrors RetryQueueEvent<T>（含 RetryQueueRetryEvent 的
// delayMs / nextAttemptAtMs 扩展与 error 载荷）。
type retryQueueEvent[T any] struct {
	Key             string
	Item            T
	AttemptIndex    int
	RetryNumber     int
	DelayMs         int64
	NextAttemptAtMs int64
	Err             error
}

// retryQueueEnqueueOptions mirrors RetryQueueEnqueueOptions（实际消费面）。
type retryQueueEnqueueOptions struct {
	// ReplaceExisting mirrors replaceExisting: pending items are replaced and
	// reset to attempt 0; running items receive the single follow-up slot.
	ReplaceExisting bool
	// DelayMs mirrors delayMs: the first run waits this many ms.
	DelayMs int64
}

type retryQueueItem[T any] struct {
	key          string
	item         T
	attemptIndex int
	nextRunAtMs  int64
	running      bool
	followUp     *retryQueueItem[T]
}

// retryQueueCallbacks mirrors the four Node callbacks (all optional).
type retryQueueCallbacks[T any] struct {
	OnSuccess        func(retryQueueEvent[T])
	OnFailure        func(retryQueueEvent[T])
	OnRetryScheduled func(retryQueueEvent[T])
	OnExhausted      func(retryQueueEvent[T])
}

// retryQueue mirrors createRetryQueue<T> for the cleanup consumer face.
type retryQueue[T any] struct {
	name        string
	retryDelays []int64
	concurrency int
	run         func(item T, attemptIndex int) error
	callbacks   retryQueueCallbacks[T]
	now         func() time.Time

	mu          sync.Mutex
	items       map[string]*retryQueueItem[T]
	timer       *time.Timer
	stopped     bool
	runningWait sync.WaitGroup
}

func newRetryQueue[T any](name string, retryDelays []int64, concurrency int,
	run func(item T, attemptIndex int) error, callbacks retryQueueCallbacks[T]) *retryQueue[T] {
	if concurrency < 1 {
		concurrency = 1
	}
	return &retryQueue[T]{
		name:        name,
		retryDelays: retryDelays,
		concurrency: concurrency,
		run:         run,
		callbacks:   callbacks,
		now:         time.Now,
		items:       map[string]*retryQueueItem[T]{},
	}
}

// enqueue mirrors enqueue(key, item, options): new keys insert; pending
// existing keys replace+reset when ReplaceExisting; running existing keys
// take the follow-up slot (Node retry-queue.ts:293-339).
func (q *retryQueue[T]) enqueue(key string, item T, options retryQueueEnqueueOptions) bool {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return false
	}
	nextRunAtMs := q.now().UnixMilli() + max64(0, options.DelayMs)
	existing := q.items[key]
	if existing != nil {
		// Node retry-queue.ts:293-339 — a running item without ReplaceExisting
		// refuses (the consumed face has no followUpWhenRunning; replace on a
		// running item takes the follow-up slot).
		if !options.ReplaceExisting {
			q.mu.Unlock()
			return false
		}
		if existing.running {
			existing.followUp = &retryQueueItem[T]{key: key, item: item, nextRunAtMs: nextRunAtMs}
			q.mu.Unlock()
			q.pump()
			return true
		}
		existing.item = item
		existing.attemptIndex = 0
		existing.nextRunAtMs = nextRunAtMs
		q.mu.Unlock()
		q.pump()
		return true
	}
	q.items[key] = &retryQueueItem[T]{key: key, item: item, nextRunAtMs: nextRunAtMs}
	q.mu.Unlock()
	q.pump()
	return true
}

// delete mirrors delete(key).
func (q *retryQueue[T]) delete(key string) {
	q.mu.Lock()
	delete(q.items, key)
	q.mu.Unlock()
	q.pump()
}

// clear mirrors clear().
func (q *retryQueue[T]) clear() {
	q.mu.Lock()
	q.items = map[string]*retryQueueItem[T]{}
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.mu.Unlock()
}

// stop mirrors stopAndDrain's scheduling half: refuse new enqueues, drop
// every pending item and follow-up, and disarm the timer. Running items
// finish their current invocation and are discarded without further
// scheduling (runItem checks stopped before settling). The caller that needs
// in-flight work to stop touching external resources owns the run context
// cancellation (see StoreBalanceSnapshotCleaner.Close).
func (q *retryQueue[T]) stop() {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.stopped = true
	q.items = map[string]*retryQueueItem[T]{}
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.mu.Unlock()
}

// waitRunning blocks until every in-flight invocation returned.
func (q *retryQueue[T]) waitRunning() {
	q.runningWait.Wait()
}

// setClockForTest overrides the scheduling clock (tests only).
func (q *retryQueue[T]) setClockForTest(now func() time.Time) {
	q.mu.Lock()
	q.now = now
	q.mu.Unlock()
}

// counts mirrors snapshot().pendingCount/runningCount.
func (q *retryQueue[T]) counts() (pending, running int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.running {
			running++
		} else {
			pending++
		}
	}
	return pending, running
}

// pump launches every due item under the concurrency cap and (re)arms the
// single timer at the next due instant (Node drain/scheduleNext).
func (q *retryQueue[T]) pump() {
	for {
		q.mu.Lock()
		if q.stopped {
			q.mu.Unlock()
			return
		}
		nowMs := q.now().UnixMilli()
		running := 0
		var next *retryQueueItem[T]
		nextDue := int64(0)
		for _, item := range q.items {
			if item.running {
				running++
				continue
			}
			if item.nextRunAtMs <= nowMs {
				if next == nil || item.nextRunAtMs < next.nextRunAtMs ||
					(item.nextRunAtMs == next.nextRunAtMs && item.key < next.key) {
					next = item
				}
			} else if nextDue == 0 || item.nextRunAtMs < nextDue {
				nextDue = item.nextRunAtMs
			}
		}
		if next == nil {
			if running >= q.concurrency || nextDue == 0 {
				q.mu.Unlock()
				return
			}
			q.armTimerLocked(nextDue - nowMs)
			q.mu.Unlock()
			return
		}
		if running >= q.concurrency {
			q.mu.Unlock()
			return
		}
		next.running = true
		q.mu.Unlock()
		go q.runItem(next)
	}
}

// armTimerLocked (re)arms the drain timer; caller holds q.mu.
func (q *retryQueue[T]) armTimerLocked(delayMs int64) {
	if delayMs < 0 {
		delayMs = 0
	}
	if q.timer != nil {
		q.timer.Stop()
	}
	q.timer = time.AfterFunc(time.Duration(delayMs)*time.Millisecond, q.pump)
}

// runItem mirrors runItem: execute, then settle the attempt (followUp
// replacement, success delete, retry schedule or exhaustion).
func (q *retryQueue[T]) runItem(queueItem *retryQueueItem[T]) {
	q.runningWait.Add(1)
	defer q.runningWait.Done()
	err := q.run(queueItem.item, queueItem.attemptIndex)
	success := err == nil

	q.mu.Lock()
	queueItem.running = false
	if q.stopped {
		if q.items[queueItem.key] == queueItem {
			delete(q.items, queueItem.key)
		}
		q.mu.Unlock()
		return
	}
	if q.items[queueItem.key] != queueItem {
		q.mu.Unlock()
		q.pump()
		return
	}
	followUp := queueItem.followUp
	base := retryQueueEvent[T]{
		Key:          queueItem.key,
		Item:         queueItem.item,
		AttemptIndex: queueItem.attemptIndex,
		RetryNumber:  queueItem.attemptIndex + 1,
		Err:          err,
	}
	if followUp != nil {
		// Node retry-queue.ts:185-198 — the completed attempt still reports
		// success/failure, then the follow-up replaces the slot with attempt 0
		// (its own run settles through the normal path).
		queueItem.item = followUp.item
		queueItem.attemptIndex = 0
		queueItem.nextRunAtMs = followUp.nextRunAtMs
		queueItem.followUp = nil
		q.mu.Unlock()
		q.emit(base, success)
		q.pump()
		return
	}
	if success {
		delete(q.items, queueItem.key)
		q.mu.Unlock()
		q.emit(base, true)
		q.pump()
		return
	}
	// Retry allowed while attemptIndex < len(retryDelays): delay for the NEXT
	// attempt index (Node retryDelayMs(policy, retryNumber)).
	if queueItem.attemptIndex < len(q.retryDelays) {
		delayMs := q.retryDelays[queueItem.attemptIndex]
		queueItem.attemptIndex++
		queueItem.nextRunAtMs = q.now().UnixMilli() + delayMs
		retryEvent := base
		retryEvent.DelayMs = delayMs
		retryEvent.NextAttemptAtMs = queueItem.nextRunAtMs
		q.mu.Unlock()
		q.emit(base, false)
		if q.callbacks.OnRetryScheduled != nil {
			q.callbacks.OnRetryScheduled(retryEvent)
		}
		q.pump()
		return
	}
	delete(q.items, queueItem.key)
	q.mu.Unlock()
	q.emit(base, false)
	if q.callbacks.OnExhausted != nil {
		q.callbacks.OnExhausted(base)
	}
	q.pump()
}

func (q *retryQueue[T]) emit(event retryQueueEvent[T], success bool) {
	if success {
		if q.callbacks.OnSuccess != nil {
			q.callbacks.OnSuccess(event)
		}
		return
	}
	if q.callbacks.OnFailure != nil {
		q.callbacks.OnFailure(event)
	}
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
