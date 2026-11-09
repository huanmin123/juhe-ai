package cleanuprepo

import (
	"context"
	"fmt"
	"time"
)

// ModelCheckRetentionStore 是 juhe_j3b 模型检测诊断域的保留清理。
//
// 背景（2026-11-09 全量清理核对）：`modelCheckRetentionDays` 设置与
// 《模型检测设计》§5.2 的「30 天保留 + 每账户 1000 条 run 上限」在此前 Go
// 实现中没有任何消费方，J3b 的 run / item / observation / input / outcome /
// claim / observation receipt / scheduler task / 质量健康小时表全部只增
// 不减。本 store 按时间窗口与账户条数上限收敛这些表；窗口天数、批次与节奏
// 由调用方（jobs 组合根 data-retention-cleanup）承担。
//
// 清理顺序：runs 先删（items / observations 随 run 外键 ON DELETE CASCADE
// 级联），再删 observation receipts（无外键，按写入时间独立收敛），然后
// claims → outcomes → inputs（三表之间无外键，按各自时间列收敛），最后是
// 质量健康小时表、scheduler 终态任务与每账户 1000 条 run 上限。表归属
// gateway J3b owner；jobs 只承担 retention 执行面（与 W40 accounthealth
// 先例同型：SQL 放在清理执行侧，不跨项目 import gateway 包）。
type ModelCheckRetentionStore struct {
	DB *DB
}

func (s *ModelCheckRetentionStore) schema() string {
	if s.DB.Postgres {
		return "juhe_j3b"
	}
	return ""
}

func (s *ModelCheckRetentionStore) table(name string) string {
	return s.DB.Table(s.schema(), name)
}

// CleanupRunsBefore 删除 created_at 严格早于 cutoff 的 run 行，最旧优先，
// 单批最多 limit 行。model_check_items / model_check_observations 有
// `run_id ... ON DELETE CASCADE`，随 run 一并删除。created_at 是 RFC3339
// 文本列，cutoff 用同格式 UTC 文本。
func (s *ModelCheckRetentionStore) CleanupRunsBefore(ctx context.Context, cutoff string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "model_check_runs", "created_at", cutoff, limit)
}

// CleanupObservationReceiptsBefore 删除 observation_created_at 严格早于
// cutoff 的信任聚合回执行。receipt 与 observation 无外键，必须独立收敛，
// 否则 observation 级联删除后会留下无主回执。
func (s *ModelCheckRetentionStore) CleanupObservationReceiptsBefore(ctx context.Context, cutoff string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "model_trust_observation_receipts", "observation_created_at", cutoff, limit)
}

// CleanupExecutionClaimsBefore 删除 updated_at 严格早于 cutoff 的执行认领行。
// claims 按 input_id upsert，过期 input 被删除后 claim 不再有消费方。
func (s *ModelCheckRetentionStore) CleanupExecutionClaimsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "model_check_execution_claims", "updated_at", cutoff, limit)
}

// CleanupOutcomesBefore 删除 observed_at 严格早于 cutoff 的执行结果行。
func (s *ModelCheckRetentionStore) CleanupOutcomesBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "model_check_outcomes", "observed_at", cutoff, limit)
}

// CleanupInputsBefore 删除 issued_at 严格早于 cutoff 的输入行（幂等账本）。
func (s *ModelCheckRetentionStore) CleanupInputsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "model_check_inputs", "issued_at", cutoff, limit)
}

// CleanupQualityHealthBefore 删除 stat_hour 严格早于 cutoffHour 的质量健康
// 小时行。stat_hour 是业务时区 `YYYY-MM-DDTHH` 键（gateway
// NewHealthStatHourFunc 唯一写入格式），cutoff 必须由调用方用同一业务时区
// 同格式生成。
func (s *ModelCheckRetentionStore) CleanupQualityHealthBefore(ctx context.Context, cutoffHour string, limit int) (int64, error) {
	return deleteRowsBefore(ctx, s.DB, s.schema(), "account_quality_health_hourly", "stat_hour", cutoffHour, limit)
}

// CleanupSchedulerTasksBefore 删除已到期的 scheduler 终态任务：
//   - completed 且 completed_at 早于 completedBefore；
//   - failed 且 due_at 早于 failedDueBefore（due_at 是下一次重试时间，早于
//     保留窗说明已不再重试，避免遗留 failed 任务无限累积）。
//
// 两条语句各自受 limit 约束，返回本轮删除总数。pending 任务（含未来 due_at）
// 永不删除，避免丢弃仍在排队的检测。
func (s *ModelCheckRetentionStore) CleanupSchedulerTasksBefore(ctx context.Context, completedBefore, failedDueBefore time.Time, limit int) (int64, error) {
	table := s.table("model_check_scheduler_tasks")
	completedQuery := fmt.Sprintf(`
      DELETE FROM %s
      WHERE ctid IN (
        SELECT ctid FROM %s
        WHERE state = 'completed' AND completed_at < ?
        ORDER BY completed_at ASC, ctid ASC
        LIMIT ?
      )`, table, table)
	completed, err := execChanged(ctx, s.DB, completedQuery, completedBefore.UTC(), batchLimit(limit))
	if err != nil {
		return completed, fmt.Errorf("清理 model_check_scheduler_tasks（completed）失败: %w", err)
	}
	failedQuery := fmt.Sprintf(`
      DELETE FROM %s
      WHERE ctid IN (
        SELECT ctid FROM %s
        WHERE state = 'failed' AND due_at < ?
        ORDER BY due_at ASC, ctid ASC
        LIMIT ?
      )`, table, table)
	failed, err := execChanged(ctx, s.DB, failedQuery, failedDueBefore.UTC(), batchLimit(limit))
	if err != nil {
		return completed, fmt.Errorf("清理 model_check_scheduler_tasks（failed）失败: %w", err)
	}
	return completed + failed, nil
}

// CleanupRunsBeyondAccountCap 删除每账户最新 perAccountCap 条之外的最旧 run，
// 单批最多 limit 行。run 的 items / observations 随外键级联删除。
//
// 这是《模型检测设计》§5.2「每账户 1000 条上限」的清理侧实现：由
// data-retention-cleanup 10 分钟节拍收敛，允许节拍内短暂超出上限；高频计划
// （1 分钟档）下仅靠 30 天时间窗仍可达 4 万+ run/账户，必须同时靠本上限
// 控体积。
func (s *ModelCheckRetentionStore) CleanupRunsBeyondAccountCap(ctx context.Context, perAccountCap, limit int) (int64, error) {
	if perAccountCap < 1 {
		perAccountCap = 1
	}
	table := s.table("model_check_runs")
	query := fmt.Sprintf(`
      DELETE FROM %s
      WHERE id IN (
        SELECT id FROM (
          SELECT id, row_number() OVER (
            PARTITION BY account_id
            ORDER BY created_at DESC, id DESC
          ) AS rn
          FROM %s
          WHERE account_id IS NOT NULL AND status <> 'running'
        ) ranked
        WHERE ranked.rn > ?
        ORDER BY ranked.rn DESC
        LIMIT ?
      )`, table, table)
	deleted, err := execChanged(ctx, s.DB, query, perAccountCap, batchLimit(limit))
	if err != nil {
		return 0, fmt.Errorf("清理 model_check_runs 账户上限失败: %w", err)
	}
	return deleted, nil
}
