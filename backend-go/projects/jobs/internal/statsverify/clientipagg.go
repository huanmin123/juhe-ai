package statsverify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Client-ip daily aggregation mirroring
// storage/client-ip-stats-aggregation.repository.ts and
// storage/client-ip-stats-writer.ts.
//
// Cursor and safety rules (aggregateClientIpStatsBatchAsync):
//   - cursorSafetyDelaySeconds = 15: rows with created_at <= now-15s are
//     eligible;
//   - traffic_source IN ('runtime_recovery_probe','cooldown_retest') is
//     excluded from aggregation but still advances the cursor via the
//     "ignored cursor" path;
//   - cursor ordering is (created_at, id), lag is floor((now-cursor)/1000).
//
// Write rules (writeClientIpStatsAggregatesFromUsageRowsAsync): registry
// upsert keeps min(first_seen)/max(last_seen); daily tables accumulate with
// additive UPSERT; duration_ms_max keeps the running maximum; every written
// ip_hash marks both range-window dirty tables and the current windows go
// stale.
const (
	clientIpStatsJobName           = "client_ip_stats_aggregation"
	clientIpCursorSafetyDelay      = 15 * time.Second
	clientIpExcludedTrafficSources = "('runtime_recovery_probe', 'cooldown_retest')"
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

	state, err := s.loadClientIPJobState(ctx, tx)
	if err != nil {
		return 0, err
	}
	rows, err := s.selectClientIPUsageRecords(ctx, tx, safeCreatedBefore, state.CursorCreatedAt, state.CursorID, batchLimit)
	if err != nil {
		return 0, err
	}

	if len(rows) == 0 {
		ignoredCursor, lagSeconds, err := s.latestIgnoredCursorAndLag(ctx, tx, safeCreatedBefore, state.CursorCreatedAt, state.CursorID, now)
		if err != nil {
			return 0, err
		}
		if err := s.updateClientIPJobState(ctx, tx, clientIPJobStateUpdate{
			CursorCreatedAt: nilIfEmpty(ignoredCursor.CreatedAt),
			CursorID:        nilIfEmpty(ignoredCursor.ID),
			LastSuccessAt:   updatedAt,
			LagSeconds:      &lagSeconds,
			UpdatedAt:       updatedAt,
		}); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	if err := s.writeClientIPAggregates(ctx, tx, rows, updatedAt, location, now); err != nil { //nolint:staticcheck // windows derived inside from the loaded location
		return 0, err
	}
	last := rows[len(rows)-1]
	cursorLag := cursorLagSeconds(last.CreatedAt, now)
	if err := s.updateClientIPJobState(ctx, tx, clientIPJobStateUpdate{
		CursorCreatedAt: &last.CreatedAt,
		CursorID:        &last.ID,
		LastSuccessAt:   updatedAt,
		LagSeconds:      &cursorLag,
		UpdatedAt:       updatedAt,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// AggregateClientIPStatsBatchState exposes the persisted global cursor for
// diagnostics and tests.
func (s *Store) AggregateClientIPStatsBatchState(ctx context.Context) (ClientIPStatsAggregationJobState, error) {
	return s.loadClientIPJobState(ctx, s.db)
}

type cursorTuple struct {
	CreatedAt string
	ID        string
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
	if s.mode == StorePostgres {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, updated_at)
			VALUES ('global', '', ?, ?)
			ON CONFLICT(scope_type, scope_id, job_name) DO NOTHING
		`, clientIpStatsJobName, NowIso(time.Now())); err != nil {
			return ClientIPStatsAggregationJobState{}, fmt.Errorf("初始化 client-ip 统计 job state 失败: %w", err)
		}
	}
	query := fmt.Sprintf(`
		SELECT cursor_created_at, cursor_id, lag_seconds
		FROM %s
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = %s
		LIMIT 1
	`, s.statsTable("stats_job_state"), s.placeholder(1))
	if s.mode == StorePostgres {
		query = `
		SELECT cursor_created_at, cursor_id, lag_seconds
		FROM juhe_stats.stats_job_state
		WHERE scope_type = 'global' AND scope_id = '' AND job_name = $1
		LIMIT 1
		FOR UPDATE
	`
	}
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

// selectClientIPUsageRecords mirrors the batch cursor query: excluded
// traffic sources are skipped, ordering is (created_at, id).
func (s *Store) selectClientIPUsageRecords(ctx context.Context, q queryer, safeCreatedBefore, cursorCreatedAt, cursorID string, batchLimit int) ([]UsageStatsRecordRow, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE created_at <= %s
		  AND traffic_source NOT IN %s
		  AND (created_at > %s OR (created_at = %s AND id > %s))
		ORDER BY created_at ASC, id ASC
		LIMIT %s
	`,
		clientIPUsageRecordColumns,
		s.usageTable("usage_records"),
		s.placeholder(1), clientIpExcludedTrafficSources,
		s.placeholder(2), s.placeholder(3), s.placeholder(4),
		s.placeholder(5),
	)
	rows, err := q.QueryContext(ctx, query, safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorID, batchLimit)
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

// latestIgnoredCursorAndLag mirrors latestIgnoredUsageRecordCursor +
// latestUsageRecordLagSeconds: when no aggregatable rows exist the cursor
// still advances past excluded traffic sources and the lag reflects the
// newest eligible record.
func (s *Store) latestIgnoredCursorAndLag(ctx context.Context, q queryer, safeCreatedBefore, cursorCreatedAt, cursorID string, now time.Time) (cursorTuple, int, error) {
	ignoredQuery := fmt.Sprintf(`
		SELECT created_at, id
		FROM %s
		WHERE created_at <= %s
		  AND traffic_source IN %s
		  AND (created_at > %s OR (created_at = %s AND id > %s))
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, s.usageTable("usage_records"), s.placeholder(1), clientIpExcludedTrafficSources,
		s.placeholder(2), s.placeholder(3), s.placeholder(4))
	var ignored cursorTuple
	var ignoredCreatedAt, ignoredID any
	err := q.QueryRowContext(ctx, ignoredQuery, safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorID).Scan(&ignoredCreatedAt, &ignoredID)
	if errors.Is(err, sql.ErrNoRows) {
		// Node leaves cursor fields NULL in this branch (COALESCE keeps the
		// previous cursor).
		ignored = cursorTuple{}
	} else if err != nil {
		return cursorTuple{}, 0, fmt.Errorf("读取 client-ip 统计忽略游标失败: %w", err)
	} else {
		ignored.CreatedAt, err = sqlText(ignoredCreatedAt)
		if err != nil {
			return cursorTuple{}, 0, err
		}
		if _, err := ParseRFC3339(ignored.CreatedAt, "usage_records.created_at"); err != nil {
			return cursorTuple{}, 0, err
		}
		if ignored.ID, err = sqlText(ignoredID); err != nil {
			return cursorTuple{}, 0, err
		}
	}

	lagQuery := fmt.Sprintf(`
		SELECT created_at
		FROM %s
		WHERE created_at <= %s
		  AND traffic_source NOT IN %s
		  AND (created_at > %s OR (created_at = %s AND id > %s))
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, s.usageTable("usage_records"), s.placeholder(1), clientIpExcludedTrafficSources,
		s.placeholder(2), s.placeholder(3), s.placeholder(4))
	var latest any
	lagSeconds := 0
	err = q.QueryRowContext(ctx, lagQuery, safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorID).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		lagSeconds = 0
	} else if err != nil {
		return cursorTuple{}, 0, fmt.Errorf("读取 client-ip 统计 lag 失败: %w", err)
	} else {
		latestText, err := sqlText(latest)
		if err != nil {
			return cursorTuple{}, 0, err
		}
		lagSeconds = cursorLagSeconds(latestText, now)
	}
	return ignored, lagSeconds, nil
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
