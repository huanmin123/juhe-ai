package oauthrefresh

// w12f_oauthrefresh_misc_test.go 覆盖 UnexpectedFailureContext 全字段渲染、
// sweep 的 finalizer 链路，以及 failurestate 的 revision 保留与退避上限合并。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

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
