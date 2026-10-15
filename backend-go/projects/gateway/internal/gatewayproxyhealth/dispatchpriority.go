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
// DispatchPriorityAccountView; 指针入参避免按值视图复制整个账户（dispatch
// 分配画像：视图副本逃逸 + 按 tier append 增长占该函数 88MB/3000 次）。
// view 返回的字段指针只在 GatewayAccountDispatchPriorityTier 的同步调用内
// 解引用，函数返回后不得保留。
func PreserveGatewayAccountDispatchPriorityTiers[T any](
	baseAccounts []T,
	reorderedAccounts []T,
	view func(*T) DispatchPriorityAccountView,
	options DispatchPriorityOrderOptions,
) []T {
	if len(baseAccounts) < 2 || len(reorderedAccounts) < 2 {
		return append([]T(nil), reorderedAccounts...)
	}

	baseTierOrder := make([]string, 0, len(baseAccounts))
	seenBaseTiers := make(map[string]struct{}, len(baseAccounts))
	for i := range baseAccounts {
		tier := GatewayAccountDispatchPriorityTier(view(&baseAccounts[i]), options)
		if _, seen := seenBaseTiers[tier]; seen {
			continue
		}
		seenBaseTiers[tier] = struct{}{}
		baseTierOrder = append(baseTierOrder, tier)
	}

	// 单次投影各账户 tier，再按基准 tier 序列分组输出：不再用
	// map[tier][]T 按 tier append（增长复制实测该项目 31.6MB）。
	tierOf := make([]string, len(reorderedAccounts))
	for i := range reorderedAccounts {
		tierOf[i] = GatewayAccountDispatchPriorityTier(view(&reorderedAccounts[i]), options)
	}

	output := make([]T, 0, len(reorderedAccounts))
	for _, tier := range baseTierOrder {
		for i := range reorderedAccounts {
			if tierOf[i] == tier {
				output = append(output, reorderedAccounts[i])
			}
		}
	}
	for i := range reorderedAccounts {
		if _, known := seenBaseTiers[tierOf[i]]; !known {
			output = append(output, reorderedAccounts[i])
		}
	}
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
