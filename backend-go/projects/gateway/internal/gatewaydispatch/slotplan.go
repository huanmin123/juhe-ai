package gatewaydispatch

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// planSessionAffinitySlot 在准备期选择会话名额账户：
//   - 候选顺序 = owner、owner 同层账户、其余账户（保持现有顺序）；
//   - 名额等待与 owner 并发等待共享同一预算，准备期消耗后把剩余额度带给
//     dispatch；
//   - 端口未实现（memory 降级/测试 fake）时不规划，保持原排序。
func (p *CandidatePipeline) planSessionAffinitySlot(ctx context.Context, input gatewaypreauth.DispatchPreparationInput, ownerAccountID string, ownerEstablished bool, accounts []AccountCandidate) (SessionAffinitySlotPlan, int, int64, bool) {
	port, ok := p.engine.Affinity.(SessionAffinitySlotPort)
	if !ok || input.SessionAffinityKey == "" || ownerAccountID == "" || len(accounts) == 0 {
		return SessionAffinitySlotPlan{}, -1, 0, false
	}
	budgetMs := p.engine.Config.SessionAffinityOwnerWaitBudgetMs
	if budgetMs < 0 {
		budgetMs = 0
	}
	if input.ServerRetryBudget != nil {
		if remaining := input.ServerRetryBudget.RemainingMs(nil); remaining < budgetMs {
			budgetMs = remaining
		}
	}
	if budgetMs < 0 {
		budgetMs = 0
	}
	var ownerTier DispatchPriorityTier
	ownerFound := false
	for _, account := range accounts {
		if account.ID == ownerAccountID {
			ownerTier = dispatchPriorityTierOf(account, input.ModelPriority)
			ownerFound = true
			break
		}
	}
	if !ownerFound {
		return SessionAffinitySlotPlan{}, -1, 0, false
	}
	candidates := make([]SessionSlotCandidate, 0, len(accounts))
	appendCandidate := func(account AccountCandidate) {
		candidates = append(candidates, SessionSlotCandidate{
			AccountID:        account.ID,
			ConcurrencyLimit: accountHardConcurrencyLimit(account),
			RPMLimit:         account.RPMLimit,
		})
	}
	for _, account := range accounts {
		if account.ID == ownerAccountID {
			appendCandidate(account)
			break
		}
	}
	for _, account := range accounts {
		if account.ID == ownerAccountID {
			continue
		}
		if dispatchPriorityTierOf(account, input.ModelPriority) == ownerTier {
			appendCandidate(account)
		}
	}
	for _, account := range accounts {
		if account.ID == ownerAccountID {
			continue
		}
		if dispatchPriorityTierOf(account, input.ModelPriority) != ownerTier {
			appendCandidate(account)
		}
	}
	plan, err := port.PlanSessionSlot(ctx, input.SessionAffinityKey, ownerAccountID, ownerEstablished, candidates, budgetMs)
	if err != nil {
		// 名额探测失败是 advisory：不把“已规划”传给 dispatch，否则会把 owner
		// 并发等待预算误置 0；按未规划处理，保留原有 owner 等待。
		return SessionAffinitySlotPlan{}, -1, 0, false
	}
	preferredIndex := -1
	if plan.PreferredAccountID != "" {
		for index, account := range accounts {
			if account.ID == plan.PreferredAccountID {
				preferredIndex = index
				break
			}
		}
	}
	remaining := budgetMs - plan.WaitedMs
	if remaining < 0 {
		remaining = 0
	}
	plan.WaitedMs = budgetMs - remaining
	return plan, preferredIndex, remaining, true
}

func moveAccountToFront(accounts []AccountCandidate, index int) []AccountCandidate {
	if index <= 0 || index >= len(accounts) {
		return accounts
	}
	ordered := make([]AccountCandidate, 0, len(accounts))
	ordered = append(ordered, accounts[index])
	ordered = append(ordered, accounts[:index]...)
	ordered = append(ordered, accounts[index+1:]...)
	return ordered
}
