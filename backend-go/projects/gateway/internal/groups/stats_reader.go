package groups

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// StatsReader is the interface for reading group_account_stats from the stats database.
// Implementations must be safe for concurrent use.
type StatsReader interface {
	ReadGroupAccountStats(ctx context.Context, groupIDs []string) (map[string]AccountStats, error)
}

// GroupAccountStatsDBReader reads group_account_stats from juhe_stats database.
type GroupAccountStatsDBReader struct {
	db  *sql.DB
	pg  bool
	now func() time.Time
}

// NewGroupAccountStatsDBReader creates a stats reader from the stats database.
func NewGroupAccountStatsDBReader(db *sql.DB, postgres bool) *GroupAccountStatsDBReader {
	return &GroupAccountStatsDBReader{db: db, pg: postgres, now: timeNow}
}

// timeNow returns the current time for timestamp generation.
var timeNow = func() time.Time { return time.Now() }

// ReadGroupAccountStats reads group_account_stats for the given group IDs from juhe_stats.
// Returns a map keyed by group_id. Missing groups are not included in the result.
func (r *GroupAccountStatsDBReader) ReadGroupAccountStats(ctx context.Context, groupIDs []string) (map[string]AccountStats, error) {
	if len(groupIDs) == 0 || r.db == nil {
		return map[string]AccountStats{}, nil
	}

	result := make(map[string]AccountStats, len(groupIDs))
	placeholders := make([]string, len(groupIDs))
	args := make([]any, 0, len(groupIDs)+1)

	for i, id := range groupIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	// Add timestamp as last argument (for updated_at filter)
	args = append(args, timeNow().UTC().Format("2006-01-02 15:04:05"))

	query := "SELECT group_id, total, available, active, disabled, error, rate_limited, " +
		"current_concurrency, concurrency_limit, today_usage, usage " +
		"FROM group_account_stats WHERE group_id IN (" + strings.Join(placeholders, ",") + ") " +
		"AND updated_at > ?"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var row groupAccountStatsRow
		if err := rows.Scan(
			&row.GroupID,
			&row.Total,
			&row.Available,
			&row.Active,
			&row.Disabled,
			&row.Error,
			&row.RateLimited,
			&row.CurrentConcurrency,
			&row.ConcurrencyLimit,
			&row.TodayUsageJSON,
			&row.UsageJSON,
		); err != nil {
			return nil, err
		}
		result[row.GroupID] = accountStatsFromGroupRow(row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// groupAccountStatsRow mirrors the juhe_stats.group_account_stats projection.
type groupAccountStatsRow struct {
	GroupID            string
	Total              int
	Available          int
	Active             int
	Disabled           int
	Error              int
	RateLimited        int
	CurrentConcurrency int
	ConcurrencyLimit   int
	TodayUsageJSON     []byte
	UsageJSON          []byte
}

// accountStatsFromGroupRow mirrors the Node GroupAccountStats mapper:
// reads the stats row and converts it to the AccountStats projection.
func accountStatsFromGroupRow(row groupAccountStatsRow) AccountStats {
	return AccountStats{
		Total:              row.Total,
		Available:          row.Available,
		Active:             row.Active,
		Disabled:           row.Disabled,
		Error:              row.Error,
		RateLimited:        row.RateLimited,
		CurrentConcurrency: row.CurrentConcurrency,
		ConcurrencyLimit:   row.ConcurrencyLimit,
		TodayUsage:         parseJSONBytes(row.TodayUsageJSON),
		Usage:              parseJSONBytes(row.UsageJSON),
	}
}

// parseJSONBytes safely parses JSON bytes, returning nil on error or empty input.
func parseJSONBytes(data []byte) any {
	if len(data) == 0 {
		return nil
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}
