package modelquality

import (
	"math"
	"testing"
)

func TestDecideQualityMatchesCompletedEvidenceRules(t *testing.T) {
	t.Parallel()
	policy := testPolicy()
	tests := []struct {
		name        string
		facts       RuntimeFacts
		triggered   bool
		hard        bool
		unavailable bool
		reason      string
	}{
		{"score below threshold", RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelLikely, Score: 79}, true, false, false, "score_below_threshold"},
		{"suspicious hard failure", RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelSuspicious, Score: 100}, true, true, false, "hard_quality_conflict"},
		{"mapping hard failure", RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelLikely, Score: 100, MappingStatus: MappingStatusUndeclaredMismatch}, true, true, false, "hard_quality_conflict"},
		{"protocol hard failure", RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelLikely, Score: 100, ProtocolStatus: ProtocolStatusFailed}, true, true, false, "hard_quality_conflict"},
		{"unavailable evidence", RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelUnavailable, Score: 0}, false, false, true, "quality_evidence_unavailable"},
		{"incomplete never triggers", RuntimeFacts{RunStatus: RunStatusFailed, Level: LevelSuspicious, Score: 0}, false, true, false, "hard_quality_conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := DecideQuality(policy, test.facts)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Triggered != test.triggered || decision.HardFailure != test.hard || decision.EvidenceUnavailable != test.unavailable {
				t.Fatalf("decision = %#v", decision)
			}
			if !contains(decision.ReasonCodes, test.reason) {
				t.Fatalf("reasons = %v, missing %q", decision.ReasonCodes, test.reason)
			}
		})
	}
}

func TestValidationRejectsInvalidPolicyAndRuntimeFacts(t *testing.T) {
	t.Parallel()
	if got := DefaultPolicy("system-1").PenaltyThreshold; got != 70 {
		t.Fatalf("DefaultPolicy() threshold = %d, want 70", got)
	}
	policy := testPolicy()
	policy.PenaltyThreshold = 39
	if err := policy.Validate(); err == nil {
		t.Fatal("Validate() accepted threshold below Node range")
	}
	if _, err := DecideQuality(testPolicy(), RuntimeFacts{RunStatus: RunStatusCompleted, Level: LevelLikely, Score: math.NaN()}); err == nil {
		t.Fatal("DecideQuality() accepted NaN score")
	}
	if _, err := Snapshot(testPolicy(), 1, "", 1); err == nil {
		t.Fatal("Snapshot() accepted schedule revision without schedule ID")
	}
}

func TestPenaltySourceStatusMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status AccountStatus
		action Action
		want   bool
	}{
		{AccountStatusActive, ActionFallback, true},
		{AccountStatusQualityIsolated, ActionFallback, false},
		{AccountStatusActive, ActionDisable, true},
		{AccountStatusQualityIsolated, ActionDisable, true},
		{AccountStatusDisabled, ActionDisable, false},
		{AccountStatusActive, ActionQualityIsolate, true},
		{AccountStatusQualityIsolated, ActionQualityIsolate, false},
	}
	for _, test := range cases {
		if got := AllowedPenaltySourceStatus(test.status, test.action); got != test.want {
			t.Fatalf("AllowedPenaltySourceStatus(%q, %q) = %v, want %v", test.status, test.action, got, test.want)
		}
	}
}

func TestPlanEnforcementAppliesManualAndRevisionGates(t *testing.T) {
	t.Parallel()
	policy := testPolicy()
	account := testAccount()
	request := testEnforcementRequest(TriggerManual, ActionFallback, policy, account)
	plan, err := PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementApply || !plan.SetFallbackEnabled || !plan.ClearSuperPrioritySet {
		t.Fatalf("fallback plan = %#v, err = %v", plan, err)
	}
	policy.ManualEnforcementEnabled = false
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementSkipped {
		t.Fatalf("disabled manual gate = %#v, err = %v", plan, err)
	}
	policy = testPolicy()
	account.OwnPhysical = false
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementSkipped {
		t.Fatalf("non-own manual gate = %#v, err = %v", plan, err)
	}
	account = testAccount()
	request.AccountRevision++
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementStale {
		t.Fatalf("stale account revision = %#v, err = %v", plan, err)
	}
	request.AccountRevision = account.ConfigRevision
	account.SystemAccountID = "other-system"
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementSkipped {
		t.Fatalf("cross-scope enforcement = %#v, err = %v", plan, err)
	}
	request.AccountRevision = account.ConfigRevision
	account.SystemAccountID = "other-system"
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementSkipped {
		t.Fatalf("cross-system account = %#v, err = %v", plan, err)
	}
}

func TestPlanEnforcementUsesCurrentPolicyActionAndIdempotence(t *testing.T) {
	t.Parallel()
	policy := testPolicy()
	account := testAccount()
	request := testEnforcementRequest(TriggerScheduled, ActionDisable, policy, account)
	plan, err := PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementStale {
		t.Fatalf("action mismatch = %#v, err = %v", plan, err)
	}
	request.Action = ActionFallback
	account.FallbackEnabled = true
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementAlreadyEffective {
		t.Fatalf("already fallback = %#v, err = %v", plan, err)
	}
	request.PenaltyThreshold++
	plan, err = PlanEnforcement(request, policy, account)
	if err != nil || plan.Result != EnforcementStale {
		t.Fatalf("threshold snapshot mismatch = %#v, err = %v", plan, err)
	}
}

func TestPlanEnforcementDisablesSchedulingForDisableAndQualityIsolation(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{ActionDisable, ActionQualityIsolate} {
		t.Run(string(action), func(t *testing.T) {
			policy := testPolicy()
			policy.PenaltyAction = action
			account := testAccount()
			request := testEnforcementRequest(TriggerScheduled, action, policy, account)

			plan, err := PlanEnforcement(request, policy, account)
			if err != nil || plan.Result != EnforcementApply || plan.TargetSchedulable == nil || *plan.TargetSchedulable {
				t.Fatalf("%s plan = %#v, err = %v", action, plan, err)
			}
		})
	}
}

func TestPlanEnforcementRejectsQualityRecoveryTrigger(t *testing.T) {
	t.Parallel()
	policy := testPolicy()
	account := testAccount()
	request := testEnforcementRequest(TriggerQualityRecovery, ActionFallback, policy, account)
	if _, err := PlanEnforcement(request, policy, account); err == nil {
		t.Fatal("PlanEnforcement() accepted quality recovery trigger")
	}
}

func TestTypedFencesAndGeneration(t *testing.T) {
	t.Parallel()
	if !(PolicyFence{Expected: 3, Current: 3}).Matches() || (AccountFence{Expected: 3, Current: 4}).Matches() {
		t.Fatal("revision fences do not compare their own typed revisions")
	}
	if !(ScheduleFence{ScheduleID: "schedule-1", Expected: 2, Current: 2}).Matches() {
		t.Fatal("schedule fence did not match")
	}
	token := EnforcementToken{ID: "enforcement-1", Generation: 2}
	if !(EnforcementFence{Expected: token, Current: token}).Matches() {
		t.Fatal("enforcement fence did not match")
	}
	if _, err := NextGeneration(EnforcementGeneration(math.MaxUint64)); err == nil {
		t.Fatal("NextGeneration() allowed generation wraparound")
	}
}

func TestPlanRecoveryFencesAndRestoresAvailability(t *testing.T) {
	t.Parallel()
	account := testAccount()
	account.Status = AccountStatusQualityIsolated
	token := EnforcementToken{ID: "enforcement-1", Generation: 2}
	request := RecoveryRequest{PolicyRevision: 5, AccountRevision: account.ConfigRevision, Enforcement: token, Passed: true}
	current := EnforcementState{SystemAccountID: account.SystemAccountID, Token: token, AccountRevision: account.ConfigRevision, Active: true, Action: ActionQualityIsolate, PolicyRevision: 5}
	plan, err := PlanRecovery(request, current, account, true)
	if err != nil || plan.Result != RecoveryRecovered || plan.TargetStatus != AccountStatusActive || plan.TargetSchedulable == nil || !*plan.TargetSchedulable {
		t.Fatalf("available recovery = %#v, err = %v", plan, err)
	}
	plan, err = PlanRecovery(request, current, account, false)
	if err != nil || plan.Result != RecoveryRecovered || plan.TargetStatus != AccountStatusDisabled || plan.TargetSchedulable == nil || *plan.TargetSchedulable {
		t.Fatalf("unavailable recovery = %#v, err = %v", plan, err)
	}
	current.PolicyRevision = 6
	plan, err = PlanRecovery(request, current, account, true)
	if err != nil || plan.Result != RecoveryStale || !plan.NeedsReschedule {
		t.Fatalf("stale policy recovery = %#v, err = %v", plan, err)
	}
	request.Passed = false
	current.PolicyRevision = 5
	plan, err = PlanRecovery(request, current, account, true)
	if err != nil || plan.Result != RecoveryKeptIsolated || !plan.NeedsReschedule {
		t.Fatalf("failed recovery = %#v, err = %v", plan, err)
	}
	current.Token.Generation++
	plan, err = PlanRecovery(request, current, account, true)
	if err != nil || plan.Result != RecoveryStale {
		t.Fatalf("stale generation recovery = %#v, err = %v", plan, err)
	}
	current.Token = token
	current.SystemAccountID = "other-system"
	plan, err = PlanRecovery(request, current, account, true)
	if err != nil || plan.Result != RecoveryStale {
		t.Fatalf("cross-scope recovery = %#v, err = %v", plan, err)
	}
}

func TestPlanRecoveryReschedulesWhenAnyAccountRevisionFenceDrifts(t *testing.T) {
	t.Parallel()
	account := testAccount()
	account.Status = AccountStatusQualityIsolated
	token := EnforcementToken{ID: "enforcement-1", Generation: 2}
	baseRequest := RecoveryRequest{PolicyRevision: 5, AccountRevision: account.ConfigRevision, Enforcement: token, Passed: true}
	baseCurrent := EnforcementState{
		SystemAccountID: account.SystemAccountID,
		Token:           token,
		AccountRevision: account.ConfigRevision,
		Active:          true,
		Action:          ActionQualityIsolate,
		PolicyRevision:  5,
	}

	tests := []struct {
		name    string
		request RecoveryRequest
		current EnforcementState
		account Account
	}{
		{
			name: "claimed request revision changed",
			request: RecoveryRequest{
				PolicyRevision:  baseRequest.PolicyRevision,
				AccountRevision: baseRequest.AccountRevision + 1,
				Enforcement:     baseRequest.Enforcement,
				// Revision fencing also wins over a failed check: an obsolete
				// result must be rescheduled rather than being recorded as the
				// current generation's failed recovery.
				Passed: false,
			},
			current: baseCurrent,
			account: account,
		},
		{
			name:    "enforcement captured revision changed",
			request: baseRequest,
			current: EnforcementState{
				SystemAccountID: baseCurrent.SystemAccountID,
				Token:           baseCurrent.Token,
				AccountRevision: baseCurrent.AccountRevision + 1,
				Active:          baseCurrent.Active,
				Action:          baseCurrent.Action,
				PolicyRevision:  baseCurrent.PolicyRevision,
			},
			account: account,
		},
		{
			name:    "current account revision changed",
			request: baseRequest,
			current: baseCurrent,
			account: Account{
				ID:               account.ID,
				SystemAccountID:  account.SystemAccountID,
				Status:           account.Status,
				ConfigRevision:   account.ConfigRevision + 1,
				OwnPhysical:      account.OwnPhysical,
				FallbackEnabled:  account.FallbackEnabled,
				SuperPrioritySet: account.SuperPrioritySet,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanRecovery(test.request, test.current, test.account, true)
			if err != nil || plan.Result != RecoveryStale || plan.TargetStatus != AccountStatusQualityIsolated || !plan.NeedsReschedule {
				t.Fatalf("revision drift plan = %#v, err = %v", plan, err)
			}
		})
	}
}

func TestClaimRecoveryRefreshesRevisionBeforeCheckAndFencesLaterDrift(t *testing.T) {
	t.Parallel()
	account := testAccount()
	account.Status = AccountStatusQualityIsolated
	// The isolation write previously captured revision 7. An operator edit
	// before the due recovery is legitimate: Node refreshes this snapshot when
	// it claims the recovery, so the ensuing check may use revision 8.
	account.ConfigRevision++
	token := EnforcementToken{ID: "enforcement-1", Generation: 2}
	current := EnforcementState{
		SystemAccountID: account.SystemAccountID,
		Token:           token,
		AccountRevision: account.ConfigRevision - 1,
		Active:          true,
		Action:          ActionQualityIsolate,
		PolicyRevision:  5,
	}
	claim, err := ClaimRecovery(RecoveryClaimRequest{PolicyRevision: 5, Enforcement: token}, current, account)
	if err != nil || claim.Result != RecoveryClaimed || claim.State.AccountRevision != account.ConfigRevision {
		t.Fatalf("claim = %#v, err = %v", claim, err)
	}

	request := RecoveryRequest{PolicyRevision: 5, AccountRevision: claim.State.AccountRevision, Enforcement: token, Passed: true}
	plan, err := PlanRecovery(request, claim.State, account, true)
	if err != nil || plan.Result != RecoveryRecovered {
		t.Fatalf("recovery after refreshed claim = %#v, err = %v", plan, err)
	}

	// A post-claim edit invalidates this check. The scheduler must obtain a new
	// claim instead of clearing the current isolation generation.
	account.ConfigRevision++
	plan, err = PlanRecovery(request, claim.State, account, true)
	if err != nil || plan.Result != RecoveryStale || !plan.NeedsReschedule {
		t.Fatalf("post-claim drift plan = %#v, err = %v", plan, err)
	}
}

func TestClaimRecoveryRejectsStaleAndInvalidFences(t *testing.T) {
	t.Parallel()
	account := testAccount()
	account.Status = AccountStatusQualityIsolated
	token := EnforcementToken{ID: "enforcement-1", Generation: 2}
	current := EnforcementState{
		SystemAccountID: account.SystemAccountID,
		Token:           token,
		AccountRevision: account.ConfigRevision,
		Active:          true,
		Action:          ActionQualityIsolate,
		PolicyRevision:  5,
	}

	for _, test := range []struct {
		name    string
		request RecoveryClaimRequest
		policy  PolicyRevision
	}{
		{name: "old policy fence", request: RecoveryClaimRequest{PolicyRevision: 4, Enforcement: token}, policy: 5},
		{name: "old generation fence", request: RecoveryClaimRequest{PolicyRevision: 5, Enforcement: EnforcementToken{ID: token.ID, Generation: token.Generation + 1}}, policy: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := current
			state.PolicyRevision = test.policy
			claim, err := ClaimRecovery(test.request, state, account)
			if err != nil || claim.Result != RecoveryClaimStale {
				t.Fatalf("claim = %#v, err = %v", claim, err)
			}
		})
	}

	invalidState := current
	invalidState.AccountRevision = 0
	if _, err := ClaimRecovery(RecoveryClaimRequest{PolicyRevision: 5, Enforcement: token}, invalidState, account); err == nil {
		t.Fatal("ClaimRecovery() accepted zero existing claim revision")
	}
	zeroAccount := account
	zeroAccount.ConfigRevision = 0
	if _, err := ClaimRecovery(RecoveryClaimRequest{PolicyRevision: 5, Enforcement: token}, current, zeroAccount); err == nil {
		t.Fatal("ClaimRecovery() accepted zero current account revision")
	}
	if _, err := ClaimRecovery(RecoveryClaimRequest{PolicyRevision: 5, Enforcement: EnforcementToken{ID: token.ID}}, current, account); err == nil {
		t.Fatal("ClaimRecovery() accepted zero enforcement generation")
	}
}

func testPolicy() Policy {
	return Policy{SystemAccountID: "system-1", Revision: 5, Profile: ProfileQuick, ManualEnforcementEnabled: true, PenaltyThreshold: 80, PenaltyAction: ActionFallback, RecoveryIntervalMinutes: 10}
}

func testAccount() Account {
	return Account{ID: "account-1", SystemAccountID: "system-1", Status: AccountStatusActive, ConfigRevision: 7, OwnPhysical: true, SuperPrioritySet: true}
}

func testEnforcementRequest(trigger Trigger, action Action, policy Policy, account Account) EnforcementRequest {
	return EnforcementRequest{
		Trigger: trigger, RunID: "run-1", Action: action,
		Profile: policy.Profile, PenaltyThreshold: policy.PenaltyThreshold,
		RecoveryIntervalMinutes: policy.RecoveryIntervalMinutes,
		PolicyRevision:          policy.Revision, AccountRevision: account.ConfigRevision,
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
