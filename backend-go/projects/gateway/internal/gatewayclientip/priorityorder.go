package gatewayclientip

import (
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Dispatch priority ordering port of
// backend/src/modules/gateway/runtime/account-dispatch-priority-order.ts.
// The account carrier is gatewayruntimecache.OpenAIAccountSecret (the G05
// AccountCandidate): Node's GatewayDispatchPriorityAccount fields map to
// ID / Priority / SuperPriorityEnabled / FallbackEnabled.

const unknownGatewayDispatchModelRank = 3

// DispatchPriorityTierInput mirrors GatewayDispatchPriorityOrderOptions.
type DispatchPriorityTierInput struct {
	// ModelRankByAccountID mirrors modelRankByAccountId; nil mirrors undefined.
	ModelRankByAccountID map[string]int
}

// PreserveGatewayAccountDispatchPriorityTiers mirrors
// preserveGatewayAccountDispatchPriorityTiers.
func PreserveGatewayAccountDispatchPriorityTiers(
	baseAccounts []gatewayruntimecache.OpenAIAccountSecret,
	reorderedAccounts []gatewayruntimecache.OpenAIAccountSecret,
	options DispatchPriorityTierInput,
) []gatewayruntimecache.OpenAIAccountSecret {
	if len(baseAccounts) < 2 || len(reorderedAccounts) < 2 {
		return append([]gatewayruntimecache.OpenAIAccountSecret(nil), reorderedAccounts...)
	}

	baseTierOrder := make([]string, 0, len(baseAccounts))
	seenBaseTiers := map[string]bool{}
	for i := range baseAccounts {
		tier := GatewayAccountDispatchPriorityTier(baseAccounts[i], options)
		if seenBaseTiers[tier] {
			continue
		}
		seenBaseTiers[tier] = true
		baseTierOrder = append(baseTierOrder, tier)
	}

	reorderedByTier := map[string][]gatewayruntimecache.OpenAIAccountSecret{}
	unknownTierAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0)
	for i := range reorderedAccounts {
		account := reorderedAccounts[i]
		tier := GatewayAccountDispatchPriorityTier(account, options)
		if !seenBaseTiers[tier] {
			unknownTierAccounts = append(unknownTierAccounts, account)
			continue
		}
		reorderedByTier[tier] = append(reorderedByTier[tier], account)
	}

	output := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(reorderedAccounts))
	for _, tier := range baseTierOrder {
		output = append(output, reorderedByTier[tier]...)
	}
	output = append(output, unknownTierAccounts...)
	return output
}

// GatewayAccountDispatchPriorityTier mirrors gatewayAccountDispatchPriorityTier.
func GatewayAccountDispatchPriorityTier(
	account gatewayruntimecache.OpenAIAccountSecret,
	options DispatchPriorityTierInput,
) string {
	modelRank := gatewayAccountDispatchModelRank(account.ID, options)
	fallbackRank := 0
	if account.FallbackEnabled {
		fallbackRank = 1
	}
	superRank := 1
	if account.SuperPriorityEnabled {
		superRank = 0
	}
	return strconv.Itoa(modelRank) + ":" + strconv.Itoa(fallbackRank) + ":" + strconv.Itoa(superRank) + ":" + strconv.Itoa(account.Priority)
}

func gatewayAccountDispatchModelRank(accountID string, options DispatchPriorityTierInput) int {
	if options.ModelRankByAccountID == nil {
		return 0
	}
	rank, ok := options.ModelRankByAccountID[accountID]
	if !ok {
		return unknownGatewayDispatchModelRank
	}
	if rank < 0 {
		return 0
	}
	return rank
}

// ModelRankByAccountID adapts gatewayrouting.GatewayAccountModelPriority to
// the options map (nil-safe).
func ModelRankByAccountID(priority *gatewayrouting.GatewayAccountModelPriority) map[string]int {
	if priority == nil {
		return nil
	}
	return priority.RankByAccountID
}
