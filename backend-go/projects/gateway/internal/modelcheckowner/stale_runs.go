package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// staleRunSweepThreshold 是遗留 running run 的收尾判定阈值：run 表的
// updated_at 仅在 CreateRun（=started_at）与终态/终态后写点（ProjectOutcome、
// MergeRunQualityDecision、MarkHealthSync）推进，运行期间的 lease 心跳
// （RenewClaim）只推进 model_check_execution_claims.updated_at，长 run 期间
// run.updated_at 不动。代码内最长 run 预算：run-now 默认 10 分钟、计划/
// 恢复执行为 lease(6 分钟)-30 秒=5.5 分钟；手动 SSE run 无代码内墙钟预算，
// 仅受每跳重试边界（10/20/30 秒）与客户端连接约束，带大题库的 full 手动
// run 可以合法超过 15 分钟而 updated_at 不推进。因此取 30 分钟：不小于最大
// 代码内预算的 3 倍，误收存活 run 的代价是该 run 终态写以显式错误失败
// （ProjectOutcome 的 status='running' CAS），不会静默污染数据。
const staleRunSweepThreshold = 30 * time.Minute

// staleRunErrorCode / staleRunErrorMessage 是收尾写入的终态语义：发起检测的
// 进程已丢失，run 无法由原进程收尾。
const (
	staleRunErrorCode    = "owner_lost"
	staleRunErrorMessage = "检测执行进程中断，运行未完成"
)

// staleRunSweepSQL 构造 PG 方言的收尾语句：updated_at 是 text 时间戳，比较
// 两侧都显式 ::timestamptz cast，避免文本字典序在零分数秒边界上的误序。
func staleRunSweepSQL(table string) string {
	return `UPDATE ` + table + ` SET status='failed',error_code=?,error_message=?,updated_at=? WHERE status='running' AND updated_at::timestamptz < ?::timestamptz`
}

// SweepStaleRuns 是启动期的一次性幂等收尾：发起进程崩溃/重启后，run 终态的
// 唯一收口（进程内 ProjectOutcome 的 CAS）永久丢失，run 停留 running。本方法
// 把超过 staleRunSweepThreshold 仍未收尾的 running run 标记为 failed
// （error_code=owner_lost）。语句以 status='running' 为条件，重复执行自然
// 空转；只标记 updated_at 超过阈值的行，不会波及刚启动的正常 run。
//
// j3b 生产形态为 PostgreSQL，比较方言为 PG-only；SQLite 模式（本地开发夹具）
// 不执行收尾并静默返回 0，避免把 PG 方言引入 SQLite 路径。
func (s *Store) SweepStaleRuns(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("J3b store is not open")
	}
	if now.IsZero() {
		return 0, errors.New("J3b stale run sweep time is required")
	}
	if s.mode != "postgres" {
		return 0, nil
	}
	cutoff := now.Add(-staleRunSweepThreshold).UTC().Format(time.RFC3339Nano)
	stamped := now.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, s.bind(staleRunSweepSQL(s.table("model_check_runs"))), staleRunErrorCode, staleRunErrorMessage, stamped, cutoff)
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
