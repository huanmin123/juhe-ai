package gatewaydispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// slotPlanAffinity 在 claim 结果之上实现会话名额端口，记录准备期传入的
// 候选顺序与预算，用于验证准备层接线。
type slotPlanAffinity struct {
	*claimOwnerAffinity
	plan           SessionAffinitySlotPlan
	planErr        error
	established    bool
	gotBudget      int64
	gotEstablished bool
	candidates     []SessionSlotCandidate
}

func (f *slotPlanAffinity) ClaimWithStateAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope AffinityScope) (string, bool, bool) {
	return f.owner, f.established, f.owner != ""
}

func (f *slotPlanAffinity) PlanSessionSlot(ctx context.Context, sessionAffinityKey, ownerAccountID string, ownerEstablished bool, candidates []SessionSlotCandidate, waitBudgetMs int64) (SessionAffinitySlotPlan, error) {
	f.gotBudget = waitBudgetMs
	f.gotEstablished = ownerEstablished
	f.candidates = append([]SessionSlotCandidate(nil), candidates...)
	return f.plan, f.planErr
}

func (f *slotPlanAffinity) ConfirmSessionSlot(ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, cap int) bool {
	return true
}

func TestPrepareDispatchAccountsAppliesSessionSlotPlan(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		established:        true,
		plan: SessionAffinitySlotPlan{
			PreferredAccountID: "a-2",
			Fallback:           true,
			WaitedMs:           600,
		},
	}
	engine.Affinity = affinity
	audit := &frozenAudit{sink: &fakeAuditSink{}}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-slot-prep"
	input.AuditCapture = audit

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.SessionAffinityOwnerAccountID != "a-1" {
		t.Fatalf("owner = %q, want a-1", result.SessionAffinityOwnerAccountID)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, want a-2 first", accountIDs(result.Accounts))
	}
	if !result.SessionAffinitySlotFallback || !result.SessionAffinityWaitPlanned {
		t.Fatalf("slot flags = fallback:%v waitPlanned:%v", result.SessionAffinitySlotFallback, result.SessionAffinityWaitPlanned)
	}
	if result.SessionAffinityWaitRemainingMs != 1400 {
		t.Fatalf("wait remaining = %d, want 1400", result.SessionAffinityWaitRemainingMs)
	}
	if affinity.gotBudget != engine.Config.SessionAffinityOwnerWaitBudgetMs {
		t.Fatalf("slot budget = %d, want %d", affinity.gotBudget, engine.Config.SessionAffinityOwnerWaitBudgetMs)
	}
	if !affinity.gotEstablished {
		t.Fatal("preparation must pass claim-established state into slot planning")
	}
	if len(affinity.candidates) != 2 || affinity.candidates[0].AccountID != "a-1" || affinity.candidates[1].AccountID != "a-2" {
		t.Fatalf("slot candidates = %+v", affinity.candidates)
	}
	found := false
	for _, label := range audit.metadata {
		if label == "session_affinity_slot" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("audit metadata = %v, want session_affinity_slot", audit.metadata)
	}
}

func TestPrepareDispatchAccountsDoesNotPromoteBusyOwnerViaSlotPlan(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		established:        true,
		plan:               SessionAffinitySlotPlan{PreferredAccountID: "a-1"},
	}
	engine.Affinity = affinity
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 4}}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-slot-busy-owner"

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, busy owner must stay behind", accountIDs(result.Accounts))
	}
	if result.SessionAffinityOwnerAccountID != "a-1" || !result.SessionAffinityOwnerBusy {
		t.Fatalf("owner settlement = %q busy=%v", result.SessionAffinityOwnerAccountID, result.SessionAffinityOwnerBusy)
	}
}

// softBusySlotPlanAffinity 模拟真实亲和排序在 owner 达到软阈值后把 peer
// 排到 owner 之前；名额规划仍可能偏好 owner，用于验证准备层不得把它提回。
type softBusySlotPlanAffinity struct {
	*slotPlanAffinity
}

func (f *softBusySlotPlanAffinity) OrderAsync(_ context.Context, accounts []AccountCandidate, _ string, _ AffinityOrderingOptions) ([]AccountCandidate, error) {
	ordered := make([]AccountCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.ID != "a-1" {
			ordered = append(ordered, account)
		}
	}
	for _, account := range accounts {
		if account.ID == "a-1" {
			ordered = append(ordered, account)
		}
	}
	return ordered, nil
}

func TestPrepareDispatchAccountsDoesNotPromoteSoftBusyOwnerViaSlotPlan(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &softBusySlotPlanAffinity{slotPlanAffinity: &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		established:        true,
		plan:               SessionAffinitySlotPlan{PreferredAccountID: "a-1"},
	}}
	engine.Affinity = affinity
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 2}}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 2, ClientIPConcurrencyLimit: 0}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-slot-soft-owner"
	input.GroupAccess = ordinaryGroupAccess(policy)

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, soft-busy owner must stay behind", accountIDs(result.Accounts))
	}
	if result.SessionAffinityOwnerAccountID != "a-1" || !result.SessionAffinityOwnerSoftBusy {
		t.Fatalf("owner settlement = %q softBusy=%v", result.SessionAffinityOwnerAccountID, result.SessionAffinityOwnerSoftBusy)
	}
	if result.SessionAffinityOwnerBusy {
		t.Fatal("soft-busy owner must not be reported as hard busy")
	}
}

func TestPrepareDispatchAccountsSoftBusyOwnerAllowsFallbackMove(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &softBusySlotPlanAffinity{slotPlanAffinity: &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		established:        true,
		plan:               SessionAffinitySlotPlan{PreferredAccountID: "a-2", Fallback: true},
	}}
	engine.Affinity = affinity
	engine.Concurrency = &mapBackedConcurrencyStore{current: map[string]int{"a-1": 2}}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 2, ClientIPConcurrencyLimit: 0}
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-slot-soft-fallback"
	input.GroupAccess = ordinaryGroupAccess(policy)

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "a-2" {
		t.Fatalf("accounts = %v, fallback account must still move to front", accountIDs(result.Accounts))
	}
	if !result.SessionAffinitySlotFallback || !result.SessionAffinityOwnerSoftBusy {
		t.Fatalf("slot flags = fallback:%v softBusy:%v", result.SessionAffinitySlotFallback, result.SessionAffinityOwnerSoftBusy)
	}
}

func TestPrepareDispatchAccountsSkipsSlotPlanOnPortError(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		planErr:            errors.New("redis unavailable"),
	}
	engine.Affinity = affinity
	input := dispatchPreparationInput(t, testAccounts("a-1", "a-2"))
	input.SessionAffinityKey = "aff-slot-error"

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.SessionAffinityWaitPlanned || result.SessionAffinitySlotFallback {
		t.Fatalf("advisory slot error must not plan wait: planned=%v fallback=%v",
			result.SessionAffinityWaitPlanned, result.SessionAffinitySlotFallback)
	}
}

func TestPrepareDispatchAccountsCapsSlotBudgetByRequestBudget(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	affinity := &slotPlanAffinity{
		claimOwnerAffinity: &claimOwnerAffinity{owner: "a-1"},
		plan:               SessionAffinitySlotPlan{PreferredAccountID: "a-1"},
	}
	engine.Affinity = affinity
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	input.SessionAffinityKey = "aff-slot-budget"
	input.ServerRetryBudget = gatewaypreauth.NewServerRetryBudget(900, gatewaypreauth.SystemClock{})

	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if affinity.gotBudget > 900 {
		t.Fatalf("slot budget = %d, want <= 900", affinity.gotBudget)
	}
	if result.SessionAffinityWaitRemainingMs != affinity.gotBudget {
		t.Fatalf("wait remaining = %d, want %d", result.SessionAffinityWaitRemainingMs, affinity.gotBudget)
	}
}
