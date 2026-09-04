package statsverify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Client-ip usage range windows mirroring
// storage/client-ip-usage-range-windows.repository.ts.
//
// Window derivation (clientIpRangeWindowsForTimezone): three windows over
// the fixed 31-day calendar — today, last 7 days, full 31 days — de-duplicated.
// Dirty claim (takeClientIpRangeWindowDirtyIpHashes): union of both dirty
// tables ordered by MIN(first_dirty_at), ip_hash; claimed generations are
// CAS-deleted so concurrent dirty writes survive (dirty generation CAS).
// Refresh (refreshClientIpUsageRangeWindow*): DELETE + INSERT..SELECT with a
// HAVING clause that keeps only ip hashes with any positive metric; windows
// become "ready" through the stats_job_state last_success_at flag only when
// no dirty hashes remain.
const (
	clientIpRangeWindowJobName   = "client_ip_range_window_refresh"
	clientIpRangeWindowScopeType = "client_ip_range_window"

	clientIpRangeWindowDirtyLimit = 1000
	clientIpRangeWindowChunkSize  = 200
)

// ClientIPRangeWindow is one [start,end] calendar window in stats-timezone
// date keys.
type ClientIPRangeWindow struct {
	StartDate string
	EndDate   string
}

// ClientIPRangeWindowsForTimezone exposes the window derivation for tests
// and diagnostics (mirrors clientIpRangeWindowsForTimezone).
func ClientIPRangeWindowsForTimezone(location *time.Location, now time.Time) []ClientIPRangeWindow {
	return clientIPRangeWindowsForTimezone(location, now)
}

func clientIPRangeWindowsForTimezone(location *time.Location, now time.Time) []ClientIPRangeWindow {
	todayKey := DateKeyIn(now, location)
	dates := FixedUsageStatsDateKeys(location, todayKey)
	if len(dates) == 0 {
		return nil
	}
	candidates := []ClientIPRangeWindow{
		{StartDate: todayKey, EndDate: todayKey},
		{StartDate: dates[len(dates)-7], EndDate: todayKey},
		{StartDate: dates[0], EndDate: todayKey},
	}
	seen := make(map[string]struct{})
	result := make([]ClientIPRangeWindow, 0, len(candidates))
	for _, window := range candidates {
		key := window.StartDate + ":" + window.EndDate
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, window)
	}
	return result
}

func clientIpRangeWindowScopeID(startDate, endDate string) string {
	return startDate + ":" + endDate
}

// ClientIPRangeWindowRefreshOptions mirrors the refreshClientIpUsageRangeWindows
// options plus the injected clock instant.
type ClientIPRangeWindowRefreshOptions struct {
	Full       bool
	DirtyLimit int
	Now        time.Time
}

// RefreshClientIPUsageRangeWindows runs one dirty-claim refresh cycle inside
// a single transaction (mirrors refreshClientIpUsageRangeWindowsAsync).
func (s *Store) RefreshClientIPUsageRangeWindows(ctx context.Context, options ClientIPRangeWindowRefreshOptions) error {
	if s == nil || s.db == nil {
		return errors.New("statsverify store 未初始化")
	}
	now := options.Now
	location, _, err := s.LoadUsageStatsLocation(ctx, now)
	if err != nil {
		return err
	}
	windows := clientIPRangeWindowsForTimezone(location, now)
	if len(windows) == 0 {
		return nil
	}
	updatedAt := NowIso(now)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	dirtyLimit := options.DirtyLimit
	if dirtyLimit <= 0 {
		dirtyLimit = clientIpRangeWindowDirtyLimit
	}
	claim, err := s.takeClientIPRangeWindowDirty(ctx, tx, options.Full, dirtyLimit)
	if err != nil {
		return err
	}

	if options.Full {
		for _, window := range windows {
			if err := s.refreshClientIPAccountUsageRangeWindow(ctx, tx, window, updatedAt); err != nil {
				return err
			}
			if err := s.refreshClientIPUsageRangeWindow(ctx, tx, window, updatedAt); err != nil {
				return err
			}
		}
		if err := s.clearClientIPRangeWindowDirty(ctx, tx, claim); err != nil {
			return err
		}
		s.forgetDirtyIPHashes(claim.ipHashes)
		return tx.Commit()
	}

	if len(claim.ipHashes) == 0 {
		stale, err := s.hasStaleClientIPUsageRangeWindows(ctx, tx, windows)
		if err != nil {
			return err
		}
		if stale {
			for _, window := range windows {
				if err := s.refreshClientIPAccountUsageRangeWindow(ctx, tx, window, updatedAt); err != nil {
					return err
				}
				if err := s.refreshClientIPUsageRangeWindow(ctx, tx, window, updatedAt); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	}

	for _, window := range windows {
		if err := s.refreshClientIPUsageRangeWindowForIPs(ctx, tx, window, claim.ipHashes, updatedAt); err != nil {
			return err
		}
		if err := s.refreshClientIPAccountUsageRangeWindowForIPs(ctx, tx, window, claim.ipHashes, updatedAt); err != nil {
			return err
		}
	}
	if err := s.clearClientIPRangeWindowDirty(ctx, tx, claim); err != nil {
		return err
	}
	s.forgetDirtyIPHashes(claim.ipHashes)

	pending, err := s.hasPendingClientIPRangeWindowDirty(ctx, tx)
	if err != nil {
		return err
	}
	if !pending {
		if err := s.markClientIPUsageRangeWindowsReady(ctx, tx, windows, updatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type clientIPRangeWindowDirtyClaim struct {
	ipHashes     []string
	clientIPRows []clientIPRangeWindowDirtyRow
	accountRows  []clientIPRangeWindowDirtyRow
}

type clientIPRangeWindowDirtyRow struct {
	ipHash     string
	generation int64
}

// takeClientIPRangeWindowDirty mirrors takeClientIpRangeWindowDirtyIpHashes:
// candidates from the UNION of both dirty tables ordered by
// MIN(first_dirty_at), ip_hash (optionally LIMIT), then the claimed rows are
// re-read with their generations (FOR UPDATE SKIP LOCKED on PostgreSQL).
func (s *Store) takeClientIPRangeWindowDirty(ctx context.Context, tx *sql.Tx, full bool, limit int) (clientIPRangeWindowDirtyClaim, error) {
	limitClause := ""
	args := []any{}
	if !full {
		limitClause = "LIMIT " + s.placeholder(1)
		args = append(args, limit)
	}
	candidateQuery := fmt.Sprintf(`
		SELECT ip_hash
		FROM (
		  SELECT ip_hash, first_dirty_at FROM %s
		  UNION ALL
		  SELECT ip_hash, first_dirty_at FROM %s
		) dirty
		GROUP BY ip_hash
		ORDER BY MIN(first_dirty_at) ASC, ip_hash ASC
		%s
	`, s.statsTable("client_ip_range_window_dirty_ips"),
		s.statsTable("client_ip_account_range_window_dirty_ips"),
		limitClause)
	candidateRows, err := tx.QueryContext(ctx, candidateQuery, args...)
	if err != nil {
		return clientIPRangeWindowDirtyClaim{}, fmt.Errorf("读取 client-ip 窗口 dirty 候选失败: %w", err)
	}
	ipHashes := make([]string, 0, 16)
	for candidateRows.Next() {
		var ipHash sql.NullString
		if err := candidateRows.Scan(&ipHash); err != nil {
			_ = candidateRows.Close()
			return clientIPRangeWindowDirtyClaim{}, err
		}
		if ipHash.Valid && strings.TrimSpace(ipHash.String) != "" {
			ipHashes = append(ipHashes, strings.TrimSpace(ipHash.String))
		}
	}
	if err := candidateRows.Err(); err != nil {
		_ = candidateRows.Close()
		return clientIPRangeWindowDirtyClaim{}, err
	}
	_ = candidateRows.Close()
	if len(ipHashes) == 0 {
		return clientIPRangeWindowDirtyClaim{}, nil
	}

	lockClause := ""
	if s.mode == StorePostgres {
		lockClause = " FOR UPDATE SKIP LOCKED"
	}
	clientIPRows, err := s.readDirtyRows(ctx, tx, s.statsTable("client_ip_range_window_dirty_ips"), ipHashes, lockClause)
	if err != nil {
		return clientIPRangeWindowDirtyClaim{}, err
	}
	accountRows, err := s.readDirtyRows(ctx, tx, s.statsTable("client_ip_account_range_window_dirty_ips"), ipHashes, lockClause)
	if err != nil {
		return clientIPRangeWindowDirtyClaim{}, err
	}

	claim := clientIPRangeWindowDirtyClaim{
		clientIPRows: clientIPRows,
		accountRows:  accountRows,
	}
	seen := make(map[string]struct{})
	for _, row := range clientIPRows {
		if _, ok := seen[row.ipHash]; !ok {
			seen[row.ipHash] = struct{}{}
			claim.ipHashes = append(claim.ipHashes, row.ipHash)
		}
	}
	for _, row := range accountRows {
		if _, ok := seen[row.ipHash]; !ok {
			seen[row.ipHash] = struct{}{}
			claim.ipHashes = append(claim.ipHashes, row.ipHash)
		}
	}
	s.rememberDirtyIPHashes(claim.ipHashes)
	return claim, nil
}

func (s *Store) readDirtyRows(ctx context.Context, tx *sql.Tx, table string, ipHashes []string, lockClause string) ([]clientIPRangeWindowDirtyRow, error) {
	result := make([]clientIPRangeWindowDirtyRow, 0)
	for _, chunk := range chunkStrings(ipHashes, clientIpRangeWindowChunkSize) {
		query := fmt.Sprintf(`
			SELECT ip_hash, generation
			FROM %s
			WHERE ip_hash IN (%s)%s
		`, table, s.placeholders(len(chunk)), lockClause)
		args := make([]any, 0, len(chunk))
		for _, ipHash := range chunk {
			args = append(args, ipHash)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", table, err)
		}
		for rows.Next() {
			var ipHash string
			var generation any
			if err := rows.Scan(&ipHash, &generation); err != nil {
				_ = rows.Close()
				return nil, err
			}
			generationInt, err := sqlInt(generation)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			if strings.TrimSpace(ipHash) == "" || generationInt <= 0 {
				continue
			}
			result = append(result, clientIPRangeWindowDirtyRow{ipHash: strings.TrimSpace(ipHash), generation: generationInt})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return result, nil
}

// clearClientIPRangeWindowDirty mirrors clearClientIpRangeWindowDirtyIpHashes:
// CAS delete by (ip_hash, generation) so a generation bumped between claim
// and clear survives for the next cycle.
func (s *Store) clearClientIPRangeWindowDirty(ctx context.Context, tx *sql.Tx, claim clientIPRangeWindowDirtyClaim) error {
	if err := s.clearClientIPRangeWindowDirtyRows(ctx, tx, s.statsTable("client_ip_range_window_dirty_ips"), claim.clientIPRows); err != nil {
		return err
	}
	return s.clearClientIPRangeWindowDirtyRows(ctx, tx, s.statsTable("client_ip_account_range_window_dirty_ips"), claim.accountRows)
}

func (s *Store) clearClientIPRangeWindowDirtyRows(ctx context.Context, tx *sql.Tx, table string, rows []clientIPRangeWindowDirtyRow) error {
	for _, chunk := range chunkDirtyRows(rows, clientIpRangeWindowChunkSize) {
		var query string
		var args []any
		if s.mode == StorePostgres {
			claimedValues := make([]string, 0, len(chunk))
			for index, row := range chunk {
				claimedValues = append(claimedValues, fmt.Sprintf("(%s, %s::bigint)", s.placeholder(2*index+1), s.placeholder(2*index+2)))
				args = append(args, row.ipHash, row.generation)
			}
			query = fmt.Sprintf(`
				DELETE FROM %s AS dirty
				USING (VALUES %s) AS claimed(ip_hash, generation)
				WHERE dirty.ip_hash = claimed.ip_hash
				  AND dirty.generation = claimed.generation
			`, table, strings.Join(claimedValues, ", "))
		} else {
			predicates := make([]string, 0, len(chunk))
			for _, row := range chunk {
				predicates = append(predicates, "(ip_hash = ? AND generation = ?)")
				args = append(args, row.ipHash, row.generation)
			}
			query = fmt.Sprintf(`DELETE FROM %s WHERE %s`, table, strings.Join(predicates, " OR "))
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", table, err)
		}
	}
	return nil
}

// hasStaleClientIPUsageRangeWindows mirrors hasStaleClientIpUsageRangeWindows:
// a window is stale when its stats_job_state row exists with a NULL
// last_success_at.
func (s *Store) hasStaleClientIPUsageRangeWindows(ctx context.Context, tx *sql.Tx, windows []ClientIPRangeWindow) (bool, error) {
	for _, window := range windows {
		query := fmt.Sprintf(`
			SELECT last_success_at
			FROM %s
			WHERE scope_type = %s AND scope_id = %s AND job_name = %s
			LIMIT 1
		`, s.statsTable("stats_job_state"),
			s.placeholder(1), s.placeholder(2), s.placeholder(3))
		var lastSuccessAt sql.NullString
		err := tx.QueryRowContext(ctx, query,
			clientIpRangeWindowScopeType,
			clientIpRangeWindowScopeID(window.StartDate, window.EndDate),
			clientIpRangeWindowJobName).Scan(&lastSuccessAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("读取 client-ip 窗口 state 失败: %w", err)
		}
		if !lastSuccessAt.Valid || lastSuccessAt.String == "" {
			return true, nil
		}
	}
	return false, nil
}

// hasPendingClientIPRangeWindowDirty mirrors hasPendingClientIpRangeWindowDirtyIpHashes:
// in-process sets short-circuit, then both dirty tables are checked.
func (s *Store) hasPendingClientIPRangeWindowDirty(ctx context.Context, tx *sql.Tx) (bool, error) {
	if s.hasInMemoryDirtyIPHashes() {
		return true, nil
	}
	for _, table := range []string{"client_ip_range_window_dirty_ips", "client_ip_account_range_window_dirty_ips"} {
		query := fmt.Sprintf(`SELECT 1 FROM %s LIMIT 1`, s.statsTable(table))
		var one int
		err := tx.QueryRowContext(ctx, query).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("检查 %s pending 失败: %w", table, err)
		}
		return true, nil
	}
	return false, nil
}

func (s *Store) markClientIPUsageRangeWindowsReady(ctx context.Context, tx execer, windows []ClientIPRangeWindow, updatedAt string) error {
	for _, window := range windows {
		if err := s.markClientIPUsageRangeWindowReady(ctx, tx, window, updatedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) markClientIPUsageRangeWindowReady(ctx context.Context, tx execer, window ClientIPRangeWindow, updatedAt string) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (scope_type, scope_id, job_name, last_success_at, updated_at)
		VALUES (%s, %s, %s, %s, %s)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
		  last_success_at = EXCLUDED.last_success_at,
		  last_error_message = NULL,
		  updated_at = EXCLUDED.updated_at
	`, s.statsTable("stats_job_state"),
		s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4), s.placeholder(5))
	_, err := tx.ExecContext(ctx, query,
		clientIpRangeWindowScopeType,
		clientIpRangeWindowScopeID(window.StartDate, window.EndDate),
		clientIpRangeWindowJobName,
		updatedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("标记 client-ip 窗口 ready 失败: %w", err)
	}
	return nil
}

const clientIPRangeWindowMetricSelect = `
	  COALESCE(SUM(request_count), 0),
	  COALESCE(SUM(success_count), 0),
	  COALESCE(SUM(error_count), 0),
	  COALESCE(SUM(input_tokens), 0),
	  COALESCE(SUM(output_tokens), 0),
	  COALESCE(SUM(cache_read_tokens), 0),
	  COALESCE(SUM(cache_read_cost_usd), 0),
	  COALESCE(SUM(cache_write_tokens), 0),
	  COALESCE(SUM(cache_write_1h_tokens), 0),
	  COALESCE(SUM(cache_write_cost_usd), 0),
	  COALESCE(SUM(thinking_tokens), 0),
	  COALESCE(SUM(input_image_tokens), 0),
	  COALESCE(SUM(output_image_tokens), 0),
	  COALESCE(SUM(total_cost_usd), 0),
	  COALESCE(SUM(duration_ms_sum), 0),
	  COALESCE(SUM(duration_ms_count), 0),
	  COALESCE(MAX(duration_ms_max), 0),
	  CASE WHEN COALESCE(SUM(duration_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(duration_ms_sum), 0) AS REAL) / COALESCE(SUM(duration_ms_count), 0) ELSE NULL END,
	  COALESCE(SUM(first_token_ms_sum), 0),
	  COALESCE(SUM(first_token_ms_count), 0),
	  CASE WHEN COALESCE(SUM(first_token_ms_count), 0) > 0 THEN CAST(COALESCE(SUM(first_token_ms_sum), 0) AS REAL) / COALESCE(SUM(first_token_ms_count), 0) ELSE NULL END,
	  COALESCE(SUM(CASE WHEN request_count > 0 THEN 1 ELSE 0 END), 0),
	  MAX(last_used_at),
	  MAX(last_error_at)
`

const clientIPRangeWindowHaving = `
	  HAVING COALESCE(SUM(request_count), 0) > 0
		OR COALESCE(SUM(input_tokens), 0) > 0
		OR COALESCE(SUM(output_tokens), 0) > 0
		OR COALESCE(SUM(cache_read_tokens), 0) > 0
		OR COALESCE(SUM(cache_read_cost_usd), 0) > 0
		OR COALESCE(SUM(cache_write_tokens), 0) > 0
		OR COALESCE(SUM(cache_write_1h_tokens), 0) > 0
		OR COALESCE(SUM(cache_write_cost_usd), 0) > 0
		OR COALESCE(SUM(thinking_tokens), 0) > 0
		OR COALESCE(SUM(input_image_tokens), 0) > 0
		OR COALESCE(SUM(output_image_tokens), 0) > 0
		OR COALESCE(SUM(total_cost_usd), 0) > 0
`

func (s *Store) refreshClientIPUsageRangeWindow(ctx context.Context, tx *sql.Tx, window ClientIPRangeWindow, updatedAt string) error {
	return s.refreshRangeWindow(ctx, tx,
		s.statsTable("client_ip_usage_range_windows"), s.statsTable("client_ip_stats_daily"),
		"", window, nil, updatedAt, true)
}

func (s *Store) refreshClientIPUsageRangeWindowForIPs(ctx context.Context, tx *sql.Tx, window ClientIPRangeWindow, ipHashes []string, updatedAt string) error {
	return s.refreshRangeWindow(ctx, tx,
		s.statsTable("client_ip_usage_range_windows"), s.statsTable("client_ip_stats_daily"),
		"", window, ipHashes, updatedAt, false)
}

func (s *Store) refreshClientIPAccountUsageRangeWindow(ctx context.Context, tx *sql.Tx, window ClientIPRangeWindow, updatedAt string) error {
	return s.refreshRangeWindow(ctx, tx,
		s.statsTable("client_ip_account_usage_range_windows"), s.statsTable("client_ip_account_stats_daily"),
		"account_id, ", window, nil, updatedAt, true)
}

func (s *Store) refreshClientIPAccountUsageRangeWindowForIPs(ctx context.Context, tx *sql.Tx, window ClientIPRangeWindow, ipHashes []string, updatedAt string) error {
	return s.refreshRangeWindow(ctx, tx,
		s.statsTable("client_ip_account_usage_range_windows"), s.statsTable("client_ip_account_stats_daily"),
		"account_id, ", window, ipHashes, updatedAt, false)
}

// refreshRangeWindow merges refreshClientIpUsageRangeWindow /
// refreshClientIpUsageRangeWindowForIps and their account counterparts.
// full=true deletes and rebuilds the entire window and (for the ip window)
// marks the window ready; full=false chunks dirty ip hashes at 200 with a
// per-chunk DELETE+INSERT pair.
func (s *Store) refreshRangeWindow(ctx context.Context, tx *sql.Tx, windowTable, sourceTable, accountColumns string, window ClientIPRangeWindow, ipHashes []string, updatedAt string, full bool) error {
	groupBy := "GROUP BY ip_hash"
	selectKeyColumns := "ip_hash"
	if accountColumns != "" {
		groupBy = "GROUP BY ip_hash, account_id"
		selectKeyColumns = "ip_hash,\n\t  account_id"
	}

	buildInsert := func(ipFilter string, firstParam int) string {
		startParam := s.placeholder(firstParam)
		endParam := s.placeholder(firstParam + 1)
		updatedParam := s.placeholder(firstParam + 2)
		startFilter := s.placeholder(firstParam + 3)
		endFilter := s.placeholder(firstParam + 4)
		return fmt.Sprintf(`
		INSERT INTO %s (
		  ip_hash, %sstart_date, end_date, request_count, success_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, average_duration_ms,
		  first_token_ms_sum, first_token_ms_count, average_first_token_ms,
		  active_days, last_used_at, last_error_at, updated_at
		)
		SELECT
		  %s,
		  %s,
		  %s,
%s,
		  %s
		FROM %s
		WHERE stat_date >= %s
		  AND stat_date <= %s
		  %s
		%s
		%s
		`,
			windowTable,
			accountColumns,
			selectKeyColumns,
			startParam,
			endParam,
			clientIPRangeWindowMetricSelect,
			updatedParam,
			sourceTable,
			startFilter,
			endFilter,
			ipFilter,
			groupBy,
			clientIPRangeWindowHaving)
	}

	if full {
		deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE start_date = %s AND end_date = %s`,
			windowTable, s.placeholder(1), s.placeholder(2))
		if _, err := tx.ExecContext(ctx, deleteQuery, window.StartDate, window.EndDate); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", windowTable, err)
		}
		insert := buildInsert("", 1)
		if _, err := tx.ExecContext(ctx, insert, window.StartDate, window.EndDate, updatedAt, window.StartDate, window.EndDate); err != nil {
			return fmt.Errorf("刷新 %s 失败: %w", windowTable, err)
		}
		if windowTable == s.statsTable("client_ip_usage_range_windows") {
			if err := s.markClientIPUsageRangeWindowReady(ctx, tx, window, updatedAt); err != nil {
				return err
			}
		}
		return nil
	}

	if len(ipHashes) == 0 {
		return nil
	}
	for _, chunk := range chunkStrings(ipHashes, clientIpRangeWindowChunkSize) {
		ipFilter := fmt.Sprintf("AND ip_hash IN (%s)", s.placeholders(len(chunk)))
		deleteQuery := fmt.Sprintf(`DELETE FROM %s WHERE start_date = %s AND end_date = %s %s`,
			windowTable, s.placeholder(1), s.placeholder(2), ipFilter)
		deleteArgs := []any{window.StartDate, window.EndDate}
		for _, ipHash := range chunk {
			deleteArgs = append(deleteArgs, ipHash)
		}
		if _, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...); err != nil {
			return fmt.Errorf("清理 %s 失败: %w", windowTable, err)
		}
		insert := buildInsert(ipFilter, 1)
		insertArgs := []any{window.StartDate, window.EndDate, updatedAt, window.StartDate, window.EndDate}
		for _, ipHash := range chunk {
			insertArgs = append(insertArgs, ipHash)
		}
		if _, err := tx.ExecContext(ctx, insert, insertArgs...); err != nil {
			return fmt.Errorf("刷新 %s 失败: %w", windowTable, err)
		}
	}
	return nil
}

func selectKeyColumnsAfter(accountColumns string) string {
	if accountColumns != "" {
		return "account_id, "
	}
	return ""
}

func chunkStrings(values []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}

func chunkDirtyRows(rows []clientIPRangeWindowDirtyRow, size int) [][]clientIPRangeWindowDirtyRow {
	if size < 1 {
		size = 1
	}
	chunks := make([][]clientIPRangeWindowDirtyRow, 0, (len(rows)+size-1)/size)
	for start := 0; start < len(rows); start += size {
		end := start + size
		if end > len(rows) {
			end = len(rows)
		}
		chunks = append(chunks, rows[start:end])
	}
	return chunks
}

func sortedSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
