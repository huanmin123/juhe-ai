package circuitcontrolplane

// w9e 覆盖率战役第二波：直接单测校验器错误分支、owner gate 矩阵、
// 谓词归一化循环、表达式索引识别与"回执无事故"等可达分支。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestW9EValidateIncidentErrorBranchMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*IncidentMutation)
		want   string
	}{
		{"runtime key 空白", func(m *IncidentMutation) { m.Incident.AccountRuntimeKey = " " }, "account runtime key is required"},
		{"runtime key 超长", func(m *IncidentMutation) { m.Incident.AccountRuntimeKey = strings.Repeat("x", 1025) }, "account runtime key is too long"},
		{"scope key 超长", func(m *IncidentMutation) { m.Incident.CircuitScopeKey = strings.Repeat("x", 2049) }, "circuit scope key is too long"},
		{"account id 超长", func(m *IncidentMutation) { m.Incident.AccountID = strings.Repeat("x", 257) }, "account id is too long"},
		{"transition id 超长", func(m *IncidentMutation) { m.Incident.TransitionID = strings.Repeat("x", 257) }, "transition id is too long"},
		{"key fingerprint 超长", func(m *IncidentMutation) {
			m.Incident.ScopeKind = "key"
			m.Incident.KeyFingerprint = strPtr2(strings.Repeat("x", 257))
		}, "key fingerprint is too long"},
		{"key fingerprint 空白", func(m *IncidentMutation) { m.Incident.ScopeKind = "key"; m.Incident.KeyFingerprint = strPtr2(" ") }, "key fingerprint is required"},
		{"protocol code 空白", func(m *IncidentMutation) {
			m.Incident.ScopeKind = "protocol_model"
			m.Incident.ProtocolCode = strPtr2(" ")
			m.Incident.RequestLane = strPtr2("lane")
			m.Incident.ModelFamily = strPtr2("fam")
		}, "protocol code is required"},
		{"request lane 空白", func(m *IncidentMutation) {
			m.Incident.ScopeKind = "protocol_model"
			m.Incident.ProtocolCode = strPtr2("openai")
			m.Incident.RequestLane = strPtr2(" ")
			m.Incident.ModelFamily = strPtr2("fam")
		}, "request lane is required"},
		{"model family 空白", func(m *IncidentMutation) {
			m.Incident.ScopeKind = "protocol_model"
			m.Incident.ProtocolCode = strPtr2("openai")
			m.Incident.RequestLane = strPtr2("lane")
			m.Incident.ModelFamily = strPtr2(" ")
		}, "model family is required"},
		{"lease id 空白", func(m *IncidentMutation) { m.Incident.LeaseID = strPtr2(" ") }, "lease id is required"},
		{"lease owner run id 空白", func(m *IncidentMutation) { m.Incident.LeaseOwnerRunID = strPtr2(" ") }, "lease owner run id is required"},
		{"parent incident id 空白", func(m *IncidentMutation) { m.Incident.ParentIncidentID = strPtr2(" ") }, "parent incident id is required"},
		{"caused by 空白", func(m *IncidentMutation) { m.Incident.CausedByTerminalOutcomeID = strPtr2(" ") }, "caused by terminal outcome id is required"},
		{"capability hash 超长", func(m *IncidentMutation) { m.Incident.CapabilityHash = strPtr2(strings.Repeat("x", 129)) }, "capability hash is too long"},
		{"child id 超长", func(m *IncidentMutation) { m.Incident.ChildIncidentIDs = []string{strings.Repeat("x", 257)} }, "child incident id is too long"},
		{"evidence 重复", func(m *IncidentMutation) {
			hash := strings.Repeat("a", 64)
			m.Incident.ConfirmationFailureEvidenceKeys = []string{hash, hash}
			m.Incident.ConfirmationFailuresRequired = 2
		}, "confirmation evidence keys must be unique"},
		{"required confirmation failures 为 0", func(m *IncidentMutation) { m.Incident.ConfirmationFailuresRequired = 0 }, "incident numeric values are invalid"},
	}
	ctx := context.Background()
	for _, tc := range cases {
		mut := wkBaseIncident("w9e-matrix", "a1", 1)
		mut.Incident.TransitionID = "tr-matrix-" + tc.name
		tc.mutate(&mut)
		_, err := storeCAS(t, ctx, mut)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want contains %q", tc.name, err, tc.want)
		}
	}
}

func storeCAS(t *testing.T, ctx context.Context, mut IncidentMutation) (IncidentResult, error) {
	t.Helper()
	s, db := wkReadyStore(t)
	defer db.Close()
	return s.CompareAndSetIncident(ctx, mut)
}

func TestW9EValidateOptionalTextDirect(t *testing.T) {
	if err := validateOptionalText(nil, 10, "x"); err != nil {
		t.Fatalf("nil value should pass: %v", err)
	}
	if err := validateOptionalText(strPtr2(" "), 10, "x"); err == nil || !strings.Contains(err.Error(), "x is required") {
		t.Fatalf("blank value = %v", err)
	}
	if err := validateOptionalText(strPtr2(strings.Repeat("x", 11)), 10, "x"); err == nil || !strings.Contains(err.Error(), "x is too long") {
		t.Fatalf("long value = %v", err)
	}
	if err := validateOptionalText(strPtr2("ok"), 10, "x"); err != nil {
		t.Fatalf("valid value = %v", err)
	}
}

func TestW9EValidateIncidentTimesDirect(t *testing.T) {
	v := &Incident{CreatedAtMS: 100, UpdatedAtMS: 100}
	if err := validateIncidentTimes(v, 100); err != nil {
		t.Fatalf("valid times = %v", err)
	}
	v.UpdatedAtMS = -1
	if err := validateIncidentTimes(v, 100); err == nil || !strings.Contains(err.Error(), "updatedAtMs cannot be negative") {
		t.Fatalf("negative updated = %v", err)
	}
	v.UpdatedAtMS = 100
	v.CreatedAtMS = 200
	if err := validateIncidentTimes(v, 300); err == nil || !strings.Contains(err.Error(), "created_at_ms cannot follow") {
		t.Fatalf("created follows updated = %v", err)
	}
	v.CreatedAtMS = 0
	v.NextTransitionAtMS = int64Ptr2(-2)
	if err := validateIncidentTimes(v, 300); err == nil || !strings.Contains(err.Error(), "nextTransitionAtMs cannot be negative") {
		t.Fatalf("negative next transition = %v", err)
	}
	v.NextTransitionAtMS = nil
	v.RetainedUntilMS = int64Ptr2(50)
	v.State = "CLOSED"
	if err := validateIncidentTimes(v, 100); err == nil || !strings.Contains(err.Error(), "retained_until_ms cannot precede") {
		t.Fatalf("retained precedes updated = %v", err)
	}
	v.RetainedUntilMS = nil
	v.State = "OPEN"
	v.LeaseUntilMS = int64Ptr2(500)
	v.AttemptStartedAtMS = int64Ptr2(100)
	v.AttemptHardDeadlineMS = int64Ptr2(600)
	if err := validateIncidentTimes(v, 300); err == nil || !strings.Contains(err.Error(), "hard deadline <= lease until") {
		t.Fatalf("deadline exceeds lease = %v", err)
	}
}

func TestW9EPredicatesEquivalentParenTrim(t *testing.T) {
	// 嵌套括号被逐层剥掉后等价。
	if !predicatesEquivalent("(( a = 1 ))", "a = 1") {
		t.Fatal("嵌套括号应等价")
	}
	if !predicatesEquivalent("((a = 1 AND b = 2))", "B = 2 and A = 1") {
		t.Fatal("括号 + 顺序应等价")
	}
}

func TestW9EOwnerGateBlocksEveryMethod(t *testing.T) {
	_, db := wkReadyStore(t)
	defer db.Close()
	s, err := New(db, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CheckContract = %v", err)
	}
	if _, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Advance = %v", err)
	}
	if _, err := s.ListDispatchRevisions(ctx, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ListDispatch = %v", err)
	}
	if _, err := s.LoadIncidentForProjection(ctx, Outbox{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Load = %v", err)
	}
	if _, err := s.CompareAndSetIncident(ctx, IncidentMutation{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CAS = %v", err)
	}
	if _, _, err := s.GetIncident(ctx, "scope"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Get = %v", err)
	}
	if _, err := s.ClaimOutbox(ctx, "o", 1, 1, 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Claim = %v", err)
	}
	if _, err := s.AcknowledgeOutbox(ctx, "e", ProjectionKey, "t", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Ack = %v", err)
	}
	if _, err := s.ReleaseOutboxForReplay(ctx, "e", "t", "c", 1, 0); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Release = %v", err)
	}
	if _, err := s.ListForRebuild(ctx, 1, 0, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Rebuild = %v", err)
	}
	if _, err := s.ListByRuntimeKeys(ctx, []string{"k"}, false, 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("RuntimeKeys = %v", err)
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Gaps = %v", err)
	}
	if _, err := s.Cleanup(ctx, 1, 1, 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Cleanup = %v", err)
	}
}

func TestW9EDedupeReceiptWithoutIncident(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// 先手工落下 dedupe 回执，但不写 incident 行。
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,circuit_scope_key,incident_id,transition_id,dispatch_revision,ledger_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms) VALUES ('evt','` + ProjectionKey + `','incident:tr-orphan','incident_changed','a1','a1','w9e-orphan','inc-w9e-orphan','tr-orphan',1,1,'pending',1,0,1,1)`); err != nil {
		t.Fatal(err)
	}
	mut := wkBaseIncident("w9e-orphan", "a1", 1)
	mut.Incident.TransitionID = "tr-orphan"
	if _, err := s.CompareAndSetIncident(ctx, mut); err == nil || !strings.Contains(err.Error(), "deduplicated incident receipt has no incident") {
		t.Fatalf("orphan receipt = %v", err)
	}
}

func TestW9ECASUpdateTimeOverrideFails(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	first := wkBaseIncident("w9e-time", "a1", 1)
	if _, err := s.CompareAndSetIncident(ctx, first); err != nil {
		t.Fatal(err)
	}
	// 更新时 created 会被覆盖为既有行的 100，而 updated=50 → 时间校验在
	// 覆盖之后才失败（行内 CAS 分支）。
	second := wkBaseIncident("w9e-time", "a1", 1)
	second.Incident.CreatedAtMS = 10
	second.UpdatedAtMS = 50
	second.Incident.TransitionID = "tr-time-2"
	second.ExpectedLedgerRevision = int64Ptr2(1)
	if _, err := s.CompareAndSetIncident(ctx, second); err == nil || !strings.Contains(err.Error(), "created_at_ms cannot follow") {
		t.Fatalf("override path = %v", err)
	}
}

func TestW9EExpressionIndexRejected(t *testing.T) {
	db, _ := w9eDDLDB(t, "expr", `CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, lower(capability_hash)) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`)
	s, _ := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	err := s.CheckContract(context.Background())
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("表达式索引应报 expression, got %v", err)
	}
}

func TestW9EClaimOutboxCorruptRowFails(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// attempt_count 写入非数值文本（WHERE 不涉及该列，行会被选出并在 scan 时失败）。
	if _, err := db.Exec(`INSERT INTO account_circuit_outbox (event_id,projection_key,dedupe_key,event_type,account_id,account_runtime_key,transition_id,dispatch_revision,status,available_at_ms,attempt_count,created_at_ms,updated_at_ms) VALUES ('bad','` + ProjectionKey + `','d-bad','incident_changed','a1','a1','t-bad',1,'pending',1,'not-a-number',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOutbox(ctx, "owner", 10, 1000, 10); err == nil {
		t.Fatal("损坏行必须使 claim 失败")
	}
}

func TestW9EScanIncidentTimeValidationFailure(t *testing.T) {
	// 通过 scanIncident 行读取路径触发 validateIncidentTimes 的负值分支。
	values := []any{
		"scope", "a1", "a1", "account",
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
		(*string)(nil), (*string)(nil),
		strPtr2("inc-1"), (*string)(nil), "[]", (*string)(nil), "OPEN", (*string)(nil),
		int64(1), int64(1), int64(1), int64(0), "tr", int64(0),
		int64Ptr2(-5), (*int64)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*int64)(nil),
		(*int64)(nil), (*int64)(nil), 0, int64(0), int64(0), int64(1), "[]", int64(0),
		(*string)(nil), (*int64)(nil), int64(100), int64(100),
	}
	if _, err := scanIncident(&fakeScanner{values: values}); err == nil || !strings.Contains(err.Error(), "openUntilMs cannot be negative") {
		t.Fatalf("scan negative openUntil = %v", err)
	}
}

func TestW9EListProjectionGapsCorruptAccountRow(t *testing.T) {
	s, db := wkReadyStore(t)
	defer db.Close()
	ctx := context.Background()
	// dispatch_revision 写入非数值文本，缺口查询 scan 失败。
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('bad','xyz',0)`); err != nil {
		t.Skipf("SQLite 类型约束不允许注入: %v", err)
	}
	if _, err := s.ListProjectionGaps(ctx, "", 0, "", 10); err == nil {
		t.Fatal("损坏账户行必须使缺口查询失败")
	}
}
