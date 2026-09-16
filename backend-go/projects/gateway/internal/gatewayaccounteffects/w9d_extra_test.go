package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// w9d：EvalRunner 形状臂集中覆盖（admission/release/renew/J1）、参数臂、
// clock 被动调度纯函数与 attempt 预备分支。
// 不可达登记：sideeffects drain 的 epoch 过期/竞态分支与 miniredis 事件订阅
// 断连重试需要真实并发故障注入面（既有 we/miniredis 测试已覆盖主路径）。
// ---------------------------------------------------------------------------

func w9dEvalStore(t *testing.T, runner func(string, []string, []string) (any, error)) *RedisKeyModelRuntimeStore {
	t.Helper()
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL:   "redis://localhost:6379/0",
		Namespace:  "w9d-eval",
		EvalRunner: runner,
	})
	if err != nil {
		t.Fatalf("构造 eval store 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// w9dQueue 按序回放 EVAL 结果。
type w9dQueue struct {
	values []any
	errs   []error
	calls  int
}

func (q *w9dQueue) run(_ string, _ []string, _ []string) (any, error) {
	i := q.calls
	q.calls++
	if i < len(q.errs) && q.errs[i] != nil {
		return nil, q.errs[i]
	}
	if i < len(q.values) {
		return q.values[i], nil
	}
	return nil, errors.New("w9d 脚本耗尽")
}

func w9dStep(value any, err error) func(*w9dQueue) {
	return func(q *w9dQueue) {}
}

func TestW9DRedisConstructorArms(t *testing.T) {
	if _, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{RedisURL: "  ", Namespace: "w9d"}); err == nil {
		t.Fatal("blank redis url must fail")
	}
	if _, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{RedisURL: "redis://localhost:6379/0", Namespace: "bad!"}); err == nil {
		t.Fatal("bad namespace must fail")
	}
}

func TestW9DRedisGetAndRecordArms(t *testing.T) {
	ctx := context.Background()
	runner := &w9dQueue{}
	store := w9dEvalStore(t, runner.run)
	// Get：能力哈希失败臂。
	if _, err := store.Get(ctx, CapabilityKey{}); err == nil {
		t.Fatal("empty capability get must fail")
	}
	// RecordFailure：intent 校验失败。
	if _, err := store.RecordFailure(ctx, KeyModelFailureIntent{}); err == nil {
		t.Fatal("empty intent must fail")
	}
	// RecordFailure：能力无效 → CreateKeyModelOpenState err。
	bad := memoryIntent(testCapability(), "i-1", 1_000)
	bad.Capability = CapabilityKey{}
	if _, err := store.RecordFailure(ctx, bad); err == nil {
		t.Fatal("invalid capability intent must fail")
	}
	// RecordFailure：permit 哈希不匹配。
	mismatch := memoryIntent(testCapability(), "i-2", 1_000)
	mismatch.Permit = &KeyModelForegroundPermit{CapabilityHash: "other-hash"}
	if _, err := store.RecordFailure(ctx, mismatch); err == nil {
		t.Fatal("permit mismatch must fail")
	}
	// Get：合法能力 + EVAL 失败透传（runner 队列耗尽）。
	capability := testCapability()
	if _, err := store.Get(ctx, capability); err == nil {
		t.Fatal("exhausted runner get must fail")
	}
}

func TestW9DAdmissionArms(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	attempt := "attempt-1"
	step := func(q *w9dQueue) {}
	_ = step
	cases := []struct {
		name   string
		values []any
		errs   []error
		check  func(t *testing.T, r KeyModelAdmissionResult, err error)
	}{
		{"runner err", nil, []error{errors.New("boom"), errors.New("boom2")}, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil {
				t.Fatal("runner err must fail")
			}
		}},
		{"non array", []any{"text"}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil || !strings.Contains(err.Error(), "数组") {
				t.Fatalf("non array err=%v", err)
			}
		}},
		{"status non string", []any{[]any{struct{}{}, int64(1), int64(2)}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil || !strings.Contains(err.Error(), "字符串") {
				t.Fatalf("status err=%v", err)
			}
		}},
		{"wake bad int", []any{[]any{"busy", "x"}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil {
				t.Fatal("wake bad int must fail")
			}
		}},
		{"busy", []any{[]any{"busy", int64(7)}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if r.Status != KeyModelForegroundDecision("busy") || r.WakeSequence != 7 {
				t.Fatalf("busy result=%+v", r)
			}
		}},
		{"blocked", []any{[]any{"blocked", int64(8)}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if r.Status != "blocked" || r.WakeSequence != 8 {
				t.Fatalf("blocked result=%+v", r)
			}
		}},
		{"unknown status", []any{[]any{"bogus", int64(1), int64(2)}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil || !strings.Contains(err.Error(), "未知状态") {
				t.Fatalf("unknown err=%v", err)
			}
		}},
		{"lease bad int", []any{[]any{"admitted", int64(1), "x"}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if err == nil {
				t.Fatal("lease bad int must fail")
			}
		}},
		{"admitted", []any{[]any{"admitted", int64(1), int64(123)}}, nil, func(t *testing.T, r KeyModelAdmissionResult, err error) {
			if r.Status != ForegroundAdmitted || r.Permit == nil || r.Permit.LeaseUntilMs != 123 {
				t.Fatalf("admitted result=%+v", r)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &w9dQueue{values: tc.values, errs: tc.errs}
			store := w9dEvalStore(t, q.run)
			result, err := store.AdmitForeground(ctx, capability, attempt)
			tc.check(t, result, err)
		})
	}
	// 参数臂：空能力 / 空 attempt。
	q := &w9dQueue{}
	store := w9dEvalStore(t, q.run)
	if _, err := store.AdmitForeground(ctx, CapabilityKey{}, attempt); err == nil {
		t.Fatal("empty capability admit must fail")
	}
	if _, err := store.AdmitForeground(ctx, capability, "  "); err == nil {
		t.Fatal("blank attempt admit must fail")
	}
	// Release / Renew：hash 与 attempt 校验臂。
	if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "", AttemptID: "a"}); err == nil {
		t.Fatal("release empty hash must fail")
	}
	if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "hash", AttemptID: " "}); err == nil {
		t.Fatal("release blank attempt must fail")
	}
	if _, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "", AttemptID: "a"}); err == nil {
		t.Fatal("renew empty hash must fail")
	}
	if _, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "hash", AttemptID: " "}); err == nil {
		t.Fatal("renew blank attempt must fail")
	}
}

func TestW9DClockPassiveArms(t *testing.T) {
	random := func(v float64) func() float64 { return func() float64 { return v } }
	// 零窗口返回 0。
	if got := passiveScheduleOffsetWithinWindowMs(0, random(0.5)); got != 0 {
		t.Fatalf("zero window offset=%d", got)
	}
	// NaN / 负 / 超界采样截断（NaN 归零 → 对称窗口内允许负偏移）。
	for _, sampled := range []float64{float64Nan(), -0.5, 1.5} {
		offset := passiveScheduleOffsetWithinWindowMs(1000, random(sampled))
		if offset < -1000 || offset > 1000 {
			t.Fatalf("sampled %v offset=%d", sampled, offset)
		}
	}
	if got := passiveScheduleOffsetMs(1000, random(0.25)); got < -500 || got > 500 {
		t.Fatalf("offset=%d", got)
	}
	if got := passiveScheduleDelayMs(0, nil); got < 1 {
		t.Fatalf("delay=%d", got)
	}
}

func float64Nan() float64 {
	nan := 0.0
	return nan / nan
}

func TestW9DAttemptPrepareNonAdmitted(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	// admission 返回 busy → 预备结果直接投影 busy 状态。
	q := &w9dQueue{values: []any{[]any{"busy", int64(3)}}}
	store := w9dEvalStore(t, q.run)
	route := GatewayKeyModelCapability{AccountID: "acc", Capability: capability}
	prep, err := PrepareGatewayKeyModelAttempt(ctx, store, PrepareGatewayKeyModelAttemptInput{
		Route:     route,
		RequestID: "req-1",
		AttemptID: "att-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Status != AttemptPreparationStatus("busy") || prep.CapabilityHash == "" {
		t.Fatalf("prep=%+v", prep)
	}
}
