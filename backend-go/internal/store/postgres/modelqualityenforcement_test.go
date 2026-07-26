package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

func TestModelQualityEnforcementSQLLocksAndFencesGeneration(t *testing.T) {
	t.Parallel()
	if !strings.Contains(lockModelQualityEnforcementAccountSQL, "FOR UPDATE OF accounts") ||
		!strings.Contains(lockModelQualityEnforcementSQL, "FOR UPDATE") {
		t.Fatal("enforcement apply must lock account before enforcement")
	}
	for _, fragment := range []string{
		"config_revision = accounts.config_revision + 1",
		"last_error_trace_id = NULL",
		"authorization_instance_authorization_id IS NULL",
		"policies.manual_enforcement_enabled = 1",
	} {
		if !strings.Contains(updateModelQualityEnforcementAccountSQL, fragment) {
			t.Fatalf("account mutation SQL missing %q", fragment)
		}
	}
	for _, sql := range []string{insertModelQualityEnforcementSQL, replaceModelQualityEnforcementSQL} {
		for _, fragment := range []string{
			"recovery_lease_owner", "recovery_lease_token", "recovery_lease_until",
			"model_quality_policies", "accounts.config_revision", "RETURNING",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("enforcement write SQL missing %q:\n%s", fragment, sql)
			}
		}
	}
	for _, fragment := range []string{
		"aqe.enforcement_id = $17", "aqe.generation = $18",
		"recovery_lease_owner = NULL", "recovery_lease_token = NULL",
		"recovery_lease_until = NULL", "last_recovery_run_id = NULL", "cleared_at = NULL",
	} {
		if !strings.Contains(replaceModelQualityEnforcementSQL, fragment) {
			t.Fatalf("replacement SQL missing %q", fragment)
		}
	}
	if !strings.Contains(insertModelQualityEnforcementSQL, "ON CONFLICT (account_id) DO NOTHING") {
		t.Fatal("first generation insert must fail closed on a concurrent writer")
	}
}

func TestApplyModelQualityEnforcementAtomicallyIsolatesAndCreatesGeneration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	due := now.Add(10 * time.Minute)
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_1", 1, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, &due)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityEnforcementInput(now, "quality_isolate"), func() (string, error) {
		return "enforcement_1", nil
	})
	if err != nil {
		t.Fatalf("applyModelQualityEnforcement() error = %v", err)
	}
	if result.Status != port.ModelQualityEnforcementApplied || result.Enforcement == nil ||
		result.Enforcement.Token.Generation != 1 || result.Enforcement.RecoveryDueAt == nil || !result.Enforcement.RecoveryDueAt.Equal(due) {
		t.Fatalf("result = %#v", result)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.execCalls) != 1 || len(tx.queryCalls) != 4 {
		t.Fatalf("transaction commit/rollback/exec/query = %d/%d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls), len(tx.queryCalls))
	}
	if tx.execCalls[0].args[1] != "quality_isolate" || tx.execCalls[0].args[8] != int64(5) || tx.queryCalls[3].args[14] != int64(8) {
		t.Fatalf("account/write CAS args = %#v / %#v", tx.execCalls[0].args, tx.queryCalls[3].args)
	}
}

func TestApplyModelQualityEnforcementTreatsClearedRunAsConsumed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	cleared := now.Add(-time.Minute)
	priorValues := modelQualityEnforcementRecordValues(now.Add(-time.Hour), "enforcement_old", 3, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, nil)
	priorValues[4] = "cleared"
	priorValues[19] = pgtype.Text{String: modelQualityPolicyTimeText(cleared), Valid: true}
	tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
		modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 12, false, false)},
		modelQualityEnforcementRowStub{values: priorValues},
	}}
	generatorCalled := false
	input := modelQualityEnforcementInput(now, "quality_isolate")
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
		generatorCalled = true
		return "unexpected", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementAlreadyEffective || result.Enforcement == nil || result.Enforcement.State != port.ModelQualityEnforcementCleared {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if generatorCalled || len(tx.execCalls) != 0 || len(tx.queryCalls) != 2 || tx.commitCalls != 1 {
		t.Fatalf("generator/exec/query/commit = %v/%d/%d/%d", generatorCalled, len(tx.execCalls), len(tx.queryCalls), tx.commitCalls)
	}
}

func TestApplyModelQualityEnforcementRecordsAlreadyEnabledFallbackWithoutAccountWrite(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
		modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, true, false)},
		modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
		modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "fallback", 1)},
		modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_1", 1, "fallback", "run_1", 5, 7, "active", "active", true, false, nil)},
	}}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityEnforcementInput(now, "fallback"), func() (string, error) {
		return "enforcement_1", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementAlreadyEffective || result.Enforcement == nil {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if len(tx.execCalls) != 0 || tx.commitCalls != 1 || tx.queryCalls[3].args[14] != int64(7) {
		t.Fatalf("exec/commit/write args = %d/%d/%#v", len(tx.execCalls), tx.commitCalls, tx.queryCalls[3].args)
	}
}

func TestApplyModelQualityEnforcementReplacesPriorGenerationWithExactCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	due := now.Add(10 * time.Minute)
	prior := modelQualityEnforcementRecordValues(now.Add(-time.Hour), "enforcement_old", 2, "fallback", "old_run", 4, 6, "active", "active", false, true, nil)
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{values: prior},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_new", 3, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, &due)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityEnforcementInput(now, "quality_isolate"), func() (string, error) {
		return "enforcement_new", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementApplied || result.Enforcement == nil || result.Enforcement.Token.Generation != 3 {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	writeArgs := tx.queryCalls[3].args
	if len(writeArgs) != 18 || writeArgs[16] != "enforcement_old" || writeArgs[17] != int64(2) {
		t.Fatalf("replacement generation CAS args = %#v", writeArgs)
	}
}

func TestApplyModelQualityEnforcementRollsBackAccountWhenGenerationWriteLosesCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	_, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityEnforcementInput(now, "quality_isolate"), func() (string, error) {
		return "enforcement_1", nil
	})
	if err == nil || !strings.Contains(err.Error(), "lost its account, policy, or generation CAS") {
		t.Fatalf("error = %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execCalls) != 1 {
		t.Fatalf("transaction commit/rollback/exec = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestApplyModelQualityEnforcementFailsBeforeMutationOnEntropyOrGenerationExhaustion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		prior     pgx.Row
		generator modelQualityEnforcementIDGenerator
		want      string
	}{
		{
			name: "entropy failure", prior: modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			generator: func() (string, error) { return "", errors.New("entropy unavailable") }, want: "entropy unavailable",
		},
		{
			name:      "generation exhausted",
			prior:     modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_old", math.MaxInt32, "quality_isolate", "old_run", 5, 7, "active", "quality_isolated", false, true, nil)},
			generator: func() (string, error) { return "unexpected", nil }, want: "generation is exhausted",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
				modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
				test.prior,
				modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			}}
			_, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), modelQualityEnforcementInput(now, "quality_isolate"), test.generator)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if len(tx.execCalls) != 0 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("exec/commit/rollback = %d/%d/%d", len(tx.execCalls), tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestTruncateModelQualityEnforcementMessagePreservesUTF8Boundary(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("质", 1001)
	got := truncateModelQualityEnforcementMessage(value)
	if len([]rune(got)) != 1000 || !strings.HasSuffix(got, "质") {
		t.Fatalf("truncated rune length/suffix = %d/%q", len([]rune(got)), got[len(got)-3:])
	}
}

func TestValidateModelQualityEnforcementRejectsPostgresNUL(t *testing.T) {
	t.Parallel()
	input := modelQualityEnforcementInput(time.Now(), "quality_isolate")
	input.Message = "quality\x00failure"
	if err := validateModelQualityEnforcementApplyInput(input); err == nil {
		t.Fatal("validateModelQualityEnforcementApplyInput() accepted PostgreSQL NUL")
	}
}

func modelQualityEnforcementInput(now time.Time, action string) port.ModelQualityEnforcementApplyInput {
	return port.ModelQualityEnforcementApplyInput{
		SystemAccountID: "sys_admin", AccountID: "account_1", RunID: "run_1",
		Trigger: modelquality.TriggerScheduled, Action: modelquality.Action(action),
		ExpectedPolicyRevision: 5, ExpectedAccountConfigRevision: 7,
		RecoveryInterval: 10 * time.Minute, Message: strings.Repeat("质量不达标", 300), DecidedAt: now,
	}
}

func modelQualityEnforcementAccountValues(status string, revision int64, fallback, super bool) []any {
	return []any{"sys_admin", status, revision, fallback, super, true, true}
}

func modelQualityEnforcementPolicyValues(now time.Time, revision int64, action string, manual int64) []any {
	return []any{
		"sys_admin", revision, "quick", manual, int64(70), action, int64(10),
		modelQualityPolicyTimeText(now.Add(-time.Hour)), modelQualityPolicyTimeText(now),
	}
}

func modelQualityEnforcementRecordValues(
	now time.Time,
	id string,
	generation int,
	action, runID string,
	policyRevision, accountRevision int64,
	before, after string,
	fallback, super bool,
	recoveryDue *time.Time,
) []any {
	var due pgtype.Text
	if recoveryDue != nil {
		due = pgtype.Text{String: modelQualityPolicyTimeText(*recoveryDue), Valid: true}
	}
	return []any{
		"account_1", "sys_admin", id, int64(generation), "active", action, runID,
		policyRevision, accountRevision, before, after, int64(modelQualityPolicyBoolInt(fallback)), int64(modelQualityPolicyBoolInt(super)),
		due, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{},
		modelQualityPolicyTimeText(now), pgtype.Text{}, modelQualityPolicyTimeText(now),
	}
}

type modelQualityEnforcementRowStub struct {
	values []any
	err    error
}

func (s modelQualityEnforcementRowStub) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != len(s.values) {
		return errors.New("unexpected enforcement scan length")
	}
	for index, value := range s.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *bool:
			*target = value.(bool)
		case *pgtype.Text:
			*target = value.(pgtype.Text)
		default:
			return errors.New("unexpected enforcement scan destination")
		}
	}
	return nil
}

var _ pgx.Row = modelQualityEnforcementRowStub{}
