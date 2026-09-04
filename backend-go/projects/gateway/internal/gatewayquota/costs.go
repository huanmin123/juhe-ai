package gatewayquota

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// RequestQuotaCosts mirrors RequestQuotaCosts (storage/request-quota-checker.ts).
type RequestQuotaCosts struct {
	Hourly  float64 `json:"hourly"`
	Daily   float64 `json:"daily"`
	Weekly  float64 `json:"weekly"`
	Monthly float64 `json:"monthly"`
	Total   float64 `json:"total"`
}

// EmptyRequestQuotaCosts mirrors emptyRequestQuotaCosts (hourly: 0, ...).
func EmptyRequestQuotaCosts() RequestQuotaCosts { return RequestQuotaCosts{} }

// CloneRequestQuotaCosts mirrors cloneRequestQuotaCosts.
func CloneRequestQuotaCosts(costs RequestQuotaCosts) RequestQuotaCosts {
	return RequestQuotaCosts{
		Hourly:  costs.Hourly,
		Daily:   costs.Daily,
		Weekly:  costs.Weekly,
		Monthly: costs.Monthly,
		Total:   costs.Total,
	}
}

// CostInput mirrors RequestQuotaCostInput. HasHourlyWindow mirrors
// hourlyWindowHours !== undefined.
type CostInput struct {
	SystemAccountID   string
	ScopeType         string
	ScopeID           string
	Now               time.Time
	HourlyWindowHours int
	HasHourlyWindow   bool
}

// Scope types used by the quota subsystem.
const (
	ScopeTypeAPIKey                   = "api_key"
	ScopeTypeAccountAuthorization     = "account_authorization"
	ScopeTypeGroupAuthorization       = "group_authorization"
	ScopeTypeAccountAuthorizationTeam = "account_authorization_team"
	ScopeTypeGroupAuthorizationTeam   = "group_authorization_team"
	ScopeTypeAuthorizationRuntime     = "authorization_runtime"
)

// NormalizeHourlyWindowHours mirrors normalizeHourlyWindowHours /
// Math.max(1, Math.trunc(value)).
func NormalizeHourlyWindowHours(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

// CostKey mirrors requestQuotaCostKey: the \x0000-joined
// (systemAccountId, scopeType, scopeId, statDate, statWeek, statMonth,
// hourlyWindowHours?) lookup key.
func CostKey(input CostInput, location *time.Location) string {
	hourlyWindow := ""
	if input.HasHourlyWindow {
		hourlyWindow = strconv.Itoa(NormalizeHourlyWindowHours(input.HourlyWindowHours))
	}
	return strings.Join([]string{
		input.SystemAccountID,
		input.ScopeType,
		input.ScopeID,
		dateKey(input.Now, location),
		weekKey(input.Now, location),
		monthKey(input.Now, location),
		hourlyWindow,
	}, "\x00")
}

// IsRequestQuotaExceeded mirrors isRequestQuotaExceeded — note the inclusive
// >= comparison: reaching the limit exactly already denies the request.
func IsRequestQuotaExceeded(limits RequestQuotaLimits, costs RequestQuotaCosts) bool {
	return (limits.Hourly != nil && limits.Hourly.Enabled && costs.Hourly >= limits.Hourly.Limit) ||
		(limits.Daily != nil && limits.Daily.Enabled && costs.Daily >= limits.Daily.Limit) ||
		(limits.Weekly != nil && limits.Weekly.Enabled && costs.Weekly >= limits.Weekly.Limit) ||
		(limits.Monthly != nil && limits.Monthly.Enabled && costs.Monthly >= limits.Monthly.Limit) ||
		(limits.Total != nil && limits.Total.Enabled && costs.Total >= limits.Total.Limit)
}

// StatsStore is the dual-mode (SQLite / PostgreSQL) reader over the juhe_stats
// usage projections. SQLite tables are unqualified; PostgreSQL qualifies with
// the juhe_stats schema (mirroring statsTableName).
type StatsStore struct {
	db *sql.DB
	pg bool
}

// NewStatsStore builds the stats reader; db must not be nil.
func NewStatsStore(db *sql.DB, postgres bool) (*StatsStore, error) {
	if db == nil {
		return nil, errors.New("gatewayquota stats store requires a database")
	}
	return &StatsStore{db: db, pg: postgres}, nil
}

func statsTable(pg bool, name string) string {
	if pg {
		return "juhe_stats." + name
	}
	return name
}

func statsBusinessTable(pg bool, name string) string {
	if pg {
		return "juhe_business." + name
	}
	return name
}

// bindPlaceholders converts ? markers to $n for PostgreSQL (Node driver
// dialects), leaving SQLite queries untouched.
func bindPlaceholders(pg bool, query string) string {
	if !pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// LoadCosts mirrors loadRequestQuotaCosts: five single-scope reads (hourly
// only when the window is configured).
func (s *StatsStore) LoadCosts(ctx context.Context, input CostInput, location *time.Location) (RequestQuotaCosts, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	costs := EmptyRequestQuotaCosts()
	var totalCost sql.NullFloat64
	err := s.db.QueryRowContext(ctx, bindPlaceholders(s.pg, `
		SELECT COALESCE(total_cost_usd, 0) AS total_cost
		FROM `+statsTable(s.pg, "usage_stats_totals")+`
		WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?`),
		input.SystemAccountID, input.ScopeType, input.ScopeID).Scan(&totalCost)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RequestQuotaCosts{}, err
	}
	if totalCost.Valid {
		costs.Total = totalCost.Float64
	}
	if input.HasHourlyWindow {
		var hourlyCost sql.NullFloat64
		err := s.db.QueryRowContext(ctx, bindPlaceholders(s.pg, `
			SELECT COALESCE(total_cost_usd, 0) AS total_cost
			FROM `+statsTable(s.pg, "usage_quota_hourly_windows")+`
			WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND window_hours = ?`),
			input.SystemAccountID, input.ScopeType, input.ScopeID, NormalizeHourlyWindowHours(input.HourlyWindowHours)).Scan(&hourlyCost)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return RequestQuotaCosts{}, err
		}
		if hourlyCost.Valid {
			costs.Hourly = hourlyCost.Float64
		}
	}
	var dailyCost sql.NullFloat64
	err = s.db.QueryRowContext(ctx, bindPlaceholders(s.pg, `
		SELECT COALESCE(total_cost_usd, 0) AS total_cost
		FROM `+statsTable(s.pg, "usage_stats_daily")+`
		WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?`),
		input.SystemAccountID, input.ScopeType, input.ScopeID, dateKey(input.Now, location)).Scan(&dailyCost)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RequestQuotaCosts{}, err
	}
	if dailyCost.Valid {
		costs.Daily = dailyCost.Float64
	}
	var weeklyCost sql.NullFloat64
	err = s.db.QueryRowContext(ctx, bindPlaceholders(s.pg, `
		SELECT COALESCE(total_cost_usd, 0) AS total_cost
		FROM `+statsTable(s.pg, "usage_stats_weekly")+`
		WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_week = ?`),
		input.SystemAccountID, input.ScopeType, input.ScopeID, weekKey(input.Now, location)).Scan(&weeklyCost)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RequestQuotaCosts{}, err
	}
	if weeklyCost.Valid {
		costs.Weekly = weeklyCost.Float64
	}
	var monthlyCost sql.NullFloat64
	err = s.db.QueryRowContext(ctx, bindPlaceholders(s.pg, `
		SELECT COALESCE(total_cost_usd, 0) AS total_cost
		FROM `+statsTable(s.pg, "usage_stats_monthly")+`
		WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_month = ?`),
		input.SystemAccountID, input.ScopeType, input.ScopeID, monthKey(input.Now, location)).Scan(&monthlyCost)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RequestQuotaCosts{}, err
	}
	if monthlyCost.Valid {
		costs.Monthly = monthlyCost.Float64
	}
	return costs, nil
}

// LoadCostsBatch mirrors loadRequestQuotaCostsBatch/loadRequestQuotaCostsBatchAsync:
// dedupe inputs by cost key, batch-load the five projection tables with OR'd
// tuple predicates (chunk size floor(800 / column count)) and fan the rows
// back out per request key.
func (s *StatsStore) LoadCostsBatch(ctx context.Context, inputs []CostInput, location *time.Location) (map[string]RequestQuotaCosts, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type costLookup struct {
		key               string
		systemAccountID   string
		scopeType         string
		scopeID           string
		statDate          string
		statWeek          string
		statMonth         string
		hourlyWindowHours int
		hasHourlyWindow   bool
	}
	requests := make([]costLookup, 0, len(inputs))
	seenKeys := map[string]struct{}{}
	for _, input := range inputs {
		hourlyWindowHours := 0
		hasHourly := input.HasHourlyWindow
		if hasHourly {
			hourlyWindowHours = NormalizeHourlyWindowHours(input.HourlyWindowHours)
		}
		statDate := dateKey(input.Now, location)
		statWeek := weekKey(input.Now, location)
		statMonth := monthKey(input.Now, location)
		hourlyPart := ""
		if hasHourly {
			hourlyPart = strconv.Itoa(hourlyWindowHours)
		}
		key := strings.Join([]string{input.SystemAccountID, input.ScopeType, input.ScopeID, statDate, statWeek, statMonth, hourlyPart}, "\x00")
		if _, seen := seenKeys[key]; seen {
			continue
		}
		seenKeys[key] = struct{}{}
		requests = append(requests, costLookup{
			key:               key,
			systemAccountID:   input.SystemAccountID,
			scopeType:         input.ScopeType,
			scopeID:           input.ScopeID,
			statDate:          statDate,
			statWeek:          statWeek,
			statMonth:         statMonth,
			hourlyWindowHours: hourlyWindowHours,
			hasHourlyWindow:   hasHourly,
		})
	}
	output := make(map[string]RequestQuotaCosts, len(requests))
	for _, request := range requests {
		output[request.key] = EmptyRequestQuotaCosts()
	}
	if len(requests) == 0 {
		return output, nil
	}

	tuple := func(values ...string) string { return strings.Join(values, "\x00") }
	base := func(r costLookup) []string { return []string{r.systemAccountID, r.scopeType, r.scopeID} }

	type costTable struct {
		name    string
		columns []string
		extra   func(r costLookup) []string
		apply   func(costs *RequestQuotaCosts, value float64)
	}
	tables := []costTable{
		{
			name:    "usage_stats_totals",
			columns: []string{"system_account_id", "scope_type", "scope_id"},
			extra:   func(costLookup) []string { return nil },
			apply:   func(costs *RequestQuotaCosts, value float64) { costs.Total = value },
		},
		{
			name:    "usage_stats_daily",
			columns: []string{"system_account_id", "scope_type", "scope_id", "stat_date"},
			extra:   func(r costLookup) []string { return []string{r.statDate} },
			apply:   func(costs *RequestQuotaCosts, value float64) { costs.Daily = value },
		},
		{
			name:    "usage_stats_weekly",
			columns: []string{"system_account_id", "scope_type", "scope_id", "stat_week"},
			extra:   func(r costLookup) []string { return []string{r.statWeek} },
			apply:   func(costs *RequestQuotaCosts, value float64) { costs.Weekly = value },
		},
		{
			name:    "usage_stats_monthly",
			columns: []string{"system_account_id", "scope_type", "scope_id", "stat_month"},
			extra:   func(r costLookup) []string { return []string{r.statMonth} },
			apply:   func(costs *RequestQuotaCosts, value float64) { costs.Monthly = value },
		},
		{
			name:    "usage_quota_hourly_windows",
			columns: []string{"system_account_id", "scope_type", "scope_id", "window_hours"},
			extra: func(r costLookup) []string {
				if !r.hasHourlyWindow {
					return nil
				}
				return []string{strconv.Itoa(r.hourlyWindowHours)}
			},
			apply: func(costs *RequestQuotaCosts, value float64) { costs.Hourly = value },
		},
	}

	for _, tableDef := range tables {
		// requestKeysByTuple: tuple -> request keys sharing it.
		keysByTuple := map[string][]string{}
		var tupleOrder []string
		for _, request := range requests {
			values := append(base(request), tableDef.extra(request)...)
			if len(values) < len(tableDef.columns) {
				continue // mirrors the undefined-tuple filter (hourly off)
			}
			key := tuple(values...)
			if _, ok := keysByTuple[key]; !ok {
				tupleOrder = append(tupleOrder, key)
			}
			keysByTuple[key] = append(keysByTuple[key], request.key)
		}
		if len(tupleOrder) == 0 {
			continue
		}
		chunkSize := 800 / len(tableDef.columns)
		if chunkSize < 1 {
			chunkSize = 1
		}
		for start := 0; start < len(tupleOrder); start += chunkSize {
			end := start + chunkSize
			if end > len(tupleOrder) {
				end = len(tupleOrder)
			}
			chunk := tupleOrder[start:end]
			clauses := make([]string, 0, len(chunk))
			args := make([]any, 0, len(chunk)*len(tableDef.columns))
			for _, key := range chunk {
				values := strings.Split(key, "\x00")
				placeholders := make([]string, len(tableDef.columns))
				for i, column := range tableDef.columns {
					placeholders[i] = column + " = ?"
					args = append(args, values[i])
				}
				clauses = append(clauses, "("+strings.Join(placeholders, " AND ")+")")
			}
			// selectColumns mirrors the JS Set insert order: base columns,
			// extra key columns, then the aliased cost.
			selectColumns := []string{"system_account_id", "scope_type", "scope_id"}
			for _, column := range tableDef.columns[3:] {
				selectColumns = append(selectColumns, column)
			}
			query := "SELECT " + strings.Join(selectColumns, ", ") + ", COALESCE(total_cost_usd, 0) AS total_cost FROM " +
				statsTable(s.pg, tableDef.name) + " WHERE " + strings.Join(clauses, " OR ")
			rows, err := s.db.QueryContext(ctx, bindPlaceholders(s.pg, query), args...)
			if err != nil {
				return nil, err
			}
			scanTargets := func(row *costRowScan) []any {
				targets := make([]any, 0, len(selectColumns)+1)
				targets = append(targets, &row.systemAccountID, &row.scopeType, &row.scopeID)
				for range selectColumns[3:] {
					targets = append(targets, &row.extraValue)
				}
				targets = append(targets, &row.totalCost)
				return targets
			}
			for rows.Next() {
				row := costRowScan{}
				if err := rows.Scan(scanTargets(&row)...); err != nil {
					rows.Close()
					return nil, err
				}
				values := []string{row.systemAccountID, row.scopeType, row.scopeID}
				if len(selectColumns) > 3 {
					values = append(values, row.extraValue)
				}
				tupleKey := tuple(values...)
				for _, requestKey := range keysByTuple[tupleKey] {
					if costs, ok := output[requestKey]; ok {
						tableDef.apply(&costs, row.totalCost)
						output[requestKey] = costs
					}
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
		}
	}
	return output, nil
}

// costRowScan is the shared scan target for batch cost rows: the fixed base
// columns, one extra key column and the cost value.
type costRowScan struct {
	systemAccountID string
	scopeType       string
	scopeID         string
	extraValue      string
	totalCost       float64
}

// ensureCtx re-points a nil context at Background (mirrors other slices).
func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
