package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// fakeWriterLeaseKeeper 按 writer_lease_reviver.go 的并行契约模拟写租约
// keeper：lose() 关闭 Lost() 通道模拟租约 terminal；Revive 逐次返回
// reviveErrs 中的错误，弹尽即模拟复活成功——lostErr 清零、Lost() 换成
// 未关闭的新通道（与真实 keeper 复活后换新通道一致）。
type fakeWriterLeaseKeeper struct {
	mu          sync.Mutex
	lostCh      chan struct{}
	lostErr     error
	reviveErrs  []error
	reviveCalls int
}

func newFakeWriterLeaseKeeper() *fakeWriterLeaseKeeper {
	return &fakeWriterLeaseKeeper{lostCh: make(chan struct{})}
}

func (f *fakeWriterLeaseKeeper) Lost() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lostCh
}

func (f *fakeWriterLeaseKeeper) LostError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lostErr
}

func (f *fakeWriterLeaseKeeper) Revive(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviveCalls++
	if len(f.reviveErrs) > 0 {
		err := f.reviveErrs[0]
		f.reviveErrs = f.reviveErrs[1:]
		if err != nil {
			return err
		}
	}
	f.lostErr = nil
	f.lostCh = make(chan struct{})
	return nil
}

// lose 模拟 fatal：记录丢失原因并关闭 Lost() 通道。
func (f *fakeWriterLeaseKeeper) lose(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lostErr = err
	close(f.lostCh)
}

func (f *fakeWriterLeaseKeeper) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reviveCalls
}

func reviverTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitForKeeperCalls 轮询 fake 的 Revive 调用次数直到达到 want 或超时，
// 返回达标记；超时由调用方断言失败。
func waitForKeeperCalls(t *testing.T, keeper *fakeWriterLeaseKeeper, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if keeper.calls() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWriterLeaseReviverRevivesOnceThenStaysHealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keeper := newFakeWriterLeaseKeeper()
	keeper.lose(errors.New("lease lost by fake fatal"))
	done := make(chan error, 1)
	go func() {
		done <- runWriterLeaseReviver(ctx, keeper, 10*time.Millisecond, reviverTestLogger())
	}()
	waitForKeeperCalls(t, keeper, 1)
	if keeper.calls() == 0 {
		t.Fatalf("runner did not attempt Revive after lease loss")
	}
	// 复活成功后 keeper 恢复健康（Lost() 已换新通道）：runner 应回到外层
	// select 等待，不再触发 Revive。
	time.Sleep(40 * time.Millisecond)
	if got := keeper.calls(); got != 1 {
		t.Fatalf("healthy keeper triggered extra Revive attempts: got %d, want 1", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner returned error after graceful stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not stop after ctx cancel")
	}
}

func TestWriterLeaseReviverRetriesUntilSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keeper := newFakeWriterLeaseKeeper()
	keeper.lose(errors.New("lease lost by fake fatal"))
	keeper.reviveErrs = []error{errors.New("F3 audit owner lease 仍被其他 owner 持有"), errors.New("renew transport down")}
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		done <- runWriterLeaseReviver(ctx, keeper, 10*time.Millisecond, reviverTestLogger())
	}()
	waitForKeeperCalls(t, keeper, 3)
	if got := keeper.calls(); got != 3 {
		t.Fatalf("runner did not retry to success: got %d attempts, want 3", got)
	}
	// 首次立即尝试 + 两次 10ms 周期重试：下界只断言重试节拍被遵守，
	// 不设上界以免调度抖动导致假失败。
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("runner did not honour retry cadence: elapsed %v, want >= 15ms", elapsed)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner returned error after graceful stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not stop after ctx cancel")
	}
}

func TestWriterLeaseReviverStopsOnClosedKeeper(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keeper := newFakeWriterLeaseKeeper()
	keeper.lose(errors.New("lease lost by fake fatal"))
	keeper.reviveErrs = []error{auditlog.ErrKeeperClosed, errors.New("never reached")}
	done := make(chan error, 1)
	go func() {
		done <- runWriterLeaseReviver(ctx, keeper, 10*time.Millisecond, reviverTestLogger())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("closed keeper must exit gracefully (nil), got error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not stop on closed-class Revive error")
	}
	if got := keeper.calls(); got != 1 {
		t.Fatalf("runner kept retrying after closed-class error: got %d attempts, want 1", got)
	}
}

func TestWriterLeaseReviverReturnsNilOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	keeper := newFakeWriterLeaseKeeper() // 健康态：Lost() 永不关闭
	done := make(chan error, 1)
	go func() {
		done <- runWriterLeaseReviver(ctx, keeper, 10*time.Millisecond, reviverTestLogger())
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runner must return nil on ctx cancel, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not return after ctx cancel")
	}
	if got := keeper.calls(); got != 0 {
		t.Fatalf("healthy keeper must not be revived: got %d Revive calls", got)
	}
}

func TestWriterLeaseReviverNilKeeperReturnsImmediately(t *testing.T) {
	if err := runWriterLeaseReviver[*fakeWriterLeaseKeeper](context.Background(), nil, 10*time.Millisecond, reviverTestLogger()); err != nil {
		t.Fatalf("nil keeper must return nil immediately, got: %v", err)
	}
}

// TestWriterLeaseReviverClosedClassClassification 钉住 BUG-0360 哨兵化后的
// 关闭态判定：只有 auditlog/operationlog 两包的 ErrKeeperClosed（含包装链）
// 命中；错误文案巧合含「已关闭 / closed」的普通错误不再被桥接匹配。
func TestWriterLeaseReviverClosedClassClassification(t *testing.T) {
	scenarios := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"auditlog sentinel", auditlog.ErrKeeperClosed, true},
		{"wrapped auditlog sentinel", fmt.Errorf("revive F3 keeper: %w", auditlog.ErrKeeperClosed), true},
		{"operationlog sentinel", operationlog.ErrKeeperClosed, true},
		{"wrapped operationlog sentinel", fmt.Errorf("revive F4 keeper: %w", operationlog.ErrKeeperClosed), true},
		{"chinese closed wording without sentinel", errors.New("F3 audit lease keeper 已关闭"), false},
		{"english closed wording without sentinel", errors.New("lease keeper is closed"), false},
		{"transport failure", errors.New("connection refused"), false},
		{"lease held elsewhere", errors.New("重启后 F4 operation log owner lease 仍被其他 owner 持有"), false},
		{"ctx canceled", context.Canceled, false},
	}
	for _, scenario := range scenarios {
		if got := writerLeaseKeeperClosed(scenario.err); got != scenario.want {
			t.Fatalf("%s: writerLeaseKeeperClosed=%v, want %v (err=%v)", scenario.name, got, scenario.want, scenario.err)
		}
	}
}
