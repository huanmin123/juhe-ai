package keymodelruntime

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStateJSONRoundTripPreservesDeadlinesAndLease(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	state := State{
		Capability:            testCapability(),
		CapabilityHash:        "hash-1",
		Generation:            3,
		Phase:                 PhaseHalfOpen,
		BackoffAttempt:        2,
		RetryAt:               now.Add(time.Second),
		RecoverySuccessCount:  1,
		LastRecoverySuccessAt: now,
		LastObservedAt:        now,
		LastOutcome:           OutcomeCompleteSuccess,
		ProbeLease:            &Lease{ID: "lease-1", Until: now.Add(time.Minute), PriorSuccesses: 1},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Capability != state.Capability {
		t.Fatalf("往返丢失 capability: got=%+v want=%+v", got.Capability, state.Capability)
	}
	if got.CapabilityHash != state.CapabilityHash || got.Generation != state.Generation || got.Phase != state.Phase || got.BackoffAttempt != state.BackoffAttempt || got.RecoverySuccessCount != state.RecoverySuccessCount || got.LastOutcome != state.LastOutcome {
		t.Fatalf("往返丢失标量字段: got hash=%q gen=%d phase=%s backoff=%d success=%d outcome=%s", got.CapabilityHash, got.Generation, got.Phase, got.BackoffAttempt, got.RecoverySuccessCount, got.LastOutcome)
	}
	if !got.RetryAt.Equal(state.RetryAt) || !got.LastRecoverySuccessAt.Equal(state.LastRecoverySuccessAt) || !got.LastObservedAt.Equal(state.LastObservedAt) {
		t.Fatalf("往返丢失时间字段: retry=%v lastSuccess=%v observed=%v", got.RetryAt, got.LastRecoverySuccessAt, got.LastObservedAt)
	}
	if got.ProbeLease == nil || state.ProbeLease == nil || *got.ProbeLease != *state.ProbeLease {
		t.Fatalf("往返丢失租约: %+v vs %+v", got.ProbeLease, state.ProbeLease)
	}
	// 零值时间字段不应出现在 wire 上。
	empty := State{Capability: testCapability(), CapabilityHash: "hash", Phase: PhaseClosed, LastObservedAt: now}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("retryAtMs")) || bytes.Contains(raw, []byte("probeLease")) {
		t.Fatalf("零值可选字段被序列化: %s", raw)
	}
	// 非法 JSON 必须报错而不是部分填充。
	var bad State
	if err := json.Unmarshal([]byte("{not-json"), &bad); err == nil {
		t.Fatal("非法 JSON 必须失败")
	}
}

func TestBackoffDelayBoundaries(t *testing.T) {
	// 退避序列契约：{5s,15s,1m,5m}，越界钳制到两端。
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{-3, 5 * time.Second},
		{0, 5 * time.Second},
		{1, 5 * time.Second},
		{2, 15 * time.Second},
		{3, time.Minute},
		{4, 5 * time.Minute},
		{99, 5 * time.Minute},
	}
	for _, tc := range tests {
		if got := BackoffDelay(tc.attempt); got != tc.want {
			t.Fatalf("BackoffDelay(%d)=%v want=%v", tc.attempt, got, tc.want)
		}
	}
}

func TestNormalizeCapabilityRejectsBlankAndTrimlikeNode(t *testing.T) {
	valid := testCapability()
	normalized, err := NormalizeCapability(valid)
	if err != nil || normalized != valid {
		t.Fatalf("合法 capability 被拒: %+v %v", normalized, err)
	}
	// 各字段空白会被裁剪。
	trimmed, err := NormalizeCapability(Capability{
		CredentialSourceAccountID: " s1 ", KeyFingerprint: " k1 ", ClientModel: " m1 ",
		ClientEndpointFamily: " chat_completions ", FinalUpstreamModel: " up ", UpstreamEndpointMode: " chat_json ",
		DispatchRevision: 9,
	})
	if err != nil || trimmed.CredentialSourceAccountID != "s1" || trimmed.KeyFingerprint != "k1" {
		t.Fatalf("trim 语义不符: %+v %v", trimmed, err)
	}
	// 每个必填字段为空白时都必须失败。
	for name, mutate := range map[string]func(*Capability){
		"credentialSourceAccountId": func(c *Capability) { c.CredentialSourceAccountID = "  " },
		"keyFingerprint":            func(c *Capability) { c.KeyFingerprint = "" },
		"clientModel":               func(c *Capability) { c.ClientModel = "\t" },
		"clientEndpointFamily":      func(c *Capability) { c.ClientEndpointFamily = "" },
		"finalUpstreamModel":        func(c *Capability) { c.FinalUpstreamModel = " " },
		"upstreamEndpointMode":      func(c *Capability) { c.UpstreamEndpointMode = "" },
	} {
		broken := testCapability()
		mutate(&broken)
		if _, err := NormalizeCapability(broken); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("空白 %s 未被拒绝: %v", name, err)
		}
	}
	zero := testCapability()
	zero.DispatchRevision = 0
	if _, err := NormalizeCapability(zero); err == nil {
		t.Fatal("非正 dispatch revision 必须被拒绝")
	}
}

func TestHashCapabilityRejectsInvalidInput(t *testing.T) {
	if _, err := HashCapability(Capability{}); err == nil {
		t.Fatal("空 capability 必须无法计算哈希")
	}
}

func TestOpenRejectsZeroTime(t *testing.T) {
	if _, err := Open(testCapability(), time.Time{}); err == nil {
		t.Fatal("零观测时间必须被拒绝")
	}
}

func TestIsBlockedFollowsPhase(t *testing.T) {
	if IsBlocked(State{Phase: PhaseClosed}) {
		t.Fatal("CLOSED 不应被拦截")
	}
	if !IsBlocked(State{Phase: PhaseOpen}) || !IsBlocked(State{Phase: PhaseRecovering}) || !IsBlocked(State{Phase: PhaseHalfOpen}) {
		t.Fatal("非 CLOSED 相位必须被拦截")
	}
}

func TestAcquireRecoveryLeaseBranches(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	base, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := AcquireRecoveryLease(base, base.Generation+1, base.DispatchRevision, "l", base.RetryAt); status != StatusStale {
		t.Fatalf("代际不匹配 status=%s", status)
	}
	if status, _ := AcquireRecoveryLease(base, base.Generation, base.DispatchRevision+1, "l", base.RetryAt); status != StatusStale {
		t.Fatalf("revision 不匹配 status=%s", status)
	}
	if status, _ := AcquireRecoveryLease(base, base.Generation, base.DispatchRevision, "l", now); status != StatusNotDue {
		t.Fatalf("退避未到期 status=%s", status)
	}
	closed := State{Capability: testCapability(), CapabilityHash: "h", Generation: 1, Phase: PhaseClosed}
	if status, _ := AcquireRecoveryLease(closed, 1, 7, "l", now); status != StatusNotDue {
		t.Fatalf("CLOSED status=%s", status)
	}
	// OPEN 且已到期、但前一个租约未过期 → lease_mismatch。
	leased := base
	leased.RetryAt = now.Add(-time.Second)
	leased.ProbeLease = &Lease{ID: "held", Until: now.Add(time.Minute)}
	if status, _ := AcquireRecoveryLease(leased, leased.Generation, leased.DispatchRevision, "l", now); status != StatusLeaseMismatch {
		t.Fatalf("活跃租约 status=%s", status)
	}
	// 空 lease id → lease_mismatch。
	due := base
	due.RetryAt = now.Add(-time.Second)
	if status, _ := AcquireRecoveryLease(due, due.Generation, due.DispatchRevision, "  ", now); status != StatusLeaseMismatch {
		t.Fatalf("空租约 id status=%s", status)
	}
	// 合法获取：进入 HALF_OPEN，携带 priorSuccesses。
	recovering := base
	recovering.Phase = PhaseRecovering
	recovering.RetryAt = now.Add(-time.Second)
	recovering.RecoverySuccessCount = 2
	status, next := AcquireRecoveryLease(recovering, recovering.Generation, recovering.DispatchRevision, "lease-ok", now.Add(-time.Second))
	if status != StatusApplied {
		t.Fatalf("applied status=%s", status)
	}
	if next.Phase != PhaseHalfOpen || next.ProbeLease == nil || next.ProbeLease.ID != "lease-ok" || next.ProbeLease.PriorSuccesses != 2 {
		t.Fatalf("租约状态不符: %+v", next)
	}
}

func TestSettleRecoveryBranches(t *testing.T) {
	now := time.UnixMilli(100_000).UTC()
	base, err := Open(testCapability(), now)
	if err != nil {
		t.Fatal(err)
	}
	due := base
	due.Phase = PhaseHalfOpen
	due.ProbeLease = &Lease{ID: "l1", Until: now.Add(time.Minute), PriorSuccesses: 0}
	if status, _ := SettleRecovery(due, due.Generation+1, due.DispatchRevision, "l1", OutcomeCompleteSuccess, now); status != StatusStale {
		t.Fatalf("代际不匹配 status=%s", status)
	}
	if status, _ := SettleRecovery(due, due.Generation, due.DispatchRevision, "wrong", OutcomeCompleteSuccess, now); status != StatusLeaseMismatch {
		t.Fatalf("租约不符 status=%s", status)
	}
	expired := due
	expired.ProbeLease = &Lease{ID: "l1", Until: now.Add(-time.Second)}
	if status, _ := SettleRecovery(expired, expired.Generation, expired.DispatchRevision, "l1", OutcomeCompleteSuccess, now); status != StatusLeaseMismatch {
		t.Fatalf("租约过期 status=%s", status)
	}
	if status, _ := SettleRecovery(due, due.Generation, due.DispatchRevision, "l1", Outcome("bogus"), now); status != StatusLeaseMismatch {
		t.Fatalf("未知 outcome status=%s", status)
	}
	// unknown outcome：无成功计数 → OPEN + 退避间隔。
	openAgain := due
	status, next := SettleRecovery(openAgain, due.Generation, due.DispatchRevision, "l1", OutcomeUnknown, now)
	if status != StatusApplied || next.Phase != PhaseOpen || next.RetryAt.Sub(now) != RecoveryInterval || next.ProbeLease != nil {
		t.Fatalf("unknown→open 不符: %s %+v", status, next)
	}
	// unknown outcome：有成功计数 → RECOVERING。
	recovering := due
	recovering.RecoverySuccessCount = 1
	status, next = SettleRecovery(recovering, recovering.Generation, recovering.DispatchRevision, "l1", OutcomeUnknown, now)
	if status != StatusApplied || next.Phase != PhaseRecovering {
		t.Fatalf("unknown→recovering 不符: %s %+v", status, next)
	}
	// upstream_not_complete：退避递增并清零成功计数。
	incomplete := due
	incomplete.Phase = PhaseHalfOpen
	incomplete.BackoffAttempt = 1
	incomplete.RecoverySuccessCount = 2
	status, next = SettleRecovery(incomplete, incomplete.Generation, incomplete.DispatchRevision, "l1", OutcomeUpstreamIncomplete, now)
	if status != StatusApplied || next.Phase != PhaseOpen || next.BackoffAttempt != 2 || next.RecoverySuccessCount != 0 || !next.LastRecoverySuccessAt.IsZero() {
		t.Fatalf("upstream_not_complete 不符: %s %+v", status, next)
	}
	// 上限钳制：BackoffAttempt 不超过序列长度。
	capped := due
	capped.BackoffAttempt = len(backoff)
	status, next = SettleRecovery(capped, capped.Generation, capped.DispatchRevision, "l1", OutcomeUpstreamIncomplete, now)
	if status != StatusApplied || next.BackoffAttempt != len(backoff) {
		t.Fatalf("退避上限不符: %s %+v", status, next)
	}
	// complete_success：计数累加进入 RECOVERING；达到阈值进入 CLOSED。
	success := due
	status, next = SettleRecovery(success, success.Generation, success.DispatchRevision, "l1", OutcomeCompleteSuccess, now)
	if status != StatusApplied || next.Phase != PhaseRecovering || next.RecoverySuccessCount != 1 {
		t.Fatalf("首次成功不符: %s %+v", status, next)
	}
	// 间隔超限 → 计数重置为 1。
	gapped := next
	gapped.Phase = PhaseHalfOpen
	gapped.ProbeLease = &Lease{ID: "l1", Until: now.Add(10 * time.Minute)}
	status, next = SettleRecovery(gapped, gapped.Generation, gapped.DispatchRevision, "l1", OutcomeCompleteSuccess, now.Add(RecoverySuccessMaxGap+time.Second))
	if status != StatusApplied || next.RecoverySuccessCount != 1 {
		t.Fatalf("成功间隔超限不符: %s %+v", status, next)
	}
	// 达到阈值 → CLOSED 并清空退避。
	closing := due
	closing.RecoverySuccessCount = RecoverySuccessThreshold - 1
	status, next = SettleRecovery(closing, closing.Generation, closing.DispatchRevision, "l1", OutcomeCompleteSuccess, now)
	if status != StatusApplied || next.Phase != PhaseClosed || next.BackoffAttempt != 0 || !next.RetryAt.IsZero() || next.RecoverySuccessCount != 0 {
		t.Fatalf("恢复完成不符: %s %+v", status, next)
	}
}

func TestMemoryStoreFullLifecycle(t *testing.T) {
	store := NewMemoryStore()
	capability := testCapability()
	now := time.UnixMilli(1000).UTC()
	// Get：缺失 → (零值,false,nil)；非法 capability → error。
	if _, ok, err := store.Get(capability); ok || err != nil {
		t.Fatalf("缺失 Get: %v %v", ok, err)
	}
	if _, _, err := store.Get(Capability{}); err == nil {
		t.Fatal("非法 capability Get 必须失败")
	}
	// RecordFailure applied。
	status, state, err := store.RecordFailure(capability, now)

	if err != nil || status != StatusApplied || state.Phase != PhaseOpen {
		t.Fatalf("record failure: %s %+v %v", status, state, err)
	}
	// 同 revision 幂等。
	status, state, err = store.RecordFailure(capability, now.Add(time.Second))
	if err != nil || status != StatusIdempotent || state.LastObservedAt != now.Add(time.Second) {
		t.Fatalf("幂等 failure: %s %+v %v", status, state, err)
	}
	// 状态非 CLOSED 且 revision 相同 → 前台准入被拦截。
	if decision, _, _, err := store.AdmitForeground(capability, "a1", now); err != nil || decision != ForegroundBlocked {
		t.Fatalf("blocked admit: %s %v", decision, err)
	}
	// 同一 capability hash 下存在更高 revision 的状态 → 失败观察 stale。
	//（dispatchRevision 参与 hash，正常路径不会命中；直接注入守护该分支。）
	guardHash, _ := HashCapability(testCapability())
	guardState := State{Capability: testCapability(), CapabilityHash: guardHash, Generation: 4, Phase: PhaseOpen}
	guardState.DispatchRevision = testCapability().DispatchRevision + 1
	store.states[guardHash] = guardState
	staleStatus, staleState, staleErr := store.RecordFailure(testCapability(), now)
	if staleErr != nil || staleStatus != StatusStale || staleState.DispatchRevision != testCapability().DispatchRevision+1 {
		t.Fatalf("stale failure: %s %+v %v", staleStatus, staleState, staleErr)
	}
	delete(store.states, guardHash)
	if _, _, err := store.RecordFailure(Capability{}, now); err == nil {
		t.Fatal("空 capability record 必须失败")
	}
	// revision 不同（更新）→ 放行。
	upgraded := testCapability()
	upgraded.DispatchRevision = capability.DispatchRevision + 1
	hash, err := HashCapability(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	// 注入 revision 不一致的历史状态：准入检查会忽略它并继续放行。
	mismatched := State{Capability: upgraded, CapabilityHash: hash, Phase: PhaseOpen}
	mismatched.DispatchRevision = upgraded.DispatchRevision + 55
	store.states[hash] = mismatched
	if decision, permit, _, err := store.AdmitForeground(upgraded, "a1", now); err != nil || decision != ForegroundAdmitted || permit.AttemptID != "a1" {
		t.Fatalf("upgraded admit: %s %v", decision, err)
	}
	// 同 attempt 幂等准入。
	if decision, permit, _, err := store.AdmitForeground(upgraded, "a1", now.Add(time.Second)); err != nil || decision != ForegroundAdmitted || permit.LeaseUntil != now.Add(90*time.Second) {
		t.Fatalf("幂等 admit: %s %v", decision, err)
	}
	// 空 attempt id。
	if _, _, _, err := store.AdmitForeground(upgraded, "", now); err == nil {
		t.Fatal("空 attempt id 必须失败")
	}
	// busy：预填满前台配额。
	busyHash, _ := HashCapability(testCapability())
	full := make(map[string]time.Time, ForegroundLimit)
	for i := 0; i < ForegroundLimit; i++ {
		full[string(rune('A'+i%26))+strconv.Itoa(i)] = now.Add(time.Hour)
	}
	store.permits[busyHash] = full
	if decision, _, _, err := store.AdmitForeground(testCapability(), "overflow", now); err != nil || decision != ForegroundBusy {
		t.Fatalf("busy admit: %s %v", decision, err)
	}
	// Release：未持有 → false；持有 → true 并推进 wake。
	if store.ReleaseForeground(ForegroundPermit{CapabilityHash: "missing"}) {
		t.Fatal("未知 hash 释放必须失败")
	}
	if store.ReleaseForeground(ForegroundPermit{CapabilityHash: busyHash, AttemptID: "not-held"}) {
		t.Fatal("未持有 attempt 释放必须失败")
	}
	if !store.ReleaseForeground(ForegroundPermit{CapabilityHash: busyHash, AttemptID: string(rune('A')) + strconv.Itoa(0)}) {
		t.Fatal("持有 attempt 释放必须成功")
	}
	if store.wakes[busyHash] != 1 {
		t.Fatalf("wake=%d", store.wakes[busyHash])
	}
	// Renew：缺失 → false；持有 → 延期。
	if _, ok := store.RenewForeground(ForegroundPermit{CapabilityHash: "missing"}, now); ok {
		t.Fatal("未知 hash 续期必须失败")
	}
	renewed, ok := store.RenewForeground(ForegroundPermit{CapabilityHash: busyHash, AttemptID: string(rune('A'+1%26)) + strconv.Itoa(1)}, now)
	if !ok || !renewed.LeaseUntil.Equal(now.Add(90*time.Second)) {
		t.Fatalf("续期不符: %+v %v", renewed, ok)
	}
	// AcquireRecovery / SettleRecovery。
	if _, _, err := store.AcquireRecovery(Capability{}, 1, "l", now); err == nil {
		t.Fatal("非法 capability acquire 必须失败")
	}
	if status, _, err := store.AcquireRecovery(upgraded, 99, "l", now); err != nil || status != StatusStale {
		t.Fatalf("缺失状态 acquire: %s %v", status, err)
	}
	dueState := State{Capability: upgraded, CapabilityHash: hash, Generation: 1, Phase: PhaseOpen, RetryAt: now.Add(-time.Second)}
	store.states[hash] = dueState
	acquireStatus, acquired, acquireErr := store.AcquireRecovery(upgraded, dueState.Generation, "lease-1", now)
	if acquireErr != nil || acquireStatus != StatusApplied || acquired.Phase != PhaseHalfOpen {
		t.Fatalf("acquire: %s %+v %v", acquireStatus, acquired, acquireErr)
	}
	if _, _, err := store.SettleRecovery(Capability{}, 1, "l", OutcomeCompleteSuccess, now); err == nil {
		t.Fatal("非法 capability settle 必须失败")
	}
	if status, _, err := store.SettleRecovery(upgraded, 99, "l", OutcomeCompleteSuccess, now); err != nil || status != StatusStale {
		t.Fatalf("缺失状态 settle: %s %v", status, err)
	}
	settleStatus, settled, settleErr := store.SettleRecovery(upgraded, dueState.Generation, "lease-1", OutcomeCompleteSuccess, now)
	if settleErr != nil || settleStatus != StatusApplied || settled.Phase != PhaseRecovering {
		t.Fatalf("settle: %s %+v %v", settleStatus, settled, settleErr)
	}
}
