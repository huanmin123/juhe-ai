package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// RedisKeyModelRuntimeStore 解析/错误分支（EvalRunner 脚本化，不连真实 Redis）
// ---------------------------------------------------------------------------

// weScriptedEval 记录 EVAL 调用并按脚本队列返回。
type weScriptedEval struct {
	results []weEvalResult
	calls   int
}

type weEvalResult struct {
	value any
	err   error
}

func (e *weScriptedEval) run(script string, keys []string, args []string) (any, error) {
	index := e.calls
	e.calls++
	if index >= len(e.results) {
		return nil, errors.New("脚本未安排该次调用")
	}
	return e.results[index].value, e.results[index].err
}

func weNewEvalStore(t *testing.T, runner func(string, []string, []string) (any, error)) *RedisKeyModelRuntimeStore {
	t.Helper()
	store, err := NewRedisKeyModelRuntimeStore(KeyModelRedisStoreOptions{
		RedisURL:   "redis://localhost:6379/0",
		Namespace:  "we-eval",
		EvalRunner: runner,
	})
	if err != nil {
		t.Fatalf("构造 eval store 失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestWeRedisRecordFailureStatusBranches(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	weAPPLIED := []any{"applied", `{"capabilityHash":"` + mustCapabilityHash(capability) + `","dispatchRevision":3,"generation":1,"phase":"OPEN"}`}

	tests := []struct {
		name      string
		results   []weEvalResult
		wantStaus KeyModelMutationStatus
		wantErr   string
	}{
		{
			name:      "applied 返回状态投影",
			results:   []weEvalResult{{value: weAPPLIED}},
			wantStaus: KeyModelMutationApplied,
		},
		{
			name:      "capacity_exhausted 无状态投影",
			results:   []weEvalResult{{value: []any{"capacity_exhausted", ""}}},
			wantStaus: "capacity_exhausted",
		},
		{
			name:      "stale 无状态投影",
			results:   []weEvalResult{{value: []any{"stale", ""}}},
			wantStaus: KeyModelMutationStale,
		},
		{
			name:    "未知状态报错",
			results: []weEvalResult{{value: []any{"bogus", ""}}},
			wantErr: "未知状态",
		},
		{
			name:    "非数组返回值报错",
			results: []weEvalResult{{value: "not-array"}},
			wantErr: "必须为数组",
		},
		{
			name:    "状态非字符串报错",
			results: []weEvalResult{{value: []any{struct{}{}, ""}}},
			wantErr: "必须为字符串",
		},
		{
			name:    "状态载荷解析失败报错",
			results: []weEvalResult{{value: []any{"applied", "{bad json"}}},
			wantErr: "invalid character",
		},
		{
			name:    "EVAL 两次失败包装错误",
			results: []weEvalResult{{err: errors.New("第一次失败")}, {err: errors.New("第二次失败")}},
			wantErr: "连续两次失败",
		},
		{
			name:      "第一次失败第二次成功恢复",
			results:   []weEvalResult{{err: errors.New("抖动")}, {value: weAPPLIED}},
			wantStaus: KeyModelMutationApplied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scripted := &weScriptedEval{results: tt.results}
			store := weNewEvalStore(t, scripted.run)
			result, err := store.RecordFailure(ctx, memoryIntent(capability, "i-1", 1_000))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want 含 %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("未预期的错误: %v", err)
			}
			if result.Status != tt.wantStaus {
				t.Fatalf("status = %s, want %s", result.Status, tt.wantStaus)
			}
		})
	}
}

func TestWeRedisRecordFailureIntentValidation(t *testing.T) {
	store := weNewEvalStore(t, nil)
	ctx := context.Background()

	// 空字段/错误 outcome 等在 EVAL 之前即被拒绝。
	if _, err := store.RecordFailure(ctx, memoryIntent(testCapability(), " ", 1)); err == nil {
		t.Fatal("空 intentId 应报错")
	}
	if _, err := store.RecordFailure(ctx, memoryIntent(testCapability(), "i-1", 0)); err == nil {
		t.Fatal("observedAtMs=0 应报错")
	}
	intent := memoryIntent(testCapability(), "i-1", 1_000)
	intent.Outcome = KeyModelOutcomeUnknown
	if _, err := store.RecordFailure(ctx, intent); err == nil {
		t.Fatal("非法 outcome 应报错")
	}
	// permit 与 capability 不匹配。
	hash := mustCapabilityHash(testCapability())
	if _, err := store.RecordFailure(ctx, KeyModelFailureIntent{
		IntentID: "i-2", RequestID: "r", AttemptID: "a", Capability: testCapability(),
		ObservedAtMs: 1_000, Outcome: KeyModelOutcomeUpstreamNotComplete, SourceFence: "f",
		Permit: &KeyModelForegroundPermit{CapabilityHash: strings.Repeat("ff", 32), AttemptID: "a"},
	}); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("permit 不匹配应报错: %v", err)
	}
	_ = hash
}

func TestWeRedisAdmitForegroundBranches(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	hash := mustCapabilityHash(capability)

	tests := []struct {
		name    string
		results []weEvalResult
		want    KeyModelForegroundDecision
		wantErr string
	}{
		{name: "busy", results: []weEvalResult{{value: []any{"busy", "0", "0"}}}, want: ForegroundBusy},
		{name: "blocked", results: []weEvalResult{{value: []any{"blocked", "0", "0"}}}, want: ForegroundBlocked},
		{
			name:    "admitted 带租约",
			results: []weEvalResult{{value: []any{"admitted", "0", "123456"}}},
			want:    ForegroundAdmitted,
		},
		{name: "idempotent 视为 admitted", results: []weEvalResult{{value: []any{"idempotent", "0", "99"}}}, want: ForegroundAdmitted},
		{name: "未知状态报错", results: []weEvalResult{{value: []any{"nope", "0", "0"}}}, wantErr: "未知状态"},
		{name: "wake 序列非数字报错", results: []weEvalResult{{value: []any{"busy", "abc", "0"}}}, wantErr: "数字结果无效"},
		{name: "wake 序列为负报错", results: []weEvalResult{{value: []any{"busy", "-1", "0"}}}, wantErr: "数字结果无效"},
		{name: "租约非数字报错", results: []weEvalResult{{value: []any{"admitted", "0", "xyz"}}}, wantErr: "数字结果无效"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scripted := &weScriptedEval{results: tt.results}
			store := weNewEvalStore(t, scripted.run)
			result, err := store.AdmitForeground(ctx, capability, "attempt-1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want 含 %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("未预期错误: %v", err)
			}
			if result.Status != tt.want {
				t.Fatalf("status = %s, want %s", result.Status, tt.want)
			}
			if tt.want == ForegroundAdmitted && result.Permit == nil {
				t.Fatal("admitted 必须携带 permit")
			}
		})
	}

	// attemptId 缺失与非法 capability 在 EVAL 之前拒绝。
	scripted := &weScriptedEval{}
	store := weNewEvalStore(t, scripted.run)
	if _, err := store.AdmitForeground(ctx, capability, "  "); err == nil {
		t.Fatal("空 attemptId 应报错")
	}
	bad := capability
	bad.KeyFingerprint = ""
	if _, err := store.AdmitForeground(ctx, bad, "a"); err == nil {
		t.Fatal("非法 capability 应报错")
	}
	if scripted.calls != 0 {
		t.Fatalf("拒绝路径不应触发 EVAL, got %d", scripted.calls)
	}
	_ = hash
}

func TestWeRedisReleaseAndRenewForeground(t *testing.T) {
	ctx := context.Background()
	permit := KeyModelForegroundPermit{CapabilityHash: strings.Repeat("ab", 32), AttemptID: "attempt-1"}

	t.Run("release 1/0 与参数校验", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{{value: []any{int64(1)}}, {value: []any{"0"}}, {value: "bad"}}}
		store := weNewEvalStore(t, scripted.run)
		released, err := store.ReleaseForeground(ctx, permit)
		if err != nil || !released {
			t.Fatalf("released = %v err = %v", released, err)
		}
		released, err = store.ReleaseForeground(ctx, permit)
		if err != nil || released {
			t.Fatalf("0 应为未释放: %v err = %v", released, err)
		}
		if _, err := store.ReleaseForeground(ctx, permit); err == nil {
			t.Fatal("非数组返回应报错")
		}
		if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: "short", AttemptID: "a"}); err == nil {
			t.Fatal("非法 hash 应报错")
		}
		if _, err := store.ReleaseForeground(ctx, KeyModelForegroundPermit{CapabilityHash: permit.CapabilityHash, AttemptID: " "}); err == nil {
			t.Fatal("空 attemptId 应报错")
		}
	})

	t.Run("renew renewed/lost/未知状态", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{
			{value: []any{"renewed", "5000"}},
			{value: []any{"lost", "0"}},
			{value: []any{"weird", "0"}},
			{value: []any{"renewed", "bad"}},
		}}
		store := weNewEvalStore(t, scripted.run)
		renewed, err := store.RenewForeground(ctx, permit)
		if err != nil || renewed == nil || renewed.LeaseUntilMs != 5000 {
			t.Fatalf("renewed = %+v err = %v", renewed, err)
		}
		lost, err := store.RenewForeground(ctx, permit)
		if err != nil || lost != nil {
			t.Fatalf("lost = %+v err = %v", lost, err)
		}
		if _, err := store.RenewForeground(ctx, permit); err == nil {
			t.Fatal("未知状态应报错")
		}
		if _, err := store.RenewForeground(ctx, permit); err == nil {
			t.Fatal("非法租约数字应报错")
		}
	})
}

func TestWeRedisMainProbeFenceAndJ1(t *testing.T) {
	ctx := context.Background()
	capability := testCapability()
	hash := mustCapabilityHash(capability)
	permit := KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: "attempt-1"}

	t.Run("RecordMainProbeFailure", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{{value: []any{"applied", int64(1)}}}}
		store := weNewEvalStore(t, scripted.run)
		if err := store.RecordMainProbeFailure(ctx, capability, permit); err != nil {
			t.Fatalf("err = %v", err)
		}
		mismatch := permit
		mismatch.CapabilityHash = strings.Repeat("cd", 32)
		if err := store.RecordMainProbeFailure(ctx, capability, mismatch); err == nil {
			t.Fatal("permit 不匹配应报错")
		}
		if err := store.RecordMainProbeFailure(ctx, CapabilityKey{CredentialSourceAccountID: "x"}, permit); err == nil {
			t.Fatal("非法 capability 应报错")
		}
	})

	t.Run("ClearMainProbeFence", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{{value: []any{int64(1)}}, {value: []any{"0"}}}}
		store := weNewEvalStore(t, scripted.run)
		fence := KeyModelFenceReference{CapabilityHash: hash, KeyFingerprint: "fp-1", DispatchRevision: 3, OwnerID: "attempt-1"}
		cleared, err := store.ClearMainProbeFence(ctx, fence, "fp-1")
		if err != nil || !cleared {
			t.Fatalf("cleared = %v err = %v", cleared, err)
		}
		if cleared, err := store.ClearMainProbeFence(ctx, fence, "fp-1"); err != nil || cleared {
			t.Fatalf("0 应为未清理: %v err = %v", cleared, err)
		}
		// winner 不一致 → false（无需 EVAL）。
		if cleared, err := store.ClearMainProbeFence(ctx, fence, "other"); err != nil || cleared {
			t.Fatalf("winner 不一致应 false: %v err = %v", cleared, err)
		}
		badRevision := fence
		badRevision.DispatchRevision = 0
		if cleared, _ := store.ClearMainProbeFence(ctx, badRevision, "fp-1"); cleared {
			t.Fatal("非法 revision 应 false")
		}
		noOwner := fence
		noOwner.OwnerID = " "
		if _, err := store.ClearMainProbeFence(ctx, noOwner, "fp-1"); err == nil {
			t.Fatal("空 owner 应报错")
		}
	})

	t.Run("DeferMainProbeFence", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{{value: []any{int64(1)}}, {value: []any{int64(0)}}}}
		store := weNewEvalStore(t, scripted.run)
		fence := KeyModelFenceReference{CapabilityHash: hash, OwnerID: "attempt-1"}
		deferred, err := store.DeferMainProbeFence(ctx, fence)
		if err != nil || !deferred {
			t.Fatalf("deferred = %v err = %v", deferred, err)
		}
		if deferred, err = store.DeferMainProbeFence(ctx, fence); err != nil || deferred {
			t.Fatalf("0 应为未延后: %v err = %v", deferred, err)
		}
		if _, err := store.DeferMainProbeFence(ctx, KeyModelFenceReference{CapabilityHash: "x", OwnerID: "o"}); err == nil {
			t.Fatal("非法 hash 应报错")
		}
	})

	t.Run("ClaimJ1Confirmation", func(t *testing.T) {
		scripted := &weScriptedEval{results: []weEvalResult{{value: []any{"claimed"}}, {value: []any{"limited"}}}}
		store := weNewEvalStore(t, scripted.run)
		claimed, err := store.ClaimJ1Confirmation(ctx, "source-1", 3)
		if err != nil || !claimed {
			t.Fatalf("claimed = %v err = %v", claimed, err)
		}
		claimed, err = store.ClaimJ1Confirmation(ctx, "source-1", 3)
		if err != nil || claimed {
			t.Fatalf("limited 应为 false: %v err = %v", claimed, err)
		}
		if _, err := store.ClaimJ1Confirmation(ctx, " ", 3); err == nil {
			t.Fatal("空 sourceAccount 应报错")
		}
	})
}

func TestWeRedisFiniteIntegerHelpers(t *testing.T) {
	if value, err := finiteRedisInteger(int64(7)); err != nil || value != 7 {
		t.Fatalf("int64 = %d err = %v", value, err)
	}
	if value, err := finiteRedisInteger(" 42 "); err != nil || value != 42 {
		t.Fatalf("string = %d err = %v", value, err)
	}
	for _, bad := range []any{struct{}{}, "abc", "-5", "1.5"} {
		if _, err := finiteRedisInteger(bad); err == nil {
			t.Fatalf("%v 应报错", bad)
		}
	}
	// 超过 Number.MAX_SAFE_INTEGER 拒绝。
	if _, err := finiteRedisInteger("9223372036854775807"); err == nil {
		t.Fatal("超过 safe integer 应报错")
	}
}
