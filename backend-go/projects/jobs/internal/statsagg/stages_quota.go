package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 配额小时窗刷新，移植 usage-stats.repository.ts:1783-1951：
//   - refreshUsageQuotaHourlyWindowsCache（SQLite 分支，:1783-1793）+
//     usage-stats-snapshot-helpers.ts:8-42 refreshUsageQuotaHourlyWindowSnapshots：
//     读业务库 request_quota_hourly_window_scope_bindings 全量绑定，DELETE 全表后
//     按 200 一批重算 usage_quota_hourly_windows（usage_stats_hourly 按
//     stat_hour >= now-windowHours 汇总 total_cost_usd，零成本不落行）；
//   - refreshUsageQuotaHourlyWindowsCacheAsync（PG 分支，:1795-1951）：单事务内
//     先做小时翻转的 expiry 打脏（usage_quota_hourly_windows_expiry 游标，滑动
//     窗口推进后给仍有时数据的 scope 打脏标记），再按
//     first_dirty_at 顺序消费 <=128 个脏 scope（FOR UPDATE SKIP LOCKED）：
//     删除旧窗口行 → 按 LEFT JOIN bindings 的活跃窗口重算 → 按 generation
//     匹配删除已消费脏行，hasMore = 本轮消费满 scopeLimit。
//
// 消费方 gateway gatewayquota/costs.go LoadCosts 按
// (system_account_id, scope_type, scope_id, window_hours) 读小时窗成本；
// D-229 的失败可见性：刷新失败写 stats_job_state(global,'',
// usage-quota-hourly-windows-refresh) 的 last_error_message，成功清空并记
// last_success_at（Node 调度层失败处理的 Go 同进程等价）。

const (
	// QuotaHourlyWindowMaxHours mirrors maxRequestQuotaHourlyWindowHours
	//（request-quota-limits.ts，gateway gatewayquota.Limits 同值）。
	QuotaHourlyWindowMaxHours = 24 * 30
	// quotaHourlyWindowExpiryJobName mirrors stats_job_state 的
	// usage_quota_hourly_windows_expiry 游标行（usage-stats.repository.ts:1809）。
	quotaHourlyWindowExpiryJobName = "usage_quota_hourly_windows_expiry"
	// QuotaHourlyWindowRefreshJobName 是 Go 侧刷新任务名（调度与
	// stats_job_state 失败记录共用）。
	QuotaHourlyWindowRefreshJobName = "usage-quota-hourly-windows-refresh"
	// quotaHourlyWindowDefaultScopeLimit mirrors :1871 scopeLimit = 128。
	quotaHourlyWindowDefaultScopeLimit = 128
	// quotaHourlyWindowRebuildBatchSize mirrors snapshot-helpers :11 的 200 一批。
	quotaHourlyWindowRebuildBatchSize = 200

	quotaHourlyWindowHourMs = int64(time.Hour / time.Millisecond)
)

// QuotaHourlyWindowRefreshResult mirrors :1795 的 { changed; hasMore }。
type QuotaHourlyWindowRefreshResult struct {
	Changed bool
	HasMore bool
}

// scopeBinding mirrors request-quota-hourly-windows.repository.ts
// RequestQuotaHourlyWindowScopeBinding。
type scopeBinding struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	windowHours     int
}

func validQuotaHourlyWindowHours(hours int) bool {
	return hours >= 1 && hours <= QuotaHourlyWindowMaxHours
}

// RunQuotaHourlyWindows 按数据库模式分派：PG 走脏范围增量消费（归档 async
// 语义），SQLite 走全量重建（归档同步语义）。返回值仅在 PG 分支有 hasMore
// 语义；SQLite 全量重建固定 {Changed: true, HasMore: false}（:1796-1799）。
func (w *WindowRefresher) RunQuotaHourlyWindows(ctx context.Context) (QuotaHourlyWindowRefreshResult, error) {
	var result QuotaHourlyWindowRefreshResult
	err := w.runQuotaHourlyWindows(ctx, &result)
	if err != nil {
		w.recordQuotaHourlyWindowJobState(ctx, &result, err)
		return QuotaHourlyWindowRefreshResult{}, err
	}
	w.recordQuotaHourlyWindowJobState(ctx, &result, nil)
	return result, nil
}

func (w *WindowRefresher) runQuotaHourlyWindows(ctx context.Context, result *QuotaHourlyWindowRefreshResult) error {
	timezone, err := w.Clock.StatsTimezone(ctx)
	if err != nil {
		return err
	}
	if w.Dialect.Postgres {
		return w.refreshQuotaHourlyWindowsIncremental(ctx, timezone, result)
	}
	// :1796-1799 非 PG 分支固定 {changed: true, hasMore: false}。
	if err := w.refreshQuotaHourlyWindowsRebuild(ctx, timezone); err != nil {
		return err
	}
	result.Changed = true
	return nil
}

// businessDB 返回业务库句柄：PG 与 stats 同池复用 DB；SQLite 由组合根注入
// 独立业务库连接（全量重建读绑定表必需）。
func (w *WindowRefresher) businessDB() *sql.DB {
	if w.BusinessDB != nil {
		return w.BusinessDB
	}
	return w.DB
}

func (w *WindowRefresher) scopeLimit() int {
	if w.ScopeLimit > 0 {
		return w.ScopeLimit
	}
	return quotaHourlyWindowDefaultScopeLimit
}

// ---- SQLite 全量重建（refreshUsageQuotaHourlyWindowsCache + helpers :8-42）----

func (w *WindowRefresher) refreshQuotaHourlyWindowsRebuild(ctx context.Context, timezone *time.Location) error {
	bindings, err := w.listQuotaHourlyWindowScopeBindings(ctx)
	if err != nil {
		return err
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, w.Dialect.bind(`DELETE FROM `+w.Dialect.StatsTable("usage_quota_hourly_windows"))); err != nil {
		return err
	}
	nowMs := w.now().UnixMilli()
	for offset := 0; offset < len(bindings); offset += quotaHourlyWindowRebuildBatchSize {
		end := offset + quotaHourlyWindowRebuildBatchSize
		if end > len(bindings) {
			end = len(bindings)
		}
		chunk := bindings[offset:end]
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, CAST(? AS INTEGER), ?), ", len(chunk)), ", ")
		query := w.Dialect.bind(`
			WITH claimed(system_account_id, scope_type, scope_id, window_hours, cutoff_hour) AS (
				VALUES ` + values + `
			)
			INSERT INTO ` + w.Dialect.StatsTable("usage_quota_hourly_windows") + ` (
				system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
			)
			SELECT claimed.system_account_id, claimed.scope_type, claimed.scope_id, claimed.window_hours,
				COALESCE(SUM(hourly.total_cost_usd), 0), ?
			FROM claimed
			LEFT JOIN ` + w.Dialect.StatsTable("usage_stats_hourly") + ` hourly
				ON hourly.system_account_id = claimed.system_account_id
				AND hourly.scope_type = claimed.scope_type
				AND hourly.scope_id = claimed.scope_id
				AND hourly.stat_hour >= claimed.cutoff_hour
			GROUP BY claimed.system_account_id, claimed.scope_type, claimed.scope_id, claimed.window_hours
			HAVING COALESCE(SUM(hourly.total_cost_usd), 0) > 0
		`)
		updatedAt := FormatRFC3339Millis(w.now())
		args := make([]any, 0, len(chunk)*5+1)
		for _, binding := range chunk {
			args = append(args,
				binding.systemAccountID,
				binding.scopeType,
				binding.scopeID,
				binding.windowHours,
				hourKey(time.UnixMilli(nowMs-int64(binding.windowHours)*quotaHourlyWindowHourMs), timezone))
		}
		args = append(args, updatedAt)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// listQuotaHourlyWindowScopeBindings mirrors listRequestQuotaHourlyWindowScopeBindings
// （request-quota-hourly-windows.repository.ts:146-167，含 1..720 过滤）。
func (w *WindowRefresher) listQuotaHourlyWindowScopeBindings(ctx context.Context) ([]scopeBinding, error) {
	query := w.Dialect.bind(`
		SELECT system_account_id, scope_type, scope_id, window_hours
		FROM ` + w.Dialect.BusinessTable("request_quota_hourly_window_scope_bindings") + `
		ORDER BY window_hours ASC, system_account_id ASC, scope_type ASC, scope_id ASC
	`)
	rows, err := w.businessDB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []scopeBinding{}
	for rows.Next() {
		var systemAccountID, scopeType, scopeID sql.NullString
		var windowHours int64
		if err := rows.Scan(&systemAccountID, &scopeType, &scopeID, &windowHours); err != nil {
			return nil, err
		}
		hours := int(windowHours)
		if !validQuotaHourlyWindowHours(hours) {
			continue
		}
		bindings = append(bindings, scopeBinding{
			systemAccountID: systemAccountID.String,
			scopeType:       scopeType.String,
			scopeID:         scopeID.String,
			windowHours:     hours,
		})
	}
	return bindings, rows.Err()
}

// ---- PG 增量消费（refreshUsageQuotaHourlyWindowsCacheAsync :1802-1950）----
//
// 语句按 Dialect.bind 双模生成：除 PG 专属的 FOR UPDATE SKIP LOCKED 外，
// 同一 SQL 在 SQLite 上可执行（包测试用 SQLite 承载增量编排语义）。

type quotaHourlyWindowExpiryState struct {
	cursorCreatedAt sql.NullString
	cursorID        sql.NullString
}

func (w *WindowRefresher) refreshQuotaHourlyWindowsIncremental(ctx context.Context, timezone *time.Location, result *QuotaHourlyWindowRefreshResult) error {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := w.now()
	nowMs := now.UnixMilli()
	updatedAt := FormatRFC3339Millis(now)
	currentHour := hourKey(now, timezone)

	expiry, err := w.quotaHourlyWindowExpiryState(ctx, tx)
	if err != nil {
		return err
	}
	if expiry.cursorID.String != currentHour {
		if err := w.markQuotaHourlyWindowExpiryRanges(ctx, tx, expiry, nowMs, currentHour, updatedAt, timezone); err != nil {
			return err
		}
	}

	dirtyScopes, err := w.claimQuotaHourlyWindowDirtyScopes(ctx, tx)
	if err != nil {
		return err
	}
	if len(dirtyScopes) == 0 {
		// :1890 无脏范围 → changed=false / hasMore=false。
		return tx.Commit()
	}
	if err := w.deleteQuotaHourlyWindowsForScopes(ctx, tx, dirtyScopes); err != nil {
		return err
	}
	if err := w.rebuildQuotaHourlyWindowsForScopes(ctx, tx, dirtyScopes, nowMs, updatedAt, timezone); err != nil {
		return err
	}
	if err := w.deleteConsumedQuotaHourlyWindowDirtyScopes(ctx, tx, dirtyScopes); err != nil {
		return err
	}
	result.Changed = true
	result.HasMore = len(dirtyScopes) >= w.scopeLimit()
	return tx.Commit()
}

// quotaHourlyWindowExpiryState mirrors :1807-1810 的游标读取。
func (w *WindowRefresher) quotaHourlyWindowExpiryState(ctx context.Context, tx *sql.Tx) (quotaHourlyWindowExpiryState, error) {
	query := w.Dialect.bind(`SELECT cursor_created_at, cursor_id FROM ` + w.Dialect.StatsTable("stats_job_state") +
		` WHERE scope_type = ? AND scope_id = ? AND job_name = ?`)
	state := quotaHourlyWindowExpiryState{}
	err := tx.QueryRowContext(ctx, query, RankSnapshotJobStateScopeType, RankSnapshotJobStateScopeID, quotaHourlyWindowExpiryJobName).Scan(&state.cursorCreatedAt, &state.cursorID)
	if errors.Is(err, sql.ErrNoRows) {
		return quotaHourlyWindowExpiryState{}, nil
	}
	return state, err
}

// markQuotaHourlyWindowExpiryRanges mirrors :1811-1869：小时翻转后按活跃
// window_hours 计算滑动截止区间，给区间内仍有时数据的 scope 打脏，并推进
// expiry 游标。
func (w *WindowRefresher) markQuotaHourlyWindowExpiryRanges(ctx context.Context, tx *sql.Tx, expiry quotaHourlyWindowExpiryState, nowMs int64, currentHour, updatedAt string, timezone *time.Location) error {
	var previousBoundaryMs int64
	if expiry.cursorCreatedAt.Valid && expiry.cursorCreatedAt.String != "" {
		parsed, ok := RFC3339Milliseconds(expiry.cursorCreatedAt.String)
		if !ok {
			// :1816-1818 非法游标 fail closed（文案逐字对齐）。
			return fmt.Errorf("用量统计 expiry cursor_created_at 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", expiry.cursorCreatedAt.String)
		}
		previousBoundaryMs = parsed
		if previousBoundaryMs > nowMs {
			previousBoundaryMs = nowMs
		}
	} else {
		previousBoundaryMs = nowMs - quotaHourlyWindowHourMs
	}
	activeWindows, err := w.listActiveQuotaHourlyWindowHours(ctx, tx)
	if err != nil {
		return err
	}
	type expiryRange struct {
		windowHours      int
		previousCutoffHr string
		currentCutoffHr  string
	}
	ranges := make([]expiryRange, 0, len(activeWindows))
	for _, hours := range activeWindows {
		rangeValue := expiryRange{
			windowHours:      hours,
			previousCutoffHr: hourKey(time.UnixMilli(previousBoundaryMs-int64(hours)*quotaHourlyWindowHourMs), timezone),
			currentCutoffHr:  hourKey(time.UnixMilli(nowMs-int64(hours)*quotaHourlyWindowHourMs), timezone),
		}
		if rangeValue.previousCutoffHr < rangeValue.currentCutoffHr {
			ranges = append(ranges, rangeValue)
		}
	}
	if len(ranges) > 0 {
		values := strings.TrimSuffix(strings.Repeat("(CAST(? AS INTEGER), ?, ?), ", len(ranges)), ", ")
		query := w.Dialect.bind(`
			WITH expired_ranges(window_hours, previous_cutoff_hour, current_cutoff_hour) AS (VALUES ` + values + `)
			INSERT INTO ` + w.Dialect.StatsTable("usage_quota_hourly_window_dirty_scopes") + ` (
				system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
			)
			SELECT DISTINCT bindings.system_account_id, bindings.scope_type, bindings.scope_id, 1, ?, ?
			FROM ` + w.Dialect.BusinessTable("request_quota_hourly_window_scope_bindings") + ` bindings
			INNER JOIN expired_ranges ranges ON ranges.window_hours = bindings.window_hours
			WHERE EXISTS (
				SELECT 1
				FROM ` + w.Dialect.StatsTable("usage_stats_hourly") + ` hourly
				WHERE hourly.system_account_id = bindings.system_account_id
					AND hourly.scope_type = bindings.scope_type
					AND hourly.scope_id = bindings.scope_id
					AND hourly.stat_hour >= ranges.previous_cutoff_hour
					AND hourly.stat_hour < ranges.current_cutoff_hour
			)
			ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
				generation = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.generation + 1,
				updated_at = excluded.updated_at
		`)
		args := make([]any, 0, len(ranges)*3+2)
		for _, rangeValue := range ranges {
			args = append(args, rangeValue.windowHours, rangeValue.previousCutoffHr, rangeValue.currentCutoffHr)
		}
		args = append(args, updatedAt, updatedAt)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	// :1859-1868 推进 expiry 游标（不含 last_error_message/lag_seconds 列，
	// 逐字对齐归档列集）。
	upsert := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("stats_job_state") + ` (
			scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
			cursor_created_at = excluded.cursor_created_at,
			cursor_id = excluded.cursor_id,
			last_success_at = excluded.last_success_at,
			updated_at = excluded.updated_at
	`)
	_, err = tx.ExecContext(ctx, upsert,
		RankSnapshotJobStateScopeType, RankSnapshotJobStateScopeID, quotaHourlyWindowExpiryJobName,
		updatedAt, currentHour, updatedAt, updatedAt)
	return err
}

// listActiveQuotaHourlyWindowHours mirrors :1822-1828 的活跃窗口小时集合。
func (w *WindowRefresher) listActiveQuotaHourlyWindowHours(ctx context.Context, tx *sql.Tx) ([]int, error) {
	query := w.Dialect.bind(`SELECT DISTINCT window_hours FROM ` +
		w.Dialect.BusinessTable("request_quota_hourly_window_scope_bindings") + `
		WHERE window_hours BETWEEN 1 AND 720
		ORDER BY window_hours ASC
		LIMIT 720`)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hours := []int{}
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if validQuotaHourlyWindowHours(int(value)) {
			hours = append(hours, int(value))
		}
	}
	return hours, rows.Err()
}

type quotaHourlyWindowDirtyScope struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	generation      int64
	windowHours     int64 // 绑定缺失时为 0（LEFT JOIN 未命中）
}

// claimQuotaHourlyWindowDirtyScopes mirrors :1871-1889 的脏范围认领读取
// （PG 附加 FOR UPDATE OF dirty SKIP LOCKED）。
func (w *WindowRefresher) claimQuotaHourlyWindowDirtyScopes(ctx context.Context, tx *sql.Tx) ([]quotaHourlyWindowDirtyScope, error) {
	query := w.Dialect.bind(`
		SELECT dirty.system_account_id, dirty.scope_type, dirty.scope_id, dirty.generation,
			bindings.window_hours
		FROM ` + w.Dialect.StatsTable("usage_quota_hourly_window_dirty_scopes") + ` dirty
		LEFT JOIN ` + w.Dialect.BusinessTable("request_quota_hourly_window_scope_bindings") + ` bindings
			ON bindings.system_account_id = dirty.system_account_id
			AND bindings.scope_type = dirty.scope_type
			AND bindings.scope_id = dirty.scope_id
		ORDER BY dirty.first_dirty_at, dirty.system_account_id, dirty.scope_type, dirty.scope_id
		LIMIT ?`)
	if w.Dialect.Postgres {
		query += ` FOR UPDATE OF dirty SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, query, w.scopeLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := []quotaHourlyWindowDirtyScope{}
	for rows.Next() {
		var scope quotaHourlyWindowDirtyScope
		var systemAccountID, scopeType, scopeID sql.NullString
		var windowHours sql.NullInt64
		if err := rows.Scan(&systemAccountID, &scopeType, &scopeID, &scope.generation, &windowHours); err != nil {
			return nil, err
		}
		scope.systemAccountID = systemAccountID.String
		scope.scopeType = scopeType.String
		scope.scopeID = scopeID.String
		scope.windowHours = windowHours.Int64
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

// deleteQuotaHourlyWindowsForScopes mirrors :1894-1901：先删被认领 scope 的
// 全部旧窗口行（含已失效绑定残留），零成本 scope 由此归零消失。
func (w *WindowRefresher) deleteQuotaHourlyWindowsForScopes(ctx context.Context, tx *sql.Tx, scopes []quotaHourlyWindowDirtyScope) error {
	values := strings.TrimSuffix(strings.Repeat("(?, ?, ?), ", len(scopes)), ", ")
	query := w.Dialect.bind(`
		WITH claimed(system_account_id, scope_type, scope_id) AS (VALUES ` + values + `)
		DELETE FROM ` + w.Dialect.StatsTable("usage_quota_hourly_windows") + `
		WHERE EXISTS (
			SELECT 1 FROM claimed
			WHERE claimed.system_account_id = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_windows") + `.system_account_id
				AND claimed.scope_type = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_windows") + `.scope_type
				AND claimed.scope_id = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_windows") + `.scope_id
		)
	`)
	args := make([]any, 0, len(scopes)*3)
	for _, scope := range scopes {
		args = append(args, scope.systemAccountID, scope.scopeType, scope.scopeID)
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// rebuildQuotaHourlyWindowsForScopes mirrors :1903-1935：按 LEFT JOIN 绑定的
// 活跃窗口重算成本，零成本不落行。
func (w *WindowRefresher) rebuildQuotaHourlyWindowsForScopes(ctx context.Context, tx *sql.Tx, scopes []quotaHourlyWindowDirtyScope, nowMs int64, updatedAt string, timezone *time.Location) error {
	active := make([]quotaHourlyWindowDirtyScope, 0, len(scopes))
	for _, scope := range scopes {
		if validQuotaHourlyWindowHours(int(scope.windowHours)) {
			active = append(active, scope)
		}
	}
	if len(active) == 0 {
		return nil
	}
	values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, CAST(? AS INTEGER), ?), ", len(active)), ", ")
	query := w.Dialect.bind(`
		WITH claimed(system_account_id, scope_type, scope_id, window_hours, cutoff_hour) AS (VALUES ` + values + `)
		INSERT INTO ` + w.Dialect.StatsTable("usage_quota_hourly_windows") + ` (
			system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
		)
		SELECT claimed.system_account_id, claimed.scope_type, claimed.scope_id, claimed.window_hours,
			COALESCE(SUM(hourly.total_cost_usd), 0), ?
		FROM claimed
		LEFT JOIN ` + w.Dialect.StatsTable("usage_stats_hourly") + ` hourly
			ON hourly.system_account_id = claimed.system_account_id
			AND hourly.scope_type = claimed.scope_type
			AND hourly.scope_id = claimed.scope_id
			AND hourly.stat_hour >= claimed.cutoff_hour
		GROUP BY claimed.system_account_id, claimed.scope_type, claimed.scope_id, claimed.window_hours
		HAVING COALESCE(SUM(hourly.total_cost_usd), 0) > 0
		ON CONFLICT(system_account_id, scope_type, scope_id, window_hours) DO UPDATE SET
			total_cost_usd = excluded.total_cost_usd,
			updated_at = excluded.updated_at
	`)
	args := make([]any, 0, len(active)*5+1)
	for _, scope := range active {
		args = append(args,
			scope.systemAccountID,
			scope.scopeType,
			scope.scopeID,
			int(scope.windowHours),
			hourKey(time.UnixMilli(nowMs-scope.windowHours*quotaHourlyWindowHourMs), timezone))
	}
	args = append(args, updatedAt)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// deleteConsumedQuotaHourlyWindowDirtyScopes mirrors :1937-1945：按
// (scope, generation) 匹配删除已消费脏行，认领后新写入的更高 generation 保留。
func (w *WindowRefresher) deleteConsumedQuotaHourlyWindowDirtyScopes(ctx context.Context, tx *sql.Tx, scopes []quotaHourlyWindowDirtyScope) error {
	values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, CAST(? AS BIGINT)), ", len(scopes)), ", ")
	query := w.Dialect.bind(`
		WITH claimed(system_account_id, scope_type, scope_id, generation) AS (VALUES ` + values + `)
		DELETE FROM ` + w.Dialect.StatsTable("usage_quota_hourly_window_dirty_scopes") + `
		WHERE EXISTS (
			SELECT 1 FROM claimed
			WHERE claimed.system_account_id = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.system_account_id
				AND claimed.scope_type = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.scope_type
				AND claimed.scope_id = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.scope_id
				AND claimed.generation = ` + w.Dialect.qualifiedTarget("usage_quota_hourly_window_dirty_scopes") + `.generation
		)
	`)
	args := make([]any, 0, len(scopes)*4)
	for _, scope := range scopes {
		args = append(args, scope.systemAccountID, scope.scopeType, scope.scopeID, scope.generation)
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// ---- stats_job_state 失败/成功记录（D-229）----
//
// 独立于刷新事务写入：失败时刷新事务已回滚，失败本身仍需可观测。

func (w *WindowRefresher) recordQuotaHourlyWindowJobState(ctx context.Context, result *QuotaHourlyWindowRefreshResult, refreshErr error) {
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	query := w.Dialect.bind(`
		INSERT INTO ` + w.Dialect.StatsTable("stats_job_state") + ` (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
		VALUES (?, ?, ?, NULL, NULL, ?, ?, NULL, ?)
		ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
		  cursor_created_at = NULL,
		  cursor_id = NULL,
		  last_success_at = COALESCE(excluded.last_success_at, ` + w.Dialect.qualifiedTarget("stats_job_state") + `.last_success_at),
		  last_error_message = excluded.last_error_message,
		  lag_seconds = NULL,
		  updated_at = excluded.updated_at
	`)
	now := FormatRFC3339Millis(w.now())
	var lastSuccessAt, lastErrorMessage any
	if refreshErr == nil {
		lastSuccessAt = now
	} else {
		lastErrorMessage = refreshErr.Error()
	}
	if _, err := tx.ExecContext(ctx, query,
		RankSnapshotJobStateScopeType, RankSnapshotJobStateScopeID, QuotaHourlyWindowRefreshJobName,
		lastSuccessAt, lastErrorMessage, now); err != nil {
		return
	}
	_ = tx.Commit()
}
