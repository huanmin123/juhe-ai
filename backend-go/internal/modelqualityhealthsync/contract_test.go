package modelqualityhealthsync

import (
	"reflect"
	"testing"
	"time"
)

func TestEvaluateEligibilityMatchesRetryPredicate(t *testing.T) {
	t.Parallel()
	now := testTime(10)
	tests := []struct {
		name string
		run  Run
		want Eligibility
	}{
		{
			name: "completed failed account health sync is eligible",
			run:  testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, now),
			want: Eligibility{Eligible: true, Reason: EligibilityEligible},
		},
		{
			name: "running failed account health sync is not eligible",
			run:  testRun("run-1", "account-1", RunStatusRunning, HealthSyncStatusFailed, now),
			want: Eligibility{Reason: EligibilityRunNotCompleted},
		},
		{
			name: "completed run without account is not eligible",
			run:  testRun("run-1", "", RunStatusCompleted, HealthSyncStatusFailed, now),
			want: Eligibility{Reason: EligibilityAccountMissing},
		},
		{
			name: "applied result is not eligible",
			run:  testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusApplied, now),
			want: Eligibility{Reason: EligibilityHealthSyncNotFail},
		},
		{
			name: "unknown status fails closed",
			run:  testRun("run-1", "account-1", RunStatus("unknown"), HealthSyncStatusFailed, now),
			want: Eligibility{Reason: EligibilityInvalidInput},
		},
		{
			name: "blank run ID fails closed",
			run:  testRun(" ", "account-1", RunStatusCompleted, HealthSyncStatusFailed, now),
			want: Eligibility{Reason: EligibilityInvalidInput},
		},
		{
			name: "zero time fails closed",
			run:  testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, time.Time{}),
			want: Eligibility{Reason: EligibilityInvalidInput},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EvaluateEligibility(test.run); got != test.want {
				t.Fatalf("EvaluateEligibility(%+v) = %+v, want %+v", test.run, got, test.want)
			}
		})
	}
}

func TestPlanRetryBatchStableOrderCursorAndCap(t *testing.T) {
	t.Parallel()
	base := testTime(0)
	input := RetryBatchInput{
		Cap: 2,
		Runs: []Run{
			testRun("run-c", "account-1", RunStatusCompleted, HealthSyncStatusFailed, base.Add(2*time.Minute)),
			testRun("run-b", "account-1", RunStatusCompleted, HealthSyncStatusFailed, base.Add(time.Minute)),
			testRun("run-a", "account-1", RunStatusCompleted, HealthSyncStatusFailed, base.Add(time.Minute)),
			testRun("skip-status", "account-1", RunStatusFailed, HealthSyncStatusFailed, base),
			testRun("skip-account", "", RunStatusCompleted, HealthSyncStatusFailed, base),
		},
	}

	first, err := PlanRetryBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runIDs(first.Runs), []string{"run-a", "run-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first IDs = %v, want %v", got, want)
	}
	if !first.HasMore || first.Next == nil || first.Next.ID != "run-b" || !first.Next.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("first page = %#v", first)
	}

	second, err := PlanRetryBatch(RetryBatchInput{Cap: 2, After: first.Next, Runs: input.Runs})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runIDs(second.Runs), []string{"run-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second IDs = %v, want %v", got, want)
	}
	if second.HasMore || second.Next == nil || second.Next.ID != "run-c" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestPlanRetryBatchNormalizesTimeToUTC(t *testing.T) {
	t.Parallel()
	local := time.Date(2026, 7, 26, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	batch, err := PlanRetryBatch(RetryBatchInput{
		Cap:  DefaultBatchCap,
		Runs: []Run{testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, local)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Next == nil || batch.Next.UpdatedAt.Location() != time.UTC || !batch.Next.UpdatedAt.Equal(local.UTC()) {
		t.Fatalf("UTC cursor = %#v, want %s", batch.Next, local.UTC())
	}
}

func TestPlanRetryBatchRejectsMalformedRowsAndCursors(t *testing.T) {
	t.Parallel()
	valid := testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, testTime(1))
	tests := []struct {
		name  string
		input RetryBatchInput
	}{
		{
			name:  "zero cap",
			input: RetryBatchInput{Cap: 0, Runs: []Run{valid}},
		},
		{
			name:  "cap over upper bound",
			input: RetryBatchInput{Cap: MaxBatchCap + 1, Runs: []Run{valid}},
		},
		{
			name:  "unknown health sync status",
			input: RetryBatchInput{Cap: 1, Runs: []Run{testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatus("other"), testTime(1))}},
		},
		{
			name:  "malformed account ID",
			input: RetryBatchInput{Cap: 1, Runs: []Run{testRun("run-1", " account-1", RunStatusCompleted, HealthSyncStatusFailed, testTime(1))}},
		},
		{
			name:  "zero updated time",
			input: RetryBatchInput{Cap: 1, Runs: []Run{testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, time.Time{})}},
		},
		{
			name:  "duplicate durable run ID",
			input: RetryBatchInput{Cap: 1, Runs: []Run{valid, valid}},
		},
		{
			name: "invalid cursor ID",
			input: RetryBatchInput{
				Cap:   1,
				After: &Cursor{UpdatedAt: testTime(1), ID: " bad"},
				Runs:  []Run{valid},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PlanRetryBatch(test.input); err == nil {
				t.Fatalf("PlanRetryBatch(%#v) accepted malformed input", test.input)
			}
		})
	}
}

func TestPlanTransitionRequiresStatisticsAndDecisionUpdate(t *testing.T) {
	t.Parallel()
	run := testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusFailed, testTime(1))
	tests := []struct {
		name    string
		outcome AttemptOutcome
		want    Transition
		wantErr bool
	}{
		{
			name:    "both durable writes apply",
			outcome: AttemptOutcome{StatisticsWritten: true, DecisionMarkedApplied: true},
			want:    Transition{Result: TransitionApplied, HealthSyncStatus: HealthSyncStatusApplied, StatisticsWasWritten: true},
		},
		{
			name:    "statistics write failure remains retryable",
			outcome: AttemptOutcome{},
			want:    Transition{Result: TransitionRemainsRetryable, HealthSyncStatus: HealthSyncStatusFailed, Retryable: true},
		},
		{
			name:    "decision update failure remains retryable after statistics write",
			outcome: AttemptOutcome{StatisticsWritten: true},
			want:    Transition{Result: TransitionRemainsRetryable, HealthSyncStatus: HealthSyncStatusFailed, Retryable: true, StatisticsWasWritten: true},
		},
		{
			name:    "applied marker without statistics is rejected",
			outcome: AttemptOutcome{DecisionMarkedApplied: true},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := PlanTransition(run, test.outcome)
			if test.wantErr {
				if err == nil {
					t.Fatalf("PlanTransition(%+v) accepted invalid outcome", test.outcome)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("PlanTransition(%+v) = %#v, want %#v", test.outcome, got, test.want)
			}
		})
	}
}

func TestPlanTransitionRejectsNonRetryEligibleRun(t *testing.T) {
	t.Parallel()
	run := testRun("run-1", "account-1", RunStatusCompleted, HealthSyncStatusApplied, testTime(1))
	if _, err := PlanTransition(run, AttemptOutcome{StatisticsWritten: true, DecisionMarkedApplied: true}); err == nil {
		t.Fatal("PlanTransition accepted an already applied run")
	}
}

func testRun(id, accountID string, status RunStatus, syncStatus HealthSyncStatus, updatedAt time.Time) Run {
	return Run{ID: id, AccountID: accountID, Status: status, HealthSyncStatus: syncStatus, UpdatedAt: updatedAt}
}

func testTime(minute int) time.Time {
	return time.Date(2026, 7, 26, 0, minute, 0, 0, time.UTC)
}

func runIDs(runs []Run) []string {
	ids := make([]string, len(runs))
	for index, run := range runs {
		ids[index] = run.ID
	}
	return ids
}
