package statsverify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// Client-ip daily aggregation mirroring
// storage/client-ip-stats-aggregation.repository.ts and
// storage/client-ip-stats-writer.ts.
//
// Cursor and safety rules (aggregateClientIpStatsBatchAsync):
//   - cursorSafetyDelaySeconds = 15: rows with created_at <= now-15s are
//     eligible;
//   - traffic_source IN ('runtime_recovery_probe','cooldown_retest') is
//     excluded from aggregation but still consumed (its marker is set), so it
//     never blocks the pending queue;
//   - batch selection is driven by the consumption marker
//     (client_ip_stats_aggregated_at IS NULL), not by the cursor: a record
//     written after its created_at has been passed by the cursor is still
//     consumed (BUG-0345). The cursor is diagnostic only, ordering is
//     (created_at, id), lag is the age of the oldest still-pending record.
//
// Write rules (writeClientIpStatsAggregatesFromUsageRowsAsync): registry
// upsert keeps min(first_seen)/max(last_seen); daily tables accumulate with
// additive UPSERT; duration_ms_max keeps the running maximum; every written
// ip_hash marks both range-window dirty tables and the current windows go
// stale.
const (
	clientIpStatsJobName      = "client_ip_stats_aggregation"
	clientIpCursorSafetyDelay = 15 * time.Second
)

// ClientIPStatsAggregationJobState mirrors the global stats_job_state row
// for the client-ip aggregation cursor.
type ClientIPStatsAggregationJobState struct {
	CursorCreatedAt string
	CursorID        string
	LagSeconds      *int
}

// AggregateClientIPStatsBatch processes one cursor-bounded batch and returns
// the number of usage records folded into the daily tables.
func (s *Store) AggregateClientIPStatsBatch(ctx context.Context, limit int, now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("statsverify store 未初始化")
	}
	batchLimit := limit
	if batchLimit < 1 {
		batchLimit = 1
	}
	safeCreatedBefore := NowIso(now.Add(-clientIpCursorSafetyDelay))
	updatedAt := NowIso(now)
	location, _, err := s.LoadUsageStatsLocation(ctx, now)
	if err != nil {
		return 0, err
	}

	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 行锁仍要拿：它把并发实例串行化；游标本身不再参与选行，只用于水位列
	// 首次启用时的存量结算与诊断。
	state, err := s.loadClientIPJobState(ctx, tx)
	if err != nil {
		return 0, err
	}
	bootstrapped, err := statsagg.BootstrapUsageRecordConsumption(ctx, tx, statsagg.UsageRecordConsumptionClientIPStats, updatedAt, state.CursorCreatedAt, state.CursorID)
	if err != nil {
		return 0, err
	}
	if bootstrapped {
		// 结算只处理「旧游标已覆盖」的行；游标之后的行仍是待消费积压，lag 照实报。
		lagSeconds, err := s.pendingClientIPStatsLagSeconds(ctx, tx, now)
		if err != nil {
			return 0, err
		}
		if err := s.updateClientIPJobState(ctx, tx, clientIPJobStateUpdate{
			LastSuccessAt: updatedAt,
			LagSeconds:    &lagSeconds,
			UpdatedAt:     updatedAt,
		}); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	rows, err := s.selectClientIPUsageRecords(ctx, tx, safeCreatedBefore, batchLimit)
	if err != nil {
		return 0, err
	}

	if len(rows) == 0 {
		lagSeconds, err := s.pendingClientIPStatsLagSeconds(ctx, tx, now)
		if err != nil {
			return 0, err
		}
		if err := s.updateClientIPJobState(ctx, tx, clientIPJobStateUpdate{
			LastSuccessAt: updatedAt,
			LagSeconds:    &lagSeconds,
			UpdatedAt:     updatedAt,
		}); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	// 被排除的流量来源不参与聚合，但同样消费（打水位），否则它们会永久留在
	// 待消费队列里，把积压读数与清理地板一起钉死。
	aggregatable := make([]UsageStatsRecordRow, 0, len(rows))
	for _, row := range rows {
		if clientIPTrafficSourceExcluded(row.TrafficSource) {
			continue
		}
		aggregatable = append(aggregatable, row)
	}
	if err := s.writeClientIPAggregates(ctx, tx, aggregatable, updatedAt, location, now); err != nil { //nolint:staticcheck // windows derived inside from the loaded location
		return 0, err
	}
	consumedIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		consumedIDs = append(consumedIDs, row.ID)
	}
	if err := statsagg.MarkUsageRecordsConsumed(ctx, tx, statsagg.UsageRecordConsumptionClientIPStats, consumedIDs, updatedAt); err != nil {
		return 0, err
	}
	last := rows[len(rows)-1]
	lagSeconds, err := s.pendingClientIPStatsLagSeconds(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	if err := s.updateClientIPJobState(ctx, tx, clientIPJobStateUpdate{
		CursorCreatedAt: &last.CreatedAt,
		CursorID:        &last.ID,
		LastSuccessAt:   updatedAt,
		LagSeconds:      &lagSeconds,
		UpdatedAt:       updatedAt,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// clientIPTrafficSourceExcluded 判定该流量来源是否不参与 client-ip 聚合。
func clientIPTrafficSourceExcluded(trafficSource string) bool {
	switch trafficSource {
	case "runtime_recovery_probe", "cooldown_retest":
		return true
	}
	return false
}

// pendingClientIPStatsLagSeconds 返回待消费积压的年龄（最旧未消费记录的
// created_at 到现在，秒）；无积压为 0。旧口径用「最后消费游标」算 lag，
// 晚到记录会让它永远显示接近 0（BUG-0345 的掩盖口径）。
func (s *Store) pendingClientIPStatsLagSeconds(ctx context.Context, q queryer, now time.Time) (int, error) {
	oldest, ok, err := statsagg.OldestPendingUsageRecordCreatedAt(ctx, q, statsagg.UsageRecordConsumptionClientIPStats)
	if err != nil || !ok {
		return 0, err
	}
	return cursorLagSeconds(oldest, now), nil
}

// AggregateClientIPStatsBatchState exposes the persisted global cursor for
// diagnostics and tests.
func (s *Store) AggregateClientIPStatsBatchState(ctx context.Context) (ClientIPStatsAggregationJobState, error) {
	return s.loadClientIPJobState(ctx, s.db)
}

type clientIPJobStateUpdate struct {
	CursorCreatedAt *string
	CursorID        *string
	LastSuccessAt   string
	ErrorMessage    *string
	LagSeconds      *int
	UpdatedAt       string
}

func (s *Store) beginWriteTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启 statsverify 事务失败: %w", err)
	}
	return tx, nil
}

// loadClientIPJobState reads the global cursor row. PostgreSQL locks it FOR
// UPDATE inside the batch transaction (postgresClientIpStatsJobState);
// SQLite relies on the BEGIN IMMEDIATE writer lock from the DSN.
func (s *Store) loadClientIPJobState(ctx context.Context, q queryer) (ClientIPStatsAggregationJobState, error) {
	// PG 分支必须用 $n 占位符（硬编码 ? 触发 pgx 语法错误；w10c PG 门禁取证修复）。
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, updated_at)
		VALUES ('global', '', $1, $2)
		ON CONFLICT(scope_type, scope_id, job_name) DO NOTHING
	`, clientIpStatsJobName, NowIso(time.Now())); err != nil {
		return ClientIPStatsAggregationJobState{}, fmt.Errorf("初始化 client-ip 统计 job state 失败: %w", err)
	}
	query := fmt.Sprintf(`
		SELECT cursor_created_at, cursor_id, lag_seconds
		FROM %s
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = %s
		LIMIT 1
	`, s.statsTable("stats_job_state"), s.placeholder(1))
	query = `
	SELECT cursor_created_at, cursor_id, lag_seconds
	FROM juhe_stats.stats_job_state
	WHERE scope_type = 'global' AND scope_id = '' AND job_name = $1
	LIMIT 1
	FOR UPDATE
`
	var row ClientIPStatsAggregationJobState
	var cursorCreatedAt, cursorID any
	var lagSeconds any
	err := q.QueryRowContext(ctx, query, clientIpStatsJobName).Scan(&cursorCreatedAt, &cursorID, &lagSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return ClientIPStatsAggregationJobState{}, fmt.Errorf("读取 client-ip 统计 job state 失败: %w", err)
	}
	row.CursorCreatedAt, err = optionalTimestampText(cursorCreatedAt, "stats_job_state.cursor_created_at")
	if err != nil {
		return ClientIPStatsAggregationJobState{}, err
	}
	row.CursorID, err = sqlText(cursorID)
	if err != nil {
		return ClientIPStatsAggregationJobState{}, err
	}
	if lagSeconds != nil {
		lagInt, err := sqlInt(lagSeconds)
		if err != nil {
			return ClientIPStatsAggregationJobState{}, err
		}
		lagValue := int(lagInt)
		row.LagSeconds = &lagValue
	}
	return row, nil
}

const clientIPUsageRecordColumns = `
	id, traffic_source, client_ip, account_id, success,
	first_token_ms, duration_ms,
	input_tokens, output_tokens,
	cache_read_tokens, cache_read_cost_usd,
	cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd,
	thinking_tokens, input_image_tokens, output_image_tokens,
	cost_usd, created_at
`

// selectClientIPUsageRecords 取最旧的一批未消费记录：选行只看消费水位列
// （client_ip_stats_aggregated_at IS NULL），不再按游标区间推进，因此记录晚到
// 多久都不会被永久越过；被排除的流量来源也在这批里，交由调用方过滤后统一标记。
func (s *Store) selectClientIPUsageRecords(ctx context.Context, q queryer, safeCreatedBefore string, batchLimit int) ([]UsageStatsRecordRow, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE created_at <= %s
		  AND client_ip_stats_aggregated_at IS NULL
		ORDER BY created_at ASC, id ASC
		LIMIT %s
	`,
		clientIPUsageRecordColumns,
		s.usageTable("usage_records"),
		s.placeholder(1),
		s.placeholder(2),
	)
	rows, err := q.QueryContext(ctx, query, safeCreatedBefore, batchLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 client-ip 统计 usage_records 失败: %w", err)
	}
	defer rows.Close()
	result := make([]UsageStatsRecordRow, 0, batchLimit)
	for rows.Next() {
		var row UsageStatsRecordRow
		var clientIP, accountID any
		var success any
		var firstTokenMs, durationMs, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, cacheWrite1hTokens, thinkingTokens, inputImageTokens, outputImageTokens any
		var cacheReadCost, cacheWriteCost, costUsd any
		if err := rows.Scan(&row.ID, &row.TrafficSource, &clientIP, &accountID, &success,
			&firstTokenMs, &durationMs,
			&inputTokens, &outputTokens,
			&cacheReadTokens, &cacheReadCost,
			&cacheWriteTokens, &cacheWrite1hTokens, &cacheWriteCost,
			&thinkingTokens, &inputImageTokens, &outputImageTokens,
			&costUsd, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("解码 client-ip 统计 usage_records 行失败: %w", err)
		}
		if row.ClientIP, err = sqlStringPtr(clientIP); err != nil {
			return nil, err
		}
		if row.AccountID, err = sqlStringPtr(accountID); err != nil {
			return nil, err
		}
		successInt, err := sqlInt(success)
		if err != nil {
			return nil, err
		}
		row.Success = int(successInt)
		if row.FirstTokenMs, err = sqlIntPtr(firstTokenMs); err != nil {
			return nil, err
		}
		if row.DurationMs, err = sqlIntPtr(durationMs); err != nil {
			return nil, err
		}
		if row.InputTokens, err = sqlIntPtr(inputTokens); err != nil {
			return nil, err
		}
		if row.OutputTokens, err = sqlIntPtr(outputTokens); err != nil {
			return nil, err
		}
		if row.CacheReadTokens, err = sqlIntPtr(cacheReadTokens); err != nil {
			return nil, err
		}
		if row.CacheReadCostUsd, err = sqlFloatPtr(cacheReadCost); err != nil {
			return nil, err
		}
		if row.CacheWriteTokens, err = sqlIntPtr(cacheWriteTokens); err != nil {
			return nil, err
		}
		if row.CacheWrite1hTokens, err = sqlIntPtr(cacheWrite1hTokens); err != nil {
			return nil, err
		}
		if row.CacheWriteCostUsd, err = sqlFloatPtr(cacheWriteCost); err != nil {
			return nil, err
		}
		if row.ThinkingTokens, err = sqlIntPtr(thinkingTokens); err != nil {
			return nil, err
		}
		if row.InputImageTokens, err = sqlIntPtr(inputImageTokens); err != nil {
			return nil, err
		}
		if row.OutputImageTokens, err = sqlIntPtr(outputImageTokens); err != nil {
			return nil, err
		}
		if row.CostUsd, err = sqlFloatPtr(costUsd); err != nil {
			return nil, err
		}
		// Mirror normalizeUsageStatsRecordTimestamp / requiredRfc3339Instant.
		createdAt, err := ParseRFC3339(row.CreatedAt, "usage_records.created_at")
		if err != nil {
			return nil, err
		}
		row.CreatedAt = NowIso(createdAt)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 client-ip 统计 usage_records 失败: %w", err)
	}
	return result, nil
}

func (s *Store) updateClientIPJobState(ctx context.Context, tx execer, input clientIPJobStateUpdate) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
		VALUES ('global', '', %s, %s, %s, %s, %s, %s, %s)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
		  cursor_created_at = COALESCE(EXCLUDED.cursor_created_at, %s.cursor_created_at),
		  cursor_id = COALESCE(EXCLUDED.cursor_id, %s.cursor_id),
		  last_success_at = COALESCE(EXCLUDED.last_success_at, %s.last_success_at),
		  last_error_message = EXCLUDED.last_error_message,
		  lag_seconds = EXCLUDED.lag_seconds,
		  updated_at = EXCLUDED.updated_at
	`, s.statsTable("stats_job_state"),
		s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4), s.placeholder(5), s.placeholder(6), s.placeholder(7),
		s.statsTable("stats_job_state"), s.statsTable("stats_job_state"), s.statsTable("stats_job_state"))
	_, err := tx.ExecContext(ctx, query,
		clientIpStatsJobName,
		textPointerArg(input.CursorCreatedAt), textPointerArg(input.CursorID),
		input.LastSuccessAt, textPointerArg(input.ErrorMessage), intPointerArg(input.LagSeconds), input.UpdatedAt)
	if err != nil {
		return fmt.Errorf("更新 client-ip 统计 job state 失败: %w", err)
	}
	return nil
}

func textPointerArg(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nilIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func cursorLagSeconds(cursorCreatedAt string, now time.Time) int {
	cursorTime, err := ParseRFC3339(cursorCreatedAt, "客户端 IP 统计 cursorCreatedAt")
	if err != nil {
		return 0
	}
	lag := int(now.Sub(cursorTime).Seconds())
	if lag < 0 {
		return 0
	}
	return lag
}

// queryer/execer let job-state reads run on either the pool (diagnostics) or
// an open transaction (batch path).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
