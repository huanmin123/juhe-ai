package groups

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// 超级分组（super group）目标契约见 docs/functions/超级分组设计.md。
// 本文件承载分组域的成员关系读写、写事务校验、调度指纹与引用守卫；
// 路由运行时只消费这里的成员快照，不复制成员事实。

// GroupKind 词表（设计 §3.1）。
const (
	GroupKindStandard = "standard"
	GroupKindSuper    = "super"
)

// 成员关系错误码（设计 §5）。
const (
	CodeGroupKindConflict                 = "group_kind_conflict"
	CodeSuperGroupMemberInvalid           = "super_group_member_invalid"
	CodeSuperGroupMemberLimit             = "super_group_member_limit"
	CodeSuperGroupMemberMinEnabled        = "super_group_member_min_enabled"
	CodeSuperGroupMemberOwnerMismatch     = "super_group_member_owner_mismatch"
	CodeSuperGroupMemberUnauthorized      = "super_group_member_unauthorized"
	CodeSuperGroupMemberNested            = "super_group_member_nested"
	CodeSuperGroupMemberDispatchMismatch  = "super_group_member_dispatch_policy_mismatch"
	CodeSuperGroupMemberDispatchConflict  = "super_group_member_dispatch_policy_conflict"
	CodeSuperGroupMemberReferenceConflict = "super_group_member_reference_conflict"
	CodeSuperGroupRevisionConflict        = "super_group_revision_conflict"
)

// maxSuperGroupMembers 是设计 §3.2 的成员行上限（enabled 与 disabled 行都计数）。
const maxSuperGroupMembers = 20

// minSuperGroupEnabledMembers 是启用或绑定超级分组时的 enabled 成员下限。
const minSuperGroupEnabledMembers = 2

// CodeError 是带稳定错误码与 HTTP 状态的领域错误；错误体固定 {code,message}。
type CodeError struct {
	Status  int
	Code    string
	Message string
}

func (e *CodeError) Error() string { return e.Message }

func codeError(status int, code, message string) *CodeError {
	return &CodeError{Status: status, Code: code, Message: message}
}

// normalizeGroupKind 归一创建入参：缺省 standard，词表外返回 group_kind_conflict。
func normalizeGroupKind(value *string) (string, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return GroupKindStandard, nil
	}
	switch kind := strings.TrimSpace(*value); kind {
	case GroupKindStandard, GroupKindSuper:
		return kind, nil
	default:
		return "", codeError(400, CodeGroupKindConflict, "分组类型无效")
	}
}

// SuperMemberInput 是成员集合替换的单行入参（客户端提交的 priority 允许重复）。
type SuperMemberInput struct {
	MemberGroupID string
	Priority      int
	Enabled       bool
}

// SuperMemberSummary 是成员关系读模型（设计 §5 GET 响应）。
type SuperMemberSummary struct {
	MemberGroupID   string `json:"memberGroupId"`
	MemberGroupName string `json:"memberGroupName"`
	Priority        int    `json:"priority"`
	Enabled         bool   `json:"enabled"`
	GroupEnabled    bool   `json:"groupEnabled"`
}

// SuperMembersSummary 是超级分组成员集合摘要；Revision 为 groups.updated_at
// 的单调毫秒时间戳（不透明字符串，设计 §5）。
type SuperMembersSummary struct {
	SuperGroupID string               `json:"superGroupId"`
	GroupKind    string               `json:"groupKind"`
	Revision     string               `json:"revision"`
	Members      []SuperMemberSummary `json:"members"`
}

// groupDispatchFingerprint 计算成员调度指纹（设计 §3.3）：
// SHA256("group-dispatch-fingerprint:v1\n" + canonical_json({group_type,
// scheduling_policy_json}))。现有模型没有独立 queue/concurrency 策略列，
// 队列与并发键位于 scheduling_policy_json 内，整体取值即覆盖。
func groupDispatchFingerprint(groupType string, schedulingJSON sql.NullString) (string, error) {
	var policy any
	if schedulingJSON.Valid && strings.TrimSpace(schedulingJSON.String) != "" {
		if err := json.Unmarshal([]byte(schedulingJSON.String), &policy); err != nil {
			return "", err
		}
	}
	canonical, err := json.Marshal(map[string]any{
		"group_type":             groupType,
		"scheduling_policy_json": policy,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("group-dispatch-fingerprint:v1\n"), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

// normalizeSuperMemberInputs 校验并归一成员集合：行数上限、成员 ID 非空且
// 不重复、priority 非负；按客户端 priority 升序（同值按 payload 数组顺序稳定
// 排序）重写为连续 0..n-1（设计 §3.2 后端归一口径）。
func normalizeSuperMemberInputs(members []SuperMemberInput) ([]SuperMemberInput, error) {
	if len(members) > maxSuperGroupMembers {
		return nil, codeError(400, CodeSuperGroupMemberLimit, "超级分组成员不能超过 20 个")
	}
	seen := make(map[string]bool, len(members))
	ordered := make([]SuperMemberInput, len(members))
	copy(ordered, members)
	for index, member := range ordered {
		member.MemberGroupID = strings.TrimSpace(member.MemberGroupID)
		if member.MemberGroupID == "" {
			return nil, codeError(400, CodeSuperGroupMemberInvalid, "成员分组不能为空")
		}
		if member.Priority < 0 {
			return nil, codeError(400, CodeSuperGroupMemberInvalid, "成员优先级必须是非负整数")
		}
		if seen[member.MemberGroupID] {
			return nil, codeError(400, CodeSuperGroupMemberInvalid, "成员分组不能重复")
		}
		seen[member.MemberGroupID] = true
		ordered[index] = member
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	for index := range ordered {
		ordered[index].Priority = index
	}
	return ordered, nil
}

// memberGroupFacts 是成员资格校验所需的普通分组事实。
type memberGroupFacts struct {
	id              string
	systemAccountID string
	name            string
	enabled         bool
	groupKind       string
	groupType       string
	schedulingJSON  sql.NullString
	updatedAt       string
}

// loadGroupFacts 读取分组事实（不加 owner 过滤，由调用方按场景校验归属）。
func (s *Store) loadGroupFacts(ctx context.Context, q queryer, groupID string) (memberGroupFacts, error) {
	var facts memberGroupFacts
	var enabled int
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, system_account_id, name, enabled, group_kind, group_type, scheduling_policy_json, updated_at
		FROM `+s.table("groups")+` WHERE id = ? LIMIT 1`), groupID).
		Scan(&facts.id, &facts.systemAccountID, &facts.name, &enabled, &facts.groupKind, &facts.groupType, &facts.schedulingJSON, &facts.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return memberGroupFacts{}, codeError(404, CodeSuperGroupMemberInvalid, "分组不存在")
	}
	if err != nil {
		return memberGroupFacts{}, err
	}
	facts.enabled = enabled == 1
	return facts, nil
}

// hasActiveGroupAuthorization 判断 viewer 是否对该分组存在有效授权（只读可见）。
func (s *Store) hasActiveGroupAuthorization(ctx context.Context, q queryer, groupID, viewer string) (bool, error) {
	if viewer == "" {
		return false, nil
	}
	var found int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("resource_authorizations")+` ra
		LEFT JOIN `+s.table("group_authorization_settings")+` gas
		  ON gas.authorization_id = ra.id AND gas.group_id = ra.resource_id
		WHERE ra.resource_type = 'group' AND ra.resource_id = ?
		  AND ra.grantee_system_account_id = ? AND ra.status = 'active'
		  AND (ra.expires_at IS NULL OR ra.expires_at > ?)
		  AND COALESCE(gas.enabled, 1) = 1
		LIMIT 1`), groupID, viewer, s.nowISO()).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetSuperMembers 返回超级分组成员摘要；管理面/个人面共用。只有 owner、
// 管理员或持有有效授权的 viewer 可读。
func (s *Store) GetSuperMembers(ctx context.Context, groupID string, access AccessScope) (*SuperMembersSummary, error) {
	ctx = ensureCtx(ctx)
	facts, err := s.loadGroupFacts(ctx, s.db, groupID)
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() && facts.systemAccountID != access.ViewerID {
		visible, visErr := s.hasActiveGroupAuthorization(ctx, s.db, groupID, access.ViewerID)
		if visErr != nil {
			return nil, visErr
		}
		if !visible {
			return nil, codeError(404, CodeSuperGroupMemberInvalid, "分组不存在")
		}
	}
	if facts.groupKind != GroupKindSuper {
		return nil, codeError(400, CodeGroupKindConflict, "该分组不是超级分组")
	}
	return s.superMembersSummary(ctx, s.db, groupID, facts.updatedAt)
}

// superMembersSummary 读取成员行与成员分组展示事实并组装摘要。
func (s *Store) superMembersSummary(ctx context.Context, q queryer, groupID, revision string) (*SuperMembersSummary, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT m.member_group_id, g.name, m.priority, m.enabled, g.enabled
		FROM `+s.table("super_group_members")+` m
		INNER JOIN `+s.table("groups")+` g ON g.id = m.member_group_id
		WHERE m.super_group_id = ?
		ORDER BY m.priority ASC, m.member_group_id ASC`), groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []SuperMemberSummary{}
	for rows.Next() {
		var member SuperMemberSummary
		var memberEnabled, groupEnabled int
		if err := rows.Scan(&member.MemberGroupID, &member.MemberGroupName, &member.Priority, &memberEnabled, &groupEnabled); err != nil {
			return nil, err
		}
		member.Enabled = memberEnabled == 1
		member.GroupEnabled = groupEnabled == 1
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &SuperMembersSummary{
		SuperGroupID: groupID,
		GroupKind:    GroupKindSuper,
		Revision:     revision,
		Members:      members,
	}, nil
}

// ReplaceSuperMembers 完整替换超级分组成员集合（设计 §5 PATCH）：单事务内
// 完成 owner/可见性、group_kind、同 owner、非嵌套、调度指纹、行数与启用
// 成员下限校验；expectedRevision 与 groups.updated_at 做 CAS；no-op 不推进
// revision 且不重写成员行。
func (s *Store) ReplaceSuperMembers(ctx context.Context, groupID, expectedRevision string, members []SuperMemberInput, access AccessScope) (*SuperMembersSummary, error) {
	ctx = ensureCtx(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	facts, err := s.loadGroupFacts(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if !access.canAccessAll() {
		if facts.systemAccountID != access.ViewerID {
			visible, visErr := s.hasActiveGroupAuthorization(ctx, tx, groupID, access.ViewerID)
			if visErr != nil {
				return nil, visErr
			}
			if visible {
				return nil, codeError(403, CodeSuperGroupMemberUnauthorized, "没有管理该超级分组的权限")
			}
			return nil, codeError(404, CodeSuperGroupMemberInvalid, "分组不存在")
		}
	}
	if facts.groupKind != GroupKindSuper {
		return nil, codeError(400, CodeGroupKindConflict, "该分组不是超级分组")
	}
	if strings.TrimSpace(expectedRevision) == "" || strings.TrimSpace(expectedRevision) != facts.updatedAt {
		return nil, codeError(409, CodeSuperGroupRevisionConflict, "超级分组成员已被其他操作更新，请刷新后重试")
	}

	normalized, err := normalizeSuperMemberInputs(members)
	if err != nil {
		return nil, err
	}

	var firstFingerprint string
	enabledCount := 0
	for _, member := range normalized {
		if member.MemberGroupID == groupID {
			return nil, codeError(400, CodeSuperGroupMemberInvalid, "超级分组不能把自己作为成员")
		}
		memberFacts, factsErr := s.loadGroupFacts(ctx, tx, member.MemberGroupID)
		if factsErr != nil {
			return nil, factsErr
		}
		if memberFacts.systemAccountID != facts.systemAccountID {
			return nil, codeError(400, CodeSuperGroupMemberOwnerMismatch, "成员分组必须与超级分组属于同一所有者")
		}
		if memberFacts.groupKind != GroupKindStandard {
			return nil, codeError(400, CodeSuperGroupMemberNested, "超级分组不能嵌套超级分组")
		}
		fingerprint, fpErr := groupDispatchFingerprint(memberFacts.groupType, memberFacts.schedulingJSON)
		if fpErr != nil {
			return nil, fpErr
		}
		if firstFingerprint == "" {
			firstFingerprint = fingerprint
		} else if fingerprint != firstFingerprint {
			return nil, codeError(400, CodeSuperGroupMemberDispatchMismatch, "成员分组的调度策略不一致")
		}
		if member.Enabled {
			enabledCount++
		}
	}
	if facts.enabled && enabledCount < minSuperGroupEnabledMembers {
		return nil, codeError(400, CodeSuperGroupMemberMinEnabled, "启用超级分组至少需要两个启用成员")
	}

	currentRows, err := s.superMemberRows(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if sameSuperMemberRows(currentRows, normalized) {
		return s.superMembersSummary(ctx, tx, groupID, facts.updatedAt)
	}

	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("super_group_members")+` WHERE super_group_id = ?`), groupID); err != nil {
		return nil, err
	}
	now := s.nowISO()
	for _, member := range normalized {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("super_group_members")+`
			(id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`),
			s.newI("sgm"), groupID, member.MemberGroupID, member.Priority, boolToInt(member.Enabled), now, now); err != nil {
			return nil, err
		}
	}
	// 成员集合变更后，把成员组的调度事实回写到超级分组自身（group_type 与
	// scheduling_policy_json）：超级分组目标在运行期按自身 GroupAccess 执行
	// 容量/队列门，只有与成员一致才符合设计 §4.3「按实际成员分组执行」。
	if len(normalized) > 0 {
		firstMemberID := normalized[0].MemberGroupID
		var memberType string
		var memberScheduling sql.NullString
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT group_type, scheduling_policy_json
			FROM `+s.table("groups")+` WHERE id = ?`), firstMemberID).Scan(&memberType, &memberScheduling); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("groups")+`
			SET group_type = ?, scheduling_policy_json = ? WHERE id = ?`),
			memberType, nullableSchedulingJSON(memberScheduling), groupID); err != nil {
			return nil, err
		}
	}
	nextUpdatedAt, err := nextGroupUpdatedAt(facts.updatedAt, s.now())
	if err != nil {
		return nil, err
	}
	update, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("groups")+` SET updated_at = ?
		WHERE id = ? AND system_account_id = ? AND updated_at = ?`), nextUpdatedAt, groupID, facts.systemAccountID, facts.updatedAt)
	if err != nil {
		return nil, err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return nil, codeError(409, CodeSuperGroupRevisionConflict, "超级分组成员已被其他操作更新，请刷新后重试")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateRuntime("super_group_members_updated")
	return s.superMembersSummary(ctx, s.db, groupID, nextUpdatedAt)
}

// nullableSchedulingJSON 把 sql.NullString 投影为可写入的 NULL 或文本。
func nullableSchedulingJSON(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

type superMemberRow struct {
	memberGroupID string
	priority      int
	enabled       bool
}

func (s *Store) superMemberRows(ctx context.Context, q queryer, groupID string) ([]superMemberRow, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT member_group_id, priority, enabled
		FROM `+s.table("super_group_members")+` WHERE super_group_id = ?
		ORDER BY priority ASC, member_group_id ASC`), groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []superMemberRow{}
	for rows.Next() {
		var row superMemberRow
		var enabled int
		if err := rows.Scan(&row.memberGroupID, &row.priority, &enabled); err != nil {
			return nil, err
		}
		row.enabled = enabled == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

func sameSuperMemberRows(current []superMemberRow, next []SuperMemberInput) bool {
	if len(current) != len(next) {
		return false
	}
	for index := range current {
		if current[index].memberGroupID != next[index].MemberGroupID ||
			current[index].priority != next[index].Priority ||
			current[index].enabled != next[index].Enabled {
			return false
		}
	}
	return true
}

// assertNoSuperGroupMemberReference 校验普通分组没有被任何超级分组引用为成员。
// 分组级停用/删除必须返回 409 super_group_member_reference_conflict（设计 §3.2）。
func (s *Store) assertNoSuperGroupMemberReference(ctx context.Context, q queryer, groupID string) error {
	var found int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("super_group_members")+`
		WHERE member_group_id = ? LIMIT 1`), groupID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return codeError(409, CodeSuperGroupMemberReferenceConflict, "该分组仍被超级分组引用，请先解除成员关系")
}

// assertSuperGroupFingerprintCompatible 在普通分组调度字段变更前重校验所有
// 引用它的超级分组：任一其他成员指纹与新指纹不一致返回 409
// super_group_member_dispatch_policy_conflict（设计 §3.3）。
func (s *Store) assertSuperGroupFingerprintCompatible(ctx context.Context, q queryer, memberGroupID, nextFingerprint string) error {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT DISTINCT super_group_id FROM `+s.table("super_group_members")+`
		WHERE member_group_id = ?`), memberGroupID)
	if err != nil {
		return err
	}
	defer rows.Close()
	superGroupIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		superGroupIDs = append(superGroupIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, superGroupID := range superGroupIDs {
		memberRows, err := q.QueryContext(ctx, s.bind(`SELECT g.group_type, g.scheduling_policy_json
			FROM `+s.table("super_group_members")+` m
			INNER JOIN `+s.table("groups")+` g ON g.id = m.member_group_id
			WHERE m.super_group_id = ? AND m.member_group_id <> ?`), superGroupID, memberGroupID)
		if err != nil {
			return err
		}
		conflict := false
		for memberRows.Next() {
			var groupType string
			var schedulingJSON sql.NullString
			if err := memberRows.Scan(&groupType, &schedulingJSON); err != nil {
				memberRows.Close()
				return err
			}
			fingerprint, fpErr := groupDispatchFingerprint(groupType, schedulingJSON)
			if fpErr != nil {
				memberRows.Close()
				return fpErr
			}
			if fingerprint != nextFingerprint {
				conflict = true
				break
			}
		}
		rowsErr := memberRows.Err()
		memberRows.Close()
		if rowsErr != nil {
			return rowsErr
		}
		if conflict {
			return codeError(409, CodeSuperGroupMemberDispatchConflict, "该分组的调度策略已与超级分组其他成员不一致")
		}
	}
	return nil
}

// countEnabledSuperMembers 返回超级分组的 enabled 成员数。
func (s *Store) countEnabledSuperMembers(ctx context.Context, q queryer, superGroupID string) (int, error) {
	var count int
	if err := q.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("super_group_members")+`
		WHERE super_group_id = ? AND enabled = 1`), superGroupID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
