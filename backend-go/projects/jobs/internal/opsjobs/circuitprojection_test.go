package opsjobs

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// recordingCAS 记录每次 CAS 输入并按脚本返回结果。
type recordingCAS struct {
	mu    sync.Mutex
	input []IncidentCASInput
	// responses/errors 为空时按 applied 返回。
	responses []IncidentCASResult
	errs      []error
}

func (m *recordingCAS) CompareAndSetIncident(_ context.Context, input IncidentCASInput) (IncidentCASResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// ExpectedLedgerRevision 值快照：防调用方复用可变地址导致断言读到终值。
	if input.ExpectedLedgerRevision != nil {
		value := *input.ExpectedLedgerRevision
		input.ExpectedLedgerRevision = &value
	}
	m.input = append(m.input, input)
	index := len(m.input) - 1
	if index < len(m.errs) && m.errs[index] != nil {
		return IncidentCASResult{}, m.errs[index]
	}
	if index < len(m.responses) {
		return m.responses[index], nil
	}
	row := input.Incident
	row.LedgerRevision = 1
	return IncidentCASResult{Status: IncidentCASApplied, CurrentDispatchRevision: input.Incident.DispatchRevision, Incident: &row}, nil
}

func (m *recordingCAS) calls() []IncidentCASInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]IncidentCASInput(nil), m.input...)
}

func projectionTestState(scopeKey, phase string, updatedAt int64) CircuitState {
	scope := CircuitScope{Kind: CircuitScopeAccount, AccountRuntimeKey: "acc-1"}
	scopeKeyComputed, _ := AccountCircuitScopeKey(scope)
	if scopeKey == "" {
		scopeKey = scopeKeyComputed
	}
	required := 2
	count := 1
	retryAt := updatedAt + 3000
	return CircuitState{
		ScopeKey:                     scopeKey,
		Scope:                        scope,
		Phase:                        CircuitPhase(phase),
		Generation:                   3,
		DispatchRevision:             "5",
		TransitionID:                 "tr-1",
		BackoffAttempt:               2,
		RecoverySuccessCount:         4,
		ConfirmationFailuresRequired: &required,
		ConfirmationFailureCount:     &count,
		RetryAtMS:                    &retryAt,
		FailureReason:                "dial tcp connect refused",
		IncidentID:                   "inc-9",
		UpdatedAtMS:                  updatedAt,
	}
}

func mustProjector(t *testing.T, cas IncidentCASPort) *CircuitIncidentProjector {
	t.Helper()
	projector, err := NewCircuitIncidentProjector(cas, CircuitIncidentProjectorOptions{
		OwnerID: "owner-test",
		NowMS:   func() int64 { return 10_000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return projector
}

// TestProjectorMutationFilters 验证挂点过滤：not_found / fenced（无状态变化）
// 不投影。
func TestProjectorMutationFilters(t *testing.T) {
	mock := &recordingCAS{}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationNotFound})
	if calls := mock.calls(); len(calls) != 0 {
		t.Fatalf("not_found 不得投影: %d", len(calls))
	}
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationStaleGeneration})
	if calls := mock.calls(); len(calls) != 0 {
		t.Fatalf("fenced 不得投影: %d", len(calls))
	}
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	if calls := mock.calls(); len(calls) != 1 {
		t.Fatalf("applied 应投影一次: %d", len(calls))
	}
}

// TestProjectorMappingFields 抽查 CAS 输入映射：IncidentID durable 规则、
// retryAt 双写、backoff/recovering、failure 分类、lease 四元组、CLOSED
// retainedUntil、expected 缓存 nil→值。
func TestProjectorMappingFields(t *testing.T) {
	mock := &recordingCAS{}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "OPEN", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})

	calls := mock.calls()
	if len(calls) != 1 {
		t.Fatalf("应恰有一次 CAS: %d", len(calls))
	}
	row := calls[0].Incident
	if calls[0].ExpectedLedgerRevision != nil {
		t.Fatalf("缓存未命中 expected 必须为 nil: %v", *calls[0].ExpectedLedgerRevision)
	}
	if row.IncidentID != "inc-9" {
		t.Fatalf("IncidentID 应取运行态 incidentId: %s", row.IncidentID)
	}
	if row.AccountID != "acc-1" || row.AccountRuntimeKey != "acc-1" || row.ScopeKind != "account" {
		t.Fatalf("身份字段不符: %+v", row)
	}
	if row.State != "OPEN" || row.Generation != 3 || row.DispatchRevision != 5 {
		t.Fatalf("状态事实不符: %+v", row)
	}
	if row.NextTransitionAtMS == nil || *row.NextTransitionAtMS != 4000 || row.OpenUntilMS == nil || *row.OpenUntilMS != 4000 {
		t.Fatalf("retryAt 应双写 next/open: %+v %+v", row.NextTransitionAtMS, row.OpenUntilMS)
	}
	if row.BackoffLevel != 2 || row.RecoveringSuccesses != 4 {
		t.Fatalf("backoff/recovering 不符: %d %d", row.BackoffLevel, row.RecoveringSuccesses)
	}
	if row.ConsecutiveFailures != 1 || row.ConfirmationFailuresRequired != 2 {
		t.Fatalf("确认三元组不符: %d %d", row.ConsecutiveFailures, row.ConfirmationFailuresRequired)
	}
	if row.LastFailureClass == nil || *row.LastFailureClass != FailureClassConnectFailed {
		t.Fatalf("failure 分类不符: %+v", row.LastFailureClass)
	}
	if !row.UpstreamAttemptObserved || row.UpdatedAtMS != 1000 || row.CreatedAtMS != 0 {
		t.Fatalf("updated/upstream/created 不符: %+v", row)
	}
	if row.RetainedUntilMS != nil {
		t.Fatalf("非 CLOSED 不得带 retainedUntil: %v", *row.RetainedUntilMS)
	}
	if row.LeaseID != nil {
		t.Fatalf("无租约状态不得带 lease: %+v", row.LeaseID)
	}

	// incidentId 缺省 → durable 规则回落 transitionId。
	fallback := projectionTestState("", "OPEN", 1000)
	fallback.IncidentID = ""
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: fallback.Scope, State: fallback, Status: CircuitMutationApplied})
	calls = mock.calls()
	if len(calls) != 2 || calls[1].Incident.IncidentID != "tr-1" {
		t.Fatalf("incidentId 缺省应回落 transitionId: %+v", calls[len(calls)-1].Incident)
	}

	// CLOSED：retainedUntil = now + retention；expected 使用回填缓存（上次
	// applied 默认响应 rev=1）。
	closed := projectionTestState("", "CLOSED", 2000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: closed.Scope, State: closed, Status: CircuitMutationApplied})
	calls = mock.calls()
	if len(calls) != 3 {
		t.Fatalf("应有三次 CAS: %d", len(calls))
	}
	if calls[2].ExpectedLedgerRevision == nil || *calls[2].ExpectedLedgerRevision != 1 {
		t.Fatalf("applied 后应回填 expected 缓存: %+v", calls[2].ExpectedLedgerRevision)
	}
	if calls[2].Incident.RetainedUntilMS == nil || *calls[2].Incident.RetainedUntilMS != 10_000+DefaultIncidentClosedRetentionMS {
		t.Fatalf("CLOSED retainedUntil 应为 now+retention: %+v", calls[2].Incident.RetainedUntilMS)
	}

	// 租约四元组：owner 取投影器 owner。
	leased := projectionTestState("", "HALF_OPEN", 3000)
	leased.Lease = &CircuitLease{Kind: CircuitLeaseHalfOpen, LeaseID: "lease-7", LeaseUntilMS: 9000}
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: leased.Scope, State: leased, Status: CircuitMutationApplied})
	calls = mock.calls()
	last := calls[len(calls)-1].Incident
	if last.LeaseID == nil || *last.LeaseID != "lease-7" || last.LeasePurpose == nil || *last.LeasePurpose != "half_open" ||
		last.LeaseOwnerRunID == nil || *last.LeaseOwnerRunID != projector.OwnerID() ||
		last.LeaseUntilMS == nil || *last.LeaseUntilMS != 9000 {
		t.Fatalf("lease 四元组不符: %+v", last)
	}
}

// TestProjectorConflictBackfillRetry 验证 cas_conflict 用响应行回填 expected
// 即时重试（≤2 次），耗尽后留 pending 且 FlushPending 再重放。
func TestProjectorConflictBackfillRetry(t *testing.T) {
	current := int64(3)
	mock := &recordingCAS{responses: []IncidentCASResult{
		{Status: IncidentCASConflict, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: current}},
		{Status: IncidentCASConflict, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 4}},
		{Status: IncidentCASApplied, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 5}},
	}}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	calls := mock.calls()
	if len(calls) != 3 {
		t.Fatalf("冲突应即时重试至成功: %d", len(calls))
	}
	if calls[1].ExpectedLedgerRevision == nil || *calls[1].ExpectedLedgerRevision != 3 {
		t.Fatalf("第二次应携带回填 expected=3: %+v", calls[1].ExpectedLedgerRevision)
	}
	if calls[2].ExpectedLedgerRevision == nil || *calls[2].ExpectedLedgerRevision != 4 {
		t.Fatalf("第三次应携带回填 expected=4: %+v", calls[2].ExpectedLedgerRevision)
	}
}

// TestProjectorConflictMissingRowClearsCache 验证 cas_conflict 且响应无行
// （expected 非 nil 但行已被 Cleanup 删除，CompareAndSetIncident 围栏返回
// conflict 且 Incident=nil）时清 revision 缓存：FlushPending 重试携带
// expected=nil 走新行围栏，applied 后缓存回填新 revision。
func TestProjectorConflictMissingRowClearsCache(t *testing.T) {
	mock := &recordingCAS{responses: []IncidentCASResult{
		// 预热缓存：applied rev=5（行随后被 Cleanup 删除）。
		{Status: IncidentCASApplied, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 5}},
		// 悬空缓存 expected=5 命中"行不存在"围栏，响应无行。
		{Status: IncidentCASConflict},
		// 重试 expected=nil 插新行成功。
		{Status: IncidentCASApplied, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 7}},
	}}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	calls := mock.calls()
	if len(calls) != 2 {
		t.Fatalf("响应无行不得即时重试: %d", len(calls))
	}
	if calls[1].ExpectedLedgerRevision == nil || *calls[1].ExpectedLedgerRevision != 5 {
		t.Fatalf("第二次应携带悬空缓存 expected=5: %+v", calls[1].ExpectedLedgerRevision)
	}
	projector.FlushPending(context.Background())
	calls = mock.calls()
	if len(calls) != 3 {
		t.Fatalf("FlushPending 应重放: %d", len(calls))
	}
	if calls[2].ExpectedLedgerRevision != nil {
		t.Fatalf("行不存在冲突清缓存后重试应 expected=nil: %v", *calls[2].ExpectedLedgerRevision)
	}
	// applied 后缓存回填新 revision，后续投影携带 expected=7。
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	calls = mock.calls()
	if len(calls) != 4 {
		t.Fatalf("应有四次 CAS: %d", len(calls))
	}
	if calls[3].ExpectedLedgerRevision == nil || *calls[3].ExpectedLedgerRevision != 7 {
		t.Fatalf("applied 后应缓存新 revision=7: %+v", calls[3].ExpectedLedgerRevision)
	}
}

func TestProjectorConflictExhaustedStaysPending(t *testing.T) {
	conflict := IncidentCASResult{Status: IncidentCASConflict, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 9}}
	mock := &recordingCAS{responses: []IncidentCASResult{conflict, conflict, conflict}}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	if calls := mock.calls(); len(calls) != 1+projectorMaxConflictRetries {
		t.Fatalf("重试应止于 1+2 次: %d", len(calls))
	}
	// FlushPending 重放 pending；脚本耗尽后按默认响应 applied 结清。
	projector.FlushPending(context.Background())
	calls := mock.calls()
	if len(calls) != 1+projectorMaxConflictRetries+1 {
		t.Fatalf("FlushPending 应重放一次并成功结清: %d", len(calls))
	}
}

// TestProjectorTerminalDiscardClearsCache 验证终态丢弃：清 pending 与该键
// revision 缓存，后续投影 expected 回到 nil。
func TestProjectorTerminalDiscardClearsCache(t *testing.T) {
	mock := &recordingCAS{responses: []IncidentCASResult{
		{Status: IncidentCASApplied, Incident: &IncidentCASRow{CircuitScopeKey: "x", LedgerRevision: 7}},
		{Status: IncidentCASStaleDispatchRevision, CurrentDispatchRevision: 9},
	}}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	calls := mock.calls()
	if len(calls) != 2 {
		t.Fatalf("应有两次 CAS: %d", len(calls))
	}
	if calls[1].ExpectedLedgerRevision == nil || *calls[1].ExpectedLedgerRevision != 7 {
		t.Fatalf("第二次应携带缓存 expected=7: %+v", calls[1].ExpectedLedgerRevision)
	}
	// 终态丢弃清缓存后，第三次投影 expected 回到 nil。
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	calls = mock.calls()
	if len(calls) != 3 {
		t.Fatalf("应有三次 CAS: %d", len(calls))
	}
	if calls[2].ExpectedLedgerRevision != nil {
		t.Fatalf("终态丢弃后缓存应清空: %+v", *calls[2].ExpectedLedgerRevision)
	}
}

// TestProjectorErrorRetainedUntilFlush 验证瞬时错误留 pending 且 FlushPending
// 重放成功。
func TestProjectorErrorRetainedUntilFlush(t *testing.T) {
	mock := &recordingCAS{errs: []error{errors.New("db down")}}
	projector := mustProjector(t, mock)
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	if calls := mock.calls(); len(calls) != 1 {
		t.Fatalf("首次失败后不得再即时重试: %d", len(calls))
	}
	projector.FlushPending(context.Background())
	calls := mock.calls()
	if len(calls) != 2 {
		t.Fatalf("FlushPending 应重放: %d", len(calls))
	}
}

// TestProjectorMergeKeepsLatest 验证同 scope 合并只留最新（错误路径下下一轮
// FlushPending 只投影最新一条）。
func TestProjectorMergeKeepsLatest(t *testing.T) {
	var mu sync.Mutex
	var recorded []IncidentCASInput
	projector, err := NewCircuitIncidentProjector(casFunc(func(_ context.Context, input IncidentCASInput) (IncidentCASResult, error) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, input)
		return IncidentCASResult{}, errors.New("db down")
	}), CircuitIncidentProjectorOptions{OwnerID: "o", NowMS: func() int64 { return 10_000 }})
	if err != nil {
		t.Fatal(err)
	}
	first := projectionTestState("", "SUSPECT", 1000)
	second := projectionTestState("", "OPEN", 2000)
	second.TransitionID = "tr-2"
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: first.Scope, State: first, Status: CircuitMutationApplied})
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: second.Scope, State: second, Status: CircuitMutationApplied})
	projector.FlushPending(context.Background())
	// 每次 OnRecoveryMutation 同步尝试 1 次（失败即留 pending，最新覆盖旧
	// 条），FlushPending 只重放最新一条。
	if len(recorded) != 3 {
		t.Fatalf("应有 2 次即时尝试 + 1 次重放: %d", len(recorded))
	}
	if recorded[2].Incident.TransitionID != "tr-2" || recorded[2].Incident.State != "OPEN" {
		t.Fatalf("重放应只保留最新 mutation: %+v", recorded[2].Incident)
	}
}

// TestProjectorCloseIdempotent 验证 Close 幂等且关闭后拒绝新投影。
func TestProjectorCloseIdempotent(t *testing.T) {
	mock := &recordingCAS{}
	projector := mustProjector(t, mock)
	projector.Close()
	projector.Close()
	state := projectionTestState("", "SUSPECT", 1000)
	projector.OnRecoveryMutation(CircuitRecoveryMutation{Scope: state.Scope, State: state, Status: CircuitMutationApplied})
	projector.FlushPending(context.Background())
	if calls := mock.calls(); len(calls) != 0 {
		t.Fatalf("关闭后不得投影: %d", len(calls))
	}
	if _, err := NewCircuitIncidentProjector(nil, CircuitIncidentProjectorOptions{NowMS: func() int64 { return 0 }}); err == nil {
		t.Fatal("缺 CAS port 必须报错")
	}
	if _, err := NewCircuitIncidentProjector(mock, CircuitIncidentProjectorOptions{}); err == nil {
		t.Fatal("缺 NowMS 必须报错")
	}
}

// casFunc 把函数适配为 IncidentCASPort。
type casFunc func(ctx context.Context, input IncidentCASInput) (IncidentCASResult, error)

func (f casFunc) CompareAndSetIncident(ctx context.Context, input IncidentCASInput) (IncidentCASResult, error) {
	return f(ctx, input)
}
