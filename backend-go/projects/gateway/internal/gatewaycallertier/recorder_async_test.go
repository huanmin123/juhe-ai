// recorder_async_test.go —— Redis 形态异步落库的行为锁：存储阻塞时请求
// 协程仍能入队返回（每请求不再承担 WATCH 往返），恢复后事件按原时间顺序
// 合并落库。
package gatewaycallertier

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingAsyncStore struct {
	release chan struct{}
	mu      sync.Mutex
	blob    []byte
}

func (*blockingAsyncStore) asynchronousMutation() {}

func (s *blockingAsyncStore) Mutate(ctx context.Context, _ string, mutator func([]byte) ([]byte, error)) error {
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated, err := mutator(s.blob)
	if err != nil {
		return err
	}
	s.blob = updated
	return nil
}

func (s *blockingAsyncStore) state() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return unmarshalState(s.blob, time.Now())
}

func TestRecorderQueueFullDropsAndCounts(t *testing.T) {
	store := &blockingAsyncStore{release: make(chan struct{})}
	var queueFull atomic.Int64
	recorder, err := NewRecorder(store, Metrics{
		RecordTotal: func(name, kind string) {
			if name == "error" && kind == "queue_full" {
				queueFull.Add(1)
			}
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = recorder.Close(closeCtx)
	})

	// 先让 worker 卡在存储上，再填满队列；worker 会先 drain 最多
	// recorderDrainBatch 条再进入 Mutate，所以总事件数要覆盖
	// 「一批在 worker 手里 + 整条队列 + 溢出量」。
	recorder.RecordRequest(context.Background(), "key-full")
	for index := 0; index < recorderDrainBatch+recorderQueueCapacity+100; index++ {
		recorder.RecordRequest(context.Background(), "key-full")
	}
	if queueFull.Load() == 0 {
		t.Fatal("队列满时必须计 queue_full 丢弃")
	}
	close(store.release)
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Flush(flushCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderAsyncMutationDoesNotBlockRequests(t *testing.T) {
	store := &blockingAsyncStore{release: make(chan struct{})}
	recorder, err := NewRecorder(store, Metrics{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = recorder.Close(closeCtx)
	})

	returned := make(chan struct{})
	go func() {
		for index := 0; index < 100; index++ {
			recorder.RecordRequest(context.Background(), "key-async")
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordRequest 被存储阻塞：请求路径仍承担了同步写")
	}

	close(store.release)
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := recorder.Flush(flushCtx); err != nil {
		t.Fatal(err)
	}
	if got := store.state().DayRequests; got != 100 {
		t.Fatalf("异步合并后 DayRequests = %d，want 100", got)
	}
}
