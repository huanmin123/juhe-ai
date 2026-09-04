package gatewaydispatch

import (
	"context"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Lane capacity ordering, migrated from dispatch/capacity.ts. Concurrency
// identity helpers live in gatewaysession (concurrency-identity migration)
// and the dispatch-priority tier preservation in gatewayproxyhealth
// (account-dispatch-priority-order migration) — both reused, not duplicated.

// laneCapacityOptions carries the ordering inputs.
type laneCapacityOptions struct {
	concurrency      AccountConcurrencyStore
	requestLane      gatewayproto.RequestLane
	schedulingPolicy *gatewayruntimecacheGroupSchedulingPolicy
	modelPriority    *gatewayrouting.GatewayAccountModelPriority
}

// gatewayruntimecacheGroupSchedulingPolicy aliases the opaque policy map.
type gatewayruntimecacheGroupSchedulingPolicy = gatewayruntimecache.GroupSchedulingPolicy

// RefreshGatewayAccountCurrentConcurrencyAsync mirrors
// refreshGatewayAccountCurrentConcurrencyAsync.
func RefreshGatewayAccountCurrentConcurrencyAsync(ctx context.Context, store AccountConcurrencyStore, accounts []AccountCandidate) ([]AccountCandidate, error) {
	ids := gatewaySessionConcurrencyIDs(accounts)
	concurrency, err := store.LoadCurrentAsync(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]AccountCandidate, len(accounts))
	for index, account := range accounts {
		account.CurrentConcurrency = newInt(mapValueOr(concurrency, gatewaySessionConcurrencyID(account), 0))
		out[index] = account
	}
	return out, nil
}

// OrderGatewayAccountsByLaneCapacityAvailabilityAsync mirrors
// orderGatewayAccountsByLaneCapacityAvailabilityAsync.
func OrderGatewayAccountsByLaneCapacityAvailabilityAsync(
	ctx context.Context,
	store AccountConcurrencyStore,
	accounts []AccountCandidate,
	requestLane gatewayproto.RequestLane,
	schedulingPolicy *gatewayruntimecache.GroupSchedulingPolicy,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) ([]AccountCandidate, error) {
	if len(accounts) < 2 {
		return accounts, nil
	}
	ids := gatewaySessionConcurrencyIDs(accounts)
	currentConcurrency, err := store.LoadCurrentAsync(ctx, ids)
	if err != nil {
		return nil, err
	}
	var imageLaneConcurrency map[string]int
	if requestLane == gatewayproto.LaneImage {
		imageLaneConcurrency, err = store.LoadCurrentByLaneAsync(ctx, ids, "image")
		if err != nil {
			return nil, err
		}
	}
	return orderAccountsByLaneCapacityBusyState(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy, modelPriority), nil
}

// AreGatewayAccountsCapacityBusyForLaneAsync mirrors
// areGatewayAccountsCapacityBusyForLaneAsync.
func AreGatewayAccountsCapacityBusyForLaneAsync(
	ctx context.Context,
	store AccountConcurrencyStore,
	accounts []AccountCandidate,
	requestLane gatewayproto.RequestLane,
	schedulingPolicy *gatewayruntimecache.GroupSchedulingPolicy,
) (bool, error) {
	if len(accounts) == 0 {
		return false, nil
	}
	ids := gatewaySessionConcurrencyIDs(accounts)
	currentConcurrency, err := store.LoadCurrentAsync(ctx, ids)
	if err != nil {
		return false, err
	}
	var imageLaneConcurrency map[string]int
	if requestLane == gatewayproto.LaneImage {
		imageLaneConcurrency, err = store.LoadCurrentByLaneAsync(ctx, ids, "image")
		if err != nil {
			return false, err
		}
	}
	for _, account := range accounts {
		if isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy) {
			return true, nil
		}
	}
	return false, nil
}

func orderAccountsByLaneCapacityBusyState(
	accounts []AccountCandidate,
	requestLane gatewayproto.RequestLane,
	currentConcurrency map[string]int,
	imageLaneConcurrency map[string]int,
	schedulingPolicy *gatewayruntimecache.GroupSchedulingPolicy,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) []AccountCandidate {
	type entry struct {
		account AccountCandidate
		index   int
		busy    bool
	}
	entries := make([]entry, len(accounts))
	for index, account := range accounts {
		entries[index] = entry{
			account: account,
			index:   index,
			busy:    isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy),
		}
	}
	// sort.SliceStable keeps the insertion-order tiebreak
	// (|| left.index - right.index).
	sort.SliceStable(entries, func(left, right int) bool {
		return boolToInt(entries[left].busy) < boolToInt(entries[right].busy)
	})
	ordered := make([]AccountCandidate, len(entries))
	for index, item := range entries {
		ordered[index] = item.account
	}
	return preserveDispatchPriorityTiers(accounts, ordered, modelPriority)
}

func isAccountCapacityBusyForLane(
	account AccountCandidate,
	requestLane gatewayproto.RequestLane,
	currentConcurrency map[string]int,
	imageLaneConcurrency map[string]int,
	schedulingPolicy *gatewayruntimecache.GroupSchedulingPolicy,
) bool {
	hardLimit := accountHardConcurrencyLimit(account)
	concurrencyAccountID := gatewaySessionConcurrencyID(account)
	if mapValueOr(currentConcurrency, concurrencyAccountID, 0) >= hardLimit {
		return true
	}
	if requestLane != gatewayproto.LaneImage {
		return false
	}
	laneLimit := gatewayhotquality.EffectiveImageLaneConcurrencyLimit(hardLimit, derefPolicy(schedulingPolicy))
	return mapValueOr(imageLaneConcurrency, concurrencyAccountID, 0) >= laneLimit
}

func accountHardConcurrencyLimit(account AccountCandidate) int {
	if account.ConcurrencyLimit < 1 {
		return 1
	}
	return account.ConcurrencyLimit
}

// preserveDispatchPriorityTiers adapts
// gatewayproxyhealth.PreserveGatewayAccountDispatchPriorityTiers.
func preserveDispatchPriorityTiers(before, after []AccountCandidate, modelPriority *gatewayrouting.GatewayAccountModelPriority) []AccountCandidate {
	options := gatewayproxyhealth.DispatchPriorityOrderOptions{ModelRankByAccountID: modelPriorityRankMap(modelPriority)}
	return gatewayproxyhealth.PreserveGatewayAccountDispatchPriorityTiers(before, after, dispatchPriorityView, options)
}

func dispatchPriorityView(account AccountCandidate) gatewayproxyhealth.DispatchPriorityAccountView {
	return gatewayproxyhealth.DispatchPriorityAccountView{
		ID:                   account.ID,
		Priority:             newFloat64(float64(account.Priority)),
		SuperPriorityEnabled: &account.SuperPriorityEnabled,
		FallbackEnabled:      &account.FallbackEnabled,
	}
}

func newFloat64(value float64) *float64 { return &value }

func modelPriorityRankMap(priority *gatewayrouting.GatewayAccountModelPriority) map[string]int {
	if priority == nil {
		return nil
	}
	out := make(map[string]int, len(priority.RankByAccountID))
	for id, rank := range priority.RankByAccountID {
		out[id] = rank
	}
	return out
}

func derefPolicy(policy *gatewayruntimecache.GroupSchedulingPolicy) gatewayruntimecache.GroupSchedulingPolicy {
	if policy == nil {
		return nil
	}
	return *policy
}

// gatewaySessionConcurrencyID mirrors gatewayAccountConcurrencyAccountId via
// the gatewaysession migration.
func gatewaySessionConcurrencyID(account AccountCandidate) string {
	return gatewaysession.GatewayAccountConcurrencyAccountID(gatewaysession.GatewayAccountConcurrencyIdentityOf(account))
}

func gatewaySessionConcurrencyIDs(accounts []AccountCandidate) []string {
	ids := make([]string, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		id := gatewaySessionConcurrencyID(account)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// GatewayAccountConcurrencyLimitsByAccountID mirrors
// gatewayAccountConcurrencyLimitsByAccountId.
func GatewayAccountConcurrencyLimitsByAccountID(accounts []AccountCandidate) map[string]int {
	result := make(map[string]int, len(accounts))
	for _, account := range accounts {
		accountID := gatewaySessionConcurrencyID(account)
		if accountID == "" {
			continue
		}
		limit := accountHardConcurrencyLimit(account)
		if existing, ok := result[accountID]; ok && existing < limit {
			limit = existing
		}
		result[accountID] = limit
	}
	return result
}

func mapValueOr(source map[string]int, key string, fallback int) int {
	if value, ok := source[key]; ok {
		return value
	}
	return fallback
}

func newInt(value int) *int { return &value }

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
