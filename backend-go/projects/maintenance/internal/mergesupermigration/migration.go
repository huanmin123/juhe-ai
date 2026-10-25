// Package mergesupermigration 实现 merge 路由策略到超级分组的迁移（超级分组
// 设计 §6）：
//
//   - Plan 只读扫描 mode='merge' 的策略，逐条判定 mapped / blocked 并生成
//     确定性目标（超级分组 ID/名称、成员行与归一 priority）；
//   - Apply 在单个业务库事务内重新计算同一计划并写入：生成 disabled 的超级
//     分组与 super_group_members、把策略改写为 normal、替换为一条绑定超级分组
//     的 route_strategy_groups 行；任一守卫失败整笔回滚；
//   - blocked 策略不写库，由调用方在 manifest 中复核；全部 merge 行解决前
//     不允许上线（设计 §6 第 5 条）。
//
// PostgreSQL 单形态：sqlite 方言随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4，2026-10-24 用户裁决）。
package mergesupermigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 迁移判定词表。
const (
	DispositionMapped  = "mapped"
	DispositionBlocked = "blocked"
)

// 阻断原因词表（manifest 固定口径）。
const (
	BlockedBindingsEmpty            = "bindings_empty"
	BlockedBindingsExceed20         = "bindings_exceed_20"
	BlockedMemberDuplicate          = "member_duplicate"
	BlockedMemberMissing            = "member_missing"
	BlockedMemberOwnerMismatch      = "member_owner_mismatch"
	BlockedMemberNested             = "member_nested"
	BlockedEnabledMembersLessThan2  = "enabled_members_less_than_2"
	BlockedMemberDispatchMismatch   = "member_dispatch_policy_mismatch"
	BlockedMigrationGroupExists     = "migration_group_exists"
	BlockedMigrationGroupIDConflict = "migration_group_id_conflict"
)

// maxSuperGroupMembers 与超级分组设计 §3.2 的成员行上限一致。
const maxSuperGroupMembers = 20

// minEnabledMembers 仅用于历史 merge 迁移产物守卫，不代表当前超级分组启用或
// 路由绑定契约；当前写入口已不要求 enabled 成员数下限。
const minEnabledMembers = 2

// Options 是 Plan/Apply 的注入参数。
type Options struct {
	// BatchID 是稳定复用键的一部分（设计 §6：manifest 复用键
	// (system_account_id, migration_batch_id, source_route_strategy_id)）。必填。
	BatchID string
	Now     time.Time // 零值取当前 UTC
}

// MemberPlan 是成员行计划；Priority 为归一后的连续值。
type MemberPlan struct {
	MemberGroupID string `json:"member_group_id"`
	Priority      int    `json:"priority"`
	Enabled       bool   `json:"enabled"`
}

// StrategyPlan 是单条 merge 策略的迁移计划。
type StrategyPlan struct {
	StrategyID       string       `json:"source_route_strategy_id"`
	OwnerID          string       `json:"system_account_id"`
	Name             string       `json:"source_route_strategy_name"`
	Disposition      string       `json:"disposition"`
	BlockedReason    string       `json:"blocked_reason,omitempty"`
	SourceBindingIDs []string     `json:"source_binding_ids"`
	SuperGroupID     string       `json:"target_super_group_id,omitempty"`
	SuperGroupName   string       `json:"target_super_group_name,omitempty"`
	Members          []MemberPlan `json:"members,omitempty"`
}

// Plan 是一次迁移的完整计划/manifest 主体。
type Plan struct {
	BatchID    string         `json:"migration_batch_id"`
	Strategies []StrategyPlan `json:"strategies"`
	Mapped     int            `json:"mapped_count"`
	Blocked    int            `json:"blocked_count"`
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func bind(query string) string {
	var builder strings.Builder
	builder.Grow(len(query) + 8)
	index := 1
	for _, r := range query {
		if r == '?' {
			builder.WriteString(fmt.Sprintf("$%d", index))
			index++
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func table(name string) string {
	return "juhe_business." + name
}

// SuperGroupID 返回确定性目标组 ID（同一策略同一批次稳定）。
func SuperGroupID(strategyID string) string { return "mgsg_" + strategyID }

// SuperGroupName 返回设计 §6 固定的稳定名称。
func SuperGroupName(strategyID string) string { return "migration-super-group:" + strategyID }

// BuildPlan 只读扫描并返回迁移计划。
func BuildPlan(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	return planWith(ctx, db, options)
}

func planWith(ctx context.Context, q querier, options Options) (Plan, error) {
	if strings.TrimSpace(options.BatchID) == "" {
		return Plan{}, fmt.Errorf("migration batch id 必填")
	}
	rows, err := q.QueryContext(ctx, bind(`SELECT id, system_account_id, name FROM `+table("route_strategies")+`
		WHERE mode = 'merge' ORDER BY id`))
	if err != nil {
		return Plan{}, err
	}
	defer rows.Close()
	plan := Plan{BatchID: options.BatchID}
	for rows.Next() {
		strategy := StrategyPlan{Disposition: DispositionMapped}
		if err := rows.Scan(&strategy.StrategyID, &strategy.OwnerID, &strategy.Name); err != nil {
			return Plan{}, err
		}
		plan.Strategies = append(plan.Strategies, strategy)
	}
	if err := rows.Err(); err != nil {
		return Plan{}, err
	}
	for index := range plan.Strategies {
		if err := planStrategy(ctx, q, &plan.Strategies[index]); err != nil {
			return Plan{}, err
		}
		if plan.Strategies[index].Disposition == DispositionMapped {
			plan.Mapped++
		} else {
			plan.Blocked++
		}
	}
	return plan, nil
}

type sourceBinding struct {
	ID       string
	GroupID  string
	Status   string
	Priority int
}

type memberFact struct {
	ID             string
	OwnerID        string
	GroupKind      string
	GroupType      string
	SchedulingJSON sql.NullString
}

func planStrategy(ctx context.Context, q querier, plan *StrategyPlan) error {
	rows, err := q.QueryContext(ctx, bind(`SELECT id, group_id, status, priority
		FROM `+table("route_strategy_groups")+`
		WHERE route_strategy_id = ?
		ORDER BY priority ASC, created_at ASC, id ASC`), plan.StrategyID)
	if err != nil {
		return err
	}
	bindings := []sourceBinding{}
	for rows.Next() {
		var binding sourceBinding
		if err := rows.Scan(&binding.ID, &binding.GroupID, &binding.Status, &binding.Priority); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(bindings) == 0 {
		return block(plan, BlockedBindingsEmpty)
	}
	if len(bindings) > maxSuperGroupMembers {
		return block(plan, BlockedBindingsExceed20)
	}
	plan.SuperGroupID = SuperGroupID(plan.StrategyID)
	plan.SuperGroupName = SuperGroupName(plan.StrategyID)

	seen := make(map[string]bool, len(bindings))
	firstFingerprint := ""
	enabledCount := 0
	for _, binding := range bindings {
		if seen[binding.GroupID] {
			return block(plan, BlockedMemberDuplicate)
		}
		seen[binding.GroupID] = true
		fact, found, err := loadMemberFact(ctx, q, binding.GroupID)
		if err != nil {
			return err
		}
		if !found {
			return block(plan, BlockedMemberMissing)
		}
		if fact.OwnerID != plan.OwnerID {
			return block(plan, BlockedMemberOwnerMismatch)
		}
		if fact.GroupKind != "standard" {
			return block(plan, BlockedMemberNested)
		}
		fingerprint, err := dispatchFingerprint(fact.GroupType, fact.SchedulingJSON)
		if err != nil {
			return err
		}
		if firstFingerprint == "" {
			firstFingerprint = fingerprint
		} else if fingerprint != firstFingerprint {
			return block(plan, BlockedMemberDispatchMismatch)
		}
		member := MemberPlan{MemberGroupID: binding.GroupID, Priority: len(plan.Members)}
		if binding.Status == "active" {
			member.Enabled = true
			enabledCount++
		}
		plan.Members = append(plan.Members, member)
		plan.SourceBindingIDs = append(plan.SourceBindingIDs, binding.ID)
	}
	if enabledCount < minEnabledMembers {
		return block(plan, BlockedEnabledMembersLessThan2)
	}
	// 目标名称/ID 冲突：同一 owner 下已有同名分组，或目标 ID 已被占用。
	var existing int
	err = q.QueryRowContext(ctx, bind(`SELECT 1 FROM `+table("groups")+`
		WHERE system_account_id = ? AND name = ? LIMIT 1`), plan.OwnerID, plan.SuperGroupName).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		return block(plan, BlockedMigrationGroupExists)
	}
	err = q.QueryRowContext(ctx, bind(`SELECT 1 FROM `+table("groups")+`
		WHERE id = ? LIMIT 1`), plan.SuperGroupID).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		return block(plan, BlockedMigrationGroupIDConflict)
	}
	return nil
}

func block(plan *StrategyPlan, reason string) error {
	plan.Disposition = DispositionBlocked
	plan.BlockedReason = reason
	plan.Members = nil
	return nil
}

func loadMemberFact(ctx context.Context, q querier, groupID string) (memberFact, bool, error) {
	var fact memberFact
	err := q.QueryRowContext(ctx, bind(`SELECT id, system_account_id, group_kind, group_type, scheduling_policy_json
		FROM `+table("groups")+` WHERE id = ? LIMIT 1`), groupID).
		Scan(&fact.ID, &fact.OwnerID, &fact.GroupKind, &fact.GroupType, &fact.SchedulingJSON)
	if err == sql.ErrNoRows {
		return memberFact{}, false, nil
	}
	if err != nil {
		return memberFact{}, false, err
	}
	return fact, true, nil
}

// dispatchFingerprint 与超级分组设计 §3.3 的成员调度指纹同构：
// SHA256("group-dispatch-fingerprint:v1\n" + canonical_json({group_type,
// scheduling_policy_json}))。
func dispatchFingerprint(groupType string, schedulingJSON sql.NullString) (string, error) {
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

// Apply 在单个业务库事务内执行迁移：重新计算计划 → 写入全部 mapped 策略 →
// 提交。返回实际执行后的计划（含 blocked 明细）。blocked 策略不写库。
func Apply(ctx context.Context, db *sql.DB, options Options) (Plan, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	plan, err := planWith(ctx, tx, options)
	if err != nil {
		return Plan{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	nowISO := now.UTC().Format("2006-01-02T15:04:05.000") + "Z"
	for index := range plan.Strategies {
		strategy := &plan.Strategies[index]
		if strategy.Disposition != DispositionMapped {
			continue
		}
		if err := applyStrategy(ctx, tx, strategy, nowISO); err != nil {
			return Plan{}, fmt.Errorf("迁移策略 %s 失败: %w", strategy.StrategyID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func applyStrategy(ctx context.Context, tx *sql.Tx, plan *StrategyPlan, nowISO string) error {
	groupType, schedulingJSON, err := firstMemberDispatchFacts(ctx, tx, plan)
	if err != nil {
		return err
	}
	// 迁移产物守卫（设计 §6）：plan 阶段已保证 enabled=true 成员数 ≥2，生成的
	// 超级分组必须直接 enabled=1——否则改写后的 normal 策略会绑定一个 disabled
	// 目标，runtimecache 装载时被 groups.enabled=1 过滤掉，迁移后整体不可用。
	if _, err := tx.ExecContext(ctx, bind(`INSERT INTO `+table("groups")+`
		(id, system_account_id, name, description, enabled, is_default, group_type, group_kind, scheduling_policy_json, created_at, updated_at)
		VALUES (?, ?, ?, NULL, 1, 0, ?, 'super', ?, ?, ?)`),
		plan.SuperGroupID, plan.OwnerID, plan.SuperGroupName, groupType, schedulingJSON, nowISO, nowISO); err != nil {
		return err
	}
	for _, member := range plan.Members {
		if _, err := tx.ExecContext(ctx, bind(`INSERT INTO `+table("super_group_members")+`
			(id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`),
			fmt.Sprintf("sgm_%s_%d", plan.StrategyID, member.Priority), plan.SuperGroupID, member.MemberGroupID,
			member.Priority, boolInt(member.Enabled), nowISO, nowISO); err != nil {
			return err
		}
	}
	update, err := tx.ExecContext(ctx, bind(`UPDATE `+table("route_strategies")+`
		SET mode = 'normal', updated_at = ? WHERE id = ? AND mode = 'merge'`), nowISO, plan.StrategyID)
	if err != nil {
		return err
	}
	if affected, _ := update.RowsAffected(); affected != 1 {
		return fmt.Errorf("策略不再处于 merge 状态（并发迁移？）")
	}
	if _, err := tx.ExecContext(ctx, bind(`DELETE FROM `+table("route_strategy_groups")+`
		WHERE route_strategy_id = ?`), plan.StrategyID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, bind(`INSERT INTO `+table("route_strategy_groups")+`
		(id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, 1, 'active', ?, ?)`),
		"rsg_"+plan.StrategyID, plan.StrategyID, plan.OwnerID, plan.SuperGroupID, nowISO, nowISO); err != nil {
		return err
	}
	return nil
}

func firstMemberDispatchFacts(ctx context.Context, q querier, plan *StrategyPlan) (string, string, error) {
	if len(plan.Members) == 0 {
		return "", "", fmt.Errorf("策略 %s 没有成员", plan.StrategyID)
	}
	var groupType string
	var schedulingJSON sql.NullString
	if err := q.QueryRowContext(ctx, bind(`SELECT group_type, scheduling_policy_json FROM `+table("groups")+`
		WHERE id = ? LIMIT 1`), plan.Members[0].MemberGroupID).Scan(&groupType, &schedulingJSON); err != nil {
		return "", "", err
	}
	policy := ""
	if schedulingJSON.Valid {
		policy = schedulingJSON.String
	}
	return groupType, policy, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
