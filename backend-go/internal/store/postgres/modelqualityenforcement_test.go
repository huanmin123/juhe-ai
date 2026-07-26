package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
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
		strings.Contains(lockModelQualityEnforcementAccountSQL, "FROM juhe_business.system_accounts") ||
		!strings.Contains(lockManualModelQualityEnforcementAccountSQL, "FROM juhe_business.system_accounts") ||
		!strings.Contains(lockManualModelQualityEnforcementAccountSQL, "FOR UPDATE OF accounts") ||
		!strings.Contains(lockModelQualityEnforcementSQL, "FOR UPDATE") ||
		!strings.Contains(lockModelQualityEnforcementRunSQL, "FOR SHARE OF runs") ||
		!strings.Contains(lockModelQualityEnforcementRunSQL, "runs.target_type") ||
		!strings.Contains(lockModelQualityEnforcementRunSQL, "OCTET_LENGTH(runs.policy_snapshot_json)") ||
		!strings.Contains(lockModelQualityEnforcementPolicySQL, "FOR SHARE") {
		t.Fatal("enforcement apply must lock owner/account, enforcement, durable run, and manual policy")
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
			"model_quality_policies", "model_quality_schedules", "config_source", "recovery_model",
			"accounts.config_revision", "RETURNING",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("enforcement write SQL missing %q:\n%s", fragment, sql)
			}
		}
	}
	for _, fragment := range []string{
		"aqe.enforcement_id = $23", "aqe.generation = $24",
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
	input := modelQualityEnforcementInput(now, "quality_isolate")
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_1", 1, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, &due)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
		return "enforcement_1", nil
	})
	if err != nil {
		t.Fatalf("applyModelQualityEnforcement() error = %v", err)
	}
	if result.Status != port.ModelQualityEnforcementApplied || result.Enforcement == nil ||
		result.Enforcement.Token.Generation != 1 || result.Enforcement.RecoveryDueAt == nil || !result.Enforcement.RecoveryDueAt.Equal(due) {
		t.Fatalf("result = %#v", result)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.execCalls) != 1 || len(tx.queryCalls) != 5 {
		t.Fatalf("transaction commit/rollback/exec/query = %d/%d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(tx.execCalls), len(tx.queryCalls))
	}
	if !reflect.DeepEqual(tx.queryCalls[0].args, []any{"account_1", "sys_admin"}) {
		t.Fatalf("owner/account lock args = %#v", tx.queryCalls[0].args)
	}
	if tx.execCalls[0].args[1] != "quality_isolate" || tx.execCalls[0].args[8] != int64(5) || tx.queryCalls[4].args[20] != int64(8) {
		t.Fatalf("account/write CAS args = %#v / %#v", tx.execCalls[0].args, tx.queryCalls[4].args)
	}
}

func TestApplyScheduledModelQualityEnforcementUsesScheduleSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	due := now.Add(30 * time.Minute)
	input := modelQualityEnforcementInput(now, "quality_isolate")
	input.Trigger = modelquality.TriggerScheduled
	input.ScheduleID = "mqs_1"
	input.Profile = modelquality.ProfileFull
	input.PenaltyThreshold = 80
	input.RecoveryInterval = 30 * time.Minute
	input.RecoveryModel = "gpt-5.6-sol"
	record := modelQualityEnforcementRecordValues(now, "enforcement_1", 1, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, &due)
	record[7] = "schedule"
	record[8] = pgtype.Text{String: "mqs_1", Valid: true}
	record[10] = "full"
	record[11] = int64(80)
	record[12] = int64(30)
	record[13] = pgtype.Text{String: "gpt-5.6-sol", Valid: true}
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
			modelQualityScheduleRowStub{values: modelQualityScheduleRowValues("mqs_1", 5, 60, 1, now.Add(time.Hour), pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, now.Add(-time.Hour), now)},
			modelQualityEnforcementRowStub{values: record},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
		return "enforcement_1", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementApplied || result.Enforcement == nil ||
		result.Enforcement.ConfigSource != port.ModelQualityConfigSourceSchedule || result.Enforcement.ConfigSourceID != "mqs_1" ||
		result.Enforcement.Profile != modelquality.ProfileFull || result.Enforcement.PenaltyThreshold != 80 ||
		result.Enforcement.RecoveryInterval != 30*time.Minute || result.Enforcement.RecoveryModel != "gpt-5.6-sol" {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if !strings.Contains(tx.queryCalls[2].query, "model_check_runs") || !strings.Contains(tx.queryCalls[3].query, "model_quality_schedules") || tx.queryCalls[4].args[6] != "schedule" || tx.queryCalls[4].args[7] != "mqs_1" {
		t.Fatalf("scheduled configuration/write calls = %#v", tx.queryCalls)
	}
	if strings.Contains(tx.queryCalls[0].query, "FROM juhe_business.system_accounts") ||
		!reflect.DeepEqual(tx.queryCalls[0].args, []any{"account_1", "sys_admin"}) {
		t.Fatalf("scheduled account lock unexpectedly uses tenant owner fence: %#v", tx.queryCalls[0])
	}
}

func TestApplyModelQualityEnforcementTreatsClearedRunAsConsumed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	cleared := now.Add(-time.Minute)
	priorValues := modelQualityEnforcementRecordValues(now.Add(-time.Hour), "enforcement_old", 3, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, nil)
	priorValues[4] = "cleared"
	priorValues[25] = pgtype.Text{String: modelQualityPolicyTimeText(cleared), Valid: true}
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
	input := modelQualityEnforcementInput(now, "fallback")
	tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
		modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, true, false)},
		modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
		modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
		modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "fallback", 1)},
		modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_1", 1, "fallback", "run_1", 5, 7, "active", "active", true, false, nil)},
	}}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
		return "enforcement_1", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementAlreadyEffective || result.Enforcement == nil {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if len(tx.execCalls) != 0 || tx.commitCalls != 1 || tx.queryCalls[4].args[20] != int64(7) {
		t.Fatalf("exec/commit/write args = %d/%d/%#v", len(tx.execCalls), tx.commitCalls, tx.queryCalls[4].args)
	}
}

func TestApplyModelQualityEnforcementReplacesPriorGenerationWithExactCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	due := now.Add(10 * time.Minute)
	input := modelQualityEnforcementInput(now, "quality_isolate")
	prior := modelQualityEnforcementRecordValues(now.Add(-time.Hour), "enforcement_old", 2, "fallback", "old_run", 4, 6, "active", "active", false, true, nil)
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{values: prior},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRecordValues(now, "enforcement_new", 3, "quality_isolate", "run_1", 5, 7, "active", "quality_isolated", false, true, &due)},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	result, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
		return "enforcement_new", nil
	})
	if err != nil || result.Status != port.ModelQualityEnforcementApplied || result.Enforcement == nil || result.Enforcement.Token.Generation != 3 {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	writeArgs := tx.queryCalls[4].args
	if len(writeArgs) != 24 || writeArgs[22] != "enforcement_old" || writeArgs[23] != int64(2) {
		t.Fatalf("replacement generation CAS args = %#v", writeArgs)
	}
}

func TestApplyModelQualityEnforcementRollsBackAccountWhenGenerationWriteLosesCAS(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	input := modelQualityEnforcementInput(now, "quality_isolate")
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{
			modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
			modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
		},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	_, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, func() (string, error) {
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
			input := modelQualityEnforcementInput(now, "quality_isolate")
			tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
				modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
				test.prior,
				modelQualityEnforcementRowStub{values: modelQualityEnforcementRunValues(input)},
				modelQualityEnforcementRowStub{values: modelQualityEnforcementPolicyValues(now, 5, "quality_isolate", 1)},
			}}
			_, err := applyModelQualityEnforcement(context.Background(), beginModelQualityRecoveryTestTx(tx), input, test.generator)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if len(tx.execCalls) != 0 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("exec/commit/rollback = %d/%d/%d", len(tx.execCalls), tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestApplyModelQualityEnforcementReturnsStaleForMissingOrMismatchedDurableRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	input := modelQualityEnforcementInput(now, "quality_isolate")
	mismatched := modelQualityEnforcementRunValues(input)
	mismatched[5] = "gpt-5.4"
	accountless := modelQualityEnforcementRunValues(input)
	accountless[1] = pgtype.Text{}
	targetTypeMismatch := modelQualityEnforcementRunValues(input)
	targetTypeMismatch[2] = "group"
	targetIDMismatch := modelQualityEnforcementRunValues(input)
	targetIDMismatch[3] = "account_other"
	targetOwnerMismatch := modelQualityEnforcementRunValues(input)
	targetOwnerMismatch[4] = pgtype.Text{String: "sys_other", Valid: true}
	manualDisabled := modelQualityEnforcementRunValues(input)
	disabledSnapshot := strings.Replace(manualDisabled[10].(pgtype.Text).String, `"manualEnforcementEnabled":true`, `"manualEnforcementEnabled":false`, 1)
	manualDisabled[10] = pgtype.Text{String: disabledSnapshot, Valid: true}
	manualDisabled[11] = int64(len(disabledSnapshot))
	for _, test := range []struct {
		name string
		run  pgx.Row
	}{
		{name: "missing", run: modelQualityEnforcementRowStub{err: pgx.ErrNoRows}},
		{name: "accountless diagnostic run", run: modelQualityEnforcementRowStub{values: accountless}},
		{name: "non-account target", run: modelQualityEnforcementRowStub{values: targetTypeMismatch}},
		{name: "target id mismatch", run: modelQualityEnforcementRowStub{values: targetIDMismatch}},
		{name: "target owner mismatch", run: modelQualityEnforcementRowStub{values: targetOwnerMismatch}},
		{name: "model mismatch", run: modelQualityEnforcementRowStub{values: mismatched}},
		{name: "manual enforcement disabled in snapshot", run: modelQualityEnforcementRowStub{values: manualDisabled}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
				modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
				modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
				test.run,
			}}
			result, err := applyModelQualityEnforcement(
				context.Background(), beginModelQualityRecoveryTestTx(tx), input,
				func() (string, error) { return "must_not_be_used", nil },
			)
			if err != nil || result.Status != port.ModelQualityEnforcementStale {
				t.Fatalf("result/error = %#v/%v", result, err)
			}
			if len(tx.queryCalls) != 3 || len(tx.execCalls) != 0 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
				t.Fatalf("query/exec/commit/rollback = %d/%d/%d/%d", len(tx.queryCalls), len(tx.execCalls), tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestApplyModelQualityEnforcementFailsClosedForCorruptDurableRunSnapshot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	input := modelQualityEnforcementInput(now, "quality_isolate")
	corrupt := modelQualityEnforcementRunValues(input)
	raw := `{"policyRevision":5,"policyRevision":5,"configSource":"manual","profile":"quick","manualEnforcementEnabled":true,"threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10,"accountConfigRevision":7}`
	corrupt[10] = pgtype.Text{String: raw, Valid: true}
	corrupt[11] = int64(len(raw))
	tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
		modelQualityEnforcementRowStub{values: modelQualityEnforcementAccountValues("active", 7, false, true)},
		modelQualityEnforcementRowStub{err: pgx.ErrNoRows},
		modelQualityEnforcementRowStub{values: corrupt},
	}}
	_, err := applyModelQualityEnforcement(
		context.Background(), beginModelQualityRecoveryTestTx(tx), input,
		func() (string, error) { return "must_not_be_used", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate top-level field") {
		t.Fatalf("error = %v", err)
	}
	if len(tx.queryCalls) != 3 || len(tx.execCalls) != 0 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("query/exec/commit/rollback = %d/%d/%d/%d", len(tx.queryCalls), len(tx.execCalls), tx.commitCalls, tx.rollbackCalls)
	}
}

func TestDecodeModelQualityEnforcementRunSnapshotRejectsNullRequiredScalars(t *testing.T) {
	t.Parallel()
	base := `{"policyRevision":5,"configSource":"manual","profile":"quick","manualEnforcementEnabled":true,"threshold":70,"action":"fallback","recoveryIntervalMinutes":10,"accountConfigRevision":7}`
	for _, raw := range []string{
		strings.Replace(base, `"policyRevision":5`, `"policyRevision":null`, 1),
		strings.Replace(base, `"manualEnforcementEnabled":true`, `"manualEnforcementEnabled":null`, 1),
	} {
		if _, err := decodeModelQualityEnforcementRunSnapshot(raw); err == nil {
			t.Fatalf("snapshot accepted required null scalar: %s", raw)
		}
	}
}

func TestTruncateModelQualityEnforcementMessagePreservesUTF8Boundary(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("质", 1001)
	got := truncateModelQualityTextRunes(value, 1000)
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

func TestValidateModelQualityEnforcementSourceAndSnapshotBounds(t *testing.T) {
	t.Parallel()
	base := modelQualityEnforcementInput(time.Now(), "quality_isolate")
	tests := []func(*port.ModelQualityEnforcementApplyInput){
		func(input *port.ModelQualityEnforcementApplyInput) { input.Profile = "" },
		func(input *port.ModelQualityEnforcementApplyInput) { input.PenaltyThreshold = 39 },
		func(input *port.ModelQualityEnforcementApplyInput) { input.RecoveryModel = "" },
		func(input *port.ModelQualityEnforcementApplyInput) {
			input.Trigger, input.ScheduleID = modelquality.TriggerScheduled, ""
		},
		func(input *port.ModelQualityEnforcementApplyInput) { input.ScheduleID = "mqs_1" },
	}
	for _, mutate := range tests {
		input := base
		mutate(&input)
		if err := validateModelQualityEnforcementApplyInput(input); err == nil {
			t.Fatalf("invalid enforcement snapshot accepted: %+v", input)
		}
	}
}

func modelQualityEnforcementInput(now time.Time, action string) port.ModelQualityEnforcementApplyInput {
	return port.ModelQualityEnforcementApplyInput{
		SystemAccountID: "sys_admin", AccountID: "account_1", RunID: "run_1",
		Trigger: modelquality.TriggerManual, Action: modelquality.Action(action),
		Profile: modelquality.ProfileQuick, PenaltyThreshold: 70, RecoveryModel: "gpt-5",
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

func modelQualityEnforcementRunValues(input port.ModelQualityEnforcementApplyInput) []any {
	configSource := modelQualityEnforcementConfigSource(input)
	snapshot := map[string]any{
		"policyRevision":           input.ExpectedPolicyRevision,
		"configSource":             configSource,
		"profile":                  input.Profile,
		"manualEnforcementEnabled": true,
		"threshold":                input.PenaltyThreshold,
		"action":                   input.Action,
		"recoveryIntervalMinutes":  int(input.RecoveryInterval / time.Minute),
		"accountConfigRevision":    input.ExpectedAccountConfigRevision,
	}
	var scheduleID pgtype.Text
	if input.ScheduleID != "" {
		snapshot["scheduleId"] = input.ScheduleID
		scheduleID = pgtype.Text{String: input.ScheduleID, Valid: true}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	return []any{
		input.SystemAccountID,
		pgtype.Text{String: input.AccountID, Valid: true},
		"account",
		input.AccountID,
		pgtype.Text{String: input.SystemAccountID, Valid: true},
		input.RecoveryModel,
		string(input.Profile),
		string(input.Trigger),
		scheduleID,
		string(modelquality.RunStatusCompleted),
		pgtype.Text{String: string(raw), Valid: true},
		int64(len(raw)),
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
		"manual", pgtype.Text{}, policyRevision, "quick", int64(70), int64(10), pgtype.Text{String: "gpt-5", Valid: true},
		accountRevision, before, after, int64(modelQualityPolicyBoolInt(fallback)), int64(modelQualityPolicyBoolInt(super)),
		due, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, modelQualityPolicyTimeText(now),
		pgtype.Text{}, modelQualityPolicyTimeText(now),
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
