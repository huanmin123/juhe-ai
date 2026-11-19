// Package keyipmigration 实现「单 IP 并发限制从分组迁至 API Key」（API Key
// 单 IP 并发限制设计 §4，schema revision 25）的 maintenance 迁移内核：
//
//   - BuildPlan（只读盘点）：扫描 juhe_business.api_keys / route_strategies /
//     route_strategy_groups / groups / super_group_members，按 Key 的路由策略
//     解析可达分组（全量绑定行，不过滤 status/enabled；超级分组展开全部成员），
//     收集旧分组策略 clientIpConcurrencyLimit 正值，产出 dry-run manifest
//     （逐 Key 目标值、来源分组值集合、冲突行、分组重写/超级分组重算计数、
//     needs_review 明细）；
//   - Apply：单个业务库事务内 SET LOCAL lock_timeout='2s' 后重新盘点并写入；
//     任一 needs_review 行即整体阻断（ErrNeedsReview，CLI 退出码 3；upgrade
//     步骤由 Verify 复查处以 exit 3），零写入；不更新 updated_at；
//   - Satisfied / VerifyPostflight：完成态谓词（分组策略无
//     clientIpConcurrencyLimit、单键/ NULL 形状合法、所有 Key 值在 0..1000000、
//     超级分组自身行与首成员一致），供 CLI postflight 与 upgrade 步骤同源使用。
//
// preexisting 语义（2026-11-19 裁决补充）：只对当前
// client_ip_concurrency_limit = 0 的 Key 写入推导值；当前值 >0 的 Key 视为
// 人工裁决/存量值，等于任一观测正值即保留且不阻断，否则按
// existing_key_limit_conflict 处置（block 阻断 / min 保留并记冲突）。
package keyipmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNeedsReview 是 Apply 被 needs_review 行阻断的哨兵错误（CLI 据此退出码 3）。
var ErrNeedsReview = errors.New("存在 needs_review 行，迁移被阻断（人工确认后再迁移）")

// RollbackNote 是报告固定的回滚口径（设计 §4.3）。
const RollbackNote = "回滚 = 恢复升级前备份并回退旧镜像；单键分组策略与 api_keys 新列使旧二进制无法直接启动。"

// Options 是 Plan/Apply 的注入参数。
type Options struct {
	// ConflictPolicy 是多个不同旧值时 block（默认）或 min。
	ConflictPolicy ConflictPolicy
	// DryRun 只影响 manifest 的 dryRun 字段（BuildPlan 本身全程只读）。
	DryRun bool
}

// KeyPlan 是单个 API Key 的迁移计划/manifest 条目。
type KeyPlan struct {
	KeyID           string        `json:"keyId"`
	OwnerID         string        `json:"systemAccountId,omitempty"`
	Name            string        `json:"name,omitempty"`
	RouteStrategyID string        `json:"routeStrategyId,omitempty"`
	CurrentValue    int           `json:"currentValue"`
	TargetValue     int           `json:"targetValue"`
	ObservedValues  []int         `json:"observedGroupValues,omitempty"`
	SourceGroupIDs  []string      `json:"sourceGroupIds,omitempty"`
	SourceGroups    []SourceGroup `json:"sourceGroups,omitempty"`
	Action          string        `json:"action"`
	Reason          string        `json:"reason,omitempty"`
	Notes           []string      `json:"notes,omitempty"`
}

// SourceGroup 是一个 Key 可达的标准分组及其运行态事实。viaSuperGroupId 非空
// 表示经由超级分组展开；runtimeActive = 绑定 active && 分组 enabled &&
// （超级分组时）成员 enabled——迁移只用于 manifest 标注，取值收集不过滤。
type SourceGroup struct {
	GroupID                  string `json:"groupId"`
	ViaSuperGroupID          string `json:"viaSuperGroupId,omitempty"`
	BindingID                string `json:"bindingId,omitempty"`
	BindingStatus            string `json:"bindingStatus,omitempty"`
	RuntimeActive            bool   `json:"runtimeActive"`
	ClientIPConcurrencyLimit *int   `json:"clientIpConcurrencyLimit,omitempty"`
	Note                     string `json:"note,omitempty"`
}

// GroupPlan 是单个分组的迁移计划/manifest 条目。
type GroupPlan struct {
	GroupID        string          `json:"groupId"`
	OwnerID        string          `json:"systemAccountId,omitempty"`
	Name           string          `json:"name,omitempty"`
	GroupKind      string          `json:"groupKind"`
	CurrentType    string          `json:"currentGroupType"`
	CurrentPolicy  string          `json:"currentPolicy,omitempty"`
	TargetType     string          `json:"targetGroupType"`
	TargetPolicy   json.RawMessage `json:"targetPolicy,omitempty"`
	Action         string          `json:"action"`
	Reason         string          `json:"reason,omitempty"`
	Notes          []string        `json:"notes,omitempty"`
	MemberGroupIDs []string        `json:"memberGroupIds,omitempty"`
	FirstMemberID  string          `json:"firstMemberId,omitempty"`
	targetNull     bool
}

// ConflictRow 是旧值冲突行（min 收敛或 preexisting 冲突留档）。
type ConflictRow struct {
	KeyID          string `json:"keyId"`
	OwnerID        string `json:"systemAccountId,omitempty"`
	ObservedValues []int  `json:"observedGroupValues"`
	CurrentValue   int    `json:"currentValue"`
	ChosenValue    int    `json:"chosenValue"`
	Kind           string `json:"kind"`
	Resolution     string `json:"resolution"`
}

// ReviewRow 是 needs_review 明细。
type ReviewRow struct {
	Target string `json:"target"` // key | group
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Plan 是一次盘点/执行的完整报告（dry-run manifest 与 apply 报告同形状）。
type Plan struct {
	Mode           string `json:"mode"` // plan | apply
	Database       string `json:"database"`
	DryRun         bool   `json:"dryRun"`
	ConflictPolicy string `json:"conflictPolicy"`
	KeysTotal      int    `json:"keysTotal"`
	// KeyColumnPresent 报告 api_keys.client_ip_concurrency_limit 是否已存在。
	// dry-run 可在 upgrade 加法相之前执行（缺列按 current=0 盘点）；apply 缺列
	// 直接失败，要求先执行 upgrade/ensure-schema。
	KeyColumnPresent      bool `json:"keyColumnPresent"`
	KeysUpdated           int  `json:"keysUpdated"`
	KeysUnchanged         int  `json:"keysUnchanged"`
	KeysPreexisting       int  `json:"keysPreexisting"`
	GroupsTotal           int  `json:"groupsTotal"`
	GroupsRewritten       int  `json:"groupsRewritten"`
	GroupPolicyRewrites   int  `json:"groupPolicyRewrites"`
	GroupsUnchanged       int  `json:"groupsUnchanged"`
	SuperGroupsTotal      int  `json:"superGroupsTotal"`
	SuperGroupsRecomputed int  `json:"superGroupsRecomputed"`
	EmptySuperGroups      int  `json:"emptySuperGroups"`
	// Applied 是 apply 模式实际 UPDATE 行数（dry-run 恒 0；完成态幂等重跑为 0）。
	Applied     int           `json:"appliedCount"`
	Conflicts   []ConflictRow `json:"conflicts,omitempty"`
	Keys        []KeyPlan     `json:"keys"`
	Groups      []GroupPlan   `json:"groups"`
	NeedsReview []ReviewRow   `json:"needsReview,omitempty"`
	// Blocked 是进入 needs_review 的 Key/分组行数（明细见 NeedsReview）。
	Blocked  int      `json:"blockedCount"`
	Warnings []string `json:"warnings,omitempty"`
	Rollback string   `json:"rollbackNote"`
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type scannedGroup struct {
	id        string
	ownerID   string
	name      string
	groupKind string
	groupType string
	policy    sql.NullString
	enabled   bool
}

type scannedMember struct {
	superGroupID  string
	memberGroupID string
	priority      int
	enabled       bool
}

type scannedBinding struct {
	id         string
	strategyID string
	ownerID    string
	groupID    string
	status     string
}

type scannedKey struct {
	id         string
	ownerID    string
	strategyID string
	name       string
	current    int
}

type groupTarget struct {
	targetType string
	soft       int
	null       bool
}

// BuildPlan 只读盘点并返回迁移计划。
func BuildPlan(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	return planWith(ctx, db, options)
}

// Apply 在单个业务库事务内执行迁移：锁超时 → 重新盘点 → needs_review 阻断 →
// 逐行 UPDATE（不触 updated_at）→ 提交。存在 needs_review 行时零写入并返回
// 带报告与 ErrNeedsReview（manifest 仍完整，供人工复核）。
func Apply(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, fmt.Errorf("开启迁移事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// 停写失效时快速失败：锁等待超过 2s 即中止，避免维护窗口长时间阻塞。
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return Plan{}, fmt.Errorf("设置事务锁超时: %w", err)
	}
	plan, err := planWith(ctx, tx, options)
	if err != nil {
		return Plan{}, err
	}
	plan.Mode = "apply"
	plan.DryRun = false
	if plan.Blocked > 0 {
		return plan, ErrNeedsReview
	}
	if !plan.KeyColumnPresent {
		return plan, errors.New("缺少 api_keys.client_ip_concurrency_limit 列；请先执行 upgrade（或 --ensure-schema）加法相再迁移")
	}
	for index := range plan.Keys {
		key := &plan.Keys[index]
		if key.Action != ActionUpdate {
			continue
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE juhe_business.api_keys SET client_ip_concurrency_limit = $1 WHERE id = $2`,
			key.TargetValue, key.KeyID)
		if err != nil {
			return Plan{}, fmt.Errorf("迁移 API Key %s 失败: %w", key.KeyID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Plan{}, fmt.Errorf("读取 API Key %s 写入行数失败: %w", key.KeyID, err)
		}
		if affected != 1 {
			return Plan{}, fmt.Errorf("迁移 API Key %s 命中 %d 行（停写窗口内被并发改写？）", key.KeyID, affected)
		}
		plan.Applied++
	}
	for index := range plan.Groups {
		group := &plan.Groups[index]
		if group.Action != ActionUpdate {
			continue
		}
		var policyArg any
		if !group.targetNull {
			policyArg = string(group.TargetPolicy)
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE juhe_business.groups SET group_type = $1, scheduling_policy_json = $2 WHERE id = $3`,
			group.TargetType, policyArg, group.GroupID)
		if err != nil {
			return Plan{}, fmt.Errorf("迁移分组 %s 失败: %w", group.GroupID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Plan{}, fmt.Errorf("读取分组 %s 写入行数失败: %w", group.GroupID, err)
		}
		if affected != 1 {
			return Plan{}, fmt.Errorf("迁移分组 %s 命中 %d 行（停写窗口内被并发改写？）", group.GroupID, affected)
		}
		plan.Applied++
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, fmt.Errorf("提交迁移事务: %w", err)
	}
	return plan, nil
}

func planWith(ctx context.Context, q querier, options Options) (Plan, error) {
	policy, ok := NormalizeConflictPolicy(string(options.ConflictPolicy))
	if !ok {
		return Plan{}, fmt.Errorf("未知 conflict policy %q", options.ConflictPolicy)
	}
	plan := Plan{
		Mode:           "plan",
		DryRun:         options.DryRun,
		ConflictPolicy: string(policy),
		Keys:           []KeyPlan{},
		Groups:         []GroupPlan{},
		NeedsReview:    []ReviewRow{},
		Rollback:       RollbackNote,
	}
	var database string
	if err := q.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return Plan{}, fmt.Errorf("读取目标数据库名: %w", err)
	}
	plan.Database = database

	groups, err := scanGroups(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	members, err := scanMembers(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	strategies, err := scanStrategies(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	bindings, err := scanBindings(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	hasKeyColumn, err := columnExistsQ(ctx, q, "juhe_business", "api_keys", "client_ip_concurrency_limit")
	if err != nil {
		return Plan{}, err
	}
	plan.KeyColumnPresent = hasKeyColumn
	keys, err := scanKeys(ctx, q, hasKeyColumn)
	if err != nil {
		return Plan{}, err
	}
	plan.KeysTotal = len(keys)
	plan.GroupsTotal = len(groups)

	groupIndex := make(map[string]scannedGroup, len(groups))
	membersBySuper := map[string][]scannedMember{}
	for _, group := range groups {
		groupIndex[group.id] = group
	}
	for _, member := range members {
		membersBySuper[member.superGroupID] = append(membersBySuper[member.superGroupID], member)
	}

	// 第一轮：standard 分组解析与目标策略（单键化）。畸形行只报告不改写。
	standardTargets := make(map[string]groupTarget, len(groups))
	for _, group := range groups {
		if group.groupKind == "super" {
			continue
		}
		if group.groupKind != "" && group.groupKind != "standard" {
			plan.blockGroup(group, ReviewUnknownGroupKind,
				"未知 group_kind "+group.groupKind)
			continue
		}
		parsed := ParseStoredPolicy(group.policy)
		if parsed.Reason != "" {
			plan.blockGroup(group, parsed.Reason, group.CurrentPolicyText())
			continue
		}
		target := groupTarget{targetType: group.groupType, soft: parsed.Soft, null: parsed.Soft == -1}
		standardTargets[group.id] = target
		plan.recordStandardGroup(group, target)
	}

	// 第二轮：超级分组自身行按首成员重算；成员阈值必须一致（不论 enabled）。
	for _, group := range groups {
		if group.groupKind != "super" {
			continue
		}
		plan.SuperGroupsTotal++
		memberRows := membersBySuper[group.id]
		if len(memberRows) == 0 {
			// 空超级分组是正常状态：收敛 personal + NULL（设计 §2.5/§4.2-6）。
			plan.EmptySuperGroups++
			plan.recordSuperGroup(group, groupTarget{targetType: "personal", soft: -1, null: true}, nil, "")
			continue
		}
		var memberIDs []string
		softSet := map[int]struct{}{}
		blocked := false
		for _, member := range memberRows {
			memberIDs = append(memberIDs, member.memberGroupID)
			memberGroup, found := groupIndex[member.memberGroupID]
			if !found {
				plan.blockSuperGroup(group, ReviewSuperGroupMemberMissing,
					"成员 "+member.memberGroupID+" 行不存在（悬挂引用）")
				blocked = true
				break
			}
			if memberGroup.groupKind == "super" {
				plan.blockSuperGroup(group, ReviewNestedSuperMember,
					"成员 "+member.memberGroupID+" 仍是超级分组（只允许一层展开）")
				blocked = true
				break
			}
			if memberGroup.groupKind != "" && memberGroup.groupKind != "standard" {
				plan.blockSuperGroup(group, ReviewUnknownGroupKind,
					"成员 "+member.memberGroupID+" group_kind 未知")
				blocked = true
				break
			}
			target, found := standardTargets[member.memberGroupID]
			if !found {
				plan.blockSuperGroup(group, ReviewSuperGroupMemberBlocked,
					"成员 "+member.memberGroupID+" 策略需要人工复核")
				blocked = true
				break
			}
			softSet[target.soft] = struct{}{}
		}
		if blocked {
			continue
		}
		if len(softSet) != 1 {
			detail := "成员 defaultSoftConcurrency 不一致：" + joinSoftSet(softSet)
			plan.blockSuperGroup(group, ReviewSuperGroupMemberMismatch, detail)
			continue
		}
		first := firstMemberRow(memberRows)
		firstGroup := groupIndex[first.memberGroupID]
		target := standardTargets[first.memberGroupID]
		plan.recordSuperGroup(group, groupTarget{
			targetType: firstGroup.groupType,
			soft:       target.soft,
			null:       target.soft == -1,
		}, memberIDs, first.memberGroupID)
	}

	// 第三轮：逐 Key 解析可达分组并决策。
	strategyIndex := make(map[string]scannedBindingStrategy, len(strategies))
	for _, strategy := range strategies {
		strategyIndex[strategy.id] = strategy
	}
	bindingsByStrategy := map[string][]scannedBinding{}
	for _, binding := range bindings {
		bindingsByStrategy[binding.strategyID] = append(bindingsByStrategy[binding.strategyID], binding)
	}
	for _, key := range keys {
		plan.recordKey(key, strategyIndex, bindingsByStrategy, groupIndex, membersBySuper, policy)
	}
	return plan, nil
}

// scannedBindingStrategy 是 route_strategies 的投影。
type scannedBindingStrategy struct {
	id      string
	ownerID string
}

// recordKey 解析单个 Key 的可达分组并生成决策。
func (p *Plan) recordKey(key scannedKey, strategies map[string]scannedBindingStrategy,
	bindings map[string][]scannedBinding, groups map[string]scannedGroup,
	membersBySuper map[string][]scannedMember, policy ConflictPolicy) {
	row := KeyPlan{
		KeyID:           key.id,
		OwnerID:         key.ownerID,
		Name:            key.name,
		RouteStrategyID: key.strategyID,
		CurrentValue:    key.current,
		TargetValue:     key.current,
	}
	strategy, found := strategies[key.strategyID]
	if !found {
		p.blockKey(row, ReviewRouteStrategyMissing, "route_strategy_id "+key.strategyID+" 不存在")
		return
	}
	if strategy.ownerID != key.ownerID {
		p.blockKey(row, ReviewOwnerMismatch,
			"Key owner "+key.ownerID+" 与路由策略 owner "+strategy.ownerID+" 不一致")
		return
	}
	var observed []int
	invalid := false
	seenGroup := map[string]struct{}{}
	for _, binding := range bindings[key.strategyID] {
		if binding.ownerID != key.ownerID {
			p.noteReview(ReviewOwnerMismatch,
				"Key "+key.id+" 绑定 "+binding.id+" owner "+binding.ownerID+" 与 Key owner 不一致")
			invalid = true
			continue
		}
		group, found := groups[binding.groupID]
		if !found {
			p.noteReview(ReviewGroupMissing, "Key "+key.id+" 绑定 "+binding.id+" 引用的分组 "+binding.groupID+" 不存在")
			invalid = true
			continue
		}
		// 分组 owner 与 Key owner 不同是合法授权形态：被授权人用自己的策略绑定
		// 授权分组，binding 行的 system_account_id 是 grantee，分组行是 grantor。
		// 设计 §4.2-1 要求按配置拓扑全量解析、不按授权状态过滤，不能在此阻断。
		if group.groupKind == "super" {
			memberRows := membersBySuper[group.id]
			if len(memberRows) == 0 {
				row.Notes = append(row.Notes, "超级分组 "+group.id+" 无成员行（无旧值可迁移）")
			}
			for _, member := range memberRows {
				memberGroup, found := groups[member.memberGroupID]
				if !found {
					p.noteReview(ReviewSuperGroupMemberMissing,
						"超级分组 "+group.id+" 成员 "+member.memberGroupID+" 行不存在")
					invalid = true
					continue
				}
				if memberGroup.groupKind == "super" {
					p.noteReview(ReviewNestedSuperMember,
						"超级分组 "+group.id+" 成员 "+memberGroup.id+" 仍是超级分组")
					invalid = true
					continue
				}
				// 成员分组 owner 可不同于 Key owner：超级分组经授权被被授权人
				// 绑定后，成员仍是授权方所有的分组；同样不按 owner 过滤。
				source := SourceGroup{
					GroupID:         memberGroup.id,
					ViaSuperGroupID: group.id,
					BindingID:       binding.id,
					BindingStatus:   binding.status,
					RuntimeActive:   binding.status == "active" && group.enabled && member.enabled,
				}
				parsed := ParseStoredPolicy(memberGroup.policy)
				if parsed.Reason != "" {
					source.Note = parsed.Reason
					p.noteReview(ReviewSourceGroupInvalid,
						"Key "+key.id+" 可达成员分组 "+memberGroup.id+" 策略需要人工复核（"+parsed.Reason+"）")
					invalid = true
				} else if parsed.HasIP {
					limit := parsed.IP
					source.ClientIPConcurrencyLimit = &limit
					if parsed.IP > 0 {
						observed = append(observed, parsed.IP)
					}
				}
				if _, seen := seenGroup[memberGroup.id]; !seen {
					seenGroup[memberGroup.id] = struct{}{}
					row.SourceGroupIDs = append(row.SourceGroupIDs, memberGroup.id)
				}
				row.SourceGroups = append(row.SourceGroups, source)
			}
			continue
		}
		source := SourceGroup{
			GroupID:       group.id,
			BindingID:     binding.id,
			BindingStatus: binding.status,
			RuntimeActive: binding.status == "active" && group.enabled,
		}
		parsed := ParseStoredPolicy(group.policy)
		if parsed.Reason != "" {
			source.Note = parsed.Reason
			p.noteReview(ReviewSourceGroupInvalid,
				"Key "+key.id+" 可达分组 "+group.id+" 策略需要人工复核（"+parsed.Reason+"）")
			invalid = true
		} else if parsed.HasIP {
			limit := parsed.IP
			source.ClientIPConcurrencyLimit = &limit
			if parsed.IP > 0 {
				observed = append(observed, parsed.IP)
			}
		}
		if _, seen := seenGroup[group.id]; !seen {
			seenGroup[group.id] = struct{}{}
			row.SourceGroupIDs = append(row.SourceGroupIDs, group.id)
		}
		row.SourceGroups = append(row.SourceGroups, source)
	}
	row.ObservedValues = distinctSorted(observed)
	if invalid {
		p.blockKey(row, ReviewSourceGroupInvalid,
			"可达分组存在需要人工复核的行（见 needsReview 明细与 sourceGroups.note）")
		return
	}
	decision := DecideKeyTarget(key.current, observed, policy)
	row.TargetValue = decision.Target
	row.Action = decision.Action
	row.Reason = decision.Reason
	row.Notes = append(row.Notes, decision.Notes...)
	if decision.Conflict != nil {
		conflict := *decision.Conflict
		conflict.KeyID = key.id
		conflict.OwnerID = key.ownerID
		p.Conflicts = append(p.Conflicts, conflict)
	}
	switch decision.Action {
	case ActionNeedsReview:
		p.NeedsReview = append(p.NeedsReview, ReviewRow{
			Target: "key", ID: key.id, Reason: decision.Reason,
			Detail: fmt.Sprintf("current=%d observed=%v", key.current, row.ObservedValues),
		})
		p.Blocked++
	case ActionUpdate:
		p.KeysUpdated++
	case ActionPreexisting:
		p.KeysPreexisting++
	default:
		p.KeysUnchanged++
	}
	p.Keys = append(p.Keys, row)
}

// recordStandardGroup 记录普通分组的目标策略并折叠为 unchanged/update。
func (p *Plan) recordStandardGroup(group scannedGroup, target groupTarget) {
	row := newGroupPlan(group, target)
	if group.groupType == target.targetType && singleKeyPolicyMatches(group.policy, target.soft) {
		row.Action = ActionUnchanged
		p.GroupsUnchanged++
		return
	}
	row.Action = ActionUpdate
	p.GroupsRewritten++
	p.GroupPolicyRewrites++
	p.Groups = append(p.Groups, row)
}

// recordSuperGroup 记录超级分组自身行的重算结果。
func (p *Plan) recordSuperGroup(group scannedGroup, target groupTarget, memberIDs []string, firstMemberID string) {
	row := newGroupPlan(group, target)
	row.MemberGroupIDs = memberIDs
	row.FirstMemberID = firstMemberID
	if group.groupType == target.targetType && singleKeyPolicyMatches(group.policy, target.soft) {
		row.Action = ActionUnchanged
		p.GroupsUnchanged++
		return
	}
	row.Action = ActionUpdate
	p.GroupsRewritten++
	p.SuperGroupsRecomputed++
	p.Groups = append(p.Groups, row)
}

func newGroupPlan(group scannedGroup, target groupTarget) GroupPlan {
	row := GroupPlan{
		GroupID:       group.id,
		OwnerID:       group.ownerID,
		Name:          group.name,
		GroupKind:     group.groupKind,
		CurrentType:   group.groupType,
		CurrentPolicy: group.CurrentPolicyText(),
		TargetType:    target.targetType,
	}
	if target.null {
		row.TargetPolicy = json.RawMessage("null")
		row.targetNull = true
	} else {
		row.TargetPolicy = json.RawMessage(SingleKeyPolicyJSON(target.soft))
	}
	return row
}

// blockGroup 记录普通分组的 needs_review 行（manifest 可见，apply 阻断）。
func (p *Plan) blockGroup(group scannedGroup, reason, detail string) {
	row := GroupPlan{
		GroupID:       group.id,
		OwnerID:       group.ownerID,
		Name:          group.name,
		GroupKind:     group.groupKind,
		CurrentType:   group.groupType,
		CurrentPolicy: group.CurrentPolicyText(),
		Action:        ActionNeedsReview,
		Reason:        reason,
	}
	p.Groups = append(p.Groups, row)
	p.NeedsReview = append(p.NeedsReview, ReviewRow{Target: "group", ID: group.id, Reason: reason, Detail: detail})
	p.Blocked++
}

// blockSuperGroup 记录超级分组的 needs_review 行。
func (p *Plan) blockSuperGroup(group scannedGroup, reason, detail string) {
	row := GroupPlan{
		GroupID:       group.id,
		OwnerID:       group.ownerID,
		Name:          group.name,
		GroupKind:     group.groupKind,
		CurrentType:   group.groupType,
		CurrentPolicy: group.CurrentPolicyText(),
		Action:        ActionNeedsReview,
		Reason:        reason,
	}
	p.Groups = append(p.Groups, row)
	p.NeedsReview = append(p.NeedsReview, ReviewRow{Target: "group", ID: group.id, Reason: reason, Detail: detail})
	p.Blocked++
}

// blockKey 记录 Key 的 needs_review 行。
func (p *Plan) blockKey(row KeyPlan, reason, detail string) {
	row.Action = ActionNeedsReview
	row.Reason = reason
	p.Keys = append(p.Keys, row)
	p.NeedsReview = append(p.NeedsReview, ReviewRow{Target: "key", ID: row.KeyID, Reason: reason, Detail: detail})
	p.Blocked++
}

// noteReview 追加一条不单独计数的明细（同一 Key 行由 blockKey 统一计数）。
func (p *Plan) noteReview(reason, detail string) {
	p.NeedsReview = append(p.NeedsReview, ReviewRow{Target: "key", Reason: reason, Detail: detail})
}

// CurrentPolicyText 返回策略列原文（NULL 为空串）。
func (g scannedGroup) CurrentPolicyText() string {
	if !g.policy.Valid {
		return ""
	}
	return g.policy.String
}

// firstMemberRow 返回超级分组自身行的重算基准：优先级最小的启用成员；
// 没有启用成员时回落优先级最小的成员（成员阈值已校验一致，取值不受影响）。
func firstMemberRow(members []scannedMember) scannedMember {
	if len(members) == 0 {
		return scannedMember{}
	}
	for _, member := range members {
		if member.enabled {
			return member
		}
	}
	return members[0]
}

func joinSoftSet(values map[int]struct{}) string {
	text := ""
	for value := range values {
		if text != "" {
			text += ","
		}
		text += fmt.Sprintf("%d", value)
	}
	return text
}

// ---------- 扫描 ----------

func scanGroups(ctx context.Context, q querier) ([]scannedGroup, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, system_account_id, name, group_kind, group_type, scheduling_policy_json, enabled
		FROM juhe_business.groups ORDER BY id`)
	if isUndefinedTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 groups 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedGroup
	for rows.Next() {
		var group scannedGroup
		var enabled int
		if err := rows.Scan(&group.id, &group.ownerID, &group.name, &group.groupKind,
			&group.groupType, &group.policy, &enabled); err != nil {
			return nil, fmt.Errorf("读取 groups 行失败: %w", err)
		}
		group.enabled = enabled == 1
		out = append(out, group)
	}
	return out, rows.Err()
}

func scanMembers(ctx context.Context, q querier) ([]scannedMember, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT super_group_id, member_group_id, priority, enabled
		FROM juhe_business.super_group_members
		ORDER BY super_group_id, priority ASC, created_at ASC, id ASC`)
	if isUndefinedTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 super_group_members 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedMember
	for rows.Next() {
		var member scannedMember
		var enabled int
		if err := rows.Scan(&member.superGroupID, &member.memberGroupID, &member.priority, &enabled); err != nil {
			return nil, fmt.Errorf("读取 super_group_members 行失败: %w", err)
		}
		member.enabled = enabled == 1
		out = append(out, member)
	}
	return out, rows.Err()
}

func scanStrategies(ctx context.Context, q querier) ([]scannedBindingStrategy, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, system_account_id FROM juhe_business.route_strategies ORDER BY id`)
	if isUndefinedTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 route_strategies 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedBindingStrategy
	for rows.Next() {
		var strategy scannedBindingStrategy
		if err := rows.Scan(&strategy.id, &strategy.ownerID); err != nil {
			return nil, fmt.Errorf("读取 route_strategies 行失败: %w", err)
		}
		out = append(out, strategy)
	}
	return out, rows.Err()
}

func scanBindings(ctx context.Context, q querier) ([]scannedBinding, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, route_strategy_id, system_account_id, group_id, status
		FROM juhe_business.route_strategy_groups
		ORDER BY route_strategy_id, priority ASC, created_at ASC, id ASC`)
	if isUndefinedTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 route_strategy_groups 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedBinding
	for rows.Next() {
		var binding scannedBinding
		if err := rows.Scan(&binding.id, &binding.strategyID, &binding.ownerID,
			&binding.groupID, &binding.status); err != nil {
			return nil, fmt.Errorf("读取 route_strategy_groups 行失败: %w", err)
		}
		out = append(out, binding)
	}
	return out, rows.Err()
}

func scanKeys(ctx context.Context, q querier, hasColumn bool) ([]scannedKey, error) {
	currentExpr := "0"
	if hasColumn {
		currentExpr = "client_ip_concurrency_limit"
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, system_account_id, route_strategy_id, name, `+currentExpr+`
		FROM juhe_business.api_keys ORDER BY id`)
	if isUndefinedTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 api_keys 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedKey
	for rows.Next() {
		var key scannedKey
		if err := rows.Scan(&key.id, &key.ownerID, &key.strategyID, &key.name, &key.current); err != nil {
			return nil, fmt.Errorf("读取 api_keys 行失败: %w", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// ---------- 完成态谓词（CLI postflight 与 upgrade Satisfied/Verify 同源） ----------

// VerifyReport 是完成态核验报告（全程零写入）。
type VerifyReport struct {
	Database              string   `json:"database"`
	KeyColumnPresent      bool     `json:"keyColumnPresent"`
	KeysTotal             int      `json:"keysTotal"`
	LegacyIPKeyGroups     int      `json:"legacyIpKeyGroups"`
	InvalidPolicyGroups   int      `json:"invalidPolicyGroups"`
	InvalidKeyLimitValues int      `json:"invalidKeyLimitValues"`
	SuperGroupMismatches  int      `json:"superGroupMismatches"`
	Examples              []string `json:"examples,omitempty"` // 前 10 条违规行说明
}

// Ready 报告完成态：分组策略无 clientIpConcurrencyLimit 且为单键/ NULL，
// 所有 Key 值非空且在 0..1000000，超级分组自身行与首成员一致。
func (r VerifyReport) Ready() bool {
	return r.KeyColumnPresent &&
		r.LegacyIPKeyGroups == 0 && r.InvalidPolicyGroups == 0 &&
		r.InvalidKeyLimitValues == 0 && r.SuperGroupMismatches == 0
}

// Satisfied 报告完成态谓词（upgrade 步骤 Satisfied 判据）。fresh 库语义：
// groups 或 api_keys 表尚未建立即「无可迁移数据」，谓词返回满足。
func Satisfied(ctx context.Context, db *sql.DB) (bool, error) {
	groupsExists, err := relationExists(ctx, db, "juhe_business", "groups")
	if err != nil {
		return false, err
	}
	if !groupsExists {
		return true, nil
	}
	keysExists, err := relationExists(ctx, db, "juhe_business", "api_keys")
	if err != nil {
		return false, err
	}
	if !keysExists {
		return true, nil
	}
	report, err := VerifyPostflight(ctx, db)
	if err != nil {
		return false, err
	}
	if !report.Ready() {
		return false, nil
	}
	// 完成态形状之外，needs_review 的拓扑/成员不一致同样必须阻断：例如超级
	// 分组自身行已与首成员一致、但成员之间阈值不一致且没有旧 IP 值时，
	// postflight 的列/策略/值检查会通过，却违反设计 §4.2-7「不静默选边」。
	// 同时要求盘点无待写入行（Key 待更新 / 分组待单键化），否则「形状看似
	// 完成、数据仍在 0」的库会被 upgrade 跳过。
	plan, err := BuildPlan(ctx, db, Options{ConflictPolicy: ConflictPolicyBlock})
	if err != nil {
		return false, err
	}
	return plan.Blocked == 0 && plan.KeysUpdated == 0 && plan.GroupsRewritten == 0, nil
}

// VerifyPostflight 只读核验完成态并返回明细。缺迁移面表直接报错，不把错库/
// 全新库按 0 行误报 Ready（fresh 库语义由 Satisfied 的表存在守卫承担）。
func VerifyPostflight(ctx context.Context, db *sql.DB) (VerifyReport, error) {
	report := VerifyReport{Examples: []string{}}
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&report.Database); err != nil {
		return report, fmt.Errorf("读取目标数据库名: %w", err)
	}
	for _, table := range []string{"groups", "api_keys"} {
		exists, err := relationExists(ctx, db, "juhe_business", table)
		if err != nil {
			return report, err
		}
		if !exists {
			return report, fmt.Errorf("目标库 %s 缺少 juhe_business.%s 表，请核对 --dsn 是否指向业务库", report.Database, table)
		}
	}
	hasColumn, err := columnExistsQ(ctx, db, "juhe_business", "api_keys", "client_ip_concurrency_limit")
	if err != nil {
		return report, err
	}
	report.KeyColumnPresent = hasColumn
	if !hasColumn {
		report.Examples = append(report.Examples, "api_keys.client_ip_concurrency_limit 列不存在（需先执行 upgrade 加法相）")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.api_keys`).Scan(&report.KeysTotal); err != nil {
		return report, fmt.Errorf("统计 api_keys 行数失败: %w", err)
	}
	if hasColumn {
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM juhe_business.api_keys
			 WHERE client_ip_concurrency_limit IS NULL
			    OR client_ip_concurrency_limit < 0
			    OR client_ip_concurrency_limit > $1`, IPLimitMax).Scan(&report.InvalidKeyLimitValues); err != nil {
			return report, fmt.Errorf("统计 API Key 单 IP 并发限制非法值失败: %w", err)
		}
		if report.InvalidKeyLimitValues > 0 {
			report.Examples = append(report.Examples,
				fmt.Sprintf("%d 个 API Key 的 client_ip_concurrency_limit 为空或越界（合法 0..%d）",
					report.InvalidKeyLimitValues, IPLimitMax))
		}
	}

	groups, err := scanGroups(ctx, db)
	if err != nil {
		return report, err
	}
	members, err := scanMembers(ctx, db)
	if err != nil {
		return report, err
	}
	groupIndex := make(map[string]scannedGroup, len(groups))
	membersBySuper := map[string][]scannedMember{}
	for _, group := range groups {
		groupIndex[group.id] = group
	}
	for _, member := range members {
		membersBySuper[member.superGroupID] = append(membersBySuper[member.superGroupID], member)
	}
	for _, group := range groups {
		parsed := ParseStoredPolicy(group.policy)
		if parsed.Reason != "" {
			report.InvalidPolicyGroups++
			appendExample(&report.Examples, "分组 "+group.id+" 策略列非单键/NULL 完成态形状（"+parsed.Reason+"）: "+group.CurrentPolicyText())
			continue
		}
		if parsed.HasIP {
			report.LegacyIPKeyGroups++
			appendExample(&report.Examples, "分组 "+group.id+" 策略仍含 clientIpConcurrencyLimit")
		}
	}
	for _, group := range groups {
		if group.groupKind != "super" {
			continue
		}
		if reason := verifySuperGroup(group, membersBySuper[group.id], groupIndex); reason != "" {
			report.SuperGroupMismatches++
			appendExample(&report.Examples, "超级分组 "+group.id+" "+reason)
		}
	}
	if len(report.Examples) == 0 {
		report.Examples = nil
	}
	return report, nil
}

// verifySuperGroup 返回超级分组自身行与成员不一致的原因（空串表示一致）：
// 空超级分组必须 personal + NULL；非空成员必须全部 standard、阈值一致，
// 且自身行 group_type/单键策略与首成员一致。
func verifySuperGroup(group scannedGroup, members []scannedMember, groups map[string]scannedGroup) string {
	if len(members) == 0 {
		if group.groupType != "personal" {
			return "空超级分组 group_type 不是 personal（" + group.groupType + "）"
		}
		if text := group.CurrentPolicyText(); text != "" {
			return "空超级分组策略列不是 NULL"
		}
		return ""
	}
	softSet := map[int]struct{}{}
	var firstEnabled, firstAny scannedMember
	for _, member := range members {
		memberGroup, ok := groups[member.memberGroupID]
		if !ok {
			return "成员 " + member.memberGroupID + " 行不存在"
		}
		if memberGroup.groupKind != "" && memberGroup.groupKind != "standard" {
			return "成员 " + member.memberGroupID + " 不是 standard 分组"
		}
		parsed := ParseStoredPolicy(memberGroup.policy)
		if parsed.Reason != "" {
			return "成员 " + member.memberGroupID + " 策略非法（" + parsed.Reason + "）"
		}
		softSet[parsed.Soft] = struct{}{}
		if firstAny.memberGroupID == "" {
			firstAny = member
		}
		if member.enabled && firstEnabled.memberGroupID == "" {
			firstEnabled = member
		}
	}
	if len(softSet) != 1 {
		return "成员 defaultSoftConcurrency 不一致：" + joinSoftSet(softSet)
	}
	// 自身行重算基准与 BuildPlan/Apply 一致：优先级最小的启用成员；没有启用
	// 成员时回落优先级最小的成员（阈值已校验一致，策略取值不受影响）。
	first := firstEnabled
	if first.memberGroupID == "" {
		first = firstAny
	}
	firstGroup := groups[first.memberGroupID]
	parsedOwn := ParseStoredPolicy(group.policy)
	if parsedOwn.Reason != "" {
		return "自身行策略非法（" + parsedOwn.Reason + "）"
	}
	if group.groupType != firstGroup.groupType {
		return "自身行 group_type " + group.groupType + " 与首成员 " + firstGroup.groupType + " 不一致"
	}
	soft := 0
	for value := range softSet {
		soft = value
	}
	if !singleKeyPolicyMatches(group.policy, soft) {
		return "自身行策略与首成员单键策略不一致"
	}
	return ""
}

// relationExists / columnExistsQ 是完成态与扫描的结构探测原语。
func relationExists(ctx context.Context, q querier, schemaName, table string) (bool, error) {
	var name sql.NullString
	if err := q.QueryRowContext(ctx,
		`SELECT to_regclass($1)::text`, schemaName+"."+table).Scan(&name); err != nil {
		return false, fmt.Errorf("检查 %s.%s 表存在性失败: %w", schemaName, table, err)
	}
	return name.Valid, nil
}

func columnExistsQ(ctx context.Context, q querier, schemaName, table, column string) (bool, error) {
	var value int
	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		schemaName, table, column).Scan(&value); err != nil {
		return false, fmt.Errorf("检查 %s.%s.%s 列存在性失败: %w", schemaName, table, column, err)
	}
	return value > 0, nil
}

func appendExample(examples *[]string, text string) {
	if len(*examples) >= 10 {
		return
	}
	*examples = append(*examples, text)
}

// isUndefinedTable 报告错误是否为 PG「关系不存在」（42P01）。
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
