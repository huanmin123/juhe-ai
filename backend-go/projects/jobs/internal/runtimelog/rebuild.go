package runtimelog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FacetRebuildResult 是一次 facet 重建的结果快照：重建后 bucket 的聚合事实，
// 与 runtime_log_facet_summary 行逐字段一致（时间文本经
// postgresNodeInstantText 归一为 nodeISO；bucket 为空时时间字段为空串）。
type FacetRebuildResult struct {
	BucketKey    string
	TotalCount   int64
	EarliestTime string
	LatestTime   string
}

// RebuildPostgresFacets 从 juhe_dataset.runtime_logs 全量重建 F1 facet 三表
// （runtime_log_facet_summary / runtime_log_level_facets /
// runtime_log_event_facets），修复 W23 修复前增量写入对与 cutoff 同一自然日
// 记录的系统性少计（历史行计数不可由增量链自愈，只能全量重算）。
//
// 一次性运维入口（W33），仅供 CLI 显式调用，禁止接入定时任务。
//
// 语义与 W23 修复后的增量链严格一致：
//   - 计数范围 = time >= retention cutoff。cutoff 派生同 indexer
//     （refreshRetentionDays + retentionCutoff）：先读
//     juhe_business.system_settings 的 runtimeLogIndexRetentionDays，缺行时
//     回落 config.RetentionDays；cutoff = now - days，并截断到毫秒，与增量链
//     nodeISO 毫秒文本比较的截断精度对齐；
//   - 汇总边界经 postgresNodeInstantText 归一为 nodeISO 文本，与增量写入
//     同格式同精度；
//   - event facet 排除空白 event（空串与纯空白，对齐
//     facetEventCounts 的 TrimSpace 跳过语义；空白 event 行仍计入 summary
//     total_count 与 level facets，与增量链一致）。
//
// 并发契约：入口先获取 F1 owner lease（被运行中的 indexer 持有时显式失败，
// 不等待、不重试、不静默重建），重建事务以 REPEATABLE READ 快照聚合并在
// 事务内校验租约 fence，排斥并发 Commit 的 facet 增量。全量重算天然幂等，
// 失败后可直接重入。
func RebuildPostgresFacets(ctx context.Context, store Store, config Config) (FacetRebuildResult, error) {
	switch value := store.(type) {
	case *postgresStore:
		return value.rebuildFacets(ctx, config)
	default:
		return FacetRebuildResult{}, fmt.Errorf("未知运行日志 Store")
	}
}

// rebuildFacets 要求 config 来自 LoadConfig（OwnerID/OwnerLease/RetentionDays
// 已按进程契约填充并校验）。
func (store *postgresStore) rebuildFacets(ctx context.Context, config Config) (result FacetRebuildResult, err error) {
	result.BucketKey = facetBucketKey
	retentionDays, err := store.RuntimeRetentionDays(ctx, config.RetentionDays)
	if err != nil {
		return result, err
	}
	// cutoff 截断到毫秒：增量链以 nodeISO（毫秒文本）做字典序比较，重建的
	// timestamptz 精确比较必须对齐同一截断，避免亚毫秒边界与增量结果分叉
	// （Go time.Format 对毫秒以下直接截断，Truncate 与之等价）。
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Truncate(time.Millisecond)
	lease, acquired, err := store.AcquireOwnerLease(ctx, config.OwnerID, config.OwnerLease)
	if err != nil {
		return result, fmt.Errorf("获取运行日志 owner lease 失败: %w", err)
	}
	if !acquired {
		return result, fmt.Errorf("运行日志 owner lease 已由另一个 Go 实例持有；facet 重建必须独占 F1 写侧，请先停止持租约的 juhe-ai-jobs 再重试")
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), postgresOwnerLeaseTimeout)
		defer releaseCancel()
		err = errors.Join(err, releaseOwnerLeaseRecoverably(releaseCtx, store, lease))
	}()
	// REPEATABLE READ：全部聚合语句共享同一 runtime_logs 快照。lease 校验在
	// 事务内复核，快照开始前已通过校验的在途 Commit 提交后只可能晚于快照
	//（其 facet 增量落在重建结果之上、其行对本重建不可见），不会双计。
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return result, err
	}
	defer rollbackPostgresTx(tx)
	for _, statement := range postgresTransactionGuardStatements() {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return result, fmt.Errorf("设置 F1 PostgreSQL 事务时限失败: %w", err)
		}
	}
	if err := verifyPostgresOwnerLease(ctx, tx, lease); err != nil {
		return result, err
	}
	for _, table := range []string{"runtime_log_facet_summary", "runtime_log_level_facets", "runtime_log_event_facets"} {
		if _, err := tx.Exec(ctx, "DELETE FROM juhe_dataset."+table+" WHERE bucket_key = $1", facetBucketKey); err != nil {
			return result, err
		}
	}
	now := nowISO()
	var totalCount int64
	var earliest, latest string
	if err := tx.QueryRow(ctx, "SELECT COUNT(*), COALESCE("+postgresNodeInstantText("min(time)")+", ''), COALESCE("+postgresNodeInstantText("max(time)")+", '') FROM juhe_dataset.runtime_logs WHERE time >= $1", cutoff).Scan(&totalCount, &earliest, &latest); err != nil {
		return result, err
	}
	if totalCount == 0 {
		// 与增量链一致：零行 bucket 不留 summary 行（增量链在 total_count<=0
		// 时删除 summary 行），level/event 聚合同样不产生行。
		if err := tx.Commit(ctx); err != nil {
			return result, err
		}
		return result, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES ($1, $2, $3, $4, $5)`, facetBucketKey, totalCount, earliest, latest, now); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) SELECT $1, level, COUNT(*), $2 FROM juhe_dataset.runtime_logs WHERE time >= $3 GROUP BY level`, facetBucketKey, now, cutoff); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) SELECT $1, event, COUNT(*), max(time), $2 FROM juhe_dataset.runtime_logs WHERE time >= $3 AND COALESCE(event, '') !~ '^\s*$' GROUP BY event`, facetBucketKey, now, cutoff); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.TotalCount = totalCount
	result.EarliestTime = earliest
	result.LatestTime = latest
	return result, nil
}
