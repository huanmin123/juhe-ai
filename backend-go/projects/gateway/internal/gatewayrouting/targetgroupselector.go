package gatewayrouting

import (
	"context"
	"sort"
)

// GroupKindSuper 是 groups.group_kind 的超级分组值（超级分组设计 §3.1）。
const GroupKindSuper = "super"

// ModelTargetGroupCandidate mirrors GatewayModelTargetGroupCandidate.
type ModelTargetGroupCandidate struct {
	Binding     GroupBindingRow
	GroupAccess GroupUsageAccessMetadata
	Accounts    []UpstreamAccount
	ModelFilter GatewayModelAccountFilterResult
	// GroupSegments 仅超级分组目标非空：按成员组携带展开后的候选片段，
	// 组合根据此复用逐片段重水化与成员级配额门（设计 §4.2）。
	GroupSegments []NormalRouteGroupSegment
}

// ModelTargetGroupSelection mirrors GatewayModelTargetGroupSelection.
type ModelTargetGroupSelection struct {
	ModelTargetGroupCandidate

	GroupID                    string
	ResponseInspectionPolicies []ResponseInspectionPolicySummary
	// SkippedSuperGroupIDs 是选中目标之前被评估为空候选的超级分组绑定。
	// 外层目标分类据此把未产生候选的超级目标记为 no_candidate（设计 §4.2），
	// 不包含选中目标之后尚未评估的绑定。
	SkippedSuperGroupIDs []string
}

// ModelTargetGroupInput mirrors selectGatewayModelTargetGroup's input.
type ModelTargetGroupInput struct {
	Request                    RequestView
	APIKeyRecord               *APIKeyRow
	Bindings                   []GroupBindingRow
	TargetModel                string
	RequestClientCompatibility string
	// AcceptCandidate mirrors input.acceptCandidate; nil accepts everything.
	AcceptCandidate func(candidate ModelTargetGroupCandidate) bool
	// CandidatePriority mirrors input.candidatePriority; when nil the first
	// viable candidate is returned immediately (Node `if
	// (!input.candidatePriority) return selection`).
	CandidatePriority func(candidate ModelTargetGroupCandidate) float64
}

// TargetGroupSelector mirrors selectGatewayModelTargetGroup: walk the
// (already dispatch-ordered) bindings, keep the first/priority-best group
// whose cached accounts survive the capability and model filters.
type TargetGroupSelector struct {
	RuntimeCache     RuntimeCacheReader
	CapabilityFilter AccountCapabilityFilter
}

// SelectGatewayModelTargetGroup mirrors selectGatewayModelTargetGroup.
func (s *TargetGroupSelector) SelectGatewayModelTargetGroup(ctx context.Context, input ModelTargetGroupInput) (*ModelTargetGroupSelection, error) {
	sourceEndpointFamily := input.Request.requestEndpointFamily()
	bindings := uniqueGatewayGroupBindings(input.Bindings)
	var selected *ModelTargetGroupSelection
	selectedPriority := negInf()
	skippedSupers := make([]string, 0)
	for _, binding := range bindings {
		candidate, ok, err := s.resolveModelTargetGroupCandidate(ctx, input, binding, sourceEndpointFamily)
		if err != nil {
			return nil, err
		}
		if !ok {
			if binding.GroupKind == GroupKindSuper && binding.GroupEnabled != 0 {
				skippedSupers = append(skippedSupers, binding.GroupID)
			}
			continue
		}
		if input.AcceptCandidate != nil && !input.AcceptCandidate(candidate) {
			continue
		}
		selection := &ModelTargetGroupSelection{
			ModelTargetGroupCandidate:  candidate,
			GroupID:                    binding.GroupID,
			ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
		}
		if input.CandidatePriority == nil {
			selection.SkippedSuperGroupIDs = skippedSuperGroupsBefore(bindings, skippedSupers, binding.GroupID)
			return selection, nil
		}
		priority := input.CandidatePriority(candidate)
		if priority > selectedPriority {
			selected = selection
			selectedPriority = priority
		}
	}
	if selected != nil {
		selected.SkippedSuperGroupIDs = skippedSuperGroupsBefore(bindings, skippedSupers, selected.GroupID)
	}
	return selected, nil
}

// skippedSuperGroupsBefore 只保留绑定序中位于选中目标之前的跳过项：选中目标
// 之后尚未评估的绑定不属于本请求的目标快照（设计 §4.2）。
func skippedSuperGroupsBefore(bindings []GroupBindingRow, skipped []string, selectedGroupID string) []string {
	if len(skipped) == 0 {
		return nil
	}
	selectedIndex := len(bindings)
	for index, binding := range bindings {
		if binding.GroupID == selectedGroupID {
			selectedIndex = index
			break
		}
	}
	before := make(map[string]struct{}, selectedIndex)
	for index := 0; index < selectedIndex; index++ {
		before[bindings[index].GroupID] = struct{}{}
	}
	out := make([]string, 0, len(skipped))
	for _, groupID := range skipped {
		if _, ok := before[groupID]; ok {
			out = append(out, groupID)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CollectGatewayModelGroupSegments walks the same per-group skeleton as
// SelectGatewayModelTargetGroup but keeps every group's surviving candidate
// instead of selecting one group. 超级分组目标的成员展开即经此入口产出
// GroupSegments（fixture 也用它直接观察候选层）。Order follows the (already
// dispatch-ordered) bindings; groups without access metadata or without an
// account surviving the capability and model filters are omitted.
func (s *TargetGroupSelector) CollectGatewayModelGroupSegments(ctx context.Context, input ModelTargetGroupInput) ([]ModelTargetGroupCandidate, error) {
	sourceEndpointFamily := input.Request.requestEndpointFamily()
	candidates := make([]ModelTargetGroupCandidate, 0, len(input.Bindings))
	for _, binding := range uniqueGatewayGroupBindings(input.Bindings) {
		candidate, ok, err := s.resolveModelTargetGroupCandidate(ctx, input, binding, sourceEndpointFamily)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// resolveModelTargetGroupCandidate resolves one binding group into a
// ModelTargetGroupCandidate through the shared walk: access metadata →
// cached accounts → capability filter → model filter. ok=false mirrors the
// selector's per-group `continue` branches.
func (s *TargetGroupSelector) resolveModelTargetGroupCandidate(ctx context.Context, input ModelTargetGroupInput, binding GroupBindingRow, sourceEndpointFamily string) (ModelTargetGroupCandidate, bool, error) {
	if binding.GroupKind == GroupKindSuper {
		return s.resolveSuperGroupMemberCandidate(ctx, input, binding, sourceEndpointFamily)
	}
	groupAccess, found, err := s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID)
	if err != nil {
		return ModelTargetGroupCandidate{}, false, err
	}
	if !found {
		return ModelTargetGroupCandidate{}, false, nil
	}
	accounts, err := s.RuntimeCache.ListCachedOpenAIAccountsForGroupAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID, CachedAccountsForGroupOptions{
		RequestedModel:          input.TargetModel,
		RequestedEndpointFamily: sourceEndpointFamily,
	})
	if err != nil {
		return ModelTargetGroupCandidate{}, false, err
	}
	if len(accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	capabilityFilter := s.CapabilityFilter.FilterAccountsByRequestCapability(ctx, accounts, CapabilityFilterInput{
		RequestModel:               input.TargetModel,
		RequestClientCompatibility: input.RequestClientCompatibility,
	})
	if len(capabilityFilter.Accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	modelFilter := FilterAccountsByRequestedModel(capabilityFilter.Accounts, input.TargetModel, sourceEndpointFamily)
	if len(modelFilter.Accounts) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	return ModelTargetGroupCandidate{
		Binding:     binding,
		GroupAccess: groupAccess,
		Accounts:    modelFilter.Accounts,
		ModelFilter: modelFilter,
	}, true, nil
}

type superGroupAccountChoice struct {
	rank        int
	memberIndex int
	groupID     string
	account     UpstreamAccount
}

// resolveSuperGroupMemberCandidate 展开超级分组目标（超级分组设计 §4.2）：
// 逐 enabled 成员执行与单组相同的分组访问、账户、能力与模型过滤，再按
// route_rank ASC -> member_priority ASC -> member_group_id ASC -> account_id
// ASC 去重排序（同一账号保留最小出现），片段按最终候选序携带成员组。
// 成员顺序只在既有调度比较器完全相等时决胜的最终语义仍由下游 dispatch
// comparator 承接；本层保证 direct(0) 先于 mapping(1) 与成员组归属稳定。
func (s *TargetGroupSelector) resolveSuperGroupMemberCandidate(ctx context.Context, input ModelTargetGroupInput, binding GroupBindingRow, sourceEndpointFamily string) (ModelTargetGroupCandidate, bool, error) {
	if len(binding.MemberGroupIDs) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}
	superAccess, found, err := s.RuntimeCache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, binding.GroupID, input.APIKeyRecord.SystemAccountID)
	if err != nil {
		return ModelTargetGroupCandidate{}, false, err
	}
	if !found {
		return ModelTargetGroupCandidate{}, false, nil
	}
	type memberCandidate struct {
		index     int
		candidate ModelTargetGroupCandidate
	}
	members := make([]memberCandidate, 0, len(binding.MemberGroupIDs))
	for index, memberGroupID := range binding.MemberGroupIDs {
		memberGroupID = trimModel(memberGroupID)
		if memberGroupID == "" || memberGroupID == binding.GroupID {
			continue
		}
		memberBinding := GroupBindingRow{
			ID:              binding.ID + ":" + memberGroupID,
			APIKeyID:        binding.APIKeyID,
			SystemAccountID: binding.SystemAccountID,
			GroupID:         memberGroupID,
			Priority:        int64(index),
			Weight:          binding.Weight,
			Status:          RowStatusActive,
			GroupEnabled:    1,
		}
		candidate, ok, resolveErr := s.resolveModelTargetGroupCandidate(ctx, input, memberBinding, sourceEndpointFamily)
		if resolveErr != nil {
			return ModelTargetGroupCandidate{}, false, resolveErr
		}
		if !ok {
			continue
		}
		members = append(members, memberCandidate{index: index, candidate: candidate})
	}
	if len(members) == 0 {
		return ModelTargetGroupCandidate{}, false, nil
	}

	best := make(map[string]superGroupAccountChoice)
	for _, member := range members {
		for _, account := range member.candidate.Accounts {
			// 方案B（2026-10-24）：候选 Accounts 只含 rank 0（direct + 同族改名
			// 映射，同层）与 rank 1（跨协议转换映射）；rank 2（不支持）不进候选，
			// 钳位 `!=0 -> 1` 与新语义等价，比较器不动。
			rank := member.candidate.ModelFilter.ModelPriority.RankByAccountID[account.ID]
			if rank != 0 {
				rank = 1
			}
			choice := superGroupAccountChoice{rank: rank, memberIndex: member.index, groupID: member.candidate.Binding.GroupID, account: account}
			current, exists := best[account.ID]
			if !exists || superGroupAccountChoiceLess(choice, current) {
				best[account.ID] = choice
			}
		}
	}
	choices := make([]superGroupAccountChoice, 0, len(best))
	for _, choice := range best {
		choices = append(choices, choice)
	}
	sort.SliceStable(choices, func(i, j int) bool { return superGroupAccountChoiceLess(choices[i], choices[j]) })

	accessByGroup := make(map[string]GroupUsageAccessMetadata, len(members))
	for _, member := range members {
		accessByGroup[member.candidate.Binding.GroupID] = member.candidate.GroupAccess
	}
	segmentIndex := make(map[string]int, len(members))
	segments := make([]NormalRouteGroupSegment, 0, len(members))
	pooled := make([]UpstreamAccount, 0, len(choices))
	rankByAccountID := make(map[string]int, len(choices))
	// 方案B（2026-10-24）：rank 0 = direct + 同族改名映射（同层），rank 1 =
	// 跨协议转换映射。结构体字段名 DirectMatchedCount/MappingMatchedCount 是
	// 既有消费契约不改；本地计数按 rank 桶命名以免误导。
	rank0Count, rank1Count := 0, 0
	for _, choice := range choices {
		pooled = append(pooled, choice.account)
		rankByAccountID[choice.account.ID] = choice.rank
		if choice.rank == 0 {
			rank0Count++
		} else {
			rank1Count++
		}
		index, exists := segmentIndex[choice.groupID]
		if !exists {
			index = len(segments)
			segmentIndex[choice.groupID] = index
			segments = append(segments, NormalRouteGroupSegment{GroupID: choice.groupID, GroupAccess: accessByGroup[choice.groupID]})
		}
		segments[index].Accounts = append(segments[index].Accounts, choice.account)
	}
	return ModelTargetGroupCandidate{
		Binding:     binding,
		GroupAccess: superAccess,
		Accounts:    pooled,
		ModelFilter: GatewayModelAccountFilterResult{
			Accounts:             pooled,
			DirectMatchedCount:   rank0Count,
			MappingMatchedCount:  rank1Count,
			RequestedModel:       input.TargetModel,
			SourceEndpointFamily: sourceEndpointFamily,
			ModelPriority: GatewayAccountModelPriority{
				RequestedModel:       input.TargetModel,
				SourceEndpointFamily: sourceEndpointFamily,
				RankByAccountID:      rankByAccountID,
			},
		},
		GroupSegments: segments,
	}, true, nil
}

func superGroupAccountChoiceLess(left, right superGroupAccountChoice) bool {
	if left.rank != right.rank {
		return left.rank < right.rank
	}
	if left.memberIndex != right.memberIndex {
		return left.memberIndex < right.memberIndex
	}
	if left.groupID != right.groupID {
		return left.groupID < right.groupID
	}
	return left.account.ID < right.account.ID
}

// uniqueGatewayGroupBindings mirrors uniqueGatewayGroupBindings: drop empty
// group ids and duplicates, preserving first-seen order.
func uniqueGatewayGroupBindings(bindings []GroupBindingRow) []GroupBindingRow {
	seen := make(map[string]struct{}, len(bindings))
	unique := make([]GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		if binding.GroupID == "" {
			continue
		}
		if _, ok := seen[binding.GroupID]; ok {
			continue
		}
		seen[binding.GroupID] = struct{}{}
		unique = append(unique, binding)
	}
	return unique
}
