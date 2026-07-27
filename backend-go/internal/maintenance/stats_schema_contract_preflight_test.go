package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type statsSchemaContractRow struct {
	columnsJSON string
	err         error
}

func (r statsSchemaContractRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("scan destinations = %d, want 1", len(dest))
	}
	value, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("scan destination = %T, want *string", dest[0])
	}
	*value = r.columnsJSON
	return nil
}

type statsSchemaContractQuerierStub struct {
	columnsByTable map[string][]string
	columnTypes    map[string]map[string]string
	uniqueKeys     map[string][][]string
	errByTable     map[string]error
	queries        []string
	tables         []string
}

func (q *statsSchemaContractQuerierStub) QueryRow(_ context.Context, query string, args ...any) statsSchemaContractScanner {
	q.queries = append(q.queries, query)
	if len(args) != 2 {
		return statsSchemaContractRow{err: fmt.Errorf("query arguments = %d, want 2", len(args))}
	}
	schema, schemaOK := args[0].(string)
	table, tableOK := args[1].(string)
	if !schemaOK || schema != "juhe_stats" || !tableOK {
		return statsSchemaContractRow{err: fmt.Errorf("unexpected arguments: %v", args)}
	}
	q.tables = append(q.tables, table)
	if err := q.errByTable[table]; err != nil {
		return statsSchemaContractRow{err: err}
	}
	if strings.Contains(query, "information_schema.table_constraints") {
		raw, err := json.Marshal(q.uniqueKeys[table])
		return statsSchemaContractRow{columnsJSON: string(raw), err: err}
	}
	columns := map[string]string{}
	for _, column := range q.columnsByTable[table] {
		columnType := "text"
		if configured := q.columnTypes[table][column]; configured != "" {
			columnType = configured
		}
		columns[column] = columnType
	}
	raw, err := json.Marshal(columns)
	return statsSchemaContractRow{columnsJSON: string(raw), err: err}
}

func TestInspectStatsSchemaContractAcceptsCompleteNodeWriterSchema(t *testing.T) {
	contracts := statsSchemaReadContracts()
	querier := completeStatsSchemaContractQuerier(contracts)

	result, err := inspectStatsSchemaContract(context.Background(), querier, contracts)
	if err != nil {
		t.Fatalf("inspectStatsSchemaContract() error = %v", err)
	}
	if !result.Success || result.ContractVersion != 3 || result.Schema != "juhe_stats" || result.WriterOwner != "node" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Features) != len(contracts) || len(result.Issues) != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, feature := range result.Features {
		if !feature.Ready {
			t.Fatalf("feature = %+v, want ready", feature)
		}
	}
	for _, query := range querier.queries {
		upper := strings.ToUpper(query)
		if (!strings.Contains(query, "information_schema.columns") && !strings.Contains(query, "information_schema.table_constraints")) || !strings.HasPrefix(strings.TrimSpace(upper), "SELECT") {
			t.Fatalf("preflight query must be read-only information_schema inspection:\n%s", query)
		}
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "CREATE ", "ALTER ", "DROP ", "TRUNCATE "} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("preflight query contains forbidden %q:\n%s", forbidden, query)
			}
		}
	}
}

func TestInspectStatsSchemaContractRejectsMissingRelationAndColumn(t *testing.T) {
	contracts := statsSchemaReadContracts()
	columns := contractColumns(contracts)
	delete(columns, "database_storage_snapshots")
	delete(columns, "usage_stats_hourly")
	delete(columns, "usage_overview_summary_windows")
	columns["system_metrics_trend_windows"] = withoutColumn(columns["system_metrics_trend_windows"], "stats_lag_seconds_max")
	querier := completeStatsSchemaContractQuerier(contracts)
	querier.columnsByTable = columns

	result, err := inspectStatsSchemaContract(context.Background(), querier, contracts)
	if err != nil {
		t.Fatalf("inspectStatsSchemaContract() error = %v", err)
	}
	if result.Success {
		t.Fatalf("result = %+v, want failed contract", result)
	}
	joined := strings.Join(result.Issues, "\n")
	for _, want := range []string{
		"juhe_stats.database_storage_snapshots is missing",
		"juhe_stats.usage_stats_hourly is missing",
		"juhe_stats.usage_overview_summary_windows is missing",
		"juhe_stats.system_metrics_trend_windows missing columns: stats_lag_seconds_max",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want %q", joined, want)
		}
	}
	if featureReady(result, "account-usage-ai-performance") || featureReady(result, "table-monitor") || featureReady(result, "system-metrics") {
		t.Fatalf("result = %+v, missing dependencies must fail their features", result)
	}
	if featureReady(result, "stats-overview") {
		t.Fatalf("result = %+v, missing overview summary must fail the feature", result)
	}
}

func TestRunStatsSchemaContractPreflightRedactsDatabaseErrors(t *testing.T) {
	const secret = "postgres://secret:password@db.example.invalid/production"
	contracts := statsSchemaReadContracts()
	querier := completeStatsSchemaContractQuerier(contracts)
	querier.errByTable = map[string]error{
		"usage_stats_daily": errors.New("driver failed: " + secret),
	}
	var out bytes.Buffer

	err := runStatsSchemaContractPreflight(context.Background(), querier, contracts, &out)
	if err == nil || err.Error() != "stats schema contract preflight 未通过" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "driver failed") {
		t.Fatalf("output exposed database details: %s", out.String())
	}
	var result StatsSchemaContractPreflightResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode result: %v; output = %q", decodeErr, out.String())
	}
	if result.Success || !strings.Contains(strings.Join(result.Issues, "\n"), "inspect juhe_stats.usage_stats_daily: unavailable") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunStatsSchemaContractPreflightRedactsInvalidPostgresURL(t *testing.T) {
	const secret = "postgres://secret:password@bad host/juhe_ai"
	var out bytes.Buffer

	err := RunStatsSchemaContractPreflight(context.Background(), secret, &out)
	if err == nil || err.Error() != "stats schema contract preflight 未通过" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), "bad host") {
		t.Fatalf("output exposed postgres URL: %s", out.String())
	}
	var result StatsSchemaContractPreflightResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode result: %v; output = %q", decodeErr, out.String())
	}
	if result.Success || !strings.Contains(strings.Join(result.Issues, "\n"), "open target postgres: unavailable") {
		t.Fatalf("result = %+v", result)
	}
}

func TestStatsSchemaReadContractsTrackGoReadersWithoutClaimingWriterOwnership(t *testing.T) {
	contracts := statsSchemaReadContracts()
	byFeature := map[string]map[string]bool{}
	for _, contract := range contracts {
		relations := map[string]bool{}
		for _, relation := range contract.Relations {
			relations[relation.Name] = true
		}
		byFeature[contract.Name] = relations
	}
	for feature, required := range map[string][]string{
		"account-usage-ai-performance": {
			"usage_scope_range_windows",
			"usage_rank_snapshots",
			"usage_stats_daily",
			"usage_stats_hourly",
			"ai_performance_summary_windows",
		},
		"stats-overview": {
			"usage_overview_summary_windows",
			"usage_stats_daily",
			"usage_overview_trend_windows",
			"usage_model_rank_windows",
			"usage_error_rank_windows",
		},
		"system-metrics": {
			"system_metrics_trend_windows",
			"process_event_loop_trend_windows",
			"process_event_loop_samples",
		},
		"table-monitor": {
			"database_storage_snapshots",
			"table_storage_snapshots",
		},
	} {
		for _, relation := range required {
			if !byFeature[feature][relation] {
				t.Fatalf("feature %q missing relation %q", feature, relation)
			}
		}
	}
	for _, contract := range contracts {
		for _, relation := range contract.Relations {
			if relation.Name == "usage_records" {
				t.Fatal("stats read contract must not depend on raw usage_records")
			}
		}
	}
}

func TestInspectStatsSchemaContractRejectsOverviewTypeAndUniqueKeyDrift(t *testing.T) {
	contracts := statsSchemaReadContracts()
	querier := completeStatsSchemaContractQuerier(contracts)
	querier.columnTypes["usage_overview_summary_windows"]["request_count"] = "text"
	querier.uniqueKeys["usage_model_rank_windows"] = nil

	result, err := inspectStatsSchemaContract(context.Background(), querier, contracts)
	if err != nil {
		t.Fatalf("inspectStatsSchemaContract() error = %v", err)
	}
	if result.Success || featureReady(result, "stats-overview") {
		t.Fatalf("result = %+v, want failed overview contract", result)
	}
	joined := strings.Join(result.Issues, "\n")
	for _, want := range []string{
		"juhe_stats.usage_overview_summary_windows column type mismatch: request_count=text want bigint",
		"juhe_stats.usage_model_rank_windows missing unique key: system_account_id, window_key, rank, provider_code, model",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want %q", joined, want)
		}
	}
}

func TestInspectStatsSchemaContractHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	querier := completeStatsSchemaContractQuerier(statsSchemaReadContracts())

	_, err := inspectStatsSchemaContract(ctx, querier, statsSchemaReadContracts())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(querier.queries) != 0 {
		t.Fatalf("queries = %d, want none after cancellation", len(querier.queries))
	}
}

func contractColumns(contracts []StatsSchemaFeatureContract) map[string][]string {
	columns := map[string][]string{}
	for _, contract := range contracts {
		for _, relation := range contract.Relations {
			columns[relation.Name] = append([]string(nil), relation.Columns...)
		}
	}
	return columns
}

func completeStatsSchemaContractQuerier(contracts []StatsSchemaFeatureContract) *statsSchemaContractQuerierStub {
	return &statsSchemaContractQuerierStub{
		columnsByTable: contractColumns(contracts),
		columnTypes:    contractColumnTypes(contracts),
		uniqueKeys:     contractUniqueKeys(contracts),
		errByTable:     map[string]error{},
	}
}

func contractColumnTypes(contracts []StatsSchemaFeatureContract) map[string]map[string]string {
	types := map[string]map[string]string{}
	for _, contract := range contracts {
		for _, relation := range contract.Relations {
			if types[relation.Name] == nil {
				types[relation.Name] = map[string]string{}
			}
			for column, columnType := range relation.ColumnTypes {
				types[relation.Name][column] = columnType
			}
		}
	}
	return types
}

func contractUniqueKeys(contracts []StatsSchemaFeatureContract) map[string][][]string {
	keys := map[string][][]string{}
	for _, contract := range contracts {
		for _, relation := range contract.Relations {
			if len(relation.UniqueKey) > 0 {
				keys[relation.Name] = append(keys[relation.Name], append([]string(nil), relation.UniqueKey...))
			}
		}
	}
	return keys
}

func withoutColumn(columns []string, unwanted string) []string {
	result := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != unwanted {
			result = append(result, column)
		}
	}
	return result
}

func featureReady(result StatsSchemaContractPreflightResult, name string) bool {
	for _, feature := range result.Features {
		if feature.Name == name {
			return feature.Ready
		}
	}
	return false
}
