package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const statsSchemaContractVersion = 3

const statsSchemaColumnsQuery = `
SELECT COALESCE(jsonb_object_agg(column_name, data_type), '{}'::jsonb)::text
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2`

const statsSchemaUniqueKeysQuery = `
SELECT COALESCE(jsonb_agg(key_columns), '[]'::jsonb)::text
FROM (
  SELECT jsonb_agg(kcu.column_name ORDER BY kcu.ordinal_position) AS key_columns
  FROM information_schema.table_constraints AS tc
  JOIN information_schema.key_column_usage AS kcu
    ON kcu.constraint_schema = tc.constraint_schema
   AND kcu.constraint_name = tc.constraint_name
   AND kcu.table_schema = tc.table_schema
   AND kcu.table_name = tc.table_name
  WHERE tc.table_schema = $1
    AND tc.table_name = $2
    AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE')
  GROUP BY tc.constraint_name
) AS keys`

// StatsSchemaFeatureContract describes only tables read by Go opt-in endpoints.
// Node remains the sole writer until the worker migration has its own cutover evidence.
type StatsSchemaFeatureContract struct {
	Name      string
	Relations []StatsSchemaRelationContract
}

type StatsSchemaRelationContract struct {
	Name        string
	Columns     []string
	ColumnTypes map[string]string
	UniqueKey   []string
}

type StatsSchemaContractPreflightResult struct {
	Success         bool                              `json:"success"`
	ContractVersion int                               `json:"contractVersion"`
	Schema          string                            `json:"schema"`
	WriterOwner     string                            `json:"writerOwner"`
	Features        []StatsSchemaFeaturePreflightInfo `json:"features"`
	Issues          []string                          `json:"issues,omitempty"`
}

type StatsSchemaFeaturePreflightInfo struct {
	Name      string   `json:"name"`
	Ready     bool     `json:"ready"`
	Relations []string `json:"relations"`
}

type statsSchemaContractScanner interface {
	Scan(...any) error
}

type statsSchemaContractQuerier interface {
	QueryRow(context.Context, string, ...any) statsSchemaContractScanner
}

type statsSchemaContractPool struct {
	pool *pgxpool.Pool
}

func (q statsSchemaContractPool) QueryRow(ctx context.Context, query string, args ...any) statsSchemaContractScanner {
	return q.pool.QueryRow(ctx, query, args...)
}

func RunStatsSchemaContractPreflight(ctx context.Context, rawPostgresURL string, out io.Writer) error {
	contracts := statsSchemaReadContracts()
	config, err := pgxpool.ParseConfig(rawPostgresURL)
	if err != nil {
		return writeStatsSchemaContractFailure(out, contracts, "open target postgres: unavailable")
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return writeStatsSchemaContractFailure(out, contracts, "open target postgres: unavailable")
	}
	defer pool.Close()
	return runStatsSchemaContractPreflight(ctx, statsSchemaContractPool{pool: pool}, contracts, out)
}

func writeStatsSchemaContractFailure(out io.Writer, contracts []StatsSchemaFeatureContract, issue string) error {
	result := StatsSchemaContractPreflightResult{
		Success:         false,
		ContractVersion: statsSchemaContractVersion,
		Schema:          "juhe_stats",
		WriterOwner:     "node",
		Features:        make([]StatsSchemaFeaturePreflightInfo, 0, len(contracts)),
		Issues:          []string{issue},
	}
	for _, feature := range contracts {
		info := StatsSchemaFeaturePreflightInfo{Name: feature.Name, Ready: false, Relations: make([]string, 0, len(feature.Relations))}
		for _, relation := range feature.Relations {
			info.Relations = append(info.Relations, relation.Name)
		}
		result.Features = append(result.Features, info)
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	return fmt.Errorf("stats schema contract preflight 未通过")
}

func runStatsSchemaContractPreflight(
	ctx context.Context,
	querier statsSchemaContractQuerier,
	contracts []StatsSchemaFeatureContract,
	out io.Writer,
) error {
	result, err := inspectStatsSchemaContract(ctx, querier, contracts)
	if err != nil {
		return err
	}
	if encodeErr := json.NewEncoder(out).Encode(result); encodeErr != nil {
		return encodeErr
	}
	if !result.Success {
		return fmt.Errorf("stats schema contract preflight 未通过")
	}
	return nil
}

func inspectStatsSchemaContract(
	ctx context.Context,
	querier statsSchemaContractQuerier,
	contracts []StatsSchemaFeatureContract,
) (StatsSchemaContractPreflightResult, error) {
	result := StatsSchemaContractPreflightResult{
		Success:         true,
		ContractVersion: statsSchemaContractVersion,
		Schema:          "juhe_stats",
		WriterOwner:     "node",
		Features:        make([]StatsSchemaFeaturePreflightInfo, 0, len(contracts)),
	}
	if err := ctx.Err(); err != nil {
		return StatsSchemaContractPreflightResult{}, err
	}
	if querier == nil {
		return StatsSchemaContractPreflightResult{}, fmt.Errorf("stats schema contract querier is required")
	}

	for _, feature := range contracts {
		info := StatsSchemaFeaturePreflightInfo{Name: feature.Name, Ready: true, Relations: make([]string, 0, len(feature.Relations))}
		for _, relation := range feature.Relations {
			info.Relations = append(info.Relations, relation.Name)
			columns, unavailable, err := inspectStatsSchemaRelation(ctx, querier, relation.Name)
			if err != nil {
				return StatsSchemaContractPreflightResult{}, err
			}
			if unavailable {
				result.Success = false
				info.Ready = false
				result.Issues = append(result.Issues, fmt.Sprintf("inspect juhe_stats.%s: unavailable", relation.Name))
				continue
			}
			if len(columns) == 0 {
				result.Success = false
				info.Ready = false
				result.Issues = append(result.Issues, fmt.Sprintf("juhe_stats.%s is missing", relation.Name))
				continue
			}
			missing := missingStatsSchemaColumns(columns, relation.Columns)
			if len(missing) > 0 {
				result.Success = false
				info.Ready = false
				result.Issues = append(result.Issues, fmt.Sprintf("juhe_stats.%s missing columns: %s", relation.Name, joinComma(missing)))
			}
			mismatched := mismatchedStatsSchemaColumnTypes(columns, relation.ColumnTypes)
			if len(mismatched) > 0 {
				result.Success = false
				info.Ready = false
				result.Issues = append(result.Issues, fmt.Sprintf("juhe_stats.%s column type mismatch: %s", relation.Name, joinComma(mismatched)))
			}
			if len(relation.UniqueKey) > 0 {
				keys, unavailable, err := inspectStatsSchemaUniqueKeys(ctx, querier, relation.Name)
				if err != nil {
					return StatsSchemaContractPreflightResult{}, err
				}
				if unavailable {
					result.Success = false
					info.Ready = false
					result.Issues = append(result.Issues, fmt.Sprintf("inspect juhe_stats.%s unique keys: unavailable", relation.Name))
				} else if !containsStatsSchemaKey(keys, relation.UniqueKey) {
					result.Success = false
					info.Ready = false
					result.Issues = append(result.Issues, fmt.Sprintf("juhe_stats.%s missing unique key: %s", relation.Name, joinComma(relation.UniqueKey)))
				}
			}
		}
		result.Features = append(result.Features, info)
	}
	if err := ctx.Err(); err != nil {
		return StatsSchemaContractPreflightResult{}, err
	}
	return result, nil
}

func inspectStatsSchemaUniqueKeys(ctx context.Context, querier statsSchemaContractQuerier, table string) ([][]string, bool, error) {
	var raw string
	err := querier.QueryRow(ctx, statsSchemaUniqueKeysQuery, "juhe_stats", table).Scan(&raw)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		return nil, true, nil
	}
	keys := [][]string{}
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, false, fmt.Errorf("decode juhe_stats.%s unique keys: %w", table, err)
	}
	return keys, false, nil
}

func inspectStatsSchemaRelation(ctx context.Context, querier statsSchemaContractQuerier, table string) (map[string]string, bool, error) {
	var raw string
	err := querier.QueryRow(ctx, statsSchemaColumnsQuery, "juhe_stats", table).Scan(&raw)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		return nil, true, nil
	}
	columns := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &columns); err != nil {
		return nil, false, fmt.Errorf("decode juhe_stats.%s columns: %w", table, err)
	}
	return columns, false, nil
}

func missingStatsSchemaColumns(actual map[string]string, required []string) []string {
	missing := make([]string, 0)
	for _, column := range required {
		if _, ok := actual[column]; !ok {
			missing = append(missing, column)
		}
	}
	sort.Strings(missing)
	return missing
}

func mismatchedStatsSchemaColumnTypes(actual map[string]string, required map[string]string) []string {
	mismatched := make([]string, 0)
	for column, want := range required {
		if got, ok := actual[column]; ok && got != want {
			mismatched = append(mismatched, fmt.Sprintf("%s=%s want %s", column, got, want))
		}
	}
	sort.Strings(mismatched)
	return mismatched
}

func containsStatsSchemaKey(actual [][]string, required []string) bool {
	for _, key := range actual {
		if len(key) != len(required) {
			continue
		}
		matched := true
		for index := range required {
			if key[index] != required[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func joinComma(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ", "
		}
		result += value
	}
	return result
}

func statsSchemaReadContracts() []StatsSchemaFeatureContract {
	return []StatsSchemaFeatureContract{
		{
			Name: "account-usage-ai-performance",
			Relations: []StatsSchemaRelationContract{
				{Name: "usage_scope_range_windows", Columns: []string{
					"system_account_id", "scope_type", "scope_id", "window_key", "request_count", "input_tokens", "output_tokens",
					"cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens", "cache_write_1h_tokens", "cache_write_cost_usd",
					"thinking_tokens", "input_image_tokens", "output_image_tokens", "total_cost_usd", "last_used_at",
				}},
				{Name: "usage_rank_snapshots", Columns: []string{
					"system_account_id", "scope_type", "scope_id", "window_key", "metric", "snapshot_at", "rank", "metric_value",
				}},
				{Name: "usage_stats_daily", Columns: []string{
					"system_account_id", "scope_type", "scope_id", "stat_date", "request_count", "input_tokens", "output_tokens",
					"cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens", "cache_write_1h_tokens", "cache_write_cost_usd",
					"thinking_tokens", "input_image_tokens", "output_image_tokens", "total_cost_usd", "last_used_at",
				}},
				{Name: "usage_stats_hourly", Columns: []string{
					"system_account_id", "scope_type", "scope_id", "stat_hour", "request_count", "first_token_ms_sum",
					"first_token_ms_count", "first_token_ms_max", "duration_ms_sum", "duration_ms_count", "duration_ms_max",
				}},
				{Name: "ai_performance_summary_windows", Columns: []string{
					"system_account_id", "window_key", "start_date", "end_date", "request_count", "first_token_ms_sum",
					"first_token_ms_count", "first_token_ms_max", "duration_ms_sum", "duration_ms_count", "duration_ms_max",
				}},
			},
		},
		{
			Name: "stats-overview",
			Relations: []StatsSchemaRelationContract{
				{Name: "usage_overview_summary_windows", Columns: []string{
					"system_account_id", "window_key", "start_date", "end_date", "request_count", "success_count", "error_count",
					"input_tokens", "output_tokens", "cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens",
					"cache_write_1h_tokens", "cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens",
					"total_cost_usd", "duration_ms_sum", "duration_ms_count", "first_token_ms_sum", "first_token_ms_count", "last_used_at",
				}, ColumnTypes: map[string]string{
					"system_account_id": "text", "window_key": "text", "start_date": "text", "end_date": "text",
					"request_count": "bigint", "success_count": "bigint", "error_count": "bigint",
					"input_tokens": "bigint", "output_tokens": "bigint", "cache_read_tokens": "bigint",
					"cache_read_cost_usd": "double precision", "cache_write_tokens": "bigint", "cache_write_1h_tokens": "bigint",
					"cache_write_cost_usd": "double precision", "thinking_tokens": "bigint", "input_image_tokens": "bigint",
					"output_image_tokens": "bigint", "total_cost_usd": "double precision", "duration_ms_sum": "bigint",
					"duration_ms_count": "bigint", "first_token_ms_sum": "bigint", "first_token_ms_count": "bigint", "last_used_at": "text",
				}, UniqueKey: []string{"system_account_id", "window_key"}},
				{Name: "usage_stats_daily", Columns: []string{
					"system_account_id", "scope_type", "scope_id", "stat_date", "request_count", "success_count", "error_count",
					"input_tokens", "output_tokens", "cache_read_tokens", "cache_read_cost_usd", "cache_write_tokens",
					"cache_write_1h_tokens", "cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens",
					"total_cost_usd", "duration_ms_sum", "duration_ms_count", "first_token_ms_sum", "first_token_ms_count", "last_used_at",
				}, ColumnTypes: map[string]string{
					"system_account_id": "text", "scope_type": "text", "scope_id": "text", "stat_date": "text",
					"input_tokens": "bigint", "output_tokens": "bigint", "total_cost_usd": "double precision",
				}, UniqueKey: []string{"system_account_id", "scope_type", "scope_id", "stat_date"}},
				{Name: "usage_overview_trend_windows", Columns: []string{
					"system_account_id", "window_key", "start_date", "end_date", "bucket_key", "request_count", "error_count",
					"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write_1h_tokens",
					"cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens", "total_cost_usd",
					"duration_ms_sum", "duration_ms_count",
				}, ColumnTypes: map[string]string{
					"system_account_id": "text", "window_key": "text", "start_date": "text", "end_date": "text", "bucket_key": "text",
					"request_count": "bigint", "error_count": "bigint", "input_tokens": "bigint", "output_tokens": "bigint",
					"cache_read_tokens": "bigint", "cache_write_tokens": "bigint", "cache_write_1h_tokens": "bigint",
					"cache_write_cost_usd": "double precision", "thinking_tokens": "bigint", "input_image_tokens": "bigint",
					"output_image_tokens": "bigint", "total_cost_usd": "double precision", "duration_ms_sum": "bigint", "duration_ms_count": "bigint",
				}, UniqueKey: []string{"system_account_id", "window_key", "bucket_key"}},
				{Name: "usage_model_rank_windows", Columns: []string{
					"system_account_id", "window_key", "start_date", "end_date", "rank", "provider_code", "model", "request_count",
					"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write_1h_tokens",
					"cache_write_cost_usd", "thinking_tokens", "input_image_tokens", "output_image_tokens", "total_cost_usd",
				}, ColumnTypes: map[string]string{
					"system_account_id": "text", "window_key": "text", "start_date": "text", "end_date": "text", "rank": "integer",
					"provider_code": "text", "model": "text", "request_count": "bigint", "input_tokens": "bigint", "output_tokens": "bigint",
					"cache_read_tokens": "bigint", "cache_write_tokens": "bigint", "cache_write_1h_tokens": "bigint",
					"cache_write_cost_usd": "double precision", "thinking_tokens": "bigint", "input_image_tokens": "bigint",
					"output_image_tokens": "bigint", "total_cost_usd": "double precision",
				}, UniqueKey: []string{"system_account_id", "window_key", "rank", "provider_code", "model"}},
				{Name: "usage_error_rank_windows", Columns: []string{
					"system_account_id", "window_key", "start_date", "end_date", "rank", "provider_code", "error_code", "status_code", "error_message", "error_count",
				}, ColumnTypes: map[string]string{
					"system_account_id": "text", "window_key": "text", "start_date": "text", "end_date": "text", "rank": "integer",
					"provider_code": "text", "error_code": "text", "status_code": "integer", "error_message": "text", "error_count": "bigint",
				}, UniqueKey: []string{"system_account_id", "window_key", "rank", "provider_code", "error_code", "status_code"}},
			},
		},
		{
			Name: "system-metrics",
			Relations: []StatsSchemaRelationContract{
				{Name: "system_metrics_trend_windows", Columns: []string{
					"window_key", "start_date", "end_date", "bucket_key", "sample_count", "cpu_percent_sum", "cpu_percent_max",
					"memory_used_percent_sum", "memory_used_percent_max", "process_rss_bytes_max", "process_heap_used_bytes_max",
					"event_loop_lag_ms_sum", "event_loop_lag_ms_count", "event_loop_lag_ms_max", "network_rx_bytes_per_sec_sum",
					"network_rx_bytes_per_sec_max", "network_rx_bytes_per_sec_count", "network_tx_bytes_per_sec_sum",
					"network_tx_bytes_per_sec_max", "network_tx_bytes_per_sec_count", "network_rx_total_bytes_max",
					"network_tx_total_bytes_max", "db_file_bytes_max", "stats_lag_seconds_max",
				}},
				{Name: "process_event_loop_trend_windows", Columns: []string{
					"window_key", "start_date", "end_date", "bucket_key", "process_role", "sample_count", "event_loop_lag_ms_sum",
					"event_loop_lag_ms_count", "event_loop_lag_ms_max", "process_rss_bytes_sum", "process_rss_bytes_max",
					"process_heap_used_bytes_sum", "process_heap_used_bytes_max", "process_heap_total_bytes_sum", "process_heap_total_bytes_max",
				}},
				{Name: "process_event_loop_samples", Columns: []string{
					"id", "process_role", "process_pid", "sampled_at", "event_loop_lag_ms", "process_rss_bytes",
					"process_heap_used_bytes", "process_heap_total_bytes", "process_external_bytes", "process_array_buffers_bytes",
				}},
			},
		},
		{
			Name: "table-monitor",
			Relations: []StatsSchemaRelationContract{
				{Name: "database_storage_snapshots", Columns: []string{
					"id", "database_role", "database_path", "sampled_at", "file_bytes", "wal_bytes", "shm_bytes", "page_size",
					"page_count", "freelist_count", "used_bytes", "free_bytes", "table_count", "index_count",
				}},
				{Name: "table_storage_snapshots", Columns: []string{
					"id", "database_role", "table_name", "sampled_at", "table_kind", "parent_table_name", "is_partition", "is_archive",
					"row_count", "table_bytes", "index_bytes", "total_bytes", "page_count", "index_count", "growth_bytes_1h",
					"growth_rows_1h", "growth_bytes_24h", "growth_rows_24h",
				}},
			},
		},
	}
}
