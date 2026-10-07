package opsjobs

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// stubRuntimeStore 用注入的运行态集合覆盖 Get（缺键返回 closed 零值形状，
// 与 Redis Lua get / MemoryCircuitStore 的缺失响应一致）。
type stubRuntimeStore struct {
	CircuitStore
	mu     sync.Mutex
	states map[string]CircuitState
}

func (s *stubRuntimeStore) Get(_ context.Context, scope CircuitScope, _ int64) (CircuitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := AccountCircuitScopeKey(scope)
	if err != nil {
		return CircuitState{}, err
	}
	if state, ok := s.states[key]; ok {
		return state, nil
	}
	return closedAccountCircuitState(scope, "", 0, "", 0), nil
}

// fakeActiveLister 按内存序返回活动行（响应 keyset 游标）。
type fakeActiveLister struct {
	rows []IncidentCASRow
}

func (f *fakeActiveLister) ListActiveIncidentsPage(_ context.Context, afterUpdatedAtMS int64, afterScopeKey string, limit int) ([]IncidentCASRow, error) {
	out := []IncidentCASRow{}
	for _, row := range f.rows {
		if row.UpdatedAtMS < afterUpdatedAtMS {
			continue
		}
		if row.UpdatedAtMS == afterUpdatedAtMS && row.CircuitScopeKey <= afterScopeKey {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// scriptedCAS 按调用序返回脚本结果。
type scriptedCAS struct {
	mu     sync.Mutex
	input  []IncidentCASInput
	script []IncidentCASResult
}

func (s *scriptedCAS) CompareAndSetIncident(_ context.Context, input IncidentCASInput) (IncidentCASResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// ExpectedLedgerRevision 值快照：调用方复用 &expected 可变地址，指针记录
	// 会在断言时读到终值。
	if input.ExpectedLedgerRevision != nil {
		value := *input.ExpectedLedgerRevision
		input.ExpectedLedgerRevision = &value
	}
	s.input = append(s.input, input)
	if len(s.input)-1 < len(s.script) {
		return s.script[len(s.input)-1], nil
	}
	row := input.Incident
	row.LedgerRevision = 1
	return IncidentCASResult{Status: IncidentCASApplied, CurrentDispatchRevision: input.Incident.DispatchRevision, Incident: &row}, nil
}

func orphanTestRow(scopeKey string, updatedAt, ledger int64) IncidentCASRow {
	required := int64(2)
	return IncidentCASRow{
		CircuitScopeKey:                 scopeKey,
		AccountID:                       "acc-1",
		AccountRuntimeKey:               "acc-1",
		ScopeKind:                       "account",
		IncidentID:                      "incident-" + scopeKey,
		ChildIncidentIDs:                []string{},
		State:                           "SUSPECT",
		Generation:                      1,
		DispatchRevision:                5,
		LedgerRevision:                  ledger,
		TransitionID:                    "tr-" + scopeKey,
		ConfirmationFailuresRequired:    required,
		ConfirmationFailureEvidenceKeys: []string{},
		LastFailureClass:                strPtrOps(FailureClassConnectFailed),
		UpdatedAtMS:                     updatedAt,
	}
}

func strPtrOps(value string) *string { return &value }

func mustOrphanCloser(t *testing.T, store CircuitStore, cas IncidentCASPort, lister IncidentActiveLister) *OrphanIncidentCloser {
	t.Helper()
	closer, err := NewOrphanIncidentCloser(store, cas, OrphanCloseOptions{
		Lister: lister,
		NowMS:  func() int64 { return 1_000_000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return closer
}

// TestOrphanCloseRetiresMissingBeyondGrace 验证缺键且超宽限期的行被 CAS 结清，
// 输入为 CLOSED + 保留期 + orphan-close 前缀 transition + 行事实照抄。
func TestOrphanCloseRetiresMissingBeyondGrace(t *testing.T) {
	row := orphanTestRow(accountScopeKeyForTest(t), 1000, 3)
	store := &stubRuntimeStore{states: map[string]CircuitState{}}
	cas := &scriptedCAS{}
	closer := mustOrphanCloser(t, store, cas, &fakeActiveLister{rows: []IncidentCASRow{row}})

	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.ClosedRetired != 1 {
		t.Fatalf("应结清一行: %+v", result)
	}
	if len(cas.input) != 1 {
		t.Fatalf("应恰有一次 CAS: %d", len(cas.input))
	}
	input := cas.input[0]
	if input.Incident.State != "CLOSED" {
		t.Fatalf("结清必须 CLOSED: %s", input.Incident.State)
	}
	if input.Incident.RetainedUntilMS == nil || *input.Incident.RetainedUntilMS != 1_000_000+DefaultIncidentClosedRetentionMS {
		t.Fatalf("retainedUntil 应为 now+retention: %+v", input.Incident.RetainedUntilMS)
	}
	if input.Incident.TransitionID != "orphan-close:tr-"+row.CircuitScopeKey {
		t.Fatalf("transitionID 应加 orphan-close 前缀: %s", input.Incident.TransitionID)
	}
	if input.ExpectedLedgerRevision == nil || *input.ExpectedLedgerRevision != 3 {
		t.Fatalf("expected 应取行 revision=3: %+v", input.ExpectedLedgerRevision)
	}
	if input.Incident.Generation != 1 || input.Incident.DispatchRevision != 5 ||
		input.Incident.LastFailureClass == nil || *input.Incident.LastFailureClass != FailureClassConnectFailed {
		t.Fatalf("行事实应照抄: %+v", input.Incident)
	}
}

// TestOrphanCloseSkipsLeasedPresentGrace 分类跳过：活跃租约 / 运行态在 /
// 宽限期内。
func TestOrphanCloseSkipsLeasedPresentGrace(t *testing.T) {
	scopeKey := accountScopeKeyForTest(t)
	leasedRow := orphanTestRow(scopeKey, 1000, 1)
	leaseUntil := int64(2_000_000)
	leasedRow.LeaseUntilMS = &leaseUntil
	leasedRow.LeaseID = strPtrOps("lease-1")
	leasedRow.LeasePurpose = strPtrOps("confirmation")

	presentRow := orphanTestRow(scopeKey, 2000, 1)
	graceRow := orphanTestRow(scopeKey, 1_000_000-60_000, 1)

	// 用例间隔离：每个 closer 一个行集。
	store := &stubRuntimeStore{states: map[string]CircuitState{}}
	cas := &scriptedCAS{}
	closer := mustOrphanCloser(t, store, cas, &fakeActiveLister{rows: []IncidentCASRow{leasedRow}})
	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunningLeased != 1 || len(cas.input) != 0 {
		t.Fatalf("活跃租约应跳过: %+v", result)
	}

	// 运行态在：Get 返回带 dispatch revision 的状态。
	runtimeState := projectionTestState(scopeKey, "SUSPECT", 1000)
	presentStore := &stubRuntimeStore{states: map[string]CircuitState{scopeKey: runtimeState}}
	presentCAS := &scriptedCAS{}
	presentCloser := mustOrphanCloser(t, presentStore, presentCAS, &fakeActiveLister{rows: []IncidentCASRow{presentRow}})
	result, err = presentCloser.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimePresent != 1 || len(presentCAS.input) != 0 {
		t.Fatalf("运行态存在应跳过: %+v", result)
	}

	// 宽限期内。
	graceCAS := &scriptedCAS{}
	graceCloser := mustOrphanCloser(t, store, graceCAS, &fakeActiveLister{rows: []IncidentCASRow{graceRow}})
	result, err = graceCloser.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.GracePending != 1 || len(graceCAS.input) != 0 {
		t.Fatalf("宽限期内应 pending: %+v", result)
	}
}

// TestOrphanCloseConflictRetryAndStale 验证冲突回填重试与终态丢弃计数。
func TestOrphanCloseConflictRetryAndStale(t *testing.T) {
	scopeKey := accountScopeKeyForTest(t)
	row := orphanTestRow(scopeKey, 1000, 3)
	store := &stubRuntimeStore{states: map[string]CircuitState{}}
	cas := &scriptedCAS{script: []IncidentCASResult{
		{Status: IncidentCASConflict, Incident: &IncidentCASRow{LedgerRevision: 4}},
		{Status: IncidentCASConflict, Incident: &IncidentCASRow{LedgerRevision: 5}},
		{Status: IncidentCASApplied, Incident: &IncidentCASRow{LedgerRevision: 6}},
	}}
	closer := mustOrphanCloser(t, store, cas, &fakeActiveLister{rows: []IncidentCASRow{row}})
	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClosedRetired != 1 || len(cas.input) != 3 {
		t.Fatalf("冲突应回填重试至成功: %+v %d", result, len(cas.input))
	}
	if *cas.input[1].ExpectedLedgerRevision != 4 || *cas.input[2].ExpectedLedgerRevision != 5 {
		t.Fatalf("重试应携带回填 expected: %v %v", *cas.input[1].ExpectedLedgerRevision, *cas.input[2].ExpectedLedgerRevision)
	}

	// 终态丢弃。
	staleCAS := &scriptedCAS{script: []IncidentCASResult{{Status: IncidentCASStaleDispatchRevision, CurrentDispatchRevision: 9}}}
	staleCloser := mustOrphanCloser(t, store, staleCAS, &fakeActiveLister{rows: []IncidentCASRow{row}})
	result, err = staleCloser.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleSkipped != 1 {
		t.Fatalf("stale 应计数丢弃: %+v", result)
	}
}

// TestOrphanCloseInvalidRowSkipped 验证 scopeKey 与字段不一致的历史脏行记
// skip 不阻断。
func TestOrphanCloseInvalidRowSkipped(t *testing.T) {
	row := orphanTestRow("bogus-scope-key", 1000, 1)
	store := &stubRuntimeStore{states: map[string]CircuitState{}}
	cas := &scriptedCAS{}
	closer := mustOrphanCloser(t, store, cas, &fakeActiveLister{rows: []IncidentCASRow{row}})
	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvalidSkipped != 1 || len(cas.input) != 0 {
		t.Fatalf("脏行应跳过: %+v", result)
	}
}

func accountScopeKeyForTest(t *testing.T) string {
	t.Helper()
	return accountScopeKeyFor(t, "acc-1")
}

func accountScopeKeyFor(t *testing.T, runtimeKey string) string {
	t.Helper()
	scopeKey, err := AccountCircuitScopeKey(CircuitScope{Kind: CircuitScopeAccount, AccountRuntimeKey: runtimeKey})
	if err != nil {
		t.Fatal(err)
	}
	return scopeKey
}

// failingRuntimeStore 的 Get 一律报错：证明被测路径未触达运行态读取。
type failingRuntimeStore struct {
	CircuitStore
}

func (failingRuntimeStore) Get(context.Context, CircuitScope, int64) (CircuitState, error) {
	return CircuitState{}, errors.New("不得读取运行态")
}

// TestOrphanCloseSkipsHandoverStates 验证持久冷却交接态（PERSISTING /
// SHADOWED_BY_PERSISTENT）在判定链最前直接跳过：不读运行态、不结清。
func TestOrphanCloseSkipsHandoverStates(t *testing.T) {
	persisting := orphanTestRow(accountScopeKeyFor(t, "acc-1"), 1000, 1)
	persisting.State = string(CircuitIncidentPersisting)
	shadowed := orphanTestRow(accountScopeKeyFor(t, "acc-2"), 1000, 1)
	shadowed.State = string(CircuitIncidentShadowedByPersistent)
	cas := &scriptedCAS{}
	closer := mustOrphanCloser(t, failingRuntimeStore{}, cas, &fakeActiveLister{rows: []IncidentCASRow{persisting, shadowed}})

	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || result.SkippedHandover != 2 {
		t.Fatalf("交接态应跳过: %+v", result)
	}
	if result.Errors != 0 || len(cas.input) != 0 {
		t.Fatalf("交接态不得读运行态或发起 CAS: %+v %d", result, len(cas.input))
	}
}

// TestOrphanCloseConflictAbortsWhenRuntimeReopened 验证冲突重试前复查运行态：
// "Get 缺键 → CAS 提交"窗口内 scope 被 gateway 以等 generation 复开时，放弃
// 本次结清且不发起第二次 CAS（等代复开不受 generation 围栏保护，盲目重试会
// 把新 OPEN 行结清为 CLOSED）。
func TestOrphanCloseConflictAbortsWhenRuntimeReopened(t *testing.T) {
	scopeKey := accountScopeKeyFor(t, "acc-1")
	row := orphanTestRow(scopeKey, 1000, 3)
	store := &stubRuntimeStore{states: map[string]CircuitState{}}
	var casCalls int
	cas := casFunc(func(_ context.Context, _ IncidentCASInput) (IncidentCASResult, error) {
		casCalls++
		if casCalls == 1 {
			// 模拟竞态：CAS 提交后、复查前，gateway 复开该 scope 运行态。
			store.mu.Lock()
			store.states[scopeKey] = projectionTestState(scopeKey, "OPEN", 2000)
			store.mu.Unlock()
			return IncidentCASResult{Status: IncidentCASConflict, Incident: &IncidentCASRow{LedgerRevision: 4}}, nil
		}
		return IncidentCASResult{Status: IncidentCASApplied, Incident: &IncidentCASRow{CircuitScopeKey: scopeKey, LedgerRevision: 5}}, nil
	})
	closer := mustOrphanCloser(t, store, cas, &fakeActiveLister{rows: []IncidentCASRow{row}})

	result, err := closer.Sweep(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if casCalls != 1 {
		t.Fatalf("运行态复开后不得重试 CAS: %d", casCalls)
	}
	if result.SkippedReopened != 1 || result.ClosedRetired != 0 {
		t.Fatalf("应计 skippedReopened 放弃结清: %+v", result)
	}
}
