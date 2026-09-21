// MemoryCircuitGate 是零配置单机模式（未配置 J3b circuit Redis）下的进程内
// 账户熔断 gate，与 j3b_memory_keymodel.go 的 memory key-model 同款定位：
// 单进程 owner、重启即重置。决策层语义逐条对齐同包 RuntimeCircuitGate
// （circuit_runtime_gate.go）与 circuit_runtime/runtime.go 的 Redis Lua 相位机
// （读时租约过期规范化、confirmation/canary 租约互斥、RECOVERING 恢复成功
// 计数、OPEN 退避阶梯）；配置 Redis 后组合根改用 RuntimeCircuitGate，本实现
// 不再参与。并发安全仅依赖进程内 sync.Mutex。
package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

// memoryCanaryBackoffs 对齐 runtime.go Lua open() 的退避阶梯（毫秒表
// {3000,5000,10000,30000,60000}）：连续失败逐级上升，第 5 次起固定 60s。
var memoryCanaryBackoffs = []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute}

// memoryRecoverySuccesses 对齐 runtime.go Lua complete_canary 的恢复成功阈值：
// RECOVERING 起源的 canary framing_complete 累计 3 次后回 CLOSED。
const memoryRecoverySuccesses = 3

// memoryRecoveringRetryDelay 对齐 runtime.go Lua enter_recovering 的重试窗口
// （retryAtMs = now_ms + 3000）。
const memoryRecoveringRetryDelay = 3 * time.Second

type memoryCircuitLease struct {
	kind  circuitruntime.GatewayAccountCircuitLeaseKind
	id    string
	until time.Time
}

// memoryCircuitState mirrors the Redis Lua per-account runtime state. 无记录
// 即视为 CLOSED，因此关闭动作直接删除记录而不落 CLOSED 条目。
type memoryCircuitState struct {
	phase                circuitruntime.GatewayAccountCircuitPhase
	generation           int64
	dispatchRevision     int64
	backoffAttempt       int
	recoverySuccessCount int
	retryAt              time.Time
	lease                *memoryCircuitLease
	halfOpenOrigin       circuitruntime.GatewayAccountCircuitPhase
	transitionID         string
	failureReason        string
}

// MemoryCircuitGate implements AccountCircuitGate entirely in-process. 状态按
// AccountID 隔离（Scope 恒为 account，与 RuntimeCircuitGate 的 Scope 构造
// 一致），键空间即 AccountID 原值。
type MemoryCircuitGate struct {
	mu     sync.Mutex
	states map[string]*memoryCircuitState
}

func NewMemoryCircuitGate() *MemoryCircuitGate {
	return &MemoryCircuitGate{states: map[string]*memoryCircuitState{}}
}

// Prepare mirrors RuntimeCircuitGate.Prepare（circuit_runtime_gate.go:19-77）：
// 无记录 = CLOSED；OPEN/SUSPECT 退避期内拦截；SUSPECT 需独立确认资格并 CAS
// 获取 confirmation 租约；HALF_OPEN/RECOVERING CAS 获取 canary 租约；其余
// （CLOSED、退避到期的 OPEN）直通且 attempt 无租约。
func (g *MemoryCircuitGate) Prepare(_ context.Context, input AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error) {
	if strings.TrimSpace(input.AccountID) == "" {
		return "", nil, errors.New("gateway account circuit account id is required")
	}
	now := time.Now().UTC()
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.states[input.AccountID]
	if state == nil {
		return AccountCircuitDispatchable, &memoryCircuitAttempt{gate: g, accountID: input.AccountID, input: input}, nil
	}
	g.normalizeExpiredLease(state, now)
	if state.phase == circuitruntime.GatewayAccountCircuitPhaseOpen || state.phase == circuitruntime.GatewayAccountCircuitPhaseSuspect {
		if state.retryAt.IsZero() || state.retryAt.After(now) {
			return AccountCircuitBlocked, nil, nil
		}
	}
	// A SUSPECT circuit may only be probed by a request independently qualified
	// for confirmation（镜像 circuit_runtime_gate.go:43-49 的确认资格规则）。
	if state.phase == circuitruntime.GatewayAccountCircuitPhaseSuspect && !input.ConfirmationEligible {
		return AccountCircuitBlocked, nil, nil
	}
	// 租约时长语义镜像 circuit_runtime_gate.go:52-56。
	leaseID := fmt.Sprintf("dispatch-%s", input.FailureEvidenceKey)
	leaseUntil := now.Add(input.ConfirmationLeaseDuration)
	if leaseUntil.Before(now.Add(time.Second)) {
		leaseUntil = now.Add(time.Minute)
	}
	switch state.phase {
	case circuitruntime.GatewayAccountCircuitPhaseSuspect:
		// confirmation 租约互斥（镜像 acquire_confirmation 的 state_mismatch 分支）。
		if state.lease != nil {
			return AccountCircuitBlocked, nil, nil
		}
		state.transitionID = input.FailureEvidenceKey
		state.lease = &memoryCircuitLease{kind: circuitruntime.GatewayAccountCircuitLeaseConfirmation, id: leaseID, until: leaseUntil}
		return AccountCircuitDispatchable, &memoryCircuitAttempt{gate: g, accountID: input.AccountID, input: input, leaseID: leaseID, leaseKind: circuitruntime.GatewayAccountCircuitLeaseConfirmation}, nil
	case circuitruntime.GatewayAccountCircuitPhaseHalfOpen, circuitruntime.GatewayAccountCircuitPhaseRecovering:
		// canary 租约互斥（镜像 acquire_canary 的 state_mismatch 分支）。memory
		// 版保留 RECOVERING 枚举判断以对齐 Redis 相位机。
		if state.lease != nil {
			return AccountCircuitBlocked, nil, nil
		}
		// canary 重试窗口未到（镜像 acquire_canary 的 not_due 分支）。
		if state.retryAt.IsZero() || state.retryAt.After(now) {
			return AccountCircuitBlocked, nil, nil
		}
		origin := state.phase
		leaseKind := circuitruntime.GatewayAccountCircuitLeaseHalfOpen
		if origin == circuitruntime.GatewayAccountCircuitPhaseRecovering {
			leaseKind = circuitruntime.GatewayAccountCircuitLeaseRecovery
		}
		state.phase = circuitruntime.GatewayAccountCircuitPhaseHalfOpen
		state.halfOpenOrigin = origin
		state.transitionID = input.FailureEvidenceKey
		state.lease = &memoryCircuitLease{kind: leaseKind, id: leaseID, until: leaseUntil}
		return AccountCircuitDispatchable, &memoryCircuitAttempt{gate: g, accountID: input.AccountID, input: input, leaseID: leaseID, leaseKind: leaseKind}, nil
	}
	// CLOSED 与退避到期的 OPEN：直通且不带租约（对齐 circuit_runtime_gate.go:76）。
	return AccountCircuitDispatchable, &memoryCircuitAttempt{gate: g, accountID: input.AccountID, input: input}, nil
}

// normalizeExpiredLease 对齐 runtime.go Lua normalize_entry 的读时规范化：
// 过期的 confirmation 租约只清租约（相位保持 SUSPECT）；过期的 canary 租约
// 回退 halfOpenOrigin 并把 retryAt 置为现在（立即可重新 canary）。调用方必须
// 持有 g.mu。
func (g *MemoryCircuitGate) normalizeExpiredLease(state *memoryCircuitState, now time.Time) {
	if state.lease == nil || state.lease.until.After(now) {
		return
	}
	if state.lease.kind == circuitruntime.GatewayAccountCircuitLeaseConfirmation {
		state.lease = nil
		return
	}
	state.phase = state.halfOpenOrigin
	if state.phase == "" {
		state.phase = circuitruntime.GatewayAccountCircuitPhaseOpen
	}
	state.lease = nil
	state.halfOpenOrigin = ""
	state.retryAt = now
}

// withState 在锁内对账户状态执行一次结算迁移；CLOSED 结果直接删除记录
// （无记录 = CLOSED）。state 缺失时按 CLOSED 初值进入迁移，迁移前后做与
// Redis 读路径一致的租约过期规范化。
func (g *MemoryCircuitGate) withState(accountID string, mutate func(state *memoryCircuitState, now time.Time)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now().UTC()
	state := g.states[accountID]
	if state == nil {
		state = &memoryCircuitState{phase: circuitruntime.GatewayAccountCircuitPhaseClosed}
	} else {
		g.normalizeExpiredLease(state, now)
	}
	mutate(state, now)
	if state.phase == circuitruntime.GatewayAccountCircuitPhaseClosed {
		delete(g.states, accountID)
		return
	}
	g.states[accountID] = state
}

// settleLease 校验本 attempt 仍持有账户租约（lease_mismatch → 静默忽略，与
// runtimeCircuitAttempt 忽略 mutation status 的行为一致）。调用方在持锁闭包内。
func settleLease(state *memoryCircuitState, attempt *memoryCircuitAttempt) bool {
	return state.lease != nil && state.lease.id == attempt.leaseID
}

// openCircuit 对齐 runtime.go Lua open()：退避阶梯、phase=OPEN、恢复计数清零、
// 清租约与 half-open 起源。
func openCircuit(state *memoryCircuitState, now time.Time, reason, transitionID string) {
	state.backoffAttempt++
	delay := memoryCanaryBackoffs[len(memoryCanaryBackoffs)-1]
	if state.backoffAttempt <= len(memoryCanaryBackoffs) {
		delay = memoryCanaryBackoffs[state.backoffAttempt-1]
	}
	state.phase = circuitruntime.GatewayAccountCircuitPhaseOpen
	state.transitionID = transitionID
	state.recoverySuccessCount = 0
	state.retryAt = now.Add(delay)
	state.failureReason = reason
	state.lease = nil
	state.halfOpenOrigin = ""
}

// enterRecovering 对齐 runtime.go Lua enter_recovering：phase=RECOVERING、
// 恢复计数清零、retryAt = now+3s、清租约与起源。
func enterRecovering(state *memoryCircuitState, now time.Time, transitionID string) {
	state.phase = circuitruntime.GatewayAccountCircuitPhaseRecovering
	state.transitionID = transitionID
	state.recoverySuccessCount = 0
	state.retryAt = now.Add(memoryRecoveringRetryDelay)
	state.lease = nil
	state.halfOpenOrigin = ""
}

// memoryCircuitAttempt 对齐 runtimeCircuitAttempt（circuit_runtime_gate.go:83-160）
// 的上报契约：首个终态上报生效，settled 后幂等 no-op。
type memoryCircuitAttempt struct {
	gate      *MemoryCircuitGate
	accountID string
	input     AccountCircuitInput
	settled   bool
	leaseID   string
	leaseKind circuitruntime.GatewayAccountCircuitLeaseKind
}

func (a *memoryCircuitAttempt) ReportFramingComplete(_ context.Context) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	if a.leaseID == "" {
		// 无租约（closed 直通或退避到期的 OPEN）：中性 no-op。
		return nil
	}
	a.gate.withState(a.accountID, func(state *memoryCircuitState, now time.Time) {
		if !settleLease(state, a) {
			return
		}
		if a.leaseKind == circuitruntime.GatewayAccountCircuitLeaseConfirmation {
			// complete_confirmation(framing_complete)：相位前进到 RECOVERING。
			enterRecovering(state, now, a.input.FailureEvidenceKey)
			return
		}
		// complete_canary(framing_complete)：OPEN 起源的首次 canary 进入
		// RECOVERING；RECOVERING 起源累计 3 次成功回 CLOSED，否则留在
		// RECOVERING 且 retryAt = now（镜像 Lua complete_canary 的
		// halfOpenOrigin 分支与恢复成功计数）。
		if state.halfOpenOrigin == circuitruntime.GatewayAccountCircuitPhaseOpen {
			enterRecovering(state, now, a.input.FailureEvidenceKey)
			return
		}
		state.recoverySuccessCount++
		if state.recoverySuccessCount >= memoryRecoverySuccesses {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseClosed
			state.transitionID = a.input.FailureEvidenceKey
			state.recoverySuccessCount = 0
			state.retryAt = time.Time{}
			state.lease = nil
			state.halfOpenOrigin = ""
			return
		}
		state.phase = circuitruntime.GatewayAccountCircuitPhaseRecovering
		state.transitionID = a.input.FailureEvidenceKey
		state.retryAt = now
		state.lease = nil
		state.halfOpenOrigin = ""
	})
	return nil
}

func (a *memoryCircuitAttempt) ReportTransportFailure(_ context.Context, cause error) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	reason := "transport_failure"
	if cause != nil {
		reason += ": " + cause.Error()
	}
	a.gate.withState(a.accountID, func(state *memoryCircuitState, now time.Time) {
		if a.leaseID != "" {
			// confirmation/canary 租约下的失败按对应完成结算（镜像
			// completeLease → complete_confirmation/complete_canary 的
			// transport_failure → open() 退避升级）。
			if settleLease(state, a) {
				openCircuit(state, now, reason, a.input.FailureEvidenceKey)
			}
			return
		}
		// 无租约时镜像 SuspectGatewayAccountCircuit：仅 CLOSED 相位生效
		// （circuit_runtime_gate.go:139-148 的 Lua suspect state_mismatch
		// 分支静默忽略其余相位）。SUSPECT 不设 retryAt，与 Redis 版一致地
		// 依赖独立确认资格的请求完成确认探测。
		if state.phase != circuitruntime.GatewayAccountCircuitPhaseClosed {
			return
		}
		state.phase = circuitruntime.GatewayAccountCircuitPhaseSuspect
		state.generation++
		state.dispatchRevision = a.input.DispatchRevision
		state.transitionID = a.input.FailureEvidenceKey
		state.failureReason = reason
		state.retryAt = time.Time{}
		state.backoffAttempt = 0
		state.recoverySuccessCount = 0
		state.lease = nil
		state.halfOpenOrigin = ""
	})
	return nil
}

func (a *memoryCircuitAttempt) ReportUnknown(_ context.Context) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	// An unknown lifecycle is deliberately neutral unless this attempt owns a
	// lease（镜像 circuit_runtime_gate.go:117-130）：无租约时不得把 CLOSED
	// 电路打成 SUSPECT。
	if a.leaseID == "" {
		return nil
	}
	a.gate.withState(a.accountID, func(state *memoryCircuitState, now time.Time) {
		if !settleLease(state, a) {
			return
		}
		if state.lease.kind == circuitruntime.GatewayAccountCircuitLeaseConfirmation {
			// confirmation unknown：释放租约、相位不变（neutral）。
			state.lease = nil
			return
		}
		// canary unknown：回 halfOpenOrigin 并把 retryAt 置为 now（neutral，
		// 立即可重新 canary；镜像 Lua complete_canary 的 unknown 分支）。
		state.phase = state.halfOpenOrigin
		if state.phase == "" {
			state.phase = circuitruntime.GatewayAccountCircuitPhaseOpen
		}
		state.retryAt = now
		state.lease = nil
		state.halfOpenOrigin = ""
	})
	return nil
}
