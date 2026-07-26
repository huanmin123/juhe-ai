package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

func TestModelQualityRecoverySQLFencesOwnershipAndLockOrder(t *testing.T) {
	t.Parallel()
	for name, sql := range map[string]string{
		"candidate":  claimDueModelQualityRecoveryCandidatesSQL,
		"claim":      claimModelQualityRecoverySQL,
		"scope":      findModelQualityRecoveryScopeSQL,
		"lock":       lockModelQualityRecoveryEnforcementSQL,
		"reschedule": rescheduleModelQualityRecoverySQL,
		"clear":      clearModelQualityRecoveryEnforcementSQL,
	} {
		for _, fragment := range []string{"enforcement_id", "generation"} {
			if name != "candidate" && !strings.Contains(sql, fragment) {
				t.Fatalf("%s SQL missing %q fence:\n%s", name, fragment, sql)
			}
		}
	}
	for _, fragment := range []string{
		"FOR UPDATE OF aqe SKIP LOCKED", "accounts.authorization_instance_authorization_id IS NULL",
		"accounts.status = 'quality_isolated'", "LIMIT $2",
	} {
		if !strings.Contains(claimDueModelQualityRecoveryCandidatesSQL, fragment) {
			t.Fatalf("claim candidate SQL missing %q", fragment)
		}
	}
	for _, sql := range []string{claimModelQualityRecoverySQL, rescheduleModelQualityRecoverySQL, clearModelQualityRecoveryEnforcementSQL} {
		for _, fragment := range []string{"recovery_lease_token", "recovery_lease_until", "model_quality_policies"} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("recovery mutation SQL missing %q:\n%s", fragment, sql)
			}
		}
	}
	if strings.Contains(claimModelQualityRecoverySQL, "aqe.policy_revision = $9") {
		t.Fatal("claim must not pin recovery to the enforcement's historical policy revision")
	}
	if !strings.Contains(lockModelQualityRecoveryAccountSQL, "FOR UPDATE") || !strings.Contains(lockModelQualityRecoveryEnforcementSQL, "FOR UPDATE") {
		t.Fatal("completion must lock both account and enforcement rows")
	}
	if strings.Contains(claimDueModelQualityRecoveryCandidatesSQL, "FOR UPDATE OF accounts") {
		t.Fatal("claim must not hold an account lock after taking the enforcement lock")
	}
	for _, fragment := range []string{
		"status = $1", "schedulable = $2", "config_revision = config_revision + 1",
		"last_error_code = NULL", "authorization_instance_authorization_id IS NULL",
	} {
		if !strings.Contains(recoverModelQualityAccountSQL, fragment) {
			t.Fatalf("account recovery SQL missing %q", fragment)
		}
	}
}

func TestClaimDueModelQualityRecoveriesUsesBoundedTokenizedCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(6 * time.Minute)
	enforcement := modelQualityRecoveryScanValues(now, pgtype.Text{String: modelQualityPolicyTimeText(now.Add(-time.Minute)), Valid: true}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{})
	// The enforcement keeps the policy revision that originally created it.
	// A newer effective policy must not make this isolation unrecoverable.
	enforcement[7] = int64(4)
	row := append(enforcement, "gpt-5", int64(8))
	tx := &modelQualityScheduleTxStub{
		queryRows:     &modelQualityScheduleRowsStub{rows: [][]any{row}},
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: modelQualityRecoveryPolicyValues(now, 5)}},
		execTags:      []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	claims, err := claimDueModelQualityRecoveries(context.Background(), beginModelQualityRecoveryTestTx(tx), port.ModelQualityRecoveryClaimInput{
		OwnerID: "worker-1", Now: now, LeaseUntil: leaseUntil,
	}, func() (string, error) { return "mqr_claim_token", nil })
	if err != nil {
		t.Fatalf("claimDueModelQualityRecoveries() error = %v", err)
	}
	if len(claims) != 1 || claims[0].Model != "gpt-5" || claims[0].ExpectedAccountConfigRevision != 8 ||
		claims[0].Policy.Policy.Revision != 5 || claims[0].Enforcement.PolicyRevision != 4 {
		t.Fatalf("claims = %#v", claims)
	}
	if claims[0].Lease.ClaimToken != "mqr_claim_token" || claims[0].Enforcement.RecoveryLease == nil || claims[0].Enforcement.AccountConfigRevision != 8 {
		t.Fatalf("tokenized claim = %#v", claims[0])
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.execCalls) != 1 {
		t.Fatalf("transaction commit/rollback/exec = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
	if got := tx.queryCalls[0].args[1]; got != port.ModelQualityRecoveryClaimDefaultLimit {
		t.Fatalf("default limit = %v", got)
	}
	args := tx.execCalls[0].args
	if args[1] != "mqr_claim_token" || args[3] != int64(8) || args[8] != int64(5) {
		t.Fatalf("claim CAS args = %#v", args)
	}
}

func TestClaimDueModelQualityRecoveriesRollsBackOnEntropyFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	row := append(modelQualityRecoveryScanValues(now, pgtype.Text{String: modelQualityPolicyTimeText(now.Add(-time.Minute)), Valid: true}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}), "gpt-5", int64(8))
	tx := &modelQualityScheduleTxStub{
		queryRows:     &modelQualityScheduleRowsStub{rows: [][]any{row}},
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: modelQualityRecoveryPolicyValues(now, 5)}},
	}
	_, err := claimDueModelQualityRecoveries(context.Background(), beginModelQualityRecoveryTestTx(tx), port.ModelQualityRecoveryClaimInput{
		OwnerID: "worker-1", Now: now, LeaseUntil: now.Add(6 * time.Minute),
	}, func() (string, error) { return "", errors.New("entropy unavailable") })
	if err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("error = %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execCalls) != 0 {
		t.Fatalf("transaction commit/rollback/exec = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestCompleteModelQualityRecoveryReschedulesStalePolicy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	lease := port.ModelQualityRecoveryLease{OwnerID: "worker-1", ClaimToken: "claim-1", Until: now.Add(5 * time.Minute)}
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityScheduleRowStub{values: []any{"sys_admin"}},
			modelQualityScheduleRowStub{values: []any{"quality_isolated", int64(8), pgtype.Text{}}},
			modelQualityScheduleRowStub{values: modelQualityRecoveryScanValues(now, pgtype.Text{}, pgtype.Text{String: "worker-1", Valid: true}, pgtype.Text{String: "claim-1", Valid: true}, pgtype.Text{String: modelQualityPolicyTimeText(lease.Until), Valid: true})},
			modelQualityScheduleRowStub{values: modelQualityRecoveryPolicyValues(now, 6)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := completeModelQualityRecovery(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityRecoveryCompleteInput(now, lease, true))
	if err != nil {
		t.Fatalf("completeModelQualityRecovery() error = %v", err)
	}
	if result.Status != port.ModelQualityRecoveryStale || result.NextRecoveryAt == nil || !result.NextRecoveryAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("result = %#v", result)
	}
	if tx.commitCalls != 1 || len(tx.execCalls) != 1 || tx.execCalls[0].args[8] != "claim-1" {
		t.Fatalf("transaction or token fence = %#v", tx)
	}
}

func TestCompleteModelQualityRecoveryFailedProbeIgnoresUnrelatedMalformedSchedule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	lease := port.ModelQualityRecoveryLease{OwnerID: "worker-1", ClaimToken: "claim-1", Until: now.Add(5 * time.Minute)}
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityScheduleRowStub{values: []any{"sys_admin"}},
			modelQualityScheduleRowStub{values: []any{"quality_isolated", int64(8), pgtype.Text{String: "{", Valid: true}}},
			modelQualityScheduleRowStub{values: modelQualityRecoveryScanValues(now, pgtype.Text{}, pgtype.Text{String: "worker-1", Valid: true}, pgtype.Text{String: "claim-1", Valid: true}, pgtype.Text{String: modelQualityPolicyTimeText(lease.Until), Valid: true})},
			modelQualityScheduleRowStub{values: modelQualityRecoveryPolicyValues(now, 5)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	input := modelQualityRecoveryCompleteInput(now, lease, false)
	result, err := completeModelQualityRecovery(context.Background(), beginModelQualityRecoveryTestTx(tx), input)
	if err != nil || result.Status != port.ModelQualityRecoveryKeptIsolated || result.NextRecoveryAt == nil {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestCompleteModelQualityRecoveryAtomicallyRecoversAndClears(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	lease := port.ModelQualityRecoveryLease{OwnerID: "worker-1", ClaimToken: "claim-1", Until: now.Add(5 * time.Minute)}
	tx := modelQualityRecoveryCompletionTx(now, lease, pgconn.NewCommandTag("UPDATE 1"), pgconn.NewCommandTag("UPDATE 1"))
	result, err := completeModelQualityRecovery(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityRecoveryCompleteInput(now, lease, true))
	if err != nil {
		t.Fatalf("completeModelQualityRecovery() error = %v", err)
	}
	if result.Status != port.ModelQualityRecoveryRecovered || result.BeforeStatus == nil || *result.BeforeStatus != modelquality.AccountStatusQualityIsolated ||
		result.AfterStatus == nil || *result.AfterStatus != modelquality.AccountStatusActive {
		t.Fatalf("result = %#v", result)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.execCalls) != 2 {
		t.Fatalf("transaction commit/rollback/exec = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
	if tx.execCalls[0].args[1] != true || tx.execCalls[1].args[7] != "claim-1" {
		t.Fatalf("recovery mutation args = %#v / %#v", tx.execCalls[0].args, tx.execCalls[1].args)
	}
}

func TestCompleteModelQualityRecoveryRollsBackAccountWhenClearLosesCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	lease := port.ModelQualityRecoveryLease{OwnerID: "worker-1", ClaimToken: "claim-1", Until: now.Add(5 * time.Minute)}
	tx := modelQualityRecoveryCompletionTx(now, lease, pgconn.NewCommandTag("UPDATE 1"), pgconn.NewCommandTag("UPDATE 0"))
	_, err := completeModelQualityRecovery(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityRecoveryCompleteInput(now, lease, true))
	if err == nil || !strings.Contains(err.Error(), "was not cleared") {
		t.Fatalf("error = %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execCalls) != 2 {
		t.Fatalf("transaction commit/rollback/exec = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestCompleteModelQualityRecoveryRejectsExpiredLeaseBeforeStartingTransaction(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	called := false
	_, err := completeModelQualityRecovery(context.Background(), func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
		called = true
		return nil, errors.New("must not begin")
	}, modelQualityRecoveryCompleteInput(now, port.ModelQualityRecoveryLease{
		OwnerID: "worker-1", ClaimToken: "claim-1", Until: now,
	}, true))
	if err == nil || called {
		t.Fatalf("error/called = %v/%v", err, called)
	}
}

func modelQualityRecoveryCompletionTx(now time.Time, lease port.ModelQualityRecoveryLease, tags ...pgconn.CommandTag) *modelQualityScheduleTxStub {
	return &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityScheduleRowStub{values: []any{"sys_admin"}},
			modelQualityScheduleRowStub{values: []any{"quality_isolated", int64(8), pgtype.Text{}}},
			modelQualityScheduleRowStub{values: modelQualityRecoveryScanValues(now, pgtype.Text{}, pgtype.Text{String: "worker-1", Valid: true}, pgtype.Text{String: "claim-1", Valid: true}, pgtype.Text{String: modelQualityPolicyTimeText(lease.Until), Valid: true})},
			modelQualityScheduleRowStub{values: modelQualityRecoveryPolicyValues(now, 5)},
		},
		execTags: tags,
	}
}

func modelQualityRecoveryCompleteInput(now time.Time, lease port.ModelQualityRecoveryLease, passed bool) port.ModelQualityRecoveryCompleteInput {
	return port.ModelQualityRecoveryCompleteInput{
		AccountID: "account_1", ExpectedEnforcement: modelquality.EnforcementToken{ID: "enforcement_1", Generation: 2},
		ExpectedPolicyRevision: 5, ExpectedAccountConfigRevision: 8, Lease: lease,
		RunID: "run_1", Passed: passed, RecoveryInterval: 10 * time.Minute, CompletedAt: now,
	}
}

func modelQualityRecoveryScanValues(now time.Time, recoveryDue, leaseOwner, leaseToken, leaseUntil pgtype.Text) []any {
	return []any{
		"account_1", "sys_admin", "enforcement_1", int64(2), "active", "quality_isolate",
		"trigger_run_1", int64(5), int64(8), "active", "quality_isolated",
		int64(0), int64(0), recoveryDue, leaseOwner, leaseToken, leaseUntil,
		pgtype.Text{}, modelQualityPolicyTimeText(now.Add(-time.Hour)), pgtype.Text{}, modelQualityPolicyTimeText(now),
	}
}

func modelQualityRecoveryPolicyValues(now time.Time, revision int64) []any {
	return []any{
		"sys_admin", revision, "quick", int64(1), int64(70), "quality_isolate", int64(10),
		modelQualityPolicyTimeText(now.Add(-time.Hour)), modelQualityPolicyTimeText(now),
	}
}

func beginModelQualityRecoveryTestTx(tx pgx.Tx) modelQualityScheduleBeginTx {
	return func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil }
}
