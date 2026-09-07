package accountquality

import (
	"context"
	"sync"
	"time"
)

// RetryQueue 是 Node createRetryQueue 在本任务族使用的子集移植：
//   - key 去重（pending 或 running 中存在同名 key 时 enqueue 返回 false）
//   - 并发上限、按到期时间出队（nextRunAtMs）
//   - 本任务族策略为 sequenceRetryPolicy(name, [], 0)：maxRetries=0，
//     即失败（run 返回 false 或 panic/error）立即 onExhausted，不重试
//   - snapshot 提供.pendingCount/.runningCount/.nextRunAt 供扫描槽位计算
//   - StopAndDrain 支持停机排空
//
// 重试与退避契约：`sequenceRetryPolicy('account_quality_failure_precheck', [], 0)`
// 与 `sequenceRetryPolicy('account_api_key_cooldown_retest_revival', [], 0)`
// 都没有配置重试档位，因此 Go 端固定零重试（保持失败终态一致）。
type RetryQueue[T any] struct {
	name        string
	concurrency int
	clock       Clock
	logger      Logger

	mu      sync.Mutex
	items   map[string]*retryQueueItem[T]
	running int
	stopped bool
	drainWg sync.WaitGroup
	nextRun *time.Timer
	// wake 立即触发一次调度检查（enqueue 提前到期时）。
	scheduleFn func(d time.Duration) *time.Timer

	run         func(ctx context.Context, run QueueRunContext, item T) (bool, error)
	onExhausted func(event RetryQueueEvent[T])
}

// RetryQueueEvent 等价 Node RetryQueueEvent。
type RetryQueueEvent[T any] struct {
	Key          string
	Item         T
	AttemptIndex int
	RetryNumber  int
}

// RetryQueueSnapshot 等价 Node RetryQueueSnapshot（nextRunAt 为 RFC3339 文本）。
type RetryQueueSnapshot struct {
	Name         string
	PendingCount int
	RunningCount int
	NextRunAt    string
}

type retryQueueItem[T any] struct {
	key          string
	item         T
	attemptIndex int
	nextRunAtMs  int64
	running      bool
}

// QueueRunContext 传给 run 回调。
type QueueRunContext struct {
	AttemptIndex int
	RetryNumber  int
}

// NewRetryQueue 构建队列；run 返回 (handled bool, err error)：
//
//	handled=false 或 err!=nil 视为失败（零重试 → onExhausted）。
//
// ctx 为调用方传入的父上下文（StopAndDrain 不取消它）。
func NewRetryQueue[T any](name string, concurrency int, clock Clock, logger Logger, run func(ctx context.Context, run QueueRunContext, item T) (bool, error), onExhausted func(event RetryQueueEvent[T])) *RetryQueue[T] {
	if concurrency < 1 {
		concurrency = 1
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if logger == nil {
		logger = NopLogger{}
	}
	return &RetryQueue[T]{
		name:        name,
		concurrency: concurrency,
		clock:       clock,
		logger:      logger,
		items:       map[string]*retryQueueItem[T]{},
		run:         run,
		onExhausted: onExhausted,
	}
}

// Enqueue 等价 Node enqueue：key 已存在（pending/running）返回 false，
// 否则入队并触发调度。
func (q *RetryQueue[T]) Enqueue(key string, item T) bool {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return false
	}
	if _, exists := q.items[key]; exists {
		q.mu.Unlock()
		return false
	}
	q.items[key] = &retryQueueItem[T]{
		key:         key,
		item:        item,
		nextRunAtMs: q.clock.Now().UnixMilli(),
	}
	q.mu.Unlock()
	q.pump()
	return true
}

// HasKey 报告 key 是否仍在队（pending 或 running）。
func (q *RetryQueue[T]) HasKey(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.items[key]
	return ok
}

// Delete 移除 key。
func (q *RetryQueue[T]) Delete(key string) {
	q.mu.Lock()
	delete(q.items, key)
	q.mu.Unlock()
}

// Clear 清空队列。
func (q *RetryQueue[T]) Clear() {
	q.mu.Lock()
	q.items = map[string]*retryQueueItem[T]{}
	q.mu.Unlock()
}

// Snapshot 等价 Node snapshot()。
func (q *RetryQueue[T]) Snapshot() RetryQueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	snap := RetryQueueSnapshot{Name: q.name}
	var nextRunAtMs int64
	for _, item := range q.items {
		if item.running {
			snap.RunningCount++
			continue
		}
		snap.PendingCount++
		if nextRunAtMs == 0 || item.nextRunAtMs < nextRunAtMs {
			nextRunAtMs = item.nextRunAtMs
		}
	}
	if nextRunAtMs != 0 && snap.PendingCount > 0 {
		snap.NextRunAt = FormatMillis(time.UnixMilli(nextRunAtMs))
	}
	return snap
}

// StopAndDrain 等价 Node stopAndDrain：停止接收新任务并等待在跑任务收口。
// 返回 (drained, activeCount)。
func (q *RetryQueue[T]) StopAndDrain(timeout time.Duration) (bool, int) {
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		q.drainWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	q.mu.Lock()
	active := 0
	for _, item := range q.items {
		if item.running {
			active++
		}
	}
	q.mu.Unlock()
	return active == 0, active
}

// pump 调度下一次出队（在锁外调用）。
func (q *RetryQueue[T]) pump() {
	q.pumpWithCtx(context.Background())
}

// pumpWithCtx 以给定父上下文驱动一次出队循环（ctx 感知入口）。
func (q *RetryQueue[T]) pumpWithCtx(ctx context.Context) {
	for {
		q.mu.Lock()
		if q.stopped {
			q.mu.Unlock()
			return
		}
		if q.running >= q.concurrency {
			q.mu.Unlock()
			return
		}
		nowMs := q.clock.Now().UnixMilli()
		var next *retryQueueItem[T]
		var nextAtMs int64
		for _, item := range q.items {
			if item.running || item.nextRunAtMs > nowMs {
				if !item.running && (nextAtMs == 0 || item.nextRunAtMs < nextAtMs) {
					nextAtMs = item.nextRunAtMs
				}
				continue
			}
			if next == nil {
				next = item
				continue
			}
			// 出队顺序：到期时间升序；同级由入队时间决定。
			if item.nextRunAtMs < next.nextRunAtMs {
				next = item
			}
		}
		if next == nil {
			q.mu.Unlock()
			return
		}
		next.running = true
		q.running++
		q.drainWg.Add(1)
		item := next
		q.mu.Unlock()
		go q.runItem(ctx, item)
	}
}

func (q *RetryQueue[T]) runItem(ctx context.Context, item *retryQueueItem[T]) {
	defer q.drainWg.Done()
	handled, runErr := q.run(ctx, QueueRunContext{AttemptIndex: item.attemptIndex, RetryNumber: item.attemptIndex + 1}, item.item)

	q.mu.Lock()
	q.running--
	// 若 key 已被外部删除则不再触发 onExhausted。
	_, stillTracked := q.items[item.key]
	delete(q.items, item.key)
	q.mu.Unlock()

	if (!handled || runErr != nil) && stillTracked && q.onExhausted != nil {
		q.onExhausted(RetryQueueEvent[T]{
			Key:          item.key,
			Item:         item.item,
			AttemptIndex: item.attemptIndex,
			RetryNumber:  item.attemptIndex + 1,
		})
	}
}
