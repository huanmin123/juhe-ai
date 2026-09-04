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

// Group account statistics refresh mirroring
// storage/group-account-stats-cache.repository.ts.
//
// Dirty protocol:
//   - a single row with group_id='__all__' (GROUP_ACCOUNT_STATS_DIRTY_ALL)
//     means "rebuild every group"; its reason field doubles as the cursor
//     ('all_cursor:<groupId>' prefix) so a full rebuild continues across
//     batches;
//   - per-group dirty rows are consumed oldest-updated first, ordered by
//     (updated_at, group_id), at most `limit` per run;
//   - when no dirty rows exist and the stats table is empty the job
//     self-seeds with markAllDirty('initial_cache_build');
//   - dirty rows are deleted with an updated_at CAS so a write that re-dirtied
//     the group during the refresh survives.
//
// Counting rules (refreshGroupAccountStatsCache): a group-account pair
// counts only when group_accounts.enabled=1, the account exists and is not
// deleted, and it is authorized — either through an active
// resource_authorization that has not expired, or because the account belongs
// to the group's own system account. status='rate_limited' increments both
// error and rate_limited, matching the Node accumulator.
const (
	groupAccountStatsDirtyAll        = "__all__"
	groupAccountStatsAllCursorPrefix = "all_cursor:"
	groupAccountStatsBatchLimit      = 1000

	groupStatsJobName = "group-account-stats-refresh"
)

// GroupAccountStatsRefreshOptions carries the batch limit and the injected
// clock instant.
type GroupAccountStatsRefreshOptions struct {
	Limit int
	Now   time.Time
}

// RefreshDirtyGroupAccountStats runs one dirty-consumption cycle and returns
// how many dirty rows were consumed (1 for a full-rebuild batch step, or the
// number of per-group dirty rows).
func (s *Store) RefreshDirtyGroupAccountStats(ctx context.Context, options GroupAccountStatsRefreshOptions) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("statsverify store 未初始化")
	}
	now := options.Now
	updatedAt := NowIso(now)
	limit := options.Limit
	if limit <= 0 {
		limit = groupAccountStatsBatchLimit
	}
	if limit > groupAccountStatsBatchLimit {
		limit = groupAccountStatsBatchLimit
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if s.mode == StorePostgres {
		return s.refreshDirtyGroupAccountStatsPostgres(ctx, limit, updatedAt, now)
	}
	return s.refreshDirtyGroupAccountStatsSQLite(ctx, limit, updatedAt, now)
}

// refreshDirtyGroupAccountStatsPostgres mirrors refreshDirtyGroupAccountStatsCacheAsync:
// the whole cycle runs in one transaction on the shared cluster.
func (s *Store) refreshDirtyGroupAccountStatsPostgres(ctx context.Context, limit int, updatedAt string, now time.Time) (int, error) {
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	refreshed, err := s.refreshDirtyGroupAccountStatsTx(ctx, tx, tx, limit, updatedAt, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return refreshed, nil
}

// refreshDirtyGroupAccountStatsSQLite mirrors
// refreshDirtyGroupAccountStatsCacheWithWriter: dirty state lives in the
// business database, the cache lives in the stats database, and there is no
// cross-database transaction (Node relies on the updated_at CAS for the
// same guarantee).
func (s *Store) refreshDirtyGroupAccountStatsSQLite(ctx context.Context, limit int, updatedAt string, now time.Time) (int, error) {
	businessTx, err := s.business.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开启 statsverify business 事务失败: %w", err)
	}
	defer businessTx.Rollback()
	statsTx, err := s.beginWriteTx(ctx)
	if err != nil {
		return 0, err
	}
	defer statsTx.Rollback()

	refreshed, err := s.refreshDirtyGroupAccountStatsTx(ctx, businessTx, statsTx, limit, updatedAt, now)
	if err != nil {
		return 0, err
	}
	if err := businessTx.Commit(); err != nil {
		return 0, err
	}
	if err := statsTx.Commit(); err != nil {
		return 0, err
	}
	return refreshed, nil
}

// refreshDirtyGroupAccountStatsTx is the shared dirty protocol. businessQ
// reads/writes the dirty tables; statsQ rewrites group_account_stats (and is
// also a queryer for the hasStats probe).
func (s *Store) refreshDirtyGroupAccountStatsTx(ctx context.Context, businessQ queryerExecer, statsQ queryerExecer, limit int, updatedAt string, now time.Time) (int, error) {
	allRows, err := s.loadAllGroupAccountStatsDirtyRows(ctx, businessQ)
	if err != nil {
		return 0, err
	}
	if len(allRows) > 0 {
		return s.refreshAllDirtyGroupAccountStatsBatch(ctx, businessQ, statsQ, allRows[0], limit, updatedAt, now)
	}

	rows, err := s.loadGroupAccountStatsDirtyRows(ctx, businessQ, limit)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		hasStats, err := s.hasGroupAccountStats(ctx, statsQ)
		if err != nil {
			return 0, err
		}
		if !hasStats {
			if err := s.markAllGroupAccountStatsDirty(ctx, businessQ, "initial_cache_build", updatedAt); err != nil {
				return 0, err
			}
			initialRows, err := s.loadAllGroupAccountStatsDirtyRows(ctx, businessQ)
			if err != nil {
				return 0, err
			}
			if len(initialRows) == 0 {
				return 0, nil
			}
			return s.refreshAllDirtyGroupAccountStatsBatch(ctx, businessQ, statsQ, initialRows[0], limit, updatedAt, now)
		}
		return 0, nil
	}

	if err := s.refreshGroupAccountStatsCache(ctx, businessQ, statsQ, groupIDs(rows), updatedAt, now); err != nil {
		return 0, err
	}
	if err := s.deleteGroupAccountStatsDirtyRows(ctx, businessQ, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// refreshAllDirtyGroupAccountStatsBatch mirrors refreshAllDirtyGroupAccountStatsCacheBatch:
// one page of groups per call; the cursor persists in the dirty row reason.
func (s *Store) refreshAllDirtyGroupAccountStatsBatch(ctx context.Context, businessQ queryerExecer, statsQ execer, dirtyRow groupAccountStatsDirtyRow, limit int, updatedAt string, now time.Time) (int, error) {
	cursorGroupID := groupAccountStatsAllCursor(dirtyRow.Reason)
	groups, err := s.loadGroupAccountStatsGroupsPage(ctx, businessQ, cursorGroupID, limit)
	if err != nil {
		return 0, err
	}
	if len(groups) == 0 {
		if err := s.deleteGroupAccountStatsDirtyRow(ctx, businessQ, dirtyRow); err != nil {
			return 0, err
		}
		return 1, nil
	}
	groupIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}
	if err := s.refreshGroupAccountStatsCache(ctx, businessQ, statsQ, groupIDs, updatedAt, now); err != nil {
		return 0, err
	}
	if len(groups) < limit {
		if err := s.deleteGroupAccountStatsDirtyRow(ctx, businessQ, dirtyRow); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if err := s.updateGroupAccountStatsAllCursor(ctx, businessQ, groups[len(groups)-1].ID, updatedAt); err != nil {
		return 0, err
	}
	return 1, nil
}

type groupAccountStatsDirtyRow struct {
	GroupID   string
	Reason    *string
	UpdatedAt string
}

type groupAccountStatsGroup struct {
	ID              string
	SystemAccountID string
}

type groupAccountStatsAccumulator struct {
	GroupID          string
	SystemAccountID  string
	Total            int
	Available        int
	Active           int
	Disabled         int
	Error            int
	RateLimited      int
	ConcurrencyLimit int64
}

// MarkAllGroupAccountStatsDirty mirrors markAllGroupAccountStatsDirtyAsync.
// The Node scheduler issues this once per stats-worker startup before the
// first refresh; hosts of this package call it explicitly.
func (s *Store) MarkAllGroupAccountStatsDirty(ctx context.Context, reason string, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("statsverify store 未初始化")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.mode == StorePostgres {
		tx, err := s.beginWriteTx(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := s.markGroupAccountStatsDirtyRows(ctx, tx, []string{groupAccountStatsDirtyAll}, reason, NowIso(now)); err != nil {
			return err
		}
		return tx.Commit()
	}
	businessTx, err := s.business.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer businessTx.Rollback()
	if err := s.markGroupAccountStatsDirtyRows(ctx, businessTx, []string{groupAccountStatsDirtyAll}, reason, NowIso(now)); err != nil {
		return err
	}
	return businessTx.Commit()
}

func (s *Store) markAllGroupAccountStatsDirty(ctx context.Context, q execer, reason, updatedAt string) error {
	return s.markGroupAccountStatsDirtyRows(ctx, q, []string{groupAccountStatsDirtyAll}, reason, updatedAt)
}

// markGroupAccountStatsDirtyRows mirrors markGroupAccountStatsDirtyAsync:
// last write wins for (reason, updated_at) per group id.
func (s *Store) markGroupAccountStatsDirtyRows(ctx context.Context, q execer, groupIDs []string, reason, updatedAt string) error {
	for _, groupID := range groupIDs {
		query := fmt.Sprintf(`
			INSERT INTO %s (group_id, reason, updated_at)
			VALUES (%s, %s, %s)
			ON CONFLICT(group_id) DO UPDATE SET
			  reason = EXCLUDED.reason,
			  updated_at = EXCLUDED.updated_at
		`, s.businessTable("group_account_stats_dirty"), s.placeholder(1), s.placeholder(2), s.placeholder(3))
		if _, err := q.ExecContext(ctx, query, groupID, reason, updatedAt); err != nil {
			return fmt.Errorf("标记 group_account_stats_dirty 失败: %w", err)
		}
	}
	return nil
}

func (s *Store) loadAllGroupAccountStatsDirtyRows(ctx context.Context, q queryer) ([]groupAccountStatsDirtyRow, error) {
	query := fmt.Sprintf(`
		SELECT group_id, reason, updated_at
		FROM %s
		WHERE group_id = %s
		LIMIT 1
	`, s.businessTable("group_account_stats_dirty"), s.placeholder(1))
	var groupID string
	var reason, updatedAt sql.NullString
	err := q.QueryRowContext(ctx, query, groupAccountStatsDirtyAll).Scan(&groupID, &reason, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 group_account_stats_dirty __all__ 失败: %w", err)
	}
	updatedText, err := sqlText(updatedAt)
	if err != nil {
		return nil, err
	}
	row := groupAccountStatsDirtyRow{GroupID: groupID, UpdatedAt: updatedText}
	if reason.Valid {
		reasonText := reason.String
		row.Reason = &reasonText
	}
	return []groupAccountStatsDirtyRow{row}, nil
}

func (s *Store) loadGroupAccountStatsDirtyRows(ctx context.Context, q queryer, limit int) ([]groupAccountStatsDirtyRow, error) {
	query := fmt.Sprintf(`
		SELECT group_id, reason, updated_at
		FROM %s
		WHERE group_id <> %s
		ORDER BY updated_at ASC, group_id ASC
		LIMIT %s
	`, s.businessTable("group_account_stats_dirty"), s.placeholder(1), s.placeholder(2))
	rows, err := q.QueryContext(ctx, query, groupAccountStatsDirtyAll, limit)
	if err != nil {
		return nil, fmt.Errorf("读取 group_account_stats_dirty 失败: %w", err)
	}
	defer rows.Close()
	result := make([]groupAccountStatsDirtyRow, 0, limit)
	for rows.Next() {
		var groupID string
		var reason, updatedAt sql.NullString
		if err := rows.Scan(&groupID, &reason, &updatedAt); err != nil {
			return nil, err
		}
		row := groupAccountStatsDirtyRow{GroupID: groupID}
		if reason.Valid {
			reasonText := reason.String
			row.Reason = &reasonText
		}
		row.UpdatedAt, err = sqlText(updatedAt)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Mirror the Node-side re-sort (localeCompare on updated_at then group id)
	// that follows the SQL ORDER BY.
	sort.SliceStable(result, func(left, right int) bool {
		updatedLeft, updatedRight := result[left].UpdatedAt, result[right].UpdatedAt
		if updatedLeft != updatedRight {
			return updatedLeft < updatedRight
		}
		return result[left].GroupID < result[right].GroupID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) deleteGroupAccountStatsDirtyRows(ctx context.Context, q execer, rows []groupAccountStatsDirtyRow) error {
	for _, row := range rows {
		if err := s.deleteGroupAccountStatsDirtyRow(ctx, q, row); err != nil {
			return err
		}
	}
	return nil
}

// deleteGroupAccountStatsDirtyRow CAS-deletes by (group_id, updated_at):
// when a concurrent write re-dirtied the group the updated_at no longer
// matches and the row survives.
func (s *Store) deleteGroupAccountStatsDirtyRow(ctx context.Context, q execer, row groupAccountStatsDirtyRow) error {
	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE group_id = %s AND updated_at = %s
	`, s.businessTable("group_account_stats_dirty"), s.placeholder(1), s.placeholder(2))
	if _, err := q.ExecContext(ctx, query, row.GroupID, row.UpdatedAt); err != nil {
		return fmt.Errorf("删除 group_account_stats_dirty 失败: %w", err)
	}
	return nil
}

func (s *Store) updateGroupAccountStatsAllCursor(ctx context.Context, q execer, cursorGroupID, updatedAt string) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET reason = %s, updated_at = %s
		WHERE group_id = %s
	`, s.businessTable("group_account_stats_dirty"),
		s.placeholder(1), s.placeholder(2), s.placeholder(3))
	if _, err := q.ExecContext(ctx, query, groupAccountStatsAllCursorPrefix+cursorGroupID, updatedAt, groupAccountStatsDirtyAll); err != nil {
		return fmt.Errorf("更新 group_account_stats cursor 失败: %w", err)
	}
	return nil
}

func (s *Store) loadGroupAccountStatsGroupsPage(ctx context.Context, q queryer, cursorGroupID string, limit int) ([]groupAccountStatsGroup, error) {
	cursorClause := ""
	args := []any{}
	if cursorGroupID != "" {
		cursorClause = fmt.Sprintf("WHERE id > %s", s.placeholder(1))
		args = append(args, cursorGroupID)
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		SELECT id, system_account_id
		FROM %s
		%s
		ORDER BY id ASC
		LIMIT %s
	`, s.businessTable("groups"), cursorClause, s.placeholder(len(args)))
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 groups 分页失败: %w", err)
	}
	defer rows.Close()
	result := make([]groupAccountStatsGroup, 0, limit)
	for rows.Next() {
		var group groupAccountStatsGroup
		if err := rows.Scan(&group.ID, &group.SystemAccountID); err != nil {
			return nil, err
		}
		result = append(result, group)
	}
	return result, rows.Err()
}

func (s *Store) hasGroupAccountStats(ctx context.Context, q queryer) (bool, error) {
	query := fmt.Sprintf(`SELECT 1 FROM %s LIMIT 1`, s.statsTable("group_account_stats"))
	var one int
	err := q.QueryRowContext(ctx, query).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查 group_account_stats 失败: %w", err)
	}
	return true, nil
}

// groupAccountStatsJoinRows loads the group/account/authorization join rows
// for the target groups (mirrors loadGroupAccountStatsRows). Authorization
// and cooldown comparisons stay textual RFC3339 comparisons, matching the
// Node SQLite accumulator.
func (s *Store) groupAccountStatsJoinRows(ctx context.Context, q queryer, groupIDs []string, updatedAt string) ([]groupAccountStatsJoinRow, error) {
	result := make([]groupAccountStatsJoinRow, 0)
	for _, chunk := range chunkStrings(groupIDs, 900) {
		placeholders := s.placeholders(len(chunk))
		offset := 1
		query := fmt.Sprintf(`
		SELECT
		  group_accounts.group_id,
		  group_accounts.account_id,
		  group_accounts.account_authorization_id,
		  groups.system_account_id AS group_system_account_id,
		  accounts.system_account_id AS account_system_account_id,
		  accounts.status,
		  accounts.schedulable,
		  accounts.cooldown_until,
		  accounts.concurrency_limit,
		  resource_authorization_rows.status AS authorization_status,
		  resource_authorization_rows.expires_at AS authorization_expires_at
		FROM %s group_accounts
		INNER JOIN %s groups ON groups.id = group_accounts.group_id
		LEFT JOIN %s accounts ON accounts.id = group_accounts.account_id
		LEFT JOIN %s resource_authorization_rows
		  ON resource_authorization_rows.id = group_accounts.account_authorization_id
		WHERE group_accounts.enabled = 1
		  AND accounts.deleted_at IS NULL
		  AND group_accounts.group_id IN (%s)
		`,
			s.businessTable("group_accounts"), s.businessTable("groups"),
			s.businessTable("accounts"), s.businessTable("resource_authorizations"),
			placeholders)
		args := make([]any, 0, len(chunk))
		for _, groupID := range chunk {
			args = append(args, groupID)
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("读取 group account 统计 join 行失败: %w", err)
		}
		scanErr := func() error {
			defer rows.Close()
			for rows.Next() {
				row, err := scanGroupAccountStatsJoinRow(rows)
				if err != nil {
					return err
				}
				result = append(result, row)
			}
			return rows.Err()
		}()
		if scanErr != nil {
			return nil, scanErr
		}
		_ = offset
	}
	return result, nil
}

type groupAccountStatsJoinRow struct {
	GroupID                string
	AccountID              *string
	AccountAuthorizationID *string
	GroupSystemAccountID   string
	AccountSystemAccountID *string
	Status                 *string
	Schedulable            *int64
	CooldownUntil          *string
	ConcurrencyLimit       *int64
	AuthorizationStatus    *string
	AuthorizationExpiresAt *string
}

func scanGroupAccountStatsJoinRow(rows *sql.Rows) (groupAccountStatsJoinRow, error) {
	var row groupAccountStatsJoinRow
	var accountID, accountAuthorizationID, accountSystemAccountID, status, cooldownUntil, authorizationStatus, authorizationExpiresAt any
	var schedulable, concurrencyLimit any
	if err := rows.Scan(&row.GroupID, &accountID, &accountAuthorizationID, &row.GroupSystemAccountID,
		&accountSystemAccountID, &status, &schedulable, &cooldownUntil, &concurrencyLimit,
		&authorizationStatus, &authorizationExpiresAt); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	var err error
	if row.AccountID, err = sqlStringPtr(accountID); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if row.AccountAuthorizationID, err = sqlStringPtr(accountAuthorizationID); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if row.AccountSystemAccountID, err = sqlStringPtr(accountSystemAccountID); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if row.Status, err = sqlStringPtr(status); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if schedulable != nil {
		schedulableInt, err := sqlInt(schedulable)
		if err != nil {
			return groupAccountStatsJoinRow{}, err
		}
		row.Schedulable = &schedulableInt
	}
	if row.CooldownUntil, err = sqlTextPtr(cooldownUntil); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if concurrencyLimit != nil {
		concurrencyInt, err := sqlInt(concurrencyLimit)
		if err != nil {
			return groupAccountStatsJoinRow{}, err
		}
		row.ConcurrencyLimit = &concurrencyInt
	}
	if row.AuthorizationStatus, err = sqlTextPtr(authorizationStatus); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	if row.AuthorizationExpiresAt, err = sqlTextPtr(authorizationExpiresAt); err != nil {
		return groupAccountStatsJoinRow{}, err
	}
	return row, nil
}

func sqlTextPtr(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, err := sqlText(value)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

// refreshGroupAccountStatsCache mirrors refreshGroupAccountStatsCache: the
// target groups are seeded with zero rows (so empty groups still appear),
// authorized accounts are accumulated, then the cache rows are replaced.
// businessQ reads groups/group_accounts/accounts/authorizations; statsQ
// rewrites group_account_stats (same transaction on PostgreSQL, two handles
// on SQLite).
func (s *Store) refreshGroupAccountStatsCache(ctx context.Context, businessQ queryer, statsQ execer, groupIDs []string, updatedAt string, now time.Time) error {
	targets := uniqueStrings(groupIDs)
	if len(targets) == 0 {
		return nil
	}
	statsByGroup := make(map[string]*groupAccountStatsAccumulator)
	order := make([]string, 0)
	groups, err := s.loadGroupAccountStatsGroupsPageAll(ctx, businessQ, targets)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if _, ok := statsByGroup[group.ID]; !ok {
			statsByGroup[group.ID] = &groupAccountStatsAccumulator{GroupID: group.ID, SystemAccountID: group.SystemAccountID}
			order = append(order, group.ID)
		}
	}
	joinRows, err := s.groupAccountStatsJoinRows(ctx, businessQ, targets, updatedAt)
	if err != nil {
		return err
	}
	for _, row := range joinRows {
		stats, ok := statsByGroup[row.GroupID]
		if !ok {
			stats = &groupAccountStatsAccumulator{GroupID: row.GroupID}
			statsByGroup[row.GroupID] = stats
			order = append(order, row.GroupID)
		}
		if row.AccountID == nil || *row.AccountID == "" || row.AccountSystemAccountID == nil || *row.AccountSystemAccountID == "" {
			continue
		}
		authorizationActive := false
		if row.AuthorizationStatus != nil {
			active := *row.AuthorizationStatus == "active"
			notExpired := row.AuthorizationExpiresAt == nil || *row.AuthorizationExpiresAt == "" || *row.AuthorizationExpiresAt > updatedAt
			authorizationActive = active && notExpired
		}
		authorized := false
		if row.AccountAuthorizationID != nil && *row.AccountAuthorizationID != "" {
			authorized = authorizationActive
		} else {
			authorized = row.AccountSystemAccountID != nil && *row.AccountSystemAccountID == row.GroupSystemAccountID
		}
		if !authorized {
			continue
		}
		stats.Total++
		if row.ConcurrencyLimit != nil {
			stats.ConcurrencyLimit += *row.ConcurrencyLimit
		}
		status := ""
		if row.Status != nil {
			status = *row.Status
		}
		switch {
		case status == "active":
			stats.Active++
			schedulable := row.Schedulable != nil && *row.Schedulable == 1
			cooldownOk := row.CooldownUntil == nil || *row.CooldownUntil == "" || *row.CooldownUntil <= updatedAt
			if schedulable && cooldownOk {
				stats.Available++
			}
		case status == "disabled":
			stats.Disabled++
		default:
			stats.Error++
		}
		if status == "rate_limited" {
			stats.RateLimited++
		}
	}

	for _, chunk := range chunkStrings(targets, 900) {
		placeholders := s.placeholders(len(chunk))
		if _, err := statsQ.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE group_id IN (%s)`, s.statsTable("group_account_stats"), placeholders), stringSliceToAny(chunk)...); err != nil {
			return fmt.Errorf("清理 group_account_stats 失败: %w", err)
		}
	}
	for _, groupID := range order {
		stats := statsByGroup[groupID]
		insert := fmt.Sprintf(`
			INSERT INTO %s (
			  system_account_id, group_id, total, available, active, disabled, error,
			  rate_limited, current_concurrency, concurrency_limit, updated_at
			) VALUES (%s, 0, %s)
		`, s.statsTable("group_account_stats"), s.placeholdersFrom(1, 8), s.placeholdersFrom(9, 2))
		if _, err := statsQ.ExecContext(ctx, insert,
			stats.SystemAccountID, stats.GroupID, stats.Total, stats.Available, stats.Active,
			stats.Disabled, stats.Error, stats.RateLimited, stats.ConcurrencyLimit, updatedAt); err != nil {
			return fmt.Errorf("写入 group_account_stats 失败: %w", err)
		}
	}
	return nil
}

// loadGroupAccountStatsGroupsPageAll resolves target groups by id chunks.
// On SQLite it reads from the business database (groups live there), which
// q cannot express when q is the stats handle; callers on SQLite therefore
// pass the business handle via refreshGroupAccountStatsCacheWithHandles.
func (s *Store) loadGroupAccountStatsGroupsPageAll(ctx context.Context, q queryer, groupIDs []string) ([]groupAccountStatsGroup, error) {
	result := make([]groupAccountStatsGroup, 0, len(groupIDs))
	for _, chunk := range chunkStrings(groupIDs, 900) {
		query := fmt.Sprintf(`
			SELECT id, system_account_id
			FROM %s
			WHERE id IN (%s)
		`, s.businessTable("groups"), s.placeholders(len(chunk)))
		args := make([]any, 0, len(chunk))
		for _, groupID := range chunk {
			args = append(args, groupID)
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("读取 groups 失败: %w", err)
		}
		for rows.Next() {
			var group groupAccountStatsGroup
			if err := rows.Scan(&group.ID, &group.SystemAccountID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result = append(result, group)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return result, nil
}

func groupAccountStatsAllCursor(reason *string) string {
	if reason == nil {
		return ""
	}
	if strings.HasPrefix(*reason, groupAccountStatsAllCursorPrefix) {
		return (*reason)[len(groupAccountStatsAllCursorPrefix):]
	}
	return ""
}

func groupIDs(rows []groupAccountStatsDirtyRow) []string {
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.GroupID)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func stringSliceToAny(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

type queryerExecer interface {
	queryer
	execer
}
