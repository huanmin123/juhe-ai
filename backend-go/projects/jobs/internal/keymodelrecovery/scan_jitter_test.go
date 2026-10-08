package keymodelrecovery

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

// TestScanLoopIntervalSourceIsJitteredPerRound 固定阶段 D 抖动缺口修复契约
// （客户端版本自动跟版设计 §9）：扫描循环（Runner.Run）的等待间隔必须来自
// schedulejitter 且每轮重新求值，防止回归为相位锁定的裸 ticker。
func TestScanLoopIntervalSourceIsJitteredPerRound(t *testing.T) {
	runner := NewRunner(&woFakeStore{now: time.Unix(10_000, 0).UTC()}, woLoaderReturning(nil, nil), nil)
	// 默认间隔来源必须与 accountbalance.Service.Run 同源（platform schedulejitter）。
	if got, want := reflect.ValueOf(runner.scanDelay).Pointer(), reflect.ValueOf(schedulejitter.Delay).Pointer(); got != want {
		t.Fatalf("扫描间隔默认来源应为 schedulejitter.Delay")
	}

	var mu sync.Mutex
	var intervals []time.Duration
	runner.scanDelay = func(interval time.Duration) time.Duration {
		mu.Lock()
		intervals = append(intervals, interval)
		mu.Unlock()
		return 2 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()

	// 起跑一次 + 每轮重置一次：≥3 次调用即证明间隔按轮重新求值（裸 ticker 为 0 次）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		calls := len(intervals)
		mu.Unlock()
		if calls >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("扫描循环未按轮重新求值间隔: %d 次", calls)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后 Run 应返回 ctx.Err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Run 应退出")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, interval := range intervals {
		// 抖动只改窗口，不改任务参数（<1min 档 ±interval/2 由 jitter 单测覆盖）。
		if interval != ScanInterval {
			t.Fatalf("扫描间隔必须保持 ScanInterval: %v", interval)
		}
	}
}
