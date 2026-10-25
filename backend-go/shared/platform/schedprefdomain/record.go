// Package schedprefdomain 承载「调度偏好独立实体与 API Key 绑定」
// （PLAN-20261012T033000000Z P2）迁移完成记录的共享语义：业务库
// global_settings 专用 key scheduling_preferences_migration，JSON 形状钉死为
// {"stage":"...","ruleVersion":1,"completedAt":<RFC3339>,"counts":{...}}。
// 阶段两值：running（迁移命令领取后、完成前）→ completed（后检通过时条件
// 更新写入）。这是一次性迁移记录，不是功能开关，不允许通过删除记录或覆盖
// 参数重新开放迁移。
//
// 下沉理由与 capabilitydomain 相同（2026-10 M2 先例）：gateway/jobs P2 启动
// 门禁跨模块读取所需——记录键、形状解析与启动门禁语义全仓库只此一份，禁止
// 在任何模块复制。maintenance 迁移执行器经 ClaimRunningStage /
// CompleteStage 条件写入（单执行者先例照 capabilitydomain stage）；fresh 空
// 库封存（SealFreshInstall）供 seed 链调用。
package schedprefdomain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MigrationSettingKey 是业务库 global_settings 的专用 key。
const MigrationSettingKey = "scheduling_preferences_migration"

// RuleVersion 是迁移规则的固定版本；记录 ruleVersion 不符时读取方与迁移命
// 令同样拒绝，不静默续跑。
const RuleVersion = 1

// Stage 是迁移记录的 stage 值。
type Stage string

const (
	// StageNone 表示记录尚不存在（非落库值）。
	StageNone Stage = ""
	// StageRunning 是迁移命令领取阶段后的进行中值（completedAt 此时承载领取
	// 时间）；断点恢复 = 同阶段重跑（NULL 判定天然幂等）。
	StageRunning Stage = "running"
	// StageCompleted 是后检通过后的完成值；gateway/jobs P2 启动门禁只接受
	// 本值。
	StageCompleted Stage = "completed"
)

// Counts 是完成记录的迁移工作量计数（fresh 空库封存全零）。
type Counts struct {
	// StrategiesSpeedFirstMigrated 是生成等值速度优先偏好的策略数。
	StrategiesSpeedFirstMigrated int `json:"strategiesSpeedFirstMigrated"`
	// PreferencesCreated 是本次迁移新建的调度偏好行数（复用行不计入）。
	PreferencesCreated int `json:"preferencesCreated"`
	// APIKeysNullGeneral 是迁移扫描到的「绑定列为 NULL 且 purpose=general」
	// API Key 数（回填对象总量）。
	APIKeysNullGeneral int `json:"apiKeysNullGeneral"`
	// APIKeysBackfilled 是实际回填绑定的 API Key 数。
	APIKeysBackfilled int `json:"apiKeysBackfilled"`
}

// Record 是迁移记录的 JSON 载荷。
type Record struct {
	Stage       Stage  `json:"stage"`
	RuleVersion int    `json:"ruleVersion"`
	CompletedAt string `json:"completedAt"`
	Counts      Counts `json:"counts"`
}

// ParseRecord 校验并解析一行迁移记录；形状损坏（stage 不在枚举、ruleVersion
// 不符、时间非法、counts 负数）原样报错，不静默修补。
func ParseRecord(valueJSON string) (Record, error) {
	var record Record
	if err := json.Unmarshal([]byte(valueJSON), &record); err != nil {
		return Record{}, fmt.Errorf("迁移记录 %s 不是合法 JSON: %w", MigrationSettingKey, err)
	}
	switch record.Stage {
	case StageRunning, StageCompleted:
	default:
		return Record{}, fmt.Errorf("迁移记录 %s 的 stage %q 不在 running/completed 内", MigrationSettingKey, record.Stage)
	}
	if record.RuleVersion != RuleVersion {
		return Record{}, fmt.Errorf("迁移记录 %s 的 ruleVersion 为 %d，期望 %d；拒绝在未知规则版本上续跑", MigrationSettingKey, record.RuleVersion, RuleVersion)
	}
	if _, err := time.Parse(time.RFC3339, record.CompletedAt); err != nil {
		return Record{}, fmt.Errorf("迁移记录 %s 的 completedAt %q 不是 RFC3339: %w", MigrationSettingKey, record.CompletedAt, err)
	}
	if record.Counts.StrategiesSpeedFirstMigrated < 0 || record.Counts.PreferencesCreated < 0 ||
		record.Counts.APIKeysNullGeneral < 0 || record.Counts.APIKeysBackfilled < 0 {
		return Record{}, fmt.Errorf("迁移记录 %s 的 counts 存在负数", MigrationSettingKey)
	}
	return record, nil
}

// ReadRecord 读取迁移记录（PostgreSQL 业务库，juhe_business schema 限定与 $1
// 占位符）；无记录返回 ok=false。gateway/jobs 启动门禁与 maintenance 迁移
// 执行器共用此读取语义，解析语义只此一份。
func ReadRecord(ctx context.Context, db *sql.DB) (Record, bool, error) {
	return readRecordWith(ctx, db)
}

// readRecordWith 是 ReadRecord 的最小 SQL 面（供 *sql.DB 与 seed 链的语句录
// 制假驱动共用）。
func readRecordWith(ctx context.Context, client FreshInstallClient) (Record, bool, error) {
	query := `SELECT value_json FROM "juhe_business"."global_settings" WHERE key = $1`
	var valueJSON string
	err := client.QueryRowContext(ctx, query, MigrationSettingKey).Scan(&valueJSON)
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

// FreshInstallClient 是 SealFreshInstall 需要的最小 SQL 面（*sql.DB 满足；
// maintenance seed 链经语句录制假驱动注入测试）。
type FreshInstallClient interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// startupGateActionText 是启动阶段门禁的统一运维动作指引（与部署顺序契约一
// 致：新二进制 + 旧库（无完成记录）必须被拦截；fresh 空库由 --ensure-schema
// / --seed 自动写完成记录）。
const startupGateActionText = "运维动作：先停写（停止 gateway/jobs）并验证可恢复备份，再运行 juhe-ai-maintenance --ensure-schema（缺表/缺内置行时）与 juhe-ai-maintenance --migrate-scheduling-preferences --node-stopped --go-stopped --backup-confirmed（调度偏好迁移，三确认门禁），核验通过后再启动本进程"

// RequireCompleted 是 gateway/jobs P2 启动门禁：迁移记录必须存在且 stage 已
// 是 completed（ruleVersion 一致由 ParseRecord 在读取时强制，损坏记录同样在
// 此报错）。任一不满足即返回携带运维动作的错误，调用方原样 fail-fast；启动
// 只读，不把启动变成迁移执行者。两侧启动门禁共用本函数，禁止在调用侧复制
// 判定语义与文案。
func RequireCompleted(ctx context.Context, db *sql.DB) error {
	record, present, err := ReadRecord(ctx, db)
	if err != nil {
		return fmt.Errorf("调度偏好迁移记录 %s 读取失败：%v。%s（记录损坏或 ruleVersion 不符时迁移命令同样拒绝执行，先核对记录来源与 maintenance 版本，禁止手工改写记录）", MigrationSettingKey, err, startupGateActionText)
	}
	if !present {
		return fmt.Errorf("调度偏好迁移记录 %s 不存在，P2 启动门禁要求 completed。%s", MigrationSettingKey, startupGateActionText)
	}
	if record.Stage != StageCompleted {
		return fmt.Errorf("调度偏好迁移记录当前为 %s，P2 启动门禁要求 completed。%s", record.Stage, startupGateActionText)
	}
	return nil
}

// recordPayload 序列化一条记录（条件写入共用，时间统一 RFC3339 UTC）。
func recordPayload(stage Stage, counts Counts, now time.Time) (string, string, error) {
	payload, err := json.Marshal(Record{Stage: stage, RuleVersion: RuleVersion, CompletedAt: now.UTC().Format(time.RFC3339), Counts: counts})
	if err != nil {
		return "", "", err
	}
	return string(payload), now.UTC().Format(time.RFC3339), nil
}

// ClaimRunningStage 首次写入 running 迁移记录（要求记录尚不存在）。并发竞争
// 下只有一个 INSERT 生效；返回 acquired=false 表示另一个执行者已写入。fresh
// 空库封存与其他写入方共用同一唯一 key 竞争语义。
func ClaimRunningStage(ctx context.Context, tx *sql.Tx, now time.Time) (bool, error) {
	payload, updatedAt, err := recordPayload(StageRunning, Counts{}, now)
	if err != nil {
		return false, err
	}
	query := `INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`
	result, err := tx.ExecContext(ctx, query, MigrationSettingKey, payload, updatedAt)
	if err != nil {
		return false, fmt.Errorf("写入迁移记录 %s 失败: %w", MigrationSettingKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取迁移记录写入行数失败: %w", err)
	}
	return affected == 1, nil
}

// CompleteStage 条件更新 running → completed 并写入工作量计数。返回
// acquired=false 表示当前 stage 已被并行执行者改变（保留 running）。
func CompleteStage(ctx context.Context, tx *sql.Tx, counts Counts, now time.Time) (bool, error) {
	payload, updatedAt, err := recordPayload(StageCompleted, counts, now)
	if err != nil {
		return false, err
	}
	query := `UPDATE "juhe_business"."global_settings" SET value_json = $1, updated_at = $2
		WHERE key = $3 AND value_json::jsonb->>'stage' = $4`
	result, err := tx.ExecContext(ctx, query, payload, updatedAt, MigrationSettingKey, string(StageRunning))
	if err != nil {
		return false, fmt.Errorf("条件更新迁移记录 %s 失败: %w", MigrationSettingKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取迁移记录条件更新行数失败: %w", err)
	}
	return affected == 1, nil
}

// SealFreshInstall 是全新空库自动写完成记录（P2 设计门裁决②：空库 seed 直
// 接写完成记录，counts 全零）：seed 链在内置调度偏好行预置之后调用。触发条
// 件=迁移记录不存在且 api_keys 无任何行（seed 尚未创建默认 Key 的时点，由
// 调用方保证顺序）；满足则以单条 INSERT ON CONFLICT DO NOTHING 原子写入
// completed 记录（与迁移命令的领取/完成竞争同一 key）。记录已存在或
// api_keys 非空的库一律不写（存量库由迁移命令负责）。返回是否本次写入。
func SealFreshInstall(ctx context.Context, client FreshInstallClient) (bool, error) {
	if _, present, err := readRecordWith(ctx, client); err != nil {
		return false, err
	} else if present {
		return false, nil
	}
	countQuery := `SELECT COUNT(*) FROM "juhe_business"."api_keys"`
	var keys int
	if err := client.QueryRowContext(ctx, countQuery).Scan(&keys); err != nil {
		return false, fmt.Errorf("空库封存检查 api_keys 行数失败: %w", err)
	}
	if keys > 0 {
		return false, nil
	}
	payload, updatedAt, err := recordPayload(StageCompleted, Counts{}, time.Now())
	if err != nil {
		return false, err
	}
	query := `INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`
	result, err := client.ExecContext(ctx, query, MigrationSettingKey, payload, updatedAt)
	if err != nil {
		return false, fmt.Errorf("空库封存写入迁移记录 %s 失败: %w", MigrationSettingKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取空库封存写入行数失败: %w", err)
	}
	return affected == 1, nil
}
