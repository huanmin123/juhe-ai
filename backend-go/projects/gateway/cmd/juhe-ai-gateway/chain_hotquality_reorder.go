package main

// 期二（缓存率感知调度与用量缓存率展示设计 5.6/7/8.1）：热质量层内纯排序
// 适配，唯一调用方是 chainHotQualityPort.ReorderOnly（高并发分组最终亲和
// 排序后的重排序；OrderAsync 的决策解释自 runtime 结果直出，不经过本文件）。
// gatewayhotquality 的纯排序入口 DecideHotQualityCandidateOrderOnly
// 以 HotQualityCandidate（selection 视图，含质量快照）为候选载荷；本文件把
// dispatch 候选切片重建为该视图（协议组切分、scope、快照读取与 OrderGateway
// AccountsByHotQuality 内部同构——该内部逻辑未导出，此处按导出面重建），
// 调纯排序并映射回 dispatch 候选序。语义边界：
//   - 纯排序：无探索累计/预留/结算、无路由观测写入（nil Exploration 短路
//     not_configured），供高并发分组最终亲和排序后重应用层内排序；
//   - 排序结果只依赖候选集合、质量快照与缓存率窗口，与输入顺序无关；
//   - 缓存率窗口快照来自 gatewaycacherate.SnapshotSource（每次调用取最新，
//     stale/未加载时不喂 CacheRates，维度中性）；
//   - 调用频率限定在高并发等待后的重排序（设计第 7 节），不进普通请求
//     热路径——快照读增量以此为界。

import (
	"context"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
)

// chainHotQualityReorderInput 是纯排序一次调用的完整输入（与
// gatewaydispatch.HotQualityOrderInput 同形 + 缓存率快照）。
type chainHotQualityReorderInput struct {
	Accounts                  []gatewaydispatch.AccountCandidate
	ModelPriority             *gatewaydispatch.ModelPriority
	Mode                      string
	SystemAccountID           string
	RouteStrategyID           string
	GroupID                   string
	RequestLane               string
	Model                     string
	LatencyDegradedAccountIDs map[string]struct{}
	CacheRates                map[string]gatewayhotquality.CacheRateWindow
	CacheRateStale            bool
}

// chainHotQualityReorderAccounts 重建候选视图并执行纯层内排序：返回排序后
// 候选与首协议组排序决策的解释（与 OrderGatewayAccountsByHotQuality 的
// firstResult 同口径）。零候选返回原序 + nil 解释（无可排序事实）。
func chainHotQualityReorderAccounts(
	ctx context.Context,
	runtime *gatewayhotquality.GatewayHotQualityRuntime,
	input chainHotQualityReorderInput,
) (gatewaydispatch.HotQualityOrder, error) {
	if len(input.Accounts) == 0 || runtime == nil || runtime.HotQualityStore == nil {
		return gatewaydispatch.HotQualityOrder{Accounts: input.Accounts}, nil
	}
	nowMs := gatewayupstream.NowMs()
	rankByAccountID := modelPriorityRankMapOf(input.ModelPriority)
	degraded := chainLatencyDegradedOf(input.LatencyDegradedAccountIDs)
	model := chainModelPtr(input.Model)

	ordered := make([]gatewaydispatch.AccountCandidate, 0, len(input.Accounts))
	var explanation *gatewayhotquality.HotQualityCandidateSelectionExplanation
	for _, group := range chainHotQualityGroupByProtocolProfile(input.Accounts) {
		routeScopeKey, err := chainHotQualityRouteScopeKey(chainHotQualityRouteScopeParts{
			SystemAccountID: input.SystemAccountID,
			RouteStrategyID: input.RouteStrategyID,
			GroupID:         input.GroupID,
			ProtocolProfile: group.profile,
			RequestLane:     input.RequestLane,
		})
		if err != nil {
			return gatewaydispatch.HotQualityOrder{}, err
		}
		candidates := make([]gatewayhotquality.HotQualityCandidate, 0, len(group.accounts))
		for index, account := range group.accounts {
			view := chainHotQualityAccountViewOf(account)
			runtimeKey, err := gatewayhotquality.GatewayAccountRuntimeKey(view)
			if err != nil {
				return gatewaydispatch.HotQualityOrder{}, err
			}
			snapshot, err := runtime.HotQualityStore.Get(ctx, gatewayhotquality.HotQualityScope{
				AccountRuntimeKey: runtimeKey,
				ProtocolProfile:   group.profile,
				RequestLane:       input.RequestLane,
				ModelFamily:       gatewayhotquality.GatewayHotQualityModelFamily(model),
			}, &nowMs)
			if err != nil {
				return gatewaydispatch.HotQualityOrder{}, err
			}
			candidate := gatewayhotquality.HotQualityCandidate{
				AccountID:         view.ID,
				AccountRuntimeKey: runtimeKey,
				RouteScopeKey:     routeScopeKey,
				ConfigurationTier: gatewayhotquality.GatewayAccountConfigurationTier{
					ModelMatchRank:       chainHotQualityModelRank(view, rankByAccountID),
					FallbackEnabled:      view.FallbackEnabled,
					SuperPriorityEnabled: view.SuperPriorityEnabled,
					Priority:             view.Priority,
				},
				StableBindingOrder: index,
				LatencyDegraded:    degraded[view.ID],
			}
			if snapshot != nil {
				selectionView := snapshot.SelectionView()
				candidate.HotQuality = &selectionView
			}
			candidates = append(candidates, candidate)
		}
		decision, err := gatewayhotquality.DecideHotQualityCandidateOrderOnly(
			gatewayhotquality.DecideHotQualityCandidateInput[gatewayhotquality.HotQualityCandidate]{
				Mode:          input.Mode,
				RouteScopeKey: routeScopeKey,
				Candidates:    candidates,
				Base: func(candidate gatewayhotquality.HotQualityCandidate) gatewayhotquality.HotQualityCandidate {
					return candidate
				},
				CacheRates:     input.CacheRates,
				CacheRateStale: input.CacheRateStale,
			})
		if err != nil {
			return gatewaydispatch.HotQualityOrder{}, err
		}
		// OrderedCandidates（HotQualityCandidate）按 runtime key 映射回
		// dispatch 候选（与 orderedPayloadsFromDecision 同语义；组内一一
		// 对应，缺映射即装配缺陷，报错而非静默丢候选）。
		byRuntimeKey := make(map[string]gatewaydispatch.AccountCandidate, len(group.accounts))
		for index, candidate := range candidates {
			byRuntimeKey[candidate.AccountRuntimeKey] = group.accounts[index]
		}
		for _, orderedCandidate := range decision.OrderedCandidates {
			account, ok := byRuntimeKey[orderedCandidate.AccountRuntimeKey]
			if !ok {
				return gatewaydispatch.HotQualityOrder{}, fmt.Errorf("热质量纯排序候选缺少账号 %s", orderedCandidate.AccountRuntimeKey)
			}
			ordered = append(ordered, account)
		}
		// 首协议组的解释即决策级事实（与 OrderGatewayAccountsByHotQuality
		// 的 firstResult 口径一致）。
		if explanation == nil {
			first := decision.Explanation
			explanation = &first
		}
	}
	if ordered == nil {
		ordered = input.Accounts
	}
	return gatewaydispatch.HotQualityOrder{Accounts: ordered, Explanation: explanation}, nil
}

// chainHotQualityProfileGroup 是协议 profile 分组（与 runtime.go 的
// groupByProtocolProfile 同语义：首现序，缺省 profile =
// protocolCode:protocolVersion）。
type chainHotQualityProfileGroup struct {
	profile  string
	accounts []gatewaydispatch.AccountCandidate
}

func chainHotQualityGroupByProtocolProfile(accounts []gatewaydispatch.AccountCandidate) []chainHotQualityProfileGroup {
	var groups []chainHotQualityProfileGroup
	indexByProfile := map[string]int{}
	for _, account := range accounts {
		view := chainHotQualityAccountViewOf(account)
		profile := view.ProviderProtocolProfileID
		if strings.TrimSpace(profile) == "" {
			profile = view.ProtocolCode + ":" + view.ProtocolVersion
		}
		if index, ok := indexByProfile[profile]; ok {
			groups[index].accounts = append(groups[index].accounts, account)
			continue
		}
		indexByProfile[profile] = len(groups)
		groups = append(groups, chainHotQualityProfileGroup{profile: profile, accounts: []gatewaydispatch.AccountCandidate{account}})
	}
	return groups
}

// chainHotQualityRouteScopeParts 是路由范围键的字段（与
// gatewayhotquality.GatewayHotQualityRouteScopeKey 的入参同形；该入参类型
// 未导出，组合根侧无法直接构造，故此处按同构重建）。
type chainHotQualityRouteScopeParts struct {
	SystemAccountID string
	RouteStrategyID string
	GroupID         string
	ProtocolProfile string
	RequestLane     string
}

// chainHotQualityRouteScopeKey 重建 gatewayHotQualityRouteScopeKey 的键编码
// （len-prefixed '|' 拼接）。键值只在单次排序调用内做一致性校验（候选
// RouteScopeKey 与输入必须一致）且不进任何契约输出（决策摘要不投影该键），
// 因此与内部编码的格式演进解耦：即使两侧格式漂移，重排序内部的键恒自洽，
// 排序结果不受影响。空 system/group/profile 按 runtimeRequiredKey 同语义
// 报错（与 OrderAsync 路径的既有失败面一致）。
func chainHotQualityRouteScopeKey(parts chainHotQualityRouteScopeParts) (string, error) {
	normalize := func(value, name string) (string, error) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return "", fmt.Errorf("%s不能为空", name)
		}
		return trimmed, nil
	}
	systemAccountID, err := normalize(parts.SystemAccountID, "systemAccountId")
	if err != nil {
		return "", err
	}
	groupID, err := normalize(parts.GroupID, "groupId")
	if err != nil {
		return "", err
	}
	protocolProfile, err := normalize(parts.ProtocolProfile, "protocolProfile")
	if err != nil {
		return "", err
	}
	routeStrategyID := strings.TrimSpace(parts.RouteStrategyID)
	if routeStrategyID == "" {
		routeStrategyID = "direct"
	}
	return chainHotQualityEncodedScopeKey([]string{systemAccountID, routeStrategyID, groupID, protocolProfile, parts.RequestLane}), nil
}

// chainHotQualityEncodedScopeKey 与 gatewayhotquality 的 encodedScopeKey 同构
// （见 chainHotQualityRouteScopeKey 注释）。
func chainHotQualityEncodedScopeKey(parts []string) string {
	encoded := make([]string, len(parts))
	for index, part := range parts {
		encoded[index] = fmt.Sprintf("%d:%s", len(part), part)
	}
	return strings.Join(encoded, "|")
}

// chainHotQualityModelRankMaxSafeInteger 与 gatewayhotquality 的
// maxSafeInteger 同值（该常量未导出；同源同义，注释互指防漂移）。
const chainHotQualityModelRankMaxSafeInteger = int64(1)<<53 - 1

// chainHotQualityModelRank 与 runtime.go 的 modelRank 同构：无秩/越秩回 3。
func chainHotQualityModelRank(account gatewayhotquality.GatewayHotQualityAccountView, rankByAccountID map[string]int) int {
	value, ok := rankByAccountID[account.ID]
	if !ok {
		return 3
	}
	if value < 0 || int64(value) > chainHotQualityModelRankMaxSafeInteger {
		return 3
	}
	return value
}
