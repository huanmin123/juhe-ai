package port

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modelquality"
)

func TestModelQualityPortFencesRemainDistinct(t *testing.T) {
	policyRevision := modelquality.PolicyRevision(7)
	scheduleRevision := modelquality.ScheduleRevision(8)
	accountRevision := modelquality.AccountRevision(9)
	generation := modelquality.EnforcementGeneration(10)

	scheduleCompletion := ModelQualityScheduleCompleteInput{
		ScheduleID:       "schedule-1",
		ExpectedRevision: scheduleRevision,
		Lease:            ModelQualityScheduleLease{OwnerID: "scheduler-a", ClaimToken: "schedule-claim-1", Until: time.Now().UTC().Add(time.Minute)},
		Status:           ModelQualityScheduleRunCompleted,
		Interval:         ModelQualityMinimumInterval,
	}
	if scheduleCompletion.ExpectedRevision != scheduleRevision || scheduleCompletion.Lease.ClaimToken == "" {
		t.Fatal("schedule completion must retain its schedule revision and lease token")
	}

	recoveryCompletion := ModelQualityRecoveryCompleteInput{
		AccountID:                     "account-1",
		ExpectedEnforcement:           modelquality.EnforcementToken{ID: "enforcement-1", Generation: generation},
		ExpectedPolicyRevision:        policyRevision,
		ExpectedAccountConfigRevision: accountRevision,
		Lease:                         ModelQualityRecoveryLease{OwnerID: "recovery-a", ClaimToken: "recovery-claim-1", Until: time.Now().UTC().Add(time.Minute)},
		RecoveryInterval:              ModelQualityMinimumInterval,
	}
	if recoveryCompletion.ExpectedPolicyRevision != policyRevision || recoveryCompletion.ExpectedAccountConfigRevision != accountRevision || recoveryCompletion.ExpectedEnforcement.Generation != generation {
		t.Fatal("recovery completion must carry policy, account, and generation fences")
	}
	if string(recoveryCompletion.Lease.ClaimToken) == string(scheduleCompletion.Lease.ClaimToken) {
		t.Fatal("recovery and schedule leases must have independent tokens")
	}

	enforcement := ModelQualityEnforcementRecord{
		FallbackWasEnabled:      true,
		SuperPriorityWasEnabled: false,
	}
	if !enforcement.FallbackWasEnabled || enforcement.SuperPriorityWasEnabled {
		t.Fatal("enforcement must retain fallback and super-priority facts independently")
	}
}

func TestModelQualityPortClaimBoundsMatchCurrentDurableSemantics(t *testing.T) {
	if ModelQualityScheduleClaimDefaultLimit != 3 || ModelQualityScheduleClaimMaximumLimit != 20 {
		t.Fatalf("schedule claim limits = %d/%d", ModelQualityScheduleClaimDefaultLimit, ModelQualityScheduleClaimMaximumLimit)
	}
	if ModelQualityRecoveryClaimDefaultLimit != 2 || ModelQualityRecoveryClaimMaximumLimit != 10 {
		t.Fatalf("recovery claim limits = %d/%d", ModelQualityRecoveryClaimDefaultLimit, ModelQualityRecoveryClaimMaximumLimit)
	}
	if ModelQualityScheduleClaimDefaultLease != 5*time.Minute || ModelQualityRecoveryClaimDefaultLease != 6*time.Minute || ModelQualityClaimMaximumLease != 30*time.Minute {
		t.Fatalf("claim lease bounds = %s/%s/%s", ModelQualityScheduleClaimDefaultLease, ModelQualityRecoveryClaimDefaultLease, ModelQualityClaimMaximumLease)
	}
	if ModelQualityMinimumInterval != 10*time.Minute || ModelQualityMaximumInterval != 7*24*time.Hour {
		t.Fatalf("model quality interval bounds = %s/%s", ModelQualityMinimumInterval, ModelQualityMaximumInterval)
	}
}

var (
	_ ModelQualityPolicyReader       = modelQualityPortProbe{}
	_ ModelQualityPolicyWriter       = modelQualityPortProbe{}
	_ ModelQualityScheduleWriter     = modelQualityPortProbe{}
	_ ModelQualityScheduleClaimer    = modelQualityPortProbe{}
	_ ModelQualityScheduleCompleter  = modelQualityPortProbe{}
	_ ModelQualityEnforcementApplier = modelQualityPortProbe{}
	_ ModelQualityRecoveryClaimer    = modelQualityPortProbe{}
	_ ModelQualityRecoveryCompleter  = modelQualityPortProbe{}
)

// modelQualityPortProbe locks the narrow method contracts at compile time. It
// intentionally does not implement an aggregate "Store" interface: future
// policy routes, schedulers and workers should depend only on their own port.
type modelQualityPortProbe struct{}

func (modelQualityPortProbe) ReadModelQualityPolicy(context.Context, string) (ModelQualityPolicyRecord, error) {
	return ModelQualityPolicyRecord{}, nil
}

func (modelQualityPortProbe) SaveModelQualityPolicy(context.Context, ModelQualityPolicySaveInput) (ModelQualityPolicySaveResult, error) {
	return ModelQualityPolicySaveResult{}, nil
}

func (modelQualityPortProbe) UpsertModelQualitySchedule(context.Context, ModelQualityScheduleUpsertInput) (ModelQualityScheduleWriteResult, error) {
	return ModelQualityScheduleWriteResult{}, nil
}

func (modelQualityPortProbe) DeleteModelQualitySchedule(context.Context, ModelQualityScheduleDeleteInput) (ModelQualityScheduleWriteResult, error) {
	return ModelQualityScheduleWriteResult{}, nil
}

func (modelQualityPortProbe) ClaimDueModelQualitySchedules(context.Context, ModelQualityScheduleClaimInput) ([]ModelQualityScheduleClaim, error) {
	return nil, nil
}

func (modelQualityPortProbe) CompleteModelQualitySchedule(context.Context, ModelQualityScheduleCompleteInput) (bool, error) {
	return false, nil
}

func (modelQualityPortProbe) ApplyModelQualityEnforcement(context.Context, ModelQualityEnforcementApplyInput) (ModelQualityEnforcementApplyResult, error) {
	return ModelQualityEnforcementApplyResult{}, nil
}

func (modelQualityPortProbe) ClaimDueModelQualityRecoveries(context.Context, ModelQualityRecoveryClaimInput) ([]ModelQualityRecoveryClaim, error) {
	return nil, nil
}

func (modelQualityPortProbe) CompleteModelQualityRecovery(context.Context, ModelQualityRecoveryCompleteInput) (ModelQualityRecoveryCompleteResult, error) {
	return ModelQualityRecoveryCompleteResult{}, nil
}
