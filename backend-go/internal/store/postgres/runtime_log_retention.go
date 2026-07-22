package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"juhe-ai/backend-go/internal/store/port"
)

const runtimeLogRetentionSettingSQL = `
SELECT value_json
FROM juhe_business.system_settings
WHERE system_account_id = 'sys_admin'
  AND key = 'runtimeLogIndexRetentionDays'
LIMIT 1`

const runtimeLogRetentionFacetLockSQL = `
LOCK TABLE juhe_dataset.runtime_log_facet_summary,
  juhe_dataset.runtime_log_level_facets,
  juhe_dataset.runtime_log_event_facets
IN SHARE ROW EXCLUSIVE MODE`

const runtimeLogRetentionEarliestCountedSQL = `
SELECT COALESCE((SELECT earliest_time
  FROM juhe_dataset.runtime_log_facet_summary
  WHERE bucket_key = 'current'
), '')`

const runtimeLogRetentionDeleteSQL = `
WITH doomed AS (
  SELECT id
  FROM juhe_dataset.runtime_logs
  WHERE time < $1::text
  ORDER BY time ASC, id ASC
  LIMIT $2::int
  FOR UPDATE SKIP LOCKED
)
DELETE FROM juhe_dataset.runtime_logs
USING doomed
WHERE runtime_logs.id = doomed.id
RETURNING runtime_logs.time, runtime_logs.level,
  COALESCE(NULLIF(BTRIM(runtime_logs.event), ''), '') AS event`

const runtimeLogRetentionSummaryUpdateSQL = `
UPDATE juhe_dataset.runtime_log_facet_summary
SET total_count = GREATEST(0, total_count - $2::bigint),
    earliest_time = (
      SELECT time FROM juhe_dataset.runtime_logs
      WHERE time >= $1::text
      ORDER BY time ASC, id ASC
      LIMIT 1
    ),
    latest_time = (
      SELECT time FROM juhe_dataset.runtime_logs
      WHERE time >= $1::text
      ORDER BY time DESC, id DESC
      LIMIT 1
    ),
    updated_at = $3::text
WHERE bucket_key = 'current'`

const runtimeLogRetentionSummaryDeleteSQL = `
DELETE FROM juhe_dataset.runtime_log_facet_summary
WHERE bucket_key = 'current' AND total_count <= 0`

const runtimeLogRetentionLevelUpdateSQL = `
WITH decrements(level, count) AS (
  SELECT * FROM unnest($2::text[], $3::bigint[])
)
UPDATE juhe_dataset.runtime_log_level_facets AS facets
SET count = GREATEST(0, facets.count - decrements.count),
    updated_at = $4::text
FROM decrements
WHERE facets.bucket_key = $1::text
  AND facets.level = decrements.level`

const runtimeLogRetentionLevelDeleteSQL = `
DELETE FROM juhe_dataset.runtime_log_level_facets
WHERE bucket_key = 'current' AND count <= 0`

const runtimeLogRetentionEventUpdateSQL = `
WITH decrements(event, count) AS (
  SELECT * FROM unnest($2::text[], $3::bigint[])
)
UPDATE juhe_dataset.runtime_log_event_facets AS facets
SET count = GREATEST(0, facets.count - decrements.count),
    latest_time = (
      SELECT logs.time FROM juhe_dataset.runtime_logs AS logs
      WHERE logs.time >= $4::text
        AND logs.event = decrements.event
      ORDER BY logs.time DESC, logs.id DESC
      LIMIT 1
    ),
    updated_at = $5::text
FROM decrements
WHERE facets.bucket_key = $1::text
  AND facets.event = decrements.event`

const runtimeLogRetentionEventDeleteSQL = `
DELETE FROM juhe_dataset.runtime_log_event_facets
WHERE bucket_key = 'current' AND count <= 0`

const runtimeLogRetentionCursorDeleteSQL = `
WITH doomed AS (
  SELECT log_file
  FROM juhe_dataset.runtime_log_file_cursors
  WHERE updated_at < $1::text
    AND cursor_offset >= file_size
    AND last_error_message IS NULL
  ORDER BY updated_at ASC, log_file ASC
  LIMIT $2::int
  FOR UPDATE SKIP LOCKED
), deleted AS (
  DELETE FROM juhe_dataset.runtime_log_file_cursors AS cursors
  USING doomed
  WHERE cursors.log_file = doomed.log_file
  RETURNING 1
)
SELECT count(*) FROM deleted`

const runtimeLogRetentionBucketKey = "current"
const runtimeLogRetentionISOLayout = "2006-01-02T15:04:05.000Z"

type runtimeLogRetentionRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type runtimeLogRetentionRow interface {
	Scan(dest ...any) error
}

type runtimeLogRetentionTx interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (runtimeLogRetentionRows, error)
	QueryRow(ctx context.Context, sql string, args ...any) runtimeLogRetentionRow
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type runtimeLogRetentionBeginner interface {
	BeginRuntimeLogRetentionTx(ctx context.Context) (runtimeLogRetentionTx, error)
}

type pgxRuntimeLogRetentionTx struct{ tx pgx.Tx }

func (tx pgxRuntimeLogRetentionTx) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return tx.tx.Exec(ctx, sql, arguments...)
}

func (tx pgxRuntimeLogRetentionTx) Query(ctx context.Context, sql string, args ...any) (runtimeLogRetentionRows, error) {
	return tx.tx.Query(ctx, sql, args...)
}

func (tx pgxRuntimeLogRetentionTx) QueryRow(ctx context.Context, sql string, args ...any) runtimeLogRetentionRow {
	return tx.tx.QueryRow(ctx, sql, args...)
}

func (tx pgxRuntimeLogRetentionTx) Commit(ctx context.Context) error   { return tx.tx.Commit(ctx) }
func (tx pgxRuntimeLogRetentionTx) Rollback(ctx context.Context) error { return tx.tx.Rollback(ctx) }

func (s *Store) BeginRuntimeLogRetentionTx(ctx context.Context) (runtimeLogRetentionTx, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	return pgxRuntimeLogRetentionTx{tx: tx}, nil
}

func (s *Store) GetRuntimeLogIndexRetentionDays(ctx context.Context) (int, bool, error) {
	var raw string
	if err := s.pool.QueryRow(ctx, runtimeLogRetentionSettingSQL).Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, fmt.Errorf("read runtime log index retention days: %w", err)
	}
	value, found := parseRuntimeLogIndexRetentionDays(raw)
	return value, found, nil
}

func (s *Store) CleanupRuntimeLogIndexBefore(ctx context.Context, input port.RuntimeLogRetentionCleanupInput) (int64, error) {
	return cleanupRuntimeLogIndexBefore(ctx, s, input)
}

func cleanupRuntimeLogIndexBefore(
	ctx context.Context,
	beginner runtimeLogRetentionBeginner,
	input port.RuntimeLogRetentionCleanupInput,
) (int64, error) {
	if strings.TrimSpace(input.CutoffISO) == "" {
		return 0, fmt.Errorf("runtime log retention cutoff is required")
	}
	if input.Limit <= 0 {
		return 0, fmt.Errorf("runtime log retention limit must be greater than zero")
	}
	tx, err := beginner.BeginRuntimeLogRetentionTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin runtime log retention tx: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if _, err := tx.Exec(ctx, runtimeLogRetentionFacetLockSQL); err != nil {
		return 0, fmt.Errorf("lock runtime log facet tables: %w", err)
	}
	var earliestCounted string
	if err := tx.QueryRow(ctx, runtimeLogRetentionEarliestCountedSQL).Scan(&earliestCounted); err != nil {
		return 0, fmt.Errorf("read runtime log facet boundary: %w", err)
	}
	rows, err := tx.Query(ctx, runtimeLogRetentionDeleteSQL, input.CutoffISO, int32(input.Limit))
	if err != nil {
		return 0, fmt.Errorf("delete runtime log retention batch: %w", err)
	}
	defer rows.Close()

	deleted := int64(0)
	counted := int64(0)
	levelCounts := make(map[string]int64)
	eventCounts := make(map[string]int64)
	for rows.Next() {
		var rowTime, level, event string
		if err := rows.Scan(&rowTime, &level, &event); err != nil {
			return 0, fmt.Errorf("scan deleted runtime log: %w", err)
		}
		deleted++
		if earliestCounted != "" && rowTime < earliestCounted {
			continue
		}
		counted++
		levelCounts[level]++
		if event != "" {
			eventCounts[event]++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate deleted runtime logs: %w", err)
	}
	rows.Close()

	if counted > 0 {
		updatedAt := time.Now().UTC().Truncate(time.Millisecond).Format(runtimeLogRetentionISOLayout)
		if _, err := tx.Exec(ctx, runtimeLogRetentionSummaryUpdateSQL, input.CutoffISO, counted, updatedAt); err != nil {
			return 0, fmt.Errorf("update runtime log facet summary: %w", err)
		}
		if _, err := tx.Exec(ctx, runtimeLogRetentionSummaryDeleteSQL); err != nil {
			return 0, fmt.Errorf("delete empty runtime log facet summary: %w", err)
		}
		levels, levelDecrements := sortedRuntimeLogRetentionCounts(levelCounts)
		if len(levels) > 0 {
			if _, err := tx.Exec(ctx, runtimeLogRetentionLevelUpdateSQL, runtimeLogRetentionBucketKey, levels, levelDecrements, updatedAt); err != nil {
				return 0, fmt.Errorf("update runtime log level facets: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, runtimeLogRetentionLevelDeleteSQL); err != nil {
			return 0, fmt.Errorf("delete empty runtime log level facets: %w", err)
		}
		events, eventDecrements := sortedRuntimeLogRetentionCounts(eventCounts)
		if len(events) > 0 {
			if _, err := tx.Exec(ctx, runtimeLogRetentionEventUpdateSQL, runtimeLogRetentionBucketKey, events, eventDecrements, input.CutoffISO, updatedAt); err != nil {
				return 0, fmt.Errorf("update runtime log event facets: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, runtimeLogRetentionEventDeleteSQL); err != nil {
			return 0, fmt.Errorf("delete empty runtime log event facets: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit runtime log retention tx: %w", err)
	}
	committed = true
	return deleted, nil
}

func (s *Store) CleanupCompletedRuntimeLogFileCursorsBefore(ctx context.Context, input port.RuntimeLogRetentionCleanupInput) (int64, error) {
	if strings.TrimSpace(input.CutoffISO) == "" {
		return 0, fmt.Errorf("runtime log cursor retention cutoff is required")
	}
	if input.Limit <= 0 {
		return 0, fmt.Errorf("runtime log cursor retention limit must be greater than zero")
	}
	var deleted int64
	if err := s.pool.QueryRow(ctx, runtimeLogRetentionCursorDeleteSQL, input.CutoffISO, int32(input.Limit)).Scan(&deleted); err != nil {
		return 0, fmt.Errorf("delete completed runtime log file cursors: %w", err)
	}
	return deleted, nil
}

func parseRuntimeLogIndexRetentionDays(raw string) (int, bool) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, false
	}
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
		return 0, false
	}
	return int(number), true
}

func sortedRuntimeLogRetentionCounts(counts map[string]int64) ([]string, []int64) {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]int64, 0, len(keys))
	for _, key := range keys {
		values = append(values, counts[key])
	}
	return keys, values
}
