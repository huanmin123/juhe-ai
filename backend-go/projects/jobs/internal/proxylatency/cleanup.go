package proxylatency

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// J3a 运行态记录的独立轻量清理。cleanuprepo 的 retention 清单只覆盖
// juhe_stats 业务表，proxy_latency_* 属于 juhe_jobs 运行态，由本包自管：
// outcomes/inputs 随每次派发只增（J3a 启用时每 5s×代理数行/天），必须按
// 时间窗回收；proxy_latency_input_versions 是每 proxy 一行的版本计数器
// （PRIMARY KEY (proxy_id)），行数不随请求增长，无需清理。
//
// 前提契约：保留窗口必须远大于 Go result projector 的游标滞后（正常为秒级），
// 否则可能删掉尚未投影的 committed outcome。projector 对确定性 rejected
// payload 会在同事务落 receipt 并越过游标（见 result_projector.go），不会
// 永久卡在旧行上。
const (
	// DefaultProxyLatencyRetention 是 outcomes/inputs 的默认保留窗口。
	// claims 的既有语义没有"保留天数"（执行完成即删、过期即可覆写），
	// 这里对齐平台常见 30 天滚动窗。
	DefaultProxyLatencyRetention = 30 * 24 * time.Hour
	// DefaultProxyLatencyCleanupInterval 是 owner 循环内的清理执行频率。
	DefaultProxyLatencyCleanupInterval = time.Hour
	// proxyLatencyPruneBatchSize 是单条 DELETE 的分批大小，避免长事务
	// 长时间持有行锁。
	proxyLatencyPruneBatchSize = 1000
	// proxyLatencyPruneMaxRowsPerRun 是单次 PruneExpiredRecords 的删除总量
	// 上限：积压超出时留待下一个清理周期，防止单轮失控。
	proxyLatencyPruneMaxRowsPerRun = 10000
)

// PruneStats 汇总一次清理实际删除的行数。
type PruneStats struct {
	Outcomes int64
	Inputs   int64
}

// PruneExpiredRecords 删除 stored_at/issued_at 早于 now-retain 的
// proxy_latency_outcomes 与 proxy_latency_inputs 行。retain 必须 > 0；
// 返回值中任一表失败即中止并保留原始错误（已删除的分批不回滚——清理是
// 幂等的，剩余行由下一周期继续）。
func (s *Store) PruneExpiredRecords(ctx context.Context, now time.Time, retain time.Duration) (PruneStats, error) {
	if s == nil || s.db == nil {
		return PruneStats{}, errors.New("proxy-latency store 未初始化")
	}
	if retain <= 0 {
		return PruneStats{}, errors.New("proxy-latency retention 必须为正")
	}
	now = now.UTC()
	cutoff := now.Add(-retain)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var stats PruneStats
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		deleted, done, err := s.pruneBatch(ctx, pruneTableOutcomes, cutoff)
		if err != nil {
			return stats, err
		}
		stats.Outcomes += deleted
		if done || stats.Outcomes+stats.Inputs >= proxyLatencyPruneMaxRowsPerRun {
			break
		}
	}
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		deleted, done, err := s.pruneBatch(ctx, pruneTableInputs, cutoff)
		if err != nil {
			return stats, err
		}
		stats.Inputs += deleted
		if done || stats.Outcomes+stats.Inputs >= proxyLatencyPruneMaxRowsPerRun {
			break
		}
	}
	return stats, nil
}

type pruneTable int

const (
	pruneTableOutcomes pruneTable = iota
	pruneTableInputs
)

func (s *Store) pruneBatch(ctx context.Context, table pruneTable, cutoff time.Time) (int64, bool, error) {
	keyColumn, timeColumn, tableLabel := "outcome_id", "stored_at", "outcomes"
	if table == pruneTableInputs {
		keyColumn, timeColumn, tableLabel = "request_id", "issued_at", "inputs"
	}
	sqliteTable := "proxy_latency_" + tableLabel
	query := `DELETE FROM ` + sqliteTable + ` WHERE ` + keyColumn + ` IN (SELECT ` + keyColumn + ` FROM ` + sqliteTable + ` WHERE ` + timeColumn + `<? LIMIT ?)`
	args := []any{cutoff.UTC().Format(time.RFC3339Nano), proxyLatencyPruneBatchSize}
	if s.mode == StorePostgres {
		postgresTable := "juhe_jobs.proxy_latency_" + tableLabel
		query = `DELETE FROM ` + postgresTable + ` WHERE ` + keyColumn + ` IN (SELECT ` + keyColumn + ` FROM ` + postgresTable + ` WHERE ` + timeColumn + `<$1 LIMIT $2)`
		args = []any{cutoff, proxyLatencyPruneBatchSize}
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, false, fmt.Errorf("清理 J3a %s 过期行失败: %w", tableLabel, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("读取 J3a %s 清理结果失败: %w", tableLabel, err)
	}
	return deleted, deleted < proxyLatencyPruneBatchSize, nil
}
