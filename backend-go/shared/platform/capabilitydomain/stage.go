// 阶段记录：业务库 global_settings 专用 key account_capability_domain_migration，
// JSON 形状钉死为 {"stage":"...","ruleVersion":1,"updatedAt":<RFC3339>}。
// 阶段依次 m1_running → m1_complete → m2_running → m2_finalized；这是一次性
// 迁移记录，不是功能开关，不允许通过删除记录或覆盖参数重新开放迁移。
// 一次客户端命令一个阶段跃迁；阶段写入全部走条件更新（首次 INSERT
// ON CONFLICT DO NOTHING，跃迁 UPDATE 带 stage 谓词），保证并行迁移进程
// 只有一个执行者能完成同一跃迁。
//
// 读取面（ReadRecord）供 gateway/jobs 启动门禁跨模块使用；写入原语
// （ClaimNewStage/TransitionStage）供 maintenance 迁移执行器使用——阶段
// 解析语义全仓库只此一份，禁止在任何模块复制。

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

// ReadRecord 读取阶段记录（PostgreSQL 业务库，juhe_business schema 限定与 $1
// 占位符）；无记录返回 ok=false。gateway/jobs 启动门禁与 maintenance 迁移
// 执行器共用此读取语义，解析语义只此一份。
func ReadRecord(ctx context.Context, db *sql.DB) (Record, bool, error) {
	query := `SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`
	var valueJSON string
	err := db.QueryRowContext(ctx, query, MigrationSettingKey).Scan(&valueJSON)
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

// stageGateActionText 是启动阶段门禁的统一运维动作指引（三个失败臂共用，
// 迁移命令序列与部署顺序契约一致：停写 → 显式 M1 → m2-cutover）。
const stageGateActionText = "运维动作：先停写（停止 gateway/jobs）并验证可恢复备份，再依次执行 juhe-ai-maintenance --migrate-account-capability-domain --account-capability-domain-stage=m1 与 juhe-ai-maintenance --migrate-account-capability-domain --account-capability-domain-stage=m2-cutover（均需显式携带 --node-stopped --go-stopped --backup-confirmed），核验通过后再启动本进程"

// RequireM2Finalized 是 gateway/jobs 启动阶段门禁：阶段记录必须存在、stage
// 必须已是 m2_finalized（ruleVersion 与本包一致由 ParseRecord 在读取时强制，
// 损坏记录同样在此报错）。任一不满足即返回携带运维动作（迁移命令序列）的
// 错误，调用方原样 fail-fast；启动只读，不把启动变成迁移执行者。两侧启动
// 门禁共用本函数，禁止在调用侧复制判定语义与文案。
func RequireM2Finalized(ctx context.Context, db *sql.DB) error {
	record, present, err := ReadRecord(ctx, db)
	if err != nil {
		return fmt.Errorf("AI 账户能力域阶段记录 %s 读取失败：%v。%s（记录损坏或 ruleVersion 不符时迁移命令同样拒绝执行，先核对记录来源与 maintenance 版本，禁止手工改写记录）", MigrationSettingKey, err, stageGateActionText)
	}
	if !present {
		return fmt.Errorf("AI 账户能力域阶段记录 %s 不存在，M2 启动门禁要求 m2_finalized。%s", MigrationSettingKey, stageGateActionText)
	}
	if record.Stage != StageM2Finalized {
		return fmt.Errorf("AI 账户能力域阶段记录当前为 %s，M2 启动门禁要求 m2_finalized。%s", record.Stage, stageGateActionText)
	}
	return nil
}

// ClaimNewStage 首次写入阶段记录（要求记录尚不存在）。并发竞争下只有一个
// INSERT 生效；返回 acquired=false 表示另一个执行者已写入。
func ClaimNewStage(ctx context.Context, tx *sql.Tx, stage Stage, now time.Time) (bool, error) {
	payload, err := json.Marshal(Record{Stage: stage, RuleVersion: RuleVersion, UpdatedAt: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return false, err
	}
	query := `INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`
	args := []any{MigrationSettingKey, string(payload), now.UTC().Format(time.RFC3339)}
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

// TransitionStage 条件更新阶段：仅当当前 stage 等于 expect 时写入 next。
// 返回 acquired=false 表示当前 stage 已被并行执行者改变。
func TransitionStage(ctx context.Context, tx *sql.Tx, expect, next Stage, now time.Time) (bool, error) {
	payload, err := json.Marshal(Record{Stage: next, RuleVersion: RuleVersion, UpdatedAt: now.UTC().Format(time.RFC3339)})
	if err != nil {
		return false, err
	}
	updatedAt := now.UTC().Format(time.RFC3339)
	query := `UPDATE "juhe_business"."global_settings" SET value_json = $1, updated_at = $2 WHERE key = $3 AND value_json::jsonb->>'stage' = $4`
	args := []any{string(payload), updatedAt, MigrationSettingKey, string(expect)}
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
