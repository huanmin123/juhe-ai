// Package groupspolicymigration 实现「high_concurrency 分组迁移为
// personal + 两键策略」（普通分组可选调度策略设计 §5）的 maintenance 迁移内核：
//
//   - Plan（只读盘点）：扫描 juhe_business.groups 与
//     juhe_business.group_authorization_settings，逐行按 §5.2 映射，产出
//     dry-run manifest（盘点统计 + 逐行计划 + needs_review 明细）；畸形行
//     只报告不改写；
//   - Apply：单个业务库事务内 SET LOCAL lock_timeout='2s' 后重新盘点并写入
//     mapped 行；任一 needs_review 行即整体阻断（ErrNeedsReview，CLI 退出码
//     3，契约 §5.2），不静默补默认或猜测修复；不更新 updated_at（§8-13，
//     由发布流程重启进程刷新运行时缓存）；
//   - Satisfied / VerifyPostflight：完成态谓词（两表无 personal 之外的
//     group_type、策略列只含两键或 NULL），供 CLI postflight 与 upgrade
//     步骤同源使用。
//
// 幂等：完成态重跑盘点全部 unchanged，零 UPDATE（契约 §5.3）。映射纯函数见
// policy.go（与 gateway internal/groups 读兼容口径同源）。
package groupspolicymigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// 行处置词表（RowPlan.Action）。
const (
	ActionUnchanged   = "unchanged"
	ActionUpdate      = "update"
	ActionNeedsReview = "needs_review"
)

// 计划行所在表词表。
const (
	TableGroups       = "groups"
	TableAuthSettings = "group_authorization_settings"
)

// ErrNeedsReview 是 Apply 被 needs_review 行阻断的哨兵错误（CLI 据此退出码 3）。
var ErrNeedsReview = errors.New("存在 needs_review 行，迁移被阻断（人工确认后再迁移）")

// Options 是 Plan/Apply 的注入参数。
type Options struct {
	// GlobalMax 是旧高并发策略注入的进程配置 JUHE_AI_CONCURRENCY_GLOBAL_MAX
	// 的实际值（契约 §5.2：迁移命令必须显式提供并回显，默认
	// DefaultGlobalConcurrencyMax）。<=0 时回落默认值。
	GlobalMax int
}

// RowPlan 是单行的迁移计划/manifest 条目。
type RowPlan struct {
	Table       string `json:"table"`
	ID          string `json:"id"`
	GroupID     string `json:"groupId,omitempty"`
	OwnerID     string `json:"systemAccountId,omitempty"`
	Name        string `json:"name,omitempty"`
	GroupKind   string `json:"groupKind,omitempty"`
	CurrentType string `json:"currentGroupType"`
	// CurrentPolicy 保留现值原文（manifest 复核用；NULL 为空串）。
	CurrentPolicy string `json:"currentPolicy,omitempty"`
	Action        string `json:"action"`
	TargetType    string `json:"targetGroupType,omitempty"`
	// TargetPolicy 是目标 JSON；"null" 字面量表示目标为 SQL NULL（两键都默认
	// 或授权设置覆盖退役）；needs_review 行缺省。
	TargetPolicy json.RawMessage `json:"targetPolicy,omitempty"`
	// Reason 是 needs_review 原因（词表见 policy.go）。
	Reason string   `json:"reason,omitempty"`
	Notes  []string `json:"notes,omitempty"`
	// targetNull 是内部写入参数（目标策略列为 SQL NULL 时置位），不序列化。
	targetNull bool
}

// Plan 是一次盘点/执行的完整报告（dry-run manifest 与 apply 报告同形状）。
type Plan struct {
	Mode                        string `json:"mode"` // plan | apply
	Database                    string `json:"database"`
	GlobalMax                   int    `json:"globalMax"`
	GroupsTotal                 int    `json:"groupsTotal"`
	AuthSettingsTotal           int    `json:"groupAuthorizationSettingsTotal"`
	HighConcurrencyGroups       int    `json:"highConcurrencyGroups"`
	HighConcurrencyAuthSettings int    `json:"highConcurrencyAuthorizationSettings"`
	UnknownGroupTypeRows        int    `json:"unknownGroupTypeRows"`
	SuperGroups                 int    `json:"superGroups"`
	// DefaultSoftDistribution / ClientIPLimitDistribution 是映射后目标值分布
	//（键为字符串化整数；needs_review 行不计入）。
	DefaultSoftDistribution   map[string]int `json:"defaultSoftConcurrencyDistribution"`
	ClientIPLimitDistribution map[string]int `json:"clientIpConcurrencyLimitDistribution"`
	ExplicitThresholdRows     int            `json:"explicitThresholdRows"` // 目标阈值 != -1 的行
	QueueOverflowRows         int            `json:"queueOverflowRows"`     // 行为变为拒绝的行
	ImageLaneRows             int            `json:"imageLaneRows"`         // 图片 lane 改为账户硬并发的行
	Mapped                    int            `json:"mappedCount"`
	Unchanged                 int            `json:"unchangedCount"`
	Blocked                   int            `json:"blockedCount"`
	// Applied 是 apply 模式实际 UPDATE 行数（dry-run 恒 0；完成态幂等重跑为 0）。
	Applied     int       `json:"appliedCount"`
	Rows        []RowPlan `json:"rows"` // update + needs_review 行（unchanged 只计数）
	NeedsReview []RowPlan `json:"needsReview,omitempty"`
	Rollback    string    `json:"rollbackNote"`
}

// RollbackNote 是报告固定的回滚口径（契约 §5.3：旧二进制无法恢复
// high_concurrency 语义，回滚前保留数据库备份）。
const RollbackNote = "回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复业务库；" +
	"已迁移的 personal 行无法恢复 high_concurrency 语义，回滚后这些分组按旧普通分组运行（无软阈值/IP 限制）。"

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// scannedGroup 是 groups 行的盘点投影。
type scannedGroup struct {
	id        string
	ownerID   string
	name      string
	groupKind string
	groupType string
	policy    sql.NullString
}

// scannedAuthSetting 是 group_authorization_settings 行的盘点投影。
type scannedAuthSetting struct {
	authorizationID string
	groupID         string
	groupType       string
	policy          sql.NullString
}

// BuildPlan 只读盘点并返回迁移计划（契约 §5.1 前置盘点 + §5.2 映射）。
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
	if plan.Blocked > 0 {
		return plan, ErrNeedsReview
	}
	for index := range plan.Rows {
		row := &plan.Rows[index]
		if row.Action != ActionUpdate {
			continue
		}
		var policyArg any
		if !row.targetNull {
			policyArg = string(row.TargetPolicy)
		}
		var statement string
		switch row.Table {
		case TableGroups:
			statement = `UPDATE juhe_business.groups SET group_type = $1, scheduling_policy_json = $2 WHERE id = $3`
		case TableAuthSettings:
			statement = `UPDATE juhe_business.group_authorization_settings SET group_type = $1, scheduling_policy_json = $2 WHERE authorization_id = $3`
		default:
			return Plan{}, fmt.Errorf("未知计划表 %q", row.Table)
		}
		result, err := tx.ExecContext(ctx, statement, row.TargetType, policyArg, row.ID)
		if err != nil {
			return Plan{}, fmt.Errorf("迁移 %s.%s 失败: %w", row.Table, row.ID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Plan{}, fmt.Errorf("读取 %s.%s 写入行数失败: %w", row.Table, row.ID, err)
		}
		if affected != 1 {
			return Plan{}, fmt.Errorf("迁移 %s.%s 命中 %d 行（停写窗口内被并发改写？）", row.Table, row.ID, affected)
		}
		plan.Applied++
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, fmt.Errorf("提交迁移事务: %w", err)
	}
	return plan, nil
}

// planWith 在 q（连接或事务）上只读盘点并生成计划。超级分组自身行按首成员的
// 映射结果重算（契约 §4/§5.2），首成员行 needs_review 或缺失时连带阻断。
func planWith(ctx context.Context, q querier, options Options) (Plan, error) {
	globalMax := options.GlobalMax
	if globalMax <= 0 {
		globalMax = DefaultGlobalConcurrencyMax
	}
	plan := Plan{
		Mode:                      "plan",
		GlobalMax:                 globalMax,
		DefaultSoftDistribution:   map[string]int{},
		ClientIPLimitDistribution: map[string]int{},
		Rows:                      []RowPlan{},
		NeedsReview:               []RowPlan{},
		Rollback:                  RollbackNote,
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
	authSettings, err := scanAuthSettings(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	firstMembers, err := scanFirstSuperMembers(ctx, q)
	if err != nil {
		return Plan{}, err
	}
	plan.GroupsTotal = len(groups)
	plan.AuthSettingsTotal = len(authSettings)

	// 第一轮：standard 分组逐行分类（超级分组自身行第二轮按首成员重算）。
	targets := make(map[string]RowTarget, len(groups))
	reviewed := make(map[string]string, len(groups))
	for _, group := range groups {
		if group.groupKind == "super" {
			plan.SuperGroups++
			continue
		}
		target, reason := NormalizeRow(group.groupType, group.policy, globalMax)
		if reason != "" {
			reviewed[group.id] = reason
			review := planRowFromGroup(group)
			review.Reason = reason
			plan.appendReview(review)
			continue
		}
		targets[group.id] = target
		plan.recordGroup(group, target)
	}

	// 第二轮：超级分组自身行按首成员重算（成员策略指纹一致性的数据面收敛，
	// 契约 §4）。自身行旧值不参与分类——无论其当前 group_type/策略是什么，
	// 一律重算为首成员映射结果，否则后续成员保存会把旧值写回。
	for _, group := range groups {
		if group.groupKind != "super" {
			continue
		}
		memberID, hasMember := firstMembers[group.id]
		if !hasMember {
			// 空超级分组是正常状态（创建后尚未添加成员），没有首成员可复制；
			// 自身行收敛为 personal + NULL，成员加入时再由 super_members 回写。
			// 不能因此阻断整次迁移。
			plan.recordGroup(group, RowTarget{
				TargetType:   GroupTypePersonal,
				TargetPolicy: TwoKeyPolicy{SoftConcurrency: -1, ClientIPLimit: 0},
				TargetNull:   true,
			})
			continue
		}
		if reason, blocked := reviewed[memberID]; blocked {
			review := planRowFromGroup(group)
			review.Reason = ReviewSuperGroupMember
			review.Notes = append(review.Notes, "首成员 "+memberID+" 需要人工复核（"+reason+"）")
			plan.appendReview(review)
			continue
		}
		memberTarget, known := targets[memberID]
		if !known {
			// 首成员行不在 groups 扫描结果中（悬挂引用）：FK 缺失的脏数据，
			// 不猜测。
			review := planRowFromGroup(group)
			review.Reason = ReviewSuperGroupMember
			review.Notes = append(review.Notes, "首成员 "+memberID+" 行不存在（悬挂引用）")
			plan.appendReview(review)
			continue
		}
		plan.recordGroup(group, memberTarget)
	}

	for _, setting := range authSettings {
		_, reason := NormalizeRow(setting.groupType, setting.policy, globalMax)
		if reason != "" {
			review := planRowFromAuthSetting(setting)
			review.Reason = reason
			plan.appendReview(review)
			continue
		}
		// 授权覆盖退役（契约 §2.1/§5.2）：通过畸形检查后 group_type 恒 personal、
		// 策略列恒 NULL——被授权方运行来源分组的两个策略，不再有 per-grantee
		// 覆盖，原策略内容不迁移保留。
		plan.recordAuthSetting(setting, RowTarget{
			TargetType:   GroupTypePersonal,
			TargetPolicy: TwoKeyPolicy{SoftConcurrency: -1, ClientIPLimit: 0},
			TargetNull:   true,
		})
	}
	return plan, nil
}

// scanGroups 全量只读扫描 groups（契约 §5.1 盘点列）。
func scanGroups(ctx context.Context, q querier) ([]scannedGroup, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, system_account_id, name, group_kind, group_type, scheduling_policy_json
		FROM juhe_business.groups ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("扫描 groups 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedGroup
	for rows.Next() {
		var group scannedGroup
		if err := rows.Scan(&group.id, &group.ownerID, &group.name, &group.groupKind, &group.groupType, &group.policy); err != nil {
			return nil, fmt.Errorf("读取 groups 行失败: %w", err)
		}
		out = append(out, group)
	}
	return out, rows.Err()
}

// scanAuthSettings 全量只读扫描 group_authorization_settings。
func scanAuthSettings(ctx context.Context, q querier) ([]scannedAuthSetting, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT authorization_id, group_id, group_type, scheduling_policy_json
		FROM juhe_business.group_authorization_settings ORDER BY authorization_id`)
	if err != nil {
		return nil, fmt.Errorf("扫描 group_authorization_settings 失败: %w", err)
	}
	defer rows.Close()
	var out []scannedAuthSetting
	for rows.Next() {
		var setting scannedAuthSetting
		if err := rows.Scan(&setting.authorizationID, &setting.groupID, &setting.groupType, &setting.policy); err != nil {
			return nil, fmt.Errorf("读取 group_authorization_settings 行失败: %w", err)
		}
		out = append(out, setting)
	}
	return out, rows.Err()
}

// scanFirstSuperMembers 返回每个超级分组的首成员 ID（priority 最小的成员行，
// 与 mergesupermigration.applyStrategy 取 Members[0] 的口径一致；
// ORDER BY priority, created_at, id 保证确定性）。
func scanFirstSuperMembers(ctx context.Context, q querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT DISTINCT ON (super_group_id) super_group_id, member_group_id
		FROM juhe_business.super_group_members
		ORDER BY super_group_id, priority ASC, created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("扫描 super_group_members 失败: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var superGroupID, memberID string
		if err := rows.Scan(&superGroupID, &memberID); err != nil {
			return nil, fmt.Errorf("读取 super_group_members 行失败: %w", err)
		}
		out[superGroupID] = memberID
	}
	return out, rows.Err()
}

// appendReview 登记一条 needs_review 行（计划行 + 明细 + 阻断计数）。
func (p *Plan) appendReview(review RowPlan) {
	review.Action = ActionNeedsReview
	p.NeedsReview = append(p.NeedsReview, review)
	p.Rows = append(p.Rows, review)
	p.Blocked++
}

// recordGroup 记录一行 groups 的分类/重算结果：盘点统计按现值归类（超级分组
// 自身行同样按现值计数），目标折叠按首成员（standard 行即自身）的映射结果。
func (p *Plan) recordGroup(group scannedGroup, target RowTarget) {
	if group.groupType == LegacyGroupTypeHighConcurrency {
		p.HighConcurrencyGroups++
	}
	if group.groupType != GroupTypePersonal && group.groupType != LegacyGroupTypeHighConcurrency {
		p.UnknownGroupTypeRows++
	}
	row := planRowFromGroup(group)
	if target.QueueOverflow {
		p.QueueOverflowRows++
		row.Notes = append(row.Notes, "旧 clientIpConcurrencyOverflowMode='queue'：迁移后超限行为变为拒绝")
	}
	if target.ImageLane {
		p.ImageLaneRows++
		row.Notes = append(row.Notes, "旧 imageLaneMaxConcurrency>0：图片 lane 改为按账户硬并发")
	}
	p.recordTarget(row, target)
}

// recordAuthSetting 记录一行授权设置的分类结果（覆盖退役：目标策略列恒为
// personal + NULL，见 NormalizeRow 与契约 §2.1）。
func (p *Plan) recordAuthSetting(setting scannedAuthSetting, target RowTarget) {
	if setting.groupType == LegacyGroupTypeHighConcurrency {
		p.HighConcurrencyAuthSettings++
	}
	if setting.groupType != GroupTypePersonal && setting.groupType != LegacyGroupTypeHighConcurrency {
		p.UnknownGroupTypeRows++
	}
	p.recordTarget(planRowFromAuthSetting(setting), target)
}

// recordTarget 把一行已通过分类的目标折叠进统计与计划：unchanged 只计数，
// update 落计划行（契约 §5.1 报告口径）。
func (p *Plan) recordTarget(row RowPlan, target RowTarget) {
	p.DefaultSoftDistribution[fmt.Sprintf("%d", target.TargetPolicy.SoftConcurrency)]++
	p.ClientIPLimitDistribution[fmt.Sprintf("%d", target.TargetPolicy.ClientIPLimit)]++
	if target.TargetPolicy.SoftConcurrency != -1 {
		p.ExplicitThresholdRows++
	}
	row.TargetType = target.TargetType
	if target.TargetNull {
		row.TargetPolicy = json.RawMessage("null")
		row.targetNull = true
	} else {
		row.TargetPolicy = json.RawMessage(target.TargetPolicy.TargetJSON())
	}
	if rowMatchesTarget(row.CurrentType, sql.NullString{String: row.CurrentPolicy, Valid: row.CurrentPolicy != ""}, target) {
		row.Action = ActionUnchanged
		p.Unchanged++
		return
	}
	row.Action = ActionUpdate
	p.Mapped++
	p.Rows = append(p.Rows, row)
}

// planRowFromGroup 组装 groups 行的计划条目。
func planRowFromGroup(group scannedGroup) RowPlan {
	return RowPlan{
		Table:         TableGroups,
		ID:            group.id,
		OwnerID:       group.ownerID,
		Name:          group.name,
		GroupKind:     group.groupKind,
		CurrentType:   group.groupType,
		CurrentPolicy: strings.TrimSpace(nullStringOrEmpty(group.policy)),
	}
}

// planRowFromAuthSetting 组装授权设置行的计划条目。
func planRowFromAuthSetting(setting scannedAuthSetting) RowPlan {
	return RowPlan{
		Table:         TableAuthSettings,
		ID:            setting.authorizationID,
		GroupID:       setting.groupID,
		CurrentType:   setting.groupType,
		CurrentPolicy: strings.TrimSpace(nullStringOrEmpty(setting.policy)),
	}
}

func nullStringOrEmpty(raw sql.NullString) string {
	if !raw.Valid {
		return ""
	}
	return raw.String
}

// ---------- 完成态谓词（CLI postflight 与 upgrade Satisfied/Verify 同源） ----------

// VerifyReport 是完成态核验报告（全程零写入）。
type VerifyReport struct {
	Database                  string   `json:"database"`
	NonPersonalGroups         int      `json:"nonPersonalGroups"`
	NonPersonalAuthSettings   int      `json:"nonPersonalAuthSettings"`
	InvalidPolicyGroups       int      `json:"invalidPolicyGroups"`
	InvalidPolicyAuthSettings int      `json:"invalidPolicyAuthSettings"`
	Examples                  []string `json:"examples,omitempty"` // 前 10 条违规行说明
}

// Ready 报告完成态：两表无 personal 之外的 group_type、策略列只含两键或 NULL。
func (r VerifyReport) Ready() bool {
	return r.NonPersonalGroups == 0 && r.NonPersonalAuthSettings == 0 &&
		r.InvalidPolicyGroups == 0 && r.InvalidPolicyAuthSettings == 0
}

// Satisfied 报告完成态谓词（upgrade 步骤 Satisfied 与 Verify 同源判据）。
func Satisfied(ctx context.Context, db *sql.DB) (bool, error) {
	report, err := VerifyPostflight(ctx, db)
	if err != nil {
		return false, err
	}
	return report.Ready(), nil
}

// VerifyPostflight 只读核验完成态并返回明细（两表逐项计数 + 违规样例）。
func VerifyPostflight(ctx context.Context, db *sql.DB) (VerifyReport, error) {
	report := VerifyReport{Examples: []string{}}
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&report.Database); err != nil {
		return report, fmt.Errorf("读取目标数据库名: %w", err)
	}
	// postflight 是完成态核验，不允许把“目标库没有迁移面表”当成 0 行 Ready；
	// upgrade 步骤的 Satisfied 另有 tableExists 守卫，fresh 库语义不受影响。
	var groupsTable sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('juhe_business.groups')::text`).Scan(&groupsTable); err != nil {
		return report, fmt.Errorf("检查 juhe_business.groups 表存在性失败: %w", err)
	}
	if !groupsTable.Valid {
		return report, fmt.Errorf("目标库 %s 缺少 juhe_business.groups 表，请核对 --dsn 是否指向业务库", report.Database)
	}
	var err error
	report.NonPersonalGroups, err = countUndefinedTableAsZero(ctx, db,
		`SELECT count(*) FROM juhe_business.groups WHERE group_type <> 'personal'`)
	if err != nil {
		return report, fmt.Errorf("统计 groups 非 personal 行失败: %w", err)
	}
	report.NonPersonalAuthSettings, err = countUndefinedTableAsZero(ctx, db,
		`SELECT count(*) FROM juhe_business.group_authorization_settings WHERE group_type <> 'personal'`)
	if err != nil {
		return report, fmt.Errorf("统计 group_authorization_settings 非 personal 行失败: %w", err)
	}
	if report.InvalidPolicyGroups, report.Examples, err = countInvalidPolicies(ctx, db, TableGroups, report.Examples); err != nil {
		return report, err
	}
	if report.InvalidPolicyAuthSettings, report.Examples, err = countInvalidPolicies(ctx, db, TableAuthSettings, report.Examples); err != nil {
		return report, err
	}
	if len(report.Examples) == 0 {
		report.Examples = nil
	}
	return report, nil
}

// countInvalidPolicies 读出一张表全部非 NULL 策略行并按完成态形状校验（行数
// 量级为业务分组数，全量读可接受；两键形状谓词无法用 SQL 表达）。样例追加进
// examples（上限 10 条）。
func countInvalidPolicies(ctx context.Context, db *sql.DB, table string, examples []string) (int, []string, error) {
	keyColumn := "id"
	if table == TableAuthSettings {
		// 授权设置的主键是 authorization_id（无 id 列）。
		keyColumn = "authorization_id"
	}
	query := `SELECT ` + keyColumn + `, scheduling_policy_json FROM juhe_business.` + table + `
		WHERE scheduling_policy_json IS NOT NULL`
	rows, err := db.QueryContext(ctx, query)
	if isUndefinedTable(err) {
		return 0, examples, nil
	}
	if err != nil {
		return 0, examples, fmt.Errorf("读取 %s 策略列失败: %w", table, err)
	}
	defer rows.Close()
	invalid := 0
	for rows.Next() {
		var id string
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			return invalid, examples, fmt.Errorf("读取 %s 策略行失败: %w", table, err)
		}
		if ValidateStoredTwoKeyPolicy(raw) {
			continue
		}
		invalid++
		if len(examples) < 10 {
			examples = append(examples, table+"."+id+" 策略列非两键/NULL 完成态形状: "+nullStringOrEmpty(raw))
		}
	}
	if err := rows.Err(); err != nil {
		return invalid, examples, fmt.Errorf("遍历 %s 策略行失败: %w", table, err)
	}
	return invalid, examples, nil
}

// countUndefinedTableAsZero 执行单值计数；fresh 库语义（升级步骤 dry-run 可先
// 于 EnsureAll 求值）：关系不存在（42P01）按 0 行处理。
func countUndefinedTableAsZero(ctx context.Context, db *sql.DB, query string) (int, error) {
	var value int
	if err := db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		if isUndefinedTable(err) {
			return 0, nil
		}
		return 0, err
	}
	return value, nil
}

// isUndefinedTable 报告错误是否为 PG「关系不存在」（42P01）。
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}
