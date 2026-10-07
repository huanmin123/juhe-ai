package circuitstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// casTestInput 构造最小合法 SUSPECT CAS 输入（account scope）。
func casTestInput(scopeKey, accountID, state string, generation, dispatchRevision, updatedAtMS int64) opsjobs.IncidentCASInput {
	return opsjobs.IncidentCASInput{Incident: opsjobs.IncidentCASRow{
		CircuitScopeKey:                 scopeKey,
		AccountID:                       accountID,
		AccountRuntimeKey:               accountID,
		ScopeKind:                       "account",
		IncidentID:                      "incident-" + scopeKey,
		ChildIncidentIDs:                []string{},
		State:                           state,
		Generation:                      generation,
		DispatchRevision:                dispatchRevision,
		TransitionID:                    "tr-" + scopeKey,
		ConfirmationFailuresRequired:    2,
		ConfirmationFailureEvidenceKeys: []string{},
		UpstreamAttemptObserved:         true,
		UpdatedAtMS:                     updatedAtMS,
	}}
}

func mustAccount(t *testing.T, db *sql.DB, id string, dispatch int64, deleted bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id, dispatch_revision) VALUES (?, ?)`, id, dispatch); err != nil {
		t.Fatal(err)
	}
	if deleted {
		if _, err := db.Exec(`UPDATE accounts SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompareAndSetIncidentApplyIdempotentConflict 覆盖 CAS 主链：新行插入
// 围栏（expected=nil 且行已存在 → conflict）、revision 相等推进、dedupe 回放
// 幂等、generation 回退冲突。
func TestCompareAndSetIncidentApplyIdempotentConflict(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	mustAccount(t, db, "acc-1", 5, false)

	// 新行插入：applied，ledger_revision=1，projected=0，created 落 0（对齐
	// gateway 链适配语义），同事务 outbox incident_changed。
	result, err := repo.CompareAndSetIncident(ctx, casTestInput("sk-1", "acc-1", "SUSPECT", 1, 5, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != opsjobs.IncidentCASApplied || result.Incident == nil || result.Incident.LedgerRevision != 1 {
		t.Fatalf("首插应 applied rev=1: %+v", result)
	}
	if result.Incident.CreatedAtMS != 0 || result.Incident.ProjectedLedgerRevision != 0 {
		t.Fatalf("新行 created/projected 应落 0: %+v", result.Incident)
	}
	var (
		state       string
		ledger      int64
		projected   int64
		outboxType  string
		outboxScope string
	)
	if err := db.QueryRow(`SELECT state, ledger_revision, projected_ledger_revision FROM account_circuit_incidents WHERE circuit_scope_key='sk-1'`).
		Scan(&state, &ledger, &projected); err != nil {
		t.Fatal(err)
	}
	if state != "SUSPECT" || ledger != 1 || projected != 0 {
		t.Fatalf("持久化行不符: %s %d %d", state, ledger, projected)
	}
	if err := db.QueryRow(`SELECT event_type, circuit_scope_key FROM account_circuit_outbox WHERE dedupe_key='incident:tr-sk-1'`).
		Scan(&outboxType, &outboxScope); err != nil {
		t.Fatal(err)
	}
	if outboxType != "incident_changed" || outboxScope != "sk-1" {
		t.Fatalf("outbox 事件不符: %s %s", outboxType, outboxScope)
	}

	// 相同 transitionID 重放：dedupe 命中 → idempotent 返回当前行。
	replay, err := repo.CompareAndSetIncident(ctx, casTestInput("sk-1", "acc-1", "SUSPECT", 1, 5, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Status != opsjobs.IncidentCASIdempotent || replay.Incident == nil || replay.Incident.LedgerRevision != 1 {
		t.Fatalf("重放应 idempotent rev=1: %+v", replay)
	}

	// 新 transition 且 expected=nil 而行已存在 → cas_conflict（新行围栏），
	// 响应携带当前行。
	conflict := casTestInput("sk-1", "acc-1", "OPEN", 1, 5, 2000)
	conflict.Incident.TransitionID = "tr-sk-1-b"
	conflictResult, err := repo.CompareAndSetIncident(ctx, conflict)
	if err != nil {
		t.Fatal(err)
	}
	if conflictResult.Status != opsjobs.IncidentCASConflict || conflictResult.Incident == nil || conflictResult.Incident.LedgerRevision != 1 {
		t.Fatalf("expected=nil 应冲突并回带当前行: %+v", conflictResult)
	}

	// expected 相等 → applied，ledger_revision 推进到 2。
	expected := int64(1)
	input := casTestInput("sk-1", "acc-1", "OPEN", 1, 5, 2000)
	input.Incident.TransitionID = "tr-sk-1-c"
	updated, err := repo.CompareAndSetIncident(ctx, opsjobs.IncidentCASInput{Incident: input.Incident, ExpectedLedgerRevision: &expected})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != opsjobs.IncidentCASApplied || updated.Incident.LedgerRevision != 2 {
		t.Fatalf("expected 相等应 applied rev=2: %+v", updated)
	}

	// expected 失配 → cas_conflict。
	wrongExpected := int64(0)
	staleInput := casTestInput("sk-1", "acc-1", "OPEN", 1, 5, 2000)
	staleInput.Incident.TransitionID = "tr-sk-1-d"
	stale, err := repo.CompareAndSetIncident(ctx, opsjobs.IncidentCASInput{Incident: staleInput.Incident, ExpectedLedgerRevision: &wrongExpected})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != opsjobs.IncidentCASConflict || stale.Incident == nil || stale.Incident.LedgerRevision != 2 {
		t.Fatalf("expected 失配应冲突: %+v", stale)
	}

	// generation 回退 → cas_conflict（expected 与当前 revision 相等，绕过
	// expected 臂到达 generation 臂）。
	currentExpected := int64(2)
	regressed := casTestInput("sk-1", "acc-1", "OPEN", 0, 5, 3000)
	regressed.Incident.TransitionID = "tr-sk-1-e"
	regressedResult, err := repo.CompareAndSetIncident(ctx, opsjobs.IncidentCASInput{Incident: regressed.Incident, ExpectedLedgerRevision: &currentExpected})
	if err != nil {
		t.Fatal(err)
	}
	if regressedResult.Status != opsjobs.IncidentCASConflict {
		t.Fatalf("generation 回退应冲突: %+v", regressedResult)
	}
}

// TestCompareAndSetIncidentAccountGates 覆盖归档热修终态围栏：账户缺失 /
// 已逻辑删除 → account_not_found；dispatch revision 失配 →
// stale_dispatch_revision。
func TestCompareAndSetIncidentAccountGates(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	mustAccount(t, db, "acc-live", 7, false)
	mustAccount(t, db, "acc-deleted", 3, true)

	missing, err := repo.CompareAndSetIncident(ctx, casTestInput("sk-m", "acc-missing", "SUSPECT", 1, 1, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != opsjobs.IncidentCASAccountNotFound || missing.CurrentDispatchRevision != 0 {
		t.Fatalf("账户缺失应 account_not_found: %+v", missing)
	}
	deleted, err := repo.CompareAndSetIncident(ctx, casTestInput("sk-d", "acc-deleted", "SUSPECT", 1, 3, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != opsjobs.IncidentCASAccountNotFound || deleted.CurrentDispatchRevision != 3 {
		t.Fatalf("已删账户应 account_not_found 并带当前 revision: %+v", deleted)
	}
	stale, err := repo.CompareAndSetIncident(ctx, casTestInput("sk-s", "acc-live", "SUSPECT", 1, 6, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != opsjobs.IncidentCASStaleDispatchRevision || stale.CurrentDispatchRevision != 7 {
		t.Fatalf("revision 失配应 stale_dispatch_revision: %+v", stale)
	}
	if _, err := db.Exec(`DELETE FROM account_circuit_incidents WHERE circuit_scope_key IN ('sk-m','sk-d','sk-s')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_circuit_incidents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("围栏终态不得落行: %d", count)
	}
}

// TestCompareAndSetIncidentValidation 覆盖入参校验：CLOSED 必须带 retained、
// 非 CLOSED 不得带、租约四元组必须齐全。
func TestCompareAndSetIncidentValidation(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	mustAccount(t, db, "acc-1", 5, false)

	closed := casTestInput("sk-c", "acc-1", "CLOSED", 1, 5, 1000)
	if _, err := repo.CompareAndSetIncident(ctx, closed); err == nil || !strings.Contains(err.Error(), "retained_until_ms") {
		t.Fatalf("CLOSED 缺 retained 应报错: %v", err)
	}
	retained := int64(2000)
	closed.Incident.RetainedUntilMS = &retained
	applied, err := repo.CompareAndSetIncident(ctx, closed)
	if err != nil || applied.Status != opsjobs.IncidentCASApplied {
		t.Fatalf("CLOSED 带 retained 应通过: %+v %v", applied, err)
	}
	nonClosed := casTestInput("sk-n", "acc-1", "SUSPECT", 1, 5, 1000)
	nonClosed.Incident.RetainedUntilMS = &retained
	if _, err := repo.CompareAndSetIncident(ctx, nonClosed); err == nil || !strings.Contains(err.Error(), "non-closed") {
		t.Fatalf("非 CLOSED 带 retained 应报错: %v", err)
	}
	leased := casTestInput("sk-l", "acc-1", "HALF_OPEN", 1, 5, 1000)
	leasePurpose := "half_open"
	leased.Incident.LeasePurpose = &leasePurpose
	if _, err := repo.CompareAndSetIncident(ctx, leased); err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("租约字段不齐应报错: %v", err)
	}
	// 租约四元组齐全但无 attempt 时间戳：与 Node bridge 输入一致，必须通过
	// （相对 gateway 校验的刻意偏差，见 validateIncidentCAS 注释）。
	leaseID := "lease-1"
	ownerRun := "owner-1"
	leaseUntil := int64(5000)
	full := casTestInput("sk-f", "acc-1", "HALF_OPEN", 1, 5, 1000)
	full.Incident.LeaseID = &leaseID
	full.Incident.LeasePurpose = &leasePurpose
	full.Incident.LeaseOwnerRunID = &ownerRun
	full.Incident.LeaseUntilMS = &leaseUntil
	fullLeased, err := repo.CompareAndSetIncident(ctx, full)
	if err != nil || fullLeased.Status != opsjobs.IncidentCASApplied {
		t.Fatalf("租约四元组无 attempt 应通过: %+v %v", fullLeased, err)
	}
}

// TestListActiveIncidentsPage 验证非围栏活动行 keyset 分页：排除 CLOSED、
// 游标推进、排序稳定。
func TestListActiveIncidentsPage(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	mustAccount(t, db, "acc-1", 5, false)
	seedIncident(t, db, "sk-a", "acc-1", "acc-1", "OPEN", 1, 5, 1, 100)
	seedIncident(t, db, "sk-b", "acc-1", "acc-1", "SUSPECT", 1, 5, 2, 100)
	seedIncident(t, db, "sk-closed", "acc-1", "acc-1", "CLOSED", 1, 5, 3, 300)

	first, err := repo.ListActiveIncidentsPage(ctx, -1, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].CircuitScopeKey != "sk-a" {
		t.Fatalf("第一页应含 sk-a: %+v", first)
	}
	second, err := repo.ListActiveIncidentsPage(ctx, first[0].UpdatedAtMS, first[0].CircuitScopeKey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].CircuitScopeKey != "sk-b" {
		t.Fatalf("第二页应只含 sk-b（CLOSED 排除）: %+v", second)
	}
	if len(first[0].ChildIncidentIDs) != 0 || first[0].ConfirmationFailuresRequired != 1 {
		t.Fatalf("全列扫描默认值不符: %+v", first[0])
	}
}

// TestCleanupRetiresExpiredClosedIncidents 验证删除条件：CLOSED + 保留期到
// 期 + 投影水位覆盖 + 无未 dispatched outbox 才删除。
func TestCleanupRetiresExpiredClosedIncidents(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	mustAccount(t, db, "acc-1", 5, false)
	// 可删：CLOSED、retained 已过、projected>=ledger、无 outbox。
	seedIncident(t, db, "sk-gone", "acc-1", "acc-1", "CLOSED", 1, 5, 3, 100)
	if _, err := db.Exec(`UPDATE account_circuit_incidents SET retained_until_ms = 500, projected_ledger_revision = 3, ledger_revision = 3 WHERE circuit_scope_key = 'sk-gone'`); err != nil {
		t.Fatal(err)
	}
	// 保留：projected 落后。
	seedIncident(t, db, "sk-behind", "acc-1", "acc-1", "CLOSED", 1, 5, 4, 100)
	if _, err := db.Exec(`UPDATE account_circuit_incidents SET retained_until_ms = 500, projected_ledger_revision = 2, ledger_revision = 4 WHERE circuit_scope_key = 'sk-behind'`); err != nil {
		t.Fatal(err)
	}
	// 保留：有未 dispatched outbox。
	seedIncident(t, db, "sk-pending", "acc-1", "acc-1", "CLOSED", 1, 5, 1, 100)
	if _, err := db.Exec(`UPDATE account_circuit_incidents SET retained_until_ms = 500, projected_ledger_revision = 1 WHERE circuit_scope_key = 'sk-pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			circuit_scope_key, incident_id, transition_id, dispatch_revision, status,
			available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('evt-p', 'account_circuit_runtime_v1', 'incident:tr-x', 'incident_changed', 'acc-1', 'acc-1',
			'sk-pending', 'incident-sk-pending', 'tr-x', 5, 'pending', 0, 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	// 保留：保留期未到。
	seedIncident(t, db, "sk-fresh", "acc-1", "acc-1", "CLOSED", 1, 5, 1, 100)
	if _, err := db.Exec(`UPDATE account_circuit_incidents SET retained_until_ms = 5000, projected_ledger_revision = 1 WHERE circuit_scope_key = 'sk-fresh'`); err != nil {
		t.Fatal(err)
	}
	// 保留：非 CLOSED。
	seedIncident(t, db, "sk-open", "acc-1", "acc-1", "OPEN", 1, 5, 1, 100)

	deleted, err := repo.Cleanup(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("应恰好删除 sk-gone: %d", deleted)
	}
	for scopeKey, want := range map[string]bool{"sk-gone": false, "sk-behind": true, "sk-pending": true, "sk-fresh": true, "sk-open": true} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM account_circuit_incidents WHERE circuit_scope_key = ?`, scopeKey).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if (count > 0) != want {
			t.Fatalf("%s 存在性不符: count=%d want=%v", scopeKey, count, want)
		}
	}
}
