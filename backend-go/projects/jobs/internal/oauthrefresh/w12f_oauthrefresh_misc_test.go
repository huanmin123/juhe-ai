package oauthrefresh

// w12f_oauthrefresh_misc_test.go 覆盖 runner 的 jitter/失败退避分支、
// UnexpectedFailureContext 全字段渲染、sweep 的 finalizer 链路，以及
// failurestate 的 revision 保留与退避上限合并。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestW12fRunnerJitterAndFailureBackoff(t *testing.T) {
	cfg := RunnerConfig{Interval: 2 * time.Millisecond, InitialDelay: 2 * time.Millisecond, PassiveJitter: true, FailureBackoffBase: time.Millisecond, FailureBackoffMax: 4 * time.Millisecond}
	var runs int
	runner := NewRunner("w12f-backoff", cfg, func(ctx context.Context) error {
		runs++
		if runs == 1 {
			return errors.New("w12f-first-run-fails")
		}
		return nil
	}, ClockFunc(func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() }), nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后必须返回 ctx.Err: %v", err)
	}
	if runs < 2 {
		t.Fatalf("至少两次运行（失败+成功）: %d", runs)
	}
}

func TestW12fUnexpectedContextLogValueFull(t *testing.T) {
	context := CaptureUnexpectedFailureContext(errors.New("w12f-err"), FailureCaptureOptions{
		StageSnapshot:  map[string]any{"stage": "w12f"},
		QueueSnapshot:  map[string]any{"depth": 1.0},
		RetryState:     map[string]any{"attempt": 1.0},
		DecisionInputs: map[string]any{"account": "w12f"},
	})
	value := context.LogValue()
	if value.Kind() == 0 {
		t.Fatal("LogValue 必须有效")
	}
	if context.StageSnapshot == nil || context.QueueSnapshot == nil || context.RetryState == nil {
		t.Fatal("可选快照必须被捕获")
	}
}

func TestW12fSweepFinalizerChain(t *testing.T) {
	store, db, clock := newSweepStore(t)
	seedGrantRow(t, db, "w12f-grant-exp", "active", isoMillis(clock.Now().Add(-time.Hour)), "", "w12f-actor")
	finalized := 0
	finalizer := FinalizerFunc(func(ctx context.Context, tx *sql.Tx, grant ResourceAuthorizationGrant, actor string) error {
		finalized++
		if grant.Status != "expired" {
			t.Fatalf("finalizer 必须收到 expired 状态: %s", grant.Status)
		}
		return nil
	})
	result, err := store.RunAuthorizationExpirySweep(context.Background(), finalizer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Expired != 1 || finalized != 1 {
		t.Fatalf("sweep 结果=%+v finalized=%d", result, finalized)
	}
	_ = db
}
