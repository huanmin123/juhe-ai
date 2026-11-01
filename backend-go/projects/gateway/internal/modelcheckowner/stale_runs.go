package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// staleRunSweepThreshold 是遗留 running run 的收尾判定阈值：run 表的
// updated_at 由 CreateRun（=started_at）、运行期心跳（Runtime.touchRunHeartbeat
// 在每次 RenewClaim 成功后调用 TouchRun）与终态/终态后写点（ProjectOutcome、
// MergeRunQualityDecision、MarkHealthSync）推进。代码内最长 run 预算：run-now
// 默认 10 分钟、计划/恢复执行为 lease(6 分钟)-30 秒=5.5 分钟；手动 SSE run 无
// 代码内墙钟预算，仅受每跳重试边界（10/20/30 秒）与客户端连接约束，带大题库
// 的 full 手动 run 可以合法超过 15 分钟。心跳让 updated_at 持续前进后，30 分钟
// 阈值只对真正失去心跳的 run 生效（默认 lease 1 分钟、心跳间隔 1/3 lease，
// 阈值留有 30 倍余量）；即便误收，终态写以显式错误失败（ProjectOutcome 的
// status='running' CAS），不会静默污染数据。
const staleRunSweepThreshold = 30 * time.Minute

// staleRunErrorCode / staleRunErrorMessage 是收尾写入的终态语义：发起检测的
// 进程已丢失，run 无法由原进程收尾。
const (
	staleRunErrorCode    = "owner_lost"
	staleRunErrorMessage = "检测执行进程中断，运行未完成"
)

// staleRunSweepSQL 构造 PG 方言的收尾语句：updated_at 是 text 时间戳，比较
// 两侧都显式 ::timestamptz cast，避免文本字典序在零分数秒边界上的误序。
// finished_at 与 updated_at 写同一个收尾时刻：sweep 是 run 的终态写点，缺少
// finished_at 会让终态行违反"failed/completed 必须带完成时刻"的读模型契约。
func staleRunSweepSQL(table string) string {
	return `UPDATE ` + table + ` SET status='failed',error_code=?,error_message=?,finished_at=?,updated_at=? WHERE status='running' AND updated_at::timestamptz < ?::timestamptz`
}

// SweepStaleRuns 是幂等收尾：发起进程崩溃/重启后（启动期由 OpenHost 调用一次），
// 或长驻进程运行期内错误链路遗留 running run（由 Scheduler.Run 周期调用）时，
// 把超过 staleRunSweepThreshold 仍未收尾的 running run 标记为 failed
// （error_code=owner_lost）。语句以 status='running' 为条件，重复执行自然
// 空转；只标记 updated_at 超过阈值的行，不会波及刚启动的正常 run。
func (s *Store) SweepStaleRuns(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("J3b store is not open")
	}
	if now.IsZero() {
		return 0, errors.New("J3b stale run sweep time is required")
	}
	table := s.table("model_check_runs")
	query := staleRunSweepSQL(table)
	cutoff := now.Add(-staleRunSweepThreshold).UTC().Format(time.RFC3339Nano)
	stamped := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, s.bind(query), staleRunErrorCode, staleRunErrorMessage, stamped, stamped, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep stale J3b running runs: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read stale J3b run sweep result: %w", err)
	}
	if changed > 0 {
		slog.Info("J3b 遗留 running 检测运行已收尾", "count", changed, "errorCode", staleRunErrorCode, "cutoff", cutoff)
	}
	return changed, nil
}

// MarkRunFailedTerminal 把仍处 running 的 run 就地收口为 failed，供运行错误臂
// （ClaimInput 失败、CommitOutcome/ProjectOutcome 失败等）在进程内写终态使用：
// 这些臂没有有效租约或 outcome 冲突，无法走 finishFailure，缺了本写入就只能
// 等周期 sweep 回收，期间 run 以僵尸 running 形态占用上游探针预算。
// CAS 限定 status='running'——与 ProjectOutcome 的终态写同一把锁：接管方已写
// 终态、run 不存在或已收尾时是幂等空操作（不报错）。
func (s *Store) MarkRunFailedTerminal(ctx context.Context, runID, errorCode, message string, finishedAt time.Time) error {
	if s == nil || s.db == nil || strings.TrimSpace(runID) == "" || finishedAt.IsZero() {
		return errors.New("J3b run failed-terminal mark input is invalid")
	}
	text := sanitizeSummaryString(message)
	stamped := finishedAt.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_runs")+` SET status='failed',message=?,error_code=?,error_message=?,finished_at=?,updated_at=? WHERE id=? AND status='running'`), text, nullable(sanitizeSummaryString(errorCode)), text, stamped, stamped, runID); err != nil {
		return fmt.Errorf("mark J3b run failed-terminal: %w", err)
	}
	return nil
}

// markRunFailedTerminalRecovery performs MarkRunFailedTerminal with a fresh
// background timeout. Callers reach this after CommitOutcome/ProjectOutcome
// already failed, often because the finalize budget ran out; reusing that
// expired context would make the fallback write fail and leave the run
// running until the periodic sweep.
func (s *Store) markRunFailedTerminalRecovery(runID, errorCode, message string, finishedAt time.Time) error {
	ctx, cancel := recoveryWriteContext()
	defer cancel()
	return s.MarkRunFailedTerminal(ctx, runID, errorCode, message, finishedAt)
}

// TouchRun advances a running run's updated_at heartbeat timestamp. The
// stale-run sweep uses this column as its only liveness signal, so a worker
// that only renews its execution claim (RenewClaim writes the claims table)
// would otherwise be reaped while still probing. The status='running' CAS
// keeps terminal rows untouched: a takeover or the sweep may have finished
// the run between the worker's last check and this write.
func (s *Store) TouchRun(ctx context.Context, runID string, now time.Time) error {
	if s == nil || s.db == nil || strings.TrimSpace(runID) == "" || now.IsZero() {
		return errors.New("J3b run touch input is invalid")
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_runs")+` SET updated_at=? WHERE id=? AND status='running'`), now.UTC().Format(time.RFC3339Nano), runID); err != nil {
		return fmt.Errorf("touch J3b running run: %w", err)
	}
	return nil
}
