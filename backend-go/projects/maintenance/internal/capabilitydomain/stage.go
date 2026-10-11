// 阶段记录：业务库 global_settings 专用 key account_capability_domain_migration，
// JSON 形状钉死为 {"stage":"...","ruleVersion":1,"updatedAt":<RFC3339>}。
// 阶段依次 m1_running → m1_complete → m2_running → m2_finalized；这是一次性
// 迁移记录，不是功能开关，不允许通过删除记录或覆盖参数重新开放迁移。
// 一次客户端命令一个阶段跃迁；阶段写入全部走条件更新（首次 INSERT
// ON CONFLICT DO NOTHING，跃迁 UPDATE 带 stage 谓词），保证并行迁移进程
// 只有一个执行者能完成同一跃迁。

package capabilitydomain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MigrationSettingKey 是业务库 global_settings 的专用 key。
const MigrationSettingKey = "account_capability_domain_migration"

// Stage 是阶段记录的 stage 值。
type Stage string

const (
	// StageNone 表示记录尚不存在（非落库值）。
	StageNone        Stage = ""
	StageM1Running   Stage = "m1_running"
	StageM1Complete  Stage = "m1_complete"
	StageM2Running   Stage = "m2_running"
	StageM2Finalized Stage = "m2_finalized"
)

// Target 是 --account-capability-domain-stage 的取值（m1 | m2-cutover）。
type Target string

const (
	TargetM1        Target = "m1"
	TargetM2Cutover Target = "m2-cutover"
)

// Record 是阶段记录的 JSON 载荷。
type Record struct {
	Stage       Stage  `json:"stage"`
	RuleVersion int    `json:"ruleVersion"`
	UpdatedAt   string `json:"updatedAt"`
}

// ParseRecord 校验并解析一行阶段记录；形状损坏（stage 不在枚举、
// ruleVersion 不符）原样报错，不静默修补。
func ParseRecord(valueJSON string) (Record, error) {
	var record Record
	if err := json.Unmarshal([]byte(valueJSON), &record); err != nil {
		return Record{}, fmt.Errorf("阶段记录 %s 不是合法 JSON: %w", MigrationSettingKey, err)
	}
	switch record.Stage {
	case StageM1Running, StageM1Complete, StageM2Running, StageM2Finalized:
	default:
		return Record{}, fmt.Errorf("阶段记录 %s 的 stage %q 不在 m1_running/m1_complete/m2_running/m2_finalized 内", MigrationSettingKey, record.Stage)
	}
	if record.RuleVersion != RuleVersion {
		return Record{}, fmt.Errorf("阶段记录 %s 的 ruleVersion 为 %d，期望 %d；拒绝在未知规则版本上续跑", MigrationSettingKey, record.RuleVersion, RuleVersion)
	}
	if _, err := time.Parse(time.RFC3339, record.UpdatedAt); err != nil {
		return Record{}, fmt.Errorf("阶段记录 %s 的 updatedAt %q 不是 RFC3339: %w", MigrationSettingKey, record.UpdatedAt, err)
	}
	return record, nil
}

// readRecord 读取阶段记录；无记录返回 StageNone 且 ok=false。
func readRecord(ctx context.Context, db *sql.DB, dialect Dialect) (Record, bool, error) {
	query := "SELECT value_json FROM global_settings WHERE key = ?"
	args := []any{MigrationSettingKey}
	if dialect == DialectPostgres {
		query = `SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`
	}
	var valueJSON string
	err := db.QueryRowContext(ctx, query, args...).Scan(&valueJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	record, err := ParseRecord(valueJSON)
	if err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

// stagePredicate 是条件更新的 stage 谓词表达式（方言分支）。
func stagePredicate(dialect Dialect) string {
	if dialect == DialectPostgres {
		return `value_json::jsonb->>'stage'`
	}
	return `json_extract(value_json, '$.stage')`
}

// claimNewStage 首次写入阶段记录（要求记录尚不存在）。并发竞争下只有一个
// INSERT 生效；返回 acquired=false 表示另一个执行者已写入。
func claimNewStage(ctx context.Context, tx *sql.Tx, dialect Dialect, stage Stage, now time.Time) (bool, error) {
	payload, err := json.Marshal(Record{Stage: stage, RuleVersion: RuleVersion, UpdatedAt: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return false, err
	}
	var (
		query string
		args  []any
	)
	if dialect == DialectPostgres {
		query = `INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`
		args = []any{MigrationSettingKey, string(payload), now.UTC().Format(time.RFC3339)}
	} else {
		query = `INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`
		args = []any{MigrationSettingKey, string(payload), now.UTC().Format(time.RFC3339)}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("写入阶段记录 %s 失败: %w", MigrationSettingKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取阶段记录写入行数失败: %w", err)
	}
	return affected == 1, nil
}

// transitionStage 条件更新阶段：仅当当前 stage 等于 expect 时写入 next。
// 返回 acquired=false 表示当前 stage 已被并行执行者改变。
func transitionStage(ctx context.Context, tx *sql.Tx, dialect Dialect, expect, next Stage, now time.Time) (bool, error) {
	payload, err := json.Marshal(Record{Stage: next, RuleVersion: RuleVersion, UpdatedAt: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return false, err
	}
	updatedAt := now.UTC().Format(time.RFC3339)
	var query string
	var args []any
	if dialect == DialectPostgres {
		query = `UPDATE "juhe_business"."global_settings" SET value_json = $1, updated_at = $2 WHERE key = $3 AND ` + stagePredicate(dialect) + ` = $4`
		args = []any{string(payload), updatedAt, MigrationSettingKey, string(expect)}
	} else {
		query = `UPDATE global_settings SET value_json = ?, updated_at = ? WHERE key = ? AND ` + stagePredicate(dialect) + ` = ?`
		args = []any{string(payload), updatedAt, MigrationSettingKey, string(expect)}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("条件更新阶段记录 %s 失败: %w", MigrationSettingKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取阶段条件更新行数失败: %w", err)
	}
	return affected == 1, nil
}

// MigrationDecision 是 target × 当前阶段的状态机判定。
type MigrationDecision int

const (
	// DecisionRun：写/推进到 decision.Next 后执行迁移。
	DecisionRun MigrationDecision = iota
	// DecisionAlreadyComplete：目标阶段已完成，只报告完成，不重算。
	DecisionAlreadyComplete
	// DecisionReject：当前阶段拒绝该目标（含封存与顺序不符）。
	DecisionReject
)

// PlanMigration 判定 target 在当前阶段下的动作与下一阶段（纯函数，状态机
// 唯一事实源，测试直接覆盖）。
func PlanMigration(target Target, current Stage) (decision MigrationDecision, next Stage, reason string) {
	switch target {
	case TargetM1:
		switch current {
		case StageNone:
			return DecisionRun, StageM1Running, "首次执行 M1：写入 m1_running 后执行"
		case StageM1Running:
			return DecisionRun, StageM1Running, "恢复失败批次：保持 m1_running，重读当前数据续跑"
		case StageM1Complete:
			return DecisionAlreadyComplete, StageM1Complete, "M1 已完成：重复执行只报告完成，不重算窗口期数据"
		case StageM2Running:
			return DecisionReject, current, "M2 切换进行中，拒绝回退执行 M1"
		case StageM2Finalized:
			return DecisionReject, current, "迁移已封存（m2_finalized），任何重新赋域命令均拒绝"
		}
	case TargetM2Cutover:
		switch current {
		case StageNone:
			return DecisionReject, current, "尚无迁移记录：M2 切换只接受 m1_complete 或恢复中的 m2_running"
		case StageM1Running:
			return DecisionReject, current, "M1 未完成：M2 切换只接受 m1_complete 或恢复中的 m2_running"
		case StageM1Complete:
			return DecisionRun, StageM2Running, "进入 M2 停写切换：写入 m2_running 后全量重算"
		case StageM2Running:
			return DecisionRun, StageM2Running, "恢复 M2 切换：保持 m2_running，重读当前数据续跑"
		case StageM2Finalized:
			return DecisionReject, current, "迁移已封存（m2_finalized），任何重新赋域命令均拒绝"
		}
	}
	return DecisionReject, current, "未知的迁移目标或阶段组合"
}
