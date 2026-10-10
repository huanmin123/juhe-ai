package gatewaydispatch

import (
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// 通用调度排除集与分派段状态机（docs/functions/调度内核通用化设计.md 5.1，
// 批次 1）。本文件只承载形状定义、准备层切段纯函数与内核活动段推导；逐段
// 推进状态机在 upstreamdispatch.go。

// SchedulingExclusions 通用调度排除集：本次引擎调用入口由准备层固化的软排除
// 账户集合。排除只表示"普通候选让位"——不改变任何硬门（熔断确认、准入注册、
// 并发租约）语义，也不新增尝试预算；被排除账号在其分派段普通候选真实耗尽后
// 由内核解除软排除（releasedAccountIDs，见 upstreamdispatch.go）。排除语义的
// 产出统一在准备层；turn 避让退化为排除集的一个来源，内核不感知画像。
type SchedulingExclusions struct {
	ExcludedAccountIDs []string
}

// DispatchPriorityTier 与 gatewaycircuit.dispatchPriorityTier 的四元
// modelRank:fallbackRank:superRank:priority 对齐（设计 5.1：tier 沿用引擎既有
// 四元组，不得折算为 account.Priority 数值）。
type DispatchPriorityTier struct {
	ModelRank    int64
	FallbackRank int64
	SuperRank    int64
	Priority     int64
}

// String 按 gatewaycircuit.dispatchPriorityTier 的 "model:fallback:super:priority"
// 形状输出（审计字段与既有 tier 口径一致）。
func (t DispatchPriorityTier) String() string {
	return strconv.FormatInt(t.ModelRank, 10) + ":" +
		strconv.FormatInt(t.FallbackRank, 10) + ":" +
		strconv.FormatInt(t.SuperRank, 10) + ":" +
		strconv.FormatInt(t.Priority, 10)
}

// parseDispatchPriorityTier 解析 gatewayAccountDispatchPriorityTier 的产出
// （单一 tier 口径：四元组由既有函数计算，这里只做形状转换；该函数输出恒为
// "m:f:s:p"，解析失败仅可能来自非引擎产出的手工输入，回落零值）。
func parseDispatchPriorityTier(raw string) DispatchPriorityTier {
	parts := strings.Split(raw, ":")
	if len(parts) != 4 {
		return DispatchPriorityTier{}
	}
	var tier DispatchPriorityTier
	values := [4]*int64{&tier.ModelRank, &tier.FallbackRank, &tier.SuperRank, &tier.Priority}
	for i, part := range parts {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return DispatchPriorityTier{}
		}
		*values[i] = value
	}
	return tier
}

// dispatchPriorityTierOf 计算账户四元 tier（复用引擎既有 tier 函数）。
func dispatchPriorityTierOf(account AccountCandidate, priority *gatewayrouting.GatewayAccountModelPriority) DispatchPriorityTier {
	return parseDispatchPriorityTier(gatewayAccountDispatchPriorityTier(account, priority))
}

// DispatchSegment 分派段：准备层最终候选顺序上的连续 run。OpaqueSegmentID 只
// 用于关联同一次准备结果与审计，不参与排序；Accounts 保持最终候选原顺序；
// 段内账户共享同一外层段类与四元 tier。
type DispatchSegment struct {
	OpaqueSegmentID string
	Tier            DispatchPriorityTier
	Accounts        []AccountCandidate
}

// newAccountIDSet 构建账户 ID 集合（空输入返回 nil，与既有 stringSet 口径一致）。
func newAccountIDSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// accountIDList 投影账户 ID 列表（保持给定顺序）。
func accountIDList(accounts []AccountCandidate) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

// dispatchOuterSegmentClassOf 构造分派段外层段类函数（实际段界来源见
// buildDispatchSegments 注释）：latencyPartitionActive 守卫下的速度优先健康/
// 降级尾部分区（latencyDegradedTailPartition 同条件）与 ipPartitionActive
// 守卫下的 ClientIP 回避尾部（Applied 时 AvoidedAccountIDs 即尾部子序列）。
// 类别只取既有决策数据的成员关系，不发起任何端口调用。
func dispatchOuterSegmentClassOf(
	latencyPartitionActive bool,
	latencyDegradedAccountIDs map[string]struct{},
	ipPartitionActive bool,
	clientIPAvoidedAccountIDs map[string]struct{},
) func(AccountCandidate) string {
	return func(account AccountCandidate) string {
		class := ""
		if latencyPartitionActive {
			if _, degraded := latencyDegradedAccountIDs[account.ID]; degraded {
				class += "d"
			}
		}
		if ipPartitionActive {
			if _, avoided := clientIPAvoidedAccountIDs[account.ID]; avoided {
				class += "i"
			}
		}
		return class
	}
}

// buildDispatchSegments 在最终候选顺序确定后沿序列生成连续分派段（设计 5.1）。
// 只投影既有顺序与元数据：不重排候选、不跨段合并、不按四元组数值重新排序，
// 不触发任何硬门（不调用 CanAttemptAccount、熔断确认或并发租约）。
//
// 段边界 = 外层段类与四元 tier 的联合连续 run：
//   - 四元 tier 变化切断 run；相同 tier 非连续出现时各自成段，不合并；
//   - 相邻同 tier 但分属不同外层段（健康/降级、ClientIP 回避尾部）不得合并。
//
// 外层段类取证（批次 1 实际段界来源）：准备层最终序列组装（quota/容量/热质量/
// 亲和 claim 之后）中带显式成员标记的重排来源有两类——
//  1. 速度优先的健康/降级尾部分区（latencyDegradedTailPartition；守卫条件
//     speed_first 模式 + 降级集非空，与该分区实现一致）；
//  2. ClientIP 回避排序的"回避账户后置"（chainClientIPAvoidance 适配
//     gatewayclientip 端口；Applied 守卫下其 AvoidedAccountIDs 即尾部子序列
//     标记）。
//
// 其余重排来源（人工迁移、热质量层内重排、来源避让排序）在最终序列上无显式
// 段界标记，按"连续 tier run"落地。来源避让（排除集成员）刻意不作为段界：
// 排除账号必须与其段内普通候选同段，才能表达"段内让位 + 段耗尽后翻回"。
func buildDispatchSegments(
	accounts []AccountCandidate,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
	outerClassOf func(AccountCandidate) string,
) []DispatchSegment {
	if len(accounts) == 0 {
		return nil
	}
	segments := make([]DispatchSegment, 0)
	runStarted := false
	runTier := DispatchPriorityTier{}
	runClass := ""
	for _, account := range accounts {
		tier := dispatchPriorityTierOf(account, modelPriority)
		class := ""
		if outerClassOf != nil {
			class = outerClassOf(account)
		}
		if runStarted && (tier != runTier || class != runClass) {
			runStarted = false
		}
		if !runStarted {
			segments = append(segments, DispatchSegment{
				OpaqueSegmentID: "prep-seg-" + strconv.Itoa(len(segments)+1),
				Tier:            tier,
			})
			runTier = tier
			runClass = class
			runStarted = true
		}
		last := len(segments) - 1
		segments[last].Accounts = append(segments[last].Accounts, account)
	}
	return segments
}

// activeDispatchSegment 是内核状态机的活动段（设计 5.1）：由当前有序候选按
// 分派段来源重新投影得到。working 是普通候选工作列表（本段未释放的软排除
// 账号不在其中）；pendingExclusions 是本段尚未释放的排除账号（保持原顺序）。
type activeDispatchSegment struct {
	originSegmentID   string
	opaqueSegmentID   string
	tier              DispatchPriorityTier
	working           []AccountCandidate
	pendingExclusions []AccountCandidate
}

// regenerateIdentity 在等待/reload 重排后重新生成本段的 opaque 段 ID（设计
// 5.1：新顺序不得复用旧段）。外层段界在重排后保持不变——活动段从不跨来源段
// 混合账户，重排只发生在单一活动段的列表内。
func (s *activeDispatchSegment) regenerateIdentity(identityGen *int) {
	*identityGen++
	if s.originSegmentID == "" {
		s.opaqueSegmentID = "engine-seg-" + strconv.Itoa(*identityGen)
		return
	}
	s.opaqueSegmentID = s.originSegmentID + "#r" + strconv.Itoa(*identityGen)
}

// deriveActiveDispatchSegments 把本次引擎调用的有序候选按分派段来源重新切段
// （设计 5.1：入口与 reload 顺序必须重新生成外层段边界；来源段已编码
// "外层段+tier"，按来源段对连续候选分组即保证相邻同 tier 的不同外层段不合并，
// 也保证不按数值重排）。未携带分派段元数据的调用（内核直调、测试）合成单一
// 活动段，保持既有扁平行为。排除集豁免路径（SameAccountRetry 钉住、熔断确认）
// 由调用方清空排除集后复用本函数。released 为调用内已释放集合：已释放账号
// 不再进入 pendingExclusions，直接作为普通候选。
func deriveActiveDispatchSegments(
	accounts []AccountCandidate,
	provided []DispatchSegment,
	exclusions map[string]struct{},
	released map[string]struct{},
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) []activeDispatchSegment {
	originByAccountID := make(map[string]string, len(accounts))
	tierByOrigin := make(map[string]DispatchPriorityTier)
	for _, segment := range provided {
		if _, seen := tierByOrigin[segment.OpaqueSegmentID]; !seen {
			tierByOrigin[segment.OpaqueSegmentID] = segment.Tier
		}
		for _, account := range segment.Accounts {
			if _, seen := originByAccountID[account.ID]; !seen {
				originByAccountID[account.ID] = segment.OpaqueSegmentID
			}
		}
	}
	if len(provided) == 0 {
		// 未携带段元数据（内核直调、测试）：合成单一活动段，保持既有扁平行为。
		// 警示：本分支不做 working/pendingExclusions 拆分——若入口携带非空
		// SchedulingExclusions 却无段元数据，排除集在该分支不生效（被排除账号
		// 仍留在 working，作为普通候选直接尝试，不会翻回让位）。当前生产链不
		// 会出现该组合：准备层固化排除集时总是同时产出分派段元数据；内核直调
		// 与测试不携带排除集。此处只留警示，不加开关、不改行为。
		segments := []activeDispatchSegment{{
			opaqueSegmentID: "engine-seg-1",
		}}
		if len(accounts) > 0 {
			segments[0].working = append(segments[0].working, accounts...)
			segments[0].tier = dispatchPriorityTierOf(accounts[0], modelPriority)
		}
		return segments
	}
	segments := make([]activeDispatchSegment, 0)
	runKey := ""
	runStarted := false
	for _, account := range accounts {
		origin, knownOrigin := originByAccountID[account.ID]
		tier := tierByOrigin[origin]
		key := origin
		if !knownOrigin {
			// 携带段元数据但候选不在任何来源段（防御路径，生产链面收窄不会
			// 产生）：按 tier 独立成 run，不与来源段账户合并。
			tier = dispatchPriorityTierOf(account, modelPriority)
			key = "\x00tier:" + tier.String()
		}
		if runStarted && key != runKey {
			runStarted = false
		}
		if !runStarted {
			segments = append(segments, activeDispatchSegment{
				originSegmentID: origin,
				opaqueSegmentID: origin,
				tier:            tier,
			})
			runKey = key
			runStarted = true
		}
		last := len(segments) - 1
		segments[last].working = append(segments[last].working, account)
	}
	// working / pendingExclusions 拆分：软排除且尚未释放的账号让位等待翻回。
	for i := range segments {
		if len(exclusions) == 0 {
			segments[i].pendingExclusions = nil
			continue
		}
		working := make([]AccountCandidate, 0, len(segments[i].working))
		pending := make([]AccountCandidate, 0)
		for _, account := range segments[i].working {
			if _, excluded := exclusions[account.ID]; excluded {
				if _, alreadyReleased := released[account.ID]; !alreadyReleased {
					pending = append(pending, account)
					continue
				}
			}
			working = append(working, account)
		}
		segments[i].working = working
		segments[i].pendingExclusions = pending
	}
	return segments
}

// activeSegmentsHaveRoundCandidates 判断整池本轮是否仍有可推进候选：任一活动
// 段的 working 非空，或尚有未释放排除账号（翻回后成为候选）。
func activeSegmentsHaveRoundCandidates(segments []activeDispatchSegment) bool {
	for i := range segments {
		if len(segments[i].working) > 0 || len(segments[i].pendingExclusions) > 0 {
			return true
		}
	}
	return false
}

// activeSegmentsPool 聚合全部活动段的 working 候选（批次 1 回炉裁决：整池等待
// 域）。按段顺序拼接保持旧整池 dispatchAccounts 的全局相对顺序；尚未释放的
// 排除账号只存在于 pendingExclusions、不入池（旧行为在入口过滤后同样不在列表），
// 释放后的账号按释放门规则已并入 working。
func activeSegmentsPool(segments []activeDispatchSegment) []AccountCandidate {
	size := 0
	for i := range segments {
		size += len(segments[i].working)
	}
	pool := make([]AccountCandidate, 0, size)
	for i := range segments {
		pool = append(pool, segments[i].working...)
	}
	return pool
}
