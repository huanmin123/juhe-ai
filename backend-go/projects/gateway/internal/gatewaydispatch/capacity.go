package gatewaydispatch

import (
	"context"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
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
	ordered, _, err := OrderGatewayAccountsByLaneCapacityAvailabilityWithBusyAsync(
		ctx, store, accounts, requestLane, schedulingPolicy, modelPriority)
	return ordered, err
}

// OrderGatewayAccountsByLaneCapacityAvailabilityWithBusyAsync 与
// OrderGatewayAccountsByLaneCapacityAvailabilityAsync 同一排序行为，额外把
// 排序前快照中 lane 容量繁忙的账户 ID 带出（W1b 决策摘要：busy 候选仍留在
// 窗口内、仅排序降位——Node parity 注释，上游派发器负责有界容量等待）。
// busy 列表按输入顺序排列（并发快照的扫描顺序，确定可回放）。
func OrderGatewayAccountsByLaneCapacityAvailabilityWithBusyAsync(
	ctx context.Context,
	store AccountConcurrencyStore,
	accounts []AccountCandidate,
	requestLane gatewayproto.RequestLane,
	schedulingPolicy *gatewayruntimecache.GroupSchedulingPolicy,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) ([]AccountCandidate, []string, error) {
	if len(accounts) < 2 {
		return accounts, nil, nil
	}
	ids := gatewaySessionConcurrencyIDs(accounts)
	currentConcurrency, err := store.LoadCurrentAsync(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	var imageLaneConcurrency map[string]int
	if requestLane == gatewayproto.LaneImage {
		imageLaneConcurrency, err = store.LoadCurrentByLaneAsync(ctx, ids, "image")
		if err != nil {
			return nil, nil, err
		}
	}
	busyAccountIDs := make([]string, 0)
	for _, account := range accounts {
		if isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy) {
			busyAccountIDs = append(busyAccountIDs, account.ID)
		}
	}
	// orderAccountsByLaneCapacityBusyState 内部已做 preserveDispatchPriority
	// Tiers，此处直接返回其结果，保证与原函数排序逐位一致。
	ordered := orderAccountsByLaneCapacityBusyState(accounts, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy, modelPriority)
	// 把本次加载的实时并发写回候选快照：后续亲和软阈值排序/准备期 softBusy
	// 判定直接读 CurrentConcurrency，不能依赖数据库快照里的陈旧值或 nil。
	for index := range ordered {
		ordered[index].CurrentConcurrency = newInt(mapValueOr(currentConcurrency, gatewaySessionConcurrencyID(ordered[index]), 0))
	}
	return ordered, busyAccountIDs, nil
}

// AreGatewayAccountsCapacityBusyForLaneAsync mirrors
// areGatewayAccountsCapacityBusyForLaneAsync（every 语义：整组候选全部容量忙
// 才返回 true；任一账户仍有容量即 false，供组回退判定继续使用本组空闲账户）。
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
	// 单账户忙不得让整组提前回退（D-94）：归档
	// areAccountsCapacityBusyForLane 使用 accounts.every，只有全部候选都达到
	// 硬并发/lane 上限时才认定组忙。
	for _, account := range accounts {
		if !isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy) {
			return false, nil
		}
	}
	return true, nil
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
		account  AccountCandidate
		index    int
		busy     bool
		softBusy bool
	}
	entries := make([]entry, len(accounts))
	for index, account := range accounts {
		concurrencyID := gatewaySessionConcurrencyID(account)
		softBusy := false
		if schedulingPolicy != nil {
			softBusy = schedulingPolicy.SoftBusy(
				mapValueOr(currentConcurrency, concurrencyID, 0),
				accountHardConcurrencyLimit(account),
			)
		}
		entries[index] = entry{
			account:  account,
			index:    index,
			busy:     isAccountCapacityBusyForLane(account, requestLane, currentConcurrency, imageLaneConcurrency, schedulingPolicy),
			softBusy: softBusy,
		}
	}
	// sort.SliceStable keeps the insertion-order tiebreak
	// (|| left.index - right.index)。硬忙在此层先降位（同层内），软忙作为
	// 同层第二键；媒体 lane 不跑热质量排序，这里保证 softBusy 仍生效。
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].busy != entries[right].busy {
			return !entries[left].busy
		}
		if entries[left].softBusy != entries[right].softBusy {
			return !entries[left].softBusy
		}
		return false
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

func dispatchPriorityView(account *AccountCandidate) gatewayproxyhealth.DispatchPriorityAccountView {
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
		return gatewayruntimecache.GroupSchedulingPolicy{}
	}
	return *policy
}

// splitHardBusyForHotQuality 把已达硬并发的候选从热质量排序输入中剥离：
// 热质量比较器只处理同层质量/稳定序，若把硬忙账户一并传入，层内质量重排会
// 把它们提回健康 peer 之前，违反「hardBusy 永远最后」（契约 §3.1）。无健康
// 候选时保持原列表（全部硬忙仍由既有容量等待处理），不足两个候选时不拆分。
func splitHardBusyForHotQuality(accounts []AccountCandidate) (dispatchable []AccountCandidate, hardBusy []AccountCandidate) {
	if len(accounts) < 2 {
		return accounts, nil
	}
	dispatchable = make([]AccountCandidate, 0, len(accounts))
	hardBusy = make([]AccountCandidate, 0)
	for _, account := range accounts {
		current := 0
		if account.CurrentConcurrency != nil {
			current = *account.CurrentConcurrency
		}
		if current >= accountHardConcurrencyLimit(account) {
			hardBusy = append(hardBusy, account)
			continue
		}
		dispatchable = append(dispatchable, account)
	}
	if len(dispatchable) == 0 {
		return accounts, nil
	}
	return dispatchable, hardBusy
}

// softBusyAccountIDsFor 计算本次候选的 softBusy 集合，供热质量排序作为层内
// 第一顺位键；策略关闭/null 时返回空集合（不参与排序判定）。
func softBusyAccountIDsFor(accounts []AccountCandidate, policy *gatewayruntimecache.GroupSchedulingPolicy) map[string]bool {
	out := map[string]bool{}
	if policy == nil {
		return out
	}
	for _, account := range accounts {
		current := 0
		if account.CurrentConcurrency != nil {
			current = *account.CurrentConcurrency
		}
		if policy.SoftBusy(current, accountHardConcurrencyLimit(account)) {
			out[account.ID] = true
		}
	}
	return out
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

// GatewayAccountRPMLimitsByAccountID 按账户资源 ID 聚合候选 RPM 上限（AI 账户
// 每分钟请求上限设计 §5.2，与并发上限聚合同风格）：同 ID 多候选取较小非零
// 上限，0 视为不限制、不参与取小；负值归一为 0。
func GatewayAccountRPMLimitsByAccountID(accounts []AccountCandidate) map[string]int {
	result := make(map[string]int, len(accounts))
	for _, account := range accounts {
		accountID := gatewaySessionConcurrencyID(account)
		if accountID == "" {
			continue
		}
		limit := max(0, account.RPMLimit)
		if existing, ok := result[accountID]; ok && existing > 0 && (limit == 0 || existing < limit) {
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
