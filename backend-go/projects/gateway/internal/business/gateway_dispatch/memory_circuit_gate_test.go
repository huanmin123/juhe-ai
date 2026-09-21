package gatewaydispatch

// MemoryCircuitGate 契约测试（2026-09-22 单机零配置恒装配）：表驱动覆盖与
// RuntimeCircuitGate 对齐的决策矩阵——closed 直通、OPEN/SUSPECT 退避拦截、
// SUSPECT 确认资格与租约互斥、HALF_OPEN/RECOVERING canary 租约、transport
// failure 退避升级、unknown 中性、framing complete 恢复路径。内部状态按
// wk_runtime_gate_test.go 的 patch 手法直接注入（同包测试）。

import (
	"context"
	"testing"
	"time"

	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

func memoryGateInput(accountID, evidence string) AccountCircuitInput {
	return AccountCircuitInput{AccountID: accountID, RequestLane: "text", Model: "m", DispatchRevision: 1, ConfirmationLeaseDuration: time.Minute, ConfirmationEligible: true, FailureEvidenceKey: evidence}
}

// memoryPatchState 直接改写进程内状态（同包测试注入，等价 Redis 测试的
// wkPatchRetryAt/wkPatchPhase 手法）。patch 为 nil 表示删除账户记录。
func memoryPatchState(t *testing.T, gate *MemoryCircuitGate, accountID string, patch func(state *memoryCircuitState)) {
	t.Helper()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if patch == nil {
		delete(gate.states, accountID)
		return
	}
	state := gate.states[accountID]
	if state == nil {
		state = &memoryCircuitState{phase: circuitruntime.GatewayAccountCircuitPhaseClosed}
		gate.states[accountID] = state
	}
	patch(state)
}

func memoryPhase(t *testing.T, gate *MemoryCircuitGate, accountID string) circuitruntime.GatewayAccountCircuitPhase {
	t.Helper()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	state := gate.states[accountID]
	if state == nil {
		return circuitruntime.GatewayAccountCircuitPhaseClosed
	}
	return state.phase
}

func TestMemoryCircuitGateDecisionMatrix(t *testing.T) {
	ctx := context.Background()
	gate := NewMemoryCircuitGate()

	t.Run("空 account id 必须失败", func(t *testing.T) {
		if _, _, err := gate.Prepare(ctx, AccountCircuitInput{AccountID: "   "}); err == nil {
			t.Fatal("空 account id 必须失败")
		}
	})

	t.Run("closed 直通且不带租约", func(t *testing.T) {
		decision, attempt, err := gate.Prepare(ctx, memoryGateInput("a-closed", "ev-1"))
		if err != nil || decision != AccountCircuitDispatchable {
			t.Fatalf("closed 电路应直接放行: %s %v", decision, err)
		}
		memory := attempt.(*memoryCircuitAttempt)
		if memory.leaseID != "" {
			t.Fatalf("closed 直通不应带租约: %q", memory.leaseID)
		}
		// 无租约时 framing/unknown 都是中性 no-op，且不得创建状态。
		if err := attempt.ReportFramingComplete(ctx); err != nil {
			t.Fatalf("closed framing: %v", err)
		}
		if _, unknownAttempt, err := gate.Prepare(ctx, memoryGateInput("a-closed", "ev-1b")); err != nil {
			t.Fatal(err)
		} else if err := unknownAttempt.ReportUnknown(ctx); err != nil {
			t.Fatalf("closed unknown 必须中性: %v", err)
		}
		if phase := memoryPhase(t, gate, "a-closed"); phase != circuitruntime.GatewayAccountCircuitPhaseClosed {
			t.Fatalf("closed 中性上报后必须仍是 CLOSED: %s", phase)
		}
	})

	t.Run("transport failure 进入 SUSPECT 且退避期内拦截", func(t *testing.T) {
		_, failureAttempt, err := gate.Prepare(ctx, memoryGateInput("a-suspect", "ev-2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := failureAttempt.ReportTransportFailure(ctx, context.DeadlineExceeded); err != nil {
			t.Fatalf("transport failure: %v", err)
		}
		if phase := memoryPhase(t, gate, "a-suspect"); phase != circuitruntime.GatewayAccountCircuitPhaseSuspect {
			t.Fatalf("传输失败后必须进入 SUSPECT: %s", phase)
		}
		// SUSPECT 不设 retryAt：与 Redis 版一致地持续拦截。
		for _, eligible := range []bool{true, false} {
			input := memoryGateInput("a-suspect", "ev-2b")
			input.ConfirmationEligible = eligible
			decision, attempt, err := gate.Prepare(ctx, input)
			if err != nil || decision != AccountCircuitBlocked || attempt != nil {
				t.Fatalf("SUSPECT 无 retryAt 必须拦截: %s %v %v", decision, attempt, err)
			}
		}
		// retryAt 在未来时同样拦截（不论确认资格）。
		memoryPatchState(t, gate, "a-suspect", func(state *memoryCircuitState) {
			state.retryAt = time.Now().UTC().Add(time.Hour)
		})
		input := memoryGateInput("a-suspect", "ev-2c")
		decision, attempt, err := gate.Prepare(ctx, input)
		if err != nil || decision != AccountCircuitBlocked || attempt != nil {
			t.Fatalf("退避期内必须拦截: %s %v %v", decision, attempt, err)
		}
	})

	t.Run("SUSPECT 非资格拦截、资格获确认租约且租约互斥", func(t *testing.T) {
		memoryPatchState(t, gate, "a-confirm", func(state *memoryCircuitState) {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseSuspect
			state.generation = 1
			state.retryAt = time.Now().UTC().Add(-time.Minute)
		})
		ineligible := memoryGateInput("a-confirm", "ev-3a")
		ineligible.ConfirmationEligible = false
		decision, attempt, err := gate.Prepare(ctx, ineligible)
		if err != nil || decision != AccountCircuitBlocked || attempt != nil {
			t.Fatalf("无确认资格必须拦截: %s %v %v", decision, attempt, err)
		}
		decision, confirmAttempt, err := gate.Prepare(ctx, memoryGateInput("a-confirm", "ev-3b"))
		if err != nil || decision != AccountCircuitDispatchable {
			t.Fatalf("确认探测被拒: %s %v", decision, err)
		}
		confirmed := confirmAttempt.(*memoryCircuitAttempt)
		if confirmed.leaseID == "" || confirmed.leaseKind != circuitruntime.GatewayAccountCircuitLeaseConfirmation {
			t.Fatalf("未取得确认租约: %q %q", confirmed.leaseID, confirmed.leaseKind)
		}
		// 租约持有期间再次 Prepare 必须被拦截（租约互斥）。
		if decision, attempt, err := gate.Prepare(ctx, memoryGateInput("a-confirm", "ev-3c")); err != nil || decision != AccountCircuitBlocked || attempt != nil {
			t.Fatalf("确认租约互斥期内必须拦截: %s %v %v", decision, attempt, err)
		}
		// framing 完成相位前进到 RECOVERING（retryAt = now+3s），再次 Prepare 拦截。
		if err := confirmAttempt.ReportFramingComplete(ctx); err != nil {
			t.Fatalf("确认结算 framing: %v", err)
		}
		if phase := memoryPhase(t, gate, "a-confirm"); phase != circuitruntime.GatewayAccountCircuitPhaseRecovering {
			t.Fatalf("确认 framing 后必须进入 RECOVERING: %s", phase)
		}
		if decision, attempt, err := gate.Prepare(ctx, memoryGateInput("a-confirm", "ev-3d")); err != nil || decision != AccountCircuitBlocked || attempt != nil {
			t.Fatalf("RECOVERING 重试窗口内必须拦截: %s %v %v", decision, attempt, err)
		}
		// settled 后重复上报必须被本地围栏吞掉（不得把 RECOVERING 打成 OPEN）。
		if err := confirmAttempt.ReportTransportFailure(ctx, nil); err != nil {
			t.Fatalf("settled attempt 上报必须为 no-op: %v", err)
		}
		if phase := memoryPhase(t, gate, "a-confirm"); phase != circuitruntime.GatewayAccountCircuitPhaseRecovering {
			t.Fatalf("settled 重复上报不得改变相位: %s", phase)
		}
	})

	t.Run("RECOVERING canary 租约与三次成功恢复", func(t *testing.T) {
		memoryPatchState(t, gate, "a-canary", func(state *memoryCircuitState) {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseRecovering
			state.generation = 2
			state.retryAt = time.Now().UTC().Add(-time.Minute)
		})
		canaryInput := memoryGateInput("a-canary", "ev-4a")
		decision, attempt, err := gate.Prepare(ctx, canaryInput)
		if err != nil || decision != AccountCircuitDispatchable {
			t.Fatalf("canary 探测被拒: %s %v", decision, err)
		}
		canary := attempt.(*memoryCircuitAttempt)
		if canary.leaseKind != circuitruntime.GatewayAccountCircuitLeaseRecovery {
			t.Fatalf("RECOVERING 起源应取得 recovery canary 租约: %q", canary.leaseKind)
		}
		if phase := memoryPhase(t, gate, "a-canary"); phase != circuitruntime.GatewayAccountCircuitPhaseHalfOpen {
			t.Fatalf("canary 租约下相位必须为 HALF_OPEN: %s", phase)
		}
		if decision, attempt, err := gate.Prepare(ctx, memoryGateInput("a-canary", "ev-4b")); err != nil || decision != AccountCircuitBlocked || attempt != nil {
			t.Fatalf("canary 租约互斥期内必须拦截: %s %v %v", decision, attempt, err)
		}
		// 两次 framing 成功后留在 RECOVERING（retryAt=now，可立即再 canary）。
		if err := attempt.ReportFramingComplete(ctx); err != nil {
			t.Fatalf("canary framing #1: %v", err)
		}
		for i := 0; i < 2; i++ {
			if phase := memoryPhase(t, gate, "a-canary"); phase != circuitruntime.GatewayAccountCircuitPhaseRecovering {
				t.Fatalf("canary 成功 #%d 后应留在 RECOVERING: %s", i+1, phase)
			}
			_, retryAttempt, err := gate.Prepare(ctx, memoryGateInput("a-canary", "ev-4c"))
			if err != nil {
				t.Fatal(err)
			}
			if retryAttempt.(*memoryCircuitAttempt).leaseKind != circuitruntime.GatewayAccountCircuitLeaseRecovery {
				t.Fatal("RECOVERING 重试必须再次取得 canary 租约")
			}
			if err := retryAttempt.ReportFramingComplete(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// 第三次成功回 CLOSED（无记录）。
		if phase := memoryPhase(t, gate, "a-canary"); phase != circuitruntime.GatewayAccountCircuitPhaseClosed {
			t.Fatalf("三次 canary 成功后必须回 CLOSED: %s", phase)
		}
	})

	t.Run("canary 失败按退避阶梯升级 OPEN 且 unknown 中性回退", func(t *testing.T) {
		// unknown（canary 有租约）：回 origin 且 retryAt=now（neutral）。
		memoryPatchState(t, gate, "a-unknown", func(state *memoryCircuitState) {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseHalfOpen
			state.halfOpenOrigin = circuitruntime.GatewayAccountCircuitPhaseOpen
			state.lease = &memoryCircuitLease{kind: circuitruntime.GatewayAccountCircuitLeaseHalfOpen, id: "dispatch-ev-5a", until: time.Now().UTC().Add(time.Minute)}
		})
		unknownAttempt := &memoryCircuitAttempt{gate: gate, accountID: "a-unknown", input: memoryGateInput("a-unknown", "ev-5a"), leaseID: "dispatch-ev-5a", leaseKind: circuitruntime.GatewayAccountCircuitLeaseHalfOpen}
		if err := unknownAttempt.ReportUnknown(ctx); err != nil {
			t.Fatalf("canary unknown: %v", err)
		}
		gate.mu.Lock()
		unknownState := gate.states["a-unknown"]
		if unknownState == nil || unknownState.phase != circuitruntime.GatewayAccountCircuitPhaseOpen || unknownState.retryAt.IsZero() {
			gate.mu.Unlock()
			t.Fatalf("canary unknown 必须回 OPEN 且 retryAt=now: %+v", unknownState)
		}
		gate.mu.Unlock()
		// transport failure（无租约、OPEN 相位）：镜像 suspect state_mismatch 静默。
		memoryPatchState(t, gate, "a-escalate", func(state *memoryCircuitState) {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseOpen
			state.retryAt = time.Now().UTC().Add(-time.Minute)
		})
		_, openAttempt, err := gate.Prepare(ctx, memoryGateInput("a-escalate", "ev-5b"))
		if err != nil {
			t.Fatal(err)
		}
		if err := openAttempt.ReportTransportFailure(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if phase := memoryPhase(t, gate, "a-escalate"); phase != circuitruntime.GatewayAccountCircuitPhaseOpen {
			t.Fatalf("OPEN 相位下无租约失败必须静默（state_mismatch）: %s", phase)
		}
		// confirmation 租约下的 transport failure：SUSPECT 升级 OPEN，退避 3s。
		memoryPatchState(t, gate, "a-open", func(state *memoryCircuitState) {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseSuspect
			state.retryAt = time.Now().UTC().Add(-time.Minute)
			state.lease = &memoryCircuitLease{kind: circuitruntime.GatewayAccountCircuitLeaseConfirmation, id: "dispatch-ev-5c", until: time.Now().UTC().Add(time.Minute)}
		})
		confirmFailure := &memoryCircuitAttempt{gate: gate, accountID: "a-open", input: memoryGateInput("a-open", "ev-5c"), leaseID: "dispatch-ev-5c", leaseKind: circuitruntime.GatewayAccountCircuitLeaseConfirmation}
		if err := confirmFailure.ReportTransportFailure(ctx, nil); err != nil {
			t.Fatal(err)
		}
		gate.mu.Lock()
		openState := gate.states["a-open"]
		if openState == nil || openState.phase != circuitruntime.GatewayAccountCircuitPhaseOpen {
			gate.mu.Unlock()
			t.Fatalf("确认租约失败必须升级 OPEN: %+v", openState)
		}
		firstRetry := openState.retryAt
		backoff := openState.backoffAttempt
		gate.mu.Unlock()
		if time.Until(firstRetry) > 3*time.Second || backoff != 1 {
			t.Fatalf("首次升级退避必须 3s/attempt=1: retry=%s attempt=%d", firstRetry, backoff)
		}
		// 连续失败退避翻倍：再次 canary 失败后 5s。
		memoryPatchState(t, gate, "a-open", func(state *memoryCircuitState) {
			state.retryAt = time.Now().UTC().Add(-time.Minute)
			state.lease = &memoryCircuitLease{kind: circuitruntime.GatewayAccountCircuitLeaseConfirmation, id: "dispatch-ev-5d", until: time.Now().UTC().Add(time.Minute)}
		})
		secondFailure := &memoryCircuitAttempt{gate: gate, accountID: "a-open", input: memoryGateInput("a-open", "ev-5d"), leaseID: "dispatch-ev-5d", leaseKind: circuitruntime.GatewayAccountCircuitLeaseConfirmation}
		if err := secondFailure.ReportTransportFailure(ctx, nil); err != nil {
			t.Fatal(err)
		}
		gate.mu.Lock()
		secondRetry := gate.states["a-open"].retryAt
		secondAttemptCount := gate.states["a-open"].backoffAttempt
		gate.mu.Unlock()
		want := 5 * time.Second
		if delta := time.Until(secondRetry); delta <= 0 || delta > want || secondAttemptCount != 2 {
			t.Fatalf("二次失败退避必须约 5s/attempt=2: retry=%s attempt=%d", secondRetry, secondAttemptCount)
		}
	})
}

func TestMemoryCircuitGateConfirmationLeaseDurationFloor(t *testing.T) {
	// ConfirmationLeaseDuration 不足 1s 时取 now+1min（镜像 circuit_runtime_gate.go:54-56）。
	ctx := context.Background()
	gate := NewMemoryCircuitGate()
	memoryPatchState(t, gate, "a-lease", func(state *memoryCircuitState) {
		state.phase = circuitruntime.GatewayAccountCircuitPhaseSuspect
		state.retryAt = time.Now().UTC().Add(-time.Minute)
	})
	input := memoryGateInput("a-lease", "ev-6")
	input.ConfirmationLeaseDuration = time.Millisecond
	decision, _, err := gate.Prepare(ctx, input)
	if err != nil || decision != AccountCircuitDispatchable {
		t.Fatalf("确认探测被拒: %s %v", decision, err)
	}
	gate.mu.Lock()
	lease := gate.states["a-lease"].lease
	gate.mu.Unlock()
	if lease == nil {
		t.Fatal("未取得确认租约")
	}
	if until := time.Until(lease.until); until < 30*time.Second || until > time.Minute {
		t.Fatalf("过短租约必须抬升到约 1 分钟: %s", until)
	}
}
