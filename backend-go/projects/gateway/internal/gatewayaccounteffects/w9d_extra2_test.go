package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// w9d（二）：EvalRunner 形状臂补齐（fence 清理/延后、续租、J1 确认、主探针
// fence 写入）与 Renew/Release 的解析分支。
// ---------------------------------------------------------------------------

func TestW9DRedisFenceAndRenewArms(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	testHash := strings.Repeat("ab", 32)
	permit := KeyModelForegroundPermit{CapabilityHash: testHash, AttemptID: "att-1"}

	newQueue := func(values []any, errs []error) *w9dQueue { return &w9dQueue{values: values, errs: errs} }

	t.Run("renew arms", func(t *testing.T) {
		// hash 缺失 / attempt 缺失。
		q := &w9dQueue{}
		store := w9dEvalStore(t, q.run)
		if _, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "", AttemptID: "a"}); err == nil {
			t.Fatal("renew empty hash must fail")
		}
		if _, err := store.RenewForeground(ctx, KeyModelForegroundPermit{CapabilityHash: testHash, AttemptID: ""}); err == nil {
			t.Fatal("renew empty attempt must fail")
		}
		// runner err。
		lost := newQueue(nil, []error{errors.New("x"), errors.New("x")})
		if _, err := w9dEvalStore(t, lost.run).RenewForeground(ctx, permit); err == nil {
			t.Fatal("renew runner err must fail")
		}
		// 非数组。
		na := newQueue([]any{"x"}, nil)
		if _, err := w9dEvalStore(t, na.run).RenewForeground(ctx, permit); err == nil {
			t.Fatal("renew non array must fail")
		}
		// status 非字符串。
		ns := newQueue([]any{[]any{struct{}{}}}, nil)
		if _, err := w9dEvalStore(t, ns.run).RenewForeground(ctx, permit); err == nil {
			t.Fatal("renew status non string must fail")
		}
		// lost。
		lostLease := newQueue([]any{[]any{"lost"}}, nil)
		if p, err := w9dEvalStore(t, lostLease.run).RenewForeground(ctx, permit); err != nil || p != nil {
			t.Fatalf("lost renew p=%v err=%v", p, err)
		}
		// until 解析失败。
		badUntil := newQueue([]any{[]any{"renewed", "junk"}}, nil)
		if _, err := w9dEvalStore(t, badUntil.run).RenewForeground(ctx, permit); err == nil {
			t.Fatal("renew bad until must fail")
		}
		// 成功。
		ok := newQueue([]any{[]any{"renewed", int64(555)}}, nil)
		if p, err := w9dEvalStore(t, ok.run).RenewForeground(ctx, permit); err != nil || p == nil || p.LeaseUntilMs != 555 {
			t.Fatalf("renew p=%+v err=%v", p, err)
		}
	})

	t.Run("release arms", func(t *testing.T) {
		nonArray := newQueue([]any{"x"}, nil)
		if _, err := w9dEvalStore(t, nonArray.run).ReleaseForeground(ctx, permit); err == nil {
			t.Fatal("release non array must fail")
		}
		badInt := newQueue([]any{[]any{"junk"}}, nil)
		if _, err := w9dEvalStore(t, badInt.run).ReleaseForeground(ctx, permit); err == nil {
			t.Fatal("release bad int must fail")
		}
		ok := newQueue([]any{[]any{int64(1)}}, nil)
		released, err := w9dEvalStore(t, ok.run).ReleaseForeground(ctx, permit)
		if err != nil || !released {
			t.Fatalf("release=%v err=%v", released, err)
		}
	})

	t.Run("main probe fence", func(t *testing.T) {
		// RecordMainProbeFailure：EVAL err / permit 不匹配 / 能力无效。
		q := newQueue(nil, []error{errors.New("x"), errors.New("x")})
		if err := w9dEvalStore(t, q.run).RecordMainProbeFailure(ctx, capability, KeyModelForegroundPermit{CapabilityHash: testHash, AttemptID: "att-1"}); err == nil {
			t.Fatal("fence write eval err must fail")
		}
		if err := w9dEvalStore(t, newQueue(nil, nil).run).RecordMainProbeFailure(ctx, capability, KeyModelForegroundPermit{CapabilityHash: "other", AttemptID: "att-1"}); err == nil {
			t.Fatal("fence permit mismatch must fail")
		}
		if err := w9dEvalStore(t, newQueue(nil, nil).run).RecordMainProbeFailure(ctx, CapabilityKey{}, KeyModelForegroundPermit{}); err == nil {
			t.Fatal("fence invalid capability must fail")
		}
		// ClearMainProbeFence：参数臂 + keyFingerprint != winner 幂等 + EVAL 结果。
		q0 := &w9dQueue{}
		s0 := w9dEvalStore(t, q0.run)
		if _, err := s0.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: ""}, "kf"); err == nil {
			t.Fatal("clear empty hash must fail")
		}
		if _, err := s0.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, KeyFingerprint: " "}, "kf"); err == nil {
			t.Fatal("clear blank fingerprint must fail")
		}
		if cleared, err := s0.ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, KeyFingerprint: "kf"}, "other"); err != nil || cleared {
			t.Fatalf("clear mismatch winner cleared=%v err=%v", cleared, err)
		}
		notWinner := newQueue(nil, nil)
		if cleared, err := w9dEvalStore(t, notWinner.run).ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, KeyFingerprint: "kf", DispatchRevision: 0, OwnerID: "o"}, "kf"); err != nil || cleared {
			t.Fatalf("revision guard cleared=%v err=%v", cleared, err)
		}
		missOwner := newQueue(nil, []error{nil})
		if _, err := w9dEvalStore(t, missOwner.run).ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, KeyFingerprint: "kf", DispatchRevision: 1}, "kf"); err == nil {
			t.Fatal("clear blank owner must fail")
		}
		clear := newQueue([]any{[]any{int64(1)}}, nil)
		if cleared, err := w9dEvalStore(t, clear.run).ClearMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, KeyFingerprint: "kf", DispatchRevision: 1, OwnerID: "o"}, "kf"); err != nil || !cleared {
			t.Fatalf("clear=%v err=%v", cleared, err)
		}
		// DeferMainProbeFence：参数臂 + EVAL 结果。
		dq := &w9dQueue{}
		ds := w9dEvalStore(t, dq.run)
		if _, err := ds.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: ""}); err == nil {
			t.Fatal("defer empty hash must fail")
		}
		if _, err := ds.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, OwnerID: " "}); err == nil {
			t.Fatal("defer blank owner must fail")
		}
		deferOK := newQueue([]any{[]any{int64(1)}}, nil)
		if deferred, err := w9dEvalStore(t, deferOK.run).DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: testHash, OwnerID: "o"}); err != nil || !deferred {
			t.Fatalf("defer=%v err=%v", deferred, err)
		}
	})
}
