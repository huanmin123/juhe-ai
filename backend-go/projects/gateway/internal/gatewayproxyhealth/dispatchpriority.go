package gatewayproxyhealth

import (
	"fmt"
	"math"
)

// Ports runtime/account-dispatch-priority-order.ts.

const unknownGatewayDispatchModelRank = 3

// DispatchPriorityAccountView extracts the tier fields Node reads off the
// generic account. Pointer fields mirror the Node optional fields: nil means
// absent (priority 0 / superRank 1 / fallbackRank 0).
type DispatchPriorityAccountView struct {
	ID                   string
	Priority             *float64
	SuperPriorityEnabled *bool
	FallbackEnabled      *bool
}

// DispatchPriorityOrderOptions mirrors GatewayDispatchPriorityOrderOptions; a
// nil modelRankByAccountID keeps every rank at 0 (Node: no map → rank 0).
type DispatchPriorityOrderOptions struct {
	ModelRankByAccountID map[string]int
}

// GatewayAccountDispatchPriorityTier mirrors gatewayAccountDispatchPriorityTier.
func GatewayAccountDispatchPriorityTier(account DispatchPriorityAccountView, options DispatchPriorityOrderOptions) string {
	modelRank := gatewayAccountDispatchModelRank(account.ID, options)
	fallbackRank := 0
	if account.FallbackEnabled != nil && *account.FallbackEnabled {
		fallbackRank = 1
	}
	superRank := 1
	if account.SuperPriorityEnabled != nil && *account.SuperPriorityEnabled {
		superRank = 0
	}
	priority := 0.0
	if account.Priority != nil && !math.IsNaN(*account.Priority) && !math.IsInf(*account.Priority, 0) {
		priority = math.Trunc(*account.Priority)
	}
	return fmt.Sprintf("%d:%d:%d:%d", modelRank, fallbackRank, superRank, int64(priority))
}

// PreserveGatewayAccountDispatchPriorityTiers mirrors
// preserveGatewayAccountDispatchPriorityTiers: the reordered accounts keep the
// base account tier sequence; accounts whose tier is unknown to the base
// preserve their relative order at the tail. view projects each element onto
// DispatchPriorityAccountView.
func PreserveGatewayAccountDispatchPriorityTiers[T any](
	baseAccounts []T,
	reorderedAccounts []T,
	view func(T) DispatchPriorityAccountView,
	options DispatchPriorityOrderOptions,
) []T {
	if len(baseAccounts) < 2 || len(reorderedAccounts) < 2 {
		return append([]T(nil), reorderedAccounts...)
	}

	baseTierOrder := make([]string, 0, len(baseAccounts))
	seenBaseTiers := make(map[string]struct{}, len(baseAccounts))
	for _, account := range baseAccounts {
		tier := GatewayAccountDispatchPriorityTier(view(account), options)
		if _, seen := seenBaseTiers[tier]; seen {
			continue
		}
		seenBaseTiers[tier] = struct{}{}
		baseTierOrder = append(baseTierOrder, tier)
	}

	reorderedByTier := make(map[string][]T)
	var unknownTierAccounts []T
	for _, account := range reorderedAccounts {
		tier := GatewayAccountDispatchPriorityTier(view(account), options)
		if _, known := seenBaseTiers[tier]; !known {
			unknownTierAccounts = append(unknownTierAccounts, account)
			continue
		}
		reorderedByTier[tier] = append(reorderedByTier[tier], account)
	}

	output := make([]T, 0, len(reorderedAccounts))
	for _, tier := range baseTierOrder {
		output = append(output, reorderedByTier[tier]...)
	}
	output = append(output, unknownTierAccounts...)
	return output
}

func gatewayAccountDispatchModelRank(accountID string, options DispatchPriorityOrderOptions) int {
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
